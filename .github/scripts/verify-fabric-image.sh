#!/bin/sh
set -eu

if test "$#" -ne 1; then
	printf 'usage: %s IMAGE_REFERENCE\n' "$0" >&2
	exit 2
fi
if ! command -v docker >/dev/null 2>&1; then
	printf 'verify-fabric-image: docker is required\n' >&2
	exit 1
fi

image=$1
case "$image" in
	"")
		printf 'verify-fabric-image: image reference is required\n' >&2
		exit 2
		;;
esac

suffix=$$
container=wormhole-fabric-verify-$suffix
mock_container=wormhole-cohere-verify-$suffix
network=wormhole-fabric-verify-$suffix
mock_image=wormhole-cohere-mock:$suffix
mock_dir=$(mktemp -d "${TMPDIR:-/tmp}/wormhole-fabric-verify.XXXXXX")
# shellcheck disable=SC2329 # Invoked indirectly by trap.
cleanup() {
	docker rm -f "$container" >/dev/null 2>&1 || true
	docker rm -f "$mock_container" >/dev/null 2>&1 || true
	docker network rm "$network" >/dev/null 2>&1 || true
	docker image rm -f "$mock_image" >/dev/null 2>&1 || true
	rm -rf "$mock_dir"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

docker build \
	--network=none \
	--file .github/scripts/fabric-image-cohere-mock/Dockerfile \
	--tag "$mock_image" \
	.github/scripts/fabric-image-cohere-mock >/dev/null
docker network create --internal "$network" >/dev/null
docker run --detach \
	--name "$mock_container" \
	--network "$network" \
	--network-alias api.cohere.com \
	--user "$(id -u):$(id -g)" \
	--read-only \
	--security-opt no-new-privileges \
	--cap-drop ALL \
	--cap-add NET_BIND_SERVICE \
	--env 'WORMHOLE_MOCK_API_KEY=release-image-smoke' \
	--env 'WORMHOLE_MOCK_CA_PATH=/run/mock/ca.pem' \
	--env 'WORMHOLE_MOCK_COUNT_PATH=/run/mock/request-count' \
	--volume "$mock_dir:/run/mock" \
	"$mock_image" >/dev/null

attempt=0
while ! test -s "$mock_dir/ca.pem"; do
	if ! docker inspect --format '{{.State.Running}}' "$mock_container" \
		2>/dev/null | grep -qx true
	then
		docker logs "$mock_container" >&2 || true
		printf 'verify-fabric-image: Cohere mock exited before becoming ready\n' >&2
		exit 1
	fi
	attempt=$((attempt + 1))
	if test "$attempt" -ge 30; then
		printf 'verify-fabric-image: Cohere mock CA was not published\n' >&2
		exit 1
	fi
	sleep 1
done
chmod 0755 "$mock_dir"

docker run --detach \
	--name "$container" \
	--network "$network" \
	--network-alias fabric \
	--read-only \
	--security-opt no-new-privileges \
	--cap-drop ALL \
	--env 'WORMHOLE_DATABASE_URL=postgres://wormhole:wormhole@database.invalid:5432/wormhole?sslmode=disable' \
	--env 'WORMHOLE_COHERE_API_KEY=release-image-smoke' \
	--env 'SSL_CERT_FILE=/mock-ca/ca.pem' \
	--volume "$mock_dir:/mock-ca:ro" \
	"$image" >/dev/null

attempt=0
while test "$attempt" -lt 30; do
	if docker exec "$mock_container" /mock probe-health \
		"http://fabric:8080/healthz" >/dev/null 2>&1
	then
		request_count=$(sed -n '1p' "$mock_dir/request-count" 2>/dev/null || true)
		if test "$request_count" = 1; then
			exit 0
		fi
		docker logs "$mock_container" >&2 || true
		printf 'verify-fabric-image: expected exactly one startup embedding request\n' >&2
		exit 1
	fi
	if ! docker inspect --format '{{.State.Running}}' "$container" \
		2>/dev/null | grep -qx true
	then
		docker logs "$container" >&2 || true
		printf 'verify-fabric-image: container exited before becoming healthy\n' \
			>&2
		exit 1
	fi
	attempt=$((attempt + 1))
	sleep 1
done

docker logs "$container" >&2 || true
docker logs "$mock_container" >&2 || true
printf 'verify-fabric-image: /healthz did not return 204\n' >&2
exit 1
