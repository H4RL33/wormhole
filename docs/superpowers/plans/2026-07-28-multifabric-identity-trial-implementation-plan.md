# Multi-Fabric, Private Identity, and Trial Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement Slices D, E, and F so each registered workspace can bind immutably to one Git-aware Fabric stream, public callers have key-continuity identification only, private actions derive authenticated human and accountable-agent provenance at the server, legacy alpha state migrates without authority invention, and issue #56 closes only from reviewed real four-VM evidence.

**Architecture:** Slice A owns portable repository, workspace, actor, tree, snapshot,
digest, and reducer contracts. Gateway schema version 7 adds explicit Fabric profiles and
complete bindings after the activity migration. Fabric may cache canonical
portable proposal/accepted trees for validated reconstruction, but Git observation alone
accepts them. Separate branch/stream-scoped `ActivityV1` transport/store/queue carries
operational collaboration under finite retention; it is never `OperationV1` or complete-
tree authority.

**Tech Stack:** Go 1.26.5; standard-library Ed25519, SHA-256, JSON, HTTP, SQLite, and Postgres access; existing `modernc.org/sqlite`, `github.com/BurntSushi/toml`, and `golang-migrate`; after the explicit human approval gate only, `github.com/coreos/go-oidc/v3/oidc` v3.20.0 and `golang.org/x/oauth2` v0.36.0.

**Branch gate:** This plan runs only on a separate branch after the portable-loop
whole-branch review and explicit human go/no-go. Do not implement any task from this plan
on the portable-loop branch. It does not consume the optional Code Graph branch; Code
Graph remains disabled throughout the issue-56 trial and no Code Graph delivery claim is
made.

## Global constraints

- RFC-0001, RFC-0003, and `docs/superpowers/specs/2026-07-28-git-native-wormhole-architecture-design.md` are authoritative.
- Consume `types.RepositoryIdentity`, `types.WorkspaceBinding`, the complete
  `types.ActorEnvelope`, and the strict `internal/types/projectstate` tree/operation/
  canonical-digest APIs; do not define parallel repository, binding, actor, snapshot,
  digest, operation, decoder, or canonicalization types.
- `internal/types/projectstate` may use the repository's existing `github.com/BurntSushi/toml`; other `internal/types` files remain standard-library-only.
- Git remains code truth and tracked-state acceptance authority. Fabric stores Git metadata and canonical `.wormhole/` tree bytes, never repository source bodies.
- Canonical tree bytes in Fabric are validated portable replicas/proposals, not
  operational activity and not acceptance authority. Only independently observed Git on
  the configured repository/ref accepts portable state.
- Fabric must expose an effective finite retention policy before accepting `ActivityV1`:
  presence is restart-discardable; ordinary activity becomes eligible when older than
  30 days **or** outside the newest 10,000 unprotected workspace rows and is pruned in
  `(created_at, activity_id)` ascending order; lifecycle evidence is excluded until
  terminal, then retained for exactly 30 days by default or a configured longer finite
  duration. Protected rows may exceed the cap. Expiry never mutates portable Git state.
- One workspace has zero or one writable Fabric binding. Profile, Fabric instance, remote project, stream, repository, and canonical ref identifiers never retarget silently.
- `fabric_profiles.credential_ref` is the sole Gateway authority for a Fabric credential reference. Bindings, cursors, queues, logs, and tracked hints contain no credential reference or raw secret.
- A copied upstream hint whose immutable `origin` repository identity differs from the canonical identity causes zero upstream Fabric network calls, including discovery, DNS, authentication, and detach.
- Version-1 sync compatibility means the existing v1 request structs, strict decode behavior, result JSON, error strings, handler behavior, and the v1 branch of each credential-authenticated compatibility descriptor remain stable. Public/identification-only registration never exposes v1 or its raw scope IDs. The private compatibility descriptor may gain an explicit v2 branch; whole-descriptor byte equality is not required.
- Public key continuity is pseudonymous identification, not verified-human authentication. Private assurance is issued only after active human authentication, membership, ownership where applicable, Passport, and unrevoked credential checks.
- WebAuthn/passkeys are not enabled in this release: add no route, flag, dependency, database object, or dormant code path for WebAuthn.
- Private Git observation uses a separately configured Fabric-server GitHub credential. Gateway never reads Git credential-helper output, OAuth tokens, SSH keys, signing private keys, or Fabric Git credentials.
- Every project-scoped Postgres table has forced RLS with `USING` and `WITH CHECK`. Every relationship between project-scoped rows uses a composite tenant foreign key, not merely two independent single-column foreign keys.
- Raw tokens, authorization headers, OIDC codes, refresh tokens, recovery codes, private keys, credential paths, source, private query text, and durable project identifiers forbidden by the trial schema never enter Git, MCP results, logs, events, diagnostics, or trial evidence.
- All successful Core mutations and their immutable actor audit row commit in one project-scoped Postgres transaction. A caller-supplied actor envelope is never authorization input.
- No ORM, web framework, global singleton, `init()` registration, control-flow panic, or new datastore.
- Every behavior change starts with a focused failing test, then the minimum implementation, focused GREEN, `make check`, and a task commit. Merged statement coverage remains at least 80%.

---

## Canonical cross-slice contracts consumed

Slice A owns these exact records and behavior. This plan imports them; Task 1 tests conformance but does not redefine them.

```go
// internal/types/workspace.go
type WorkspaceBinding struct {
    Scope              WorkspaceScope
    Checkout           CheckoutIdentity
    Repository         RepositoryIdentity
    AcceptedRef        string
    AcceptedCommitSHA  string
    AcceptedTreeDigest string
}
```

`WorkspaceBinding.Validate` requires a non-empty project/workspace scope, canonical path/device/inode checkout identity, valid `RepositoryIdentity`, a canonical `refs/heads/...` accepted ref or the Slice-A detached form, a lowercase 40- or 64-hex commit SHA, and `AcceptedTreeDigest` matching `sha256:[0-9a-f]{64}`.

```go
// internal/types/identity.go
type ActorEnvelope struct {
    ActorKind          ActorKind `json:"actor_kind"`
    HumanPrincipalID   string    `json:"human_principal_id,omitempty"`
    AgentID            string    `json:"agent_id,omitempty"`
    AccountableHumanID string    `json:"accountable_human_id,omitempty"`
    SessionID          string    `json:"session_id,omitempty"`
    HarnessName        string    `json:"harness_name,omitempty"`
    HarnessVersion     string    `json:"harness_version,omitempty"`
    ModelName          string    `json:"model_name,omitempty"`
    ModelVersion       string    `json:"model_version,omitempty"`
    Assurance          Assurance `json:"assurance"`
    OccurredAt         time.Time `json:"occurred_at"`
}
func (e ActorEnvelope) PrincipalID() string
func (e ActorEnvelope) Validate() error
func (e ActorEnvelope) ValidateLocalAction() error
func (e ActorEnvelope) ValidateHistorical() error
```

`Validate` performs assurance-aware structural validation for a newly constructed envelope. `ValidateLocalAction` validates a newly issued local action and rejects public/private/legacy/unknown issuance. `ValidateHistorical` is reserved for decoding or migrating already-persisted envelopes, including legacy/unknown history. Public and private issuers construct a fresh envelope from verified server state, call `Validate`, and then apply their stricter issuer checks. Neither issuer mutates history or upgrades `legacy`/`unknown` assurance.

```go
// internal/types/projectstate
type Digest string
type Tree []File
type Snapshot struct { /* canonical Slice-A fields */ }
type OperationV1 struct { /* canonical Slice-A fields */ }

func DecodeTree(Tree) (Snapshot, error)
func EncodeTree(Snapshot) (Tree, error)
func Validate(Snapshot) error
func DigestTree(Tree) (Digest, error)
func CanonicalJSON(any) ([]byte, error)
func DecodeOperation([]byte) (OperationV1, error)
func CanonicalOperation(OperationV1) ([]byte, error)
func DigestCanonicalJSON(any) (Digest, error)
func DigestCanonicalMarkdown([]byte) (Digest, error)
func ApplyOperation(Snapshot, OperationV1) (Snapshot, error)
```

Fabric persists `Tree` with one server-local, deterministic length-prefixed binary container; it does not create another semantic codec. On every read it decodes the container to `Tree`, calls `DecodeTree`, `Validate`, and `DigestTree`, and rejects a mismatch before serving or applying an operation.
Persisted operation JSON is equally untrusted: every insert uses `CanonicalOperation` and
`DigestCanonicalJSON`, while every read/restart uses `DecodeOperation`, requires the
decoded ID to equal its `operation_id` column, requires `CanonicalOperation(decoded)` to
byte-match the stored bytes, and requires `DigestCanonicalJSON(decoded)` to equal the
stored digest before serving, replaying, or reconstructing a transition.

## New shared routing and authorization records

Task 1 adds only these plain records:

```go
// internal/types/routing.go
type FabricMode string
const (
    FabricModePublic  FabricMode = "public"
    FabricModePrivate FabricMode = "private"
)
type FabricProfile struct {
    ProfileID        string
    Alias            string
    FabricInstanceID string
    BaseURL          string
    Mode             FabricMode
    CredentialRef    string
}
type RemoteBindingKey struct {
    ProjectID        string
    WorkspaceID      WorkspaceID
    FabricInstanceID string
    RemoteProjectID  string
    StreamID         string
}
type FabricBinding struct {
    Workspace         WorkspaceBinding
    ProfileID         string
    FabricInstanceID  string
    RemoteProjectID   string
    StreamID          string
    AttachmentRef     string
    CanonicalRef      string
    Writable          bool
}
func (p FabricProfile) Validate() error
func (b FabricBinding) ValidateWithProfile(FabricProfile) error
func (b FabricBinding) RemoteKey() RemoteBindingKey

// internal/types/identity.go; appended without changing ActorEnvelope.
type ActorScope struct {
    Actor         ActorEnvelope
    ProjectID     string
    MembershipID  string
    PassportID    string
    CredentialID  string
    Roles         []string
    Permissions   []string
}
func (s ActorScope) Validate() error
func (s ActorScope) HasPermission(string) bool
```

`FabricBinding.ValidateWithProfile` requires `Workspace.Validate`, full non-empty UUID
identifiers including the opaque `AttachmentRef`, `FabricInstanceID ==
profile.FabricInstanceID`, exact repository/ref equality with the workspace binding, and
no credential field. `ActorScope.Validate` calls `Actor.Validate` and checks structural
scope consistency only. Task 9's `identity.Store.ResolvePrivateCredential` and
`mcp.ResolvePublicIssuedScope` derive fresh scopes from server records and perform
issuer-specific validation.

## File ownership map

| Path | Responsibility |
|---|---|
| `internal/types/routing.go` | plain profile, binding, and immutable remote-key types |
| `internal/runtime/localstore/migrations/000006_fabric_routes.sql` | one-way local v6 profile/binding/cursor/hint-recovery migration |
| `internal/runtime/localstore/migrations/000007_sync_binding.sql` | one-way local v7 complete-key queue/audit quarantine migration |
| `internal/runtime/localstore/fabric_routes.go` | profile and binding repositories; profile-only credential resolution |
| `internal/runtime/sync/queue_repo.go` | complete-key durable queue and conflict audit |
| `internal/core/git/stream_codec.go` | deterministic storage container for canonical `projectstate.Tree` |
| `internal/core/git/streams.go` | Postgres stream version/precondition store using shared reducer |
| `internal/core/git/github_observer.go` | exact-commit GitHub repository/ref/`.wormhole/` observer |
| `internal/mcp/public_auth.go` | Ed25519 continuity proof and public-scope issuer |
| `internal/core/identity/private_schema_test.go` | private schema, composite-FK, RLS, and immutability proof |
| `internal/core/identity/humans.go` | human/authenticator/bootstrap/invitation/member lifecycle |
| `internal/core/identity/ownership.go` | project-agent ownership and transfer |
| `internal/core/identity/sessions.go` | human access/refresh families and agent harness sessions |
| `internal/core/identity/credentials.go` | agent credentials and scoped PATs |
| `internal/core/identity/actors.go` | private scope issuance and immutable actor audit |
| `internal/webui/oidc.go` | OIDC challenge, code+PKCE, device, refresh, and recovery HTTP endpoints |
| `internal/mcp/mutation.go` | project transaction coordinating Core mutation and audit |
| `internal/mcp/{agent,channel,git,integration_manifest,kb,sync,task}.go` | server-derived `ActorScope` handlers |
| `cmd/wormhole/fabric_auth.go` | human auth/member/ownership/PAT CLI |

---

### Task 1: Reconcile routing and actor issuer boundaries

**Files:**
- Create: `internal/types/routing.go`
- Create: `internal/types/routing_test.go`
- Modify: `internal/types/identity.go`
- Modify: `internal/types/identity_test.go`

**Interfaces:**
- Consumes: the canonical Slice-A APIs above.
- Produces: `FabricProfile`, `RemoteBindingKey`, `FabricBinding`, `ActorScope`, and `ErrInvalidFabricRoute`.

- [ ] **Step 1: Add failing canonical-boundary tests**

Add these named table tests:

```go
func TestFabricBindingRequiresCanonicalWorkspaceAndMatchingInstance(t *testing.T)
func TestFabricBindingHasNoCredentialAuthority(t *testing.T)
func TestRemoteBindingKeyRejectsEveryPartialCombination(t *testing.T)
func TestActorScopeValidateDoesNotUpgradeAssurance(t *testing.T)
```

The first test mutates each UUID, repository identity, canonical ref, and instance equality independently. The second uses reflection to fail if `FabricBinding` or `RemoteBindingKey` gains a field containing `credential`, `token`, or `secret`. The actor tests prove `ValidateLocalAction` accepts only a newly issued local envelope and `ValidateHistorical` accepts structurally valid persisted assurances without upgrading them.

- [ ] **Step 2: Run RED**

Run: `go test ./internal/types -run 'Test(FabricBinding|RemoteBindingKey|ActorScope)' -count=1`

Expected: FAIL because routing and actor-scope contracts are absent; existing Slice-A actor tests remain PASS.

- [ ] **Step 3: Implement the exact records and validators**

Implement only the routing and structural actor-scope records shown above. Database-backed private scope derivation depends on migration 22 and is implemented in Task 9; Task 1 must not invent authority from tables that do not yet exist.

- [ ] **Step 4: Run GREEN and the shared-package import guard**

Run:

```bash
go test ./internal/types -run 'Test(FabricBinding|RemoteBindingKey|Actor)' -count=1
go list -deps ./internal/types/... | rg 'internal/runtime|internal/core|internal/mcp' && exit 1 || true
```

Expected: tests PASS; the dependency scan prints nothing. `internal/types/projectstate` may still list `github.com/BurntSushi/toml`.

- [ ] **Step 5: Commit**

```bash
git add internal/types/routing.go internal/types/routing_test.go internal/types/identity.go internal/types/identity_test.go
git commit -m "feat: freeze Fabric routing contracts"
```

### Task 2: Install one-way local schemas v6/v7 and complete-key repositories

**Files:**
- Create: `internal/runtime/localstore/migrations/000006_fabric_routes.sql`
- Create: `internal/runtime/localstore/migrations/000007_sync_binding.sql`
- Modify: `internal/runtime/localstore/migrations.go`
- Modify: `internal/runtime/localstore/migrations_test.go`
- Create: `internal/runtime/localstore/fabric_routes.go`
- Create: `internal/runtime/localstore/fabric_routes_test.go`
- Modify: `internal/runtime/sync/queue_repo.go`
- Modify: `internal/runtime/sync/queue_repo_test.go`
- Modify: `internal/runtime/sync/status.go`
- Modify: `internal/runtime/sync/sync.go`
- Modify: `internal/runtime/sync/sync_test.go`
- Modify: `cmd/gatewayd/gatewayd.go`
- Modify: `cmd/gatewayd/gatewayd_test.go`

**Interfaces:**
- Consumes: `types.WorkspaceBinding`, Task 1 routing types, committed Slice-A
  `000001`/`000002`, the amendment-owned `000003_workspace_publication.sql`, Task-5-owned
  `000004_checkpoint_publication_review.sql`, the reviewed Task-6A
  `000005_workspace_activity.sql` implementation commit, and its exact activity/policy/
  promotion interfaces, Slice-A's conflict gate/sentinel, and the single
  `gateway_schema_migrations` ledger. It has no Code Graph dependency; that separate
  branch consumes the schema produced here. Task 2 must not begin from schema version 2.
- Produces: `GatewaySchemaVersion = 6` after `000006_fabric_routes.sql`, then
  `GatewaySchemaVersion = 7` after `000007_sync_binding.sql`, `FabricRouteRepo`,
  complete-key `QueueRepo`, quarantine repositories, profile-only `CredentialSource`,
  and the conflict-aware atomic `MarkDelivered` boundary used by Task 6.

Repository signatures are exact:

```go
// Consumed from internal/runtime/localstore; do not redeclare another shape/error.
type WorkspaceConflictGate interface {
    HasOpenConflicts(context.Context, types.WorkspaceScope) (bool, error)
}

type FabricRouteRepo struct { db *sql.DB }
func (r *FabricRouteRepo) CreateProfile(context.Context, types.FabricProfile) error
func (r *FabricRouteRepo) UpdateProfile(context.Context, profileID, expectedInstanceID, baseURL, credentialRef string) error
func (r *FabricRouteRepo) GetProfile(context.Context, profileID string) (types.FabricProfile, error)
func (r *FabricRouteRepo) ListProfiles(context.Context) ([]types.FabricProfile, error)
func (r *FabricRouteRepo) AttachWorkspace(context.Context, types.FabricBinding) error
func (r *FabricRouteRepo) DetachWorkspace(context.Context, types.WorkspaceScope, string) error
func (r *FabricRouteRepo) GetBinding(context.Context, types.WorkspaceScope) (types.FabricBinding, error)
func (r *FabricRouteRepo) GetRoute(context.Context, types.WorkspaceScope) (types.FabricBinding, types.FabricProfile, error)
func (r *FabricRouteRepo) UpdateCursor(context.Context, types.RemoteBindingKey, int64, string) error

type CredentialSource interface { Read(context.Context, string) (string, error) }
type QueueRepo struct { db *sql.DB }
func (r *QueueRepo) Enqueue(context.Context, types.RemoteBindingKey, projectstate.OperationV1, int) (QueueEntry, error)
func (r *QueueRepo) EnqueueTx(context.Context, *sql.Tx, types.RemoteBindingKey, projectstate.OperationV1, int) (QueueEntry, error)
func (r *QueueRepo) ListPending(context.Context, types.RemoteBindingKey, int) ([]QueueEntry, error)
func (r *QueueRepo) PendingCount(context.Context, types.RemoteBindingKey) (int, error)
func (r *QueueRepo) MarkDelivered(context.Context, types.RemoteBindingKey, string) error
func (r *QueueRepo) GetEntry(context.Context, types.RemoteBindingKey, string) (QueueEntry, error)
func (r *QueueRepo) DeleteEntry(context.Context, types.RemoteBindingKey, string) error
```

`cmd/gatewayd` wires the same non-nil `*localstore.WorkspaceRepo` as the v2 sync
engine's `WorkspaceConflictGate`; no project-only, workspace-only, ambient-current, or
optional permissive implementation is allowed. `QueueRepo.MarkDelivered` owns the local
post-network concurrency boundary: on one dedicated SQLite connection it executes
`BEGIN IMMEDIATE`, rechecks `workspace_conflicts` with the queue key's exact
`(project_id, workspace_id)` and `state='open'`, and only then updates the exact complete
remote key plus operation ID from pending to delivered. An open row rolls back and
returns `localstore.ErrWorkspaceConflicted`; the pending queue row remains byte-identical,
including operation bytes/digest, priority, timestamps, and NULL delivered_at. Resolved
rows and another project/workspace never block. A storage/check failure also rolls back
without updating queue, cursor, or audit state.
`localstore.ErrWorkspaceConflicted` is consumed directly; this plan adds no sentinel
declaration or runtime alias.

