# Sync v2 Slice 7 Gateway Durability Amendment

**Status:** pending written-spec approval and independent re-review

**Date:** 2026-09-02

**Scope:** Stage 3 Task 6, Slice 7 only

## 1. Decision and authority

This document is the complete authority for Slice 7, **Gateway v2 durability**. It
implements the seventh ordered slice of the approved
`2026-08-28-sync-v2-public-identity-design.md`: selected-human proof signing, the
strict public runtime client, atomic local install, accepted-tree import, portable
cursor/conflict/queue convergence, byte-identical retry, and both local conflict
checks.

The cited master plan
`docs/superpowers/plans/2026-08-28-sync-v2-public-identity-implementation-plan.md`
is absent from the repository and Git object inventory. This amendment replaces that
missing plan as the sole Slice 7 behavioral and authority contract. The next planning
step must update the current parent plan to cite this amendment, remove its stale
sync-v1/v8 assumptions for Slice 7, and decompose implementation in the order in §14.
Implementation may not begin from the old Task-6 prose alone.

Authority order for this slice is:

1. RFC-0003 and the Git-native architecture;
2. the approved Activity-v1 retention amendment;
3. the approved 2026-08-28 public-identity design;
4. this focused amendment for Slice 7 details; and
5. older plans and reconnaissance only where they do not conflict with the first four.

This amendment supersedes the old requirements for sync-v1 compatibility, private
credential transport, Fabric Activity promotion, and a Gateway v8 format during Slice
7. It does not alter the shared sync-v2 or Activity-v1 public record definitions.

Git remains the sole accepted authority for the local workspace. Fabric's `AcceptedTree`
is authenticated evidence of Fabric's independent canonical-Git observation, but it
never advances or rewrites `types.WorkspaceBinding.AcceptedRef`,
`AcceptedCommitSHA`, or `AcceptedTreeDigest`. The checkout and the local Git observer
remain authoritative for those fields. Slice 7 instead persists a route-scoped,
durable remote-replica record containing Fabric's accepted/live snapshots and stream
cursor. The replica is evidence for convergence and restart recovery, not a local Git
base. The import writes neither the checkout nor Git. A later explicit Git observation
of the checkout at the remote accepted commit may advance the local binding through
the existing Git-base transition; an unchanged checkout must never silently undo or
reclassify the durable remote replica.

## 2. Slice boundary

Slice 7 delivers independently constructible and testable components, but does not
activate them in a running product:

- a narrow selected-human Ed25519 signer;
- one proof-owning strict public caller shared by portable sync and Activity;
- a strict runtime client for attach, issue-agent-session, bootstrap, pull, push, and
  conflict;
- the public `wormhole.sync.issue_agent_session` handler;
- destructive exact Gateway schema v9 and local durability repositories;
- a ProjectState-owned accepted-tree import transaction; and
- one routed `sync.V2Engine` coordinator capable of attach/bootstrap, pull, queue
  drain, and remote conflict resolution when dependency-injected in tests.

Slice 8 owns the public registry entry for `issue_agent_session`, the final exact-ten
public registry inventory, `cmd/fabric` and `cmd/gatewayd` construction, supervisor
activation, local API wiring, production configuration, documentation claims, and the
whole-Task review. A production Gateway must contain exactly one routed `V2Engine`
instance per supervisor, shared by all routed portable operations. Slice 7 neither
constructs nor starts that instance in production.

Programme Task 7 remains the owner of user-facing attach/detach, zero-contact fork
policy, rebind decisions, and CLI/UI workflow. Slice 7 exposes the internal attach and
bootstrap operation Task 7 will consume; it adds no user-facing command.

## 3. Non-negotiable invariants

1. Runtime transport uses the shared values in `internal/types/projectstate`. It does
   not import `internal/mcp`, duplicate wire records, or define another canonical JSON
   encoder.
2. The selected private key never leaves `internal/runtime/localidentity`. SQLite,
   logs, errors, interfaces, and results contain no private key, seed, token, credential
   contents, or proof preimage.
3. Public `credential_ref` is exactly
   `localidentity:human:<canonical-selected-human-UUID>` and is non-secret. It must
   match the selected profile at the instant of signing.
4. Every network attempt reloads and jointly validates the workspace, active binding,
   profile, portable cursor, route-scoped remote replica, and relevant local
   ProjectState view. Route data is not cached as authority. The route is selected only
   from that exact persisted `WorkspaceScope`; public arguments, display handles, URL
   similarity, environment, and last-used profiles are never routing inputs.
5. No SQLite transaction, SQLite connection writer barrier, ProjectState workspace
   lock, selected-key lock, or engine status lock spans DNS, client construction, or
   network I/O.
6. Sync never writes `workspace_bindings`, candidates, overlay operation state,
   `workspace_conflicts`, or accepted snapshots directly. Only ProjectState's import
   facade delegation may change them. The same delegation owns remote-replica writes;
   sync has no direct table or writer access.
7. A portable cursor and its route-scoped remote-replica accepted/live snapshots
   advance only in the ProjectState accepted-tree transaction that installs the
   corresponding complete remote state. The local `WorkspaceBinding` accepted base
   does not change in that transaction. An Activity cursor advances only through the
   Activity owner's validated pull-batch transaction.
8. Initial install writes the active binding, initial portable cursor, Activity cursor,
   and either enabled policy evidence or explicit disabled state in one immediate
   transaction. No partially usable route is publishable.
9. Ongoing convergence revalidates the existing binding, profile relationship, and
   stored policy state, but does not rewrite them and does not couple their CAS to a
   portable cursor update. Activity policy changes remain Activity-owner transactions.
10. Fabric supplies canonical trees and opaque server conflict IDs. It never supplies
    local semantic conflict records. ProjectState alone derives and encodes local
    conflicts.
