package tasks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestTaskOperationsPropagateCanceledContext(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	operations := map[string]func() error{
		"create": func() error {
			_, err := s.Create(ctx, uuid.NewString(), "title", "description", nil, 0, nil)
			return err
		},
		"assign": func() error {
			_, err := s.Assign(ctx, uuid.NewString(), uuid.NewString(), uuid.NewString())
			return err
		},
		"list": func() error {
			_, err := s.List(ctx, uuid.NewString(), nil)
			return err
		},
		"update status": func() error {
			_, err := s.UpdateStatus(ctx, uuid.NewString(), uuid.NewString(), "wip", uuid.NewString(), uuid.NewString())
			return err
		},
	}

	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			if err := operation(); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
		})
	}
}

func TestCreateRejectsCrossProjectParentWithoutWriting(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectA := createProject(t, s, "parent-project-a")
	projectB := createProject(t, s, "parent-project-b")
	parent, err := s.Create(ctx, projectA, "Parent", "", nil, 0, nil)
	if err != nil {
		t.Fatalf("Create parent: %v", err)
	}

	_, err = s.Create(ctx, projectB, "Child", "", &parent.ID, 0, nil)
	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("Create cross-project child error = %v, want ErrTaskNotFound", err)
	}

	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM tasks WHERE project_id = $1`, projectB).Scan(&count); err != nil {
		t.Fatalf("count project-B tasks: %v", err)
	}
	if count != 0 {
		t.Fatalf("project-B task count = %d, want 0", count)
	}
}

func TestCreateWithIDDuplicatePreservesOriginal(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "duplicate-task-id")
	id := uuid.NewString()
	if _, err := s.CreateWithID(ctx, id, projectID, "Original", "original", nil, 1, nil); err != nil {
		t.Fatalf("first CreateWithID: %v", err)
	}
	if _, err := s.CreateWithID(ctx, id, projectID, "Replacement", "replacement", nil, 2, nil); err == nil || !strings.Contains(err.Error(), "tasks: create") {
		t.Fatalf("duplicate CreateWithID error = %v, want wrapped insert error", err)
	}

	var title string
	if err := s.db.QueryRowContext(ctx, `SELECT title FROM tasks WHERE id = $1`, id).Scan(&title); err != nil {
		t.Fatalf("query original task: %v", err)
	}
	if title != "Original" {
		t.Fatalf("title after duplicate = %q, want Original", title)
	}
}

func TestAssignRejectsCrossProjectTaskWithoutMutation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectA := createProject(t, s, "assign-task-project-a")
	projectB := createProject(t, s, "assign-task-project-b")
	task, err := s.Create(ctx, projectA, "Project A task", "", nil, 0, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectB)

	_, err = s.Assign(ctx, projectB, task.ID, agentID)
	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("Assign cross-project task error = %v, want ErrTaskNotFound", err)
	}
	var owner *string
	if err := s.db.QueryRowContext(ctx, `SELECT owner_agent_id FROM tasks WHERE id = $1`, task.ID).Scan(&owner); err != nil {
		t.Fatalf("query owner: %v", err)
	}
	if owner != nil {
		t.Fatalf("owner after rejected assignment = %q, want nil", *owner)
	}
}

func TestUpdateStatusRejectsCrossProjectTaskWithoutEvent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectA := createProject(t, s, "status-task-project-a")
	projectB := createProject(t, s, "status-task-project-b")
	task, err := s.Create(ctx, projectA, "Project A task", "", nil, 0, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectB)
	channelID := createChannel(t, s, projectB, "status-events")

	_, err = s.UpdateStatus(ctx, projectB, task.ID, "wip", channelID, agentID)
	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("UpdateStatus cross-project task error = %v, want ErrTaskNotFound", err)
	}
	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id = $1`, task.ID).Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "todo" {
		t.Fatalf("status after rejected update = %q, want todo", status)
	}
	var eventCount int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE channel_id = $1`, channelID).Scan(&eventCount); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("event count = %d, want 0", eventCount)
	}
}

func TestUpdateStatusUnknownChannelRollsBackTask(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "status-unknown-channel")
	task, err := s.Create(ctx, projectID, "Task", "", nil, 0, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	_, err = s.UpdateStatus(ctx, projectID, task.ID, "wip", uuid.NewString(), agentID)
	if err == nil || !strings.Contains(err.Error(), "channel not found") {
		t.Fatalf("UpdateStatus error = %v, want channel-not-found error", err)
	}
	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id = $1`, task.ID).Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "todo" {
		t.Fatalf("status after rejected update = %q, want todo", status)
	}
}

func TestCreateRoundTripsDueDate(t *testing.T) {
	s := testStore(t)
	due := time.Now().UTC().Truncate(time.Microsecond).Add(24 * time.Hour)
	created, err := s.Create(context.Background(), createProject(t, s, "task-due-date"), "Due task", "", nil, 0, &due)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.DueBy == nil || !created.DueBy.Equal(due) {
		t.Fatalf("DueBy = %v, want %v", created.DueBy, due)
	}
}
