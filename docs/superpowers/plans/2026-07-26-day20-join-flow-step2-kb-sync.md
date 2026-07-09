# Day 20 — Join Flow Step 2: KB Sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend `wormhole join` (Day 19: registers the agent, issues a passport, persists credentials) with join flow step 2 (RFC-0001 §8.5): after a successful registration, retrieve a relevant slice of the project's knowledge base via semantic search, filtered against the joining agent's declared context (owner/model/capabilities/roles), and print it — mirroring the RFC's indicative output `Synchronising knowledge graph (247 articles, 89 relevant)...`.

**Architecture:** Reuses the already-shipped `wormhole.kb.search` MCP tool (`internal/mcp/kb.go`, semantic ranking via pgvector — built Day 14) rather than adding new server-side code: this is a client-side join-flow addition only. `cmd/wormhole-cli/main.go`'s `doRegister` and the new KB-sync call both POST to `/mcp/tools/call` and decode the same `callResponse` envelope, differing only in whether an `Authorization` bearer header is set (`wormhole.agent.register` doesn't require auth; `wormhole.kb.search` does, using the token step 1 just issued) — so this task factors that shared POST+decode logic into one `callTool` helper instead of duplicating it, then adds `doSearch` on top.

**Tech Stack:** Go stdlib only. No new dependencies.

## Global Constraints

- `cmd/wormhole-cli` imports `internal/types` and client-side code only (`docs/architecture.md` §2 module table) — stdlib only, no `internal/mcp` import even in tests. Wire DTOs stay locally duplicated, matching Day 19's pattern.
- R4 (`docs/architecture.md` §2): no new external Go dependencies.
- T4 (`docs/architecture.md` §7): must pass `go build ./...`, `go vet ./...`, `go test ./...` before commit.
- `wormhole.kb.search` (`internal/mcp/kb.go`) is `RequiresAuth: true` and takes `SearchArticlesInput{Query string; Limit int}` (`limit` defaults server-side to 10 if 0), returning `SearchArticlesOutput{Articles []ArticleSummary}` where `ArticleSummary` carries `article_id`, `project_id`, `title`, `body`, `frontmatter`, `author_agent_id`, `created_at`, `updated_at` (all snake_case JSON tags). The CLI only needs `article_id` and `title` for the sync summary line, so its local `articleSummary` DTO declares only those two fields — `encoding/json` ignores the rest on decode, so this is a safe partial mirror, not a wire mismatch.
- **Design decision (RFC-0001 §8.5 doesn't specify per-step failure semantics for `join`, so this is a free variable — flagged, not silently resolved):** if KB sync (step 2) fails after registration (step 1) already succeeded, the join does **not** fail as a whole. Step 1's output (agent identity, passport, credentials file) is already durable and useful on its own; discarding it because a *read-only* sync step failed would force a pointless re-registration. The CLI prints the sync failure to stderr as a warning and still exits 0. If no query text can be derived (no `--context` flag and no owner/model/capabilities/roles were supplied), the KB sync call is skipped entirely — embedding an empty string against the KB is not a meaningful "relevant slice" and shouldn't cost the RPC.
- Server endpoint, envelope, and error surfacing are unchanged from Day 19: `POST {server}/mcp/tools/call`, `callRequest{Tool, ProjectID, Arguments}` / `callResponse{Result, Error}`, tool errors as HTTP 400 with `CallResponse.Error` as a string.

---

### Task 1: KB sync after registration, `callTool` refactor

**Files:**
- Modify: `cmd/wormhole-cli/main.go` (refactors `doRegister`'s POST+decode logic into a shared `callTool` helper, adds `doSearch`, adds `--context`/`--kb-limit` flags, adds the sync step to `runJoin`)
- Modify: `cmd/wormhole-cli/main_test.go` (existing tests must still pass unchanged in behavior for the registration path; new tests cover the KB-sync addition)

**Interfaces:**
- Consumes: nothing from an earlier plan — this task modifies Day 19's `cmd/wormhole-cli/main.go` in place. Existing symbols it builds on: `callRequest{Tool, ProjectID, Arguments}`, `callResponse{Result, Error}`, `registerAgentInput`/`registerAgentOutput`, `credentials`, `defaultTokenFilePath() (string, error)`, `writeCredentials(path string, creds credentials) error`, `runJoin(args []string, stdout, stderr io.Writer) int`.
- Produces: `callTool(client *http.Client, server, tool, projectID, token string, args any) (json.RawMessage, error)` — the shared MCP call helper; `searchArticlesInput{Query string; Limit int}`, `articleSummary{ArticleID, Title string}`, `searchArticlesOutput{Articles []articleSummary}`; `doSearch(client *http.Client, server, project, token, query string, limit int) (searchArticlesOutput, error)`.

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

// fakeServer builds an httptest.Server that answers wormhole.agent.register
// with a fixed successful registration and wormhole.kb.search with
// searchArticles (a caller-supplied stand-in for the tool handler), so
// tests can exercise the full two-call join sequence without a real
// Postgres-backed server.
func fakeServer(t *testing.T, searchArticles func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse)) *httptest.Server {
	t.Helper()
	issuedAt := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp/tools/call" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var req callRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Tool {
		case "wormhole.agent.register":
			var in registerAgentInput
			if err := json.Unmarshal(req.Arguments, &in); err != nil {
				t.Fatalf("decode register arguments: %v", err)
			}
			if in.Permissions == nil {
				t.Fatal("permissions: got nil, want non-nil")
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
		case "wormhole.kb.search":
			if got := r.Header.Get("Authorization"); got != "Bearer sekrit-token" {
				t.Fatalf("kb.search Authorization header: got %q, want %q", got, "Bearer sekrit-token")
			}
			var in searchArticlesInput
			if err := json.Unmarshal(req.Arguments, &in); err != nil {
				t.Fatalf("decode search arguments: %v", err)
			}
			out, errResp := searchArticles(t, in)
			if errResp != nil {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(*errResp)
				return
			}
			resultRaw, _ := json.Marshal(out)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(callResponse{Result: resultRaw})
		default:
			t.Fatalf("unexpected tool: %s", req.Tool)
		}
	}))
}