- [ ] **Step 1: Write failing migration, direct-SQL, and repository tests**

Add `TestGatewayMigration6Fresh`, `TestGatewayMigration5To6Routes`, `TestGatewayMigration7QuarantinesLegacyRows`, `TestGatewayMigration6FailureRollsBackWithoutLedgerAdvance`, `TestGatewayMigration7FailureRollsBackWithoutLedgerAdvance`, `TestGatewayMigrationLoaderAcceptsOnlyNumberedSQL`, `TestGatewayMigrationsAreOneWay`, `TestFabricProfileIsSoleCredentialAuthority`, `TestFabricBindingRejectsWorkspaceMismatchByDirectSQL`, `TestCursorRejectsBindingMismatchByDirectSQL`, `TestQueueRejectsBindingMismatchByDirectSQL`, `TestCompleteKeyQueueIsolation`, `TestCredentialRotationKeepsOneEngine`, and `TestFabricFailureDoesNotBlockLocalWriteOrOtherFabric`. Add `TestQueueMarkDeliveredRechecksOpenConflictAtomically`, `TestQueueMarkDeliveredConflictScopeIsolation`, and `TestGatewayWiresExactWorkspaceConflictGate`. The first captures the complete pending row, inserts an exact-workspace open conflict before delivery, requires `errors.Is(err, localstore.ErrWorkspaceConflicted)`, and compares every row byte/field after reopen. The isolation table covers resolved-only, same project/different workspace, different project, and exact open conflict. Direct SQL tests expect SQLite `SQLITE_CONSTRAINT_FOREIGNKEY`, not repository validation errors. The loader test rejects `.up.sql`, `.down.sql`, unnumbered files, duplicate versions, and version gaps; it embeds only `^[0-9]{6}_[a-z0-9_]+\.sql$`.

- [ ] **Step 2: Run RED**

Run: `go test ./internal/runtime/localstore ./internal/runtime/sync ./cmd/gatewayd -run 'Test(GatewayMigration|FabricProfile|FabricBinding|Cursor|Queue|CredentialRotation|FabricFailure|GatewayWiresExactWorkspaceConflictGate)' -count=1`

Expected: FAIL because migrations 6/7 and complete-key APIs are absent.

- [ ] **Step 3: Add the complete one-way migration-6 route SQL**

`000006_fabric_routes.sql` is exactly the following route/profile portion; it contains no queue rename or queue copy:

```sql
PRAGMA foreign_keys = ON;

CREATE TABLE fabric_profiles (
  profile_id TEXT NOT NULL,
  alias TEXT NOT NULL UNIQUE,
  fabric_instance_id TEXT NOT NULL,
  base_url TEXT NOT NULL,
  mode TEXT NOT NULL CHECK(mode IN ('public','private')),
  credential_ref TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(profile_id),
  UNIQUE(profile_id,fabric_instance_id),
  UNIQUE(fabric_instance_id)
);

CREATE TABLE workspace_fabric_bindings (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  profile_id TEXT NOT NULL,
  fabric_instance_id TEXT NOT NULL,
  remote_project_id TEXT NOT NULL,
  stream_id TEXT NOT NULL,
  attachment_ref TEXT NOT NULL,
  repository_provider TEXT NOT NULL,
  repository_immutable_id TEXT NOT NULL,
  canonical_ref TEXT NOT NULL,
  writable INTEGER NOT NULL CHECK(writable IN (0,1)),
  state TEXT NOT NULL CHECK(state IN ('active','detached')),
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  detached_at TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id),
  UNIQUE(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id),
  UNIQUE(fabric_instance_id,attachment_ref),
  FOREIGN KEY(project_id,workspace_id)
    REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE,
  FOREIGN KEY(profile_id,fabric_instance_id)
    REFERENCES fabric_profiles(profile_id,fabric_instance_id) ON DELETE RESTRICT,
  CHECK((state='active' AND detached_at IS NULL) OR
        (state='detached' AND writable=0 AND detached_at IS NOT NULL))
);

CREATE UNIQUE INDEX workspace_one_active_writable_fabric
  ON workspace_fabric_bindings(project_id,workspace_id)
  WHERE writable=1 AND state='active';

CREATE TABLE fabric_cursors (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  fabric_instance_id TEXT NOT NULL,
  remote_project_id TEXT NOT NULL,
  stream_id TEXT NOT NULL,
  stream_version INTEGER NOT NULL CHECK(stream_version >= 0),
  pull_cursor TEXT NOT NULL DEFAULT '',
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id),
  FOREIGN KEY(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id)
    REFERENCES workspace_fabric_bindings(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id)
    ON DELETE CASCADE
);

CREATE TABLE legacy_fabric_profile_recoveries (
  recovery_id TEXT PRIMARY KEY,
  source_server_url TEXT NOT NULL,
  source_project_id TEXT NOT NULL,
  source_credential_path_hash TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('quarantined','completed','rejected')),
  completed_profile_id TEXT,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  completed_at TIMESTAMP,
  FOREIGN KEY(completed_profile_id) REFERENCES fabric_profiles(profile_id) ON DELETE RESTRICT
);

CREATE TABLE legacy_fabric_hint_recoveries (
  recovery_id TEXT NOT NULL,
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  source_hint_json TEXT NOT NULL,
  reason TEXT NOT NULL CHECK(reason IN ('missing_fabric_instance','missing_stream','ambiguous_workspace','fork_mismatch')),
  state TEXT NOT NULL CHECK(state IN ('quarantined','completed','rejected')),
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  completed_at TIMESTAMP,
  PRIMARY KEY(recovery_id,project_id,workspace_id),
  FOREIGN KEY(project_id,workspace_id) REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE
);

```

The migration runner owns `BEGIN IMMEDIATE`, foreign-key enablement, statement rollback,
and inserting ledger version 6 only after this script completes. It shape-checks versions
1–5 before applying version 6.

- [ ] **Step 4: Add the complete one-way migration-7 sync-binding SQL**

`000007_sync_binding.sql` is exactly:

```sql
PRAGMA foreign_keys = ON;
ALTER TABLE sync_queue RENAME TO sync_queue_v2_legacy;
ALTER TABLE sync_audit RENAME TO sync_audit_v2_legacy;

CREATE TABLE sync_queue (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  fabric_instance_id TEXT NOT NULL,
  remote_project_id TEXT NOT NULL,
  stream_id TEXT NOT NULL,
  id TEXT NOT NULL,
  operation_json TEXT NOT NULL,
  operation_digest TEXT NOT NULL CHECK(operation_digest GLOB 'sha256:[0-9a-f]*' AND length(operation_digest)=71),
  priority INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  delivered_at TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,id),
  FOREIGN KEY(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id)
    REFERENCES workspace_fabric_bindings(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id)
    ON DELETE CASCADE
);
CREATE TABLE sync_audit (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  fabric_instance_id TEXT NOT NULL,
  remote_project_id TEXT NOT NULL,
  stream_id TEXT NOT NULL,
  id TEXT NOT NULL,
  conflict_json TEXT NOT NULL,
  actor_json TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,id),
  FOREIGN KEY(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id)
    REFERENCES workspace_fabric_bindings(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id)
    ON DELETE CASCADE
);
CREATE TABLE legacy_sync_queue_recoveries (
  id TEXT PRIMARY KEY,
  namespace_id TEXT NOT NULL,
  entity_type TEXT NOT NULL,
  entity_id TEXT NOT NULL,
  operation TEXT NOT NULL,
  payload TEXT NOT NULL,
  priority INTEGER NOT NULL,
  created_at TIMESTAMP NOT NULL,
  updated_at TIMESTAMP NOT NULL,
  reason TEXT NOT NULL CHECK(reason='missing_immutable_binding')
);
CREATE TABLE legacy_sync_history (
  id TEXT PRIMARY KEY,
  namespace_id TEXT NOT NULL,
  record_kind TEXT NOT NULL CHECK(record_kind IN ('delivered_queue','conflict_audit')),
  record_json TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL
);
INSERT INTO legacy_sync_queue_recoveries
  (id,namespace_id,entity_type,entity_id,operation,payload,priority,created_at,updated_at,reason)
SELECT id,namespace_id,entity_type,entity_id,operation,payload,priority,created_at,updated_at,'missing_immutable_binding'
FROM sync_queue_v2_legacy WHERE delivered_at IS NULL;
INSERT INTO legacy_sync_history(id,namespace_id,record_kind,record_json,created_at)
SELECT id,namespace_id,'delivered_queue',json_object(
  'entity_type',entity_type,'entity_id',entity_id,'operation',operation,
  'payload',json(payload),'priority',priority,'delivered_at',delivered_at),created_at
FROM sync_queue_v2_legacy WHERE delivered_at IS NOT NULL;
INSERT INTO legacy_sync_history(id,namespace_id,record_kind,record_json,created_at)
SELECT id,namespace_id,'conflict_audit',json_object(
  'entity_type',entity_type,'entity_id',entity_id,'conflict_type',conflict_type,
  'server_value',server_value,'local_value',local_value,
  'resolved_value',resolved_value,'resolved_by',resolved_by),created_at
FROM sync_audit_v2_legacy;
DROP TABLE sync_queue_v2_legacy;
DROP TABLE sync_audit_v2_legacy;
CREATE INDEX sync_queue_pending
  ON sync_queue(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,priority DESC,created_at)
  WHERE delivered_at IS NULL;
CREATE INDEX sync_audit_recent
  ON sync_audit(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,created_at DESC);
```

The runner embeds only files matching `^[0-9]{6}_[a-z0-9_]+\.sql$`, sorts by numeric prefix, requires the sequence `000001` through `GatewaySchemaVersion` without gaps or duplicates, and never looks for `.up.sql`/`.down.sql`. It applies each file once under its own `BEGIN IMMEDIATE` and advances the ledger only after commit. Gateway migrations are one-way; restoring an older binary requires restoring a pre-migration database backup, not executing reverse SQL.

- [ ] **Step 5: Implement repositories and engine identity**

All repository queries bind every component of `WorkspaceScope` or `RemoteBindingKey`. `AttachWorkspace` loads the canonical `types.WorkspaceBinding` and profile in the same transaction, compares repository/ref/instance values, and inserts one complete row; it never inserts a recovery row. `UpdateProfile` permits base-URL and credential-reference rotation only when `expectedInstanceID` still matches. `sync.Engine` stores binding/profile IDs and the required exact conflict gate, calls `GetRoute` for each network cycle, resolves `profile.CredentialRef` immediately before the HTTP call, and excludes credential material from keys, equality, status, and errors. Implement `MarkDelivered` with the immediate recheck/complete-key update transaction above; `localstore.ErrWorkspaceConflicted` maps to `StateAttentionRequired` as a typed non-transient local block, never `ErrFabricUnavailable` or a network retry classification.

- [ ] **Step 6: Run GREEN, one-way loader, and isolation tests**

Run:

```bash
go test ./internal/runtime/localstore ./internal/runtime/sync ./cmd/gatewayd -run 'Test(GatewayMigration|FabricProfile|FabricBinding|Cursor|Queue|CredentialRotation|FabricFailure|GatewayWiresExactWorkspaceConflictGate)' -count=1
go test -race ./internal/runtime/localstore ./internal/runtime/sync -run 'Test(CompleteKeyQueueIsolation|CredentialRotationKeepsOneEngine)' -count=1
```

Expected: PASS; an injected migration-6 failure leaves version 5 byte-equivalent, an
injected migration-7 failure leaves version 6 and the legacy queue byte-equivalent, the
loader ignores/rejects noncanonical filenames, and no quarantine row is returned by
`ListPending`.

- [ ] **Step 7: Commit**

```bash
git add internal/runtime/localstore internal/runtime/sync cmd/gatewayd
git commit -m "feat: persist complete Fabric routes"
```

### Hard gate before Task 3: Fabric activity transport and finite retention

Task 3 is blocked until a focused approved amendment replaces its migration-21 SQL and
interfaces with a branch/stream/workspace-scoped `ActivityV1` transport/store/queue plus
effective-policy handshake. Keep Postgres migration number `000021`; do not renumber
later migrations. The amendment must make operational activity separate from
`OperationV1`, portable canonical trees, and accepted-stream authority. It must freeze:

- strict attributed ActivityV1 bytes/digest and complete tenant/ref/stream/workspace keys;
- restart-discarded presence; ordinary activity eligible when older than 30 days **or**
  outside the newest 10,000 unprotected workspace rows, pruned deterministically by
  `(created_at, activity_id)` ascending; lifecycle protection until terminal, followed
  by exactly 30 days by default or a configured longer finite duration, allowing
  protected rows to exceed the cap;
- an effective finite-policy response that Gateway validates before sending/accepting
  live activity, with no indefinite catch-all;
- queue/delivery/replay/terminal/prune transactions and proof that expiry cannot change a
  portable tree, Git acceptance, or promotion; and
- promotion remaining Gateway-local ProjectState authority until its resulting portable
  OperationV1 is synced as a proposal; Fabric activity never promotes itself.

Required RED/GREEN covers policy absence/malformed/unbounded rejection, branch/workspace
isolation, restart, cap/age/protected-row pruning, queue retry, replay, RLS/composite FKs,
and portable-tree byte identity across expiry. The detailed migration-21 portable-stream
SQL below is retained as design input for tree reconstruction only and is not executable
until the amendment integrates the activity schema and retention transactions.

### Task 3: Add durable Git-aware Fabric streams in migration 000021

**Files:**
- Create: `migrations/000021_git_aware_streams.up.sql`
- Create: `migrations/000021_git_aware_streams.down.sql`
- Create: `internal/core/git/private_schema_test.go`
- Modify: `docs/db-entities.md`

**Interfaces:**
- Consumes: existing migrations 1–20 unchanged.
- Produces: branch-isolated repository bindings, non-authoritative portable proposal/
  accepted-tree replicas, canonical portable operation requests, the gate-frozen separate
  ActivityV1 store/policy/queue, public-key activations, nonces, composite tenant FKs,
  forced RLS, and policy-governed evidence.

- [ ] **Step 1: Write failing real-Postgres schema tests**

Add these exact integration tests:

```go
func TestMigration21StoresEveryVersionTreeAndOperationBytes(t *testing.T)
func TestMigration21DirectSQLRejectsCrossProjectStreamFKs(t *testing.T)
func TestMigration21DirectSQLRejectsCrossStreamWorkspaceAndRequestFKs(t *testing.T)
func TestMigration21ForcesRLSForEveryProjectTable(t *testing.T)
func TestMigration21RejectsVersionAndRequestUpdateOutsidePolicyPruner(t *testing.T)
func TestMigration21DownLeavesVersion20Shape(t *testing.T)
```

Seed two projects, two Fabric UUIDs, two streams, and two workspace UUIDs. Attempt direct inserts pairing project A with project B's repository binding, stream, workspace binding, version, public key, and nonce. Each insert must fail with SQLSTATE `23503`. Run reads and writes as the ordinary non-superuser table owner and prove cross-project rows are invisible/rejected. Direct updates fail; deletion exists only through the gate-frozen policy-owned pruning transaction after eligibility.
`TestMigration21StoresEveryVersionTreeAndOperationBytes` also proves an operation
transition requires `operation_id`, canonical bytes, digest, and actor together, while
initial/accepted-ref transitions require all four to be null.

- [ ] **Step 2: Run RED from an explicit version-20 database**

Run:

```bash
migrate -path migrations -database "$WORMHOLE_DATABASE_URL" goto 20
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run 'TestMigration21' -count=1
```

Expected: migration version reports 20; tests FAIL because migration 21 is absent.

- [ ] **Step 3: Create the complete migration-21 up SQL**

`000021_git_aware_streams.up.sql` is exactly:

