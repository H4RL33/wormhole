# Wormhole Wiki

Wormhole is shared, durable organisational context for every agent, harness,
model, and human on your team.

Its mission is total interoperability: agentic harnesses and models from any
provider should be able to work as one. Wormhole also bridges humans and agents
while preserving human authority, and provides a foundation for innovative,
experimental multi-agent systems.

## Start here

- [Project README](https://github.com/H4RL33/wormhole#readme) — overview,
  architecture, status, and quickstarts
- [CLI Guide](CLI-Guide) — commands, profiles, paths, and connection patterns
- [Security Model](Security-Model) — deployment boundaries and human control
- [Release policy](https://github.com/H4RL33/wormhole/blob/main/docs/releasing.md)
- [Compatibility policy](https://github.com/H4RL33/wormhole/blob/main/docs/compatibility.md)
- [Contributing](https://github.com/H4RL33/wormhole/blob/main/CONTRIBUTING.md)
- [RFCs](https://github.com/H4RL33/wormhole/tree/main/docs/rfcs)

## The system in one minute

```text
MCP harness -> wormhole mcp -> Gateway -> Fabric
                                  |         |
                               SQLite   PostgreSQL
```

Every machine runs one local `gatewayd` daemon. Harnesses call the Gateway
through `wormhole mcp`; they never call Fabric directly.
Local writes become durable in SQLite before synchronization. Fabric supplies
authenticated, project-scoped authority across people,
machines, and runtimes.

Git remains the source of truth for code. Wormhole stores tasks, events,
knowledge, identities, permissions, and pointers to commits and pull requests.

## Documentation authority

This Wiki is a user-facing navigation layer. Repository files are canonical:

- [README.md](https://github.com/H4RL33/wormhole/blob/main/README.md)
- [SECURITY.md](https://github.com/H4RL33/wormhole/blob/main/SECURITY.md)
- [Implementation rules](https://github.com/H4RL33/wormhole/blob/main/docs/implementation-rules.md)
- [MCP protocol](https://github.com/H4RL33/wormhole/blob/main/docs/mcp-protocol.md)

When Wiki text and a repository file disagree, follow the repository.

The current interface policy is `alpha-inventory`, not a beta promise. Hosted
release-environment and branch-protection configuration must be verified through
repository API read-back before it is described as active.
