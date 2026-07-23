# Alpha interface inventory

`alpha-contract.json` is the reviewed inventory of Wormhole's externally
observable alpha interfaces. It records the current MCP descriptors, CLI
surface, environment and path conventions, Gateway local protocol, sync wire
protocol, migrations, and release artifacts.

`mcp_tools` keeps the Fabric and Gateway registries separate because they are
different externally observable surfaces. Fabric descriptors include their
authentication and permission requirements; Gateway descriptors include every
local permission gate, while same-user socket access remains part of the local
protocol boundary. Request and successful-response schemas are derived from
named examples on the canonical registry descriptors. In particular, Gateway's
dual-shape `wormhole.agent.register` requests and responses, and
`wormhole.kb.get` responses, are inventoried as explicit variants rather than
flattened into synthetic shapes.

The inventory is intentionally in `alpha-inventory` mode. It makes drift
visible during review, but it does not activate a beta compatibility promise
or a general stability guarantee. A reviewed alpha addition or change updates
the manifest in the same change. Entries that are deliberately less settled
must include `"stability": "experimental"`.

Keep arrays sorted and preserve the existing top-level key order so diffs stay
deterministic. The contract tests only read this file; they never generate or
rewrite it. `.github/scripts/check-contract-manifest.sh` runs the focused
packages twice, verifies that the file's hash does not change, and compares
normalized test output between runs.
