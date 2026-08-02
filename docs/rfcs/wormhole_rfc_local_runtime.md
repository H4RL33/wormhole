# RFC-0003: Wormhole Local Runtime

**Git-native local execution. Optional remote coordination.**

| | |
|---|---|
| Status | Draft — revised architecture, 2026-08-01 |
| Author | Harley |
| Original date | 2026-07-13 |
| Supersedes | Nothing directly; amends RFC-0001 local-runtime, transport, workspace, and optional-coordination assumptions (see §4) |
| Related | [RFC-0001: Wormhole Core](wormhole_rfc.md), [RFC-0002: Wormhole Governance](wormhole_rfc_governance.md), [Git-Native Architecture Design](../superpowers/specs/2026-07-28-git-native-wormhole-architecture-design.md) |

> **Revision note (2026-08-01).** This revision replaces the profile-per-daemon
> and Fabric-first joining model. `gatewayd` is one passive, non-interactive
> user-level supervisor with a stable local socket. Git carries the portable
> project base; Gateway retains a durable machine-local overlay; Fabric is an
> optional coordination service. `wormhole setup` replaces `join` and
> `connect`. Legacy alpha implementation contracts are migration inputs only.
> Prior alpha specifications and trial outputs remain unchanged historical
> evidence, not current architectural authority. Migration preserves stable
> IDs and historical attribution while deliberately replacing those contracts.

---

## 1. Abstract

RFC-0001 defines Wormhole's pillars and keeps Git as the source of truth for
code. This RFC makes that boundary operational. A checked-out repository
contains a typed, mergeable Wormhole base under `.wormhole/`; a single local
`gatewayd` supervises durable machine-private overlays for every registered
workspace; and an optional Fabric coordinates only the workspaces explicitly
bound to it.

MCP is an agent-only, stateless tool surface. It does not install software,
hold user configuration, perform human login, or decide a project binding.
The human-first `wormhole` CLI installs and configures `gatewayd`, connector
launchers, workspace bindings, and optional Fabric access. Agents may invoke
the CLI where a workflow permits it, but its authority and UX remain human
first.

`gatewayd` is passive deterministic infrastructure: it ingests local MCP
operations, CLI commands, Git workspace observations, and optional Fabric
messages. It makes no model calls and owns no autonomous planning. Git review
and merge remain the way an open-source contributor proposes and upstream
accepts change; Wormhole accelerates that workflow rather than introducing a
second permission system for it.

---

## 2. Motivation

The earlier local-runtime model correctly sought offline durability and low
latency, but coupled first use to remote enrolment and treated a Gateway
snapshot as a private replica. That is a poor fit for normal Git work:
contributors can clone a public repository, create a branch, edit anything,
and submit a pull request without being admitted to a separate collaboration
service.

This architecture closes four gaps:

1. **Portable context.** A clone must contain enough non-secret Wormhole
   context to work immediately, just as it contains code and project
   instructions.
2. **Git-native collaboration.** Context needs meaningful diff, merge,
   rebase, branch, fork, review, and rollback semantics rather than opaque
   SQLite-copy semantics.
3. **One local control plane.** Multiple checkouts, worktrees, projects,
   agents, and optional Fabric accounts must not require competing daemon
   processes or mutable per-connector configuration.
4. **Accountability without gatekeeping public contribution.** A stable human
   can own and sponsor agents where remote authority is needed, while Git
   authorship and pull-request review remain sufficient for an unaffiliated
   public contributor.

---

## 3. Goals and Non-Goals

### 3.1 Goals

- G1: One background, non-interactive `gatewayd` supervisor runs per
  user/system installation and owns one stable same-user IPC endpoint.
- G2: A public clone can hydrate a local workspace from tracked `.wormhole/`
  content with no Wormhole account, Passport, or reachable Fabric.
- G3: Git is the authoritative history, transport, diff, merge, review, and
  acceptance mechanism for code and tracked Wormhole context.
- G4: Gateway stores every local operation durably before any optional remote
  work, and survives Fabric loss or restart without dropping the overlay.
- G5: Multiple checkouts and Git worktrees are separate immutable workspaces,
  even when they name the same Wormhole project.
- G6: Reconciliation is a semantic three-way rebase with explicit conflicts;
  no last-write-wins rule may silently discard local or upstream meaning.
- G7: Fabric is optional. Canonical public projects may use identification-only
  coordination, while private projects use authenticated membership for
  coordination, project bootstrap, and approved skills.
- G8: Human identity, authentication, agent ownership, Git authorship, and
  Fabric permissions are separate concepts with auditable links.
- G9: Code Graph computation is model-free and isolated per checkout.

### 3.2 Non-goals

