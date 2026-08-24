package localstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
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

func TestWorkspaceRevisionCoreWriterInventory(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		setup  func(*testing.T, *Store, *WorkspaceRepo, types.WorkspaceBinding)
		mutate func(*testing.T, *WorkspaceRepo, types.WorkspaceBinding) error
		verify func(*testing.T, *WorkspaceRepo, types.WorkspaceBinding)
	}{
		{
			name: "AdvanceAcceptedBase",
			mutate: func(t *testing.T, repo *WorkspaceRepo, binding types.WorkspaceBinding) error {
				return repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
					current, err := tx.Workspace(ctx)
					if err != nil {
						return err
					}
					_, err = tx.AdvanceAcceptedBase(ctx, WorkspaceAcceptedBaseTransition{
						Expected: current, ObservedRef: "refs/heads/revised", ObservedCommitSHA: strings.Repeat("c", 40),
						ObservedTree: changedWorkspaceTree(t, binding, "revision inventory"), NextState: "pending",
					})
					return err
				})
			},
			verify: func(t *testing.T, repo *WorkspaceRepo, binding types.WorkspaceBinding) {
				workspace, err := repo.Workspace(ctx, binding.Scope)
				if err != nil || workspace.Binding.AcceptedRef != "refs/heads/revised" || workspace.State != "pending" {
					t.Fatalf("accepted-base committed state=(%+v,%v)", workspace, err)
				}
			},
		},
		{
			name: "TransitionOperations",
			setup: func(t *testing.T, store *Store, _ *WorkspaceRepo, binding types.WorkspaceBinding) {
				insertWorkspaceOperation(t, store, binding.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000101"), "active")
				insertWorkspaceOperation(t, store, binding.Scope, 2, validWorkspaceOperation("00000000-0000-4000-8000-000000000102"), "active")
			},
			mutate: func(t *testing.T, repo *WorkspaceRepo, binding types.WorkspaceBinding) error {
				return repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
					audit, err := tx.OperationAudit(ctx)
					if err != nil || len(audit) != 2 {
						if err == nil {
							err = fmt.Errorf("operation audit length=%d, want 2", len(audit))
						}
						return err
					}
					return tx.TransitionOperations(ctx, []WorkspaceOperation{audit[0].WorkspaceOperation, audit[1].WorkspaceOperation}, "materialized", nil)
				})
			},
			verify: func(t *testing.T, repo *WorkspaceRepo, binding types.WorkspaceBinding) {
				requireCoreWriterOperationStates(t, repo, binding.Scope, []string{"materialized", "materialized"})
			},
		},
		{
			name: "InsertActiveOperations",
			mutate: func(t *testing.T, repo *WorkspaceRepo, binding types.WorkspaceBinding) error {
				first := validWorkspaceOperation("00000000-0000-4000-8000-000000000103")
				second := validWorkspaceOperation("00000000-0000-4000-8000-000000000104")
				firstCanonical, err := state.CanonicalOperation(first)
				if err != nil {
					t.Fatal(err)
				}
				secondCanonical, err := state.CanonicalOperation(second)
				if err != nil {
					t.Fatal(err)
				}
				return repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
					return tx.InsertActiveOperations(ctx, []WorkspaceOperationInsert{
						{Generation: 1, OperationID: first.ID, OperationJSON: firstCanonical},
						{Generation: 2, OperationID: second.ID, OperationJSON: secondCanonical},
					})
				})
			},
			verify: func(t *testing.T, repo *WorkspaceRepo, binding types.WorkspaceBinding) {
				requireCoreWriterOperationStates(t, repo, binding.Scope, []string{"active", "active"})
			},
		},
		{
			name: "SetStatus",
			mutate: func(_ *testing.T, repo *WorkspaceRepo, binding types.WorkspaceBinding) error {
				return repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error { return tx.SetStatus(ctx, "pending") })
			},
			verify: func(t *testing.T, repo *WorkspaceRepo, binding types.WorkspaceBinding) {
				workspace, err := repo.Workspace(ctx, binding.Scope)
				if err != nil || workspace.State != "pending" {
					t.Fatalf("status committed state=(%+v,%v)", workspace, err)
				}
			},
		},
		{
			name: "SetStatusReturningUpdatedAt",
			mutate: func(t *testing.T, repo *WorkspaceRepo, binding types.WorkspaceBinding) error {
				return repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
					updatedAt, err := tx.SetStatusReturningUpdatedAt(ctx, "blocked")
					if err != nil || updatedAt.IsZero() || updatedAt.Location() != time.UTC {
						t.Fatalf("SetStatusReturningUpdatedAt()=(%v,%v)", updatedAt, err)
					}
					return nil
				})
			},
			verify: func(t *testing.T, repo *WorkspaceRepo, binding types.WorkspaceBinding) {
				workspace, err := repo.Workspace(ctx, binding.Scope)
				if err != nil || workspace.State != "blocked" {
					t.Fatalf("status returning committed state=(%+v,%v)", workspace, err)
				}
			},
		},
		{
			name: "UpsertCandidate",
			mutate: func(t *testing.T, repo *WorkspaceRepo, binding types.WorkspaceBinding) error {
				candidate := workspaceCandidateRecord(t, binding, false, 0)
				return repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error { return tx.UpsertCandidate(ctx, candidate) })
			},
			verify: func(t *testing.T, repo *WorkspaceRepo, binding types.WorkspaceBinding) {
				candidate := requireCoreWriterCandidate(t, repo, binding.Scope)
				if candidate.ImportedBy != workspaceCandidateImporter || candidate.AcceptedBaseDigest != state.Digest(binding.AcceptedTreeDigest) {
					t.Fatalf("candidate committed state=%+v", candidate)
				}
			},
		},
		{
			name: "DeleteCandidate",
			setup: func(t *testing.T, store *Store, _ *WorkspaceRepo, binding types.WorkspaceBinding) {
				candidate := workspaceCandidateRecord(t, binding, false, 0)
				direct, directBytes := encodedWorkspaceSnapshot(t, binding.Scope.ProjectID, binding.Repository)
				insertWorkspaceCandidate(t, store, binding.Scope, candidate.AcceptedBaseDigest, direct.Digest, directBytes, nil, 0)
			},
			mutate: func(_ *testing.T, repo *WorkspaceRepo, binding types.WorkspaceBinding) error {
				return repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error { return tx.DeleteCandidate(ctx, true) })
			},
			verify: func(t *testing.T, repo *WorkspaceRepo, binding types.WorkspaceBinding) {
				if candidate := requireCoreWriterCandidate(t, repo, binding.Scope); candidate != nil {
					t.Fatalf("deleted candidate=%+v, want nil", candidate)
				}
			},
		},
		{
			name: "ReplaceOpenConflictOccurrences",
			mutate: func(t *testing.T, repo *WorkspaceRepo, binding types.WorkspaceBinding) error {
				evidence := workspaceConflictEvidence(101, state.RecordKey{Kind: "task", ID: "00000000-0000-4000-8000-000000000021"})
				return repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
					_, err := tx.ReplaceOpenConflictOccurrences(ctx, []WorkspaceConflictEvidence{evidence}, time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC))
					return err
				})
			},
			verify: func(t *testing.T, repo *WorkspaceRepo, binding types.WorkspaceBinding) {
				if err := repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
					occurrences, err := tx.OpenConflictOccurrences(ctx)
					if err != nil || len(occurrences) != 1 || occurrences[0].ConflictID != "sha256:0000000000000000000000000000000000000000000000000000000000000065" {
						t.Fatalf("open conflict committed state=(%+v,%v)", occurrences, err)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "InsertStash",
			mutate: func(t *testing.T, repo *WorkspaceRepo, binding types.WorkspaceBinding) error {
				stash := validWorkspaceStash(t, binding, "00000000-0000-4000-8000-000000000103")
				return repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error { return tx.InsertStash(ctx, stash) })
			},
			verify: func(t *testing.T, repo *WorkspaceRepo, binding types.WorkspaceBinding) {
				if err := repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
					stash, err := tx.Stash(ctx, "00000000-0000-4000-8000-000000000103")
					if err != nil || stash == nil {
						t.Fatalf("stash committed state=(%+v,%v)", stash, err)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "DeleteStash",
			setup: func(t *testing.T, _ *Store, repo *WorkspaceRepo, binding types.WorkspaceBinding) {
				stash := validWorkspaceStash(t, binding, "00000000-0000-4000-8000-000000000104")
				if err := repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error { return tx.InsertStash(ctx, stash) }); err != nil {
					t.Fatal(err)
				}
			},
			mutate: func(_ *testing.T, repo *WorkspaceRepo, binding types.WorkspaceBinding) error {
				return repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
					return tx.DeleteStash(ctx, "00000000-0000-4000-8000-000000000104")
				})
			},
			verify: func(t *testing.T, repo *WorkspaceRepo, binding types.WorkspaceBinding) {
				if err := repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
					stash, err := tx.Stash(ctx, "00000000-0000-4000-8000-000000000104")
					if err != nil || stash != nil {
						t.Fatalf("deleted stash committed state=(%+v,%v)", stash, err)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "InsertTransitionReceipt",
			mutate: func(t *testing.T, repo *WorkspaceRepo, binding types.WorkspaceBinding) error {
				receipt := validWorkspaceTransitionReceipt(t, "00000000-0000-1000-8000-000000000105", "stash", "clean")
				return repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error { return tx.InsertTransitionReceipt(ctx, receipt) })
			},
			verify: func(t *testing.T, repo *WorkspaceRepo, binding types.WorkspaceBinding) {
				receipt, err := repo.TransitionReceipt(ctx, binding.Scope, "00000000-0000-1000-8000-000000000105")
				if err != nil || receipt == nil || receipt.Action != "stash" {
					t.Fatalf("receipt committed state=(%+v,%v)", receipt, err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, repo, binding := coreWriterInventoryFixture(t)
			if test.setup != nil {
				test.setup(t, store, repo, binding)
			}
			before := coreWriterRevision(t, repo, binding.Scope)
			if err := test.mutate(t, repo, binding); err != nil {
				t.Fatal(err)
			}
			test.verify(t, repo, binding)
			if got := coreWriterRevision(t, repo, binding.Scope); got != before+1 {
				t.Fatalf("workspace revision after %s=%d, want %d", test.name, got, before+1)
			}
		})
	}

	t.Run("UpsertCandidate changed update", func(t *testing.T) {
		_, repo, binding := coreWriterInventoryFixture(t)
		seed := workspaceCandidateRecord(t, binding, false, 0)
		if err := repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
			return tx.UpsertCandidate(ctx, seed)
		}); err != nil {
			t.Fatal(err)
		}
		changed := seed
		changed.DirectSnapshot.Project.Name = "changed inventory candidate"
		changed.DirectSnapshot.Project.UpdatedAt = changed.DirectSnapshot.Project.UpdatedAt.Add(time.Minute)
		var err error
		changed.DirectSnapshot, _ = encodedSnapshot(t, changed.DirectSnapshot)
		changed.WorkingTreeDigest = changed.DirectSnapshot.Digest
		changed.ImportedAt = changed.ImportedAt.Add(time.Minute)
		if changed.DirectSnapshot.Project.Name == seed.DirectSnapshot.Project.Name || changed.WorkingTreeDigest == seed.WorkingTreeDigest {
			t.Fatal("changed candidate fixture is semantically identical to seed")
		}
		before := coreWriterRevision(t, repo, binding.Scope)
		if err = repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
			return tx.UpsertCandidate(ctx, changed)
		}); err != nil {
			t.Fatal(err)
		}
		if got := requireCoreWriterCandidate(t, repo, binding.Scope); !reflect.DeepEqual(got, &changed) {
			t.Fatalf("changed candidate=%+v, want %+v", got, changed)
		}
		if got := coreWriterRevision(t, repo, binding.Scope); got != before+1 {
			t.Fatalf("changed candidate revision=%d, want %d", got, before+1)
		}
	})

	t.Run("ReplaceOpenConflictOccurrences resolves open evidence", func(t *testing.T) {
		store, repo, binding := coreWriterInventoryFixture(t)
		evidence := workspaceConflictEvidence(108, state.RecordKey{Kind: "task", ID: "00000000-0000-4000-8000-000000000021"})
		if err := repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
			_, err := tx.ReplaceOpenConflictOccurrences(ctx, []WorkspaceConflictEvidence{evidence}, time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		open := coreWriterOpenConflicts(t, repo, binding.Scope)
		if len(open) != 1 {
			t.Fatalf("seed open conflicts=%+v, want one", open)
		}
		before := coreWriterRevision(t, repo, binding.Scope)
		resolvedAt := time.Date(2026, 8, 17, 12, 1, 0, 0, time.UTC)
		if err := repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
			_, err := tx.ReplaceOpenConflictOccurrences(ctx, nil, resolvedAt)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if got := coreWriterOpenConflicts(t, repo, binding.Scope); len(got) != 0 {
			t.Fatalf("resolved open conflicts=%+v, want none", got)
		}
		assertCoreWriterResolvedConflict(t, store, binding.Scope, open[0], resolvedAt)
		if got := coreWriterRevision(t, repo, binding.Scope); got != before+1 {
			t.Fatalf("resolved conflict revision=%d, want %d", got, before+1)
		}
	})

	t.Run("multiple helpers commit once", func(t *testing.T) {
		_, repo, binding := coreWriterInventoryFixture(t)
		operation := validWorkspaceOperation("00000000-0000-4000-8000-000000000106")
		canonical, err := state.CanonicalOperation(operation)
		if err != nil {
			t.Fatal(err)
		}
		receipt := validWorkspaceTransitionReceipt(t, "00000000-0000-1000-8000-000000000106", "stash", "clean")
		before := coreWriterRevision(t, repo, binding.Scope)
		if err := repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
			if err := tx.InsertActiveOperations(ctx, []WorkspaceOperationInsert{{Generation: 1, OperationID: operation.ID, OperationJSON: canonical}}); err != nil {
				return err
			}
			if err := tx.SetStatus(ctx, "pending"); err != nil {
				return err
			}
			return tx.InsertTransitionReceipt(ctx, receipt)
		}); err != nil {
			t.Fatal(err)
		}
		requireCoreWriterOperationState(t, repo, binding.Scope, "active")
		if workspace, err := repo.Workspace(ctx, binding.Scope); err != nil || workspace.State != "pending" {
			t.Fatalf("multi-helper workspace=(%+v,%v)", workspace, err)
		}
		if receipt, err := repo.TransitionReceipt(ctx, binding.Scope, receipt.RequestID); err != nil || receipt == nil {
			t.Fatalf("multi-helper receipt=(%+v,%v)", receipt, err)
		}
		if got := coreWriterRevision(t, repo, binding.Scope); got != before+1 {
			t.Fatalf("multi-helper workspace revision=%d, want %d", got, before+1)
		}
	})

	t.Run("TransitionOperations nil batch is revision stable", func(t *testing.T) {
		_, repo, binding := coreWriterInventoryFixture(t)
		assertCoreWriterRevisionStable(t, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
			return tx.TransitionOperations(ctx, nil, "materialized", nil)
		})
	})

	t.Run("TransitionOperations empty batch is revision stable", func(t *testing.T) {
		_, repo, binding := coreWriterInventoryFixture(t)
		assertCoreWriterRevisionStable(t, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
			return tx.TransitionOperations(ctx, []WorkspaceOperation{}, "materialized", nil)
		})
	})

	t.Run("InsertActiveOperations nil batch is revision stable", func(t *testing.T) {
		_, repo, binding := coreWriterInventoryFixture(t)
		assertCoreWriterRevisionStable(t, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
			return tx.InsertActiveOperations(ctx, nil)
		})
	})

	t.Run("InsertActiveOperations empty batch is revision stable", func(t *testing.T) {
		_, repo, binding := coreWriterInventoryFixture(t)
		assertCoreWriterRevisionStable(t, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
			return tx.InsertActiveOperations(ctx, []WorkspaceOperationInsert{})
		})
	})

	t.Run("DeleteCandidate absent optional delete is revision stable", func(t *testing.T) {
		_, repo, binding := coreWriterInventoryFixture(t)
		assertCoreWriterRevisionStable(t, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
			return tx.DeleteCandidate(ctx, false)
		})
		if candidate := requireCoreWriterCandidate(t, repo, binding.Scope); candidate != nil {
			t.Fatalf("absent optional delete created candidate=%+v", candidate)
		}
	})

	t.Run("AdvanceAcceptedBase identical transition preserves metadata and revision", func(t *testing.T) {
		store, repo, binding := coreWriterInventoryFixture(t)
		forceCoreWriterMutationMetadata(t, store, binding.Scope,
			time.Date(2026, 7, 1, 4, 5, 5, 0, time.UTC), time.Date(2026, 7, 2, 4, 5, 6, 0, time.UTC))
		beforeMetadata := readCoreWriterBindingMetadata(t, store, binding.Scope)
		assertCoreWriterRevisionStable(t, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
			workspace, err := tx.Workspace(ctx)
			if err != nil {
				return err
			}
			tree, err := state.EncodeTree(workspace.Snapshot)
			if err != nil {
				return err
			}
			got, err := tx.AdvanceAcceptedBase(ctx, WorkspaceAcceptedBaseTransition{
				Expected: workspace, ObservedRef: workspace.Binding.AcceptedRef, ObservedCommitSHA: workspace.Binding.AcceptedCommitSHA,
				ObservedTree: tree, NextState: workspace.State,
			})
			if err != nil || !equalWorkspaceRecords(got, workspace) {
				return fmt.Errorf("identical AdvanceAcceptedBase=(%+v,%v)", got, err)
			}
			return nil
		})
		assertCoreWriterBindingMetadataEqual(t, readCoreWriterBindingMetadata(t, store, binding.Scope), beforeMetadata)
	})

	t.Run("SetStatus identical state preserves metadata and revision", func(t *testing.T) {
		store, repo, binding := coreWriterInventoryFixture(t)
		forceCoreWriterMutationMetadata(t, store, binding.Scope,
			time.Date(2026, 7, 1, 4, 5, 5, 0, time.UTC), time.Date(2026, 7, 2, 4, 5, 6, 0, time.UTC))
		beforeMetadata := readCoreWriterBindingMetadata(t, store, binding.Scope)
		assertCoreWriterRevisionStable(t, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
			return tx.SetStatus(ctx, "clean")
		})
		assertCoreWriterBindingMetadataEqual(t, readCoreWriterBindingMetadata(t, store, binding.Scope), beforeMetadata)
	})

	t.Run("SetStatusReturningUpdatedAt identical state preserves timestamp metadata and revision", func(t *testing.T) {
		store, repo, binding := coreWriterInventoryFixture(t)
		forceCoreWriterMutationMetadata(t, store, binding.Scope,
			time.Date(2026, 7, 1, 4, 5, 5, 0, time.UTC), time.Date(2026, 7, 2, 4, 5, 6, 0, time.UTC))
		beforeMetadata := readCoreWriterBindingMetadata(t, store, binding.Scope)
		assertCoreWriterRevisionStable(t, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
			updatedAt, err := tx.SetStatusReturningUpdatedAt(ctx, "clean")
			if err != nil {
				return err
			}
			assertCoreWriterUTCTime(t, updatedAt)
			if !updatedAt.Equal(beforeMetadata.UpdatedAt) {
				return fmt.Errorf("identical SetStatusReturningUpdatedAt=%v, want %v", updatedAt, beforeMetadata.UpdatedAt)
			}
			return nil
		})
		assertCoreWriterBindingMetadataEqual(t, readCoreWriterBindingMetadata(t, store, binding.Scope), beforeMetadata)
	})

	t.Run("UpsertCandidate identical value preserves candidate and revision", func(t *testing.T) {
		_, repo, binding := coreWriterInventoryFixture(t)
		candidate := workspaceCandidateRecord(t, binding, false, 0)
		if err := repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
			return tx.UpsertCandidate(ctx, candidate)
		}); err != nil {
			t.Fatal(err)
		}
		beforeCandidate := requireCoreWriterCandidate(t, repo, binding.Scope)
		assertCoreWriterRevisionStable(t, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
			return tx.UpsertCandidate(ctx, candidate)
		})
		if afterCandidate := requireCoreWriterCandidate(t, repo, binding.Scope); !reflect.DeepEqual(afterCandidate, beforeCandidate) {
			t.Fatalf("identical candidate upsert=%+v, want %+v", afterCandidate, beforeCandidate)
		}
	})

	t.Run("ReplaceOpenConflictOccurrences identical membership and evidence preserves revision", func(t *testing.T) {
		_, repo, binding := coreWriterInventoryFixture(t)
		evidence := workspaceConflictEvidence(107, state.RecordKey{Kind: "task", ID: "00000000-0000-4000-8000-000000000021"})
		if err := repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
			_, err := tx.ReplaceOpenConflictOccurrences(ctx, []WorkspaceConflictEvidence{evidence}, time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		beforeConflicts := coreWriterOpenConflicts(t, repo, binding.Scope)
		assertCoreWriterRevisionStable(t, repo, binding.Scope, func(tx *WorkspaceMutationTx) error {
			_, err := tx.ReplaceOpenConflictOccurrences(ctx, []WorkspaceConflictEvidence{evidence}, time.Date(2026, 8, 17, 12, 1, 0, 0, time.UTC))
			return err
		})
		if afterConflicts := coreWriterOpenConflicts(t, repo, binding.Scope); !reflect.DeepEqual(afterConflicts, beforeConflicts) {
			t.Fatalf("identical conflict replacement=%+v, want %+v", afterConflicts, beforeConflicts)
		}
	})
}

