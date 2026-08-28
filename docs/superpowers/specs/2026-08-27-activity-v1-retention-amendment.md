# ActivityV1 Finite-Retention and Private-Schema-v8 Amendment

**Status:** Human-authorized focused amendment  
**Date:** 2026-08-27  
**Scope:** Stage 3 Task 3 and the ActivityV1 seams consumed by later Stage 3 tasks

**Approved contract correction (2026-08-28):** each Activity pull batch carries one
deduplicated canonical historical-policy evidence record for every distinct policy
version named by a returned receipt. Gateway strict-validates and atomically
insert-or-exact-replays those immutable versions with the batch evidence and cursor,
while historical evidence itself can never advance the current-policy pointer. When the
separately returned current policy is newer, its expected-old CAS occurs in that same
transaction. Missing, corrupt, changed, duplicate-conflicting, or wrong-route evidence
rolls back the current-policy CAS, historical-policy inserts, Activity, lifecycle,
receipt, and cursor changes together. This correction is part of the current Task-3
contract. Every returned receipt and historical-policy evidence version must be less
than or equal to the separately returned current policy version; a future version is a
replay/policy conflict rejected before mutation. Dated implementation and review
evidence remains historical.

## 1. Decision and authority

This amendment approves approach B: an immutable `ActivityV1` ledger, an immutable
ingress receipt, and separate mutable delivery/lifecycle state. Operational activity
remains distinct from `OperationV1`, portable trees, and accepted-stream authority.

It implements the finite-retention decisions in RFC-0003 §§6.3 and 8.5 and closes the
hard gate before Task 3 in the Multi-Fabric plan. It narrowly amends:

- the retained migration-21 design;
- the Gateway private-format epoch, advancing exact schema v7 to exact schema v8;
- the strict ActivityV1, policy, receipt, replay, cursor, retention, and promotion
  interfaces required by Stage 3.

The actual PostgreSQL baseline is migration
`000019_kb_semantic_embeddings` followed by
`000020_integration_manifests`. Migration 000020 owns integration-manifest lineage,
version, immutability, revocation, and RLS objects. It is not a publication-policy
migration. Migrations 000001 through 000020 remain byte-for-byte unchanged. The new
PostgreSQL pair remains `000021_git_aware_streams.{up,down}.sql`; later migration
numbers remain unchanged. Migration 21 down must restore exactly the version-20 shape,
including all integration-manifest objects from migration 20.

Git remains the sole acceptance authority for portable state. Activity expiry cannot
advance a stream, modify a canonical tree, create an `EventV1`, apply an
`OperationV1`, observe or accept a Git ref, or mark any portable proposal accepted.

## 2. Goals and non-goals

This amendment freezes:

- a closed, strictly decoded, canonical ActivityV1 record and digest;
- a closed effective finite-policy record and digest;
- complete route keys and immutable replay receipts;
- restart-discarded presence and durable ordinary/lifecycle activity;
- deterministic age-or-cap pruning and finite lifecycle retention;
- PostgreSQL migration-21 Activity tables, RLS, composite foreign keys, privileges,
  transactions, and pruner ownership;
- the consolidated Gateway private schema v8, durable queues, cursors, lifecycle rows,
  and promotion receipts; and
- the one Gateway-local ProjectState promotion seam.

It does not:

- add a new dependency or top-level package;
- reinstate a numbered SQLite migration runner or any private compatibility path;
- change the tracked `.wormhole/state/v1/` schema or canonical portable codecs;
- turn ActivityV1 into a portable stream transition or use stream version as an
  activity cursor;
- add Fabric promotion state, procedures, triggers, endpoints, or authority;
- change the five approved EventV1 event types;
- add indefinite retention, legal-hold, archival, export, or server-side search; or
- implement Task 4 portable reconstruction, Task 5 GitHub observation, Task 6 public
  proof descriptors, Task 8 private identity, or later Stage 3 features.

## 3. Rejected alternatives

One mutable activity row is rejected because it mixes immutable source evidence with
retry, lifecycle, and retention state. Treating activity as a versioned portable stream
transition is rejected because it would make expiry part of accepted history. A
Gateway-only in-memory queue is rejected because offline ordinary activity must survive
restart. Incremental v7-to-v8 SQLite migration is rejected by the approved private-format
hard-cut policy.

## 4. Complete route authority

Activity bytes contain no project, Fabric, stream, ref, workspace, attachment, profile,
credential, or cursor field. Those values are transport and persistence authority and
must be resolved from an authenticated attachment plus Gateway's immutable binding, not
accepted from Activity JSON.

The Gateway complete binding key is:

```text
(local_project_id,
 local_workspace_id,
 fabric_instance_id,
 remote_project_id,
 stream_id,
 canonical_ref)
```

Every value is non-empty. The first five identifiers are canonical non-nil lower-case
UUID strings. `canonical_ref` is the exact binding ref and matches
`^refs/heads/[A-Za-z0-9._/-]+$`; `//`, trailing `/`, `.` and `..` path components are
invalid. The ref must equal both the immutable Gateway binding and the Fabric stream ref.

An Activity origin adds `source_workspace_id`. The authoritative idempotency key is:

```text
(remote_project_id,
 fabric_instance_id,
 stream_id,
 canonical_ref,
 source_workspace_id,
 activity_id)
```

PostgreSQL uses its tenant `project_id` in the `remote_project_id` position. Gateway
inbound storage prefixes that origin key with its complete local binding key. Gateway
outbound storage requires `source_workspace_id == local_workspace_id`. A pull cursor is
owned by the destination Gateway binding, not by an Activity origin.

The existing `types.RemoteBindingKey` remains valid for portable sync. Activity code
adds these exact plain values beside it in `internal/types/routing.go` rather than
weakening or silently reinterpreting the Task-1 type:

```go
type ActivityRouteKey struct {
    ProjectID        string
    WorkspaceID      WorkspaceID
    FabricInstanceID string
    RemoteProjectID  string
    StreamID         string
    CanonicalRef     string
}

type ActivityOriginKey struct {
    Route             ActivityRouteKey
    SourceWorkspaceID WorkspaceID
    ActivityID        string
}
```

`ActivityRouteKey.Validate` enforces the complete binding rules above.
`ActivityOriginKey.Validate` additionally requires canonical source-workspace and
Activity UUIDs. Neither type has JSON tags or is accepted as public request data. They
may not contain or infer a profile, repository, attachment, credential, token, secret,
URL, actor, policy, cursor, or mutable state. `fabric_profiles.credential_ref` remains
the sole credential reference.

## 5. Closed wire records

The following types live in `internal/types/projectstate/activity.go`. This location
reuses the existing strict canonical-JSON authority only; ActivityV1 is not added to
`Snapshot`, `Tree`, `RecordValueV1`, `OperationV1`, `ApplyOperation`, or any reducer.

```go
type ActivityClassV1 string

const (
    ActivityPresenceV1  ActivityClassV1 = "presence"
    ActivityOrdinaryV1  ActivityClassV1 = "ordinary"
    ActivityLifecycleV1 ActivityClassV1 = "lifecycle"
)

type ActivityLifecycleKindV1 string

const (
    ActivityLifecycleDeliveryV1 ActivityLifecycleKindV1 = "delivery"
    ActivityLifecycleConflictV1 ActivityLifecycleKindV1 = "conflict"
    ActivityLifecycleRecoveryV1 ActivityLifecycleKindV1 = "recovery"
    ActivityLifecycleReceiptV1 ActivityLifecycleKindV1 = "receipt"
)

type ActivityLifecycleProjectionV1 struct {
    Kind        ActivityLifecycleKindV1 `json:"kind"`
    ReferenceID string                  `json:"reference_id"`
}

type ActivityEventProjectionV1 struct {
    ChannelID string          `json:"channel_id"`
    ActorID   string          `json:"actor_id"`
    EventType string          `json:"event_type"`
    Payload   json.RawMessage `json:"payload"`
    Note      *string         `json:"note"`
    CreatedAt time.Time       `json:"created_at"`
}

type ActivityV1 struct {
    SchemaVersion int                            `json:"schema_version"`
    ID            string                         `json:"id"`
    Class         ActivityClassV1                `json:"class"`
    Actor         types.ActorEnvelope            `json:"actor"`
    Event         *ActivityEventProjectionV1     `json:"event,omitempty"`
    Lifecycle     *ActivityLifecycleProjectionV1 `json:"lifecycle,omitempty"`
    CreatedAt     time.Time                      `json:"created_at"`
}

type EffectiveActivityPolicyV1 struct {
    SchemaVersion              int   `json:"schema_version"`
    PolicyVersion              int64 `json:"policy_version"`
    OrdinaryMaxAgeSeconds      int64 `json:"ordinary_max_age_seconds"`
    OrdinaryMaxRows            int64 `json:"ordinary_max_rows"`
    TerminalDefaultAgeSeconds  int64 `json:"terminal_default_age_seconds"`
    TerminalMaximumAgeSeconds  int64 `json:"terminal_maximum_age_seconds"`
    TerminalRetentionSeconds   int64 `json:"terminal_retention_seconds"`
}

type ActivityReceiptV1 struct {
    SchemaVersion int    `json:"schema_version"`
    ActivityID    string `json:"activity_id"`
    ActivityDigest Digest `json:"activity_digest"`
    Sequence      int64  `json:"sequence"`
    PolicyVersion int64  `json:"policy_version"`
    PolicyDigest  Digest `json:"policy_digest"`
    AcceptedAt    time.Time `json:"accepted_at"`
}
```

All three records are closed. Decoders use `json.Decoder.DisallowUnknownFields`, require
one JSON value followed by EOF, validate the typed value, reproduce bytes through the
existing `CanonicalJSON`, and require byte equality. Canonical bytes therefore use struct
field order, recursively byte-sorted map keys, compact JSON, canonical `json.RawMessage`,
standard UTC RFC3339Nano time encoding, and one final newline. Nil and empty values are
not normalized. A decoder never accepts semantically equivalent noncanonical bytes.

The Activity digest is exactly:

```text
"sha256:" + lowercase_hex(SHA-256(canonical_activity_bytes))
```

Policy digests use the same rule over canonical policy bytes. Receipt bytes are strict
and canonical but do not introduce a second receipt-digest field.

### 5.1 Activity validation

- `schema_version` is exactly `1`.
- `id` and every lifecycle `reference_id` are canonical non-nil lower-case UUIDs.
- `actor.ValidateHistorical()` succeeds; new activity rejects `legacy` and `unknown`
  assurance. A local-only record requires `ValidateLocalAction`. Activity queued to a
  remote requires an already issued public-key-continuity or private-authenticated
  envelope for that route; Gateway never upgrades a local envelope or rewrites actor
  bytes after Activity construction.
- `created_at` is non-zero UTC and equals `actor.occurred_at` exactly.
- `presence` has neither `event` nor `lifecycle`.
- `ordinary` has exactly one `event` and no `lifecycle`.
- `lifecycle` has exactly one `lifecycle`; it may have one complete `event` projection or
  no event projection. Absence means it is not promotable.
- An event projection has canonical UUID `channel_id` and `actor_id`; `actor_id` equals
  `actor.PrincipalID()`, and its `created_at` equals top-level `created_at`.
- `event_type` is exactly one of `task.status_changed`, `review.requested`,
  `build.failed`, `discovery.logged`, or `message.posted`.
