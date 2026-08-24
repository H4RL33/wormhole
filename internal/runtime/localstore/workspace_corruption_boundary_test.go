package localstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

// TestWorkspaceTransactionWrappersRollBackInjectedWriteFailure keeps the real
// statement-failure rollback seam for both immediate workspace wrappers. The
// trigger is intentionally a test fault injection, not a hostile-write oracle.
func TestWorkspaceTransactionWrappersRollBackInjectedWriteFailure(t *testing.T) {
	for _, wrapper := range workspaceRevisionTestWrappers() {
		t.Run(wrapper.name, func(t *testing.T) {
			store, repo := openWorkspaceStore(t)
			target := createBinding(t, repo,
				"00000000-0000-4000-8000-000000000001",
				"00000000-0000-4000-8000-000000000011",
				"/checkpoint-target", 1, 11)
			sibling := createBinding(t, repo, target.Scope.ProjectID,
				"00000000-0000-4000-8000-000000000012",
				"/checkpoint-sibling", 2, 12)
			ctx := context.Background()

			if _, err := store.DB().Exec(fmt.Sprintf(`
				CREATE TRIGGER workspace_corruption_boundary_abort
				BEFORE UPDATE OF status ON workspace_bindings
				WHEN OLD.project_id=%s AND OLD.workspace_id=%s AND NEW.status='pending'
				BEGIN
					SELECT RAISE(ABORT, 'workspace corruption boundary injected status failure');
				END
			`, quoteSQLiteTextLiteral(target.Scope.ProjectID), quoteSQLiteTextLiteral(string(target.Scope.WorkspaceID)))); err != nil {
				t.Fatal(err)
			}

			operation := validWorkspaceOperation("00000000-0000-4000-8000-000000000191")
			operationJSON, err := state.CanonicalOperation(operation)
			if err != nil {
				t.Fatal(err)
			}
			before := readAtomicWorkspaceRawSnapshot(t, store.DB())
			var statementErr error
			err = wrapper.run(ctx, repo, target.Scope, func(tx *WorkspaceMutationTx) error {
				if err := tx.InsertActiveOperations(ctx, []WorkspaceOperationInsert{{
					Generation: 1, OperationID: operation.ID, OperationJSON: operationJSON,
				}}); err != nil {
					return err
				}
				statementErr = tx.SetStatus(ctx, "pending")
				return statementErr
			})
			if statementErr == nil {
				t.Fatal("injected status failure did not reach the wrapper callback")
			}
			if err != statementErr || errors.Is(err, ErrCommitOutcomeUnknown) ||
				!strings.Contains(err.Error(), "workspace corruption boundary injected status failure") {
				t.Fatalf("wrapper error=%v callback error=%v, want exact pre-COMMIT RAISE(ABORT) error", err, statementErr)
			}
			after := readAtomicWorkspaceRawSnapshot(t, store.DB())
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("injected statement failure changed target or sibling raw state:\n got %#v\nwant %#v", after, before)
			}
			assertWorkspaceRevisionTestState(t, store.DB(), target.Scope, 1, "clean")
			assertWorkspaceRevisionTestState(t, store.DB(), sibling.Scope, 1, "clean")
			if operations := readWorkspaceOperations(t, store, target.Scope); len(operations) != 0 {
				t.Fatalf("target rollback retained operations=%+v", operations)
			}
			if operations := readWorkspaceOperations(t, store, sibling.Scope); len(operations) != 0 {
				t.Fatalf("sibling changed operations=%+v", operations)
			}

			if _, err := store.DB().Exec(`DROP TRIGGER workspace_corruption_boundary_abort`); err != nil {
				t.Fatal(err)
			}
			if err := wrapper.run(ctx, repo, target.Scope, func(tx *WorkspaceMutationTx) error {
				if err := tx.InsertActiveOperations(ctx, []WorkspaceOperationInsert{{
					Generation: 1, OperationID: operation.ID, OperationJSON: operationJSON,
				}}); err != nil {
					return err
				}
				return tx.SetStatus(ctx, "pending")
			}); err != nil {
				t.Fatalf("retry after dropping injected trigger: %v", err)
			}
			assertWorkspaceRevisionTestState(t, store.DB(), target.Scope, 2, "pending")
			assertWorkspaceRevisionTestState(t, store.DB(), sibling.Scope, 1, "clean")
			if operations := readWorkspaceOperations(t, store, target.Scope); len(operations) != 1 || operations[0].OperationID != operation.ID {
				t.Fatalf("target retry operations=%+v, want inserted operation %q", operations, operation.ID)
			}
		})
	}
}

// TestWorkspaceReducedRepresentationBoundary rejects only the concrete raw
// representation proof fragments retired by Task 9. It deliberately leaves
// workspace-revision typing, publication/materialization, checkpoint, history,
// and restore-retry paths to their owning later tasks.
func TestWorkspaceReducedRepresentationBoundary(t *testing.T) {
	for _, test := range []struct {
		name       string
		file       string
		fragments  []string
		retirement string
	}{
		{
			name: "accepted-base-and-status-raw-timestamp-cas",
			file: "workspace_repo.go",
			fragments: []string{
				"type workspaceBindingMutationMetadata struct",
				"AND created_at=? AND updated_at=?",
				"RETURNING updated_at, CAST(updated_at AS TEXT), typeof(updated_at)",
			},
			retirement: "Task 9 replaces current binding raw timestamp and full-row proof with semantic transition, affected-row, and revision authority",
		},
		{
			name: "named-stash-reader-and-delete-storage-class-proof",
			file: "workspace_stash_repo.go",
			fragments: []string{
				"COUNT(stash.rowid) OVER ()",
				"typeof(stash.project_id)",
				"ON CAST(stash.project_id AS TEXT)=binding.project_id",
				"AND typeof(project_id)='text' AND typeof(workspace_id)='text' AND typeof(stash_id)='text'",
			},
			retirement: "Task 9 keeps exact scope/key and semantic stash validation but removes raw alias/count/storage-class proof",
		},
		{
			name: "transition-receipt-reader-storage-class-proof",
			file: "workspace_transition_repo.go",
			fragments: []string{
				"typeof(project_id), typeof(workspace_id), typeof(request_id)",
				"WHERE CAST(project_id AS TEXT)=?",
				"COUNT(receipt.rowid) OVER ()",
				"NULLIF(typeof(receipt.project_id),'null')",
				"ON CAST(receipt.project_id AS TEXT)=binding.project_id",
			},
			retirement: "Task 9 keeps immutable receipt semantics and exact scope/key while removing raw alias/count/storage-class proof",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, err := os.ReadFile(test.file)
			if err != nil {
				t.Fatalf("read %s: %v", test.file, err)
			}
			for _, fragment := range test.fragments {
				if bytes.Contains(source, []byte(fragment)) {
					t.Errorf("retired representation fragment remains in %s: %q\n%s", test.file, fragment, test.retirement)
				}
			}
		})
	}
}
