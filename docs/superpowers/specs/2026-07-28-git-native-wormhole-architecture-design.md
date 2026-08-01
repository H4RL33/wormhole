# Git-Native Wormhole Architecture Design

**Date:** 2026-07-28

**Reviewed:** 2026-08-01

**Status:** Architecture approved; Git-nativity review reconciled

**Decision owner:** Harley Welsh

**Scope:** V1 project/repository-lineage bootstrap, Gateway topology, Git-native collaboration,
multi-Fabric routing, human and agent identity, harness connectors, and local
Code Graph operation

## 1. Outcome

Wormhole becomes a Git-native collaboration layer that a human or agent can use
immediately after cloning a repository. The tracked `.wormhole/` directory is a
portable, reviewable project base. One local `gatewayd` supervisor maintains a
durable working overlay for every checkout, exposes the same project operations
to humans and agents, and optionally synchronises a workspace with one explicitly
bound Fabric stream.

Git remains the acceptance and merge authority for explicitly curated portable
project context. Gateway and Fabric own operational collaboration and bootstrap
connected Gateways; neither becomes a second Git host, silently promotes live
activity into `.wormhole/`, or overwrites divergent repository state.

The resulting product has four distinct layers:

1. **MCP connector:** stateless, agent-facing harness bridge.
2. **`wormhole` CLI:** human-first installation, setup, identity, authentication,
   connector, project, and Fabric surface; also usable by agents.
3. **Gateway:** one non-interactive user-level supervisor and primary local data
   plane, with isolated workers for project-local derivative work.
4. **Fabric:** optional server-side synchronisation and bootstrap service for
   public or private projects.

This design is the new architectural baseline. RFC-0001 and RFC-0003 are revised
with it; prior joining, Passport-only identity, last-write-wins, and single-remote
assumptions do not constrain the implementation.

## 2. Goals

- A public repository clone is fully usable without a Wormhole account or Fabric.
- Humans and agents have operational parity for project work: either can inspect and change
  tasks, knowledge, events, links, and other project context and submit the result
  through the repository's normal Git workflow.
- Keep Wormhole agent-first: typed schemas, progressive retrieval, autonomous local
  durability, attribution, and handoff are designed primarily for agent work; the
  human CLI supplies setup, review, and control over the same authorised operations.
- `.wormhole/` contains everything another checkout needs to reconstruct the
  accepted project context, but no machine secrets or private runtime state.
- Project context is human-reviewable, deterministically serialised, diffable,
  mergeable, and resilient to branches, forks, and worktrees.
- One Gateway supports many projects and many Fabric connections without routing
  by ambiguous display names.
- Public Fabric is available without account authentication, while forks cannot
  consume or pollute their upstream project's Fabric.
- Private Fabric separates human identity, authenticators, project
  authorisation, agent identity, and accountable ownership.
- `wormhole setup` installs and verifies Gateway and supported harness connectors,
  including a first-party Codex connector flow.
- Code Graph improves agent navigation with compiler and deterministic lexical
  indexes, without embeddings, local models, vector search, or ML compute controls.
- Local work remains available during Fabric outage, revocation, or absence.

## 3. Non-goals

- Storing source-code bodies in Fabric or replacing Git hosting.
- Treating checked-in actor claims as credentials or authority grants.
- Allowing one local workspace to write concurrently to multiple Fabric streams.
- Synchronising Code Graph state through Fabric in this release.
- Snapshotting Code Graph into `.wormhole/` in this release.
- Vector search, embedding generation, local model downloads, compute profiles,
  compute warnings, or Warpspeed for the model-free Code Graph.
- Making repository-supplied setup files executable.
- Reading, copying, or exposing Git HTTPS tokens, SSH private keys, credential
  helper output, or signing secrets.
- Replacing GitHub, GitLab, or another Git host's branch, review, and merge policy.
- Defining an organisation-wide or cross-repository graph, inherited policy, merged
  knowledge base, or cross-project authority. V1 is project/repository-lineage scoped;
  organisation-wide semantics require a separate RFC.

## 4. Authority and invariants

### 4.1 Authority order

After reconciliation, authority is:

1. RFC-0001 for Wormhole Core.
2. RFC-0003 where it explicitly defines or amends Gateway, transport, Git-native
   workspace, or Fabric-routing behaviour.
3. RFC-0002 only for optional Governance.
4. This approved design for the version-one contract details delegated by the
   RFCs.
5. `docs/implementation-rules.md`.
6. Existing code.

The reconciled RFCs supply the governing authority and delegate the concrete
version-one contract to this design. Historical alpha specifications and trial
evidence remain evidence of the system at that date, not current architectural
authority.

### 4.2 System invariants

- Git is the sole source of truth for code and the acceptance authority for
  explicitly curated tracked Wormhole project state.
- The version-one `Snapshot` and `OperationV1` are the portable representation, not a log of all
  collaboration. Gateway/Fabric retain operational activity under finite policy.
- Gateway is local-first: a successful project write is durable locally before
  any remote synchronisation is attempted.
- Harnesses communicate with Gateway, never directly with Fabric.
- Fabric may replicate or propose project state but may not silently overwrite a
  divergent Git base.
- Every workspace routes by immutable identifiers, never by a human-friendly name
  alone.
- A workspace has zero or one writable Fabric stream.
- A fork never activates a copied upstream Fabric hint.
- Agent actions preserve the agent, accountable human, session, and assurance
  level at action time.
- No secret is committed beneath `.wormhole/`; structural secret checks make content
  eligible for tracking but do not prove that it is non-confidential or suitable for
  a public repository.
- Gateway performs no generative inference. Code Graph performs no model
  inference or vector retrieval.
- Unsupported schema versions, ambiguous routes, integrity mismatches, and
  unresolved merges fail closed.

## 5. Layer model and process topology

```text
Human
  |
  | wormhole CLI
  v
+-----------------------------+
| one user-level gatewayd     |<----- stateless MCP ----- Harness agents
| one stable local socket     |
|                             |
| control plane               |
| - workspace bindings        |
| - identities and sessions   |
| - Fabric profiles/routes    |
| - durable overlay/sync      |
|                             |
| isolated workers            |
| - Code Graph per checkout   |
+--------------+--------------+
               |
       +-------+--------+
       |                |
       v                v
Git checkout        optional Fabrics
`.wormhole/`        public or private
```

### 5.1 MCP layer

The MCP layer is not a persistent product database or configuration owner. A
supported harness launches `wormhole mcp`; the bridge discovers the current
working directory, connects to Gateway's stable local socket, and forwards the
workspace context with each request. Gateway resolves that context to an explicit
workspace binding.

The connector stores no Fabric credential, project database, or independent
policy. Installing it means registering the stable `wormhole mcp` command in the
harness's supported configuration mechanism.

### 5.2 CLI layer

`wormhole` is the install target and the human-facing control surface. It:

- installs, starts, upgrades, and diagnoses `gatewayd`;
- discovers and registers repository workspaces;
- manages local human identity and private authentication;
- configures Fabric profiles and workspace bindings;
- detects, installs, verifies, and removes harness connectors;
- exposes project diff, import, stash, and checkpoint operations; and
- bridges stdio MCP for harnesses.

The CLI delegates durable domain operations to Gateway rather than maintaining a
second state machine.

### 5.3 Gateway layer

There is one logical `gatewayd` supervisor per OS-user security boundary. It owns
one stable local socket and one control database. Project and workspace isolation
is explicit in every repository and routing method.

The supervisor owns lightweight control and data-plane work. Code Graph indexing
runs in isolated, on-demand workers with a read-only view of one approved checkout,
a separate derivative graph database, and no Fabric credentials. Worker crashes do
not stop Gateway or invalidate the last published graph revision.

The worker boundary remains useful for fault and checkout isolation even though
the current Code Graph is model-free. There are no user-selectable compute
profiles, compute warnings, or Warpspeed path.

### 5.4 Fabric layer

Fabric is optional and server-side. It synchronises connected workspace streams,
provides bootstrap content such as project skills, and authenticates private
projects. Public projects may offer identification-only Fabric access.

Fabric stores finite-retention operational collaboration, bootstrap material, and Git
metadata—not code bodies or an independent accepted project truth. It may cache validated
portable proposals/accepted trees for reconstruction and read Git refs plus the
`.wormhole/` subtree to verify accepted state. It must not execute repository content.

## 6. Projects, repositories, and human-friendly identifiers

### 6.1 Stable identities

The following identifiers have distinct purposes:

| Identifier | Scope | Stored in Git | Purpose |
|---|---|---:|---|
| `project_id` | project lineage | yes | immutable internal project key shared by clones and forks |
| project handle | project lineage | yes | human-friendly `namespace/project` display and selection |
| `workspace_id` | one checkout/worktree | no | local durable overlay and routing key |
| Fabric instance ID | one Fabric realm | hint only | prevents cross-Fabric collisions |
| remote project ID | project inside one Fabric | hint allowed | Fabric-side project key |
| stream ID | branch/workspace stream | no | one writable synchronisation target |
| repository identity | canonical Git repository | yes | distinguishes canonical repository from forks |

Handles are mutable aliases and never database primary keys. A rename changes the
display handle while preserving `project_id`. From a working directory, commands
need no project argument: Gateway resolves the nearest registered checkout to its
`workspace_id`. When an explicit remote project is required, the display form is
`fabricAlias:namespace/project`; resolution still pins immutable IDs.

### 6.2 Canonical Git repository identity

The project manifest records a canonical repository identity. A provider-issued
immutable repository ID is preferred when the Git host exposes one. The manifest
also records a normalised canonical remote for diagnosis and hosts without such an
ID.

Local setup considers an upstream Fabric hint eligible only when `origin` resolves
to the canonical repository identity. Merely sharing history, having an `upstream`
remote, or retaining a copied URL in another remote does not qualify a fork.

Fabric independently verifies repository, ref, and commit state. It never trusts a
caller's branch name or repository claim by itself.

## 7. The tracked `.wormhole/` project base

### 7.1 Version-one layout

```text
.wormhole/
  config.toml
  remotes.toml                       # optional, non-secret Fabric hints
  state/v1/
    project.json
    actors/<actor-id>.json
    tasks/<task-id>.json
    tasks/links/<link-id>.json
    kb/<article-id>/record.json
    kb/<article-id>/body.md
    channels/<channel-id>.json
    events/<event-id>.json
    git-links/<link-id>.json
```

