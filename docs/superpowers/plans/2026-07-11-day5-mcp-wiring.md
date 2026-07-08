# Day 5: MCP Tool Wiring Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire `wormhole.agent.register` and `wormhole.agent.whoami` onto a real MCP tool-call HTTP endpoint, add an auth middleware that resolves bearer tokens at the MCP boundary and rejects invalid/unscoped/expired ones, and prove the full loop end-to-end (RFC-0001 §8.5 joining flow, first two steps).

**Architecture:** `internal/mcp` gains a `Handler` function type and `RequiresAuth` flag on `Tool` (Task 2), a single `/mcp/tools/call` HTTP endpoint that decodes a `{tool, project_id, arguments}` envelope and dispatches to the registered tool (Task 3), and two tool implementations backed by `internal/core/identity.Store` (Task 4). `cmd/wormhole-server` wires DB + registry + endpoint (Task 4). Auth happens once, in the endpoint's middleware layer, per architecture.md M4 — core packages never see a raw token.

**Tech Stack:** Go stdlib `net/http`, `encoding/json`, existing `internal/core/identity` package, Postgres via `internal/storage`.

## Global Constraints

- Follow `internal/core/identity`'s layering pattern for any new Store code (architecture.md §3): sentinel errors as package vars, wrapped errors `fmt.Errorf("<pkg>: <op>: %w", err)`, transactions for multi-statement writes, security-relevant lookups collapse to one error.
- M2 naming grammar: `wormhole.<pillar-noun>.<verb>` — tool names are exactly `wormhole.agent.register` and `wormhole.agent.whoami`, already fixed by the roadmap.
- M4: auth happens at the MCP boundary; `internal/mcp` middleware resolves the bearer token via `identity.Store.WhoAmI`, yielding `AuthenticatedScope`. Core packages (here, the tool handlers) receive the already-resolved scope, never a raw token.
- R1: `internal/core/*` never imports `internal/mcp`. `internal/mcp` imports `internal/core/identity`.
- D1: schema changes only via golang-migrate pairs, zero-padded sequential, in `migrations/`. Down migration must actually revert.
- D5: `agent_tokens` gains a column, not a new append-only violation — this is a schema addition to an existing mutable table (agent_tokens is not append-only; only `audit_log` is), so no D5 conflict.
- **Inference flagged (CLAUDE.md §3.2):** neither RFC specifies a token TTL. This plan uses a 30-day fixed expiry (`const tokenTTL = 30 * 24 * time.Hour`) as a reasonable default for the alpha, chosen by explicit user decision during Day 5 planning (not a documented RFC value). Revisit if a real requirement emerges.
- Expired tokens collapse into the existing `identity.ErrInvalidToken` sentinel, per architecture.md layering rule 4 ("security-relevant lookups collapse to one error") — callers must not be able to tell "expired" from "forged" from "unknown".
- Tests follow the existing `testStore(t)` pattern in `internal/core/identity/identity_test.go`: real Postgres connection, `t.Skipf` if unreachable (`t.Fatalf` if `WORMHOLE_INTEGRATION_REQUIRED=1`), `t.Cleanup` for teardown. Run migrations against `127.0.0.1:5432` before running any task's tests: `docker-compose up -d db && /home/harley/go/bin/migrate -path migrations -database "postgres://wormhole:wormhole@127.0.0.1:5432/wormhole?sslmode=disable" up` (adjust path to `migrate` binary if different on the implementer's machine — `go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest` if missing).

---

### Task 1: Token expiry

