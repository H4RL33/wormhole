package mcp

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"testing"

	_ "github.com/lib/pq"

	"github.com/H4RL33/wormhole/internal/types"
)

const rlsFixtureLockKey = 867530913

type rlsMatrixFixture struct {
	projectA  string
	projectB  string
	agentID   string
	agentID2  string
	passportA string
	taskA     string
	channelA  string
	articleA  string
	articleA2 string
	viewerKeyA string
}

type rlsTableCase struct {
	name      string
	rowID     string
	updateSQL string
	insertSQL string
	insertArg []any
}

func TestRestrictedRoleRLSOperationMatrix(t *testing.T) {
	owner := testDB(t)
	restricted := newRestrictedRLSDB(t, owner)
	fx := seedRLSMatrix(t, owner)

	cases := []rlsTableCase{
		{name: "projects", rowID: fx.projectA, updateSQL: `UPDATE projects SET name = name WHERE id = $1`, insertSQL: `INSERT INTO projects (id, name, owner) VALUES (gen_random_uuid(), 'matrix-project', 'matrix')`},
		{name: "passports", rowID: mustRowID(t, owner, `SELECT id FROM passports WHERE project_id = $1`, fx.projectA), updateSQL: `UPDATE passports SET repositories = repositories WHERE id = $1`, insertSQL: `INSERT INTO passports (agent_id, project_id) VALUES ($1, $2)`, insertArg: []any{fx.agentID2, fx.projectA}},
		{name: "permissions", rowID: mustRowID(t, owner, `SELECT id FROM permissions WHERE project_id = $1`, fx.projectA), updateSQL: `UPDATE permissions SET granted = granted WHERE id = $1`, insertSQL: `INSERT INTO permissions (passport_id, project_id, action, granted) VALUES ($1, $2, 'matrix.read', true)`, insertArg: []any{fx.passportA, fx.projectA}},
		{name: "agent_tokens", rowID: mustRowID(t, owner, `SELECT id FROM agent_tokens WHERE project_id = $1`, fx.projectA), updateSQL: `UPDATE agent_tokens SET permissions = permissions WHERE id = $1`, insertSQL: `INSERT INTO agent_tokens (agent_id, project_id, permissions, token_hash, expires_at) VALUES ($1, $2, '[]', gen_random_uuid()::text, now() + interval '1 hour')`, insertArg: []any{fx.agentID, fx.projectA}},
		{name: "audit_log", rowID: mustRowID(t, owner, `SELECT id FROM audit_log WHERE project_id = $1`, fx.projectA), updateSQL: `UPDATE audit_log SET action = action WHERE id = $1`, insertSQL: `INSERT INTO audit_log (agent_id, project_id, action) VALUES ($1, $2, 'matrix.test')`, insertArg: []any{fx.agentID, fx.projectA}},
		{name: "tasks", rowID: fx.taskA, updateSQL: `UPDATE tasks SET title = title WHERE id = $1`, insertSQL: `INSERT INTO tasks (project_id, title) VALUES ($1, 'matrix-task')`, insertArg: []any{fx.projectA}},
		{name: "task_links", rowID: mustRowID(t, owner, `SELECT id FROM task_links WHERE project_id = $1`, fx.projectA), updateSQL: `UPDATE task_links SET target_ref = target_ref WHERE id = $1`, insertSQL: `INSERT INTO task_links (project_id, task_id, link_type, target_ref) VALUES ($1, $2, 'commit', 'matrix')`, insertArg: []any{fx.projectA, fx.taskA}},
		{name: "channels", rowID: fx.channelA, updateSQL: `UPDATE channels SET name = name WHERE id = $1`, insertSQL: `INSERT INTO channels (project_id, name) VALUES ($1, gen_random_uuid()::text)`, insertArg: []any{fx.projectA}},
		{name: "events", rowID: mustRowID(t, owner, `SELECT id FROM events WHERE project_id = $1`, fx.projectA), updateSQL: `UPDATE events SET note = note WHERE id = $1`, insertSQL: `INSERT INTO events (project_id, channel_id, agent_id, event_type) VALUES ($1, $2, $3, 'message.posted')`, insertArg: []any{fx.projectA, fx.channelA, fx.agentID}},
		{name: "git_links", rowID: mustRowID(t, owner, `SELECT id FROM git_links WHERE project_id = $1`, fx.projectA), updateSQL: `UPDATE git_links SET summary = summary WHERE id = $1`, insertSQL: `INSERT INTO git_links (project_id, task_id, repo, commit_sha, summary, agent_id) VALUES ($1, $2, 'matrix/repo', gen_random_uuid()::text, 'matrix', $3)`, insertArg: []any{fx.projectA, fx.taskA, fx.agentID}},
		{name: "kb_articles", rowID: fx.articleA, updateSQL: `UPDATE kb_articles SET title = title WHERE id = $1`, insertSQL: `INSERT INTO kb_articles (project_id, title, body, author_agent_id) VALUES ($1, 'matrix', 'matrix', $2)`, insertArg: []any{fx.projectA, fx.agentID}},
		{name: "kb_links", rowID: mustRowID(t, owner, `SELECT id FROM kb_links WHERE project_id = $1`, fx.projectA), updateSQL: `UPDATE kb_links SET created_at = created_at WHERE id = $1`, insertSQL: `INSERT INTO kb_links (project_id, from_article_id, to_article_id) VALUES ($1, $2, $3)`, insertArg: []any{fx.projectA, fx.articleA, fx.articleA2}},
		{name: "viewer_keys", rowID: fx.viewerKeyA, updateSQL: `UPDATE viewer_keys SET label = label WHERE id = $1`, insertSQL: `INSERT INTO viewer_keys (project_id, label, key_hash) VALUES ($1, 'matrix', gen_random_uuid()::text)`, insertArg: []any{fx.projectA}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRLSRows(t, restricted, "", fmt.Sprintf(`SELECT count(*) FROM %s WHERE id = $1`, tc.name), tc.rowID, 0)
			assertRLSRows(t, restricted, fx.projectA, fmt.Sprintf(`SELECT count(*) FROM %s WHERE id = $1`, tc.name), tc.rowID, 1)
			assertRLSRows(t, restricted, fx.projectB, fmt.Sprintf(`SELECT count(*) FROM %s WHERE id = $1`, tc.name), tc.rowID, 0)

			assertRLSMutation(t, restricted, "", tc.updateSQL, []any{tc.rowID}, false)
			assertRLSMutation(t, restricted, fx.projectA, tc.updateSQL, []any{tc.rowID}, true)
			assertRLSMutation(t, restricted, fx.projectB, tc.updateSQL, []any{tc.rowID}, false)

			deleteSQL := fmt.Sprintf(`DELETE FROM %s WHERE id = $1`, tc.name)
			assertRLSMutation(t, restricted, "", deleteSQL, []any{tc.rowID}, false)
			assertRLSMutation(t, restricted, fx.projectA, deleteSQL, []any{tc.rowID}, true)
			assertRLSMutation(t, restricted, fx.projectB, deleteSQL, []any{tc.rowID}, false)

			assertRLSInsert(t, restricted, "", tc.insertSQL, tc.insertArg, false)
			if tc.name != "projects" {
				assertRLSInsert(t, restricted, fx.projectA, tc.insertSQL, tc.insertArg, true)
			} else {
				assertProjectReplacementInsert(t, restricted, fx.projectA)
			}
			assertRLSInsert(t, restricted, fx.projectB, tc.insertSQL, tc.insertArg, false)
		})
	}
}