`config.toml` declares the snapshot version, `project_id`, project handle, and
canonical repository identity. `remotes.toml` may contain safe connection hints:
Fabric URL, immutable Fabric instance ID, remote project ID, expected repository
identity, and public/private mode. It contains no token, session, private key,
absolute path, or machine binding.

The `state/v1/` tree is typed and normalised by entity. Stable IDs, not titles,
handles, or slugs, determine paths. Relationship edges use their own stable files
so unrelated entities do not conflict in one aggregate document.

Optional Governance adds no ad hoc path: RFC-0002 owns strict schema-version-1
`dev.wormhole.governance.realm` on `project.json` and
`dev.wormhole.governance.adoption` on the Constitution KB article record. Core preserves
their canonical extension objects; Governance strict-decodes their closed schemas and
domain-separated digests/signature before activation. Canonical/shared and remote-fork
realms require server/provider observation of the exact configured repository/ref/commit;
only an explicitly local-only realm may use Gateway local-ref observation.

### 7.2 Portable versus operational state

Structurally eligible portable state includes:

- project metadata and aliases;
- self-declared actor display records and public identity keys;
- task definitions, portable task state, and stable task links;
- curated KB records and Markdown bodies;
- channel definitions and explicitly selected audit-significant `EventV1` evidence;
- Git commit, branch, and pull-request links;
- explicit tombstones; and
- non-secret Fabric discovery hints.

An `EventV1` is explicitly promoted portable evidence. It is not the persistence
format for every event observed by Gateway or Fabric. Task-transition chatter,
progress messages, generic channel posts/status activity, presence, runtime/session
attribution, subscriptions, delivery queues, telemetry, conflicts and receipts, and
discoveries awaiting curation remain operational. A task mutation may update the
portable task state without automatically creating an
`EventV1`; generic channel activity must not create one automatically.

Promotion from operational activity is a distinct typed operation available through
equivalent CLI and MCP capabilities. In one ProjectState service-owned immediate
transaction it strict-reads the exact canonical source activity and digest. Only an
`ActivityV1` with a complete promotable-event projection is eligible. `EventV1.ID` is
the promotion's stable generated ID; `ChannelID`, `ActorID`, `EventType`, `Payload`,
`Note`, and `CreatedAt` are exact deep-owned copies of the source projection. The source
actor remains `EventV1.ActorID`; the distinct curating actor is the enclosing
`OperationV1.Actor`. No caller may replace either attribution or any copied semantic
field. The event has no caller-supplied extensions: its sole extension key is
`dev.wormhole.promotion`; its schema-version-1 data contains only
`source_activity_id` and `source_activity_digest`. The transaction atomically
marks/receipts the promotion with the event ID, operation ID, promoter envelope, source
activity ID, and source digest. Sources
without the complete projection require a separate typed curation design and cannot be
promoted by this operation. It never nests the existing portable `ApplyBatch`. The result
is visible in status/diff for review. Checkpoint only
materialises operations that are already portable; it never performs promotion. Direct
Git editing and import remain an explicit alternative publication path.

Machine-private state remains outside the repository in Gateway's XDG-scoped
configuration, data, and runtime roots:

- human authentication sessions and Fabric credentials;
- agent credentials, Passports, ownership grants, and sponsorships;
- workspace IDs, absolute checkout paths, and worktree bindings;
- Gateway's control/read-model database;
- working overlays, stashes, sync queues, cursors, and conflict records;
- integration recovery journals and connector backups;
- Code Graph databases, revisions, worker processes, and sockets; and
- local identity private keys.

The existing `.wormhole/integration-state.json` is machine-private legacy state.
Migration moves its durable local meaning into Gateway storage and ensures that it
is ignored by Git. A future `.wormhole/local/` path, if introduced, is also ignored
and is never needed to bootstrap a clone.

### 7.3 Canonical encoding and integrity

- JSON files use the versioned typed schema, UTF-8, deterministic field order,
  deterministic map-key order, LF line endings, and one trailing newline.
- Markdown bodies use UTF-8 and LF line endings with one trailing newline.
- Checkpoint formatting is deterministic; formatting-only churn is a bug.
- The tree digest is computed from sorted relative paths and canonical file bytes.
  No generated digest or index file is tracked, avoiding a merge hotspot.
- Duplicate IDs, path/record ID mismatches, broken required references, unknown
  kinds, and invalid current schemas reject the entire import.
- Unknown future snapshot versions fail closed. Supported old versions migrate
  deterministically and are written back only by an explicit checkpoint.
- Unknown optional fields are preserved only where the versioned schema explicitly
  defines an extension envelope; arbitrary ignored security fields are forbidden.

Portable `EventV1` records and Git links are live-only, immutable, and add-only.
Replaying the same ID
and byte-identical canonical value is idempotent; the same ID with different
canonical bytes is one generic immutable-record integrity conflict. Neither kind
has a tombstone or resurrection path. Deleting any mutable actor, task, task-link,
KB, or channel record writes a typed tombstone at that record's stable path rather
than silently removing the path. A tombstone carries the stable ID, entity kind,
deleted-content digest, and deletion attribution; delete-versus-edit is an explicit
merge conflict. For an existing mutable record, `created_at` is immutable on
ordinary update. An explicit digest-proven resurrection may supply a fresh valid
`created_at` because the tombstone does not retain the prior record bytes.

Three-way reconciliation treats lifecycle evidence conservatively. From an old
tombstone, exact endpoints coalesce and a side that remains equal to the old tombstone
yields to the other endpoint, including a changed tombstone or live resurrection. Only
two divergent non-base endpoints conflict: divergent resurrection, or resurrection
opposed by a changed tombstone, is `invalid_resurrection`; divergent changed tombstones
are root `same_field`. From an old live mutable record, exact dual tombstones coalesce
and unequal dual tombstones are root `same_field`. Concurrent unequal mutable additions
at one previously absent ID conflict rather than synthesising an absent object as a
merge base. Tombstones are never selected by timestamp. For KB articles, conflict
evidence is emitted independently for `record.json` and `/body` whenever both surfaces
carry competing meaning, so neither side's body can disappear from the audit evidence.

Endpoint provenance remains outside the snapshot-only merge. Reducer replay and direct
import/materialisation journals prove that a tombstone or resurrection was authorised;
three-way reconciliation compares only the validated endpoint lifecycle. Existing
immutable Events and Git links are clean only when both endpoints equal the old record,
even if both sides made the same mutation. After validating `oldBase`, reconciliation
preflights raw mutable disappearance in `newBase` and then `candidate` before validating
either side, preserving the dedicated raw-deletion error and deterministic side/kind/ID
precedence. Event and Git-link disappearance is instead side-validated first: an invalid
reference graph returns its typed validation error, while a valid disappearance becomes
immutable-record conflict evidence.

Lifecycle equality is byte-exact, including `updated_at`. Timestamp metadata selection
applies only when merging two live versions of an old-live record. In an old-live
tombstone race, a timestamp-only live difference is not a semantic edit, so the tombstone
wins without selecting a timestamp. Concurrent KB adds and resurrections are
surface-aware: root evidence exists only when record JSON differs, and `/body` evidence
only when body presence or content differs. Final typed/reference validation runs only
on a conflict-free assembled shell; if that shell is invalid, the typed error is returned
rather than inventing a conflict kind.

Markdown merge hunks use half-open base-line ranges. An insertion strictly inside a
replacement conflicts; an insertion at its start or end boundary remains independent
and is emitted before or after that replacement respectively. After equality and
one-sided fast paths, exact minimum-edit reconstruction refuses a side comparison above
100,000,000 dynamic-programming cells before allocation. It returns a typed merge-limit
error and a zero in-memory merge result; the caller's already-held prior candidate and
persistent state remain unchanged. It never substitutes an approximate merge.

For a single-file entity, the tombstone replaces its JSON record. For a KB article,
`record.json` becomes the tombstone and `body.md` is absent; a tombstoned KB directory
that still contains `body.md` is invalid. The KB tombstone also records the deleted
body digest. From an old live KB article, a one-sided tombstone is clean when the
opposing live endpoint has no semantic edit; deletion racing a record or body edit
conflicts, with root and `/body` evidence emitted independently. Re-creation at the same
stable ID requires an explicit resurrection operation that names the tombstone digest.

Versioned schemas classify references as live-required or historical. A missing target
is always broken. A tombstone satisfies historical event, Git-link, attribution, and
relationship-edge references, which resolve as deleted rather than dangling; it does
not satisfy a live-required structural reference. This distinction is validated from
the candidate tree and prevents deletion from erasing audit history or silently leaving
a live hierarchy attached to a deleted record.

Snapshot version, project ID, and canonical repository identity are immutable binding
fields and must agree across every accepted base, direct candidate, composed view, and
rebase input. Project handle and `remotes.toml` are Git-base-owned: overlay operations
cannot change them, semantic diff omits them, and rebase requires the candidate to retain
the old base values before taking the new base values. A normal candidate load still
accepts old-base handle/remotes after Git advances; it validates the immutable binding
fields rather than incorrectly comparing those Git-owned values with the newer base.

Tracked actors are attribution claims, not authentication or membership. A clone
or fork may create any actor claim just as it may edit any other repository file;
Git review determines whether that content is accepted.

## 8. Gateway base, overlay, and checkpoint

### 8.1 Workspace state

For each checkout, Gateway maintains:

```text
accepted Git base + checkpointed working-tree delta + active overlay
  = current Wormhole workspace view
```

The accepted base is the validated `.wormhole/` tree at a specific observed Git
commit and computed tree digest. A checkpointed candidate is validated project
state materialised in the working tree but not yet accepted by a Git commit. The
active overlay contains durable operations not yet materialised. Gateway stores
the overlay and a private materialisation journal scoped by `workspace_id`,
`project_id`, accepted-base digest, candidate digest, and checkout identity.
Workspace status exposes both the exact composed candidate digest and the overlay
generation through which that candidate was reproduced, separately from the accepted
snapshot.

