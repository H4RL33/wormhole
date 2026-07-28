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
- Connector mutation is forbidden until the prior entry is represented by a typed, round-trippable absent/stdio/http state and a rollback plan is durable.
- Setup journals and connector backups are 0600 machine-private files; journals contain no raw credential. Connector backup contents are never logged or returned.
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
| internal/runtime/localidentity | Owner-only human, Ed25519 key, durable agent, selection, and connection-session records. |
| internal/runtime/localapi/actor.go | Resolve CLI selection and MCP clientInfo into server-owned local ActorEnvelope values. |
| internal/runtime/localapi/request_scope.go | Strip bridge context and attach one validated binding to request context. |
| cmd/wormhole/mcp.go | Observe cwd and inject bridge-only context. |
| cmd/gatewayd/gatewayd.go | Construct all local dependencies and recover one supervisor. |
| internal/runtime/codegraph/store | Workspace DB schema, fingerprints, lexical documents, and legacy invalidation. |
| internal/runtime/codegraph/worker | On-demand process manager, private protocol, sandbox policy, crash isolation. |
| internal/runtime/codegraph/manager | Convert frozen bindings to stdlib-only graph scopes and supervise per-workspace workers. |
| cmd/gatewayd/codegraph_worker.go | Hidden child-process entrypoint over inherited private configuration. |
| internal/runtime/config/service*.go | systemd-user lifecycle primitive or exact manual-start diagnostic. |
| internal/runtime/config/setup_journal.go | Durable setup stages and connector backup references only. |
| internal/runtime/config/identity_suggestion.go | Read-only Git suggestions/attestation request. |
| internal/runtime/config/connector | Full transactional adapter contract and Codex/Claude implementations. |
| cmd/wormhole/setup.go | Human interaction, setup orchestration, and Gateway RPC calls. |

---

### Task 0: Persist local identities, keys, agents, and connection sessions

**Files:**

- Create: internal/runtime/localidentity/store.go
- Create: internal/runtime/localidentity/files.go
- Create: internal/runtime/localidentity/keys.go
- Create: internal/runtime/localidentity/store_test.go
- Create: internal/runtime/localapi/actor.go
- Create: internal/runtime/localapi/actor_test.go
- Modify: internal/runtime/localapi/mcp.go
- Modify: internal/runtime/localapi/mcp_test.go
- Modify: docs/implementation-rules.md

**Produces:**

~~~go
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

type LocalActorResolver struct { /* localidentity store plus UTC clock */ }
func NewLocalActorResolver(*localidentity.Store, string) (*LocalActorResolver, error)
func (r *LocalActorResolver) SelectLocalIdentity(context.Context, string) error
func (r *LocalActorResolver) ResolveCLI(context.Context) (types.ActorEnvelope, error)
func (r *LocalActorResolver) OpenMCP(context.Context, MCPClientInfo) (string, error)
func (r *LocalActorResolver) ResolveMCP(context.Context, string) (types.ActorEnvelope, error)
~~~

The root is dataHome/wormhole/identities, mode 0700. Profiles, the selected-human pointer, agents, and sessions are canonical JSON files written by temp-file/fsync/rename/fsync-dir with mode 0600. Each human has identity.ed25519.public and identity.ed25519.private outside every repository; both local copies are 0600, the private file is PKCS#8 PEM, and only Sign reads it. CreateHuman uses crypto/ed25519 and crypto/rand. Human, agent, and session IDs are canonical UUIDs generated from crypto/rand. EnsureAgent is idempotent and unique by accountable-human UUID plus normalized harness name; a version change creates a new session, not a new durable agent.

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
~~~

Run: go test ./internal/runtime/localidentity ./internal/runtime/localapi -run 'Test(LocalIdentity|MCPActor|CLIActor|NewActorIssuance|IdentityKey)' -count=1

Expected: FAIL because the store and resolver are absent.

- [ ] **Step 2: Implement the file store and resolver, then run GREEN.**

Run: go test ./internal/runtime/localidentity ./internal/runtime/localapi -run 'Test(LocalIdentity|MCPActor|CLIActor|NewActorIssuance|IdentityKey)' -count=1

Expected: PASS across close/reopen, mode, sign/verify, human+harness isolation, initialize-forgery, local-assurance, and no-secret assertions.

- [ ] **Step 3: Update the module map and commit.**

Add internal/runtime/localidentity as internal/types+stdlib-only and state that only localapi constructs ActorEnvelope values from it.

~~~bash
git add internal/runtime/localidentity internal/runtime/localapi/actor.go internal/runtime/localapi/actor_test.go internal/runtime/localapi/mcp.go internal/runtime/localapi/mcp_test.go docs/implementation-rules.md
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

These are the complete supervisor-facing interfaces: every method takes the exact resolved binding, and implementations may not accept project/workspace strings as an alternate path. FabricRouter has a localOnlyFabricRouter and, after Slice D, a multi-Fabric router; local-only methods return ErrFabricUnavailable. CodeGraphProvider has a disabledCodeGraphProvider and a manager adapter whose sole conversion is codegraphmanager.ScopeFromBinding; disabled methods return ErrCodeGraphUnavailable. codegraphmanager.Status is the only exported Code Graph lifecycle/status type. NewSupervisor receives non-nil providers in all modes, so no nil-provider branch or unscoped fallback exists.

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
- Create: internal/runtime/localstore/migrations/000002_invalidate_legacy_codegraph.sql
- Create: internal/runtime/localstore/codegraph_invalidation.go
- Create: internal/runtime/localstore/codegraph_invalidation_test.go
- Modify: internal/runtime/localstore/migrations.go
- Modify: internal/runtime/localstore/migrations_test.go
- Modify: internal/runtime/codegraph/config/config.go
- Modify: internal/runtime/codegraph/store/store.go
- Modify: internal/runtime/codegraph/store/schema_test.go

