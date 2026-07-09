package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
		t.Skipf("postgres not reachable (%v); run `docker compose up -d db` and apply migrations before running this test", err)
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
	srv := httptest.NewServer(NewMCPHandler(registry, store))
	defer srv.Close()

	status, rpcResp := toolsCallRPC(t, srv, "", "wormhole.agent.nonexistent", "00000000-0000-0000-0000-000000000000", json.RawMessage(`{}`))
	if status != http.StatusOK {
		t.Fatalf("status: got %d, want %d", status, http.StatusOK)
	}
	if rpcResp.Error == nil || rpcResp.Error.Code != RPCInvalidParams {
		t.Fatalf("rpcResp.Error: got %+v, want Code %d", rpcResp.Error, RPCInvalidParams)
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
	srv := httptest.NewServer(NewMCPHandler(registry, store))
	defer srv.Close()

	status, rpcResp := toolsCallRPC(t, srv, "", "wormhole.agent.whoami", "00000000-0000-0000-0000-000000000000", json.RawMessage(`{}`))
	if status != http.StatusOK {
		t.Fatalf("status: got %d, want %d", status, http.StatusOK)
	}
	if rpcResp.Error == nil || rpcResp.Error.Code != RPCInvalidParams {
		t.Fatalf("rpcResp.Error: got %+v, want Code %d", rpcResp.Error, RPCInvalidParams)
	}
	if !strings.Contains(rpcResp.Error.Message, "missing bearer token") {
		t.Fatalf("rpcResp.Error.Message: got %q, want it to contain %q", rpcResp.Error.Message, "missing bearer token")
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
	srv := httptest.NewServer(NewMCPHandler(registry, store))
	defer srv.Close()

	status, rpcResp := toolsCallRPC(t, srv, "not-a-real-token", "wormhole.agent.whoami", "00000000-0000-0000-0000-000000000000", json.RawMessage(`{}`))
	if status != http.StatusOK {
		t.Fatalf("status: got %d, want %d", status, http.StatusOK)
	}
	if rpcResp.Error == nil || rpcResp.Error.Code != -32001 {
		t.Fatalf("rpcResp.Error: got %+v, want Code %d", rpcResp.Error, -32001)
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
	srv := httptest.NewServer(NewMCPHandler(registry, store))
	defer srv.Close()

	mustToolResult(t, srv, "", "wormhole.agent.register", "proj-1", json.RawMessage(`{}`))
	if !called {
		t.Fatalf("handler was not called")
	}
}

func TestToolsDiscoveryEndpoint(t *testing.T) {
	registry := NewRegistry()
	registry.Register(Tool{
		Name:         "test.dummy.tool",
		Description:  "A dummy tool for testing",
		RequiresAuth: true,
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			return nil, nil
		},
	})

	// 1. Test json.Marshal directly
	tools := registry.List()
	body, err := json.Marshal(tools)
	if err != nil {
		t.Fatalf("failed to marshal tools: %v", err)
	}

	var rawList []map[string]any
	if err := json.Unmarshal(body, &rawList); err != nil {
		t.Fatalf("failed to unmarshal marshaled tools: %v", err)
	}

	if len(rawList) != 1 {
		t.Fatalf("got %d tools, want 1", len(rawList))
	}

	toolMap := rawList[0]
	requiredKeys := []string{"name", "description", "requires_auth"}
	for _, key := range requiredKeys {
		if _, ok := toolMap[key]; !ok {
			t.Errorf("missing key %q in serialized tool map", key)
		}
	}

	forbiddenKeys := []string{"handler", "Handler"}
	for _, key := range forbiddenKeys {
		if _, ok := toolMap[key]; ok {
			t.Errorf("forbidden key %q found in serialized tool map", key)
		}
	}
}
