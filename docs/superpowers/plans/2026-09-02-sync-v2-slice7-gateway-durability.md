# Sync v2 Slice 7 Gateway Durability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement selected-human public proof transport, accountable public agent sessions, destructive Gateway schema v9, ProjectState-owned remote-replica convergence, and a routed dependency-injected sync-v2 engine without activating production Gateway wiring.

**Architecture:** Each attempt resolves one persisted route, acquires current proof/session authority, performs strict network work outside SQLite, and submits one complete validated result to ProjectState. ProjectState alone derives local semantic consequences and commits them with the six-field remote replica/cursor/queue evidence, while Git remains sole authority for the local accepted base. The configured engine is test-only until Slice 8.

**Tech Stack:** Go 1.25; standard-library Ed25519, JSON, and HTTP; SQLite through `database/sql`; existing PostgreSQL public-sync handlers; canonical `internal/types/projectstate` codecs; real SQLite/PostgreSQL tests, fault injection, race, vet, architecture tests, and statement coverage.

## Global Constraints

- `CROSS_BRANCH_BASE=0735306a3dacd02a0e197ab56cbfeb90728c7397` is only for the final parallel-ownership audit. `IMPLEMENTATION_BASE` is the committed SHA of this approved plan. Each task review uses its own captured predecessor and GREEN head.
- Preserve all inherited dirty/controller files. Do not stash, reset, clean, rebase, switch branches, apply `stash@{0}`, or stage unrelated edits.
- The only implementation tasks are: (1) signer/common caller, (2) session/strict client/Activity session integration, (3) schema-v9 local durability, (4) ProjectState remote-replica import, (5) routed engine, (6) verification checkpoint.
- Slice 7 adds no PostgreSQL migration. `000022` remains the final Fabric migration and private identity starts at `000023`; no production migration-number constant is added.
- Git is the only local acceptance authority. Fabric state never mutates the checkout or `WorkspaceBinding.AcceptedRef`, `AcceptedCommitSHA`, or `AcceptedTreeDigest`.
- Route selection uses only an exact persisted `WorkspaceScope` and active route. Repository/fork mismatch precedes credential read, signer, DNS, client construction, Git-provider observation, identity-provider activity, and HTTP.
- Values contain credential references only. Never persist, return, log, or interpolate keys, seeds, tokens, proof preimages, or bearer values.
- Rebinding is an explicit Task-7 human CLI action. Consumers receive no Activity, queue, cursor, conflict, receipt, policy, or binding mutator.
- Do not edit Code Graph implementation, private OIDC/authentication, `internal/runtime/localapi/{providers.go,supervisor.go,mcp.go}`, `cmd/gatewayd/gatewayd.go`, or `docs/contracts/alpha-contract.json`.
- No SQLite transaction, writer barrier, ProjectState gate, selected-key lock, or engine-status lock spans DNS/client construction/network I/O.
- Initial install atomically writes binding, complete remote replica/cursor, Activity cursor zero, and enabled/explicitly-disabled policy. Ongoing imports revalidate but never rewrite identity/policy.
- Complete route key: `(project_id, workspace_id, fabric_instance_id, remote_project_id, stream_id, canonical_ref)` everywhere.
- Schema v9 is a destructive epoch: fresh initializes v9, exact v9 reopens, and every other/partial/sidecar database is byte-preserved and refused before mutation. Former v8 exists only as a test fixture.
- `errors.Is` remains valid for all frozen localstore/projectstate/sync sentinels. Merged coverage remains at least 80.0%.

## Frozen Types and Constructors

The following snippets are normative; unexported SQL helper names may change, but types, ownership, causal order, and error mapping may not.

```go
// internal/runtime/localidentity/signer.go
type SelectedHumanSignature struct {
	KeyID     string
	PublicKey [ed25519.PublicKeySize]byte
	Signature []byte
}
type SelectedHumanSigner interface {
	SignSelectedHuman(context.Context, string, []byte) (SelectedHumanSignature, error)
}
type SelectedHumanSource interface {
	Selected(context.Context) (PublicHumanProfile, error)
}

// internal/runtime/sync/public_caller.go
type PublicToolCaller interface {
	Call(context.Context, types.FabricProfile, string, string, string, json.RawMessage) (json.RawMessage, error)
}
type PublicCallerConfig struct {
	Signer           localidentity.SelectedHumanSigner
	Client           *http.Client
	Now              func() time.Time
	Rand             io.Reader
	MaxResponseBytes int64
}
func NewHTTPPublicToolCaller(PublicCallerConfig) (*HTTPPublicToolCaller, error)
func PublicHumanProofScope(attachmentRef string) (string, error)
func PublicAgentProofScope(attachmentRef, sessionID string) (string, error)

// Call scope is attachmentUUID or attachmentUUID+"#"+sessionUUID.
// Call signs only attachmentUUID; the optional suffix becomes proof.SessionID.

// internal/runtime/sync/contract_v2.go
type FabricRouteSource interface {
	GetRoute(context.Context, types.WorkspaceScope) (types.FabricBinding, types.FabricProfile, error)
}
type FabricAttachSource interface {
	GetAttachTarget(context.Context, types.WorkspaceScope) (types.WorkspaceBinding, types.FabricProfile, error)
}
type PublicAgentSessionAuthority interface {
	Acquire(context.Context, types.WorkspaceBinding, types.ActorEnvelope) (projectstate.PublicAgentSessionIssueV2Result, error)
}
type V2CallAuthority struct {
	CredentialRef string
	AgentSession  *projectstate.PublicAgentSessionIssueV2Result
}
type AttachResult struct {
	Wire           projectstate.SyncAttachV2Result
	ActivityPolicy localstore.ActivityPolicyState
}
type BootstrapResult struct {
	Wire           projectstate.SyncBootstrapV2Result
	ActivityPolicy localstore.ActivityPolicyState
}
type PushResult struct {
	Applied  *projectstate.SyncPushAppliedV2Result
	Conflict *projectstate.SyncPushConflictV2Result
}
type V2FabricClient interface {
	Attach(context.Context, V2CallAuthority, projectstate.SyncAttachV2Args) (AttachResult, error)
	IssueAgentSession(context.Context, V2CallAuthority, projectstate.PublicAgentSessionIssueV2Args) (projectstate.PublicAgentSessionIssueV2Result, error)
	Bootstrap(context.Context, V2CallAuthority, projectstate.SyncBootstrapV2Args) (BootstrapResult, error)
	Pull(context.Context, V2CallAuthority, projectstate.SyncPullV2Args) (projectstate.SyncPullV2Result, error)
	Push(context.Context, V2CallAuthority, projectstate.SyncPushV2Args) (PushResult, error)
	ResolveConflict(context.Context, V2CallAuthority, projectstate.SyncConflictV2Args) (projectstate.SyncConflictResolvedV2Result, error)
}
type V2FabricClientFactory interface {
	AttachClient(context.Context, types.WorkspaceBinding, types.FabricProfile) (V2FabricClient, error)
	Client(context.Context, types.FabricBinding, types.FabricProfile) (V2FabricClient, error)
}
func HumanCallAuthority(credentialRef string) (V2CallAuthority, error)
func AgentCallAuthority(credentialRef string, session projectstate.PublicAgentSessionIssueV2Result) (V2CallAuthority, error)
func NewMCPV2FabricClientFactory(PublicToolCaller, func() time.Time) (*MCPV2FabricClientFactory, error)

type RemoteReplicaImporter interface {
	CaptureRemoteAttempt(context.Context, types.WorkspaceScope) (localstore.RemoteAttemptCapture, error)
	ImportAcceptedTree(context.Context, localstore.RemoteReplicaImportRequest) (localstore.RemoteReplicaImportResult, error)
	PrepareRemoteResolution(context.Context, localstore.PrepareRemoteResolutionRequest) (localstore.PrepareRemoteResolutionResult, error)
}

func NewConfiguredV2Engine(
	FabricRouteSource,
	FabricAttachSource,
	V2FabricClientFactory,
	PublicAgentSessionAuthority,
	RemoteReplicaImporter,
	*QueueRepo,
	localstore.WorkspaceConflictGate,
) (*V2Engine, error)
func (e *V2Engine) AttachAndBootstrap(context.Context, types.WorkspaceScope) error
func (e *V2Engine) Pull(context.Context, types.WorkspaceScope) error
func (e *V2Engine) DrainPending(context.Context, types.WorkspaceScope, int) error
func (e *V2Engine) ResolveRemoteConflict(context.Context, types.WorkspaceScope, string, projectstate.OperationV1) error
func (e *V2Engine) Status(context.Context, types.WorkspaceBinding) (Status, error)
```

`V2CallAuthority` is immutable per attempt; constructors copy the agent session. Nil
selects human attachment proof and nonnil selects attachment-plus-session proof.
`AttachClient` binds only a validated local workspace and explicit persisted public
profile, permits only `Attach`, and needs no unknown server IDs. `Client` requires a
complete active route, rejects `Attach`, and permits the other five operations.
`FabricAttachSource` reads an explicit persisted human selection and never chooses by
URL, alias, environment, public input, or last-used state. These seams do not expand a
public sync-v2 wire value.

The localstore-owned values avoid a forbidden `runtime/sync -> runtime/projectstate`
import. ProjectState exports aliases and `*projectstate.Service` satisfies
`RemoteReplicaImporter`; the reverse edge is forbidden.

```go
// internal/runtime/localstore/fabric_sync_state.go
type RemoteImportAction string
const (
	RemoteImportInitial     RemoteImportAction = "initial_install"
	RemoteImportPull        RemoteImportAction = "pull"
	RemoteImportPushDeliver RemoteImportAction = "push_deliver"
	RemoteImportPushRetain  RemoteImportAction = "push_retain_pending"
	RemoteImportResolution  RemoteImportAction = "remote_resolution"
)
type ActivityPolicyState struct {
	State          string // enabled|disabled
	Policy         *projectstate.EffectiveActivityPolicyV1
	PolicyJSON     json.RawMessage
	PolicyVersion  *int64
	PolicyDigest   *projectstate.Digest
	DisabledReason string // empty|missing|malformed|unbounded
	UpdatedAt      time.Time
}
type FabricCursorRecord struct {
	Key                types.RemoteBindingKey
	StreamVersion      int64
	AcceptedCommitSHA  string
	AcceptedTree       projectstate.Tree
	AcceptedTreeDigest projectstate.Digest
	LiveTree           projectstate.Tree
	LiveTreeDigest     projectstate.Digest
	UpdatedAt          time.Time
}
type QueueDisposition string
const (
	QueueUnchanged QueueDisposition = "unchanged"
	QueueDelivered QueueDisposition = "delivered"
)
type QueueConsequence struct {
	OperationID             string
	ExpectedOperationJSON   json.RawMessage
	ExpectedOperationDigest projectstate.Digest
	Disposition             QueueDisposition
}
type RemoteConflictConsequence struct {
	ConflictID                string
	OriginalOperationID       string
	OriginalOperationDigest   projectstate.Digest
	DetectedStreamVersion     int64
	DetectedLiveTreeDigest    projectstate.Digest
	ResolutionOperationID     string
	ResolutionOperationJSON   json.RawMessage
	ResolutionOperationDigest projectstate.Digest
	Resolve                   bool
}
type RemoteReplicaImportRequest struct {
	Scope                      types.WorkspaceScope
	ExpectedWorkspace          WorkspaceRecord
	ExpectedViewDigest         projectstate.Digest
	ExpectedOverlayGeneration  int64
	ExpectedCandidatePresent   bool
	ExpectedCandidateDigest    projectstate.Digest
	ExpectedWorkspaceState     string
	ExpectedOpenConflictIDs    []string
	Route                      types.FabricBinding
	Profile                    types.FabricProfile
	ExpectedRoutePresent       bool
	ExpectedCursor             *FabricCursorRecord
	ExpectedActivityPolicy     *ActivityPolicyState // nil initial; nonnil ongoing
	PriorScope                 projectstate.SyncV2Scope
	RemoteState                projectstate.SyncStateV2
	InitialActivityPolicy      *ActivityPolicyState
	Queue                      *QueueConsequence
	RemoteConflict             *RemoteConflictConsequence
	Action                     RemoteImportAction
	ActionTag                  string
}
type RemoteReplicaImportResult struct {
	WorkspaceState      string
	LocalAcceptedDigest projectstate.Digest
	ComposedViewDigest  projectstate.Digest
	RemoteCursorVersion int64
	SemanticConflictIDs []string
	QueueDisposition    QueueDisposition
	Changed             bool
}
type PrepareRemoteResolutionRequest struct {
	Scope                       types.WorkspaceScope
	ExpectedWorkspace           WorkspaceRecord
	ExpectedViewDigest          projectstate.Digest
	ExpectedOverlayGeneration   int64
	ExpectedCandidatePresent    bool
	ExpectedCandidateDigest     projectstate.Digest
	ExpectedWorkspaceState      string
	ExpectedOpenConflictIDs     []string
	Route                       types.FabricBinding
	Profile                     types.FabricProfile
	ExpectedCursor              FabricCursorRecord
	ExpectedRemoteConflict      RemoteConflictConsequence
	Resolution                  projectstate.OperationV1
	ActionTag                   string
}
type PrepareRemoteResolutionResult struct {
	ConflictID                string
	OriginalOperationID       string
	ResolutionOperationID     string
	ResolutionOperationJSON   json.RawMessage
	ResolutionOperationDigest projectstate.Digest
	AlreadyPrepared           bool
}
type RemoteAttemptCapture struct {
	Workspace           WorkspaceRecord
	ViewDigest          projectstate.Digest
	OverlayGeneration   int64
	CandidatePresent    bool
	CandidateDigest     projectstate.Digest
	WorkspaceState      string
	OpenConflictIDs     []string
	RoutePresent        bool
	Route               types.FabricBinding
	Profile             types.FabricProfile
	Cursor              *FabricCursorRecord
	ActivityPolicy      *ActivityPolicyState
	OpenRemoteConflicts []RemoteConflictRecord
}
type RemoteConflictRecord struct {
	Key                       types.RemoteBindingKey
	ConflictID                string
	OriginalOperationID       string
	OriginalOperationDigest   projectstate.Digest
	DetectedStreamVersion     int64
	DetectedLiveTreeDigest    projectstate.Digest
	State                     string // open|resolved
	ResolutionIntentState     string // none|prepared|resolved
	ResolutionOperationID     string
	ResolutionOperationJSON   json.RawMessage
	ResolutionOperationDigest projectstate.Digest
	CreatedAt                 time.Time
	ResolvedAt                *time.Time
}
```

Enabled Activity policy requires a nonnil typed policy, copied canonical
`PolicyJSON`, matching nonnil version/digest, empty disabled reason, and non-zero UTC
time. Disabled policy requires nil policy/version/digest, empty policy bytes, one closed
reason, and non-zero UTC time. Initial import supplies `InitialActivityPolicy` and no
expected policy; ongoing import supplies captured `ExpectedActivityPolicy` and no
initial policy. Capture and import deep-copy policy and canonical bytes.

```go
// internal/runtime/projectstate/remote_import.go
type RemoteAttemptCapture = localstore.RemoteAttemptCapture
type ImportAcceptedTreeRequest = localstore.RemoteReplicaImportRequest
type ImportAcceptedTreeResult = localstore.RemoteReplicaImportResult
type PrepareRemoteResolutionRequest = localstore.PrepareRemoteResolutionRequest
type PrepareRemoteResolutionResult = localstore.PrepareRemoteResolutionResult
func (s *Service) CaptureRemoteAttempt(context.Context, types.WorkspaceScope) (RemoteAttemptCapture, error)
func (s *Service) ImportAcceptedTree(context.Context, ImportAcceptedTreeRequest) (ImportAcceptedTreeResult, error)
func (s *Service) PrepareRemoteResolution(context.Context, PrepareRemoteResolutionRequest) (PrepareRemoteResolutionResult, error)
```

Localstore exposes only transaction mechanics; it does not decode or merge trees:

```go
func (tx *WorkspaceMutationTx) FabricRoute(context.Context) (types.FabricBinding, types.FabricProfile, bool, error)
func (tx *WorkspaceMutationTx) RemoteReplica(context.Context, types.RemoteBindingKey) (*FabricCursorRecord, error)
func (tx *WorkspaceMutationTx) ActivityPolicy(context.Context, types.RemoteBindingKey) (*ActivityPolicyState, error)
func (tx *WorkspaceMutationTx) ActivityCursor(context.Context, types.RemoteBindingKey) (int64, bool, error)
func (tx *WorkspaceMutationTx) RemoteConflicts(context.Context, types.RemoteBindingKey) ([]RemoteConflictRecord, error)
func (tx *WorkspaceMutationTx) QueueEntry(context.Context, types.RemoteBindingKey, string) (QueueRecord, error)
func (tx *WorkspaceMutationTx) CompareAndSwapRemoteReplica(context.Context, *FabricCursorRecord, FabricCursorRecord) error
func (tx *WorkspaceMutationTx) InstallFabricBinding(context.Context, types.FabricBinding, types.FabricProfile) error
func (tx *WorkspaceMutationTx) InstallActivityPolicy(context.Context, types.RemoteBindingKey, ActivityPolicyState) error
func (tx *WorkspaceMutationTx) ApplyQueueConsequence(context.Context, types.RemoteBindingKey, QueueConsequence) error
func (tx *WorkspaceMutationTx) ApplyRemoteConflict(context.Context, types.RemoteBindingKey, RemoteConflictConsequence) error
type QueueRecord struct {
	Key             types.RemoteBindingKey
	ID              string
	Operation       projectstate.OperationV1
	OperationJSON   json.RawMessage
	OperationDigest projectstate.Digest
	Priority        int
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeliveredAt     *time.Time
}
type QueueStore struct{ db *sql.DB }
func NewQueueStore(*sql.DB) *QueueStore
func (q *QueueStore) Enqueue(context.Context, types.RemoteBindingKey, projectstate.OperationV1, int) (QueueRecord, error)
func (q *QueueStore) ListPending(context.Context, types.RemoteBindingKey, int) ([]QueueRecord, error)
func (q *QueueStore) PendingCount(context.Context, types.RemoteBindingKey) (int, error)
func (q *QueueStore) GetEntry(context.Context, types.RemoteBindingKey, string) (QueueRecord, error)
func (q *QueueStore) DeleteEntry(context.Context, types.RemoteBindingKey, string) error
func (q *QueueStore) MarkDelivered(context.Context, types.RemoteBindingKey, string) error
```

`ApplyQueueConsequence` rechecks exact-workspace open conflicts immediately before its
six-key update. `QueueStore.MarkDelivered` enters
`WorkspaceRepo.WithImmediateWorkspace`, reads through
`WorkspaceMutationTx.QueueEntry`, and calls that same exported
`WorkspaceMutationTx.ApplyQueueConsequence`; all SQL stays private to `localstore`.
`sync.QueueRepo` is a compatibility wrapper over `*localstore.QueueStore` and contains
no SQL.

## Per-Task Commit and Review Protocol

For task `N`, run `TASK_N_BASE=$(git rev-parse HEAD)` before RED. After focused GREEN/race/vet and an exact-path audit, commit the GREEN implementation and set `TASK_N_HEAD=$(git rev-parse HEAD)`. Review `TASK_N_BASE..TASK_N_HEAD`. If C/I repairs are needed, commit them separately, update `TASK_N_HEAD`, rerun all task gates, and re-review the full task range. Proceed only at C0/I0. Controller files under `.superpowers/sdd/` are never force-added.

---

### Task 1: Selected-Human Signer and Common Public Caller

**Files:**
- Create: `internal/runtime/localidentity/signer.go`, `internal/runtime/localidentity/signer_test.go`
- Create: `internal/runtime/sync/public_caller.go`, `internal/runtime/sync/public_caller_test.go`
- Modify: `internal/runtime/sync/contract_v2.go`, `activity_v1.go`, `activity_v1_test.go`, `activity_mcp_client.go`, `activity_mcp_client_test.go`

**Consumes:** existing owner-only local-identity store, `FabricProfile`, `PublicRequestProof`, `PublicProofMessage`, and Activity public client.

**Produces:** the signer and caller types above; public Activity no longer reads secret material. Task 2 later adds agent-session acquisition.

- [ ] **Step 1: Capture the implementation/task base.**

```bash
CROSS_BRANCH_BASE=0735306a3dacd02a0e197ab56cbfeb90728c7397
IMPLEMENTATION_BASE=$(git rev-parse HEAD)
TASK_1_BASE=$IMPLEMENTATION_BASE
test "$(git merge-base "$CROSS_BRANCH_BASE" "$IMPLEMENTATION_BASE")" = "$CROSS_BRANCH_BASE"
printf 'IMPLEMENTATION_BASE=%s\n' "$IMPLEMENTATION_BASE" > .superpowers/sdd/task6-slice7-implementation-base
```

- [ ] **Step 2: Add the complete causal signer RED suite.**

Create `signer_test.go` with imports `bytes`, `context`, `crypto/ed25519`,
`encoding/json`, `errors`, `os`, `path/filepath`, `sync`, `testing`, and
`github.com/H4RL33/wormhole/internal/types`. Reuse the existing package-local
`testJournalID` and `testSelection()` from `store_test.go`. The complete table and
copy/leak assertions are:

```go
func TestSignSelectedHumanExactMessage(t *testing.T) {
    store, err := Open(t.TempDir())
    if err != nil { t.Fatal(err) }
    profile, err := store.EnsureSelectedForSetup(t.Context(),
        "11111111-1111-4111-8111-111111111111",
        types.ConfirmedIdentitySelection{DisplayName: "Alice Example"})
    if err != nil { t.Fatal(err) }
    message := []byte("exact public proof preimage")
    got, err := store.SignSelectedHuman(t.Context(),
        "localidentity:human:"+profile.HumanPrincipalID, message)
    if err != nil { t.Fatal(err) }
    if !ed25519.Verify(got.PublicKey[:], message, got.Signature) {
        t.Fatal("signature does not verify over exact message")
    }
    changed := append([]byte(nil), message...)
    changed[0] ^= 1
    if ed25519.Verify(got.PublicKey[:], changed, got.Signature) {
        t.Fatal("signature verified over changed message")
    }
}

func TestSignSelectedHumanRejectsInvalidAuthority(t *testing.T) {
    store, err := Open(t.TempDir())
    if err != nil { t.Fatal(err) }
    profile, err := store.EnsureSelectedForSetup(t.Context(), testJournalID, testSelection())
    if err != nil { t.Fatal(err) }
    selected := "localidentity:human:" + profile.HumanPrincipalID
    cancelled, cancel := context.WithCancel(t.Context())
    cancel()
    tests := []struct {
        name string
        ctx context.Context
        ref string
        message []byte
        want error
    }{
        {"empty ref", t.Context(), "", []byte("m"), ErrInvalidStoreRecord},
        {"malformed ref", t.Context(), "localidentity:human:not-a-uuid", []byte("m"), ErrInvalidStoreRecord},
        {"wrong selected human", t.Context(), "localidentity:human:22222222-2222-4222-8222-222222222222", []byte("m"), ErrNoSelectedIdentity},
        {"empty message", t.Context(), selected, nil, ErrInvalidStoreRecord},
        {"cancelled", cancelled, selected, []byte("m"), context.Canceled},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := store.SignSelectedHuman(tt.ctx, tt.ref, tt.message)
            if !errors.Is(err, tt.want) { t.Fatalf("error = %v, want %v", err, tt.want) }
            if got != (SelectedHumanSignature{}) { t.Fatalf("result = %#v, want zero", got) }
        })
    }
}

func TestSignSelectedHumanCopiesInputsAndOutputsWithoutPrivateBytes(t *testing.T) {
    root := t.TempDir()
    store, err := Open(root)
    if err != nil { t.Fatal(err) }
    profile, err := store.EnsureSelectedForSetup(t.Context(), testJournalID, testSelection())
    if err != nil { t.Fatal(err) }
    keyBytes, err := os.ReadFile(filepath.Join(root, privateKeyRecordName(profile.HumanPrincipalID)))
    if err != nil { t.Fatal(err) }
    message := []byte("copy me")
    original := bytes.Clone(message)
    got, err := store.SignSelectedHuman(t.Context(), "localidentity:human:"+profile.HumanPrincipalID, message)
    if err != nil { t.Fatal(err) }
    message[0] ^= 1
    if !ed25519.Verify(got.PublicKey[:], original, got.Signature) { t.Fatal("input was retained instead of copied") }
    first := bytes.Clone(got.Signature)
    got.Signature[0] ^= 1
    again, err := store.SignSelectedHuman(t.Context(), "localidentity:human:"+profile.HumanPrincipalID, original)
    if err != nil { t.Fatal(err) }
    if !bytes.Equal(first, again.Signature) { t.Fatal("returned signature aliases store memory") }
    encoded, err := json.Marshal(again)
    if err != nil { t.Fatal(err) }
    if bytes.Contains(encoded, bytes.TrimSpace(keyBytes)) { t.Fatal("private key leaked in result") }
}

func TestSignSelectedHumanConcurrentSelectionIsSerialized(t *testing.T) {
    store, err := Open(t.TempDir())
    if err != nil { t.Fatal(err) }
    profile, err := store.EnsureSelectedForSetup(t.Context(), testJournalID, testSelection())
    if err != nil { t.Fatal(err) }
    fd, err := store.openRoot()
    if err != nil { t.Fatal(err) }
    unlock, err := lockLocalIdentityStore(t.Context(), fd)
    if err != nil { t.Fatal(err) }
    entered := make(chan struct{})
    done := make(chan error, 1)
    go func() {
        close(entered)
        _, e := store.SignSelectedHuman(t.Context(), "localidentity:human:"+profile.HumanPrincipalID, []byte("m"))
        done <- e
    }()
    <-entered
    select { case err := <-done: t.Fatalf("sign completed through held selection lock: %v", err); default: }
    unlock()
    if err := closeLocalIdentityFD(fd); err != nil { t.Fatal(err) }
    if err := <-done; err != nil { t.Fatal(err) }
}
```

```bash
go test ./internal/runtime/localidentity -run '^TestSignSelectedHuman' -count=1
```

Expected: compile failure because signer types/method are absent.

- [ ] **Step 3: Implement the signer exactly.**

Add the frozen signer/source types, then implement with the existing no-follow root, process lock, record readers, and key parser:

```go
func (s *Store) SignSelectedHuman(ctx context.Context, ref string, message []byte) (SelectedHumanSignature, error) {
    if err := ctx.Err(); err != nil { return SelectedHumanSignature{}, err }
    const prefix = "localidentity:human:"
    humanID := strings.TrimPrefix(ref, prefix)
    if ref != prefix+humanID || !types.CanonicalUUID(humanID) || len(message) == 0 {
        return SelectedHumanSignature{}, ErrInvalidStoreRecord
    }
    preimage := append([]byte(nil), message...)
    fd, err := s.openRoot()
    if err != nil { return SelectedHumanSignature{}, err }
    defer closeLocalIdentityFD(fd)
    unlock, err := lockLocalIdentityStore(ctx, fd)
    if err != nil { return SelectedHumanSignature{}, err }
    defer unlock()
    profile, err := selectedProfileLocked(fd)
    if err != nil || profile.HumanPrincipalID != humanID {
        return SelectedHumanSignature{}, ErrNoSelectedIdentity
    }
    encoded, exists, err := readLocalIdentityFile(fd, privateKeyRecordName(humanID))
    if err != nil || !exists { return SelectedHumanSignature{}, ErrInvalidStoreRecord }
    privateKey, err := parsePrivateKey(encoded)
    if err != nil { return SelectedHumanSignature{}, ErrInvalidStoreRecord }
    publicKey := privateKey.Public().(ed25519.PublicKey)
    if !bytes.Equal(publicKey, profile.PublicKey) { return SelectedHumanSignature{}, ErrInvalidStoreRecord }
    fingerprint := sha256.Sum256(publicKey)
    var copiedPublic [ed25519.PublicKeySize]byte
    copy(copiedPublic[:], publicKey)
    return SelectedHumanSignature{
        KeyID: "sha256:"+hex.EncodeToString(fingerprint[:]), PublicKey: copiedPublic,
        Signature: append([]byte(nil), ed25519.Sign(privateKey, preimage)...),
    }, nil
}
```

`SelectedHumanSource` is exactly `Selected(ctx context.Context) (PublicHumanProfile, error)`; the existing `*localidentity.Store.Selected` method satisfies it without a new adapter.

```bash
go test ./internal/runtime/localidentity -run '^TestSignSelectedHuman' -count=1
go test -race ./internal/runtime/localidentity -run '^TestSignSelectedHumanConcurrent' -count=1
```

- [ ] **Step 4: Add the complete strict caller RED tables.**

Use a fixed clock, exact 32-byte reader, recording signer, and `roundTripFunc`. The first RED test must construct the concrete caller and inspect the request captured by the transport:

