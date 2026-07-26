# Historical: Wormhole Alpha Validation Roadmap

**Status:** Historical source document; superseded by the canonical
[`docs/superpowers/specs/2026-07-25-alpha-validation-unified-spec.md`](2026-07-25-alpha-validation-unified-spec.md).

The canonical root roadmap pointer is
[`ROADMAP-ALPHA-VALIDATION.md`](../../../ROADMAP-ALPHA-VALIDATION.md).

---

**Status:** Proposed
**Target:** Next validated alpha release
**Primary objective:** Prove that independently operating agents can join a Wormhole project, receive the context and operating instructions required to use Wormhole correctly, coordinate through Gateway and Fabric, and preserve useful organisational state across models, sessions, machines, and network interruptions.

## 1. Alpha thesis

The next alpha must prove this complete proposition:

> A fresh agent can connect through Gateway, understand how and when to use Wormhole, retrieve relevant project context, coordinate its work through the task graph and event bus, contribute durable knowledge, and make that state available to another agent without continual human instruction.

This roadmap does not attempt to prepare Wormhole for beta compatibility or general public deployment. Its purpose is to validate the product thesis through the actual CLI, Gateway, Fabric, storage, synchronization, MCP, and agent-instruction paths.

## 2. Critical path

```text
Repository rebaseline
        ↓
Gateway-owned enrolment lifecycle
        ↓
Offline Gateway restart
        ↓
Integration-manifest design
        ↓
Gateway-managed AGENTS and SKILLS templates
        ↓
Meaning-bearing semantic search
        ↓
Fabric distribution of approved manifests
        ↓
Real two-agent alpha acceptance loop
        ↓
Closed external validation
```

Semantic-search work may proceed in parallel with the local integration-manifest implementation after the integration design is approved.

## 3. Global constraints

* Git remains the sole source of truth for code.
* Harnesses connect to local Gateway, never directly to Fabric.
* Gateway remains deterministic and performs no LLM inference.
* Gateway writes local state durably before attempting synchronization.
* Fabric remains authoritative for shared multi-runtime state.
* Integration manifests are project-scoped and role-aware.
* Version one integration manifests contain declarative text only.
* Gateway must not download or execute arbitrary scripts, binaries, plugins, hooks, or model-generated code.
* Gateway must not silently overwrite user-owned instructions.
* Installing or updating files in a repository requires explicit human action.
* Models may inspect their current guidance through Gateway, but may not silently alter that guidance.
* Generated guidance must correspond exactly to the currently exposed Gateway MCP tools.
* The current compatibility policy remains `alpha-inventory`.
* Governance, managed cloud, human authentication, and beta compatibility are outside this roadmap.

---

# Milestone 0: Rebaseline the repository

## Goal

Replace the historical day-based roadmap with a roadmap matching the implemented system and create explicit GitHub work items for the remaining alpha programme.

## Work

* [ ] Add this document as `ROADMAP-ALPHA-VALIDATION.md`.
* [ ] Mark superseded roadmaps as historical rather than deleting them.
* [ ] Create a GitHub milestone named `Alpha Validation`.
* [ ] Attach issues `#8`, `#10`, `#24`, and the offline-startup portion of `#37`.
* [ ] Resolve issue `#3` by deciding whether durable agent sessions are required for alpha.
* [ ] Create the following new issues:

  * `Design Gateway-managed integration manifests`
  * `Generate agent guidance from the MCP tool inventory`
  * `Materialize AGENTS and SKILLS templates safely`
  * `Distribute project integration manifests through Fabric bootstrap`
  * `Evaluate autonomous Wormhole tool use across models`
* [ ] Explicitly leave issues `#22`, `#23`, and `#36` outside the alpha milestone.

## Session decision

Unless a concrete alpha requirement depends on durable session history, amend the relevant documentation to state:

* Passports identify agents.
* Credential profiles authorize local runtime access.
* Harness process sessions are ephemeral during alpha.
* Durable session records are deferred until a use case requires them.

## Exit criteria

* One current roadmap governs the next alpha.
* Every alpha deliverable has one GitHub issue and one clear acceptance criterion.
* No beta or Governance work appears on the alpha critical path.

---

# Milestone 1: Gateway-owned enrolment lifecycle

**Existing issue:** `#24`

## Goal

Make Gateway the sole owner of:

