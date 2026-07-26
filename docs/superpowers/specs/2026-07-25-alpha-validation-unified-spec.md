# Wormhole Alpha Validation Unified Specification

**Status:** Current canonical specification
**Date:** 2026-07-25
**Scope:** Next validated alpha release
**Supersedes:** `2026-07-25-alpha-validation-roadmap.md`, `2026-07-25-code-graph-alpha-validation-slice-ammendment.md`
**Ground truth:** RFC-0001 Wormhole Core, RFC-0002 Wormhole Governance, approved Code Graph design
**Implementation companion:** `docs/superpowers/plans/2026-07-25-alpha-validation-implementation-plan.md`
**Canonical root roadmap pointer:** [`ROADMAP-ALPHA-VALIDATION.md`](../../../ROADMAP-ALPHA-VALIDATION.md)

## 1. Purpose

This specification defines the complete programme required to validate Wormhole's current alpha thesis.

It combines the original Alpha Validation roadmap with the Code Graph Alpha Slice amendment. The amendment is incorporated directly rather than retained as an overlay. Where the two source documents differ, this specification adopts the amendment's revised critical path, milestone numbering, acceptance scenario, Definition of Alpha Validated, and pull-request sequence.

The release is an evidence-gathering alpha, not a beta-preparation release and not a declaration that every included capability is production-complete.

## 2. Alpha thesis

The alpha must prove this complete proposition:

> A fresh agent can connect through Gateway, understand how and when to use Wormhole, retrieve relevant project context, coordinate its work through the Task Graph and Event Bus, contribute durable knowledge, and make that state available to another agent without continual human instruction.

The Code Graph slice adds a narrower experimental proposition:

> A coding agent can use a local structural graph to identify the relevant code path and retrieve a bounded source package before resorting to broad repository searches and whole-file reads.

The alpha succeeds only if the complete organisational-context loop works and the Code Graph experiment produces measurable narrowing without degrading correctness.

## 3. Product and architectural boundaries

### 3.1 Core remains Core

Wormhole Core remains the four-pillar system defined by RFC-0001:

1. Communication through the Event Bus.
2. Coordination through the Task Graph.
3. Organisational memory through the Knowledge Base.
4. Identity and Permissions.

Code Graph is not a fifth Core pillar. Integration manifests are not a new Core pillar. Both are Gateway capabilities that support agent operation.

### 3.2 Governance remains separate

Wormhole Governance remains optional, additive, and independently adoptable. Constitution and Congress are outside this roadmap. Governance has no dependency on, control over, or authority over Code Graph.

### 3.3 Authority model

| Concern | Authority |
|---|---|
| Source code and source history | Git and the active working tree |
| Shared multi-runtime organisational state | Fabric-backed shared state |
| Local agent endpoint and normal operation | Gateway |
| Local durable replica and outbound queue | Gateway-owned SQLite |
| Locally approved integration state | Gateway |
| Project-approved manifest offered to runtimes | Fabric |
| Code Graph configuration and derivative index | Gateway on one machine |
| Code Graph source slices | Current files in the approved working tree, after hash validation |
| Human approval for repository-file materialisation | Human operator |

Fabric is authoritative only for shared multi-runtime state. It is not authoritative for the local Code Graph index, local checkout selection, or the operator's approval to modify repository files.

### 3.4 Non-negotiable boundaries

* Git remains the sole source of truth for code.
* Wormhole stores pointers and organisational context, not a competing repository.
* Harnesses connect to local Gateway, never directly to Fabric.
* Gateway remains deterministic and performs no generative LLM inference.
* Gateway writes supported local state durably before attempting synchronisation.
* Integration manifests are project-scoped, role-aware, declarative, and text-only in version one.
* Gateway must not download or execute arbitrary scripts, binaries, plugins, hooks, packages, or model-generated code.
* Repository files are installed or updated only through an explicit human action.
* Models may inspect current guidance but may not approve, replace, or silently alter it.
* Generated guidance must correspond to the current Gateway MCP registry.
* Code Graph is disabled by default, local, project-scoped, and Go-only for this alpha.
* Code Graph is not synchronised through Fabric.
* Code Graph never stores complete source files, function bodies, or returned context packages.
* Code Graph enablement, disablement, checkout selection, and destructive operations remain human-controlled.
* Agents cannot activate Warpspeed.
* The compatibility policy remains `alpha-inventory`.
* Managed cloud, human authentication, beta compatibility, and Governance remain outside the critical path.

## 4. Target topology

```text
Agent A
  |
  | MCP
  v
Gateway A
  | \
  |  \ local-only
  |   +--> Code Graph A --> approved working tree A
  |
  | synchronisation
  v
Fabric --> PostgreSQL
  ^
  | synchronisation
  |
Gateway B
  | \
  |  \ local-only
  |   +--> Code Graph B, if separately enabled
  |
  | MCP
  v
Agent B
```

Each Gateway owns its local SQLite replica and durable outbound queue. Code Graph is never transferred between Gateways. A second machine must build its own graph from its own approved checkout.

## 5. Critical path and permitted parallelism

