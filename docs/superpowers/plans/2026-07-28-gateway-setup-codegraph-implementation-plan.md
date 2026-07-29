# Gateway Supervisor, Setup, and Code Graph Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** Deliver Slices B and C: one user-level Gateway with private workspace routing, isolated on-demand Code Graph workers and deterministic freshness-gated retrieval, plus journalled setup and transactional Codex/Claude connector lifecycle.

**Architecture:** Slice A owns the shared project-state types, codec, workspace resolver/service, OperationV1 write authority, and one localapi workspace domain file. Slice B adds owner-only local identities/sessions, a private bridge envelope, binding-aware actor/project dispatch, one recovered supervisor, and per-workspace worker processes. Slice C keeps service/journal/connector primitives under internal/runtime/config while cmd/wormhole owns orchestration and all Gateway RPC calls; multi-Fabric project administration remains dependent on Slice D.

**Tech Stack:** Go 1.26.5, standard library, existing modernc.org/sqlite, existing golang.org/x/tools/go/packages, Unix sockets, MCP JSON-RPC, Git, Codex CLI, and Claude Code CLI. Add no dependency.

## Global Constraints

- RFC-0003 and docs/superpowers/specs/2026-07-28-git-native-wormhole-architecture-design.md are authoritative over legacy alpha code.
- Consume internal/types/workspace.go, internal/types/identity.go, internal/types/projectstate, and internal/runtime/projectstate. Do not introduce duplicate workspace, identity, codec, resolver, or repository types.
- internal/runtime never imports internal/core or internal/mcp. internal/types remains standard-library-only. No ORM, global singleton, init registration, web framework, or panic control flow.
- Gateway owns one stable user socket and one control DB. No positional profile, current-project default, current-workspace default, or direct harness-to-Fabric path.
- Public MCP schemas expose neither working directories nor machine-private project/workspace/checkout IDs. The bridge-only cwd envelope is stripped before schema validation and never forwarded to Fabric.
- Every local project, sync, graph, and authorization operation consumes the binding resolved from the private request context.
- Every newly generated project, operation, actor, workspace, agent, session, graph fixture, and journal ID is a canonical lower-case UUID. Intentionally invalid negative-test inputs are the only exception.
- New local actions are issued only with assurance=local from Gateway-owned human/agent/session records. Legacy and unknown envelopes remain readable historical data and are never issuable for a new action.
- Projectstate Snapshot/OperationV1 is the sole local domain write authority. Legacy task/KB/channel/event/Git replica tables are projections only and no public handler writes them directly.
- Code Graph is private, derivative, per checkout, model-free, vector-free, and offline by default. It persists no source body and never writes into the approved checkout.
- Linux service lifecycle uses systemd-user only when usable. Unsupported/no-manager returns exactly: gatewayd service manager unavailable; start gatewayd manually.
- Connector mutation is allowed only when the existing entry is absent or a fully reconstructable stdio entry. HTTP/SSE, OAuth, literal/env header variants, hidden-scope duplicates, unknown versions, and ambiguous output fail closed before backup or mutation.
- Setup journals and connector backups are 0600 machine-private files; journals contain no raw credential. The one deliberate PII-not-secret exception is the final confirmed identity display name and optional email needed for crash-safe execution; it remains owner-private and is never logged or returned by a public API. Connector backup contents are never logged or returned.
- Interactive setup renders one complete plan and confirms once before any external mutation; the finalized optional journal selection is durable before service, Gateway, identity, connector, Fabric, graph, user-config, or repository effects.
- Confirmation persists config-owned SHA-256 plan/prior/desired digests, safe action metadata, and the required bounded `ConfirmedIdentitySelection`. Resume never derives an unconfirmed action; drift fails with `ErrConfirmedPlanDrift` and performs zero write of any kind, including no journal error update.
- Private setup/connector stores enforce effective-UID ownership and exact modes on Unix. V1 explicitly returns `ErrPrivateStateUnsupported` on non-Unix rather than claiming unimplemented ACL equivalence.
- The public Codex desired command is exactly codex mcp add wormhole -- /absolute/path/to/wormhole mcp.
- Keep merged statement coverage at or above 80 percent. Every task follows RED, GREEN, focused verification, then one explicit commit.

## Shared Slice A contracts consumed

The portable-state plan supplies these packages and responsibilities:

- internal/types/workspace.go: WorkspaceID, WorkspaceScope, CheckoutIdentity, RepositoryIdentity, WorkspaceBinding, and validation.
- internal/types/identity.go: ActorEnvelope and local assurance values.
- internal/types/projectstate: the versioned .wormhole codec, canonical snapshot/digest types, strict manifest validation, and canonical tracked inventory.
- internal/runtime/projectstate: Service registration, ResolveWorkingDirectory, RegisteredWorkspaces, ObserveGitBase, Status, Diff, Apply, Import, Checkpoint, Stash, and Recover.
- internal/runtime/localapi/workspace.go: the single Slice A binding-aware projectstate domain adapter file. Do not create workspace_domain.go or a second workspace adapter.

The single workspace adapter also exposes the composed project-state seam used to retire legacy replica authority:

~~~go
func (d *WorkspaceDomain) View(context.Context, types.WorkspaceBinding) (projectstate.Snapshot, error)
func (d *WorkspaceDomain) Apply(context.Context, types.WorkspaceBinding, types.ActorEnvelope, projectstate.OperationV1) (runtimeprojectstate.WorkspaceStatus, error)
~~~

Consume the frozen binding exactly; do not flatten, wrap, or redeclare it:

~~~go
type WorkspaceBinding struct {
    Scope WorkspaceScope
    Checkout CheckoutIdentity
    Repository RepositoryIdentity
    AcceptedRef string
    AcceptedCommitSHA string
    AcceptedTreeDigest string
}

type RegisterWorkspaceResult struct {
    Binding types.WorkspaceBinding
    Created bool
}
func (s *Service) RegisterWorkspace(context.Context, RegisterWorkspaceRequest) (RegisterWorkspaceResult, error)
func (s *Service) ResolveWorkingDirectory(context.Context, types.WorkspaceContext) (types.WorkspaceBinding, error)
func (s *Service) RegisteredWorkspaces(context.Context) ([]types.WorkspaceBinding, error)
func (s *Service) RefreshWorkspace(context.Context, types.WorkspaceBinding) (types.WorkspaceBinding, error)
~~~

WorkspaceBinding.Validate lives in internal/types and validates AcceptedTreeDigest as a string, avoiding a types-to-projectstate dependency. Registration returns RegisterWorkspaceResult.Binding; working-directory resolution and workspace enumeration return that exact binding directly. No caller may treat RegisterWorkspaceResult itself as the binding. Code Graph has one allowed runtime conversion outside its config package:

~~~go
// package internal/runtime/codegraph/manager
func ScopeFromBinding(binding types.WorkspaceBinding) (codegraphconfig.Scope, error)
~~~

All graph configuration, store, manager, and worker APIs receive codegraphconfig.Scope. codegraph/config imports only the standard library; manager is the only graph package that imports internal/types and converts the binding's validated digest string. No other function reconstructs graph scope from strings, cwd, tool arguments, credentials, or display names.

Positive executable fixtures use these canonical workspace UUIDs consistently:

~~~go
const (
    workspaceA types.WorkspaceID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
    workspaceB types.WorkspaceID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)
~~~

## File map

| Path | Responsibility |
| --- | --- |
| internal/runtime/localapi/workspace.go | Extend Slice A handlers with private context binding; keep public schemas machine-ID-free. |
| internal/runtime/localidentity | Owner-only human, Ed25519 key, durable agent, selection, connection-session, and setup-intent receipt records. |
| internal/runtime/localapi/actor.go | Resolve CLI selection and MCP clientInfo into server-owned local ActorEnvelope values. |
| internal/runtime/localapi/setup.go | Private, non-MCP setup-control RPCs for journal-keyed identity execution and cached graph inspection. |
| internal/runtime/localapi/request_scope.go | Strip bridge context and attach one validated binding to request context. |
| cmd/wormhole/mcp.go | Observe cwd and inject bridge-only context. |
| cmd/gatewayd/gatewayd.go | Construct all local dependencies and recover one supervisor. |
| internal/runtime/codegraph/store | Workspace DB schema, fingerprints, lexical documents, and legacy invalidation. |
| internal/runtime/codegraph/worker | On-demand process manager, private protocol, sandbox policy, crash isolation. |
| internal/runtime/codegraph/manager | Convert frozen bindings to stdlib-only graph scopes and supervise per-workspace workers. |
| cmd/gatewayd/codegraph_worker.go | Hidden child-process entrypoint over inherited private configuration. |
| internal/runtime/config/command.go | Shared no-shell, bounded-output command runner for service, Git identity, and connector primitives. |
| internal/runtime/config/service*.go | systemd-user lifecycle primitive or exact manual-start diagnostic. |
| internal/runtime/config/setup_journal.go | Durable setup stages, required confirmed identity execution intent, digests, and connector backup references. |
| internal/runtime/config/identity_suggestion.go | Read-only Git suggestions/attestation request. |
| internal/runtime/config/connector | Full transactional adapter contract and Codex/Claude implementations. |
| cmd/wormhole/setup.go | Human interaction, setup orchestration, and Gateway RPC calls. |

---

### Task 0: Persist local identities, keys, agents, and connection sessions

**Files:**

- Modify: internal/types/identity.go
- Modify: internal/types/identity_test.go
- Create: internal/runtime/localidentity/store.go
- Create: internal/runtime/localidentity/files.go
- Create: internal/runtime/localidentity/keys.go
- Create: internal/runtime/localidentity/setup.go
- Create: internal/runtime/localidentity/store_test.go
- Create: internal/runtime/localidentity/setup_test.go
- Create: internal/runtime/localapi/actor.go
- Create: internal/runtime/localapi/actor_test.go
- Create: internal/runtime/localapi/setup.go
- Create: internal/runtime/localapi/setup_test.go
- Modify: internal/runtime/localapi/localapi.go
- Modify: internal/runtime/localapi/mcp.go
- Modify: internal/runtime/localapi/mcp_test.go
- Modify: docs/implementation-rules.md

**Produces:**

~~~go
// package internal/types
const (
    ConfirmedIdentityActionEnsureSelected = "ensure-selected"
    ConfirmedIdentityKeyPolicyEnsureEd25519 = "ensure-ed25519"
    MaxConfirmedIdentityDisplayNameBytes = 128
    MaxConfirmedIdentityEmailBytes = 254
)
type ConfirmedIdentitySelection struct {
    Action string `json:"action"`
    DisplayName string `json:"display_name"`
    Email string `json:"email,omitempty"`
    KeyPolicy string `json:"key_policy"`
}
func (s ConfirmedIdentitySelection) Validate() error

// package internal/runtime/localidentity
type HumanProfile struct {
    SchemaVersion int `json:"schema_version"`
    HumanPrincipalID string `json:"human_principal_id"`
    DisplayName string `json:"display_name"`
    Email string `json:"email,omitempty"`
    PublicKey string `json:"public_key"`
    CreatedAt time.Time `json:"created_at"`
}
type AgentProfile struct {
    SchemaVersion int `json:"schema_version"`
    AgentID string `json:"agent_id"`
    AccountableHumanID string `json:"accountable_human_id"`
    HarnessName string `json:"harness_name"`
    CreatedAt time.Time `json:"created_at"`
}
type ConnectionSession struct {
    SchemaVersion int `json:"schema_version"`
    SessionID string `json:"session_id"`
    AgentID string `json:"agent_id,omitempty"`
    HumanPrincipalID string `json:"human_principal_id,omitempty"`
    AccountableHumanID string `json:"accountable_human_id,omitempty"`
    HarnessName string `json:"harness_name"`
    HarnessVersion string `json:"harness_version"`
    ModelName string `json:"model_name,omitempty"`
    ModelVersion string `json:"model_version,omitempty"`
    StartedAt time.Time `json:"started_at"`
}
type MCPClientInfo struct { Name, Version, ModelName, ModelVersion string }
func Open(root string) (*Store, error)
func (s *Store) CreateHuman(context.Context, string, string) (HumanProfile, error)
func (s *Store) Humans(context.Context) ([]HumanProfile, error)
func (s *Store) SelectHuman(context.Context, string) error
func (s *Store) SelectedHuman(context.Context) (HumanProfile, error)
func (s *Store) EnsureAgent(context.Context, string, string) (AgentProfile, error)
func (s *Store) StartHumanSession(context.Context, string, string) (ConnectionSession, error)
func (s *Store) StartAgentSession(context.Context, AgentProfile, MCPClientInfo) (ConnectionSession, error)
func (s *Store) Session(context.Context, string) (ConnectionSession, error)
func (s *Store) Sign(context.Context, string, []byte) ([]byte, error)
var ErrSetupIdentityConflict = errors.New("local identity: setup intent conflict")
func (s *Store) EnsureSelectedForSetup(
    context.Context, string, types.ConfirmedIdentitySelection,
) (HumanProfile, error)

// package internal/runtime/localapi
type LocalActorResolver struct { /* localidentity store plus UTC clock */ }
func NewLocalActorResolver(*localidentity.Store, string) (*LocalActorResolver, error)
func (r *LocalActorResolver) SelectLocalIdentity(context.Context, string) error
func (r *LocalActorResolver) ResolveCLI(context.Context) (types.ActorEnvelope, error)
func (r *LocalActorResolver) OpenMCP(context.Context, MCPClientInfo) (string, error)
func (r *LocalActorResolver) ResolveMCP(context.Context, string) (types.ActorEnvelope, error)
func (r *LocalActorResolver) EnsureSelectedForSetup(
    context.Context, string, types.ConfirmedIdentitySelection,
) (localidentity.HumanProfile, error)
const PrivateSetupEnsureIdentityRPC = "wormhole.private.setup.ensure_identity"
~~~

`ConfirmedIdentitySelection.Validate` accepts only action `ensure-selected` and key policy `ensure-ed25519`. `DisplayName` must be valid UTF-8, 1-128 bytes, equal to its `strings.TrimSpace` result, contain no repeated spaces, and use only Unicode letters/marks/numbers, ASCII space, apostrophe, typographic apostrophe, hyphen, period, comma, or parentheses. Reject case-insensitive credential/key sentinels (`private key`, `token`, `password`, `secret`, `credential`, `authorization`, `bearer`, and PEM begin/end markers) as well. Together those rules exclude `/`, `\\`, `:`, `=`, `@`, JSON delimiters, control characters, PEM text, assignments, credential/path encodings, and raw Git-config syntax. `Email` is absent or 3-254 bytes, contains no control/whitespace, and `mail.ParseAddress` must return the identical address with no display phrase. Validation never normalizes a different value: the displayed value the human confirms is the canonical execution value.

The root is dataHome/wormhole/identities, mode 0700. Profiles, the selected-human pointer, setup-intent receipts, agents, and sessions are canonical JSON files written by temp-file/fsync/rename/fsync-dir with mode 0600. Each human has identity.ed25519.public and identity.ed25519.private outside every repository; both local copies are 0600, the private file is PKCS#8 PEM, and only Sign reads it. CreateHuman uses crypto/ed25519 and crypto/rand. Human, agent, and session IDs are canonical UUIDs generated from crypto/rand; the setup API accepts but never generates Task 8's canonical journal UUID. EnsureAgent is idempotent and unique by accountable-human UUID plus normalized harness name; a version change creates a new session, not a new durable agent.

`EnsureSelectedForSetup` is the sole setup identity mutation. Under the identity-store cross-process lock it validates the journal UUID and confirmed selection, then uses `setup-intents/<journal UUID>.json` as a durable idempotency receipt. A matching completed receipt reloads the same human profile, verifies the same Ed25519 public/private key pair, reselects it idempotently, and returns only the public profile. A receipt with a different selection returns `ErrSetupIdentityConflict` before mutation and without echoing either value. For a new receipt, reuse the selected exact valid profile; otherwise reuse one unique exact valid profile; if two or more unselected exact profiles exist, return `ErrSetupIdentityConflict` without a write; if none exists, reserve one new human UUID. Durably write that UUID and the exact confirmed selection to the receipt before creating its key; key creation is create-if-absent at that UUID, so recovery derives the same public key from the same private key rather than generating a replacement. Then durably write/verify the profile, selected-human pointer, and completed receipt in order. Recovery completes any prefix under the same lock. No crash point can create a second human, change the profile/key, or select a different human.

`PrivateSetupEnsureIdentityRPC` is a same-user Gateway setup-control method, not an MCP tool and absent from `tools/list`, public contract manifests, logs, and normal status output. It accepts only setup journal UUID plus `types.ConfirmedIdentitySelection`, calls the resolver method above, and returns the resulting public `HumanProfile`; it never returns the confirmed selection, private key, signature, receipt path, raw Git config, or credential material.

ResolveCLI uses the selected human and opens a wormhole-cli/version session; it returns ActorHuman, the selected HumanPrincipalID, assurance local, session/harness, and current UTC time. OpenMCP accepts only initialize clientInfo name/version/model metadata, rejects actor/human/agent/session/assurance claims, resolves the selected human, ensures that human's harness agent, and persists a fresh connection session. ResolveMCP reconstructs ActorAgent with that server-owned AgentID, AccountableHumanID, SessionID, harness/model fields, assurance local, and fresh OccurredAt. No issuance path emits legacy, unknown, public-key-continuity, or private-authenticated assurance. Private key bytes/paths never enter an ActorEnvelope, log, Git tree, MCP response, or tracked actor record.

- [ ] **Step 1: Write RED durability, attribution, and secrecy tests.**

~~~go
func TestLocalIdentityRestartPreservesSelectionAgentAndSession(t *testing.T) {
    root := t.TempDir()
    store := mustOpenIdentityStore(t, root)
    human := mustCreateAndSelectHuman(t, store)
    agent := mustEnsureAgent(t, store, human.HumanPrincipalID, "codex")
    session := mustStartAgentSession(t, store, agent, MCPClientInfo{Name:"codex",Version:"1.2.3"})
    reopened := mustOpenIdentityStore(t, root)
    if got := mustSelectedHuman(t,reopened); got.HumanPrincipalID != human.HumanPrincipalID { t.Fatal(got) }
    if got := mustSession(t,reopened,session.SessionID); got.AgentID != agent.AgentID { t.Fatal(got) }
    assertOwnerOnlyIdentityTree(t, root)
}
func TestMCPActorIsServerOwnedLocalAndAccountable(t *testing.T) {
    resolver, human := actorResolverFixture(t)
    sessionID, err := resolver.OpenMCP(t.Context(), MCPClientInfo{Name:"codex",Version:"1.2.3"})
    if err != nil { t.Fatal(err) }
    got, err := resolver.ResolveMCP(t.Context(),sessionID)
    if err != nil || got.Assurance != types.AssuranceLocal || got.AccountableHumanID != human.HumanPrincipalID || got.SessionID != sessionID || got.HarnessName != "codex" { t.Fatalf("%+v %v",got,err) }
    if got.AgentID == "" || got.HarnessVersion != "1.2.3" { t.Fatalf("%+v",got) }
}
func TestNewActorIssuanceCannotUseLegacyUnknownOrSecrets(t *testing.T) {
    resolver, _ := actorResolverFixture(t)
    if _, err := resolver.OpenMCP(t.Context(), forgedClientInfoWithActorClaims()); err == nil { t.Fatal("forged actor accepted") }
    actor := mustResolveMCPActor(t,resolver)
    encoded := mustJSON(t,actor)
    if bytes.Contains(encoded, privateKeyBytes(t,resolver)) || bytes.Contains(encoded, []byte("identity.ed25519.private")) { t.Fatalf("envelope=%s",encoded) }
    assertNoIdentitySecretLoggedOrWrittenToRepo(t,resolver)
}
func TestConfirmedIdentitySelectionValidationIsStrictAndBounded(t *testing.T) {
    valid := types.ConfirmedIdentitySelection{Action:"ensure-selected",DisplayName:"Alice Example",Email:"alice@example.test",KeyPolicy:"ensure-ed25519"}
    if err := valid.Validate(); err != nil { t.Fatal(err) }
    for _, invalid := range invalidConfirmedIdentitySelections(valid) {
        if err := invalid.Validate(); err == nil { t.Fatalf("accepted=%+v",invalid) }
    }
    for _, forbidden := range []string{"/home/alice/.ssh/id_ed25519",`C:\\Users\\alice\\credentials`,"TOKEN=visible","user.name=Alice","-----BEGIN PRIVATE KEY-----"} {
        changed := valid
        changed.DisplayName = forbidden
        if err := changed.Validate(); err == nil { t.Fatalf("forbidden display name accepted=%q",forbidden) }
    }
}
func TestSetupIdentitySurvivesRealProcessCrashAndReopen(t *testing.T) {
    for _, point := range []string{"receipt-durable","key-durable","profile-durable","selected-durable","completed-durable"} {
        fixture := onDiskSetupIdentityFixture(t)
        fixture.RunHelperProcessUntilHardExit(point) // child reads the owner-only on-disk request fixture
        reopened := mustOpenIdentityStore(t,fixture.Root)
        first, err := reopened.EnsureSelectedForSetup(t.Context(),fixture.JournalID,fixture.Selection)
        if err != nil { t.Fatalf("%s: %v",point,err) }
        reopenedAgain := mustOpenIdentityStore(t,fixture.Root)
        second, err := reopenedAgain.EnsureSelectedForSetup(t.Context(),fixture.JournalID,fixture.Selection)
        if err != nil || !reflect.DeepEqual(first,second) { t.Fatalf("%s first=%+v second=%+v err=%v",point,first,second,err) }
        fixture.AssertExactlyOneHumanAndSamePrivateKey(first.HumanPrincipalID)
    }
}
func TestSetupIdentityIntentMismatchFailsWithoutWrite(t *testing.T) {
    store,fixture := setupIdentityFixture(t)
    _, _ = store.EnsureSelectedForSetup(t.Context(),fixture.JournalID,fixture.Selection)
    before := fixture.IdentityTreeBytes()
    _, err := store.EnsureSelectedForSetup(t.Context(),fixture.JournalID,fixture.DifferentSelection())
    if !errors.Is(err,ErrSetupIdentityConflict) || !bytes.Equal(before,fixture.IdentityTreeBytes()) { t.Fatalf("err=%v",err) }

    ambiguous := setupIdentityFixtureWithTwoUnselectedExactHumans(t)
    ambiguousBefore := ambiguous.IdentityTreeBytes()
    _, err = ambiguous.Store.EnsureSelectedForSetup(t.Context(),ambiguous.JournalID,ambiguous.Selection)
    if !errors.Is(err,ErrSetupIdentityConflict) || !bytes.Equal(ambiguousBefore,ambiguous.IdentityTreeBytes()) { t.Fatalf("ambiguous err=%v",err) }
}
func TestPrivateSetupIdentityRPCIsNotAnMCPTool(t *testing.T) {
    server := setupControlFixture(t)
    got := callPrivateSetupIdentityRPC(t,server,server.JournalID,server.Selection)
    if got.HumanPrincipalID == "" || slices.Contains(server.MCPToolNames(),PrivateSetupEnsureIdentityRPC) { t.Fatalf("got=%+v tools=%v",got,server.MCPToolNames()) }
    server.AssertConfirmedIdentityAbsentFromLogsAndPublicResponses()
}
~~~

