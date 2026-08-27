# Gateway enrolment lifecycle

**Status:** alpha-inventory contract design
**Protocol version:** `1`
**Local MCP tool:** `wormhole.agent.enrol`

Tasks 2 through 4 implement the contract boundary, Fabric registration,
atomic Gateway credential persistence, transactional bootstrap, and the
transition to incremental sync. `credentials_persisted` is a recoverable
checkpoint; the completed call returns `success` in state `ready` only after
the snapshot and ready checkpoint commit together.

## Ownership and startup seam

Initial enrolment is a Gateway/Fabric MCP lifecycle. An operator-approved
private control-plane client constructs the closed request, generates one
attempt key, selects a credential profile identifier, and sends the request
over Gateway's existing same-user, OS-protected local MCP socket:

```text
operator-approved private MCP client -> local Gateway socket -> Gateway-owned Fabric MCP client
```

The control client has no direct-to-Fabric fallback. Gateway is the only
component permitted to call Fabric enrolment, persist the issued credential,
bootstrap SQLite, start incremental synchronisation, and report readiness.

The public `wormhole` CLI remains responsible for canonical `setup`, connector
lifecycle, and the five `status`/`diff`/`import`/`checkpoint`/`stash` workspace
operations. Those commands are not enrolment aliases and do not invoke this
endpoint. Profile listing and identity inspection operate on credential
profiles that a completed control-plane enrolment already persisted; neither
constructs an enrolment request or persists a new profile.

This creates an intentional pre-credential startup mode. `gatewayd` must be
able to resolve its XDG socket/database paths, open the local socket, complete
the MCP `initialize` sequence, and serve `wormhole.agent.enrol` before a
credential profile exists. Normal credential-bound tools remain unchanged.
The startup configuration seam must use `internal/runtime/config`-compatible
values; `internal/runtime/localapi` must not import command-package
configuration code.

Task 2 registers the pre-credential tool and its validation/schema contract.
Its executor fails closed as unavailable until Task 3 wires registration and
credential persistence. This prevents the design-only endpoint from silently
performing a partial lifecycle or allowing its control client to call Fabric
directly.

The name `wormhole.agent.enrol` follows the RFC-0001 M2
`wormhole.<pillar-noun>.<verb>` grammar by using the existing `agent` pillar;
it does not introduce an unratified `gateway` pillar. RFC-0003 §8.1 authorises
Gateway ownership of the lifecycle. The accompanying RFC-0003 amendment now
ratifies the pre-credential startup mode, `wormhole.agent.enrol`, Gateway-owned
profile persistence, and the no-token result contract, superseding the old
`proxyRegister` token-return exception for enrolment.

## Version 1 request

All fields are required. Empty arrays are encoded as arrays rather than by
omitting the field.

| Field | Meaning |
|---|---|
| `version` | Integer `1`; other values are rejected. |
| `project_id` | Explicit local project binding and Fabric project scope. |
| `owner` | Human or organisation owner of the agent identity. |
| `model` | Model identifier. |
| `capabilities` | Declared agent capabilities. |
| `repositories` | Requested repository scope. |
| `roles` | Requested project roles. |
| `requested_permissions` | Requested permission actions. |
| `fabric_address` | Address used only by Gateway's Fabric client after the local control client submits the request. |
| `idempotency_key` | Canonical lowercase UUID identifying one user-approved attempt. |
| `credential_profile` | Non-empty safe profile identifier resolved only beneath Gateway's credential root. |

The role and permission envelope is trusted local Gateway policy and is not a
request field. A client therefore cannot expand the envelope it is checked
against. A fresh daemon obtains this policy through the
`EnrolmentPolicySource` seam backed by operator-approved local Gateway
configuration. Missing or unreadable policy is deny-all and returns a policy
error; request data and absent Passport credentials are never policy sources.

`credential_profile` is a wire field selected by the approved control client,
not a public command-line option. Gateway resolves that safe identifier below
its credential directory. The protocol accepts no caller-selected credential
destination outside that root. Profile identifiers cannot contain path
separators or `..`, so absolute, out-of-root, and symlink traversal paths cannot
be expressed by the wire contract.

