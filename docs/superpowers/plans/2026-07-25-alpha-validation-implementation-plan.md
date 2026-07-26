# Wormhole Alpha Validation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver and validate the unified Wormhole alpha programme, including Gateway-owned lifecycle, offline operation, declarative integration manifests, the local Go-only Code Graph experiment, meaning-bearing shared KB search, two-agent handoff, and closed external evaluation.

**Architecture:** Gateway remains the only agent-facing MCP endpoint and owns local durable operation. Fabric remains authoritative for shared multi-runtime state. Code Graph is a separate Gateway-owned, project-scoped local derivative of one approved Git checkout and never enters Fabric or the shared KB. The programme is delivered as 23 independently reviewable pull requests with evidence gates after lifecycle, Code Graph hardening, the complete alpha loop, and external validation.

**Tech Stack:** Go, SQLite, PostgreSQL with pgvector, MCP, Git CLI, standard-library Go analysis packages (`go/types`, `go/ast`), Markdown, process-level integration tests, and the existing Wormhole CLI/Gateway/Fabric test harnesses. `golang.org/x/tools/go/packages` may be added only after the explicit dependency-approval gate in Task 8.

## Global Constraints

* Git remains the sole source of truth for code.
* Harnesses connect to local Gateway, never directly to Fabric.
* Gateway performs no generative LLM inference.
* Gateway writes supported local state durably before synchronisation.
* Fabric is authoritative only for shared multi-runtime state.
* Integration manifests are project-scoped, role-aware, declarative, and text-only.
* Gateway never downloads or executes arbitrary scripts, binaries, plugins, hooks, packages, or model-generated code.
* Repository materialisation requires explicit human action.
* Models may inspect guidance but may not approve or alter it.
* Generated guidance must match the live Gateway MCP registry.
* Code Graph is disabled by default, local, Go-only, and not synchronised.
* Code Graph never stores complete source files, function bodies, or returned source packages.
* Code Graph enablement, disablement, checkout selection, and destructive actions are human-controlled.
* Agents cannot activate Warpspeed.
* Governance, managed cloud, human login, beta compatibility, and full Code Graph V1 remain out of scope.
* Every behaviour-changing task follows a red-green-refactor cycle.
* Every public contract change updates the alpha contract inventory.
* Every task ends with focused tests, the required wider suite, documentation, and a commit.
* Existing tracked files named in this plan are repository paths verified on 2026-07-25. A task that discovers a moved path must update this plan in the same PR rather than inventing a replacement.
* No new external Go dependency may be added without explicit human approval recorded in the owning issue and PR.
* Never use `git add -A`, `git add .`, or another repository-wide staging command. Stage only the exact paths listed in the task after checking `git status --short`.

## Required verification inherited by every implementation task

After the task's focused red-green test and before its commit, run:

```bash
make build
make test
make vet
git diff --check
git status --short
```

Expected: build, tests, and vet exit `0`; `git diff --check` prints no errors; `git status --short` contains only the task's declared files and pre-existing user changes. Postgres-backed tests may skip only under the repository's documented integration policy; a release-gate task must instead run with `WORMHOLE_INTEGRATION_REQUIRED=1` and an available Postgres instance. Record command outputs in the PR description.

---

## Repository path integrity

This plan was reconciled against the live repository tree on 2026-07-25. Every production-code task names exact existing files or exact files it will create. A fixture directory ending in `/` means the task creates the enumerated test repository or dataset inside that isolated directory; it is not permission to place production code or unrelated fixtures there.

If repository evolution invalidates a path, the implementing PR must first identify the current precedent with `rg --files` and `rg -n`, amend the task's **Files** block to the resolved path, and explain the move in the PR. Do not introduce a second path-map abstraction or guess from conventional Go layouts.

## Planned new files and directories

```text
ROADMAP-ALPHA-VALIDATION.md
docs/superpowers/plans/2026-07-25-alpha-validation-implementation-plan.md
docs/architecture/integration-manifest-design.md
docs/architecture/code-graph-alpha-contract.md
docs/testing/alpha-validation.md
docs/testing/code-graph-benchmarks.md
docs/operators/alpha-validation-trial.md

internal/runtime/codegraph/
  config/
  store/
  index/
  golang/
  query/
  source/

testdata/codegraph/
  basic/
  body-edit/
  signature-edit/
  untracked/
  malformed/
  symlink-escape/
  cross-project/

testdata/alpha/
  manifests/
  kb/
  projects/
```

### Task 1: Rebaseline roadmap and issue inventory

**Pull request:** PR 1

**Files:**
- Create: `ROADMAP-ALPHA-VALIDATION.md`
- Modify: `docs/superpowers/specs/2026-07-25-alpha-validation-unified-spec.md`
- Modify: `docs/superpowers/specs/2026-07-25-alpha-validation-roadmap.md`
- Modify: `docs/superpowers/specs/2026-07-25-code-graph-alpha-validation-slice-ammendment.md`
- Modify: `docs/github-open-issue-reconciliation.md`
- Modify: `agents/README.md`
- Test: shell link and status checks in this task

**Interfaces:**
- Produces: one canonical roadmap pointer, historical status on superseded documents, an issue-to-milestone matrix, and the alpha session-history decision.
- Consumes: `docs/rfcs/wormhole_rfc.md`, `docs/rfcs/wormhole_rfc_governance.md`, `docs/rfcs/wormhole_rfc_local_runtime.md`, `AGENTS.md`, `agents/README.md`, and `docs/github-open-issue-reconciliation.md`.

- [ ] **Step 1: Verify the named repository paths**

Run:

```bash
git ls-files AGENTS.md agents/README.md docs/rfcs docs/superpowers/specs docs/github-open-issue-reconciliation.md
rg -n "Supersedes|Alpha Validation|session|Passport|credential" AGENTS.md agents/README.md docs/superpowers/specs docs/github-open-issue-reconciliation.md
```

Expected: every named input exists. If a path has moved, amend this task before continuing.

- [ ] **Step 2: Install one canonical roadmap pointer**

Create `ROADMAP-ALPHA-VALIDATION.md` as a short pointer to `docs/superpowers/specs/2026-07-25-alpha-validation-unified-spec.md`; do not copy the full specification. Add a historical status header and that same pointer to the two superseded source documents. Do not create `unified-design.md`, duplicate the implementation plan, or delete historical documents.

- [ ] **Step 3: Record the alpha session decision**

Update `agents/README.md` with:

```text
Passports identify agents.
Credential profiles authorise local runtime access.
Harness process sessions are ephemeral during alpha.
Durable session records are deferred until a demonstrated use case requires them.
```

- [ ] **Step 4: Reconcile the issue inventory**

Update `docs/github-open-issue-reconciliation.md` with the intended `Alpha Validation` milestone, all 23 work packages, owners, dependencies, and acceptance criteria. Then create or update the GitHub milestone and issues through the approved GitHub workflow. Read the resulting milestone and issues back before recording completion. Keep issues `#22`, `#23`, and `#36` outside the milestone and narrow `#37` to offline restart.

- [ ] **Step 5: Run documentation validation**

Run:

```bash
test -f ROADMAP-ALPHA-VALIDATION.md
rg -n "docs/superpowers/specs/2026-07-25-alpha-validation-unified-spec.md" ROADMAP-ALPHA-VALIDATION.md docs/superpowers/specs/2026-07-25-alpha-validation-roadmap.md docs/superpowers/specs/2026-07-25-code-graph-alpha-validation-slice-ammendment.md
rg -n "Alpha Validation|#22|#23|#36|#37" docs/github-open-issue-reconciliation.md
git diff --check
```

Expected: every command exits `0`.

- [ ] **Step 6: Run the complete required suite**

