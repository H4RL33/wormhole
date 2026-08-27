package git

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

var migration21Tables = []string{
	"project_repository_bindings",
	"fabric_streams",
	"fabric_stream_versions",
	"fabric_workspace_stream_bindings",
	"fabric_stream_requests",
	"fabric_stream_conflicts",
	"fabric_public_actor_keys",
	"public_request_nonces",
	"fabric_activity_policy_versions",
	"fabric_activity_policy_current",
	"fabric_activity_stream_sequences",
	"fabric_activities",
	"fabric_activity_ingress_receipts",
	"fabric_activity_lifecycle",
}

func migration21DB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", types.LoadConfig().DatabaseURL)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		if os.Getenv("WORMHOLE_INTEGRATION_REQUIRED") == "1" {
			t.Fatalf("postgres required but not reachable: %v", err)
		}
		t.Skipf("postgres not reachable: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func requireMigration21(t *testing.T, db *sql.DB) {
	t.Helper()
	var version int
	var dirty bool
	if err := db.QueryRow(`SELECT version, dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil {
		t.Fatalf("read migration version: %v", err)
	}
	if version != 21 || dirty {
		t.Fatalf("migration version = %d dirty=%v, want 21 false", version, dirty)
	}
	for _, table := range migration21Tables {
		var exists bool
		if err := db.QueryRow(`SELECT to_regclass('public.' || $1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatalf("look up %s: %v", table, err)
		}
		if !exists {
			t.Errorf("migration 21 relation %s is absent", table)
		}
	}
}

