# Atomic Task Route Durability Design

## Goal

Make `wormhole.task.route` preserve its local scheduler behavior without ever
leaving a durable task that lacks its outbound sync queue entry. A successful
route must durably commit one assigned task and one matching task-create queue
entry. A failed route must commit neither.

## Design

`handleTaskRoute` opens one SQLite transaction and keeps it narrow. Within that
transaction it creates the task, registers the generated task ID with the local
scheduler, assigns a matching agent, records that owner through a transaction-
scoped repository method, and enqueues the final task-create payload through
`EnqueueTx`. Only then does it commit.

The scheduler remains an in-memory participant rather than part of the SQLite
transaction. Once scheduler registration succeeds, every later failure removes
the registered scheduler task deterministically before returning. This includes
assignment failure, owner persistence failure, queue insertion failure, and
commit failure. Removal is idempotent so deferred cleanup can cover every exit;
successful commit disables that cleanup.

Registration failure and no-match assignment are tool errors. They leave no
durable task, no queue entry, and no scheduler task. Successful responses retain
the existing task ID, `todo` status, assigned agent, and capability fields.

## Repository and Scheduler Changes

- Add `TaskRepo.AssignTx` and keep `Assign` as its standalone transaction
  wrapper.
- Add an idempotent scheduler task-removal operation used only to compensate an
  uncommitted/failed route.
- Require a sync queue for `task.route`, matching the other durable local write
  tools.

## Tests

- Registration failure: invalid/empty resolved namespace causes scheduler
  registration to fail after task creation; transaction rollback leaves zero
  tasks and queue entries.
- Assignment failure: no eligible agent causes rollback and scheduler cleanup,
  leaving zero tasks and queue entries.
- Post-registration database failure: injected queue insertion failure proves
  task/assignment rollback and scheduler cleanup.
- Success: one assigned local task and exactly one pending `task/create` queue
  entry with the same entity ID and final assigned payload.

Focused local API and scheduler tests run under the race detector. Full required
Postgres integration, build, vet, formatting, and diff checks remain final gates.
