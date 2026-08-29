# Sync v2 Slice 4 Read Paths Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the descriptor-only public sync-v2 attach, bootstrap, and pull contracts into live, proof-authenticated Fabric handlers without yet adding push, conflict, Gateway transport, private sync, or production server assembly.

**Architecture:** MCP remains the sole composition owner across Git observation, Identity, Git streams, Activity, and the Slice 3 `MutationCoordinator`. Attach uses the existing fixed atomic coordinator; bootstrap and pull resolve an opaque attachment under forced RLS and return validated read-only evidence. Registry wiring occurs only after all three handlers pass their direct integration tests.

**Tech Stack:** Go, PostgreSQL 16, `database/sql`, Ed25519 public proofs, existing MCP JSON-RPC registry, shared `internal/types/projectstate` sync-v2 records.

## Global Constraints

- Baseline is `4de1e08`; schema version 22 with `dirty=false` is mandatory.
- Public sync accepts only `AssurancePublicKeyContinuity`; private sync remains deferred to Task 14.
- Public request bodies remain ID-free except for the opaque `attachment_ref`; never accept project, workspace, Fabric, remote-project, stream, or actor-routing IDs.
- Git observation is server-owned. No request supplies an observer credential.
- No Core package imports another Core package. Cross-Core composition stays in `internal/mcp`.
- No handler duplicates Core SQL, reducer logic, nonce storage, audit storage, or policy decoding.
- Attach is the only mutation in this slice. Bootstrap and pull are read-only and never append audit rows.
- Do not register push, conflict, agent-session, Activity accept/presence/lifecycle, private sync, Gateway transport, or production Fabric assembly.
- Public failures expose only the frozen safe code and operation label.
- Merged statement coverage must remain at or above 80%.

---

### Task 1: Live public attach handler

**Files:**
- Create: `internal/mcp/sync_v2.go`
- Create: `internal/mcp/sync_v2_test.go`
- Modify: `internal/mcp/public_auth.go`
- Modify: `internal/mcp/public_auth_test.go`

**Interfaces:**
- Consumes: `PublicProofVerifier.VerifyInitialAttach`, `git.CanonicalGitObserver`, `MutationCoordinator.ExecuteInitialAttach`, `MutationCoordinator.ReplayInitialAttach`, `projectstate.SyncAttachV2Args`, and `projectstate.SyncAttachV2Result`.
- Produces: a direct, unregistered `SyncV2AttachHandler` that accepts canonical raw arguments plus `types.PublicRequestProof` and returns the exact closed v2 result or one safe public failure.

The production constructor receives this exact observer interface:

```go
type CanonicalGitObserver interface {
	ObserveRef(context.Context, types.RepositoryIdentity, string, string) (
		git.RefObservation, projectstate.Tree, error,
	)
}
```

This is the existing `internal/core/git.CanonicalGitObserver`; do not create a second
observer abstraction. The Fabric-owned credential reference is constructor state, not a
request field. The handler calls the observer before opening SQL, extracts the one tracked
human whose canonical Ed25519 key equals the verified proof key from the validated tree,
and passes that exact `ActorV1` to the coordinator. The observer owns network reads only;
the handler owns no observer transaction and never accepts actor evidence from the request.

- [ ] **Step 1: Freeze the handler dependency boundary in failing tests.**

Define narrow MCP-owned interfaces for the server observer and attach coordinator. Tests construct the handler without a registry and prove nil dependencies fail before SQL or observation.

- [ ] **Step 2: Add strict attach request RED tests.**

Add table tests that reject unknown, duplicate, missing, null, wrong-kind, trailing, and noncanonical JSON; wrong version; repository/ref mismatch; wrong commit/tree digest; invalid finite Activity policy; noncanonical proof timestamp; padded/base64-alphabet errors; and every forbidden private routing field. Assert observer, coordinator, nonce, audit, and database counters remain zero.

- [ ] **Step 3: Add observed-human and replay RED tests.**

Using real schema-22 PostgreSQL, prove the server observer supplies the exact repository/ref/commit/tree and tracked human, the signing key belongs to that human, first activation returns one generated attachment, exact retry consumes only a fresh nonce, changed or denied retry burns the nonce without domain mutation, another human receives a distinct workspace, and concurrent distinct nonces converge on one attachment.

