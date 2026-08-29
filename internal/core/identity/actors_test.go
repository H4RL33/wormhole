package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
)

func TestRecordActorActionTypedAuditCommitAndRollback(t *testing.T) {
	f := newPublicSyncFixture(t)
	payload := []byte(`{"action":"checkpoint","version":2}`)
	now := time.Now().UTC().Truncate(time.Microsecond)
	scope := types.ActorScope{ProjectID: f.projectID, Actor: types.ActorEnvelope{
		ActorKind: types.ActorHuman, HumanPrincipalID: f.humanID,
		Assurance: types.AssurancePublicKeyContinuity, OccurredAt: now,
	}}
	tx, err := f.store.BeginProjectTx(context.Background(), f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := f.store.RecordActorActionInTx(context.Background(), tx, scope, "sync.attach", payload)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.db.QueryRow(`SELECT count(*) FROM audit_log WHERE id=$1`, entry.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 || entry.AgentID != "" || entry.HumanPrincipalID != f.humanID || entry.AccountableHumanID != "" || entry.SessionID != "" || !bytes.Equal(entry.CanonicalPayloadJSON, payload) {
		t.Fatalf("unexpected human entry: %+v", entry)
	}
	digest := sha256.Sum256(payload)
	actorJSON, err := json.Marshal(scope.Actor)
	if err != nil {
		t.Fatal(err)
	}
	if entry.RequestDigest != "sha256:"+hex.EncodeToString(digest[:]) || !bytes.Equal(entry.ActorEnvelopeJSON, actorJSON) {
		t.Fatalf("audit proof mismatch: %+v", entry)
	}

	agentScope := scope
	agentScope.Actor = types.ActorEnvelope{ActorKind: types.ActorAgent, AgentID: f.agentID, AccountableHumanID: f.humanID, SessionID: "77777777-7777-4777-8777-777777777231", HarnessName: "codex", HarnessVersion: "1", Assurance: types.AssurancePublicKeyContinuity, OccurredAt: now}
	tx, err = f.store.BeginProjectTx(context.Background(), f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.RecordActorActionInTx(context.Background(), tx, agentScope, "sync.push", payload); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT count(*) FROM audit_log WHERE project_id=$1 AND action='sync.push'`, f.projectID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rollback persisted %d typed rows", count)
	}
}

func TestRecordActorActionInTxRejectsInvalidInputsBeforeSQL(t *testing.T) {
	var s *Store
	if _, err := s.RecordActorActionInTx(context.Background(), nil, types.ActorScope{}, "x", []byte(`{}`)); err == nil {
		t.Fatal("nil store/transaction accepted")
	}
}
