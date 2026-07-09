# wormhole-cli connect + wormhole-server activity logging Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Two independent developer-experience fixes surfaced by Chapter 4's live connector test: (1) a `wormhole connect` CLI subcommand that joins a project and wires the issued token straight into Claude Code's MCP connector config (`claude mcp add -H`), eliminating the manual token-extraction/header-quoting steps that broke live during that test (a fish-shell nested-quoting bug produced an empty `Authorization: Bearer` header); (2) per-request activity logging on `wormhole-server`'s stdout, since it currently only logs its own startup line and gives no visibility into what's happening during a demo or test run.

**Architecture:** `wormhole connect` is a new `cmd/wormhole-cli` subcommand, sibling to `join`. It performs the same `wormhole.agent.register` call `join` does (via the existing `doRegister`/`writeCredentials` helpers — no duplication of that logic), then shells out to the `claude` CLI binary (`os/exec`) to remove any stale connector registration and re-add it with the issued token as a bearer header. It does not run `join`'s KB-sync/self-introduction/task-summary steps — those are `join`'s concern for an already-connected agent identity, not `connect`'s concern of wiring up the transport. The server logging is a small `http.Handler` middleware in `cmd/wormhole-server` (new file `logging.go`) that wraps the existing `mux`: it records method, path, status, and latency for every request, and additionally decodes the JSON-RPC `method` (and, for `tools/call`, the tool name) for requests to `/mcp` so operators can watch tool calls scroll by during a demo — this requires buffering and restoring the request body, since the JSON-RPC handler downstream also needs to read it.

**Tech Stack:** Go stdlib only (`os/exec`, `net/http`, `encoding/json`, `log`) — no new dependencies.

## Global Constraints