```go
func TestPublicToolCallerSignsCanonicalArguments(t *testing.T) {
    transport := &recordingRoundTripper{response: jsonResponse(200,
        `{"jsonrpc":"2.0","id":"00000000-0000-4000-8000-000000000001","result":{"content":[{"type":"text","text":"{\"version\":2}"}]}}`)}
    signer := &recordingSelectedSigner{result: validSelectedSignature(t)}
    caller, err := NewHTTPPublicToolCaller(PublicCallerConfig{
        Signer: signer, Client: &http.Client{Transport: transport},
        Now: func() time.Time { return time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC) },
        Rand: bytes.NewReader(bytes.Repeat([]byte{0x2a}, 32)), MaxResponseBytes: 1 << 20,
    })
    if err != nil { t.Fatal(err) }
    _, err = caller.Call(t.Context(), publicProfileFixture(),
        "localidentity:human:11111111-1111-4111-8111-111111111111",
        "wormhole.sync.pull", "22222222-2222-4222-8222-222222222222",
        json.RawMessage(`{ "version" : 2 }`))
    if err != nil { t.Fatal(err) }
    if !bytes.Equal(signer.message, transport.proofMessage) {
        t.Fatal("signed preimage differs from transported proof")
    }
}
```

Add these exact table rows beneath the representative test. `callFixture` constructs a
fresh caller using the same fixed clock/reader as the representative test and returns
the signer and transport; `validResult(id,text,isError)` returns the closed one-text
JSON-RPC result. Both helpers are defined in this test file, not production:

```go
func TestPublicProofScopeGrammar(t *testing.T) {
    tests := []struct{ name, attachment, session, want string; wantErr bool }{
        {"human", "22222222-2222-4222-8222-222222222222", "", "22222222-2222-4222-8222-222222222222", false},
        {"agent", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333", "22222222-2222-4222-8222-222222222222#33333333-3333-4333-8333-333333333333", false},
        {"bad attachment", "display-name", "", "", true},
        {"bad session", "22222222-2222-4222-8222-222222222222", "last-used", "", true},
    }
    for _, tt := range tests { t.Run(tt.name, func(t *testing.T) {
        got, err := publicProofScope(tt.attachment, tt.session)
        if (err != nil) != tt.wantErr || got != tt.want { t.Fatalf("scope=%q error=%v", got, err) }
    }) }
}

func TestPublicToolCallerRejectsBeforeSigner(t *testing.T) {
    tests := []struct{ name string; mutate func(*types.FabricProfile,*string,*string,*string) }{
        {"private mode", func(p *types.FabricProfile, _,_,_ *string){ p.Mode=types.FabricModePrivate }},
        {"http URL", func(p *types.FabricProfile, _,_,_ *string){ p.BaseURL="http://fabric.example" }},
        {"wrong Fabric", func(p *types.FabricProfile, _,_,_ *string){ p.FabricInstanceID="display" }},
        {"raw credential", func(_ *types.FabricProfile,c,_,_ *string){ *c="secret-token" }},
        {"unknown tool", func(_ *types.FabricProfile,_,tool,_ *string){ *tool="wormhole.unknown" }},
        {"bad scope", func(_ *types.FabricProfile,_,_,scope *string){ *scope="last-profile" }},
    }
    for _, tt := range tests { t.Run(tt.name, func(t *testing.T) {
        caller, signer, _ := callFixture(t, validResult(rpcIDForByte(0x2a), `{"version":2}`, false))
        profile, credential, tool, scope := publicProfileFixture(), "localidentity:human:11111111-1111-4111-8111-111111111111", "wormhole.sync.pull", "22222222-2222-4222-8222-222222222222"
        tt.mutate(&profile,&credential,&tool,&scope)
        if _, err := caller.Call(t.Context(), profile, credential, tool, scope, json.RawMessage(`{"version":2}`)); !errors.Is(err, ErrAttentionRequired) { t.Fatalf("error=%v",err) }
        if signer.calls != 0 { t.Fatalf("signer calls=%d",signer.calls) }
    }) }
}

func TestPublicToolCallerRejectsClosedResponseMatrix(t *testing.T) {
    id := rpcIDForByte(0x2a)
    tests := []struct{ name string; status int; contentType, body string }{
        {"redirect", 302, "application/json", validResult(id, `{"version":2}`, false)},
        {"wrong content type", 200, "text/plain", `{}`},
        {"oversize", 200, "application/json", strings.Repeat("x", (1<<20)+1)},
        {"duplicate", 200, "application/json", `{"jsonrpc":"2.0","jsonrpc":"2.0","id":"`+id+`","result":{"content":[]}}`},
        {"trailing", 200, "application/json", validResult(id, `{"version":2}`, false)+` {}`},
        {"unknown wrapper", 200, "application/json", `{"jsonrpc":"2.0","id":"`+id+`","result":{"content":[],"extra":1}}`},
        {"two content", 200, "application/json", `{"jsonrpc":"2.0","id":"`+id+`","result":{"content":[{"type":"text","text":"{}"},{"type":"text","text":"{}"}]}}`},
        {"non-text", 200, "application/json", `{"jsonrpc":"2.0","id":"`+id+`","result":{"content":[{"type":"image","text":"{}"}]}}`},
    }
    for _, tt := range tests { t.Run(tt.name, func(t *testing.T) {
        caller, _, transport := callFixture(t, tt.body)
        transport.status, transport.contentType = tt.status, tt.contentType
        _, err := caller.Call(t.Context(), publicProfileFixture(), "localidentity:human:11111111-1111-4111-8111-111111111111", "wormhole.sync.pull", "22222222-2222-4222-8222-222222222222", json.RawMessage(`{"version":2}`))
        if err == nil || strings.Contains(err.Error(), "secret") { t.Fatalf("unsafe error=%v",err) }
    }) }
}

func TestPublicToolCallerRetryKeepsArgumentsAndRefreshesProof(t *testing.T) {
    clock := &sequenceClock{values: []time.Time{time.Date(2026,9,2,12,0,0,0,time.UTC), time.Date(2026,9,2,12,0,1,0,time.UTC)}}
    random := bytes.NewReader(append(bytes.Repeat([]byte{1},32),bytes.Repeat([]byte{2},32)...))
    transport := &recordingRoundTripper{status:200,contentType:"application/json",dynamicSuccess:true}
    signer := &recordingSelectedSigner{result:validSelectedSignature(t)}
    caller, err := NewHTTPPublicToolCaller(PublicCallerConfig{Signer:signer,Client:&http.Client{Transport:transport},Now:clock.Next,Rand:random,MaxResponseBytes:1<<20})
    if err != nil { t.Fatal(err) }
    args:=json.RawMessage(`{"operation":{"id":"44444444-4444-4444-8444-444444444444"},"version":2}`)
    for range 2 { if _,err:=caller.Call(t.Context(),publicProfileFixture(),"localidentity:human:11111111-1111-4111-8111-111111111111","wormhole.sync.push","22222222-2222-4222-8222-222222222222",args); err!=nil { t.Fatal(err) } }
    if !bytes.Equal(transport.calls[0].Arguments,transport.calls[1].Arguments) { t.Fatal("arguments changed") }
    if transport.calls[0].ID==transport.calls[1].ID || transport.calls[0].Proof.Nonce==transport.calls[1].Proof.Nonce || transport.calls[0].Proof.Timestamp==transport.calls[1].Proof.Timestamp { t.Fatal("retry proof was reused") }
}
```

Define `recordingRoundTripper`, `recordingSelectedSigner`, `sequenceClock`,
`rpcIDForByte`, `validResult`, `callFixture`, `jsonResponse`,
`validSelectedSignature`, and `publicProfileFixture` as ordinary test structs/functions;
their fields are exactly those referenced above. The existing
`internal/mcp/public_proof_test.go` `signedProof` fixture is the behavioral model; no
production hook is added.

```bash
go test ./internal/runtime/sync -run '^Test(PublicToolCaller|PublicProofScope)' -count=1
```

Expected: compile failure for caller/config/scope helpers.

- [ ] **Step 5: Implement the concrete caller and strict envelope.**

The constructor rejects nil dependencies, non-positive or over-1MiB limits, and any supplied non-nil `CheckRedirect` policy because function behavior cannot be safely inspected. It clones the client and installs an unconditional `http.ErrUseLastResponse` redirect policy:

```go
type HTTPPublicToolCaller struct {
    signer localidentity.SelectedHumanSigner
    client *http.Client
    now func() time.Time
    random io.Reader
    maxResponseBytes int64
}

func NewHTTPPublicToolCaller(cfg PublicCallerConfig) (*HTTPPublicToolCaller, error) {
    if nilInterface(cfg.Signer) || cfg.Client == nil || cfg.Now == nil || cfg.Rand == nil ||
        cfg.MaxResponseBytes <= 0 || cfg.MaxResponseBytes > 1<<20 || cfg.Client.CheckRedirect != nil {
        return nil, ErrAttentionRequired
    }
    client := *cfg.Client
    client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
    return &HTTPPublicToolCaller{signer: cfg.Signer, client: &client, now: cfg.Now,
        random: cfg.Rand, maxResponseBytes: cfg.MaxResponseBytes}, nil
}

func (c *HTTPPublicToolCaller) Call(ctx context.Context, profile types.FabricProfile,
    credentialRef, tool, scope string, arguments json.RawMessage) (json.RawMessage, error) {
    attachmentRef, sessionID, err := validatePublicCall(profile, credentialRef, tool, scope)
    if err != nil { return nil, err }
    canonicalArgs, err := canonicalPublicArguments(arguments)
    if err != nil { return nil, err }
    now := c.now()
    if now.IsZero() || now.Location() != time.UTC { return nil, ErrAttentionRequired }
    var nonce [32]byte
    if _, err := io.ReadFull(c.random, nonce[:]); err != nil { return nil, ErrFabricUnavailable }
    message, err := projectstate.PublicProofMessage(profile.FabricInstanceID, tool,
        attachmentRef, canonicalArgs, now, nonce)
    if err != nil { return nil, ErrAttentionRequired }
    signed, err := c.signer.SignSelectedHuman(ctx, credentialRef, message)
    if err != nil || validateSelectedHumanSignature(signed, message) != nil {
        return nil, ErrAttentionRequired
    }
    body, requestID, err := encodePublicToolCall(tool, canonicalArgs, sessionID, now, nonce, signed)
    if err != nil { return nil, ErrAttentionRequired }
    req, err := http.NewRequestWithContext(ctx, http.MethodPost, profile.BaseURL, bytes.NewReader(body))
    if err != nil { return nil, ErrFabricUnavailable }
    req.Header.Set("Content-Type", "application/json")
    response, err := c.client.Do(req)
    if err != nil { return nil, ErrFabricUnavailable }
    defer response.Body.Close()
    if response.StatusCode < 200 || response.StatusCode > 299 ||
        response.Header.Get("Content-Type") != "application/json" {
        return nil, ErrFabricUnavailable
    }
    bounded, err := readBounded(response.Body, c.maxResponseBytes)
    if err != nil { return nil, ErrFabricUnavailable }
    result, err := decodePublicToolResult(bounded, requestID)
    if err != nil { return nil, err }
    return append(json.RawMessage(nil), result...), nil
}
```

Paste these complete helpers below `Call`; imports additionally include `encoding/base64`,
`errors`, `fmt`, `net/url`, `reflect`, `strings`, and
`github.com/google/uuid`. `rejectDuplicateJSONMembers` is moved unchanged from
`activity_mcp_client.go` into `public_caller.go` so both callers use it:

```go
func nilInterface(v any) bool { if v==nil{return true}; rv:=reflect.ValueOf(v); return rv.Kind()==reflect.Ptr && rv.IsNil() }
func publicProofScope(attachmentRef, sessionID string) (string,error) {
    if !types.CanonicalUUID(attachmentRef) || (sessionID!="" && !types.CanonicalUUID(sessionID)) { return "",ErrAttentionRequired }
    scope:=attachmentRef
    if sessionID!="" { scope+="#"+sessionID }
    return scope,nil
}
func PublicHumanProofScope(attachmentRef string) (string,error) { return publicProofScope(attachmentRef,"") }
func PublicAgentProofScope(attachmentRef,sessionID string) (string,error) { return publicProofScope(attachmentRef,sessionID) }
func validatePublicCall(profile types.FabricProfile, credentialRef, tool, scope string) (string,string,error) {
    if profile.Validate()!=nil || profile.Mode!=types.FabricModePublic || !strings.HasPrefix(credentialRef,"localidentity:human:") || strings.TrimPrefix(credentialRef,"localidentity:human:")==credentialRef || !types.CanonicalUUID(strings.TrimPrefix(credentialRef,"localidentity:human:")) { return "","",ErrAttentionRequired }
    parsed,err:=url.Parse(profile.BaseURL); if err!=nil || parsed.Scheme!="https" || parsed.Host=="" || parsed.User!=nil || parsed.Fragment!="" { return "","",ErrAttentionRequired }
    allowed:=map[string]bool{"wormhole.sync.attach":true,"wormhole.sync.issue_agent_session":true,"wormhole.sync.bootstrap":true,"wormhole.sync.pull":true,"wormhole.sync.push":true,"wormhole.sync.conflict":true,"wormhole.activity.accept":true,"wormhole.activity.presence":true,"wormhole.activity.pull":true,"wormhole.activity.lifecycle":true}
    if !allowed[tool] { return "","",ErrAttentionRequired }
    if strings.HasPrefix(scope,"repository:sha256:") { if tool!="wormhole.sync.attach" || len(scope)!=82 { return "","",ErrAttentionRequired }; return scope,"",nil }
    parts:=strings.Split(scope,"#")
    if len(parts)>2 || !types.CanonicalUUID(parts[0]) { return "","",ErrAttentionRequired }
    if len(parts)==2 && !types.CanonicalUUID(parts[1]) { return "","",ErrAttentionRequired }
    session:=""; proofScope:="attachment:"+parts[0]
    if len(parts)==2 { session=parts[1]; proofScope+=":session:"+session }
    return proofScope,session,nil
}
func canonicalPublicArguments(raw json.RawMessage) (json.RawMessage,error) {
    if rejectDuplicateJSONMembers(raw)!=nil { return nil,ErrAttentionRequired }
    var value any; dec:=json.NewDecoder(bytes.NewReader(raw)); dec.UseNumber()
    if err:=dec.Decode(&value); err!=nil { return nil,ErrAttentionRequired }
    if _,ok:=value.(map[string]any); !ok { return nil,ErrAttentionRequired }
    var tail any; if err:=dec.Decode(&tail); !errors.Is(err,io.EOF) { return nil,ErrAttentionRequired }
    out,err:=projectstate.CanonicalJSONObject(value); if err!=nil { return nil,ErrAttentionRequired }
    return json.RawMessage(bytes.Clone(out)),nil
}
func validateSelectedHumanSignature(s localidentity.SelectedHumanSignature,message []byte) error {
    sum:=sha256.Sum256(s.PublicKey[:])
    if s.KeyID!="sha256:"+hex.EncodeToString(sum[:]) || len(s.Signature)!=ed25519.SignatureSize || !ed25519.Verify(s.PublicKey[:],message,s.Signature) { return ErrAttentionRequired }
    return nil
}
type publicToolCall struct { JSONRPC string `json:"jsonrpc"`; ID string `json:"id"`; Method string `json:"method"`; Params publicToolParams `json:"params"` }
type publicToolParams struct { Name string `json:"name"`; Arguments json.RawMessage `json:"arguments"`; Proof types.PublicRequestProof `json:"proof"` }
func encodePublicToolCall(tool string,args json.RawMessage,sessionID string,at time.Time,nonce [32]byte,s localidentity.SelectedHumanSignature)([]byte,string,error){
    id:=uuid.NewSHA1(uuid.Nil,append(bytes.Clone(nonce[:]),[]byte(at.Format(time.RFC3339Nano))...)).String()
    call:=publicToolCall{JSONRPC:"2.0",ID:id,Method:"tools/call",Params:publicToolParams{Name:tool,Arguments:bytes.Clone(args),Proof:types.PublicRequestProof{KeyID:s.KeyID,PublicKey:base64.RawURLEncoding.EncodeToString(s.PublicKey[:]),Timestamp:at.Format(time.RFC3339Nano),Nonce:base64.RawURLEncoding.EncodeToString(nonce[:]),Signature:base64.RawURLEncoding.EncodeToString(s.Signature),SessionID:sessionID}}}
    body,err:=json.Marshal(call); if err!=nil{return nil,"",err}; return body,id,nil
}
func readBounded(r io.Reader,limit int64)([]byte,error){
    lr:=io.LimitReader(r,limit+1); body,err:=io.ReadAll(lr); if err!=nil||int64(len(body))>limit{return nil,ErrFabricUnavailable}; return body,nil
}
type publicRPCResponse struct { JSONRPC string `json:"jsonrpc"`; ID string `json:"id"`; Result *publicToolResult `json:"result,omitempty"`; Error *json.RawMessage `json:"error,omitempty"` }
type publicToolResult struct { Content []publicToolContent `json:"content"`; IsError bool `json:"isError,omitempty"` }
type publicToolContent struct { Type string `json:"type"`; Text string `json:"text"` }
type publicFailure struct { Code string `json:"code"`; Operation string `json:"operation"` }
func decodePublicToolResult(body []byte,requestID string)(json.RawMessage,error){
    if rejectDuplicateJSONMembers(body)!=nil{return nil,ErrAttentionRequired}
    dec:=json.NewDecoder(bytes.NewReader(body)); dec.DisallowUnknownFields(); var response publicRPCResponse
    if dec.Decode(&response)!=nil{return nil,ErrAttentionRequired}; var tail any; if !errors.Is(dec.Decode(&tail),io.EOF){return nil,ErrAttentionRequired}
    if response.JSONRPC!="2.0"||response.ID!=requestID||(response.Result==nil)==(response.Error==nil){return nil,ErrAttentionRequired}
    if response.Error!=nil{return nil,ErrFabricUnavailable}
    result:=response.Result; if len(result.Content)!=1||result.Content[0].Type!="text"||!json.Valid([]byte(result.Content[0].Text)){return nil,ErrAttentionRequired}
    raw:=json.RawMessage(result.Content[0].Text)
    if result.IsError { var failure publicFailure; d:=json.NewDecoder(bytes.NewReader(raw)); d.DisallowUnknownFields(); if d.Decode(&failure)!=nil{return nil,ErrAttentionRequired}; allowed:=map[string]bool{"invalid_request":true,"unknown_version":true,"authentication_failed":true,"permission_denied":true,"attachment_not_found":true,"sync_precondition_failed":true,"sync_conflict":true,"sync_replay_conflict":true,"sync_observer_unavailable":true,"internal_error":true,"invalid_activity":true,"unknown_activity_version":true,"activity_policy_required":true,"activity_policy_changed":true,"activity_not_found":true,"activity_replay_conflict":true,"activity_cursor_invalid":true,"activity_lifecycle_conflict":true}; if !allowed[failure.Code]||failure.Operation==""{return nil,ErrAttentionRequired}; return nil,fmt.Errorf("%w: %s",ErrAttentionRequired,failure.Code) }
    return bytes.Clone(raw),nil
}
```

- [ ] **Step 6: Convert public Activity to the common caller.**

Make the old Activity caller interface alias-compatible with `PublicToolCaller`. Branch before secret access:

```go
credential := profile.CredentialRef
if profile.Mode == types.FabricModePrivate {
    credential, err = s.credentials.Read(ctx, profile.CredentialRef)
    if err != nil { return activityNetworkCycle{}, classifyActivityError("credential", err, ErrFabricUnavailable) }
}
```

Task 1 uses human attachment scope for public Activity; Task 2 replaces it for agent-authored records. Add `TestActivityPublicModeNeverReadsCredentialSource` and retain private credential tests.

- [ ] **Step 7: Verify and commit GREEN before review.**

```bash
go test ./internal/runtime/localidentity ./internal/runtime/sync -run 'Test(SignSelectedHuman|PublicToolCaller|PublicProofScope|Activity)' -count=1
go test -race ./internal/runtime/localidentity ./internal/runtime/sync -run 'Test(SignSelectedHuman|PublicToolCaller|Activity)' -count=1
go vet ./internal/runtime/localidentity ./internal/runtime/sync
git status --short
git add internal/runtime/localidentity/signer.go internal/runtime/localidentity/signer_test.go \
  internal/runtime/sync/public_caller.go internal/runtime/sync/public_caller_test.go \
  internal/runtime/sync/contract_v2.go internal/runtime/sync/activity_v1.go \
  internal/runtime/sync/activity_v1_test.go internal/runtime/sync/activity_mcp_client.go \
  internal/runtime/sync/activity_mcp_client_test.go
test -z "$(git diff --cached --name-only | grep -Ev '^(internal/runtime/localidentity/(signer.go|signer_test.go)|internal/runtime/sync/(public_caller.go|public_caller_test.go|contract_v2.go|activity_v1.go|activity_v1_test.go|activity_mcp_client.go|activity_mcp_client_test.go))$')"
git commit -m "feat: add strict public sync caller"
TASK_1_HEAD=$(git rev-parse HEAD)
```

- [ ] **Step 8: Review `TASK_1_BASE..TASK_1_HEAD`; repair in a later commit only.**

Require C0/I0 against design §§4–6. If repairs are required, use the same exact staging allowlist, commit `fix: address Slice 7 caller review`, rerun Step 7 gates without recommitting, update the head, and re-review.

### Task 2: Accountable Session, Strict Portable Client, and Activity Session Integration

**Files:**
- Create: `internal/mcp/sync_v2_session.go`, `internal/mcp/sync_v2_session_test.go`
- Modify: `internal/mcp/public_auth.go`, `public_auth_test.go`, `sync_v2.go`, `sync_v2_contract_test.go`
- Modify: `internal/core/identity/public_sync.go`, `public_sync_test.go`
- Create: `internal/runtime/sync/client_v2.go`, `client_v2_test.go`, `public_session.go`, `public_session_test.go`
- Modify: `internal/runtime/sync/contract_v2.go`, `activity_v1.go`, `activity_v1_test.go`, `activity_mcp_client.go`, `activity_mcp_client_test.go`, `activity_lifecycle_transport.go`, `activity_lifecycle_transport_test.go`
- Modify: `internal/runtime/localstore/activity_records.go`, `activity_records_test.go` only to add the exact-record read needed to authenticate lifecycle attempts from persisted actor evidence.

**Consumes:** Task 1 `PublicToolCaller`, selected-human signer/source, and exact persisted routes.

**Produces:** one attachment-only transaction resolver; an unregistered session handler; a
strict route-bound client with explicit immutable per-call authority; one shared session cache;
and Activity calls authenticated from persisted actor evidence.

- [ ] **Step 1: Capture the task base and add the complete resolver/handler RED tests.**

```bash
TASK_2_BASE=$(git rev-parse HEAD)
```

Create `internal/mcp/sync_v2_session_test.go`. Reuse the existing real-PostgreSQL
`mutationFixture`, `realBoundResolverForDB`, `signedBoundProof`, and snapshot helpers. The test
body below is complete; the two fixture helpers at its end are part of the same file.

```go
package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	coregit "github.com/H4RL33/wormhole/internal/core/git"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestSyncV2IssueAgentSessionCommitsOneNonceSessionAndAudit(t *testing.T) {
	f := newMutationFixture(t)
	attached := f.attach(71)
	agentID := addAcceptedSessionAgent(t, f, attached, "70000000-0000-4000-8000-000000000071")
	h := newSessionHandlerForFixture(t, f)
	args := projectstate.PublicAgentSessionIssueV2Args{Version: 2, AttachmentRef: attached.Attachment.AttachmentRef,
		AgentID: agentID, HarnessName: "codex", HarnessVersion: "1", ModelName: "gpt", ModelVersion: "5"}
	raw := canonicalMutationJSON(t, mustJSON(t, args))
	seed := sha256.Sum256([]byte(f.projectID))
	proof := signedBoundProof(t, f.fabricID, "wormhole.sync.issue_agent_session", raw,
		args.AttachmentRef, f.transport.OccurredAt, bytes.Repeat([]byte{71}, 32), seed[:])
	before := sessionEvidenceCounts(t, f)
	got, err := h.Handle(context.Background(), raw, proof)
	if err != nil { t.Fatal(err) }
	if got.Version != 2 || !types.CanonicalUUID(got.SessionID) || got.AgentID != agentID ||
		got.AccountableHumanID != f.transport.HumanPrincipalID || got.HarnessName != args.HarnessName ||
		got.HarnessVersion != args.HarnessVersion || got.ModelName != args.ModelName ||
		got.ModelVersion != args.ModelVersion || got.Assurance != types.AssurancePublicKeyContinuity ||
		!got.ExpiresAt.Equal(f.transport.OccurredAt.Add(24*time.Hour)) {
		t.Fatalf("session=%+v", got)
	}
	assertSessionEvidenceDelta(t, before, sessionEvidenceCounts(t, f), 1, 1, 1)
	assertLastSessionAudit(t, f, agentID, args.AttachmentRef, "issued")
	if _, live := NewPublicRegistry(PublicRegistryDependencies{}).Get("wormhole.sync.issue_agent_session"); live {
		t.Fatal("Slice 7 registered issue_agent_session")
	}
}

func TestSyncV2IssueAgentSessionDeniedValidProofBurnsNonceAndAudits(t *testing.T) {
	for name, mutate := range map[string]func(*mutationFixture, *InitialAttachResult, *projectstate.PublicAgentSessionIssueV2Args){
		"missing agent": func(_ *mutationFixture, _ *InitialAttachResult, a *projectstate.PublicAgentSessionIssueV2Args) { a.AgentID = "70000000-0000-4000-8000-000000000072" },
		"removed issuer": func(f *mutationFixture, attached *InitialAttachResult, _ *projectstate.PublicAgentSessionIssueV2Args) { removeAcceptedActor(t, f, *attached, f.transport.HumanPrincipalID) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newMutationFixture(t); attached := f.attach(72)
			agentID := addAcceptedSessionAgent(t, f, attached, "70000000-0000-4000-8000-000000000073")
			args := projectstate.PublicAgentSessionIssueV2Args{Version: 2, AttachmentRef: attached.Attachment.AttachmentRef,
				AgentID: agentID, HarnessName: "codex", HarnessVersion: "1"}
			mutate(f, &attached, &args)
			raw := canonicalMutationJSON(t, mustJSON(t, args)); seed := sha256.Sum256([]byte(f.projectID))
			proof := signedBoundProof(t, f.fabricID, "wormhole.sync.issue_agent_session", raw,
				args.AttachmentRef, f.transport.OccurredAt, bytes.Repeat([]byte{72}, 32), seed[:])
			before := sessionEvidenceCounts(t, f)
			_, err := newSessionHandlerForFixture(t, f).Handle(context.Background(), raw, proof)
			if err == nil || (err.Error() != `{"code":"permission_denied","operation":"wormhole.sync.issue_agent_session"}` &&
				err.Error() != `{"code":"authentication_failed","operation":"wormhole.sync.issue_agent_session"}`) { t.Fatalf("error=%v", err) }
			assertSessionEvidenceDelta(t, before, sessionEvidenceCounts(t, f), 1, 0, 1)
			assertLastSessionAudit(t, f, args.AgentID, args.AttachmentRef, "denied")
		})
	}
}

func TestSyncV2IssueAgentSessionReplayConflictExpiryAndRollback(t *testing.T) {
	f := newMutationFixture(t); attached := f.attach(73)
	agentID := addAcceptedSessionAgent(t, f, attached, "70000000-0000-4000-8000-000000000074")
	now := f.transport.OccurredAt
	h := newSessionHandlerForFixtureAt(t, f, &now)
	args := projectstate.PublicAgentSessionIssueV2Args{Version: 2, AttachmentRef: attached.Attachment.AttachmentRef,
		AgentID: agentID, HarnessName: "codex", HarnessVersion: "1"}
	call := func(nonce byte, value projectstate.PublicAgentSessionIssueV2Args) (projectstate.PublicAgentSessionIssueV2Result, error) {
		raw := canonicalMutationJSON(t, mustJSON(t, value)); seed := sha256.Sum256([]byte(f.projectID))
		return h.Handle(context.Background(), raw, signedBoundProof(t, f.fabricID,
			"wormhole.sync.issue_agent_session", raw, value.AttachmentRef, now,
			bytes.Repeat([]byte{nonce}, 32), seed[:]))
	}
	first, err := call(73, args); if err != nil { t.Fatal(err) }
	replay, err := call(74, args); if err != nil || replay != first { t.Fatalf("replay=(%+v,%v)", replay, err) }
	changed := args; changed.HarnessVersion = "2"
	before := sessionEvidenceCounts(t, f)
	if _, err := call(75, changed); err == nil || err.Error() != `{"code":"sync_replay_conflict","operation":"wormhole.sync.issue_agent_session"}` { t.Fatalf("changed error=%v", err) }
	assertSessionEvidenceDelta(t, before, sessionEvidenceCounts(t, f), 1, 0, 1)
	now = first.ExpiresAt.Add(time.Nanosecond)
	second, err := call(76, changed); if err != nil || second.SessionID == first.SessionID { t.Fatalf("expired=(%+v,%v)", second, err) }

	if _, err := f.db.Exec(`CREATE OR REPLACE FUNCTION slice7_fail_session_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'slice7 audit fault'; END $$`); err != nil { t.Fatal(err) }
	if _, err := f.db.Exec(`CREATE TRIGGER slice7_fail_session_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION slice7_fail_session_audit()`); err != nil { t.Fatal(err) }
	before = sessionEvidenceCounts(t, f)
	args.AgentID = addAcceptedSessionAgent(t, f, attached, "70000000-0000-4000-8000-000000000075")
	if _, err := call(77, args); err == nil { t.Fatal("audit fault succeeded") }
	assertSessionEvidenceDelta(t, before, sessionEvidenceCounts(t, f), 0, 0, 0)
}

func TestResolvePublicAttachmentIssueRejectsSessionBeforeSQL(t *testing.T) {
	f := newMutationFixture(t); attached := f.attach(78)
	raw := json.RawMessage(`{"agent_id":"70000000-0000-4000-8000-000000000078","attachment_ref":"`+attached.Attachment.AttachmentRef+`","harness_name":"codex","harness_version":"1","model_name":"","model_version":"","version":2}`)
	seed := sha256.Sum256([]byte(f.projectID))
	proof := signedBoundSessionProof(t, f.fabricID, "wormhole.sync.issue_agent_session", raw,
		attached.Attachment.AttachmentRef, "60000000-0000-4000-8000-000000000078",
		f.transport.OccurredAt, bytes.Repeat([]byte{78}, 32), seed[:])
	before := sessionEvidenceCounts(t, f); called := false
	err := realBoundResolverForDB(t, f, publicRuntimeDB(t)).ResolveAttachmentIssue(context.Background(),
		"wormhole.sync.issue_agent_session", attached.Attachment.AttachmentRef, raw, proof,
		func(context.Context, *sql.Tx, VerifiedPublicAttachmentIssue) (error,error) { called=true; return nil,nil })
	if !errors.Is(err, identity.ErrPublicAuthentication) || called { t.Fatalf("error=%v called=%v", err, called) }
	assertSessionEvidenceDelta(t, before, sessionEvidenceCounts(t, f), 0, 0, 0)
}

// The file defines these fixture helpers using the existing mutationFixture fields.
// `addAcceptedSessionAgent` advances the stream accepted tree through
// AdvanceAcceptedObservedRefInTx with one canonical live agent ActorV1.
// `removeAcceptedActor` advances it with the named actor tombstoned.
// Both helpers use the existing stream transition API, never raw UPDATE.
type sessionEvidence struct{ nonces, sessions, audits int }
func sessionEvidenceCounts(t *testing.T, f *mutationFixture) sessionEvidence {
	t.Helper(); var got sessionEvidence
	for query, out := range map[string]*int{
		`SELECT count(*) FROM public_request_nonces WHERE project_id=$1`: &got.nonces,
		`SELECT count(*) FROM fabric_public_agent_sessions WHERE project_id=$1`: &got.sessions,
		`SELECT count(*) FROM audit_log WHERE project_id=$1 AND action='sync.issue_agent_session'`: &got.audits,
	} { if err:=f.db.QueryRow(query,f.projectID).Scan(out); err!=nil { t.Fatal(err) } }
	return got
}
func assertSessionEvidenceDelta(t *testing.T,before,after sessionEvidence,n,s,a int){
	t.Helper(); if after.nonces-before.nonces!=n||after.sessions-before.sessions!=s||after.audits-before.audits!=a{t.Fatalf("delta=%+v -> %+v",before,after)}
}
func mustJSON(t *testing.T,v any) json.RawMessage { t.Helper(); b,e:=json.Marshal(v); if e!=nil{t.Fatal(e)}; return b }
```

