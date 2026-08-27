# Optional Fabric DB Entity Sketch

**Status:** PostgreSQL migrations 1–20 are the retained server baseline.
Migration 21 adds non-authoritative Git-aware stream replicas and a separate
finite-retention Activity authority. Stage 2 portable state remains
`.wormhole/state/v1/`; the Gateway private database remains exact schema v7
until the separately scoped Stage 3 private-format task lands.

Git remains the sole acceptance authority for portable project state. Fabric
stores replica/proposal evidence and operational Activity; neither can advance
an accepted Git ref by itself.

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

## Migration 21: Git-aware portable replicas

The portable relations are deliberately non-authoritative. Their keys are
project-first and include the complete Fabric, stream, ref, and workspace
identity needed by each relationship:

- `project_repository_bindings` binds
  `(project_id, fabric_instance_id)` to an immutable provider repository ID,
  canonical remote, default branch, visibility, and optional private observer
  credential reference.
- `fabric_streams` is keyed by
  `(project_id, fabric_instance_id, stream_id, canonical_ref)` and requires
  `canonical_ref == ref_name`. It stores the current replica version and exact
  live/accepted tree and Git-commit digests.
- `fabric_stream_versions` retains every canonical live tree, canonical
  accepted tree, digest, commit, and transition. Operation transitions alone
  contain an operation UUID, canonical operation bytes/digest, and exact actor
  envelope. Initial and accepted-ref transitions contain none of those four
  operation fields. Version history is immutable.
- `fabric_workspace_stream_bindings` binds an authenticated attachment and
  repository identity to the complete stream/workspace/ref identity. Both
  `(project_id, fabric_instance_id, stream_id, workspace_id, ref_name)` and its
  canonical-ref equivalent are unique and are the parents of requests and
  Activity origins.
- `fabric_stream_requests` stores immutable canonical operation requests and
  their applied/conflict/rejected result under the complete workspace/ref key.
- `fabric_stream_conflicts` binds conflict evidence to an exact retained stream
  version.
- `fabric_public_actor_keys` binds key continuity to an exact stream version;
  `public_request_nonces` binds nonce replay evidence to that complete actor-key
  identity.

Every composite foreign key carries `project_id` first. Cross-project,
cross-Fabric, cross-stream, cross-ref, and cross-workspace combinations therefore
fail as foreign-key violations rather than being inferred or normalized.

## Migration 21: Fabric Activity

Activity is operational evidence, not portable state. Presence is memory-only
and cannot enter any PostgreSQL relation. Durable `ordinary` and `lifecycle`
records use these relations:

- `fabric_activity_policy_versions` retains immutable canonical policy bytes,
  digest, the fixed V1 ordinary/default/maximum values, and one effective finite
  terminal-retention value. `fabric_activity_policy_current` points to one exact
  version for a complete stream/ref key.
- `fabric_activity_stream_sequences` owns a positive, monotonically increasing,
  JSON-safe Activity high watermark per complete stream/ref. It is independent
  of `fabric_streams.current_version`.
- `fabric_activities` is the immutable ledger. Its complete origin key is
  `(project_id, fabric_instance_id, stream_id, canonical_ref,
  source_workspace_id, activity_id)`. It retains canonical Activity and actor
  bytes, digest, exact optional event/lifecycle projections, Activity time,
  server acceptance time, and a unique stream Activity sequence.
- `fabric_activity_ingress_receipts` is an immutable replay companion. Its
  digest, sequence, policy pair, and acceptance time composite-reference the
  exact ledger and policy evidence.
- `fabric_activity_lifecycle` is the sole mutable Activity relation. Multiple
  delivery/conflict/recovery/receipt claims may protect one ledger record. Each
  row captures its ingress policy pair and finite terminal retention; the first
  terminal transition captures database transaction time and derives expiry.

The exact state machines are `delivery: pending -> delivered|cancelled`,
`conflict: open -> resolved|cancelled`, `recovery: pending ->
blocked|recovered|cancelled; blocked -> pending|recovered|cancelled`, and
`receipt: pending -> confirmed|rejected|cancelled`. Exact replays are read-only.

Ordinary Activity with no lifecycle row is eligible when it is at least 30 days
old **or** outside the newest 10,000 unprotected rows for its source workspace.
Ranking is `(created_at DESC, activity_id DESC)` and bounded deletion is oldest
first by `(created_at, activity_id)`. Activity with lifecycle rows is eligible
only after every claim is terminal and the greatest captured expiry has passed.
Pruning is limited to 1–1,000 rows and deletes lifecycle/receipt children before
the ledger in one transaction. Protected rows may exceed the cap.

Only these four fixed-search-path, security-definer functions mutate Activity:

- `fabric_accept_activity_v1`
- `fabric_transition_activity_lifecycle_v1`
- `fabric_prune_activities_v1`
- `fabric_publish_activity_policy_v1`

Deployment pre-provisions `wormhole_activity_owner` (NOLOGIN),
`wormhole_fabric_runtime` (the process login), and
`wormhole_activity_maintenance` (NOLOGIN). Migration 21 validates but never
creates, alters, or drops those cluster roles. The owner owns only Activity
relations/functions and has the minimum parent-table privileges needed to take
portable binding locks. Runtime receives Activity `SELECT` and execution of
accept/transition/policy publication, with no direct mutable-table DML.
Maintenance receives pruner execution only.

Every migration-21 project table has enabled and forced RLS. Both `USING` and
`WITH CHECK` use exactly
`project_id = NULLIF(current_setting('wormhole.project_id',true),'')::uuid`, and
each store transaction sets that GUC locally before reading or writing.

There is no Fabric promotion table, column, function, trigger, or method. Activity
expiry cannot create an Event/Operation, mutate a portable tree, advance a Git
ref, or mark a proposal accepted. Any later promotion is a separate Gateway-local
ProjectState transaction and remains subject to ordinary Git review.

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
