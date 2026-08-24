package localstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/H4RL33/wormhole/internal/types"
	sqlite "modernc.org/sqlite"
)

func TestWorkspaceTransitionBoundaryAPIExists(t *testing.T) {
	var repo *WorkspaceRepo
	scope := types.WorkspaceScope{
		ProjectID:   "00000000-0000-4000-8000-000000000001",
		WorkspaceID: "00000000-0000-4000-8000-000000000011",
	}
	requestID := "00000000-0000-1000-8000-000000000031"
	if _, err := repo.TransitionReceiptByKey(context.Background(), scope, requestID); err == nil {
		t.Fatal("nil repository lookup unexpectedly succeeded")
	}
	if err := repo.WithImmediateWorkspaceTransition(context.Background(), scope, requestID,
		func(*WorkspaceMutationTx, *WorkspaceTransitionReceiptRecord) error { return nil }); err == nil {
		t.Fatal("nil repository transaction unexpectedly succeeded")
	}
}

func TestTransitionReceiptByKeyExactReadAbsentUnregisteredIsolationAndRestart(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	a := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	b := createBinding(t, repo, a.Scope.ProjectID, "00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	c := createBinding(t, repo, "00000000-0000-4000-8000-000000000002", string(a.Scope.WorkspaceID), "/checkout-c", 3, 13)
	requestID := "00000000-0000-1000-8000-000000000031"
	receipts := []WorkspaceTransitionReceiptInsert{
		validWorkspaceTransitionReceipt(t, requestID, "stash", "clean"),
		validWorkspaceTransitionReceipt(t, requestID, "restore", "conflicted"),
		validWorkspaceTransitionReceipt(t, requestID, "discard", "clean"),
	}
	for index, binding := range []types.WorkspaceBinding{a, b, c} {
		if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
			return tx.InsertTransitionReceipt(context.Background(), receipts[index])
		}); err != nil {
			t.Fatal(err)
		}
	}
	for index, scope := range []types.WorkspaceScope{a.Scope, b.Scope, c.Scope} {
		got, err := repo.TransitionReceiptByKey(context.Background(), scope, requestID)
		if err != nil {
			t.Fatal(err)
		}
		assertWorkspaceTransitionReceipt(t, got, receipts[index])
	}
	absentID := "00000000-0000-1000-8000-000000000032"
	if got, err := repo.TransitionReceiptByKey(context.Background(), a.Scope, absentID); err != nil || got != nil {
		t.Fatalf("absent receipt=(%+v,%v), want (nil,nil)", got, err)
	}
	unregistered := types.WorkspaceScope{ProjectID: "00000000-0000-4000-8000-000000000099", WorkspaceID: "00000000-0000-4000-8000-000000000098"}
	if got, err := repo.TransitionReceiptByKey(context.Background(), unregistered, requestID); err != nil || got != nil {
		t.Fatalf("unregistered receipt=(%+v,%v), want (nil,nil)", got, err)
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
	got, err := repo.TransitionReceiptByKey(context.Background(), a.Scope, requestID)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkspaceTransitionReceipt(t, got, receipts[0])
}

func TestTransitionReceiptByKeyRejectsSelectedLogicalCorruption(t *testing.T) {
	for _, test := range []struct {
		name, column, value string
	}{
		{"invalid action", "action", "publish"},
		{"invalid digest", "request_digest", "sha256:" + strings.Repeat("A", 64)},
		{"invalid actor", "actor_json", `{"actor_kind":"human","human_principal_id":"BAD","assurance":"local","occurred_at":"2026-07-29T12:00:00Z"}` + "\n"},
		{"noncanonical result", "result_json", `{"ok": true}` + "\n"},
		{"invalid outcome", "outcome", "pending"},
		{"invalid timestamp", "created_at", "not-a-time"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, repo, binding, receipt := transitionBoundaryFixture(t)
			conn, err := store.DB().Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if _, err := conn.ExecContext(context.Background(), `PRAGMA ignore_check_constraints=ON`); err != nil {
				t.Fatal(err)
			}
			query := "UPDATE workspace_transition_receipts SET " + test.column + "=? WHERE project_id=? AND workspace_id=? AND request_id=?"
			if _, err := conn.ExecContext(context.Background(), query, test.value, binding.Scope.ProjectID, binding.Scope.WorkspaceID, receipt.RequestID); err != nil {
				t.Fatal(err)
			}
			if got, err := repo.TransitionReceiptByKey(context.Background(), binding.Scope, receipt.RequestID); err == nil || got != nil {
				t.Fatalf("corrupt %s receipt=(%+v,%v), want fail closed", test.column, got, err)
			}
		})
	}
}