func TestMigration21StoresEveryVersionTreeAndOperationBytes(t *testing.T) {
	db := migration21DB(t)
	requireMigration21(t, db)

	projectID := migration21CreateProject(t, db, "migration21-version-bytes")
	instanceID := "11111111-1111-4111-8111-111111111111"
	streamID := "22222222-2222-4222-8222-222222222222"
	ref := "refs/heads/main"
	migration21SeedStream(t, db, projectID, instanceID, streamID, ref)

	initialLive := []byte("initial-live\n")
	initialAccepted := []byte("initial-accepted\n")
	operationLive := []byte("operation-live\n")
	operationAccepted := []byte("operation-accepted\n")
	operationJSON := []byte("{\"schema_version\":1}\n")
	actorJSON := []byte("{\"actor_kind\":\"human\"}\n")
	_, err := db.Exec(`INSERT INTO fabric_stream_versions
		(project_id,fabric_instance_id,stream_id,canonical_ref,version,transition_kind,
		 accepted_commit_sha,canonical_live_tree,live_tree_digest,canonical_accepted_tree,
		 accepted_tree_digest,operation_id,canonical_operation_json,operation_digest,actor_envelope_json)
		VALUES ($1,$2,$3,$4,0,'initial',$5,$6,$7,$8,$9,NULL,NULL,NULL,NULL),
		       ($1,$2,$3,$4,1,'operation',$5,$10,$11,$12,$13,$14,$15,$16,$17)`,
		projectID, instanceID, streamID, ref, strings.Repeat("a", 40),
		initialLive, testDigest("1"), initialAccepted, testDigest("2"),
		operationLive, testDigest("3"), operationAccepted, testDigest("4"),
		"33333333-3333-4333-8333-333333333333", operationJSON, testDigest("5"), actorJSON)
	if err != nil {
		t.Fatalf("insert version trees: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM fabric_stream_versions WHERE project_id=$1`, projectID) })

	rows, err := db.Query(`SELECT canonical_live_tree,canonical_accepted_tree,canonical_operation_json,actor_envelope_json
		FROM fabric_stream_versions WHERE project_id=$1 ORDER BY version`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got [][4][]byte
	for rows.Next() {
		var row [4][]byte
		if err := rows.Scan(&row[0], &row[1], &row[2], &row[3]); err != nil {
			t.Fatal(err)
		}
		got = append(got, row)
	}
	if len(got) != 2 || string(got[0][0]) != string(initialLive) || string(got[0][1]) != string(initialAccepted) || got[0][2] != nil || got[0][3] != nil || string(got[1][0]) != string(operationLive) || string(got[1][1]) != string(operationAccepted) || string(got[1][2]) != string(operationJSON) || string(got[1][3]) != string(actorJSON) {
		t.Fatalf("version bytes were not retained exactly: %#v", got)
	}
}

func TestMigration21DirectSQLRejectsCrossProjectStreamFKs(t *testing.T) {
	db := migration21DB(t)
	requireMigration21(t, db)
	a := migration21CreateProject(t, db, "migration21-cross-project-a")
	b := migration21CreateProject(t, db, "migration21-cross-project-b")
	instanceID := "11111111-1111-4111-8111-111111111112"
	migration21SeedRepository(t, db, a, instanceID)
	_, err := db.Exec(`INSERT INTO fabric_streams
		(project_id,fabric_instance_id,stream_id,canonical_ref,ref_name,current_version,live_tree_digest,accepted_tree_digest,accepted_commit_sha)
		VALUES ($1,$2,$3,'refs/heads/main','refs/heads/main',0,$4,$5,$6)`,
		b, instanceID, "22222222-2222-4222-8222-222222222223", testDigest("1"), testDigest("2"), strings.Repeat("a", 40))
	requireSQLState(t, err, "23503")
}

func TestMigration21DirectSQLRejectsCrossStreamWorkspaceAndRequestFKs(t *testing.T) {
	db := migration21DB(t)
	requireMigration21(t, db)
	projectID := migration21CreateProject(t, db, "migration21-cross-stream")
	instanceID := "11111111-1111-4111-8111-111111111113"
	streamA := "22222222-2222-4222-8222-222222222224"
	streamB := "22222222-2222-4222-8222-222222222225"
	migration21SeedStream(t, db, projectID, instanceID, streamA, "refs/heads/main")
	migration21SeedStream(t, db, projectID, instanceID, streamB, "refs/heads/topic")
	workspaceID := "33333333-3333-4333-8333-333333333334"
	migration21SeedWorkspace(t, db, projectID, instanceID, streamA, workspaceID, "refs/heads/main")
	_, err := db.Exec(`INSERT INTO fabric_stream_requests
		(project_id,fabric_instance_id,stream_id,workspace_id,ref_name,operation_id,canonical_operation_json,operation_digest,
		 expected_stream_version,expected_tree_digest,result,result_stream_version,actor_envelope_json)
		VALUES ($1,$2,$3,$4,'refs/heads/topic',$5,$6,$7,0,$8,'rejected',0,$9)`,
		projectID, instanceID, streamB, workspaceID, "44444444-4444-4444-8444-444444444444", []byte("{}\n"), testDigest("1"), testDigest("2"), []byte("{}\n"))
	requireSQLState(t, err, "23503")
}

func TestMigration21ActivityDirectSQLRejectsCrossProjectStreamAndWorkspaceFKs(t *testing.T) {
	db := migration21DB(t)
	requireMigration21(t, db)
	a := migration21CreateProject(t, db, "migration21-activity-a")
	b := migration21CreateProject(t, db, "migration21-activity-b")
	instanceID := "11111111-1111-4111-8111-111111111114"
	streamID := "22222222-2222-4222-8222-222222222226"
	workspaceID := "33333333-3333-4333-8333-333333333335"
	migration21SeedStream(t, db, a, instanceID, streamID, "refs/heads/main")
	migration21SeedWorkspace(t, db, a, instanceID, streamID, workspaceID, "refs/heads/main")
	_, err := db.Exec(`INSERT INTO fabric_activities
		(project_id,fabric_instance_id,stream_id,canonical_ref,source_workspace_id,activity_id,sequence,activity_class,
		 canonical_activity_json,activity_digest,source_actor_json,event_channel_id,event_actor_id,event_type,
		 event_payload_json,event_created_at,created_at)
		VALUES ($1,$2,$3,'refs/heads/main',$4,$5,1,'ordinary',$6,$7,$8,$9,$10,'message.posted',$11,now(),now())`,
		b, instanceID, streamID, workspaceID, "44444444-4444-4444-8444-444444444445", []byte("{}\n"), testDigest("1"), []byte("{}\n"),
		"66666666-6666-4666-8666-666666666666", "77777777-7777-4777-8777-777777777777", []byte("{}"))
	requireSQLState(t, err, "23503")
}

func TestMigration21ForcesRLSForEveryProjectTable(t *testing.T) {
	db := migration21DB(t)
	requireMigration21(t, db)
	for _, table := range migration21Tables {
		var enabled, forced bool
		if err := db.QueryRow(`SELECT relrowsecurity,relforcerowsecurity FROM pg_class WHERE oid=to_regclass('public.'||$1)`, table).Scan(&enabled, &forced); err != nil {
			t.Fatalf("inspect RLS for %s: %v", table, err)
		}
		if !enabled || !forced {
			t.Errorf("%s RLS enabled=%v forced=%v", table, enabled, forced)
		}
		var usingExpr, checkExpr string
		if err := db.QueryRow(`SELECT pg_get_expr(polqual,polrelid),pg_get_expr(polwithcheck,polrelid)
			FROM pg_policy WHERE polrelid=to_regclass('public.'||$1)`, table).Scan(&usingExpr, &checkExpr); err != nil {
			t.Fatalf("inspect policy for %s: %v", table, err)
		}
		for label, expr := range map[string]string{"USING": usingExpr, "WITH CHECK": checkExpr} {
			const expected = "(project_id = (NULLIF(current_setting('wormhole.project_id'::text, true), ''::text))::uuid)"
			if expr != expected {
				t.Errorf("%s %s predicate = %q", table, label, expr)
			}
		}
	}
}

func TestMigration21ActivityRolesAndPrivileges(t *testing.T) {
	db := migration21DB(t)
	requireMigration21(t, db)
	for _, role := range []string{"wormhole_activity_owner", "wormhole_fabric_runtime", "wormhole_activity_maintenance"} {
		var superuser, bypass bool
		if err := db.QueryRow(`SELECT rolsuper,rolbypassrls FROM pg_roles WHERE rolname=$1`, role).Scan(&superuser, &bypass); err != nil {
			t.Fatalf("role %s: %v", role, err)
		}
		if superuser || bypass {
			t.Errorf("role %s superuser=%v bypassrls=%v", role, superuser, bypass)
		}
	}
	for _, table := range []string{"fabric_activity_policy_versions", "fabric_activity_policy_current", "fabric_activity_stream_sequences", "fabric_activities", "fabric_activity_ingress_receipts", "fabric_activity_lifecycle"} {
		var owner string
		if err := db.QueryRow(`SELECT pg_get_userbyid(relowner) FROM pg_class WHERE oid=to_regclass('public.'||$1)`, table).Scan(&owner); err != nil || owner != "wormhole_activity_owner" {
			t.Errorf("%s owner=(%q,%v), want wormhole_activity_owner", table, owner, err)
		}
		var runtimeSelect, runtimeInsert, maintenanceDelete bool
		if err := db.QueryRow(`SELECT has_table_privilege('wormhole_fabric_runtime',$1,'SELECT'),
			has_table_privilege('wormhole_fabric_runtime',$1,'INSERT'),
			has_table_privilege('wormhole_activity_maintenance',$1,'DELETE')`, table).Scan(&runtimeSelect, &runtimeInsert, &maintenanceDelete); err != nil {
			t.Fatal(err)
		}
		if !runtimeSelect || runtimeInsert || maintenanceDelete {
			t.Errorf("%s privileges runtime_select=%v runtime_insert=%v maintenance_delete=%v", table, runtimeSelect, runtimeInsert, maintenanceDelete)
		}
	}
	wanted := map[string][2]bool{
		"fabric_accept_activity_v1":               {true, false},
		"fabric_transition_activity_lifecycle_v1": {true, false},
		"fabric_publish_activity_policy_v1":       {true, false},
		"fabric_prune_activities_v1":              {false, true},
	}
	for name, grants := range wanted {
		var owner string
		var runtime, maintenance, public bool
		if err := db.QueryRow(`SELECT pg_get_userbyid(p.proowner),
			has_function_privilege('wormhole_fabric_runtime',p.oid,'EXECUTE'),
			has_function_privilege('wormhole_activity_maintenance',p.oid,'EXECUTE'),
			has_function_privilege('public',p.oid,'EXECUTE')
			FROM pg_proc p WHERE p.pronamespace='public'::regnamespace AND p.proname=$1`, name).Scan(&owner, &runtime, &maintenance, &public); err != nil {
			t.Fatalf("function %s: %v", name, err)
		}
		if owner != "wormhole_activity_owner" || runtime != grants[0] || maintenance != grants[1] || public {
			t.Errorf("function %s owner=%s runtime=%v maintenance=%v public=%v", name, owner, runtime, maintenance, public)
		}
	}

	projectID := migration21CreateProject(t, db, "migration21-role-execution")
	otherProjectID := migration21CreateProject(t, db, "migration21-role-isolation")
	instanceID := "11111111-1111-4111-8111-111111111119"
	streamID := "22222222-2222-4222-8222-222222222229"
	workspaceID := "33333333-3333-4333-8333-333333333339"
	migration21SeedStream(t, db, projectID, instanceID, streamID, "refs/heads/main")
	migration21SeedWorkspaceWithAttachment(t, db, projectID, instanceID, streamID, workspaceID,
		"44444444-4444-4444-8444-444444444449", "refs/heads/main")
	stream := FabricActivityStreamKey{ProjectID: projectID, FabricInstanceID: instanceID, StreamID: streamID, CanonicalRef: "refs/heads/main"}
	policy := testActivityPolicy(1, 2_592_000)
	if _, err := NewActivityStore(db).PublishPolicy(context.Background(), stream, policy); err != nil {
		t.Fatalf("bootstrap role-test policy: %v", err)
	}

	runtimeTx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer runtimeTx.Rollback()
	if _, err := runtimeTx.Exec(`SET LOCAL ROLE wormhole_fabric_runtime`); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeTx.Exec(`SELECT set_config('wormhole.project_id',$1,true)`, projectID); err != nil {
		t.Fatal(err)
	}
	var visible int
	if err := runtimeTx.QueryRow(`SELECT count(*) FROM fabric_activity_policy_current`).Scan(&visible); err != nil || visible != 1 {
		t.Fatalf("runtime same-project policy rows=%d err=%v", visible, err)
	}
	if _, err := runtimeTx.Exec(`SELECT set_config('wormhole.project_id',$1,true)`, otherProjectID); err != nil {
		t.Fatal(err)
	}
	if err := runtimeTx.QueryRow(`SELECT count(*) FROM fabric_activity_policy_current`).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("runtime cross-project policy rows=%d err=%v", visible, err)
	}
	if err := runtimeTx.Rollback(); err != nil {
		t.Fatal(err)
	}

	directMutationTx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := directMutationTx.Exec(`SET LOCAL ROLE wormhole_fabric_runtime`); err != nil {
		t.Fatal(err)
	}
	if _, err := directMutationTx.Exec(`SELECT set_config('wormhole.project_id',$1,true)`, projectID); err != nil {
		t.Fatal(err)
	}
	_, err = directMutationTx.Exec(`UPDATE fabric_activity_policy_current SET policy_version=policy_version WHERE project_id=$1`, projectID)
	requireSQLState(t, err, "42501")
	_ = directMutationTx.Rollback()

	maintenanceTx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer maintenanceTx.Rollback()
	if _, err := maintenanceTx.Exec(`SET LOCAL ROLE wormhole_activity_maintenance`); err != nil {
		t.Fatal(err)
	}
	if _, err := maintenanceTx.Exec(`SELECT set_config('wormhole.project_id',$1,true)`, projectID); err != nil {
		t.Fatal(err)
	}
	var pruned int
	if err := maintenanceTx.QueryRow(`SELECT fabric_prune_activities_v1($1,$2,$3,$4,$5,1)`,
		projectID, instanceID, streamID, "refs/heads/main", workspaceID).Scan(&pruned); err != nil || pruned != 0 {
		t.Fatalf("maintenance pruner=(%d,%v), want 0,nil", pruned, err)
	}
}

func TestMigration21RejectsImmutableHistoryUpdateAndDirectActivityDelete(t *testing.T) {
	db := migration21DB(t)
	requireMigration21(t, db)
	for _, table := range []string{"fabric_stream_versions", "fabric_stream_requests", "fabric_activity_policy_versions", "fabric_activities", "fabric_activity_ingress_receipts"} {
		var triggerCount int
		if err := db.QueryRow(`SELECT count(*) FROM pg_trigger WHERE tgrelid=to_regclass('public.'||$1) AND NOT tgisinternal AND tgenabled <> 'D'`, table).Scan(&triggerCount); err != nil {
			t.Fatal(err)
		}
		if triggerCount == 0 {
			t.Errorf("%s lacks immutable history trigger", table)
		}
	}
	for _, table := range []string{"fabric_activities", "fabric_activity_ingress_receipts", "fabric_activity_lifecycle"} {
		var allowed bool
		if err := db.QueryRow(`SELECT has_table_privilege('wormhole_fabric_runtime',$1,'DELETE')`, table).Scan(&allowed); err != nil {
			t.Fatal(err)
		}
		if allowed {
			t.Errorf("runtime can directly delete from %s", table)
		}
	}

	projectID := migration21CreateProject(t, db, "migration21-immutable-activity")
	instanceID := "11111111-1111-4111-8111-111111111118"
	streamID := "22222222-2222-4222-8222-222222222228"
	workspaceID := "33333333-3333-4333-8333-333333333338"
	migration21SeedStream(t, db, projectID, instanceID, streamID, "refs/heads/main")
	migration21SeedWorkspaceWithAttachment(t, db, projectID, instanceID, streamID, workspaceID,
		"44444444-4444-4444-8444-444444444448", "refs/heads/main")
	stream := FabricActivityStreamKey{ProjectID: projectID, FabricInstanceID: instanceID, StreamID: streamID, CanonicalRef: "refs/heads/main"}
	store := NewActivityStore(db)
	policy := testActivityPolicy(1, 2_592_000)
	if _, err := store.PublishPolicy(context.Background(), stream, policy); err != nil {
		t.Fatal(err)
	}
	actor := testActivityActor(time.Date(2026, 8, 27, 13, 0, 0, 0, time.UTC))
	activity := testOrdinaryActivity("55555555-5555-4555-8555-555555555558", actor, "immutable")
	policyDigest, _ := projectstate.DigestActivityPolicy(policy)
	_, err := store.Accept(context.Background(), AcceptActivityInput{
		Key:      FabricActivityOriginKey{Stream: stream, SourceWorkspaceID: workspaceID, ActivityID: activity.ID},
		Activity: activity, IssuedActor: actor, PolicyVersion: 1, PolicyDigest: policyDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE fabric_activity_policy_versions SET canonical_policy_json=canonical_policy_json WHERE project_id=$1`,
		`UPDATE fabric_activities SET canonical_activity_json=canonical_activity_json WHERE project_id=$1`,
		`UPDATE fabric_activity_ingress_receipts SET activity_digest=activity_digest WHERE project_id=$1`,
	} {
		_, err := db.Exec(statement, projectID)
		requireSQLState(t, err, "55000")
	}

	runtimeTx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeTx.Exec(`SET LOCAL ROLE wormhole_fabric_runtime`); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeTx.Exec(`SELECT set_config('wormhole.project_id',$1,true)`, projectID); err != nil {
		t.Fatal(err)
	}
	_, err = runtimeTx.Exec(`DELETE FROM fabric_activities WHERE project_id=$1`, projectID)
	requireSQLState(t, err, "42501")
	_ = runtimeTx.Rollback()
}

func TestMigration21ContainsNoActivityPromotionAuthority(t *testing.T) {
	db := migration21DB(t)
	requireMigration21(t, db)
	var names []string
	rows, err := db.Query(`SELECT proname FROM pg_proc WHERE pronamespace='public'::regnamespace AND proname LIKE '%promot%'
		UNION ALL SELECT relname FROM pg_class WHERE relnamespace='public'::regnamespace AND relname LIKE '%promot%'
		ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if len(names) != 0 {
		t.Fatalf("Fabric contains promotion-shaped authority: %v", names)
	}
	for _, table := range []string{"fabric_activities", "fabric_activity_ingress_receipts", "fabric_activity_lifecycle"} {
		var forbidden []string
		rows, err := db.Query(`SELECT column_name FROM information_schema.columns WHERE table_schema='public' AND table_name=$1
			AND (column_name LIKE '%promot%' OR column_name IN ('event_id','operation_id')) ORDER BY column_name`, table)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var column string
			_ = rows.Scan(&column)
			forbidden = append(forbidden, column)
		}
		rows.Close()
		if len(forbidden) != 0 {
			t.Errorf("%s has promotion-shaped columns: %v", table, forbidden)
		}
	}
}

func TestMigration21DownLeavesVersion20Shape(t *testing.T) {
	db := migration21DB(t)
	var version int
	var dirty bool
	if err := db.QueryRow(`SELECT version,dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil {
		t.Fatal(err)
	}
	if version == 20 {
		for _, table := range migration21Tables {
			var exists bool
			if err := db.QueryRow(`SELECT to_regclass('public.'||$1) IS NOT NULL`, table).Scan(&exists); err != nil {
				t.Fatal(err)
			}
			if exists {
				t.Errorf("migration-21 table %s remains after down", table)
			}
		}
		for _, object := range []string{"integration_manifest_lineages", "integration_manifest_versions", "integration_manifest_versions_immutable_body", "integration_manifest_versions_immutable_body_trigger"} {
			var exists bool
			query := `SELECT EXISTS(SELECT 1 FROM pg_class WHERE relname=$1) OR EXISTS(SELECT 1 FROM pg_proc WHERE proname=$1) OR EXISTS(SELECT 1 FROM pg_trigger WHERE tgname=$1)`
			if err := db.QueryRow(query, object).Scan(&exists); err != nil || !exists {
				t.Errorf("migration-20 object %s exists=%v err=%v", object, exists, err)
			}
		}
		return
	}
	if version != 21 || dirty {
		t.Fatalf("migration version = %d dirty=%v, want clean 20 or 21", version, dirty)
	}
	// The workflow performs the actual down and reruns this test at version 20.
}

func migration21CreateProject(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`INSERT INTO projects(name,owner) VALUES($1,'migration21-test') RETURNING id`, name).Scan(&id); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM projects WHERE id=$1`, id) })
	return id
}

func migration21SeedRepository(t *testing.T, db *sql.DB, projectID, instanceID string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO project_repository_bindings
		(project_id,fabric_instance_id,provider,provider_repository_id,canonical_remote,default_ref,visibility)
		VALUES($1,$2,'github',$3,$4,'refs/heads/main','public')
		ON CONFLICT (project_id,fabric_instance_id) DO NOTHING`, projectID, instanceID,
		strings.ReplaceAll(instanceID, "-", ""), "https://github.com/wormhole/"+projectID)
	if err != nil {
		t.Fatalf("seed repository: %v", err)
	}
}

func migration21SeedStream(t *testing.T, db *sql.DB, projectID, instanceID, streamID, ref string) {
	t.Helper()
	migration21SeedRepository(t, db, projectID, instanceID)
	_, err := db.Exec(`INSERT INTO fabric_streams
		(project_id,fabric_instance_id,stream_id,canonical_ref,ref_name,current_version,live_tree_digest,accepted_tree_digest,accepted_commit_sha)
		VALUES($1,$2,$3,$4,$4,0,$5,$6,$7)`, projectID, instanceID, streamID, ref, testDigest("a"), testDigest("b"), strings.Repeat("a", 40))
	if err != nil {
		t.Fatalf("seed stream: %v", err)
	}
}

func migration21SeedWorkspace(t *testing.T, db *sql.DB, projectID, instanceID, streamID, workspaceID, ref string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO fabric_workspace_stream_bindings
		(project_id,fabric_instance_id,stream_id,workspace_id,attachment_ref,repository_provider,repository_immutable_id,canonical_ref,ref_name,writable)
		VALUES($1,$2,$3,$4,$5,'github',$6,$7,$7,true)`, projectID, instanceID, streamID, workspaceID,
		"55555555-5555-4555-8555-555555555555", strings.ReplaceAll(instanceID, "-", ""), ref)
	if err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
}