Run `make build`, `make test`, and `make vet`. Record the exact commands and results in the PR description.

- [ ] **Step 7: Commit**

```bash
git add ROADMAP-ALPHA-VALIDATION.md docs/
git commit -m "docs: unify alpha validation specification"
```

---

### Task 2: Gateway enrolment request and lifecycle design

**Pull request:** PR 2

**Files:**
- Create: `docs/architecture/gateway-enrolment-lifecycle.md`
- Create: `internal/runtime/localapi/enrolment.go`
- Create: `internal/runtime/localapi/enrolment_test.go`
- Modify: `internal/runtime/localapi/mcp.go`
- Modify: `internal/runtime/localapi/contract_manifest_test.go`
- Modify: `cmd/wormhole/connect.go`
- Modify: `docs/contracts/README.md`

**Interfaces:**
- Produces: one versioned local enrolment request, one result union, idempotency rules, recovery states, and contract-inventory entries.
- Consumes: existing project, Passport, permission, repository-binding, and Fabric client types.

- [ ] **Step 1: Write failing contract tests**

Add tests for a request containing:

```text
project binding
owner
model
capabilities
repositories
roles
requested permissions
Fabric address
idempotency key
```

Add result cases for:

```text
fabric_unreachable
invalid_project
permissions_rejected
duplicate_identity
repository_mismatch
credential_persistence_failed
bootstrap_failed_after_enrolment
success
```

Run focused tests.

Expected: FAIL because the new contract is absent.

- [ ] **Step 2: Define the request and result contract**

Implement the minimum types in the existing Gateway contract package. Follow repository naming conventions. Do not add transport-specific fields to the domain contract.

The idempotency key must be stable across a CLI retry of the same user-approved enrolment attempt.

- [ ] **Step 3: Define lifecycle states**

Document and implement the state progression:

```text
requested
registration_in_progress
registered
credentials_persisted
bootstrap_in_progress
ready
recovery_required
failed
```

`registered` without committed bootstrap must remain recoverable.

- [ ] **Step 4: Add validation**

Reject:

* empty project binding;
* missing Fabric address;
* duplicate repository entries after canonicalisation;
* roles or permissions outside the requester's locally permitted envelope;
* malformed idempotency keys.

- [ ] **Step 5: Register the contract**

Update the alpha contract inventory and any schema-generation mechanism. Mark exact MCP signatures as alpha-inventory contracts.

- [ ] **Step 6: Run focused and package tests**

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add docs/architecture/gateway-enrolment-lifecycle.md
git diff --check
git status --short
git commit -m "design: define gateway enrolment lifecycle"
```

---

### Task 3: Gateway-owned registration and credential persistence

**Pull request:** PR 3

**Files:**
- Modify: `internal/runtime/localapi/enrolment.go`
- Modify: `internal/runtime/localapi/enrolment_test.go`
- Modify: `internal/runtime/localapi/localapi.go`
- Modify: `internal/runtime/localapi/localapi_join_test.go`
- Modify: `cmd/wormhole/connect.go`
- Modify: `cmd/wormhole/connect_test.go`
- Modify: `internal/core/identity/identity.go`
- Modify: `internal/core/identity/identity_test.go`
- Modify: `internal/config/paths.go`
- Modify: `internal/config/config_test.go`

**Interfaces:**
- Consumes: Task 2 enrolment request and result contract.
- Produces: Gateway registration orchestration, idempotent Passport issuance, atomic credential writes, and CLI-to-Gateway delegation.

- [ ] **Step 1: Write failing orchestration tests**

Tests must prove:

* CLI sends one local enrolment request;
* Gateway calls Fabric registration;
* retry with the same idempotency key returns the same Passport;
* credential persistence occurs only after registration succeeds;
* bearer tokens are absent from logs and model-facing errors;
* a credential-write failure returns `credential_persistence_failed`.

Run focused tests.

Expected: FAIL.

- [ ] **Step 2: Move registration orchestration into Gateway**

Remove direct Fabric registration from the CLI path. CLI retains prompts, display, and harness configuration only.

- [ ] **Step 3: Add idempotent registration**

Use the existing Fabric persistence model to associate idempotency key, project, and requested identity. A repeated completed request returns the original Passport. A conflicting request using the same key is rejected.

- [ ] **Step 4: Implement atomic credential persistence**

Write to a temporary file in the target directory, set restrictive permissions, `fsync` where the repository's durability conventions require it, and rename atomically.

The persisted file must never be world-readable.

- [ ] **Step 5: Add redaction tests**

Capture logs, Events, errors, and test failure output. Assert the bearer token and credential payload never appear.

- [ ] **Step 6: Run focused tests and the identity/security suite**

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git diff --check
git status --short
git commit -m "feat: move registration and credentials into gateway"
```

---

### Task 4: Gateway bootstrap ownership and lifecycle process tests

**Pull request:** PR 4

**Files:**
- Create: `internal/runtime/localapi/bootstrap.go`
- Create: `internal/runtime/localapi/bootstrap_test.go`
- Modify: `internal/runtime/localapi/enrolment.go`
- Modify: `internal/runtime/localapi/localapi.go`
- Modify: `internal/runtime/localstore/localstore.go`
- Modify: `internal/runtime/localstore/localstore_test.go`
- Modify: `internal/runtime/sync/sync.go`
- Modify: `internal/runtime/sync/sync_test.go`
- Modify: `cmd/wormhole/connect.go`
- Modify: `cmd/wormhole/cli_main_join_socket_test.go`
- Modify: `cmd/gatewayd/p7_e2e_integration_test.go`
- Create: `testdata/alpha/projects/bootstrap-non-empty/`

**Interfaces:**
- Consumes: registered identity and credential profile from Task 3.
- Produces: transactional bootstrap into SQLite and transition to incremental synchronisation.

- [ ] **Step 1: Create a non-empty bootstrap fixture**

Include:

* project metadata;
* one Agent identity;
* one Task;
* one Channel;
* one Event;
* one KB article;
* integration-manifest metadata without materialised files.

- [ ] **Step 2: Write a failing process-level test**

Launch real CLI, Gateway, Fabric, PostgreSQL, and SQLite. Assert:

* Gateway creates credentials;
* non-empty state reaches SQLite;
* retry does not duplicate Passport or bootstrap rows;
* CLI performs no direct post-request Fabric call;
* incremental sync starts only after transaction commit.

Expected: FAIL.

- [ ] **Step 3: Implement transactional bootstrap**

Apply bootstrap in one SQLite transaction. If any required object fails validation or insertion, roll back all bootstrap rows and record `recovery_required`.

- [ ] **Step 4: Implement recovery after partial lifecycle**

When registration succeeded but bootstrap failed, a retry must reuse the Passport and credentials, rerun bootstrap, and not create a second identity.

- [ ] **Step 5: Remove CLI bootstrap orchestration**

Delete or deprecate the direct CLI path. Add a test or call spy proving no direct follow-on Fabric call.

- [ ] **Step 6: Run process test twice**

First run proves fresh enrolment. Second run proves idempotent recovery and no duplicate rows.

Expected: PASS both times.