11. A route failure is isolated to that route: local workspace operations and every
    other Fabric route remain available. A consumer receives immutable value copies and
    cannot mutate Activity queues, cursors, conflicts, receipts, policy, or binding
    identity through any Slice 7 interface.

## 4. Components and APIs

The exact component boundary is below. A plan may refine unexported helper names but may
not move authority between owners or broaden any interface.

| Owner | Slice 7 API | Contract |
|---|---|---|
| `runtime/localidentity` | `SelectedHumanSigner.SignSelectedHuman(ctx, credentialRef, message)` | Exact-match the selected human reference, sign a copied message under the existing owner-only topology, and return only key ID, copied public key, and copied signature. |
| `runtime/sync` | `PublicToolCaller.Call(ctx, profile, credentialRef, toolName, scope, arguments)` | Canonicalize arguments once, create a fresh proof, send those exact bytes in a closed `tools/call` request, and strictly decode one bounded response. |
| `runtime/sync` | `V2FabricClient` with `Attach`, `IssueAgentSession`, `Bootstrap`, `Pull`, `Push`, and `ResolveConflict` | Use only shared request/result values; own no persistence and accept no caller-supplied remote routing IDs beyond the public records. |
| `mcp` | `SyncV2AgentSessionHandler.Handle(ctx, rawArguments, proof)` | Verify an attachment-scoped human proof, derive the accountable human from the current accepted tree, issue/replay the session in the existing identity transaction, and co-commit nonce and audit evidence. |
| `runtime/sync` | existing `FabricRouteSource.GetRoute(ctx, scope) (types.FabricBinding, types.FabricProfile, error)` | Read one exact active route from the local private store. The existing value-returning method is the attach consumer boundary; each value is an owned copy, `FabricProfile.CredentialRef` is a reference only, and no mutator is exposed. |
| `runtime/localstore` | exact route/cursor/policy readers plus transaction-scoped install, cursor, queue, remote-replica, and remote-conflict primitives | Validate complete private keys and expose narrow operations to the ProjectState coordinator; no public tree semantics live here. |
| `runtime/projectstate` | `Service.ImportAcceptedTree(ctx, request)` | Directly delegate to the existing `transitionCoordinator`; independently validate expected local state and complete server evidence, derive semantic merge/conflicts, and atomically install remote-replica, candidate, operation, conflict, cursor, remote-link, and queue consequences without changing the local Git accepted base. |
| `runtime/sync` | one dependency-injected `V2Engine` with `AttachAndBootstrap`, `Pull`, `DrainPending`, `ResolveRemoteConflict`, and `Status(ctx, binding)` | Coordinate gates, fresh reads, network calls, validation, and the one ProjectState mutation call. Status is exact-binding scoped and owns one status lane per workspace; no configured status method is unscoped. |

`NewV2Engine()` remains the unconfigured offline/zero-pending constructor required by
the landed local-only behavior. Slice 7 adds one configured constructor for tests; it
rejects nil or internally inconsistent dependencies before returning an engine. Slice
8 is the only owner allowed to call that configured constructor from a process.

The issue-session handler accepts exactly the landed
`PublicAgentSessionIssueV2Args`. Its proof must have no session ID. Authorization is
attachment-only and human-only; the resolver locks the attachment, checks the issuer
key, and in that same transaction reads the accepted ProjectState tree. It must find
exactly one current accepted-tree actor with the requested ID and `actor_kind=agent`.
Harness/model values are bounded request provenance and are validated separately; they
are not read from `ActorV1`. The activated issuer key must resolve to exactly one current
accepted-tree human, and that human becomes the accountable principal. No agent-to-human
edge is inferred from the tree. A valid proof that fails either authority read still consumes its nonce and writes the denied audit row,
but issues no session. The handler then calls the existing
`IssuePublicAgentSessionInTx`, records `sync.issue_agent_session`, and commits once.
Exact live-session retry returns the same result, changed metadata maps to the existing
closed `sync_replay_conflict` code, and reissue after expiry creates a new 24-hour
session. Registration remains absent until Slice 8.

The existing route source is the complete internal attach result for private consumers;
Slice 7 does not add a parallel aggregate or resolver:

```go
type FabricRouteSource interface {
	GetRoute(context.Context, types.WorkspaceScope) (types.FabricBinding, types.FabricProfile, error)
}
```

`GetRoute` is server/local-store derived and exact: it validates the requested
`WorkspaceScope`, `WorkspaceBinding`, repository identity, canonical ref, Fabric
instance, remote project, stream, attachment, and profile relationship before returning
the pair. A consumer may retain the returned values but cannot mutate durable routing,
Activity, queue, cursor, conflict, receipt, or binding identity because the interface
has no write method and the values are copies. Any result used for attach/session work
is immutable for that attempt; explicit human CLI rebind is the only rebinding path.

### Future Code Graph seam (published, not implemented in Slice 7)

The Code Graph branch consumes one already validated `types.WorkspaceBinding`; it does
not receive a project handle, remote URL, Fabric profile, credential, Activity route,
sync cursor, or ambient workspace. The minimal additive seam to be implemented and
owned by that branch is:

```go
// package localapi; result/request types are owned by the Code Graph branch.
type CodeGraphProvider interface {
	Status(context.Context, types.WorkspaceBinding) (codegraphmanager.Status, error)
	Query(context.Context, types.WorkspaceBinding, codegraphquery.Request) (codegraphquery.Result, error)
	Rebuild(context.Context, types.WorkspaceBinding) (codegraphmanager.Status, error)
}
```

`codegraphmanager.Status` and the manager package are branch-owned; the request and
result reuse the existing `codegraphquery.Request` and `codegraphquery.Result` values.
Together they form the typed API over the existing local Code Graph `config`, `store`,
`index`, `query`, and `source` packages. `Status` reports
availability/freshness and active revision; `Query` performs the existing bounded
lexical/structural query and freshness-gated source behavior; `Rebuild` performs the
explicit local rebuild and returns its resulting status/revision. Provider and caller
both validate the binding, and all three methods fail closed on an invalid or
cross-workspace binding. The implementation and worker construction belong entirely to
the Code Graph branch. Slice 7 does not edit any localapi, gatewayd, contract, or Code
Graph implementation file and does not route any of these methods through Fabric,
Activity, identity, or sync.

