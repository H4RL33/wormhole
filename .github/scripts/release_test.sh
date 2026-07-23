#!/bin/sh
set -eu

repo_root=$(git rev-parse --show-toplevel)
test_root=$repo_root/dist/release/.release-test-$$
cleanup() {
	if test -d "$test_root" && ! test -L "$test_root"; then
		find -P "$test_root" -depth -delete
	fi
}
trap cleanup EXIT HUP INT TERM

version=9.8.7-alpha.rehearsal
epoch=1700000000
tool_dir=$test_root/tools
first=$test_root/first
second=$test_root/second
first_relative=${first#"$repo_root"/}
mkdir -p "$test_root"

"$repo_root/.github/scripts/install-syft.sh" "$tool_dir"
syft=$tool_dir/syft
test "$("$syft" version -o json | jq -r .version)" = 1.44.0

tampered_syft=$test_root/tampered-syft
cp "$syft" "$tampered_syft"
printf '\n' >>"$tampered_syft"
chmod 0755 "$tampered_syft"
if WORMHOLE_SYFT_BIN=$tampered_syft SOURCE_DATE_EPOCH=$epoch \
	"$repo_root/.github/scripts/build-release.sh" "$version" \
	"$test_root/tampered-tool-output" >/dev/null 2>&1
then
	printf 'build-release accepted a modified Syft binary\n' >&2
	exit 1
fi

(
	umask 077
	WORMHOLE_SYFT_BIN=$syft SOURCE_DATE_EPOCH=$epoch \
		"$repo_root/.github/scripts/build-release.sh" "$version" "$first_relative"
)
(
	umask 002
	WORMHOLE_SYFT_BIN=$syft SOURCE_DATE_EPOCH=$epoch \
		"$repo_root/.github/scripts/build-release.sh" "$version" "$second"
)

for output in "$first" "$second"; do
	test "$(stat -c %a "$output")" = 755
	for name in \
		"wormhole-${version}-linux-amd64.tar.gz" \
		"wormhole-${version}-linux-arm64.tar.gz" \
		wormhole-amd64.spdx.json \
		wormhole-arm64.spdx.json \
		SHA256SUMS
	do
		test "$(stat -c %a "$output/$name")" = 644
	done
done

(cd "$first" && sha256sum -c SHA256SUMS)
if awk '$2 ~ /\// { found = 1 } END { exit !found }' "$first/SHA256SUMS"; then
	printf 'release checksum manifest contains non-portable paths\n' >&2
	exit 1
fi

for arch in amd64 arm64; do
	archive="wormhole-${version}-linux-${arch}.tar.gz"
	sbom="wormhole-${arch}.spdx.json"
	test -f "$first/$archive"
	test -f "$first/$sbom"
	cmp "$first/$archive" "$second/$archive"
	cmp "$first/$sbom" "$second/$sbom"

	tar -tzf "$first/$archive" | sort >"$test_root/${arch}.contents"
	cat >"$test_root/${arch}.expected" <<EOF
wormhole-${version}-linux-${arch}/
wormhole-${version}-linux-${arch}/LICENSE
wormhole-${version}-linux-${arch}/README.md
wormhole-${version}-linux-${arch}/fabric
wormhole-${version}-linux-${arch}/gatewayd
wormhole-${version}-linux-${arch}/wormhole
EOF
	cmp "$test_root/${arch}.expected" "$test_root/${arch}.contents"

	archive_sha=$(sha256sum "$first/$archive" | cut -d' ' -f1)
	jq -e \
		--arg archive "$archive" \
		--arg digest "$archive_sha" \
		--arg platform "linux/$arch" \
		'
		.spdxVersion == "SPDX-2.3" and
		(.creationInfo.creators | index("Tool: syft-1.44.0") != null) and
		([.files[] | select(.fileName != "") | .fileName] | sort ==
			["LICENSE", "README.md", "fabric", "gatewayd", "wormhole"]) and
		(all(.files[] | select(.fileName != "");
			any(.checksums[]; .algorithm == "SHA256"))) and
		(.packages | length > 5) and
		(.relationships | length > 5) and
		(any(.packages[];
			.packageFileName == $archive and
			any(.checksums[]; .algorithm == "SHA256" and
				.checksumValue == $digest) and
			any(.externalRefs[];
				.referenceType == "wormhole-target-platform" and
				.referenceLocator == $platform))) and
		(any(.relationships[]; .relationshipType == "DESCRIBES")) and
		(any(.relationships[]; .relationshipType == "CONTAINS"))
		' "$first/$sbom" >/dev/null

	extract_dir=$test_root/extract-$arch
	mkdir -p "$extract_dir"
	tar -xzf "$first/$archive" -C "$extract_dir"
	stage=$extract_dir/wormhole-${version}-linux-${arch}
	test "$(stat -c %a "$stage")" = 755
	test "$(stat -c %a "$stage/LICENSE")" = 644
	test "$(stat -c %a "$stage/README.md")" = 644
	for binary in wormhole gatewayd fabric; do
		test "$(stat -c %a "$stage/$binary")" = 755
		if go version -m "$stage/$binary" |
			grep -q 'build[[:space:]]vcs='
		then
			printf '%s/%s contains non-reproducible VCS metadata\n' "$arch" "$binary" >&2
			exit 1
		fi
	done
done

cmp "$first/SHA256SUMS" "$second/SHA256SUMS"
"$test_root/extract-amd64/wormhole-${version}-linux-amd64/wormhole" --help |
	grep -Fq "version: $version"

archive_amd64_sha=$(sha256sum "$first/wormhole-${version}-linux-amd64.tar.gz" | cut -d' ' -f1)
archive_arm64_sha=$(sha256sum "$first/wormhole-${version}-linux-arm64.tar.gz" | cut -d' ' -f1)
sbom_amd64_sha=$(sha256sum "$first/wormhole-amd64.spdx.json" | cut -d' ' -f1)
sbom_arm64_sha=$(sha256sum "$first/wormhole-arm64.spdx.json" | cut -d' ' -f1)
manifest_sha=$(sha256sum "$first/SHA256SUMS" | cut -d' ' -f1)
"$repo_root/.github/scripts/verify-artifact-transfer.sh" \
	"$version" "$first" \
	"$archive_amd64_sha" "$archive_arm64_sha" \
	"$sbom_amd64_sha" "$sbom_arm64_sha" "$manifest_sha"

tampered=$test_root/tampered
mkdir "$tampered"
cp "$first"/* "$tampered/"
printf 'tamper\n' >>"$tampered/wormhole-amd64.spdx.json"
if "$repo_root/.github/scripts/verify-artifact-transfer.sh" \
	"$version" "$tampered" \
	"$archive_amd64_sha" "$archive_arm64_sha" \
	"$sbom_amd64_sha" "$sbom_arm64_sha" "$manifest_sha"
then
	printf 'artifact transfer verification accepted tampered bytes\n' >&2
	exit 1
fi

invalid=$test_root/invalid-sbom
mkdir "$invalid"
cp "$first"/* "$invalid/"
jq '.packages = []' "$first/wormhole-amd64.spdx.json" \
	>"$invalid/wormhole-amd64.spdx.json"
(
	cd "$invalid"
	sha256sum \
		"wormhole-${version}-linux-amd64.tar.gz" \
		"wormhole-${version}-linux-arm64.tar.gz" \
		wormhole-amd64.spdx.json \
		wormhole-arm64.spdx.json >SHA256SUMS
)
if "$repo_root/.github/scripts/verify-release-artifacts.sh" "$invalid"; then
	printf 'release verification accepted an empty package inventory\n' >&2
	exit 1
fi

for binary in wormhole gatewayd fabric; do
	WORMHOLE_EXPECT_LINKED_VERSION="$version" \
		go test \
		-ldflags "-X github.com/H4RL33/wormhole/cmd/${binary}.version=$version" \
		"./cmd/$binary" \
		-run 'TestLinkedVersion' \
		-count=1
done