- NG1: Replacing Git hosting, branch protection, code review, or pull
  requests.
- NG2: Making an arbitrary public fork a member of, or a writable client of,
  the upstream project's Fabric namespace.
- NG3: Full peer-to-peer synchronization, CRDTs, operational transforms, or
  distributed consensus.
- NG4: Model calls, autonomous planning, or policy reasoning inside Gateway or
  a Code Graph worker.
- NG5: Compute-warning screens, compute profiles, or Warpspeed. The model-free
  worker uses normal OS scheduling and observable status.
- NG6: Persisting code bodies in Fabric or using Wormhole data as code truth.
- NG7: Broad governance integration; RFC-0002 remains opt-in and Fabric-side.

---

## 4. Relationship to RFC-0001

RFC-0001's pillars, MCP naming grammar, Git-source-of-truth rule, and
project-scoped security remain authoritative. This RFC amends only local
runtime, transport, workspace, and optional-coordination assumptions:

- Coding harnesses call the local Gateway over MCP; they do not call Fabric
  directly.
- MCP remains the agent contract for pillar operations, but is not a human
  setup, account, or connector-management surface.
- `wormhole.sync.*` remains the Gateway-to-Fabric protocol when a workspace is
  Fabric-bound. It is not required for Git-native public workspaces.
- `wormhole.workspace.*` exposes local status, diff, import, checkpoint, and
  stash project operations, while `wormhole.code_graph.*` exposes local
  derivative graph operations. Neither namespace is a new Core pillar.
- Git-tracked Wormhole records are project content. They are not credentials,
  Passport grants, nor a substitute for server RLS.
- Where a Fabric namespace exists, RFC-0001's server-side RLS, permissions,
  audit, and Passport rules apply unchanged.

---

## 5. Architecture Overview

```text
human: wormhole setup / status / connector install
        │ records immutable workspace binding and local credentials
        ▼
one gatewayd supervisor ─── stable same-user socket
        │
        ├── agent MCP bridge (stateless, agent-only)
        │        └── local durable workspace overlay
        │
        ├── Git observer
        │        ├── tracked .wormhole/state/v1 base
        │        ├── semantic rebase and deterministic checkpoints
        │        └── per-checkout model-free Code Graph worker
        │
        └── optional, explicitly bound Fabric stream
                 └── identified public or authenticated private coordination
                     / bootstrap / approved skills
```

There is one `gatewayd` process, not one daemon per credential profile,
project, harness, or checkout. It contains many namespace-scoped workspace
records and may hold many Fabric profiles. The socket is stable for the user;
only the supervisor owns it. A connector launches the stateless bridge, which
resolves an already registered workspace binding and forwards MCP traffic.

---

## 6. Components and Responsibilities

### 6.1 `wormhole` CLI

The CLI is the human-first installation and lifecycle surface. Its primary
command is `wormhole setup`, which:

1. discovers the repository root and tracked `.wormhole/` snapshot;
2. validates its schema, digests, project reference, and Git context;
3. creates or reuses an immutable local workspace binding;
4. resolves trusted machine-private publication visibility as `unclassified`,
   `local_only`, `public_git`, or `private_git`, independent of canonical/fork routing,
   Fabric mode, caller arguments, actor assurance, and copied hints; public forks are
   `public_git`, unknown visibility remains unclassified, and public-Git setup warns;
5. activates an eligible identification-only public Fabric hint, requests
   login/profile selection for private Fabric, or remains local-only;
6. starts or upgrades the one `gatewayd` supervisor as necessary;
7. installs or repairs supported harness connector launchers; and
8. asks whether to enable Code Graph and, if selected, completes its full
   initial model-free build; and
9. verifies local readiness without requiring Fabric for a Git-only workspace.

`join` and `connect` are replaced by this one coherent flow. Connector setup
is transactional: failed replacement restores the prior harness configuration
where possible and reports the remaining repair action. The CLI may be called
by an agent, but an agent does not gain authority to bypass the human action
or authentication that a particular setup step requires.

The first-party Codex adapter implements discover, inspect, plan, apply, verify,
rollback, and remove using Codex's supported command:

```text
codex mcp add wormhole -- /absolute/path/to/wormhole mcp
```

It installs the common stateless bridge, not a Codex-specific Gateway protocol.
Other supported harness adapters follow the same transactional lifecycle.

Human CLI workspace operations have equivalent MCP operations at
`wormhole.workspace.status|diff|import|checkpoint|stash`. Setup, private login,
Fabric and connector administration, and Code Graph disablement remain human
control-plane operations; ordinary project operations do not split into a
human and an agent model.

### 6.2 Gateway supervisor

