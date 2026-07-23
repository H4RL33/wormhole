# RFC Open-Question Reconciliation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Convert every Wormhole RFC open-question list into an explicit decision register and publish an evidence-based disposition for every currently open GitHub issue.

**Architecture:** RFCs remain the authority for architectural policy; GitHub remains the tracker for implementation work. Each former question is classified as decided, deferred by scope, or open, while a separate issue ledger records code/test evidence and recommends issue closure or retention without changing GitHub state.

**Tech Stack:** Markdown, Go source and tests as read-only evidence, GitHub issue metadata, `rg`, `go test`

## Global Constraints

- Follow the authority order in `agents/README.md`: RFC-0001, RFC-0003 amendments, RFC-0002 for optional Governance, implementation rules, then code.
- Do not turn contradictory or accidental implementation behavior into architecture.
- Do not modify production code or GitHub issue state.
- Preserve unrelated worktree changes.
- Use exact issue URLs in the ledger and RFCs only when an issue materially tracks the decision.
- Leave superseded-Constitution behavior, cross-runtime discovery, and manifest enforcement open.

---

### Task 1: Reconcile RFC-0001 and RFC-0002

**Files:**
- Modify: `docs/rfcs/wormhole_rfc.md`
- Modify: `docs/rfcs/wormhole_rfc_governance.md`

**Interfaces:**
- Consumes: authority rules from `agents/README.md` and the dispositions approved in `docs/superpowers/specs/2026-07-23-rfc-open-question-reconciliation-design.md`
- Produces: decision-register terminology and policy boundaries consumed by RFC-0003 and the issue ledger

- [ ] **Step 1: Capture the existing questions and supporting authority**

Run:

```bash
sed -n '315,355p' docs/rfcs/wormhole_rfc.md
sed -n '120,150p' docs/rfcs/wormhole_rfc_governance.md
rg -n 'poll|soft-reject|project-scoped|Project Binding|delegat|Constitution' \
  agents/README.md docs/implementation-rules.md docs/kb-schema.md docs/rfcs
```

Expected: five RFC-0001 questions and three RFC-0002 questions are visible, with repository evidence for polling, soft KB rejection, strict project scoping, explicit project bindings, direct attribution, and per-project Governance adoption.

- [ ] **Step 2: Replace RFC-0001 §15 with a decision register**

Use these exact dispositions:

```markdown
## 15. Decision Register

### Decided

- **Event delivery:** V1 uses Postgres-backed polling. Agent runtimes poll at
  turn start and during their configured sync cycle; Wormhole does not add
  NATS, Redis, or another event-stream datastore.
- **KB compliance:** Compliance failures use soft rejection with structured
  rewrite suggestions. Thresholds remain tunable configuration rather than
  architecture.
- **Cross-project KB visibility:** KB reads are strictly project-scoped. A
  runtime serving several projects keeps separate namespaces and never
  constructs an implicit merged KB.
- **Identity scope:** Credentials and resolved permissions belong to explicit
  project Passport bindings. Harnesses may reuse descriptive persona metadata,
  but Wormhole does not federate credentials or permissions across projects.
- **MCP hosting boundary:** Wormhole exposes its own MCP surface. It does not
  host arbitrary unrelated MCP servers for agents; approved integration
  manifests are a separate RFC-0003 bootstrap concern.

### Open

None.
```

Also update earlier prose that still calls any of these decisions open.

- [ ] **Step 3: Replace RFC-0002 §9 with a decision register**

Use these exact dispositions:

```markdown
## 9. Decision Register

### Decided

- **Delegation:** V1 Congress does not support delegated turns. Every turn is
  submitted by, and attributed directly to, the participating identity.
- **Constitution scope:** V1 has one Constitution per project. Per-team
  Constitutions and inheritance are outside this RFC and require an amendment
  supported by real multi-team adoption evidence.

### Open

- **Mid-task supersession:** When a Constitution is superseded while a task is
  in progress, Wormhole has not chosen between grandfathering the version active
  at task start and re-validating the remaining actions under the new version.
  Governance implementation must not guess this policy.
```

- [ ] **Step 4: Validate the two registers**

Run:

```bash
rg -n -i 'open questions?|real-time vs|compliance check strictness|cross-project KB visibility|identity federation|naming collision|Congress support delegation|single project-wide Constitution' \
  docs/rfcs/wormhole_rfc.md docs/rfcs/wormhole_rfc_governance.md
git diff --check -- docs/rfcs/wormhole_rfc.md docs/rfcs/wormhole_rfc_governance.md
```

