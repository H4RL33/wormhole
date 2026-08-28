# Alpha interface inventory

[`alpha-contract.json`](alpha-contract.json) is the reviewed inventory of
Wormhole's externally observable alpha interfaces. It records MCP descriptors,
CLI commands, environment/path conventions, local socket protocol, optional
Fabric sync protocol, database migrations, and release artifacts.

The inventory is in `alpha-inventory` mode. Reviewed interface changes update
the file in the same change so drift is visible; this is not a beta
compatibility promise.

## Live MCP inventories

`mcp_tools.gateway`, `mcp_tools.fabric`, and
`mcp_tools.public_fabric_contract` are separate projections.

- Gateway has exactly 17 live agent-facing descriptors: local presence,
  portable Channel/KB, clone-local operational activity, truthful offline sync
  status, and portable workspace operations.
- Optional Fabric has exactly 16 live private HTTP descriptors backed by
  PostgreSQL. It is not a direct Stage 2 harness endpoint.
- The Fabric public contract has exactly ten descriptor-only sync-v2 and
  Activity-v1 values. They contain no handler and are not callable in Slice 1.

The optional server projections are not additive to Gateway. Server enrolment,
whoami, semantic KB search, task mutation, Git-link mutation, remote bootstrap,
and remote sync are not in the Stage 2 Gateway inventory. A private descriptor
is live only in `mcp_tools.fabric`; a public contract value is non-callable.

Gateway `agent.register` is presence-only. Its public request identifies the
target agent and optional local capabilities; it cannot supply owner, model,
role, permission, repository, credential, actor, session, assurance, or
workspace authority. Gateway derives the action actor and exact workspace from
machine-private state.

The five `wormhole.workspace.{status,diff,import,checkpoint,stash}` descriptors
share the projectstate operation layer with top-level CLI commands. Diff returns
the exact publication-review digest. A `public_git` checkpoint requires that
current digest; checkpoint never stages, commits, or pushes Git.

`wormhole.sync.status` is a local-only state read, not a Fabric probe. It
reports `offline` and zero pending writes without network access.

## Generated tool guidance

The live Gateway registry has one structured guidance record for each of its
17 descriptors. Names, permissions, examples, and request/response schema
snapshots derive from the registry rather than a second tool allowlist.

The generated `wormhole-tool-use` skill is checked against the exact 17 names
and the generated manifest binds its exact bytes with `content_digest`. It
contains no section for a server-only or removed Gateway tool. Other generated
skills describe orientation, the local operating loop, contribution, and
review without claiming remote coordination.

## Designed and retained interfaces

`designed_interfaces` is separate from live MCP inventories. It records
reviewed contracts that may be implemented or retained in non-Stage-2 layers.
The integration-manifest cache, private CLI materialisation commands, and
optional Fabric distribution structures remain inventoried, while the planned
model-facing guidance-read descriptor is not in the Stage 2 Gateway inventory.

Integration manifests are declarative Markdown data. Digests bind exact
content, manifest bytes, and the running Gateway tool contract. Human preview,
confirmed digest, no-follow filesystem operations, journalled recovery, and
drift preservation remain required. Their presence in `designed_interfaces`
does not make a tool live.

Public sync-v2 and Activity-v1 records retain strict closed descriptor shapes,
proof beside arguments, and safe `{code,operation}` failure values. They remain
non-callable until production assembly. Optional Fabric semantic search retains
generation-scoped ranking metadata and a structured degraded error with no
lexical fallback. These are server contracts, not Stage 2 Gateway claims.

## CLI and private protocol

The public CLI inventory includes canonical `setup`, top-level portable
workspace commands, connector lifecycle, optional integration commands, and
local-only trial-metrics validation/formatting. Removed initialisation, join,
combined connection, and join-shaped registration commands have no aliases.

Same-user setup/workspace control uses private socket methods absent from
`tools/list`. The stdio bridge rejects them and never forwards the private CLI
capability. The private protocol binds exact confirmed-plan predicates; it is
not model-facing.

## Maintaining the file

Keep arrays sorted and preserve top-level key order for deterministic review.
Contract tests only read the inventory. The stability script runs the focused
packages twice, verifies the file hash is unchanged, and compares normalised
test output:

```bash
.github/scripts/check-contract-manifest.sh
```

An intentionally experimental entry must retain explicit experimental
stability metadata. Moving a name between `designed_interfaces`, Fabric, and
Gateway requires implementation, documentation, generated-guidance, process,
and contract tests in the same reviewed change.