## 5. Proof-owning public caller

The common caller performs this order for every call:

1. validate the public profile (`mode == public`), HTTPS base URL, exact Fabric instance
   UUID, logical selected-human credential reference, tool name, and derived repository,
   attachment, or attachment-plus-session proof scope;
2. canonicalize the closed argument object with the shared canonical encoder and retain
   those exact bytes;
3. obtain a non-zero UTC time from the injected clock and 32 cryptographically random
   bytes; format time as canonical RFC3339Nano and nonce as strict unpadded RawURL;
4. build the frozen `PublicProofMessage` preimage, ask the narrow signer to sign it, and
   verify the returned key ID/public-key relationship before use;
5. construct exactly one sibling object `{name,arguments,proof}` in a JSON-RPC
   `tools/call`; and
6. send through the bounded HTTPS transport and strictly decode the result.

Arguments are not re-marshaled between signing and sending. Retries preserve the exact
canonical operation and its ID, but use a fresh timestamp, nonce, proof, and JSON-RPC
request ID. Redirects, mixed proof and bearer authentication, non-HTTPS destinations,
wrong content type, oversized bodies, duplicate or trailing JSON, unknown fields,
multiple content items, unsafe error bodies, wrong versions/statuses, and ambiguous
success unions fail closed.

The landed Activity MCP adapter is changed to consume this caller rather than acquiring
its own proof or HTTP stack. `ActivityTransport` uses a mode-specific credential
branch: public profiles pass the logical selected-human `credential_ref` to the common
signer/caller and never call `CredentialSource.Read` or require bearer material; private
profiles retain the existing secret lookup boundary but private bearer/OIDC work is
deferred from Slice 7. A public Activity attempt therefore has no secret dependency.

Agent-authored queued work has an explicit attempt lifecycle owned by one shared narrow
session authority, used independently by `V2Engine` and `ActivityTransport`:

```go
type PublicAgentSessionAuthority interface {
	Acquire(context.Context, types.WorkspaceBinding, types.ActorEnvelope) (projectstate.PublicAgentSessionIssueV2Result, error)
}
```

The authority owns the only in-memory live-session cache, keyed by the exact attachment,
agent, accountable human, harness, model, and expiry. It reloads the route and selected
human for every acquisition, verifies that the immutable historical envelope is an agent
envelope with a complete stable tuple, verifies the selected human still equals its
`AccountableHumanID`, and issues/replays the exact session request. It returns a fresh
unexpired proof scope for the transport attempt; expiry forces issue/replay before send.
Neither caller may cache authority separately. This never changes historical actor,
accountable-human, model, session, Activity, or operation bytes. Human-attributed calls
do not use this authority and follow the separate human-proof path. Selected-human drift,
removed agents, expiry/reissue failure, and changed session metadata fail closed before
send. Activity remains owned by `ActivityTransport`; it is not routed through `V2Engine`.

## 6. Strict server-state validation

Before ProjectState is called, sync validates a complete `SyncStateV2` as one unit:

- protocol version and every safe integer are exact;
- the locally resolved route and returned state exactly match the persisted workspace,
  repository, canonical ref, Fabric instance, remote project, stream, and attachment;
  no route is selected from public arguments, display handles, URL similarity,
  environment, or a last-used profile;
- a repository/fork mismatch is rejected from the local route evidence before
  credential lookup, signer invocation, DNS, HTTP-client construction, Git-provider
  observation, or identity-provider activity. The same ordering applies to a missing,
  ambiguous, detached, or unsafe route;
- attachment, repository, canonical ref, accepted commit, and stream expectations are
  for the freshly loaded route;
- both trees strictly decode, validate, re-encode byte-identically, and reproduce their
  advertised lowercase SHA-256 digests;
- both snapshots have the expected project identity and repository identity;
- the live tree's Git-owned configuration/remotes equal the accepted tree's Git-owned
  values;
- `OpenConflictIDs` is non-nil, sorted, unique, and contains canonical UUIDs; and
- an attach/push/conflict result exactly names the request operation and advertised
  version/digest.

This validation is necessary but not sufficient. ProjectState repeats canonical tree,
digest, binding, expected-base, and expected-view checks inside its writer transaction.
The sync-side validation cannot authorize a write.

Activity policy is independently classified. A canonical finite policy produces
`enabled` evidence. An absent policy produces disabled reason `missing`; bytes that do
not strictly decode and canonicalize, including an unknown policy version, produce
`malformed`; a structurally decoded policy with a zero, overflowed, or out-of-range
retention bound produces `unbounded`. Unknown fields outside the policy member still
invalidate the whole response. This narrow classification preserves strict portable
state handling while ensuring invalid policy bytes can never enable Activity.

## 7. ProjectState accepted-tree import

`ImportAcceptedTree` is a direct facade delegation to the existing
`transitionCoordinator`; Slice 7 does not add a seventh ProjectState coordinator.
The transition coordinator owns this one caller-supplied
`WorkspaceRepo.WithImmediateWorkspace` transaction and reuses the existing workspace
composition, merge, operation, candidate, and conflict codecs. It does not call other
coordinators through public facades or open a nested writer.

The exact public shape is:

```go
type Service struct { // exactly six package-private coordinator pointers
	registration *registrationCoordinator
	workspace    *workspaceCoordinator
	publication  *publicationCoordinator
	checkpoint   *checkpointCoordinator
	gitBase      *gitBaseCoordinator
	transition   *transitionCoordinator
}

func (s *Service) ImportAcceptedTree(context.Context, ImportAcceptedTreeRequest) (ImportAcceptedTreeResult, error)
```