func testDigest(seed string) string {
	if seed == "" {
		seed = "0"
	}
	return "sha256:" + strings.Repeat(seed[:1], 64)
}

func requireSQLState(t *testing.T, err error, state string) {
	t.Helper()
	if err == nil {
		t.Fatalf("SQL unexpectedly succeeded; want SQLSTATE %s", state)
	}
	var pqError interface{ SQLState() string }
	if !errors.As(err, &pqError) || pqError.SQLState() != state {
		t.Fatalf("SQL error = %v, want SQLSTATE %s", err, state)
	}
}

func sortedStrings(values []string) []string {
	copyOfValues := append([]string(nil), values...)
	sort.Strings(copyOfValues)
	return copyOfValues
}

func migration21ObjectSummary(db *sql.DB) (string, error) {
	rows, err := db.Query(`SELECT c.relkind::text||':'||c.relname FROM pg_class c
		WHERE c.relnamespace='public'::regnamespace AND c.relkind IN ('r','i','S','v','m')
		UNION ALL SELECT 'f:'||proname FROM pg_proc WHERE pronamespace='public'::regnamespace
		UNION ALL SELECT 't:'||tgname FROM pg_trigger WHERE NOT tgisinternal
		ORDER BY 1`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return "", err
		}
		values = append(values, value)
	}
	return fmt.Sprint(sortedStrings(values)), rows.Err()
}
