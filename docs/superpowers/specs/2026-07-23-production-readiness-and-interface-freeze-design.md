# Production Readiness and Interface Freeze Design

**Date:** 2026-07-23
**Status:** Approved design
**Scope:** Alpha production-readiness controls and preparation for a future
`v0.3.0-beta.1` compatibility freeze

## Objective

Give Wormhole an enforceable quality and release baseline while alpha feature
development continues. This work prepares the repository for a future beta
interface freeze but does not create a beta release, beta tag, or active beta
compatibility commitment.

The work also makes two pre-beta hard renames:

- `wormholed` becomes `gatewayd`;
- `wormhole-server` becomes `fabric`.

Neither former name receives a compatibility alias.

## Current State

The repository already has a comprehensive GitHub Actions workflow covering
formatting, builds, PostgreSQL integration tests, race tests, coverage, and
migration rollback. It does not yet provide a production-ready control plane:

- `main` has no branch protection or repository ruleset;
- recent CI runs fail in Linux stale-socket race tests;
- all checks run in one serial job, so an early failure hides later results;
- third-party Actions use mutable major-version tags;
- GitHub dependency and secret-scanning protections are disabled;
- no release workflow, signed artifacts, attestations, SBOMs, or container
  publication exist;
- public interfaces are not inventoried in a compatibility manifest.

Protection must not be enabled until the checks intended to become mandatory
have succeeded on `main`.

## Naming and Architecture

The user-facing architecture becomes:

```text
Harness -> wormhole mcp -> Gateway -> Fabric -> PostgreSQL
                         |
                       SQLite
```

The binaries are:

| Binary | Responsibility |
|---|---|
| `wormhole` | Setup, administration, profile management, and MCP stdio bridge |
| `gatewayd` | Per-user local Gateway, SQLite replica, local MCP endpoint, and durable sync queue |
| `fabric` | Shared coordination Fabric backed by PostgreSQL and pgvector |

The rename is a hard alpha cut:

- rename command directories, Makefile targets, tests, workflow references,
  Compose configuration, documentation, and examples atomically;
- do not ship old binaries, aliases, symlinks, or deprecation shims;
- retain `WORMHOLE_*` environment variables and `~/.wormhole` paths because
  they configure the Wormhole system rather than one binary;
- publish the Fabric image as `ghcr.io/h4rl33/wormhole-fabric`.

## Repository Freeze

The repository freeze controls quality and publication, not alpha feature
scope.

`main` will:

- accept changes through pull requests only;
- require every production-readiness check to pass;
- reject force pushes and branch deletion;
- resolve review conversations before merge;
- not require an external approval while there is only one maintainer;
- require one approval when a second maintainer is formally designated;
- permit the repository owner an emergency bypass.

An emergency bypass requires a follow-up GitHub issue recording the reason,
impact, verification debt, and corrective action. This is a policy obligation
and audit trail, not an automated issue created with broad workflow write
permissions.

Repository enforcement is applied only after the required checks have run
successfully on `main`. The configured rules are then queried through the
GitHub API and compared with the intended policy.

## Continuous Integration

The monolithic workflow will be split into independently visible required
checks. Exact workflow-file boundaries may follow GitHub Actions reuse
constraints, but the required check names remain stable.

### Required checks

1. **Formatting and static analysis**
   - `make fmt-check`
   - `make vet`
   - workflow and configuration validation

2. **Build**
   - build `wormhole`, `gatewayd`, and `fabric`;
   - cross-compile every supported release target;
   - preserve the unsupported-platform behavior tests where applicable.

3. **Unit and PostgreSQL integration**
   - start PostgreSQL 16 with pgvector;
   - apply all migrations from an empty database;
   - run with `WORMHOLE_INTEGRATION_REQUIRED=1`.

4. **Race**
   - run the repository race target against migrated PostgreSQL.

5. **Coverage**
   - enforce the existing merged statement-coverage floor of 90%;
   - upload the coverage profile for diagnosis.

6. **Migration verification**
   - migrate an empty database fully up;
   - migrate fully down;
   - exercise an upgrade from the latest published alpha baseline,
     `v0.2.4-alpha`, to the current schema.

7. **Dependency and vulnerability security**
   - scan Go dependencies and built artifacts for known vulnerabilities;
   - detect accidental credential or secret commits;
   - validate that GitHub Actions are pinned to immutable commit SHAs.

8. **Compatibility drift**
   - generate the current public-interface inventory;
   - compare it to the checked-in alpha manifest;
   - report intentional additions during alpha without treating them as a beta
     compatibility violation.

The existing Linux socket-race failures must be root-caused and repaired before
these checks become mandatory.

### Workflow security and reliability

- Pin third-party Actions to full commit SHAs and retain a version comment for
  maintainability.
- Keep default workflow permissions read-only.
- Grant write permissions only to the individual release jobs that need them.
- Use concurrency groups to cancel superseded pull-request CI and prevent
  duplicate publication for one tag.