```text
Repository rebaseline
        |
        v
Gateway-owned enrolment lifecycle
        |
        v
Offline Gateway restart
        |
        v
Integration-manifest design
        |
        v
Code Graph Alpha Slice
        |
        v
Generated AGENTS and SKILLS guidance
        |
        v
Meaning-bearing shared KB search
        |
        v
Fabric-distributed integration manifests
        |
        v
Two-agent alpha acceptance loop
        |
        v
Closed external validation
```

Permitted parallel work:

* Integration-manifest implementation and shared KB semantic-search work may proceed in parallel after the integration-manifest design is approved.
* Code Graph implementation and shared KB semantic-search work may proceed in parallel after Gateway lifecycle and local project binding are stable.
* Generated model guidance must wait until the relevant Gateway tool contracts are available.
* Fabric manifest distribution must wait until local schema, verification, rendering, ownership, and approval behaviour are approved and implemented.
* The full acceptance loop must use completed contracts rather than mocks at release-gate time.

## 6. Programme-wide delivery rules

Every implementation pull request must:

* begin with focused failing tests where behaviour changes;
* remain independently reviewable;
* update the alpha contract inventory when a public interface changes;
* run focused tests before the complete required suite;
* avoid unrelated refactoring;
* preserve Gateway, Fabric, Core, and Code Graph dependency boundaries;
* document externally observable behaviour;
* avoid claiming unmeasured performance;
* retain explicit error and omission metadata rather than silently degrading;
* use atomic writes or copy-on-write publication for durable state changes;
* preserve project and namespace isolation in every storage and query path.

Harness process sessions are ephemeral during alpha unless a concrete acceptance requirement proves otherwise. Passports identify agents, and credential profiles authorise local runtime access. Durable session records remain deferred.

---

# Milestone 0: Repository rebaseline

## Goal

Replace historical day-based planning with one roadmap that matches the implemented system and one issue inventory for the remaining alpha programme.

## Work

* Add this specification as `ROADMAP-ALPHA-VALIDATION.md` or link the canonical Superpowers spec from that path.
* Mark superseded roadmaps as historical rather than deleting them.
* Create a GitHub milestone named `Alpha Validation`.
* Attach issues `#8`, `#10`, `#24`, and the offline-startup portion of `#37`.
* Resolve issue `#3` using the alpha session decision in section 6.
* Create work items for:
  * Gateway-managed integration-manifest design;
  * generated guidance from the Gateway MCP tool inventory;
  * safe AGENTS and SKILLS materialisation;
  * Fabric distribution of project manifests;
  * Code Graph Alpha Slice;
  * autonomous Wormhole and Code Graph evaluation across models.
* Leave issues `#22`, `#23`, and `#36` outside the alpha milestone.
* Record every milestone, dependency, owner, and acceptance criterion in the issue tracker.

## Exit criteria

* One current roadmap governs the next alpha.
* Every alpha deliverable has one issue and one unambiguous acceptance criterion.
* No beta or Governance work appears on the critical path.
* The Code Graph work item explicitly states that it is an experimental alpha slice, not full V1.

---

# Milestone 1: Gateway-owned enrolment lifecycle

**Existing issue:** `#24`

## Goal

Make Gateway the sole owner of:

```text
Authentication
-> Enrolment
-> Bootstrap
-> Synchronisation
-> Normal operation
```

The CLI orchestrates human interaction and harness configuration, but does not independently own project enrolment or bootstrap behaviour.

## 1.1 Local enrolment request

Specify the request Gateway receives from `wormhole join` and `wormhole connect`.

It must include:

* project binding;
* owner;
* model;
* declared capabilities;
* repositories;
* requested roles;
* requested permissions;
* Fabric address;
* retry or idempotency identity.

Define explicit results for:

* unreachable Fabric;
* invalid project;
* rejected permissions;
* duplicate identity;
* canonical-repository mismatch;
* credential-persistence failure;
* enrolment success followed by bootstrap failure.

Add the request and result shapes to the alpha contract inventory.

## 1.2 Fabric registration through Gateway

* The CLI sends one enrolment request to Gateway.
* Gateway performs the Fabric registration handshake.
* Retries do not issue duplicate Passports.
* Project and namespace boundaries remain intact.
* The CLI performs no direct follow-on Fabric calls.

## 1.3 Credential persistence through Gateway

* Persist credentials only after successful enrolment.
* Use restrictive directory and file permissions.
* Write credential files atomically.
* Never expose bearer tokens in logs, events, test output, diagnostics, or model-facing responses.

## 1.4 Initial bootstrap through Gateway

Bootstrap:

* project metadata;
* agent identity;
* initial tasks;
* channels;
* KB state;
* applicable integration-manifest metadata.

Apply the bootstrap transactionally to the local SQLite replica. Incremental synchronisation begins only after bootstrap commits. Preserve a recoverable state when enrolment succeeds but bootstrap fails.

## 1.5 Process-level proof

Launch real CLI, Gateway, Fabric, PostgreSQL, and SQLite components.

Prove:

* a fresh identity enrols;
* Gateway creates the credential profile;
* non-empty project state reaches SQLite;
* retries do not duplicate Passports;
* CLI makes no direct post-request Fabric call;
* failed bootstrap remains recoverable.

## Exit criteria

* Gateway owns the complete lifecycle.
* CLI contains no independent bootstrap orchestration.
* Enrolment and bootstrap pass a real process-level test.
* Issue `#24` can close independently of the broader alpha demonstration.

