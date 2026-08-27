# Wormhole Wiki

Wormhole's local-only Stage 2 release gives coding agents and humans one
Git-portable project-state format with a private local runtime. The release is
deliberately smaller than the broader architecture described by the RFCs.

## Start here

- [Project README](https://github.com/H4RL33/wormhole#readme) — current scope,
  architecture, status, and quickstart
- [CLI Guide](CLI-Guide) — commands, local paths, and connection pattern
- [Security Model](Security-Model) — deployment boundary and human control
- [Alpha validation](https://github.com/H4RL33/wormhole/blob/main/docs/testing/alpha-validation.md)
- [Release policy](https://github.com/H4RL33/wormhole/blob/main/docs/releasing.md)
- [Compatibility policy](https://github.com/H4RL33/wormhole/blob/main/docs/compatibility.md)

## The Stage 2 system in one minute

```text
MCP harness -> wormhole mcp -> gatewayd -> private SQLite
                                      |
Git checkout <- reviewed checkpoint --+
```

Every supported machine runs an owner-private `gatewayd`. Harnesses use the
real `wormhole mcp` stdio bridge and its exact 17-tool Gateway inventory. The
portable `.wormhole/state/v1/` tree carries selected project records between
clones. A checkpoint only materialises a reviewed candidate; ordinary Git
add/commit/push accepts and transports it. Git is the sole acceptance authority.

Operational activity, presence, overlays, stashes, receipts, workspace
bindings, selected human/agent/session identity, and credentials stay in
machine-private state. They do not become portable merely because a portable
Channel, KB record, or actor record refers to the same subject.

Fabric is optional. Its PostgreSQL-backed 20-tool server surface is retained
for separate non-Stage 2 testing; Gateway setup, normal local work, acceptance,
restart, and clone equivalence do not require or contact it. Live Gateway tools
do not expose Task, Git-link, semantic KB search, enrolment, or managed-guidance
operations.

## Documentation authority

This Wiki is a user-facing navigation layer. Repository files are canonical:

- [README.md](https://github.com/H4RL33/wormhole/blob/main/README.md)
- [SECURITY.md](https://github.com/H4RL33/wormhole/blob/main/SECURITY.md)
- [Implementation rules](https://github.com/H4RL33/wormhole/blob/main/docs/implementation-rules.md)
- [MCP protocol](https://github.com/H4RL33/wormhole/blob/main/docs/mcp-protocol.md)

When Wiki text and a repository file disagree, follow the repository. The
current interface policy is `alpha-inventory`, not a beta promise.
