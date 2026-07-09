# Chapter 4 — CLI JSON-RPC Migration + Claude Code Connector Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the Chapter 3 final-review blocker (`cmd/wormhole-cli` still speaks the deleted `/mcp/tools/call` envelope) so `wormhole join` works against a live server, then document the Claude Code connector setup. `ROADMAP-ALPHA2.md` Chapter 4's remaining two checkboxes (live connector test, M1 review/demo) are human-run verification steps, not coded tasks — this plan produces everything needed for a human to execute them, but does not itself perform them.

**Architecture:** `cmd/wormhole-cli/main.go`'s `callTool` currently POSTs a bespoke `{tool, project_id, arguments}` envelope to the deleted `/mcp/tools/call` path. Chapter 3 replaced the server side with a single JSON-RPC 2.0 `/mcp` endpoint (`internal/mcp/jsonrpc.go`, frozen in `docs/mcp-protocol.md`). `cmd/wormhole-cli` cannot import `internal/mcp` (`docs/architecture.md` module boundary), so it keeps its own minimal mirror of the wire types, same pattern already used for `registerAgentInput`/`registerAgentOutput`. `callTool` is rewritten to build a JSON-RPC `tools/call` request (with `project_id` folded into `arguments`, per `docs/mcp-protocol.md` §4.1 — there is no sibling envelope field), POST it to `/mcp`, and unwrap the `{content: [{type, text}], isError}` result shape. `cmd/wormhole-cli/main_test.go`'s `fakeServer` test harness is rewritten to answer the same JSON-RPC shape.

**Tech Stack:** Go stdlib only (`net/http`, `encoding/json`) — no new dependencies.

## Global Constraints

- JSON-RPC envelope, error codes, and endpoint are frozen by `docs/mcp-protocol.md` (Chapter 1 decision) and implemented exactly as-is in `internal/mcp/jsonrpc.go` — do not deviate from either.
- `project_id` lives inside `arguments`, never as a sibling JSON-RPC field (`docs/mcp-protocol.md` §4.1).
- Every RPC response comes back HTTP `200 OK` regardless of success/failure — outcome is in the body (`docs/mcp-protocol.md` §2). Tool-handler failures are `isError: true` inside a successful RPC result, not a JSON-RPC `error` object (`docs/mcp-protocol.md` §3.1).
- `cmd/wormhole-cli` may not import `internal/mcp` (`docs/architecture.md` module boundary) — wire types are a local mirror, matching the existing pattern for `registerAgentInput`/`registerAgentOutput`.
- No behavior change to `runJoin`'s output, flags, or credential file format — only the wire transport underneath `callTool` changes. All existing `doRegister`/`doSearch`/`doListChannels`/`doPostEvent`/`doListTasks` call sites keep their current signatures.

---

### Task 1: Migrate `cmd/wormhole-cli` to the JSON-RPC `/mcp` endpoint

**Files:**
- Modify: `cmd/wormhole-cli/main.go:41-56` (type definitions), `cmd/wormhole-cli/main.go:138-181` (`callTool`)
- Modify: `cmd/wormhole-cli/main_test.go:64-191` (`fakeServer`/`fakeServerExtended`), `cmd/wormhole-cli/main_test.go:246-268` (`TestRunJoin_ServerError_PrintsError`)

**Interfaces:**
- Consumes: `internal/mcp/jsonrpc.go`'s frozen wire shapes (read-only reference, not imported — `RPCRequest{jsonrpc, id, method, params}`, `RPCResponse{jsonrpc, id, result, error}`, `RPCError{code, message, data}`, `toolsCallParams{name, arguments}`, `toolCallResult{content: [{type, text}], isError}}`), `docs/mcp-protocol.md` §3-§4.1.
- Produces: `callTool(client, server, tool, projectID, token, args)` keeps its exact existing signature and return type (`(json.RawMessage, error)`) — every caller (`doRegister`, `doSearch`, `doListChannels`, `doPostEvent`, `doListTasks`) needs zero changes.

- [ ] **Step 1: Replace the wire-type mirror in `main.go`**

Replace lines 41-56 (the `callRequest`/`callResponse` type block and its doc comment) with:

```go
// rpcRequest/rpcResponse/rpcError/toolsCallParams/toolCallResult mirror
// internal/mcp's JSON-RPC 2.0 wire shapes (internal/mcp/jsonrpc.go,
// docs/mcp-protocol.md §3-§4). cmd/wormhole-cli cannot import internal/mcp
// (docs/architecture.md §2 module table restricts this package to
// internal/types and client-side code only, and mcp pulls in the server's
// registry/auth stack), so the wire contract is duplicated here instead,
// same pattern as registerAgentInput/registerAgentOutput below.
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
	Data    any    `json:"data,omitempty"`
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
```