**Consumes:** Slice-A's single `gateway_schema_migrations` ledger at `GatewaySchemaVersion = 1`; stdlib strings only for graph configuration. Task 3 introduces the conversion in the manager package before Task 4 builds the full manager; config never owns it.

**Produces:** the same `internal/runtime/localstore` migration constant advanced to `GatewaySchemaVersion = 2` after `000002_invalidate_legacy_codegraph.sql`, plus:

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

Gateway migration 000002 uses the existing gateway_schema_migrations ledger, advances the sole localstore `GatewaySchemaVersion` constant from 1 to 2, and uses exact DDL:

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
- codegraph_lexical_docs stores node_id, stable_id, field lengths, and canonical tokens; no source body or function body.

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
func TestGatewayMigration2InvalidatesLegacyGraphIdempotently(t *testing.T) {
    store := legacyGraphControlStore(t)
    first, err := store.InvalidateLegacyCodeGraph(t.Context())
    if err != nil { t.Fatal(err) }
    second, err := store.InvalidateLegacyCodeGraph(t.Context())
    if err != nil || !reflect.DeepEqual(first,second) { t.Fatalf("first=%+v second=%+v err=%v",first,second,err) }
    assertGatewayMigrationVersion(t,store,2)
    assertLegacyGraphEvidenceIntact(t,store)
}
func TestSchemaV3ContainsWorkspaceAndFingerprints(t *testing.T) {
    db := openGraphDB(t, validScope(t))
    assertColumns(t,db,"codegraph_revisions","project_id","workspace_id","revision_id","source_fingerprint","analysis_fingerprint","dirty_tracked","graph_schema_version","adapter_version","toolchain_identity")
}
~~~

- [ ] **Step 2: Run RED tests.**

Run: go test ./internal/runtime/localstore ./internal/runtime/codegraph/config ./internal/runtime/codegraph/store ./internal/runtime/codegraph/manager -run 'ScopeFromBinding|DerivativePath|SchemaV3|GatewayMigration2|LegacyProjectGraph' -count=1

Expected: FAIL because scope conversion and schema v3 are absent.

- [ ] **Step 3: Implement scoped database and legacy invalidation (GREEN).**

manager.ScopeFromBinding first calls binding.Validate, then copies its fields into the string-only config.Scope. DerivativePath requires a canonical UUID string, creates dataHome/wormhole/codegraph with 0700, and returns workspace-UUID.db beneath that exact directory. Open creates an owner-only file-backed SQLite DB and retains Scope. All SQL predicates and insert keys use scope.ProjectID, scope.WorkspaceID, and revision_id. The localstore loader still embeds the same numbered one-way files and now requires and applies the contiguous sequence `000001` through `000002`; no second ledger or graph-specific migration constant is introduced.

The supervisor translates localstore invalidation records into rebuild-required flags without mapping ambiguous legacy rows to a checkout. A new workspace graph starts disabled/rebuild-required. Restart repeats safely without deleting diagnostic evidence.

- [ ] **Step 4: Run GREEN tests.**

Run: go test ./internal/runtime/localstore ./internal/runtime/codegraph/config ./internal/runtime/codegraph/store ./internal/runtime/codegraph/manager -run 'Scope|Workspace|SchemaV3|Legacy|GatewayMigration|Restart|CrossProject|ControlDB' -count=1

Expected: PASS.

- [ ] **Step 5: Commit.**

~~~bash
git add internal/runtime/localstore/migrations/000002_invalidate_legacy_codegraph.sql internal/runtime/localstore/codegraph_invalidation.go internal/runtime/localstore/codegraph_invalidation_test.go internal/runtime/localstore/migrations.go internal/runtime/localstore/migrations_test.go internal/runtime/codegraph/config/scope.go internal/runtime/codegraph/config/scope_test.go internal/runtime/codegraph/config/config.go internal/runtime/codegraph/manager/scope.go internal/runtime/codegraph/manager/scope_test.go internal/runtime/codegraph/store/path.go internal/runtime/codegraph/store/path_test.go internal/runtime/codegraph/store/migration.go internal/runtime/codegraph/store/migration_test.go internal/runtime/codegraph/store/store.go internal/runtime/codegraph/store/schema_test.go
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
- Create: cmd/gatewayd/codegraph_worker.go
- Create: cmd/gatewayd/codegraph_worker_test.go
- Modify: cmd/gatewayd/main.go
- Modify: internal/runtime/localapi/localapi.go
- Modify: docs/implementation-rules.md

