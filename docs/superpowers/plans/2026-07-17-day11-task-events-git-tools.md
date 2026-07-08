# Day 11: Task-Status Auto-Emit Events + Git Tools

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close RFC-0001 §8.2's defining property (task state transitions emit `task.status_changed` on the bus atomically, no separate sync step) and wire the two Git Integration tools (`wormhole.git.link_commit`, `wormhole.git.request_review`), manual-link only per architecture.md §6.

**Ambiguities resolved before this plan (do not re-litigate):**

1. `wormhole.task.update_status` must supply a `channel_id` explicitly (no implicit/default channel exists anywhere in the codebase; `wormhole.channel.post` already requires it explicitly — this is the established precedent, RFC/architecture.md are silent on channel selection).
2. Git links use a dedicated `git_links` table exactly as sketched in `docs/db-entities.md` (§`## git_links`), not `task_links` — `task_links` is polymorphic for KB/commit/PR/event pointers *from a task*, but `wormhole.git.request_review` has no `task_id` in its RFC-0001 §9 signature, so it cannot go through `task_links`.
3. Git tooling lives in a **new** top-level package `internal/core/git`, human-approved (architecture.md R4 requires sign-off for new top-level packages). `docs/architecture.md`'s module map table (§2) must be updated in this change to add the row.

**Tech Stack:** Go, Postgres (`internal/storage`), existing `internal/core/events` / `internal/core/tasks` patterns.

## Global Constraints

- All MCP handlers follow the pattern in `internal/mcp/task.go` / `internal/mcp/channel.go` exactly.
- `RequiresAuth: true` on all new tools.
- Do not use em-dashes (commas, colons, semicolons, parentheses instead).
- No ORM, no web framework, stdlib `net/http`/`database/sql` only, matching every existing core package.
- R2 (architecture.md §2): `internal/core/*` packages never import each other except the one sanctioned `tasks` → `events` exception. `internal/core/git` must NOT import `internal/core/tasks` or `internal/core/events` — it only needs `internal/storage`-style `*sql.DB` access, matching `internal/core/kb`'s isolation.
- T1-T4 (architecture.md §7): real-Postgres tests (`t.Skipf` if unreachable, matching every existing `*_test.go`), happy path + each sentinel error + isolation/RLS test, `go build ./...` / `go vet ./...` / `go test ./...` all passing with output observed before any commit.

---

### Task 1: Auto-emit `task.status_changed` on status transitions

**Files:**
- Modify: `internal/core/events/events.go` (extract tx-scoped publish helper)
- Modify: `internal/core/tasks/tasks.go` (`NewStore` takes `*events.Store`; `UpdateStatus` takes `channelID`, `agentID`, emits event in the same tx)
- Modify: `internal/mcp/task.go` (`UpdateTaskStatusInput` gains `channel_id`; `UpdateTaskStatusTool` passes `scope.AgentID`)
- Modify: `cmd/wormhole-server/main.go` (pass `eventsStore` into `tasks.NewStore`)
- Modify: `internal/core/tasks/tasks_test.go`, `internal/mcp/task_test.go` (existing tests call `NewStore`/`UpdateStatus`/`UpdateTaskStatusTool` — signatures changed, must be updated to compile and to assert the new event-emission behavior)

**Interfaces:**
- Consumes: `internal/types.TaskStatusChangedPayload` (already defined Day 10, `internal/types/events.go`: `TaskID`, `FromStatus`, `ToStatus` — no `AgentID` field, it is already a column on `events`, do not add a duplicate).
- Produces: `func (s *events.Store) PublishEventInTx(ctx context.Context, tx *sql.Tx, projectID, channelID, agentID, eventType string, payload json.RawMessage, note *string) (Event, error)` (exported, new).

