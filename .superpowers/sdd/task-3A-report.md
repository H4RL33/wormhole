# Stage 3 Task 3A report — ActivityV1 routes and wire records

## Status

Task 3A is complete: complete Activity routing keys, closed ActivityV1,
effective-policy, and receipt codecs are implemented and verified without
adding Activity to portable state or a reducer.

Implementation commits:

```text
0d58bb083dcaeab082e0eedcde1d754eaa25b423 feat: add complete Activity route keys
266d6df05d695fb460e266702ae0dc4202c6a61a feat: freeze ActivityV1 wire records
```

Both implementation commits have author and committer set to
`Harley Welsh <git@h4rl3y.xyz>`.

## Changed files

- `internal/types/routing.go`
- `internal/types/routing_test.go`
- `internal/types/projectstate/activity.go`
- `internal/types/projectstate/activity_test.go`

No schema, migration, MCP, runtime, snapshot, tree, operation, record-value, or
reducer file changed.

## Implemented behavior

- Added the complete six-component `ActivityRouteKey` and the
  source-workspace/activity `ActivityOriginKey` as plain internal values with
  no JSON tags or credential, profile, URL, actor, policy, or cursor authority.
- Route validation requires five canonical non-nil UUIDs plus the exact
  non-empty `refs/heads/` ASCII alphabet, rejects noncanonical Git refs and
  dot path components, and validates both origin UUIDs after the route.
- Added the exact ActivityV1 class and lifecycle constants and record shapes,
  including the event projection, finite effective policy, and receipt.
- Activity, policy, and receipt decoders are closed, single-value, strict,
  canonical-byte codecs. Activity and policy digests are SHA-256 of their
  exact canonical bytes.
- Activity validation binds the event actor and all timestamps to the actor
  envelope, rejects legacy and unknown assurance, enforces exact class/event/
  lifecycle cardinality, and strictly validates all five existing typed event
  payloads without returning record content in errors.
- Policy validation fixes the 30-day ordinary/default terminal age, 10,000-row
  ordinary cap, 365-day maximum terminal age, finite effective retention, and
  JSON-safe positive policy version.
- Receipt validation enforces canonical evidence digests, UUID, UTC acceptance
  time, and JSON-safe positive sequence and policy version.
- Hard-coded canonical bytes and SHA-256 goldens cover ordinary Activity,
  lifecycle Activity, and both finite-policy boundaries.

## RED evidence

Route tests were written before the route types existed:

```text
go test ./internal/types -run 'TestActivity(Route|Origin)Key' -count=1
# github.com/H4RL33/wormhole/internal/types
internal/types/routing_test.go:140:11: undefined: ActivityRouteKey
internal/types/routing_test.go:154:16: undefined: ActivityRouteKey
...
FAIL github.com/H4RL33/wormhole/internal/types [build failed]
```

The exact ASCII branch-ref cases added during self-review failed against the
initial minimal implementation before it was amended into the route commit:

```text
go test ./internal/types -run 'TestActivity(Route|Origin)Key' -count=1
--- FAIL: TestActivityRouteKeyRequiresCompleteBindingAndCanonicalRef (0.00s)
    --- FAIL: .../non-ASCII
        Validate() error = <nil>, want ErrInvalidFabricRoute
    --- FAIL: .../outside_exact_alphabet
        Validate() error = <nil>, want ErrInvalidFabricRoute
FAIL github.com/H4RL33/wormhole/internal/types
```

Codec and policy tests were written before any Activity record or codec
existed:

```text
go test ./internal/types/projectstate -run 'Test(Activity|EffectiveActivityPolicy)' -count=1
# github.com/H4RL33/wormhole/internal/types/projectstate
internal/types/projectstate/activity_test.go:60:29: undefined: ActivityV1
internal/types/projectstate/activity_test.go:65:18: undefined: ActivityOrdinaryV1
internal/types/projectstate/activity_test.go:67:11: undefined: ActivityEventProjectionV1
...
FAIL github.com/H4RL33/wormhole/internal/types/projectstate [build failed]
```

A missing-schema closed record also produced causal RED during self-review,
showing that absence had initially been misclassified as a future version:

```text
go test ./internal/types/projectstate -run 'TestActivityV1RejectsUnknownNonCanonicalAndForgedAttribution' -count=1
--- FAIL: TestActivityV1RejectsUnknownNonCanonicalAndForgedAttribution/missing_fields
    DecodeActivity error = projectstate: unknown activity version: activity,
    want ErrInvalidActivity
FAIL github.com/H4RL33/wormhole/internal/types/projectstate
```

## GREEN evidence

Focused route contract:

```text
go test ./internal/types -run 'TestActivity(Route|Origin)Key' -count=1
ok github.com/H4RL33/wormhole/internal/types 0.004s
```

Focused Activity, policy, receipt, and isolation contract:

```text
go test ./internal/types/projectstate -run 'Test(Activity|EffectiveActivityPolicy)' -count=1
ok github.com/H4RL33/wormhole/internal/types/projectstate 0.005s
```

Fresh full shared-types verification after the final implementation:

```text
go test ./internal/types/... -count=1
ok github.com/H4RL33/wormhole/internal/types 0.004s
ok github.com/H4RL33/wormhole/internal/types/projectstate 0.061s
```

Race and vet verification:

```text
go test -race ./internal/types/... -count=1
ok github.com/H4RL33/wormhole/internal/types 1.024s
ok github.com/H4RL33/wormhole/internal/types/projectstate 1.306s

go vet ./internal/types/...
# exit 0, no output
```

Formatting and range verification:

```text
git diff --check c8735f8..266d6df
# exit 0, no output
```

## Self-review

- Compared the exported records, field order, JSON tags, constants, and
  function signatures line by line with amendment sections 4 through 5.3 and
  the Task 3A brief.
- Strengthened the existing branch-ref predicate with the amendment's exact
  ASCII alphabet and explicit dot-component check; `RemoteBindingKey` and
  `FabricBinding.RemoteKey` remain unchanged.
- Confirmed closed decoding recursively rejects unknown payload fields,
  multiple values, noncanonical raw payload maps, member reordering,
  whitespace, missing fields, unsafe integers, and future versions.
- Confirmed every validation error is a fixed sentinel or safe static wrapper;
  no error includes Activity bytes, payload/note, actor envelope, policy bytes,
  or any route value.
- Reflected `Snapshot`, `File`, `RecordValueV1`, `OperationV1`, and the
  `ApplyOperation` signature to retain explicit zero-coupling evidence for
  portable state and reducers.
- Confirmed the task range contains only the four authorized Task 3A files and
  no Task 3B or later implementation.

## Concerns

No known functional concern or blocker.
