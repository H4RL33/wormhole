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

To that extent, we officially provide connectors for harnesses that do not lock an agent to a single proprietary model provider by design — currently Claude Code and OpenCode. We will not officially support connectors whose entire purpose is wiring up a specific provider we've chosen not to endorse (e.g. a "Gemini connector" or "OpenAI connector" as such), though nothing stops a harness-level connector like OpenCode's from being pointed at any model the user chooses — that choice is the user's, not Wormhole's to gate.

#### Being Open-Source

Wormhole will always remain open-source, as we believe that all products in the AI-space should be.

Because of that, it is relatively trivial to create third-party connectors to other platforms.

We state that we will never officially support the aforementioned providers, however it would be impossible for us to stop the development of community connectors for these platforms; so simply, if one was made, go ahead and use it.

Furthermore, we reiterate that Wormhole is built on-top of the MCP, which is an open protocol and model-agnostic (all the model needs is the ability to use tools).

All we can do is encourage you to reconsider your provider of choice.

---

## Status

**Local Runtime Alpha (v0.2.4-alpha)**. Core data schemas, Row-Level Security, multi-tenant isolation, and MCP tools for all four pillars are implemented (see [ROADMAP.md](ROADMAP.md)), plus the local-first runtime layer: `wormholed` daemon, SQLite replica, event bus/scheduler, sync engine with offline-write/reconnect, and multi-org bootstrap (see [ROADMAP-LOCAL-RUNTIME.md](ROADMAP-LOCAL-RUNTIME.md)). Offline/reconnect kill-network test suite and a comprehensive cross-repo isolation audit remain deferred to the beta pass — see that roadmap's P6 section for exact scope.

Since v0.2.0-alpha, the dashboard viewer-key issuance endpoint (`POST /dashboard/api/projects/{id}/viewer-keys`) and CLI command (`wormhole-cli viewer-key create`) have been added, gated by the `WORMHOLE_ADMIN_KEY` shared-secret admin auth stopgap — a thin placeholder ahead of real human identity/auth, not a full auth system.

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

## Human Dashboard

`wormhole-server` serves a read-only human dashboard at `/dashboard/`
(RFC-0001 §14 V2, an explicit exception to "every capability is an MCP
tool" — see `internal/webui`'s package doc). It exposes a static page plus
three JSON endpoints, each scoped to one project and gated by a
project-scoped viewer key (`Authorization: Bearer <key>`):

- `GET /dashboard/api/projects/{id}/tasks`
- `GET /dashboard/api/projects/{id}/events`
- `GET /dashboard/api/projects/{id}/kb`

To issue a viewer key, `wormhole-server` needs an admin key configured:

```bash
export WORMHOLE_ADMIN_KEY="choose-a-long-random-secret"
```

Set this before starting `wormhole-server` (step 4 above) — it's read once at
startup. With that set, mint a viewer key:

```bash
wormhole viewer-key create \
  --server http://localhost:8080 \
  --project 00000000-0000-0000-0000-000000000001 \
  --label "harley's laptop"
```

`--admin-key` can be passed explicitly instead of `$WORMHOLE_ADMIN_KEY` if the
CLI is running somewhere that doesn't share the server's environment. The
command prints the raw key once — give it to the human who'll use the
dashboard, as their `Authorization: Bearer <key>` value:

```bash
curl -H "Authorization: Bearer <viewer_key>" \
  http://localhost:8080/dashboard/api/projects/00000000-0000-0000-0000-000000000001/tasks
```

This admin-key gate is a deliberate stopgap, not real human authentication —
there's no per-human identity or audit trail yet (tracked separately).

---

## Quickstart / Local Demo

Follow this guide to spin up a local instance of `wormhole-server` (the Coordination Server), run `wormholed` (the local daemon each agent talks to), and connect a coding harness to it.

### Prerequisites

- Go 1.26.4+
- Docker & Docker Compose
- PostgreSQL client (`psql`) installed locally (optional, for manual queries)
- Claude Code and/or OpenCode installed, if you intend to connect one of those harnesses

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

### 4. Run the Coordination Server

Build and run `wormhole-server`. By default, it connects to the local Postgres database and listens on `:8080`.

```bash
go run ./cmd/wormhole-server
```

### 5. Install the Wormhole CLI

```bash
go install ./cmd/wormhole
```

### Quick Start Options

Choose one approach for agent setup:

#### Option 1: Interactive Setup (Recommended)

Run the interactive setup wizard — perfect for first-time configuration:

```bash
wormhole init
```

Prompts for server URL, project ID, and role selection; configures everything and wires harnesses automatically. This writes configs to:
- Global: `~/.config/wormhole/config.toml`
- Local: `./.wormhole/config.toml`

#### Option 2: Manual Configuration (CI/Scripting)

For scripted setups or CI/CD, create config files directly.

Create `.wormhole/config.toml` in your repository (example at `.wormhole/config.toml`):

```toml
project = "00000000-0000-0000-0000-000000000001"
role = "backend-engineer"
```

Create `~/.config/wormhole/config.toml` globally:

```toml
server = "http://localhost:8080"
```

Then register and wire harnesses:

```bash
wormhole join
wormhole connect
```

### File Locations (XDG-Compliant)

Wormhole respects the XDG Base Directory specification:

- **Global config:** `$XDG_CONFIG_HOME/wormhole/config.toml` (default `~/.config/wormhole/config.toml`)
  - Server URL, default roles and capabilities
  - Shared across projects
- **Local config:** `./.wormhole/config.toml` (walked up from cwd like `.git`)
  - Project ID, role overrides, server overrides
  - Committed to repository; safe for all contributors
- **Credentials:** `$XDG_DATA_HOME/wormhole/credentials` (default `~/.local/share/wormhole/credentials`)
  - API tokens and profiles, **never committed**

### Flags Precedence

All flags are optional when config is properly set. Resolution order for each flag:

```
explicit flag > local config > global config > environment/git > default > error
```

Examples:

- `--server`: global/local config, explicit flag to override
- `--project`: local config required, explicit flag to override
- `--owner`: from `git config user.name`, fallback `$USER`, explicit flag to override
- `--repositories`: from `git remote get-url origin`, empty if no repo, explicit flag to override
- `--model`: from harness self-report (`$WORMHOLE_MODEL`), explicit flag to override

### Running the Daemon and Harnesses

`wormholed` is the local daemon a coding harness talks to over a Unix domain socket. Install once:

```bash
go install ./cmd/wormholed
```

Run in its own terminal; every command below talks to it:

```bash
wormholed
```

It reads configuration from `$XDG_CONFIG_HOME/wormhole/config.toml` by default (see `internal/runtime/config` if you need custom paths).

After setup, interact with your harness — Claude Code or OpenCode — which will communicate through `wormholed` to the Coordination Server. See `wormhole whoami` and `wormhole profile list` to inspect stored credentials.

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