The request/result are internal runtime values, not a new wire contract. Their required
fields are fixed below; all slices, trees, operations, and records are copied on entry
and exit.

`ImportAcceptedTreeRequest` contains:

- the exact workspace scope and complete expected `WorkspaceRecord`;
- expected composed-view digest, overlay generation, candidate presence, workspace
  status, and open-conflict membership observed before the network call;
- either expected absence of a local route/cursor for initial install or the exact
  active binding, profile identity, and portable cursor for ongoing convergence;
- the prior signed server scope and the complete returned `SyncStateV2`;
- initial Activity policy classification when and only when installing a route;
- an optional queue consequence bound to the exact route, operation ID, canonical
  operation bytes, and operation digest; and
- an optional opaque remote-conflict consequence bound to that same operation; and
- an action tag identifying `initial_install`, `pull`, `push_deliver`,
  `push_retain_pending`, or `remote_resolution`.

`RemoteReplicaState` is the durable route-scoped projection of returned Fabric state:

```go
type RemoteReplicaState struct {
	Route             types.RemoteBindingKey // six fields, including CanonicalRef
	StreamVersion     int64
	AcceptedCommitSHA string
	AcceptedTree      state.Tree
	AcceptedTreeDigest state.Digest
	LiveTree          state.Tree
	LiveTreeDigest    state.Digest
	UpdatedAt         time.Time
}
```

It is tied one-to-one to the complete-key portable cursor by the v9 schema. It is the
only accepted/live remote snapshot ProjectState persists. It never changes the local
`WorkspaceBinding` accepted fields. `ImportAcceptedTreeResult` contains the committed
workspace status, local accepted/view digests, remote replica cursor version, derived
semantic conflicts, queue disposition, and whether the transaction changed durable
state. It returns no private route, policy bytes, canonical tree bytes, proof, or
credential evidence.

Inside that one transaction, ProjectState:

1. reloads the exact workspace and composed view and compares every expected predicate;
2. in initial mode, proves no binding or cursors exist and that the selected profile is
   the expected active public profile; in ongoing mode, exact-matches the active binding,
   profile relationship, portable cursor, and structurally valid enabled/disabled
   policy row without rewriting them;
3. independently decodes/canonicalizes the accepted and live trees and rechecks all
   state and route evidence;
4. requires the returned version to be at least the prior cursor and accepts equality
   only for an exact byte/digest replay;
5. computes `ThreeWayRebase(base, returned live snapshot, pre-network composed local
   snapshot)`, where `base` is the prior route-scoped remote-replica **live** snapshot
   when present, otherwise the local `WorkspaceBinding` accepted snapshot; Ours is the
   local composed view; and Theirs is the authenticated remote live view;
6. leaves `WorkspaceBinding.AcceptedRef`, `AcceptedCommitSHA`, and
   `AcceptedTreeDigest` unchanged. It writes the returned accepted/live trees and
   digests only to the route-scoped remote replica tied to the new cursor;
7. conditionally materialises the candidate. If the composed post-import view equals
   the local accepted snapshot and no active/rebased operation or semantic conflict
   remains, it deletes/omits the candidate and sets `clean`. Otherwise it stores the
   direct or rebased candidate and sets coherent `pending` or `conflicted` state;
8. transitions captured active local operations to `rebased` only when a rebase was
   actually performed, and replaces open local conflict occurrences using the existing
   ProjectState codec;
9. installs or compare-and-swaps the complete-key portable cursor and its tied remote
   replica to the returned version, accepted commit/digest, and live digest;
10. installs/updates the opaque remote-conflict link and applies the requested queue
    consequence only after the newly derived conflict membership is known; and
11. in initial mode only, inserts the binding, Activity cursor zero, and enabled or
    explicitly disabled policy state before commit.

Any mismatch returns `projectstate.ErrAcceptedTreeImportDrift`; malformed remote
evidence returns `projectstate.ErrAcceptedTreeEvidence`; a stale complete-key cursor
returns `localstore.ErrFabricCursorConflict`. Each error rolls back every listed
consequence. The method never reads the filesystem, invokes Git, performs network I/O,
creates an actor, or calls the Activity transport. Candidate provenance is the fixed
non-actor token `fabric_sync_v2`; this token is added to the closed local candidate
import-origin union and is not attribution.

Opaque Fabric conflict IDs are never copied into `workspace_conflicts`. The local
semantic IDs and field evidence come only from `ThreeWayRebase`. A remote conflict may
validly correspond to zero local semantic conflicts; in that case the durable remote
link, not a fabricated local conflict, drives `attention_required`.

The local Git observer has an explicit restart interlock. `RefreshWorkspace` compares
the physical checkout with the local `WorkspaceBinding` first. If the checkout still
equals the binding's accepted ref/commit/tree, it leaves the remote replica and cursor
untouched; the replica is not treated as a local Git transition. Only a genuine later
checkout movement (a changed ref, commit, or tree) enters the existing Git-base
coordinator. When that movement lands exactly on the replica's accepted commit/tree and
the replica also has `AcceptedTree == LiveTree`, the Git-base coordinator advances the
local binding, deletes the `fabric_sync_v2` candidate, and finishes `clean` only when
there are no local active/rebased operations or semantic conflicts. If remote live or a
local overlay differs, it advances the binding and preserves/rebases the proposal with
coherent `pending|conflicted` state. The route-scoped remote replica and cursor remain
unchanged; only a later Fabric convergence transaction advances them. An unrelated
checkout movement follows the existing branch/base transition. A restart after importing
B → remote C while the checkout remains B therefore preserves C as remote evidence and
does not rebase it back to B. Tests must run real `PrepareRegisteredWorkspaces`/
`RefreshWorkspace` for that case and for a later actual checkout movement.

## 8. Initial attach and read recovery

