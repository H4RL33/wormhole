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
	migrationBarrier := &driverCallBarrier{
		queryContains: "BEGIN IMMEDIATE",
		entered:       make(chan struct{}, workers),
		release:       make(chan struct{}),
	}
	driverName := registerBarrierDriver(&barrierDriver{
		inner:       &sqlite.Driver{},
		execBarrier: migrationBarrier,
	})
	dsn := wrappedSQLiteDSN(filepath.Join(t.TempDir(), "gateway.db"))
	start := make(chan struct{})
	errorsByWorker := make(chan error, workers)
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			<-start
			db, err := sql.Open(driverName, dsn)
			if err == nil {
				db.SetMaxOpenConns(2)
				_, err = store.Open(context.Background(), db, fmt.Sprintf("project-%d", worker))
			}
			if db != nil {
				_ = db.Close()
			}
			errorsByWorker <- err
		}(worker)
	}
	close(start)
	for range workers {
		waitForBarrier(t, migrationBarrier.entered)
	}
	close(migrationBarrier.release)
	group.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		if err != nil {
			t.Errorf("concurrent Open() error = %v", err)
		}
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var versionRows int
	if err := db.QueryRow("SELECT COUNT(*) FROM codegraph_schema_migrations WHERE version = 1").Scan(&versionRows); err != nil {
		t.Fatal(err)
	}
	if versionRows != 1 {
		t.Fatalf("version 1 ledger rows = %d, want 1", versionRows)
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
	inner       driver.Driver
	execBarrier *driverCallBarrier
}

func (wrapped *barrierDriver) Open(name string) (driver.Conn, error) {
	connection, err := wrapped.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &barrierConn{Conn: connection, execBarrier: wrapped.execBarrier}, nil
}

type barrierConn struct {
	driver.Conn
	execBarrier *driverCallBarrier
}

func (connection *barrierConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if connection.execBarrier.matches(query, args) {
		connection.execBarrier.wait()
	}
	return connection.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
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

var _ driver.Driver = (*barrierDriver)(nil)
var _ driver.ExecerContext = (*barrierConn)(nil)
var _ driver.QueryerContext = (*barrierConn)(nil)
var _ driver.ConnBeginTx = (*barrierConn)(nil)
var _ driver.Pinger = (*barrierConn)(nil)
