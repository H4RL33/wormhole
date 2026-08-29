package git

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/lib/pq"
)

var migration22Tables = append(append([]string{}, migration21Tables...), "fabric_public_agent_sessions")

func requireMigration22(t *testing.T, db *sql.DB) {
	t.Helper()
	var version int
	var dirty bool
	if err := db.QueryRow(`SELECT version,dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil {
		t.Fatalf("read migration version: %v", err)
	}
	if version != 22 || dirty {
		t.Fatalf("migration version=%d dirty=%v, want 22 false", version, dirty)
	}
}

func TestMigration22CatalogAndRuntimePrivileges(t *testing.T) {
	db := migration21DB(t)
	requireMigration22(t, db)
	for _, table := range migration22Tables {
		var enabled, forced bool
		if err := db.QueryRow(`SELECT relrowsecurity,relforcerowsecurity FROM pg_class WHERE oid=to_regclass('public.'||$1)`, table).Scan(&enabled, &forced); err != nil {
			t.Fatalf("inspect %s RLS: %v", table, err)
		}
		if !enabled || !forced {
			t.Errorf("%s RLS enabled=%v forced=%v", table, enabled, forced)
		}
	}
	wantTable := map[string]string{
		"project_repository_bindings": "SELECT", "fabric_streams": "SELECT,INSERT,UPDATE",
		"fabric_stream_versions": "SELECT,INSERT", "fabric_workspace_stream_bindings": "SELECT,INSERT,UPDATE",
		"fabric_stream_requests": "SELECT,INSERT", "fabric_stream_conflicts": "SELECT,INSERT,UPDATE",
		"fabric_public_actor_keys": "SELECT,INSERT", "public_request_nonces": "INSERT",
		"fabric_public_agent_sessions": "SELECT,INSERT,UPDATE", "audit_log": "SELECT,INSERT",
	}
	all := []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"}
	for table, csv := range wantTable {
		wanted := map[string]bool{}
		for _, privilege := range strings.Split(csv, ",") {
			wanted[privilege] = true
		}
		for _, privilege := range all {
			var got bool
			if err := db.QueryRow(`SELECT has_table_privilege('wormhole_fabric_runtime',$1,$2)`, table, privilege).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != wanted[privilege] {
				t.Errorf("runtime %s on %s=%v, want %v", privilege, table, got, wanted[privilege])
			}
		}
	}
	var usage, selectPriv, update bool
	if err := db.QueryRow(`SELECT has_sequence_privilege('wormhole_fabric_runtime','audit_log_seq_seq','USAGE'),has_sequence_privilege('wormhole_fabric_runtime','audit_log_seq_seq','SELECT'),has_sequence_privilege('wormhole_fabric_runtime','audit_log_seq_seq','UPDATE')`).Scan(&usage, &selectPriv, &update); err != nil {
		t.Fatal(err)
	}
	if !usage || selectPriv || update {
		t.Fatalf("runtime sequence privileges usage=%v select=%v update=%v", usage, selectPriv, update)
	}
	var owner string
	var definer, runtime, public bool
	if err := db.QueryRow(`SELECT pg_get_userbyid(p.proowner),p.prosecdef,has_function_privilege('wormhole_fabric_runtime',p.oid,'EXECUTE'),has_function_privilege(0,p.oid,'EXECUTE') FROM pg_proc p WHERE p.oid='fabric_resolve_attachment_project_v1(uuid,uuid)'::regprocedure`).Scan(&owner, &definer, &runtime, &public); err != nil {
		t.Fatal(err)
	}
	if owner != "wormhole_attachment_resolver" || !definer || !runtime || public {
		t.Fatalf("resolver owner=%q definer=%v runtime=%v public=%v", owner, definer, runtime, public)
	}
}

func TestMigration22ResolverReturnsOnlyLiveProjectWithoutProjectGUC(t *testing.T) {
	db := migration21DB(t)
	requireMigration22(t, db)
	projectID := migration21CreateProject(t, db, "migration22-resolver")
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM fabric_workspace_stream_bindings WHERE project_id=$1`, projectID)
		_, _ = db.Exec(`DELETE FROM projects WHERE id=$1`, projectID)
	})
	instanceID := "11111111-1111-4111-8111-111111112201"
	streamID := "22222222-2222-4222-8222-222222222201"
	workspaceID := "33333333-3333-4333-8333-333333333201"
	attachment := "44444444-4444-4444-8444-444444444201"
	migration21SeedStream(t, db, projectID, instanceID, streamID, "refs/heads/main")
	migration21SeedWorkspaceWithAttachment(t, db, projectID, instanceID, streamID, workspaceID, attachment, "refs/heads/main")
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SET LOCAL ROLE wormhole_fabric_runtime`); err != nil {
		t.Fatal(err)
	}
	var got sql.NullString
	if err := tx.QueryRow(`SELECT fabric_resolve_attachment_project_v1($1,$2)`, instanceID, attachment).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Valid || got.String != projectID {
		t.Fatalf("resolved project=%+v, want %s", got, projectID)
	}
	if err := tx.QueryRow(`SELECT fabric_resolve_attachment_project_v1($1,$2)`, instanceID, "44444444-4444-4444-8444-444444444299").Scan(&got); err != nil || got.Valid {
		t.Fatalf("unknown attachment=%+v err=%v", got, err)
	}
	var visible int
	if err := tx.QueryRow(`SELECT count(*) FROM fabric_workspace_stream_bindings`).Scan(&visible); err != nil {
		t.Fatal(err)
	}
	if visible != 0 {
		t.Fatalf("runtime saw %d binding rows without a project GUC", visible)
	}
}

func TestMigration22SessionRLSRejectsCrossProjectAndOrdinaryOwnerBypass(t *testing.T) {
	db := migration21DB(t)
	requireMigration22(t, db)
	lockRLSFixture(t, db)
	projectA := migration21CreateProject(t, db, "migration22-session-a")
	projectB := migration21CreateProject(t, db, "migration22-session-b")
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM projects WHERE id IN ($1,$2)`, projectA, projectB) })
	for _, statement := range []string{
		`CREATE ROLE wormhole_session_rls_test NOLOGIN NOSUPERUSER NOBYPASSRLS NOINHERIT`,
		`GRANT SELECT,INSERT,UPDATE ON fabric_public_agent_sessions TO wormhole_session_rls_test`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`REVOKE ALL ON fabric_public_agent_sessions FROM wormhole_session_rls_test; DROP ROLE wormhole_session_rls_test`)
	})
	var originalOwner string
	if err := db.QueryRow(`SELECT pg_get_userbyid(relowner) FROM pg_class WHERE oid='fabric_public_agent_sessions'::regclass`).Scan(&originalOwner); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{query: `ALTER TABLE fabric_public_agent_sessions OWNER TO wormhole_session_rls_test`},
		{query: `SET LOCAL ROLE wormhole_session_rls_test`},
		{query: `SELECT set_config('wormhole.project_id',$1,true)`, args: []any{projectA}},
	} {
		if _, err := tx.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := tx.QueryRow(`SELECT count(*) FROM fabric_public_agent_sessions WHERE project_id=$1`, projectB).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("ordinary owner saw %d cross-project rows", count)
	}
	if _, err := tx.Exec(`SAVEPOINT cross_project_insert`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO fabric_public_agent_sessions(project_id,fabric_instance_id,stream_id,canonical_ref,workspace_id,attachment_ref,issuer_key_fingerprint,agent_id,accountable_human_id,source_version,harness_name,harness_version,expires_at) VALUES($1,gen_random_uuid(),gen_random_uuid(),'refs/heads/main',gen_random_uuid(),gen_random_uuid(),$2,gen_random_uuid(),gen_random_uuid(),0,'codex','1',now()+interval '24 hours')`, projectB, "sha256:"+strings.Repeat("a", 64)); err == nil || pqCode(err) != "42501" {
		t.Fatalf("cross-project owner insert error=%v, want 42501", err)
	}
	if _, err := tx.Exec(`ROLLBACK TO SAVEPOINT cross_project_insert`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`RESET ROLE`, `ALTER TABLE fabric_public_agent_sessions OWNER TO ` + pq.QuoteIdentifier(originalOwner)} {
		if _, err := tx.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigration22AuditIsImmutableIncludingCascade(t *testing.T) {
	db := migration21DB(t)
	requireMigration22(t, db)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var projectID, agentID string
	if err := tx.QueryRow(`INSERT INTO projects(name,owner) VALUES('migration22-audit','test') RETURNING id`).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(`INSERT INTO agents(owner,model) VALUES('test','test') RETURNING id`).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO audit_log(agent_id,project_id,action) VALUES($1,$2,'legacy')`, agentID, projectID); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`UPDATE audit_log SET action=action WHERE project_id=$1`, `DELETE FROM audit_log WHERE project_id=$1`, `DELETE FROM projects WHERE id=$1`} {
		if _, err := tx.Exec(`SAVEPOINT audit_immutable`); err != nil {
			t.Fatal(err)
		}
		_, err := tx.Exec(statement, projectID)
		var pqErr *pq.Error
		if !errors.As(err, &pqErr) || string(pqErr.Code) != "55000" {
			t.Fatalf("%s error=%v, want 55000", statement, err)
		}
		if _, err := tx.Exec(`ROLLBACK TO SAVEPOINT audit_immutable`); err != nil {
			t.Fatal(err)
		}
	}
}

