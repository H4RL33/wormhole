//go:build linux

package localapi

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
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
	cloneOneGit := capturePortableGitInvariant(t, fixture, cloneOne)
	privateOne := filepath.Join(t.TempDir(), "clone-one-private.db")
	storeOne, serviceOne, bindingOne := openPortableLoopWorkspace(t, cloneOne, privateOne)
	defer storeOne.Close()
	assertPortableGitInvariant(t, fixture, cloneOne, cloneOneGit, "workspace registration")

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
	assertPortableGitInvariant(t, fixture, cloneOne, cloneOneGit, "workspace import")
	statusBefore, err := serviceOne.Status(ctx, bindingOne.Scope)
	if err != nil {
		t.Fatal(err)
	}
	assertPortableGitInvariant(t, fixture, cloneOne, cloneOneGit, "workspace status")
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
	assertPortableGitInvariant(t, fixture, cloneOne, cloneOneGit, "attributed operation")
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
	assertPortableGitInvariant(t, fixture, cloneOne, cloneOneGit, "publication configuration read")
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
	assertPortableGitInvariant(t, fixture, cloneOne, cloneOneGit, "publication configuration")
	diffResult, err := serverOne.executeWorkspaceCommand(callContext, WorkspaceCommandRequest{Operation: WorkspaceOperationDiff})
	if err != nil || diffResult.Diff == nil || diffResult.Diff.PublicationReviewDigest == "" || diffResult.Diff.CandidateDigest == statusBefore.AcceptedSnapshot.Digest {
		t.Fatalf("workspace diff/review = (%+v, %v)", diffResult, err)
	}
	assertPortableGitInvariant(t, fixture, cloneOne, cloneOneGit, "publication review")

	for _, rejection := range []struct {
		name   string
		digest string
	}{
		{name: "missing", digest: ""},
		{name: "stale", digest: "sha256:" + strings.Repeat("0", 64)},
		{name: "mismatched candidate digest", digest: string(diffResult.Diff.CandidateDigest)},
	} {
		t.Run("reject "+rejection.name+" acknowledgement", func(t *testing.T) {
			before := workspaceMutationCounts(t, storeOne)
			if _, err := serverOne.executeWorkspaceCommand(callContext, WorkspaceCommandRequest{
				Operation: WorkspaceOperationCheckpoint, PublicationReviewDigest: rejection.digest,
			}); err == nil {
				t.Fatal("checkpoint acknowledgement error = nil")
			}
			if after := workspaceMutationCounts(t, storeOne); after != before {
				t.Fatalf("failed acknowledgement mutated journal/operations: before=%+v after=%+v", before, after)
			}
			assertPortableGitInvariant(t, fixture, cloneOne, cloneOneGit, "failed "+rejection.name+" acknowledgement")
		})
	}
	checkpointResult, err := serverOne.executeWorkspaceCommand(callContext, WorkspaceCommandRequest{
		Operation: WorkspaceOperationCheckpoint, PublicationReviewDigest: string(diffResult.Diff.PublicationReviewDigest),
	})
	if err != nil || checkpointResult.Checkpoint == nil || checkpointResult.Checkpoint.JournalID == "" {
		t.Fatalf("workspace checkpoint = (%+v, %v)", checkpointResult, err)
	}
	assertPortableGitInvariant(t, fixture, cloneOne, cloneOneGit, "successful checkpoint")
	porcelain := gitOutput(t, cloneOne, "status", "--porcelain")
	if porcelain == "" || strings.Contains(porcelain, ".db") || strings.Contains(porcelain, "identities") || strings.Contains(porcelain, "checkpoints") {
		t.Fatalf("checkpoint working tree = %q, want only portable unstaged state", porcelain)
	}

	// Git acceptance is deliberately performed by the fixture, outside Wormhole.
	gitRun(t, cloneOne, "config", "user.name", "Portable Loop Fixture")
	gitRun(t, cloneOne, "config", "user.email", "fixture@example.test")
	gitRun(t, cloneOne, "add", ".wormhole/state/v1")
	gitRun(t, cloneOne, "commit", "-m", "test: accept portable state")
	acceptedCommit := gitOutput(t, cloneOne, "rev-parse", "HEAD")
	accepted, err := serverOne.executeWorkspaceCommand(callContext, WorkspaceCommandRequest{Operation: WorkspaceOperationStatus})
	if err != nil || accepted.Status == nil || accepted.Status.AcceptedCommitSHA != acceptedCommit || accepted.Status.CandidatePresent {
		t.Fatalf("ordinary Git acceptance status = (%+v, %v), want commit %s and no candidate", accepted, err, acceptedCommit)
	}
	var acceptedJournalState string
	if err := storeOne.DB().QueryRow(`SELECT state FROM workspace_materializations WHERE journal_id=?`, checkpointResult.Checkpoint.JournalID).Scan(&acceptedJournalState); err != nil || acceptedJournalState != "accepted" {
		t.Fatalf("ordinary Git acceptance journal state = %q, err=%v; want accepted", acceptedJournalState, err)
	}
	refreshedBinding, err := serviceOne.ResolveWorkingDirectory(ctx, types.WorkspaceContext{WorkingDirectory: cloneOne})
	if err != nil {
		t.Fatal(err)
	}
	refreshedContext := withServerOwnedActor(WithResolvedBinding(ctx, refreshedBinding), actor)
	acceptedDiff, err := serverOne.executeWorkspaceCommand(refreshedContext, WorkspaceCommandRequest{Operation: WorkspaceOperationDiff})
	if err != nil || acceptedDiff.Diff == nil || acceptedDiff.Diff.BaseDigest != acceptedDiff.Diff.ViewDigest || len(acceptedDiff.Diff.Changes) != 0 {
		t.Fatalf("ordinary Git acceptance diff = (%+v, %v), want clean", acceptedDiff, err)
	}
	gitRun(t, cloneOne, "push", "origin", "HEAD:main")
	if acceptedCommit == cloneOneGit.head || fixture.remoteHead(t) != acceptedCommit {
		t.Fatalf("fixture Git publication did not advance exact remote commit")
	}

	cloneTwo := fixture.clone(t, "clone-two")
	cloneTwoGit := capturePortableGitInvariant(t, fixture, cloneTwo)
	privateTwo := filepath.Join(t.TempDir(), "clone-two-private.db")
	storeTwo, serviceTwo, bindingTwo := openPortableLoopWorkspace(t, cloneTwo, privateTwo)
	defer storeTwo.Close()
	assertPortableGitInvariant(t, fixture, cloneTwo, cloneTwoGit, "second workspace registration")
	serverTwo := &Server{projectState: serviceTwo}
	cloneTwoContext := withServerOwnedActor(WithResolvedBinding(ctx, bindingTwo), actor)
	if _, err := serverTwo.executeWorkspaceCommand(cloneTwoContext, WorkspaceCommandRequest{Operation: WorkspaceOperationImport}); err != nil {
		t.Fatalf("second clone import: %v", err)
	}
	assertPortableGitInvariant(t, fixture, cloneTwo, cloneTwoGit, "second workspace import")
	statusTwo, err := serviceTwo.Status(ctx, bindingTwo.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if statusTwo.AcceptedSnapshot.Digest != diffResult.Diff.CandidateDigest {
		t.Fatalf("second clone accepted digest = %s, want first clone candidate %s", statusTwo.AcceptedSnapshot.Digest, diffResult.Diff.CandidateDigest)
	}
	assertPortableGitInvariant(t, fixture, cloneTwo, cloneTwoGit, "second workspace status")
	if firstTree, secondTree := gitOutput(t, cloneOne, "rev-parse", "HEAD:.wormhole/state/v1"), gitOutput(t, cloneTwo, "rev-parse", "HEAD:.wormhole/state/v1"); firstTree != secondTree {
		t.Fatalf("tracked portable tree differs: first=%s second=%s", firstTree, secondTree)
	}
	assertFreshClonePrivateInventory(t, storeTwo, bindingTwo, cloneTwo)
	assertTrackedTreeHasNoPrivateState(t, cloneTwo)
}

