# Sync v2 and Public Agent Identity Design

**Status:** approved design, pending written-spec review  
**Date:** 2026-08-28  
**Programme:** Stage 3 multi-Fabric, private identity, and four-VM trial

## Controller clarification (2026-08-28 plan review)

This clarification narrows implementation sequencing without changing the public wire:

- The exact sync-v2 and Activity-v1 value records live in
  `internal/types/projectstate/sync_protocol.go`, an allowed shared plain-value owner.
  MCP owns aliases, schemas, descriptors, authentication dispatch, and handlers; runtime
  imports the shared value owner and never imports `internal/mcp`.
- Task 6 ships public-key-continuity sync only. The private authenticated registry keeps
  its unrelated existing tools, but does not list or dispatch sync-v2 tools until Task 14
  extends the same `MutationCoordinator` with in-transaction private issuer
  revalidation. `Config.AdminKey` remains WebUI-only and is never an MCP bearer.
- Bootstrap atomically installs portable binding/cursor state and either a validated
  finite Activity policy or an explicit Activity-disabled state. The latter keeps
  portable sync usable while every Activity queue/delivery operation fails closed.
- Production Fabric application queries execute as the exact pre-provisioned
  `wormhole_fabric_runtime` login with no memberships. Migration 22 grants only its
  enumerated minimum table DML, resolver execution, and `audit_log_seq_seq` usage;
  application evidence never runs as the dev superuser. Schema migration is a separate,
  intentionally cluster-administrator-capable deployment operation because the migrator
  must transfer and later drop the security-definer resolver owned by the NOLOGIN
  `BYPASSRLS` resolver role. Cluster-role creation/alteration/deletion remains outside
  numbered migrations.
- The controller records the delivery base before Slice 1 and assigns a distinct worker
  to the whole-range review after Slice 8. Slice implementers neither self-certify nor
  push.

## Purpose

Deliver one live, production-assembled sync v2 protocol for public-key-continuity and
private-authenticated Fabric access. Git remains acceptance authority; Fabric stores
validated portable replicas and operational Activity only. This design destructively
removes sync v1 because Wormhole is closed pre-alpha and carries no compatibility
obligation.

## Binding decisions

1. Initial public attach authenticates a tracked human key. Fabric subsequently issues
   accountable agent sessions.
2. Attachment references are opaque and globally unique within one Fabric instance.
   A narrow security-definer resolver discovers only the owning project before normal
   forced-RLS access.
3. Portable operations retain stable author attribution. Request assurance, session,
   harness, model, and credential evidence authorize transport and are recorded in the
   immutable audit; they do not rewrite canonical operation bytes.
4. Activity promotion remains Gateway-local. Fabric has no promotion method, sentinel,
   descriptor, or promotion-named production code.
5. Task 6 introduces the shared atomic Core-mutation/actor-audit coordinator. Task 14
   extends it rather than introducing a second transaction owner.
6. Sync v1 and its compatibility descriptors/tests are deleted. Sync v2 is the sole
   portable sync protocol for public and private authentication.
7. Public proof is a sibling of `name` and `arguments` in MCP `tools/call`. Proof and
   bearer authorization are mutually exclusive.
8. The selected-human Ed25519 key signs agent requests. Fabric-issued `session_id`
   binds the request to the agent, accountable human, harness, model, and expiry.

## Protocol surface

### MCP carriage

```json
{
  "name": "wormhole.sync.push",
  "arguments": { "version": 2 },
  "proof": {
    "key_id": "sha256:...",
    "public_key": "...",
    "timestamp": "...",
    "nonce": "...",
    "signature": "...",
    "session_id": "..."
  }
}
```

`proof` is omitted for bearer-authenticated private calls. Supplying both proof and
bearer authorization fails authentication. Schemas are closed and strict; unknown
fields, duplicate/trailing JSON, missing required fields, and wrong versions fail.

The proof preimage is exactly:

```text
wormhole-public-v1
<server Fabric instance UUID>
<tool name>
<server-derived scope key>
<lowercase SHA-256 of canonical arguments>
<canonical RFC3339Nano timestamp>
<RawURLEncoding nonce>
```

Scope keys are `repository:<digest>` for attach, `attachment:<ref>` for human bound
calls, and `attachment:<ref>:session:<session_id>` for agent calls. Keys, nonces, and
signatures use strict unpadded RawURL base64. Timestamp acceptance is inclusive from
`now-5m` through `now+30s`. Nonces are consumed once under the server-derived project.

### Sync tools

The exact tool names are:

- `wormhole.sync.attach`
- `wormhole.sync.issue_agent_session`
- `wormhole.sync.bootstrap`
- `wormhole.sync.pull`
- `wormhole.sync.push`
- `wormhole.sync.conflict`

Every sync record requires integer `version:2`. Bound requests carry only the opaque
attachment plus repository/ref/base evidence and expected stream/live evidence. No
public argument contains project, workspace, Fabric instance, remote project, stream,
or actor-routing IDs.