- Add timeouts to prevent stuck jobs consuming runners indefinitely.
- Use dependency caching only when cache keys include the relevant lock inputs.
- Make diagnostic artifacts available even when a check fails.
- Keep tests deterministic; do not hide races with retries or blanket
  `continue-on-error`.

## GitHub Security Controls

Enable supported repository controls:

- Dependabot security updates;
- secret scanning;
- secret-scanning push protection;
- secret validity checks where available;
- immutable Actions pinning policy.

Controls unavailable for the repository plan or visibility are recorded as
unavailable rather than silently omitted. Enabling a GitHub setting is followed
by a read-back verification through the API.

## Release Pipeline

Release publication is triggered only by an annotated version tag matching the
project prerelease policy. Installing this workflow during alpha does not
authorize or create `v0.3.0-beta.1`.

### Artifacts

Each release produces:

- Linux `amd64` and `arm64` archives containing `wormhole`, `gatewayd`, and
  `fabric`;
- a multi-architecture Fabric image at
  `ghcr.io/h4rl33/wormhole-fabric`;
- SHA-256 checksum manifests;
- SPDX or CycloneDX SBOMs;
- GitHub build-provenance attestations;
- keyless Sigstore signatures;
- generated GitHub release notes.

### Publication gates

- A protected GitHub Environment requires owner approval for publication.
- The complete required CI suite runs again for the tagged commit.
- Artifacts are built from the clean tagged checkout, never reused from a
  developer working tree.
- Archive contents, version metadata, checksums, signatures, SBOMs, container
  startup, and attestations are verified before publication.
- A failure prevents GitHub Release creation and container/tag publication.
- Release concurrency ensures one publisher per tag.
- A non-publishing rehearsal validates the workflow during alpha.

## Future Beta Interface Freeze

During alpha, add a machine-readable compatibility manifest and generated
snapshots for:

- MCP tool names, authentication requirements, permissions, requests, and
  responses;
- CLI commands, flags, positional arguments, exit behavior, and configuration
  precedence;
- environment variables and credential/config file formats;
- Gateway local-socket protocol and local data paths;
- Gateway-to-Fabric sync methods and envelopes;
- PostgreSQL migration sequence and supported upgrade path;
- published binary, archive, and container names.

Alpha compatibility checks distinguish drift from breakage: intentional
feature additions update the manifest through review. Experimental interfaces
must be explicitly marked and excluded from the future stable baseline.

At a later, explicit `v0.3.0-beta.1` activation:

- a maintainer records the reviewed beta baseline;
- compatibility checks change from alpha inventory checks to required
  backward-compatibility enforcement;
- additive compatible changes remain allowed in beta minor releases;
- breaking changes require the next declared compatibility boundary,
  migration guidance, and explicit maintainer approval;
- released migrations remain immutable and corrections use new migrations;
- deprecated interfaces remain functional for a documented support window;
- urgent security fixes may shorten that window only with release notes and a
  security rationale.

That activation, tag, and release are explicitly outside this design's
implementation scope.

## Delivery Sequence

1. Root-cause and repair the failing Linux socket-race tests.
2. Hard-rename Gateway and Fabric and verify no old executable references
   remain.
3. Split CI into stable, independently visible checks.
4. Add dependency, vulnerability, secret, and workflow-security controls.
5. Add reproducible release artifacts, Fabric images, SBOMs, signatures,
   attestations, and a non-publishing rehearsal.
6. Add the alpha compatibility manifest and drift checks.
7. Observe every intended required check succeeding on `main`.
8. Apply repository protection and security settings.
9. Read back and verify the effective GitHub configuration.

## Failure Handling

- CI failures block merging after protection is active.
- Release failures leave no partial GitHub Release or mutable final image tag.
- Failed security setup reports the exact unsupported or unauthorized setting.
- Compatibility generation fails on nondeterministic output or an unexplained
  deletion.
- Migration verification fails closed on dirty versions, irreversible down
  paths, or upgrade-path errors.
- Emergency bypasses create explicit human-owned remediation work.

## Verification

Implementation is complete only when evidence shows:

- formatting, vet, build, integration, race, and coverage checks pass;
- migrations pass empty-database up/down and supported upgrade testing;
- renamed binaries build and old binary names are absent from release outputs;
- the Fabric container starts and passes a health smoke test on both target
  architectures where runner support permits;
- archives, checksums, SBOMs, signatures, and attestations validate;
- a release rehearsal completes without publishing a beta;
- compatibility snapshots are deterministic and alpha drift is reviewable;
- GitHub reports the intended required checks, bypass, branch, Actions, and
  security policies as active.

## Explicit Non-Goals

- Creating or tagging a beta release.
- Declaring current alpha interfaces backward-compatible.
- Freezing alpha feature development.
- Supporting the old `wormholed` or `wormhole-server` executable names.
- Renaming `WORMHOLE_*` environment variables or `~/.wormhole` paths.
- Adding a second maintainer or requiring an unavailable external approval.
- Expanding Wormhole's product feature scope.
