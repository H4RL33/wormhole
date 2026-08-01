# RFC-0001: Wormhole Core

**The shortest path between a person or agent and organisational context.**

| | |
|---|---|
| Status | Revised Draft |
| Author | Harley |
| Date | 2026-07-07 |
| Revised | 2026-08-01 |
| Supersedes | `slack_for_agents.md`, `slack_for_agents_revised.md`, `AIOS_V3_Proposal.md` |
| Related | [RFC-0002: Wormhole Governance](wormhole_rfc_governance.md), [RFC-0003: Wormhole Local Runtime](wormhole_rfc_local_runtime.md), [Git-Native Architecture Design](../superpowers/specs/2026-07-28-git-native-wormhole-architecture-design.md) |

> **Revision status (2026-08-01):** This draft has been reconciled around a
> Git-native project model. Humans and agents have parity for project
> operations; agents are durable, accountable principals; `wormhole setup`
> replaces the legacy join/connect flow; and Fabric is an optional live
> collaboration accelerator rather than the authority for project state.

---

## 1. Abstract

Coding agents have crossed a threshold: single-shot code generation is now
good enough that the bottleneck has moved from "can the model write correct
code" to "can every participant behave like a competent, well-informed member
of an engineering organisation?" Humans and agents need the same durable record
of what a project is doing and why, without re-deriving it from scratch in each
tool, session, machine, or fork.

Wormhole is shared organisational infrastructure for humans and AI agents. Its
Core has four pillars: a structured Event Bus, a Task Graph, a linked Knowledge
Base, and Identity & Permissions. Project state is typed, Git-native data under
`.wormhole/`. The CLI is the human-first interface; a stateless MCP connector
gives coding harnesses the same project operations without becoming a second
stateful authority.

Git remains the sole source of truth for code and accepts explicitly curated portable
project-state changes through normal Git workflows. An optional Fabric can make public or private
projects feel live across participants, but it does not replace Git authority.

**Code and curated accepted project state are versioned by Git. Wormhole gives that
project state typed collaboration semantics.** Governance (Constitution and Congress) remains
a separate, independently shippable layer in RFC-0002; see §10.

---

## 2. Motivation

### 2.1 The problem today

Three failure modes recur across agentic coding workflows:

1. **Context fragmentation across tools and models.** Switching harnesses loses
   accumulated project understanding. Re-establishing context consumes time and
   tokens before useful work starts.
2. **Context fragmentation across machines and forks.** Memory tied to one
   workstation or unshared tool-specific file is unavailable to another person,
   agent, machine, or fork.
3. **Context fragmentation across participants.** Humans manually relay task
   status, discoveries, and decisions because people and their agents lack one
   shared, typed project record.

These are infrastructure problems. Static instruction files are useful inputs,
but they are not a typed task graph, event history, identity model, or linked
knowledge base. A remote service alone is also insufficient: project context
must remain usable and reviewable through Git without requiring a service to be
available.

### 2.2 Why now

- Agents can stay on-task for long, autonomous stretches, so their decisions and
  hand-offs need durable attribution.
- MCP provides a common harness connector, while Git already provides a common
  acceptance, review, and distribution mechanism.
- Mixed human-agent teams increasingly need the same operations and project
  context rather than separate human and agent systems.

### 2.3 What "solved" looks like

- A person or agent can inspect typed project state from the repository and
  understand decisions, work in flight, ownership, and known constraints.
- A human and an agent can perform the same authorised project operations using
  the interface appropriate to each: CLI for humans, MCP for harnesses.
- A project works locally or from a fork without Fabric, while projects that use
  Fabric gain faster live collaboration.
- Moving between tools or machines does not discard organisational context that
  has been accepted into Git.

---

## 3. Prior Art and Inspiration

Wormhole borrows established shapes without copying their delivery assumptions:

- **Slack / Discord / Matrix** — named streams and event-oriented coordination.
- **Trello / GitHub Projects / Asana** — typed task ownership, status, priority,
  and dependencies.