- [ ] **Step 7: Run the complete required suite**

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add testdata/alpha/projects/bootstrap-non-empty
git diff --check
git status --short
git commit -m "feat: make gateway own bootstrap lifecycle"
```

---

### Task 5: Offline Gateway restart

**Pull request:** PR 5

**Files:**
- Modify: `cmd/gatewayd/gatewayd.go`
- Modify: `cmd/gatewayd/gatewayd_test.go`
- Modify: `cmd/gatewayd/p7_e2e_integration_test.go`
- Modify: `internal/runtime/localapi/localapi.go`
- Modify: `internal/runtime/localapi/mcp.go`
- Modify: `internal/runtime/localapi/mcp_test.go`
- Modify: `internal/runtime/localstore/localstore.go`
- Modify: `internal/runtime/localstore/localstore_test.go`
- Modify: `internal/runtime/sync/queue_repo.go`
- Modify: `internal/runtime/sync/queue_repo_test.go`
- Modify: `internal/runtime/sync/sync.go`
- Modify: `internal/runtime/sync/sync_test.go`
- Modify: `README.md`
- Modify: `docs/wiki/CLI-Guide.md`

**Interfaces:**
- Produces: local-first startup, asynchronous Fabric synchronisation, durable offline queueing, and connection-state reporting.
- Consumes: completed bootstrap from Task 4.

- [ ] **Step 1: Write failing startup-order tests**

Instrument startup and assert:

```text
credentials opened
SQLite opened
local state validated
MCP socket served
Fabric sync started
```

Fabric failure must not terminate Gateway after local validation succeeds.

- [ ] **Step 2: Write failing offline write tests**

After stopping Fabric and restarting Gateway:

* local reads succeed;
* supported writes commit to SQLite;
* outbound queue grows;
* authority-requiring operations return an explicit denial;
* queue survives another Gateway restart.

- [ ] **Step 3: Implement connection states**

Expose exactly:

```text
online
offline
synchronizing
attention_required
```

through CLI and read-only local MCP status.

- [ ] **Step 4: Separate startup from synchronisation**

Start the local MCP server before remote sync. Run sync in the existing background lifecycle mechanism and propagate status without crashing the process.

- [ ] **Step 5: Prove exactly-once recovery**

Restart Fabric. Assert each queued mutation reaches shared state once, the queue entry is acknowledged once, and a second sync pass creates no duplicate.

- [ ] **Step 6: Update documentation**

Remove any statement that Fabric must be reachable for every Gateway start. Describe which operations remain local and which require central authority.

- [ ] **Step 7: Run focused interruption test and full required suite**

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git diff --check
git status --short
git commit -m "feat: support offline gateway restart"
```

---

### Task 6: Approve the integration-manifest design

**Pull request:** PR 6

**Files:**
- Create: `docs/architecture/integration-manifest-design.md`
- Create: `cmd/wormhole/integration_manifest_design_contract_test.go`
- Modify: `docs/contracts/README.md`
- Modify: `docs/contracts/alpha-contract.json`
- Modify: `docs/superpowers/plans/2026-07-25-alpha-validation-implementation-plan.md`

**Interfaces:**
- Produces: binding manifest schema, exact CLI names, exact read-only MCP name, marker syntax, trust model, merge policy, rollback model, audit taxonomy, and test strategy.
- Consumes: live Gateway registry and Task 5 offline model.

- [ ] **Step 1: Write a design conformance checklist**

The document must resolve:

```text
schema
trust
ownership
role selection
rendering
merge behaviour
update behaviour
rollback
offline state
audit events
compatibility
threat model
test strategy
exact CLI names
exact MCP name
managed-section markers
```

- [ ] **Step 2: Define the manifest schema**

Define and exemplify:

```text
schema_version
manifest_id
manifest_version
project_id
source
created_at
tool_contract_digest
role_filters
entries[]
```

and entry fields:

```text
kind
target
content_digest
merge_policy
required
role_filters
```

Permit only `agents_bootstrap`, `skill`, and `reference`.

- [ ] **Step 3: Define exact lifecycle commands**

Choose and record exact commands for:

```text
preview
apply
status
update
remove
rollback
```

Do not implement aliases in this PR.

- [ ] **Step 4: Define exact read-only MCP contract**

Name the tool and specify its request, response, permission, and offline behaviour. Add it to the alpha contract inventory as designed but not implemented.

- [ ] **Step 5: Define managed ownership**

Specify:

* `.wormhole/integration-state.json`;
* managed-section markers for `AGENTS.md`;
* file digest tracking;
* user-owned content preservation;
* rollback snapshots or reconstructable previous state;
* revocation behaviour.

- [ ] **Step 6: Add a failing design completeness test**

The test scans the design for every required heading and ensures prohibited entry kinds remain explicitly rejected.

- [ ] **Step 7: Complete the document and run documentation tests**

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add cmd/wormhole/integration_manifest_design_contract_test.go \
  docs/architecture/integration-manifest-design.md \
  docs/contracts/README.md \
  docs/contracts/alpha-contract.json \
  docs/superpowers/plans/2026-07-25-alpha-validation-implementation-plan.md
git diff --check
git status --short
git commit -m "design: specify gateway integration manifests"
```

---

### Task 7: Code Graph alpha storage and revision model

**Pull request:** PR 7

**Files:**
- Create: `docs/architecture/code-graph-alpha-contract.md`
- Create: `internal/runtime/codegraph/config/config.go`
- Create: `internal/runtime/codegraph/config/config_test.go`
- Create: `internal/runtime/codegraph/store/store.go`
- Create: `internal/runtime/codegraph/store/store_test.go`
- Create: `internal/runtime/codegraph/index/index.go`
- Create: `internal/runtime/codegraph/index/index_test.go`
- Modify: `internal/runtime/localstore/localstore.go`
- Modify: `internal/runtime/localstore/localstore_test.go`
- Create: `testdata/codegraph/basic/`

**Interfaces:**
- Produces: local configuration, schema, revision states, candidate publication, invariant validation, and failure preservation.
- Consumes: stable project binding from Gate A.

**Implementation choice:** The unified specification does not prescribe internal Go signatures. This task must adapt names to established repository conventions while preserving the contracts below.

- [ ] **Step 1: Write failing default-state and schema tests**

Prove:

* Code Graph is disabled by default;
* project configuration is isolated by project identifier;
* schema contains revisions, files, symbols, edges, and diagnostics;
* no column stores a complete source body or returned context package.

- [ ] **Step 2: Write failing revision visibility tests**

Create an active revision and a candidate revision. Queries through the store read only the active revision. A failed candidate leaves the active revision unchanged.

- [ ] **Step 3: Implement configuration storage**

Persist:

```text
project_id
enabled
canonical_remote
active_checkout
project_source_byte_ceiling
last_successful_build
```

Do not persist Warpspeed, embeddings, watchers, dependencies, or polyglot settings.

- [ ] **Step 4: Implement graph storage**

Persist:

```text
revisions
files and indexed hashes
nodes
symbols
edges
diagnostics
```

Every row is project-scoped and revision-scoped where applicable.

- [ ] **Step 5: Implement copy-on-write publication**

Provide one transaction that:

1. validates the candidate;
2. atomically marks it active;
3. retires the previous active revision;
4. leaves readers on one coherent revision.

- [ ] **Step 6: Implement invariant validation**

Reject candidates with:

* dangling edges;
* duplicate deterministic IDs;
* invalid source ranges;
* cross-project references;
* more than one active revision.

- [ ] **Step 7: Prove failed candidate cleanup**

Inject validation failure. Assert candidate rows are removed or marked failed and invisible, diagnostics remain available, and the active revision stays queryable.

- [ ] **Step 8: Run package tests and SQLite migration tests**

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add docs/architecture/code-graph-alpha-contract.md
git add internal/runtime/codegraph/config internal/runtime/codegraph/store internal/runtime/codegraph/index
git diff --check
git status --short
git commit -m "feat: add code graph revision store"
```

---

### Task 8: Git-tracked Go inventory and semantic adapter

**Pull request:** PR 8

**Files:**
- Create: `internal/runtime/codegraph/golang/analyzer.go`
- Create: `internal/runtime/codegraph/golang/analyzer_test.go`
- Modify: `internal/runtime/codegraph/index/index.go`
- Modify: `internal/runtime/codegraph/index/index_test.go`
- Create: `testdata/codegraph/body-edit/`
- Create: `testdata/codegraph/signature-edit/`
- Create: `testdata/codegraph/untracked/`

