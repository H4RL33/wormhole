# Gateway integration manifest version 1

**Status:** designed, not implemented; awaiting human approval under issue #50.

This document closes the RFC-0003 integration-manifest open question for
declarative Markdown guidance. It is an implementation contract for later
milestones, not authority to expose a new CLI or MCP surface. Wormhole's own
repository instructions remain repository-owned. This design applies only to
usage guidance offered by Fabric and approved locally through Gateway.

## Status and scope

Version 1 distributes opaque Markdown. Gateway may verify and cache an offer
automatically, but it never approves or applies repository guidance
automatically. Authenticated revocation is the sole automatic repository
mutation: it deactivates model guidance and safely removes only unchanged
Gateway-owned bytes. The only materialised destinations are one managed section
in `AGENTS.md` and managed Markdown files in Wormhole's reserved skill
namespace. No runtime, Fabric storage, CLI command, or MCP tool is implemented
by this design change.

The words MUST, MUST NOT, SHOULD, and MAY are normative. Validation is fail
closed: a violation rejects the whole manifest; entries are never partially
accepted or partially applied.

## Strict version-one schemas

An input is a UTF-8 I-JSON object. A decoder MUST reject invalid UTF-8,
duplicate object member names, non-integer JSON numbers, unknown members at
every schema level, and a non-object root before digest verification. UUIDs use
the canonical lowercase hyphenated form and MUST NOT be the nil UUID. A digest
uses the exact lowercase form `sha256:` followed by 64 hexadecimal characters.

`<slug>` means `^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`, with 1 through 63 ASCII
characters. Every `role_filters` array contains zero through 64 slugs in
strictly increasing bytewise order with no duplicates. An empty filter means
"all binding roles"; it never means "no roles".

### Manifest schema

The root has exactly these ten required members and no others. The required
member list is recorded alphabetically in the contract inventory.

| Member | Exact version-one constraint |
|---|---|
| `schema_version` | JSON integer exactly `1` |
| `manifest_id` | canonical non-nil UUID; identifies one lineage |
| `manifest_version` | JSON integer from `1` through `9007199254740991` |
| `project_id` | canonical non-nil UUID equal to the selected binding |
| `source` | string exactly `fabric` |
| `created_at` | RFC 3339 timestamp in UTC ending `Z`; canonical RFC3339Nano formatting round-trips byte-for-byte |
| `tool_contract_digest` | lowercase SHA-256 digest in the required format |
| `manifest_digest` | lowercase SHA-256 digest in the required format |
| `role_filters` | sorted, unique `<slug>` array as defined above |
| `entries` | 1 through 64 entry objects, strictly increasing by `target` |

Two entries MUST NOT resolve to the same target. The sum of the UTF-8 byte
lengths of every entry's `content` MUST be at most 1,048,576 bytes before role
selection. The bound is on decoded string bytes, not JSON source bytes.

The `manifest_digest` member is deliberately additional to the original M3
outline: without it Gateway cannot bind approval, replay detection, rollback,
and the CLI confirmation to one complete manifest.

### Entry schema and target matrix

Each entry has exactly these seven required members and no others:

```text
kind
target
content
content_digest
merge_policy
required
role_filters
```

`required` is a JSON boolean. It is surfaced to agents as guidance priority; it
does not permit partial materialisation. `content_digest` uses the required
lowercase SHA-256 format. The remaining permitted combinations are exhaustive:

| `kind` | Exact `target` | Exact `merge_policy` |
|---|---|---|
| `agents_bootstrap` | `AGENTS.md` | `managed_section` |
| `skill` | `.agents/skills/wormhole-<skill-slug>/SKILL.md` | `managed_file` |
| `reference` | `.agents/skills/wormhole-<skill-slug>/references/<reference-slug>.md` | `managed_file` |

Targets use `/` separators and are relative to the canonical project root.
Case variants, `.` or `..` components, empty components, backslashes, absolute
paths, drive prefixes, percent-encoding, and Unicode lookalikes are rejected.
The literal `wormhole-` prefix reserves the managed skill namespace; Gateway
never owns `.agents/skills/<other-name>`.