```text
Authentication
→ Enrolment
→ Bootstrap
→ Synchronisation
→ Normal operation
```

The CLI should orchestrate user interaction and harness configuration, but it should not independently own project enrolment or bootstrap behaviour.

## Work packages

### 1.1 Define the local enrolment request

* [ ] Specify the request Gateway receives from `wormhole join` and `wormhole connect`.
* [ ] Include project binding, owner, model, capabilities, repositories, roles, requested permissions, and Fabric address.
* [ ] Define explicit error results for unreachable Fabric, invalid project, rejected permissions, duplicate identity, and credential persistence failure.
* [ ] Add the request and response to the alpha contract inventory.

### 1.2 Move Fabric registration into Gateway

* [ ] Make the CLI send the enrolment request to Gateway.
* [ ] Make Gateway perform the Fabric registration handshake.
* [ ] Prevent duplicate Passport issuance during retries.
* [ ] Preserve project and namespace boundaries throughout the request.

### 1.3 Move credential persistence into Gateway

* [ ] Persist credentials only after successful enrolment.
* [ ] Use restrictive directory and file permissions.
* [ ] Write credentials atomically.
* [ ] Never expose bearer tokens in logs, events, test output, or model-facing responses.

### 1.4 Move initial bootstrap into Gateway

* [ ] Pull project metadata, agent identity, initial tasks, channels, KB state, and applicable integration-manifest metadata.
* [ ] Apply the bootstrap transactionally to the local SQLite replica.
* [ ] Transition to incremental synchronization only after bootstrap succeeds.
* [ ] Preserve a recoverable state when enrolment succeeds but bootstrap fails.

### 1.5 Add process-level tests

* [ ] Launch actual CLI, Gateway, Fabric, PostgreSQL, and SQLite components.
* [ ] Enrol a fresh identity.
* [ ] Verify the credential profile is created by Gateway.
* [ ] Verify a non-empty project bootstrap reaches SQLite.
* [ ] Verify retries do not create duplicate Passports.
* [ ] Verify the CLI does not perform direct follow-on Fabric calls.

## Exit criteria

* Gateway owns the complete lifecycle.
* The CLI contains no independent bootstrap orchestration.
* Enrolment and bootstrap are covered by a real process-level test.
* Issue `#24` can be closed without relying on the broader alpha demonstration.

---

# Milestone 2: Offline Gateway restart

**Existing issue:** `#37`, narrowed for alpha

## Goal

An already-enrolled Gateway must start and remain useful when Fabric is unavailable.

Completely serverless creation of a new local namespace is a separate follow-up and is not required for this alpha.

## Work packages

### 2.1 Separate local startup from remote synchronization

* [ ] Open the credential profile and SQLite replica before attempting network bootstrap.
* [ ] Serve the local MCP socket as soon as the local state is valid.
* [ ] Start Fabric synchronization asynchronously.
* [ ] Report connection state without treating remote unavailability as process failure.

### 2.2 Preserve local reads and writes

* [ ] Serve local task, event, KB, identity, and integration-guidance reads while offline.
* [ ] Make supported writes durable in SQLite.
* [ ] Queue outbound state changes for later delivery.
* [ ] Preserve queue contents across Gateway restart.

### 2.3 Define degraded-state behaviour

* [ ] Distinguish `online`, `offline`, `synchronizing`, and `attention_required`.
* [ ] Expose status to the CLI and through a read-only local MCP response.
* [ ] Reject operations requiring central authority rather than pretending they succeeded.
* [ ] Continue allowing operations whose authority and data exist locally.

### 2.4 Test interruption and recovery

* [ ] Bootstrap while Fabric is available.
* [ ] Stop Fabric.
* [ ] Restart Gateway.
* [ ] Perform local reads and supported writes.
* [ ] Restart Fabric.
* [ ] Verify queued state synchronizes exactly once.
* [ ] Verify no local state is lost or silently overwritten.

## Exit criteria

* An enrolled Gateway starts without Fabric.
* Local agents remain productive during the outage.
* Queued writes survive restart and synchronize after recovery.
* The README no longer requires Fabric availability for every Gateway startup.

## Deferred from this milestone

* Creating a brand-new local-only organisation.
* Reconciling provisional local identities with Fabric.
* Peer-to-peer synchronization.
* Distributed identity issuance.

---

# Milestone 3: Gateway-managed integration-manifest design

## Goal

