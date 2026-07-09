# Day 18 — M3 Close + M4 Kickoff Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close M3 (Knowledge Base) with an end-to-end integration test proving write → search → compliance checks work together through the real MCP HTTP boundary, then kick off M4 with a `wormhole-cli` scaffold for the `join` subcommand.

**Architecture:** Task 1 adds one new test file exercising existing, already-shipped KB store and MCP tool code (`internal/core/kb`, `internal/mcp/kb.go`) through a real `httptest.Server`, mirroring the `TestM2_TaskLifecycleEventsOnChannel` pattern in `internal/mcp/m2_integration_test.go`. Task 2 fills in the empty `cmd/wormhole-cli/main.go` with a minimal, testable command dispatcher (`run`) and a `join` subcommand that parses and validates flags only — no network call yet, since passport issuance is Day 19's job (RFC-0001 §8.5).

**Tech Stack:** Go stdlib only (`net/http/httptest`, `flag`, `encoding/json`), real Postgres via existing `testDB`/`testKBStore`/`testIdentityStore` helpers for Task 1. No new dependencies.

## Global Constraints

- R1/R2 (`docs/architecture.md` §2): `internal/core/*` never imports `internal/mcp`; no new cross-core imports. Task 1 only adds a test in `internal/mcp`, no core changes.
- R4 (`docs/architecture.md` §2): no new external Go dependencies. Both tasks use stdlib only.
- `cmd/wormhole-cli` may import `internal/types` and client-side code only (`docs/architecture.md` §2 module table) — Task 2 uses stdlib only, which satisfies this trivially.
- T1/T4 (`docs/architecture.md` §7): Task 1 is DB-backed against real Postgres (no mocks), following `internal/mcp/m2_integration_test.go`'s pattern. Both tasks must pass `go build ./...`, `go vet ./...`, `go test ./...` before commit.
- MCP error envelope (`internal/mcp/server.go`): tool errors return HTTP 400 with `CallResponse.Error` set to the raw JSON string from the store's `Error()` method (e.g. `{"error":"...","code":"DEDUP_VIOLATION",...}`) — not a Go-wrapped string. Assertions must decode `CallResponse.Error` as JSON and check the `code` field, matching `TestMcp_WriteArticle_DedupViolation`'s existing pattern.

---

### Task 1: M3 integration test — write → search → compliance loop

**Files:**
- Create: `internal/mcp/m3_integration_test.go`
- Test: same file (this task is itself a test file; no separate test/impl split)

**Interfaces:**
- Consumes: `testDB(t)`, `testIdentityStore(t)`, `mustCreateProject(t, name)`, `mustRegisterAgent(t, projectID)` (all in `internal/mcp/*_test.go`, package-visible); `kb.NewStore(db *sql.DB, embedder kb.Embedder, dedupThreshold float64, maxBodyLength int, minLinksDecision, minLinksPolicy, minLinksProcedure int) *kb.Store`; `kb.StubEmbedder{}`; `NewRegistry()`, `(*Registry).Register(Tool)`, `NewCallHandler(registry *Registry, identityStore *identity.Store) http.HandlerFunc`; `WriteArticleTool(store *kb.Store) Tool`; `SearchArticlesTool(store *kb.Store) Tool`; `WriteArticleInput{Title, Body string; Frontmatter json.RawMessage; Links []string; Force bool}`; `WriteArticleOutput{ArticleID, ProjectID, Title string; CreatedAt time.Time}`; `SearchArticlesInput{Query string; Limit int}`; `SearchArticlesOutput{Articles []ArticleSummary}`; `ArticleSummary{ArticleID, ProjectID, Title, Body string; ...}`; `CallRequest{Tool, ProjectID string; Arguments json.RawMessage}`; `CallResponse{Result any; Error string}`.
- Produces: nothing consumed by later tasks — this is a terminal integration test.

- [ ] **Step 1: Write the integration test**

Create `internal/mcp/m3_integration_test.go`:

```go
package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/H4RL33/wormhole/internal/core/kb"
)

// TestM3_KBWriteSearchComplianceLoop is the M3 exit-bar test (RFC-0001
// §8.3): it drives the KB's MCP boundary end-to-end through a real HTTP
// server, proving the three pieces built across Days 13-17 work together
// rather than only in isolation: a written article is retrievable via
// semantic search, a semantic duplicate is rejected on write, and an
// over-length body is rejected on write. Mirrors the shape of
// TestM2_TaskLifecycleEventsOnChannel.
func TestM3_KBWriteSearchComplianceLoop(t *testing.T) {
	db := testDB(t)
	// maxBodyLength=120 keeps the conciseness case below deliberately small
	// so the test doesn't need a multi-KB body; dedupThreshold=0.85 and
	// minLinks=1/1/1 match the values wired in cmd/wormhole-server/main.go's
	// default config.
	store := kb.NewStore(db, kb.StubEmbedder{}, 0.85, 120, 1, 1, 1)
	identityStore := testIdentityStore(t)
	projectID := mustCreateProject(t, "m3-kb-write-search-compliance")
	_, token := mustRegisterAgent(t, projectID)

	registry := NewRegistry()
	registry.Register(WriteArticleTool(store))
	registry.Register(SearchArticlesTool(store))
	handler := NewCallHandler(registry, identityStore)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	callTool := func(tool string, args any) (*http.Response, CallResponse) {
		t.Helper()
		argBytes, _ := json.Marshal(args)
		body, _ := json.Marshal(CallRequest{Tool: tool, ProjectID: projectID, Arguments: argBytes})
		req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s POST: %v", tool, err)
		}
		defer resp.Body.Close()
		var callResp CallResponse
		if decodeErr := json.NewDecoder(resp.Body).Decode(&callResp); decodeErr != nil {
			t.Fatalf("%s decode: %v", tool, decodeErr)
		}
		return resp, callResp
	}

	// 1. Write an article.
	writeResp, writeCall := callTool("wormhole.kb.write", WriteArticleInput{
		Title: "deploy runbook",
		Body:  "run deploy.sh then verify the health endpoint returns 200",
	})
	if writeResp.StatusCode != http.StatusOK {
		t.Fatalf("write status: got %d, want 200, body %+v", writeResp.StatusCode, writeCall)
	}
	writeRaw, _ := json.Marshal(writeCall.Result)
	var writeOut WriteArticleOutput
	json.Unmarshal(writeRaw, &writeOut)
	if writeOut.ArticleID == "" {
		t.Fatalf("write output missing article_id: %+v", writeOut)
	}

	// 2. Search retrieves it.
	searchResp, searchCall := callTool("wormhole.kb.search", SearchArticlesInput{Query: "deploy runbook"})
	if searchResp.StatusCode != http.StatusOK {
		t.Fatalf("search status: got %d, want 200, body %+v", searchResp.StatusCode, searchCall)
	}
	searchRaw, _ := json.Marshal(searchCall.Result)
	var searchOut SearchArticlesOutput
	json.Unmarshal(searchRaw, &searchOut)
	found := false
	for _, a := range searchOut.Articles {
		if a.ArticleID == writeOut.ArticleID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("search results missing written article %s: %+v", writeOut.ArticleID, searchOut.Articles)
	}

	// 3. Dedup check fires on a semantic duplicate (same body, stub
	// embedder is deterministic so identical body -> identical embedding
	// -> similarity 1.0 >= threshold).
	dedupResp, dedupCall := callTool("wormhole.kb.write", WriteArticleInput{
		Title: "deploy runbook (copy)",
		Body:  "run deploy.sh then verify the health endpoint returns 200",
	})
	if dedupResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("dedup write status: got %d, want 400, body %+v", dedupResp.StatusCode, dedupCall)
	}
	var dedupErr struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(dedupCall.Error), &dedupErr); err != nil {
		t.Fatalf("dedup error not valid JSON: %q (%v)", dedupCall.Error, err)
	}
	if dedupErr.Code != "DEDUP_VIOLATION" {
		t.Fatalf("dedup error code: got %q, want DEDUP_VIOLATION", dedupErr.Code)
	}

	// 4. Conciseness check fires on an over-length body (store configured
	// with maxBodyLength=120 above; this body is 130 runes).
	longBody := strings.Repeat("x", 130)
	concisenessResp, concisenessCall := callTool("wormhole.kb.write", WriteArticleInput{
		Title: "too long",
		Body:  longBody,
	})
	if concisenessResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("conciseness write status: got %d, want 400, body %+v", concisenessResp.StatusCode, concisenessCall)
	}
	var concisenessErr struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(concisenessCall.Error), &concisenessErr); err != nil {
		t.Fatalf("conciseness error not valid JSON: %q (%v)", concisenessCall.Error, err)
	}
	if concisenessErr.Code != "CONCISENESS_VIOLATION" {
		t.Fatalf("conciseness error code: got %q, want CONCISENESS_VIOLATION", concisenessErr.Code)
	}
}
```

- [ ] **Step 2: Run the test to verify it passes**