Attach carries repository identity, canonical ref, base commit, and base tree digest.
Its closed result returns attachment ref, remote project ID, stream ID/version, and the
effective finite Activity policy. Remote IDs are response data retained only in the
Gateway private complete key and never echoed by later public requests.

Agent-session issue carries attachment ref, tracked agent ID, harness name/version, and
model name/version. Fabric derives the accountable human from the activated signing
key. A session lasts 24 hours, contains no token or key, and exact retry returns the
same live session. Changed metadata conflicts; reissue after expiry creates a new ID.

Bootstrap and pull carry the complete signed scope plus `after_version`. They always
return a complete validated stream state; `changed` is exactly whether the returned
version exceeds `after_version`. Bootstrap also repeats the effective finite Activity
policy so local binding, cursors, and policy can install atomically.

Push carries the complete scope and one canonical `OperationV1`. Its success variant is
`applied` or `conflict`; both return operation ID, stream version, and live digest, with
the conflict variant also returning the durable conflict ID. Conflict resolution carries
the complete scope, durable conflict ID, and canonical resolution operation, returning
`resolved`, operation ID, stream version, and live digest. Exact replay returns the
original result; changed bytes under an existing operation ID fail replay.

### Stable operation attribution

Human stable attribution is `(actor_kind, human_principal_id)`. Agent stable attribution
is `(actor_kind, agent_id, accountable_human_id)`. Both the portable operation actor and
resolved transport actor must validate and these stable tuples must match.

The operation is persisted byte-for-byte. Local/public/private assurance, session,
harness/model metadata, and occurrence time inside portable content are not routing
authority and are not rewritten. The immutable audit stores the freshly resolved
transport actor and request digest separately.

### Activity tools

The exact names are:

- `wormhole.activity.accept`
- `wormhole.activity.presence`
- `wormhole.activity.pull`
- `wormhole.activity.lifecycle`

Activity is schema v1 because it is a distinct protocol, not sync-v1 compatibility.
Requests are closed and reuse the Task-3A frozen `ActivityV1`, policy, and receipt
records. Accept and presence carry attachment, policy version/digest, Activity, and
Activity digest. Accept returns `accepted` with receipt and effective policy, or
`policy_changed` with replacement policy. Presence returns `accepted` or the same policy
replacement and creates no durable Activity, receipt, sequence, lifecycle, or audit row.

Pull carries attachment, `after_sequence`, and bounded limit. It returns current policy,
ordered historical policy evidence, deliveries with opaque `source_ref`, next sequence,
and `has_more`. It preserves the current-policy ceiling and historical-evidence contract.
Lifecycle carries attachment, activity ID, transition kind/reference, expected state,
and next state; the attachment derives the source workspace. It reuses the landed closed
lifecycle matrix.

Fabric exposes exactly eight Activity domain codes:

```text
invalid_activity
unknown_activity_version
activity_policy_required
activity_policy_changed
activity_not_found
activity_replay_conflict
activity_cursor_invalid
activity_lifecycle_conflict
```

## Identity lifecycle

Initial attach independently observes the numeric-ID repository/ref through Task 5 and
requires the supplied commit/tree evidence to match. The signing key must belong to one
live tracked human in the observed tree, and that portable project must already exist in
Fabric. Public Git content never creates server project, ownership, or membership
authority.

Attach atomically creates or exact-matches the repository/stream/workspace binding,
finite policy, human-key activation, nonce, and immutable audit. Exact retry by the same
human and stream returns the existing attachment. Another tracked human may create a
separate workspace attachment.

Session issue verifies the agent remains tracked, binds it to the activated human,
stores bounded harness/model provenance, and returns the non-secret session ID. Each
agent request is still signed by the selected human key. Fabric revalidates the human,
agent, session, attachment, and accepted tree on every resolution. Removing a tracked
actor disables future requests without rewriting history.

Activity authored by an agent contains the issued session provenance before local
queueing, allowing exact Activity actor comparison without rewrite. Delayed delivery may
validate the historical creation session even after expiry, but current request authority
must come from a live current session with the same stable actor tuple.

## Persistence and RLS

Task 6 owns PostgreSQL migration `000022_public_sync_v2`; reviewed migration 21 remains
unchanged. Migration 22:

- enforces attachment uniqueness within each Fabric instance;
- adds opaque Activity source refs and human-key attach idempotency;
- creates forced-RLS public agent sessions with complete tenant/route foreign keys;
- adds exact conflict-resolution operation/version evidence;
- normalizes public actor keys as activated tracked-human keys;
- adds typed canonical transport-actor/request evidence and immutability to audit rows;
- adds nonce/session/attachment indexes and constraints; and
- creates the minimal attachment-to-project security-definer resolver.

The resolver accepts Fabric instance and attachment UUID and returns only project UUID or
null. Its dedicated pre-provisioned NOLOGIN `BYPASSRLS` owner has `SELECT` only on the
binding table. `PUBLIC` has no execute; only the exact `wormhole_fabric_runtime`
application login may call it, and that login remains `NOBYPASSRLS`, `NOINHERIT`, and has
no memberships. After resolution, every query runs in a normal project-scoped transaction
under forced RLS.