Define how Gateway stores, renders, installs, updates, and exposes project-specific agent instructions without creating an arbitrary-code distribution system.

This milestone produces an approved design before implementation.

## 3.1 Distinguish the two instruction classes

### Wormhole development instructions

Used by agents modifying the Wormhole repository itself:

* root `AGENTS.md`;
* `agents/README.md`;
* Wormhole implementation and review skills;
* RFC and implementation-rule precedence.

### Wormhole usage instructions

Used by an agent working inside any project connected to Wormhole:

* how to identify itself;
* how to inspect tasks, channels, agents, and KB state;
* when each Wormhole tool should be used;
* how to report progress, blockers, discoveries, and completion;
* what should remain in Git rather than Wormhole;
* how to avoid event spam and low-value KB pollution.

The second class is the subject of Gateway-managed integration manifests.

## 3.2 Recommended manifest properties

The design should specify a versioned structure containing:

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

Each entry should contain:

```text
kind
target
content_digest
merge_policy
required
role_filters
```

Permitted version-one entry kinds:

* `agents_bootstrap`
* `skill`
* `reference`

Prohibited version-one entry kinds:

* executable script;
* binary;
* shell command;
* package dependency;
* dynamic plugin;
* post-install hook;
* arbitrary environment mutation.

## 3.3 Authority model

* [ ] Gateway owns the locally approved manifest state.
* [ ] Fabric may offer a project-approved manifest during bootstrap or synchronization.
* [ ] Gateway verifies project scope, schema version, tool-contract compatibility, and content digest.
* [ ] Gateway presents a preview before applying new or changed files.
* [ ] A human explicitly approves initial application and material updates.
* [ ] Gateway caches the last approved manifest for offline operation.
* [ ] Models may read the current guidance but may not approve or replace it.
* [ ] Revocation removes only Gateway-managed content, never unrelated user content.

## 3.4 File ownership and merging

Recommended local layout:

```text
.wormhole/
  integration-state.json

.agents/
  skills/
    wormhole-orientation/
      SKILL.md
    wormhole-tool-use/
      SKILL.md
    wormhole-operating-loop/
      SKILL.md
    wormhole-contributor/
      SKILL.md
    wormhole-reviewer/
      SKILL.md
```

The repository-level `AGENTS.md` should follow these rules:

* create it only when absent;
* otherwise insert or update a clearly delimited managed section;
* preserve all user-owned text outside that section;
* show a diff before modifying it;
* write atomically;
* provide removal and rollback;
* never rewrite the whole file merely to change the managed block.

Harness-specific adapters may additionally generate supported Claude Code or OpenCode references, but the canonical content remains the Gateway-managed manifest.

## Deliverable

Create an approved design document covering:

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
* test strategy.

## Exit criteria

* The design resolves every RFC-0003 integration-manifest open question required for declarative text assets.
* Arbitrary code execution remains explicitly out of scope.
* No implementation begins before the design is approved.

---

# Milestone 4: Generated tool guidance and model operating templates

## Goal

Teach models how to use every Gateway-exposed Wormhole tool, when to use it, when not to use it, and how individual calls fit into a coherent work cycle.

## 4.1 Audit Gateway and Fabric tool parity

Before generating guidance:

* [ ] Compare the Gateway and Fabric MCP inventories.
* [ ] Identify tools required for the alpha workflow that are unavailable through Gateway.
* [ ] Add or proxy any missing alpha-required operation.
* [ ] Confirm every agent-facing call terminates at Gateway.
* [ ] Update the alpha contract inventory for reviewed interface changes.

The integration templates must never instruct a model to call a tool that Gateway does not actually expose.

## 4.2 Extend canonical tool guidance

For every agent-facing tool, maintain structured guidance containing:

* concise purpose;
* use when;
* do not use when;
* whether it mutates state;
* required permission;
* expected prerequisites;
* recommended follow-up;
* one minimal example;
* common misuse to avoid.

The exact input and result schemas must continue to come from the live registry.

Add a contract test requiring:

```text
every exposed agent-facing tool
has exactly one guidance record
```

A tool addition without guidance should fail CI.

## 4.3 Generate the tool-use skill

Generate `.agents/skills/wormhole-tool-use/SKILL.md` from:

* the live Gateway registry;
* the structured guidance records;
* current permissions;
* current argument and result examples;
* the reviewed alpha contract.

Do not manually duplicate tool schemas in Markdown.