Gateway is a passive data layer and supervisor, not an agent. It owns:

- the stable local socket and stateless MCP bridge endpoint;
- namespace- and workspace-scoped durable local storage;
- import of tracked bases, local overlays, semantic rebases, and checkpoints;
- optional Fabric streams, credentials, and retry/recovery state;
- routing to model-free per-workspace Code Graph workers; and
- same-user local IPC and credential-file security boundaries.

It does not own connector configuration, a human UI, an LLM, a mutable
workspace default, or a hidden remote fallback.

### 6.3 Git workspace and snapshot

The repository carries the portable base. Version one uses canonical typed
files rather than a tracked database:

```text
.wormhole/
  config.toml                    # tracked bootstrap manifest
  remotes.toml                   # optional non-secret Fabric hints
  state/v1/
    project.json
    actors/<stable-id>.json
    tasks/<stable-id>.json
    tasks/links/<stable-id>.json
    kb/<stable-id>/record.json
    kb/<stable-id>/body.md
    channels/<stable-id>.json
    events/<stable-id>.json
    git-links/<stable-id>.json
```

The file inventory may evolve only through a versioned snapshot schema. Every
record has an immutable stable ID and canonical serialization. Events and Git
links are add-only; mutable records use semantic three-way merges; deletion
writes a typed tombstone at the record's stable path rather than silently
removing history. A single-file tombstone replaces its JSON record. A KB
tombstone replaces `record.json`, records the deleted body digest, and requires
`body.md` to be absent; a retained body makes the snapshot invalid. Tombstones
record entity kind, deleted-content digest, and deletion attribution.

The versioned schema marks references as live-required or historical. Missing
targets are always broken. Historical event, Git-link, attribution, and
relationship-edge references may resolve to a tombstone as deleted; a
live-required structural reference may not. Delete-versus-record or KB-body
edit conflicts, and same-ID resurrection requires an explicit operation naming
the tombstone digest. Gateway computes the tree digest from sorted paths and
canonical bytes. No generated digest or index file is tracked. Unknown future
schemas, duplicate IDs, broken references, path/record mismatches, and invalid
canonical form fail closed.

Tracked bases contain structurally eligible, explicitly portable context: project
metadata, task definitions and portable task state, curated KB, channels,
explicitly promoted audit-significant `EventV1` records, Git pointers, and non-secret
actor references. Secret-shape checks do not establish confidentiality or public-Git
suitability. Tracked bases never contain bearer tokens, Passport IDs or permissions,
credential profiles, Fabric cursors, sync queues, local audit/recovery state,
absolute paths, socket state, connector backups, file identities, or Code
Graph state. Those belong outside the repository in Gateway-managed local
storage. In particular, legacy `.wormhole/integration-state.json` is migrated
into local Gateway state and ignored; it is not snapshot authority.

Task-transition notifications/history, progress, generic channel activity, presence,
runtime attribution, subscriptions, queues, telemetry, conflicts/receipts, and
discoveries awaiting curation are operational Gateway/Fabric state. They do not enter
the version-one Snapshot automatically. Promotion accepts only an activity with a
complete promotable-event projection. Channel, source actor, event type, payload, note,
and creation time copy exactly into `EventV1`; the enclosing operation actor is the
distinct promoter, and callers cannot replace either attribution or copied semantics.
The event's sole extension is `dev.wormhole.promotion`, with schema-version-1 data
containing only `source_activity_id` and `source_activity_digest`. Promotion
marks/receipts that source atomically in one ProjectState-owned immediate transaction.
Checkpoint only materialises already-portable operations and never performs promotion.

### 6.4 Per-checkout Code Graph workers

When Code Graph is enabled, its immutable workspace binding gets an isolated,
model-free worker and derivative store. The worker receives only that
checkout's root, Git revision, declared source limits, and binding identity. It
may build, replace, or discard a graph without affecting another checkout or
worktree.
It never persists source bodies as Wormhole truth, calls Fabric, or accesses
another workspace's files. It uses no model download or inference, embedding,
vector storage/search/call, or implicit network access. Build progress and
failures are reported in status under normal OS scheduling; there are no compute
warnings, profiles, or Warpspeed. The checkout view is read-only.