Checkpoint publication never advances the accepted base. Only when a same-symbolic-ref
Reject/Refresh observation sees a new checked-out Git commit containing the candidate
tree does that Git tree become the new workspace base and the matching materialisation
journal enter terminal accepted state. Publication alone never advances the accepted
base. Direct
`.wormhole/` edits are allowed, but Gateway validates and imports their working-tree
delta before composing it with an overlay.
Only one published or recovered-new checkpoint may await Git acceptance per workspace.
A second Checkpoint returns `ErrCheckpointPendingAcceptance` before staging and preserves
all later overlay; Wormhole never silently supersedes the first journal.

Import performs a bounded canonical no-follow capture before its database transaction,
honours an optional canonical expected digest, then repeats the exact read under one
immediate transaction before mutation. A byte or digest change returns
`ErrWorkingTreeChanged` with zero writes. Import immediately deep-clones every retained
filesystem/repository reader result and, after that second capture matches, revalidates
canonical root and checkout identity immediately before conflict replacement, its first
write; byte-identical checkout replacement still fails when device/inode changes. It also
requires pre-existing open-conflict evidence and `conflicted` workspace status to agree
before replacement. Missing prior record paths are classified in
canonical kind/UUID order before the next typed decode: mutable disappearance is
`ErrDirectPathDeletion`, while Event/Git-link disappearance is
`ErrDirectImmutableRecordMutation`. The exported direct-delta validator accepts no
materialisation proof; only a private repository-owned match against an
acceptance-eligible published or recovered-new journal, accepted base, checkout, and
exact candidate bytes/digest may authorize its exception. That match receives the
complete disposition proof, eligible row, binding, canonical prior tree, captured tree,
and captured digest explicitly. The same disposition is the sole operation inventory:
active at/below the selected boundary or rebased above it is corruption; every valid
active row is composed and only that exact preloaded membership transitions to rebased.
Import never uses a filtered active-row read or generation-range update.
Import has no request ID or receipt. Indeterminate COMMIT returns a zero result plus
`ErrCommitOutcomeUnknown`; retry repeats the full capture/recomputation and never infers
the original result from readback state.

A machine-private stash records its semantic pre-stash rebase base separately from replay
selection. `source_tree` and `source_base_digest` identify that semantic base, normally
the accepted snapshot. A strict canonical `StashReplayV1` in the existing operations
column records schema version, the exact selected composition start tree/digest, its
explicit initial through-generation, the absorbed rebased prefix, and active operations
above that boundary.
Stash obtains those memberships from one same-transaction, non-nil complete operation
audit, not separate filtered reads. The audit contains every exact-workspace active,
rebased, materialized, stashed, and discarded row as a stable
increasing-generation sequence of records that embed the exact operation and retain its
creation time. It fails closed on any invalid generation, duplicate or noncanonical
operation identity, noncanonical operation bytes, unknown state, invalid owner metadata,
or invalid timestamp. Stash maps every embedded operation, in returned order and without
filtering or omission, into a non-nil count-preserving operation inventory for the pure
planner. Audit creation times remain retry evidence and are not planner inputs. The
planner derives ownerless rebased rows at/below the selected boundary and ownerless active
rows above it, and rejects active rows at/below the boundary or rebased rows above it.
Terminal materialized/stashed/discarded rows are strictly validated but remain unchanged
and do not enter the replay or transition sets.
Only the planner's exact derived cloned memberships transition; filtered reads and
generation-range updates cannot define Stash ownership.
Already-rebased rows at or below the boundary move to terminal stashed state because
their effect is in the selected start tree; they appear in a non-nil absorbed-prefix
array, while later active rows appear in a separate non-nil suffix array. All and only
rows with that stash's immutable ownership ID must match the two arrays byte for byte. An
immediate post-rebase stash therefore has an empty suffix array with initial and final
through-generations equal. Restore proves suffix replay from that selected start
reproduces the composed stash, then semantically rebases it with the separately retained
source base; clean restore leaves both original arrays terminal and owner-attributed.

Two worktrees of the same project have different workspace IDs, base digests,
overlays, Fabric stream bindings, and Code Graph revisions. They cannot leak pending
state into each other.

### 8.2 Project operations and parity

Human CLI and agent MCP calls invoke the same Gateway domain operations. Both can
create, update, link, or tombstone project records and can inspect, diff, import,
stash, or checkpoint the result. Direct repository edits remain possible and are
ingested through validation and import; Wormhole does not claim exclusive write
ownership over `.wormhole/`.

Authentication, authenticator recovery, membership administration, ownership
transfer, and local service/connector installation remain CLI control-plane
operations because they handle human secrets or machine administration. This does
not restrict what an agent can change in a fork or propose through Git.

Parity means equivalent authorised project operations, not equal product-priority
weighting. MCP remains a first-class agent contract rather than a thin CLI adapter:
schemas, bounded retrieval/progressive disclosure, autonomous durability, attribution,
and handoff are agent-first. Human CLI workflows provide comprehensible setup, review,
and machine/security control without reserving ordinary project operations to humans.

### 8.3 Diff and checkpoint

`wormhole status` and `wormhole diff` expose the exact `candidate_tree_digest` and an exact
`publication_review_digest`. The latter is the SHA-256 digest of the versioned canonical
envelope frozen by the 2026-08-01 publication-classification/review-CAS amendment. It binds
exact workspace scope, canonical repository identity, trusted semantic-origin digest,
publication classification and monotonic policy revision, acceptance ref and commit,
accepted-tree digest, candidate tree digest, canonical semantic-diff digest, and overlay
generation. It changes if any publication-relevant input changes.

`wormhole diff` renders the semantic accepted-base-versus-current-view diff in
stable entity order, combining a checkpointed working-tree delta and active overlay
and including actor attribution and tombstones.

Publication visibility is trusted machine-private setup/workspace state resolved by the
service with exact values `unclassified`, `local_only`, `public_git`, or `private_git`;
it is independent of canonical/fork routing and Fabric mode and is never selected by a
checkpoint caller, actor assurance, or copied hint. Unknown visibility remains
`unclassified`. `public_git` setup warns that structurally valid context can still disclose
incidents, customer information, security weaknesses, roadmaps, or personal discussion.
For `public_git`—including canonical repositories and public forks—checkpoint requires an
attributed explicit acknowledgement
whose digest exactly equals the freshly recomputed `publication_review_digest` before
staging, journal creation, or filesystem publication. The CLI and MCP expose the same
acknowledgement capability; it records intent and supplies a compare-and-swap
precondition, not extra authority or a human-only approval. `unclassified` permits
inspect/status/diff but blocks checkpoint; `local_only` and `private_git` remain governed
by their respective Git visibility boundaries.

Publication visibility is an explicit user policy, not a claim that Wormhole continuously
knows a Git host's access controls. Its private record is bound to the exact workspace,
repository identity, and credential-free semantic-origin digest. After first
configuration, an observed origin/repository-identity change stickily invalidates it to
`unclassified` and advances its policy revision; changing visibility otherwise requires an
explicit setup reconfiguration, which also advances the revision and invalidates every
earlier publication-review digest. Status/diff always display the current classification.
Wormhole performs no implicit network visibility probe and cannot detect a same-identity
host changing from private to public; the operator must reclassify before publishing
through Git. Exact storage, canonical codecs, race behavior, and migration ownership are
frozen by the 2026-08-01 amendment.

`wormhole checkpoint`:

1. locks the workspace checkpoint operation and opens an immediate preparation
   transaction against the expected current `.wormhole/` working-tree digest;
2. validates the accepted base, any imported working-tree delta, overlay, and exact
   open-conflict gate;
3. renders, fsyncs, validates, and digests a complete canonical candidate tree in a
   sibling staging location;
4. rechecks the live working-tree digest as a compare-and-swap precondition and aborts
   without publication if a direct edit raced with the checkpoint;
5. commits a prepared journal containing both complete trees, the accepted-base and
   candidate digests, checkout identity, included operation bytes/states, exact
   through-generation, and the canonical version-1 publication-review envelope/digest plus
   checkpoint actor in its durable prepared record/receipt before mutating the live tree;
   `public_git` additionally requires that exact digest as the caller acknowledgement,
   while every new local/private journal retains the same proof without requiring it;
6. opens a second `BEGIN IMMEDIATE` after that prepared commit and, before rename or
   exchange, reloads and rechecks the exact live digest, binding, candidate, overlay
   generation/rows, and open-conflict gate;
7. holds the second transaction across atomic directory exchange where supported (or
   durable fallback publication), parent-directory fsync, and the published journal,
   candidate, and included-operation-row updates;
8. commits those database updates only after the new complete live tree is durable,
   while leaving the accepted Git base unchanged; and
9. retains enough prepared-journal information for restart recovery to recognise either
   the old or new complete live tree, restore/finalise its matching database state, or
   recognise the later accepting Git commit.

Checkpoint materialises only operations already classified as portable. It never scans,
selects, summarises, or promotes operational activity as a side effect.

Before staging or journal creation, checkpoint queries open conflicts with the exact
project/workspace predicate inside its preparation transaction. After the prepared row
commits, the second immediate transaction repeats that gate and every publication
precondition and holds the SQLite writer barrier through filesystem and database
publication. Thus a conflict opened in the post-journal/pre-rename window yields zero
publication, a zero checkpoint result, and `localstore.ErrWorkspaceConflicted`; the
caller obtains preserved candidate digest/generation separately from status. Resolved
conflicts and conflicts in another workspace do not block. There is no second sentinel
or runtime alias.

Gateway never commits or pushes Git unless a separately explicit future command is
designed. The resulting files enter the user's normal diff, commit, push, and pull
request workflow.

### 8.4 Git changes and semantic rebase

When Git changes the accepted base while a checkpointed candidate or overlay is
pending, Gateway performs a three-way rebase using old base, new base, and the
combined candidate-plus-overlay change. Conflict evidence is always oriented as
`Base=oldBase`, `Ours=candidate`, and `Theirs=newBase`:

- a change on only one side is accepted except where lifecycle, immutable-record, or
  existing-record `created_at` invariants forbid it;
- identical changes on both sides coalesce except mutations of an existing immutable
  Event or Git link;
- disjoint typed fields merge deterministically;
- conflicting changes to the same typed field are surfaced;
- Markdown bodies use a deterministic three-way text merge and retain explicit
  conflict state when unresolved;
