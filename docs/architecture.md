# Wormhole Architecture & Implementation Guardrails

**Audience:** implementation agents (any model tier) making changes to this repo.
**Authority order:** RFC-0001 > RFC-0002 > this document > existing code. This document
derives from the RFCs and the code as of Day 3; if it conflicts with an RFC, the RFC wins
and this file has a bug — flag it, don't silently pick one.

This is a *constraint document*, not a tutorial. Every section states rules. If a task
requires breaking a rule here, stop and escalate to the orchestrating agent or human;
do not improvise.

---

## 0. Operating Protocol — How to Think Before You Type

Most defects in agent-written code are not coding errors. They are *reasoning* errors made
before the first line: guessing instead of reading, importing habits from other codebases,
expanding scope, and declaring victory without evidence. This section is the antidote.
Follow it as a literal procedure, in order, for every task.

### 0.1 Restate the task in one sentence

Write (in your working notes, not the code) one sentence: *"This task is complete when
___."* If you cannot fill the blank precisely, you do not understand the task — go back to
the task description or escalate. Everything you do next must serve that sentence. Anything
that doesn't serve it is scope creep, even if it's a genuine improvement. Note improvements
separately; do not implement them.

### 0.2 Read before you write — minimum reading list

Never write code from memory of "how Go projects usually work." This repo has one way of
doing each thing, and it is written down. Before editing, read:

| Task touches | Must read first |
|---|---|
| Any core package | `internal/core/identity/identity.go` (the canonical pattern) + the package you're editing |
| DB schema | `docs/db-entities.md` + the latest migration pair in `migrations/` |
| MCP tools | `internal/mcp/registry.go` + RFC-0001 §9 |
| Tests | `internal/core/identity/identity_test.go` |
| Anything at all | The RFC section the task cites; §1–2 of this document |

If the task cites an RFC section, read that exact section — not your recollection of it.
The RFCs are short; reading the real text costs less than one wrong assumption.

### 0.3 Locate the precedent, then copy it

For any construct you're about to write (a store method, a migration, an error, a test),
ask: *"Where does this repo already do something shaped like this?"* Find it, open it,
and match it — naming, error style, transaction shape, comment density. The correct
implementation of almost every alpha task is *"the identity package's pattern, applied to
a new entity."* If you find **no** precedent, that's a signal, not a licence: it means the
construct is new to the repo, and new constructs need a sanity check against §2's rules and
§8's tripwires before you invent one.

Concretely, this rules out reflexes learned elsewhere: no ORM (no GORM/sqlx/ent), no web
framework (no gin/echo — stdlib `net/http`), no `panic` for control flow, no global
singletons, no `init()` registration magic, no context stored in structs, no interfaces
defined before a second implementation exists.

### 0.4 The ambiguity ladder — what to do when the task under-specifies

Ambiguity is normal; guessing is the failure. Resolve in this exact order, stopping at the
first rung that answers the question:

1. **RFC text.** Does RFC-0001/0002 state it? Then it's decided; do that.
2. **`docs/db-entities.md`** for anything entity-shaped.
3. **Existing code.** Does the repo already embody an answer? Match it.
4. **This document's rules.** Do §2–§7 constrain it to one option?
5. **RFC "open questions" (§15 Core / §9 Governance).** Is it listed there? Then it is
   *deliberately* undecided. Do not resolve it. Pick the most conservative reading
   (strictest scoping, per-project isolation, poll not push), state in your output that
   you did so and why, and flag it.
6. **None of the above** → stop and escalate with a concrete question and your
   recommended answer. "Should `task.assign` accept a human owner? RFC §8.2 says owner
   is 'agent or human' but the agents table has no human rows — I recommend X because Y"
   is a good escalation. "What should I do?" is not.

The test for whether you guessed: could you cite a source (rung 1–5) for every decision in
your diff? If a decision traces only to "seemed reasonable," it's a guess — surface it.

### 0.5 Smallest correct diff

