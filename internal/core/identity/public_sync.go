package identity

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

var (
	ErrPublicAuthentication  = errors.New("identity: public authentication failed")
	ErrPublicNonceReplay     = errors.New("identity: public nonce replay")
	ErrPublicSessionConflict = errors.New("identity: public session conflict")
	ErrInvalidPublicIdentity = errors.New("identity: invalid public identity")
)

type PublicNonceClaim struct {
	NonceHash string
	ExpiresAt time.Time
}

type PublicNonceUse struct {
	ProjectID, FabricInstanceID, StreamID, CanonicalRef string
	KeyFingerprint                                      string
	Claim                                               PublicNonceClaim
}

type MutationAuthority struct {
	Scope                                             types.ActorScope
	FabricInstanceID, StreamID, WorkspaceID           string
	CanonicalRef, AttachmentRef, IssuerKeyFingerprint string
	SessionID                                         string
}

type PublicAuthorityEvidence struct {
	ProjectID, FabricInstanceID, StreamID, WorkspaceID string
	CanonicalRef, AttachmentRef, IssuerKeyFingerprint  string
	AttachmentSourceVersion, CurrentStreamVersion      int64
	Accepted                                           projectstate.Snapshot
}

type PublicHumanActivation struct {
	ProjectID, FabricInstanceID, StreamID, CanonicalRef string
	SourceVersion                                       int64
	ObservedHuman                                       projectstate.ActorV1
	TransportActor                                      types.ActorEnvelope
	KeyFingerprint                                      string
	PublicKey                                           [ed25519.PublicKeySize]byte
}

type PublicAgentSessionIssue struct {
	ProjectID, FabricInstanceID, StreamID, WorkspaceID string
	CanonicalRef, AttachmentRef, IssuerKeyFingerprint  string
	AgentID, HarnessName, HarnessVersion               string
	ModelName, ModelVersion                            string
	SourceVersion                                      int64
	IssuedAt                                           time.Time
}

type PublicAgentSession struct {
	ProjectID, FabricInstanceID, StreamID, WorkspaceID   string
	CanonicalRef, AttachmentRef, SessionID               string
	IssuerKeyFingerprint, AgentID, AccountableHumanID    string
	SourceVersion                                        int64
	HarnessName, HarnessVersion, ModelName, ModelVersion string
	IssuedAt, ExpiresAt                                  time.Time
	RevokedAt                                            *time.Time
}

func (s *Store) ActivatePublicHumanInTx(ctx context.Context, tx *sql.Tx, in PublicHumanActivation) (types.ActorScope, error) {
	if tx == nil || !validActivation(in) {
		return types.ActorScope{}, ErrInvalidPublicIdentity
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO fabric_public_actor_keys(project_id,fabric_instance_id,stream_id,canonical_ref,key_fingerprint,public_key,actor_kind,human_principal_id,session_id,harness_name,harness_version,model_name,model_version,source_version) VALUES($1,$2,$3,$4,$5,$6,'human',$7,NULL,'','','','',$8) ON CONFLICT(project_id,fabric_instance_id,stream_id,canonical_ref,key_fingerprint) DO NOTHING`, in.ProjectID, in.FabricInstanceID, in.StreamID, in.CanonicalRef, in.KeyFingerprint, in.PublicKey[:], in.ObservedHuman.ID, in.SourceVersion); err != nil {
		return types.ActorScope{}, fmt.Errorf("identity: activate public human: %w", err)
	}
	var publicKey []byte
	var humanID string
	var sourceVersion int64
	var revokedAt sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT public_key,human_principal_id,source_version,revoked_at FROM fabric_public_actor_keys WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND canonical_ref=$4 AND key_fingerprint=$5`, in.ProjectID, in.FabricInstanceID, in.StreamID, in.CanonicalRef, in.KeyFingerprint).Scan(&publicKey, &humanID, &sourceVersion, &revokedAt); err != nil {
		return types.ActorScope{}, fmt.Errorf("identity: read public human activation: %w", err)
	}
	if !bytes.Equal(publicKey, in.PublicKey[:]) || humanID != in.ObservedHuman.ID || sourceVersion != in.SourceVersion || revokedAt.Valid {
		return types.ActorScope{}, ErrPublicAuthentication
	}
	return types.ActorScope{ProjectID: in.ProjectID, Actor: in.TransportActor}, nil
}

