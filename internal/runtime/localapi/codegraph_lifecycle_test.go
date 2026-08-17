package localapi

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	codegraphindex "github.com/H4RL33/wormhole/internal/runtime/codegraph/index"
	codegraphstore "github.com/H4RL33/wormhole/internal/runtime/codegraph/store"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
)

func TestCodeGraphLifecycleEnableValidatesRepositoryAndPublishesAtomically(t *testing.T) {
	ctx := context.Background()
	state, db := newLifecycleTestState(t, "project-a", []string{"https://example.invalid/approved.git"})

	t.Run("non git", func(t *testing.T) {
		if _, err := state.Enable(ctx, t.TempDir()); err == nil {
			t.Fatal("Enable(non-Git) error = nil")
		}
		assertLifecycleDisabled(t, db, state)
	})

	t.Run("mismatched remote", func(t *testing.T) {
		checkout := lifecycleGitRepository(t, "https://example.invalid/other.git", "package other\n")
		if _, err := state.Enable(ctx, checkout); !errors.Is(err, ErrCodeGraphRepositoryMismatch) {
			t.Fatalf("Enable(mismatched remote) error = %v, want ErrCodeGraphRepositoryMismatch", err)
		}
		assertLifecycleDisabled(t, db, state)
	})

	t.Run("tracked symlink escape", func(t *testing.T) {
		checkout := lifecycleGitRepository(t, "https://example.invalid/approved.git", "package approved\n")
		outside := filepath.Join(t.TempDir(), "outside.go")
		if err := os.WriteFile(outside, []byte("package outside\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(checkout, "source.go")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(checkout, "source.go")); err != nil {
			t.Fatal(err)
		}
		lifecycleGit(t, checkout, "add", "source.go")
		lifecycleGit(t, checkout, "commit", "-m", "escape")
		if _, err := state.Enable(ctx, checkout); err == nil {
			t.Fatal("Enable(symlink escape) error = nil")
		}
		assertLifecycleDisabled(t, db, state)
	})

	t.Run("initial build failure", func(t *testing.T) {
		checkout := lifecycleGitRepository(t, "https://example.invalid/approved.git", "package broken\nfunc Broken( {\n")
		if _, err := state.Enable(ctx, checkout); err == nil {
			t.Fatal("Enable(broken checkout) error = nil")
		}
		assertLifecycleDisabled(t, db, state)
	})

	checkout := lifecycleGitRepository(t, "https://example.invalid/approved.git", "package approved\nfunc Ready() {}\n")
	status, err := state.Enable(ctx, checkout)
	if err != nil {
		t.Fatalf("Enable(clean checkout) error = %v", err)
	}
	canonical, _ := filepath.EvalSymlinks(checkout)
	if !status.Enabled || status.ActiveCheckout != canonical || status.ActiveRevision == "" || status.IndexedCommit == "" {
		t.Fatalf("enabled status = %+v", status)
	}
}

func TestCodeGraphLifecycleCheckoutAndRebuildFailuresRetainPublishedState(t *testing.T) {
	ctx := context.Background()
	state, _ := newLifecycleTestState(t, "project-a", []string{"https://example.invalid/approved.git"})
	first := lifecycleGitRepository(t, "https://example.invalid/approved.git", "package first\nfunc First() {}\n")
	before, err := state.Enable(ctx, first)
	if err != nil {
		t.Fatal(err)
	}

	broken := lifecycleGitRepository(t, "https://example.invalid/approved.git", "package broken\nfunc Broken( {\n")
	if _, err := state.SetCheckout(ctx, broken); err == nil {
		t.Fatal("SetCheckout(broken) error = nil")
	}
	afterSwitch, err := state.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterSwitch.ActiveCheckout != before.ActiveCheckout || afterSwitch.ActiveRevision != before.ActiveRevision {
		t.Fatalf("failed checkout changed state: before=%+v after=%+v", before, afterSwitch)
	}

	if err := os.WriteFile(filepath.Join(first, "source.go"), []byte("package first\nfunc Broken( {\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Rebuild(ctx); err == nil {
		t.Fatal("Rebuild(broken working tree) error = nil")
	}
	afterRebuild, err := state.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterRebuild.ActiveCheckout != before.ActiveCheckout || afterRebuild.ActiveRevision != before.ActiveRevision {
		t.Fatalf("failed rebuild changed state: before=%+v after=%+v", before, afterRebuild)
	}
}

func TestCodeGraphLifecycleRebuildRejectsDisabledBeforeRepositoryBinding(t *testing.T) {
	state, _ := newLifecycleTestState(t, "project-a", []string{"https://example.invalid/approved.git"})
	if _, err := state.Rebuild(context.Background()); !errors.Is(err, codegraphindex.ErrProjectDisabled) {
		t.Fatalf("Rebuild(disabled) = %v, want ErrProjectDisabled", err)
	}
}

func TestCodeGraphLifecycleSuccessfulCheckoutSwitchDoesNotMergePayload(t *testing.T) {
	ctx := context.Background()
	state, _ := newLifecycleTestState(t, "project-a", []string{"https://example.invalid/approved.git"})
	first := lifecycleGitRepository(t, "https://example.invalid/approved.git", "package first\nfunc FirstOnly() {}\n")
	before, err := state.Enable(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	configured, err := state.store.ProjectConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	configured.ProjectSourceByteCeiling = 1234567
	if err := state.store.PutProjectConfig(ctx, configured); err != nil {
		t.Fatal(err)
	}
	second := lifecycleGitRepository(t, "https://example.invalid/approved.git", "package second\nfunc SecondOnly() {}\n")
	after, err := state.SetCheckout(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := filepath.EvalSymlinks(second)
	if after.ActiveCheckout != canonical || after.ActiveRevision == before.ActiveRevision {
		t.Fatalf("checkout switch before=%+v after=%+v", before, after)
	}
	preserved, err := state.store.ProjectConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if preserved.ProjectSourceByteCeiling != configured.ProjectSourceByteCeiling {
		t.Fatalf("checkout switch ceiling = %d, want %d", preserved.ProjectSourceByteCeiling, configured.ProjectSourceByteCeiling)
	}
	if _, err := state.Enable(ctx, second); err != nil {
		t.Fatalf("re-enable existing checkout: %v", err)
	}
	preserved, err = state.store.ProjectConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if preserved.ProjectSourceByteCeiling != configured.ProjectSourceByteCeiling {
		t.Fatalf("re-enable ceiling = %d, want %d", preserved.ProjectSourceByteCeiling, configured.ProjectSourceByteCeiling)
	}
	old, err := state.store.Revision(ctx, before.ActiveRevision)
	if err != nil || old.State != codegraphstore.RevisionRetired {
		t.Fatalf("old revision = %+v, %v", old, err)
	}
	if err := state.store.ReadActive(ctx, func(snapshot *codegraphstore.Snapshot) error {
		symbols, err := snapshot.Symbols(ctx)
		if err != nil {
			return err
		}
		for _, symbol := range symbols {
			if strings.Contains(symbol.QualifiedName, "FirstOnly") {
				t.Fatalf("old checkout symbol merged into new payload: %s", symbol.QualifiedName)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCodeGraphLifecycleRejectsPassportRepositoryRotationBeforePublication(t *testing.T) {
	ctx := context.Background()
	state, db := newLifecycleTestState(t, "project-a", []string{"https://example.invalid/approved.git"})
	checkout := lifecycleGitRepository(t, "https://example.invalid/approved.git", "package approved\nfunc Ready() {}\n")
	entered := make(chan struct{})
	release := make(chan struct{})
	state.beforeBuild = func() {
		close(entered)
		<-release
	}
	done := make(chan error, 1)
	go func() {
		_, err := state.executeWithBinding(ctx, CodeGraphLifecycleRequest{
			Operation: CodeGraphEnable, ProjectID: "project-a", Checkout: checkout,
		}, codeGraphRepositoryBinding{profile: "profile", agent: "agent", passport: "passport"})
		done <- err
	}()
	<-entered
	if _, err := db.Exec(`UPDATE passports SET repositories = '["https://example.invalid/revoked.git"]' WHERE namespace_id = 'project-a' AND id = 'passport'`); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrCodeGraphRepositoryMismatch) {
		t.Fatalf("Enable after Passport repository rotation = %v, want ErrCodeGraphRepositoryMismatch", err)
	}
	assertLifecycleDisabled(t, db, state)
}

func TestCodeGraphLifecycleDisableDeletesAllGraphStateAndPreservesGit(t *testing.T) {
	ctx := context.Background()
	state, db := newLifecycleTestState(t, "project-a", []string{"https://example.invalid/approved.git"})
	checkout := lifecycleGitRepository(t, "https://example.invalid/approved.git", "package approved\nfunc Ready() {}\n")
	if _, err := state.Enable(ctx, checkout); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if err := state.store.CreateCandidate(ctx, codegraphstore.Revision{ProjectID: "project-a", ID: "surviving-candidate", IndexedCommit: strings.Repeat("a", 40), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := state.store.PutNode(ctx, codegraphstore.Node{ProjectID: "project-a", RevisionID: "surviving-candidate", ID: "candidate-node", Kind: codegraphstore.NodeRepository, Name: "candidate"}); err != nil {
		t.Fatal(err)
	}
	if err := state.store.PutDiagnostic(ctx, codegraphstore.Diagnostic{ProjectID: "project-a", RevisionID: "surviving-candidate", ID: "candidate-diagnostic", Severity: codegraphstore.DiagnosticInfo, Code: "candidate", Message: "candidate", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := state.store.CreateCandidate(ctx, codegraphstore.Revision{ProjectID: "project-a", ID: "failed-candidate", IndexedCommit: strings.Repeat("b", 40), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := state.store.FailCandidate(ctx, "failed-candidate", "failed", "failed"); err != nil {
		t.Fatal(err)
	}
	beforeHead := lifecycleGit(t, checkout, "rev-parse", "HEAD")
	beforeStatus := lifecycleGit(t, checkout, "status", "--porcelain=v1", "-uall")

	if err := state.Disable(ctx); err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	for _, table := range []string{"codegraph_config", "codegraph_revisions", "codegraph_nodes", "codegraph_files", "codegraph_symbols", "codegraph_edges", "codegraph_diagnostics", "codegraph_lifecycle"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE project_id = ?", "project-a").Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Errorf("%s rows = %d, want 0", table, count)
		}
	}
	status, err := state.Status(ctx)
	if err != nil || status.Enabled || status.ActiveRevision != "" || status.ActiveCheckout != "" {
		t.Fatalf("disabled status = %+v, %v", status, err)
	}
	if got := lifecycleGit(t, checkout, "rev-parse", "HEAD"); got != beforeHead {
		t.Fatalf("HEAD changed: before=%q after=%q", beforeHead, got)
	}
	if got := lifecycleGit(t, checkout, "status", "--porcelain=v1", "-uall"); got != beforeStatus {
		t.Fatalf("working tree changed: before=%q after=%q", beforeStatus, got)
	}
}

func TestCodeGraphLifecycleUsesBootstrappedPassportRepositoryScope(t *testing.T) {
	state, _ := newLifecycleTestState(t, "project-a", nil)
	checkout := lifecycleGitRepository(t, "https://example.invalid/unapproved.git", "package unapproved\n")
	if _, err := state.Enable(context.Background(), checkout); !errors.Is(err, ErrCodeGraphRepositoryBinding) {
		t.Fatalf("Enable(without approved repositories) error = %v, want ErrCodeGraphRepositoryBinding", err)
	}
}

func TestCodeGraphLifecycleRejectsIncompleteOrMalformedPassportBinding(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*sql.DB) error
	}{
		{"wrong passport project", func(db *sql.DB) error { _, err := db.Exec(`UPDATE passports SET project_id = 'project-b'`); return err }},
		{"malformed repositories", func(db *sql.DB) error { _, err := db.Exec(`UPDATE passports SET repositories = '{bad'`); return err }},
		{"missing bootstrap metadata", func(db *sql.DB) error { _, err := db.Exec(`DELETE FROM bootstrap_metadata`); return err }},
		{"missing auth scope", func(db *sql.DB) error { _, err := db.Exec(`DELETE FROM auth_scopes`); return err }},
		{"not ready", func(db *sql.DB) error {
			_, err := db.Exec(`UPDATE enrolment_attempts SET state = 'failed', terminal = 0`)
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, db := newLifecycleTestState(t, "project-a", []string{"https://example.invalid/approved.git"})
			if err := tt.mutate(db); err != nil {
				t.Fatal(err)
			}
			checkout := lifecycleGitRepository(t, "https://example.invalid/approved.git", "package approved\n")
			if _, err := state.Enable(context.Background(), checkout); !errors.Is(err, ErrCodeGraphRepositoryBinding) {
				t.Fatalf("Enable() error = %v, want ErrCodeGraphRepositoryBinding", err)
			}
		})
	}
}

func TestCodeGraphLifecycleUsesOnlyExplicitActivePassportNotStaleReadyPassport(t *testing.T) {
	state, db := newLifecycleTestState(t, "project-a", []string{"https://example.invalid/current.git"})
	statements := []string{
		`INSERT INTO agents(namespace_id,id,owner,model,capabilities,created_at) VALUES ('project-a','old-agent','owner','model','[]',CURRENT_TIMESTAMP)`,
		`INSERT INTO passports(namespace_id,id,agent_id,project_id,repositories,roles,issued_at) VALUES ('project-a','old-passport','old-agent','project-a','["https://example.invalid/old.git"]','[]',CURRENT_TIMESTAMP)`,
		`INSERT INTO auth_scopes(namespace_id,agent_id,passport_id,permissions) VALUES ('project-a','old-agent','old-passport','[]')`,
		`INSERT INTO whoami_cache(agent_id,owner,model,capabilities,project_id,permissions,cached_at) VALUES ('old-agent','owner','model','[]','project-a','[]',CURRENT_TIMESTAMP)`,
		`INSERT INTO enrolment_attempts(project_id,idempotency_key,request_hash,state,credential_profile,agent_id,passport_id,terminal) VALUES ('project-a','old-key','old-hash','ready','old-profile','old-agent','old-passport',1)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	oldCheckout := lifecycleGitRepository(t, "https://example.invalid/old.git", "package old\n")
	request := CodeGraphLifecycleRequest{Operation: CodeGraphEnable, ProjectID: "project-a", Checkout: oldCheckout}
	binding := codeGraphRepositoryBinding{profile: "profile", agent: "agent", passport: "passport"}
	if _, err := state.executeWithBinding(context.Background(), request, binding); !errors.Is(err, ErrCodeGraphRepositoryMismatch) {
		t.Fatalf("stale Passport checkout error = %v, want ErrCodeGraphRepositoryMismatch", err)
	}
	currentCheckout := lifecycleGitRepository(t, "https://example.invalid/current.git", "package current\n")
	request.Checkout = currentCheckout
	if _, err := state.executeWithBinding(context.Background(), request, binding); err != nil {
		t.Fatalf("active Passport checkout error = %v", err)
	}
	request.Operation = CodeGraphRebuild
	request.Checkout = ""
	if _, err := state.executeWithBinding(context.Background(), request, binding); err != nil {
		t.Fatalf("active Passport rebuild error = %v", err)
	}
	binding = codeGraphRepositoryBinding{profile: "old-profile", agent: "old-agent", passport: "old-passport"}
	if _, err := state.executeWithBinding(context.Background(), request, binding); !errors.Is(err, ErrCodeGraphRepositoryMismatch) {
		t.Fatalf("stale Passport rebuild error = %v, want ErrCodeGraphRepositoryMismatch", err)
	}
}

func newLifecycleTestState(t *testing.T, projectID string, repositories []string) (*CodeGraphLifecycle, *sql.DB) {
	t.Helper()
	local, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close() })
	repositoriesJSON := `[]`
	if len(repositories) > 0 {
		repositoriesJSON = `["` + strings.Join(repositories, `","`) + `"]`
	}
	if _, err := local.DB().Exec(`INSERT INTO passports (namespace_id,id,agent_id,project_id,repositories,roles,issued_at) VALUES (?,?,?,?,?,?,CURRENT_TIMESTAMP)`, projectID, "passport", "agent", projectID, repositoriesJSON, `[]`); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO projects(namespace_id,id,name,owner,created_at) VALUES ('` + projectID + `','` + projectID + `','project','owner',CURRENT_TIMESTAMP)`,
		`INSERT INTO agents(namespace_id,id,owner,model,capabilities,created_at) VALUES ('` + projectID + `','agent','owner','model','[]',CURRENT_TIMESTAMP)`,
		`INSERT INTO auth_scopes(namespace_id,agent_id,passport_id,permissions) VALUES ('` + projectID + `','agent','passport','[]')`,
		`INSERT INTO whoami_cache(agent_id,owner,model,capabilities,project_id,permissions,cached_at) VALUES ('agent','owner','model','[]','` + projectID + `','[]',CURRENT_TIMESTAMP)`,
		`INSERT INTO enrolment_attempts(project_id,idempotency_key,request_hash,state,credential_profile,agent_id,passport_id,terminal) VALUES ('` + projectID + `','key','hash','ready','profile','agent','passport',1)`,
		`INSERT INTO bootstrap_metadata(namespace_id,schema_version,integration_manifest_metadata,bootstrap_timestamp) VALUES ('` + projectID + `',1,'{}',CURRENT_TIMESTAMP)`,
	}
	for _, statement := range statements {
		if _, err := local.DB().Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	state, err := NewCodeGraphLifecycle(context.Background(), local.DB(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	return state, local.DB()
}

func assertLifecycleDisabled(t *testing.T, db *sql.DB, state *CodeGraphLifecycle) {
	t.Helper()
	status, err := state.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled || status.ActiveRevision != "" || status.ActiveCheckout != "" {
		t.Fatalf("lifecycle state = %+v, want disabled without active graph", status)
	}
	var active int
	if err := db.QueryRow(`SELECT COUNT(*) FROM codegraph_revisions WHERE project_id = ? AND state = 'active'`, "project-a").Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatalf("active revisions = %d, want 0", active)
	}
}

func lifecycleGitRepository(t *testing.T, remote, source string) string {
	t.Helper()
	root := t.TempDir()
	lifecycleGit(t, root, "init")
	lifecycleGit(t, root, "config", "user.email", "test@example.invalid")
	lifecycleGit(t, root, "config", "user.name", "Test")
	lifecycleGit(t, root, "remote", "add", "origin", remote)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.invalid/lifecycle\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	lifecycleGit(t, root, "add", "go.mod", "source.go")
	lifecycleGit(t, root, "commit", "-m", "fixture")
	return root
}

func lifecycleGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}
