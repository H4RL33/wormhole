package identity

import (
	"context"
	"strings"
	"testing"
)

func TestBootstrapSnapshotReadsCanonicalIdentityAndTimestamp(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "bootstrap-identity")
	agent, passport, _, err := s.Register(ctx, projectID, []string{"task.read", "kb.read", "task.read"}, "owner", "model", []string{"review", "code", "review"}, []string{"z", "a"}, []string{"contributor", "contributor"})
	if err != nil {
		t.Fatal(err)
	}
	cleanupAgent(t, s, agent.ID)

	tx, err := s.BeginBootstrapSnapshotTx(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	project, identity, err := s.ReadBootstrapIdentityInTx(ctx, tx, projectID, agent.ID, []string{"task.read", "kb.read", "task.read"})
	if err != nil {
		t.Fatalf("ReadBootstrapIdentityInTx: %v", err)
	}
	if project.ID != projectID || identity.Agent.ID != agent.ID || identity.Passport.ID != passport.ID {
		t.Fatalf("bootstrap identity = project %#v identity %#v", project, identity)
	}
	if got := strings.Join(identity.Agent.Capabilities, ","); got != "code,review" {
		t.Fatalf("capabilities = %q", got)
	}
	if got := strings.Join(identity.Passport.Repositories, ","); got != "a,z" {
		t.Fatalf("repositories = %q", got)
	}
	if got := strings.Join(identity.Permissions, ","); got != "kb.read,task.read" {
		t.Fatalf("permissions = %q", got)
	}
	if timestamp, err := s.BootstrapTimestampInTx(ctx, tx); err != nil || timestamp.IsZero() {
		t.Fatalf("BootstrapTimestampInTx = %v, %v", timestamp, err)
	}
}

func TestBootstrapIdentityRejectsNonArrayJSONFields(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "bootstrap-invalid-json-shapes")
	agent, _, _, err := s.Register(ctx, projectID, []string{"task.read"}, "owner", "model", []string{"code"}, []string{"repo"}, []string{"role"})
	if err != nil {
		t.Fatal(err)
	}
	cleanupAgent(t, s, agent.ID)

	tests := []struct {
		name   string
		update string
		want   string
	}{
		{name: "capabilities", update: `UPDATE agents SET capabilities = '"scalar"'::jsonb WHERE id = $1`, want: "decode bootstrap capabilities"},
		{name: "repositories", update: `UPDATE passports SET repositories = '"scalar"'::jsonb WHERE agent_id = $1 AND project_id = $2`, want: "decode bootstrap repositories"},
		{name: "roles", update: `UPDATE passports SET roles = '"scalar"'::jsonb WHERE agent_id = $1 AND project_id = $2`, want: "decode bootstrap roles"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.db.ExecContext(ctx, `UPDATE agents SET capabilities = '["code"]'::jsonb WHERE id = $1`, agent.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.ExecContext(ctx, `UPDATE passports SET repositories = '["repo"]'::jsonb, roles = '["role"]'::jsonb WHERE agent_id = $1 AND project_id = $2`, agent.ID, projectID); err != nil {
				t.Fatal(err)
			}
			var err error
			if tt.name == "capabilities" {
				_, err = s.db.ExecContext(ctx, tt.update, agent.ID)
			} else {
				_, err = s.db.ExecContext(ctx, tt.update, agent.ID, projectID)
			}
			if err != nil {
				t.Fatal(err)
			}
			tx, err := s.BeginBootstrapSnapshotTx(ctx, projectID)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			_, _, err = s.ReadBootstrapIdentityInTx(ctx, tx, projectID, agent.ID, []string{"task.read"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ReadBootstrapIdentityInTx error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestBootstrapIdentityRejectsInvalidScope(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.BeginBootstrapSnapshotTx(ctx, ""); err == nil {
		t.Fatal("empty project unexpectedly accepted")
	}
	if _, _, err := s.ReadBootstrapIdentityInTx(ctx, nil, "project", "agent", []string{}); err == nil {
		t.Fatal("nil transaction unexpectedly accepted")
	}
	if _, err := s.BootstrapTimestampInTx(ctx, nil); err == nil {
		t.Fatal("nil timestamp transaction unexpectedly accepted")
	}
}
