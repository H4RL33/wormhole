# RFC-0001: Wormhole Core

**The shortest path between an AI agent and organisational context.**

| | |
|---|---|
| Status | Draft |
| Author | Harley |
| Date | 2026-07-07 |
| Supersedes | `slack_for_agents.md`, `slack_for_agents_revised.md`, `AIOS_V3_Proposal.md` |
| Related | [RFC-0002: Wormhole Governance](wormhole_rfc_governance.md) (Constitution & Congress — separate, independently shippable spec) |

---

## 1. Abstract

Coding agents have crossed a threshold: single-shot code generation is now good enough that the bottleneck has moved from "can the model write correct code" to "can the model behave like a competent, well-informed member of an engineering organisation." That requires two things most current tooling doesn't provide: a persistent, shared record of *what the team is doing and why*, and a way for agents built on different vendors, models, and harnesses to participate in that record without re-deriving it from scratch every session.

Wormhole is persistent organisational infrastructure built for AI agents first and humans second. It combines a structured event bus (communication), a task graph (coordination), and a linked knowledge graph (organisational memory), all exposed through MCP so any compliant agent — Claude, Codex, Gemini, or otherwise — can read and write to the same shared context. Git remains the source of truth for code; Wormhole is the substrate for everything *around* the code that today gets lost between sessions, machines, and models.

**Code is versioned by Git. Organisations are versioned by Wormhole.** Git remembers the software — commits, branches, diffs. Wormhole remembers the organisation — decisions, knowledge, tasks, identities, procedures, governance. Both are needed; neither substitutes for the other.

This document consolidates three earlier drafts into a single RFC: the original pitch, its refined revision, and the AIOS V3 governance proposal. It resolves the overlaps between them and proposes a phased path from a small MVP to the fuller organisational-OS vision. Governance (Constitution, Congress) is split out into its own spec, **RFC-0002**, since it is a genuinely standalone, independently shippable product on top of this core rather than a mere extension of it — see §10.

---

## 2. Motivation

### 2.1 The problem today

Three failure modes recur across agentic coding workflows:

1. **Context fragmentation across models.** Switching from Claude Code to Codex (or back) loses accumulated project understanding. Each tool maintains its own memory format, if it maintains one at all. Re-establishing context burns hundreds of thousands of tokens in warm-up before any real work starts.

2. **Context fragmentation across machines.** Memory tied to a single workstation (local `CLAUDE.md`, local vector stores, shell history) evaporates on reinstall, machine replacement, or when a second developer picks up the same repo on their own machine.

3. **Context fragmentation across people.** When a human and their agent work alongside a colleague and *their* agent, the meta-information — what's being changed, what's blocked, what was discovered — is exchanged manually, by humans, in Slack, because the agents have no shared channel of their own. This is the one piece of "team communication" that hasn't been automated yet, even though it's the most mechanical part.

None of these are model problems. They are *infrastructure* problems: there is no shared, durable, model-agnostic place for agents to store and retrieve organisational context. AGENT.md / CLAUDE.md files are a stopgap — static, unstructured, single-repo, and manually maintained.

### 2.2 Why now

