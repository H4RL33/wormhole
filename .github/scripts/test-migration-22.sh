#!/bin/sh
set -eu

database_url=${WORMHOLE_DATABASE_URL:?required}
scratch=$(mktemp -d)

cleanup() {
	set +e
	psql "$database_url" -X -v ON_ERROR_STOP=0 >/dev/null 2>&1 <<'SQL'
DO $restore$
BEGIN
    IF to_regrole('wormhole_fabric_runtime') IS NULL AND to_regrole('wormhole_fabric_runtime_missing') IS NOT NULL THEN
        EXECUTE 'ALTER ROLE wormhole_fabric_runtime_missing RENAME TO wormhole_fabric_runtime';
    END IF;
    IF to_regrole('wormhole_attachment_resolver') IS NULL AND to_regrole('wormhole_attachment_resolver_missing') IS NOT NULL THEN
        EXECUTE 'ALTER ROLE wormhole_attachment_resolver_missing RENAME TO wormhole_attachment_resolver';
    END IF;
    IF to_regrole('wormhole_fabric_runtime') IS NOT NULL THEN
        ALTER ROLE wormhole_fabric_runtime LOGIN NOSUPERUSER NOBYPASSRLS NOINHERIT NOCREATEDB NOCREATEROLE NOREPLICATION;
    END IF;
    IF to_regrole('wormhole_attachment_resolver') IS NOT NULL THEN
        ALTER ROLE wormhole_attachment_resolver NOLOGIN NOSUPERUSER BYPASSRLS NOINHERIT NOCREATEDB NOCREATEROLE NOREPLICATION;
        REVOKE USAGE ON SCHEMA public FROM wormhole_attachment_resolver;
    END IF;
    IF to_regrole('wormhole_runtime_parent') IS NOT NULL THEN
        REVOKE wormhole_runtime_parent FROM wormhole_fabric_runtime;
        DROP ROLE wormhole_runtime_parent;
    END IF;
END
$restore$;
SQL
	rm -rf "$scratch"
}
trap cleanup EXIT HUP INT TERM

catalog_fingerprint() {
	psql "$database_url" -X -At -v ON_ERROR_STOP=1 <<'SQL'
SELECT jsonb_build_object(
  'columns',(SELECT jsonb_agg(to_jsonb(x) ORDER BY table_name,ordinal_position) FROM (SELECT table_name,ordinal_position,column_name,data_type,is_nullable,column_default FROM information_schema.columns WHERE table_schema='public' AND table_name IN ('fabric_workspace_stream_bindings','fabric_public_actor_keys','public_request_nonces','fabric_stream_versions','fabric_stream_conflicts','fabric_stream_requests','fabric_public_agent_sessions','audit_log')) x),
  'constraints',(SELECT jsonb_agg(to_jsonb(x) ORDER BY table_name,name) FROM (SELECT conrelid::regclass::text table_name,conname name,pg_get_constraintdef(oid,true) definition,condeferrable,condeferred,convalidated FROM pg_constraint WHERE connamespace='public'::regnamespace) x),
  'indexes',(SELECT jsonb_agg(to_jsonb(x) ORDER BY tablename,indexname) FROM (SELECT tablename,indexname,indexdef FROM pg_indexes WHERE schemaname='public') x),
  'policies',(SELECT jsonb_agg(to_jsonb(x) ORDER BY table_name,name) FROM (SELECT polrelid::regclass::text table_name,polname name,polcmd,polpermissive,pg_get_expr(polqual,polrelid) using_expr,pg_get_expr(polwithcheck,polrelid) check_expr FROM pg_policy) x),
  'triggers',(SELECT jsonb_agg(to_jsonb(x) ORDER BY table_name,name) FROM (SELECT tgrelid::regclass::text table_name,tgname name,pg_get_triggerdef(oid,true) definition FROM pg_trigger WHERE NOT tgisinternal) x),
  'functions',(SELECT jsonb_agg(to_jsonb(x) ORDER BY name,args) FROM (SELECT proname name,pg_get_function_identity_arguments(oid) args,pg_get_functiondef(oid) definition,pg_get_userbyid(proowner) owner,prosecdef,proconfig,proacl FROM pg_proc WHERE pronamespace='public'::regnamespace AND prokind IN ('f','p')) x),
  'relations',(SELECT jsonb_agg(to_jsonb(x) ORDER BY name) FROM (SELECT relname name,relkind,pg_get_userbyid(relowner) owner,relrowsecurity,relforcerowsecurity,COALESCE(relacl,acldefault(CASE WHEN relkind='S' THEN 's'::"char" ELSE 'r'::"char" END,relowner)) relacl FROM pg_class WHERE relnamespace='public'::regnamespace) x),
  'sequence',(SELECT to_jsonb(x) FROM (SELECT sequencename,start_value,min_value,max_value,increment_by,cycle,cache_size FROM pg_sequences WHERE schemaname='public' AND sequencename='audit_log_seq_seq') x)
)::text;
SQL
}