**Interfaces:**
- Produces: tracked Go file inventory, package and symbol extraction, deterministic identities, and supported edges.
- Consumes: Task 7 candidate revision writer.

- [ ] **Step 1: Write failing Git inventory tests**

Fixture includes:

* tracked `.go` files;
* modified tracked file;
* untracked `.go` file;
* ignored `.go` file;
* tracked non-Go file.

Assert only tracked Go files enter the candidate.

- [ ] **Step 2: Implement canonical Git inventory**

Use Git's tracked-file output, not recursive filesystem discovery. Canonicalise paths and reject anything outside the approved checkout.

- [ ] **Step 3: Write failing symbol extraction tests**

Cover:

* package;
* file;
* function;
* method;
* type;
* interface;
* constant;
* variable;
* imports;
* calls;
* references;
* type use.

- [ ] **Step 4: Implement Go analysis**

Before importing `golang.org/x/tools/go/packages`, obtain explicit human approval for the new direct dependency and record it in the issue and PR. If approval is granted, add the pinned module with:

```bash
go get golang.org/x/tools@v0.48.0
go mod tidy
```

Review both `go.mod` and `go.sum`; reject unrelated module-graph changes. If approval is declined, stop Task 8 and revise the approved Code Graph design rather than silently replacing package loading semantics.

Use the repository's supported Go version with:

```text
go/packages
go/types
go/ast
```

Expected dependency evidence: `go.mod` contains a direct `golang.org/x/tools v0.48.0` requirement and the PR links the human approval recorded in issue #51 on 2026-07-26 for the current stable version compatible with the repository Go version.

Record provenance as one of:

```text
go_packages
go_types
go_ast
parser
heuristic
```

- [ ] **Step 5: Implement deterministic symbol fingerprints**

Body, comment, and whitespace edits preserve IDs. Signature changes may change IDs. Renames change IDs.

- [ ] **Step 6: Write edge-integrity tests**

Every edge must point to existing nodes in the same project and revision. Heuristic edges must carry `heuristic` provenance and non-authoritative presentation metadata.

- [ ] **Step 7: Build and publish the basic fixture**

Assert tracked file, symbol, and edge counts are deterministic for the same commit and working tree.

- [ ] **Step 8: Run package and race tests**

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/runtime/codegraph/golang internal/runtime/codegraph/index testdata/codegraph
git commit -m "feat: index tracked go source into code graph"
```

---

### Task 9: Structural query and bounded source assembly

**Pull request:** PR 9

**Files:**
- Create: `internal/runtime/codegraph/query/query.go`
- Create: `internal/runtime/codegraph/query/query_test.go`
- Create: `internal/runtime/codegraph/source/source.go`
- Create: `internal/runtime/codegraph/source/source_test.go`
- Modify: `internal/runtime/codegraph/store/store.go`
- Modify: `internal/runtime/codegraph/store/store_test.go`
- Create: `testdata/codegraph/malformed/`

**Interfaces:**
- Produces: lexical entry ranking, bounded traversal, source-budget allocation, hash-validated slicing, explicit omission metadata.
- Consumes: coherent active revision from Tasks 7 and 8.

- [ ] **Step 1: Write failing lexical ranking tests**

Given an intent and optional entry symbols, exact qualified-name and exact name matches rank before lexical package or file matches.

No embedding path may exist in alpha.

- [ ] **Step 2: Write failing traversal tests**

Enforce:

* requested edge filters;
* maximum depth;
* minimum confidence;
* deterministic order;
* one graph revision per response.

- [ ] **Step 3: Write failing source-budget tests**

Test:

* requested budget below project ceiling;
* requested budget above project ceiling;
* exact exhaustion;
* multiple slices competing for budget;
* explicit omitted-node count and reason;
* suggested follow-up symbols.

- [ ] **Step 4: Implement ranking and traversal**

Rank using:

1. exact entry symbol;
2. lexical symbol match;
3. package and file match;
4. graph distance;
5. relationship relevance;
6. confidence;
7. provenance strength.

Do not add embeddings or speculative semantic similarity.

- [ ] **Step 5: Implement filesystem containment**

Resolve files beneath the approved checkout. Reject `..`, absolute escapes, and symlinks resolving outside the checkout.

- [ ] **Step 6: Implement source integrity**

Hash the current file and compare with the indexed hash before slicing. On mismatch, omit source and set:

```json
{
  "source_included": false,
  "source_omission_reason": "working_tree_changed",
  "refresh_recommended": true
}
```

- [ ] **Step 7: Implement explicit completeness**

Every response includes completeness, omitted-node count, omission reason, and follow-up symbols.

- [ ] **Step 8: Run focused tests and fuzz containment inputs**

Expected: PASS with no panic and no escaped read.

- [ ] **Step 9: Commit**

```bash
git add internal/runtime/codegraph/query internal/runtime/codegraph/source internal/runtime/codegraph/store testdata/codegraph/malformed
git commit -m "feat: add bounded code graph queries"
```

---

### Task 10: Code Graph MCP tools and permissions

**Pull request:** PR 10

**Files:**
- Modify: `internal/runtime/localapi/mcp.go`
- Modify: `internal/runtime/localapi/mcp_test.go`
- Modify: `internal/runtime/localapi/contract_manifest_test.go`
- Modify: `internal/runtime/codegraph/query/query.go`
- Modify: `internal/runtime/codegraph/store/store.go`
- Modify: `internal/runtime/codegraph/index/index.go`
- Modify: `docs/contracts/README.md`

**Interfaces:**
- Produces:
  - `wormhole.code_graph.query`
  - `wormhole.code_graph.status`
  - `wormhole.code_graph.rebuild`
- Consumes: Tasks 7 to 9.

- [ ] **Step 1: Write failing registry tests**

Assert exactly three Code Graph tools are exposed and each has the expected permission.

- [ ] **Step 2: Add project-scoped permissions**

```text
code_graph.query
code_graph.source.read
code_graph.status
code_graph.rebuild
```

- [ ] **Step 3: Implement metadata-only degradation**

A caller with `code_graph.query` but without `code_graph.source.read` receives matched symbols, paths, locations, confidence, provenance, and:

```json
{
  "source_included": false,
  "source_omission_reason": "missing_permission",
  "required_permission": "code_graph.source.read"
}
```

- [ ] **Step 4: Implement status**

Return the six states and required counters from the specification. Dirty tracked-file count is computed without modifying the graph.

- [ ] **Step 5: Implement balanced rebuild**

The MCP tool may request only a normal copy-on-write rebuild. Add tests proving it cannot enable, disable, change checkout, change ceilings, invoke Warpspeed, or perform in-place rebuild.

- [ ] **Step 6: Add cross-project denial tests**

A credential for project A cannot query, inspect, or rebuild project B's graph.

- [ ] **Step 7: Update contract inventory**

Record request and response schemas, permissions, error cases, and freshness implications.

- [ ] **Step 8: Run registry, permission, and integration tests**

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/runtime/codegraph
git diff --check
git status --short
git commit -m "feat: expose code graph through gateway mcp"
```

---

### Task 11: Code Graph CLI lifecycle and destructive disablement

**Pull request:** PR 11

**Files:**
- Create: `cmd/wormhole/code_graph.go`
- Create: `cmd/wormhole/code_graph_test.go`
- Modify: `cmd/wormhole/main.go`
- Modify: `internal/runtime/localapi/localapi.go`
- Modify: `internal/runtime/codegraph/config/config.go`
- Modify: `internal/runtime/codegraph/index/index.go`
- Modify: `internal/runtime/codegraph/store/store.go`
- Modify: `docs/wiki/CLI-Guide.md`
- Create: `testdata/codegraph/symlink-escape/`

