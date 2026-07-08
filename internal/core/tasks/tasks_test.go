package tasks

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	_ "github.com/lib/pq"

	"github.com/H4RL33/wormhole/internal/types"
)

// testStore opens a real connection to the configured Postgres instance and
// skips the test if it isn't reachable — these are integration tests against
// real schema behavior, not mocks (mirrors internal/core/identity's pattern).
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
		t.Skipf("postgres not reachable (%v) — run `docker compose up -d db` and apply migrations before running this test", err)
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

func TestCreate_ReturnsPopulatedTask(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "create-task")

	task, err := s.Create(ctx, projectID, "Write docs", "long description", nil, 3, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if task.ID == "" {
		t.Error("Task.ID is empty")
	}
	if task.Status != "todo" {
		t.Errorf("Task.Status = %q, want %q", task.Status, "todo")
	}
	if task.Title != "Write docs" {
		t.Errorf("Task.Title = %q, want %q", task.Title, "Write docs")
	}
	if task.Description != "long description" {
		t.Errorf("Task.Description = %q, want %q", task.Description, "long description")
	}
	if task.Priority != 3 {
		t.Errorf("Task.Priority = %d, want %d", task.Priority, 3)
	}
	if task.ParentTaskID != nil {
		t.Errorf("Task.ParentTaskID = %v, want nil", task.ParentTaskID)
	}
	if task.OwnerAgentID != nil {
		t.Errorf("Task.OwnerAgentID = %v, want nil", task.OwnerAgentID)
	}
	if task.DueBy != nil {
		t.Errorf("Task.DueBy = %v, want nil", task.DueBy)
	}
}

func TestCreate_WithParentTask(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "create-parent-child")

	parent, err := s.Create(ctx, projectID, "Parent", "", nil, 0, nil)
	if err != nil {
		t.Fatalf("Create(parent): %v", err)
	}

	child, err := s.Create(ctx, projectID, "Child", "", &parent.ID, 0, nil)
	if err != nil {
		t.Fatalf("Create(child): %v", err)
	}

	if child.ParentTaskID == nil {
		t.Fatal("Child.ParentTaskID is nil, want parent id")
	}
	if *child.ParentTaskID != parent.ID {
		t.Errorf("Child.ParentTaskID = %q, want %q", *child.ParentTaskID, parent.ID)
	}
}

func TestAssign_SetsOwner(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "assign-task")
	agentID := createAgent(t, s)

	task, err := s.Create(ctx, projectID, "Assign me", "", nil, 0, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.Assign(ctx, projectID, task.ID, agentID)
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}
	if got.OwnerAgentID == nil {
		t.Fatal("Assign: OwnerAgentID is nil")
	}
	if *got.OwnerAgentID != agentID {
		t.Errorf("Assign: OwnerAgentID = %q, want %q", *got.OwnerAgentID, agentID)
	}
}

func TestList_FiltersByProjectAndStatus(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	project1 := createProject(t, s, "list-project-1")
	project2 := createProject(t, s, "list-project-2")

	todoTask, err := s.Create(ctx, project1, "Todo task", "", nil, 0, nil)
	if err != nil {
		t.Fatalf("Create(todo): %v", err)
	}
	wipTask, err := s.Create(ctx, project1, "Wip task", "", nil, 0, nil)
	if err != nil {
		t.Fatalf("Create(wip): %v", err)
	}
	if _, err := s.UpdateStatus(ctx, project1, wipTask.ID, "wip"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if _, err := s.Create(ctx, project2, "Other project task", "", nil, 0, nil); err != nil {
		t.Fatalf("Create(project2): %v", err)
	}

	all, err := s.List(ctx, project1, nil)
	if err != nil {
		t.Fatalf("List(nil): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("List(nil) returned %d tasks, want 2: %+v", len(all), all)
	}
	gotIDs := map[string]bool{all[0].ID: true, all[1].ID: true}
	if !gotIDs[todoTask.ID] || !gotIDs[wipTask.ID] {
		t.Errorf("List(nil) = %+v, want todo and wip tasks", all)
	}

	wipStatus := "wip"
	filtered, err := s.List(ctx, project1, &wipStatus)
	if err != nil {
		t.Fatalf("List(wip): %v", err)
	}
	if len(filtered) != 1 {
		t.Fatalf("List(wip) returned %d tasks, want 1: %+v", len(filtered), filtered)
	}
	if filtered[0].ID != wipTask.ID {
		t.Errorf("List(wip)[0].ID = %q, want %q", filtered[0].ID, wipTask.ID)
	}
}

func TestUpdateStatus_ValidTransitions(t *testing.T) {
	tests := []struct {
		from string
		to   string
	}{
		{"todo", "wip"},
		{"wip", "blocked"},
		{"wip", "done"},
		{"blocked", "wip"},
	}

	for _, tt := range tests {
		t.Run(tt.from+"->"+tt.to, func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			projectID := createProject(t, s, "transition-"+tt.from+"-"+tt.to)

			var taskID string
			if err := s.db.QueryRowContext(ctx,
				`INSERT INTO tasks (project_id, title, status) VALUES ($1, $2, $3) RETURNING id`,
				projectID, "transition task", tt.from,
			).Scan(&taskID); err != nil {
				t.Fatalf("insert task at status %q: %v", tt.from, err)
			}

			got, err := s.UpdateStatus(ctx, projectID, taskID, tt.to)
			if err != nil {
				t.Fatalf("UpdateStatus(%s->%s): %v", tt.from, tt.to, err)
			}
			if got.Status != tt.to {
				t.Errorf("UpdateStatus(%s->%s).Status = %q, want %q", tt.from, tt.to, got.Status, tt.to)
			}
		})
	}
}

func TestUpdateStatus_InvalidTransitionsRejected(t *testing.T) {
	tests := []struct {
		from string
		to   string
	}{
		{"todo", "done"},
		{"todo", "blocked"},
		{"blocked", "done"},
		{"done", "wip"},
	}

	for _, tt := range tests {
		t.Run(tt.from+"->"+tt.to, func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			projectID := createProject(t, s, "invalid-transition-"+tt.from+"-"+tt.to)

			var taskID string
			if err := s.db.QueryRowContext(ctx,
				`INSERT INTO tasks (project_id, title, status) VALUES ($1, $2, $3) RETURNING id`,
				projectID, "invalid transition task", tt.from,
			).Scan(&taskID); err != nil {
				t.Fatalf("insert task at status %q: %v", tt.from, err)
			}

			_, err := s.UpdateStatus(ctx, projectID, taskID, tt.to)
			if !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("UpdateStatus(%s->%s) error = %v, want ErrInvalidTransition", tt.from, tt.to, err)
			}

			var status string
			if err := s.db.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id = $1`, taskID).Scan(&status); err != nil {
				t.Fatalf("re-fetch status: %v", err)
			}
			if status != tt.from {
				t.Errorf("status after rejected transition = %q, want unchanged %q", status, tt.from)
			}
		})
	}
}

func TestUpdateStatus_UnknownTaskReturnsNotFound(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "unknown-task")

	_, err := s.UpdateStatus(ctx, projectID, "00000000-0000-0000-0000-000000000000", "wip")
	if !errors.Is(err, ErrTaskNotFound) {
		t.Errorf("UpdateStatus(unknown task) error = %v, want ErrTaskNotFound", err)
	}
}