Run: go test ./internal/types ./internal/runtime/localidentity ./internal/runtime/localapi -run 'Test(ConfirmedIdentity|LocalIdentity|SetupIdentity|PrivateSetupIdentity|MCPActor|CLIActor|NewActorIssuance|IdentityKey)' -count=1

Expected: FAIL because the store and resolver are absent.

- [ ] **Step 2: Implement the file store and resolver, then run GREEN.**

Run: go test ./internal/types ./internal/runtime/localidentity ./internal/runtime/localapi -run 'Test(ConfirmedIdentity|LocalIdentity|SetupIdentity|PrivateSetupIdentity|MCPActor|CLIActor|NewActorIssuance|IdentityKey)' -count=1

Expected: PASS across real helper-process crash/reopen at every identity receipt/key/profile/selection boundary, same-profile/key idempotency, mismatch/ambiguity no-write conflicts, private-RPC exclusion, mode, sign/verify, human+harness isolation, initialize-forgery, local-assurance, and no-secret assertions.

- [ ] **Step 3: Update the module map and commit.**

Add internal/runtime/localidentity as internal/types+stdlib-only and state that only localapi constructs ActorEnvelope values from it.

~~~bash
git add internal/types/identity.go internal/types/identity_test.go internal/runtime/localidentity internal/runtime/localapi/actor.go internal/runtime/localapi/actor_test.go internal/runtime/localapi/setup.go internal/runtime/localapi/setup_test.go internal/runtime/localapi/localapi.go internal/runtime/localapi/mcp.go internal/runtime/localapi/mcp_test.go docs/implementation-rules.md
git commit -m "feat(runtime): persist accountable local identities"
~~~

### Task 1: Add the private cwd envelope and binding-aware dispatch

**Files:**

- Create: internal/runtime/localapi/request_scope.go
- Create: internal/runtime/localapi/request_scope_test.go
- Modify: internal/runtime/localapi/workspace.go
- Modify: internal/runtime/localapi/localapi.go
- Modify: internal/runtime/localapi/mcp.go
- Modify: internal/runtime/localapi/codegraph.go
- Modify: internal/runtime/localapi/guidance.go
- Modify: internal/runtime/localapi/alpha_acceptance_gap_test.go
- Modify: internal/runtime/localapi/corrupt_replica_coverage_test.go
- Modify: internal/runtime/localapi/handler_boundary_coverage_test.go
- Modify: internal/runtime/localapi/localapi_p3_test.go
- Modify: internal/runtime/localapi/localapi_p5_test.go
- Modify: internal/runtime/localapi/localapi_qa_test.go
- Modify: internal/runtime/localapi/localapi_test.go
- Modify: internal/runtime/localapi/localapi_write_test.go
- Modify: internal/runtime/localapi/mcp_test.go
- Modify: internal/runtime/localapi/contract_manifest_test.go
- Modify: cmd/wormhole/mcp.go
- Modify: cmd/wormhole/mcp_stdio_test.go

**Consumes:** Task 0 LocalActorResolver; projectstate.Service.ResolveWorkingDirectory/ObserveGitBase; and the single Slice A WorkspaceDomain View/Apply/workspace-operation adapters.

**Produces:**

~~~go
const privateWorkspaceContextKey = "_wormhole_workspace"
func overwriteBridgeWorkspaceContext(json.RawMessage, string) (json.RawMessage, error)
func withWorkspaceBinding(context.Context, types.WorkspaceBinding) context.Context
func workspaceBindingFromContext(context.Context) (types.WorkspaceBinding, error)
func stripAndResolveWorkspace(context.Context, json.RawMessage, *projectstate.Service) (context.Context, json.RawMessage, error)
func refreshGitBinding(context.Context, types.WorkspaceBinding, *projectstate.Service) (types.WorkspaceBinding, error)
~~~

- [ ] **Step 1: Write failing private-envelope and forgery tests (RED).**

~~~go
func TestStripAndResolveWorkspace_RemovesPrivateEnvelope(t *testing.T) {
    service, binding := registeredWorkspaceFixture(t, "/repo/a")
    raw := json.RawMessage("{\"_wormhole_workspace\":{\"working_directory\":\"/repo/a\"},\"status\":\"todo\"}")
    scoped, forwarded, err := stripAndResolveWorkspace(t.Context(), raw, service)
    if err != nil { t.Fatal(err) }
    got, err := workspaceBindingFromContext(scoped)
    if err != nil || got.Scope.WorkspaceID != binding.Scope.WorkspaceID { t.Fatalf("binding=%+v err=%v", got, err) }
    if bytes.Contains(forwarded, []byte("_wormhole_workspace")) { t.Fatalf("forwarded=%s", forwarded) }
}
func TestBridgeOverwritesHarnessSuppliedPrivateWorkspace(t *testing.T) {
    raw := json.RawMessage(`{"name":"wormhole.workspace.status","arguments":{"_wormhole_workspace":{"working_directory":"/forged/repo"}}}`)
    forwarded, err := overwriteBridgeWorkspaceContext(raw, "/observed/repo")
    if err != nil { t.Fatal(err) }
    got := decodeBridgeWorkspaceContext(t, forwarded)
    if got.WorkingDirectory != "/observed/repo" || bytes.Contains(forwarded, []byte("/forged/repo")) {
        t.Fatalf("forwarded=%s context=%+v", forwarded, got)
    }
}
func TestDispatchRejectsForgedProjectAndCrossWorkspace(t *testing.T) {
    server, a, b := twoWorkspaceServer(t)
    _, err := callToolFromCWD(t, server, "/repo/a", "wormhole.workspace.status", map[string]any{"project_id": b.Scope.ProjectID, "workspace_id": b.Scope.WorkspaceID})
    if err == nil || !strings.Contains(err.Error(), "machine-private scope fields are forbidden") { t.Fatalf("err=%v", err) }
    _, err = callToolFromCWD(t, server, "/repo/b", "wormhole.code_graph.status", map[string]any{"workspace_id": a.Scope.WorkspaceID})
    if err == nil { t.Fatal("cross-workspace graph request succeeded") }
}
~~~

- [ ] **Step 2: Run RED tests.**

Run: go test ./internal/runtime/localapi ./cmd/wormhole -run 'TestStripAndResolveWorkspace|TestDispatchRejectsForged|TestBridgeOverwritesHarnessSuppliedPrivateWorkspace|TestBridgeWorkspaceEnvelope' -count=1

Expected: FAIL because request_scope.go and bridge injection are absent.

- [ ] **Step 3: Implement private routing and migrate every dispatch family (GREEN).**

The bridge reads os.Getwd once per call. overwriteBridgeWorkspaceContext parses the tools/call payload, unconditionally deletes any harness-supplied _wormhole_workspace member, injects exactly one replacement containing the observed cwd, and preserves MCP framing. Gateway removes the private member before public argument-schema validation, calls projectstate.ResolveWorkingDirectory, validates the returned types.WorkspaceBinding, resolves the connection's ActorEnvelope through LocalActorResolver, and attaches both to context before authorization. A private envelope can therefore reach routing but can never make an otherwise-valid public call fail schema validation or reach a handler/Fabric payload.

Change these call-site families to use workspaceBindingFromContext and the server-owned actor: Slice A workspace handlers; task/channel/KB/event/Git reads and writes; sync status and queue selection; proxyAuthenticatedTool/Fabric selection; permission authorization; integration guidance; and Code Graph status/query/rebuild. Reject project_id, workspace_id, checkout_id, working_directory, actor, agent_id, assurance, session_id, accountable_human_id, and Fabric identifiers in public arguments. tools/list schemas must not contain those properties.

Delete direct write authority from internal/runtime/localapi/localapi.go. Exact delegation is: wormhole.task.create -> WorkspaceDomain.CreateTask; wormhole.task.update_status -> WorkspaceDomain.UpdateTaskStatus, which alone constructs the portable atomic ApplyBatch of updated TaskV1 plus immutable EventV1; wormhole.task.route -> WorkspaceDomain.RouteTask after scheduler selection; wormhole.channel.create -> WorkspaceDomain.CreateChannel; wormhole.channel.post -> WorkspaceDomain.PostChannelEvent; wormhole.kb.write -> WorkspaceDomain.WriteArticle; and wormhole.git.link_commit -> WorkspaceDomain.LinkCommit. The seven public mutations therefore append eight OperationV1 rows. The task-status result returns the exact EventID generated for its persisted EventV1; the handler must not generate, replace, or drop it. No handler calls TaskRepo.Create/Assign/UpdateStatus, EventRepo.CreateChannel/PublishEvent, KBRepo.WriteArticle, GitRepo.LinkCommit, beginLocalWrite, or queue EnqueueTx.

Exact projection reads are wormhole.task.list/get, wormhole.channel.list/events, and wormhole.kb.list/get over WorkspaceDomain.View(binding), filtered only inside that binding's composed Snapshot. channel.subscribe remains eventbus-only for new ephemeral notifications, seeded from workspace-scoped durable events; kb.search remains a binding-scoped Fabric call. Legacy task/KB/channel/event/Git tables may be populated only by an explicit projection rebuilder from the composed snapshot and are never read as authority by public handlers.

Before every scoped operation after startup—including status, diff, every pillar write, import, checkpoint, sync/Fabric routing, and graph status/query/rebuild—refreshGitBinding calls the exact Slice-A Service.RefreshWorkspace(binding), which wraps trusted ObserveGitBase with BranchSwitchReject and returns the refreshed binding. ErrBranchSwitchPending, ErrGitObservationChanged, invalid committed snapshots, and conflicts fail the requested operation before OperationV1 construction or graph access. The sole exception is stash: only when that preflight returns ErrBranchSwitchPending may WorkspaceDomain.Stash run with the still-validated binding; the dispatcher then immediately calls RefreshWorkspace on that binding and Recover on the returned refreshed scope, and reports stash success only if both follow-up calls succeed.

- [ ] **Step 4: Add a registry-wide migration assertion and run GREEN tests.**

~~~go
func TestEveryProjectScopedToolUsesResolvedBinding(t *testing.T) {
    server, a, b := twoWorkspaceServer(t)
    for _, tool := range projectScopedTools(server.registry.List()) {
        t.Run(tool.Name, func(t *testing.T) {
            _, err := callToolFromCWD(t, server, a.Checkout.CanonicalPath, tool.Name, minimumValidArguments(tool))
            if err != nil { t.Fatalf("workspace A: %v", err) }
            recorded := recordedBindings(t, server, tool.Name)
            if len(recorded) != 1 || recorded[0].Scope != a.Scope { t.Fatalf("bindings=%+v want=%+v", recorded, a.Scope) }
            _, err = callToolFromCWD(t, server, b.Checkout.CanonicalPath, tool.Name, forgedScopeArguments(tool, a))
            if err == nil { t.Fatal("forged scope accepted") }
        })
    }
}
func TestProjectWritesUseOperationV1NotLegacyRepos(t *testing.T) {
    server, binding, domains, legacy := projectDomainServer(t)
    for _, tool := range []string{"wormhole.task.create","wormhole.task.update_status","wormhole.task.route","wormhole.channel.create","wormhole.channel.post","wormhole.kb.write","wormhole.git.link_commit"} {
        if _, err := callToolFromCWD(t,server,binding.Checkout.CanonicalPath,tool,minimumValidArguments(namedTool(t,server,tool))); err != nil { t.Fatalf("%s: %v",tool,err) }
    }
    if legacy.WriteCount() != 0 || domains.OperationCount() != 8 { t.Fatalf("legacy=%d operations=%d",legacy.WriteCount(),domains.OperationCount()) }
    statusResult := domains.TaskStatusResult()
    statusEvent := domains.TaskStatusEvent()
    if statusResult.EventID == "" || statusResult.EventID != statusEvent.ID { t.Fatalf("result=%+v event=%+v",statusResult,statusEvent) }
    for _, op := range domains.Operations() { if op.Actor.Assurance != types.AssuranceLocal { t.Fatalf("%+v",op.Actor) } }
}
func TestDiffAndCheckpointSeeProjectDomainWrites(t *testing.T) {
    fixture := routedProjectStateFixture(t)
    fixture.Call("wormhole.task.create", validTaskCreateArgs())
    diff := fixture.Diff()
    if !diffContainsKindAndID(diff,"task",fixture.LastOperationRecordID()) { t.Fatalf("diff=%+v",diff) }
    fixture.Checkpoint()
    if !trackedSnapshotContains(t,fixture.Root(),"task",fixture.LastOperationRecordID()) { t.Fatal("checkpoint omitted operation") }
}
func TestCrossWorkspaceProjectionAndForgedActorFailClosed(t *testing.T) {
    fixture := twoWorkspaceProjectDomainFixture(t)
    fixture.CreateTask("a","only-a")
    if fixture.TaskNamed("b","only-a") { t.Fatal("cross-workspace projection leak") }
    if err := fixture.CallWithActorFields("b","wormhole.task.create",forgedActorArgs()); err == nil { t.Fatal("forged actor accepted") }
}
func TestStashAfterBranchPendingRefreshesThenRecovers(t *testing.T) {
    fixture := branchPendingWorkspaceFixture(t)
    if _, err := fixture.Call("wormhole.workspace.status",nil); !errors.Is(err,runtimeprojectstate.ErrBranchSwitchPending) { t.Fatalf("status err=%v",err) }
    if _, err := fixture.Call("wormhole.workspace.stash",map[string]any{"label":"branch switch"}); err != nil { t.Fatal(err) }
    if got := fixture.ProjectStateCalls(); !reflect.DeepEqual(got,[]string{"refresh","refresh","stash","refresh","recover"}) { t.Fatalf("calls=%v",got) }
}
~~~

Run: go test ./internal/runtime/localapi ./cmd/wormhole -run 'Workspace|ResolvedBinding|ProjectWritesUseOperation|DiffAndCheckpoint|CrossWorkspaceProjection|StashAfterBranchPending|Forged|BridgeOverwrites|BridgeWorkspace|ToolSchema' -count=1

Expected: PASS.

- [ ] **Step 5: Commit.**

~~~bash
git add internal/runtime/localapi/request_scope.go internal/runtime/localapi/request_scope_test.go internal/runtime/localapi/workspace.go internal/runtime/localapi/localapi.go internal/runtime/localapi/mcp.go internal/runtime/localapi/codegraph.go internal/runtime/localapi/guidance.go internal/runtime/localapi/alpha_acceptance_gap_test.go internal/runtime/localapi/corrupt_replica_coverage_test.go internal/runtime/localapi/handler_boundary_coverage_test.go internal/runtime/localapi/localapi_p3_test.go internal/runtime/localapi/localapi_p5_test.go internal/runtime/localapi/localapi_qa_test.go internal/runtime/localapi/localapi_test.go internal/runtime/localapi/localapi_write_test.go internal/runtime/localapi/mcp_test.go internal/runtime/localapi/contract_manifest_test.go cmd/wormhole/mcp.go cmd/wormhole/mcp_stdio_test.go
git commit -m "feat(runtime): bind local dispatch to bridge cwd"
~~~

### Task 2: Replace legacy constructors with one fully wired supervisor

**Files:**

- Modify: internal/runtime/localapi/localapi.go
- Modify: internal/runtime/localapi/localapi_test.go
- Modify: cmd/gatewayd/gatewayd.go
- Modify: cmd/gatewayd/gatewayd_test.go
- Modify: cmd/gatewayd/main.go
- Modify: cmd/gatewayd/main_test.go
- Modify: internal/runtime/config/config.go
- Modify: internal/runtime/config/config_test.go

**Consumes:** Task 0 LocalActorResolver; Task 1 request binding/projectstate domain routing; projectstate.Service; eventbus, scheduler, sync status/router, integration manifest service; Task 4 later supplies codegraph/manager.Manager.

**Produces:**

~~~go
var ErrFabricUnavailable = errors.New("localapi: fabric unavailable for workspace")

type FabricRouter interface {
    Status(context.Context, types.WorkspaceBinding) (sync.Status, error)
    Call(context.Context, types.WorkspaceBinding, string, json.RawMessage) (json.RawMessage, error)
}

var ErrCodeGraphUnavailable = errors.New("localapi: code graph unavailable for workspace")

type CodeGraphProvider interface {
    InspectCached(context.Context, types.WorkspaceBinding) (codegraphmanager.Inspection, error)
    Status(context.Context, types.WorkspaceBinding) (codegraphmanager.Status, error)
    Query(context.Context, types.WorkspaceBinding, codegraphquery.Request) (codegraphquery.Result, error)
    Rebuild(context.Context, types.WorkspaceBinding) (codegraphmanager.Status, error)
}

type Dependencies struct {
    Store *localstore.Store
    ProjectState *projectstate.Service
    WorkspaceDomain *WorkspaceDomain
    Actors *LocalActorResolver
    EventBus *eventbus.EventBus
    Scheduler *scheduler.Scheduler
    SyncStatus SyncStatusProvider
    Fabric FabricRouter
    Integration *IntegrationManifestService
    CodeGraphs CodeGraphProvider
}
func NewSupervisor(socketPath string, deps Dependencies) (*Server, error)
func Run(context.Context) error
~~~

These are the complete supervisor-facing interfaces: every method takes the exact resolved binding, and implementations may not accept project/workspace strings as an alternate path. FabricRouter has a localOnlyFabricRouter and, after Slice D, a multi-Fabric router; local-only methods return ErrFabricUnavailable. CodeGraphProvider has a disabledCodeGraphProvider and a manager adapter whose sole conversion is codegraphmanager.ScopeFromBinding. `InspectCached` is the one non-starting preference/cache read and remains available when disabled; worker-backed Status/Query/Rebuild return ErrCodeGraphUnavailable in the disabled provider. NewSupervisor receives non-nil providers in all modes, so no nil-provider branch or unscoped fallback exists.

- [ ] **Step 1: Write failing constructor/recovery tests (RED).**

~~~go
func TestNewSupervisorRejectsIncompleteDependencies(t *testing.T) {
    _, err := NewSupervisor(filepath.Join(t.TempDir(), "gateway.sock"), Dependencies{})
    if err == nil || !strings.Contains(err.Error(), "project state") { t.Fatalf("err=%v", err) }
}
func TestUnavailableProvidersReturnTypedBindingScopedErrors(t *testing.T) {
    binding := validWorkspaceBinding(t)
    fabric := localOnlyFabricRouter{}
    if _, err := fabric.Status(t.Context(), binding); !errors.Is(err, ErrFabricUnavailable) { t.Fatalf("fabric status: %v", err) }
    if _, err := fabric.Call(t.Context(), binding, "wormhole.task.list", nil); !errors.Is(err, ErrFabricUnavailable) { t.Fatalf("fabric call: %v", err) }

    graphs := disabledCodeGraphProvider{}
    if got,err := graphs.InspectCached(t.Context(),binding); err != nil || got.Preference != codegraphmanager.PreferenceOff || got.WorkerRunning { t.Fatalf("graph inspection=%+v err=%v",got,err) }
    if _, err := graphs.Status(t.Context(), binding); !errors.Is(err, ErrCodeGraphUnavailable) { t.Fatalf("graph status: %v", err) }
    if _, err := graphs.Query(t.Context(), binding, codegraphquery.Request{}); !errors.Is(err, ErrCodeGraphUnavailable) { t.Fatalf("graph query: %v", err) }
    if _, err := graphs.Rebuild(t.Context(), binding); !errors.Is(err, ErrCodeGraphUnavailable) { t.Fatalf("graph rebuild: %v", err) }
}
func TestGatewayRecoversAllWorkspacesBeforeServing(t *testing.T) {
    runtime := gatewayFixture(t, []types.WorkspaceID{workspaceA,workspaceB})
    runtime.RecoverError[workspaceB] = errors.New("damaged")
    server, err := runtime.Start(t.Context())
    if err != nil { t.Fatal(err) }
    if !runtime.Recovered[workspaceA] || !runtime.Unavailable[workspaceB] { t.Fatalf("runtime=%+v", runtime) }
    if !serverIsServing(server) { t.Fatal("healthy workspace did not serve") }
}
func TestGatewayRecoversThenRefreshesEveryBindingBeforeServing(t *testing.T) {
    runtime := gatewayFixture(t, []types.WorkspaceID{workspaceA,workspaceB})
    runtime.AdvanceCommittedBase(workspaceA)
    if _, err := runtime.Start(t.Context()); err != nil { t.Fatal(err) }
    if !reflect.DeepEqual(runtime.CallOrder(workspaceA),[]string{"recover","refresh"}) || !reflect.DeepEqual(runtime.CallOrder(workspaceB),[]string{"recover","refresh"}) { t.Fatalf("calls=%v",runtime.AllCalls()) }
    if runtime.Binding(workspaceA).AcceptedCommitSHA != runtime.ObservedHEAD(workspaceA) { t.Fatalf("binding=%+v",runtime.Binding(workspaceA)) }
}
~~~

- [ ] **Step 2: Run RED tests.**

Run: go test ./internal/runtime/localapi ./cmd/gatewayd -run 'TestNewSupervisor|TestUnavailableProviders|TestGatewayRecoversAllWorkspaces|TestGatewayRecoversThenRefreshesEveryBinding|TestRunMainRejectsProfile' -count=1

Expected: FAIL because NewSupervisor is absent and daemon still selects profiles.

- [ ] **Step 3: Implement constructor and no-profile startup (GREEN).**

NewSupervisor creates the one registry after validating all dependencies; delete New, NewWithRuntime, and NewMultiOrg after call sites migrate. It does not accept legacy TaskRepo/EventRepo/KBRepo/GitRepo as write authorities. Run takes no profile/positional argument, opens one control DB, constructs projectstate, WorkspaceDomain, LocalActorResolver, integration, sync/Fabric adapter, and graph manager. For every RegisteredWorkspaces binding it calls Recover, then Service.RefreshWorkspace(binding), then recovers its worker with the returned binding. Refresh failures mark only that workspace unavailable with the exact branch/pending/conflict error; healthy workspaces still serve. Only after that ordered pass does it bind the stable socket and signal Serving.

Do not implement wormhole project or wormhole fabric commands here. Their profile registry, attach/detach/login, and final multi-Fabric router are owned by the Slice D plan. B exposes only the FabricRouter seam and local-only behavior; C setup consumes D RPCs only when D is present.

