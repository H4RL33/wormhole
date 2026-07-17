# Task 4 (issue #20 subtask 4): Retarget `wormhole connect` at the stdio bridge

Repo: this git worktree (branch `wormholed-mcp-endpoint`). Read
`docs/architecture.md` before touching code — it is required reading and
states module-boundary/layering rules for this codebase.

## Context

Subtasks 1-3 are done and merged into this branch:
- Subtask 2 gave `wormholed`'s Unix socket (`internal/runtime/localapi`) a
  real MCP JSON-RPC surface (`initialize`, `tools/list`, `tools/call`,
  `notifications/initialized`), newline-delimited framing, no auth (local
  same-user process trust, RFC-0003 OQ4 default — no bearer token concept
  exists at this layer).
- Subtask 3 added `cmd/wormhole-mcp-stdio`, a thin binary with **no CLI
  flags and no auth wiring**. It dials wormholed's socket (derived via its
  own `wormholedSocketPath()`, `cmd/wormhole-mcp-stdio/main.go:70-76`) and
  bridges Content-Length-framed stdio MCP traffic to/from it. A harness
  spawns it as a subprocess with zero arguments.

`cmd/wormhole-cli/main.go`'s `runConnect` (starts ~line 665) currently
wires Claude Code and OpenCode's MCP client straight at the Coordination
Server's HTTP `/mcp` endpoint with a bearer token (`mcpURL := ... + "/mcp"`,
then `claude mcp add --transport http ... -H "Authorization: Bearer ..."`,
and the OpenCode branch writes a `"type": "remote"` config entry with a
`headers.Authorization` bearer token). This is exactly the bypass issue #20
describes — it must instead point both harnesses at the local
`wormhole-mcp-stdio` binary, which talks to `wormholed`, not the
Coordination Server, directly.

`wormholedSocketPath()` already exists in `cmd/wormhole-cli/main.go`
(lines 257-267) — reuse it, don't duplicate it again in this file.

## Required changes, all in `cmd/wormhole-cli/main.go`

1. **New flag on `runConnect`'s flag set**: `stdioBin := fs.String("stdio-bin", "wormhole-mcp-stdio", "path to the wormhole-mcp-stdio bridge binary")`, following the exact same pattern as the existing `claudeBin` flag on the line above/below it in the flag block.

2. **Before wiring either target, verify `wormholed` is reachable**: dial
   `wormholedSocketPath()` with `net.DialTimeout("unix", wormholedSocketPath(), 2*time.Second)` (same timeout constant `doRegisterViaSocket` already uses elsewhere in this file — check its import block, `net` and `time` are already imported). If the dial fails, do **not** fall back to the direct-Coordination-Server path — print a clear error to `stderr` and return `1`:
   ```
   wormhole connect: wormholed not running (dial %s: %v) — start wormholed before running connect
   ```
   (substitute the actual socket path and dial error). Close the probe connection immediately on success; this step is a reachability check only, it does not reuse the connection for anything else.

3. **Claude branch** (currently the `if *target == "opencode" {...}` / else block that shells out to `claude mcp add`): replace the HTTP wiring with stdio wiring.
   - Resolve the stdio bridge binary the same way `claudeBin` is resolved today: `exec.LookPath(*stdioBin)`. If not found, print to stderr the manual instructions and return `1`, mirroring the existing `claudeBin`-not-found branch's shape:
     ```
     wormhole connect: %q not found in PATH — wire the connector manually:
       claude mcp add %s -- %s
     ```
     (connector name, then the resolved-or-literal stdio binary name — use `*stdioBin` verbatim here since `LookPath` failed).
   - On success, replace the existing `addCmd := exec.Command(*claudeBin, "mcp", "add", "--transport", "http", *connectorName, mcpURL, "-H", "Authorization: Bearer "+out.Token)` line with:
     `exec.Command(*claudeBin, "mcp", "add", *connectorName, "--", resolvedStdioBinPath)`
     where `resolvedStdioBinPath` is the absolute path `exec.LookPath` returned. (`claude mcp add <name> -- <command>` defaults to stdio transport — confirmed via `claude mcp add --help`; no `--transport` flag needed, no `-H` header, no URL.)
   - The `removeCmd := exec.Command(*claudeBin, "mcp", "remove", *connectorName, "-s", "local")` line stays unchanged (best-effort remove, same as today).
   - Update the final success message (`fmt.Fprintf(stdout, "Connector %q registered with %s ...")`) to reference the stdio bridge instead of `mcpURL`, e.g. `"Connector %q registered (stdio via %s).\n"` with the connector name and resolved binary path.
   - Delete the now-unused `mcpURL := strings.TrimRight(*server, "/") + "/mcp"` line entirely — nothing needs it once neither branch depends on it. (Confirm nothing else in `runConnect` still references `mcpURL` before deleting; if something does, keep it but only for that other use.)