The implementation must also add fault-table subtests for attachment lock, activated-human read,
session insert, audit insert, and commit. Each uses the same `sessionEvidenceCounts` pre/post image;
only a deliberate authority denial returns `(decisionErr,nil)` and commits nonce+audit.

Run:

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/mcp -run 'Test(SyncV2IssueAgentSession|ResolvePublicAttachmentIssue)' -count=1
```

Expected: compile failure for the new seam and handler, not a skipped test.

- [ ] **Step 2: Implement the attachment-only transaction and handler exactly.**

Append to `internal/mcp/public_auth.go`:

```go
type VerifiedPublicAttachmentIssue struct {
	Proof        VerifiedPublicProof
	Attachment   coregit.StreamAttachment
	State        coregit.StreamTransition
	AuditScope   types.ActorScope
	AuthorityErr error
}
type PublicAttachmentIssueFunc func(context.Context,*sql.Tx,VerifiedPublicAttachmentIssue)(decisionErr,error)

func (r *PublicBoundProofResolver) ResolveAttachmentIssue(ctx context.Context,tool,attachmentRef string,
	raw json.RawMessage,proof types.PublicRequestProof,callback PublicAttachmentIssueFunc) error {
	if r==nil||r.identity==nil||r.streams==nil||!r.verifier.readyForFabric(r.fabricInstanceID)||callback==nil{return identity.ErrInvalidPublicIdentity}
	if proof.SessionID!="" { return identity.ErrPublicAuthentication }
	verified,err:=r.verifier.VerifyBound(tool,attachmentRef,raw,proof); if err!=nil{return err}
	projectID,err:=r.streams.ResolveAttachmentProject(ctx,r.fabricInstanceID,attachmentRef); if err!=nil{return err}
	tx,err:=r.identity.BeginProjectTx(ctx,projectID); if err!=nil{return err}; defer tx.Rollback()
	attached,err:=r.streams.LockAttachmentInTx(ctx,tx,coregit.AttachmentLookup{ProjectID:projectID,FabricInstanceID:r.fabricInstanceID,AttachmentRef:attachmentRef}); if err!=nil{return err}
	if !completePublicAttachment(attached)||attached.Attachment.IssuerKeyFingerprint!=verified.KeyFingerprint{return identity.ErrPublicAuthentication}
	evidence:=authorityEvidence(attached)
	activatedHuman,err:=r.identity.ResolvePublicIssuerHumanInTx(ctx,tx,evidence,verified.KeyFingerprint); if err!=nil{return err}
	auditScope:=types.ActorScope{ProjectID:projectID,Actor:types.ActorEnvelope{ActorKind:types.ActorHuman,HumanPrincipalID:activatedHuman,Assurance:types.AssurancePublicKeyContinuity,OccurredAt:verified.Timestamp}}
	if auditScope.Validate()!=nil{return identity.ErrPublicAuthentication}
	authorityErr:=error(nil)
	currentHuman,currentErr:=resolveVerifiedTrackedHuman(attached.State.Accepted,verified)
	if currentErr!=nil||currentHuman.ID!=activatedHuman { authorityErr=identity.ErrPublicAuthentication }
	if err:=r.identity.ConsumePublicNonceInTx(ctx,tx,identity.PublicNonceUse{ProjectID:projectID,FabricInstanceID:attached.Attachment.Key.FabricInstanceID,StreamID:attached.Attachment.Key.StreamID,CanonicalRef:attached.Attachment.CanonicalRef,KeyFingerprint:verified.KeyFingerprint,Claim:verified.Claim});err!=nil{return err}
	decisionErr,internalErr:=callback(ctx,tx,VerifiedPublicAttachmentIssue{Proof:verified,Attachment:attached.Attachment,State:attached.State,AuditScope:auditScope,AuthorityErr:authorityErr})
	if internalErr!=nil{return internalErr}
	if err:=tx.Commit();err!=nil{return fmt.Errorf("mcp: commit public attachment issue: %w",err)}
	return decisionErr
}
```

Add this exact identity reader to `internal/core/identity/public_sync.go`:

```go
func (s *Store) ResolvePublicIssuerHumanInTx(ctx context.Context,tx *sql.Tx,e PublicAuthorityEvidence,fingerprint string)(string,error){
	if s==nil||tx==nil||!validHistoricalSessionEvidence(e)||fingerprint!=e.IssuerKeyFingerprint{return "",ErrInvalidPublicIdentity}
	var humanID string; var publicKey []byte; var sourceVersion int64
	err:=tx.QueryRowContext(ctx,`SELECT human_principal_id,public_key,source_version FROM fabric_public_actor_keys WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND canonical_ref=$4 AND key_fingerprint=$5 AND source_version=$6 AND actor_kind='human' AND revoked_at IS NULL`,e.ProjectID,e.FabricInstanceID,e.StreamID,e.CanonicalRef,fingerprint,e.AttachmentSourceVersion).Scan(&humanID,&publicKey,&sourceVersion)
	if errors.Is(err,sql.ErrNoRows){return "",ErrPublicAuthentication}; if err!=nil{return "",fmt.Errorf("identity: resolve public issuer human: %w",err)}
	if !types.CanonicalUUID(humanID)||len(publicKey)!=ed25519.PublicKeySize||sourceVersion!=e.AttachmentSourceVersion{return "",ErrPublicAuthentication}
	return humanID,nil
}
```

Create `internal/mcp/sync_v2_session.go`:

```go
package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	coregit "github.com/H4RL33/wormhole/internal/core/git"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

type SyncV2AgentSessionHandler struct{resolver *PublicBoundProofResolver;identity *identity.Store;now func()time.Time}
func NewSyncV2AgentSessionHandler(r *PublicBoundProofResolver,s *identity.Store,now func()time.Time)(*SyncV2AgentSessionHandler,error){
	h:=&SyncV2AgentSessionHandler{r,s,now}; if r==nil||s==nil||now==nil{return nil,identity.ErrInvalidPublicIdentity}; return h,nil
}
type sessionAuditV2 struct{AgentID string `json:"agent_id"`;AttachmentRef string `json:"attachment_ref"`;Outcome string `json:"outcome"`;Version int `json:"version"`}

func (h *SyncV2AgentSessionHandler) Handle(ctx context.Context,raw json.RawMessage,proof types.PublicRequestProof)(projectstate.PublicAgentSessionIssueV2Result,error){
	const tool="wormhole.sync.issue_agent_session"
	if h==nil||h.resolver==nil||h.identity==nil||h.now==nil{return projectstate.PublicAgentSessionIssueV2Result{},syncSessionFailure(tool,"internal_error")}
	var args projectstate.PublicAgentSessionIssueV2Args
	if decodePublicArguments(raw,&args)!=nil||!isCanonicalJSONObject(raw){return projectstate.PublicAgentSessionIssueV2Result{},syncReadDecodeFailure(tool,raw)}
	if !validSessionIssueArgs(args){return projectstate.PublicAgentSessionIssueV2Result{},syncSessionFailure(tool,"invalid_request")}
	var result projectstate.PublicAgentSessionIssueV2Result
	err:=h.resolver.ResolveAttachmentIssue(ctx,tool,args.AttachmentRef,raw,proof,func(ctx context.Context,tx *sql.Tx,issue VerifiedPublicAttachmentIssue)(error,error){
		decision:=issue.AuthorityErr
		if decision==nil&&!acceptedLiveActor(issue.State.Accepted,args.AgentID,types.ActorAgent){decision=coregit.ErrStreamActor}
		outcome:="denied"
		if decision==nil {
			now:=h.now(); if now.IsZero()||now.Location()!=time.UTC{return nil,identity.ErrInvalidPublicIdentity}
			stored,e:=h.identity.IssuePublicAgentSessionInTx(ctx,tx,identity.PublicAgentSessionIssue{ProjectID:issue.Attachment.Key.ProjectID,FabricInstanceID:issue.Attachment.Key.FabricInstanceID,StreamID:issue.Attachment.Key.StreamID,WorkspaceID:issue.Attachment.WorkspaceID,CanonicalRef:issue.Attachment.CanonicalRef,AttachmentRef:issue.Attachment.AttachmentRef,IssuerKeyFingerprint:issue.Attachment.IssuerKeyFingerprint,AgentID:args.AgentID,HarnessName:args.HarnessName,HarnessVersion:args.HarnessVersion,ModelName:args.ModelName,ModelVersion:args.ModelVersion,SourceVersion:issue.Attachment.SourceVersion,IssuedAt:now})
			if errors.Is(e,identity.ErrPublicSessionConflict){decision=e}else if e!=nil{return nil,e}else{result=sessionIssueResult(stored);outcome="issued"}
		}
		payload,e:=projectstate.CanonicalJSONObject(sessionAuditV2{AgentID:args.AgentID,AttachmentRef:args.AttachmentRef,Outcome:outcome,Version:2}); if e!=nil{return nil,e}
		if _,e=h.identity.RecordActorActionInTx(ctx,tx,issue.AuditScope,"sync.issue_agent_session",payload);e!=nil{return nil,e}
		return decision,nil
	})
	if err!=nil{return projectstate.PublicAgentSessionIssueV2Result{},syncSessionFailure(tool,syncSessionErrorCode(err))}
	return result,nil
}

func validSessionIssueArgs(a projectstate.PublicAgentSessionIssueV2Args)bool{return a.Version==2&&types.CanonicalUUID(a.AttachmentRef)&&types.CanonicalUUID(a.AgentID)&&len(a.HarnessName)>0&&len(a.HarnessName)<=128&&len(a.HarnessVersion)>0&&len(a.HarnessVersion)<=128&&len(a.ModelName)<=128&&len(a.ModelVersion)<=128&&(a.ModelName=="")== (a.ModelVersion=="")}
func acceptedLiveActor(s projectstate.Snapshot,id string,kind types.ActorKind)bool{r,ok:=s.Actors[id];return ok&&r.Value!=nil&&r.Tombstone==nil&&r.Value.ID==id&&r.Value.ActorKind==kind}
func sessionIssueResult(s identity.PublicAgentSession)projectstate.PublicAgentSessionIssueV2Result{return projectstate.PublicAgentSessionIssueV2Result{Version:2,SessionID:s.SessionID,AgentID:s.AgentID,AccountableHumanID:s.AccountableHumanID,HarnessName:s.HarnessName,HarnessVersion:s.HarnessVersion,ModelName:s.ModelName,ModelVersion:s.ModelVersion,Assurance:types.AssurancePublicKeyContinuity,ExpiresAt:s.ExpiresAt.UTC()}}
func syncSessionErrorCode(err error)string{switch{case errors.Is(err,identity.ErrPublicAuthentication),errors.Is(err,identity.ErrPublicNonceReplay),errors.Is(err,identity.ErrInvalidPublicIdentity):return "authentication_failed";case errors.Is(err,coregit.ErrStreamNotFound):return "attachment_not_found";case errors.Is(err,coregit.ErrStreamActor):return "permission_denied";case errors.Is(err,identity.ErrPublicSessionConflict):return "sync_replay_conflict";default:return "internal_error"}}
func syncSessionFailure(operation,code string)error{return syncReadFailure(operation,code)}
```

Run Step 1. Expected: handler/resolver tests pass.

- [ ] **Step 3: Add the strict client RED tables.**

Create `internal/runtime/sync/client_v2_test.go` with a `recordingPublicCaller` implementing
Task 1's exact interface. Its complete causal core is:

```go
type recordingPublicCaller struct{calls []publicCall; result json.RawMessage; err error}
type publicCall struct{profile types.FabricProfile;credential,tool,scope string;args json.RawMessage}
func (c *recordingPublicCaller) Call(_ context.Context,p types.FabricProfile,credential,tool,scope string,args json.RawMessage)(json.RawMessage,error){c.calls=append(c.calls,publicCall{p,credential,tool,scope,bytes.Clone(args)});return bytes.Clone(c.result),c.err}

func TestV2FabricClientUsesExactImmutableAuthorityAndRoute(t *testing.T){
	workspace,profile,binding,state:=v2ClientFixture(t)
	caller:=&recordingPublicCaller{}
	factory,err:=NewMCPV2FabricClientFactory(caller,func()time.Time{return time.Date(2026,9,2,12,0,0,0,time.UTC)});if err!=nil{t.Fatal(err)}
	attachClient,err:=factory.AttachClient(context.Background(),workspace,profile);if err!=nil{t.Fatal(err)}
	caller.result=mustCanonicalObject(t,projectstate.SyncAttachV2Result{Version:2,AttachmentRef:binding.AttachmentRef,RemoteProjectID:binding.RemoteProjectID,StreamID:binding.StreamID,StreamVersion:state.StreamVersion,EffectiveActivityPolicy:testFinitePolicy()})
	human,_:=HumanCallAuthority(profile.CredentialRef)
	attachArgs:=projectstate.SyncAttachV2Args{Version:2,Repository:workspace.Repository,CanonicalRef:workspace.AcceptedRef,BaseCommitSHA:workspace.AcceptedCommitSHA,BaseTreeDigest:projectstate.Digest(workspace.AcceptedTreeDigest)}
	got,err:=attachClient.Attach(context.Background(),human,attachArgs);if err!=nil||got.Wire.AttachmentRef!=binding.AttachmentRef||got.ActivityPolicy.State!="enabled"{t.Fatalf("attach=(%+v,%v)",got,err)}
	wantRepositoryScope,_:=projectstate.RepositoryScopeKey(workspace.Repository,workspace.AcceptedRef)
	assertLastPublicCall(t,caller,"wormhole.sync.attach",wantRepositoryScope,profile.CredentialRef,attachArgs)

	client,err:=factory.Client(context.Background(),binding,profile);if err!=nil{t.Fatal(err)}
	session:=projectstate.PublicAgentSessionIssueV2Result{Version:2,SessionID:"60000000-0000-4000-8000-000000000001",AgentID:"70000000-0000-4000-8000-000000000001",AccountableHumanID:"80000000-0000-4000-8000-000000000001",HarnessName:"codex",HarnessVersion:"1",ModelName:"gpt",ModelVersion:"5",Assurance:types.AssurancePublicKeyContinuity,ExpiresAt:time.Date(2026,9,3,12,0,0,0,time.UTC)}
	agent,_:=AgentCallAuthority(profile.CredentialRef,session)
	op:=v2ClientOperation(t,state.LiveTreeDigest,session)
	caller.result=mustCanonicalObject(t,projectstate.SyncPushAppliedV2Result{Version:2,Status:"applied",OperationID:op.ID,StreamVersion:state.StreamVersion+1,LiveTreeDigest:state.LiveTreeDigest})
	push,err:=client.Push(context.Background(),agent,projectstate.SyncPushV2Args{SyncV2Scope:v2Scope(binding,state),Operation:op});if err!=nil||push.Applied==nil||push.Conflict!=nil{t.Fatalf("push=(%+v,%v)",push,err)}
	assertLastPublicCall(t,caller,"wormhole.sync.push",binding.AttachmentRef+"#"+session.SessionID,profile.CredentialRef,projectstate.SyncPushV2Args{SyncV2Scope:v2Scope(binding,state),Operation:op})
}

func TestV2FabricClientClosedDecoderAndPolicyClassification(t *testing.T){
	workspace,profile,binding,_:=v2ClientFixture(t);caller:=&recordingPublicCaller{};factory,_:=NewMCPV2FabricClientFactory(caller,func()time.Time{return time.Date(2026,9,2,12,0,0,0,time.UTC)});client,_:=factory.AttachClient(context.Background(),workspace,profile);human,_:=HumanCallAuthority(profile.CredentialRef)
	base:=`{"attachment_ref":"`+binding.AttachmentRef+`","remote_project_id":"`+binding.RemoteProjectID+`","stream_id":"`+binding.StreamID+`","stream_version":0,"version":2}`
	for name,test:=range map[string]struct{raw string;wantState,wantReason string;wantErr bool}{
		"missing policy":{base,"disabled","missing",false},
		"null policy":{strings.TrimSuffix(base,"}")+`,"effective_activity_policy":null}`,"disabled","missing",false},
		"malformed policy":{strings.TrimSuffix(base,"}")+`,"effective_activity_policy":{"schema_version":"one"}}`,"disabled","malformed",false},
		"unbounded policy":{strings.TrimSuffix(base,"}")+`,"effective_activity_policy":{"schema_version":1,"policy_version":1,"ordinary_max_age_seconds":2592000,"ordinary_max_rows":-1,"terminal_default_age_seconds":2592000,"terminal_maximum_age_seconds":31536000,"terminal_retention_seconds":2592000}}`,"disabled","unbounded",false},
		"duplicate wrapper":{strings.Replace(base,`"version":2`,`"version":2,"version":2`,1),"","",true},
		"unknown wrapper":{strings.TrimSuffix(base,"}")+`,"unknown":true}`,"","",true},
		"trailing":{base+` {}`,"","",true},
	}{t.Run(name,func(t *testing.T){caller.result=json.RawMessage(test.raw);got,err:=client.Attach(context.Background(),human,projectstate.SyncAttachV2Args{Version:2,Repository:workspace.Repository,CanonicalRef:workspace.AcceptedRef,BaseCommitSHA:workspace.AcceptedCommitSHA,BaseTreeDigest:projectstate.Digest(workspace.AcceptedTreeDigest)});if (err!=nil)!=test.wantErr{t.Fatalf("err=%v",err)};if !test.wantErr&&(got.ActivityPolicy.State!=test.wantState||got.ActivityPolicy.DisabledReason!=test.wantReason){t.Fatalf("policy=%+v",got.ActivityPolicy)}})}
}
```

Add sibling table tests invoking all six methods for wrong operation/status/version, duplicate and
unsorted conflict IDs, divergent equal cursor state, oversized tree/body, wrong route, ambiguous
push union, and agent tuple mismatch. Every row asserts `errors.Is(err, ErrFabricProtocol)` and no
second caller invocation.

Run:

```bash
go test ./internal/runtime/sync -run '^TestV2FabricClient' -count=1
```

Expected: compile failure for the client/factory.

- [ ] **Step 4: Implement all client and authority method bodies.**

Create `internal/runtime/sync/client_v2.go`. The following is the complete central implementation;
small `validateV2*` helpers follow the stated closed checks and are exercised by the Step-3 tables.

```go
package sync

import("bytes";"context";"encoding/json";"errors";"io";"reflect";"sort";"strings";"time";
 "github.com/H4RL33/wormhole/internal/runtime/localstore";"github.com/H4RL33/wormhole/internal/types";"github.com/H4RL33/wormhole/internal/types/projectstate")

var ErrFabricProtocol=errors.New("sync: Fabric protocol violation")
type MCPV2FabricClientFactory struct{caller PublicToolCaller;now func()time.Time}
type MCPV2FabricClient struct{caller PublicToolCaller;now func()time.Time;workspace types.WorkspaceBinding;binding *types.FabricBinding;profile types.FabricProfile}

func NewMCPV2FabricClientFactory(c PublicToolCaller,now func()time.Time)(*MCPV2FabricClientFactory,error){if nilInterface(c)||now==nil{return nil,ErrAttentionRequired};return &MCPV2FabricClientFactory{c,now},nil}
func (f *MCPV2FabricClientFactory) AttachClient(_ context.Context,w types.WorkspaceBinding,p types.FabricProfile)(V2FabricClient,error){if f==nil||nilInterface(f.caller)||w.Validate()!=nil||p.Validate()!=nil||p.Mode!=types.FabricModePublic{return nil,ErrAttentionRequired};return &MCPV2FabricClient{f.caller,f.now,w,nil,p},nil}
func (f *MCPV2FabricClientFactory) Client(_ context.Context,b types.FabricBinding,p types.FabricProfile)(V2FabricClient,error){if f==nil||nilInterface(f.caller)||b.ValidateWithProfile(p)!=nil||p.Mode!=types.FabricModePublic{return nil,ErrAttentionRequired};copy:=b;return &MCPV2FabricClient{f.caller,f.now,b.Workspace,&copy,p},nil}

func HumanCallAuthority(ref string)(V2CallAuthority,error){a:=V2CallAuthority{CredentialRef:ref};if validateCallAuthority(a)==nil{return a,nil};return V2CallAuthority{},ErrAttentionRequired}
func AgentCallAuthority(ref string,s projectstate.PublicAgentSessionIssueV2Result)(V2CallAuthority,error){copy:=s;a:=V2CallAuthority{CredentialRef:ref,AgentSession:&copy};if validateCallAuthority(a)==nil{return a,nil};return V2CallAuthority{},ErrAttentionRequired}
func validateCallAuthority(a V2CallAuthority)error{if !strings.HasPrefix(a.CredentialRef,"localidentity:human:")||!types.CanonicalUUID(strings.TrimPrefix(a.CredentialRef,"localidentity:human:")){return ErrAttentionRequired};if a.AgentSession==nil{return nil};s:=a.AgentSession;if s.Version!=2||!types.CanonicalUUID(s.SessionID)||!types.CanonicalUUID(s.AgentID)||!types.CanonicalUUID(s.AccountableHumanID)||s.Assurance!=types.AssurancePublicKeyContinuity||s.HarnessName==""||s.HarnessVersion==""||s.ExpiresAt.IsZero()||s.ExpiresAt.Location()!=time.UTC||(s.ModelName=="")!=(s.ModelVersion==""){return ErrAttentionRequired};return nil}
func (c *MCPV2FabricClient) proofScope(a V2CallAuthority)(string,error){if c==nil||validateCallAuthority(a)!=nil||a.CredentialRef!=c.profile.CredentialRef||c.binding==nil{return "",ErrAttentionRequired};if a.AgentSession==nil{return PublicHumanProofScope(c.binding.AttachmentRef)};return PublicAgentProofScope(c.binding.AttachmentRef,a.AgentSession.SessionID)}
func (c *MCPV2FabricClient) call(ctx context.Context,a V2CallAuthority,tool,scope string,value any)(json.RawMessage,error){if c==nil||validateCallAuthority(a)!=nil||a.CredentialRef!=c.profile.CredentialRef{return nil,ErrAttentionRequired};raw,e:=projectstate.CanonicalJSONObject(value);if e!=nil{return nil,ErrFabricProtocol};out,e:=c.caller.Call(ctx,c.profile,a.CredentialRef,tool,scope,raw);if e!=nil{return nil,e};return bytes.Clone(out),nil}

func (c *MCPV2FabricClient) Attach(ctx context.Context,a V2CallAuthority,in projectstate.SyncAttachV2Args)(AttachResult,error){
	if c==nil||c.binding!=nil||a.AgentSession!=nil||validateAttachArgs(c.workspace,in)!=nil{return AttachResult{},ErrFabricProtocol};scope,e:=projectstate.RepositoryScopeKey(in.Repository,in.CanonicalRef);if e!=nil{return AttachResult{},ErrFabricProtocol};raw,e:=c.call(ctx,a,"wormhole.sync.attach",scope,in);if e!=nil{return AttachResult{},e};wire,policy,e:=decodeAttachResult(raw,c.now());if e!=nil||wire.Version!=2||!types.CanonicalUUID(wire.AttachmentRef)||!types.CanonicalUUID(wire.RemoteProjectID)||!types.CanonicalUUID(wire.StreamID)||wire.StreamVersion<0||wire.StreamVersion>maximumV2Integer{return AttachResult{},ErrFabricProtocol};return AttachResult{wire,policy},nil
}
func (c *MCPV2FabricClient) IssueAgentSession(ctx context.Context,a V2CallAuthority,in projectstate.PublicAgentSessionIssueV2Args)(projectstate.PublicAgentSessionIssueV2Result,error){
	if c==nil||c.binding==nil||a.AgentSession!=nil||!validRuntimeSessionArgs(in)||in.AttachmentRef!=c.binding.AttachmentRef{return projectstate.PublicAgentSessionIssueV2Result{},ErrFabricProtocol};scope,e:=c.proofScope(a);if e!=nil{return projectstate.PublicAgentSessionIssueV2Result{},e};raw,e:=c.call(ctx,a,"wormhole.sync.issue_agent_session",scope,in);if e!=nil{return projectstate.PublicAgentSessionIssueV2Result{},e};var out projectstate.PublicAgentSessionIssueV2Result;if decodeClosedV2(raw,&out)!=nil||validateIssuedSession(out,in,c.now())!=nil{return projectstate.PublicAgentSessionIssueV2Result{},ErrFabricProtocol};return out,nil
}
func (c *MCPV2FabricClient) Bootstrap(ctx context.Context,a V2CallAuthority,in projectstate.SyncBootstrapV2Args)(BootstrapResult,error){
	if c==nil||c.binding==nil||validateV2Scope(*c.binding,in.SyncV2Scope)!=nil||in.AfterVersion<0||in.AfterVersion>maximumV2Integer{return BootstrapResult{},ErrFabricProtocol};scope,e:=c.proofScope(a);if e!=nil{return BootstrapResult{},e};raw,e:=c.call(ctx,a,"wormhole.sync.bootstrap",scope,in);if e!=nil{return BootstrapResult{},e};wire,policy,e:=decodeBootstrapResult(raw,c.now());if e!=nil||wire.Version!=2||wire.Changed!=(wire.State.StreamVersion>in.AfterVersion)||validateV2State(*c.binding,wire.State)!=nil{return BootstrapResult{},ErrFabricProtocol};return BootstrapResult{wire,policy},nil
}
func (c *MCPV2FabricClient) Pull(ctx context.Context,a V2CallAuthority,in projectstate.SyncPullV2Args)(projectstate.SyncPullV2Result,error){
	if c==nil||c.binding==nil||validateV2Scope(*c.binding,in.SyncV2Scope)!=nil||in.AfterVersion<0||in.AfterVersion>maximumV2Integer{return projectstate.SyncPullV2Result{},ErrFabricProtocol};scope,e:=c.proofScope(a);if e!=nil{return projectstate.SyncPullV2Result{},e};raw,e:=c.call(ctx,a,"wormhole.sync.pull",scope,in);if e!=nil{return projectstate.SyncPullV2Result{},e};var out projectstate.SyncPullV2Result;if decodeClosedV2(raw,&out)!=nil||out.Version!=2||out.Changed!=(out.State.StreamVersion>in.AfterVersion)||validateV2State(*c.binding,out.State)!=nil{return projectstate.SyncPullV2Result{},ErrFabricProtocol};return out,nil
}
func (c *MCPV2FabricClient) Push(ctx context.Context,a V2CallAuthority,in projectstate.SyncPushV2Args)(PushResult,error){
	if c==nil||c.binding==nil||validateV2Scope(*c.binding,in.SyncV2Scope)!=nil||validateOperationAuthority(in.Operation,a)!=nil{return PushResult{},ErrFabricProtocol};scope,e:=c.proofScope(a);if e!=nil{return PushResult{},e};raw,e:=c.call(ctx,a,"wormhole.sync.push",scope,in);if e!=nil{return PushResult{},e};fields,e:=closedV2Fields(raw);if e!=nil{return PushResult{},ErrFabricProtocol};switch string(fields["status"]){case `"applied"`:var out projectstate.SyncPushAppliedV2Result;if decodeClosedV2(raw,&out)!=nil||out.Version!=2||out.Status!="applied"||out.OperationID!=in.Operation.ID||out.StreamVersion<=in.ExpectedStreamVersion||!validDigest(out.LiveTreeDigest){return PushResult{},ErrFabricProtocol};return PushResult{Applied:&out},nil;case `"conflict"`:var out projectstate.SyncPushConflictV2Result;if decodeClosedV2(raw,&out)!=nil||out.Version!=2||out.Status!="conflict"||out.OperationID!=in.Operation.ID||out.StreamVersion<=in.ExpectedStreamVersion||!validDigest(out.LiveTreeDigest)||!types.CanonicalUUID(out.ConflictID){return PushResult{},ErrFabricProtocol};return PushResult{Conflict:&out},nil;default:return PushResult{},ErrFabricProtocol}
}
func (c *MCPV2FabricClient) ResolveConflict(ctx context.Context,a V2CallAuthority,in projectstate.SyncConflictV2Args)(projectstate.SyncConflictResolvedV2Result,error){
	if c==nil||c.binding==nil||validateV2Scope(*c.binding,in.SyncV2Scope)!=nil||!types.CanonicalUUID(in.ConflictID)||validateOperationAuthority(in.Resolution,a)!=nil{return projectstate.SyncConflictResolvedV2Result{},ErrFabricProtocol};scope,e:=c.proofScope(a);if e!=nil{return projectstate.SyncConflictResolvedV2Result{},e};raw,e:=c.call(ctx,a,"wormhole.sync.conflict",scope,in);if e!=nil{return projectstate.SyncConflictResolvedV2Result{},e};var out projectstate.SyncConflictResolvedV2Result;if decodeClosedV2(raw,&out)!=nil||out.Version!=2||out.Status!="resolved"||out.ConflictID!=in.ConflictID||out.OperationID!=in.Resolution.ID||out.StreamVersion<=in.ExpectedStreamVersion||!validDigest(out.LiveTreeDigest){return projectstate.SyncConflictResolvedV2Result{},ErrFabricProtocol};return out,nil
}

