package roles

import (
	"context"
	"strings"
	"testing"
)

func shadowRoleTemplateStore(t *testing.T, permissions, capabilities, rolesJSON string) *Store {
	t.Helper()
	s := testStore(t)
	s.db.SetMaxOpenConns(1)
	if _, err := s.db.Exec(`CREATE TEMP TABLE role_templates (
		name text PRIMARY KEY,
		permission_bundle jsonb NOT NULL,
		default_capabilities jsonb NOT NULL,
		default_roles jsonb NOT NULL,
		default_task_view jsonb NOT NULL,
		created_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		t.Fatalf("create shadow role_templates: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO role_templates
		(name, permission_bundle, default_capabilities, default_roles, default_task_view)
		VALUES ('broken', $1, $2, $3, '{}')`, permissions, capabilities, rolesJSON); err != nil {
		t.Fatalf("insert shadow role template: %v", err)
	}
	return s
}

func TestGetTemplateRejectsMalformedBundles(t *testing.T) {
	tests := []struct {
		name         string
		permissions  string
		capabilities string
		roles        string
		want         string
	}{
		{"permission bundle", `{}`, `[]`, `[]`, "unmarshal permission bundle"},
		{"default capabilities", `[]`, `{}`, `[]`, "unmarshal default capabilities"},
		{"default roles", `[]`, `[]`, `{}`, "unmarshal default roles"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := shadowRoleTemplateStore(t, tt.permissions, tt.capabilities, tt.roles)
			_, err := s.GetTemplate(context.Background(), "broken")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("GetTemplate error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestListTemplatesRejectsMalformedBundles(t *testing.T) {
	tests := []struct {
		name         string
		permissions  string
		capabilities string
		roles        string
		want         string
	}{
		{"permission bundle", `{}`, `[]`, `[]`, "unmarshal permission bundle"},
		{"default capabilities", `[]`, `{}`, `[]`, "unmarshal default capabilities"},
		{"default roles", `[]`, `[]`, `{}`, "unmarshal default roles"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := shadowRoleTemplateStore(t, tt.permissions, tt.capabilities, tt.roles)
			_, err := s.ListTemplates(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ListTemplates error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRoleTemplateReadsReportQueryAndScanErrors(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.GetTemplate(ctx, "contributor"); err == nil || !strings.Contains(err.Error(), "get template") {
		t.Fatalf("GetTemplate error = %v, want query cancellation", err)
	}
	if _, err := s.ListTemplates(ctx); err == nil || !strings.Contains(err.Error(), "list templates") {
		t.Fatalf("ListTemplates error = %v, want query cancellation", err)
	}

	s = testStore(t)
	s.db.SetMaxOpenConns(1)
	if _, err := s.db.Exec(`CREATE TEMP TABLE role_templates (
		name text,
		permission_bundle jsonb,
		default_capabilities jsonb,
		default_roles jsonb,
		default_task_view jsonb,
		created_at text
	)`); err != nil {
		t.Fatalf("create scan-error role_templates: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO role_templates VALUES ('broken', '[]', '[]', '[]', '{}', 'not-a-time')`); err != nil {
		t.Fatalf("insert scan-error role template: %v", err)
	}
	if _, err := s.ListTemplates(context.Background()); err == nil || !strings.Contains(err.Error(), "scan row") {
		t.Fatalf("ListTemplates error = %v, want scan row failure", err)
	}
}