**Consumes:** Task 3 Scope/store and existing index/query services.

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
type Manager struct { /* process table keyed by WorkspaceID */ }
func NewManager(runtimeRoot, dataHome, gatewayExecutable string) (*Manager, error)
func (m *Manager) Status(context.Context, codegraphconfig.Scope) (Status, error)
func (m *Manager) Query(context.Context, codegraphconfig.Scope, query.Request) (query.Result, error)
func (m *Manager) Rebuild(context.Context, codegraphconfig.Scope) (Status, error)
func (m *Manager) Disable(context.Context, codegraphconfig.Scope) error
func (m *Manager) Recover(context.Context, codegraphconfig.Scope) error
~~~

These declarations live in package codegraphmanager. Worker protocol structs are private wire records converted by Manager and may not define another exported Status. localapi status/rebuild responses are projections of codegraphmanager.Status without inventing a lifecycle type.

Each workspace uses dataHome/wormhole/codegraph/workspace-ID.db and runtimeRoot/wormhole/codegraph/workspace-ID.sock. Manager starts a child on first status/query/rebuild, passes scope over an inherited private descriptor, and communicates over the owner-only socket. The hidden child entrypoint activates only when the inherited descriptor and internal marker both validate; ordinary gatewayd still accepts no arguments.

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

Run: go test ./internal/runtime/codegraph/manager ./internal/runtime/codegraph/worker ./cmd/gatewayd -run 'Worker|Manager|Status|Crash|ReadOnly' -count=1

Expected: FAIL because worker package and child entrypoint are absent.

- [ ] **Step 3: Implement process, filesystem, and credential isolation (GREEN).**

Child cwd is a private temporary directory, never the checkout. It receives only the stdlib-only Scope, graph DB/socket paths, byte limits, and build configuration. Environment is an allowlist containing PATH plus GOOS/GOARCH and private HOME, GOCACHE, GOPATH, GOMODCACHE; force GOENV=off, GOPROXY=off, GOSUMDB=off, GOVCS=*:off, GOTOOLCHAIN=local. Remove HTTP_PROXY, HTTPS_PROXY, ALL_PROXY, NO_PROXY, SSH_AUTH_SOCK, Git credential variables, Wormhole tokens, Fabric URLs, Passport data, and credential paths. Source reads use existing no-follow/hash validation rooted at Scope.CheckoutPath; all writes target private cache/DB/socket directories.

Update docs/implementation-rules.md with explicit rows: codegraph/config -> stdlib only; codegraph/worker -> config/store/index/query plus stdlib, never internal/types; codegraph/manager -> internal/types, config, worker, query, and stdlib. manager.ScopeFromBinding is the sole types.WorkspaceBinding conversion.

A crash marks only that workspace worker unavailable, preserves the last published DB, and permits an on-demand restart. Disable stops only the selected child, closes its socket, removes its DB/socket/enablement, and leaves another workspace intact.

- [ ] **Step 4: Run GREEN tests.**

Run: go test ./internal/runtime/codegraph/manager ./internal/runtime/codegraph/worker ./cmd/gatewayd -run 'Worker|Manager|Status|OnDemand|Crash|Restart|ReadOnly|Sanitized|NoFabricCredential|Disable' -count=1

Expected: PASS.

- [ ] **Step 5: Commit.**

~~~bash
git add internal/runtime/codegraph/worker/protocol.go internal/runtime/codegraph/worker/child.go internal/runtime/codegraph/worker/child_test.go internal/runtime/codegraph/worker/environment.go internal/runtime/codegraph/worker/environment_test.go internal/runtime/codegraph/manager/scope.go internal/runtime/codegraph/manager/scope_test.go internal/runtime/codegraph/manager/manager.go internal/runtime/codegraph/manager/manager_test.go cmd/gatewayd/codegraph_worker.go cmd/gatewayd/codegraph_worker_test.go cmd/gatewayd/main.go internal/runtime/localapi/localapi.go docs/implementation-rules.md
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

- Create: internal/runtime/codegraph/query/lexical.go
- Create: internal/runtime/codegraph/query/lexical_test.go
- Create: internal/runtime/codegraph/query/ranking_acceptance_test.go
- Create: internal/runtime/codegraph/query/scale_acceptance_test.go
- Create: internal/runtime/codegraph/query/testdata/heldout/documents.json
- Create: internal/runtime/codegraph/query/testdata/heldout/queries.json
- Modify: internal/runtime/codegraph/store/store.go
- Modify: internal/runtime/codegraph/query/query.go
- Modify: internal/runtime/codegraph/query/benchmark_test.go

**Algorithm contract:**

- LexicalAlgorithmVersion = lexical-bm25-v1.
- Tokenise UTF-8 by letter/digit class, lower-to-upper transitions, acronym boundary before the final upper followed by lower, underscore/path/punctuation, and letter/digit transitions; lower-case with unicode.ToLower; discard empty tokens.
- Fields and weights: qualified_name 8, symbol_name 6, signature_and_type 3, package_path 2, file_path 2, documentation 1.
- BM25 constants: k1=1.2 and b=0.75. Per-field score is weight multiplied by IDF multiplied by tf*(k1+1)/(tf+k1*(1-b+b*docLength/averageLength)).
- IDF is ln(1+(N-df+0.5)/(df+0.5)).
- Convert total score to fixedScore = floor(score*1,000,000+0.5), stored/compared as int64.
- Sort: exact qualified tier first, exact unqualified symbol tier second, BM25 tier third; then fixedScore descending; then stable node ID bytewise ascending.
- Compiler edges may rerank/expand only after lexical anchors; their fixed bonus table is versioned with LexicalAlgorithmVersion.

