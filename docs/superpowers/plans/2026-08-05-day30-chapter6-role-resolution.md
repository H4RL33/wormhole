# Chapter 6 — Role Resolution at Passport Issuance

Source: ROADMAP-ALPHA2.md Chapter 6 (M2 — Role System). Authority order for
this plan: RFC-0001 > RFC-0002 > docs/architecture.md > existing code.

## Context

Chapter 5 (commits b7c2f99..d0a976d) built `internal/core/roles`: a
`role_templates` table plus `Store.GetTemplate(name)` /
`Store.ListTemplates()`, seeded with `backend-engineer`,
`frontend-engineer`, `project-manager`, `contributor`, `reviewer`,
`maintainer`. It deliberately did not wire role resolution into passport
issuance or `wormhole join` — that's this chapter.

Passports already carry a free-text `roles []string` tag column
(`internal/core/identity/identity.go`, migration 000001) via
`RegisterAgentInput.Roles` (`internal/mcp/agent.go`) and the CLI's
`--roles` (plural) flag (`cmd/wormhole-cli/main.go`). That path is
untouched by this chapter. Chapter 6 adds a **new, separate concept**: a
single named role template (`--role`, singular) that, when given, is
looked up in `role_templates` and (a) its permission bundle is folded into
the token's granted permissions, (b) its name is added to the same
`passports.roles` tag array the existing `--roles` flag already writes to.

**Deviation from the roadmap's literal wording, flagged per plan
convention:** the roadmap text says this "extends `issuePassport` in
`internal/core/identity/identity.go`". That's not achievable as literal
code motion: `docs/architecture.md` §2 rule R2 forbids
`internal/core/identity` importing `internal/core/roles` (cross-core
imports banned, no sanctioned exception for this pair). Resolution instead
happens one layer up, in `internal/mcp/agent.go`'s `RegisterAgentTool`
handler, which already imports both `internal/core/identity` and can take
`internal/core/roles` as a new constructor argument — it resolves the
template, then calls `identity.Store.Register` with the merged
permissions/roles slices. `issuePassport` itself does not change. The
outcome the roadmap bullet cares about — role template resolved and
applied at passport issuance time, still using the existing
`passports.roles` column — is preserved exactly; only the layer doing the
resolution moves to respect R2.

**Sentinel error:** the roadmap names `ErrRoleTemplateNotFound`. Chapter 5
already defined `roles.ErrTemplateNotFound` for exactly this "unknown
template name" case. This plan reuses that existing sentinel rather than
adding a near-duplicate — same failure mode, same package, no behavior
difference. Do not add a second sentinel.