- [ ] **Step 4: Run GREEN tests.**

Run: go test ./internal/runtime/localapi ./cmd/gatewayd ./internal/runtime/config -run 'Supervisor|Recover|NoProfile|StaleSocket|MultiFabric|LocalOnly' -count=1

Expected: PASS.

- [ ] **Step 5: Commit.**

~~~bash
git add internal/runtime/localapi/localapi.go internal/runtime/localapi/localapi_test.go cmd/gatewayd/gatewayd.go cmd/gatewayd/gatewayd_test.go cmd/gatewayd/main.go cmd/gatewayd/main_test.go internal/runtime/config/config.go internal/runtime/config/config_test.go
git commit -m "feat(gateway): wire one recovered supervisor"
~~~

### Task 3: Scope and migrate the per-workspace Code Graph database

**Files:**

- Create: internal/runtime/codegraph/config/scope.go
- Create: internal/runtime/codegraph/config/scope_test.go
- Create: internal/runtime/codegraph/manager/scope.go
- Create: internal/runtime/codegraph/manager/scope_test.go
- Create: internal/runtime/codegraph/store/path.go
- Create: internal/runtime/codegraph/store/path_test.go
- Create: internal/runtime/codegraph/store/migration.go
- Create: internal/runtime/codegraph/store/migration_test.go
- Create: internal/runtime/localstore/migrations/000003_invalidate_legacy_codegraph.sql
- Create: internal/runtime/localstore/codegraph_invalidation.go
- Create: internal/runtime/localstore/codegraph_invalidation_test.go
- Modify: internal/runtime/localstore/migrations.go
- Modify: internal/runtime/localstore/migrations_test.go
- Modify: internal/runtime/codegraph/config/config.go
- Modify: internal/runtime/codegraph/store/store.go
- Modify: internal/runtime/codegraph/store/schema_test.go

**Consumes:** Slice-A's single `gateway_schema_migrations` ledger after portable transitions at `GatewaySchemaVersion = 2`; stdlib strings only for graph configuration. Task 3 introduces the conversion in the manager package before Task 4 builds the full manager; config never owns it.

**Produces:** the same `internal/runtime/localstore` migration constant advanced to `GatewaySchemaVersion = 3` after `000003_invalidate_legacy_codegraph.sql`, plus:

~~~go
type Scope struct {
    ProjectID string
    WorkspaceID string
    CheckoutPath string
    CheckoutDevice uint64
    CheckoutInode uint64
    RepositoryProvider string
    RepositoryImmutableID string
    RepositoryCanonicalRemote string
    AcceptedRef string
    AcceptedCommitSHA string
    AcceptedTreeDigest string
}
func (s Scope) Validate() error
func DerivativePath(dataHome, workspaceID string) (string, error)
func Open(context.Context, Scope, string) (*Store, error)
type FreshnessRecord struct {
    SourceFingerprint string
    AnalysisFingerprint string
    DirtyTracked bool
    GraphSchemaVersion string
    AdapterVersion string
    ToolchainIdentity string
}
~~~

config/scope.go and every other config file import only stdlib. They do not import internal/types or internal/types/projectstate; AcceptedTreeDigest remains the validated sha256 string used in fingerprints and SQL.

Gateway migration 000003 uses the existing gateway_schema_migrations ledger, advances the sole localstore `GatewaySchemaVersion` constant from 2 to 3, and uses exact DDL:

~~~sql
CREATE TABLE legacy_codegraph_invalidations (
  project_id TEXT NOT NULL,
  legacy_revision_id TEXT NOT NULL,
  legacy_schema_version INTEGER NOT NULL CHECK(legacy_schema_version > 0),
  rebuild_required INTEGER NOT NULL DEFAULT 1 CHECK(rebuild_required = 1),
  invalidated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,legacy_revision_id)
);
~~~

~~~go
type LegacyCodeGraphInvalidation struct {
    ProjectID string
    LegacyRevisionID string
    LegacySchemaVersion int
    RebuildRequired bool
    InvalidatedAt time.Time
}
func (s *localstore.Store) InvalidateLegacyCodeGraph(context.Context) ([]LegacyCodeGraphInvalidation, error)
~~~

InvalidateLegacyCodeGraph owns the control-DB transaction. It shape-checks optional legacy codegraph_schema_migrations/config/revisions tables, records every legacy project/revision (empty revision ID for config-only projects) with INSERT OR IGNORE under BEGIN IMMEDIATE, and leaves legacy tables intact as diagnostic evidence. Missing legacy tables return an empty result. A newer/unknown legacy schema fails closed. The supervisor calls this localstore API once before constructing graph managers; no internal/runtime/codegraph package opens, queries, migrates, or writes the Gateway control DB.

Schema version 3 is exact:

- codegraph_config primary key project_id, workspace_id; columns checkout_path, checkout_device, checkout_inode, repository_identity_json, accepted_ref, accepted_commit_sha, accepted_tree_digest, enabled, source_byte_ceiling, active_revision_id, last_successful_build.
- codegraph_revisions primary key project_id, workspace_id, revision_id; columns state, indexed_commit, source_fingerprint, analysis_fingerprint, dirty_tracked, graph_schema_version, adapter_version, toolchain_identity, created_at, completed_at.
- every node/file/symbol/edge/diagnostic/lexical row includes project_id, workspace_id, revision_id in its key.
- codegraph_lexical_docs has composite project/workspace/revision/node key, algorithm version, exact normalized qualified/symbol names, canonical JSON token arrays, and six field lengths.
- codegraph_lexical_terms has revision-scoped field/token/node/term-frequency inverted rows.
- codegraph_lexical_stats has revision-scoped field/token document frequency plus revision document count and summed field lengths.

All three lexical tables use composite foreign keys with `ON DELETE CASCADE`. Candidate failure, dead-build reclaim, retirement cleanup, and disable must leave zero orphan lexical rows. Task 3 owns this schema and cascading lifecycle; Task 6 owns analyzer metadata extraction, lexical population, pinned-revision query, and acceptance.

- [ ] **Step 1: Write failing conversion/path/schema tests (RED).**

~~~go
func TestScopeFromBindingIsOnlyValidatedConversion(t *testing.T) {
    binding := validWorkspaceBinding(t)
    got, err := manager.ScopeFromBinding(binding)
    if err != nil { t.Fatal(err) }
    if got.ProjectID != binding.Scope.ProjectID || got.WorkspaceID != string(binding.Scope.WorkspaceID) || got.CheckoutPath != binding.Checkout.CanonicalPath ||
        got.RepositoryCanonicalRemote != binding.Repository.CanonicalRemote ||
        got.AcceptedRef != binding.AcceptedRef || got.AcceptedCommitSHA != binding.AcceptedCommitSHA ||
        got.AcceptedTreeDigest != binding.AcceptedTreeDigest { t.Fatalf("scope=%+v binding=%+v", got, binding) }
}
func TestDerivativePathRejectsTraversalAndSeparatesWorkspaces(t *testing.T) {
    root := t.TempDir()
    if _, err := DerivativePath(root, "../escape"); err == nil { t.Fatal("traversal accepted") }
    a, _ := DerivativePath(root, string(workspaceA))
    b, _ := DerivativePath(root, string(workspaceB))
    if a == b || filepath.Dir(a) != filepath.Join(root,"wormhole","codegraph") { t.Fatalf("a=%q b=%q",a,b) }
}
func TestGatewayMigration3InvalidatesLegacyGraphIdempotently(t *testing.T) {
    store := legacyGraphControlStore(t)
    first, err := store.InvalidateLegacyCodeGraph(t.Context())
    if err != nil { t.Fatal(err) }
    second, err := store.InvalidateLegacyCodeGraph(t.Context())
    if err != nil || !reflect.DeepEqual(first,second) { t.Fatalf("first=%+v second=%+v err=%v",first,second,err) }
    assertGatewayMigrationVersion(t,store,3)
    assertLegacyGraphEvidenceIntact(t,store)
}
func TestSchemaV3ContainsWorkspaceAndFingerprints(t *testing.T) {
    db := openGraphDB(t, validScope(t))
    assertColumns(t,db,"codegraph_revisions","project_id","workspace_id","revision_id","source_fingerprint","analysis_fingerprint","dirty_tracked","graph_schema_version","adapter_version","toolchain_identity")
}
~~~

- [ ] **Step 2: Run RED tests.**

Run: go test ./internal/runtime/localstore ./internal/runtime/codegraph/config ./internal/runtime/codegraph/store ./internal/runtime/codegraph/manager -run 'ScopeFromBinding|DerivativePath|SchemaV3|GatewayMigration3|LegacyProjectGraph' -count=1

Expected: FAIL because scope conversion and schema v3 are absent.

- [ ] **Step 3: Implement scoped database and legacy invalidation (GREEN).**

manager.ScopeFromBinding first calls binding.Validate, then copies its fields into the string-only config.Scope. DerivativePath requires a canonical UUID string, creates dataHome/wormhole/codegraph with 0700, and returns workspace-UUID.db beneath that exact directory. Open creates an owner-only file-backed SQLite DB and retains Scope. All SQL predicates and insert keys use scope.ProjectID, scope.WorkspaceID, and revision_id. The localstore loader still embeds the same numbered one-way files and now requires and applies the contiguous sequence `000001` through `000003`; no second ledger or graph-specific migration constant is introduced.

The supervisor translates localstore invalidation records into rebuild-required flags without mapping ambiguous legacy rows to a checkout. A new workspace graph starts disabled/rebuild-required. Restart repeats safely without deleting diagnostic evidence.

- [ ] **Step 4: Run GREEN tests.**

Run: go test ./internal/runtime/localstore ./internal/runtime/codegraph/config ./internal/runtime/codegraph/store ./internal/runtime/codegraph/manager -run 'Scope|Workspace|SchemaV3|Legacy|GatewayMigration|Restart|CrossProject|ControlDB' -count=1

Expected: PASS.

- [ ] **Step 5: Commit.**

~~~bash
git add internal/runtime/localstore/migrations/000003_invalidate_legacy_codegraph.sql internal/runtime/localstore/codegraph_invalidation.go internal/runtime/localstore/codegraph_invalidation_test.go internal/runtime/localstore/migrations.go internal/runtime/localstore/migrations_test.go internal/runtime/codegraph/config/scope.go internal/runtime/codegraph/config/scope_test.go internal/runtime/codegraph/config/config.go internal/runtime/codegraph/manager/scope.go internal/runtime/codegraph/manager/scope_test.go internal/runtime/codegraph/store/path.go internal/runtime/codegraph/store/path_test.go internal/runtime/codegraph/store/migration.go internal/runtime/codegraph/store/migration_test.go internal/runtime/codegraph/store/store.go internal/runtime/codegraph/store/schema_test.go
git commit -m "feat(codegraph): scope databases by workspace"
~~~

### Task 4: Add isolated on-demand Code Graph worker processes

**Files:**

- Create: internal/runtime/codegraph/worker/protocol.go
- Create: internal/runtime/codegraph/worker/child.go
- Create: internal/runtime/codegraph/worker/child_test.go
- Create: internal/runtime/codegraph/worker/environment.go
- Create: internal/runtime/codegraph/worker/environment_test.go
- Create: internal/runtime/codegraph/manager/manager.go
- Create: internal/runtime/codegraph/manager/manager_test.go
- Create: internal/runtime/codegraph/manager/inspection_test.go
- Create: cmd/gatewayd/codegraph_worker.go
- Create: cmd/gatewayd/codegraph_worker_test.go
- Modify: cmd/gatewayd/main.go
- Modify: internal/runtime/localapi/localapi.go
- Modify: internal/runtime/localapi/codegraph.go
- Modify: internal/runtime/localapi/setup.go
- Modify: internal/runtime/localapi/setup_test.go
- Modify: docs/implementation-rules.md

**Consumes:** Task 0's private setup-control RPC file and Task 3 Scope/store plus existing index/query services.

**Produces:**

~~~go
type State string
const (
    StateDisabled State = "disabled"
    StateStarting State = "starting"
    StateReady State = "ready"
    StateStale State = "stale"
    StateBuilding State = "building"
    StateUnavailable State = "unavailable"
)
type Status struct {
    WorkspaceID string
    State State
    Enabled bool
    RebuildRequired bool
    ActiveRevisionID string
    IndexedCommitSHA string
    IndexedSourceFingerprint string
    CurrentSourceFingerprint string
    IndexedAnalysisFingerprint string
    CurrentAnalysisFingerprint string
    DirtyTracked bool
    GraphNotCurrent bool
    RebuildRecommended bool
    Detail string
}
type Preference string
const (
    PreferenceUnset Preference = "unset"
    PreferenceOn Preference = "on"
    PreferenceOff Preference = "off"
)
type Inspection struct {
    WorkspaceID string
    Preference Preference
    HasCachedStatus bool
    CachedStatus Status
    WorkerRunning bool
}
type Manager struct { /* process table keyed by WorkspaceID */ }
func NewManager(runtimeRoot, dataHome, gatewayExecutable string) (*Manager, error)
func (m *Manager) InspectCached(context.Context, codegraphconfig.Scope) (Inspection, error)
func (m *Manager) Status(context.Context, codegraphconfig.Scope) (Status, error)
func (m *Manager) Query(context.Context, codegraphconfig.Scope, query.Request) (query.Result, error)
func (m *Manager) Rebuild(context.Context, codegraphconfig.Scope) (Status, error)
func (m *Manager) Disable(context.Context, codegraphconfig.Scope) error
func (m *Manager) Recover(context.Context, codegraphconfig.Scope) error

// package internal/runtime/localapi
const PrivateSetupInspectCodeGraphRPC = "wormhole.private.setup.inspect_code_graph"
~~~

These declarations live in package codegraphmanager. Worker protocol structs are private wire records converted by Manager and may not define another exported Status. `Inspection.CachedStatus` is only the last durable status projection already present in the per-workspace database; it does not claim recomputed freshness. localapi status/rebuild responses are projections of codegraphmanager.Status without inventing a lifecycle type.

Each workspace uses dataHome/wormhole/codegraph/workspace-ID.db and runtimeRoot/wormhole/codegraph/workspace-ID.sock. Manager starts a child on first status/query/rebuild, passes scope over an inherited private descriptor, and communicates over the owner-only socket. The hidden child entrypoint activates only when the inherited descriptor and internal marker both validate; ordinary gatewayd still accepts no arguments.

`InspectCached` reads only the durable preference/config row, last cached status fields, and the manager process table; it never calls the child-start path, opens a worker socket, recomputes fingerprints, analyzes source, or changes preference. Task 4 extends Task 0's `internal/runtime/localapi/setup.go` with exact method `PrivateSetupInspectCodeGraphRPC`; it consumes the resolved binding and is absent from MCP `tools/list` and the public contract inventory. The Task 0 identity method and this graph inspection are the complete private setup-control RPC surface at this point. Worker-backed `Status` remains the public currentness read and may start a child, but setup may call it only after consent is finalized.

- [ ] **Step 1: Write failing worker isolation tests (RED).**

~~~go
func TestManagerStartsOneChildPerWorkspaceOnDemand(t *testing.T) {
    manager, probe := workerManagerFixture(t)
    if probe.Starts() != 0 { t.Fatal("eager child start") }
    _, _ = manager.Status(t.Context(), scopeFor(t,workspaceA))
    _, _ = manager.Status(t.Context(), scopeFor(t,workspaceA))
    _, _ = manager.Status(t.Context(), scopeFor(t,workspaceB))
    if probe.StartsFor(workspaceA) != 1 || probe.StartsFor(workspaceB) != 1 { t.Fatalf("starts=%v",probe.All()) }
}
func TestInspectCachedPreferenceNeverStartsWorker(t *testing.T) {
    manager, probe := workerManagerFixtureWithPreference(t,PreferenceOn)
    got, err := manager.InspectCached(t.Context(),scopeFor(t,workspaceA))
    if err != nil || got.Preference != PreferenceOn || !got.HasCachedStatus { t.Fatalf("got=%+v err=%v",got,err) }
    if probe.Starts() != 0 || got.WorkerRunning { t.Fatalf("starts=%d got=%+v",probe.Starts(),got) }
    callPrivateSetupGraphInspectionRPC(t,PrivateSetupInspectCodeGraphRPC,manager,scopeFor(t,workspaceA))
    if probe.Starts() != 0 { t.Fatalf("RPC started worker: %d",probe.Starts()) }
}
func TestWorkerCrashDoesNotAffectGatewayOrOtherWorkspace(t *testing.T) {
    manager, probe := workerManagerFixture(t)
    a, b := scopeFor(t,workspaceA), scopeFor(t,workspaceB)
    mustRebuild(t,manager,a); mustRebuild(t,manager,b)
    probe.Kill(workspaceA)
    if _, err := manager.Query(t.Context(),b,knownQuery()); err != nil { t.Fatalf("workspace b: %v",err) }
    if _, err := manager.Status(t.Context(),a); err != nil { t.Fatalf("workspace a restart: %v",err) }
    if probe.StartsFor(workspaceA) != 2 { t.Fatalf("starts=%v",probe.All()) }
}
func TestWorkerCannotWriteApprovedCheckout(t *testing.T) {
    manager := realWorkerFixture(t, readOnlyCheckoutFixture(t))
    before := treeDigest(t,manager.Scope().CheckoutPath)
    _, _ = manager.Rebuild(t.Context(),manager.Scope())
    after := treeDigest(t,manager.Scope().CheckoutPath)
    if before != after { t.Fatalf("checkout changed: %s -> %s",before,after) }
}
~~~

- [ ] **Step 2: Run RED tests.**

Run: go test ./internal/runtime/codegraph/manager ./internal/runtime/codegraph/worker ./internal/runtime/localapi ./cmd/gatewayd -run 'Worker|Manager|InspectCached|SetupGraphInspection|Status|Crash|ReadOnly' -count=1

Expected: FAIL because worker package and child entrypoint are absent.

- [ ] **Step 3: Implement process, filesystem, and credential isolation (GREEN).**

Child cwd is a private temporary directory, never the checkout. It receives only the stdlib-only Scope, graph DB/socket paths, byte limits, and build configuration. Environment is an allowlist containing PATH plus GOOS/GOARCH and private HOME, GOCACHE, GOPATH, GOMODCACHE; force GOENV=off, GOPROXY=off, GOSUMDB=off, GOVCS=*:off, GOTOOLCHAIN=local. Remove HTTP_PROXY, HTTPS_PROXY, ALL_PROXY, NO_PROXY, SSH_AUTH_SOCK, Git credential variables, Wormhole tokens, Fabric URLs, Passport data, and credential paths. Source reads use existing no-follow/hash validation rooted at Scope.CheckoutPath; all writes target private cache/DB/socket directories.

Update docs/implementation-rules.md with explicit rows: codegraph/config -> stdlib only; codegraph/worker -> config/store/index/query plus stdlib, never internal/types; codegraph/manager -> internal/types, config, worker, query, and stdlib. manager.ScopeFromBinding is the sole types.WorkspaceBinding conversion.

A crash marks only that workspace worker unavailable, preserves the last published DB, and permits an on-demand restart. Disable stops only the selected child, closes its socket, removes its DB/socket/enablement, and leaves another workspace intact.

- [ ] **Step 4: Run GREEN tests.**

Run: go test ./internal/runtime/codegraph/manager ./internal/runtime/codegraph/worker ./internal/runtime/localapi ./cmd/gatewayd -run 'Worker|Manager|InspectCached|SetupGraphInspection|Status|OnDemand|Crash|Restart|ReadOnly|Sanitized|NoFabricCredential|Disable' -count=1

Expected: PASS.

- [ ] **Step 5: Commit.**

~~~bash
git add internal/runtime/codegraph/worker/protocol.go internal/runtime/codegraph/worker/child.go internal/runtime/codegraph/worker/child_test.go internal/runtime/codegraph/worker/environment.go internal/runtime/codegraph/worker/environment_test.go internal/runtime/codegraph/manager/scope.go internal/runtime/codegraph/manager/scope_test.go internal/runtime/codegraph/manager/manager.go internal/runtime/codegraph/manager/manager_test.go internal/runtime/codegraph/manager/inspection_test.go cmd/gatewayd/codegraph_worker.go cmd/gatewayd/codegraph_worker_test.go cmd/gatewayd/main.go internal/runtime/localapi/localapi.go internal/runtime/localapi/codegraph.go internal/runtime/localapi/setup.go internal/runtime/localapi/setup_test.go docs/implementation-rules.md
git commit -m "feat(codegraph): isolate on-demand workers"
~~~

### Task 5: Compute complete fingerprints and enforce offline analysis

**Files:**

- Create: internal/runtime/codegraph/config/fingerprint.go
- Create: internal/runtime/codegraph/config/fingerprint_test.go
- Create: internal/runtime/codegraph/golang/offline.go
- Create: internal/runtime/codegraph/golang/offline_test.go
- Create: internal/runtime/codegraph/query/freshness_test.go
- Modify: internal/runtime/codegraph/golang/analyzer.go
- Modify: internal/runtime/codegraph/index/inventory.go
- Modify: internal/runtime/codegraph/index/inventory_test.go
- Modify: internal/runtime/codegraph/index/build.go
- Modify: internal/runtime/codegraph/store/store.go
- Modify: internal/runtime/codegraph/query/query.go
- Modify: internal/runtime/localapi/codegraph.go
- Modify: internal/runtime/localapi/codegraph_status_test.go

**Produces:**

~~~go
type AnalysisInputs struct {
    SourceFingerprint string
    TrackedInputs []TrackedInput
    BuildConstraints string
    Target string
    AdapterConfiguration string
    GraphSchemaVersion string
    AdapterVersion string
    ToolchainIdentity string
}
type Fingerprints struct { Source, Analysis string; DirtyTracked bool }
func ComputeFingerprints(context.Context, codegraphconfig.Scope, AnalysisInputs) (Fingerprints, error)
var ErrGraphNotCurrent = errors.New("codegraph query: graph is not current")
var ErrDependencyUnavailableOffline = errors.New("codegraph go: dependency unavailable while offline")
~~~

Task 5 populates the fingerprint, dirty-tree, currentness, and rebuild fields on the sole exported `codegraphmanager.Status` declared by Task 4. Store and worker protocol implementations may use unexported persistence/wire records, but no graph package introduces a second exported freshness or lifecycle status type.

The Go declared input set is tracked source plus go.mod, go.sum, go.work, go.work.sum, adapter config, normalised build tags/GOOS/GOARCH/CGO, graph/adapter schema versions, runtime.Version, and go env GOTOOLDIR identity.

- [ ] **Step 1: Write failing fingerprint/offline tests (RED).**

