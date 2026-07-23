#!/bin/sh
set -eu

workflow=.github/workflows/release.yml
test -f "$workflow"

grep -Fq 'workflow_dispatch:' "$workflow"
grep -Fq "tags: ['v*']" "$workflow"
for required_workflow in ci security migrations; do
	grep -Fq 'workflow_call:' ".github/workflows/$required_workflow.yml"
	grep -Fq "uses: ./.github/workflows/$required_workflow.yml" "$workflow"
done
publish_image_block=$(
	sed -n '/^  publish-image:/,/^  publish-release:/p' "$workflow"
)
printf '%s\n' "$publish_image_block" |
	grep -Fq 'needs: [validate, artifacts, ci, security, migrations]'
grep -Fq "github.event_name == 'push'" "$workflow"
grep -Fq 'environment: release' "$workflow"
grep -Fq 'ghcr.io/h4rl33/wormhole-fabric' "$workflow"
grep -Fq '.github/scripts/release-metadata.sh' "$workflow"
grep -Fq '.github/scripts/verify-release-tag.sh' "$workflow"
grep -Fq '.github/scripts/verify-artifact-transfer.sh' "$workflow"
grep -Fq '.github/scripts/publish-github-release.sh' "$workflow"
test "$(grep -c 'CERTIFICATE_IDENTITY: https://github.com/${{ github.workflow_ref }}' \
	"$workflow")" -eq 2
test "$(grep -c 'OIDC_ISSUER: https://token.actions.githubusercontent.com' \
	"$workflow")" -eq 2
test "$(grep -c 'cosign-release: v2.5.2' "$workflow")" -eq 2
test "$(grep -cF -- '--certificate-identity "$CERTIFICATE_IDENTITY"' \
	"$workflow")" -eq 2
test "$(grep -cF -- '--certificate-oidc-issuer "$OIDC_ISSUER"' \
	"$workflow")" -eq 2
test "$(grep -cF -- '--cert-identity "$CERTIFICATE_IDENTITY"' \
	"$workflow")" -eq 2
test "$(grep -cF -- '--cert-oidc-issuer "$OIDC_ISSUER"' \
	"$workflow")" -eq 2
for attestation_id in attest-image attest-amd64 attest-arm64; do
	grep -Fq "id: $attestation_id" "$workflow"
	grep -Fq "steps.$attestation_id.outputs.bundle-path" "$workflow"
done
# shellcheck disable=SC2016 # Literal workflow expression.
grep -Fq 'WORMHOLE_RELEASE_ENABLED: ${{ vars.WORMHOLE_RELEASE_ENABLED }}' \
	"$workflow"
# shellcheck disable=SC2016 # Literal workflow expression.
grep -Fq 'release-enabled: ${{ steps.metadata.outputs.release_enabled }}' "$workflow"
direct_release_gate_count=$(
	grep -c "vars.WORMHOLE_RELEASE_ENABLED == 'true'" "$workflow"
)
test "$direct_release_gate_count" -eq 2
release_gate_count=$(
	grep -c "needs.validate.outputs.release-enabled == 'true'" "$workflow"
)
test "$release_gate_count" -eq 2
grep -Fq 'archive-amd64-sha256:' "$workflow"
grep -Fq 'archive-arm64-sha256:' "$workflow"
grep -Fq 'sbom-amd64-sha256:' "$workflow"
grep -Fq 'sbom-arm64-sha256:' "$workflow"
grep -Fq 'manifest-sha256:' "$workflow"
grep -Fq 'temporary_manifest=' "$workflow"
grep -Fq -- '--prefer-index=false' "$workflow"
# shellcheck disable=SC2016 # Literal workflow shell source.
grep -Fq 'verify-fabric-image.sh "$IMAGE@$digest"' "$workflow"
build_line=$(grep -n 'docker buildx build' "$workflow" | cut -d: -f1)
# shellcheck disable=SC2016 # Literal workflow shell source.
local_health_line=$(grep -n 'verify-fabric-image.sh "$staging_tag"' \
	"$workflow" | cut -d: -f1)
# shellcheck disable=SC2016 # Literal workflow shell source.
push_line=$(grep -n 'docker push "$staging_tag"' "$workflow" |
	cut -d: -f1)
# shellcheck disable=SC2016 # Literal workflow shell source.
health_line=$(grep -n 'verify-fabric-image.sh "$IMAGE@$digest"' "$workflow" |
	cut -d: -f1)
temporary_manifest_line=$(
	# shellcheck disable=SC2016 # Literal workflow shell source.
	grep -nF -- '--tag "$temporary_manifest"' "$workflow" | cut -d: -f1
)
promotion_line=$(
	# shellcheck disable=SC2016 # Literal workflow shell source.
	grep -nF -- '--tag "$IMAGE:$VERSION"' "$workflow" | cut -d: -f1
)
prefer_carbon_copy_line=$(
	grep -nF -- '--prefer-index=false' "$workflow" | cut -d: -f1
)
tag_check_lines=$(grep -nF '.github/scripts/verify-release-tag.sh' \
	"$workflow" | cut -d: -f1)
