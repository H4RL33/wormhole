# Stage 2 Portable Loop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver one binding-aware Gateway supervisor, crash-safe `wormhole setup`, transactional native Codex/Claude connectors, and the two-clone Git-native portable loop, then stop for review.

**Architecture:** The CLI supplies only canonical cwd context to one user-level `gatewayd`; Gateway resolves the persisted workspace binding and local actor, then delegates project state through the existing six-coordinator `projectstate.Service`. Owner-only identity, setup-journal, and connector stores make setup resumable; Fabric is local-only and Code Graph is hidden at every public boundary.

**Tech Stack:** Go 1.26.5, standard-library Ed25519/SHA-256/JSON/filesystem primitives, existing SQLite/localstore and projectstate packages, native Codex/Claude CLIs behind injected no-shell command runners.

## Global Constraints

- Consume current private Gateway schema v6 exactly; add no private-schema compatibility migration, reset, export, or legacy importer.
- Consume `types.WorkspaceBinding`, `types.ActorEnvelope`, and the six-coordinator `projectstate.Service`; define no parallel scope, actor, tree, operation, digest, or lifecycle authority.
- Git remains the sole accepted project-state authority. Setup never stages, commits, pushes, or executes repository-supplied content.
- Stage 2 performs zero Fabric discovery, DNS, authentication, attach, detach, or remote operations.
- Existing Code Graph implementation packages remain internal, but public CLI/MCP/help/setup/contract exposure is removed; no Code Graph delivery claim is made.
- `wormhole init`, `wormhole join`, and `wormhole connect` are deleted at final cutover with no aliases or compatibility adapters.
- Owner-only identity, setup, connector backup, and operation state remains outside the repository and rejects insecure ownership/modes on Unix; unsupported platforms fail closed.
- Setup renders one complete plan and confirms once before any external mutation. Exact prior/desired state resumes; any third state blocks without mutation.
- Connector operations hold one cross-process `(adapter, connector-name)` lock across recovery, CAS, backup, mutation, verification, rollback, and completion.
- No new external dependency, ORM, web framework, singleton, `init()` registration, shell execution, or control-flow panic.
- Every task records observed RED, minimum GREEN, focused/full verification, independent review, and one bounded commit. Merged coverage remains at least 80 percent.

---

### Task 1: Persist local identities and setup intent

**Files:**
- Modify: `internal/types/identity.go`
- Modify: `internal/types/identity_test.go`
- Create: `internal/runtime/localidentity/store.go`
- Create: `internal/runtime/localidentity/store_unix.go`
- Create: `internal/runtime/localidentity/store_unsupported.go`
- Create: `internal/runtime/localidentity/store_test.go`
- Create: `internal/runtime/localidentity/store_unix_test.go`
- Modify: `docs/module-map.md`

**Interfaces:**
- Consumes: `types.ActorEnvelope`, canonical UUID/time validators, injected clock/random/atomic-write hooks.
- Produces:

```go
type ConfirmedIdentitySelection struct {
    DisplayName string `json:"display_name"`
    Email       string `json:"email,omitempty"`
}
func (s ConfirmedIdentitySelection) Validate() error

type PublicHumanProfile struct {
    HumanPrincipalID string
    DisplayName      string
    PublicKey        []byte
    CreatedAt        time.Time
}

type Store struct { /* private owner-only root and injected dependencies */ }
func Open(root string) (*Store, error)
func (s *Store) EnsureSelectedForSetup(context.Context, string, types.ConfirmedIdentitySelection) (PublicHumanProfile, error)
func (s *Store) Selected(context.Context) (PublicHumanProfile, error)
func (s *Store) ResolveLocalActor(context.Context, ConnectionIdentity) (types.ActorEnvelope, error)
```

- [ ] Add tests that reject invalid/oversized selections, insecure paths/modes, unknown/duplicate JSON, and secret-bearing public output; prove Ed25519 key/profile/selection durability and exact setup-journal idempotence across every injected write-failure boundary.
- [ ] Run `go test ./internal/types ./internal/runtime/localidentity -run 'ConfirmedIdentity|EnsureSelected|Owner|Mode|Fault|Secret' -count=1`. Expected: RED because the type/package does not exist.
- [ ] Implement strict owner-only canonical records and a store-index cross-process lock. Persist setup intent with the reserved human UUID before key creation; make key creation create-if-absent at that UUID; recover any durable prefix to the identical profile/key/selection.
- [ ] Run the focused command, `go test -race ./internal/runtime/localidentity -count=1`, and `git diff --check`. Expected: PASS.
- [ ] Request independent review for identity uniqueness, key secrecy, ownership, crash recovery, and public-result bounds; resolve Critical/Important findings.
- [ ] Commit `feat(identity): persist local setup identities`.