- Payload is a canonical JSON object strictly decoded into the corresponding existing
  `internal/types` payload (`TaskStatusChangedPayload`, `ReviewRequestedPayload`,
  `BuildFailedPayload`, `DiscoveryLoggedPayload`, or `MessagePostedPayload`). Unknown,
  missing, wrong-typed, or empty required string fields reject the Activity.
- `TaskStatusChangedPayload` uses statuses from exactly `todo`, `wip`, `blocked`, and
  `done`, with distinct `from_status` and `to_status` and canonical UUID `task_id`.
- `message.posted` requires a non-nil, non-empty, trim-stable note. Other event types may
  use a canonical optional note; no event may contain NUL or invalid UTF-8.
- No validation path logs or returns Activity bytes, payload, note, actor envelope,
  credential data, or a complete route.

Fabric never trusts the embedded actor as authorization. Before ingress it derives the
actor from public-key continuity or private authenticated scope, canonicalizes both, and
requires exact equality. The stored actor is the exact issuer-derived object already
present in the canonical Activity. A local Gateway binds its own local actor only for
local-only Activity; absent remote-issued scope blocks remote queueing. Any mismatch is actor forgery and
produces no ledger, receipt, sequence, lifecycle, cursor, or audit mutation.

### 5.2 Effective policy validation

The exact V1 constants are:

```text
schema_version                    = 1
ordinary_max_age_seconds          = 2,592,000  (30 x 24 x 60 x 60)
ordinary_max_rows                 = 10,000
terminal_default_age_seconds      = 2,592,000
terminal_maximum_age_seconds      = 31,536,000 (365 x 24 x 60 x 60)
1 <= policy_version               <= 9,007,199,254,740,991
2,592,000 <= terminal_retention_seconds <= 31,536,000
```

There is no zero, null, negative, omitted, inherited, unlimited, `forever`, or
server-default wire value. The ordinary age and cap and the terminal default/maximum
cannot vary within schema version 1. `terminal_retention_seconds` is the effective
configured value: exactly the default or a longer whole-second value within the maximum.
Unknown schema version, unknown field, arithmetic overflow, malformed canonical bytes,
or a value outside these constants rejects the policy.

### 5.3 Receipt validation

A durable receipt is created only for a persisted `ordinary` or `lifecycle` Activity.
Presence has no durable receipt. Receipt version is 1; IDs and digests are canonical;
sequence and policy version are in `1..9,007,199,254,740,991`; `accepted_at` is non-zero
UTC. The receipt Activity ID/digest and policy version/digest must exact-match the ledger
and effective policy used in the ingress transaction. Receipt data is response evidence,
not bearer authority.

## 6. Policy source, publication, and changes

Fabric is the sole source of remote effective activity policy. A versioned policy row is
scoped to `(project_id, fabric_instance_id, stream_id, canonical_ref)`. New streams begin
at policy version 1 with the V1 constants and the configured finite terminal retention;
absence of an override means exactly 2,592,000 seconds.

Policy versions are immutable. A trusted human control-plane change locks the stream's
current-policy row, validates the complete next policy, requires
`next.policy_version == current+1`, inserts its canonical bytes and digest, and advances
the current pointer in one transaction. Repeating the exact requested version and bytes
returns the existing policy; a version or byte mismatch conflicts. Task 3 adds the store
transaction, not a new public policy-management tool.

Attach and every activity bootstrap response carries the current closed policy. Gateway
stores every observed canonical policy version plus one current-version pointer under its
complete binding key. Before queueing, sending, accepting, or exposing remote Activity it
revalidates the relevant bytes and binding. Before every network send/pull cycle it
resolves the current route,
profile, credential, and policy. Missing, malformed, unknown, or unbounded policy disables
only remote activity for that binding; local portable operations, status, diff, import,
checkpoint, stash, and local presence continue.

Every ingress request carries the policy version and digest last observed for the
authenticated attachment. Fabric accepts only the exact current pair. A stale pair
returns `activity_policy_changed` plus the current canonical policy and performs no
Activity mutation. Gateway validates and atomically replaces its current policy row,
updates only the mutable expected-policy fields of still-pending outbound queue rows, and
retries the same Activity ID, canonical bytes, and digest. It never rewrites activity or
source attribution.

Existing accepted Activities and lifecycle rows retain the policy version, digest, and
terminal-retention seconds captured at ingress/lifecycle creation. A later policy change
is not retroactive. This prevents pruning deadlines from changing beneath a pending
transaction while keeping every deadline finite.

Promotion of an already accepted, retained source is a local ProjectState operation. It
validates the immutable policy version/digest captured by that source receipt, but does
not require a live Fabric call or a still-current remote policy. Policy outage therefore
cannot block curation of evidence that Gateway already accepted under a valid policy.

For pull, Fabric joins every returned receipt to the immutable policy version under
which that Activity was accepted. The batch carries the canonical policy bytes and
digest once for every distinct receipt policy version, deterministically ordered by
policy version. This historical evidence is route-bound response evidence, never bearer
authority and never a request-selected route. Fabric fails the pull rather than emit a
delivery whose policy row is missing, malformed, changed, belongs to another route, or
names a version greater than the current policy captured by that same pull transaction.

## 7. Lifecycle and retention model

Presence is memory-only. It may be validated and transmitted live after policy validation,
but neither Gateway nor Fabric writes it to SQLite/PostgreSQL, assigns a durable sequence,
queues it, receipts it, replays it, exposes it through a durable cursor, or promotes it.
It disappears on process restart or network loss. Local eventbus presence does not require
Fabric policy; only remote presence send/accept/exposure does.

Ordinary and lifecycle Activity ledger rows are immutable. Mutable lifecycle rows are
separate and have one of these exact state machines:

| Kind | Nonterminal states | Terminal states | Allowed transitions |
|---|---|---|---|
| `delivery` | `pending` | `delivered`, `cancelled` | `pending` to either terminal state |
| `conflict` | `open` | `resolved`, `cancelled` | `open` to either terminal state |
| `recovery` | `pending`, `blocked` | `recovered`, `cancelled` | `pending` to `blocked`, `recovered`, or `cancelled`; `blocked` to `pending`, `recovered`, or `cancelled` |
| `receipt` | `pending` | `confirmed`, `rejected`, `cancelled` | `pending` to one terminal state |

Ingress of `class=lifecycle` creates exactly one row from its embedded projection with
the initial state `pending` for delivery/recovery/receipt and `open` for conflict. An
outbound queued Activity of either durable class additionally creates a
`delivery/pending` row whose reference ID is the Activity ID. An ordinary inbound
Activity creates no lifecycle row unless a separate trusted operation explicitly binds
one of the four lifecycle claims.

An Activity can have multiple lifecycle rows, keyed by lifecycle kind and reference ID.
Any nonterminal row protects the Activity, its ingress receipt, and related queue evidence
from all age/rank pruning. A same-state or same-terminal replay returns the stored row;
changing a terminal state/time or using a forbidden edge is a lifecycle conflict.

On the first terminal transition, the transaction captures one UTC `terminal_at` and sets
`expires_at = terminal_at + captured_terminal_retention_seconds`. It never accepts either
timestamp from an untrusted request. All lifecycle rows must be terminal and the current
time must be at or after the greatest `expires_at` before the Activity can be deleted.
Protected rows, including blocked recovery, may exceed the ordinary cap.

The immutable Activity ingress receipt defined here is a replay companion, not by itself
a separate nonterminal lifecycle claim. An ordinary Activity with no lifecycle rows is
unprotected and its ledger and receipt are pruned together under the ordinary rule. A
separately modeled pending receipt uses a `receipt` lifecycle row and is protected by the
state machine above.

### 7.1 Ordinary eligibility

For each complete Activity-origin workspace key, consider only `ordinary` rows with no
lifecycle rows. A row is eligible when either:

1. `observed_now >= created_at + 2,592,000 seconds`; or
2. its one-based rank is greater than 10,000 when unprotected ordinary rows are ordered
   newest first by `(created_at DESC, activity_id DESC)`.

The pruner deletes eligible rows oldest first by `(created_at ASC, activity_id ASC)`, with
a required batch limit from 1 through 1,000. Age and rank are an OR, not an AND. Timestamp
ties are always broken by the canonical Activity UUID text. Lifecycle rows never
participate in ordinary rank. A paired receipt is deleted in the same transaction before
its ledger parent. No partially deleted pair is observable.

### 7.2 Terminal eligibility

An Activity with lifecycle rows is eligible only when every row is terminal and
`observed_now >= max(expires_at)`. Terminal rows do not enter ordinary rank even when the
underlying Activity class is `ordinary`. The pruner deletes queue/lifecycle children,
ingress receipt, then ledger in one transaction. A failure rolls back every deletion.

Pruning establishes only a finite idempotency window. After a ledger/receipt pair has
been validly pruned, reuse of its complete idempotency key is not remembered and can be
accepted as new. No API promises replay recognition beyond retained evidence.

## 8. Gateway private schema v8

### 8.1 Hard cut

Gateway advances `GatewaySchemaVersion` from 7 to 8 and replaces the consolidated
`private_schema_v7.sql` snapshot with `private_schema_v8.sql`. Version 8 is a complete
format epoch, not migration 8.

The current binary supports exactly:

1. a missing/empty database with no sidecar evidence, initialized atomically to the
   complete v8 shape and singleton ledger `{8}`; or
2. an exact v8 database, reopened after read-only exact-schema/proof validation without
   schema mutation.

Every other existing database, including exact former v7, is copied/read only for
classification, preserved byte-for-byte with all sidecars, and refused before mutation.
There is no v7 reader, exporter, converter, reset, quarantine, normalization, numbered
migration directory, or compatibility alias. Failed fresh initialization rolls back to
the fresh preimage and leaves no partial schema. The unsupported-format error retains the
R06 operator guidance to inspect unpublished work and back up before deliberate manual
removal.

Current Code Graph catalog handling remains orthogonal: an exact optional Code Graph
catalog is accepted only alongside an exact v8 Gateway catalog under its existing
component ledger rules. Activity tables never enter the Code Graph catalog.

### 8.2 Required v8 Activity tables

All route columns below are `TEXT NOT NULL`; integer versions/sequences use SQLite
`INTEGER` with explicit safe-range checks; canonical JSON is stored as `BLOB` so no text
normalization can change bytes. Each child has a composite foreign key including every
listed parent key and `ON DELETE` behavior stated below.

`activity_policy_versions`

- complete Gateway binding key plus `policy_version` primary key;
- `policy_version`, `canonical_policy_json`, `policy_digest`,
  `terminal_retention_seconds`, and `received_at`;
- complete-key foreign key to the active `workspace_fabric_bindings` row, whose v8
  unique key now includes `canonical_ref`;
- immutable after insertion.

`activity_policy_current`

- complete Gateway binding key primary key;
- `policy_version`, `policy_digest`, and `updated_at`;
- complete composite foreign key to `activity_policy_versions` and to the active binding.

`activity_ledger`

- complete Gateway binding key plus `source_workspace_id, activity_id` primary key;
- `activity_class`, `canonical_activity_json`, `activity_digest`,
  `source_actor_json`, exact nullable event projection columns, exact nullable embedded
  lifecycle projection columns, `created_at`, `accepted_at`, and nullable server
  `sequence`;
- outbound rows require source workspace equal local workspace; inbound rows carry the
  source workspace returned by Fabric;
- uniqueness of complete key plus activity digest and of complete key plus non-null
  sequence.

