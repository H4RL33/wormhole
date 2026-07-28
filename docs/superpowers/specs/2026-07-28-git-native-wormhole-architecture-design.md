# Git-Native Wormhole Architecture Design

**Date:** 2026-07-28

**Status:** Architecture approved; document review pending

**Decision owner:** Harley Welsh

**Scope:** Project bootstrap, Gateway topology, Git-native collaboration,
multi-Fabric routing, human and agent identity, harness connectors, and local
Code Graph operation

## 1. Outcome

Wormhole becomes a Git-native collaboration layer that a human or agent can use
immediately after cloning a repository. The tracked `.wormhole/` directory is a
portable, reviewable project base. One local `gatewayd` supervisor maintains a
durable working overlay for every checkout, exposes the same project operations
to humans and agents, and optionally synchronises a workspace with one explicitly
bound Fabric stream.

Git remains the acceptance and merge authority. Fabric accelerates live
collaboration and bootstraps connected Gateways; it does not become a second Git
host or an authority that can overwrite divergent repository state.

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
- Humans and agents have parity for project work: either can inspect and change
  tasks, knowledge, events, links, and other project context and submit the result
  through the repository's normal Git workflow.
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
  tracked Wormhole project state.
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
- No secret is committed beneath `.wormhole/`.
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

Fabric stores organisational state and Git metadata, not code bodies. It may read
Git refs and the `.wormhole/` subtree to verify branch and accepted-main state. It
must not execute repository content.

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

### 7.2 Tracked versus private state

Safe tracked state includes:

- project metadata and aliases;
- self-declared actor display records and public identity keys;
- tasks and task links;
- KB records and Markdown bodies;
- channel definitions and immutable events;
- Git commit, branch, and pull-request links;
- explicit tombstones; and
- non-secret Fabric discovery hints.

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

Events and Git links are live-only, immutable, and add-only. Replaying the same ID
and byte-identical canonical value is idempotent; the same ID with different
canonical bytes is one generic immutable-record integrity conflict. Neither kind
has a tombstone or resurrection path. Deleting any mutable actor, task, task-link,
KB, or channel record writes a typed tombstone at that record's stable path rather
than silently removing the path. A tombstone carries the stable ID, entity kind,
deleted-content digest, and deletion attribution; delete-versus-edit is an explicit
merge conflict. For an existing mutable record, `created_at` is immutable on
ordinary update. An explicit digest-proven resurrection may supply a fresh valid
`created_at` because the tombstone does not retain the prior record bytes.

For a single-file entity, the tombstone replaces its JSON record. For a KB article,
`record.json` becomes the tombstone and `body.md` is absent; a tombstoned KB directory
that still contains `body.md` is invalid. The KB tombstone also records the deleted
body digest. Deletion racing any record or body edit conflicts, and re-creation at the
same stable ID requires an explicit resurrection operation that names the tombstone
digest.

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

Checkpoint publication never advances the accepted base. When Gateway observes a
new checked-out Git commit containing the candidate tree, that Git tree becomes the
new workspace base and the matching materialisation journal can retire. Direct
`.wormhole/` edits are allowed, but Gateway validates and imports their working-tree
delta before composing it with an overlay.

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

### 8.3 Diff and checkpoint

`wormhole diff` renders the semantic accepted-base-versus-current-view diff in
stable entity order, combining a checkpointed working-tree delta and active overlay
and including actor attribution and tombstones.

`wormhole checkpoint`:

1. locks the workspace checkpoint operation and records the expected current
   `.wormhole/` working-tree digest;
2. validates the accepted base, any imported working-tree delta, and overlay;
3. renders a complete canonical candidate tree in a sibling staging location;
4. validates the candidate and computes its tree digest;
5. rechecks the live working-tree digest as a compare-and-swap precondition and
   aborts without mutation if a direct edit raced with the checkpoint;
6. publishes the candidate with an atomic directory exchange where supported, or
   a durable recovery journal with equivalent all-or-recover semantics elsewhere;
7. marks the included overlay generation as materialised in a private journal,
   while leaving the accepted Git base unchanged; and
8. retains enough private recovery information to restore the prior complete tree,
   reconstitute the overlay, or recognise the later accepting Git commit.

Gateway never commits or pushes Git unless a separately explicit future command is
designed. The resulting files enter the user's normal diff, commit, push, and pull
request workflow.

### 8.4 Git changes and semantic rebase

When Git changes the accepted base while a checkpointed candidate or overlay is
pending, Gateway performs a three-way rebase using old base, new base, and the
combined candidate-plus-overlay change:

- a change on only one side is accepted;
- identical changes on both sides coalesce;
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

