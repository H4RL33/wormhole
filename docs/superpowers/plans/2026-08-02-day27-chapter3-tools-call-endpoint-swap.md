# Chapter 3 — `tools/call` Handler, `/mcp` Endpoint Swap, Test Migration

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement `ROADMAP-ALPHA2.md` Chapter 3: the `tools/call` JSON-RPC method handler, the single `/mcp` Streamable HTTP endpoint replacing `/mcp/tools` + `/mcp/tools/call` (no back-compat shim — this is pre-1.0 per `docs/mcp-protocol.md` §1), and migration of every test that exercises the old envelope onto the new one.

**Architecture:** All new/changed code lives in `internal/mcp` and `cmd/wormhole-server/main.go`. No new external Go dependency (R4).

**Scope correction vs. the roadmap text:** `ROADMAP-ALPHA2.md` names `e2e_test.go`, `m1/m2/m3_integration_test.go`, `hardening_test.go`, `audit_test.go` as the tests to migrate. Verified against the actual codebase: `audit_test.go` calls `tool.Handler` directly and never references `CallRequest`/`CallResponse`/`NewCallHandler` — it needs no changes. Two files the roadmap text omits DO reference those types and would fail to compile once they're removed: `kb_test.go` and `task_test.go`. The real migration set, verified by `grep -rl "NewCallHandler\|CallRequest\|CallResponse" internal/mcp/*_test.go`, is: `e2e_test.go`, `hardening_test.go`, `kb_test.go`, `m1_integration_test.go`, `m2_integration_test.go`, `m3_integration_test.go`, `server_test.go`, `task_test.go`, `v1_exit_criteria_test.go` (9 files). This plan's test tasks target that verified set, not the roadmap's literal list.

## Global Constraints

- Authority order: RFC-0001 > RFC-0002 > `docs/architecture.md` > existing code > `docs/mcp-protocol.md` (frozen Chapter 1 design) > this chapter's own new decisions (flagged inline as inference, same convention Chapter 1 and Chapter 2 used).
- `docs/mcp-protocol.md` §3.1 is load-bearing verbatim (already implemented as constants in `internal/mcp/jsonrpc.go` — `RPCParseError`, `RPCInvalidRequest`, `RPCMethodNotFound`, `RPCInvalidParams`, `RPCInternalError` — reuse them, do not redefine).
- `docs/mcp-protocol.md` §3: tool-level failure is `{"content":[{"type":"text","text":"<error message>"}],"isError":true}` inside a **successful** RPC `result`, not an RPC `error`. Auth failure on `tools/call` IS an RPC error (§5): missing token → `{"code": -32602, "message": "missing bearer token"}`; invalid/expired token → `{"code": -32001, "message": "invalid or expired token"}` (new code, in the -32000..-32099 implementation-defined range, first free slot per §5 — already named in the frozen doc, not this chapter's invention).
- `docs/mcp-protocol.md` §4.1: `tools/call` request `params` is `{"name": "...", "arguments": {...}}`; `project_id` is read out of `arguments`, not a sibling envelope field. Unknown tool name is this chapter's own inference (not decided in Chapter 1): treat it as `-32602 Invalid params` (`"unknown tool: <name>"`), consistent with the doc's own `-32602` example row ("`tools/call` missing `name`" is also a params-shape failure) — document this as a flagged inference in the handler's doc comment, do not silently invent an undocumented behavior.
- **HTTP status code decision (this chapter's own inference, not in `docs/mcp-protocol.md` — flag it there in the same change):** every well-formed JSON-RPC exchange over `POST /mcp` returns HTTP `200 OK`, success or RPC-level error alike — the JSON-RPC `error` object in the body carries the failure, matching how the real MCP Streamable HTTP transport behaves in practice (a transport-level HTTP status per RPC error code was never a documented requirement, and REST-style status-code-per-error-type is what Chapter 1's replaced bespoke shape did — this chapter deliberately does not carry that forward, since a real MCP client keys off the JSON-RPC envelope, not HTTP status). Exceptions, both already fixed by Chapter 1's doc: `GET /mcp` → `405 Method Not Allowed`; a notification (no `id`) → `202 Accepted`, empty body. Add this HTTP-status decision to `docs/mcp-protocol.md` §2 in Task 2's commit (one paragraph, not a rewrite).
- A **notification** is any parsed request with no `id` field or `id: null`. Only `notifications/initialized` is anticipated in current scope (§3 of the doc names it explicitly) — the dispatcher does not need method-specific notification handling, just: if it's a notification, do not attempt to produce a `result`/`error` body at all, respond `202` empty, regardless of what `method` says (even an unrecognized notification method gets `202`, not `-32601`, since there is no `id` to attach an error response to per JSON-RPC 2.0's own notification semantics).
- Auth resolution logic is unchanged from `internal/mcp/server.go`'s current `NewCallHandler` (`bearerToken()` header parsing, `identityStore.WhoAmI(ctx, projectID, token)`, `AuthenticatedScope`) — reuse `bearerToken()` as-is (keep it in `server.go` or move it to `jsonrpc.go`, implementer's call, but do not duplicate the header-parsing logic in two places).
- No back-compat shim (`docs/mcp-protocol.md` §1, pre-1.0): delete `CallRequest`, `CallResponse`, `NewCallHandler`, `writeCallResponse` from `internal/mcp/server.go` once nothing references them. If `bearerToken()` is the only survivor, it can stay in `server.go` or move — implementer's call, but `server.go` should end up small/empty of dead code, not left half-gutted.
- `cmd/wormhole-server/main.go`: remove the `/mcp/tools` and `/mcp/tools/call` `mux.HandleFunc` registrations; add a single `mux.HandleFunc("/mcp", ...)` mounting the new dispatcher. `/healthz` is untouched.
- `go build ./...`, `go vet ./...`, `go test ./...` must pass before any commit (T4). DB-backed tests need Postgres reachable; note any DB-unavailability skips separately from real regressions in every report.