- [ ] **Step 1: Write failing tokenizer/ranking/corpus tests (RED).**

~~~go
func TestTokenizeLexicalContract(t *testing.T) {
    got := TokenizeLexical("HTTPServer_v2/load-path")
    want := []string{"http","server","v","2","load","path"}
    if !slices.Equal(got,want) { t.Fatalf("got=%v want=%v",got,want) }
}
func TestLexicalTieBreakIsStableID(t *testing.T) {
    got := RankLexical([]string{"worker"},[]LexicalDocument{{StableID:"z",SymbolName:"Worker"},{StableID:"a",SymbolName:"Worker"}})
    if got[0].StableID != "a" || got[0].FixedScore != got[1].FixedScore { t.Fatalf("got=%+v",got) }
}
func TestHeldOutCorpusTenRunsAreByteIdentical(t *testing.T) {
    corpus := loadHeldOutCorpus(t)
    first := marshalResults(t,runCorpus(t,corpus))
    for run := 1; run < 10; run++ {
        if got := marshalResults(t,runCorpus(t,corpus)); !bytes.Equal(got,first) { t.Fatalf("run %d differs",run) }
    }
}
~~~

- [ ] **Step 2: Run RED tests.**

Run: go test ./internal/runtime/codegraph/query -run 'Tokenize|BM25|TieBreak|HeldOut|Disclosure' -count=1

Expected: FAIL because lexical-bm25-v1 is absent.

- [ ] **Step 3: Implement fixed retrieval and progressive disclosure (GREEN).**

Persist canonical field tokens/lengths per revision; compute document frequency only inside the pinned revision. Exact and BM25 ordering follows the algorithm contract. Responses expose in order: repository/package map and reasons; at most five candidate files and one-hop relationships; deterministic outlines; then hash-validated source for selected paths only. The default discovery response before correct path is at most five files and 8192 source bytes.

- [ ] **Step 4: Run correctness, recall, disclosure, and scale gates.**

Run: go test ./internal/runtime/codegraph/query -run 'ExactQualified|UnqualifiedTopThree|HeldOut|Recall|Disclosure|TenRuns' -count=1

Expected: exact qualified top-1; unqualified top-3; primary-file recall@5 at least 0.90; expected-file recall@10 at least 0.90; structural-path recall after bounded expansion at least 0.90; ten runs byte-identical; no more than five files/8192 bytes before correct path.

Run: go test ./internal/runtime/codegraph/query -run TestScaleAcceptance250K -count=1 -timeout=10m

Expected: fixture contains 250000 symbols and 2000000 edges; twenty warm queries have p95 at most 300ms and measured heap growth at most 128MiB.

- [ ] **Step 5: Commit.**

~~~bash
git add internal/runtime/codegraph/query/lexical.go internal/runtime/codegraph/query/lexical_test.go internal/runtime/codegraph/query/ranking_acceptance_test.go internal/runtime/codegraph/query/scale_acceptance_test.go internal/runtime/codegraph/query/testdata/heldout/documents.json internal/runtime/codegraph/query/testdata/heldout/queries.json internal/runtime/codegraph/store/store.go internal/runtime/codegraph/query/query.go internal/runtime/codegraph/query/benchmark_test.go
git commit -m "feat(codegraph): add deterministic bm25 retrieval"
~~~

### Task 7: Add the conservative Gateway service primitive

**Files:**

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

- [ ] **Step 1: Write failing service tests (RED).**

~~~go
func TestUnavailableManagerReturnsExactManualDiagnostic(t *testing.T) {
    _, err := newUnavailableService().Inspect(t.Context())
    if !errors.Is(err,ErrServiceManagerUnavailable) || err.Error() != "gatewayd service manager unavailable; start gatewayd manually" { t.Fatalf("err=%v",err) }
}
func TestSystemdUserInstallIsIdempotentAndVerifiesSocket(t *testing.T) {
    runner := systemdFakeRunner(t)
    service := NewGatewayService(runner)
    spec := GatewayServiceSpec{Executable:"/opt/wormhole/gatewayd",SocketPath:filepath.Join(t.TempDir(),"wormholed.sock")}
    if err := service.Install(t.Context(),spec); err != nil { t.Fatal(err) }
    if err := service.Install(t.Context(),spec); err != nil { t.Fatal(err) }
    if runner.Count("enable","--now") != 1 { t.Fatalf("calls=%v",runner.Calls()) }
}
~~~

- [ ] **Step 2: Run RED tests.**

Run: go test ./internal/runtime/config -run 'Manager|Systemd|Service' -count=1

Expected: FAIL because service primitives are absent.

- [ ] **Step 3: Implement systemd-user only (GREEN).**

On Linux, require systemctl and a usable systemctl --user show-environment. Inspect is-enabled/is-active; Install validates absolute regular gatewayd executable, writes an owner-private user unit atomically, daemon-reload, and enable --now; Start starts only an installed unit; Verify dials the exact user socket and performs Gateway readiness RPC. Any unsupported OS or absent/unusable manager returns ErrServiceManagerUnavailable before mutation.

- [ ] **Step 4: Run GREEN tests.**

Run: go test ./internal/runtime/config -run 'Manager|Systemd|Service|ManualDiagnostic' -count=1