The CLI exposes `wormhole code-graph status|query|rebuild|disable`; MCP exposes
equivalent project operations for status, query, and rebuild. Every revision
stores a source fingerprint plus an analysis fingerprint over the source
fingerprint, all tracked non-source inputs declared by the adapter, normalised
build/target/configuration, graph/adapter schema version, and compiler/toolchain
identity. Every status and query recomputes and compares the analysis
fingerprint, including tracked dirty changes. A mismatch reports indexed and
current analysis fingerprints, source fingerprints where different, dirty
state, `graph_not_current`, and `rebuild_recommended`, and query fails closed
without returning matches, edges, or source. Rebuild explicitly indexes that
exact analysis view and publishes copy-on-write only if its fingerprint remains
unchanged. A failed rebuild preserves the prior revision for recovery but never
serves it as current. Restart or adapter/toolchain upgrade repeats this freshness
check; disable removes only that workspace's derivative graph and machine-local
enablement. No watcher or automatic rebuild is required.

### 6.5 Optional Fabric

Fabric is a coordination service, not a prerequisite for Wormhole. A
Fabric-bound workspace may use identified public or authenticated private
bootstrap, shared coordination, project policy, and separately approved skill
distribution. Fabric remains
the authority for its own project membership, Passport permissions, audit,
and server-side RLS; Gateway cannot infer or manufacture that authority from
Git files.

Public repositories work from their tracked base alone. A canonical public
repository may also commit an identification-only Fabric hint. Gateway
activates it only when `origin` matches the canonical repository identity; the
caller proves continuity of a self-issued public identity key but needs no
Wormhole account or membership. Canonical branches receive isolated streams.

A fork inherits the base as Git content but **cannot use the upstream Fabric
namespace** merely by copying its manifest, remote URL, project ID, or history.
It has no upstream Fabric stream; it may remain local-only or bind an independent
Fabric realm. Contributions return upstream through normal Git commits and pull
requests. Private repositories may use Fabric, but require both Git access and
authenticated project membership before Fabric context is disclosed.

---

## 7. Workspace Binding, Namespaces, and Isolation

### 7.1 Immutable workspace binding

A workspace is one checked-out repository root or Git worktree, not merely a
project UUID. `wormhole setup` creates a local binding whose identity and routing
fields are immutable, with associated observed state:

- a generated workspace ID;
- stable filesystem/worktree identity and canonical root recorded at setup;
- the tracked project ID, current display handle, base tree digest, and snapshot
  schema;
- the canonical Git remote and canonical accepted ref, if declared;
- the selected optional immutable Fabric instance ID, remote project ID, stream
  ID, and credential reference, if any; and
- whether that one binding is read-only or writable.

The workspace, project, checkout, Fabric-instance, remote-project, and writable-
stream identities are created once and never silently retargeted by a changed
current directory, branch, remote, copied config, or MCP request. The observed
base digest, checked-out ref, display handle, and credential contents may evolve
under their validation rules without selecting a different route. A moved
checkout, changed remote, changed project identity, or incompatible snapshot
requires a human CLI rebind/re-setup action. Multiple worktrees therefore
receive separate overlays and Code Graph workers even when their project IDs
match.

Every localstore operation carries both project namespace and workspace ID.
SQLite has no RLS, so repository methods must scope queries explicitly and
ship cross-namespace and cross-workspace rejection tests. Fabric profiles are
also explicit: no profile is chosen from ambient environment, connector
configuration, or the last workspace used. Every remote queue and cursor key
includes the immutable Fabric instance ID.

### 7.2 Identity, authentication, and agent ownership

Human identity is stable; authenticators are replaceable; agents are durable
accountability principals. A human may have browser/SSO, passkey, SSH, or
other authenticators, but those authenticate the human rather than becoming
the human's ID. Every agent action captures the durable agent ID, accountable
human at action time, harness/model session, and assurance. The accountable
human may be self-declared locally or in public mode; private remote ownership
is membership-backed and transfer is an audited human action. An agent Passport
grants only project-scoped Fabric capability and is distinct from both the human
session and Git authorship.

An unauthenticated public contributor may create a local workspace agent or
actor record and use all local Git-backed capability. That record has no
private Fabric authority. A canonical-public Fabric may accept the contributor's
self-issued key for pseudonymous continuity; this is identification, not verified
human identity. Git commit identity/signature and pull-request identity remain
separate from Wormhole attribution. Login is required only for private Fabric
identity or resources. Private authentication, ownership transfer, membership
and permission administration, and policy activation remain human-authorized
control-plane actions; ordinary project operations retain human-agent parity.

### 7.3 Local secret boundary

Raw tokens are never placed in Git, snapshots, MCP results, logs, events, or
diagnostics. Gateway stores credentials beneath its protected local credential
root with owner-only permissions and no-follow, atomic replacement rules.
Credential profiles, Fabric refresh/session material, connector rollback data,
and workspace overlays are local state. Fabric stores token hashes, never raw
tokens. The same-user local socket is the v1 pre-credential and MCP transport
boundary; Gateway is not shared across OS users.

---

## 8. Local Durability, Git Rebase, and Optional Synchronisation

