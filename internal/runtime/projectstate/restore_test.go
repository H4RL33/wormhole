package projectstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestServiceRestoreStashAPI(t *testing.T) {
	var service *Service
	if got, err := service.RestoreStash(context.Background(), RestoreStashRequest{}); err == nil || !reflect.DeepEqual(got, RestoreStashResult{}) {
		t.Fatalf("RestoreStash()=(%+v,%v), want zero result and error", got, err)
	}
}

func TestServiceRestoreStashV2ReceiptsStoreCommittedRevisionAndRetryRequiresIt(t *testing.T) {
	for _, conflicted := range []bool{false, true} {
		name := "clean"
		if conflicted {
			name = "conflicted"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newRestoreServiceFixture(t)
			if conflicted {
				current := restoreProjectName(t, fixture.accepted, "current project", 2*time.Minute)
				insertServiceCandidate(t, fixture.store, fixture.req.Scope, fixture.accepted.Digest, current, nil, 0)
				setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "pending")
			}
			want, err := fixture.service.RestoreStash(context.Background(), fixture.req)
			if err != nil {
				t.Fatal(err)
			}
			var raw []byte
			var committedRevision int64
			if err := fixture.store.DB().QueryRow(`SELECT receipt.result_json,binding.workspace_revision
				FROM workspace_transition_receipts receipt JOIN workspace_bindings binding USING(project_id,workspace_id)
				WHERE receipt.project_id=? AND receipt.workspace_id=? AND receipt.request_id=?`,
				fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID, fixture.req.RequestID).Scan(&raw, &committedRevision); err != nil {
				t.Fatal(err)
			}
			var receipt restoreStashReceiptV2
			if err := json.Unmarshal(raw, &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt.SchemaVersion != 2 || receipt.WorkspaceRevision != committedRevision {
				t.Fatalf("receipt=%+v committed revision=%d", receipt, committedRevision)
			}
			if !conflicted {
				return
			}

			if _, err := fixture.store.DB().Exec(`UPDATE workspace_bindings SET workspace_revision=workspace_revision+1
				WHERE project_id=? AND workspace_id=?`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID); err != nil {
				t.Fatal(err)
			}
			before := captureStashRawState(t, fixture.store)
			got, err := fixture.service.RestoreStash(context.Background(), fixture.req)
			if err == nil || !reflect.DeepEqual(got, RestoreStashResult{}) || !strings.Contains(err.Error(), "revision mismatch") {
				t.Fatalf("revision-mismatched retry=(%+v,%v)", got, err)
			}
			if after := captureStashRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
				t.Fatal("revision-mismatched retry wrote state")
			}
			_ = want
		})
	}
}

func TestServiceRestoreStashAlreadyConflictedIdenticalEvidenceProjectsReceiptRevision(t *testing.T) {
	fixture := newRestoreServiceFixture(t)
	current := restoreProjectName(t, fixture.accepted, "current project", 2*time.Minute)
	insertServiceCandidate(t, fixture.store, fixture.req.Scope, fixture.accepted.Digest, current, nil, 0)
	setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "pending")

	var evidence []localstore.WorkspaceConflictEvidence
	if err := fixture.service.repo.WithImmediateWorkspace(context.Background(), fixture.req.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		persisted, err := tx.RestoreCurrentState(context.Background(), fixture.req.StashID)
		if err != nil {
			return err
		}
		plan, err := buildRestoreCurrentPlan(persisted)
		if err != nil {
			return err
		}
		if len(plan.Result.Conflicts) == 0 || len(plan.ConflictEvidence) == 0 {
			return fmt.Errorf("restore fixture did not conflict")
		}
		evidence = append([]localstore.WorkspaceConflictEvidence{}, plan.ConflictEvidence...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.repo.WithImmediateWorkspace(context.Background(), fixture.req.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		if _, err := tx.ReplaceOpenConflictOccurrences(context.Background(), evidence, time.Date(2026, 8, 24, 15, 0, 0, 0, time.UTC)); err != nil {
			return err
		}
		return tx.SetStatus(context.Background(), "conflicted")
	}); err != nil {
		t.Fatal(err)
	}
	before, err := fixture.service.repo.Workspace(context.Background(), fixture.req.Scope)
	if err != nil {
		t.Fatal(err)
	}

	want, err := fixture.service.RestoreStash(context.Background(), fixture.req)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	var committedRevision int64
	if err := fixture.store.DB().QueryRow(`SELECT receipt.result_json,binding.workspace_revision
		FROM workspace_transition_receipts receipt JOIN workspace_bindings binding USING(project_id,workspace_id)
		WHERE receipt.project_id=? AND receipt.workspace_id=? AND receipt.request_id=?`,
		fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID, fixture.req.RequestID).Scan(&raw, &committedRevision); err != nil {
		t.Fatal(err)
	}
	var receipt restoreStashReceiptV2
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	if committedRevision != before.WorkspaceRevision+1 || receipt.WorkspaceRevision != committedRevision {
		t.Fatalf("already-conflicted receipt revision=%d committed revision=%d before=%d",
			receipt.WorkspaceRevision, committedRevision, before.WorkspaceRevision)
	}

	beforeRetry := captureStashRawState(t, fixture.store)
	got, err := fixture.service.RestoreStash(context.Background(), fixture.req)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("already-conflicted immediate retry=(%+v,%v), want (%+v,nil)", got, err, want)
	}
	if afterRetry := captureStashRawState(t, fixture.store); !reflect.DeepEqual(afterRetry, beforeRetry) {
		t.Fatal("already-conflicted immediate retry wrote state")
	}
}

