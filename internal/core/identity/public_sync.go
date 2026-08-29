package identity

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
	"github.com/lib/pq"
	"strings"
	"time"
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
	ProjectID, FabricInstanceID, StreamID, CanonicalRef, KeyFingerprint string
	Claim                                                               PublicNonceClaim
}
type MutationAuthority struct {
	Scope                                                                                                 types.ActorScope
	FabricInstanceID, StreamID, WorkspaceID, CanonicalRef, AttachmentRef, IssuerKeyFingerprint, SessionID string
}
type PublicAuthorityEvidence struct {
	ProjectID, FabricInstanceID, StreamID, WorkspaceID, CanonicalRef, AttachmentRef, IssuerKeyFingerprint string
	AttachmentSourceVersion, CurrentStreamVersion                                                         int64
	Accepted                                                                                              projectstate.Snapshot
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
	ProjectID, FabricInstanceID, StreamID, WorkspaceID, CanonicalRef, AttachmentRef, IssuerKeyFingerprint, AgentID, HarnessName, HarnessVersion, ModelName, ModelVersion string
	SourceVersion                                                                                                                                                        int64
	IssuedAt                                                                                                                                                             time.Time
}
type PublicAgentSession struct {
	ProjectID, FabricInstanceID, StreamID, WorkspaceID, CanonicalRef, AttachmentRef, SessionID, IssuerKeyFingerprint, AgentID, AccountableHumanID string
	SourceVersion                                                                                                                                 int64
	HarnessName, HarnessVersion, ModelName, ModelVersion                                                                                          string
	IssuedAt, ExpiresAt                                                                                                                           time.Time
	RevokedAt                                                                                                                                     *time.Time
}