`activity_ingress_receipts`

- the full ledger primary key;
- `activity_digest`, `sequence`, `policy_version`, `policy_digest`, and `accepted_at`;
- complete composite foreign key to `activity_ledger` with `ON DELETE CASCADE`.

`activity_lifecycle`

- full ledger primary key plus `lifecycle_kind, reference_id` primary key suffix;
- `state`, captured policy version/digest/terminal-retention seconds, nullable
  `terminal_at`, nullable `expires_at`, and `updated_at`;
- SQL checks encode the exact per-kind states and require both terminal timestamps
  together only for terminal states;
- complete composite foreign key to ledger with `ON DELETE CASCADE`.

`activity_outbound_queue`

- the full ledger primary key;
- `state` exactly `pending` or `delivered`, `expected_policy_version`,
  `expected_policy_digest`, immutable `created_policy_version` and
  `created_policy_digest`, nonnegative `attempt_count`, `next_attempt_at`, `created_at`,
  `updated_at`, and nullable `delivered_at`;
- pending rows have no delivered timestamp; delivered rows have one;
- complete composite foreign key to ledger with `ON DELETE CASCADE`.

`activity_cursors`

- complete Gateway binding key primary key;
- `after_sequence` initialized to zero and constrained to the JSON-safe integer range;
- `updated_at`; complete-key binding foreign key with `ON DELETE CASCADE`.

`activity_promotion_receipts`

- `(local_project_id, local_workspace_id, source_activity_id)` primary key;
- the complete source ledger key and exact source digest;
- generated `event_id`, `operation_id`, canonical promoter envelope, and `promoted_at`;
- unique `(local_project_id, local_workspace_id,event_id)` and operation ID;
- complete composite source-ledger foreign key with `ON DELETE RESTRICT` while the
  receipt is retained. Promotion-receipt expiry removes the receipt before ordinary
  source pruning can proceed.

The v8 private-schema fingerprint, required-table inventory, required-column inventory,
exact-ledger validation, failure-injection seam, error text, README, compatibility docs,
agent context, and tests all advance from v7 to v8. Dated R06 and Stage-3-v7 historical
records remain historical and are not rewritten to pretend v8 existed earlier.

### 8.3 Gateway transactions

All durable mutations use the one-daemon private DB and `BEGIN IMMEDIATE`.

**Queue ordinary/lifecycle Activity:** lock the writer; load the exact active binding and
current validated policy; validate/canonicalize Activity and digest; exact-replay the
ledger row or reject changed bytes; insert ledger, required lifecycle rows, outbound
queue, and `delivery/pending` lifecycle protection; commit. Any error leaves no row.
Presence stops before the transaction.

**Acknowledge outbound delivery:** load every complete key; require the receipt to match
Activity/digest and one retained, strictly validated policy version for that binding;
insert or exact-replay the immutable receipt;
transition queue to delivered and its delivery lifecycle to delivered with one captured
terminal time; commit. An unknown/changed receipt or wrong route rolls back.

**Accept pull batch:** strict-validate the effective current policy; every delivery,
receipt, Activity, digest, origin workspace, and ascending sequence; and one
deduplicated canonical historical-policy evidence record for every distinct receipt
policy version before writing. Reject missing evidence, extra duplicate-conflicting
evidence, digest/version mismatch, evidence from another complete route, or any receipt
or evidence version greater than the separately returned current policy version. In one
immediate transaction, insert-or-exact-replay the immutable historical policy versions
without deriving any current-pointer change from that evidence; if the separately
returned current policy differs, CAS `activity_policy_current` from its expected old
version/digest and update only pending queue policy expectations in the same transaction;
exact-replay or insert all ledger/receipt/lifecycle rows; then advance
`activity_cursors.after_sequence` from the exact expected old value to the batch result's
`next_sequence`. A duplicate batch is read-only idempotent. Any invalid item, policy
replay conflict, insert conflict, or cursor CAS failure rolls back the current pointer,
pending queue expectations, historical-policy rows, the whole batch, and cursor.

**Change effective policy after server response:** require exact old current
version/digest, strict-validate the next policy, insert or exact-replay its immutable
version row, advance the current pointer, and update only expected policy version/digest
on pending queue rows. Created-policy fields, Activity, and lifecycle retention fields do
not change.

**Prune:** use one immediate transaction per complete source-workspace route, recompute
eligibility in the transaction, select in the stable order, delete only eligible child
sets and ledger rows, and commit. Promotion and prune serialize on the same writer; a
source cannot disappear between promotion validation and receipt insertion.

## 9. PostgreSQL migration 000021

Migration 21 still creates the retained portable repository binding, stream/version,
workspace binding, request, conflict, public-key, and nonce tables required by Tasks 4–7.
The portable definitions from the prior plan remain design input except where this
amendment requires the workspace/ref composite uniqueness and the universal forced-RLS
rules below. Activity never enters `fabric_stream_versions` or
`fabric_stream_requests`.

### 9.1 Required Activity relations

All relations are project-scoped. Every primary/unique/composite foreign key lists
`project_id` first.

`fabric_activity_policy_versions`

- key `(project_id,fabric_instance_id,stream_id,canonical_ref,policy_version)`;
- canonical policy `bytea`, digest, every numeric policy field, `created_at`;
- composite FK to the exact `fabric_streams` project/instance/stream/ref identity;
- immutable update trigger.

`fabric_activity_policy_current`

- key `(project_id,fabric_instance_id,stream_id,canonical_ref)`;
- `policy_version`, `updated_at`;
- complete FK to a policy-version row and stream.

`fabric_activity_stream_sequences`

- key `(project_id,fabric_instance_id,stream_id,canonical_ref)`;
- `high_watermark` in `0..9,007,199,254,740,991`;
- complete FK to stream; changed only in the ingress transaction.

