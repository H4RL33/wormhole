# Local Runtime P1 — Walking Skeleton Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A coding harness dials `wormholed`'s local Unix socket, calls `wormhole.agent.whoami`, and gets back the identity resolved by the existing (unmodified) Coordination Server — proving the full chain harness → local socket → `wormholed` → HTTP `/mcp` → Coordination Server → Postgres, with a local SQLite cache write on success.

**Architecture:** New `internal/runtime/*` package tree (RFC-0003 §6.3), never imported by `internal/core/*` or `internal/mcp` (one-way, same shape as the existing R1 rule). `wormholed` duplicates the JSON-RPC 2.0 wire shapes it needs to talk to the Coordination Server rather than importing `internal/mcp`, matching the precedent already set by `cmd/wormhole-cli/main.go` (see its `rpcRequest` doc comment) — this keeps the daemon binary decoupled from the server's registry/DB stack.

**Tech Stack:** Go 1.26, `modernc.org/sqlite` (pure-Go SQLite driver, no cgo — chosen for cross-platform build simplicity), `net` (Unix domain socket), stdlib `net/http`/`encoding/json` for the Coordination Server proxy call.

## Global Constraints

- Module path: `github.com/H4RL33/wormhole` (go.mod).
- No ORM, no ambient global state, `context.Context` first param on every store/network method — same discipline as `internal/core/identity` (`docs/architecture.md` §3).
- Sentinel errors as package vars, `errors.New("<pkg>: ...")`, wrapped internal errors via `fmt.Errorf("<pkg>: <op>: %w", err)` — `docs/architecture.md` §3 rules 2-3.
- `internal/runtime/*` never imports `internal/core/*`, `internal/mcp`, or `internal/storage` (RFC-0003 §6.3 keeps these trees separate). Duplication of small wire-shape structs across the boundary is accepted precedent (see cmd/wormhole-cli), not a bug.
- Raw credential tokens: never logged, never written anywhere except the existing `~/.wormhole/credentials/<profile>.json` file this plan reads from (already 0600, already written by `wormhole-cli`) — `wormholed` does not create or modify that file in P1.
- `go build ./...`, `go vet ./...`, `go test ./...` must pass before any task is considered done (mirrors `docs/architecture.md` T4).

---

### Task 1: `internal/runtime/localstore` — SQLite-backed WhoAmI cache

**Files:**
- Create: `internal/runtime/localstore/localstore.go`
- Create: `internal/runtime/localstore/localstore_test.go`

**Interfaces:**
- Produces: `localstore.Open(path string) (*localstore.Store, error)`, `(*Store).Close() error`, `(*Store).CacheWhoAmI(ctx context.Context, c localstore.WhoAmICache) error`, `(*Store).GetCachedWhoAmI(ctx context.Context, agentID string) (localstore.WhoAmICache, error)`, `localstore.WhoAmICache{AgentID, Owner, Model string; Capabilities, Permissions []string; ProjectID string; CachedAt time.Time}`, `localstore.ErrNotFound`.

- [ ] **Step 1: Add the SQLite dependency**

Run: `go get modernc.org/sqlite@latest`
Expected: `go.mod` gains a `modernc.org/sqlite` require line; `go.sum` updated.

- [ ] **Step 2: Write the failing test**