const maximumV2Integer int64=9_007_199_254_740_991
func decodeClosedV2(raw []byte,dst any)error{if rejectDuplicateJSONMembers(raw)!=nil{return ErrFabricProtocol};d:=json.NewDecoder(bytes.NewReader(raw));d.DisallowUnknownFields();if d.Decode(dst)!=nil{return ErrFabricProtocol};var tail any;if !errors.Is(d.Decode(&tail),io.EOF){return ErrFabricProtocol};return nil}
func closedV2Fields(raw []byte)(map[string]json.RawMessage,error){var v map[string]json.RawMessage;if decodeClosedV2(raw,&v)!=nil{return nil,ErrFabricProtocol};return v,nil}
func validateOperationAuthority(op projectstate.OperationV1,a V2CallAuthority)error{if _,e:=projectstate.CanonicalOperation(op);e!=nil{return e};if op.Actor.ActorKind==types.ActorHuman {if a.AgentSession!=nil{return ErrAttentionRequired};return nil};if op.Actor.ActorKind!=types.ActorAgent||a.AgentSession==nil{return ErrAttentionRequired};s:=a.AgentSession;if op.Actor.AgentID!=s.AgentID||op.Actor.AccountableHumanID!=s.AccountableHumanID||op.Actor.HarnessName!=s.HarnessName||op.Actor.HarnessVersion!=s.HarnessVersion||op.Actor.ModelName!=s.ModelName||op.Actor.ModelVersion!=s.ModelVersion{return ErrAttentionRequired};return nil}
```

`decodeAttachResult` and `decodeBootstrapResult` must first strict-decode a private raw struct whose
policy field is `json.RawMessage`; unknown/duplicate/trailing top-level data remains a protocol
error. They then construct the unchanged shared wire struct and call:

```go
func classifyInitialPolicy(raw json.RawMessage,at time.Time) localstore.ActivityPolicyState {
	disabled:=func(reason string)localstore.ActivityPolicyState{return localstore.ActivityPolicyState{State:"disabled",DisabledReason:reason,UpdatedAt:at.UTC()}}
	if len(raw)==0||bytes.Equal(bytes.TrimSpace(raw),[]byte("null")){return disabled("missing")}
	var p projectstate.EffectiveActivityPolicyV1
	if decodeClosedV2(raw,&p)!=nil||p.SchemaVersion!=1||p.PolicyVersion<1||p.PolicyVersion>maximumV2Integer{return disabled("malformed")}
	if p.OrdinaryMaxAgeSeconds!=2_592_000||p.OrdinaryMaxRows!=10_000||p.TerminalDefaultAgeSeconds!=2_592_000||p.TerminalMaximumAgeSeconds!=31_536_000||p.TerminalRetentionSeconds<2_592_000||p.TerminalRetentionSeconds>31_536_000{return disabled("unbounded")}
	canonical,e:=projectstate.CanonicalActivityPolicy(p);if e!=nil{return disabled("malformed")};digest,e:=projectstate.DigestActivityPolicy(p);if e!=nil{return disabled("malformed")};version:=p.PolicyVersion;copyDigest:=digest;copyPolicy:=p
	return localstore.ActivityPolicyState{State:"enabled",Policy:&copyPolicy,PolicyJSON:bytes.Clone(canonical),PolicyVersion:&version,PolicyDigest:&copyDigest,UpdatedAt:at.UTC()}
}
```

`validateV2Scope` exact-matches repository/ref/base commit/base digest to the bound workspace and
validates safe expected version/digest. `validateV2State` requires nonnil sorted unique canonical
conflict IDs, safe version, valid commit/digests, canonical `DecodeTree` round trips, project and
repository equality in both snapshots, and `reflect.DeepEqual(accepted.Config/live.Config)` plus
their remotes. `decodeClosedV2` and the state validator enforce the Task-3 test table; they never
truncate or normalize server evidence.

- [ ] **Step 5: Implement the shared session authority and its complete causal tests.**

Create `internal/runtime/sync/public_session.go`:

```go
package sync
import("context";"errors";"reflect";"sync";"time";"github.com/H4RL33/wormhole/internal/runtime/localidentity";"github.com/H4RL33/wormhole/internal/types";"github.com/H4RL33/wormhole/internal/types/projectstate")
type sessionCacheKey struct{Attachment,Agent,Human,Harness,HarnessVersion,Model,ModelVersion string}
type AgentSessionAuthority struct{routes FabricRouteSource;clients V2FabricClientFactory;selected localidentity.SelectedHumanSource;now func()time.Time;mu sync.Mutex;cache map[sessionCacheKey]projectstate.PublicAgentSessionIssueV2Result}
func NewAgentSessionAuthority(r FabricRouteSource,c V2FabricClientFactory,s localidentity.SelectedHumanSource,now func()time.Time)(*AgentSessionAuthority,error){if nilInterface(r)||nilInterface(c)||nilInterface(s)||now==nil{return nil,ErrAttentionRequired};return &AgentSessionAuthority{routes:r,clients:c,selected:s,now:now,cache:map[sessionCacheKey]projectstate.PublicAgentSessionIssueV2Result{}},nil}
func (a *AgentSessionAuthority) Acquire(ctx context.Context,w types.WorkspaceBinding,actor types.ActorEnvelope)(projectstate.PublicAgentSessionIssueV2Result,error){
	if a==nil||w.Validate()!=nil||actor.ValidateHistorical()!=nil||actor.ActorKind!=types.ActorAgent{return projectstate.PublicAgentSessionIssueV2Result{},ErrAttentionRequired}
	b,p,e:=a.routes.GetRoute(ctx,w.Scope);if e!=nil{return projectstate.PublicAgentSessionIssueV2Result{},ErrAttentionRequired};if b.Workspace!=w||b.ValidateWithProfile(p)!=nil||p.Mode!=types.FabricModePublic{return projectstate.PublicAgentSessionIssueV2Result{},ErrAttentionRequired}
	h,e:=a.selected.Selected(ctx);if e!=nil||h.HumanPrincipalID!=actor.AccountableHumanID{return projectstate.PublicAgentSessionIssueV2Result{},ErrAttentionRequired}
	credential:="localidentity:human:"+h.HumanPrincipalID;if p.CredentialRef!=credential{return projectstate.PublicAgentSessionIssueV2Result{},ErrAttentionRequired}
	key:=sessionCacheKey{b.AttachmentRef,actor.AgentID,actor.AccountableHumanID,actor.HarnessName,actor.HarnessVersion,actor.ModelName,actor.ModelVersion};now:=a.now();if now.IsZero()||now.Location()!=time.UTC{return projectstate.PublicAgentSessionIssueV2Result{},ErrAttentionRequired}
	a.mu.Lock();cached,ok:=a.cache[key];a.mu.Unlock();if ok&&cached.ExpiresAt.After(now){return cached,nil}
	client,e:=a.clients.Client(ctx,b,p);if e!=nil{return projectstate.PublicAgentSessionIssueV2Result{},e};human,e:=HumanCallAuthority(credential);if e!=nil{return projectstate.PublicAgentSessionIssueV2Result{},e}
	request:=projectstate.PublicAgentSessionIssueV2Args{Version:2,AttachmentRef:b.AttachmentRef,AgentID:actor.AgentID,HarnessName:actor.HarnessName,HarnessVersion:actor.HarnessVersion,ModelName:actor.ModelName,ModelVersion:actor.ModelVersion}
	issued,e:=client.IssueAgentSession(ctx,human,request);if e!=nil{return projectstate.PublicAgentSessionIssueV2Result{},e};if issued.AgentID!=actor.AgentID||issued.AccountableHumanID!=actor.AccountableHumanID||issued.HarnessName!=actor.HarnessName||issued.HarnessVersion!=actor.HarnessVersion||issued.ModelName!=actor.ModelName||issued.ModelVersion!=actor.ModelVersion||!issued.ExpiresAt.After(now){return projectstate.PublicAgentSessionIssueV2Result{},ErrFabricProtocol}
	a.mu.Lock();if current,exists:=a.cache[key];exists&&current.ExpiresAt.After(now)&&!reflect.DeepEqual(current,issued){a.mu.Unlock();return projectstate.PublicAgentSessionIssueV2Result{},ErrFabricProtocol};a.cache[key]=issued;a.mu.Unlock();return issued,nil
}
```

`public_session_test.go` defines recording route/selected/factory/client fakes and contains a table
that mutates each exact route/workspace/selected-human/agent tuple. For every denial it asserts
`factory.calls==0`. A concurrent two-route test blocks route A's client and completes B to prove
the cache mutex is not held over network. An expiry test advances the injected clock and asserts a
second issue call; a copied-result test mutates the caller's result and proves the cache is intact.

- [ ] **Step 6: Integrate Activity with persisted actor proof scopes.**

Add `ProofScope string` to all four private runtime request structs in `activity_v1.go`. Add
`sessions PublicAgentSessionAuthority` to `ActivityTransport` and require it in the constructor.
For private profiles, retain the existing bearer path and leave `ProofScope` empty. For public
profiles, do not call `CredentialSource.Read`; create the client with the logical profile reference.

Add this helper and call it immediately before every public send:

```go
func (s *ActivityTransport) publicActivityScope(ctx context.Context,b types.FabricBinding,p types.FabricProfile,actor *types.ActorEnvelope)(string,error){
	if p.Mode!=types.FabricModePublic{return "",nil}
	if actor==nil{return PublicHumanProofScope(b.AttachmentRef)}
	if actor.ValidateHistorical()!=nil{return "",ErrAttentionRequired}
	switch actor.ActorKind{case types.ActorHuman:return PublicHumanProofScope(b.AttachmentRef);case types.ActorAgent:issued,e:=s.sessions.Acquire(ctx,b.Workspace,*actor);if e!=nil{return "",e};return PublicAgentProofScope(b.AttachmentRef,issued.SessionID);default:return "",ErrAttentionRequired}
}
```

For Accept and Presence pass `&record.Activity.Actor` / `&activity.Actor`. Pull is a Gateway read
and passes nil (human proof). Lifecycle first calls the new read below, then authenticates from
that immutable stored actor before client construction/network:

```go
// internal/runtime/localstore/activity_records.go
func (r *ActivityRepo) Activity(ctx context.Context,key types.ActivityOriginKey)(ActivityRecord,error){
	if r==nil||r.db==nil||key.Validate()!=nil{return ActivityRecord{},ErrActivityNotFound}
	loader:=newActivityEvidenceLoader();e,err:=loader.load(ctx,r.db,key);if err!=nil{return ActivityRecord{},err};return cloneActivityRecord(e.record),nil
}
```

Replace `ActivityPublicCaller` with the Task-1 `PublicToolCaller`. `ActivityPublicClient.call`
requires `request.ProofScope` and calls:

```go
raw,err:=c.caller.Call(ctx,c.profile,c.credentialRef,operation,proofScope,canonical)
```

Add tests named `TestActivityPublicModeNeverReadsCredentialSource`,
`TestActivityAgentAttemptUsesFreshRemoteSessionNotHistoricalSession`,
`TestActivityLifecycleLoadsPersistedActorBeforeClient`, and
`TestActivitySessionFailureStopsBeforeHTTP`. Each uses a credential fake that panics on public
reads and records route/session/client/caller order. Retain all private-mode tests unchanged.

- [ ] **Step 7: Run Task 2 GREEN/race/vet, commit, and independently review.**

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/mcp ./internal/core/identity -run 'Test(SyncV2IssueAgentSession|ResolvePublicAttachmentIssue|PublicAgentSession)' -count=1
go test ./internal/runtime/sync ./internal/runtime/localstore -run 'Test(V2FabricClient|AgentSessionAuthority|Activity)' -count=1
go test -race ./internal/runtime/sync ./internal/mcp -run 'Test(SyncV2IssueAgentSession|AgentSessionAuthority|Activity)' -count=1
go vet ./internal/mcp ./internal/core/identity ./internal/runtime/sync ./internal/runtime/localstore
git add internal/mcp/public_auth.go internal/mcp/public_auth_test.go internal/mcp/sync_v2_session.go internal/mcp/sync_v2_session_test.go internal/mcp/sync_v2.go internal/mcp/sync_v2_contract_test.go \
  internal/core/identity/public_sync.go internal/core/identity/public_sync_test.go \
  internal/runtime/sync/contract_v2.go internal/runtime/sync/client_v2.go internal/runtime/sync/client_v2_test.go internal/runtime/sync/public_session.go internal/runtime/sync/public_session_test.go \
  internal/runtime/sync/activity_v1.go internal/runtime/sync/activity_v1_test.go internal/runtime/sync/activity_mcp_client.go internal/runtime/sync/activity_mcp_client_test.go internal/runtime/sync/activity_lifecycle_transport.go internal/runtime/sync/activity_lifecycle_transport_test.go \
  internal/runtime/localstore/activity_records.go internal/runtime/localstore/activity_records_test.go
git diff --cached --name-only
git commit -m "feat: add accountable public sync sessions"
TASK_2_HEAD=$(git rev-parse HEAD)
```

The cached manifest must equal the Files list. Review `TASK_2_BASE..TASK_2_HEAD` to C0/I0;
repairs use a separate `fix: address Slice 7 session review` commit and repeat every gate.

---

### Task 3: Destructive Gateway Schema v9 and Six-Key Sync State

**Files:** create `internal/runtime/localstore/private_schema_v9.sql`,
`internal/runtime/localstore/fabric_sync_state.go`,
`internal/runtime/localstore/fabric_sync_state_test.go`,
`internal/runtime/localstore/activity_policy_repo.go`, and
`internal/runtime/localstore/activity_policy_repo_test.go`; modify
`internal/runtime/localstore/migrations.go`, `private_format.go`,
`private_format_test.go`, `migrations_test.go`; rename
`internal/runtime/localstore/activity_format_v8_test.go` to
`internal/runtime/localstore/activity_format_v9_test.go`; modify
`fabric_routes.go`, `fabric_routes_test.go`, `workspace_repo.go`,
`internal/runtime/sync/queue_repo.go`, `internal/runtime/sync/queue_repo_test.go`,
`internal/types/routing.go`, `internal/types/routing_test.go`, `docs/db-entities.md`,
`README.md`, `internal/runtime/localstore/r06_authority_test.go`, and
`internal/runtime/localstore/coverage_contract_test.go`. Move the former
production v8 SQL to `internal/runtime/localstore/testdata/private_schema_v8.sql` as a
refusal fixture and remove its production embed. No PostgreSQL migration is added and
`migrations/000022_public_sync_v2.{up,down}.sql` remains untouched.

**Produces:** exact-v9 fresh/open/refusal behavior, canonical-ref-complete route state, atomic
remote replica CAS, explicit enabled/disabled policy, immutable queue delivery, and durable
remote-conflict resolution intent.

- [ ] **Step 1: Capture the base and write the exact format/key RED test.**

```bash
TASK_3_BASE=$(git rev-parse HEAD)
```

Rename `activity_format_v8_test.go` to `activity_format_v9_test.go`, advance all retained exact-format
tests in that file to v9/former-v8 semantics, and add this causal core (the existing format-test helpers supply
`openPrivateFixture`, `tableDDL`, `indexDDL`, and byte-preservation assertions):

```go
func TestPrivateSchemaV9ChangedObjectsAreExact(t *testing.T){
	store:=openPrivateFixture(t);defer store.Close()
	if GatewaySchemaVersion!=9{t.Fatalf("version=%d",GatewaySchemaVersion)}
	for object,want:=range map[string]string{
		"fabric_cursors": expectedV9FabricCursorsDDL,
		"sync_queue": expectedV9SyncQueueDDL,
		"sync_audit": expectedV9SyncAuditDDL,
		"activity_policy_current": expectedV9ActivityPolicyCurrentDDL,
		"fabric_sync_conflicts": expectedV9FabricSyncConflictsDDL,
	}{if got:=tableDDL(t,store.DB(),object);normalizeDDL(got)!=normalizeDDL(want){t.Fatalf("%s DDL\n%s",object,got)}}
	for object,want:=range map[string]string{"sync_queue_pending":expectedV9QueueIndexDDL,"sync_audit_recent":expectedV9AuditIndexDDL,"fabric_sync_conflicts_open":expectedV9ConflictIndexDDL}{if got:=indexDDL(t,store.DB(),object);normalizeDDL(got)!=normalizeDDL(want){t.Fatalf("%s DDL\n%s",object,got)}}
}
func TestExactPrivateV8IsRefusedByteIdentically(t *testing.T){
	path:=filepath.Join(t.TempDir(),"gateway.db");if err:=os.WriteFile(path,privateSchemaV8Fixture,0600);err!=nil{t.Fatal(err)};before:=readDatabaseEvidence(t,path);if _,err:=Open(path);err==nil{t.Fatal("v8 opened")};after:=readDatabaseEvidence(t,path);if !reflect.DeepEqual(before,after){t.Fatal("v8 evidence mutated")}
}
func TestRemoteBindingKeyRequiresCanonicalRef(t *testing.T){
	key:=v9RouteKey();for name,mutate:=range map[string]func(*types.RemoteBindingKey){"missing":func(k *types.RemoteBindingKey){k.CanonicalRef=""},"unsafe":func(k *types.RemoteBindingKey){k.CanonicalRef="refs/heads/../main"}}{t.Run(name,func(t *testing.T){candidate:=key;mutate(&candidate);if candidate.Validate()==nil{t.Fatalf("accepted %+v",candidate)}})}
}
```

Run the existing format fault suite plus these names. Expected: RED because v9 and canonical ref do
not exist.

- [ ] **Step 2: Replace v8 mechanically with this exact v9 DDL.**

Copy the exact former production v8 SQL into `testdata/private_schema_v8.sql` before deleting the
production embed. The unchanged v8 tables/triggers are copied byte-for-byte into
`private_schema_v9.sql`; replace only the following complete blocks. These strings also initialize
the `expectedV9*DDL` constants in the test, so the test and production cannot silently diverge.

In the base `schema` constant in `localstore.go`:

```sql
CREATE TABLE IF NOT EXISTS sync_queue (
  project_id TEXT NOT NULL, workspace_id TEXT NOT NULL,
  fabric_instance_id TEXT NOT NULL, remote_project_id TEXT NOT NULL,
  stream_id TEXT NOT NULL, canonical_ref TEXT NOT NULL, id TEXT NOT NULL,
  operation_json TEXT NOT NULL,
  operation_digest TEXT NOT NULL CHECK(operation_digest GLOB 'sha256:[0-9a-f]*' AND length(operation_digest)=71 AND substr(operation_digest,8) NOT GLOB '*[^0-9a-f]*'),
  priority INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  delivered_at TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,id),
  FOREIGN KEY(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref)
    REFERENCES workspace_fabric_bindings(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS sync_audit (
  project_id TEXT NOT NULL, workspace_id TEXT NOT NULL,
  fabric_instance_id TEXT NOT NULL, remote_project_id TEXT NOT NULL,
  stream_id TEXT NOT NULL, canonical_ref TEXT NOT NULL, id TEXT NOT NULL,
  conflict_json TEXT NOT NULL, actor_json TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,id),
  FOREIGN KEY(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref)
    REFERENCES workspace_fabric_bindings(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref) ON DELETE CASCADE
);
```

In `private_schema_v9.sql`:

```sql
CREATE TABLE fabric_cursors (
  project_id TEXT NOT NULL, workspace_id TEXT NOT NULL,
  fabric_instance_id TEXT NOT NULL, remote_project_id TEXT NOT NULL,
  stream_id TEXT NOT NULL, canonical_ref TEXT NOT NULL,
  stream_version INTEGER NOT NULL CHECK(typeof(stream_version)='integer' AND stream_version BETWEEN 0 AND 9007199254740991),
  accepted_commit_sha TEXT NOT NULL CHECK(length(accepted_commit_sha) IN (40,64) AND accepted_commit_sha NOT GLOB '*[^0-9a-f]*'),
  accepted_tree BLOB NOT NULL CHECK(typeof(accepted_tree)='blob'),
  accepted_tree_digest TEXT NOT NULL CHECK(length(accepted_tree_digest)=71 AND accepted_tree_digest GLOB 'sha256:*' AND substr(accepted_tree_digest,8) NOT GLOB '*[^0-9a-f]*'),
  live_tree BLOB NOT NULL CHECK(typeof(live_tree)='blob'),
  live_tree_digest TEXT NOT NULL CHECK(length(live_tree_digest)=71 AND live_tree_digest GLOB 'sha256:*' AND substr(live_tree_digest,8) NOT GLOB '*[^0-9a-f]*'),
  updated_at TIMESTAMP NOT NULL,
  PRIMARY KEY(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref),
  FOREIGN KEY(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref)
    REFERENCES workspace_fabric_bindings(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref) ON DELETE CASCADE
);

CREATE TABLE activity_policy_current (
  project_id TEXT NOT NULL, workspace_id TEXT NOT NULL,
  fabric_instance_id TEXT NOT NULL, remote_project_id TEXT NOT NULL,
  stream_id TEXT NOT NULL, canonical_ref TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('enabled','disabled')),
  policy_version INTEGER CHECK(policy_version IS NULL OR (typeof(policy_version)='integer' AND policy_version BETWEEN 1 AND 9007199254740991)),
  policy_digest TEXT CHECK(policy_digest IS NULL OR (length(policy_digest)=71 AND policy_digest GLOB 'sha256:*' AND substr(policy_digest,8) NOT GLOB '*[^0-9a-f]*')),
  disabled_reason TEXT CHECK(disabled_reason IS NULL OR disabled_reason IN ('missing','malformed','unbounded')),
  updated_at TIMESTAMP NOT NULL,
  PRIMARY KEY(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref),
  CHECK((state='enabled' AND policy_version IS NOT NULL AND policy_digest IS NOT NULL AND disabled_reason IS NULL) OR
        (state='disabled' AND policy_version IS NULL AND policy_digest IS NULL AND disabled_reason IS NOT NULL)),
  FOREIGN KEY(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref)
    REFERENCES workspace_fabric_bindings(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref) ON DELETE CASCADE,
  FOREIGN KEY(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,policy_version,policy_digest)
    REFERENCES activity_policy_versions(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,policy_version,policy_digest) ON DELETE RESTRICT
);

CREATE TABLE fabric_sync_conflicts (
  project_id TEXT NOT NULL, workspace_id TEXT NOT NULL,
  fabric_instance_id TEXT NOT NULL, remote_project_id TEXT NOT NULL,
  stream_id TEXT NOT NULL, canonical_ref TEXT NOT NULL,
  conflict_id TEXT NOT NULL,
  original_operation_id TEXT NOT NULL,
  original_operation_digest TEXT NOT NULL CHECK(length(original_operation_digest)=71 AND original_operation_digest GLOB 'sha256:*' AND substr(original_operation_digest,8) NOT GLOB '*[^0-9a-f]*'),
  detected_stream_version INTEGER NOT NULL CHECK(typeof(detected_stream_version)='integer' AND detected_stream_version BETWEEN 0 AND 9007199254740991),
  detected_live_tree_digest TEXT NOT NULL CHECK(length(detected_live_tree_digest)=71 AND detected_live_tree_digest GLOB 'sha256:*' AND substr(detected_live_tree_digest,8) NOT GLOB '*[^0-9a-f]*'),
  state TEXT NOT NULL CHECK(state IN ('open','resolved')),
  created_at TIMESTAMP NOT NULL,
  resolution_intent_state TEXT NOT NULL CHECK(resolution_intent_state IN ('none','prepared','resolved')),
  resolution_operation_id TEXT,
  resolution_operation_bytes BLOB CHECK(resolution_operation_bytes IS NULL OR typeof(resolution_operation_bytes)='blob'),
  resolution_operation_digest TEXT CHECK(resolution_operation_digest IS NULL OR (length(resolution_operation_digest)=71 AND resolution_operation_digest GLOB 'sha256:*' AND substr(resolution_operation_digest,8) NOT GLOB '*[^0-9a-f]*')),
  resolved_at TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,conflict_id),
  CHECK((resolution_intent_state='none' AND state='open' AND resolution_operation_id IS NULL AND resolution_operation_bytes IS NULL AND resolution_operation_digest IS NULL AND resolved_at IS NULL) OR
        (resolution_intent_state='prepared' AND state='open' AND resolution_operation_id IS NOT NULL AND resolution_operation_bytes IS NOT NULL AND resolution_operation_digest IS NOT NULL AND resolved_at IS NULL) OR
        (resolution_intent_state='resolved' AND state='resolved' AND resolution_operation_id IS NOT NULL AND resolution_operation_bytes IS NOT NULL AND resolution_operation_digest IS NOT NULL AND resolved_at IS NOT NULL)),
  FOREIGN KEY(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref)
    REFERENCES workspace_fabric_bindings(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref) ON DELETE CASCADE,
  FOREIGN KEY(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,original_operation_id)
    REFERENCES sync_queue(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,id) ON DELETE RESTRICT
);

CREATE INDEX sync_queue_pending ON sync_queue(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,priority DESC,created_at,id) WHERE delivered_at IS NULL;
CREATE INDEX sync_audit_recent ON sync_audit(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,created_at DESC,id DESC);
CREATE INDEX fabric_sync_conflicts_open ON fabric_sync_conflicts(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,created_at,conflict_id) WHERE state='open';
CREATE UNIQUE INDEX fabric_sync_one_open_operation ON fabric_sync_conflicts(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,original_operation_id) WHERE state='open';
INSERT INTO gateway_schema_migrations(version) VALUES (9);
```

Set `GatewaySchemaVersion=9`, embed only `private_schema_v9.sql`, rename every v8 production hook
and initializer to v9, and make `Open` call `initializePrivateSchemaV9`. Existing exact fingerprint,
required-object, failure-restoration, sidecar, and Code Graph orthogonality tests are advanced in
the same commit. Former v8 is accepted only as a refusal fixture.

- [ ] **Step 3: Add repository RED tests with actual transaction faults.**

Create `fabric_sync_state_test.go`. A `newV9SyncFixture` registers one workspace, creates a public
profile, and returns two complete keys differing only in canonical ref. For each table below install
`CREATE TRIGGER fail_<table> BEFORE INSERT ON <table> BEGIN SELECT RAISE(ABORT,'fault'); END`, invoke
one `WithImmediateWorkspace`, reopen, and compare ordered `quote(...)` row projections byte-for-byte:

