package localstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenConfiguresSQLitePragmasOnEachConnection(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "pragmas.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	store.DB().SetMaxOpenConns(2)
	first, err := store.DB().Conn(ctx)
	if err != nil {
		t.Fatalf("first connection: %v", err)
	}
	defer first.Close()
	second, err := store.DB().Conn(ctx)
	if err != nil {
		t.Fatalf("second connection: %v", err)
	}
	defer second.Close()

	for index, conn := range []*sql.Conn{first, second} {
		var journalMode string
		if err := conn.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
			t.Fatalf("connection %d journal mode: %v", index, err)
		}
		if journalMode != "wal" {
			t.Fatalf("connection %d journal mode = %q, want wal", index, journalMode)
		}
		var busyTimeout int
		if err := conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
			t.Fatalf("connection %d busy timeout: %v", index, err)
		}
		if busyTimeout != 5000 {
			t.Fatalf("connection %d busy timeout = %d, want 5000", index, busyTimeout)
		}
	}
}

func TestOpenWaitsForConcurrentSQLiteWriter(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "concurrent.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	tx, err := store.DB().Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		INSERT INTO whoami_cache
			(agent_id, owner, model, capabilities, project_id, permissions, cached_at)
		VALUES (?, ?, ?, '[]', ?, '[]', ?)
	`, "lock-holder", "owner", "model", "project", time.Now().UTC()); err != nil {
		t.Fatalf("hold write lock: %v", err)
	}

	startWriter := make(chan struct{})
	writerStarted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		<-startWriter
		close(writerStarted)
		done <- store.CacheWhoAmI(context.Background(), WhoAmICache{
			AgentID:   "waiter",
			Owner:     "owner",
			Model:     "model",
			ProjectID: "project",
			CachedAt:  time.Now().UTC(),
		})
	}()
	close(startWriter)
	<-writerStarted

	select {
	case err := <-done:
		t.Fatalf("concurrent write returned before lock release: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit lock holder: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("concurrent write after lock release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent write did not resume after lock release")
	}
}