```go
// internal/runtime/localstore/localstore_test.go
package localstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestCacheAndGetWhoAmI(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	want := WhoAmICache{
		AgentID:      "agent-1",
		Owner:        "harley",
		Model:        "claude-sonnet-5",
		Capabilities: []string{"code", "review"},
		ProjectID:    "project-1",
		Permissions:  []string{"read_kb", "create_task"},
		CachedAt:     time.Now().UTC().Truncate(time.Second),
	}

	if err := store.CacheWhoAmI(ctx, want); err != nil {
		t.Fatalf("CacheWhoAmI: %v", err)
	}

	got, err := store.GetCachedWhoAmI(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetCachedWhoAmI: %v", err)
	}
	if got.AgentID != want.AgentID || got.Owner != want.Owner || got.Model != want.Model ||
		got.ProjectID != want.ProjectID || !got.CachedAt.Equal(want.CachedAt) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if len(got.Capabilities) != 2 || got.Capabilities[0] != "code" || got.Capabilities[1] != "review" {
		t.Fatalf("capabilities mismatch: got %v", got.Capabilities)
	}
	if len(got.Permissions) != 2 || got.Permissions[0] != "read_kb" || got.Permissions[1] != "create_task" {
		t.Fatalf("permissions mismatch: got %v", got.Permissions)
	}
}

func TestGetCachedWhoAmI_NotFound(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	_, err = store.GetCachedWhoAmI(context.Background(), "no-such-agent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got err %v, want ErrNotFound", err)
	}
}

func TestCacheWhoAmI_Overwrite(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	first := WhoAmICache{AgentID: "agent-1", Owner: "harley", Model: "claude-sonnet-5", ProjectID: "project-1", CachedAt: time.Now().UTC().Truncate(time.Second)}
	if err := store.CacheWhoAmI(ctx, first); err != nil {
		t.Fatalf("CacheWhoAmI (first): %v", err)
	}
	second := first
	second.Model = "claude-opus-4-8"
	if err := store.CacheWhoAmI(ctx, second); err != nil {
		t.Fatalf("CacheWhoAmI (second): %v", err)
	}

	got, err := store.GetCachedWhoAmI(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetCachedWhoAmI: %v", err)
	}
	if got.Model != "claude-opus-4-8" {
		t.Fatalf("got model %q, want overwrite to take effect", got.Model)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/runtime/localstore/... -v`
Expected: FAIL — build error, `Open`/`WhoAmICache`/etc. undefined.

- [ ] **Step 4: Write the implementation**

```go
// internal/runtime/localstore/localstore.go

// Package localstore is wormholed's durable local state (RFC-0003 §6.3,
// §7.2). It follows the Store-struct/sentinel-error/wrapped-error shape
// established by internal/core/identity (docs/architecture.md §3), adapted
// for SQLite: no transactions needed yet (single-statement writes only,
// P1 scope), schema applied on Open rather than via golang-migrate (that
// tooling targets the Coordination Server's Postgres only).
package localstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a cache lookup has no matching row.
var ErrNotFound = errors.New("localstore: not found")

const schema = `
CREATE TABLE IF NOT EXISTS whoami_cache (
	agent_id     TEXT PRIMARY KEY,
	owner        TEXT NOT NULL,
	model        TEXT NOT NULL,
	capabilities TEXT NOT NULL DEFAULT '[]',
	project_id   TEXT NOT NULL,
	permissions  TEXT NOT NULL DEFAULT '[]',
	cached_at    TIMESTAMP NOT NULL
);
`

// Store wraps a *sql.DB backed by a local SQLite file.
type Store struct {
	db *sql.DB
}

// Open creates (if needed) and opens the SQLite file at path, applying the
// schema. Callers must Close the returned Store.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("localstore: open %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("localstore: apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

// WhoAmICache is the cached wormhole.agent.whoami result for one agent.
type WhoAmICache struct {
	AgentID      string
	Owner        string
	Model        string
	Capabilities []string
	ProjectID    string
	Permissions  []string
	CachedAt     time.Time
}

// CacheWhoAmI upserts the cached identity for c.AgentID.
func (s *Store) CacheWhoAmI(ctx context.Context, c WhoAmICache) error {
	capsJSON, err := json.Marshal(nonNil(c.Capabilities))
	if err != nil {
		return fmt.Errorf("localstore: marshal capabilities: %w", err)
	}
	permsJSON, err := json.Marshal(nonNil(c.Permissions))
	if err != nil {
		return fmt.Errorf("localstore: marshal permissions: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO whoami_cache (agent_id, owner, model, capabilities, project_id, permissions, cached_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET
			owner = excluded.owner,
			model = excluded.model,
			capabilities = excluded.capabilities,
			project_id = excluded.project_id,
			permissions = excluded.permissions,
			cached_at = excluded.cached_at
	`, c.AgentID, c.Owner, c.Model, string(capsJSON), c.ProjectID, string(permsJSON), c.CachedAt)
	if err != nil {
		return fmt.Errorf("localstore: cache whoami for %s: %w", c.AgentID, err)
	}
	return nil
}