```sql
CREATE TABLE project_repository_bindings (
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  fabric_instance_id uuid NOT NULL,
  provider text NOT NULL CHECK(provider='github'),
  provider_repository_id text NOT NULL CHECK(provider_repository_id ~ '^[0-9]+$'),
  canonical_remote text NOT NULL,
  default_ref text NOT NULL CHECK(default_ref ~ '^refs/heads/[A-Za-z0-9._/-]+$'),
  visibility text NOT NULL CHECK(visibility IN ('public','private')),
  observer_credential_ref text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(project_id,fabric_instance_id),
  UNIQUE(fabric_instance_id,provider,provider_repository_id),
  CHECK((visibility='public' AND observer_credential_ref='') OR visibility='private')
);

CREATE TABLE fabric_streams (
  project_id uuid NOT NULL,
  fabric_instance_id uuid NOT NULL,
  stream_id uuid NOT NULL DEFAULT gen_random_uuid(),
  ref_name text NOT NULL CHECK(ref_name ~ '^refs/heads/[A-Za-z0-9._/-]+$'),
  current_version bigint NOT NULL DEFAULT 0 CHECK(current_version >= 0),
  live_tree_digest text NOT NULL CHECK(live_tree_digest ~ '^sha256:[0-9a-f]{64}$'),
  accepted_tree_digest text NOT NULL CHECK(accepted_tree_digest ~ '^sha256:[0-9a-f]{64}$'),
  accepted_commit_sha text NOT NULL CHECK(accepted_commit_sha ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(project_id,fabric_instance_id,stream_id),
  UNIQUE(project_id,fabric_instance_id,ref_name),
  FOREIGN KEY(project_id,fabric_instance_id)
    REFERENCES project_repository_bindings(project_id,fabric_instance_id) ON DELETE CASCADE
);

CREATE TABLE fabric_stream_versions (
  project_id uuid NOT NULL,
  fabric_instance_id uuid NOT NULL,
  stream_id uuid NOT NULL,
  version bigint NOT NULL CHECK(version >= 0),
  transition_kind text NOT NULL CHECK(transition_kind IN ('initial','operation','accepted_ref')),
  accepted_commit_sha text NOT NULL CHECK(accepted_commit_sha ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
  canonical_live_tree bytea NOT NULL,
  live_tree_digest text NOT NULL CHECK(live_tree_digest ~ '^sha256:[0-9a-f]{64}$'),
  canonical_accepted_tree bytea NOT NULL,
  accepted_tree_digest text NOT NULL CHECK(accepted_tree_digest ~ '^sha256:[0-9a-f]{64}$'),
  operation_id uuid,
  canonical_operation_json bytea,
  operation_digest text CHECK(operation_digest ~ '^sha256:[0-9a-f]{64}$'),
  actor_envelope_json bytea,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(project_id,fabric_instance_id,stream_id,version),
  FOREIGN KEY(project_id,fabric_instance_id,stream_id)
    REFERENCES fabric_streams(project_id,fabric_instance_id,stream_id) ON DELETE CASCADE,
  CHECK((transition_kind='operation') = (canonical_operation_json IS NOT NULL)),
  CHECK((canonical_operation_json IS NULL) = (operation_id IS NULL)),
  CHECK((canonical_operation_json IS NULL) = (operation_digest IS NULL)),
  CHECK((transition_kind='operation') = (actor_envelope_json IS NOT NULL))
);

CREATE TABLE fabric_workspace_stream_bindings (
  project_id uuid NOT NULL,
  fabric_instance_id uuid NOT NULL,
  stream_id uuid NOT NULL,
  workspace_id uuid NOT NULL,
  attachment_ref uuid NOT NULL,
  repository_provider text NOT NULL CHECK(repository_provider='github'),
  repository_immutable_id text NOT NULL CHECK(repository_immutable_id ~ '^[0-9]+$'),
  ref_name text NOT NULL CHECK(ref_name ~ '^refs/heads/[A-Za-z0-9._/-]+$'),
  writable boolean NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  detached_at timestamptz,
  PRIMARY KEY(project_id,fabric_instance_id,workspace_id),
  UNIQUE(project_id,fabric_instance_id,stream_id,workspace_id),
  UNIQUE(fabric_instance_id,attachment_ref),
  FOREIGN KEY(project_id,fabric_instance_id,stream_id)
    REFERENCES fabric_streams(project_id,fabric_instance_id,stream_id) ON DELETE CASCADE,
  FOREIGN KEY(project_id,fabric_instance_id,repository_provider,repository_immutable_id)
    REFERENCES project_repository_bindings(project_id,fabric_instance_id,provider,provider_repository_id)
    ON DELETE RESTRICT,
  CHECK((detached_at IS NULL) OR (NOT writable))
);

CREATE UNIQUE INDEX fabric_workspace_one_live_writable
  ON fabric_workspace_stream_bindings(project_id,fabric_instance_id,workspace_id)
  WHERE writable AND detached_at IS NULL;

CREATE TABLE fabric_stream_requests (
  project_id uuid NOT NULL,
  fabric_instance_id uuid NOT NULL,
  stream_id uuid NOT NULL,
  workspace_id uuid NOT NULL,
  operation_id uuid NOT NULL,
  canonical_operation_json bytea NOT NULL,
  operation_digest text NOT NULL CHECK(operation_digest ~ '^sha256:[0-9a-f]{64}$'),
  expected_stream_version bigint NOT NULL CHECK(expected_stream_version >= 0),
  expected_tree_digest text NOT NULL CHECK(expected_tree_digest ~ '^sha256:[0-9a-f]{64}$'),
  result text NOT NULL CHECK(result IN ('applied','conflict','rejected')),
  result_stream_version bigint NOT NULL CHECK(result_stream_version >= 0),
  actor_envelope_json bytea NOT NULL,
  conflict_json jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(project_id,fabric_instance_id,stream_id,operation_id),
  FOREIGN KEY(project_id,fabric_instance_id,stream_id,workspace_id)
    REFERENCES fabric_workspace_stream_bindings(project_id,fabric_instance_id,stream_id,workspace_id)
    ON DELETE RESTRICT,
  CHECK((result='conflict') = (conflict_json IS NOT NULL))
);

CREATE TABLE fabric_stream_conflicts (
  project_id uuid NOT NULL,
  fabric_instance_id uuid NOT NULL,
  stream_id uuid NOT NULL,
  conflict_id uuid NOT NULL DEFAULT gen_random_uuid(),
  detected_at_version bigint NOT NULL,
  conflict_kind text NOT NULL CHECK(conflict_kind IN ('operation_precondition','git_base_diverged')),
  base_tree_digest text NOT NULL CHECK(base_tree_digest ~ '^sha256:[0-9a-f]{64}$'),
  ours_tree_digest text NOT NULL CHECK(ours_tree_digest ~ '^sha256:[0-9a-f]{64}$'),
  theirs_tree_digest text NOT NULL CHECK(theirs_tree_digest ~ '^sha256:[0-9a-f]{64}$'),
  detail_json jsonb NOT NULL CHECK(jsonb_typeof(detail_json)='object'),
  state text NOT NULL CHECK(state IN ('open','resolved')),
  created_at timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz,
  PRIMARY KEY(project_id,fabric_instance_id,stream_id,conflict_id),
  FOREIGN KEY(project_id,fabric_instance_id,stream_id,detected_at_version)
    REFERENCES fabric_stream_versions(project_id,fabric_instance_id,stream_id,version) ON DELETE RESTRICT,
  CHECK((state='open' AND resolved_at IS NULL) OR (state='resolved' AND resolved_at IS NOT NULL))
);

CREATE TABLE fabric_public_actor_keys (
  project_id uuid NOT NULL,
  fabric_instance_id uuid NOT NULL,
  stream_id uuid NOT NULL,
  key_fingerprint text NOT NULL CHECK(key_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
  public_key bytea NOT NULL CHECK(octet_length(public_key)=32),
  actor_kind text NOT NULL CHECK(actor_kind IN ('human','agent')),
  human_principal_id uuid,
  agent_id uuid,
  accountable_human_id uuid,
  session_id uuid NOT NULL,
  harness_name text NOT NULL,
  harness_version text NOT NULL,
  model_name text NOT NULL DEFAULT '',
  model_version text NOT NULL DEFAULT '',
  source_version bigint NOT NULL,
  activated_at timestamptz NOT NULL DEFAULT now(),
  revoked_at timestamptz,
  PRIMARY KEY(project_id,fabric_instance_id,key_fingerprint),
  FOREIGN KEY(project_id,fabric_instance_id,stream_id,source_version)
    REFERENCES fabric_stream_versions(project_id,fabric_instance_id,stream_id,version) ON DELETE RESTRICT,
  CHECK((actor_kind='human' AND human_principal_id IS NOT NULL AND agent_id IS NULL AND accountable_human_id IS NULL)
     OR (actor_kind='agent' AND human_principal_id IS NULL AND agent_id IS NOT NULL AND accountable_human_id IS NOT NULL)),
  CHECK((model_name='') = (model_version=''))
);

CREATE TABLE public_request_nonces (
  project_id uuid NOT NULL,
  fabric_instance_id uuid NOT NULL,
  key_fingerprint text NOT NULL,
  nonce_hash text NOT NULL CHECK(nonce_hash ~ '^[0-9a-f]{64}$'),
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(project_id,fabric_instance_id,key_fingerprint,nonce_hash),
  FOREIGN KEY(project_id,fabric_instance_id,key_fingerprint)
    REFERENCES fabric_public_actor_keys(project_id,fabric_instance_id,key_fingerprint) ON DELETE CASCADE
);

CREATE FUNCTION reject_fabric_immutable_history() RETURNS trigger
LANGUAGE plpgsql AS $$ BEGIN
  RAISE EXCEPTION 'fabric history is immutable' USING ERRCODE='55000';
END $$;
CREATE TRIGGER fabric_stream_versions_immutable
  BEFORE UPDATE ON fabric_stream_versions
  FOR EACH ROW EXECUTE FUNCTION reject_fabric_immutable_history();
CREATE TRIGGER fabric_stream_requests_immutable
  BEFORE UPDATE ON fabric_stream_requests
  FOR EACH ROW EXECUTE FUNCTION reject_fabric_immutable_history();

DO $$
DECLARE n text;
BEGIN
  FOREACH n IN ARRAY ARRAY[
    'project_repository_bindings','fabric_streams','fabric_stream_versions',
    'fabric_workspace_stream_bindings','fabric_stream_requests','fabric_stream_conflicts',
    'fabric_public_actor_keys','public_request_nonces'
  ] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',n);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',n);
    EXECUTE format(
      'CREATE POLICY %I ON %I USING (project_id = NULLIF(current_setting(''wormhole.project_id'',true),'''')::uuid) WITH CHECK (project_id = NULLIF(current_setting(''wormhole.project_id'',true),'''')::uuid)',
      n || '_project_isolation',n);
  END LOOP;
END $$;
```

The two repository-binding uniqueness constraints needed by composite references are already the primary key and `(fabric_instance_id,provider,provider_repository_id)` unique key; add `UNIQUE(project_id,fabric_instance_id,provider,provider_repository_id)` explicitly immediately after table creation so the four-column foreign key is valid.

- [ ] **Step 4: Create the exact migration-21 down SQL**

```sql
DROP TRIGGER fabric_stream_requests_immutable ON fabric_stream_requests;
DROP TRIGGER fabric_stream_versions_immutable ON fabric_stream_versions;
DROP FUNCTION reject_fabric_immutable_history();
DROP TABLE public_request_nonces;
DROP TABLE fabric_public_actor_keys;
DROP TABLE fabric_stream_conflicts;
DROP TABLE fabric_stream_requests;
DROP TABLE fabric_workspace_stream_bindings;
DROP TABLE fabric_stream_versions;
DROP TABLE fabric_streams;
DROP TABLE project_repository_bindings;
```

- [ ] **Step 5: Apply all available migrations and run GREEN**

Run:

```bash
migrate -path migrations -database "$WORMHOLE_DATABASE_URL" goto 20
migrate -path migrations -database "$WORMHOLE_DATABASE_URL" up
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run 'TestMigration21' -count=1
migrate -path migrations -database "$WORMHOLE_DATABASE_URL" goto 20
```

Expected: upgrade reaches 21, tests PASS, downgrade reaches 20, and migrations 1–20 checksums and shapes remain unchanged.

- [ ] **Step 6: Commit**

```bash
git add migrations/000021_* internal/core/git/private_schema_test.go docs/db-entities.md
git commit -m "feat: add durable Git-aware streams"
```

### Task 4: Implement exact portable-stream reconstruction and shared-reducer transactions

**Files:**
- Create: `internal/core/git/stream_codec.go`
- Create: `internal/core/git/stream_codec_test.go`
- Create: `internal/core/git/streams.go`
- Create: `internal/core/git/streams_test.go`

**Interfaces:**
- Consumes: migration 21 and the exact `projectstate` API, including strict
  `DecodeOperation`, `CanonicalOperation`, `DigestCanonicalJSON`, and
  `DigestCanonicalMarkdown`.
- Produces: restart-safe non-authoritative portable `StreamStore` and transaction methods
  used by MCP portable-proposal coordination. ActivityV1 uses the Task-3-gated separate
  store/transport and never enters these methods.

```go
type StreamKey struct {
    ProjectID, FabricInstanceID, StreamID string
}
type RefObservation struct {
    Repository types.RepositoryIdentity
    RefName, CommitSHA string
    ObservedAt time.Time
}
type AttachStreamInput struct {
    Key StreamKey
    WorkspaceID string
    Repository types.RepositoryIdentity
    Ref RefObservation
    Tree projectstate.Tree
    Writable bool
}
type ApplyStreamOperationInput struct {
    Key StreamKey
    WorkspaceID string
    ExpectedVersion int64
    ExpectedTreeDigest projectstate.Digest
    Operation projectstate.OperationV1
}
type AdvanceAcceptedInput struct {
    Key StreamKey
    Ref RefObservation
    Tree projectstate.Tree
}
type StreamTransition struct {
    Key StreamKey
    Version int64
    Live, Accepted projectstate.Snapshot
    AcceptedCommitSHA string
    ConflictID string
}
type StreamStore struct { db *sql.DB }
func EncodeStoredTree(projectstate.Tree) ([]byte, error)
func DecodeStoredTree([]byte) (projectstate.Tree, error)
func (s *StreamStore) AttachInTx(context.Context, *sql.Tx, types.ActorScope, AttachStreamInput) (StreamTransition, error)
func (s *StreamStore) Read(context.Context, StreamKey, int64) (StreamTransition, error)
func (s *StreamStore) ApplyOperationInTx(context.Context, *sql.Tx, types.ActorScope, ApplyStreamOperationInput) (StreamTransition, error)
func (s *StreamStore) AdvanceAcceptedDefaultInTx(context.Context, *sql.Tx, types.ActorScope, AdvanceAcceptedInput) (StreamTransition, error)
```

- [ ] **Step 1: Write failing codec/restart/reducer tests**

Add `TestStoredTreeRoundTripPreservesCanonicalBytes`,
`TestAttachPersistsVersionZeroLiveAndAcceptedTrees`,
`TestApplyOperationPersistsCanonicalOperationAndResultTree`,
`TestReadReconstructsEveryVersionAfterRestart`,
`TestApplyOperationReplayReturnsOriginalResult`,
`TestApplyOperationChangedBodyReplayRejects`,
`TestApplyOperationStaleVersionPersistsConflict`,
`TestAdvanceAcceptedUsesExactObservedCommit`, and
`TestAdvanceAcceptedDivergencePreservesLiveAndPersistsConflict`. For every stored
version, close/reopen the DB, `DecodeStoredTree`, `DecodeTree`, `Validate`, and compare
both `DigestTree` values and every file byte.

Add `TestReadRejectsCorruptStoredOperation` and
`TestApplyOperationReplayRejectsCorruptStoredRequest` as corruption tables over
`fabric_stream_versions` and `fabric_stream_requests`: malformed operation JSON, unknown
field, trailing JSON, noncanonical bytes, decoded operation ID unequal to the row
`operation_id`, and stored digest unequal to `DigestCanonicalJSON(decoded)`. Each case
closes/reopens the database and proves `Read` and exact-operation replay reject before
returning a transition, serving a snapshot, applying a later operation, or taking an
idempotency result. Fixed goldens compare the stored bytes to
`CanonicalOperation(operation)` and the stored digest to
`DigestCanonicalJSON(operation)` rather than recomputing through store-private logic.

- [ ] **Step 2: Run RED**

Run: `WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run 'Test(StoredTree|Attach|ApplyOperation|Read(Reconstructs|Rejects)|AdvanceAccepted)' -count=1`

Expected: FAIL because codec and store are absent.

- [ ] **Step 3: Implement the deterministic tree container**

The container is `uint32 file-count`, then for each path-sorted file `uint32 path-length`, UTF-8 slash path bytes, `uint64 data-length`, and data bytes, all unsigned big-endian. Reject duplicate/out-of-order paths, zero/absolute/backslash paths, trailing bytes, more than 10,000 files, any file over 1 MiB, and aggregate data over 16 MiB. Encoding calls `projectstate.DecodeTree`, `Validate`, and `DigestTree` before writing; decoding calls them again before returning.

- [ ] **Step 4: Implement the transaction state machine**

`AttachInTx` locks the repository binding and rejects repository/ref/instance mismatches;
version 0 stores identical live and accepted trees. `ApplyOperationInTx` locks the stream,
checks workspace composite binding, structurally reconciles the operation actor with
`scope.Actor`, and uses the authoritative server actor before persistence. It calls
`projectstate.CanonicalOperation` for the exact bytes and
`projectstate.DigestCanonicalJSON` for their digest, requires the decoded operation ID to
equal each request/version row's `operation_id`, checks expected version/tree digest,
strictly loads the latest stored live tree, calls `projectstate.ApplyOperation`, encodes
the result with `projectstate.EncodeTree`, computes `DigestTree`, inserts the same
canonical operation bytes/digest in the immutable request and version rows, and updates
the stream. An
identical operation ID is idempotent only after strict decoding and complete byte/digest/
ID verification of both persisted rows; a changed canonical digest returns
`ErrOperationReplay`.

Every operation-bearing read path, including `Read`, restart reconstruction, duplicate-ID
lookup, request-result replay, and the prior-version load before another transition,
strict-decodes with `projectstate.DecodeOperation`. It rejects malformed, unknown-field,
trailing, or noncanonical JSON; decoded-ID/row-ID mismatch; canonical-byte mismatch; and
`DigestCanonicalJSON`/stored-digest mismatch before serving or replaying anything. There
is no trusted-row shortcut, permissive extraction, or store-private canonicalization.

`AdvanceAcceptedDefaultInTx` accepts only a `RefObservation` and tree returned by Task 5, verifies exact repository/ref/commit, persists a new version with the new accepted tree. If prior live equals prior accepted, new live equals new accepted. Otherwise prior live remains byte-identical and an open `git_base_diverged` conflict records old accepted, live, and new accepted digests. It never chooses by timestamp or discards either tree.

- [ ] **Step 5: Run GREEN and race tests**

