package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func TestCodeGraphLifecycleExecuteRejectsInvalidScopeBindingAndOperation(t *testing.T) {
	ctx := context.Background()
	var nilLifecycle *CodeGraphLifecycle
	if _, err := nilLifecycle.Execute(ctx, CodeGraphLifecycleRequest{}); err == nil {
		t.Fatal("nil lifecycle Execute succeeded")
	}
	lifecycle, _ := newLifecycleTestState(t, "project-a", []string{"https://example.invalid/approved.git"})
	tests := []CodeGraphLifecycleRequest{
		{Operation: CodeGraphStatus, ProjectID: "project-b"},
		{Operation: CodeGraphRebuild, ProjectID: "project-a", CredentialProfile: "profile"},
		{Operation: "unsupported", ProjectID: "project-a"},
	}
	for _, request := range tests {
		if _, err := lifecycle.Execute(ctx, request); err == nil {
			t.Fatalf("Execute(%+v) succeeded", request)
		}
	}
}

func TestCodeGraphStatusWithoutActiveRevisionReportsCheckoutFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
		code   string
	}{
		{"missing checkout", func(t *testing.T, checkout string) {
			if err := os.Rename(checkout, checkout+"-moved"); err != nil {
				t.Fatal(err)
			}
		}, "checkout_inspection_failed"},
		{"remote mismatch", func(t *testing.T, checkout string) {
			runCodeGraphGit(t, checkout, "remote", "set-url", "origin", "https://example.invalid/drifted.git")
		}, "canonical_remote_mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, checkout := newCodeGraphStatusFixture(t)
			tt.mutate(t, checkout)
			status := codeGraphStatus(t, srv)
			if status.State != "error" || status.ActiveRevision != "" {
				t.Fatalf("status = %+v, want error without active revision", status)
			}
			found := false
			for _, diagnostic := range status.LatestDiagnostics {
				found = found || diagnostic.Code == tt.code
			}
			if !found {
				t.Fatalf("status diagnostics = %+v, missing %q", status.LatestDiagnostics, tt.code)
			}
		})
	}
}

func TestCodeGraphRepositoryBindingRejectsNoncanonicalAndAmbiguousReadyPassports(t *testing.T) {
	ctx := context.Background()
	t.Run("noncanonical repository", func(t *testing.T) {
		lifecycle, db := newLifecycleTestState(t, "project-a", []string{"https://example.invalid/approved.git"})
		if _, err := db.Exec(`UPDATE passports SET repositories = '[" https://example.invalid/approved.git"]' WHERE id = 'passport'`); err != nil {
			t.Fatal(err)
		}
		checkout := lifecycleGitRepository(t, "https://example.invalid/approved.git", "package approved\n")
		if _, err := lifecycle.Enable(ctx, checkout); !errors.Is(err, ErrCodeGraphRepositoryBinding) {
			t.Fatalf("Enable error = %v, want ErrCodeGraphRepositoryBinding", err)
		}
	})

	t.Run("ambiguous ready passports", func(t *testing.T) {
		lifecycle, db := newLifecycleTestState(t, "project-a", []string{"https://example.invalid/approved.git"})
		statements := []string{
			`INSERT INTO agents(namespace_id,id,owner,model,capabilities,created_at) VALUES ('project-a','agent-2','owner','model','[]',CURRENT_TIMESTAMP)`,
			`INSERT INTO passports(namespace_id,id,agent_id,project_id,repositories,roles,issued_at) VALUES ('project-a','passport-2','agent-2','project-a','["https://example.invalid/approved.git"]','[]',CURRENT_TIMESTAMP)`,
			`INSERT INTO auth_scopes(namespace_id,agent_id,passport_id,permissions) VALUES ('project-a','agent-2','passport-2','[]')`,
			`INSERT INTO whoami_cache(agent_id,owner,model,capabilities,project_id,permissions,cached_at) VALUES ('agent-2','owner','model','[]','project-a','[]',CURRENT_TIMESTAMP)`,
			`INSERT INTO enrolment_attempts(project_id,idempotency_key,request_hash,state,credential_profile,agent_id,passport_id,terminal) VALUES ('project-a','key-2','hash-2','ready','profile-2','agent-2','passport-2',1)`,
		}
		for _, statement := range statements {
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
		checkout := lifecycleGitRepository(t, "https://example.invalid/approved.git", "package approved\n")
		if _, err := lifecycle.Enable(ctx, checkout); !errors.Is(err, ErrCodeGraphRepositoryBinding) {
			t.Fatalf("Enable error = %v, want ErrCodeGraphRepositoryBinding", err)
		}
	})
}

func TestCodeGraphRuntimeAndStatusFailClosedOnCorruptOrUnavailableRuntime(t *testing.T) {
	srv, _ := newMCPTestServer(t)
	srv.codeGraphs.Store("project-1", "not-a-runtime")
	if _, err := srv.resolveCodeGraphRuntime("project-1"); err == nil {
		t.Fatal("corrupt runtime resolved")
	}

	srv, runtime, _ := newCodeGraphStatusFixture(t)
	if err := srv.store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.handleCodeGraphStatus(context.Background(), json.RawMessage(`{"project_id":"project-1"}`)); err == nil {
		t.Fatal("status succeeded with unavailable database")
	}
	_ = runtime
}