**Files:**
- Create: `migrations/000005_agent_tokens_expiry.up.sql`
- Create: `migrations/000005_agent_tokens_expiry.down.sql`
- Modify: `internal/core/identity/identity.go` (add `tokenTTL` const, set `expires_at` on both token-issuing INSERTs, filter `WhoAmI`'s SELECT on non-expired)
- Test: `internal/core/identity/identity_test.go` (add `TestWhoAmI_ExpiredTokenRejected`)

**Interfaces:**
- Consumes: existing `Store.Register`, `Store.IssueToken`, `Store.WhoAmI` signatures — none of them change.
- Produces: no new exported symbols. `agent_tokens.expires_at timestamptz NOT NULL` column exists after migration 000005; expired rows are excluded by `WhoAmI` and collapse to `identity.ErrInvalidToken`.

- [ ] **Step 1: Write the migration**

`migrations/000005_agent_tokens_expiry.up.sql`:
```sql
-- RFC-0001/RFC-0002 do not specify a token TTL; 30 days is an inferred
-- alpha default (docs/superpowers/plans/2026-07-11-day5-mcp-wiring.md),
-- not an RFC value. Existing rows get an expiry 30 days from now so the
-- backfill doesn't retroactively invalidate live tokens.
ALTER TABLE agent_tokens ADD COLUMN expires_at timestamptz NOT NULL DEFAULT (now() + interval '30 days');
ALTER TABLE agent_tokens ALTER COLUMN expires_at DROP DEFAULT;
```

`migrations/000005_agent_tokens_expiry.down.sql`:
```sql
ALTER TABLE agent_tokens DROP COLUMN expires_at;
```

- [ ] **Step 2: Apply the migration and verify round-trip**

Run:
```bash
/home/harley/go/bin/migrate -path migrations -database "postgres://wormhole:wormhole@127.0.0.1:5432/wormhole?sslmode=disable" up
/home/harley/go/bin/migrate -path migrations -database "postgres://wormhole:wormhole@127.0.0.1:5432/wormhole?sslmode=disable" down 1
/home/harley/go/bin/migrate -path migrations -database "postgres://wormhole:wormhole@127.0.0.1:5432/wormhole?sslmode=disable" up
```
Expected: all three commands succeed with no error output beyond the `N/u`/`N/d` progress lines.

- [ ] **Step 3: Write the failing test**

Add to `internal/core/identity/identity_test.go`:
```go
// TestWhoAmI_ExpiredTokenRejected covers the expiry half of RFC-0001 §13's
// unforgeable-identity guarantee: a token past its expires_at must be
// rejected the same way a forged one is (ErrInvalidToken, no distinct
// error), never resolved to a live scope.
func TestWhoAmI_ExpiredTokenRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "expired-token")

	agent, _, token, err := s.Register(ctx, projectID, []string{"event.publish"}, "harley", "claude", nil, nil, nil)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	cleanupAgent(t, s, agent.ID)

	if _, err := s.db.ExecContext(ctx,
		`UPDATE agent_tokens SET expires_at = now() - interval '1 hour' WHERE agent_id = $1`,
		agent.ID,
	); err != nil {
		t.Fatalf("backdate token expiry: %v", err)
	}

	if _, err := s.WhoAmI(ctx, projectID, token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("WhoAmI with expired token: got err %v, want ErrInvalidToken", err)
	}
}
```

- [ ] **Step 4: Run test to verify it fails**

Run: `go test ./internal/core/identity/ -run TestWhoAmI_ExpiredTokenRejected -v`
Expected: FAIL — `WhoAmI` currently ignores `expires_at`, so the expired token still resolves (no error), failing the `errors.Is` check.

- [ ] **Step 5: Implement expiry — set on issuance**

In `internal/core/identity/identity.go`, add near the top (after the sentinel error vars, before `type Agent struct`):
```go
// tokenTTL is an inferred alpha default — neither RFC-0001 nor RFC-0002
// specifies a token lifetime. See Global Constraints in
// docs/superpowers/plans/2026-07-11-day5-mcp-wiring.md.
const tokenTTL = 30 * 24 * time.Hour
```

In `Register`, change the token INSERT (around line 142-147):
```go
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO agent_tokens (agent_id, project_id, permissions, token_hash, expires_at) VALUES ($1, $2, $3, $4, $5)`,
		agent.ID, projectID, permissionsJSON, tokenHash, time.Now().Add(tokenTTL),
	); err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: insert token: %w", err)
	}
```

In `IssueToken`, change the token INSERT (around line 304-309):
```go
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO agent_tokens (agent_id, project_id, permissions, token_hash, expires_at) VALUES ($1, $2, $3, $4, $5)`,
		agentID, projectID, permissionsJSON, tokenHash, time.Now().Add(tokenTTL),
	); err != nil {
		return "", fmt.Errorf("identity: insert token: %w", err)
	}
```

- [ ] **Step 6: Implement expiry — filter on lookup**

In `WhoAmI`, change the SELECT (around line 335-341):
```go
	err := s.db.QueryRowContext(ctx,
		`SELECT a.id, a.owner, a.model, a.capabilities, a.created_at, t.permissions
		 FROM agents a
		 JOIN agent_tokens t ON t.agent_id = a.id
		 WHERE t.token_hash = $1 AND t.project_id = $2 AND t.expires_at > now()`,
		hash, projectID,
	).Scan(&agent.ID, &agent.Owner, &agent.Model, &capsRaw, &agent.CreatedAt, &permissionsRaw)
```

