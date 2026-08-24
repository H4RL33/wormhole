package projectstate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestNewCanonicalStashIDReturnsVersionFour(t *testing.T) {
	stashID, err := newCanonicalStashID()
	if err != nil || !canonicalUUIDv4(stashID) {
		t.Fatalf("newCanonicalStashID()=(%q,%v)", stashID, err)
	}
}

func TestServiceStashValidatesRequestAndGeneratedIDBeforeTransaction(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	store, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	valid := StashRequest{
		Scope: registered.Binding.Scope, RequestID: "40000000-0000-4000-8000-000000000001",
		Actor: diffActorEnvelope(), Label: "validation",
	}

	for name, unavailable := range map[string]*Service{"nil": nil, "empty": {}} {
		t.Run(name+" service", func(t *testing.T) {
			if got, err := unavailable.Stash(context.Background(), valid); !errors.Is(err, localstore.ErrNotFound) || got != (StashResult{}) {
				t.Fatalf("Stash()=(%+v,%v), want zero ErrNotFound", got, err)
			}
		})
	}

	calls := 0
	service.newStashID = func() (string, error) {
		calls++
		return "20000000-0000-4000-8000-000000000001", nil
	}
	invalid := valid
	invalid.Label = ""
	before := captureStashRawState(t, store)
	if got, err := service.Stash(context.Background(), invalid); err == nil || got != (StashResult{}) || calls != 0 {
		t.Fatalf("invalid Stash()=(%+v,%v), generator calls=%d", got, err, calls)
	}
	if after := captureStashRawState(t, store); !reflect.DeepEqual(after, before) {
		t.Fatal("invalid request changed workspace state")
	}

	generatorErr := errors.New("stash ID source unavailable")
	service.newStashID = func() (string, error) { return "", generatorErr }
	if got, err := service.Stash(context.Background(), valid); !errors.Is(err, generatorErr) || got != (StashResult{}) {
		t.Fatalf("generator failure Stash()=(%+v,%v)", got, err)
	}
	for _, invalidID := range []string{
		"", "BAD", "20000000-0000-1000-8000-000000000001", "20000000-0000-4000-7000-000000000001",
	} {
		service.newStashID = func() (string, error) { return invalidID, nil }
		if got, err := service.Stash(context.Background(), valid); err == nil || got != (StashResult{}) {
			t.Fatalf("generated ID %q Stash()=(%+v,%v)", invalidID, got, err)
		}
	}
	service.newStashID = nil
	if got, err := service.Stash(context.Background(), valid); !errors.Is(err, localstore.ErrNotFound) || got != (StashResult{}) {
		t.Fatalf("nil generator Stash()=(%+v,%v)", got, err)
	}
	if after := captureStashRawState(t, store); !reflect.DeepEqual(after, before) {
		t.Fatal("generator validation changed workspace state")
	}
}

func TestServiceStashAcceptedBaseEmptySuccess(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	store, service := openProjectStateServiceAt(t, databasePath)
	registered := registerGitRepository(t, service, repository)
	accepted := mustServiceStatus(t, service, registered.Binding.Scope).AcceptedSnapshot
	const stashID = "20000000-0000-4000-8000-000000000001"
	service.newStashID = func() (string, error) { return stashID, nil }
	req := StashRequest{
		Scope: registered.Binding.Scope, RequestID: "40000000-0000-4000-8000-000000000001",
		Actor: diffActorEnvelope(), Label: "accepted base",
	}

	got, err := service.Stash(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	want := StashResult{StashID: stashID, SourceDigest: accepted.Digest, CandidateDigest: accepted.Digest, OperationCount: 0}
	if got != want {
		t.Fatalf("Stash()=%+v, want %+v", got, want)
	}
	assertAcceptedBaseStash(t, service, req, want, accepted)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	_, reopened := openProjectStateServiceAt(t, databasePath)
	assertAcceptedBaseStash(t, reopened, req, want, accepted)
}

func TestServiceStashDirectCandidateWithActiveSuffix(t *testing.T) {
	fixture := newStashServiceFixture(t)
	direct := diffCloneSnapshot(t, fixture.accepted)
	direct.Project.Name = "direct candidate"
	direct.Project.UpdatedAt = direct.Project.UpdatedAt.Add(time.Minute)
	direct = diffCanonicalSnapshot(t, direct)
	insertServiceCandidate(t, fixture.store, fixture.req.Scope, fixture.accepted.Digest, direct, nil, 0)
	setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "pending")
	later := servicePutTaskOperation(direct, "90000000-0000-4000-8000-000000000001", "80000000-0000-4000-8000-000000000001", "later task")
	wantComposed, err := state.ApplyOperation(direct, later)
	if err != nil {
		t.Fatal(err)
	}
	insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 3, later, "active")

	got, err := fixture.service.Stash(context.Background(), fixture.req)
	if err != nil {
		t.Fatal(err)
	}
	want := StashResult{StashID: fixture.stashID, SourceDigest: fixture.accepted.Digest, CandidateDigest: wantComposed.Digest, OperationCount: 1}
	if got != want {
		t.Fatalf("Stash()=%+v, want %+v", got, want)
	}
	persisted := readServiceStash(t, fixture.service, fixture.req.Scope, fixture.stashID)
	replay, err := decodeStashReplay(persisted.OperationsJSON, fixture.binding, persisted.ThroughGeneration)
	if err != nil {
		t.Fatal(err)
	}
	if replay.AbsorbedOperations == nil || len(replay.AbsorbedOperations) != 0 || len(replay.Operations) != 1 ||
		replay.Operations[0].Generation != 3 || replay.Operations[0].Operation.ID != later.ID {
		t.Fatalf("replay=%+v", replay)
	}
	assertStashOperationState(t, fixture.service, fixture.req.Scope, 3, "stashed", fixture.stashID, later)
	assertStashFinished(t, fixture.service, fixture.req.Scope)
}

