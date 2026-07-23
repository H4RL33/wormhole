# DB Entity Sketch

No SQL yet — entities and relations only, per RFC-0001 §7.1 (indicative storage shape), §8 (pillars), §13 (multi-tenancy).

All tenant tables use row-level project scoping (RFC §13). Child tables carry
`project_id`; the `projects` root scopes on `id`. `agents` is
project-agnostic because an agent identity can span projects via Passports;
`role_templates` is global configuration applied during registration. Those
are the only application tables without tenant RLS.

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
- `action` (`task.create` / `channel.post` / `kb.write` / other exact
  `Tool.RequiredPermission` values)
- `granted` (bool)

## viewer_keys
- `id`
- `project_id` -> projects
- `label` (human-readable name for the key)
- `key_hash` (SHA-256, raw key shown once at creation)
- `created_at`

## sessions
- `id`
- `agent_id` -> agents
- `project_id` -> projects
- `started_at`
- `ended_at`

## audit_log
- `id`
- `agent_id` -> agents
- `project_id` -> projects
- `action`
- `payload` (jsonb)
- `created_at`

Append-only, per RFC §8.4.

`audit_log` uses forced PostgreSQL RLS with both `USING` and `WITH CHECK`.
Production database credentials must not be superusers or hold `BYPASSRLS`.

## channels
- `id`
- `project_id` -> projects
- `name` (unique within a project)
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
- `project_id` -> projects
- `task_id` -> tasks
- `link_type` (kb_article / commit / pr / event)
- `target_ref` (kb_article_id, commit_sha, pr_url, or event_id depending on link_type)

RFC-0001 §8.2 doesn't specify exact column names/types for `tasks`/`task_links` — this sketch is a reasonable extension, not an RFC-literal schema.

`project_id` added Day 7 (deviation from the original Day 1/Day 6 sketch above): D3 requires a `project_id` + RLS policy on every project-scoped table, and `task_links` had none in the original sketch.

## kb_articles
- `id`
- `project_id` -> projects
- `title`
- `body`
- `frontmatter` (jsonb)
- `embedding` (vector, pgvector)
- `author_agent_id` -> agents
- `bootstrap_key` (nullable; partial uniqueness within a project is reserved
  for fixed system/bootstrap articles and does not constrain ordinary titles)
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

## role_templates

Stores role definitions and their default capabilities, roles, and permissions. Used during agent registration to auto-fill Passport fields when a role is specified.
This is a global configuration table, not tenant data, so it intentionally has
no `project_id` or RLS policy.

| Column | Type | Notes |
|--------|------|-------|
| role | varchar(255) | primary key; e.g., "backend-engineer", "frontend-engineer" |
| default_capabilities | text[] | capabilities assigned by default to agents with this role; e.g., ["read", "write"] |
| default_roles | text[] | roles assigned by default (e.g., ["agent"]); typically at least one role is required |
| permissions | jsonb | permissions (e.g., `{"kb": "read-write", "kb_feedback": "read-write"}`) |

Example row:

```json
{
  "role": "backend-engineer",
  "default_capabilities": ["read", "write"],
  "default_roles": ["agent"],
  "permissions": { "kb": "read-write", "kb_feedback": "read-write", "tasks": "create" }
}
```

When `wormhole join --role backend-engineer` is run (without explicit `--capabilities` or `--roles` flags),
the agent inherits `default_capabilities` and `default_roles` from the template, reducing flag verbosity
for common roles. Explicit flags always override template defaults.