The right diff is the smallest one that makes the task-complete sentence (§0.1) true while
obeying every rule in this document. Do not: reformat untouched code, rename things for
taste, add "while I'm here" fixes, add configuration for needs that don't exist yet, or
build abstractions for the *next* task. If you notice a real adjacent bug, report it in
your output; touch it only if told to.

A useful self-check before finishing: for each hunk in your diff, can you say which part
of the task sentence it serves? Hunks that serve nothing get reverted.

### 0.6 When something fails — debugging discipline

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

### 0.7 Evidence before "done"

"It should work" is not a state of the world; it's a feeling. Done means: you ran the
commands in T4, you read the output, and the output says pass. Paste the decisive lines
(final test summary, not the full log) into your completion report. If you could not run
verification (missing DB, sandbox limits), the status is **not** "done" — it is "written,
unverified, because ___", stated exactly that way.

### 0.8 Rationalisations to catch yourself making

| The thought | The reality |
|---|---|
| "This is standard practice" | Standard where? This repo's practice is §3. Match it. |
| "The RFC probably means..." | Open the RFC. It's one file away. |
| "I'll add this field, it'll be needed later" | Later's task adds it. Yours doesn't. (§0.5) |
| "The test is too strict" | The test encodes a security property. Prove it wrong or satisfy it. (§0.6) |
| "This helper would be cleaner in a shared package" | Cross-core imports are banned (R2). Duplicate or escalate. |
| "Mocking the DB makes tests simpler" | T1 exists because mocks pass while RLS fails. Real Postgres. |
| "It compiles, so it's done" | Compiling is not passing. (§0.7) |
| "This is basically like [famous project]" | Wormhole rejects several famous patterns on purpose. Precedent is this repo only. (§0.3) |
| "I'll just resolve this ambiguity quietly" | Silent decisions are how policy drift starts. Ladder, then flag. (§0.4) |

---

## 1. System in One Paragraph

Wormhole is a single Go backend (`wormhole-server`) plus a CLI (`wormhole-cli`), backed by
one Postgres database with pgvector. It exposes four pillars — Event Bus, Task Graph,
Knowledge Base, Identity & Permissions — exclusively through an MCP tool surface. Git stays
the sole source of truth for code; Wormhole stores pointers (commit SHAs, PR URLs) and
commentary only. There is no message broker, no second datastore, no web UI in scope.
Governance (Constitution, Congress; RFC-0002) is **not** being built in the current
24-day alpha and must not leak into Core code.

```
MCP clients (Claude, Codex, Gemini, ...)
        │  MCP tools only — the sole platform surface (RFC-0001 §5.5)
        ▼
cmd/wormhole-server ──► internal/mcp (tool registry + auth boundary)
                              │
                              ▼
                internal/core/{identity,tasks,events,kb,permissions}
                              │
                              ▼
                internal/storage ──► Postgres + pgvector (only external dependency)
```

---

## 2. Module Map and Dependency Rules

| Package | Owns | May import |
|---|---|---|
| `cmd/wormhole-server` | Process wiring: config, HTTP server, registry construction | `internal/mcp`, `internal/storage`, `internal/types` |
| `cmd/wormhole-cli` | CLI entrypoint (`wormhole join` etc.) | `internal/types`, client-side code only |
| `internal/mcp` | MCP tool descriptors, registry, request/response schemas, auth middleware | `internal/core/*`, `internal/types` |
| `internal/core/identity` | Agents, tokens, passports, whoami, audit trail | `internal/types`, stdlib |
| `internal/core/tasks` | Task graph: CRUD, status machine, task links | `internal/types`, `internal/core/events` (to emit transition events) |
| `internal/core/events` | Channels, append-only event log, typed event payloads | `internal/types`, stdlib |
| `internal/core/kb` | KB articles, links, embeddings, compliance checks, semantic search | `internal/types`, stdlib |
| `internal/core/permissions` | Permission resolution/enforcement helpers | `internal/types`, stdlib |
| `internal/core/git` | Git integration pointers: commit links, review requests (manual-link only, RFC-0001 §8.6) | `internal/types`, stdlib |
| `internal/storage` | DB connection only (`Open`) | `internal/types`, `lib/pq` |
| `internal/types` | Config, shared plain types | stdlib only |