~~~go
func TestAnalysisFingerprintChangesForEveryDeclaredClass(t *testing.T) {
    base := analysisFixture(t)
    want := fingerprint(t,base)
    for name, changed := range mutationsByClass(base) {
        t.Run(name,func(t *testing.T) {
            if got := fingerprint(t,changed); got.Analysis == want.Analysis { t.Fatalf("unchanged class %s",name) }
        })
    }
}
func TestGoPackagesBuildCannotAttemptNetwork(t *testing.T) {
    logPath := filepath.Join(t.TempDir(),"go-invocations")
    analyzer := analyzerWithFakeGo(t,logPath)
    err := analyzer.Analyze(t.Context(),missingDependencyFixture(t))
    if !errors.Is(err,ErrDependencyUnavailableOffline) { t.Fatalf("err=%v",err) }
    invocation := readInvocation(t,logPath)
    for _, forbidden := range []string{"HTTP_PROXY=","HTTPS_PROXY=","SSH_AUTH_SOCK="} {
        if strings.Contains(invocation,forbidden) { t.Fatalf("network environment leaked: %s",invocation) }
    }
    if !strings.Contains(invocation,"GOPROXY=off") || !strings.Contains(invocation,"GOVCS=*:off") { t.Fatalf("offline policy missing: %s",invocation) }
}
~~~

- [ ] **Step 2: Run RED tests.**

Run: go test ./internal/runtime/codegraph/config ./internal/runtime/codegraph/golang ./internal/runtime/codegraph/query -run 'Fingerprint|Manifest|Offline|Network|GraphNotCurrent' -count=1

Expected: FAIL because analysis fingerprint and offline policy are incomplete.

- [ ] **Step 3: Implement canonical fingerprints and fail-closed publication (GREEN).**

Use internal/types/projectstate canonical tracked inventory. Sort paths bytewise, hash path length/path/content length/content using SHA-256, include dirty tracked bytes, exclude untracked files, and include all declared non-source inputs. Persist exact freshness columns from Task 3. Recompute on status/query/restart. Query returns ErrGraphNotCurrent before reading matches/edges/source. Rebuild fingerprints before analysis and inside publication guard; a changed or failed publish preserves diagnostic/candidate rows and prior active revision but cannot serve it as current.

Apply Task 4 offline environment to go/packages. Do not run go mod download inside index/build/worker. A separate explicit dependency-fetch consent flow may be designed in setup later; absence returns ErrDependencyUnavailableOffline with module/path detail.

- [ ] **Step 4: Run GREEN tests.**

Run: go test ./internal/runtime/codegraph/config ./internal/runtime/codegraph/golang ./internal/runtime/codegraph/index ./internal/runtime/codegraph/query ./internal/runtime/localapi -run 'Fingerprint|Manifest|Ordering|Offline|Network|Restart|FailedPublish|Invalidated|GraphNotCurrent' -count=1

Expected: PASS.

- [ ] **Step 5: Commit.**

~~~bash
git add internal/runtime/codegraph/config/fingerprint.go internal/runtime/codegraph/config/fingerprint_test.go internal/runtime/codegraph/golang/offline.go internal/runtime/codegraph/golang/offline_test.go internal/runtime/codegraph/golang/analyzer.go internal/runtime/codegraph/index/inventory.go internal/runtime/codegraph/index/inventory_test.go internal/runtime/codegraph/index/build.go internal/runtime/codegraph/store/store.go internal/runtime/codegraph/query/query.go internal/runtime/codegraph/query/freshness_test.go internal/runtime/localapi/codegraph.go internal/runtime/localapi/codegraph_status_test.go
git commit -m "feat(codegraph): enforce offline analysis freshness"
~~~

### Task 6: Implement versioned lexical BM25 and acceptance gates

**Files:**

- Modify: internal/runtime/codegraph/golang/analyzer.go
- Modify: internal/runtime/codegraph/golang/analyzer_test.go
- Modify: internal/runtime/codegraph/index/index.go
- Modify: internal/runtime/codegraph/index/index_test.go
- Modify: internal/runtime/codegraph/index/build.go
- Create: internal/runtime/codegraph/index/build_test.go
- Modify: internal/runtime/codegraph/store/store.go
- Modify: internal/runtime/codegraph/store/schema_test.go
- Create: internal/runtime/codegraph/query/lexical.go
- Create: internal/runtime/codegraph/query/lexical_test.go
- Create: internal/runtime/codegraph/query/ranking_acceptance_test.go
- Create: internal/runtime/codegraph/query/scale_acceptance_test.go
- Create: internal/runtime/codegraph/query/testdata/heldout/documents.json
- Create: internal/runtime/codegraph/query/testdata/heldout/queries.json
- Modify: internal/runtime/codegraph/query/query.go

**Consumes:** Task 3's three revision-scoped lexical tables and cascading lifecycle; Task 5's pinned active-revision `ReadActive` callback and currentness gate.

**Produces:**

~~~go
type LexicalDocument struct {
    NodeID, StableID                 string
    ExactQualified, ExactSymbol      string
    QualifiedName, SymbolName        []string
    SignatureType, PackagePath       []string
    FilePath, Documentation          []string
}
func (s *Store) PutLexicalDocuments(
    context.Context, string, []LexicalDocument,
) error
func (s *Snapshot) SearchLexical(
    context.Context, LexicalRequest,
) ([]LexicalHit, error)
var ErrIncompleteLexicalRevision = errors.New("codegraph store: incomplete lexical revision")
~~~

Add `SelectedSourcePaths []string` to the existing `query.Request`.

`PutLexicalDocuments` populates Task 3's docs, terms, and stats for one candidate revision. `SearchLexical` runs inside the same `ReadActive` callback as edges and source; `N`, per-field `df`, document counts, summed lengths, and average lengths come only from that pinned revision. Query tokens are deduplicated after canonical tokenization, while document length retains repeated tokens.

`internal/runtime/codegraph/index/build.go` constructs all lexical documents from the analyzed candidate and calls `PutLexicalDocuments` after candidate nodes/symbols/edges are durable but before requesting candidate publication. Inside the same publication transaction that changes `active_revision_id`, the store validates lexical completeness: exactly one document exists for every package and symbol node and no other node; every document's node/stable-ID pair and algorithm version match its candidate row; its six stored lengths match its canonical token arrays; the inverted term rows exactly aggregate those token multisets; and revision document count, summed field lengths, and per-token document frequencies exactly recompute from the documents. Any missing, extra, or inconsistent document/term/stat row returns `ErrIncompleteLexicalRevision`, leaves the previous active revision unchanged, and lets candidate cleanup cascade all lexical rows.

Extend analyzer results with transient, non-source-body documentation metadata:

Add an exported `Documentation string` field to both existing analyzer result types, `golang.Package` and `golang.Symbol`.

The analyzer reads `ast.File.Doc`, `FuncDecl.Doc`, and declaration/spec docs. Package comments produce package lexical documents; declaration docs populate symbol documents. Persist only canonical documentation tokens, never raw comments or source bodies.

**Algorithm contract:**

- LexicalAlgorithmVersion = lexical-bm25-v1.
- Tokenise UTF-8 by letter/digit class, lower-to-upper transitions, acronym boundary before the final upper followed by lower, underscore/path/punctuation, and letter/digit transitions; lower-case with unicode.ToLower; discard empty tokens.
- Fields and weights: qualified_name 8, symbol_name 6, signature_and_type 3, package_path 2, file_path 2, documentation 1.
- BM25 constants: k1=1.2 and b=0.75. Per-field score is weight multiplied by IDF multiplied by tf*(k1+1)/(tf+k1*(1-b+b*docLength/averageLength)).
- IDF is ln(1+(N-df+0.5)/(df+0.5)).
- Convert total score to fixedScore = floor(score*1,000,000+0.5), stored/compared as int64.
- Sort: exact qualified tier first, exact unqualified symbol tier second, BM25 tier third; then fixedScore descending; then stable node ID bytewise ascending.
- Compiler edges may rerank/expand only after lexical anchors. Expanded unanchored nodes may appear as structural relationships, never as lexical matches, and reranking never crosses the exact-qualified/exact-symbol tier boundary.

The versioned integer-only structural bonus table is:

~~~go
var structuralBonusV1 = map[int]int64{
    1: 100000,
    2: 50000,
    3: 25000,
    4: 12500,
    5: 6250,
    6: 3125,
    7: 1562,
    8: 781,
}
~~~

- [ ] **Step 1: Write failing tokenizer, documentation, pinned-revision, lifecycle, and ranking tests (RED).**

~~~go
func TestTokenizeLexicalContract(t *testing.T) {
    got := TokenizeLexical("HTTPServer_v2/load-path")
    want := []string{"http","server","v","2","load","path"}
    if !slices.Equal(got,want) { t.Fatalf("got=%v want=%v",got,want) }
}
func TestLexicalTieBreakIsStableID(t *testing.T) {
    got := RankLexical([]string{"worker"},[]LexicalDocument{{StableID:"z",ExactSymbol:"worker",SymbolName:[]string{"worker"}},{StableID:"a",ExactSymbol:"worker",SymbolName:[]string{"worker"}}})
    if got[0].StableID != "a" || got[0].FixedScore != got[1].FixedScore { t.Fatalf("got=%+v",got) }
}
func TestHeldOutCorpusTenRunsAreByteIdentical(t *testing.T) {
    corpus := loadHeldOutCorpus(t)
    first := marshalResults(t,runCorpus(t,corpus))
    for run := 1; run < 10; run++ {
        if got := marshalResults(t,runCorpus(t,corpus)); !bytes.Equal(got,first) { t.Fatalf("run %d differs",run) }
    }
}
func TestDocumentationTermsRankPackageAndDeclarationSymbols(t *testing.T) {
    fixture := analyzedDocumentedPackage(t)
    got := fixture.Query("lease arbitration")
    if got[0].QualifiedName != "example.LeaseManager" { t.Fatalf("got=%+v",got) }
    assertStoreHasTokensButNoRawComments(t,fixture.Store())
}
func TestLexicalRowsCascadeAcrossFailedAndRetiredRevisions(t *testing.T) {
    store := lexicalLifecycleStore(t)
    failed, retired := store.BuildAndFailCandidate(t.Context()), store.BuildAndRetireRevision(t.Context())
    store.ReclaimDeadBuild(t.Context(),failed)
    store.CleanupRetired(t.Context(),retired)
    assertNoLexicalRows(t,store,failed,retired)
    store.Disable(t.Context())
    assertLexicalTablesEmpty(t,store)
}
func TestCandidatePublicationRejectsIncompleteLexicalRevision(t *testing.T) {
    fixture := candidatePublicationFixture(t)
    previous := fixture.ActiveRevisionID()
    candidate := fixture.BuildCandidateOmittingOneLexicalDocument()
    err := fixture.Publish(candidate)
    if !errors.Is(err,ErrIncompleteLexicalRevision) { t.Fatalf("err=%v",err) }
    if got := fixture.ActiveRevisionID(); got != previous { t.Fatalf("active=%q want=%q",got,previous) }
    fixture.AssertNoPublishedRows(candidate)
}
~~~

- [ ] **Step 2: Run RED tests.**

Run: go test ./internal/runtime/codegraph/golang ./internal/runtime/codegraph/index ./internal/runtime/codegraph/store ./internal/runtime/codegraph/query -run 'Documentation|Lexical|Tokenize|BM25|TieBreak|HeldOut|Disclosure|Lifecycle' -count=1

Expected: FAIL because lexical-bm25-v1 is absent.

- [ ] **Step 3: Implement analyzer extraction, population, fixed retrieval, and progressive disclosure (GREEN).**

Persist canonical field tokens/lengths per revision and compute statistics only inside the pinned revision. Wire the build path to populate them before publication and make publication validate their exact completeness in its active-revision transaction. Exact and BM25 ordering follows the algorithm contract. Before explicit selection, responses expose in order the repository/package map and reasons, at most five distinct candidate files and one-hop relationships, deterministic outlines, and no more than 8192 source bytes. `SelectedSourcePaths` may request hash-validated source only for paths already present in the pinned ranked/traversal result; reject every other path.

Do not modify or repurpose `benchmark_test.go` as acceptance evidence. The held-out corpus must be independent and entry-symbol-free. Add separate disclosure and deterministic generated scale fixtures. For the twenty-query scale gate, compute p95 with nearest-rank index `ceil(.95*20)-1`; measure post-GC peak `HeapAlloc` growth.

- [ ] **Step 4: Run correctness, recall, disclosure, and scale gates.**

Run: go test ./internal/runtime/codegraph/query -run 'ExactQualified|UnqualifiedTopThree|HeldOut|Recall|Disclosure|TenRuns' -count=1

Expected: exact qualified top-1; unqualified top-3; primary-file recall@5 at least 0.90; expected-file recall@10 at least 0.90; structural-path recall after bounded expansion at least 0.90; ten runs byte-identical; no more than five files/8192 bytes before correct path.

Run: go test ./internal/runtime/codegraph/query -run TestScaleAcceptance250K -count=1 -timeout=10m

Expected: deterministic fixture contains 250000 symbols and 2000000 edges; twenty warm queries have nearest-rank p95 at most 300ms and post-GC peak HeapAlloc growth at most 128MiB.

- [ ] **Step 5: Commit.**

~~~bash
git add internal/runtime/codegraph/golang/analyzer.go internal/runtime/codegraph/golang/analyzer_test.go internal/runtime/codegraph/index/index.go internal/runtime/codegraph/index/index_test.go internal/runtime/codegraph/index/build.go internal/runtime/codegraph/index/build_test.go internal/runtime/codegraph/store/store.go internal/runtime/codegraph/store/schema_test.go internal/runtime/codegraph/query/lexical.go internal/runtime/codegraph/query/lexical_test.go internal/runtime/codegraph/query/ranking_acceptance_test.go internal/runtime/codegraph/query/scale_acceptance_test.go internal/runtime/codegraph/query/testdata/heldout/documents.json internal/runtime/codegraph/query/testdata/heldout/queries.json internal/runtime/codegraph/query/query.go
git commit -m "feat(codegraph): add deterministic bm25 retrieval"
~~~

## Shared Slice C command contract

Task 7 creates `internal/runtime/config/command.go` and freezes the only subprocess seam used by service lifecycle, Git identity/signing, and native connectors:

~~~go
type Command struct {
    Executable string
    Args       []string
    Dir        string
    Env        []string // nil inherits runner base environment
    Stdin      []byte
}
type CommandResult struct {
    ExitCode int
    Stdout   []byte
    Stderr   []byte
}
type CommandRunner interface {
    LookPath(string) (string, error)
    Run(context.Context, Command) (CommandResult, error)
}
func NewExecCommandRunner(baseEnvironment []string) CommandRunner
~~~

`Run` returns a nil error for a started process with any ordinary exit code; callers interpret `ExitCode`. Errors mean lookup, start, signal, context, or output-limit failure only and never include stdin, stdout, or stderr. Stdout and stderr are independently capped at 1 MiB. Context cancellation remains detectable with `errors.Is`. Callers provide exact argv and no shell is involved. Tasks 7, 9, and 10 consume this exact type. The connector subpackage imports parent `config`; parent `config` never imports the connector package.

### Task 7: Add the conservative Gateway service primitive

**Files:**

- Create: internal/runtime/config/command.go
- Create: internal/runtime/config/command_test.go
- Create: internal/runtime/config/service.go
- Create: internal/runtime/config/service_linux.go
- Create: internal/runtime/config/service_unsupported.go
- Create: internal/runtime/config/service_test.go

**Produces:**

~~~go
type GatewayServiceSpec struct { Executable string; SocketPath string }
type ServiceStatus struct { Installed, Running, Ready bool; Detail string }
type GatewayService interface {
    Inspect(context.Context) (ServiceStatus, error)
    Install(context.Context, GatewayServiceSpec) error
    Start(context.Context) error
    Verify(context.Context, string) error
}
var ErrServiceManagerUnavailable = errors.New("gatewayd service manager unavailable; start gatewayd manually")
func NewGatewayService(CommandRunner) GatewayService
~~~

The unit name is `wormhole-gatewayd.service` under `$XDG_CONFIG_HOME/systemd/user`, else `~/.config/systemd/user`. Directories are `0700`; the unit is `0600`. `GatewayServiceSpec.SocketPath` must equal `config.ResolveRuntimePaths().SocketPath`. The effective runtime root is `$XDG_RUNTIME_DIR`, or `$TMPDIR/wormhole-runtime`; write that exact effective value as `XDG_RUNTIME_DIR` in the unit so `gatewayd` derives the same socket.

Exact unit bytes, after escaping values, are:

~~~ini
[Unit]
Description=Wormhole Gateway
After=default.target

[Service]
Type=simple
ExecStart="<escaped absolute gatewayd path>"
Environment="XDG_RUNTIME_DIR=<escaped effective runtime root>"
Restart=on-failure
RestartSec=2s

[Install]
WantedBy=default.target
~~~

Unit escaping quotes the whole argument, escapes backslash and quote, doubles `%`, and rejects NUL/control characters and unsafe `$` expansion.

- [ ] **Step 1: Write failing service tests (RED).**

~~~go
func TestUnavailableManagerReturnsExactManualDiagnostic(t *testing.T) {
    _, err := newUnavailableService().Inspect(t.Context())
    if !errors.Is(err,ErrServiceManagerUnavailable) || err.Error() != "gatewayd service manager unavailable; start gatewayd manually" { t.Fatalf("err=%v",err) }
}
func TestSystemdUserInstallIsIdempotentAndVerifiesSocket(t *testing.T) {
    environment := xdgRuntimeEnvironment(t)
    setRuntimeEnvironment(t,environment)
    paths, err := ResolveRuntimePaths()
    if err != nil { t.Fatal(err) }
    runner := systemdFakeRunnerWithEnvironment(t,environment)
    service := NewGatewayService(runner)
    spec := GatewayServiceSpec{Executable:ownedGatewayExecutable(t),SocketPath:paths.SocketPath}
    if err := service.Install(t.Context(),spec); err != nil { t.Fatal(err) }
    if err := service.Install(t.Context(),spec); err != nil { t.Fatal(err) }
    if runner.Count("enable","--now") != 1 { t.Fatalf("calls=%v",runner.Calls()) }
}
func TestSystemdUnitMatchesRuntimeSocketAndExactBytes(t *testing.T) {
    environment := fallbackRuntimeEnvironment(t)
    setRuntimeEnvironment(t,environment)
    paths, err := ResolveRuntimePaths()
    if err != nil { t.Fatal(err) }
    fixture := systemdUnitFixture(t,environment,GatewayServiceSpec{Executable:ownedGatewayExecutable(t),SocketPath:paths.SocketPath})
    if err := fixture.Service.Install(t.Context(),fixture.Spec); err != nil { t.Fatal(err) }
    if got,want := fixture.UnitBytes(),fixture.ExpectedUnitBytes(); !bytes.Equal(got,want) { t.Fatalf("got=%q want=%q",got,want) }
    if fixture.UnitRuntimeRoot() != fixture.EffectiveRuntimeRoot() { t.Fatalf("unit=%q runtime=%q",fixture.UnitRuntimeRoot(),fixture.EffectiveRuntimeRoot()) }
}
func TestReadyRPCRejectsWrongIdentityProtocolAndSocket(t *testing.T) {
    fixture := readinessFixture(t)
    for _, response := range fixture.InvalidResponses() {
        if err := fixture.VerifyResponse(response); err == nil { t.Fatalf("accepted=%s",response) }
    }
    fixture.AssertSingleInitialize("wormhole-setup")
}
~~~

Every positive service fixture resolves its socket by calling the configured `ResolveRuntimePaths` seam under the same injected environment used by the service; no positive fixture fabricates or independently joins a socket path. The RED suite also covers shared-runner exit semantics, independent 1 MiB stdout/stderr caps, `errors.Is` cancellation, exact argv/no-shell execution, fallback runtime path, every install idempotence branch, recognized disabled/inactive exits, unknown manager output, executable/unit file safety, exact readiness framing, context handling, and zero mutation on unavailable manager.

- [ ] **Step 2: Run RED tests.**

Run: go test ./internal/runtime/config -run 'CommandRunner|Manager|Systemd|Service|ManualDiagnostic|ReadyRPC|Readiness' -count=1

Expected: FAIL because service primitives are absent.

- [ ] **Step 3: Implement the shared runner and systemd-user lifecycle (GREEN).**

Implement the shared no-shell runner exactly as frozen above. On Linux, availability requires a resolved `systemctl` and successful `systemctl --user show-environment` before any filesystem write or mutating command. Missing/unusable manager and unsupported OS return `ErrServiceManagerUnavailable` itself with the exact unchanged error string and no appended command output.

`Inspect` accepts only recognized `is-enabled` and `is-active` exit/output combinations; unknown exit/output is unavailable, never inactive. `Install` branches exactly:

- identical unit, enabled, active: no rewrite, reload, enable, or start;
- identical, enabled, inactive: `start` only;
- identical, disabled: `enable --now` only;
- new or changed: atomic write, directory fsync, `daemon-reload`, then `enable --now`.

Reject relative, symlink, directory, nonregular, non-owner executable or unit paths. `Start` requires the installed unit. `Verify` dials only the supplied Unix socket and sends one newline-terminated MCP `initialize` request with `clientInfo.name="wormhole-setup"`; it requires the matching ID, no RPC error, `serverInfo.name=="wormhole"`, and the expected protocol version. Service code imports no `localapi`.

- [ ] **Step 4: Run GREEN tests.**

Run: go test ./internal/runtime/config -run 'CommandRunner|Manager|Systemd|Service|ManualDiagnostic|ReadyRPC|Readiness' -count=1

Expected: PASS.

- [ ] **Step 5: Commit.**

~~~bash
git add internal/runtime/config/command.go internal/runtime/config/command_test.go internal/runtime/config/service.go internal/runtime/config/service_linux.go internal/runtime/config/service_unsupported.go internal/runtime/config/service_test.go
git commit -m "feat(setup): manage systemd user gateway"
~~~

### Task 8: Add a durable setup journal primitive

**Files:**

- Create: internal/runtime/config/setup_journal.go
- Create: internal/runtime/config/setup_journal_lock_unix.go
- Create: internal/runtime/config/setup_journal_unsupported.go
- Create: internal/runtime/config/setup_journal_test.go
- Create: internal/runtime/config/setup_journal_lock_test.go
- Modify: internal/runtime/config/config.go
- Modify: internal/runtime/config/config_test.go

**Consumes:** Task 0's `types.ConfirmedIdentitySelection` and its exact validation constants; Task 8 owns its required owner-private persistence inside unshipped journal schema v1.

**Produces:**

