#!/bin/sh
set -eu

repo_root=$(git rev-parse --show-toplevel)
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM

version=9.8.7-alpha.rehearsal
epoch=1700000000
first="$tmp_dir/first"
second="$tmp_dir/second"

SOURCE_DATE_EPOCH=$epoch "$repo_root/.github/scripts/build-release.sh" "$version" "$first"
SOURCE_DATE_EPOCH=$epoch "$repo_root/.github/scripts/build-release.sh" "$version" "$second"

for arch in amd64 arm64; do
	archive="wormhole-${version}-linux-${arch}.tar.gz"
	sbom="wormhole-${arch}.spdx.json"
	test -f "$first/$archive"
	test -f "$first/$sbom"
	cmp "$first/$archive" "$second/$archive"
	cmp "$first/$sbom" "$second/$sbom"

	tar -tzf "$first/$archive" | sort >"$tmp_dir/${arch}.contents"
	cat >"$tmp_dir/${arch}.expected" <<EOF
wormhole-${version}-linux-${arch}/
wormhole-${version}-linux-${arch}/LICENSE
wormhole-${version}-linux-${arch}/README.md
wormhole-${version}-linux-${arch}/fabric
wormhole-${version}-linux-${arch}/gatewayd
wormhole-${version}-linux-${arch}/wormhole
EOF
	cmp "$tmp_dir/${arch}.expected" "$tmp_dir/${arch}.contents"
	grep -q '"spdxVersion": "SPDX-2.3"' "$first/$sbom"
	grep -q "\"fileName\": \"$archive\"" "$first/$sbom"

	extract_dir=$tmp_dir/extract-$arch
	mkdir -p "$extract_dir"
	tar -xzf "$first/$archive" -C "$extract_dir"
	for binary in wormhole gatewayd fabric; do
		if go version -m "$extract_dir/wormhole-${version}-linux-${arch}/$binary" |
			grep -q 'build[[:space:]]vcs='
		then
			printf '%s/%s contains non-reproducible VCS metadata\n' "$arch" "$binary" >&2
			exit 1
		fi
	done
done

(cd "$first" && sha256sum -c SHA256SUMS)
cmp "$first/SHA256SUMS" "$second/SHA256SUMS"
"$tmp_dir/extract-amd64/wormhole-${version}-linux-amd64/wormhole" --help |
	grep -Fq "version: $version"

for binary in wormhole gatewayd fabric; do
	WORMHOLE_EXPECT_LINKED_VERSION="$version" \
		go test \
		-ldflags "-X github.com/H4RL33/wormhole/cmd/${binary}.version=$version" \
		"./cmd/$binary" \
		-run 'TestLinkedVersion' \
		-count=1
done