`content` is opaque UTF-8 Markdown with at least one non-line-feed character,
at most 262,144 UTF-8 bytes per entry, no NUL, no carriage return, and exactly
one trailing LF. "Exactly one trailing LF" permits internal LF characters but
rejects a missing final LF and two or more final LF characters. Content MUST
NOT contain any of the three managed marker strings or another
`<!-- wormhole:` marker prefix. Gateway performs no variable interpolation,
template expansion, command parsing, model call, or execution.

All three kinds and only these three are valid: `agents_bootstrap`, `reference`,
and `skill`. All other kinds, targets, merge policies, and kind/target/policy
combinations reject the entire manifest.

### Shape example

This example illustrates the wire shape. The digest strings are placeholders
and therefore the object is not an offer that Gateway would accept.

```json
{
  "schema_version": 1,
  "manifest_id": "94f21655-c38f-4e07-a643-71606ca16a84",
  "manifest_version": 1,
  "project_id": "d86ee890-44e6-4f0f-ae7f-bf6886863f05",
  "source": "fabric",
  "created_at": "2026-07-26T12:00:00Z",
  "tool_contract_digest": "sha256:<64 lowercase hex>",
  "manifest_digest": "sha256:<64 lowercase hex>",
  "role_filters": ["contributor"],
  "entries": [
    {
      "kind": "agents_bootstrap",
      "target": "AGENTS.md",
      "content": "Use Wormhole guidance for this project.\n",
      "content_digest": "sha256:<64 lowercase hex>",
      "merge_policy": "managed_section",
      "required": true,
      "role_filters": []
    }
  ]
}
```

## Canonical digests

V1 uses SHA-256 for integrity and RFC 8785 JSON Canonicalization Scheme (JCS)
for structured values.

1. `content_digest` is `sha256:` plus lowercase hex SHA-256 of the exact UTF-8
   bytes in the decoded `content` string. Gateway does not normalize Unicode,
   whitespace, or line endings.
2. `manifest_digest` is SHA-256 of the RFC 8785/JCS UTF-8 encoding of the
   complete validated manifest object with only the `manifest_digest` member
   omitted. Entry order therefore participates in the digest.
3. `tool_contract_digest` is SHA-256 of the RFC 8785/JCS UTF-8 encoding of the
   live Gateway descriptor array represented by `mcp_tools.gateway` in
   `docs/contracts/alpha-contract.json`. Descriptors are sorted by `name` before
   canonicalization. Planned `designed_interfaces` and Fabric descriptors are
   not inputs. Variant and enum arrays retain their inventoried order.

Gateway validates every content digest before the manifest digest, then checks
the tool-contract digest against its running registry. It compares decoded
digest bytes in constant time. RFC 8785 does not sort arrays, so the manifest
schema's entry ordering and the explicit Gateway descriptor name ordering are
mandatory.

The tuple `(manifest_id, manifest_version, manifest_digest)` is idempotent. An
exact replay is a no-op apart from last-seen telemetry. The same ID and version
with a different digest is Fabric equivocation: Gateway rejects it, retains the
previous state, raises `attention_required`, and audits it. A lower version is a
replay and cannot become pending; rollback uses only an already approved local
cache entry. A higher version in the same lineage is an update candidate.

## Trust and binding selection

Fabric offers a manifest only on an authenticated, project-bound Fabric
session. Non-loopback Fabric endpoints require authenticated HTTPS. Plain HTTP
is allowed only for explicit development endpoints on `localhost`,
`127.0.0.0/8`, or `[::1]`; it does not weaken Passport authentication. Gateway
rejects manifests from local files, arbitrary URLs, another project session,
or a response whose `project_id` differs from the credential binding.

V1 has no detached signatures, publisher keys, certificate pin set, or separate
manifest PKI. SHA-256 detects corruption and binds local confirmation to bytes;
it does not establish publisher identity. Authentication of the Fabric session
is the publisher trust decision. A compromised authorised Fabric can still
offer malicious prose, which is why a human-readable preview and explicit
local approval remain mandatory.

Exactly one manifest lineage may be active for a project. The selected identity
must have exactly one singular role in its credential binding. Missing,
multiple, or malformed binding roles fail closed. The caller, repository,
manifest content, CLI, and MCP request cannot override that role.

