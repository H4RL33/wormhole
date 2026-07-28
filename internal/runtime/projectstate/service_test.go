package projectstate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
)

func TestRegisterWorkspaceIdempotent(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	store, service := openProjectStateService(t, "")
	request := RegisterWorkspaceRequest{Root: repository.root, ExpectedProjectID: repository.projectID, ExpectedRepository: repository.identity, ExpectedCommit: repository.commit}
	first, err := service.RegisterWorkspace(context.Background(), request)
	if err != nil || !first.Created {
		t.Fatalf("first registration created=%v err=%v", first.Created, err)
	}
	if err := first.Binding.Validate(); err != nil {
		t.Fatalf("registered binding: %v", err)
	}
	if _, err := store.DB().Exec(`
		CREATE TRIGGER reject_workspace_insert BEFORE INSERT ON workspace_bindings BEGIN SELECT RAISE(ABORT, 'idempotent registration inserted'); END;
		CREATE TRIGGER reject_workspace_update BEFORE UPDATE ON workspace_bindings BEGIN SELECT RAISE(ABORT, 'idempotent registration updated'); END;
		CREATE TRIGGER reject_workspace_delete BEFORE DELETE ON workspace_bindings BEGIN SELECT RAISE(ABORT, 'idempotent registration deleted'); END;
	`); err != nil {
		t.Fatal(err)
	}
	second, err := service.RegisterWorkspace(context.Background(), request)
	if err != nil || second.Created {
		t.Fatalf("repeat registration created=%v err=%v", second.Created, err)
	}
	if second.Binding != first.Binding {
		t.Fatalf("repeat binding=%+v, want %+v", second.Binding, first.Binding)
	}
}