~~~go
type SetupStage string
type SetupJournalState string
const (
    SetupJournalActive SetupJournalState = "active"
    SetupJournalCompleted SetupJournalState = "completed"
    SetupJournalSuperseded SetupJournalState = "superseded"
)
type StateDigest string // sha256:<64 lowercase hex>
func SHA256StateDigest(canonicalState []byte) StateDigest
func ParseStateDigest(string) (StateDigest, error)
type ConfirmedChange struct {
    Stage SetupStage       `json:"stage"`
    Subject string         `json:"subject"`
    Action string          `json:"action"`
    PriorDigest StateDigest `json:"prior_digest"`
    DesiredDigest StateDigest `json:"desired_digest"`
}
type SetupSelection struct {
    ConnectorAdapters []string      `json:"connector_adapters"` // sorted unique codex|claude
    CodeGraphMode string             `json:"code_graph_mode"` // on|off
    Identity types.ConfirmedIdentitySelection `json:"identity"`
    PlanDigest StateDigest           `json:"plan_digest"`
    Changes []ConfirmedChange        `json:"changes"` // sorted by stage, subject
}
type SetupJournal struct {
    SchemaVersion int                  `json:"schema_version"`
    ID string                          `json:"id"`
    State SetupJournalState            `json:"state"`
    RepositoryRoot string              `json:"repository_root"`
    WorkspaceID types.WorkspaceID      `json:"workspace_id,omitempty"`
    Selection *SetupSelection          `json:"selection,omitempty"`
    SelectedHumanID string             `json:"selected_human_id,omitempty"`
    Completed []SetupStage             `json:"completed"`
    ConnectorBackupRefs []BackupReference `json:"connector_backup_refs"`
    LastErrorStage SetupStage          `json:"last_error_stage,omitempty"`
    LastError string                   `json:"last_error,omitempty"`
    CreatedAt time.Time                `json:"created_at"`
    UpdatedAt time.Time                `json:"updated_at"`
    CompletedAt time.Time              `json:"completed_at,omitempty"`
}
type BackupReference string
// Valid values are connector-backup:v1:<codex|claude>:<canonical UUID>.
var ErrJournalCredentialMaterial = errors.New("setup journal: credential material forbidden")
var ErrAmbiguousSetupJournal = errors.New("setup journal: ambiguous active journal")
var ErrSetupSelectionRequired = errors.New("setup journal: finalized selection required")
var ErrConfirmedPlanDrift = errors.New("setup: confirmed plan drift")
var ErrPrivateStateUnsupported = errors.New("wormhole private state unsupported on this platform")
func OpenSetupJournalStore() (*SetupJournalStore, error)
func OpenSetupJournalStoreAt(existingRoot string) (*SetupJournalStore, error)
func (s *SetupJournalStore) Begin(context.Context, string) (SetupJournal, error)
func (s *SetupJournalStore) BindWorkspace(context.Context, string, types.WorkspaceID) error
func (s *SetupJournalStore) BindIdentity(context.Context, string, string) error
func (s *SetupJournalStore) SetSelection(context.Context, string, SetupSelection) error
func (s *SetupJournalStore) BeginConfirmedReplacement(context.Context, string, string, SetupSelection) (SetupJournal, error)
func (s *SetupJournalStore) MarkCompleted(context.Context, string, SetupStage) error
func (s *SetupJournalStore) RecordConnectorBackup(context.Context, string, BackupReference) error
func (s *SetupJournalStore) RecordLastError(context.Context, string, SetupStage, error) error
func (s *SetupJournalStore) Complete(context.Context, string) error
func (s *SetupJournalStore) Resumable(context.Context, string) (SetupJournal, bool, error)
~~~

Stages form this exact ordered prefix:

~~~text
project_validated
gateway_ready
workspace_registered
identity_selected
base_imported
connectors_applied
fabric_resolved
code_graph_resolved
final_verified
~~~

`StateDigest` is the config-owned digest boundary: callers strict-canonicalize state, `SHA256StateDigest` returns `sha256:` plus lowercase SHA-256 hex, and `ParseStateDigest` rejects every other representation. `SetupSelection` is the canonical confirmation record. `Identity` is required and must pass Task 0's exact bounded validation; because this schema has not shipped, it is part of journal schema v1 rather than a migration or optional compatibility field. Its sorted `Changes` contains safe stage/subject/action enums and prior/desired digests for the resolved service executable/action/unit/socket, workspace registration and base import, selected identity, each connector independently, Fabric/local-only resolution, and Code Graph mode/preference. `PlanDigest` hashes the strict canonical confirmation envelope: finalized connector/graph choices, the complete canonical `Identity`, and the ordered change metadata with its already-computed prior/desired digests. It never embeds the other raw state preimages. This makes the overall digest reproducible on resume from persisted identity execution intent, revalidated stable inputs, freshly derived desired digests, fixed bounded-predicate encodings, and the frozen exact-prior digests without replacing a post-effect prior with the current desired state.

The confirmed display name and optional email are the single narrow PII-not-secret exception to the digest-only rule because a new process must be able to execute the already-confirmed identity action. The owner-private journal may persist exactly those two bounded final values plus `ensure-selected` and `ensure-ed25519`. It still forbids private/public key bytes, signatures, key or credential paths, raw Git config/output, connector command/argv/env/header/credential, Fabric credential/binding secret, executable/private paths, and raw graph path/status. `SetupSelection` and its identity values are never logged, emitted by status, returned through MCP, or included in a public contract response.

`Begin` writes no `selection` member: `Selection` remains nil until the caller has rendered the complete plan and received confirmation. Before calling `SetSelection`, `setupPlan.ValidateConfirmation` recomputes the overall and per-change digests from the in-memory canonical plan. `SetSelection` requires and validates `Identity`, strict-validates subject/action vocabulary, exact required stage coverage, adapter correspondence, digest syntax, ordering, uniqueness, and a `PlanDigest` that covers the exact identity selection, then durably finalizes once; repeating the exact value is idempotent and a different value fails closed. `RecordConnectorBackup` rejects a missing selection or an adapter absent from its finalized changes. `MarkCompleted` rejects `connectors_applied` or any later stage unless selection is finalized, and `Complete` requires a finalized selection plus every stage, sets terminal state/time, and clears `LastError`. Completed and superseded journals are never resumable. Exactly one active canonical-root match resumes. Multiple matches return `ErrAmbiguousSetupJournal`. `Begin` holds a store-index lock and refuses a second active journal for the same root.

`BeginConfirmedReplacement` is callable only after the caller has recomputed, rendered, and newly confirmed a drifted plan. Under the store-index lock it verifies the named old journal is still active with a finalized different selection, marks it `superseded`, and creates a new active journal for the same canonical root with only `project_validated` complete and the newly confirmed selection durable. It never copies completed effects, workspace/identity bindings, backup references, or errors from the old journal. Detection of drift itself only returns `ErrConfirmedPlanDrift` and performs no write; a replacement is a separate explicitly confirmed action.

- [ ] **Step 1: Write failing durability/no-secret tests (RED).**

~~~go
func TestSetupJournalResumesCompletedStagesAfterRestart(t *testing.T) {
    root := ownerOnlyExistingDirectory(t)
    store, _ := OpenSetupJournalStoreAt(root)
    journal, _ := store.Begin(t.Context(),"/repo")
    _ = store.MarkCompleted(t.Context(),journal.ID,SetupStage("project_validated"))
    reopened, _ := OpenSetupJournalStoreAt(root)
    got, ok, err := reopened.Resumable(t.Context(),"/repo")
    if err != nil || !ok || !slices.Contains(got.Completed,SetupStage("project_validated")) { t.Fatalf("got=%+v ok=%v err=%v",got,ok,err) }
}
func TestSetupJournalSelectionIsUnsetUntilFinalizedAndRequiredForEffects(t *testing.T) {
    store,journal := setupJournalFixture(t)
    if journal.Selection != nil { t.Fatalf("selection=%+v",journal.Selection) }
    markCompletedThroughBaseImported(t,store,journal.ID)
    if err := store.RecordConnectorBackup(t.Context(),journal.ID,BackupReference("connector-backup:v1:codex:99999999-9999-4999-8999-999999999999")); !errors.Is(err,ErrSetupSelectionRequired) { t.Fatalf("backup err=%v",err) }
    if err := store.MarkCompleted(t.Context(),journal.ID,SetupStage("connectors_applied")); !errors.Is(err,ErrSetupSelectionRequired) { t.Fatalf("err=%v",err) }
    selection := confirmedSelectionFixture(t,"codex","on")
    if err := store.SetSelection(t.Context(),journal.ID,selection); err != nil { t.Fatal(err) }
    if err := store.SetSelection(t.Context(),journal.ID,confirmedSelectionFixture(t,"codex","off")); err == nil { t.Fatal("finalized selection changed") }
}
func TestSetupJournalRejectsRawCredential(t *testing.T) {
    store, _ := OpenSetupJournalStoreAt(ownerOnlyExistingDirectory(t))
    const journalID = "99999999-9999-4999-8999-999999999999"
    seedSetupJournal(t,store,journalID)
    err := store.RecordConnectorBackup(t.Context(),journalID,BackupReference("{\"token\":\"secret\"}"))
    if !errors.Is(err,ErrJournalCredentialMaterial) { t.Fatalf("err=%v",err) }
}
func TestSetupJournalCompletionIsTerminalAndAmbiguityFailsClosed(t *testing.T) {
    store := setupJournalStoreFixture(t)
    journal := completeJournalThroughFinalVerified(t,store,"/repo")
    if err := store.Complete(t.Context(),journal.ID); err != nil { t.Fatal(err) }
    if _,ok,err := store.Resumable(t.Context(),"/repo"); err != nil || ok { t.Fatalf("ok=%v err=%v",ok,err) }
    seedTwoActiveJournalsForRoot(t,store,"/other")
    if _,_,err := store.Resumable(t.Context(),"/other"); !errors.Is(err,ErrAmbiguousSetupJournal) { t.Fatalf("err=%v",err) }
}
func TestSetupJournalRedactsNestedCredentialMaterial(t *testing.T) {
    store,journal := setupJournalFixture(t)
    source := errors.New(`request failed: {"Authorization":"Bearer visible","nested":{"PASSWORD":"visible"}} callback_code=visible`)
    if err := store.RecordLastError(t.Context(),journal.ID,SetupStage("connectors_applied"),source); err != nil { t.Fatal(err) }
    encoded := readJournalBytes(t,store,journal.ID)
    for _, forbidden := range [][]byte{[]byte("Bearer visible"),[]byte("PASSWORD"),[]byte("callback_code=visible")} {
        if bytes.Contains(encoded,forbidden) { t.Fatalf("journal=%s",encoded) }
    }
}
func TestSetupJournalRedactsSensitiveCredentialPaths(t *testing.T) {
    store,journal := setupJournalFixture(t)
    source := errors.New(`key_path=/home/alice/.ssh/id_ed25519 credentials_file=C:\Users\alice\.wormhole\credentials\private.json identity_file=/home/alice/.gnupg/private-keys-v1.d/key.key`)
    if err := store.RecordLastError(t.Context(),journal.ID,SetupStage("connectors_applied"),source); err != nil { t.Fatal(err) }
    encoded := readJournalBytes(t,store,journal.ID)
    for _, forbidden := range [][]byte{[]byte("/home/alice/.ssh"),[]byte(`C:\Users\alice\.wormhole\credentials`),[]byte("/home/alice/.gnupg")} {
        if bytes.Contains(encoded,forbidden) { t.Fatalf("journal=%s",encoded) }
    }
}
func TestConfirmedSelectionStoresDigestsWithoutRawPlanState(t *testing.T) {
    store,journal := setupJournalFixture(t)
    selection := confirmedSelectionWithSecretInputs(t,"TOKEN=visible","/home/alice/private/gatewayd")
    if err := store.SetSelection(t.Context(),journal.ID,selection); err != nil { t.Fatal(err) }
    encoded := readJournalBytes(t,store,journal.ID)
    for _, forbidden := range [][]byte{[]byte("TOKEN=visible"),[]byte("/home/alice/private/gatewayd"),[]byte("Authorization"),[]byte("--secret")} {
        if bytes.Contains(encoded,forbidden) { t.Fatalf("journal=%s",encoded) }
    }
    assertOnlySafeConfirmationFields(t,encoded)
}
func TestSetupSelectionRequiresCanonicalConfirmedIdentityInV1(t *testing.T) {
    store,journal := setupJournalFixture(t)
    identity := types.ConfirmedIdentitySelection{Action:"ensure-selected",DisplayName:"Alice Example",Email:"alice@example.test",KeyPolicy:"ensure-ed25519"}
    valid := confirmedSelectionFixtureWithIdentity(t,"codex","on",identity)
    if err := store.SetSelection(t.Context(),journal.ID,valid); err != nil { t.Fatal(err) }
    reopened, _ := OpenSetupJournalStoreAt(store.Root())
    got,ok,err := reopened.Resumable(t.Context(),journal.RepositoryRoot)
    if err != nil || !ok || !reflect.DeepEqual(got.Selection.Identity,valid.Identity) { t.Fatalf("got=%+v ok=%v err=%v",got,ok,err) }
    changedIdentity := identity
    changedIdentity.Email = "other@example.test"
    changed := confirmedSelectionFixtureWithIdentity(t,"codex","on",changedIdentity)
    if changed.PlanDigest == valid.PlanDigest || confirmedIdentityChange(t,changed).DesiredDigest == confirmedIdentityChange(t,valid).DesiredDigest { t.Fatal("identity not bound into plan/desired digest") }

    for _, invalid := range invalidRequiredIdentitySelections(t) {
        otherStore,otherJournal := setupJournalFixture(t)
        selection := confirmedSelectionFixtureWithIdentity(t,"codex","on",invalid)
        if err := otherStore.SetSelection(t.Context(),otherJournal.ID,selection); err == nil { t.Fatalf("accepted=%+v",invalid) }
    }
}
~~~

The RED suite also covers canonical StateDigest parse/hash vectors, exact confirmed-change coverage/order/uniqueness, required bounded identity selection and its inclusion in `PlanDigest`, safe-enum-only confirmation encoding, the explicit final display-name/optional-email exception, absence of every forbidden raw plan secret/key/signature/config/path, workspace and selected-human binding, nil-before-confirmation and immutable canonical selection persistence across real close/reopen, confirmed replacement after explicit reconfirmation, selection-required connector/final completion, redacted `LastError`, completed/superseded exclusion from resume, multiple-incomplete ambiguity, duplicate/unknown/oversized/corrupt JSON, invalid root/ID/stage, symlink/nonregular/owner/mode failures, nested case-insensitive redaction, opaque-token/private-key/credential-path redaction, invalid backup references, cross-process lock serialization, atomic old-or-new fault recovery, and the unsupported-platform sentinel before filesystem mutation.

- [ ] **Step 2: Run RED tests.**

Run: go test ./internal/runtime/config -run 'SetupJournal|Selection|CredentialPath|UnsupportedPlatform' -count=1

Expected: FAIL because journal store is absent.

- [ ] **Step 3: Implement canonical owner-only journals, OS locks, and redaction (GREEN).**

Production derives `$XDG_DATA_HOME/wormhole/setup-journals`, else `~/.local/share/wormhole/setup-journals`. `OpenSetupJournalStoreAt` accepts only an already-existing canonical owner-only directory and is the test/open-existing seam.

Each record is at most 64 KiB and must have a canonical lowercase UUID, supported version/state, canonical root, valid optional workspace and selected-human UUIDs, ordered stage prefix, and absent-or-finalized canonical selection with the required validated identity, matching plan/change digests, and safe sorted unique metadata. Reject duplicate/unknown JSON keys and any bytes unequal to canonical re-encoding. On Unix, open roots/records/locks/temp files without following symlinks, require directory/file ownership by the effective UID, reject nonregular files and group/other permission bits, and require exact `0700` directories and `0600` records/locks. Serialize the store index and each journal with OS-level advisory locks, not process mutexes. Use temp-file/fsync/rename/directory-fsync writes; fault injection at every write boundary must recover an old-or-new whole JSON record. V1 does not claim Windows ACL equivalence: non-Unix constructors return `ErrPrivateStateUnsupported` before creating, opening, locking, or mutating a path; the unsupported build file and a `GOOS=windows` compile gate freeze that behavior.

Accept only opaque backup references matching `connector-backup:v1:<codex|claude>:<canonical UUID>`. Reject everything else with `ErrJournalCredentialMaterial` without reflecting input. Journals never store backup contents, inline environment/header/config data, or credential-shaped fields. Redaction recursively replaces case-insensitive credential-shaped JSON keys and scrubs bearer values, authorization headers, token/password/secret/private-key assignments, callback codes, PEM private-key blocks, assignments named `credential_path`, `credentials_path`, `credential_file`, `credentials_file`, `token_file`, `private_key_path`, `key_path`, or `identity_file`, and path values beneath `.ssh`, `.gnupg`, `.aws`, `.config/gcloud`, or `.wormhole/credentials` or naming `id_rsa`, `id_ed25519`, `identity.ed25519.private`, or `credentials`. Rejection errors never quote source input.

- [ ] **Step 4: Run GREEN tests.**

Run: go test ./internal/runtime/config -run 'SetupJournal|Selection|Permission|Atomic|Restart|NoSecret|CredentialPath|UnsupportedPlatform' -count=1

Expected: PASS.

Run: GOOS=windows go test -c -o /tmp/wormhole-config-windows.test.exe ./internal/runtime/config

Expected: PASS compilation with the non-Unix constructors bound to `ErrPrivateStateUnsupported`; no Windows ACL support is claimed.

- [ ] **Step 5: Commit.**

~~~bash
git add internal/runtime/config/setup_journal.go internal/runtime/config/setup_journal_lock_unix.go internal/runtime/config/setup_journal_unsupported.go internal/runtime/config/setup_journal_test.go internal/runtime/config/setup_journal_lock_test.go internal/runtime/config/config.go internal/runtime/config/config_test.go
git commit -m "feat(setup): persist resumable setup journal"
~~~

### Task 9: Add read-only Git identity suggestions

**Files:**

- Create: internal/runtime/config/identity_suggestion.go
- Create: internal/runtime/config/identity_suggestion_test.go

**Produces:**

~~~go
type IdentitySuggestion struct {
    Name, Email, SigningKey, SigningFormat, Source string
}
const IdentitySuggestionSourceGitConfig = "git-config"
func SuggestGitIdentity(context.Context, string, CommandRunner) (IdentitySuggestion, error)
func RequestSigningAttestation(context.Context, string, []byte, CommandRunner) ([]byte, error)
~~~

- [ ] **Step 1: Write failing suggestion tests (RED).**

~~~go
func TestSuggestGitIdentityOnlyReadsExpectedKeys(t *testing.T) {
    runner := recordingCommandRunner(t,map[string]string{"user.name":"Harley","user.email":"h@example.test","user.signingkey":"KEY"})
    got, err := SuggestGitIdentity(t.Context(),"/repo",runner)
    if err != nil || got.Name != "Harley" || got.Email != "h@example.test" { t.Fatalf("got=%+v err=%v",got,err) }
    if runner.HasMutation() || runner.ReadKey("credential.helper") { t.Fatalf("calls=%v",runner.Calls()) }
}
func TestSigningAttestationNeverReadsPrivateKey(t *testing.T) {
    runner := signingAgentRunner(t)
    _, err := RequestSigningAttestation(t.Context(),"/repo",[]byte("identity-key"),runner)
    if err != nil { t.Fatal(err) }
    if runner.ReadFileCalled() { t.Fatal("private key file read") }
}
func TestSigningAttestationUsesExactOpenPGPArgv(t *testing.T) {
    runner := recordingCommandRunner(t,map[string]string{"user.signingkey":"KEY","gpg.format":"openpgp"})
    runner.Result.Stdout = []byte("detached-signature")
    got, err := RequestSigningAttestation(t.Context(),"/repo",[]byte("payload"),runner)
    if err != nil || string(got) != "detached-signature" { t.Fatalf("got=%q err=%v",got,err) }
    runner.AssertCommand(Command{Executable:"gpg",Args:[]string{"--batch","--no-tty","--status-fd=2","--local-user","KEY","--detach-sign","--output","-"},Dir:"/repo",Stdin:[]byte("payload")})
}
func TestSigningAttestationRejectsSSHX509AndPathKeys(t *testing.T) {
    for _, config := range []gitSigningConfig{sshSigningConfig(),x509SigningConfig(),pathSigningKeyConfig()} {
        runner := signingRunnerForConfig(t,config)
        if _,err := RequestSigningAttestation(t.Context(),"/repo",[]byte("payload"),runner); err == nil { t.Fatalf("accepted=%+v",config) }
        if runner.SignCount() != 0 || runner.ReadFileCalled() { t.Fatalf("calls=%v",runner.Calls()) }
    }
}
~~~

Tests record exact executable, argv, cwd, environment, the four allowed Git config keys, unset exit-1 behavior, verbatim signature output, cancellation/failure, root mismatch, and absence of credential-helper reads, config writes, filesystem key reads, or network calls.

- [ ] **Step 2: Run RED tests.**

Run: go test ./internal/runtime/config -run 'GitIdentity|SigningAttestation' -count=1

Expected: FAIL because suggestion functions are absent.

- [ ] **Step 3: Implement canonical-root Git reads and OpenPGP-only signing (GREEN).**

Canonicalize and validate the repository root, then require `git rev-parse --show-toplevel` to return that same root. Read exactly `user.name`, `user.email`, `user.signingkey`, and `gpg.format` with `git -C root config --get`. Exit 1 with empty output means unset; exit 0 strips one trailing newline. Every other exit, oversized output, NUL, or root mismatch is an error. Set `Source` to `git-config` when any value is present, otherwise leave it empty. Suggestions remain self-declared and Task 9 never publishes or persists them.

V1 signing requires explicit `gpg.format=openpgp` and a non-path signing-key selector. Invoke exactly `gpg --batch --no-tty --status-fd=2 --local-user <key> --detach-sign --output -`, with payload on stdin and signature on stdout. Absent format, `ssh`, `x509`, path-like signing keys, unknown formats, empty/oversized signatures, and signer failure return typed fail-closed errors before any key-file read. Never return signer stderr or an error containing key or signature material. This is a narrow OpenPGP initial scope, not an SSH file-key fallback.

- [ ] **Step 4: Run GREEN tests.**

Run: go test ./internal/runtime/config -run 'GitIdentity|SigningAttestation|NoCredentialHelper|NoWrite' -count=1

Expected: PASS.

- [ ] **Step 5: Commit.**

~~~bash
git add internal/runtime/config/identity_suggestion.go internal/runtime/config/identity_suggestion_test.go
git commit -m "feat(setup): suggest local git identity"
~~~

### Task 10: Implement transactional native Codex and Claude stdio adapters

**Files:**