Selection is deterministic:

1. require exact `project_id` equality with the binding;
2. require the singular binding role to match the manifest `role_filters`, or
   accept an empty manifest filter;
3. retain entries whose `role_filters` are empty or contain that exact role;
4. order selected entries by target and expose that same order everywhere.

If the manifest filter does not match, the offer is inapplicable and cannot be
approved. If no entry matches, verification fails. Role filters use exact,
case-sensitive slug equality; there are no wildcards, aliases, inheritance,
multiple-role unions, or caller-selected roles in V1.

## Ownership and safe materialisation

Gateway is authoritative for approved manifest bodies, journal records, and
audit records in project-scoped local SQLite. The repository file
`.wormhole/integration-state.json` is a mode-`0600`, atomically written
projection for inspection and crash diagnosis, not an authority and never a
credential store. A newly created `.wormhole` directory is mode `0700`; an
existing directory's mode is not silently changed.

The projection has schema version 1 and records project ID, active and pending
manifest IDs/versions/digests, resolved role, approval state, materialisation
state (including `removal_required`), last verification time, and for each
selected target its kind, content digest, rendered digest, ownership mode, and
verified file identity. Unknown projection fields are ignored on read because
SQLite is authoritative; a missing, stale, malformed, symlinked, or hard-linked
projection is regenerated only during a human-approved repository transaction
or the mandatory safe cleanup of an authenticated revocation.

Lexical canonicalisation followed by a pathname recheck is not a sufficient
time-of-check/time-of-use control. Every materialisation and removal MUST use
descriptor-relative, no-follow filesystem operations rooted in a held project
directory descriptor or equivalent platform handle:

1. open the canonical project root without following a link, validate its
   directory identity, and retain that handle for the complete transaction;
2. open each existing ancestor relative to the previously verified handle with
   no-follow and directory-only semantics, validate device/file ID, type, and
   link identity, and retain held root and ancestor directory handles until the
   operation commits;
3. inspect a target relative to its held parent with no-follow metadata calls;
   reject a symlink/reparse point, non-regular target, or regular target whose
   link count is not exactly one, even when every link is inside the project;
4. create an exclusive temporary regular file relative to that verified parent
   handle, fsync it, and rename it relative to the same verified handle; remove
   files only through a relative unlink against that handle;
5. validate post-operation identity and content through the retained handles,
   including revalidating every root/ancestor identity tuple, then fsync the
   affected directory handles before releasing any of them.

On POSIX this requires `openat`/`openat2`-class no-follow opens,
`fstatat(..., AT_SYMLINK_NOFOLLOW)`, and `renameat`/`unlinkat`-class operations;
absolute-path `os.Rename` or a fresh pathname walk is not equivalent. On
Windows it requires no-reparse-point directory/file handles, volume and file-ID
validation, and rename/delete operations bound to those verified handles. If a
platform cannot provide equivalent descriptor-relative containment, stable
identity validation, link-count verification, and post-operation identity
checks, materialisation and revocation cleanup are unsupported and fail closed.

New Markdown files use mode `0644`; existing `AGENTS.md` permissions are
preserved. Gateway never sets an executable bit. It creates only the specific
parent directories needed for selected managed targets, using relative
directory creation followed by a no-follow open and identity validation against
the verified parent handle. It removes a directory later only through its held
parent handle, and only if Gateway created it and it is empty.

### `AGENTS.md` managed section

The three marker lines are fixed exactly:

```text
<!-- wormhole:managed-begin integration-manifest/v1 -->
<!-- wormhole:manifest id=<uuid> version=<n> digest=sha256:<hex> -->
<!-- wormhole:managed-end integration-manifest/v1 -->
```

The rendered block is begin marker plus LF, metadata marker with actual values
plus LF, the already LF-terminated manifest content, then end marker plus LF.
The metadata digest is the complete `manifest_digest`, not the entry digest.