database_fingerprint() {
	catalog_fingerprint
	pg_dump "$database_url" --schema=public --data-only --column-inserts --no-owner --no-privileges |
		sed '/^\\restrict /d; /^\\unrestrict /d'
}

migrate -path migrations -database "$database_url" goto 21
database_fingerprint >"$scratch/v21-before"
migrate -path migrations -database "$database_url" goto 22
migrate -path migrations -database "$database_url" goto 21
database_fingerprint >"$scratch/v21-after"
cmp "$scratch/v21-before" "$scratch/v21-after"
migrate -path migrations -database "$database_url" goto 22
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run '^TestMigration22' -count=1
migrate -path migrations -database "$database_url" goto 21

test_polluted_duplicate_attachment_ref() {
	psql "$database_url" -X -v ON_ERROR_STOP=1 <<'SQL'
INSERT INTO projects(id,name,owner) VALUES
('00000000-0000-4000-8000-000000002201','migration22-duplicate-a','test'),
('00000000-0000-4000-8000-000000002202','migration22-duplicate-b','test');
INSERT INTO project_repository_bindings(project_id,fabric_instance_id,provider,provider_repository_id,canonical_remote,default_ref,visibility) VALUES
('00000000-0000-4000-8000-000000002201','11111111-1111-4111-8111-111111112201','github','2201','https://github.com/test/a','refs/heads/main','public'),
('00000000-0000-4000-8000-000000002202','11111111-1111-4111-8111-111111112201','github','2202','https://github.com/test/b','refs/heads/main','public');
INSERT INTO fabric_streams(project_id,fabric_instance_id,stream_id,canonical_ref,ref_name,live_tree_digest,accepted_tree_digest,accepted_commit_sha) VALUES
('00000000-0000-4000-8000-000000002201','11111111-1111-4111-8111-111111112201','22222222-2222-4222-8222-222222222201','refs/heads/main','refs/heads/main','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'),
('00000000-0000-4000-8000-000000002202','11111111-1111-4111-8111-111111112201','22222222-2222-4222-8222-222222222202','refs/heads/main','refs/heads/main','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa');
INSERT INTO fabric_workspace_stream_bindings(project_id,fabric_instance_id,stream_id,workspace_id,attachment_ref,repository_provider,repository_immutable_id,canonical_ref,ref_name,writable) VALUES
('00000000-0000-4000-8000-000000002201','11111111-1111-4111-8111-111111112201','22222222-2222-4222-8222-222222222201','33333333-3333-4333-8333-333333333201','44444444-4444-4444-8444-444444444201','github','2201','refs/heads/main','refs/heads/main',true),
('00000000-0000-4000-8000-000000002202','11111111-1111-4111-8111-111111112201','22222222-2222-4222-8222-222222222202','33333333-3333-4333-8333-333333333202','44444444-4444-4444-8444-444444444201','github','2202','refs/heads/main','refs/heads/main',true);
SQL
	database_fingerprint >"$scratch/polluted-before"
	if psql "$database_url" -X -v ON_ERROR_STOP=1 -v VERBOSITY=verbose -1 -f migrations/000022_public_sync_v2.up.sql >"$scratch/polluted.out" 2>&1; then
		echo 'migration 22 accepted duplicate attachment references' >&2
		exit 1
	fi
	grep -Fq '55000' "$scratch/polluted.out"
	grep -Fq 'duplicate Fabric attachment references' "$scratch/polluted.out"
	database_fingerprint >"$scratch/polluted-after"
	cmp "$scratch/polluted-before" "$scratch/polluted-after"
	test "$(psql "$database_url" -X -At -c 'select version||chr(58)||dirty from schema_migrations')" = '21:false'
	psql "$database_url" -X -v ON_ERROR_STOP=1 \
		-c "DELETE FROM fabric_workspace_stream_bindings WHERE project_id IN ('00000000-0000-4000-8000-000000002201','00000000-0000-4000-8000-000000002202')" \
		-c "DELETE FROM projects WHERE id IN ('00000000-0000-4000-8000-000000002201','00000000-0000-4000-8000-000000002202')"
}