- **Wiki.js / DokuWiki** — linked organisational knowledge.
- **Git** — distributed change proposal, review, acceptance, history, forks, and
  public or private assurance boundaries.
- **MCP** — a vendor-neutral connector between coding harnesses and Wormhole
  project operations.
- **agents.md / AGENT.md conventions** — repository-local instructions that
  demonstrate the value of context travelling with a project.

---

## 4. Goals and Non-Goals

### 4.1 Goals

- G1: Provide durable, model-agnostic organisational context for both humans and
  agents.
- G2: Let authorised humans and agents perform the same project operations.
- G3: Keep typed project state Git-native under `.wormhole/` so it works across
  local clones, forks, and canonical repositories.
- G4: Keep Git as the sole source of truth for code; Wormhole stores context
  about code, never a competing copy of it.
- G5: Offer a human-first CLI and a stateless MCP connector over the same project
  operation semantics.
- G6: Support local/fork, canonical-public, and private assurance modes without
  requiring Fabric.
- G7: Make Fabric available as an optional live collaboration accelerator for
  both public and private projects.
- G8: Keep the product agent-first while giving humans equivalent authorised
  project operations and strong setup/review/control surfaces.

### 4.2 Non-goals

- NG1: Replacing Git or Git hosting. Wormhole does not host source code or
  create a competing acceptance history.
- NG2: Making Fabric mandatory for project operation or project-state authority.
- NG3: Giving humans and agents different project operation models merely
  because they use different interfaces.
- NG4: Treating identity, authentication, and authorization as one object.
- NG5: Being a general-purpose chat or rich-media platform.
- NG6: Full autonomous governance. Constitution and Congress are specified in
  RFC-0002, not as a phase of this RFC.
- NG7: An implicit organisation-wide or cross-repository graph, KB merge,
  inherited policy, or cross-project authority. V1 is repository-lineage scoped.

---

## 5. Core Principles

1. **Humans and agents have operational parity.** A permission applies to a
   project principal and operation, not to whether the caller is human or AI.
   CLI and MCP may be shaped for different clients, but neither defines a
   lesser class of participant. Parity does not change Wormhole's agent-first
   priority: typed schemas, progressive retrieval, autonomous durability,
   attribution, and handoff are optimised for agents.
2. **Git is authoritative.** Git is the sole code truth and the acceptance path
   for typed `.wormhole/` project state. Wormhole stores pointers and context
   about code, not source copies.
3. **Portable project state is typed and Git-native.** Curated events, task
   definitions and portable task state, curated knowledge,
   self-declared actor attribution claims, and Git pointers have reviewable
   project-state representations under `.wormhole/`. Tracked actor claims never
   grant membership, ownership, a Passport, or an effective permission; those
   remain private local/Fabric authority records.
4. **Structured events precede chatter.** Typed events carry project facts;
   natural language adds nuance where a schema cannot.
5. **Identity, authentication, and authorization are separate.** A durable
   principal is not a credential, and a valid credential does not itself grant
   an operation.
6. **Fabric accelerates; it does not replace Git.** Projects can operate without
   Fabric. Gateway/Fabric own operational collaboration under finite retention;
   Git accepts only explicitly curated portable context.
7. **Interfaces are fit for their callers.** The CLI is human-first. MCP is a
   stateless harness connector to the same Core operations.

---

## 6. Vision: A Shared Project Operating Layer

Wormhole is the practical collaboration core beneath higher-order organisational
systems:

```
Mission -> Policy -> Project State -> Principals
```

| Layer | Wormhole equivalent | Status |
|---|---|---|
| Mission | Project metadata + KB root articles | Core |
| Policy | Project permissions; optional governance in RFC-0002 | Core + optional layer |
| Project state | Selected events, portable task state, curated knowledge, and Git pointers in `.wormhole/` | Core |
| Principals | Durable human and agent identities with accountable ownership | Core |