`fabric_activities`

- key `(project_id,fabric_instance_id,stream_id,canonical_ref,
  source_workspace_id,activity_id)`;
- positive per-stream `sequence`, unique with the complete stream/ref key;
- class constrained to `ordinary` or `lifecycle`; presence is impossible;
- canonical Activity bytes/digest, canonical source-actor bytes, exact nullable event
  projection columns, exact nullable embedded-lifecycle projection columns,
  Activity `created_at`, and database `accepted_at`;
- complete FK through `(project_id,fabric_instance_id,stream_id,
  source_workspace_id,canonical_ref)` to the workspace binding;
- immutable update trigger. No generic direct delete grant exists.

`fabric_activity_ingress_receipts`

- the full Activity key;
- exact digest, sequence, policy version/digest, and `accepted_at`;
- complete FKs to the Activity and policy-version row;
- immutable update trigger. It is deleted only with its Activity by the pruner.

`fabric_activity_lifecycle`

- full Activity key plus `(lifecycle_kind,reference_id)`;
- exact state machine, captured policy version/digest/terminal-retention seconds,
  nullable terminal/expiry timestamps, and updated timestamp;
- complete FKs to Activity and policy-version row;
- direct update/delete denied to ordinary runtime; only the lifecycle/pruner functions
  mutate it.

No Fabric table has `promoted`, `promotion_id`, `event_id`, `operation_id`, or promoter
columns for Activity. Migration 21 creates no Fabric promotion function or trigger.

### 9.2 Composite constraints and RLS

`fabric_streams` gains a unique identity including ref. The workspace binding gains:

```text
UNIQUE(project_id,fabric_instance_id,stream_id,workspace_id,ref_name)
```

Every Activity foreign key uses those composite identities; independent single-column
foreign keys are insufficient. Direct cross-project, cross-Fabric, cross-stream,
cross-ref, or cross-workspace inserts fail with SQLSTATE `23503` before a row exists.

Every migration-21 project table, including policy-current/version, sequence, Activity,
receipt, and lifecycle, has `ENABLE ROW LEVEL SECURITY`, `FORCE ROW LEVEL SECURITY`, and
one project policy using:

```sql
USING (project_id = NULLIF(current_setting('wormhole.project_id',true),'')::uuid)
WITH CHECK (project_id = NULLIF(current_setting('wormhole.project_id',true),'')::uuid)
```

Store transactions set the project GUC locally before any read or write. Acceptance tests
use an ordinary non-superuser, non-`BYPASSRLS` role genuinely subject to forced RLS.

### 9.3 Function and role ownership

Deployment provisions these exact Activity-specific roles before migration 21; the
migration validates that none is superuser or `BYPASSRLS` and does not create, alter, or
drop cluster roles:

- `wormhole_activity_owner`: `NOLOGIN`, owns only the Activity policy, sequence, ledger,
  receipt, and lifecycle relations/functions;
- `wormhole_fabric_runtime`: the Fabric process login role; and
- `wormhole_activity_maintenance`: `NOLOGIN`, assumed only by the bounded maintenance
  job that invokes pruning.

`wormhole_activity_owner` owns these exact security-definer functions:

- `fabric_accept_activity_v1` — exact replay or one new ledger/receipt/sequence insert;
- `fabric_transition_activity_lifecycle_v1` — one allowed lifecycle transition;
- `fabric_prune_activities_v1` — bounded eligible deletion; and
- `fabric_publish_activity_policy_v1` — immutable next policy version/current-pointer
  CAS.

Each function sets a fixed `search_path` of `pg_catalog,public`, validates non-null
complete keys, and remains subject to the transaction's project GUC and forced RLS.
Execution is revoked from `PUBLIC`. `wormhole_fabric_runtime` receives Activity-table
`SELECT` and execute on accept, transition, and policy publication; authorization before
policy publication remains the trusted human control-plane check. It receives no direct
sequence/current-policy/ledger/receipt/lifecycle mutation privilege.
`wormhole_activity_maintenance` receives execute on the pruner only and no table DML.
The migrator can create/alter/drop schema objects and transfer ownership but is never the
Fabric process credential. Tests provision temporary equivalents and prove owner,
runtime, and maintenance ACLs under forced RLS.

### 9.4 PostgreSQL transaction order

Every mutation starts a project-scoped transaction and locks the exact
`fabric_workspace_stream_bindings` row. This serializes ingress, terminalization, and
pruning for one source workspace without blocking sibling routes.

**Ingress:** reconcile actor and strict-decode before SQL; lock binding and current
policy; require request policy version/digest exact. If the complete Activity key exists,
lock ledger/receipt and return the stored receipt only when canonical bytes and digest
match exactly. Any mismatch returns replay conflict. For a new key, lock the stream
sequence row, fail closed at the safe-integer maximum, increment once, insert immutable
ledger and receipt and required lifecycle rows, and commit. Concurrent duplicate insert
unique failure is re-read in the same semantic path; it never consumes a second durable
sequence or returns a fabricated receipt.

**Lifecycle transition:** lock binding, Activity, and selected lifecycle row; compare
expected state; apply one allowed edge; capture database transaction time once for the
first terminal transition and calculate expiry from captured policy seconds; update only
the lifecycle row; commit. Exact retry returns stored state.

**Prune:** lock binding; capture database time once; recompute ordinary rank and terminal
eligibility in the transaction; lock the bounded candidate rows in
`(created_at,activity_id)` ascending order; recheck that no nonterminal lifecycle exists;
delete lifecycle and ingress receipts before ledger; commit.
Ingress and terminalization on the same origin cannot interleave after the binding lock.
Sibling workspaces may prune concurrently.

## 10. Replay, delivery, and cursor protocol

