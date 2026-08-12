# Wormhole Implementation Rules & Dispatch Heuristic

**Audience:** implementation agents (any model tier) making changes to this repo.
Authority order: RFC-0001, with RFC-0003 overriding it only where RFC-0003
explicitly amends local-runtime, transport, workspace, or optional-coordination
assumptions; RFC-0002 governs optional Governance; the approved
`docs/superpowers/specs/2026-07-28-git-native-wormhole-architecture-design.md`
defines their version-one contract details, with
`docs/superpowers/specs/2026-08-01-publication-classification-review-cas-amendment.md`
governing publication policy, origin, review CAS, and its durable proof, and
`docs/superpowers/specs/2026-08-11-task5-fallback-checkpoint-recovery-simplification-design.md`
narrowly governing the Task-5 V1 publisher/recovery mechanism and platform boundary;
`docs/implementation-rules.md`;
existing code.
This document derives from the RFCs and current code; if it conflicts with an RFC, the RFC
wins and this file has a bug — flag it, don't silently pick one.

This is a *constraint document*, not a tutorial. Every section states rules. If a task
requires breaking a rule here, stop and escalate to the orchestrating agent or human;
do not improvise.

Approved programme and slice-plan execution amendments control task scope and sequencing.
They cannot weaken an RFC requirement, but they can defer an otherwise described task or
private implementation choice. The current amendment permits only the fallback-only Task-5
`5F`/`5G` checkpoint-and-recovery boundary defined in
`docs/superpowers/specs/2026-08-11-task5-fallback-checkpoint-recovery-simplification-design.md`,
followed by the mandatory Stage 1A simplification gate and hard pause; Tasks 6, 6A, 7, 8,
and Stage 2 require a later explicit human go/no-go.

---

## 1. Dispatch Heuristic — Direct Edit vs Subagent-Driven

**Use direct edit (bypass subagent-driven-development) if ALL conditions hold:**

1. **Single file touched** — change is contained to one file only
2. **≤100 lines of code** — total additions/modifications ≤100 lines
3. **No RFC ambiguity** — task cites an RFC section, decision is unambiguous
4. **No cross-pillar implications** — touches only one pillar (events, tasks, kb, identity, permissions) OR only config/docs/tests

**Otherwise → subagent-driven-development.**

**Dispatch examples:**