---

# Milestone 2: Offline Gateway restart

**Existing issue:** `#37`, narrowed for alpha

## Goal

An already-enrolled Gateway starts and remains useful while Fabric is unavailable.

Creating a brand-new local-only organisation remains deferred.

## 2.1 Startup separation

* Open the credential profile and SQLite replica before network synchronisation.
* Serve the local MCP socket as soon as local state is valid.
* Start Fabric synchronisation asynchronously.
* Report remote unavailability without treating it as process failure.

## 2.2 Local reads and writes

While offline:

* serve local task, event, KB, identity, integration-guidance, and Code Graph status reads where applicable;
* make supported writes durable in SQLite;
* queue outbound changes for later delivery;
* preserve the queue across Gateway restart;
* reject operations requiring central authority.

## 2.3 Connection-state contract

Expose:

```text
online
offline
synchronizing
attention_required
```

The state is available through CLI and a read-only local MCP response.

## 2.4 Interruption and recovery proof

1. Bootstrap while Fabric is available.
2. Stop Fabric.
3. Restart Gateway.
4. Perform local reads and supported writes.
5. Restart Fabric.
6. Verify queued state synchronises exactly once.
7. Verify no local state is lost or silently overwritten.

## Exit criteria

* An enrolled Gateway starts without Fabric.
* Local agents remain productive during the outage.
* Queued writes survive restart and synchronise exactly once after recovery.
* Documentation no longer requires Fabric availability for every Gateway startup.

## Deferred

* brand-new local-only organisations;
* provisional local identities;
* peer-to-peer synchronisation;
* distributed identity issuance.

---

# Milestone 3: Gateway-managed integration-manifest design

## Goal

Approve the schema, trust model, ownership model, rendering model, lifecycle, and threat model for project-specific agent instructions before implementation.

## 3.1 Instruction classes

### Wormhole development instructions

Instructions for agents modifying Wormhole itself:

* root `AGENTS.md`;
* `agents/README.md`;
* Wormhole implementation and review skills;
* RFC and implementation-rule precedence.

These remain repository-owned development assets.

### Wormhole usage instructions

Instructions for an agent working inside any project connected to Wormhole:

* how to identify itself;
* how to inspect tasks, channels, agents, KB state, local status, and Code Graph state;
* when each Gateway tool should and should not be used;
* how to report progress, blockers, discoveries, reviews, and completion;
* what remains in Git rather than Wormhole;
* how to avoid event spam, duplicate tasks, and low-value KB pollution.

These are the subject of Gateway-managed integration manifests.

## 3.2 Version-one manifest structure

The design must define:

```text
schema_version
manifest_id
manifest_version
project_id
source
created_at
tool_contract_digest
role_filters
entries[]
```

Each entry defines:

```text
kind
target
content_digest
merge_policy
required
role_filters
```

Permitted entry kinds:

```text
agents_bootstrap
skill
reference
```

Prohibited entry kinds:

* executable scripts;
* binaries;
* shell commands;
* package dependencies;
* dynamic plugins;
* post-install hooks;
* arbitrary environment mutation.

## 3.3 Authority and approval

* Gateway owns the locally approved manifest state.
* Fabric may offer a project-approved manifest during bootstrap or synchronisation.
* Gateway verifies project scope, schema version, tool-contract compatibility, entry kinds, and content digests.
* Gateway presents a preview before applying new or changed repository files.
* A human explicitly approves initial application and material updates.
* Gateway caches the last approved manifest for offline operation.
* Models may inspect but cannot approve or replace guidance.
* Revocation removes only Gateway-managed content.

## 3.4 File ownership and merging

Canonical local layout:

```text
.wormhole/
  integration-state.json

.agents/
  skills/
    wormhole-orientation/
      SKILL.md
    wormhole-tool-use/
      SKILL.md
    wormhole-code-graph/
      SKILL.md
    wormhole-operating-loop/
      SKILL.md
    wormhole-contributor/
      SKILL.md
    wormhole-reviewer/
      SKILL.md
```

`AGENTS.md` rules:

* create only when absent;
* otherwise insert or update one clearly delimited managed section;
* preserve all user-owned text outside that section;
* show a diff before modification;
* write atomically;
* support removal and rollback;
* never rewrite the whole file solely to update the managed section.

Harness adapters may generate supported references, but canonical content remains the manifest.

## Deliverable

An approved design covering:

* schema;
* trust;
* ownership;
* role selection;
* rendering;
* merge behaviour;
* update behaviour;
* rollback;
* offline state;
* audit events;
* compatibility;
* threat model;
* test strategy;
* exact CLI names;
* exact read-only MCP name;
* managed-section marker syntax.

## Exit criteria

* Every RFC-0003 integration-manifest open question required for declarative text assets is resolved.
* Every remaining declarative-text question needed for alpha is resolved.
* Arbitrary code execution remains explicitly out of scope.
* Exact public names are entered into the alpha contract inventory.
* No manifest implementation begins before approval.

---

# Milestone 4: Code Graph Alpha Slice

## Goal

Implement the smallest coherent local Code Graph capability that can be dogfooded on the Wormhole Go repository during Alpha Validation.

The slice tests source-discovery value. It does not accept or complete the full Code Graph V1 design.

