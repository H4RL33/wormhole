#!/bin/sh
set -eu

if test "$#" -ne 2; then
	printf 'usage: %s VERSION OUTPUT_DIR\n' "$0" >&2
	exit 2
fi

version=$1
output_arg=$2
epoch=${SOURCE_DATE_EPOCH:-}

case "$version" in
	"" | *[!A-Za-z0-9._-]*)
		printf 'build-release: invalid version %s\n' "$version" >&2
		exit 2
		;;
esac
if test "${#version}" -gt 64; then
	printf 'build-release: version must not exceed 64 characters\n' >&2
	exit 2
fi
case "$epoch" in
	"" | *[!0-9]*)
		printf 'build-release: SOURCE_DATE_EPOCH must be a non-negative integer\n' >&2
		exit 2
		;;
esac
case "$output_arg" in
	"" | / | . | ..)
		printf 'build-release: unsafe output directory %s\n' "$output_arg" >&2
		exit 2
		;;
esac

repo_root=$(git rev-parse --show-toplevel)
case "$output_arg" in
	/*) output_dir=$output_arg ;;
	*) output_dir=$repo_root/$output_arg ;;
esac
mkdir -p "$output_dir"
output_dir=$(cd "$output_dir" && pwd -P)

find "$output_dir" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +
build_dir=$(mktemp -d "$output_dir/.build.XXXXXX")
trap 'rm -rf "$build_dir"' EXIT HUP INT TERM

created=$(date -u -d "@$epoch" '+%Y-%m-%dT%H:%M:%SZ')

cd "$repo_root"
for arch in amd64 arm64; do
	base=wormhole-${version}-linux-${arch}
	stage=$build_dir/$base
	archive=$output_dir/$base.tar.gz
	mkdir -p "$stage"

	for binary in wormhole gatewayd fabric; do
		CGO_ENABLED=0 GOOS=linux GOARCH=$arch \
			go build -mod=readonly -trimpath -buildvcs=false \
			-ldflags "-s -w -X main.version=$version" \
			-o "$stage/$binary" "./cmd/$binary"
	done
	cp LICENSE README.md "$stage/"

	tar \
		--sort=name \
		--format=ustar \
		--owner=0 \
		--group=0 \
		--numeric-owner \
		--mtime="@$epoch" \
		-C "$build_dir" \
		-cf - "$base" |
		gzip -9 -n >"$archive"

	archive_name=$(basename "$archive")
	archive_sha=$(sha256sum "$archive" | cut -d' ' -f1)
	cat >"$output_dir/wormhole-${arch}.spdx.json" <<EOF
{
  "spdxVersion": "SPDX-2.3",
  "dataLicense": "CC0-1.0",
  "SPDXID": "SPDXRef-DOCUMENT",
  "name": "$base",
  "documentNamespace": "https://github.com/H4RL33/wormhole/releases/$version/$arch/$archive_sha",
  "creationInfo": {
    "created": "$created",
    "creators": ["Tool: wormhole-build-release"]
  },
  "files": [
    {
      "fileName": "$archive_name",
      "SPDXID": "SPDXRef-Archive",
      "checksums": [
        {"algorithm": "SHA256", "checksumValue": "$archive_sha"}
      ],
      "licenseConcluded": "NOASSERTION",
      "copyrightText": "NOASSERTION"
    }
  ],
  "relationships": [
    {
      "spdxElementId": "SPDXRef-DOCUMENT",
      "relationshipType": "DESCRIBES",
      "relatedSpdxElement": "SPDXRef-Archive"
    }
  ]
}
EOF
done

checksum_prefix=
case "$output_arg" in
	/*) ;;
	*) checksum_prefix=${output_arg%/}/ ;;
esac
: >"$output_dir/SHA256SUMS"
for file in \
	"wormhole-${version}-linux-amd64.tar.gz" \
	"wormhole-${version}-linux-arm64.tar.gz" \
	wormhole-amd64.spdx.json \
	wormhole-arm64.spdx.json
do
	hash=$(sha256sum "$output_dir/$file" | cut -d' ' -f1)
	printf '%s  %s%s\n' "$hash" "$checksum_prefix" "$file" >>"$output_dir/SHA256SUMS"
done