When `AGENTS.md` is absent, Gateway creates a file containing only this block.
When it exists without Wormhole markers, Gateway appends a separator and the
block: one LF if the file already ends in LF, otherwise two LFs. The separator
is recorded as managed insertion bytes. Every pre-existing byte remains
unchanged. On update, Gateway replaces only the inclusive marker block. On
remove it removes the block and its recorded insertion separator, restoring an
otherwise unedited original byte-for-byte. It never rewrites the whole file
solely to update the managed section.

Zero, one, and more-than-one sections are distinct cases. Unmatched, reordered,
duplicate, nested, edited, or metadata-inconsistent markers are marker drift and
block every apply, update, remove, and rollback. A rendered-digest mismatch is
also drift. There is no `--force` adoption or overwrite path; the human must
restore the recorded bytes or remove the malformed material manually.

### Managed skill and reference files

Skill and reference entries own the entire exact target file. Gateway creates
an absent target, but refuses to adopt or overwrite a pre-existing untracked
file. It updates or removes a tracked target only when its current byte digest
equals the last rendered digest. It never scans, owns, rewrites, or deletes
other files in the skill directory. An unmodified Gateway-created file may be
removed; a modified file is drift and is preserved.

### Atomic journal and recovery

Each multi-file mutation is one durable journaled logical transaction:

1. validate every target and compute every before/after digest;
2. persist and fsync a SQLite journal containing operation ID, complete
   before-images or absence records, intended bytes, modes, and digests;
3. retain the verified root/ancestor handles and write each target in lexical
   order to a same-directory exclusive temporary regular file relative to its
   held parent, fsync it, rename it relative to that same handle, validate the
   resulting identity and bytes, then fsync the parent handle;
4. write `.wormhole/integration-state.json` last by the same method;
5. commit active state and an audit record, then mark the journal complete.

A validation or write error before completion restores changed targets in
reverse order using the durable before-images, but only where the current file
identity and bytes match that journal operation's intended result. A mismatch
stops recovery in `recovery_required` rather than overwriting user work. On
startup Gateway finishes rollback of any incomplete apply/update/rollback
journal before reporting guidance as active. No manifest is reported `applied`
until every rename, post-operation identity check, and state commit is durable.
An incomplete revocation journal is different: guidance remains deactivated
and startup resumes safe forward removal; it never rolls back into an active
manifest.

Normal rollback does not restore a historic whole `AGENTS.md` before-image.
It re-renders the prior approved managed block into the current verified
outside bytes, preserving user edits made outside the section since approval.

## Lifecycle, offline operation, revocation, and rollback

The durable candidate lifecycle is:

```text
offered -> verified -> awaiting_approval -> approved -> applied
                                  |             |
                                  +-> postponed +-> not_applied
                                  +-> rejected
offered -> verification_failed
approved/applied -> revoked
revoked -> not_applied
        -> removal_required + attention_required
```

`offered` records authenticated receipt. Verification is automatic and has no
repository side effects. A valid applicable candidate becomes `verified` and
then `awaiting_approval`. A human may postpone or reject it. Re-receiving the
same tuple preserves that decision; only a higher same-lineage version creates
a new candidate. Approval is durable before materialisation. `applied` is an
independent materialisation state, not proof of approval.

Gateway caches immutable validated bodies. It may automatically receive,
verify, and cache while online, but never auto-approves or auto-applies. The
automatic removal of unchanged owned bytes after revocation is a safety action,
not approval or application. Initial approval and every update require an
online authenticated binding so known revocation and role state are current.
While offline:

* preview and status work from cache;
* the last approved, nonrevoked, schema/tool-compatible guidance remains
  readable;
* initial approval and `update` are refused;
* reapplication of the already approved active digest is allowed;
* rollback to the cached prior approved digest is allowed;
* remove is allowed.

A valid authenticated Fabric revocation first atomically marks the digest
`revoked` in SQLite and empties its model-readable guidance cache. Every later
`wormhole.agent.get_guidance` read therefore returns no revoked content,
including during a crash recovery. The same durable journal operation then:

1. removes the exact tracked `AGENTS.md` managed span and its recorded insertion
   separator only when markers, file identity, and rendered digest are intact;
2. removes each managed skill/reference file only when its verified identity
   and bytes equal the tracked rendered identity and digest;
3. preserves every drifted, replaced, linked, nonregular, or otherwise
   unverifiable target byte-for-byte and never deletes an unrelated file;