## 4.1 Included scope

* one active checkout per Wormhole project;
* canonical Git remote validation;
* Git-tracked files only;
* Go repositories only;
* repository, package, file, and symbol nodes;
* containment, definition, import, call, reference, and type-use relationships;
* deterministic symbol identities;
* one active completed graph revision;
* copy-on-write candidate publication;
* lexical and structural entry-point discovery;
* bounded structural traversal;
* bounded source-slice assembly;
* source-hash validation before return;
* explicit truncation and omission metadata;
* project-scoped permissions;
* Gateway MCP exposure;
* CLI enable, disable, status, rebuild, and checkout commands;
* generated model guidance;
* benchmark and dogfood evaluation.

## 4.2 Excluded scope

* TypeScript or JavaScript semantic adapters;
* Python semantic adapters;
* Rust semantic adapters;
* generic polyglot tree-sitter support;
* local embeddings;
* remote embedding providers;
* filesystem watchers;
* fine-grained incremental indexing;
* stale last-known-good region preservation;
* promoted dependency source;
* Warpspeed;
* pause and resume;
* shared audit events;
* durable Code Graph attachments on Tasks, Events, or KB articles;
* destructive in-place rebuild;
* automatic parser or model downloads;
* historical graphs for commits;
* compiler-complete control-flow or data-flow analysis.

## 4.3 Gateway-owned package boundary

```text
internal/runtime/codegraph/
  config
  store
  index
  golang
  query
  source
```

Responsibilities:

### `config`

* project enablement state;
* canonical remote;
* active checkout;
* project and global source-byte ceilings;
* enabled state.

### `store`

* graph metadata;
* completed and candidate revisions;
* files;
* symbols;
* edges;
* build diagnostics.

It must not persist complete source files, function bodies, or returned source packages.

### `index`

* Git-tracked file inventory;
* candidate revision creation;
* graph invariant validation;
* atomic active-revision publication;
* failed-candidate cleanup.

### `golang`

* package discovery through Go tooling;
* declarations;
* qualified names;
* signatures;
* imports;
* calls;
* references;
* type relationships;
* deterministic symbol fingerprints.

### `query`

* intent tokenisation;
* optional exact entry-symbol lookup;
* lexical symbol and package ranking;
* structural graph traversal;
* confidence and provenance ranking;
* source-budget allocation;
* omission reporting.

### `source`

* filesystem containment;
* path canonicalisation;
* working-tree reads;
* indexed-hash comparison;
* exact line and byte slicing;
* source omission on mismatch.

## 4.4 Graph model

Node categories:

```text
Repository
PackageOrModule
File
Symbol
```

Relationships:

```text
contains
defines
imports
calls
references
uses_type
```

Every edge records:

```text
source_node_id
target_node_id
relationship_type
confidence
provenance
graph_revision
```

Permitted provenance:

```text
go_packages
go_types
go_ast
parser
heuristic
```

Heuristic edges must never be presented as authoritative.

## 4.5 Deterministic symbol identity

Identity derives from stable declaration attributes rather than function bodies.

Required behaviour:

* body-only edits preserve identity;
* comments and whitespace preserve identity;
* signature changes may produce a new identity;
* renames produce a new identity;
* deterministic IDs cannot collide within one repository.

## 4.6 Revision model

Initial build and rebuild use coarse copy-on-write publication:

```text
retain active revision
-> enumerate tracked Go files
-> construct candidate graph
-> validate candidate
-> atomically publish candidate
-> retire previous revision
```

On failure:

* preserve the active graph;
* discard the candidate;
* expose diagnostics;
* report `degraded` or `error`;
* never expose a partial graph.

A dirty working tree does not automatically trigger indexing. Status and query responses report that the checkout changed since the active revision.

A caller may receive:

```text
graph_not_current
rebuild_recommended
```

An agent may request only a normal balanced rebuild and only with `code_graph.rebuild`.

## 4.7 MCP surface

The alpha surface is limited to:

```text
wormhole.code_graph.query
wormhole.code_graph.status
wormhole.code_graph.rebuild
```

The names are binding for this alpha specification. Their exact transport schemas must be entered into the live Gateway registry and alpha contract inventory.

### Query request

```json
{
  "intent": "Trace agent registration authentication",
  "entry_symbols": ["RegisterAgent"],
  "include_edges": [
    "calls",
    "references",
    "uses_type"
  ],
  "max_depth": 4,
  "minimum_confidence": 0.8,
  "requested_source_bytes": 12000
}
```

### Query result

Return:

* matched symbols;
* structural paths;
* source locations;
* edge confidence;
* edge provenance;
* exact source slices when authorised;
* current Git commit;
* working-tree status;
* graph revision;
* completeness;
* omitted-node count;
* omission reason;
* suggested follow-up symbols.

### Status result

State:

```text
disabled
initializing
ready
degraded
stale
error
```

Plus:

* active checkout;
* canonical remote;
* active revision;
* indexed commit;
* tracked Go file count;
* symbol count;
* edge count;
* dirty tracked file count;
* last successful build;
* database size;
* latest diagnostics.

### Rebuild limits

`wormhole.code_graph.rebuild` cannot:

* enable or disable Code Graph;
* change checkout;
* invoke Warpspeed;
* perform destructive in-place rebuild;
* alter source-byte ceilings;
* change project configuration.

## 4.8 Permissions

Project-scoped permissions:

```text
code_graph.query
code_graph.source.read
code_graph.status
code_graph.rebuild
```

Behaviour:

* `code_graph.query` permits graph metadata and source locations.
* Source slices additionally require `code_graph.source.read`.
* `code_graph.status` permits health inspection.
* `code_graph.rebuild` permits only a balanced copy-on-write rebuild.
* Enablement, disablement, checkout selection, and ceiling configuration remain CLI-only.

Source denial degrades to metadata:

```json
{
  "source_included": false,
  "source_omission_reason": "missing_permission",
  "required_permission": "code_graph.source.read"
}
```

## 4.9 CLI lifecycle

Required commands:

```bash
wormhole config code-graph enable
wormhole config code-graph disable
wormhole config code-graph status
wormhole config code-graph rebuild
wormhole config code-graph checkout set /path/to/repository
wormhole config code-graph checkout show
```

Enablement:

1. verifies a Git working tree;
2. canonicalises the path;
3. resolves the canonical remote;
4. verifies the remote matches the Wormhole project binding;
5. explains local and experimental scope;
6. explains CPU, memory, disk, and I/O implications;
7. requires explicit human confirmation;
8. builds the initial graph;
9. leaves the feature disabled if the build fails.

Disablement:

1. requires explicit human confirmation;
2. rejects new queries;
3. allows readers to complete or cancels them safely;
4. deletes completed and candidate revisions;
5. deletes nodes, edges, diagnostics, and configuration;
6. leaves Git and the working tree untouched.

## 4.10 Source integrity and containment

Before returning a source slice:

1. resolve the path beneath the approved checkout;
2. reject traversal;
3. reject symlinks escaping the checkout;
4. hash the current file;
5. compare with the indexed hash;
6. return only on a match.

On mismatch:

```json
{
  "source_included": false,
  "source_omission_reason": "working_tree_changed",
  "refresh_recommended": true
}
```

The indexer reads only tracked files in the checkout and Go metadata required to analyse them.

## 4.11 Generated skill

The integration manifest includes:

```text
.agents/skills/wormhole-code-graph/SKILL.md
```

Use Code Graph when:

* beginning a code task in an unfamiliar area;
* tracing an implementation path;
* locating callers or references;
* finding where a type or interface is used;
* identifying code responsible for an error;
* narrowing files required for review;
* a Wormhole object references a symbol or location.

Do not use it when:

* reading an already-known file;
* inspecting prose or assets;
* querying untracked or ignored files;
* status is `disabled` or `error`;
* strict current source is required but the graph is stale;
* ordinary filesystem inspection is simpler;
* code is outside the approved checkout.

Default sequence:

```text
inspect code_graph.status
-> query with a bounded source budget
-> inspect returned path and slices
-> request targeted follow-up symbols
-> use ordinary filesystem tools for unresolved context
```

Required statement:

> Code Graph narrows source discovery. It does not replace Git, the working tree, direct verification, builds, or tests.

The general operating-loop skill adds:

> For code tasks, use Code Graph before broad repository search when it is enabled and sufficiently current.

## 4.12 Benchmark corpus

Initial Wormhole queries:

```text
Where is agent registration authenticated?
Trace a task status update into its emitted event.
Where are Gateway sync response versions validated?
Which code writes Passport audit records?
Trace a local task write into the durable outbound queue.
Where is project isolation enforced for local SQLite queries?
```

Record for each:

* expected entry symbols;
* expected files;
* expected relationship path;
* authoritative and heuristic edges;
* source bytes returned;
* irrelevant source bytes;
* omissions;
* query duration;
* whether context was sufficient to begin work.

No performance threshold may be invented before measurements exist.

## 4.13 Automated acceptance

Tests prove:

* disabled by default;
* mismatched remote rejected;
* only tracked Go files indexed;
* complete bodies not stored;
* deterministic IDs survive body-only edits;
* signature changes may change identity;
* every edge references existing nodes;
* candidate revisions remain invisible;
* failed rebuilds preserve the active revision;
* one query sees one coherent revision;
* source budgets enforced;
* truncation explicit;
* source slices separately authorised;
* source hashes revalidated;
* cross-project access rejected;
* traversal and symlink escape rejected;
* agent rebuild cannot activate Warpspeed;
* disablement removes local graph state;
* two MCP clients observe one local contract.

## Exit criteria

1. Code Graph can be enabled for the Wormhole repository.
2. A coherent Go structural graph is constructed.
3. A fresh agent retrieves a bounded path for each benchmark query.
4. Returned slices match indexed hashes.
5. Query and source permissions are independently enforced.
6. Failed rebuilds preserve the previous graph.
7. Generated skills teach appropriate use.
8. Two models or harnesses call the same Gateway tools.
9. Dogfood evaluation records whether broad source reading falls.
10. No full Code Graph V1 claim is made.

---

# Milestone 5: Generated tool guidance and model operating templates

## Goal

Teach models how to use every Gateway-exposed agent-facing tool and how those tools compose into a disciplined operating loop.

## 5.1 Gateway and Fabric tool parity