func TestRestrictedRoleRejectsCrossProjectForeignReferences(t *testing.T) {
	owner := testDB(t)
	restricted := newRestrictedRLSDB(t, owner)
	fx := seedRLSMatrix(t, owner)

	cases := []struct {
		name string
		sql  string
		args []any
	}{
		{name: "permission passport", sql: `INSERT INTO permissions (passport_id, project_id, action, granted) VALUES ($1, $2, 'cross', true)`, args: []any{fx.passportA, fx.projectB}},
		{name: "task parent", sql: `INSERT INTO tasks (project_id, parent_task_id, title) VALUES ($1, $2, 'cross')`, args: []any{fx.projectB, fx.taskA}},
		{name: "task link", sql: `INSERT INTO task_links (project_id, task_id, link_type, target_ref) VALUES ($1, $2, 'commit', 'cross')`, args: []any{fx.projectB, fx.taskA}},
		{name: "event channel", sql: `INSERT INTO events (project_id, channel_id, agent_id, event_type) VALUES ($1, $2, $3, 'message.posted')`, args: []any{fx.projectB, fx.channelA, fx.agentID}},
		{name: "git task", sql: `INSERT INTO git_links (project_id, task_id, repo, commit_sha, summary, agent_id) VALUES ($1, $2, 'matrix/repo', 'cross', 'cross', $3)`, args: []any{fx.projectB, fx.taskA, fx.agentID}},
		{name: "kb source", sql: `INSERT INTO kb_links (project_id, from_article_id, to_article_id) VALUES ($1, $2, $3)`, args: []any{fx.projectB, fx.articleA, fx.articleA2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx := beginRestrictedTx(t, restricted, fx.projectB)
			defer tx.Rollback()
			if _, err := tx.ExecContext(context.Background(), tc.sql, tc.args...); err == nil {
				t.Fatal("cross-project foreign reference succeeded")
			}
		})
	}
}