func (s *Store) ConsumePublicNonceInTx(ctx context.Context, tx *sql.Tx, in PublicNonceUse) error {
	if tx == nil || !validRoute(in.ProjectID, in.FabricInstanceID, in.StreamID, in.CanonicalRef) || !validFingerprint(in.KeyFingerprint) || !validNonceClaim(in.Claim) {
		return ErrInvalidPublicIdentity
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO public_request_nonces(project_id,fabric_instance_id,stream_id,canonical_ref,key_fingerprint,nonce_hash,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, in.ProjectID, in.FabricInstanceID, in.StreamID, in.CanonicalRef, in.KeyFingerprint, in.Claim.NonceHash, in.Claim.ExpiresAt)
	if err == nil {
		return nil
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && string(pqErr.Code) == "23505" && pqErr.Constraint == "public_request_nonces_pkey" {
		return ErrPublicNonceReplay
	}
	return fmt.Errorf("identity: consume public nonce: %w", err)
}

func (s *Store) IssuePublicAgentSessionInTx(ctx context.Context, tx *sql.Tx, in PublicAgentSessionIssue) (PublicAgentSession, error) {
	if tx == nil || !validSessionIssue(in) {
		return PublicAgentSession{}, ErrInvalidPublicIdentity
	}
	lockKey := strings.Join([]string{in.ProjectID, in.FabricInstanceID, in.AttachmentRef, in.IssuerKeyFingerprint, in.AgentID}, ":")
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, lockKey); err != nil {
		return PublicAgentSession{}, fmt.Errorf("identity: lock public session issue: %w", err)
	}
	var humanID string
	if err := tx.QueryRowContext(ctx, `SELECT human_principal_id FROM fabric_public_actor_keys WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND canonical_ref=$4 AND key_fingerprint=$5 AND source_version=$6 AND revoked_at IS NULL`, in.ProjectID, in.FabricInstanceID, in.StreamID, in.CanonicalRef, in.IssuerKeyFingerprint, in.SourceVersion).Scan(&humanID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PublicAgentSession{}, ErrPublicAuthentication
		}
		return PublicAgentSession{}, fmt.Errorf("identity: resolve public session issuer: %w", err)
	}
	current, err := readActivePublicSession(ctx, tx, in)
	if err == nil {
		if in.IssuedAt.Before(current.ExpiresAt) {
			if current.AccountableHumanID != humanID || current.SourceVersion != in.SourceVersion || current.HarnessName != in.HarnessName || current.HarnessVersion != in.HarnessVersion || current.ModelName != in.ModelName || current.ModelVersion != in.ModelVersion {
				return PublicAgentSession{}, ErrPublicSessionConflict
			}
			return current, nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE fabric_public_agent_sessions SET revoked_at=expires_at WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND canonical_ref=$4 AND workspace_id=$5 AND session_id=$6 AND revoked_at IS NULL`, current.ProjectID, current.FabricInstanceID, current.StreamID, current.CanonicalRef, current.WorkspaceID, current.SessionID); err != nil {
			return PublicAgentSession{}, fmt.Errorf("identity: expire public session: %w", err)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return PublicAgentSession{}, err
	}
	var out PublicAgentSession
	var revoked sql.NullTime
	expiresAt := in.IssuedAt.Add(24 * time.Hour)
	err = tx.QueryRowContext(ctx, `INSERT INTO fabric_public_agent_sessions(project_id,fabric_instance_id,stream_id,canonical_ref,workspace_id,attachment_ref,issuer_key_fingerprint,agent_id,accountable_human_id,source_version,harness_name,harness_version,model_name,model_version,issued_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16) RETURNING project_id,fabric_instance_id,stream_id,workspace_id,canonical_ref,attachment_ref,session_id,issuer_key_fingerprint,agent_id,accountable_human_id,source_version,harness_name,harness_version,model_name,model_version,issued_at,expires_at,revoked_at`, in.ProjectID, in.FabricInstanceID, in.StreamID, in.CanonicalRef, in.WorkspaceID, in.AttachmentRef, in.IssuerKeyFingerprint, in.AgentID, humanID, in.SourceVersion, in.HarnessName, in.HarnessVersion, in.ModelName, in.ModelVersion, in.IssuedAt, expiresAt).Scan(&out.ProjectID, &out.FabricInstanceID, &out.StreamID, &out.WorkspaceID, &out.CanonicalRef, &out.AttachmentRef, &out.SessionID, &out.IssuerKeyFingerprint, &out.AgentID, &out.AccountableHumanID, &out.SourceVersion, &out.HarnessName, &out.HarnessVersion, &out.ModelName, &out.ModelVersion, &out.IssuedAt, &out.ExpiresAt, &revoked)
	if err != nil {
		return PublicAgentSession{}, fmt.Errorf("identity: insert public session: %w", err)
	}
	return out, nil
}

func readActivePublicSession(ctx context.Context, tx *sql.Tx, in PublicAgentSessionIssue) (PublicAgentSession, error) {
	var out PublicAgentSession
	var revoked sql.NullTime
	err := tx.QueryRowContext(ctx, `SELECT project_id,fabric_instance_id,stream_id,workspace_id,canonical_ref,attachment_ref,session_id,issuer_key_fingerprint,agent_id,accountable_human_id,source_version,harness_name,harness_version,model_name,model_version,issued_at,expires_at,revoked_at FROM fabric_public_agent_sessions WHERE project_id=$1 AND fabric_instance_id=$2 AND attachment_ref=$3 AND issuer_key_fingerprint=$4 AND agent_id=$5 AND revoked_at IS NULL FOR UPDATE`, in.ProjectID, in.FabricInstanceID, in.AttachmentRef, in.IssuerKeyFingerprint, in.AgentID).Scan(&out.ProjectID, &out.FabricInstanceID, &out.StreamID, &out.WorkspaceID, &out.CanonicalRef, &out.AttachmentRef, &out.SessionID, &out.IssuerKeyFingerprint, &out.AgentID, &out.AccountableHumanID, &out.SourceVersion, &out.HarnessName, &out.HarnessVersion, &out.ModelName, &out.ModelVersion, &out.IssuedAt, &out.ExpiresAt, &revoked)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PublicAgentSession{}, sql.ErrNoRows
		}
		return PublicAgentSession{}, fmt.Errorf("identity: read active public session: %w", err)
	}
	return out, nil
}

func (s *Store) RevalidateMutationAuthorityInTx(ctx context.Context, tx *sql.Tx, authority MutationAuthority, evidence PublicAuthorityEvidence) (types.ActorScope, error) {
	if tx == nil || !validEvidence(authority, evidence) {
		return types.ActorScope{}, ErrPublicAuthentication
	}
	at := authority.Scope.Actor.OccurredAt
	switch authority.Scope.Actor.ActorKind {
	case types.ActorHuman:
		var humanID string
		var publicKey []byte
		err := tx.QueryRowContext(ctx, `SELECT human_principal_id,public_key FROM fabric_public_actor_keys WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND canonical_ref=$4 AND key_fingerprint=$5 AND source_version=$6 AND revoked_at IS NULL`, evidence.ProjectID, evidence.FabricInstanceID, evidence.StreamID, evidence.CanonicalRef, evidence.IssuerKeyFingerprint, evidence.AttachmentSourceVersion).Scan(&humanID, &publicKey)
		if err != nil || humanID != authority.Scope.Actor.HumanPrincipalID || !snapshotActorHasKey(evidence.Accepted, humanID, types.ActorHuman, publicKey) {
			return types.ActorScope{}, ErrPublicAuthentication
		}
		return types.ActorScope{ProjectID: evidence.ProjectID, Actor: types.ActorEnvelope{ActorKind: types.ActorHuman, HumanPrincipalID: humanID, Assurance: types.AssurancePublicKeyContinuity, OccurredAt: at}}, nil
	case types.ActorAgent:
		var agentID, humanID, harnessName, harnessVersion, modelName, modelVersion string
		var sourceVersion int64
		err := tx.QueryRowContext(ctx, `SELECT s.agent_id,s.accountable_human_id,s.source_version,s.harness_name,s.harness_version,s.model_name,s.model_version FROM fabric_public_agent_sessions s JOIN fabric_public_actor_keys k ON k.project_id=s.project_id AND k.fabric_instance_id=s.fabric_instance_id AND k.stream_id=s.stream_id AND k.canonical_ref=s.canonical_ref AND k.key_fingerprint=s.issuer_key_fingerprint AND k.human_principal_id=s.accountable_human_id AND k.source_version=s.source_version AND k.revoked_at IS NULL WHERE s.project_id=$1 AND s.fabric_instance_id=$2 AND s.stream_id=$3 AND s.workspace_id=$4 AND s.canonical_ref=$5 AND s.attachment_ref=$6 AND s.issuer_key_fingerprint=$7 AND s.session_id=$8 AND s.revoked_at IS NULL AND s.expires_at>transaction_timestamp()`, evidence.ProjectID, evidence.FabricInstanceID, evidence.StreamID, evidence.WorkspaceID, evidence.CanonicalRef, evidence.AttachmentRef, evidence.IssuerKeyFingerprint, authority.SessionID).Scan(&agentID, &humanID, &sourceVersion, &harnessName, &harnessVersion, &modelName, &modelVersion)
		if err != nil || sourceVersion != evidence.AttachmentSourceVersion || agentID != authority.Scope.Actor.AgentID || humanID != authority.Scope.Actor.AccountableHumanID || !snapshotActorLive(evidence.Accepted, agentID, types.ActorAgent) || !snapshotActorLive(evidence.Accepted, humanID, types.ActorHuman) {
			return types.ActorScope{}, ErrPublicAuthentication
		}
		actor := types.ActorEnvelope{ActorKind: types.ActorAgent, AgentID: agentID, AccountableHumanID: humanID, SessionID: authority.SessionID, HarnessName: harnessName, HarnessVersion: harnessVersion, ModelName: modelName, ModelVersion: modelVersion, Assurance: types.AssurancePublicKeyContinuity, OccurredAt: at}
		if err := actor.Validate(); err != nil {
			return types.ActorScope{}, ErrPublicAuthentication
		}
		return types.ActorScope{ProjectID: evidence.ProjectID, Actor: actor}, nil
	default:
		return types.ActorScope{}, ErrPublicAuthentication
	}
}

func (s *Store) ResolveHistoricalPublicSessionActorInTx(ctx context.Context, tx *sql.Tx, evidence PublicAuthorityEvidence, sessionID string, occurredAt time.Time) (types.ActorEnvelope, error) {
	if tx == nil || !validHistoricalSessionEvidence(evidence) || !types.CanonicalUUID(sessionID) || occurredAt.IsZero() || occurredAt.Location() != time.UTC {
		return types.ActorEnvelope{}, ErrInvalidPublicIdentity
	}
	var agentID, humanID, harnessName, harnessVersion, modelName, modelVersion string
	var issuedAt, expiresAt time.Time
	var revokedAt sql.NullTime
	err := tx.QueryRowContext(ctx, `SELECT agent_id,accountable_human_id,harness_name,harness_version,model_name,model_version,issued_at,expires_at,revoked_at FROM fabric_public_agent_sessions WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND workspace_id=$4 AND canonical_ref=$5 AND attachment_ref=$6 AND issuer_key_fingerprint=$7 AND source_version=$8 AND session_id=$9`, evidence.ProjectID, evidence.FabricInstanceID, evidence.StreamID, evidence.WorkspaceID, evidence.CanonicalRef, evidence.AttachmentRef, evidence.IssuerKeyFingerprint, evidence.AttachmentSourceVersion, sessionID).Scan(&agentID, &humanID, &harnessName, &harnessVersion, &modelName, &modelVersion, &issuedAt, &expiresAt, &revokedAt)
	if err != nil {
		return types.ActorEnvelope{}, ErrPublicAuthentication
	}
	end := expiresAt
	if revokedAt.Valid && revokedAt.Time.Before(end) {
		end = revokedAt.Time
	}
	if occurredAt.Before(issuedAt) || !occurredAt.Before(end) {
		return types.ActorEnvelope{}, ErrPublicAuthentication
	}
	actor := types.ActorEnvelope{ActorKind: types.ActorAgent, AgentID: agentID, AccountableHumanID: humanID, SessionID: sessionID, HarnessName: harnessName, HarnessVersion: harnessVersion, ModelName: modelName, ModelVersion: modelVersion, Assurance: types.AssurancePublicKeyContinuity, OccurredAt: occurredAt}
	if err := actor.Validate(); err != nil {
		return types.ActorEnvelope{}, ErrPublicAuthentication
	}
	return actor, nil
}

func validHistoricalSessionEvidence(e PublicAuthorityEvidence) bool {
	return validRoute(e.ProjectID, e.FabricInstanceID, e.StreamID, e.CanonicalRef) &&
		types.CanonicalUUID(e.WorkspaceID) && types.CanonicalUUID(e.AttachmentRef) &&
		validFingerprint(e.IssuerKeyFingerprint) && e.AttachmentSourceVersion >= 0 &&
		e.CurrentStreamVersion >= e.AttachmentSourceVersion
}

func validActivation(in PublicHumanActivation) bool {
	if !validRoute(in.ProjectID, in.FabricInstanceID, in.StreamID, in.CanonicalRef) || in.SourceVersion < 0 || !validFingerprint(in.KeyFingerprint) || in.ObservedHuman.ActorKind != types.ActorHuman || in.ObservedHuman.ID != in.TransportActor.HumanPrincipalID || in.TransportActor.ActorKind != types.ActorHuman || in.TransportActor.Assurance != types.AssurancePublicKeyContinuity || in.TransportActor.Validate() != nil {
		return false
	}
	matches := 0
	for _, key := range in.ObservedHuman.PublicKeys {
		decoded, err := base64.StdEncoding.DecodeString(key.PublicKeyBase64)
		if err == nil && key.Algorithm == "ed25519" && bytes.Equal(decoded, in.PublicKey[:]) {
			matches++
		}
	}
	return matches == 1
}

func validSessionIssue(in PublicAgentSessionIssue) bool {
	return validRoute(in.ProjectID, in.FabricInstanceID, in.StreamID, in.CanonicalRef) && types.CanonicalUUID(in.WorkspaceID) && types.CanonicalUUID(in.AttachmentRef) && validFingerprint(in.IssuerKeyFingerprint) && types.CanonicalUUID(in.AgentID) && in.SourceVersion >= 0 && in.IssuedAt.Location() == time.UTC && !in.IssuedAt.IsZero() && len(in.HarnessName) >= 1 && len(in.HarnessName) <= 128 && len(in.HarnessVersion) >= 1 && len(in.HarnessVersion) <= 128 && len(in.ModelName) <= 128 && len(in.ModelVersion) <= 128 && (in.ModelName == "") == (in.ModelVersion == "")
}

func validEvidence(a MutationAuthority, e PublicAuthorityEvidence) bool {
	return a.Scope.Validate() == nil && a.Scope.ProjectID == e.ProjectID && a.FabricInstanceID == e.FabricInstanceID && a.StreamID == e.StreamID && a.WorkspaceID == e.WorkspaceID && a.CanonicalRef == e.CanonicalRef && a.AttachmentRef == e.AttachmentRef && a.IssuerKeyFingerprint == e.IssuerKeyFingerprint && a.SessionID == a.Scope.Actor.SessionID && e.AttachmentSourceVersion >= 0 && e.CurrentStreamVersion >= e.AttachmentSourceVersion
}

func validRoute(projectID, fabricID, streamID, canonicalRef string) bool {
	return types.CanonicalUUID(projectID) && types.CanonicalUUID(fabricID) && types.CanonicalUUID(streamID) && strings.HasPrefix(canonicalRef, "refs/heads/")
}
func validFingerprint(value string) bool {
	return len(value) == 71 && strings.HasPrefix(value, "sha256:") && strings.Trim(value[7:], "0123456789abcdef") == ""
}
func validNonceClaim(claim PublicNonceClaim) bool {
	return len(claim.NonceHash) == 64 && strings.Trim(claim.NonceHash, "0123456789abcdef") == "" && !claim.ExpiresAt.IsZero() && claim.ExpiresAt.Location() == time.UTC
}

func snapshotActorLive(snapshot projectstate.Snapshot, id string, kind types.ActorKind) bool {
	record, ok := snapshot.Actors[id]
	return ok && record.Value != nil && record.Tombstone == nil && record.Value.ID == id && record.Value.ActorKind == kind
}
func snapshotActorHasKey(snapshot projectstate.Snapshot, id string, kind types.ActorKind, key []byte) bool {
	if !snapshotActorLive(snapshot, id, kind) {
		return false
	}
	for _, candidate := range snapshot.Actors[id].Value.PublicKeys {
		decoded, err := base64.StdEncoding.DecodeString(candidate.PublicKeyBase64)
		if err == nil && candidate.Algorithm == "ed25519" && bytes.Equal(decoded, key) {
			return true
		}
	}
	return false
}
