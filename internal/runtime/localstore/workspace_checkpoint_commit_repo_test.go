package localstore

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
	sqlite "modernc.org/sqlite"
)

func TestWorkspaceCheckpointCommitAPIExists(t *testing.T) {
	var tx *WorkspaceMutationTx
	state, err := tx.CaptureCheckpointCommitState(context.Background())
	if err == nil || !reflect.DeepEqual(state, WorkspaceCheckpointCommitState{}) {
		t.Fatalf("nil capture=(%+v,%v), want (zero,error)", state, err)
	}

	var repo *WorkspaceRepo
	match, err := repo.ConfirmCheckpointCommit(
		context.Background(),
		WorkspaceCheckpointCommitState{},
		WorkspaceCheckpointCommitState{},
	)
	if err == nil || match != WorkspaceCheckpointCommitThird {
		t.Fatalf("nil confirmation=(%v,%v), want (Third,error)", match, err)
	}
}

func TestWorkspaceCheckpointCommitClassifiesExactPriorAndNext(t *testing.T) {
	_, repo, binding := checkpointCommitFixture(t, "")
	prior := captureCheckpointCommitState(t, repo, binding.Scope)
	next := rolledBackCheckpointCommitState(t, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.conn.ExecContext(context.Background(), `
			UPDATE workspace_bindings SET status='pending'
			WHERE project_id=? AND workspace_id=?
		`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		return err
	})

	priorBefore := cloneWorkspaceCheckpointCommitState(prior)
	nextBefore := cloneWorkspaceCheckpointCommitState(next)
	match, err := repo.ConfirmCheckpointCommit(context.Background(), prior, next)
	if err != nil || match != WorkspaceCheckpointCommitPrior {
		t.Fatalf("prior confirmation=(%v,%v), want (Prior,nil)", match, err)
	}
	if !reflect.DeepEqual(prior, priorBefore) || !reflect.DeepEqual(next, nextBefore) {
		t.Fatal("confirmation mutated a supplied checkpoint commit state")
	}
	if got := classifyWorkspaceCheckpointCommitState(next, next, next); got != WorkspaceCheckpointCommitNext {
		t.Fatalf("next-first classifier=%v, want Next", got)
	}
	if got, err := repo.ConfirmCheckpointCommit(context.Background(), prior, prior); err == nil || got != WorkspaceCheckpointCommitThird {
		t.Fatalf("identical confirmation=(%v,%v), want (Third,error)", got, err)
	}

	commitCheckpointMutation(t, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.conn.ExecContext(context.Background(), `
			UPDATE workspace_bindings SET status='pending'
			WHERE project_id=? AND workspace_id=?
		`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		return err
	})
	match, err = repo.ConfirmCheckpointCommit(context.Background(), prior, next)
	if err != nil || match != WorkspaceCheckpointCommitNext {
		t.Fatalf("next confirmation=(%v,%v), want (Next,nil)", match, err)
	}
}