---

### Task 2: Add private cwd routing and server-owned local actors

**Files:**
- Modify: `cmd/wormhole/mcp.go`
- Create: `internal/runtime/localapi/request_scope.go`
- Create: `internal/runtime/localapi/actor.go`
- Create: `internal/runtime/localapi/setup.go`
- Modify: `internal/runtime/localapi/localapi.go`
- Modify: `internal/runtime/localapi/mcp.go`
- Modify: `internal/runtime/localapi/workspace.go`
- Test: `cmd/wormhole/mcp_test.go`
- Test: `internal/runtime/localapi/*_test.go`

**Interfaces:**
- Consumes: Task-1 `localidentity.Store`, `projectstate.Service.ResolveWorkingDirectory`, exact `types.WorkspaceBinding`.
- Produces:

```go
type PrivateRequestContext struct { WorkingDirectory string `json:"working_directory"` }
func WithResolvedBinding(context.Context, types.WorkspaceBinding) context.Context
func ResolvedBinding(context.Context) (types.WorkspaceBinding, error)

type LocalActorResolver interface {
    ResolveLocalActor(context.Context, ConnectionIdentity) (types.ActorEnvelope, error)
}

func (s *Server) PrivateSetupEnsureIdentityRPC(context.Context, SetupIdentityRequest) (localidentity.PublicHumanProfile, error)
```

- [ ] Add registry-wide tests that inject forged cwd/project/workspace/binding/actor values across workspace and project-scoped handlers; require Gateway resolution to overwrite or reject them, strip the private envelope before public decoding, and isolate sibling workspaces.
- [ ] Run `go test ./cmd/wormhole ./internal/runtime/localapi -run 'PrivateContext|Forged|ResolvedBinding|ServerOwnedActor|CrossWorkspace' -count=1`. Expected: RED.
- [ ] Make the stdio bridge observe and canonicalize cwd once per request. In Gateway, resolve the binding before dispatch, attach it only to context, derive the actor from Task 1 and connection identity, and remove ambient/unscoped fallbacks.
- [ ] Run focused tests, `go test -race ./internal/runtime/localapi ./cmd/wormhole -count=1`, and `git diff --check`. Expected: PASS.
- [ ] Review routing authority, private-envelope invisibility, actor forgery, and workspace isolation; resolve findings.
- [ ] Commit `feat(gateway): bind local requests to workspaces`.

---

### Task 3: Construct one complete Gateway supervisor

**Files:**
- Create: `internal/runtime/localapi/supervisor.go`
- Create: `internal/runtime/localapi/providers.go`
- Modify: `cmd/gatewayd/gatewayd.go`
- Modify: `cmd/gatewayd/gatewayd_test.go`
- Modify: `internal/runtime/localapi/localapi.go`
- Test: `internal/runtime/localapi/supervisor_test.go`

**Interfaces:**
- Consumes: Task-2 binding/actor adapters, current localstore schema v6, six-coordinator `projectstate.Service`.
- Produces:

```go
var ErrFabricUnavailable = errors.New("gateway: Fabric is unavailable in local-only mode")
var ErrCodeGraphUnavailable = errors.New("gateway: Code Graph is unavailable")

type FabricRouter interface { /* binding-scoped local-only methods */ }
type CodeGraphProvider interface { /* binding-scoped unavailable methods */ }
type SupervisorDependencies struct {
    Store *localstore.Store
    ProjectState *projectstate.Service
    Identity *localidentity.Store
    Fabric FabricRouter
    CodeGraph CodeGraphProvider
}
func NewSupervisor(SupervisorDependencies) (*Supervisor, error)
func NewLocalOnlyFabricRouter() FabricRouter
func NewDisabledCodeGraphProvider() CodeGraphProvider
```

