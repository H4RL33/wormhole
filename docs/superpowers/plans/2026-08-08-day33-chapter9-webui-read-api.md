# Day 33 / Chapter 9 — Read-only Web UI API (M3, Chapters 9-11)

Source: `ROADMAP-ALPHA2.md` Chapter 9 (2026-08-08), part of M3 — Read-Only Web UI.
Ground truth: RFC-0001 §14 (V2 human-facing read dashboard, pulled forward into
alpha 2 per `ROADMAP-ALPHA2.md`'s scope-decision flags), §13 (human oversight role:
"can observe all channels/tasks/KB activity for projects they own"), §5.5 (every
*platform* capability ships as MCP — the dashboard is the RFC's own named exception,
not a violation of that rule), `docs/architecture.md` §5 M3 ("`/healthz` and similar
operational endpoints are the only exception" — the dashboard is a second,
RFC-sanctioned exception at the same tier, not a precedent for future REST creep).

**Scope of this chapter (roadmap text, verbatim):**
- New `internal/webui/api.go`: plain read-only JSON GET endpoints, no MCP/JSON-RPC —
  `/dashboard/api/projects/{id}/tasks`, `/events`, `/kb`.
- Human-facing auth: a project-scoped read-only viewer key, separate from agent
  bearer tokens — new `viewer_keys` table, migration.