Expected: PASS.

- [ ] **Step 5: Commit.**

~~~bash
git add internal/runtime/config/service.go internal/runtime/config/service_linux.go internal/runtime/config/service_unsupported.go internal/runtime/config/service_test.go
git commit -m "feat(setup): manage systemd user gateway"
~~~

### Task 8: Add a durable setup journal primitive

**Files:**

- Create: internal/runtime/config/setup_journal.go
- Create: internal/runtime/config/setup_journal_test.go
- Modify: internal/runtime/config/paths.go
- Modify: internal/runtime/config/config_test.go

**Produces:**

~~~go
type SetupStage string
type SetupJournal struct {
    ID string
    RepositoryRoot string
    WorkspaceID types.WorkspaceID
    Completed []SetupStage
    ConnectorBackupRefs []string
    LastError string
}
var ErrJournalCredentialMaterial = errors.New("setup journal: credential material forbidden")
func OpenSetupJournal(path string) (*SetupJournalStore, error)
func (s *SetupJournalStore) Begin(context.Context, string) (SetupJournal, error)
func (s *SetupJournalStore) MarkCompleted(context.Context, string, SetupStage) error
func (s *SetupJournalStore) RecordConnectorBackup(context.Context, string, string) error
func (s *SetupJournalStore) Resumable(context.Context, string) (SetupJournal, error)
~~~

- [ ] **Step 1: Write failing durability/no-secret tests (RED).**

~~~go
func TestSetupJournalResumesCompletedStagesAfterRestart(t *testing.T) {
    path := filepath.Join(t.TempDir(),"setup-journal.json")
    store, _ := OpenSetupJournal(path)
    journal, _ := store.Begin(t.Context(),"/repo")
    _ = store.MarkCompleted(t.Context(),journal.ID,SetupStage("workspace_imported"))
    reopened, _ := OpenSetupJournal(path)
    got, _ := reopened.Resumable(t.Context(),"/repo")
    if !slices.Contains(got.Completed,SetupStage("workspace_imported")) { t.Fatalf("got=%+v",got) }
}
func TestSetupJournalRejectsRawCredential(t *testing.T) {
    store, _ := OpenSetupJournal(filepath.Join(t.TempDir(),"journal.json"))
    const journalID = "99999999-9999-4999-8999-999999999999"
    seedSetupJournal(t,store,journalID)
    err := store.RecordConnectorBackup(t.Context(),journalID,"{\"token\":\"secret\"}")
    if !errors.Is(err,ErrJournalCredentialMaterial) { t.Fatalf("err=%v",err) }
}
~~~

- [ ] **Step 2: Run RED tests.**

Run: go test ./internal/runtime/config -run 'SetupJournal' -count=1

Expected: FAIL because journal store is absent.

- [ ] **Step 3: Implement atomic 0600 JSON journal (GREEN).**

Use XDG data path wormhole/setup-journals/journal-ID.json, strict typed JSON, temp-file fsync/rename/directory-sync, 0700 parent, 0600 file, and per-journal locking. Persist stage names, stable workspace ID after registration, backup file references, and redacted errors only. Reject token/password/private-key/bearer-shaped fields. Record completion after a stage verifies its effect; retries skip verified completed stages.

- [ ] **Step 4: Run GREEN tests.**

Run: go test ./internal/runtime/config -run 'SetupJournal|Permission|Atomic|Restart|NoSecret' -count=1

Expected: PASS.

- [ ] **Step 5: Commit.**

~~~bash
git add internal/runtime/config/setup_journal.go internal/runtime/config/setup_journal_test.go internal/runtime/config/paths.go internal/runtime/config/config_test.go
git commit -m "feat(setup): persist resumable setup journal"
~~~

### Task 9: Add read-only Git identity suggestions

**Files:**

- Create: internal/runtime/config/identity_suggestion.go
- Create: internal/runtime/config/identity_suggestion_test.go

**Produces:**

~~~go
type IdentitySuggestion struct { Name, Email, SigningKey, Source string }
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
~~~

- [ ] **Step 2: Run RED tests.**

Run: go test ./internal/runtime/config -run 'GitIdentity|SigningAttestation' -count=1

Expected: FAIL because suggestion functions are absent.

- [ ] **Step 3: Implement read-only suggestions (GREEN).**

Run only git -C root config --get user.name, user.email, user.signingkey, and gpg.format. Return self-declared suggestions; do not write Git config or publish email. RequestSigningAttestation delegates signing to git/gpg/ssh-agent based on existing config and accepts signature bytes from stdout; it never reads a key file or credential helper.

- [ ] **Step 4: Run GREEN tests.**

Run: go test ./internal/runtime/config -run 'GitIdentity|SigningAttestation|NoCredentialHelper|NoWrite' -count=1

Expected: PASS.

- [ ] **Step 5: Commit.**

~~~bash
git add internal/runtime/config/identity_suggestion.go internal/runtime/config/identity_suggestion_test.go
git commit -m "feat(setup): suggest local git identity"
~~~

### Task 10: Implement full transactional Codex and Claude adapters

**Files:**

