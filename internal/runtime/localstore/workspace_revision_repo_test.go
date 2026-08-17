package localstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/H4RL33/wormhole/internal/types"
)

const workspaceRevisionTrackerRequestID = "00000000-0000-1000-8000-000000000091"

type workspaceRevisionTestWrapper struct {
	name string
	run  func(context.Context, *WorkspaceRepo, types.WorkspaceScope, func(*WorkspaceMutationTx) error) error
}

func workspaceRevisionTestWrappers() []workspaceRevisionTestWrapper {
	return []workspaceRevisionTestWrapper{
		{
			name: "workspace",
			run: func(ctx context.Context, repo *WorkspaceRepo, scope types.WorkspaceScope, fn func(*WorkspaceMutationTx) error) error {
				return repo.WithImmediateWorkspace(ctx, scope, fn)
			},
		},
		{
			name: "transition",
			run: func(ctx context.Context, repo *WorkspaceRepo, scope types.WorkspaceScope, fn func(*WorkspaceMutationTx) error) error {
				return repo.WithImmediateWorkspaceTransition(ctx, scope, workspaceRevisionTrackerRequestID,
					func(tx *WorkspaceMutationTx, receipt *WorkspaceTransitionReceiptRecord) error {
						if receipt != nil {
							return fmt.Errorf("unexpected transition receipt")
						}
						return fn(tx)
					})
			},
		},
	}
}

func TestWorkspaceRevisionTrackerCleanOneAndManyMarksBothWrappers(t *testing.T) {
	for _, wrapper := range workspaceRevisionTestWrappers() {
		t.Run(wrapper.name, func(t *testing.T) {
			store, repo := openWorkspaceStore(t)
			binding := createBinding(t, repo,
				"00000000-0000-4000-8000-000000000001",
				"00000000-0000-4000-8000-000000000011",
				"/checkout", 1, 11)
			ctx := context.Background()

			if err := wrapper.run(ctx, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
				revision, err := tx.projectedWorkspaceRevision(ctx)
				if err != nil {
					return err
				}
				if revision != 1 {
					return fmt.Errorf("clean projected revision=%d, want 1", revision)
				}
				_, err = tx.Workspace(ctx)
				return err
			}); err != nil {
				t.Fatalf("clean transaction: %v", err)
			}
			assertWorkspaceRevisionTestState(t, store.DB(), binding.Scope, 1, "clean")

			if err := wrapper.run(ctx, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
				if err := tx.markWorkspaceDirty(ctx); err != nil {
					return err
				}
				revision, err := tx.projectedWorkspaceRevision(ctx)
				if err != nil {
					return err
				}
				if revision != 2 {
					return fmt.Errorf("dirty projected revision=%d, want 2", revision)
				}
				return updateWorkspaceRevisionTestStatus(ctx, tx, "pending")
			}); err != nil {
				t.Fatalf("one-mark transaction: %v", err)
			}
			assertWorkspaceRevisionTestState(t, store.DB(), binding.Scope, 2, "pending")

			if err := wrapper.run(ctx, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
				for range 5 {
					if err := tx.markWorkspaceDirty(ctx); err != nil {
						return err
					}
				}
				revision, err := tx.projectedWorkspaceRevision(ctx)
				if err != nil {
					return err
				}
				if revision != 3 {
					return fmt.Errorf("many-mark projected revision=%d, want 3", revision)
				}
				return updateWorkspaceRevisionTestStatus(ctx, tx, "clean")
			}); err != nil {
				t.Fatalf("many-mark transaction: %v", err)
			}
			assertWorkspaceRevisionTestState(t, store.DB(), binding.Scope, 3, "clean")
		})
	}
}

