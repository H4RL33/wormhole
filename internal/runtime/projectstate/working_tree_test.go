package projectstate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestResolveWorkingDirectoryChild(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	child := filepath.Join(repository.root, "nested", "child")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	_, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	got, err := service.ResolveWorkingDirectory(context.Background(), types.WorkspaceContext{WorkingDirectory: child})
	if err != nil {
		t.Fatal(err)
	}
	if got != registered.Binding {
		t.Fatalf("resolved=%+v, want %+v", got, registered.Binding)
	}
}

func TestResolveWorkingDirectoryRejectsSiblingPrefix(t *testing.T) {
	parent := t.TempDir()
	repository := createGitRepositoryAt(t, filepath.Join(parent, "repo"), "00000000-0000-4000-8000-000000000001")
	sibling := filepath.Join(parent, "repository", "child")
	if err := os.MkdirAll(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	_, service := openProjectStateService(t, "")
	registerGitRepository(t, service, repository)
	_, err := service.ResolveWorkingDirectory(context.Background(), types.WorkspaceContext{WorkingDirectory: sibling})
	requireNotFound(t, err)
}

func TestResolveWorkingDirectoryRejectsReplacedCheckout(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	_, service := openProjectStateService(t, "")
	registerGitRepository(t, service, repository)
	moved := repository.root + ".original"
	if err := os.Rename(repository.root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(repository.root, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := service.ResolveWorkingDirectory(context.Background(), types.WorkspaceContext{WorkingDirectory: repository.root})
	requireNotFound(t, err)
}

func TestResolveWorkingDirectoryLongestAncestor(t *testing.T) {
	outer := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	inner := createGitRepositoryAt(t, filepath.Join(outer.root, "nested", "repo"), "00000000-0000-4000-8000-000000000002")
	child := filepath.Join(inner.root, "deeper")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	_, service := openProjectStateService(t, "")
	registerGitRepository(t, service, outer)
	innerBinding := registerGitRepository(t, service, inner)
	got, err := service.ResolveWorkingDirectory(context.Background(), types.WorkspaceContext{WorkingDirectory: child})
	if err != nil {
		t.Fatal(err)
	}
	if got != innerBinding.Binding {
		t.Fatalf("resolved=%+v, want inner %+v", got, innerBinding.Binding)
	}
}

func TestResolveWorkingDirectoryRejectsAmbiguousMatch(t *testing.T) {
	root := t.TempDir()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := localstore.NewWorkspaceRepo(store.DB())
	projectID := "00000000-0000-4000-8000-000000000001"
	tree := testSnapshotTree(t, projectID, types.RepositoryIdentity{})
	snapshot, err := state.DecodeTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	for index, workspaceID := range []types.WorkspaceID{
		"00000000-0000-4000-8000-000000000011",
		"00000000-0000-4000-8000-000000000012",
	} {
		binding := types.WorkspaceBinding{
			Scope:      types.WorkspaceScope{ProjectID: projectID, WorkspaceID: workspaceID},
			Checkout:   types.CheckoutIdentity{CanonicalPath: root, Device: uint64(index + 1), Inode: uint64(index + 11)},
			Repository: types.RepositoryIdentity{}, AcceptedRef: "refs/heads/main",
			AcceptedCommitSHA: strings.Repeat("a", 40), AcceptedTreeDigest: string(snapshot.Digest),
		}
		if _, _, err := repo.RegisterWorkspace(context.Background(), binding, tree); err != nil {
			t.Fatal(err)
		}
	}
	service, err := NewService(repo, ServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ResolveWorkingDirectory(context.Background(), types.WorkspaceContext{WorkingDirectory: root})
	requireNotFound(t, err)
}

func TestResolveWorkingDirectoryEvaluatesChildSymlink(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	child := filepath.Join(repository.root, "real-child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "child-link")
	if err := os.Symlink(child, link); err != nil {
		t.Fatal(err)
	}
	_, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	got, err := service.ResolveWorkingDirectory(context.Background(), types.WorkspaceContext{WorkingDirectory: link})
	if err != nil {
		t.Fatal(err)
	}
	if got != registered.Binding {
		t.Fatalf("resolved=%+v, want %+v", got, registered.Binding)
	}
}

func TestRegisteredWorkspacesStableAfterRestart(t *testing.T) {
	database := filepath.Join(t.TempDir(), "gateway.db")
	firstRepo := createGitRepository(t, "00000000-0000-4000-8000-000000000002")
	secondRepo := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	store, err := localstore.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(localstore.NewWorkspaceRepo(store.DB()), ServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	registerGitRepository(t, service, firstRepo)
	registerGitRepository(t, service, secondRepo)
	before, err := service.RegisteredWorkspaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = localstore.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err = NewService(localstore.NewWorkspaceRepo(store.DB()), ServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	after, err := service.RegisteredWorkspaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 2 || after[0] != before[0] || after[1] != before[1] {
		t.Fatalf("after restart=%+v, want %+v", after, before)
	}
	if after[0].Scope.ProjectID > after[1].Scope.ProjectID {
		t.Fatalf("workspaces are not sorted: %+v", after)
	}
}

func TestWorkingTreeGitReaderEnforcesOutputLimit(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	if _, err := readOnlyGitLimited(context.Background(), repository.root, 1, "rev-parse", "HEAD"); err == nil {
		t.Fatal("read-only Git runner ignored its output limit")
	}
}

type gitRepository struct {
	root      string
	projectID string
	identity  types.RepositoryIdentity
	commit    string
}

func createGitRepository(t *testing.T, projectID string) gitRepository {
	t.Helper()
	return createGitRepositoryAt(t, filepath.Join(t.TempDir(), "repository"), projectID)
}

func createGitRepositoryAt(t *testing.T, root, projectID string) gitRepository {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "-b", "main")
	runGit(t, root, "config", "user.name", "Wormhole Tests")
	runGit(t, root, "config", "user.email", "wormhole@example.invalid")
	identity := types.RepositoryIdentity{}
	commit := commitSnapshot(t, root, projectID, identity)
	return gitRepository{root: root, projectID: projectID, identity: identity, commit: commit}
}

func commitSnapshot(t *testing.T, root, projectID string, identity types.RepositoryIdentity) string {
	t.Helper()
	tree := testSnapshotTree(t, projectID, identity)
	for _, file := range tree {
		path := filepath.Join(root, ".wormhole", filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, file.Data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, root, "add", ".wormhole")
	runGit(t, root, "commit", "-m", "snapshot")
	return strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
}

func testSnapshotTree(t *testing.T, projectID string, repository types.RepositoryIdentity) state.Tree {
	t.Helper()
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	snapshot := state.Snapshot{
		Config:  state.ConfigV1{SnapshotVersion: 1, ProjectID: projectID, Handle: types.ProjectHandle{Namespace: "acme", Name: "wormhole"}, Repository: repository},
		Project: state.ProjectV1{SchemaVersion: 1, Kind: "project", ID: projectID, Name: "Wormhole", Aliases: []string{}, CreatedAt: now, UpdatedAt: now, Extensions: state.ExtensionsV1{}},
		Actors:  map[string]state.Record[state.ActorV1]{}, Tasks: map[string]state.Record[state.TaskV1]{},
		TaskLinks: map[string]state.Record[state.TaskLinkV1]{}, Articles: map[string]state.KBRecord{},
		Channels: map[string]state.Record[state.ChannelV1]{}, Events: map[string]state.EventV1{},
		GitLinks: map[string]state.Record[state.GitLinkV1]{},
	}
	tree, err := state.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func registerGitRepository(t *testing.T, service *Service, repository gitRepository) RegisterWorkspaceResult {
	t.Helper()
	result, err := service.RegisterWorkspace(context.Background(), RegisterWorkspaceRequest{
		Root: repository.root, ExpectedProjectID: repository.projectID, ExpectedRepository: repository.identity, ExpectedCommit: repository.commit,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func runGit(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}
