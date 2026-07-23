#!/bin/sh
set -eu

if test "$#" -ne 1; then
	printf 'usage: %s OUTPUT_DIR\n' "$0" >&2
	exit 2
fi

repo_root=$(git rev-parse --show-toplevel)
output_arg=$1
case "$output_arg" in
	/*) output_dir=$output_arg ;;
	*) output_dir=$repo_root/$output_arg ;;
esac
if ! test -d "$output_dir"; then
	printf 'verify-release: output directory does not exist: %s\n' "$output_dir" >&2
	exit 1
fi
output_dir=$(cd "$output_dir" && pwd -P)

file_count=$(find "$output_dir" -maxdepth 1 -type f | wc -l)
if test "$file_count" -ne 5; then
	printf 'verify-release: expected 5 release files, found %s\n' "$file_count" >&2
	find "$output_dir" -maxdepth 1 -type f -printf '%f\n' | sort >&2
	exit 1
fi

set -- "$output_dir"/wormhole-*-linux-amd64.tar.gz
if test "$#" -ne 1 || ! test -f "$1"; then
	printf 'verify-release: expected exactly one amd64 archive\n' >&2
	exit 1
fi
amd64_archive=$1
version=${amd64_archive##*/wormhole-}
version=${version%-linux-amd64.tar.gz}

for arch in amd64 arm64; do
	archive=$output_dir/wormhole-${version}-linux-${arch}.tar.gz
	sbom=$output_dir/wormhole-${arch}.spdx.json
	test -f "$archive"
	test -f "$sbom"
	grep -q '"spdxVersion":[[:space:]]*"SPDX-2.3"' "$sbom"

	actual=$(mktemp)
	expected=$(mktemp)
	tar -tzf "$archive" | sort >"$actual"
	{
		printf 'wormhole-%s-linux-%s/\n' "$version" "$arch"
		printf 'wormhole-%s-linux-%s/LICENSE\n' "$version" "$arch"
		printf 'wormhole-%s-linux-%s/README.md\n' "$version" "$arch"
		printf 'wormhole-%s-linux-%s/fabric\n' "$version" "$arch"
		printf 'wormhole-%s-linux-%s/gatewayd\n' "$version" "$arch"
		printf 'wormhole-%s-linux-%s/wormhole\n' "$version" "$arch"
	} >"$expected"
	if ! cmp "$expected" "$actual"; then
		rm -f "$actual" "$expected"
		printf 'verify-release: unexpected %s archive contents\n' "$arch" >&2
		exit 1
	fi
	rm -f "$actual" "$expected"
done

checksum_subject=$(awk 'NR == 1 { print $2 }' "$output_dir/SHA256SUMS")
case "$checksum_subject" in
	*/*)
		(
			cd "$repo_root"
			sha256sum -c "$output_dir/SHA256SUMS"
		)
		;;
	*)
		(
			cd "$output_dir"
			sha256sum -c SHA256SUMS
		)
		;;
esac

if ! command -v docker >/dev/null 2>&1; then
	printf 'verify-release: docker is required for the Fabric smoke test\n' >&2
	exit 1
fi

image=${WORMHOLE_FABRIC_IMAGE:-wormhole-fabric:release-rehearsal}
container=wormhole-fabric-verify-$$
built_image=false
# shellcheck disable=SC2329 # Invoked indirectly by trap.
cleanup() {
	docker rm -f "$container" >/dev/null 2>&1 || true
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

docker run --detach \
	--name "$container" \
	--env 'WORMHOLE_DATABASE_URL=postgres://wormhole:wormhole@database.invalid:5432/wormhole?sslmode=disable' \
	--publish 127.0.0.1::8080 \
	"$image" >/dev/null
port=$(docker port "$container" 8080/tcp | sed 's/.*://')

attempt=0
while test "$attempt" -lt 30; do
	status=$(curl --silent --output /dev/null --write-out '%{http_code}' "http://127.0.0.1:$port/healthz" || true)
	if test "$status" = 204; then
		exit 0
	fi
	if ! docker inspect --format '{{.State.Running}}' "$container" 2>/dev/null | grep -qx true; then
		docker logs "$container" >&2 || true
		printf 'verify-release: Fabric container exited before becoming healthy\n' >&2
		exit 1
	fi
	attempt=$((attempt + 1))
	sleep 1
done

docker logs "$container" >&2 || true
printf 'verify-release: /healthz did not return 204\n' >&2
exit 1