| Change | Route | Reasoning |
|---|---|---|
| Fix typo in docs/kb-schema.md | Direct | Single file, <5 lines, doc only |
| Add config flag to cmd/gatewayd | Direct | Single file, <20 lines, operational |
| Implement `wormhole.task.update_status` | Subagent | RFC §8.2: transitions must emit events; crosses tasks + events pillars; needs transaction pattern |
| Add integration test to identity_test.go | Direct | Single file, testing only, no RFC ambiguity |
| Refactor internal/core/* | Subagent | Multi-file, uncertain precedent, cross-pillar |
| Update KB schema + migration | Subagent | Touches schema (D1–D3 rules), multiple files, coordination needed |
| Update comment in permission checker | Direct | Single file, <3 lines, doc-only |

**Rationale:** Small, obvious changes (typos, config, single-file tests) unblock fast iteration. Complex changes get subagent isolation + oversight. Conservative heuristic errs toward subagent dispatch; override by human review if justified.

---

## 2. Operating Protocol — How to Think Before You Type

Most defects in agent-written code are not coding errors. They are *reasoning* errors made
before the first line: guessing instead of reading, importing habits from other codebases,
expanding scope, and declaring victory without evidence. This section is the antidote.
Follow it as a literal procedure, in order, for every task.

### 2.1 Restate the task in one sentence

Write (in your working notes, not the code) one sentence: *"This task is complete when
___."* If you cannot fill the blank precisely, you do not understand the task — go back to
the task description or escalate. Everything you do next must serve that sentence. Anything
that doesn't serve it is scope creep, even if it's a genuine improvement. Note improvements
separately; do not implement them.

### 2.2 Read before you write — minimum reading list

Never write code from memory of "how Go projects usually work." This repo has one way of
doing each thing, and it is written down. Before editing, read:

| Task touches | Must read first |
|---|---|
| Any core package | `internal/core/identity/identity.go` (the canonical pattern) + the package you're editing |
| DB schema | `docs/db-entities.md` + the latest migration pair in `migrations/` |
| MCP tools | `internal/mcp/registry.go` + RFC-0001 §9 |
| Git-native workspace, setup, identity, Fabric routing, or Code Graph | `docs/superpowers/specs/2026-07-28-git-native-wormhole-architecture-design.md` + RFC-0003 |
| Tests | `internal/core/identity/identity_test.go` |
| Anything at all | The RFC section the task cites; §1–2 of this document |

If the task cites an RFC section, read that exact section — not your recollection of it.
The RFCs are short; reading the real text costs less than one wrong assumption.

### 2.3 Locate the precedent, then copy it

For any construct you're about to write (a store method, a migration, an error, a test),
ask: *"Where does this repo already do something shaped like this?"* Find it, open it,
and match it — naming, error style, transaction shape, comment density. The correct
implementation of almost every Core task is *"the identity package's pattern, applied to
a new entity."* If you find **no** precedent, that's a signal, not a licence: it means the
construct is new to the repo, and new constructs need a sanity check against §4's rules and
§10's tripwires before you invent one.

Concretely, this rules out reflexes learned elsewhere: no ORM (no GORM/sqlx/ent), no web
framework (no gin/echo — stdlib `net/http`), no `panic` for control flow, no global
singletons, no `init()` registration magic, no context stored in structs, no interfaces
defined before a second implementation exists.

### 2.4 The ambiguity ladder — what to do when the task under-specifies

Ambiguity is normal; guessing is the failure. Resolve in this exact order, stopping at the
first rung that answers the question:

1. **RFC text and Decision Registers.** Does RFC-0001 state it, does RFC-0002
   state it for optional Governance, or does RFC-0003 explicitly amend a
   local-runtime, transport, workspace, or optional-coordination assumption?
   A decided entry binds. An entry listed as open remains open and may not be
   resolved as a side effect. RFC-0003 does not otherwise supersede RFC-0001.
2. **Approved architecture contract.** For the 2026-07-28 migration, does the
   Git-native architecture design specify the version-one detail? Follow it.
3. **`docs/db-entities.md`** for anything entity-shaped.
4. **Existing code.** Does the repo already embody a compatible answer? Match it.
   Legacy code that the architecture explicitly supersedes is migration input,
   not precedent.
5. **This document's rules.** Do §4–§7 constrain it to one option?
6. **None of the above** → stop and escalate with a concrete question and your
   recommended answer. "Should `task.assign` accept a human owner? RFC §8.2 says owner
   is 'agent or human' but the agents table has no human rows — I recommend X because Y"
   is a good escalation. "What should I do?" is not.

The test for whether you guessed: could you cite a source (rung 1–5) for every decision in
your diff? If a decision traces only to "seemed reasonable," it's a guess — surface it.

### 2.5 Smallest correct diff

The right diff is the smallest one that makes the task-complete sentence (§2.1) true while
obeying every rule in this document. Do not: reformat untouched code, rename things for
taste, add "while I'm here" fixes, add configuration for needs that don't exist yet, or
build abstractions for the *next* task. If you notice a real adjacent bug, report it in
your output; touch it only if told to.

A useful self-check before finishing: for each hunk in your diff, can you say which part
of the task sentence it serves? Hunks that serve nothing get reverted.

### 2.6 When something fails — debugging discipline

1. **Read the actual error.** The full message, not the first line's vibe. Quote it in
   your notes.
2. **Form a hypothesis before changing anything.** "The test fails because X" — then
   verify X by reading code or adding one targeted print/query, not by shotgunning edits.
3. **Never make a failure disappear without explaining it.** Deleting the assertion,
   widening the accepted values, wrapping in a retry, or skipping the test are all the
   same move: hiding evidence. A test that fails is information about the code; the code
   moves toward the test, not the reverse — unless you can *prove* the test itself
   contradicts the RFC, in which case say so explicitly with the citation.
4. **Three failed fix attempts = stop.** You are missing something structural. Write up
   what you tried, what you observed, your current hypothesis — and escalate. A clean
   handoff after 3 attempts is worth more than a mess after 10.

### 2.7 Evidence before "done"

"It should work" is not a state of the world; it's a feeling. Done means: you ran the
commands in T4, you read the output, and the output says pass. Paste the decisive lines
(final test summary, not the full log) into your completion report. If you could not run
verification (missing DB, sandbox limits), the status is **not** "done" — it is "written,
unverified, because ___", stated exactly that way.

### 2.8 Invariant proportionality review gate

Every new or strengthened invariant must name, in its task brief and review evidence:

1. its owning requirement;
2. the concrete failure or threat it prevents;
3. V1 likelihood evidence, or an explicit assumption when evidence is unavailable; and
4. why fail-closed handling, manual repair, recovery to the last known complete state, or
   another simpler recovery path is insufficient.

Absent all four, review blocks the change. A plausible hypothetical is not enough to add
durable state, a compatibility commitment, a new mutation boundary, or redundant proof.

### 2.9 Rationalisations to catch yourself making

| The thought | The reality |
|---|---|
| "This is standard practice" | Standard where? This repo's practice is §5. Match it. |
| "The RFC probably means..." | Open the RFC. It's one file away. |
| "I'll add this field, it'll be needed later" | Later's task adds it. Yours doesn't. (§2.5) |
| "The test is too strict" | The test encodes a security property. Prove it wrong or satisfy it. (§2.6) |
| "This helper would be cleaner in a shared package" | Cross-core imports are banned (R2). Duplicate or escalate. |
| "Mocking the DB makes tests simpler" | T1 exists because mocks pass while RLS fails. Real Postgres. |
| "It compiles, so it's done" | Compiling is not passing. (§2.7) |
| "This is basically like [famous project]" | Wormhole rejects several famous patterns on purpose. Precedent is this repo only. (§2.3) |
| "I'll just resolve this ambiguity quietly" | Silent decisions are how policy drift starts. Ladder, then flag. (§2.4) |

---

## 3. System in One Paragraph

Wormhole has four layers: stateless harness MCP connectors, the human-first
`wormhole` CLI, one user-level Gateway supervisor, and optional public/private
Fabrics. A repository's typed `.wormhole/state/v1/` tree at an observed commit is
the accepted Git base; Gateway stores a durable private overlay per
checkout/worktree and materialises an uncommitted candidate through deterministic,
compare-and-swap checkpoints. Checkpoint alone never advances the base. Gateway
may connect different workspaces to
different Fabrics, but each workspace has at most one writable stream. Git is the
sole code truth and accepts explicitly curated portable project-state changes.
Gateway/Fabric own finite-retention operational collaboration without overwriting
divergent Git state. The four Core pillars remain
Event Bus, Task Graph, Knowledge Base, and Identity & Permissions. Governance is
optional and must not leak into Core.

```text
Human CLI                         Harness MCP bridges
    \                                 /
     +---- one gatewayd supervisor --+
           | bases + overlays + queues
           | explicit workspace routes
           +---- isolated model-free Code Graph workers
           |
           +---- optional Fabric A / Fabric B / ...
                      |
               Postgres + pgvector per Fabric

Git checkout: .wormhole/state/v1/ <-> checkpoint/review/merge
```

The 2026-07-28 target is authoritative but migration is incomplete. Match the
approved slice plan and tests; do not mistake legacy current code for a new
architectural decision or claim an unimplemented target as shipped.

---

## 4. Module Map and Dependency Rules

| Package | Owns | May import |
|---|---|---|
| `cmd/fabric` | Process wiring: config, HTTP server, registry construction | `internal/core/*`, `internal/mcp`, `internal/storage`, `internal/types`, `internal/webui` |
| `cmd/wormhole` | Human-first CLI entrypoint (`setup`, identity/auth, project/Fabric/connector lifecycle, checkpoint, MCP bridge) | `internal/config`, client-side code, stdlib |
| `internal/config` | CLI global/project TOML configuration | stdlib, BurntSushi TOML |
| `internal/mcp` | MCP tool descriptors, registry, request/response schemas, auth middleware | `internal/core/*`, `internal/types` |
| `internal/core/identity` | Human and agent principals, authenticators, memberships, ownership, sessions, Passports/tokens, whoami, audit trail | `internal/types`, stdlib |
| `internal/core/tasks` | Task graph: CRUD, status machine, task links | `internal/types`, `internal/core/events` (to emit transition events) |
| `internal/core/events` | Channels, append-only event log, typed event payloads | `internal/types`, stdlib |
| `internal/core/kb` | KB articles, links, embeddings, compliance checks, semantic search | `internal/types`, stdlib |
| `internal/core/permissions` | Permission resolution/enforcement helpers | `internal/types`, stdlib |
| `internal/core/git` | Source-code pointers only: commit links and review requests; never repository source | `internal/types`, stdlib |
| `internal/core/roles` | Immutable role templates and default task views | stdlib |
| `internal/storage` | DB connection only (`Open`) | `internal/types`, `lib/pq` |
| `internal/types` | Config and shared plain cross-layer types | stdlib only |
| `internal/types/projectstate` | Canonical snapshot schemas, strict tree/operation codecs, canonical JSON/Markdown digests, validator, and typed reducer | `internal/types`, stdlib, BurntSushi TOML |
| `internal/webui` | Human read projection and approved private-auth browser callbacks/session boundary | `internal/core/*`, stdlib |

### 4.1 Local Runtime Module Map

The local-first Gateway (`gatewayd`) uses `internal/runtime/*` packages, separate from and
parallel to `internal/core/*` (which stays Fabric-only). Local packages follow
the same layering pattern and isolation discipline.

| Package | Owns | May import |
|---|---|---|
| `cmd/gatewayd` | Process wiring: config load, localstore, localapi, sync engine, graceful shutdown | `internal/runtime/*`, `internal/types` |
| `internal/runtime/config` | XDG-compliant paths, immutable workspace bindings, identity refs, and multiple Fabric profiles | `internal/types`, stdlib |
| `internal/runtime/localstore` | SQLite-backed bases, overlays, domain records, queues, conflicts, and checkpoints scoped by project and workspace | `internal/types`, stdlib, modernc SQLite driver |
| `internal/runtime/localapi` | Stable local IPC, project-operation registry, cwd/workspace routing, and actor-envelope resolution | All sibling `internal/runtime/*` packages, `internal/types`, stdlib |
| `internal/runtime/eventbus` | In-memory pub/sub for ephemeral events (presence, heartbeats); never persists | `internal/types`, stdlib |
| `internal/runtime/scheduler` | Agent registration, presence tracking, capability matching, local task routing | `internal/types`, stdlib |
| `internal/runtime/sync` | Explicit per-binding Fabric clients, durable queues, bootstrap/incremental streams, Git-base preconditions, and conflict audit | `internal/runtime/localstore`, `internal/types`, stdlib |
| `internal/runtime/codegraph/config` | Disabled-by-default, workspace-scoped local Code Graph configuration | stdlib |
| `internal/runtime/codegraph/golang` | Read-only Go compiler analysis into the language-neutral graph model | `golang.org/x/tools/go/packages`, stdlib |
| `internal/runtime/codegraph/store` | Component-local SQLite schema, revision payloads, snapshot reads, and atomic publication | `internal/runtime/codegraph/config`, stdlib |
| `internal/runtime/codegraph/index` | Canonical source inventory, compiler-backed candidate construction, invariant validation, and publication | `internal/runtime/codegraph/config`, `internal/runtime/codegraph/golang`, `internal/runtime/codegraph/store`, stdlib |
| `internal/runtime/codegraph/query` | Deterministic lexical and structural retrieval with freshness-gated source access | `internal/runtime/codegraph/config`, `internal/runtime/codegraph/source`, `internal/runtime/codegraph/store`, stdlib |
| `internal/runtime/codegraph/source` | Bounded, hash-validated transient source assembly | stdlib |

**Local runtime hard dependency rules:**

- LR1: `internal/runtime/*` packages never import `internal/core/*` or `internal/mcp`. Local storage and coordination are strictly separated.
- LR2: `internal/runtime/localapi` may import all other `internal/runtime/*` packages (it wires them together). Other runtime packages may not import `localapi`.
- LR3: `internal/runtime/localstore` repository methods enforce project and workspace isolation by construction: every query is scoped through mandatory parameters, never inferred from ambient state. Every change ships cross-project and cross-workspace rejection tests.
- LR4: Ephemeral presence/heartbeats are eventbus-only. Task-transition notifications,
  progress, generic channel activity, runtime attribution, subscriptions, telemetry, and
  uncurated discoveries use the operational activity store, never automatic `EventV1`
  operations. Task definition/owner/portable candidate status may use `OperationV1`.
  Explicit promotion alone strict-binds a source activity ID/digest into portable
  audit evidence.
- LR5: Sync queues are SQLite-backed, restart-surviving, and keyed by explicit Fabric
  instance/project/stream binding. Local writes become durable before sync; one Fabric
  failure never blocks local work or another binding. Nonterminal queues, conflicts,
  recovery state, and receipts are excluded from age/rank pruning. After terminal the
  exact default retention is 30 days; a configured longer value must remain finite.
  Ordinary activity is eligible when it is older than 30 days **or** falls outside the
  newest 10,000 unprotected workspace rows, and is pruned deterministically in
  `(created_at, activity_id)` ascending order. Protected rows may exceed the cap.
- LR6: Code Graph is Gateway-local derivative state. Dependencies are `store` →
  `config`, `index` → `config`/`golang`/`store`, and `query` →
  `config`/`source`/`store`; none imports `localapi`, `sync`, Core, or MCP.
  Stores bind one explicit workspace at `Open`, every payload SQL remains project-,
  workspace-, and revision-scoped, and no Code Graph state enters Fabric or
  `.wormhole/`. Before serving status or query, the runtime compares the active
  analysis fingerprint with a recomputed fingerprint over tracked source and
  adapter-declared non-source inputs plus normalised build/target/configuration,
  graph/adapter schema version, and compiler/toolchain identity. A mismatch
  reports stale state and query fails closed until an explicit successful rebuild.
  Source bytes remain separately hash-validated. Code Graph uses no model,
  embedding, vector query, compute profile, Warpspeed path, or implicit network
  access.

**Hard dependency rules (Coordination Server):**

- R1: `internal/core/*` packages never import `internal/mcp`. Flow is one-way: mcp → core.
- R2: `internal/core/*` packages never import each other, with one sanctioned exception:
  `tasks` → `events`, because task status transitions emit events (RFC-0001 §8.2).
  Need another cross-core import? Escalate; do not add it.
- R3: Parent-package `internal/types` imports nothing outside stdlib and remains the
  bottom of the graph. Its `internal/types/projectstate` subpackage is the one exact
  exception: it may import `internal/types`, stdlib, and BurntSushi TOML. Runtime and
  Fabric code must consume `internal/types/projectstate` rather than duplicate the
  canonical snapshot schemas, strict `DecodeOperation`/`CanonicalOperation` byte
  authority, codec, validator,
  `DigestCanonicalJSON`/`DigestCanonicalMarkdown`, or reducer. `Digest` lives in
  `internal/types/projectstate`, not the parent `internal/types` package.
- R4: No new top-level packages or external Go dependencies without explicit human
  sign-off. Source code directly imports `github.com/BurntSushi/toml`,
  `github.com/lib/pq`, and `modernc.org/sqlite`; the complete locked module graph is
  recorded in `go.mod`/`go.sum`. `golang-migrate` remains external schema tooling rather
  than a linked Go module.
- R5: Each Fabric instance has one datastore: Postgres + pgvector. RFC-0003 separately
  requires Gateway's local SQLite replica and durable sync queue; that SQLite database
  is not a Fabric datastore. Do not add Redis, NATS, another datastore, or
  another storage service without explicit human approval. RFC-0001 §15 decides that
  durable Fabric change discovery is "Postgres table, polled by Gateway". Multiple
  Fabric profiles in one Gateway do not create another Fabric datastore or merge
  namespaces. Harnesses consume local SQLite/runtime state; ephemeral local
  notifications and the in-memory eventbus remain permitted under LR4.

---

## 5. Layering Pattern (follow `internal/core/identity` exactly)

`identity.go` is the reference implementation for every core package. Copy its shape:

1. **Store struct wrapping `*sql.DB`**: `type Store struct { db *sql.DB }` +
   `func NewStore(db *sql.DB) *Store`. No ORM, no query builder — hand-written SQL with
   `$n` placeholders, `QueryRowContext`/`ExecContext`, always `context.Context` first param.
2. **Sentinel errors as package vars**, named `Err...`, message prefixed with the package
   name: `errors.New("identity: invalid token")`. Callers match with `errors.Is`.
3. **Wrapped internal errors**: `fmt.Errorf("identity: <operation>: %w", err)`. Never
   return a bare driver error; never swallow one.
4. **Security-relevant lookups collapse to one error.** Forged, unknown, and
   wrong-project tokens all return `ErrInvalidToken` — callers must not be able to
   distinguish failure modes. This is a retained identity security contract; apply
   the same principle to any future auth-adjacent lookup.
5. **Multi-statement writes use a transaction** with `defer tx.Rollback()` then explicit
   `tx.Commit()`. Single inserts don't need a tx.
6. **Secrets are hashed at rest.** Raw tokens returned exactly once; only SHA-256 hex
   hashes stored. Never log a raw token, never SELECT it (it isn't stored), never add a
   "debug" path that prints one.
7. **JSON columns**: Go `[]string`/structs marshalled to `jsonb`; nil slices normalised to
   empty (`capabilities == nil → []string{}`) before persisting. Unmarshal on read; a
   failed unmarshal is a wrapped error, not a silent default.
8. **Structs are plain data**: exported fields, no behaviour beyond the Store methods.
9. **Doc comments cite the RFC section that motivates non-obvious behaviour**
   (see `ErrInvalidToken`'s comment). Do this only where the RFC constraint is real,
   not on every function.

---

## 6. Database Rules

- D1: Schema changes only via golang-migrate pairs in `migrations/`
  (`NNNNNN_name.up.sql` + `.down.sql`, zero-padded sequential). Down migration must
  actually revert. Never edit an already-committed migration; add a new one.
- D2: Entity shapes come from `docs/db-entities.md`. Deviating from it means updating
  that file in the same change, with the reason.
- D3: Every project-scoped table gets RLS. Global application tables are limited
  to project-agnostic principals/authenticators, agents, and registration
  configuration such as `role_templates`; memberships, ownership grants,
  credentials, and actor actions are project-scoped unless the approved schema
  explicitly records a project-agnostic relationship.
  The `projects` root scopes on its `id`; child tables get a
  `project_id uuid NOT NULL REFERENCES projects(id)` column and an index on it.
  Every scoped table gets `ENABLE ROW LEVEL SECURITY` and a policy comparing
  its scope column (`projects.id` or child `project_id`) to
  `current_setting('wormhole.project_id', true)::uuid`.
  This is the Fabric-tenancy guarantee in RFC-0001 §15; it is not optional per table.
- D4: Conventions already in force: `uuid` PKs via `gen_random_uuid()` (pgcrypto),
  `timestamptz NOT NULL DEFAULT now()` timestamps, `text` not `varchar`, `jsonb` with
  `DEFAULT '[]'` for list-shaped columns, snake_case names, header comment citing the
  RFC section.
- D5: Append-only means no semantic update or in-place correction. Portable accepted
  history remains in Git. Operational activity may be deleted only by the policy-owned
  pruning transaction after eligibility: ephemeral presence is not persisted; ordinary
  activity becomes eligible when older than 30 days **or** outside the newest 10,000
  unprotected workspace rows and is pruned by `(created_at, activity_id)` ascending;
  lifecycle rows are excluded until terminal, then retained for exactly 30 days by
  default or a configured longer finite duration. Protected rows may make the cap
  exceed. No caller or generic CRUD path may update/delete append-only evidence.
- D6: KB embeddings live in Fabric's Postgres pgvector datastore, in the
  project-scoped `kb_article_embeddings` generation table; an approved remote
  provider may compute vectors but is never the vector datastore. The legacy
  nullable `kb_articles.embedding` column is compatibility-only and must not be
  used for production ranking or new writes.
- D7: D1 governs Fabric's Postgres schema. The currently implemented legacy
  Code Graph schema is migration input, not the final per-workspace worker
  schema. Its tables use their
  own `codegraph_schema_migrations` SQLite ledger, fail closed on a schema newer
  than the binary, and never enter the Fabric migration sequence. They may store
  paths, indexed hashes, signatures, ranges, edges, and diagnostics, but never
  complete source files, function bodies, or returned context packages. Schema
  version 2 adds only `codegraph_lifecycle`, a project-scoped cross-handle
  build/disable lease with PID plus process-start identity for safe startup
  recovery arbitration. Startup recovery holds a SQLite `BEGIN IMMEDIATE`
  writer barrier across lifecycle inspection and cleanup; ordinary build
  admission may reclaim only an exactly matched, positively dead owner, while
  uncertain liveness fails closed. Completed disablement removes its project row
  together with all derivative graph/configuration rows. The target migration
  must key store identity and lifecycle by workspace/checkout before claiming
  worker isolation. Code Graph lexical
  retrieval may index identifiers, signatures, paths, and documentation terms,
  but it never downloads or invokes a model, stores vectors, or treats enabling
  the graph as network consent. KB embeddings under D6 are a separate Fabric
  capability.

---

## 7. MCP Surface Rules

- M1: MCP tool names and schemas are governed by the checked-in alpha contract
  inventory. The 2026-07-28 migration may intentionally revise that inventory in
  its approved slice, with compatibility tests and documentation changed together.
  Outside such a slice, do not invent or silently drift a tool contract.
- M2: Naming grammar is `wormhole.<namespace-noun>.<verb>`. Core pillar namespaces
  are `agent`, `channel`, `task`, `kb`, and `git`; RFC-0003 additionally ratifies
  `sync` for Gateway-to-Fabric operations, `workspace` for Gateway-local status,
  diff, import, checkpoint, and stash operations, and `code_graph` for
  Gateway-local derivative status, query, and rebuild operations. `workspace`
  and `code_graph` are not Core pillars. Their contract-inventory changes ship
  atomically with the approved migration slice. No other namespace prefix may be
  added without an RFC change; `wormhole.governance.*` is governed by optional
  RFC-0002 and remains out of Core.
- M3: Every ordinary project operation available through the human CLI has an
  equivalent agent MCP operation over the same Gateway domain semantics. Human
  authentication/recovery, ownership transfer, membership/policy administration,
  service installation, and connector configuration are control-plane exceptions,
  not a second project-write model. Do not add an unrelated REST-only project API.
- M4: Fabric authentication is resolved at its boundary: private requests resolve
  the human/agent credential, membership, ownership, and assurance into an actor
  scope before Core executes; public requests resolve key continuity and label it
  identification-only. Core packages never re-parse raw credentials. Local Gateway
  operations carry a typed actor envelope and do not fabricate private assurance.
- M5: Every authenticated capability-gated tool declares `Tool.RequiredPermission`.
  Current values match the tool name without the `wormhole.` prefix (for example,
  `task.create`, `channel.post`, and `kb.write`). Deliberate auth-only exceptions declare
  an empty permission. When adding a tool, update the registry invariant, role-template
  migration, and permission documentation together.
- M6: Authentication recovery, ownership transfer, membership changes, deleting a
  project, revoking all access, and changing policy are human control-plane actions
  by default. This must not be used to deny agents ordinary project operations or
  Git-proposable `.wormhole/` changes.

---

## 8. Pillar-Specific Constraints

### Events / Channels
- Operational activity is typed first: `event_type` from the RFC vocabulary
  (`task.status_changed`, `review.requested`, `build.failed`, `discovery.logged`,
  `message.posted`), typed `payload` jsonb per type, optional free-text `note`.
  `message.posted` is the escape hatch; do not add prose-first event types.
- New event types are an escalation, not a local decision.
- Generic channel posts/status, progress, and task transition notifications are
  `ActivityV1`, not portable `EventV1`. Explicit promotion creates the latter with exact
  extension key `dev.wormhole.promotion` and schema-version-1 data containing only
  `source_activity_id` and `source_activity_digest`.
- Promotion accepts only a complete promotable-event projection. `EventV1` channel,
  source actor, type, payload, note, and creation time are exact deep-owned source copies;
  `OperationV1.Actor` is the distinct promoter. Caller-selected semantics, attribution,
  or extra extensions reject the operation.
- Durable Fabric change discovery uses Postgres-backed polling by Gateway
  (RFC-0001 §15). Harnesses consume local SQLite/runtime state over
  MCP IPC. Ephemeral local notifications and the in-memory eventbus are
  permitted for wake-ups, presence, and heartbeats, but never as a second
  durable coordination datastore. Do not add server-side push/streaming
  infrastructure or another durable delivery service.

### Tasks
- Hierarchy is Project → Task → Subtask via `parent_task_id`. Status enum exactly
  `todo / wip / blocked / done`. Transitions go through a validated state machine and
  atomically update portable task state and append operational `task.status_changed`
  activity through the Task-7-gated seam. They never automatically create `EventV1`.
- Links to KB articles / commits / PRs / events go through `task_links`, not ad hoc
  columns.

### Knowledge Base
- Atomic articles: one fact/decision/procedure each. Markdown body + jsonb frontmatter.
- Compliance uses soft rejection with structured rewrite guidance under
  RFC-0001 §15. Fabric revalidates remote writes rather than trusting a client;
  local Git-only operation must not depend on Fabric availability or remote
  embeddings. Deterministic local checks and any pending remote-only semantic
  advice must remain distinguishable. Thresholds are tunable configuration,
  not hardcoded architecture.
- Linking via `kb_links` rows (graph), never folder/path hierarchy.
- The current Fabric KB contract supports semantic pgvector search, distinct
  from model-free Code Graph retrieval. KB reads are strictly project-scoped
  under RFC-0001 §15; a multi-project runtime never constructs an implicit
  merged KB. Changing semantic-search requirements needs a focused contract.

### Identity
- Human and agent principals are distinct and durable. Authenticators, project
  memberships, agent ownership/sponsorship, sessions, Passports, and credentials
  are separate records; do not collapse them into one token row.
- Agent identity remains project-agnostic; do not add `project_id` to `agents`.
  Local/fork operations require no Fabric grant, canonical-public participation
  uses identification-only key continuity, and private Fabric access uses project
  membership plus accountable ownership-bound grants.
- Passport is a project-scoped Fabric capability grant to an agent, not a human
  identity, local actor record, or Git credential. Legacy uniqueness constraints
  may be migrated only through an approved plan preserving history.
- Every action is attributable through a typed actor envelope. Agent actions record
  agent, accountable human at action time, harness/model session, and assurance.
  Audit rows are append-only and server/Gateway generated, not client-trusted.

### Git integration
- Source integration remains pointers only (`git_links`, commit SHA, PR URL,
  summary); Wormhole never stores or mirrors code bodies. Separately, typed
  Wormhole project records live under `.wormhole/state/v1/` and use a private
  Gateway overlay plus deterministic checkpoint. Fabric may inspect Git refs and
  the `.wormhole/` subtree for canonical acceptance but must not ingest or execute
  repository source. Canonical-public activation requires `origin` to match the
  canonical repository identity. A fork/mismatch leaves the upstream hint inactive,
  makes no upstream Fabric contact/read/write, and remains local or binds an
  independent realm. Worktree isolation is mandatory.
- Checkpoint publication must compare-and-swap the expected live `.wormhole/`
  digest so direct edits cannot be overwritten. It records materialised-pending-
  commit state privately. Before staging, one existing prepared/published/recovered-new
  journal returns `ErrCheckpointPendingAcceptance`; mixed/multiple state fails closed.
  Resolve an owner-only Git-private checkpoint directory outside the portable worktree
  through hardened `git rev-parse --git-path wormhole/checkpoints` and require it to be on
  the live tree's device. Allocate fresh no-replace stage/backup names but create only stage
  before the CAS. Any pre-journal stage is unowned, never worktree-visible, ignored by
  Recover, and never reused; cleanup is deferred.
- Every new prepared checkpoint carries proof version 1 plus non-null strict canonical
  publication-review and prior-candidate JSON. The latter contains complete inline direct
  and optional rebased candidate trees; it never aliases journal `prior_tree`. Migration
  version 4 enforces the joint version-0/null/null or version-1/non-null/non-null shape.
  Each direct or rebased inline prior-candidate tree is independently limited to at most
  `10_000` files, at most `4 << 10` UTF-8 bytes per path, at most `16 << 20` bytes per file
  body, and at most `64 << 20` total bytes, counted as the sum of every path byte plus every
  file-data byte. There is no combined direct-plus-rebased aggregate limit and no raw-JSON
  byte limit in v1. Filesystem-only directory-count and depth limits do not apply to the
  serialized proof; canonical `DecodeTree` still rejects unknown or unsafe project-state
  paths.
  After prepared commit, checkpoint opens a second `BEGIN IMMEDIATE`, rechecks the exact
  live digest, binding, both proofs, candidate, overlay rows, review, and conflict gate,
  and holds it across publication, ordered parent fsyncs, and journal/candidate/row update.
  Task-5 V1 no-replace renames live to absent backup, fsyncs the private destination before
  the checkout source, reclassifies, then renames stage to absent live and fsyncs the
  checkout destination before the private source. It writes no database postimage before
  durable publication. Exchange and Darwin Task-5 support are deferred.
- Checkpoint and Recover never advance the accepted binding and expose no base-advanced
  result flag. Same-symbolic-ref Reject/Refresh alone accepts a checkpoint materialization;
  Task-4 proposal-free ref switch and applicable Discard may separately advance the base.
  Task-5 indeterminate writes use exact transition-relative prior/next journal confirmation
  without replay; attempted prepare treats journal absence as exact prior and retains every
  unconfirmed unjournaled stage.
- Recover runs its recovery-specific disposition proof in one
  `BEGIN IMMEDIATE` snapshot before Git/path I/O. Empty or accepted/recovered-old-only
  history, and separately one proved recovered-new, compose and return DB status from that
  snapshot with no Git/path I/O. Exactly one prepared/published row drives recovery;
  mixed/multiple pending state, no journal with materialized rows, or cross-journal/orphan/
  partial ownership fails first. Prepared requires exact prior
  candidate plus recorded active/rebased operations and zero owned materialized rows;
  published/recovered-new require the exact publication postimage and every claim
  materialized. Accepted history must pass complete historical ownership (version 0 only
  without residual materialized rows); recovered-old owns none.
- For a recovery driver, retain that writer transaction while observing position -> full
  tree at SHA -> origin -> final position once, re-prove the disposition, classify or
  mutate journal-bound paths, write and reread the selected database outcome, and commit.
  Stored base exact or same-ref different-commit exact candidate may converge, without an
  ancestry assumption. Any malformed/racing/drifted/other base fails with the recovery
  precondition sentinel before origin invalidation. Recover-old restores the exact inline
  candidate/operation preimage; recover-new retains the exact publication postimage.
- Before stage/backup evidence I/O, re-resolve and no-follow open the owner-only same-device
  Git-private root. Stored absolute paths must be distinct direct children named
  `<journal_id>.stage`/`<journal_id>.backup`; inspect them descriptor-relatively without
  following links and revalidate stable identity before mutation. Root/path containment,
  naming, device, or rebind failure is a recovery precondition error; contained
  symlink/type/existence/digest/identity mismatch is recovery-blocked. Mutate no path.
- Snapshot deletion is canonical: single-file records become tombstones at their
  stable path; a KB tombstone replaces `record.json` and requires `body.md` to be
  absent. Missing targets are broken, while only schema-declared historical
  references may resolve to tombstones. Tombstone/edit and tombstone/KB-body
  changes conflict; resurrection must explicitly name the tombstone digest. Merge
  lifecycle equality is byte-exact. From an old tombstone, exact endpoints coalesce
  and one unchanged endpoint yields to the other; only divergent non-base endpoints
  conflict. From an old live mutable record, exact dual tombstones coalesce and unequal
  dual tombstones conflict. KB root and `/body` evidence are independent.
- Portable Events and Git links are both live-only, immutable, and add-only. An
  exact canonical same-ID replay coalesces; any unequal same-ID value uses the
  generic immutable-record error, conflict, and direct-delta sentinel. Neither kind
  may be tombstoned or resurrected. `ErrImmutableEvent` and a Git-link-specific name
  may remain compatibility aliases to `ErrImmutableRecord`, but they are not distinct
  normative behaviours. An old-live immutable record is clean only when both endpoints
  still equal the old record. Valid disappearance produces immutable-record evidence;
  a side made reference-invalid by disappearance returns its typed validation error.
  After old-base validation, raw mutable disappearance is preflighted in new-base then
  candidate order before either side validates and is invalid unless its stable path
  contains the valid typed tombstone.
- For an existing live mutable record, `created_at` is immutable on ordinary update
  through the reducer, direct import, and rebase. An explicit digest-proven
  resurrection may carry a fresh valid `created_at` because tombstones retain content
  digests, not prior record bytes. Exact lifecycle endpoint comparisons include
  `updated_at`. For old-live/live-live semantic resolution, take the only semantic
  editor's timestamp, the later UTC timestamp when both sides merge cleanly, or the
  old-base timestamp when neither side changed semantics. For an old-live tombstone
  racing a live endpoint, a timestamp-only live difference is not a semantic edit, so
  the tombstone wins without timestamp selection. A timestamp never selects or
  suppresses a semantic change.
- Snapshot version, project ID, and repository identity are immutable binding
  invariants on every accepted/candidate/composed/rebased snapshot. `Config.Handle`
  and `Remotes` are Git-base-owned: operations and semantic diff exclude them; rebase
  requires the candidate values to equal the old base and takes the new base values.
  Candidate loading after a Git advance validates the binding invariants but must not
  reject retained old-base handle/remotes merely because the accepted base is newer.
- Candidate `ImportedBy` validation is the exact union canonical UUID or
  `system:git-observation-rebase-v1`; `ImportedAt` is a valid UTC timestamp. Candidate,
  stash, and retry reads apply the same union. A trusted zero-actor same-ref Git rebase
  preserves both fields exactly when a candidate exists. Without one, it uses only the
  fixed token and the single UTC transaction observation time captured after
  in-transaction Git reobservation and before the first write. It never borrows an
  operation actor or fabricates an ActorEnvelope or UUID.
  Apply this predicate to candidate read/write validation in
  `localstore/workspace_repo.go`, retry loading in
  `localstore/workspace_restore_retry_repo.go`, and ProjectState validation/projection in
  `restore_plan.go`, `restore_retry.go`, and `stash_plan.go:validateStashCandidate`.
  Retain the UUID retry golden and add a separate literal system-token retry preimage with
  a hard-coded digest; production code must not generate either expected digest.
- Direct-delta validation compares only the prior direct surface, the next direct tree,
  and a bound materialisation exception. It accepts a correct new tombstone, rejects a
  changed prior tombstone, and never inspects an overlay. `ThreeWayRebase` alone owns
  the overlay-versus-direct tombstone/edit conflict and its persisted evidence.

### Portable state replay, diff, and merge

- Task 7/domain projection is hard-blocked until a focused approved plan freezes
  `000005_workspace_activity.sql`, strict `ActivityV1`, finite effective-policy storage,
  atomic terminal/pruning rules, and the promotion receipt seam. Promotion must use one
  ProjectState-owned immediate transaction to strict-read an exact source activity ID and
  digest, append an attributed `EventV1` `OperationV1` with extension key
  `dev.wormhole.promotion` and schema-version-1 data containing only
  `source_activity_id` and `source_activity_digest`, and atomically mark/receipt that
  source. It must not nest `ApplyBatch`.
- Trusted machine-private setup/workspace state classifies publication as exactly
  `unclassified`, `local_only`, `public_git`, or `private_git`, independently of
  canonical/fork routing and Fabric mode. A public fork is `public_git`. A caller, actor
  assurance, or copied remote hint never selects classification. Unclassified workspaces
  permit status/diff but not checkpoint.
  Status/diff bind exact workspace, repository, credential-free semantic origin,
  classification plus monotonic policy revision, candidate tree, canonical semantic diff,
  and independently observed Git/base inputs into the publication-review digest. `public_git`
  checkpoint accepts that exact digest (not a boolean), rechecks it before staging, and
  persists the exact acknowledging actor/digest in the prepared journal/receipt. CLI and
  MCP are equivalent.
- Classification is explicit user policy, not continuous host-visibility evidence. Bind
  it to the exact workspace/repository/semantic-origin identity. After first configuration,
  a stable observed identity/origin change atomically and stickily transitions the current
  row plus append-only history to `unclassified` at revision+1; returning to the old origin
  never revives the old digest. Surface the effective class on status/diff and require an
  explicit human `ValidateLocalAction` reconfiguration for a same-identity visibility
  change; never add an implicit network visibility probe. The exact schema, origin codec,
  diff/review goldens, checkpoint/recovery CAS, and zero-domain-mutation rules are frozen in
  `docs/superpowers/specs/2026-08-01-publication-classification-review-cas-amendment.md`.

- Persisted operation JSON is untrusted. Decode it only with the shared strict
  `projectstate.DecodeOperation`, reject non-canonical bytes, trailing JSON, unknown
  fields, malformed envelopes/payloads, invalid IDs/digests, and any row-ID/operation-ID
  mismatch, require byte equality with `projectstate.CanonicalOperation`, then replay
  with the shared reducer. Any recorded operation digest must equal
  `projectstate.DigestCanonicalJSON(decoded)` before serving or replay. Persisted trees
  likewise require the strict file-list decoder, `DecodeTree`, canonical re-encoding,
  recorded-digest checks, and the complete binding predicate. There is no legacy-table
  or malformed-row fallback.
- Composition receives an explicit strict-decoded start snapshot, an explicit initial
  through-generation, and strictly increasing active stored operations whose generation
  is greater than that boundary. Rebased, stashed, materialized, and discarded rows do
  not replay.
  Status exposes the exact composed `CandidateDigest` and final `OverlayGeneration`
  while retaining the accepted snapshot separately. `WorkspaceStatus.State` remains a
  string until an approved plan introduces another type.
- Stash serialisation keeps `source_tree`/`source_base_digest` as the semantic pre-stash
  rebase base. The existing `operations_json` column is a strict canonical
  `StashReplayV1` containing schema version, selected-start tree/digest, initial boundary,
  a non-nil absorbed-rebased prefix array, and a separate non-nil active suffix array.
  Inside the same immediate transaction, Stash reads one non-nil complete
  `OperationAudit` of `WorkspaceOperationAuditRecord` values containing every
  exact-workspace operation in stable increasing-generation order across active, rebased,
  materialized, stashed, and discarded states. Each record embeds its exact
  `WorkspaceOperation` and retains `CreatedAt` for `RestoreRetryState`. The reader strictly
  validates every row's positive generation, globally unique canonical operation ID,
  canonical operation bytes, state, stash-owner metadata, and timestamp. Stash maps every
  embedded operation in returned order into a non-nil `[]WorkspaceOperation`, preserving
  count and performing no filtering or omission, then passes only that operation inventory
  to `buildStashPlan`; `CreatedAt` is not a planner input. The planner
  derives all ownerless rebased rows at/below the selected boundary and all ownerless
  active rows above it, rejects active rows at/below the boundary and rebased rows above
  it, and validates but ignores terminal materialized/stashed/discarded rows. Only the
  two exact cloned memberships returned by the planner may be passed to
  `TransitionOperations`; filtered stash readers and generation-range updates are
  forbidden.
  All and only rows attributed by `stashed_by_stash_id` must match those arrays byte for
  byte. Portable transitions own Gateway
  migration `000002_portable_transitions.sql` and `GatewaySchemaVersion = 2`; committed
  `000001`/`000002` are immutable. The reviewed publication amendment owns
  `000003_workspace_publication.sql`; Task 5 owns
  `000004_checkpoint_publication_review.sql`, whose three-column v4 change adds
  `publication_review_json`, `prior_candidate_json`, and
  `publication_review_proof_version` with the exact joint version-0/null/null versus
  version-1/non-null/non-null CHECK. Mandatory portable-plan Task 6A owns
  append-only `000005_workspace_activity.sql` after its focused operational
  activity/retention/promotion artifact is reviewed and explicitly approved. Task 7 and
  migration 6 are blocked until the reviewed v5 implementation commit lands. Multi-Fabric
  routing/sync own `000006`/`000007`; the later, separately gated Code Graph branch
  consumes that schema and owns `000008_invalidate_legacy_codegraph.sql`. Restore must
  prove `Compose(selectedStart,boundary,operations)` equals the
  recorded composed tree, then call `ThreeWayRebase(sourceBase,current,stashComposed)`.
  Already-rebased rows at/below the boundary and later active rows move to terminal
  owner-attributed stashed state in their respective envelope arrays. Stash rejects any open
  exact-workspace conflict with `localstore.ErrWorkspaceConflicted` and zero mutation;
  successful stash sets status `clean`.
- Only a clean stash restore may persist a candidate, transition absorbed current-active
  operation rows, delete the stash, and set status `pending`; original stash rows remain
  terminal stashed. A conflicted restore leaves
  the candidate, every operation row, and full stash byte-identical; it persists only
  deterministic conflict evidence and status `conflicted`. Before writing those allowed
  fields it captures the protected state, including every non-status binding field,
  binding `created_at`/`updated_at`, and the accepted snapshot. Afterward it rereads the
  complete state. The accepted snapshot, all non-status binding fields, binding
  `created_at`, every candidate field/blob, every operation logical field plus
  `created_at`, and every stash field/blob/envelope/actor plus `created_at` must be
  unchanged; only the exact
  status/`updated_at` mutation produced by `SetStatus("conflicted")` and deterministic
  open-conflict replacement may differ. The post-state must contain that exact status,
  timestamp, and evidence. It rejects every other change and
  stores a canonical retry digest over explicit restore/conflicted action/outcome, scope,
  request ID/digest, stash ID, and that post-state, including both binding timestamps, in
  the same transaction. Exact
  repeat strict-composes current and replay rows, recomputes semantic evidence and that
  digest, and returns the same public result with zero writes only when both binding
  timestamps and every other committed field still match; a same-status rewrite of
  `updated_at` therefore fails closed. Localstore supplies explicit exact-byte digests for
  the raw accepted-snapshot, candidate direct/rebased, and stash source/composed canonical
  file-list BLOBs while those bytes are available. Each digest is `"sha256:" +
  lowerhex(SHA256(raw))` over the complete raw BLOB with no canonicalization, framing,
  prefix, or separator in the hash input, after strict decode and byte-equal canonical
  re-encoding. Runtime copies these fields and never re-encodes decoded trees to recreate
  them. Literal-byte golden vectors freeze every BLOB digest;
  corruption or drift fails closed. The digest cryptographically commits to equality at
  the retry transaction's linearization point, not to absence of an intermediate
  mutation-and-reversal. The retained stash is resolution/audit evidence, not a blind
  replay instruction.
- Stash, restore, and discard require canonical UUID request IDs and immutable canonical
  transition receipts. Each request digest is canonical JSON of a dedicated tagged v1
  projection binding schema/action/scope, the complete strict canonical actor envelope,
  and label or stash ID; discard additionally binds a dedicated tagged projection of the
  complete private adapter-supplied resolved expected binding, canonical root, and expected commit. Golden
  tests freeze all three preimages and digests. Stash, restore, and discard use explicit
  action-specific private tagged v1 codecs: `stashResultV1`/`stashReceiptV1`,
  `restoreStashResultV1`/`restoreStashReceiptV1`, and
  `discardResultV1`/`discardReceiptV1`. Each receipt contains schema version, action,
  outcome, and result; restore alone additionally carries a
  retry digest that is nil exactly for clean and non-nil exactly for conflicted.
  Action/outcome must equal the receipt row. A transition receipt's logical key is the
  exact textual project/workspace/request triple. For detection only, readers CAST-match
  all three persisted key columns so a BLOB alias cannot hide: a present key requires
  exactly one match, exact raw key values, and TEXT storage for every selected column;
  zero matches must satisfy the strict all-null absence shape, and multiple matches are
  corruption. `InsertTransitionReceipt` strict-preflights that same logical key inside
  its caller-owned `BEGIN IMMEDIATE`, so it cannot insert beside a hidden alias. This
  hardening uses the existing table and requires no schema change. Localstore treats
  `result_json` as
  action-opaque: it requires exactly one valid compact JSON value followed by exactly one
  LF, preserves those bytes and object-member order, and never schema-decodes or
  generic-map re-encodes the value. The action-specific ProjectState codec owns schema,
  tags, member order, exact re-encoding, and semantic canonicality and rejects any
  noncanonical bytes. Conflict arrays are always non-nil (`[]`, never `null`), optional
  pointers are emitted as explicit `null`, and no digest-bearing field uses `omitempty`.
  Fixed golden bytes and hard-coded canonical-JSON digests cover stash, clean/conflicted
  restore, and discard result/receipt encodings. They use the existing TEXT column and
  require no schema version or migration change. Same ID and digest is a retry; a
  different digest returns `ErrIdempotencyConflict`. Clean retries use receipts read-only,
  while conflicted restore recomputes current/stash/evidence and retry digest before
  matching. An unknown clean
  commit may be proved by an exact receipt; an unknown conflicted commit must pass the same
  retry-state verification or remain `ErrCommitOutcomeUnknown`. Retry uses the same
  request or operation ID and reads back the receipt before attempting an insert.
  BranchSwitchDiscard is stricter. Its outside preflight uses exactly
  `WorkspaceRepo.TransitionReceiptByKey(ctx, scope, requestID)`. That method syntactically
  validates scope/request ID and queries only `workspace_transition_receipts`; it never
  queries `workspace_bindings`. It CAST-matches the textual triple, requires exact raw
  TEXT keys/storage, and selects `COUNT(rowid) OVER()` plus exact columns/types.
  `sql.ErrNoRows` returns nil, count one returns one fully strict record, and every other
  returned count is corruption/ambiguity. This read follows pure validation/digest computation
  and precedes every filesystem, Git, and current-binding check. An exact receipt returns
  read-only with zero Git calls/writes; another action/digest is
  `ErrIdempotencyConflict`; corruption or ambiguity fails closed.
  Discard then uses exactly `WorkspaceRepo.WithImmediateWorkspaceTransition` with
  `(ctx, scope, requestID, fn)`. It syntactically validates inputs, begins
  `BEGIN IMMEDIATE`, and makes the same table-only receipt lookup its first SQL read. It
  passes the receipt and bound-scope transaction to `fn`; a concurrent exact receipt wins
  read-only, and only nil permits `fn` to call `tx.Workspace` or another state method. Existing
  `WithImmediateWorkspace` and `TransitionReceipt` semantics remain unchanged for Stash
  and RestoreStash.
- Persisted conflicts have UUIDv4 occurrence IDs separate from recomputed deterministic
  semantic conflict IDs. One open semantic ID per exact workspace is enforced; repeated
  evidence reuses its occurrence, absent evidence resolves it, and reopening allocates a
  new occurrence. Strict reads validate canonical envelopes, typed roots, recomputed IDs,
  ordering, and row metadata before serving or mutation.
- Import performs a canonical no-follow capture before `BEGIN IMMEDIATE`, then an exact
  second read under the writer barrier immediately before its first database mutation;
  every retained filesystem/localstore reader result is deep-cloned immediately. After
  second-capture byte/digest equality it revalidates canonical root and checkout identity
  immediately before conflict replacement, its first write, so a byte-identical checkout
  replacement still fails on changed device/inode. Before replacement it requires exact
  consistency between pre-existing open-conflict evidence and workspace `conflicted`
  status; either mismatch fails closed.
  Raw path deletion is classified in canonical kind/UUID order before next-tree decode.
  `ValidateDirectDelta(prior,next)` accepts no proof; only a repository-owned exact
  published or recovered-new materialization match may bypass it. The matcher receives
  the complete disposition proof, eligible row, binding, canonical prior tree, captured
  candidate tree, and captured digest; no input is inferred or omitted.
- Import, ObserveGitBase, and Discard read one complete same-transaction materialization
  disposition before an eligible match and use the acceptance-specific proof. Accepted,
  published, and recovered-new journals own materialized rows; prepared is rejected by
  that proof and drives Recover; recovered-old owns none. Claims are globally unique by
  generation and operation ID and form an exact byte/state/ownerless bijection with all
  materialized rows. Nil legacy
  accepted history contributes no claim and is valid only without a dependent residual
  materialized row. Each owning journal also rejects any unclaimed active/rebased row at
  or below its boundary; stashed/discarded gaps and later active rows are allowed.
  Historical prepublication state comes from the exact checkpoint transition and durable
  envelope, not an independently reconstructable later column. Recover does not reuse this
  acceptance proof: its recovery-specific proof admits exactly one prepared only when the
  current candidate equals the prior-candidate preimage, every listed row remains in its
  recorded active/rebased state, and no owned materialized row exists; exactly one
  published requires the publication postimage and all claims materialized. Import also
  uses the disposition operations as its sole
  replay/transition inventory: active at/below the selected boundary or rebased above it
  is corruption; all valid active rows are composed and then passed as exact preloaded
  membership to `TransitionOperations`. `ActiveOperationsAfter` and generation-range
  updates are forbidden for this mutation.
  Import has no request ID or receipt: indeterminate COMMIT always returns
  `ImportResult{}` plus `ErrCommitOutcomeUnknown`, and retry recomputes from fresh capture
  and transaction state rather than inferring an original result from database readback.
  Its `ImportedChangeCount` is exactly
  `len(SemanticDiff(priorSurface, liveSnapshot, nil).Changes)`: overlay changes, merge
  results, and materialization-exception status never alter that direct semantic count.
- ObserveGitBase supports Reject and Discard only. Reject and the Discard no-receipt path
  perform a full outside observation. Inside one immediate transaction, continued
  Discard absence or Reject requires the complete current binding to equal the valid
  private adapter-supplied `ExpectedBinding`, with equal scope and matching root; preloads
  complete candidate, operation, conflict, accepted, and materialization state; and
  reobserves checkout identity, symbolic ref, and HEAD before the first write. This is the
  linearization boundary; a same-SHA symbolic-ref change counts. Before applicability,
  strict-prove and classify the complete disposition. Prepared, orphan/unclaimed or
  nonterminal materialized, corrupt/ambiguous terminal ownership, and every incomplete
  proof fail first as recovery/corruption blockers. Historical accepted/recovered-old
  rows are nonblocking only after their proof passes.
- Discard is applicable only to an actual symbolic-ref change plus at least one candidate,
  exact active/rebased proposal row, or open conflict occurrence. Otherwise it returns
  `ObserveGitBaseResult{}` plus `ErrBranchSwitchDiscardNotApplicable`, with no mutation or
  receipt; an exact receipt retry still wins first. Reject may advance a ref switch with no
  proposal normally. Same-ref changes use normal acceptance/rebase under Reject. Only
  stashed rows, only discarded rows, only accepted-journal historical materialized rows,
  or only resolved conflict history is terminal history and is not proposal state.
  Under Reject, any symbolic-ref change with proposal always returns
  `ErrBranchSwitchPending` and cannot accept a materialization. Discard instead proceeds
  through its proved applicability and materialization-match rules.
  Applicable Discard keeps the frozen write order and exact affected-count checks: mark
  preloaded active/rebased rows discarded, preserve stashed and accepted-journal
  materialized rows, delete candidate, resolve open conflicts, insert the receipt, and
  advance the base. Any failure rolls back all of them.
- Only same-symbolic-ref Reject/Refresh may accept an exact proved `published` or
  `recovered_new`
  materialization match. Discard never accepts it: exact match returns
  `ObserveGitBaseResult{}` plus `ErrBranchSwitchDiscardNotApplicable`, with no receipt or
  mutation and byte-identical journal, candidate, materialized rows, conflicts,
  operations, and binding. A nonmatching proved acceptance-eligible row blocks same-ref
  Reject rebase and applicable Discard with `ErrGitMaterializationPrecondition`, retaining
  those bytes. Prepared still requires Recover; historical accepted/recovered-old rows do
  not block. Do not add a journal state or migration. The clean-discard codec remains
  candidate-not-accepted with nil journal, no rebase, and no conflicts.
- Unknown applicable-Discard COMMIT confirmation reads a fresh exact receipt without Git
  and succeeds only when its strict action, digest, and complete result equal the attempted
  transition. Otherwise it returns `ObserveGitBaseResult{}` plus the original error
  wrapping `ErrCommitOutcomeUnknown`; mismatch is not `ErrIdempotencyConflict`.
  Non-discard ObserveGitBase returns the same zero result and sentinel on unknown COMMIT,
  with no receipt or state inference. RefreshWorkspace injects its validated resolved
  binding as `ExpectedBinding`, but returns `types.WorkspaceBinding{}` on that error;
  private adapters do the same, and no CLI/MCP client supplies binding or routing context.
- At most one prepared, published, or recovered-new checkpoint exists per workspace.
  Checkpoint checks inside its first immediate transaction and returns
  `ErrCheckpointPendingAcceptance` with zero artifact allocation or mutation while one
  remains; a prepared row must be converged through Recover. Mixed/multiple pending rows
  are corruption. Later active generations remain overlay until recovery/acceptance;
  checkpoints are never silently superseded.
- All append/rebase mutations use one caller-owned dedicated SQLite connection and one
  `BEGIN IMMEDIATE`: read exact binding/candidate/active rows, decode and compose, apply
  the shared reducer, write all rows/candidate/conflict state and the binding status,
  then commit or roll back together. Helpers must not open nested transactions. A
  standalone append API that bypasses composition, status, generation, or candidate
  validation is forbidden.
- `localstore.WorkspaceConflictGate` has exactly
  `HasOpenConflicts(context.Context, types.WorkspaceScope) (bool, error)`;
  `WorkspaceRepo.HasOpenConflicts` uses that signature and
  `WorkspaceMutationTx.HasOpenConflicts(context.Context) (bool, error)` is restricted to
  its callback's bound scope. Both query exact project/workspace plus `state='open'`;
  resolved or other-workspace rows do not block. The sole sentinel is
  `localstore.ErrWorkspaceConflicted`; no runtime alias or alternate declaration is
  permitted.
- Diff and conflict paths use RFC 6901 escaping, `""` for a record root, and `/body`
  for KB Markdown. `FieldValue` has `Present bool` tagged `json:"present"` and
  `Value json.RawMessage` tagged `json:"value,omitempty"`: false requires nil Value;
  true requires exactly one canonical JSON value, including literal `null`. Complete
  root values use the concrete typed record's canonical schema field order. Generic
  sorted-map JSON exists only transiently while recursively merging object members;
  roots are rehydrated through their strict concrete type before assignment, evidence
  comparison, or exposure. Persisted conflict reads must perform the same rehydration
  because canonical encoding of a `json.RawMessage` may normalize root member order.
  Arrays are atomic. Diff attribution comes from the last applied active operation
  affecting the record key. Conflict IDs are the shared canonical `sha256:<lowerhex>`
  digest of a versioned key/path/kind/base/ours/theirs tuple, and conflicts sort by
  entity kind, record ID, field path, conflict kind, then ID.
- Markdown merge canonicalizes LF, computes deterministic minimum-edit old-base LCS
  hunks, prefers the base/deletion-side step for equal-cost choices, and orders hunks
  by base start, base end, and inserted bytes. Non-overlapping hunks merge; identical
  same-anchor insertions coalesce; unequal same-anchor insertions and overlapping
  replacement/deletion hunks conflict. Never put conflict markers in a candidate.
- A conflicted three-way rebase returns the complete validated prior composed candidate
  byte-identically, never a partial merge. Evidence orientation is always
  `Base=oldBase`, `Ours=candidate`, and `Theirs=newBase`. The importer atomically persists
  that `ours` surface with the complete new direct `theirs` tree, both-side evidence,
  absorbed generation/row states, and conflicted status. Restart reproduces it.
  Checkpoint returns zero `CheckpointResult` plus
  `localstore.ErrWorkspaceConflicted`; callers use
  Status separately to prove preserved candidate digest/generation. Writable v2 Fabric
  push checks the same exact-scope gate before credential resolution, client/DNS/signing,
  or network. Delivery marking rechecks inside one local `BEGIN IMMEDIATE` immediately
  before the complete-key queue update. No transaction spans the network: an in-flight
  conflict may follow remote acceptance, in which case the byte-identical row stays
  pending and retries the same operation after resolution. Checkpoint and writable Fabric
  delivery remain blocked until explicit resolution.

---

## 9. Testing Rules

- T1: Follow `internal/core/identity/identity_test.go`: DB-backed tests against real
  Postgres (docker-compose service), not mocks of `*sql.DB`.
- T2: Every core package change ships tests covering: the happy path, each sentinel
  error, and the security property the package guards (isolation, forgery,
  scope preservation — whatever applies).
- T3: RLS and project isolation get explicit cross-project rejection tests whenever a
  new project-scoped table or query lands. Gateway workspace changes also require
  cross-workspace, cross-Fabric, and fork/upstream rejection tests as applicable.
- T4: Run focused tests first. Do not claim an implementation slice done without
  `make check` passing and its output observed. Before a milestone handoff or
  release claim, also run the repository's release test and rehearsal gates.
- T5: Merged statement coverage must remain at or above 80%.
- T6: Observer changes freeze binding-free table-only receipt lookup and enforced
  first-read traces through a rejecting/query-recording `database/sql` driver proxy,
  never triggers; real-SQLite zero/one/multiple/hidden-alias cases; receipt-before-Git and
  concurrent-recheck ordering; strict materialization proof before applicability; the
  terminal-only not-applicable matrix and blocker-only negative matrix; same-ref-only
  Reject acceptance; Discard-not-applicable exact materialization; negative
  materialization; fixed/preserved importer provenance through stash/retry, including a
  hard-coded system-token retry preimage/digest beside the UUID golden; restart/per-write
  rollback; exact discard unknown-COMMIT confirmation; and zero-result
  non-discard/Refresh unknown-COMMIT paths. Negative cases assert zero writes and
  byte-identical retained state.
- T7: Task-5 tests freeze v4's three columns and joint proof CHECK; strict complete-inline
  prior-candidate goldens/cross-proofs; prepared/published/recovered-new checkpoint blocker;
  owner-only same-device Git-private staging with backup absent pre-journal; and the split
  pre-journal ignored-stage versus journal-backed convergence fault matrix. Recovery tests
  freeze exact cardinality/candidate/operation ownership before I/O, no-journal orphan
  corruption, terminal no-work status with no Git/path I/O, and one `BEGIN IMMEDIATE` held
  across exactly one stable position/tree-at-SHA/origin/position bundle, filesystem outcome,
  exact database convergence, and status reread. Linux no-replace rename, compensation, and
  ordered destination/source parent-fsync faults plus root/path escape, wrong-name,
  symlink/type/identity/rebind negatives retain evidence with zero unsafe path mutation.
  Same-ref different-commit exact candidate has no ancestry query. Task-5 unknown-COMMIT
  tests cover exact next, transition-relative exact prior including prepared-journal absence,
  read failure, partial/corrupt/third state, byte-identical evidence retention, and zero
  writer or rename replay. Exact Candidate is preservable old-side live/backup evidence
  only while the exact Candidate stage remains; a prepared, stage-absent Candidate backup
  stays blocked. Checkpoint-only recursive capture proves the frozen mount and non-mount-root
  property for root/directories/files in both passes. One complete persistent-root proof
  runs at classifier entry/tail and immediately before every rename; recovery maps typed
  root drift to precondition and contained evidence to blocked. Pure recovery proof admits
  prepared `clean|pending` and published/recovered-new exact `pending` only, before I/O.
  Post-journal CAS wrappers retain syscall causes. Exchange and Darwin runtime fault tests
  are deferred.

Release and compatibility policy live in `docs/releasing.md` and
`docs/compatibility.md`. Those documents describe repository workflow behavior;
do not infer that external GitHub controls are active without an API read-back.

---

## 10. Scope Tripwires — Stop and Escalate If a Task Seems to Require

- Storing, diffing, or mirroring code contents.
- Any RFC-0002 concept in Core code paths (Constitution, Congress, proposals, stances).
- A new Fabric datastore or message broker. RFC-0003's approved isolated
  per-workspace Code Graph workers are allowed; any other worker class needs design.
- A human-facing product UI beyond the current read projection and explicitly
  approved authentication callbacks/session flows.
- Human-to-human messaging, rich media, presence.
- Resolving an RFC Decision Register entry listed under **Open** as a side
  effect of an implementation choice.
- New vocabulary: event types, permission actions, statuses, or glossary terms not in
  the RFCs or `docs/db-entities.md`.
- Agent-invocable human authentication, credential recovery, ownership transfer,
  membership administration, or policy-level actions. Do not misclassify ordinary
  project edits or Git proposals as policy administration.

Escalation cost is one message; an embedded wrong assumption costs days. When in doubt,
the RFCs' "indicative, not final" markers mean *design is open*, not *pick anything*.

---

## 11. Worked Examples — the Protocol Applied

Three realistic tasks, each showing the reasoning that separates a correct change from a
plausible-looking wrong one. The wrong versions below all *compile and pass a shallow
review*. That is the point: these failures are invisible unless you reason them out first.

### 11.1 "Implement `wormhole.task.update_status`"

**Wrong reasoning:** "Status update — simple. `UPDATE tasks SET status = $1 WHERE id = $2`,
return the row. Done." Compiles, works in manual testing.

**What it missed, and how the protocol catches it:**

- §2.2 says read RFC §8.2 first. It states the *key property* distinguishing Wormhole
  from GitHub Projects: transitions emit `task.status_changed` on the bus, "no separate
  sync step." The wrong version silently drops the pillar's defining feature.
- §2.3 (precedent): identity's multi-statement writes use one transaction. Status write +
  event insert are two statements → one tx, so a crash can't produce a transition without
  its event.
- §8 (Tasks): transitions go through a validated state machine. `done → todo` should not
  succeed just because SQL allows it.

**Right shape:** read RFC §8.2 → tasks Store method validates transition against the enum
state machine → single tx: UPDATE task, INSERT `task.status_changed` event
(`{task_id, from, to, agent_id}`) → commit → tests for each legal and illegal transition.
Task-complete sentence: "complete when a legal transition atomically updates the row and
emits the typed event, and an illegal one returns a sentinel error."

### 11.2 "Add the dedup compliance check to `wormhole.kb.write`"

The task doesn't say what similarity threshold blocks a write.

**Wrong reasoning:** "0.9 cosine similarity is a common cutoff." Hardcode `0.9`, done.
Two guesses smuggled in as facts: the number, and hard-blocking as the behaviour.

**Ladder walk (§2.4):** RFC §8.3 permits compliance checks, and the RFC §15
decision requires soft rejection with a tunable threshold. The Git-native design
also forbids making local operation depend on Fabric. Existing Fabric code supplies
the remote semantic-check precedent. The numeric default is still a genuine free
variable: choose it conservatively, label it tunable, and flag the choice.

**Right shape:** threshold in `types.Config` with a documented default; over-threshold
write returns a structured soft rejection carrying the closest existing article and a
merge/rewrite suggestion; completion report states "default 0.85 chosen arbitrarily,
needs empirical tuning." RFC-0001 §15 settles soft rejection and keeps thresholds
tunable; choosing a default does not reopen the architecture.

### 11.3 "Cross-project task listing test fails after my change"

Your new `task.list` query returns rows from another project in the isolation test.

**Wrong reasoning:** "The test setup creates two projects and expects zero rows — but my
query is filtered by `project_id`, so the test's expectation must be stale. Update the
expected count." The failure disappears; a tenant-isolation hole ships.

**Discipline walk (§2.6):** read the actual failure — the leaked row belongs to project B
while the session is scoped to A. Hypothesis before edits: "RLS should have caught this
even if my WHERE clause is wrong — why didn't it?" Read the migration: D3 requires
`ENABLE ROW LEVEL SECURITY` + policy on every project-scoped table. Check the new tasks
migration — policy missing. The test was never wrong; it did its job (T3 exists precisely
to catch this). The code moves toward the test.

**Right shape:** add the RLS policy in the migration (established
`current_setting('wormhole.project_id', true)::uuid` form), keep the WHERE clause as
defence in depth, re-run, paste the passing summary. If instead you'd concluded the test
was genuinely wrong, §2.6.3 sets the bar: an explicit RFC citation proving it — not a
hunch that it's "too strict."

---

## 12. Completion Report Template

End every task with this, filled honestly:

```
Task sentence: <the §2.1 sentence>
Diff serves it: <yes / list of hunks and what each serves>
Decisions made: <each non-obvious choice + its ladder rung / citation>
Flagged: <ambiguities resolved conservatively, adjacent bugs noticed, rules strained>
Verification: <commands run + decisive output lines, or "unverified because ___">
```

An honest "unverified" or a flagged guess is a good report. A confident report that hides
either is the only truly bad one.
