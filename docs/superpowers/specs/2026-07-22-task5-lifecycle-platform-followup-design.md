# Task 5 Lifecycle and Platform Follow-up Design

## Scope

Close the remaining local-runtime lifecycle and stale-socket review findings
without adding dependencies or changing the 1 MiB frame and eight-connection
limits.

## Connection lifecycle

Each admitted connection receives a child context before it is published in the
server's connection map. The map value owns both the connection and its cancel
function. `Close` prevents further admission, cancels each admitted context,
closes each connection, and waits for registered handlers within the existing
one-second bound. Handler teardown repeats cancellation safely and removes its
state. This makes direct `Close`, Serve-context cancellation, blocked tool
handlers, and connection-owned subscriptions follow one lifecycle.

## Platform contract

`wormholed` is supported on Linux only. `Run` checks a build-selected platform
guard before reading configuration or creating runtime state. Non-Linux builds
remain buildable but return an immediate error directing Windows users to WSL
and other users to run on Linux. Their stale-removal stub remains fail-closed and
provides manual recovery guidance; it never deletes a path.

Darwin's no-replace rename is not implemented through a hard-coded raw syscall:
the Go standard library exposes no supported wrapper, the vendored x/sys
implementation uses libc trampolines, and this repository has no macOS runner on
which to verify ABI behavior. CI cross-builds `wormholed` for Darwin to keep the
unsupported path compiling. README, connector, and security documentation state
the support boundary.

## Stale-socket restoration race

The Linux quarantine helper gains a test-only post-quarantine hook that receives
the quarantine path. A deterministic test swaps the checked socket for a file,
lets quarantine move that file, then creates a newer public path. Restoration
must fail with `EEXIST`, preserve the newer public file, and preserve the
displaced inode at the reported quarantine path.

## Logging regression

The real local tool-error logging test returns an HTTP 401 JSON-RPC error whose
message contains the configured bearer token. The response remains an error,
diagnostics contain `[REDACTED]`, and neither the token nor an overlapping-token
suffix may appear.

## Verification

Use RED/GREEN focused race tests for direct Close and the post-quarantine race,
retain the subscription cleanup regression, and run the localapi/wormholed race
suite, current-platform `go test ./...` compile coverage, Darwin cross-build,
build, vet, and the serialized integration suite.
