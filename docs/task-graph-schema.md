# Task Graph Schema

No SQL yet — task graph entities and relations only, per architecture.md §6 (Coordination pillar). Complements the identity/passport sketch in `db-entities.md`.

All task tables carry `project_id` for row-level scoping.

## tasks

- `id`
- `project_id` (FK to projects)
- `parent_task_id` (nullable FK to tasks.id, self-referential — encodes Project → Task → Subtask hierarchy per architecture.md §6)
- `title`
- `description`
- `status` (enum: `todo` / `wip` / `blocked` / `done`)
- `owner` (agent id, nullable)
- `priority`
- `due_date` (nullable)
- `created_at`
- `updated_at`

Status transitions emit `task.status_changed` events (architecture.md §6 key property — no separate sync step). Valid state-machine transitions (which statuses can transition to which others) are deferred to Day 8's `wormhole.task.update_status` implementation, not decided in this draft.

## task_links

- `id`
- `task_id` (FK to tasks)
- `link_type` (e.g. `kb_article` / `commit` / `pr` / `event`)
- `target_ref` (opaque string: KB article id, commit SHA, PR URL, or event id depending on link_type)

Links to KB articles, commits, PRs, and events go through `task_links`, not ad hoc columns (architecture.md §6).

---

## Design Notes

RFC-0001 §8.2 (Task Graph, Coordination pillar) does not specify exact column names, types, or nullable constraints — only the logical shape (hierarchy via parent_task_id, status transitions as events, links to external artifacts). This schema is a reasonable extension for Day 7's migration implementer to start from, not an RFC-literal transcription. The chosen column names, `status` enum values, and nullable fields reflect production conventions (timestamps, priority ordering, deferred due-date enforcement) and will be revisited during implementation if necessary.