// GetCachedWhoAmI returns the cached identity for agentID, or ErrNotFound.
func (s *Store) GetCachedWhoAmI(ctx context.Context, agentID string) (WhoAmICache, error) {
	var c WhoAmICache
	var capsJSON, permsJSON string
	err := s.db.QueryRowContext(ctx, `
		SELECT agent_id, owner, model, capabilities, project_id, permissions, cached_at
		FROM whoami_cache WHERE agent_id = ?
	`, agentID).Scan(&c.AgentID, &c.Owner, &c.Model, &capsJSON, &c.ProjectID, &permsJSON, &c.CachedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return WhoAmICache{}, ErrNotFound
	}
	if err != nil {
		return WhoAmICache{}, fmt.Errorf("localstore: get cached whoami for %s: %w", agentID, err)
	}
	if err := json.Unmarshal([]byte(capsJSON), &c.Capabilities); err != nil {
		return WhoAmICache{}, fmt.Errorf("localstore: unmarshal capabilities: %w", err)
	}
	if err := json.Unmarshal([]byte(permsJSON), &c.Permissions); err != nil {
		return WhoAmICache{}, fmt.Errorf("localstore: unmarshal permissions: %w", err)
	}
	c.CachedAt = c.CachedAt.UTC()
	return c, nil
}