The durable ingress idempotency key is the complete origin key from §4. For a new key,
the request policy must be the exact current version/digest. For an existing key, the
request must still present the current policy, but exact same canonical Activity bytes and
digest return the original receipt—including its originally captured policy—without a new
row, sequence, lifecycle state, or timestamp. Same key with any changed canonical byte or
digest returns `activity_replay_conflict` and preserves existing evidence. Gateway keeps
observed immutable policy versions so it can validate such an original receipt after the
current policy advances.

Sequences are positive monotonically increasing safe integers allocated per
`(project_id,fabric_instance_id,stream_id,canonical_ref)` across all source workspaces.
They are Activity cursors only and are unrelated to portable stream version.

A pull request supplies the authenticated attachment, `after_sequence`, and a required
integer `limit` from 1 through 500 only; all route and actor fields remain derived. Fabric
resolves the route, rejects negative or greater-than-high-watermark cursors, snapshots the
stream high watermark, and returns retained Activities with sequence greater than the
cursor ordered ascending, bounded by `limit`. Each delivery
contains source workspace ID, canonical Activity bytes/digest, and exact receipt.
The same response contains canonical historical-policy evidence, deduplicated by policy
version and ordered ascending, for exactly every distinct policy version named by those
receipts. Its separately returned current policy remains the only current-pointer
candidate; historical evidence can never advance that pointer, and no returned receipt
or historical evidence version may be greater than that current policy version.

If another retained row exists after the last returned row, `next_sequence` is that last
row and `has_more=true`. Otherwise `next_sequence` is the captured high watermark and
`has_more=false`; this deliberately advances across gaps created by valid finite pruning.
An empty result therefore advances to the high watermark. Gateway advances its cursor
only after atomically validating and storing the entire batch. Repeating a response is
read-only idempotent.

Pending outbound delivery survives restart and preserves exact Activity bytes. Retry
count/backoff is mutable queue scheduling metadata, never part of Activity or receipt.
Credential/profile resolution happens immediately before each network call. A changed
route, detached binding, open workspace conflict, invalid policy, or missing credential
blocks that binding without marking a row delivered or affecting sibling bindings.

## 11. Gateway-only promotion

Promotion belongs only to `internal/runtime/projectstate`. Its input is the exact local
workspace, source Activity ID, expected source digest, expected portable view digest, and
fresh promoter `ActorEnvelope`. Callers cannot supply EventV1 semantics, source actor,
creation time, extensions, event ID, or operation ID.

One ProjectState-owned SQLite `BEGIN IMMEDIATE` transaction:

1. conflict-gates the workspace and validates the promoter as a local action;
2. strict-reads the exact retained source Activity and canonical digest under the
   complete local binding/origin key;
3. requires a complete event projection and no conflicting promotion receipt;
4. generates stable EventV1 and OperationV1 UUIDs;
5. copies channel ID, source actor ID, event type, deep-owned canonical payload, note,
   and creation time exactly into `EventV1`;
6. creates the event's sole extension `dev.wormhole.promotion`, schema version 1, whose
   data contains only `source_activity_id` and `source_activity_digest`;
7. constructs the enclosing put-record `OperationV1` with the distinct promoter actor
   and expected view digest, using ProjectState's transaction-local append/reducer
   helpers rather than nesting public `ApplyBatch`;
8. inserts the promotion receipt with source, Event, Operation, promoter, and time plus a
   terminal `receipt/confirmed` lifecycle row whose finite retention is captured from the
   source's validated policy; and
9. commits the Activity receipt and portable overlay mutation together.

Exact retry of the same source ID/digest returns the stored Event/Operation result.
Changed source digest or another receipt conflicts. Prune cannot remove the source while
the transaction holds the writer. After the finite promotion-receipt retention ends, the
portable Event/Operation remains normal Git-reviewable state; deleting operational
evidence cannot change its bytes.

Fabric has absolute zero promotion authority. Its migration, store, transport, MCP
registry, functions, and triggers have no method to create EventV1/OperationV1, call
`ApplyOperation`, write Gateway promotion receipts, or mark an Activity promoted. A
portable Operation produced locally may later travel through the existing proposal path,
but only independent Git observation can accept it.

## 12. Stable error contract

Packages wrap, but preserve with `errors.Is`, these semantic sentinels:

- `projectstate.ErrInvalidActivity` — malformed, noncanonical, invalid projection, or
  actor mismatch;
- `projectstate.ErrUnknownActivityVersion` — Activity/policy/receipt schema version;
- `projectstate.ErrInvalidActivityPolicy` — absent, malformed, unbounded, or internally
  inconsistent policy;
- `ErrActivityPolicyUnavailable` and `ErrActivityPolicyChanged`;
- `ErrActivityNotFound`;
- `ErrActivityReplayConflict`;
- `ErrActivityCursorConflict`;
- `ErrActivityLifecycleConflict`; and
- `ErrActivityNotPromotable` / `ErrActivityPromotionConflict` on Gateway only.

Public protocol codes are respectively `invalid_activity`,
`unknown_activity_version`, `activity_policy_required`,
`activity_policy_changed`, `activity_not_found`, `activity_replay_conflict`,
`activity_cursor_invalid`, `activity_lifecycle_conflict`, `activity_not_promotable`, and
`activity_promotion_conflict`. Errors may identify a safe operation and code but must not
include canonical bytes, payload/note, actor envelope, policy bytes, credential/profile
reference, attachment reference, private path, or complete route.

Policy and ordinary Fabric outages are typed per-binding activity failures. They do not
masquerade as portable-state conflict, advance a cursor, or block local portable work.

## 13. Test contract

Implementation begins with focused RED tests and retains every portable migration-21
test. Minimum causal evidence is:

### Codec and policy

- canonical Activity/policy/receipt round-trip and hard-coded digest goldens;
- unknown field, trailing JSON, wrong field order/whitespace, noncanonical payload/time,
  unknown version/class/kind/type, forged actor, wrong event attribution, and malformed
  typed payload rejection;