- event or Git-link byte disagreement is the same generic immutable-record
  integrity conflict; and
- edit-versus-tombstone is always explicit.

Raw disappearance is not a valid one-sided semantic change. Removing an existing
Event or Git link produces immutable-record conflict evidence; removing a mutable record
without replacing its stable path with a valid tombstone rejects the rebase/import.
Direct-delta validation compares only the prior direct surface with the new direct tree
and any bound materialisation exception. It accepts a correctly formed new tombstone;
the three-way rebase alone compares that tombstone with the overlay and owns the explicit
tombstone/edit conflict. `ImportResult.ImportedChangeCount` is exactly
`len(SemanticDiff(priorSurface, liveSnapshot, nil).Changes)`; overlay changes, the merged
result, and use of the matching-materialisation exception do not alter this direct count.

Semantic fields use RFC 6901 JSON Pointer paths, with `""` as the record root and
`/body` as a KB Markdown body. A complete root `FieldValue` contains canonical JSON for
the concrete typed record in schema field order. Generic sorted-map JSON exists only
transiently while recursively merging object members; roots are rehydrated into their
typed record before assignment or evidence emission. Arrays are atomic. Field values
carry an explicit present/absent envelope so absent is not confused with JSON `null`.
Conflicts sort by entity kind, record ID, field path, kind, and a canonical SHA-256 ID
derived from a versioned tuple containing the complete base/ours/theirs values. The
same inputs therefore reproduce byte-identical conflict evidence and ordering.
Persisted evidence separates deterministic semantic `conflict_id` from UUIDv4
`occurrence_id`. One open occurrence per workspace and semantic ID is allowed; identical
open evidence retains its occurrence, absent evidence resolves it without deleting
history, and a later reopening receives a new occurrence. Reads strict-decode every
envelope, rehydrate typed roots, recompute IDs, and require canonical sort order.

Markdown merge canonicalises LF first and computes deterministic minimum-edit,
old-base-relative LCS hunks. Equal-cost choices prefer advancing the base/deletion side;
hunks then sort by base start, base end, and inserted bytes. Non-overlapping hunks merge,
identical insertions at one anchor coalesce, and unequal same-anchor insertions or
overlapping replacement/deletion hunks conflict. Conflict markers never enter a
snapshot.

`updated_at` is post-semantic-merge metadata, never precedence: one semantic editor
contributes its timestamp; two cleanly merged semantic editors contribute the later UTC
timestamp; no semantic edit retains the old-base timestamp. Timestamp-only edits are
ignored. `created_at` changes on an existing live mutable record are invalid ordinary
updates and are never silently selected.

There is no last-write-wins fallback. A conflict result retains a complete validated,
byte-identical copy of the prior composed candidate, while deterministic
`Base=oldBase`, `Ours=candidate`, `Theirs=newBase` triples retain the complete evidence;
it never returns a partial merge or partially adopted Git-owned state. Persistence of
the new direct tree, prior candidate surface, absorbed overlay generation, operation-row
state transitions, conflict triples, and conflicted workspace state is atomic. A
conflicted workspace remains locally usable for unaffected records but cannot checkpoint
or use a writable Fabric path until resolved. Other workspaces and Fabric connections
continue normally.

A branch switch with proposal state requires one explicit choice: checkpoint/commit as
applicable, stash, or discard. Proposal state for discard applicability is exactly a
candidate, an `active` or `rebased` operation, or open conflict evidence. Git observation
offers only Reject and Discard; stash is a separate operation performed after
`ErrBranchSwitchPending`, followed by Refresh and Recover. Stash, restore, and discard
use canonical UUID request IDs and immutable canonical transition receipts. Same-ID,
same-request retries are read-only; changed request content returns
`ErrIdempotencyConflict`. Dedicated tagged v1 request-digest projections bind the exact
scope and actor plus label or stash ID; discard also binds the complete private
adapter-supplied resolved expected binding,
canonical root, and expected commit. Golden tests freeze their canonical bytes and
digests. An indeterminate database commit wraps `ErrCommitOutcomeUnknown`, and retry
reuses the same request or operation ID. An exact clean receipt may prove its commit;
a conflicted restore receipt must pass complete retry-state verification before proving
success. Stashes
remain machine-private and retain the semantic source base, the selected start tree and
digest, initial replay boundary, absorbed rebased operations, later active stored
operations, and candidate
digest. Stash itself rejects any exact-workspace open conflict with
`localstore.ErrWorkspaceConflicted` and zero mutation; success atomically leaves the
binding `clean`. Restore first proves the selected replay composes to the stored candidate,
then calls the semantic equivalent of
`ThreeWayRebase(sourceBase,current,stashComposed)`. Only a clean restore transitions
absorbed rows, persists the merged candidate, deletes the stash, and leaves status
`pending`. A conflicted restore leaves candidate and every operation row byte-identical,
persists only deterministic conflict evidence plus `conflicted` status, and retains the
full stash for resolution/audit. Stash, restore, and discard use explicit action-specific
private tagged v1 codecs: `stashResultV1`/`stashReceiptV1`,
`restoreStashResultV1`/`restoreStashReceiptV1`, and
`discardResultV1`/`discardReceiptV1`. Each receipt contains schema version, action,
outcome, and result; restore alone also carries its conflict retry digest. Conflict
arrays are non-nil (`[]`, never `null`), optional pointers encode explicit `null`, and
golden result/receipt bytes and hard-coded canonical digests cover every action/outcome.
A transition receipt's logical key is the exact textual project/workspace/request triple.
For detection only, localstore readers CAST-match all three persisted key columns so BLOB
aliases are exposed. A present key requires exactly one match, exact raw key values, and
TEXT storage for every selected column; zero matches must satisfy the strict all-null
absence shape, and multiple matches are corruption. Inserts strict-preflight the same
logical key inside the caller-owned immediate transaction, preventing hidden logical
duplicates without a schema change.
Localstore treats `result_json` as action-opaque: it requires exactly one valid compact
JSON value followed by exactly one LF, preserves those bytes and object-member order, and
never schema-decodes or generic-map re-encodes the value. The action-specific ProjectState
codec owns schema, tags, member order, exact re-encoding, and semantic canonicality and
rejects noncanonical bytes.

BranchSwitchDiscard has stricter lookup order. After pure request validation and canonical
digest computation, it calls binding-free `TransitionReceiptByKey`, which syntactically
validates scope/request ID and queries only `workspace_transition_receipts`. It CAST-matches
the textual triple and selects `COUNT(rowid) OVER()` plus exact columns/types. It requires
raw TEXT key/storage equality, maps `sql.ErrNoRows` to nil, returns a strict record only
for count one, and treats any other returned count as corruption/ambiguity; it never
queries `workspace_bindings`. This occurs before any filesystem, Git, or current-binding
check.
An exact strict receipt returns its complete result read-only with zero Git calls and
writes; another action or digest for the same ID returns `ErrIdempotencyConflict`, and
corrupt or ambiguous state fails closed. Only absence permits outside observation.

The Discard transaction uses `WithImmediateWorkspaceTransition`, not the ordinary
workspace transaction seam. After syntactic validation and `BEGIN IMMEDIATE`, its first
SQL read is that same receipt-table-only lookup. It passes the receipt and bound-scope
transaction to the callback. A concurrently committed exact receipt wins read-only; only
nil permits the callback to read the workspace or other state, require complete binding
equality, preload complete candidate/operation/conflict/materialization state, and
reobserve checkout/ref/HEAD before writes. Existing `WithImmediateWorkspace` and
`TransitionReceipt` behavior remains unchanged for Stash and RestoreStash.

The conflicted restore receipt binds explicit action/outcome, its tagged result, and a canonical digest
over scope, request ID/digest, stash ID, and the complete post-conflict
binding/status/accepted snapshot, candidate, all operation
rows including creation times, retained stash, and sorted open occurrences/evidence.
That state includes binding `created_at` and `updated_at` plus localstore-computed
exact-byte digests for the raw accepted-snapshot, candidate direct/rebased, and stash
source/composed canonical file-list BLOBs. Each BLOB digest is `"sha256:" +
lowerhex(SHA256(raw))` over the complete raw BLOB, with no canonicalization, framing,
prefix, or separator in the hash input, after strict decode and byte-equal canonical
re-encoding; runtime copies the fields rather than recomputing them from decoded trees,
and literal-byte golden vectors freeze the domain. This requires no schema change.
The envelope action/outcome equal the receipt row and uses the existing `result_json`
column without a schema or migration change.
The first conflicted attempt rereads post-state and proves the accepted snapshot, every
non-status binding field, binding `created_at`, candidate, operations, and stash equal
their prewrite values. Only the exact status/`updated_at` change caused by
`SetStatus("conflicted")` and deterministic open-conflict replacement may differ; their
post-values must equal the computed mutation. Exact repeat strict-composes current rows,
recomputes semantic evidence and the digest, and is read-only with zero writes only when
the retry-time state, including both binding timestamps, equals the recorded post-state;
a same-status timestamp rewrite fails closed. It is never blind replay. This is a
cryptographic commitment at the retry linearization point, not proof that no intermediate
mutation was later reversed. Clean restore receipts survive stash deletion; conflicted
retries verify the retained stash, complete current rows, and evidence rather than trust
cached output. Clean restore builds its candidate from the self-contained stash, leaves
original stash rows terminal stashed, and moves only newly absorbed current active rows
to rebased.

Discard applicability is evaluated only after the no-receipt transaction has preloaded
state, rechecked in-transaction Git position, and strictly proved/classified the complete
materialization disposition. Prepared, orphan/unclaimed or nonterminal materialized,
corrupt/ambiguous terminal ownership, and any incomplete ownership proof fail first as
their recovery/corruption blocker. Historical `accepted`/`recovered_old` rows are
nonblocking only after their complete proof. Only a proved-safe disposition continues.