- [ ] **Step 7: Run test to verify it passes**

Run: `go test ./internal/core/identity/ -v`
Expected: PASS for all tests including the new `TestWhoAmI_ExpiredTokenRejected` — full suite must stay green, not just the new test.

- [ ] **Step 8: Commit**

```bash
git add migrations/000005_agent_tokens_expiry.up.sql migrations/000005_agent_tokens_expiry.down.sql internal/core/identity/identity.go internal/core/identity/identity_test.go
git commit -m "Day 5: token expiry (30-day TTL, folds into ErrInvalidToken)"
```

---

### Task 2: MCP tool/handler types

**Files:**
- Modify: `internal/mcp/registry.go` (replace placeholder `Tool` struct, add `Handler` type, add `Get` method)
- Test: Create `internal/mcp/registry_test.go`

**Interfaces:**
- Consumes: `identity.AuthenticatedScope` (from `internal/core/identity`, already defined).
- Produces (for Tasks 3 and 4):
  - `type Handler func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error)`
  - `type Tool struct { Name string; Description string; RequiresAuth bool; Handler Handler }`
  - `func (r *Registry) Get(name string) (Tool, bool)`
  - Existing `NewRegistry`, `Register(t Tool)`, `List() []Tool` keep their current signatures.

- [ ] **Step 1: Write the failing test**

Create `internal/mcp/registry_test.go`:
```go
package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/H4RL33/wormhole/internal/core/identity"
)

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	tool := Tool{
		Name:         "wormhole.agent.whoami",
		Description:  "test tool",
		RequiresAuth: true,
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			return "ok", nil
		},
	}
	r.Register(tool)

	got, ok := r.Get("wormhole.agent.whoami")
	if !ok {
		t.Fatalf("Get: tool not found")
	}
	if got.Name != tool.Name || got.RequiresAuth != tool.RequiresAuth {
		t.Fatalf("Get: got %+v, want matching Name/RequiresAuth of %+v", got, tool)
	}
	if got.Handler == nil {
		t.Fatalf("Get: Handler is nil")
	}
}

func TestRegistry_GetMissing(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Get("wormhole.agent.nonexistent"); ok {
		t.Fatalf("Get: expected ok=false for unregistered tool")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/mcp/ -v`
Expected: FAIL to compile — `Tool` has no `RequiresAuth`/`Handler` field yet, `Registry` has no `Get` method.

- [ ] **Step 3: Implement**

Replace the contents of `internal/mcp/registry.go`:
```go
package mcp

import (
	"context"
	"encoding/json"

	"github.com/H4RL33/wormhole/internal/core/identity"
)

// Handler executes one MCP tool call. scope is nil when the tool's
// RequiresAuth is false; otherwise it is the AuthenticatedScope the auth
// middleware already resolved from the caller's bearer token
// (docs/architecture.md M4 — handlers never see a raw token). projectID is
// always populated from the call envelope, independent of auth, since
// project-scoped bootstrap calls (e.g. registration) need it before any
// token exists.
type Handler func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error)

// Tool is an MCP tool descriptor: name, docs, whether the auth middleware
// must resolve a scope before dispatch, and the handler itself.
type Tool struct {
	Name         string
	Description  string
	RequiresAuth bool
	Handler      Handler
}

// Registry holds the set of MCP tools this server exposes. Empty at boot
// per Day 1 scope — tools register themselves as each pillar lands.
type Registry struct {
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

func (r *Registry) Register(t Tool) {
	r.tools[t.Name] = t
}

// Get returns the tool registered under name, if any.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

func (r *Registry) List() []Tool {
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}
```