func TestWorkspaceOperationsRefreshSameRefCommitAndRebasePendingOverlay(t *testing.T) {
	fixture := newPortableLoopGitFixture(t)
	root := fixture.clone(t, "same-ref-rebase")
	store, service, binding := openPortableLoopWorkspace(t, root, filepath.Join(t.TempDir(), "private.db"))
	defer store.Close()
	actor := portableLoopActor()
	applied := applyPortableLoopTaskMutation(t, service, binding, actor,
		"aaaaaaaa-0000-4000-8000-000000000001", "pending overlay survives an ordinary commit")

	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("ordinary developer commit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "config", "user.name", "Portable Loop Fixture")
	gitRun(t, root, "config", "user.email", "fixture@example.test")
	gitRun(t, root, "add", "README.md")
	gitRun(t, root, "commit", "-m", "test: ordinary same-ref advance")
	wantCommit := gitOutput(t, root, "rev-parse", "HEAD")

	server := &Server{projectState: service}
	callContext := withServerOwnedActor(WithResolvedBinding(context.Background(), binding), actor)
	got, err := server.executeWorkspaceCommand(callContext, WorkspaceCommandRequest{Operation: WorkspaceOperationStatus})
	if err != nil || got.Status == nil || got.Status.AcceptedCommitSHA != wantCommit ||
		!got.Status.CandidatePresent || got.Status.CandidateDigest != applied.CandidateDigest ||
		got.Status.OverlayGeneration != applied.OverlayGeneration {
		t.Fatalf("same-ref refreshed status = (%+v, %v), want commit %s and rebased proposal", got, err, wantCommit)
	}
	resolved, err := service.ResolveWorkingDirectory(context.Background(), types.WorkspaceContext{WorkingDirectory: root})
	if err != nil {
		t.Fatal(err)
	}
	diff, err := server.executeWorkspaceCommand(
		withServerOwnedActor(WithResolvedBinding(context.Background(), resolved), actor),
		WorkspaceCommandRequest{Operation: WorkspaceOperationDiff},
	)
	if err != nil || diff.Diff == nil || len(diff.Diff.Changes) == 0 || diff.Diff.CandidateDigest != applied.CandidateDigest {
		t.Fatalf("same-ref refreshed diff = (%+v, %v), want pending semantic change", diff, err)
	}
}

func TestWorkspaceOperationsBlockBranchSwitchExceptStashThenRefreshRecover(t *testing.T) {
	fixture := newPortableLoopGitFixture(t)
	root := fixture.clone(t, "branch-switch-stash")
	store, service, binding := openPortableLoopWorkspace(t, root, filepath.Join(t.TempDir(), "private.db"))
	defer store.Close()
	actor := portableLoopActor()
	applyPortableLoopTaskMutation(t, service, binding, actor,
		"aaaaaaaa-0000-4000-8000-000000000002", "stash before accepting the new branch")
	gitRun(t, root, "switch", "-c", "next")

	server := &Server{projectState: service}
	callContext := withServerOwnedActor(WithResolvedBinding(context.Background(), binding), actor)
	before := workspaceMutationCounts(t, store)
	if got, err := server.executeWorkspaceCommand(callContext, WorkspaceCommandRequest{Operation: WorkspaceOperationStatus}); !errors.Is(err, projectstate.ErrBranchSwitchPending) || got != (WorkspaceCommandResult{}) {
		t.Fatalf("branch-switch status = (%+v, %v), want ErrBranchSwitchPending", got, err)
	}
	if after := workspaceMutationCounts(t, store); after != before {
		t.Fatalf("blocked branch switch mutated workspace: before=%+v after=%+v", before, after)
	}

	stashed, err := server.executeWorkspaceCommand(callContext, WorkspaceCommandRequest{
		Operation: WorkspaceOperationStash, RequestID: "aaaaaaaa-0000-4000-8000-000000000003", Label: "switch to next",
	})
	if err != nil || stashed.Stash == nil || stashed.Stash.OperationCount != 1 {
		t.Fatalf("branch-switch stash = (%+v, %v)", stashed, err)
	}
	resolved, err := service.ResolveWorkingDirectory(context.Background(), types.WorkspaceContext{WorkingDirectory: root})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.AcceptedRef != "refs/heads/next" || resolved.AcceptedCommitSHA != gitOutput(t, root, "rev-parse", "HEAD") {
		t.Fatalf("post-stash binding = %+v, want accepted next branch", resolved)
	}
	status, err := service.Status(context.Background(), resolved.Scope)
	if err != nil || status.State != "clean" || status.CandidatePresent || status.OverlayGeneration != 0 {
		t.Fatalf("post-stash recovered status = (%+v, %v), want clean", status, err)
	}
}

func TestPrivateWorkspaceRPCRejectsInvalidCommandBeforeGitRefresh(t *testing.T) {
	fixture := newPortableLoopGitFixture(t)
	root := fixture.clone(t, "private-invalid-command")
	store, service, binding := openPortableLoopWorkspace(t, root, filepath.Join(t.TempDir(), "private.db"))
	defer store.Close()
	gitRun(t, root, "config", "user.name", "Portable Loop Fixture")
	gitRun(t, root, "config", "user.email", "fixture@example.test")
	gitRun(t, root, "commit", "--allow-empty", "-m", "test: advance before invalid private command")
	advancedCommit := gitOutput(t, root, "rev-parse", "HEAD")
	actorCalls := 0
	server := &Server{
		projectState: service,
		actorResolver: localActorResolverFunc(func(context.Context, ConnectionIdentity) (types.ActorEnvelope, error) {
			actorCalls++
			return portableLoopActor(), nil
		}),
	}

	result, err := server.PrivateWorkspaceRPC(t.Context(), PrivateWorkspaceCommandRequest{
		WorkingDirectory: root,
		Command:          WorkspaceCommandRequest{Operation: WorkspaceOperation("invalid")},
	})
	if result != (WorkspaceCommandResult{}) || !errors.Is(err, ErrWorkspaceCommand) {
		t.Fatalf("invalid private command = (%+v, %v), want ErrWorkspaceCommand", result, err)
	}
	if actorCalls != 0 {
		t.Fatalf("invalid private command resolved actor %d times, want zero", actorCalls)
	}
	persisted, err := service.ResolveWorkingDirectory(t.Context(), types.WorkspaceContext{WorkingDirectory: root})
	if err != nil {
		t.Fatal(err)
	}
	if persisted.AcceptedCommitSHA != binding.AcceptedCommitSHA || persisted.AcceptedCommitSHA == advancedCommit {
		t.Fatalf("invalid private command refreshed binding from %s to %s", binding.AcceptedCommitSHA, persisted.AcceptedCommitSHA)
	}
}

func TestScopedPillarOperationRefreshesResolvedBindingBeforeDispatch(t *testing.T) {
	fixture := newPortableLoopGitFixture(t)
	root := fixture.clone(t, "pillar-refresh")
	store, service, binding := openPortableLoopWorkspace(t, root, filepath.Join(t.TempDir(), "private.db"))
	defer store.Close()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("pillar refresh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "config", "user.name", "Portable Loop Fixture")
	gitRun(t, root, "config", "user.email", "fixture@example.test")
	gitRun(t, root, "add", "README.md")
	gitRun(t, root, "commit", "-m", "test: advance before pillar call")
	wantCommit := gitOutput(t, root, "rev-parse", "HEAD")

	server := &Server{projectState: service}
	ctx := WithResolvedBinding(context.Background(), binding)
	refreshedContext, err := server.refreshScopedToolBinding(ctx, "wormhole.channel.list")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ResolvedBinding(refreshedContext)
	if err != nil || got.AcceptedCommitSHA != wantCommit {
		t.Fatalf("pillar binding = (%+v, %v), want commit %s", got, err, wantCommit)
	}
}

func portableLoopActor() types.ActorEnvelope {
	return types.ActorEnvelope{
		ActorKind: types.ActorHuman, HumanPrincipalID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		Assurance: types.AssuranceLocal, OccurredAt: time.Date(2026, 8, 27, 2, 0, 0, 0, time.UTC),
	}
}

func applyPortableLoopTaskMutation(t *testing.T, service *projectstate.Service, binding types.WorkspaceBinding, actor types.ActorEnvelope, operationID, description string) projectstate.WorkspaceStatus {
	t.Helper()
	status, err := service.Status(context.Background(), binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	task := *status.AcceptedSnapshot.Tasks["22222222-2222-4222-8222-222222222222"].Value
	task.Description = description
	task.UpdatedAt = actor.OccurredAt
	got, err := service.Apply(context.Background(), binding.Scope, state.OperationV1{
		SchemaVersion: 1, ID: operationID, Kind: state.OperationPutRecord,
		ExpectedViewDigest: status.CandidateDigest, Actor: actor,
		PutRecord: &state.PutRecordV1{Record: state.RecordValueV1{Task: &task}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
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

type portableGitInvariant struct {
	head       string
	remoteHead string
	remotes    string
	index      string
}

func capturePortableGitInvariant(t *testing.T, fixture portableLoopGitFixture, root string) portableGitInvariant {
	t.Helper()
	return portableGitInvariant{
		head: gitOutput(t, root, "rev-parse", "HEAD"), remoteHead: fixture.remoteHead(t),
		remotes: gitOutput(t, root, "remote", "-v"), index: gitOutput(t, root, "ls-files", "--stage"),
	}
}

func assertPortableGitInvariant(t *testing.T, fixture portableLoopGitFixture, root string, want portableGitInvariant, phase string) {
	t.Helper()
	got := capturePortableGitInvariant(t, fixture, root)
	if got != want {
		t.Fatalf("Wormhole mutated Git during %s: got=%+v want=%+v", phase, got, want)
	}
	if err := exec.Command("git", "-C", root, "diff", "--cached", "--quiet").Run(); err != nil {
		t.Fatalf("Wormhole staged files during %s", phase)
	}
}

type portableMutationCounts struct{ operations, materializations, stashes, receipts int }

func workspaceMutationCounts(t *testing.T, store *localstore.Store) portableMutationCounts {
	t.Helper()
	var got portableMutationCounts
	for query, destination := range map[string]*int{
		"SELECT count(*) FROM workspace_overlay_operations":  &got.operations,
		"SELECT count(*) FROM workspace_materializations":    &got.materializations,
		"SELECT count(*) FROM workspace_stashes":             &got.stashes,
		"SELECT count(*) FROM workspace_transition_receipts": &got.receipts,
	} {
		if err := store.DB().QueryRow(query).Scan(destination); err != nil {
			t.Fatal(err)
		}
	}
	return got
}

func assertFreshClonePrivateInventory(t *testing.T, store *localstore.Store, binding types.WorkspaceBinding, root string) {
	t.Helper()
	wantTables := []string{
		"agents", "auth_scopes", "bootstrap_metadata", "channels", "enrolment_attempts", "events", "gateway_schema_migrations", "git_links",
		"integration_manifest_audit", "integration_manifest_bodies", "integration_manifest_decisions", "integration_manifest_journal", "integration_manifest_project_state", "integration_manifest_revocations",
		"kb_articles", "kb_links", "legacy_integration_state_migrations", "passports", "projects", "sync_audit", "sync_queue", "tasks", "whoami_cache",
		"workspace_bindings", "workspace_candidates", "workspace_conflicts", "workspace_materializations", "workspace_overlay_operations", "workspace_publication_policies",
		"workspace_publication_policy_history", "workspace_stashes", "workspace_transition_receipts",
	}
	rows, err := store.DB().Query(`SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var gotTables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		gotTables = append(gotTables, table)
	}
	sort.Strings(wantTables)
	if !reflect.DeepEqual(gotTables, wantTables) {
		t.Fatalf("schema-v6 private table inventory = %v, want %v", gotTables, wantTables)
	}
	wantCounts := map[string]int{
		"gateway_schema_migrations": 1, "workspace_bindings": 1, "workspace_candidates": 1,
		"workspace_publication_policies": 1, "workspace_publication_policy_history": 1,
	}
	for _, table := range wantTables {
		var count int
		if err := store.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != wantCounts[table] {
			t.Errorf("fresh clone %s rows = %d, error=%v; want %d", table, count, err, wantCounts[table])
		}
	}
	var version int
	if err := store.DB().QueryRow(`SELECT version FROM gateway_schema_migrations`).Scan(&version); err != nil || version != localstore.GatewaySchemaVersion {
		t.Fatalf("schema bootstrap row = %d, err=%v", version, err)
	}
	var projectID, workspaceID, checkoutPath, status, acceptedCommit string
	if err := store.DB().QueryRow(`SELECT project_id,workspace_id,checkout_path,status,accepted_commit FROM workspace_bindings`).Scan(&projectID, &workspaceID, &checkoutPath, &status, &acceptedCommit); err != nil {
		t.Fatal(err)
	}
	if projectID != binding.Scope.ProjectID || workspaceID != string(binding.Scope.WorkspaceID) || checkoutPath != root || status != "pending" || acceptedCommit != gitOutput(t, root, "rev-parse", "HEAD") {
		t.Fatalf("binding row = project %q workspace %q checkout %q status %q commit %q", projectID, workspaceID, checkoutPath, status, acceptedCommit)
	}
	var candidateProject, candidateWorkspace, acceptedBase, workingTree, importedBy string
	if err := store.DB().QueryRow(`SELECT project_id,workspace_id,accepted_base_digest,working_tree_digest,imported_by FROM workspace_candidates`).Scan(&candidateProject, &candidateWorkspace, &acceptedBase, &workingTree, &importedBy); err != nil {
		t.Fatal(err)
	}
	if candidateProject != projectID || candidateWorkspace != workspaceID || acceptedBase == "" || workingTree == "" || importedBy != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("candidate row = project %q workspace %q base %q tree %q imported_by %q", candidateProject, candidateWorkspace, acceptedBase, workingTree, importedBy)
	}
	var policyProject, policyWorkspace, classification, transition string
	var revision int
	var origin sql.NullString
	if err := store.DB().QueryRow(`SELECT project_id,workspace_id,classification,policy_revision,transition_kind,origin_digest FROM workspace_publication_policies`).Scan(&policyProject, &policyWorkspace, &classification, &revision, &transition, &origin); err != nil {
		t.Fatal(err)
	}
	if policyProject != projectID || policyWorkspace != workspaceID || classification != "unclassified" || revision != 1 || transition != "bootstrap" || origin.Valid {
		t.Fatalf("publication bootstrap row = project %q workspace %q classification %q revision %d transition %q origin=%v", policyProject, policyWorkspace, classification, revision, transition, origin)
	}
	var historyCount int
	if err := store.DB().QueryRow(`SELECT count(*) FROM workspace_publication_policy_history WHERE project_id=? AND workspace_id=? AND policy_revision=1 AND classification='unclassified' AND transition_kind='bootstrap' AND origin_digest IS NULL`, projectID, workspaceID).Scan(&historyCount); err != nil || historyCount != 1 {
		t.Fatalf("publication bootstrap history rows=%d err=%v", historyCount, err)
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
