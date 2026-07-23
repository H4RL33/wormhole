#!/bin/sh
set -eu

if test "$#" -ne 7; then
	printf 'usage: %s VERSION OUTPUT_DIR AMD64_ARCHIVE_SHA ARM64_ARCHIVE_SHA AMD64_SBOM_SHA ARM64_SBOM_SHA MANIFEST_SHA\n' \
		"$0" >&2
	exit 2
fi

version=$1
output_dir=$2
archive_amd64_sha=$3
archive_arm64_sha=$4
sbom_amd64_sha=$5
sbom_arm64_sha=$6
manifest_sha=$7

case "$version" in
	"" | *[!A-Za-z0-9._-]*) exit 2 ;;
esac
for digest in \
	"$archive_amd64_sha" "$archive_arm64_sha" \
	"$sbom_amd64_sha" "$sbom_arm64_sha" "$manifest_sha"
do
	if ! printf '%s\n' "$digest" | grep -Eq '^[0-9a-f]{64}$'; then
		printf 'verify-artifact-transfer: invalid producer digest %s\n' \
			"$digest" >&2
		exit 1
	fi
done

if ! test -d "$output_dir" || test -L "$output_dir"; then
	printf 'verify-artifact-transfer: invalid output directory\n' >&2
	exit 1
fi
output_dir=$(cd "$output_dir" && pwd -P)

verify_digest() {
	expected=$1
	name=$2
	path=$output_dir/$name
	if ! test -f "$path" || test -L "$path"; then
		printf 'verify-artifact-transfer: missing regular file %s\n' \
			"$name" >&2
		exit 1
	fi
	(
		cd "$output_dir"
		printf '%s  %s\n' "$expected" "$name" | sha256sum -c -
	)
}

verify_digest "$archive_amd64_sha" \
	"wormhole-${version}-linux-amd64.tar.gz"
verify_digest "$archive_arm64_sha" \
	"wormhole-${version}-linux-arm64.tar.gz"
verify_digest "$sbom_amd64_sha" wormhole-amd64.spdx.json
verify_digest "$sbom_arm64_sha" wormhole-arm64.spdx.json
verify_digest "$manifest_sha" SHA256SUMS

"$(dirname "$0")/verify-release-artifacts.sh" "$output_dir"
