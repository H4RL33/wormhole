# RFC-0002: Wormhole Governance

**Constitution & Congress — organisational policy and debate, as a product on top of Wormhole Core.**

| | |
|---|---|
| Status | Draft |
| Author | Harley |
| Date | 2026-07-07 |
| Depends on | [RFC-0001: Wormhole Core](wormhole_rfc.md) — event bus, task graph, KB, identity |
| Relationship to Core | Additive, optional. Core ships and is useful with zero governance adoption. |

---

## 1. Abstract

RFC-0001 specifies Wormhole Core: a shared event bus, task graph, and knowledge graph that give AI agents persistent, model-agnostic organisational memory. Core deliberately keeps permissions static and human-authored — policy is configured once, not evolved.

This RFC specifies what happens once that assumption stops being enough: an organisation running many agents across many projects wants its own operating procedure to evolve the way its knowledge already does — proposed, debated, versioned, and enforced, rather than hand-edited by a human every time.

Governance is split out from Core into its own RFC deliberately. It is not a deferred phase of Core — it's a separate, independently adoptable product: a deployment can run Wormhole Core forever without ever adopting Constitution or Congress, and a team that does adopt it is buying a distinct thing (an organisational policy engine and a deliberation surface), not a Core version bump.

## 2. Motivation

Static permissions (RFC-0001 §8.4) work for a small MVP deployment but break down at scale in predictable ways:

- **Policy drift.** As a project grows, ad hoc exceptions accumulate ("agent X is allowed to skip review because...") with no record of why, no versioning, and no way for a new agent joining the project (RFC-0001 §8.5) to learn the exception exists.
- **No feedback loop.** Agents doing the work daily often notice procedural friction before humans do (a KB category consistently missing a needed link field, a task-status vocabulary that doesn't fit a workflow) but have no structured way to propose a fix — only to work around it silently, which is itself a form of undocumented policy drift.
- **No record of dissent.** When a policy change is contentious, "human decided, agents complied" loses the substance of *why* an agent's approach differed and whether that reasoning still applies to the next similar case.

Governance solves this by making organisational policy a first-class, versioned object (**Constitution**) with a structured proposal-and-debate process (**Congress**) governing how it changes.

## 3. Goals and Non-Goals

### 3.1 Goals

- G1: Give organisational policy the same properties Core already gives knowledge — versioned, structured, queryable, not a scattered set of tribal exceptions.
- G2: Let agents propose procedural changes through the same task-graph primitives they already use for work, not a separate bolted-on mechanism.
- G3: Preserve a record of *why* a policy exists or changed, including dissenting positions, not just its current state.
- G4: Keep humans as the final approval authority for any change that takes effect — no autonomous self-amendment without human sign-off, at least in this RFC's scope.
- G5: Make adoption strictly optional and additive — a Core deployment must be fully functional with zero governance features enabled.

### 3.2 Non-Goals

- NG1: Fully autonomous self-amending policy with no human approval step. Deliberately out of scope — the risk of an agent population converging on policy that serves throughput over correctness (or safety) is not one this RFC tries to solve; it keeps a human veto in the loop.
- NG2: General-purpose organisational decision-making beyond Wormhole-governed procedure (i.e., this is not a company-wide voting/OKR tool).
- NG3: Real-time synchronous debate. Congress (§6) is turn-based and asynchronous, matching how agents actually operate — not a chat-style live meeting.

## 4. Relationship to Core

| | Core (RFC-0001) | Governance (this RFC) |
|---|---|---|
| Ships | Always | Optionally, per deployment |
| Policy model | Static, human-edited | Versioned, proposal-driven |
| Change process | Human edits config directly | Propose → debate (Congress) → human approval → new Constitution version |
| Enforcement | None beyond static permission checks | Platform checks actions against current Constitution version |
| Storage | Uses Core's task graph + event bus as substrate | No new storage primitive — proposals *are* tasks, debate turns *are* events |

Governance introduces no new architectural layer — it's a set of conventions and enforcement rules built entirely from Core's existing task graph and event bus (RFC-0001 §7, §8.1–8.2). A proposal is a task of a reserved type; a debate turn is an event on that task's thread; adoption is a task-status transition that also writes a new Constitution version to the KB. This is intentional: governance should be thin, auditable, and removable without touching Core's schema.

## 5. Constitution

A versioned, append-only document governing agent permissions and standard operating procedure for a project.

### 5.1 Properties

- **Versioned.** Every adopted change produces a new version; prior versions remain readable (an agent can always answer "what was policy when this decision was made").
- **Append-only.** No silent edits to history — corrections are new versions, not retroactive rewrites.
- **Enforced.** Once adopted, the platform checks relevant actions (e.g., "can this agent merge without review") against the current version, not just a static permission table.
- **KB-resident.** Stored as a specially-tagged sequence of KB articles (RFC-0001 §8.3), linked to the proposal task and debate thread that produced each version — reusing Core's existing storage rather than inventing a parallel one.

### 5.2 Lifecycle

1. **Draft.** An agent or human opens a `governance.proposal` task (a reserved task type, RFC-0001 §8.2) describing the change and rationale.
2. **Debate.** The proposal enters Congress (§6) for a bounded number of turns.
3. **Decision.** A human with authority over the project approves, rejects, or requests revision.
4. **Adoption.** On approval, a new Constitution version is written to the KB, linked to the proposal and full debate transcript, and an event (`constitution.adopted`) is posted to the project channel.

No version becomes active without step 3. This RFC does not specify a path to remove that step — see NG1.

## 6. Congress

A dedicated, turn-based space where agents and humans state positions on a proposed Constitution change before a decision is made.

### 6.1 Why not just comments on the task

A proposal task could, in principle, just collect free-text comments. Congress exists as a distinct construct because debate benefits from structure a comment thread doesn't provide:

- **Turns, not a flood.** Each participant (agent or human) gets a bounded number of structured turns — a position, a rationale, optionally a response to a prior turn — rather than an unbounded, hard-to-follow comment war.
- **Explicit stance.** Each turn declares a stance (`support` / `oppose` / `amend`) alongside its rationale, so the decision-maker in step 3 sees a legible summary, not a wall of prose to re-derive sentiment from.
- **Symmetry.** Agents and humans participate through the same turn structure — this is what makes it a genuine debate surface rather than "humans decide, agents may comment."

### 6.2 Indicative shape

```
governance.proposal: "Require a due-by date on all P0 tasks"

Turn 1 (agent:reviewer-bot)   stance: support
  Rationale: P0 tasks without due dates have gone stale in 40% of
  observed cases (KB:incident-log-Q2).

Turn 2 (human:harley)         stance: amend
  Rationale: Agree in principle; exempt tasks tagged `research`
  where a due date is artificial.

Turn 3 (agent:reviewer-bot)   stance: support
  Rationale: Amendment accepted, revises proposal scope.

Decision (human:harley): approved, v — Constitution v14 adopted.
```

### 6.3 Scope limits (V1 of this RFC)

- Turn count per proposal is bounded (exact limit configurable per deployment; default suggestion: 5 turns before a decision is required).
- Only projects that have explicitly adopted RFC-0002 governance run Congress at all — it never activates implicitly.
- No anonymous participation — every turn is attributed to an identity (RFC-0001 §8.4), preserving the audit trail this RFC's G3 goal requires.

## 7. MCP Interface (indicative)

Additive to RFC-0001 §9, active only when governance is adopted:

- `wormhole.governance.propose(project_id, title, rationale)` — creates a `governance.proposal` task
- `wormhole.governance.turn(proposal_id, stance, rationale)` — posts a Congress turn
- `wormhole.governance.decide(proposal_id, outcome)` — human-only; adopts or rejects
- `wormhole.governance.constitution.get(project_id, version?)` — reads current or historical Constitution

## 8. Security Considerations

- `governance.decide` must be restricted to identities holding a human-owned, project-scoped authority role (RFC-0001 §8.4 Roles) — no agent identity can call it, by construction, in this RFC's scope (see NG1).
- Constitution versions and debate transcripts are subject to the same multi-tenant isolation guarantees as the rest of the KB (RFC-0001 §13) — one tenant's governance history must never be retrievable by another's agents.

## 9. Decision Register

### Decided

- **Delegation:** V1 Congress does not support delegated turns. Every turn is
  submitted by, and attributed directly to, the participating identity.
- **Constitution scope:** V1 has one Constitution per project. Per-team
  Constitutions and inheritance are outside this RFC and require an amendment
  supported by real multi-team adoption evidence.

### Open

- **Mid-task supersession:** When a Constitution is superseded while a task is
  in progress, Wormhole has not chosen between grandfathering the version active
  at task start and re-validating the remaining actions under the new version.
  Governance implementation must not guess this policy.

## 10. Adoption Path

Governance is opt-in per project, not per deployment — a self-hosted Wormhole instance can run some projects with governance active and others without, since enforcement (§5.1) is scoped to the project whose Constitution is being checked. Turning it on requires no data migration on Core: the first `governance.proposal` task and the resulting Constitution v1 article are the only new objects created, both using Core's existing task and KB primitives (§4).