Run: `go test ./internal/mcp/... -run TestM3_KBWriteSearchComplianceLoop -v`

Expected: `PASS` (this test exercises already-implemented store/tool code from Days 13-17, so it should pass on first run against a reachable Postgres instance — it is not new production logic, only a new integration path). If Postgres is unreachable, `testDB` skips the test; confirm a reachable instance is running (`docker-compose up -d` per `docs/architecture.md`) before treating a skip as a pass.

- [ ] **Step 3: Run full test suite to confirm no regressions**

Run: `go build ./... && go vet ./... && go test ./...`

Expected: all packages build, vet clean, all tests pass.

- [ ] **Step 4: Commit**

```bash
git add internal/mcp/m3_integration_test.go
git commit -m "test(kb): add M3 exit-bar integration test (write, search, dedup, conciseness)"
```

---

### Task 2: `wormhole-cli` join command scaffold

**Files:**
- Modify: `cmd/wormhole-cli/main.go`
- Test: `cmd/wormhole-cli/main_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks (stdlib only).
- Produces: `run(args []string, stdout, stderr io.Writer) int` — the command dispatcher later `join` steps (Day 19+) will extend with a real HTTP call; `runJoin(args []string, stdout, stderr io.Writer) int` — the `join` subcommand handler.

- [ ] **Step 1: Write the failing tests**

Create `cmd/wormhole-cli/main_test.go`:

```go
package main

import (
	"bytes"
	"strings"
	"testing"
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

func TestRunJoin_ValidArgs_PrintsConfirmation(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"join",
		"--server", "http://localhost:8080",
		"--project", "proj-123",
		"--owner", "harley",
		"--model", "claude",
		"--capabilities", "code,review",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"server=http://localhost:8080",
		"project=proj-123",
		"owner=harley",
		"model=claude",
		"capabilities=[code review]",
		"not yet implemented",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q: got %q", want, out)
		}
	}
}

func TestRunJoin_ValidArgs_NoCapabilities(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"join",
		"--server", "http://localhost:8080",
		"--project", "proj-123",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "capabilities=[]") {
		t.Fatalf("stdout missing empty capabilities: %q", stdout.String())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/wormhole-cli/... -v`

Expected: FAIL — `run` and `runJoin` are undefined (current `main.go` is `package main; func main() {}`).

- [ ] **Step 3: Write the implementation**

Replace `cmd/wormhole-cli/main.go` with:

```go
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
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

// runJoin parses and validates `wormhole join` flags. It is a scaffold
// only: it does not yet call the server. Passport issuance and the rest
// of the join flow (RFC-0001 §8.5) land Day 19+.
func runJoin(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "Wormhole server base URL (required)")
	project := fs.String("project", "", "project ID to join (required)")
	owner := fs.String("owner", "", "human/org owner of this agent identity")
	model := fs.String("model", "", "model identifier for this agent identity")
	capabilities := fs.String("capabilities", "", "comma-separated list of agent capabilities")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *server == "" || *project == "" {
		fmt.Fprintln(stderr, "wormhole join: --server and --project are required")
		fs.Usage()
		return 2
	}

	var caps []string
	if *capabilities != "" {
		caps = strings.Split(*capabilities, ",")
	}

	fmt.Fprintf(stdout, "wormhole join: server=%s project=%s owner=%s model=%s capabilities=%v\n",
		*server, *project, *owner, *model, caps)
	fmt.Fprintln(stdout, "join flow not yet implemented: passport issuance and permission grant land Day 19 (RFC-0001 §8.5)")
	return 0
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/wormhole-cli/... -v`

Expected: `PASS` for all six tests.

- [ ] **Step 5: Run full build/vet/test**

Run: `go build ./... && go vet ./... && go test ./...`

Expected: all packages build, vet clean, all tests pass.

- [ ] **Step 6: Commit**

```bash
git add cmd/wormhole-cli/main.go cmd/wormhole-cli/main_test.go
git commit -m "feat(cli): scaffold wormhole join command (flag parsing, no network call yet)"
```

---

## After Both Tasks

Controller (not a subagent) closes Day 18 directly:
- Mark Day 18's three roadmap checkboxes in `ROADMAP.md` (M3 integration test, M3 review/demo, M4 kickoff) once both tasks pass final review.
- Write the M3 review/demo line in `ROADMAP.md` summarizing what the exit-bar test proved (same style as Day 6's and Day 12's demo lines), naming the test function and file.
- Append the Day 18 entry to `.superpowers/sdd/progress.md`.