Expected: no stale question headings or historical question wording; the only open RFC-0002 policy is mid-task supersession; `git diff --check` exits 0.

- [ ] **Step 5: Commit the RFC-0001 and RFC-0002 reconciliation**

```bash
git add docs/rfcs/wormhole_rfc.md docs/rfcs/wormhole_rfc_governance.md
git commit -m "docs(rfc): close core governance questions"
```

### Task 2: Reconcile RFC-0003

**Files:**
- Modify: `docs/rfcs/wormhole_rfc_local_runtime.md`

**Interfaces:**
- Consumes: RFC-0001 project-isolation and MCP-boundary decisions from Task 1; implemented sync shapes in `internal/mcp/sync.go`; local IPC behavior in `cmd/wormholed` and `internal/runtime/localapi`
- Produces: the authoritative local-runtime decision register referenced by the issue ledger

- [ ] **Step 1: Verify implemented protocol and IPC evidence**

Run:

```bash
rg -n 'SyncProtocolVersion|unsupported protocol version|type .*Input|type .*Output' internal/mcp/sync.go
rg -n '0600|0700|ListenUnix|socket|bearer|token' cmd/wormholed internal/runtime/localapi internal/runtime/config
rg -n 'org_config|project_list|manifest|approved integration|discovery' internal cmd
```

Expected: protocol version `1` and exact-version rejection are implemented; sync request/response structs exist; local socket permissions implement same-user trust without a second bearer token; discovery and manifest enforcement have no complete implementation.

- [ ] **Step 2: Update earlier RFC-0003 references**

Change NG1 and any §7–§10 wording that says a settled question remains open:

```markdown
Sync conflict handling in v1 is last-write-wins at the field/row level with
a durable audit trail. CRDTs and operational transforms are outside this RFC
and require a future amendment.
```

Remove `(OQ4)` references after the same-user IPC decision is recorded. Describe the sync structs in `internal/mcp/sync.go` as the frozen v1 wire contract rather than “indicative only.”

- [ ] **Step 3: Replace RFC-0003 §9 with a decision register**

Use these exact dispositions:

```markdown
## 9. Decision Register

### Decided

- **Conflict resolution:** V1 uses coordination-server-timestamp-authoritative
  last-write-wins with a durable audit trail. CRDTs, operational transforms,
  and distributed consensus are outside RFC-0003.
- **Sync wire contract:** The version-1 request and response shapes implemented
  by `wormhole.sync.bootstrap`, `incremental_pull`, `incremental_push`, and
  `conflict_report` are the frozen v1 contract.
- **Local IPC authentication:** V1 trusts processes able to connect through the
  same-user OS-protected socket or named pipe. It adds no local bearer-token
  layer and does not support sharing one daemon across OS users.
- **Version skew:** Every sync request and response carries integer protocol
  version `1`. Peers accept exactly that version and reject incompatible
  versions; backward-compatible negotiation is deferred until a second
  protocol version exists.

### Open

- **Cross-runtime discovery:** The Coordination Server owns discovery, but the
  protocol for advertising runtimes, projects, capabilities, and presence has
  not been selected. Empty bootstrap `org_config` and `project_list` values are
  placeholders, not a contract.
- **Integration-manifest enforcement:** Bootstrap may eventually distribute
  approved integration manifests, but signature verification, allow-listing,
  installation, update, revocation, and sandbox boundaries have not been
  selected. `wormholed` must not install or execute manifests until a separate
  approved design defines those controls.
```

- [ ] **Step 4: Validate RFC-0003**

Run:

```bash
rg -n -i 'open questions?|OQ[0-9]+|indicative only|not specified|unspecified' \
  docs/rfcs/wormhole_rfc_local_runtime.md
git diff --check -- docs/rfcs/wormhole_rfc_local_runtime.md
```

Expected: no OQ identifiers or stale open-question heading; remaining “not selected” language appears only in the two explicitly open entries; `git diff --check` exits 0.

- [ ] **Step 5: Commit RFC-0003 reconciliation**

```bash
git add docs/rfcs/wormhole_rfc_local_runtime.md
git commit -m "docs(rfc): resolve local runtime questions"
```

### Task 3: Audit and Publish Open-Issue Dispositions

**Files:**
- Create: `docs/github-open-issue-reconciliation.md`
- Modify only if evidence requires a link: `docs/rfcs/wormhole_rfc.md`
- Modify only if evidence requires a link: `docs/rfcs/wormhole_rfc_governance.md`
- Modify only if evidence requires a link: `docs/rfcs/wormhole_rfc_local_runtime.md`

