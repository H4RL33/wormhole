package projectstate

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
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

func TestBoundedGitOutput(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	if _, err := readOnlyGitLimited(context.Background(), repository.root, 1, "rev-parse", "HEAD"); err == nil {
		t.Fatal("read-only Git runner ignored its output limit")
	}
}

func TestCommittedTreeUsesSingleBatchReader(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "git.log")
	wrapper := filepath.Join(bin, "git")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> \"$WORMHOLE_GIT_LOG\"\n" +
		"exec \"$WORMHOLE_REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WORMHOLE_GIT_LOG", logPath)
	t.Setenv("WORMHOLE_REAL_GIT", realGit)

	if _, err := readCommittedTree(context.Background(), repository.root, repository.commit); err != nil {
		t.Fatal(err)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	catFileCommands := 0
	usedBatch := false
	for _, line := range strings.Split(string(logData), "\n") {
		if strings.Contains(line, " cat-file ") {
			catFileCommands++
			usedBatch = usedBatch || strings.Contains(line, " cat-file --batch")
		}
	}
	if catFileCommands != 1 || !usedBatch {
		t.Fatalf("Git commands used %d cat-file processes (batch=%v):\n%s", catFileCommands, usedBatch, logData)
	}
}

func TestPromisorMissingObjectFailsClosedWithoutNetwork(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	listing := strings.Fields(runGit(t, repository.root, "ls-tree", "-r", repository.commit, "--", ".wormhole"))
	if len(listing) < 3 {
		t.Fatalf("unexpected ls-tree output: %q", listing)
	}
	missingOID := listing[2]

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(writer, "network access forbidden", http.StatusInternalServerError)
	}))
	defer server.Close()
	runGit(t, repository.root, "config", "remote.origin.url", server.URL+"/repository")
	runGit(t, repository.root, "config", "remote.origin.promisor", "true")
	runGit(t, repository.root, "config", "remote.origin.partialclonefilter", "blob:none")
	objectPath := filepath.Join(repository.root, ".git", "objects", missingOID[:2], missingOID[2:])
	if err := os.Remove(objectPath); err != nil {
		t.Fatalf("remove promised object %s: %v", missingOID, err)
	}

	_, service := openProjectStateService(t, "")
	_, err := service.RegisterWorkspace(context.Background(), RegisterWorkspaceRequest{
		Root: repository.root, ExpectedProjectID: repository.projectID,
		ExpectedRepository: repository.identity, ExpectedCommit: repository.commit,
	})
	if !errors.Is(err, ErrInvalidRegistration) {
		t.Fatalf("registration error=%v, want ErrInvalidRegistration", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("missing promised object caused %d network request(s)", got)
	}
}

func TestNoNetworkGitEnvironment(t *testing.T) {
	for key, value := range map[string]string{
		"HTTPS_PROXY": "http://proxy.example", "http_proxy": "http://proxy.example",
		"ALL_PROXY": "socks5://proxy.example", "NO_PROXY": "localhost",
		"SSH_AUTH_SOCK": "/tmp/agent.sock", "SSH_AGENT_PID": "123",
		"GIT_SSH_COMMAND": "ssh -i secret", "GIT_ASKPASS": "/tmp/askpass",
		"GCM_CREDENTIAL_STORE": "secretservice",
	} {
		t.Setenv(key, value)
	}
	environment := make(map[string]string)
	for _, entry := range sanitizedGitEnvironment() {
		key, value, _ := strings.Cut(entry, "=")
		environment[key] = value
	}
	for _, key := range []string{
		"HTTPS_PROXY", "http_proxy", "ALL_PROXY", "NO_PROXY",
		"SSH_AUTH_SOCK", "SSH_AGENT_PID", "GCM_CREDENTIAL_STORE",
	} {
		if _, found := environment[key]; found {
			t.Errorf("sanitized Git environment retained %s", key)
		}
	}
	for key, want := range map[string]string{
		"GIT_NO_LAZY_FETCH": "1", "GIT_ALLOW_PROTOCOL": "",
		"GIT_SSH_COMMAND": "/bin/false", "GIT_ASKPASS": "/bin/false",
	} {
		if got := environment[key]; got != want {
			t.Errorf("%s=%q, want %q", key, got, want)
		}
	}
}