Discard is applicable only to an actual symbolic-ref change, including a
same-SHA ref change, with at least one exact proposal-state kind above. Otherwise it
returns `ObserveGitBaseResult{}` plus `ErrBranchSwitchDiscardNotApplicable`, creates no
receipt, and changes nothing; an exact earlier receipt still wins before this check.
Reject may advance a ref switch with no proposal normally, and same-ref changes use normal
acceptance/rebase under Reject. Under Reject, any actual symbolic-ref change with proposal
always returns `ErrBranchSwitchPending` and cannot accept a materialization. Discard
instead proceeds through its proved applicability and materialization-match rules.
Applicable discard
marks active and rebased proposal rows
discarded, deletes the candidate, resolves open conflicts, leaves stashed and
accepted-journal materialized rows untouched, records its receipt, and advances the
observed base atomically in the existing write order. Orphan/nonterminal materialized
rows still block pending recovery.

Import, Git observation, recovery, and discard obtain one complete exact-workspace
materialization disposition in their transaction before matching an eligible journal.
Accepted, published, and recovered-new journals own materialized rows; prepared blocks a
stable proof and drives recovery, while recovered-old is excluded. Globally unique
generation and operation-ID claims must byte-match an ownerless materialized row and form
a bijection with every materialized row. A nil legacy accepted envelope contributes no
claim and is permitted only without dependent residual materialized history. Each owning
journal also rejects an omitted active/rebased row at or below its boundary; terminal
stashed/discarded gaps and later active rows are allowed. Historical prepublication state
is established by the exact checkpoint transition plus its envelope, not reconstructed
from a later row. This proof adds no database schema or migration.

Reject and the Discard no-receipt path perform a full read outside the transaction. Under
one immediate transaction, they validate the private adapter-supplied resolved expected
binding, equal scope, and matching root; compare the complete current binding; preload
the complete state and materialization disposition; and reobserve checkout identity,
symbolic ref (empty means detached), and HEAD immediately before the first write. That is
the linearization boundary. Same-SHA ref changes count, and external changes afterward
are caught by the next mandatory refresh.

Only same-symbolic-ref Reject/Refresh may accept an exact proved `published` or
`recovered_new` materialization match, retain it as `accepted`, and preserve later generations.
Discard never accepts a materialization: an exact match returns
`ObserveGitBaseResult{}` plus
`ErrBranchSwitchDiscardNotApplicable`, with no receipt or mutation and byte-identical
journal, candidate, materialized history, conflicts, operations, and binding. A
nonmatching proved acceptance-eligible row blocks same-ref Reject rebase and applicable
Discard with `ErrGitMaterializationPrecondition`, retaining the same bytes. A `prepared`
row still requires recovery. Historical `accepted` and `recovered_old` rows do not block.
This uses the existing states and migration and preserves the clean-discard codec:
`CandidateAccepted == false`, nil journal, no rebase, and no conflicts.

Trusted zero-actor same-ref rebase preserves an existing candidate's `ImportedBy` and
`ImportedAt` exactly. With no candidate it uses the fixed non-authority origin
`system:git-observation-rebase-v1` and the single UTC transaction observation time
captured after in-transaction Git reobservation and before the first write. Candidate
importer validation is exactly canonical UUID or that token, including stash and retry
reads. Observation never borrows an operation actor or fabricates an ActorEnvelope or UUID.

After an unknown applicable-Discard COMMIT, a fresh exact receipt is read without Git;
success requires its strict action, digest, and complete result to equal the attempted
transition. Every other readback returns `ObserveGitBaseResult{}` plus the original error
wrapping `ErrCommitOutcomeUnknown`. A non-discard ObserveGitBase unknown COMMIT always
returns that zero result and sentinel without receipt or state inference. Refresh injects
the validated resolved binding, but returns `types.WorkspaceBinding{}` on this error;
private adapters do likewise, and no CLI/MCP client supplies binding or routing context.
Gateway never guesses that pending state belongs on the destination branch.

## 9. Multi-Fabric routing and Git-aware streams

### 9.1 Local profiles and bindings

Gateway stores many machine-private Fabric connection profiles. A workspace binding
has the logical shape:

```text
workspace_id
  -> project_id
  -> base tree digest
  -> optional fabric_instance_id
  -> optional remote_project_id
  -> optional stream_id
  -> optional credential_ref
```

One Fabric may host many projects and streams. One Gateway may connect different
workspaces to different Fabrics. The Fabric instance ID is part of every remote key,
so identical project UUIDs or handles on two Fabrics never collide.

The binding's workspace, project, checkout, Fabric-instance, remote-project, and
writable-stream identities are immutable until an explicit rebind. The base tree digest
is evolving observed state attached to that binding and is included as a precondition on
every relevant sync operation; the credential reference names a stable local slot whose
secret may rotate. Updating an observed digest, display handle, or credential never
retargets the binding to a different project, checkout, Fabric, or stream.

Missing, stale, or ambiguous bindings fail closed. Gateway never chooses a Fabric
from a display name, URL similarity, or last-used profile. A failure on one Fabric
does not block local work or another Fabric profile.

### 9.2 Local and fork mode

A clone works immediately without Fabric, authentication, invitation, or membership.
The local human identity is self-declared and all project operations remain available.

A fork inherits the Git snapshot and project lineage but does not inherit access to
the upstream Fabric. When its `origin` repository identity differs from the canonical
identity, the copied hint is inactive. The fork may remain local-only or attach to a
different Fabric under its own realm and binding. Changes return upstream through a
normal Git pull request.

### 9.3 Canonical public Fabric

A canonical public repository may commit an identification-only Fabric hint. Setup
activates it only when `origin` matches the canonical repository identity. No
Wormhole account, browser login, invitation, or project membership is required.

Gateway presents a self-issued public identity and proves continuity with its local
identity key. This establishes only that actions came from the same pseudonymous
actor; it does not verify a legal identity or Git-host account.

Every canonical branch uses an isolated stream derived from independently observed
repository identity, ref, and commit. Fabric may accept identified live operations
into that branch stream. The accepted default-branch stream advances only when
Fabric independently observes the canonical Git default ref moving to a commit with
a valid `.wormhole/` snapshot tree. Anonymous or pseudonymous branch activity never
mutates accepted `main` directly.

Fabric reads only the Git metadata and `.wormhole/` subtree needed for this decision.
It neither executes nor stores source bodies.

### 9.4 Private Fabric

Private Fabric requires an authenticated human principal and an authorised project
membership. Workspace and agent credentials derive from that membership. Removing
membership or ownership revokes future remote synchronisation but never deletes the
local base, overlay, or historical attribution.

Private repositories may require a Git-provider installation or deploy credential
so Fabric can independently observe allowed refs. That server-side integration is
separate from a developer's local Git credentials.

### 9.5 Synchronisation conflicts

Fabric replication uses record versions and base preconditions. A remote operation
that no longer applies to the workspace base becomes a durable conflict and enters
the same semantic rebase path as an incoming Git base. Fabric cannot resolve a Git
divergence by timestamp. Events and Git links remain add-only immutable records, and
mutable records use explicit optimistic conflict detection.

Every Fabric operation transition persists the operation ID, shared canonical
`OperationV1` bytes, and shared canonical JSON digest. Every read, restart reconstruction,
and duplicate-ID replay uses `projectstate.DecodeOperation`, requires decoded ID to match
the row, requires `CanonicalOperation` byte equality, and checks
`DigestCanonicalJSON`; malformed, unknown-field, trailing, noncanonical, ID-mismatched,
or digest-mismatched rows fail closed before any state is served or replayed.

Before a v2 writable push resolves credentials, constructs a client, performs DNS, signs,
or contacts Fabric, Gateway checks the shared exact-project/workspace open-conflict gate.
An open conflict returns the typed non-transient
`localstore.ErrWorkspaceConflicted`, reports
attention required, and leaves the complete queue row byte-identical and pending. After
a successful remote response, delivery marking uses one local `BEGIN IMMEDIATE` to
recheck the same predicate immediately before its complete-key queue update. No local
transaction is held across the network: if a conflict opens in flight after Fabric has
accepted the operation, the local recheck deliberately leaves the identical operation
pending, and retry after explicit resolution reuses its operation ID/bytes for idempotent
server replay. If delivery commits before a later conflict opens, that later conflict
does not retroactively undeliver the earlier operation. Other workspaces and resolved
conflicts do not block or share this gate.

### 9.6 Operational retention

Operational storage has three V1 retention classes:

1. **Ephemeral presence:** presence and heartbeat state is memory-only and may be
   discarded on restart.
2. **Bounded activity:** task-transition chatter, progress, generic channel activity,
   subscriptions, runtime attribution, telemetry, and uncurated discoveries default to
   30 days and the newest 10,000 unprotected records per workspace. A row is eligible
   when either its UTC `created_at` is older than 30 days or it falls outside that newest
   10,000 set. Pruning is deterministic in `(created_at, activity_id)` ascending order;
   the stable ID breaks timestamp ties.
3. **Lifecycle evidence:** pending queues, open conflicts, recovery state, and receipts
   are excluded from both age and rank pruning until terminal. The default post-terminal
   duration is exactly 30 days; a project/Fabric may advertise a longer duration only if
   it is finite. After that deadline the row is eligible regardless of rank. Protected
   rows may temporarily make the workspace exceed the activity cap.

Gateway and Fabric must expose the effective finite policy before accepting live
activity. There is no indefinite catch-all class. Expiry and pruning never mutate an
accepted Git base, a working-tree candidate, or any other portable state; only an
explicit typed promotion can copy selected operational meaning into portable state.
The exact operational schema, policy handshake, and atomic terminal/pruning seam are a
hard design-and-TDD gate before portable projection/domain work begins.

## 10. Human identity, authentication, and agent accountability

### 10.1 Separation of concerns

The model separates:

- **principal identity:** the durable human or agent being attributed;
- **authentication:** evidence used to establish control of a principal;
- **authorisation:** project-scoped grants held through membership or local Git
  contribution workflow;
- **ownership:** the human accountable for an agent at action time; and
- **session:** the harness/model/runtime instance that performed an agent action.

Possession of a tracked actor file grants none of these remote capabilities.

### 10.2 Assurance modes

| Mode | Human assurance | Remote authority |
|---|---|---|
| local or fork | self-declared local profile | none required; Git review accepts changes |
| canonical public Fabric | self-issued key continuity | identified branch-stream participation |
| private Fabric | authenticated stable principal | explicit project membership and derived grants |

