# Alpha interface inventory

`alpha-contract.json` is the reviewed inventory of Wormhole's externally
observable alpha interfaces. It records the current MCP descriptors, CLI
surface, environment and path conventions, Gateway local protocol, sync wire
protocol, migrations, and release artifacts.

`designed_interfaces` is separate from those live inventories. It records
decision-complete public contracts that have human design review but no runtime
implementation. `integration_manifest_v1` is currently
`designed_not_implemented`; its exact manifest/entry constraints, managed
markers, CLI names, and full closed read-only MCP request/response schemas are fixed by
[`integration-manifest-design.md`](../architecture/integration-manifest-design.md).
Its commands must not appear under `cli.commands`, and
`wormhole.agent.get_guidance` must not appear under either live MCP registry,
until their implementation changes update both the runtime and inventory.
The design requires descriptor-relative, no-follow repository operations and
immediate journaled removal of unchanged managed bytes after revocation; drift
is preserved and reported as `removal_required`.

`mcp_tools` keeps the Fabric and Gateway registries separate because they are
different externally observable surfaces. Fabric descriptors include their
authentication and permission requirements; Gateway descriptors include every
local permission gate, while same-user socket access remains part of the local
protocol boundary. Request and successful-response schemas are derived from
named examples on the canonical registry descriptors. In particular, Gateway's
dual-shape `wormhole.agent.register` requests and responses, and
`wormhole.kb.get` responses, are inventoried as explicit variants rather than
flattened into synthetic shapes.

Gateway's `wormhole.agent.enrol` entry is the version-1 pre-credential local
contract. Its closed request records the explicit project binding, requested
identity and scope, Fabric address, stable attempt key, and a profile identifier
contained beneath Gateway's credential root. Its response schemas list strict
per-code variants, including the nonterminal credential-persisted stage, with
fixed lifecycle state and retryability. The empty permission list is intentional: same-user
socket protection is the trust boundary before a Passport exists. Result
schemas contain identity references but never raw
credential material. See
[`gateway-enrolment-lifecycle.md`](../architecture/gateway-enrolment-lifecycle.md).

Gateway's read-only `wormhole.sync.status` entry is local runtime state, not a
Fabric probe. Its request contains only `project_id`; its response contains
only `state` and `pending_writes`.

Gateway's experimental Code Graph entries are project-scoped and closed:
`wormhole.code_graph.query` requires `code_graph.query`,
`wormhole.code_graph.status` requires `code_graph.status`, and
`wormhole.code_graph.rebuild` requires `code_graph.rebuild`. Query source
assembly additionally checks `code_graph.source.read` from the same cached
credential scope; its absence returns metadata-only source outcomes rather
than denying graph metadata. Query reports current HEAD and tracked
working-tree state separately from graph freshness. The project-local result
keeps `working_tree_status` to `clean` or `dirty`;
it does not call the working tree `stale`. `graph_not_current` and
`rebuild_recommended` report only an exact
tracked-Go inventory or approved-remote mismatch. A non-Go-only commit or
non-Go dirtiness therefore does not make a matching graph stale. Inspection errors are health
payloads (`error` without an active graph, `degraded` with one), and never
mutate graph state. Rebuild accepts only `project_id` and uses persisted,
approved configuration for a balanced copy-on-write build.

Code Graph tool errors are fail-closed:

- `invalid or unknown input` is rejected by the strict closed decoder;
- `missing primary permission` denies the tool call, while only missing source
  permission degrades a successful query to metadata-only results;
- `missing or ambiguous project scope` and an unavailable runtime are rejected
  before graph access;
- a `disabled rebuild` and a `concurrent rebuild` are rejected; and
- a failed balanced copy-on-write build preserves the active graph and bounded
  diagnostics, so status becomes `degraded` or `error` according to whether an
  active graph remains.

Fabric's version-1 `wormhole.sync.bootstrap` response has a fixed six-field
outer shape and a strict nested `org_config` snapshot. The nested snapshot
contains project, authenticated identity and authorization, Channels, Events,
Tasks, KB articles, and integration-manifest metadata fixed to JSON `null`.
The top-level
Task and KB lists mirror the nested lists exactly; `project_list` is a non-null
empty array in version 1.

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

Fabric's successful `wormhole.kb.search` response includes generation-scoped
ranking metadata (`semantic_applied`, provider, model, version, dimension,
generation, and cosine metric). Cohere/provider or active-index unavailability
is not a successful empty response: it is a structured tool error with
`semantic_ranking=false`, `degraded=true`, `fallback="none"`, and `retryable`.
There is no lexical fallback. `wormhole.kb.write` likewise fails without a
database commit when embedding is unavailable.
