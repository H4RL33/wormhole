# Wormhole Agent Guide

## Mission and State

Wormhole gives humans and agents shared, durable project context. Git is
the sole code truth and accepts explicitly curated portable Wormhole project state.
Gateway/Fabric own finite-retention operational collaboration. Wormhole gives
typed semantics to events, task state, KB records, identities, permissions, and
links to code. The repository builds the CLI, local Gateway, and optional Fabric.
V1 is project/repository-lineage scoped and agent-first; it defines no implicit
organisation-wide graph, merged KB, inherited policy, or cross-project authority.

## Authority Order

Authority order: RFC-0001, with RFC-0003 overriding it only where RFC-0003
explicitly amends local-runtime, transport, workspace, or optional-coordination
assumptions; RFC-0002 governs optional Governance; the approved
`docs/superpowers/specs/2026-07-28-git-native-wormhole-architecture-design.md`
defines their version-one contract details, with
`docs/superpowers/specs/2026-08-01-publication-classification-review-cas-amendment.md`
governing the trusted publication-policy/origin/review-CAS boundary and its then-current
successor migration numbering, and
`docs/superpowers/specs/2026-08-11-task5-fallback-checkpoint-recovery-simplification-design.md`
narrowly amending the Task-5 V1 publication/recovery mechanism and platform boundary, and
`docs/superpowers/specs/2026-08-17-stage1a-r01-r05-foundation-reduction-design.md`
recording the Stage 1A human decisions and narrowly governing the selected R01-R05
private-persistence reduction after its approved written-spec review gate;
and `docs/superpowers/specs/2026-08-24-r06-private-format-hard-cut-design.md`
governing the approved closed-pre-alpha private Gateway format hard cut;
`docs/implementation-rules.md`;
existing code.

RFC tool shapes are indicative unless code freezes them. Governance is optional and
must not leak into Core code.

## Layer Architecture

- Stateless harness connectors call the one user-level `gatewayd` supervisor by
  local MCP IPC.
- The human-first `wormhole` CLI installs and configures Gateway, workspaces,
  identities, optional Fabrics, and harness connectors.
- A tracked, typed `.wormhole/state/v1/` tree at an observed Git commit is the
  accepted base. Gateway writes a private per-workspace overlay durably before
  optional sync and materialises it as an uncommitted working-tree candidate
  with `wormhole checkpoint`; checkpoint alone never advances the base.
- One Gateway supports many projects, worktrees, and explicit Fabric profiles.
  Each workspace has zero or one writable Fabric stream.
- Fabric is optional for public and private projects. It owns its Postgres plus
  pgvector projection, membership, remote audit, and operational activity under an
  exposed finite retention policy; it cannot overwrite a
  divergent Git base. A canonical-public hint activates only when `origin`
  matches the canonical repository identity; a fork/mismatch makes no upstream
  Fabric contact, read, or write and may bind only an independent realm.
- Isolated on-demand Code Graph workers are local, deterministic, model-free,
  and per checkout. They do not sync through Fabric.

Core pillars remain Event Bus, Task Graph, Knowledge Base, and Identity &
Permissions. Wormhole stores no competing copy of repository source.

## Transition State

The 2026-07-28 architecture is authoritative but not yet fully implemented.
Task-5 `5F`/`5G`, the mandatory Stage 1A review, and the approved R01-R05
measured-simplification tranche are complete. The approved R06 design authorizes
one closed-pre-alpha private-format hard cut: fresh Gateway state is initialized
directly as schema v6, exact v6 state reopens without schema mutation, and every
other existing private database is preserved and refused before mutation. The
implementation candidate is `27f5b85`; it remains pending the independent review
and final repository gates, so do not describe R06 as released until those gates
pass.

After R06's review boundary, all remaining reduction work (R07-R14) is paused.
The next authorized tranche is decomposition of `projectstate.Service` behind its
existing facade; it must be separately planned and reviewed, and it must not be
silently folded into R06. Subsequent work returns to feature delivery toward the
Git-native branch goal. Tasks 6, 6A, 7, 8, Stage 2, and unrelated preparation remain
non-executable unless the next explicit decision expands scope.
Current code still contains legacy `join`/`connect`, single-profile bootstrap,
Passport-only attribution, and pre-snapshot assumptions. Implementation plans
must migrate those paths in tested slices. Do not document a target command or
schema as shipped until its implementation and contract checks pass.

## Binaries

- `wormhole`: setup, identity/auth, project/Fabric/connector administration,
  first-party Codex/Claude connector lifecycle, checkpointing, and stdio MCP
  bridge.
- `gatewayd`: one per-user passive supervisor, stable Unix socket API, SQLite
  control/read model, durable overlays and queues, and worker lifecycle.