func nonNil(ss []string) []string {
	if ss == nil {
		return []string{}
	}
	return ss
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/runtime/localstore/... -v`
Expected: PASS — `TestCacheAndGetWhoAmI`, `TestGetCachedWhoAmI_NotFound`, `TestCacheWhoAmI_Overwrite` all pass.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/runtime/localstore
git commit -m "feat(runtime): SQLite-backed local WhoAmI cache"
```

---

### Task 2: `internal/runtime/config` — local paths and credential loading

**Files:**
- Create: `internal/runtime/config/config.go`
- Create: `internal/runtime/config/config_test.go`

**Interfaces:**
- Consumes: nothing from Task 1.
- Produces: `config.Config{SocketPath, DBPath string; Credentials config.Credentials}`, `config.Credentials{Server, ProjectID, AgentID, Token string}`, `config.Load(profileName string) (config.Config, error)`, `config.ErrCredentialsNotFound`.

- [ ] **Step 1: Write the failing test**

```go
// internal/runtime/config/config_test.go
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeFakeCredentials(t *testing.T, home, profile string) {
	t.Helper()
	dir := filepath.Join(home, ".wormhole", "credentials")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	data, err := json.Marshal(map[string]string{
		"server":     "http://localhost:8080",
		"project_id": "project-1",
		"agent_id":   "agent-1",
		"token":      "test-token",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, profile+".json"), data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestLoad_ReadsCredentialsAndDerivesPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(home, "run"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	writeFakeCredentials(t, home, "default")

	cfg, err := Load("default")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Credentials.Server != "http://localhost:8080" {
		t.Fatalf("got server %q", cfg.Credentials.Server)
	}
	if cfg.Credentials.Token != "test-token" {
		t.Fatalf("got token %q", cfg.Credentials.Token)
	}
	if cfg.SocketPath != filepath.Join(home, "run", "wormhole", "wormholed.sock") {
		t.Fatalf("got socket path %q", cfg.SocketPath)
	}
	if cfg.DBPath != filepath.Join(home, "data", "wormhole", "wormholed.db") {
		t.Fatalf("got db path %q", cfg.DBPath)
	}
}

func TestLoad_MissingProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	_, err := Load("nonexistent")
	if !errors.Is(err, ErrCredentialsNotFound) {
		t.Fatalf("got err %v, want ErrCredentialsNotFound", err)
	}
}

func TestLoad_FallsBackToHomeWhenXDGUnset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("XDG_DATA_HOME", "")
	writeFakeCredentials(t, home, "default")

	cfg, err := Load("default")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DBPath != filepath.Join(home, ".local", "share", "wormhole", "wormholed.db") {
		t.Fatalf("got db path %q, want XDG default fallback under home", cfg.DBPath)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/runtime/config/... -v`
Expected: FAIL — build error, `Load`/`Config`/etc. undefined.

- [ ] **Step 3: Write the implementation**

```go
// internal/runtime/config/config.go

// Package config resolves wormholed's local paths and reads the credential
// profile wormhole-cli already wrote (RFC-0003 §6.1). It duplicates the
// minimal credentials JSON shape from cmd/wormhole-cli/main.go rather than
// importing it: main packages are not importable, and this matches the
// existing wire-shape-duplication precedent at the cmd/wormhole-cli module
// boundary (docs/architecture.md §2). wormholed does not write this file
// in P1 — only reads what wormhole-cli's `wormhole join` already produced.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrCredentialsNotFound is returned when the named profile has no
// credentials file under ~/.wormhole/credentials/.
var ErrCredentialsNotFound = errors.New("config: credentials not found")

// Credentials mirrors the fields of cmd/wormhole-cli's credentials struct
// that wormholed needs to proxy calls to the Coordination Server.
type Credentials struct {
	Server    string `json:"server"`
	ProjectID string `json:"project_id"`
	AgentID   string `json:"agent_id"`
	Token     string `json:"token"`
}

// Config is wormholed's resolved local configuration for one run.
type Config struct {
	SocketPath  string
	DBPath      string
	Credentials Credentials
}

// Load resolves paths and reads the named credential profile.
func Load(profileName string) (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, fmt.Errorf("config: resolve home directory: %w", err)
	}

	credPath := filepath.Join(home, ".wormhole", "credentials", profileName+".json")
	data, err := os.ReadFile(credPath)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("%w: profile %q at %s", ErrCredentialsNotFound, profileName, credPath)
	}
	if err != nil {
		return Config{}, fmt.Errorf("config: read credentials %s: %w", credPath, err)
	}
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return Config{}, fmt.Errorf("config: decode credentials %s: %w", credPath, err)
	}

	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = filepath.Join(os.TempDir(), "wormhole-runtime")
	}
	dataDir := os.Getenv("XDG_DATA_HOME")
	if dataDir == "" {
		dataDir = filepath.Join(home, ".local", "share")
	}

	return Config{
		SocketPath:  filepath.Join(runtimeDir, "wormhole", "wormholed.sock"),
		DBPath:      filepath.Join(dataDir, "wormhole", "wormholed.db"),
		Credentials: creds,
	}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/runtime/config/... -v`
Expected: PASS — all three tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/config
git commit -m "feat(runtime): local config and credential-profile loading"
```

---

### Task 3: `internal/runtime/localapi` — socket server proxying `wormhole.agent.whoami`

**Files:**
- Create: `internal/runtime/localapi/localapi.go`
- Create: `internal/runtime/localapi/localapi_test.go`

**Interfaces:**
- Consumes: `localstore.Store` (`CacheWhoAmI(ctx, WhoAmICache) error`) from Task 1. Does not import `internal/runtime/config` — takes plain strings so it stays independently testable.
- Produces: `localapi.New(socketPath, coordServerURL, token, projectID string, store *localstore.Store) (*localapi.Server, error)`, `(*Server).Serve(ctx context.Context) error`, `(*Server).Close() error`.

- [ ] **Step 1: Write the failing test**

```go
// internal/runtime/localapi/localapi_test.go
package localapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
)