**Interfaces:**
- Produces exact CLI commands from the specification.
- Consumes: Task 10 local MCP and permission contracts.

- [ ] **Step 1: Write failing command-registration tests**

Assert these commands exist:

```bash
wormhole config code-graph enable
wormhole config code-graph disable
wormhole config code-graph status
wormhole config code-graph rebuild
wormhole config code-graph checkout set
wormhole config code-graph checkout show
```

- [ ] **Step 2: Write failing enablement tests**

Prove rejection for:

* non-Git directory;
* mismatched canonical remote;
* checkout path escape;
* initial build failure.

On build failure, configuration remains disabled and no active revision exists.

- [ ] **Step 3: Implement human-confirmed enablement**

Interactive enablement explains local experimental scope and resource implications. Non-interactive mode requires the repository's established explicit confirmation flag.

- [ ] **Step 4: Implement checkout selection**

Canonicalise the path, resolve remote, compare project binding, and perform a clean build. Never merge graph state between checkouts.

- [ ] **Step 5: Write failing concurrent-disable tests**

Start a query, request disablement, and prove new queries are rejected while the current reader completes or is cleanly cancelled.

- [ ] **Step 6: Implement destructive disablement**

Delete:

* completed revisions;
* candidate revisions;
* nodes;
* edges;
* files;
* diagnostics;
* project Code Graph configuration.

Leave Git and the working tree untouched.

- [ ] **Step 7: Prove Warpspeed absence**

Assert no CLI or MCP alpha path accepts `--warpspeed`, `warpspeed`, in-place rebuild, pause, or resume.

- [ ] **Step 8: Run CLI, concurrency, and process tests**

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/runtime/codegraph testdata/codegraph/symlink-escape
git diff --check
git status --short
git commit -m "feat: add code graph cli lifecycle"
```

---

### Task 12: Code Graph benchmark and security hardening

**Pull request:** PR 12

**Files:**
- Create: `docs/testing/code-graph-benchmarks.md`
- Create: `testdata/codegraph/cross-project/`
- Create: `internal/runtime/codegraph/source/security_test.go`
- Create: `internal/runtime/codegraph/query/benchmark_test.go`
- Modify: `internal/runtime/codegraph/index/index_test.go`
- Modify: `cmd/gatewayd/p7_e2e_integration_test.go`

**Interfaces:**
- Produces: checked-in benchmark corpus, reproducible measurements, and Gate B evidence.
- Consumes: complete Code Graph alpha toolchain.

- [ ] **Step 1: Encode the six benchmark questions**

For each, record expected entry symbols, files, path, authoritative and heuristic edges, and sufficiency criteria.

- [ ] **Step 2: Add a benchmark runner**

Capture:

```text
source bytes returned
irrelevant source bytes
omitted results
query duration
result sufficiency
files selected
```

Store raw results as test artefacts or structured local output, not as hard pass thresholds.

- [ ] **Step 3: Add security cases**

Cover:

* path traversal;
* symlink escape;
* source changing during assembly;
* cross-project retrieval;
* malformed parser input;
* deeply nested files;
* oversized files;
* corrupt graph rows;
* concurrent query and disablement.

- [ ] **Step 4: Add persistence inspection**

After indexing fixtures, inspect the SQLite database and assert complete source bodies and returned context packages are absent.

- [ ] **Step 5: Run the benchmark on the Wormhole repository**

Record baseline measurements. Do not convert them into arbitrary release thresholds.

- [ ] **Step 6: Run security and race tests**

Expected: PASS.

- [ ] **Step 7: Review Gate B**

Document:

* active revision preservation on failure;
* source integrity;
* project isolation;
* containment;
* measured benchmark results;
* known useless or incomplete queries.

- [ ] **Step 8: Commit**

```bash
git add docs/testing/code-graph-benchmarks.md internal/runtime/codegraph testdata/codegraph
git commit -m "test: harden code graph alpha slice"
```

---

### Task 13: Tool-guidance metadata and contract coverage

**Pull request:** PR 13

**Files:**
- Create: `internal/runtime/localapi/guidance.go`
- Create: `internal/runtime/localapi/guidance_test.go`
- Modify: `internal/runtime/localapi/mcp.go`
- Modify: `internal/runtime/localapi/contract_manifest_test.go`
- Modify: `docs/contracts/README.md`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Produces: exactly one guidance record per exposed agent-facing tool.
- Consumes: all current Gateway tools, including Code Graph and the designed integration-guidance tool.

- [ ] **Step 1: Define the guidance record**

Fields:

```text
purpose
use_when
do_not_use_when
mutates_state
required_permission
prerequisites
freshness_implications
source_access_implications
recommended_follow_up
minimal_example
misuse_warning
```

- [ ] **Step 2: Write a failing one-to-one contract test**

Enumerate the live agent-facing registry. Assert:

* every tool has one record;
* no record points to a missing tool;
* no duplicate record exists;
* Code Graph records include freshness and source-access implications.

Expected: FAIL for existing unguided tools.

- [ ] **Step 3: Populate guidance for every tool**

Group by established concepts, not invented pillars:

```text
identity
tasks
channels and events
knowledge
Git pointers
local status and synchronisation
integration guidance
Code Graph
```

- [ ] **Step 4: Generate minimal examples from live schemas**

Examples must compile or validate against the registry schema. Do not duplicate stale hand-written parameter definitions.

- [ ] **Step 5: Wire the contract test into CI**

A new exposed tool without guidance must fail the required suite.

- [ ] **Step 6: Run registry and CI-equivalent tests**

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git diff --check
git status --short
git commit -m "feat: require guidance for gateway tools"
```

---

### Task 14: Generated orientation, operating-loop, role, and Code Graph skills

**Pull request:** PR 14

**Files:**
- Create: `internal/runtime/localapi/guidance_render.go`
- Create: `internal/runtime/localapi/guidance_render_test.go`
- Create: `testdata/alpha/manifests/generated-guidance/manifest.json`
- Create: `testdata/alpha/manifests/generated-guidance/wormhole-orientation.md`
- Create: `testdata/alpha/manifests/generated-guidance/wormhole-tool-use.md`
- Create: `testdata/alpha/manifests/generated-guidance/wormhole-code-graph.md`
- Create: `testdata/alpha/manifests/generated-guidance/wormhole-operating-loop.md`
- Create: `testdata/alpha/manifests/generated-guidance/wormhole-contributor.md`
- Create: `testdata/alpha/manifests/generated-guidance/wormhole-reviewer.md`
- Generated targets:
  - `.agents/skills/wormhole-orientation/SKILL.md`
  - `.agents/skills/wormhole-tool-use/SKILL.md`
  - `.agents/skills/wormhole-code-graph/SKILL.md`
  - `.agents/skills/wormhole-operating-loop/SKILL.md`
  - `.agents/skills/wormhole-contributor/SKILL.md`
  - `.agents/skills/wormhole-reviewer/SKILL.md`

**Interfaces:**
- Produces: deterministic role-aware Markdown rendered from live registry and canonical guidance.
- Consumes: Task 13 records and Task 6 manifest design.

- [ ] **Step 1: Write failing golden tests**

Golden output must include all six skills, current tool names, current permissions, no unsupported tools, and every operating-loop assertion listed in Step 3.

- [ ] **Step 2: Implement orientation rendering**

Include Core boundaries, Git authority, Gateway/Fabric roles, typed Events, Tasks, KB use, and identity.

- [ ] **Step 3: Implement capability-aware operating loop**