type migration22DownRoute struct{ projectID, fabricID, streamID, workspaceID, attachmentRef, humanID, agentID, operationID string }

func migration22SeedDownRoute(t *testing.T, tx *sql.Tx) migration22DownRoute {
	t.Helper()
	route := migration22DownRoute{"00000000-0000-4000-8000-000000002221", "11111111-1111-4111-8111-111111112221", "22222222-2222-4222-8222-222222222221", "33333333-3333-4333-8333-333333333221", "44444444-4444-4444-8444-444444444221", "55555555-5555-4555-8555-555555555221", "66666666-6666-4666-8666-666666666221", "77777777-7777-4777-8777-777777777221"}
	digest := "sha256:" + strings.Repeat("a", 64)
	commit := strings.Repeat("a", 40)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO projects(id,name,owner) VALUES($1,'migration22-down-matrix','test')`, []any{route.projectID}},
		{`INSERT INTO project_repository_bindings(project_id,fabric_instance_id,provider,provider_repository_id,canonical_remote,default_ref,visibility) VALUES($1,$2,'github','2221','https://github.com/test/down-matrix','refs/heads/main','public')`, []any{route.projectID, route.fabricID}},
		{`INSERT INTO fabric_streams(project_id,fabric_instance_id,stream_id,canonical_ref,ref_name,live_tree_digest,accepted_tree_digest,accepted_commit_sha) VALUES($1,$2,$3,'refs/heads/main','refs/heads/main',$4,$4,$5)`, []any{route.projectID, route.fabricID, route.streamID, digest, commit}},
		{`INSERT INTO fabric_stream_versions(project_id,fabric_instance_id,stream_id,canonical_ref,version,transition_kind,accepted_commit_sha,canonical_live_tree,live_tree_digest,canonical_accepted_tree,accepted_tree_digest) VALUES($1,$2,$3,'refs/heads/main',0,'initial',$5,'{}',$4,'{}',$4)`, []any{route.projectID, route.fabricID, route.streamID, digest, commit}},
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed down route: %v", err)
		}
	}
	return route
}
func migration22SeedDownBinding(t *testing.T, tx *sql.Tx, r migration22DownRoute) {
	t.Helper()
	if _, err := tx.Exec(`INSERT INTO fabric_workspace_stream_bindings(project_id,fabric_instance_id,stream_id,workspace_id,attachment_ref,repository_provider,repository_immutable_id,canonical_ref,ref_name,writable) VALUES($1,$2,$3,$4,$5,'github','2221','refs/heads/main','refs/heads/main',true)`, r.projectID, r.fabricID, r.streamID, r.workspaceID, r.attachmentRef); err != nil {
		t.Fatalf("seed down binding: %v", err)
	}
}
func migration22SeedDownActor(t *testing.T, tx *sql.Tx, r migration22DownRoute) string {
	t.Helper()
	f := "sha256:" + strings.Repeat("b", 64)
	if _, err := tx.Exec(`INSERT INTO fabric_public_actor_keys(project_id,fabric_instance_id,stream_id,canonical_ref,key_fingerprint,public_key,actor_kind,human_principal_id,source_version,harness_name,harness_version,model_name,model_version) VALUES($1,$2,$3,'refs/heads/main',$4,decode(repeat('bb',32),'hex'),'human',$5,0,'','','','')`, r.projectID, r.fabricID, r.streamID, f, r.humanID); err != nil {
		t.Fatalf("seed down actor: %v", err)
	}
	return f
}
func migration22DownFingerprint(t *testing.T, tx *sql.Tx, projectID string) string {
	t.Helper()
	var value string
	err := tx.QueryRow(`SELECT md5(jsonb_build_object('version',(SELECT to_jsonb(v) FROM (SELECT version,dirty FROM schema_migrations) v),'catalog',(SELECT coalesce(jsonb_agg(to_jsonb(c) ORDER BY c.kind,c.name),'[]'::jsonb) FROM (SELECT 'relation' kind,relname name,relkind::text detail FROM pg_class WHERE relnamespace='public'::regnamespace UNION ALL SELECT 'constraint',conname,pg_get_constraintdef(oid,true) FROM pg_constraint WHERE connamespace='public'::regnamespace UNION ALL SELECT 'function',proname||'('||pg_get_function_identity_arguments(oid)||')',pg_get_functiondef(oid) FROM pg_proc WHERE pronamespace='public'::regnamespace AND prokind IN ('f','p'))c),'bindings',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.attachment_ref),'[]'::jsonb) FROM fabric_workspace_stream_bindings x WHERE x.project_id=$1),'actors',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.key_fingerprint),'[]'::jsonb) FROM fabric_public_actor_keys x WHERE x.project_id=$1),'sessions',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.session_id),'[]'::jsonb) FROM fabric_public_agent_sessions x WHERE x.project_id=$1),'conflicts',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.conflict_id),'[]'::jsonb) FROM fabric_stream_conflicts x WHERE x.project_id=$1),'audit',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.seq),'[]'::jsonb) FROM audit_log x WHERE x.project_id=$1))::text)`, projectID).Scan(&value)
	if err != nil {
		t.Fatalf("fingerprint down fixture: %v", err)
	}
	return value
}
func TestMigration22DownRefusalMatrix(t *testing.T) {
	db := migration21DB(t)
	requireMigration22(t, db)
	downSQL, err := os.ReadFile("../../../migrations/000022_public_sync_v2.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*testing.T, *sql.Tx, migration22DownRoute){"actor key": func(t *testing.T, tx *sql.Tx, r migration22DownRoute) { migration22SeedDownActor(t, tx, r) }, "binding": func(t *testing.T, tx *sql.Tx, r migration22DownRoute) { migration22SeedDownBinding(t, tx, r) }, "session": func(t *testing.T, tx *sql.Tx, r migration22DownRoute) {
		migration22SeedDownBinding(t, tx, r)
		f := migration22SeedDownActor(t, tx, r)
		if _, err := tx.Exec(`UPDATE fabric_workspace_stream_bindings SET source_version=0,public_issuer_key_fingerprint=$1 WHERE project_id=$2`, f, r.projectID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO fabric_public_agent_sessions(project_id,fabric_instance_id,stream_id,canonical_ref,workspace_id,attachment_ref,issuer_key_fingerprint,agent_id,accountable_human_id,source_version,harness_name,harness_version,issued_at,expires_at) VALUES($1,$2,$3,'refs/heads/main',$4,$5,$6,$7,$8,0,'codex','1','2026-08-29T12:00:00Z','2026-08-30T12:00:00Z')`, r.projectID, r.fabricID, r.streamID, r.workspaceID, r.attachmentRef, f, r.agentID, r.humanID); err != nil {
			t.Fatal(err)
		}
	}, "resolved conflict": func(t *testing.T, tx *sql.Tx, r migration22DownRoute) {
		migration22SeedDownBinding(t, tx, r)
		d := "sha256:" + strings.Repeat("c", 64)
		commit := strings.Repeat("c", 40)
		statements := []struct {
			query string
			args  []any
		}{
			{`INSERT INTO fabric_stream_requests(project_id,fabric_instance_id,stream_id,workspace_id,ref_name,operation_id,canonical_operation_json,operation_digest,expected_stream_version,expected_tree_digest,result,result_stream_version,actor_envelope_json) VALUES($1,$2,$3,$4,'refs/heads/main',$5,'{}',$6,0,$6,'applied',1,'{}')`, []any{r.projectID, r.fabricID, r.streamID, r.workspaceID, r.operationID, d}},
			{`INSERT INTO fabric_stream_versions(project_id,fabric_instance_id,stream_id,canonical_ref,version,transition_kind,accepted_commit_sha,canonical_live_tree,live_tree_digest,canonical_accepted_tree,accepted_tree_digest,operation_id,canonical_operation_json,operation_digest,actor_envelope_json) VALUES($1,$2,$3,'refs/heads/main',1,'operation',$6,'{}',$5,'{}',$5,$4,'{}',$5,'{}')`, []any{r.projectID, r.fabricID, r.streamID, r.operationID, d, commit}},
			{`INSERT INTO fabric_stream_conflicts(project_id,fabric_instance_id,stream_id,canonical_ref,detected_at_version,conflict_kind,base_tree_digest,ours_tree_digest,theirs_tree_digest,detail_json,state,resolved_at,resolution_operation_id,resolution_version) VALUES($1,$2,$3,'refs/heads/main',0,'git_base_diverged',$5,$5,$5,'{}','resolved','2026-08-29T12:00:00Z',$4,1)`, []any{r.projectID, r.fabricID, r.streamID, r.operationID, d}},
		}
		for _, statement := range statements {
			if _, err := tx.Exec(statement.query, statement.args...); err != nil {
				t.Fatal(err)
			}
		}
	}, "typed audit": func(t *testing.T, tx *sql.Tx, r migration22DownRoute) {
		if _, err := tx.Exec(`INSERT INTO audit_log(project_id,action,actor_kind,human_principal_id,assurance,occurred_at,actor_envelope_json,canonical_payload_json,request_digest) VALUES($1,'public.test','human',$2,'public-key-continuity','2026-08-29T12:00:00Z','{}','{}','sha256:'||encode(digest('{}','sha256'),'hex'))`, r.projectID, r.humanID); err != nil {
			t.Fatal(err)
		}
	}}
	for name, fixture := range tests {
		t.Run(name, func(t *testing.T) {
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			r := migration22SeedDownRoute(t, tx)
			fixture(t, tx, r)
			before := migration22DownFingerprint(t, tx, r.projectID)
			if _, err := tx.Exec(`SAVEPOINT down_refusal`); err != nil {
				t.Fatal(err)
			}
			_, err = tx.Exec(string(downSQL))
			if pqCode(err) != "55000" {
				t.Fatalf("down error=%v, want 55000", err)
			}
			if _, err := tx.Exec(`ROLLBACK TO SAVEPOINT down_refusal`); err != nil {
				t.Fatal(err)
			}
			if after := migration22DownFingerprint(t, tx, r.projectID); after != before {
				t.Fatalf("down refusal changed fingerprint: before=%s after=%s", before, after)
			}
		})
	}
}
func pqCode(err error) string {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return string(pqErr.Code)
	}
	return ""
}