**Hard dependency rules:**

- R1: `internal/core/*` packages never import `internal/mcp`. Flow is one-way: mcp → core.
- R2: `internal/core/*` packages never import each other, with one sanctioned exception:
  `tasks` → `events`, because task status transitions emit events (RFC-0001 §8.2).
  Need another cross-core import? Escalate; do not add it.
- R3: `internal/types` imports nothing outside stdlib. It is the bottom of the graph.
- R4: No new top-level packages, no new external Go dependencies, without explicit
  human sign-off. Current dependency budget: `lib/pq`, golang-migrate (tooling), and
  whatever MCP SDK gets frozen when the real MCP transport lands. That's it.
- R5: One database. No Redis, no NATS, no SQLite cache, no second service. RFC-0001 §7.1
  leaves streams open as a *future* option; the alpha answer is "Postgres table, poll".

---

## 3. Layering Pattern (follow `internal/core/identity` exactly)

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
   distinguish failure modes (RFC-0001 §13). Apply the same principle to any future
   auth-adjacent lookup.
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

## 4. Database Rules

- D1: Schema changes only via golang-migrate pairs in `migrations/`
  (`NNNNNN_name.up.sql` + `.down.sql`, zero-padded sequential). Down migration must
  actually revert. Never edit an already-committed migration; add a new one.
- D2: Entity shapes come from `docs/db-entities.md`. Deviating from it means updating
  that file in the same change, with the reason.
- D3: Every project-scoped table (everything except `projects` and `agents`) gets:
  a `project_id uuid NOT NULL REFERENCES projects(id)` column, an index on it,
  `ENABLE ROW LEVEL SECURITY`, and a policy of the established form:
  `USING (project_id = current_setting('wormhole.project_id', true)::uuid)`.
  This is the multi-tenancy guarantee (RFC-0001 §13); it is not optional per table.
- D4: Conventions already in force: `uuid` PKs via `gen_random_uuid()` (pgcrypto),
  `timestamptz NOT NULL DEFAULT now()` timestamps, `text` not `varchar`, `jsonb` with
  `DEFAULT '[]'` for list-shaped columns, snake_case names, header comment citing the
  RFC section.
- D5: Append-only tables (`events`, `audit_log`, future Constitution versions): no
  UPDATE or DELETE statements against them anywhere in application code. Corrections
  are new rows.
- D6: KB embeddings live in pgvector (`vector` column on `kb_articles`), not an
  external vector DB.

---

## 5. MCP Surface Rules

- M1: The MCP tool list in RFC-0001 §9 is **indicative, not finalised**. Tool *names*
  (`wormhole.agent.register`, `wormhole.task.create`, `wormhole.kb.search`, ...) are
  fixed; exact request/response schemas get designed at implementation time and frozen
  in `internal/mcp`. When a schema decision isn't obvious, propose it in the PR/task
  notes rather than inventing silently.
- M2: Naming grammar is `wormhole.<pillar-noun>.<verb>`. Pillars: `agent`, `channel`,
  `task`, `kb`, `git`. No new pillar prefixes; `wormhole.governance.*` is RFC-0002 and
  out of scope.
- M3: Every capability ships as an MCP tool or it doesn't exist (RFC-0001 §5.5).
  No REST-only endpoints for platform features. `/healthz` and similar operational
  endpoints are the only exception.
- M4: Auth happens at the MCP boundary (`internal/mcp` middleware resolves bearer token
  via `identity.Store.WhoAmI`, yielding `AuthenticatedScope`), then core packages
  receive the already-resolved scope. Core packages never re-parse tokens.
- M5: Permission checks use the `AuthenticatedScope.Permissions` list against action
  names matching the `permissions.action` vocabulary (`post_channel`, `create_task`,
  `write_kb`, `modify_permissions`, ...). Extending that vocabulary requires updating
  `docs/db-entities.md` too.