### 8.1 Base, overlay, checkpoint

Gateway operates on three durable layers:

1. **Accepted base:** the exact tracked snapshot at the workspace's observed
   checked-out Git commit.
2. **Overlay:** private durable operations not yet materialised, plus a private
   journal for operations already materialised but not yet accepted by Git.
3. **Checkpoint candidate:** deterministic materialisation into a complete
   canonical `.wormhole/state/v1/` working tree for Git diff, commit, and review.
   It is not a new accepted base until Gateway observes a Git commit containing it.

Gateway atomically records an operation and the resulting overlay before it
reports local success. `wormhole checkpoint` records the expected live tree
digest and exact publication review, then renders the candidate in an owner-only,
same-device Git-private checkpoint directory outside the portable worktree. It creates
only a fresh no-replace stage before rechecking the live digest as a compare-and-swap
precondition; backup does not exist until journal-backed publication. A raced direct edit
aborts without overwriting either input. The unjournaled stage remains private, unowned,
ignored by recovery, and never reused.

Before mutating live bytes, checkpoint durably records both complete publication trees,
exact operation states, the version-one review proof, and an exact prior-candidate
preimage containing complete inline direct and optional rebased snapshots. Publication
uses atomic directory exchange where available or a durable all-or-recover journal
elsewhere. Indeterminate journal COMMITs are confirmed against exact transition-relative
prior/next state without replay. Recover-old restores candidate absence or every original
candidate/operation byte; recover-new retains the exact publication postimage.

Recovery strict-proves complete journal cardinality, candidate state, and operation
ownership before Git/path I/O. No-work history returns DB-composed status without Git/path
I/O. One prepared/published driver uses an advisory current-HEAD/origin observation, then
under `BEGIN IMMEDIATE` byte-matches and re-proves the disposition and repeats the
position/tree-at-SHA/origin/position bundle before path access. Stored base exact or a
same-ref different-commit exact candidate can converge; the latter has no ancestry
requirement. Other bases fail before origin invalidation or path I/O.

Checkpoint and recovery never advance the accepted base. A checkpoint materialization is
accepted only when same-symbolic-ref Reject/Refresh observes the matching Git commit and
marks the retained journal accepted while preserving later overlay. Task-4 proposal-free
ref-switch and applicable-Discard transitions may otherwise advance the base independently.
One prepared, published, or recovered-new journal blocks another checkpoint with
`ErrCheckpointPendingAcceptance` before artifact creation. Later overlay remains available
and is not superseded.

### 8.2 Semantic three-way rebase

When Git advances, Gateway reconciles `(old accepted base, checkpointed
candidate + local overlay, new base)` by entity semantics. It does not use
timestamp or row-level last-write-wins.

- Independent records merge automatically.
- For an absent immutable ID, one-sided and byte-exact dual additions are
  accepted; unequal dual additions conflict. For an old-live Event or Git link,
  both endpoints must still equal the old record: mutation or valid
  disappearance conflicts even when both sides made the same change.
- Mutable fields merge only when changed semantic fields do not overlap.
  Lifecycle reconciliation accepts exact endpoints and a single non-base
  endpoint. Only divergent non-base tombstones/resurrections or a tombstone
  racing a semantic edit conflict; KB root and Markdown body are independent
  evidence surfaces.
- Schema, digest, and reference-invalid inputs return typed errors. After the
  old base validates, raw mutable disappearance returns its dedicated error
  before side validation; immutable disappearance is side-validated first.

Conflict triples are oriented `Base=old accepted base`, `Ours=candidate plus
local overlay`, and `Theirs=new base`. Any conflict returns the byte-identical
complete candidate with the full sorted evidence set; no clean subset or Git-owned
field is partially merged into that fallback.
The semantic conflict digest is not its history key: Gateway stores a UUIDv4
occurrence, permits only one open occurrence for a semantic digest and workspace,
resolves absent evidence without deleting it, and allocates a new occurrence if the
same semantic conflict later reopens. Persisted evidence is strict-decoded, typed-root
rehydrated, ID-recomputed, and sort-checked before use.

An unresolved conflict blocks checkpoint, writable Fabric delivery, and accepted
state advancement for that workspace while preserving both sides and the
diagnostic. An authorised participant resolves it through equivalent CLI/MCP
Gateway project operations or Git; only authentication, ownership transfer,
connector installation, and machine administration are reserved to the human
control plane. Gateway then records a new deterministic checkpoint. There is no
silent loss and no automatic overwrite rule.

### 8.3 Canonical branches and acceptance