Delete `internal/mcp/doc.go` if its content is now redundant with the doc comments above — check its contents first; if it holds package-level documentation only, merge that text into a `// Package mcp ...` comment at the top of `registry.go` and delete `doc.go`, rather than keeping two files whose scope is now confused. If `doc.go` holds something else, leave it and just leave `registry.go` as above.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/mcp/ -v`
Expected: PASS — `TestRegistry_RegisterAndGet` and `TestRegistry_GetMissing` both green.

- [ ] **Step 5: Run full build to confirm nothing else broke**

Run: `go build ./...`
Expected: no errors. (`cmd/wormhole-server/main.go` still constructs `mcp.NewRegistry()` and calls `.List()` — both signatures are unchanged, so it should build untouched.)

- [ ] **Step 6: Commit**

```bash
git add internal/mcp/registry.go internal/mcp/registry_test.go internal/mcp/doc.go
git commit -m "Day 5: MCP Tool gains Handler/RequiresAuth, Registry gains Get"
```

---

### Task 3: Auth middleware + tool-call HTTP endpoint

**Files:**
- Create: `internal/mcp/server.go`
- Test: Create `internal/mcp/server_test.go`

**Interfaces:**
- Consumes: `Registry.Get(name string) (Tool, bool)`, `Handler` type, `Tool` struct (Task 2); `identity.Store.WhoAmI(ctx, projectID, rawToken string) (identity.AuthenticatedScope, error)` and `identity.ErrInvalidToken` (already exist, unchanged).
- Produces (for Task 4's e2e test and `cmd/wormhole-server`):
  - `type CallRequest struct { Tool string; ProjectID string; Arguments json.RawMessage }` (JSON tags: `tool`, `project_id`, `arguments`)
  - `type CallResponse struct { Result any; Error string }` (JSON tags: `result,omitempty`, `error,omitempty`)
  - `func NewCallHandler(registry *Registry, identityStore *identity.Store) http.HandlerFunc`

- [ ] **Step 1: Write the failing test**

Create `internal/mcp/server_test.go`:
```go
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

func TestCallHandler_UnknownTool(t *testing.T) {
	registry := NewRegistry()
	store := testIdentityStore(t)
	handler := NewCallHandler(registry, store)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	body, _ := json.Marshal(CallRequest{Tool: "wormhole.agent.nonexistent", ProjectID: "x", Arguments: json.RawMessage(`{}`)})
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

	body, _ := json.Marshal(CallRequest{Tool: "wormhole.agent.whoami", ProjectID: "x", Arguments: json.RawMessage(`{}`)})
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

	body, _ := json.Marshal(CallRequest{Tool: "wormhole.agent.whoami", ProjectID: "x", Arguments: json.RawMessage(`{}`)})
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/mcp/ -v`
Expected: FAIL to compile — `NewCallHandler`, `CallRequest`, `CallResponse` don't exist yet.

- [ ] **Step 3: Implement**

Create `internal/mcp/server.go`:
```go
package mcp

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/H4RL33/wormhole/internal/core/identity"
)

// CallRequest is the envelope for a single MCP tool invocation over the
// /mcp/tools/call HTTP endpoint. project_id is always required, even for
// tools that don't need auth yet (e.g. registration), because it's the
// scope every identity operation is keyed on (RFC-0001 §13).
type CallRequest struct {
	Tool      string          `json:"tool"`
	ProjectID string          `json:"project_id"`
	Arguments json.RawMessage `json:"arguments"`
}

// CallResponse is the envelope returned for a tool call: exactly one of
// Result or Error is populated.
type CallResponse struct {
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// NewCallHandler builds the /mcp/tools/call endpoint. It decodes the call
// envelope, looks up the tool, and — for tools with RequiresAuth — resolves
// the caller's bearer token via identityStore.WhoAmI before dispatch
// (docs/architecture.md M4: auth happens once, at this boundary; handlers
// receive an already-resolved scope, never a raw token).
func NewCallHandler(registry *Registry, identityStore *identity.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req CallRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeCallResponse(w, http.StatusBadRequest, CallResponse{Error: "invalid request body"})
			return
		}

		tool, ok := registry.Get(req.Tool)
		if !ok {
			writeCallResponse(w, http.StatusNotFound, CallResponse{Error: "unknown tool: " + req.Tool})
			return
		}

		var scope *identity.AuthenticatedScope
		if tool.RequiresAuth {
			token := bearerToken(r.Header.Get("Authorization"))
			if token == "" {
				writeCallResponse(w, http.StatusUnauthorized, CallResponse{Error: "missing bearer token"})
				return
			}
			resolved, err := identityStore.WhoAmI(r.Context(), req.ProjectID, token)
			if errors.Is(err, identity.ErrInvalidToken) {
				writeCallResponse(w, http.StatusUnauthorized, CallResponse{Error: "invalid or expired token"})
				return
			}
			if err != nil {
				writeCallResponse(w, http.StatusInternalServerError, CallResponse{Error: "auth resolution failed"})
				return
			}
			scope = &resolved
		}

		result, err := tool.Handler(r.Context(), scope, req.ProjectID, req.Arguments)
		if err != nil {
			writeCallResponse(w, http.StatusBadRequest, CallResponse{Error: err.Error()})
			return
		}
		writeCallResponse(w, http.StatusOK, CallResponse{Result: result})
	}
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimPrefix(header, prefix)
}