// fakeCoordServer stands in for the Coordination Server's /mcp endpoint
// (docs/mcp-protocol.md §2-§4.1): decodes a tools/call JSON-RPC request,
// asserts the bearer token, returns a canned wormhole.agent.whoami result.
func fakeCoordServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		var params toolsCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("decode params: %v", err)
		}
		if params.Name != "wormhole.agent.whoami" {
			t.Fatalf("got tool %q, want wormhole.agent.whoami", params.Name)
		}
		out := whoAmIOutput{
			AgentID:      "agent-1",
			Owner:        "harley",
			Model:        "claude-sonnet-5",
			Capabilities: []string{"code"},
			ProjectID:    "project-1",
			Permissions:  []string{"read_kb"},
		}
		outRaw, _ := json.Marshal(out)
		resultRaw, _ := json.Marshal(toolCallResult{
			Content: []toolCallResultContent{{Type: "text", Text: string(outRaw)}},
		})
		resp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: resultRaw}
		json.NewEncoder(w).Encode(resp)
	}))
}

func TestServer_ProxiesWhoAmI(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()

	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	// Give Serve a moment to bind the socket.
	var conn net.Conn
	for i := 0; i < 50; i++ {
		conn, err = net.Dial("unix", socketPath)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial socket: %v", err)
	}
	defer conn.Close()

	reqRaw, _ := json.Marshal(localRequest{Tool: "wormhole.agent.whoami"})
	if _, err := conn.Write(append(reqRaw, '\n')); err != nil {
		t.Fatalf("write request: %v", err)
	}

	var resp localResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("got error response: %s", resp.Error)
	}
	var out whoAmIOutput
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if out.AgentID != "agent-1" || out.Owner != "harley" {
		t.Fatalf("got %+v", out)
	}

	cached, err := store.GetCachedWhoAmI(context.Background(), "agent-1")
	if err != nil {
		t.Fatalf("GetCachedWhoAmI: %v", err)
	}
	if cached.Model != "claude-sonnet-5" {
		t.Fatalf("cache not written: got %+v", cached)
	}
}

