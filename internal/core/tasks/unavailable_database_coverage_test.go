package tasks

import (
	"context"
	"testing"
)

func TestTaskStoreOperationsFailClosedWhenDatabaseIsUnavailable(t *testing.T) {
	s := testStore(t)
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := "00000000-0000-0000-0000-000000000001"
	checks := []struct {
		name string
		call func() error
	}{
		{"create", func() error { _, err := s.Create(ctx, id, "title", "description", nil, 1, nil); return err }},
		{"create with id", func() error { _, err := s.CreateWithID(ctx, id, id, "title", "description", nil, 1, nil); return err }},
		{"create with owner", func() error {
			_, err := s.CreateWithIDAndOwner(ctx, id, id, "title", "description", nil, nil, 1, nil)
			return err
		}},
		{"assign", func() error { _, err := s.Assign(ctx, id, id, id); return err }},
		{"list", func() error { _, err := s.List(ctx, id, nil); return err }},
		{"update status", func() error { _, err := s.UpdateStatus(ctx, id, id, "done", id, id); return err }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); err == nil {
				t.Fatal("operation succeeded with a closed database")
			}
		})
	}
}