Checkouts and worktrees are isolated workspaces. A branch's overlay and any
checkpointed working-tree change are proposal state, not upstream acceptance.
Switching one workspace to another branch with proposal state requires checkpoint/commit
as applicable, stash, or discard. Discardable proposal state is exactly a candidate, an
active or rebased operation, or open conflict evidence. Git observation supports Reject
or Discard only. Stash follows a rejected refresh as a separate receipt-backed
transaction and is followed by Refresh and Recover; it is never nested in observation.

BranchSwitchDiscard first validates and digests only its request, then reads the exact
receipt through binding-free `TransitionReceiptByKey` before any filesystem, Git, or
current-binding check. That API syntactically validates scope/request ID, queries only
`workspace_transition_receipts`, CAST-matches the exact textual key, requires raw TEXT
storage/equality, and selects `COUNT(rowid) OVER()` with exact columns/types.
`sql.ErrNoRows` returns nil, count one returns a strict record, and every other returned
count is corruption/ambiguity. It never queries `workspace_bindings`. An exact receipt
returns read-only with zero Git calls and writes; another action or digest returns
`ErrIdempotencyConflict`, while corrupt or ambiguous state fails closed.

Only absence permits outside observation. Discard then uses
`WithImmediateWorkspaceTransition`: after syntactic validation and `BEGIN IMMEDIATE`, its
first SQL read is the same receipt-table-only lookup. It passes the receipt and bound-scope
transaction to the callback. Only nil permits any workspace/state read, complete binding
equality, complete state/materialization preloads, and in-transaction checkout/ref/HEAD
reobservation. Existing Stash/Restore receipt and immediate-workspace seams are unchanged.

After that Git-position recheck, Gateway strictly proves and classifies the complete
materialization disposition before Discard applicability. Prepared, orphan/unclaimed or
nonterminal materialized, corrupt/ambiguous terminal ownership, and any other incomplete
ownership proof fail first as recovery/corruption blockers. Historical `accepted` and
`recovered_old` rows are nonblocking only after a complete proof. Only a proved-safe
disposition reaches applicability.

Discard applies only to an actual symbolic-ref change, including a same-SHA ref change,
with discardable proposal state. Otherwise it returns zero result plus
`ErrBranchSwitchDiscardNotApplicable`, with no mutation or receipt; an exact prior receipt
still wins. Reject may advance a ref change without proposal normally, and same-ref
changes use normal acceptance or semantic rebase under Reject. Under Reject, any
symbolic-ref change with proposal always returns `ErrBranchSwitchPending` and cannot
accept a materialization. Discard instead proceeds through the proved applicability and
materialization-match rules above and below. Applicable discard keeps
the established order: mark exact active/rebased rows discarded, preserve stashed and
accepted-journal materialized rows, delete the candidate, resolve open conflicts, record
the receipt, and advance the independently observed base atomically.
Stashed-only, discarded-only, accepted-journal historical-materialized-only, and
resolved-conflict-history-only state is terminal history, not proposal state.

Only same-symbolic-ref Reject/Refresh may accept an exact proved `published` or
`recovered_new` materialization. Discard never accepts one: an exact match returns zero plus
`ErrBranchSwitchDiscardNotApplicable`, with no receipt/base change and byte-identical
state. A nonmatching proved acceptance-eligible row blocks same-ref Reject rebase and
applicable discard with `ErrGitMaterializationPrecondition`, retaining journal, candidate,
and materialized history byte-identically. `prepared` still requires recovery and blocks
another checkpoint before artifact creation; historical `accepted` and `recovered_old`
do not block. No new journal state or migration
is introduced. Orphan or nonterminal materialized rows remain recovery blockers. The
clean-discard receipt remains candidate-not-accepted, with nil journal, no rebase, and no
conflicts.

Trusted zero-actor same-ref rebase preserves an existing candidate's importer and time.
Without a candidate it uses `system:git-observation-rebase-v1` and the single UTC
transaction observation time captured after in-transaction Git reobservation and before
the first write. Candidate importer validation, including stash and retry reads, accepts
exactly a canonical UUID or that fixed non-authority token. It never borrows an operation
actor or fabricates actor authority.

If an applicable Discard COMMIT is unknown, a fresh exact receipt read without Git proves
success only when its complete result equals the attempted transition; every other
readback returns zero plus `ErrCommitOutcomeUnknown`. Non-discard observation never
infers an unknown COMMIT from receipt or state, and Refresh returns no binding on that
error. Same request IDs remain retry-safe only for the same canonical digest. A local
merge, branch name, PR status, or claimed commit SHA is insufficient acceptance evidence.

Import captures the canonical no-follow working tree before its transaction and repeats
the exact read under the writer barrier immediately before mutation. Reject and a
Discard no-receipt path perform a full outside observation, then compare the complete old
binding and reobserve checkout/ref/HEAD at the in-transaction linearization point. Later
external changes are caught by the next refresh.

