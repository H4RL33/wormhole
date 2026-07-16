# Design: wormholed as the primary MCP endpoint for coding harnesses

Status: design spike (issue #20, subtask 1)
Author: design-spike subagent, 2026-07-16
Authority order: RFC-0001 > RFC-0002 > RFC-0003 > docs/architecture.md > existing code

## 0. Problem restated

`internal/runtime/localapi` today speaks a bespoke wire protocol: one JSON
`{tool, args}` request per connection, one JSON `{result, error}` response,
connection closed (`localapi.go:87-97`, `handle()` at `localapi.go:292-454`).
No real MCP client (Claude Code, OpenCode, or any stdio/HTTP MCP transport)
can attach to this socket. RFC-0003 §5/§6.1 says harnesses must talk MCP to
`wormholed` over local IPC and never directly to the Coordination Server.
Today `wormhole connect` (`cmd/wormhole-cli/main.go` `runConnect`) wires
Claude Code/OpenCode straight at the Coordination Server's `/mcp` HTTP
endpoint, bypassing `wormholed` entirely — the opposite of RFC-0003's
intent. This document specifies the MCP surface, wire framing, backward
compatibility posture, and module boundary for closing that gap, plus a
concrete file/function breakdown for subtasks 2 and 3.

Prior art doing the equivalent job already exists and is frozen:
`docs/mcp-protocol.md` (Chapter 1 of `ROADMAP-ALPHA2.md`) designed the exact
same MCP surface for the Coordination Server's `/mcp` HTTP endpoint, and
`internal/mcp/jsonrpc.go` implements it. This design reuses that decision
set wherever RFC-0003 doesn't force a difference, and calls out every place
it must differ (framing, connection lifecycle, auth).

---

## 1. MCP method surface

Four methods, matching `docs/mcp-protocol.md` §4 plus the notification
`docs/mcp-protocol.md` §3 already documents but the Coordination Server's
handler doesn't need to act on (HTTP has no persistent session to mark
"initialized"; a socket does — see §2).

### `initialize`