4. commits `not_applied` when all owned bytes were removed, otherwise commits
   `removal_required` and `attention_required` with the preserved target list.

This is a journaled logical transaction: deactivation is durable before any
filesystem mutation, every deletion is descriptor-relative and individually
atomic, and a crash resumes forward cleanup. Mixed clean/drifted manifests may
therefore have clean owned targets removed while divergent targets are
preserved; they never reactivate model guidance. Revocation records and audit
history are immutable. Apply, reapply, update, and rollback to the revoked
digest remain prohibited.

Rollback selects the most recent earlier approved version in the same manifest
lineage that is cached, nonrevoked, and compatible with schema version 1 and the
current Gateway tool digest. It never downloads while offline and never accepts
a raw version or digest supplied as a rollback target. The preview identifies
the selected prior digest; confirmation binds to it. Rollback is a new audited
local action and does not erase later manifest/audit history.

`integration remove` deactivates the local active lineage and removes only
unchanged Gateway-managed bytes. For a revoked manifest in `removal_required`,
it is the human-confirmed retry after the user has resolved drift. It preserves
cached immutable history and audit records, and permits a later human-approved
manifest ID to establish a new lineage. Remaining drift or incomplete recovery
preserves those bytes and leaves `removal_required` rather than risking
user-owned content.

## CLI contract

The exact planned commands are:

| Command | Exact flags | Behaviour |
|---|---|---|
| `wormhole integration preview` | `--project <uuid>` | read-only candidate/current metadata and unified repository diff |
| `wormhole integration apply` | `--project <uuid>`, `--confirm-digest <sha256:...>` | approve and apply an initial candidate, or reapply the already approved active digest |
| `wormhole integration status` | `--project <uuid>`, `--json` | read-only lifecycle, compatibility, drift, connection, active, pending, and rollback-candidate state |
| `wormhole integration update` | `--project <uuid>`, `--confirm-digest <sha256:...>` | approve and atomically apply the latest verified higher version in the active lineage |
| `wormhole integration remove` | `--project <uuid>`, `--confirm-digest <sha256:...>` | deactivate locally and remove unchanged managed material |
| `wormhole integration rollback` | `--project <uuid>`, `--confirm-digest <sha256:...>` | atomically apply the automatically selected prior approved version |

There are no positionals and no aliases. `--project` participates in the
existing explicit project resolver: flag, then nearest project config. Missing
or ambiguous binding fails; no default project is invented. `--json` exists
only on status. The four mutation commands have `--confirm-digest`; there is no
`--force`, `--yes`, `--root`, manifest-path, role, URL, or content flag.

Status reports revocation even while cleanup is running. Its human and JSON
forms include `approval_state: revoked`, `guidance_active: false`, the
materialisation state, and preserved target paths. An incomplete or unsafe
cleanup reports `materialization_state: removal_required` and
`connection_state: attention_required`; it never describes revoked guidance as
active. `integration remove` may retry that safe cleanup after the human repairs
or removes divergent bytes, but it gains no overwrite option.

Before a mutation, CLI prints the operation, project, role, full digest, and
diff. On a TTY, acceptance is exactly case-insensitive `y`, `yes`, or the full
expected digest. All other input declines. In noninteractive use,
`--confirm-digest` is mandatory and its value must equal the full expected
digest byte-for-byte; abbreviations are rejected. When supplied on a TTY, a
matching flag is itself the confirmation. Apply/update bind to the pending
digest, remove to the active digest, and rollback to the selected prior digest.
When revocation cleanup is `removal_required`, remove binds to that revoked
manifest's retained digest.

Every command uses these exits:

* `0`: the requested read completed, or the confirmed mutation committed
  durably;
* `1`: operational, trust, compatibility, policy, drift, offline, declined,
  unavailable-candidate, or recovery failure;
* `2`: command, flag, value, or usage error.

Status returning a successfully read unhealthy state still exits `0`; the JSON
body carries that state. No command silently changes files after exit `1` or
`2`; an interrupted journal may only restore its own partial writes.

## Read-only MCP contract