The rendered skill should group tools by the established Wormhole concepts:

* identity;
* tasks;
* channels and events;
* knowledge;
* Git pointers;
* local status and synchronization, where agent-facing.

## 4.4 Create the orientation skill

`wormhole-orientation/SKILL.md` should teach:

* Wormhole stores organisational context, not code.
* Git remains authoritative for source code.
* Gateway is the local MCP endpoint.
* Fabric coordinates shared state.
* KB articles should contain durable facts, decisions, discoveries, and procedures.
* Typed events are preferred to unstructured chatter.
* Tasks represent intended work and its state.
* Agent identity and permissions are explicit.
* The agent should consult Wormhole before reconstructing project context from scratch.

## 4.5 Create the operating-loop skill

`wormhole-operating-loop/SKILL.md` should establish this default behaviour.

### At session start

1. Inspect identity and permissions.
2. Inspect assigned and relevant open tasks.
3. Retrieve relevant KB context.
4. Inspect recent relevant channel events.
5. Confirm the intended work before broad codebase exploration.

### Before changing code

1. Retrieve the task and linked context.
2. Check for relevant architectural decisions and known constraints.
3. Report or record that work has begun when the available tool surface supports it.
4. Preserve Git as the code authority.

### During work

1. Record meaningful blockers through task or event state.
2. Publish durable discoveries only when they will help another agent or future session.
3. Avoid narrating every implementation action.
4. Prefer typed events.
5. Do not create duplicate tasks or KB articles without checking existing state.

### At completion

1. Run the required verification.
2. Update task state.
3. Link the relevant commit or pull request where supported.
4. Record new durable knowledge.
5. Publish a concise completion event.
6. Leave enough context for another agent to continue without a human relay.

## 4.6 Create role templates

Initial role-specific skills:

### Contributor

Emphasize:

* task pickup;
* scoped implementation;
* progress and blocker reporting;
* verification;
* durable discovery capture.

### Reviewer

Emphasize:

* retrieving task intent and linked decisions;
* checking implementation against those sources;
* recording actionable findings;
* avoiding silent redesign;
* linking review conclusions to the relevant task or Git reference.

Do not add coordinator, manager, governance, or autonomous-planning templates until real usage demonstrates a need.

## 4.7 Encourage use without causing noise

The templates should use imperative language such as:

> Consult Wormhole before reconstructing project context from repository files.

> Record a discovery when it will change how another agent approaches future work.

> Update shared state at meaningful transitions, not after every command.

Avoid instructions such as:

> Post constant updates.

> Write everything you learn to the KB.

> Create a task for every implementation step.

The objective is reliable coordination, not maximal tool-call volume.

## 4.8 Implement safe materialization

Add an explicit setup flow, with exact command naming decided by the approved design, supporting:

* preview;
* apply;
* status;
* update;
* remove;
* rollback.

Required behaviour:

* dry-run diff;
* atomic writes;
* managed-section markers;
* digest tracking;
* preservation of user content;
* role-aware rendering;
* no silent startup modification;
* no network requirement when using the last approved cached manifest.

## 4.9 Expose guidance to the model

Expose a read-only Gateway MCP capability that allows an agent to retrieve:

* its current integration-manifest version;
* its resolved role;
* applicable operating guidance;
* whether local materialized files match the approved manifest;
* whether an update is awaiting human approval.

The exact MCP naming must be chosen during design and added to the contract inventory. Do not casually create a new Core pillar solely for this feature.

## Exit criteria

* A fresh project can receive safe, role-appropriate Wormhole instructions.
* Every exposed tool has generated guidance.
* Instructions cannot drift silently from the live tool registry.
* Existing user instructions survive installation and updates.
* A model can inspect its current Wormhole operating guidance through Gateway.
* No executable content is distributed.

---

# Milestone 5: Meaning-bearing semantic search

**Existing issue:** `#8`

## Goal

Replace the development stub with retrieval that actually finds conceptually relevant organisational knowledge.

## Work packages

* [ ] Define a provider-neutral embedding interface.
* [ ] Configure one supported production embedding implementation.
* [ ] Record embedding model and version metadata.
* [ ] Generate query and article embeddings through the same configured model.
* [ ] Define behaviour when the embedder is unavailable.
* [ ] Provide a re-embedding procedure when the model changes.
* [ ] Preserve project isolation in every vector query.
* [ ] Add semantic-ranking tests using related phrases with low lexical overlap.
* [ ] Add negative tests for unrelated articles.
* [ ] Document local and externally hosted embedding configurations.
* [ ] Update generated agent guidance to explain when semantic search should precede broad repository reading.