- `fabric`: optional public/private coordination service, HTTP MCP boundary,
  Git-aware stream verifier, and Postgres-backed Core.

## Package Ownership and Dependency Bans

- `cmd/*`: process wiring only.
- `internal/mcp`: server MCP registry, envelopes, auth, tool handlers.
- `internal/core/identity`, `events`, `tasks`, `kb`, `permissions`, `git`, `roles`:
  server pillars. Identity separates humans, authenticators, memberships,
  durable agents, ownership, sessions, Passports, credentials, and audit.
- `internal/runtime/localapi`, `localstore`, `eventbus`, `scheduler`, `sync`, `config`:
  local runtime, including explicit project/workspace routing, Git bases,
  private overlays, checkpoints, and multiple Fabric profiles.
- `internal/runtime/codegraph/{config,golang,store,index,query,source}`: Gateway-local
  Code Graph configuration, compiler analysis, derivative SQLite state, revision
  publication, bounded query, and transient hash-validated source assembly only;
  never Fabric state or persisted source bodies.
- `internal/storage`: server DB open only. `internal/types`: shared plain types/config.
- `internal/webui`: read-oriented human dashboard.
- `internal/core/*` never imports `internal/mcp`; core-to-core imports are banned except
  `tasks` to `events` for status events.
- `internal/runtime/*` never imports `internal/core/*` or `internal/mcp`. `localapi`
  may import all sibling runtime packages because it wires them together; other runtime
  packages must not import `localapi`.
- Code Graph dependencies are `store` → `config`, `index` →
  `config`/`golang`/`store`, and `query` → `config`/`source`/`store`;
  `config` and `source` use only the standard library, while `golang` uses the
  standard library plus `golang.org/x/tools/go/packages`. These packages do not
  import `localapi`, `sync`, Core, or MCP and do not use Fabric migrations,
  models, embeddings, vectors, or implicit network access. Status and query fail
  closed unless the graph's analysis fingerprint matches tracked source and
  adapter-declared inputs plus build, adapter, and toolchain identity.
- `internal/types` imports stdlib only. No new top-level package or external dependency
  without human approval. No ORM, global singleton, `init()` registration, or control-flow
  `panic`.

## Data and Security Invariants

- Git remains sole code truth. Source integration stores commit SHA, PR URL,
  and commentary only. Typed Wormhole project records may be tracked beneath
  `.wormhole/`; source bodies may not.
- Tracked `.wormhole/` files are structurally eligible repository-visible project
  content, visible to every principal with Git access; they are never credentials or
  authority. Secret-shape validation is not confidentiality detection. Trusted
  machine-private `public_git` classification (canonical or fork) warns, and checkpoint
  requires an exact attributed publication-review digest
  acknowledgement from either CLI or MCP; Wormhole makes no DLP claim.
  Classification is explicit user policy bound to workspace/repository identity, not
  continuous host-visibility detection. After first configuration, observed semantic
  origin or repository-identity changes stickily invalidate it with a monotonic policy
  revision; same-identity visibility changes require explicit setup reconfiguration.
  Machine-private state and secrets stay outside the repository.
- All project-scoped Fabric data is protected by Postgres RLS. Only explicitly
  project-agnostic principal, authenticator, and agent records, plus explicitly
  global registration configuration such as `role_templates`, are global (see
  `docs/implementation-rules.md` D3).
- Localstore queries require explicit project namespace and workspace scope.
  Add cross-namespace and cross-workspace tests for localstore changes.
- Local writes become durable before sync. Ephemeral presence/heartbeat events stay in
  eventbus. Generic task/channel/progress/runtime activity is operational and never
  automatically becomes `EventV1`; explicit source-bound promotion alone makes selected
  audit evidence portable. Promotion copies the source event projection exactly, keeps
  the source actor in `EventV1`, and records the distinct promoter on `OperationV1`.
  Ordinary activity expires when older than 30 days or outside the newest 10,000
  unprotected workspace rows; lifecycle evidence is excluded until terminal, then uses
  an exact 30-day default or a finite advertised longer duration.
- Passport tokens and credentials are secrets. Do not log them. Server stores token
  hashes. Keep socket and credential file permissions restrictive.
- Humans and agents have equivalent authorised project operations through CLI and MCP,
  while typed schemas, progressive disclosure, autonomous durability, attribution, and
  handoff remain agent-first.
  Human authentication, ownership transfer, credential recovery, membership,
  and policy administration remain human control-plane operations.
- Git/Fabric divergence uses semantic three-way rebase with explicit conflicts;
  never introduce last-write-wins.

## MCP Surface

