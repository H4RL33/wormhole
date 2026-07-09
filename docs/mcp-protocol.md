# Wormhole MCP Protocol — Transport & Auth (Chapter 1 decision)

**Status:** Design decision, Chapter 1 of `ROADMAP-ALPHA2.md` M1. Implemented by Chapters 2-4.
**Inference flag (RFC-0001 §9):** the MCP tool surface is "indicative, not finalised." Tool
*names* below are fixed by the RFC's naming grammar; the JSON-RPC envelope, field placement,
and error-code mapping are this document's inference, made because no RFC text or existing
code fully specifies them (ambiguity ladder, `docs/architecture.md` §0.4, rung 6 — decided
here rather than escalated, since the decision is local to the transport layer and does not
touch a pillar's data model or the open questions listed in RFC-0001 §15).

## 1. Why this replaces the current shape

`internal/mcp/server.go` today exposes a bespoke JSON/HTTP pair: `POST /mcp/tools` (list) and
`POST /mcp/tools/call` (invoke), with a custom `CallRequest{Tool, ProjectID, Arguments}` /
`CallResponse{Result, Error}` envelope. No real MCP client — Claude Code included — can attach
to this; it does not speak the MCP wire protocol. `ROADMAP-ALPHA2.md`'s scope-decision flags
call this out as the reason M1 exists and comes first. This document is the design this repo
freezes and Chapters 2-4 build against; there is no back-compat shim (project is pre-1.0).

## 2. Transport: Streamable HTTP, single `/mcp` endpoint

- One HTTP route, `/mcp`, replacing both `/mcp/tools` and `/mcp/tools/call`.
- `POST /mcp`: client-to-server JSON-RPC 2.0 requests and notifications, one JSON-RPC message
  per HTTP request body (batching not required for alpha 2 — Wormhole has no server-initiated
  requests yet, so batched responses add complexity with no consumer).
- `GET /mcp`: reserved for the server-to-client SSE stream the Streamable HTTP transport spec
  defines for server-initiated messages. Wormhole has no server-initiated MCP messages in
  alpha 2 scope (no sampling requests, no server notifications) — Chapter 2 implements this
  route to return `405 Method Not Allowed` rather than a real SSE stream, and that limitation
  is stated in `docs/claude-code-connector.md` (Chapter 4). Building a real SSE stream for
  zero current consumers would violate `docs/architecture.md` §0.5 (smallest correct diff).
- Every request and response body is `Content-Type: application/json`.

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

Standard JSON-RPC 2.0 codes, used exactly as the spec defines them (Chapter 2/3 must not invent
new negative codes in this range):

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

Three methods, matching M1's roadmap scope exactly (Chapter 1 decides these three; no others
are added or implied):

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
  "serverInfo": { "name": "wormhole", "version": "0.2.0-alpha" }
}
```

`protocolVersion` pin: `2025-11-25` is the current published stable MCP specification revision,
verified at Chapter 2 implementation time (2026-07-09). `2025-03-26` was the last known revision
when this document was first written (Chapter 1) and has since been superseded. A `2026-07-28`
revision exists as a release candidate at verification time but is not yet a published stable
spec, so it is not used here. Re-verify before any future protocol bump.

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

Auto-derived from the existing `Registry` (Chapter 2 requirement — no manual duplication of the
16 registered tools' schemas). Each tool's `inputSchema` MUST include `project_id` as a required
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
Chapter 2's `tools/list` auto-derivation and Chapter 3's `tools/call` handler must read
`project_id` out of `arguments`, not off a transport envelope field.

## 5. Auth carry-over

Decision: **the existing bearer-token-per-passport scheme stays exactly as-is** — same token
format, same `identityStore.WhoAmI(ctx, projectID, token)` resolution, same
`AuthenticatedScope` result type consumed by `tool.Handler`. What moves is *where* the token is
read from:

- Today: `bearerToken(r.Header.Get("Authorization"))` is called directly inside
  `NewCallHandler`, once per `/mcp/tools/call` request, only when `tool.RequiresAuth`.
  Unchanged.
- After Chapter 3: the same `bearerToken()` helper and `Authorization: Bearer <token>` header
  read happen inside the new JSON-RPC `tools/call` method handler, at the same point in the
  control flow (after resolving `tool.RequiresAuth` from the registry, before invoking
  `tool.Handler`). `initialize` and `tools/list` never require auth — listing tool schemas is
  not a scoped operation, matching today's `/mcp/tools` (list) endpoint having no auth check.
- Auth failure on a `tools/call` request is a JSON-RPC error, not a tool-result `isError`:
  missing token → `{"code": -32602, "message": "missing bearer token"}`; invalid/expired token
  (`identity.ErrInvalidToken`) → a new code in the server-error range, `{"code": -32001,
  "message": "invalid or expired token"}` (JSON-RPC reserves -32000 to -32099 for
  implementation-defined server errors; -32001 is arbitrary within that range, chosen for no
  reason beyond being the first free slot — flagged as arbitrary, not RFC-derived).
- No new auth mechanism, no OAuth, no session cookies. `docs/architecture.md` §5 M4's rule
  (auth resolved once, at the MCP boundary, before core packages see anything) is unchanged by
  the transport migration.

## 6. What Chapter 1 explicitly does not decide

- Whether request batching (JSON-RPC batch arrays) is ever needed — deferred; not used because
  no current caller needs it (see §2).
- The real SSE server-push stream on `GET /mcp` — stubbed to `405` per §2, revisit only if a
  future roadmap chapter adds a server-initiated MCP message type.
- Any MCP SDK / library choice for Chapter 2's implementation (hand-rolled JSON-RPC types vs. an
  existing Go MCP SDK) — that is Chapter 2's decision, constrained by `docs/architecture.md` R4
  (new external dependency needs explicit human sign-off).