func writeCallResponse(w http.ResponseWriter, status int, resp CallResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(resp)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/mcp/ -v`
Expected: PASS for all of `TestCallHandler_UnknownTool`, `TestCallHandler_RequiresAuthMissingToken`, `TestCallHandler_RequiresAuthInvalidToken`, `TestCallHandler_NoAuthRequiredDispatchesDirectly`, plus Task 2's registry tests still green.

- [ ] **Step 5: Commit**

```bash
git add internal/mcp/server.go internal/mcp/server_test.go
git commit -m "Day 5: MCP tool-call endpoint with auth middleware"
```

---

### Task 4: Implement agent.register and agent.whoami tools, wire into server

**Files:**
- Create: `internal/mcp/agent.go`
- Test: Create `internal/mcp/agent_test.go`
- Modify: `cmd/wormhole-server/main.go`

**Interfaces:**
- Consumes: `identity.Store.Register(ctx, projectID string, permissions []string, owner, model string, capabilities, repositories, roles []string) (identity.Agent, identity.Passport, string, error)` (unchanged); `Tool`, `Handler`, `Registry.Register` (Task 2); `NewCallHandler` (Task 3).
- Produces (for Task 5's e2e test):
  - `type RegisterAgentInput struct { Permissions, Owner, Model string/[]string; Capabilities, Repositories, Roles []string }` (JSON tags: `permissions`, `owner`, `model`, `capabilities`, `repositories`, `roles`)
  - `type RegisterAgentOutput struct { AgentID, PassportID, Token string; Repositories, Roles []string; IssuedAt time.Time }` (JSON tags: `agent_id`, `passport_id`, `token`, `repositories`, `roles`, `issued_at`)
  - `func RegisterAgentTool(store *identity.Store) Tool`
  - `type WhoAmIOutput struct { AgentID, Owner, Model, ProjectID string; Capabilities, Permissions []string }` (JSON tags: `agent_id`, `owner`, `model`, `capabilities`, `project_id`, `permissions`)
  - `func WhoAmITool() Tool`

- [ ] **Step 1: Write the failing test**

Create `internal/mcp/agent_test.go`:
```go
package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/H4RL33/wormhole/internal/core/identity"
)

func TestRegisterAgentTool_Handler(t *testing.T) {
	store := testIdentityStore(t)
	tool := RegisterAgentTool(store)
	if tool.Name != "wormhole.agent.register" {
		t.Fatalf("Name: got %q", tool.Name)
	}
	if tool.RequiresAuth {
		t.Fatalf("RequiresAuth: got true, want false — registration bootstraps identity, no token exists yet")
	}

	projectID := mustCreateProject(t, store, "mcp-register")
	arguments, _ := json.Marshal(RegisterAgentInput{
		Permissions:  []string{"event.publish"},
		Owner:        "harley",
		Model:        "claude",
		Capabilities: []string{"code_review"},
	})

	result, err := tool.Handler(context.Background(), nil, projectID, arguments)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out, ok := result.(RegisterAgentOutput)
	if !ok {
		t.Fatalf("result type: got %T, want RegisterAgentOutput", result)
	}
	if out.AgentID == "" || out.PassportID == "" || out.Token == "" {
		t.Fatalf("output missing fields: %+v", out)
	}
}

