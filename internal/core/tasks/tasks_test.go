package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"

	_ "github.com/lib/pq"

	"github.com/H4RL33/wormhole/internal/core/events"
	"github.com/H4RL33/wormhole/internal/types"
)

// testStore opens a real connection to the configured Postgres instance and
// skips the test if it isn't reachable (these are integration tests against
// real schema behavior, not mocks; mirrors internal/core/identity's pattern).
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
	return NewStore(db, events.NewStore(db))
}

// createChannel creates an events channel in projectID and returns its ID,
// for use as the required channelID argument to UpdateStatus.
func createChannel(t *testing.T, s *Store, projectID, name string) string {
	t.Helper()
	channel, err := s.events.CreateChannel(context.Background(), projectID, name)
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	return channel.ID
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
	createPassport(t, s, agentID, projectID)

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
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, project1)
	channelID := createChannel(t, s, project1, "list-filter-events")
	if _, err := s.UpdateStatus(ctx, project1, wipTask.ID, "wip", channelID, agentID); err != nil {
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

			agentID := createAgent(t, s)
			createPassport(t, s, agentID, projectID)
			channelID := createChannel(t, s, projectID, "transition-events")

			got, err := s.UpdateStatus(ctx, projectID, taskID, tt.to, channelID, agentID)
			if err != nil {
				t.Fatalf("UpdateStatus(%s->%s): %v", tt.from, tt.to, err)
			}
			if got.Status != tt.to {
				t.Errorf("UpdateStatus(%s->%s).Status = %q, want %q", tt.from, tt.to, got.Status, tt.to)
			}

			var eventCount int
			var payload []byte
			if err := s.db.QueryRowContext(ctx,
				`SELECT count(*) FROM events WHERE channel_id = $1 AND event_type = 'task.status_changed'`,
				channelID,
			).Scan(&eventCount); err != nil {
				t.Fatalf("count events: %v", err)
			}
			if eventCount != 1 {
				t.Fatalf("event count after legal transition = %d, want 1", eventCount)
			}
			var gotAgentID string
			if err := s.db.QueryRowContext(ctx,
				`SELECT agent_id, payload FROM events WHERE channel_id = $1 AND event_type = 'task.status_changed'`,
				channelID,
			).Scan(&gotAgentID, &payload); err != nil {
				t.Fatalf("fetch event: %v", err)
			}
			if gotAgentID != agentID {
				t.Errorf("event agent_id = %q, want %q", gotAgentID, agentID)
			}
			var gotPayload types.TaskStatusChangedPayload
			if err := json.Unmarshal(payload, &gotPayload); err != nil {
				t.Fatalf("unmarshal event payload: %v", err)
			}
			if gotPayload.TaskID != taskID || gotPayload.FromStatus != tt.from || gotPayload.ToStatus != tt.to {
				t.Errorf("event payload = %+v, want {TaskID: %q, FromStatus: %q, ToStatus: %q}", gotPayload, taskID, tt.from, tt.to)
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

			agentID := createAgent(t, s)
			createPassport(t, s, agentID, projectID)
			channelID := createChannel(t, s, projectID, "invalid-transition-events")

			_, err := s.UpdateStatus(ctx, projectID, taskID, tt.to, channelID, agentID)
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

			// The tx rolled back on rejection, so no event row should exist
			// for this channel either: no orphan event without its status
			// transition (RFC-0001 §8.2, architecture.md §9.1).
			var eventCount int
			if err := s.db.QueryRowContext(ctx,
				`SELECT count(*) FROM events WHERE channel_id = $1`,
				channelID,
			).Scan(&eventCount); err != nil {
				t.Fatalf("count events: %v", err)
			}
			if eventCount != 0 {
				t.Errorf("event count after rejected transition = %d, want 0 (no orphan event on rollback)", eventCount)
			}
		})
	}
}

func TestUpdateStatus_UnknownTaskReturnsNotFound(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "unknown-task")

	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)
	channelID := createChannel(t, s, projectID, "unknown-task-events")

	_, err := s.UpdateStatus(ctx, projectID, "00000000-0000-0000-0000-000000000000", "wip", channelID, agentID)
	if !errors.Is(err, ErrTaskNotFound) {
		t.Errorf("UpdateStatus(unknown task) error = %v, want ErrTaskNotFound", err)
	}
}