4. **`runConnectOpenCode`** (currently takes `mcpURL, token string` params and writes a `"type": "remote"` / `url` / `headers.Authorization` config entry): change its signature to take the resolved stdio binary path instead of `mcpURL`/`token` (drop the `token` parameter entirely — nothing to authenticate locally), and write:
   ```go
   mcp[connectorName] = map[string]any{
       "type":    "local",
       "command": []string{resolvedStdioBinPath},
       "enabled": true,
   }
   ```
   (Per opencode.ai's documented schema: local MCP servers use `type: "local"` + a `command` string array; no `url`/`headers` fields apply to that type.) Update the call site in `runConnect` that invokes `runConnectOpenCode(...)` to match the new signature — it should also go through the same `exec.LookPath(*stdioBin)` resolution step described in point 3 before calling into `runConnectOpenCode` (both target branches need the resolved binary path; do the `LookPath` once, before the `if *target == "opencode"` branch, and pass the result into whichever branch runs — the not-found error message differs slightly by target since Claude's manual fallback shows a `claude mcp add` command and OpenCode's should show something equivalent, e.g. printing the JSON config snippet the user would need to add by hand, but do not over-engineer this — a one-line "not found in PATH" message naming the binary is sufficient if a target-specific fallback command is awkward to construct for OpenCode).

## What NOT to change

- `runJoin`, `doRegister`, `doRegisterViaSocket`, `writeCredentials`,
  credential-writing logic in `runConnect` (the `Passport created.` /
  `agent_id=...` / `credentials written to ...` block) — all unchanged.
  `wormhole connect` still registers via the Coordination Server and writes
  local credentials; only the *harness wiring* (the MCP connector
  registration) changes to point at the stdio bridge instead of the HTTP
  endpoint.
- `internal/runtime/localapi`, `cmd/wormhole-mcp-stdio` — both already
  correct from subtasks 2/3, out of scope here.
- No new top-level packages. No auth token added to the stdio bridge or the
  socket (RFC-0003 OQ4: same-user process trust, no change here).

## Tests

Existing tests for `runConnect`/`runConnectOpenCode` (search
`cmd/wormhole-cli/main_test.go` for `TestRunConnect` /
`TestRunConnectOpenCode` or similar) currently assert against the HTTP/
bearer-token wiring (`claude mcp add --transport http`, `mcp[name]["url"]`,
`mcp[name]["headers"]`). These need to be **rewritten**, not just patched,
to assert the new stdio wiring instead:
- Claude-target tests: assert the `claude` subprocess is invoked with
  `mcp`, `add`, `<connector-name>`, `--`, `<stdio-bin-path>` (however the
  test currently fakes/records subprocess invocation — follow that existing
  pattern).
- OpenCode-target tests: assert the written JSON config has
  `mcp.<name>.type == "local"` and `mcp.<name>.command == [<stdio-bin-path>]`,
  and no `url`/`headers` keys.
- Add a new test: `runConnect` returns `1` and prints a clear error when
  wormholed's socket isn't reachable (no listener at the derived socket
  path) — for both targets, or just once if the reachability check happens
  before the target branch (it does, per point 2 above).
- Follow TDD: write/adjust the failing tests first, then make them pass.
  Use `superpowers:test-driven-development` if you want the full skill
  loaded, otherwise just follow red-green-refactor.

Run `go build ./...` and `go test ./cmd/wormhole-cli/...` before reporting
done. Report DONE / DONE_WITH_CONCERNS / NEEDS_CONTEXT / BLOCKED per the
implementer contract.