func TestServiceStashImmediateRebasedCandidateHasEmptySuffix(t *testing.T) {
	fixture := newStashServiceFixture(t)
	absorbed := servicePutTaskOperation(fixture.accepted, "90000000-0000-4000-8000-000000000001", "80000000-0000-4000-8000-000000000001", "absorbed task")
	rebased, err := state.ApplyOperation(fixture.accepted, absorbed)
	if err != nil {
		t.Fatal(err)
	}
	insertServiceCandidate(t, fixture.store, fixture.req.Scope, fixture.accepted.Digest, fixture.accepted, &rebased, 4)
	insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 4, absorbed, "rebased")
	setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "pending")

	got, err := fixture.service.Stash(context.Background(), fixture.req)
	if err != nil {
		t.Fatal(err)
	}
	if got.CandidateDigest != rebased.Digest || got.OperationCount != 1 {
		t.Fatalf("Stash()=%+v", got)
	}
	persisted := readServiceStash(t, fixture.service, fixture.req.Scope, fixture.stashID)
	replay, err := decodeStashReplay(persisted.OperationsJSON, fixture.binding, persisted.ThroughGeneration)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.AbsorbedOperations) != 1 || replay.AbsorbedOperations[0].Generation != 4 ||
		replay.Operations == nil || len(replay.Operations) != 0 || replay.InitialThroughGeneration != 4 || persisted.ThroughGeneration != 4 {
		t.Fatalf("replay=%+v through=%d", replay, persisted.ThroughGeneration)
	}
	assertStashOperationState(t, fixture.service, fixture.req.Scope, 4, "stashed", fixture.stashID, absorbed)
	assertStashFinished(t, fixture.service, fixture.req.Scope)
}

func TestServiceStashAbsorbedSparseSuffixLeavesTerminalHistoryUnchanged(t *testing.T) {
	fixture := newStashServiceFixture(t)
	absorbed := servicePutTaskOperation(fixture.accepted, "90000000-0000-4000-8000-000000000004", "80000000-0000-4000-8000-000000000004", "absorbed task")
	rebased, err := state.ApplyOperation(fixture.accepted, absorbed)
	if err != nil {
		t.Fatal(err)
	}
	insertServiceCandidate(t, fixture.store, fixture.req.Scope, fixture.accepted.Digest, fixture.accepted, &rebased, 4)
	terminalMaterialized := servicePutTaskOperation(fixture.accepted, "90000000-0000-4000-8000-000000000001", "80000000-0000-4000-8000-000000000001", "materialized")
	terminalStashed := servicePutTaskOperation(fixture.accepted, "90000000-0000-4000-8000-000000000005", "80000000-0000-4000-8000-000000000005", "old stash")
	terminalDiscarded := servicePutTaskOperation(fixture.accepted, "90000000-0000-4000-8000-000000000006", "80000000-0000-4000-8000-000000000006", "discarded")
	first := servicePutTaskOperation(rebased, "90000000-0000-4000-8000-000000000007", "80000000-0000-4000-8000-000000000007", "later one")
	afterFirst, err := state.ApplyOperation(rebased, first)
	if err != nil {
		t.Fatal(err)
	}
	second := servicePutTaskOperation(afterFirst, "90000000-0000-4000-8000-000000000010", "80000000-0000-4000-8000-000000000010", "later two")
	afterSecond, err := state.ApplyOperation(afterFirst, second)
	if err != nil {
		t.Fatal(err)
	}
	insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 1, terminalMaterialized, "materialized")
	insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 4, absorbed, "rebased")
	insertOwnedServiceOperation(t, fixture.store, fixture.req.Scope, 5, terminalStashed, "stashed", "30000000-0000-4000-8000-000000000005")
	insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 6, terminalDiscarded, "discarded")
	insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 7, first, "active")
	insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 10, second, "active")
	setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "pending")

	got, err := fixture.service.Stash(context.Background(), fixture.req)
	if err != nil {
		t.Fatal(err)
	}
	if got.CandidateDigest != afterSecond.Digest || got.OperationCount != 3 {
		t.Fatalf("Stash()=%+v", got)
	}
	persisted := readServiceStash(t, fixture.service, fixture.req.Scope, fixture.stashID)
	replay, err := decodeStashReplay(persisted.OperationsJSON, fixture.binding, persisted.ThroughGeneration)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.AbsorbedOperations) != 1 || replay.AbsorbedOperations[0].Generation != 4 || len(replay.Operations) != 2 ||
		replay.Operations[0].Generation != 7 || replay.Operations[1].Generation != 10 || persisted.ThroughGeneration != 10 {
		t.Fatalf("replay=%+v", replay)
	}
	assertStashOperationState(t, fixture.service, fixture.req.Scope, 1, "materialized", "", terminalMaterialized)
	assertStashOperationState(t, fixture.service, fixture.req.Scope, 4, "stashed", fixture.stashID, absorbed)
	assertStashOperationState(t, fixture.service, fixture.req.Scope, 5, "stashed", "30000000-0000-4000-8000-000000000005", terminalStashed)
	assertStashOperationState(t, fixture.service, fixture.req.Scope, 6, "discarded", "", terminalDiscarded)
	assertStashOperationState(t, fixture.service, fixture.req.Scope, 7, "stashed", fixture.stashID, first)
	assertStashOperationState(t, fixture.service, fixture.req.Scope, 10, "stashed", fixture.stashID, second)
	assertStashFinished(t, fixture.service, fixture.req.Scope)
}