func TestServiceRestoreStashConflictedV1AdapterRequiresMigrationBaselineRevision(t *testing.T) {
	fixture := newRestoreServiceFixture(t)
	current := restoreProjectName(t, fixture.accepted, "current project", 2*time.Minute)
	insertServiceCandidate(t, fixture.store, fixture.req.Scope, fixture.accepted.Digest, current, nil, 0)
	setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "pending")
	want, err := fixture.service.RestoreStash(context.Background(), fixture.req)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := fixture.store.DB().QueryRow(`SELECT result_json FROM workspace_transition_receipts
		WHERE project_id=? AND workspace_id=? AND request_id=?`, fixture.req.Scope.ProjectID,
		fixture.req.Scope.WorkspaceID, fixture.req.RequestID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var v2 restoreStashReceiptV2
	if err := json.Unmarshal(raw, &v2); err != nil {
		t.Fatal(err)
	}
	v1, err := encodeRestoreReceipt(restoreStashReceiptV1{
		SchemaVersion: 1, Action: "restore", Outcome: "conflicted",
		Result: v2.Result, ConflictRetryDigest: v2.ConflictRetryDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.DB().Exec(`UPDATE workspace_transition_receipts SET result_json=?
		WHERE project_id=? AND workspace_id=? AND request_id=?`, v1, fixture.req.Scope.ProjectID,
		fixture.req.Scope.WorkspaceID, fixture.req.RequestID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.DB().Exec(`UPDATE workspace_bindings SET workspace_revision=1
		WHERE project_id=? AND workspace_id=?`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	got, err := fixture.service.RestoreStash(context.Background(), fixture.req)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("baseline v1 retry=(%+v,%v), want (%+v,nil)", got, err, want)
	}
	if _, err := fixture.store.DB().Exec(`UPDATE workspace_bindings SET workspace_revision=2
		WHERE project_id=? AND workspace_id=?`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	before := captureStashRawState(t, fixture.store)
	got, err = fixture.service.RestoreStash(context.Background(), fixture.req)
	if err == nil || !reflect.DeepEqual(got, RestoreStashResult{}) || !strings.Contains(err.Error(), "legacy") {
		t.Fatalf("nonbaseline v1 retry=(%+v,%v)", got, err)
	}
	if after := captureStashRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
		t.Fatal("nonbaseline v1 retry wrote state")
	}
}

func TestServiceRestoreStashConflictedRereadsSemanticPostimageBeforeReceipt(t *testing.T) {
	fixture := newRestoreServiceFixture(t)
	current := restoreProjectName(t, fixture.accepted, "current project", 2*time.Minute)
	insertServiceCandidate(t, fixture.store, fixture.req.Scope, fixture.accepted.Digest, current, nil, 0)
	setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "pending")
	if _, err := fixture.store.DB().Exec(`CREATE TRIGGER restore_mutate_named_postimage
		AFTER UPDATE OF status ON workspace_bindings WHEN NEW.status='conflicted' BEGIN
		  UPDATE workspace_stashes SET label='trigger changed semantic postimage'
		  WHERE project_id=NEW.project_id AND workspace_id=NEW.workspace_id;
		END`); err != nil {
		t.Fatal(err)
	}
	before := captureStashRawState(t, fixture.store)
	got, err := fixture.service.RestoreStash(context.Background(), fixture.req)
	if err == nil || !reflect.DeepEqual(got, RestoreStashResult{}) || !strings.Contains(err.Error(), "protected current state") {
		t.Fatalf("postimage-mutated RestoreStash()=(%+v,%v)", got, err)
	}
	if after := captureStashRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
		t.Fatal("failed semantic postimage reread did not roll back")
	}
}

func TestServiceRestoreStashCleanConsumesStashAndReceiptRetryIsReadOnly(t *testing.T) {
	fixture := newRestoreServiceFixture(t)
	fixedNow := time.Date(2026, 7, 29, 12, 34, 56, 0, time.UTC)
	clockCalls := 0
	fixture.service.now = func() time.Time {
		clockCalls++
		return fixedNow
	}

	got, err := fixture.service.RestoreStash(context.Background(), fixture.req)
	if err != nil {
		t.Fatal(err)
	}
	if got.RestoredDigest != fixture.stashed.Digest || got.RebasedThroughGeneration != 0 ||
		got.Conflicts == nil || len(got.Conflicts) != 0 || got.StashRetained || clockCalls != 1 {
		t.Fatalf("RestoreStash()=%+v clock calls=%d", got, clockCalls)
	}

	var candidate *localstore.WorkspaceCandidateRecord
	var stash *localstore.WorkspaceStashRecord
	var receipt *localstore.WorkspaceTransitionReceiptRecord
	if err := fixture.service.repo.WithImmediateWorkspace(context.Background(), fixture.req.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		var err error
		candidate, err = tx.Candidate(context.Background())
		if err != nil {
			return err
		}
		stash, err = tx.Stash(context.Background(), fixture.req.StashID)
		if err != nil {
			return err
		}
		receipt, err = tx.TransitionReceipt(context.Background(), fixture.req.RequestID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	status := mustServiceStatus(t, fixture.service, fixture.req.Scope)
	if status.State != "pending" || candidate == nil || stash != nil ||
		candidate.AcceptedBaseDigest != fixture.accepted.Digest || candidate.WorkingTreeDigest != fixture.accepted.Digest ||
		candidate.DirectSnapshot.Digest != fixture.accepted.Digest || candidate.RebasedSnapshot == nil ||
		candidate.RebasedSnapshot.Digest != fixture.stashed.Digest || candidate.RebasedThroughGeneration != 0 ||
		candidate.ImportedBy != fixture.req.Actor.PrincipalID() || !candidate.ImportedAt.Equal(fixedNow) {
		t.Fatalf("post-restore status=%+v candidate=%+v stash=%+v", status, candidate, stash)
	}
	digest, err := restoreRequestDigest(fixture.req)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeRestoreReceipt(receipt, fixture.req, digest)
	if err != nil || decoded.Outcome != "clean" || !reflect.DeepEqual(decoded.Result, got) {
		t.Fatalf("decoded receipt=(%+v,%v), want %+v", decoded, err, got)
	}

	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedStore, reopenedService := openProjectStateServiceAt(t, fixture.databasePath)
	fixture.store, fixture.service = reopenedStore, reopenedService
	var reopenedStash, reopenedReceipt int
	if err := fixture.store.DB().QueryRow(`SELECT
		(SELECT COUNT(*) FROM workspace_stashes WHERE project_id=? AND workspace_id=? AND stash_id=?),
		(SELECT COUNT(*) FROM workspace_transition_receipts WHERE project_id=? AND workspace_id=? AND request_id=? AND action='restore' AND outcome='clean')
	`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID, fixture.req.StashID,
		fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID, fixture.req.RequestID).Scan(&reopenedStash, &reopenedReceipt); err != nil {
		t.Fatal(err)
	}
	if reopenedStash != 0 || reopenedReceipt != 1 {
		t.Fatalf("reopened clean restore rows=(stash=%d receipt=%d), want (0,1)", reopenedStash, reopenedReceipt)
	}
	installRestoreWriteBlockTriggers(t, fixture.store)
	before := captureStashRawState(t, fixture.store)
	fixture.service.now = func() time.Time { panic("clean receipt retry consulted clock") }
	retried, err := fixture.service.RestoreStash(context.Background(), fixture.req)
	if err != nil || !reflect.DeepEqual(retried, got) {
		t.Fatalf("retry RestoreStash()=(%+v,%v), want %+v", retried, err, got)
	}
	if after := captureStashRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
		t.Fatal("clean receipt retry changed persisted state")
	}
}

func TestServiceRestoreStashCleanPreservesCurrentProvenanceAndOperationOwnership(t *testing.T) {
	fixture := newStashServiceFixture(t)
	stashedOperation := servicePutTaskOperation(fixture.accepted,
		"90000000-0000-4000-8000-000000000011", "80000000-0000-4000-8000-000000000011", "stashed task")
	stashedSnapshot, err := state.ApplyOperation(fixture.accepted, stashedOperation)
	if err != nil {
		t.Fatal(err)
	}
	insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 1, stashedOperation, "active")
	if _, err := fixture.service.Stash(context.Background(), fixture.req); err != nil {
		t.Fatal(err)
	}

	insertServiceCandidate(t, fixture.store, fixture.req.Scope, fixture.accepted.Digest, fixture.accepted, nil, 0)
	currentOperation := servicePutTaskOperation(fixture.accepted,
		"90000000-0000-4000-8000-000000000012", "80000000-0000-4000-8000-000000000012", "current task")
	currentSnapshot, err := state.ApplyOperation(fixture.accepted, currentOperation)
	if err != nil {
		t.Fatal(err)
	}
	insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 2, currentOperation, "active")
	setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "pending")
	req := RestoreStashRequest{
		Scope: fixture.req.Scope, RequestID: "50000000-0000-4000-8000-000000000012",
		StashID: fixture.stashID, Actor: diffActorEnvelope(),
	}

	got, err := fixture.service.RestoreStash(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	wantMerge, err := ThreeWayRebase(fixture.accepted, currentSnapshot, stashedSnapshot)
	if err != nil || len(wantMerge.Conflicts) != 0 {
		t.Fatalf("fixture merge=(%+v,%v)", wantMerge, err)
	}
	if got.RestoredDigest != wantMerge.Snapshot.Digest || got.RebasedThroughGeneration != 2 ||
		got.Conflicts == nil || len(got.Conflicts) != 0 || got.StashRetained {
		t.Fatalf("RestoreStash()=%+v", got)
	}
	var candidate *localstore.WorkspaceCandidateRecord
	if err := fixture.service.repo.WithImmediateWorkspace(context.Background(), req.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		var err error
		candidate, err = tx.Candidate(context.Background())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	wantImportedAt := time.Date(2026, 7, 28, 14, 0, 0, 0, time.UTC)
	if candidate == nil || candidate.DirectSnapshot.Digest != fixture.accepted.Digest ||
		candidate.RebasedSnapshot == nil || candidate.RebasedSnapshot.Digest != wantMerge.Snapshot.Digest ||
		candidate.RebasedThroughGeneration != 2 || candidate.ImportedBy != "00000000-0000-4000-8000-000000000071" ||
		!candidate.ImportedAt.Equal(wantImportedAt) {
		t.Fatalf("restored candidate=%+v", candidate)
	}
	assertStashOperationState(t, fixture.service, req.Scope, 1, "stashed", fixture.stashID, stashedOperation)
	assertStashOperationState(t, fixture.service, req.Scope, 2, "rebased", "", currentOperation)
}

func TestServiceRestoreStashCleanPreservesSystemCurrentProvenanceAndOperationOwnership(t *testing.T) {
	fixture := newStashServiceFixture(t)
	stashedOperation := servicePutTaskOperation(fixture.accepted,
		"90000000-0000-4000-8000-000000000011", "80000000-0000-4000-8000-000000000011", "stashed task")
	stashedSnapshot, err := state.ApplyOperation(fixture.accepted, stashedOperation)
	if err != nil {
		t.Fatal(err)
	}
	insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 1, stashedOperation, "active")
	if _, err := fixture.service.Stash(context.Background(), fixture.req); err != nil {
		t.Fatal(err)
	}

	insertServiceCandidate(t, fixture.store, fixture.req.Scope, fixture.accepted.Digest, fixture.accepted, nil, 0)
	if _, err := fixture.store.DB().Exec(`UPDATE workspace_candidates SET imported_by=? WHERE project_id=? AND workspace_id=?`,
		types.CandidateImportOriginGitObservationRebaseV1, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	currentOperation := servicePutTaskOperation(fixture.accepted,
		"90000000-0000-4000-8000-000000000012", "80000000-0000-4000-8000-000000000012", "current task")
	currentSnapshot, err := state.ApplyOperation(fixture.accepted, currentOperation)
	if err != nil {
		t.Fatal(err)
	}
	insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 2, currentOperation, "active")
	setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "pending")
	req := RestoreStashRequest{
		Scope: fixture.req.Scope, RequestID: "50000000-0000-4000-8000-000000000012",
		StashID: fixture.stashID, Actor: diffActorEnvelope(),
	}

	got, err := fixture.service.RestoreStash(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	wantMerge, err := ThreeWayRebase(fixture.accepted, currentSnapshot, stashedSnapshot)
	if err != nil || len(wantMerge.Conflicts) != 0 {
		t.Fatalf("fixture merge=(%+v,%v)", wantMerge, err)
	}
	if got.RestoredDigest != wantMerge.Snapshot.Digest || got.RebasedThroughGeneration != 2 ||
		got.Conflicts == nil || len(got.Conflicts) != 0 || got.StashRetained {
		t.Fatalf("RestoreStash()=%+v", got)
	}
	var candidate *localstore.WorkspaceCandidateRecord
	if err := fixture.service.repo.WithImmediateWorkspace(context.Background(), req.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		var err error
		candidate, err = tx.Candidate(context.Background())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	wantImportedAt := time.Date(2026, 7, 28, 14, 0, 0, 0, time.UTC)
	if candidate == nil || candidate.DirectSnapshot.Digest != fixture.accepted.Digest ||
		candidate.RebasedSnapshot == nil || candidate.RebasedSnapshot.Digest != wantMerge.Snapshot.Digest ||
		candidate.RebasedThroughGeneration != 2 || candidate.ImportedBy != types.CandidateImportOriginGitObservationRebaseV1 ||
		!candidate.ImportedAt.Equal(wantImportedAt) {
		t.Fatalf("restored candidate=%+v", candidate)
	}
	if candidate.ImportedBy == req.Actor.PrincipalID() || candidate.ImportedAt.Equal(req.Actor.OccurredAt) {
		t.Fatalf("restore substituted operation actor provenance: candidate=%+v actor=%+v", candidate, req.Actor)
	}
	assertStashOperationState(t, fixture.service, req.Scope, 1, "stashed", fixture.stashID, stashedOperation)
	assertStashOperationState(t, fixture.service, req.Scope, 2, "rebased", "", currentOperation)
}

func TestServiceRestoreStashCleanResolvesOpenConflictsAndSetsPending(t *testing.T) {
	fixture := newRestoreServiceFixture(t)
	insertRestoreValidStaleConflict(t, fixture.store, fixture.req.Scope)
	setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "conflicted")
	if _, err := fixture.service.RestoreStash(context.Background(), fixture.req); err != nil {
		t.Fatal(err)
	}
	if status := mustServiceStatus(t, fixture.service, fixture.req.Scope); status.State != "pending" {
		t.Fatalf("clean restore status=%+v, want pending", status)
	}
	var open, resolved int
	if err := fixture.store.DB().QueryRow(`
		SELECT COUNT(*) FILTER (WHERE state='open'), COUNT(*) FILTER (WHERE state='resolved')
		FROM workspace_conflicts WHERE project_id=? AND workspace_id=?
	`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID).Scan(&open, &resolved); err != nil {
		t.Fatal(err)
	}
	if open != 0 || resolved != 1 {
		t.Fatalf("conflict counts=(open=%d resolved=%d), want (0,1)", open, resolved)
	}
}