- [ ] **Step 1: Extract `PublishEventInTx` in `internal/core/events/events.go`**
  Read the existing `PublishEvent` method fully first (it already does: begin tx, `SET LOCAL wormhole.project_id`, passport check, channel lookup, INSERT, commit).
  Refactor so the tx-scoped body (everything after "begin tx" through the INSERT/RETURNING scan, *not* including `Commit`/`Rollback`) becomes a new exported method `PublishEventInTx(ctx context.Context, tx *sql.Tx, projectID, channelID, agentID, eventType string, payload json.RawMessage, note *string) (Event, error)` that takes an existing `*sql.Tx` instead of opening its own. It still validates `AllowedEventTypes[eventType]`, defaults empty payload to `{}`, does the passport check and channel lookup against the *given* tx (assume `wormhole.project_id` is already set by the caller in that tx, since `tasks.UpdateStatus` sets it once for its own statements), and returns the scanned `Event`.
  `PublishEvent` (the existing exported entrypoint used by `wormhole.channel.post`) becomes a thin wrapper: begin its own tx, `SET LOCAL wormhole.project_id`, call `PublishEventInTx`, commit/rollback exactly as before. Its exported signature and behavior must not change, existing `channel_test.go` callers must keep passing unmodified.

- [ ] **Step 2: Wire `tasks.Store` to depend on `events.Store`**
  In `internal/core/tasks/tasks.go`:
  - Add `import "github.com/H4RL33/wormhole/internal/core/events"` and `"encoding/json"` and `"github.com/H4RL33/wormhole/internal/types"` (for `TaskStatusChangedPayload`).
  - `Store` struct gains a field `events *events.Store`.
  - `NewStore(db *sql.DB, eventsStore *events.Store) *Store` (signature change, update every caller: `cmd/wormhole-server/main.go`, `internal/core/tasks/tasks_test.go`, `internal/mcp/task_test.go`).
  - `UpdateStatus(ctx context.Context, projectID, taskID, newStatus, channelID, agentID string) (Task, error)` (signature gains `channelID`, `agentID`, both required strings; no default/implicit channel per this plan's resolved ambiguity #1).
  - Inside the existing single tx (after the `UPDATE tasks ... RETURNING` succeeds, before `tx.Commit()`): marshal `types.TaskStatusChangedPayload{TaskID: taskID, FromStatus: currentStatus, ToStatus: newStatus}` to `json.RawMessage`, call `s.events.PublishEventInTx(ctx, tx, projectID, channelID, agentID, "task.status_changed", payload, nil)`. If it errors, return the wrapped error (tx rolls back via the existing `defer tx.Rollback()`, so the status update and event insert are atomic — either both happen or neither does, per RFC-0001 §8.2 and architecture.md §9.1's worked example).
  - Then `tx.Commit()` exactly as before.
  - Remove the now-stale doc comments in the package header and above `UpdateStatus` that say emitting events is "wired later (Day 11)" — Day 11 is this task, update them to state current behavior factually (no "TODO"/"later" language left).

- [ ] **Step 3: Update `internal/mcp/task.go`**
  - `UpdateTaskStatusInput` gains `ChannelID string \`json:"channel_id"\``.
  - `UpdateTaskStatusTool`'s handler calls `store.UpdateStatus(ctx, projectID, in.TaskID, in.NewStatus, in.ChannelID, scope.AgentID)`.
  - Update the tool's `Description` string to mention it also emits `task.status_changed` on the given channel.

- [ ] **Step 4: Update `cmd/wormhole-server/main.go`**
  Ensure `eventsStore := events.NewStore(db)` is constructed before `tasks.NewStore(db, eventsStore)` (reorder if needed), and `tasks.NewStore` call site passes it.

- [ ] **Step 5: Update existing tests to compile and to cover the new behavior**
  - `internal/core/tasks/tasks_test.go`: every `tasks.NewStore(db)` call becomes `tasks.NewStore(db, eventsStore)` (construct a real `events.Store` from the same `testDB(t)`, matching how `internal/mcp` tests already share a DB handle across stores). Every `store.UpdateStatus(ctx, projectID, taskID, status)` call gains a valid `channelID` (create one via `eventsStore.CreateChannel` in the test setup) and `agentID` (already available in existing fixtures). Add a new test asserting that a legal transition also inserts a `task.status_changed` row in `events` with the correct `task_id`/`from_status`/`to_status` payload and matching `agent_id` (query `events` directly via `testDB(t)`, matching the pattern other tests use for direct-SQL assertions). Add a test asserting an illegal transition leaves `events` untouched (no orphan event row on rollback).
  - `internal/mcp/task_test.go`: update any `UpdateTaskStatusInput` literal to include a real `channel_id` (create a channel first via the existing channel-tool test helpers or store call).

- [ ] **Step 6: Run full test suite**
  Run: `go build ./...`, `go vet ./...`, `go test ./...`
  Expected: PASS, no regressions.

- [ ] **Step 7: Commit**
  Commit: `feat(tasks): auto-emit task.status_changed events atomically on status transitions`

---

### Task 2: Git Integration MCP Tools

**Files:**
- Create: `migrations/000008_git_links.up.sql`, `migrations/000008_git_links.down.sql`
- Create: `internal/core/git/git.go`
- Create: `internal/core/git/git_test.go`
- Create: `internal/mcp/git.go`
- Create: `internal/mcp/git_test.go`
- Modify: `cmd/wormhole-server/main.go` (register the two new tools)
- Modify: `docs/architecture.md` (§2 module map: add `internal/core/git` row; this is the human sign-off already given for this plan, recorded here for the next reader)

**Interfaces:**
- Produces:
  - `func NewStore(db *sql.DB) *Store` (package `git`, mirrors `events.NewStore`/`tasks.NewStore` exactly)
  - `func (s *Store) LinkCommit(ctx context.Context, projectID, agentID string, taskID *string, repo, commitSHA, summary string) (GitLink, error)`
  - `func (s *Store) RequestReview(ctx context.Context, projectID, agentID, repo, prURL, summary string) (GitLink, error)`
  - `func LinkCommitTool(store *git.Store) Tool`
  - `func RequestReviewTool(store *git.Store) Tool`

- [ ] **Step 1: Migration**
  Follow `migrations/000007_event_channels.up.sql`'s exact style (header comment citing RFC-0001 §8.6, column block, indexes, RLS policy, `down.sql` mirrors with `DROP TABLE`).
  Table `git_links` per `docs/db-entities.md`'s `## git_links` section verbatim:
  - `id` (uuid PK, `gen_random_uuid()`)
  - `project_id` (uuid NOT NULL, FK `projects(id)` ON DELETE CASCADE, RLS column matching every other project-scoped table)
  - `task_id` (uuid, nullable, FK `tasks(id)` ON DELETE CASCADE — nullable because `request_review` has no task)
  - `repo` (text NOT NULL)
  - `commit_sha` (text, nullable)
  - `pr_url` (text, nullable)
  - `summary` (text NOT NULL)
  - `agent_id` (uuid NOT NULL, FK `agents(id)` ON DELETE CASCADE)
  - `created_at` (timestamptz NOT NULL DEFAULT now())
  Add a `CHECK` constraint that exactly one of `commit_sha`/`pr_url` is non-null (link_commit always sets `commit_sha` and leaves `pr_url` null; request_review always sets `pr_url` and leaves `commit_sha` null and `task_id` null — this is a reasonable extension per CLAUDE.md §3.2 since neither RFC nor db-entities.md states the constraint explicitly; note this inference in the migration's header comment). Index on `project_id`, index on `task_id`. Enable RLS with the standard `project_id = current_setting('wormhole.project_id', true)::uuid` policy, matching every other project-scoped table exactly.

- [ ] **Step 2: `internal/core/git/git.go`**
  Mirror `internal/core/events/events.go`'s shape and style exactly (package doc comment citing RFC-0001 §8.6 and architecture.md §Git integration "manual link only, no webhooks/CI/cloning/diff storage"; `GitLink` struct with all columns, `NewStore`, `Store` type, one method per operation, `SELECT set_config('wormhole.project_id', ...)` + passport check pattern copied from `events.PublishEvent`, single tx per method, `RETURNING` + scan, no ORM).
  `LinkCommit` requires `taskID` (verify it exists in-project the same way `events.PublishEvent` verifies channel existence, since `git_links.task_id` FKs to `tasks`), inserts with `pr_url = NULL`.
  `RequestReview` inserts with `task_id = NULL`, `commit_sha = NULL`.
  Both verify the calling agent has a passport for the project (same passport-check block as `events.PublishEvent`, copy the pattern, do not invent a different check).
  Define sentinel errors matching the package's existing error style (`tasks.ErrTaskNotFound`, `tasks.ErrPassportNotFound` as the naming precedent): `ErrTaskNotFound`, `ErrPassportNotFound`.

- [ ] **Step 3: `internal/mcp/git.go`**
  Mirror `internal/mcp/channel.go` exactly. Tool names, per RFC-0001 §9 verbatim: `wormhole.git.link_commit`, `wormhole.git.request_review`.
  `LinkCommitInput`: `TaskID string`, `Repo string`, `CommitSHA string`, `Summary string` (json tags `task_id`, `repo`, `commit_sha`, `summary`, matching RFC-0001 §9's `wormhole.git.link_commit(task_id, repo, commit_sha, summary)` argument order/names).
  `RequestReviewInput`: `Repo string`, `PRUrl string`, `Summary string` (json tags `repo`, `pr_url`, `summary`, matching RFC-0001 §9's `wormhole.git.request_review(repo, pr_url, summary)`).
  Both outputs return the created `git_link_id` plus the fields that were set (mirror `CreateChannelOutput`'s shape/style).
  `RequiresAuth: true` on both. Errors wrapped `fmt.Errorf("mcp: wormhole.git.link_commit: %w", err)` / same for request_review, matching every other tool.

- [ ] **Step 4: Register in `cmd/wormhole-server/main.go`**
  Add `gitStore := git.NewStore(db)`, register `git.LinkCommitTool(gitStore)` and `git.RequestReviewTool(gitStore)` alongside the existing tool registrations.

- [ ] **Step 5: Tests**
  `internal/core/git/git_test.go`: mirror `internal/core/events/events_test.go`'s helper pattern (`testDB(t)`, `t.Skipf` if Postgres unreachable). Cover: `LinkCommit` happy path (row persisted with correct `commit_sha`, `pr_url` NULL), `LinkCommit` with unknown `task_id` returns `ErrTaskNotFound`, `RequestReview` happy path (`task_id`/`commit_sha` NULL, `pr_url` set), unregistered/no-passport agent returns `ErrPassportNotFound` for both, cross-project isolation test (a `git_links` row from project A is invisible when `wormhole.project_id` is set to project B), matching T3's explicit cross-project rejection test requirement.
  `internal/mcp/git_test.go`: mirror `internal/mcp/channel_test.go`'s pattern, one test per tool's happy path plus one error-surfacing test (e.g. `TestGitTools_LinkCommitUnknownTask`).

- [ ] **Step 6: Update `docs/architecture.md` module map**
  In §2's table, add a row: `| \`internal/core/git\` | Git integration pointers: commit links, review requests (manual-link only, RFC-0001 §8.6) | \`internal/types\`, stdlib |`. Do not add `internal/core/events` or `internal/core/tasks` as allowed imports for it (per this plan's Global Constraints, R2 stays a one-exception rule).

- [ ] **Step 7: Run full test suite**
  Run: `go build ./...`, `go vet ./...`, `go test ./...`
  Expected: PASS.

- [ ] **Step 8: Commit**
  Commit: `feat(git): wire wormhole.git.link_commit/request_review MCP tools`
