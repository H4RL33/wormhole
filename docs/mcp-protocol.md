# Wormhole MCP Protocol — Transport & Auth

**Implementation status (RFC-0001 §9):** the MCP tool surface is "indicative, not finalised."
Tool *names* below are fixed by the RFC's naming grammar. The JSON-RPC envelope, field
placement, and error-code mapping document the transport contract implemented today. Future
transport changes that remain undecided after consulting the RFC and existing code must follow
the ambiguity ladder in `docs/implementation-rules.md` §2.4.

## 1. Transport contract

`fabric` exposes a single JSON-RPC 2.0 MCP endpoint at `/mcp`. The contract below
keeps the server compatible with standard MCP clients, including Claude Code, without a
custom tool-call envelope.

## 2. Transport: Streamable HTTP, single `/mcp` endpoint

- One HTTP route, `/mcp`, replacing both `/mcp/tools` and `/mcp/tools/call`.
- `POST /mcp`: client-to-server JSON-RPC 2.0 requests and notifications, one JSON-RPC message
  per HTTP request body (batching is not currently required — Wormhole has no server-initiated
  requests yet, so batched responses add complexity with no consumer).
- `GET /mcp`: reserved for the server-to-client SSE stream the Streamable HTTP transport spec
  defines for server-initiated messages. Wormhole has no server-initiated MCP messages in
  the current implementation (no sampling requests, no server notifications), so this route
  returns `405 Method Not Allowed`. The Claude Code connector uses the local `wormhole mcp`
  stdio bridge and does not depend on a server-to-client SSE stream. Building a real SSE
  stream for zero current consumers would violate the smallest-correct-diff rule in
  `docs/implementation-rules.md` §2.5.
- Every request and response body is `Content-Type: application/json`.

HTTP status codes carry only transport-level meaning, never RPC-level outcome: every
well-formed JSON-RPC request/response exchange over `POST /mcp` returns HTTP `200 OK`
regardless of whether the RPC call succeeds or the response body's `error` field is populated
(a JSON-RPC error is still valid JSON-RPC, decoded from a normal 200 body). `GET /mcp` always
returns `405 Method Not Allowed` (no SSE stream implemented, see below). A notification (no
`id` field) gets no response body at all — HTTP `202 Accepted`, empty, since there is nothing
to decode. Malformed input that fails before a `RPCRequest` can even be decoded (invalid JSON)
still returns `200` with a JSON-RPC `-32700` error body, not an HTTP `4xx` — the malformed body
is a protocol-level failure the client parses via the JSON-RPC envelope, not the HTTP layer.

## 3. JSON-RPC 2.0 envelope

Request:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": { "...": "..." }
}
```

Success response:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": { "...": "..." }
}
```

