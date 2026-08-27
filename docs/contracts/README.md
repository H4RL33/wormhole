# Alpha interface inventory

`alpha-contract.json` is the reviewed inventory of Wormhole's externally
observable alpha interfaces. It records the current MCP descriptors, CLI
surface, environment and path conventions, Gateway local protocol, sync wire
protocol, migrations, and release artifacts.

The CLI inventory includes the local-only `trial-metrics validate` and
`trial-metrics format` commands. They accept participant previews, submitted
participant exports, or aggregates from a file or stdin; they do not contact
Gateway or Fabric.

`designed_interfaces` is separate from those live inventories. It records
decision-complete public contracts that have human design review and may be
implemented in independently inventoried stages. `integration_manifest_v1` is
currently `fabric_distribution_materialization_guidance_and_gateway_cache_binding_implemented`:
its exact
manifest/entry constraints, managed markers, CLI names, and full closed
read-only MCP request/response schemas are fixed by
[`integration-manifest-design.md`](../architecture/integration-manifest-design.md).
Its six repository-materialisation commands appear under `cli.commands`, and
its read-only `wormhole.agent.get_guidance` contract appears in the live Gateway
registry. The authoritative SQLite cache/provider binding is live; an unbound
provider still fails closed.
The design requires descriptor-relative, no-follow repository operations and
immediate journaled removal of unchanged managed bytes after revocation; drift
is preserved and reported as `removal_required`.

`mcp_tools` keeps the Fabric and Gateway registries separate because they are
different externally observable surfaces. Fabric descriptors include their
authentication and permission requirements; Gateway descriptors include every
local permission gate, while same-user socket access remains part of the local
protocol boundary. Request and successful-response schemas are derived from
named examples on the canonical registry descriptors. Gateway's
`wormhole.agent.register` contract is presence-only: its closed request accepts
only the local presence identity and optional capability declaration, never
ownership, model, role, permission, repository, Passport, or credential fields.
Its response reports the trusted local presence record. Variant responses such as `wormhole.kb.get`
remain inventoried explicitly rather than flattened into synthetic shapes.

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

## Gateway tool-guidance inventory

The live Gateway registry currently has 27 agent-facing tools and every one
has exactly one structured guidance record. A record carries purpose, use and
misuse boundaries, mutation behaviour, descriptor-derived permissions and
minimal request example, prerequisites, freshness and source-access
implications, and the recommended follow-up. Permission lists and examples
come from the live descriptor: examples are synthesized from
`buildInputSchema`, then validated against that schema rather than maintaining
another parameter inventory.

The inventory covers only live descriptors. Metadata-only Git commit pointers,
semantic KB search, and local-first task status transitions are live Gateway
tools with explicit permission, freshness, and no-fallback guidance. Integration
guidance has one live read-only record. `wormhole.agent.get_guidance` accepts
only the bound project UUID, reads the cached approved state once, exposes
applicable role-filtered content without repository targets or merge policy,
and reports a newer unapproved version separately. Offline reads may continue
to return compatible, non-revoked approved guidance; revoked or incompatible
guidance is withheld. The call performs no refresh, approval, rendering,
materialisation, filesystem, audit, or persistence mutation.

The five `wormhole.workspace.{status,diff,import,checkpoint,stash}` tools expose
the same Gateway project-operation layer as the five top-level CLI commands.
Gateway resolves workspace binding and actor attribution; public clients cannot
supply either. Diff returns the deterministic semantic review digest. A
`public_git` checkpoint must acknowledge that exact digest and rechecks it before
materialisation. All results are operation readbacks, and checkpoint never
stages, commits, or pushes Git state.

Fabric's version-1 `wormhole.sync.bootstrap` response has a fixed six-field
outer shape and a strict nested `org_config` snapshot. The nested snapshot
contains project, authenticated identity and authorization, Channels, Events,
Tasks, KB articles, and the applicable Fabric integration-manifest offer or
revocation. When the project has no applicable manifest, integration-manifest
metadata is JSON `null`. Manifest bodies remain declarative data: Fabric
preserves immutable version history and digest strings, while Gateway owns
verification, approval, caching, audit, and repository application. Publication
and revocation require the explicit `integration_manifest.publish` and
`integration_manifest.revoke` permissions respectively; they are not exposed
as model-facing MCP authoring tools.
The top-level
Task and KB lists mirror the nested lists exactly; `project_list` is a non-null
empty array in version 1.

`wormhole.sync.incremental_pull` carries later offers and revocations inside
the existing `{type,data}` update envelope with type `integration_manifest`.
The `integration_manifest_change` wire record binds every change to project,
manifest ID, version, full digest, operation, and timestamp. Offered changes
contain the exact manifest body; revocations use a null body.

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
