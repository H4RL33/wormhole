#!/bin/sh
set -eu
baseline_tag=v0.2.4-alpha
baseline_version=12
current_version=17

if test "${1:-}" = "--print-contract"; then
	if test "$#" -ne 1; then
		printf 'usage: %s --print-contract\n' "$0" >&2
		exit 2
	fi
	printf 'baseline_tag\t%s\n' "$baseline_tag"
	printf 'baseline_version\t%s\n' "$baseline_version"
	printf 'current_version\t%s\n' "$current_version"
	exit 0
fi
if test "$#" -ne 0; then
	printf 'usage: %s [--print-contract]\n' "$0" >&2
	exit 2
fi

database_url=${WORMHOLE_DATABASE_URL:?required}
baseline_dir=$(mktemp -d)
trap 'rm -rf "$baseline_dir"' EXIT
git archive "$baseline_tag" migrations | tar -x -C "$baseline_dir"
migrate -path "$baseline_dir/migrations" -database "$database_url" up
test "$(psql "$database_url" -At -F: \
	-c 'select version, dirty from schema_migrations')" = "$baseline_version:f"
migrate -path migrations -database "$database_url" up
test "$(psql "$database_url" -At -F: \
	-c 'select version, dirty from schema_migrations')" = "$current_version:f"