func TestServiceStashCurrentWorksetRejectsWrongSideAndIgnoresCorruptTerminalRows(t *testing.T) {
	for _, test := range []struct {
		name        string
		setup       func(*testing.T, stashServiceFixture)
		wantSuccess bool
	}{
		{name: "active at rebased boundary", setup: func(t *testing.T, fixture stashServiceFixture) {
			operation := servicePutTaskOperation(fixture.accepted, "90000000-0000-4000-8000-000000000004", "80000000-0000-4000-8000-000000000004", "wrong active")
			rebased, err := state.ApplyOperation(fixture.accepted, operation)
			if err != nil {
				t.Fatal(err)
			}
			insertServiceCandidate(t, fixture.store, fixture.req.Scope, fixture.accepted.Digest, fixture.accepted, &rebased, 4)
			insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 4, operation, "active")
			setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "pending")
		}},
		{name: "rebased above boundary", setup: func(t *testing.T, fixture stashServiceFixture) {
			absorbed := servicePutTaskOperation(fixture.accepted, "90000000-0000-4000-8000-000000000004", "80000000-0000-4000-8000-000000000004", "absorbed")
			rebased, err := state.ApplyOperation(fixture.accepted, absorbed)
			if err != nil {
				t.Fatal(err)
			}
			wrong := servicePutTaskOperation(rebased, "90000000-0000-4000-8000-000000000007", "80000000-0000-4000-8000-000000000007", "wrong rebased")
			insertServiceCandidate(t, fixture.store, fixture.req.Scope, fixture.accepted.Digest, fixture.accepted, &rebased, 4)
			insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 4, absorbed, "rebased")
			insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 7, wrong, "rebased")
			setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "pending")
		}},
		{name: "corrupt terminal payload", setup: func(t *testing.T, fixture stashServiceFixture) {
			if _, err := fixture.store.DB().Exec(`
				INSERT INTO workspace_overlay_operations
				(project_id,workspace_id,generation,operation_id,operation_json,state)
				VALUES (?,?,?,?,?,'discarded')
			`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID, 1,
				"90000000-0000-4000-8000-000000000001", "{"); err != nil {
				t.Fatal(err)
			}
		}, wantSuccess: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newStashServiceFixture(t)
			test.setup(t, fixture)
			before := captureStashRawState(t, fixture.store)
			got, err := fixture.service.Stash(context.Background(), fixture.req)
			if test.wantSuccess {
				if err != nil || got.StashID != fixture.stashID {
					t.Fatalf("Stash()=(%+v,%v), want current-workset success", got, err)
				}
				if err := fixture.service.repo.AuditWorkspaceHistory(context.Background(), fixture.req.Scope); err == nil {
					t.Fatal("AuditWorkspaceHistory accepted corrupt terminal operation")
				}
				return
			}
			if err == nil || got != (StashResult{}) {
				t.Fatalf("Stash()=(%+v,%v), want zero failure", got, err)
			}
			if after := captureStashRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
				t.Fatal("failed complete-audit stash changed state")
			}
		})
	}
}

func TestServiceStashOpenConflictReturnsZeroWithoutMutation(t *testing.T) {
	fixture := newStashServiceFixture(t)
	operation := servicePutTaskOperation(fixture.accepted, "90000000-0000-4000-8000-000000000001", "80000000-0000-4000-8000-000000000001", "pending")
	insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 1, operation, "active")
	insertServiceConflict(t, fixture.store, fixture.req.Scope, "open-stash-conflict", state.RecordKey{Kind: "task", ID: "80000000-0000-4000-8000-000000000001"}, "open")
	setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "conflicted")
	before := captureStashRawState(t, fixture.store)

	got, err := fixture.service.Stash(context.Background(), fixture.req)
	if !errors.Is(err, localstore.ErrWorkspaceConflicted) || got != (StashResult{}) {
		t.Fatalf("Stash()=(%+v,%v), want zero ErrWorkspaceConflicted", got, err)
	}
	if after := captureStashRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
		t.Fatal("conflicted stash changed state")
	}
}

