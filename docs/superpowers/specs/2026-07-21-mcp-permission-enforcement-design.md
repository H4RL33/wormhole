# MCP Permission Enforcement — Design

Date: 2026-07-21
Issue: #21 — MCP permission strings resolved into `AuthenticatedScope` but never enforced by any tool handler.
Authority: RFC-0001 §8.4 (Identity & Permissions pillar).

## Problem

`identity.AuthenticatedScope.Permissions` is populated at token resolution
(`internal/core/identity/identity.go:394`, via `WhoAmI`) from the permission
strings a Passport was granted. Every MCP tool handler receives
`*identity.AuthenticatedScope`, but none checks `scope.Permissions` before
acting. RLS enforces project isolation and bearer-token auth enforces
identity, but nothing enforces that an agent's Passport actually grants the
specific capability it is exercising. Any authenticated agent for a project
can call any tool.

`AuthenticatedScope`'s own doc comment already anticipates this:
"Later middleware can enforce the returned permissions without another token
lookup." The hook was designed in; only the enforcement was never built.

## Decisions (locked with owner)

1. **Fine-grained, per-tool permissions.** Each tool requires its own
   permission string, not a coarse resource-verb bundle. Rationale: agents
   are autonomous and can hallucinate; humans want maximum granular control
   over what each Passport can do. The permission string is the tool name
   minus the `wormhole.` prefix (e.g. `wormhole.task.create` → `task.create`).

2. **`git.*` gets its own fine-grained strings; `sync.*` is exempt.**
   `git.link_commit` and `git.request_review` each become a distinct
   permission. `sync.*` tools (bootstrap, incremental_pull, incremental_push,
   conflict_report) are `wormholed`↔Coordination-Server transport moving the
   agent's *own* data, not a discretionary agent action, so they require
   authentication only (no specific permission). `agent.whoami` is likewise
   auth-only: every authenticated agent must be able to self-identify, and
   gating that would be circular.

3. **Alpha hard-cut for existing agents.** A migration re-seeds
   `role_templates` with fine-grained bundles. Already-registered agents keep
   their now-inert coarse strings and must re-register / re-join to obtain the
   new strings. No coarse→fine translation of stored permissions. This is
   honest about pre-production status and avoids a fragile mapping migration.

4. **Audit-log every denial.** On a permission-denied, write an audit row via
   the existing `identity.Store.RecordAction` so humans have a persisted
   record of what agents attempted beyond their grant.

## Permission map

Gated tools (14), `RequiredPermission` = tool name minus `wormhole.`:

| Tool | Required permission |
|------|---------------------|
| wormhole.task.create | task.create |
| wormhole.task.assign | task.assign |
| wormhole.task.update_status | task.update_status |
| wormhole.task.list | task.list |
| wormhole.kb.write | kb.write |
| wormhole.kb.search | kb.search |
| wormhole.kb.get | kb.get |
| wormhole.kb.get_links | kb.get_links |
| wormhole.channel.create | channel.create |
| wormhole.channel.post | channel.post |
| wormhole.channel.list | channel.list |
| wormhole.channel.subscribe | channel.subscribe |
| wormhole.git.link_commit | git.link_commit |
| wormhole.git.request_review | git.request_review |

Exempt (`RequiredPermission` = `""`):

| Tool | RequiresAuth | Reason |
|------|--------------|--------|
| wormhole.agent.whoami | true | self-identification; gating is circular |
| wormhole.sync.bootstrap | true | transport of agent's own data, not a capability |
| wormhole.sync.incremental_pull | true | same |
| wormhole.sync.incremental_push | true | same |
| wormhole.sync.conflict_report | true | same |
| wormhole.agent.register | false | bootstrap, pre-token |

Invariant: a tool with `RequiresAuth == true` and no explicit exemption MUST
carry a non-empty `RequiredPermission`. Enforced by a registry test (below)
so a future tool cannot silently ship ungated.

## Architecture

### 1. `Tool.RequiredPermission` field (`internal/mcp/registry.go`)

Add one field to the `Tool` descriptor:

```go
type Tool struct {
    Name         string
    Description  string
    RequiresAuth bool
    // RequiredPermission is the fine-grained permission string a caller's
    // AuthenticatedScope must carry to invoke this tool (RFC-0001 §8.4).
    // Empty means "any authenticated caller" — used only for self-
    // identification (whoami) and wormholed transport (sync.*). Meaningful
    // only when RequiresAuth is true.
    RequiredPermission string
    ArgumentsExample   any
    Handler            Handler
}
```

Each tool's registration site sets it (14 edits across task.go, kb.go,
channel.go, git.go). The exempt sites (agent.go whoami, sync.go ×4) leave it
`""` with a one-line comment stating why, so the omission reads as deliberate.

### 2. `AuthenticatedScope.HasPermission` (`internal/core/identity/identity.go`)

```go
// HasPermission reports whether this scope's Passport grants the named
// permission. Exact string match against the resolved permission set; no
// wildcards or hierarchy (RFC-0001 §8.4 permissions are a flat action list).
func (s AuthenticatedScope) HasPermission(name string) bool {
    for _, p := range s.Permissions {
        if p == name {
            return true
        }
    }
    return false
}
```

Exact match only. No wildcard / `*` / admin bypass — YAGNI, and the
max-control intent argues against implicit broadening. If a future elevated
role needs many permissions, it lists them explicitly in its bundle.

### 3. Enforcement in `HandleToolsCall` (`internal/mcp/jsonrpc.go`)