MCP is the stateless agent-facing project-operation contract. Core names use
`wormhole.<pillar>.<verb>` for agent, channel, task, KB, and git operations.
`wormhole.sync.*` is Gateway-to-Fabric sync;
`wormhole.workspace.{status,diff,import,checkpoint,stash}` provides equivalent
local project-state operations for agents; and
`wormhole.code_graph.{status,query,rebuild}` is the RFC-0003 Gateway-local
derivative namespace. The latter two are not Core pillars. Harnesses use local
Gateway; do not add a direct remote harness path. Human CLI project operations
must share the same Gateway domain semantics. Private remote auth and
permission enforcement remain at the Fabric boundary; local/public assurance
is explicit in the actor envelope.

## Development Protocol

Read task sources, relevant RFC sections, `docs/implementation-rules.md`, and local
precedent before editing. Keep smallest correct diff. Match `internal/core/identity` for
Core store shape. Run focused tests first, then required full checks. Do not guess across
an RFC open question: use conservative documented behavior or escalate. Do not alter
unrelated worktree changes.

## Identity and Session Decision

Humans and agents are separate durable principals. Authenticators prove a human
identity; project memberships authorise private Fabric access; ownership records
make a human accountable for an agent. Passports are project-scoped Fabric
capability grants, not the human identity or the agent itself. Agent actions
capture agent, accountable human, harness/model session, and assurance at action
time. Local/fork actors are self-declared, public Fabric uses key continuity,
and private Fabric uses authenticated membership.

## Build and Test Commands

```bash
# focused package or behavior tests first
make check
```

Merged statement coverage must remain at or above 80%. Before a milestone or
release claim, also run the repository release test and rehearsal gates. Use
`make build` for binaries in `dist/`. Integration tests use Postgres when
available and may skip unless `WORMHOLE_INTEGRATION_REQUIRED=1`.

## Config and Credential Paths

- Project base: nearest `.wormhole/config.toml`, optional
  `.wormhole/remotes.toml`, and
  `.wormhole/state/v1/` from current directory upward.
- Global config: `$XDG_CONFIG_HOME/wormhole/config.toml`, else
  `~/.config/wormhole/config.toml`.
- CLI and runtime credential profiles: `~/.wormhole/credentials/*.json`.
- Runtime socket: `$XDG_RUNTIME_DIR/wormhole/wormholed.sock`, else
  `$TMPDIR/wormhole-runtime/wormhole/wormholed.sock`.
- Runtime SQLite: `$XDG_DATA_HOME/wormhole/wormholed.db`, else
  `~/.local/share/wormhole/wormholed.db`.

Workspace IDs, overlays, stashes, recovery journals, Fabric credentials,
connector backups, and Code Graph databases are machine-private and remain
outside the repository. Legacy `.wormhole/integration-state.json` must be
migrated/ignored, never committed.

The private Gateway SQLite database is a closed-pre-alpha format. The current
binary supports only a fresh initialization or an exact schema-v6 database. It
does not migrate, export, reset, normalize, or delete an older, future, malformed,
partial, or proof-incompatible database. An unsupported database is left in place;
inspect unpublished overlays/checkpoints, make an operator backup, stop Gateway,
and remove it only as an explicit manual action before rerunning setup. This rule
does not change the portable, Git-tracked `.wormhole/state/v1/` format.

`wormholed.sock` and `wormholed.db` are retained local-state filenames, not
legacy executable aliases. Invoke `gatewayd`, never a former daemon name.

## Delivery and Compatibility Policy

The intended required CI contexts are `Contract Inventory`, `Static`, `Build`,
`Integration`, `Race`, `Coverage`, `Migrations`, `Vulnerability`, `Secret Scan`,
and `Action Pins`; `Dependency Review` is pull-request-only. Do not represent
these as hosted protections until their GitHub configuration has been read back
and verified. An emergency owner bypass requires a follow-up issue with reason,
impact, verification debt, and corrective action.

`docs/releasing.md` distinguishes non-publishing rehearsals from guarded tag
publication. `docs/compatibility.md` records the current `alpha-inventory`
policy; no beta compatibility promise exists.

## Live-Doc Map

- RFCs: `docs/rfcs/`.
- Current architecture contract:
  `docs/superpowers/specs/2026-07-28-git-native-wormhole-architecture-design.md`.
- Implementation guardrails: `docs/implementation-rules.md`.
- Data entities: `docs/db-entities.md`; KB rules: `docs/kb-schema.md`.
- MCP transport/auth: `docs/mcp-protocol.md`.
- Product connector setup: `docs/claude-code-connector.md`; the first-party
  Codex lifecycle contract and smoke-test target are in the current architecture
  design until a dedicated Codex document ships. Codex uses
  `codex mcp add wormhole -- /absolute/path/to/wormhole mcp`, with transactional
  inspect/apply/verify/rollback/remove.
- Contributor and security entrypoints: `CONTRIBUTING.md`, `SECURITY.md`, `README.md`.