```go
func TestFabricSyncMutationRollsBackEveryBoundary(t *testing.T){
	for _,table:=range []string{"workspace_fabric_bindings","fabric_cursors","activity_cursors","activity_policy_versions","activity_policy_current","sync_queue","fabric_sync_conflicts"}{
		t.Run(table,func(t *testing.T){f:=newV9SyncFixture(t);before:=f.snapshot(t);installFailTrigger(t,f.db,table);err:=f.repo.WithImmediateWorkspace(context.Background(),f.workspace.Scope,func(tx *WorkspaceMutationTx)error{return applyCompleteV9Mutation(ctx,tx,f)});if err==nil{t.Fatal("fault succeeded")};f.reopen(t);if after:=f.snapshot(t);!bytes.Equal(before,after){t.Fatalf("partial mutation at %s",table)}})
	}
}
func TestCompleteCursorCASUsesAllSixRouteFields(t *testing.T){f:=newV9SyncFixture(t);first:=f.cursor;f.withTx(t,func(tx *WorkspaceMutationTx){if err:=tx.InstallFabricBinding(ctx,f.binding,f.profile);err!=nil{t.Fatal(err)};if err:=tx.CompareAndSwapRemoteReplica(ctx,nil,first);err!=nil{t.Fatal(err)}});wrong:=first;wrong.Key.CanonicalRef="refs/heads/other";next:=first;next.StreamVersion++;if err:=f.cas(&wrong,next);!errors.Is(err,ErrFabricCursorConflict){t.Fatalf("cross-ref CAS=%v",err)}}
func TestActivityPolicyCurrentEnabledDisabledAndAbsence(t *testing.T){
	f:=newV9SyncFixture(t);f.installBindingAndCursor(t);enabled:=f.enabledPolicy();disabled:=f.disabledPolicy("missing");f.installPolicy(t,enabled)
	if got,err:=f.policy(f.firstKey);err!=nil||!reflect.DeepEqual(got,enabled){t.Fatalf("enabled=(%+v,%v)",got,err)}
	f.installPolicy(t,disabled);if _,err:=f.policy(f.firstKey);!errors.Is(err,ErrActivityPolicyUnavailable){t.Fatalf("disabled=%v",err)};f.deleteCurrentPolicy(t);if _,err:=f.policy(f.firstKey);!errors.Is(err,ErrActivityPolicyUnavailable){t.Fatalf("absent=%v",err)}
}
func TestRemoteConflictPreparedIntentSurvivesReopen(t *testing.T){
	f:=newV9SyncFixture(t);f.installBindingAndCursor(t);f.enqueueOriginal(t);f.insertOpenConflict(t);want:=f.resolution();if err:=f.prepareResolution(want);err!=nil{t.Fatal(err)};f.reopen(t);got:=f.conflict(t);if !bytes.Equal(got.ResolutionOperationJSON,want.ResolutionOperationJSON)||got.ResolutionOperationDigest!=want.ResolutionOperationDigest{t.Fatalf("prepared=%+v",got)};changed:=want;changed.ResolutionOperationJSON=json.RawMessage(`{"different":true}`);if err:=f.prepareResolution(changed);!errors.Is(err,ErrRemoteSyncConflict){t.Fatalf("different replay=%v",err)}
}
func TestQueueDeliveryRechecksOpenConflictInTransaction(t *testing.T){
	f:=newV9SyncFixture(t);f.installBindingAndCursor(t);f.enqueueOriginal(t);before:=f.queueProjection(t);release:=f.holdWorkspaceConflictWriter(t);started:=make(chan struct{});done:=make(chan error,1);go func(){close(started);done<-f.markDelivered()}();<-started;select{case err:=<-done:t.Fatalf("delivery returned before writer release: %v",err);case<-time.After(50*time.Millisecond):}release();if err:=<-done;!errors.Is(err,ErrWorkspaceConflicted){t.Fatalf("delivery=%v",err)};if after:=f.queueProjection(t);!bytes.Equal(before,after){t.Fatal("queue changed")}
}
```

The same file imports `bytes`, `context`, `crypto/sha256`, `database/sql`, `encoding/hex`,
`encoding/json`, `errors`, `path/filepath`, `reflect`, `strings`, `testing`, and `time`, and defines
the complete fixture below. Every mutation passes through the frozen Task-3 transaction API except
the two deliberately package-local inserts that arrange the retained v8 Activity cursor and a queue
row within the *same* fault-injected transaction. The two fixture keys differ only by
`CanonicalRef`; the second key is deliberately never installed, so the CAS test proves that an
expected cursor from another ref cannot authorize the active route.

```go
var ctx = context.Background()

type v9SyncFixture struct {
	t         *testing.T
	path      string
	store     *Store
	db        *sql.DB
	repo      *WorkspaceRepo
	queue     *QueueStore
	workspace types.WorkspaceBinding
	profile   types.FabricProfile
	binding   types.FabricBinding
	firstKey  types.RemoteBindingKey
	secondKey types.RemoteBindingKey
	cursor    FabricCursorRecord

	originalOperation       projectstate.OperationV1
	originalOperationJSON   json.RawMessage
	originalOperationDigest projectstate.Digest
	originalQueue           QueueRecord
}

func newV9SyncFixture(t *testing.T) *v9SyncFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	workspace := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011",
		"/slice7-checkout", 71, 91)
	profile := types.FabricProfile{
		ProfileID:        "10000000-0000-4000-8000-000000000001",
		Alias:            "slice7-public",
		FabricInstanceID: "20000000-0000-4000-8000-000000000001",
		BaseURL:          "https://fabric.example.test",
		Mode:             types.FabricModePublic,
	}
	if err := NewFabricRouteRepo(store.DB()).CreateProfile(ctx, profile); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	binding := types.FabricBinding{
		Workspace:        workspace,
		ProfileID:        profile.ProfileID,
		FabricInstanceID: profile.FabricInstanceID,
		RemoteProjectID:  "30000000-0000-4000-8000-000000000001",
		StreamID:         "40000000-0000-4000-8000-000000000001",
		AttachmentRef:    "50000000-0000-4000-8000-000000000001",
		CanonicalRef:     "refs/heads/main",
		Writable:         true,
	}
	firstKey := binding.RemoteKey()
	secondKey := firstKey
	secondKey.CanonicalRef = "refs/heads/other"
	if err := firstKey.Validate(); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := secondKey.Validate(); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	tree := workspaceTree(t, workspace.Scope.ProjectID, workspace.Repository)
	treeDigest, err := projectstate.DigestTree(tree)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	operation := projectstate.OperationV1{
		SchemaVersion:      1,
		ID:                 "90000000-0000-4000-8000-000000000001",
		Kind:               projectstate.OperationTombstone,
		ExpectedViewDigest: treeDigest,
		Actor: types.ActorEnvelope{
			ActorKind:        types.ActorHuman,
			HumanPrincipalID: "80000000-0000-4000-8000-000000000001",
			Assurance:        types.AssuranceLocal,
			OccurredAt:       testUTCNow(),
		},
		Tombstone: &projectstate.TombstoneOperationV1{
			Key:                   projectstate.RecordKey{Kind: "task", ID: "70000000-0000-4000-8000-000000000001"},
			ExpectedContentDigest: projectstate.Digest("sha256:" + strings.Repeat("b", 64)),
		},
	}
	operationJSON, err := projectstate.CanonicalOperation(operation)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	sum := sha256.Sum256(operationJSON)
	fixture := &v9SyncFixture{
		t:         t,
		path:      path,
		store:     store,
		db:        store.DB(),
		repo:      repo,
		queue:     NewQueueStore(store.DB()),
		workspace: workspace,
		profile:   profile,
		binding:   binding,
		firstKey:  firstKey,
		secondKey: secondKey,
		cursor: FabricCursorRecord{
			Key:                firstKey,
			StreamVersion:      1,
			AcceptedCommitSHA:  workspace.AcceptedCommitSHA,
			AcceptedTree:       tree,
			AcceptedTreeDigest: treeDigest,
			LiveTree:           tree,
			LiveTreeDigest:     treeDigest,
			UpdatedAt:          testUTCNow(),
		},
		originalOperation:       operation,
		originalOperationJSON:   append(json.RawMessage(nil), operationJSON...),
		originalOperationDigest: projectstate.Digest("sha256:" + hex.EncodeToString(sum[:])),
	}
	t.Cleanup(func() {
		if fixture.store != nil {
			_ = fixture.store.Close()
		}
	})
	return fixture
}

func (f *v9SyncFixture) snapshot(t *testing.T) []byte {
	t.Helper()
	var snapshot strings.Builder
	for _, table := range []string{
		"workspace_fabric_bindings",
		"fabric_cursors",
		"activity_cursors",
		"activity_policy_versions",
		"activity_policy_current",
		"sync_queue",
		"fabric_sync_conflicts",
	} {
		columns, err := f.db.Query(`PRAGMA table_info(` + v9QuoteIdentifier(table) + `)`)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for columns.Next() {
			var ordinal, required, primaryKey int
			var name, columnType string
			var defaultValue any
			if err := columns.Scan(&ordinal, &name, &columnType, &required, &defaultValue, &primaryKey); err != nil {
				_ = columns.Close()
				t.Fatal(err)
			}
			names = append(names, name)
		}
		if err := columns.Err(); err != nil {
			_ = columns.Close()
			t.Fatal(err)
		}
		if err := columns.Close(); err != nil {
			t.Fatal(err)
		}
		if len(names) == 0 {
			t.Fatalf("no columns for %s", table)
		}
		quoted := make([]string, 0, len(names))
		ordered := make([]string, 0, len(names))
		for _, name := range names {
			identifier := v9QuoteIdentifier(name)
			quoted = append(quoted, `quote(`+identifier+`)`)
			ordered = append(ordered, identifier)
		}
		rows, err := f.db.Query(`SELECT ` + strings.Join(quoted, `,`) + ` FROM ` +
			v9QuoteIdentifier(table) + ` ORDER BY ` + strings.Join(ordered, `,`))
		if err != nil {
			t.Fatal(err)
		}
		snapshot.WriteString(table)
		snapshot.WriteByte('\n')
		for rows.Next() {
			values := make([]string, len(names))
			destinations := make([]any, len(values))
			for index := range values {
				destinations[index] = &values[index]
			}
			if err := rows.Scan(destinations...); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			snapshot.WriteString(strings.Join(values, `|`))
			snapshot.WriteByte('\n')
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return []byte(snapshot.String())
}

func v9QuoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func installFailTrigger(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	allowed := map[string]bool{
		"workspace_fabric_bindings": true,
		"fabric_cursors":            true,
		"activity_cursors":          true,
		"activity_policy_versions":  true,
		"activity_policy_current":   true,
		"sync_queue":                true,
		"fabric_sync_conflicts":     true,
	}
	if !allowed[table] {
		t.Fatalf("unexpected fault table %q", table)
	}
	name := `fail_` + table
	if _, err := db.Exec(`CREATE TRIGGER ` + v9QuoteIdentifier(name) + ` BEFORE INSERT ON ` +
		v9QuoteIdentifier(table) + ` BEGIN SELECT RAISE(ABORT,'fault'); END`); err != nil {
		t.Fatal(err)
	}
}

func applyCompleteV9Mutation(ctx context.Context, tx *WorkspaceMutationTx, f *v9SyncFixture) error {
	if err := tx.InstallFabricBinding(ctx, f.binding, f.profile); err != nil {
		return err
	}
	if err := tx.CompareAndSwapRemoteReplica(ctx, nil, f.cursor); err != nil {
		return err
	}
	if _, err := tx.conn.ExecContext(ctx, `INSERT INTO activity_cursors
		(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,after_sequence,updated_at)
		VALUES (?,?,?,?,?,?,?,?)`, f.firstKey.ProjectID, f.firstKey.WorkspaceID,
		f.firstKey.FabricInstanceID, f.firstKey.RemoteProjectID, f.firstKey.StreamID, f.firstKey.CanonicalRef,
		0, f.cursor.UpdatedAt.UTC()); err != nil {
		return err
	}
	if err := tx.InstallActivityPolicy(ctx, f.firstKey, f.enabledPolicy()); err != nil {
		return err
	}
	if _, err := tx.conn.ExecContext(ctx, `INSERT INTO sync_queue
		(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,id,
		 operation_json,operation_digest,priority,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, f.firstKey.ProjectID, f.firstKey.WorkspaceID,
		f.firstKey.FabricInstanceID, f.firstKey.RemoteProjectID, f.firstKey.StreamID, f.firstKey.CanonicalRef,
		f.originalOperation.ID, string(f.originalOperationJSON), f.originalOperationDigest, 7,
		f.cursor.UpdatedAt.UTC(), f.cursor.UpdatedAt.UTC()); err != nil {
		return err
	}
	return tx.ApplyRemoteConflict(ctx, f.firstKey, RemoteConflictConsequence{
		ConflictID:              "a0000000-0000-4000-8000-000000000001",
		OriginalOperationID:     f.originalOperation.ID,
		OriginalOperationDigest: f.originalOperationDigest,
		DetectedStreamVersion:   f.cursor.StreamVersion,
		DetectedLiveTreeDigest:  f.cursor.LiveTreeDigest,
	})
}

func (f *v9SyncFixture) reopen(t *testing.T) {
	t.Helper()
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.store = store
	f.db = store.DB()
	f.repo = NewWorkspaceRepo(f.db)
	f.queue = NewQueueStore(f.db)
}