Run:

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run 'Test(StoredTree|Attach|ApplyOperation|Read(Reconstructs|Rejects)|AdvanceAccepted)' -count=1
WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./internal/core/git -run 'Test(ApplyOperation|AdvanceAccepted)' -count=1
```

Expected: PASS; two concurrent expected-version writes produce exactly one applied transition and one durable conflict.

- [ ] **Step 6: Commit**

```bash
git add internal/core/git/stream_codec.go internal/core/git/stream_codec_test.go internal/core/git/streams.go internal/core/git/streams_test.go
git commit -m "feat: reconstruct versioned Fabric streams"
```

### Task 5: Observe one exact GitHub commit without credential leakage

**Files:**
- Create: `internal/core/git/observer.go`
- Create: `internal/core/git/fake_observer.go`
- Create: `internal/core/git/github_observer.go`
- Create: `internal/core/git/github_observer_test.go`
- Modify: `internal/types/config.go`
- Modify: `internal/types/config_test.go`
- Modify: `cmd/fabric/main.go`
- Modify: `cmd/fabric/main_test.go`

**Interfaces:**
- Consumes: `types.RepositoryIdentity`, `projectstate.Tree`, Task 4 `RefObservation`.
- Produces: provider-neutral observer, deterministic local fake, and GitHub-v1 observer.

```go
type CanonicalGitObserver interface {
    ObserveRef(context.Context, types.RepositoryIdentity, string, string) (RefObservation, projectstate.Tree, error)
}
type GitCredentialSource interface {
    ReadServerCredential(context.Context, string) (string, error)
}
type FakeObserver struct { /* mutex-protected repository/ref/commit/tree fixtures and call log */ }
func (f *FakeObserver) SetRef(types.RepositoryIdentity, string, string, projectstate.Tree)
func (f *FakeObserver) ObserveRef(context.Context, types.RepositoryIdentity, string, string) (RefObservation, projectstate.Tree, error)
```

The fourth `ObserveRef` argument is the Fabric-side `observer_credential_ref`; Gateway has no observer credential parameter.

- [ ] **Step 1: Write failing fake-server and consistency tests**

Add `TestGitHubObserverUsesProviderRepositoryID`, `TestGitHubObserverReadsTreeAtResolvedCommitNotMovingRef`, `TestGitHubObserverRejectsCommitMismatch`, `TestGitHubObserverFetchesOnlyWormholeBlobs`, `TestGitHubObserverPublicSendsNoAuthorization`, `TestGitHubObserverPrivateUsesServerCredentialOnly`, `TestGitHubObserverRejectsCredentialedRedirectBeforeFollow`, `TestGitHubObserverRejectsCrossOriginRedirect`, `TestGitHubObserverBoundsTree`, and `TestFakeObserverIsDeterministic`. In the moving-ref test, change the ref response after it returns commit A and make commit B contain different `.wormhole/` bytes; the result must be commit A and A's exact tree.

- [ ] **Step 2: Run RED**

Run: `go test ./internal/core/git ./cmd/fabric -run 'Test(GitHubObserver|FakeObserver)' -count=1`

Expected: FAIL because observer implementations are absent.

- [ ] **Step 3: Implement exact GitHub request order and redirect policy**

For repository numeric ID `N` and canonical ref `refs/heads/X`, issue only:

```text
GET /repositories/N
GET /repositories/N/git/ref/heads/<path-escaped X>
GET /repositories/N/git/commits/<resolved-commit-sha>
GET /repositories/N/git/trees/<commit-response-tree-sha>?recursive=1
GET /repositories/N/git/blobs/<blob-sha>  (only .wormhole/ entries)
```

Verify repository response ID equals `N`, ref response name/SHA match, commit response SHA equals the resolved SHA, and tree/blob SHAs agree. Build the `projectstate.Tree`, then call `DecodeTree`, `Validate`, and `DigestTree`. Never re-read the ref during tree fetch and never use a caller-supplied commit as observation.

Configure a dedicated `http.Client.CheckRedirect` that always returns `http.ErrUseLastResponse`; any 3xx is an observer error. This is mandatory for credentialed requests so `Authorization` cannot follow a redirect, and also applies to public requests for deterministic origin policy. Send `Authorization: Bearer ...` only when the repository binding is private, the credential reference resolves from Fabric config, and every request URL has the exact configured API scheme/host/port. Reject userinfo, query/fragment in the base URL, symlink/submodule entries, more than 10,000 entries, individual blobs over 1 MiB, aggregate bytes over 16 MiB, and paths outside `.wormhole/`. Do not call local Git or credential helpers.

- [ ] **Step 4: Run GREEN**

Run: `go test ./internal/core/git ./cmd/fabric -run 'Test(GitHubObserver|FakeObserver|PrivateGitCredential)' -count=1`

Expected: PASS; fake-server request logs contain no source blob and no redirected request.

- [ ] **Step 5: Commit**

```bash
git add internal/core/git internal/types/config.go internal/types/config_test.go cmd/fabric
git commit -m "feat: observe exact canonical GitHub commits"
```

### Task 6: Add exact public proofs and explicit sync protocol v2

**Files:**
- Create: `internal/mcp/auth.go`
- Create: `internal/mcp/public_auth.go`
- Create: `internal/mcp/public_auth_test.go`
- Create: `internal/mcp/sync_v2.go`
- Create: `internal/mcp/sync_v2_test.go`
- Create: `internal/runtime/sync/protocol_v2.go`
- Create: `internal/runtime/sync/protocol_v2_test.go`
- Modify: `internal/mcp/registry.go`
- Modify: `internal/mcp/registry_test.go`
- Modify: `internal/mcp/jsonrpc.go`
- Modify: `internal/mcp/fabric_registry.go`
- Modify: `internal/runtime/sync/sync.go`
- Modify: `docs/mcp-protocol.md`

**Interfaces:**
- Consumes: Tasks 1–5 plus Slice-A `localstore.WorkspaceConflictGate`, the shared
  `localstore.ErrWorkspaceConflicted`, and Task 2's conflict-aware
  `QueueRepo.MarkDelivered`.
- Produces: public key-continuity scopes, ID-free v2 attach/bootstrap/pull/push/conflict variants, and frozen private-credential v1 branch compatibility.

```go
type PublicRequestProof struct {
    KeyID string `json:"key_id"`
    PublicKey string `json:"public_key"`
    Timestamp string `json:"timestamp"`
    Nonce string `json:"nonce"`
    Signature string `json:"signature"`
}
type AuthRequest struct {
    ToolName, AttachmentRef, Authorization string
    PublicProof *PublicRequestProof
    Parameters any
}
type AuthResolver interface {
    Resolve(context.Context, AuthRequest) (types.ActorScope, error)
    RecordDenied(context.Context, types.ActorScope, string, json.RawMessage) error
}
type SyncV2Scope struct {
    Version int `json:"version"`
    AttachmentRef string `json:"attachment_ref"`
    Repository types.RepositoryIdentity `json:"repository"`
    CanonicalRef string `json:"canonical_ref"`
    BaseCommitSHA string `json:"base_commit_sha"`
    BaseTreeDigest projectstate.Digest `json:"base_tree_digest"`
    ExpectedStreamVersion int64 `json:"expected_stream_version"`
    ExpectedLiveTreeDigest projectstate.Digest `json:"expected_live_tree_digest"`
}
type SyncPushV2Args struct { SyncV2Scope; Operation projectstate.OperationV1 `json:"operation"` }
type SyncAttachV2Args struct {
    Version int `json:"version"` // const 2
    Repository types.RepositoryIdentity `json:"repository"`
    CanonicalRef string `json:"canonical_ref"`
    BaseCommitSHA string `json:"base_commit_sha"`
    BaseTreeDigest projectstate.Digest `json:"base_tree_digest"`
}
type SyncAttachV2Result struct {
    Version int `json:"version"` // const 2
    AttachmentRef string `json:"attachment_ref"`
    RemoteProjectID string `json:"remote_project_id"`
    StreamID string `json:"stream_id"`
    StreamVersion int64 `json:"stream_version"`
    EffectiveActivityPolicy EffectiveActivityPolicyV1 `json:"effective_activity_policy"`
}
```

`AttachmentRef` is a server-issued canonical UUID that is opaque, non-secret, and never
grants authority by possession. Fabric resolves it to the complete project/Fabric/remote-
project/stream binding before authorization and overwrites any private transport context;
no v2 public argument schema contains `project_id`, `workspace_id`, `fabric_instance_id`,
`remote_project_id`, `stream_id`, or an actor-routing field. Attach has no attachment
reference and uses the exact closed `SyncAttachV2Args` above. Only after the server
independently observes and validates that repository/ref may it allocate and return the
exact closed result. `EffectiveActivityPolicyV1` is the prior Task-3-gate-frozen finite
policy type; missing, malformed, unknown, or unbounded values reject attach before local
binding persistence. The result's remote IDs are response data retained only in Gateway's
private complete key and never echoed in later public arguments. Bootstrap/pull carry
`after_version`; conflict carries the
durable conflict ID and resolution operation. A portable operation's embedded attribution
is content, not routing authority, and must exact-match the freshly resolved actor scope.
All v2 structs are closed (`additionalProperties:false`) and require `version:2`.

Task 3's focused amendment adds a separate ActivityV1 protocol branch. Attach/bootstrap
must return its exact effective finite retention policy; Gateway validates that policy
before it queues, sends, accepts, or exposes live activity. Missing, malformed, unknown,
or unbounded policy disables remote activity without blocking portable/local work. The
portable v2 OperationV1/tree branch does not carry generic channel/task activity.

- [ ] **Step 1: Freeze v1 branches and add failing v2/proof tests**

For each of `wormhole.sync.bootstrap`, `incremental_pull`, `incremental_push`, and `conflict_report`, preserve fixtures for strict v1 request decode, result JSON bytes, documented error strings, and the exact v1 descriptor branch on the credential-authenticated compatibility registry. Add `TestSyncDescriptorV1BranchUnchangedWhenV2Added`; compare the normalized `oneOf[version=1]` branch to the frozen legacy schema, not the complete descriptor. Public/identification-only `tools/list` exposes only ID-free v2 branches; add `TestPublicSyncV2DescriptorsRejectPrivateScopeFields` for every forbidden field above. Add public tests for padded base64, non-URL alphabet, nonce lengths 31/33, wrong key ID, stale `now-5m-1ns`, future `now+30s+1ns`, replay, body tamper, unknown/mismatched attachment or repository, wrong Fabric/tool, and noncanonical timestamp.

Add `TestSyncV2PushOpenConflictStopsBeforeCredentialOrNetwork`,
`TestSyncV2PushConflictOpenedInFlightLeavesQueueRowByteIdenticalPending`,
`TestSyncV2PushConflictScopeIsolation`,
`TestSyncV2PushGateFailureHasNoSideEffects`, and
`TestSyncV2PushRetriesExactOperationAfterResolution`. Instrument workspace/profile reads,
credential source, signer, DNS/client construction, HTTP transport, cursor, audit, and
queue writes. An exact-workspace open conflict must return an error satisfying
`errors.Is(err, localstore.ErrWorkspaceConflicted)`, classify as non-transient
`StateAttentionRequired`, and leave every credential/network/remote/cursor/audit/delivery
counter at zero. Same-project/different-workspace, different-project, and resolved-only
fixtures must not block. The in-flight case opens a conflict after the server accepts the
operation but before local delivery marking, then compares the complete queue row before/
after reopen. After explicit resolution, retry must send the identical canonical
operation ID/bytes and then deliver that same row once.

Add `TestSyncV2EverySignedPreconditionIsServerChecked`: independently mutate attachment,
repository provider/immutable ID/remote, canonical ref, base commit, accepted-tree digest,
expected stream version, and expected live-tree digest while re-signing otherwise valid
parameters. Each case must fail before reducer dispatch, stream mutation, audit, cursor,
or delivery and leave the stored version/tree bytes unchanged.

- [ ] **Step 2: Run RED**

Run: `go test ./internal/mcp ./internal/runtime/sync -run 'Test(SyncV1|SyncDescriptorV1|SyncV2|PublicProof)' -count=1`

Expected: v1 fixture tests PASS; new v2/proof tests FAIL.

- [ ] **Step 3: Implement the exact Ed25519 proof**

Decode public key, nonce, and signature with `base64.RawURLEncoding.Strict()` and reject `=` padding. Require 32-byte key, 32-byte nonce, and 64-byte signature. `KeyID` must equal `sha256:` plus lowercase SHA-256 hex of the raw public key. Parse `Timestamp` with `time.RFC3339Nano`, require it equals `parsed.UTC().Format(time.RFC3339Nano)`, and accept inclusive `[now-5m, now+30s]` only.

Canonicalize `Parameters` with `projectstate.CanonicalJSON`; hash those bytes with
SHA-256. Fabric derives `scope-key` as `attachment:<attachment_ref>` for bound calls and
as `repository:<sha256 canonical repository identity and ref digest>` for initial attach;
the caller cannot supply a different discriminator. Verify the signature over exactly:

```text
wormhole-public-v1\n<server-configured Fabric instance UUID>\n<tool name>\n<scope-key>\n<lowercase parameter SHA-256 hex>\n<canonical timestamp>\n<RawURLEncoding nonce>
```

After resolving the attachment—or independently observing the initial attach repository—
require the key ID/public key to match one active `fabric_public_actor_keys` row in that
server-derived project. The initial attach activation may derive a public actor only after
Task 5 observes the exact canonical tree, the signing key belongs to that tracked actor,
and an agent's claimed accountable-human ID names a tracked human actor in the same tree;
store that self-declared linkage and force `AssurancePublicKeyContinuity`. Later calls use
`ResolvePublicIssuedScope` to construct a new envelope/scope from that row and the resolved
attachment, call `Validate`, and reject private/local/legacy/unknown assurance without
mutating caller scope or labeling the principal verified. Insert the nonce hash in the
same server-derived project transaction before dispatch; unique violation is replay.

- [ ] **Step 4: Add explicit version dispatch**

Add `ArgumentVariants` and `ResultVariants` to `Tool`. The credential-authenticated
compatibility registry emits `oneOf` branches discriminated by required integer `version`
const 1 or 2; public/identification-only registration emits only the ID-free v2 branch.
Dispatch decodes only `version`, then strict-decodes the selected closed struct. Preserve
existing v1 structs, results, handlers, and error text without allowing v1 on public-key
continuity connections. `wormhole.sync.attach` is v2-only.

- [ ] **Step 5: Implement Gateway v2 transport**

New complete bindings use v2. Gateway loads `types.WorkspaceBinding` and the local
`FabricBinding` immediately before each call, copies only the server-issued attachment
reference plus repository/ref/commit/digest into public params, loads the current stream
cursor and live-tree digest, and signs canonical params. Fabric resolves that attachment
back to its complete binding and, in one StreamStore transaction before dispatch,
exact-matches repository/ref, current version, accepted commit, accepted-tree digest, and
live-tree digest against the addressed stored version. A mismatch is a semantic
precondition failure with zero reducer/audit/version mutation. Pull calls the server
observer path and imports only a validated accepted tree. No recovery/quarantine row can
construct a v2 request. V1 remains available only to a valid newly resolved private
credential during the explicit compatibility window; it cannot attach a new stream,
serve public-key continuity, or bypass v2 preconditions.

ActivityV1 send/pull uses only the gate-frozen branch/stream/workspace queue and policy;
it never calls `ApplyOperation`, reconstructs a canonical tree, or advances accepted state.

Every v2 push uses this exact order for the pending row's complete `RemoteBindingKey`:

```text
scope = types.WorkspaceScope{ProjectID: key.ProjectID, WorkspaceID: key.WorkspaceID}
open = conflictGate.HasOpenConflicts(ctx, scope)
if gate error: fail closed; leave row byte-identical pending
if open: return localstore.ErrWorkspaceConflicted; leave row byte-identical pending
load current WorkspaceBinding/route/cursor
resolve profile credential; construct/sign request; perform network call
if stale/precondition result: persist its conflict; leave row pending
if success: QueueRepo.MarkDelivered(ctx, key, operation.ID)
  // MarkDelivered atomically rechecks the same exact workspace for open conflicts
  // immediately before its complete-key delivery UPDATE.
```

The first gate precedes credential resolution, DNS, client/transport construction,
signing, and every network call. A true result is a typed non-transient local block:
wrap `localstore.ErrWorkspaceConflicted`, set `StateAttentionRequired`, retain the exact
pending row, and do not consume retry budget as Fabric unavailability. Storage gate
errors also fail closed before side effects. Task 2's `MarkDelivered` transaction is the
mandatory post-network recheck; an in-flight conflict makes it return the same sentinel
without changing the row, cursor, or audit state.

No SQLite transaction or workspace lock is held across the network. Therefore a remote
server may accept an operation just before a local conflict opens. In that case the
atomic delivery recheck intentionally leaves the canonical row pending. After explicit
resolution, retry sends the same operation ID and bytes; server-side OperationV1 exact
replay/preconditions make that retry idempotent. If `MarkDelivered` commits first and a
conflict opens later, the later conflict belongs to subsequent local state and does not
retroactively undeliver the completed operation. This is the complete concurrency
boundary; no implementation may omit either check or claim a distributed transaction.

- [ ] **Step 6: Run GREEN**

Run:

```bash
go test ./internal/mcp ./internal/runtime/sync -run 'Test(SyncV1|SyncDescriptorV1|SyncV2|PublicProof|Nonce|Precondition)' -count=1
go test -race ./internal/mcp -run 'TestPublicProofNonceReplay' -count=1
```

Expected: PASS; exactly one concurrent nonce use succeeds; public descriptors expose only
ID-free v2; private-credential v1 request/result/error fixtures and descriptor branches
are unchanged.

- [ ] **Step 7: Commit**

```bash
git add internal/mcp internal/runtime/sync docs/mcp-protocol.md
git commit -m "feat: add identified sync protocol v2"
```

### Task 7: Enforce zero-contact forks and atomic attach/detach

**Files:**
- Create: `internal/runtime/sync/attach.go`
- Create: `internal/runtime/sync/attach_test.go`
- Modify: `internal/runtime/localapi/bootstrap.go`
- Modify: `internal/runtime/localapi/bootstrap_test.go`
- Create: `cmd/wormhole/fabric.go`
- Create: `cmd/wormhole/fabric_test.go`
- Modify: `cmd/gatewayd/gatewayd.go`

**Interfaces:**
- Consumes: canonical `types.WorkspaceBinding`, Task 2 repositories, Task 6 v2 attach.
- Produces: `EvaluateAttach`, `AttachWorkspace`, `DetachWorkspace`; `wormhole fabric profile {add,list,update}`, `fabric {attach,detach,list}`.

```go
type AttachDecision string
const (
    AttachLocalOnly AttachDecision = "local_only"
    AttachCanonical AttachDecision = "canonical"
    AttachInactiveFork AttachDecision = "inactive_fork"
)
func EvaluateAttach(types.WorkspaceBinding, projectstate.FabricHintV1, types.RepositoryIdentity) (AttachDecision, error)
```

- [ ] **Step 1: Write failing pre-transport tests**

Add `TestCopiedHintForkPerformsZeroNetworkWork`, `TestMatchingURLWithoutImmutableIDPerformsZeroNetworkWork`, `TestUpstreamRemoteDoesNotAuthorizeOrigin`, `TestCanonicalOriginMayAttach`, `TestIndependentForkRealmRequiresDifferentFabricInstance`, `TestDetachPreservesQueueAndConflicts`, and `TestRebindRequiresEmptyQueueNoOpenConflictAndConfirmation`. The zero-contact fixture injects DNS resolver, dialer, HTTP transport, credential source, and observer counters; every counter must remain zero for a mismatch.

- [ ] **Step 2: Run RED**

Run: `go test ./internal/runtime/sync ./internal/runtime/localapi ./cmd/wormhole -run 'Test(CopiedHint|MatchingURL|UpstreamRemote|CanonicalOrigin|IndependentFork|Detach|Rebind)' -count=1`

Expected: FAIL because attach validation does not precede transport construction.

- [ ] **Step 3: Implement the decision and two-phase attach**

`EvaluateAttach` is pure and compares the registered workspace repository with the independently observed local `origin` identity supplied by Slice C; shared history, URL equality, or an `upstream` remote never substitute for immutable ID equality. On mismatch, return `inactive_fork` before loading a profile credential, resolving DNS, creating a client, or observing Git.

For a canonical decision, phase one contacts the chosen profile and verifies its immutable
Fabric instance. Phase two calls v2 attach and receives the opaque attachment reference
plus complete remote project/stream identifiers as result data. Only then does one SQLite
transaction insert `workspace_fabric_bindings` and its cursor. Later public requests send
only the attachment reference; the other identifiers remain private complete local keys.
Network failure leaves no binding. Detach sets the complete binding non-writable/detached
and preserves queues/conflicts. Rebind is a human-only command requiring the typed
confirmation string `<workspace-id>:<old-fabric-id>:<old-stream-id>`, zero pending rows,
and zero open conflicts; it deletes the detached binding and creates the new complete
binding in one transaction.

- [ ] **Step 4: Run GREEN**

Run: `go test ./internal/runtime/sync ./internal/runtime/localapi ./cmd/wormhole ./cmd/gatewayd -run 'Test(CopiedHint|MatchingURL|UpstreamRemote|CanonicalOrigin|IndependentFork|Detach|Rebind|Attach)' -count=1`

Expected: PASS; all mismatch counters remain zero and no partial binding survives any injected phase failure.

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/sync internal/runtime/localapi cmd/wormhole cmd/gatewayd
git commit -m "feat: reject upstream Fabric access from forks"
```

### Task 8: Add private identity, OIDC state, and composite tenant schema in migration 000022

**Files:**
- Create: `migrations/000022_private_identity.up.sql`
- Create: `migrations/000022_private_identity.down.sql`
- Create: `internal/core/identity/private_schema_test.go`
- Modify: `docs/db-entities.md`

**Interfaces:**
- Consumes: existing global `agents`, project-scoped `passports`, `agent_tokens`, and `audit_log`.
- Produces: human/authenticator/session protocol state plus project membership, invitation, first-owner, agent ownership/session, Passport, credential, PAT, and immutable typed-audit schema.

- [ ] **Step 1: Write failing direct-SQL/RLS/history tests**

Add `TestMigration22GlobalTablesAreNarrowAuthenticationState`, `TestMigration22DirectSQLRejectsMembershipTenantMismatch`, `TestMigration22DirectSQLRejectsOwnershipMembershipMismatch`, `TestMigration22DirectSQLRejectsSessionOwnershipMismatch`, `TestMigration22DirectSQLRejectsPassportOwnershipMismatch`, `TestMigration22DirectSQLRejectsAgentCredentialCompositeMismatch`, `TestMigration22DirectSQLRejectsPATMembershipMismatch`, `TestMigration22AuditRejectsHumanMembershipMismatchByDirectSQL`, `TestMigration22AuditRejectsAgentMembershipMismatchByDirectSQL`, `TestMigration22AuditRejectsSessionActorMismatchByDirectSQL`, `TestMigration22AuditRejectsCredentialSessionMismatchByDirectSQL`, `TestMigration22AuditRejectsHumanAgentShapeMismatchByDirectSQL`, `TestMigration22ProjectListRLSShowsOnlyActingHumanMemberships`, `TestMigration22AuditIsImmutable`, and `TestMigration22LegacyAuditRemainsUnknownWithoutInventedHuman`. Expect SQLSTATE `23503` for composite-FK attacks, `23514` for actor/assurance CHECK attacks, `42501` for RLS writes, and `55000` for audit mutation.

