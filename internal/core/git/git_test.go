package git

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"

	_ "github.com/lib/pq"

	"github.com/H4RL33/wormhole/internal/types"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	cfg := types.LoadConfig()
	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		if os.Getenv("WORMHOLE_INTEGRATION_REQUIRED") == "1" {
			t.Fatalf("postgres required but not reachable: %v", err)
		}
		t.Skipf("postgres not reachable (%v); run `docker compose up -d db` and apply migrations before running this test", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db)
}

func createProject(t *testing.T, s *Store, name string) string {
	t.Helper()
	var id string
	if err := s.db.QueryRow(`INSERT INTO projects (name, owner) VALUES ($1, $2) RETURNING id`, name, "harley").Scan(&id); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		if _, err := s.db.Exec(`DELETE FROM projects WHERE id = $1`, id); err != nil {
			t.Logf("cleanup: delete project %s: %v", id, err)
		}
	})
	return id
}

func createAgent(t *testing.T, s *Store) string {
	t.Helper()
	var id string
	if err := s.db.QueryRow(`INSERT INTO agents (owner, model, capabilities) VALUES ($1, $2, $3) RETURNING id`,
		"harley", "claude", `[]`).Scan(&id); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() {
		if _, err := s.db.Exec(`DELETE FROM agents WHERE id = $1`, id); err != nil {
			t.Logf("cleanup: delete agent %s: %v", id, err)
		}
	})
	return id
}