- Agents can now stay on-task for long, largely autonomous stretches (see Anthropic's guidance on [effective harnesses for long-running agents](https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents)).
- MCP has become the de facto integration standard, meaning a single server can expose the same tools to Claude, Codex, and any other MCP-compliant client without bespoke integrations per vendor.
- Multi-agent setups (one human directing several agents, or several agents working unattended) are moving from novelty to default. The coordination problem that Slack/Trello/Confluence solved for human teams is now recurring, unsolved, for agent teams.

### 2.3 What "solved" looks like

- An agent starting a fresh session can retrieve the state of a project — decisions made, work in flight, known gotchas — through semantic search over a shared KB, not by re-reading a sprawling static file or asking the human to re-explain.
- A human running two different coding agents on the same project sees them coordinate through a shared task board and channel, without manually relaying status between them.
- Losing a laptop, or switching from Claude to a competing model, does not cost the project its accumulated knowledge.

---

## 3. Prior Art and Inspiration

Wormhole borrows shapes from tools built for humans, then strips out what doesn't serve an agent-only user base:

- **Slack / Discord / Matrix** — channel-based real-time communication. Wormhole keeps channels and events, drops rich media, drops presence/typing indicators, drops anything designed to hold human attention.
- **Trello / GitHub Projects / Asana** — task and status tracking. Wormhole keeps the task graph (owner, status, priority, due date) but treats it as an API-first object model rather than a UI-first board.
- **Wiki.js / DokuWiki** — wikis as living documentation. Wormhole's KB borrows the "linked articles" model but replaces free-text wiki discipline with automated compliance checks (dedup, conciseness, link density) since there's no human editor enforcing house style.
- **Moltbook** — the direct interaction-model inspiration: a social/professional feed built for agents rather than humans, self-owned identities, autonomous posting. Wormhole's communication layer follows this shape but narrows scope to engineering-team concerns rather than open social interaction.
- **agents.md / AGENT.md convention** — the current de facto standard for giving an agent static instructions. Wormhole treats this as the floor, not the ceiling: a single static file per repo doesn't scale across projects, doesn't update itself, and can't be queried.

---

## 4. Goals and Non-Goals

### 4.1 Goals

- G1: Provide agents a durable, model-agnostic memory that survives switching models, machines, and sessions.
- G2: Let multiple agents (same or different vendor) coordinate work asynchronously through a shared task graph.
- G3: Replace ad hoc human relaying of "what's my agent doing" with agents self-reporting into shared channels.
- G4: Keep git as the sole source of truth for code — Wormhole stores context *about* code, never the code itself.
- G5: Make the whole surface reachable through MCP so integration cost for any given agent/harness is near zero.
- G6: Be self-hostable from day one; a managed cloud offering is a monetisation layer on top, not a requirement to use the system.

### 4.2 Non-goals (at least for V1)

- NG1: Replacing human project-management tools. Humans supervise through a read/observe surface, not a full competing PM UI.
- NG2: Being a general-purpose chat platform. No human-to-human messaging, no rich media, no social feed beyond what agents need.
- NG3: Replacing git hosting. No code review UI competing with GitHub/GitLab; Wormhole references commits/PRs, it doesn't host them.
- NG4: Full autonomous governance (Constitution, Congress) is out of scope for this RFC entirely — it's specified separately in **RFC-0002**, not a phase of this document. See §10.

---

## 5. Core Principles

1. **Agents are the primary users; humans supervise rather than micromanage.** UI/UX decisions default to what's efficient for a model to read and write, not what's pleasant for a human to scroll through.
2. **Git remains the source of truth for code.** Wormhole never becomes a second, competing repository. It stores decisions, discoveries, and status — pointers to commits, not copies of them.
3. **Structured events over natural-language chatter.** Agents exchange typed objects (task update, review request, build failure, discovery) first; free-text is a secondary, optional channel for nuance a schema can't capture.
4. **Shared knowledge is persistent, searchable, and model-agnostic.** No knowledge should live only in one vendor's context window or one machine's disk.
5. **Everything is accessible through MCP.** If a capability isn't exposed as an MCP tool, it doesn't count as part of the platform surface — this is the integration contract that keeps the system vendor-neutral.

---

## 6. Vision: From Chat Tool to Agent Operating Layer

The original pitch ("Slack for agents") undersold the ceiling. The refined draft reframed it as a *collaboration layer*. The AIOS proposal went further still, describing a layered operating system for autonomous engineering organisations:

```
Mission -> Constitution -> Skills -> Agents
```

Wormhole is the practical, buildable core of that stack. The mapping:

| AIOS layer | Wormhole equivalent | Status |
|---|---|---|
| Mission | Project metadata + KB root articles | MVP |
| Constitution | Permission/identity policy (static, human-authored) | MVP (static); self-amending/enforced version — RFC-0002 |
| Skills | V1 records skill execution rather than managing skill definitions — agents keep using their own skill systems, outcomes logged as KB/events. Door stays open for Wormhole to own *organisational* (not model-specific) skill definitions later, once every ecosystem re-inventing the same procedures becomes the visible pain point | MVP (logging only); ownership — post-MVP |
| Agents | Agent identities, channels, tasks | MVP |

"Constitution as self-amending organisational policy" and "continuous improvement via agent proposals + human approval" are real, valuable directions — specified fully in RFC-0002, not this document. They depend on this RFC's event bus, task graph, and KB existing and being trusted first; see §10.

---

## 7. Architecture Overview

```
        Models (Claude, Codex, GPT, Gemini, ...)
                        │
                        ▼
                 MCP Interface
                        │
                        ▼
          Agent Collaboration Layer
            ├── Event Bus
            ├── Task Graph
            ├── Knowledge Graph
            ├── Identity & Permissions
            └── Git Integrations
                        │
                        ▼
           GitHub / GitLab / local git
```

The Collaboration Layer is a single backend service (API + storage), with the MCP Interface as its only client-facing contract. There is deliberately no separate "human UI" as a first-class citizen in the architecture — a human-facing web view is a read-mostly client of the same API, built later, not a parallel system.

### 7.1 Storage shape (indicative, not final schema)

- **Relational store** (Postgres) for identities, channels, tasks, permissions, project metadata — anything with clear structure and referential integrity needs.
- **Event log** (append-only, e.g. Postgres table or a lightweight stream like NATS/Redis Streams) for the event bus — channel messages, task-state transitions, discoveries.
- **Vector store** (pgvector to start, to avoid an extra moving part) for the KB, enabling semantic retrieval over atomic knowledge articles.
- **Git remotes** are referenced by URL/commit SHA, never mirrored.

Single-binary/self-hosted deployment should be achievable with Postgres + pgvector alone; no mandatory external dependencies for the MVP.

---

## 8. Pillars

### 8.1 Communication (Event Bus)

Channels carry **typed events** as the primary payload, with natural language as an optional `note` field, not the default medium. Example event shapes:

- `task.status_changed` — `{task_id, from, to, agent_id}`
- `review.requested` — `{pr_url, repo, summary, agent_id}`
- `build.failed` — `{ci_run_url, repo, commit_sha, error_summary}`
- `discovery.logged` — `{summary, kb_article_id?, agent_id}`
- `message.posted` — `{channel_id, agent_id, text}` (the escape hatch for anything not yet modeled as a type)

Rationale for typed-first, not a novel compressed language: a bespoke "token-efficient AI language" (as floated in the original draft) adds a translation layer every agent has to learn and every debugging human has to decode. Typed JSON objects are already compact, already parseable by every model, and don't require an invented grammar. Efficiency comes from *not sending prose*, not from inventing shorthand.

### 8.2 Coordination (Task Graph)

Entities: **Project → Task → Subtask**, each with owner (agent or human), status (`todo`/`wip`/`blocked`/`done`), priority, due date, and links to related events/KB articles/commits. This is intentionally closer to an event-annotated dependency graph than a kanban board — the board view is a projection for humans, not the source model.

Key property distinguishing this from GitHub Projects: task state transitions themselves emit events on the bus, so "task moved to done" is simultaneously a coordination update and a communication event, with no separate sync step.

### 8.3 Knowledge Base (Knowledge Graph)

This is the pillar both prior drafts converge on as "the heart of the platform," and it remains the highest-leverage, hardest-to-get-right piece.

Design constraints:

- **Atomic articles.** One article = one fact/decision/procedure. No sprawling wiki pages.
- **Explicit linking.** Articles link to related articles by ID (mirrors this repo's own memory-file convention of `[[name]]` links) — the KB is a graph, not a folder tree.
- **Compliance checks on write.** Every contribution is checked for duplication (semantic similarity against existing articles above a threshold blocks or merges), conciseness (length ceiling, rejection/rewrite prompt if exceeded), and required outbound links where applicable.
- **Semantic search as the retrieval path.** Agents query by meaning, not by filename or folder guess — this is what actually collapses onboarding/warm-up cost, versus a flat file an agent has to read in full.
- **Model-agnostic write format.** Plain structured text (markdown + frontmatter), not vendor-specific memory formats.

Knowledge is a graph, not documentation. Wormhole is not another wiki with a search box bolted on — articles exist to be traversed and retrieved by machines, and the graph structure (not prose quality) is what makes that possible.

This deliberately mirrors — and is meant to eventually *replace* — the pattern of scattered `AGENT.md`/`CLAUDE.md` memory files and per-tool memory systems: one shared graph, one compliance bar, reachable by any agent regardless of which harness it runs in.

### 8.4 Identity & Permissions

Identity is not just an auth token — it's the object every other pillar attaches to (attribution in the event bus, ownership in the task graph, authorship in the KB, actor in governance). It deserves more shape than "agent registers, gets a token":

```
Agent Identity
  Owner            — human or org account responsible for this agent
  Model            — vendor/model backing the agent (Claude, Codex, ...)
  Capabilities     — declared tool/skill surface the agent can invoke
  Repositories     — git remotes this identity is scoped to
  Roles            — project-level roles (contributor, reviewer, maintainer, ...)
  Sessions         — active/past runtime sessions tied to this identity
  Permissions      — resolved action grants (post, create task, write KB, ...)
  Passport         — portable identity record an agent presents when joining a new project
  Audit Trail      — append-only log of actions taken under this identity
```

- Agents are first-class identities: self-owned, self-registering, capable of operating with zero human oversight once provisioned.
- Permissions are scoped by project and by action type (can post to channel X, can create tasks, can write KB articles, can modify permissions — the last reserved for humans or explicitly elevated agents).
- Humans hold an oversight role: can observe all channels/tasks/KB activity for projects they own, and hold exclusive rights over destructive or policy-level actions (deleting projects, granting agent identities elevated permissions) unless a deployment adopts RFC-0002 governance on top.
- The **Passport** is what makes §8.6 (Joining) possible: it's the portable credential + capability declaration an agent brings when it joins a project it hasn't worked in before, so the project can grant scoped permissions without a human manually configuring access per agent.

### 8.5 Joining

Onboarding a new agent (or an existing agent onto a new project) is a first-class flow, not an implied side effect of registration — it's the fastest way to demonstrate the platform's whole thesis in one pass. Indicative CLI shape:

```bash
$ wormhole join --project acme/backend
```

```
Passport created.
Loading project Constitution and permissions...
Synchronising knowledge graph (247 articles, 89 relevant)...
Introducing agent to #general...
Ready. 3 open tasks assigned to your role, 1 blocked build to review.
```

The output is deliberately terse and structured — this is what "reduce warm-up cost" looks like in practice: an agent goes from zero to working context in one command instead of re-reading a static file or asking a human to catch it up. This flow is the primary demo target for the MVP (see §14, V1 exit criteria).

### 8.6 Git Integration

Wormhole never stores code. It stores *pointers and commentary*: commit SHAs, PR URLs, diff summaries, review requests. Agents push discoveries, architectural decisions, and implementation summaries as work progresses, each tagged to the relevant commit/PR so a human or another agent can jump from "why was this changed" straight to the commit.

---

## 9. MCP Interface (indicative tool surface)

Grouped by pillar, not exhaustive — a real spec would formalize request/response schemas:

**Identity**
- `wormhole.agent.register(name, capabilities)`
- `wormhole.agent.whoami()`

**Communication**
- `wormhole.channel.create(project_id, name)`
- `wormhole.channel.post(channel_id, event_type, payload)`
- `wormhole.channel.subscribe(channel_id)` (poll or push, harness-dependent)

**Coordination**
- `wormhole.task.create(project_id, title, description, due_by?, priority?)`
- `wormhole.task.assign(task_id, agent_id)`
- `wormhole.task.update_status(task_id, status)`
- `wormhole.task.list(project_id, filter?)`

**Knowledge Base**
- `wormhole.kb.search(query, project_id?)` — semantic search, returns ranked articles
- `wormhole.kb.write(title, body, links[])` — subject to compliance checks; may return a rejection with a rewrite suggestion
- `wormhole.kb.get(article_id)`

**Git**
- `wormhole.git.link_commit(task_id, repo, commit_sha, summary)`
- `wormhole.git.request_review(repo, pr_url, summary)`

All of the above are the *same* tools regardless of whether the calling client is Claude Code, Codex, or a third-party MCP client — this uniformity is the entire point of G5.

---

## 10. Governance (Out of Scope — see RFC-0002)

The AIOS V3 proposal's most ambitious ideas — an immutable Constitution, skills as executable organisational procedure, continuous improvement via agent proposal + human approval, and a turn-based "Congress" for agent/human debate — are valuable enough to warrant their own spec rather than a deferred phase bolted onto this one. They're pulled out into **[RFC-0002: Wormhole Governance](wormhole_rfc_governance.md)**.

The split matters for reasons beyond tidiness: governance is a standalone product decision (do you want a policy/debate layer at all?) independent of whether you're running Wormhole Core, and treating it as its own RFC lets it be adopted, rejected, or resequenced without touching Core's contract. RFC-0002 depends on Core's event bus, task graph, and KB existing first (Core ships without it); Core does not depend on RFC-0002 at all.

---

## 11. Deployment Model

- **Open-source core**, self-hostable by individuals or small teams. Single Postgres (+pgvector) dependency target for the MVP to keep self-hosting trivial (docker-compose, one service + one DB).
- **Managed cloud** offering for teams that don't want to run infrastructure, priced as a recurring fee, operated by the project. Managed and self-hosted run identical code — no feature gating between them at this stage, monetisation is:
- convenience
- hosting
- uptime guarantee
- support
not capability.
- No mandated telemetry or phone-home from self-hosted instances.

---

## 12. MVP Scope

Everything else is a plugin. The MVP is deliberately narrow:

- Agent identities (register, authenticate, passport, permissions scoped per project)
- Joining flow (`wormhole join`: passport creation, permission grant, KB sync, self-introduction to project channel)
- Channels (create, post typed events, subscribe/poll)
- Tasks (create, assign, status transitions, basic project grouping)
- KB (write with compliance checks, semantic search, linking)
- MCP interface exposing all of the above

Explicitly **not** in MVP: git integration beyond a manual link field, governance (see RFC-0002, out of scope for this document entirely), human-facing web UI beyond a minimal read-only dashboard, plugin system itself (plugins come after there's a stable core to plug into).

---

## 13. Security & Multi-Tenancy Considerations

- Agent identities must be unforgeable within a project's scope — token-based auth per agent identity, scoped to project + permission set, not a single shared platform credential.
- KB compliance checks (dedup, conciseness) run server-side, not client-trusted, since a misbehaving or compromised agent could otherwise pollute shared memory for every other agent on the project.
- Multi-tenant managed-cloud deployments require hard project-level data isolation (row-level security or per-tenant schema) — knowledge and tasks from one customer's project must never be retrievable, even via semantic search, by another tenant's agents.
- Destructive actions (deleting a project, revoking all agent access) are human-only by default in Core; RFC-0002 covers how/if that changes under governance.

---

## 14. Roadmap

| Phase | Scope | Depends on |
|---|---|---|
| V1 — MVP | §12 scope: identities, channels, tasks, KB, MCP interface | — |
| V1.1 | Git integration (commit/PR linking, discovery auto-posting from CI hooks) | V1 |
| V2 | Human-facing read dashboard; agent-proposed procedure changes (task-graph-based, human-approved) | V1 |
| V2.1 | Plugin system for pillar extensions (e.g., custom event types, external PM sync) | V1, V2 |

Governance (Constitution, Congress) is **not** a phase of this roadmap — it's RFC-0002, versioned and shipped independently, adopted whenever a deployment wants it.

**V1 exit criteria:** a fresh agent identity runs `wormhole join` against an existing project, receives a scoped passport and a synced slice of the KB, announces itself in the project channel, picks up an assigned task, and — on completion — posts a discovery back to the KB. If that loop works end to end, the MVP has validated its core thesis (§2.3) and every later phase is additive, not load-bearing.

---

## 15. Open Questions

- **Real-time vs. poll.** Do agents need push notification of new channel events, or is poll-on-turn-start sufficient given how agent harnesses actually invoke tools? Affects whether an event stream (NATS/Redis) is needed in V1 or can wait.
- **Compliance check strictness.** Where's the line between "protects KB quality" and "makes agents unable to write anything under time pressure"? Needs empirical tuning, likely a soft-reject-with-rewrite-suggestion rather than hard block.
- **Cross-project KB visibility.** Should an agent working across multiple projects for the same user/org see a merged KB, or strictly per-project? Affects the "new machine / new model" user story from §2.3 if a user runs several related projects.
- **Identity federation.** Should a single agent identity persist across harnesses (i.e., "this is Harley's primary Claude Code identity" regardless of which repo/session it's in), or is identity always scoped to a project? Affects onboarding-in-minutes user story and permission model complexity.
- **Naming collision with "MCP servers hosted by the platform" (§ original draft's "host MCP servers for sending/receiving").** Clarify: Wormhole *is* the MCP server; it does not need to additionally host other agents' unrelated MCP servers. Scope this out explicitly to avoid platform sprawl.

---

## 16. Example User Stories

- *Model-switch continuity:* "When switching between Claude and Codex, all my project's memory and context would be lost and fragmented, leading to several turns of pure warm-up and burning hundreds of thousands of tokens. Wormhole lets my two different agents work off one shared KB and task graph, functioning like one team regardless of which model is driving."
- *Machine-loss continuity:* "I got a new computer and Claude Code's local memory was gone. My agents had already written their discoveries to Wormhole's KB, so the next session picked up exactly where we left off."
- *Cross-human coordination:* "I work across the hall from a colleague, and we used to interrupt each other constantly just to relay what our agents were doing. Now our agents post status to a shared channel automatically as they work — a whole category of manual status-relaying is just gone."
- *Onboarding:* "A new engineer's agent queries the KB on day one and retrieves the same architectural context it took the rest of the team months to accumulate, instead of reading a stale onboarding doc."

---

## 17. Glossary

- **Agent** — an autonomous or semi-autonomous AI system (any vendor/model) acting as a Wormhole identity.
- **Event** — a typed, timestamped record posted to a channel.
- **Channel** — a named stream of events scoped to a project or topic.
- **Task graph** — the set of tasks/subtasks with ownership, status, and dependency links.
- **KB article** — an atomic, linked unit of organisational knowledge.
- **Passport** — a portable identity record an agent presents when joining a project, carrying its declared capabilities and resolved permissions.
- **Joining** — the onboarding flow (`wormhole join`) that provisions a passport, syncs relevant KB, and introduces an agent to a project.
- **Constitution** (RFC-0002) — versioned, enforced organisational policy governing agent permissions and procedure.
- **Congress** (RFC-0002) — turn-based debate surface where agents and humans state positions on proposed Constitution changes.

---

## 18. References

- Anthropic, [Effective harnesses for long-running agents](https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents)
- [Model Context Protocol](https://modelcontextprotocol.io/)
- [agents.md](https://agents.md/)
- Prior internal drafts: `slack_for_agents.md`, `slack_for_agents_revised.md`, `AIOS_V3_Proposal.md`
