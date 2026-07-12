package main

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	_ "github.com/lib/pq"

	"github.com/H4RL33/wormhole/internal/core/events"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/core/kb"
	"github.com/H4RL33/wormhole/internal/core/tasks"
	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/webui"
)

// testDB opens an independent Postgres connection, skipping the test if
// Postgres is not reachable — mirrors internal/mcp/server_test.go's and
// internal/webui/api_test.go's testDB pattern.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	cfg := types.LoadConfig()
	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		if os.Getenv("WORMHOLE_INTEGRATION_REQUIRED") == "1" {
			t.Fatalf("postgres required but not reachable: %v", err)
		}
		t.Skipf("postgres not reachable (%v); run `docker compose up -d db` and apply migrations before running this test", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestDashboardMount_ServesStaticPageAndPreservesAuth exercises the exact
// mux wiring main() constructs (mux.Handle("/dashboard/", webuiHandler.NewMux()))
// against a real HTTP server, proving both that GET /dashboard/ serves
// Task 1's static page and that the mount did not bypass Chapter 9's
// withViewerAuth on the API routes nested under the same prefix.
func TestDashboardMount_ServesStaticPageAndPreservesAuth(t *testing.T) {
	db := testDB(t)
	identityStore := identity.NewStore(db)
	eventsStore := events.NewStore(db)
	tasksStore := tasks.NewStore(db, eventsStore)
	kbStore := kb.NewStore(db, kb.StubEmbedder{}, 0.85, 4000, 1, 1, 1)

	webuiHandler := &webui.Handler{
		Identity: identityStore,
		Tasks:    tasksStore,
		Events:   eventsStore,
		KB:       kbStore,
	}

	mux := http.NewServeMux()
	mux.Handle("/dashboard/", webuiHandler.NewMux())

	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Run("static page served", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/dashboard/")
		if err != nil {
			t.Fatalf("GET /dashboard/: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: got %d, want 200", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Fatalf("content-type: got %q, want text/html prefix", ct)
		}
	})

	t.Run("api route under same prefix still requires auth", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/dashboard/api/projects/00000000-0000-0000-0000-000000000000/tasks")
		if err != nil {
			t.Fatalf("GET tasks (no auth): %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status: got %d, want 403 (mount must not bypass withViewerAuth)", resp.StatusCode)
		}
	})
}