func TestRLSIsolation(t *testing.T) {
	ownerStore := testStore(t)

	roleName := "rls_test_user"
	rolePassword := "rls_test_password"

	// Ensure clean slate and cleanup afterwards
	t.Cleanup(func() {
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE tasks FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE projects FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("DROP ROLE IF EXISTS %s", roleName))
	})

	_, err := ownerStore.db.Exec(fmt.Sprintf("DROP ROLE IF EXISTS %s", roleName))
	if err != nil {
		t.Fatalf("failed to drop pre-existing role: %v", err)
	}

	_, err = ownerStore.db.Exec(fmt.Sprintf("CREATE ROLE %s WITH LOGIN PASSWORD '%s'", roleName, rolePassword))
	if err != nil {
		t.Fatalf("failed to create role: %v", err)
	}

	_, err = ownerStore.db.Exec(fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE tasks, projects TO %s", roleName))
	if err != nil {
		t.Fatalf("failed to grant table privileges: %v", err)
	}

	cfg := types.LoadConfig()
	u, err := url.Parse(cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("failed to parse database URL: %v", err)
	}
	u.User = url.UserPassword(roleName, rolePassword)
	restrictedDSN := u.String()

	restrictedDb, err := sql.Open("postgres", restrictedDSN)
	if err != nil {
		t.Fatalf("failed to open restricted db connection: %v", err)
	}
	t.Cleanup(func() {
		restrictedDb.Close()
	})

	if err := restrictedDb.PingContext(context.Background()); err != nil {
		t.Fatalf("failed to ping restricted database: %v", err)
	}

	// Create two projects
	projectAID := createProject(t, ownerStore, "Project A")
	projectBID := createProject(t, ownerStore, "Project B")

	// Insert task in Project A using owner connection
	ctx := context.Background()
	task, err := ownerStore.Create(ctx, projectAID, "Task in Project A", "RLS verification", nil, 1, nil)
	if err != nil {
		t.Fatalf("failed to create task in project A: %v", err)
	}

	// 1. Attempt to read Project A's task using the restricted connection without setting RLS context, verifying RLS blocks it.
	var foundID string
	err = restrictedDb.QueryRowContext(ctx, "SELECT id FROM tasks WHERE id = $1", task.ID).Scan(&foundID)
	if err == nil {
		t.Errorf("expected task to be hidden by RLS when no project context is set, but read ID: %s", foundID)
	} else if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("unexpected error when querying task without project context: %v", err)
	}

	// 2. Attempt to read Project A's task using the restricted connection with RLS context set, verifying it succeeds.
	restrictedStore := NewStore(restrictedDb, events.NewStore(restrictedDb))
	tasks, err := restrictedStore.List(ctx, projectAID, nil)
	if err != nil {
		t.Fatalf("failed to list tasks under restricted store with correct context: %v", err)
	}
	found := false
	for _, tsk := range tasks {
		if tsk.ID == task.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected task %s to be visible with correct project context, but it was not found in the list", task.ID)
	}

	// Test manually within a transaction setting the correct context
	tx, err := restrictedDb.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectAID); err != nil {
		t.Fatalf("failed to set RLS context: %v", err)
	}

	err = tx.QueryRowContext(ctx, "SELECT id FROM tasks WHERE id = $1", task.ID).Scan(&foundID)
	if err != nil {
		t.Errorf("expected task to be visible manually with project A RLS context, got error: %v", err)
	}

	// 3. Attempt to read Project A's task using the restricted connection with Project B's RLS context set, verifying it returns empty/not found.
	tasksB, err := restrictedStore.List(ctx, projectBID, nil)
	if err != nil {
		t.Fatalf("failed to list tasks under restricted store with project B context: %v", err)
	}
	for _, tsk := range tasksB {
		if tsk.ID == task.ID {
			t.Errorf("task %s (from project A) was visible under restricted store when project B context was set", task.ID)
		}
	}

	// Test manually within a transaction setting the wrong context (Project B)
	txB, err := restrictedDb.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction B: %v", err)
	}
	defer txB.Rollback()

	if _, err := txB.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectBID); err != nil {
		t.Fatalf("failed to set RLS context to project B: %v", err)
	}

	err = txB.QueryRowContext(ctx, "SELECT id FROM tasks WHERE id = $1", task.ID).Scan(&foundID)
	if err == nil {
		t.Errorf("expected task to be hidden manually when project B context is set, but read ID: %s", foundID)
	} else if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("unexpected error when querying task with mismatched context: %v", err)
	}
}