Render:

```text
if Code Graph is ready:
    query it before broad code discovery
else:
    continue with normal filesystem and repository tools
```

Do not imply installation, enablement, freshness, or permission.

The generated operating-loop skill must also encode and test this complete sequence:

```text
session start:
  inspect identity and permissions
  inspect assigned and relevant Tasks
  retrieve relevant KB context
  inspect recent relevant Events
  inspect Code Graph status for code tasks
  confirm intended work before broad exploration

before changing code:
  retrieve the Task and links
  check decisions and constraints
  use Code Graph when ready and useful
  report work begun when supported
  preserve Git as authority

during work:
  record meaningful blockers
  publish only durable discoveries
  do not narrate every command
  prefer typed Events
  check for duplicate Tasks and KB articles before creating them

completion:
  run required verification
  update Task state
  link the commit or pull request where supported
  record durable knowledge
  publish one concise completion Event
  leave sufficient context for another Agent
```

Add one golden assertion for every line above so omissions fail the task rather than relying on prose review.

- [ ] **Step 4: Implement Code Graph skill**

Include use cases, non-use cases, bounded source budget, status-first sequence, heuristic warning, and the required statement that Code Graph does not replace Git, direct verification, builds, or tests.

- [ ] **Step 5: Implement contributor and reviewer role overlays**

Reviewer guidance must verify graph findings against Git and treat heuristic edges as hypotheses.

- [ ] **Step 6: Add drift tests**

Change a fixture registry schema and prove generated Markdown changes. Remove a guidance record and prove generation fails.

- [ ] **Step 7: Run golden and contract tests**

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add testdata/alpha/manifests
git diff --check
git status --short
git commit -m "feat: generate wormhole agent skills"
```

---

### Task 15: Safe AGENTS and SKILLS materialisation

**Pull request:** PR 15

**Files:**
- Create: `internal/runtime/localapi/materialize.go`
- Create: `internal/runtime/localapi/materialize_test.go`
- Create: `cmd/wormhole/integration.go`
- Create: `cmd/wormhole/integration_test.go`
- Modify: `cmd/wormhole/main.go`
- Create: `testdata/alpha/manifests/integration-state.json`

**Interfaces:**
- Produces: preview, apply, status, update, remove, and rollback commands selected in Task 6.
- Consumes: generated manifest content from Task 14.

- [ ] **Step 1: Write failing preview tests**

Preview must produce a deterministic diff and perform no writes.

- [ ] **Step 2: Write failing ownership tests**

Cover:

* absent `AGENTS.md`;
* existing user-only `AGENTS.md`;
* existing managed section;
* modified managed file;
* unrelated user skill directory;
* revoked manifest.

- [ ] **Step 3: Implement atomic materialisation**

Write temporary files, preserve permissions where appropriate, and rename atomically. Record digests and approved version in `.wormhole/integration-state.json`.

- [ ] **Step 4: Implement managed-section editing**

Update only the delimited Wormhole section. Preserve byte-for-byte user content outside it.

- [ ] **Step 5: Implement rollback and removal**

Rollback restores the prior managed state. Removal deletes only managed files or sections and leaves user-owned content intact.

- [ ] **Step 6: Enforce human action**

No startup path may auto-apply. Model-facing calls cannot approve. Non-interactive CLI application requires an explicit confirmation flag.

- [ ] **Step 7: Prove offline operation**

With Fabric unavailable, preview and status use the last approved cached manifest.

- [ ] **Step 8: Run filesystem, CLI, and process tests**

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add testdata/alpha/manifests
git diff --check
git status --short
git commit -m "feat: materialize gateway managed guidance safely"
```

---

### Task 16: Read-only model guidance through Gateway

**Pull request:** PR 16

**Files:**
- Modify: `internal/runtime/localapi/mcp.go`
- Modify: `internal/runtime/localapi/mcp_test.go`
- Modify: `internal/runtime/localapi/guidance.go`
- Modify: `internal/runtime/localapi/guidance_test.go`
- Modify: `internal/runtime/localapi/contract_manifest_test.go`
- Modify: `docs/contracts/README.md`

**Interfaces:**
- Produces: the exact read-only MCP tool approved in Task 6.
- Consumes: local approved integration state from Task 15.

- [ ] **Step 1: Write failing contract tests**

Response includes:

```text
manifest version
resolved role
applicable guidance
materialised match state
pending approval state
```

- [ ] **Step 2: Implement read-only resolution**

Resolve role filters and return guidance without exposing filesystem paths that are not part of the contract.

- [ ] **Step 3: Implement offline behaviour**

Return the last approved cached guidance while offline and distinguish it from a newer unapproved offered version.

- [ ] **Step 4: Enforce non-mutation**

The tool has no request field or side effect capable of approval, update, apply, remove, or rollback.

- [ ] **Step 5: Add guidance record**

Task 13's one-to-one test must include the new tool.

- [ ] **Step 6: Run registry, permission, and offline tests**

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git diff --check
git status --short
git commit -m "feat: expose current integration guidance"
```

---

### Task 17: Shared KB semantic embedding implementation

**Pull request:** PR 17

**Files:**
- Modify: `internal/core/kb/kb.go`
- Modify: `internal/core/kb/kb_test.go`
- Modify: `internal/mcp/kb.go`
- Modify: `internal/mcp/kb_test.go`
- Modify: `cmd/fabric/main.go`
- Modify: `cmd/fabric/main_test.go`
- Modify: `internal/types/config.go`
- Modify: `internal/types/config_test.go`
- Create: `migrations/000019_kb_semantic_embeddings.up.sql`
- Create: `migrations/000019_kb_semantic_embeddings.down.sql`
- Modify: `docs/contracts/README.md`
- Modify: `docs/kb-schema.md`
- Create: `testdata/alpha/kb/semantic-low-overlap.json`
- Create: `testdata/alpha/kb/provider-responses.json`

**Interfaces:**
- Produces: provider-neutral embedder, one supported provider, model metadata, query/article embedding parity, and unavailable-provider behaviour.
- Consumes: existing KB write and search contracts.

- [ ] **Step 1: Approve the production embedding contract**

Before implementation, record human approval in issue `#8` for one production provider, exact model and version, endpoint and authentication configuration, vector dimension, data-handling implications, timeout, and unavailable-provider response. The approved response must explicitly say whether lexical fallback occurs and how callers learn that semantic ranking did not occur. Do not infer these choices from the existing deterministic development stub.

- [ ] **Step 2: Write failing provider-interface tests**

Define behaviour for:

* embed one query;
* embed one article;
* batch embed;
* model identifier;
* vector dimension;
* unavailable provider;
* dimension mismatch.

- [ ] **Step 3: Implement provider-neutral interface**

Keep provider-specific configuration outside the KB domain. Gateway or Fabric orchestration must not silently choose a remote endpoint.

- [ ] **Step 4: Implement the approved supported provider**

Record:

```text
provider
model
version
dimension
created_at
```

with every stored embedding.

- [ ] **Step 5: Add schema migration**

Store vectors through pgvector while retaining project isolation. Reject vectors with a different dimension or model identifier in the active index.

- [ ] **Step 6: Implement write-time and query-time embedding**

Use the same configured model for articles and queries.

- [ ] **Step 7: Implement the approved unavailable behaviour**

Return the exact degraded result or fallback approved in Step 1. In either case, include explicit metadata that semantic ranking did not occur; never label lexical fallback as semantic search.

