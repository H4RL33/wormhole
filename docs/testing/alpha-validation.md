# Automated alpha validation

The Stage 2 release gate is
`TestStage2LocalOnlyRealProcessAcceptance`. It is hermetic, local-only, and
requires neither PostgreSQL nor Fabric.

## Process topology

```text
real MCP client
      |
real `wormhole mcp` stdio bridge
      |
owner-private Unix socket
      |
real gatewayd process ---- schema-v6 SQLite + identity store
      |
clone A tracked candidate ---- ordinary Git commit/push ---- fresh clone B
```

The test builds the production `wormhole` and `gatewayd` binaries. It uses two
independent homes, XDG roots, sockets, databases, identity stores, workspaces,
and real Git clones. It does not build or start a coordination server, database
server, fake HTTP server, or in-process Gateway.

The fixture bootstraps each fresh daemon through the production private setup
RPCs: workspace registration, selected human identity, `local_only`
publication, base import, and verification. This exercises the same exact
confirmed-plan predicates and owner-private capability used by setup. The
hermetic gate does not install a host user service; service-manager installation is covered separately
by setup/systemd lifecycle tests. There is no production bypass flag or test
configuration seam.

After setup, all agent operations cross the real stdio/socket boundary. The
test proves:

- the real daemon advertises exactly the 17 descriptors below, each with a
  non-empty description;
- generated tool guidance contains exactly that live inventory and its bytes
  and content digest match the checked-in generated manifest;
- the selected human, durable accountable agent, harness/model provenance, and
  server-generated session come from machine-private identity state;
- a direct portable actor import, channel creation, and KB write compose into
  one candidate and attributed semantic diff;
- channel activity and local presence are operational/private and survive a
  same-machine daemon restart without entering the portable tree;
- checkpoint materialises portable state without changing Git HEAD, index, or
  remote ref;
- ordinary Git commit and push are the sole acceptance action;
- a second real clone with fresh private state reconstructs equal accepted
  channel/KB/actor state; and
- operational events, presence, workspace IDs, identities, sessions, overlay
  operations, materialisation journals, stashes, conflicts, receipts, legacy
  Channel/KB/Task/Git rows, and sync rows do not cross into the fresh clone.

## Exact Stage 2 inventory

Harnesses connect to Gateway, whose live Stage 2 registry is closed at these
17 tools.

### Gateway MCP (17 tools)

```text
wormhole.agent.list
wormhole.agent.presence
wormhole.agent.register
wormhole.channel.create
wormhole.channel.events
wormhole.channel.list
wormhole.channel.post
wormhole.channel.subscribe
wormhole.kb.get
wormhole.kb.list
wormhole.kb.write
wormhole.sync.status
wormhole.workspace.checkpoint
wormhole.workspace.diff
wormhole.workspace.import
wormhole.workspace.stash
wormhole.workspace.status
```

## Run the Stage 2 gate

From the repository root:

```bash
go test ./cmd/gatewayd \
  -run '^TestStage2LocalOnlyRealProcessAcceptance$' \
  -count=1 -v

go test ./cmd/wormhole \
  -run '^TestStage2' \
  -count=1
```

Then run the owning packages, contract stability script, race coverage, and
repository gates:

```bash
go test ./cmd/gatewayd ./cmd/wormhole -count=1
go test -race ./cmd/gatewayd \
  -run '^TestStage2LocalOnlyRealProcessAcceptance$' \
  -count=1
.github/scripts/check-contract-manifest.sh
go build ./...
go vet ./...
make check
make release-test
make release-rehearsal
git diff --check
git status --short
```

The process test uses only temporary directories and loopback Unix sockets. It
does not change the caller's checkout, print identity secrets or capabilities,
or contact the public network.

## Optional Fabric/PostgreSQL coverage

The optional Fabric binary retains a distinct authenticated HTTP MCP inventory
of exactly 20 tools. Fabric is not a direct harness endpoint and this coverage
is not a Stage 2 acceptance dependency.

### Fabric MCP (20 tools)

```text
wormhole.agent.enrol
wormhole.agent.whoami
wormhole.channel.create
wormhole.channel.list
wormhole.channel.post
wormhole.channel.subscribe
wormhole.git.link_commit
wormhole.git.request_review
wormhole.kb.get
wormhole.kb.get_links
wormhole.kb.search
wormhole.kb.write
wormhole.sync.bootstrap
wormhole.sync.conflict_report
wormhole.sync.incremental_pull
wormhole.sync.incremental_push
wormhole.task.assign
wormhole.task.create
wormhole.task.list
wormhole.task.update_status
```

