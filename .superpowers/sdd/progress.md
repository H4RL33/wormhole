# Task 4 Implementation Progress

**Base commit:** 46becdd (before Task 4 work)

## Task 4: Retarget `wormhole connect` at stdio bridge

- Status: IMPLEMENTING (implementer dispatched, ID: aa1e03bd9e50539d2)
- Expected changes:
  - `cmd/wormhole-cli/main.go`: Add `stdioBin` flag, wormholed reachability check, stdlib wiring for both Claude and OpenCode branches
  - `cmd/wormhole-cli/main_test.go`: Rewrite existing connect tests, add wormholed-unreachable test
- Implementation: TDD (red → green)
