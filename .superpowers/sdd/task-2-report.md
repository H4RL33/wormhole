# Task 2 Report: Gateway and Fabric Hard Rename

## Status

Implemented the alpha hard rename on `production-readiness`:

- moved `cmd/wormholed/` to `cmd/gatewayd/` and `cmd/wormhole-server/` to `cmd/fabric/` with `git mv`;
- renamed the former daemon implementation and test source files to `gatewayd.go` and `gatewayd_test.go`;
- restricted build output to `dist/wormhole`, `dist/gatewayd`, and `dist/fabric`, with a `naming-check` hard-cut assertion;
- changed local MCP initialization server info to `gatewayd` at version `0.2.4-alpha`, covered by a test-first assertion;
- updated executable-facing error and log prefixes, semantic source comments, tests, CI, documentation, and examples to Gateway/Fabric terminology;
- retained `WORMHOLE_*`, `~/.wormhole`, `wormholed.sock`, and `wormholed.db` unchanged; no old executable alias is produced.

Historical RFC and issue-reconciliation records remain intentionally unchanged, as required.

## TDD Evidence

1. Added the MCP `serverInfo` assertion expecting `{"name":"gatewayd","version":"0.2.4-alpha"}` and `naming-check` before implementing the rename.
2. `go test ./internal/runtime/localapi` failed as expected because initialization still reported `wormholed`.
3. `make build naming-check` failed as expected because `dist/gatewayd` and `dist/fabric` did not exist.
4. After implementation, focused command-package tests and the full gate suite passed.

## Verification

Passed:

```text
go test ./cmd/gatewayd ./cmd/fabric ./cmd/wormhole
make clean
make build
make naming-check
make fmt-check
make vet
make integration
make race
make coverage
```

The exact old-command scan specified in the task reports only intentionally preserved historical statements in `docs/rfcs/**` and `docs/github-open-issue-reconciliation.md`. Re-running the scan with those immutable historical records excluded returns no matches. `git diff --check` also passed.

## Self-Review

- Confirmed `make build` produces only the three required executables and `naming-check` rejects both old artifact names.
- Confirmed no tracked Go comments, test names, or executable-facing messages retain the old executable names.
- Confirmed all remaining non-historical occurrences are the explicitly preserved socket/database paths or the negative hard-cut assertions.
- A read-only review identified one remaining test-helper identifier, `WORMHOLED_MAIN_HELPER`; it was renamed to `GATEWAYD_MAIN_HELPER`, then every gate above was rerun.
- Preserved the pre-existing, user-owned `.superpowers/sdd/progress.md` modification without staging it.

## Concern

The immutable historical RFC and issue-reconciliation records prevent the task's literal `rg` command from returning zero matches. This is an intentional consequence of the instruction not to alter approved design and historical issue documentation; the scoped semantic scan is clean.

## Commit

`baa527a4363991dbe0a6471624bd0e5763978eb4 refactor: rename Gateway and Fabric binaries`

## Review Remediation: Prefix Boundaries and Wiki Alignment

- Updated the Wiki overview to say `Fabric supplies` and aligned the SQLite and
  PostgreSQL labels beneath Gateway and Fabric.
- Kept executable prefixes exclusively at the Fabric `main` and Gateway
  `runMain` process boundaries. Internal startup errors retain their diagnostic
  context and `%w` causes without embedding a second executable prefix.

### RED

The new exact-output regressions failed before the production changes:

```text
go test ./cmd/fabric ./cmd/gatewayd -run 'TestRunServerWithOpenReturnsDatabaseFailureBeforeServing|TestRunMainReportsConfigFailureWithOneGatewayPrefix' -count=1
--- FAIL: TestRunServerWithOpenReturnsDatabaseFailureBeforeServing
runServerWithOpen error text = "fabric: open database: database unavailable", want "open database: database unavailable"
--- FAIL: TestRunMainReportsConfigFailureWithOneGatewayPrefix
runMain error text = "gatewayd: load config: ...", want "load config: ..."
FAIL
```

### GREEN

```text
go test ./cmd/fabric ./cmd/gatewayd -run 'Test(ServerMainExitsOneWhenWiringFails|RunServerWithOpenReturnsDatabaseFailureBeforeServing|RunMainReportsConfigFailureWithOneGatewayPrefix)' -count=1
ok github.com/H4RL33/wormhole/cmd/fabric
ok github.com/H4RL33/wormhole/cmd/gatewayd
```

The source-prefix audit now finds `fabric:` only at Fabric's `main` boundary
and `gatewayd:` only at Gateway's `runMain` boundary (plus test assertions).