- [ ] **Step 2: Run RED from version 21**

Run:

```bash
migrate -path migrations -database "$WORMHOLE_DATABASE_URL" goto 21
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/identity -run 'TestMigration22' -count=1
```

Expected: FAIL because migration 22 is absent.

- [ ] **Step 3: Create global authentication state in the up migration**

The first part of `000022_private_identity.up.sql` is:

```sql
CREATE TABLE human_principals (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  display_name text NOT NULL,
  state text NOT NULL CHECK(state IN ('active','disabled')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE human_authenticators (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  human_id uuid NOT NULL REFERENCES human_principals(id) ON DELETE RESTRICT,
  kind text NOT NULL CHECK(kind='oidc'),
  issuer text NOT NULL,
  subject text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz,
  revoked_at timestamptz,
  UNIQUE(issuer,subject)
);
CREATE TABLE human_recovery_codes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  human_id uuid NOT NULL REFERENCES human_principals(id) ON DELETE CASCADE,
  code_hash text NOT NULL UNIQUE CHECK(code_hash ~ '^[0-9a-f]{64}$'),
  created_at timestamptz NOT NULL DEFAULT now(),
  used_at timestamptz,
  revoked_at timestamptz
);
CREATE TABLE oidc_login_challenges (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  challenge_secret_hash text NOT NULL UNIQUE CHECK(challenge_secret_hash ~ '^[0-9a-f]{64}$'),
  provider_alias text NOT NULL,
  issuer text NOT NULL,
  client_id text NOT NULL,
  authorization_endpoint text NOT NULL,
  token_endpoint text NOT NULL,
  jwks_url text NOT NULL,
  redirect_uri text NOT NULL,
  pkce_s256_challenge text NOT NULL,
  state_hash text NOT NULL UNIQUE CHECK(state_hash ~ '^[0-9a-f]{64}$'),
  nonce_hash text NOT NULL UNIQUE CHECK(nonce_hash ~ '^[0-9a-f]{64}$'),
  expires_at timestamptz NOT NULL,
  used_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE oidc_id_token_replays (
  issuer text NOT NULL,
  token_hash text NOT NULL CHECK(token_hash ~ '^[0-9a-f]{64}$'),
  subject text NOT NULL,
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(issuer,token_hash)
);
CREATE TABLE human_session_families (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  human_id uuid NOT NULL REFERENCES human_principals(id) ON DELETE RESTRICT,
  authenticator_id uuid REFERENCES human_authenticators(id) ON DELETE RESTRICT,
  assurance text NOT NULL CHECK(assurance='private-authenticated'),
  created_at timestamptz NOT NULL DEFAULT now(),
  absolute_expires_at timestamptz NOT NULL,
  revoked_at timestamptz,
  revocation_reason text
);
CREATE TABLE human_access_tokens (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  family_id uuid NOT NULL REFERENCES human_session_families(id) ON DELETE CASCADE,
  token_hash text NOT NULL UNIQUE CHECK(token_hash ~ '^[0-9a-f]{64}$'),
  expires_at timestamptz NOT NULL,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE human_refresh_tokens (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  family_id uuid NOT NULL REFERENCES human_session_families(id) ON DELETE CASCADE,
  sequence bigint NOT NULL CHECK(sequence >= 0),
  token_hash text NOT NULL UNIQUE CHECK(token_hash ~ '^[0-9a-f]{64}$'),
  expires_at timestamptz NOT NULL,
  used_at timestamptz,
  replaced_by_id uuid REFERENCES human_refresh_tokens(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(family_id,sequence)
);
CREATE TABLE oidc_device_authorizations (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  provider_alias text NOT NULL,
  issuer text NOT NULL,
  client_id text NOT NULL,
  token_endpoint text NOT NULL,
  device_authorization_endpoint text NOT NULL,
  jwks_url text NOT NULL,
  provider_device_code_ciphertext bytea NOT NULL,
  user_code_hash text NOT NULL CHECK(user_code_hash ~ '^[0-9a-f]{64}$'),
  device_nonce_hash text NOT NULL UNIQUE CHECK(device_nonce_hash ~ '^[0-9a-f]{64}$'),
  verification_uri text NOT NULL,
  poll_interval_seconds integer NOT NULL CHECK(poll_interval_seconds BETWEEN 1 AND 60),
  next_poll_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  state text NOT NULL CHECK(state IN ('pending','complete','expired','denied')),
  completed_human_id uuid REFERENCES human_principals(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz
);
CREATE TABLE human_recovery_attempts (
  subject_hash text NOT NULL CHECK(subject_hash ~ '^[0-9a-f]{64}$'),
  source_hash text NOT NULL CHECK(source_hash ~ '^[0-9a-f]{64}$'),
  window_started_at timestamptz NOT NULL,
  failure_count integer NOT NULL CHECK(failure_count BETWEEN 0 AND 20),
  blocked_until timestamptz,
  PRIMARY KEY(subject_hash,source_hash,window_started_at)
);
```

`provider_device_code_ciphertext` is encrypted with the configured Fabric secret-encryption key; its key reference is server configuration, not a table column or Gateway value.

- [ ] **Step 4: Create project authorization and composite agent relations**

Append exactly:

```sql
CREATE TABLE project_memberships (
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  human_id uuid NOT NULL REFERENCES human_principals(id) ON DELETE RESTRICT,
  roles jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(roles)='array'),
  permissions jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(permissions)='array'),
  created_at timestamptz NOT NULL DEFAULT now(),
  revoked_at timestamptz,
  revoked_by_human_id uuid REFERENCES human_principals(id) ON DELETE RESTRICT,
  PRIMARY KEY(project_id,id),
  UNIQUE(id,project_id,human_id),
  UNIQUE(project_id,id,human_id)
);
CREATE UNIQUE INDEX project_memberships_one_active
  ON project_memberships(project_id,human_id) WHERE revoked_at IS NULL;

CREATE TABLE project_agent_principals (
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  agent_id uuid NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(project_id,agent_id)
);
INSERT INTO project_agent_principals(project_id,agent_id)
SELECT DISTINCT project_id,agent_id FROM passports ON CONFLICT DO NOTHING;

CREATE TABLE project_owner_bootstrap_grants (
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  issuer text NOT NULL,
  subject text NOT NULL,
  roles jsonb NOT NULL CHECK(jsonb_typeof(roles)='array'),
  permissions jsonb NOT NULL CHECK(jsonb_typeof(permissions)='array'),
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  consumed_by_human_id uuid REFERENCES human_principals(id) ON DELETE RESTRICT,
  consumed_membership_id uuid,
  consumed_at timestamptz,
  PRIMARY KEY(project_id,id),
  UNIQUE(project_id,issuer,subject),
  CHECK((consumed_at IS NULL AND consumed_by_human_id IS NULL AND consumed_membership_id IS NULL)
     OR (consumed_at IS NOT NULL AND consumed_by_human_id IS NOT NULL AND consumed_membership_id IS NOT NULL)),
  FOREIGN KEY(consumed_membership_id,project_id,consumed_by_human_id)
    REFERENCES project_memberships(id,project_id,human_id) ON DELETE RESTRICT
);

CREATE TABLE membership_invitations (
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  invitation_hash text NOT NULL UNIQUE CHECK(invitation_hash ~ '^[0-9a-f]{64}$'),
  invited_issuer text,
  invited_subject text,
  roles jsonb NOT NULL CHECK(jsonb_typeof(roles)='array'),
  permissions jsonb NOT NULL CHECK(jsonb_typeof(permissions)='array'),
  invited_by_membership_id uuid NOT NULL,
  invited_by_human_id uuid NOT NULL REFERENCES human_principals(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  accepted_membership_id uuid,
  accepted_human_id uuid REFERENCES human_principals(id) ON DELETE RESTRICT,
  accepted_at timestamptz,
  revoked_at timestamptz,
  PRIMARY KEY(project_id,id),
  FOREIGN KEY(invited_by_membership_id,project_id,invited_by_human_id)
    REFERENCES project_memberships(id,project_id,human_id) ON DELETE RESTRICT,
  FOREIGN KEY(accepted_membership_id,project_id,accepted_human_id)
    REFERENCES project_memberships(id,project_id,human_id) ON DELETE RESTRICT,
  CHECK((invited_issuer IS NULL)=(invited_subject IS NULL)),
  CHECK((accepted_at IS NULL AND accepted_membership_id IS NULL AND accepted_human_id IS NULL)
     OR (accepted_at IS NOT NULL AND accepted_membership_id IS NOT NULL AND accepted_human_id IS NOT NULL))
);

CREATE TABLE agent_ownerships (
  project_id uuid NOT NULL,
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  agent_id uuid NOT NULL,
  accountable_human_id uuid NOT NULL REFERENCES human_principals(id) ON DELETE RESTRICT,
  membership_id uuid NOT NULL,
  supersedes_id uuid,
  effective_at timestamptz NOT NULL DEFAULT now(),
  ended_at timestamptz,
  end_reason text,
  PRIMARY KEY(project_id,id),
  UNIQUE(id,project_id,agent_id,accountable_human_id,membership_id),
  UNIQUE(id,project_id,agent_id),
  FOREIGN KEY(project_id,agent_id)
    REFERENCES project_agent_principals(project_id,agent_id) ON DELETE RESTRICT,
  FOREIGN KEY(membership_id,project_id,accountable_human_id)
    REFERENCES project_memberships(id,project_id,human_id) ON DELETE RESTRICT,
  FOREIGN KEY(supersedes_id,project_id,agent_id)
    REFERENCES agent_ownerships(id,project_id,agent_id) ON DELETE RESTRICT,
  CHECK((ended_at IS NULL AND end_reason IS NULL) OR (ended_at IS NOT NULL AND end_reason IS NOT NULL))
);
CREATE UNIQUE INDEX agent_ownerships_one_active
  ON agent_ownerships(project_id,agent_id) WHERE ended_at IS NULL;

CREATE TABLE agent_sessions (
  project_id uuid NOT NULL,
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  agent_id uuid NOT NULL,
  accountable_human_id uuid NOT NULL REFERENCES human_principals(id) ON DELETE RESTRICT,
  membership_id uuid NOT NULL,
  ownership_id uuid NOT NULL,
  harness_name text NOT NULL,
  harness_version text NOT NULL,
  model_name text NOT NULL DEFAULT '',
  model_version text NOT NULL DEFAULT '',
  started_at timestamptz NOT NULL DEFAULT now(),
  ended_at timestamptz,
  PRIMARY KEY(project_id,id),
  UNIQUE(project_id,id,agent_id,accountable_human_id),
  UNIQUE(id,project_id,agent_id,accountable_human_id,membership_id,ownership_id),
  FOREIGN KEY(ownership_id,project_id,agent_id,accountable_human_id,membership_id)
    REFERENCES agent_ownerships(id,project_id,agent_id,accountable_human_id,membership_id) ON DELETE RESTRICT,
  CHECK((model_name='')=(model_version=''))
);
```

- [ ] **Step 5: Alter Passports and create credentials with complete composite keys**

Append exactly:

```sql
ALTER TABLE passports DROP CONSTRAINT passports_agent_id_project_id_key;
ALTER TABLE passports
  ADD COLUMN accountable_human_id uuid REFERENCES human_principals(id) ON DELETE RESTRICT,
  ADD COLUMN membership_id uuid,
  ADD COLUMN ownership_id uuid,
  ADD COLUMN expires_at timestamptz,
  ADD COLUMN revoked_at timestamptz,
  ADD COLUMN supersedes_id uuid,
  ADD CONSTRAINT passports_project_agent_fkey
    FOREIGN KEY(project_id,agent_id) REFERENCES project_agent_principals(project_id,agent_id) ON DELETE RESTRICT,
  ADD CONSTRAINT passports_membership_fkey
    FOREIGN KEY(membership_id,project_id,accountable_human_id)
      REFERENCES project_memberships(id,project_id,human_id) ON DELETE RESTRICT,
  ADD CONSTRAINT passports_ownership_fkey
    FOREIGN KEY(ownership_id,project_id,agent_id,accountable_human_id,membership_id)
      REFERENCES agent_ownerships(id,project_id,agent_id,accountable_human_id,membership_id) ON DELETE RESTRICT,
  ADD CONSTRAINT passports_supersedes_fkey
    FOREIGN KEY(supersedes_id,project_id,agent_id)
      REFERENCES passports(id,project_id,agent_id) ON DELETE RESTRICT,
  ADD CONSTRAINT passports_private_binding_complete CHECK(
    (accountable_human_id IS NULL AND membership_id IS NULL AND ownership_id IS NULL)
    OR (accountable_human_id IS NOT NULL AND membership_id IS NOT NULL AND ownership_id IS NOT NULL)
  ),
  ADD CONSTRAINT passports_id_project_agent_key UNIQUE(id,project_id,agent_id),
  ADD CONSTRAINT passports_audit_composite_key UNIQUE(project_id,id,agent_id,accountable_human_id),
  ADD CONSTRAINT passports_private_composite_key
    UNIQUE(id,project_id,agent_id,accountable_human_id,membership_id,ownership_id);
CREATE UNIQUE INDEX passports_one_active_private
  ON passports(project_id,agent_id)
  WHERE membership_id IS NOT NULL AND revoked_at IS NULL;

CREATE TABLE agent_credentials (
  project_id uuid NOT NULL,
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  agent_id uuid NOT NULL,
  accountable_human_id uuid NOT NULL,
  membership_id uuid NOT NULL,
  ownership_id uuid NOT NULL,
  passport_id uuid NOT NULL,
  session_id uuid NOT NULL,
  token_hash text NOT NULL UNIQUE CHECK(token_hash ~ '^[0-9a-f]{64}$'),
  permissions jsonb NOT NULL CHECK(jsonb_typeof(permissions)='array'),
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  revoked_at timestamptz,
  revocation_reason text,
  PRIMARY KEY(project_id,id),
  UNIQUE(project_id,id,agent_id,accountable_human_id,session_id),
  FOREIGN KEY(passport_id,project_id,agent_id,accountable_human_id,membership_id,ownership_id)
    REFERENCES passports(id,project_id,agent_id,accountable_human_id,membership_id,ownership_id) ON DELETE RESTRICT,
  FOREIGN KEY(session_id,project_id,agent_id,accountable_human_id,membership_id,ownership_id)
    REFERENCES agent_sessions(id,project_id,agent_id,accountable_human_id,membership_id,ownership_id) ON DELETE RESTRICT,
  CHECK((revoked_at IS NULL AND revocation_reason IS NULL) OR
        (revoked_at IS NOT NULL AND revocation_reason IS NOT NULL))
);

CREATE TABLE personal_access_tokens (
  project_id uuid NOT NULL,
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  human_id uuid NOT NULL,
  membership_id uuid NOT NULL,
  label text NOT NULL,
  token_hash text NOT NULL UNIQUE CHECK(token_hash ~ '^[0-9a-f]{64}$'),
  scopes jsonb NOT NULL CHECK(jsonb_typeof(scopes)='array'),
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  revoked_at timestamptz,
  PRIMARY KEY(project_id,id),
  FOREIGN KEY(membership_id,project_id,human_id)
    REFERENCES project_memberships(id,project_id,human_id) ON DELETE RESTRICT
);
```

- [ ] **Step 6: Extend typed audit without rewriting history**

Append exactly:

```sql
ALTER TABLE audit_log DROP CONSTRAINT audit_log_agent_id_fkey;
ALTER TABLE audit_log ALTER COLUMN agent_id DROP NOT NULL;
ALTER TABLE audit_log
  ADD CONSTRAINT audit_log_agent_id_fkey FOREIGN KEY(agent_id) REFERENCES agents(id) ON DELETE RESTRICT,
  ADD COLUMN actor_kind text,
  ADD COLUMN human_principal_id uuid REFERENCES human_principals(id) ON DELETE RESTRICT,
  ADD COLUMN accountable_human_id uuid REFERENCES human_principals(id) ON DELETE RESTRICT,
  ADD COLUMN session_id uuid,
  ADD COLUMN membership_id uuid,
  ADD COLUMN passport_id uuid,
  ADD COLUMN credential_id uuid,
  ADD COLUMN harness_name text NOT NULL DEFAULT '',
  ADD COLUMN harness_version text NOT NULL DEFAULT '',
  ADD COLUMN model_name text NOT NULL DEFAULT '',
  ADD COLUMN model_version text NOT NULL DEFAULT '',
  ADD COLUMN assurance text,
  ADD COLUMN occurred_at timestamptz,
  ADD COLUMN actor_envelope_json bytea,
  ADD COLUMN payload jsonb NOT NULL DEFAULT '{}';
UPDATE audit_log SET actor_kind='agent', assurance='unknown', occurred_at=created_at;
ALTER TABLE audit_log
  ALTER COLUMN actor_kind SET NOT NULL,
  ALTER COLUMN assurance SET NOT NULL,
  ALTER COLUMN occurred_at SET NOT NULL,
  ADD CONSTRAINT audit_actor_shape CHECK(
    (actor_kind='human' AND human_principal_id IS NOT NULL AND agent_id IS NULL
      AND accountable_human_id IS NULL AND session_id IS NULL AND passport_id IS NULL AND credential_id IS NULL)
    OR (actor_kind='agent' AND agent_id IS NOT NULL AND human_principal_id IS NULL)
  ),
  ADD CONSTRAINT audit_assurance_relation_shape CHECK(
    (assurance='private-authenticated' AND actor_kind='human' AND membership_id IS NOT NULL)
    OR (assurance='private-authenticated' AND actor_kind='agent' AND accountable_human_id IS NOT NULL
        AND membership_id IS NOT NULL AND session_id IS NOT NULL AND passport_id IS NOT NULL AND credential_id IS NOT NULL)
    OR (assurance='public-key-continuity' AND membership_id IS NULL AND passport_id IS NULL AND credential_id IS NULL
        AND (actor_kind='human' OR accountable_human_id IS NOT NULL))
    OR (assurance IN ('legacy','unknown') AND actor_kind='agent' AND accountable_human_id IS NULL
        AND membership_id IS NULL AND session_id IS NULL AND passport_id IS NULL AND credential_id IS NULL)
  ),
  ADD CONSTRAINT audit_new_envelope_required CHECK(
    assurance IN ('legacy','unknown') OR actor_envelope_json IS NOT NULL
  ),
  ADD CONSTRAINT audit_session_project_fkey
    FOREIGN KEY(project_id,session_id,agent_id,accountable_human_id)
      REFERENCES agent_sessions(project_id,id,agent_id,accountable_human_id) ON DELETE RESTRICT,
  ADD CONSTRAINT audit_human_membership_project_fkey
    FOREIGN KEY(project_id,membership_id,human_principal_id)
      REFERENCES project_memberships(project_id,id,human_id) ON DELETE RESTRICT,
  ADD CONSTRAINT audit_agent_membership_project_fkey
    FOREIGN KEY(project_id,membership_id,accountable_human_id)
      REFERENCES project_memberships(project_id,id,human_id) ON DELETE RESTRICT,
  ADD CONSTRAINT audit_passport_project_fkey
    FOREIGN KEY(project_id,passport_id,agent_id,accountable_human_id)
      REFERENCES passports(project_id,id,agent_id,accountable_human_id) ON DELETE RESTRICT,
  ADD CONSTRAINT audit_credential_project_fkey
    FOREIGN KEY(project_id,credential_id,agent_id,accountable_human_id,session_id)
      REFERENCES agent_credentials(project_id,id,agent_id,accountable_human_id,session_id) ON DELETE RESTRICT;

CREATE FUNCTION reject_audit_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$ BEGIN
  RAISE EXCEPTION 'audit_log is immutable' USING ERRCODE='55000';
END $$;
CREATE TRIGGER audit_log_immutable
  BEFORE UPDATE OR DELETE ON audit_log
  FOR EACH ROW EXECUTE FUNCTION reject_audit_mutation();
```