- [ ] **Step 8: Run migration and provider tests**

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add testdata/alpha/kb
git diff --check
git status --short
git commit -m "feat: add shared kb embeddings"
```

---

### Task 18: Shared KB semantic ranking and migration tests

**Pull request:** PR 18

**Files:**
- Modify: `internal/core/kb/kb.go`
- Modify: `internal/core/kb/kb_test.go`
- Modify: `internal/mcp/kb.go`
- Modify: `internal/mcp/kb_test.go`
- Modify: `internal/mcp/rls_integration_test.go`
- Modify: `testdata/alpha/kb/semantic-low-overlap.json`
- Modify: `testdata/alpha/kb/provider-responses.json`
- Modify: `testdata/alpha/manifests/generated-guidance/wormhole-tool-use.md`
- Modify: `testdata/alpha/manifests/generated-guidance/wormhole-operating-loop.md`

**Interfaces:**
- Produces: meaning-bearing ranking, negative controls, re-embedding procedure, and accurate model guidance.
- Consumes: Task 17 embedder and vector schema.

- [ ] **Step 1: Create low-overlap semantic fixtures**

Include pairs that are conceptually related but share few words, plus lexically similar irrelevant decoys.

- [ ] **Step 2: Write failing ranking tests**

Assert related articles rank ahead of decoys. Do not assert arbitrary latency or score thresholds.

- [ ] **Step 3: Add project-isolation tests**

A query in project A must never return project B vectors, including nearest-neighbour edge cases.

- [ ] **Step 4: Implement ranking**

Combine semantic score with existing approved metadata filters. Keep project filtering inside the database query, not post-filtering.

- [ ] **Step 5: Implement re-embedding**

Changing model or version marks old vectors inactive, rebuilds through the new model, and atomically activates the complete replacement.

- [ ] **Step 6: Update generated guidance**

Teach when KB semantic search should precede broad repository reconstruction and distinguish shared KB search from Code Graph.

- [ ] **Step 7: Run ranking, migration, and generated-guidance tests**

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add testdata/alpha/kb
git diff --check
git status --short
git commit -m "feat: rank shared kb by meaning"
```

---

### Task 19: Fabric manifest storage and bootstrap distribution

**Pull request:** PR 19

**Files:**
- Create: `internal/mcp/integration_manifest.go`
- Create: `internal/mcp/integration_manifest_test.go`
- Modify: `internal/mcp/fabric_registry.go`
- Modify: `internal/mcp/sync.go`
- Modify: `internal/mcp/sync_test.go`
- Create: `migrations/000020_integration_manifests.up.sql`
- Create: `migrations/000020_integration_manifests.down.sql`
- Modify: `docs/contracts/README.md`
- Create: `testdata/alpha/manifests/fabric/valid.json`
- Create: `testdata/alpha/manifests/fabric/wrong-project.json`
- Create: `testdata/alpha/manifests/fabric/revoked.json`

**Interfaces:**
- Produces: project-scoped versioned manifest storage and bootstrap/sync delivery.
- Consumes: approved schema from Task 6.

- [ ] **Step 1: Write failing storage tests**

Prove:

* project isolation;
* version history;
* authorised modification;
* immutable historical content;
* digest preservation;
* role filter storage.

- [ ] **Step 2: Add storage schema**

Persist metadata and declarative content. Reject prohibited kinds at write time.

- [ ] **Step 3: Add bootstrap delivery**

Return applicable manifest metadata and content with project bootstrap.

- [ ] **Step 4: Add incremental sync delivery**

Manifest updates and revocations travel through the existing sync protocol with version and digest.

- [ ] **Step 5: Add authorisation tests**

Only explicitly authorised identities may publish or revoke project manifests.

- [ ] **Step 6: Run Fabric storage and sync tests**

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add testdata/alpha/manifests/fabric
git diff --check
git status --short
git commit -m "feat: distribute project manifests through fabric"
```

---

### Task 20: Gateway manifest verification and approval

**Pull request:** PR 20

**Files:**
- Create: `internal/runtime/localapi/manifest.go`
- Create: `internal/runtime/localapi/manifest_test.go`
- Modify: `internal/runtime/localapi/bootstrap.go`
- Modify: `internal/runtime/localapi/materialize.go`
- Modify: `internal/runtime/localstore/localstore.go`
- Modify: `internal/runtime/localstore/localstore_test.go`
- Modify: `internal/runtime/sync/sync.go`
- Modify: `internal/runtime/sync/sync_test.go`
- Modify: `cmd/wormhole/integration.go`
- Modify: `cmd/wormhole/integration_test.go`

**Interfaces:**
- Produces: verified cache, operator notification, preview, approval, postponement, rejection, revocation, and audit.
- Consumes: Task 19 offered manifests and Task 15 materialiser.

- [ ] **Step 1: Write failing verification tests**

Reject:

* wrong project;
* unsupported schema;
* digest mismatch;
* tool-contract mismatch;
* unknown entry kind;
* executable content;
* malformed role filters.

- [ ] **Step 2: Implement verified cache**

Cache only after all checks pass. A failed offered version must not replace the last approved version.

- [ ] **Step 3: Implement operator state transitions**

```text
offered
verified
awaiting_approval
approved
applied
rejected
postponed
revoked
```

A model response cannot cause `approved`.

- [ ] **Step 4: Connect preview and application**

Use Task 15's materialiser. Display a diff before explicit approval.

- [ ] **Step 5: Implement revocation**

Remove or deactivate only managed content. Preserve audit records and user-owned files.

- [ ] **Step 6: Prove offline behaviour**

When Fabric is unavailable, Gateway serves and materialises only the last approved cached version.

- [ ] **Step 7: Run verification, process, and security tests**

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git diff --check
git status --short
git commit -m "feat: verify and approve integration manifests"
```

---

### Task 21: Full automated alpha acceptance loop

**Pull request:** PR 21

**Files:**
- Create: `docs/testing/alpha-validation.md`
- Create: `cmd/gatewayd/alpha_validation_e2e_test.go`
- Create: `testdata/alpha/projects/full-loop/`
- Modify only when the new acceptance test proves a defect: the exact production file owning that defect; add each such path to this **Files** block before changing it

**Interfaces:**
- Produces: deterministic full-system acceptance covering two Gateways, Fabric, PostgreSQL, two SQLite replicas, manifests, KB, offline recovery, and Code Graph.
- Consumes: Tasks 1 to 20.

- [ ] **Step 1: Create the full-loop fixture**

Include:

* one project;
* contributor and reviewer identities;
* one open Task;
* one Channel;
* non-empty KB;
* one approved manifest;
* a Code Graph-enabled Wormhole checkout binding.

- [ ] **Step 2: Write the failing automated scenario**

Topology:

```text
simulated Agent A -> Gateway A -> Fabric -> Gateway B -> simulated Agent B
```

Assert Task, Event, KB, manifest, and Git-pointer propagation.

- [ ] **Step 3: Add Agent A operating-loop assertions**

The simulated agent must:

* inspect identity;
* inspect Tasks;
* search KB;
* inspect Events;
* inspect Code Graph status;
* query Code Graph;
* request bounded source;
* update meaningful state;
* complete the Task;
* record one durable discovery.

- [ ] **Step 4: Add outage and restart**

Stop Fabric, restart Gateway A, perform supported writes, restore Fabric, and assert exactly-once delivery to Gateway B.

- [ ] **Step 5: Add Agent B handoff assertions**

Reviewer receives enough context to identify intent, changed Git pointer, discovery, and relevant Code Graph path without a human relay.

- [ ] **Step 6: Add denial and degradation assertions**

Include missing source permission, stale graph, unavailable embedder, and unapproved manifest update.

- [ ] **Step 7: Run the full process test from a clean environment**

Expected: PASS.

