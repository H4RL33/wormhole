# Stage 3 Task 5 report — exact GitHub observation

## Status

DONE

Implementation commit: `23710a7` (`feat: observe exact canonical GitHub commits`).

Author and committer: `Harley Welsh <git@h4rl3y.xyz>`.

## Task sentence

Task 5 is complete when Fabric can independently resolve one canonical GitHub branch
exactly once by numeric provider repository ID, pin all later commit/tree/blob reads to
that result, import only the bounded validated `.wormhole/` tree, and use only an
explicit Fabric-server credential without redirects, ambient credentials, local Git,
or credential leakage.

## Root and approved-design fit

- Work began at exact Task 4 base `8ce6f3d393cbcb5adb22ffa8924d16e8e3eeaa1f`
  on `feat/multifabric-private-identity` in the assigned worktree.
- The implementation consumes the existing Task 4 `RefObservation`, the canonical
  `types.RepositoryIdentity`, and `projectstate.Tree`; it defines no parallel portable
  stream, repository, snapshot, digest, decoder, or canonicalization semantics.
- The observer boundary is provider-neutral. The GitHub-v1 adapter uses the immutable
  numeric repository ID and the exact approved request order, while the fake supplies
  deterministic mutex-protected fixtures and an ordered call log.
- Fabric owns credential resolution. A non-empty fourth `ObserveRef` argument is the
  binding's `observer_credential_ref`; public observations leave it empty and never
  read or send a credential. Gateway and callers have no raw credential input.
- The implementation ends at Fabric construction/configuration. It adds no Task 6
  MCP/public protocol, ActivityV1 behavior, local Git access, credential-helper access,
  Code Graph behavior, datastore, framework, or unrelated cleanup.

## Files

Created:

- `internal/core/git/observer.go`
- `internal/core/git/fake_observer.go`
- `internal/core/git/github_observer.go`
- `internal/core/git/github_observer_test.go`

Modified:

- `internal/types/config.go`
- `internal/types/config_test.go`
- `cmd/fabric/main.go`
- `cmd/fabric/main_test.go`

Evidence:

- `.superpowers/sdd/stage3-task-5-report.md`

The generated `.superpowers/sdd/stage3-task-5-brief.md` remained ignored, untouched,
and unstaged.

## Strict RED evidence

The exact required RED command was run after writing the fake-server, consistency,
credential, redirect, bounds, and fake-observer tests and before production code:

```text
$ go test ./internal/core/git ./cmd/fabric -run 'Test(GitHubObserver|FakeObserver)' -count=1
# github.com/H4RL33/wormhole/cmd/fabric [github.com/H4RL33/wormhole/cmd/fabric.test]
cmd/fabric/main_test.go: undefined: types.GitHubObserverConfig
cmd/fabric/main_test.go: undefined: privateGitCredentialSource
cmd/fabric/main_test.go: undefined: errPrivateGitCredentialUnavailable
cmd/fabric/main_test.go: cfg.GitHubObserver undefined
cmd/fabric/main_test.go: undefined: newCanonicalGitObserver
cmd/fabric/main_test.go: undefined: coregit.CanonicalGitObserver
# github.com/H4RL33/wormhole/internal/core/git [github.com/H4RL33/wormhole/internal/core/git.test]
internal/core/git/github_observer_test.go: undefined: GitCredentialSource
internal/core/git/github_observer_test.go: undefined: GitHubObserver
internal/core/git/github_observer_test.go: undefined: NewGitHubObserver
internal/core/git/github_observer_test.go: undefined: ErrGitObservation
internal/core/git/github_observer_test.go: undefined: FakeObserver
FAIL github.com/H4RL33/wormhole/internal/core/git [build failed]
FAIL github.com/H4RL33/wormhole/cmd/fabric [build failed]
FAIL
```

This was the expected absent-implementation compile RED, not a fixture, network, or
database failure.

The first focused implementation GREEN exposed three security/consistency cases worth
making explicit. Their tests were added first and the same focused command returned
RED:

```text
$ go test ./internal/core/git ./cmd/fabric -run 'Test(GitHubObserver|FakeObserver|PrivateGitCredential)' -count=1
--- FAIL: TestGitHubObserverPathEscapesNestedRef
    nested branch slash was double-escaped as %252F instead of one %2F path segment
--- FAIL: TestGitHubObserverCredentialFailureDoesNotExposeSourceDetail
    credential-source error detail reached the observer error
--- FAIL: TestPrivateGitCredentialRejectsIncompleteOrUnsafeConfiguration
    whitespace-bearing credential configuration was accepted
FAIL
```

The fixes preserved a single escaped ref segment through `URL.RawPath`, collapsed
credential-source detail at the observer boundary, and fail closed on incomplete,
whitespace-bearing, or control-bearing Fabric credential configuration.

The first repository-wide gate produced one diagnostic contract RED:

```text
$ make check
--- FAIL: TestAlphaContractEnvironmentAndPaths
    environment inventory contained newly introduced WORMHOLE_GITHUB_* names that are
    not in the frozen alpha contract
FAIL
make: *** [integration] Error 1
```

Root cause: the alpha environment inventory is contract-owned and Task 15 owns its
update; Task 5's exact file scope does not include the contract manifest. The live env
lookups were removed rather than bypassing or broadening the frozen contract. The
Fabric config type and constructor remain explicit and injectable, and `LoadConfig`
defaults to public observation without importing ambient GitHub credentials.

The focused contract regression then passed:

```text
$ go test ./cmd/wormhole -run '^TestAlphaContractEnvironmentAndPaths$' -count=1
ok github.com/H4RL33/wormhole/cmd/wormhole
```

## Focused GREEN and implementation

The exact required focused command passed after each production fix:

```text
$ go test ./internal/core/git ./cmd/fabric -run 'Test(GitHubObserver|FakeObserver|PrivateGitCredential)' -count=1
ok github.com/H4RL33/wormhole/internal/core/git 0.435s
ok github.com/H4RL33/wormhole/cmd/fabric 0.005s
```

- `CanonicalGitObserver` and `GitCredentialSource` match the approved interfaces; the
  deterministic `FakeObserver` clones fixture and result bytes, sorts trees, records
  calls, honors context cancellation, and is safe for concurrent use.
- `GitHubObserver` validates the GitHub repository identity and canonical branch, reads
  `/repositories/N`, resolves the ref once, and pins the commit, tree, and every selected
  blob read to the verified response chain. It never accepts a caller-supplied commit
  and never re-reads the ref.
- The dedicated client has a finite timeout and always returns
  `http.ErrUseLastResponse` from `CheckRedirect`; every 3xx is rejected before follow for
  both public and credentialed observations.
- The API base rejects userinfo, query, fragment, unsafe schemes, and unclean paths.
  Every request is rebuilt from the exact configured scheme/host/port and cannot carry
  an authorization header without a successfully resolved non-empty Fabric credential
  reference.
- Provider repository, ref, commit, tree, and blob identities must agree exactly.
  Trees reject truncation, more than 10,000 entries, invalid `.wormhole/` paths,
  symlinks, submodules, invalid entry modes, blobs over 1 MiB, and aggregate canonical
  bytes over 16 MiB. Only `.wormhole/` blobs are fetched.
- Imported paths strip the `.wormhole/` prefix, blob bytes and reported sizes must
  agree, and the assembled canonical tree passes `DecodeTree`, `Validate`, and
  `DigestTree`; its digest must equal the snapshot's embedded digest.
- Fabric configuration defaults to the official public GitHub API origin and an empty
  credential. Constructor wiring validates the reference/credential pair at startup,
  resolves only the exact configured reference, and ignores ambient `GITHUB_TOKEN`.

All required named tests are present, including a moving-ref fixture whose commit B has
different valid `.wormhole/` bytes, plus nested-ref escaping, credential-error
redaction, inconsistent provider objects, unsafe API origins, deep-clone behavior, and
configuration hardening regressions.

## Fresh affected, race, security, and repository verification

The full affected packages passed:

```text
$ go test ./internal/core/git ./internal/types ./cmd/fabric -count=1
ok github.com/H4RL33/wormhole/internal/core/git 6.572s
ok github.com/H4RL33/wormhole/internal/types 0.004s
ok github.com/H4RL33/wormhole/cmd/fabric 0.153s
```

The observer, fake, and credential boundary passed the race detector:

```text
$ go test -race ./internal/core/git ./cmd/fabric -run 'Test(GitHubObserver|FakeObserver|PrivateGitCredential)' -count=1
ok github.com/H4RL33/wormhole/internal/core/git 5.031s
ok github.com/H4RL33/wormhole/cmd/fabric 1.032s
```

Static and patch checks passed:

```text
$ go vet ./internal/core/git ./internal/types ./cmd/fabric
PASS

$ git diff --check
PASS
```

The corrected repository-wide gate completed successfully on the same production
tree. It included mandatory integration tests, the full repository race suite, and the
coverage gate:

```text
$ make check
build: PASS
vet: PASS
mandatory integration: PASS
repository race: PASS
coverage: PASS (80.8%)
```

## Diff and security scope scan

- Compared the working tree to exact base `8ce6f3d`; only the eight brief-owned source
  and test files above plus this report are intended for the task commit.
- `git diff --check` is clean. The four untracked production/test files are exactly the
  brief's four creates; no unexpected untracked file is present.
- Searched production changes for ambient `GITHUB_TOKEN`, `WORMHOLE_GITHUB_*`, local
  process execution, Git credential helpers, raw authorization logging, ActivityV1,
  Code Graph, and new MCP/public-protocol behavior. None is present.
- The request-log tests assert the numeric repository endpoint/order, absence of source
  blob fetches, absence of public authorization, and zero redirected destination
  requests. Credential-failure tests assert that source details, references, and secret
  material are absent from returned errors and that no network request occurs.
- No pre-existing tracked or user-owned change was overwritten. The generated brief is
  still ignored and will not be force-added with this report.

## Self-review

- Re-read the exact Task 5 brief, plan global constraints, canonical architecture/RFC
  boundary, implementation rules, Task 4 observation contract, and existing
  configuration/error conventions after implementation.
- Compared every exported interface and fake method with the verbatim brief. Confirmed
  the fourth observer argument remains only the Fabric-side credential reference.
- Walked the HTTP flow request by request and confirmed one ref resolution, exact
  repository/ref/commit/tree/blob equality checks, a dedicated no-redirect client, and
  no mutable repository-name or caller-commit route.
- Reviewed every error path around credential resolution. Raw credential values and
  credential-source errors do not flow into observer errors, diagnostics, events,
  results, or request logs; authorization exists only in outbound request headers to
  the configured origin.
- Reviewed every provider-boundary count, path, mode, size, encoding, and canonical
  decode/validation/digest check, including exact-limit acceptance and over-limit
  rejection behavior.
- Confirmed fake fixture/call state and test HTTP fixture state pass focused race tests.
- Confirmed the implementation consumes portable ProjectState semantics unchanged and
  adds no source-body persistence or operational-activity behavior.
- Confirmed Task 6 remains the owner of observer consumption in public/MCP protocol
  wiring; Task 15 remains the owner of a frozen environment-contract publication.

## Concerns

The Fabric credential configuration is explicit and injectable, its constructor path
is wired and startup-validated, and the binary defaults safely to public observation.
However, this exact-scope task does not publish new `WORMHOLE_GITHUB_*` environment
names because doing so fails the frozen alpha inventory and Task 15 owns that contract
update. A later contract-owning task must publish the live configuration surface before
operators can enable private GitHub observation through `LoadConfig` alone.

## Independent-review fix — live Fabric credential publication

Status: DONE. The one Important review finding was reproduced and fixed in
`ea83aa7` (`fix(fabric): publish GitHub observer config`). The earlier concern above is
resolved by the user-approved Task 5 ownership clarification.

### Root cause and design fit

Production `main` calls only `types.LoadConfig`, but the initial Task 5 implementation
hardcoded the public GitHub API base and left both observer credential fields empty.
Private observation was therefore available only to programmatically constructed test
configs. The prior attempt to publish the live names exposed a real sequencing
contradiction because the frozen alpha inventory had not authorized them.

The approved correction keeps the existing security boundary and publishes exactly
three Fabric-process values:

- `WORMHOLE_GITHUB_API_BASE_URL`
- `WORMHOLE_GITHUB_CREDENTIAL_REF`
- `WORMHOLE_GITHUB_CREDENTIAL`