- No change to `join`'s existing behavior, flags, or output (`cmd/wormhole-cli/main.go`'s `runJoin` is not modified by this plan).
- `connect` reuses `doRegister` and `writeCredentials` exactly as `join` does — do not duplicate their internal logic, only the flag-parsing/dispatch shell around them (which necessarily differs since `connect` doesn't need `--context`/`--kb-limit`).
- The server logging middleware must not alter response bodies, status codes, or headers sent to the client — it only observes and logs. It must not break `/mcp` or `/healthz` behavior (all existing `internal/mcp` and `cmd/wormhole-server` tests, if any, must still pass).
- Buffering the `/mcp` request body for logging must restore it via `io.NopCloser` before calling the downstream handler — the JSON-RPC handler must see the exact same body it would have without logging.
- Do not invoke a real `claude` binary from tests — use a fake stub script written by the test itself (executable shell script that records its invocation), since there is no `claude` CLI dependency safe to assume in CI or a reviewer's environment.

---

### Task 1: `wormhole-server` per-request activity logging

**Files:**
- Create: `cmd/wormhole-server/logging.go`
- Create: `cmd/wormhole-server/logging_test.go`
- Modify: `cmd/wormhole-server/main.go:48-50` (mount the middleware around `mux`)

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: `loggingMiddleware(next http.Handler) http.Handler` — later code (`main.go`) wraps `mux` with it before passing to `http.Server`.

- [ ] **Step 1: Write the failing tests**

Create `cmd/wormhole-server/logging_test.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoggingMiddleware_LogsMethodPathStatus(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(loggingMiddleware(next))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	resp.Body.Close()

	got := buf.String()
	for _, want := range []string{"GET", "/healthz", "204"} {
		if !strings.Contains(got, want) {
			t.Fatalf("log output missing %q: got %q", want, got)
		}
	}
}

func TestLoggingMiddleware_LogsJSONRPCMethodForMCP(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"method":"initialize"`) {
			t.Fatalf("downstream handler did not see original body: %q", body)
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(loggingMiddleware(next))
	defer srv.Close()

	reqBody, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	resp, err := http.Post(srv.URL+"/mcp", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	resp.Body.Close()

	got := buf.String()
	for _, want := range []string{"POST", "/mcp", "200", "initialize"} {
		if !strings.Contains(got, want) {
			t.Fatalf("log output missing %q: got %q", want, got)
		}
	}
}

func TestLoggingMiddleware_LogsToolNameForToolsCall(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(loggingMiddleware(next))
	defer srv.Close()

	params, _ := json.Marshal(map[string]any{"name": "wormhole.task.list", "arguments": map[string]any{}})
	reqBody, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": json.RawMessage(params)})
	resp, err := http.Post(srv.URL+"/mcp", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	resp.Body.Close()

	got := buf.String()
	for _, want := range []string{"tools/call", "wormhole.task.list"} {
		if !strings.Contains(got, want) {
			t.Fatalf("log output missing %q: got %q", want, got)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/wormhole-server/... -run TestLoggingMiddleware -v`
Expected: FAIL — `loggingMiddleware` is not defined (compile error).

- [ ] **Step 3: Implement the middleware**

Create `cmd/wormhole-server/logging.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"
)

// statusRecorder wraps http.ResponseWriter to capture the status code
// written by the downstream handler, since http.ResponseWriter has no
// getter for it and loggingMiddleware needs it after the handler returns.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// rpcLogProbe is a partial decode of a JSON-RPC request body, used only to
// extract the method (and, for tools/call, the tool name) for the activity
// log line. Decode failures are silently ignored — the request still
// reaches the real handler, which does its own full validation and error
// reporting; this is best-effort observability, not a second validator.
type rpcLogProbe struct {
	Method string `json:"method"`
	Params json.RawMessage `json:"params"`
}

type toolsCallLogProbe struct {
	Name string `json:"name"`
}

// describeMCPRequest reads r's body (if r is a POST to /mcp), restores it
// via io.NopCloser so the downstream handler sees the same bytes it would
// without logging, and returns a short description for the log line
// ("initialize", "tools/list", or "tools/call wormhole.task.list"). Returns
// "" for any non-/mcp request or any body it can't parse.
func describeMCPRequest(r *http.Request) string {
	if r.URL.Path != "/mcp" || r.Method != http.MethodPost {
		return ""
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return ""
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	var probe rpcLogProbe
	if err := json.Unmarshal(body, &probe); err != nil || probe.Method == "" {
		return ""
	}
	if probe.Method != "tools/call" {
		return probe.Method
	}
	var toolProbe toolsCallLogProbe
	if err := json.Unmarshal(probe.Params, &toolProbe); err != nil || toolProbe.Name == "" {
		return probe.Method
	}
	return probe.Method + " " + toolProbe.Name
}

// loggingMiddleware logs one line per request to stdout via the standard
// log package: method, path, status, latency, and (for /mcp requests) the
// JSON-RPC method and tool name, so `wormhole-server`'s stdout shows real
// activity during a demo or test run instead of only its startup line.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		mcpDesc := describeMCPRequest(r)

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		elapsed := time.Since(start)
		if mcpDesc != "" {
			log.Printf("%s %s %d %s %s", r.Method, r.URL.Path, rec.status, elapsed, mcpDesc)
		} else {
			log.Printf("%s %s %d %s", r.Method, r.URL.Path, rec.status, elapsed)
		}
	})
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/wormhole-server/... -run TestLoggingMiddleware -v`
Expected: PASS (all 3 tests)

- [ ] **Step 5: Wire the middleware into `main.go`**

In `cmd/wormhole-server/main.go`, change:

```go
	log.Printf("wormhole-server listening on %s", cfg.ListenAddr)
	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
```

to:

```go
	log.Printf("wormhole-server listening on %s", cfg.ListenAddr)
	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           loggingMiddleware(mux),
```

- [ ] **Step 6: Full package build/vet/test**

Run: `go build ./... && go vet ./... && go test ./cmd/wormhole-server/... -v`
Expected: clean build/vet, all tests pass.

- [ ] **Step 7: Commit**

```bash
git add cmd/wormhole-server/logging.go cmd/wormhole-server/logging_test.go cmd/wormhole-server/main.go
git commit -m "feat(server): log per-request activity to stdout

wormhole-server previously only logged its own startup line, giving no
visibility during a demo or test run. Adds loggingMiddleware: one log
line per request (method, path, status, latency), plus the JSON-RPC
method and tool name for /mcp requests so tool calls are visible as
they happen."
```

---

### Task 2: `wormhole connect` CLI subcommand

**Files:**
- Modify: `cmd/wormhole-cli/main.go:20-38` (usage text, `run` dispatch)
- Modify: `cmd/wormhole-cli/main.go` (add `runConnect`, after `runJoin`)
- Modify: `cmd/wormhole-cli/main_test.go` (add tests, described below)

**Interfaces:**
- Consumes: `doRegister(client *http.Client, server, project string, in registerAgentInput) (registerAgentOutput, error)` and `writeCredentials(path string, creds credentials) error` and `defaultTokenFilePath() (string, error)` — all already defined in `main.go`, unchanged by this task.
- Produces: `runConnect(args []string, stdout, stderr io.Writer) int`, dispatched from `run()` on `args[0] == "connect"`. No other task depends on this.

- [ ] **Step 1: Update `usage()` and dispatch in `run()`**

In `cmd/wormhole-cli/main.go`, change `usage()`:

```go
func usage() string {
	return "usage: wormhole <command> [flags]\n\ncommands:\n  join     join a Wormhole project (RFC-0001 §8.5)\n  connect  join a project and register it as a Claude Code MCP connector"
}
```

And in `run()`, add the new case alongside `"join"`:

```go
	switch args[0] {
	case "join":
		return runJoin(args[1:], stdout, stderr)
	case "connect":
		return runConnect(args[1:], stdout, stderr)
	default:
```

- [ ] **Step 2: Write the failing tests**

Add to `cmd/wormhole-cli/main_test.go` (near the existing `TestRunJoin_*` tests — same file, same package, reuses `fakeServer`/`registerAgentOutput`/`credentials` already defined there):

```go
// fakeClaudeScript writes an executable shell script to t.TempDir() that
// appends every invocation's arguments as one line to <script-dir>/calls.log,
// then always exits 0. Tests use this instead of invoking a real `claude`
// binary, which cannot be assumed present in any environment running this
// suite.
func fakeClaudeScript(t *testing.T) (scriptPath, logPath string) {
	t.Helper()
	dir := t.TempDir()
	scriptPath = filepath.Join(dir, "fake-claude.sh")
	logPath = filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\necho \"$@\" >> \"" + logPath + "\"\nexit 0\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude script: %v", err)
	}
	return scriptPath, logPath
}

func TestRunConnect_Success_RegistersAndWiresConnector(t *testing.T) {
	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		t.Fatal("connect must not call wormhole.kb.search")
		return searchArticlesOutput{}, nil
	})
	defer srv.Close()

	claudeBin, logPath := fakeClaudeScript(t)
	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"connect",
		"--server", srv.URL,
		"--project", "proj-1",
		"--owner", "harley",
		"--model", "claude-code",
		"--permissions", "task.read",
		"--token-file", tokenFile,
		"--claude-bin", claudeBin,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}

	data, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatalf("read credentials file: %v", err)
	}
	var creds credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		t.Fatalf("decode credentials file: %v", err)
	}
	if creds.Token != "sekrit-token" {
		t.Fatalf("credentials.Token: got %q, want %q", creds.Token, "sekrit-token")
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake claude call log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(logData)), "\n")
	if len(lines) != 2 {
		t.Fatalf("fake claude invocation count: got %d, want 2 (remove, add): %q", len(lines), logData)
	}
	if !strings.Contains(lines[0], "mcp remove wormhole -s local") {
		t.Fatalf("first invocation: got %q, want it to contain %q", lines[0], "mcp remove wormhole -s local")
	}
	wantAdd := "mcp add --transport http wormhole " + srv.URL + "/mcp -H Authorization: Bearer sekrit-token"
	if !strings.Contains(lines[1], wantAdd) {
		t.Fatalf("second invocation: got %q, want it to contain %q", lines[1], wantAdd)
	}

	out := stdout.String()
	for _, want := range []string{"Passport created.", "Connector \"wormhole\" registered"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q: got %q", want, out)
		}
	}
}