func TestRegisterWorkspaceCheckoutCollision(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	_, service := openProjectStateService(t, "")
	first := RegisterWorkspaceRequest{Root: repository.root, ExpectedProjectID: repository.projectID, ExpectedRepository: repository.identity, ExpectedCommit: repository.commit}
	if _, err := service.RegisterWorkspace(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	secondProject := "00000000-0000-4000-8000-000000000002"
	secondCommit := commitSnapshot(t, repository.root, secondProject, repository.identity)
	_, err := service.RegisterWorkspace(context.Background(), RegisterWorkspaceRequest{
		Root: repository.root, ExpectedProjectID: secondProject, ExpectedRepository: repository.identity, ExpectedCommit: secondCommit,
	})
	if !errors.Is(err, localstore.ErrCheckoutCollision) {
		t.Fatalf("collision error=%v, want ErrCheckoutCollision", err)
	}
}

func TestTwoWorktreesDistinct(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	worktree := filepath.Join(t.TempDir(), "second-worktree")
	runGit(t, repository.root, "worktree", "add", "-b", "second-worktree", worktree, repository.commit)
	_, service := openProjectStateService(t, "")

	first, err := service.RegisterWorkspace(context.Background(), RegisterWorkspaceRequest{
		Root: repository.root, ExpectedProjectID: repository.projectID, ExpectedRepository: repository.identity, ExpectedCommit: repository.commit,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.RegisterWorkspace(context.Background(), RegisterWorkspaceRequest{
		Root: worktree, ExpectedProjectID: repository.projectID, ExpectedRepository: repository.identity, ExpectedCommit: repository.commit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Binding.Scope.WorkspaceID == second.Binding.Scope.WorkspaceID {
		t.Fatal("two worktrees received the same workspace ID")
	}
	if first.Binding.Checkout == second.Binding.Checkout {
		t.Fatal("two worktrees received the same checkout identity")
	}
}

func TestRegisterWorkspaceReadsCommittedTree(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	if err := os.WriteFile(filepath.Join(repository.root, ".wormhole", "config.toml"), []byte("untrusted working tree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, service := openProjectStateService(t, "")
	result, err := service.RegisterWorkspace(context.Background(), RegisterWorkspaceRequest{
		Root: repository.root, ExpectedProjectID: repository.projectID, ExpectedRepository: repository.identity, ExpectedCommit: repository.commit,
	})
	if err != nil {
		t.Fatalf("RegisterWorkspace used the caller working tree: %v", err)
	}
	if result.Binding.AcceptedCommitSHA != repository.commit {
		t.Fatalf("accepted commit=%q, want %q", result.Binding.AcceptedCommitSHA, repository.commit)
	}
}

func TestRegisterWorkspaceRejectsCallerIdentityMismatch(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	_, service := openProjectStateService(t, "")
	wrong := types.RepositoryIdentity{Provider: "github", ImmutableID: "R_wrong", CanonicalRemote: "https://github.com/acme/wrong"}
	_, err := service.RegisterWorkspace(context.Background(), RegisterWorkspaceRequest{
		Root: repository.root, ExpectedProjectID: repository.projectID, ExpectedRepository: wrong, ExpectedCommit: repository.commit,
	})
	if err == nil {
		t.Fatal("RegisterWorkspace accepted a repository identity differing from the committed snapshot")
	}
}

func TestRegisterWorkspaceRejectsHeadMismatch(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	_, service := openProjectStateService(t, "")
	_, err := service.RegisterWorkspace(context.Background(), RegisterWorkspaceRequest{
		Root: repository.root, ExpectedProjectID: repository.projectID, ExpectedRepository: repository.identity, ExpectedCommit: strings.Repeat("a", 40),
	})
	if !errors.Is(err, ErrInvalidRegistration) {
		t.Fatalf("HEAD mismatch error=%v, want ErrInvalidRegistration", err)
	}
}

func TestRegisterWorkspaceRejectsSymlinkRoot(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	link := filepath.Join(t.TempDir(), "repository-link")
	if err := os.Symlink(repository.root, link); err != nil {
		t.Fatal(err)
	}
	_, service := openProjectStateService(t, "")
	_, err := service.RegisterWorkspace(context.Background(), RegisterWorkspaceRequest{
		Root: link, ExpectedProjectID: repository.projectID, ExpectedRepository: repository.identity, ExpectedCommit: repository.commit,
	})
	if err == nil {
		t.Fatal("RegisterWorkspace accepted a symlink checkout root")
	}
}

func TestNewServiceRejectsBackupRootInsideRepository(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	backup := filepath.Join(repository.root, ".private-backups")
	store, err := localstore.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := NewService(localstore.NewWorkspaceRepo(store.DB()), ServiceConfig{LegacyIntegrationBackupRoot: backup}); err == nil {
		t.Fatal("NewService accepted a repository-contained process-private backup root")
	}
}

func TestNewServiceBackupRootIsOwnerOnly(t *testing.T) {
	backup := filepath.Join(t.TempDir(), "state", "legacy-backups")
	_, _ = openProjectStateService(t, backup)
	info, err := os.Stat(backup)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("backup mode=%#o, want 0700", info.Mode().Perm())
	}
}

func TestNewServiceDoesNotRepurposePermissiveDirectoryAsBackupRoot(t *testing.T) {
	backup := filepath.Join(t.TempDir(), "existing")
	if err := os.Mkdir(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(backup, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := localstore.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := NewService(localstore.NewWorkspaceRepo(store.DB()), ServiceConfig{LegacyIntegrationBackupRoot: backup}); err == nil {
		t.Fatal("NewService repurposed a permissive existing directory")
	}
	info, err := os.Stat(backup)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("rejected backup mode=%#o, want unchanged 0755", info.Mode().Perm())
	}
}

func TestNewServiceRejectsInvalidBackupRoots(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := localstore.NewWorkspaceRepo(store.DB())
	if _, err := NewService(repo, ServiceConfig{LegacyIntegrationBackupRoot: "relative"}); err == nil {
		t.Fatal("NewService accepted a relative backup root")
	}
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "backup-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(repo, ServiceConfig{LegacyIntegrationBackupRoot: link}); err == nil {
		t.Fatal("NewService accepted a symlinked backup root")
	}
}

func TestRegisterWorkspaceStatusIsScoped(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	_, service := openProjectStateService(t, "")
	registered, err := service.RegisterWorkspace(context.Background(), RegisterWorkspaceRequest{
		Root: repository.root, ExpectedProjectID: repository.projectID, ExpectedRepository: repository.identity, ExpectedCommit: repository.commit,
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := service.Status(context.Background(), registered.Binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if status.Binding != registered.Binding || status.State != "clean" {
		t.Fatalf("status=%+v", status)
	}
	wrong := registered.Binding.Scope
	wrong.ProjectID = "00000000-0000-4000-8000-000000000002"
	if _, err := service.Status(context.Background(), wrong); !errors.Is(err, localstore.ErrNotFound) {
		t.Fatalf("wrong-project Status error=%v, want ErrNotFound", err)
	}
}

func openProjectStateService(t *testing.T, backupRoot string) (*localstore.Store, *Service) {
	t.Helper()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service, err := NewService(localstore.NewWorkspaceRepo(store.DB()), ServiceConfig{LegacyIntegrationBackupRoot: backupRoot})
	if err != nil {
		t.Fatal(err)
	}
	return store, service
}

func requireNotFound(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, localstore.ErrNotFound) {
		t.Fatalf("error=%v, want ErrNotFound", err)
	}
}