The legacy update sets only new classification/time columns; it preserves every old ID, project, agent, action, `created_at`, and `seq`, creates no human/authenticator/membership/ownership row, and leaves `actor_envelope_json` null.

- [ ] **Step 7: Force project RLS with acting-human project-list policy**

For `project_memberships`, use two policies: normal project isolation for all commands, plus `FOR SELECT USING (human_id = NULLIF(current_setting('wormhole.human_id',true),'')::uuid)` so `ListMyProjects` exposes only the authenticated human's memberships. For each of `project_agent_principals`, `project_owner_bootstrap_grants`, `membership_invitations`, `agent_ownerships`, `agent_sessions`, `passports`, `agent_credentials`, `personal_access_tokens`, and `audit_log`, enable and force RLS and create project isolation with both `USING` and `WITH CHECK` against `wormhole.project_id`. Drop/recreate the older Passport/audit policies so they have `WITH CHECK` before forcing.

- [ ] **Step 8: Add fail-closed down ordering**

`000022_private_identity.down.sql` first uses a `DO` block to raise SQLSTATE `55000` if any private-bound Passport, credential, PAT, membership, invitation, ownership, session, bootstrap grant, authenticator, human session, OIDC state, recovery code, non-legacy audit row, or non-null new audit envelope exists. It then drops the audit trigger/function and new audit constraints/columns, restores `agent_id NOT NULL` and its RESTRICT FK, removes Passport private constraints/columns and restores `UNIQUE(agent_id,project_id)`, drops project tables in credential→session→ownership→invitation/bootstrap→project-agent→membership order, then drops global device/token/session/replay/challenge/recovery/authenticator/human tables in child-first order. Empty-database 22→21 verification must pass.

- [ ] **Step 9: Apply and run GREEN**

Run:

```bash
migrate -path migrations -database "$WORMHOLE_DATABASE_URL" goto 21
migrate -path migrations -database "$WORMHOLE_DATABASE_URL" up
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/identity -run 'TestMigration22' -count=1
migrate -path migrations -database "$WORMHOLE_DATABASE_URL" goto 21
```

Expected: upgrade reaches 22, all tests PASS, and empty-state downgrade reaches 21.

- [ ] **Step 10: Commit**

```bash
git add migrations/000022_* internal/core/identity/private_schema_test.go docs/db-entities.md
git commit -m "feat: add private identity schema"
```

### Task 9: Implement human, membership, ownership, Passport, and credential control planes

**Files:**
- Create: `internal/core/identity/humans.go`
- Create: `internal/core/identity/humans_test.go`
- Create: `internal/core/identity/memberships.go`
- Create: `internal/core/identity/memberships_test.go`
- Create: `internal/core/identity/ownership.go`
- Create: `internal/core/identity/ownership_test.go`
- Create: `internal/core/identity/sessions.go`
- Create: `internal/core/identity/sessions_test.go`
- Create: `internal/core/identity/credentials.go`
- Create: `internal/core/identity/credentials_test.go`
- Create: `internal/core/identity/actors.go`
- Create: `internal/core/identity/actors_test.go`
- Create: `internal/core/identity/issuer_validation.go`
- Create: `internal/core/identity/issuer_validation_test.go`
- Modify: `internal/core/identity/identity.go`
- Create: `cmd/wormhole/project_members.go`
- Create: `cmd/wormhole/project_members_test.go`
- Create: `cmd/wormhole/fabric_auth.go`
- Create: `cmd/wormhole/fabric_auth_test.go`

**Interfaces:**
- Consumes: migration 22 and Task 1 `types.ActorScope`.
- Produces: acting-human control-plane transactions and private credential resolver primitives.

```go
func (s *Store) ProvisionFirstOwnerGrant(context.Context, projectID, issuer, subject string, roles, permissions []string, expiresAt time.Time) error
func (s *Store) ConsumeFirstOwnerGrantInTx(context.Context, *sql.Tx, projectID, humanID, issuer, subject string) (Membership, error)
func (s *Store) CreateInvitation(context.Context, types.ActorScope, InviteInput) (rawCode string, Invitation, error)
func (s *Store) AcceptInvitationInTx(context.Context, *sql.Tx, humanID, issuer, subject, rawCode string) (Membership, error)
func (s *Store) ListMyProjects(context.Context, humanID string) ([]ProjectMembershipSummary, error)
func (s *Store) ListMembers(context.Context, types.ActorScope) ([]Membership, error)
func (s *Store) RevokeMembership(context.Context, types.ActorScope, membershipID string) error
func (s *Store) CreateProjectAgent(context.Context, types.ActorScope, AgentInput) (Agent, Ownership, error)
func (s *Store) TransferOwnership(context.Context, types.ActorScope, agentID, targetMembershipID string) (Ownership, Passport, error)
func (s *Store) StartAgentSession(context.Context, types.ActorScope, agentID string, HarnessDescriptor, ModelDescriptor) (AgentSession, error)
func (s *Store) IssuePrivatePassportInTx(context.Context, *sql.Tx, types.ActorScope, agentID string, permissions []string, expiresAt time.Time) (Passport, error)
func (s *Store) IssueAgentCredentialInTx(context.Context, *sql.Tx, types.ActorScope, passportID, sessionID string, ttl time.Duration) (raw string, Credential, error)
func (s *Store) IssuePAT(context.Context, types.ActorScope, label string, scopes []string, ttl time.Duration) (raw string, PersonalAccessToken, error)
func (s *Store) ResolvePrivateCredential(context.Context, projectID, raw string) (types.ActorScope, error)
func (s *Store) RecordActorActionInTx(context.Context, *sql.Tx, types.ActorScope, action string, payload json.RawMessage) (AuditEntry, error)
```

- [ ] **Step 1: Write failing policy/lifecycle tests**

Add `TestFirstOwnerRequiresExactUnexpiredOneUseOIDCGrantAndEmptyProject`, `TestExistingProjectRequiresInvitation`, `TestInvitationAdminRequiresActingHumanMemberAdmin`, `TestInvitationBoundSubjectRejectsOtherHuman`, `TestProjectListReturnsOnlyActingHumanMemberships`, `TestMemberListAndRevokeRequireMemberAdmin`, `TestMembershipRevokeRevokesDerivedAuthority`, `TestOwnershipTransferRequiresActingHumanOwnershipAdmin`, `TestOwnershipTransferRevokesPriorPassportAndCredentials`, `TestMultipleAgentsPerHuman`, `TestPATScopesMustBeSubsetOfActiveMembership`, `TestPATMaxTTLIs90Days`, `TestResolvePrivateCredentialDerivesActiveServerRecords`, and `TestHistoricalAuditRetainsTransferredOwner`.

- [ ] **Step 2: Run RED**

Run: `WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/identity ./cmd/wormhole -run 'Test(FirstOwner|ExistingProject|Invitation|ProjectList|Member|Ownership|MultipleAgents|PAT|ResolvePrivateCredential|HistoricalAudit)' -count=1`

Expected: FAIL because lifecycle APIs/commands are absent.

- [ ] **Step 3: Implement fixed authorization policy**

The first owner is never “first login wins.” An operator-created, one-use, maximum-24-hour grant binds exact project, normalized OIDC issuer, and subject. Consumption locks the grant and project, requires zero active memberships, creates one owner membership, and marks the grant consumed in the same transaction. Thereafter membership creation requires an invitation issued by an authenticated acting human whose active membership contains `member.admin`; bound issuer/subject invitations reject all other humans. `project list` sets only `wormhole.human_id` and uses the self-membership SELECT policy; member list/revoke set both human and project context and require `member.admin`.

Ownership transfer requires `ownership.admin`, locks old ownership and target membership, ends old ownership, creates the successor, revokes prior Passport/agent credentials, ends old agent sessions, issues a successor Passport, and records one audit row in that transaction. PAT scopes must be a sorted unique subset of the acting membership's current permissions and the fixed automation allowlist; TTL is positive and at most 90 days. Raw invitation, agent credential, recovery, and PAT values use 32 random bytes, prefix-distinct encodings, SHA-256 at rest, and one-time output.

`ResolvePrivateCredential` executes one joined query over credential, Passport,
ownership, membership, human, and session rows; compares every
project/agent/human composite key; and returns the same `ErrInvalidToken` for
missing, cross-project, expired, ended, disabled, or revoked state. It derives a
new private-authenticated `ActorEnvelope` and sorted roles/permissions only from
those rows, calls `Actor.Validate`, and accepts no caller-supplied `ActorScope`.

- [ ] **Step 4: Add exact human commands**

Ship:

```text
fabric bootstrap-owner grant --project <uuid> --issuer <url> --subject <opaque> --expires <rfc3339>
wormhole project list
wormhole project members invite --project <uuid> [--issuer <url> --subject <opaque>] --role <role> --permission <permission> --expires <rfc3339>
wormhole project members list --project <uuid>
wormhole project members revoke --project <uuid> --membership <uuid>
wormhole agent ownership transfer --project <uuid> --agent <uuid> --to-membership <uuid>
wormhole auth pat create --project <uuid> --label <label> --scope <scope> --ttl <duration>
wormhole auth pat revoke --project <uuid> --pat <uuid>
```

Every `wormhole` command requires the current human session, sends no claimed human ID, and displays raw secret material only for creation. The Fabric operator command is local-server administration and writes the exact issuer/subject grant; it cannot create a human or membership directly.

- [ ] **Step 5: Run GREEN**

Run: `WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/identity ./cmd/wormhole -run 'Test(FirstOwner|ExistingProject|Invitation|ProjectList|Member|Ownership|MultipleAgents|PAT|ResolvePrivateCredential|HistoricalAudit)' -count=1`

Expected: PASS, including concurrent first-owner and invitation redemption tests with exactly one winner and fail-closed private credential derivation.

- [ ] **Step 6: Commit**

```bash
git add internal/core/identity cmd/wormhole/project_members.go cmd/wormhole/project_members_test.go cmd/wormhole/fabric_auth.go cmd/wormhole/fabric_auth_test.go
git commit -m "feat: add accountable private identity control"
```

### Task 10: Obtain human approval for OIDC dependencies

**Files:**
- Modify after approval only: `go.mod`
- Modify after approval only: `go.sum`

**Interfaces:**
- Consumes: implementation-rule R4 human dependency approval.
- Produces: approved upstream OIDC/JWT and OAuth2/PKCE implementations; Task 11 is blocked until approval is recorded.

- [ ] **Step 1: Present the exact approval request**

```text
Approve adding github.com/coreos/go-oidc/v3/oidc v3.20.0 for issuer/JWKS/ID-token verification and golang.org/x/oauth2 v0.36.0 for Authorization Code + PKCE and RFC 8628 exchanges. Both are used only by the private human-auth control plane. WebAuthn remains disabled; no WebAuthn dependency, route, flag, schema, or dormant implementation will be added. JWT/JWK verification will not be hand-written.
```

- [ ] **Step 2: Stop unless the exact approval is recorded**

Record the approving human, UTC time, exact module paths/versions, and link or transcript reference in the task/PR. If absent, make no `go.mod`, `go.sum`, or OIDC implementation change and report Task 10 blocked. Approval for a different version or general OAuth work does not satisfy this gate.

- [ ] **Step 3: Add only the approved modules**

Run after approval:

```bash
go get github.com/coreos/go-oidc/v3/oidc@v3.20.0
go get golang.org/x/oauth2@v0.36.0
go mod tidy
go mod verify
```

Expected: both exact modules resolve and `go mod verify` prints `all modules verified`.

- [ ] **Step 4: Verify scope and absence of WebAuthn**

Run:

```bash
go list -m all | rg 'github.com/coreos/go-oidc/v3 v3.20.0|golang.org/x/oauth2 v0.36.0'
go list -m all | rg -i 'webauthn|passkey' && exit 1 || true
```

Expected: the two approved lines appear; the WebAuthn scan prints nothing.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum
git commit -m "build: approve OIDC dependencies"
```

### Task 11: Implement one-use OIDC, refresh rotation, device login, and recovery

**Files:**
- Create: `internal/webui/oidc.go`
- Create: `internal/webui/oidc_test.go`
- Create: `internal/core/identity/oidc_state.go`
- Create: `internal/core/identity/oidc_state_test.go`
- Modify: `internal/types/config.go`
- Modify: `internal/types/config_test.go`
- Modify: `internal/webui/api.go`
- Modify: `internal/webui/api_test.go`
- Modify: `cmd/fabric/main.go`
- Modify: `cmd/fabric/main_test.go`
- Modify: `cmd/wormhole/fabric_auth.go`
- Modify: `cmd/wormhole/fabric_auth_test.go`
- Create: `internal/runtime/config/credentials.go`
- Create: `internal/runtime/config/credentials_test.go`

**Interfaces:**
- Consumes: Tasks 8–10.
- Produces: browser Authorization Code+PKCE, RFC 8628 device login, access/refresh rotation, logout, and throttled recovery; no WebAuthn.

```go
// internal/types/config.go; types.Config has OIDCProviders map[string]OIDCProviderConfig.
type OIDCProviderConfig struct {
    Alias, Issuer, ClientID string
    AuthorizationEndpoint, TokenEndpoint, DeviceAuthorizationEndpoint, JWKSURL string
}
type LoginChallengeRequest struct { ProviderAlias, RedirectURI, PKCES256Challenge string }
type LoginChallenge struct { ID, Secret, State, Nonce, AuthorizationURL string; ExpiresAt time.Time }
func (s *Store) CreateOIDCChallenge(context.Context, LoginChallengeRequest) (LoginChallenge, error)
func (s *Store) ConsumeOIDCChallenge(context.Context, challengeID, secret, state string) (StoredOIDCChallenge, error)
func (s *Store) ConsumeIDTokenReplayInTx(context.Context, *sql.Tx, issuer, subject string, rawIDToken []byte, expiresAt time.Time) error
func (s *Store) RotateRefreshToken(context.Context, raw string) (rawAccess, rawRefresh string, scope types.ActorScope, err error)
func (s *Store) RevokeSessionFamily(context.Context, familyID, reason string) error
func (s *Store) BeginDeviceAuthorization(context.Context, providerAlias string) (DeviceAuthorization, error)
func (s *Store) PollDeviceAuthorization(context.Context, id, rawDeviceNonce string) (SessionTokens, error)
func (s *Store) RecoverHuman(context.Context, subjectHint, sourceKey, rawCode string) (RecoverySession, error)
```

- [ ] **Step 1: Write failing protocol/security tests**

Add `TestOIDCChallengeIsFabricIssuedOneUseAndBoundToProviderRedirectPKCE`, `TestOIDCUnknownProviderAliasPerformsZeroDiscoveryDNSAndHTTP`, `TestOIDCCallerCannotSupplyIssuerClientOrEndpoint`, `TestOIDCConfiguredEndpointOriginIsValidatedBeforeNetwork`, `TestOIDCRejectsStateNonceIssuerAudienceAndPKCEMismatch`, `TestOIDCIDTokenReplayRejectedAcrossChallenges`, `TestOIDCCodeChallengeConcurrentConsumeHasOneWinner`, `TestRefreshRotatesAndMarksPriorUsed`, `TestRefreshReuseRevokesWholeFamily`, `TestConcurrentRefreshHasOneWinnerAndOneFamilyRevocation`, `TestDeviceUnknownProviderAliasPerformsZeroNetwork`, `TestDevicePollRequiresExact32ByteDeviceNonce`, `TestDevicePollRateAndExpiry`, `TestRecoveryCodeOneUse`, `TestRecoveryThrottleFivePer15MinutesAndTwentyPer24Hours`, `TestOIDCSecretsRedacted`, and `TestNoWebAuthnSurface`. Zero-network tests inject discovery, DNS, dialer, and HTTP counters and require all four to remain zero.

- [ ] **Step 2: Run RED**

Run: `go test ./internal/webui ./internal/core/identity ./cmd/fabric ./cmd/wormhole ./internal/runtime/config -run 'Test(OIDC|Refresh|Device|Recovery|NoWebAuthn)' -count=1`

Expected: FAIL because protocol handlers are absent.

- [ ] **Step 3: Implement browser code+PKCE flow**

CLI generates a 32-byte PKCE verifier and S256 challenge, then requests a Fabric challenge with only a provider alias, loopback redirect URI, and challenge. `types.Config.OIDCProviders` is the sole allowlist, keyed by normalized alias; configuration load validates every provider before the HTTP server starts. Before discovery, DNS, dialing, authorization URL construction, token exchange, or JWKS fetch, Fabric looks up that alias in the immutable map. The selected entry supplies the exact normalized issuer, client ID, authorization endpoint, token endpoint, device authorization endpoint, and JWKS URL; caller JSON has no issuer, client ID, URL, host, or endpoint fields. Unknown aliases and any configured endpoint with userinfo, fragment, non-HTTPS non-loopback scheme, or origin outside the provider's configured issuer origin fail before network. Build `oauth2.Config` and `oidc.NewRemoteKeySet`/`oidc.NewVerifier` exclusively from the selected configuration; do not use caller-driven discovery. If deployments enable discovery as an operator option, it may contact only the selected configured issuer and must byte-match every configured endpoint before use.

Fabric then generates independent 32-byte challenge secret, state, and nonce; it stores only hashes plus the selected provider alias and resolved issuer/client/endpoints, loopback redirect URI, S256 challenge, and five-minute expiry. CLI opens the returned configured authorization URL and receives code/state on its loopback listener. It sends code, verifier, challenge ID/secret/state to Fabric. Fabric atomically consumes the challenge before exchange, uses the selected configured `oauth2.Config.Exchange` with the verifier, and verifies the ID token with the selected configured verifier using exact issuer, audience/client ID, nonce, expiry, and signature. It inserts SHA-256 of the complete raw ID token into `oidc_id_token_replays`; a duplicate is rejected even under a different challenge. Do not parse or verify JWT/JWK manually.

- [ ] **Step 4: Implement refresh-family replay defense**

Issue `whs_` access and `whr_` refresh values from independent 32-byte randomness. Refresh locks the family and token row. A valid unused token is marked used, a sequence+1 token is inserted, `replaced_by_id` is set, and a new access token is issued in one transaction. Presentation of any used/replaced token sets family `revoked_at`/`refresh_reuse`, revokes all access tokens, returns `ErrInvalidToken`, and issues nothing. Logout revokes the family. Absolute family lifetime is 30 days; access lifetime is 15 minutes; refresh rotation never extends the absolute deadline.

- [ ] **Step 5: Implement RFC 8628 device flow with a separate nonce**

Fabric accepts only the provider alias, resolves it through the same allowlist before any network call, and calls that provider's configured device authorization endpoint through `x/oauth2`. It generates a 32-byte RawURLEncoding device nonce returned once to CLI, encrypts the provider device code at rest, and stores only user-code and device-nonce hashes. Poll requires exact nonce, enforces provider interval and `slow_down`, never returns provider tokens, verifies the resulting ID token with the same configured issuer/audience/JWKS and replay checks, and returns Wormhole session tokens once. Wrong/unknown alias, wrong nonce, early poll, expiry, denial, or completed replay returns a collapsed error without identity disclosure.

- [ ] **Step 6: Implement recovery and throttling**

Recovery codes are ten independent 128-bit RawURLEncoding values returned once and SHA-256 hashed. A successful code is marked used and issues a 10-minute recovery-only human session that may relink OIDC or rotate remaining codes but cannot list/mutate projects, issue PATs, invite members, or own agents. Count failures per `(SHA256 normalized subject hint, SHA256 coarse source key)`; block at five failures in a rolling 15-minute window and at twenty failures across source keys in 24 hours. Success does not erase attempt history. Tests use an injected clock and never weaken the fixed limits.

- [ ] **Step 7: Run GREEN and secret scans**

Run:

```bash
go test ./internal/webui ./internal/core/identity ./cmd/fabric ./cmd/wormhole ./internal/runtime/config -run 'Test(OIDC|Refresh|Device|Recovery|NoWebAuthn)' -count=1
go test -race ./internal/webui ./internal/core/identity -run 'Test(OIDCCodeChallengeConcurrent|ConcurrentRefresh)' -count=1
rg -n 'authorization_code|refresh_token|device_code|recovery_code' docs/testing/results internal/runtime/testdata && exit 1 || true
```

Expected: tests PASS; scan prints nothing from evidence/fixture paths.

- [ ] **Step 8: Commit**

```bash
git add internal/webui internal/core/identity/oidc_state.go internal/core/identity/oidc_state_test.go internal/types/config.go internal/types/config_test.go cmd/fabric cmd/wormhole internal/runtime/config
git commit -m "feat: add private OIDC authentication"
```

### Task 12: Revoke legacy authority and add explicit recovery in migration 000023

**Files:**
- Create: `migrations/000023_legacy_identity_recovery.up.sql`
- Create: `migrations/000023_legacy_identity_recovery.down.sql`
- Create: `internal/core/identity/legacy_recovery.go`
- Create: `internal/core/identity/legacy_recovery_test.go`
- Modify: `internal/core/identity/identity.go`
- Modify: `internal/core/identity/identity_test.go`
- Modify: `cmd/wormhole/fabric_auth.go`
- Modify: `cmd/wormhole/fabric_auth_test.go`
- Modify: `docs/db-entities.md`

**Interfaces:**
- Consumes: migration 22 and Task 9 acting-human APIs.
- Produces: rejected legacy bearer credentials before Task 13 resolver cutover, explicit recovery records, and non-resurrecting downgrade.

- [ ] **Step 1: Write failing version-20 upgrade and up/down rejection tests**

Seed a version-20 database with agents, Passports, raw-token hashes, viewer keys, and audit rows. Add `TestMigration23RevokesEveryUnboundLegacyTokenBeforeResolverCutover`, `TestWhoAmIRejectsRevokedLegacyTokenAtVersion23`, `TestMigration23CreatesNoHumanOrOwnership`, `TestLegacyRecoveryRequiresOIDCHumanAndActiveMembership`, `TestLegacyRecoveryIsIdempotent`, `TestMigration23DownDeletesMigratedRevokedLegacyTokens`, and `TestLegacyWhoAmIRejectsOldRawTokenAfterUpThenDown`. The last test authenticates the old token at version 20, upgrades through 23, downgrades to 22, and proves old `WhoAmI` rejects because the row no longer exists.

- [ ] **Step 2: Run RED from version 22**

Run:

```bash
migrate -path migrations -database "$WORMHOLE_DATABASE_URL" goto 22
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/identity ./cmd/wormhole -run 'Test(Migration23|LegacyRecovery|LegacyWhoAmI)' -count=1
```

Expected: FAIL because migration 23 and recovery are absent.

- [ ] **Step 3: Create the exact migration-23 up SQL**

```sql
ALTER TABLE agent_tokens
  ADD COLUMN revoked_at timestamptz,
  ADD COLUMN revocation_reason text;