* Compare Gateway and Fabric MCP inventories.
* Identify alpha-required operations missing through Gateway.
* Add or proxy missing operations.
* Confirm every agent-facing call terminates at Gateway.
* Update the alpha contract inventory.

Templates must never instruct a model to call a tool Gateway does not expose.

## 5.2 Canonical guidance records

Every exposed agent-facing tool has exactly one structured guidance record containing:

* purpose;
* use when;
* do not use when;
* mutation behaviour;
* required permission;
* prerequisites;
* freshness implications;
* source-access implications;
* recommended follow-up;
* minimal request example;
* common misuse.

CI fails when:

```text
exposed agent-facing tool count != guidance record count
```

The inventory includes:

* Core Gateway tools;
* approved integration-guidance tools;
* Code Graph alpha tools.

## 5.3 Generated skills

Generate:

```text
wormhole-orientation
wormhole-tool-use
wormhole-code-graph
wormhole-operating-loop
wormhole-contributor
wormhole-reviewer
```

Schemas come from the live registry, not duplicated Markdown.

Capability-aware instruction:

```text
if Code Graph is ready:
    query it before broad code discovery
else:
    continue with normal filesystem and repository tools
```

Instructions must not assume Code Graph is installed, enabled, current, or authorised.

## 5.4 Orientation content

Teach:

* Wormhole stores organisational context, not code.
* Git remains authoritative for source.
* Gateway is the local MCP endpoint.
* Fabric coordinates shared state.
* KB articles contain durable facts, decisions, discoveries, and procedures.
* Typed Events are preferred to chatter.
* Tasks represent intended work and state.
* identity and permissions are explicit;
* agents consult Wormhole before reconstructing context.

## 5.5 Operating loop

### Session start

1. inspect identity and permissions;
2. inspect assigned and relevant Tasks;
3. retrieve relevant KB context;
4. inspect recent relevant Events;
5. inspect Code Graph status for code tasks;
6. confirm intended work before broad codebase exploration.

### Before changing code

1. retrieve the Task and links;
2. check decisions and constraints;
3. use Code Graph when ready and useful;
4. report work begun when supported;
5. preserve Git as authority.

### During work

1. record meaningful blockers;
2. publish only durable discoveries;
3. avoid narrating every command;
4. prefer typed Events;
5. check before creating duplicate Tasks or KB articles.

### Completion

1. run required verification;
2. update Task state;
3. link commit or pull request where supported;
4. record durable knowledge;
5. publish a concise completion Event;
6. leave enough context for another Agent.

## 5.6 Role templates

Contributor:

* task pickup;
* scoped implementation;
* progress and blocker reporting;
* verification;
* durable discovery capture.

Reviewer:

* retrieve Task intent and decisions;
* use Code Graph to narrow changed paths when appropriate;
* verify against Git and current source;
* record actionable findings;
* avoid silent redesign;
* link conclusions to the Task or Git pointer;
* treat heuristic graph edges as hypotheses, not proof.

No coordinator, manager, Governance, or autonomous-planning role is added in this alpha.

## 5.7 Safe materialisation

The approved CLI supports:

```text
preview
apply
status
update
remove
rollback
```

Exact names come from Milestone 3.

Required behaviour:

* dry-run diff;
* atomic writes;
* managed-section markers;
* digest tracking;
* preservation of user content;
* role-aware rendering;
* no silent startup modification;
* cached offline operation.

## 5.8 Read-only model guidance

Expose one read-only Gateway MCP capability that returns:

* current manifest version;
* resolved role;
* applicable guidance;
* materialisation match state;
* pending human approval state.

The exact name is selected in Milestone 3 and recorded in the contract inventory.

## Exit criteria

* Fresh projects receive safe, role-appropriate guidance.
* Every exposed tool has current generated guidance.
* Guidance cannot silently drift from the live registry.
* Existing user instructions survive install and update.
* Models can inspect current guidance through Gateway.
* No executable content is distributed.

---

# Milestone 6: Meaning-bearing shared KB search

**Existing issue:** `#8`

## Goal

Replace the development stub with retrieval that ranks conceptually relevant organisational knowledge.

This is the shared Knowledge Base. It is separate from Code Graph.

## Work

* Define a provider-neutral embedding interface.
* Configure one supported production embedding implementation.
* Record model and version metadata.
* Generate query and article embeddings through the same configured model.
* Define unavailable-provider behaviour.
* Provide re-embedding when the model changes.
* Preserve project isolation in every vector query.
* Test related phrases with low lexical overlap.
* Test unrelated articles.
* Document local and external embedding configurations.
* Update generated guidance to explain when KB semantic search should precede repository reconstruction.

## Exit criteria

* Semantically related articles rank ahead of lexically similar irrelevant articles.
* Search remains project-scoped.
* Model and version changes are observable and recoverable.
* Generated guidance accurately describes retrieval.
* Issue `#8` can close.

---

# Milestone 7: Fabric-distributed integration manifests

## Goal

Allow a project to define approved text-only operating instructions that Fabric offers to enrolled Gateways.

## 7.1 Fabric storage

* Store project-scoped manifest metadata and declarative content.
* Preserve version history.
* Restrict modification to authorised identities.
* Include applicable manifests in bootstrap.
* distribute changes through incremental synchronisation.