The exact planned local Gateway tool name is
`wormhole.agent.get_guidance`. It has `required_permissions: []` because the
same-user protected local IPC boundary is the V1 trust boundary. It is absent
from both live MCP inventories until implemented.

The request is a closed object with one required property and no others:

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["project_id"],
  "properties": {
    "project_id": {"type": "string", "format": "uuid"}
  }
}
```

The response is a closed object. Every listed member is required; nullable
members are present as JSON `null`, never omitted.

| Member | Exact type |
|---|---|
| `schema_version` | integer exactly `1` |
| `project_id` | canonical UUID |
| `manifest_id` | canonical UUID or `null` |
| `manifest_version` | positive integer or `null` |
| `manifest_digest` | required digest string or `null` |
| `resolved_role` | role slug or `null` |
| `guidance` | non-null array of closed guidance objects |
| `materialization_state` | `applied`, `drifted`, `not_applied`, `recovery_required`, or `removal_required` |
| `approval_state` | `none`, `offered`, `verified`, `awaiting_approval`, `postponed`, `rejected`, `approved`, `revoked`, or `verification_failed` |
| `pending_manifest_version` | positive integer or `null` |
| `pending_manifest_digest` | required digest string or `null` |
| `connection_state` | `online`, `offline`, `synchronizing`, or `attention_required` |
| `last_verified_at` | canonical UTC RFC3339Nano string or `null` |

Each guidance object is closed and requires exactly `kind`, `content`,
`content_digest`, and `required`. `kind` is one of the three entry kinds,
`content` is the exact approved Markdown, `content_digest` binds those bytes,
and `required` is boolean. Targets and merge policy are intentionally not
exposed as model instructions. Items retain deterministic target selection
order.

The tool performs one project-scoped read of already cached, approved state. It
does not contact Fabric, refresh, verify, approve, postpone, reject, render,
read repository files, repair drift, write SQLite, write audit records, or
mutate files. It accepts no role, version, digest, approval, refresh, or mutation
input. Offline reads return the last approved compatible nonrevoked cache.
Without such a cache, all active manifest fields are null and `guidance` is an
empty array. Revocation deactivates the returned content immediately, before
its journaled filesystem cleanup starts; `removal_required` exposes preserved
drift without exposing the revoked content.

If an upgrade makes the approved tool digest incompatible, the historical
approval remains visible, `guidance` is empty, and `connection_state` is
`attention_required`; repository removal still requires the CLI. This avoids
silently presenting guidance written for a different tool surface.

## Audit contract

Gateway writes project-scoped, append-only audit records for these exact event
types:

```text
integration_manifest.offered
integration_manifest.verified
integration_manifest.verification_failed
integration_manifest.awaiting_approval
integration_manifest.postponed
integration_manifest.rejected
integration_manifest.approved
integration_manifest.applied
integration_manifest.apply_failed
integration_manifest.removed
integration_manifest.rollback_applied
integration_manifest.revoked
integration_manifest.revocation_removed
integration_manifest.revocation_removal_required
integration_manifest.equivocation_detected
integration_manifest.drift_detected
integration_manifest.recovery_started
integration_manifest.recovery_completed
```

Every record includes event ID, project ID, event type, UTC timestamp, actor
kind (`fabric`, `gateway`, or `human_cli`), operation ID, manifest ID/version/
digest when known, prior digest when relevant, outcome, and a stable reason
code. It never stores raw credentials or duplicates manifest content. Local
records are durable before the represented state transition is reported and
are queued for Fabric synchronisation when the corresponding audit transport
exists. Synchronisation failure never deletes a local record.

## Threat model and prohibited capabilities

V1 protects user-owned repository bytes against accidental overwrite, path
escape, symlink/hard-link and ancestor-replacement races, partial multi-file
writes, corrupted transport, replay, equivocation, incompatible tool guidance,
and unapproved material changes. Size and count limits bound memory, disk, and
preview costs. Authenticated project scope, exact digests, human preview,
descriptor-relative held-handle operations, strict identity/ownership checks,
and durable recovery are the controls.

V1 does not make authenticated Fabric prose inherently safe. Markdown can
contain prompt injection or describe dangerous commands. It remains opaque
text: Gateway neither interprets nor executes it, and only approved content is
returned to models. Human confirmation is a policy control, not cryptographic
proof that a human rather than another same-user process typed the answer. The
same-user CLI boundary cannot prevent a compromised same-user account from
editing files directly.

The following capabilities are prohibited in a manifest and remain
unimplemented:

* executable scripts and binaries;
* shell commands as executable entry types or install actions;
* package dependencies or package-manager actions;
* dynamic plugins or plugin installation;
* pre-install, post-install hooks, or any other lifecycle hook;
* arbitrary environment mutation, credential mutation, or process launch;
* executable file modes, non-Markdown targets, symlinks, or hard links;
* template expansion, variables, wildcards, includes, remote references, or
  generated commands;
* automatic approval/application, model approval, or model-authored mutation;
* detached signatures, detached PKI, or multiple publisher trust roots;
* multiple active manifests, role unions, or caller-selected role filters.

Markdown may show a command as prose or a fenced example; that does not create
an executable capability. Enforcement is structural: only the three Markdown
entry kinds and exact `.md` targets exist, and no implementation may parse
content into actions.

## Compatibility and cache

Gateway accepts exactly manifest `schema_version: 1`; there is no negotiation.
An unknown schema version or tool-contract mismatch becomes
`verification_failed` and cannot be approved. Backward-compatible additions
still require a future schema version because unknown members are rejected.

The project-scoped SQLite cache stores immutable validated manifest bodies by
ID, version, and digest, current and pending state, revocations, the active
approved version, and the most recent earlier approved compatible version as a
rollback candidate. It retains the active body, one rollback body, and latest
pending body even across restarts. Older bodies may be garbage-collected only
after their digest and audit history are durable; revocation and audit metadata
are never discarded by cache eviction.

There is no time-to-live for an approved offline cache. "Last approved" is
bounded by known revocation: Gateway cannot learn a new revocation while
offline, but applies it before serving subsequent guidance as soon as sync
delivers it. A running-registry digest change immediately makes an otherwise
approved cache incompatible as described in the MCP contract.

## Test strategy

Implementation work must retain the design-only tests and add:

* strict decoding table tests for every field, boundary, unknown/duplicate
  member, target matrix, byte limit, ordering, and content rule;
* RFC 8785 published vectors plus manifest, content, and Gateway descriptor
  digest fixtures;
* authenticated project and singular-role cross-binding rejection tests;
* replay/equivocation, schema/tool mismatch, revocation, and offline lifecycle
  tests;
* byte-for-byte `AGENTS.md` preservation, marker drift/injection, pre-existing
  skill, traversal, symlink, hard-link, nonregular file, and mode tests;
* adversarial ancestor/target rename and replacement tests at every filesystem
  boundary, proving held descriptor containment and post-operation identity;
* process-kill fault injection before and after each journal/fsync/rename/state
  step for apply and revocation, proving recovery cannot overwrite divergent
  user bytes or reactivate revoked guidance;
* CLI golden help/flag/exit/TTY/noninteractive digest-confirmation tests;
* MCP closed request/response tests proving no permission, no Fabric access,
  no repository access, and no mutation;
* contract inventory tests proving all planned names remain only under
  `designed_interfaces.integration_manifest_v1` until runtime implementation.

No runtime implementation may begin until the human approval required by issue
#50 is recorded.

## Decision checklist

- Schema and entry constraints: fixed above, including strict unknown-member
  rejection, byte limits, digest fields, and target matrix.
- Trust: authenticated project-bound Fabric; SHA integrity; no detached
  signatures.
- Ownership and rendering: SQLite authority, `0600` state projection, fixed
  markers, managed skill namespace, and byte-preserving merge.
- Role selection: exact project plus one credential-binding role; no override.
- Update, removal, rollback, offline, revocation, recovery, and audit: fixed.
- Compatibility and cache retention: fixed for schema and Gateway tool digest.
- Threat model and all prohibited executable capabilities: explicit.
- Exact CLI names, flags, confirmations, and exits: fixed.
- Exact read-only MCP name, request, response, permission, offline behaviour,
  and non-mutation guarantee: fixed.
- Test strategy and planned-versus-live contract separation: fixed.