- Create: internal/runtime/config/connector/connector.go
- Create: internal/runtime/config/connector/transaction.go
- Create: internal/runtime/config/connector/codex.go
- Create: internal/runtime/config/connector/claude.go
- Create: internal/runtime/config/connector/connector_test.go
- Create: internal/runtime/config/connector/testdata/codex/get-stdio.json
- Create: internal/runtime/config/connector/testdata/codex/get-http.json
- Create: internal/runtime/config/connector/testdata/codex/get-absent.stderr
- Create: internal/runtime/config/connector/testdata/codex/list.json
- Create: internal/runtime/config/connector/testdata/claude/2.1.220/get-stdio.txt
- Create: internal/runtime/config/connector/testdata/claude/2.1.220/get-http.txt
- Create: internal/runtime/config/connector/testdata/claude/2.1.220/get-absent.txt

**Produces:**

~~~go
type EntryState string
const (EntryAbsent EntryState = "absent"; EntryPresent EntryState = "present")
type Transport string
const (TransportStdio Transport = "stdio"; TransportHTTP Transport = "http")
type ConnectorEntry struct {
    State EntryState
    Scope string
    Transport Transport
    Command string
    Args []string
    Env []string
    URL string
    Headers []string
    BearerTokenEnvVar string
}
type ChangePlan struct { Action string; Prior ConnectorEntry; Desired ConnectorEntry; Mutates bool }
type Adapter interface {
    Discover(context.Context) (Availability, error)
    Inspect(context.Context) (ConnectorEntry, error)
    Plan(context.Context, ConnectorEntry, ConnectorEntry) (ChangePlan, error)
    Apply(context.Context, ChangePlan) error
    Verify(context.Context, ConnectorEntry) error
    Rollback(context.Context, ChangePlan) error
    Remove(context.Context, ConnectorEntry) error
}
func ApplyTransactional(context.Context, Adapter, ConnectorEntry, BackupStore) error
~~~

Prior absent is a first-class state. Typed stdio/http entries must reconstruct command, args/env or URL/headers/token-env/scope exactly. Inspect returns ErrUnsupportedPriorEntry before mutation for unknown transport, hidden secret, unknown Claude output version, ambiguous scope, or any value that cannot round-trip. Rollback restores typed prior; prior absent rollback removes the newly added entry.

- [ ] **Step 1: Write failing lifecycle/round-trip tests (RED).**

~~~go
func TestCodexLifecycleUsesSupportedJSONAndExactAdd(t *testing.T) {
    runner := connectorRunner(t)
    adapter := NewCodexAdapter(runner)
    prior, err := adapter.Inspect(t.Context())
    if err != nil { t.Fatal(err) }
    plan, _ := adapter.Plan(t.Context(),prior,desiredStdio("/abs/wormhole"))
    if err := adapter.Apply(t.Context(),plan); err != nil { t.Fatal(err) }
    assertCallsContain(t,runner,[]string{"codex","mcp","get","wormhole","--json"})
    assertCallsContain(t,runner,[]string{"codex","mcp","list","--json"})
    assertCallsContain(t,runner,[]string{"codex","mcp","add","wormhole","--","/abs/wormhole","mcp"})
}
func TestRollbackRestoresPriorAbsentAndTypedHTTP(t *testing.T) {
    for _, prior := range []ConnectorEntry{{State:EntryAbsent},typedHTTPFixture(t)} {
        runner := failingVerifyRunner(t,prior)
        err := ApplyTransactional(t.Context(),runner.Adapter(),desiredStdio("/abs/wormhole"),privateBackupStore(t))
        if err == nil { t.Fatal("verification failure hidden") }
        if got := runner.FinalEntry(); !reflect.DeepEqual(got,prior) { t.Fatalf("got=%+v prior=%+v",got,prior) }
    }
}
func TestUnsupportedPriorFailsBeforeMutation(t *testing.T) {
    runner := unsupportedPriorRunner(t)
    err := ApplyTransactional(t.Context(),runner.Adapter(),desiredStdio("/abs/wormhole"),privateBackupStore(t))
    if !errors.Is(err,ErrUnsupportedPriorEntry) || runner.MutationCount() != 0 { t.Fatalf("mutations=%d err=%v",runner.MutationCount(),err) }
}
func TestGetAbsentRequiresRecognizedDiagnosticAndExitCode(t *testing.T) {
    for _, fixture := range []struct {
        name string
        adapter Adapter
        absentDiagnostic string
    }{
        {"codex", codexAdapterWithGetResult(t, 1, "testdata/codex/get-absent.stderr"), "testdata/codex/get-absent.stderr"},
        {"claude-2.1.220", claudeAdapterWithGetResult(t, 1, "testdata/claude/2.1.220/get-absent.txt"), "testdata/claude/2.1.220/get-absent.txt"},
    } {
        t.Run(fixture.name, func(t *testing.T) {
            got, err := fixture.adapter.Inspect(t.Context())
            if err != nil || got.State != EntryAbsent { t.Fatalf("got=%+v err=%v", got, err) }
            for _, result := range []commandResult{
                {ExitCode: 2, Stderr: readFixture(t, fixture.absentDiagnostic)},
                {ExitCode: 1, Stderr: "permission denied"},
            } {
                adapter := adapterWithGetResult(t, fixture.name, result)
                if _, err := adapter.Inspect(t.Context()); err == nil { t.Fatalf("accepted result=%+v", result) }
            }
        })
    }
}
~~~

- [ ] **Step 2: Run RED tests.**

