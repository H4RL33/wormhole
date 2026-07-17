# Viewer Key Issuance

## Context

`internal/webui` serves a read-only human dashboard at `/dashboard/`
(RFC-0001 §14 V2), gated per-project by a viewer key
(`Authorization: Bearer <key>`, resolved via `identity.Store.ResolveViewerKey`).
`identity.Store.CreateViewerKey` (`internal/core/identity/viewer_keys.go`)
already issues these keys, but nothing calls it outside a test — there is no
server endpoint or CLI command to mint one. An operator today would have to
hand-write a Go call or a raw SQL insert (and get the SHA-256 hashing right
themselves) to give a human dashboard access at all.

This spec adds the missing issuance path: a server endpoint plus a CLI
command that calls it.

## Non-goal: real human auth

Gating *who* can issue a viewer key is itself an access-control decision.
RFC-0001/RFC-0002 don't yet define a human identity or credential model —
`Owner` on a Passport is a free-text string, not an authenticatable record
(tracked as its own future project, issue #22). Building that is out of
scope here. This spec gates issuance with a single shared operator secret
instead, tracked as a deliberate, retrofit-later stopgap (issue #23).

## Design

### Config

Add `AdminKey string` to `internal/types.Config`, loaded from
`WORMHOLE_ADMIN_KEY` (no default — empty string means "unset"). Follows the
existing `getEnv`/`LoadConfig` pattern in `internal/types/config.go`.

### Server endpoint

New handler in `internal/webui` (new file, e.g. `admin.go`, keeping
`api.go`'s read-only routes separate from this write route):

```
POST /dashboard/api/projects/{id}/viewer-keys
Header: X-Admin-Key: <shared secret>
Body:   {"label": "<string>"}
```

Registered on the same `*http.ServeMux` `NewMux` already returns, alongside
the three existing GET routes.

Auth check (`withAdminKey` middleware, mirroring `withViewerAuth`'s shape):
- If `Handler.AdminKey == ""`: respond 503, `{"error": "dashboard admin key not configured"}`.
- Compare the `X-Admin-Key` header against `Handler.AdminKey` with
  `crypto/subtle.ConstantTimeCompare`. Mismatch (including empty header):
  403, `{"error": "invalid admin key"}` — same generic shape for missing vs.
  wrong, consistent with `withViewerAuth`'s side-channel-neutral convention.

Handler body: decode `{"label": string}` (missing/empty body → 400,
`{"error": "label is required"}`); reject empty `{id}` path param the same
way the existing GET routes implicitly require it (400 if empty, matching
`NewMux`'s existing `{id}`-required invariant comment); call
`h.Identity.CreateViewerKey(ctx, projectID, label)`; DB error → 500,
`{"error": "failed to create viewer key"}`; success → 201,
`{"id": "...", "project_id": "...", "label": "...", "viewer_key": "<raw key>"}`.

`Handler` (the existing struct in `api.go`) gains one new field: `AdminKey
string`, set by `cmd/wormhole-server/main.go` from `cfg.AdminKey` alongside
the existing `Identity`/`Tasks`/`Events`/`KB` fields.

### CLI command

New `wormhole-cli viewer-key create` subcommand in `cmd/wormhole-cli`,
following the existing flag/dispatch pattern (`run`'s switch in `main.go`,
a new `runViewerKeyCreate` function):

```
wormhole-cli viewer-key create \
  --server http://localhost:8080 \
  --project <project-id> \
  --label "<human-readable label>" \
  --admin-key <shared secret, or read from $WORMHOLE_ADMIN_KEY if flag omitted>
```

Flags: `--server`, `--project`, `--label` (required); `--admin-key` (falls
back to `os.LookupEnv("WORMHOLE_ADMIN_KEY")` if not passed, erroring if
neither is set). Posts the JSON body, sets the `X-Admin-Key` header, prints
on success:

```
Viewer key created (id=<id>, project=<project-id>).
viewer_key=<raw key>
This key is shown once. Give it to the human who will use the dashboard,
as the Authorization: Bearer value at /dashboard/.
```

Non-2xx response: print the server's `{"error": ...}` message to stderr,
exit 1.

### README

Update the "## Human Dashboard" section (added by a prior doc-sync pass) to
replace its current "no CLI command to mint a viewer key yet" note with:
how to set `WORMHOLE_ADMIN_KEY` on `wormhole-server`, the
`wormhole-cli viewer-key create` example above, and how to then use the
returned key against `/dashboard/` (paste it as the page's viewer-key
input, or `curl -H "Authorization: Bearer <key>" .../dashboard/api/projects/{id}/tasks`).

## Testing

- `internal/webui`: real-Postgres integration test (per CONTRIBUTING's
  DB-testing rule — no mocking `database/sql`) covering: valid admin key
  succeeds and the returned `viewer_key` actually authenticates against one
  of the existing GET routes; wrong/missing `X-Admin-Key` → 403; `AdminKey`
  unset on the `Handler` → 503; missing `label` → 400.
- `cmd/wormhole-cli`: table-driven test for `runViewerKeyCreate` against an
  `httptest.Server`, matching existing CLI test conventions in
  `cmd/wormhole-cli/main_test.go` (flag validation, `--admin-key` vs
  `$WORMHOLE_ADMIN_KEY` fallback, success and non-2xx response paths).
- `go build ./... && go vet ./... && go test ./...` clean.

## Out of scope (tracked separately)

- Real human identity/auth replacing the shared secret (#22, #23).
- General MCP permission enforcement (#21) — unrelated; this endpoint isn't
  an MCP tool and doesn't touch `AuthenticatedScope.Permissions`.
- Revoking or listing existing viewer keys — only issuance is in scope here.
