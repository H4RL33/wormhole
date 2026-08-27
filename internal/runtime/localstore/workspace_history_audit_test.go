package localstore

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestAuditWorkspaceHistoryValidatesRetainedEvidenceReadOnlyAcrossRestart(t *testing.T) {
	path, store, repo, target, _ := newWorkspaceHistoryAuditFixture(t)
	before := readAtomicWorkspaceRawSnapshot(t, store.DB())
	if err := repo.AuditWorkspaceHistory(context.Background(), target.Scope); err != nil {
		t.Fatalf("AuditWorkspaceHistory first: %v", err)
	}
	if after := readAtomicWorkspaceRawSnapshot(t, store.DB()); !reflect.DeepEqual(after, before) {
		t.Fatal("AuditWorkspaceHistory changed retained evidence")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedRepo := NewWorkspaceRepo(reopened.DB())
	if err := reopenedRepo.AuditWorkspaceHistory(context.Background(), target.Scope); err != nil {
		t.Fatalf("AuditWorkspaceHistory after restart: %v", err)
	}
	if after := readAtomicWorkspaceRawSnapshot(t, reopened.DB()); !reflect.DeepEqual(after, before) {
		t.Fatal("restart-stable AuditWorkspaceHistory changed retained evidence")
	}
}

func TestAuditWorkspaceHistoryStrictOrderedFamiliesAndExactScope(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(*testing.T, *Store, types.WorkspaceBinding, types.WorkspaceBinding)
	}{
		{
			name: "accepted and recovered-old journals",
			corrupt: func(t *testing.T, store *Store, binding, _ types.WorkspaceBinding) {
				mustExecMaterialization(t, store, `
					UPDATE workspace_materializations SET candidate_digest='invalid'
					WHERE project_id=? AND workspace_id=? AND state='accepted'
				`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
			},
		},
		{
			name: "old materialized and discarded operations",
			corrupt: func(t *testing.T, store *Store, binding, _ types.WorkspaceBinding) {
				if _, err := store.DB().Exec(`
					UPDATE workspace_overlay_operations SET operation_json='{}'
					WHERE project_id=? AND workspace_id=? AND state='discarded'
				`, binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "unrelated stashes",
			corrupt: func(t *testing.T, store *Store, binding, _ types.WorkspaceBinding) {
				if _, err := store.DB().Exec(`
					UPDATE workspace_stashes SET label='bad'||char(13)||'label'
					WHERE project_id=? AND workspace_id=? AND stash_id='00000000-0000-4000-8000-000000000032'
				`, binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "orphaned stash operation owner",
			corrupt: func(t *testing.T, store *Store, binding, _ types.WorkspaceBinding) {
				if _, err := store.DB().Exec(`
					UPDATE workspace_overlay_operations
					SET stashed_by_stash_id='00000000-0000-4000-8000-000000000099'
					WHERE project_id=? AND workspace_id=? AND generation=3
				`, binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "sibling-only stash operation owner",
			corrupt: func(t *testing.T, store *Store, binding, sibling types.WorkspaceBinding) {
				const siblingStashID = "00000000-0000-4000-8000-000000000033"
				repo := NewWorkspaceRepo(store.DB())
				if err := repo.WithImmediateWorkspace(context.Background(), sibling.Scope, func(tx *WorkspaceMutationTx) error {
					return tx.InsertStash(context.Background(), validWorkspaceStash(t, sibling, siblingStashID))
				}); err != nil {
					t.Fatal(err)
				}
				if _, err := store.DB().Exec(`
					UPDATE workspace_overlay_operations SET stashed_by_stash_id=?
					WHERE project_id=? AND workspace_id=? AND generation=3
				`, siblingStashID, binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "resolved conflicts",
			corrupt: func(t *testing.T, store *Store, binding, _ types.WorkspaceBinding) {
				if _, err := store.DB().Exec(`
					UPDATE workspace_conflicts SET resolved_at=NULL
					WHERE project_id=? AND workspace_id=? AND state='resolved'
				`, binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "publication policy history",
			corrupt: func(t *testing.T, store *Store, binding, _ types.WorkspaceBinding) {
				if _, err := store.DB().Exec(`
					UPDATE workspace_publication_policy_history SET repository_identity_json='not-json'
					WHERE project_id=? AND workspace_id=? AND policy_revision=1
				`, binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "unrelated receipts",
			corrupt: func(t *testing.T, store *Store, binding, _ types.WorkspaceBinding) {
				if _, err := store.DB().Exec(`
					UPDATE workspace_transition_receipts SET result_json='{}'
					WHERE project_id=? AND workspace_id=? AND request_id='00000000-0000-4000-8000-000000000042'
				`, binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, store, repo, target, sibling := newWorkspaceHistoryAuditFixture(t)
			defer store.Close()
			test.corrupt(t, store, target, sibling)
			before := readAtomicWorkspaceRawSnapshot(t, store.DB())
			if err := repo.AuditWorkspaceHistory(context.Background(), target.Scope); err == nil {
				t.Fatal("AuditWorkspaceHistory accepted corrupt retained evidence")
			}
			if err := repo.AuditWorkspaceHistory(context.Background(), sibling.Scope); err != nil {
				t.Fatalf("sibling AuditWorkspaceHistory: %v", err)
			}
			if after := readAtomicWorkspaceRawSnapshot(t, store.DB()); !reflect.DeepEqual(after, before) {
				t.Fatal("failed scoped audit changed retained evidence")
			}
		})
	}
}

func TestAuditWorkspaceHistoryRejectsInvalidRepositoryAndScope(t *testing.T) {
	ctx := context.Background()
	var nilRepo *WorkspaceRepo
	if err := nilRepo.AuditWorkspaceHistory(ctx, types.WorkspaceScope{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nil AuditWorkspaceHistory error=%v, want ErrNotFound", err)
	}
	_, store, repo, target, _ := newWorkspaceHistoryAuditFixture(t)
	defer store.Close()
	if err := repo.AuditWorkspaceHistory(ctx, types.WorkspaceScope{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("invalid-scope AuditWorkspaceHistory error=%v, want ErrNotFound", err)
	}
	missing := target.Scope
	missing.WorkspaceID = "00000000-0000-4000-8000-000000000099"
	if err := repo.AuditWorkspaceHistory(ctx, missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing-scope AuditWorkspaceHistory error=%v, want ErrNotFound", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := repo.AuditWorkspaceHistory(ctx, target.Scope); err == nil {
		t.Fatal("closed database AuditWorkspaceHistory succeeded")
	}
}

func newWorkspaceHistoryAuditFixture(t *testing.T) (string, *Store, *WorkspaceRepo, types.WorkspaceBinding, types.WorkspaceBinding) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	target := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	sibling := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	raw := "{\"schema_version\":1,\"initial_through_generation\":0,\"operations\":[]}\n"
	priorTree := workspaceTree(t, target.Scope.ProjectID, target.Repository)
	priorSnapshot, err := state.DecodeTree(priorTree)
	if err != nil {
		t.Fatal(err)
	}
	candidateTree := changedWorkspaceTree(t, target, "History Candidate")
	candidateSnapshot, err := state.DecodeTree(candidateTree)
	if err != nil {
		t.Fatal(err)
	}
	insertMaterializationRow(t, store, target, "audit-accepted", "accepted", priorTree, candidateTree,
		priorSnapshot.Digest, candidateSnapshot.Digest, &raw)
	insertMaterializationRow(t, store, target, "audit-recovered-old", "recovered_old", priorTree, candidateTree,
		priorSnapshot.Digest, candidateSnapshot.Digest, &raw)
	insertWorkspaceOperation(t, store, target.Scope, 1,
		validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "materialized")
	insertWorkspaceOperation(t, store, target.Scope, 2,
		validWorkspaceOperation("00000000-0000-4000-8000-000000000092"), "discarded")
	stashID := "00000000-0000-4000-8000-000000000031"
	otherStashID := "00000000-0000-4000-8000-000000000032"
	insertWorkspaceOperationOwned(t, store, target.Scope, 3,
		validWorkspaceOperation("00000000-0000-4000-8000-000000000093"), "stashed", &stashID)
	insertWorkspaceOperationOwned(t, store, target.Scope, 4,
		validWorkspaceOperation("00000000-0000-4000-8000-000000000094"), "stashed", nil)

	evidence := workspaceConflictEvidence(1, state.RecordKey{
		Kind: "task", ID: "00000000-0000-4000-8000-000000000021",
	})
	if err := repo.WithImmediateWorkspace(context.Background(), target.Scope, func(tx *WorkspaceMutationTx) error {
		if err := tx.InsertStash(context.Background(), validWorkspaceStash(t, target, stashID)); err != nil {
			return err
		}
		if err := tx.InsertStash(context.Background(), validWorkspaceStash(t, target, otherStashID)); err != nil {
			return err
		}
		if err := tx.InsertTransitionReceipt(context.Background(), validWorkspaceTransitionReceipt(t,
			"00000000-0000-4000-8000-000000000041", "stash", "clean")); err != nil {
			return err
		}
		if err := tx.InsertTransitionReceipt(context.Background(), validWorkspaceTransitionReceipt(t,
			"00000000-0000-4000-8000-000000000042", "restore", "conflicted")); err != nil {
			return err
		}
		_, err := tx.ReplaceOpenConflictOccurrences(context.Background(), []WorkspaceConflictEvidence{evidence},
			time.Date(2099, 8, 20, 12, 0, 0, 0, time.UTC))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.WithImmediateWorkspace(context.Background(), target.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.ReplaceOpenConflictOccurrences(context.Background(), nil,
			time.Date(2099, 8, 20, 12, 1, 0, 0, time.UTC))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	configurePublicationPolicy(t, repo, target, types.PublicationLocalOnly)
	return path, store, repo, target, sibling
}