func TestServiceStashIdempotentReceiptIsReadOnlyAndCollisionsFail(t *testing.T) {
	fixture := newStashServiceFixture(t)
	first, err := fixture.service.Stash(context.Background(), fixture.req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.DB().Exec(`
		CREATE TRIGGER stash_retry_reject_stash BEFORE INSERT ON workspace_stashes BEGIN SELECT RAISE(ABORT,'stash write'); END;
		CREATE TRIGGER stash_retry_reject_candidate BEFORE DELETE ON workspace_candidates BEGIN SELECT RAISE(ABORT,'candidate write'); END;
		CREATE TRIGGER stash_retry_reject_operation BEFORE UPDATE ON workspace_overlay_operations BEGIN SELECT RAISE(ABORT,'operation write'); END;
		CREATE TRIGGER stash_retry_reject_status BEFORE UPDATE ON workspace_bindings BEGIN SELECT RAISE(ABORT,'status write'); END;
		CREATE TRIGGER stash_retry_reject_receipt BEFORE INSERT ON workspace_transition_receipts BEGIN SELECT RAISE(ABORT,'receipt write'); END;
	`); err != nil {
		t.Fatal(err)
	}
	fixture.service.newStashID = func() (string, error) { return "20000000-0000-4000-8000-000000000002", nil }
	before := captureStashRawState(t, fixture.store)
	retry, err := fixture.service.Stash(context.Background(), fixture.req)
	if err != nil || retry != first {
		t.Fatalf("exact retry Stash()=(%+v,%v), want %+v", retry, err, first)
	}
	if after := captureStashRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
		t.Fatal("exact receipt retry performed a write")
	}

	actorCollision := fixture.req
	actorCollision.Actor.OccurredAt = actorCollision.Actor.OccurredAt.Add(time.Second)
	labelCollision := fixture.req
	labelCollision.Label = "different label"
	for name, collision := range map[string]StashRequest{"actor": actorCollision, "label": labelCollision} {
		t.Run(name+" collision", func(t *testing.T) {
			got, err := fixture.service.Stash(context.Background(), collision)
			if !errors.Is(err, ErrIdempotencyConflict) || got != (StashResult{}) {
				t.Fatalf("collision Stash()=(%+v,%v)", got, err)
			}
			if after := captureStashRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
				t.Fatal("idempotency collision changed state")
			}
		})
	}
}

func TestServiceStashCrossActionReceiptIsIdempotencyConflict(t *testing.T) {
	fixture := newStashServiceFixture(t)
	receipt := localstore.WorkspaceTransitionReceiptInsert{
		RequestID: fixture.req.RequestID, Action: "restore",
		RequestDigest: state.Digest("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		Actor:         fixture.req.Actor, ResultJSON: []byte("{\"action\":\"restore\"}\n"), Outcome: "clean",
	}
	if err := fixture.service.repo.WithImmediateWorkspace(context.Background(), fixture.req.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		return tx.InsertTransitionReceipt(context.Background(), receipt)
	}); err != nil {
		t.Fatal(err)
	}
	before := captureStashRawState(t, fixture.store)
	got, err := fixture.service.Stash(context.Background(), fixture.req)
	if !errors.Is(err, ErrIdempotencyConflict) || got != (StashResult{}) {
		t.Fatalf("Stash()=(%+v,%v), want zero ErrIdempotencyConflict", got, err)
	}
	if after := captureStashRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
		t.Fatal("cross-action collision changed state")
	}
}

func TestServiceStashCorruptExactReceiptFailsClosedWithoutConflictAlias(t *testing.T) {
	fixture := newStashServiceFixture(t)
	if _, err := fixture.service.Stash(context.Background(), fixture.req); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.DB().Exec(`
		UPDATE workspace_transition_receipts SET result_json=?
		WHERE project_id=? AND workspace_id=? AND request_id=?
	`, "{\"schema_version\": 1}\n", fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID, fixture.req.RequestID); err != nil {
		t.Fatal(err)
	}
	before := captureStashRawState(t, fixture.store)
	got, err := fixture.service.Stash(context.Background(), fixture.req)
	if err == nil || errors.Is(err, ErrIdempotencyConflict) || got != (StashResult{}) {
		t.Fatalf("corrupt receipt Stash()=(%+v,%v)", got, err)
	}
	if after := captureStashRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
		t.Fatal("corrupt receipt retry changed state")
	}
}

