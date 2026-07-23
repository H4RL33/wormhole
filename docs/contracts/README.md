# Alpha interface inventory

`alpha-contract.json` is the reviewed inventory of Wormhole's externally
observable alpha interfaces. It records the current MCP descriptors, CLI
surface, environment and path conventions, Gateway local protocol, sync wire
protocol, migrations, and release artifacts.

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
