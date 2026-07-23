# Task 2 report — preserve local namespace ownership

## Status

Implemented and verified. The sync local-apply upserts now reject an ID already
owned by another namespace, preserving the existing row and its namespace.

## Files changed

- `internal/runtime/localstore/task_repo.go`
- `internal/runtime/localstore/kb_repo.go`
- `internal/runtime/localstore/cross_namespace_test.go`

## Decisions

- Added the package sentinel `ErrNamespaceCollision` with the required text.
- Both upserts query the existing row's `namespace_id` inside their write
  transaction before writing. A different namespace returns the sentinel.
- Removed `namespace_id = excluded.namespace_id` from both conflict-update
  clauses, making namespace ownership immutable while retaining same-namespace
  updates.
- Added task and KB regression cases: collision returns the sentinel, namespace
  A remains unchanged, namespace B cannot retrieve the ID, and same-namespace
  updates succeed.

## RED

The first test-only change intentionally referenced the new sentinel before it
existed. The exact requested command therefore failed to compile, confirming
the missing public error interface:

```text
go test ./internal/runtime/localstore -run 'TestUpsert(Task|Article)_CrossNamespaceIDCollisionRejected' -count=1
```

Decisive output before production changes:

```text
# github.com/H4RL33/wormhole/internal/runtime/localstore [github.com/H4RL33/wormhole/internal/runtime/localstore.test]
internal/runtime/localstore/cross_namespace_test.go:35:21: undefined: ErrNamespaceCollision
internal/runtime/localstore/cross_namespace_test.go:83:21: undefined: ErrNamespaceCollision
FAIL	github.com/H4RL33/wormhole/internal/runtime/localstore [build failed]
FAIL
```

To demonstrate the original runtime defect as well, I reran that exact test
against the pre-fix `2c79aba` task and KB repository files through a temporary
Go overlay. The overlay provided a test-only `ErrNamespaceCollision` so the
test could compile without adding production behavior. The decisive output
shows both cross-namespace upserts returned `nil` instead of rejecting the
collision:

```text
--- FAIL: TestUpsertTask_CrossNamespaceIDCollisionRejected (0.00s)
    cross_namespace_test.go:38: upsert collision error = <nil>, want ErrNamespaceCollision
--- FAIL: TestUpsertArticle_CrossNamespaceIDCollisionRejected (0.00s)
    cross_namespace_test.go:86: upsert collision error = <nil>, want ErrNamespaceCollision
FAIL
FAIL	github.com/H4RL33/wormhole/internal/runtime/localstore	0.009s
FAIL
```

## GREEN / verification

Focused regression command:

```text
go test ./internal/runtime/localstore -run 'TestUpsert(Task|Article)_CrossNamespaceIDCollisionRejected' -count=1
ok  	github.com/H4RL33/wormhole/internal/runtime/localstore	0.009s
```

Required race suite:

```text
go test -race ./internal/runtime/localstore -count=1
ok  	github.com/H4RL33/wormhole/internal/runtime/localstore	1.302s
```

Required dependent suites:

```text
go test ./internal/runtime/sync ./internal/runtime/localapi -count=1
ok  	github.com/H4RL33/wormhole/internal/runtime/sync	4.006s
ok  	github.com/H4RL33/wormhole/internal/runtime/localapi	0.401s
```

`git diff --check` also exited successfully.

## Concerns

None for the requested scope. The existing `.superpowers/sdd/progress.md`
modification was left untouched as instructed.

## Review remediation (namespace scoping and complete record assertions)

### Files changed

- `internal/runtime/localstore/task_repo.go`
- `internal/runtime/localstore/kb_repo.go`
- `internal/runtime/localstore/cross_namespace_test.go`

### Changes

- Changed both collision probes to query only for a conflicting owner with
  `WHERE id = ? AND namespace_id <> ?`, binding the requested namespace in
  the existing write transaction as required by RFC-0003 §7.2/LR3.
- Strengthened task collision coverage to preserve and compare every stored
  field (including namespace, description, parent/owner pointers, status,
  priority, due date, and timestamps) and to verify all same-namespace
  mutable updates plus retained namespace.
- Strengthened KB coverage to preserve and compare title, body, frontmatter,
  author, namespace, and timestamps, using distinct attempted collision
  values and complete same-namespace update assertions.
- The due-date assertions exposed that SQLite stores `due_by` (`TEXT`) as a
  Go time string. `scanTaskRows` now parses that value, including the
  driver's optional monotonic-clock suffix; a regression test covers it.

### Commands and results

The new monotonic-clock due-date regression was RED before the parser change:

```text
go test ./internal/runtime/localstore -run TestUpsertTask_PreservesDueByWithMonotonicClock -count=1
--- FAIL: TestUpsertTask_PreservesDueByWithMonotonicClock (0.00s)
    cross_namespace_test.go:161: UpsertTask: localstore/task: upsert: parse due_by: parsing time "... m=+...": extra text: " m=+..."
FAIL
FAIL	github.com/H4RL33/wormhole/internal/runtime/localstore	0.006s
FAIL
```

Final focused collision and due-date regression coverage:

```text
go test ./internal/runtime/localstore -run 'TestUpsert(Task|Article)_CrossNamespaceIDCollisionRejected|TestUpsertTask_PreservesDueByWithMonotonicClock' -count=1
ok  	github.com/H4RL33/wormhole/internal/runtime/localstore	0.012s
```

Required race suite:

```text
go test -race ./internal/runtime/localstore -count=1
ok  	github.com/H4RL33/wormhole/internal/runtime/localstore	1.316s
```

Required dependent suites:

```text
go test ./internal/runtime/sync ./internal/runtime/localapi -count=1
ok  	github.com/H4RL33/wormhole/internal/runtime/sync	3.971s
ok  	github.com/H4RL33/wormhole/internal/runtime/localapi	0.399s
```

`git diff --check` exited successfully.