assert_up_refuses() {
	label=$1
	message=$2
	database_fingerprint >"$scratch/$label-before"
	if psql "$database_url" -X -v ON_ERROR_STOP=1 -v VERBOSITY=verbose -1 -f migrations/000022_public_sync_v2.up.sql >"$scratch/$label.out" 2>&1; then
		echo "migration 22 accepted refused fixture $label" >&2
		exit 1
	fi
	grep -Fq '55000' "$scratch/$label.out"
	grep -Fq "$message" "$scratch/$label.out"
	database_fingerprint >"$scratch/$label-after"
	cmp "$scratch/$label-before" "$scratch/$label-after"
	test "$(psql "$database_url" -X -At -c 'select version||chr(58)||dirty from schema_migrations')" = '21:false'
}

test_role_preflights() {
	psql "$database_url" -X -v ON_ERROR_STOP=1 -c 'ALTER ROLE wormhole_fabric_runtime NOLOGIN'
	assert_up_refuses malformed-runtime 'malformed wormhole_fabric_runtime'
	psql "$database_url" -X -v ON_ERROR_STOP=1 -c 'ALTER ROLE wormhole_fabric_runtime LOGIN'

	psql "$database_url" -X -v ON_ERROR_STOP=1 -c 'ALTER ROLE wormhole_attachment_resolver NOBYPASSRLS'
	assert_up_refuses malformed-resolver 'malformed wormhole_attachment_resolver'
	psql "$database_url" -X -v ON_ERROR_STOP=1 -c 'ALTER ROLE wormhole_attachment_resolver BYPASSRLS'

	psql "$database_url" -X -v ON_ERROR_STOP=1 -c 'ALTER ROLE wormhole_fabric_runtime RENAME TO wormhole_fabric_runtime_missing'
	assert_up_refuses missing-runtime 'malformed wormhole_fabric_runtime'
	psql "$database_url" -X -v ON_ERROR_STOP=1 -c 'ALTER ROLE wormhole_fabric_runtime_missing RENAME TO wormhole_fabric_runtime'

	psql "$database_url" -X -v ON_ERROR_STOP=1 -c 'ALTER ROLE wormhole_attachment_resolver RENAME TO wormhole_attachment_resolver_missing'
	assert_up_refuses missing-resolver 'malformed wormhole_attachment_resolver'
	psql "$database_url" -X -v ON_ERROR_STOP=1 -c 'ALTER ROLE wormhole_attachment_resolver_missing RENAME TO wormhole_attachment_resolver'

	psql "$database_url" -X -v ON_ERROR_STOP=1 -c 'GRANT USAGE ON SCHEMA public TO wormhole_attachment_resolver'
	assert_up_refuses resolver-privilege 'pre-existing resolver ownership or privilege'
	psql "$database_url" -X -v ON_ERROR_STOP=1 -c 'REVOKE USAGE ON SCHEMA public FROM wormhole_attachment_resolver'

	psql "$database_url" -X -v ON_ERROR_STOP=1 -c 'CREATE ROLE wormhole_runtime_parent NOLOGIN' -c 'GRANT wormhole_runtime_parent TO wormhole_fabric_runtime'
	assert_up_refuses runtime-membership 'runtime or resolver role membership'
	psql "$database_url" -X -v ON_ERROR_STOP=1 -c 'REVOKE wormhole_runtime_parent FROM wormhole_fabric_runtime' -c 'DROP ROLE wormhole_runtime_parent'
}

