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
	tx := f.begin(t)
	var err error
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
	tx = f.begin(t)
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

func TestRecordActorActionRejectsMalformedInputsWithoutRows(t *testing.T) {
	f := newPublicSyncFixture(t)
	ctx := context.Background()
	cases := map[string][2]string{
		"scope": {"sync.x", `{}`}, "action": {" sync.x", `{}`},
		"duplicate": {"sync.x", `{"a":1,"a":1}`}, "trailing": {"sync.x", `{} {}`},
	}
	for name, pair := range cases {
		action, payload := pair[0], pair[1]
		t.Run(name, func(t *testing.T) {
			tx := f.begin(t)
			before := countAudit(t, f)
			scope := types.ActorScope{ProjectID: f.projectID, Actor: types.ActorEnvelope{ActorKind: types.ActorHuman, HumanPrincipalID: f.humanID, Assurance: types.AssurancePublicKeyContinuity, OccurredAt: f.now}}
			if name == "scope" {
				scope.ProjectID = ""
			}
			if _, err := f.store.RecordActorActionInTx(ctx, tx, scope, action, []byte(payload)); err == nil {
				t.Fatal("accepted malformed input")
			}
			_ = tx.Rollback()
			if got := countAudit(t, f); got != before {
				t.Fatalf("row count changed: %d -> %d", before, got)
			}
		})
	}
}

func TestTypedAuditImmutableAndLegacyRemainUsable(t *testing.T) {
	f := newPublicSyncFixture(t)
	ctx := context.Background()
	scope := types.ActorScope{ProjectID: f.projectID, Actor: types.ActorEnvelope{ActorKind: types.ActorAgent, AgentID: f.agentID, AccountableHumanID: f.humanID, SessionID: "77777777-7777-4777-8777-777777777231", HarnessName: "codex", HarnessVersion: "1", Assurance: types.AssurancePublicKeyContinuity, OccurredAt: f.now}}
	tx := f.begin(t)
	e, err := f.store.RecordActorActionInTx(ctx, tx, scope, "sync.push", []byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`UPDATE audit_log SET action='tampered' WHERE id=$1`, `DELETE FROM audit_log WHERE id=$1`} {
		tx = f.begin(t)
		_, err = tx.Exec(q, e.ID)
		if err == nil {
			t.Fatal("immutable mutation accepted")
		}
		_ = tx.Rollback()
	}
	if _, err = f.store.RecordAction(ctx, f.agentID, f.projectID, "legacy.action"); err != nil {
		t.Fatal(err)
	}
	entries, err := f.store.ListAuditTrail(ctx, f.agentID, f.projectID)
	if err != nil || len(entries) == 0 {
		t.Fatalf("legacy list: %v %+v", err, entries)
	}
	var human, agent, accountable, session bool
	if err := f.db.QueryRow(`SELECT human_principal_id IS NULL, agent_id IS NULL, accountable_human_id IS NULL, session_id IS NULL FROM audit_log WHERE id=$1`, e.ID).Scan(&human, &agent, &accountable, &session); err != nil {
		t.Fatal(err)
	}
	if !human || agent || accountable || session {
		t.Fatalf("agent nullable shape incorrect: %v %v %v %v", human, agent, accountable, session)
	}
	tx = f.begin(t)
	var visible int
	if err := tx.QueryRow(`SELECT count(*) FROM audit_log WHERE project_id=$1`, "00000000-0000-4000-8000-000000000000").Scan(&visible); err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback()
	if visible != 0 {
		t.Fatalf("cross-project audit visible: %d", visible)
	}
}

func countAudit(t *testing.T, f publicSyncFixture) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow(`SELECT count(*) FROM audit_log WHERE project_id=$1`, f.projectID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