func coreWriterInventoryFixture(t *testing.T) (*Store, *WorkspaceRepo, types.WorkspaceBinding) {
	t.Helper()
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011",
		"/revision-inventory", 1, 11)
	return store, repo, binding
}

func coreWriterRevision(t *testing.T, repo *WorkspaceRepo, scope types.WorkspaceScope) int64 {
	t.Helper()
	workspace, err := repo.Workspace(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	return workspace.WorkspaceRevision
}

type coreWriterBindingMetadata struct {
	CreatedAt time.Time
	UpdatedAt time.Time
}

func readCoreWriterBindingMetadata(t *testing.T, store *Store, scope types.WorkspaceScope) coreWriterBindingMetadata {
	t.Helper()
	var metadata coreWriterBindingMetadata
	if err := store.DB().QueryRow(`
		SELECT created_at, updated_at FROM workspace_bindings
		WHERE project_id=? AND workspace_id=?
	`, scope.ProjectID, scope.WorkspaceID).Scan(&metadata.CreatedAt, &metadata.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	assertCoreWriterUTCTime(t, metadata.CreatedAt)
	assertCoreWriterUTCTime(t, metadata.UpdatedAt)
	return metadata
}

func forceCoreWriterMutationMetadata(t *testing.T, store *Store, scope types.WorkspaceScope, createdAt, updatedAt time.Time) {
	t.Helper()
	assertCoreWriterUTCTime(t, createdAt)
	assertCoreWriterUTCTime(t, updatedAt)
	if _, err := store.DB().Exec(`
		UPDATE workspace_bindings SET created_at=?, updated_at=?
		WHERE project_id=? AND workspace_id=?
	`, createdAt, updatedAt, scope.ProjectID, scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
}

func assertCoreWriterUTCTime(t *testing.T, value time.Time) {
	t.Helper()
	_, offset := value.Zone()
	if value.IsZero() || offset != 0 || value.Location() != time.UTC {
		t.Fatalf("timestamp=%v, want nonzero UTC", value)
	}
}

func assertCoreWriterBindingMetadataEqual(t *testing.T, got, want coreWriterBindingMetadata) {
	t.Helper()
	assertCoreWriterUTCTime(t, got.CreatedAt)
	assertCoreWriterUTCTime(t, got.UpdatedAt)
	if !got.CreatedAt.Equal(want.CreatedAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("binding metadata=(created=%v,updated=%v), want (created=%v,updated=%v)", got.CreatedAt, got.UpdatedAt, want.CreatedAt, want.UpdatedAt)
	}
}

func assertCoreWriterRevisionStable(t *testing.T, repo *WorkspaceRepo, scope types.WorkspaceScope, mutate func(*WorkspaceMutationTx) error) {
	t.Helper()
	before := coreWriterRevision(t, repo, scope)
	if err := repo.WithImmediateWorkspace(context.Background(), scope, mutate); err != nil {
		t.Fatal(err)
	}
	if got := coreWriterRevision(t, repo, scope); got != before {
		t.Fatalf("semantic no-op revision=%d, want %d", got, before)
	}
}

func requireCoreWriterOperationState(t *testing.T, repo *WorkspaceRepo, scope types.WorkspaceScope, want string) {
	requireCoreWriterOperationStates(t, repo, scope, []string{want})
}

func requireCoreWriterOperationStates(t *testing.T, repo *WorkspaceRepo, scope types.WorkspaceScope, want []string) {
	t.Helper()
	if err := repo.WithImmediateWorkspace(context.Background(), scope, func(tx *WorkspaceMutationTx) error {
		audit, err := tx.OperationAudit(context.Background())
		if err != nil || len(audit) != len(want) {
			t.Fatalf("operation committed state=(%+v,%v), want %d operations", audit, err, len(want))
		}
		for index, state := range want {
			if audit[index].State != state {
				t.Fatalf("operation %d state=%q, want %q", index, audit[index].State, state)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func requireCoreWriterCandidate(t *testing.T, repo *WorkspaceRepo, scope types.WorkspaceScope) *WorkspaceCandidateRecord {
	t.Helper()
	var candidate *WorkspaceCandidateRecord
	if err := repo.WithImmediateWorkspace(context.Background(), scope, func(tx *WorkspaceMutationTx) error {
		var err error
		candidate, err = tx.Candidate(context.Background())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return candidate
}

func coreWriterOpenConflicts(t *testing.T, repo *WorkspaceRepo, scope types.WorkspaceScope) []WorkspaceConflictOccurrence {
	t.Helper()
	var conflicts []WorkspaceConflictOccurrence
	if err := repo.WithImmediateWorkspace(context.Background(), scope, func(tx *WorkspaceMutationTx) error {
		var err error
		conflicts, err = tx.OpenConflictOccurrences(context.Background())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return conflicts
}

func assertCoreWriterResolvedConflict(t *testing.T, store *Store, scope types.WorkspaceScope, want WorkspaceConflictOccurrence, resolvedAt time.Time) {
	t.Helper()
	var (
		projectID, workspaceID, occurrenceID, conflictID string
		kind, recordID, fieldPath, conflictKind          string
		baseJSON, oursJSON, theirsJSON, status           string
		createdAt, gotResolvedAt                         time.Time
	)
	if err := store.DB().QueryRow(`
		SELECT project_id, workspace_id, occurrence_id, conflict_id, record_kind, record_id,
		       field_path, conflict_kind, base_json, ours_json, theirs_json, state, created_at, resolved_at
		FROM workspace_conflicts
		WHERE project_id=? AND workspace_id=? AND occurrence_id=?
	`, scope.ProjectID, scope.WorkspaceID, want.OccurrenceID).Scan(
		&projectID, &workspaceID, &occurrenceID, &conflictID, &kind, &recordID,
		&fieldPath, &conflictKind, &baseJSON, &oursJSON, &theirsJSON, &status, &createdAt, &gotResolvedAt,
	); err != nil {
		t.Fatal(err)
	}
	gotEvidence := WorkspaceConflictEvidence{
		ConflictID: conflictID, Key: state.RecordKey{Kind: kind, ID: recordID}, FieldPath: fieldPath,
		ConflictKind: conflictKind, BaseJSON: baseJSON, OursJSON: oursJSON, TheirsJSON: theirsJSON,
	}
	if projectID != scope.ProjectID || workspaceID != string(scope.WorkspaceID) || occurrenceID != want.OccurrenceID ||
		status != "resolved" || gotEvidence != want.WorkspaceConflictEvidence || !createdAt.Equal(want.CreatedAt) {
		t.Fatalf("resolved conflict state=(scope=%q/%q occurrence=%q evidence=%+v status=%q created=%v), want %+v", projectID, workspaceID, occurrenceID, gotEvidence, status, createdAt, want)
	}
	assertCoreWriterUTCTime(t, createdAt)
	assertCoreWriterUTCTime(t, gotResolvedAt)
	if !gotResolvedAt.Equal(resolvedAt) {
		t.Fatalf("resolved conflict timestamp=%v, want %v", gotResolvedAt, resolvedAt)
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
