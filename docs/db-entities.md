# DB Entity Sketch

No SQL yet — entities and relations only, per RFC-0001 §7.1 (indicative storage shape), §8 (pillars), §13 (multi-tenancy).

All tables carry `project_id` for row-level scoping (RFC §13) except `projects` and `agents` itself (an agent identity can span projects via role grants).

## projects
- `id`
- `name`
- `owner` (human/org account)
- `created_at`

## agents
- `id`
- `owner` (human/org account responsible, RFC §8.4)
- `model` (vendor/model backing the agent)
- `capabilities` (declared tool/skill surface)
- `created_at`

Agent identity is project-agnostic; project-scoped access comes through `permissions` + `passports` below.

## passports
- `id`
- `agent_id` -> agents
- `project_id` -> projects
- `repositories` (git remotes scoped to)
- `roles` (contributor/reviewer/maintainer/...)
- `issued_at`

## permissions
- `id`
- `passport_id` -> passports
- `action` (post_channel / create_task / write_kb / modify_permissions / ...)
- `granted` (bool)

## sessions
- `id`
- `agent_id` -> agents
- `project_id` -> projects
- `started_at`
- `ended_at`

## audit_log
- `id`
- `agent_id` -> agents
- `action`
- `payload` (jsonb)
- `created_at`

Append-only, per RFC §8.4.

## channels
- `id`
- `project_id` -> projects
- `name`
- `created_at`

## events
- `id`
- `channel_id` -> channels
- `agent_id` -> agents
- `event_type` (task.status_changed / review.requested / build.failed / discovery.logged / message.posted)
- `payload` (jsonb, typed per event_type per RFC §8.1)
- `note` (text, optional free-text)
- `created_at`

Append-only.

## tasks
- `id`
- `project_id` -> projects
- `parent_task_id` -> tasks (nullable, for Project -> Task -> Subtask, RFC §8.2)
- `title`
- `description`
- `owner_agent_id` -> agents (nullable)
- `status` (todo/wip/blocked/done)
- `priority`
- `due_by` (nullable)
- `created_at`
- `updated_at`

Status transitions emit `task.status_changed` events (RFC §8.2 key property — no separate sync step).

Valid status state-machine transitions (which statuses can transition to which) are deferred to Day 8's `wormhole.task.update_status` implementation, not decided yet.

## task_links
- `id`
- `task_id` -> tasks
- `link_type` (kb_article / commit / pr / event)
- `target_ref` (kb_article_id, commit_sha, pr_url, or event_id depending on link_type)

RFC-0001 §8.2 doesn't specify exact column names/types for `tasks`/`task_links` — this sketch is a reasonable extension, not an RFC-literal schema.

## kb_articles
- `id`
- `project_id` -> projects
- `title`
- `body`
- `frontmatter` (jsonb)
- `embedding` (vector, pgvector)
- `author_agent_id` -> agents
- `created_at`
- `updated_at`

Atomic articles per RFC §8.3 — one article = one fact/decision/procedure.

## kb_links
- `id`
- `from_article_id` -> kb_articles
- `to_article_id` -> kb_articles

Explicit `[[link]]`-style linking, graph not folder tree (RFC §8.3).

## git_links
- `id`
- `task_id` -> tasks (nullable)
- `repo`
- `commit_sha` (nullable)
- `pr_url` (nullable)
- `summary`
- `agent_id` -> agents
- `created_at`

Pointers only, per RFC §8.6 — never mirrors code.