seed_actor_preflight_route() {
	project_id=$1
	psql "$database_url" -X -v ON_ERROR_STOP=1 \
		-c "INSERT INTO projects(id,name,owner) VALUES('$project_id','migration22-actor-preflight','test')" \
		-c "INSERT INTO project_repository_bindings(project_id,fabric_instance_id,provider,provider_repository_id,canonical_remote,default_ref,visibility) VALUES('$project_id','11111111-1111-4111-8111-111111112211','github','2211','https://github.com/test/actor','refs/heads/main','public')" \
		-c "INSERT INTO fabric_streams(project_id,fabric_instance_id,stream_id,canonical_ref,ref_name,live_tree_digest,accepted_tree_digest,accepted_commit_sha) VALUES('$project_id','11111111-1111-4111-8111-111111112211','22222222-2222-4222-8222-222222222211','refs/heads/main','refs/heads/main','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')" \
		-c "INSERT INTO fabric_stream_versions(project_id,fabric_instance_id,stream_id,canonical_ref,version,transition_kind,accepted_commit_sha,canonical_live_tree,live_tree_digest,canonical_accepted_tree,accepted_tree_digest) VALUES('$project_id','11111111-1111-4111-8111-111111112211','22222222-2222-4222-8222-222222222211','refs/heads/main',0,'initial','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','[]','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','[]','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')"
}

cleanup_preflight_route() {
	project_id=$1
	psql "$database_url" -X -v ON_ERROR_STOP=1 \
		-c "DELETE FROM fabric_public_actor_keys WHERE project_id='$project_id'" \
		-c "DELETE FROM fabric_stream_conflicts WHERE project_id='$project_id'" \
		-c "DELETE FROM fabric_workspace_stream_bindings WHERE project_id='$project_id'" \
		-c "DELETE FROM projects WHERE id='$project_id'"
}

test_actor_preflights() {
	agent_project=00000000-0000-4000-8000-000000002211
	seed_actor_preflight_route "$agent_project"
	psql "$database_url" -X -v ON_ERROR_STOP=1 -c "INSERT INTO fabric_public_actor_keys(project_id,fabric_instance_id,stream_id,canonical_ref,key_fingerprint,public_key,actor_kind,agent_id,accountable_human_id,session_id,harness_name,harness_version,model_name,model_version,source_version) VALUES('$agent_project','11111111-1111-4111-8111-111111112211','22222222-2222-4222-8222-222222222211','refs/heads/main','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',decode(repeat('aa',32),'hex'),'agent','55555555-5555-4555-8555-555555555211','66666666-6666-4666-8666-666666666211','77777777-7777-4777-8777-777777777211','codex','1','','',0)"
	assert_up_refuses agent-actor-key 'cannot normalize agent actor keys'
	cleanup_preflight_route "$agent_project"

	chronology_project=00000000-0000-4000-8000-000000002212
	seed_actor_preflight_route "$chronology_project"
	psql "$database_url" -X -v ON_ERROR_STOP=1 -c "INSERT INTO fabric_public_actor_keys(project_id,fabric_instance_id,stream_id,canonical_ref,key_fingerprint,public_key,actor_kind,human_principal_id,session_id,harness_name,harness_version,model_name,model_version,source_version,activated_at,revoked_at) VALUES('$chronology_project','11111111-1111-4111-8111-111111112211','22222222-2222-4222-8222-222222222211','refs/heads/main','sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',decode(repeat('bb',32),'hex'),'human','55555555-5555-4555-8555-555555555212','77777777-7777-4777-8777-777777777212','','','','',0,now(),now()-interval '1 second')"
	assert_up_refuses actor-chronology 'invalid actor-key chronology'
	cleanup_preflight_route "$chronology_project"
}