Run: go test ./internal/runtime/config/connector -run 'Codex|Claude|Rollback|Absent|HTTP|UnsupportedPrior|RoundTrip' -count=1

Expected: FAIL because connector package is absent.

- [ ] **Step 3: Implement Codex and Claude exact command adapters (GREEN).**

Codex: Discover resolves codex and version. Inspect runs codex mcp get wormhole --json first. Exit zero parses a present entry; exit one is EntryAbsent only when stderr matches the checked-in get-absent.stderr not-found grammar; every other nonzero exit or malformed/changed diagnostic is an inspection error and forbids mutation. For a present result, also run codex mcp list --json and cross-check presence. Desired apply is exact stdio add. Remove uses codex mcp remove wormhole. Prior HTTP reconstruction uses codex mcp add wormhole --url URL plus represented bearer-token-env/oauth fields only.

Claude: Discover runs claude --version. Inspect runs human-readable claude mcp get wormhole; there is no inspect command and no JSON flag. Select a versioned parser using the tested version fixture. Exit zero parses a present entry; exit one is EntryAbsent only when the selected version's get-absent fixture grammar matches; every other nonzero exit or malformed/unknown not-found diagnostic is an inspection error and forbids mutation. Desired apply is exactly claude mcp add --scope user wormhole -- /abs/wormhole mcp. Remove is exactly claude mcp remove --scope user wormhole. Prior stdio/http is reconstructed with its parsed scope/transport/env/header fields. Unknown output/version or a field that cannot round-trip returns ErrUnsupportedPriorEntry before Remove.

Transaction sequence is Discover, Inspect, Plan, durable 0600 typed backup, Apply, Verify, then success; any Apply/Verify error calls Rollback. Plan with Mutates=false performs no backup/mutation. Never log entry/env/header content.

- [ ] **Step 4: Run GREEN tests without touching real harness config.**

Run: go test ./internal/runtime/config/connector -run 'Codex|Claude|Rollback|Absent|HTTP|UnsupportedPrior|RoundTrip|ExactArgv' -count=1

Expected: PASS using fake runners and versioned fixtures only.

Run: codex mcp get --help && codex mcp list --help && codex mcp add --help && codex mcp remove --help && claude mcp get --help && claude mcp add --help && claude mcp remove --help

Expected: capability check exits zero; it does not add/remove any connector.

- [ ] **Step 5: Commit.**

~~~bash
git add internal/runtime/config/connector/connector.go internal/runtime/config/connector/transaction.go internal/runtime/config/connector/codex.go internal/runtime/config/connector/claude.go internal/runtime/config/connector/connector_test.go internal/runtime/config/connector/testdata
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

**Consumes:** Tasks 7-10 primitives; internal/types/projectstate codec; internal/types/identity; Gateway RPCs backed by projectstate and worker manager. Runtime/config contains no setup orchestration and makes no Gateway RPC.

**Produces:**

~~~go
type setupOptions struct { CodeGraph string; NonInteractive bool }
type setupPlan struct { Root string; Stages []config.SetupStage }
func runSetup(context.Context, []string, io.Reader, io.Writer, io.Writer) int
type gatewayClient struct { SocketPath string }
func (c gatewayClient) RegisterWorkspace(context.Context, string) (projectstate.RegisterWorkspaceResult, error)
func (c gatewayClient) CreateLocalIdentity(context.Context, string, string) (localidentity.HumanProfile, error)
func (c gatewayClient) SelectLocalIdentity(context.Context, string) error
func (c gatewayClient) RebuildCodeGraph(context.Context) error
func (c gatewayClient) VerifyWorkspace(context.Context) error
func runConnector(context.Context, []string, io.Reader, io.Writer, io.Writer, connectorCommandDeps) int
~~~

The exact standalone forms are wormhole connector list, wormhole connector install <codex|claude> [--yes], and wormhole connector remove <codex|claude> [--yes]. list calls Discover/Inspect and prints only adapter name, availability, state, scope, and transport. install plans, displays, confirms unless --yes, and calls ApplyTransactional with the private backup store. remove uses an equivalent RemoveTransactional sequence: Discover, Inspect, durable typed backup, Remove, verify EntryAbsent, and restore the exact prior entry on failure. No CLI output includes command environment, headers, bearer fields, private paths, or backup contents.

Fabric profile/login/attach and wormhole project/fabric commands are Slice D-owned. Setup exposes an optional Fabric stage only through D's Gateway RPC when that capability exists; otherwise it records local-only and succeeds. It never reintroduces legacy credential-profile enrollment.

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
            if fixture.ReplayedAny(completed) { t.Fatalf("replayed=%v completed=%v",fixture.Calls(),completed) }
        })
    }
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
    if !reflect.DeepEqual(deps.Actions(),[]string{"discover","inspect","plan","backup","apply","verify","discover","inspect","backup","remove","verify-absent"}) { t.Fatalf("actions=%v",deps.Actions()) }
    if deps.RealConfigTouched() || deps.OutputContainsSecret() { t.Fatal("connector command escaped fake/redacted boundary") }
}
~~~

- [ ] **Step 2: Run RED tests.**

Run: go test ./cmd/wormhole -run 'Setup|GatewayClient|NonInteractive|NeverExecutes' -count=1

Expected: FAIL because setup.go and Gateway client are absent.

- [ ] **Step 3: Implement one-plan setup and RPC orchestration (GREEN).**