- [ ] **Step 8: Run the complete repository suite**

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add docs/testing/alpha-validation.md testdata/alpha/projects/full-loop
git diff --check
git status --short
git commit -m "test: add full alpha acceptance loop"
```

---

### Task 22: Manual two-model and Code Graph validation

**Pull request:** PR 22

**Files:**
- Create: `docs/testing/manual-alpha-validation-2026-07.md`
- Create: `docs/testing/results/code-graph-baseline.json`
- Create: `docs/testing/results/code-graph-enabled.json`
- Modify only when validation proves a guidance defect: `internal/runtime/localapi/guidance_render.go`
- Modify only when validation proves a guidance defect: `internal/runtime/localapi/guidance_render_test.go`
- Modify only when validation proves a guidance defect: the affected file in `testdata/alpha/manifests/generated-guidance/`, added by exact name before editing

**Interfaces:**
- Produces: release-gate evidence from two different real models or harnesses.
- Consumes: Task 21 deployable alpha environment.

- [ ] **Step 1: Select two distinct model or harness combinations**

Record model, harness, version, permissions, repository commit, manifest version, and Code Graph revision.

- [ ] **Step 2: Run the baseline task without Code Graph**

Capture:

```text
files opened before first correct edit
source bytes exposed
broad searches
turns or elapsed time to relevant path
selected files
task result
human corrections
```

- [ ] **Step 3: Run the comparable task with Code Graph**

Capture the same fields plus every Code Graph call and whether it produced useful value.

- [ ] **Step 4: Run Agent A to Agent B handoff**

Agent B must review using Wormhole context and Git pointers without a human replay. Record misunderstandings, omissions, and manual corrections.

- [ ] **Step 5: Verify graph findings**

For every relationship used in reasoning, record confidence and provenance and whether direct source verification confirmed it.

- [ ] **Step 6: Record negative evidence**

Include useless calls, false leads, stale omissions, excessive source, or incorrect confidence. Do not filter failures from the report.

- [ ] **Step 7: Review Gate C**

State whether:

* automated loop passes;
* manual loop passes;
* fresh models follow guidance;
* Code Graph narrows discovery without lower correctness.

- [ ] **Step 8: Commit**

```bash
git add docs/testing/manual-alpha-validation-2026-07.md docs/testing/results
git commit -m "docs: record manual alpha validation"
```

---

### Task 23: Closed-trial instrumentation and operator guide

**Pull request:** PR 23

**Files:**
- Create: `docs/operators/alpha-validation-trial.md`
- Create: `docs/testing/closed-trial-metrics.md`
- Create after the trial: `docs/testing/results/closed-alpha-trial-2026-07.json`
- Create after the trial: `docs/testing/results/closed-alpha-trial-2026-07.md`
- Create: `internal/runtime/localapi/trial_metrics.go`
- Create: `internal/runtime/localapi/trial_metrics_test.go`

**Interfaces:**
- Produces: controlled trial procedure, consent boundaries, metric schema, support checklist, and final alpha decision template.
- Consumes: Gate C evidence and the releasable alpha build.

- [ ] **Step 1: Define the cohort and consent model**

At least three technically capable coding-agent users. Record what data is collected, where it is stored, retention, redaction, and opt-out.

- [ ] **Step 2: Define the metric schema**

Capture:

```text
installation completion
time to first Gateway MCP call
time to productive work
tool success and denial
context retrieval at session start
human coaching
model handoff
sync recovery
KB relevance
duplicate or low-value KB contributions
Code Graph useful-query rate
files and bytes before correct edit
event noise
task accuracy
context reconstruction avoided
token use before productive work
```

- [ ] **Step 3: Write failing privacy tests**

Assert collected records exclude:

* source bodies;
* bearer tokens;
* private query text unless explicitly consented;
* unrelated repository content;
* cross-project identifiers not required for analysis.

- [ ] **Step 4: Implement minimum instrumentation**

Prefer local structured exports and explicit participant submission over mandatory telemetry. Do not add phone-home behaviour.

- [ ] **Step 5: Write the operator runbook**

Cover installation, enrolment, manifest approval, Code Graph enablement, benchmark task, outage exercise, support escalation, data export, participant withdrawal, and deletion of withdrawn data.

- [ ] **Step 6: Create the decision template**

The final report must choose one:

```text
continue towards beta planning
continue with narrowed scope
repeat alpha after corrective work
stop the current direction
```

- [ ] **Step 7: Run schema and privacy tests**

Expected: PASS.

- [ ] **Step 8: Commit and merge the trial tooling**

```bash
git add docs/operators/alpha-validation-trial.md docs/testing/closed-trial-metrics.md
git diff --check
git status --short
git commit -m "docs: prepare closed alpha validation trial"
```

Merge this implementation PR and deploy the resulting release candidate before collecting trial evidence. Trial results must not be fabricated in fixtures or gathered against an earlier build.

- [ ] **Step 9: Run the closed trial**

Run the operator guide with at least three external participants who already use coding agents. For each participant, record consent, environment, completion or withdrawal, raw structured export, support interventions, failures, and omissions. Store redacted results in:

```text
docs/testing/results/closed-alpha-trial-2026-07.json
docs/testing/results/closed-alpha-trial-2026-07.md
```

Do not mark this step complete merely because three users were invited; three external users must complete the controlled trial.

- [ ] **Step 10: Perform the required comparative evaluation**

For at least one representative Task per participant, compare either guidance-off or Code-Graph-off baseline with the alpha configuration. Use the same Task, checkout revision, permissions, success criteria, and measurement method for both arms. Compare:

```text
tool selection
operating-loop adherence
useful shared-state writes
human correction
Task quality
unnecessary tool volume
source-discovery breadth
review quality
```

Include negative evidence, withdrawn or incomplete runs, and missing measurements. Do not discard unsuccessful calls or participants to improve the result.

- [ ] **Step 11: Apply the Gate D decision rule**

Evaluate whether the evidence reduces manual context relay and repeated reconstruction, improves cross-model continuation, survives interruption, can be learned through managed guidance, narrows source discovery, and avoids disproportionate maintenance, Event noise, and incorrect confidence. Record exactly one decision from Step 6 with supporting and contrary evidence. If only one component shows value, choose narrowed scope rather than preserving the complete platform by assumption.

- [ ] **Step 12: Verify and commit trial evidence**

Run the privacy-schema validator over the real redacted exports, then run:

```bash
go test ./internal/runtime/localapi -run 'TrialMetrics|TrialPrivacy' -count=1
git diff --check
git status --short
```

Expected: tests pass, the two result files are present, at least three completed participant records exist, every completed participant has a comparison, and one Gate D decision is recorded.

Commit the evidence separately from the tooling so the implementation PR remains independently reviewable:

```bash
git add docs/testing/results/closed-alpha-trial-2026-07.json docs/testing/results/closed-alpha-trial-2026-07.md
git commit -m "docs: record closed alpha validation decision"
```

---

## Programme verification commands

Before declaring the programme implementation complete, run the repository's required commands plus, at minimum:

```bash
go test ./...
go test -race ./...
git diff --check
git status --short
```

Run the process-level acceptance command documented in `docs/testing/alpha-validation.md` from a clean environment.

Run the Code Graph benchmark command documented in `docs/testing/code-graph-benchmarks.md`.

Verify the roadmap line by line against merged PRs and issue acceptance criteria.

## Completion evidence checklist

- [ ] PRs 1 to 23 are merged or deliberately superseded with traceable replacements.
- [ ] Gate A evidence is recorded.
- [ ] Gate B evidence is recorded.
- [ ] Gate C evidence is recorded.
- [ ] Three-user closed trial is complete.
- [ ] Gate D decision is recorded.
- [ ] Full suite and race suite pass from the release candidate commit.
- [ ] No beta compatibility claim appears.
- [ ] No full Code Graph V1 claim appears.
- [ ] Deferred work remains outside the release.