func TestTransitionReceiptByKeyReadsOnlyReceiptTable(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(fmt.Sprintf("present=%v", present), func(t *testing.T) {
			databasePath := filepath.Join(t.TempDir(), "gateway.db")
			store, err := Open(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			repo := NewWorkspaceRepo(store.DB())
			binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
			receipt := validWorkspaceTransitionReceipt(t, "00000000-0000-1000-8000-000000000031", "discard", "clean")
			if present {
				if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
					return tx.InsertTransitionReceipt(context.Background(), receipt)
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			tracedDB, trace := openTransitionTraceDB(t, databasePath, []string{"workspace_transition_receipts"})
			repo = NewWorkspaceRepo(tracedDB)
			got, err := repo.TransitionReceiptByKey(context.Background(), binding.Scope, receipt.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			if present {
				assertWorkspaceTransitionReceipt(t, got, receipt)
			} else if got != nil {
				t.Fatalf("absent traced receipt=%+v", got)
			}
			assertTransitionSelectTables(t, trace, []string{"workspace_transition_receipts"})
		})
	}
}

func TestWithImmediateWorkspaceTransitionReceiptIsFirstRead(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(fmt.Sprintf("present=%v", present), func(t *testing.T) {
			databasePath := filepath.Join(t.TempDir(), "gateway.db")
			store, err := Open(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			repo := NewWorkspaceRepo(store.DB())
			binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
			receipt := validWorkspaceTransitionReceipt(t, "00000000-0000-1000-8000-000000000031", "discard", "clean")
			if present {
				if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
					return tx.InsertTransitionReceipt(context.Background(), receipt)
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			allowed := []string{"workspace_transition_receipts"}
			if !present {
				allowed = append(allowed, "workspace_bindings")
			}
			tracedDB, trace := openTransitionTraceDB(t, databasePath, allowed)
			repo = NewWorkspaceRepo(tracedDB)
			callbackCalls := 0
			err = repo.WithImmediateWorkspaceTransition(context.Background(), binding.Scope, receipt.RequestID,
				func(tx *WorkspaceMutationTx, got *WorkspaceTransitionReceiptRecord) error {
					callbackCalls++
					if present {
						assertWorkspaceTransitionReceipt(t, got, receipt)
						return nil
					}
					if got != nil {
						t.Fatalf("absent transaction receipt=%+v", got)
					}
					workspace, err := tx.Workspace(context.Background())
					if err != nil || workspace.Binding.Scope != binding.Scope {
						t.Fatalf("tx.Workspace()=(%+v,%v)", workspace, err)
					}
					return nil
				})
			if err != nil || callbackCalls != 1 {
				t.Fatalf("WithImmediateWorkspaceTransition calls=%d err=%v", callbackCalls, err)
			}
			if present {
				assertTransitionSelectTables(t, trace, []string{"workspace_transition_receipts"})
			} else {
				assertTransitionSelectTables(t, trace, []string{"workspace_transition_receipts", "workspace_bindings"})
			}
		})
	}
}

func TestWorkspaceTransitionBoundariesInvalidSyntaxIssuesNoSQL(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	tracedDB, trace := openTransitionTraceDB(t, databasePath, []string{"workspace_transition_receipts"})
	repo := NewWorkspaceRepo(tracedDB)
	validScope := types.WorkspaceScope{ProjectID: "00000000-0000-4000-8000-000000000001", WorkspaceID: "00000000-0000-4000-8000-000000000011"}
	validRequestID := "00000000-0000-1000-8000-000000000031"
	tests := []struct {
		name string
		call func() error
	}{
		{"lookup invalid scope", func() error {
			_, err := repo.TransitionReceiptByKey(context.Background(), types.WorkspaceScope{}, validRequestID)
			return err
		}},
		{"lookup invalid request", func() error {
			_, err := repo.TransitionReceiptByKey(context.Background(), validScope, "BAD")
			return err
		}},
		{"transaction invalid scope", func() error {
			return repo.WithImmediateWorkspaceTransition(context.Background(), types.WorkspaceScope{}, validRequestID, func(*WorkspaceMutationTx, *WorkspaceTransitionReceiptRecord) error { return nil })
		}},
		{"transaction invalid request", func() error {
			return repo.WithImmediateWorkspaceTransition(context.Background(), validScope, "BAD", func(*WorkspaceMutationTx, *WorkspaceTransitionReceiptRecord) error { return nil })
		}},
		{"transaction nil callback", func() error {
			return repo.WithImmediateWorkspaceTransition(context.Background(), validScope, validRequestID, nil)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trace.reset()
			if err := test.call(); err == nil {
				t.Fatal("invalid boundary call unexpectedly succeeded")
			}
			if calls := trace.snapshot(); len(calls) != 0 {
				t.Fatalf("invalid boundary issued SQL: %+v", calls)
			}
		})
	}
}

func TestWithImmediateWorkspaceTransitionAllowsUnregisteredScope(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	scope := types.WorkspaceScope{ProjectID: "00000000-0000-4000-8000-000000000099", WorkspaceID: "00000000-0000-4000-8000-000000000098"}
	called := false
	err := repo.WithImmediateWorkspaceTransition(context.Background(), scope, "00000000-0000-1000-8000-000000000031",
		func(tx *WorkspaceMutationTx, receipt *WorkspaceTransitionReceiptRecord) error {
			called = true
			if tx.scope != scope || receipt != nil {
				t.Fatalf("callback tx scope/receipt=(%+v,%+v)", tx.scope, receipt)
			}
			return nil
		})
	if err != nil || !called {
		t.Fatalf("unregistered transaction called=%v err=%v", called, err)
	}
}

func TestWithImmediateWorkspaceTransitionRollbackCommitFailureAndRecovery(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	receipt := validWorkspaceTransitionReceipt(t, "00000000-0000-1000-8000-000000000031", "discard", "clean")
	callbackErr := errors.New("transition callback fixture")
	err = repo.WithImmediateWorkspaceTransition(context.Background(), binding.Scope, receipt.RequestID,
		func(tx *WorkspaceMutationTx, existing *WorkspaceTransitionReceiptRecord) error {
			if existing != nil {
				t.Fatalf("rollback fixture existing=%+v", existing)
			}
			if err := tx.InsertTransitionReceipt(context.Background(), receipt); err != nil {
				return err
			}
			return callbackErr
		})
	if !errors.Is(err, callbackErr) || errors.Is(err, ErrCommitOutcomeUnknown) {
		t.Fatalf("callback error=%v", err)
	}
	if got, err := repo.TransitionReceiptByKey(context.Background(), binding.Scope, receipt.RequestID); err != nil || got != nil {
		t.Fatalf("rolled-back receipt=(%+v,%v)", got, err)
	}

	err = repo.WithImmediateWorkspaceTransition(context.Background(), binding.Scope, receipt.RequestID,
		func(tx *WorkspaceMutationTx, existing *WorkspaceTransitionReceiptRecord) error {
			if existing != nil {
				t.Fatalf("commit fixture existing=%+v", existing)
			}
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
	if got, err := repo.TransitionReceiptByKey(context.Background(), binding.Scope, receipt.RequestID); err != nil || got != nil {
		t.Fatalf("failed-commit receipt=(%+v,%v)", got, err)
	}
	if err := repo.WithImmediateWorkspaceTransition(context.Background(), binding.Scope, receipt.RequestID,
		func(tx *WorkspaceMutationTx, existing *WorkspaceTransitionReceiptRecord) error {
			if existing != nil {
				t.Fatalf("recovery fixture existing=%+v", existing)
			}
			return tx.InsertTransitionReceipt(context.Background(), receipt)
		}); err != nil {
		t.Fatalf("subsequent transaction: %v", err)
	}
	got, err := repo.TransitionReceiptByKey(context.Background(), binding.Scope, receipt.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkspaceTransitionReceipt(t, got, receipt)
}

func TestLegacyWorkspaceTransitionSeamsRemainBindingScoped(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	unregistered := types.WorkspaceScope{ProjectID: "00000000-0000-4000-8000-000000000099", WorkspaceID: "00000000-0000-4000-8000-000000000098"}
	requestID := "00000000-0000-1000-8000-000000000031"
	if got, err := repo.TransitionReceipt(context.Background(), unregistered, requestID); !errors.Is(err, ErrNotFound) || got != nil {
		t.Fatalf("legacy TransitionReceipt=(%+v,%v), want nil,ErrNotFound", got, err)
	}
	called := false
	if err := repo.WithImmediateWorkspace(context.Background(), unregistered, func(*WorkspaceMutationTx) error {
		called = true
		return nil
	}); !errors.Is(err, ErrNotFound) || called {
		t.Fatalf("legacy WithImmediateWorkspace called=%v err=%v, want false,ErrNotFound", called, err)
	}
}

func transitionBoundaryFixture(t *testing.T) (*Store, *WorkspaceRepo, types.WorkspaceBinding, WorkspaceTransitionReceiptInsert) {
	t.Helper()
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	receipt := validWorkspaceTransitionReceipt(t, "00000000-0000-1000-8000-000000000031", "discard", "clean")
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.InsertTransitionReceipt(context.Background(), receipt)
	}); err != nil {
		t.Fatal(err)
	}
	return store, repo, binding, receipt
}

type transitionSQLCall struct {
	kind  string
	query string
}

type transitionTraceDriver struct {
	inner          driver.Driver
	allowedSelects []string
	mu             sync.Mutex
	calls          []transitionSQLCall
}

func (wrapped *transitionTraceDriver) Open(name string) (driver.Conn, error) {
	connection, err := wrapped.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &transitionTraceConn{Conn: connection, trace: wrapped}, nil
}

func (wrapped *transitionTraceDriver) record(kind, query string) error {
	wrapped.mu.Lock()
	wrapped.calls = append(wrapped.calls, transitionSQLCall{kind: kind, query: query})
	wrapped.mu.Unlock()
	if kind != "query" || !strings.Contains(strings.ToUpper(query), "SELECT") {
		return nil
	}
	allowed := make(map[string]struct{}, len(wrapped.allowedSelects))
	for _, table := range wrapped.allowedSelects {
		allowed[table] = struct{}{}
	}
	tables := transitionSelectTablePattern.FindAllStringSubmatch(query, -1)
	if len(tables) == 0 {
		return fmt.Errorf("transition trace: rejected table-free SELECT: %s", query)
	}
	for _, match := range tables {
		if _, ok := allowed[strings.ToLower(match[1])]; !ok {
			return fmt.Errorf("transition trace: rejected unexpected table %q in SELECT: %s", match[1], query)
		}
	}
	return nil
}

func (wrapped *transitionTraceDriver) reset() {
	wrapped.mu.Lock()
	defer wrapped.mu.Unlock()
	wrapped.calls = nil
}

func (wrapped *transitionTraceDriver) snapshot() []transitionSQLCall {
	wrapped.mu.Lock()
	defer wrapped.mu.Unlock()
	return append([]transitionSQLCall(nil), wrapped.calls...)
}

type transitionTraceConn struct {
	driver.Conn
	trace *transitionTraceDriver
}

func (connection *transitionTraceConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := connection.trace.record("exec", query); err != nil {
		return nil, err
	}
	return connection.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (connection *transitionTraceConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := connection.trace.record("query", query); err != nil {
		return nil, err
	}
	return connection.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (connection *transitionTraceConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	return connection.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
}

func (connection *transitionTraceConn) Ping(ctx context.Context) error {
	return connection.Conn.(driver.Pinger).Ping(ctx)
}

var transitionTraceSequence atomic.Uint64

var transitionSelectTablePattern = regexp.MustCompile(`(?i)\b(?:FROM|JOIN)\s+([a-z_][a-z0-9_]*)`)

func openTransitionTraceDB(t *testing.T, path string, allowedSelects []string) (*sql.DB, *transitionTraceDriver) {
	t.Helper()
	trace := &transitionTraceDriver{inner: &sqlite.Driver{}, allowedSelects: append([]string(nil), allowedSelects...)}
	driverName := fmt.Sprintf("localstore-transition-trace-%d", transitionTraceSequence.Add(1))
	sql.Register(driverName, trace)
	db, err := sql.Open(driverName, sqliteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	trace.reset()
	t.Cleanup(func() { _ = db.Close() })
	return db, trace
}

func assertTransitionSelectTables(t *testing.T, trace *transitionTraceDriver, tables []string) {
	t.Helper()
	calls := trace.snapshot()
	selects := make([]transitionSQLCall, 0)
	for _, call := range calls {
		if call.kind == "query" && strings.Contains(strings.ToUpper(call.query), "SELECT") {
			selects = append(selects, call)
		}
	}
	if len(selects) != len(tables) {
		t.Fatalf("SELECT trace=%+v, want tables=%v; all calls=%+v", selects, tables, calls)
	}
	for index, table := range tables {
		if !strings.Contains(selects[index].query, table) {
			t.Fatalf("SELECT %d=%q, want table %q", index, selects[index].query, table)
		}
		if index == 0 && (table != "workspace_transition_receipts" || strings.Contains(selects[index].query, "workspace_bindings")) {
			t.Fatalf("first SELECT is not receipt-table-only: %q", selects[index].query)
		}
		if index == 0 {
			assertTransitionReceiptQueryShape(t, selects[index].query)
		}
	}
}

func assertTransitionReceiptQueryShape(t *testing.T, query string) {
	t.Helper()
	if !strings.Contains(query, "WHERE project_id=? AND workspace_id=? AND request_id=?") {
		t.Fatalf("receipt query lacks exact scoped key predicate: %q", query)
	}
}

var (
	_ driver.Driver         = (*transitionTraceDriver)(nil)
	_ driver.ExecerContext  = (*transitionTraceConn)(nil)
	_ driver.QueryerContext = (*transitionTraceConn)(nil)
	_ driver.ConnBeginTx    = (*transitionTraceConn)(nil)
	_ driver.Pinger         = (*transitionTraceConn)(nil)
)