**Interfaces:**
- Consumes: current GitHub issue metadata, all three decision registers, production code, and tests
- Produces: a dated, reproducible closure/retention recommendation for every open issue without mutating GitHub

- [ ] **Step 1: Refresh the complete open-issue inventory**

Run:

```bash
gh issue list --repo H4RL33/wormhole --state open --limit 200 \
  --json number,title,body,createdAt,updatedAt,labels,url
```

Expected: the inventory includes every issue that remains open on 2026-07-23. Record the retrieval date and do not omit recent issues merely because the request emphasizes long-standing ones.

- [ ] **Step 2: Verify milestone issues #1–#10**

Run:

```bash
for issue in 1 2 3 4 5 6 7 8 9 10; do
  gh issue view "$issue" --repo H4RL33/wormhole --json number,title,body,state,url
done
rg -n 'RegisterAgentTool|WhoAmITool|CreateTaskTool|UpdateTaskStatusTool|CreateChannelTool|PostEventTool|WriteArticleTool|SearchArticlesTool|BootstrapTool' internal cmd
go test ./internal/mcp ./internal/core/... ./cmd/wormholed ./cmd/wormhole
```

Expected: classify each ticket individually. Passing tests and matching implementation support closure; missing semantic embedding behavior or incomplete exit criteria keep the relevant issue open.

- [ ] **Step 3: Verify issues #21, #24, #22, #23, #32, and #33**

Run:

```bash
rg -n 'RequiredPermission|EveryAuthedToolDeclaresPermission|proxyRegister|doRegisterViaSocket|X-Admin-Key|WORMHOLE_ADMIN_KEY|audit_log_project_isolation|wormhole.project_id|FORCE ROW LEVEL SECURITY' \
  internal cmd migrations docs
go test ./internal/mcp ./internal/runtime/localapi ./cmd/wormhole ./cmd/wormholed
```

Expected:

- #21 is a closure candidate only if production dispatch enforces declared permissions.
- #24 is a closure candidate only if join registration goes through `wormholed` when its socket is available and the daemon owns the server proxy step.
- #22 and #23 remain open unless structured human identity and per-human viewer-key issuance authentication exist.
- #32 remains open while production and test registries are independently hand-maintained.
- #33 remains open while the audit RLS policy lacks both transaction scope setup and an intentional owner-bypass decision.

- [ ] **Step 4: Write the issue reconciliation ledger**

Create `docs/github-open-issue-reconciliation.md` with:

```markdown
# GitHub Open-Issue Reconciliation

**Repository:** `H4RL33/wormhole`
**Reviewed:** 2026-07-23
**Scope:** Every issue open at review time

This ledger recommends issue state from current RFCs, code, and tests. It does
not itself change GitHub state.

| Issue | Opened | Recommendation | Evidence |
|---|---|---|---|
| [#N: title](exact issue URL) | YYYY-MM-DD | Close / Keep open / Superseded | Exact file, test, RFC section, or missing behavior |
```

Include one row for every refreshed issue. Follow the table with:

- `Closure candidates`, listing exact acceptance evidence and any caveat;
- `Keep open`, stating the unresolved behavior and relevant decision-register entry;
- `Recommended GitHub actions`, listing exact issue numbers but making no API changes.

- [ ] **Step 5: Cross-link only materially related RFC entries**

Link:

- the human-identity issue from RFC-0001 identity scope or RFC-0002 human authority only if it clarifies an unresolved implementation boundary;
- the bootstrap issue from RFC-0003 only if verification shows it is not fully resolved;
- no milestone ticket from an RFC merely because it cites that RFC.

- [ ] **Step 6: Run final documentation and repository validation**

Run:

```bash
rg -n -i '## [0-9]+\\. Open Questions|OQ[0-9]+' docs/rfcs
rg -n 'TBD|TODO|fill in|implement later' \
  docs/rfcs docs/github-open-issue-reconciliation.md
git diff --check
go test ./...
```

Expected: no old open-question headings or OQ identifiers; no placeholders in changed documentation; `git diff --check` exits 0; `go test ./...` passes or any environment-dependent integration skip is explicitly reported.

- [ ] **Step 7: Commit the issue ledger and final links**

```bash
git add docs/github-open-issue-reconciliation.md docs/rfcs
git commit -m "docs: reconcile open GitHub issues"
```