Organisational skills may be referenced by project state, but management of
model- or harness-specific skill definitions is outside this RFC. Constitution,
self-amendment, and Congress remain optional RFC-0002 concerns. They depend on
Core; Core does not depend on them.

---

## 7. Architecture Overview

```
Humans                              Agent harnesses
   │ human-first CLI                       │ stateless MCP connector
   └───────────────────┬───────────────────┘
                       ▼
             Wormhole project operations
                       │
          ┌────────────┴────────────┐
          ▼                         ▼
 typed .wormhole/ state       optional Fabric
          │                   live collaboration
          └────────────┬────────────┘
                       ▼
            Git acceptance and history
                       │
                       ▼
          GitHub / GitLab / local Git
```

The operation model is shared. Interface choice does not decide authority or
permission: project context, actor assurance, and—where a remote requires
it—authentication and project authorization do.

`.wormhole/` contains typed project state, not source-code copies. Git accepts
changes to that state through the same local, fork, review, and merge mechanisms
available to the repository. Fabric may provide a faster shared view and live
coordination path, but no Fabric-only record silently becomes accepted project
state.

### 7.1 Assurance modes

Wormhole supports three project modes:

- **Local/fork mode.** A participant works from a local clone or fork. Fabric is
  not required; project-state changes travel through Git when the participant
  chooses to share them.
- **Canonical-public mode.** The canonical public repository carries the typed
  `.wormhole/` state. Public Git history is the acceptance and distribution
  boundary. A public-project Fabric is optional and uses self-issued key
  continuity for identification rather than account authentication.
- **Private assurance mode.** The typed project state remains within private Git
  controls. A private-project Fabric is optional and does not weaken or replace
  that Git boundary.

These modes change where project state is shared and assured, not the four Core
pillars or the parity between authorised humans and agents.

### 7.2 State shape

The `.wormhole/` tree is the Git-native home for explicitly portable typed project
state. It records selected audit-significant events, task definitions and portable
task state, curated knowledge, self-declared attribution, and references to
code, including commit SHAs and change-request URLs, but never stores a second
copy of repository source or any membership, authenticator, credential,
ownership grant, Passport, or effective permission. Concrete filenames, schemas,
migrations, and merge behavior are versioned contract details.

Gateway/Fabric maintain operational task-transition chatter, progress, generic
channel activity, presence, runtime attribution, subscriptions, queues, telemetry,
conflicts/receipts, and discoveries awaiting curation. Promotion into portable
state is explicit, typed, attributed, and reviewable; checkpoint never promotes it.
Operational facilities do not create an independent accepted project truth.

---

## 8. Core Pillars

### 8.1 Event Bus

Channels carry typed events as their primary payload, with natural language as
an optional note. Representative categories include task transitions, review
requests, build failures, discoveries, and free-text messages.

Events identify their acting principal, regardless of whether that principal is a
human or an agent. Generic posts, progress, status chatter, presence, and runtime
notifications are operational and do not automatically create portable records. An
`EventV1` represents explicitly promoted audit-significant evidence whose source
activity ID/digest and attribution are reviewable. Git-native promoted events remain
available without Fabric; Fabric accelerates live publication and subscription.

### 8.2 Task Graph

Entities are **Project → Task → Subtask**, with an owner that may be a human or
agent principal, status, priority, due date, dependencies, and links to related
events, KB articles, commits, and change requests.

Task definitions, owner, and portable task status are reviewable project state;
Git acceptance makes that state accepted. Transition notifications/history and
progress chatter are operational unless explicitly promoted as audit evidence. The
task graph is a shared project model, not an agent-only queue or a human-only board.

### 8.3 Knowledge Base

The Knowledge Base is linked, typed organisational memory. Its constraints are:

- **Atomic articles.** One article represents one fact, decision, or procedure.
- **Explicit linking.** Articles link to related project records by stable
  identity.
- **Compliance checks on write.** Contributions may be checked for duplication,
  conciseness, and required links, with structured rewrite guidance.
