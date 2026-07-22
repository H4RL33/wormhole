# Task 1 Report: Restore and Codify the Quality Gate

## Status

Implemented and committed in two intentionally separate commits: formatting-only
normalization first, then the quality-gate behavior and CI enforcement.

## Changes

- Replaced stale coarse role expectations in `internal/mcp/agent_test.go` with the
  exact migration 000014 backend-engineer permission bundle.
- Added assertions that neither known-role path grants a coarse alias.
- Added deterministic `fmt-check`, `race`, `integration`, `coverage`, and `check`
  Make targets.
- Added the executable 100%-coverage checker and an empty, documented exception
  manifest format.
- Updated CI to retain Postgres and migrations while running formatting, build, vet,
  required integration tests, race tests, coverage, and an always-uploaded coverage
  artifact.

## RED/GREEN Evidence

RED before changing the stale role contract:

```text
FAIL TestRegisterAgentTool_Handler_KnownRole
Permissions: got [task.list ... git.request_review], want superset of
[task.read task.write kb.read kb.write channel.read channel.write]
FAIL TestRegisterAgentTool_Handler_KnownRole_UnionsExplicitPermissions
```

GREEN after replacing the expectations:

```text
ok github.com/H4RL33/wormhole/internal/mcp 0.098s
```

Coverage checker fixture evidence (temporary fixture and profiles removed afterward):

```text
FIXTURE_UNCOVERED_RC=1 FIXTURE_COMPLETE_RC=0
```

The uncovered fixture printed `Covered 0.0%` and the complete fixture printed
`Covered 100.0%`.

## Verification

| Command | Result |
| --- | --- |
| `WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/mcp -run 'TestRegisterAgentTool_Handler_KnownRole' -count=1` | pass |
| `make fmt-check && make build && make vet && make integration` | pass |
| `make race` | pass |
| `make coverage` | expected failure, exit 2; checker reported uncovered functions and total statement coverage `78.1%` |
| `git diff --check` | pass |

## Files

- `internal/mcp/agent_test.go`
- `Makefile`
- `.github/workflows/ci.yml`
- `.github/scripts/coverage-check.sh`
- `docs/testing-coverage-exceptions.md`
- Formatting-only commit also touched the 13 pre-existing gofmt-drift files specified by
  the preflight check.

## Self-review

- Confirmed the exact 13 required backend-engineer permissions are used in both tests;
  the union adds only `task.assign` before that bundle.
- Confirmed all five prohibited coarse aliases are rejected in both tests.
- Confirmed every required Make command exactly includes
  `WORMHOLE_INTEGRATION_REQUIRED=1` where prescribed.
- Confirmed CI keeps the existing service and migration-up/down steps, uses job-level
  database/integration environment variables, and uploads `coverage.out` even after a
  coverage gate failure.
- Confirmed no temporary coverage test fixture or synthetic profile remains in the
  worktree. The ignored `coverage.out` remains as the intentional output of the required
  `make coverage` verification.

## Concerns

`make coverage` is deliberately red until the later coverage work raises repository-wide
coverage to 100.0%; its current failure is actionable and reports individual uncovered
functions plus the total.
