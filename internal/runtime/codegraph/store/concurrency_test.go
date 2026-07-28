package store_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/codegraph/store"
	sqlite "modernc.org/sqlite"
)

var wrappedDriverSequence atomic.Uint64

func TestCandidateInsertCannotCommitAfterConcurrentPublication(t *testing.T) {
	insertBarrier := &driverCallBarrier{
		queryContains: "INSERT INTO codegraph_nodes",
		argument:      "late-node",
		entered:       make(chan struct{}, 1),
		release:       make(chan struct{}),
	}
	db := openWrappedSQLite(t, filepath.Join(t.TempDir(), "gateway.db"), &barrierDriver{
		inner:       &sqlite.Driver{},
		execBarrier: insertBarrier,
	})
	ctx := context.Background()
	s1, err := store.Open(ctx, db, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	s2, err := store.Open(ctx, db, "project-a")
	if err != nil {
		t.Fatal(err)
	}

	createSingleNodeCandidate(t, s1, "revision-old", "old-repository")
	if err := s1.PublishCandidate(ctx, "revision-old", requireOneNode); err != nil {
		t.Fatal(err)
	}
	createSingleNodeCandidate(t, s1, "revision-new", "new-repository")

	addDone := make(chan error, 1)
	go func() {
		addDone <- s2.PutNode(ctx, store.Node{
			ProjectID: "project-a", RevisionID: "revision-new", ID: "late-node",
			Kind: store.NodeSymbol, Name: "late-node",
		})
	}()
	waitForBarrier(t, insertBarrier.entered)
	if err := s1.PublishCandidate(ctx, "revision-new", requireOneNode); err != nil {
		close(insertBarrier.release)
		t.Fatalf("PublishCandidate() error = %v", err)
	}
	close(insertBarrier.release)
	if err := <-addDone; !errors.Is(err, store.ErrNotCandidate) {
		t.Fatalf("late PutNode() error = %v, want ErrNotCandidate", err)
	}

	if err := s1.ReadActive(ctx, func(snapshot *store.Snapshot) error {
		nodes, err := snapshot.Nodes(ctx)
		if err != nil {
			return err
		}
		if len(nodes) != 1 || nodes[0].ID != "new-repository" {
			t.Fatalf("active nodes = %#v, late unvalidated insert became visible", nodes)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentOpenSerializesFreshMigration(t *testing.T) {
	const workers = 8
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	path := filepath.Join(t.TempDir(), "gateway.db")
	dsn := migrationSQLiteDSN(path)
	bootstrapDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	var journalMode string
	if err := bootstrapDB.QueryRowContext(ctx, `PRAGMA journal_mode=WAL`).Scan(&journalMode); err != nil {
		_ = bootstrapDB.Close()
		t.Fatalf("preinitialize WAL: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		_ = bootstrapDB.Close()
		t.Fatalf("preinitialized journal mode = %q, want WAL", journalMode)
	}
	var existingSchemaTables int
	if err := bootstrapDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name LIKE 'codegraph_%'`).Scan(&existingSchemaTables); err != nil {
		_ = bootstrapDB.Close()
		t.Fatalf("inspect preinitialized schema: %v", err)
	}
	if existingSchemaTables != 0 {
		_ = bootstrapDB.Close()
		t.Fatalf("preinitialized codegraph tables = %d, want 0", existingSchemaTables)
	}
	if err := bootstrapDB.Close(); err != nil {
		t.Fatalf("close WAL bootstrap database: %v", err)
	}

	migrationBarrier := &driverCallBarrier{
		queryContains: "BEGIN IMMEDIATE",
		entered:       make(chan struct{}, 1),
		release:       make(chan struct{}),
	}
	followerBarrier := &driverCallBarrier{
		queryContains: "BEGIN IMMEDIATE",
		entered:       make(chan struct{}, workers-1),
		release:       make(chan struct{}),
	}
	leaderDriverName := registerBarrierDriver(&barrierDriver{
		inner:            &sqlite.Driver{},
		afterExecBarrier: migrationBarrier,
	})
	followerDriverName := registerBarrierDriver(&barrierDriver{
		inner:       &sqlite.Driver{},
		execBarrier: followerBarrier,
	})
	type workerResult struct {
		worker int
		err    error
	}
	results := make(chan workerResult, workers)
	var group sync.WaitGroup
	startWorker := func(worker int, driverName string) {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			db, err := sql.Open(driverName, dsn)
			if err == nil {
				db.SetMaxOpenConns(2)
				_, err = store.Open(ctx, db, fmt.Sprintf("project-%d", worker))
			}
			if db != nil {
				_ = db.Close()
			}
			results <- workerResult{worker: worker, err: err}
		}(worker)
	}
	var releaseLeaderOnce, releaseFollowersOnce sync.Once
	releaseLeader := func() { releaseLeaderOnce.Do(func() { close(migrationBarrier.release) }) }
	releaseFollowers := func() { releaseFollowersOnce.Do(func() { close(followerBarrier.release) }) }
	runErr := func() (runErr error) {
		defer func() {
			releaseFollowers()
			releaseLeader()
			if runErr != nil {
				cancel()
			}
			group.Wait()
		}()

		startWorker(0, leaderDriverName)
		select {
		case <-migrationBarrier.entered:
		case result := <-results:
			return fmt.Errorf("leader completed before acquiring migration lock: worker=%d err=%v", result.worker, result.err)
		case <-ctx.Done():
			return fmt.Errorf("wait for leader migration lock: %w", ctx.Err())
		}
		for worker := 1; worker < workers; worker++ {
			startWorker(worker, followerDriverName)
		}
		for parked := 0; parked < workers-1; parked++ {
			select {
			case <-followerBarrier.entered:
			case result := <-results:
				return fmt.Errorf("follower completed before reaching migration gate: worker=%d err=%v", result.worker, result.err)
			case <-ctx.Done():
				return fmt.Errorf("wait for followers at migration gate: %w", ctx.Err())
			}
		}
		releaseFollowers()
		serializationWindow := time.NewTimer(100 * time.Millisecond)
		defer serializationWindow.Stop()
		select {
		case result := <-results:
			return fmt.Errorf("worker completed while leader held migration lock: worker=%d err=%v", result.worker, result.err)
		case <-serializationWindow.C:
		case <-ctx.Done():
			return fmt.Errorf("prove migration serialization: %w", ctx.Err())
		}

		releaseLeader()
		for completed := 0; completed < workers; completed++ {
			select {
			case result := <-results:
				if result.err != nil {
					return fmt.Errorf("worker %d Open(): %w", result.worker, result.err)
				}
			case <-ctx.Done():
				return fmt.Errorf("collect migration workers: %w", ctx.Err())
			}
		}
		return nil
	}()
	if runErr != nil {
		t.Fatal(runErr)
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, version := range []int{1, 2} {
		var versionRows int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM codegraph_schema_migrations WHERE version = ?", version).Scan(&versionRows); err != nil {
			t.Fatal(err)
		}
		if versionRows != 1 {
			t.Fatalf("version %d ledger rows = %d, want 1", version, versionRows)
		}
	}
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(codegraph_lifecycle)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var lifecycleColumns []string
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		lifecycleColumns = append(lifecycleColumns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	wantLifecycleColumns := "project_id,state,build_token,owner_pid,owner_start,build_owner_pid,build_owner_start"
	if got := strings.Join(lifecycleColumns, ","); got != wantLifecycleColumns {
		t.Fatalf("codegraph_lifecycle columns = %q, want %q", got, wantLifecycleColumns)
	}
}

func createSingleNodeCandidate(t *testing.T, graphStore *store.Store, revisionID, nodeID string) {
	t.Helper()
	ctx := context.Background()
	if err := graphStore.CreateCandidate(ctx, store.Revision{
		ProjectID: "project-a", ID: revisionID,
		IndexedCommit: strings.Repeat("a", 40), CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := graphStore.PutNode(ctx, store.Node{
		ProjectID: "project-a", RevisionID: revisionID, ID: nodeID,
		Kind: store.NodeRepository, Name: nodeID,
	}); err != nil {
		t.Fatal(err)
	}
}

func requireOneNode(ctx context.Context, snapshot *store.Snapshot) error {
	nodes, err := snapshot.Nodes(ctx)
	if err != nil {
		return err
	}
	if len(nodes) != 1 {
		return fmt.Errorf("node count = %d, want 1", len(nodes))
	}
	return nil
}

type barrierDriver struct {
	inner            driver.Driver
	execBarrier      *driverCallBarrier
	afterExecBarrier *driverCallBarrier
}

func (wrapped *barrierDriver) Open(name string) (driver.Conn, error) {
	connection, err := wrapped.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &barrierConn{
		Conn:             connection,
		execBarrier:      wrapped.execBarrier,
		afterExecBarrier: wrapped.afterExecBarrier,
	}, nil
}

type barrierConn struct {
	driver.Conn
	execBarrier      *driverCallBarrier
	afterExecBarrier *driverCallBarrier
}

func (connection *barrierConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if connection.execBarrier.matches(query, args) {
		connection.execBarrier.wait()
	}
	afterExecMatches := connection.afterExecBarrier.matches(query, args)
	result, err := connection.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
	if err == nil && afterExecMatches {
		connection.afterExecBarrier.waitOnce()
	}
	return result, err
}

func (connection *barrierConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return connection.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (connection *barrierConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	return connection.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
}

func (connection *barrierConn) Ping(ctx context.Context) error {
	return connection.Conn.(driver.Pinger).Ping(ctx)
}

type driverCallBarrier struct {
	queryContains string
	argument      string
	entered       chan struct{}
	release       chan struct{}
	once          sync.Once
}

func (barrier *driverCallBarrier) matches(query string, args []driver.NamedValue) bool {
	if barrier == nil || !strings.Contains(query, barrier.queryContains) {
		return false
	}
	if barrier.argument == "" {
		return true
	}
	for _, argument := range args {
		if value, ok := argument.Value.(string); ok && value == barrier.argument {
			return true
		}
	}
	return false
}

func (barrier *driverCallBarrier) wait() {
	barrier.entered <- struct{}{}
	<-barrier.release
}

func (barrier *driverCallBarrier) waitOnce() {
	barrier.once.Do(barrier.wait)
}

func waitForBarrier(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for driver barrier")
	}
}

func openWrappedSQLite(t *testing.T, path string, wrapped *barrierDriver) *sql.DB {
	t.Helper()
	db, err := sql.Open(registerBarrierDriver(wrapped), wrappedSQLiteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(8)
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func registerBarrierDriver(wrapped *barrierDriver) string {
	name := fmt.Sprintf("codegraph-barrier-%d", wrappedDriverSequence.Add(1))
	sql.Register(name, wrapped)
	return name
}

func wrappedSQLiteDSN(path string) string {
	u := &url.URL{Scheme: "file", Path: path, OmitHost: true}
	query := u.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "journal_mode(WAL)")
	u.RawQuery = query.Encode()
	return u.String()
}

func migrationSQLiteDSN(path string) string {
	u := &url.URL{Scheme: "file", Path: path, OmitHost: true}
	query := u.Query()
	query.Add("_pragma", "busy_timeout(15000)")
	u.RawQuery = query.Encode()
	return u.String()
}

var _ driver.Driver = (*barrierDriver)(nil)
var _ driver.ExecerContext = (*barrierConn)(nil)
var _ driver.QueryerContext = (*barrierConn)(nil)
var _ driver.ConnBeginTx = (*barrierConn)(nil)
var _ driver.Pinger = (*barrierConn)(nil)
