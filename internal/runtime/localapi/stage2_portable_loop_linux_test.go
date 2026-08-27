//go:build linux

package localapi

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/runtime/projectstate"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

const portableLoopProjectID = "00000000-0000-4000-8000-000000000001"

func TestStage2PortableLoopAcrossTwoRealClones(t *testing.T) {
	ctx := context.Background()
	fixture := newPortableLoopGitFixture(t)
	cloneOne := fixture.clone(t, "clone-one")
	privateOne := filepath.Join(t.TempDir(), "clone-one-private.db")
	storeOne, serviceOne, bindingOne := openPortableLoopWorkspace(t, cloneOne, privateOne)
	defer storeOne.Close()

	actor := types.ActorEnvelope{
		ActorKind: types.ActorHuman, HumanPrincipalID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		Assurance: types.AssuranceLocal, OccurredAt: time.Date(2026, 8, 27, 1, 0, 0, 0, time.UTC),
	}
	serverOne := &Server{projectState: serviceOne}
	callContext := withServerOwnedActor(WithResolvedBinding(ctx, bindingOne), actor)

	imported, err := serverOne.executeWorkspaceCommand(callContext, WorkspaceCommandRequest{Operation: WorkspaceOperationImport})
	if err != nil || imported.Import == nil || imported.Import.ImportedCandidateDigest == "" {
		t.Fatalf("workspace import = (%+v, %v)", imported, err)
	}
	statusBefore, err := serviceOne.Status(ctx, bindingOne.Scope)
	if err != nil {
		t.Fatal(err)
	}
	task := *statusBefore.AcceptedSnapshot.Tasks["22222222-2222-4222-8222-222222222222"].Value
	task.Status = "done"
	task.Description = "Portable state verified through the attributed two-clone loop."
	task.UpdatedAt = time.Date(2026, 8, 27, 1, 1, 0, 0, time.UTC)
	operation := state.OperationV1{
		SchemaVersion: 1, ID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Kind: state.OperationPutRecord,
		ExpectedViewDigest: statusBefore.CandidateDigest, Actor: actor,
		PutRecord: &state.PutRecordV1{Record: state.RecordValueV1{Task: &task}},
	}
	if _, err := serviceOne.Apply(ctx, bindingOne.Scope, operation); err != nil {
		t.Fatalf("apply attributed mutation: %v", err)
	}
	var storedOperation string
	if err := storeOne.DB().QueryRowContext(ctx, `SELECT operation_json FROM workspace_overlay_operations WHERE project_id=? AND workspace_id=?`, bindingOne.Scope.ProjectID, bindingOne.Scope.WorkspaceID).Scan(&storedOperation); err != nil {
		t.Fatalf("read attributed operation: %v", err)
	}
	if !strings.Contains(storedOperation, actor.HumanPrincipalID) {
		t.Fatalf("operation does not retain server-owned human attribution: %s", storedOperation)
	}

	currentPublication, err := serviceOne.PublicationConfiguration(ctx, bindingOne.Scope)
	if err != nil {
		t.Fatal(err)
	}
	bindingDigest, err := projectstate.DigestPublicationBindingConstraint(bindingOne.Repository, currentPublication.ObservedOriginDigest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := serviceOne.ReconfigurePublication(ctx, projectstate.ReconfigurePublicationRequest{
		Scope: bindingOne.Scope, ExpectedBinding: bindingOne, ExpectedPublicationBindingDigest: bindingDigest,
		Expected: currentPublication, Classification: types.PublicationPublicGit, Actor: actor,
	}); err != nil {
		t.Fatalf("configure publication: %v", err)
	}
	diffResult, err := serverOne.executeWorkspaceCommand(callContext, WorkspaceCommandRequest{Operation: WorkspaceOperationDiff})
	if err != nil || diffResult.Diff == nil || diffResult.Diff.PublicationReviewDigest == "" || diffResult.Diff.CandidateDigest == statusBefore.AcceptedSnapshot.Digest {
		t.Fatalf("workspace diff/review = (%+v, %v)", diffResult, err)
	}

	headBefore := gitOutput(t, cloneOne, "rev-parse", "HEAD")
	remoteBefore := fixture.remoteHead(t)
	checkpointResult, err := serverOne.executeWorkspaceCommand(callContext, WorkspaceCommandRequest{
		Operation: WorkspaceOperationCheckpoint, PublicationReviewDigest: string(diffResult.Diff.PublicationReviewDigest),
	})
	if err != nil || checkpointResult.Checkpoint == nil || checkpointResult.Checkpoint.JournalID == "" {
		t.Fatalf("workspace checkpoint = (%+v, %v)", checkpointResult, err)
	}
	if got := gitOutput(t, cloneOne, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("Wormhole advanced HEAD: got %s, want %s", got, headBefore)
	}
	if got := fixture.remoteHead(t); got != remoteBefore {
		t.Fatalf("Wormhole pushed the remote: got %s, want %s", got, remoteBefore)
	}
	if err := exec.Command("git", "-C", cloneOne, "diff", "--cached", "--quiet").Run(); err != nil {
		t.Fatal("Wormhole staged checkpoint files")
	}
	porcelain := gitOutput(t, cloneOne, "status", "--porcelain")
	if porcelain == "" || strings.Contains(porcelain, ".db") || strings.Contains(porcelain, "identities") || strings.Contains(porcelain, "checkpoints") {
		t.Fatalf("checkpoint working tree = %q, want only portable unstaged state", porcelain)
	}

	// Git acceptance is deliberately performed by the fixture, outside Wormhole.
	gitRun(t, cloneOne, "config", "user.name", "Portable Loop Fixture")
	gitRun(t, cloneOne, "config", "user.email", "fixture@example.test")
	gitRun(t, cloneOne, "add", ".wormhole/state/v1")
	gitRun(t, cloneOne, "commit", "-m", "test: accept portable state")
	gitRun(t, cloneOne, "push", "origin", "HEAD:main")
	acceptedCommit := gitOutput(t, cloneOne, "rev-parse", "HEAD")
	if acceptedCommit == headBefore || fixture.remoteHead(t) != acceptedCommit {
		t.Fatalf("fixture Git publication did not advance exact remote commit")
	}

	cloneTwo := fixture.clone(t, "clone-two")
	privateTwo := filepath.Join(t.TempDir(), "clone-two-private.db")
	storeTwo, serviceTwo, bindingTwo := openPortableLoopWorkspace(t, cloneTwo, privateTwo)
	defer storeTwo.Close()
	serverTwo := &Server{projectState: serviceTwo}
	cloneTwoContext := withServerOwnedActor(WithResolvedBinding(ctx, bindingTwo), actor)
	if _, err := serverTwo.executeWorkspaceCommand(cloneTwoContext, WorkspaceCommandRequest{Operation: WorkspaceOperationImport}); err != nil {
		t.Fatalf("second clone import: %v", err)
	}
	statusTwo, err := serviceTwo.Status(ctx, bindingTwo.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if statusTwo.AcceptedSnapshot.Digest != diffResult.Diff.CandidateDigest {
		t.Fatalf("second clone accepted digest = %s, want first clone candidate %s", statusTwo.AcceptedSnapshot.Digest, diffResult.Diff.CandidateDigest)
	}
	if firstTree, secondTree := gitOutput(t, cloneOne, "rev-parse", "HEAD:.wormhole/state/v1"), gitOutput(t, cloneTwo, "rev-parse", "HEAD:.wormhole/state/v1"); firstTree != secondTree {
		t.Fatalf("tracked portable tree differs: first=%s second=%s", firstTree, secondTree)
	}
	assertFreshCloneHasNoOperationalRows(t, storeTwo)
	assertTrackedTreeHasNoPrivateState(t, cloneTwo)
}

type portableLoopGitFixture struct {
	root   string
	remote string
}

func newPortableLoopGitFixture(t *testing.T) portableLoopGitFixture {
	t.Helper()
	root := t.TempDir()
	seed := filepath.Join(root, "seed")
	if err := os.Mkdir(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	fixtureSource := filepath.Join("..", "..", "types", "projectstate", "testdata", "v1", "valid", ".wormhole")
	copyPortableLoopTree(t, fixtureSource, filepath.Join(seed, ".wormhole"))
	gitRun(t, seed, "init", "-b", "main")
	gitRun(t, seed, "config", "user.name", "Portable Loop Fixture")
	gitRun(t, seed, "config", "user.email", "fixture@example.test")
	gitRun(t, seed, "add", ".wormhole")
	gitRun(t, seed, "commit", "-m", "test: seed portable state")
	remote := filepath.Join(root, "origin.git")
	command := exec.Command("git", "clone", "--bare", seed, remote)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create bare fixture: %v: %s", err, output)
	}
	return portableLoopGitFixture{root: root, remote: remote}
}

func (f portableLoopGitFixture) clone(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join(f.root, name)
	command := exec.Command("git", "clone", "--no-local", f.remote, root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("clone %s: %v: %s", name, err, output)
	}
	return root
}

func (f portableLoopGitFixture) remoteHead(t *testing.T) string {
	t.Helper()
	command := exec.Command("git", "--git-dir", f.remote, "rev-parse", "refs/heads/main")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("read fixture remote HEAD: %v: %s", err, output)
	}
	return strings.TrimSpace(string(output))
}

func openPortableLoopWorkspace(t *testing.T, root, databasePath string) (*localstore.Store, *projectstate.Service, types.WorkspaceBinding) {
	t.Helper()
	store, err := localstore.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	service, err := projectstate.NewService(localstore.NewWorkspaceRepo(store.DB()), projectstate.ServiceConfig{})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	commit := gitOutput(t, root, "rev-parse", "HEAD")
	registered, err := service.RegisterWorkspace(context.Background(), projectstate.RegisterWorkspaceRequest{
		Root: root, ExpectedProjectID: portableLoopProjectID,
		ExpectedRepository: types.RepositoryIdentity{Provider: "github", ImmutableID: "R_kgDOExample-1", CanonicalRemote: "https://github.com/acme/wormhole"},
		ExpectedCommit:     commit,
	})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, service, registered.Binding
}

func assertFreshCloneHasNoOperationalRows(t *testing.T, store *localstore.Store) {
	t.Helper()
	for _, table := range []string{"workspace_overlay_operations", "workspace_materializations", "workspace_stashes", "workspace_conflicts", "workspace_transition_receipts", "legacy_integration_state_migrations"} {
		var count int
		if err := store.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Errorf("fresh clone %s rows = %d, error=%v; want zero", table, count, err)
		}
	}
	for _, legacy := range []string{"tasks", "channels", "events", "agents", "passports", "sync_queue", "sync_audit", "enrolment_attempts"} {
		var count int
		if err := store.DB().QueryRow("SELECT COUNT(*) FROM " + legacy).Scan(&count); err != nil || count != 0 {
			t.Errorf("fresh clone legacy table %s rows = %d, error=%v; want zero", legacy, count, err)
		}
	}
}

func assertTrackedTreeHasNoPrivateState(t *testing.T, root string) {
	t.Helper()
	tracked := strings.Fields(gitOutput(t, root, "ls-files"))
	for _, path := range tracked {
		if !strings.HasPrefix(path, ".wormhole/state/v1/") && path != ".wormhole/config.toml" && path != ".wormhole/remotes.toml" {
			t.Errorf("unexpected tracked path %q", path)
		}
		lower := strings.ToLower(path)
		for _, forbidden := range []string{".db", "credential", "identity", "checkpoint", "stash", "journal", "operation"} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("tracked private/operational path %q", path)
			}
		}
	}
}

func copyPortableLoopTree(t *testing.T, source, destination string) {
	t.Helper()
	if err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	}); err != nil {
		t.Fatalf("copy portable fixture: %v", err)
	}
}

func gitRun(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

func gitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
