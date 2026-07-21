package roles

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"reflect"
	"testing"

	_ "github.com/lib/pq"

	"github.com/H4RL33/wormhole/internal/types"
)

// testStore opens a real connection to the configured Postgres instance
// and skips the test if it isn't reachable — these are integration tests
// against real schema/RLS behavior, not mocks (RFC-0001 §13 claims are
// about actual storage guarantees).
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

// TestGetTemplate_NotFound covers the error case when a template name
// doesn't exist.
func TestGetTemplate_NotFound(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	_, err := s.GetTemplate(ctx, "nonexistent-role")
	if err == nil {
		t.Fatal("GetTemplate: expected error, got nil")
	}
	if !errors.Is(err, ErrTemplateNotFound) {
		t.Errorf("error = %v, want ErrTemplateNotFound", err)
	}
}

// TestListTemplates_All verifies that ListTemplates returns all six
// pre-seeded templates in deterministic order (by name ascending).
func TestListTemplates_All(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	got, err := s.ListTemplates(ctx)
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}

	if len(got) != 6 {
		t.Errorf("count = %d, want 6", len(got))
	}

	expectedOrder := []string{
		"backend-engineer",
		"contributor",
		"frontend-engineer",
		"maintainer",
		"project-manager",
		"reviewer",
	}

	if len(got) > 0 {
		gotNames := make([]string, len(got))
		for i, template := range got {
			gotNames[i] = template.Name
		}
		if !reflect.DeepEqual(gotNames, expectedOrder) {
			t.Errorf("order = %v, want %v", gotNames, expectedOrder)
		}
	}

	// Verify each template has a non-empty permission bundle and default view
	for _, tmpl := range got {
		if len(tmpl.PermissionBundle) == 0 {
			t.Errorf("template %q: PermissionBundle is empty", tmpl.Name)
		}
		if len(tmpl.DefaultTaskView) == 0 {
			t.Errorf("template %q: DefaultTaskView is empty", tmpl.Name)
		}
	}
}

// TestListTemplates_OrderingDeterministic verifies that ListTemplates
// always returns templates in the same order (alphabetically by name).
func TestListTemplates_OrderingDeterministic(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Call ListTemplates twice and verify identical order
	list1, err := s.ListTemplates(ctx)
	if err != nil {
		t.Fatalf("first ListTemplates: %v", err)
	}

	list2, err := s.ListTemplates(ctx)
	if err != nil {
		t.Fatalf("second ListTemplates: %v", err)
	}

	if len(list1) != len(list2) {
		t.Errorf("count differs: %d vs %d", len(list1), len(list2))
	}

	for i := range list1 {
		if list1[i].Name != list2[i].Name {
			t.Errorf("order differs at index %d: %q vs %q", i, list1[i].Name, list2[i].Name)
		}
	}
}

// TestGetTemplate_AllSeededRoles verifies that all six seeded templates
// can be retrieved and have the correct permission bundles.
func TestGetTemplate_AllSeededRoles(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	testCases := []struct {
		name          string
		expectedPerms []string
	}{
		{
			"backend-engineer",
			[]string{"task.list", "task.create", "task.update_status", "kb.search", "kb.get", "kb.get_links", "kb.write", "channel.list", "channel.subscribe", "channel.create", "channel.post", "git.link_commit", "git.request_review"},
		},
		{
			"frontend-engineer",
			[]string{"task.list", "task.create", "task.update_status", "kb.search", "kb.get", "kb.get_links", "kb.write", "channel.list", "channel.subscribe", "channel.create", "channel.post", "git.link_commit", "git.request_review"},
		},
		{
			"project-manager",
			[]string{"task.list", "task.create", "task.update_status", "kb.search", "kb.get", "kb.get_links", "kb.write", "channel.list", "channel.subscribe", "channel.create", "channel.post", "task.assign"},
		},
		{
			"contributor",
			[]string{"task.list", "task.create", "task.update_status", "kb.search", "kb.get", "kb.get_links", "kb.write", "channel.list", "channel.subscribe", "channel.create", "channel.post", "git.link_commit", "git.request_review"},
		},
		{
			"reviewer",
			[]string{"task.list", "kb.search", "kb.get", "kb.get_links", "kb.write", "channel.list", "channel.subscribe", "channel.create", "channel.post"},
		},
		{
			"maintainer",
			[]string{"task.list", "task.create", "task.update_status", "kb.search", "kb.get", "kb.get_links", "kb.write", "channel.list", "channel.subscribe", "channel.create", "channel.post", "task.assign", "git.link_commit", "git.request_review"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.GetTemplate(ctx, tc.name)
			if err != nil {
				t.Fatalf("GetTemplate: %v", err)
			}

			if !reflect.DeepEqual(got.PermissionBundle, tc.expectedPerms) {
				t.Errorf("PermissionBundle = %v, want %v", got.PermissionBundle, tc.expectedPerms)
			}

			// Migration 000014 replaced the coarse 000010 bundles wholesale;
			// any survivor means the re-seed missed this row.
			for _, p := range got.PermissionBundle {
				switch p {
				case "task.read", "task.write", "kb.read", "channel.read", "channel.write":
					t.Errorf("%s: coarse permission %q survived re-seed", tc.name, p)
				}
			}
		})
	}
}