- [ ] Add failing constructor-table tests for every nil/incomplete dependency, one-Gateway multi-workspace isolation, exact schema-v6 reopen/refusal, and typed local-only/disabled provider failures with zero network/process starts.
- [ ] Run `go test ./cmd/gatewayd ./internal/runtime/localapi -run 'Supervisor|Dependencies|LocalOnlyFabric|DisabledCodeGraph|SchemaV6' -count=1`. Expected: RED.
- [ ] Replace legacy constructors with one complete dependency graph. Wire explicit non-nil unavailable providers and the current projectstate facade; do not expose coordinator internals or recreate Service-owned dependencies.
- [ ] Run focused tests, `go test -race ./cmd/gatewayd ./internal/runtime/localapi -count=1`, and `git diff --check`. Expected: PASS.
- [ ] Review constructor completeness, shutdown ownership, schema assumptions, provider fail-closed behavior, and absence of cycles/fallbacks.
- [ ] Commit `refactor(gateway): construct one supervisor`.

---

### Task 4: Add the Gateway service primitive

**Files:**
- Create: `internal/runtime/config/command.go`
- Create: `internal/runtime/config/command_test.go`
- Create: `internal/runtime/config/service.go`
- Create: `internal/runtime/config/service_linux.go`
- Create: `internal/runtime/config/service_unsupported.go`
- Create: `internal/runtime/config/service_test.go`
- Create: `internal/runtime/config/service_linux_test.go`

**Interfaces:**
- Produces:

```go
type CommandRunner interface {
    Run(context.Context, string, ...string) (stdout []byte, stderr []byte, err error)
}
type ServiceState struct { Installed, Enabled, Active, Ready bool; Diagnostic string }
type GatewayService interface {
    Inspect(context.Context) (ServiceState, error)
    Install(context.Context, ConfirmedServiceChange) error
    Start(context.Context) error
    WaitReady(context.Context) error
}
```

- [ ] Add failing tests for no-shell argv, bounded output, cancellation/process-group termination, exact user unit/socket/runtime paths, active idempotence, stale/incomplete state, unsupported-platform pre-mutation failure, and repository-content non-execution.
- [ ] Run `go test ./internal/runtime/config -run 'CommandRunner|GatewayService|Systemd|Unsupported|RepositoryContent' -count=1`. Expected: RED.
- [ ] Implement the shared runner and conservative systemd-user lifecycle with injected filesystem/runner hooks and exact state readback.
- [ ] Run focused tests, `go test -race ./internal/runtime/config -count=1`, and `git diff --check`. Expected: PASS.
- [ ] Review command injection, process cleanup, unit ownership, idempotence, and unsupported-platform claims.
- [ ] Commit `feat(config): manage the Gateway service`.

---

### Task 5: Add the durable confirmed setup journal

**Files:**
- Create: `internal/runtime/config/setup_journal.go`
- Create: `internal/runtime/config/setup_journal_unix.go`
- Create: `internal/runtime/config/setup_journal_unsupported.go`
- Create: `internal/runtime/config/setup_journal_test.go`
- Create: `internal/runtime/config/setup_journal_unix_test.go`

**Interfaces:**
- Consumes: Task-1 `types.ConfirmedIdentitySelection`.
- Produces:

```go
type StateDigest string // sha256:<64 lowercase hex>
func SHA256StateDigest([]byte) StateDigest
func ParseStateDigest(string) (StateDigest, error)
type SetupStage string
const (
    StageProjectValidated SetupStage = "project_validated"
    StageGatewayReady SetupStage = "gateway_ready"
    StageWorkspaceRegistered SetupStage = "workspace_registered"
    StageIdentitySelected SetupStage = "identity_selected"
    StagePublicationClassified SetupStage = "publication_classified"
    StageBaseImported SetupStage = "base_imported"
    StageConnectorsApplied SetupStage = "connectors_applied"
    StageFinalVerified SetupStage = "final_verified"
)
type ConfirmedChange struct {
    Stage SetupStage `json:"stage"`; Subject, Action string
    PriorDigest, DesiredDigest StateDigest
}
type SetupSelection struct {
    ConnectorAdapters []string
    PublicationVisibility string
    PublicationBindingDigest StateDigest
    Identity types.ConfirmedIdentitySelection
    PlanDigest StateDigest
    Changes []ConfirmedChange
}
type SetupJournal struct { /* canonical v1 identity, root, binding, selection, stages, refs, redacted failure, times */ }
type BackupReference string // connector-backup:v1:<codex|claude>:<UUID>
func OpenSetupJournalStore() (*SetupJournalStore, error)
func OpenSetupJournalStoreAt(string) (*SetupJournalStore, error)
func (s *SetupJournalStore) Begin(context.Context, string) (SetupJournal, error)
func (s *SetupJournalStore) SetSelection(context.Context, string, SetupSelection) error
func (s *SetupJournalStore) BindWorkspace(context.Context, string, types.WorkspaceID) error
func (s *SetupJournalStore) BindIdentity(context.Context, string, string) error
func (s *SetupJournalStore) RecordConnectorBackup(context.Context, string, BackupReference) error
func (s *SetupJournalStore) MarkCompleted(context.Context, string, SetupStage) error
func (s *SetupJournalStore) RecordLastError(context.Context, string, SetupStage, error) error
func (s *SetupJournalStore) BeginConfirmedReplacement(context.Context, string, string, SetupSelection) (SetupJournal, error)
func (s *SetupJournalStore) Resumable(context.Context, string) (SetupJournal, bool, error)
func (s *SetupJournalStore) Complete(context.Context, string) error
```

The eight stage values above are the complete ordered prefix. There is no Fabric or Code Graph selection/stage in journal schema v1 on this branch.

- [ ] Add failing tests for digest vectors; exact required stage/order/uniqueness; nil-before-confirmation and immutable selection; one active journal per canonical root; ambiguous/corrupt/insecure refusal; old-or-new write faults; redaction of tokens, keys, env/config contents and sensitive paths; and explicit confirmed replacement without copied effects.
- [ ] Run `go test ./internal/runtime/config -run 'StateDigest|SetupJournal|ConfirmedPlan|Redact|Owner|Fault' -count=1`. Expected: RED.
- [ ] Implement strict canonical JSON, owner-only per-UUID files, store-index locking, atomic writes, exact prior/desired predicates, terminal states, and `ErrConfirmedPlanDrift` no-write behavior.
- [ ] Run focused tests, `go test -race ./internal/runtime/config -run 'SetupJournal|ConfirmedPlan' -count=1`, and `git diff --check`. Expected: PASS.
- [ ] Review secret boundaries, confirmation immutability, crash recovery, concurrency, and drift semantics.
- [ ] Commit `feat(config): journal confirmed setup plans`.

---

### Task 6: Add read-only Git identity suggestions

**Files:**
- Create: `internal/runtime/config/identity_suggestion.go`
- Create: `internal/runtime/config/identity_suggestion_test.go`

**Interfaces:**
- Consumes: Task-4 `CommandRunner`, canonical repository root.
- Produces:

```go
type GitIdentitySuggestion struct {
    DisplayName string
    Email string
    SigningKey string
    CommitSigning bool
}
func SuggestGitIdentity(context.Context, CommandRunner, string) (GitIdentitySuggestion, error)
```

- [ ] Add failing table tests for exact four-key reads, unset/duplicate/malformed/bounded output, canonical-root containment, OpenPGP-only signing references, no credential-helper/key-file access, and no mutation.
- [ ] Run `go test ./internal/runtime/config -run 'GitIdentitySuggestion' -count=1`. Expected: RED.
- [ ] Implement fixed `git config --local --get` argv reads through the shared runner and strict bounded parsing.
- [ ] Run focused tests and `git diff --check`. Expected: PASS.
- [ ] Review read-only guarantees, root binding, privacy, and signing-key classification.
- [ ] Commit `feat(config): suggest local Git identity`.

---

### Task 7: Implement transactional native Codex and Claude connectors

**Files:**
- Create: `internal/runtime/config/connector/types.go`
- Create: `internal/runtime/config/connector/store.go`
- Create: `internal/runtime/config/connector/coordinator.go`
- Create: `internal/runtime/config/connector/codex.go`
- Create: `internal/runtime/config/connector/claude.go`
- Create: `internal/runtime/config/connector/*_test.go`

