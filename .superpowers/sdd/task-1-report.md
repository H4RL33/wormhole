# Task 1 report: Repair Linux stale-socket identity checks

Status: DONE

Commit: `685642dfde76a7ea533524f31f4ed0c0e24b1423`

## Outcome

Linux stale-socket removal now holds an `O_PATH|O_NOFOLLOW|O_CLOEXEC`
descriptor for the checked socket across the dial, hook, quarantine rename,
identity comparison, restoration, and removal sequence. The quarantined
object is compared against the descriptor's device and inode, so an unlinked
socket's inode cannot be immediately reused by a replacement and mistaken for
the original.

## Root cause and TDD evidence

The original implementation retained only `os.FileInfo` metadata after
`Lstat`. Once the original stale socket's listener and path were closed or
unlinked, Linux could recycle the inode. `os.SameFile` then compared only the
stored device/inode pair against the quarantined replacement, allowing a
reused pair to alias the replacement to the original.

The initial prescribed pre-fix stochastic reproducer completed 100 runs
without triggering reuse on this filesystem:

```text
go test ./cmd/wormholed \
  -run 'TestRemoveStaleSocket_(InodeSwapPreservesReplacement|PostQuarantineCollisionPreservesBothPaths)' \
  -count=100
ok github.com/H4RL33/wormhole/cmd/wormholed 0.066s
```

The tests were then strengthened to inspect `/proc/self/fd` from both
replacement hooks and require the checked inode to remain referenced before a
replacement is created. A Unix-socket replacement case was added alongside
the existing regular-file and symlink cases. The required RED run then failed
deterministically for all three cases:

```text
go test ./cmd/wormholed \
  -run TestRemoveStaleSocket_InodeSwapPreservesReplacement \
  -count=20
FAIL: checked socket inode is no longer referenced
```

After the implementation change, the same command passed:

```text
ok github.com/H4RL33/wormhole/cmd/wormholed 0.017s
```

## Diff review

- `cmd/wormholed/wormholed.go`: opens the checked path using the exact Linux
  flags from the plan, holds the descriptor with a deferred close, obtains
  identity using `unix.Fstat`, and passes `Dev` and `Ino` to quarantine.
- `cmd/wormholed/stale_socket_linux.go`: uses `unix.Lstat` after rename and
  compares both device and inode while preserving the existing
  `RENAME_NOREPLACE` restoration and fail-closed error paths.
- `cmd/wormholed/wormholed_test.go`: directly asserts the checked inode is
  held open in both race hooks, preserves the existing content and quarantine
  assertions, and covers regular file, symlink, and Unix-socket replacements.
- `go.mod`: promotes `golang.org/x/sys v0.47.0` from indirect to direct without
  changing its version.
- `go.sum`: removes obsolete `golang.org/x/sys v0.44.0` entries after module
  maintenance; the selected `v0.47.0` checksums are unchanged.

`git diff --check` passed. The only unrelated worktree change is the
pre-existing `.superpowers/sdd/progress.md`; it was not edited or staged by
this task.

## Verification

Focused race behavior:

```text
go test ./cmd/wormholed \
  -run 'TestRemoveStaleSocket_(InodeSwapPreservesReplacement|PostQuarantineCollisionPreservesBothPaths)' \
  -count=100
ok github.com/H4RL33/wormhole/cmd/wormholed 0.106s

go test -race ./cmd/wormholed
ok github.com/H4RL33/wormhole/cmd/wormholed 14.530s
```

Full current gate:

```text
make fmt-check
exit 0

make build
go build -o dist/wormhole ./cmd/wormhole
go build -o dist/wormholed ./cmd/wormholed
go build -o dist/wormhole-server ./cmd/wormhole-server
exit 0

make vet
go vet ./...
exit 0

make integration
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./...
all packages passed

make race
WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./...
all packages passed with no race report

make coverage
total: (statements) 90.2%
exit 0
```

## Completion template

Task sentence: This task is complete when Linux stale-socket removal keeps
the originally checked filesystem object alive through quarantine, rejects
device/inode changes without losing replacements or collisions, passes the
focused repeated/race checks and full gate, and is committed using only the
five scoped files.

Diff serves it: yes. Every production hunk holds or compares socket identity;
every test hunk proves identity lifetime or preserves replacement behavior;
the module hunks make the new direct import explicit.

Decisions made: the implementation follows the task brief verbatim for
descriptor flags, `Fstat`, `Dev`/`Ino`, `Lstat`, dependency version, and
restoration behavior. The deterministic `/proc/self/fd` assertion was chosen
after the allocator-dependent pre-fix reproducer did not fail locally; it
tests the required live-reference invariant directly.

Flagged: the original 100-run reproducer did not exhibit the stochastic inode
reuse on this filesystem. The strengthened RED test did fail deterministically
on the original implementation, and all post-fix checks passed.

Verification: all commands required by Task 1 were run and observed as shown
above.

## Review fixes

Linux descriptor acquisition is now build-tagged and is the first filesystem
observation: `unix.Open` is followed by `unix.Fstat`, and socket mode, device,
and inode all come from that held descriptor. The common removal path does not
import or reference Linux-only constants. The unsupported implementation uses
the same identity/quarantine signatures and retains fail-closed errors.

The new `afterInitialInspection` hook deterministically exercises the former
`Lstat`-to-`Open` replacement window. Both inode-swap tests now also inspect
the held descriptor from `afterQuarantine`, after the rename has completed.

### RED

Before the production fix, the new regression replaced the initially
inspected socket with another stale Unix socket. The old code adopted and
deleted that replacement:

```text
go test ./cmd/wormholed \
  -run TestRemoveStaleSocket_ReplacementAfterInitialInspectionPreserved \
  -count=1
--- FAIL: TestRemoveStaleSocket_ReplacementAfterInitialInspectionPreserved
    removeStaleSocketWithHooks error = <nil>, want replacement rejection
FAIL
```

Both required Darwin commands also failed before the fix:

```text
GOOS=darwin GOARCH=arm64 go build -o /tmp/gateway-task1-darwin ./cmd/wormholed
cmd/wormholed/wormholed.go:201:40: undefined: unix.O_PATH
cmd/wormholed/wormholed.go:219:75: too many arguments in call to quarantineAndRemoveSocket
exit 1

GOOS=darwin GOARCH=arm64 go test -c \
  -o /tmp/gateway-task1-darwin.test ./cmd/wormholed
same compile errors
exit 1
```

### GREEN

```text
go test ./cmd/wormholed \
  -run 'TestRemoveStaleSocket_(ReplacementAfterInitialInspectionPreserved|InodeSwapPreservesReplacement|PostQuarantineCollisionPreservesBothPaths|NonSocketsPreserved)' \
  -count=100
ok github.com/H4RL33/wormhole/cmd/wormholed 0.162s

go test -race ./cmd/wormholed
ok github.com/H4RL33/wormhole/cmd/wormholed 14.509s

GOOS=darwin GOARCH=arm64 go build \
  -o /tmp/gateway-task1-darwin ./cmd/wormholed
exit 0

GOOS=darwin GOARCH=arm64 go test -c \
  -o /tmp/gateway-task1-darwin.test ./cmd/wormholed
exit 0

git diff --check
exit 0
```