func (f *v9SyncFixture) withTx(t *testing.T, operation func(*WorkspaceMutationTx)) {
	t.Helper()
	if err := f.repo.WithImmediateWorkspace(ctx, f.workspace.Scope, func(tx *WorkspaceMutationTx) error {
		operation(tx)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *v9SyncFixture) cas(expected *FabricCursorRecord, next FabricCursorRecord) error {
	return f.repo.WithImmediateWorkspace(ctx, f.workspace.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.CompareAndSwapRemoteReplica(ctx, expected, next)
	})
}

func (f *v9SyncFixture) installBindingAndCursor(t *testing.T) {
	t.Helper()
	f.withTx(t, func(tx *WorkspaceMutationTx) {
		if err := tx.InstallFabricBinding(ctx, f.binding, f.profile); err != nil {
			t.Fatal(err)
		}
		if err := tx.CompareAndSwapRemoteReplica(ctx, nil, f.cursor); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.conn.ExecContext(ctx, `INSERT INTO activity_cursors
			(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,after_sequence,updated_at)
			VALUES (?,?,?,?,?,?,?,?)`, f.firstKey.ProjectID, f.firstKey.WorkspaceID,
			f.firstKey.FabricInstanceID, f.firstKey.RemoteProjectID, f.firstKey.StreamID, f.firstKey.CanonicalRef,
			0, f.cursor.UpdatedAt.UTC()); err != nil {
			t.Fatal(err)
		}
	})
}

func (f *v9SyncFixture) enabledPolicy() ActivityPolicyState {
	f.t.Helper()
	policy := projectstate.EffectiveActivityPolicyV1{
		SchemaVersion:             1,
		PolicyVersion:             1,
		OrdinaryMaxAgeSeconds:     2_592_000,
		OrdinaryMaxRows:           10_000,
		TerminalDefaultAgeSeconds: 2_592_000,
		TerminalMaximumAgeSeconds: 31_536_000,
		TerminalRetentionSeconds:  2_592_000,
	}
	canonical, err := projectstate.CanonicalActivityPolicy(policy)
	if err != nil {
		f.t.Fatal(err)
	}
	digest, err := projectstate.DigestActivityPolicy(policy)
	if err != nil {
		f.t.Fatal(err)
	}
	version := policy.PolicyVersion
	return ActivityPolicyState{
		State:         "enabled",
		Policy:        &policy,
		PolicyJSON:    append(json.RawMessage(nil), canonical...),
		PolicyVersion: &version,
		PolicyDigest:  &digest,
		UpdatedAt:     f.cursor.UpdatedAt.UTC(),
	}
}

func (f *v9SyncFixture) disabledPolicy(reason string) ActivityPolicyState {
	f.t.Helper()
	if reason != "missing" && reason != "malformed" && reason != "unbounded" {
		f.t.Fatalf("invalid disabled reason %q", reason)
	}
	return ActivityPolicyState{State: "disabled", DisabledReason: reason, UpdatedAt: f.cursor.UpdatedAt.UTC()}
}

func (f *v9SyncFixture) installPolicy(t *testing.T, policy ActivityPolicyState) {
	t.Helper()
	f.withTx(t, func(tx *WorkspaceMutationTx) {
		if err := tx.InstallActivityPolicy(ctx, f.firstKey, policy); err != nil {
			t.Fatal(err)
		}
	})
}

func (f *v9SyncFixture) policy(key types.RemoteBindingKey) (ActivityPolicyState, error) {
	var result *ActivityPolicyState
	err := f.repo.WithImmediateWorkspace(ctx, f.workspace.Scope, func(tx *WorkspaceMutationTx) error {
		state, err := tx.ActivityPolicy(ctx, key)
		if err != nil {
			return err
		}
		result = state
		return nil
	})
	if err != nil {
		return ActivityPolicyState{}, err
	}
	if result == nil || result.State != "enabled" {
		return ActivityPolicyState{}, ErrActivityPolicyUnavailable
	}
	copy := *result
	copy.PolicyJSON = append(json.RawMessage(nil), result.PolicyJSON...)
	return copy, nil
}

func (f *v9SyncFixture) deleteCurrentPolicy(t *testing.T) {
	t.Helper()
	if _, err := f.db.Exec(`DELETE FROM activity_policy_current
		WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=?
		AND stream_id=? AND canonical_ref=?`, f.firstKey.ProjectID, f.firstKey.WorkspaceID,
		f.firstKey.FabricInstanceID, f.firstKey.RemoteProjectID, f.firstKey.StreamID, f.firstKey.CanonicalRef); err != nil {
		t.Fatal(err)
	}
}

func (f *v9SyncFixture) enqueueOriginal(t *testing.T) {
	t.Helper()
	entry, err := f.queue.Enqueue(ctx, f.firstKey, f.originalOperation, 7)
	if err != nil {
		t.Fatal(err)
	}
	f.originalQueue = entry
}

func (f *v9SyncFixture) insertOpenConflict(t *testing.T) {
	t.Helper()
	f.withTx(t, func(tx *WorkspaceMutationTx) {
		if err := tx.ApplyRemoteConflict(ctx, f.firstKey, RemoteConflictConsequence{
			ConflictID:              "a0000000-0000-4000-8000-000000000001",
			OriginalOperationID:     f.originalQueue.ID,
			OriginalOperationDigest: f.originalQueue.OperationDigest,
			DetectedStreamVersion:   f.cursor.StreamVersion,
			DetectedLiveTreeDigest:  f.cursor.LiveTreeDigest,
		}); err != nil {
			t.Fatal(err)
		}
	})
}

func (f *v9SyncFixture) resolution() RemoteConflictConsequence {
	f.t.Helper()
	operation := f.originalOperation
	operation.ID = "90000000-0000-4000-8000-000000000002"
	canonical, err := projectstate.CanonicalOperation(operation)
	if err != nil {
		f.t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	return RemoteConflictConsequence{
		ConflictID:                "a0000000-0000-4000-8000-000000000001",
		OriginalOperationID:       f.originalQueue.ID,
		OriginalOperationDigest:   f.originalQueue.OperationDigest,
		DetectedStreamVersion:     f.cursor.StreamVersion,
		DetectedLiveTreeDigest:    f.cursor.LiveTreeDigest,
		ResolutionOperationID:     operation.ID,
		ResolutionOperationJSON:   append(json.RawMessage(nil), canonical...),
		ResolutionOperationDigest: projectstate.Digest("sha256:" + hex.EncodeToString(sum[:])),
	}
}

func (f *v9SyncFixture) prepareResolution(consequence RemoteConflictConsequence) error {
	return f.repo.WithImmediateWorkspace(ctx, f.workspace.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.ApplyRemoteConflict(ctx, f.firstKey, consequence)
	})
}

func (f *v9SyncFixture) conflict(t *testing.T) RemoteConflictRecord {
	t.Helper()
	var result RemoteConflictRecord
	f.withTx(t, func(tx *WorkspaceMutationTx) {
		records, err := tx.RemoteConflicts(ctx, f.firstKey)
		if err != nil {
			t.Fatal(err)
		}
		if len(records) != 1 {
			t.Fatalf("remote conflicts=%d, want 1", len(records))
		}
		result = records[0]
	})
	return result
}

func (f *v9SyncFixture) queueProjection(t *testing.T) []byte {
	t.Helper()
	rows, err := f.db.Query(`SELECT quote(project_id),quote(workspace_id),quote(fabric_instance_id),
		quote(remote_project_id),quote(stream_id),quote(canonical_ref),quote(id),quote(operation_json),
		quote(operation_digest),quote(priority),quote(created_at),quote(updated_at),quote(delivered_at)
		FROM sync_queue WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=?
		AND stream_id=? AND canonical_ref=? ORDER BY id`, f.firstKey.ProjectID, f.firstKey.WorkspaceID,
		f.firstKey.FabricInstanceID, f.firstKey.RemoteProjectID, f.firstKey.StreamID, f.firstKey.CanonicalRef)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var projection strings.Builder
	for rows.Next() {
		values := make([]string, 13)
		destinations := make([]any, len(values))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			t.Fatal(err)
		}
		projection.WriteString(strings.Join(values, `|`))
		projection.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return []byte(projection.String())
}

func (f *v9SyncFixture) holdWorkspaceConflictWriter(t *testing.T) func() {
	t.Helper()
	release := make(chan struct{})
	ready := make(chan error, 1)
	done := make(chan error, 1)
	go func() {
		done <- f.repo.WithImmediateWorkspace(ctx, f.workspace.Scope, func(tx *WorkspaceMutationTx) error {
			if _, err := tx.conn.ExecContext(ctx, `INSERT INTO workspace_conflicts
				(project_id,workspace_id,occurrence_id,conflict_id,record_kind,record_id,field_path,conflict_kind,
				 base_json,ours_json,theirs_json,state,resolved_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,NULL)`, f.workspace.Scope.ProjectID, f.workspace.Scope.WorkspaceID,
				"b0000000-0000-4000-8000-000000000001", "b0000000-0000-4000-8000-000000000002",
				"task", "70000000-0000-4000-8000-000000000001", "title", "changed", "{}", "{}", "{}", "open"); err != nil {
				ready <- err
				return err
			}
			ready <- nil
			<-release
			return nil
		})
	}()
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	return func() {
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func (f *v9SyncFixture) markDelivered() error {
	return f.queue.MarkDelivered(ctx, f.firstKey, f.originalQueue.ID)
}
```

- [ ] **Step 4: Implement the exact localstore records and SQL.**

Create `fabric_sync_state.go` with the frozen types plus:

```go
var ErrFabricCursorConflict=errors.New("localstore: Fabric cursor conflict")
var ErrRemoteSyncConflict=errors.New("localstore: remote sync conflict")
type RemoteConflictRecord struct{Key types.RemoteBindingKey;ConflictID,OriginalOperationID string;OriginalOperationDigest projectstate.Digest;DetectedStreamVersion int64;DetectedLiveTreeDigest projectstate.Digest;State,ResolutionIntentState string;CreatedAt time.Time;ResolutionOperationID string;ResolutionOperationJSON json.RawMessage;ResolutionOperationDigest projectstate.Digest;ResolvedAt *time.Time}

func (tx *WorkspaceMutationTx) RemoteReplica(ctx context.Context,key types.RemoteBindingKey)(*FabricCursorRecord,error){
	if tx==nil||tx.conn==nil||key.Validate()!=nil||key.ProjectID!=tx.scope.ProjectID||key.WorkspaceID!=tx.scope.WorkspaceID{return nil,ErrNotFound}
	var r FabricCursorRecord;var accepted,live []byte;var acceptedDigest,liveDigest string
	err:=tx.conn.QueryRowContext(ctx,`SELECT stream_version,accepted_commit_sha,accepted_tree,accepted_tree_digest,live_tree,live_tree_digest,updated_at FROM fabric_cursors WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=? AND stream_id=? AND canonical_ref=?`,routeArgs(key)...).Scan(&r.StreamVersion,&r.AcceptedCommitSHA,&accepted,&acceptedDigest,&live,&liveDigest,&r.UpdatedAt)
	if errors.Is(err,sql.ErrNoRows){return nil,nil};if err!=nil{return nil,fmt.Errorf("localstore: read remote replica: %w",err)};r.Key=key;r.AcceptedTree,err=decodeFileList(accepted);if err!=nil{return nil,ErrFabricCursorConflict};r.LiveTree,err=decodeFileList(live);if err!=nil{return nil,ErrFabricCursorConflict};r.AcceptedTreeDigest=projectstate.Digest(acceptedDigest);r.LiveTreeDigest=projectstate.Digest(liveDigest);if validateCursor(r)!=nil{return nil,ErrFabricCursorConflict};return cloneCursor(&r),nil
}

func (tx *WorkspaceMutationTx) CompareAndSwapRemoteReplica(ctx context.Context,expected *FabricCursorRecord,next FabricCursorRecord)error{
	if tx==nil||tx.conn==nil||validateCursor(next)!=nil||next.Key.ProjectID!=tx.scope.ProjectID||next.Key.WorkspaceID!=tx.scope.WorkspaceID{return ErrFabricCursorConflict};accepted,_:=encodeFileList(next.AcceptedTree);live,_:=encodeFileList(next.LiveTree);args:=routeArgs(next.Key)
	if expected==nil{_,err:=tx.conn.ExecContext(ctx,`INSERT INTO fabric_cursors(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,stream_version,accepted_commit_sha,accepted_tree,accepted_tree_digest,live_tree,live_tree_digest,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,append(args,next.StreamVersion,next.AcceptedCommitSHA,accepted,next.AcceptedTreeDigest,live,next.LiveTreeDigest,next.UpdatedAt.UTC())...);if err!=nil{return ErrFabricCursorConflict};return nil}
	if validateCursor(*expected)!=nil||expected.Key!=next.Key||next.StreamVersion<expected.StreamVersion{return ErrFabricCursorConflict};oldAccepted,_:=encodeFileList(expected.AcceptedTree);oldLive,_:=encodeFileList(expected.LiveTree)
	result,err:=tx.conn.ExecContext(ctx,`UPDATE fabric_cursors SET stream_version=?,accepted_commit_sha=?,accepted_tree=?,accepted_tree_digest=?,live_tree=?,live_tree_digest=?,updated_at=? WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=? AND stream_id=? AND canonical_ref=? AND stream_version=? AND accepted_commit_sha=? AND accepted_tree=? AND accepted_tree_digest=? AND live_tree=? AND live_tree_digest=? AND updated_at=?`,next.StreamVersion,next.AcceptedCommitSHA,accepted,next.AcceptedTreeDigest,live,next.LiveTreeDigest,next.UpdatedAt.UTC(),args[0],args[1],args[2],args[3],args[4],args[5],expected.StreamVersion,expected.AcceptedCommitSHA,oldAccepted,expected.AcceptedTreeDigest,oldLive,expected.LiveTreeDigest,expected.UpdatedAt.UTC());if err!=nil{return fmt.Errorf("localstore: CAS remote replica: %w",err)};n,_:=result.RowsAffected();if n!=1{return ErrFabricCursorConflict};return nil
}

func (tx *WorkspaceMutationTx) InstallActivityPolicy(ctx context.Context,key types.RemoteBindingKey,p ActivityPolicyState)error{
	if validatePolicyState(p)!=nil||key.Validate()!=nil||key.ProjectID!=tx.scope.ProjectID||key.WorkspaceID!=tx.scope.WorkspaceID{return ErrActivityPolicyUnavailable};args:=routeArgs(key)
	if p.State=="enabled"{if _,err:=tx.conn.ExecContext(ctx,`INSERT INTO activity_policy_versions(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,policy_version,canonical_policy_json,policy_digest,terminal_retention_seconds,received_at) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,append(args,*p.PolicyVersion,bytes.Clone(p.PolicyJSON),*p.PolicyDigest,p.Policy.TerminalRetentionSeconds,p.UpdatedAt.UTC())...);err!=nil{return err}}
	var version,digest,reason any;if p.State=="enabled"{version=*p.PolicyVersion;digest=*p.PolicyDigest}else{reason=p.DisabledReason}
	_,err:=tx.conn.ExecContext(ctx,`INSERT INTO activity_policy_current(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,state,policy_version,policy_digest,disabled_reason,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref) DO UPDATE SET state=excluded.state,policy_version=excluded.policy_version,policy_digest=excluded.policy_digest,disabled_reason=excluded.disabled_reason,updated_at=excluded.updated_at`,append(args,p.State,version,digest,reason,p.UpdatedAt.UTC())...);return err
}

func (tx *WorkspaceMutationTx) ApplyQueueConsequence(ctx context.Context,key types.RemoteBindingKey,c QueueConsequence)error{
	record,err:=tx.QueueEntry(ctx,key,c.OperationID);if err!=nil{return err};if !bytes.Equal(record.OperationJSON,c.ExpectedOperationJSON)||record.OperationDigest!=c.ExpectedOperationDigest{return ErrQueueNotFound};if c.Disposition==QueueUnchanged{return nil};if c.Disposition!=QueueDelivered{return ErrQueueNotFound};open,err:=tx.HasOpenConflicts(ctx);if err!=nil{return err};if open{return ErrWorkspaceConflicted};result,err:=tx.conn.ExecContext(ctx,`UPDATE sync_queue SET delivered_at=COALESCE(delivered_at,CURRENT_TIMESTAMP),updated_at=CASE WHEN delivered_at IS NULL THEN CURRENT_TIMESTAMP ELSE updated_at END WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=? AND stream_id=? AND canonical_ref=? AND id=? AND operation_json=? AND operation_digest=?`,append(routeArgs(key),c.OperationID,string(c.ExpectedOperationJSON),c.ExpectedOperationDigest)...);if err!=nil{return err};n,_:=result.RowsAffected();if n!=1{return ErrQueueNotFound};return nil
}
```

`InstallFabricBinding` exact-compares the transaction workspace and stored profile, then inserts the
single active binding; it never creates a cursor. `ApplyRemoteConflict` reads the exact existing row
first: absent accepts only `none`; exact replay is a no-op; `none -> prepared` updates only the three
canonical resolution fields; `prepared -> resolved` requires byte-identical ID/bytes/digest and
sets both states plus `resolved_at`; every other transition returns `ErrRemoteSyncConflict`.
All predicates bind `routeArgs(key)` in this exact order:

```go
func routeArgs(k types.RemoteBindingKey)[]any{return []any{k.ProjectID,k.WorkspaceID,k.FabricInstanceID,k.RemoteProjectID,k.StreamID,k.CanonicalRef}}
```

Move all queue SQL into `localstore.QueueStore`; its methods are the frozen signatures. `sync.QueueRepo`
becomes a no-SQL wrapper and type aliases `QueueEntry=localstore.QueueRecord`. `MarkDelivered` calls
`WorkspaceRepo.WithImmediateWorkspace`, then `QueueEntry`, then `ApplyQueueConsequence`. Add
`CanonicalRef` to every `sync_audit` select/insert/scan as well.

Delete `FabricRouteRepo.UpdateCursor`. Replace the old synthetic-zero attach with
`AttachWorkspace(ctx,binding,initialCursor,initialPolicy)`, implemented as one
`WithImmediateWorkspace` calling `InstallFabricBinding`, `CompareAndSwapRemoteReplica(nil,...)`,
zero `activity_cursors` insert, and `InstallActivityPolicy`; Task 4 uses transaction methods directly.

- [ ] **Step 5: Advance exact-format docs/tests, verify, commit, and review.**

Update all current format inventories and docs to v9, former v8 refusal, six-key audit/queue/cursor,
explicit disabled policy, remote resolution intent, final Fabric migration `000022`, and private
identity first `000023`.

```bash
go test ./internal/runtime/localstore ./internal/runtime/sync ./internal/types -run 'Test(Private|FreshPrivate|ExactPrivate|RemoteBindingKey|FabricRoute|Queue|ActivityPolicy|RemoteConflict|CompleteCursor|FabricSyncMutation)' -count=1
go test -race ./internal/runtime/localstore ./internal/runtime/sync -run 'Test(Queue|FabricRoute|RemoteConflict|CompleteCursor|FabricSyncMutation)' -count=1
go vet ./internal/runtime/localstore ./internal/runtime/sync ./internal/types
rg -n 'private_schema_v8|privateSchemaV8|SchemaV8|GatewaySchemaVersion = 8' internal/runtime/localstore --glob '!testdata/private_schema_v8.sql'
```

The final `rg` must have no match. Stage and checkpoint exactly:

```bash
git add internal/runtime/localstore/private_schema_v9.sql internal/runtime/localstore/testdata/private_schema_v8.sql \
  internal/runtime/localstore/migrations.go internal/runtime/localstore/private_format.go internal/runtime/localstore/private_format_test.go \
  internal/runtime/localstore/migrations_test.go internal/runtime/localstore/activity_format_v8_test.go internal/runtime/localstore/activity_format_v9_test.go \
  internal/runtime/localstore/fabric_sync_state.go internal/runtime/localstore/fabric_sync_state_test.go \
  internal/runtime/localstore/activity_policy_repo.go internal/runtime/localstore/activity_policy_repo_test.go \
  internal/runtime/localstore/fabric_routes.go internal/runtime/localstore/fabric_routes_test.go internal/runtime/localstore/workspace_repo.go \
  internal/runtime/localstore/r06_authority_test.go internal/runtime/localstore/coverage_contract_test.go \
  internal/runtime/sync/queue_repo.go internal/runtime/sync/queue_repo_test.go internal/types/routing.go internal/types/routing_test.go \
  docs/db-entities.md README.md
git diff --cached --name-only
git commit -m "feat: hard-cut Gateway schema v9"
TASK_3_HEAD=$(git rev-parse HEAD)
```

Review `TASK_3_BASE..TASK_3_HEAD` to C0/I0. Review repairs are a distinct
`fix: address Slice 7 schema review` commit followed by all Step-5 gates.

### Task 4: ProjectState Remote-Replica Import

**Files:**
- Create: `internal/runtime/projectstate/remote_import.go`
- Create: `internal/runtime/projectstate/remote_import_test.go`
- Modify: `internal/runtime/projectstate/service.go`
- Modify: `internal/runtime/projectstate/transition_coordinator.go`
- Modify: `internal/runtime/projectstate/git_observer.go`
- Modify: `internal/runtime/projectstate/git_observer_test.go`
- Modify: `internal/runtime/projectstate/architecture_test.go`
- Modify: `internal/runtime/localstore/workspace_repo.go`
- Modify: `internal/runtime/localstore/workspace_repo_test.go`
- Modify: `internal/runtime/localstore/workspace_commit_confirmation.go`
- Modify: `internal/runtime/localstore/workspace_commit_confirmation_test.go`
- Modify: `internal/types/identity.go`
- Modify: `internal/types/identity_test.go`

**Consumes:** Task 3's complete six-key route/cursor/policy/queue/conflict transaction
methods, existing `loadComposedWorkspace`, `ThreeWayRebase`, candidate and semantic
conflict codecs.

**Produces:** one read-only attempt capture, alias-based façade methods, one direct
`transitionCoordinator` delegation for each method, exact unknown-COMMIT confirmation,
and Git-observer reconciliation.  It creates no seventh coordinator and imports no
`runtime/sync` package.

#### Frozen Task-4 values and interfaces

Add these localstore-owned values beside the existing Task-3 request values:

```go
// internal/runtime/localstore/fabric_sync_state.go
type RemoteAttemptCapture struct {
	Workspace             WorkspaceRecord
	ViewDigest            projectstate.Digest
	OverlayGeneration     int64
	CandidatePresent      bool
	CandidateDigest       projectstate.Digest
	WorkspaceState        string
	OpenConflictIDs       []string
	RoutePresent          bool
	Route                 types.FabricBinding
	Profile               types.FabricProfile
	Cursor                *FabricCursorRecord
	ActivityPolicy        *ActivityPolicyState
	OpenRemoteConflicts   []RemoteConflictRecord
}

type RemoteConflictRecord struct {
	Key                       types.RemoteBindingKey
	ConflictID                string
	OriginalOperationID       string
	OriginalOperationDigest   projectstate.Digest
	DetectedStreamVersion     int64
	DetectedLiveTreeDigest    projectstate.Digest
	State                     string // open|resolved
	ResolutionIntentState     string // none|prepared|resolved
	ResolutionOperationID     string
	ResolutionOperationJSON   json.RawMessage
	ResolutionOperationDigest projectstate.Digest
	CreatedAt                 time.Time
	ResolvedAt                *time.Time
}
```

Task 3 must expose read-only transaction accessors used by the capture and confirmation
code.  They return owned copies; route absence is `(zero,zero,false,nil)` and policy or
cursor absence under a present route is corruption.

```go
func (tx *WorkspaceMutationTx) FabricRoute(context.Context) (types.FabricBinding, types.FabricProfile, bool, error)
func (tx *WorkspaceMutationTx) RemoteReplica(context.Context, types.RemoteBindingKey) (*FabricCursorRecord, error)
func (tx *WorkspaceMutationTx) ActivityPolicy(context.Context, types.RemoteBindingKey) (*ActivityPolicyState, error)
func (tx *WorkspaceMutationTx) ActivityCursor(context.Context, types.RemoteBindingKey) (int64, bool, error)
func (tx *WorkspaceMutationTx) RemoteConflicts(context.Context, types.RemoteBindingKey) ([]RemoteConflictRecord, error)
func (tx *WorkspaceMutationTx) QueueEntry(context.Context, types.RemoteBindingKey, string) (QueueRecord, error)
```

The façade and narrow importer interface become:

```go
// internal/runtime/projectstate/remote_import.go
type RemoteAttemptCapture = localstore.RemoteAttemptCapture
type ImportAcceptedTreeRequest = localstore.RemoteReplicaImportRequest
type ImportAcceptedTreeResult = localstore.RemoteReplicaImportResult
type PrepareRemoteResolutionRequest = localstore.PrepareRemoteResolutionRequest
type PrepareRemoteResolutionResult = localstore.PrepareRemoteResolutionResult

func (s *Service) CaptureRemoteAttempt(ctx context.Context, scope types.WorkspaceScope) (RemoteAttemptCapture, error) {
	if s == nil || s.transition == nil {
		return RemoteAttemptCapture{}, localstore.ErrNotFound
	}
	return s.transition.captureRemoteAttempt(ctx, scope)
}

func (s *Service) ImportAcceptedTree(ctx context.Context, req ImportAcceptedTreeRequest) (ImportAcceptedTreeResult, error) {
	if s == nil || s.transition == nil {
		return ImportAcceptedTreeResult{}, localstore.ErrNotFound
	}
	return s.transition.importAcceptedTree(ctx, req)
}

func (s *Service) PrepareRemoteResolution(ctx context.Context, req PrepareRemoteResolutionRequest) (PrepareRemoteResolutionResult, error) {
	if s == nil || s.transition == nil {
		return PrepareRemoteResolutionResult{}, localstore.ErrNotFound
	}
	return s.transition.prepareRemoteResolution(ctx, req)
}
```

```go
// internal/runtime/sync/contract_v2.go, consumed by Task 5
type RemoteReplicaImporter interface {
	CaptureRemoteAttempt(context.Context, types.WorkspaceScope) (localstore.RemoteAttemptCapture, error)
	ImportAcceptedTree(context.Context, localstore.RemoteReplicaImportRequest) (localstore.RemoteReplicaImportResult, error)
	PrepareRemoteResolution(context.Context, localstore.PrepareRemoteResolutionRequest) (localstore.PrepareRemoteResolutionResult, error)
}
```

- [ ] **Step 1: Capture the base and add complete façade/architecture RED tests.**

```bash
TASK_4_BASE=$(git rev-parse HEAD)
go test ./internal/runtime/projectstate -run 'Test(ServiceRemoteReplicaFacade|ProjectStateArchitecture)' -count=1
```

Append this compile-time and behavioral suite to `remote_import_test.go`:

```go
func TestServiceRemoteReplicaFacade(t *testing.T) {
	var _ interface {
		CaptureRemoteAttempt(context.Context, types.WorkspaceScope) (localstore.RemoteAttemptCapture, error)
		ImportAcceptedTree(context.Context, localstore.RemoteReplicaImportRequest) (localstore.RemoteReplicaImportResult, error)
		PrepareRemoteResolution(context.Context, localstore.PrepareRemoteResolutionRequest) (localstore.PrepareRemoteResolutionResult, error)
	} = (*Service)(nil)

	var nilService *Service
	if _, err := nilService.CaptureRemoteAttempt(t.Context(), types.WorkspaceScope{}); !errors.Is(err, localstore.ErrNotFound) {
		t.Fatalf("CaptureRemoteAttempt nil error = %v", err)
	}
	if _, err := nilService.ImportAcceptedTree(t.Context(), localstore.RemoteReplicaImportRequest{}); !errors.Is(err, localstore.ErrNotFound) {
		t.Fatalf("ImportAcceptedTree nil error = %v", err)
	}
	if _, err := nilService.PrepareRemoteResolution(t.Context(), localstore.PrepareRemoteResolutionRequest{}); !errors.Is(err, localstore.ErrNotFound) {
		t.Fatalf("PrepareRemoteResolution nil error = %v", err)
	}
}
```

Extend `TestProjectStateArchitecture` to parse `Service`, require exactly the existing six
pointer fields, require each façade body to contain one `s.transition.<method>` call, and
reject `internal/runtime/sync` imports from every projectstate production file.

- [ ] **Step 2: Add attempt-capture RED tests, then implement the read-only capture.**

```go
func TestCaptureRemoteAttemptOwnsExactCoherentState(t *testing.T) {
	fixture := newRemoteImportFixture(t, remoteFixtureOngoing)
	got, err := fixture.service.CaptureRemoteAttempt(t.Context(), fixture.binding.Scope)
	if err != nil { t.Fatal(err) }
	if got.Workspace.Binding != fixture.binding || !got.RoutePresent || got.Route != fixture.route || got.Profile != fixture.profile {
		t.Fatalf("capture identity = %+v", got)
	}
	if got.Cursor == nil || !reflect.DeepEqual(*got.Cursor, fixture.cursor) || got.ActivityPolicy == nil || !reflect.DeepEqual(*got.ActivityPolicy, fixture.policy) {
		t.Fatalf("capture remote state = %+v", got)
	}
	wantView, err := fixture.service.View(t.Context(), fixture.binding.Scope)
	if err != nil { t.Fatal(err) }
	if got.ViewDigest != wantView.Snapshot.Digest || got.OverlayGeneration != wantView.ThroughGeneration {
		t.Fatalf("capture view = %+v, want %+v", got, wantView)
	}
	if got.OpenConflictIDs == nil || got.OpenRemoteConflicts == nil { t.Fatal("nil owned slices") }
	got.Cursor.LiveTree[0].Content[0] ^= 1
	again, err := fixture.service.CaptureRemoteAttempt(t.Context(), fixture.binding.Scope)
	if err != nil { t.Fatal(err) }
	if reflect.DeepEqual(got.Cursor.LiveTree, again.Cursor.LiveTree) { t.Fatal("capture aliases repository memory") }
}

func TestCaptureRemoteAttemptRejectsIncompleteRouteState(t *testing.T) {
	for _, statement := range []string{
		`DELETE FROM fabric_cursors`,
		`DELETE FROM activity_policy_current`,
		`UPDATE fabric_cursors SET live_tree_digest='sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'`,
	} {
		t.Run(statement, func(t *testing.T) {
			fixture := newRemoteImportFixture(t, remoteFixtureOngoing)
			if _, err := fixture.store.DB().Exec(statement); err != nil { t.Fatal(err) }
			if got, err := fixture.service.CaptureRemoteAttempt(t.Context(), fixture.binding.Scope); err == nil || got != (localstore.RemoteAttemptCapture{}) {
				t.Fatalf("capture = %#v, %v", got, err)
			}
		})
	}
}
```

Production body:

```go
func (c *transitionCoordinator) captureRemoteAttempt(ctx context.Context, scope types.WorkspaceScope) (localstore.RemoteAttemptCapture, error) {
	if c == nil || c.repo == nil || c.withImmediateWorkspace == nil {
		return localstore.RemoteAttemptCapture{}, localstore.ErrNotFound
	}
	var result localstore.RemoteAttemptCapture
	err := c.withImmediateWorkspace(ctx, scope, func(tx *localstore.WorkspaceMutationTx) error {
		loaded, err := loadComposedWorkspace(ctx, tx)
		if err != nil { return err }
		rows, err := tx.OpenConflictOccurrences(ctx)
		if err != nil { return err }
		conflicts, err := decodeWorkspaceConflictOccurrences(rows)
		if err != nil { return err }
		ids := make([]string, len(conflicts))
		for i := range conflicts { ids[i] = conflicts[i].ID }
		route, profile, present, err := tx.FabricRoute(ctx)
		if err != nil { return err }
		result = localstore.RemoteAttemptCapture{
			Workspace: loaded.statusRecord(), ViewDigest: loaded.view.Snapshot.Digest,
			OverlayGeneration: loaded.view.ThroughGeneration,
			CandidatePresent: loaded.status.CandidatePresent, CandidateDigest: loaded.status.CandidateDigest,
			WorkspaceState: loaded.status.State, OpenConflictIDs: ids,
			RoutePresent: present, Route: route, Profile: profile,
			OpenRemoteConflicts: []localstore.RemoteConflictRecord{},
		}
		if !present { return nil }
		cursor, err := tx.RemoteReplica(ctx, route.RemoteKey())
		if err != nil || cursor == nil {
			if err == nil { err = ErrAcceptedTreeEvidence }
			return err
		}
		policy, err := tx.ActivityPolicy(ctx, route.RemoteKey())
		if err != nil || policy == nil {
			if err == nil { err = ErrAcceptedTreeEvidence }
			return err
		}
		remoteConflicts, err := tx.RemoteConflicts(ctx, route.RemoteKey())
		if err != nil { return err }
		result.Cursor = cloneFabricCursor(cursor)
		result.ActivityPolicy = cloneActivityPolicy(policy)
		result.OpenRemoteConflicts = cloneRemoteConflicts(remoteConflicts)
		return nil
	})
	if err != nil { return localstore.RemoteAttemptCapture{}, err }
	return cloneRemoteAttemptCapture(result), nil
}
```

Add `record localstore.WorkspaceRecord` to `composedWorkspace` and set it in
`loadComposedWorkspaceRecord`; `statusRecord()` above is the literal method
`func (v composedWorkspace) statusRecord() localstore.WorkspaceRecord { return v.record }`.
This avoids a second transaction read and keeps the capture coherent.

- [ ] **Step 3: Add the complete validation/merge RED table.**

```go
func TestImportAcceptedTreeMatrix(t *testing.T) {
	tests := []struct {
		name string
		mode remoteFixtureMode
		mutate func(*localstore.RemoteReplicaImportRequest)
		wantErr error
		wantState string
		wantChanged bool
	}{
		{"initial clean", remoteFixtureInitial, nil, nil, "pending", true},
		{"pull clean", remoteFixtureOngoing, nil, nil, "pending", true},
		{"exact replay", remoteFixtureReplay, nil, nil, "pending", false},
		{"semantic conflict", remoteFixtureConflict, nil, nil, "conflicted", true},
		{"opaque remote conflict without semantic conflict", remoteFixtureOpaqueConflict, nil, nil, "pending", true},
		{"version regression", remoteFixtureOngoing, func(r *localstore.RemoteReplicaImportRequest) { r.RemoteState.StreamVersion = r.ExpectedCursor.StreamVersion-1 }, projectstate.ErrAcceptedTreeEvidence, "", false},
		{"same version divergence", remoteFixtureReplay, func(r *localstore.RemoteReplicaImportRequest) { r.RemoteState.LiveTreeDigest = digestA }, localstore.ErrFabricCursorConflict, "", false},
		{"wrong project", remoteFixtureOngoing, func(r *localstore.RemoteReplicaImportRequest) { r.Scope.ProjectID = projectB }, projectstate.ErrAcceptedTreeImportDrift, "", false},
		{"wrong repository", remoteFixtureOngoing, func(r *localstore.RemoteReplicaImportRequest) { r.Route.Workspace.Repository = repositoryB }, projectstate.ErrAcceptedTreeEvidence, "", false},
		{"malformed live tree", remoteFixtureOngoing, func(r *localstore.RemoteReplicaImportRequest) { r.RemoteState.LiveTree[0].Content = []byte("{") }, projectstate.ErrAcceptedTreeEvidence, "", false},
		{"changed local view", remoteFixtureOngoing, func(r *localstore.RemoteReplicaImportRequest) { r.ExpectedViewDigest = digestA }, projectstate.ErrAcceptedTreeImportDrift, "", false},
		{"changed policy", remoteFixtureOngoing, func(r *localstore.RemoteReplicaImportRequest) { r.ExpectedActivityPolicy.DisabledReason = "missing" }, projectstate.ErrAcceptedTreeImportDrift, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newRemoteImportFixture(t, tt.mode)
			req := fixture.request(t)
			if tt.mutate != nil { tt.mutate(&req) }
			before := captureAllRemoteImportRows(t, fixture.store)
			got, err := fixture.service.ImportAcceptedTree(t.Context(), req)
			if !errors.Is(err, tt.wantErr) { t.Fatalf("error = %v, want %v", err, tt.wantErr) }
			if tt.wantErr != nil {
				if after := captureAllRemoteImportRows(t, fixture.store); !reflect.DeepEqual(after, before) { t.Fatal("failed import mutated state") }
				return
			}
			if got.WorkspaceState != tt.wantState || got.Changed != tt.wantChanged { t.Fatalf("result = %+v", got) }
			workspace, err := fixture.repo.Workspace(t.Context(), fixture.binding.Scope)
			if err != nil { t.Fatal(err) }
			if workspace.Binding.AcceptedRef != fixture.binding.AcceptedRef || workspace.Binding.AcceptedCommitSHA != fixture.binding.AcceptedCommitSHA || workspace.Binding.AcceptedTreeDigest != fixture.binding.AcceptedTreeDigest {
				t.Fatal("Fabric import changed Git-owned accepted base")
			}
		})
	}
}
```

`newRemoteImportFixture`, `request`, and `captureAllRemoteImportRows` are complete test
helpers in this file: they open a real schema-v9 SQLite database, register a real Git
fixture through `openProjectStateServiceAt`/`registerGitRepository`, insert Task-3
profile/route/cursor/policy records using exported repository methods, derive B/C trees
with `state.EncodeTree`/`state.DigestTree`, and return canonical row strings ordered by
every primary key.  They must not mock `WorkspaceMutationTx` or the merge codecs.

- [ ] **Step 4: Implement the central import transaction and exact helpers.**

Add these errors beside the existing ProjectState errors:

```go
var ErrAcceptedTreeEvidence = errors.New("projectstate: invalid accepted-tree evidence")
var ErrAcceptedTreeImportDrift = errors.New("projectstate: accepted-tree import drift")
var ErrRemoteResolutionReplay = errors.New("projectstate: remote resolution replay conflict")
var ErrRemoteImportAttention = errors.New("projectstate: remote import attention required")
const fabricSyncCandidateOriginV2 = "system:fabric-sync-v2"
```

Add the origin constant to `types.ValidCandidateImportOrigin` in Task 4 and test the
closed union.  The complete coordinator method is:

```go
func (c *transitionCoordinator) importAcceptedTree(ctx context.Context, input localstore.RemoteReplicaImportRequest) (localstore.RemoteReplicaImportResult, error) {
	if c == nil || c.repo == nil || c.withImmediateWorkspace == nil || c.now == nil {
		return localstore.RemoteReplicaImportResult{}, localstore.ErrNotFound
	}
	req, err := cloneAndValidateRemoteImportRequest(input)
	if err != nil { return localstore.RemoteReplicaImportResult{}, err }
	remoteAccepted, remoteLive, err := decodeRemoteState(req)
	if err != nil { return localstore.RemoteReplicaImportResult{}, err }

	var result localstore.RemoteReplicaImportResult
	var prior, next localstore.WorkspaceCommitConfirmation
	err = c.withImmediateWorkspace(ctx, req.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		loaded, err := loadComposedWorkspace(ctx, tx)
		if err != nil { return err }
		if err := requireRemoteAttemptPredicates(ctx, tx, loaded, req); err != nil { return err }
		prior, err = tx.CaptureRemoteImportCommitConfirmation(ctx, req)
		if err != nil { return err }
		if exactReplicaReplay(req) && req.Queue == nil && req.RemoteConflict == nil {
			result = localstore.RemoteReplicaImportResult{
				WorkspaceState: loaded.status.State,
				LocalAcceptedDigest: loaded.record.Snapshot.Digest,
				ComposedViewDigest: loaded.view.Snapshot.Digest,
				RemoteCursorVersion: req.ExpectedCursor.StreamVersion,
				SemanticConflictIDs: append([]string(nil), req.ExpectedOpenConflictIDs...),
				QueueDisposition: localstore.QueueUnchanged, Changed: false,
			}
			next = prior
			return nil
		}

		var oldBase state.Snapshot
		if req.ExpectedCursor == nil {
			oldBase, err = cloneImportSnapshot(loaded.record.Snapshot)
		} else {
			oldBase, err = decodeExactRemoteTree(req.ExpectedCursor.LiveTree, req.ExpectedCursor.LiveTreeDigest, req.Route)
		}
		if err != nil { return err }
		merged, err := ThreeWayRebase(oldBase, remoteLive, loaded.view.Snapshot)
		if err != nil { return fmt.Errorf("%w: merge: %v", ErrAcceptedTreeEvidence, err) }
		evidence, err := encodeWorkspaceConflictEvidence(merged.Conflicts)
		if err != nil { return err }

		rebasePerformed := oldBase.Digest != remoteLive.Digest
		activeRows, err := tx.ActiveOperationsAfter(ctx, loaded.boundary)
		if err != nil { return err }
		if rebasePerformed {
			if err := tx.TransitionOperations(ctx, activeRows, "rebased", nil); err != nil { return err }
		}
		if _, err := tx.ReplaceOpenConflictOccurrences(ctx, evidence, c.now().UTC()); err != nil { return err }

		remoteLinkOpen := req.RemoteConflict != nil && !req.RemoteConflict.Resolve
		keepCandidate := merged.Snapshot.Digest != loaded.record.Snapshot.Digest || len(activeRows) != 0 || len(merged.Conflicts) != 0 || remoteLinkOpen
		if keepCandidate {
			direct, err := cloneImportSnapshot(remoteLive)
			if err != nil { return err }
			rebased, err := cloneImportSnapshot(merged.Snapshot)
			if err != nil { return err }
			if err := tx.UpsertCandidate(ctx, localstore.WorkspaceCandidateRecord{
				AcceptedBaseDigest: state.Digest(loaded.record.Binding.AcceptedTreeDigest),
				WorkingTreeDigest: remoteLive.Digest,
				DirectSnapshot: direct, RebasedSnapshot: &rebased,
				RebasedThroughGeneration: loaded.view.ThroughGeneration,
				ImportedBy: fabricSyncCandidateOriginV2, ImportedAt: c.now().UTC(),
			}); err != nil { return err }
		} else if err := tx.DeleteCandidate(ctx, loaded.status.CandidatePresent); err != nil {
			return err
		}

		if req.ExpectedRoutePresent {
			if err := tx.CompareAndSwapRemoteReplica(ctx, req.ExpectedCursor, cursorFromRemote(req.Route.RemoteKey(), req.RemoteState, c.now().UTC())); err != nil { return err }
		} else {
			if err := tx.InstallFabricBinding(ctx, req.Route, req.Profile); err != nil { return err }
			if err := tx.CompareAndSwapRemoteReplica(ctx, nil, cursorFromRemote(req.Route.RemoteKey(), req.RemoteState, c.now().UTC())); err != nil { return err }
			if err := tx.InstallActivityCursor(ctx, req.Route.RemoteKey(), 0, c.now().UTC()); err != nil { return err }
			if err := tx.InstallActivityPolicy(ctx, req.Route.RemoteKey(), *req.InitialActivityPolicy); err != nil { return err }
		}
		if req.RemoteConflict != nil {
			if err := tx.ApplyRemoteConflict(ctx, req.Route.RemoteKey(), *req.RemoteConflict); err != nil { return err }
		}
		if req.Queue != nil {
			if err := tx.ApplyQueueConsequence(ctx, req.Route.RemoteKey(), *req.Queue); err != nil { return err }
		}
		nextState := "pending"
		if len(merged.Conflicts) != 0 { nextState = "conflicted" }
		if !keepCandidate && len(merged.Conflicts) == 0 { nextState = "clean" }
		if err := tx.SetStatus(ctx, nextState); err != nil { return err }

		result = localstore.RemoteReplicaImportResult{
			WorkspaceState: nextState,
			LocalAcceptedDigest: loaded.record.Snapshot.Digest,
			ComposedViewDigest: merged.Snapshot.Digest,
			RemoteCursorVersion: req.RemoteState.StreamVersion,
			SemanticConflictIDs: conflictIDs(merged.Conflicts),
			QueueDisposition: queueDisposition(req.Queue), Changed: true,
		}
		next, err = tx.CaptureRemoteImportCommitConfirmation(ctx, req)
		return err
	})
	if err == nil { return cloneRemoteImportResult(result), nil }
	if !errors.Is(err, localstore.ErrCommitOutcomeUnknown) || prior == (localstore.WorkspaceCommitConfirmation{}) || next == (localstore.WorkspaceCommitConfirmation{}) {
		return localstore.RemoteReplicaImportResult{}, err
	}
	match, confirmErr := c.repo.ConfirmWorkspaceCommit(ctx, prior, next)
	if confirmErr != nil { return localstore.RemoteReplicaImportResult{}, fmt.Errorf("%w: %v", ErrRemoteImportAttention, confirmErr) }
	switch match {
	case localstore.WorkspaceCommitNext:
		return cloneRemoteImportResult(result), nil
	case localstore.WorkspaceCommitPrior:
		return localstore.RemoteReplicaImportResult{}, err
	default:
		return localstore.RemoteReplicaImportResult{}, fmt.Errorf("%w: remote import commit is indeterminate", ErrRemoteImportAttention)
	}
}
```

Task 5 maps `ErrRemoteImportAttention` to `sync.ErrAttentionRequired`; ProjectState does
not import `runtime/sync`.

The called helpers have these exact rules and bodies:

```go
func requireRemoteAttemptPredicates(ctx context.Context, tx *localstore.WorkspaceMutationTx, loaded composedWorkspace, req localstore.RemoteReplicaImportRequest) error {
	if !reflect.DeepEqual(loaded.record, req.ExpectedWorkspace) || loaded.view.Snapshot.Digest != req.ExpectedViewDigest ||
		loaded.view.ThroughGeneration != req.ExpectedOverlayGeneration || loaded.status.CandidatePresent != req.ExpectedCandidatePresent ||
		loaded.status.CandidateDigest != req.ExpectedCandidateDigest || loaded.status.State != req.ExpectedWorkspaceState {
		return ErrAcceptedTreeImportDrift
	}
	rows, err := tx.OpenConflictOccurrences(ctx)
	if err != nil { return err }
	conflicts, err := decodeWorkspaceConflictOccurrences(rows)
	if err != nil { return err }
	if !slices.Equal(conflictIDs(conflicts), req.ExpectedOpenConflictIDs) { return ErrAcceptedTreeImportDrift }
	route, profile, present, err := tx.FabricRoute(ctx)
	if err != nil { return err }
	if present != req.ExpectedRoutePresent { return ErrAcceptedTreeImportDrift }
	if !present {
		if req.ExpectedCursor != nil || req.ExpectedActivityPolicy != nil || req.InitialActivityPolicy == nil { return ErrAcceptedTreeImportDrift }
		return nil
	}
	if route != req.Route || profile != req.Profile || req.ExpectedCursor == nil || req.ExpectedActivityPolicy == nil || req.InitialActivityPolicy != nil {
		return ErrAcceptedTreeImportDrift
	}
	cursor, err := tx.RemoteReplica(ctx, route.RemoteKey())
	if err != nil { return err }
	policy, err := tx.ActivityPolicy(ctx, route.RemoteKey())
	if err != nil { return err }
	if cursor == nil || policy == nil || !reflect.DeepEqual(*cursor, *req.ExpectedCursor) || !reflect.DeepEqual(*policy, *req.ExpectedActivityPolicy) {
		return ErrAcceptedTreeImportDrift
	}
	return nil
}

func decodeRemoteState(req localstore.RemoteReplicaImportRequest) (state.Snapshot, state.Snapshot, error) {
	accepted, err := decodeExactRemoteTree(req.RemoteState.AcceptedTree, req.RemoteState.AcceptedTreeDigest, req.Route)
	if err != nil { return state.Snapshot{}, state.Snapshot{}, err }
	live, err := decodeExactRemoteTree(req.RemoteState.LiveTree, req.RemoteState.LiveTreeDigest, req.Route)
	if err != nil { return state.Snapshot{}, state.Snapshot{}, err }
	if req.RemoteState.StreamVersion < 0 || req.RemoteState.OpenConflictIDs == nil || !sort.StringsAreSorted(req.RemoteState.OpenConflictIDs) ||
		accepted.Config != live.Config || !reflect.DeepEqual(accepted.Remotes, live.Remotes) || req.RemoteState.AcceptedCommitSHA == "" {
		return state.Snapshot{}, state.Snapshot{}, ErrAcceptedTreeEvidence
	}
	for i, id := range req.RemoteState.OpenConflictIDs {
		if !types.CanonicalUUID(id) || i > 0 && req.RemoteState.OpenConflictIDs[i-1] == id { return state.Snapshot{}, state.Snapshot{}, ErrAcceptedTreeEvidence }
	}
	if req.ExpectedCursor != nil {
		if req.RemoteState.StreamVersion < req.ExpectedCursor.StreamVersion { return state.Snapshot{}, state.Snapshot{}, ErrAcceptedTreeEvidence }
		if req.RemoteState.StreamVersion == req.ExpectedCursor.StreamVersion && !remoteStateEqualsCursor(req.RemoteState, *req.ExpectedCursor) {
			return state.Snapshot{}, state.Snapshot{}, localstore.ErrFabricCursorConflict
		}
	}
	return accepted, live, nil
}

func decodeExactRemoteTree(tree state.Tree, digest state.Digest, route types.FabricBinding) (state.Snapshot, error) {
	copyTree := cloneCheckpointTree(tree)
	gotDigest, err := state.DigestTree(copyTree)
	if err != nil || gotDigest != digest { return state.Snapshot{}, ErrAcceptedTreeEvidence }
	snapshot, err := state.DecodeTree(copyTree)
	if err != nil || state.Validate(snapshot) != nil { return state.Snapshot{}, ErrAcceptedTreeEvidence }
	canonical, err := state.EncodeTree(snapshot)
	if err != nil || !equalCheckpointTree(canonical, copyTree) || snapshot.Digest != digest ||
		snapshot.Config.ProjectID != route.Workspace.Scope.ProjectID || snapshot.Config.Repository != route.Workspace.Repository {
		return state.Snapshot{}, ErrAcceptedTreeEvidence
	}
	return snapshot, nil
}
```

`cloneAndValidateRemoteImportRequest` deep-copies every tree/JSON/slice/pointer; validates
scope, route/profile, action/action-tag, expected workspace/view/candidate/status, policy
mode, queue/link canonical operation bytes and digests; requires exact action-shape
combinations; and returns `ErrAcceptedTreeEvidence` for remote syntax versus
`ErrAcceptedTreeImportDrift` for local expected-state syntax.  It recomputes `ActionTag`
as the digest of canonical `{schema_version:1,action,route,stream_version,
live_tree_digest,operation_id,conflict_id}` and rejects a mismatch.
`exactReplicaReplay` is true only for an ongoing request whose returned complete
accepted/live/version bytes and digests equal `ExpectedCursor`; it is false for initial
install and for any queue or link consequence.

- [ ] **Step 5: Add and implement durable resolution preparation.**

```go
func TestPrepareRemoteResolutionExactReplay(t *testing.T) {
	fixture := newRemoteImportFixture(t, remoteFixtureOpaqueConflict)
	req := fixture.prepareResolutionRequest(t)
	first, err := fixture.service.PrepareRemoteResolution(t.Context(), req)
	if err != nil { t.Fatal(err) }
	second, err := fixture.service.PrepareRemoteResolution(t.Context(), req)
	if err != nil { t.Fatal(err) }
	if first.AlreadyPrepared || !second.AlreadyPrepared || !reflect.DeepEqual(first, second) { t.Fatalf("prepare replay = %+v / %+v", first, second) }
	req.Resolution.ExpectedViewDigest = digestA
	if _, err := fixture.service.PrepareRemoteResolution(t.Context(), req); !errors.Is(err, ErrRemoteResolutionReplay) { t.Fatalf("collision error = %v", err) }
}
```

```go
func (c *transitionCoordinator) prepareRemoteResolution(ctx context.Context, input localstore.PrepareRemoteResolutionRequest) (localstore.PrepareRemoteResolutionResult, error) {
	req, canonical, digest, err := cloneAndValidatePrepareResolution(input)
	if err != nil { return localstore.PrepareRemoteResolutionResult{}, err }
	var out localstore.PrepareRemoteResolutionResult
	err = c.withImmediateWorkspace(ctx, req.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		loaded, err := loadComposedWorkspace(ctx, tx)
		if err != nil { return err }
		if err := requirePreparePredicates(ctx, tx, loaded, req); err != nil { return err }
		if open, err := tx.HasOpenConflicts(ctx); err != nil { return err } else if open { return localstore.ErrWorkspaceConflicted }
		links, err := tx.RemoteConflicts(ctx, req.Route.RemoteKey())
		if err != nil { return err }
		link, ok := exactOpenRemoteConflict(links, req.ExpectedRemoteConflict.ConflictID)
		if !ok { return ErrAcceptedTreeImportDrift }
		if link.ResolutionIntentState == "prepared" {
			if link.ResolutionOperationID != req.Resolution.ID || link.ResolutionOperationDigest != digest || !bytes.Equal(link.ResolutionOperationJSON, canonical) {
				return ErrRemoteResolutionReplay
			}
			out = prepareResult(link, true)
			return nil
		}
		if link.ResolutionIntentState != "none" { return ErrAcceptedTreeImportDrift }
		consequence := req.ExpectedRemoteConflict
		consequence.ResolutionOperationID = req.Resolution.ID
		consequence.ResolutionOperationJSON = bytes.Clone(canonical)
		consequence.ResolutionOperationDigest = digest
		consequence.Resolve = false
		if err := tx.ApplyRemoteConflict(ctx, req.Route.RemoteKey(), consequence); err != nil { return err }
		out = localstore.PrepareRemoteResolutionResult{
			ConflictID: consequence.ConflictID, OriginalOperationID: consequence.OriginalOperationID,
			ResolutionOperationID: req.Resolution.ID, ResolutionOperationJSON: bytes.Clone(canonical),
			ResolutionOperationDigest: digest, AlreadyPrepared: false,
		}
		return nil
	})
	if err != nil { return localstore.PrepareRemoteResolutionResult{}, err }
	return clonePrepareRemoteResolutionResult(out), nil
}
```

Use the same remote-import confirmation target for `PrepareRemoteResolution`; capture
prior/next around `ApplyRemoteConflict` and use the exact unknown-COMMIT branch from
`importAcceptedTree`.  This is mandatory because a reported-unknown prepared intent is
otherwise not safely retryable.

- [ ] **Step 6: Add statement-fault and full confirmation RED tables.**

```go
func TestImportAcceptedTreeStatementFaultsRollBack(t *testing.T) {
	for _, table := range []string{
		"workspace_overlay_operations", "workspace_conflicts", "workspace_candidates",
		"fabric_sync_conflicts", "sync_queue", "fabric_cursors", "activity_cursors",
		"activity_policy_current", "workspace_bindings",
	} {
		t.Run(table, func(t *testing.T) {
			fixture := newRemoteImportFixture(t, remoteFixtureOngoing)
			before := captureAllRemoteImportRows(t, fixture.store)
			installAbortTrigger(t, fixture.store.DB(), table)
			if _, err := fixture.service.ImportAcceptedTree(t.Context(), fixture.request(t)); err == nil { t.Fatal("fault succeeded") }
			fixture.reopen(t)
			if after := captureAllRemoteImportRows(t, fixture.store); !reflect.DeepEqual(after, before) { t.Fatalf("%s left partial state", table) }
		})
	}
}