New error code alongside the existing set:

```go
RPCPermissionDenied = -32002
```

Insert the check after auth resolution (current line 295), before dispatch
(current line 297):

```go
if tool.RequiresAuth && tool.RequiredPermission != "" &&
    !scope.HasPermission(tool.RequiredPermission) {
    // Persist the attempt so humans see what agents reach for beyond grant.
    _, _ = identityStore.RecordAction(ctx, scope.Agent.ID, scope.ProjectID,
        "permission.denied:"+tool.Name)
    return nil, &RPCError{
        Code:    RPCPermissionDenied,
        Message: "permission denied: requires " + tool.RequiredPermission,
    }
}
```

Notes:
- The audit write reuses the existing writer; no new store, no new parameter
  to `HandleToolsCall`, no migration. The `action` text column absorbs
  `permission.denied:<tool>`.
- The audit error is intentionally swallowed (`_`): a failed audit write must
  not convert a clean permission-denied into a 500. Denial still returns.
- Implementation must verify `RecordAction` inserts succeed on this path the
  same way they do inside `Store.Register` (the audit_log RLS policy is
  `USING`-only; INSERTs already work in the Register path, so the pattern
  holds — confirm during implementation, do not assume).

### 4. Role-template re-seed (`migrations/000014_role_templates_fine_grained`)

`.up.sql` rewrites each `role_templates.permission_bundle` from the coarse
seed (000010) to fine-grained strings. Coarse→fine expansion:

- task.read → task.list
- task.write → task.create, task.update_status
- task.assign → task.assign
- kb.read → kb.search, kb.get, kb.get_links
- kb.write → kb.write
- channel.read → channel.list, channel.subscribe
- channel.write → channel.create, channel.post

git permissions are new (no coarse equivalent). Assigned only to roles that
author code: backend-engineer, frontend-engineer, contributor, maintainer get
`git.link_commit` and `git.request_review`. reviewer and project-manager get
neither (they do not push commits or open reviews via these tools). This is a
reversible seed-level judgement, called out here so review can challenge it.

Resulting bundles:

- **backend-engineer / frontend-engineer / contributor**: task.list,
  task.create, task.update_status, kb.search, kb.get, kb.get_links, kb.write,
  channel.list, channel.subscribe, channel.create, channel.post,
  git.link_commit, git.request_review.
- **project-manager**: the above task/kb/channel set **plus task.assign**,
  **no git**.
- **maintainer**: full engineer set (incl. git) **plus task.assign**.
- **reviewer**: task.list, kb.search, kb.get, kb.get_links, kb.write,
  channel.list, channel.subscribe, channel.create, channel.post. No task
  create/update, no assign, no git.

`.down.sql` restores the 000010 coarse bundles verbatim.

## Data flow

```
tools/call
  → extractProjectID
  → [RequiresAuth] WhoAmI → AuthenticatedScope{Permissions, Roles, Agent}
  → [RequiredPermission != ""] scope.HasPermission(required)?
        no  → RecordAction("permission.denied:<tool>") → RPCError -32002
        yes → tool.Handler(...)
```

## Error handling

- Missing permission: `-32002`, message names the required permission. Distinct
  from `-32001` (invalid/expired token) and `-32602` (bad params) so callers
  and harnesses can tell "you are not allowed" from "you are not authenticated"
  from "your request was malformed".
- Audit-write failure on the denial path: swallowed; denial still returned.

## Testing

1. **`HasPermission` unit** (`identity`): present → true; absent → false;
   empty scope → false; empty name → false (empty is never a granted string).
2. **Enforcement** (`internal/mcp`, `HandleToolsCall`):
   - agent with the required permission → handler runs, normal result.
   - agent lacking it → `-32002`, message names the perm, and an audit row
     `permission.denied:<tool>` is written.
   - exempt tool (whoami; a sync tool) with a permission-less scope → runs.
   - `RequiresAuth == false` tool (register) → unaffected by the new check.
3. **Registry invariant** (`internal/mcp`): iterate the full production
   registry; assert every tool with `RequiresAuth == true` and name not in the
   exempt set (whoami, sync.*) has a non-empty `RequiredPermission`. Guards
   against a future tool shipping ungated.
4. **Migration** (storage): after applying 000014, each role bundle contains
   the expected fine-grained strings and no coarse remnant (`task.write`,
   `kb.read`, `channel.write`, etc. absent). `.down.sql` restores coarse.

## Files touched

- `internal/mcp/registry.go` — `RequiredPermission` field + doc.
- `internal/mcp/{task,kb,channel,git}.go` — set `RequiredPermission` (14).
- `internal/mcp/agent.go`, `internal/mcp/sync.go` — explicit `""` + comment.
- `internal/core/identity/identity.go` — `HasPermission` method.
- `internal/mcp/jsonrpc.go` — `RPCPermissionDenied` const + enforcement block.
- `migrations/000014_role_templates_fine_grained.{up,down}.sql`.
- Tests across `internal/mcp`, `internal/core/identity`, storage.
- README §4 / docs: note enforcement is now live and lists the tool→perm map
  (documentation-only follow, non-blocking).

## Out of scope

- Human identity/auth (#22), viewer-key retrofit (#23), RFC-0003 bootstrap
  refactor (#24).
- Wildcard / hierarchical / deny-rule permission semantics.
- Governance-driven dynamic permissions (RFC-0002, Constitution) — this is
  static Core enforcement only.
- Coarse→fine translation of already-stored agent permissions (explicitly
  rejected: alpha hard-cut).