Attach and bootstrap network calls complete before any local writer begins. To make a
crash between those calls recoverable without changing the frozen public records,
Fabric attach success is restricted to a source stream version whose live tree equals
its accepted tree. `SyncAttachV2Result.StreamVersion` is that immutable attachment
source version, including on exact attach replay. The client therefore knows the full
source precondition from its attach request: accepted commit/tree, source version, and
live digest equal to the accepted-tree digest.

Bootstrap and pull authorize a retained historical signed scope, not only the current
version. Fabric exact-matches repository/ref plus accepted commit/tree and live digest
at `ExpectedStreamVersion`, requires the version to be no earlier than the attachment's
source version, then returns the current complete state from the same repeatable-read
transaction. Mutations remain current-state exact, apart from their existing exact
operation replay rule. Thus every signed precondition is still checked, a stale client
can fetch current trees, and no conflict wire expansion is required.

The initial engine flow is:

1. capture the exact registered workspace/base/view and selected public profile;
2. call attach and strictly validate its route IDs, source version, and policy
   classification;
3. call bootstrap using the immutable source precondition and validate the complete
   current state and repeated policy classification;
4. require attach/bootstrap identity and valid-policy evidence to agree; if either
   policy is invalid, choose the most restrictive disabled reason in order
   `missing`, `malformed`, `unbounded` and retain no policy bytes; and
5. call `ImportAcceptedTree` in initial mode once. The binding installed by this
   transaction is the exact local workspace/repository identity selected before the
   call; the server-derived remote project, stream, attachment, accepted/live trees,
   and cursor are stored in its route-scoped remote replica. No value from a public
   handle or response can select a different local route.

A crash before step 5 leaves no local binding. Repeating attach returns the same
attachment and source version; repeating bootstrap returns current state; the one local
install remains safe. A commit outcome that SQLite reports as unknown is resolved by
reopening and exact-reading one action-tagged confirmation projection (defined below),
not by inspecting an unscoped subset of rows.

For every `ImportAcceptedTree` action, the transition coordinator records the attempted
action tag and a fresh monotonic workspace revision in the confirmation evidence. The
projection includes, for the exact six-field route and workspace scope: local binding
identity and accepted ref/commit/tree, workspace revision/status, accepted and composed
view digests, candidate presence/direct/rebased candidate digests and provenance,
operation IDs/bytes/digests and membership states, semantic conflict IDs/evidence and
open membership, portable cursor and tied remote-replica cursor/version/accepted/live
canonical tree digests, remote-conflict link state/IDs, queue operation state and
canonical bytes/digest, Activity cursor, policy state/digest or disabled reason, and
all timestamp-bearing action provenance required to identify this attempt. The
projection is compared as an exact canonical value, including absent-vs-present rows;
timestamps are evidence only when bound to the action/revision, never a standalone
success test.

After reopening, an exact tagged next projection returns the original committed result.
An exact tagged prior projection is retryable with the same immutable operation bytes
and action tag. Any mixed, third-state, unexpected route, concurrent revision, unstable,
or otherwise non-identical projection returns `ErrAttentionRequired` and preserves all
evidence. The implementation must fault-inject commit confirmation for clean,
conflicted, queue-delivery, remote-link, remote-resolution, and every initial policy
branch, then reopen before classifying the result.

## 9. Ongoing pull, push, and remote conflict flow

### Pull

The engine serializes work for one workspace, captures the exact route, remote-replica
cursor, and local view, releases all locks, signs a pull from cursor evidence, and
receives the current complete state. `changed=false` is accepted only when the full
state exactly matches the cursor, remote replica, and local view evidence; it causes no
write. A changed result goes through `ImportAcceptedTree`. Cursor equality is
idempotent; regression or divergent same-version evidence is corruption.

### Push queue

Every pending push uses this causal order:

1. query the exact `(project_id, workspace_id)` open-conflict gate;
2. if it is open, return `localstore.ErrWorkspaceConflicted` before route/profile read,
   credential lookup, signer, DNS/client construction, HTTP, import, cursor, audit, or
   queue mutation;
3. if the gate read fails, fail closed with the same zero-side-effect ordering;
4. reload and validate route, profile, complete-key cursor/remote replica, local view,
   and the complete pending row;
5. release local locks and send the exact stored `OperationV1` bytes;
6. after any applied result, fetch and validate the resulting complete state and invoke
   `ImportAcceptedTree` with `deliver` for that exact row; and
7. after conflict or stale/precondition, keep the row byte-identical pending, fetch the
   current complete state through historical read authorization, and invoke
   `ImportAcceptedTree` with `retain_pending` plus any returned opaque conflict ID.

The transaction-local `deliver` primitive preserves the existing `QueueRepo.MarkDelivered`
semantics: it rechecks exact-workspace open conflicts immediately before its complete-key
update. The standalone `MarkDelivered` method and ProjectState import use one shared
transaction-local implementation. If a conflict opened in flight, the entire queue row
remains byte-identical pending and cursor/import consequences also roll back. After that
local conflict is explicitly resolved, the engine retries the same operation ID and
canonical bytes; Fabric exact replay returns the original applied result, after which
the same accepted-tree transaction marks the row delivered once.

A stale/precondition response never becomes a fabricated local conflict. The fetched
trees determine whether ProjectState finds semantic conflicts. The immutable original
queue row remains pending. If the merge is clean but its current live digest no longer
equals the operation's immutable `ExpectedViewDigest`, the engine does not rewrite or
blindly retry it; it records no false delivery and reports
`sync.ErrRemotePrecondition`/`attention_required`. A later explicit resolution operation
is a distinct canonical operation. Slice 7's internal conflict API can submit it only
when supplied by an authorized caller; programme Task 7 owns the user workflow.

### Remote conflict resolution