func TestRunConnect_CustomConnectorName(t *testing.T) {
	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		t.Fatal("connect must not call wormhole.kb.search")
		return searchArticlesOutput{}, nil
	})
	defer srv.Close()

	claudeBin, logPath := fakeClaudeScript(t)
	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"connect",
		"--server", srv.URL,
		"--project", "proj-1",
		"--permissions", "task.read",
		"--token-file", tokenFile,
		"--claude-bin", claudeBin,
		"--connector-name", "wh-staging",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake claude call log: %v", err)
	}
	if !strings.Contains(string(logData), "wh-staging") {
		t.Fatalf("fake claude call log missing custom connector name: %q", logData)
	}
}

func TestRunConnect_RegisterFailure_NeverInvokesClaude(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)
		result := toolCallResult{
			Content: []toolCallResultContent{{Type: "text", Text: `{"error":"identity: invalid scope"}`}},
			IsError: true,
		}
		resultRaw, _ := json.Marshal(result)
		json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: resultRaw})
	}))
	defer srv.Close()

	claudeBin, logPath := fakeClaudeScript(t)
	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"connect", "--server", srv.URL, "--project", "proj-1", "--permissions", "task.read",
		"--token-file", tokenFile, "--claude-bin", claudeBin,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code: got %d, want 1", code)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("fake claude should never have been invoked on register failure")
	}
	if _, err := os.Stat(tokenFile); !os.IsNotExist(err) {
		t.Fatalf("credentials file should not have been written on register failure")
	}
}