- [ ] **Step 4: Implement the direct attach handler.**

Strict-decode and recanonicalize `SyncAttachV2Args`; call the server observer; exact-match repository, canonical ref, commit, and tree digest; verify the initial proof against the repository-derived scope; build `InitialAttachCommand` only from verified/observed values; call `ExecuteInitialAttach`; and map only frozen sentinels to safe failures. Never derive authority from request-supplied actor or route IDs.

- [ ] **Step 5: Run the Task 1 gate.**

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/mcp -run 'Test(SyncV2Attach|PublicProof|ExecuteInitialAttach)' -count=1
WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./internal/mcp -run 'TestSyncV2AttachConcurrent' -count=1
```

- [ ] **Step 6: Commit the reviewed deliverable.**

```bash
git add internal/mcp/sync_v2.go internal/mcp/sync_v2_test.go internal/mcp/public_auth.go internal/mcp/public_auth_test.go
git commit -m "feat(sync): serve public v2 attach"
```

Run an independent task review against the exact Task 1 base/head. Fix every Critical or Important finding and repeat the focused gates.

### Task 2: Live bootstrap and pull handlers

**Files:**
- Modify: `internal/mcp/sync_v2.go`
- Modify: `internal/mcp/sync_v2_test.go`
- Modify: `internal/mcp/public_auth.go`
- Modify: `internal/mcp/public_auth_test.go`
- Modify only if a missing read seam is proven: `internal/core/git/public_streams.go`, `internal/core/git/public_streams_test.go`, `internal/core/git/activity_store.go`, `internal/core/git/activity_store_test.go`

**Interfaces:**
- Consumes: `PublicProofVerifier.VerifyBound`, `git.ReadAttachmentInTx`, Identity public-issued-scope resolution and nonce consumption, `git.ActivityStore.CurrentPolicyInTx`, `projectstate.SyncBootstrapV2Args/Result`, and `projectstate.SyncPullV2Args/Result`.
- Produces: direct, unregistered `SyncV2BootstrapHandler` and `SyncV2PullHandler` with one shared bound-proof resolver.

Server bootstrap and pull are strictly read-only with respect to stream, binding, cursor,
policy, Activity, and audit state. Slice 3 attach already creates the remote stream/binding
and publishes the required finite Activity policy. The approved design's “bootstrap
installs portable binding/cursor state” describes the later Gateway-side acceptance step,
owned by the Gateway transport slice; it is not a Fabric mutation and is out of Slice 4.
Because the frozen `SyncBootstrapV2Result` contains a required finite policy and no disabled
variant, a missing or invalid server policy is `activity_policy_required`; the explicit
local Activity-disabled marker is created later by Gateway when it cannot validate a
remote policy response.

- [ ] **Step 1: Add bound-proof and route RED tests.**

Prove attachment resolution is server-derived, proof and attachment issuer must match, nonce use is project-scoped, and wrong project/Fabric/repository/ref/issuer/detached evidence fails before any response. Include forced-RLS cross-project fixtures and concurrent nonce replay.

- [ ] **Step 2: Add bootstrap RED tests.**

Assert bootstrap returns exact current stream version, accepted commit/tree, live tree, and the current finite Activity policy. A missing, malformed, unknown, or unbounded server policy returns safe `activity_policy_required` and never leaks raw evidence. Bootstrap performs no stream, binding, request, conflict, Activity, cursor, policy, or audit mutation.

- [ ] **Step 3: Add pull RED tests.**

Assert `changed` is exactly `current_version > after_version`; after-version bounds are enforced; unchanged pull returns the frozen complete state shape with `changed=false`; changed pull returns the exact validated state. `SyncPullV2Result` carries no Activity deliveries—Activity pull remains descriptor-only in this slice. Wrong cursor or corrupt stream evidence returns a safe error without mutation.

- [ ] **Step 4: Implement the shared bound resolver and handlers.**

Verify proof shape before SQL, resolve the attachment/project via the security-definer resolver, then use one caller-owned project transaction for the complete bound call: lock/read the complete route, exact-match every signed scope field, resolve the fresh public-issued scope, consume the nonce, assemble and validate the stream response (plus current policy for bootstrap), then commit once. Any route, cursor, tree, policy, or response validation failure rolls back the nonce, so a corrected retry may reuse that nonce; a successful bootstrap/pull consumes it exactly once. No second read transaction, portable operation, binding/cursor/policy mutation, or audit row is permitted.

- [ ] **Step 5: Run the Task 2 gate.**

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/mcp ./internal/core/git ./internal/core/identity -run 'Test(SyncV2Bootstrap|SyncV2Pull|PublicProofBound|AttachmentReads|ActivityPullReturnsOpaque)' -count=1
WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./internal/mcp -run 'TestPublicProofNonceReplay' -count=1
```