`LoadConfig` uses the repository's existing explicit `getEnv` convention. The existing
Fabric startup constructor remains the fail-closed validator: reference and credential
must both be empty for public observation or both be present and free of leading,
trailing, internal whitespace and control characters. The GitHub observer remains the
only component that turns a successfully resolved server credential into an outbound
authorization header. `GITHUB_TOKEN`, Gateway credentials, local Git, and credential
helpers remain outside the path.

The implementation plan now states that Task 5 owns runtime publication plus the alpha
environment inventory and that Task 15 only documents/verifies the already-shipped
surface. Task 5's file list, tests, GREEN command, and commit command were updated
accordingly; no Task 6 protocol work moved into this fix.

### Files changed

- `internal/types/config.go`: load the exact three Fabric-only environment values.
- `internal/types/config_test.go`: prove safe public defaults, ambient-token exclusion,
  and exact explicit loading.
- `cmd/fabric/main_test.go`: prove the live credential resolves only its configured
  reference and incomplete/whitespace/control-bearing live pairs fail closed.
- `cmd/wormhole/contract_manifest_test.go`: assert the exact observer environment
  surface and explicit exclusion of ambient `GITHUB_TOKEN`.
- `docs/contracts/alpha-contract.json`: publish the three sorted live names.
- `docs/superpowers/plans/2026-07-28-multifabric-identity-trial-implementation-plan.md`:
  resolve Task 5/Task 15 runtime ownership.
- `.superpowers/sdd/stage3-task-5-report.md`: record this fix wave.

The ignored review and brief files were read as inputs but remained unstaged and
uncommitted.

### Exact RED evidence

Tests were added before the production configuration or inventory changed:

```text
$ go test ./internal/types ./cmd/fabric ./cmd/wormhole -run 'Test(LoadConfig_PrivateGitCredential|PrivateGitCredential(ResolvesOnlyConfiguredFabricReference|EnvironmentFailsClosed)|AlphaContractFabricGitHubObserverEnvironment)' -count=1
--- FAIL: TestLoadConfig_PrivateGitCredential/explicit_Fabric_server_configuration
    LoadConfig did not load the explicit Fabric-server GitHub observer configuration
--- FAIL: TestPrivateGitCredentialResolvesOnlyConfiguredFabricReference
    ReadServerCredential did not return the configured Fabric-server credential:
    fabric: private Git credential unavailable
--- FAIL: TestPrivateGitCredentialEnvironmentFailsClosed/reference_only
--- FAIL: TestPrivateGitCredentialEnvironmentFailsClosed/credential_only
--- FAIL: TestPrivateGitCredentialEnvironmentFailsClosed/reference_internal_whitespace
--- FAIL: TestPrivateGitCredentialEnvironmentFailsClosed/credential_internal_whitespace
--- FAIL: TestPrivateGitCredentialEnvironmentFailsClosed/reference_control
--- FAIL: TestPrivateGitCredentialEnvironmentFailsClosed/credential_control
    each invalid case produced observer=true error=<nil>
--- FAIL: TestAlphaContractFabricGitHubObserverEnvironment
    Fabric GitHub observer environment is missing WORMHOLE_GITHUB_API_BASE_URL
    Fabric GitHub observer environment is missing WORMHOLE_GITHUB_CREDENTIAL
    Fabric GitHub observer environment is missing WORMHOLE_GITHUB_CREDENTIAL_REF
FAIL
```

The failures were the expected missing-behavior RED: `LoadConfig` ignored the new
surface, causing invalid live pairs to collapse to an apparently valid public config,
and the contract had not published the names.

After the minimal `LoadConfig` wiring, the configuration behavior passed:

```text
$ go test ./internal/types ./cmd/fabric -run 'Test(LoadConfig_PrivateGitCredential|PrivateGitCredential(ResolvesOnlyConfiguredFabricReference|EnvironmentFailsClosed))' -count=1
ok github.com/H4RL33/wormhole/internal/types 0.003s
ok github.com/H4RL33/wormhole/cmd/fabric 0.004s
```

The unchanged frozen inventory then gave the expected contract-specific RED:

```text
$ go test ./cmd/wormhole -run 'TestAlphaContract(EnvironmentAndPaths|FabricGitHubObserverEnvironment)' -count=1
--- FAIL: TestAlphaContractEnvironmentAndPaths
    production environment contained WORMHOLE_GITHUB_API_BASE_URL,
    WORMHOLE_GITHUB_CREDENTIAL, and WORMHOLE_GITHUB_CREDENTIAL_REF; manifest did not
--- FAIL: TestAlphaContractFabricGitHubObserverEnvironment
    all three required Fabric GitHub observer names were missing
FAIL
```

This proved the inventory update was required rather than an unrelated documentation
edit. Adding exactly the three sorted names produced GREEN:

```text
$ go test ./cmd/wormhole -run 'TestAlphaContract(EnvironmentAndPaths|FabricGitHubObserverEnvironment)' -count=1
ok github.com/H4RL33/wormhole/cmd/wormhole 0.086s
```

### Full affected, security, race, and contract verification

The complete affected packages passed:

```text
$ go test ./internal/core/git ./internal/types ./cmd/fabric ./cmd/wormhole -count=1
ok github.com/H4RL33/wormhole/internal/core/git 5.918s
ok github.com/H4RL33/wormhole/internal/types 0.013s
ok github.com/H4RL33/wormhole/cmd/fabric 0.178s
ok github.com/H4RL33/wormhole/cmd/wormhole 6.312s
```

The focused race/security matrix passed. It covers public/no-authorization behavior,
private exact-reference resolution, credential-error redaction, credentialed and public
redirect refusal, unsafe API bases, live loading, invalid pairs, and both inventory
assertions:

```text
$ go test -race ./internal/core/git ./internal/types ./cmd/fabric ./cmd/wormhole -run 'Test(GitHubObserver(PublicSendsNoAuthorization|PrivateUsesServerCredentialOnly|CredentialFailureDoesNotExposeSourceDetail|RejectsCredentialedRedirectBeforeFollow|RejectsCrossOriginRedirect|RejectsUnsafeAPIBaseURL)|LoadConfig_PrivateGitCredential|PrivateGitCredential|AlphaContract(EnvironmentAndPaths|FabricGitHubObserverEnvironment))' -count=1
ok github.com/H4RL33/wormhole/internal/core/git 1.050s
ok github.com/H4RL33/wormhole/internal/types 1.027s
ok github.com/H4RL33/wormhole/cmd/fabric 1.031s
ok github.com/H4RL33/wormhole/cmd/wormhole 1.569s
```

The alpha contract stability script passed without rewriting the inventory:

```text
$ .github/scripts/check-contract-manifest.sh
Contract inventory is deterministic and unchanged.
```

The repository-wide gate passed on the fix tree:

```text
$ make check
build: PASS
vet: PASS
mandatory integration: PASS
repository race: PASS
coverage: PASS (80.9%)
```

### Diff, scope, and self-review

- Compared the fix with `a674f73`; the implementation commit contains exactly the six
  runtime-config, tests, alpha inventory, and approved-plan files listed above. This
  report is the only follow-up evidence file.
- `git diff --check` passed before the fix commit. The environment names are sorted in
  the inventory and the contract determinism script left the file byte-identical.
- Production-only scans found no `GITHUB_TOKEN`, authorization logging, local process
  execution, credential-helper access, ActivityV1, Code Graph, or new MCP/public
  protocol behavior in the changed runtime files.
- Re-read the review finding against the production `main` call chain and confirmed the
  fix closes the actual operator path rather than only adjusting a test seam.
- Confirmed explicit empty reference/credential retains the public default and existing
  observer tests prove it reads no credential and sends no authorization header.
- Confirmed incomplete, leading/trailing/internal whitespace, and control-bearing pairs
  fail before database open; unsafe API bases still fail in the existing GitHub
  constructor validation.
- Confirmed neither configuration errors nor credential-source errors include the raw
  configured reference, server credential, source detail, or ambient token.
- Confirmed the plan's Task 5 file list, test requirements, GREEN gates, and commit scope
  now agree with the shipped runtime surface, while Task 15 is explicitly documentation
  and verification only for this already-live configuration.

### Concerns

None. The independent-review Important finding and the Task 5/Task 15 ownership
contradiction are resolved without broadening the observer or protocol boundary.
