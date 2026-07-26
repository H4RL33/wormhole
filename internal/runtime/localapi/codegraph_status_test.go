package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	codegraphconfig "github.com/H4RL33/wormhole/internal/runtime/codegraph/config"
	codegraphindex "github.com/H4RL33/wormhole/internal/runtime/codegraph/index"
	codegraphstore "github.com/H4RL33/wormhole/internal/runtime/codegraph/store"
)

func TestCodeGraphStatusSixStatesAndRequiredFields(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *Server, CodeGraphRuntime, string)
		want  string
	}{
		{"disabled", func(t *testing.T, _ *Server, runtime CodeGraphRuntime, _ string) {
			putCodeGraphConfig(t, runtime, codegraphconfig.DefaultProject("project-1"))
		}, "disabled"},
		{"initializing", func(t *testing.T, _ *Server, _ CodeGraphRuntime, _ string) {}, "initializing"},
		{"ready", func(t *testing.T, _ *Server, runtime CodeGraphRuntime, _ string) {
			buildCodeGraphFixture(t, runtime, "ready")
		}, "ready"},
		{"degraded", func(t *testing.T, _ *Server, runtime CodeGraphRuntime, checkout string) {
			buildCodeGraphFixture(t, runtime, "active")
			writeCodeGraphFile(t, checkout, "target.go", "package fixture\nfunc Broken( {\n")
			if err := runtime.Index.Build(context.Background(), codegraphindex.BuildRequest{ProjectID: "project-1", RevisionID: "failed"}); err == nil {
				t.Fatal("broken rebuild unexpectedly succeeded")
			}
		}, "degraded"},
		{"stale", func(t *testing.T, _ *Server, runtime CodeGraphRuntime, checkout string) {
			buildCodeGraphFixture(t, runtime, "stale")
			writeCodeGraphFile(t, checkout, "target.go", "package fixture\nfunc Target() int { return 2 }\n")
		}, "stale"},
		{"error", func(t *testing.T, _ *Server, runtime CodeGraphRuntime, checkout string) {
			writeCodeGraphFile(t, checkout, "target.go", "package fixture\nfunc Broken( {\n")
			if err := runtime.Index.Build(context.Background(), codegraphindex.BuildRequest{ProjectID: "project-1", RevisionID: "failed-first"}); err == nil {
				t.Fatal("broken initial build unexpectedly succeeded")
			}
		}, "error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv, runtime, checkout := newCodeGraphStatusFixture(t)
			test.setup(t, srv, runtime, checkout)
			beforeConfig, _ := runtime.Store.ProjectConfig(context.Background())
			beforeActive, beforeActiveErr := runtime.Store.ActiveRevision(context.Background())
			beforePayload := statusPayloadCounts(t, runtime, beforeActive, beforeActiveErr)
			result := codeGraphStatus(t, srv)
			if result.State != test.want {
				t.Fatalf("state = %q, want %q; result=%+v", result.State, test.want, result)
			}
			if result.DatabaseSize <= 0 || result.LatestDiagnostics == nil {
				t.Fatalf("required database/diagnostic fields missing: %+v", result)
			}
			if test.want == "ready" || test.want == "degraded" || test.want == "stale" {
				if result.ActiveCheckout != checkout || result.CanonicalRemote == "" || result.ActiveRevision == "" || result.IndexedCommit == "" || result.TrackedGoFileCount != 1 || result.SymbolCount < 1 || result.EdgeCount < 1 || result.LastSuccessfulBuild == nil {
					t.Fatalf("active status fields/counters incomplete: %+v", result)
				}
			}
			if (test.want == "degraded" || test.want == "error") && !hasErrorDiagnostics(result.LatestDiagnostics) {
				t.Fatalf("failure state lacks latest error diagnostics: %+v", result)
			}
			afterConfig, _ := runtime.Store.ProjectConfig(context.Background())
			afterActive, afterActiveErr := runtime.Store.ActiveRevision(context.Background())
			afterPayload := statusPayloadCounts(t, runtime, afterActive, afterActiveErr)
			sameActiveError := (beforeActiveErr == nil && afterActiveErr == nil) || (errors.Is(beforeActiveErr, codegraphstore.ErrNotFound) && errors.Is(afterActiveErr, codegraphstore.ErrNotFound))
			if !reflect.DeepEqual(beforeConfig, afterConfig) || !sameActiveError || (beforeActiveErr == nil && !reflect.DeepEqual(beforeActive, afterActive)) || beforePayload != afterPayload {
				t.Fatalf("status mutated config/revision\nbefore=%+v/%+v/%v\nafter=%+v/%+v/%v", beforeConfig, beforeActive, beforeActiveErr, afterConfig, afterActive, afterActiveErr)
			}
		})
	}
}

func TestCodeGraphStatusPreservesActiveFieldsWhileRebuildInProgress(t *testing.T) {
	srv, runtime, _ := newCodeGraphStatusFixture(t)
	buildCodeGraphFixture(t, runtime, "active-before-rebuild")
	runtime.rebuildMu.Lock()
	defer runtime.rebuildMu.Unlock()
	result := codeGraphStatus(t, srv)
	if result.State != "initializing" || result.ActiveRevision != "active-before-rebuild" || result.SymbolCount < 1 || result.EdgeCount < 1 || result.LastSuccessfulBuild == nil {
		t.Fatalf("in-progress status lost active graph fields: %+v", result)
	}
}