func TestRLSProjectBoundaries(t *testing.T) {
	ownerStore := testStore(t)

	roleName := "rls_boundary_test_user"
	rolePassword := "rls_boundary_test_password"

	// Ensure clean slate and cleanup afterwards
	t.Cleanup(func() {
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE tasks FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE projects FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE passports FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("DROP ROLE IF EXISTS %s", roleName))
	})

	_, err := ownerStore.db.Exec(fmt.Sprintf("DROP ROLE IF EXISTS %s", roleName))
	if err != nil {
		t.Fatalf("failed to drop pre-existing role: %v", err)
	}

	_, err = ownerStore.db.Exec(fmt.Sprintf("CREATE ROLE %s WITH LOGIN PASSWORD '%s'", roleName, rolePassword))
	if err != nil {
		t.Fatalf("failed to create role: %v", err)
	}

	_, err = ownerStore.db.Exec(fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE tasks, projects, passports TO %s", roleName))
	if err != nil {
		t.Fatalf("failed to grant table privileges: %v", err)
	}

	cfg := types.LoadConfig()
	u, err := url.Parse(cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("failed to parse database URL: %v", err)
	}
	u.User = url.UserPassword(roleName, rolePassword)
	restrictedDSN := u.String()

	restrictedDb, err := sql.Open("postgres", restrictedDSN)
	if err != nil {
		t.Fatalf("failed to open restricted db connection: %v", err)
	}
	t.Cleanup(func() {
		restrictedDb.Close()
	})

	if err := restrictedDb.PingContext(context.Background()); err != nil {
		t.Fatalf("failed to ping restricted database: %v", err)
	}

	restrictedStore := NewStore(restrictedDb, events.NewStore(restrictedDb))
	ctx := context.Background()

	// Create two projects using ownerStore
	projectAID := createProject(t, ownerStore, "Project A (Boundaries)")
	projectBID := createProject(t, ownerStore, "Project B (Boundaries)")

	// Create a task in Project A
	taskA, err := ownerStore.Create(ctx, projectAID, "Task in Project A", "", nil, 1, nil)
	if err != nil {
		t.Fatalf("failed to create task in project A: %v", err)
	}

	// 1. Attempt to create a task in Project B with ParentTaskID pointing to Project A's task
	_, err = restrictedStore.Create(ctx, projectBID, "Child Task in Project B", "", &taskA.ID, 1, nil)
	if err == nil {
		t.Errorf("expected cross-project parent task linkage to fail, but it succeeded")
	} else if !errors.Is(err, ErrTaskNotFound) {
		t.Errorf("expected error wrapping ErrTaskNotFound, got: %v", err)
	}

	// 2. Create agent and passport in Project A
	agentID := createAgent(t, ownerStore)
	createPassport(t, ownerStore, agentID, projectAID)

	// Create a task in Project B using ownerStore
	taskB, err := ownerStore.Create(ctx, projectBID, "Task in Project B", "", nil, 1, nil)
	if err != nil {
		t.Fatalf("failed to create task in project B: %v", err)
	}

	// 3. Attempt to assign task in Project B to the agent who only has passport in Project A
	_, err = restrictedStore.Assign(ctx, projectBID, taskB.ID, agentID)
	if err == nil {
		t.Errorf("expected cross-project agent assignment to fail, but it succeeded")
	} else if !errors.Is(err, ErrPassportNotFound) {
		t.Errorf("expected error wrapping ErrPassportNotFound, got: %v", err)
	}

	// 4. Create passport for the agent in Project B using ownerStore
	createPassport(t, ownerStore, agentID, projectBID)

	// 5. Try assignment again, which should now succeed because the agent has a passport in Project B
	gotTask, err := restrictedStore.Assign(ctx, projectBID, taskB.ID, agentID)
	if err != nil {
		t.Errorf("expected assignment to succeed, but got error: %v", err)
	}
	if gotTask.OwnerAgentID == nil || *gotTask.OwnerAgentID != agentID {
		t.Errorf("expected assignee to be %q, got %v", agentID, gotTask.OwnerAgentID)
	}
}

func TestUpdateStatusInvalidTransitionMessage(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "transition-msg-test")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)
	channelID := createChannel(t, s, projectID, "transition-msg-events")

	task, err := s.Create(ctx, projectID, "t", "d", nil, 0, nil)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	_, err = s.UpdateStatus(ctx, projectID, task.ID, "blocked", channelID, agentID)
	if err == nil {
		t.Fatal("expected error for todo -> blocked")
	}
	want := "tasks: invalid status transition: todo -> blocked (valid from todo: wip): tasks: invalid status transition"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("errors.Is(err, ErrInvalidTransition) = false, want true")
	}
}

func TestUpdateStatusInvalidTransitionMessageTerminal(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "transition-msg-terminal-test")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)
	channelID := createChannel(t, s, projectID, "transition-msg-terminal-events")

	task, err := s.Create(ctx, projectID, "t", "d", nil, 0, nil)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := s.UpdateStatus(ctx, projectID, task.ID, "wip", channelID, agentID); err != nil {
		t.Fatalf("todo -> wip: %v", err)
	}
	if _, err := s.UpdateStatus(ctx, projectID, task.ID, "done", channelID, agentID); err != nil {
		t.Fatalf("wip -> done: %v", err)
	}

	_, err = s.UpdateStatus(ctx, projectID, task.ID, "wip", channelID, agentID)
	if err == nil {
		t.Fatal("expected error for done -> wip")
	}
	want := "tasks: invalid status transition: done -> wip (done is a terminal state, no valid transitions): tasks: invalid status transition"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("errors.Is(err, ErrInvalidTransition) = false, want true")
	}
}
