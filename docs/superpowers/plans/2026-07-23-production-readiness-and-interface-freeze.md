# Production Readiness and Interface Freeze Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Establish production-grade CI, release, repository, and alpha contract-inventory controls while hard-renaming the local daemon to Gateway and the coordination service to Fabric.

**Architecture:** Repair the existing Linux safety test before making it a gate, then land the two binary renames as an alpha hard cut. Split CI into stable named jobs, add a reproducible tag/rehearsal release workflow, inventory public contracts without freezing alpha development, and apply GitHub enforcement only after every proposed required check is green on `main`.

**Tech Stack:** Go 1.26.4, GitHub Actions, PostgreSQL 16 with pgvector, Docker Buildx, GHCR, Syft SPDX SBOMs, Sigstore Cosign, GitHub artifact attestations, GitHub repository rulesets.

## Global Constraints

- Do not create `v0.3.0-beta.1`, any beta tag, or any beta release.
- Alpha feature development remains open; compatibility enforcement stays in inventory mode.
- Hard rename `wormholed` to `gatewayd` and `wormhole-server` to `fabric`; ship no old-name alias.
- Keep all `WORMHOLE_*` environment variables and `~/.wormhole` paths unchanged.
- Preserve the existing SQLite database and socket filenames for in-place alpha upgrades; names inside `~/.wormhole` are data-format paths, not executable aliases.
- Publish the future Fabric image as `ghcr.io/h4rl33/wormhole-fabric`.
- Release targets are Linux `amd64` and `arm64`.
- Keep GitHub Actions permissions read-only by default and grant writes only to release jobs.
- Pin every third-party Action to a full commit SHA with its release tag in a comment.
- Do not enable required checks until all proposed checks have succeeded on `main`.
- Preserve unrelated untracked files `brand/WORMHOLE-BRAND-GUIDELINES.md` and `resume.md`.

---

### Task 1: Repair Linux stale-socket identity checks

**Files:**
- Modify: `cmd/wormholed/stale_socket_linux.go`
- Modify: `cmd/wormholed/wormholed.go`
- Modify: `cmd/wormholed/wormholed_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Consumes: `removeStaleSocketWithHooks(socketPath string, hooks staleSocketRemovalHooks) error`
- Produces: inode-stable stale-socket quarantine that cannot mistake a replacement reusing the same inode number for the original socket

- [ ] **Step 1: Reproduce the CI failure repeatedly**

Run:

```bash
go test ./cmd/wormholed \
  -run 'TestRemoveStaleSocket_(InodeSwapPreservesReplacement|PostQuarantineCollisionPreservesBothPaths)' \
  -count=100
