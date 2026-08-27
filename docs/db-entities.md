# Optional Fabric DB Entity Sketch

**Status:** retained PostgreSQL/server model, not the live Stage 2 Gateway
surface. Stage 2 portable state is `.wormhole/state/v1/`; Gateway operational
and machine-private state is schema-v7 SQLite. The local-only Gateway does not
enrol with, bootstrap from, search, mutate Tasks through, or sync to Fabric.

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

## agent_enrolments
- `project_id` -> projects
- `idempotency_key` (project-scoped attempt identifier)
- `request_hash` (SHA-256 of the canonical request; no raw credential)
- `state` (`registration_in_progress` / `registered`)
- `agent_id` -> agents (set after registration)
- `passport_id` -> passports (set after registration)
- `token_id` -> agent_tokens (hash-bearing credential reference only)
- `reissue_count` (bounded recovery counter; maximum one)
- `created_at`
- `updated_at`

The `(project_id, idempotency_key)` primary key and transaction-scoped advisory
lock serialize retries. A matching digest replays the same identity references;
a different digest conflicts. This table has project RLS with both `USING` and
`WITH CHECK`. Raw Passport tokens are never stored here.

The historical/future optional-Fabric Gateway design separately stores a local SQLite `enrolment_attempts` checkpoint keyed
by `(project_id, idempotency_key)`, with one active row per credential profile.
It contains the canonical request hash, lifecycle state, profile identifier,
and optional agent/Passport references only. This local row is committed before
Fabric contact and permits a later CLI process or restarted Gateway to resume
the original attempt key; it has no token column.

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

These server Task rows and transitions belong to the optional Fabric inventory;
they do not imply a live Gateway Task tool.

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
- `embedding` (legacy nullable vector; compatibility-only after migration 19)
- `author_agent_id` -> agents
- `bootstrap_key` (nullable; partial uniqueness within a project is reserved
  for fixed system/bootstrap articles and does not constrain ordinary titles)
- `created_at`
- `updated_at`

Atomic articles per RFC §8.3 — one article = one fact/decision/procedure.

## kb_embedding_generations
- `id`
- `project_id` -> projects
- `provider`
- `model`
- `version`
- `dimension` (1024)
- `state` (building / active / failed / retired)
- `failure_code` (nullable, safe machine code only)
- lifecycle timestamps

At most one generation is active per project. A building generation is
activated only when complete; the previous active generation becomes retired
without deleting its vectors.

## kb_article_embeddings
- `project_id` -> projects
- `article_id` -> kb_articles
- `generation_id` -> kb_embedding_generations
- `provider`
- `model`
- `version`
- `dimension` (1024)
- `content_hash` (SHA-256 of the exact `title + "\n\n" + body` provider input)
- `embedding` (vector(1024), pgvector)
- `created_at`

Model metadata is bound to the generation by a composite foreign key. New
writes and semantic ranking use this table, not the legacy article column.
Activation verifies both exact article membership and every content hash, so a
same-count candidate containing a stale vector cannot become active.

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

## integration_manifest_lineages

- `project_id` -> projects
- `manifest_id`
- `active`
- `created_by_agent_id` -> agents
- `created_at`

At most one lineage is active per project. Fabric stores and distributes
declarative offers; it is not the approval or repository-application authority.

## integration_manifest_versions

- `(project_id, manifest_id)` -> integration_manifest_lineages
- `manifest_version`
- `schema_version` (1)
- `source` (`fabric`)
- `authored_at`
- `tool_contract_digest`
- `manifest_digest`
- `role_filters` (jsonb)
- `entries` (jsonb declarative content)
- authenticated publication and revocation agent IDs/timestamps

The manifest body and publication metadata are immutable after insertion. A
revocation may be recorded once; immutable version history is retained. Both
manifest tables are project-scoped with RLS.

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

Role templates are optional Fabric-side policy data for server workflows. The
current public CLI and Stage 2 Gateway expose no role-template enrolment
shortcut or live enrolment route. Missing authority is never inferred from a
CLI default.