test_resolved_conflict_preflight() {
	project_id=00000000-0000-4000-8000-000000002213
	seed_actor_preflight_route "$project_id"
	psql "$database_url" -X -v ON_ERROR_STOP=1 -c "INSERT INTO fabric_stream_conflicts(project_id,fabric_instance_id,stream_id,canonical_ref,detected_at_version,conflict_kind,base_tree_digest,ours_tree_digest,theirs_tree_digest,detail_json,state,resolved_at) VALUES('$project_id','11111111-1111-4111-8111-111111112211','22222222-2222-4222-8222-222222222211','refs/heads/main',0,'git_base_diverged','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc','{}','resolved',now())"
	assert_up_refuses resolved-conflict 'cannot invent conflict resolution evidence'
	cleanup_preflight_route "$project_id"
}

test_down_refusal() {
	migrate -path migrations -database "$database_url" goto 22
	psql "$database_url" -X -v ON_ERROR_STOP=1 -c "INSERT INTO projects(id,name,owner) VALUES('00000000-0000-4000-8000-000000002203','migration22-down','test')" -c "INSERT INTO project_repository_bindings(project_id,fabric_instance_id,provider,provider_repository_id,canonical_remote,default_ref,visibility) VALUES('00000000-0000-4000-8000-000000002203','11111111-1111-4111-8111-111111112203','github','2203','https://github.com/test/down','refs/heads/main','public')" -c "INSERT INTO fabric_streams(project_id,fabric_instance_id,stream_id,canonical_ref,ref_name,live_tree_digest,accepted_tree_digest,accepted_commit_sha) VALUES('00000000-0000-4000-8000-000000002203','11111111-1111-4111-8111-111111112203','22222222-2222-4222-8222-222222222203','refs/heads/main','refs/heads/main','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')" -c "INSERT INTO fabric_workspace_stream_bindings(project_id,fabric_instance_id,stream_id,workspace_id,attachment_ref,repository_provider,repository_immutable_id,canonical_ref,ref_name,writable) VALUES('00000000-0000-4000-8000-000000002203','11111111-1111-4111-8111-111111112203','22222222-2222-4222-8222-222222222203','33333333-3333-4333-8333-333333333203','44444444-4444-4444-8444-444444444203','github','2203','refs/heads/main','refs/heads/main',true)"
	database_fingerprint >"$scratch/down-refusal-before"
	if psql "$database_url" -X -v ON_ERROR_STOP=1 -v VERBOSITY=verbose -1 -f migrations/000022_public_sync_v2.down.sql >"$scratch/down-refusal.out" 2>&1; then
		echo 'migration 22 down accepted a populated binding' >&2
		exit 1
	fi
	grep -Fq '55000' "$scratch/down-refusal.out"
	grep -Fq 'down refuses non-representable public sync evidence' "$scratch/down-refusal.out"
	database_fingerprint >"$scratch/down-refusal-after"
	cmp "$scratch/down-refusal-before" "$scratch/down-refusal-after"
	test "$(psql "$database_url" -X -At -c 'select version||chr(58)||dirty from schema_migrations')" = '22:false'
	psql "$database_url" -X -v ON_ERROR_STOP=1 \
		-c "DELETE FROM fabric_workspace_stream_bindings WHERE project_id='00000000-0000-4000-8000-000000002203'" \
		-c "DELETE FROM projects WHERE id='00000000-0000-4000-8000-000000002203'"
	migrate -path migrations -database "$database_url" goto 21
}

test_polluted_duplicate_attachment_ref
test_role_preflights
test_actor_preflights
test_resolved_conflict_preflight
test_down_refusal
