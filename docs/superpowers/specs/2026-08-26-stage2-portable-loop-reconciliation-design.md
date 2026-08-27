# Stage 2 Portable Loop Reconciliation Design

Date: 2026-08-26
Status: approved design, pending written-spec review

## Goal

Finish the Git-native feature branch with the minimum Gateway supervisor, journalled
`wormhole setup`, native Codex and Claude connector lifecycle, and a complete
two-clone portable-state acceptance loop. Stop after the Stage-2 review boundary;
multi-Fabric, private identity/OIDC, the external issue-56 trial, and user-facing Code
Graph delivery remain separate follow-on branches.

## Current Baseline

The branch already provides the portable Slice-A foundation: strict tracked
`.wormhole/state/v1/` codecs, repository/workspace registration, durable overlays,
semantic import/diff/rebase, publication review, stash/restore, checkpoint/recovery,
private schema-v6 hard cut, workspace revisions, compact confirmation, and the
six-coordinator `projectstate.Service` facade.

The older Stage-2 plan predates the schema-v6 hard cut, the reduction programme, and
the coordinator decomposition. It also describes optional Code Graph work that is no
longer part of this branch's acceptance boundary. The implementation plan must consume
the current types and coordinator facade directly and must not recreate superseded
persistence proofs, migration compatibility, or Service dependency fields.

## Selected Approach

Use an aggressive closed-pre-alpha cutover:

- implement the reconciled equivalents of setup-plan Tasks 0–2 and 7–12;
- keep Fabric behind a fail-closed local-only provider;
- retain existing Code Graph implementation packages internally but hide every public
  Code Graph command, setup choice, contract entry, and Stage-2 delivery claim;
- delete legacy `wormhole init`, `wormhole join`, and `wormhole connect` at final
  cutover, with no aliases or compatibility adapters;
- prove the complete portable loop and pause for an explicit human go/no-go.

The rejected alternatives are temporary legacy aliases and expanding Stage 2 to include
Code Graph or multi-Fabric. Both would retain duplicate authority or blur the result of
the portable-loop gate.

## Runtime Architecture

```text
wormhole CLI
  |-- setup
  |-- connector list/install/remove
  `-- workspace status/diff/import/checkpoint/stash
          |
          v
one user-level gatewayd supervisor
  |-- binding-aware private local API
  |-- local identity owner
  |-- six-coordinator projectstate Service
  |-- local-only Fabric provider
  `-- disabled and publicly hidden Code Graph provider
```

The CLI observes a canonical repository cwd but does not select a project, workspace,
binding, or actor. Gateway resolves the exact persisted binding, strips bridge-only
context before public schema validation, and derives the locally assured actor from its
owner-only identity state. Public tool arguments cannot inject routing or authority.

`NewService` and its six package-private coordinators remain the sole project-state
lifecycle facade. Stage 2 may add local API adapters and supervisor wiring, but it may
not move persistence, filesystem, Git, publication, clock, or gate ownership back into
the facade.

## Components and Ownership

### Local identity

An owner-only local identity store owns human profiles, Ed25519 private/public key
pairs, durable agents, connection sessions, the selected-human pointer, and
journal-keyed setup intent receipts. Setup identity selection is idempotent by setup
journal UUID. Recovery completes the same reserved identity and key; it never creates a
replacement identity after a partial write.

The machine-readable setup/status/MCP surfaces return only the bounded public profile.
Private keys, optional email, sensitive paths, and raw identity store bytes never appear
in tracked state, logs, diagnostics, contracts, or public RPC results.

### Binding-aware dispatch

The stdio bridge attaches only its independently observed cwd in a private envelope.
Gateway canonicalizes it, resolves `types.WorkspaceBinding`, attaches that binding to
request context, and removes the private envelope before normal request decoding.
Every project-state handler consumes the resolved binding. Forged cwd, scope, project,
workspace, and actor fields fail closed and cannot cross workspace boundaries.

### Supervisor

One complete constructor wires the local repository, six-coordinator projectstate
service, binding-aware local API, local identity resolver, local-only Fabric provider,
and disabled Code Graph provider. Providers are non-nil and fail with their typed
unavailable errors; there is no unscoped, single-profile, nil-provider, or legacy
constructor fallback.

### Gateway service primitive

The shared command runner executes fixed argv without a shell, bounds output, respects
context cancellation, and never executes repository-supplied content. The service
primitive owns the exact user-level service/unit/socket contract and supports
inspect/install/start/readiness behavior. Unsupported platforms produce an actionable
manual-start diagnostic before mutation rather than claiming equivalent service-manager
semantics.

### Setup journal

The owner-only setup journal stores one canonical root, journal UUID, version, state,
completed stages, safe state digests, bounded confirmed identity selection, connector
backup references, and redacted last failure. It stores no credentials, keys, raw
connector entries, environment values, or sensitive paths.

The complete plan is rendered and confirmed once. The finalized selection and exact
ordered change digests become durable before service, Gateway, identity, connector,
publication, user-config, or repository effects. Repeating the exact selection is
idempotent; a different selection or mutable third state returns confirmed-plan drift
without mutation. A replacement journal requires a separately rendered and confirmed
plan and does not inherit completed effects.

### Git identity suggestions

