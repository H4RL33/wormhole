#!/bin/sh
set -eu
database_url=${WORMHOLE_DATABASE_URL:?required}
baseline_dir=$(mktemp -d)
trap 'rm -rf "$baseline_dir"' EXIT
git archive v0.2.4-alpha migrations | tar -x -C "$baseline_dir"
migrate -path "$baseline_dir/migrations" -database "$database_url" up
migrate -path migrations -database "$database_url" up
test "$(psql "$database_url" -Atc 'select dirty from schema_migrations')" = "f"