- Create: internal/runtime/config/connector/connector.go
- Create: internal/runtime/config/connector/transaction.go
- Create: internal/runtime/config/connector/backup.go
- Create: internal/runtime/config/connector/operation_journal.go
- Create: internal/runtime/config/connector/operation_coordinator.go
- Create: internal/runtime/config/connector/transaction_lock_unix.go
- Create: internal/runtime/config/connector/private_store_unsupported.go
- Create: internal/runtime/config/connector/codex.go
- Create: internal/runtime/config/connector/claude.go
- Create: internal/runtime/config/connector/connector_test.go
- Create: internal/runtime/config/connector/transaction_test.go
- Create: internal/runtime/config/connector/operation_coordinator_test.go
- Create: internal/runtime/config/connector/private_store_test.go
- Create: internal/runtime/config/connector/real_smoke_test.go
- Create: internal/runtime/config/connector/testdata/codex/0.145.0/get-stdio.json
- Create: internal/runtime/config/connector/testdata/codex/0.145.0/get-http.json
- Create: internal/runtime/config/connector/testdata/codex/0.145.0/get-absent.stderr
- Create: internal/runtime/config/connector/testdata/codex/0.145.0/list-stdio.json
- Create: internal/runtime/config/connector/testdata/claude/2.1.220/user-stdio.json
- Create: internal/runtime/config/connector/testdata/claude/2.1.220/local-stdio.json
- Create: internal/runtime/config/connector/testdata/claude/2.1.220/project-stdio.json
- Create: internal/runtime/config/connector/testdata/claude/2.1.220/user-http.json
- Create: internal/runtime/config/connector/testdata/claude/2.1.220/hidden-scope-duplicate-user.json
- Create: internal/runtime/config/connector/testdata/claude/2.1.220/hidden-scope-duplicate-project.json

**Produces:**

~~~go
type AdapterName string
type AvailabilityState string
const (
    AvailabilityAvailable AvailabilityState = "available"
    AvailabilityUnavailable AvailabilityState = "unavailable"
    AvailabilityUnsupported AvailabilityState = "unsupported"
)
type Availability struct {
    Adapter AdapterName
    State AvailabilityState
    Version string
}
type EntryState string
const (EntryAbsent EntryState = "absent"; EntryPresent EntryState = "present")
type Transport string
const TransportStdio Transport = "stdio"
type Scope string
const ScopeUser Scope = "user"
type EnvironmentVariable struct { Name, Value string }
type ConnectorEntry struct {
    State     EntryState
    Scope     Scope
    Transport Transport
    Command   string
    Args      []string
    Env       []EnvironmentVariable // sorted by name
}
type ChangePlan struct { Action string; Prior ConnectorEntry; Desired ConnectorEntry; Mutates bool }
type TransactionResult struct {
    Mutated, Recovered bool
    BackupRef config.BackupReference
}
type ConnectorBackup struct {
    SchemaVersion int                    `json:"schema_version"`
    Adapter AdapterName                  `json:"adapter"`
    Name string                          `json:"name"`
    Prior ConnectorEntry                 `json:"prior"`
    Desired ConnectorEntry               `json:"desired"`
    CreatedAt time.Time                  `json:"created_at"`
}
type BackupStore interface {
    Put(context.Context, ConnectorBackup) (config.BackupReference, error)
    Get(context.Context, config.BackupReference) (ConnectorBackup, error)
}
func OpenBackupStore() (BackupStore, error)
func OpenBackupStoreAt(existingRoot string) (BackupStore, error)

type OperationAction string // apply|remove
type OperationStage string // prepared|applied|verified|rolled_back|complete
type PrepareOperation struct {
    Adapter AdapterName
    Name string
    Action OperationAction
    PlanDigest config.StateDigest
    PriorDigest config.StateDigest
    DesiredDigest config.StateDigest
    BackupRef config.BackupReference
}
type OperationRecord struct {
    SchemaVersion int                    `json:"schema_version"`
    ID string                            `json:"id"`
    Adapter AdapterName                  `json:"adapter"`
    Name string                          `json:"name"`
    Action OperationAction               `json:"action"`
    Stage OperationStage                 `json:"stage"`
    PlanDigest config.StateDigest        `json:"plan_digest"`
    PriorDigest config.StateDigest       `json:"prior_digest"`
    DesiredDigest config.StateDigest     `json:"desired_digest"`
    BackupRef config.BackupReference     `json:"backup_ref"`
    CreatedAt time.Time                  `json:"created_at"`
    UpdatedAt time.Time                  `json:"updated_at"`
}
type OperationJournal interface {
    Prepare(context.Context, PrepareOperation) (OperationRecord, error)
    Get(context.Context, string) (OperationRecord, error)
    Active(context.Context, AdapterName, string) (OperationRecord, bool, error)
    Advance(context.Context, string, OperationStage) error
}
func OpenOperationJournal() (OperationJournal, error)
func OpenOperationJournalAt(existingRoot string) (OperationJournal, error)
func DigestConnectorEntry(ConnectorEntry) (config.StateDigest, error)

type OperationCoordinator interface {
    WithOperationLock(
        context.Context, AdapterName, string,
        func(context.Context) error,
    ) error
}
func OpenOperationCoordinator() (OperationCoordinator, error)
func OpenOperationCoordinatorAt(existingOperationsRoot string) (OperationCoordinator, error)