The v9 remote-conflict link retains the opaque Fabric conflict ID, exact original queue
operation/digest, and detected version/live digest across restart. It contains no local
field evidence. `ResolveRemoteConflict` requires that exact open link, no open local
semantic conflicts, a caller-supplied canonical resolution operation whose actor is
already authorized, and fresh route/cursor evidence. Before any network call, the
transition coordinator durably records the canonical resolution operation ID, exact
canonical bytes, and digest in the open link as a `prepared` intent. A second request
with the same ID and bytes resumes it; the same ID with different bytes returns the
existing `sync_replay_conflict` outcome. The engine then calls
`wormhole.sync.conflict` with those exact bytes, fetches the resulting complete state,
and asks ProjectState to close the link, import state, and mark the original queue row
delivered only if the post-import conflict gate is clear. A process crash after remote
success leaves the prepared intent intact; restart replays that exact ID/bytes and
converges the same transaction. Failure before a known remote result leaves the
prepared intent and original queue row unchanged for exact retry; a different
resolution is never substituted.

## 10. Concurrency and crash behavior

One configured engine owns a keyed in-memory serialization lane per workspace. Different
workspaces may perform network I/O concurrently; calls for one workspace are ordered.
This is scheduling, not authority. SQLite expected-state checks remain authoritative
across restarts, tests, and accidental concurrent callers.

Network calls run with no local transaction. Consequently remote success and local
commit cannot be one distributed transaction. Safety comes from Fabric operation replay,
complete response validation, ProjectState expected-state checks, cursor CAS, and queue
post-network conflict recheck.

- Crash before a request: no remote or local mutation.
- Timeout/connection loss before a known result: queue stays pending; retry uses the
  same operation bytes and fresh proof.
- Remote success followed by local crash: replay returns the same remote result, then a
  fresh complete-state fetch converges locally.
- Local conflict opened during network: accepted import/delivery rolls back and status
  becomes attention-required.
- Local view, binding, profile, cursor, remote replica, or policy drift during network:
  the import transaction returns drift/CAS conflict and changes nothing; the engine
  starts a fresh attempt from new evidence. The local binding's accepted Git base is
  never replaced by remote evidence.
- Process crash during SQLite commit: exact postimage/preimage inspection decides
  success or retry; a partial logical postimage is corruption and attention-required.
- Duplicate complete response: byte-identical state is read-only idempotent. Same
  version with different evidence, a lower version, or cross-route evidence is fatal
  protocol corruption.

`Status` remains the frozen `{state,pending_writes}` response, but the configured seam is
binding-scoped:

```go
type FabricRouter interface {
	Status(context.Context, types.WorkspaceBinding) (sync.Status, error)
}
```

`V2Engine.Status(ctx, binding)` uses the exact supplied binding and freshly counts
pending writes for that complete route. Each workspace has an independent status lane;
one route may be `synchronizing` while another remains `online`, and no status lock
spans a network call. The unconfigured local-only constructor may remain an offline
test double, but it is never used as a configured multi-workspace engine and cannot
infer scope from ambient context. A configured operation is `synchronizing` only while
coordinating an attempt, becomes `online` after coherent convergence, `offline` after
typed retryable Fabric unavailability, and `attention_required` after local/remote
conflict, protocol corruption, unsupported credential mode, or indeterminate local
state. `pending_writes` is never inferred from a cached global counter.

## 11. Destructive Gateway private schema v9

Slice 7 advances `GatewaySchemaVersion` from 8 to 9 and replaces the embedded
consolidated snapshot with `private_schema_v9.sql`. Version 9 is a destructive format
epoch, not migration 9. The binary accepts only:

1. a missing/empty database with no sidecar evidence, atomically initialized to the
   complete v9 schema and singleton ledger `{9}`; or
2. an exact v9 database reopened after read-only schema, object, and proof validation.

Every prior, future, partial, altered, or sidecar-only database, including exact v8, is
preserved byte-for-byte and refused before mutation with the existing backup/manual
removal guidance. There is no v8 reader, migration, exporter, converter, reset,
quarantine conversion, compatibility alias, or version fallback.

All v8 tables not changed below retain their exact constraints. V9 makes these focused
changes. The complete route key is always
`(project_id, workspace_id, fabric_instance_id, remote_project_id, stream_id,
canonical_ref)`; `types.RemoteBindingKey` is amended to carry all six fields and
`FabricBinding.RemoteKey()` returns all six. Every queue, cursor, replica, conflict,
audit/link predicate, index, primary key, unique key, and foreign key that claims route
identity includes all six fields.

### `fabric_cursors`

The complete six-column binding key remains the primary/foreign key. `pull_cursor` is
removed. The row contains `stream_version`, `accepted_commit_sha`,
`accepted_tree` (canonical tree bytes), `accepted_tree_digest`, `live_tree` (canonical
tree bytes), `live_tree_digest`, and `updated_at`. These accepted/live columns are the
route-scoped durable remote replica and are atomically tied to the cursor row; there is
no second unbound snapshot store. Versions use the safe integer range; commit/digest
and canonical-tree constraints match shared validators. A cursor is complete server
evidence, not a generic string token. Initial install uses the validated bootstrap
state, never a synthetic zero cursor. The row never changes the local
`workspace_bindings` accepted base.

### `sync_queue`

The queue is part of the v9 hard cut even though it existed in v8. Its primary key,
active/pending indexes, complete route foreign key to `workspace_fabric_bindings`, and
every `QueueRepo` read/update/deliver predicate include `canonical_ref` alongside the
other five route columns and `operation_id`. The typed queue methods accept the
six-field `types.RemoteBindingKey` (or a complete `types.ActivityRouteKey` where
appropriate); no five-field overload remains. This makes the conflict link's original
queue foreign key complete and prevents colliding refs from being read, delivered, or
linked across one another.

### `activity_policy_current`

The complete binding key remains its primary/foreign key. It adds:

- `state TEXT NOT NULL CHECK(state IN ('enabled','disabled'))`;
- nullable `policy_version` and `policy_digest`;
- nullable `disabled_reason TEXT CHECK(disabled_reason IN
  ('missing','malformed','unbounded'))`; and
- non-null `updated_at`.

One route has exactly one current row. The table check requires either:

- `enabled` with non-null valid version/digest, null reason, and a complete composite
  foreign key to `activity_policy_versions`; or
- `disabled` with both policy fields null and one non-null disabled reason.

Absence of the row is corruption, never disabled state. Disabled rows retain no unsafe
policy bytes. Policy-version rows remain immutable. Activity queue/send/pull/exposure
fails with `ErrActivityPolicyUnavailable` before credential/client construction for a
disabled row; portable sync and local work continue.

### `fabric_sync_conflicts`

V9 adds one private link table keyed by the complete six-column binding key plus
canonical Fabric `conflict_id`. It stores canonical `operation_id`, exact
`operation_digest`, detected safe `stream_version`, detected `live_tree_digest`, state
`open|resolved`, `created_at`, `resolution_intent_state` (`none|prepared|resolved`),
nullable `resolution_operation_id`/`resolution_operation_bytes`/
`resolution_operation_digest`, and `resolved_at`. It has complete foreign keys to the
active binding and original queue row. Its check is exact: `none` requires all
resolution fields and `resolved_at` null; `prepared` requires operation ID/bytes/digest
non-null and `resolved_at` null while link state remains `open`; `resolved` requires
all four fields non-null and link state `resolved`. The table is routing/lifecycle
evidence only; it has no record key, field path, Base/Ours/Theirs JSON, actor, or remote
tree column. Canonical resolution bytes are retained so a post-network/local-crash
restart can replay the exact operation.

### Fabric migration allocation

Slice 7 adds no Fabric PostgreSQL migration. Migration `000022` is owned by sync-v2
public authentication and remains unchanged. After the complete Slice 7 migration
inventory, the frozen allocation is therefore final Fabric migration `000022`; the
first available migration for the private-identity branch is `000023`. This allocation
is recorded here and in the Slice 7 plan/migration ledger. No production constant is
introduced solely to communicate the number, and private identity must not create or
rename a migration until this allocation is consumed through its normal migration
review.

Schema fingerprints, required-table/column inventories, fresh/open behavior, database
entity documentation, and unsupported-format tests advance together to exact v9.

## 12. Error contract

Packages may wrap these errors but must preserve `errors.Is`:

| Error | Meaning and side effect |
|---|---|
| `localstore.ErrWorkspaceConflicted` | Exact local pre/post-network conflict gate is open; no new side effect. |
| `localstore.ErrFabricCursorConflict` | Complete cursor absence/value CAS failed; whole import rolls back. |
| `projectstate.ErrAcceptedTreeEvidence` | Tree, digest, project/repository, live/accepted relation, or state evidence is invalid; zero mutation. |
| `projectstate.ErrAcceptedTreeImportDrift` | Expected workspace/base/view/operation/conflict state changed; zero mutation. |
| `sync.ErrFabricUnavailable` | Retryable DNS/transport/server availability failure; durable work remains pending and local work remains available. |
| `sync.ErrFabricProtocol` | Strict wrapper/result/safe-error validation failed; attention required and no response-derived mutation. |
| `sync.ErrRemoteSyncConflict` | Durable opaque remote conflict link is open. |
| `sync.ErrRemotePrecondition` | Remote state was fetched/imported but immutable pending bytes cannot be applied to that current view. |
| `sync.ErrAttentionRequired` | Public status classification for non-transient conflict, corrupt evidence, unsupported mode, or indeterminate local commit. |

Public handler errors remain safe closed codes. Session issue uses
`invalid_request`, `unknown_version`, `authentication_failed`,
`attachment_not_found`, `permission_denied`, `sync_replay_conflict`, and
`internal_error`, always paired with operation
`wormhole.sync.issue_agent_session`. No error includes attachment, route, profile,
credential, proof, actor, session metadata, tree, operation, policy, or private path.

## 13. Test contract

Implementation begins with causal RED tests. The following five names are immutable
Slice 7 acceptance requirements:

- `TestSyncV2PushOpenConflictStopsBeforeCredentialOrNetwork`
- `TestSyncV2PushConflictOpenedInFlightLeavesQueueRowByteIdenticalPending`
- `TestSyncV2PushConflictScopeIsolation`
- `TestSyncV2PushGateFailureHasNoSideEffects`
- `TestSyncV2PushRetriesExactOperationAfterResolution`

They instrument route/profile reads, credential lookup, signer, DNS/client creation,
HTTP, importer, cursor, audit, and queue writes. Additional required causal evidence is:

- signer reference matching, concurrent selection/signing, copied outputs, cancellation,
  exact preimage/RawURL/time/nonce behavior, and zero private-byte leakage;
- composed client-to-handler bytes and strict result/error decoding for all six sync
  methods, including session replay/expiry, tracked-agent same-transaction checks,
  `sync_replay_conflict`, and the handler remaining unregistered;
- public Activity calls use the common caller without invoking `CredentialSource.Read`,
  retain historical attribution while acquiring fresh session authority, reject
  selected-human drift, and reject human calls that attempt to omit their required
  proof path;
- historical bootstrap/pull precondition validation, source-version attach replay, and
  rejection of attach while source live differs from accepted;
- corrupt/noncanonical/duplicate/trailing/unknown/cross-route trees, digest mismatch,
  wrong IDs/status/version, and no importer call on failure;
- exact v9 fresh/open/refusal/fingerprint tests, v8 byte-preserving refusal, no
  compatibility symbol/path, enabled/three-disabled policy branches, six-field queue
  and cursor keys, colliding canonical-ref rows, and wrong-ref read/deliver/link
  rejection;
- statement and commit fault injection for every initial-install write, restart/exact
  retry, and proof that no partial binding/cursor/policy is readable;