- **Meaningful retrieval.** Semantic retrieval may supplement typed traversal
  without weakening project scope.
- **Model-agnostic content.** Knowledge is usable from the CLI, MCP-connected
  harnesses, and ordinary Git tooling.

Knowledge accepted into `.wormhole/` survives any one tool, model, machine, or
Fabric deployment.

### 8.4 Identity & Permissions

This pillar contains three related but separate concerns:

- **Identity:** a durable principal to which ownership, attribution, and audit
  records attach. Humans and agents are distinct principals. An agent is not a
  human's session or alias; every agent has accountable human ownership.
- **Authentication:** evidence that a caller controls or represents a principal.
  A credential or Passport may provide that evidence, but it is not the
  identity itself.
- **Authorization:** the project-scoped decision that a principal may perform a
  requested operation. Private remote authorization follows authentication;
  local and public Git contribution do not acquire authority from a credential.

Agents remain durable across harness processes and model sessions. Their human
ownership establishes accountability without collapsing the agent into the
owner's identity or requiring approval for every authorised action. Humans and
agents may hold project roles and permissions; Core does not reserve ordinary
project operations to one class.

### 8.5 Project Setup

`wormhole setup` is the single onboarding and project-configuration flow. It
replaces the legacy `wormhole join` and `wormhole connect` concepts.

Setup establishes the project mode, locates or initializes typed `.wormhole/`
state, and configures optional Fabric use when selected. It does not make Fabric
a prerequisite for a valid project, and it does not conflate creation of a
principal with authentication or authorization.

The CLI presents setup for humans. Harnesses use the configured project through
the stateless MCP connector; they do not acquire authority by opening a durable
MCP session.

### 8.6 Git Integration

Git is a cross-cutting integration, not a fifth Core pillar. Wormhole stores
pointers and commentary—commit SHAs, change-request URLs, summaries, review
requests, and their relationships to tasks, events, and knowledge. It never
stores source bodies as an alternative repository.

Project-state changes are proposed and accepted through Git. Fabric can reduce
the latency of seeing and coordinating those changes, but acceptance remains a
Git operation.

---

## 9. Interface Surfaces

### 9.1 Human-first CLI

The `wormhole` CLI is the primary human interface for setup and project
operations. It presents the same Core concepts stored under `.wormhole/` and,
when configured, accelerated by Fabric.

### 9.2 Stateless MCP connector

MCP is the vendor-neutral harness connector. Its tools expose Event Bus, Task
Graph, Knowledge Base, Identity & Permissions, and Git-reference operations to
coding agents.

Stateless means the connector is not a principal, does not become a source of
project truth, and does not rely on a durable harness session to establish
identity or authorization. Each operation is evaluated in project context with
an attributed principal and explicit assurance; private remote operations also
require authentication and authorization. Exact tool names and request/response
schemas are separate interface contracts.

CLI and MCP are not required to have identical syntax. They are required to
offer equivalent authorised project operations so that humans and agents can
collaborate on one model.

---

## 10. Governance (Out of Scope — see RFC-0002)

Constitution, continuous policy improvement, and Congress are specified in
**[RFC-0002: Wormhole Governance](wormhole_rfc_governance.md)**. Governance is
an optional product decision independent of whether a project uses Core or
Fabric.

RFC-0002 depends on Core's pillars existing first. Core does not depend on
RFC-0002, and governance does not alter Git's authority without an explicit
future decision.

---

## 11. Deployment and Operation

- **Git-only operation** is valid in local/fork, canonical-public, and private
  assurance modes.
- **Fabric is optional** for both public and private projects. It accelerates
  live collaboration, discovery, and shared projections.
- **Self-hosted or managed Fabric** may be offered without changing the Core
  authority model.
- **No mandatory Fabric telemetry or phone-home** is required for Git-only or
  self-hosted operation.

The presence or absence of Fabric changes collaboration latency and
availability characteristics, not which project operations exist.