Consequently, the accepted public `main` Fabric stream advances only after
Fabric independently observes the canonical repository's default ref move to a
commit containing a valid tracked snapshot. This prevents a fork, stale checkout,
or arbitrary local branch from masquerading as upstream state. Each canonical
branch has an isolated stream, while each local workspace has at most one
writable Fabric binding. Fabric delivery is asynchronous and never gates a
durable local operation.

### 8.4 Fabric bootstrap and synchronization

For an explicitly Fabric-bound workspace, Gateway may bootstrap identified
public or authenticated private Fabric state into its local overlay and
incrementally push/pull that overlay.
The binding is checked on every operation; Fabric state from a different
project, profile, or workspace is rejected. Push happens only after the local
operation is durable and does not block local work during an outage. Failed
delivery remains queued in protected local state and is retried from the
checkpointed stream.

Fabric bootstrap does not replace the tracked Git base, and a tracked base
does not grant Fabric access. Where a public project chooses not to bind
Fabric, this section is simply absent from its setup path.

### 8.5 Operational retention and publication review

V1 operational retention has three classes: ephemeral presence is memory-only and may
disappear on restart; ordinary activity is eligible when older than 30 days or outside
the newest 10,000 unprotected workspace rows, then pruned by `(created_at, activity_id)`
ascending; and lifecycle evidence such as pending queues, open conflicts, recovery state,
and receipts is excluded from age/rank pruning until terminal. Post-terminal retention
defaults to exactly 30 days and may be configured longer only to a finite duration.
Protected rows may exceed the ordinary cap. Gateway and Fabric expose an effective finite
policy before accepting live activity; expiry never mutates portable Git state.

Status and diff remain available for an unclassified workspace and expose the exact
candidate and publication-review digests. Checkpoint is blocked until setup has persisted
a trusted machine-private classification. For `public_git`, including a public fork, it
additionally requires the caller's exact publication-review digest, rechecks that digest
before any staging/journal/publication, and persists the exact actor plus digest in its prepared
journal/receipt. CLI and MCP provide the same capability. The acknowledgement is
attributed publication intent and CAS, not authorization or DLP; direct Git edits remain
possible.

Classification is explicit user publication policy, not continuous Git-host visibility
detection. It is bound to the workspace and repository identity; an origin/repository
identity change invalidates it to `unclassified`. A same-identity host visibility change
cannot be detected offline and requires explicit setup reconfiguration, which invalidates
earlier review digests. Status/diff always surface the current classification.

---

## 9. Decision Register

### Decided

- **Supervisor:** `gatewayd` is one non-interactive user-level supervisor with
  one stable same-user socket, not a process per profile or project.
- **MCP:** MCP is agent-only and stateless. It owns neither human login nor
  persistent connector/project configuration.
- **Setup:** `wormhole setup` is the human-first lifecycle command and
  replaces `join` and `connect`.
- **Portable state:** `.wormhole/state/v1/` is typed, tracked, canonical,
  versioned curated project content. Generic activity is operational; only explicit
  source-bound promotion creates portable `EventV1` evidence. SQLite, credentials,
  queues, cursors, and integration state remain private.
- **Workspace routing:** bindings are explicit and immutable; every operation
  is project-namespace and workspace scoped.
- **Conflict resolution:** semantic three-way rebase with surfaced conflicts;
  no last-write-wins authority exists in this architecture.
- **Fabric:** optional for public projects, membership-controlled for private
  projects, and unavailable to a fork unless independently bound.
- **Accepted state:** the accepted default-branch Fabric stream advances only
  after Fabric independently observes the canonical Git ref.
- **Code Graph:** one isolated, model-free worker per workspace; no model
  download/inference, embeddings, vectors, implicit network, compute warnings,
  compute profiles, or Warpspeed. Status and query verify a current analysis
  fingerprint covering tracked inputs plus adapter/toolchain/config identity;
  stale queries fail closed, and rebuild is explicit and copy-on-write.
- **Identity:** every agent has an accountable human and typed action envelope;
  local/public ownership may be self-declared, while private ownership is
  membership-backed. Authenticators, Passport grants, and Git authorship are
  distinct and auditable.
- **Agent-first parity:** CLI and MCP expose equivalent authorised project operations,
  while schemas, progressive retrieval, autonomous durability, attribution, and handoff
  remain agent-first.
- **Scope:** V1 state and authority are project/repository-lineage scoped. Cross-repo
  graphs, KB merge, inherited governance, and organisation authority require a new RFC.

### Contract details