Assurance travels with every action and is never upgraded merely because content is
merged. Git acceptance and Wormhole actor assurance are separate facts.

### 10.3 Human principals and authenticators

Private Fabric stores stable human principals separately from authenticators and
project memberships. A human may link multiple authenticators and must have a
recovery path that does not depend on email alone.

Default CLI authentication is browser OIDC Authorization Code with PKCE using a
loopback callback, S256 challenge, state, nonce, and exact issuer/audience checks.
Headless environments use RFC 8628 device authorisation. Self-hosted deployments may
offer WebAuthn/passkeys. Personal access tokens are automation-only, scoped, expiring,
revocable, and never the interactive default.

### 10.4 Agent principals, ownership, and sessions

An agent is a distinct durable principal, not an alias for its owner. One human may
own multiple agent profiles for different harnesses, roles, or purposes. An ownership
or sponsorship record is versioned and project-scoped where appropriate.

An agent action carries a typed actor envelope:

```text
actor_kind: agent
agent_id: <durable agent principal>
accountable_human_id: <owner captured at action time>
session_id: <harness execution session>
harness: <name and version>
model: <declared model and version, when known>
assurance: <local | public-key-continuity | private-authenticated>
occurred_at: <timestamp>
```

Human actions use the same envelope with `actor_kind: human` and no agent owner.
Ownership means accountability; it does not claim that the human personally initiated
each action. Transfer, revocation, or membership removal never rewrites historical
envelopes.

Private agent credentials and Passport issuances bind directly to the agent, current
accountable human, project membership, permissions, and expiry. Membership removal,
ownership revocation, or credential expiry stops subsequent remote actions.

### 10.5 Local Git identity integration

During setup, `git config user.name` and `user.email` may seed suggested local profile
metadata. Both remain explicitly self-declared. Email is not committed to `.wormhole/`
unless the user intentionally marks it public.

Wormhole may optionally link its public identity key to the configured Git signing key
through a signed attestation. The signing operation remains inside Git/GPG/SSH-agent
boundaries; Wormhole never reads a signing private key.

All Git network operations continue through `git`, the SSH agent, or the configured
credential helper. Wormhole does not inspect helper output or reuse Git-host tokens as
Fabric credentials. Private Fabric may separately offer explicit GitHub or GitLab OAuth.

## 11. `wormhole setup` and command surface

### 11.1 One setup lifecycle

`wormhole setup` replaces the legacy joining and combined connection flows. It runs a
single discoverable, idempotent lifecycle:

1. discover the nearest `.wormhole/` directory;
2. parse it as data and validate schema, project ID, repository identity, references,
   and computed tree digest without executing repository content;
3. resolve all choices, render one complete plan, and obtain its single confirmation
   before external mutation;
4. install or start the one user-level `gatewayd` service;
5. register the checkout without importing its base;
6. create or select a local human identity using optional Git metadata suggestions and
   create its local identity key if needed;
7. have that locally assured human configure the trusted publication class as exactly
   `unclassified`, `local_only`, `public_git`, or `private_git`, bound to Gateway's own
   current repository/origin observation;
8. refresh and import the Git base through Gateway using the selected accountable actor;
9. detect supported harnesses and transactionally install approved connectors;
10. activate an independently eligible public Fabric binding, request private
    authentication, or remain local-only; repository mode and hints never authorize it;
11. fetch a compatible remote delta and bootstrap content when connected;
12. ask whether to enable Code Graph and, if selected, complete its initial build; and
13. verify Gateway, workspace, publication policy, selected identity, connectors,
    optional Fabric, and optional Code Graph readiness.

Interactive setup presents one plan and confirmation, then reports each stage. A
machine-readable journal makes stages independently retryable. Repository manifests
are never executed. Local setup succeeds if Fabric is absent or unavailable. A
connector failure leaves the imported workspace intact and retryable. A Code Graph
failure leaves the project usable and the graph build retryable.

`wormhole join` and `wormhole connect` are removed in this alpha architecture rather
than retained as aliases: both encode the obsolete assumption that local usability,
agent registration, remote enrolment, and harness installation are one remote-first
operation.

### 11.2 Intended command families

```text
wormhole setup [--code-graph=on|off] [--non-interactive]
wormhole status
wormhole project list
wormhole fabric list|attach|detach|login
wormhole connector list|install|remove
wormhole code-graph status|query|rebuild|disable
wormhole diff|import|checkpoint|stash
wormhole mcp
```

Interactive setup asks whether to enable Code Graph. Non-interactive setup requires an
explicit `--code-graph=on|off` choice when no existing project preference is available.
There is no build profile flag.

The project-state commands have agent MCP counterparts
`wormhole.workspace.status|diff|import|checkpoint|stash`. These are RFC-0003 local
runtime operations, not a fifth Core pillar. Core entity changes continue through their
existing pillar tools, while setup, private login, Fabric/connector administration, and
Code Graph disablement remain human control-plane operations. The contract inventory is
updated atomically with the migration that introduces these names.

### 11.3 Transactional connector installation

Each harness adapter implements discover, inspect, plan, apply, verify, rollback, and
remove. Before mutation, setup records the exact existing Wormhole connector entry and
validates the selected `wormhole` executable. It applies the smallest supported harness
change, verifies the resulting command, and restores the prior entry exactly if
verification fails. A replacement must never irreversibly remove a working connector
before rollback information exists.

The first-party Codex adapter uses Codex's supported MCP configuration command:

```text
codex mcp add wormhole -- /absolute/path/to/wormhole mcp
```

“Native Codex connector” means a first-party, tested Wormhole installer and lifecycle
adapter for Codex. The runtime protocol remains the common stateless `wormhole mcp`
bridge; no Codex-only Gateway protocol is introduced.

Claude Code and other supported harnesses follow the same transactional adapter
contract. Unsupported or unavailable harnesses are reported without failing the rest
of setup.

## 12. Model-free Code Graph

### 12.1 Purpose and boundary

Code Graph helps an agent locate the relevant code path and request bounded source with
less repository scanning. It is derivative, project- and workspace-scoped,
revision-scoped, local to one checkout, and rebuildable from Git.

The current Go implementation uses `go/packages`, `go/types`, and `go/ast` to build
package, file, symbol, type, import, call, reference, and containment relationships. It
does not require a local model, embeddings, vector storage, or vector search.

### 12.2 Retrieval pipeline

The graph adds a deterministic revision-scoped lexical index over:

- symbol and qualified names;
- signatures and type names;
- package and file paths;
- declaration documentation and package comments; and
- identifier segments such as camel-case, acronym, underscore, path, and digits.

Retrieval prioritises exact qualified and exact symbol matches, then a versioned
FTS/BM25-style lexical score with fixed tokenisation, field weights, normalisation, and
stable-ID tie-breaking. A deterministic query planner classifies definition, caller,
callee, reference, type-use, package-overview, and topic searches. Compiler-derived
edges rerank and expand the best anchors. The index implementation may change only with
an analysis-fingerprint version change and the same deterministic acceptance corpus.

Responses use progressive disclosure:

1. a compact repository/package map and match reasons;
2. a small set of candidate files and one-hop relationships;
3. deterministic package, file, and symbol outlines; and
4. bounded, hash-validated source only for selected paths and authorised callers.

The persistent index stores graph structure and search terms, not complete source
files or function bodies. Source is assembled transiently from the approved checkout
and checked against the indexed revision.

### 12.3 Setup and worker operation

Setup asks one question: whether to enable Code Graph. If selected, the complete initial
build runs before graph readiness is reported. The candidate revision publishes
copy-on-write; failure preserves the previous ready revision.

After setup, the CLI exposes `wormhole code-graph status|query|rebuild|disable`.
Gateway exposes equivalent project-operation tools
`wormhole.code_graph.status|query|rebuild` through MCP; disabling the workspace-local
facility remains a human machine-configuration operation. Status, query, and rebuild
use the same explicit workspace binding and authorisation checks.

Every graph revision stores a source fingerprint and an analysis fingerprint. The source
fingerprint covers canonical tracked source paths and bytes. The analysis fingerprint
commits to that source fingerprint plus every tracked non-source input consumed by the
adapter (for Go, module/workspace manifests and checksums), normalised build constraints,
target and adapter configuration, graph/adapter schema version, and compiler/toolchain
identity. Adapter implementations must declare their complete fingerprint input set.

Every status or query recomputes the current analysis fingerprint with the same canonical
inventory algorithm used by the indexer and compares it with the active revision. Dirty
tracked source, manifest, or configuration changes are included; the fingerprint is not
merely the current commit ID. A mismatch reports indexed and current analysis
fingerprints, indexed and current source fingerprints where they differ, whether the
tracked checkout is dirty, `graph_not_current=true`, and `rebuild_recommended=true`.
Query fails closed before returning navigation matches, edges, or source when the graph
is not current, so a last-known-good revision is never mistaken for the current checkout.

Rebuild is explicit. It indexes the exact current tracked analysis view, publishes
copy-on-write only if the analysis fingerprint is unchanged at publication, and binds
the new graph to both fingerprints. A failed or concurrently invalidated rebuild
preserves the prior revision for diagnostics and recovery, but that revision remains
unavailable to query while its analysis fingerprint is stale. Restart and binary or
adapter upgrade repeat the comparison before serving the graph. Disable stops the
workspace worker and removes its derivative store and local enablement state. No
filesystem watcher or automatic rebuild is required for this release.

The worker uses normal OS scheduling. There are no compute warnings, Balanced or
Warpspeed profiles, “unrestricted hardware” mode, or model download. Those concepts are
deferred unless a future vector/ML design creates a demonstrated need.

The worker receives a read-only approved checkout view and no Fabric credentials.
Language tools must not execute repository code. If a language tool requires a missing
dependency download, setup requests separate network consent or fails with a precise
offline diagnostic; enabling Code Graph is not implicit permission to access the
network.

### 12.4 Language evolution

The graph exposes a language-neutral package/file/symbol/edge intermediate model.
Go remains the first compiler-backed adapter. Future languages add explicit compiler or
parser adapters. Unsupported languages appear as honest opaque file/module boundaries;
Wormhole does not invent semantic edges.

Vectors are reconsidered only if blind held-out evaluation demonstrates a material
concept-discovery gap that deterministic identifiers, documentation, and graph structure
cannot close.

