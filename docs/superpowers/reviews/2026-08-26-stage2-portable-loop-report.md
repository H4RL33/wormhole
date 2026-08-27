# Stage 2 Portable-Loop Cutover Report

**Scope:** Stage 2 Task 9 final public cutover and portable-loop acceptance
**Date:** 2026-08-27
**Candidate commit:** `feat(cli): complete Gateway setup cutover`

## Outcome

The live public interface is now the Git-native setup and workspace loop. The
CLI exposes top-level `status`, `diff`, `import`, `checkpoint`, and `stash`; the
Gateway exposes the matching `wormhole.workspace.*` MCP tools. Both paths call
one `executeWorkspaceCommand` operation layer. Gateway owns cwd-to-workspace
binding and actor attribution. A `public_git` checkpoint accepts only the exact
current publication-review digest.

The previous `init`, `join`, and combined connection commands were deleted, not
aliased. Public registration now has one presence-only shape. All public
structural-discovery CLI, MCP, guidance, setup, help, and alpha-contract entries
were removed. The independent `internal/runtime/codegraph/...` packages and
their tests remain intact and pass.

Wormhole materialises a portable working-tree candidate only. It never stages,
commits, or pushes Git changes.

## TDD evidence

The new cutover and portable-loop tests were run before implementation:

```text
go test ./cmd/wormhole ./internal/runtime/localapi \
  -run 'Stage2Final|Stage2Workspace|Stage2Portable' -count=1
```

RED failed because the five workspace operations/backend did not exist and the
removed command, join-shaped registration, and public graph surfaces were still
present. The final focused run is GREEN:

```text
go test ./cmd/wormhole ./cmd/gatewayd ./internal/runtime/localapi \
  -run 'Stage2Final|Stage2Workspace|Stage2Portable' -count=1
ok  github.com/H4RL33/wormhole/cmd/wormhole
ok  github.com/H4RL33/wormhole/cmd/gatewayd [no tests to run]
ok  github.com/H4RL33/wormhole/internal/runtime/localapi
```

The complete owning packages also pass:

```text
go test ./cmd/wormhole ./cmd/gatewayd ./internal/runtime/localapi -count=1
ok  github.com/H4RL33/wormhole/cmd/wormhole            1.142s
ok  github.com/H4RL33/wormhole/cmd/gatewayd            1.228s
ok  github.com/H4RL33/wormhole/internal/runtime/localapi 4.236s
```

## Public cutover

- CLI dispatch/help and the alpha inventory no longer contain the removed
  commands or public graph commands.
- The registration descriptor has one default request/result variant. Its only
  public inputs are the local presence identity and optional capabilities; it
  cannot accept ownership, model, roles, permissions, repositories, Passport,
  or credential material.
- The Gateway registry has exactly 27 tools: the previous 25 minus three public
  graph entries plus five workspace operations.
- The old CLI and local API graph adapters, graph-specific public tests, and
  generated graph guidance were removed. `internal/runtime/codegraph/...` was
  not changed or deleted.
- README, connector guide, compatibility policy, alpha-contract guide, agent
  context, implementation rules, generated guidance, current wiki CLI guide,
  release acceptance fixture, and machine-readable contract changed together.

The exact prescribed scan reports only `runConnector` and `wormhole connector`
locations. Its `runConnect|wormhole (join|connect|init)` alternatives lack a word
boundary and therefore match the required `connector` command. A boundary-corrected
form of the same scan returns zero legacy/public graph matches. No listed hit is a
removed connection command or public graph surface.

## Real two-clone proof

`TestStage2PortableLoopAcrossTwoRealClones` uses Git processes and temporary
files, not an in-memory repository fake:

1. create a seed repository and bare remote;
2. create clone A with `git clone --no-local` and a fresh private schema-v6 DB;
3. register/import the tracked `.wormhole/state/v1/` base;
4. apply an operation containing the Gateway-owned human actor and prove the
   exact actor remains in the durable overlay row;
5. classify the workspace `public_git`, obtain the semantic diff and review
   digest, and acknowledge that digest at checkpoint;
6. prove HEAD, remote HEAD, and Git index are unchanged by every Wormhole call,
   with only portable unstaged state in the working tree;
7. use ordinary fixture-owned Git add/commit/push to accept the candidate;
8. create clone B with `git clone --no-local` and another fresh private DB;
9. register/import and prove clone B's accepted portable digest equals clone A's
   candidate digest and both commits contain the same portable subtree object;
10. prove clone B has zero overlay, materialisation, stash, conflict, transition,
    legacy migration, Task, Channel, Event, agent, Passport, sync-queue, sync-audit,
    or enrolment rows, and no tracked private/operational path.

The test passes in both the focused and full owning-package runs.

## Verification

| Gate | Result |
|---|---|
| Focused Stage 2 cutover/portable loop | PASS |
| Full CLI/Gateway/local API packages | PASS |
| Preserved `internal/runtime/codegraph/...` packages | PASS |
| Focused race: CLI, Gateway, local API, config, connectors | PASS |
| Alpha CLI/Gateway contract and generated-guidance tests | PASS |
| `go test ./... -count=1` | All reported non-PostgreSQL packages PASS; PostgreSQL-backed `internal/mcp` fails on `localhost:5432` connection refusal |
| `make check` build and vet | PASS before required integration phase |
| `make check` required integration | BLOCKED only by unavailable PostgreSQL on `localhost:5432`; tests were not skipped or weakened |
| `make release-test` | PASS |
| `make release-rehearsal` | Both architecture archives and SPDX/checksums built and verified; Docker image verification BLOCKED because `/var/run/docker.sock` is absent |
| `git diff --check` | PASS |

Docker CLI is installed, but `docker info` reports no daemon socket. `pg_isready`
is not installed and direct test connections to PostgreSQL port 5432 are refused.
No integration-required flag, assertion, coverage threshold, or test was relaxed.

## Review boundary and concerns

The controller must perform the broad whole-branch review and detached
`git clone --no-local` verification against the exact candidate commit before
push. No push was performed by this task.

Remaining environmental debt is limited to rerunning the PostgreSQL-required
`make check` phases and Docker-dependent end of `make release-rehearsal` on a host
with those services. The code-level focused, owning-package, race, contract,
portable-loop, preserved-internal-package, and release-test gates are green.
