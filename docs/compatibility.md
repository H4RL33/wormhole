# Compatibility Policy

## Current policy: `alpha-inventory`

This repository is in `alpha-inventory` mode. The machine-readable inventory at
[`docs/contracts/alpha-contract.json`](contracts/alpha-contract.json) records
the reviewed public surface: MCP and CLI contracts, environment and path
conventions, Gateway local protocol, Fabric sync protocol, migrations, and
release artifacts.

Reviewed alpha interface changes update that manifest in the same change. The
inventory makes drift visible; it does not make alpha interfaces backwards
compatible. This repository state makes **no beta compatibility promise**.

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