func newRestrictedRLSDB(t *testing.T, owner *sql.DB) *sql.DB {
	t.Helper()
	lockConn, err := owner.Conn(context.Background())
	if err != nil {
		t.Fatalf("open RLS fixture lock: %v", err)
	}
	if _, err := lockConn.ExecContext(context.Background(), `SELECT pg_advisory_lock($1)`, rlsFixtureLockKey); err != nil {
		lockConn.Close()
		t.Fatalf("lock RLS fixture: %v", err)
	}
	t.Cleanup(func() {
		_, _ = owner.Exec(`REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM wormhole_rls_matrix`)
		_, _ = owner.Exec(`REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM wormhole_rls_matrix`)
		_, _ = owner.Exec(`DROP ROLE IF EXISTS wormhole_rls_matrix`)
		_, _ = lockConn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, rlsFixtureLockKey)
		lockConn.Close()
	})

	_, _ = owner.Exec(`REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM wormhole_rls_matrix`)
	_, _ = owner.Exec(`REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM wormhole_rls_matrix`)
	if _, err := owner.Exec(`DROP ROLE IF EXISTS wormhole_rls_matrix`); err != nil {
		t.Fatalf("drop old RLS role: %v", err)
	}
	if _, err := owner.Exec(`CREATE ROLE wormhole_rls_matrix LOGIN PASSWORD 'wormhole_rls_matrix'`); err != nil {
		t.Fatalf("create RLS role: %v", err)
	}
	if _, err := owner.Exec(`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO wormhole_rls_matrix`); err != nil {
		t.Fatalf("grant RLS table privileges: %v", err)
	}
	if _, err := owner.Exec(`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO wormhole_rls_matrix`); err != nil {
		t.Fatalf("grant RLS sequence privileges: %v", err)
	}

	cfg := types.LoadConfig()
	u, err := url.Parse(cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	u.User = url.UserPassword("wormhole_rls_matrix", "wormhole_rls_matrix")
	db, err := sql.Open("postgres", u.String())
	if err != nil {
		t.Fatalf("open restricted database: %v", err)
	}
	db.SetMaxIdleConns(0)
	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		t.Fatalf("ping restricted database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func seedRLSMatrix(t *testing.T, db *sql.DB) rlsMatrixFixture {
	t.Helper()
	fx := rlsMatrixFixture{}
	mustScan(t, db, `INSERT INTO projects (name, owner) VALUES ('rls-matrix-a', 'matrix') RETURNING id`, &fx.projectA)
	mustScan(t, db, `INSERT INTO projects (name, owner) VALUES ('rls-matrix-b', 'matrix') RETURNING id`, &fx.projectB)
	mustScan(t, db, `INSERT INTO agents (owner, model) VALUES ('matrix', 'test') RETURNING id`, &fx.agentID)
	mustScan(t, db, `INSERT INTO agents (owner, model) VALUES ('matrix-2', 'test') RETURNING id`, &fx.agentID2)
	mustScan(t, db, `INSERT INTO passports (agent_id, project_id) VALUES ($1, $2) RETURNING id`, &fx.passportA, fx.agentID, fx.projectA)
	mustExec(t, db, `INSERT INTO permissions (passport_id, project_id, action, granted) VALUES ($1, $2, 'matrix.read', true)`, fx.passportA, fx.projectA)
	mustExec(t, db, `INSERT INTO agent_tokens (agent_id, project_id, permissions, token_hash, expires_at) VALUES ($1, $2, '[]', gen_random_uuid()::text, now() + interval '1 hour')`, fx.agentID, fx.projectA)
	mustExec(t, db, `INSERT INTO audit_log (agent_id, project_id, action) VALUES ($1, $2, 'matrix.seed')`, fx.agentID, fx.projectA)
	mustScan(t, db, `INSERT INTO tasks (project_id, title) VALUES ($1, 'matrix-task-a') RETURNING id`, &fx.taskA, fx.projectA)
	mustExec(t, db, `INSERT INTO task_links (project_id, task_id, link_type, target_ref) VALUES ($1, $2, 'commit', 'seed')`, fx.projectA, fx.taskA)
	mustScan(t, db, `INSERT INTO channels (project_id, name) VALUES ($1, 'matrix-channel') RETURNING id`, &fx.channelA, fx.projectA)
	mustExec(t, db, `INSERT INTO events (project_id, channel_id, agent_id, event_type) VALUES ($1, $2, $3, 'message.posted')`, fx.projectA, fx.channelA, fx.agentID)
	mustExec(t, db, `INSERT INTO git_links (project_id, task_id, repo, commit_sha, summary, agent_id) VALUES ($1, $2, 'matrix/repo', 'seed', 'seed', $3)`, fx.projectA, fx.taskA, fx.agentID)
	mustScan(t, db, `INSERT INTO kb_articles (project_id, title, body, author_agent_id) VALUES ($1, 'matrix-a', 'seed', $2) RETURNING id`, &fx.articleA, fx.projectA, fx.agentID)
	mustScan(t, db, `INSERT INTO kb_articles (project_id, title, body, author_agent_id) VALUES ($1, 'matrix-a2', 'seed', $2) RETURNING id`, &fx.articleA2, fx.projectA, fx.agentID)
	mustExec(t, db, `INSERT INTO kb_links (project_id, from_article_id, to_article_id) VALUES ($1, $2, $3)`, fx.projectA, fx.articleA, fx.articleA2)
	mustScan(t, db, `INSERT INTO viewer_keys (project_id, label, key_hash) VALUES ($1, 'matrix-key', gen_random_uuid()::text) RETURNING id`, &fx.viewerKeyA, fx.projectA)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM agents WHERE id IN ($1, $2)`, fx.agentID, fx.agentID2)
		_, _ = db.Exec(`DELETE FROM projects WHERE id IN ($1, $2)`, fx.projectA, fx.projectB)
	})
	return fx
}

func beginRestrictedTx(t *testing.T, db *sql.DB, projectID string) *sql.Tx {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin restricted transaction: %v", err)
	}
	if projectID != "" {
		if _, err := tx.ExecContext(context.Background(), `SELECT set_config('wormhole.project_id', $1, true)`, projectID); err != nil {
			tx.Rollback()
			t.Fatalf("set restricted project context: %v", err)
		}
	}
	return tx
}

func assertRLSRows(t *testing.T, db *sql.DB, projectID, query, rowID string, want int) {
	t.Helper()
	tx := beginRestrictedTx(t, db, projectID)
	defer tx.Rollback()
	var got int
	if err := tx.QueryRowContext(context.Background(), query, rowID).Scan(&got); err != nil {
		t.Fatalf("RLS select: %v", err)
	}
	if got != want {
		t.Fatalf("RLS select rows = %d, want %d (context %q)", got, want, projectID)
	}
}

func assertRLSMutation(t *testing.T, db *sql.DB, projectID, query string, args []any, wantRow bool) {
	t.Helper()
	tx := beginRestrictedTx(t, db, projectID)
	defer tx.Rollback()
	result, err := tx.ExecContext(context.Background(), query, args...)
	if err != nil {
		t.Fatalf("RLS mutation: %v", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		t.Fatalf("RLS mutation rows: %v", err)
	}
	if (rows == 1) != wantRow {
		t.Fatalf("RLS mutation rows = %d, want row=%v (context %q)", rows, wantRow, projectID)
	}
}

func assertRLSInsert(t *testing.T, db *sql.DB, projectID, query string, args []any, wantSuccess bool) {
	t.Helper()
	tx := beginRestrictedTx(t, db, projectID)
	defer tx.Rollback()
	_, err := tx.ExecContext(context.Background(), query, args...)
	if wantSuccess && err != nil {
		t.Fatalf("RLS insert: %v", err)
	}
	if !wantSuccess && err == nil {
		t.Fatalf("RLS insert unexpectedly succeeded (context %q)", projectID)
	}
}

func assertProjectReplacementInsert(t *testing.T, db *sql.DB, projectID string) {
	t.Helper()
	tx := beginRestrictedTx(t, db, projectID)
	defer tx.Rollback()
	if _, err := tx.ExecContext(context.Background(), `DELETE FROM projects WHERE id = $1`, projectID); err != nil {
		t.Fatalf("delete project before matching-id insert: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO projects (id, name, owner) VALUES ($1, 'matrix-project-reinsert', 'matrix')`, projectID); err != nil {
		t.Fatalf("RLS project insert with id matching tenant GUC: %v", err)
	}
}

func mustRowID(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var id string
	if err := db.QueryRow(query, args...).Scan(&id); err != nil {
		t.Fatalf("query seeded row: %v", err)
	}
	return id
}

func mustScan(t *testing.T, db *sql.DB, query string, dest any, args ...any) {
	t.Helper()
	if err := db.QueryRow(query, args...).Scan(dest); err != nil {
		t.Fatalf("seed row: %v", err)
	}
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("seed row: %v", err)
	}
}
