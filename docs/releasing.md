# Releasing Wormhole

This is the canonical release procedure for the implemented workflow. It does
not claim that GitHub-hosted environment protection, branch rules, or repository
variables have already been configured or verified.

## Two workflow modes

```text
workflow_dispatch -> rehearsal only, never publication
annotated v* tag + protected environment approval -> publication
```

`workflow_dispatch` builds and verifies a supplied rehearsal version, including
archives, SBOMs, and local Fabric image health checks. It never pushes to GHCR
or creates a GitHub Release.

Publication candidates require an **annotated** tag matching `v*`. The workflow
resolves the tag object and commit, verifies them repeatedly, and uses the
`release` environment for the publication jobs. Publication is fail-closed: both
publication jobs require the repository variable
`WORMHOLE_RELEASE_ENABLED` to be exactly lowercase `true`. An absent, false, or
differently cased value prevents publication. Enable that variable only after a
read-back audit confirms the protected environment and repository policy.

Do not create a beta tag, beta release, or beta compatibility baseline as part
of this work.

## Produced artifacts

For version `{version}`, the workflow produces:

- `wormhole-{version}-linux-amd64.tar.gz`;
- `wormhole-{version}-linux-arm64.tar.gz`;
- `SHA256SUMS`;
- `wormhole-amd64.spdx.json` and `wormhole-arm64.spdx.json`;
- keyless Sigstore `.sig` signatures and `.pem` certificates for each archive,
  SBOM, and checksum manifest;
- GitHub build-provenance attestations for the archives; and
- a multi-architecture Fabric image at
  `ghcr.io/h4rl33/wormhole-fabric` for `linux/amd64` and `linux/arm64`.

Each archive contains `wormhole`, `gatewayd`, `fabric`, `README.md`, and
`LICENSE`. The archive builder uses deterministic timestamps and writes the
checksum manifest after it verifies the release artifact contract.

## Verification before publication or installation

The workflow verifies archive layout and contents, `SHA256SUMS`, and SPDX SBOM
contents before artifacts leave the producer job. The publication job independently
re-verifies transferred artifact digests, then signs the verified bytes and
creates provenance attestations. Fabric architecture images pass `/healthz` before
their exact digests form the public multi-architecture manifest; the manifest is
also signed and attested. Consumers should verify the checksum manifest, keyless
Sigstore signature/certificate, SBOM, and applicable GitHub attestation before
trusting a release.

The GitHub Release is created last. A failure before Fabric manifest promotion
leaves no public version image tag. After that promotion, a later artifact
signing, attestation, tag-verification, or GitHub Release failure can leave the
signed and attested public version image without a GitHub Release.

## GHCR staging-tag retention

The workflow builds run-scoped architecture tags and a run-scoped manifest tag
before promoting the verified manifest digest to the public version. Retain those
staging tags for the lifetime of their public version. GHCR package deletion is
digest-scoped, so deleting a staging tag can remove a manifest still referenced
by the public multi-architecture version. Remove staging tags only after the
corresponding public version is retired and its referenced child manifests no
longer need to resolve.

## Maintainer controls

The intended required CI contexts are `Contract Inventory`, `Static`, `Build`,
`Integration`, `Race`, `Coverage`, `Migrations`, `Vulnerability`, `Secret Scan`,
and `Action Pins`. `Dependency Review` is pull-request-only. An emergency owner
bypass requires a follow-up issue documenting reason, impact, verification debt,
and corrective action. The controls become operational only after hosted
configuration is read back and verified.
