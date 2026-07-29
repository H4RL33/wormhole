package localstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestWorkspaceTransitionReceiptTransactionRoundTrip(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	receipt := validWorkspaceTransitionReceipt(t, "00000000-0000-1000-8000-000000000031", "stash", "clean")

	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		if err := tx.InsertTransitionReceipt(context.Background(), receipt); err != nil {
			return err
		}
		got, err := tx.TransitionReceipt(context.Background(), receipt.RequestID)
		if err != nil {
			return err
		}
		assertWorkspaceTransitionReceipt(t, got, receipt)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceTransitionReceiptRepoReadbackRestartAndNonAliasing(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	receipt := validWorkspaceTransitionReceipt(t, "00000000-0000-2000-8000-000000000031", "restore", "conflicted")
	wantResult := bytes.Clone(receipt.ResultJSON)

	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.InsertTransitionReceipt(context.Background(), receipt)
	}); err != nil {
		t.Fatal(err)
	}
	receipt.ResultJSON[0] = '['

	var actorJSON, resultJSON []byte
	if err := store.DB().QueryRow(`
		SELECT actor_json, result_json FROM workspace_transition_receipts
		WHERE project_id=? AND workspace_id=? AND request_id=?
	`, binding.Scope.ProjectID, binding.Scope.WorkspaceID, receipt.RequestID).Scan(&actorJSON, &resultJSON); err != nil {
		t.Fatal(err)
	}
	wantActorJSON, err := state.CanonicalJSON(receipt.Actor)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actorJSON, wantActorJSON) || !bytes.Equal(resultJSON, wantResult) {
		t.Fatalf("persisted actor/result bytes=(%q,%q), want (%q,%q)", actorJSON, resultJSON, wantActorJSON, wantResult)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo = NewWorkspaceRepo(store.DB())
	receipt.ResultJSON = wantResult
	got, err := repo.TransitionReceipt(context.Background(), binding.Scope, receipt.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkspaceTransitionReceipt(t, got, receipt)
	got.ResultJSON[0] = '['
	again, err := repo.TransitionReceipt(context.Background(), binding.Scope, receipt.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again.ResultJSON, wantResult) {
		t.Fatalf("mutating read result aliased persisted bytes: got %q, want %q", again.ResultJSON, wantResult)
	}
}

func TestWorkspaceTransitionReceiptAcceptsAllActionsAndOutcomes(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	requestNumber := 40
	for _, action := range []string{"stash", "restore", "discard"} {
		for _, outcome := range []string{"clean", "conflicted"} {
			requestID := fmt.Sprintf("00000000-0000-1000-8000-%012d", requestNumber)
			requestNumber++
			receipt := validWorkspaceTransitionReceipt(t, requestID, action, outcome)
			if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				return tx.InsertTransitionReceipt(context.Background(), receipt)
			}); err != nil {
				t.Fatalf("InsertTransitionReceipt(%s,%s): %v", action, outcome, err)
			}
			got, err := repo.TransitionReceipt(context.Background(), binding.Scope, requestID)
			if err != nil {
				t.Fatalf("TransitionReceipt(%s,%s): %v", action, outcome, err)
			}
			assertWorkspaceTransitionReceipt(t, got, receipt)
		}
	}
}

func TestWorkspaceTransitionReceiptAbsentAndInvalidReceivers(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	requestID := "00000000-0000-1000-8000-000000000031"
	got, err := repo.TransitionReceipt(context.Background(), binding.Scope, requestID)
	if err != nil || got != nil {
		t.Fatalf("absent TransitionReceipt=(%+v,%v), want (nil,nil)", got, err)
	}

	for _, test := range []struct {
		name  string
		repo  *WorkspaceRepo
		scope types.WorkspaceScope
	}{
		{name: "nil repo", repo: nil, scope: binding.Scope},
		{name: "nil database", repo: &WorkspaceRepo{}, scope: binding.Scope},
		{name: "invalid project", repo: repo, scope: types.WorkspaceScope{ProjectID: "BAD", WorkspaceID: binding.Scope.WorkspaceID}},
		{name: "invalid workspace", repo: repo, scope: types.WorkspaceScope{ProjectID: binding.Scope.ProjectID, WorkspaceID: "BAD"}},
		{name: "unregistered scope", repo: repo, scope: types.WorkspaceScope{ProjectID: "00000000-0000-4000-8000-000000000099", WorkspaceID: "00000000-0000-4000-8000-000000000098"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.repo.TransitionReceipt(context.Background(), test.scope, requestID)
			if !errors.Is(err, ErrNotFound) || got != nil {
				t.Fatalf("TransitionReceipt=(%+v,%v), want (nil,ErrNotFound)", got, err)
			}
		})
	}

	var nilTx *WorkspaceMutationTx
	if got, err := nilTx.TransitionReceipt(context.Background(), requestID); !errors.Is(err, ErrNotFound) || got != nil {
		t.Fatalf("nil tx TransitionReceipt=(%+v,%v), want (nil,ErrNotFound)", got, err)
	}
	if err := nilTx.InsertTransitionReceipt(context.Background(), validWorkspaceTransitionReceipt(t, requestID, "stash", "clean")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nil tx InsertTransitionReceipt error=%v, want ErrNotFound", err)
	}
}