// mustCreateProject inserts a project directly (identity.Store has no
// project-creation method — projects are out of this task's scope) and
// registers cleanup. Mirrors identity_test.go's createProject.
func mustCreateProject(t *testing.T, store *identity.Store, name string) string {
	t.Helper()
	db := store.DB()
	var id string
	if err := db.QueryRow(`INSERT INTO projects (name, owner) VALUES ($1, $2) RETURNING id`, name, "harley").Scan(&id); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM projects WHERE id = $1`, id); err != nil {
			t.Logf("cleanup: delete project %s: %v", id, err)
		}
	})
	return id
}

func TestWhoAmITool_Handler(t *testing.T) {
	tool := WhoAmITool()
	if tool.Name != "wormhole.agent.whoami" {
		t.Fatalf("Name: got %q", tool.Name)
	}
	if !tool.RequiresAuth {
		t.Fatalf("RequiresAuth: got false, want true")
	}

	scope := &identity.AuthenticatedScope{
		Agent:       identity.Agent{ID: "agent-1", Owner: "harley", Model: "claude", Capabilities: []string{"code_review"}},
		ProjectID:   "proj-1",
		Permissions: []string{"event.publish"},
	}
	result, err := tool.Handler(context.Background(), scope, "proj-1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out, ok := result.(WhoAmIOutput)
	if !ok {
		t.Fatalf("result type: got %T, want WhoAmIOutput", result)
	}
	if out.AgentID != "agent-1" || out.ProjectID != "proj-1" {
		t.Fatalf("output: got %+v", out)
	}
}
```

`mustCreateProject` above calls `store.DB()`, which doesn't exist yet on `identity.Store` (its `db` field is unexported). That accessor is added in Step 3 below, before this test is expected to compile.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/mcp/ -v`
Expected: FAIL to compile — `RegisterAgentTool`, `RegisterAgentInput`, `RegisterAgentOutput`, `WhoAmITool`, `WhoAmIOutput` don't exist; `identity.Store.DB` doesn't exist.

- [ ] **Step 3: Implement `identity.Store.DB`**

Apply the `DB()` method addition from Step 1b to `internal/core/identity/identity.go`.

- [ ] **Step 4: Implement the tools**

Create `internal/mcp/agent.go`:
```go
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/H4RL33/wormhole/internal/core/identity"
)

// RegisterAgentInput is the wormhole.agent.register argument shape.
// Schema is indicative per architecture.md M1 — frozen here at
// implementation time, not finalized by any RFC text.
type RegisterAgentInput struct {
	Permissions  []string `json:"permissions"`
	Owner        string   `json:"owner"`
	Model        string   `json:"model"`
	Capabilities []string `json:"capabilities"`
	Repositories []string `json:"repositories"`
	Roles        []string `json:"roles"`
}

// RegisterAgentOutput is the wormhole.agent.register result shape. Token
// is the raw bearer token, returned exactly once (identity.Store.Register
// never persists or re-derives it).
type RegisterAgentOutput struct {
	AgentID      string    `json:"agent_id"`
	PassportID   string    `json:"passport_id"`
	Token        string    `json:"token"`
	Repositories []string  `json:"repositories"`
	Roles        []string  `json:"roles"`
	IssuedAt     time.Time `json:"issued_at"`
}

// RegisterAgentTool wires wormhole.agent.register: no auth required, since
// registration is how an identity first comes into existence (RFC-0001
// §8.5 joining flow, step 1).
func RegisterAgentTool(store *identity.Store) Tool {
	return Tool{
		Name:         "wormhole.agent.register",
		Description:  "Registers a new agent identity, issues its passport and a project-scoped bearer token.",
		RequiresAuth: false,
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			var in RegisterAgentInput
			if err := json.Unmarshal(arguments, &in); err != nil {
				return nil, fmt.Errorf("mcp: decode wormhole.agent.register arguments: %w", err)
			}
			agent, passport, token, err := store.Register(ctx, projectID, in.Permissions, in.Owner, in.Model, in.Capabilities, in.Repositories, in.Roles)
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.agent.register: %w", err)
			}
			return RegisterAgentOutput{
				AgentID:      agent.ID,
				PassportID:   passport.ID,
				Token:        token,
				Repositories: passport.Repositories,
				Roles:        passport.Roles,
				IssuedAt:     passport.IssuedAt,
			}, nil
		},
	}
}

// WhoAmIOutput is the wormhole.agent.whoami result shape: the identity and
// authorization scope the auth middleware already resolved.
type WhoAmIOutput struct {
	AgentID      string   `json:"agent_id"`
	Owner        string   `json:"owner"`
	Model        string   `json:"model"`
	Capabilities []string `json:"capabilities"`
	ProjectID    string   `json:"project_id"`
	Permissions  []string `json:"permissions"`
}

// WhoAmITool wires wormhole.agent.whoami: requires auth, and its handler
// does no identity lookup of its own — the resolved scope from the
// middleware (architecture.md M4) is the entire answer.
func WhoAmITool() Tool {
	return Tool{
		Name:         "wormhole.agent.whoami",
		Description:  "Returns the identity and authorization scope resolved from the caller's bearer token.",
		RequiresAuth: true,
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			return WhoAmIOutput{
				AgentID:      scope.Agent.ID,
				Owner:        scope.Agent.Owner,
				Model:        scope.Agent.Model,
				Capabilities: scope.Agent.Capabilities,
				ProjectID:    scope.ProjectID,
				Permissions:  scope.Permissions,
			}, nil
		},
	}
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/mcp/ -v`
Expected: PASS for `TestRegisterAgentTool_Handler`, `TestWhoAmITool_Handler`, plus every earlier test in the package still green.

- [ ] **Step 6: Wire into `cmd/wormhole-server/main.go`**

Replace the full contents of `cmd/wormhole-server/main.go`:
```go
package main

import (
	"log"
	"net/http"
	"time"

	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/mcp"
	"github.com/H4RL33/wormhole/internal/storage"
	"github.com/H4RL33/wormhole/internal/types"
)

func main() {
	cfg := types.LoadConfig()

	db, err := storage.Open(cfg)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	identityStore := identity.NewStore(db)

	registry := mcp.NewRegistry()
	registry.Register(mcp.RegisterAgentTool(identityStore))
	registry.Register(mcp.WhoAmITool())

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/mcp/tools", func(w http.ResponseWriter, r *http.Request) {
		tools := registry.List()
		w.Header().Set("Content-Type", "application/json")
		if len(tools) == 0 {
			w.Write([]byte("[]"))
			return
		}
	})
	mux.HandleFunc("/mcp/tools/call", mcp.NewCallHandler(registry, identityStore))

	log.Printf("wormhole-server listening on %s", cfg.ListenAddr)
	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
```

- [ ] **Step 7: Verify the build**

Run: `go build ./...`
Expected: no errors.

- [ ] **Step 8: Commit**

```bash
git add internal/mcp/agent.go internal/mcp/agent_test.go internal/core/identity/identity.go cmd/wormhole-server/main.go
git commit -m "Day 5: wire wormhole.agent.register and wormhole.agent.whoami"
```

---

### Task 5: End-to-end integration test

**Files:**
- Create: `internal/mcp/e2e_test.go`

**Interfaces:**
- Consumes: everything produced by Tasks 1-4 — `NewCallHandler`, `RegisterAgentTool`, `WhoAmITool`, `CallRequest`, `CallResponse`, `RegisterAgentOutput`, `WhoAmIOutput`, `testIdentityStore`, `mustCreateProject`. No new exported symbols.

- [ ] **Step 1: Write the end-to-end test**

Create `internal/mcp/e2e_test.go`:
```go
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestE2E_RegisterThenWhoAmI drives RFC-0001 §8.5's first two joining-flow
// steps through the real HTTP tool-call endpoint: an MCP client registers
// an agent, gets back a passport and token, then calls whoami with that
// token and gets back the same identity.
func TestE2E_RegisterThenWhoAmI(t *testing.T) {
	store := testIdentityStore(t)
	registry := NewRegistry()
	registry.Register(RegisterAgentTool(store))
	registry.Register(WhoAmITool())
	handler := NewCallHandler(registry, store)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	projectID := mustCreateProject(t, store, "e2e-register-whoami")

	registerArgs, _ := json.Marshal(RegisterAgentInput{
		Permissions: []string{"event.publish", "kb.write"},
		Owner:       "harley",
		Model:       "claude",
	})
	registerBody, _ := json.Marshal(CallRequest{Tool: "wormhole.agent.register", ProjectID: projectID, Arguments: registerArgs})
	resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(registerBody))
	if err != nil {
		t.Fatalf("register POST: %v", err)
	}
	var registerResp CallResponse
	if err := json.NewDecoder(resp.Body).Decode(&registerResp); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register status: got %d, body %+v", resp.StatusCode, registerResp)
	}

	resultRaw, _ := json.Marshal(registerResp.Result)
	var registerOut RegisterAgentOutput
	if err := json.Unmarshal(resultRaw, &registerOut); err != nil {
		t.Fatalf("unmarshal register result: %v", err)
	}
	if registerOut.AgentID == "" || registerOut.Token == "" {
		t.Fatalf("register output missing fields: %+v", registerOut)
	}

	whoamiBody, _ := json.Marshal(CallRequest{Tool: "wormhole.agent.whoami", ProjectID: projectID, Arguments: json.RawMessage(`{}`)})
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(whoamiBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+registerOut.Token)
	whoamiResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("whoami POST: %v", err)
	}
	defer whoamiResp.Body.Close()
	var whoamiCallResp CallResponse
	if err := json.NewDecoder(whoamiResp.Body).Decode(&whoamiCallResp); err != nil {
		t.Fatalf("decode whoami response: %v", err)
	}
	if whoamiResp.StatusCode != http.StatusOK {
		t.Fatalf("whoami status: got %d, body %+v", whoamiResp.StatusCode, whoamiCallResp)
	}

	whoamiResultRaw, _ := json.Marshal(whoamiCallResp.Result)
	var whoamiOut WhoAmIOutput
	if err := json.Unmarshal(whoamiResultRaw, &whoamiOut); err != nil {
		t.Fatalf("unmarshal whoami result: %v", err)
	}
	if whoamiOut.AgentID != registerOut.AgentID {
		t.Fatalf("whoami AgentID: got %q, want %q (from register)", whoamiOut.AgentID, registerOut.AgentID)
	}
	if whoamiOut.ProjectID != projectID {
		t.Fatalf("whoami ProjectID: got %q, want %q", whoamiOut.ProjectID, projectID)
	}
}

// TestE2E_WhoAmI_RejectsExpiredToken proves the auth middleware's expiry
// enforcement end-to-end, not just at the identity.Store layer (Task 1
// already covers the Store layer directly).
func TestE2E_WhoAmI_RejectsExpiredToken(t *testing.T) {
	store := testIdentityStore(t)
	registry := NewRegistry()
	registry.Register(RegisterAgentTool(store))
	registry.Register(WhoAmITool())
	handler := NewCallHandler(registry, store)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	projectID := mustCreateProject(t, store, "e2e-expired-token")

	registerArgs, _ := json.Marshal(RegisterAgentInput{Permissions: []string{"event.publish"}, Owner: "harley", Model: "claude"})
	registerBody, _ := json.Marshal(CallRequest{Tool: "wormhole.agent.register", ProjectID: projectID, Arguments: registerArgs})
	resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(registerBody))
	if err != nil {
		t.Fatalf("register POST: %v", err)
	}
	var registerResp CallResponse
	json.NewDecoder(resp.Body).Decode(&registerResp)
	resp.Body.Close()
	resultRaw, _ := json.Marshal(registerResp.Result)
	var registerOut RegisterAgentOutput
	json.Unmarshal(resultRaw, &registerOut)

	if _, err := store.DB().ExecContext(context.Background(),
		`UPDATE agent_tokens SET expires_at = now() - interval '1 hour' WHERE agent_id = $1`,
		registerOut.AgentID,
	); err != nil {
		t.Fatalf("backdate token expiry: %v", err)
	}

	whoamiBody, _ := json.Marshal(CallRequest{Tool: "wormhole.agent.whoami", ProjectID: projectID, Arguments: json.RawMessage(`{}`)})
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(whoamiBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+registerOut.Token)
	whoamiResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("whoami POST: %v", err)
	}
	defer whoamiResp.Body.Close()
	if whoamiResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("whoami status: got %d, want %d (expired token)", whoamiResp.StatusCode, http.StatusUnauthorized)
	}
}
```

- [ ] **Step 2: Run the full package test suite**

Run: `go test ./internal/mcp/ -v`
Expected: PASS for `TestE2E_RegisterThenWhoAmI`, `TestE2E_WhoAmI_RejectsExpiredToken`, and every test from Tasks 2-4 in this package.

- [ ] **Step 3: Run the full repo test suite**

Run: `go test ./...`
Expected: PASS across all packages, including `internal/core/identity` (Task 1's expiry test) and `internal/mcp` (this task's e2e tests).

- [ ] **Step 4: Manual smoke test against the real server**

Run:
```bash
docker-compose up -d db
/home/harley/go/bin/migrate -path migrations -database "postgres://wormhole:wormhole@127.0.0.1:5432/wormhole?sslmode=disable" up
go run ./cmd/wormhole-server &
sleep 1
curl -s -X POST localhost:8080/mcp/tools/call -H 'Content-Type: application/json' -d '{"tool":"wormhole.agent.register","project_id":"<a real project uuid, insert one via psql first>","arguments":{"permissions":["event.publish"],"owner":"harley","model":"claude"}}'
```
Expected: JSON response with `result.agent_id`, `result.token`, etc. (a `project_id` must exist in the `projects` table first — insert one manually via `psql` or the test helper pattern; there is no `wormhole.project.create` tool yet, out of Day 5 scope). Kill the background `go run` process afterward.

- [ ] **Step 5: Commit**

```bash
git add internal/mcp/e2e_test.go
git commit -m "Day 5: end-to-end register->whoami test through MCP HTTP endpoint"
```

---

## Post-plan: update ROADMAP.md

After all 5 tasks are complete and reviewed, check off Day 5's three items in `ROADMAP.md` (lines 54-56) and commit that separately, the same way Day 4 did (`6c52552 Day 4: mark roadmap items complete`).