## Exit criteria

* Semantically related articles rank ahead of lexically similar but irrelevant articles.
* Search remains project-scoped.
* Model/version changes are observable and recoverable.
* Gateway-delivered skills accurately describe the available retrieval behaviour.

---

# Milestone 6: Fabric-distributed project integration manifests

## Goal

Allow a project to define approved agent-operating instructions that Fabric distributes to each enrolled Gateway.

## Work packages

### 6.1 Fabric storage and retrieval

* [ ] Store project-scoped manifest metadata and declarative content.
* [ ] Preserve manifest version history.
* [ ] Restrict modification to explicitly authorized identities.
* [ ] Return the applicable manifest during bootstrap.
* [ ] Return manifest changes through incremental synchronization.

### 6.2 Gateway verification

* [ ] Verify project binding.
* [ ] Verify schema compatibility.
* [ ] Verify content digests.
* [ ] Verify tool-contract compatibility.
* [ ] Reject unknown entry kinds.
* [ ] Reject executable content.
* [ ] Cache only successfully verified manifests.

### 6.3 Approval and application

* [ ] Notify the user when a new manifest is available.
* [ ] Render and preview its effect.
* [ ] Require explicit approval before modifying repository files.
* [ ] Record the locally approved version.
* [ ] Continue using the last approved version when offline.
* [ ] Allow rejection or postponement without breaking Gateway operation.

### 6.4 Revocation and audit

* [ ] Support project manifest revocation.
* [ ] Remove or deactivate only managed content.
* [ ] Retain an audit record of offered, approved, rejected, applied, updated, and revoked versions.
* [ ] Never treat a model response as human approval.

## Exit criteria

* Fabric can offer one project-specific, role-aware, text-only manifest.
* Gateway verifies and caches it.
* Repository changes remain human-approved.
* Offline operation uses the last approved state.
* Revocation does not damage user-owned files.

---

# Milestone 7: Real alpha acceptance loop

**Existing issue:** `#10`

## Goal

Exercise the complete product through actual binaries and two distinct agents.

## Acceptance topology

```text
Agent A
  ↓ MCP
Gateway A
  ↓ sync
Fabric
  ↓ sync
Gateway B
  ↓ MCP
Agent B
```

PostgreSQL is authoritative shared storage. Each Gateway uses its own SQLite replica and queue.

## Required scenario

### Project preparation

* [ ] Create a project with:

  * one non-empty KB context set;
  * one open implementation task;
  * one relevant channel;
  * one approved integration manifest;
  * contributor and reviewer roles.

### Agent A enrolment

* [ ] Connect a fresh contributor agent through the real CLI and Gateway.
* [ ] Bootstrap project state.
* [ ] Offer and apply the contributor integration manifest.
* [ ] Start the harness without a bespoke human explanation of Wormhole.

### Agent A autonomous operating loop

Verify that the model, prompted only with its actual task:

* [ ] inspects its identity;
* [ ] inspects relevant tasks;
* [ ] searches or retrieves relevant KB context;
* [ ] checks recent channel state;
* [ ] performs the work;
* [ ] records meaningful status or blockers;
* [ ] completes the task;
* [ ] links Git state where supported;
* [ ] records one useful discovery;
* [ ] avoids high-volume low-value updates.

### Agent B handoff

* [ ] Connect a fresh reviewer agent through another Gateway or profile.
* [ ] Apply the reviewer integration manifest.
* [ ] Synchronize the completed task, events, and discovery.
* [ ] Verify Agent B can understand what happened without a human replay.
* [ ] Have Agent B perform a review using Wormhole context and Git pointers.

### Resilience

* [ ] Interrupt Fabric during Agent A’s work.
* [ ] Restart Gateway A while Fabric remains unavailable.
* [ ] Continue supported local work.
* [ ] Restore Fabric.
* [ ] Verify queued state reaches Gateway B exactly once.

## Automated acceptance

Create a process-level test covering:

* CLI;
* Gateway socket;
* Fabric HTTP synchronization;
* PostgreSQL;
* two SQLite replicas;
* non-empty KB bootstrap;
* integration-manifest retrieval;
* offline queueing;
* task progression;
* event propagation;
* KB discovery propagation;
* restart recovery.