The approved [Git-Native Wormhole Architecture Design](../superpowers/specs/2026-07-28-git-native-wormhole-architecture-design.md)
defines the version-one layout, canonicalisation boundary, actor assurance,
authenticator families, routing keys, merge rules, setup flow, and delivery
slices. Platform-specific filesystem identity, atomic publication mechanisms,
and public wire encodings are implementation-plan decisions; none may weaken
these RFC invariants or reintroduce last-write-wins.

### Transition constraints

- The implemented version-one `wormhole.sync.bootstrap`, incremental pull/push,
  and conflict-report envelopes remain frozen compatibility inventory until an
  approved delivery slice introduces and tests a new protocol version. New
  semantics must not be smuggled into version one.
- Local IPC continues to trust only processes able to connect through the
  same-user OS-protected socket or named pipe; it gains no ambient cross-user
  sharing or connector bearer-token fallback.
- Raw legacy Passport tokens remain hashed server-side, written atomically to
  owner-only local storage when retained for migration, and excluded from MCP
  results and logs.
- Existing integration manifests remain declarative, human-approved
  materialisation input. Neither they nor the new tracked project base may
  install or execute arbitrary repository content.

### Verification and acceptance

Every delivery slice requires focused tests followed by the repository's full
check gate. Relevant changes add cross-project, cross-workspace, cross-Fabric,
fork/upstream, connector rollback, migration, race, and security tests. Merged
statement coverage remains at or above 80%. The architecture is not externally
validated until the three-Gateway/one-Fabric VM trial and every issue #56
subsidiary gate pass.

Observer tests additionally freeze the binding-free table-only receipt API and a
query-recorded first-read transaction seam; receipt-before-Git and concurrent retry;
terminal-only discard applicability; Reject/Refresh-only materialization acceptance;
exact-match Discard non-applicability; nonmatching materialization; fixed/preserved
candidate provenance; rollback/restart; and both discard and non-discard unknown-COMMIT
behavior. Every negative case proves zero result where required and byte-identical state.

---

## 10. Security Considerations

- Git-tracked snapshots are content, not authorization. Treat public snapshot
  data as public and validate every imported byte before use.
- The snapshot must never include secrets or machine-specific state. Git history makes
  accidental disclosure durable, and secret-shape validation is not confidentiality
  detection. `public_git` setup warns explicitly; checkpoint requires the exact
  attributed publication-review digest from human CLI or agent MCP. Wormhole claims no
  DLP and cannot prevent direct Git publication.
- Fabric requires HTTPS for every non-loopback endpoint. Canonical-public
  requests prove identification-only key continuity; private requests
  authenticate with the binding's credential. HTTP is development-only on
  loopback.
- Server-side Postgres RLS remains the authoritative second line of defense
  for Fabric. Gateway's SQLite isolation is application-enforced and therefore
  requires mandatory scoped repository methods and cross-scope tests.
- A copied `.wormhole/config.toml` cannot authorize a fork against upstream
  Fabric. Gateway validates immutable workspace identity, canonical Git
  relationship, and explicit local binding before creating a stream.
- Integration manifests and their local projection remain separately designed
  human-approved materialisation. No tracked snapshot entry may cause Gateway
  to install or execute arbitrary code.
- MCP's same-user socket trusts processes running as that OS user. Connector
  installation must not relax its socket/parent-directory permissions.

---

## 11. Glossary

- **Gateway supervisor:** the single passive `gatewayd` process for one
  user/system installation.
- **Workspace:** one immutable local binding to a repository checkout or Git
  worktree, distinct from a project namespace.
- **Accepted base:** the typed `.wormhole/state/v1/` state at the exact Git
  commit observed by a workspace.
- **Overlay:** private, durable Gateway operations layered over the accepted
  base, including recovery metadata for materialised-pending-commit operations.
- **Checkpoint:** deterministic working-tree materialisation of an overlay, with
  private durable recovery evidence; it does not advance the accepted base.
- **Canonical ref:** the declared upstream Git ref whose observed advance is
  the only way a workspace accepts new canonical state.
- **Fabric stream:** one explicit optional synchronization path bound to a
  workspace and Fabric namespace; at most one is writable per workspace.
- **Passport:** a project-scoped Fabric capability grant to an agent; never a
  Git or human-login credential.

---

## 12. References

- [RFC-0001: Wormhole Core](wormhole_rfc.md)
- [RFC-0002: Wormhole Governance](wormhole_rfc_governance.md)
- [Git-Native Wormhole Architecture Design](../superpowers/specs/2026-07-28-git-native-wormhole-architecture-design.md)
- [`docs/implementation-rules.md`](../implementation-rules.md) — implementation guardrails.
