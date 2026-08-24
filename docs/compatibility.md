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

The current private Gateway format is schema-v6. This is a complete format epoch,
not an upgrade path from v1-v5. A missing or genuinely empty private database is
initialized atomically as v6. An exact current v6 database reopens without schema
mutation. Every other existing private database—including an older/future,
malformed, partial, unexpected, or proof-incompatible database—is classified
read-only and refused before mutation.

An exact v6 database may omit Code Graph or contain the complete optional current
Code Graph catalog at schema version 2 with ledger rows 1 and 2. A Code Graph
v1-only, partial, malformed, future, or extra catalog is not a compatibility case
and is refused before mutation. Existing current Code Graph rows remain intact
across Gateway close and reopen; preflight does not rebuild or discard them.

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