---

## 12. Core Scope

The initial Core scope is deliberately narrow:

- Typed `.wormhole/` project records for curated events, task definitions/portable
  task state, curated knowledge, attribution, and code pointers; operational and
  authority records remain private or Fabric-side
- Durable human and agent principals with accountable human ownership for agents
- Separate authentication and project authorization
- `wormhole setup` for project initialization and configuration
- Human-first CLI project operations
- Stateless MCP access to equivalent project operations
- Git pointers without code copies
- Optional Fabric integration for public and private projects

Explicitly outside Core are autonomous governance (RFC-0002), rich-media chat,
replacement Git hosting, and mandatory hosted infrastructure.

---

## 13. Security and Assurance Considerations

- Authentication proves control of a principal; it does not by itself authorize
  a project operation.
- Authorization is project-scoped and applies to human and agent principals by
  role and operation, not by a blanket caller-class hierarchy.
- Every agent has accountable human ownership while retaining its own durable
  identity and audit attribution.
- Public projects intentionally expose accepted `.wormhole/` state through
  public Git. Structural secret-shape validation does not prove confidentiality;
  trusted machine-private `public_git` classification—whether canonical or fork—warns
  accordingly and checkpoint requires a matching
  attributed publication-review digest acknowledgement from either CLI or MCP.
  This is intent/CAS, not authorization or DLP, and direct Git edits remain possible.
  Private assurance mode keeps state within the project's private Git boundary.
- Publication visibility is explicit user policy bound to the workspace/repository, not
  continuous Git-host visibility detection. Repository-identity changes invalidate it;
  same-identity host visibility changes require explicit reconfiguration and are always
  the operator's responsibility.
- Fabric must respect the selected public or private project boundary. Because
  Fabric is optional, loss of Fabric availability does not erase Git-accepted
  project state.
- Wormhole never stores source code outside Git as a competing truth.
- Operational retention is finite: presence is restart-discardable; ordinary activity
  expires by age or by falling outside the newest 10,000 unprotected workspace rows,
  with deterministic oldest-first pruning; pending lifecycle evidence is protected until
  terminal, then defaults to exactly 30 days (or an advertised finite longer duration).
  Expiry cannot mutate portable Git state.

---

## 14. Roadmap

| Phase | Scope | Depends on |
|---|---|---|
| Core | Four pillars, typed `.wormhole/` state, setup, CLI, stateless MCP | Git |
| Fabric | Optional live collaboration for public and private projects | Core |
| Extensions | Additional integrations and projections | Core; Fabric only where needed |

Governance is not a phase of this roadmap. RFC-0002 is versioned and adopted
independently.

**Core exit criterion:** after `wormhole setup`, an authorised human and an
authorised agent can inspect and change the same typed project state through CLI
and MCP, respectively; Git can accept those changes; and the project remains
usable without Fabric. When Fabric is configured, both can observe accelerated
live collaboration without changing the Git authority boundary.

---

## 15. Decision Register

### Decided

- **Core pillars:** Event Bus, Task Graph, Knowledge Base, and Identity &
  Permissions remain the four Core pillars. Git integration is cross-cutting.
- **Operational parity:** Humans and agents can perform the same authorised
  project operations; Wormhole remains agent-first in schemas, retrieval, durability,
  attribution, and handoff.
- **Identity model:** Identity, authentication, and authorization are separate.
  Agents are distinct durable principals with accountable human ownership.
- **Project authority:** Explicitly curated portable project state is Git-native under
  `.wormhole/`. Git is the sole source of truth for code and accepts those changes.
- **Assurance modes:** Core supports local/fork, canonical-public, and private
  assurance modes.
- **Onboarding:** `wormhole setup` replaces legacy join/connect onboarding.
- **Interfaces:** The CLI is human-first; MCP is a stateless harness connector.
- **Fabric:** Fabric is optional for both public and private projects and
  accelerates live collaboration without replacing Git authority.