**Permission merge semantics (this plan's decision, not RFC-specified):**
when `--role`/`role` resolves to a template, the effective permission set
passed to `identity.Store.Register` is the **union** of the caller-supplied
`permissions` and the template's `PermissionBundle`, deduplicated. Union
(not override) so a caller can still ask for extra permissions beyond the
role's baseline without the role silently downgrading them, and so the
role's baseline is never silently dropped either. This only applies to
`wormhole.agent.register` / `wormhole join`; `wormhole connect` is out of
scope (roadmap Chapter 6 bullet only names `wormhole.agent.register` and
`wormhole join --role`).

Registration only — `identity.IssuePassport` (used to add a second
passport for an existing agent) is not exposed as an MCP tool today (see
`internal/mcp/agent.go` — only `RegisterAgentTool` and `WhoAmITool`
exist). Out of scope for this chapter.

## Global Constraints

- No new migration. Reuses Chapter 5's `role_templates` table as-is.
- `internal/core/roles` package is unchanged (its API from Chapter 5 —
  `Store.GetTemplate`, `roles.ErrTemplateNotFound` — is sufficient).
- `internal/core/identity` package is unchanged — R2 forbids it importing
  `internal/core/roles`, and no new behavior is needed there since
  `issuePassport`/`Register` already accept arbitrary `roles []string` and
  `permissions []string`.
- `internal/mcp/agent.go`:
  - `RegisterAgentTool` gains a new parameter:
    `func RegisterAgentTool(store *identity.Store, eventsStore *events.Store, rolesStore *roles.Store) Tool`
  - `RegisterAgentInput` gains `Role string \`json:"role,omitempty"\`` (singular — distinct field from the existing `Roles []string`).
  - `RegisterAgentOutput` gains `Role string \`json:"role,omitempty"\`` echoing back the resolved template name (empty string when none was given), so a caller can confirm what was applied.
  - Handler logic when `in.Role != ""`:
    1. `template, err := rolesStore.GetTemplate(ctx, in.Role)`
    2. `errors.Is(err, roles.ErrTemplateNotFound)` → return that error
       wrapped with context (`fmt.Errorf("mcp: wormhole.agent.register: unknown role template %q: %w", in.Role, err)`),
       matching this file's existing wrap style. Any other error from
       `GetTemplate` also returns wrapped, same as other DB-error paths in
       this file.
    3. On success: merge `template.PermissionBundle` into `in.Permissions`
       (union, dedup — preserve first-seen order, caller-supplied
       permissions first then any new ones from the bundle, to keep output
       deterministic for tests). Append `in.Role` into `in.Roles` if not
       already present (dedup against existing tags).
    4. Proceed to the existing `store.Register(...)` call with the merged
       slices.
  - When `in.Role == ""`, behavior is byte-for-byte unchanged from today.
- `cmd/wormhole-server/main.go`: pass a `roles.Store` into
  `mcp.RegisterAgentTool(...)` at the existing call site (line ~34).
  Construct it the same way `identityStore`/`eventsStore` are constructed
  nearby (check the file for the exact `roles.NewStore(db)` call shape —
  Chapter 5 built `roles.NewStore` for this purpose but this is its first
  wiring into the server binary).
- `cmd/wormhole-cli/main.go`:
  - `registerAgentInput` mirror struct gains `Role string \`json:"role,omitempty"\``.
  - `registerAgentOutput` mirror struct gains `Role string \`json:"role,omitempty"\``.
  - `runJoin`: new flag `role := fs.String("role", "", "role template name to resolve permissions from (e.g. backend-engineer)")`, wired into `registerAgentInput{Role: *role, ...}`.
  - `runConnect` is explicitly out of scope per the roadmap bullet's
    wording (`wormhole join --role`, not `wormhole connect --role`) — do
    not add the flag there.
- Dedup helper: if a small "union, preserve order" helper is needed for
  merging permissions/roles, keep it unexported and local to
  `internal/mcp/agent.go` (this file already has no shared-helpers import
  from elsewhere for this kind of logic) — do not add a new package for
  two call sites.

## Task 1 — Role resolution in wormhole.agent.register + wormhole join --role

Files:
- `internal/mcp/agent.go`
- `internal/mcp/agent_test.go` (or wherever this file's existing tests for
  `RegisterAgentTool` live — check `internal/mcp/*_test.go` for the
  current test, follow its construction pattern for `roles.Store` setup,
  reusing Chapter 5's `internal/core/roles/roles_test.go` DB-seeding
  convention if `internal/mcp` tests already spin up a real Postgres test
  DB — check `internal/mcp/server_test.go` or similar for how other tools'
  tests get their stores)
- `cmd/wormhole-server/main.go`
- `cmd/wormhole-cli/main.go`
- `cmd/wormhole-cli/main_test.go` (existing tests for `runJoin` /
  `registerAgentInput` marshaling — extend, don't duplicate)

Requirements:
1. Implement exactly the `RegisterAgentTool`/`RegisterAgentInput`/
   `RegisterAgentOutput` changes described in Global Constraints above.
2. Wire `roles.Store` into `cmd/wormhole-server/main.go`'s
   `RegisterAgentTool` call.
3. Add the CLI `--role` flag to `runJoin` only, per Global Constraints.
4. Tests to add/extend:
   - `wormhole.agent.register` with a known `role` (e.g.
     `backend-engineer`): resulting passport's `Roles` contains
     `backend-engineer`; resulting token's stored permissions are a
     superset of `backend-engineer`'s seeded bundle from Chapter 5's
     migration (`["task.read", "task.write", "kb.read", "kb.write",
     "channel.read", "channel.write"]`).
   - `wormhole.agent.register` with a known `role` AND explicit
     `permissions` that include something outside the bundle (e.g.
     `task.assign`): result includes both the bundle's permissions and
     `task.assign` (proves union, not override).
   - `wormhole.agent.register` with an unknown `role` (e.g. `"nonexistent"`):
     call fails with an error whose message identifies the unknown role
     name; verify via `errors.Is` against `roles.ErrTemplateNotFound` at
     the Go level if the test calls the handler directly, or via the
     `isError`/message-substring path if the test goes through
     `HandleToolsCall`/JSON-RPC (check `internal/mcp/agent.go`'s existing
     tests for which layer they test at and match it).
   - `wormhole.agent.register` with `role == ""` (existing tests):
     confirm unchanged — no regression on the pre-Chapter-6 path.
   - CLI: `runJoin` with `--role backend-engineer` sends
     `role: "backend-engineer"` in the registration request body (extend
     whatever existing test asserts `registerAgentInput`'s JSON shape).
5. Run `go build ./...` and `go test ./internal/mcp/... ./cmd/wormhole-cli/...`
   before committing.

This is Chapter 6's entire scope — both roadmap bullets (role resolution,
unknown-role rejection) land in one task since they're the same code path
and cannot be meaningfully split (the rejection branch only exists inside
the resolution logic being added).