- [ ] **Step 2: Rewrite `callTool` to speak JSON-RPC**

Replace the `callTool` function (lines 138-181, including its doc comment) with:

```go
// callTool sends one JSON-RPC 2.0 "tools/call" request to server's single
// /mcp endpoint (docs/mcp-protocol.md §2-§4.1, internal/mcp/jsonrpc.go) and
// returns the decoded tool result's raw JSON. project_id is folded into
// arguments, not sent as a sibling field (§4.1 — there is no envelope
// field for it). token is optional: pass "" for tools that don't require
// auth (e.g. wormhole.agent.register); a non-empty token is sent as a
// bearer Authorization header for tools that do (e.g. wormhole.kb.search).
// A tool-handler failure (isError: true) and a JSON-RPC-level error both
// surface as a plain Go error — callers don't need to distinguish them,
// matching this function's pre-Chapter-4 behavior.
func callTool(client *http.Client, server, tool, projectID, token string, args any) (json.RawMessage, error) {
	argsRaw, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("marshal %s arguments: %w", tool, err)
	}
	var argsMap map[string]any
	if err := json.Unmarshal(argsRaw, &argsMap); err != nil {
		return nil, fmt.Errorf("decode %s arguments for project_id injection: %w", tool, err)
	}
	if argsMap == nil {
		argsMap = map[string]any{}
	}
	argsMap["project_id"] = projectID
	argsWithProject, err := json.Marshal(argsMap)
	if err != nil {
		return nil, fmt.Errorf("marshal %s arguments with project_id: %w", tool, err)
	}

	paramsRaw, err := json.Marshal(toolsCallParams{Name: tool, Arguments: argsWithProject})
	if err != nil {
		return nil, fmt.Errorf("marshal tools/call params: %w", err)
	}
	reqBody, err := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "tools/call",
		Params:  paramsRaw,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal JSON-RPC request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(server, "/")+"/mcp", bytes.NewReader(reqBody))
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

	var rpcResp rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("%s", rpcResp.Error.Message)
	}

	var result toolCallResult
	if err := json.Unmarshal(rpcResp.Result, &result); err != nil {
		return nil, fmt.Errorf("decode tools/call result: %w", err)
	}
	if len(result.Content) == 0 {
		return nil, fmt.Errorf("%s: empty tool result content", tool)
	}
	if result.IsError {
		return nil, fmt.Errorf("%s", result.Content[0].Text)
	}
	return json.RawMessage(result.Content[0].Text), nil
}
```

- [ ] **Step 3: Rewrite `fakeServer`/`fakeServerExtended` in `main_test.go` to answer JSON-RPC**

Replace `main_test.go` lines 64-191 (the `fakeServer` and `fakeServerExtended` functions) with:

```go
// callResponse is a test-only convenience type: a callback returns either
// its tool's real output or a *callResponse carrying an error message,
// which fakeServerExtended wraps into a JSON-RPC isError:true result
// (docs/mcp-protocol.md §3.1 — a tool-handler failure is a successful RPC
// call whose result carries isError:true, never a JSON-RPC error object).
type callResponse struct {
	Error string
}

// fakeServer builds an httptest.Server that answers wormhole.agent.register
// with a fixed successful registration and wormhole.kb.search with
// searchArticles (a caller-supplied stand-in for the tool handler), so
// tests can exercise the full two-call join sequence without a real
// Postgres-backed server.
func fakeServer(t *testing.T, searchArticles func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse)) *httptest.Server {
	return fakeServerExtended(t, searchArticles, nil, nil, nil)
}

func fakeServerExtended(
	t *testing.T,
	searchArticles func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse),
	listChannels func(t *testing.T) (listChannelsOutput, *callResponse),
	postEvent func(t *testing.T, in postEventInput) (postEventOutput, *callResponse),
	listTasks func(t *testing.T) (listTasksOutput, *callResponse),
) *httptest.Server {
	t.Helper()
	issuedAt := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		var params toolsCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("decode tools/call params: %v", err)
		}

		writeResult := func(resultOut any) {
			resultRaw, _ := json.Marshal(resultOut)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: resultRaw})
		}
		writeToolResult := func(out any, errResp *callResponse) {
			if errResp != nil {
				writeResult(toolCallResult{
					Content: []toolCallResultContent{{Type: "text", Text: errResp.Error}},
					IsError: true,
				})
				return
			}
			outRaw, _ := json.Marshal(out)
			writeResult(toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: string(outRaw)}}})
		}

		switch params.Name {
		case "wormhole.agent.register":
			var in registerAgentInput
			if err := json.Unmarshal(params.Arguments, &in); err != nil {
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
			writeToolResult(out, nil)
		case "wormhole.kb.search":
			if got := r.Header.Get("Authorization"); got != "Bearer sekrit-token" {
				t.Fatalf("kb.search Authorization header: got %q, want %q", got, "Bearer sekrit-token")
			}
			var in searchArticlesInput
			if err := json.Unmarshal(params.Arguments, &in); err != nil {
				t.Fatalf("decode search arguments: %v", err)
			}
			out, errResp := searchArticles(t, in)
			writeToolResult(out, errResp)
		case "wormhole.channel.list":
			if got := r.Header.Get("Authorization"); got != "Bearer sekrit-token" {
				t.Fatalf("channel.list Authorization header: got %q, want %q", got, "Bearer sekrit-token")
			}
			var out listChannelsOutput
			var errResp *callResponse
			if listChannels != nil {
				out, errResp = listChannels(t)
			} else {
				out = listChannelsOutput{
					Channels: []channelSummary{
						{ChannelID: "chan-1", Name: "introductions"},
					},
				}
			}
			writeToolResult(out, errResp)
		case "wormhole.channel.post":
			if got := r.Header.Get("Authorization"); got != "Bearer sekrit-token" {
				t.Fatalf("channel.post Authorization header: got %q, want %q", got, "Bearer sekrit-token")
			}
			var in postEventInput
			if err := json.Unmarshal(params.Arguments, &in); err != nil {
				t.Fatalf("decode post event arguments: %v", err)
			}
			var out postEventOutput
			var errResp *callResponse
			if postEvent != nil {
				out, errResp = postEvent(t, in)
			} else {
				out = postEventOutput{EventID: "evt-1"}
			}
			writeToolResult(out, errResp)
		case "wormhole.task.list":
			if got := r.Header.Get("Authorization"); got != "Bearer sekrit-token" {
				t.Fatalf("task.list Authorization header: got %q, want %q", got, "Bearer sekrit-token")
			}
			var out listTasksOutput
			var errResp *callResponse
			if listTasks != nil {
				out, errResp = listTasks(t)
			} else {
				out = listTasksOutput{Tasks: []taskSummary{}}
			}
			writeToolResult(out, errResp)
		default:
			t.Fatalf("unexpected tool: %s", params.Name)
		}
	}))
}
```

- [ ] **Step 4: Rewrite `TestRunJoin_ServerError_PrintsError`'s bespoke server**

Replace `main_test.go` lines 246-268 (`TestRunJoin_ServerError_PrintsError`) with:

```go
func TestRunJoin_ServerError_PrintsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		result := toolCallResult{
			Content: []toolCallResultContent{{Type: "text", Text: `{"error":"identity: invalid scope","code":"INVALID_SCOPE"}`}},
			IsError: true,
		}
		resultRaw, _ := json.Marshal(result)
		json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: resultRaw})
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
```

- [ ] **Step 5: Build and run the full package test suite**

Run: `go build ./... && go vet ./... && go test ./cmd/wormhole-cli/... -v`
Expected: build and vet clean; all tests PASS, including `TestRunJoin_Success_RegistersAndPersistsCredentials`, `TestRunJoin_ServerError_PrintsError`, `TestRunJoin_NetworkError_PrintsError`, `TestRunJoin_KBSync_UsesCapabilitiesAndRolesAsQuery`, `TestRunJoin_KBSync_ExplicitContextAndLimit`, and every other `TestRunJoin_*` test in the file (self-introduction, task-summary, error-path tests included — none of their signatures changed, only the transport underneath).

- [ ] **Step 6: Manually verify against a live server**

Run:
```bash
go run ./cmd/wormhole-server &
go run ./cmd/wormhole-cli join --server http://localhost:8080 --project proj-1 --owner harley --model claude --permissions task.create,kb.write
```
Expected: `Passport created.` and subsequent join-flow output print with no `404` or connection error (confirms the fix against the real server, not just the mocked test harness). Stop the background server afterward.

- [ ] **Step 7: Commit**

