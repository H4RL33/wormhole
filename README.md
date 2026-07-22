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

Wormhole currently builds three binaries: `wormhole` for project setup and harness connection, `wormholed` for the local SQLite-backed MCP runtime and sync queue, and `wormhole-server` for the Postgres-backed coordination server. The server exposes MCP tools for identity, tasks, channels, knowledge base, git pointers, and runtime synchronization; the local runtime supports local writes, scheduling, multi-organization routing, and incremental synchronization.

`wormholed` is currently supported on Linux only. Windows users should run it
inside WSL. Non-Linux builds fail immediately with an actionable error rather
than starting without safe stale-socket recovery.

The read-only dashboard exposes project-scoped task, event, and knowledge-base views. Viewer-key issuance is protected by the `WORMHOLE_ADMIN_KEY` shared-secret admin stopgap; it is not a full human authentication system.

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

#### Per-tool permission enforcement

Every authenticated MCP tool enforces one fine-grained permission at
dispatch. A Passport whose permission bundle lacks the required string gets
JSON-RPC error `-32002` (permission denied) and the denial is recorded in the
audit trail. The required permission is the tool name minus the `wormhole.`
prefix:

| Tool | Required permission |
|------|---------------------|
| `wormhole.task.list` | `task.list` |
| `wormhole.task.create` | `task.create` |
| `wormhole.task.assign` | `task.assign` |
| `wormhole.task.update_status` | `task.update_status` |
| `wormhole.kb.search` | `kb.search` |
| `wormhole.kb.get` | `kb.get` |
| `wormhole.kb.get_links` | `kb.get_links` |
| `wormhole.kb.write` | `kb.write` |
| `wormhole.channel.list` | `channel.list` |
| `wormhole.channel.subscribe` | `channel.subscribe` |
| `wormhole.channel.create` | `channel.create` |
| `wormhole.channel.post` | `channel.post` |
| `wormhole.git.link_commit` | `git.link_commit` |
| `wormhole.git.request_review` | `git.request_review` |

`wormhole.agent.whoami` and the four `wormhole.sync.*` tools are auth-only:
they require a valid token but no permission. Self-identification cannot be
gated without circularity, and sync moves an agent's own data rather than
granting a capability. `wormhole.agent.register` is the pre-token bootstrap
and requires neither.

A registry invariant test fails the build if any tool it sees declares
`RequiresAuth: true` without a permission and is not on that exempt list.
(The test iterates a hand-maintained registry; a gated tool must be wired
into that list to be checked, so keep the test registry in sync with
`cmd/wormhole-server/main.go`.)

**Alpha hard-cut:** migration `000014` re-seeds the role templates with these
fine-grained strings. Agents registered before it hold the older coarse
bundles (`task.read`, `channel.write`, ...), which no tool matches, and must
re-register or re-join to obtain a working Passport.

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

Set this before starting `wormhole-server` — it's read once at
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

## Quickstart

This local demo starts the Postgres-backed Coordination Server, creates one
credential profile, runs the local SQLite-backed daemon, and connects Claude
Code or OpenCode through MCP.

`wormholed` currently requires Linux. On Windows, clone the repository and run
the build, daemon, and harness connector inside WSL. Native Windows and macOS
daemon execution is not supported yet.

### Prerequisites

- Linux, or WSL on Windows
- Go 1.26.4 or newer
- Docker with Docker Compose
- Claude Code or OpenCode if you want to connect a harness

### 1. Build the binaries

```bash
make build
```

This writes `wormhole`, `wormholed`, and `wormhole-server` to `dist/`. The
directory is gitignored; `make clean` removes it.

### 2. Start Postgres and apply migrations

```bash
docker compose up -d db
go install -tags postgres github.com/golang-migrate/migrate/v4/cmd/migrate@latest
migrate \
  -path migrations \
  -database "postgres://wormhole:wormhole@localhost:5432/wormhole?sslmode=disable" \
  up
```

The development database listens on `127.0.0.1:5432` with database, username,
and password all set to `wormhole`.

Create an idempotent demo project:

```bash
export WORMHOLE_PROJECT_ID=00000000-0000-0000-0000-000000000001
docker compose exec -T db psql -U wormhole -d wormhole -v ON_ERROR_STOP=1 -c \
  "INSERT INTO projects (id, name, owner)
   VALUES ('$WORMHOLE_PROJECT_ID', 'Demo Project', 'demo-owner')
   ON CONFLICT (id) DO NOTHING;"
```

### 3. Start the Coordination Server

In a separate terminal from the repository root:

```bash
export WORMHOLE_DATABASE_URL="postgres://wormhole:wormhole@localhost:5432/wormhole?sslmode=disable"
./dist/wormhole-server
```

The server listens on `http://localhost:8080` by default. Set
`WORMHOLE_LISTEN_ADDR` to change the address.

### 4. Create credentials and connect a harness

`wormhole connect` registers the agent, writes a credential profile, and wires
one harness. Run it while `wormhole-server` is available; the daemon does not
need to be running yet.

For Claude Code:

```bash
export WORMHOLE_PROJECT_ID=00000000-0000-0000-0000-000000000001
./dist/wormhole connect \
  --server http://localhost:8080 \
  --project "$WORMHOLE_PROJECT_ID" \
  --owner "${USER:-demo-user}" \
  --model local-agent \
  --permissions task.list,task.create,task.assign,task.update_status,kb.search,kb.get,kb.get_links,kb.write,channel.list,channel.subscribe,channel.create,channel.post,git.link_commit,git.request_review \
  --profile demo \
  --target claude \
  --stdio-bin "$(pwd)/dist/wormhole"
```

For OpenCode, use `--target opencode` instead. You can also add
`--opencode-config /path/to/opencode.json` when auto-detection cannot find its
configuration.

If you only want to create credentials, use `wormhole join` instead of
`connect`; see `./dist/wormhole join --help`. Connector-specific setup and
manual wiring are documented in the
[Claude Code connector guide](docs/claude-code-connector.md).

Credentials are stored in `~/.wormhole/credentials/demo.json`. This file
contains a bearer token: do not commit, share, or loosen its permissions.

### 5. Start the local daemon

In another terminal, pass the exact credential profile name created above:

```bash
./dist/wormholed demo
```

The daemon owns the local SQLite replica and durable sync queue. Harnesses do
not connect directly to `wormhole-server`; they start `wormhole mcp`, which
bridges MCP over stdio to the daemon's Unix socket:

```text
Claude Code or OpenCode -> wormhole mcp -> wormholed -> wormhole-server
```

The socket is `$XDG_RUNTIME_DIR/wormhole/wormholed.sock`. When
`XDG_RUNTIME_DIR` is unset, it falls back beneath
`$TMPDIR/wormhole-runtime/`.

### 6. Verify the setup

```bash
./dist/wormhole profile list
./dist/wormhole whoami --profile demo
```

Confirm the harness lists a `wormhole` MCP connector, then ask it to list the
available Wormhole tools and call `wormhole.task.list` for the demo project.
The request should travel through the daemon and return project-scoped data.

## CLI Usage

Run `./dist/wormhole help` for the current command list. For command flags,
replace the command name as needed; for example:

```bash
./dist/wormhole connect --help
```

| Command | Purpose |
|---|---|
| `wormhole init` | Interactive global and project configuration |
| `wormhole join` | Register an agent and write a credential profile |
| `wormhole connect` | Register, save credentials, and wire a harness |
| `wormhole whoami` | Inspect the identity associated with a profile |
| `wormhole profile list` | List stored credential profiles |
| `wormhole viewer-key create` | Issue a project-scoped dashboard viewer key |
| `wormhole mcp` | Run the harness stdio-to-daemon bridge |
| `wormholed <profile>` | Run the local daemon for a credential profile |

`wormhole mcp` is normally launched by the harness connector. Run
`wormholed <profile>` yourself before opening the harness.

### Configuration and data locations

- Global CLI config: `$XDG_CONFIG_HOME/wormhole/config.toml`, defaulting to
  `~/.config/wormhole/config.toml`
- Project config: `.wormhole/config.toml`, discovered by walking upward from
  the current directory
- Credential profiles: `~/.wormhole/credentials/<profile>.json`
- Local runtime database: `$XDG_DATA_HOME/wormhole/wormholed.db`, defaulting to
  `~/.local/share/wormhole/wormholed.db`
- Daemon socket: `$XDG_RUNTIME_DIR/wormhole/wormholed.sock`, with the
  `$TMPDIR/wormhole-runtime/` fallback described above

Never commit credential profiles or the local runtime database.

### Configuration precedence

For CLI setup commands, values resolve in this order:

```text
explicit flag > project config > global config > environment or Git > default > error
```

Common examples:

- `--server`: explicit flag, then project/global config
- `--project`: explicit flag, then project config
- `--owner`: explicit flag, then `git config user.name`, then `$USER`
- `--repositories`: explicit flag, then `git remote get-url origin`
- `--model`: explicit flag, then `$WORMHOLE_MODEL`

`wormholed` reads `~/.wormhole/credentials/<profile>.json` for its server,
project, agent, and token settings. Its positional profile is not a TOML config
name.

### Troubleshooting

- **`wormhole mcp: dial wormholed socket ...`:** start `wormholed` with the
  correct profile and ensure the harness shares its `XDG_RUNTIME_DIR`.
- **`wormholed` cannot find credentials:** run `wormhole profile list` and
  verify `~/.wormhole/credentials/<profile>.json` exists.
- **The harness has no `wormhole` connector:** rerun `wormhole connect` with an
  explicit `--target`, or follow the connector guide for manual wiring.
- **Tool calls do not sync:** verify `wormhole-server` is running and that the
  credential profile contains the reachable server URL.
- **A second daemon will not start:** only one daemon may own a socket. Stop the
  existing process; `wormholed` deliberately refuses to replace a live socket.

---

## Design Documents

- [RFC-0001: Wormhole Core](docs/rfcs/wormhole_rfc.md)
- [RFC-0002: Wormhole Governance](docs/rfcs/wormhole_rfc_governance.md)
- [RFC-0003: Local Runtime](docs/rfcs/wormhole_rfc_local_runtime.md)

---

## Stack

- **Backend**: Go (Standard Library `net/http`)
- **Database**: PostgreSQL (v16) + `pgvector`
- **Interface**: Model Context Protocol (MCP)

---

## License

See [LICENSE](LICENSE).