// TestRunJoin_Success_RegistersAndPersistsCredentials confirms step 1
// (registration + credential persistence) still behaves exactly as Day 19
// left it, now routed through the callTool refactor.
func TestRunJoin_Success_RegistersAndPersistsCredentials(t *testing.T) {
	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		return searchArticlesOutput{Articles: []articleSummary{}}, nil
	})
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

// TestRunJoin_KBSync_UsesCapabilitiesAndRolesAsQuery confirms that when no
// --context is given, the query sent to wormhole.kb.search is built from
// owner/model/capabilities/roles, and that the returned articles are
// printed.
func TestRunJoin_KBSync_UsesCapabilitiesAndRolesAsQuery(t *testing.T) {
	var gotQuery string
	var gotLimit int
	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		gotQuery = in.Query
		gotLimit = in.Limit
		return searchArticlesOutput{Articles: []articleSummary{
			{ArticleID: "art-1", Title: "deploy runbook"},
			{ArticleID: "art-2", Title: "on-call rotation"},
		}}, nil
	})
	defer srv.Close()

	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"join",
		"--server", srv.URL,
		"--project", "proj-1",
		"--owner", "harley",
		"--model", "claude",
		"--capabilities", "deploy,review",
		"--roles", "contributor",
		"--permissions", "kb.write",
		"--token-file", tokenFile,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}

	for _, want := range []string{"harley", "claude", "deploy", "review", "contributor"} {
		if !strings.Contains(gotQuery, want) {
			t.Fatalf("kb.search query: got %q, want it to contain %q", gotQuery, want)
		}
	}
	if gotLimit != 10 {
		t.Fatalf("kb.search limit: got %d, want default 10", gotLimit)
	}

	out := stdout.String()
	for _, want := range []string{"Synchronising knowledge graph (2 relevant)", "deploy runbook (art-1)", "on-call rotation (art-2)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q: got %q", want, out)
		}
	}
}

// TestRunJoin_KBSync_ExplicitContextAndLimit confirms --context overrides
// the derived query and --kb-limit is forwarded.
func TestRunJoin_KBSync_ExplicitContextAndLimit(t *testing.T) {
	var gotQuery string
	var gotLimit int
	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		gotQuery = in.Query
		gotLimit = in.Limit
		return searchArticlesOutput{Articles: []articleSummary{}}, nil
	})
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"join",
		"--server", srv.URL,
		"--project", "proj-1",
		"--permissions", "kb.write",
		"--context", "billing service architecture",
		"--kb-limit", "5",
		"--token-file", filepath.Join(t.TempDir(), "credentials.json"),
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}
	if gotQuery != "billing service architecture" {
		t.Fatalf("kb.search query: got %q, want %q", gotQuery, "billing service architecture")
	}
	if gotLimit != 5 {
		t.Fatalf("kb.search limit: got %d, want 5", gotLimit)
	}
	if !strings.Contains(stdout.String(), "Synchronising knowledge graph (0 relevant)") {
		t.Fatalf("stdout missing sync summary: %q", stdout.String())
	}
}