**Interfaces:**
- Consumes: Task-4 `CommandRunner`; Task-5 `StateDigest`, confirmed-plan drift, opaque backup reference, redaction/private-platform contracts.
- Produces:

```go
type AdapterName string // codex|claude
type ConnectorEntry struct {
    State EntryState // absent|present
    Scope Scope // user
    Transport Transport // stdio
    Command string
    Args []string
    Env []EnvironmentVariable // sorted unique names
}
type Adapter interface {
    AdapterName() AdapterName
    Discover(context.Context) (Availability, error)
    Inspect(context.Context) (ConnectorEntry, error)
    Plan(context.Context, ConnectorEntry, ConnectorEntry) (ChangePlan, error)
    Apply(context.Context, ChangePlan) error
    Verify(context.Context, ConnectorEntry) error
    Rollback(context.Context, ChangePlan) error
    Remove(context.Context, ConnectorEntry) error
}
type ConfirmedConnectorChange struct {
    Adapter AdapterName; Name string; Action OperationAction
    PlanDigest, ExpectedPriorDigest, DesiredDigest config.StateDigest
}
type OperationStage string // prepared|applied|verified|rolled_back|complete
type BackupStore interface { Put(context.Context, ConnectorBackup) (config.BackupReference, error); Get(context.Context, config.BackupReference) (ConnectorBackup, error) }
type OperationJournal interface { Prepare(context.Context, PrepareOperation) (OperationRecord, error); Active(context.Context, AdapterName, string) (OperationRecord, bool, error); Advance(context.Context, string, OperationStage) error }
type OperationCoordinator interface { WithOperationLock(context.Context, AdapterName, string, func(context.Context) error) error }
func ApplyTransactional(context.Context, Adapter, ConnectorEntry, ConfirmedConnectorChange, BackupStore, OperationJournal, OperationCoordinator) (TransactionResult, error)
func RemoveTransactional(context.Context, Adapter, ConfirmedConnectorChange, BackupStore, OperationJournal, OperationCoordinator) (TransactionResult, error)
func RecoverTransactions(context.Context, Adapter, string, BackupStore, OperationJournal, OperationCoordinator) error
```

- [ ] Add RED tests for exact-version discovery; absent and supported stdio priors; HTTP/OAuth/ambiguous/unknown rejection before backup; canonical store limits; pair-lock two-process serialization; expected-prior CAS; apply/remove crash recovery at every durable stage; exact rollback; concurrent third-state preservation; redaction; and fake-runner isolation from real config.
- [ ] Run `go test ./internal/runtime/config/connector -run 'Codex|Claude|Transactional|Recovery|Rollback|Unsupported|Lock|Redact' -count=1`. Expected: RED.
- [ ] Implement strict native inspection and `prepared/applied/verified/rolled_back/complete` recovery under one continuous lock. Use no health check and never interpret unsupported entries as replaceable.
- [ ] Run focused tests, `go test -race ./internal/runtime/config/connector -count=1`, read-only real-client capability smoke where installed, and `git diff --check`. Expected: PASS with no real connector mutation.
- [ ] Review prior-state preservation, lock scope, crash matrix, strict decoding, secret handling, and native CLI contracts.
- [ ] Commit `feat(config): manage native harness connectors`.

---

### Task 8: Orchestrate journalled `wormhole setup`

**Files:**
- Create: `cmd/wormhole/setup.go`
- Create: `cmd/wormhole/setup_test.go`
- Create: `cmd/wormhole/connector.go`
- Create: `cmd/wormhole/connector_test.go`
- Modify: `cmd/wormhole/main.go`
- Modify: `internal/runtime/localapi/setup.go`
- Test: `cmd/gatewayd/gatewayd_test.go`

**Interfaces:**
- Consumes: Tasks 1–7, projectstate public facade, local-only Fabric and disabled graph providers.
- Produces `wormhole setup` and standalone `wormhole connector list|install|remove <codex|claude> [--yes]`.