func TestRunConnect_ClaudeBinaryNotFound_PrintsManualFallback(t *testing.T) {
	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		t.Fatal("connect must not call wormhole.kb.search")
		return searchArticlesOutput{}, nil
	})
	defer srv.Close()

	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"connect", "--server", srv.URL, "--project", "proj-1", "--permissions", "task.read",
		"--token-file", tokenFile, "--claude-bin", "definitely-not-a-real-binary-xyz",
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code: got %d, want 1", code)
	}
	// Registration itself must still have succeeded and been persisted —
	// only the auto-wire step failed.
	if _, err := os.Stat(tokenFile); err != nil {
		t.Fatalf("credentials file should have been written even though claude binary was missing: %v", err)
	}
	if !strings.Contains(stderr.String(), "sekrit-token") {
		t.Fatalf("stderr missing manual-fallback command with token: %q", stderr.String())
	}
}
```

Add `"os/exec"` is NOT needed in the test file (tests invoke `run`, not `exec` directly); confirm `"net/http/httptest"` and `"strings"` are already imported in `main_test.go` (they are, per existing tests in the file) — only `filepath` and `os` need to already be present too (they are).

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./cmd/wormhole-cli/... -run TestRunConnect -v`
Expected: FAIL — `runConnect` undefined (compile error), `"connect"` not a recognized command.

- [ ] **Step 4: Implement `runConnect`**

Add to `cmd/wormhole-cli/main.go`, after `runJoin`'s closing brace:

```go
// runConnect implements `wormhole connect`: it performs the same
// wormhole.agent.register call as `wormhole join` (via the same
// doRegister/writeCredentials helpers), then wires the issued token into
// Claude Code's MCP connector config by shelling out to the `claude` CLI
// (`claude mcp remove` then `claude mcp add -H`). Unlike `join`, it does
// not run the KB-sync/self-introduction/task-summary steps — those are
// join's concern for an already-connected identity, not connect's concern
// of wiring up the transport.
func runConnect(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "Wormhole server base URL (required)")
	project := fs.String("project", "", "project ID to join (required)")
	owner := fs.String("owner", "", "human/org owner of this agent identity")
	model := fs.String("model", "", "model identifier for this agent identity")
	capabilities := fs.String("capabilities", "", "comma-separated list of agent capabilities")
	repositories := fs.String("repositories", "", "comma-separated list of git repositories this identity is scoped to")
	roles := fs.String("roles", "", "comma-separated list of project-level roles")
	permissions := fs.String("permissions", "", "comma-separated list of permissions to request (e.g. task.create,kb.write)")
	tokenFile := fs.String("token-file", "", "path to write issued credentials to (default: ~/.wormhole/credentials.json)")
	connectorName := fs.String("connector-name", "wormhole", "name to register the MCP connector under (claude mcp add/remove)")
	claudeBin := fs.String("claude-bin", "claude", "path to the claude CLI binary")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *server == "" || *project == "" {
		fmt.Fprintln(stderr, "wormhole connect: --server and --project are required")
		fs.Usage()
		return 2
	}

	splitOrNil := func(s string) []string {
		if s == "" {
			return nil
		}
		return strings.Split(s, ",")
	}
	splitOrEmpty := func(s string) []string {
		if s == "" {
			return []string{}
		}
		return strings.Split(s, ",")
	}

	in := registerAgentInput{
		Permissions:  splitOrEmpty(*permissions),
		Owner:        *owner,
		Model:        *model,
		Capabilities: splitOrNil(*capabilities),
		Repositories: splitOrNil(*repositories),
		Roles:        splitOrNil(*roles),
	}

	out, err := doRegister(http.DefaultClient, *server, *project, in)
	if err != nil {
		fmt.Fprintf(stderr, "wormhole connect: %v\n", err)
		return 1
	}

	path := *tokenFile
	if path == "" {
		defaultPath, err := defaultTokenFilePath()
		if err != nil {
			fmt.Fprintf(stderr, "wormhole connect: %v\n", err)
			return 1
		}
		path = defaultPath
	}

	creds := credentials{
		Server:     *server,
		ProjectID:  *project,
		AgentID:    out.AgentID,
		PassportID: out.PassportID,
		Token:      out.Token,
		IssuedAt:   out.IssuedAt,
	}
	if err := writeCredentials(path, creds); err != nil {
		fmt.Fprintf(stderr, "wormhole connect: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, "Passport created.")
	fmt.Fprintf(stdout, "agent_id=%s passport_id=%s project=%s\n", out.AgentID, out.PassportID, *project)
	fmt.Fprintf(stdout, "credentials written to %s\n", path)

	mcpURL := strings.TrimRight(*server, "/") + "/mcp"
	if _, lookErr := exec.LookPath(*claudeBin); lookErr != nil {
		fmt.Fprintf(stderr, "wormhole connect: %q not found in PATH — wire the connector manually:\n  claude mcp add --transport http %s %s -H \"Authorization: Bearer %s\"\n", *claudeBin, *connectorName, mcpURL, out.Token)
		return 1
	}

	removeCmd := exec.Command(*claudeBin, "mcp", "remove", *connectorName, "-s", "local")
	removeCmd.Run() // best-effort: fine if the connector wasn't registered yet

	addCmd := exec.Command(*claudeBin, "mcp", "add", "--transport", "http", *connectorName, mcpURL, "-H", "Authorization: Bearer "+out.Token)
	addCmd.Stdout = stdout
	addCmd.Stderr = stderr
	if err := addCmd.Run(); err != nil {
		fmt.Fprintf(stderr, "wormhole connect: claude mcp add failed: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Connector %q registered with %s (run /mcp inside Claude Code to reconnect).\n", *connectorName, mcpURL)
	return 0
}
```

Add `"os/exec"` to `main.go`'s import block (alongside the existing `"os"` import).

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./cmd/wormhole-cli/... -run TestRunConnect -v`
Expected: PASS (all 4 tests)

- [ ] **Step 6: Full package build/vet/test**

Run: `go build ./... && go vet ./... && go test ./cmd/wormhole-cli/... -v`
Expected: clean build/vet, every test in the package passes — including the pre-existing `TestRunJoin_*` tests, unmodified by this task.

- [ ] **Step 7: Commit**

```bash
git add cmd/wormhole-cli/main.go cmd/wormhole-cli/main_test.go
git commit -m "feat(cli): add wormhole connect subcommand

Wires a joined project's issued token straight into Claude Code's MCP
connector config (claude mcp remove + add -H) instead of requiring the
operator to manually extract the token from credentials.json and quote
it into a header — this manual step broke live during Chapter 4's
connector test (a fish-shell nested-quoting bug produced an empty
Authorization header). Falls back to printing the manual command if
the claude binary isn't on PATH."
```