func TestWorkspaceRevisionTrackerCallbackAndStatementFailuresRollBackBothWrappers(t *testing.T) {
	callbackErr := errors.New("workspace revision callback failure")
	for _, wrapper := range workspaceRevisionTestWrappers() {
		t.Run(wrapper.name+" callback", func(t *testing.T) {
			store, repo := openWorkspaceStore(t)
			binding := createBinding(t, repo,
				"00000000-0000-4000-8000-000000000001",
				"00000000-0000-4000-8000-000000000011",
				"/checkout", 1, 11)
			ctx := context.Background()
			err := wrapper.run(ctx, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
				if err := tx.markWorkspaceDirty(ctx); err != nil {
					return err
				}
				if err := updateWorkspaceRevisionTestStatus(ctx, tx, "pending"); err != nil {
					return err
				}
				return callbackErr
			})
			if !errors.Is(err, callbackErr) || errors.Is(err, ErrCommitOutcomeUnknown) {
				t.Fatalf("callback error=%v, want callback cause without unknown-COMMIT", err)
			}
			assertWorkspaceRevisionTestState(t, store.DB(), binding.Scope, 1, "clean")
		})

		t.Run(wrapper.name+" statement", func(t *testing.T) {
			store, repo := openWorkspaceStore(t)
			binding := createBinding(t, repo,
				"00000000-0000-4000-8000-000000000001",
				"00000000-0000-4000-8000-000000000011",
				"/checkout", 1, 11)
			ctx := context.Background()
			var statementErr error
			err := wrapper.run(ctx, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
				if err := tx.markWorkspaceDirty(ctx); err != nil {
					return err
				}
				if err := updateWorkspaceRevisionTestStatus(ctx, tx, "pending"); err != nil {
					return err
				}
				_, statementErr = tx.conn.ExecContext(ctx, `INSERT INTO workspace_revision_missing_table(value) VALUES (1)`)
				return statementErr
			})
			if statementErr == nil || err != statementErr || errors.Is(err, ErrCommitOutcomeUnknown) {
				t.Fatalf("statement error=%v captured=%v, want exact pre-COMMIT cause", err, statementErr)
			}
			assertWorkspaceRevisionTestState(t, store.DB(), binding.Scope, 1, "clean")
		})
	}
}

func TestWorkspaceRevisionTrackerDeferredForeignKeyCommitFailureRollsBackBothWrappers(t *testing.T) {
	for _, wrapper := range workspaceRevisionTestWrappers() {
		t.Run(wrapper.name, func(t *testing.T) {
			store, repo := openWorkspaceStore(t)
			binding := createBinding(t, repo,
				"00000000-0000-4000-8000-000000000001",
				"00000000-0000-4000-8000-000000000011",
				"/checkout", 1, 11)
			ctx := context.Background()
			err := wrapper.run(ctx, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
				if err := tx.markWorkspaceDirty(ctx); err != nil {
					return err
				}
				if err := updateWorkspaceRevisionTestStatus(ctx, tx, "pending"); err != nil {
					return err
				}
				if _, err := tx.conn.ExecContext(ctx, `PRAGMA defer_foreign_keys=ON`); err != nil {
					return err
				}
				_, err := tx.conn.ExecContext(ctx, `
					INSERT INTO workspace_conflicts
					(project_id, workspace_id, occurrence_id, conflict_id, record_kind, record_id,
					 field_path, conflict_kind, base_json, ours_json, theirs_json, state)
					VALUES (?, ?, 'revision-deferred-fk', 'revision-deferred-fk', 'task', ?,
					 '/title', 'same_field', '{}', '{}', '{}', 'open')
				`, "00000000-0000-4000-8000-000000000099",
					"00000000-0000-4000-8000-000000000098",
					"00000000-0000-4000-8000-000000000021")
				return err
			})
			if err == nil || !errors.Is(err, ErrCommitOutcomeUnknown) {
				t.Fatalf("deferred-FK COMMIT error=%v, want ErrCommitOutcomeUnknown", err)
			}
			assertWorkspaceRevisionTestState(t, store.DB(), binding.Scope, 1, "clean")
			var conflicts int
			if err := store.DB().QueryRow(`SELECT COUNT(*) FROM workspace_conflicts WHERE conflict_id='revision-deferred-fk'`).Scan(&conflicts); err != nil {
				t.Fatal(err)
			}
			if conflicts != 0 {
				t.Fatalf("failed COMMIT retained %d injected conflicts", conflicts)
			}
		})
	}
}