- M6: Destructive actions (delete project, revoke all access, `modify_permissions`)
  are human-only by default (RFC-0001 §13). Never wire a code path that lets an agent
  identity perform them.

---

## 6. Pillar-Specific Constraints

### Events / Channels
- Typed events first: `event_type` from the RFC vocabulary
  (`task.status_changed`, `review.requested`, `build.failed`, `discovery.logged`,
  `message.posted`), typed `payload` jsonb per type, optional free-text `note`.
  `message.posted` is the escape hatch; do not add prose-first event types.
- New event types are an escalation, not a local decision.
- Delivery model for alpha: poll. Do not build push/streaming infrastructure
  (open question, RFC-0001 §15).

### Tasks
- Hierarchy is Project → Task → Subtask via `parent_task_id`. Status enum exactly
  `todo / wip / blocked / done`. Transitions go through a validated state machine and
  emit `task.status_changed` on the bus in the same operation — never a separate sync.
- Links to KB articles / commits / PRs / events go through `task_links`, not ad hoc
  columns.

### Knowledge Base
- Atomic articles: one fact/decision/procedure each. Markdown body + jsonb frontmatter.
- Compliance checks run **server-side** on write (RFC-0001 §13): semantic dedup against
  existing embeddings, length ceiling, required links where applicable. Rejection style
  is soft-reject-with-rewrite-suggestion, not hard block (RFC-0001 §15 leans this way;
  exact thresholds are tunable config, not hardcoded constants).
- Linking via `kb_links` rows (graph), never folder/path hierarchy.
- Search is semantic (pgvector similarity), project-scoped. Cross-project KB visibility
  is an open question (RFC-0001 §15) — default to strict per-project until decided.

### Identity
- Agent identity is project-agnostic; project access flows through passports +
  scoped tokens. Do not add `project_id` to `agents`.
- Passport = the join-time credential carrying repositories, roles, resolved
  permissions (RFC-0001 §8.4). One passport per (agent, project), enforced by the
  existing UNIQUE constraint.
- Every action attributable: audit log rows are append-only and written by the server,
  not the client.

### Git integration
- Alpha scope is a manual link field only (`git_links`, `wormhole.git.link_commit`).
  No webhooks, no CI hooks, no repo cloning, no diff storage — Wormhole never stores
  or mirrors code, only `repo` + `commit_sha`/`pr_url` + `summary`.

---

## 7. Testing Rules

- T1: Follow `internal/core/identity/identity_test.go`: DB-backed tests against real
  Postgres (docker-compose service), not mocks of `*sql.DB`.
- T2: Every core package change ships tests covering: the happy path, each sentinel
  error, and the security property the package guards (isolation, forgery,
  scope preservation — whatever applies).
- T3: RLS and project isolation get explicit cross-project rejection tests whenever a
  new project-scoped table or query lands.
- T4: Do not claim done without `go build ./...`, `go vet ./...`, and `go test ./...`
  passing, run and output observed.

## 8. Scope Tripwires — Stop and Escalate If a Task Seems to Require

- Storing, diffing, or mirroring code contents.
- Any RFC-0002 concept in Core code paths (Constitution, Congress, proposals, stances).
- A second datastore, message broker, or background worker process.
- A human-facing UI beyond a minimal read-only surface.
- Human-to-human messaging, rich media, presence.
- Resolving an RFC open question (§15 Core, §9 Governance) as a side effect of an
  implementation choice.
- New vocabulary: event types, permission actions, statuses, or glossary terms not in
  the RFCs or `docs/db-entities.md`.
- Agent-invocable destructive or policy-level actions.

Escalation cost is one message; an embedded wrong assumption costs days. When in doubt,
the RFCs' "indicative, not final" markers mean *design is open*, not *pick anything*.

---

## 9. Worked Examples — the Protocol Applied