func TestServiceRestoreStashCleanRollsBackEveryWriteStage(t *testing.T) {
	for _, test := range []struct {
		name       string
		newFixture func(*testing.T) restoreServiceFixture
		trigger    string
	}{
		{name: "candidate insert aborted", newFixture: newRestoreCleanInsertWriteFixture,
			trigger: `CREATE TRIGGER restore_fault BEFORE INSERT ON workspace_candidates BEGIN SELECT RAISE(ABORT,'candidate insert'); END`},
		{name: "candidate update aborted", trigger: `CREATE TRIGGER restore_fault BEFORE UPDATE ON workspace_candidates
			BEGIN SELECT RAISE(ABORT,'candidate update'); END`},
		{name: "second active transition aborted", trigger: `CREATE TRIGGER restore_fault BEFORE UPDATE OF state,stashed_by_stash_id ON workspace_overlay_operations
			WHEN OLD.generation=3 AND OLD.state='active' AND NEW.state='rebased' AND NEW.stashed_by_stash_id IS NULL
			BEGIN SELECT RAISE(ABORT,'second active transition'); END`},
		{name: "conflict replacement ignored", trigger: `CREATE TRIGGER restore_fault BEFORE UPDATE OF state ON workspace_conflicts
			WHEN OLD.state='open' AND NEW.state='resolved' BEGIN SELECT RAISE(IGNORE); END`},
		{name: "status aborted", trigger: `CREATE TRIGGER restore_fault BEFORE UPDATE OF status ON workspace_bindings
			BEGIN SELECT RAISE(ABORT,'restore status'); END`},
		{name: "stash delete ignored", trigger: `CREATE TRIGGER restore_fault BEFORE DELETE ON workspace_stashes
			BEGIN SELECT RAISE(IGNORE); END`},
		{name: "receipt insert ignored", trigger: `CREATE TRIGGER restore_fault BEFORE INSERT ON workspace_transition_receipts
			WHEN NEW.action='restore' BEGIN SELECT RAISE(IGNORE); END`},
	} {
		t.Run(test.name, func(t *testing.T) {
			newFixture := test.newFixture
			if newFixture == nil {
				newFixture = newRestoreCleanWriteFixture
			}
			fixture := newFixture(t)
			if _, err := fixture.store.DB().Exec(test.trigger); err != nil {
				t.Fatal(err)
			}
			before := captureStashRawState(t, fixture.store)
			got, err := fixture.service.RestoreStash(context.Background(), fixture.req)
			if err == nil || errors.Is(err, ErrIdempotencyConflict) || errors.Is(err, localstore.ErrCommitOutcomeUnknown) ||
				!reflect.DeepEqual(got, RestoreStashResult{}) {
				t.Fatalf("faulted RestoreStash()=(%+v,%v), want ordinary zero failure", got, err)
			}
			if after := captureStashRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
				t.Fatal("write-stage failure changed complete raw state")
			}
			if err := fixture.store.Close(); err != nil {
				t.Fatal(err)
			}
			reopenedStore, _ := openProjectStateServiceAt(t, fixture.databasePath)
			if after := captureStashRawState(t, reopenedStore); !reflect.DeepEqual(after, before) {
				t.Fatal("write-stage rollback changed complete raw state after restart")
			}
		})
	}
}

func TestServiceRestoreStashCleanWriteOrderIsExact(t *testing.T) {
	for _, test := range []struct {
		name       string
		newFixture func(*testing.T) restoreServiceFixture
		candidate  string
	}{
		{name: "insert candidate", newFixture: newRestoreCleanInsertWriteFixture, candidate: "candidate_insert"},
		{name: "update candidate", newFixture: newRestoreCleanWriteFixture, candidate: "candidate_update"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := test.newFixture(t)
			installRestoreWriteOrderLog(t, fixture.store)
			if _, err := fixture.service.RestoreStash(context.Background(), fixture.req); err != nil {
				t.Fatal(err)
			}
			want := []string{test.candidate, "operation:2", "operation:3", "conflict_resolve", "status:pending", "stash_delete", "receipt:clean"}
			if got := restoreWriteOrder(t, fixture.store); !reflect.DeepEqual(got, want) {
				t.Fatalf("clean write order=%v, want %v", got, want)
			}
		})
	}
}

func TestServiceRestoreStashConflictedWriteOrderIsExact(t *testing.T) {
	fixture := newRestoreConflictedWriteFixture(t)
	installRestoreWriteOrderLog(t, fixture.store)
	installRestoreConflictedProtectedWriteBlockers(t, fixture.store)
	got, err := fixture.service.RestoreStash(context.Background(), fixture.req)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"conflict_resolve"}
	for _, conflict := range got.Conflicts {
		want = append(want, "conflict_insert:"+conflict.ID)
	}
	want = append(want, "receipt:conflicted")
	if order := restoreWriteOrder(t, fixture.store); !reflect.DeepEqual(order, want) {
		t.Fatalf("conflicted write order=%v, want %v", order, want)
	}
}

func TestServiceRestoreStashConflictedRollsBackEveryWriteStage(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, restoreServiceFixture)
		trigger string
	}{
		{name: "stale conflict resolution", trigger: `CREATE TRIGGER restore_fault BEFORE UPDATE OF state,resolved_at ON workspace_conflicts
			WHEN OLD.state='open' AND NEW.state='resolved' BEGIN SELECT RAISE(ABORT,'conflict resolve'); END`},
		{name: "desired conflict insertion", trigger: `CREATE TRIGGER restore_fault BEFORE INSERT ON workspace_conflicts
			WHEN NEW.state='open' BEGIN SELECT RAISE(ABORT,'conflict insert'); END`},
		{name: "conflicted status", prepare: func(t *testing.T, fixture restoreServiceFixture) {
			result, err := fixture.store.DB().Exec(`
				DELETE FROM workspace_conflicts WHERE project_id=? AND workspace_id=?
			`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID)
			if err != nil {
				t.Fatal(err)
			}
			deleted, err := result.RowsAffected()
			if err != nil || deleted != 1 {
				t.Fatalf("delete seeded restore conflict=(%d,%v), want (1,nil)", deleted, err)
			}
			setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "pending")
		}, trigger: `CREATE TRIGGER restore_fault BEFORE UPDATE OF status ON workspace_bindings
			WHEN NEW.status='conflicted' BEGIN SELECT RAISE(ABORT,'conflicted status'); END`},
		{name: "conflicted receipt", trigger: `CREATE TRIGGER restore_fault BEFORE INSERT ON workspace_transition_receipts
			WHEN NEW.action='restore' AND NEW.outcome='conflicted' BEGIN SELECT RAISE(ABORT,'conflicted receipt'); END`},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRestoreConflictedWriteFixture(t)
			if test.prepare != nil {
				test.prepare(t, fixture)
			}
			if _, err := fixture.store.DB().Exec(test.trigger); err != nil {
				t.Fatal(err)
			}
			before := captureStashRawState(t, fixture.store)
			got, err := fixture.service.RestoreStash(context.Background(), fixture.req)
			if err == nil || errors.Is(err, ErrIdempotencyConflict) || errors.Is(err, localstore.ErrCommitOutcomeUnknown) ||
				!reflect.DeepEqual(got, RestoreStashResult{}) {
				t.Fatalf("faulted conflicted RestoreStash()=(%+v,%v), want ordinary zero failure", got, err)
			}
			if after := captureStashRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
				t.Fatal("conflicted write-stage failure changed complete raw state")
			}
			if err := fixture.store.Close(); err != nil {
				t.Fatal(err)
			}
			reopenedStore, _ := openProjectStateServiceAt(t, fixture.databasePath)
			if after := captureStashRawState(t, reopenedStore); !reflect.DeepEqual(after, before) {
				t.Fatal("conflicted write-stage rollback changed complete raw state after restart")
			}
		})
	}
}