```

Expected before the fix: at least one failure reporting either `want inode-change rejection` or `want EEXIST restoration collision`.

- [ ] **Step 2: Add a regression assertion that the checked inode remains referenced**

Extend the two failing tests so the hook replacement can reuse neither the
checked inode identity nor a released file handle. Keep the existing content
and quarantine-path assertions. Add a table case that replaces the socket with
another Unix socket as well as the existing regular file and symlink cases.

Run:

```bash
go test ./cmd/wormholed -run TestRemoveStaleSocket_InodeSwapPreservesReplacement -count=20
```

Expected: FAIL on the current implementation.

- [ ] **Step 3: Hold an `O_PATH|O_NOFOLLOW` descriptor across quarantine**

Use `golang.org/x/sys/unix`, already present in the module graph, to open the
checked socket without connecting to it:

```go
fd, err := unix.Open(socketPath, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
if err != nil {
    return fmt.Errorf("wormholed: open stale socket path: %w", err)
}
defer unix.Close(fd)

var expected unix.Stat_t
if err := unix.Fstat(fd, &expected); err != nil {
    return fmt.Errorf("wormholed: stat stale socket descriptor: %w", err)
}
```

Pass `expected.Dev` and `expected.Ino` to `quarantineAndRemoveSocket`. After
the rename, use `unix.Lstat` on the quarantine path and compare both values.
The held descriptor prevents immediate inode reuse. Preserve the existing
`RENAME_NOREPLACE` restoration path and all fail-closed errors.

Move `golang.org/x/sys` from the indirect block into the direct dependency
block in `go.mod`; do not change its version.

- [ ] **Step 4: Verify the focused race behavior**

Run:

```bash
go test ./cmd/wormholed \
  -run 'TestRemoveStaleSocket_(InodeSwapPreservesReplacement|PostQuarantineCollisionPreservesBothPaths)' \
  -count=100
go test -race ./cmd/wormholed
```

Expected: PASS with no race report.

- [ ] **Step 5: Verify the full current gate**

Run:

```bash
make fmt-check
make build
make vet
make integration
make race
make coverage
```

Expected: all commands exit 0 and merged coverage is at least `90.0%`.

- [ ] **Step 6: Commit**

```bash
git add cmd/wormholed/stale_socket_linux.go cmd/wormholed/wormholed.go \
  cmd/wormholed/wormholed_test.go go.mod go.sum
git commit -m "fix(gateway): hold stale socket identity"
```

---

### Task 2: Hard-rename the Gateway and Fabric binaries

**Files:**
- Move: `cmd/wormholed/` to `cmd/gatewayd/`
- Move: `cmd/wormhole-server/` to `cmd/fabric/`
- Modify: `Makefile`
- Modify: `.github/workflows/ci.yml`
- Modify: all tracked Go comments, test names, error prefixes, MCP server-info names, documentation, and examples containing the old executable names
- Modify: `README.md`
- Modify: `SECURITY.md`
- Modify: `CONTRIBUTING.md`
- Modify: `agents/README.md`
- Modify: `docs/implementation-rules.md`
- Modify: `docs/claude-code-connector.md`
- Modify: `docs/mcp-protocol.md`
- Modify: `docs/wiki/Home.md`
- Modify: `docs/wiki/CLI-Guide.md`
- Modify: `docs/wiki/Security-Model.md`

**Interfaces:**
- Consumes: existing CLI, local runtime, and coordination server behavior
- Produces: `dist/wormhole`, `dist/gatewayd`, and `dist/fabric`; local MCP initialization reports server name `gatewayd`

- [ ] **Step 1: Add hard-cut build assertions**

Add a shell-backed Make target:

```make
.PHONY: naming-check
naming-check:
	@test -x $(DIST)/wormhole
	@test -x $(DIST)/gatewayd
	@test -x $(DIST)/fabric
	@test ! -e $(DIST)/wormholed
	@test ! -e $(DIST)/wormhole-server
```

Update the MCP initialization test to require:

```go
wantInfo := map[string]string{"name": "gatewayd", "version": "0.2.4-alpha"}
```

Run `make build naming-check`; expected before the rename: FAIL because the new
binaries do not exist.

- [ ] **Step 2: Move command packages and update build targets**

Use `git mv` for both directories. Set:

```make
BINARIES := wormhole gatewayd fabric
```

Update cross-build commands to use `./cmd/gatewayd`. Change executable-facing
errors and logs to begin with `gatewayd:` or `fabric:`. Do not rename
`wormholed.db`, `wormholed.sock`, `WORMHOLE_*`, or `~/.wormhole`.

- [ ] **Step 3: Replace semantic references**

Replace prose according to meaning:

- local process or daemon: `Gateway` or `gatewayd`;
- shared service: `Fabric` or `fabric`;
- architecture: `Harness -> wormhole mcp -> Gateway -> Fabric`;
- code package paths: `cmd/gatewayd` and `cmd/fabric`.

Do not mechanically alter historical plan/spec files, migration files, GitHub
issue URLs, or the approved design's statements describing the old names.

- [ ] **Step 4: Prove the hard cut**

Run:

```bash
make clean
make build
make naming-check
go test ./cmd/gatewayd ./cmd/fabric ./cmd/wormhole
rg -n 'cmd/(wormholed|wormhole-server)|dist/(wormholed|wormhole-server)|go run ./cmd/(wormholed|wormhole-server)' \
  --glob '!docs/superpowers/**' .
```

Expected: builds and tests PASS; `rg` returns no matches.

- [ ] **Step 5: Run the full suite**

Run:

```bash
make fmt-check
make vet
make integration
make race
make coverage
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add Makefile .github cmd internal README.md SECURITY.md CONTRIBUTING.md \
  agents docs
git commit -m "refactor: rename Gateway and Fabric binaries"
```

---

### Task 3: Split and harden required CI checks

**Files:**
- Replace: `.github/workflows/ci.yml`
- Create: `.github/workflows/migrations.yml`
- Create: `.github/workflows/security.yml`
- Create: `.github/dependabot.yml`
- Create: `.github/scripts/check-action-pins.sh`
- Create: `.github/scripts/test-alpha-upgrade.sh`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `make fmt-check`, `make build`, `make integration`, `make race`, `make coverage`, migration pairs
- Produces stable check names: `Static`, `Build`, `Integration`, `Race`, `Coverage`, `Migrations`, `Vulnerability`, `Secret Scan`, `Dependency Review`, `Action Pins`

- [ ] **Step 1: Add a failing immutable-action validator**

Create `.github/scripts/check-action-pins.sh`:

```sh
#!/bin/sh
set -eu
bad=$(
  grep -RhoE 'uses:[[:space:]]+[^[:space:]]+@[^[:space:]#]+' .github/workflows |
  awk -F@ '$2 !~ /^[0-9a-f]{40}$/ { print }'
)
if test -n "$bad"; then
  printf '%s\n' "Actions must be pinned to full commit SHAs:" "$bad" >&2
  exit 1
fi
```

Run `sh .github/scripts/check-action-pins.sh`; expected before workflow
replacement: FAIL on `actions/checkout@v4`, `actions/setup-go@v5`, and
`actions/upload-artifact@v4`.

- [ ] **Step 2: Define stable CI jobs**

In `ci.yml`, use pull-request and `main` push triggers, top-level
`permissions: contents: read`, PR concurrency, 30-minute job timeouts, and
these job IDs/names:

```yaml
jobs:
  static:
    name: Static
  build:
    name: Build
  integration:
    name: Integration
  race:
    name: Race
  coverage:
    name: Coverage
```

Pin:

```yaml
- uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4
- uses: actions/setup-go@40f1582b2485089dde7abd97c1529aa768e1baff # v5
- uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02 # v4
```

Give every database job its own pgvector PostgreSQL service and run migrations
before tests so jobs are isolated and independently diagnostic.

- [ ] **Step 3: Add migration verification**

Create `migrations.yml` with check name `Migrations`. Test empty-database
`up`, `down -all`, and an upgrade whose starting migration set is extracted
from tag `v0.2.4-alpha` before current migrations are applied.

`.github/scripts/test-alpha-upgrade.sh` must:

```sh
#!/bin/sh
set -eu
database_url=${WORMHOLE_DATABASE_URL:?required}
baseline_dir=$(mktemp -d)
trap 'rm -rf "$baseline_dir"' EXIT
git archive v0.2.4-alpha migrations | tar -x -C "$baseline_dir"
migrate -path "$baseline_dir/migrations" -database "$database_url" up
migrate -path migrations -database "$database_url" up
test "$(psql "$database_url" -Atc 'select dirty from schema_migrations')" = "f"
```

- [ ] **Step 4: Add dependency and workflow security**

Create `security.yml` with:

- `Vulnerability`: install and run
  `golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...`;
- `Secret Scan`: run
  `gitleaks/gitleaks-action@ff98106e4c7b2bc287b24eaf42907196329070c7`
  (`v2`) over the checked-out history;
- `Dependency Review`: PR-only
  `actions/dependency-review-action@38ecb5b593bf0eb19e335c03f97670f792489a8b`;
- `Action Pins`: run `.github/scripts/check-action-pins.sh`.

Create `.github/dependabot.yml` with weekly Go modules, GitHub Actions, and
Docker checks, each limited to five open pull requests.

- [ ] **Step 5: Validate workflows locally**

Run:

```bash
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7
sh .github/scripts/check-action-pins.sh
shellcheck .github/scripts/*.sh
make fmt-check
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add .github Makefile
git commit -m "ci: split production readiness gates"
```

---

### Task 4: Add reproducible release artifacts and Fabric image

**Files:**
- Create: `Dockerfile.fabric`
- Create: `.dockerignore`
- Create: `.github/scripts/build-release.sh`
- Create: `.github/scripts/verify-release.sh`
- Create: `.github/workflows/release.yml`
- Modify: `cmd/wormhole/main.go`
- Modify: `cmd/gatewayd/main.go`
- Modify: `cmd/fabric/main.go`
- Modify: `Makefile`

**Interfaces:**
- Consumes: an annotated `v*-alpha`, `v*-beta.*`, or stable `v*` tag, or manual rehearsal input
- Produces: two Linux archives, checksums, SPDX SBOMs, keyless signatures, provenance attestations, Fabric multi-arch image, and GitHub Release

- [ ] **Step 1: Add injectable version metadata and tests**

In each main package:

```go
var version = "dev"
```

Expose it through existing help/initialization/server metadata without adding a
new command surface. Add tests that link with an injected value and assert the
reported version. Build flags use:

```sh
-ldflags "-s -w -X main.version=$version"
```

- [ ] **Step 2: Add deterministic archive builder**

`.github/scripts/build-release.sh VERSION OUTPUT_DIR` must build both
`linux/amd64` and `linux/arm64` with `CGO_ENABLED=0`, place `wormhole`,
`gatewayd`, `fabric`, `LICENSE`, and `README.md` under
`wormhole-${VERSION}-linux-${arch}/`, normalize tar owner/group and timestamps
to `SOURCE_DATE_EPOCH`, gzip with `-n`, and write `SHA256SUMS`.

Generate one SPDX JSON SBOM per extracted archive tree with a direct Syft
`1.44.0` invocation. Pin the release archive and extracted executable by
platform-specific SHA-256 checksums, verify both checksums and the reported
version before execution, and use the same installer locally and in the
hosted workflow. This checksum-pinned direct invocation replaces
`anchore/sbom-action`: the pinned action delegates to mutable installer and
tooling resolution, so pinning the action commit alone does not freeze the
SBOM producer.

Add `make release-rehearsal`:

```make
release-rehearsal:
	SOURCE_DATE_EPOCH=$$(git show -s --format=%ct HEAD) \
	  .github/scripts/build-release.sh 0.0.0-alpha.rehearsal dist/release
	.github/scripts/verify-release.sh dist/release
```

- [ ] **Step 3: Add the Fabric container**

Create a multi-stage `Dockerfile.fabric` that builds `./cmd/fabric` with
`CGO_ENABLED=0`, copies it into `gcr.io/distroless/static-debian12:nonroot`,
exposes `8080`, runs as nonroot, and sets:

```dockerfile
ENTRYPOINT ["/fabric"]
```

The verification script starts the local image with a test database URL and
polls `/healthz` until it receives `204`, then removes the container.

- [ ] **Step 4: Add release and rehearsal workflow**

`release.yml` supports:

```yaml
on:
  push:
    tags: ['v*']
  workflow_dispatch:
```

Reject lightweight tags in the tag path with:

```sh
test "$(git cat-file -t "$GITHUB_REF_NAME")" = tag
```

Use a `release` environment only for the annotated-tag publication jobs.
`workflow_dispatch` is always a rehearsal and cannot publish, regardless of
who invokes it. Every annotated-tag publication job must also require
repository variable `WORMHOLE_RELEASE_ENABLED` to equal the exact lowercase
string `true`; an absent or different value fails closed. Task 8 may set the
variable only after auditing the release environment and repository rules.
Rehearsal builds and verifies everything but skips GHCR push and GitHub
Release creation.

For annotated tag publication, sign every archive, checksum manifest, and SBOM:

```sh
for artifact in dist/release/*; do
  cosign sign-blob --yes \
    --output-signature "${artifact}.sig" \
    --output-certificate "${artifact}.pem" \
    "$artifact"
done
```

Attest the archive subject digests. For Fabric, assemble a run-scoped staging
manifest from the exact two architecture digests that passed `/healthz`,
resolve and sign/attest that manifest digest, recheck the annotated tag
identity, and only then promote that same digest to the public version tag as
the final image-publication action. A failure before promotion must leave no
public version tag. Retain the run-scoped architecture and manifest staging
tags while the public version exists: GHCR deletion is digest-scoped, so
automatic deletion could remove manifests referenced by the public
multi-architecture tag. Create the GitHub release last:

```sh
gh release create "$GITHUB_REF_NAME" dist/release/* \
  --verify-tag --generate-notes --title "$GITHUB_REF_NAME"
```

Pin the Docker and publication Actions to:

```yaml
docker/login-action@c94ce9fb468520275223c153574b00df6fe4bcc9 # v3
docker/setup-qemu-action@c7c53464625b32c7a7e944ae62b3e17d2b600130 # v3
docker/setup-buildx-action@8d2750c68a42422c14e847fe6c8ac0403b4cbd6f # v3
docker/build-push-action@10e90e3645eae34f1e60eeb005ba3a3d33f178e8 # v6
sigstore/cosign-installer@398d4b0eeef1380460a10c8013a76f728fb906ac # v3
actions/attest-build-provenance@e8998f949152b193b063cb0ec769d69d929409be # v2
```

Grant `packages: write`, `id-token: write`, and `attestations: write` only to
publishing jobs.

- [ ] **Step 5: Verify a non-publishing release**

Run:

```bash
make release-rehearsal
find dist/release -maxdepth 1 -type f -printf '%f\n' | sort
sha256sum -c dist/release/SHA256SUMS
```

Expected: both architecture archives and SBOMs exist, archive contents are
exact, checksum verification passes, and no beta tag or release exists.

- [ ] **Step 6: Commit**

```bash
git add Dockerfile.fabric .dockerignore .github Makefile cmd
git commit -m "ci(release): add signed release rehearsal"
```

---

### Task 5: Add the alpha compatibility inventory

**Files:**
- Create: `docs/contracts/alpha-contract.json`
- Create: `docs/contracts/README.md`
- Create: `internal/mcp/contract_manifest_test.go`
- Create: `cmd/wormhole/contract_manifest_test.go`
- Create: `internal/runtime/sync/contract_manifest_test.go`
- Create: `.github/scripts/check-contract-manifest.sh`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: registered MCP descriptors, CLI help surfaces, sync protocol version, migrations, binary/image names
- Produces: deterministic `docs/contracts/alpha-contract.json` and required check `Contract Inventory`

- [ ] **Step 1: Check in the manifest schema and alpha policy**

Use JSON with sorted arrays and these top-level keys:

```json
{
  "mode": "alpha-inventory",
  "mcp_tools": [],
  "cli": {},
  "environment": [],
  "paths": {},
  "local_protocol": {},
  "sync_protocol": {},
  "migrations": [],
  "artifacts": {}
}
```

Document that reviewed alpha additions update the file, experimental entries
carry `"stability": "experimental"`, and no beta compatibility promise is
active.

- [ ] **Step 2: Cross-check MCP and sync contracts**

Reuse the existing MCP registry invariant fixture rather than create a second
hand-maintained registry. Assert exact tool name, auth, permission, and schema
snapshots against `alpha-contract.json`. In the sync package, assert
`SyncProtocolVersion`, method names, and JSON field names against the same
file.

- [ ] **Step 3: Cross-check CLI and artifact contracts**

Invoke `runMain`/command flag sets in-process and compare commands, flags,
positionals, and exit behavior to the manifest. Assert:

```json
{
  "binaries": ["fabric", "gatewayd", "wormhole"],
  "fabric_image": "ghcr.io/h4rl33/wormhole-fabric",
  "release_platforms": ["linux/amd64", "linux/arm64"]
}
```

Assert the retained local paths remain `wormholed.sock` and `wormholed.db`.

- [ ] **Step 4: Add deterministic drift checking**

`.github/scripts/check-contract-manifest.sh` runs the three focused test
packages twice, hashes the manifest before and after, and fails if tests mutate
it or generated output differs. Add CI job name `Contract Inventory`.

- [ ] **Step 5: Verify**

Run:

```bash
sh .github/scripts/check-contract-manifest.sh
go test ./internal/mcp ./cmd/wormhole ./internal/runtime/sync
git diff --exit-code -- docs/contracts/alpha-contract.json
```

Expected: PASS and no generated diff.

- [ ] **Step 6: Commit**

```bash
git add docs/contracts internal/mcp/contract_manifest_test.go \
  cmd/wormhole/contract_manifest_test.go \
  internal/runtime/sync/contract_manifest_test.go \
  .github
git commit -m "test(contracts): inventory alpha interfaces"
```

---

### Task 6: Update canonical documentation and Wiki sources

**Files:**
- Modify: `README.md`
- Modify: `SECURITY.md`
- Modify: `CONTRIBUTING.md`
- Modify: `agents/README.md`
- Modify: `docs/implementation-rules.md`
- Modify: `docs/claude-code-connector.md`
- Modify: `docs/mcp-protocol.md`
- Modify: `docs/wiki/Home.md`
- Modify: `docs/wiki/CLI-Guide.md`
- Modify: `docs/wiki/Security-Model.md`
- Create: `docs/releasing.md`
- Create: `docs/compatibility.md`

**Interfaces:**
- Consumes: final Gateway/Fabric commands, workflow names, artifact formats, alpha contract policy
- Produces: canonical operator, contributor, release, and compatibility guidance

- [ ] **Step 1: Document operator-facing names and paths**

Every current command example must use `gatewayd` and `fabric`; diagrams must
use `Gateway` and `Fabric`. Explicitly explain why the retained
`wormholed.sock` and `wormholed.db` filenames are not executable aliases.

- [ ] **Step 2: Document contributor gates and bypass policy**

List the exact required check names and state that emergency owner bypass
requires an issue containing reason, impact, verification debt, and corrective
action.

- [ ] **Step 3: Document release rehearsal and publication**

`docs/releasing.md` must distinguish:

```text
workflow_dispatch -> rehearsal only, never publication
annotated v* tag + protected environment approval -> publication
```

Include archive names, GHCR image name, checksum/SBOM/signature verification,
the fail-closed `WORMHOLE_RELEASE_ENABLED` activation gate, and the explicit
prohibition on creating beta as part of this work. Document that run-scoped
GHCR staging tags are retained for the lifetime of their public version
because package deletion is digest-scoped; they may be removed only when the
corresponding public version is retired and its referenced child manifests no
longer need to resolve.

- [ ] **Step 4: Document alpha versus beta compatibility**

`docs/compatibility.md` must state:

- current mode is `alpha-inventory`;
- reviewed alpha interface changes update the manifest;
- a later explicit `v0.3.0-beta.1` action records and activates the baseline;
- this repository state makes no beta compatibility promise.

- [ ] **Step 5: Verify links and old executable references**

Run:

```bash
git diff --check
rg -n '`(wormholed|wormhole-server)( |`)|dist/(wormholed|wormhole-server)|cmd/(wormholed|wormhole-server)' \
  README.md SECURITY.md CONTRIBUTING.md agents docs \
  --glob '!docs/superpowers/**'
```

Expected: no current-instruction matches; historical specs/plans are excluded.

- [ ] **Step 6: Commit**

```bash
git add README.md SECURITY.md CONTRIBUTING.md agents docs
git commit -m "docs: define release and compatibility policy"
```

---

### Task 7: Publish through a pull request and observe green checks

**Files:**
- No new repository files unless CI diagnosis produces an approved focused fix

**Interfaces:**
- Consumes: Tasks 1–6
- Produces: reviewed merge on `main` with all proposed required checks observed green

- [ ] **Step 1: Run the complete local gate**

Run:

```bash
make fmt-check
make build
make naming-check
make vet
make integration
make race
make coverage
make release-rehearsal
sh .github/scripts/check-action-pins.sh
sh .github/scripts/check-contract-manifest.sh
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7
```

Expected: every command exits 0.

- [ ] **Step 2: Push a feature branch and open a pull request**

Use a branch named `production-readiness`. The PR body links the approved
design and lists each required check. Do not push a version tag.

- [ ] **Step 3: Wait for and inspect every check**

Run:

```bash
gh pr checks --watch
```

Expected: `Static`, `Build`, `Integration`, `Race`, `Coverage`, `Migrations`,
`Vulnerability`, `Secret Scan`, `Dependency Review`, `Action Pins`, and
`Contract Inventory` all pass or, for PR-inapplicable checks, are explicitly
documented as skipped by design.

- [ ] **Step 4: Merge through the PR and verify `main`**

After review, merge without bypass and wait for `main` workflows. Record the
successful run URLs for Task 8. No protection setting is changed before this
evidence exists.

---

### Task 8: Apply and verify GitHub freezes

**Files:**
- Create: `docs/operations/github-enforcement.md`

**Interfaces:**
- Consumes: stable successful check names and main-run URLs from Task 7
- Produces: active repository ruleset, protected release environment, enabled security controls, and read-back audit record

- [ ] **Step 1: Create the protected release environment**

Confirm repository variable `WORMHOLE_RELEASE_ENABLED` is absent or has a
value other than the exact lowercase string `true` before changing
enforcement. The release workflow must remain fail-closed throughout setup.

Create environment `release` with the repository owner as required reviewer,
deployment branches restricted to protected branches and tags, and no
self-review prevention that would deadlock a solo maintainer.

- [ ] **Step 2: Enable supported security controls**

Use GitHub repository APIs to enable Dependabot security updates, secret
scanning, push protection, validity checks, and SHA pinning. For any HTTP
`422`/plan limitation, record the exact response in
`docs/operations/github-enforcement.md`; do not silently weaken another gate.

- [ ] **Step 3: Create the `main` repository ruleset**

Target `refs/heads/main` and configure:

- pull requests required;
- zero approving reviews while there is one designated maintainer;
- review conversations resolved;
- force pushes and deletion blocked;
- required status checks from Task 7;
- repository-owner bypass in `pull_request`/emergency mode.

Do not require `Dependency Review` on push if the job is PR-only. Require only
check contexts actually observed with successful conclusions.

- [ ] **Step 4: Read back effective enforcement**

Query:

```bash
gh api repos/H4RL33/wormhole/rulesets
gh api repos/H4RL33/wormhole/environments/release
gh api repos/H4RL33/wormhole/actions/permissions
gh api repos/H4RL33/wormhole/actions/permissions/workflow
gh api repos/H4RL33/wormhole
```

Store a redacted, human-readable summary—not raw account IDs or credentials—in
`docs/operations/github-enforcement.md`.

- [ ] **Step 5: Activate release publication after the audit**

Only after the release-environment and repository-ruleset read-back in Step 4
matches the intended protections, set repository variable
`WORMHOLE_RELEASE_ENABLED` to the exact lowercase string `true`. Read it back
through the GitHub API and record the enabled state in
`docs/operations/github-enforcement.md`. If any audit check fails, leave the
variable absent or false.

- [ ] **Step 6: Confirm normal and bypass behavior**

Open a documentation-only test PR and confirm direct pushes are rejected while
the PR path is accepted after required checks. Do not perform an emergency
bypass merely to test it; confirm the configured bypass actor and mode through
the API.

- [ ] **Step 7: Commit the enforcement record through a PR**

```bash
git add docs/operations/github-enforcement.md
git commit -m "docs(ops): record GitHub enforcement"
```

Merge through the protected PR path and confirm required checks apply.

---

## Final Verification

- [ ] Run the full local gate from Task 7 on the final protected `main`.
- [ ] Confirm `git tag --list '*beta*'` contains no newly created beta tag.
- [ ] Confirm `gh release list` contains no newly created beta release.
- [ ] Confirm release rehearsal artifacts contain only `wormhole`, `gatewayd`,
  `fabric`, `README.md`, and `LICENSE`.
- [ ] Confirm the Fabric image name is
  `ghcr.io/h4rl33/wormhole-fabric`.
- [ ] Confirm the alpha contract mode is `alpha-inventory`.
- [ ] Confirm all required checks and GitHub controls by API read-back.
- [ ] Confirm `WORMHOLE_RELEASE_ENABLED` is exactly `true` only after the
      protected release environment and repository ruleset pass read-back.
- [ ] Preserve the unrelated `brand/WORMHOLE-BRAND-GUIDELINES.md` and
  `resume.md` files unchanged.