// TestRunJoin_KBSync_SkippedWhenNoContext confirms the sync call is
// skipped entirely (no HTTP request made) when nothing was supplied to
// build a query from.
func TestRunJoin_KBSync_SkippedWhenNoContext(t *testing.T) {
	called := false
	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		called = true
		return searchArticlesOutput{Articles: []articleSummary{}}, nil
	})
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"join",
		"--server", srv.URL,
		"--project", "proj-1",
		"--permissions", "kb.write",
		"--token-file", filepath.Join(t.TempDir(), "credentials.json"),
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}
	if called {
		t.Fatalf("expected wormhole.kb.search to be skipped, but it was called")
	}
	if !strings.Contains(stdout.String(), "skipped") {
		t.Fatalf("stdout missing skip notice: %q", stdout.String())
	}
}

// TestRunJoin_KBSync_FailureIsNonFatal confirms a failed KB sync doesn't
// erase step 1's already-persisted credentials or flip the exit code.
func TestRunJoin_KBSync_FailureIsNonFatal(t *testing.T) {
	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		return searchArticlesOutput{}, &callResponse{Error: `{"error":"kb: search: boom"}`}
	})
	defer srv.Close()

	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"join",
		"--server", srv.URL,
		"--project", "proj-1",
		"--owner", "harley",
		"--permissions", "kb.write",
		"--token-file", tokenFile,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0 (KB sync failure must not fail the whole join), stderr: %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "KB sync") {
		t.Fatalf("stderr missing KB sync warning: %q", stderr.String())
	}
	if _, err := os.Stat(tokenFile); err != nil {
		t.Fatalf("credentials file should still exist after a KB sync failure: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/wormhole-cli/... -v`

Expected: FAIL — `searchArticlesInput`, `articleSummary`, `searchArticlesOutput`, `callTool`, `doSearch` are all undefined, and the current `runJoin` never calls `wormhole.kb.search`.

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

// searchArticlesInput mirrors internal/mcp.SearchArticlesInput's JSON
// shape (internal/mcp/kb.go). searchArticlesOutput/articleSummary are a
// deliberately partial mirror of SearchArticlesOutput/ArticleSummary: the
// CLI only needs article_id and title for the join-time sync summary, and
// encoding/json safely ignores the other fields (body, frontmatter,
// author_agent_id, created_at, updated_at) on decode.
type searchArticlesInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

type articleSummary struct {
	ArticleID string `json:"article_id"`
	Title     string `json:"title"`
}

type searchArticlesOutput struct {
	Articles []articleSummary `json:"articles"`
}

// credentials is what gets persisted to the token file after a successful
// join, so later join steps (Day 21 self-introduction) can reuse the
// issued token without re-registering.
type credentials struct {
	Server     string    `json:"server"`
	ProjectID  string    `json:"project_id"`
	AgentID    string    `json:"agent_id"`
	PassportID string    `json:"passport_id"`
	Token      string    `json:"token"`
	IssuedAt   time.Time `json:"issued_at"`
}

// callTool POSTs one MCP tool invocation to server's /mcp/tools/call
// endpoint (cmd/wormhole-server/main.go) and returns the decoded result's
// raw JSON. token is optional: pass "" for tools that don't require auth
// (e.g. wormhole.agent.register); a non-empty token is sent as a bearer
// Authorization header for tools that do (e.g. wormhole.kb.search).
func callTool(client *http.Client, server, tool, projectID, token string, args any) (json.RawMessage, error) {
	argsRaw, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("marshal %s arguments: %w", tool, err)
	}
	reqBody, err := json.Marshal(callRequest{Tool: tool, ProjectID: projectID, Arguments: argsRaw})
	if err != nil {
		return nil, fmt.Errorf("marshal call request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(server, "/")+"/mcp/tools/call", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call %s: %w", tool, err)
	}
	defer resp.Body.Close()

	var callResp callResponse
	if err := json.NewDecoder(resp.Body).Decode(&callResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if callResp.Error != "" {
		return nil, fmt.Errorf("%s", callResp.Error)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned status %d", resp.StatusCode)
	}
	return callResp.Result, nil
}

// doRegister calls wormhole.agent.register (no auth required).
func doRegister(client *http.Client, server, project string, in registerAgentInput) (registerAgentOutput, error) {
	resultRaw, err := callTool(client, server, "wormhole.agent.register", project, "", in)
	if err != nil {
		return registerAgentOutput{}, err
	}
	var out registerAgentOutput
	if err := json.Unmarshal(resultRaw, &out); err != nil {
		return registerAgentOutput{}, fmt.Errorf("decode register result: %w", err)
	}
	return out, nil
}

// doSearch calls wormhole.kb.search with the token issued by doRegister
// (join flow step 2, RFC-0001 §8.5: relevant-article slice retrieval).
func doSearch(client *http.Client, server, project, token, query string, limit int) (searchArticlesOutput, error) {
	resultRaw, err := callTool(client, server, "wormhole.kb.search", project, token, searchArticlesInput{Query: query, Limit: limit})
	if err != nil {
		return searchArticlesOutput{}, err
	}
	var out searchArticlesOutput
	if err := json.Unmarshal(resultRaw, &out); err != nil {
		return searchArticlesOutput{}, fmt.Errorf("decode search result: %w", err)
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

// runJoin implements join flow steps 1-2 (RFC-0001 §8.5): step 1 calls
// wormhole.agent.register to create a passport and grant permissions, then
// persists the issued credentials; step 2 retrieves a relevant KB slice
// via wormhole.kb.search, filtered against the agent's declared context.
// Self-introduction and the open-task summary are later join steps
// (Day 21+).
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
	context := fs.String("context", "", "explicit text to use for the KB semantic-sync query (default: built from owner/model/capabilities/roles)")
	kbLimit := fs.Int("kb-limit", 10, "max number of KB articles to retrieve during join sync")
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

	kbQuery := *context
	if kbQuery == "" {
		parts := []string{}
		if *owner != "" {
			parts = append(parts, *owner)
		}
		if *model != "" {
			parts = append(parts, *model)
		}
		parts = append(parts, in.Capabilities...)
		parts = append(parts, in.Roles...)
		kbQuery = strings.Join(parts, " ")
	}
	if kbQuery == "" {
		fmt.Fprintln(stdout, "Synchronising knowledge graph... skipped (no --context, capabilities, roles, owner, or model to build a query from)")
	} else {
		searchOut, searchErr := doSearch(http.DefaultClient, *server, *project, out.Token, kbQuery, *kbLimit)
		if searchErr != nil {
			fmt.Fprintf(stderr, "wormhole join: KB sync failed: %v\n", searchErr)
		} else {
			fmt.Fprintf(stdout, "Synchronising knowledge graph (%d relevant)...\n", len(searchOut.Articles))
			for _, a := range searchOut.Articles {
				fmt.Fprintf(stdout, "  - %s (%s)\n", a.Title, a.ArticleID)
			}
		}
	}

	fmt.Fprintln(stdout, "Self-introduction and task summary land Day 21+ (RFC-0001 §8.5)")
	return 0
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/wormhole-cli/... -v`

Expected: `PASS` for all 11 tests (the original 8 from Day 19, plus `TestRunJoin_KBSync_UsesCapabilitiesAndRolesAsQuery`, `TestRunJoin_KBSync_ExplicitContextAndLimit`, `TestRunJoin_KBSync_SkippedWhenNoContext`, `TestRunJoin_KBSync_FailureIsNonFatal`).

- [ ] **Step 5: Run full build/vet/test**

Run: `go build ./... && go vet ./... && go test ./...`

Expected: all packages build, vet clean, all tests pass. The known pre-existing flaky `internal/core/tasks.TestRLSIsolation` under full-suite concurrent load (tracked in `docs/TODO.md`) is not a regression from this task if it's the only failure — re-run it in isolation to confirm before treating it as such.

- [ ] **Step 6: Commit**

```bash
git add cmd/wormhole-cli/main.go cmd/wormhole-cli/main_test.go
git commit -m "feat(cli): add KB sync to wormhole join (join flow step 2)"
```

---

## After Task 1

Controller (not a subagent) closes Day 20 directly:
- Mark Day 20's roadmap checkbox in `ROADMAP.md` once the task passes final review.
- Append the Day 20 entry to `.superpowers/sdd/progress.md`.
