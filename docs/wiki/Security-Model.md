# Security Model

Wormhole Stage 2 is a local-only, same-user Gateway with Git-native portable
state. Its controls separate repository-visible data from operational and
machine-private authority; they do not turn a multi-user host into a hardened
security boundary.

## Trust boundaries

- Git is source truth and the Git acceptance authority for portable Wormhole
  state. Gateway may materialise a reviewed candidate but cannot stage, commit,
  push, or make it accepted.
- `gatewayd` is one user-level daemon. Its Unix socket, SQLite database,
  identity store, and private CLI capability are owner-only.
- `wormhole mcp` is a public-agent stdio bridge. It adds the canonical private
  working directory to `tools/call`, rejects private RPC methods, and never
  forwards the CLI capability.
- Setup and top-level workspace commands are same-user private RPCs. They bind
  the printed confirmed plan to exact repository, identity, publication, and
  import predicates before committing it.
- The Stage 2 Gateway does not contact Fabric. The separately buildable Fabric
  server and PostgreSQL tests are optional non-Stage-2 surfaces.

Owner-only permissions prevent other OS users from routinely opening these
files and sockets. They do not defend against a hostile same-user process,
administrator/root access, kernel compromise, debugger access, stolen user
credentials, or a compromised session. The private CLI capability proves
possession by the same OS user, not physical human presence.

## Portable project state

Portable project state is the canonical typed tree under `.wormhole/`:

- `config.toml` and optional `remotes.toml`;
- project and actor records;
- Tasks and Task links;
- channel definitions;
- KB records and Markdown bodies;
- explicitly portable Event and Git-link records; and
- tombstones and namespaced extensions.

Every Git reader can see this state. Portable records must contain no tokens,
private keys, passwords, private identity details, private worktree paths, or
operational database rows. Secret-shape checks reject known unsafe structures
but are not DLP and cannot decide whether arbitrary prose is confidential.

Tracked actor and Fabric-hint records are statements, not credentials,
membership, permission, or execution authority. A clean clone reconstructs
the same accepted portable digest and records after ordinary Git transfer.

## Operational state

Operational state supports a single clone/session and is not portable:

- `channel.post` activity and `channel.events` history;
- local scheduler registration and presence;
- live subscriptions; and
- disabled/offline sync status.

Operational events validate their target against the composed portable channel
view but are written only to the private local event store. Checkpoint excludes
them, Git never receives them, and a fresh clone begins with none of them.

## Machine-private state

Machine-private state includes:

- selected human profile, private key, and setup receipt;
- durable harness-agent identity and accountable human binding;
- server-generated connection sessions and harness/model provenance;
- private CLI capability;
- workspace IDs, checkout identity, accepted-base bookkeeping, overlays,
  candidates, publication policy, stashes, conflicts, journals, receipts, and
  materialisation proofs;
- schema-v7 SQLite rows and owner lock; and
- connector backups and any optional Fabric credentials.

It remains outside the repository and is intentionally different between two
clones. Private state may survive a daemon restart on the same machine; it must
not appear in checkpoint output or a fresh clone.

The private database is closed pre-alpha. Only a missing/genuinely empty
database or an exact current schema-v7 database is accepted. Every other
existing database, including former exact v6, older, future, malformed,
partial, unexpected, or proof-incompatible state, is preserved and refused
before mutation. Operators must stop Gateway, inspect and back up unpublished
state, then make any deletion decision manually.

## Identity and action attribution

Setup selects one local human through an explicit confirmed plan. Gateway
derives one durable agent for the selected human plus normalised harness name,
then creates a fresh connection session for each MCP connection. The action
actor, accountable human, session, and local assurance are server-owned.

MCP `clientInfo` harness, version, model, and model version are bounded
self-declarations retained for provenance. Local assurance proves that Gateway
bound those values into its own session; it does not authenticate the vendor,
model, or person typing at the terminal. Public tool arguments cannot supply or
override workspace, actor, human, owner, assurance, session, or private paths.

## Publication review

`public_git` warns that portable candidate bytes may become repository-visible
and requires the exact current publication-review digest at checkpoint. The
digest binds accepted tree, candidate tree, semantic diff, repository identity,
origin observation, classification, actor attribution, and selected overlay
operations. Any intervening change makes the acknowledgement stale.

`local_only` does not require the public-visibility acknowledgement, but
checkpoint still changes only the working tree. Git review and ordinary Git
commands remain the only acceptance and transport mechanism.

## Optional Fabric boundary

The optional Fabric binary has a separate authenticated 20-tool HTTP MCP
inventory backed by PostgreSQL. Its tokens, RLS, semantic embedding provider,
remote sync, enrolment, and permission model are server concerns. None is a
claim about the live Stage 2 Gateway. Do not expose Fabric on a non-loopback
network without authenticated HTTPS, secret handling, database isolation, and
an explicit deployment review.