func TestWorkspaceTransitionReceiptSingleSnapshotDistinguishesWorkspaceFromAbsentReceipt(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	requestID := "00000000-0000-1000-8000-000000000031"

	got, err := readWorkspaceTransitionReceipt(context.Background(), store.DB(), binding.Scope, requestID)
	if err != nil || got != nil {
		t.Fatalf("registered workspace absent receipt=(%+v,%v), want (nil,nil)", got, err)
	}
	unregistered := types.WorkspaceScope{
		ProjectID:   "00000000-0000-4000-8000-000000000099",
		WorkspaceID: "00000000-0000-4000-8000-000000000098",
	}
	got, err = readWorkspaceTransitionReceipt(context.Background(), store.DB(), unregistered, requestID)
	if !errors.Is(err, ErrNotFound) || got != nil {
		t.Fatalf("unregistered workspace receipt=(%+v,%v), want (nil,ErrNotFound)", got, err)
	}
}

func TestWorkspaceTransitionReceiptRejectsMalformedInputWithoutWrites(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	valid := validWorkspaceTransitionReceipt(t, "00000000-0000-1000-8000-000000000031", "stash", "clean")
	for _, test := range []struct {
		name string
		edit func(*WorkspaceTransitionReceiptInsert)
	}{
		{name: "request ID", edit: func(r *WorkspaceTransitionReceiptInsert) { r.RequestID = "BAD" }},
		{name: "action", edit: func(r *WorkspaceTransitionReceiptInsert) { r.Action = "publish" }},
		{name: "digest prefix", edit: func(r *WorkspaceTransitionReceiptInsert) { r.RequestDigest = state.Digest(strings.Repeat("a", 64)) }},
		{name: "digest uppercase", edit: func(r *WorkspaceTransitionReceiptInsert) {
			r.RequestDigest = state.Digest("sha256:" + strings.Repeat("A", 64))
		}},
		{name: "actor", edit: func(r *WorkspaceTransitionReceiptInsert) { r.Actor.HumanPrincipalID = "BAD" }},
		{name: "nil result", edit: func(r *WorkspaceTransitionReceiptInsert) { r.ResultJSON = nil }},
		{name: "malformed result", edit: func(r *WorkspaceTransitionReceiptInsert) { r.ResultJSON = json.RawMessage("{\n") }},
		{name: "trailing result", edit: func(r *WorkspaceTransitionReceiptInsert) { r.ResultJSON = json.RawMessage("{}\n{}\n") }},
		{name: "noncanonical result", edit: func(r *WorkspaceTransitionReceiptInsert) { r.ResultJSON = json.RawMessage("{\"z\":1,\"a\":2}\n") }},
		{name: "outcome", edit: func(r *WorkspaceTransitionReceiptInsert) { r.Outcome = "pending" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			receipt := valid
			receipt.ResultJSON = bytes.Clone(valid.ResultJSON)
			test.edit(&receipt)
			err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				return tx.InsertTransitionReceipt(context.Background(), receipt)
			})
			if err == nil || errors.Is(err, ErrNotFound) {
				t.Fatalf("InsertTransitionReceipt error=%v, want content validation error", err)
			}
		})
	}
	if got, err := repo.TransitionReceipt(context.Background(), binding.Scope, "BAD"); err == nil || errors.Is(err, ErrNotFound) || got != nil {
		t.Fatalf("malformed key TransitionReceipt=(%+v,%v), want validation error", got, err)
	}
	var count int
	if err := repo.db.QueryRow(`SELECT count(*) FROM workspace_transition_receipts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid inserts left count=%d err=%v", count, err)
	}
}

func TestWorkspaceTransitionReceiptDuplicateAndIgnoredInsertFail(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	receipt := validWorkspaceTransitionReceipt(t, "00000000-0000-1000-8000-000000000031", "stash", "clean")
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.InsertTransitionReceipt(context.Background(), receipt)
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.InsertTransitionReceipt(context.Background(), receipt)
	}); err == nil {
		t.Fatal("duplicate receipt insert succeeded")
	}

	ignored := validWorkspaceTransitionReceipt(t, "00000000-0000-1000-8000-000000000032", "discard", "conflicted")
	if _, err := store.DB().Exec(`
		CREATE TRIGGER ignore_transition_receipt
		BEFORE INSERT ON workspace_transition_receipts
		WHEN NEW.request_id='00000000-0000-1000-8000-000000000032'
		BEGIN SELECT RAISE(IGNORE); END
	`); err != nil {
		t.Fatal(err)
	}
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.InsertTransitionReceipt(context.Background(), ignored)
	}); err == nil {
		t.Fatal("ignored receipt insert succeeded")
	}
	got, err := repo.TransitionReceipt(context.Background(), binding.Scope, receipt.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkspaceTransitionReceipt(t, got, receipt)
}

func TestWorkspaceTransitionReceiptDuplicateCannotMutateImmutableFields(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	original := validWorkspaceTransitionReceipt(t, "00000000-0000-1000-8000-000000000031", "stash", "clean")
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.InsertTransitionReceipt(context.Background(), original)
	}); err != nil {
		t.Fatal(err)
	}
	wantRecord, err := repo.TransitionReceipt(context.Background(), binding.Scope, original.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	wantRaw := readRawWorkspaceTransitionReceipt(t, store, binding.Scope, original.RequestID)
	otherResult, err := state.CanonicalJSON(map[string]any{"changed": true})
	if err != nil {
		t.Fatal(err)
	}

	variants := []struct {
		name string
		edit func(*WorkspaceTransitionReceiptInsert)
	}{
		{name: "action", edit: func(r *WorkspaceTransitionReceiptInsert) { r.Action = "restore" }},
		{name: "request digest", edit: func(r *WorkspaceTransitionReceiptInsert) {
			r.RequestDigest = state.Digest("sha256:" + strings.Repeat("b", 64))
		}},
		{name: "actor", edit: func(r *WorkspaceTransitionReceiptInsert) {
			r.Actor.HumanPrincipalID = "00000000-0000-4000-8000-000000000062"
			r.Actor.OccurredAt = r.Actor.OccurredAt.Add(time.Second)
		}},
		{name: "result JSON", edit: func(r *WorkspaceTransitionReceiptInsert) { r.ResultJSON = bytes.Clone(otherResult) }},
		{name: "outcome", edit: func(r *WorkspaceTransitionReceiptInsert) { r.Outcome = "conflicted" }},
		{name: "every field", edit: func(r *WorkspaceTransitionReceiptInsert) {
			r.Action = "discard"
			r.RequestDigest = state.Digest("sha256:" + strings.Repeat("c", 64))
			r.Actor.HumanPrincipalID = "00000000-0000-4000-8000-000000000063"
			r.Actor.OccurredAt = r.Actor.OccurredAt.Add(2 * time.Second)
			r.ResultJSON = bytes.Clone(otherResult)
			r.Outcome = "conflicted"
		}},
	}
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			collision := original
			collision.ResultJSON = bytes.Clone(original.ResultJSON)
			variant.edit(&collision)
			if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				return tx.InsertTransitionReceipt(context.Background(), collision)
			}); err == nil {
				t.Fatal("same request ID with changed immutable content succeeded")
			}
		})
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo = NewWorkspaceRepo(store.DB())
	got, err := repo.TransitionReceipt(context.Background(), binding.Scope, original.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkspaceTransitionReceipt(t, got, original)
	if !got.CreatedAt.Equal(wantRecord.CreatedAt) {
		t.Fatalf("CreatedAt=%v, want unchanged %v", got.CreatedAt, wantRecord.CreatedAt)
	}
	if raw := readRawWorkspaceTransitionReceipt(t, store, binding.Scope, original.RequestID); raw != wantRaw {
		t.Fatalf("raw receipt after collisions=%+v, want unchanged %+v", raw, wantRaw)
	}
}

func TestWorkspaceTransitionReceiptCallbackRollback(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	receipt := validWorkspaceTransitionReceipt(t, "00000000-0000-1000-8000-000000000031", "stash", "clean")
	callbackErr := errors.New("callback fixture")
	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		if err := tx.InsertTransitionReceipt(context.Background(), receipt); err != nil {
			return err
		}
		return callbackErr
	})
	if !errors.Is(err, callbackErr) || errors.Is(err, ErrCommitOutcomeUnknown) {
		t.Fatalf("callback error=%v, want callback fixture without ErrCommitOutcomeUnknown", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo = NewWorkspaceRepo(store.DB())
	got, err := repo.TransitionReceipt(context.Background(), binding.Scope, receipt.RequestID)
	if err != nil || got != nil {
		t.Fatalf("rolled-back receipt=(%+v,%v), want (nil,nil)", got, err)
	}
}

func TestWorkspaceTransitionReceiptCommitFailureKeepsSentinel(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	receipt := validWorkspaceTransitionReceipt(t, "00000000-0000-1000-8000-000000000031", "stash", "clean")
	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		if err := tx.InsertTransitionReceipt(context.Background(), receipt); err != nil {
			return err
		}
		if _, err := tx.conn.ExecContext(context.Background(), `PRAGMA defer_foreign_keys=ON`); err != nil {
			return err
		}
		_, err := tx.conn.ExecContext(context.Background(), `
			INSERT INTO workspace_conflicts
			(project_id,workspace_id,occurrence_id,conflict_id,record_kind,record_id,field_path,
			 conflict_kind,base_json,ours_json,theirs_json,state)
			VALUES ('00000000-0000-4000-8000-000000000099','00000000-0000-4000-8000-000000000098',
			 'invalid-fk','invalid-fk','task','00000000-0000-4000-8000-000000000097','/title',
			 'same_field','{}','{}','{}','open')
		`)
		return err
	})
	if !errors.Is(err, ErrCommitOutcomeUnknown) {
		t.Fatalf("commit error=%v, want ErrCommitOutcomeUnknown", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo = NewWorkspaceRepo(store.DB())
	got, err := repo.TransitionReceipt(context.Background(), binding.Scope, receipt.RequestID)
	if err != nil || got != nil {
		t.Fatalf("failed-commit receipt=(%+v,%v), want (nil,nil)", got, err)
	}
}

func TestWorkspaceTransitionReceiptExactScopeIsolation(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	a := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	b := createBinding(t, repo, a.Scope.ProjectID, "00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	c := createBinding(t, repo, "00000000-0000-4000-8000-000000000002", string(a.Scope.WorkspaceID), "/checkout-c", 3, 13)
	requestID := "00000000-0000-1000-8000-000000000031"
	fixtures := []struct {
		scope   types.WorkspaceScope
		receipt WorkspaceTransitionReceiptInsert
	}{
		{scope: a.Scope, receipt: validWorkspaceTransitionReceipt(t, requestID, "stash", "clean")},
		{scope: b.Scope, receipt: validWorkspaceTransitionReceipt(t, requestID, "restore", "conflicted")},
		{scope: c.Scope, receipt: validWorkspaceTransitionReceipt(t, requestID, "discard", "clean")},
	}
	for index := range fixtures {
		fixture := &fixtures[index]
		fixture.receipt.RequestDigest = state.Digest(fmt.Sprintf("sha256:%064x", index+1))
		if err := repo.WithImmediateWorkspace(context.Background(), fixture.scope, func(tx *WorkspaceMutationTx) error {
			return tx.InsertTransitionReceipt(context.Background(), fixture.receipt)
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, fixture := range fixtures {
		got, err := repo.TransitionReceipt(context.Background(), fixture.scope, requestID)
		if err != nil {
			t.Fatal(err)
		}
		assertWorkspaceTransitionReceipt(t, got, fixture.receipt)
	}
}

func TestWorkspaceTransitionReceiptCorruptionFailsClosedAfterReopen(t *testing.T) {
	for _, test := range []struct {
		name   string
		column string
		value  any
	}{
		{name: "action", column: "action", value: "publish"},
		{name: "digest", column: "request_digest", value: "sha256:" + strings.Repeat("A", 64)},
		{name: "actor", column: "actor_json", value: "{\"actor_kind\":\"human\",\"assurance\":\"local\",\"human_principal_id\":\"BAD\",\"occurred_at\":\"2026-07-29T12:00:00Z\"}\n"},
		{name: "result", column: "result_json", value: "{\"z\":1,\"a\":2}\n"},
		{name: "outcome", column: "outcome", value: "pending"},
		{name: "time", column: "created_at", value: "not-a-time"},
	} {
		t.Run(test.name, func(t *testing.T) {
			databasePath := filepath.Join(t.TempDir(), "gateway.db")
			store, err := Open(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			repo := NewWorkspaceRepo(store.DB())
			binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
			receipt := validWorkspaceTransitionReceipt(t, "00000000-0000-1000-8000-000000000031", "stash", "clean")
			if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				return tx.InsertTransitionReceipt(context.Background(), receipt)
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.DB().Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
				t.Fatal(err)
			}
			query := fmt.Sprintf("UPDATE workspace_transition_receipts SET %s=? WHERE project_id=? AND workspace_id=?", test.column)
			if _, err := store.DB().Exec(query, test.value, binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = Open(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			repo = NewWorkspaceRepo(store.DB())
			got, err := repo.TransitionReceipt(context.Background(), binding.Scope, receipt.RequestID)
			if err == nil || got != nil {
				t.Fatalf("corrupt TransitionReceipt=(%+v,%v), want fail closed", got, err)
			}
		})
	}
}

func TestWorkspaceTransitionReceiptReadQueryFailure(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.TransitionReceipt(context.Background(), binding.Scope, "00000000-0000-1000-8000-000000000031"); err == nil || got != nil {
		t.Fatalf("closed database TransitionReceipt=(%+v,%v), want query error", got, err)
	}
}

func validWorkspaceTransitionReceipt(t *testing.T, requestID, action, outcome string) WorkspaceTransitionReceiptInsert {
	t.Helper()
	result, err := state.CanonicalJSON(map[string]any{"action": action, "ok": true})
	if err != nil {
		t.Fatal(err)
	}
	return WorkspaceTransitionReceiptInsert{
		RequestID:     requestID,
		Action:        action,
		RequestDigest: state.Digest("sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		Actor: types.ActorEnvelope{
			ActorKind: types.ActorHuman, HumanPrincipalID: "00000000-0000-4000-8000-000000000061",
			Assurance: types.AssuranceLocal, OccurredAt: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC),
		},
		ResultJSON: json.RawMessage(result),
		Outcome:    outcome,
	}
}

func assertWorkspaceTransitionReceipt(t *testing.T, got *WorkspaceTransitionReceiptRecord, want WorkspaceTransitionReceiptInsert) {
	t.Helper()
	if got == nil {
		t.Fatal("TransitionReceipt returned nil")
	}
	if got.RequestID != want.RequestID || got.Action != want.Action || got.RequestDigest != want.RequestDigest ||
		got.Actor != want.Actor || got.Outcome != want.Outcome || !bytes.Equal(got.ResultJSON, want.ResultJSON) {
		t.Fatalf("TransitionReceipt=%+v, want %+v", got, want)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("TransitionReceipt returned a zero CreatedAt")
	}
	_, offset := got.CreatedAt.Zone()
	if offset != 0 || got.CreatedAt.Location() != time.UTC {
		t.Fatalf("CreatedAt=%v in %v offset %d, want normalized UTC", got.CreatedAt, got.CreatedAt.Location(), offset)
	}
}

type rawWorkspaceTransitionReceipt struct {
	RequestID     string
	Action        string
	RequestDigest string
	ActorJSON     string
	ResultJSON    string
	Outcome       string
	CreatedAt     time.Time
}

func readRawWorkspaceTransitionReceipt(t *testing.T, store *Store, scope types.WorkspaceScope, requestID string) rawWorkspaceTransitionReceipt {
	t.Helper()
	var raw rawWorkspaceTransitionReceipt
	if err := store.DB().QueryRow(`
		SELECT request_id, action, request_digest, actor_json, result_json, outcome, created_at
		FROM workspace_transition_receipts
		WHERE project_id=? AND workspace_id=? AND request_id=?
	`, scope.ProjectID, scope.WorkspaceID, requestID).Scan(
		&raw.RequestID, &raw.Action, &raw.RequestDigest, &raw.ActorJSON,
		&raw.ResultJSON, &raw.Outcome, &raw.CreatedAt,
	); err != nil {
		t.Fatal(err)
	}
	return raw
}