func TestImportCommitConfirmationClassifiesWholeProjection(t *testing.T) {
	for _, mode := range []remoteFixtureMode{
		remoteFixtureInitialEnabled, remoteFixtureInitialMissing, remoteFixtureInitialMalformed,
		remoteFixtureInitialUnbounded, remoteFixtureOngoing, remoteFixtureConflict,
		remoteFixturePushDelivery, remoteFixtureOpaqueConflict, remoteFixtureResolution,
	} {
		t.Run(mode.String(), func(t *testing.T) {
			fixture := newRemoteImportFixture(t, mode)
			prior, next := fixture.expectedConfirmations(t)
			if got := classifyWorkspaceCommitConfirmation(prior, prior, next); got != localstore.WorkspaceCommitPrior { t.Fatalf("prior = %v", got) }
			if got := classifyWorkspaceCommitConfirmation(next, prior, next); got != localstore.WorkspaceCommitNext { t.Fatalf("next = %v", got) }
			for _, mutate := range remoteProjectionMutations() {
				third := mutate(next)
				if got := classifyWorkspaceCommitConfirmation(third, prior, next); got != localstore.WorkspaceCommitThird { t.Fatalf("mixed projection classified %v", got) }
			}
		})
	}
}
```

Extend the existing opaque confirmation with a third target kind:

```go
const workspaceCommitRemoteImport workspaceCommitTargetKind = "remote_import"

type remoteImportCommitAuthorityV1 struct {
	SchemaVersion int `json:"schema_version"`
	Action localstore.RemoteImportAction `json:"action"`
	ActionTag string `json:"action_tag"`
	Scope types.WorkspaceScope `json:"scope"`
	Route types.FabricBinding `json:"route"`
	ProfileID string `json:"profile_id"`
	CredentialRef string `json:"credential_ref"` // logical reference only
	ExpectedRevision int64 `json:"expected_revision"`
	ExpectedCursor *localstore.FabricCursorRecord `json:"expected_cursor"`
	RemoteVersion int64 `json:"remote_version"`
	RemoteAcceptedDigest projectstate.Digest `json:"remote_accepted_digest"`
	RemoteLiveDigest projectstate.Digest `json:"remote_live_digest"`
	QueueOperationID string `json:"queue_operation_id"`
	RemoteConflictID string `json:"remote_conflict_id"`
}

type remoteImportCommitPostimageV1 struct {
	SchemaVersion int `json:"schema_version"`
	Workspace WorkspaceRecord `json:"workspace"`
	Candidate *WorkspaceCandidateRecord `json:"candidate"`
	Operations []WorkspaceOperation `json:"operations"`
	Conflicts []WorkspaceConflictOccurrence `json:"conflicts"`
	RoutePresent bool `json:"route_present"`
	Route types.FabricBinding `json:"route"`
	Cursor *FabricCursorRecord `json:"cursor"`
	Policy *ActivityPolicyState `json:"policy"`
	ActivityCursorPresent bool `json:"activity_cursor_present"`
	ActivityCursor int64 `json:"activity_cursor"`
	RemoteConflicts []RemoteConflictRecord `json:"remote_conflicts"`
	Queue *QueueRecord `json:"queue"`
}
```

`CaptureRemoteImportCommitConfirmation(ctx, req)` constructs the authority value,
captures the postimage exclusively through the current transaction accessors, sorts
operations/conflicts/remote links by their complete primary keys, hashes both canonical
values with `projectstate.DigestCanonicalJSON`, sets `targetID=req.ActionTag`, and uses
`tx.ProjectedWorkspaceRevision(ctx)`.  `ConfirmWorkspaceCommit` adds this switch arm:

```go
case workspaceCommitRemoteImport:
	current, err = tx.CaptureRemoteImportCommitConfirmation(ctx, prior.remoteRequest)
```

Store an owned `remoteRequest *RemoteReplicaImportRequest` inside the opaque
`WorkspaceCommitConfirmation`; exclude it from equality, validate it against the
authority digest, and use it only to reproduce the exact read projection.  Require
`next.revision == prior.revision+1`; exact replay may instead have identical prior/next
and returns before attempting a commit.  A fresh read transaction must commit before
classification.  Any read error, unstable double-read, mixed projection, unexpected
route, or revision other than prior/next returns `WorkspaceCommitThird`.

- [ ] **Step 7: Add and implement the real Git restart/reconciliation tests.**

```go
func TestRefreshWorkspaceKeepsRemoteReplicaWhenCheckoutHasNotMoved(t *testing.T) {
	fixture := newRemoteGitFixture(t) // checkout and local accepted base are B
	before := fixture.importRemoteC(t) // remote accepted/live and candidate are C
	fixture.restart(t)
	if err := fixture.service.PrepareRegisteredWorkspaces(t.Context()); err != nil { t.Fatal(err) }
	after := captureAllRemoteImportRows(t, fixture.store)
	if !reflect.DeepEqual(after, before) { t.Fatal("startup reinterpreted remote replica as Git authority") }
	workspace, err := fixture.repo.Workspace(t.Context(), fixture.binding.Scope)
	if err != nil { t.Fatal(err) }
	if workspace.Binding.AcceptedCommitSHA != fixture.commitB { t.Fatalf("accepted commit = %s", workspace.Binding.AcceptedCommitSHA) }
}

func TestRefreshWorkspaceAdoptsExactRemoteAcceptedOnlyAfterGitMoves(t *testing.T) {
	fixture := newRemoteGitFixture(t)
	fixture.importRemoteC(t)
	runGit(t, fixture.root, "checkout", "--detach", fixture.commitC)
	got, err := fixture.service.RefreshWorkspace(t.Context(), fixture.binding)
	if err != nil { t.Fatal(err) }
	if got.AcceptedCommitSHA != fixture.commitC || got.AcceptedTreeDigest != string(fixture.snapshotC.Digest) { t.Fatalf("binding = %+v", got) }
	status, err := fixture.service.Status(t.Context(), fixture.binding.Scope)
	if err != nil { t.Fatal(err) }
	if status.State != "clean" || status.CandidatePresent { t.Fatalf("status = %+v", status) }
	assertRemoteReplicaUnchanged(t, fixture.store, fixture.cursorC)
}

func TestRefreshWorkspaceRebasesOverlayAcrossRemoteExactGitMovement(t *testing.T) {
	fixture := newRemoteGitFixture(t)
	fixture.addLocalOperation(t)
	fixture.importRemoteC(t)
	runGit(t, fixture.root, "checkout", "--detach", fixture.commitC)
	if _, err := fixture.service.RefreshWorkspace(t.Context(), fixture.binding); err != nil { t.Fatal(err) }
	status, err := fixture.service.Status(t.Context(), fixture.binding.Scope)
	if err != nil { t.Fatal(err) }
	if status.State != "pending" || !status.CandidatePresent { t.Fatalf("status = %+v", status) }
	assertRemoteReplicaUnchanged(t, fixture.store, fixture.cursorC)
}
```

At the start of `gitBaseCoordinator.refresh`, retain the physical read outside SQLite,
then bypass reconciliation only for an exact unchanged local accepted position:

```go
position, err := readGitBasePosition(ctx, binding.Checkout.CanonicalPath)
if err != nil { return types.WorkspaceBinding{}, err }
if position.root != binding.Checkout.CanonicalPath || position.checkout != binding.Checkout { return types.WorkspaceBinding{}, ErrGitObservationChanged }
if position.acceptedRef == binding.AcceptedRef && position.commit == binding.AcceptedCommitSHA {
	observed, err := observeGitBaseOutside(ctx, ObserveGitBaseRequest{Scope: binding.Scope, ExpectedBinding: binding, Root: position.root, ExpectedCommit: position.commit})
	if err != nil { return types.WorkspaceBinding{}, err }
	if observed.snapshot.Digest != state.Digest(binding.AcceptedTreeDigest) { return types.WorkspaceBinding{}, ErrGitObservationChanged }
	return binding, nil
}
```

In the existing `baseChanged && loaded.proposal` branch, before the generic candidate
upsert, read the active route and replica.  Exact adoption is:

```go
route, _, present, err := tx.FabricRoute(ctx)
if err != nil { return err }
var replica *localstore.FabricCursorRecord
if present { replica, err = tx.RemoteReplica(ctx, route.RemoteKey()); if err != nil { return err } }
exactRemoteAdoption := replica != nil && replica.AcceptedCommitSHA == observed.commit &&
	replica.AcceptedTreeDigest == observed.snapshot.Digest && replica.LiveTreeDigest == observed.snapshot.Digest &&
	loaded.candidate != nil && loaded.candidate.ImportedBy == fabricSyncCandidateOriginV2 &&
	loaded.candidate.RebasedThroughGeneration == 0 && len(loaded.activeRows) == 0 && len(merged.Conflicts) == 0 &&
	merged.Snapshot.Digest == observed.snapshot.Digest
if exactRemoteAdoption {
	if _, err := tx.AdvanceAcceptedBase(ctx, localstore.WorkspaceAcceptedBaseTransition{
		Expected: loaded.workspace, ObservedRef: observed.acceptedRef,
		ObservedCommitSHA: observed.commit, ObservedTree: observed.tree, NextState: "clean",
	}); err != nil { return err }
	if err := tx.DeleteCandidate(ctx, true); err != nil { return err }
	if _, err := tx.ReplaceOpenConflictOccurrences(ctx, nil, mutationTime); err != nil { return err }
	result.Rebased = false
	attempted = cloneObserveGitBaseResult(result)
	return nil
}
```

Do not update the remote replica/cursor in Git-observer code.  Remote live different
from accepted, any overlay generation, any semantic conflict, or an unrelated D follows
the existing generic Git-base rebase path.

- [ ] **Step 8: Run Task-4 gates, stage exact files, commit, and review.**

```bash
go test ./internal/runtime/projectstate ./internal/runtime/localstore ./internal/types -run 'Test(ServiceRemoteReplica|CaptureRemoteAttempt|ImportAcceptedTree|PrepareRemoteResolution|ImportCommitConfirmation|RefreshWorkspace|ValidCandidateImportOrigin|ProjectStateArchitecture)' -count=1
go test -race ./internal/runtime/projectstate ./internal/runtime/localstore -run 'Test(CaptureRemoteAttempt|ImportAcceptedTree|PrepareRemoteResolution|ImportCommitConfirmation|RefreshWorkspace)' -count=1
go vet ./internal/runtime/projectstate ./internal/runtime/localstore ./internal/types
git add internal/runtime/projectstate/remote_import.go internal/runtime/projectstate/remote_import_test.go \
  internal/runtime/projectstate/service.go internal/runtime/projectstate/transition_coordinator.go \
  internal/runtime/projectstate/git_observer.go internal/runtime/projectstate/git_observer_test.go \
  internal/runtime/projectstate/architecture_test.go internal/runtime/localstore/workspace_repo.go \
  internal/runtime/localstore/workspace_repo_test.go internal/runtime/localstore/workspace_commit_confirmation.go \
  internal/runtime/localstore/workspace_commit_confirmation_test.go internal/types/identity.go internal/types/identity_test.go
git diff --cached --name-only
git commit -m "feat: import Fabric remote replicas atomically"
TASK_4_HEAD=$(git rev-parse HEAD)
```

Review `TASK_4_BASE..TASK_4_HEAD`.  C/I repairs are a distinct commit followed by the
same gates and a full-range C0/I0 re-review.

---

### Task 5: Routed Sync-v2 Engine

**Files:**
- Modify: `internal/runtime/sync/contract_v2.go`
- Modify: `internal/runtime/sync/engine_v2.go`
- Modify: `internal/runtime/sync/engine_v2_test.go`
- Create: `internal/runtime/sync/engine_v2_integration_test.go`
- Modify: `internal/runtime/sync/status.go`
- Create: `internal/runtime/sync/status_test.go`
- Modify: `internal/runtime/sync/contract_manifest_test.go`
- Modify: `docs/implementation-rules.md`

**Consumes:** corrected Task-2 client/call-authority interfaces; Task-3 queue store and
conflict gate; Task-4 capture/import/prepare interface.

**Produces:** one dependency-injected engine with workspace-keyed attempt lanes,
explicit pre-route attach selection, attach/bootstrap, pull, immutable queue drain,
durable remote resolution, and binding-scoped status.  `NewV2Engine()` remains an
offline zero-pending double.  Slice 8 owns construction and activation.

#### Exact engine seam

```go
type FabricAttachSource interface {
	GetAttachTarget(context.Context, types.WorkspaceScope) (types.WorkspaceBinding, types.FabricProfile, error)
}

func NewConfiguredV2Engine(
	routes FabricRouteSource,
	attachTargets FabricAttachSource,
	clients V2FabricClientFactory,
	sessions PublicAgentSessionAuthority,
	importer RemoteReplicaImporter,
	queue *QueueRepo,
	conflicts localstore.WorkspaceConflictGate,
) (*V2Engine, error)

func (e *V2Engine) AttachAndBootstrap(context.Context, types.WorkspaceScope) error
func (e *V2Engine) Pull(context.Context, types.WorkspaceScope) error
func (e *V2Engine) DrainPending(context.Context, types.WorkspaceScope, int) error
func (e *V2Engine) ResolveRemoteConflict(context.Context, types.WorkspaceScope, string, projectstate.OperationV1) error
func (e *V2Engine) Status(context.Context, types.WorkspaceBinding) (Status, error)
```

`FabricAttachSource` is read-only and represents an explicit persisted human attach
selection.  It never chooses by URL, alias, environment, public argument, or last-used
profile.  Slice 7 uses recording fixtures; Slice 8/Task 7 supplies production assembly.

- [ ] **Step 1: Capture the base and add constructor/lane/status RED tests.**

```bash
TASK_5_BASE=$(git rev-parse HEAD)
go test ./internal/runtime/sync -run 'Test(NewConfiguredV2Engine|V2EngineStatus|NewV2EngineOffline)' -count=1
```

```go
func TestNewConfiguredV2EngineRejectsNilDependencies(t *testing.T) {
	valid := configuredEngineDependencies(t)
	tests := []struct { name string; mutate func(*engineDependencies) }{
		{"routes", func(d *engineDependencies) { d.routes = nil }},
		{"attach targets", func(d *engineDependencies) { d.attachTargets = nil }},
		{"clients", func(d *engineDependencies) { d.clients = nil }},
		{"sessions", func(d *engineDependencies) { d.sessions = nil }},
		{"importer", func(d *engineDependencies) { d.importer = nil }},
		{"queue", func(d *engineDependencies) { d.queue = nil }},
		{"conflicts", func(d *engineDependencies) { d.conflicts = nil }},
	}
	for _, tt := range tests { t.Run(tt.name, func(t *testing.T) {
		d := valid.clone(); tt.mutate(&d)
		if engine, err := NewConfiguredV2Engine(d.routes,d.attachTargets,d.clients,d.sessions,d.importer,d.queue,d.conflicts); err == nil || engine != nil {
			t.Fatalf("engine = %#v, %v", engine, err)
		}
	}) }
}

func TestNewV2EngineOffline(t *testing.T) {
	engine := NewV2Engine()
	got, err := engine.Status(t.Context(), validWorkspaceBindingA())
	if err != nil { t.Fatal(err) }
	if got != (Status{State: StateOffline, PendingWrites: 0}) { t.Fatalf("status = %+v", got) }
}

func TestV2EngineStatusIsBindingScoped(t *testing.T) {
	fixture := newEngineFixture(t)
	fixture.enqueue(t, fixture.routeA.RemoteKey(), operationA())
	fixture.blockPullA()
	done := make(chan error,1)
	go func(){ done <- fixture.engine.Pull(t.Context(), fixture.routeA.Workspace.Scope) }()
	fixture.waitPullA()
	a, err := fixture.engine.Status(t.Context(), fixture.routeA.Workspace)
	if err != nil { t.Fatal(err) }
	b, err := fixture.engine.Status(t.Context(), fixture.routeB.Workspace)
	if err != nil { t.Fatal(err) }
	if a != (Status{State:StateSynchronizing,PendingWrites:1}) || b != (Status{State:StateOffline,PendingWrites:0}) { t.Fatalf("A=%+v B=%+v",a,b) }
	fixture.releasePullA()
	if err := <-done; err != nil { t.Fatal(err) }
}
```

- [ ] **Step 2: Implement the engine, keyed lanes, error mapping, and status.**

```go
type v2Lane struct {
	attempt sync.Mutex
	statusMu sync.RWMutex
	state ConnectionState
}

type V2Engine struct {
	configured bool
	routes FabricRouteSource
	attachTargets FabricAttachSource
	clients V2FabricClientFactory
	sessions PublicAgentSessionAuthority
	importer RemoteReplicaImporter
	queue *QueueRepo
	conflicts localstore.WorkspaceConflictGate
	lanesMu sync.Mutex
	lanes map[types.WorkspaceScope]*v2Lane
}

func NewV2Engine() *V2Engine { return &V2Engine{} }

func NewConfiguredV2Engine(routes FabricRouteSource, attachTargets FabricAttachSource, clients V2FabricClientFactory, sessions PublicAgentSessionAuthority, importer RemoteReplicaImporter, queue *QueueRepo, conflicts localstore.WorkspaceConflictGate) (*V2Engine,error) {
	if nilInterface(routes)||nilInterface(attachTargets)||nilInterface(clients)||nilInterface(sessions)||nilInterface(importer)||queue==nil||nilInterface(conflicts) { return nil,ErrAttentionRequired }
	return &V2Engine{configured:true,routes:routes,attachTargets:attachTargets,clients:clients,sessions:sessions,importer:importer,queue:queue,conflicts:conflicts,lanes:make(map[types.WorkspaceScope]*v2Lane)},nil
}

func (e *V2Engine) lane(scope types.WorkspaceScope) *v2Lane {
	e.lanesMu.Lock(); defer e.lanesMu.Unlock()
	if lane:=e.lanes[scope]; lane!=nil{return lane}
	lane:=&v2Lane{state:StateOffline}; e.lanes[scope]=lane; return lane
}

func (l *v2Lane) set(state ConnectionState){l.statusMu.Lock();l.state=state;l.statusMu.Unlock()}
func (l *v2Lane) get() ConnectionState{l.statusMu.RLock();defer l.statusMu.RUnlock();return l.state}

func validEngineScope(scope types.WorkspaceScope) bool {
	return types.CanonicalUUID(scope.ProjectID) && types.CanonicalUUID(string(scope.WorkspaceID))
}

func (e *V2Engine) attempt(ctx context.Context, scope types.WorkspaceScope, fn func(context.Context) error) error {
	if e==nil||!e.configured||!validEngineScope(scope){return ErrAttentionRequired}
	l:=e.lane(scope); l.attempt.Lock(); defer l.attempt.Unlock()
	if err:=ctx.Err();err!=nil{return err}
	l.set(StateSynchronizing)
	err:=fn(ctx)
	switch {case err==nil:l.set(StateOnline);case errors.Is(err,ErrFabricUnavailable):l.set(StateOffline);default:l.set(StateAttentionRequired)}
	return err
}