## Result union

Every result carries `version`, `code`, `state`, the original
`idempotency_key`, and `retryable`. Successful or post-registration results may
also carry `agent_id`, `passport_id`, and the safe `credential_profile`.
Results never contain a raw Passport token.

| Code | State | Retryable | Meaning |
|---|---|---:|---|
| `fabric_unreachable` | `failed` | yes | No registration result was confirmed. Retry the same attempt. |
| `invalid_project` | `failed` | no | Fabric rejected the project binding. |
| `permissions_rejected` | `failed` | no | Requested scope was rejected. |
| `duplicate_identity` | `failed` | no | A distinct attempt conflicts with an existing identity. This is not used for a replay of the same key. |
| `repository_mismatch` | `failed` | no | Canonical repository scope does not match the project. |
| `credential_persistence_failed` | `recovery_required` | yes | Registration completed but the credential was not committed locally. |
| `bootstrap_failed_after_enrolment` | `recovery_required` | yes | Registration and credential persistence completed, but bootstrap did not commit. |
| `checkpoint_persistence_failed` | `attention_required` | no | Bootstrap failed and Gateway could not durably record the recovery checkpoint; operator attention is required. |
| `credentials_persisted` | `credentials_persisted` | yes | Task 3's truthful nonterminal boundary; retrying the same local request resumes bootstrap. |
| `success` | `ready` | no | Credential persistence and bootstrap committed; normal operation may begin. |

`message`, when present, is a sanitized operator-facing explanation. It must
not include tokens or credential contents.

## Lifecycle and recovery

```text
requested
  -> registration_in_progress
  -> registered
  -> credentials_persisted
  -> bootstrap_in_progress
  -> ready
```

Before registration is confirmed, an operation may enter `failed`; only a
retryable `fabric_unreachable` result at the `requested` durable checkpoint may
restart at `requested` with the same key. Nonretryable failures are terminal.
After `registered`, failure
to durably commit credentials or bootstrap enters `recovery_required`, never
plain `failed`. Recovery resumes from the last durable checkpoint:

* `credential_persistence_failed` may return to `registration_in_progress`
  only from the durable `registered` checkpoint;
* `bootstrap_failed_after_enrolment` may resume at `bootstrap_in_progress`
  only from the durable `credentials_persisted` checkpoint;
* `registered` without committed bootstrap is recoverable only after Gateway
  durably records the recovery checkpoint;
* `ready` is terminal for the attempt.

Gateway writes post-bootstrap-failure recovery checkpoints with a bounded,
detached context so cancellation of the originating local request cannot
interrupt the durability decision. Gateway returns
`bootstrap_failed_after_enrolment` only after that checkpoint commits. If the
checkpoint itself cannot be committed, Gateway returns the distinct,
non-retryable `checkpoint_persistence_failed` result in `attention_required`
state instead of claiming that recovery is durable.

Incremental synchronisation starts only after bootstrap commits and the state
becomes `ready`.

### Bootstrap snapshot contract

`wormhole.sync.bootstrap` version 1 returns exactly `org_config`,
`project_list`, `task_list`, `kb_list`, `timestamp`, and `version`.
`org_config` is a strict schema-version-1 project snapshot containing project
metadata, the authenticated Agent and Passport, permissions, Channels,
Events, Tasks, KB articles, and integration-manifest metadata. The top-level
Task and KB arrays exactly mirror their nested counterparts; `project_list`
is deliberately a non-null empty array in version 1. Integration-manifest
metadata must be exactly JSON `null` until its storage and distribution design
is approved.

Fabric composes the snapshot from one repeatable-read project transaction.
Gateway validates the complete response before mutation, then stores identity,
authorization, project state, Channels, Tasks, KB, Events, metadata, and the
`ready` checkpoint in one SQLite transaction. Any validation or insertion
failure leaves snapshot rows uncommitted and records `recovery_required` in a
separate, bounded detached transaction. If that recovery checkpoint cannot be
committed, Gateway reports `attention_required` instead. Retrying the same
durable attempt reuses its credential and Passport and reruns bootstrap without
registration.