- every finite policy boundary, safe-integer overflow, zero/null/negative/unbounded value,
  and unknown policy rejection.

### Gateway v8

- fresh/empty atomically initializes exact v8 and exact v8 reopens without schema write;
- exact v7, malformed, partial, future, unsafe-path, sidecar-only, and failed-classification
  evidence is byte-preserved/refused before mutation;
- injected fresh-init failure leaves no partial schema;
- required Activity tables/constraints/fingerprint and absence of a numbered migrations
  directory;
- queue/restart/retry/replay, changed-byte rejection, route/origin isolation, policy CAS,
  cursor batch atomicity, lifecycle state machines, age-or-cap order, protected overflow,
  terminal expiry, and presence restart discard;
- concurrent enqueue/ack/pull/prune/promotion under race tests with exact sibling
  isolation.

### PostgreSQL 000021/store

- upgrade from actual migration 20, full migration up/down, and down exact version-20
  integration-manifest shape;
- portable version tree/operation reconstruction tests retained separately;
- cross-project/Fabric/stream/ref/workspace direct SQL composite-FK rejection;
- forced RLS `USING`/`WITH CHECK` under ordinary owner/runtime roles;
- exact replay returns one original receipt/sequence and changed bytes reject;
- current-policy CAS and stale-policy zero-mutation response;
- direct immutable update/delete privilege rejection;
- age OR cap pruning, `(created_at,activity_id)` order, lifecycle protection, finite
  terminal expiry, bounded batches, and sibling-route concurrency;
- no Activity relation/procedure/trigger/handler containing promotion authority.

### Transport and promotion

- current policy is validated before queue, send, accept, and expose; promotion validates
  the retained source policy without live Fabric contact;
- presence has zero durable SQLite/PostgreSQL rows and vanishes on restart;
- pull gaps/high-watermark semantics and cursor rollback on one invalid batch item;
- v1 observed -> v2 accepted Activity -> v3 current pull, including a fresh Gateway at
  v3, deduplicated historical-policy export, and exact replay without current-pointer
  movement;
- missing, corrupt, changed, duplicate-conflicting, digest/version-mismatched, and
  wrong-route historical pull policy evidence rolls back every new historical policy,
  current-policy CAS, Activity, lifecycle, receipt, and cursor row;
- retry and exact server replay preserve bytes, digest, receipt, and sequence;
- inbound pull and retained exposure bind every Activity actor assurance to the freshly
  resolved profile mode in both directions before mutation or exposure;
- maintenance pruning accepts the exact preserved detached route, while queue, pull,
  lifecycle mutation, pending delivery, and retained exposure remain active-only;
- detach -> expiry -> prune covers ordinary and terminal evidence, preserves
  nonterminal/protected evidence, and leaves sibling routes unchanged;
- Activity transport never invokes reducer/application or advances portable stream;
- promotion exact-copies every source projection field, uses the distinct promoter,
  writes the sole closed extension, and atomically receipts/rolls back at every write;
- Activity expiry before/after promotion leaves portable tree bytes, Git acceptance,
  and resulting promotion bytes unchanged;
- Fabric has no compile-time/runtime promotion seam.

Focused packages pass first, followed by real SQLite/PostgreSQL integration, `-race`,
`make check`, and merged statement coverage of at least 80 percent. Tests may not hide
the new production paths behind build tags merely to preserve coverage.

## 14. Stage and task allocation

Task 3 is amended into one reviewed programme with independently reviewable internal
slices:

1. strict Activity/policy/receipt codecs and digest goldens;
2. PostgreSQL migration 21 plus Activity store, roles, RLS, replay, lifecycle, and pruner;
3. Gateway consolidated v8 plus complete-route ledger/receipt/queue/cursor/lifecycle
   repositories;
4. policy-gated transport service and cursor/retry semantics without public descriptor
   expansion; and
5. Gateway-local ProjectState promotion service and no-Fabric-promotion proof.

The implementation plan must replace the old Task-3 SQL/interface block with these
slices before production implementation. Task 4 remains the first portable-stream task
and cannot start until all Task-3 slices pass independent review. Task 6 later wires the
already-frozen effective policy and separate Activity protocol into its attach/bootstrap
and public/private descriptor branches; it may not redefine these records. Tasks 8 and
later may derive private actors but may not change Activity bytes, idempotency, policy,
retention, or promotion authority.

Documentation changed with implementation includes `docs/db-entities.md`, current
README/agent/implementation/compatibility/operator context, the Stage-3 plan, and SDD
evidence. Dated historical R06 and v7 artifacts remain unchanged. No Task-4+, Stage-4,
Code Graph, reduction R07-R14, unrelated cleanup, or legacy compatibility work is
authorized by this amendment.

## 15. Proportionality and completion gate

The immutable ledger/receipt prevents a retry with changed attributed bytes from being
accepted as the same activity; retries and restart are normal V1 behavior, so a volatile
or inference-based alternative is insufficient. Complete route keys prevent branch and
workspace cross-delivery; multiple workspaces/Fabrics are the Stage-3 trial's primary
case. Lifecycle separation prevents ordinary pruning from deleting pending delivery,
conflict, recovery, or receipt evidence. Finite deadlines and bounded pruning prevent the
same protection from becoming accidental indefinite retention. Fail-closed policy
handling is sufficient for policy uncertainty because portable/local work remains
available. No redundant portable proof or Fabric promotion state is added.

Task 3 is complete only when its amended implementation plan is approved, every slice is
implemented and independently reviewed, v8 and migration-21 causal tests pass, full
verification remains at or above 80 percent coverage, current documentation names v8,
and the branch is pushed at a clean reviewed checkpoint.
