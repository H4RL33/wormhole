# Chapter 5 — Role Template Schema

Source: ROADMAP-ALPHA2.md Chapter 5 (M2 — Role System). Authority order for
this plan: RFC-0001 > RFC-0002 > docs/architecture.md > existing code.

## Context

RFC-0001 §8.4 defines Roles only as free-text `contributor / reviewer /
maintainer` tags on a Passport, no behavior attached. `alpha-2.md`'s
backend/frontend/PM team roles with permission bundles are an **alpha-2-only
extension**, not an RFC requirement (see ROADMAP-ALPHA2.md's scope-decision
flags). This chapter only adds the storage + read path for role templates;
it does NOT wire role resolution into `issuePassport` or `wormhole join`
(that's Chapter 6) and does NOT touch `wormhole.task.list` (Chapter 7).

Existing passports already carry a free-text `roles []string` column
(`internal/core/identity/identity.go`, migration 000001) — untouched by this
chapter. Role templates are a new, separate lookup table; nothing here
changes how passports are issued or stored.

Next available migration number: `000010` (000009 is the last one in the
repo — `kb_articles`; ROADMAP-ALPHA2.md's own suggested number 000008 is
stale, that slot is taken by `git_links`).

## Global Constraints

- New package `internal/core/roles`, isolated per architecture.md R2: does
  not import `internal/core/tasks`, `internal/core/events`, or
  `internal/core/kb`. May use `database/sql` directly against the new table,
  same pattern as `kb.go`'s raw-SQL link checks.
- Migration pair: `migrations/000010_role_templates.up.sql` /
  `.down.sql`.
- Table `role_templates`:
  - `name TEXT PRIMARY KEY`
  - `permission_bundle JSONB NOT NULL` — list of permission strings
  - `default_task_view JSONB NOT NULL` — filter object for Chapter 7's
    task-list default view (shape: `{"status": [...], "assignee": "self"|null}`
    — keep it a simple free-form JSONB, no Go struct validation beyond
    valid-JSON in this chapter, since Chapter 7 is the first real consumer)
  - `created_at TIMESTAMPTZ NOT NULL DEFAULT now()`
- Seed rows (`INSERT` in the `.up.sql` migration, not code):
  - `backend-engineer` — permission bundle: `["task.read", "task.write",
    "kb.read", "kb.write", "channel.read", "channel.write"]`; default view:
    `{"status": ["todo", "in_progress"], "assignee": "self"}`
  - `frontend-engineer` — same shape, same permission bundle as
    `backend-engineer` (alpha-2 doesn't differentiate backend/frontend
    permissions, only task-view framing) — default view:
    `{"status": ["todo", "in_progress"], "assignee": "self"}`
  - `project-manager` — permission bundle: `["task.read", "task.write",
    "kb.read", "kb.write", "channel.read", "channel.write", "task.assign"]`;
    default view: `{"status": [], "assignee": null}` (sees everything)
  - `contributor`, `reviewer`, `maintainer` — the existing RFC-0001 §8.4
    free-text tags, seeded here too so `roles.Store.GetTemplate` has one
    lookup path for both alpha-2 role names and RFC tags. Permission
    bundles: `contributor` = `["task.read", "task.write", "kb.read",
    "kb.write", "channel.read", "channel.write"]`, `reviewer` =
    `["task.read", "kb.read", "kb.write", "channel.read", "channel.write"]`,
    `maintainer` = same bundle as `project-manager`. Default view for all
    three: `{"status": [], "assignee": null}`.
- Go API (`internal/core/roles/roles.go`):
  ```go
  package roles

  type Template struct {
      Name             string
      PermissionBundle []string
      DefaultTaskView  json.RawMessage
      CreatedAt        time.Time
  }

  var ErrTemplateNotFound = errors.New("roles: template not found")

  type Store struct { db *sql.DB }

  func NewStore(db *sql.DB) *Store
  func (s *Store) GetTemplate(ctx context.Context, name string) (Template, error)
  func (s *Store) ListTemplates(ctx context.Context) ([]Template, error)
  ```
  `GetTemplate` returns `ErrTemplateNotFound` (wrapped, matching
  `identity.ErrInvalidToken`-style sentinel errors) when `name` has no row.
  `ListTemplates` orders by `name` ascending for deterministic test
  assertions.
- No MCP tool, no HTTP route, no CLI flag in this chapter — pure storage +
  Go read API. Chapter 6 wires `GetTemplate` into passport issuance.

## Task 1 — Migration + roles package

Files:
- `migrations/000010_role_templates.up.sql`
- `migrations/000010_role_templates.down.sql`
- `internal/core/roles/roles.go`
- `internal/core/roles/roles_test.go`

Requirements:
1. `.up.sql` creates `role_templates` per the schema above and seeds all six
   rows in the same migration (idempotent `INSERT ... ON CONFLICT DO
   NOTHING` not required — migrations run once, plain `INSERT` is fine,
   matching this repo's existing migration style — check
   `migrations/000009_kb_articles.up.sql` for the seeding convention if any
   exists, otherwise plain `INSERT` statements).
2. `.down.sql` drops the table.
3. `roles.go` implements `Store`, `Template`, `ErrTemplateNotFound`,
   `NewStore`, `GetTemplate`, `ListTemplates` exactly as specified above.
4. Tests use the repo's existing Postgres test-DB setup convention (check
   `internal/core/kb/kb_test.go` or `internal/core/identity/identity_test.go`
   for how tests get a `*sql.DB` and run migrations — reuse that helper,
   don't invent a new one). Cover: `GetTemplate` for a seeded name returns
   the expected permission bundle; `GetTemplate` for an unknown name returns
   `ErrTemplateNotFound`; `ListTemplates` returns all six seeded templates
   ordered by name.
5. Run `go build ./...` and `go test ./internal/core/roles/...` before
   committing.

This is Chapter 5's entire scope — one task, no second task needed (the
roadmap lists two bullets: migration+seed, and the Go package; both land
together since the package is untestable without the migration).