func TestWorkspaceRevisionTrackerStaleCASRollsBackInjectedRevisionBothWrappers(t *testing.T) {
	for _, wrapper := range workspaceRevisionTestWrappers() {
		t.Run(wrapper.name, func(t *testing.T) {
			store, repo := openWorkspaceStore(t)
			binding := createBinding(t, repo,
				"00000000-0000-4000-8000-000000000001",
				"00000000-0000-4000-8000-000000000011",
				"/checkout", 1, 11)
			ctx := context.Background()
			err := wrapper.run(ctx, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
				if err := tx.markWorkspaceDirty(ctx); err != nil {
					return err
				}
				if err := updateWorkspaceRevisionTestStatus(ctx, tx, "pending"); err != nil {
					return err
				}
				result, err := tx.conn.ExecContext(ctx, `
					UPDATE workspace_bindings SET workspace_revision=2
					WHERE project_id=? AND workspace_id=?
				`, tx.scope.ProjectID, tx.scope.WorkspaceID)
				if err != nil {
					return err
				}
				affected, err := result.RowsAffected()
				if err != nil || affected != 1 {
					return fmt.Errorf("inject stale revision affected=%d: %w", affected, err)
				}
				return nil
			})
			if err == nil || errors.Is(err, ErrCommitOutcomeUnknown) {
				t.Fatalf("stale-CAS error=%v, want pre-COMMIT failure", err)
			}
			assertWorkspaceRevisionTestState(t, store.DB(), binding.Scope, 1, "clean")
		})
	}
}

func TestWorkspaceRevisionTrackerSiblingScopesRemainIndependent(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	a := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011",
		"/checkout-a", 1, 11)
	b := createBinding(t, repo, a.Scope.ProjectID,
		"00000000-0000-4000-8000-000000000012",
		"/checkout-b", 2, 12)
	c := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000002",
		string(a.Scope.WorkspaceID),
		"/checkout-c", 3, 13)
	ctx := context.Background()
	wrappers := workspaceRevisionTestWrappers()
	if err := wrappers[0].run(ctx, repo, a.Scope, func(tx *WorkspaceMutationTx) error {
		if err := tx.markWorkspaceDirty(ctx); err != nil {
			return err
		}
		return updateWorkspaceRevisionTestStatus(ctx, tx, "pending")
	}); err != nil {
		t.Fatal(err)
	}
	if err := wrappers[1].run(ctx, repo, b.Scope, func(tx *WorkspaceMutationTx) error {
		if err := tx.markWorkspaceDirty(ctx); err != nil {
			return err
		}
		return updateWorkspaceRevisionTestStatus(ctx, tx, "blocked")
	}); err != nil {
		t.Fatal(err)
	}
	assertWorkspaceRevisionTestState(t, store.DB(), a.Scope, 2, "pending")
	assertWorkspaceRevisionTestState(t, store.DB(), b.Scope, 2, "blocked")
	assertWorkspaceRevisionTestState(t, store.DB(), c.Scope, 1, "clean")
}

func TestWorkspaceRevisionTrackerSuccessfulRevisionSurvivesRestart(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011",
		"/checkout", 1, 11)
	ctx := context.Background()
	wrappers := workspaceRevisionTestWrappers()
	if err := wrappers[0].run(ctx, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
		if err := tx.markWorkspaceDirty(ctx); err != nil {
			return err
		}
		return updateWorkspaceRevisionTestStatus(ctx, tx, "pending")
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo = NewWorkspaceRepo(store.DB())
	assertWorkspaceRevisionTestState(t, store.DB(), binding.Scope, 2, "pending")
	if err := wrappers[1].run(ctx, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
		if err := tx.markWorkspaceDirty(ctx); err != nil {
			return err
		}
		return updateWorkspaceRevisionTestStatus(ctx, tx, "clean")
	}); err != nil {
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
	assertWorkspaceRevisionTestState(t, store.DB(), binding.Scope, 3, "clean")
}