**Explicitly NOT this chapter** (Chapter 10's scope, do not touch):
- No static HTML/dashboard page.
- No mounting `/dashboard` in `cmd/wormhole-server/main.go`. The API package ships
  unmounted; Chapter 10 wires it into the server, matching the precedent set by
  Chapter 2 (JSON-RPC envelope built, not yet mounted) → Chapter 3 (mounted).
- No hardening test asserting no POST/PUT/DELETE exists (Chapter 11).

**One correction to the roadmap text, decided here, do not re-litigate:**
The roadmap line names `migrations/000009_viewer_keys.up.sql`. That number is
already taken (`000009_kb_articles`, landed Chapter-13-era, before this roadmap
file's numbers were written). Migrations are sequential per `docs/architecture.md`
D1; the next free pair is **000011** (000010 is `role_templates`, Chapter 5). Use
`migrations/000011_viewer_keys.{up,down}.sql`. This is rung-3 (existing code beats
a stale doc reference), not an open decision — implementers must not ask about it.

## Global Constraints (bind every task below)

- Module boundaries (`docs/architecture.md` §2): `internal/webui` is a new package,
  sanctioned by this roadmap chapter itself (not a §0.3/R4 violation — the roadmap
  already named the package). It may import `internal/core/identity`,
  `internal/core/tasks`, `internal/core/events`, `internal/core/kb`, and
  `internal/types` — the same shape `cmd/wormhole-server` already uses to wire
  stores directly, just read-only. It must NOT import `internal/mcp` (this is a
  parallel read surface, not an MCP client) and core packages must not import
  `internal/webui` (R1's direction still holds: core never imports outward).
- Viewer-key storage and resolution lives in `internal/core/identity` (new file
  `internal/core/identity/viewer_keys.go`, same package — human oversight
  credentials are RFC-0001 §8.4/§13 identity-and-permissions territory, and this
  keeps `internal/webui` to a single auth import instead of a new cross-core
  package). Do not create a new `internal/core/*` package for this.
- Secrets hashed at rest (`docs/architecture.md` §3.6): viewer keys follow
  `agent_tokens`' exact pattern — SHA-256 hex hash stored, raw key returned exactly
  once at creation, never logged, never SELECTed back.
- Security-relevant lookups collapse to one error (§3.4): an unknown viewer key, a
  forged one, and a key that doesn't match the requested `{id}` project must all
  produce the same auth failure from the caller's point of view — do not let a
  timing- or message-distinguishable difference leak which case occurred.
- D3 (every project-scoped table gets RLS): `viewer_keys` needs
  `project_id uuid NOT NULL REFERENCES projects(id)`, an index on it,
  `ENABLE ROW LEVEL SECURITY`, and the standard
  `USING (project_id = current_setting('wormhole.project_id', true)::uuid)` policy,
  identical in shape to `agent_tokens`' policy (migration 000002).
- D2: update `docs/db-entities.md` with a `## viewer_keys` entry in the same task
  that adds the migration.
- T1/T4 (`docs/architecture.md` §7): all new store methods get real-Postgres tests
  (happy path + each sentinel error + the isolation property). `go build ./...`,
  `go vet ./...`, `go test ./...` must be clean before any task is reported done.
- Layering (`docs/architecture.md` §3): every new `Store` method takes
  `context.Context` first, wraps driver errors with `fmt.Errorf("<pkg>: <op>: %w", err)`,
  uses sentinel `Err...` vars for expected failure modes, hand-written SQL with `$n`
  placeholders — no ORM, no query builder.
- HTTP layer: stdlib `net/http` only (Go 1.26 `http.ServeMux` path-parameter
  patterns, e.g. `"GET /dashboard/api/projects/{id}/tasks"`), no router library.
- REST response shape: plain JSON arrays of the core packages' existing exported
  struct types (`tasks.Task`, `events.Event`, `kb.Article`), `json.Marshal`ed
  directly — no new DTO/envelope types invented for this chapter. Errors: JSON body
  `{"error": "<message>"}` with an appropriate HTTP status
  (401 unauthenticated/invalid key, 403 project mismatch, 404 unknown project id
  only if that's cheap to distinguish from 403 — otherwise 403 for both per the
  single-error-shape rule above; 500 for unexpected failures).

## Task 1 — `viewer_keys` migration + identity store methods

Files: `migrations/000011_viewer_keys.up.sql`, `migrations/000011_viewer_keys.down.sql`,
`internal/core/identity/viewer_keys.go`, `internal/core/identity/viewer_keys_test.go`,
`docs/db-entities.md`.

**Migration** (mirror `migrations/000002_agent_tokens.up.sql`'s shape exactly):

```sql
-- RFC-0001 §8.4/§13: human oversight role. A viewer key is a project-scoped,
-- read-only credential for the human-facing dashboard (RFC-0001 §14 V2, pulled
-- forward into Alpha 2 M3) — distinct from agent bearer tokens (agent_tokens),
-- never grants write access, never resolves to an agent identity.
-- Raw keys are never stored — only a SHA-256 hash, generated application-side
-- before insert.

CREATE TABLE viewer_keys (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id   uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    label        text NOT NULL,
    key_hash     text NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_viewer_keys_project_id ON viewer_keys(project_id);

ALTER TABLE viewer_keys ENABLE ROW LEVEL SECURITY;
CREATE POLICY viewer_keys_project_isolation ON viewer_keys
    USING (project_id = current_setting('wormhole.project_id', true)::uuid);
```

Down migration: `DROP TABLE IF EXISTS viewer_keys;`

`label` is a human-readable name for the key (e.g. "Harley's laptop") so a project
owner can tell multiple issued keys apart — same rationale as Chapter 8's credential
profiles, applied to the human side. No `revoked_at`/expiry column: nothing in RFC-0001
or this roadmap chapter asks for viewer-key revocation or expiry; do not add one
(§0.5 smallest correct diff — `agent_tokens` has `expires_at` because RFC-0001 §13
requires token expiry for agents specifically; no equivalent text exists for viewer
keys). If this gap matters, flag it in your completion report as a rung-6 escalation,
don't silently add the column.

**`internal/core/identity/viewer_keys.go`** — same file layout as `identity.go`
(package `identity`, same `Store` receiver, so it shares `s.db`):

```go
package identity

import (
    "context"
    "crypto/sha256"
    "database/sql"
    "encoding/hex"
    "errors"
    "fmt"
)

// ErrInvalidViewerKey is returned by ResolveViewerKey for any key that doesn't
// match a stored hash for the requested project — forged, unknown, or a real
// key presented against a project it wasn't issued for all collapse to this
// one error (docs/architecture.md §3.4: security-relevant lookups must not let
// a caller distinguish failure modes).
var ErrInvalidViewerKey = errors.New("identity: invalid viewer key")

// CreateViewerKey issues a new project-scoped read-only viewer key. The raw
// key is returned exactly once; only its SHA-256 hash is persisted.
func (s *Store) CreateViewerKey(ctx context.Context, projectID, label string) (rawKey string, id string, err error) {
    rawKey, keyHash, err := generateToken()
    if err != nil {
        return "", "", err
    }

    row := s.db.QueryRowContext(ctx,
        `INSERT INTO viewer_keys (project_id, label, key_hash) VALUES ($1, $2, $3) RETURNING id`,
        projectID, label, keyHash,
    )
    var newID string
    if err := row.Scan(&newID); err != nil {
        return "", "", fmt.Errorf("identity: create viewer key: %w", err)
    }
    return rawKey, newID, nil
}

// ResolveViewerKey resolves a raw viewer key to the project it grants
// read-only access to. Returns ErrInvalidViewerKey if the key doesn't match
// any stored hash, or if projectID is non-empty and doesn't match the key's
// own project (cross-tenant isolation, same principle as identity.WhoAmI).
func (s *Store) ResolveViewerKey(ctx context.Context, projectID, rawKey string) (resolvedProjectID string, err error) {
    if rawKey == "" {
        return "", ErrInvalidViewerKey
    }
    sum := sha256.Sum256([]byte(rawKey))
    hash := hex.EncodeToString(sum[:])

    var gotProjectID string
    err = s.db.QueryRowContext(ctx,
        `SELECT project_id FROM viewer_keys WHERE key_hash = $1`,
        hash,
    ).Scan(&gotProjectID)
    if errors.Is(err, sql.ErrNoRows) {
        return "", ErrInvalidViewerKey
    }
    if err != nil {
        return "", fmt.Errorf("identity: resolve viewer key: %w", err)
    }
    if projectID != "" && gotProjectID != projectID {
        return "", ErrInvalidViewerKey
    }
    return gotProjectID, nil
}
```

`generateToken()` already exists in `identity.go` (used by `IssueToken`) and returns
`(rawToken, tokenHash string, err error)` — reuse it verbatim, do not duplicate its
random-generation logic.

**Tests** (`viewer_keys_test.go`, real Postgres per T1, follow
`identity_test.go`'s setup helpers): create a viewer key for project A, resolve it
with `projectID=""` (succeeds, returns A), resolve it with `projectID=A` (succeeds),
resolve it with `projectID=B` (returns `ErrInvalidViewerKey` — this is the isolation
test T3 requires), resolve a garbage/unknown raw key (returns `ErrInvalidViewerKey`),
resolve an empty string (returns `ErrInvalidViewerKey`).

**`docs/db-entities.md`**: add, after the existing `## permissions` section (or
wherever alphabetically/logically consistent with the file's existing order):

```markdown
## viewer_keys
- `id`
- `project_id` -> projects
- `label` (human-readable name for the key)
- `key_hash` (SHA-256, raw key shown once at creation)
- `created_at`
```

Report: DONE / DONE_WITH_CONCERNS / NEEDS_CONTEXT / BLOCKED, commits, test command +
output summary, any concerns.

## Task 2 — core store read-all methods needed by the dashboard

Files: `internal/core/kb/kb.go`, `internal/core/kb/kb_test.go`,
`internal/core/events/events.go`, `internal/core/events/events_test.go`.

The dashboard's `/kb` and `/events` endpoints need to list *all* rows for a project;
neither core package currently has that. `kb.Store` only has `SearchArticles`
(requires a query + embedding similarity) and `GetArticle`/`GetArticleLinks` (single
article by id). `events.Store.ListEvents` requires a specific `channelID` (verified
in `internal/core/events/events.go:218`). Add the minimal new method each package is
missing — do not touch `tasks.Store.List`, it already does what's needed
(`List(ctx, projectID, status *string)` with `status = nil` returns all tasks).

**`kb.Store.ListArticles`** — mirror `SearchArticles`'s auth/RLS setup
(`kb.go:359`) but without the embedding/similarity math, ordered by `created_at`:

```go
// ListArticles returns every KB article in the project, newest first. Unlike
// SearchArticles this has no query/similarity component — it's the plain
// listing the read-only dashboard needs (Alpha-2 Chapter 9).
func (s *Store) ListArticles(ctx context.Context, projectID string) ([]Article, error) {
```

Set `wormhole.project_id` via `set_config` exactly as `SearchArticles` does, then
`SELECT <article columns> FROM kb_articles WHERE project_id = $1 ORDER BY created_at DESC`.
Read `SearchArticles` first for the exact column list / scan order to reuse
verbatim — do not invent a different column set.

**`events.Store.ListEventsByProject`** — mirror `ListEvents`'s transaction/RLS
shape (`events.go:218`) but across all channels in the project, no channel-existence
check needed (there's no single channel to verify):

```go
// ListEventsByProject returns events across every channel in the project,
// newest first, for the read-only dashboard (Alpha-2 Chapter 9). Unlike
// ListEvents this is not scoped to one channel.
func (s *Store) ListEventsByProject(ctx context.Context, projectID string, limit, offset int) ([]Event, error) {
```

Same tx + `set_config` pattern as `ListEvents`, query
`SELECT <eventColumns> FROM events WHERE project_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`
(reuse the existing `eventColumns` constant, same scan order as `ListEvents`).

**Tests**: for each new method, a happy-path test (seed 2-3 rows across
projects/channels, confirm the right set + order comes back) and a cross-project
isolation test (project A's list never contains project B's rows) — T3.

Report: DONE / DONE_WITH_CONCERNS / NEEDS_CONTEXT / BLOCKED, commits, test command +
output summary, any concerns.

## Task 3 — `internal/webui/api.go`: the three GET endpoints + viewer-key middleware

Files: `internal/webui/api.go`, `internal/webui/api_test.go`.

Depends on Task 1 (`identity.Store.ResolveViewerKey`) and Task 2
(`kb.Store.ListArticles`, `events.Store.ListEventsByProject`) — both must be
complete and committed before this task starts.

```go
package webui

import (
    "encoding/json"
    "net/http"
    "strconv"

    "github.com/H4RL33/wormhole/internal/core/events"
    "github.com/H4RL33/wormhole/internal/core/identity"
    "github.com/H4RL33/wormhole/internal/core/kb"
    "github.com/H4RL33/wormhole/internal/core/tasks"
)

// Handler serves the read-only dashboard API (RFC-0001 §14 V2, pulled forward
// into Alpha-2 M3). It is a plain JSON REST surface, not MCP/JSON-RPC — see
// docs/architecture.md §5 M3, which names the human read-only dashboard as an
// RFC-sanctioned exception to "every capability is an MCP tool", not a
// precedent for further REST endpoints.
type Handler struct {
    Identity *identity.Store
    Tasks    *tasks.Store
    Events   *events.Store
    KB       *kb.Store
}

// NewMux returns the dashboard API's routes, unmounted — Chapter 10 mounts
// this under /dashboard in cmd/wormhole-server/main.go.
func (h *Handler) NewMux() *http.ServeMux {
    mux := http.NewServeMux()
    mux.HandleFunc("GET /dashboard/api/projects/{id}/tasks", h.withViewerAuth(h.listTasks))
    mux.HandleFunc("GET /dashboard/api/projects/{id}/events", h.withViewerAuth(h.listEvents))
    mux.HandleFunc("GET /dashboard/api/projects/{id}/kb", h.withViewerAuth(h.listKB))
    return mux
}

// withViewerAuth resolves the Authorization: Bearer <key> header against
// identity.Store.ResolveViewerKey, scoped to the {id} path param's project.
// Any failure (missing header, unknown key, key belongs to a different
// project) returns the same 403 JSON error — docs/architecture.md §3.4's
// single-error-shape rule applies to this human-facing boundary too.
func (h *Handler) withViewerAuth(next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        projectID := r.PathValue("id")
        token := extractBearerToken(r.Header.Get("Authorization"))
        if _, err := h.Identity.ResolveViewerKey(r.Context(), projectID, token); err != nil {
            writeJSONError(w, http.StatusForbidden, "invalid or unauthorized viewer key")
            return
        }
        next(w, r)
    }
}
```

Implement `extractBearerToken` (parse `"Bearer <token>"`, return `""` if the header
is missing or malformed — do not panic or 500 on a malformed header, that's still an
auth failure, not a server error) and `writeJSONError(w, status, message string)`
(`{"error": message}`, matching `Content-Type: application/json`).

Handlers:

```go
func (h *Handler) listTasks(w http.ResponseWriter, r *http.Request) {
    projectID := r.PathValue("id")
    result, err := h.Tasks.List(r.Context(), projectID, nil)
    if err != nil {
        writeJSONError(w, http.StatusInternalServerError, "failed to list tasks")
        return
    }
    writeJSON(w, result)
}
```

Same shape for `listEvents` (call `h.Events.ListEventsByProject(r.Context(), projectID, 100, 0)`
— hardcode `limit=100, offset=0` for this chapter; pagination via query params is
not asked for by the roadmap text, don't add it) and `listKB` (call
`h.KB.ListArticles(r.Context(), projectID)`).

`writeJSON(w http.ResponseWriter, v any)`: set `Content-Type: application/json`,
`json.NewEncoder(w).Encode(v)`; a nil slice from any of the three List calls should
serialize as `[]` not `null` — check each Store method already normalises nil to
`[]T{}` before return (per §3.7's jsonb convention, the same normalisation applies
here); if a method can return a bare `nil` slice on the empty-result path, normalise
it in the handler rather than changing the store method's contract.

**Tests** (`api_test.go`, real Postgres — this package wires real stores, no
mocking per T1): seed a project via the existing store constructors directly (same
pattern `internal/mcp`'s integration tests use to seed state — read one of
`m1/m2/m3_integration_test.go` for the seeding helpers before writing this), issue a
viewer key for it, hit all three routes via `httptest.NewServer(h.NewMux())` with the
key in the `Authorization` header, assert the JSON body matches what was seeded.
Also test: missing `Authorization` header → 403; a viewer key from project B against
project A's `{id}` → 403; wrong/garbage key → 403.

Report: DONE / DONE_WITH_CONCERNS / NEEDS_CONTEXT / BLOCKED, commits, test command +
output summary, any concerns.

## After all three tasks

- Full suite green: `go build ./...`, `go vet ./...`, `go test ./...`.
- Check off Chapter 9's two roadmap bullets in `ROADMAP-ALPHA2.md`.
- Do NOT check off Chapter 10 or 11 bullets, and do not start mounting `/dashboard`
  in `cmd/wormhole-server/main.go` — that's explicitly out of scope here (see
  "Explicitly NOT this chapter" above).
