#!/bin/sh
set -eu
baseline_tag=v0.2.4-alpha
baseline_version=12
current_version=20

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
migrate -path migrations -database "$database_url" goto 18
legacy_project=00000000-0000-0000-0000-000000000053
legacy_agent=00000000-0000-0000-0000-000000000008
legacy_article=00000000-0000-0000-0000-000000000018
psql "$database_url" -v ON_ERROR_STOP=1 \
	-c "insert into projects (id, name, owner) values ('$legacy_project', 'semantic-upgrade', 'migration-test')" \
	-c "insert into agents (id, owner, model) values ('$legacy_agent', 'migration-test', 'legacy-stub')" \
	-c "insert into kb_articles (id, project_id, title, body, embedding, author_agent_id) values ('$legacy_article', '$legacy_project', 'legacy stub', 'preserve only', '[1,0,0]'::vector, '$legacy_agent')"
migrate -path migrations -database "$database_url" up
test "$(psql "$database_url" -At -F: \
	-c 'select version, dirty from schema_migrations')" = "$current_version:f"
test "$(psql "$database_url" -At -c "select count(*) from kb_articles where id = '$legacy_article' and vector_dims(embedding) = 3")" = 1
test "$(psql "$database_url" -At -c "select count(*) from kb_embedding_generations where project_id = '$legacy_project'")" = 0
test "$(psql "$database_url" -At -c "select to_regclass('public.integration_manifest_versions') is not null")" = t
migrate -path migrations -database "$database_url" goto 18
test "$(psql "$database_url" -At -c "select count(*) from kb_articles where id = '$legacy_article' and vector_dims(embedding) = 3")" = 1
test "$(psql "$database_url" -At -c "select to_regclass('public.kb_embedding_generations') is null")" = t
test "$(psql "$database_url" -At -c "select to_regclass('public.integration_manifest_versions') is null")" = t
migrate -path migrations -database "$database_url" up
test "$(psql "$database_url" -At -F: \
	-c 'select version, dirty from schema_migrations')" = "$current_version:f"
test "$(psql "$database_url" -At -c "select count(*) from kb_embedding_generations where project_id = '$legacy_project'")" = 0
test "$(psql "$database_url" -At -c "select to_regclass('public.integration_manifest_versions') is not null")" = t