Exact stages: discover nearest .wormhole; decode/validate using internal/types/projectstate; inspect/install/start/verify service; Gateway RegisterWorkspace and consume result.Binding; create/select a Task-0 local human profile through Gateway; refresh/import base; connector Discover/Inspect/Plan display; one user confirmation; connector transactional apply; optional Slice D Fabric RPC or local-only; explicit Code Graph rebuild; Gateway final verification; journal complete. SelectLocalIdentity sends only the human UUID and updates the owner-only localidentity store; it never accepts an ActorEnvelope from the CLI.

Interactive prints one plan and asks once. Noninteractive requires --code-graph=on|off unless a preference exists. Every completed stage is journalled after verification. Connector failure leaves workspace imported; Fabric unavailable leaves local-only; graph failure leaves workspace usable. Missing dependencies return the Task 5 offline error and explain that separate dependency-fetch consent is required; setup does not silently download.

- [ ] **Step 4: Run GREEN tests.**

Run: go test ./cmd/wormhole -run 'Setup|GatewayClient|JournalResume|LocalOnly|ConnectorFailure|CodeGraphFailure|NeverExecutes|NonInteractive' -count=1

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
func TestConnectorFailureRestoresExactPriorOrAbsence(t *testing.T) {
    for _, prior := range []connector.ConnectorEntry{{State:connector.EntryAbsent},typedStdioPrior(t),typedHTTPPrior(t)} {
        fixture := setupConnectorFailureFixture(t,prior)
        if code := fixture.Run(); code == 0 { t.Fatal("verification failure succeeded") }
        if got := fixture.Entry(); !reflect.DeepEqual(got,prior) { t.Fatalf("got=%+v prior=%+v",got,prior) }
        if !fixture.WorkspaceImported() { t.Fatal("workspace rolled back") }
    }
}
~~~

- [ ] **Step 2: Run RED tests.**

Run: go test ./cmd/wormhole ./cmd/gatewayd ./internal/runtime/localapi -run 'RemovedCommands|SemanticallyEqual|OneGatewayIsolates|ConnectorFailure|Contract' -count=1

Expected: FAIL until cutover/integration is complete.

- [ ] **Step 3: Implement contract cutover and safe smoke tests (GREEN).**

Remove init/join/connect from dispatcher, runInit/runJoin/runConnect callers and helpers, usage, tests, guidance, docs, and JSON contract with no aliases. Remove the join-shaped wormhole.agent.register variant, agentJoinRegisterArgs, isJoinRegisterArgs, proxyRegister, and localJoinResult while preserving the unrelated MCP initialize handshake. Modify the Slice-A cmd/wormhole/workspace.go and workspace_test.go so status/diff/import/checkpoint/stash are top-level commands only; there is no wormhole workspace subcommand. Register final workspace/code-graph tools without cwd or machine-ID schema fields. CLI workspace and graph operations call Gateway from cwd and render the same semantic result as MCP. Dispatch connector list/install/remove to the tested connector.go implementation. Update docs only for tested B/C commands; mark project/fabric administration as Slice D-dependent.

All automated adapter tests use fake runners/config roots. Real-client smoke is limited to read-only version/help/get capability checks against an isolated temporary config when supported; no test adds/removes an entry in the actual user config. There is no real mutation smoke in make check.

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
- Task 3 follows Slice A shared binding freeze. Task 4 follows Task 3. Task 5 follows Tasks 3-4. Task 6 follows Task 5.
- Tasks 7, 8, 9, and 10 are independent after runtime/config command-runner conventions are frozen.
- Task 11 consumes Tasks 7-10 and Gateway RPCs from Tasks 1-6.
- Task 12 consumes every prior task. Slice D owns project/fabric CLI and authenticated attach/login; Slice E owns private human authentication.

## Self-review

- Shared ownership: the exact Slice-A types.WorkspaceBinding flows from registration/resolution/enumeration; types.RepositoryIdentity is reused, its digest remains a validated string, and only codegraphmanager.ScopeFromBinding copies it into the stdlib-only string Scope.
- Routing: the bridge overwrites forged private cwd before forwarding, Gateway strips it before schema validation, and every project/sync/graph/auth path records the resolved binding in forged/cross-workspace coverage.
- Supervisor seams: FabricRouter and CodeGraphProvider have exact binding-scoped methods, non-nil fail-closed implementations, and errors.Is coverage for their typed unavailable errors.
- Workers: separate process, DB, socket, private caches, offline/sanitized environment, read-only checkout, crash/restart/disable isolation.
- Graph: exact schema/status fingerprints, manifest ordering/restart/failed-publish tests, explicit BM25/tokenizer constants, held-out recall/disclosure/determinism/scale gates.
- Setup: service, journal, identity, connector, and cmd orchestration are separate reviewable tasks; runtime/config does not call Gateway.
- Connectors: complete seven-operation contract, absent/http/stdio round-trip, fail-before-mutation, exact Codex commands, versioned human-readable Claude get parser, recognized exit-one not-found fixtures only, exact add/remove commands, and read-only help smoke for both mutation subcommands.
- Verification: acceptance tests have executable assertions, normal tests never mutate real user connector state, the current rg -l cutover inventory is frozen, final add/rm staging lists every path explicitly, and the 80-percent gate is explicit.