func TestServiceStashRollsBackEveryWriteStage(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, stashServiceFixture)
		trigger string
	}{
		{name: "stash insert", trigger: `
			CREATE TRIGGER stash_fault BEFORE INSERT ON workspace_stashes BEGIN SELECT RAISE(ABORT,'stash insert'); END`},
		{name: "candidate delete", prepare: func(t *testing.T, fixture stashServiceFixture) {
			direct := diffCloneSnapshot(t, fixture.accepted)
			direct.Project.Name = "candidate"
			direct.Project.UpdatedAt = direct.Project.UpdatedAt.Add(time.Minute)
			direct = diffCanonicalSnapshot(t, direct)
			insertServiceCandidate(t, fixture.store, fixture.req.Scope, fixture.accepted.Digest, direct, nil, 0)
			setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "pending")
		}, trigger: `
			CREATE TRIGGER stash_fault BEFORE DELETE ON workspace_candidates BEGIN SELECT RAISE(ABORT,'candidate delete'); END`},
		{name: "absorbed transition", prepare: func(t *testing.T, fixture stashServiceFixture) {
			operation := servicePutTaskOperation(fixture.accepted, "90000000-0000-4000-8000-000000000004", "80000000-0000-4000-8000-000000000004", "absorbed")
			rebased, err := state.ApplyOperation(fixture.accepted, operation)
			if err != nil {
				t.Fatal(err)
			}
			insertServiceCandidate(t, fixture.store, fixture.req.Scope, fixture.accepted.Digest, fixture.accepted, &rebased, 4)
			insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 4, operation, "rebased")
			setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "pending")
		}, trigger: `
			CREATE TRIGGER stash_fault BEFORE UPDATE OF state ON workspace_overlay_operations
			WHEN OLD.generation=4 AND NEW.state='stashed' BEGIN SELECT RAISE(ABORT,'absorbed transition'); END`},
		{name: "later transition", prepare: func(t *testing.T, fixture stashServiceFixture) {
			operation := servicePutTaskOperation(fixture.accepted, "90000000-0000-4000-8000-000000000007", "80000000-0000-4000-8000-000000000007", "later")
			insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 7, operation, "active")
			setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "pending")
		}, trigger: `
			CREATE TRIGGER stash_fault BEFORE UPDATE OF state ON workspace_overlay_operations
			WHEN OLD.generation=7 AND NEW.state='stashed' BEGIN SELECT RAISE(ABORT,'later transition'); END`},
		{name: "status", prepare: func(t *testing.T, fixture stashServiceFixture) {
			setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "pending")
		}, trigger: `
			CREATE TRIGGER stash_fault BEFORE UPDATE OF status ON workspace_bindings BEGIN SELECT RAISE(ABORT,'status'); END`},
		{name: "receipt", trigger: `
			CREATE TRIGGER stash_fault BEFORE INSERT ON workspace_transition_receipts BEGIN SELECT RAISE(ABORT,'receipt'); END`},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newStashServiceFixture(t)
			if test.prepare != nil {
				test.prepare(t, fixture)
			}
			if _, err := fixture.store.DB().Exec(test.trigger); err != nil {
				t.Fatal(err)
			}
			before := captureStashRawState(t, fixture.store)
			got, err := fixture.service.Stash(context.Background(), fixture.req)
			if err == nil || errors.Is(err, localstore.ErrCommitOutcomeUnknown) || got != (StashResult{}) {
				t.Fatalf("Stash()=(%+v,%v), want ordinary zero failure", got, err)
			}
			if after := captureStashRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
				t.Fatal("write-stage failure changed state")
			}
			if _, err := fixture.store.DB().Exec(`DROP TRIGGER stash_fault`); err != nil {
				t.Fatal(err)
			}
			if err := fixture.store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, _ := openProjectStateServiceAt(t, fixture.databasePath)
			if after := captureStashRawState(t, reopened); !reflect.DeepEqual(after, before) {
				t.Fatal("write-stage rollback changed state after restart")
			}
		})
	}
}

func TestServiceStashCommitUnknownReturnsZeroAndSameRequestRetrySucceeds(t *testing.T) {
	fixture := newStashServiceFixture(t)
	ids := []string{
		"20000000-0000-4000-8000-000000000001",
		"20000000-0000-4000-8000-000000000002",
	}
	generated := 0
	fixture.service.newStashID = func() (string, error) {
		id := ids[generated]
		generated++
		return id, nil
	}
	if _, err := fixture.store.DB().Exec(`
		CREATE TABLE stash_deferred_failure(
		  project_id TEXT NOT NULL,
		  workspace_id TEXT NOT NULL,
		  FOREIGN KEY(project_id,workspace_id) REFERENCES workspace_bindings(project_id,workspace_id)
		    DEFERRABLE INITIALLY DEFERRED
		);
		CREATE TRIGGER stash_fail_commit AFTER INSERT ON workspace_transition_receipts BEGIN
		  INSERT INTO stash_deferred_failure(project_id,workspace_id)
		  VALUES ('00000000-0000-4000-8000-000000000099','00000000-0000-4000-8000-000000000098');
		END;
	`); err != nil {
		t.Fatal(err)
	}
	before := captureStashRawState(t, fixture.store)
	got, err := fixture.service.Stash(context.Background(), fixture.req)
	if !errors.Is(err, localstore.ErrCommitOutcomeUnknown) || errors.Is(err, ErrIdempotencyConflict) || got != (StashResult{}) {
		t.Fatalf("unknown Stash()=(%+v,%v)", got, err)
	}
	if after := captureStashRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
		t.Fatal("failed commit changed state")
	}
	if _, err := fixture.store.DB().Exec(`DROP TRIGGER stash_fail_commit; DROP TABLE stash_deferred_failure`); err != nil {
		t.Fatal(err)
	}
	retry, err := fixture.service.Stash(context.Background(), fixture.req)
	if err != nil || retry.StashID != ids[1] || generated != 2 {
		t.Fatalf("same-request retry Stash()=(%+v,%v), generated=%d", retry, err, generated)
	}
}