Migration ownership is destructively renumbered: Task 6 owns 22, private identity owns
23, and legacy identity recovery owns 24. Later gates target schema 24. No compatibility
or migration aliases are retained.

## Atomic mutation authority

One shared `MutationCoordinator` begins a project transaction, revalidates attachment,
issuer/session, and actor scope, invokes the Core `*InTx` mutation, appends canonical
transport-actor audit evidence, and commits. Any authority, domain, reducer, or audit
failure rolls back the entire transaction.

Attach, session issue, push (including durable conflict), observed accepted-ref advance,
conflict resolution, Activity accept, and Activity lifecycle use the coordinator.
Presence and reads do not produce mutation audits. An already activated key's nonce is
consumed before dispatch so denied/failed requests cannot replay; initial activation and
its first nonce are atomic with attach.

Task 14 reuses this coordinator and adds private issuer revalidation. It must not create
a second audit or transaction owner.

## Gateway durability and production assembly

Gateway signs through a narrow localidentity interface returning key ID, public key, and
signature only. Private bytes never leave localidentity. Public profile credential refs
are non-secret logical references to the selected human; private profiles retain their
secret-store reference.

Each portable push checks the exact workspace conflict gate before route, credential,
signer, DNS/client construction, or network. It then reloads binding/route/cursor/live
evidence, signs canonical arguments, and sends. Success calls the existing atomic
`QueueRepo.MarkDelivered`, which rechecks the same workspace immediately before the
complete-key update. A conflict opening in flight leaves the exact row pending. After
resolution, retry sends the identical operation ID and bytes. No SQLite transaction or
workspace lock crosses the network.

Bootstrap installs complete binding, cursors, and validated initial Activity policy in
one local transaction. Missing/malformed/unbounded policy disables remote Activity but
does not block local portable work. Recovery or quarantine rows cannot construct v2
requests.

Fabric production assembly retains and injects the Task-5 observer, Fabric UUID and
observer credential, StreamStore, ActivityStore, identity store, public resolver/session
store, and mutation coordinator. Its public registry exposes only v2 sync/session and
four Activity tools. The private registry may retain unrelated private tools, but sync is
still v2-only.

Gateway production assembly constructs one routed v2 engine and the landed Activity
transport from shared route, conflict, queue, Activity, signer, credential, client, and
accepted-tree importer owners. Task 7 adds user-facing attach/detach by consuming this
live transport; it does not introduce another client or policy owner.

## Safe failures

Once a tool is identified, an MCP failure body is canonical JSON of exactly:

```json
{"code":"authentication_failed","operation":"wormhole.sync.push"}
```

There is no detail or cause field. Sync/auth codes are limited to `invalid_request`,
`unknown_version`, `authentication_failed`, `permission_denied`,
`attachment_not_found`, `sync_precondition_failed`, `sync_conflict`,
`sync_replay_conflict`, `sync_observer_unavailable`, and `internal_error`. The eight
Activity codes are the only Activity additions. Standard JSON-RPC numeric errors remain
only when dispatch cannot identify an operation.

Errors never expose proofs, keys, sessions, attachment/route IDs, repository evidence,
operations, Activity payload/note, policy/actor/credential evidence, paths, or wrapped
causes.

## Implementation slices

Task 6 is one cohesive delivery range with eight sequential checkpoint slices:

1. Destructive v2 contract cut: delete v1 and freeze strict schemas, proof wrapper,
   errors, Activity descriptors, docs, and contract inventory.
2. Migration/auth foundation: migration 22, resolver, proof crypto, activation, nonce,
   sessions, and forced-RLS tests.
3. Shared transaction/Core seams: typed audit coordinator, attachment/precondition,
   exact-ref observation, stable actor matching, conflict resolution, and Activity
   transaction adapters.
4. Attach/bootstrap/pull handlers: observer, human activation, finite policy, complete
   state, source refs, branch isolation, and rollback.
5. Push/conflict handlers: shared reducer, exact replay, every signed precondition,
   resolution races, attribution, and audit atomicity.
6. Activity MCP/client adapter: four tools, policy handshake, historical evidence,
   lifecycle, eight safe codes, and zero promotion/portable crossover.
7. Gateway durability: proof signing, local install transaction, import/cursor/conflict/
   queue behavior, byte-identical retry, and both conflict checks.
8. Production assembly: Fabric/Gateway constructors, configuration, docs, contract and
   migration CI, and whole-range verification.

Each behavior begins RED, reaches focused GREEN, and receives affected/forced-RLS/race
coverage. The final range must pass migration up/down and role tests, security/redaction
matrices, contract determinism, vet, race, `make check`, and at least 80% merged statement
coverage. One independent whole-Task review gates completion; all Critical and Important
findings are fixed and re-reviewed.

## Non-goals

- No sync-v1 compatibility or legacy decoder.
- No WebAuthn, agent private-key store, local Git credential helper, Code Graph, ORM,
  framework, datastore, or new join/connect flow.
- No Fabric Activity promotion or portable/Activity state mixing.
- No public creation of projects, ownership, or private membership from Git content.
- No source bodies in Fabric and no private keys/tokens in Gateway routing state.