Request `params` (from the harness):
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
  "serverInfo": { "name": "wormholed", "version": "0.2.0-alpha" }
}
```

`protocolVersion` pin and reverification discipline: identical to
`docs/mcp-protocol.md` §4 (`2025-11-25`, current published stable MCP spec
revision as of this doc's writing). `serverInfo.name` changes to
`"wormholed"` — this is the local daemon identifying itself, not the
Coordination Server; a harness inspecting `serverInfo` should be able to
tell it's talking to the local runtime, not a proxy. Capabilities are
`{"tools": {}}` only, same reasoning as the Coordination Server: RFC-0001
§5.5 exposes every capability as a tool, nothing else. `wormholed` does not
gain a `resources`, `prompts`, `sampling`, or `logging` capability by virtue
of being local-first — RFC-0003 doesn't add any of those pillars, and NG3
(`wormholed` is deterministic infrastructure, no LLM calls inside it) rules
out `sampling` specifically.

No auth on `initialize` — same as the Coordination Server (listing server
capabilities is not a scoped operation), and RFC-0003 OQ4's conservative
default (same-user process trust, no additional local auth token) applies
identically at this layer.

### `tools/list`

Request `params`: none.

Response `result`:
```json
{
  "tools": [
    {
      "name": "wormhole.task.list",
      "description": "...",
      "inputSchema": { "type": "object", "properties": { "...": "..." }, "required": [] }
    }
  ]
}
```

**Must be generated dynamically from wormholed's existing local tool
registry** — the task brief's framing implies a `Registry` type analogous
to `internal/mcp.Registry` already exists in `localapi`. It does not:
`localapi`'s dispatch is a hand-written `switch req.Tool` statement
(`handle()`, `localapi.go:305-453`), 15 cases, no descriptor type, no
reflection-based schema generation. This is the single largest net-new
piece of subtask 2's work: **introduce a `localTool`/`localRegistry` type
in `localapi` mirroring `internal/mcp.Tool`/`internal/mcp.Registry`'s
shape** (name, description, arguments-example-for-schema-reflection,
handler function), register each of the 15 existing tools into it at
`Server` construction time, and replace the `switch` in `handle()` with a
registry lookup + dispatch. `tools/list` then becomes `registry.List()` +
the same `buildInputSchema`/`reflectStructSchema` reflection machinery
`internal/mcp/jsonrpc.go:106-225` already implements — that reflection code
has no dependency on `internal/mcp`'s auth/identity types, so it can be
**copied**, not imported (see §4 for why import is still wrong even though
technically dependency-free).

The 15 tools currently dispatched by name in `handle()`, which
`tools/list` must enumerate: `wormhole.agent.whoami`, `wormhole.task.list`,
`wormhole.task.get`, `wormhole.task.create`, `wormhole.task.route`,
`wormhole.channel.list`, `wormhole.channel.events`, `wormhole.channel.post`,
`wormhole.channel.subscribe`, `wormhole.kb.list`, `wormhole.kb.get`,
`wormhole.kb.write`, `wormhole.agent.register` (dual-shape, see §3),
`wormhole.agent.presence`, `wormhole.agent.list`.

Every tool's `inputSchema` requires `project_id` as a string property,
**except** `wormhole.agent.whoami` — identical rule to
`docs/mcp-protocol.md` §4, and consistent with every existing handler in
this file resolving `project_id` from args with a same fallback-to-
`s.projectID` pattern (`localListTasks`, `localGetTask`, etc., all follow
this shape already). One local wrinkle: unlike the Coordination Server,
several of these tools treat `project_id` as *optional in single-org mode*
(fallback to `s.projectID`) and *required in multi-org mode*
(`resolveOrgContext` returns an error with no binding). The schema cannot
express "conditionally required" in plain JSON Schema without `oneOf`
complexity that has no real consumer benefit — **decision: mark
`project_id` required in the schema unconditionally**, matching the
Coordination Server's existing behavior exactly, and treat the single-org
fallback as a permissive runtime behavior the schema doesn't need to
advertise. This is the same posture already implicit in
`docs/mcp-protocol.md` §4 for the Coordination Server side, so nothing new
is being decided here beyond confirming it also applies locally.

### `tools/call`

Request `params`, response shapes: byte-identical to
`docs/mcp-protocol.md` §4/§4.1/§3.1 — `{name, arguments}` in, `{content:
[{type, text}], isError?}` out, `project_id` lives inside `arguments` (not
a sibling field), a tool-handler failure is `isError: true` inside a
successful RPC result, not a JSON-RPC error.

Dispatch target: **the same underlying handler functions that currently
back `localRequest.Tool`** — `s.proxyWhoAmI`, `s.localListTasks`,
`s.localGetTask`, `s.handleTaskCreate`, `s.handleTaskRoute`,
`s.localListChannels`, `s.localListChannelEvents`, `s.handleChannelPost`,
`s.handleChannelSubscribe` (special-cased, see below), `s.localListArticles`,
`s.localGetArticle`, `s.handleKBWrite`, `s.proxyRegister`/
`s.handleAgentRegister` (dual-shape dispatch preserved, see §3),
`s.handleAgentPresence`, `s.handleAgentList`. None of these functions'
internals need to change — `tools/call`'s job is purely re-plumbing how a
tool name + `json.RawMessage` args reach them, and how their `(result,
error)` return gets wrapped back into MCP's `{content, isError}` shape
instead of today's `localResponse{Result, Error}`.

`wormhole.channel.subscribe` is the one handler that doesn't return a
single `(result, error)` — it streams (`handleChannelSubscribe`,
`localapi.go:1122-1182`, blocks and writes N responses on the same
connection). MCP's `tools/call` is a single-response RPC; there is no
first-class "streaming tool result" in the MCP spec's request/response
shape. Two options:
1. Model each pushed event as an MCP **notification** on the same
   connection (`notifications/wormhole.event`, a server-to-client message
   with no `id`), sent after the initial `tools/call` response
   acknowledges subscription creation.
2. Keep `tools/call`'s single response as just the subscription handle
   (`{subscription_id, ...}`), and require the client to poll a separate
   tool (`wormhole.channel.events`, which already exists) for delivery.

**Recommendation: option 1** (server-initiated notification per event),
because it's the closest fit to what `handleChannelSubscribe` already does
(push-as-it-happens, not poll), and JSON-RPC 2.0 notifications are exactly
"no response expected, no `id`" — the wire shape is a strict subset of what
this repo's `rpcRequest`/`rpcResponse` types already model (an outbound
message with `method` set and no `id` and a `params` field instead of
`result`). This is new server-to-client traffic on a socket that
previously only ever wrote in response to a read, which is exactly what
persistent framing (§2) has to support regardless of `channel.subscribe` —
it is not extra scope this design introduces, it's the first proof that
persistent framing is needed at all. Flagged as an inference (rung 6 of
the ambiguity ladder — no RFC text specifies subscription delivery shape
over MCP): a defensible call, not a guess, because it minimizes the delta
against `handleChannelSubscribe`'s existing behavior. If subtask 2's
implementer disagrees, escalate before diverging — this is the one part of
the surface genuinely underspecified by RFC-0003.

### `notifications/initialized`

Client-to-server notification, sent once after the client processes
`initialize`'s response, per the MCP spec's standard lifecycle. No
response (it's a notification: no `id`, nothing decoded back). Server-side
behavior: **no-op beyond marking the connection's session as initialized**
(reject `tools/list`/`tools/call` on a connection that hasn't completed
the `initialize` → `notifications/initialized` handshake, mirroring how a
compliant MCP server enforces lifecycle order). This is new statefulness
`localapi` doesn't have today (today's connections carry no session state
at all — one shot, no lifecycle). See §5 for the `session` type this
requires.

---

## 2. Wire framing

### Current state

Newline-delimited JSON, one request/response pair, then the connection
closes (`handle()`, `localapi.go:292-304`, `writeResponse`,
`localapi.go:456-462`). `wormhole.channel.subscribe` is the sole exception:
it keeps the connection open and writes multiple newline-delimited
`localResponse` messages until the subscription ends
(`handleChannelSubscribe`, `localapi.go:1122-1182`).

### Decision: newline-delimited JSON stays, framing does not need Content-Length

MCP's **stdio transport** convention (used by the `cmd/wormhole-mcp-stdio`
bridge in subtask 3, talking to a harness over stdin/stdout) uses
`Content-Length`-prefixed framing, borrowed from LSP, because stdio has no
natural message boundary and must support binary-safe payloads mixed with
arbitrary process output. **That constraint does not apply to a Unix
domain socket carrying only wormholed's own protocol** — there is no
third-party output sharing the stream, and every message wormholed will
ever emit here is JSON text with no embedded newlines requiring escaping
(the existing `bufio.Reader.ReadBytes('\n')` / `json.Marshal` round-trip
already assumes this and it has held for four phases of implementation,
P1–P3, without incident). Introducing `Content-Length` framing here would
be reformatting a convention designed to solve a problem (shared/binary
stdio) this transport doesn't have — a violation of
`docs/architecture.md` §0.5's "smallest correct diff" discipline, and it
would also break every existing test in `localapi_test.go` /
`localapi_p3_test.go` / `localapi_write_test.go` for zero benefit, since
none of them are MCP-spec-constrained (they dial the socket directly, not
through an MCP client library).

**What does need to change**, independent of the byte-framing question:
connection *lifecycle*. Today each connection is one-shot (`handle()`
returns after one write, closing the connection via the `defer conn.Close()`
at the top). MCP requires a **persistent session per connection**:
`initialize` → `notifications/initialized` → N × `tools/call`/`tools/list`,
all on the same connection, until the client disconnects. `handle()` must
become a loop that reads successive newline-delimited JSON-RPC messages
off the same connection and dispatches each, rather than reading exactly
one line and returning. This is the actual "framing" change subtask 2 must
make — not the byte-delimiter, but the read loop's cardinality. Confirmed
against `docs/mcp-protocol.md` precedent: even the Coordination Server's
HTTP transport is one-request-per-HTTP-call (`docs/mcp-protocol.md` §2 —
"one JSON-RPC message per HTTP request body"), but HTTP's connection reuse
is transport-layer and invisible to the JSON-RPC handler; a Unix socket's
"connection" *is* the session, so `localapi` must carry session state
(`initialized bool` at minimum, plus whatever `notifications/subscribe`
needs for interleaved outbound pushes — see §5) that the Coordination
Server's stateless-per-request handler never needed.

Concurrent reads and interleaved writes on the same connection (a
`tools/call` request that's still processing while a subscription
notification needs to be pushed) require a per-connection write mutex —
`net.Conn.Write` is not guaranteed safe for concurrent goroutines writing
without external synchronization. New requirement, not present today
(today's per-connection model never has two goroutines writing to the same
`conn` concurrently, since there's exactly one write per connection except
inside `handleChannelSubscribe`'s own single loop, which is already the
sole writer for its connection).

---

## 3. Backward compatibility

### Who dials wormholed's socket today

Two call sites only, confirmed by search:

1. **`cmd/wormhole-cli/main.go`'s `doRegisterViaSocket`** (`main.go:292-333`),
   used by `wormhole join` (`runJoin`, `main.go:499`). This is the *only*
   production code path that dials the socket. It sends
   `localSocketRequest{Tool: "wormhole.agent.register", ...}` and decodes
   `localSocketResponse`.
2. **`internal/runtime/localapi`'s own test suite**
   (`localapi_test.go`, `localapi_p3_test.go`, `localapi_write_test.go`) —
   dials the socket directly with the bespoke shape to exercise the server.

Critically: **`wormhole connect` (`runConnect`, `main.go:631-740`) never
dials the socket at all** — it calls `doRegister` directly against
`--server` and then wires the harness's MCP client straight at the
Coordination Server's `/mcp` URL (`mcpURL := ... + "/mcp"`,
`main.go:716`). This is the exact bug issue #20 describes; it is not
"backward compatibility to preserve," it is the bypass subtask 4 must fix
by retargeting `runConnect` at the stdio bridge (subtask 3) dialing
`wormholed`'s socket instead.

### Decision: clean break, no bespoke-shape compatibility shim

No external harness or CLI depends on the bespoke `localRequest`/
`localResponse` shape surviving — the sole production caller
(`doRegisterViaSocket`) is itself CLI code in this repo, updated in the
same effort (subtask 2/4 boundary — the exact split is an implementation
detail, not this design's call to make, but it must not ship
subtask-2-only leaving `doRegisterViaSocket` broken). `docs/mcp-protocol.md`
§1 already set this precedent for the Coordination Server's equivalent
migration ("there is no back-compat shim (project is pre-1.0)") — the
same posture applies here for the identical reason (pre-1.0, no external
consumers to protect). The test suite is source, not a compatibility
boundary: it gets rewritten to speak MCP JSON-RPC directly (new
`sendMCPRequest`-style helpers replacing today's `sendRequest`,
`dialLocalSocket` can stay as-is since it's transport-agnostic).

Recommendation for subtask 2/4 sequencing (not this design's authority to
mandate, but stated so the two subtasks don't produce a broken
intermediate state): land the new MCP dispatch and `doRegisterViaSocket`'s
MCP-shaped rewrite in the same PR, or keep the bespoke `switch` dispatch
alive under a second code path (e.g. a distinct listener/port) only long
enough to avoid breaking `wormhole join` mid-refactor, then delete it
immediately after `doRegisterViaSocket` is updated. Do not ship both wire
shapes as a permanent dual surface — that's scope creep against
`docs/architecture.md` §0.5, and there is no caller who needs it kept
around past the refactor window.

---

## 4. Module boundary: does `internal/runtime/localapi` need `internal/mcp`, or a new shared package?

**Recommendation: hold the existing precedent. No import, no shared
`internal/mcpwire` package. Duplicate the wire-shape types, same as
today.**

Reasoning:

- RFC-0003 §6.3 and `docs/architecture.md` LR1 ("`internal/runtime/*`
  packages never import `internal/core/*` or `internal/mcp`") are explicit
  hard rules, not soft guidance — and `localapi.go`'s own header comment
  (`localapi.go:1-13`) already states the precedent this design would be
  breaking if it introduced an import: *"localapi cannot import
  internal/mcp (RFC-0003 §6.3 keeps internal/runtime/* and internal/mcp
  separate trees), so the wire contract is duplicated here, same as
  cmd/wormhole-cli/main.go already does for the same reason."* This
  design's `rpcRequest`/`rpcResponse`/`toolsCallParams`/`toolCallResult`
  types (needed for `tools/call`'s envelope) are the *third* duplication
  of this exact shape in the repo (`internal/mcp/jsonrpc.go`,
  `cmd/wormhole-cli/main.go`, `internal/runtime/localapi/localapi.go`) —
  a real signal that a shared package is tempting, but the RFC-0003 §6.3
  rule is stated as a hard dependency rule (LR1), not a rule with a "unless
  three duplicates accumulate" escape hatch. `docs/architecture.md`'s own
  rationalisation table (§0.8: *"This helper would be cleaner in a shared
  package" — "Cross-core imports are banned... Duplicate or escalate."*)
  says exactly this move is a rationalisation to catch, not a fact to
  act on.
- A theoretical `internal/mcpwire` package holding just the JSON-RPC
  envelope types (`RPCRequest`/`RPCResponse`/`RPCError`/
  `toolsCallParams`/`toolCallResult`) with **zero** logic would not
  itself violate LR1's letter (it isn't `internal/mcp` or
  `internal/core/*`), and would remove real duplication. But creating it
  is a **new top-level package**, which `docs/architecture.md` R4 requires
  explicit human sign-off for ("No new top-level packages... without
  explicit human sign-off"), and RFC-0003 §6.3's design intent reads as
  "runtime and coordination-server MCP surfaces are independently
  evolvable, on purpose" (the RFC explicitly calls the runtime side a
  parallel tree, not a shared one) — collapsing them into one wire-shape
  package pre-empts that independence for a purely cosmetic DRY win. The
  three duplications aren't at risk of drifting silently either: they're
  all frozen against the same external spec (MCP JSON-RPC 2.0), so a spec
  version bump is the only thing that would need to touch all three, and
  that's already a coordinated, deliberate, cross-repo change today (see
  `docs/mcp-protocol.md` §4's explicit "re-verify before any future
  protocol bump" instruction) — not an accidental-drift risk duplication
  bans are meant to prevent.
- Net effect: this design adds a **fourth** duplication of the same
  envelope shapes inside `localapi` itself, specifically the
  `initialize`/`tools/list` reflection-schema helpers
  (`buildInputSchema`/`reflectStructSchema`/`jsonSchemaForType`/
  `parseJSONTag`, `internal/mcp/jsonrpc.go:106-225`) that `tools/list`
  needs and don't exist in `localapi` yet at all — these get copied
  verbatim (with `wormholed`-specific tool descriptors substituted) into a
  new file in `internal/runtime/localapi`, not imported.

**Escalation flag (this is a defensible call, not a certainty):** if a
fifth wire-shape duplication site appears in a future phase (e.g. a
governance-adjacent MCP surface, or a second local-runtime-side MCP
listener), that is the point to escalate the `internal/mcpwire` question
to a human decision rather than re-deciding it locally again — three (soon
four) duplications is a lot for a "no shared package" rule to keep
absorbing, and this design's recommendation is to hold the line *this
time*, not that the line is infinitely load-bearing.

---

## 5. File/function-level breakdown

### Subtask 2 — `internal/runtime/localapi` real MCP surface

New file: `internal/runtime/localapi/mcp.go`
- `type localTool struct { Name, Description string; ArgumentsExample any; Handler localToolHandler }` — mirrors `internal/mcp.Tool`, `internal/mcp.Handler` shape but with `localapi`'s existing `(context.Context, json.RawMessage) (any, error)` handler signature (no `*identity.AuthenticatedScope` parameter — `localapi` has no auth middleware today, RFC-0003 OQ4 default; do not invent one here as a side effect, that's a separate escalation if ever needed).
- `type localRegistry struct { tools map[string]localTool }`, `newLocalRegistry(s *Server) *localRegistry` — constructs and registers all 15 existing tools, each wrapping the corresponding existing method (`s.proxyWhoAmI`, `s.localListTasks`, etc.) with a thin adapter closure matching `localToolHandler`'s signature.
- `func (r *localRegistry) List() []localTool`, `func (r *localRegistry) Get(name string) (localTool, bool)`.
- `buildInputSchema(t localTool) map[string]any`, `reflectStructSchema`, `jsonSchemaForType`, `parseJSONTag` — copied from `internal/mcp/jsonrpc.go:106-225`, adjusted for `localTool` instead of `Tool` and the same `project_id`-except-`whoami` injection rule (§1).
- `type mcpSession struct { initialized bool; writeMu sync.Mutex }` — one per connection, created in `handle()`, tracks lifecycle state (§2) and serializes writes for interleaved notification delivery (§1's `channel.subscribe` handling).
- `handleInitialize() any`, `handleToolsList(reg *localRegistry) any`, `handleToolsCall(ctx context.Context, reg *localRegistry, params json.RawMessage) (any, *rpcError)` — mirrors `internal/mcp.HandleInitialize`/`HandleToolsList`/`HandleToolsCall` but drops the auth-resolution branch entirely (no `identityStore`, no bearer token check — `localapi` has no local-auth concept per OQ4).
- `dispatchMCPMessage(ctx, sess, conn, reg, msg rpcRequest)` — the per-message router replacing `handle()`'s tool-name `switch`; handles `initialize`, `notifications/initialized` (no-op, sets `sess.initialized = true`), `tools/list`, `tools/call`, and returns a `-32601` error for anything else, matching `docs/mcp-protocol.md` §3.1's error table.

Changed file: `internal/runtime/localapi/localapi.go`
- `handle()` (`localapi.go:292-454`): replace the single `ReadBytes('\n')` → dispatch-once → `defer conn.Close()` shape with a loop: create `sess := &mcpSession{}`, then loop `reader.ReadBytes('\n')` until EOF/error, unmarshal each line into `rpcRequest`, call `dispatchMCPMessage`. Notification handling (`isNotification := len(req.ID) == 0`) mirrors `internal/mcp/jsonrpc.go:358-366`'s check exactly (reuse the logic, not the code — different envelope type `rpcRequest` already exists locally at `localapi.go:37-42`).
- `handleChannelSubscribe` (`localapi.go:1133-1182`): change its final `writeResponse(conn, localResponse{...})` calls (§1, initial ack) and the event-delivery loop's `writeResponse` calls to instead marshal and write MCP notification envelopes (`{"jsonrpc":"2.0","method":"notifications/wormhole.event","params":...}`) through `sess.writeMu`-guarded writes, since it now shares the connection with other in-flight `tools/call` traffic instead of owning it exclusively.
- Delete `localRequest`/`localResponse` types (`localapi.go:87-97`) and `writeResponse` (`localapi.go:456-462`) once the switch-based dispatch is fully replaced — no remaining caller needs them per §3.
- Every existing `local*`/`handle*` method (`localListTasks`, `localGetTask`, `handleTaskCreate`, etc.) is **unchanged internally** — only the adapter closures in `newLocalRegistry` change how they're invoked. This keeps the diff's blast radius to dispatch plumbing, matching `docs/architecture.md` §0.5.

Test files: `internal/runtime/localapi/localapi_test.go`,
`localapi_p3_test.go`, `localapi_write_test.go` all get their
`sendRequest`/`dialLocalSocket` helpers rewritten to speak
`initialize` → `notifications/initialized` → `tools/call` instead of the
bespoke `localRequest`/`localResponse` shape. New test file
`internal/runtime/localapi/mcp_test.go` for `tools/list` schema-shape
assertions and the `initialize`/`notifications/initialized` lifecycle
(including the "reject `tools/call` before `initialize`" case, if that
enforcement is implemented — flagged as a design choice for subtask 2 to
confirm, not certain either way whether strict pre-initialize rejection is
worth the complexity vs. simply always answering).

Rough surface area: ~350-450 new lines (`mcp.go` + schema-reflection
copy + session/dispatch loop), ~150-200 lines changed in `localapi.go`
(the `handle()` rewrite and `handleChannelSubscribe`'s write-path change),
~100-150 lines of test rewrites.

### Subtask 3 — stdio bridge

New binary: `cmd/wormhole-mcp-stdio/main.go`
- Package `main`, no `internal/runtime/*` import needed beyond what's
  necessary to dial the socket — this is a thin transport bridge, not a
  daemon, so it should mirror `cmd/wormhole-cli`'s existing posture (client-
  side code only, per `docs/architecture.md`'s module table) rather than
  pull in `internal/runtime/localapi` directly. It dials the Unix socket
  and relays framed messages in both directions.
- `func main()`: dial `wormholedSocketPath()` (same derivation as
  `cmd/wormhole-cli/main.go:277-283` — duplicate this small helper again,
  consistent with §4's duplication posture, or factor it into
  `internal/types` if it's judged genuinely shared config-path logic; that
  call belongs to subtask 3's implementer, not this design, since it's a
  much smaller/lower-risk duplication than the wire-shape question in §4).
- `func bridge(ctx context.Context, stdin io.Reader, stdout io.Writer, conn net.Conn) error`: reads Content-Length-framed messages from `stdin` (stdio MCP transport convention — this is the one place Content-Length framing *does* apply, because stdin/stdout genuinely is shared with the host process's other I/O and needs unambiguous framing), strips the framing, writes the raw JSON-RPC message newline-delimited to `conn` (matching §2's socket-side framing), and the reverse: reads newline-delimited responses/notifications off `conn`, wraps each in `Content-Length: N\r\n\r\n<body>` framing, writes to `stdout`. Two goroutines (stdin→socket, socket→stdout) synchronized via the same `ctx`-cancellation-on-EOF pattern `localapi.Server.Serve` already uses (`localapi.go:270-290`).
- No MCP semantics live in this binary at all — it is a pure framing translator. All `initialize`/`tools/list`/`tools/call` logic stays in `wormholed`; the bridge does not interpret message contents, only re-frames them. This keeps it a "thin adapter" per the issue's own description, and keeps `internal/runtime/localapi` the single place MCP semantics are implemented for the local runtime.

Test file: `cmd/wormhole-mcp-stdio/main_test.go` — round-trip test:
fake `wormholed` socket (`net.Listen("unix", ...)` in-test, similar to
`localapi_test.go`'s `fakeCoordServer` pattern but for the socket side),
feed Content-Length-framed `initialize` on stdin, assert Content-Length-
framed response on stdout.

Rough surface area: ~150-200 lines for `main.go` + framing helpers,
~100 lines of tests. Small relative to subtask 2 — this genuinely is a
"thin bridge," which is the right shape per the issue description.

### Subtask 4 (not this design's scope, noted for sequencing only)

`runConnect` (`cmd/wormhole-cli/main.go:631-740`) needs its `claude mcp
add --transport http ...` / OpenCode `type: "remote"` wiring replaced with
a `stdio` transport pointed at the new `cmd/wormhole-mcp-stdio` binary
(`claude mcp add --transport stdio wormhole -- wormhole-mcp-stdio`, roughly
— exact CLI flag shape depends on Claude Code's current `mcp add` stdio
syntax, not verified here since it's out of this subtask's scope). This
depends on subtask 3 shipping first and is called out only so subtask 1's
design doesn't leave a dangling reference.

---

## 6. Open questions / flags carried forward

- **Subscription delivery via MCP notification** (§1, `tools/call` for
  `channel.subscribe`): inference, not RFC-specified. Recommended shape
  given, flagged for confirmation at subtask 2 implementation time.
- **Whether `notifications/initialized` should be *enforced*** (reject
  `tools/call` before it arrives) vs. purely informational: not decided
  here, left to subtask 2's implementer with a recommendation to enforce
  it (closer to spec-compliant), not a hard requirement of this design.
- **`internal/mcpwire` shared package**: recommended against for this
  round (§4), with an explicit escalation trigger stated (a fifth
  duplication site) rather than a permanent closure of the question.
- **RFC-0003 OQ4 (local IPC auth)**: unchanged by this design. No bearer
  token, no session auth added to `wormholed`'s socket — same-user process
  trust stays the model. If a future security review revisits OQ4, this
  design's session (`mcpSession`) would be the natural place to carry an
  auth-resolution result, but nothing here adds one.
