# Day 7: Task Graph Schema + Migration

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land the `tasks` and `task_links` tables as a real golang-migrate migration (RFC-0001 §8.2, architecture.md §6 Tasks), matching Day 1's schema conventions and Day 6's design sketch in `docs/db-entities.md`. Schema and migration only — no Go code, no MCP tools. Day 8 wires `wormhole.task.create`/`assign`/`list`/`update_status` on top of this.

**Architecture:** New migration `000006_task_graph` (next sequential number after Day 5's `000005_agent_tokens_expiry`). Two tables: `tasks` (Project → Task → Subtask via self-referential `parent_task_id`, status enum exactly `todo`/`wip`/`blocked`/`done` via `CHECK`) and `task_links` (polymorphic link row to KB articles/commits/PRs/events, per architecture.md §6 "not ad hoc columns"). Both are project-scoped tables and must get the full D3 multi-tenancy treatment (`project_id`, index, RLS, policy) — `docs/db-entities.md`'s existing `task_links` sketch (from Day 1) does not list a `project_id` column, so this is a deliberate deviation from that doc, corrected in the same change per D2.

**Tech Stack:** golang-migrate SQL migrations against Postgres+pgvector, no Go code this task.

## Global Constraints

- D1: migration pair `migrations/000006_task_graph.up.sql` / `.down.sql`, zero-padded sequential (next after `000005`). Down migration must fully revert (drop tables in FK-safe order). Never edit an already-applied migration.
- D2: `docs/db-entities.md` is the entity-shape authority. Its existing `tasks`/`task_links` sketch (lines 69-88, extended Day 6 with two design notes) is the base — implement exactly those columns. The one deviation (adding `project_id` to `task_links`, required by D3, not present in the current sketch) must be applied to `db-entities.md` in this same change, with a one-line note explaining why (D3 multi-tenancy is non-optional per table).
- D3: every project-scoped table (`tasks`, `task_links` — neither is `projects` nor `agents`) gets: `project_id uuid NOT NULL REFERENCES projects(id)`, an index on it, `ENABLE ROW LEVEL SECURITY`, and policy `USING (project_id = current_setting('wormhole.project_id', true)::uuid)` — copy the exact form already used for `passports`/`permissions` in `migrations/000001_init_schema.up.sql`.
- D4: `uuid` PKs via `gen_random_uuid()` (pgcrypto, already enabled by migration 000001 — do not re-run `CREATE EXTENSION`), `timestamptz NOT NULL DEFAULT now()` timestamps, `text` not `varchar`, snake_case names, header comment citing RFC-0001 §8.2.
- Status enum: exactly `todo` / `wip` / `blocked` / `done`, enforced via `CHECK (status IN (...))`, default `'todo'`. No other values, no separate Postgres `ENUM` type (matches D4's `text`-not-custom-type convention already in force — `agent_tokens`/`agents` use `text` columns, not Postgres enums, elsewhere in the schema).
- `parent_task_id` is nullable, self-referential FK to `tasks(id)`, `ON DELETE CASCADE` (deleting a task deletes its subtasks — matches the `ON DELETE CASCADE` convention already used for `passports.agent_id`/`project_id` in migration 000001).
- `task_links.task_id` FK to `tasks(id) ON DELETE CASCADE` (a task's links die with it).
- No Go code, no `internal/core/tasks` implementation, no MCP tool — those are explicitly Day 8's scope per ROADMAP.md. Leave `internal/core/tasks/doc.go` untouched.

---

### Task 1: Task graph migration

**Files:**
- Create: `migrations/000006_task_graph.up.sql`
- Create: `migrations/000006_task_graph.down.sql`
- Modify: `docs/db-entities.md` (add `project_id` to the `task_links` sketch, with a one-line D3 deviation note)

**Interfaces:** None (SQL only). No Go symbols produced or consumed.

- [ ] **Step 1: Read the precedent migrations**

Read `migrations/000001_init_schema.up.sql` in full (RLS policy form, FK/index conventions) and `docs/db-entities.md` lines 69-88 (current `tasks`/`task_links` sketch, including Day 6's two added notes) before writing SQL — column names and the two deferred-design notes must match what's already documented, except for the one deviation below.

- [ ] **Step 2: Write the up migration**

Create `migrations/000006_task_graph.up.sql` with:
```sql
-- RFC-0001 §8.2 Task Graph (Coordination pillar). Project -> Task -> Subtask
-- hierarchy via parent_task_id; status transitions emit task.status_changed
-- events (wired Day 11, not this migration). Column shapes per
-- docs/db-entities.md, extended Day 7.

CREATE TABLE tasks (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id     uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    parent_task_id uuid REFERENCES tasks(id) ON DELETE CASCADE,
    title          text NOT NULL,
    description    text NOT NULL DEFAULT '',
    owner_agent_id uuid REFERENCES agents(id),
    status         text NOT NULL DEFAULT 'todo' CHECK (status IN ('todo', 'wip', 'blocked', 'done')),
    priority       int NOT NULL DEFAULT 0,
    due_by         timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_tasks_project_id ON tasks(project_id);
CREATE INDEX idx_tasks_parent_task_id ON tasks(parent_task_id);

ALTER TABLE tasks ENABLE ROW LEVEL SECURITY;
CREATE POLICY tasks_project_isolation ON tasks
    USING (project_id = current_setting('wormhole.project_id', true)::uuid);

-- task_links: polymorphic links from a task to a KB article, commit, PR, or
-- event (architecture.md §6 -- "not ad hoc columns"). project_id is a
-- deliberate deviation from docs/db-entities.md's original sketch, added
-- here for D3 multi-tenancy (RLS requires it on every project-scoped
-- table); db-entities.md is updated in this same change.
CREATE TABLE task_links (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_id    uuid NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    link_type  text NOT NULL,
    target_ref text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_task_links_project_id ON task_links(project_id);
CREATE INDEX idx_task_links_task_id ON task_links(task_id);

ALTER TABLE task_links ENABLE ROW LEVEL SECURITY;
CREATE POLICY task_links_project_isolation ON task_links
    USING (project_id = current_setting('wormhole.project_id', true)::uuid);
```

- [ ] **Step 3: Write the down migration**

Create `migrations/000006_task_graph.down.sql`:
```sql
DROP TABLE IF EXISTS task_links;
DROP TABLE IF EXISTS tasks;
```

- [ ] **Step 4: Apply and verify round-trip**

Ensure Postgres is up (`docker compose up -d db` or `docker-compose up -d db` — check which the repo's `docker-compose.yml` responds to), then run:
```bash
/home/harley/go/bin/migrate -path migrations -database "postgres://wormhole:wormhole@127.0.0.1:5432/wormhole?sslmode=disable" up
/home/harley/go/bin/migrate -path migrations -database "postgres://wormhole:wormhole@127.0.0.1:5432/wormhole?sslmode=disable" down 1
/home/harley/go/bin/migrate -path migrations -database "postgres://wormhole:wormhole@127.0.0.1:5432/wormhole?sslmode=disable" up
```
Expected: all three succeed, no errors, ends in the `up` state (migration 000006 applied). If the `migrate` binary isn't at that path, locate it (`which migrate`, or `go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest`) and use the correct path/DB URL (cross-check `internal/types/config.go` or `.env` if the connection string differs).

- [ ] **Step 5: Manual constraint smoke-check**

Run direct SQL against the running DB to confirm the CHECK constraint and RLS actually behave (use `psql "postgres://wormhole:wormhole@127.0.0.1:5432/wormhole?sslmode=disable"` or `docker compose exec db psql -U wormhole`):
```sql
-- should fail (invalid status)
INSERT INTO projects (name, owner) VALUES ('t7', 'harley') RETURNING id; -- note the id
INSERT INTO tasks (project_id, title, status) VALUES ('<id from above>', 'x', 'not-a-status');
```
Expected: the second statement errors with a check-constraint violation naming `tasks_status_check` (or similar). Clean up the test project row afterward (`DELETE FROM projects WHERE name = 't7'`, cascades to any tasks).

- [ ] **Step 6: Update `docs/db-entities.md`**

Add `project_id -> projects` as the first bullet under the existing `## task_links` section, and append one line noting this is a D3-driven deviation from the original Day 1/Day 6 sketch (RLS requires it on every project-scoped table), per D2's "update in the same change, with the reason" rule. Do not alter any other existing bullet or column name in the file.

- [ ] **Step 7: Run the full repo test suite**

Run: `go test ./...`
Expected: PASS, unchanged from before this task — this migration touches no Go code, so no test should newly fail or newly pass.

- [ ] **Step 8: Commit**

```bash
git add migrations/000006_task_graph.up.sql migrations/000006_task_graph.down.sql docs/db-entities.md
git commit -m "Day 7: task graph migration (tasks, task_links tables, RFC-0001 §8.2)"
```

---

## Post-plan: update ROADMAP.md

After the task is reviewed clean, check off Day 7's two items in `ROADMAP.md` (lines 68-69) and commit separately, matching prior days' pattern.