func TestServer_UnknownTool(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	var conn net.Conn
	for i := 0; i < 50; i++ {
		conn, err = net.Dial("unix", socketPath)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial socket: %v", err)
	}
	defer conn.Close()

	reqRaw, _ := json.Marshal(localRequest{Tool: "wormhole.task.create"})
	conn.Write(append(reqRaw, '\n'))

	var resp localResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == "" {
		t.Fatalf("want error for unsupported tool, got none")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/runtime/localapi/... -v`
Expected: FAIL — build error, `New`/`localRequest`/etc. undefined.

- [ ] **Step 3: Write the implementation**

```go
// internal/runtime/localapi/localapi.go

// Package localapi is wormholed's local API: a Unix-domain-socket server
// coding harnesses connect to (RFC-0003 §6.1). Wire shapes (localRequest/
// localResponse) are P1's own minimal protocol — one JSON request per
// connection, one JSON response, connection closed. Later phases (P2+)
// extend this to a persistent, multiplexed, subscription-capable protocol;
// P1 deliberately keeps it to the smallest thing that proves the chain.
//
// rpcRequest/rpcResponse/toolsCallParams/toolCallResult/whoAmIOutput mirror
// internal/mcp's JSON-RPC 2.0 wire shapes for talking to the Coordination
// Server. localapi cannot import internal/mcp (RFC-0003 §6.3 keeps
// internal/runtime/* and internal/mcp separate trees), so the wire
// contract is duplicated here, same as cmd/wormhole-cli/main.go already
// does for the same reason.
package localapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type toolCallResultContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolCallResult struct {
	Content []toolCallResultContent `json:"content"`
	IsError bool                    `json:"isError,omitempty"`
}

type whoAmIOutput struct {
	AgentID      string   `json:"agent_id"`
	Owner        string   `json:"owner"`
	Model        string   `json:"model"`
	Capabilities []string `json:"capabilities"`
	ProjectID    string   `json:"project_id"`
	Permissions  []string `json:"permissions"`
}

// localRequest is the P1 local-socket request: one tool call, no
// arguments needed yet (whoami takes none beyond project_id, which the
// Server already knows from its own config).
type localRequest struct {
	Tool string `json:"tool"`
}

// localResponse is the P1 local-socket response.
type localResponse struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Server is wormholed's local API socket server.
type Server struct {
	listener    net.Listener
	socketPath  string
	httpClient  *http.Client
	coordServer string
	token       string
	projectID   string
	store       *localstore.Store
}

// New binds the Unix domain socket at socketPath. Callers must call Serve
// to start accepting connections, and Close to release the socket.
func New(socketPath, coordServerURL, token, projectID string, store *localstore.Store) (*Server, error) {
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("localapi: listen on %s: %w", socketPath, err)
	}
	return &Server{
		listener:    ln,
		socketPath:  socketPath,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
		coordServer: coordServerURL,
		token:       token,
		projectID:   projectID,
		store:       store,
	}, nil
}

// Close stops accepting connections and releases the socket.
func (s *Server) Close() error {
	return s.listener.Close()
}

// Serve accepts connections until ctx is cancelled or the listener closes.
func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		s.listener.Close()
	}()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("localapi: accept: %w", err)
		}
		go s.handle(ctx, conn)
	}
}

func (s *Server) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}
	var req localRequest
	if err := json.Unmarshal(bytes.TrimSpace(line), &req); err != nil {
		writeResponse(conn, localResponse{Error: fmt.Sprintf("localapi: decode request: %v", err)})
		return
	}

	switch req.Tool {
	case "wormhole.agent.whoami":
		out, err := s.proxyWhoAmI(ctx)
		if err != nil {
			writeResponse(conn, localResponse{Error: err.Error()})
			return
		}
		outRaw, _ := json.Marshal(out)
		writeResponse(conn, localResponse{Result: outRaw})
	default:
		writeResponse(conn, localResponse{Error: fmt.Sprintf("localapi: unsupported tool %q in P1 walking skeleton", req.Tool)})
	}
}

func writeResponse(conn net.Conn, resp localResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	conn.Write(append(data, '\n'))
}