## 13. Failure and recovery model

| Failure | Required outcome |
|---|---|
| invalid tracked snapshot | reject atomically; retain prior valid base and explain exact path/schema error |
| Fabric unavailable | local base and overlay remain fully usable; queue retry survives restart |
| Fabric credential revoked | stop remote sync; preserve local state, conflicts, and history |
| Gateway restart | reopen every valid workspace, overlay, queue, and checkpoint journal before serving it |
| connector apply/verify failure | restore prior connector entry; leave workspace setup intact |
| Code Graph worker crash | keep Gateway and last published graph revision healthy; report retryable failure |
| tracked analysis input, adapter, or toolchain changes after Code Graph publication | mark graph not current and fail query closed until explicit successful rebuild |
| Git base changes with candidate/overlay | semantic three-way rebase; never timestamp overwrite |
| branch switch with pending state | require checkpoint/commit, stash, or discard |
| one workspace conflicts | isolate conflict; other workspaces and Fabrics continue |
| v2 push sees an open workspace conflict before or during delivery | make no credential/network contact when known before push; otherwise retain the byte-identical pending row after the atomic delivery recheck and retry the same operation only after resolution |
| checkpoint interruption | recover either previous complete tree or new complete tree from durable journal |
| concurrent direct `.wormhole/` edit | fail checkpoint compare-and-swap without overwriting the edit; validate/import then retry |
| conflict opens after checkpoint prepared commit | second immediate recheck returns `localstore.ErrWorkspaceConflicted` with zero publication and retains recoverable prepared evidence |
| private authentication failure | local setup succeeds; remote stage remains incomplete and retryable |
| copied upstream hint in fork | leave hint inactive; do not contact or write upstream Fabric |

Setup and migration journals contain no raw credential and are permission-restricted.
Diagnostics redact bearer tokens, authorisation headers, callback codes, private keys,
credential paths where sensitive, and repository-private data not needed for the error.

## 14. Security model

- Gateway's local socket and state are protected by the OS-user boundary.
- Every local repository operation carries explicit workspace and project scope.
- Every Fabric operation carries Fabric instance, remote project, stream, actor,
  assurance, and credential scope.
- Public identification proves key continuity only; UI and audit output must not label it
  verified identity.
- Private authentication tokens are short-lived where protocols permit; refresh and
  automation tokens are encrypted or stored in a permission-restricted secret store.
- Raw tokens are returned only across the credential-establishment boundary and never
  enter tracked project state, event payloads, MCP output, or logs.
- Secret-shape validation is not confidentiality detection. `public_git` setup must warn
  before use, and checkpoint must enforce the matching publication acknowledgement
  CAS described in §8.3 for human and agent callers alike. Wormhole makes no DLP claim;
  direct Git edits and commits remain possible outside Wormhole.
- Private agent credentials derive from an accountable human and membership;
  revocation is enforced on subsequent remote requests.
- Repository content is untrusted data. Setup parses declarative schemas and never runs
  project-supplied commands, hooks, binaries, or model instructions.
- Code Graph workers receive least authority: one checkout, one graph store, no Fabric
  secrets, no implicit network, and no repository-code execution.
- Fabric validates canonical Git state independently before advancing accepted streams.
- Cross-project, cross-workspace, cross-Fabric, and fork/upstream isolation each require
  explicit negative tests.
- V1 authority is repository-lineage scoped. No repository implicitly imports an
  organisation-wide graph, KB, Constitution, or permission from another project.

## 15. Compatibility and migration

This is an alpha hard transition. It updates the compatibility inventory but does not
promise aliases for obsolete flows.

Migration must:

1. revise RFC-0001, RFC-0003, the canonical agent guide, implementation rules, README,
   command documentation, and contract inventory before code relies on the new model;
2. classify the prior alpha-validation specification and trial outputs as historical
   evidence where they describe `join`, Passport-only actors, Warpspeed, or remote-first
   bootstrap;
3. introduce the version-one `.wormhole/` schemas and deterministic codecs;
4. import compatible existing Gateway project records into a workspace overlay without
   committing secrets or local recovery state;
5. move or ignore legacy `.wormhole/integration-state.json` safely;
6. migrate single Fabric configuration into an explicit profile and workspace binding;
7. preserve stable project, agent, Passport, task, KB, channel, event, and Git-link IDs
   where their meaning remains valid;
8. create human principals and ownership links for legacy agents through an explicit
   migration/recovery flow rather than silently inventing verified humans;
9. invalidate or rotate legacy remote credentials when they cannot express the new
   human/membership/ownership binding; and
10. remove `join` and `connect` from CLI help, tests, examples, generated guidance, and
    compatibility inventory when the replacement setup path is complete.

No migration rewrites historical actor attribution. Legacy assurance remains labelled as
legacy or unknown until explicitly linked.

## 16. Verification and acceptance

### 16.1 Project-state tests

- Golden canonical encoding and decode/encode round trips for every tracked record.
- CLI and MCP workspace operations produce the same semantic status, diff, import,
  checkpoint, and stash outcomes for the same actor and workspace.
- Stable tree digest under repeated renders and filesystem enumeration order changes.
- Full-tree rejection for duplicate IDs, broken references, path/ID mismatch, secrets,
  and unknown versions.
- Public-Git status/diff return a stable publication-review digest; human CLI and
  agent MCP checkpoint both reject a missing, stale, or mismatched acknowledgement before
  staging/journalling/publication, while local/private modes preserve their Git boundary.
- Generic channel activity, status chatter, presence, queues, receipts, telemetry, and
  uncurated discoveries do not appear in the version-one `Snapshot`; only explicit typed promotion can
  add selected `EventV1` evidence, and checkpoint itself never promotes it.
- Retention tests cover restart-discarded presence, age-**or**-rank eligibility outside
  the newest 10,000 unprotected rows with stable-ID tie-breaking, exact default-30-day/
  configured-finite-longer terminal retention, policy negotiation before
  live acceptance, and pruning that cannot change portable Git state.
- Golden deletion/reimport tests cover single-file and KB tombstones, reject residual
  KB bodies, preserve valid historical references, and reject live-required references
  to deleted targets.
- Two-clone and two-worktree base/overlay/checkpoint isolation.
- Checkpoint leaves the accepted base unchanged until a same-symbolic-ref Reject/Refresh
  observation accepts a matching Git commit, then marks the retained materialisation
  journal accepted without losing later overlay work. Publication alone never advances
  the accepted base.
- Direct-edit/checkpoint races fail the digest compare-and-swap and preserve both inputs.
- A conflict opened after prepared-journal commit but before checkpoint rename is caught
  by the second immediate transaction and causes zero filesystem/database publication;
  restart recovery accepts recorded old or new live-tree states at every later fault.
- Three-way merges for disjoint fields, same-field conflict, Markdown conflict,
  Event/Git-link ID collision, tombstone/record edit, tombstone/KB-body edit,
  explicit resurrection, and crash recovery. Golden merge tests cover RFC 6901
  escaping and root/body paths, absent versus JSON `null`, sorted object traversal,
  atomic arrays, canonical conflict IDs/order, deterministic Markdown LCS ties and
  hunks, `updated_at` metadata selection, and immutable `created_at` on update.
- A conflicted rebase returns and persists the byte-identical complete prior candidate
  plus both-side evidence atomically, reproduces it after restart, and blocks checkpoint
  and writable Fabric delivery until explicit resolution.
- Stash restart tests distinguish semantic source base from selected replay start, cover
  accepted and direct selected starts with real active reducer-operation suffixes, a
  rebased start with no later operations, and an absorbed rebased prefix plus only later
  active operations. The direct-start case proves its suffix cannot compose from the
  accepted source. Complete-audit tests reject otherwise-hidden active rows at/below the
  boundary and rebased rows above it while proving valid terminal
  materialized/stashed/discarded rows are validated, preserved, and ignored. Restart tests
  prove the absorbed prefix plus later operations survive clean and conflicting paths.
  They also cover strict boundary/envelope/current-row corruption, open-conflict rejection
  with zero mutation, exact clean/pending/conflicted statuses, and read-only repeated
  access to retained conflicted evidence without candidate or operation-row changes.
  Before the Stash tranche commits, a hidden-BLOB receipt-key retry must fail with zero
  mutation, sibling isolation must include the same project with different workspaces,
  and unrestricted update-blocking triggers must prove an idempotent retry cannot rewrite
  owner, timestamp, or any other column.
- Discard tests prove an exact receipt is served before broken Git/current binding, the
  immediate-transaction recheck closes a concurrent first-commit race before state or Git
  reads, different action/digest conflicts, and corrupt or ambiguous receipts fail closed.
  A rejecting/query-recording `database/sql` driver proxy proves
  `TransitionReceiptByKey` reads only the receipt table and
  `WithImmediateWorkspaceTransition` makes that its first and only retry SELECT; no binding
  read occurs until receipt absence. Triggers are not SELECT-order evidence. Existing
  Stash/Restore seams are unchanged. Real SQLite covers zero, one, multiple, and
  hidden-storage-alias receipt rows.
  An applicability matrix covers unchanged refs, changed refs without proposal, each of
  candidate/active/rebased/open-conflict proposal state, same-SHA ref changes, and exact
  prior receipt precedence. Changed-ref terminal-only stashed, discarded,
  accepted-journal materialized, and resolved-conflict histories are not applicable with
  no receipt/base change and byte-identical restart state. Orphan/nonterminal-only and
  corrupt terminal ownership fail before applicability. Reject-without-proposal advances;
  under Reject, any ref change with proposal is pending, while applicable Discard follows
  its proved discard path.
- Materialization tests let same-ref Reject/Refresh accept exact `published` and
  `recovered_new` matches, while exact-match Discard is not applicable and preserves every
  state surface.
  They reject nonmatches for same-ref rebase and discard without changing journal,
  candidate, or materialized bytes across restart; require recovery for `prepared`; and
  ignore historical `accepted`/`recovered_old` as blockers. Same-ref rebase tests preserve
  existing candidate provenance or use only `system:git-observation-rebase-v1` and the
  single post-reobservation UTC time, with strict importer-union tests through stash/retry
  and a hard-coded system-token retry preimage/digest alongside the UUID golden.
