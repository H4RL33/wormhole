package localstore

import (
	"context"
	"testing"
)

func TestWorkspaceMutationTxRestoreCurrentStateSelectsNamedSemanticWorkset(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	stashID := "00000000-0000-4000-8000-000000000031"
	stash := validWorkspaceStash(t, binding, stashID)
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		if err := tx.UpsertCandidate(context.Background(), workspaceCandidateRecord(t, binding, true, 3)); err != nil {
			return err
		}
		return tx.InsertStash(context.Background(), stash)
	}); err != nil {
		t.Fatal(err)
	}
	insertWorkspaceOperationOwned(t, store, binding.Scope, 1,
		validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "stashed", &stashID)
	insertWorkspaceOperation(t, store, binding.Scope, 3,
		validWorkspaceOperation("00000000-0000-4000-8000-000000000093"), "rebased")
	insertWorkspaceOperation(t, store, binding.Scope, 4,
		validWorkspaceOperation("00000000-0000-4000-8000-000000000094"), "active")
	insertWorkspaceOperation(t, store, binding.Scope, 5,
		validWorkspaceOperation("00000000-0000-4000-8000-000000000095"), "materialized")
	if _, err := store.DB().Exec(`UPDATE workspace_overlay_operations SET operation_json='{}'
		WHERE project_id=? AND workspace_id=? AND generation=5`, binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}

	var got WorkspaceRestoreCurrentState
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		var err error
		got, err = tx.RestoreCurrentState(context.Background(), stashID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got.Workspace.Binding != binding || got.Candidate == nil || got.Stash.StashID != stashID ||
		len(got.CurrentOperations) != 2 || got.CurrentOperations[0].Generation != 3 || got.CurrentOperations[1].Generation != 4 ||
		len(got.StashOperations) != 1 || got.StashOperations[0].Generation != 1 || got.OpenConflicts == nil {
		t.Fatalf("RestoreCurrentState()=%+v", got)
	}
	if err := repo.AuditWorkspaceHistory(context.Background(), binding.Scope); err == nil {
		t.Fatal("AuditWorkspaceHistory accepted corrupt unrelated terminal row")
	}
}