// proxyWhoAmI forwards wormhole.agent.whoami to the Coordination Server
// over its existing /mcp JSON-RPC 2.0 endpoint, then caches the result
// locally on success (RFC-0003 G4: local durability, best-effort here —
// a cache-write failure does not fail the caller's request).
func (s *Server) proxyWhoAmI(ctx context.Context) (whoAmIOutput, error) {
	argsRaw, _ := json.Marshal(map[string]string{"project_id": s.projectID})
	paramsRaw, err := json.Marshal(toolsCallParams{Name: "wormhole.agent.whoami", Arguments: argsRaw})
	if err != nil {
		return whoAmIOutput{}, fmt.Errorf("localapi: marshal params: %w", err)
	}
	reqBody, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call", Params: paramsRaw})
	if err != nil {
		return whoAmIOutput{}, fmt.Errorf("localapi: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.coordServer, "/")+"/mcp", bytes.NewReader(reqBody))
	if err != nil {
		return whoAmIOutput{}, fmt.Errorf("localapi: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+s.token)

	resp, err := s.httpClient.Do(httpReq)
	if err != nil {
		return whoAmIOutput{}, fmt.Errorf("localapi: call coordination server: %w", err)
	}
	defer resp.Body.Close()

	var rpcResp rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return whoAmIOutput{}, fmt.Errorf("localapi: decode coordination server response: %w", err)
	}
	if rpcResp.Error != nil {
		return whoAmIOutput{}, errors.New(rpcResp.Error.Message)
	}

	var result toolCallResult
	if err := json.Unmarshal(rpcResp.Result, &result); err != nil {
		return whoAmIOutput{}, fmt.Errorf("localapi: decode tools/call result: %w", err)
	}
	if len(result.Content) == 0 {
		return whoAmIOutput{}, errors.New("localapi: empty whoami result from coordination server")
	}
	if result.IsError {
		return whoAmIOutput{}, errors.New(result.Content[0].Text)
	}

	var out whoAmIOutput
	if err := json.Unmarshal([]byte(result.Content[0].Text), &out); err != nil {
		return whoAmIOutput{}, fmt.Errorf("localapi: decode whoami output: %w", err)
	}

	cacheErr := s.store.CacheWhoAmI(ctx, localstore.WhoAmICache{
		AgentID:      out.AgentID,
		Owner:        out.Owner,
		Model:        out.Model,
		Capabilities: out.Capabilities,
		ProjectID:    out.ProjectID,
		Permissions:  out.Permissions,
		CachedAt:     time.Now().UTC(),
	})
	_ = cacheErr // best-effort: cache-write failure must not fail the caller's request (P1 scope)

	return out, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/runtime/localapi/... -v`
Expected: PASS — `TestServer_ProxiesWhoAmI`, `TestServer_UnknownTool` both pass.

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/localapi
git commit -m "feat(runtime): local socket API proxying wormhole.agent.whoami"
```

---

### Task 4: `cmd/wormholed` — daemon entrypoint and end-to-end integration test

**Files:**
- Create: `cmd/wormholed/main.go`
- Create: `cmd/wormholed/wormholed.go`
- Create: `cmd/wormholed/wormholed_test.go`

**Interfaces:**
- Consumes: `config.Load` (Task 2), `localstore.Open` (Task 1), `localapi.New`/`Serve`/`Close` (Task 3).
- Produces: `Run(ctx context.Context, profileName string) error` (testable entrypoint; `main.go`'s `main()` is a thin wrapper calling this with `os.Args`-derived profile and OS signal-derived context).

- [ ] **Step 1: Write the failing test**

```go
// cmd/wormholed/wormholed_test.go
package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRun_EndToEndWhoAmI(t *testing.T) {
	coord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		type rpcRequest struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id,omitempty"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params,omitempty"`
		}
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)
		out := map[string]any{
			"agent_id": "agent-1", "owner": "harley", "model": "claude-sonnet-5",
			"capabilities": []string{"code"}, "project_id": "project-1", "permissions": []string{"read_kb"},
		}
		outRaw, _ := json.Marshal(out)
		result := map[string]any{"content": []map[string]string{{"type": "text", "text": string(outRaw)}}}
		resultRaw, _ := json.Marshal(result)
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": json.RawMessage(resultRaw)}
		json.NewEncoder(w).Encode(resp)
	}))
	defer coord.Close()

	home := t.TempDir()
	os.Setenv("HOME", home)
	defer os.Unsetenv("HOME")
	runDir := filepath.Join(home, "run")
	os.Setenv("XDG_RUNTIME_DIR", runDir)
	defer os.Unsetenv("XDG_RUNTIME_DIR")
	dataDir := filepath.Join(home, "data")
	os.Setenv("XDG_DATA_HOME", dataDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	credDir := filepath.Join(home, ".wormhole", "credentials")
	os.MkdirAll(credDir, 0o700)
	credData, _ := json.Marshal(map[string]string{
		"server": coord.URL, "project_id": "project-1", "agent_id": "agent-1", "token": "test-token",
	})
	os.WriteFile(filepath.Join(credDir, "default.json"), credData, 0o600)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, "default") }()

	socketPath := filepath.Join(runDir, "wormhole", "wormholed.sock")
	var conn net.Conn
	var err error
	for i := 0; i < 100; i++ {
		conn, err = net.Dial("unix", socketPath)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		cancel()
		t.Fatalf("dial socket: %v", err)
	}
	defer conn.Close()

	reqRaw, _ := json.Marshal(map[string]string{"tool": "wormhole.agent.whoami"})
	conn.Write(append(reqRaw, '\n'))

	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		cancel()
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != "" {
		cancel()
		t.Fatalf("got error: %s", resp.Error)
	}
	var out struct {
		AgentID string `json:"agent_id"`
	}
	json.Unmarshal(resp.Result, &out)
	if out.AgentID != "agent-1" {
		cancel()
		t.Fatalf("got agent_id %q, want agent-1", out.AgentID)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned error after cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not shut down after context cancel")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/wormholed/... -v`
Expected: FAIL — build error, `Run` undefined.

- [ ] **Step 3: Write the implementation**

```go
// cmd/wormholed/wormholed.go

// Run wires config, localstore, and localapi into one running daemon
// instance, and blocks until ctx is cancelled (RFC-0003 §6.1). Split from
// main() so it's directly testable without touching os.Args/os.Exit or
// OS signals.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/localapi"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
)

func Run(ctx context.Context, profileName string) error {
	cfg, err := config.Load(profileName)
	if err != nil {
		return fmt.Errorf("wormholed: load config: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(cfg.SocketPath), 0o700); err != nil {
		return fmt.Errorf("wormholed: create socket directory: %w", err)
	}
	// A stale socket file from a previous unclean shutdown would make
	// net.Listen fail with "address already in use"; remove it first.
	_ = os.Remove(cfg.SocketPath)

	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o700); err != nil {
		return fmt.Errorf("wormholed: create data directory: %w", err)
	}

	store, err := localstore.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("wormholed: open local store: %w", err)
	}
	defer store.Close()

	srv, err := localapi.New(cfg.SocketPath, cfg.Credentials.Server, cfg.Credentials.Token, cfg.Credentials.ProjectID, store)
	if err != nil {
		return fmt.Errorf("wormholed: start local api: %w", err)
	}
	defer srv.Close()

	return srv.Serve(ctx)
}
```

```go
// cmd/wormholed/main.go

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	profile := "default"
	if len(os.Args) > 1 {
		profile = os.Args[1]
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := Run(ctx, profile); err != nil {
		fmt.Fprintf(os.Stderr, "wormholed: %v\n", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/wormholed/... -v`
Expected: PASS — `TestRun_EndToEndWhoAmI` passes, proving the full harness→socket→wormholed→HTTP→Coordination-Server chain.

- [ ] **Step 5: Run the full test suite and build**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: build clean, vet clean, all tests (existing alpha suite + new `internal/runtime/*` + `cmd/wormholed`) pass.

- [ ] **Step 6: Commit**

```bash
git add cmd/wormholed
git commit -m "feat(wormholed): daemon entrypoint, P1 walking skeleton complete"
```

---

## P1 Exit Check

- [ ] `go build ./...`, `go vet ./...`, `go test ./...` all pass with output pasted into the completion report (mirrors `docs/architecture.md` §0.7/T4).
- [ ] Manual smoke: run `wormhole join --server <url> --project <id> --profile default` (existing CLI; the explicit `--profile default` is required — without it, `wormhole join` derives the filename as `<project>__<role>.json`, not `default.json`) against a running `wormhole-server`, then `go run ./cmd/wormholed default` and dial its socket by hand (e.g. `echo '{"tool":"wormhole.agent.whoami"}' | nc -U $XDG_RUNTIME_DIR/wormhole/wormholed.sock`) — confirms the skeleton against a *real* Coordination Server, not just the test's `httptest.Server` stand-in.
- [ ] Check off P1 in `ROADMAP-LOCAL-RUNTIME.md`, note completion date, kick off P2 detailed plan.