CREATE TABLE legacy_identity_recoveries (
  project_id uuid NOT NULL,
  agent_id uuid NOT NULL,
  legacy_passport_id uuid NOT NULL,
  state text NOT NULL CHECK(state IN ('recovery_required','completed')),
  completed_by_human_id uuid REFERENCES human_principals(id) ON DELETE RESTRICT,
  completed_membership_id uuid,
  replacement_passport_id uuid,
  replacement_credential_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  PRIMARY KEY(project_id,agent_id,legacy_passport_id),
  FOREIGN KEY(legacy_passport_id,project_id,agent_id)
    REFERENCES passports(id,project_id,agent_id) ON DELETE RESTRICT,
  FOREIGN KEY(completed_membership_id,project_id,completed_by_human_id)
    REFERENCES project_memberships(id,project_id,human_id) ON DELETE RESTRICT,
  FOREIGN KEY(replacement_passport_id,project_id,agent_id)
    REFERENCES passports(id,project_id,agent_id) ON DELETE RESTRICT,
  FOREIGN KEY(replacement_credential_id,project_id)
    REFERENCES agent_credentials(id,project_id) ON DELETE RESTRICT,
  CHECK((state='recovery_required' AND completed_at IS NULL AND completed_by_human_id IS NULL
         AND completed_membership_id IS NULL AND replacement_passport_id IS NULL AND replacement_credential_id IS NULL)
     OR (state='completed' AND completed_at IS NOT NULL AND completed_by_human_id IS NOT NULL
         AND completed_membership_id IS NOT NULL AND replacement_passport_id IS NOT NULL AND replacement_credential_id IS NOT NULL))
);

INSERT INTO legacy_identity_recoveries(project_id,agent_id,legacy_passport_id,state)
SELECT project_id,agent_id,id,'recovery_required'
FROM passports WHERE membership_id IS NULL OR ownership_id IS NULL;

UPDATE agent_tokens t
SET revoked_at=now(), revocation_reason='legacy_binding_missing'
WHERE t.revoked_at IS NULL
  AND EXISTS (
    SELECT 1 FROM legacy_identity_recoveries r
    WHERE r.project_id=t.project_id AND r.agent_id=t.agent_id
  );

ALTER TABLE legacy_identity_recoveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE legacy_identity_recoveries FORCE ROW LEVEL SECURITY;
CREATE POLICY legacy_identity_recoveries_project_isolation ON legacy_identity_recoveries
  USING(project_id=NULLIF(current_setting('wormhole.project_id',true),'')::uuid)
  WITH CHECK(project_id=NULLIF(current_setting('wormhole.project_id',true),'')::uuid);
```

- [ ] **Step 4: Create a non-resurrecting down migration**

```sql
DELETE FROM agent_tokens t
USING legacy_identity_recoveries r
WHERE t.project_id=r.project_id
  AND t.agent_id=r.agent_id
  AND t.revocation_reason='legacy_binding_missing';

DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM agent_tokens WHERE revoked_at IS NOT NULL OR revocation_reason IS NOT NULL) THEN
    RAISE EXCEPTION 'migration 000023 downgrade refused: remaining token revocation cannot be represented'
      USING ERRCODE='55000';
  END IF;
END $$;

DROP TABLE legacy_identity_recoveries;
ALTER TABLE agent_tokens DROP COLUMN revocation_reason, DROP COLUMN revoked_at;
```

This deliberately deletes migrated legacy token rows before dropping revocation columns. It never turns a revoked raw credential valid again; unrelated post-23 revocations make downgrade fail closed.

- [ ] **Step 5: Enforce revocation in legacy `WhoAmI` before any cutover commit**

Change the legacy `identity.Store.WhoAmI` token query in `internal/core/identity/identity.go` to project `NULLIF(to_jsonb(t)->>'revoked_at','')::timestamptz` from alias `agent_tokens t` and require the scanned value to be null in addition to the existing hash, expiry, project, agent, and Passport checks. This expression remains valid after migration-23 down removes the column; the deleted legacy row then yields no match. Return the same `ErrInvalidToken` for revoked, missing, expired, or cross-project values. Migration 23 and this `WhoAmI` enforcement are one indivisible task commit: do not commit/deploy the migration without the query change, and do not merge Task 13's composite resolver/admin cutover until this task's version-23 and up→down tests pass.

- [ ] **Step 6: Implement explicit recovery**

`RecoverLegacyAgentInTx(ctx, tx, actingHumanScope, agentID)` locks the recovery, requires a private-authenticated human scope and active membership, creates project-agent ownership, new private Passport, agent session, and replacement `wha_` credential, then marks the recovery complete and writes the audit row in the same transaction. It never updates `agents.owner` or old audit fields. CLI command is:

```text
wormhole auth recover-agent --project <uuid> --agent <uuid> --harness <name> --harness-version <version>
```

It prints the replacement credential exactly once and redacts it on every error/replay.

- [ ] **Step 7: Apply, run GREEN, and prove up/down rejection**

Run:

```bash
migrate -path migrations -database "$WORMHOLE_DATABASE_URL" goto 20
migrate -path migrations -database "$WORMHOLE_DATABASE_URL" up
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/identity ./cmd/wormhole -run 'Test(Migration23|LegacyRecovery|LegacyWhoAmI)' -count=1
migrate -path migrations -database "$WORMHOLE_DATABASE_URL" goto 22
```

Expected: upgrade reaches 23, tests PASS, downgrade reaches 22 only after deleting migrated legacy token rows, and old `WhoAmI` still rejects.

- [ ] **Step 8: Commit migration, revocation enforcement, and recovery together**

```bash
git add migrations/000023_* internal/core/identity/identity.go internal/core/identity/identity_test.go internal/core/identity/legacy_recovery.go internal/core/identity/legacy_recovery_test.go cmd/wormhole/fabric_auth.go cmd/wormhole/fabric_auth_test.go docs/db-entities.md
git commit -m "feat: recover legacy identities explicitly"
```

### Task 13: Derive every Fabric actor scope server-side and commit mutation provenance atomically

**Files:**
- Create: `internal/mcp/mutation.go`
- Create: `internal/mcp/mutation_test.go`
- Modify: `internal/mcp/auth.go`
- Modify: `internal/mcp/registry.go`
- Modify: `internal/mcp/jsonrpc.go`
- Modify: `internal/mcp/agent.go`
- Modify: `internal/mcp/channel.go`
- Modify: `internal/mcp/git.go`
- Modify: `internal/mcp/integration_manifest.go`
- Modify: `internal/mcp/kb.go`
- Modify: `internal/mcp/sync.go`
- Modify: `internal/mcp/sync_v2.go`
- Modify: `internal/mcp/task.go`
- Modify: corresponding `internal/mcp/*_test.go` files
- Modify: `internal/core/events/events.go`
- Modify: `internal/core/git/git.go`
- Modify: `internal/core/kb/kb.go`
- Modify: `internal/core/tasks/tasks.go`
- Modify: corresponding Core tests
- Modify: `internal/webui/admin.go`
- Modify: `internal/webui/api.go`
- Modify: `internal/core/identity/viewer_keys.go`
- Modify: `cmd/fabric/main.go`

**Interfaces:**
- Consumes: public resolver Task 6, private stores Task 9, migration-23 revocation Task 12.
- Produces: one resolver, `types.ActorScope` handler signature, trusted actor replacement, and atomic mutation/audit coordination for every Fabric MCP mutation.

```go
type Handler func(context.Context, types.ActorScope, string, json.RawMessage) (any, error)
type MutationFunc func(context.Context, *sql.Tx, types.ActorScope) (any, error)
type MutationCoordinator struct { identity *identity.Store }
func (m *MutationCoordinator) Execute(context.Context, types.ActorScope, string, json.RawMessage, MutationFunc) (any, error)
func ReconcileRequestActor(*types.ActorEnvelope, types.ActorScope) (types.ActorEnvelope, error)

func (s *events.Store) CreateChannelInTx(context.Context, *sql.Tx, string, string) (Channel, error)
func (s *events.Store) PublishEventInTx(context.Context, *sql.Tx, PublishInput) (Event, error)
func (s *git.Store) LinkCommitInTx(context.Context, *sql.Tx, LinkCommitInput) (GitLink, error)
func (s *git.Store) RequestReviewInTx(context.Context, *sql.Tx, ReviewInput) (GitLink, error)
func (s *kb.Store) WriteArticleInTx(context.Context, *sql.Tx, WriteArticleInput) (Article, error)
func (s *tasks.Store) CreateInTx(context.Context, *sql.Tx, CreateInput) (Task, error)
func (s *tasks.Store) AssignInTx(context.Context, *sql.Tx, AssignInput) (Task, error)
func (s *tasks.Store) UpdateStatusInTx(context.Context, *sql.Tx, UpdateStatusInput) (Task, error)
```

- [ ] **Step 1: Write failing resolver, forgery, cutover, and rollback tests**

Add table tests over every registered tool in `agent.go`, `channel.go`, `git.go`, `integration_manifest.go`, `kb.go`, `sync.go`, and `task.go`. `TestEveryAuthenticatedHandlerUsesActorScope` fails if `identity.AuthenticatedScope` remains in production MCP code. `TestRequestActorAccountableHumanAndAssuranceCannotBeForged` changes each envelope field and expects `ErrActorClaimMismatch`. `TestRequestActorIsReplacedWithDerivedScope` supplies an equal envelope then proves the handler received the server copy. `TestPrivateResolverRefusesSchemaBefore23` starts Fabric at schema 22 and expects startup failure. `TestMutationRollsBackWhenAuditInsertFails` injects audit failure for each Core mutation and proves no domain row/event/stream version commits. `TestPermissionDenialIsAuditedAndCannotFailOpen` proves denial plus immutable audit. `TestRevocationEffectiveOnNextRequest` covers membership, ownership, Passport, agent credential, human logout, refresh-family, and PAT revocation.

- [ ] **Step 2: Run RED**

Run: `WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/mcp ./internal/core/events ./internal/core/git ./internal/core/kb ./internal/core/tasks ./internal/webui ./cmd/fabric -run 'Test(EveryAuthenticated|RequestActor|PrivateResolver|Mutation|PermissionDenial|Revocation)' -count=1`

Expected: FAIL because handlers still use `AuthenticatedScope` and mutations/audit have separate transactions.

- [ ] **Step 3: Implement resolver and actor reconciliation**

Fabric startup refuses private-mode dispatch until migration version is at least 23. HTTP reads and bounds the request once, canonicalizes parameters, then selects exactly one auth family: public proof, `whs_` human access, `wha_` agent credential, or `whp_` PAT. Prefix mismatch/unknown/expired/revoked/cross-project all return one `ErrInvalidToken`. Core receives no raw credential.

`ReconcileRequestActor` first validates a supplied envelope structurally, canonicalizes supplied and resolved envelopes with `projectstate.CanonicalJSON`, and rejects any byte difference. It then returns `scope.Actor`, never the request object. A missing optional envelope also returns `scope.Actor`. All operation/event/audit authors use this returned server object; accountable human, assurance, session, harness, model, roles, and permissions are never taken from request JSON.

- [ ] **Step 4: Implement atomic mutation and audit**

`MutationCoordinator.Execute` starts one identity project transaction, sets `wormhole.project_id` and `wormhole.human_id` from scope, revalidates issuer state in that transaction, executes the supplied `*InTx` Core mutation, calls `RecordActorActionInTx` with canonical action/payload, and commits. Any mutation or audit error rolls back. Refactor all mutating tools to this coordinator and all read tools to the derived scope signature. Permission denial runs a dedicated audit-only project transaction and returns denial only after the audit commits; audit failure returns an internal error, never authorization success.

Remove production `identity.AuthenticatedScope`. Preserve the v1 protocol objects and route them through a valid newly resolved scope. Replace dashboard admin/viewer authorization with human session/PAT plus active membership; remove live `WORMHOLE_ADMIN_KEY`, `X-Admin-Key`, and `--admin-key`. Legacy unknown viewer keys cannot issue keys or mutate; migration/recovery may retain read-only historical display until explicitly revoked.

- [ ] **Step 5: Run GREEN and production-symbol scans**

Run:

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/mcp ./internal/core/events ./internal/core/git ./internal/core/kb ./internal/core/tasks ./internal/webui ./cmd/fabric -run 'Test(EveryAuthenticated|RequestActor|PrivateResolver|Mutation|PermissionDenial|Revocation|AdminKey|Viewer)' -count=1
rg -n 'AuthenticatedScope|WORMHOLE_ADMIN_KEY|X-Admin-Key|--admin-key' internal cmd --glob '!**/*_test.go' && exit 1 || true
```

Expected: tests PASS and the production-symbol scan prints nothing.

- [ ] **Step 6: Commit**

```bash
git add internal/mcp internal/core/events internal/core/git internal/core/kb internal/core/tasks internal/core/identity/viewer_keys.go internal/webui cmd/fabric
git commit -m "feat: derive and audit Fabric actor scopes"
```

### Task 14: Recover legacy local Fabric state without creating partial bindings

**Files:**
- Create: `internal/runtime/config/legacy_credentials.go`
- Create: `internal/runtime/config/legacy_credentials_test.go`
- Create: `internal/runtime/localstore/legacy_fabric.go`
- Create: `internal/runtime/localstore/legacy_fabric_test.go`
- Create: `cmd/wormhole/migrate.go`
- Create: `cmd/wormhole/migrate_test.go`

**Interfaces:**
- Consumes: Task 2 quarantine tables and Slice A's completed `Service.MigrateLegacyIntegrationState` plus `legacy_integration_state_migrations` outcome. It does not edit, rename, move, ignore, or reimplement `.wormhole/integration-state.json`.
- Produces: idempotent alpha credential/hint inspection and an explicit authenticated recovery workflow.

```go
type LegacyFabricCandidate struct {
    RecoveryID, ServerURL, ProjectID, AgentID, PassportID, CredentialPathHash string
}
func InspectLegacyCredential(path string) (LegacyFabricCandidate, error)
func (r *LegacyFabricRepo) QuarantineProfile(context.Context, LegacyFabricCandidate) error
func (r *LegacyFabricRepo) QuarantineHint(context.Context, string, types.WorkspaceScope, projectstate.FabricHintV1, string) error
func (r *LegacyFabricRepo) ListRecoveries(context.Context, types.WorkspaceScope) ([]LegacyRecovery, error)
func (r *LegacyFabricRepo) CompleteRecoveryInTx(context.Context, *sql.Tx, string, types.FabricProfile, types.FabricBinding) error
```

- [ ] **Step 1: Write failing filesystem/quarantine/restart tests**

Add `TestLegacyCredentialRejectsSymlinkNonRegularAndModeAbove0600`, `TestLegacyCredentialOutputNeverContainsRawTokenOrPath`, `TestLegacyHintWithMissingInstanceCreatesOnlyQuarantine`, `TestLegacyHintAmbiguousWorkspaceCreatesNoBinding`, `TestLegacyForkRecoveryPerformsZeroNetworkCalls`, `TestLegacyRecoveryCrashAtEachStageResumesIdempotently`, `TestLegacyQueueRowsRemainQuarantined`, and `TestLegacyIntegrationStateOutcomeIsConsumedWithoutSecondRename`. The final test seeds each Slice-A outcome (`migrated_and_moved`, `migrated_tracked_source_retained`, `ignored_unsafe`), hashes the repository/index/files before this command, and proves they are byte-identical afterward.

- [ ] **Step 2: Run RED**

Run: `go test ./internal/runtime/config ./internal/runtime/localstore ./cmd/wormhole -run 'TestLegacy(Credential|Hint|Fork|Recovery|Queue|IntegrationState)' -count=1`

Expected: FAIL because the recovery command/repositories are absent.

- [ ] **Step 3: Implement inspection and quarantine**

`wormhole migrate alpha-state` accepts explicit credential file and workspace flags; it never scans arbitrary home-directory files. Open with no-follow semantics, require one-link regular owner-owned mode `0600` or stricter, decode exact legacy server/project/agent/Passport/token fields with unknown-field rejection, validate UUIDs/URL, and retain raw token only in the owner-only credential file. Store SHA-256 of the canonical path, never the path or token. If workspace selection is ambiguous, require `--workspace <uuid>` and write only `legacy_fabric_hint_recoveries(reason='ambiguous_workspace')`. Missing Fabric instance/stream always produces quarantine rows; it never inserts `fabric_profiles`, `workspace_fabric_bindings`, cursors, or active queue rows.

- [ ] **Step 4: Implement explicit recovery completion**

`wormhole migrate alpha-state recover --recovery <uuid> --workspace <uuid> --confirm-server <scheme://host>` first performs the Task-7 immutable-origin decision. Fork mismatch returns before credential read or network construction. On match, the human confirms the exact server, the old credential may call only the legacy recovery endpoint, Fabric rotates it to a new bound credential, returns immutable Fabric instance/remote project/stream/repository/ref values, and invalidates the old token. One SQLite transaction then creates the complete profile (whose `credential_ref` points to a newly written owner-only secret), complete binding/cursor, and marks profile/hint recovery rows completed. Injected failure before commit removes the new secret; failure after secret fsync but before commit records an owner-only recovery journal reference and resume either completes the DB transaction or deletes the orphan. Namespace-only queue recovery rows are displayed for manual semantic re-entry and are never transmitted.