Semantic fields use RFC 6901 JSON Pointer paths, with `""` as the record root and
`/body` as a KB Markdown body. Object members merge recursively in sorted-key order;
arrays are atomic. Field values carry an explicit present/absent envelope so absent is
not confused with JSON `null`. Conflicts sort by entity kind, record ID, field path,
kind, and a canonical SHA-256 ID derived from a versioned tuple containing the complete
base/ours/theirs values. The same inputs therefore reproduce byte-identical conflict
evidence and ordering.

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
byte-identical copy of the prior composed candidate, while deterministic triples retain
the complete upstream/direct evidence; it never returns a partial merge. Persistence of
the new direct tree, prior candidate surface, absorbed overlay generation, operation-row
state transitions, conflict triples, and conflicted workspace state is atomic. A
conflicted workspace remains locally usable for unaffected records but cannot checkpoint
or use a writable Fabric path until resolved. Other workspaces and Fabric connections
continue normally.

A branch switch with an active overlay or uncommitted checkpoint candidate requires
one explicit choice: checkpoint/commit as applicable, stash, or discard. Stashes
remain machine-private and retain their source base and candidate digests. Gateway
never guesses that pending state belongs on the destination branch.

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
   and computed tree digest;
3. install or start the one user-level `gatewayd` service;
4. register the checkout and import its Git base;
5. create or select a local human identity using optional Git metadata suggestions;
6. create a local identity key if needed;
7. detect supported harnesses and transactionally install approved connectors;
8. activate an eligible public Fabric hint, request private authentication, or remain
   local-only according to repository mode;
9. fetch a compatible remote delta and bootstrap content when connected;
10. ask whether to enable Code Graph and, if selected, complete its initial build; and
11. verify Gateway, workspace, connectors, optional Fabric, and optional Code Graph
    readiness.

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
| checkpoint interruption | recover either previous complete tree or new complete tree from durable journal |
| concurrent direct `.wormhole/` edit | fail checkpoint compare-and-swap without overwriting the edit; validate/import then retry |
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
- Private agent credentials derive from an accountable human and membership;
  revocation is enforced on subsequent remote requests.
- Repository content is untrusted data. Setup parses declarative schemas and never runs
  project-supplied commands, hooks, binaries, or model instructions.
- Code Graph workers receive least authority: one checkout, one graph store, no Fabric
  secrets, no implicit network, and no repository-code execution.
- Fabric validates canonical Git state independently before advancing accepted streams.
- Cross-project, cross-workspace, cross-Fabric, and fork/upstream isolation each require
  explicit negative tests.

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
- Golden deletion/reimport tests cover single-file and KB tombstones, reject residual
  KB bodies, preserve valid historical references, and reject live-required references
  to deleted targets.
- Two-clone and two-worktree base/overlay/checkpoint isolation.
- Checkpoint leaves the accepted base unchanged until a matching Git commit is
  observed, then retires the materialisation journal without losing later overlay work.
- Direct-edit/checkpoint races fail the digest compare-and-swap and preserve both inputs.
- Three-way merges for disjoint fields, same-field conflict, Markdown conflict,
  Event/Git-link ID collision, tombstone/record edit, tombstone/KB-body edit,
  explicit resurrection, and crash recovery. Golden merge tests cover RFC 6901
  escaping and root/body paths, absent versus JSON `null`, sorted object traversal,
  atomic arrays, canonical conflict IDs/order, deterministic Markdown LCS ties and
  hunks, `updated_at` metadata selection, and immutable `created_at` on update.
- A conflicted rebase returns and persists the byte-identical complete prior candidate
  plus both-side evidence atomically, reproduces it after restart, and blocks checkpoint
  and writable Fabric delivery until explicit resolution.
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
- Fabric outage or one-profile failure does not block local work or another profile.

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

- One service/socket with persisted multi-workspace and multi-Fabric binding registry.
- Explicit request routing and cross-workspace isolation.
- Isolated Code Graph worker lifecycle and per-checkout graph stores.
- Deterministic lexical retrieval, graph-aware expansion, and progressive disclosure.

### Slice C — Setup and native connectors

- Idempotent journalled `wormhole setup`.
- Gateway service installation and readiness checks.
- Local Git identity suggestions and optional signing attestation.
- Transactional Codex, Claude Code, and other supported harness adapters.
- Removal of legacy `join` and `connect` flows.
- Optional normal Code Graph initial build with no compute profiles.

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

Slices B and C may proceed in parallel after Slice A freezes their workspace and Gateway
interfaces. Slice D depends on A and Gateway routing from B. Slice E may develop its
Fabric schema behind frozen identity interfaces but cannot integrate before D's stream
binding exists. Slice F integrates only completed contracts.

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

Each deferred feature requires a separate approved design. None should leave placeholder
flags, dormant schema, or speculative dependencies in the current implementation.

## 20. Definition of complete

This architecture is implemented when a new engineer can clone a canonical public,
forked public, private, or local-only repository; run `wormhole setup`; use a human or
agent surface against the same local project operations; optionally connect to the one
correct Fabric stream; collaborate through typed durable context; checkpoint a clean
reviewable `.wormhole/` diff; and submit or accept that state through normal Git policy.

Completion also requires private actions to retain authenticated human and accountable
agent provenance, Code Graph to improve navigation without any ML dependency, all
failure/isolation gates to pass, coverage to remain at least 80%, and the four-VM external
trial plus all issue #56 subsidiary gates to be passing.
