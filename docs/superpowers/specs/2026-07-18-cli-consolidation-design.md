# CLI Consolidation Design

**Status:** Approved
**Date:** 2026-07-18
**Piece:** D of 4 (onboarding-failure remediation). Sequenced first because it fixes the CLI surface an agent actually sees before A (skill-share content) teaches how to use it. B (seed article expansion) and C (friendly project names) are separate, smaller follow-ups, not covered here.

## Problem

First real-world agent session against Wormhole failed: the model didn't know how to use the MCP tools provided, and separately expected to interact with Wormhole through a single `wormhole` command. Today there are three client-facing binaries (`wormhole-cli`, `wormhole-mcp-stdio`, `wormholed`) plus `wormhole-server`. The human-facing commands (`join`, `connect`, `whoami`, `profile`, `viewer-key`) live in `wormhole-cli`; the MCP stdio bridge a harness actually spawns is a separate binary, `wormhole-mcp-stdio`; `wormholed` is the local daemon; `wormhole-server` is the remote coordination server (RFC-0003), not meant to run on an agent's machine at all, though the quickstart currently walks through running it locally for dev/testing.

This spec addresses only the multi-binary confusion (`wormhole-cli` + `wormhole-mcp-stdio` merge). `wormholed` was already found to be fully non-interactive (no stdin prompts anywhere in the codebase) and needs no change here. `wormhole-server`'s remote-only posture is already correctly specified by RFC-0003; this spec doesn't touch it.

## Architecture

`cmd/wormhole-cli` and `cmd/wormhole-mcp-stdio` collapse into one `cmd/wormhole` package.

- The existing `run(args, stdout, stderr) int` dispatch table (`cmd/wormhole-cli/main.go`) gains an `mcp` case.
- The `mcp` case runs the bridge logic currently in `cmd/wormhole-mcp-stdio/main.go` (`bridge`, `stdinToSocket`, `socketToStdout`), moved into the merged package as unexported functions, unchanged in behavior: dial `wormholed`'s local socket, relay newline-delimited JSON-RPC in both directions until either side closes or SIGINT/SIGTERM.
- `wormholedSocketPath()` is currently duplicated in both binaries (identical logic, `cmd/wormhole-cli/main.go:267` and `cmd/wormhole-mcp-stdio/main.go:74`, the latter explicitly noting the duplication). Collapses to a single definition in the merged package, used by both the `join`/`connect` reachability check and the new `mcp` subcommand's dial.
- `cmd/wormholed` and `cmd/wormhole-server` are untouched by this piece.

## Command surface

```
wormhole join                 (unchanged)
wormhole connect               (unchanged, rewires harness to spawn `wormhole mcp`)
wormhole whoami                (unchanged)
wormhole profile list          (unchanged)
wormhole viewer-key create     (unchanged)
wormhole mcp                   (new — replaces wormhole-mcp-stdio; stdio<->wormholed-socket bridge, no flags)
```

`connect`'s harness-wiring step (`runConnect`, and the OpenCode path `runConnectOpenCode`) changes from resolving `wormhole-mcp-stdio` on `$PATH` and running `claude mcp add <name> -- <path-to-wormhole-mcp-stdio>`, to resolving `wormhole` on `$PATH` and running `claude mcp add <name> -- <path-to-wormhole> mcp`.

`usage()` gains an `mcp` line. All other subcommand handlers move unchanged.

## Tests

- `cmd/wormhole-cli`'s existing tests (`main_test.go`, `main_join_socket_test.go`, `connect_opencode_test.go`, `profiles_test.go`, `viewer_key_test.go`) move into `cmd/wormhole` with package name updated, no logic changes.
- `cmd/wormhole-mcp-stdio`'s `main_test.go` (bridge/framing coverage) moves into `cmd/wormhole` alongside them.
- New/updated test: `connect`'s harness-wiring resolves `wormhole mcp` (the merged binary + subcommand) rather than a separate `wormhole-mcp-stdio` binary path.

## Compatibility

Clean break: `cmd/wormhole-cli/` and `cmd/wormhole-mcp-stdio/` are deleted outright, no deprecated wrapper binaries. Project is pre-alpha (v0.2.4-alpha per README), no external installs to preserve.

## Docs

- README: install step collapses from two `go install` lines (`./cmd/wormhole-cli`, `./cmd/wormhole-mcp-stdio`) to one (`./cmd/wormhole`). All `wormhole-cli <cmd>` example invocations become `wormhole <cmd>`. The "`wormhole-cli connect` requires `wormhole-mcp-stdio` on `$PATH`" caveat is deleted.
- `docs/architecture.md`: module table entry for `cmd/wormhole-cli` renamed to `cmd/wormhole`.

## Out of scope

- Piece A: skill-share/onboarding content teaching correct tool usage.
- Piece B: seeded KB article expansion at project creation.
- Piece C: friendly (alphanumeric) project names replacing long codes.
- Any change to `wormholed` or `wormhole-server` behavior.