func TestWorkspaceCheckpointCommitClassifiesEveryChangedBoundaryAsThird(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *Store, *WorkspaceRepo, types.WorkspaceBinding)
	}{
		{"binding state", func(t *testing.T, store *Store, _ *WorkspaceRepo, binding types.WorkspaceBinding) {
			checkpointCommitExec(t, store.DB(), `UPDATE workspace_bindings SET status='blocked' WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		}},
		{"binding raw timestamp evidence", func(t *testing.T, store *Store, _ *WorkspaceRepo, binding types.WorkspaceBinding) {
			checkpointCommitExec(t, store.DB(), `UPDATE workspace_bindings SET updated_at=strftime('%Y-%m-%dT%H:%M:%SZ',updated_at) WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		}},
		{"current policy transition", func(t *testing.T, _ *Store, repo *WorkspaceRepo, binding types.WorkspaceBinding) {
			commitCheckpointMutation(t, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
				current, err := tx.PublicationPolicy(context.Background())
				if err != nil {
					return err
				}
				next := publicationOriginInvalidation(binding.Repository, current.PolicyRevision+1, 'b')
				_, err = tx.ReconfigurePublication(context.Background(), WorkspacePublicationPolicyTransition{Expected: current, Next: next})
				return err
			})
		}},
		{"current policy raw metadata", func(t *testing.T, store *Store, _ *WorkspaceRepo, binding types.WorkspaceBinding) {
			checkpointCommitExec(t, store.DB(), `UPDATE workspace_publication_policies SET updated_at='2099-01-01T00:00:00Z' WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		}},
		{"policy history raw metadata", func(t *testing.T, store *Store, _ *WorkspaceRepo, binding types.WorkspaceBinding) {
			checkpointCommitExec(t, store.DB(), `UPDATE workspace_publication_policy_history SET recorded_at='2099-01-01T00:00:00Z' WHERE project_id=? AND workspace_id=? AND policy_revision=2`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		}},
		{"journal public field", func(t *testing.T, store *Store, _ *WorkspaceRepo, binding types.WorkspaceBinding) {
			checkpointCommitExec(t, store.DB(), `UPDATE workspace_materializations SET stage_path='/changed-stage' WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		}},
		{"journal private mutation metadata", func(t *testing.T, store *Store, _ *WorkspaceRepo, binding types.WorkspaceBinding) {
			checkpointCommitExec(t, store.DB(), `UPDATE workspace_materializations SET updated_at='2026-07-28T12:00:01Z' WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		}},
		{"operation public field", func(t *testing.T, store *Store, _ *WorkspaceRepo, binding types.WorkspaceBinding) {
			checkpointCommitExec(t, store.DB(), `UPDATE workspace_overlay_operations SET state='discarded' WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		}},
		{"operation raw timestamp evidence", func(t *testing.T, store *Store, _ *WorkspaceRepo, binding types.WorkspaceBinding) {
			checkpointCommitExec(t, store.DB(), `UPDATE workspace_overlay_operations SET created_at=strftime('%Y-%m-%dT%H:%M:%SZ',created_at) WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		}},
		{"operation nullable storage evidence", func(t *testing.T, store *Store, _ *WorkspaceRepo, binding types.WorkspaceBinding) {
			checkpointCommitExec(t, store.DB(), `UPDATE workspace_overlay_operations SET state='stashed',stashed_by_stash_id='00000000-0000-4000-8000-000000000031' WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		}},
		{"candidate canonical blob", func(t *testing.T, _ *Store, repo *WorkspaceRepo, binding types.WorkspaceBinding) {
			candidate := workspaceCandidateRecord(t, binding, false, 0)
			candidate.DirectSnapshot.Project.Name = "changed candidate"
			candidate.DirectSnapshot.Project.UpdatedAt = candidate.DirectSnapshot.Project.UpdatedAt.Add(time.Minute)
			candidate.DirectSnapshot, _ = encodedSnapshot(t, candidate.DirectSnapshot)
			candidate.WorkingTreeDigest = candidate.DirectSnapshot.Digest
			commitCheckpointMutation(t, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
				return tx.UpsertCandidate(context.Background(), candidate)
			})
		}},
		{"candidate import evidence", func(t *testing.T, store *Store, _ *WorkspaceRepo, binding types.WorkspaceBinding) {
			checkpointCommitExec(t, store.DB(), `UPDATE workspace_candidates SET imported_by=? WHERE project_id=? AND workspace_id=?`, types.CandidateImportOriginGitObservationRebaseV1, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		}},
		{"candidate raw timestamp evidence", func(t *testing.T, store *Store, _ *WorkspaceRepo, binding types.WorkspaceBinding) {
			checkpointCommitExec(t, store.DB(), `UPDATE workspace_candidates SET imported_at='2026-07-28T14:00:00Z' WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		}},
		{"candidate rebased storage evidence", func(t *testing.T, _ *Store, repo *WorkspaceRepo, binding types.WorkspaceBinding) {
			candidate := workspaceCandidateRecord(t, binding, true, 1)
			commitCheckpointMutation(t, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
				return tx.UpsertCandidate(context.Background(), candidate)
			})
		}},
		{"candidate absence", func(t *testing.T, store *Store, _ *WorkspaceRepo, binding types.WorkspaceBinding) {
			checkpointCommitExec(t, store.DB(), `DELETE FROM workspace_candidates WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, repo, binding := checkpointCommitFixture(t, "")
			prior := captureCheckpointCommitState(t, repo, binding.Scope)
			next := rolledBackCheckpointCommitState(t, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
				_, err := tx.conn.ExecContext(context.Background(), `UPDATE workspace_bindings SET status='pending' WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
				return err
			})
			test.mutate(t, store, repo, binding)
			match, err := repo.ConfirmCheckpointCommit(context.Background(), prior, next)
			if err != nil || match != WorkspaceCheckpointCommitThird {
				t.Fatalf("changed boundary confirmation=(%v,%v), want (Third,nil)", match, err)
			}
		})
	}
}

func TestWorkspaceCheckpointCommitRejectsInvalidTokensAndCrossScope(t *testing.T) {
	_, repo, a := checkpointCommitFixture(t, "")
	b := createBinding(t, repo, a.Scope.ProjectID, "00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	prior := captureCheckpointCommitState(t, repo, a.Scope)
	next := rolledBackCheckpointCommitState(t, repo, a.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.conn.ExecContext(context.Background(), `UPDATE workspace_bindings SET status='pending' WHERE project_id=? AND workspace_id=?`, a.Scope.ProjectID, a.Scope.WorkspaceID)
		return err
	})
	other := captureCheckpointCommitState(t, repo, b.Scope)

	tests := []struct {
		name        string
		prior, next WorkspaceCheckpointCommitState
	}{
		{"zero prior", WorkspaceCheckpointCommitState{}, next},
		{"zero next", prior, WorkspaceCheckpointCommitState{}},
		{"unknown version", func() WorkspaceCheckpointCommitState { value := prior; value.version++; return value }(), next},
		{"malformed scope", func() WorkspaceCheckpointCommitState {
			value := prior
			value.scope = types.WorkspaceScope{}
			return value
		}(), next},
		{"mismatched internal scope", func() WorkspaceCheckpointCommitState {
			value := prior
			value.binding.Record.Binding.Scope = b.Scope
			return value
		}(), next},
		{"malformed raw storage evidence", func() WorkspaceCheckpointCommitState {
			value := cloneWorkspaceCheckpointCommitState(prior)
			value.adjacent.Operations[0].StorageClasses[7] = "blob"
			return value
		}(), next},
		{"missing publication origin raw evidence", checkpointCommitPolicyRawPresenceMismatch(prior, 3), next},
		{"missing publication actor raw evidence", checkpointCommitPolicyRawPresenceMismatch(prior, 7), next},
		{"missing publication changed-at raw evidence", checkpointCommitPolicyRawPresenceMismatch(prior, 8), next},
		{"mismatched publication origin raw evidence", func() WorkspaceCheckpointCommitState {
			value := cloneWorkspaceCheckpointCommitState(prior)
			changed := state.Digest("sha256:" + strings.Repeat("f", 64))
			value.policy.Record.OriginDigest = &changed
			return value
		}(), next},
		{"mismatched publication actor raw evidence", func() WorkspaceCheckpointCommitState {
			value := cloneWorkspaceCheckpointCommitState(prior)
			value.policy.Record.ChangedBy.HumanPrincipalID = "00000000-0000-4000-8000-000000000099"
			return value
		}(), next},
		{"mismatched publication changed-at raw evidence", func() WorkspaceCheckpointCommitState {
			value := cloneWorkspaceCheckpointCommitState(prior)
			changed := value.policy.Record.ChangedAt.Add(time.Minute)
			value.policy.Record.ChangedAt = &changed
			return value
		}(), next},
		{"reordered journals", checkpointCommitReorderedJournals(prior), next},
		{"duplicate journal ID", checkpointCommitDuplicateJournal(prior), next},
		{"reordered operations", checkpointCommitMalformedOperations(t, prior, "reordered"), next},
		{"duplicate operation generation", checkpointCommitMalformedOperations(t, prior, "duplicate generation"), next},
		{"duplicate operation ID", checkpointCommitMalformedOperations(t, prior, "duplicate ID"), next},
		{"cross scope", prior, other},
		{"identical", prior, prior},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			match, err := repo.ConfirmCheckpointCommit(context.Background(), test.prior, test.next)
			if err == nil || match != WorkspaceCheckpointCommitThird {
				t.Fatalf("invalid confirmation=(%v,%v), want (Third,error)", match, err)
			}
		})
	}
}

func checkpointCommitPolicyRawPresenceMismatch(value WorkspaceCheckpointCommitState, index int) WorkspaceCheckpointCommitState {
	value = cloneWorkspaceCheckpointCommitState(value)
	records := []*workspacePublicationRawRecord{&value.policy, &value.policyHistory[len(value.policyHistory)-1]}
	for _, record := range records {
		switch index {
		case 3:
			record.OriginValue = sql.NullString{}
		case 7:
			record.ActorJSON = sql.NullString{}
		case 8:
			record.ChangedAtRaw = sql.NullString{}
		}
		record.StorageClasses[index] = "null"
	}
	return value
}

func checkpointCommitReorderedJournals(value WorkspaceCheckpointCommitState) WorkspaceCheckpointCommitState {
	value = cloneWorkspaceCheckpointCommitState(value)
	first := value.materialization.Journals[0]
	second := cloneWorkspaceMaterializationRecord(first)
	second.JournalID = "zzzz-journal"
	value.materialization.Journals = []WorkspaceMaterializationRecord{second, first}
	return value
}

func checkpointCommitDuplicateJournal(value WorkspaceCheckpointCommitState) WorkspaceCheckpointCommitState {
	value = cloneWorkspaceCheckpointCommitState(value)
	duplicate := cloneWorkspaceMaterializationRecord(value.materialization.Journals[0])
	value.materialization.Journals = append(value.materialization.Journals, duplicate)
	return value
}

func checkpointCommitMalformedOperations(t *testing.T, value WorkspaceCheckpointCommitState, kind string) WorkspaceCheckpointCommitState {
	t.Helper()
	value = cloneWorkspaceCheckpointCommitState(value)
	first := cloneWorkspaceCheckpointOperation(value.materialization.Operations[0])
	second := WorkspaceOperation{
		Generation:  2,
		OperationID: "00000000-0000-4000-8000-000000000092",
		State:       "active",
	}
	operationJSON, err := state.CanonicalOperation(validWorkspaceOperation(second.OperationID))
	if err != nil {
		t.Fatal(err)
	}
	second.OperationJSON = operationJSON
	secondEvidence := value.adjacent.Operations[0]
	secondEvidence.Operation = cloneWorkspaceCheckpointOperation(second)

	switch kind {
	case "reordered":
		value.materialization.Operations = []WorkspaceOperation{second, first}
		value.adjacent.Operations = []workspaceMaterializationOperationEvidence{secondEvidence, value.adjacent.Operations[0]}
	case "duplicate generation":
		second.Generation = first.Generation
		secondEvidence.Operation.Generation = first.Generation
		value.materialization.Operations = append(value.materialization.Operations, second)
		value.adjacent.Operations = append(value.adjacent.Operations, secondEvidence)
	case "duplicate ID":
		second.OperationID = first.OperationID
		second.OperationJSON = bytes.Clone(first.OperationJSON)
		secondEvidence.Operation = cloneWorkspaceCheckpointOperation(second)
		value.materialization.Operations = append(value.materialization.Operations, second)
		value.adjacent.Operations = append(value.adjacent.Operations, secondEvidence)
	default:
		t.Fatalf("unknown malformed operation kind %q", kind)
	}
	return value
}

func TestWorkspaceCheckpointCommitMissingCorruptCanceledAndZeroOnError(t *testing.T) {
	store, repo, binding := checkpointCommitFixture(t, "")
	prior := captureCheckpointCommitState(t, repo, binding.Scope)
	next := rolledBackCheckpointCommitState(t, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.conn.ExecContext(context.Background(), `UPDATE workspace_bindings SET status='pending' WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		return err
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	match, err := repo.ConfirmCheckpointCommit(ctx, prior, next)
	if !errors.Is(err, context.Canceled) || match != WorkspaceCheckpointCommitThird {
		t.Fatalf("canceled confirmation=(%v,%v), want (Third,context.Canceled)", match, err)
	}
	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.CaptureCheckpointCommitState(ctx)
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, WorkspaceCheckpointCommitState{}) {
			t.Fatalf("canceled capture=(%+v,%v), want (zero,context.Canceled)", got, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	checkpointCommitExec(t, store.DB(), `UPDATE workspace_overlay_operations SET created_at=CAST(created_at AS BLOB) WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
	match, err = repo.ConfirmCheckpointCommit(context.Background(), prior, next)
	if err == nil || match != WorkspaceCheckpointCommitThird {
		t.Fatalf("corrupt confirmation=(%v,%v), want (Third,error)", match, err)
	}
	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.CaptureCheckpointCommitState(context.Background())
		if err == nil || !reflect.DeepEqual(got, WorkspaceCheckpointCommitState{}) {
			t.Fatalf("corrupt capture=(%+v,%v), want (zero,error)", got, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	_, missingRepo := openWorkspaceStore(t)
	match, err = missingRepo.ConfirmCheckpointCommit(context.Background(), prior, next)
	if !errors.Is(err, ErrNotFound) || match != WorkspaceCheckpointCommitThird {
		t.Fatalf("missing confirmation=(%v,%v), want (Third,ErrNotFound)", match, err)
	}
}

func TestWorkspaceCheckpointCommitRejectsCandidateStorageCorruption(t *testing.T) {
	store, repo, binding := checkpointCommitFixture(t, "")
	prior := captureCheckpointCommitState(t, repo, binding.Scope)
	next := rolledBackCheckpointCommitState(t, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.conn.ExecContext(context.Background(), `UPDATE workspace_bindings SET status='pending' WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		return err
	})
	var candidateBefore *WorkspaceCandidateRecord
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		var err error
		candidateBefore, err = tx.Candidate(context.Background())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	checkpointCommitExec(t, store.DB(), `
		UPDATE workspace_candidates SET direct_tree=CAST(direct_tree AS TEXT)
		WHERE project_id=? AND workspace_id=?
	`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
	var candidateAfter *WorkspaceCandidateRecord
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		var err error
		candidateAfter, err = tx.Candidate(context.Background())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !equalWorkspaceCandidateRecords(candidateBefore, candidateAfter) {
		t.Fatal("representation-only candidate corruption changed the semantic candidate")
	}

	match, err := repo.ConfirmCheckpointCommit(context.Background(), prior, next)
	if err == nil || match != WorkspaceCheckpointCommitThird {
		t.Fatalf("candidate storage corruption confirmation=(%v,%v), want (Third,error)", match, err)
	}
	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		got, captureErr := tx.CaptureCheckpointCommitState(context.Background())
		if captureErr == nil || !reflect.DeepEqual(got, WorkspaceCheckpointCommitState{}) {
			t.Fatalf("candidate storage corruption capture=(%+v,%v), want (zero,error)", got, captureErr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceCheckpointCommitConfirmationBeginAndCommitFailures(t *testing.T) {
	for _, phase := range []string{"BEGIN", "COMMIT"} {
		t.Run(phase, func(t *testing.T) {
			databasePath := filepath.Join(t.TempDir(), "gateway.db")
			_, repo, binding := checkpointCommitFixture(t, databasePath)
			prior := captureCheckpointCommitState(t, repo, binding.Scope)
			next := rolledBackCheckpointCommitState(t, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
				_, err := tx.conn.ExecContext(context.Background(), `UPDATE workspace_bindings SET status='pending' WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
				return err
			})
			db, failure := openCheckpointCommitFailureDB(t, databasePath, phase)
			match, err := NewWorkspaceRepo(db).ConfirmCheckpointCommit(context.Background(), prior, next)
			if !errors.Is(err, failure.injected) || match != WorkspaceCheckpointCommitThird {
				t.Fatalf("%s failure confirmation=(%v,%v), want (Third,wrapped injected error)", phase, match, err)
			}
			wantContext := "begin checkpoint commit confirmation"
			if phase == "COMMIT" {
				wantContext = "commit checkpoint confirmation read"
			}
			if !strings.Contains(err.Error(), wantContext) {
				t.Fatalf("%s failure error=%q, want wrapped context %q", phase, err, wantContext)
			}
			wantCalls := []string{"BEGIN"}
			if phase == "COMMIT" {
				wantCalls = []string{"BEGIN", "COMMIT", "ROLLBACK"}
			}
			if calls := failure.snapshot(); !reflect.DeepEqual(calls, wantCalls) {
				t.Fatalf("%s transaction calls=%v, want no-retry cleanup %v", phase, calls, wantCalls)
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_bindings`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("%s failure left connection unusable: count=%d err=%v", phase, count, err)
			}
		})
	}
}

func TestWorkspaceCheckpointCommitCaptureAndConfirmationAreReadOnly(t *testing.T) {
	store, repo, binding := checkpointCommitFixture(t, "")
	before := readAtomicWorkspaceRawSnapshot(t, store.DB())
	prior := captureCheckpointCommitState(t, repo, binding.Scope)
	next := rolledBackCheckpointCommitState(t, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.conn.ExecContext(context.Background(), `UPDATE workspace_bindings SET status='pending' WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		return err
	})
	if after := readAtomicWorkspaceRawSnapshot(t, store.DB()); !reflect.DeepEqual(after, before) {
		t.Fatalf("capture changed state: before=%#v after=%#v", before, after)
	}
	match, err := repo.ConfirmCheckpointCommit(context.Background(), prior, next)
	if err != nil || match != WorkspaceCheckpointCommitPrior {
		t.Fatalf("confirmation=(%v,%v), want (Prior,nil)", match, err)
	}
	if after := readAtomicWorkspaceRawSnapshot(t, store.DB()); !reflect.DeepEqual(after, before) {
		t.Fatalf("confirmation changed state: before=%#v after=%#v", before, after)
	}
}

func TestWorkspaceCheckpointCommitDeepOwnershipAndRestartStability(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, repo, binding := checkpointCommitFixture(t, databasePath)
	candidate := workspaceCandidateRecord(t, binding, true, 1)
	commitCheckpointMutation(t, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.UpsertCandidate(context.Background(), candidate)
	})
	checkpointCommitExec(t, store.DB(), `
		UPDATE workspace_overlay_operations
		SET state='stashed',stashed_by_stash_id='00000000-0000-4000-8000-000000000031'
		WHERE project_id=? AND workspace_id=?
	`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)

	prior := captureCheckpointCommitState(t, repo, binding.Scope)
	next := rolledBackCheckpointCommitState(t, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.conn.ExecContext(context.Background(), `UPDATE workspace_bindings SET status='pending' WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		return err
	})
	owned := cloneWorkspaceCheckpointCommitState(prior)
	mutateCheckpointCommitStateReferences(t, &prior)
	fresh := captureCheckpointCommitState(t, repo, binding.Scope)
	if !equalWorkspaceCheckpointCommitStates(owned, fresh) {
		t.Fatal("deep-owned clone changed after mutating every pointer/slice-bearing source layer")
	}
	if equalWorkspaceCheckpointCommitStates(prior, fresh) {
		t.Fatal("reference mutation did not exercise exact private equality")
	}

	// Mutating the caller's original candidate after persistence/capture must not
	// alter the opaque token or the database-owned state.
	candidate.DirectSnapshot.Project.Name = "caller mutation"
	candidate.RebasedSnapshot.Project.Name = "caller rebased mutation"
	if !equalWorkspaceCheckpointCommitStates(owned, captureCheckpointCommitState(t, repo, binding.Scope)) {
		t.Fatal("caller-owned candidate mutation changed captured state")
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	restartedRepo := NewWorkspaceRepo(restarted.DB())
	match, err := restartedRepo.ConfirmCheckpointCommit(context.Background(), owned, next)
	if err != nil || match != WorkspaceCheckpointCommitPrior {
		t.Fatalf("restart confirmation=(%v,%v), want (Prior,nil)", match, err)
	}
}

func TestWorkspaceCheckpointCommitConfirmationUsesOneCoherentReadSnapshot(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	_, writerRepo, binding := checkpointCommitFixture(t, databasePath)
	prior := captureCheckpointCommitState(t, writerRepo, binding.Scope)
	next := rolledBackCheckpointCommitState(t, writerRepo, binding.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.conn.ExecContext(context.Background(), `UPDATE workspace_bindings SET status='pending' WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		return err
	})

	readStarted := make(chan struct{})
	writerFinished := make(chan struct{})
	readDB := openCheckpointCommitSnapshotDB(t, databasePath, readStarted, writerFinished)
	readRepo := NewWorkspaceRepo(readDB)
	writerError := make(chan error, 1)
	go func() {
		<-readStarted
		err := writerRepo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
			_, err := tx.conn.ExecContext(context.Background(), `UPDATE workspace_bindings SET status='pending' WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
			return err
		})
		writerError <- err
		close(writerFinished)
	}()

	match, err := readRepo.ConfirmCheckpointCommit(context.Background(), prior, next)
	if err != nil || match != WorkspaceCheckpointCommitPrior {
		t.Fatalf("raced snapshot confirmation=(%v,%v), want (Prior,nil)", match, err)
	}
	if err := <-writerError; err != nil {
		t.Fatalf("concurrent writer transition: %v", err)
	}
	match, err = readRepo.ConfirmCheckpointCommit(context.Background(), prior, next)
	if err != nil || match != WorkspaceCheckpointCommitNext {
		t.Fatalf("post-writer confirmation=(%v,%v), want (Next,nil)", match, err)
	}
}

func checkpointCommitFixture(t *testing.T, databasePath string) (*Store, *WorkspaceRepo, types.WorkspaceBinding) {
	t.Helper()
	var store *Store
	var repo *WorkspaceRepo
	if databasePath == "" {
		store, repo = openWorkspaceStore(t)
	} else {
		var err error
		store, err = Open(databasePath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		repo = NewWorkspaceRepo(store.DB())
	}
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011",
		"/checkout-a", 1, 11,
	)
	configurePublicationPolicy(t, repo, binding, types.PublicationPrivateGit)
	proof := "{\"schema_version\":1,\"initial_through_generation\":0,\"operations\":[]}\n"
	makeMaterializationFixture(t, store, repo, binding, "accepted", &proof)
	candidate := workspaceCandidateRecord(t, binding, false, 0)
	commitCheckpointMutation(t, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.UpsertCandidate(context.Background(), candidate)
	})
	insertWorkspaceOperation(t, store, binding.Scope, 1,
		validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "active")
	return store, repo, binding
}

func captureCheckpointCommitState(t *testing.T, repo *WorkspaceRepo, scope types.WorkspaceScope) WorkspaceCheckpointCommitState {
	t.Helper()
	var captured WorkspaceCheckpointCommitState
	if err := repo.WithImmediateWorkspace(context.Background(), scope, func(tx *WorkspaceMutationTx) error {
		var err error
		captured, err = tx.CaptureCheckpointCommitState(context.Background())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return captured
}

func rolledBackCheckpointCommitState(
	t *testing.T,
	repo *WorkspaceRepo,
	scope types.WorkspaceScope,
	mutate func(*WorkspaceMutationTx) error,
) WorkspaceCheckpointCommitState {
	t.Helper()
	rollback := errors.New("checkpoint commit fixture rollback")
	var captured WorkspaceCheckpointCommitState
	err := repo.WithImmediateWorkspace(context.Background(), scope, func(tx *WorkspaceMutationTx) error {
		if err := mutate(tx); err != nil {
			return err
		}
		var err error
		captured, err = tx.CaptureCheckpointCommitState(context.Background())
		if err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("rolled-back capture error=%v, want fixture rollback", err)
	}
	return captured
}

func commitCheckpointMutation(t *testing.T, repo *WorkspaceRepo, scope types.WorkspaceScope, mutate func(*WorkspaceMutationTx) error) {
	t.Helper()
	if err := repo.WithImmediateWorkspace(context.Background(), scope, mutate); err != nil {
		t.Fatal(err)
	}
}

func checkpointCommitExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func mutateCheckpointCommitStateReferences(t *testing.T, value *WorkspaceCheckpointCommitState) {
	t.Helper()
	value.binding.SnapshotBytes[0] ^= 0xff
	value.binding.Record.Snapshot.Project.Name = "mutated binding snapshot"
	value.binding.Record.Snapshot.Project.Aliases = append(value.binding.Record.Snapshot.Project.Aliases, "mutated")
	if value.policy.Record.OriginDigest == nil || value.policy.Record.ChangedBy == nil || value.policy.Record.ChangedAt == nil {
		t.Fatal("configured policy pointers are missing")
	}
	*value.policy.Record.OriginDigest = state.Digest("sha256:" + strings.Repeat("f", 64))
	value.policy.Record.ChangedBy.HumanPrincipalID = "00000000-0000-4000-8000-000000000099"
	*value.policy.Record.ChangedAt = value.policy.Record.ChangedAt.Add(time.Minute)
	value.policyHistory[1].RepositoryJSON = "mutated history"
	*value.policyHistory[1].Record.OriginDigest = state.Digest("sha256:" + strings.Repeat("e", 64))
	value.materialization.Journals[0].PriorTree[0].Data[0] ^= 0xff
	value.materialization.Journals[0].CandidateTree[0].Data[0] ^= 0xff
	*value.materialization.Journals[0].IncludedOperationsJSON = "mutated operation proof"
	*value.materialization.Journals[0].PublicationReviewJSON = "mutated review"
	*value.materialization.Journals[0].PriorCandidateJSON = "mutated prior candidate"
	value.materialization.Operations[0].OperationJSON[0] ^= 0xff
	*value.materialization.Operations[0].StashedByStashID = "00000000-0000-4000-8000-000000000099"
	value.adjacent.Operations[0].Operation.OperationJSON[0] ^= 0xff
	*value.adjacent.Operations[0].Operation.StashedByStashID = "00000000-0000-4000-8000-000000000099"
	value.adjacent.Candidate.DirectBytes[0] ^= 0xff
	value.adjacent.Candidate.RebasedBytes[0] ^= 0xff
}

type checkpointCommitSnapshotDriver struct {
	inner          driver.Driver
	readStarted    chan<- struct{}
	writerFinished <-chan struct{}
	once           sync.Once
}

func (wrapped *checkpointCommitSnapshotDriver) Open(name string) (driver.Conn, error) {
	connection, err := wrapped.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &checkpointCommitSnapshotConn{Conn: connection, wrapped: wrapped}, nil
}

type checkpointCommitSnapshotConn struct {
	driver.Conn
	wrapped *checkpointCommitSnapshotDriver
}

func (connection *checkpointCommitSnapshotConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return connection.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (connection *checkpointCommitSnapshotConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := connection.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err != nil || !strings.Contains(strings.ToLower(query), "from workspace_bindings") {
		return rows, err
	}
	return &checkpointCommitSnapshotRows{Rows: rows, wrapped: connection.wrapped}, nil
}

func (connection *checkpointCommitSnapshotConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	return connection.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
}

func (connection *checkpointCommitSnapshotConn) Ping(ctx context.Context) error {
	return connection.Conn.(driver.Pinger).Ping(ctx)
}

type checkpointCommitSnapshotRows struct {
	driver.Rows
	wrapped *checkpointCommitSnapshotDriver
}

func (rows *checkpointCommitSnapshotRows) Next(destinations []driver.Value) error {
	err := rows.Rows.Next(destinations)
	if err == nil {
		rows.wrapped.once.Do(func() {
			close(rows.wrapped.readStarted)
			<-rows.wrapped.writerFinished
		})
	}
	return err
}

var checkpointCommitSnapshotSequence atomic.Uint64

func openCheckpointCommitSnapshotDB(
	t *testing.T,
	databasePath string,
	readStarted chan<- struct{},
	writerFinished <-chan struct{},
) *sql.DB {
	t.Helper()
	wrapped := &checkpointCommitSnapshotDriver{
		inner: &sqlite.Driver{}, readStarted: readStarted, writerFinished: writerFinished,
	}
	driverName := fmt.Sprintf("localstore-checkpoint-commit-snapshot-%d", checkpointCommitSnapshotSequence.Add(1))
	sql.Register(driverName, wrapped)
	db, err := sql.Open(driverName, sqliteDSN(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type checkpointCommitFailureDriver struct {
	inner     driver.Driver
	failPhase string
	injected  error
	mu        sync.Mutex
	calls     []string
}

func (wrapped *checkpointCommitFailureDriver) Open(name string) (driver.Conn, error) {
	connection, err := wrapped.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &checkpointCommitFailureConn{Conn: connection, wrapped: wrapped}, nil
}

func (wrapped *checkpointCommitFailureDriver) record(phase string) {
	wrapped.mu.Lock()
	defer wrapped.mu.Unlock()
	wrapped.calls = append(wrapped.calls, phase)
}

func (wrapped *checkpointCommitFailureDriver) snapshot() []string {
	wrapped.mu.Lock()
	defer wrapped.mu.Unlock()
	return append([]string(nil), wrapped.calls...)
}

type checkpointCommitFailureConn struct {
	driver.Conn
	wrapped *checkpointCommitFailureDriver
}

func (connection *checkpointCommitFailureConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	phase := strings.ToUpper(strings.TrimSpace(query))
	switch phase {
	case "BEGIN", "COMMIT", "ROLLBACK":
		connection.wrapped.record(phase)
	}
	if phase == connection.wrapped.failPhase {
		return nil, connection.wrapped.injected
	}
	return connection.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (connection *checkpointCommitFailureConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return connection.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (connection *checkpointCommitFailureConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	return connection.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
}

func (connection *checkpointCommitFailureConn) Ping(ctx context.Context) error {
	return connection.Conn.(driver.Pinger).Ping(ctx)
}

var checkpointCommitFailureSequence atomic.Uint64

func openCheckpointCommitFailureDB(t *testing.T, databasePath, failPhase string) (*sql.DB, *checkpointCommitFailureDriver) {
	t.Helper()
	failure := &checkpointCommitFailureDriver{
		inner: &sqlite.Driver{}, failPhase: failPhase, injected: errors.New("injected checkpoint confirmation " + failPhase + " failure"),
	}
	driverName := fmt.Sprintf("localstore-checkpoint-commit-failure-%d", checkpointCommitFailureSequence.Add(1))
	sql.Register(driverName, failure)
	db, err := sql.Open(driverName, sqliteDSN(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, failure
}

func TestWorkspaceCheckpointCommitOpaqueStateHasOnlyPrivateFields(t *testing.T) {
	typeOfState := reflect.TypeOf(WorkspaceCheckpointCommitState{})
	for index := 0; index < typeOfState.NumField(); index++ {
		if typeOfState.Field(index).PkgPath == "" {
			t.Fatalf("opaque state field %q is exported", typeOfState.Field(index).Name)
		}
	}
}

func TestWorkspaceCheckpointCommitSnapshotCloneOwnsEveryRecordLayer(t *testing.T) {
	parent, owner := "parent", "owner"
	due := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	wantDue := due
	note, taskID, commitSHA, prURL := "note", "task", strings.Repeat("a", 40), "https://example.com/pr/1"
	deletedBody := state.Digest("sha256:" + strings.Repeat("b", 64))
	wantDeletedBody := deletedBody
	tombstone := &state.TombstoneV1{
		SchemaVersion: 1, Kind: "tombstone", ID: "deleted", EntityKind: "task",
		DeletedContentDigest: state.Digest("sha256:" + strings.Repeat("c", 64)),
		DeletedBodyDigest:    &deletedBody,
		Extensions:           state.ExtensionsV1{"tombstone": {SchemaVersion: 1, Data: []byte(`{"t":1}`)}},
	}
	actor := state.ActorV1{
		SchemaVersion: 1, Kind: "actor", ID: "actor", DisplayName: "actor",
		PublicKeys: []state.PublicKeyV1{{KeyID: "key", Algorithm: "ed25519", PublicKeyBase64: "YQ=="}},
		Extensions: state.ExtensionsV1{"actor": {SchemaVersion: 1, Data: []byte(`{"a":1}`)}},
	}
	task := state.TaskV1{
		SchemaVersion: 1, Kind: "task", ID: "task", ParentTaskID: &parent, OwnerActorID: &owner,
		DueBy: &due, Extensions: state.ExtensionsV1{"task": {SchemaVersion: 1, Data: []byte(`{"x":1}`)}},
	}
	taskLink := state.TaskLinkV1{SchemaVersion: 1, Kind: "task_link", ID: "link", Extensions: state.ExtensionsV1{}}
	article := state.KBArticleV1{
		SchemaVersion: 1, Kind: "kb_article", ID: "article",
		Frontmatter:       map[string]json.RawMessage{"raw": []byte(`{"front":1}`)},
		RelatedArticleIDs: []string{"related"}, Extensions: state.ExtensionsV1{},
	}
	channel := state.ChannelV1{SchemaVersion: 1, Kind: "channel", ID: "channel", Extensions: state.ExtensionsV1{}}
	event := state.EventV1{
		SchemaVersion: 1, Kind: "event", ID: "event", Payload: []byte(`{"event":1}`), Note: &note,
		Extensions: state.ExtensionsV1{"event": {SchemaVersion: 1, Data: []byte(`{"e":1}`)}},
	}
	gitLink := state.GitLinkV1{
		SchemaVersion: 1, Kind: "git_link", ID: "git", TaskID: &taskID, CommitSHA: &commitSHA, PRURL: &prURL,
		Extensions: state.ExtensionsV1{},
	}
	snapshot := state.Snapshot{
		Remotes: &state.RemotesV1{Version: 1, Fabrics: []state.FabricHintV1{{Alias: "origin"}}},
		Project: state.ProjectV1{
			SchemaVersion: 1, Kind: "project", ID: "project", Aliases: []string{"alias"},
			Extensions: state.ExtensionsV1{"project": {SchemaVersion: 1, Data: []byte(`{"p":1}`)}},
		},
		Actors: map[string]state.Record[state.ActorV1]{
			"value": {Value: &actor}, "tombstone": {Tombstone: tombstone},
		},
		Tasks:     map[string]state.Record[state.TaskV1]{"value": {Value: &task}, "tombstone": {Tombstone: tombstone}},
		TaskLinks: map[string]state.Record[state.TaskLinkV1]{"value": {Value: &taskLink}},
		Articles: map[string]state.KBRecord{
			"value": {Value: &article, Body: []byte("body")}, "tombstone": {Tombstone: tombstone},
		},
		Channels: map[string]state.Record[state.ChannelV1]{"value": {Value: &channel}},
		Events:   map[string]state.EventV1{"value": event},
		GitLinks: map[string]state.Record[state.GitLinkV1]{"value": {Value: &gitLink}},
	}

	cloned := cloneWorkspaceCheckpointSnapshot(snapshot)
	snapshot.Remotes.Fabrics[0].Alias = "mutated"
	snapshot.Project.Aliases[0] = "mutated"
	projectExtension := snapshot.Project.Extensions["project"]
	projectExtension.Data[0] ^= 0xff
	snapshot.Actors["value"].Value.PublicKeys[0].KeyID = "mutated"
	*snapshot.Tasks["value"].Value.ParentTaskID = "mutated"
	*snapshot.Tasks["value"].Value.OwnerActorID = "mutated"
	*snapshot.Tasks["value"].Value.DueBy = snapshot.Tasks["value"].Value.DueBy.Add(time.Hour)
	snapshot.Articles["value"].Value.Frontmatter["raw"][0] ^= 0xff
	snapshot.Articles["value"].Value.RelatedArticleIDs[0] = "mutated"
	snapshot.Articles["value"].Body[0] ^= 0xff
	snapshot.Events["value"].Payload[0] ^= 0xff
	*snapshot.Events["value"].Note = "mutated"
	*snapshot.GitLinks["value"].Value.TaskID = "mutated"
	*snapshot.GitLinks["value"].Value.CommitSHA = "mutated"
	*snapshot.GitLinks["value"].Value.PRURL = "mutated"
	*tombstone.DeletedBodyDigest = state.Digest("mutated")
	tombstoneExtension := tombstone.Extensions["tombstone"]
	tombstoneExtension.Data[0] ^= 0xff

	if cloned.Remotes.Fabrics[0].Alias != "origin" || cloned.Project.Aliases[0] != "alias" ||
		cloned.Actors["value"].Value.PublicKeys[0].KeyID != "key" ||
		*cloned.Tasks["value"].Value.ParentTaskID != "parent" || *cloned.Tasks["value"].Value.OwnerActorID != "owner" ||
		!cloned.Tasks["value"].Value.DueBy.Equal(wantDue) || string(cloned.Articles["value"].Value.Frontmatter["raw"]) != `{"front":1}` ||
		cloned.Articles["value"].Value.RelatedArticleIDs[0] != "related" || string(cloned.Articles["value"].Body) != "body" ||
		string(cloned.Events["value"].Payload) != `{"event":1}` || *cloned.Events["value"].Note != "note" ||
		*cloned.GitLinks["value"].Value.TaskID != "task" || *cloned.GitLinks["value"].Value.CommitSHA != strings.Repeat("a", 40) ||
		*cloned.GitLinks["value"].Value.PRURL != "https://example.com/pr/1" ||
		*cloned.Tasks["tombstone"].Tombstone.DeletedBodyDigest != wantDeletedBody {
		t.Fatalf("snapshot clone retained a caller-owned reference: %+v", cloned)
	}

	zeroClone := cloneWorkspaceCheckpointSnapshot(state.Snapshot{})
	if zeroClone.Remotes != nil || zeroClone.Actors != nil || zeroClone.Tasks != nil || zeroClone.TaskLinks != nil ||
		zeroClone.Articles != nil || zeroClone.Channels != nil || zeroClone.Events != nil || zeroClone.GitLinks != nil {
		t.Fatalf("zero snapshot clone changed nil distinctions: %+v", zeroClone)
	}
	zeroStateClone := cloneWorkspaceCheckpointCommitState(WorkspaceCheckpointCommitState{})
	if !reflect.DeepEqual(zeroStateClone, WorkspaceCheckpointCommitState{}) {
		t.Fatalf("zero state clone=%+v", zeroStateClone)
	}

	left := WorkspaceCheckpointCommitState{}
	right := WorkspaceCheckpointCommitState{policyHistory: []workspacePublicationRawRecord{}}
	if equalWorkspaceCheckpointCommitStates(left, right) {
		t.Fatal("equality collapsed nil and empty policy history")
	}
	right = WorkspaceCheckpointCommitState{materialization: WorkspaceMaterializationDisposition{Journals: []WorkspaceMaterializationRecord{}, Operations: []WorkspaceOperation{}}}
	if equalWorkspaceCheckpointCommitStates(left, right) {
		t.Fatal("equality collapsed nil and empty materialization slices")
	}
	right = WorkspaceCheckpointCommitState{adjacent: workspaceMaterializationAdjacentEvidence{Operations: []workspaceMaterializationOperationEvidence{}}}
	if equalWorkspaceCheckpointCommitStates(left, right) {
		t.Fatal("equality collapsed nil and empty adjacent operation slices")
	}
}