Three realistic tasks, each showing the reasoning that separates a correct change from a
plausible-looking wrong one. The wrong versions below all *compile and pass a shallow
review*. That is the point: these failures are invisible unless you reason them out first.

### 9.1 "Implement `wormhole.task.update_status`"

**Wrong reasoning:** "Status update — simple. `UPDATE tasks SET status = $1 WHERE id = $2`,
return the row. Done." Compiles, works in manual testing.

**What it missed, and how the protocol catches it:**

- §0.2 says read RFC §8.2 first. It states the *key property* distinguishing Wormhole
  from GitHub Projects: transitions emit `task.status_changed` on the bus, "no separate
  sync step." The wrong version silently drops the pillar's defining feature.
- §0.3 (precedent): identity's multi-statement writes use one transaction. Status write +
  event insert are two statements → one tx, so a crash can't produce a transition without
  its event.
- §6 (Tasks): transitions go through a validated state machine. `done → todo` should not
  succeed just because SQL allows it.

**Right shape:** read RFC §8.2 → tasks Store method validates transition against the enum
state machine → single tx: UPDATE task, INSERT `task.status_changed` event
(`{task_id, from, to, agent_id}`) → commit → tests for each legal and illegal transition.
Task-complete sentence: "complete when a legal transition atomically updates the row and
emits the typed event, and an illegal one returns a sentinel error."

### 9.2 "Add the dedup compliance check to `wormhole.kb.write`"

The task doesn't say what similarity threshold blocks a write.

**Wrong reasoning:** "0.9 cosine similarity is a common cutoff." Hardcode `0.9`, done.
Two guesses smuggled in as facts: the number, and hard-blocking as the behaviour.

**Ladder walk (§0.4):** RFC §8.3 mandates the check exists, server-side, but no number —
rung 1 gives behaviour, not threshold. Not entity-shaped (rung 2). No precedent in code
(rung 3). §6 KB rules (rung 4) answer more than expected: *soft-reject-with-rewrite-
suggestion, not hard block*, and *thresholds are tunable config, not hardcoded constants*.
Remaining ambiguity, the default value, is a genuine free variable → pick conservatively,
flag it.

**Right shape:** threshold in `types.Config` with a documented default; over-threshold
write returns a structured soft rejection carrying the closest existing article and a
merge/rewrite suggestion; completion report states "default 0.85 chosen arbitrarily,
needs empirical tuning per RFC §15" — the open question stays visibly open.

### 9.3 "Cross-project task listing test fails after my change"

Your new `task.list` query returns rows from another project in the isolation test.

**Wrong reasoning:** "The test setup creates two projects and expects zero rows — but my
query is filtered by `project_id`, so the test's expectation must be stale. Update the
expected count." The failure disappears; a tenant-isolation hole ships.

**Discipline walk (§0.6):** read the actual failure — the leaked row belongs to project B
while the session is scoped to A. Hypothesis before edits: "RLS should have caught this
even if my WHERE clause is wrong — why didn't it?" Read the migration: D3 requires
`ENABLE ROW LEVEL SECURITY` + policy on every project-scoped table. Check the new tasks
migration — policy missing. The test was never wrong; it did its job (T3 exists precisely
to catch this). The code moves toward the test.

**Right shape:** add the RLS policy in the migration (established
`current_setting('wormhole.project_id', true)::uuid` form), keep the WHERE clause as
defence in depth, re-run, paste the passing summary. If instead you'd concluded the test
was genuinely wrong, §0.6.3 sets the bar: an explicit RFC citation proving it — not a
hunch that it's "too strict."

---

## 10. Completion Report Template

End every task with this, filled honestly:

```
Task sentence: <the §0.1 sentence>
Diff serves it: <yes / list of hunks and what each serves>
Decisions made: <each non-obvious choice + its ladder rung / citation>
Flagged: <ambiguities resolved conservatively, adjacent bugs noticed, rules strained>
Verification: <commands run + decisive output lines, or "unverified because ___">
```

An honest "unverified" or a flagged guess is a good report. A confident report that hides
either is the only truly bad one.
