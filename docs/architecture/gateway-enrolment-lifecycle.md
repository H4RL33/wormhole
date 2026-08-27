# Gateway enrolment lifecycle

**Status: Historical/future optional-Fabric design.**

This document preserves the reviewed pre-credential lifecycle for an optional
Fabric-connected Gateway. Enrolment is not a live Stage 2 Gateway operation.
The Stage 2 Gateway is local-only, setup selects machine-private human and agent
identity, and its 17-tool inventory contains no enrolment descriptor.

Nothing below is a current user procedure, CLI promise, service bootstrap path,
or Stage 2 acceptance dependency. The optional Fabric binary retains a
server-side enrolment descriptor in its separate 20-tool inventory, but no
current production Gateway route connects a harness to this lifecycle.

## Preserved design goals

The future lifecycle was intended to:

- keep raw credentials inside an owner-private Gateway boundary;
- expose bounded non-secret status to a same-user client;
- bind every attempt to project, repository, requested identity, Fabric URL,
  profile, and idempotency key;
- make retries idempotent across process interruption;
- persist credentials before declaring the operation ready;
- avoid returning tokens in MCP results, errors, logs, audit, or sync rows; and
- recover without creating duplicate remote agents or credential profiles.

These goals remain constraints if optional Fabric connection work is revived.
They do not authorise adding a live tool or command.

## Retained state machine

The historical design used these phases:

```text
requested
  -> remote_identity_issued
  -> credential_persisted
  -> bootstrap_pending
  -> ready
```

Failures before credential persistence could retry the same remote attempt.
Failures after credential persistence had to resume from the owner-private
credential reference rather than return or recreate a token. Terminal binding
or configuration errors remained failed until a human changed the plan.

An implementation was required to compare exact request hashes and stable
attempt keys before retrying. The same key with different binding fields was
not idempotent. Remote success plus local persistence failure was an explicit
recoverable state, never permission to print the credential.

## Authority boundary

The proposed method was a pre-credential same-user control-plane route. Empty
MCP permissions did not mean unauthenticated network access: owner-only socket
access and a private setup capability were the local boundary before a remote
Passport existed. That boundary did not defend against a hostile same-user
process or prove physical human presence.

Project ID, repository identity, Fabric address, requested role/permissions,
and local profile name were comparison evidence bound into the confirmed
attempt. Gateway, not a harness, owned credential persistence and remote
bootstrap. Result variants exposed stable public IDs and lifecycle state only.

## Secret handling

Any future implementation must keep token bytes:

- out of public and private RPC results;
- out of logs, diagnostics, telemetry, audit, sync payloads, and database error
  strings;
- out of tracked `.wormhole/` state; and
- inside an owner-only, no-follow, atomically committed credential store.

Diagnostics may identify a profile or non-secret remote entity but must not
include authorization headers or request bodies containing credentials.

## Relationship to Stage 2 setup

Current `wormhole setup` does not enrol with Fabric. It prints and confirms a
plan, ensures the host user service, registers an existing Git-native project,
selects a local human identity, records publication policy, imports the tracked
base, verifies exact readback, and installs detected connectors.

The setup private RPC primitives remain outside `tools/list` and retain their
plan-drift protections. The hermetic Stage 2 process gate calls those daemon
primitives directly because host service-manager installation is tested
separately; it does not introduce a bypass or activate this enrolment design.

## Conditions for future activation

Reviving optional Fabric enrolment requires a new approved design and plan,
production routing, threat review, exact Gateway inventory and generated
guidance updates, contract-manifest changes, process coverage, migration and
recovery evidence, active documentation cutover, and independent review. It
must remain absent from Gateway until all those changes land together.
