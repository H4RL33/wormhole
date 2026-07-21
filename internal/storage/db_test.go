package storage

import (
	"context"
	"os"
	"testing"

	"github.com/H4RL33/wormhole/internal/types"
)

// TestOpen_ValidDSN_Pingable opens against the configured Postgres and
// verifies the returned *sql.DB is usable (Ping succeeds). It skips when
// Postgres is unreachable, matching the integration skip-guard used in
// internal/core/roles/roles_test.go — these assert real storage behavior,
// not mocks.
func TestOpen_ValidDSN_Pingable(t *testing.T) {
	cfg := types.LoadConfig()
	db, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if db == nil {
		t.Fatal("Open returned nil db with nil error")
	}
	t.Cleanup(func() { db.Close() })

	if err := db.PingContext(context.Background()); err != nil {
		if os.Getenv("WORMHOLE_INTEGRATION_REQUIRED") == "1" {
			t.Fatalf("postgres required but not reachable: %v", err)
		}
		t.Skipf("postgres not reachable (%v) — run `docker compose up -d db` and apply migrations before running this test", err)
	}
}

// TestOpen_MalformedDSN_DefersError documents the database/sql contract that
// Open relies on: sql.Open validates only that the driver is registered, it
// does not dial or parse the DSN. A malformed DSN therefore yields a non-nil
// *sql.DB and a nil error here; the parse error surfaces on first use (Ping).
// The guarantee Open must uphold is no nil-deref: a non-nil handle on which
// the caller can observe the connection error.
func TestOpen_MalformedDSN_DefersError(t *testing.T) {
	db, err := Open(types.Config{DatabaseURL: `not-a-valid-dsn-::::`})
	if err != nil {
		t.Fatalf("Open on malformed DSN: got err %v, want nil (sql.Open is lazy)", err)
	}
	if db == nil {
		t.Fatal("Open returned nil db; caller cannot observe the deferred error")
	}
	t.Cleanup(func() { db.Close() })

	if err := db.PingContext(context.Background()); err == nil {
		t.Fatal("Ping on malformed DSN: got nil error, want a parse/connection error")
	}
}