func TestBoundedCommittedTreeListing(t *testing.T) {
	oid := strings.Repeat("a", 40)
	t.Run("file count", func(t *testing.T) {
		var listing bytes.Buffer
		for index := 0; index <= maxCommittedTreeFiles; index++ {
			fmt.Fprintf(&listing, "100644 blob %s\t.wormhole/state/v1/%d.json%c", oid, index, 0)
		}
		if _, err := parseCommittedTreeListing(listing.Bytes()); err == nil || !strings.Contains(err.Error(), "file count") {
			t.Fatalf("parse error=%v, want file-count limit", err)
		}
	})
	t.Run("path size", func(t *testing.T) {
		path := strings.Repeat("a", maxCommittedTreePathBytes+1)
		listing := fmt.Sprintf("100644 blob %s\t.wormhole/%s%c", oid, path, 0)
		if _, err := parseCommittedTreeListing([]byte(listing)); err == nil || !strings.Contains(err.Error(), "path") {
			t.Fatalf("parse error=%v, want path-size limit", err)
		}
	})
}

func TestBoundedBatchObjectBytes(t *testing.T) {
	oidA := strings.Repeat("a", 40)
	oidB := strings.Repeat("b", 40)
	objects := []committedTreeObject{{path: "a", oid: oidA}, {path: "b", oid: oidB}}

	t.Run("single object", func(t *testing.T) {
		output := fmt.Sprintf("%s blob 4\ndata\n", oidA)
		_, err := decodeBatchObjects(bufio.NewReader(strings.NewReader(output)), objects[:1], 3, 10)
		if err == nil || !strings.Contains(err.Error(), "object size") {
			t.Fatalf("decode error=%v, want object-size limit", err)
		}
	})
	t.Run("aggregate", func(t *testing.T) {
		output := fmt.Sprintf("%s blob 3\none\n%s blob 3\ntwo\n", oidA, oidB)
		_, err := decodeBatchObjects(bufio.NewReader(strings.NewReader(output)), objects, 3, 5)
		if err == nil || !strings.Contains(err.Error(), "aggregate") {
			t.Fatalf("decode error=%v, want aggregate limit", err)
		}
	})
}

func TestBoundedGitStderr(t *testing.T) {
	bin := t.TempDir()
	wrapper := filepath.Join(bin, "git")
	script := fmt.Sprintf("#!/bin/sh\n/usr/bin/head -c %d /dev/zero | /usr/bin/tr '\\000' x >&2\nexit 1\n", maxGitStderrBytes+1024)
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	_, err := readOnlyGitLimited(context.Background(), t.TempDir(), 1, "rev-parse", "HEAD")
	if err == nil {
		t.Fatal("fake Git stderr overflow unexpectedly succeeded")
	}
	if len(err.Error()) > maxGitStderrBytes+256 || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("stderr error was not bounded: length=%d suffix=%q", len(err.Error()), err.Error()[max(0, len(err.Error())-80):])
	}
}

func TestDeadlineRegistrationGetsFiniteDeadline(t *testing.T) {
	ctx, cancel := registrationContext(context.Background(), workspaceRegistrationTimeout)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("registration context has no service-owned deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > workspaceRegistrationTimeout {
		t.Fatalf("registration deadline remaining=%v, limit=%v", remaining, workspaceRegistrationTimeout)
	}

	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	_, service := openProjectStateService(t, "")
	service.registrationTimeout = 0
	_, err := service.RegisterWorkspace(context.Background(), RegisterWorkspaceRequest{
		Root: repository.root, ExpectedProjectID: repository.projectID,
		ExpectedRepository: repository.identity, ExpectedCommit: repository.commit,
	})
	if !errors.Is(err, ErrInvalidRegistration) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired service deadline error=%v, want invalid registration and deadline identities", err)
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