Error response:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "error": { "code": -32602, "message": "invalid params", "data": { "...": "..." } }
}
```

Notifications (no `id` field, e.g. `notifications/initialized`) get no response body — server
returns HTTP `202 Accepted` with an empty body.

### 3.1 Error code mapping

Standard JSON-RPC 2.0 codes, used exactly as the spec defines them (new negative codes must not
be invented in this range):

| Code | Meaning | Wormhole trigger |
|---|---|---|
| -32700 | Parse error | Request body is not valid JSON |
| -32600 | Invalid Request | Missing/malformed `jsonrpc`, `method`, or `id` field |
| -32601 | Method not found | `method` is not `initialize`, `tools/list`, or `tools/call` |
| -32602 | Invalid params | `params` fails the method's expected shape (e.g. `tools/call` missing `name`) |
| -32603 | Internal error | Unexpected server-side failure (DB error, etc.) |

A **tool execution failure** (e.g. `wormhole.task.create` rejecting an invalid status) is
**not** a JSON-RPC error. It is a successful RPC call whose `result` carries the tool's own
failure shape: `{ "content": [{ "type": "text", "text": "<error message>" }], "isError": true }`.
This matches the current `CallResponse{Error: "..."}` behavior (HTTP 200-equivalent, error in
the body) and matches the MCP spec's convention of separating protocol errors from tool errors.
Unauthenticated/unauthorized calls to an auth-required tool ARE JSON-RPC errors (see §5), since
that is a transport-boundary failure, not a tool-logic failure.

## 4. Methods

Three methods are supported:

### `initialize`

Client-to-server, sent once at connection start.

Request `params`:
```json
{
  "protocolVersion": "2025-11-25",
  "capabilities": {},
  "clientInfo": { "name": "claude-code", "version": "..." }
}
```

Response `result`:
```json
{
  "protocolVersion": "2025-11-25",
  "capabilities": { "tools": {} },
  "serverInfo": { "name": "wormhole", "version": "0.2.4-alpha" }
}
```

`protocolVersion` is pinned to the MCP revision implemented by this server. Re-verify the stable
MCP specification before any protocol-version bump.

Server capabilities are `{"tools": {}}` only — no `resources`, `prompts`, `sampling`, or
`logging` capability objects, since Wormhole exposes only tools (RFC-0001 §5.5: every
capability ships as an MCP tool or it doesn't exist).

### `tools/list`

Request `params`: none (empty object or omitted).

Response `result`:
```json
{
  "tools": [
    {
      "name": "wormhole.task.create",
      "description": "...",
      "inputSchema": { "type": "object", "properties": { "...": "..." }, "required": ["..."] }
    }
  ]
}
```

Auto-derived from the existing `Registry`; tool schemas are not manually duplicated. Each tool's
`inputSchema` MUST include `project_id` as a required
string property (see §4.3 below) unless the tool is project-agnostic (`wormhole.agent.whoami`
takes no project_id per RFC-0001 §9).

### `tools/call`

Request `params`:
```json
{
  "name": "wormhole.task.create",
  "arguments": { "project_id": "...", "title": "...", "description": "..." }
}
```

Response `result` (success): `{ "content": [{ "type": "text", "text": "<JSON-encoded tool result>" }] }`
Response `result` (tool-level failure): see §3.1's `isError: true` shape.

### 4.1 Where `project_id` goes (the envelope decision this chapter had to make)

The current bespoke shape carries `project_id` as a top-level `CallRequest` field, sibling to
`arguments`. Standard MCP `tools/call` has no such sibling field — `params` is exactly
`{name, arguments}`. No RFC text or existing precedent resolves this (ambiguity ladder rung 3
turns up nothing: this is the first time Wormhole's tool envelope is constrained by an external
protocol shape). Decision: **`project_id` moves inside `arguments`**, as a required property on
every project-scoped tool's `inputSchema`, populated by the calling client exactly like any
other argument. This is the only option compatible with an unmodified standard MCP client
(Claude Code) — a custom sibling field is not something a real MCP client would ever send.
The `tools/list` schema generator and `tools/call` handler read `project_id` from `arguments`,
not from a transport-envelope field.

## 5. Auth carry-over

Decision: **the existing bearer-token-per-passport scheme stays exactly as-is** — same token
format, same `identityStore.WhoAmI(ctx, projectID, token)` resolution, same
`AuthenticatedScope` result type consumed by `tool.Handler`. What moves is *where* the token is
read from:

- `bearerToken(r.Header.Get("Authorization"))` is read by the JSON-RPC `tools/call` handler
  after it resolves `tool.RequiresAuth` from the registry and before it invokes `tool.Handler`.
  `initialize` and `tools/list` never require auth because listing tool schemas is not a scoped
  operation.
- Auth failure on a `tools/call` request is a JSON-RPC error, not a tool-result `isError`:
  missing token → `{"code": -32602, "message": "missing bearer token"}`; invalid/expired token
  (`identity.ErrInvalidToken`) → a new code in the server-error range, `{"code": -32001,
  "message": "invalid or expired token"}` (JSON-RPC reserves -32000 to -32099 for
  implementation-defined server errors; -32001 is arbitrary within that range, chosen for no
  reason beyond being the first free slot — flagged as arbitrary, not RFC-derived).
- No new auth mechanism, OAuth flow, or session cookies. Authentication is resolved once at the
  MCP boundary before core packages receive the request, as required by
  `docs/implementation-rules.md` §4.

## 6. Deliberately unspecified

- Whether request batching (JSON-RPC batch arrays) is ever needed — deferred; not used because
  no current caller needs it (see §2).
- The real SSE server-push stream on `GET /mcp` — stubbed to `405` per §2, revisit only if a
  future server-initiated MCP message type requires it.
- Any MCP SDK or library choice (hand-rolled JSON-RPC types versus an existing Go MCP SDK) — a
  new external dependency requires explicit human sign-off under
  `docs/implementation-rules.md` §4 R4.
