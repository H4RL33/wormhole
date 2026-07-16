# Local Runtime Alpha Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the remaining P4/P6 roadmap gaps, finish the OpenCode connector, rewrite the README for the local-runtime era, and tag `v0.2.0-alpha` — closing out `ROADMAP-LOCAL-RUNTIME.md` P4 through P7.

**Architecture:** No new modules. Task 1 extends `internal/runtime/sync.Engine` with a second, faster ticker for latency-sensitive (high-priority) queue entries. Task 2 adds a per-namespace fixed-window rate limiter as a package-level var in `internal/mcp/sync.go`, checked at the top of each of the four `wormhole.sync.*` handlers. Task 3 finishes the already-started (uncommitted, currently non-compiling) `cmd/wormhole-cli/connect_opencode_test.go` by adding a `--target` flag to `runConnect` and an `opencode` branch alongside the existing `claude` branch. Task 4 rewrites `README.md`. Task 5 updates the roadmap checkboxes and tags the release.

**Tech Stack:** Go (stdlib only — `net/http`, `net/http/httptest`, `encoding/json`, `flag`, `os/exec`), `modernc.org/sqlite` (pure-Go driver, already a dependency).

## Global Constraints

- `internal/runtime/*` may not import `internal/mcp/*` and vice versa (RFC-0003 §6.3) — wire-shape structs are duplicated across the boundary, not shared. Task 1 stays entirely inside `internal/runtime/sync`.
- Every `wormhole.sync.*` handler already validates `namespace_id` and protocol `version` before doing work (P6 hardening, already landed) — Task 2's rate-limit check goes immediately after those two checks, not before (a malformed/version-mismatched request must still fail on that ground, not silently consume a rate-limit slot's rejection message instead).
- No new server-side audit table (Global Constraints from the P6 hardening plan, still binding) — not touched by this plan, no task needs one.
- Go 1.26.4+ (per README's stated prerequisite) — no new external dependencies in any task.
- `go build ./...`, `go vet ./...`, and `go test ./... -count=1` must be clean after every task's commit.

---

## Task 1: Sync Engine latency-sensitive batching bypass (P4 gap)

**Files:**
- Modify: `internal/runtime/sync/sync.go`
- Test: `internal/runtime/sync/sync_latency_test.go` (new file)

**Interfaces:**
- Consumes: existing `Engine` struct fields (`queueRepo`, `namespaceID`, `batchInterval`, `batchSize`, `shutdown`, `wg`), existing `QueueRepo.ListPending(ctx, namespaceID, limit) ([]QueueEntry, error)` (already orders `priority DESC, created_at ASC`, see `internal/runtime/sync/queue_repo.go:100-106`), existing `Engine.pushBatch(ctx) error`.
- Produces: `Config.LatencyCheckInterval time.Duration`, `Config.HighPriorityThreshold int` (both with defaults in `DefaultConfig()`), unexported `Engine.checkLatencySensitive(ctx context.Context) error` (callable directly from tests, also wired onto a second ticker inside `syncLoop`).

- [ ] **Step 1: Write the failing test**

```go
// internal/runtime/sync/sync_latency_test.go
package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestCheckLatencySensitive_HighPriorityPushesImmediately confirms a
// high-priority (>= HighPriorityThreshold) queue entry is pushed to the
// server by checkLatencySensitive without waiting for the full
// batchInterval to elapse (RFC-0003 §8.2 "latency-sensitive bypass",
// P4 roadmap gap).
func TestCheckLatencySensitive_HighPriorityPushesImmediately(t *testing.T) {
	qRepo, aRepo := setupTestRepos(t)
	defer qRepo.db.Close()

	var pushCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&pushCalls, 1)
		resultData := map[string]interface{}{
			"items_received": 1,
			"applied":        []interface{}{},
			"timestamp":      time.Now().UTC().Format(time.RFC3339),
			"version":        1,
		}
		writeFakeToolResult(w, resultData)
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	engine := New(srv.URL, "token", "ns-1", qRepo, aRepo, nil, nil, cfg)

	ctx := context.Background()
	payload := json.RawMessage(`{"title":"urgent"}`)
	if _, err := qRepo.Enqueue(ctx, "ns-1", "task", "task-1", "create", payload, cfg.HighPriorityThreshold); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if err := engine.checkLatencySensitive(ctx); err != nil {
		t.Fatalf("checkLatencySensitive: %v", err)
	}

	if got := atomic.LoadInt32(&pushCalls); got != 1 {
		t.Fatalf("push calls: got %d, want 1", got)
	}

	entries, err := qRepo.ListPending(ctx, "ns-1", 10)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected entry delivered (0 pending), got %d pending", len(entries))
	}
}

// TestCheckLatencySensitive_LowPriorityDoesNotPush confirms an entry below
// HighPriorityThreshold is left for the normal batchInterval ticker instead
// of being pushed immediately.
func TestCheckLatencySensitive_LowPriorityDoesNotPush(t *testing.T) {
	qRepo, aRepo := setupTestRepos(t)
	defer qRepo.db.Close()

	var pushCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&pushCalls, 1)
		writeFakeToolResult(w, map[string]interface{}{
			"items_received": 1, "applied": []interface{}{}, "timestamp": time.Now().UTC().Format(time.RFC3339), "version": 1,
		})
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	engine := New(srv.URL, "token", "ns-1", qRepo, aRepo, nil, nil, cfg)

	ctx := context.Background()
	payload := json.RawMessage(`{"title":"routine"}`)
	if _, err := qRepo.Enqueue(ctx, "ns-1", "task", "task-1", "create", payload, cfg.HighPriorityThreshold-1); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if err := engine.checkLatencySensitive(ctx); err != nil {
		t.Fatalf("checkLatencySensitive: %v", err)
	}

	if got := atomic.LoadInt32(&pushCalls); got != 0 {
		t.Fatalf("push calls: got %d, want 0 (low priority must wait for batchInterval)", got)
	}
}

// writeFakeToolResult writes a JSON-RPC 2.0 tools/call success envelope
// wrapping resultData, matching callSyncToolWithResult's expected decode
// shape (content[0].text is the marshalled tool output).
func writeFakeToolResult(w http.ResponseWriter, resultData interface{}) {
	dataBytes, _ := json.Marshal(resultData)
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"result": map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": string(dataBytes)},
			},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/runtime/sync/... -run TestCheckLatencySensitive -v`
Expected: FAIL with `undefined: engine.checkLatencySensitive` (or `Config.HighPriorityThreshold` undefined) — compile error, not a runtime failure.

- [ ] **Step 3: Write minimal implementation**

In `internal/runtime/sync/sync.go`, extend `Config` and `DefaultConfig`:

```go
// Config holds tunable sync batching parameters (RFC-0003 §8.2).
type Config struct {
	BatchInterval         time.Duration // time-based batching threshold
	BatchSize             int           // queue-size batching threshold
	LatencyCheckInterval  time.Duration // how often to check for high-priority entries needing an immediate push
	HighPriorityThreshold int           // queue entries with Priority >= this bypass BatchInterval
}

// DefaultConfig returns conservative batching defaults: 5 sec interval, 50
// item batch, high-priority entries (priority >= 2) checked every 500ms
// instead of waiting the full 5 sec.
func DefaultConfig() Config {
	return Config{
		BatchInterval:         5 * time.Second,
		BatchSize:             50,
		LatencyCheckInterval:  500 * time.Millisecond,
		HighPriorityThreshold: 2,
	}
}
```

Add two fields to `Engine` and wire them in `New`:

```go
type Engine struct {
	httpClient            *http.Client
	coordServer           string
	token                 string
	namespaceID           string
	queueRepo             *QueueRepo
	auditRepo             *AuditRepo
	taskRepo              *localstore.TaskRepo
	kbRepo                *localstore.KBRepo
	mu                    sync.Mutex
	lastSyncTime          time.Time
	batchInterval         time.Duration
	batchSize             int
	latencyCheckInterval  time.Duration
	highPriorityThreshold int
	shutdown              chan struct{}
	wg                    sync.WaitGroup
}
```

```go
func New(coordServerURL, token, namespaceID string, queueRepo *QueueRepo, auditRepo *AuditRepo, taskRepo *localstore.TaskRepo, kbRepo *localstore.KBRepo, cfg Config) *Engine {
	return &Engine{
		httpClient:            &http.Client{Timeout: 30 * time.Second},
		coordServer:           coordServerURL,
		token:                 token,
		namespaceID:           namespaceID,
		queueRepo:             queueRepo,
		auditRepo:             auditRepo,
		taskRepo:              taskRepo,
		kbRepo:                kbRepo,
		batchInterval:         cfg.BatchInterval,
		batchSize:             cfg.BatchSize,
		latencyCheckInterval:  cfg.LatencyCheckInterval,
		highPriorityThreshold: cfg.HighPriorityThreshold,
		shutdown:              make(chan struct{}),
	}
}
```

Add the second ticker to `syncLoop`:

```go
func (e *Engine) syncLoop(ctx context.Context) {
	defer e.wg.Done()

	ticker := time.NewTicker(e.batchInterval)
	defer ticker.Stop()

	latencyTicker := time.NewTicker(e.latencyCheckInterval)
	defer latencyTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-e.shutdown:
			return
		case <-ticker.C:
			// Time-based batch trigger: push any pending work.
			if err := e.pushBatch(ctx); err != nil {
				// Best-effort: log error and continue. The batch remains queued
				// for retry on the next interval.
				_ = err
			}
		case <-latencyTicker.C:
			if err := e.checkLatencySensitive(ctx); err != nil {
				_ = err
			}
		}
	}
}

// checkLatencySensitive peeks the highest-priority pending entry and, if it
// meets highPriorityThreshold, pushes immediately rather than waiting for
// the next batchInterval tick (RFC-0003 §8.2 latency-sensitive bypass).
// ListPending already orders priority DESC, so the first row is the one
// that matters.
func (e *Engine) checkLatencySensitive(ctx context.Context) error {
	e.mu.Lock()
	entries, err := e.queueRepo.ListPending(ctx, e.namespaceID, 1)
	e.mu.Unlock()
	if err != nil {
		return fmt.Errorf("sync: check latency-sensitive: list pending: %w", err)
	}
	if len(entries) == 0 || entries[0].Priority < e.highPriorityThreshold {
		return nil
	}
	return e.pushBatch(ctx)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/runtime/sync/... -v`
Expected: PASS (all tests in the package, not just the new ones — confirms the `Engine`/`Config` field additions didn't break `TestEngineNew`, `TestDefaultConfig`, etc.)

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/sync/sync.go internal/runtime/sync/sync_latency_test.go
git commit -m "feat(sync): add latency-sensitive batching bypass for high-priority queue entries"
```

---

## Task 2: Rate limiting on `wormhole.sync.*` handlers (P6 gap)

**Files:**
- Modify: `internal/mcp/sync.go`
- Test: `internal/mcp/sync_ratelimit_test.go` (new file)

**Interfaces:**
- Consumes: existing `validateNamespace(namespaceID, projectID string) error`, existing `SyncProtocolVersion` const, existing four `Handler` closures inside `BootstrapTool`/`IncrementalPullTool`/`IncrementalPushTool`/`ConflictReportTool`.
- Produces: package-level `var globalSyncRateLimiter *syncRateLimiter`, method `(*syncRateLimiter) allow(namespaceID string, now time.Time) bool`, constructor `newSyncRateLimiter(limit int, window time.Duration) *syncRateLimiter`.

- [ ] **Step 1: Write the failing test**

```go
// internal/mcp/sync_ratelimit_test.go
package mcp

import (
	"testing"
	"time"
)

// TestSyncRateLimiter_AllowsUpToLimit confirms exactly `limit` calls within
// `window` succeed and the next one is rejected (P6 hardening: rate
// limiting on wormhole.sync.* handlers, previously deferred to beta).
func TestSyncRateLimiter_AllowsUpToLimit(t *testing.T) {
	rl := newSyncRateLimiter(3, time.Minute)
	now := time.Now()

	for i := 0; i < 3; i++ {
		if !rl.allow("ns-1", now) {
			t.Fatalf("call %d: expected allowed", i)
		}
	}
	if rl.allow("ns-1", now) {
		t.Fatalf("4th call within window: expected rejected")
	}
}

// TestSyncRateLimiter_NamespacesIndependent confirms one namespace hitting
// its limit does not affect another namespace's budget.
func TestSyncRateLimiter_NamespacesIndependent(t *testing.T) {
	rl := newSyncRateLimiter(1, time.Minute)
	now := time.Now()

	if !rl.allow("ns-1", now) {
		t.Fatalf("ns-1 first call: expected allowed")
	}
	if rl.allow("ns-1", now) {
		t.Fatalf("ns-1 second call: expected rejected")
	}
	if !rl.allow("ns-2", now) {
		t.Fatalf("ns-2 first call: expected allowed despite ns-1 exhausted")
	}
}

// TestSyncRateLimiter_WindowExpires confirms a call outside the window no
// longer counts against the limit.
func TestSyncRateLimiter_WindowExpires(t *testing.T) {
	rl := newSyncRateLimiter(1, time.Minute)
	now := time.Now()

	if !rl.allow("ns-1", now) {
		t.Fatalf("first call: expected allowed")
	}
	later := now.Add(2 * time.Minute)
	if !rl.allow("ns-1", later) {
		t.Fatalf("call after window expiry: expected allowed")
	}
}

// TestBootstrapTool_RateLimitRejectsCleanly confirms the handler itself
// (not just the limiter struct in isolation) returns a clean error once the
// per-namespace budget is exhausted, restoring the limiter afterward so
// this test doesn't leak state into others in the package.
func TestBootstrapTool_RateLimitRejectsCleanly(t *testing.T) {
	prev := globalSyncRateLimiter
	globalSyncRateLimiter = newSyncRateLimiter(1, time.Minute)
	defer func() { globalSyncRateLimiter = prev }()

	tasksStore, kbStore, eventsStore, projectID := newSyncToolTestStores(t)
	tool := BootstrapTool(tasksStore, kbStore, eventsStore)

	in := BootstrapInput{NamespaceID: projectID, Version: SyncProtocolVersion}
	argsFirst := mustMarshal(t, in)
	if _, err := tool.Handler(contextBackground(), nil, projectID, argsFirst); err != nil {
		t.Fatalf("first call: expected success, got %v", err)
	}
	if _, err := tool.Handler(contextBackground(), nil, projectID, argsFirst); err == nil {
		t.Fatalf("second call within window: expected rate-limit rejection, got nil error")
	}
}
```

`newSyncToolTestStores`, `mustMarshal`, and `contextBackground` are small helpers this test needs; check `internal/mcp/sync_test.go` first — if it already has equivalent setup helpers (it very likely does, since it already builds `tasksStore`/`kbStore`/`eventsStore` for the existing bootstrap/pull/push/conflict tests), reuse those exact helper names instead of adding new ones. Only add `newSyncToolTestStores`/`mustMarshal`/`contextBackground` to `sync_ratelimit_test.go` if `sync_test.go` truly has no equivalent — grep `internal/mcp/sync_test.go` for `func newSyncTool`, `func mustMarshal`, `context.Background()` before writing this step for real.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/mcp/... -run TestSyncRateLimiter -v`
Expected: FAIL with `undefined: newSyncRateLimiter`.

- [ ] **Step 3: Write minimal implementation**

Add near the top of `internal/mcp/sync.go` (after the existing `SyncAuditChannelID` const, before `validateNamespace`):

```go
import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/H4RL33/wormhole/internal/core/events"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/core/kb"
	"github.com/H4RL33/wormhole/internal/core/tasks"
)

// syncRateLimiter enforces a simple fixed-window per-namespace request cap
// on wormhole.sync.* handlers (P6 minimal hardening — RFC-0003 §10; this
// was explicitly deferred to the beta pass in ROADMAP-LOCAL-RUNTIME.md and
// is now closed for the alpha tag).
type syncRateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string][]time.Time
}

func newSyncRateLimiter(limit int, window time.Duration) *syncRateLimiter {
	return &syncRateLimiter{limit: limit, window: window, hits: make(map[string][]time.Time)}
}

// allow reports whether namespaceID may make another sync call at time now,
// recording the call if so. Timestamps older than window are pruned first.
func (r *syncRateLimiter) allow(namespaceID string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	cutoff := now.Add(-r.window)
	kept := make([]time.Time, 0, len(r.hits[namespaceID]))
	for _, t := range r.hits[namespaceID] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= r.limit {
		r.hits[namespaceID] = kept
		return false
	}
	r.hits[namespaceID] = append(kept, now)
	return true
}

// globalSyncRateLimiter is shared across every wormhole.sync.* handler
// instance the process constructs (there is one Coordination Server process
// per deployment, so one limiter is correct — not per-Tool-construction
// state). 30 calls/minute/namespace comfortably exceeds the default sync
// engine's busiest case (BatchInterval=5s plus latency-sensitive bypass
// checks every 500ms would only call incremental_push, never bootstrap or
// conflict_report, at that rate) while still bounding abuse.
var globalSyncRateLimiter = newSyncRateLimiter(30, time.Minute)
```

Then, in each of the four handlers, add the check immediately after the existing version check (do not reorder ahead of `validateNamespace`/version checks — those must still win on their own grounds). Example for `BootstrapTool`:

```go
			if in.Version != SyncProtocolVersion {
				return nil, fmt.Errorf("mcp: wormhole.sync.bootstrap: unsupported protocol version %d (expected %d)", in.Version, SyncProtocolVersion)
			}
			if !globalSyncRateLimiter.allow(in.NamespaceID, time.Now()) {
				return nil, fmt.Errorf("mcp: wormhole.sync.bootstrap: rate limit exceeded for namespace %q", in.NamespaceID)
			}
```

Repeat the same two-line addition (with the tool's own name substituted into both the doc-comment-style function name in the error string) in `IncrementalPullTool`, `IncrementalPushTool`, and `ConflictReportTool`, each directly after their existing `if in.Version != SyncProtocolVersion { ... }` block.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/mcp/... -v`
Expected: PASS (full package, confirming the four handler edits didn't break any of the existing bootstrap/pull/push/conflict-report tests already in `sync_test.go`).

- [ ] **Step 5: Commit**

```bash
git add internal/mcp/sync.go internal/mcp/sync_ratelimit_test.go
git commit -m "feat(mcp): add per-namespace rate limiting to wormhole.sync.* handlers"
```

---

## Task 3: Finish the OpenCode connector (`wormhole connect --target opencode`)

**Files:**
- Modify: `cmd/wormhole-cli/main.go`
- Modify: `cmd/wormhole-cli/connect_opencode_test.go` (already exists, untracked, currently fails to compile — add the two missing test helpers to it)

**Interfaces:**
- Consumes: existing `runConnect(args []string, stdout, stderr io.Writer) int`, existing `doRegister`, `resolveCredentialsPath`, `writeCredentials`, `registerAgentInput`, `credentials` types (all already defined earlier in `main.go`), existing test helpers `fakeServer`, `callResponse`, `searchArticlesInput`, `searchArticlesOutput` (defined in `cmd/wormhole-cli/main_test.go`).
- Produces: new `--target` flag on `runConnect` (values `"claude"` default, `"opencode"`), new `--opencode-config` flag, `resolveOpenCodeConfigPath(explicit, cwd string) (string, error)`, `bytesBufferHolder` struct, `contains(s, substr string) bool` (both test-only, added to `connect_opencode_test.go`).

First, confirm the exact expectations already locked in by the existing (uncompiled) test file — read `cmd/wormhole-cli/connect_opencode_test.go` in full before writing code. It specifies:
- `--target opencode --opencode-config <path>` writes a JSON file with `$schema: "https://opencode.ai/config.json"` (only when the file doesn't already have a `$schema`), and `mcp.<connector-name>` = `{"type": "remote", "url": "<server>/mcp", "enabled": true, "headers": {"Authorization": "Bearer <token>"}}`.
- Merging into an existing config file preserves unrelated top-level keys, other `mcp.*` entries, and an existing `$schema`.
- `--connector-name` controls the `mcp.<name>` key, same as Claude's positional connector name.
- `--target bogus-ide` exits 2 with `--target` in stderr, before any network call.
- `resolveOpenCodeConfigPath("/explicit/path", cwd)` returns the explicit path unchanged.
- `resolveOpenCodeConfigPath("", sub)` walks up from `sub` looking for `opencode.json`/`opencode.jsonc` next to (or above, up to and including) a `.git` directory; if found, returns that path.
- `resolveOpenCodeConfigPath("", sub)` with no project config found falls back to `$HOME/.config/opencode/opencode.json`.

- [ ] **Step 1: Add the two missing test helpers to the existing test file**

Append to `cmd/wormhole-cli/connect_opencode_test.go` (the file's own trailing comment already anticipates this — "see init below"):

```go
// bytesBufferHolder wraps a *bytes.Buffer behind a field named buf so this
// file's stdout/stderr locals read the same way main_test.go's do, without
// re-importing "bytes" and "strings" redundantly at the top of this file.
type bytesBufferHolder struct {
	buf *bytesBuffer
}

// contains is a tiny strings.Contains wrapper kept local to this file for
// the same reason as bytesBufferHolder above.
func contains(s, substr string) bool {
	return stringsContains(s, substr)
}
```

Check `cmd/wormhole-cli/main_test.go`'s imports first: if it already imports `"bytes"` and `"strings"` as a normal Go file (it does, to build `fakeServer`), then the simplest correct fix — skip the wrapper types above entirely — is to change `connect_opencode_test.go`'s own top-level `var stdout, stderr bytesBufferHolder` declarations to plain `var stdout, stderr bytes.Buffer` and every `stdout.buf.String()` to `stdout.String()`, and every `contains(x, y)` call to `strings.Contains(x, y)`, adding `"bytes"` and `"strings"` to this file's own import block. That avoids inventing wrapper types purely to dodge two stdlib imports. Prefer this simpler fix — only fall back to the wrapper-type version above if `bytes.Buffer`/`strings.Contains` somehow collide with something else already in package `main`'s test files (they don't, based on `main_test.go`'s existing usage).

- [ ] **Step 2: Run test to verify it still fails, now on the real gap**

Run: `go test ./cmd/wormhole-cli/... -run TestRunConnect_OpenCode -v`
Expected: FAIL with `undefined: resolveOpenCodeConfigPath` and/or `flag provided but not defined: -target` — the helper-import errors from before are gone, compile fails only on the missing production code now.

- [ ] **Step 3: Write minimal implementation**

In `cmd/wormhole-cli/main.go`, add flags to `runConnect` (after the existing `claudeBin` flag declaration at line 643):

```go
	target := fs.String("target", "claude", "connector target: \"claude\" or \"opencode\"")
	openCodeConfig := fs.String("opencode-config", "", "path to the OpenCode config file (default: nearest opencode.json/.jsonc walking up to .git, else $HOME/.config/opencode/opencode.json)")
```

Right after the existing `if *server == "" || *project == ""` validation block, validate `--target` before any network call:

```go
	if *target != "claude" && *target != "opencode" {
		fmt.Fprintf(stderr, "wormhole connect: --target: unknown value %q (must be \"claude\" or \"opencode\")\n", *target)
		fs.Usage()
		return 2
	}
```

After the existing `doRegister`/`writeCredentials`/confirmation-message block (right before the current `mcpURL := ...` line that starts the Claude-specific wiring), branch on target:

```go
	mcpURL := strings.TrimRight(*server, "/") + "/mcp"

	if *target == "opencode" {
		return runConnectOpenCode(*openCodeConfig, *connectorName, mcpURL, out.Token, stdout, stderr)
	}

	if _, lookErr := exec.LookPath(*claudeBin); lookErr != nil {
```

(The existing Claude-path code from `if _, lookErr := exec.LookPath(*claudeBin); lookErr != nil {` through the final `return 0` stays exactly as-is — this only inserts the new branch above it.)

Add the new functions after `runConnect` (before `runWhoami`):

```go
// runConnectOpenCode implements the --target opencode branch of `wormhole
// connect`: it writes (or merges into) an OpenCode config file's mcp.<name>
// entry, per the opencode.ai/config.json schema (confirmed shape: $schema,
// mcp.<name>.{type, url, enabled, headers.Authorization}).
func runConnectOpenCode(explicitPath, connectorName, mcpURL, token string, stdout, stderr io.Writer) int {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "wormhole connect: %v\n", err)
		return 1
	}
	configPath, err := resolveOpenCodeConfigPath(explicitPath, cwd)
	if err != nil {
		fmt.Fprintf(stderr, "wormhole connect: %v\n", err)
		return 1
	}

	cfg := map[string]any{}
	if data, readErr := os.ReadFile(configPath); readErr == nil {
		if jsonErr := json.Unmarshal(data, &cfg); jsonErr != nil {
			fmt.Fprintf(stderr, "wormhole connect: parse existing %s: %v\n", configPath, jsonErr)
			return 1
		}
	} else if !os.IsNotExist(readErr) {
		fmt.Fprintf(stderr, "wormhole connect: read %s: %v\n", configPath, readErr)
		return 1
	}

	if _, ok := cfg["$schema"]; !ok {
		cfg["$schema"] = "https://opencode.ai/config.json"
	}

	mcp, ok := cfg["mcp"].(map[string]any)
	if !ok {
		mcp = map[string]any{}
	}
	mcp[connectorName] = map[string]any{
		"type":    "remote",
		"url":     mcpURL,
		"enabled": true,
		"headers": map[string]any{
			"Authorization": "Bearer " + token,
		},
	}
	cfg["mcp"] = mcp

	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		fmt.Fprintf(stderr, "wormhole connect: create config directory: %v\n", err)
		return 1
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "wormhole connect: encode config: %v\n", err)
		return 1
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		fmt.Fprintf(stderr, "wormhole connect: write %s: %v\n", configPath, err)
		return 1
	}

	fmt.Fprintf(stdout, "Connector %q written to wormhole config in %s.\n", connectorName, configPath)
	return 0
}

// resolveOpenCodeConfigPath decides which OpenCode config file to write.
// An explicit path always wins. Otherwise it walks up from cwd looking for
// opencode.json or opencode.jsonc, stopping (inclusive) at the first
// directory containing .git; if none is found by then, it falls back to
// the global $HOME/.config/opencode/opencode.json.
func resolveOpenCodeConfigPath(explicit, cwd string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}

	dir := cwd
	for {
		for _, name := range []string{"opencode.json", "opencode.jsonc"} {
			candidate := filepath.Join(dir, name)
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve opencode config path: %w", err)
	}
	return filepath.Join(home, ".config", "opencode", "opencode.json"), nil
}
```

Check `cmd/wormhole-cli/main.go`'s existing import block for `"encoding/json"`, `"os"`, `"path/filepath"` — `main.go` almost certainly already imports `"os"` and likely `"encoding/json"` (used elsewhere for credentials) but may not import `"path/filepath"` yet; add whichever of the three are missing.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/wormhole-cli/... -v`
Expected: PASS (full package — confirms the new `--target`/`opencode-config` flags and the inserted branch didn't change any existing Claude-path test's behavior, since that code path is untouched).

- [ ] **Step 5: Commit**

```bash
git add cmd/wormhole-cli/main.go cmd/wormhole-cli/connect_opencode_test.go
git commit -m "feat(cli): implement wormhole connect --target opencode"
```

---

## Task 4: README rewrite for the local-runtime era

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: nothing code-level. Consumes facts established by Tasks 1-3 and the existing `wormhole join`/`wormhole connect` CLI surface in `cmd/wormhole-cli/main.go`, and `cmd/wormholed`'s existence (`cmd/wormholed/wormholed.go`).
- Produces: nothing consumed by later tasks except Task 5's tag message referencing "README updated".

- [ ] **Step 1: Amend the connector-policy paragraph**

In the "Being Open-Source" section, replace:

```
To that extent, we will officially provide Claude Code connectors only. Neither Gemini nor OpenAI models will ever see official support in Wormhole.
```

with:

```
To that extent, we officially provide connectors for harnesses that do not lock an agent to a single proprietary model provider by design — currently Claude Code and OpenCode. We will not officially support connectors whose entire purpose is wiring up a specific provider we've chosen not to endorse (e.g. a "Gemini connector" or "OpenAI connector" as such), though nothing stops a harness-level connector like OpenCode's from being pointed at any model the user chooses — that choice is the user's, not Wormhole's to gate.
```

This resolves the contradiction identified in this plan's motivating conversation: the OpenCode connector added in Task 3 is a harness integration (OpenCode is model-agnostic), not a provider endorsement, so it does not conflict with the project's stated refusal to endorse specific proprietary model providers.

- [ ] **Step 2: Update the Status section**

Replace:

```
**Alpha Release (v0.1.0-alpha)**. Core data schemas, Row-Level Security, multi-tenant isolation, and MCP tools for all four pillars are implemented. See [ROADMAP.md](ROADMAP.md) for future plans.
```

with:

```
**Local Runtime Alpha (v0.2.0-alpha)**. Core data schemas, Row-Level Security, multi-tenant isolation, and MCP tools for all four pillars are implemented (see [ROADMAP.md](ROADMAP.md)), plus the local-first runtime layer: `wormholed` daemon, SQLite replica, event bus/scheduler, sync engine with offline-write/reconnect, and multi-org bootstrap (see [ROADMAP-LOCAL-RUNTIME.md](ROADMAP-LOCAL-RUNTIME.md)). Offline/reconnect kill-network test suite and a comprehensive cross-repo isolation audit remain deferred to the beta pass — see that roadmap's P6 section for exact scope.
```

- [ ] **Step 3: Rewrite the Quickstart section**

Replace the entire "## Quickstart / Local Demo" section (from `## Quickstart / Local Demo` through the line before `## Design Documents`) with:

```markdown
## Quickstart / Local Demo

Follow this guide to spin up a local instance of `wormhole-server` (the Coordination Server), run `wormholed` (the local daemon each agent talks to), and connect a coding harness to it.

### Prerequisites

- Go 1.26.4+
- Docker & Docker Compose
- PostgreSQL client (`psql`) installed locally (optional, for manual queries)
- Claude Code and/or OpenCode installed, if you intend to connect one of those harnesses

### 1. Run PostgreSQL with pgvector

Wormhole uses a Postgres database with pgvector for state and semantic search. Start it via Docker Compose:

```bash
docker compose up -d
```

This runs PostgreSQL at `127.0.0.1:5432` with user/password `wormhole` and database `wormhole`.

### 2. Install Migration Tooling & Run Migrations

Database schema management is handled via `golang-migrate`.

Install the `migrate` CLI:
```bash
go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
```

Apply all migrations:
```bash
migrate -path migrations -database "postgres://wormhole:wormhole@localhost:5432/wormhole?sslmode=disable" up
```

### 3. Create a Demo Project

Wormhole requires a Project to scope all tokens, tasks, and events. Run the following command to insert a demo project in the database:

```bash
docker compose exec db psql -U wormhole -d wormhole -c \
  "INSERT INTO projects (id, name, owner) VALUES ('00000000-0000-0000-0000-000000000001', 'Demo Project', 'demo-owner');"
```

### 4. Run the Coordination Server

Build and run `wormhole-server`. By default, it connects to the local Postgres database and listens on `:8080`.

```bash
go run cmd/wormhole-server/main.go
```

### 5. Run `wormholed`

`wormholed` is the local daemon a coding harness talks to over a Unix domain socket — it proxies to the Coordination Server and caches state in a local SQLite replica so reads keep working offline. Install it once:

```bash
go install ./cmd/wormholed
```

Then run it (it reads its org connection config from `$XDG_CONFIG_HOME/wormhole/` or `~/.config/wormhole/` by default — see `internal/runtime/config` if you need to point it elsewhere):

```bash
wormholed
```

Leave it running in its own terminal/session; every command below talks to it.

### 6. Connect a harness

`wormhole connect` registers a fresh agent identity (a Passport), then wires the issued MCP token into your harness of choice. Install the CLI:

```bash
go install ./cmd/wormhole-cli
```

**Claude Code:**

```bash
wormhole-cli connect \
  --server http://localhost:8080 \
  --project 00000000-0000-0000-0000-000000000001 \
  --owner "demo-owner" \
  --model "claude-sonnet-5" \
  --permissions "task.create,kb.write" \
  --target claude
```

This shells out to the `claude` CLI (`claude mcp add/remove`) to register the connector. Run `/mcp` inside Claude Code afterward to reconnect.

**OpenCode:**

```bash
wormhole-cli connect \
  --server http://localhost:8080 \
  --project 00000000-0000-0000-0000-000000000001 \
  --owner "demo-owner" \
  --model "opencode" \
  --permissions "task.create,kb.write" \
  --target opencode
```

This writes (or merges into) an `opencode.json`/`opencode.jsonc` config — by default the nearest one found walking up from your current directory to your project's `.git` root, falling back to `~/.config/opencode/opencode.json` if none exists. Pass `--opencode-config <path>` to target a specific file instead.

Either connector accepts `--connector-name <name>` to register under a name other than the default `wormhole`.

### 7. Join and verify

`wormhole join` performs the same registration, then runs a KB-sync/self-introduction/task-summary handshake so an agent's first turn already has context:

```bash
wormhole-cli join \
  --server http://localhost:8080 \
  --project 00000000-0000-0000-0000-000000000001 \
  --owner "demo-owner" \
  --model "claude-sonnet-5" \
  --capabilities "code_edit,run_tests" \
  --repositories "github.com/H4RL33/wormhole" \
  --roles "developer" \
  --permissions "task.create,kb.write"
```

Credentials are written under `~/.wormhole/credentials/` (see `wormhole-cli whoami` and `wormhole-cli profile list` to inspect stored profiles).
```

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs(readme): rewrite quickstart for wormholed + connect (Claude, OpenCode), amend connector policy"
```

---

## Task 5: Close roadmap checkboxes and tag the release

**Files:**
- Modify: `ROADMAP-LOCAL-RUNTIME.md`

**Interfaces:**
- Consumes: completed Tasks 1-4 (this task only records that they happened and closes the roadmap).
- Produces: an annotated git tag `v0.2.0-alpha` (local only — do not push without asking the user first; tagging and pushing a tag is a visible, semi-permanent action per this project's operating norms).

- [ ] **Step 1: Update P4's batching line**

In `ROADMAP-LOCAL-RUNTIME.md`, change:

```
- [ ] Batching: time/queue-size/priority criteria, latency-sensitive bypass — time- and size-based batch triggers and priority-ordered dequeue implemented (`internal/runtime/sync.Engine`, `QueueRepo.ListPending`); no explicit latency-sensitive bypass path found, left unchecked
- [ ] P4 review/demo, kick off P5
```

to:

```
- [x] Batching: time/queue-size/priority criteria, latency-sensitive bypass — time- and size-based batch triggers, priority-ordered dequeue, and a latency-sensitive bypass (`Engine.checkLatencySensitive`, checked on a 500ms ticker independent of the 5s `batchInterval`, pushes immediately when the highest-priority pending entry's `Priority >= HighPriorityThreshold`) all implemented in `internal/runtime/sync.Engine`.
- [x] P4 review/demo, kick off P5 — completed 2026-07-15. `go build`/`go vet`/`go test ./internal/runtime/sync/...` clean.
```

- [ ] **Step 2: Update P5's review/demo line**

Change:

```
- [ ] P5 review/demo, kick off P6
```

to:

```
- [x] P5 review/demo, kick off P6 — completed 2026-07-15. All P5 exit-criteria items above were already checked; this closes the phase's own review bullet, no code gap found.
```

- [ ] **Step 3: Update P6's rate-limiting and closing lines**

Change:

```
- [ ] Rate limiting on `wormhole.sync.*` handlers — **deferred to beta**
```

to:

```
- [x] Rate limiting on `wormhole.sync.*` handlers — per-namespace fixed-window limiter (`internal/mcp.syncRateLimiter`, 30 calls/minute/namespace) added ahead of the original beta deferral; checked in all four `wormhole.sync.*` handlers immediately after the existing namespace/version validation.
```

Leave the offline/reconnect test suite and comprehensive isolation-gap audit bullets unchecked (both remain genuinely deferred — this plan does not touch either). Change the closing bullet:

```
- [ ] P6 review/demo, kick off P7
```

to:

```
- [x] P6 review/demo, kick off P7 — completed 2026-07-15. Rate limiting closed; offline/reconnect suite and isolation-gap audit remain explicitly deferred to beta (unchanged from the 2026-07-15 minimal-hardening note above).
```

- [ ] **Step 4: Update P7's tag-release line**

Change:

```
- [ ] Tag release
- [ ] Launch demo
```

to:

```
- [x] Tag release — `v0.2.0-alpha`, tagged 2026-07-15. README updated (connector policy, quickstart for `wormholed`/`connect`/Claude/OpenCode), P4 batching bypass and P6 rate limiting closed.
- [ ] Launch demo — not part of this plan; still open.
```

- [ ] **Step 5: Commit the roadmap update**

```bash
git add ROADMAP-LOCAL-RUNTIME.md
git commit -m "docs(roadmap): close P4/P5/P6 review gates, record P7 tag-release"
```

- [ ] **Step 6: Full-suite verification before tagging**

Run: `go build ./... && go vet ./... && go test ./... -count=1`
Expected: all clean, zero failures. Do not proceed to Step 7 if anything fails — fix and re-commit first.

- [ ] **Step 7: Tag the release (local only)**

```bash
git tag -a v0.2.0-alpha -m "Local runtime alpha: wormholed daemon, sync engine, multi-org bootstrap, OpenCode connector"
```

Stop here. Pushing the tag (`git push origin v0.2.0-alpha`) is a visible, shared-state action — surface the tag to the user and ask before pushing it, per this project's standing operating norm on hard-to-reverse/visible actions.