## 7.2 Gateway verification

Verify:

* project binding;
* schema compatibility;
* content digests;
* tool-contract compatibility;
* permitted entry kinds;
* absence of executable content.

Cache only successfully verified manifests.

## 7.3 Approval and application

* notify the operator;
* render and preview changes;
* require explicit approval before repository modification;
* record the locally approved version;
* retain the last approved version offline;
* allow reject or postpone without breaking Gateway.

## 7.4 Revocation and audit

* support revocation;
* remove only managed content;
* retain offered, approved, rejected, applied, updated, and revoked audit records;
* never treat a model response as human approval.

## Exit criteria

* Fabric offers one project-specific, role-aware, text-only manifest.
* Gateway verifies and caches it.
* Repository changes remain human-approved.
* Offline operation uses the last approved state.
* Revocation does not damage user-owned files.

---

# Milestone 8: Real two-agent alpha acceptance loop

**Existing issue:** `#10`

## Goal

Exercise the complete product through real binaries and two distinct agents or harnesses.

## 8.1 Project preparation

Create:

* non-empty KB context;
* one open implementation Task;
* one relevant Channel;
* one approved integration manifest;
* contributor and reviewer roles;
* a Code Graph-enabled Wormhole checkout for the code-discovery experiment.

## 8.2 Agent A enrolment

* connect a fresh contributor through CLI and Gateway;
* bootstrap project state;
* offer and apply the contributor manifest;
* start the harness without bespoke Wormhole coaching.

## 8.3 Agent A autonomous loop

Prompt only with the actual Task.

Verify the agent:

* inspects identity;
* inspects Tasks;
* retrieves KB context;
* checks recent Events;
* inspects Code Graph status;
* queries Code Graph when ready;
* requests a bounded source package;
* reviews graph results before broad repository inspection;
* performs the work;
* verifies through Git, build, and tests independently;
* records meaningful state;
* links Git state where supported;
* records one useful discovery;
* avoids noisy updates.

## 8.4 Code Graph baseline comparison

Run a comparable task without Code Graph.

Measure:

* files opened before first correct edit;
* total source bytes exposed;
* broad search operations;
* turns or elapsed time before locating the implementation path;
* correctness of selected files;
* Task outcome;
* human corrections;
* useless Code Graph calls.

Code Graph passes only if evidence shows useful narrowing without lower correctness.

## 8.5 Agent B handoff and review

* connect a fresh reviewer through another Gateway or profile;
* apply the reviewer manifest;
* synchronise Task, Events, discovery, and Git pointer;
* verify the reviewer understands the work without human replay;
* use Code Graph to trace the changed path, callers, and affected types where useful;
* verify every conclusion against Git and the working tree;
* treat heuristic edges as hypotheses.

## 8.6 Resilience

* interrupt Fabric during Agent A's work;
* restart Gateway A while offline;
* continue supported local work;
* restore Fabric;
* verify queued state reaches Gateway B exactly once.

## 8.7 Automated acceptance

The process-level test covers:

* CLI;
* Gateway socket;
* Fabric HTTP synchronisation;
* PostgreSQL;
* two SQLite replicas;
* non-empty KB bootstrap;
* manifest retrieval;
* offline queueing;
* Task progression;
* Event propagation;
* KB discovery propagation;
* restart recovery;
* Code Graph status, query, permission, and bounded-source contracts.

Deterministic simulated agents may prove protocol behaviour. The release gate also requires a manual run with two real model or harness combinations.

## Exit criteria

* Complete loop passes automatically.
* Complete loop passes manually with two distinct agents or harnesses.
* A fresh model follows generated guidance without bespoke coaching.
* Another agent resumes or reviews using Wormhole and Git context.
* Code Graph comparison data is recorded.
* Issue `#10` can close.
* The release may be tagged through the guarded release procedure.

---

# Milestone 9: Closed external validation

## Goal

Determine whether Wormhole creates measurable value outside its creator's workflow.

## Cohort

At least three technically capable external users who already use coding agents.

Prefer participants with:

* multiple models or harnesses;
* non-trivial repositories;
* repeated context reconstruction;
* multiple devices or collaborators;
* willingness to provide structured feedback and logs.

## Measurements

Capture:

* installation completion;
* time to first Gateway MCP call;
* time from enrolment to productive work;
* tool-call success and denial rates;
* sessions beginning with useful Wormhole retrieval;
* human interventions needed to explain tool use;
* successful model-to-model handoffs;
* recovery after interruption;
* KB semantic relevance;
* Code Graph useful-query rate;
* files and source bytes read before correct edits;
* duplicate or low-value KB contributions;
* Event noise;
* Task-state accuracy;
* context reconstruction avoided;
* token consumption before productive work.

## Comparative evaluation

For at least one representative Task per participant, compare guidance-off or Code-Graph-off baseline with the alpha configuration.

Compare:

* tool selection;
* operating-loop adherence;
* useful shared-state writes;
* human correction;
* Task quality;
* unnecessary tool volume;
* source-discovery breadth;
* review quality.

## Decision rule

Proceed towards beta planning only if evidence shows that Wormhole:

* reduces manual context relay;
* reduces repeated project reconstruction;
* improves cross-model continuation;
* preserves useful state through interruption;
* can be learned through Gateway-managed guidance;
* narrows source discovery where Code Graph is applicable;
* does not create disproportionate maintenance, event noise, or incorrect confidence.