func TestWorkspaceRevisionTrackerMalformedRevisionFailsBeforeLogicalMutation(t *testing.T) {
	for _, wrapper := range workspaceRevisionTestWrappers() {
		t.Run(wrapper.name, func(t *testing.T) {
			store, repo := openWorkspaceStore(t)
			binding := createBinding(t, repo,
				"00000000-0000-4000-8000-000000000001",
				"00000000-0000-4000-8000-000000000011",
				"/checkout", 1, 11)
			corruptWorkspaceRevisionForTest(t, store.DB(), binding.Scope, "'malformed'")
			ctx := context.Background()
			callbackCalls := 0
			err := wrapper.run(ctx, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
				callbackCalls++
				if err := tx.markWorkspaceDirty(ctx); err != nil {
					return err
				}
				return updateWorkspaceRevisionTestStatus(ctx, tx, "pending")
			})
			if err == nil || errors.Is(err, ErrCommitOutcomeUnknown) {
				t.Fatalf("malformed-revision error=%v, want pre-COMMIT failure", err)
			}
			if wrapper.name == "workspace" && callbackCalls != 0 {
				t.Fatalf("workspace callback calls=%d, want 0", callbackCalls)
			}
			if wrapper.name == "transition" && callbackCalls != 1 {
				t.Fatalf("transition callback calls=%d, want 1", callbackCalls)
			}
			var revision, state, revisionClass string
			if err := store.DB().QueryRow(`
				SELECT CAST(workspace_revision AS TEXT), status, typeof(workspace_revision)
				FROM workspace_bindings WHERE project_id=? AND workspace_id=?
			`, binding.Scope.ProjectID, binding.Scope.WorkspaceID).Scan(&revision, &state, &revisionClass); err != nil {
				t.Fatal(err)
			}
			if revision != "malformed" || revisionClass != "text" || state != "clean" {
				t.Fatalf("retained malformed state=(%q,%q,%q)", revision, revisionClass, state)
			}
		})
	}
}

func TestWorkspaceRevisionTrackerMaxInt64AllowsReadsButRejectsDirtyCommit(t *testing.T) {
	for _, wrapper := range workspaceRevisionTestWrappers() {
		t.Run(wrapper.name, func(t *testing.T) {
			store, repo := openWorkspaceStore(t)
			binding := createBinding(t, repo,
				"00000000-0000-4000-8000-000000000001",
				"00000000-0000-4000-8000-000000000011",
				"/checkout", 1, 11)
			if _, err := store.DB().Exec(`
				UPDATE workspace_bindings SET workspace_revision=?
				WHERE project_id=? AND workspace_id=?
			`, int64(math.MaxInt64), binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if err := wrapper.run(ctx, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
				revision, err := tx.projectedWorkspaceRevision(ctx)
				if err != nil {
					return err
				}
				if revision != math.MaxInt64 {
					return fmt.Errorf("clean max projected revision=%d", revision)
				}
				_, err = tx.Workspace(ctx)
				return err
			}); err != nil {
				t.Fatalf("clean max transaction: %v", err)
			}
			assertWorkspaceRevisionTestState(t, store.DB(), binding.Scope, math.MaxInt64, "clean")

			err := wrapper.run(ctx, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
				if err := tx.markWorkspaceDirty(ctx); err != nil {
					return err
				}
				if _, err := tx.projectedWorkspaceRevision(ctx); err == nil {
					return fmt.Errorf("dirty max projected revision unexpectedly succeeded")
				}
				return updateWorkspaceRevisionTestStatus(ctx, tx, "pending")
			})
			if err == nil || errors.Is(err, ErrCommitOutcomeUnknown) {
				t.Fatalf("dirty max error=%v, want pre-COMMIT overflow failure", err)
			}
			assertWorkspaceRevisionTestState(t, store.DB(), binding.Scope, math.MaxInt64, "clean")
		})
	}
}

