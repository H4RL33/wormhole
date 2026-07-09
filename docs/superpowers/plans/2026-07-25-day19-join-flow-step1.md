# Day 19 — Join Flow Step 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire `wormhole join`'s scaffold (Day 18, `cmd/wormhole-cli/main.go`) to actually perform join flow step 1 (RFC-0001 §8.5): call the server's `wormhole.agent.register` MCP tool over HTTP to create a passport and grant permissions, then persist the issued credentials locally so Day 20/21's join steps (KB sync, self-introduction) can reuse them without re-registering.

**Architecture:** `cmd/wormhole-cli` may only import `internal/types` and client-side code (`docs/architecture.md` §2 module table) — it must not import `internal/mcp` (that would pull the server's registry/auth stack into a client binary and violate the mcp→core one-way dependency this repo enforces). The CLI therefore defines its own small DTOs that mirror the `/mcp/tools/call` wire contract (`CallRequest`/`CallResponse` in `internal/mcp/server.go`, `RegisterAgentInput`/`RegisterAgentOutput` in `internal/mcp/agent.go`) byte-for-byte on the JSON side, rather than importing those types. `runJoin` becomes: parse flags → POST to `{server}/mcp/tools/call` → on success, write a credentials file (`~/.wormhole/credentials.json` by default, or `--token-file`) containing the agent ID, passport ID, and bearer token.

**Tech Stack:** Go stdlib only (`net/http`, `net/http/httptest` for tests, `encoding/json`, `os`, `path/filepath`). No new dependencies.

## Global Constraints

- `cmd/wormhole-cli` imports `internal/types` and client-side code only (`docs/architecture.md` §2 module table) — this task uses stdlib only, no import of `internal/mcp` or any `internal/core/*` package, even in tests. The wire contract is duplicated as local DTOs instead.
- R4 (`docs/architecture.md` §2): no new external Go dependencies.
- T4 (`docs/architecture.md` §7): must pass `go build ./...`, `go vet ./...`, `go test ./...` before commit.
- `identity.Store.Register` (`internal/core/identity/identity.go:99`) rejects a **nil** `permissions` slice with `ErrInvalidScope` — an empty slice `[]string{}` is fine, `nil` is not. The CLI must always marshal `permissions` as `[]` when the flag is unset, never omit it or leave it nil. This is the one field that needs an empty-slice default; `capabilities`/`repositories`/`roles` are nil-safe server-side (`Register` normalizes nil capabilities to `[]string{}`; `issuePassport` normalizes nil repositories/roles the same way), so they may stay `nil` when their flags are unset.
- Server endpoint is `POST {server}/mcp/tools/call` (`cmd/wormhole-server/main.go:64`), envelope is `CallRequest{Tool, ProjectID, Arguments}` / `CallResponse{Result, Error}` (`internal/mcp/server.go:16-26`). Tool errors come back as HTTP 400 with `CallResponse.Error` set to a raw JSON string (`internal/mcp/server.go`'s `writeCallResponse` on the error path) — the CLI does not need to parse that JSON, just surface it to the user.
- Scope note: this task does not add a second, real-server-backed integration test for the CLI's HTTP call. `internal/mcp/e2e_test.go` (Day 5) already proves `wormhole.agent.register`'s real behavior end-to-end; this task's test instead verifies the CLI sends/receives the exact wire shapes those real DTOs use (field names cross-checked against `internal/mcp/agent.go` and `internal/mcp/server.go` in this plan). Adding a from-scratch real-server CLI test would duplicate that coverage — out of scope per `docs/architecture.md` §0.5 (smallest correct diff).

---

### Task 1: Wire `wormhole join` to call `wormhole.agent.register` and persist credentials

**Files:**
- Modify: `cmd/wormhole-cli/main.go` (replaces the Day 18 scaffold's `runJoin` body; adds `--repositories`, `--roles`, `--permissions`, `--token-file` flags)
- Modify: `cmd/wormhole-cli/main_test.go` (replaces the two Day 18 tests that asserted the old "not yet implemented" stdout text, since that text no longer exists; keeps the flag-validation tests, which are still accurate)

**Interfaces:**
- Consumes: nothing from earlier tasks — this is the sole task in this plan.
- Produces: `credentials{Server, ProjectID, AgentID, PassportID, Token string; IssuedAt time.Time}` (JSON persisted to the token file) — Day 20's KB-sync join step will read this file to get a bearer token without re-registering.

- [ ] **Step 1: Write the failing/updated tests**

Replace `cmd/wormhole-cli/main_test.go` in full with:

```go
package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRun_NoArgs_PrintsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code: got %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "usage: wormhole") {
		t.Fatalf("stderr missing usage text: %q", stderr.String())
	}
}

func TestRun_UnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"bogus"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code: got %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), `unknown command "bogus"`) {
		t.Fatalf("stderr missing unknown-command text: %q", stderr.String())
	}
}

func TestRunJoin_MissingRequiredFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"join"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code: got %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--server and --project are required") {
		t.Fatalf("stderr missing required-flags text: %q", stderr.String())
	}
}

func TestRunJoin_MissingProjectOnly(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"join", "--server", "http://localhost:8080"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code: got %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--server and --project are required") {
		t.Fatalf("stderr missing required-flags text: %q", stderr.String())
	}
}

// TestRunJoin_Success_RegistersAndPersistsCredentials drives runJoin
// against a fake /mcp/tools/call server, asserting both the outbound
// request shape (matches internal/mcp.RegisterAgentInput's JSON tags, and
// permissions is never nil) and that a successful response is persisted
// to the credentials file with 0600 permissions.
func TestRunJoin_Success_RegistersAndPersistsCredentials(t *testing.T) {
	issuedAt := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp/tools/call" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var req callRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Tool != "wormhole.agent.register" {
			t.Fatalf("tool: got %q, want wormhole.agent.register", req.Tool)
		}
		if req.ProjectID != "proj-1" {
			t.Fatalf("project_id: got %q, want proj-1", req.ProjectID)
		}
		var in registerAgentInput
		if err := json.Unmarshal(req.Arguments, &in); err != nil {
			t.Fatalf("decode arguments: %v", err)
		}
		if in.Permissions == nil {
			t.Fatal("permissions: got nil, want non-nil (identity.Store.Register rejects nil permissions)")
		}
		if len(in.Capabilities) != 1 || in.Capabilities[0] != "code" {
			t.Fatalf("capabilities: got %v, want [code]", in.Capabilities)
		}
		out := registerAgentOutput{
			AgentID:      "agent-1",
			PassportID:   "passport-1",
			Token:        "sekrit-token",
			Repositories: []string{},
			Roles:        []string{},
			IssuedAt:     issuedAt,
		}
		resultRaw, _ := json.Marshal(out)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(callResponse{Result: resultRaw})
	}))
	defer srv.Close()

	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"join",
		"--server", srv.URL,
		"--project", "proj-1",
		"--owner", "harley",
		"--model", "claude",
		"--capabilities", "code",
		"--permissions", "task.create,kb.write",
		"--token-file", tokenFile,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}

	out := stdout.String()
	for _, want := range []string{"Passport created.", "agent_id=agent-1", "passport_id=passport-1", "project=proj-1", tokenFile} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q: got %q", want, out)
		}
	}

	data, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatalf("read credentials file: %v", err)
	}
	var creds credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		t.Fatalf("decode credentials file: %v", err)
	}
	if creds.Token != "sekrit-token" || creds.AgentID != "agent-1" || creds.PassportID != "passport-1" || creds.ProjectID != "proj-1" || creds.Server != srv.URL {
		t.Fatalf("credentials: got %+v", creds)
	}

	info, err := os.Stat(tokenFile)
	if err != nil {
		t.Fatalf("stat credentials file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials file mode: got %o, want 0600", info.Mode().Perm())
	}
}

// TestRunJoin_ServerError_PrintsError confirms a tool-level rejection
// (HTTP 400 + CallResponse.Error, per internal/mcp/server.go) surfaces to
// stderr and does not write a credentials file.
func TestRunJoin_ServerError_PrintsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(callResponse{Error: `{"error":"identity: invalid scope","code":"INVALID_SCOPE"}`})
	}))
	defer srv.Close()

	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"join", "--server", srv.URL, "--project", "proj-1", "--permissions", "task.create",
		"--token-file", tokenFile,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code: got %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "invalid scope") {
		t.Fatalf("stderr missing server error text: %q", stderr.String())
	}
	if _, err := os.Stat(tokenFile); !os.IsNotExist(err) {
		t.Fatalf("credentials file should not have been written on error")
	}
}

// TestRunJoin_NetworkError_PrintsError confirms an unreachable server
// surfaces a clean error instead of a panic or a silent empty exit.
func TestRunJoin_NetworkError_PrintsError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"join", "--server", "http://127.0.0.1:1", "--project", "proj-1", "--permissions", "task.create",
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code: got %d, want 1", code)
	}
	if stderr.String() == "" {
		t.Fatalf("expected stderr to contain network error, got empty")
	}
}

func TestDefaultTokenFilePath_UnderWormholeDir(t *testing.T) {
	path, err := defaultTokenFilePath()
	if err != nil {
		t.Fatalf("defaultTokenFilePath: %v", err)
	}
	want := filepath.Join(".wormhole", "credentials.json")
	if !strings.HasSuffix(path, want) {
		t.Fatalf("path: got %q, want suffix %q", path, want)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/wormhole-cli/... -v`

Expected: FAIL — `callRequest`, `callResponse`, `registerAgentInput`, `registerAgentOutput`, `credentials`, `defaultTokenFilePath` are all undefined in the current `main.go`, and `runJoin`'s current behavior (prints "not yet implemented", no network call, always exits 0) doesn't match the new tests.

- [ ] **Step 3: Write the implementation**

Replace `cmd/wormhole-cli/main.go` in full with:

```go
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func usage() string {
	return "usage: wormhole <command> [flags]\n\ncommands:\n  join    join a Wormhole project (RFC-0001 §8.5)"
}

// run dispatches to a subcommand and returns the process exit code. It
// takes explicit args/stdout/stderr so subcommands are testable without
// touching os.Args or os.Exit.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage())
		return 2
	}
	switch args[0] {
	case "join":
		return runJoin(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "wormhole: unknown command %q\n\n%s\n", args[0], usage())
		return 2
	}
}

// callRequest/callResponse mirror internal/mcp.CallRequest/CallResponse's
// JSON shape (internal/mcp/server.go). cmd/wormhole-cli cannot import
// internal/mcp (docs/architecture.md §2 module table restricts this
// package to internal/types and client-side code only, and mcp pulls in
// the server's registry/auth stack), so the wire contract is duplicated
// here instead.
type callRequest struct {
	Tool      string          `json:"tool"`
	ProjectID string          `json:"project_id"`
	Arguments json.RawMessage `json:"arguments"`
}

type callResponse struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// registerAgentInput/registerAgentOutput mirror
// internal/mcp.RegisterAgentInput/RegisterAgentOutput's JSON shape
// (internal/mcp/agent.go), for the same reason as callRequest/callResponse.
type registerAgentInput struct {
	Permissions  []string `json:"permissions"`
	Owner        string   `json:"owner"`
	Model        string   `json:"model"`
	Capabilities []string `json:"capabilities"`
	Repositories []string `json:"repositories"`
	Roles        []string `json:"roles"`
}

type registerAgentOutput struct {
	AgentID      string    `json:"agent_id"`
	PassportID   string    `json:"passport_id"`
	Token        string    `json:"token"`
	Repositories []string  `json:"repositories"`
	Roles        []string  `json:"roles"`
	IssuedAt     time.Time `json:"issued_at"`
}

// credentials is what gets persisted to the token file after a successful
// join, so later join steps (Day 20 KB sync, Day 21 self-introduction) can
// reuse the issued token without re-registering.
type credentials struct {
	Server     string    `json:"server"`
	ProjectID  string    `json:"project_id"`
	AgentID    string    `json:"agent_id"`
	PassportID string    `json:"passport_id"`
	Token      string    `json:"token"`
	IssuedAt   time.Time `json:"issued_at"`
}

// doRegister calls wormhole.agent.register at server's /mcp/tools/call
// endpoint (cmd/wormhole-server/main.go) and decodes the result.
func doRegister(client *http.Client, server, project string, in registerAgentInput) (registerAgentOutput, error) {
	argsRaw, err := json.Marshal(in)
	if err != nil {
		return registerAgentOutput{}, fmt.Errorf("marshal register arguments: %w", err)
	}
	reqBody, err := json.Marshal(callRequest{Tool: "wormhole.agent.register", ProjectID: project, Arguments: argsRaw})
	if err != nil {
		return registerAgentOutput{}, fmt.Errorf("marshal call request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(server, "/")+"/mcp/tools/call", bytes.NewReader(reqBody))
	if err != nil {
		return registerAgentOutput{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return registerAgentOutput{}, fmt.Errorf("call wormhole.agent.register: %w", err)
	}
	defer resp.Body.Close()

	var callResp callResponse
	if err := json.NewDecoder(resp.Body).Decode(&callResp); err != nil {
		return registerAgentOutput{}, fmt.Errorf("decode response: %w", err)
	}
	if callResp.Error != "" {
		return registerAgentOutput{}, fmt.Errorf("%s", callResp.Error)
	}
	if resp.StatusCode != http.StatusOK {
		return registerAgentOutput{}, fmt.Errorf("server returned status %d", resp.StatusCode)
	}

	var out registerAgentOutput
	if err := json.Unmarshal(callResp.Result, &out); err != nil {
		return registerAgentOutput{}, fmt.Errorf("decode register result: %w", err)
	}
	return out, nil
}

// defaultTokenFilePath is where credentials land when --token-file isn't
// given: ~/.wormhole/credentials.json.
func defaultTokenFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".wormhole", "credentials.json"), nil
}

// writeCredentials persists creds to path as indented JSON, creating the
// parent directory if needed. File mode is 0600 (owner read/write only)
// since it contains a live bearer token.
func writeCredentials(path string, creds credentials) error {
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create credentials directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write credentials file: %w", err)
	}
	return nil
}

// runJoin implements join flow step 1 (RFC-0001 §8.5): it calls
// wormhole.agent.register to create a passport and grant permissions, then
// persists the issued credentials. KB sync, self-introduction, and the
// open-task summary are later join steps (Day 20+).
func runJoin(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
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
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *server == "" || *project == "" {
		fmt.Fprintln(stderr, "wormhole join: --server and --project are required")
		fs.Usage()
		return 2
	}

	splitOrNil := func(s string) []string {
		if s == "" {
			return nil
		}
		return strings.Split(s, ",")
	}
	// identity.Store.Register rejects a nil permissions slice with
	// ErrInvalidScope (internal/core/identity/identity.go:99); an empty
	// slice is fine, nil is not, so permissions always gets an explicit
	// default here even though the other flags can stay nil.
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
		fmt.Fprintf(stderr, "wormhole join: %v\n", err)
		return 1
	}

	path := *tokenFile
	if path == "" {
		defaultPath, err := defaultTokenFilePath()
		if err != nil {
			fmt.Fprintf(stderr, "wormhole join: %v\n", err)
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
		fmt.Fprintf(stderr, "wormhole join: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, "Passport created.")
	fmt.Fprintf(stdout, "agent_id=%s passport_id=%s project=%s\n", out.AgentID, out.PassportID, *project)
	fmt.Fprintf(stdout, "credentials written to %s\n", path)
	fmt.Fprintln(stdout, "KB sync, self-introduction, and task summary land Day 20+ (RFC-0001 §8.5)")
	return 0
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/wormhole-cli/... -v`

Expected: `PASS` for all nine tests (`TestRun_NoArgs_PrintsUsage`, `TestRun_UnknownCommand`, `TestRunJoin_MissingRequiredFlags`, `TestRunJoin_MissingProjectOnly`, `TestRunJoin_Success_RegistersAndPersistsCredentials`, `TestRunJoin_ServerError_PrintsError`, `TestRunJoin_NetworkError_PrintsError`, `TestDefaultTokenFilePath_UnderWormholeDir`).

- [ ] **Step 5: Run full build/vet/test**

Run: `go build ./... && go vet ./... && go test ./...`

Expected: all packages build, vet clean, all tests pass. (A pre-existing flaky `internal/core/tasks.TestRLSIsolation` under full-suite concurrent load is a known, unrelated issue — if only that one fails, it is not a regression from this task; re-run it in isolation to confirm.)

- [ ] **Step 6: Commit**

```bash
git add cmd/wormhole-cli/main.go cmd/wormhole-cli/main_test.go
git commit -m "feat(cli): wire wormhole join to wormhole.agent.register, persist credentials"
```

---

## After Task 1

Controller (not a subagent) closes Day 19 directly:
- Mark Day 19's roadmap checkbox in `ROADMAP.md` once the task passes final review.
- Append the Day 19 entry to `.superpowers/sdd/progress.md`.
