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

container=wormhole-fabric-verify-$$
# shellcheck disable=SC2329 # Invoked indirectly by trap.
cleanup() {
	docker rm -f "$container" >/dev/null 2>&1 || true
}
trap cleanup EXIT HUP INT TERM

docker run --detach \
	--name "$container" \
	--env 'WORMHOLE_DATABASE_URL=postgres://wormhole:wormhole@database.invalid:5432/wormhole?sslmode=disable' \
	--publish 127.0.0.1::8080 \
	"$image" >/dev/null
port=$(docker port "$container" 8080/tcp | sed 's/.*://')

attempt=0
while test "$attempt" -lt 30; do
	status=$(curl --silent --output /dev/null --write-out '%{http_code}' \
		"http://127.0.0.1:$port/healthz" || true)
	if test "$status" = 204; then
		exit 0
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
printf 'verify-fabric-image: /healthz did not return 204\n' >&2
exit 1