func TestWorkspaceRevisionTrackerLazyLoadFinalizerSQLAndTransitionReceiptOrdering(t *testing.T) {
	for _, wrapper := range workspaceRevisionTestWrappers() {
		t.Run(wrapper.name+" clean no-op", func(t *testing.T) {
			databasePath, binding := workspaceRevisionTraceFixture(t)
			allowed := []string{"workspace_bindings"}
			if wrapper.name == "transition" {
				allowed = []string{"workspace_transition_receipts"}
			}
			tracedDB, trace := openTransitionTraceDB(t, databasePath, allowed)
			repo := NewWorkspaceRepo(tracedDB)
			if err := wrapper.run(context.Background(), repo, binding.Scope, func(*WorkspaceMutationTx) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if wrapper.name == "transition" {
				assertTransitionSelectTables(t, trace, []string{"workspace_transition_receipts"})
			}
			assertWorkspaceRevisionTrackerFinalizerWrites(t, trace, 0)
		})

		t.Run(wrapper.name+" many marks", func(t *testing.T) {
			databasePath, binding := workspaceRevisionTraceFixture(t)
			allowed := []string{"workspace_bindings"}
			if wrapper.name == "transition" {
				allowed = []string{"workspace_transition_receipts", "workspace_bindings"}
			}
			tracedDB, trace := openTransitionTraceDB(t, databasePath, allowed)
			repo := NewWorkspaceRepo(tracedDB)
			ctx := context.Background()
			if err := wrapper.run(ctx, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
				for range 5 {
					if err := tx.markWorkspaceDirty(ctx); err != nil {
						return err
					}
				}
				revision, err := tx.projectedWorkspaceRevision(ctx)
				if err != nil || revision != 2 {
					return fmt.Errorf("projected revision=%d: %w", revision, err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if wrapper.name == "transition" {
				assertTransitionSelectTables(t, trace, []string{"workspace_transition_receipts", "workspace_bindings"})
			} else {
				assertWorkspaceRevisionTrackerSelects(t, trace, []string{"workspace_bindings", "workspace_bindings"})
			}
			assertWorkspaceRevisionTrackerFinalizerWrites(t, trace, 1)
		})
	}
}

func updateWorkspaceRevisionTestStatus(ctx context.Context, tx *WorkspaceMutationTx, state string) error {
	result, err := tx.conn.ExecContext(ctx, `
		UPDATE workspace_bindings SET status=? WHERE project_id=? AND workspace_id=?
	`, state, tx.scope.ProjectID, tx.scope.WorkspaceID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("test status update affected %d rows", affected)
	}
	return nil
}

func assertWorkspaceRevisionTestState(t *testing.T, db *sql.DB, scope types.WorkspaceScope, revision int64, state string) {
	t.Helper()
	var gotRevision int64
	var gotState string
	if err := db.QueryRow(`
		SELECT workspace_revision, status FROM workspace_bindings
		WHERE project_id=? AND workspace_id=?
	`, scope.ProjectID, scope.WorkspaceID).Scan(&gotRevision, &gotState); err != nil {
		t.Fatal(err)
	}
	if gotRevision != revision || gotState != state {
		t.Fatalf("workspace state=(revision=%d,status=%q), want (%d,%q)", gotRevision, gotState, revision, state)
	}
}

func corruptWorkspaceRevisionForTest(t *testing.T, db *sql.DB, scope types.WorkspaceScope, expression string) {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatal(err)
	}
	query := `UPDATE workspace_bindings SET workspace_revision=` + expression + ` WHERE project_id=? AND workspace_id=?`
	if _, err := conn.ExecContext(context.Background(), query, scope.ProjectID, scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
}

func workspaceRevisionTraceFixture(t *testing.T) (string, types.WorkspaceBinding) {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011",
		"/checkout", 1, 11)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return databasePath, binding
}

func assertWorkspaceRevisionTrackerSelects(t *testing.T, trace *transitionTraceDriver, tables []string) {
	t.Helper()
	selects := make([]transitionSQLCall, 0)
	for _, call := range trace.snapshot() {
		if call.kind == "query" && strings.Contains(strings.ToUpper(call.query), "SELECT") {
			selects = append(selects, call)
		}
	}
	if len(selects) != len(tables) {
		t.Fatalf("SELECT trace=%+v, want tables=%v", selects, tables)
	}
	for index, table := range tables {
		if !strings.Contains(selects[index].query, table) {
			t.Fatalf("SELECT %d=%q, want table %q", index, selects[index].query, table)
		}
	}
}

func assertWorkspaceRevisionTrackerFinalizerWrites(t *testing.T, trace *transitionTraceDriver, want int) {
	t.Helper()
	writes := 0
	for _, call := range trace.snapshot() {
		normalized := strings.Join(strings.Fields(call.query), " ")
		if !strings.Contains(normalized, "UPDATE workspace_bindings SET workspace_revision=") {
			continue
		}
		writes++
		if strings.Contains(normalized, "workspace_revision + 1") || strings.Contains(normalized, "workspace_revision+1") {
			t.Fatalf("finalizer used SQL arithmetic: %q", call.query)
		}
		wantSQL := "UPDATE workspace_bindings SET workspace_revision=? WHERE project_id=? AND workspace_id=? AND workspace_revision=?"
		if normalized != wantSQL {
			t.Fatalf("finalizer SQL=%q, want %q", normalized, wantSQL)
		}
	}
	if writes != want {
		t.Fatalf("finalizer writes=%d, want %d; calls=%+v", writes, want, trace.snapshot())
	}
}