func TestConfirmStashCommitRequiresExactCompleteReceipt(t *testing.T) {
	commitErr := fmt.Errorf("%w: fixture", localstore.ErrCommitOutcomeUnknown)
	t.Run("exact", func(t *testing.T) {
		fixture := newStashServiceFixture(t)
		want, err := fixture.service.Stash(context.Background(), fixture.req)
		if err != nil {
			t.Fatal(err)
		}
		got, err := confirmStashCommit(context.Background(), fixture.service.repo, fixture.req, want, commitErr)
		if err != nil || got != want {
			t.Fatalf("confirmStashCommit()=(%+v,%v), want %+v", got, err, want)
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*testing.T, stashServiceFixture, StashResult) (StashRequest, StashResult)
	}{
		{name: "missing", mutate: func(_ *testing.T, fixture stashServiceFixture, want StashResult) (StashRequest, StashResult) {
			fixture.req.RequestID = "40000000-0000-4000-8000-000000000099"
			return fixture.req, want
		}},
		{name: "malformed action receipt", mutate: func(t *testing.T, fixture stashServiceFixture, want StashResult) (StashRequest, StashResult) {
			if _, err := fixture.store.DB().Exec(`
				UPDATE workspace_transition_receipts SET result_json=?
				WHERE project_id=? AND workspace_id=? AND request_id=?
			`, "{\"schema_version\":1,\"action\":\"stash\"}\n", fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID, fixture.req.RequestID); err != nil {
				t.Fatal(err)
			}
			return fixture.req, want
		}},
		{name: "complete result mismatch", mutate: func(_ *testing.T, fixture stashServiceFixture, want StashResult) (StashRequest, StashResult) {
			want.OperationCount++
			return fixture.req, want
		}},
		{name: "read error", mutate: func(t *testing.T, fixture stashServiceFixture, want StashResult) (StashRequest, StashResult) {
			if err := fixture.store.Close(); err != nil {
				t.Fatal(err)
			}
			return fixture.req, want
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newStashServiceFixture(t)
			persisted, err := fixture.service.Stash(context.Background(), fixture.req)
			if err != nil {
				t.Fatal(err)
			}
			req, expected := test.mutate(t, fixture, persisted)
			got, err := confirmStashCommit(context.Background(), fixture.service.repo, req, expected, commitErr)
			if !errors.Is(err, localstore.ErrCommitOutcomeUnknown) || errors.Is(err, ErrIdempotencyConflict) || got != (StashResult{}) {
				t.Fatalf("confirmStashCommit()=(%+v,%v), want zero commit-unknown non-conflict error", got, err)
			}
		})
	}
}

func TestServiceStashKeepsSiblingWorkspaceAndReceiptScopeIsolated(t *testing.T) {
	store, service := openProjectStateService(t, "")
	repositoryA := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	repositoryB := createGitRepository(t, "00000000-0000-4000-8000-000000000002")
	a := registerGitRepository(t, service, repositoryA)
	b := registerGitRepository(t, service, repositoryB)
	sameProjectRoot := filepath.Join(t.TempDir(), "same-project-worktree")
	runGit(t, repositoryA.root, "worktree", "add", "-b", "stash-sibling", sameProjectRoot, repositoryA.commit)
	c, err := service.RegisterWorkspace(context.Background(), RegisterWorkspaceRequest{
		Root: sameProjectRoot, ExpectedProjectID: repositoryA.projectID,
		ExpectedRepository: repositoryA.identity, ExpectedCommit: repositoryA.commit,
	})
	if err != nil {
		t.Fatal(err)
	}
	acceptedA := mustServiceStatus(t, service, a.Binding.Scope).AcceptedSnapshot
	acceptedB := mustServiceStatus(t, service, b.Binding.Scope).AcceptedSnapshot
	acceptedC := mustServiceStatus(t, service, c.Binding.Scope).AcceptedSnapshot
	operationA := servicePutTaskOperation(acceptedA, "90000000-0000-4000-8000-000000000001", "80000000-0000-4000-8000-000000000001", "workspace A")
	operationB := servicePutTaskOperation(acceptedB, "90000000-0000-4000-8000-000000000001", "80000000-0000-4000-8000-000000000001", "workspace B")
	operationC := servicePutTaskOperation(acceptedC, "90000000-0000-4000-8000-000000000001", "80000000-0000-4000-8000-000000000001", "workspace C")
	insertStashServiceOperation(t, store, a.Binding.Scope, 1, operationA, "active")
	insertStashServiceOperation(t, store, b.Binding.Scope, 1, operationB, "active")
	insertStashServiceOperation(t, store, c.Binding.Scope, 1, operationC, "active")
	setServiceWorkspaceState(t, store, a.Binding.Scope, "pending")
	setServiceWorkspaceState(t, store, b.Binding.Scope, "pending")
	setServiceWorkspaceState(t, store, c.Binding.Scope, "pending")
	const requestID = "40000000-0000-4000-8000-000000000001"
	service.newStashID = func() (string, error) { return "20000000-0000-4000-8000-000000000001", nil }
	reqA := StashRequest{Scope: a.Binding.Scope, RequestID: requestID, Actor: diffActorEnvelope(), Label: "isolated"}
	if _, err := service.Stash(context.Background(), reqA); err != nil {
		t.Fatal(err)
	}
	siblings := []struct {
		name      string
		scope     types.WorkspaceScope
		operation state.OperationV1
		stashID   string
	}{
		{name: "cross-project", scope: b.Binding.Scope, operation: operationB, stashID: "20000000-0000-4000-8000-000000000002"},
		{name: "same-project", scope: c.Binding.Scope, operation: operationC, stashID: "20000000-0000-4000-8000-000000000003"},
	}
	for _, sibling := range siblings {
		status := mustServiceStatus(t, service, sibling.scope)
		if status.State != "pending" || status.OverlayGeneration != 1 {
			t.Fatalf("%s sibling Status()=%+v", sibling.name, status)
		}
		assertStashOperationState(t, service, sibling.scope, 1, "active", "", sibling.operation)
		if err := service.repo.WithImmediateWorkspace(context.Background(), sibling.scope, func(tx *localstore.WorkspaceMutationTx) error {
			stash, err := tx.Stash(context.Background(), "20000000-0000-4000-8000-000000000001")
			if err != nil {
				return err
			}
			if stash != nil {
				t.Fatalf("%s sibling read workspace A stash: %+v", sibling.name, stash)
			}
			receipt, err := tx.TransitionReceipt(context.Background(), requestID)
			if err != nil {
				return err
			}
			if receipt != nil {
				t.Fatalf("%s sibling read workspace A receipt: %+v", sibling.name, receipt)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, sibling := range siblings {
		stashID := sibling.stashID
		service.newStashID = func() (string, error) { return stashID, nil }
		req := reqA
		req.Scope = sibling.scope
		result, err := service.Stash(context.Background(), req)
		if err != nil || result.StashID != sibling.stashID {
			t.Fatalf("%s sibling Stash()=(%+v,%v)", sibling.name, result, err)
		}
	}
}

func assertAcceptedBaseStash(t *testing.T, service *Service, req StashRequest, want StashResult, accepted state.Snapshot) {
	t.Helper()
	if status := mustServiceStatus(t, service, req.Scope); status.State != "clean" || status.CandidateDigest != accepted.Digest || status.OverlayGeneration != 0 {
		t.Fatalf("Status()=%+v", status)
	}
	var persisted *localstore.WorkspaceStashRecord
	var receipt *localstore.WorkspaceTransitionReceiptRecord
	if err := service.repo.WithImmediateWorkspace(context.Background(), req.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		var err error
		persisted, err = tx.Stash(context.Background(), want.StashID)
		if err != nil {
			return err
		}
		if candidate, err := tx.Candidate(context.Background()); err != nil || candidate != nil {
			t.Fatalf("Candidate()=(%+v,%v), want absent", candidate, err)
		}
		receipt, err = tx.TransitionReceipt(context.Background(), req.RequestID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	acceptedTree, err := state.EncodeTree(accepted)
	if err != nil {
		t.Fatal(err)
	}
	if persisted == nil || persisted.StashID != want.StashID || persisted.SourceBaseDigest != accepted.Digest ||
		persisted.CandidateDigest != accepted.Digest || persisted.ThroughGeneration != 0 || persisted.Label != req.Label ||
		persisted.Actor != req.Actor || !equalCheckpointTree(persisted.SourceTree, acceptedTree) || !equalCheckpointTree(persisted.ComposedTree, acceptedTree) {
		t.Fatalf("persisted stash=%+v", persisted)
	}
	replay, err := decodeStashReplay(persisted.OperationsJSON, mustServiceStatus(t, service, req.Scope).Binding, 0)
	if err != nil {
		t.Fatal(err)
	}
	if replay.AbsorbedOperations == nil || replay.Operations == nil || len(replay.AbsorbedOperations) != 0 || len(replay.Operations) != 0 {
		t.Fatalf("replay=%+v, want non-nil empty arrays", replay)
	}
	decoded, err := decodeStashReceipt(receipt, req)
	if err != nil || decoded != want {
		t.Fatalf("receipt=(%+v,%v), want %+v", decoded, err, want)
	}
}

type stashRawState struct {
	workspace importRawState
	stashes   [][]string
	receipts  [][]string
}

func captureStashRawState(t *testing.T, store *localstore.Store) stashRawState {
	t.Helper()
	return stashRawState{
		workspace: captureImportRawState(t, store),
		stashes: queryImportRawRows(t, store, `
			SELECT quote(project_id),quote(workspace_id),quote(stash_id),quote(source_base_digest),
			       quote(candidate_digest),quote(source_tree),quote(composed_tree),quote(operations_json),
			       quote(through_generation),quote(actor_json),quote(label),quote(created_at)
			FROM workspace_stashes ORDER BY project_id,workspace_id,stash_id`),
		receipts: queryImportRawRows(t, store, `
			SELECT quote(project_id),quote(workspace_id),quote(request_id),quote(action),quote(request_digest),
			       quote(actor_json),quote(result_json),quote(outcome),quote(created_at)
			FROM workspace_transition_receipts ORDER BY project_id,workspace_id,request_id`),
	}
}

type stashServiceFixture struct {
	databasePath string
	store        *localstore.Store
	service      *Service
	binding      types.WorkspaceBinding
	accepted     state.Snapshot
	req          StashRequest
	stashID      string
}

func newStashServiceFixture(t *testing.T) stashServiceFixture {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	store, service := openProjectStateServiceAt(t, databasePath)
	registered := registerGitRepository(t, service, repository)
	accepted := mustServiceStatus(t, service, registered.Binding.Scope).AcceptedSnapshot
	const stashID = "20000000-0000-4000-8000-000000000001"
	service.newStashID = func() (string, error) { return stashID, nil }
	return stashServiceFixture{
		databasePath: databasePath, store: store, service: service, binding: registered.Binding, accepted: accepted, stashID: stashID,
		req: StashRequest{
			Scope: registered.Binding.Scope, RequestID: "40000000-0000-4000-8000-000000000001",
			Actor: diffActorEnvelope(), Label: "stash fixture",
		},
	}
}

func readServiceStash(t *testing.T, service *Service, scope types.WorkspaceScope, stashID string) *localstore.WorkspaceStashRecord {
	t.Helper()
	var result *localstore.WorkspaceStashRecord
	if err := service.repo.WithImmediateWorkspace(context.Background(), scope, func(tx *localstore.WorkspaceMutationTx) error {
		var err error
		result, err = tx.Stash(context.Background(), stashID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("persisted stash is absent")
	}
	return result
}

func assertStashFinished(t *testing.T, service *Service, scope types.WorkspaceScope) {
	t.Helper()
	status := mustServiceStatus(t, service, scope)
	if status.State != "clean" {
		t.Fatalf("workspace status=%q, want clean", status.State)
	}
	if err := service.repo.WithImmediateWorkspace(context.Background(), scope, func(tx *localstore.WorkspaceMutationTx) error {
		candidate, err := tx.Candidate(context.Background())
		if err != nil {
			return err
		}
		if candidate != nil {
			t.Fatalf("candidate remains after stash: %+v", candidate)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func assertStashOperationState(
	t *testing.T,
	service *Service,
	scope types.WorkspaceScope,
	generation int64,
	wantState, wantOwner string,
	wantOperation state.OperationV1,
) {
	t.Helper()
	wantJSON, err := state.CanonicalOperation(wantOperation)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	if err := service.repo.WithImmediateWorkspace(context.Background(), scope, func(tx *localstore.WorkspaceMutationTx) error {
		records, err := tx.OperationsByGenerations(context.Background(), []int64{generation})
		if err != nil {
			return err
		}
		for _, record := range records {
			if record.Generation != generation {
				continue
			}
			found = true
			gotOwner := ""
			if record.StashedByStashID != nil {
				gotOwner = *record.StashedByStashID
			}
			if record.State != wantState || gotOwner != wantOwner || record.OperationID != wantOperation.ID || !bytes.Equal(record.OperationJSON, wantJSON) {
				t.Fatalf("operation generation %d=%+v owner=%q", generation, record, gotOwner)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("operation generation %d is absent", generation)
	}
}

func insertOwnedServiceOperation(
	t *testing.T,
	store *localstore.Store,
	scope types.WorkspaceScope,
	generation int64,
	operation state.OperationV1,
	operationState, owner string,
) {
	t.Helper()
	raw, err := state.CanonicalOperation(operation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
		INSERT INTO workspace_overlay_operations
		(project_id,workspace_id,generation,operation_id,operation_json,state,stashed_by_stash_id)
		VALUES (?,?,?,?,?,?,?)
	`, scope.ProjectID, scope.WorkspaceID, generation, operation.ID, string(raw), operationState, owner); err != nil {
		t.Fatal(err)
	}
}

func insertStashServiceOperation(
	t *testing.T,
	store *localstore.Store,
	scope types.WorkspaceScope,
	generation int64,
	operation state.OperationV1,
	operationState string,
) {
	t.Helper()
	raw, err := state.CanonicalOperation(operation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
		INSERT INTO workspace_overlay_operations
		(project_id,workspace_id,generation,operation_id,operation_json,state)
		VALUES (?,?,?,?,?,?)
	`, scope.ProjectID, scope.WorkspaceID, generation, operation.ID, string(raw), operationState); err != nil {
		t.Fatal(err)
	}
}
