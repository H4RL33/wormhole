# Wormhole Agent Guide

## Mission and State

Wormhole gives humans and agents durable, Git-native project context. Git is the sole accepted
source and portable-state authority. The Stage 2 Gateway is
local-only, exposes exactly 17 agent-facing tools, and never contacts the
optional Fabric binary. Gateway owns clone-local operational and
machine-private state; tracked `.wormhole/state/v1/` owns portable state.
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
  selected human identity, publication policy, and harness connectors.
- A tracked, typed `.wormhole/state/v1/` tree at an observed Git commit is the
  accepted base. Gateway writes a private per-workspace overlay durably and
  materialises it as an uncommitted working-tree candidate
  with `wormhole checkpoint`; checkpoint alone never advances the base.
- One Gateway supports many registered projects and worktrees with distinct
  workspace IDs and overlays. `wormhole.sync.status` reports `offline` and zero
  pending writes.
- Fabric remains an optional separately tested 20-tool PostgreSQL server. It is
  not attached to the Stage 2 Gateway and is not a direct harness endpoint.
- Isolated on-demand Code Graph workers are local, deterministic, model-free,
  and per checkout. They do not sync through Fabric.

Core pillars remain Event Bus, Task Graph, Knowledge Base, and Identity &
Permissions. Wormhole stores no competing copy of repository source.

## Transition State

The 2026-07-28 architecture remains design authority where later approved
Stage 2 decisions do not amend it.
Task-5 `5F`/`5G`, the mandatory Stage 1A review, the approved R01-R05
measured-simplification tranche, and R06 are complete. R06's closed-pre-alpha
private-format hard cut initializes fresh Gateway state directly as schema v6,
reopens exact v6 state without schema mutation, and preserves/refuses every other
existing private database before mutation. Approved implementation commits are
`27f5b85`, `a18b6f4`, and `e1d2df5`; independent review, `make check` (84.8%),
release, rehearsal, and clean-clone gates passed.

After R06's review boundary, all remaining reduction work (R07-R14) is paused.
The separately approved decomposition of `projectstate.Service` behind its existing
facade is complete. Git-native Tasks 1-8 and the Stage 2 final cutover are
implemented. Canonical setup is `wormhole setup`; the five portable workspace
operations are available through both top-level CLI and Gateway MCP. The former
initialisation, join, combined connection, join-shaped registration, and public
structural-discovery surfaces were removed without compatibility aliases. Do not
reintroduce them as migration helpers.

The `projectstate.Service` decomposition tranche is complete: the public Service
is a nil-safe facade over exactly six package-private coordinators (registration,
workspace, publication, checkpoint/recovery, Git-base, and transition). The
facade owns no repository, filesystem, clock, observer, writer, or gate seams;
checkpoint and recovery continue to share one coordinator gate. See
`docs/superpowers/reviews/2026-08-25-projectstate-service-decomposition-report.md`.

## Binaries

- `wormhole`: setup, identity, publication, connector administration,
  first-party Codex/Claude connector lifecycle, portable status/diff/import/
  checkpoint/stash operations, and stdio MCP bridge.
- `gatewayd`: one per-user local-only supervisor, stable owner-private Unix
  socket, schema-v6 SQLite state, durable overlays, and 17-tool MCP registry.
- `fabric`: optional 20-tool HTTP MCP server backed by PostgreSQL; not a Stage 2
  Gateway dependency or acceptance authority.

## Package Ownership and Dependency Bans

- `cmd/*`: process wiring only.
- `internal/mcp`: server MCP registry, envelopes, auth, tool handlers.
- `internal/core/identity`, `events`, `tasks`, `kb`, `permissions`, `git`, `roles`:
  server pillars. Identity separates humans, authenticators, memberships,
  durable agents, ownership, sessions, Passports, credentials, and audit.
- `internal/runtime/localapi`, `localstore`, `eventbus`, `scheduler`, `sync`, `config`:
  local runtime, including explicit project/workspace routing, Git bases,
  private overlays, checkpoints, and retained optional-Fabric packages/profile
  types. The local-only Stage 2 supervisor does not route or instantiate them.
- `internal/runtime/codegraph/{config,golang,schema,store,index,query,source}`: Gateway-local
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
- Optional Fabric project data is protected by Postgres RLS. Only explicitly
  project-agnostic principal, authenticator, and agent records, plus explicitly
  global registration configuration such as `role_templates`, are global (see
  `docs/implementation-rules.md` D3).
- Localstore queries require explicit project namespace and workspace scope.
  Add cross-namespace and cross-workspace tests for localstore changes.
- Portable candidate writes become durable in the private overlay before
  checkpoint. Presence and `channel.post` events are operational and never
  automatically becomes `EventV1`; explicit source-bound promotion alone makes selected
  audit evidence portable. Promotion copies the source event projection exactly, keeps
  the source actor in `EventV1`, and records the distinct promoter on `OperationV1`.
  Ordinary activity expires when older than 30 days or outside the newest 10,000
  unprotected workspace rows; lifecycle evidence is excluded until terminal, then uses
  an exact 30-day default or a finite advertised longer duration.
- Passport tokens and credentials are secrets. Do not log them. Server stores token
  hashes. Keep socket and credential file permissions restrictive.
- Setup and top-level workspace commands are human private-control operations;
  public MCP tool calls are agent-attributed. Both share projectstate semantics
  but not the same authority surface.

## MCP Surface

MCP is the stateless agent-facing project-operation contract. The live Gateway
surface is exactly 17 tools: three local agent-presence tools, five channel
tools, three deterministic KB tools, truthful local-only `sync.status`, and
five workspace tools. There is no live Gateway enrolment, whoami, guidance
read, semantic search, task, Git-link, remote bootstrap, or live sync tool.

Harnesses use `wormhole mcp` and the local Gateway; never add a direct remote
harness path. The bridge injects private working-directory context and Gateway
removes it before public schema validation. Clients cannot choose binding,
human, assurance, accountable owner, session, or action actor.

## Development Protocol

Read task sources, relevant RFC sections, `docs/implementation-rules.md`, and local
precedent before editing. Keep smallest correct diff. Match `internal/core/identity` for
Core store shape. Run focused tests first, then required full checks. Do not guess across
an RFC open question: use conservative documented behavior or escalate. Do not alter
unrelated worktree changes.

## Identity and Session Decision

Humans and agents are separate durable machine-private principals. Setup
selects one human. Gateway derives a durable harness agent for that human and
creates a fresh connection session. Agent actions capture agent, accountable
human, harness/model session, and assurance at action time. Local MCP
`clientInfo` harness/model values are self-declared provenance
bound by Gateway to its own session; local assurance does not independently
verify either value. Portable actor records are repository-visible identity
statements, not local credentials or authority. CLI-attributed human envelopes
retain their Gateway session and `wormhole-cli` version
provenance without agent, owner, or model fields. The owner-private CLI
capability separates compliant local protocol paths and binds accountability;
it does not prove physical human presence or defend against hostile same-user
processes, which remain inside the approved same-user local trust boundary.

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

Workspace IDs, overlays, stashes, recovery journals, optional Fabric credentials,
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
