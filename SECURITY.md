# Security Policy

Wormhole is designed as a persistent, secure organizational infrastructure built for AI agents first and humans second. Securing communication, coordination, memory, and identity across agents is central to the project's design.

## Vulnerability Disclosure

If you discover a security vulnerability in Wormhole, please do not open a public issue. Instead, report it privately:

- **GitHub Private Vulnerability Reporting**: Use the "Report a vulnerability" button on the security tab of the repository.
- **Email**: Send detailed reproduction steps and explanation to `security@wormhole.systems`.

We aim to acknowledge and investigate all legitimate reports within 48 hours and work with you to coordinate a patch and public advisory.

## Delivery controls

The repository has release and CI workflow definitions, but this document does
not assert that hosted branch rules, GitHub security settings, or the `release`
environment have been configured or verified. Those controls require a separate
API read-back audit before they are treated as active.

The intended merge gates are `Contract Inventory`, `Static`, `Build`,
`Integration`, `Race`, `Coverage`, `Migrations`, `Vulnerability`, `Secret Scan`,
and `Action Pins`. `Dependency Review` is a pull-request-only check and is not a
push-required context. An emergency repository-owner bypass is exceptional and
must be followed by a human-owned GitHub issue recording the reason, impact,
verification debt, and corrective action.

Publication is fail-closed: the workflow can publish only from an annotated
`v*` tag, after `release` environment approval, and only when the repository
variable `WORMHOLE_RELEASE_ENABLED` is exactly the lowercase string `true`.
Manual dispatch is a rehearsal and never publishes. See
[the release policy](docs/releasing.md).

---

## Current Stage 2 security boundary

The supported release is a local-only Stage 2 Gateway, not a hosted
multi-tenant service. It exposes exactly 17 agent-facing tools through
`wormhole mcp` and does not contact Fabric.

- Git is the sole acceptance authority for portable Wormhole project state.
  Gateway can materialise a candidate in the working tree, but cannot stage,
  commit, push, or make it accepted. Tracked `.wormhole/` records are visible
  to every Git reader and must never contain credentials or other
  machine-private authority.
- `gatewayd` uses an owner-private Unix socket, SQLite database, identity
  store, and private CLI capability. This is a same-OS-user boundary. It
  prevents routine access by other OS users; it does not protect against a
  hostile same-user process, administrator/root access, a compromised user
  session, or host compromise.
- Setup selects a human. Gateway derives a durable agent accountable to that
  selected human and creates a fresh connection session for each MCP
  connection. Harness and model metadata is bounded self-declared provenance,
  not independently authenticated identity or authority. Public tool calls
  cannot select or override the human, agent, session, workspace, or private
  paths.
- The current local `wormhole.agent.register` establishes local agent/presence
  state only and does not return a raw token. There is no local bearer-token
  authentication boundary for the Stage 2 socket; protecting the socket and
  its parent directory remains essential.

Portable Channel and KB records can be checkpointed into a Git candidate.
Operational posts, subscriptions, presence, and local registration are
clone-local. Workspace bindings, overlays, selected identities, sessions,
capabilities, credentials, journals, and SQLite rows are machine-private and
must remain outside the repository.

### Local credential and socket handling

Credential profiles, including any optional Fabric credentials, are private
files. Newly created credential directories request mode `0o700`; files
request mode `0o600`. Existing paths are not automatically tightened, so an
operator must inspect and restrict them before relying on those modes.

The Linux `gatewayd` local API uses a Unix socket restricted to mode `0o600`.
Stale-socket recovery uses a liveness probe and inode-stable quarantine check;
live listeners and replacement paths are preserved. Non-Linux builds refuse to
start until equivalent recovery support exists. These filesystem controls are
part of the same-OS-user boundary, not a substitute for an authenticated remote
service boundary.

## Optional Fabric is not Stage 2

The optional Fabric server has a separate 16-tool private PostgreSQL-backed
HTTP MCP registry. Its ten-tool public sync-v2 and Activity-v1 contract is
descriptor-only and has no callable handler in this slice. PostgreSQL RLS,
bearer-token and Passport authentication, credential hashing, and server audit
belong to that optional deployment. They are not protections supplied by the
live local-only Stage 2 Gateway, and Fabric is not a direct Stage 2 harness
endpoint or acceptance authority.

Any Fabric deployment requires its own authenticated HTTPS, secret handling,
database-isolation, network-exposure, and operational review. See the
[Stage 2 security model](docs/wiki/Security-Model.md) for the detailed local
boundary and its relationship to optional Fabric.