Setup may read bounded local Git `user.name`, `user.email`, `user.signingkey`, and
`commit.gpgsign` values from the canonical repository. These are suggestions only.
They become execution input only when represented by the confirmed bounded identity
selection. V1 accepts OpenPGP signing-key references only and never opens a key file or
credential helper.

### Native connectors

Codex and Claude adapters discover their clients and strict-inspect only supported
native stdio entries. HTTP, OAuth, hidden-scope duplicates, ambiguous entries, unknown
fields, or other unsupported prior states fail before backup or mutation.

Each `(adapter, connector name)` operation holds one cross-process lock across recovery,
inspection, prior-state CAS, durable backup, durable operation journal, external
mutation, verification, rollback, and completion. The durable progression is
`prepared -> applied -> verified -> complete`; rollback uses
`prepared|applied -> rolled_back -> complete`. Recovery accepts only exact prior or
exact desired state at the documented stage. A third-party state is preserved and
returns a recovery conflict.

Connector list/install/remove are first-class CLI commands. Normal automated tests use
fake config roots and runners. Optional real-client smoke is read-only capability
inspection and never mutates the user's actual connector configuration.

### Setup orchestration and CLI cutover

`wormhole setup` performs:

1. canonical project validation and read-only inspection;
2. complete plan rendering and one confirmation;
3. durable confirmed setup selection;
4. Gateway service installation/readiness;
5. workspace registration and tracked-base import;
6. publication classification configuration and exact digest readback;
7. journal-keyed local identity selection;
8. transactional installation of each selected native connector;
9. final state verification and journal completion.

Every stage has an exact readback predicate and is resumable. A later stage cannot
complete before its prerequisites. Failure of a connector does not roll back an already
durable workspace import, but the connector itself restores and verifies its exact
supported prior state or absence.

The final cutover deletes `init`, `join`, and `connect` dispatch, helpers, tests,
guidance, and contract entries. Workspace status/diff/import/checkpoint/stash remain
top-level human commands backed by the same Gateway domain semantics as MCP.

## Fabric and Code Graph Boundaries

Stage 2 performs no Fabric discovery, DNS, authentication, attach, detach, or remote
operation. Its Fabric provider is explicit local-only/unavailable behavior. Tracked
remote hints remain inert data.

Existing deterministic Code Graph packages remain compiled and tested, but all public
Code Graph CLI commands, MCP tools, help text, setup choices, and contract entries are
hidden on this branch. No Stage-2 test or documentation claims Code Graph delivery.
Scoped databases, isolated workers, complete fingerprints, BM25 retrieval, and their
acceptance gates belong to the later dedicated Code Graph branch.

## Data and Security Boundaries

- Git is the sole accepted project-state authority.
- Tracked `.wormhole/state/v1/` contains portable product state only.
- Identity, setup journals, connector backups/operations, credentials, overlays,
  stashes, checkpoint evidence, and Gateway databases remain machine-private.
- Owner-only stores require effective-user ownership and exact restrictive modes on
  Unix. Unsupported platforms fail closed until equivalent private-state protection is
  designed.
- No setup or connector path executes repository-supplied commands or shell text.
- Gateway derives workspace and actor authority; public callers cannot choose them.
- Fabric and Code Graph public paths are unavailable, not silently degraded.

## Failure Semantics

- Stable input mismatch before confirmation requires replanning.
- Exact prior or desired state after confirmation is resumable.
- Any third state returns confirmed-plan drift and performs no further mutation.
- Corrupt, ambiguous, multiply active, insecurely owned, or noncanonical journal state
  is preserved and refused.
- Connector failure restores exact supported prior state or absence; concurrent
  third-party changes are preserved and blocked.
- Errors and durable failure fields redact credentials, private keys, tokens,
  environment values, connector contents, and sensitive paths.
- No compatibility migration is added for unreleased private schemas or legacy CLI
  commands.

## Verification and Completion Gate

Every implementation task follows TDD, focused RED/GREEN evidence, a bounded commit,
and independent review. The final gate requires:

- cross-process identity/setup/connector crash recovery tests;
- registry-wide forged-routing and cross-workspace isolation tests;
- exact connector rollback and unsupported-prior rejection-before-mutation tests;
- command/contract scans proving `init`, `join`, `connect`, and Code Graph exposure are
  absent;
- CLI/MCP parity for workspace operations and publication acknowledgement;
- clone -> setup -> inspect -> mutate -> diff/publication review -> checkpoint -> Git
  commit/review -> second clean clone -> setup -> equivalent portable reconstruction;
- operational/private state absent from the second clone unless explicitly promoted;
- `make check` with merged statement coverage at least 80 percent;
- release-test, release-rehearsal, detached clean-clone verification, and broad
  whole-branch review with no unresolved Critical or Important findings.

The branch then stops as experimental/internal and waits for an explicit human
go/no-go before multi-Fabric/private identity work begins.

## Plan Reconciliation Rules

The implementation plan will replace the stale all-in-one setup/Code Graph ordering with
nine Stage-2 tasks corresponding to the approved components above. It must use current
schema v6 and the six-coordinator projectstate facade, omit Code Graph Tasks 3–6, and
preserve no transitional aliases. It may reuse valid detailed contracts from the older
plan only when they do not conflict with this design or current code.