## Idempotency and crash boundaries

The approved control client generates a candidate key once per operator-approved
enrolment attempt; it must not generate a new key inside an individual network
retry.
Before contacting Fabric, Gateway writes a non-secret SQLite attempt row with
the project, canonical request digest, state, credential profile, and candidate
key. A later control-client call may present another candidate key, but Gateway
loads the active row for that profile and matching project/digest and reuses
its stored key. The result reports that durable key, which the control client
treats as the resumed attempt identifier. Gateway scopes keys by `project_id`.

Task 3 durably records the project, key, canonical request digest, state,
credential profile, and non-secret identity references in Gateway SQLite. The
initial `requested` checkpoint commits before any Fabric contact. The rules are:

* same project, key, and digest resumes or returns the stored result;
* same project and key with a different digest is rejected;
* a retry must not issue a second Passport;
* result replay returns references and state, never credential material.

Gateway serializes execution by credential profile. It safely inspects the
profile before registration and rejects malformed, symlinked, non-regular,
over-permissive, wrongly owned, or mismatched files without contacting Fabric.
Credential commit is atomic and no-clobber: an occupied or concurrently
created final path is never replaced. Attempt rows and credential files never
contain a second copy of the raw token.

Credential-root traversal and profile reads use directory/file handles with
no-follow opens plus ownership, type, and mode checks on Linux and Darwin.
Linux commits with `renameat2(RENAME_NOREPLACE)`; Darwin commits with
`renameatx_np(RENAME_EXCL)`. Other operating-system builds fail credential
reads and writes closed because they do not provide a verified implementation
of those guarantees. This does not broaden daemon release support: `gatewayd`
remains supported on Linux/WSL only; native macOS and Windows execution remains
unsupported.

Fabric also persists the project-scoped idempotency key and digest with the
identity and hashed-token references. The paired local/Fabric checkpoints close
the crash window after Fabric issuance but before Gateway credential commit by
allowing the same identity to be replayed and, when necessary, recovered through
the controlled reissue flow. Existing uniqueness constraints remain a backstop,
not the idempotency contract.

Raw tokens are intentionally not recoverable from Fabric or SQLite. If a token
was issued but not durably persisted, replay cannot promise the original raw
token. Recovery permits exactly one controlled reissue for the matching
project, attempt key, and request digest. Fabric preserves the original agent
and Passport references, expires the prior token hash, stores only the new
token hash/reference, and records the reissue in the audit trail. The same
transaction-scoped advisory lock that serializes initial registration also
serializes reissue. A second reissue is rejected as exhausted and requires
operator recovery; it never creates a duplicate identity or exposes token
material in a local result, log, event, or error.

On an ordinary replay, Fabric returns identity references without a token.
Gateway accepts that replay only when the matching credential profile is
already readable and references the same identity. If the profile is absent
after confirmed registration, Gateway requests the single controlled reissue
and atomically persists it. A mismatched or malformed existing profile fails
closed instead of being overwritten.

## Validation and canonicalisation

Gateway rejects a request before execution when:

* `version` is not `1`;
* `project_id` is empty;
* `fabric_address` is empty;
* the idempotency key is not a canonical lowercase UUID;
* a requested role or permission is outside trusted local policy;
* `credential_profile` is empty or contains traversal/path syntax;
* repositories collide after lexical canonicalisation.

Repository canonicalisation trims whitespace, lowercases URL scheme and host,
cleans the path, removes a trailing `.git`, and removes trailing separators.
It preserves path case and does not access the filesystem, resolve symlinks,
clone repositories, or fetch remotes. Project repository comparison remains a
Gateway/Fabric execution concern for Task 3.

The local socket's same-user OS boundary is the only pre-credential transport
trust boundary in protocol version 1. The tool requires no Passport permission
because a Passport does not yet exist.

The request schema is closed to additional properties and requires a non-empty
`credential_profile`. The result inventory is a per-code union: each variant
fixes its `code`, valid `state`, and `retryable` value so an invalid combination
cannot satisfy the reviewed schema.