- complete-key cursor CAS and branch/project/workspace/Fabric isolation; no cursor
  advance on validation, merge, queue, conflict-link, or commit failure;
- ProjectState clean merge, semantic conflict, zero-semantic remote conflict,
  idempotence, local overlay preservation, conditional candidate omission for clean
  initial/pull cases, accepted-base immutability, route-scoped remote-replica
  correctness, no checkout/Git write, and transaction rollback at every consequence;
- a real B → remote-C import followed by restart through
  `PrepareRegisteredWorkspaces`/`RefreshWorkspace` with checkout still at B, plus a
  genuine later checkout movement to C and to an unrelated commit;
- stale/precondition and conflict fetch/import behavior, durable opaque link restart,
  durable canonical resolution intent, same-ID/different-bytes rejection, remote
  success/local-crash replay, and byte-identical original queue retention;
- detached/recovery/quarantine/disabled-policy behavior stopping before forbidden
  dependencies while portable local work remains available;
- deterministic blocked-network tests proving no SQLite, workspace, identity, or engine
  status lock crosses network I/O;
- exact binding-scoped status tests for concurrent workspaces, independent pending
  counts, one synchronizing route beside one online route, and no ambient-route
  inference;
- issue-session untracked/removed/duplicate-agent, changed-metadata replay,
  expiry/reissue, and denied-valid-proof nonce/audit rollback tests;
- an architecture assertion that `Service` has exactly the six existing coordinator
  pointers and that `ImportAcceptedTree` is one direct `transitionCoordinator`
  delegation;
- one-engine per-supervisor construction enforcement in Slice 8-facing assembly tests,
  while Slice 7 production commands remain unchanged; and
- regressions for the landed server signed-precondition matrix, Activity policy/cursor
  atomicity, exact public/private registry inventories, and zero Fabric promotion seam.

Verification order is focused package tests, affected package tests, affected race
tests, vet, repository-wide tests, architecture scans, and focused coverage. The Slice
7 plan must set a measured focused coverage floor high enough that the merged Task-6
range remains at least 80% statement coverage; it may not infer a lower floor after
implementation. A fresh independent review must compare implementation and tests to
this amendment before Slice 7 is checkpointed.

## 14. Slice 7 task decomposition

Planning must preserve these independently reviewable tasks and dependency order:

1. **Identity and common caller:** narrow selected-human signer, proof authorizer,
   bounded strict public caller, and Activity caller adoption. No durable sync writes.
2. **Session and portable client:** issue-agent-session handler, all six strict client
   methods, historical read recovery, and handler/client integration tests. No live
   registry activation.
3. **V9 local durability:** destructive schema hard cut, complete cursor, explicit
   Activity-disabled state, route-scoped remote-replica snapshots, six-field queue keys,
   remote-conflict links with durable resolution intents, transaction-local queue
   primitive, and atomic initial-install storage support.
4. **ProjectState accepted-tree transition:** request/result, independent evidence
   validation, semantic merge/conflict codec, conditional candidate
   accepted/remote-replica/candidate/operation/cursor/link/queue transaction, fault/
   restart tests, fixed provenance token, and direct delegation through the existing
   `transitionCoordinator` (no seventh coordinator).
5. **Routed engine:** one configured coordinator, attach/bootstrap, pull, queue drain,
   stale/conflict convergence, remote resolution, status transitions, both gates, and
   deterministic no-lock-across-network tests.
6. **Slice gate:** package/race/vet/repository/coverage/architecture verification,
   independent review, repairs, re-review, and checkpoint. Slice 8 then performs live
   registry and production assembly.

Each task starts with its causal failing tests and ends with a compiling repository.
No task may pre-wire production merely to exercise its component.

## 15. Explicit exclusions

Slice 7 does not add:

- production process assembly or a second engine;
- the session tool's live public registry entry;
- user-facing attach/detach/rebind/zero-contact-fork workflow;
- sync-v1 names, decoders, compatibility, private sync, bearer credentials, AdminKey
   reuse, an agent key store, WebAuthn, or Git credential-helper integration;
- private OIDC, issuer discovery, browser callbacks, refresh tokens, private sessions,
   private bearer implementation, and authenticator work owned by
   `feat/private-human-identity-prep`;
- checkout mutation, Git writes, commits, branch movement, or acceptance derived from
   Fabric live/proposal state. Fabric accepted/live evidence remains a remote replica
   until a real local Git observation advances the local binding;
- Fabric-supplied semantic conflict fields or an expanded conflict result;
- direct sync writes to ProjectState tables, a second reducer, a second conflict codec,
   or a second Activity policy/current-pointer owner;
- Fabric Activity promotion, promotion codes/state, Activity-to-Operation routing, or
   Activity expiry effects on portable state;
- Code Graph implementation, manager/types/provider/worker construction, or any edit to
   `internal/runtime/localapi/providers.go`, `internal/runtime/localapi/supervisor.go`,
   `internal/runtime/localapi/mcp.go`, `cmd/gatewayd/gatewayd.go`, or
   `docs/contracts/alpha-contract.json`. The Code Graph branch consumes only the
   additive binding-scoped seam published in the companion interface note and owns its
   implementation; Code Graph remains outside Fabric, Activity, identity, and sync;
- ORM/datastore, project/ownership creation from Git, or a new join/connect identity
   model; or
- any v8-to-v9 migration, converter, exporter, reset, or compatibility path.

These exclusions are testable architecture boundaries, not deferred implementation
details. Before Slice 7 checkpoint, capture the exact base `0735306` and Slice 7 head
diff and gate it against production changes in either parallel branch. The captured
range must contain no Code Graph or private-OIDC production changes; do not cherry-pick,
copy, or infer parallel-branch implementation into this slice. Only the pre-existing
disabled Code Graph boundary and unrelated pre-existing code may appear in the range.