func TestServiceRestoreStashKeepsSiblingWorkspaceIsolated(t *testing.T) {
	fixture := newRestoreServiceFixture(t)
	siblingRoot := filepath.Join(t.TempDir(), "same-project-restore-sibling")
	runGit(t, fixture.binding.Checkout.CanonicalPath, "worktree", "add", "-b", "restore-sibling",
		siblingRoot, fixture.binding.AcceptedCommitSHA)
	sibling, err := fixture.service.RegisterWorkspace(context.Background(), RegisterWorkspaceRequest{
		Root: siblingRoot, ExpectedProjectID: fixture.binding.Scope.ProjectID,
		ExpectedRepository: fixture.binding.Repository, ExpectedCommit: fixture.binding.AcceptedCommitSHA,
	})
	if err != nil {
		t.Fatal(err)
	}
	siblingAccepted := mustServiceStatus(t, fixture.service, sibling.Binding.Scope).AcceptedSnapshot
	siblingOperation := servicePutTaskOperation(siblingAccepted,
		"90000000-0000-4000-8000-000000000021", "80000000-0000-4000-8000-000000000021", "sibling task")
	insertStashServiceOperation(t, fixture.store, sibling.Binding.Scope, 1, siblingOperation, "active")
	setServiceWorkspaceState(t, fixture.store, sibling.Binding.Scope, "pending")

	if _, err := fixture.service.RestoreStash(context.Background(), fixture.req); err != nil {
		t.Fatal(err)
	}
	status := mustServiceStatus(t, fixture.service, sibling.Binding.Scope)
	if status.State != "pending" || status.OverlayGeneration != 1 {
		t.Fatalf("sibling status=%+v", status)
	}
	assertStashOperationState(t, fixture.service, sibling.Binding.Scope, 1, "active", "", siblingOperation)
	if err := fixture.service.repo.WithImmediateWorkspace(context.Background(), sibling.Binding.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		stash, err := tx.Stash(context.Background(), fixture.req.StashID)
		if err != nil {
			return err
		}
		if stash != nil {
			t.Fatalf("sibling exposed restored workspace stash: %+v", stash)
		}
		receipt, err := tx.TransitionReceipt(context.Background(), fixture.req.RequestID)
		if err != nil {
			return err
		}
		if receipt != nil {
			t.Fatalf("sibling exposed restored workspace receipt: %+v", receipt)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestServiceRestoreStashConflictRetainsStateAndExactRetryIsReadOnly(t *testing.T) {
	fixture := newRestoreServiceFixture(t)
	current := restoreProjectName(t, fixture.accepted, "current project", 2*time.Minute)
	insertServiceCandidate(t, fixture.store, fixture.req.Scope, fixture.accepted.Digest, current, nil, 0)
	active := servicePutTaskOperation(current,
		"90000000-0000-4000-8000-000000000081", "80000000-0000-4000-8000-000000000081", "current active")
	currentComposed, err := state.ApplyOperation(current, active)
	if err != nil {
		t.Fatal(err)
	}
	terminal := servicePutTaskOperation(fixture.accepted,
		"90000000-0000-4000-8000-000000000091", "80000000-0000-4000-8000-000000000091", "terminal audit")
	insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 8, active, "active")
	insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 9, terminal, "materialized")
	insertRestoreValidStaleConflict(t, fixture.store, fixture.req.Scope)
	setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "conflicted")
	fixedNow := time.Date(2026, 7, 29, 13, 0, 0, 0, time.UTC)
	clockCalls := 0
	fixture.service.now = func() time.Time { clockCalls++; return fixedNow }

	beforeBinding := captureRestoreProtectedBindingRows(t, fixture.store)
	beforeCandidate := captureRestoreCandidateRows(t, fixture.store)
	beforeOperations := captureRestoreOperationRows(t, fixture.store)
	beforeStashes := captureRestoreStashRows(t, fixture.store)
	got, err := fixture.service.RestoreStash(context.Background(), fixture.req)
	if err != nil {
		t.Fatal(err)
	}
	if got.RestoredDigest != currentComposed.Digest || got.RebasedThroughGeneration != 8 ||
		len(got.Conflicts) == 0 || !got.StashRetained || clockCalls != 1 {
		t.Fatalf("conflicted RestoreStash()=%+v clock calls=%d", got, clockCalls)
	}
	if status := mustServiceStatus(t, fixture.service, fixture.req.Scope); status.State != "conflicted" {
		t.Fatalf("conflicted status=%+v", status)
	}
	if after := captureRestoreCandidateRows(t, fixture.store); !reflect.DeepEqual(after, beforeCandidate) {
		t.Fatal("conflicted restore changed candidate bytes")
	}
	if after := captureRestoreOperationRows(t, fixture.store); !reflect.DeepEqual(after, beforeOperations) {
		t.Fatal("conflicted restore changed operation bytes")
	}
	if after := captureRestoreStashRows(t, fixture.store); !reflect.DeepEqual(after, beforeStashes) {
		t.Fatal("conflicted restore changed stash bytes")
	}
	if after := captureRestoreProtectedBindingRows(t, fixture.store); !reflect.DeepEqual(after, beforeBinding) {
		t.Fatal("conflicted restore changed accepted binding or snapshot bytes")
	}
	assertStashOperationState(t, fixture.service, fixture.req.Scope, 8, "active", "", active)
	assertStashOperationState(t, fixture.service, fixture.req.Scope, 9, "materialized", "", terminal)
	var openConflicts []localstore.WorkspaceConflictOccurrence
	if err := fixture.service.repo.WithImmediateWorkspace(context.Background(), fixture.req.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		var err error
		openConflicts, err = tx.OpenConflictOccurrences(context.Background())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	decodedConflicts, err := decodeWorkspaceConflictOccurrences(openConflicts)
	if err != nil || !reflect.DeepEqual(decodedConflicts, got.Conflicts) {
		t.Fatalf("persisted open conflicts=(%+v,%v), want %+v", decodedConflicts, err, got.Conflicts)
	}
	var resolved int
	if err := fixture.store.DB().QueryRow(`SELECT COUNT(*) FROM workspace_conflicts
		WHERE project_id=? AND workspace_id=? AND state='resolved'`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID).Scan(&resolved); err != nil || resolved != 1 {
		t.Fatalf("resolved stale conflicts=(%d,%v), want 1", resolved, err)
	}
	digest, err := restoreRequestDigest(fixture.req)
	if err != nil {
		t.Fatal(err)
	}
	commitErr := fmt.Errorf("%w: fixture", localstore.ErrCommitOutcomeUnknown)
	confirmed, err := confirmRestoreStashCommit(context.Background(), fixture.service.repo, fixture.req, digest, got, commitErr)
	if err != nil || !reflect.DeepEqual(confirmed, got) {
		t.Fatalf("conflicted confirmRestoreStashCommit()=(%+v,%v), want %+v", confirmed, err, got)
	}
	if len(confirmed.Conflicts[0].Base.Value) != 0 {
		confirmed.Conflicts[0].Base.Value[0] ^= 0xff
	}

	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedStore, reopenedService := openProjectStateServiceAt(t, fixture.databasePath)
	fixture.store, fixture.service = reopenedStore, reopenedService
	installRestoreWriteBlockTriggers(t, fixture.store)
	beforeRetry := captureStashRawState(t, fixture.store)
	fixture.service.now = func() time.Time { panic("conflicted receipt retry consulted clock") }
	retried, err := fixture.service.RestoreStash(context.Background(), fixture.req)
	if err != nil || !reflect.DeepEqual(retried, got) {
		t.Fatalf("conflicted retry RestoreStash()=(%+v,%v), want %+v", retried, err, got)
	}
	if after := captureStashRawState(t, fixture.store); !reflect.DeepEqual(after, beforeRetry) {
		t.Fatal("conflicted receipt retry changed persisted state")
	}

	collision := fixture.req
	collision.Actor = diffActorEnvelope()
	collision.Actor.HumanPrincipalID = "00000000-0000-4000-8000-000000000099"
	collisionBefore := captureStashRawState(t, fixture.store)
	failed, err := fixture.service.RestoreStash(context.Background(), collision)
	if !errors.Is(err, ErrIdempotencyConflict) || !reflect.DeepEqual(failed, RestoreStashResult{}) {
		t.Fatalf("collision RestoreStash()=(%+v,%v)", failed, err)
	}
	if after := captureStashRawState(t, fixture.store); !reflect.DeepEqual(after, collisionBefore) {
		t.Fatal("idempotency collision changed persisted state")
	}
}

func TestServiceRestoreStashConflictedRetryIgnoresRawBindingTimestampDrift(t *testing.T) {
	fixture := newRestoreServiceFixture(t)
	current := restoreProjectName(t, fixture.accepted, "current project", 2*time.Minute)
	insertServiceCandidate(t, fixture.store, fixture.req.Scope, fixture.accepted.Digest, current, nil, 0)
	setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "pending")
	fixture.service.now = func() time.Time { return time.Date(2026, 7, 29, 13, 30, 0, 0, time.UTC) }
	want, err := fixture.service.RestoreStash(context.Background(), fixture.req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.DB().Exec(`
		UPDATE workspace_bindings SET updated_at='2026-07-29 14:30:00+00:00'
		WHERE project_id=? AND workspace_id=?
	`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	before := captureStashRawState(t, fixture.store)
	fixture.service.now = func() time.Time { panic("drifted retry consulted clock") }
	got, err := fixture.service.RestoreStash(context.Background(), fixture.req)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("timestamp-only retry RestoreStash()=(%+v,%v), want (%+v,nil)", got, err, want)
	}
	if after := captureStashRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
		t.Fatal("failed drifted retry changed persisted state")
	}
}

func TestServiceRestoreStashConflictedRetryAfterRestartOwnsResult(t *testing.T) {
	fixture := newRestoreServiceFixture(t)
	current := restoreProjectName(t, fixture.accepted, "current project", 2*time.Minute)
	insertServiceCandidate(t, fixture.store, fixture.req.Scope, fixture.accepted.Digest, current, nil, 0)
	setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "pending")
	got, err := fixture.service.RestoreStash(context.Background(), fixture.req)
	if err != nil || len(got.Conflicts) == 0 {
		t.Fatalf("first conflicted RestoreStash()=(%+v,%v)", got, err)
	}
	want := cloneRestoreStashResult(got)
	if len(got.Conflicts[0].Base.Value) != 0 {
		got.Conflicts[0].Base.Value[0] ^= 0xff
	} else if len(got.Conflicts[0].Ours.Value) != 0 {
		got.Conflicts[0].Ours.Value[0] ^= 0xff
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedStore, reopenedService := openProjectStateServiceAt(t, fixture.databasePath)
	fixture.store, fixture.service = reopenedStore, reopenedService
	fixture.service.now = func() time.Time { panic("restart retry consulted clock") }
	retried, err := fixture.service.RestoreStash(context.Background(), fixture.req)
	if err != nil || !reflect.DeepEqual(retried, want) {
		t.Fatalf("restart retry RestoreStash()=(%+v,%v), want %+v", retried, err, want)
	}
	if reflect.DeepEqual(got, retried) {
		t.Fatal("mutating initial public result did not change the caller-owned value")
	}
}

