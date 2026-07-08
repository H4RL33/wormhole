package mcp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	_ "github.com/lib/pq"

	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/types"
)

// testIdentityStore mirrors identity.testStore's pattern: real Postgres,
// skip if unreachable, since the auth middleware's guarantees depend on
// real WhoAmI/expiry behavior, not a mock.
func testIdentityStore(t *testing.T) *identity.Store {
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
		t.Skipf("postgres not reachable (%v) — run `docker compose up -d db` and apply migrations before running this test", err)
	}
	t.Cleanup(func() { db.Close() })
	return identity.NewStore(db)
}

// testDB opens an independent Postgres connection for tests that need raw
// SQL access to set up fixtures or backdate rows outside identity.Store's
// own API surface (mirrors identity_test.go's testStore(t) pattern).
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	cfg := types.LoadConfig()
	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestCallHandler_UnknownTool(t *testing.T) {
	registry := NewRegistry()
	store := testIdentityStore(t)
	handler := NewCallHandler(registry, store)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	body, _ := json.Marshal(CallRequest{Tool: "wormhole.agent.nonexistent", ProjectID: "00000000-0000-0000-0000-000000000000", Arguments: json.RawMessage(`{}`)})
	resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: got %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestCallHandler_RequiresAuthMissingToken(t *testing.T) {
	registry := NewRegistry()
	registry.Register(Tool{
		Name:         "wormhole.agent.whoami",
		RequiresAuth: true,
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			return "should not reach here", nil
		},
	})
	store := testIdentityStore(t)
	handler := NewCallHandler(registry, store)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	body, _ := json.Marshal(CallRequest{Tool: "wormhole.agent.whoami", ProjectID: "00000000-0000-0000-0000-000000000000", Arguments: json.RawMessage(`{}`)})
	resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestCallHandler_RequiresAuthInvalidToken(t *testing.T) {
	registry := NewRegistry()
	registry.Register(Tool{
		Name:         "wormhole.agent.whoami",
		RequiresAuth: true,
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			return "should not reach here", nil
		},
	})
	store := testIdentityStore(t)
	handler := NewCallHandler(registry, store)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	body, _ := json.Marshal(CallRequest{Tool: "wormhole.agent.whoami", ProjectID: "00000000-0000-0000-0000-000000000000", Arguments: json.RawMessage(`{}`)})
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestCallHandler_NoAuthRequiredDispatchesDirectly(t *testing.T) {
	registry := NewRegistry()
	called := false
	registry.Register(Tool{
		Name:         "wormhole.agent.register",
		RequiresAuth: false,
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			called = true
			if scope != nil {
				t.Fatalf("scope: got non-nil, want nil for RequiresAuth=false tool")
			}
			if projectID != "proj-1" {
				t.Fatalf("projectID: got %q, want %q", projectID, "proj-1")
			}
			return map[string]string{"ok": "yes"}, nil
		},
	})
	store := testIdentityStore(t)
	handler := NewCallHandler(registry, store)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	body, _ := json.Marshal(CallRequest{Tool: "wormhole.agent.register", ProjectID: "proj-1", Arguments: json.RawMessage(`{}`)})
	resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if !called {
		t.Fatalf("handler was not called")
	}
}