If only one component produces clear value, narrow the product around that evidence rather than preserving the whole platform by assumption.

---

# 10. Cross-cutting acceptance matrix

| Property | Enrolment | Offline | Manifests | Code Graph | KB Search | Alpha Loop |
|---|---:|---:|---:|---:|---:|---:|
| Project isolation | Required | Required | Required | Required | Required | Required |
| Atomic durability | Credentials/bootstrap | Queue/local writes | State/files | Revision publication | Embedding migration | End-to-end |
| Explicit degraded state | Required | Required | Required | Required | Required | Required |
| Human-only destructive action | Credentials/config | N/A | Apply/remove | Enable/disable/checkout | Provider config | Release gate |
| No secret leakage | Required | Required | Required | N/A | Provider config | Required |
| No executable content | N/A | N/A | Required | N/A | N/A | Required |
| Live registry contract | Enrolment MCP | Status MCP | Guidance MCP | Three tools | KB tools | All tools |
| Real process test | Required | Required | Required | Required | Required | Required |
| Manual model validation | Deferred | Deferred | Required | Required | Required | Required |

## 11. Definition of Alpha Validated

Wormhole reaches **Alpha Validated** when:

1. Gateway owns enrolment, bootstrap, synchronisation, and normal operation.
2. An enrolled Gateway remains useful while Fabric is unavailable.
3. Shared KB retrieval is meaning-bearing.
4. Gateway manages safe, declarative AGENTS and SKILLS templates.
5. Every exposed agent-facing tool has current usage guidance.
6. Code Graph locally indexes the Wormhole Go repository.
7. Code Graph returns bounded, coherent, source-validated code paths.
8. Fresh models use Code Graph appropriately through generated guidance.
9. Two different agents complete the shared-context handoff.
10. Alpha evaluation measures whether Code Graph reduces broad source reading.
11. At least three external users complete a controlled trial.
12. Measured results support continued development.
13. No beta compatibility claim is made.
14. No full Code Graph V1 claim is made.

## 12. Deferred work

The following remain outside Alpha Validation:

* full human identity and login;
* viewer-key migration from the operator secret;
* complete production database-role and RLS audit;
* beta compatibility baseline;
* Wormhole Governance;
* Constitution and Congress;
* managed cloud operations;
* billing;
* plugin execution;
* remote executable skills;
* automatic manifest application;
* polished human UI;
* parent-organisation incorporation or certification;
* autonomous planning inside Gateway;
* CRDT or peer-to-peer synchronisation;
* brand-new serverless local organisations;
* durable harness session history;
* full Code Graph V1;
* Code Graph embeddings;
* polyglot adapters;
* filesystem watchers and incremental indexing;
* stale-region preservation;
* dependency promotion;
* Warpspeed;
* shared Code Graph audit events;
* durable Code Graph references;
* destructive in-place rebuild;
* historical commit graphs.

## 13. Recommended pull-request sequence

```text
PR 1   Rebaseline roadmap and issues
PR 2   Gateway enrolment request and lifecycle design
PR 3   Gateway-owned registration and credentials
PR 4   Gateway bootstrap ownership and lifecycle tests
PR 5   Offline Gateway restart
PR 6   Integration-manifest design
PR 7   Code Graph alpha storage and revision model
PR 8   Git-tracked Go inventory and semantic adapter
PR 9   Structural query and bounded source assembly
PR 10  Code Graph MCP tools and permissions
PR 11  Code Graph CLI lifecycle and destructive disablement
PR 12  Code Graph benchmark and security hardening
PR 13  Tool-guidance metadata and contract coverage
PR 14  Generated orientation, operating-loop, and Code Graph skills
PR 15  Safe AGENTS and SKILLS materialisation
PR 16  Read-only model guidance through Gateway
PR 17  Shared KB semantic embedding implementation
PR 18  Shared KB semantic ranking and migration tests
PR 19  Fabric manifest storage and bootstrap distribution
PR 20  Gateway manifest verification and approval
PR 21  Full automated alpha acceptance loop
PR 22  Manual two-model and Code Graph validation
PR 23  Closed-trial instrumentation and operator guide
```

## 14. Decision gates

### Gate A: lifecycle foundation

After PR 5:

* enrolment and bootstrap are Gateway-owned;
* Gateway restarts offline;
* local project binding is stable.

Code Graph implementation and semantic-search implementation may proceed in parallel only after this gate.

### Gate B: local capability integrity

After PR 12:

* Code Graph cannot expose unvalidated source;
* failed rebuilds preserve the active revision;
* project isolation and containment pass;
* benchmark data exists, without invented thresholds.

Generated Code Graph guidance may become release-candidate content only after this gate.

### Gate C: complete alpha loop

After PR 22:

* automated protocol loop passes;
* manual two-model loop passes;
* Code Graph baseline comparison is recorded;
* failures and useless calls are included, not filtered out.

External validation begins only after this gate.

### Gate D: alpha validation decision

After PR 23 and the closed trial:

* evidence supports continuation, narrowing, or stopping;
* beta planning is a separate decision;
* no compatibility promise is inferred from an alpha tag.
