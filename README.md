![Wormhole wordmark](brand/wordmark_bws_ow.jpg)

# Wormhole

**Shared, durable organisational context for every agent, harness, model, and
human on your team.**

Wormhole is open-source coordination infrastructure for agentic work. It gives
different coding harnesses and models one shared event stream, task graph,
knowledge base, identity system, and set of Git pointers. Git remains the
source of truth for code; Wormhole preserves the organisational context around
the code.

Wormhole is not another agent, model, or orchestration framework. It is the
common layer beneath them.

## Mission

Wormhole exists to make agentic harnesses and models from any provider work as
one.

Its primary mission is **total interoperability**: a task started in one
harness, continued by another model, and reviewed by a third should retain the
same identity, history, decisions, and organisational memory.

Its second mission is to bridge the gap between humans and agents while
preserving human control. Agents can coordinate and act through explicit
permissions, durable audit trails, and project-scoped identities; humans retain
authority over destructive actions, policy, credentials, and deployment.

Its third mission is exploratory: to push the boundaries of what agents can do
together and venture into new ground with innovative, experimental systems.
Wormhole is a practical tool today and a platform for discovering better forms
of human-agent and agent-agent collaboration tomorrow.

## What Wormhole provides

- **Shared organisational memory** — atomic, linked knowledge articles available
  across sessions, models, and machines.
- **Structured coordination** — typed events, channels, tasks, dependencies,
  ownership, and status transitions.
- **Portable agent identity** — project-scoped Passports, explicit permissions,
  roles, and append-only audit records.
- **Local-first operation** — a per-user daemon with a SQLite replica and
  durable outbound sync queue.
- **Multi-device collaboration** — a Coordination Server that reconciles
  runtimes through project-scoped PostgreSQL state.
- **Provider neutrality** — interoperability is defined by open protocols and
  harness capabilities, not by the model vendor.
- **Git-native boundaries** — Wormhole stores commit SHAs, PR URLs, and
  commentary; it never copies or replaces repository code.