```bash
git add cmd/wormhole-cli/main.go cmd/wormhole-cli/main_test.go
git commit -m "fix(cli): migrate wormhole-cli to JSON-RPC /mcp endpoint

wormhole join was 404ing against any post-Chapter-3 server: callTool
still posted the deleted /mcp/tools/call envelope. Rewrites callTool to
send a JSON-RPC 2.0 tools/call request to /mcp (docs/mcp-protocol.md),
folding project_id into arguments per §4.1. No change to runJoin's
flags, output, or credential file format."
```

---

### Task 2: `docs/claude-code-connector.md`

**Files:**
- Create: `docs/claude-code-connector.md`

**Interfaces:**
- Consumes: `docs/mcp-protocol.md` (transport/auth decisions), `cmd/wormhole-cli`'s `join` command (Task 1's fixed version), `internal/types/config.go`'s `WORMHOLE_LISTEN_ADDR` env var (default `:8080`).
- Produces: nothing consumed by later tasks — this is a leaf doc.

- [ ] **Step 1: Write the doc**

Create `docs/claude-code-connector.md`:

```markdown
# Claude Code Connector Setup

Connects a Claude Code session to a running Wormhole server over the MCP Streamable HTTP
transport (`docs/mcp-protocol.md`).

## 1. Start the server

```bash
go run ./cmd/wormhole-server
```

Defaults to `:8080` (override with `WORMHOLE_LISTEN_ADDR`). Requires a reachable Postgres
instance (`WORMHOLE_DATABASE_URL`, see `internal/types/config.go`).

## 2. Join the project and obtain a token

`wormhole join` calls `wormhole.agent.register` (no auth required) and writes the issued
bearer token to `~/.wormhole/credentials.json` (or `--token-file <path>`):

```bash
go run ./cmd/wormhole-cli join \
  --server http://localhost:8080 \
  --project <project-id> \
  --owner <your-name> \
  --model claude-code \
  --permissions task.create,task.read,kb.write,kb.read,channel.read,channel.post
```

This also runs the rest of the join flow (RFC-0001 §8.5): a KB sync search, a self-introduction
post to the `#introductions` channel, and an open-task summary. The token in the credentials
file is what a live MCP client authenticates with — the connector step below doesn't read
`~/.wormhole/credentials.json` for you; carry the token manually into Claude Code's config.

## 3. Register the connector in Claude Code

```bash
claude mcp add --transport http wormhole http://localhost:8080/mcp
```

If your server requires bearer auth for the tools you intend to call, supply the token issued
in step 2 as an `Authorization: Bearer <token>` header via Claude Code's connector auth config
(`wormhole.agent.register` itself is unauthenticated, but every other tool requires the token).

## 4. Verify

- `claude mcp list` should show `wormhole` as connected.
- Ask Claude Code to list Wormhole tools — it should enumerate all registered tools (from
  `tools/list`, `internal/mcp/jsonrpc.go`'s `HandleToolsList`).
- Ask it to call `wormhole.task.list` for your project — it should round-trip a real answer
  from the live server, not a mock.

## Troubleshooting

- **`404` or connection refused calling any tool:** confirm the server is running and the
  connector URL ends in `/mcp` (not `/mcp/tools/call` — that path was removed in Chapter 3).
- **`-32001 invalid or expired token`:** the bearer token wasn't supplied, doesn't match an
  issued passport, or has expired — re-run `wormhole join` to issue a fresh one.
- **`GET /mcp` returns `405`:** expected. This server doesn't implement the SSE server-push
  stream (`docs/mcp-protocol.md` §2) — no current consumer needs it in alpha 2 scope.
- **Tool call returns a result with `isError: true` instead of failing the RPC call:** this is
  the tool's own handler rejecting the input (e.g. invalid task status), not a transport
  problem — read the `content[0].text` message (`docs/mcp-protocol.md` §3.1).
```

- [ ] **Step 2: Commit**

```bash
git add docs/claude-code-connector.md
git commit -m "docs: add Claude Code connector setup guide (Chapter 4)"
```

---

## After this plan: human-run steps (not coded tasks)

`ROADMAP-ALPHA2.md` Chapter 4 has two remaining checkboxes this plan does not execute, since
they require an interactive Claude Code session, not a subagent:

- **Live connector test:** `claude mcp add --transport http wormhole http://localhost:8080/mcp`
  against a running `wormhole-server` (Task 1 fixes the blocker that made this 404 before);
  confirm `/mcp` lists all registered tools and a real call round-trips.
- **M1 review/demo:** a real Claude Code session (not a Go test) calls `wormhole.task.list` and
  gets a real answer — M1 exit bar.

Hand these to the human partner once Tasks 1-2 are reviewed and merged.
