package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/H4RL33/wormhole/internal/types"
)

// RecordActorActionInTx appends an immutable, typed audit row. The caller
// retains ownership of tx, including commit and rollback.
func (s *Store) RecordActorActionInTx(ctx context.Context, tx *sql.Tx, scope types.ActorScope, action string, canonicalPayload []byte) (AuditEntry, error) {
	if s == nil || tx == nil {
		return AuditEntry{}, errors.New("identity: nil store or transaction")
	}
	if err := scope.Validate(); err != nil {
		return AuditEntry{}, fmt.Errorf("identity: invalid actor scope: %w", err)
	}
	if action == "" || len(action) > 256 || strings.TrimSpace(action) != action || strings.ContainsAny(action, "\r\n") {
		return AuditEntry{}, errors.New("identity: invalid audit action")
	}
	var decoded any
	if len(canonicalPayload) == 0 || json.Unmarshal(canonicalPayload, &decoded) != nil {
		return AuditEntry{}, errors.New("identity: invalid canonical payload")
	}
	canonical, err := json.Marshal(decoded)
	if err != nil || !bytes.Equal(canonical, canonicalPayload) {
		return AuditEntry{}, errors.New("identity: non-canonical payload")
	}
	envelope, err := json.Marshal(scope.Actor)
	if err != nil {
		return AuditEntry{}, fmt.Errorf("identity: encode actor: %w", err)
	}
	digest := sha256.Sum256(canonicalPayload)
	requestDigest := "sha256:" + hex.EncodeToString(digest[:])
	actor := scope.Actor
	var agentID, humanID, accountable, session any
	if actor.ActorKind == types.ActorHuman {
		humanID = actor.HumanPrincipalID
	} else {
		agentID, accountable, session = actor.AgentID, actor.AccountableHumanID, actor.SessionID
	}
	var e AuditEntry
	var agentIDRead, humanIDRead, accountableRead, sessionRead sql.NullString
	err = tx.QueryRowContext(ctx, `INSERT INTO audit_log(agent_id,project_id,action,actor_kind,human_principal_id,accountable_human_id,session_id,harness_name,harness_version,model_name,model_version,assurance,occurred_at,actor_envelope_json,canonical_payload_json,request_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16) RETURNING id,agent_id,project_id,action,created_at,seq,actor_kind,human_principal_id,accountable_human_id,session_id,harness_name,harness_version,model_name,model_version,assurance,occurred_at,actor_envelope_json,canonical_payload_json,request_digest`, agentID, scope.ProjectID, action, actor.ActorKind, humanID, accountable, session, actor.HarnessName, actor.HarnessVersion, actor.ModelName, actor.ModelVersion, actor.Assurance, actor.OccurredAt, envelope, canonicalPayload, requestDigest).Scan(&e.ID, &agentIDRead, &e.ProjectID, &e.Action, &e.CreatedAt, &e.Seq, &e.ActorKind, &humanIDRead, &accountableRead, &sessionRead, &e.HarnessName, &e.HarnessVersion, &e.ModelName, &e.ModelVersion, &e.Assurance, &e.OccurredAt, &e.ActorEnvelopeJSON, &e.CanonicalPayloadJSON, &e.RequestDigest)
	if err != nil {
		return AuditEntry{}, fmt.Errorf("identity: insert typed audit entry: %w", err)
	}
	e.AgentID, e.HumanPrincipalID, e.AccountableHumanID, e.SessionID = agentIDRead.String, humanIDRead.String, accountableRead.String, sessionRead.String
	if !bytes.Equal(e.ActorEnvelopeJSON, envelope) || e.ActorKind != string(actor.ActorKind) ||
		e.HarnessName != actor.HarnessName || e.HarnessVersion != actor.HarnessVersion ||
		e.ModelName != actor.ModelName || e.ModelVersion != actor.ModelVersion ||
		e.Assurance != string(actor.Assurance) || !e.OccurredAt.Equal(actor.OccurredAt) ||
		e.ProjectID != scope.ProjectID || e.Action != action ||
		!bytes.Equal(e.CanonicalPayloadJSON, canonicalPayload) || e.RequestDigest != requestDigest ||
		(actor.ActorKind == types.ActorHuman && (agentIDRead.Valid || e.HumanPrincipalID != actor.HumanPrincipalID || accountableRead.Valid || sessionRead.Valid)) ||
		(actor.ActorKind == types.ActorAgent && (humanIDRead.Valid || e.AgentID != actor.AgentID || e.AccountableHumanID != actor.AccountableHumanID || e.SessionID != actor.SessionID)) {
		return AuditEntry{}, errors.New("identity: typed audit readback mismatch")
	}
	return e, nil
}