The test may use deterministic simulated agents for protocol assertions, but the release gate must also include a manual run with two real model or harness combinations.

## Exit criteria

* The complete loop passes automatically.
* The complete loop passes manually with two different agents or harnesses.
* At least one fresh model follows the Gateway-managed operating instructions without bespoke coaching.
* Another agent can resume or review the work using only Wormhole and Git context.
* Issue `#10` can be closed.
* The next alpha release may be tagged through the existing guarded release procedure.

---

# Milestone 8: Closed external validation

## Goal

Determine whether Wormhole creates measurable value outside its creator’s own workflow.

## Cohort

Recruit at least three technically capable external users who already use coding agents.

Prefer users with:

* more than one model or harness;
* projects larger than a trivial demonstration repository;
* repeated context reconstruction;
* multiple devices or collaborators;
* willingness to provide structured feedback and logs.

## Measurements

Capture:

* installation completion rate;
* time to first successful Gateway MCP call;
* time from enrolment to productive work;
* tool-call success and denial rates;
* percentage of sessions beginning with useful Wormhole context retrieval;
* human interventions required to explain how to use Wormhole;
* successful model-to-model handoffs;
* synchronization recovery after interruption;
* semantic-search relevance;
* duplicate or low-value KB contributions;
* event noise;
* task-state accuracy;
* user-reported context reconstruction avoided;
* token consumption before productive work.

## Comparative evaluation

For at least one representative task per participant:

1. perform or estimate the workflow without Gateway-managed guidance;
2. perform it with the integration manifest;
3. compare:

   * correct tool selection;
   * sequence adherence;
   * useful shared-state writes;
   * human correction;
   * task completion quality;
   * unnecessary tool volume.

## Alpha-validation decision

Proceed toward beta planning only if the trial provides evidence that Wormhole:

* reduces manual context relay;
* reduces repeated project reconstruction;
* improves cross-model continuation;
* preserves useful state through interruption;
* can be learned through Gateway-managed guidance;
* does not create disproportionate maintenance or event noise.

If only one component creates clear value, narrow the product around that component rather than preserving the entire platform by assumption.

---

# Deferred work

The following remain explicitly outside the Alpha Validation roadmap:

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
* code-graph indexing;
* polished human UI;
* parent-organisation incorporation or certification;
* autonomous planning inside Gateway;
* CRDT or peer-to-peer synchronization.

---

# Recommended pull-request sequence

```text
PR 1  Rebaseline roadmap and issues
PR 2  Gateway enrolment request and lifecycle design
PR 3  Gateway-owned registration and credentials
PR 4  Gateway bootstrap ownership and lifecycle tests
PR 5  Offline Gateway restart
PR 6  Integration-manifest design
PR 7  Tool-guidance metadata and contract coverage
PR 8  Generated orientation and tool-use skills
PR 9  Safe AGENTS and SKILLS materialization
PR 10 Read-only model guidance through Gateway
PR 11 Semantic embedding implementation
PR 12 Semantic ranking and migration tests
PR 13 Fabric manifest storage and bootstrap distribution
PR 14 Gateway manifest verification and approval
PR 15 Full automated alpha acceptance loop
PR 16 Manual two-model validation documentation
PR 17 Closed-trial instrumentation and operator guide
```

Each PR must:

* begin with failing focused tests where behaviour changes;
* remain independently reviewable;
* update the alpha contract inventory when public interfaces change;
* run focused tests before the complete required suite;
* avoid unrelated refactoring;
* preserve Gateway, Fabric, and Core dependency boundaries;
* include documentation for any externally observable behaviour.

# Definition of Alpha Validated

Wormhole reaches **Alpha Validated** when:

1. Gateway owns enrolment, bootstrap, synchronization, and normal operation.
2. An enrolled Gateway remains useful while Fabric is unavailable.
3. Knowledge retrieval is meaning-bearing rather than hash-based.
4. Gateway manages safe, declarative, project-specific AGENTS and SKILLS templates.
5. Every exposed Wormhole tool has current model-facing usage guidance.
6. A fresh model uses Wormhole correctly without bespoke human coaching.
7. Two different agents complete the full shared-context handoff.
8. At least three external users complete a controlled trial.
9. Measured results support continued development.
10. No beta compatibility claim has been made.