Read Slice A's `legacy_integration_state_migrations` row and report its fixed outcome only. Do not modify `internal/runtime/projectstate/legacy_integration.go`, `.gitignore`, the legacy file, or Git index; Slice A exclusively owns those effects.

- [ ] **Step 5: Run GREEN and secret scan**

Run:

```bash
go test ./internal/runtime/config ./internal/runtime/localstore ./cmd/wormhole -run 'TestLegacy(Credential|Hint|Fork|Recovery|Queue|IntegrationState)' -count=1
go test -race ./internal/runtime/localstore ./cmd/wormhole -run 'TestLegacyRecoveryCrash' -count=1
rg -n 'Bearer |wha_|whs_|whr_|whp_' testdata docs/testing/results && exit 1 || true
```

Expected: PASS; secret scan prints nothing; no partial immutable binding exists at any fault point.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/config/legacy_credentials.go internal/runtime/config/legacy_credentials_test.go internal/runtime/localstore/legacy_fabric.go internal/runtime/localstore/legacy_fabric_test.go cmd/wormhole/migrate.go cmd/wormhole/migrate_test.go
git commit -m "feat: recover legacy local Fabric state"
```

### Task 15: Publish contracts and verify every upgrade/downgrade path

**Files:**
- Modify: `README.md`
- Modify: `docs/mcp-protocol.md`
- Modify: `docs/db-entities.md`
- Modify: `docs/compatibility.md`
- Modify: `docs/releasing.md`
- Modify: `docs/contracts/README.md`
- Modify: `docs/contracts/alpha-contract.json`
- Modify: `docs/claude-code-connector.md`
- Modify: `docs/wiki/CLI-Guide.md`
- Modify: `cmd/wormhole/contract_manifest_test.go`
- Modify: `internal/mcp/contract_manifest_test.go`
- Modify: `internal/runtime/localapi/contract_manifest_test.go`
- Modify: `internal/runtime/sync/contract_manifest_test.go`
- Modify: `.github/scripts/test-alpha-upgrade.sh`
- Modify: `.github/workflows/migrations.yml`

**Interfaces:**
- Consumes: shipped Tasks 1–14 and Slice-C setup.
- Produces: truthful live command/schema/auth inventory plus reproducible SQLite-v7 and Postgres-21–23 migration gates.

- [ ] **Step 1: Add failing contract assertions**

Assert exact canonical type package/API names, including `DecodeOperation`,
`CanonicalOperation`, `DigestCanonicalJSON`, and `DigestCanonicalMarkdown`; local schema
version 7; Postgres migrations 21–23; strict stored operation ID/bytes/digest verification;
profile-only credential authority; private-credential v1 request/result/error/descriptor-
branch compatibility plus public exclusion; ID-free v2 routes; token prefixes;
first-owner/invitation/member/project-list/ownership/PAT/
recovery commands; GitHub-only v1 observer; and absence of live `join`, `connect`,
admin-key, WebAuthn, duplicate codec/canonicalizer, or duplicate legacy integration-state
migration claims.

- [ ] **Step 2: Run RED**

Run: `go test ./cmd/wormhole ./internal/mcp ./internal/runtime/localapi ./internal/runtime/sync -run 'Test.*Contract' -count=1`

Expected: FAIL because live inventory/docs still describe alpha authority.

- [ ] **Step 3: Update live documentation atomically**

Document public continuity as pseudonymous, private assurance as server-issued, GitHub provider IDs and exact commits, separately configured Fabric-side private Git credential, complete binding identity, profile-only `credential_ref`, per-version canonical trees/operations, legacy quarantine/recovery, OIDC code+PKCE and RFC 8628 defaults, first-owner grant/invitations, refresh replay defense, and WebAuthn as unshipped. Keep dated prior alpha specs/results unchanged as historical evidence. Slice C has already removed `join`/`connect`; assert their absence here and do not reintroduce either flow.

- [ ] **Step 4: Replace migration scripts with explicit version transitions**

`test-alpha-upgrade.sh` creates a fresh database, runs full `up`, full `down`, then seeds fixtures at version 20 with `migrate ... goto 20`, runs `migrate ... up`, asserts version 23 and preserved IDs/audit plus revoked legacy tokens, runs 23→22 and proves old `WhoAmI` rejection, and tests 22→21 only on an empty private-identity fixture. SQLite tests construct exact schema versions 1 through 5, open through one-way `000006_fabric_routes.sql` and `000007_sync_binding.sql`, verify per-file transactional rollback and quarantine copy, and reject noncanonical migration filenames; they do not claim a reverse migration. No command uses `up 1`, assumes the current version, edits committed SQLite migrations 1–5, or edits Postgres migrations 1–20.

- [ ] **Step 5: Run GREEN contract/release gates**

Run:

```bash
go test ./cmd/wormhole ./internal/mcp ./internal/runtime/localapi ./internal/runtime/sync -run 'Test.*Contract' -count=1
.github/scripts/check-contract-manifest.sh
.github/scripts/test-alpha-upgrade.sh
make release-test
make release-rehearsal
```

Expected: all PASS; rehearsal creates no tag, release, or publication.

- [ ] **Step 6: Commit**

```bash
git add README.md docs cmd/wormhole/contract_manifest_test.go internal/mcp/contract_manifest_test.go internal/runtime/localapi/contract_manifest_test.go internal/runtime/sync/contract_manifest_test.go .github/scripts/test-alpha-upgrade.sh .github/workflows/migrations.yml
git commit -m "docs: publish multi-Fabric identity contracts"
```

### Task 16: Freeze issue-56 subsidiary codes and four-VM preflight

**Files:**
- Create: `docs/operators/git-native-four-vm-trial.md`
- Create: `docs/testing/four-vm-acceptance.md`
- Create: `testdata/trial/four-vm/README.md`
- Create: `cmd/gatewayd/four_vm_acceptance_test.go`
- Modify: `internal/runtime/localapi/trial_metrics.go`
- Modify: `internal/runtime/localapi/trial_metrics_test.go`
- Modify: `cmd/wormhole/trial_metrics.go`
- Modify: `cmd/wormhole/trial_metrics_test.go`
- Modify: `docs/testing/closed-trial-metrics.md`
- Modify: `docs/operators/alpha-validation-trial.md`

**Interfaces:**
- Consumes: complete D/E/F release candidate.
- Produces: deterministic automated topology preflight and a closed real-evidence schema; it produces no participant or Gate-D result.

Freeze these exact acceptance codes:

```text
external_users_at_least_3
matched_comparison_every_completed_participant
real_redacted_exports_privacy_valid
single_gate_d_decision
supporting_and_contrary_evidence_present
no_beta_or_full_codegraph_claim
```

Freeze these exact prerequisite codes:

```text
gate_c_complete
trial_tooling_merged_before_collection
exact_rc_sha_deployed
local_only_no_phone_home
consent_chronology_valid
withdrawal_receipts_valid
negative_incomplete_contrary_retained
release_test_pass
release_rehearsal_pass
make_check_pass
coverage_at_least_80
hosted_ci_security_migrations_same_sha_pass
```

- [ ] **Step 1: Write failing topology, enum, and privacy tests**

Add `TestFourVMTopologyRequiresThreeIndependentGatewayRootsAndOneFabric`, `TestFourVMPreflightSharedBranchAndBranchIsolation`, `TestFourVMPreflightForkZeroContact`, `TestFourVMPreflightExactCommitAcceptance`, `TestFourVMPreflightOutageRestartQueueDrain`, `TestFourVMPreflightMembershipCredentialRevocationAndLocalContinuation`, `TestIssue56CodeSetExact`, `TestIssue56ValidatorRejectsMissingDuplicateUnknownOrFalseCode`, `TestIssue56ValidatorRequiresExactRCSHAForHostedCI`, `TestTrialExportPrivacyRejectsIdentifiersSourceQueriesAndSecrets`, and `TestPreflightCannotCreateParticipantOrDecisionEvidence`.

- [ ] **Step 2: Run RED**

Run: `go test ./cmd/gatewayd ./internal/runtime/localapi ./cmd/wormhole -run 'Test(FourVM|Issue56|TrialExport|Preflight)' -count=1`

Expected: FAIL because topology checks and exact code enums are absent.

- [ ] **Step 3: Implement automated preflight and strict schema**

The automated topology uses three distinct temporary Gateway databases, workspace roots, sockets, credential roots, actor sessions, and harness labels plus one Fabric/Postgres fixture and deterministic `FakeObserver`. It proves shared canonical branch context, isolation of a second branch, zero-contact fork, accepted ref only after fake commit movement, durable offline local write/restart/drain without duplication, immediate private revocation, continued local-only work, and cross-project/workspace/Fabric rejection. These tests set product readiness fields only and cannot set any of the six real acceptance codes.

The JSON validator accepts the exact six acceptance and twelve prerequisite codes once each, explicit booleans, one exact 40-character release SHA, and no arbitrary observation strings. It rejects repository/project/agent/Passport/Task/KB/channel/event IDs, paths, source, query text, authorization values, credential paths, and unknown codes. `hosted_ci_security_migrations_same_sha_pass` requires recorded hosted Contract Inventory, Build, Static, Integration, Race, Coverage, Migrations, Vulnerability, Secret Scan, and Action Pins conclusions for that same SHA, plus Dependency Review when the release candidate is evaluated through a pull request.

- [ ] **Step 4: Write the exact real topology/runbook boundary**

VM1–VM3 each host one external participant's independent Gateway/harness/credential root; VM4 hosts Fabric and Postgres. All use TLS and the same deployed full RC SHA. Each completed participant runs one matched guidance-off or Code-Graph-off comparison with identical task, checkout, permissions, criteria, and measurement method; exercises two branches, canonical Git acceptance, outage/restart, and configured public/private identity; affirmatively submits a local export; and may withdraw. Raw submissions remain encrypted outside Git. Automated fixtures, operators, invited-but-incomplete users, synthetic exports, and agent-written narratives satisfy none of the six acceptance codes.

- [ ] **Step 5: Run GREEN without evidence artifacts**

Run: `go test ./cmd/gatewayd ./internal/runtime/localapi ./cmd/wormhole -run 'Test(FourVM|Issue56|TrialExport|Preflight)' -count=1`

Expected: PASS and neither `docs/testing/results/git-native-four-vm-gate-d.json` nor `.md` exists.

- [ ] **Step 6: Commit**

```bash
git add docs/operators/git-native-four-vm-trial.md docs/testing/four-vm-acceptance.md docs/testing/closed-trial-metrics.md docs/operators/alpha-validation-trial.md testdata/trial/four-vm/README.md cmd/gatewayd/four_vm_acceptance_test.go internal/runtime/localapi/trial_metrics.go internal/runtime/localapi/trial_metrics_test.go cmd/wormhole/trial_metrics.go cmd/wormhole/trial_metrics_test.go
git commit -m "test: prepare issue 56 four-VM trial"
```

### Task 17: Execute the real four-VM trial and record exactly one Gate D decision

**Files:**
- Create only after real completion: `docs/testing/results/git-native-four-vm-gate-d.json`
- Create only after real completion: `docs/testing/results/git-native-four-vm-gate-d.md`
- Modify only after evidence review: `docs/operators/git-native-four-vm-trial.md`

**Interfaces:**
- Consumes: Task 16 runbook, one merged/deployed RC SHA, real hosted checks for that SHA, three completed external users, and four actual VMs.
- Produces: privacy-reviewed redacted evidence and exactly one issue-56 Gate D decision.

- [ ] **Step 1: Satisfy all twelve prerequisite codes before participant collection**

Run locally on the exact RC SHA:

```bash
make check
make release-test
make release-rehearsal
.github/scripts/check-contract-manifest.sh
git rev-parse HEAD
```

Read back hosted Contract Inventory, Build, Static, Integration, Race, Coverage, Migrations, Vulnerability, Secret Scan, and Action Pins results, plus Dependency Review for a pull-request release candidate, and verify every applicable result names the same 40-character SHA. Confirm Gate C, tooling merge/deploy chronology, local-only export behavior, consent/withdrawal schema, retained negative/incomplete/contrary fields, and coverage ≥80%. If any prerequisite code is false or absent, collect no participant evidence.

- [ ] **Step 2: Complete three external participant runs on VM1–VM3 against VM4**

Each participant must complete the procedure and matched comparison; invitations, operator runs, synthetic records, and incomplete participants do not count. Record successes, denials, failures, coaching, handoff, recovery, KB relevance, source breadth counts, low-value contributions, Event noise, Task accuracy, reconstruction avoided, and token use under the existing privacy schema. Do not replace unsuccessful participants or discard contrary, negative, incomplete, or missing measurements.

- [ ] **Step 3: Validate consent chronology, withdrawal receipts, and redaction**

For each submitted and reviewed redacted file run:

```bash
wormhole trial-metrics validate --kind participant participant-submitted.json
wormhole trial-metrics validate --kind participant participant-redacted.json
```

Raw files stay outside Git. Withdrawn data is deleted within seven days and only an unlinked receipt survives. After at least three qualifying completions, run:

```bash
wormhole trial-metrics format --kind aggregate aggregate-draft.json > aggregate.json
wormhole trial-metrics validate --kind aggregate aggregate.json
```

Expected: `valid`; all six acceptance codes are present once and true, with evidence derived from real reviewed exports.

- [ ] **Step 4: Select exactly one decision**

Both result files select exactly one of:

```text
continue towards beta planning
continue with narrowed scope
repeat alpha after corrective work
stop the current direction
```

Include supporting and contrary evidence plus negative, incomplete, and missing results. A continuation does not authorize beta compatibility and does not establish a full Code Graph V1 claim.

- [ ] **Step 5: Re-run gates over only reviewed redacted artifacts**

Run: `make check && make release-test && make release-rehearsal && git diff --check`

Expected: all PASS, coverage remains at least 80%, and rehearsal publishes nothing.

- [ ] **Step 6: Commit reviewed redacted evidence separately**

```bash
git add docs/testing/results/git-native-four-vm-gate-d.json docs/testing/results/git-native-four-vm-gate-d.md docs/operators/git-native-four-vm-trial.md
git commit -m "docs: record issue 56 Gate D evidence"
```

Issue #56 may close only after review confirms the exact twelve prerequisites, six acceptance codes, three real completions, three matched comparisons, privacy-valid real redacted exports, and one decision. No agent may fabricate participant results, hosted results, consent, evidence codes, or the decision.

---

## Dependency order

```text
Slice A/B/C canonical contracts
  -> 1 -> 2
  -> 3 -> 4 -> 5 -> 6 -> 7
  -> 8 -> 9 -> [human dependency approval 10] -> 11
  -> 12 legacy revocation -> 13 resolver/handler cutover
  -> 14 -> 15 -> 16 -> real external gate 17
```

Tasks 2 and 3 may run in parallel after Task 1. Task 8 may run beside Tasks 3–7, but Task 9 waits for migration 22. Task 11 cannot begin before the exact Task-10 approval. Task 13 cannot run before migration 23 has revoked unbound legacy tokens. Task 17 cannot be parallelized, simulated, or satisfied by fixtures.

## Final verification before participant collection

Run:

```bash
go test ./internal/types/... ./internal/core/identity ./internal/core/git ./internal/core/events ./internal/core/kb ./internal/core/tasks ./internal/mcp ./internal/webui ./internal/runtime/config ./internal/runtime/localstore ./internal/runtime/sync ./internal/runtime/localapi ./cmd/fabric ./cmd/gatewayd ./cmd/wormhole -count=1
WORMHOLE_INTEGRATION_REQUIRED=1 make check
make release-test
make release-rehearsal
```

Expected: all PASS; Postgres 20→23, safe 23→22, empty 22→21, and fresh full up/down pass; SQLite 1→2→3→4→5→6→7, migration-6/7 transactional failure rollback, canonical filename enforcement, and quarantine copy pass without a reverse-migration claim; v1 branch compatibility, v2 preconditions, effective finite activity-retention handshake, exact-commit observation, restart reconstruction, composite-FK direct SQL rejection, actor forgery, atomic audit rollback, revocation, zero-contact fork, OIDC allowlist/SSRF/replay/rotation/throttling, secret redaction, and issue-56 schema tests pass; merged statement coverage is at least 80%.

## Plan self-review

- **Canonical contracts:** the plan consumes `types.RepositoryIdentity`, the exact
  Slice-A `types.WorkspaceBinding`, full `types.ActorEnvelope`, `projectstate.Digest`,
  `DecodeTree`, `EncodeTree`, `Validate`, `DigestTree`, `CanonicalJSON`,
  `DecodeOperation`, `CanonicalOperation`, `DigestCanonicalJSON`,
  `DigestCanonicalMarkdown`, and `ApplyOperation`; it defines no second semantic codec,
  operation parser/canonicalizer, digest rule, or reducer.
- **Durability/security:** portable stream versions may store complete canonical
  live/accepted proposal replicas plus operation ID/canonical bytes/digest, but only
  independently observed Git is acceptance authority. Every read/restart strict-decodes the
  operation, checks decoded ID against the row, checks canonical byte equality and
  `DigestCanonicalJSON`, and rejects malformed, unknown-field, trailing, noncanonical,
  ID-mismatched, or digest-mismatched corruption before serving/replay. The observer reads
  one exact commit; all tenant relationships have direct-SQL composite-FK tests; every
  handler derives scope server-side; mutation and provenance share one transaction.
  ActivityV1 is a separate finite-retention store/queue with an effective-policy handshake
  and cannot enter portable tree reconstruction or advance accepted state.
- **Identity cutover:** first owner is an operator-provisioned one-use OIDC grant, later membership is invitation/admin controlled, PAT scopes are subsets, refresh/device/recovery replay defenses are fixed, and migration 23 revokes legacy authority before resolver cutover and cannot resurrect it on down.
- **Migration ownership:** local schema v7 owns Fabric profile/binding/queue quarantine. Task 14 consumes Slice A's legacy integration-state outcome and performs no second rename, ignore, or repository-state edit.
- **Compatibility/gates:** v1 compatibility covers request/result/error/handler and the v1 descriptor branch; WebAuthn remains absent; issue #56 uses the exact twelve prerequisite and six acceptance codes and requires real non-fabricated four-VM evidence.
