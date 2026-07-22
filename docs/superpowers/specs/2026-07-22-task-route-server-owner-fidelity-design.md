# Routed Task Server Ownership Fidelity Design

## Goal

Preserve the owner selected by `wormhole.task.route` across incremental push
and pull without weakening project isolation or assignment authorization. A
successful routed push must create one server task under the client task ID
with its selected owner; every validation or authorization failure must create
no task.

## Authorization Contract

The queued `owner_agent_id` is an assignment request, not merely task-create
metadata. An incremental-push task item with an owner therefore requires both
`task.create` and `task.assign`. The local `wormhole.task.route` boundary
preflights both permissions against the exact cached agent-and-project scope so
an offline route cannot enqueue an item that the server must reject.

Create-only task payloads remain valid with only `task.create`. The server
independently checks every queued item, so a stale or tampered local queue
cannot bypass the current token's permissions.

## Atomic Server Write

Add a task-store entry point for client-ID creation with an optional owner. It
opens one transaction, sets the authenticated project RLS context, validates
the optional parent in that project, validates that the optional owner has a
passport in that project, inserts the task and owner together, and commits.
The method never performs create-then-assign as separate transactions. A
missing or cross-project parent/owner returns an error and leaves no row.

Existing `Create` and `CreateWithID` delegate through the same transaction core
with no owner, preserving their behavior. Ordinary `Assign` remains unchanged.

## Payload Fidelity

`syncTaskCreatePayload` decodes `owner_agent_id` and `status` in addition to the
existing create fields. The envelope entity ID and authenticated project are
authoritative; payload `id` and `namespace_id` are not used for scoping. The
server owns `created_at` and `updated_at`.

Task creation always begins at `todo`. A payload status may be absent or
`todo`; any other value is rejected rather than acknowledged and silently
discarded. Title, description, parent, priority, due date, and routed owner are
the complete supported create-state fidelity set.

## End-to-End Proof

Extend the real stdio/daemon/Coordination Server/Postgres integration test to
register the authenticated agent locally, route a task while the server proxy
is offline, and capture the assigned owner. Before reconnect, deliberately
clear only the local task owner while leaving the queued canonical payload
intact. After restart:

1. incremental push must create the Postgres task with that same owner;
2. a later incremental pull must restore the cleared SQLite owner; and
3. the Coordination Server MCP list result must expose the same owner.

Focused integration tests additionally prove a routed owner without
`task.assign`, a cross-project owner, and a non-`todo` status each produce a
per-item error and zero task rows.