- Rollback and restart tests fault every observer write. Unknown discard COMMIT succeeds
  only from an exact fresh receipt read without Git; all other readbacks return zero plus
  `ErrCommitOutcomeUnknown`. Non-discard observation never infers success, and Refresh
  returns no binding on that error.
- Checkpoint conflict tests require zero `CheckpointResult`,
  `localstore.ErrWorkspaceConflicted`,
  separate unchanged status digest/generation proof, and exact-scope isolation.
- A fork accepts and edits the base but cannot activate the copied upstream Fabric hint.

### 16.2 Identity and Fabric tests

- Local self-declared, public key-continuity, and private-authenticated actor envelopes.
- Multiple agents per human, ownership transfer, revocation, and immutable historical
  accountable-human attribution.
- OIDC PKCE validation, device flow, WebAuthn where enabled, token expiry, membership
  removal, and agent-credential revocation.
- Multiple Fabrics with duplicate handles/UUIDs never collide.
- One workspace cannot acquire two writable streams.
- Canonical branches isolate live state; accepted default-branch state advances only
  after independently observed Git ref movement and valid snapshot import.
- Fabric stream restart/replay rejects malformed, unknown-field, trailing, noncanonical,
  operation-ID-mismatched, and digest-mismatched stored operation rows before serving a
  version or applying a later transition.
- Fabric outage or one-profile failure does not block local work or another profile.
- V2 push conflict gates run before credential/network side effects and atomically before
  delivery marking; tests cover exact scope, an in-flight conflict, byte-identical pending
  rows across restart, typed non-transient classification, and exact-operation retry after
  resolution.

### 16.3 Setup and connector tests

- Fresh public clone, fork, private clone, local-only clone, repeated setup, partial
  failure, restart recovery, and non-interactive setup.
- Fake-harness lifecycle tests for discover, inspect, apply, verify, rollback, and remove.
- Real Codex and Claude Code smoke tests where their CLIs are available.
- Connector rollback reproduces the exact prior entry after injected failure.
- No setup fixture can execute repository-supplied content.

### 16.4 Code Graph tests

- Exact qualified symbols rank top one; unqualified symbols rank top three.
- On an entry-symbol-free held-out corpus, primary-file recall at five is at least 90%,
  expected-file recall at ten is at least 90%, and expected structural-path recall after
  bounded expansion is at least 90%.
- Identical revision and query produce byte-identical ordering across ten runs.
- Median discovery exposes at most five files and 8 KiB before the correct path.
- Controlled same-task/same-role trials reduce files or bytes opened before the correct
  path by at least 30% without a correctness regression.
- At 250,000 symbols and 2,000,000 edges, warm query p95 is at most 300 ms and query
  memory growth is at most 128 MiB.
- Dirty tracked source, module/workspace manifest, or adapter configuration makes status
  report the indexed/current analysis fingerprints and makes query fail closed; a
  successful explicit rebuild restores readiness against the exact dirty-tree analysis
  fingerprint.
- Build-constraint, target, graph/adapter-version, and compiler/toolchain identity changes
  invalidate readiness even when all source bytes are unchanged.
- Failed and concurrently invalidated rebuilds preserve the prior revision without
  serving it as current, and restart re-establishes the same freshness result.
- Disable removes the selected workspace's derivative graph and local enablement without
  changing tracked project state or another workspace.
- Tests prove zero model download, inference, vector call, implicit network access,
  stale-source return, cross-revision mix, or permission bypass.

### 16.5 Programme gates

- Focused tests precede implementation for each behaviour change.
- `make check`, release tests, migration verification, race tests, security checks, and
  connector/CLI contract checks pass.
- Merged statement coverage remains at or above the approved 80% floor.
- Documentation and compatibility inventories match shipped commands and schemas.
- The portable loop is proven from clone through second-clone reconstruction before any
  Code Graph, multi-Fabric, private identity, or OIDC delivery branch proceeds.
- The external four-VM trial uses three independent Gateway/harness installations and a
  fourth Fabric VM, exercises public or private identity as configured, proves shared
  context and branch isolation, records recovery evidence, and closes issue #56 only
  when Gate D and every subsidiary gate pass.

## 17. Delivery decomposition

The work is deliberately split into bounded, independently verified slices. Detailed
implementation plans are written only after this design document is reviewed.

### Slice 0 — Authority reconciliation

- Reconcile RFC-0001 and RFC-0003.
- Update canonical agent and implementation guidance.
- Mark conflicting dated roadmaps/specifications as historical rather than rewriting
  recorded evidence.
- Freeze the new terminology and command direction in the alpha contract inventory.

### Slice A — Portable project foundation

- Versioned `.wormhole/` schemas, codecs, validation, and tree digest.
- Project handles, canonical repository identity, and local workspace IDs.
- Snapshot import, durable overlays, semantic diff, stash, rebase, and checkpoint.
- Local actor envelopes and machine-private/tracked boundary migration.

### Slice B — Gateway supervisor and workers

- One service/socket with persisted multi-workspace routing and the minimal local-only
  provider needed by setup.
- Explicit request routing and cross-workspace isolation.
- Code Graph remains disabled for the portable-loop validation.

### Slice C — Setup and native connectors

- Idempotent journalled `wormhole setup`.
- Gateway service installation and readiness checks.
- Local Git identity suggestions and optional signing attestation.
- Transactional Codex, Claude Code, and other supported harness adapters.
- Removal of legacy `join` and `connect` flows.
- Clone/setup/inspect/mutate/diff/checkpoint/reconstruct trial with Code Graph disabled.

### Slice D — Git-aware multi-Fabric

- Fabric profile registry, committed hints, immutable routing, and stream protocol.
- Public identification keys and canonical repository/ref verification.
- Branch streams, accepted-default-ref projection, and upstream-fork rejection.
- Semantic precondition conflicts and local-first independent failure handling.

### Slice E — Private human authentication and accountability

- Human principals, authenticators, project memberships, and recovery.
- Agent ownership/sponsorship, sessions, Passport-bound credentials, and typed audit.
- OIDC PKCE, device flow, WebAuthn option, revocation, and private Git observation.
- Replace legacy viewer/admin-key assumptions with authenticated human sessions where
  the private human surface requires them.

### Slice F — Migration and external trial

- Legacy data, credential, configuration, and documentation migrations.
- Upgrade, downgrade where safe, recovery, and compatibility verification.
- Packaging and installation rehearsal across supported systems.
- Three harness/Gateway VMs plus one Fabric VM trial and issue #56 Gate D evidence.

This branch stops after Slice A plus the minimum Slice-B/C supervisor, setup, and native
connector path proves the portable loop and passes a fresh whole-branch review. The
result is an experimental/internal capability and requires an explicit human go/no-go.
Slice D multi-Fabric and Slice E private identity/OIDC proceed on the issue-56 path only
after that decision. They own shared Gateway schema through version 7. Optional Code
Graph proceeds on a separate branch based after version 7, owns migration `000008`, and
is not an issue-56 prerequisite. Slice F and issue #56 retain their ultimate gates and
integrate only explicitly approved, completed contracts.

## 18. Rejected alternatives

### One full Gateway daemon per project

Rejected because it multiplies runtimes, sockets, database pools, service management,
upgrades, and baseline memory while failing to remove indexing cost. One supervisor with
isolated workers provides the useful fault boundary without fragmenting the user surface.

### Checked-in Gateway SQLite database

Rejected because binary databases, WAL files, machine-private tables, concurrent
mutation, and opaque Git conflicts make it unsafe and unreviewable. Typed files are the
portable base; SQLite remains a local projection and overlay store.

### One aggregate snapshot JSON file

Rejected because unrelated edits collide and reviews become noisy. Stable per-record
files and separate edges minimise merge surface.

### Fabric as the accepted source of project truth

Rejected because it prevents unauthenticated fork workflows and can overwrite repository
history. Fabric streams accelerate collaboration; canonical Git commits accept it.

### Name- or URL-guessed Fabric routing

Rejected because two Fabrics can host identical handles and URLs can be renamed or copied.
Routing uses immutable instance, project, repository, workspace, and stream identities.

### Upstream Fabric access from forks

Rejected because copied configuration would allow unrelated fork activity to consume or
pollute upstream collaboration. Forks inherit only Git project state.

### Passport-only identity

Rejected because it conflates an agent credential with the accountable human,
authenticator, project membership, and historical actor. Those are separate typed
relationships.

### Vector-first Code Graph

Rejected for the current system because compiler structure plus deterministic lexical
retrieval provides a cheaper, offline, explainable navigation path. Vectors remain a
future evidence-driven option, not an assumed dependency.

## 19. Explicitly deferred decisions

- Portable or partially reusable Code Graph snapshots in `.wormhole/`.
- Vector/embedding retrieval and any associated model distribution.
- Compute profiles, resource warnings, and Warpspeed associated with a future ML graph.
- Additional language adapters beyond the first compiler-backed implementation.
- Managed-cloud billing and organisation administration beyond required private identity.
- Automatic Git commit or push from `wormhole checkpoint`.
- Cross-Fabric replication of one workspace.
- Organisation-wide/cross-repository graphs, merged KBs, inherited policy, and
  cross-project authority.

Each deferred feature requires a separate approved design. None should leave placeholder
flags, dormant schema, or speculative dependencies in the current implementation.

## 20. Definition of complete

The experimental portable-loop gate is satisfied when a new engineer can clone a
canonical public, forked public, private, or local-only repository; run `wormhole setup`;
use a human or agent surface against the same local project operations; inspect, mutate,
diff and publication-review that context; checkpoint a clean reviewable `.wormhole/`
diff without staging, committing, pushing, or advancing Git; accept it through normal
Git policy; and reconstruct the accepted state from a second clean clone/setup. Status,
diff, and import must not advance Git, and operational activity must not enter the second
clone unless it was explicitly promoted.

That gate ends this branch as an experimental/internal capability and requires an
explicit human go/no-go. The issue-56 path still requires private actions to retain
authenticated human and accountable agent provenance, all failure/isolation gates, at
least 80% coverage, and the four-VM external trial plus every subsidiary gate. The
model-free Code Graph may improve navigation on its separately approved branch but is
not part of that closure condition.