func TestCodeGraphStatusReturnsHealthPayloadForCheckoutAndRemoteFailures(t *testing.T) {
	for _, test := range []struct {
		name          string
		breakCheckout func(*testing.T, string)
		wantCode      string
		wantState     string
	}{
		{"missing checkout", func(t *testing.T, checkout string) {
			if err := os.Rename(checkout, checkout+"-moved"); err != nil {
				t.Fatal(err)
			}
		}, "checkout_inspection_failed", "degraded"},
		{"remote drift", func(t *testing.T, checkout string) {
			runCodeGraphGit(t, checkout, "remote", "set-url", "origin", "https://example.invalid/drifted.git")
		}, "canonical_remote_mismatch", "stale"},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv, runtime, checkout := newCodeGraphStatusFixture(t)
			buildCodeGraphFixture(t, runtime, "active")
			test.breakCheckout(t, checkout)
			result := codeGraphStatus(t, srv)
			if result.State != test.wantState || result.ActiveRevision != "active" || result.SymbolCount < 1 {
				t.Fatalf("health failure status = %+v, want %s active payload", result, test.wantState)
			}
			found := false
			for _, diagnostic := range result.LatestDiagnostics {
				if diagnostic.Code == test.wantCode {
					found = true
					if len(diagnostic.Message) > 1024 || strings.Contains(diagnostic.Message, checkout) {
						t.Fatalf("health diagnostic not bounded/sanitized: %+v", diagnostic)
					}
				}
			}
			if !found {
				t.Fatalf("missing health diagnostic %q: %+v", test.wantCode, result.LatestDiagnostics)
			}
			stored, err := runtime.Store.LatestDiagnostics(context.Background())
			if err != nil || len(stored) != 0 {
				t.Fatalf("health diagnostic was persisted: diagnostics=%+v err=%v", stored, err)
			}
		})
	}
}

func newCodeGraphStatusFixture(t *testing.T) (*Server, CodeGraphRuntime, string) {
	t.Helper()
	srv, _ := newMCPTestServer(t)
	checkout := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "test@example.invalid"}, {"config", "user.name", "test"}, {"remote", "add", "origin", "https://example.invalid/status.git"}} {
		runCodeGraphGit(t, checkout, args...)
	}
	writeCodeGraphFile(t, checkout, "go.mod", "module example.invalid/status\n\ngo 1.26\n")
	writeCodeGraphFile(t, checkout, "target.go", "package fixture\nfunc Target() int { return 1 }\n")
	runCodeGraphGit(t, checkout, "add", ".")
	runCodeGraphGit(t, checkout, "commit", "-m", "fixture")
	runtime, err := NewCodeGraphRuntime(context.Background(), srv.store.DB(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	putCodeGraphConfig(t, runtime, codegraphconfig.Project{ProjectID: "project-1", Enabled: true, CanonicalRemote: "https://example.invalid/status.git", ActiveCheckout: checkout, ProjectSourceByteCeiling: codegraphconfig.DefaultProjectSourceByteCeiling})
	srv.SetCodeGraphRuntime("project-1", runtime)
	return srv, runtime, checkout
}

func buildCodeGraphFixture(t *testing.T, runtime CodeGraphRuntime, revision string) {
	t.Helper()
	if err := runtime.Index.Build(context.Background(), codegraphindex.BuildRequest{ProjectID: "project-1", RevisionID: revision}); err != nil {
		t.Fatalf("Build: %v", err)
	}
}

func putCodeGraphConfig(t *testing.T, runtime CodeGraphRuntime, project codegraphconfig.Project) {
	t.Helper()
	if err := runtime.Store.PutProjectConfig(context.Background(), project); err != nil {
		t.Fatal(err)
	}
}

func writeCodeGraphFile(t *testing.T, checkout, name, content string) {
	t.Helper()
	path := filepath.Join(checkout, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func codeGraphStatus(t *testing.T, srv *Server) codeGraphStatusResult {
	t.Helper()
	value, err := srv.handleCodeGraphStatus(context.Background(), json.RawMessage(`{"project_id":"project-1"}`))
	if err != nil {
		t.Fatalf("status returned tool failure: %v", err)
	}
	return value.(codeGraphStatusResult)
}

func statusPayloadCounts(t *testing.T, runtime CodeGraphRuntime, revision codegraphstore.Revision, revisionErr error) codegraphstore.PayloadCounts {
	t.Helper()
	if revisionErr != nil {
		return codegraphstore.PayloadCounts{}
	}
	var counts codegraphstore.PayloadCounts
	if err := runtime.Store.ReadRevision(context.Background(), revision.ID, func(snapshot *codegraphstore.Snapshot) error {
		var err error
		counts, err = snapshot.PayloadCounts(context.Background())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return counts
}