- [ ] **Step 6: Commit the reviewed deliverable.**

```bash
git add internal/mcp/sync_v2.go internal/mcp/sync_v2_test.go internal/mcp/public_auth.go internal/mcp/public_auth_test.go internal/core/git
git commit -m "feat(sync): serve v2 bootstrap and pull"
```

Stage only Core files actually changed. Run an independent task review and repair all Critical/Important findings.

### Task 3: Explicit public registry and JSON-RPC dispatch

**Files:**
- Modify: `internal/mcp/registry.go`
- Modify: `internal/mcp/registry_test.go`
- Modify: `internal/mcp/jsonrpc.go`
- Modify: `internal/mcp/jsonrpc_test.go`
- Modify: `internal/mcp/fabric_registry.go`
- Modify: `internal/mcp/contract_manifest_test.go`
- Modify: `internal/mcp/sync_v2_contract_test.go`
- Modify: `docs/mcp-protocol.md`

**Interfaces:**
- Consumes: the direct handlers from Tasks 1–2 and the frozen descriptor schemas from Slice 1.
- Produces: a public Fabric registry containing exactly live attach/bootstrap/pull plus the still descriptor-only remaining public tools; explicit version-selected strict dispatch; unchanged private registry contents.

- [ ] **Step 1: Add descriptor/registry RED tests.**

Assert public `tools/list` exposes only closed ID-free v2 branches, attach/bootstrap/pull are live, the other seven public descriptors remain unavailable, and the private registry retains its exact sixteen unrelated tools with no sync path. Compare normalized schemas byte-for-byte with Slice 1 fixtures.

- [ ] **Step 2: Add explicit dispatch RED tests.**

Dispatch decodes only the required integer `version`, selects version 2, then strict-decodes the selected closed struct. Reject missing, string, fractional, null, unknown version, duplicate version, unknown members, and trailing JSON before handler invocation. Result encoding must match the frozen result variant exactly.

- [ ] **Step 3: Implement registry/dispatch wiring.**

Extend `Tool` only with the minimum explicit argument/result variant metadata needed by JSON-RPC. Preserve existing private handler behavior. Add a separate public-registry constructor/dependency record; do not overload `NewFabricRegistry` or expose public sync through credential authentication. Register attach/bootstrap/pull only when their complete dependencies are non-nil.

- [ ] **Step 4: Reconcile protocol documentation.**

Document proof authentication, opaque attachment routing, live attach/bootstrap/pull, safe errors, and the fact that push/conflict/agent-session/Activity descriptors are not live yet. Do not document Gateway transport or private sync as shipped.

- [ ] **Step 5: Run the Slice 4 gates.**

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/mcp ./internal/core/git ./internal/core/identity -run 'Test(SyncV2Attach|SyncV2Bootstrap|SyncV2Pull|PublicProof|Descriptor|JSONRPC|FabricRegistry|AttachmentReads|ActivityPullReturnsOpaque)' -count=1
WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./internal/mcp -run 'Test(SyncV2AttachConcurrent|PublicProofNonceReplay)' -count=1
go test ./... -count=1
go vet ./...
git diff --check
```

- [ ] **Step 6: Commit and review the complete slice.**

```bash
git add internal/mcp docs/mcp-protocol.md
git commit -m "feat(sync): dispatch public v2 reads"
```

Run the task review, then a whole-slice review over the recorded Slice 4 base/head. The final reviewer must confirm proof-before-mutation, server-derived scope, forced-RLS isolation, exact nonce semantics, read-only bootstrap/pull, opaque source refs, unchanged private registry, no live deferred tools, no runtime/Gateway production changes, and coverage at or above 80%.