---

### Task 1: `HandleToolsCall` — business logic in `internal/mcp/jsonrpc.go`

**Files:**
- Modify: `internal/mcp/jsonrpc.go` (add the handler + supporting types; do not touch `HandleInitialize`/`HandleToolsList`/the reflection helpers from Chapter 2)
- Create: `internal/mcp/jsonrpc_toolscall_test.go`

**Interfaces:**
- Consumes: `Registry.Get(name)` (existing), `identity.Store.WhoAmI` (existing, via a passed `*identity.Store`), `bearerToken()` (existing, `internal/mcp/server.go` — read it, do not copy its logic elsewhere).
- Produces: `HandleToolsCall(ctx context.Context, registry *Registry, identityStore *identity.Store, authHeader string, params json.RawMessage) (result any, rpcErr *RPCError)` — a pure function, no `http.ResponseWriter`/`*http.Request` dependency, mirroring how `HandleInitialize`/`HandleToolsList` are plain functions (Task 2's HTTP dispatcher calls this and the two Chapter 2 handlers uniformly). Returns exactly one of `result` (the MCP content-wrapper value, `{"content":[...], "isError": bool}`) or `rpcErr` (a JSON-RPC protocol-level error) — never both.

- [ ] **Step 1: `toolsCallParams` request/response shapes**

```go
// toolsCallParams is the tools/call method's params shape
// (docs/mcp-protocol.md §4).
type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// toolCallResultContent is the MCP content-wrapper item type.
type toolCallResultContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// toolCallResult is the tools/call result shape wrapping a tool's own
// output or failure message (docs/mcp-protocol.md §3, §4).
type toolCallResult struct {
	Content []toolCallResultContent `json:"content"`
	IsError bool                    `json:"isError,omitempty"`
}
```

- [ ] **Step 2: `HandleToolsCall`**

```go
// HandleToolsCall implements the JSON-RPC "tools/call" method
// (docs/mcp-protocol.md §4, §4.1, §5). project_id is read out of
// arguments per §4.1 — there is no sibling envelope field. Unknown tool
// name is treated as -32602 Invalid params (flagged inference,
// docs/mcp-protocol.md doesn't decide this case explicitly; consistent
// with the doc's own example of a params-shape failure). Auth failure
// (missing/invalid token) is an RPC error per §5; a tool's own handler
// returning an error is NOT an RPC error — it's a successful result with
// isError: true (§3).
func HandleToolsCall(ctx context.Context, registry *Registry, identityStore *identity.Store, authHeader string, rawParams json.RawMessage) (any, *RPCError) {
	var params toolsCallParams
	if err := json.Unmarshal(rawParams, &params); err != nil || params.Name == "" {
		return nil, &RPCError{Code: RPCInvalidParams, Message: "tools/call requires params.name"}
	}

	tool, ok := registry.Get(params.Name)
	if !ok {
		return nil, &RPCError{Code: RPCInvalidParams, Message: "unknown tool: " + params.Name}
	}

	projectID, err := extractProjectID(params.Arguments)
	if err != nil {
		return nil, &RPCError{Code: RPCInvalidParams, Message: err.Error()}
	}

	var scope *identity.AuthenticatedScope
	if tool.RequiresAuth {
		token := bearerToken(authHeader)
		if token == "" {
			return nil, &RPCError{Code: RPCInvalidParams, Message: "missing bearer token"}
		}
		resolved, err := identityStore.WhoAmI(ctx, projectID, token)
		if errors.Is(err, identity.ErrInvalidToken) {
			return nil, &RPCError{Code: -32001, Message: "invalid or expired token"}
		}
		if err != nil {
			return nil, &RPCError{Code: RPCInternalError, Message: "auth resolution failed"}
		}
		scope = &resolved
	}

	result, err := tool.Handler(ctx, scope, projectID, params.Arguments)
	if err != nil {
		return toolCallResult{
			Content: []toolCallResultContent{{Type: "text", Text: err.Error()}},
			IsError: true,
		}, nil
	}

	resultJSON, err := json.Marshal(result)
	if err != nil {
		return nil, &RPCError{Code: RPCInternalError, Message: "encode tool result"}
	}
	return toolCallResult{
		Content: []toolCallResultContent{{Type: "text", Text: string(resultJSON)}},
	}, nil
}

// extractProjectID reads project_id out of a tools/call arguments object
// (docs/mcp-protocol.md §4.1 — project_id lives inside arguments, not a
// sibling envelope field). Missing project_id is a params-shape failure:
// every project-scoped tool's inputSchema marks it required (Chapter 2's
// tools/list), so a caller omitting it has violated the advertised
// schema.
func extractProjectID(arguments json.RawMessage) (string, error) {
	var probe struct {
		ProjectID string `json:"project_id"`
	}
	if err := json.Unmarshal(arguments, &probe); err != nil {
		return "", fmt.Errorf("decode arguments: %w", err)
	}
	return probe.ProjectID, nil
}
```

Note: `wormhole.agent.whoami` takes no `project_id` (Chapter 2's schema already excludes it). `extractProjectID` returning `""` for that tool is correct — `WhoAmITool`'s handler ignores its `projectID` parameter entirely (ADR: it derives everything from the resolved `scope`). Do not special-case whoami in `HandleToolsCall` — this already works because the handler signature always takes `projectID` and whoami's handler simply doesn't use it.

Add `context`, `errors`, `fmt` imports to `jsonrpc.go` alongside the existing ones.

- [ ] **Step 3: Tests** (`internal/mcp/jsonrpc_toolscall_test.go`, DB-backed where auth is exercised — follow `server_test.go`'s `testIdentityStore(t)` pattern)

- `TestHandleToolsCall_UnknownTool`: registry with no tools, call `HandleToolsCall` with `name: "wormhole.nonexistent.tool"`, assert `rpcErr.Code == RPCInvalidParams`.
- `TestHandleToolsCall_MissingName`: `rawParams` is `{"arguments":{}}` (no `name`), assert `RPCInvalidParams`.
- `TestHandleToolsCall_NoAuthRequiredDispatchesDirectly`: register a fake tool with `RequiresAuth: false`, assert `scope` passed to the handler is `nil`, `projectID` matches what was in `arguments.project_id`, and the returned result is `toolCallResult{Content: [...], IsError: false}` with `Content[0].Text` being the JSON-encoded handler return value.
- `TestHandleToolsCall_MissingBearerToken`: tool with `RequiresAuth: true`, empty `authHeader`, assert `rpcErr.Code == RPCInvalidParams`, message contains "missing bearer token".
- `TestHandleToolsCall_InvalidBearerToken`: real `identityStore` (`testIdentityStore(t)`), tool `RequiresAuth: true`, `authHeader: "Bearer not-a-real-token"`, assert `rpcErr.Code == -32001`.
- `TestHandleToolsCall_ToolHandlerErrorIsIsError`: register a fake tool whose `Handler` returns `(nil, errors.New("boom"))`, `RequiresAuth: false`, assert `rpcErr == nil` (NOT an RPC error) and the returned `toolCallResult.IsError == true`, `Content[0].Text == "boom"`.
- `TestHandleToolsCall_RealToolEndToEnd`: register `RegisterAgentTool` + `WhoAmITool` against real `testIdentityStore(t)`/`testEventsStore(t)` (mirror `e2e_test.go`'s existing fixtures — read that file for `mustCreateProject`/`testEventsStore` helpers before writing this test, they already exist), call `wormhole.agent.register` with `arguments` containing `project_id`, decode the resulting `Content[0].Text` back into `RegisterAgentOutput`, assert non-empty `Token`; then call `wormhole.agent.whoami` with `authHeader: "Bearer " + token`, assert the resulting `AgentID` matches.

Run `go test ./internal/mcp/... -run TestHandleToolsCall -v` and confirm all pass, then `go build ./... && go vet ./...`.

- [ ] **Step 4: Commit**

```bash
git add internal/mcp/jsonrpc.go internal/mcp/jsonrpc_toolscall_test.go
git commit -m "feat(mcp): tools/call JSON-RPC method handler (Chapter 3)"
```

---

### Task 2: `/mcp` HTTP dispatcher, mount it, delete the old envelope

**Files:**
- Modify: `internal/mcp/jsonrpc.go` (add the HTTP dispatcher function)
- Modify: `internal/mcp/server.go` (delete `CallRequest`, `CallResponse`, `NewCallHandler`, `writeCallResponse`; keep `bearerToken` — move it to `jsonrpc.go` if that reads more cleanly given `server.go` would otherwise be near-empty, implementer's call)
- Modify: `cmd/wormhole-server/main.go` (replace `/mcp/tools` + `/mcp/tools/call` registrations with one `/mcp` registration)
- Modify: `docs/mcp-protocol.md` (add the one-paragraph HTTP-status-code decision from this plan's Global Constraints to §2; this is a doc update landing in this task's commit since it's this chapter's own inference, same convention as Chapter 2's protocolVersion update)
- Create: `internal/mcp/jsonrpc_dispatch_test.go`

**Interfaces:**
- Consumes: `HandleInitialize()`, `HandleToolsList(registry)` (Chapter 2), `HandleToolsCall(...)` (Task 1), `RPCRequest`/`RPCResponse`/`RPCError`/error code constants (Chapter 2).
- Produces: `NewMCPHandler(registry *Registry, identityStore *identity.Store) http.HandlerFunc` — the single handler `main.go` mounts at `/mcp` for both `GET` and `POST` (it decides per-method internally).

- [ ] **Step 1: The dispatcher**

```go
// NewMCPHandler builds the single /mcp Streamable HTTP endpoint
// (docs/mcp-protocol.md §2): POST carries JSON-RPC requests/notifications,
// GET is reserved for a server-push SSE stream this server doesn't
// implement yet (405, per §2 — no current consumer, docs/architecture.md
// §0.5 smallest correct diff).
func NewMCPHandler(registry *Registry, identityStore *identity.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var req RPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeRPCResponse(w, RPCResponse{JSONRPC: "2.0", Error: &RPCError{Code: RPCParseError, Message: "parse error"}})
			return
		}

		isNotification := len(req.ID) == 0 || string(req.ID) == "null"

		if req.JSONRPC != "2.0" || req.Method == "" {
			if isNotification {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			writeRPCResponse(w, RPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &RPCError{Code: RPCInvalidRequest, Message: "invalid request"}})
			return
		}

		if isNotification {
			// No result/error is ever produced for a notification — the
			// method (e.g. notifications/initialized) is acknowledged
			// with an empty 202, never dispatched to a method handler
			// that expects to answer (docs/mcp-protocol.md §3).
			w.WriteHeader(http.StatusAccepted)
			return
		}

		var result any
		var rpcErr *RPCError
		switch req.Method {
		case "initialize":
			result = HandleInitialize()
		case "tools/list":
			result = HandleToolsList(registry)
		case "tools/call":
			result, rpcErr = HandleToolsCall(r.Context(), registry, identityStore, r.Header.Get("Authorization"), req.Params)
		default:
			rpcErr = &RPCError{Code: RPCMethodNotFound, Message: "method not found: " + req.Method}
		}

		writeRPCResponse(w, RPCResponse{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rpcErr})
	}
}

func writeRPCResponse(w http.ResponseWriter, resp RPCResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}
```

Adjust for `gofmt`/exact Go idiom as needed — this is the field/control-flow content, not a literal gofmt-clean block. Add `net/http` import to `jsonrpc.go`.

- [ ] **Step 2: Delete the old envelope from `server.go`**

Remove `CallRequest`, `CallResponse`, `NewCallHandler`, `writeCallResponse` entirely. Keep `bearerToken` (used by `HandleToolsCall`) — leave it in `server.go` if anything else remains there worth the file, otherwise move it into `jsonrpc.go` and delete `server.go` (implementer's call; if `server.go` ends up empty, delete the file rather than leave a near-empty package file sitting around).

- [ ] **Step 3: `cmd/wormhole-server/main.go`**

Replace:
```go
	mux.HandleFunc("/mcp/tools", func(w http.ResponseWriter, r *http.Request) { ... })
	mux.HandleFunc("/mcp/tools/call", mcp.NewCallHandler(registry, identityStore))
```
with:
```go
	mux.HandleFunc("/mcp", mcp.NewMCPHandler(registry, identityStore))
```
Remove the now-unused `encoding/json` import from `main.go` if nothing else in that file uses it (check before removing).

- [ ] **Step 4: `docs/mcp-protocol.md` update**

Add one paragraph to §2 stating the HTTP-status decision from this plan's Global Constraints (every well-formed JSON-RPC exchange returns HTTP 200 regardless of RPC-level success/error; `GET /mcp` → 405; notification → 202 empty). Do not rewrite the section, append/integrate the paragraph.

- [ ] **Step 5: Tests** (`internal/mcp/jsonrpc_dispatch_test.go`)

- `TestMCPHandler_Initialize`: POST `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, assert HTTP 200, decode `RPCResponse`, assert `Result` matches `HandleInitialize()`'s shape (spot-check `protocolVersion`/`serverInfo.name`).
- `TestMCPHandler_ToolsList`: POST `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` against a registry with 1 registered tool, assert HTTP 200, `Result` contains that tool.
- `TestMCPHandler_UnknownMethod`: POST `{"jsonrpc":"2.0","id":1,"method":"bogus/method"}`, assert HTTP 200, `RPCResponse.Error.Code == RPCMethodNotFound`.
- `TestMCPHandler_ParseError`: POST `not valid json`, assert HTTP 200, `RPCResponse.Error.Code == RPCParseError`.
- `TestMCPHandler_InvalidRequest`: POST `{"method":"initialize"}` (missing `jsonrpc`), assert HTTP 200, `RPCResponse.Error.Code == RPCInvalidRequest`.
- `TestMCPHandler_Notification`: POST `{"jsonrpc":"2.0","method":"notifications/initialized"}` (no `id` field at all), assert HTTP 202, empty body.
- `TestMCPHandler_GetMethodNotAllowed`: `GET /mcp`, assert HTTP 405.
- `TestMCPHandler_ToolsCallRoutesThrough`: register a no-auth fake tool, POST a `tools/call` request, assert HTTP 200 and `Result` is the expected `toolCallResult` shape (cheap integration check that the dispatcher's `tools/call` case actually calls `HandleToolsCall` — deep coverage of `HandleToolsCall` itself is Task 1's job, not this test's).

Run `go test ./internal/mcp/... -run TestMCPHandler -v`, then full `go build ./... && go vet ./... && go test ./internal/mcp/...` (Task 3/4 haven't migrated the old tests yet at this point in sequence — expect compile failures in the 9 files still referencing `CallRequest`/`CallResponse`/`NewCallHandler` until those tasks land. This task's own new files must compile and its own new tests must pass; the full-package build only turns green after Task 4. State this expected interim state clearly in the report so it isn't mistaken for a regression.)

- [ ] **Step 6: Commit**

```bash
git add internal/mcp/jsonrpc.go internal/mcp/server.go internal/mcp/jsonrpc_dispatch_test.go cmd/wormhole-server/main.go docs/mcp-protocol.md
git commit -m "feat(mcp): single /mcp Streamable HTTP endpoint, remove old envelope (Chapter 3)"
```

(This commit leaves the package non-compiling for the 9 test files still on the old envelope — expected, resolved by Tasks 3-4 immediately after. Do not let this block the commit; the roadmap's own Chapter 3 ordering puts the endpoint swap before test migration.)

---

### Task 3: Test helper + migrate `server_test.go`, `e2e_test.go`

**Files:**
- Create: `internal/mcp/jsonrpc_test_helpers_test.go`
- Modify: `internal/mcp/server_test.go`
- Modify: `internal/mcp/e2e_test.go`

**Interfaces:**
- Consumes: `NewMCPHandler` (Task 2), `RPCRequest`/`RPCResponse`/`RPCError` (Chapter 2).
- Produces: two test helpers every remaining migration task (Task 4) also uses — get the names and signatures right here since Task 4's brief will reference them by name, not redefine them.

- [ ] **Step 1: Helpers**

```go
// toolsCallRPC posts a tools/call JSON-RPC request to srv, merging
// projectID into arguments per docs/mcp-protocol.md §4.1 (project_id
// lives inside arguments, not a sibling field — this helper exists so
// call sites don't hand-roll that merge). Returns the raw HTTP status and
// decoded RPCResponse for callers that need to assert on protocol-level
// failure (RPCResponse.Error) or a tool-level failure
// (result.isError) without the helper pre-judging pass/fail.
func toolsCallRPC(t *testing.T, srv *httptest.Server, token, toolName, projectID string, arguments json.RawMessage) (int, RPCResponse) {
	t.Helper()
	merged := mergeProjectID(t, arguments, projectID)
	reqBody, _ := json.Marshal(RPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "tools/call",
		Params:  mustMarshal(t, toolsCallParams{Name: toolName, Arguments: merged}),
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("tools/call POST: %v", err)
	}
	defer resp.Body.Close()
	var rpcResp RPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		t.Fatalf("decode tools/call response: %v", err)
	}
	return resp.StatusCode, rpcResp
}

// mustToolResult calls toolsCallRPC and asserts both RPC-level and
// tool-level success, then returns the tool's own JSON result bytes
// (Content[0].Text) ready for the caller to unmarshal into a specific
// Output struct — the common "happy path decode" pattern most existing
// tests use.
func mustToolResult(t *testing.T, srv *httptest.Server, token, toolName, projectID string, arguments json.RawMessage) json.RawMessage {
	t.Helper()
	status, rpcResp := toolsCallRPC(t, srv, token, toolName, projectID, arguments)
	if status != http.StatusOK {
		t.Fatalf("tools/call %s: HTTP status got %d, want 200", toolName, status)
	}
	if rpcResp.Error != nil {
		t.Fatalf("tools/call %s: unexpected RPC error: %+v", toolName, rpcResp.Error)
	}
	var result toolCallResult
	if err := json.Unmarshal(mustMarshal(t, rpcResp.Result), &result); err != nil {
		t.Fatalf("tools/call %s: decode result wrapper: %v", toolName, err)
	}
	if result.IsError {
		t.Fatalf("tools/call %s: tool returned isError: %s", toolName, result.Content[0].Text)
	}
	return json.RawMessage(result.Content[0].Text)
}

// mergeProjectID adds project_id into a raw JSON arguments object
// (docs/mcp-protocol.md §4.1). arguments must already be a JSON object
// (possibly `{}`).
func mergeProjectID(t *testing.T, arguments json.RawMessage, projectID string) json.RawMessage {
	t.Helper()
	m := map[string]json.RawMessage{}
	if len(arguments) > 0 {
		if err := json.Unmarshal(arguments, &m); err != nil {
			t.Fatalf("mergeProjectID: decode arguments: %v", err)
		}
	}
	m["project_id"] = mustMarshal(t, projectID)
	return mustMarshal(t, m)
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
```

Add whatever imports are needed (`bytes`, `encoding/json`, `net/http`, `net/http/httptest`, `testing`).

- [ ] **Step 2: Migrate `server_test.go`**

Translate each test's mechanics, keep each test's actual assertion intent identical (per Global Constraints' HTTP-status decision, auth/unknown-tool failures now assert on `RPCResponse.Error.Code`, not HTTP status — HTTP status is always 200 for a well-formed RPC exchange):

- `TestCallHandler_UnknownTool`: build `srv := httptest.NewServer(NewMCPHandler(registry, store))`, call `toolsCallRPC(t, srv, "", "wormhole.agent.nonexistent", "00000000-0000-0000-0000-000000000000", json.RawMessage(`{}`))`, assert `status == http.StatusOK` and `rpcResp.Error.Code == RPCInvalidParams`.
- `TestCallHandler_RequiresAuthMissingToken`: same pattern, empty token, assert `rpcResp.Error.Code == RPCInvalidParams`, message contains "missing bearer token".
- `TestCallHandler_RequiresAuthInvalidToken`: token `"not-a-real-token"`, assert `rpcResp.Error.Code == -32001`.
- `TestCallHandler_NoAuthRequiredDispatchesDirectly`: use `mustToolResult`, keep the `scope == nil` / `projectID == "proj-1"` assertions inside the registered fake tool's `Handler` closure exactly as today (that check doesn't change).
- `TestToolsDiscoveryEndpoint`: this test currently checks two things — direct `registry.List()` marshaling (keep as-is, unrelated to the endpoint), and an inline HTTP handler literal copy-pasting `main.go`'s old `/mcp/tools` logic (delete that second half entirely; it was testing dead code that no longer exists in `main.go`). If the discovery-endpoint behavior still needs an HTTP-level test, that's `tools/list` — already covered by Task 2's `TestMCPHandler_ToolsList` in `jsonrpc_dispatch_test.go`, don't duplicate it here.

- [ ] **Step 3: Migrate `e2e_test.go`**

Both tests (`TestE2E_RegisterThenWhoAmI`, `TestE2E_WhoAmI_RejectsExpiredToken`) follow the same shape: `httptest.NewServer(NewMCPHandler(registry, store))` instead of `NewCallHandler`; replace `CallRequest`/`CallResponse` marshal/decode blocks with `mustToolResult`/`toolsCallRPC` calls. `TestE2E_RegisterThenWhoAmI` uses `mustToolResult` for both calls (both expected to succeed) and unmarshals into `RegisterAgentOutput`/`WhoAmIOutput` exactly as before. `TestE2E_WhoAmI_RejectsExpiredToken`'s final assertion changes from `whoamiResp.StatusCode != http.StatusUnauthorized` to `toolsCallRPC(...)`'s returned `rpcResp.Error.Code != -32001` (HTTP status is 200 now, not 401 — the expired-token rejection is proven by the RPC error code instead).

- [ ] **Step 4: Test**

`go test ./internal/mcp/... -run 'TestCallHandler|TestToolsDiscoveryEndpoint|TestE2E' -v`. Full package build/test will still fail on the remaining 7 unmigrated files (`hardening_test.go`, `kb_test.go`, `m1/m2/m3_integration_test.go`, `task_test.go`, `v1_exit_criteria_test.go`) — Task 4's job. State this expected interim state in the report.

- [ ] **Step 5: Commit**

```bash
git add internal/mcp/jsonrpc_test_helpers_test.go internal/mcp/server_test.go internal/mcp/e2e_test.go
git commit -m "test(mcp): migrate server_test.go and e2e_test.go to JSON-RPC /mcp endpoint (Chapter 3)"
```

---

### Task 4: Migrate the remaining 7 test files

**Files:**
- Modify: `internal/mcp/hardening_test.go`
- Modify: `internal/mcp/kb_test.go`
- Modify: `internal/mcp/m1_integration_test.go`
- Modify: `internal/mcp/m2_integration_test.go`
- Modify: `internal/mcp/m3_integration_test.go`
- Modify: `internal/mcp/task_test.go`
- Modify: `internal/mcp/v1_exit_criteria_test.go`

**Interfaces:**
- Consumes: `toolsCallRPC`, `mustToolResult`, `mergeProjectID`, `mustMarshal` (Task 3, `internal/mcp/jsonrpc_test_helpers_test.go` — read that file first, do not redefine these helpers here) and `NewMCPHandler` (Task 2).

- [ ] **Step 1: Read every target file's current usage before editing**

For each of the 7 files, grep its `NewCallHandler`/`CallRequest`/`CallResponse` call sites first (`grep -n "NewCallHandler\|CallRequest\|CallResponse" internal/mcp/<file>`) to see the exact pattern in use — most will match `server_test.go`/`e2e_test.go`'s already-migrated shape closely (Task 3's commit is your reference implementation, read it before starting this task).

- [ ] **Step 2: Mechanical translation recipe**

For every call site:
- `handler := NewCallHandler(registry, store)` → `handler := NewMCPHandler(registry, store)` (same registry/store args, different constructor).
- `json.Marshal(CallRequest{Tool: X, ProjectID: Y, Arguments: Z})` + manual POST + `CallResponse` decode → replace with `mustToolResult(t, srv, token, X, Y, Z)` when the test expects success and only cares about the tool's result payload, or `toolsCallRPC(t, srv, token, X, Y, Z)` when the test is itself asserting on a failure mode (auth rejection, unknown tool, tool-level error) and needs the raw `RPCResponse`/status.
- Any assertion of the old bespoke HTTP status codes (404 unknown tool, 401 auth failure, 400 bad request) becomes an assertion on `RPCResponse.Error.Code` instead (`RPCInvalidParams` for the first two per this chapter's decisions in Task 1, `-32001` specifically for invalid/expired token) — HTTP status is uniformly 200 for any well-formed RPC exchange now (this chapter's Global Constraints decision).
- A tool-level failure the old code surfaced via `CallResponse.Error` (a non-empty string, HTTP 400 from the old `NewCallHandler`) is now `toolCallResult.IsError == true` with the message in `Content[0].Text` — decode `rpcResp.Result` into a `toolCallResult` to check this (see `mustToolResult`'s internals in Task 3 for the exact unmarshal pattern to mirror when a test needs to assert the failure case specifically rather than treat it as fatal).

- [ ] **Step 3: Test**

Run per-file as each is migrated: `go test ./internal/mcp/... -run '<TestNamesInThatFile>' -v`. After all 7 are migrated, run the full package: `go build ./... && go vet ./... && go test ./internal/mcp/...` and confirm everything passes — this is the point where the whole package finally compiles and goes green again after Task 2 broke it. Also run `go build ./...` at the repo root (covers `cmd/wormhole-server`) to catch any stray reference to the deleted `mcp.CallRequest`/`mcp.NewCallHandler` outside `internal/mcp` itself.

- [ ] **Step 4: Commit**

```bash
git add internal/mcp/hardening_test.go internal/mcp/kb_test.go internal/mcp/m1_integration_test.go internal/mcp/m2_integration_test.go internal/mcp/m3_integration_test.go internal/mcp/task_test.go internal/mcp/v1_exit_criteria_test.go
git commit -m "test(mcp): migrate remaining test files to JSON-RPC /mcp endpoint (Chapter 3)"
```

---

## Self-Review Notes (for the plan author, not a task step)

- Spec coverage: `ROADMAP-ALPHA2.md` Chapter 3 has exactly three checklist items — `tools/call` handler (Task 1), endpoint replacement (Task 2), test migration (Tasks 3-4, split because 9 files is too much for one reviewable diff, and Task 3 establishes the shared helper Task 4 depends on).
- The roadmap's named test-file list was verified against the actual codebase and corrected (see the plan header) — `audit_test.go` needs nothing, `kb_test.go`/`task_test.go` were missing from the roadmap's list but do need migration. This is flagged explicitly rather than silently deviating from the roadmap text.
- Two chapter-local design decisions not settled by `docs/mcp-protocol.md` (unknown-tool-name error code, uniform HTTP 200 status policy) are documented as flagged inferences both in code comments and via a doc update in Task 2 — consistent with how Chapter 1 handled its own inferences, so Chapter 4's connector work and any later reader of `docs/mcp-protocol.md` sees the full frozen decision set in one place, not split across a doc and an unlogged implementation choice.
- Task 2 deliberately leaves the package non-compiling until Task 4 lands (old test files still reference deleted types) — called out explicitly in Task 2 and Task 3's test steps so neither implementer nor reviewer mistakes the expected interim `go build` failure for a regression they introduced.