func createPassport(t *testing.T, s *Store, agentID, projectID string) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO passports (agent_id, project_id) VALUES ($1, $2)`, agentID, projectID); err != nil {
		t.Fatalf("create passport: %v", err)
	}
	t.Cleanup(func() {
		if _, err := s.db.Exec(`DELETE FROM passports WHERE agent_id = $1 AND project_id = $2`, agentID, projectID); err != nil {
			t.Logf("cleanup: delete passport for agent %s in project %s: %v", agentID, projectID, err)
		}
	})
}

func createTask(t *testing.T, s *Store, projectID, title string) string {
	t.Helper()
	var id string
	if err := s.db.QueryRow(`INSERT INTO tasks (project_id, title) VALUES ($1, $2) RETURNING id`, projectID, title).Scan(&id); err != nil {
		t.Fatalf("create task: %v", err)
	}
	t.Cleanup(func() {
		if _, err := s.db.Exec(`DELETE FROM tasks WHERE id = $1`, id); err != nil {
			t.Logf("cleanup: delete task %s: %v", id, err)
		}
	})
	return id
}

func TestLinkCommit_Success(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "git-link-commit-success")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)
	taskID := createTask(t, s, projectID, "fix the bug")

	link, err := s.LinkCommit(ctx, projectID, agentID, &taskID, "github.com/example/repo", "abc123", "fixed the bug")
	if err != nil {
		t.Fatalf("LinkCommit: %v", err)
	}

	if link.ID == "" {
		t.Error("link.ID is empty")
	}
	if link.ProjectID != projectID {
		t.Errorf("link.ProjectID = %q, want %q", link.ProjectID, projectID)
	}
	if link.TaskID == nil || *link.TaskID != taskID {
		t.Errorf("link.TaskID = %v, want %q", link.TaskID, taskID)
	}
	if link.Repo != "github.com/example/repo" {
		t.Errorf("link.Repo = %q, want %q", link.Repo, "github.com/example/repo")
	}
	if link.CommitSHA == nil || *link.CommitSHA != "abc123" {
		t.Errorf("link.CommitSHA = %v, want %q", link.CommitSHA, "abc123")
	}
	if link.PRUrl != nil {
		t.Errorf("link.PRUrl = %v, want nil", link.PRUrl)
	}
	if link.Summary != "fixed the bug" {
		t.Errorf("link.Summary = %q, want %q", link.Summary, "fixed the bug")
	}
	if link.AgentID != agentID {
		t.Errorf("link.AgentID = %q, want %q", link.AgentID, agentID)
	}
	if link.CreatedAt.IsZero() {
		t.Error("link.CreatedAt is zero")
	}
}

func TestLinkCommit_UnknownTaskRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "git-link-commit-unknown-task")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	unknownTaskID := "00000000-0000-0000-0000-000000000000"
	_, err := s.LinkCommit(ctx, projectID, agentID, &unknownTaskID, "github.com/example/repo", "abc123", "summary")
	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("expected ErrTaskNotFound, got: %v", err)
	}
}

func TestLinkCommit_PassportRequired(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "git-link-commit-passport-required")
	agentID := createAgent(t, s)
	taskID := createTask(t, s, projectID, "task without passport")

	_, err := s.LinkCommit(ctx, projectID, agentID, &taskID, "github.com/example/repo", "abc123", "summary")
	if !errors.Is(err, ErrPassportNotFound) {
		t.Fatalf("expected ErrPassportNotFound, got: %v", err)
	}
}

func TestRequestReview_Success(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "git-request-review-success")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	link, err := s.RequestReview(ctx, projectID, agentID, "github.com/example/repo", "https://github.com/example/repo/pull/1", "please review")
	if err != nil {
		t.Fatalf("RequestReview: %v", err)
	}

	if link.ID == "" {
		t.Error("link.ID is empty")
	}
	if link.TaskID != nil {
		t.Errorf("link.TaskID = %v, want nil", link.TaskID)
	}
	if link.CommitSHA != nil {
		t.Errorf("link.CommitSHA = %v, want nil", link.CommitSHA)
	}
	if link.PRUrl == nil || *link.PRUrl != "https://github.com/example/repo/pull/1" {
		t.Errorf("link.PRUrl = %v, want %q", link.PRUrl, "https://github.com/example/repo/pull/1")
	}
	if link.Summary != "please review" {
		t.Errorf("link.Summary = %q, want %q", link.Summary, "please review")
	}
}

func TestRequestReview_PassportRequired(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "git-request-review-passport-required")
	agentID := createAgent(t, s)

	_, err := s.RequestReview(ctx, projectID, agentID, "github.com/example/repo", "https://github.com/example/repo/pull/2", "please review")
	if !errors.Is(err, ErrPassportNotFound) {
		t.Fatalf("expected ErrPassportNotFound, got: %v", err)
	}
}

// TestGitLinks_CrossProjectIsolation mirrors events_test.go's TestRLSIsolation:
// a plain project_id-scoped connection using the table owner role bypasses
// RLS entirely (Postgres does not enforce RLS against the table owner), so
// this test creates a restricted, non-owner role to prove the policy itself
// (not just a WHERE clause) hides project A's row when project B's context is
// set.
func TestGitLinks_CrossProjectIsolation(t *testing.T) {
	ownerStore := testStore(t)
	ctx := context.Background()

	roleName := "git_rls_test_user"
	rolePassword := "git_rls_test_password"

	t.Cleanup(func() {
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE git_links FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE projects FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE agents FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE passports FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("DROP ROLE IF EXISTS %s", roleName))
	})

	if _, err := ownerStore.db.Exec(fmt.Sprintf("DROP ROLE IF EXISTS %s", roleName)); err != nil {
		t.Fatalf("failed to drop pre-existing role: %v", err)
	}
	if _, err := ownerStore.db.Exec(fmt.Sprintf("CREATE ROLE %s WITH LOGIN PASSWORD '%s'", roleName, rolePassword)); err != nil {
		t.Fatalf("failed to create role: %v", err)
	}
	if _, err := ownerStore.db.Exec(fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE git_links, projects, agents, passports TO %s", roleName)); err != nil {
		t.Fatalf("failed to grant table privileges: %v", err)
	}

	cfg := types.LoadConfig()
	u, err := url.Parse(cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("failed to parse database URL: %v", err)
	}
	u.User = url.UserPassword(roleName, rolePassword)
	restrictedDSN := u.String()

	restrictedDB, err := sql.Open("postgres", restrictedDSN)
	if err != nil {
		t.Fatalf("failed to open restricted db connection: %v", err)
	}
	t.Cleanup(func() { restrictedDB.Close() })

	if err := restrictedDB.PingContext(ctx); err != nil {
		t.Fatalf("failed to ping restricted database: %v", err)
	}

	projectA := createProject(t, ownerStore, "git-isolation-project-a")
	projectB := createProject(t, ownerStore, "git-isolation-project-b")
	agentID := createAgent(t, ownerStore)
	createPassport(t, ownerStore, agentID, projectA)

	// Create the link via the (RLS-bypassing) owner store so the restricted
	// connection below has done nothing but Ping before its first query;
	// this avoids the restricted session's wormhole.project_id placeholder
	// GUC being left at '' (rather than unset) by an earlier local SET on the
	// same pooled connection, which would make the "no context set" check
	// below fail with a cast error instead of exercising RLS.
	link, err := ownerStore.RequestReview(ctx, projectA, agentID, "github.com/example/repo", "https://github.com/example/repo/pull/3", "project a review")
	if err != nil {
		t.Fatalf("RequestReview (project A): %v", err)
	}

	// 1. No project context set: RLS must hide the row entirely.
	var found string
	err = restrictedDB.QueryRowContext(ctx, "SELECT id FROM git_links WHERE id = $1", link.ID).Scan(&found)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected git_links row to be hidden with no project context set, got err=%v found=%q", err, found)
	}

	// 2. Project B's context set: project A's row must still be invisible.
	tx, err := restrictedDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectB); err != nil {
		t.Fatalf("set project id: %v", err)
	}
	err = tx.QueryRowContext(ctx, "SELECT id FROM git_links WHERE id = $1", link.ID).Scan(&found)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected git_links row from project A to be hidden under project B's RLS context, got err=%v found=%q", err, found)
	}

	// 3. Project A's own context set: the row must be visible (sanity check
	// that RLS scopes rather than blanket-denies).
	tx2, err := restrictedDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx2.Rollback()
	if _, err := tx2.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectA); err != nil {
		t.Fatalf("set project id: %v", err)
	}
	if err := tx2.QueryRowContext(ctx, "SELECT id FROM git_links WHERE id = $1", link.ID).Scan(&found); err != nil {
		t.Fatalf("expected git_links row to be visible under its own project context, got err=%v", err)
	}
}
