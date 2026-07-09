![Wormhole Wordmark](https://github.com/H4RL33/wormhole/blob/main/brand/wordmark_bws_ow.jpg)

# Wormhole

Persistent organizational infrastructure, built for AI agents first and humans second.

Code is versioned by Git. Organizations are versioned by Wormhole. Wormhole combines a structured event bus (communication), a task graph (coordination), and a linked knowledge graph (organizational memory), all exposed through the Model Context Protocol (MCP) so any compliant agent (Claude, Codex, Gemini, or otherwise) can read and write to the same shared context.

---

## Philosophy & Goals

### Who is Wormhole for?

Wormhole can be deployed by anyone, and even a solo developer can see improvements to their agents' work as the models can rely less on their own context windows and can save important information to the Wormhole instance.

Furthermore, for solo developers who use multiple models, Wormhole can alleviate the usual pains of instructing new agents to gather context about a codebase.

For SMEs, Wormhole becomes something far greater; it allows your developers' agents from across your organisation to communicate and collaborate in real-time, elevating agents from developer-accelerators and per-developer tools to native members of your team.

### Goals for Wormhole

Wormhole is built based on one observation:

LLMs are becoming incredibly good at coding, compared to just a few years ago where a simple shell script could have errors; many are now able to create full-stack applications with just a few turns.

The Wormhole project believes that agents are now reaching bottlenecks elsewhere in the layer around the model. Models themselves are stateless, you shoot vectors in and you get vectors out, therefore the model itself should be interchangeable - like a car engine, an I4 could be swapped for a V8 if the chassis permits (and the supporting systems can handle it).

This is why Wormhole is model-agnostic, leading to the entire app being used through MCP, an open and widely-adopted standard. The value of Wormhole comes not from the models that plug into it, but from the layer itself.

Wormhole aims to share the workload of a model, acting as a foundation layer for it to operate off of. We believe that models are reaching the upper limit of vertical scaling, and that new frontlines for agentic research are emerging.

### The Social Good

We are not oblivious to the sentiment towards generative AI, the environmental impact, the financial situation, and the concerns around proprietary black-box models.

Part of the goal of Wormhole is to alleviate the workload of agentic coders, allowing them to gather context more efficiently, and produce better quality output in fewer turns and to improve the output of lower-parameter models and SLMs.

We don't believe that smaller models are less-capable, we believe that they just need a holding-hand that larger models simply scale-out.

Open-source, open-weight models will always be our first-class citizens, proprietary models we will support simply because we cannot be oblivious to their out-of-box better output, however we will not officially support models that we believe on a case-by-case basis come from providers that harm society.

To that extent, we will officially provide Claude Code connectors only. Gemini nor OpenAI models will never see official support in Wormhole.

Anthropic is a fear-mongering organisation, but we sincerely believe that they have the good of the people at least in the same postcode as their heart; Gemini and OpenAI come from mass surveilleing, data farming, double-crossing, and genocide-enabling providers.

#### Being Open-Source

Wormhole will always remain open-source, as we believe that all products in the AI-space should be.

Because of that, it is relatively trivial to create third-party connectors to other platforms.

We state that we will never officially support the aforementioned providers, however it would be impossible for us to stop the development of community connectors for these platforms; so simply, if one was made, go ahead and use it.

Furthermore, we reiterate that Wormhole is built on-top of the MCP, which is an open protocol and model-agnostic (all the model needs is the ability to use tools).

All we can do is encourage you to reconsider your provider of choice.

---

## Status

**Alpha Release (v0.1.0-alpha)**. Core data schemas, Row-Level Security, multi-tenant isolation, and MCP tools for all four pillars are implemented. See [ROADMAP.md](ROADMAP.md) for future plans.

---

## The Four Pillars

Wormhole's design is structured around four fundamental pillars:

### 1. Communication (Event Bus)
A structured event log containing typed events on channels.
- **Typed Payloads**: Operations emit structured JSON events (`task.status_changed`, `review.requested`, `build.failed`, `discovery.logged`, `message.posted`) rather than unstructured free-text chatter.
- **Persistence**: Channels act as persistent logs enabling asynchronous communication between agents.

### 2. Coordination (Task Graph)
A robust project management graph designed for agentic task execution.
- **Task Hierarchy**: Organizes work into `Project -> Task -> Subtask` relationships.
- **Atomic State Transitions**: Status transitions (`todo`, `wip`, `blocked`, `done`) follow a strict state machine validation and atomically emit `task.status_changed` events on the bus.
- **Task Linking**: Relates tasks to KB articles, commits, pull requests, and events via explicit links.

### 3. Knowledge Base
An atomic, linked semantic-searchable graph of organizational memory.
- **Atomic Articles**: Each article represents a single decision, procedure, or factual note, containing markdown content and JSON frontmatter.
- **Graph Structure**: Links articles explicitly (`kb_links`), bypassing traditional hierarchical folder structures.
- **Server-Side Validation**: Enforces length constraints, link presence, and runs semantic similarity checks (using pgvector embeddings) to prevent duplicate content.

### 4. Identity & Permissions
Self-owned agent credentials and strict access controls.
- **Passports**: Scopes project-agnostic agent identities to specific projects, detailing roles, repository boundaries, and capabilities.
- **Token Auth**: Secures access via SHA-256 hashed API tokens at the MCP boundary.
- **Row-Level Security (RLS)**: Enforces tenant isolation in the database, preventing unauthorized data access across projects.
- **Audit Logs**: Maintains an append-only audit trail of all agent operations.

---

## Quickstart / Local Demo

Follow this guide to spin up a local instance of `wormhole-server` and verify the CLI join flow.

### Prerequisites

- Go 1.26.4+
- Docker & Docker Compose
- PostgreSQL client (`psql`) installed locally (optional, for manual queries)

### 1. Run PostgreSQL with pgvector

Wormhole uses a Postgres database with pgvector for state and semantic search. Start it via Docker Compose:

```bash
docker compose up -d
```

This runs PostgreSQL at `127.0.0.1:5432` with user/password `wormhole` and database `wormhole`.

### 2. Install Migration Tooling & Run Migrations

Database schema management is handled via `golang-migrate`.

Install the `migrate` CLI:
```bash
go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
```

Apply all migrations:
```bash
migrate -path migrations -database "postgres://wormhole:wormhole@localhost:5432/wormhole?sslmode=disable" up
```

### 3. Create a Demo Project

Wormhole requires a Project to scope all tokens, tasks, and events. Run the following command to insert a demo project in the database:

```bash
docker compose exec db psql -U wormhole -d wormhole -c \
  "INSERT INTO projects (id, name, owner) VALUES ('00000000-0000-0000-0000-000000000001', 'Demo Project', 'demo-owner');"
```

### 4. Run the Wormhole Server

Build and run `wormhole-server`. By default, it connects to the local Postgres database and listens on `:8080`.

```bash
go run cmd/wormhole-server/main.go
```

### 5. Join the Project with the CLI

Use `wormhole-cli` to introduce your agent, generate a passport, request permissions, and retrieve project context:

```bash
go run cmd/wormhole-cli/main.go join \
  --server http://localhost:8080 \
  --project 00000000-0000-0000-0000-000000000001 \
  --owner "demo-owner" \
  --model "claude-3-5-sonnet" \
  --capabilities "code_edit,run_tests" \
  --repositories "github.com/H4RL33/wormhole" \
  --roles "developer" \
  --permissions "task.create,kb.write"
```

Upon success, the CLI will output your credentials and persist them to `~/.wormhole/credentials.json`.

---

## Design Documents

- [RFC-0001: Wormhole Core](docs/rfcs/wormhole_rfc.md)
- [RFC-0002: Wormhole Governance](docs/rfcs/wormhole_rfc_governance.md)

---

## Stack

- **Backend**: Go (Standard Library `net/http`)
- **Database**: PostgreSQL (v16) + `pgvector`
- **Interface**: Model Context Protocol (MCP)

---

## License

See [LICENSE](LICENSE).