- **Authority boundary:** Git accepts explicitly curated portable project context;
  Gateway/Fabric own operational activity under finite retention. `EventV1` is selected
  audit evidence, never the automatic representation of every channel or task event.
- **V1 scope:** Authority is project/repository-lineage scoped. Organisation-wide and
  cross-repository graphs, merged KBs, inherited policy, and cross-project authority
  require a separate RFC.
- **Durable event discovery:** Each Fabric uses its Postgres change records,
  polled by bound Gateways during sync. Ephemeral Gateway notifications may
  wake local consumers but are not a second durable coordination store.
- **KB compliance:** Compliance failures use soft rejection with structured
  rewrite suggestions. Thresholds remain tunable configuration rather than
  architecture.
- **KB isolation:** KB reads are strictly project-scoped. A Gateway serving
  several projects keeps separate namespaces and never constructs an implicit
  merged KB.
- **MCP hosting boundary:** Wormhole exposes its own project-operation MCP
  surface. It does not host arbitrary unrelated MCP servers; harness connector
  installation and approved project skill bootstrap do not change that boundary.
- **Fabric tenancy:** Project-scoped Fabric data remains isolated by Postgres
  RLS in addition to application checks.

### Contract details

The approved [Git-Native Wormhole Architecture Design](../superpowers/specs/2026-07-28-git-native-wormhole-architecture-design.md)
defines the version-one snapshot layout, base/overlay/checkpoint semantics,
Fabric routing, assurance modes, setup lifecycle, and delivery slices. Public
wire schemas remain versioned implementation contracts and may not weaken the
decisions above.

---

## 16. Example User Stories

- *Model-switch continuity:* "I switched agent harnesses, and the new agent read
  the same accepted project state from Git instead of reconstructing it from a
  vendor-specific memory."
- *Human-agent parity:* "I updated a task through the CLI, while my coding agent
  updated the same task model through MCP; permissions and attribution were
  evaluated per principal, not per interface."
- *Fork workflow:* "My fork carried its Wormhole project state without requiring
  a live service, and I proposed the relevant changes upstream through Git."
- *Public collaboration:* "Our public repository exposed its accepted project
  context, and optional Fabric made active coordination faster."
- *Private assurance:* "Our project state stayed inside private Git controls,
  while our private Fabric accelerated collaboration without becoming the
  authority."

---

## 17. Glossary

- **Principal** — a durable human or agent identity to which ownership,
  attribution, roles, and audit records attach.
- **Agent** — an autonomous or semi-autonomous AI system acting as its own
  durable principal, with accountable human ownership.
- **Authentication** — verification that a caller controls or represents a
  principal.
- **Authorization** — a project-scoped decision allowing a principal to perform
  an operation.
- **Event** — a typed, timestamped project record.
- **Task graph** — tasks and subtasks with ownership, status, and dependency
  links.
- **KB article** — an atomic, linked unit of organisational knowledge.
- **`.wormhole/`** — the repository-local home of typed Wormhole project state.
- **Fabric** — an optional service that accelerates live collaboration for
  public or private projects without replacing Git authority.
- **MCP connector** — the stateless harness-facing adapter to Wormhole project
  operations.
- **Assurance mode** — the selected local/fork, canonical-public, or private
  boundary for sharing and accepting project state.

---

## 18. References

- Anthropic, [Effective harnesses for long-running agents](https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents)
- [Model Context Protocol](https://modelcontextprotocol.io/)
- [agents.md](https://agents.md/)
- [RFC-0002: Wormhole Governance](wormhole_rfc_governance.md)
- [RFC-0003: Wormhole Local Runtime](wormhole_rfc_local_runtime.md)
- [Git-Native Wormhole Architecture Design](../superpowers/specs/2026-07-28-git-native-wormhole-architecture-design.md)
- Prior internal drafts: `slack_for_agents.md`, `slack_for_agents_revised.md`, `AIOS_V3_Proposal.md`