func (e *V2Engine) Status(ctx context.Context,binding types.WorkspaceBinding)(Status,error){
	if e==nil||binding.Validate()!=nil{return Status{},ErrAttentionRequired}
	if !e.configured{return Status{State:StateOffline,PendingWrites:0},nil}
	route,profile,err:=e.routes.GetRoute(ctx,binding.Scope)
	if errors.Is(err,localstore.ErrNotFound){return Status{State:e.lane(binding.Scope).get(),PendingWrites:0},nil}
	if err!=nil||route.Workspace!=binding||route.ValidateWithProfile(profile)!=nil{return Status{},ErrAttentionRequired}
	pending,err:=e.queue.PendingCount(ctx,route.RemoteKey());if err!=nil{return Status{},ErrAttentionRequired}
	return Status{State:e.lane(binding.Scope).get(),PendingWrites:pending},nil
}
```

Add sentinels in `status.go`:

```go
var ErrFabricProtocol = errors.New("sync: Fabric protocol violation")
var ErrRemoteSyncConflict = errors.New("sync: remote conflict")
var ErrRemotePrecondition = errors.New("sync: remote precondition")
```

`mapEngineError` is closed and never includes underlying secret-bearing text:

```go
func mapEngineError(err error) error {
	if err == nil { return nil }
	switch {
	case errors.Is(err, context.Canceled): return context.Canceled
	case errors.Is(err, context.DeadlineExceeded): return context.DeadlineExceeded
	case errors.Is(err, ErrFabricUnavailable): return ErrFabricUnavailable
	case errors.Is(err, localstore.ErrWorkspaceConflicted): return localstore.ErrWorkspaceConflicted
	case errors.Is(err, localstore.ErrFabricCursorConflict): return localstore.ErrFabricCursorConflict
	case errors.Is(err, ErrRemoteSyncConflict): return ErrRemoteSyncConflict
	case errors.Is(err, ErrRemotePrecondition): return ErrRemotePrecondition
	case errors.Is(err, ErrFabricProtocol): return ErrFabricProtocol
	default: return ErrAttentionRequired
	}
}
```

- [ ] **Step 3: Add and implement attach/bootstrap.**

```go
func TestSyncV2AttachAndBootstrapCausalOrder(t *testing.T){
	f:=newEngineFixture(t); f.removeRouteA(t)
	if err:=f.engine.AttachAndBootstrap(t.Context(),f.bindingA.Scope);err!=nil{t.Fatal(err)}
	want:=[]string{"attach-target","capture","attach-client","attach","route-client","bootstrap","import:initial_install"}
	if !slices.Equal(f.trace(),want){t.Fatalf("trace=%v",f.trace())}
	got:=f.importer.lastImport
	if got.ExpectedRoutePresent||got.ExpectedCursor!=nil||got.InitialActivityPolicy==nil||got.Route.Workspace!=f.bindingA{t.Fatalf("request=%+v",got)}
}

func TestSyncV2AttachRejectsForkBeforeClient(t *testing.T){
	f:=newEngineFixture(t);f.removeRouteA(t);f.attachTargets.repository=repositoryB
	if err:=f.engine.AttachAndBootstrap(t.Context(),f.bindingA.Scope);!errors.Is(err,ErrAttentionRequired){t.Fatalf("error=%v",err)}
	if f.clients.calls()!=0||f.importer.importCalls()!=0{t.Fatalf("forbidden calls client=%d import=%d",f.clients.calls(),f.importer.importCalls())}
}
```

```go
func (e *V2Engine) AttachAndBootstrap(ctx context.Context,scope types.WorkspaceScope) error{
	return e.attempt(ctx,scope,func(ctx context.Context)error{
		workspace,profile,err:=e.attachTargets.GetAttachTarget(ctx,scope)
		if err!=nil||workspace.Validate()!=nil||workspace.Scope!=scope||profile.Validate()!=nil||profile.Mode!=types.FabricModePublic{return ErrAttentionRequired}
		capture,err:=e.importer.CaptureRemoteAttempt(ctx,scope);if err!=nil{return mapEngineError(err)}
		if capture.Workspace.Binding!=workspace||capture.RoutePresent||capture.Cursor!=nil||capture.ActivityPolicy!=nil{return ErrAttentionRequired}
		client,err:=e.clients.AttachClient(ctx,workspace,profile);if err!=nil{return mapEngineError(err)}
		authority,err:=HumanCallAuthority(profile.CredentialRef);if err!=nil{return ErrAttentionRequired}
		attach,err:=client.Attach(ctx,authority,projectstate.SyncAttachV2Args{Version:2,Repository:workspace.Repository,CanonicalRef:workspace.AcceptedRef,BaseCommitSHA:workspace.AcceptedCommitSHA,BaseTreeDigest:projectstate.Digest(workspace.AcceptedTreeDigest)})
		if err!=nil{return mapEngineError(err)}
		route:=types.FabricBinding{Workspace:workspace,ProfileID:profile.ProfileID,FabricInstanceID:profile.FabricInstanceID,RemoteProjectID:attach.Wire.RemoteProjectID,StreamID:attach.Wire.StreamID,AttachmentRef:attach.Wire.AttachmentRef,CanonicalRef:workspace.AcceptedRef,Writable:true}
		if route.ValidateWithProfile(profile)!=nil||attach.Wire.Version!=2{return ErrFabricProtocol}
		active,err:=e.clients.Client(ctx,route,profile);if err!=nil{return mapEngineError(err)}
		prior:=projectstate.SyncV2Scope{Version:2,AttachmentRef:route.AttachmentRef,Repository:workspace.Repository,CanonicalRef:workspace.AcceptedRef,BaseCommitSHA:workspace.AcceptedCommitSHA,BaseTreeDigest:projectstate.Digest(workspace.AcceptedTreeDigest),ExpectedStreamVersion:attach.Wire.StreamVersion,ExpectedLiveTreeDigest:projectstate.Digest(workspace.AcceptedTreeDigest)}
		bootstrap,err:=active.Bootstrap(ctx,authority,projectstate.SyncBootstrapV2Args{SyncV2Scope:prior,AfterVersion:attach.Wire.StreamVersion});if err!=nil{return mapEngineError(err)}
		policy,err:=combineInitialPolicy(attach.ActivityPolicy,bootstrap.ActivityPolicy);if err!=nil{return ErrFabricProtocol}
		if err:=validateCompleteState(route,prior,bootstrap.Wire.State);err!=nil{return err}
		req:=importRequestFromCapture(capture,route,profile,nil,prior,bootstrap.Wire.State,localstore.RemoteImportInitial)
		req.InitialActivityPolicy=&policy;req.ActionTag=remoteActionTag(req)
		_,err=e.importer.ImportAcceptedTree(ctx,req);return mapEngineError(err)
	})
}
```

`combineInitialPolicy` requires two enabled policies to be identical; otherwise returns
the most restrictive disabled reason in `missing,malformed,unbounded` order and removes
policy version/digest.  `validateCompleteState` checks route/project/repository/ref,
strict canonical trees/digests, safe version, sorted unique UUID conflict IDs, accepted
commit, and accepted/live Git-owned config equality.

- [ ] **Step 4: Add and implement pull.**

```go
func TestSyncV2PullChangedAndUnchanged(t *testing.T){
	for _,changed:=range []bool{false,true}{t.Run(strconv.FormatBool(changed),func(t *testing.T){
		f:=newEngineFixture(t);f.client.pull.Changed=changed
		if err:=f.engine.Pull(t.Context(),f.bindingA.Scope);err!=nil{t.Fatal(err)}
		want:=0;if changed{want=1};if f.importer.importCalls()!=want{t.Fatalf("imports=%d",f.importer.importCalls())}
	})}
}
```

```go
func (e *V2Engine) Pull(ctx context.Context,scope types.WorkspaceScope)error{
	return e.attempt(ctx,scope,func(ctx context.Context)error{
		capture,route,profile,err:=e.captureRoutedAttempt(ctx,scope);if err!=nil{return err}
		client,err:=e.clients.Client(ctx,route,profile);if err!=nil{return mapEngineError(err)}
		authority,err:=HumanCallAuthority(profile.CredentialRef);if err!=nil{return ErrAttentionRequired}
		prior:=scopeFromCursor(route,*capture.Cursor)
		pulled,err:=client.Pull(ctx,authority,projectstate.SyncPullV2Args{SyncV2Scope:prior,AfterVersion:capture.Cursor.StreamVersion});if err!=nil{return mapEngineError(err)}
		if err:=validateCompleteState(route,prior,pulled.State);err!=nil{return err}
		if !pulled.Changed{
			if !stateEqualsCursor(pulled.State,*capture.Cursor){return ErrFabricProtocol}
			return nil
		}
		req:=importRequestFromCapture(capture,route,profile,capture.Cursor,prior,pulled.State,localstore.RemoteImportPull)
		req.ActionTag=remoteActionTag(req)
		_,err=e.importer.ImportAcceptedTree(ctx,req);return mapEngineError(err)
	})
}
```

`captureRoutedAttempt` calls `routes.GetRoute` and `importer.CaptureRemoteAttempt`, then
requires exact workspace/route/profile equality, a nonnil cursor/current policy, and a
valid six-field key.  It returns owned copies.  No lock other than the attempt lane is
held across subsequent network calls.

- [ ] **Step 5: Add the five immutable push acceptance tests and implement drain.**

The test fixture is a real schema-v9 SQLite queue plus recording interfaces.  These
complete test bodies establish the mandatory causal gates:

```go
func TestSyncV2PushOpenConflictStopsBeforeCredentialOrNetwork(t *testing.T){
	f:=newEngineFixture(t);f.conflicts.open=true
	err:=f.engine.DrainPending(t.Context(),f.bindingA.Scope,1)
	if !errors.Is(err,localstore.ErrWorkspaceConflicted){t.Fatalf("error=%v",err)}
	if got:=f.trace();!slices.Equal(got,[]string{"conflict-gate"}){t.Fatalf("trace=%v",got)}
}

func TestSyncV2PushConflictGateFailureHasNoSideEffects(t *testing.T){
	f:=newEngineFixture(t);f.conflicts.err=errors.New("corrupt gate")
	before:=f.queueBytes(t)
	if err:=f.engine.DrainPending(t.Context(),f.bindingA.Scope,1);!errors.Is(err,ErrAttentionRequired){t.Fatalf("error=%v",err)}
	if !bytes.Equal(before,f.queueBytes(t))||len(f.trace())!=1{t.Fatal("gate failure had side effects")}
}

func TestSyncV2PushConflictOpenedInFlightLeavesQueueByteIdenticalPending(t *testing.T){
	f:=newEngineFixture(t);before:=f.queueBytes(t);f.blockPush()
	done:=make(chan error,1);go func(){done<-f.engine.DrainPending(t.Context(),f.bindingA.Scope,1)}();f.waitPush()
	f.openRealWorkspaceConflict(t);f.releasePush()
	if err:=<-done;!errors.Is(err,localstore.ErrWorkspaceConflicted){t.Fatalf("error=%v",err)}
	if !bytes.Equal(before,f.queueBytes(t)){t.Fatal("in-flight conflict changed queue")}
}

func TestSyncV2PushConflictScopeIsolation(t *testing.T){
	f:=newEngineFixture(t);f.openConflictFor(t,f.bindingB.Scope)
	if err:=f.engine.DrainPending(t.Context(),f.bindingA.Scope,1);err!=nil{t.Fatal(err)}
	if f.pending(t,f.routeA.RemoteKey())!=0||f.pending(t,f.routeB.RemoteKey())!=1{t.Fatal("cross-scope delivery")}
}

func TestSyncV2PushRetriesExactOperationAfterResolution(t *testing.T){
	f:=newEngineFixture(t);want:=bytes.Clone(f.pendingOperationBytes(t));f.client.pushConflict=true
	if err:=f.engine.DrainPending(t.Context(),f.bindingA.Scope,1);!errors.Is(err,ErrRemoteSyncConflict){t.Fatalf("error=%v",err)}
	f.client.pushConflict=false
	if err:=f.engine.DrainPending(t.Context(),f.bindingA.Scope,1);err!=nil{t.Fatal(err)}
	if len(f.client.pushed)!=2||!bytes.Equal(f.client.pushed[0],want)||!bytes.Equal(f.client.pushed[1],want){t.Fatal("retry changed operation bytes")}
}
```

```go
func (e *V2Engine) DrainPending(ctx context.Context,scope types.WorkspaceScope,limit int)error{
	if limit<=0{return ErrAttentionRequired}
	return e.attempt(ctx,scope,func(ctx context.Context)error{
		for delivered:=0;delivered<limit;delivered++{
			open,err:=e.conflicts.HasOpenConflicts(ctx,scope)
			if err!=nil{return ErrAttentionRequired};if open{return localstore.ErrWorkspaceConflicted}
			capture,route,profile,err:=e.captureRoutedAttempt(ctx,scope);if err!=nil{return err}
			entries,err:=e.queue.ListPending(ctx,route.RemoteKey(),1);if err!=nil{return ErrAttentionRequired};if len(entries)==0{return nil}
			entry:=entries[0]
			canonical,err:=projectstate.CanonicalOperation(entry.Operation);if err!=nil||!bytes.Equal(canonical,entry.OperationJSON){return ErrAttentionRequired}
			client,err:=e.clients.Client(ctx,route,profile);if err!=nil{return mapEngineError(err)}
			authority,err:=e.callAuthority(ctx,route,profile,entry.Operation.Actor);if err!=nil{return err}
			prior:=scopeFromCursor(route,*capture.Cursor)
			pushed,pushErr:=client.Push(ctx,authority,projectstate.SyncPushV2Args{SyncV2Scope:prior,Operation:entry.Operation})
			if pushErr!=nil&&!errors.Is(pushErr,ErrRemotePrecondition){return mapEngineError(pushErr)}
			pulled,err:=client.Pull(ctx,authority,projectstate.SyncPullV2Args{SyncV2Scope:prior,AfterVersion:capture.Cursor.StreamVersion});if err!=nil{return mapEngineError(err)}
			if err:=validateCompleteState(route,prior,pulled.State);err!=nil{return err}
			req:=importRequestFromCapture(capture,route,profile,capture.Cursor,prior,pulled.State,localstore.RemoteImportPushRetain)
			req.Queue=&localstore.QueueConsequence{OperationID:entry.ID,ExpectedOperationJSON:bytes.Clone(entry.OperationJSON),ExpectedOperationDigest:entry.OperationDigest,Disposition:localstore.QueueUnchanged}
			switch{
			case pushErr==nil&&pushed.Applied!=nil:
				if pushed.Applied.OperationID!=entry.ID||pulled.State.StreamVersion<pushed.Applied.StreamVersion{return ErrFabricProtocol}
				req.Action=localstore.RemoteImportPushDeliver;req.Queue.Disposition=localstore.QueueDelivered
			case pushErr==nil&&pushed.Conflict!=nil:
				if pushed.Conflict.OperationID!=entry.ID{return ErrFabricProtocol}
				req.RemoteConflict=&localstore.RemoteConflictConsequence{ConflictID:pushed.Conflict.ConflictID,OriginalOperationID:entry.ID,OriginalOperationDigest:entry.OperationDigest,DetectedStreamVersion:pushed.Conflict.StreamVersion,DetectedLiveTreeDigest:pushed.Conflict.LiveTreeDigest}
			case errors.Is(pushErr,ErrRemotePrecondition):
			default:return ErrFabricProtocol
			}
			req.ActionTag=remoteActionTag(req)
			result,err:=e.importer.ImportAcceptedTree(ctx,req);if err!=nil{return mapEngineError(err)}
			if req.Action==localstore.RemoteImportPushRetain{
				if result.ComposedViewDigest!=entry.Operation.ExpectedViewDigest{return ErrRemotePrecondition}
				if req.RemoteConflict!=nil{return ErrRemoteSyncConflict}
				return ErrRemotePrecondition
			}
		}
		return nil
	})
}

func (e *V2Engine) callAuthority(ctx context.Context,route types.FabricBinding,profile types.FabricProfile,actor types.ActorEnvelope)(V2CallAuthority,error){
	if err:=actor.ValidateHistorical();err!=nil{return V2CallAuthority{},ErrAttentionRequired}
	if actor.ActorKind==types.ActorHuman{return HumanCallAuthority(profile.CredentialRef)}
	if actor.ActorKind!=types.ActorAgent{return V2CallAuthority{},ErrAttentionRequired}
	issued,err:=e.sessions.Acquire(ctx,route.Workspace,actor);if err!=nil{return V2CallAuthority{},ErrAttentionRequired}
	return AgentCallAuthority(profile.CredentialRef,issued)
}
```

The Task-4 import transaction performs the authoritative post-network open-conflict
check in `ApplyQueueConsequence`; an in-flight conflict rolls back cursor, candidate,
link, and delivery together.  The operation row is never rewritten.

- [ ] **Step 6: Add and implement durable remote resolution.**

```go
func TestSyncV2ResolveRemoteConflictSurvivesCrashAfterPrepare(t *testing.T){
	f:=newEngineFixture(t);resolution:=resolutionOperation();f.client.failResolve=ErrFabricUnavailable
	if err:=f.engine.ResolveRemoteConflict(t.Context(),f.bindingA.Scope,f.remoteConflictID,resolution);!errors.Is(err,ErrFabricUnavailable){t.Fatalf("error=%v",err)}
	prepared:=f.readRemoteConflict(t);if prepared.ResolutionIntentState!="prepared"{t.Fatalf("link=%+v",prepared)}
	f.restartEngine(t);f.client.failResolve=nil
	if err:=f.engine.ResolveRemoteConflict(t.Context(),f.bindingA.Scope,f.remoteConflictID,resolution);err!=nil{t.Fatal(err)}
	if len(f.client.resolutions)!=2||!bytes.Equal(f.client.resolutions[0],f.client.resolutions[1]){t.Fatal("resolution retry changed bytes")}
}

func TestSyncV2ResolveRemoteConflictRejectsDifferentReplay(t *testing.T){
	f:=newEngineFixture(t);resolution:=resolutionOperation();f.prepareOnly(t,resolution)
	resolution.ExpectedViewDigest=digestA
	if err:=f.engine.ResolveRemoteConflict(t.Context(),f.bindingA.Scope,f.remoteConflictID,resolution);!errors.Is(err,ErrAttentionRequired){t.Fatalf("error=%v",err)}
	if f.client.resolveCalls()!=0{t.Fatal("collision reached network")}
}
```

```go
func (e *V2Engine) ResolveRemoteConflict(ctx context.Context,scope types.WorkspaceScope,conflictID string,resolution projectstate.OperationV1)error{
	if !types.CanonicalUUID(conflictID){return ErrAttentionRequired}
	canonical,err:=projectstate.CanonicalOperation(resolution);if err!=nil{return ErrAttentionRequired}
	return e.attempt(ctx,scope,func(ctx context.Context)error{
		open,err:=e.conflicts.HasOpenConflicts(ctx,scope);if err!=nil{return ErrAttentionRequired};if open{return localstore.ErrWorkspaceConflicted}
		capture,route,profile,err:=e.captureRoutedAttempt(ctx,scope);if err!=nil{return err}
		link,ok:=findOpenRemoteConflict(capture.OpenRemoteConflicts,conflictID);if !ok{return ErrRemoteSyncConflict}
		prepareReq:=prepareRequestFromCapture(capture,route,profile,*capture.Cursor,link,resolution)
		prepareReq.ActionTag=resolutionActionTag(prepareReq)
		prepared,err:=e.importer.PrepareRemoteResolution(ctx,prepareReq);if errors.Is(err,projectstate.ErrRemoteResolutionReplay){return ErrAttentionRequired};if err!=nil{return mapEngineError(err)}
		decoded,err:=projectstate.DecodeOperation(prepared.ResolutionOperationJSON);if err!=nil||!bytes.Equal(canonical,prepared.ResolutionOperationJSON)||decoded.ID!=resolution.ID{return ErrAttentionRequired}
		client,err:=e.clients.Client(ctx,route,profile);if err!=nil{return mapEngineError(err)}
		authority,err:=e.callAuthority(ctx,route,profile,decoded.Actor);if err!=nil{return err}
		prior:=scopeFromCursor(route,*capture.Cursor)
		resolved,err:=client.ResolveConflict(ctx,authority,projectstate.SyncConflictV2Args{SyncV2Scope:prior,ConflictID:conflictID,Resolution:decoded});if err!=nil{return mapEngineError(err)}
		if resolved.ConflictID!=conflictID||resolved.OperationID!=decoded.ID{return ErrFabricProtocol}
		pulled,err:=client.Pull(ctx,authority,projectstate.SyncPullV2Args{SyncV2Scope:prior,AfterVersion:capture.Cursor.StreamVersion});if err!=nil{return mapEngineError(err)}
		if err:=validateCompleteState(route,prior,pulled.State);err!=nil{return err}
		req:=importRequestFromCapture(capture,route,profile,capture.Cursor,prior,pulled.State,localstore.RemoteImportResolution)
		req.RemoteConflict=&localstore.RemoteConflictConsequence{ConflictID:conflictID,OriginalOperationID:link.OriginalOperationID,OriginalOperationDigest:link.OriginalOperationDigest,DetectedStreamVersion:link.DetectedStreamVersion,DetectedLiveTreeDigest:link.DetectedLiveTreeDigest,ResolutionOperationID:prepared.ResolutionOperationID,ResolutionOperationJSON:bytes.Clone(prepared.ResolutionOperationJSON),ResolutionOperationDigest:prepared.ResolutionOperationDigest,Resolve:true}
		entry,err:=e.queue.GetEntry(ctx,route.RemoteKey(),link.OriginalOperationID);if err!=nil{return ErrAttentionRequired}
		req.Queue=&localstore.QueueConsequence{OperationID:entry.ID,ExpectedOperationJSON:bytes.Clone(entry.OperationJSON),ExpectedOperationDigest:entry.OperationDigest,Disposition:localstore.QueueDelivered}
		req.ActionTag=remoteActionTag(req)
		_,err=e.importer.ImportAcceptedTree(ctx,req);return mapEngineError(err)
	})
}
```

- [ ] **Step 7: Add deterministic no-local-lock-across-network and non-activation tests.**

```go
func TestV2EngineNetworkWaitDoesNotBlockOtherWorkspaceOrSQLite(t *testing.T){
	f:=newEngineFixture(t);f.blockPush();done:=make(chan error,1)
	go func(){done<-f.engine.DrainPending(t.Context(),f.bindingA.Scope,1)}();f.waitPush()
	if err:=f.writeLocalWorkspaceB(t);err!=nil{t.Fatal(err)}
	if err:=f.engine.Pull(t.Context(),f.bindingB.Scope);err!=nil{t.Fatal(err)}
	f.releasePush();if err:=<-done;err!=nil{t.Fatal(err)}
}

func TestConfiguredV2EngineRemainsUnassembled(t *testing.T){
	root:=repositoryRoot(t)
	for _,path:=range []string{"cmd/gatewayd/gatewayd.go","internal/runtime/localapi/supervisor.go","internal/runtime/localapi/mcp.go","docs/contracts/alpha-contract.json"}{
		raw,err:=os.ReadFile(filepath.Join(root,path));if err!=nil{t.Fatal(err)}
		if bytes.Contains(raw,[]byte("NewConfiguredV2Engine"))||bytes.Contains(raw,[]byte("wormhole.sync.issue_agent_session")){t.Fatalf("%s activates Slice 7",path)}
	}
}
```

The blocked client channels are entered only after route/capture/queue reads return.
While blocked, the test successfully runs an immediate SQLite write and a second route's
network attempt.  Run it under `-race`.

- [ ] **Step 8: Run Task-5 gates, stage exact files, commit, and review.**

```bash
go test ./internal/runtime/sync -run 'Test(NewConfiguredV2Engine|NewV2EngineOffline|V2EngineStatus|SyncV2Attach|SyncV2Pull|SyncV2Push|SyncV2Resolve|ConfiguredV2EngineRemainsUnassembled)' -count=1
go test -race ./internal/runtime/sync -run 'Test(V2Engine|SyncV2Push|SyncV2Pull|SyncV2Resolve)' -count=1
go vet ./internal/runtime/sync
git add internal/runtime/sync/contract_v2.go internal/runtime/sync/engine_v2.go \
  internal/runtime/sync/engine_v2_test.go internal/runtime/sync/engine_v2_integration_test.go \
  internal/runtime/sync/status.go internal/runtime/sync/status_test.go \
  internal/runtime/sync/contract_manifest_test.go docs/implementation-rules.md
git diff --cached --name-only
git commit -m "feat: coordinate routed sync v2 durability"
TASK_5_HEAD=$(git rev-parse HEAD)
```

Review `TASK_5_BASE..TASK_5_HEAD` for exact immutable retries, both conflict gates,
durable resolution, route/status isolation, proof authority, no local lock across
network, narrow dependency direction, and non-activation.  C/I repairs are a distinct
commit followed by the same gates and a full-range C0/I0 re-review.

### Task 6: Slice Gate, Independent Review, and Ledger Checkpoint

**Files:**
- Controller-only, normally ignored: `.superpowers/sdd/task6-slice7-brief.md`, `task6-slice7-plan.md`, `task6-slice7-report.md`, implementation-base record
- Modify `.superpowers/sdd/progress.md` in place only for the Slice 7 ledger entry, preserving inherited edits; do not stage it unless it is already tracked and the orchestrator explicitly owns the combined diff
- Design/consumer note may change only for verified factual discrepancy; no interface broadening

- [ ] **Step 1: Freeze exact ranges and perform the ownership audit.**

```bash
CROSS_BRANCH_BASE=0735306a3dacd02a0e197ab56cbfeb90728c7397
IMPLEMENTATION_BASE=$(sed -n 's/^IMPLEMENTATION_BASE=//p' .superpowers/sdd/task6-slice7-implementation-base)
SLICE_7_HEAD=$(git rev-parse HEAD)
test -n "$IMPLEMENTATION_BASE"
git diff --name-status "$IMPLEMENTATION_BASE...$SLICE_7_HEAD"
if ! forbidden=$(git diff --name-only "$CROSS_BRANCH_BASE...$SLICE_7_HEAD" -- \
  internal/runtime/localapi internal/runtime/codegraph cmd/gatewayd/gatewayd.go docs/contracts/alpha-contract.json); then
  exit 1
fi
test -z "$forbidden"
```

Negative self-test proving the matcher fails on a forbidden path:

```bash
set -o pipefail
if printf '%s\n' internal/runtime/localapi/providers.go | rg -q '^(internal/runtime/localapi/|internal/runtime/codegraph/|cmd/gatewayd/gatewayd\.go$|docs/contracts/alpha-contract\.json$)'; then
  : expected synthetic collision
else
  exit 1
fi
```

The explicit assignment guard fails on any `git diff` error. `test -z` then fails on any
real forbidden path, which is reported to its owning branch.

- [ ] **Step 2: Run focused, race, vet, and architecture/contract gates.**

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/mcp ./internal/core/identity ./internal/runtime/localidentity ./internal/runtime/sync ./internal/runtime/localstore ./internal/runtime/projectstate ./internal/types ./internal/types/projectstate \
  -run 'Test(SyncV2|ResolvePublicAttachmentIssue|PublicToolCaller|SignSelectedHuman|AgentSessionAuthority|ImportAcceptedTree|ImportCommitConfirmation|V2Engine|PrivateSchemaV9|Queue|FabricRoute|Activity)' -count=1
go test -race ./internal/runtime/localidentity ./internal/runtime/sync ./internal/runtime/localstore ./internal/runtime/projectstate -count=1
go vet ./internal/mcp ./internal/core/identity ./internal/runtime/localidentity ./internal/runtime/sync ./internal/runtime/localstore ./internal/runtime/projectstate ./internal/types ./internal/types/projectstate
go test ./internal/runtime/projectstate -run 'TestProjectStateArchitecture' -count=1
go test ./internal/runtime/sync -run 'TestContractManifest' -count=1
```

- [ ] **Step 3: Run repository and coverage gates.**

```bash
make check
go test ./... -covermode=atomic -coverprofile=/tmp/wormhole-slice7.cover
go tool cover -func=/tmp/wormhole-slice7.cover | tail -1
```

Require at least 80.0%. Record any environment skip exactly; a skipped required Postgres gate is not completion.

- [ ] **Step 4: Request whole-slice independent review.**

Review exact range `IMPLEMENTATION_BASE..SLICE_7_HEAD` against every design section. Require explicit dispositions for: tracked-agent session transaction and denied audit; Activity session use; strict wire tables; six-key schema/FKs; every enumerated statement fault; full unknown-COMMIT branch table; real restart interlock; candidate omission; immutable queue retry; prepared resolution replay; exact-binding status; package direction; migration allocation; parallel collision audit. Repair each C/I in separate narrow commits, rerun Steps 1–3, update head, and re-review until C0/I0.

- [ ] **Step 5: Update controller report/ledger without absorbing inherited work.**

Record base/head, per-task commit/review SHAs, C/I/M disposition, focused/full/race/vet results, exact coverage, v9 refusal evidence, migration `000022/000023`, and collision result. Do not force-add ignored SDD files. If `.superpowers/sdd/progress.md` contains inherited changes, append the Slice 7 entry but leave it unstaged for the orchestrator to reconcile.

- [ ] **Step 6: Commit only a necessary checked-in factual report, otherwise leave no Task-6 code commit.**

Run `git status --short`, compare every path with the inherited-dirty inventory, and stage only a checked-in Slice 7 report or factual design correction that the independent review required. If none is required, Task 6 creates no artificial empty commit. Never stage controller artifacts merely to make a checkpoint commit.

## Self-Review

- Spec §§1–6: global constraints and Tasks 1–2 cover authority, route order, caller, strict wire, sessions, Activity, and no private OIDC.
- Spec §§7–10: Tasks 4–5 cover remote-replica semantics, Git restart interlock, import atomicity, immutable queue/resolution replay, status isolation, and no lock across network.
- Spec §11: Task 3 inventories every current v8 production/test/doc hook, removes production v8, freezes v9, and records migration allocation.
- Spec §§12–15: frozen errors, enumerated tests, six reviewable commits, exclusions, and final whole-slice gate are explicit.
- Every new type and constructor used by a later task is defined above; `RemoteReplicaImporter` avoids the forbidden runtime dependency and the reverse edge is tested.
- Every task follows RED → GREEN gates → GREEN commit → exact-range review → separate repair commit/re-review.
- Every staging command is an exact path list; controller/inherited files and parallel collision files are excluded.
- No `TBD`, `TODO`, “similar to”, generic “add tests”, or unassigned production behavior remains.