pre_push_check=$(printf '%s\n' "$tag_check_lines" | sed -n '1p')
pre_temporary_manifest_check=$(printf '%s\n' "$tag_check_lines" | sed -n '2p')
pre_promotion_check=$(printf '%s\n' "$tag_check_lines" | sed -n '3p')
pre_sign_check=$(printf '%s\n' "$tag_check_lines" | sed -n '4p')
sign_line=$(grep -n 'cosign sign-blob' "$workflow" | cut -d: -f1)
manifest_sign_line=$(
	# shellcheck disable=SC2016 # Literal workflow shell source.
	grep -nF 'cosign sign --yes "$IMAGE@$DIGEST"' "$workflow" | cut -d: -f1
)
manifest_signature_verify_line=$(
	# shellcheck disable=SC2016 # Literal workflow shell source.
	grep -nF 'cosign verify "$IMAGE@$DIGEST"' "$workflow" | cut -d: -f1
)
manifest_attest_line=$(
	# shellcheck disable=SC2016 # Literal workflow expression.
	grep -nF 'subject-digest: ${{ steps.image.outputs.digest }}' "$workflow" |
		cut -d: -f1
)
image_attestation_verify_line=$(
	# shellcheck disable=SC2016 # Literal workflow shell source.
	grep -nF 'gh attestation verify "oci://$IMAGE@$DIGEST"' "$workflow" |
		cut -d: -f1
)
artifact_signature_verify_line=$(
	grep -nF 'cosign verify-blob' "$workflow" | cut -d: -f1
)
artifact_attestation_verify_line=$(
	# shellcheck disable=SC2016 # Literal workflow shell source.
	grep -nF 'gh attestation verify "$artifact"' "$workflow" | cut -d: -f1
)
amd64_attest_line=$(grep -nF 'id: attest-amd64' "$workflow" | cut -d: -f1)
arm64_attest_line=$(grep -nF 'id: attest-arm64' "$workflow" | cut -d: -f1)
test "$(printf '%s\n' "$build_line" | wc -l)" -eq 1
test "$build_line" -lt "$local_health_line"
test "$local_health_line" -lt "$pre_push_check"
test "$pre_push_check" -lt "$push_line"
test "$push_line" -lt "$health_line"
test "$health_line" -lt "$pre_temporary_manifest_check"
test "$pre_temporary_manifest_check" -lt "$temporary_manifest_line"
test "$temporary_manifest_line" -lt "$manifest_sign_line"
test "$manifest_sign_line" -lt "$manifest_signature_verify_line"
test "$manifest_signature_verify_line" -lt "$manifest_attest_line"
test "$manifest_attest_line" -lt "$image_attestation_verify_line"
test "$image_attestation_verify_line" -lt "$pre_promotion_check"
test "$pre_promotion_check" -lt "$promotion_line"
test "$prefer_carbon_copy_line" -lt "$promotion_line"
test "$pre_sign_check" -lt "$sign_line"
test "$sign_line" -lt "$artifact_signature_verify_line"
test "$artifact_signature_verify_line" -lt "$amd64_attest_line"
test "$amd64_attest_line" -lt "$arm64_attest_line"
test "$arm64_attest_line" -lt "$artifact_attestation_verify_line"
grep -Fq -- '--load' "$workflow"
grep -Fq 'Staging tag retention policy:' "$workflow"
if grep -Eq 'packages:[[:space:]]*delete|gh api .*--method DELETE' "$workflow"; then
	printf 'release workflow must not broaden package deletion permissions\n' >&2
	exit 1
fi

if grep -Fq 'anchore/sbom-action' "$workflow"; then
	printf 'release workflow must use the same pinned Syft installer as local builds\n' >&2
	exit 1
fi

qemu_image='docker.io/tonistiigi/binfmt:qemu-v10.2.3-68@sha256:400a4873b838d1b89194d982c45e5fb3cda4593fbfd7e08a02e76b03b21166f0'
buildkit_image='docker.io/moby/buildkit:v0.31.2@sha256:2f5adac4ecd194d9f8c10b7b5d7bceb5186853db1b26e5abd3a657af0b7e26ec'
grep -Fq "image: $qemu_image" "$workflow"
grep -Fq 'version: v0.35.0' "$workflow"
grep -Fq "image=$buildkit_image" "$workflow"

grep -Fq 'SYFT_VERSION=1.44.0' .github/scripts/install-syft.sh
if grep -R -E 'install\\.sh|:latest|version:[[:space:]]*latest' \
	.github/scripts/install-syft.sh "$workflow"
then
	printf 'release dependencies must not use mutable installers or tags\n' >&2
	exit 1
fi

for pin in \
	c94ce9fb468520275223c153574b00df6fe4bcc9 \
	c7c53464625b32c7a7e944ae62b3e17d2b600130 \
	8d2750c68a42422c14e847fe6c8ac0403b4cbd6f \
	10e90e3645eae34f1e60eeb005ba3a3d33f178e8 \
	398d4b0eeef1380460a10c8013a76f728fb906ac \
	e8998f949152b193b063cb0ec769d69d929409be
do
	grep -Fq "@$pin" "$workflow"
done

.github/scripts/check-action-pins.sh

publish_lines=$(grep -cE 'packages: write|id-token: write|attestations: write' "$workflow")
test "$publish_lines" -eq 5

release_line=$(grep -n 'publish-github-release.sh' "$workflow" | tail -n 1 | cut -d: -f1)
last_step_line=$(grep -n '^      - name:' "$workflow" | tail -n 1 | cut -d: -f1)
test "$release_line" -gt "$last_step_line"
test "$artifact_attestation_verify_line" -lt "$release_line"

release_docs=docs/releasing.md
grep -Fq 'sha256sum -c SHA256SUMS' "$release_docs"
grep -Fq 'certificate_identity="https://github.com/H4RL33/wormhole/.github/workflows/release.yml@refs/tags/$release_tag"' \
	"$release_docs"
grep -Fq 'certificate_oidc_issuer="https://token.actions.githubusercontent.com"' \
	"$release_docs"
grep -Fq 'cosign verify-blob' "$release_docs"
grep -Fq 'gh attestation verify "$artifact"' "$release_docs"
grep -Fq 'cosign verify "$image@$digest"' "$release_docs"
grep -Fq 'gh attestation verify "oci://$image@$digest"' "$release_docs"
