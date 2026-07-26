package tasks

import (
	"context"
	"strings"
	"testing"
)

func TestListBootstrapOrdersParentsBeforeChildrenAndRejectsCycles(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "bootstrap-task-graph")

	parent, err := s.Create(ctx, projectID, "parent", "", nil, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.Create(ctx, projectID, "child", "", &parent.ID, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := s.Create(ctx, projectID, "sibling", "", nil, 1, nil)
	if err != nil {
		t.Fatal(err)
	}

	read := func() ([]string, error) {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectID); err != nil {
			return nil, err
		}
		rows, err := s.ListBootstrapInTx(ctx, tx, projectID)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		return ids, nil
	}

	ids, err := read()
	if err != nil {
		t.Fatalf("ListBootstrapInTx: %v", err)
	}
	position := make(map[string]int, len(ids))
	for index, id := range ids {
		position[id] = index
	}
	parentPosition, parentPresent := position[parent.ID]
	childPosition, childPresent := position[child.ID]
	_, siblingPresent := position[sibling.ID]
	if len(ids) != 3 || !parentPresent || !childPresent || !siblingPresent || parentPosition >= childPosition {
		t.Fatalf("bootstrap order = %v; want parent before child and all tasks", ids)
	}

	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET parent_task_id = $1 WHERE id = $2`, child.ID, parent.ID); err != nil {
		t.Fatalf("create cycle: %v", err)
	}
	if _, err := read(); err == nil || !strings.Contains(err.Error(), "contains a cycle") {
		t.Fatalf("cycle error = %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET parent_task_id = NULL WHERE id = $1`, parent.ID); err != nil {
		t.Fatalf("remove cycle: %v", err)
	}
}

func TestListBootstrapRejectsInvalidScope(t *testing.T) {
	s := testStore(t)
	if _, err := s.ListBootstrapInTx(context.Background(), nil, "project"); err == nil {
		t.Fatal("nil transaction unexpectedly accepted")
	}
}