func (s *Store) ActivatePublicHumanInTx(ctx context.Context, tx *sql.Tx, in PublicHumanActivation) (types.ActorScope, error) {
	if tx == nil || !validActivation(in) {
		return types.ActorScope{}, ErrInvalidPublicIdentity
	}
	_, e := tx.ExecContext(ctx, `INSERT INTO fabric_public_actor_keys(project_id,fabric_instance_id,stream_id,canonical_ref,key_fingerprint,public_key,actor_kind,human_principal_id,session_id,harness_name,harness_version,model_name,model_version,source_version) VALUES($1,$2,$3,$4,$5,$6,'human',$7,NULL,'','','','',$8) ON CONFLICT(project_id,fabric_instance_id,stream_id,canonical_ref,key_fingerprint) DO NOTHING`, in.ProjectID, in.FabricInstanceID, in.StreamID, in.CanonicalRef, in.KeyFingerprint, in.PublicKey[:], in.ObservedHuman.ID, in.SourceVersion)
	if e != nil {
		return types.ActorScope{}, fmt.Errorf("identity: activate public human: %w", e)
	}
	var k []byte
	var id string
	var ver int64
	var rev sql.NullTime
	e = tx.QueryRowContext(ctx, `SELECT public_key,human_principal_id,source_version,revoked_at FROM fabric_public_actor_keys WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND canonical_ref=$4 AND key_fingerprint=$5`, in.ProjectID, in.FabricInstanceID, in.StreamID, in.CanonicalRef, in.KeyFingerprint).Scan(&k, &id, &ver, &rev)
	if e != nil {
		return types.ActorScope{}, e
	}
	if !bytes.Equal(k, in.PublicKey[:]) || id != in.ObservedHuman.ID || ver != in.SourceVersion || rev.Valid {
		return types.ActorScope{}, ErrPublicAuthentication
	}
	return types.ActorScope{ProjectID: in.ProjectID, Actor: in.TransportActor}, nil
}
func (s *Store) ConsumePublicNonceInTx(ctx context.Context, tx *sql.Tx, in PublicNonceUse) error {
	if tx == nil || !validRoute(in.ProjectID, in.FabricInstanceID, in.StreamID, in.CanonicalRef) || !validFingerprint(in.KeyFingerprint) || !validNonceClaim(in.Claim) {
		return ErrInvalidPublicIdentity
	}
	_, e := tx.ExecContext(ctx, `INSERT INTO public_request_nonces(project_id,fabric_instance_id,stream_id,canonical_ref,key_fingerprint,nonce_hash,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, in.ProjectID, in.FabricInstanceID, in.StreamID, in.CanonicalRef, in.KeyFingerprint, in.Claim.NonceHash, in.Claim.ExpiresAt)
	if e == nil {
		return nil
	}
	var pe *pq.Error
	if errors.As(e, &pe) && pe.Code == "23505" && pe.Constraint == "public_request_nonces_pkey" {
		return ErrPublicNonceReplay
	}
	return e
}
func validRoute(p, f, s, r string) bool {
	return types.CanonicalUUID(p) && types.CanonicalUUID(f) && types.CanonicalUUID(s) && strings.HasPrefix(r, "refs/heads/")
}
func validFingerprint(v string) bool {
	return len(v) == 71 && strings.HasPrefix(v, "sha256:") && strings.Trim(v[7:], "0123456789abcdef") == ""
}
func validNonceClaim(c PublicNonceClaim) bool {
	return len(c.NonceHash) == 64 && strings.Trim(c.NonceHash, "0123456789abcdef") == "" && !c.ExpiresAt.IsZero() && c.ExpiresAt.Location() == time.UTC
}
func validActivation(in PublicHumanActivation) bool {
	if !validRoute(in.ProjectID, in.FabricInstanceID, in.StreamID, in.CanonicalRef) || in.SourceVersion < 0 || !validFingerprint(in.KeyFingerprint) || in.ObservedHuman.ActorKind != types.ActorHuman || in.ObservedHuman.ID != in.TransportActor.HumanPrincipalID || in.TransportActor.ActorKind != types.ActorHuman || in.TransportActor.Assurance != types.AssurancePublicKeyContinuity || in.TransportActor.Validate() != nil {
		return false
	}
	n := 0
	for _, k := range in.ObservedHuman.PublicKeys {
		d, e := base64.StdEncoding.DecodeString(k.PublicKeyBase64)
		if e == nil && k.Algorithm == "ed25519" && bytes.Equal(d, in.PublicKey[:]) {
			n++
		}
	}
	return n == 1
}

func (s *Store) IssuePublicAgentSessionInTx(ctx context.Context, tx *sql.Tx, in PublicAgentSessionIssue) (PublicAgentSession, error) {
	if tx == nil || !validRoute(in.ProjectID, in.FabricInstanceID, in.StreamID, in.CanonicalRef) || !types.CanonicalUUID(in.WorkspaceID) || !types.CanonicalUUID(in.AttachmentRef) || !validFingerprint(in.IssuerKeyFingerprint) || !types.CanonicalUUID(in.AgentID) {
		return PublicAgentSession{}, ErrInvalidPublicIdentity
	}
	_, e := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, strings.Join([]string{in.ProjectID, in.FabricInstanceID, in.AttachmentRef, in.IssuerKeyFingerprint, in.AgentID}, ":"))
	if e != nil {
		return PublicAgentSession{}, e
	}
	var human string
	e = tx.QueryRowContext(ctx, `SELECT human_principal_id FROM fabric_public_actor_keys WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND canonical_ref=$4 AND key_fingerprint=$5 AND source_version=$6 AND revoked_at IS NULL`, in.ProjectID, in.FabricInstanceID, in.StreamID, in.CanonicalRef, in.IssuerKeyFingerprint, in.SourceVersion).Scan(&human)
	if e != nil {
		return PublicAgentSession{}, ErrPublicAuthentication
	}
	var o PublicAgentSession
	o.ProjectID = in.ProjectID
	o.FabricInstanceID = in.FabricInstanceID
	o.StreamID = in.StreamID
	o.WorkspaceID = in.WorkspaceID
	o.CanonicalRef = in.CanonicalRef
	o.AttachmentRef = in.AttachmentRef
	o.IssuerKeyFingerprint = in.IssuerKeyFingerprint
	o.AgentID = in.AgentID
	o.AccountableHumanID = human
	o.SourceVersion = in.SourceVersion
	o.HarnessName = in.HarnessName
	o.HarnessVersion = in.HarnessVersion
	o.ModelName = in.ModelName
	o.ModelVersion = in.ModelVersion
	o.IssuedAt = in.IssuedAt
	o.ExpiresAt = in.IssuedAt.Add(24 * time.Hour)
	e = tx.QueryRowContext(ctx, `INSERT INTO fabric_public_agent_sessions(project_id,fabric_instance_id,stream_id,canonical_ref,workspace_id,attachment_ref,issuer_key_fingerprint,agent_id,accountable_human_id,source_version,harness_name,harness_version,model_name,model_version,issued_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16) RETURNING session_id`, in.ProjectID, in.FabricInstanceID, in.StreamID, in.CanonicalRef, in.WorkspaceID, in.AttachmentRef, in.IssuerKeyFingerprint, in.AgentID, human, in.SourceVersion, in.HarnessName, in.HarnessVersion, in.ModelName, in.ModelVersion, in.IssuedAt, o.ExpiresAt).Scan(&o.SessionID)
	if e != nil {
		return PublicAgentSession{}, e
	}
	return o, nil
}
func (s *Store) RevalidateMutationAuthorityInTx(ctx context.Context, tx *sql.Tx, a MutationAuthority, e PublicAuthorityEvidence) (types.ActorScope, error) {
	if tx == nil || a.Scope.ProjectID != e.ProjectID || a.Scope.Actor.Validate() != nil {
		return types.ActorScope{}, ErrPublicAuthentication
	}
	return a.Scope, nil
}
func (s *Store) ResolveHistoricalPublicSessionActorInTx(ctx context.Context, tx *sql.Tx, fabric, session string, at time.Time) (types.ActorEnvelope, error) {
	if tx == nil {
		return types.ActorEnvelope{}, ErrInvalidPublicIdentity
	}
	var agent, human, hv, hver, mv, mver string
	var issued, expires time.Time
	var revoked sql.NullTime
	e := tx.QueryRowContext(ctx, `SELECT agent_id,accountable_human_id,harness_name,harness_version,model_name,model_version,issued_at,expires_at,revoked_at FROM fabric_public_agent_sessions WHERE fabric_instance_id=$1 AND session_id=$2`, fabric, session).Scan(&agent, &human, &hv, &hver, &mv, &mver, &issued, &expires, &revoked)
	if e != nil || at.Before(issued) || !at.Before(expires) {
		return types.ActorEnvelope{}, ErrPublicAuthentication
	}
	return types.ActorEnvelope{ActorKind: types.ActorAgent, AgentID: agent, AccountableHumanID: human, SessionID: session, HarnessName: hv, HarnessVersion: hver, ModelName: mv, ModelVersion: mver, Assurance: types.AssurancePublicKeyContinuity, OccurredAt: at}, nil
}
