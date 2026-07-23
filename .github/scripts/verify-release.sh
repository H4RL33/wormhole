#!/bin/sh
set -eu

if test "$#" -ne 1; then
	printf 'usage: %s OUTPUT_DIR\n' "$0" >&2
	exit 2
fi

repo_root=$(cd "$(git rev-parse --show-toplevel)" && pwd -P)
script_dir=$repo_root/.github/scripts
output_dir=$1
"$script_dir/verify-release-artifacts.sh" "$output_dir"

set -- "$output_dir"/wormhole-*-linux-amd64.tar.gz
if test "$#" -ne 1 || ! test -f "$1"; then
	printf 'verify-release: expected exactly one amd64 archive\n' >&2
	exit 1
fi
version=${1##*/wormhole-}
version=${version%-linux-amd64.tar.gz}

image=${WORMHOLE_FABRIC_IMAGE:-wormhole-fabric:release-rehearsal}
built_image=false
cleanup() {
	if test "$built_image" = true; then
		docker image rm "$image" >/dev/null 2>&1 || true
	fi
}
trap cleanup EXIT HUP INT TERM

if test -z "${WORMHOLE_FABRIC_IMAGE:-}"; then
	docker build \
		--file "$repo_root/Dockerfile.fabric" \
		--build-arg "VERSION=$version" \
		--tag "$image" \
		"$repo_root"
	built_image=true
fi

"$script_dir/verify-fabric-image.sh" "$image"