func TestServiceRestoreStashConflictedRetryDriftMatrixAfterRestart(t *testing.T) {
	for _, test := range []struct {
		name         string
		mutate       func(*testing.T, restoreServiceFixture)
		wantContains string
		wantSentinel error
		wantSuccess  bool
	}{
		{name: "binding ref digest", wantContains: "retry state mismatch", mutate: func(t *testing.T, fixture restoreServiceFixture) {
			if _, err := fixture.store.DB().Exec(`UPDATE workspace_bindings SET accepted_ref='refs/heads/retry-drift'
				WHERE project_id=? AND workspace_id=?`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "binding created at excluded", wantSuccess: true, mutate: func(t *testing.T, fixture restoreServiceFixture) {
			if _, err := fixture.store.DB().Exec(`UPDATE workspace_bindings SET created_at='2025-01-02 03:04:05+00:00'
				WHERE project_id=? AND workspace_id=?`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "accepted snapshot raw structural", wantContains: "accepted snapshot", mutate: func(t *testing.T, fixture restoreServiceFixture) {
			if _, err := fixture.store.DB().Exec(`UPDATE workspace_bindings SET accepted_snapshot=CAST(accepted_snapshot||X'00' AS BLOB)
				WHERE project_id=? AND workspace_id=?`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "candidate attribution timestamp excluded", wantSuccess: true, mutate: func(t *testing.T, fixture restoreServiceFixture) {
			if _, err := fixture.store.DB().Exec(`UPDATE workspace_candidates SET imported_at='2032-01-02 03:04:05+00:00'
				WHERE project_id=? AND workspace_id=?`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "candidate raw structural", wantContains: "candidate", mutate: func(t *testing.T, fixture restoreServiceFixture) {
			if _, err := fixture.store.DB().Exec(`UPDATE workspace_candidates SET direct_tree=CAST(direct_tree||X'00' AS BLOB)
				WHERE project_id=? AND workspace_id=?`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "retained stash label digest", wantContains: "retry state mismatch", mutate: func(t *testing.T, fixture restoreServiceFixture) {
			if _, err := fixture.store.DB().Exec(`UPDATE workspace_stashes SET label='changed after receipt'
				WHERE project_id=? AND workspace_id=? AND stash_id=?`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID, fixture.req.StashID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "retained stash actor digest", wantContains: "retry state mismatch", mutate: func(t *testing.T, fixture restoreServiceFixture) {
			actor := diffActorEnvelope()
			actor.HumanPrincipalID = "00000000-0000-4000-8000-000000000098"
			raw, err := state.CanonicalJSON(actor)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.store.DB().Exec(`UPDATE workspace_stashes SET actor_json=?
				WHERE project_id=? AND workspace_id=? AND stash_id=?`, string(raw), fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID, fixture.req.StashID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "retained stash timestamp excluded", wantSuccess: true, mutate: func(t *testing.T, fixture restoreServiceFixture) {
			if _, err := fixture.store.DB().Exec(`UPDATE workspace_stashes SET created_at='2032-01-02 03:04:05+00:00'
				WHERE project_id=? AND workspace_id=? AND stash_id=?`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID, fixture.req.StashID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "retained stash replay structural", wantSentinel: ErrStashCorrupt, mutate: func(t *testing.T, fixture restoreServiceFixture) {
			if _, err := fixture.store.DB().Exec(`UPDATE workspace_stashes SET operations_json=operations_json||' '
				WHERE project_id=? AND workspace_id=? AND stash_id=?`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID, fixture.req.StashID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "retained stash source blob structural", wantContains: "decode persisted workspace stash source tree", mutate: func(t *testing.T, fixture restoreServiceFixture) {
			if _, err := fixture.store.DB().Exec(`UPDATE workspace_stashes SET source_tree=CAST(source_tree||X'00' AS BLOB)
				WHERE project_id=? AND workspace_id=? AND stash_id=?`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID, fixture.req.StashID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "retained stash composed blob structural", wantContains: "decode persisted workspace stash composed tree", mutate: func(t *testing.T, fixture restoreServiceFixture) {
			if _, err := fixture.store.DB().Exec(`UPDATE workspace_stashes SET composed_tree=CAST(composed_tree||X'00' AS BLOB)
				WHERE project_id=? AND workspace_id=? AND stash_id=?`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID, fixture.req.StashID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "conflict created at excluded", wantSuccess: true, mutate: func(t *testing.T, fixture restoreServiceFixture) {
			if _, err := fixture.store.DB().Exec(`UPDATE workspace_conflicts SET created_at='2032-01-02 03:04:05+00:00'
				WHERE project_id=? AND workspace_id=? AND state='open'`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "conflict occurrence digest", wantContains: "retry state mismatch", mutate: func(t *testing.T, fixture restoreServiceFixture) {
			if _, err := fixture.store.DB().Exec(`UPDATE workspace_conflicts SET occurrence_id='70000000-0000-4000-8000-000000000099'
				WHERE project_id=? AND workspace_id=? AND state='open' AND rowid=(SELECT MIN(rowid) FROM workspace_conflicts
				WHERE project_id=? AND workspace_id=? AND state='open')`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID,
				fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "semantic conflict evidence", wantContains: "conflict evidence mismatch", mutate: func(t *testing.T, fixture restoreServiceFixture) {
			var occurrences []localstore.WorkspaceConflictOccurrence
			if err := fixture.service.repo.WithImmediateWorkspace(context.Background(), fixture.req.Scope, func(tx *localstore.WorkspaceMutationTx) error {
				var err error
				occurrences, err = tx.OpenConflictOccurrences(context.Background())
				return err
			}); err != nil {
				t.Fatal(err)
			}
			conflicts, err := decodeWorkspaceConflictOccurrences(occurrences)
			if err != nil || len(conflicts) == 0 {
				t.Fatalf("decode open conflicts=(%+v,%v)", conflicts, err)
			}
			original := conflicts[0]
			changed, err := newConflict(original.Key, original.FieldPath, original.Kind,
				original.Base, original.Theirs, original.Ours)
			if err != nil || changed.ID == original.ID {
				t.Fatalf("build changed semantic conflict=(%+v,%v)", changed, err)
			}
			evidence, err := encodeWorkspaceConflictEvidence([]Conflict{changed})
			if err != nil || len(evidence) != 1 {
				t.Fatalf("encode changed evidence=(%+v,%v)", evidence, err)
			}
			changedEvidence := evidence[0]
			if _, err := fixture.store.DB().Exec(`
				UPDATE workspace_conflicts
				SET conflict_id=?, record_kind=?, record_id=?, field_path=?, conflict_kind=?,
				    base_json=?, ours_json=?, theirs_json=?
				WHERE project_id=? AND workspace_id=? AND occurrence_id=? AND state='open'
			`, changedEvidence.ConflictID, changedEvidence.Key.Kind, changedEvidence.Key.ID,
				changedEvidence.FieldPath, changedEvidence.ConflictKind, changedEvidence.BaseJSON,
				changedEvidence.OursJSON, changedEvidence.TheirsJSON, fixture.req.Scope.ProjectID,
				fixture.req.Scope.WorkspaceID, occurrences[0].OccurrenceID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "current active created at excluded", wantSuccess: true, mutate: func(t *testing.T, fixture restoreServiceFixture) {
			if _, err := fixture.store.DB().Exec(`UPDATE workspace_overlay_operations SET created_at='2032-01-02 03:04:05+00:00'
				WHERE project_id=? AND workspace_id=? AND generation=8`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unrelated operation created at excluded", wantSuccess: true, mutate: func(t *testing.T, fixture restoreServiceFixture) {
			if _, err := fixture.store.DB().Exec(`UPDATE workspace_overlay_operations SET created_at='2032-01-02 03:04:05+00:00'
				WHERE project_id=? AND workspace_id=? AND generation=9`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "absorbed current row structural", wantSentinel: ErrStashOperationMismatch, mutate: func(t *testing.T, fixture restoreServiceFixture) {
			if _, err := fixture.store.DB().Exec(`UPDATE workspace_overlay_operations SET state='rebased'
				WHERE project_id=? AND workspace_id=? AND generation=8`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRestoreServiceFixture(t)
			current := restoreProjectName(t, fixture.accepted, "current project", 2*time.Minute)
			insertServiceCandidate(t, fixture.store, fixture.req.Scope, fixture.accepted.Digest, current, nil, 0)
			active := servicePutTaskOperation(current,
				"90000000-0000-4000-8000-000000000081", "80000000-0000-4000-8000-000000000081", "absorbed current")
			terminal := servicePutTaskOperation(fixture.accepted,
				"90000000-0000-4000-8000-000000000091", "80000000-0000-4000-8000-000000000091", "unrelated terminal")
			insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 8, active, "active")
			insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 9, terminal, "materialized")
			setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "pending")
			want, err := fixture.service.RestoreStash(context.Background(), fixture.req)
			if err != nil || len(want.Conflicts) == 0 {
				t.Fatalf("first conflicted RestoreStash()=(%+v,%v)", want, err)
			}
			if err := fixture.store.Close(); err != nil {
				t.Fatal(err)
			}
			reopenedStore, reopenedService := openProjectStateServiceAt(t, fixture.databasePath)
			fixture.store, fixture.service = reopenedStore, reopenedService
			test.mutate(t, fixture)
			before := captureStashRawState(t, fixture.store)
			fixture.service.now = func() time.Time { panic("drift retry consulted clock") }
			got, err := fixture.service.RestoreStash(context.Background(), fixture.req)
			if test.wantSuccess {
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("excluded raw drift RestoreStash()=(%+v,%v), want (%+v,nil)", got, err, want)
				}
				if after := captureStashRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
					t.Fatal("read-only raw-drift retry changed persisted state")
				}
				return
			}
			if err == nil || errors.Is(err, ErrIdempotencyConflict) || !reflect.DeepEqual(got, RestoreStashResult{}) {
				t.Fatalf("drifted RestoreStash()=(%+v,%v), want ordinary zero failure", got, err)
			}
			if test.wantContains != "" && !strings.Contains(err.Error(), test.wantContains) {
				t.Fatalf("drift error=%v, want %q layer", err, test.wantContains)
			}
			if test.wantSentinel != nil && !errors.Is(err, test.wantSentinel) {
				t.Fatalf("drift error=%v, want sentinel %v", err, test.wantSentinel)
			}
			if after := captureStashRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
				t.Fatal("failed drifted retry changed persisted state")
			}
		})
	}
}

func TestServiceRestoreStashConflictPreservesAbsorbedPrefixAndSparseLaterRowsAcrossRestart(t *testing.T) {
	fixture := newRestoreSparseConflictFixture(t)
	first, err := fixture.service.RestoreStash(context.Background(), fixture.req)
	if err != nil || len(first.Conflicts) == 0 || !first.StashRetained {
		t.Fatalf("first RestoreStash()=(%+v,%v)", first, err)
	}
	assertStashOperationState(t, fixture.service, fixture.req.Scope, 4, "stashed", fixture.stashID, fixture.absorbed)
	assertStashOperationState(t, fixture.service, fixture.req.Scope, 7, "stashed", fixture.stashID, fixture.laterOne)
	assertStashOperationState(t, fixture.service, fixture.req.Scope, 10, "stashed", fixture.stashID, fixture.laterTwo)
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	_, reopened := openProjectStateServiceAt(t, fixture.databasePath)
	reopened.now = func() time.Time { panic("restart sparse retry consulted clock") }
	retried, err := reopened.RestoreStash(context.Background(), fixture.req)
	if err != nil || !reflect.DeepEqual(retried, first) {
		t.Fatalf("restart RestoreStash()=(%+v,%v), want %+v", retried, err, first)
	}
	assertStashOperationState(t, reopened, fixture.req.Scope, 4, "stashed", fixture.stashID, fixture.absorbed)
	assertStashOperationState(t, reopened, fixture.req.Scope, 7, "stashed", fixture.stashID, fixture.laterOne)
	assertStashOperationState(t, reopened, fixture.req.Scope, 10, "stashed", fixture.stashID, fixture.laterTwo)
}

func TestServiceRestoreStashConflictedRetryRejectsStashOwnedRowDriftAfterRestart(t *testing.T) {
	for _, test := range []struct {
		name         string
		mutate       func(*testing.T, restoreSparseConflictFixture)
		wantContains string
		wantSentinel error
		wantSuccess  bool
	}{
		{name: "absorbed created at excluded", wantSuccess: true, mutate: func(t *testing.T, fixture restoreSparseConflictFixture) {
			if _, err := fixture.store.DB().Exec(`UPDATE workspace_overlay_operations SET created_at='2033-01-02 03:04:05+00:00'
				WHERE project_id=? AND workspace_id=? AND generation=4`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "later created at excluded", wantSuccess: true, mutate: func(t *testing.T, fixture restoreSparseConflictFixture) {
			if _, err := fixture.store.DB().Exec(`UPDATE workspace_overlay_operations SET created_at='2033-01-02 03:04:05+00:00'
				WHERE project_id=? AND workspace_id=? AND generation=10`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "absorbed owner structural", wantSentinel: ErrStashOperationMismatch, mutate: func(t *testing.T, fixture restoreSparseConflictFixture) {
			if _, err := fixture.store.DB().Exec(`UPDATE workspace_overlay_operations
				SET stashed_by_stash_id='30000000-0000-4000-8000-000000000099'
				WHERE project_id=? AND workspace_id=? AND generation=4`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRestoreSparseConflictFixture(t)
			want, err := fixture.service.RestoreStash(context.Background(), fixture.req)
			if err != nil || len(want.Conflicts) == 0 {
				t.Fatalf("first conflicted RestoreStash()=(%+v,%v)", want, err)
			}
			if err := fixture.store.Close(); err != nil {
				t.Fatal(err)
			}
			reopenedStore, reopenedService := openProjectStateServiceAt(t, fixture.databasePath)
			fixture.store, fixture.service = reopenedStore, reopenedService
			test.mutate(t, fixture)
			before := captureStashRawState(t, fixture.store)
			fixture.service.now = func() time.Time { panic("stash-owned drift retry consulted clock") }
			got, err := fixture.service.RestoreStash(context.Background(), fixture.req)
			if test.wantSuccess {
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("excluded stash-row timestamp RestoreStash()=(%+v,%v), want (%+v,nil)", got, err, want)
				}
				if after := captureStashRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
					t.Fatal("read-only stash timestamp retry changed state")
				}
				return
			}
			if err == nil || errors.Is(err, ErrIdempotencyConflict) || !reflect.DeepEqual(got, RestoreStashResult{}) {
				t.Fatalf("stash-owned drift RestoreStash()=(%+v,%v)", got, err)
			}
			if test.wantContains != "" && !strings.Contains(err.Error(), test.wantContains) {
				t.Fatalf("stash-owned drift error=%v, want %q layer", err, test.wantContains)
			}
			if test.wantSentinel != nil && !errors.Is(err, test.wantSentinel) {
				t.Fatalf("stash-owned drift error=%v, want sentinel %v", err, test.wantSentinel)
			}
			if after := captureStashRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
				t.Fatal("failed stash-owned drift retry changed state")
			}
		})
	}
}

func TestServiceRestoreStashConflictedIgnoresRawStatusTimestampRewrite(t *testing.T) {
	fixture := newRestoreServiceFixture(t)
	current := restoreProjectName(t, fixture.accepted, "current project", 2*time.Minute)
	insertServiceCandidate(t, fixture.store, fixture.req.Scope, fixture.accepted.Digest, current, nil, 0)
	setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "pending")
	if _, err := fixture.store.DB().Exec(`
		CREATE TRIGGER restore_tamper_updated_at AFTER UPDATE OF status ON workspace_bindings
		WHEN NEW.status='conflicted' BEGIN
		  UPDATE workspace_bindings SET updated_at='2030-01-02 03:04:05+00:00'
		  WHERE project_id=NEW.project_id AND workspace_id=NEW.workspace_id;
		END;
	`); err != nil {
		t.Fatal(err)
	}
	got, err := fixture.service.RestoreStash(context.Background(), fixture.req)
	if err != nil || len(got.Conflicts) == 0 {
		t.Fatalf("timestamp-rewritten RestoreStash()=(%+v,%v), want conflicted success", got, err)
	}
}

func TestServiceRestoreStashCrossActionReceiptIsIdempotencyConflict(t *testing.T) {
	fixture := newRestoreServiceFixture(t)
	collision := fixture.req
	collision.RequestID = fixture.stashServiceFixture.req.RequestID
	before := captureStashRawState(t, fixture.store)
	got, err := fixture.service.RestoreStash(context.Background(), collision)
	if !errors.Is(err, ErrIdempotencyConflict) || !reflect.DeepEqual(got, RestoreStashResult{}) {
		t.Fatalf("cross-action RestoreStash()=(%+v,%v)", got, err)
	}
	if after := captureStashRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
		t.Fatal("cross-action collision changed persisted state")
	}
}

func TestServiceRestoreStashCommitUnknownReturnsZeroAndRetrySucceeds(t *testing.T) {
	fixture := newRestoreServiceFixture(t)
	if _, err := fixture.store.DB().Exec(`
		CREATE TABLE restore_deferred_failure(
		  project_id TEXT NOT NULL,
		  workspace_id TEXT NOT NULL,
		  FOREIGN KEY(project_id,workspace_id) REFERENCES workspace_bindings(project_id,workspace_id)
		    DEFERRABLE INITIALLY DEFERRED
		);
		CREATE TRIGGER restore_fail_commit AFTER INSERT ON workspace_transition_receipts
		WHEN NEW.action='restore' BEGIN
		  INSERT INTO restore_deferred_failure(project_id,workspace_id)
		  VALUES ('00000000-0000-4000-8000-000000000099','00000000-0000-4000-8000-000000000098');
		END;
	`); err != nil {
		t.Fatal(err)
	}
	before := captureStashRawState(t, fixture.store)
	got, err := fixture.service.RestoreStash(context.Background(), fixture.req)
	if !errors.Is(err, localstore.ErrCommitOutcomeUnknown) || errors.Is(err, ErrIdempotencyConflict) ||
		!reflect.DeepEqual(got, RestoreStashResult{}) {
		t.Fatalf("unknown RestoreStash()=(%+v,%v)", got, err)
	}
	if after := captureStashRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
		t.Fatal("failed restore commit changed state")
	}
	if _, err := fixture.store.DB().Exec(`DROP TRIGGER restore_fail_commit; DROP TABLE restore_deferred_failure`); err != nil {
		t.Fatal(err)
	}
	retried, err := fixture.service.RestoreStash(context.Background(), fixture.req)
	if err != nil || retried.RestoredDigest != fixture.stashed.Digest || retried.StashRetained {
		t.Fatalf("same-request retry RestoreStash()=(%+v,%v)", retried, err)
	}
}

func TestServiceRestoreStashConflictedCommitUnknownReopensByteIdenticalAndRetrySucceeds(t *testing.T) {
	fixture := newRestoreConflictedWriteFixture(t)
	if _, err := fixture.store.DB().Exec(`
		CREATE TABLE restore_conflicted_deferred_failure(
		  project_id TEXT NOT NULL,
		  workspace_id TEXT NOT NULL,
		  FOREIGN KEY(project_id,workspace_id) REFERENCES workspace_bindings(project_id,workspace_id)
		    DEFERRABLE INITIALLY DEFERRED
		);
		CREATE TRIGGER restore_conflicted_fail_commit AFTER INSERT ON workspace_transition_receipts
		WHEN NEW.action='restore' AND NEW.outcome='conflicted' BEGIN
		  INSERT INTO restore_conflicted_deferred_failure(project_id,workspace_id)
		  VALUES ('00000000-0000-4000-8000-000000000099','00000000-0000-4000-8000-000000000098');
		END;
	`); err != nil {
		t.Fatal(err)
	}
	before := captureStashRawState(t, fixture.store)
	got, err := fixture.service.RestoreStash(context.Background(), fixture.req)
	if !errors.Is(err, localstore.ErrCommitOutcomeUnknown) || errors.Is(err, ErrIdempotencyConflict) ||
		!reflect.DeepEqual(got, RestoreStashResult{}) {
		t.Fatalf("unknown conflicted RestoreStash()=(%+v,%v)", got, err)
	}
	if after := captureStashRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
		t.Fatal("failed conflicted commit changed immediate raw state")
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedStore, reopenedService := openProjectStateServiceAt(t, fixture.databasePath)
	fixture.store, fixture.service = reopenedStore, reopenedService
	if after := captureStashRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
		t.Fatal("failed conflicted commit changed reopened raw state")
	}
	if _, err := fixture.store.DB().Exec(`DROP TRIGGER restore_conflicted_fail_commit; DROP TABLE restore_conflicted_deferred_failure`); err != nil {
		t.Fatal(err)
	}
	retried, err := fixture.service.RestoreStash(context.Background(), fixture.req)
	if err != nil || len(retried.Conflicts) == 0 || !retried.StashRetained {
		t.Fatalf("same-request conflicted retry RestoreStash()=(%+v,%v)", retried, err)
	}
}

func TestConfirmRestoreStashCommitRequiresExactReceipt(t *testing.T) {
	fixture := newRestoreServiceFixture(t)
	want, err := fixture.service.RestoreStash(context.Background(), fixture.req)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := restoreRequestDigest(fixture.req)
	if err != nil {
		t.Fatal(err)
	}
	commitErr := fmt.Errorf("%w: fixture", localstore.ErrCommitOutcomeUnknown)
	got, err := confirmRestoreStashCommit(context.Background(), fixture.service.repo, fixture.req, digest, want, commitErr)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("confirmRestoreStashCommit()=(%+v,%v), want %+v", got, err, want)
	}

	missing := fixture.req
	missing.RequestID = "50000000-0000-4000-8000-000000000099"
	missingDigest, err := restoreRequestDigest(missing)
	if err != nil {
		t.Fatal(err)
	}
	got, err = confirmRestoreStashCommit(context.Background(), fixture.service.repo, missing, missingDigest, want, commitErr)
	if !errors.Is(err, localstore.ErrCommitOutcomeUnknown) || errors.Is(err, ErrIdempotencyConflict) ||
		!reflect.DeepEqual(got, RestoreStashResult{}) {
		t.Fatalf("missing confirmRestoreStashCommit()=(%+v,%v)", got, err)
	}
}

func TestConfirmRestoreStashCommitFailureMatrixPreservesUnknownOutcome(t *testing.T) {
	commitErr := fmt.Errorf("%w: fixture", localstore.ErrCommitOutcomeUnknown)
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, restoreServiceFixture, *RestoreStashResult)
	}{
		{name: "malformed receipt", mutate: func(t *testing.T, fixture restoreServiceFixture, _ *RestoreStashResult) {
			if _, err := fixture.store.DB().Exec(`UPDATE workspace_transition_receipts SET result_json='{}'||char(10)
				WHERE project_id=? AND workspace_id=? AND request_id=?`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID, fixture.req.RequestID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "cross action", mutate: func(t *testing.T, fixture restoreServiceFixture, _ *RestoreStashResult) {
			if _, err := fixture.store.DB().Exec(`UPDATE workspace_transition_receipts SET action='stash'
				WHERE project_id=? AND workspace_id=? AND request_id=?`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID, fixture.req.RequestID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "digest mismatch", mutate: func(t *testing.T, fixture restoreServiceFixture, _ *RestoreStashResult) {
			bad := state.Digest("sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")
			if _, err := fixture.store.DB().Exec(`UPDATE workspace_transition_receipts SET request_digest=?
				WHERE project_id=? AND workspace_id=? AND request_id=?`, bad, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID, fixture.req.RequestID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "result mismatch", mutate: func(_ *testing.T, _ restoreServiceFixture, expected *RestoreStashResult) {
			expected.RebasedThroughGeneration++
		}},
		{name: "unavailable", mutate: func(t *testing.T, fixture restoreServiceFixture, _ *RestoreStashResult) {
			if err := fixture.store.Close(); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRestoreServiceFixture(t)
			want, err := fixture.service.RestoreStash(context.Background(), fixture.req)
			if err != nil {
				t.Fatal(err)
			}
			digest, err := restoreRequestDigest(fixture.req)
			if err != nil {
				t.Fatal(err)
			}
			expected := cloneRestoreStashResult(want)
			test.mutate(t, fixture, &expected)
			got, err := confirmRestoreStashCommit(context.Background(), fixture.service.repo, fixture.req, digest, expected, commitErr)
			if !errors.Is(err, localstore.ErrCommitOutcomeUnknown) || errors.Is(err, ErrIdempotencyConflict) ||
				!reflect.DeepEqual(got, RestoreStashResult{}) {
				t.Fatalf("confirmRestoreStashCommit()=(%+v,%v), want zero commit-unknown non-conflict error", got, err)
			}
		})
	}
}

func TestConfirmRestoreStashConflictedCommitIgnoresRawTimestampDrift(t *testing.T) {
	fixture := newRestoreServiceFixture(t)
	current := restoreProjectName(t, fixture.accepted, "current project", 2*time.Minute)
	insertServiceCandidate(t, fixture.store, fixture.req.Scope, fixture.accepted.Digest, current, nil, 0)
	setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "pending")
	want, err := fixture.service.RestoreStash(context.Background(), fixture.req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.DB().Exec(`UPDATE workspace_bindings SET updated_at='2031-01-02 03:04:05+00:00'
		WHERE project_id=? AND workspace_id=?`, fixture.req.Scope.ProjectID, fixture.req.Scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	digest, err := restoreRequestDigest(fixture.req)
	if err != nil {
		t.Fatal(err)
	}
	commitErr := fmt.Errorf("%w: fixture", localstore.ErrCommitOutcomeUnknown)
	got, err := confirmRestoreStashCommit(context.Background(), fixture.service.repo, fixture.req, digest, want, commitErr)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("timestamp-only confirmRestoreStashCommit()=(%+v,%v), want (%+v,nil)", got, err, want)
	}
}

type restoreServiceFixture struct {
	stashServiceFixture
	req     RestoreStashRequest
	stashed state.Snapshot
}

type restoreSparseConflictFixture struct {
	stashServiceFixture
	req                          RestoreStashRequest
	absorbed, laterOne, laterTwo state.OperationV1
}

func newRestoreServiceFixture(t *testing.T) restoreServiceFixture {
	t.Helper()
	fixture := newStashServiceFixture(t)
	stashed := restoreProjectName(t, fixture.accepted, "stashed project", time.Minute)
	insertServiceCandidate(t, fixture.store, fixture.req.Scope, fixture.accepted.Digest, stashed, nil, 0)
	setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "pending")
	if _, err := fixture.service.Stash(context.Background(), fixture.req); err != nil {
		t.Fatal(err)
	}
	return restoreServiceFixture{
		stashServiceFixture: fixture,
		req: RestoreStashRequest{
			Scope: fixture.req.Scope, RequestID: "50000000-0000-4000-8000-000000000001",
			StashID: fixture.stashID, Actor: diffActorEnvelope(),
		},
		stashed: stashed,
	}
}

func newRestoreSparseConflictFixture(t *testing.T) restoreSparseConflictFixture {
	t.Helper()
	fixture := newStashServiceFixture(t)
	stashedDirect := restoreProjectName(t, fixture.accepted, "stashed project", time.Minute)
	absorbed := servicePutTaskOperation(stashedDirect,
		"90000000-0000-4000-8000-000000000041", "80000000-0000-4000-8000-000000000041", "absorbed")
	rebased, err := state.ApplyOperation(stashedDirect, absorbed)
	if err != nil {
		t.Fatal(err)
	}
	laterOne := servicePutTaskOperation(rebased,
		"90000000-0000-4000-8000-000000000071", "80000000-0000-4000-8000-000000000071", "later one")
	afterOne, err := state.ApplyOperation(rebased, laterOne)
	if err != nil {
		t.Fatal(err)
	}
	laterTwo := servicePutTaskOperation(afterOne,
		"90000000-0000-4000-8000-000000000010", "80000000-0000-4000-8000-000000000010", "later two")
	insertServiceCandidate(t, fixture.store, fixture.req.Scope, fixture.accepted.Digest, stashedDirect, &rebased, 4)
	insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 4, absorbed, "rebased")
	insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 7, laterOne, "active")
	insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 10, laterTwo, "active")
	setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "pending")
	if _, err := fixture.service.Stash(context.Background(), fixture.req); err != nil {
		t.Fatal(err)
	}
	current := restoreProjectName(t, fixture.accepted, "current project", 2*time.Minute)
	insertServiceCandidate(t, fixture.store, fixture.req.Scope, fixture.accepted.Digest, current, nil, 0)
	setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "pending")
	return restoreSparseConflictFixture{
		stashServiceFixture: fixture,
		req: RestoreStashRequest{
			Scope: fixture.req.Scope, RequestID: "50000000-0000-4000-8000-000000000041",
			StashID: fixture.stashID, Actor: diffActorEnvelope(),
		},
		absorbed: absorbed, laterOne: laterOne, laterTwo: laterTwo,
	}
}

func newRestoreCleanWriteFixture(t *testing.T) restoreServiceFixture {
	t.Helper()
	fixture := newRestoreServiceFixture(t)
	insertServiceCandidate(t, fixture.store, fixture.req.Scope, fixture.accepted.Digest, fixture.accepted, nil, 0)
	prepareRestoreCleanWriteRows(t, fixture)
	return fixture
}

func newRestoreCleanInsertWriteFixture(t *testing.T) restoreServiceFixture {
	t.Helper()
	fixture := newRestoreServiceFixture(t)
	prepareRestoreCleanWriteRows(t, fixture)
	return fixture
}

func prepareRestoreCleanWriteRows(t *testing.T, fixture restoreServiceFixture) {
	t.Helper()
	first := servicePutTaskOperation(fixture.accepted,
		"90000000-0000-4000-8000-000000000002", "80000000-0000-4000-8000-000000000002", "clean active")
	afterFirst, err := state.ApplyOperation(fixture.accepted, first)
	if err != nil {
		t.Fatal(err)
	}
	second := servicePutTaskOperation(afterFirst,
		"90000000-0000-4000-8000-000000000003", "80000000-0000-4000-8000-000000000003", "clean active two")
	insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 2, first, "active")
	insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 3, second, "active")
	insertRestoreValidStaleConflict(t, fixture.store, fixture.req.Scope)
	setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "conflicted")
}

func newRestoreConflictedWriteFixture(t *testing.T) restoreServiceFixture {
	t.Helper()
	fixture := newRestoreServiceFixture(t)
	current := restoreProjectName(t, fixture.accepted, "current project", 2*time.Minute)
	insertServiceCandidate(t, fixture.store, fixture.req.Scope, fixture.accepted.Digest, current, nil, 0)
	active := servicePutTaskOperation(current,
		"90000000-0000-4000-8000-000000000081", "80000000-0000-4000-8000-000000000081", "conflicted active")
	terminal := servicePutTaskOperation(fixture.accepted,
		"90000000-0000-4000-8000-000000000091", "80000000-0000-4000-8000-000000000091", "conflicted terminal")
	insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 8, active, "active")
	insertStashServiceOperation(t, fixture.store, fixture.req.Scope, 9, terminal, "materialized")
	insertRestoreValidStaleConflict(t, fixture.store, fixture.req.Scope)
	setServiceWorkspaceState(t, fixture.store, fixture.req.Scope, "conflicted")
	return fixture
}

func insertRestoreValidStaleConflict(t *testing.T, store *localstore.Store, scope types.WorkspaceScope) {
	t.Helper()
	conflict := restoreCodecConflictedResult(t).Conflicts[0]
	evidence, err := encodeWorkspaceConflictEvidence([]Conflict{conflict})
	if err != nil || len(evidence) != 1 {
		t.Fatalf("encode stale restore conflict=(%+v,%v)", evidence, err)
	}
	row := evidence[0]
	if _, err := store.DB().Exec(`
		INSERT INTO workspace_conflicts
		(project_id,workspace_id,occurrence_id,conflict_id,record_kind,record_id,field_path,
		 conflict_kind,base_json,ours_json,theirs_json,state)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,'open')
	`, scope.ProjectID, scope.WorkspaceID, "70000000-0000-4000-8000-000000000001",
		row.ConflictID, row.Key.Kind, row.Key.ID, row.FieldPath, row.ConflictKind,
		row.BaseJSON, row.OursJSON, row.TheirsJSON); err != nil {
		t.Fatal(err)
	}
}

func installRestoreWriteBlockTriggers(t *testing.T, store *localstore.Store) {
	t.Helper()
	if _, err := store.DB().Exec(`
		CREATE TRIGGER restore_block_candidate_insert BEFORE INSERT ON workspace_candidates BEGIN SELECT RAISE(ABORT,'candidate insert'); END;
		CREATE TRIGGER restore_block_candidate_update BEFORE UPDATE ON workspace_candidates BEGIN SELECT RAISE(ABORT,'candidate update'); END;
		CREATE TRIGGER restore_block_candidate_delete BEFORE DELETE ON workspace_candidates BEGIN SELECT RAISE(ABORT,'candidate delete'); END;
		CREATE TRIGGER restore_block_operation_insert BEFORE INSERT ON workspace_overlay_operations BEGIN SELECT RAISE(ABORT,'operation insert'); END;
		CREATE TRIGGER restore_block_operation_update BEFORE UPDATE ON workspace_overlay_operations BEGIN SELECT RAISE(ABORT,'operation update'); END;
		CREATE TRIGGER restore_block_operation_delete BEFORE DELETE ON workspace_overlay_operations BEGIN SELECT RAISE(ABORT,'operation delete'); END;
		CREATE TRIGGER restore_block_conflict_insert BEFORE INSERT ON workspace_conflicts BEGIN SELECT RAISE(ABORT,'conflict insert'); END;
		CREATE TRIGGER restore_block_conflict_update BEFORE UPDATE ON workspace_conflicts BEGIN SELECT RAISE(ABORT,'conflict update'); END;
		CREATE TRIGGER restore_block_conflict_delete BEFORE DELETE ON workspace_conflicts BEGIN SELECT RAISE(ABORT,'conflict delete'); END;
		CREATE TRIGGER restore_block_binding_insert BEFORE INSERT ON workspace_bindings BEGIN SELECT RAISE(ABORT,'binding insert'); END;
		CREATE TRIGGER restore_block_binding_update BEFORE UPDATE ON workspace_bindings BEGIN SELECT RAISE(ABORT,'binding update'); END;
		CREATE TRIGGER restore_block_binding_delete BEFORE DELETE ON workspace_bindings BEGIN SELECT RAISE(ABORT,'binding delete'); END;
		CREATE TRIGGER restore_block_stash_insert BEFORE INSERT ON workspace_stashes BEGIN SELECT RAISE(ABORT,'stash insert'); END;
		CREATE TRIGGER restore_block_stash_update BEFORE UPDATE ON workspace_stashes BEGIN SELECT RAISE(ABORT,'stash update'); END;
		CREATE TRIGGER restore_block_stash_delete BEFORE DELETE ON workspace_stashes BEGIN SELECT RAISE(ABORT,'stash delete'); END;
		CREATE TRIGGER restore_block_receipt_insert BEFORE INSERT ON workspace_transition_receipts BEGIN SELECT RAISE(ABORT,'receipt insert'); END;
		CREATE TRIGGER restore_block_receipt_update BEFORE UPDATE ON workspace_transition_receipts BEGIN SELECT RAISE(ABORT,'receipt update'); END;
		CREATE TRIGGER restore_block_receipt_delete BEFORE DELETE ON workspace_transition_receipts BEGIN SELECT RAISE(ABORT,'receipt delete'); END;
	`); err != nil {
		t.Fatal(err)
	}
}

func installRestoreWriteOrderLog(t *testing.T, store *localstore.Store) {
	t.Helper()
	if _, err := store.DB().Exec(`
		CREATE TABLE restore_write_order(sequence INTEGER PRIMARY KEY AUTOINCREMENT, stage TEXT NOT NULL);
		CREATE TRIGGER restore_order_candidate_insert AFTER INSERT ON workspace_candidates BEGIN INSERT INTO restore_write_order(stage) VALUES ('candidate_insert'); END;
		CREATE TRIGGER restore_order_candidate_update AFTER UPDATE ON workspace_candidates BEGIN INSERT INTO restore_write_order(stage) VALUES ('candidate_update'); END;
		CREATE TRIGGER restore_order_candidate_delete AFTER DELETE ON workspace_candidates BEGIN INSERT INTO restore_write_order(stage) VALUES ('candidate_unexpected_delete'); END;
		CREATE TRIGGER restore_order_operation_insert AFTER INSERT ON workspace_overlay_operations BEGIN INSERT INTO restore_write_order(stage) VALUES ('operation_unexpected_insert'); END;
		CREATE TRIGGER restore_order_operation_update AFTER UPDATE ON workspace_overlay_operations BEGIN INSERT INTO restore_write_order(stage) VALUES ('operation:'||NEW.generation); END;
		CREATE TRIGGER restore_order_operation_delete AFTER DELETE ON workspace_overlay_operations BEGIN INSERT INTO restore_write_order(stage) VALUES ('operation_unexpected_delete'); END;
		CREATE TRIGGER restore_order_conflict_insert AFTER INSERT ON workspace_conflicts BEGIN INSERT INTO restore_write_order(stage) VALUES ('conflict_insert:'||NEW.conflict_id); END;
		CREATE TRIGGER restore_order_conflict_update AFTER UPDATE ON workspace_conflicts BEGIN INSERT INTO restore_write_order(stage) VALUES ('conflict_resolve'); END;
		CREATE TRIGGER restore_order_conflict_delete AFTER DELETE ON workspace_conflicts BEGIN INSERT INTO restore_write_order(stage) VALUES ('conflict_unexpected_delete'); END;
		CREATE TRIGGER restore_order_status AFTER UPDATE OF status ON workspace_bindings BEGIN INSERT INTO restore_write_order(stage) VALUES ('status:'||NEW.status); END;
		CREATE TRIGGER restore_order_stash_insert AFTER INSERT ON workspace_stashes BEGIN INSERT INTO restore_write_order(stage) VALUES ('stash_unexpected_insert'); END;
		CREATE TRIGGER restore_order_stash_update AFTER UPDATE ON workspace_stashes BEGIN INSERT INTO restore_write_order(stage) VALUES ('stash_unexpected_update'); END;
		CREATE TRIGGER restore_order_stash_delete AFTER DELETE ON workspace_stashes BEGIN INSERT INTO restore_write_order(stage) VALUES ('stash_delete'); END;
		CREATE TRIGGER restore_order_receipt_insert AFTER INSERT ON workspace_transition_receipts BEGIN INSERT INTO restore_write_order(stage) VALUES ('receipt:'||NEW.outcome); END;
		CREATE TRIGGER restore_order_receipt_update AFTER UPDATE ON workspace_transition_receipts BEGIN INSERT INTO restore_write_order(stage) VALUES ('receipt_unexpected_update'); END;
		CREATE TRIGGER restore_order_receipt_delete AFTER DELETE ON workspace_transition_receipts BEGIN INSERT INTO restore_write_order(stage) VALUES ('receipt_unexpected_delete'); END;
	`); err != nil {
		t.Fatal(err)
	}
}

func installRestoreConflictedProtectedWriteBlockers(t *testing.T, store *localstore.Store) {
	t.Helper()
	if _, err := store.DB().Exec(`
		CREATE TRIGGER restore_protect_candidate_insert BEFORE INSERT ON workspace_candidates BEGIN SELECT RAISE(ABORT,'candidate insert forbidden'); END;
		CREATE TRIGGER restore_protect_candidate_update BEFORE UPDATE ON workspace_candidates BEGIN SELECT RAISE(ABORT,'candidate update forbidden'); END;
		CREATE TRIGGER restore_protect_candidate_delete BEFORE DELETE ON workspace_candidates BEGIN SELECT RAISE(ABORT,'candidate delete forbidden'); END;
		CREATE TRIGGER restore_protect_operation_insert BEFORE INSERT ON workspace_overlay_operations BEGIN SELECT RAISE(ABORT,'operation insert forbidden'); END;
		CREATE TRIGGER restore_protect_operation_update BEFORE UPDATE ON workspace_overlay_operations BEGIN SELECT RAISE(ABORT,'operation update forbidden'); END;
		CREATE TRIGGER restore_protect_operation_delete BEFORE DELETE ON workspace_overlay_operations BEGIN SELECT RAISE(ABORT,'operation delete forbidden'); END;
		CREATE TRIGGER restore_protect_stash_insert BEFORE INSERT ON workspace_stashes BEGIN SELECT RAISE(ABORT,'stash insert forbidden'); END;
		CREATE TRIGGER restore_protect_stash_update BEFORE UPDATE ON workspace_stashes BEGIN SELECT RAISE(ABORT,'stash update forbidden'); END;
		CREATE TRIGGER restore_protect_stash_delete BEFORE DELETE ON workspace_stashes BEGIN SELECT RAISE(ABORT,'stash delete forbidden'); END;
	`); err != nil {
		t.Fatal(err)
	}
}

func restoreWriteOrder(t *testing.T, store *localstore.Store) []string {
	t.Helper()
	rows, err := store.DB().Query(`SELECT stage FROM restore_write_order ORDER BY sequence`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	order := make([]string, 0)
	for rows.Next() {
		var stage string
		if err := rows.Scan(&stage); err != nil {
			t.Fatal(err)
		}
		order = append(order, stage)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return order
}

func restoreProjectName(t *testing.T, snapshot state.Snapshot, name string, offset time.Duration) state.Snapshot {
	t.Helper()
	changed := diffCloneSnapshot(t, snapshot)
	changed.Project.Name = name
	changed.Project.UpdatedAt = changed.Project.UpdatedAt.Add(offset)
	return diffCanonicalSnapshot(t, changed)
}

func captureRestoreCandidateRows(t *testing.T, store *localstore.Store) [][]string {
	t.Helper()
	return queryImportRawRows(t, store, `
		SELECT quote(project_id),quote(workspace_id),quote(accepted_base_digest),quote(working_tree_digest),
		       quote(direct_tree),quote(rebased_tree),quote(rebased_through_generation),quote(imported_by),quote(imported_at)
		FROM workspace_candidates ORDER BY project_id,workspace_id`)
}

func captureRestoreOperationRows(t *testing.T, store *localstore.Store) [][]string {
	t.Helper()
	return queryImportRawRows(t, store, `
		SELECT quote(project_id),quote(workspace_id),quote(generation),quote(operation_id),quote(operation_json),
		       quote(state),quote(stashed_by_stash_id),quote(created_at)
		FROM workspace_overlay_operations ORDER BY project_id,workspace_id,generation`)
}

func captureRestoreStashRows(t *testing.T, store *localstore.Store) [][]string {
	t.Helper()
	return queryImportRawRows(t, store, `
		SELECT quote(project_id),quote(workspace_id),quote(stash_id),quote(source_base_digest),quote(candidate_digest),
		       quote(source_tree),quote(composed_tree),quote(operations_json),quote(through_generation),
		       quote(actor_json),quote(label),quote(created_at)
		FROM workspace_stashes ORDER BY project_id,workspace_id,stash_id`)
}

func captureRestoreProtectedBindingRows(t *testing.T, store *localstore.Store) [][]string {
	t.Helper()
	return queryImportRawRows(t, store, `
		SELECT quote(project_id),quote(workspace_id),quote(checkout_path),quote(checkout_device),quote(checkout_inode),
		       quote(repository_identity_json),quote(accepted_ref),quote(accepted_commit),quote(accepted_digest),
		       quote(accepted_snapshot),quote(created_at)
		FROM workspace_bindings ORDER BY project_id,workspace_id`)
}
