# Task 1 Report: Reconcile Routing and Actor Issuer Boundaries

## Status

DONE

Base: `3519cc4872ea633e45cc9a1323983a491fbc3417`

Commit: `a8e87a2f974fc6d3cb0f1ae4462f8fd60b9a52e8` (`Harley Welsh <git@h4rl3y.xyz>`)

## Files

- Created `internal/types/routing.go`
- Created `internal/types/routing_test.go`
- Modified `internal/types/identity.go`
- Modified `internal/types/identity_test.go`

## Delivered

- Added the exact shared `FabricProfile`, `RemoteBindingKey`, and `FabricBinding`
  records plus `ErrInvalidFabricRoute`.
- Validated canonical workspace and complete remote-key UUID boundaries, profile/instance
  equality, and the binding's exact accepted canonical ref.
- Kept credentials out of `FabricBinding` and `RemoteBindingKey`; `CredentialRef` remains
  profile-only.
- Added structural `ActorScope` validation and exact permission lookup without issuer-side
  authority derivation or assurance rewriting.

## RED/GREEN Evidence

RED command:

```text
go test ./internal/types -run 'Test(FabricBinding|RemoteBindingKey|ActorScope)' -count=1
FAIL: undefined FabricProfile, FabricBinding, FabricModePrivate, and ActorScope
```

GREEN commands:

```text
go test ./internal/types -run 'Test(FabricBinding|RemoteBindingKey|Actor)' -count=1
ok github.com/H4RL33/wormhole/internal/types 0.003s

go test ./internal/types -count=1
ok github.com/H4RL33/wormhole/internal/types 0.004s

go test ./internal/types/... -count=1
ok github.com/H4RL33/wormhole/internal/types 0.004s
ok github.com/H4RL33/wormhole/internal/types/projectstate 0.058s

go vet ./internal/types/...
PASS
```

Repository check:

```text
make check
PASS: build, go vet ./..., required integration tests, race tests, and coverage.
coverage-check total: 81.1%
```

The project-qualified shared-package import scan printed no Wormhole runtime, Core, or MCP
dependencies:

```text
go list -deps ./internal/types/... | rg 'github\\.com/H4RL33/wormhole/internal/(runtime|core|mcp)' && exit 1 || true
```

## Self-review

- The required named table tests independently tamper with every boundary UUID, repository
  identity, canonical ref, and profile/Fabric instance equality.
- Reflection prevents credential-, token-, and secret-shaped fields in binding and remote
  key types.
- `ActorScope.Validate` delegates to `ActorEnvelope.Validate`; it accepts valid historical
  assurance values without upgrading them, while `ValidateLocalAction` remains restricted
  to local assurance.
- The diff is limited to the four Task 1 files; it does not implement Task 2 schemas,
  storage, credential resolution, or issuer derivation.
- `git diff --check` passed before the commit.

## Concerns

The brief's literal dependency scan pattern also matches standard-library paths such as
`internal/runtime`. The command therefore printed Go runtime packages, though it found no
Wormhole `internal/runtime`, `internal/core`, or `internal/mcp` dependency; the
project-qualified scan above verifies the intended package boundary.

## Review Follow-up

Review identified missing independent `FabricProfile.ProfileID` coverage. Commit
`3bc47e3df38ca5a51f68ac9f048d81b4fd47082a` adds only test coverage:

- `TestFabricProfileRequiresCanonicalProfileID` directly rejects an invalid profile ID.
- The binding table now rejects a separately valid but unequal profile ID.

Mutation evidence:

```text
Without FabricProfile.ProfileID validation:
--- FAIL: TestFabricProfileRequiresCanonicalProfileID
Validate() error = <nil>, want ErrInvalidFabricRoute

Without binding/profile profile-ID equality:
--- FAIL: TestFabricBindingRequiresCanonicalWorkspaceAndMatchingInstance/profile_ID_equality
ValidateWithProfile() error = <nil>, want ErrInvalidFabricRoute
```

Restored-validator verification:

```text
go test ./internal/types -run 'Test(FabricBinding|FabricProfile|RemoteBindingKey|ActorScope)' -count=1
ok github.com/H4RL33/wormhole/internal/types 0.004s

go test ./internal/types/... -count=1
ok github.com/H4RL33/wormhole/internal/types 0.004s
ok github.com/H4RL33/wormhole/internal/types/projectstate 0.059s

go vet ./internal/types/...
PASS
```