- [ ] Add RED end-to-end fake-boundary tests for one complete render/confirmation; no mutation before durable selection; stage ordering; crash/restart after every effect; prior-or-desired verification; drift no-write; unavailable Gateway recovery; identity idempotence; publication digest acknowledgement; connector failure preserving imported workspace and restoring connector prior; and zero Fabric/Code Graph contact.
- [ ] Run `go test ./cmd/wormhole ./cmd/gatewayd ./internal/runtime/localapi -run 'Setup|Connector|Confirmation|Resume|Drift|LocalOnly|DisabledCodeGraph' -count=1`. Expected: RED.
- [ ] Implement the canonical-root Gateway client and staged orchestrator. Private setup RPCs resolve binding/actor inside Gateway and return only bounded public readback. Standalone connector commands use the same transaction coordinator and confirmation/CAS rules.
- [ ] Run focused tests, `go test -race ./cmd/wormhole ./internal/runtime/localapi -run 'Setup|Connector' -count=1`, and `git diff --check`. Expected: PASS.
- [ ] Review consent boundary, stage dependencies, restart behavior, output privacy, and parity with projectstate semantics.
- [ ] Commit `feat(cli): orchestrate durable setup`.

---

### Task 9: Hard-cut commands and prove the portable loop

**Files:**
- Modify: `cmd/wormhole/main.go` and CLI tests
- Modify: `internal/runtime/localapi` registrations/tests
- Delete: `cmd/wormhole/init.go`, `cmd/wormhole/init_test.go`, legacy connect/join helpers and tests
- Modify: `docs/contracts/alpha-contract.json`, `docs/contracts/README.md`
- Modify: `README.md`, `docs/claude-code-connector.md`, `docs/compatibility.md`, `agents/README.md`, `docs/implementation-rules.md`
- Create: `docs/superpowers/reviews/2026-08-26-stage2-portable-loop-report.md`

**Interfaces:**
- Produces the final Stage-2 command/MCP inventory and two-clone acceptance evidence.

- [ ] Add RED contract/architecture tests requiring removal of `init/join/connect`, join-shaped agent registration, and every public Code Graph CLI/MCP/help/setup/contract entry; require top-level workspace commands and CLI/MCP equality for status/diff/import/checkpoint/stash and publication acknowledgement.
- [ ] Add a clean-clone integration harness that registers/imports tracked state, applies an attributed mutation, obtains diff/review digest, checkpoints without Git staging/commit/push, commits through normal Git in the fixture, then creates a second clone with fresh private state and proves equal accepted portable state with no private/operational rows.
- [ ] Run the focused cutover/portable-loop tests. Expected: RED while legacy and Code Graph public surfaces remain.
- [ ] Delete legacy/publicly hidden surfaces and update the contract/docs atomically. Do not delete internal Code Graph packages or their internal tests.
- [ ] Run `rg -l --glob '*.go' --glob '*.md' --glob '*.json' 'runJoin|runConnect|runInit|wormhole (join|connect|init)|case "(join|connect|init)"|join_connect|connect\.opencode|agentJoinRegisterArgs|isJoinRegisterArgs|proxyRegister|localJoinResult|wormhole\.code_graph|code.graph' cmd/wormhole internal/runtime/localapi docs/contracts README.md docs/claude-code-connector.md docs/compatibility.md`. Expected: no legacy/public Code Graph exposure; any permitted historical reference must be outside this live-surface scan.
- [ ] Run focused packages, `make check`, `make release-test`, and `make release-rehearsal`. Expected: PASS and merged coverage at least 80 percent.
- [ ] Request broad whole-branch review against the approved design for authority, durability, privacy, command inventory, behavior parity, and scope. Resolve all Critical/Important findings.
- [ ] Commit `feat(cli): complete Gateway setup cutover`.
- [ ] In a detached `git clone --no-local` of the exact candidate, run the focused setup/portable-loop tests, `go test -race ./cmd/wormhole ./cmd/gatewayd ./internal/runtime/localapi ./internal/runtime/config/... -count=1`, and contract scans. Push the branch only after all pass, record exact SHA/evidence, and pause for human go/no-go.

## Dependency Order

```text
1 identity -> 2 routing -> 3 supervisor
4 service runner -> 5 setup journal -> 6 Git suggestions
4 + 5 -> 7 connectors
1..7 -> 8 setup orchestration -> 9 hard cut and portable-loop gate
```

Tasks 4 and 5 may be implemented in parallel after Task 1 only if they edit no shared files. All other tasks follow the order above. No multi-Fabric, OIDC/private identity, issue-56 evidence collection, or Code Graph delivery work is authorized by this plan.