The platform contract is the
[Model Context Protocol](https://modelcontextprotocol.io/). Any compatible
harness can use Wormhole directly or through a community connector.

## Architecture

```text
Claude Code, OpenCode, or another MCP harness
                     |
              wormhole mcp
           stdio-to-socket bridge
                     |
                 gatewayd
       local MCP API + SQLite replica
          durable local sync queue
                     |
          wormhole.sync.* over HTTP(S)
                     |
                fabric
       coordination + PostgreSQL/pgvector
```

Harnesses talk only to the local `gatewayd` daemon. The Gateway makes local
writes durable before attempting network synchronization. The Coordination
Server provides the authority required across users and machines: enrollment,
project-scoped identity, shared discovery, conflict authority, and durable
multi-runtime coordination.

Wormhole builds three binaries:

| Binary | Role |
|---|---|
| `wormhole` | Setup, profiles, harness connection, and MCP stdio bridge |
| `gatewayd` | Gateway: per-user local runtime, SQLite replica, local MCP API, and sync queue |
| `fabric` | Fabric: Coordination Server backed by PostgreSQL and pgvector |

## Status

Wormhole is alpha software under active development.

- `gatewayd` is currently supported on Linux. Windows users should use WSL.
- Claude Code and OpenCode have first-party connection flows.
- Other MCP-capable harnesses can use `wormhole mcp` or community connectors.
- The current production embedder is a non-semantic development stub.
- First-time enrollment and the current daemon startup bootstrap require a
  reachable Coordination Server.
- True serverless initialization and startup from an offline replica are
  tracked in [issue #37](https://github.com/H4RL33/wormhole/issues/37).

Interfaces may change before a stable release. Do not expose an alpha
Coordination Server directly to the public internet without reviewing the
[security policy](SECURITY.md).

The current compatibility mode is `alpha-inventory`: reviewed interface changes
update the checked-in inventory, but this alpha state makes no beta compatibility
promise. See [Compatibility policy](docs/compatibility.md).

## Get started

Choose the path that matches how you want to use Wormhole.

### Single device and offline-capable operation

Use this path when one machine and its local agents need to share an existing
Wormhole project. Once enrolled and bootstrapped, harness calls go through
`gatewayd`, local state lives in SQLite, and local writes enter a
restart-surviving queue.

> **Current limitation:** first-time enrollment and the startup bootstrap still
> require a reachable Coordination Server. A fresh, permanently serverless
> namespace cannot be created yet. Issue
> [#37](https://github.com/H4RL33/wormhole/issues/37) tracks that gap.

Prerequisites:

- Go 1.24 or newer
- Linux, or Windows through WSL
- an existing credential profile created with `wormhole join` or
  `wormhole connect`

Build the CLI and daemon:

```bash
git clone https://github.com/H4RL33/wormhole.git
cd wormhole
make build
```

Inspect available profiles and start the local runtime:

```bash
./dist/wormhole profile list
./dist/gatewayd demo
```

In another terminal, verify the profile:

```bash
./dist/wormhole whoami --profile demo
```

Connect an MCP harness to the local bridge. For Claude Code:

```bash
./dist/wormhole connect \
  --server https://your-coordination-server.example \
  --project YOUR_PROJECT_UUID \
  --owner "${USER:-local-user}" \
  --model your-model \
  --permissions task.list,task.create,task.update_status,kb.search,kb.get,kb.write,channel.list,channel.subscribe,channel.post \
  --profile demo \
  --target claude \
  --stdio-bin "$(pwd)/dist/wormhole"
```

Use `--target opencode` for OpenCode. The harness launches `wormhole mcp`,
which bridges stdio to the daemon socket; it does not call the Coordination
Server directly.

After the daemon has bootstrapped, its SQLite replica and durable queue are the
local source for work. Network interruptions do not make queued data
disappear. Current startup still requires the bootstrap endpoint, so restart
the daemon while the Coordination Server is reachable until #37 is resolved.

Local paths:

- Credentials: `~/.wormhole/credentials/<profile>.json`
- SQLite replica: `$XDG_DATA_HOME/wormhole/wormholed.db`, or
  `~/.local/share/wormhole/wormholed.db`
- Daemon socket: `$XDG_RUNTIME_DIR/wormhole/wormholed.sock`, or the
  `$TMPDIR/wormhole-runtime/` fallback

The retained `wormholed.sock` and `wormholed.db` basenames identify on-disk
runtime state from before the hard executable rename. They are data-path
compatibility names only: neither is a command, binary, symlink, or alias for
`gatewayd`.

Credential profiles contain bearer tokens. Never commit or share them.

### Multi-device and team coordination

Use this path when multiple machines, people, or runtimes need to share the
same project state. Each machine still talks to its own `gatewayd`; Fabric is
the authenticated meeting point between them.

Prerequisites:

- Go 1.24 or newer
- Docker with Compose
- [`golang-migrate`](https://github.com/golang-migrate/migrate)
- Linux or WSL for every machine running `gatewayd`

#### 1. Build Wormhole

```bash
git clone https://github.com/H4RL33/wormhole.git
cd wormhole
make build
```

#### 2. Start PostgreSQL and apply migrations

```bash
docker compose up -d db

migrate \
  -path migrations \
  -database "postgres://wormhole:wormhole@localhost:5432/wormhole?sslmode=disable" \
  up
```

The Compose database listens on `127.0.0.1:5432` with development-only
credentials: database, username, and password are all `wormhole`.

Create a project:

```bash
export WORMHOLE_PROJECT_ID=00000000-0000-0000-0000-000000000001

docker compose exec -T db psql -U wormhole -d wormhole -v ON_ERROR_STOP=1 -c \
  "INSERT INTO projects (id, name, owner)
   VALUES ('$WORMHOLE_PROJECT_ID', 'Demo Project', 'demo-owner')
   ON CONFLICT (id) DO NOTHING;"
```

#### 3. Start the Coordination Server

```bash
export WORMHOLE_DATABASE_URL="postgres://wormhole:wormhole@localhost:5432/wormhole?sslmode=disable"
./dist/fabric
```

The default listener is `http://localhost:8080`. Set
`WORMHOLE_LISTEN_ADDR` to change it. Use HTTPS for any non-loopback
deployment; bearer tokens must never cross an unencrypted network.

The Coordination Server:

- enrolls agents and issues project-scoped credentials;
- stores authoritative shared state in PostgreSQL;
- accepts incremental pushes from local runtimes;
- serves bootstrap and incremental pulls;
- enforces permissions and project isolation at the MCP boundary;
- provides conflict authority and durable audit history across runtimes.

It is not the harness endpoint. Every harness still connects to its local
daemon:

```text
Harness A -> Gateway A --\
                          -> Fabric -> PostgreSQL
Harness B -> Gateway B --/
```

#### 4. Enroll and connect each machine

On each device, while the Coordination Server is reachable:

```bash
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

Then start that device's daemon:

```bash
./dist/gatewayd demo
```

Repeat with a distinct owner/model/profile on each machine as appropriate.
Use `--target opencode` for OpenCode.

#### 5. Verify the path

```bash
./dist/wormhole profile list
./dist/wormhole whoami --profile demo
```

Ask the connected harness to list Wormhole tools and call
`wormhole.task.list`. The request should travel through its local daemon; state
written on one runtime becomes available to the others through incremental
synchronization.

## CLI

Run:

```bash
./dist/wormhole help
./dist/wormhole <command> --help
```

| Command | Purpose |
|---|---|
| `wormhole init` | Create project configuration interactively |
| `wormhole join` | Register an agent and write a credential profile |
| `wormhole connect` | Register, save credentials, and wire a harness |
| `wormhole whoami` | Inspect the identity associated with a profile |
| `wormhole profile list` | List stored credential profiles |
| `wormhole viewer-key create` | Issue a project-scoped dashboard viewer key |
| `wormhole mcp` | Run the harness stdio-to-daemon bridge |
| `gatewayd <profile>` | Run the local Gateway for a credential profile |

Configuration precedence for setup commands:

```text
explicit flag > project config > global config > environment or Git > default > error
```

The detailed [CLI Guide](https://github.com/H4RL33/wormhole/wiki/CLI-Guide)
covers flags, profiles, paths, and connection patterns.

## Human control and security

Wormhole treats human authority as an architectural boundary:

- identities and permissions are explicit and project-scoped;
- credentials are hashed server-side and restricted on local disk;
- destructive or policy-changing actions remain human-controlled;
- PostgreSQL RLS and mandatory namespace parameters protect tenant boundaries;
- audit records are append-only;
- Git remains the sole source of truth for code.

Read the canonical [Security Policy](SECURITY.md) before deployment. The Wiki's
[Security Model](https://github.com/H4RL33/wormhole/wiki/Security-Model) is an
approachable guide, not a replacement for the repository policy.

Security vulnerabilities should be reported privately through GitHub Private
Vulnerability Reporting or `security@wormhole.systems`, never through a public
issue.

## Documentation

- [GitHub Wiki](https://github.com/H4RL33/wormhole/wiki)
- [CLI Guide](https://github.com/H4RL33/wormhole/wiki/CLI-Guide)
- [Security Model](https://github.com/H4RL33/wormhole/wiki/Security-Model)
- [Architecture and implementation rules](docs/implementation-rules.md)
- [MCP protocol](docs/mcp-protocol.md)
- [Release policy](docs/releasing.md)
- [Compatibility policy](docs/compatibility.md)
- [Data entities](docs/db-entities.md)
- [RFC-0001: Core](docs/rfcs/wormhole_rfc.md)
- [RFC-0002: Governance](docs/rfcs/wormhole_rfc_governance.md)
- [RFC-0003: Local Runtime](docs/rfcs/wormhole_rfc_local_runtime.md)
- [Contributing](CONTRIBUTING.md)
- [Security Policy](SECURITY.md)

Repository files are canonical. Wiki pages provide a friendlier navigation
layer and link back to their source material.

## Development

```bash
make build
make vet
make test
```

For required PostgreSQL integration coverage:

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./...
```

See [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request.

## License

Wormhole is released under the [MIT License](LICENSE).
