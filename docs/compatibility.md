# Compatibility Policy

## Current policy: `alpha-inventory`

This repository is in `alpha-inventory` mode. The machine-readable inventory at
[`docs/contracts/alpha-contract.json`](contracts/alpha-contract.json) records
the reviewed public surface: MCP and CLI contracts, environment and path
conventions, Gateway local protocol, Fabric sync protocol, migrations, and
release artifacts.

Reviewed alpha interface changes update that manifest in the same change. The
inventory makes drift visible; it does not make alpha interfaces backwards
compatible. This repository state makes **no beta compatibility promise**. It is
closed pre-alpha software: private Gateway SQLite state has no
backward-compatibility promise between format epochs.

## Private Gateway database format

The current private Gateway format is schema-v8. This is a complete format epoch,
not an upgrade path from any earlier private schema. A missing or genuinely empty
private database is initialized atomically as v8. An exact current v8 database
reopens without schema mutation. Every other existing private database—including
former exact v7, older, future, malformed, partial, unexpected, or
proof-incompatible state—is classified read-only and refused before mutation.

Schema v8 adds only complete-route, finite-policy Activity ledger, receipt, queue,
cursor, lifecycle, and future promotion-receipt evidence to the consolidated private
snapshot. Presence remains memory-only. Activity state is neither a compatibility
reader for v7 nor portable tracked ProjectState.

Wormhole does not migrate, normalize, export, reset, quarantine, rename, or delete
an unsupported private database. The refusal leaves the database and its evidence
under operator control. Before any deliberate manual removal, stop Gateway, inspect
for unpublished overlays, stashes, and pending checkpoints, and make a backup. The
current binary has no reset or export command; removal is an explicit operator
action followed by a fresh setup.

This private-format rule does not apply to the portable tracked project state under
`.wormhole/state/v1/`. That Git format remains the supported interchange and
clean-clone reconstruction format. The dormant W11
`legacy_integration_state_migrations`/`LegacyIntegrationBackupRoot` seam is retained
but is not a private-format compatibility mechanism and is not implemented by R06.

## CLI and local MCP cutover

The canonical public setup command is `wormhole setup`. The former project
initialisation, enrol-and-join, and combined harness-connection commands were
removed as a hard alpha cut and have no aliases or argument-shape compatibility.
Agent registration is presence-only; Gateway derives binding and action
attribution from trusted machine-private setup state rather than accepting the
former ownership, model, role, permission, repository, Passport, or credential
payload.

The top-level `status`, `diff`, `import`, `checkpoint`, and `stash` commands and
their `wormhole.workspace.*` MCP counterparts share one Gateway operation layer.
For `public_git`, checkpoint requires the exact current publication-review digest.
Wormhole writes the portable candidate but never stages, commits, or pushes it.

The live Gateway is a closed local-only 17-tool registry. The optional 20-tool Fabric
registry remains a separate PostgreSQL-backed server surface and is not attached
to the Stage 2 Gateway. Server-only enrolment, identity lookup, semantic search,
Task, Git-link, bootstrap, and remote sync names have no Gateway compatibility
aliases. `wormhole.sync.status` is a truthful offline/zero-pending local read.

Git acceptance is the only transition that makes a checkpointed portable tree
the accepted base or transports it to another clone. A clean clone reconstructs
portable state from `.wormhole/` but creates fresh machine-private workspace,
identity, agent, session, overlay, journal, receipt, credential, operational,
legacy, and sync state.

## Future beta activation

A maintainer must take a later, explicit `v0.3.0-beta.1` action to record and
activate the beta baseline. Only then do compatibility checks become
backward-compatibility enforcement. That action, beta tag, and beta release are
outside the scope of the current work.

## Names and retained paths

The executable names are `wormhole`, `gatewayd`, and `fabric`. The old daemon
and server executable names have no compatibility aliases.

The local paths intentionally retain `wormholed.sock` and `wormholed.db`:

- `$XDG_RUNTIME_DIR/wormhole/wormholed.sock`, with the documented `$TMPDIR`
  fallback;
- `$XDG_DATA_HOME/wormhole/wormholed.db`, with the documented XDG fallback.

Those basenames identify persisted/runtime data and are not commands, binaries,
symlinks, or aliases for `gatewayd`.