Fabric and PostgreSQL remain mandatory repository-quality boundaries even
though they are not part of the local-only Stage 2 process topology. The former
combined Gateway/Fabric process tests were retired when Gateway removed remote
tools and network wiring. Their executable replacement map is:

| Retired combined assertion | Executable boundary replacements |
|---|---|
| stdio bridge, Unix socket, local Gateway writes, private attribution, restart, checkpoint and portable clone convergence | `TestStage2LocalOnlyRealProcessAcceptance`; `TestConfiguredPrivateRuntimeDerivesAttributionAndIsolatesChannelAndKBHandlers`; `TestStage2ConfiguredSyncStatusReportsQueueFreeLocalOnlyState` |
| Fabric HTTP MCP enrolment and Postgres pillar persistence | `TestM3_MCPSeededStateReflectedInDashboard`; `TestEnrolAgentTool_DurableReplayAndControlledReissue`; `TestEnrolAgentToolBootstrapFailureRollsBackAndRetryIsIdempotent` |
| Fabric bootstrap, incremental push/pull, replay safety and routed-owner fidelity | `TestBootstrapTool_ReturnsCompleteDeterministicSnapshot`; `TestAlphaAcceptanceIncrementalTaskEventAndGitPropagationIsReplaySafe`; `TestIncrementalPushTool_IdenticalReplaySucceedsWithoutDuplicateEffects`; `TestIncrementalPushTool_AppliesRoutedTaskOwner` |
| server snapshot application and cross-runtime convergence | `TestBootstrap_AppliesServerTasksAndKBToLocalStore`; `TestPullIncremental_AppliesServerUpdatesToLocalStore`; `TestSyncLoopPullsWithEmptyQueue`; `TestP7_MultiDaemonSync` |
| durable offline queue, restart and reconnect | `TestP7_SyncQueueDurability`; `TestEngineQueuePersistence`; `TestOfflineQueueSurvivalNetworkFailure`; `TestOfflineQueueReconnect`; `TestLocalDurableWrites_SuccessSurvivesRestartWithPendingQueue` |
| single-Gateway multi-workspace and project isolation | `TestCrossWorkspacePrivateContextResolvesSiblingExactly`; `TestConfiguredPrivateRuntimeDerivesAttributionAndIsolatesChannelAndKBHandlers`; `TestWorkspaceScopeMismatchIsRejected`; `TestSyncQueueCrossNamespaceRejection` |
| enrolment credential/bootstrap failure recovery and separated tool ownership | `TestEnrolmentResumesDurableAttemptAfterRestartAndReissuesOnce`; `TestEnrolmentBootstrapFailureRecoversWithoutReregistrationAndStartsIncrementalAfterCommit`; `TestStage2FinalPublicMCPInventory`; `TestAlphaContractMCPRegistry`; `TestFabricRegistryIncludesGatewayEnrolmentEndpoint` |
| Fabric token, tenant and vector isolation | `TestMCP_MultiTenantIsolation`; `TestAgentEnrolmentsRLSHidesProjectBWhileScopedToProjectA`; `TestRestrictedRoleRLSOperationMatrix`; `TestRestrictedRoleRejectsCrossProjectForeignReferences`; `TestRestrictedRoleKBVectorQueryCannotCrossProject` |

No row restores a removed Gateway tool or makes Fabric part of Stage 2. The
repository gate runs all of these tests with the database required, so a missing
or unmigrated Postgres instance fails instead of skipping:

```bash
export WORMHOLE_DATABASE_URL='postgres://wormhole:wormhole@127.0.0.1:5432/wormhole?sslmode=disable'
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./...
WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./...
make check
```

The semantic KB fixture separately asserts that vector ranking is an optional
Fabric/Core capability. It must not advertise `kb.search` or Code Graph in the
Stage 2 Gateway inventory.

## Historical artifacts

[`manual-alpha-validation-2026-07.md`](manual-alpha-validation-2026-07.md) is
dated evidence for the July 2026 candidate, not a current procedure.
[`code-graph-benchmarks.md`](code-graph-benchmarks.md) records internal package
benchmark evidence. Neither defines a live Stage 2 CLI, MCP, setup, connector,
or release surface.