type ConfirmedConnectorChange struct {
    Adapter AdapterName
    Name string
    Action OperationAction
    PlanDigest config.StateDigest
    ExpectedPriorDigest config.StateDigest
    DesiredDigest config.StateDigest
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
func ApplyTransactional(
    context.Context, Adapter, ConnectorEntry, ConfirmedConnectorChange,
    BackupStore, OperationJournal, OperationCoordinator,
) (TransactionResult, error)
func RemoveTransactional(
    context.Context, Adapter, ConfirmedConnectorChange,
    BackupStore, OperationJournal, OperationCoordinator,
) (TransactionResult, error)
func RecoverTransactions(
    ctx context.Context, adapter Adapter, connectorName string,
    backups BackupStore, operations OperationJournal, coordinator OperationCoordinator,
) error
~~~

Prior absence is first-class. Only an absent or fully reconstructable user-scope stdio entry can reach planning. HTTP/SSE/OAuth/header entries, hidden-scope duplicates, malformed/unknown fields or versions, and anything that cannot round-trip return `ErrUnsupportedPriorEntry` before backup, journal, or mutation.

Production backup and operation roots are `$XDG_DATA_HOME/wormhole/connector-backups` and `$XDG_DATA_HOME/wormhole/connector-operations`, else `~/.local/share/wormhole/connector-backups` and `~/.local/share/wormhole/connector-operations`. The `At` constructors accept only already-existing canonical private roots. Backup references are `connector-backup:v1:<adapter>:<backup UUID>`; operation files are named by canonical operation UUID. `DigestConnectorEntry` returns Task 8's `config.StateDigest` over the strict canonical JSON entry encoding.

`AdapterName` returns the adapter's immutable canonical `codex|claude` identity without discovery or subprocess work. The only connector name in this slice is exact lowercase `wormhole`. `OpenOperationCoordinator` uses the operation root's `locks/` directory; its `At` form reuses the exact validated operations root. `WithOperationLock` derives one filename from the validated `adapter.AdapterName()`/connector-name pair, opens it with the same Unix no-follow/owner/`0600` policy, waits on a context-cancellable OS advisory exclusive lock, and holds the file descriptor and lock continuously until the callback returns. It is non-reentrant for the same adapter/name. The callback context carries an unexported lock proof used by transaction internals; no external inspect, mutation, verification, rollback, or journal transition may occur without that proof.

Every public apply/remove call first requires `ConfirmedConnectorChange.Adapter == adapter.AdapterName()` and exact name `wormhole`, then executes one `WithOperationLock` callback for that same pair spanning, without unlock gaps: incomplete-operation recovery; native `Inspect`; strict digest of the inspected entry; expected-prior CAS against `ConfirmedConnectorChange`; confirmation/action/desired-digest validation; no-op decision; durable backup; durable `prepared` journal; external apply/remove; durable `applied`; exact desired verification; durable `verified`; and `complete`. Failure rollback, exact prior verification, and `rolled_back→complete` advancement occur under the same lock. Public `RecoverTransactions(ctx, adapter, connectorName, stores...)` validates `adapter.AdapterName()` and `connectorName`, acquires that exact pair's lock, queries `Active` for that exact pair, and calls the internal lock-required recovery path, so recovery and a new process can never interleave or select an operation through ambient/default names.

Both stores cap each record at 64 KiB; reject duplicate/unknown JSON keys, unsupported versions/enums, noncanonical UUID/reference/digest/time values, unsorted or duplicate environment names, invalid typed entries, and bytes unequal to canonical re-encoding. The reference adapter/UUID must match backup contents. Recovery additionally requires operation adapter/name == requested lock pair == `adapter.AdapterName()`/connectorName and backup adapter/name == that same operation pair; any mismatch is `ErrRecoveryConflict` before external mutation or stage advance. A backup contains only the strict prior/desired entries and metadata above; an operation contains no entry content. `Prepare` creates one durable `prepared` record only after its referenced backup is durably readable, rejects a second active adapter/name operation, and `Advance` permits only `prepared→applied|rolled_back|complete`, `applied→verified|rolled_back`, `verified→complete`, or `rolled_back→complete`; exact repeated advances are idempotent.

On Unix, both stores use no-follow opens, effective-UID ownership checks, exact `0700` roots and `0600` record/lock modes, regular single-link files, OS advisory adapter/name locks, and temp-file/fsync/rename/directory-fsync writes. Startup removes only validated orphan temporary files; it never guesses about malformed records. On non-Unix, constructors return Task 8's `config.ErrPrivateStateUnsupported` before touching a path. Fault injection proves old-or-new whole records at every write boundary. V1 retains completed operation records and every referenced backup indefinitely; transaction/recovery APIs never prune or delete them, so forensic rollback evidence cannot disappear implicitly. Backup contents, environment values, record paths, and credential-bearing errors are never logged or returned.

- [ ] **Step 1: Write failing lifecycle/round-trip tests (RED).**

~~~go
func TestCodexLifecycleUsesSupportedJSONAndExactAdd(t *testing.T) {
    runner := connectorRunner(t)
    adapter := NewCodexAdapter(runner)
    if adapter.AdapterName() != AdapterName("codex") { t.Fatalf("adapter=%q",adapter.AdapterName()) }
    prior := ConnectorEntry{State:EntryAbsent}
    desired := desiredStdio("/abs/wormhole")
    confirmed := confirmedApplyChange(t,prior,desired)
    _, err := ApplyTransactional(
        t.Context(),adapter,desired,confirmed,
        privateBackupStore(t),operationJournal(t),operationCoordinator(t),
    )
    if err != nil { t.Fatal(err) }
    assertCallsContain(t,runner,[]string{"codex","mcp","get","wormhole","--json"})
    assertCallsContain(t,runner,[]string{"codex","mcp","list","--json"})
    assertCallsContain(t,runner,[]string{"codex","mcp","add","wormhole","--","/abs/wormhole","mcp"})
}
func TestRollbackRestoresPriorAbsentAndSupportedStdio(t *testing.T) {
    for _, prior := range []ConnectorEntry{{State:EntryAbsent},typedStdioPrior(t),typedStdioPriorWithArgsAndEnv(t)} {
        runner := failingVerifyRunner(t,prior)
        desired := desiredStdio("/abs/wormhole")
        _, err := ApplyTransactional(t.Context(),runner.Adapter(),desired,confirmedApplyChange(t,prior,desired),privateBackupStore(t),operationJournal(t),operationCoordinator(t))
        if err == nil { t.Fatal("verification failure hidden") }
        if got := runner.FinalEntry(); !reflect.DeepEqual(got,prior) { t.Fatalf("got=%+v prior=%+v",got,prior) }
    }
}
func TestUnsupportedPriorFailsBeforeMutation(t *testing.T) {
    runner := unsupportedPriorRunner(t)
    backup, journal := privateBackupStore(t), operationJournal(t)
    desired := desiredStdio("/abs/wormhole")
    _, err := ApplyTransactional(t.Context(),runner.Adapter(),desired,confirmedUnsupportedFixture(t),backup,journal,operationCoordinator(t))
    if !errors.Is(err,ErrUnsupportedPriorEntry) || backup.Count() != 0 || runner.MutationCount() != 0 { t.Fatalf("backups=%d mutations=%d err=%v",backup.Count(),runner.MutationCount(),err) }
}
func TestCodexAbsentRequiresVersionedRawDiagnostic(t *testing.T) {
    fixture := "testdata/codex/0.145.0/get-absent.stderr"
    got, err := codexAdapterWithGetResult(t,1,fixture).Inspect(t.Context())
    if err != nil || got.State != EntryAbsent { t.Fatalf("got=%+v err=%v",got,err) }
    for _, result := range []config.CommandResult{{ExitCode:2,Stderr:readFixture(t,fixture)},{ExitCode:1,Stderr:[]byte("permission denied")}} {
        if _,err := codexAdapterWithRawGetResult(t,result).Inspect(t.Context()); err == nil { t.Fatalf("accepted=%+v",result) }
    }
}
func TestClaudeInspectParsesNativeFilesWithoutHealthCheck(t *testing.T) {
    fixture := claudeNativeConfigFixture(t,"2.1.220","user-stdio.json")
    got, err := fixture.Adapter.Inspect(t.Context())
    if err != nil || got.State != EntryPresent || got.Transport != TransportStdio { t.Fatalf("got=%+v err=%v",got,err) }
    if fixture.Runner.Called("claude","mcp","get") || fixture.Runner.Called("claude","mcp","list") { t.Fatalf("calls=%v",fixture.Runner.Calls()) }
}
func TestRecoveryRestoresAppliedPriorAndRejectsThirdPartyState(t *testing.T) {
    fixture := crashedAfterApplyFixture(t,typedStdioPriorWithArgsAndEnv(t))
    if err := RecoverTransactions(t.Context(),fixture.Adapter,fixture.Name,fixture.Backups,fixture.Journal,fixture.Coordinator); err != nil { t.Fatal(err) }
    if !reflect.DeepEqual(fixture.Entry(),fixture.Prior) { t.Fatalf("got=%+v prior=%+v",fixture.Entry(),fixture.Prior) }
    conflict := recoveryConflictFixture(t)
    if err := RecoverTransactions(t.Context(),conflict.Adapter,conflict.Name,conflict.Backups,conflict.Journal,conflict.Coordinator); !errors.Is(err,ErrRecoveryConflict) { t.Fatalf("err=%v",err) }
    if conflict.MutationCount() != 0 { t.Fatalf("mutations=%d",conflict.MutationCount()) }
}
func TestApplyAndRemoveRecoverAfterEveryDurableCrashPoint(t *testing.T) {
    for _, action := range []OperationAction{OperationAction("apply"),OperationAction("remove")} {
        for _, stage := range []OperationStage{OperationStage("prepared"),OperationStage("applied"),OperationStage("verified"),OperationStage("rolled_back")} {
            t.Run(string(action)+"/"+string(stage),func(t *testing.T) {
                fixture := transactionCrashFixture(t,action,stage)
                if err := RecoverTransactions(t.Context(),fixture.Adapter,fixture.Name,fixture.Backups,fixture.Journal,fixture.Coordinator); err != nil { t.Fatal(err) }
                fixture.AssertExactRecoveredState(stage)
                fixture.AssertOperationComplete()
            })
        }
    }
}
func TestRolledBackRecoveryRequiresExactPrior(t *testing.T) {
    exact := rolledBackFixture(t,true)
    if err := RecoverTransactions(t.Context(),exact.Adapter,exact.Name,exact.Backups,exact.Journal,exact.Coordinator); err != nil { t.Fatal(err) }
    if exact.MutationCount() != 0 || exact.ActiveOperation() { t.Fatalf("mutation=%d active=%v",exact.MutationCount(),exact.ActiveOperation()) }
    mismatch := rolledBackFixture(t,false)
    if err := RecoverTransactions(t.Context(),mismatch.Adapter,mismatch.Name,mismatch.Backups,mismatch.Journal,mismatch.Coordinator); !errors.Is(err,ErrRecoveryConflict) { t.Fatalf("err=%v",err) }
    if mismatch.MutationCount() != 0 { t.Fatalf("mutations=%d",mismatch.MutationCount()) }
}
func TestRecoveryAfterExternalRollbackBeforeJournalAdvance(t *testing.T) {
    for _, action := range []OperationAction{OperationAction("apply"),OperationAction("remove")} {
        for _, stage := range []OperationStage{OperationStage("prepared"),OperationStage("applied")} {
            fixture := crashAfterExternalRollbackFixture(t,action,stage)
            if err := RecoverTransactions(t.Context(),fixture.Adapter,fixture.Name,fixture.Backups,fixture.Journal,fixture.Coordinator); err != nil { t.Fatalf("%s/%s: %v",action,stage,err) }
            if !reflect.DeepEqual(fixture.Entry(),fixture.Prior) || fixture.ExternalMutationCount() != 0 { t.Fatalf("%s/%s fixture=%+v",action,stage,fixture) }
            want := []OperationStage{OperationStage("complete")}
            if stage == OperationStage("applied") { want = []OperationStage{OperationStage("rolled_back"),OperationStage("complete")} }
            fixture.AssertStagesAfterRecovery(stage,want)
        }
    }
}
func TestWithOperationLockSerializesTwoProcessApplyAndRemove(t *testing.T) {
    for _, action := range []OperationAction{OperationAction("apply"),OperationAction("remove")} {
        fixture := twoProcessTransactionFixture(t,action)
        fixture.StartBothAndReleaseBarrier()
        fixture.Wait()
        if fixture.MaximumConcurrentCriticalSections() != 1 { t.Fatalf("%s intervals=%v",action,fixture.Intervals()) }
        fixture.AssertSecondInspectOccurredAfterFirstUnlock()
        fixture.AssertExpectedFinalEntry()
    }
}
func TestConfirmedConnectorPriorDriftFailsBeforeBackupOrMutation(t *testing.T) {
    fixture := confirmedConnectorDriftFixture(t)
    _, err := ApplyTransactional(t.Context(),fixture.Adapter,fixture.Desired,fixture.Confirmation,fixture.Backups,fixture.Journal,fixture.Coordinator)
    if !errors.Is(err,config.ErrConfirmedPlanDrift) { t.Fatalf("err=%v",err) }
    if fixture.BackupCount() != 0 || fixture.OperationCount() != 0 || fixture.ExternalMutationCount() != 0 { t.Fatalf("fixture=%+v",fixture) }
}
func TestRecoveryRejectsAdapterNameOperationAndBackupPairMismatch(t *testing.T) {
    for _, fixture := range recoveryPairMismatchFixtures(t) {
        err := RecoverTransactions(t.Context(),fixture.Adapter,fixture.RequestedName,fixture.Backups,fixture.Journal,fixture.Coordinator)
        if !errors.Is(err,ErrRecoveryConflict) { t.Fatalf("%s err=%v",fixture.Case,err) }
        if fixture.ExternalMutationCount() != 0 || fixture.StageAdvanceCount() != 0 { t.Fatalf("%s fixture=%+v",fixture.Case,fixture) }
        fixture.AssertNoLockOrActiveQueryUsedAnyPairOtherThanRequested()
    }
}
func TestConnectorPrivateStoresFailClosedAndRetainEvidence(t *testing.T) {
    backups, operations := privateStoreFixtures(t)
    reference := putTypedBackup(t,backups)
    operation := prepareAndCompleteOperation(t,operations,reference)
    reopenAndAssertBackup(t,backups.Root(),reference)
    reopenAndAssertCompletedOperation(t,operations.Root(),operation.ID)
    for _, unsafe := range unsafePrivateStoreFixtures(t) {
        if err := unsafe.Open(); err == nil { t.Fatalf("accepted %s",unsafe.Name) }
    }
}
func TestConnectorErrorsRedactCredentialPaths(t *testing.T) {
    fixture := connectorFailureWithError(t,errors.New(`credentials_file=/home/alice/.wormhole/credentials/private.json key_path=C:\Users\alice\.ssh\id_ed25519`))
    _, err := fixture.Apply(t.Context())
    for _, forbidden := range []string{"/home/alice/.wormhole/credentials",`.ssh\id_ed25519`} {
        if strings.Contains(err.Error(),forbidden) || strings.Contains(fixture.Logs(),forbidden) { t.Fatalf("err=%v logs=%q",err,fixture.Logs()) }
    }
}
~~~

- [ ] **Step 2: Run RED tests.**

Run: go test ./internal/runtime/config/connector -run 'Codex|Claude|Rollback|Recovery|Crash|OperationLock|TwoProcess|Serialization|ConfirmedConnector|Absent|HTTP|UnsupportedPrior|RoundTrip|PrivateStore|CredentialPath' -count=1

Expected: FAIL because connector package is absent.

- [ ] **Step 3: Implement fail-closed native inspection and durable transaction recovery (GREEN).**

Codex 0.145.0: `Inspect` runs `codex mcp get wormhole --json` and cross-checks `codex mcp list --json`. Support absence or stdio with exact command/args and literal environment only. Reject nonempty `cwd`, `env_vars`, every HTTP entry, OAuth, headers, malformed/unknown keys, and unknown versions. Desired add is exactly `codex mcp add wormhole -- /absolute/path/to/wormhole mcp`; remove is exactly `codex mcp remove wormhole`. Exit 1 means absent only when raw stderr matches the checked-in versioned not-found fixture; every other exit/output fails closed. Codex HTTP is categorically unsupported in v1 because `get --json` omits persisted OAuth client/resource fields; do not claim arbitrary HTTP rollback.

Claude Code 2.1.220: production `Inspect` must not run `claude mcp get` or `claude mcp list`, because those commands health-check existing entries and text output loses argv boundaries. Parse bounded, duplicate-key-safe native files instead: user scope at `~/.claude.json` top-level `mcpServers`; local scope at `~/.claude.json` `projects[canonicalRoot].mcpServers`; project scope at `<canonicalRoot>/.mcp.json` `mcpServers`. Reject unsafe files, unknown target-entry fields/schema, HTTP/SSE/OAuth/header entries, and same-name presence in more than one scope. Reconstruct only exact stdio command, argument array, and string environment. Desired add is exactly `claude mcp add --scope user wormhole -- /absolute/path/to/wormhole mcp`; remove is exactly `claude mcp remove --scope user wormhole`. Mutation may use native commands only after file inspection proves the prior and never pre-removes a differing entry without a durable exact backup and operation journal.

Use the frozen stores, coordinator, confirmed change, and stage machine above. Under `WithOperationLock`, inspection must strict-decode and hash to `ExpectedPriorDigest`; otherwise return `config.ErrConfirmedPlanDrift` before backup, operation record, or external mutation. The caller-supplied desired entry must hash to `DesiredDigest`, and adapter/name/action/plan digest must match the finalized confirmation; the adapter may validate the frozen action but may not recompute a different action. A durable backup and durable `prepared` record precede mutation. No-op performs no backup, journal, or mutation. Apply/verify failure restores and verifies the exact prior or absence before advancing through `rolled_back` to `complete`. Rollback first observes current state and refuses to overwrite concurrent third-party changes. Store/decode/filesystem errors are redacted with Task 8's policy, including sensitive credential paths; tests inject `.ssh`, `.gnupg`, cloud, and Wormhole credential locations and assert that neither errors nor logs contain them.

Recovery uses the same continuous cross-process adapter/name lock and exact digest comparisons. `prepared+prior` advances directly to complete without mutation, covering both no external mutation and a partial apply already rolled back before its journal advance. `applied+prior` advances `rolled_back→complete`. `prepared+desired` and `applied+desired` restore and verify exact prior, then advance `rolled_back→complete`. `verified+desired` advances complete. `rolled_back+prior` advances complete without mutation. At every stage, any other state/digest is a third-party mismatch: return `ErrRecoveryConflict` without mutation or stage advance. A `complete` record is terminal and not active. `RemoveTransactional` uses the same confirmed CAS, coordinator, backup, journal, crash recovery, verification, and conflict rules; tests crash both apply and remove after every durable stage and after external rollback but before the journal records it.

Raw stdout, stderr, and native-config fixtures live under exact supported-version directories. Unknown version/output/config shapes fail closed. Tests cover absence, exact desired no-op, supported stdio prior, HTTP/OAuth/header rejection, spaced and empty argv, malformed/unknown version/transport, hidden-scope duplicates, immutable `AdapterName`, adapter/request/operation/backup pair mismatch, confirmed-prior/desired digest CAS, backup failure, strict backup/journal decode and canonical-size limits, no-follow/ownership/mode/platform enforcement, atomic write faults, indefinite retention, partial apply, verify mismatch, rollback failure/conflict, apply/remove crashes after every durable stage and rollback-before-advance, real two-process apply/remove serialization across the entire coordinator callback, cancellation while waiting for the lock, output bounds, secret and credential-path redaction. Fake runners record exact argv. HTTP cases assert zero backup and zero mutation; they are never rollback-success cases.

- [ ] **Step 4: Run GREEN tests without touching real harness config.**

Run: go test ./internal/runtime/config/connector -run 'Codex|Claude|Rollback|Recovery|Crash|OperationLock|TwoProcess|Serialization|ConfirmedConnector|Absent|HTTP|UnsupportedPrior|RoundTrip|ExactArgv|PrivateStore|CredentialPath|UnsupportedPlatform' -count=1

Expected: PASS using fake runners and versioned fixtures only.

Run: GOOS=windows go test -c -o /tmp/wormhole-connector-windows.test.exe ./internal/runtime/config/connector

Expected: PASS compilation with private backup/journal constructors returning `config.ErrPrivateStateUnsupported` before filesystem access.

Run: go test ./internal/runtime/config/connector -run TestRealClientReadOnlySmoke -count=1

Expected: `TestRealClientReadOnlySmoke` has independent `codex` and `claude` subtests. Each calls `CommandRunner.LookPath` and skips only its own subtest when unavailable, creates separate temporary `HOME` plus Codex/Claude config roots, and runs only version, help, get, and list against those empty roots. A recognized absent result is success. Neither subtest depends on the other client, reads the real user config, or invokes add/remove.

- [ ] **Step 5: Commit.**

~~~bash
git add internal/runtime/config/connector/connector.go internal/runtime/config/connector/transaction.go internal/runtime/config/connector/backup.go internal/runtime/config/connector/operation_journal.go internal/runtime/config/connector/operation_coordinator.go internal/runtime/config/connector/transaction_lock_unix.go internal/runtime/config/connector/private_store_unsupported.go internal/runtime/config/connector/codex.go internal/runtime/config/connector/claude.go internal/runtime/config/connector/connector_test.go internal/runtime/config/connector/transaction_test.go internal/runtime/config/connector/operation_coordinator_test.go internal/runtime/config/connector/private_store_test.go internal/runtime/config/connector/real_smoke_test.go internal/runtime/config/connector/testdata
git commit -m "feat(setup): add transactional harness adapters"
~~~

### Task 11: Orchestrate journalled setup in cmd/wormhole

**Files:**

- Create: cmd/wormhole/setup.go
- Create: cmd/wormhole/setup_test.go
- Create: cmd/wormhole/gateway_client.go
- Create: cmd/wormhole/gateway_client_test.go
- Create: cmd/wormhole/connector.go
- Create: cmd/wormhole/connector_test.go
- Modify: cmd/wormhole/main.go
- Modify: cmd/wormhole/cli_main_test.go

**Consumes:** Task 0's validated confirmed-identity type plus journal-keyed private identity RPC; Task 4's private non-starting graph inspection; Tasks 7-10 primitives; internal/types/projectstate codec; and Gateway RPCs backed by projectstate and worker manager. Runtime/config contains no setup orchestration and makes no Gateway RPC.

**Produces:**

~~~go
type setupOptions struct { CodeGraph string; NonInteractive bool }
type setupPlan struct {
    Root string
    ServiceExecutable string
    ServiceAction string
    Identity types.ConfirmedIdentitySelection
    ConnectorPlans []connector.ChangePlan
    FabricAction string
    CodeGraphMode string
    Confirmation config.SetupSelection
}
func (p setupPlan) ValidateConfirmation() error
func (p setupPlan) ValidateResumeConfirmation(config.SetupSelection) error
func runSetup(context.Context, []string, io.Reader, io.Writer, io.Writer) int
type gatewayClient struct { socketPath, canonicalRoot string }
func newGatewayClient(socketPath, canonicalRoot string) (gatewayClient, error)
func (c gatewayClient) Readiness(context.Context) error
func (c gatewayClient) RegisterWorkspace(context.Context) (projectstate.RegisterWorkspaceResult, error)
func (c gatewayClient) WorkspaceStatus(context.Context) (projectstate.WorkspaceStatus, error)
func (c gatewayClient) EnsureSelectedSetupIdentity(
    context.Context, string, types.ConfirmedIdentitySelection,
) (localidentity.HumanProfile, error)
func (c gatewayClient) SelectedLocalIdentity(context.Context) (localidentity.HumanProfile, error)
func (c gatewayClient) ImportWorkspace(context.Context) (projectstate.ImportResult, error)
func (c gatewayClient) RebuildCodeGraph(context.Context) error
func (c gatewayClient) DisableCodeGraph(context.Context) error
func (c gatewayClient) InspectCodeGraphPreference(context.Context) (codegraphmanager.Inspection, error)
func (c gatewayClient) CodeGraphStatus(context.Context) (codegraphmanager.Status, error)
func runConnector(context.Context, []string, io.Reader, io.Writer, io.Writer, connectorCommandDeps) int
~~~

`newGatewayClient` canonicalizes and validates the root once, stores it immutably in the unexported `canonicalRoot` field, and rejects an empty, relative, symlink-aliased, or nonrepository root. Every scoped RPC injects that exact root; no method accepts a replacement root, consults a later ambient cwd, or sends a workspace binding or actor envelope. The sole setup identity mutation is the private `EnsureSelectedSetupIdentity` call, which sends the exact setup journal UUID and journal-owned `ConfirmedIdentitySelection` to Task 0's idempotent RPC; the CLI never calls independent create/select methods. The RPC returns only the public profile. The interactive confirmation deliberately renders the final display name/optional email to the same human, but machine-readable status, MCP/public APIs, diagnostics, and logs never emit that selection. The exact standalone forms are wormhole connector list, wormhole connector install <codex|claude> [--yes], and wormhole connector remove <codex|claude> [--yes]. list calls Discover/Inspect and prints only adapter name, availability, state, scope, and transport. install and remove render/confirm one exact change, derive its `ConfirmedConnectorChange` prior/desired/plan digests, and invoke Task 10's coordinated durable transaction APIs; `--yes` supplies that command-local confirmation but never bypasses expected-prior CAS. No CLI output includes command environment, headers, bearer fields, private paths, backup references, or backup contents.

Fabric profile/login/attach and wormhole project/fabric commands are Slice D-owned. Setup exposes an optional Fabric stage only through D's Gateway RPC when that capability exists; otherwise it records local-only and succeeds. It never reintroduces legacy credential-profile enrollment.

Task 11 may add setup and connector dispatch cases but must not edit or delete the legacy `init`/`join`/`connect` command files, variants, contracts, docs, or tests. Task 12 retains exclusive ownership of that deletion and cutover.

- [ ] **Step 1: Write failing orchestration/recovery tests (RED).**

~~~go
func TestSetupResumesAfterEveryInjectedStageFailure(t *testing.T) {
    for _, stage := range allSetupStages() {
        t.Run(string(stage),func(t *testing.T) {
            fixture := setupFixture(t)
            fixture.FailOnce(stage)
            if code := fixture.Run(); code == 0 { t.Fatal("injected failure succeeded") }
            completed := fixture.CompletedStages()
            if code := fixture.Run(); code != 0 { t.Fatalf("resume code=%d",code) }
            if !fixture.ReverifiedBeforeSkip(completed) { t.Fatalf("calls=%v completed=%v",fixture.Calls(),completed) }
        })
    }
}
func TestSetupRendersOnePlanAndConfirmsExactlyOnce(t *testing.T) {
    fixture := setupFixture(t)
    if code := fixture.RunWithInput("yes\n"); code != 0 { t.Fatalf("code=%d",code) }
    if fixture.PlanCount() != 1 || fixture.ConfirmationCount() != 1 { t.Fatalf("plans=%d confirmations=%d",fixture.PlanCount(),fixture.ConfirmationCount()) }
    if fixture.FirstExternalMutationIndex() < fixture.ConfirmationIndex() { t.Fatalf("calls=%v",fixture.Calls()) }
    if !(fixture.ConfirmationIndex() < fixture.SelectionDurableIndex() && fixture.SelectionDurableIndex() < fixture.SelectionReadbackIndex() && fixture.SelectionReadbackIndex() < fixture.FirstExternalMutationIndex()) { t.Fatalf("calls=%v",fixture.Calls()) }
}
func TestDeclinedSetupLeavesOnlyUnselectedValidationJournal(t *testing.T) {
    fixture := setupFixture(t)
    if code := fixture.RunWithInput("no\n"); code == 0 { t.Fatalf("code=%d",code) }
    if fixture.ExternalMutationCount() != 0 { t.Fatalf("calls=%v",fixture.Calls()) }
    journal := fixture.Journal()
    if journal.Selection != nil || !slices.Equal(journal.Completed,[]config.SetupStage{"project_validated"}) { t.Fatalf("journal=%+v",journal) }
}
func TestGatewayClientUsesOneImmutableCanonicalRoot(t *testing.T) {
    client, server := gatewayClientFixture(t,"/repo/a")
    chdirForTest(t,"/repo/b")
    _, _ = client.RegisterWorkspace(t.Context())
    _, _ = client.WorkspaceStatus(t.Context())
    _, _ = client.SelectedLocalIdentity(t.Context())
    _, _ = client.InspectCodeGraphPreference(t.Context())
    _, _ = client.CodeGraphStatus(t.Context())
    if got := server.RequestRoots(); !allEqual(got,"/repo/a") { t.Fatalf("roots=%v",got) }
}
func TestPreConsentGraphInspectionNeverStartsWorker(t *testing.T) {
    fixture := setupFixture(t)
    fixture.StopAfterConfirmationPrompt()
    fixture.RunWithInput("no\n")
    if fixture.CodeGraphStatusCalls() != 0 || fixture.WorkerStarts() != 0 || fixture.CodeGraphInspectionCalls() != 1 { t.Fatalf("calls=%v",fixture.Calls()) }
}
func TestGatewayInitiallyUnavailableStillUsesOneConfirmedPlan(t *testing.T) {
    fixture := setupFixture(t)
    fixture.SetGatewayInitiallyUnavailable()
    if code := fixture.RunWithInput("yes\n"); code != 0 { t.Fatalf("code=%d calls=%v",code,fixture.Calls()) }
    if fixture.PlanCount() != 1 || fixture.ConfirmationCount() != 1 { t.Fatalf("plans=%d confirmations=%d",fixture.PlanCount(),fixture.ConfirmationCount()) }
    fixture.AssertBoundedPreconditionsUsed("workspace_registered","identity_selected","base_imported","code_graph_resolved")
    fixture.AssertNoWorkerStatusBeforeSelection()
    fixture.AssertConfirmationEnvelopeUnchangedAfterGatewayStart()
    fixture.AssertExactPostConfirmationOrder()
}
func TestSetupNeverExecutesRepositoryContent(t *testing.T) {
    fixture := setupFixtureWithManifest(t,"hooks = [\"touch sentinel\"]")
    if code := fixture.Run(); code != 0 { t.Fatalf("code=%d",code) }
    if _, err := os.Stat(filepath.Join(fixture.Root,"sentinel")); !errors.Is(err,os.ErrNotExist) { t.Fatalf("sentinel err=%v",err) }
}
func TestNonInteractiveRequiresExplicitCodeGraphChoice(t *testing.T) {
    fixture := setupFixture(t)
    if code := fixture.RunArgs("--non-interactive"); code != 2 { t.Fatalf("code=%d",code) }
}
func TestConnectorListInstallRemoveUseTransactionalAdapters(t *testing.T) {
    deps := connectorCommandFixture(t)
    if code := runConnector(t.Context(),[]string{"list"},strings.NewReader(""),io.Discard,io.Discard,deps); code != 0 { t.Fatalf("list=%d",code) }
    if code := runConnector(t.Context(),[]string{"install","codex","--yes"},strings.NewReader(""),io.Discard,io.Discard,deps); code != 0 { t.Fatalf("install=%d",code) }
    if code := runConnector(t.Context(),[]string{"remove","codex","--yes"},strings.NewReader(""),io.Discard,io.Discard,deps); code != 0 { t.Fatalf("remove=%d",code) }
    if !deps.InstallAndRemoveUsedDurableRecovery() { t.Fatalf("actions=%v",deps.Actions()) }
    if deps.RealConfigTouched() || deps.OutputContainsSecret() { t.Fatal("connector command escaped fake/redacted boundary") }
}
func TestResumeReverifiesIdentityReadinessAndCodeGraphBeforeSkip(t *testing.T) {
    fixture := completedSetupFixture(t)
    fixture.BreakGatewayReadiness()
    fixture.SelectDifferentIdentity()
    fixture.MakeCodeGraphStale()
    if code := fixture.Resume(); code != 0 { t.Fatalf("code=%d calls=%v",code,fixture.Calls()) }
    fixture.AssertReadbackBeforeRepair("gateway_ready","identity_selected","code_graph_resolved")
    fixture.AssertSelectedIdentity(fixture.Journal().SelectedHumanID)
    fixture.AssertCodeGraphReadyAndCurrent()
}
func TestConfirmedPlanDriftBeforeEffectFailsWithZeroMutation(t *testing.T) {
    for _, subject := range []string{"gateway-service","identity","connector:codex","fabric","code-graph"} {
        fixture := confirmedSetupFixture(t)
        before := fixture.JournalBytes()
        fixture.ResetWriteCounters()
        fixture.DriftConfirmedSubjectOutsidePriorAndDesired(subject)
        code := fixture.ContinueAfterConfirmation()
        if code == 0 || !errors.Is(fixture.Err(),config.ErrConfirmedPlanDrift) { t.Fatalf("%s code=%d err=%v",subject,code,fixture.Err()) }
        if fixture.AnyWriteCount() != 0 || fixture.ExternalMutationCount() != 0 || fixture.BackupCount() != 0 || fixture.OperationCount() != 0 || fixture.RecordLastErrorCalls() != 0 { t.Fatalf("%s calls=%v",subject,fixture.Calls()) }
        if !bytes.Equal(before,fixture.JournalBytes()) { t.Fatalf("%s journal changed",subject) }
    }
}
func TestCrashResumeUsesConfirmedDesiredWithoutReplanning(t *testing.T) {
    for _, subject := range []string{"gateway-service","identity","connector:codex","fabric","code-graph"} {
        fixture := setupFixture(t)
        fixture.CrashAfterDesiredBeforeStageMark(subject)
        if code := fixture.RunWithInput("yes\n"); code == 0 { t.Fatalf("%s first run succeeded",subject) }
        if code := fixture.Resume(); code != 0 { t.Fatalf("%s resume=%d err=%v",subject,code,fixture.Err()) }
        if fixture.ConfirmationCount() != 1 || fixture.ReplannedAction(subject) { t.Fatalf("%s calls=%v",subject,fixture.Calls()) }
        fixture.AssertAcceptedConfirmedDesired(subject)
    }
}
func TestSetupResumeAfterServiceAndConnectorEffectsDoesNotFalseDrift(t *testing.T) {
    fixture := setupFixture(t)
    fixture.CrashAfterStagesMarked("gateway_ready","workspace_registered","identity_selected","base_imported","connectors_applied")
    if code := fixture.RunWithInput("yes\n"); code == 0 { t.Fatal("injected crash succeeded") }
    fixture.AssertCurrentStateEqualsConfirmedDesired("gateway-service","connector:codex")
    if code := fixture.Resume(); code != 0 { t.Fatalf("resume=%d err=%v calls=%v",code,fixture.Err(),fixture.Calls()) }
    if fixture.ConfirmationCount() != 1 || fixture.ReplannedAction("gateway-service") || fixture.ReplannedAction("connector:codex") { t.Fatalf("calls=%v",fixture.Calls()) }
    fixture.AssertResumeComparedFrozenPriorOrDesired("gateway-service","connector:codex")
}
func TestSetupIdentityCrashResumeAcrossFreshProcessesIsExactlyOnce(t *testing.T) {
    for _, point := range []string{"after-confirmation-before-rpc","after-identity-receipt","after-identity-key-profile","after-identity-selected","after-rpc-before-bind","after-bind-before-stage-mark"} {
        fixture := newOnDiskSetupProcessFixture(t)
        fixture.RunFirstCLIProcessUntilHardExit(point,"yes\n")
        confirmed := fixture.ReopenJournalStore().MustSelection().Identity
        second := fixture.RunResumeInFreshCLIAndGatewayProcesses()
        third := fixture.CallEnsureSetupIdentityInAnotherFreshGatewayProcess(confirmed)
        if second.ExitCode != 0 || third.Err != nil { t.Fatalf("%s second=%+v third=%+v",point,second,third) }
        fixture.AssertExactlyOneHumanProfile()
        fixture.AssertSameHumanProfilePublicKeyAndSigningContinuity(second,third)
        fixture.AssertSelectedProfileMatches(confirmed)
        fixture.AssertJournalBindsSameSelectedHumanID(second.HumanPrincipalID)
        fixture.AssertConfirmationCount(1)
    }
}
~~~

- [ ] **Step 2: Run RED tests.**

Run: go test ./cmd/wormhole -run 'Setup|GatewayClient|NonInteractive|NeverExecutes|ResumeReverifies|ConfirmedPlanDrift|CrashResume|PreConsentGraphInspection|GatewayInitiallyUnavailable' -count=1

Expected: FAIL because setup.go and Gateway client are absent.

- [ ] **Step 3: Implement ordered, resumable, one-confirmation orchestration (GREEN).**

Execute this exact order:

1. Find nearest `.wormhole`, canonicalize its root, and decode/validate canonical project state without executing repository content.
2. Resume or begin the setup journal and mark `project_validated`; this owner-private journal write is the only pre-consent state change and `Selection` remains nil.
3. Perform read-only discovery: resolve and validate the exact Gateway executable and service action; call direct Gateway readiness/workspace/identity reads only when already reachable; read Git identity suggestions; connector `Discover`/`Inspect`/`Plan`; derive Slice D capability from the resolved executable/version; and, only when Gateway is reachable, call the private non-starting `InspectCodeGraphPreference`. Pre-consent setup never calls worker-backed `CodeGraphStatus`. Resolve unsupported/ambiguous connector blockers before consent.
4. Resolve every choice and construct/render one complete `setupPlan` describing the service, registration, final identity display name/optional email plus `ensure-selected`/`ensure-ed25519`, base import, every connector action, Fabric/local-only action, and graph mode. Build its canonical `SetupSelection`: persist that required bounded `ConfirmedIdentitySelection`, hash the overall strict confirmation envelope and each confirmed prior predicate/desired state, and validate it. All other raw plan values remain memory-only. Noninteractive setup exits 2 here unless `--code-graph=on|off`, an already-finalized active-journal selection, or an existing durable graph preference supplies the choice.
5. Interactive setup obtains exactly one confirmation for that whole plan. A decline leaves only the active `project_validated` journal with nil selection and performs zero external mutation. Resume with an already-finalized selection never reprompts.
6. Immediately after consent, durably call `SetSelection` with the exact safe confirmation/digests and read the journal back byte-for-byte before proceeding. This finalized record is the recovery authority. No service, Gateway, identity, connector, Fabric, graph, user-config, or repository mutation occurs before that durable readback.
7. Inspect/install/start/verify `gatewayd`. Manager-unavailable may proceed only if direct Gateway `Readiness` already succeeds; otherwise return the exact manual diagnostic with the journal active.
8. Register the workspace, consume only `RegisterWorkspaceResult.Binding`, and bind its workspace UUID in the journal.
9. Call `EnsureSelectedSetupIdentity` with the setup journal UUID and its exact finalized `Selection.Identity`; the private Gateway/localidentity receipt creates or reuses and selects exactly one matching Ed25519 human idempotently. Then bind the returned human UUID in the setup journal and read it back with `SelectedLocalIdentity`. A crash before or after the RPC, selection, return, or bind repeats the same intent and cannot create a second profile/key.
10. Refresh/import the accepted base through Gateway using the selected Gateway-owned actor. This deliberate register → identity → actor-attributed import ordering resolves the architecture outline's import-before-identity conflict with Slice A's valid-actor requirement.
11. Apply connectors transactionally and record every nonempty opaque returned backup reference. Connector failure restores only connector state and leaves the imported workspace intact.
12. Feature-detect Slice D's future Fabric RPC; when absent, record local-only resolution. Task 11 adds no Fabric schema, profile, command, or auth type.
13. For code graph `on`, explicitly rebuild and require `CodeGraphStatus` ready/current; for `off`, explicitly disable and read back disabled status.
14. Reverify Gateway, workspace, selected identity, connectors, Fabric/local-only resolution, and graph; mark `final_verified`; call journal `Complete`.

Planning does not depend on unknowable post-start state when Gateway is initially unavailable. The confirmation freezes these deterministic actions and bounded prior predicates:

- service: exact resolved executable identity, exact unit/socket bytes, and `noop|start|install` action; both its observed pre-consent state and ready desired state are hashed;
- workspace: `register` the exact immutable root/project/accepted commit/tree, whose prior predicate is `absent-or-exact` and whose desired predicate requires those exact semantic fields plus one canonical Gateway-owned workspace UUID, bound in the journal on first success rather than guessed before consent;
- identity: the exact durable `ConfirmedIdentitySelection` (`ensure-selected`, final bounded display name, optional email, `ensure-ed25519`) is both execution input and part of `PlanDigest`; any strictly valid local identity-store state is the bounded prior, while the desired predicate requires the receipt-bound human to have exactly those attributes, one valid matching Ed25519 keypair, and selected status. The first successful UUID is bound and read back rather than guessed before consent;
- base import: exact registered workspace with `unimported-or-already-imported-exact` prior and exact accepted commit/tree desired;
- connectors: exact inspected typed prior and exact desired entry per adapter; no bounded wildcard is permitted;
- Fabric: a capability/action derived from the confirmed Gateway executable; C11's desired state is local-only and does not invent or attach a binding;
- graph: `set-mode` for the confirmed `on|off`, with any strictly valid persisted preference/cache row as the bounded prior and exact desired preference/readiness predicate as desired. If Gateway was unreachable, this predicate replaces an unknown cached-status read; it never starts a worker.

These predicate descriptions have fixed canonical encodings and are what `PriorDigest`/`DesiredDigest` hash; a bounded digest is not the digest of an unavailable guessed value. After service readiness, each evaluator accepts only the exact alternatives its confirmed predicate names. Thus service start does not itself cause workspace/identity/base/graph plan drift, while malformed or out-of-bound state still fails closed. The initially-unavailable acceptance fixture has no pre-existing workspace/identity conflict and completes under one confirmation.

The post-confirmation external effect order is therefore fixed as service → workspace registration → identity selection → actor-attributed base import → connectors → Fabric/local-only resolution → code graph → final verification. Journal validation and finalized-selection writes do not broaden that order into product or user-config mutation.

On every resume, load the finalized selection as the action authority and revalidate only stable planning inputs: the immutable root/project state, finalized user choices, resolved executable identity, the persisted validated `ConfirmedIdentitySelection`, adapter availability/version plus desired-entry rendering, and safe Fabric/graph capability inputs. Never reread a changed Git identity suggestion to replace the confirmed identity. `ValidateResumeConfirmation` freshly hashes every derivable desired state and every fixed bounded-predicate encoding, carries forward each stored exact-prior digest, rebuilds the canonical confirmation envelope, and requires the same safe actions, identity selection, per-change digests, and `PlanDigest`. It must not run `Plan(current,current-desired)`, replace a confirmed action with `noop`, or hash post-effect service/connector state as a new prior. Current mutable readbacks are evidence for the next paragraph, not inputs that choose a new plan. Drift in any stable input, desired state, bounded predicate, or action legality returns `ErrConfirmedPlanDrift`, creates no backup/operation, performs no external mutation, and performs no journal/store write—not even `RecordLastError`; compare the old journal bytes unchanged. A different action may run only after a separately rendered confirmation calls `BeginConfirmedReplacement` to create a new journal; setup never silently replans inside the old confirmation.

Immediately before every stage, whether its journal marker is incomplete or complete, compare the current readback against the frozen confirmed predicates. If it matches the confirmed prior, run or rerun only the confirmed idempotent action and verify the confirmed desired state. If it already matches confirmed desired, verify it and mark or skip the stage without replay. Any other state is `ErrConfirmedPlanDrift`. A completed marker never authorizes a skip by itself. These checks never synthesize a new action from observed state, and service startup or a completed connector mutation therefore cannot create false drift merely because the current state is now desired.

Before skipping any completed stage on resume, run its exact read-only predicate:

- `project_validated`: the client's immutable canonical root equals the journal root and the current tracked project state still strict-decodes and validates without executing repository content.
- `gateway_ready`: direct `gatewayClient.Readiness` succeeds; when systemd-user is usable, `GatewayService.Inspect` also reports `Installed`, `Running`, and `Ready`, while manual mode requires only direct readiness.
- `workspace_registered`: `WorkspaceStatus` reports the client's canonical root and the exact journal workspace UUID.
- `identity_selected`: `SelectedLocalIdentity` reports the exact journal `SelectedHumanID`; Task 0's receipt for that journal UUID names the same human/public key, and the profile exactly matches finalized `Selection.Identity` with a valid Ed25519 keypair.
- `base_imported`: `WorkspaceStatus` reports the validated project's accepted commit/tree digest and a recovered imported base with no unresolved import conflict.
- `connectors_applied`: each adapter in finalized `Selection.ConnectorAdapters` re-inspects as the exact desired user-scope stdio entry and has no active Task 10 operation; skipped unavailable adapters are absent from the finalized selection.
- `fabric_resolved`: when Slice D is absent, capability detection still proves local-only; when present later, its readback must match the planned binding before this predicate can pass.
- `code_graph_resolved`: `CodeGraphStatus` is disabled for `off`; for `on` it is enabled, `StateReady`, current, `GraphNotCurrent==false`, and not rebuild-required.
- `final_verified`: every preceding predicate succeeds in order in the same run.

A failed desired-state predicate is never silently skipped: rerun that stage's frozen idempotent effect only when the readback matches its confirmed prior; when it matches neither prior nor desired, return its typed conflict/error. `ErrConfirmedPlanDrift` is the no-write exception and must return without `RecordLastError` or any other journal/store update; other operational errors may record a redacted `LastError`. Mark each stage only after its desired predicate passes. Persisted selections make resume deterministic and prevent reprompting.

Unavailable harnesses are reported and skipped. A supported adapter with an ambiguous/unsupported prior is a non-mutating incomplete connector stage; registration/import remains intact and retryable. Setup does not demand rollback of HTTP/OAuth/ambiguous transports rejected by Task 10 before mutation. Fabric unavailable resolves local-only; graph failure leaves the workspace usable. Missing dependencies return Task 5's offline error and explain that separate dependency-fetch consent is required; setup never silently downloads.

- [ ] **Step 4: Run GREEN tests.**

Run: go test ./cmd/wormhole -run 'Setup|GatewayClient|JournalResume|ResumeReverifies|ConfirmedPlanDrift|CrashResume|PreConsentGraphInspection|GatewayInitiallyUnavailable|LocalOnly|ConnectorFailure|CodeGraphFailure|NeverExecutes|NonInteractive' -count=1

Expected: PASS.

- [ ] **Step 5: Commit.**

~~~bash
git add cmd/wormhole/setup.go cmd/wormhole/setup_test.go cmd/wormhole/gateway_client.go cmd/wormhole/gateway_client_test.go cmd/wormhole/connector.go cmd/wormhole/connector_test.go cmd/wormhole/main.go cmd/wormhole/cli_main_test.go
git commit -m "feat(cli): orchestrate journalled setup"
~~~

### Task 12: Cut over commands, parity, acceptance, and coverage

**Files:**

- Delete: cmd/wormhole/init.go
- Delete: cmd/wormhole/init_test.go
- Delete: cmd/wormhole/connect.go
- Delete: cmd/wormhole/connect_test.go
- Delete: cmd/wormhole/connect_status_coverage_test.go
- Delete: cmd/wormhole/cli_connect_opencode_test.go
- Delete: cmd/wormhole/cli_main_join_socket_test.go
- Delete: internal/runtime/localapi/localapi_join_test.go
- Modify: cmd/wormhole/main.go
- Modify: cmd/wormhole/cli_main_test.go
- Modify: cmd/wormhole/cli_coverage_behavior_test.go
- Modify: cmd/wormhole/cli_error_paths_test.go
- Modify: cmd/wormhole/contract_manifest_test.go
- Modify: cmd/wormhole/code_graph.go
- Modify: cmd/wormhole/code_graph_test.go
- Modify: cmd/wormhole/workspace.go
- Modify: cmd/wormhole/workspace_test.go
- Modify: internal/runtime/localapi/localapi.go
- Modify: internal/runtime/localapi/mcp.go
- Modify: internal/runtime/localapi/localapi_proxy_errors_test.go
- Modify: internal/runtime/localapi/localapi_qa_test.go
- Modify: internal/runtime/localapi/contract_manifest_test.go
- Modify: cmd/gatewayd/codegraph_gate_b_process_test.go
- Modify: docs/contracts/alpha-contract.json
- Modify: docs/contracts/README.md
- Modify: README.md
- Modify: docs/claude-code-connector.md
- Modify: docs/compatibility.md

This inventory was frozen from the live tree with the following targeted search; rerun it immediately before editing and explicitly classify any newly reported path rather than staging it implicitly:

~~~bash
rg -l --glob '*.go' --glob '*.md' --glob '*.json' 'runJoin|runConnect|runInit|wormhole (join|connect|init)|case "(join|connect|init)"|join_connect|connect\.opencode|agentJoinRegisterArgs|isJoinRegisterArgs|proxyRegister|localJoinResult|"variant": "join"|"name": "(join|connect)"' cmd/wormhole internal/runtime/localapi docs/contracts README.md docs/claude-code-connector.md docs/compatibility.md | sort
~~~

The frozen 2026-07-28 result is README.md; cmd/wormhole/cli_connect_opencode_test.go; cmd/wormhole/cli_coverage_behavior_test.go; cmd/wormhole/cli_error_paths_test.go; cmd/wormhole/cli_main_test.go; cmd/wormhole/connect_status_coverage_test.go; cmd/wormhole/connect_test.go; cmd/wormhole/contract_manifest_test.go; cmd/wormhole/init.go; cmd/wormhole/init_test.go; cmd/wormhole/main.go; docs/claude-code-connector.md; docs/contracts/alpha-contract.json; internal/runtime/localapi/localapi.go; internal/runtime/localapi/localapi_join_test.go; internal/runtime/localapi/localapi_proxy_errors_test.go; internal/runtime/localapi/localapi_qa_test.go; and internal/runtime/localapi/mcp.go. The explicit inventory additionally includes helper-only cmd/wormhole/connect.go, socket coverage cmd/wormhole/cli_main_join_socket_test.go, final command/contract tests, and the documentation files that must describe the replacement surface.

**Public command surface in this slice:**

~~~text
wormhole setup [--code-graph=on|off] [--non-interactive]
wormhole status|diff|import|checkpoint|stash
wormhole connector list
wormhole connector install <codex|claude> [--yes]
wormhole connector remove <codex|claude> [--yes]
wormhole code-graph status|query|rebuild|disable
wormhole mcp
wormhole.workspace.status|diff|import|checkpoint|stash
wormhole.code_graph.status|query|rebuild
~~~

wormhole project and wormhole fabric commands remain explicitly dependent on Slice D and are not claimed shipped by this plan.

- [ ] **Step 1: Write executable cutover and end-to-end tests (RED).**

~~~go
func TestRemovedCommandsAreUnknownAndAbsentFromUsage(t *testing.T) {
    for _, command := range []string{"init","join","connect"} {
        var out, stderr bytes.Buffer
        if code := run([]string{command},&out,&stderr); code != 2 { t.Fatalf("%s code=%d",command,code) }
        if strings.Contains(usageText(t),"wormhole "+command) { t.Fatalf("%s remains in usage",command) }
    }
}
func TestCLIAndMCPWorkspaceOperationsAreSemanticallyEqual(t *testing.T) {
    for _, operation := range []string{"status","diff","import","checkpoint","stash"} {
        t.Run(operation,func(t *testing.T) {
            cli := decodeSemanticResult(t,callCLIFromCWD(t,"/repo/a",operation))
            mcp := decodeSemanticResult(t,callMCPFromCWD(t,"/repo/a","wormhole.workspace."+operation))
            if !reflect.DeepEqual(cli,mcp) { t.Fatalf("cli=%+v mcp=%+v",cli,mcp) }
        })
    }
}
func TestOneGatewayIsolatesTwoWorkspacesAcrossWorkerCrashAndRestart(t *testing.T) {
    fixture := twoWorkspaceProcessFixture(t)
    fixture.WriteTask("a","task-a")
    fixture.RebuildGraph("a")
    fixture.RebuildGraph("b")
    fixture.KillWorker("a")
    if got := fixture.ListTasks("b"); slices.Contains(got,"task-a") { t.Fatalf("leak=%v",got) }
    if err := fixture.QueryGraph("b","KnownB"); err != nil { t.Fatalf("workspace b query: %v",err) }
    fixture.RestartGateway()
    if got := fixture.WorkspaceStatus("a").WorkspaceID; got != fixture.Binding("a").Scope.WorkspaceID { t.Fatalf("got=%s",got) }
}
func TestConnectorFailureRestoresExactSupportedPriorOrAbsence(t *testing.T) {
    for _, prior := range []connector.ConnectorEntry{{State:connector.EntryAbsent},typedStdioPrior(t),typedStdioPriorWithArgsAndEnv(t)} {
        fixture := setupConnectorFailureFixture(t,prior)
        if code := fixture.Run(); code == 0 { t.Fatal("verification failure succeeded") }
        if got := fixture.Entry(); !reflect.DeepEqual(got,prior) { t.Fatalf("got=%+v prior=%+v",got,prior) }
        if !fixture.WorkspaceImported() { t.Fatal("workspace rolled back") }
    }
}
func TestUnsupportedConnectorPriorFailsBeforeMutation(t *testing.T) {
    for _, prior := range []unsupportedPrior{codexHTTPPrior(t),codexOAuthPrior(t),claudeHTTPPrior(t),claudeHiddenScopeDuplicate(t)} {
        fixture := setupUnsupportedConnectorFixture(t,prior)
        if code := fixture.Run(); code == 0 { t.Fatal("unsupported prior succeeded") }
        if !errors.Is(fixture.Err(),connector.ErrUnsupportedPriorEntry) { t.Fatalf("err=%v",fixture.Err()) }
        if fixture.BackupCount() != 0 || fixture.MutationCount() != 0 { t.Fatalf("backup=%d mutation=%d",fixture.BackupCount(),fixture.MutationCount()) }
        if !fixture.WorkspaceImported() { t.Fatal("workspace rolled back") }
    }
}
~~~

- [ ] **Step 2: Run RED tests.**

Run: go test ./cmd/wormhole ./cmd/gatewayd ./internal/runtime/localapi -run 'RemovedCommands|SemanticallyEqual|OneGatewayIsolates|ConnectorFailure|UnsupportedConnectorPrior|Contract' -count=1

Expected: FAIL until cutover/integration is complete.

- [ ] **Step 3: Implement contract cutover and safe smoke tests (GREEN).**

Remove init/join/connect from dispatcher, runInit/runJoin/runConnect callers and helpers, usage, tests, guidance, docs, and JSON contract with no aliases. Remove the join-shaped wormhole.agent.register variant, agentJoinRegisterArgs, isJoinRegisterArgs, proxyRegister, and localJoinResult while preserving the unrelated MCP initialize handshake. Modify the Slice-A cmd/wormhole/workspace.go and workspace_test.go so status/diff/import/checkpoint/stash are top-level commands only; there is no wormhole workspace subcommand. Register final workspace/code-graph tools without cwd or machine-ID schema fields. CLI workspace and graph operations call Gateway from cwd and render the same semantic result as MCP. Dispatch connector list/install/remove to the tested connector.go implementation. Update docs only for tested B/C commands; mark project/fabric administration as Slice D-dependent.

All automated adapter tests use fake runners/config roots. Real-client smoke is limited to isolated read-only version/help/get/list capability checks; no test adds or removes a real entry. There is no real mutation smoke in make check. Supported stdio priors prove transactional rollback. HTTP/OAuth/ambiguous priors prove `ErrUnsupportedPriorEntry`, zero backup, zero mutation, and preservation of the already-imported workspace; they are not rollback-success cases.

- [ ] **Step 4: Run focused, scale, and full verification (GREEN).**

Run: go test ./cmd/wormhole ./cmd/gatewayd ./internal/runtime/localapi ./internal/runtime/codegraph/... ./internal/runtime/config/... -count=1

Expected: PASS.

Run: rg -l --glob '*.go' --glob '*.md' --glob '*.json' 'runJoin|runConnect|runInit|wormhole (join|connect|init)|case "(join|connect|init)"|join_connect|connect\.opencode|agentJoinRegisterArgs|isJoinRegisterArgs|proxyRegister|localJoinResult|"variant": "join"|"name": "(join|connect)"' cmd/wormhole internal/runtime/localapi docs/contracts README.md docs/claude-code-connector.md docs/compatibility.md

Expected: no output; every legacy init/join/connect command, helper, variant, contract entry, and test is gone.

Run: go test ./internal/runtime/codegraph/query -run TestScaleAcceptance250K -count=1 -timeout=10m

Expected: p95 at most 300ms and heap growth at most 128MiB on the fixed 250k/2m fixture.

Run: make check

Expected: format, build, vet, integration, race, and coverage pass; merged statement coverage is at least 80 percent.

- [ ] **Step 5: Commit exact files without broad staging.**

~~~bash
git add cmd/wormhole/main.go cmd/wormhole/cli_main_test.go cmd/wormhole/cli_coverage_behavior_test.go cmd/wormhole/cli_error_paths_test.go cmd/wormhole/contract_manifest_test.go cmd/wormhole/code_graph.go cmd/wormhole/code_graph_test.go cmd/wormhole/workspace.go cmd/wormhole/workspace_test.go internal/runtime/localapi/localapi.go internal/runtime/localapi/mcp.go internal/runtime/localapi/localapi_proxy_errors_test.go internal/runtime/localapi/localapi_qa_test.go internal/runtime/localapi/contract_manifest_test.go cmd/gatewayd/codegraph_gate_b_process_test.go docs/contracts/alpha-contract.json docs/contracts/README.md README.md docs/claude-code-connector.md docs/compatibility.md
git rm cmd/wormhole/init.go cmd/wormhole/init_test.go cmd/wormhole/connect.go cmd/wormhole/connect_test.go cmd/wormhole/connect_status_coverage_test.go cmd/wormhole/cli_connect_opencode_test.go cmd/wormhole/cli_main_join_socket_test.go internal/runtime/localapi/localapi_join_test.go
git commit -m "feat(cli): complete gateway setup cutover"
~~~

## Dependencies

- Task 1 begins after Slice A localapi workspace operations and projectstate resolver exist.
- Task 2 follows Task 1; its FabricRouter is local-only/legacy until Slice D supplies the final multi-Fabric implementation.
- Task 3 follows Slice A shared binding freeze. Task 4 follows Task 0's private setup-control RPC and Task 3. Task 5 follows Tasks 3-4. Task 6 follows Task 5.
- Task 7 first freezes and ships the shared `CommandRunner` contract.
- Tasks 8 and 9 may then proceed independently after Task 0 freezes `types.ConfirmedIdentitySelection`; Task 8 consumes that exact validated type in journal v1.
- Task 10 consumes Task 7's shared runner plus Task 8's `StateDigest`, confirmed-plan error, opaque `config.BackupReference`, redaction, and private-platform contracts, but owns the cross-process operation coordinator, fully specified connector backup contents, operation journal, retention, and recovery.
- Task 11 consumes completed Tasks 7-10 and Gateway RPCs from Tasks 1-6.
- Task 12 consumes every prior task and retains all legacy deletion/cutover ownership. Slice D owns project/fabric CLI and authenticated attach/login; Slice E owns private human authentication.

## Self-review

- Shared ownership: the exact Slice-A types.WorkspaceBinding flows from registration/resolution/enumeration; types.RepositoryIdentity is reused, its digest remains a validated string, and only codegraphmanager.ScopeFromBinding copies it into the stdlib-only string Scope.
- Routing: the bridge overwrites forged private cwd before forwarding, Gateway strips it before schema validation, and every project/sync/graph/auth path records the resolved binding in forged/cross-workspace coverage.
- Supervisor seams: FabricRouter and CodeGraphProvider have exact binding-scoped methods, non-nil fail-closed implementations, and errors.Is coverage for their typed unavailable errors.
- Workers: separate process, DB, socket, private caches, offline/sanitized environment, read-only checkout, crash/restart/disable isolation, plus the Task 4 extension to Task 0's private setup-control RPC for cached preference inspection that never starts a worker.
- Graph: exact schema/status fingerprints, manifest ordering/restart/failed-publish tests, explicit BM25/tokenizer constants, held-out recall/disclosure/determinism/scale gates.
- Shared execution: one runner contract has exact exit/output/context semantics and is reused by service, Git identity, and connectors.
- Lexical ownership: Task 3 owns scoped schema and cascading lifecycle; Task 6 populates lexical rows in `index/build.go` before publication, validates exact completeness in the publication transaction, and owns documentation extraction, BM25 query, and held-out/scale acceptance.
- Service: one exact unit/socket/runtime-root contract, positive fixtures derived from configured `ResolveRuntimePaths`, fail-closed manager probing, and byte-identical active idempotence.
- Journals: config-owned `StateDigest`, safe overall/per-change confirmed digests, required bounded `types.ConfirmedIdentitySelection` in unshipped v1 as the sole PII exception, canonical owner-only per-UUID files, OS locks, nil-before-confirmation immutable selection, explicit drift replacement, selected identity, terminal completion, ambiguous-resume rejection, credential-path redaction, Unix enforcement/non-Unix rejection, opaque backup references, and old-or-new fault recovery.
- Git identity: exactly four read-only config keys, explicit unset behavior, canonical-root proof, and OpenPGP-only v1 signing without key-file access.
- Setup: the Gateway client binds one immutable canonical root; one full plan/confirmation and durable digest-plus-identity execution selection precede external effects; the journal-keyed private identity RPC survives real process crash/reopen without duplicate human/profile/key; initially unavailable Gateway state is represented by confirmed bounded idempotent predicates; resume revalidates stable inputs and the frozen confirmation envelope, then compares mutable state with confirmed prior-or-desired predicates without replanning from post-effect state; `ErrConfirmedPlanDrift` leaves old journal bytes unchanged; every completed stage has an exact readiness/workspace/identity/connector/Fabric/graph readback predicate; runtime/config does not call Gateway.
- Connectors: exact-version absent/provable-stdio support, native Claude config parsing without health checks, HTTP/OAuth rejection before mutation, immutable `AdapterName` plus exact requested connector name, one pair-scoped `WithOperationLock` critical section covering recovery/CAS/backup/journal/external mutation/verify/rollback, adapter/request/operation/backup mismatch rejection, two-process apply/remove serialization, fully specified strict private stores with indefinite retention, exact prepared/applied/verified/rolled-back recovery, and independently availability-gated isolated real-client smoke.
- C12 boundary: legacy deletion, docs/contracts cutover, and final integration remain C12-owned; earlier tasks do not edit those legacy surfaces.
- Verification: acceptance tests have executable assertions, normal tests never mutate real user connector state, the current rg -l cutover inventory is frozen, final add/rm staging lists every path explicitly, and the 80-percent gate is explicit.
