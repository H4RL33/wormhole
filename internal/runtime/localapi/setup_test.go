package localapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/localidentity"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/runtime/projectstate"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestPrivateSetupEnsureIdentityRPCUsesServerOwnedStore(t *testing.T) {
	store, err := localidentity.Open(filepath.Join(t.TempDir(), "identities"))
	if err != nil {
		t.Fatal(err)
	}
	binding := privateRoutingTestBinding(t, filepath.Join(t.TempDir(), "checkout"), "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011")
	server := setupIdentityTestServer(t, store, binding)
	req := SetupIdentityRequest{
		WorkingDirectory:    binding.Checkout.CanonicalPath,
		JournalID:           "00000000-0000-4000-8000-000000000031",
		Selection:           types.ConfirmedIdentitySelection{DisplayName: "Alice Example", Email: "alice@example.test"},
		ExpectedPriorDigest: DigestSetupIdentityUnselected(),
	}
	first, err := server.PrivateSetupEnsureIdentityRPC(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := server.PrivateSetupEnsureIdentityRPC(context.Background(), req)
	if err != nil || first.HumanPrincipalID == "" || first.HumanPrincipalID != second.HumanPrincipalID {
		t.Fatalf("profiles = (%+v, %+v), err %v", first, second, err)
	}
	if _, exists := newLocalRegistry(server).Get(PrivateSetupEnsureIdentityRPCMethod); exists {
		t.Fatal("private setup RPC became a public MCP tool")
	}
}

func TestPrivateSetupEnsureIdentityRPCResolvesActorBeforeMutation(t *testing.T) {
	store, err := localidentity.Open(filepath.Join(t.TempDir(), "identities"))
	if err != nil {
		t.Fatal(err)
	}
	binding := privateRoutingTestBinding(t, filepath.Join(t.TempDir(), "checkout"), "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011")
	server := setupIdentityTestServer(t, store, binding)
	server.actorResolver = localActorResolverFunc(func(context.Context, ConnectionIdentity) (types.ActorEnvelope, error) {
		return types.ActorEnvelope{}, errors.New("actor unavailable")
	})
	_, err = server.PrivateSetupEnsureIdentityRPC(t.Context(), SetupIdentityRequest{
		WorkingDirectory:    binding.Checkout.CanonicalPath,
		JournalID:           "00000000-0000-4000-8000-000000000034",
		Selection:           types.ConfirmedIdentitySelection{DisplayName: "Alice Example"},
		ExpectedPriorDigest: DigestSetupIdentityUnselected(),
	})
	if !errors.Is(err, ErrPrivateSetupRequest) {
		t.Fatalf("identity error = %v", err)
	}
	if _, err := store.Selected(t.Context()); !errors.Is(err, localidentity.ErrNoSelectedIdentity) {
		t.Fatalf("identity mutated before actor resolution: %v", err)
	}
}

func TestPrivateSetupEnsureIdentityRPCDispatchIsNotAnMCPTool(t *testing.T) {
	store, err := localidentity.Open(filepath.Join(t.TempDir(), "identities"))
	if err != nil {
		t.Fatal(err)
	}
	binding := privateRoutingTestBinding(t, filepath.Join(t.TempDir(), "checkout"), "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011")
	server := setupIdentityTestServer(t, store, binding)
	server.registry = newLocalRegistry(server)
	client, gateway := net.Pipe()
	defer client.Close()
	defer gateway.Close()
	req := SetupIdentityRequest{WorkingDirectory: binding.Checkout.CanonicalPath, JournalID: "00000000-0000-4000-8000-000000000033", Selection: types.ConfirmedIdentitySelection{DisplayName: "Alice Example"}, ExpectedPriorDigest: DigestSetupIdentityUnselected()}
	params, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		server.dispatchMCPMessage(context.Background(), &mcpSession{initialized: true}, gateway, server.registry, rpcRequest{
			JSONRPC: "2.0", ID: json.RawMessage("1"), Method: PrivateSetupEnsureIdentityRPCMethod, Params: params,
		})
		close(done)
	}()
	line, err := bufio.NewReader(client).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	<-done
	var response rpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(line), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != nil {
		t.Fatalf("private dispatch error = %+v", response.Error)
	}
	var profile SetupIdentityReadback
	if err := json.Unmarshal(response.Result, &profile); err != nil || profile.HumanPrincipalID == "" {
		t.Fatalf("profile = %+v, err %v", profile, err)
	}
	if _, exists := server.registry.Get(PrivateSetupEnsureIdentityRPCMethod); exists {
		t.Fatal("private setup RPC became a public tool")
	}
}

func TestPrivateSetupRPCResolvesWorkspaceAndActorInsideGateway(t *testing.T) {
	identity, err := localidentity.Open(filepath.Join(t.TempDir(), "identities"))
	if err != nil {
		t.Fatal(err)
	}
	binding := privateRoutingTestBinding(t, filepath.Join(t.TempDir(), "checkout"), "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011")
	server := setupIdentityTestServer(t, identity, binding)
	workspace, err := server.PrivateSetupRegisterWorkspaceRPC(t.Context(), SetupWorkspaceRequest{
		WorkingDirectory: binding.Checkout.CanonicalPath, ExpectedProjectID: binding.Scope.ProjectID,
		ExpectedRepository: binding.Repository, ExpectedCommit: binding.AcceptedCommitSHA,
	})
	if err != nil || workspace.WorkspaceID != binding.Scope.WorkspaceID || workspace.ProjectID != binding.Scope.ProjectID {
		t.Fatalf("workspace readback = %+v, err %v", workspace, err)
	}
	forged := binding.Scope.ProjectID[:len(binding.Scope.ProjectID)-1] + "2"
	if _, err := server.PrivateSetupRegisterWorkspaceRPC(t.Context(), SetupWorkspaceRequest{
		WorkingDirectory: binding.Checkout.CanonicalPath, ExpectedProjectID: forged,
		ExpectedRepository: binding.Repository, ExpectedCommit: binding.AcceptedCommitSHA,
	}); !errors.Is(err, config.ErrConfirmedPlanDrift) {
		t.Fatalf("forged workspace authority error = %v", err)
	}
	if _, err := server.PrivateSetupEnsureIdentityRPC(t.Context(), SetupIdentityRequest{
		WorkingDirectory: filepath.Join(binding.Checkout.CanonicalPath, "forged"), JournalID: "00000000-0000-4000-8000-000000000032",
		Selection: types.ConfirmedIdentitySelection{DisplayName: "Alice Example", Email: "alice@example.test"}, ExpectedPriorDigest: DigestSetupIdentityUnselected(),
	}); !errors.Is(err, ErrPrivateSetupRequest) {
		t.Fatalf("forged identity context error = %v", err)
	}
	if _, err := identity.Selected(t.Context()); !errors.Is(err, localidentity.ErrNoSelectedIdentity) {
		t.Fatalf("identity mutated before authoritative binding: %v", err)
	}
}

func TestPrivateSetupIdentityReadbackIsBoundedPublicData(t *testing.T) {
	identity, err := localidentity.Open(filepath.Join(t.TempDir(), "identities"))
	if err != nil {
		t.Fatal(err)
	}
	binding := privateRoutingTestBinding(t, filepath.Join(t.TempDir(), "checkout"), "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011")
	server := setupIdentityTestServer(t, identity, binding)
	readback, err := server.PrivateSetupEnsureIdentityRPC(t.Context(), SetupIdentityRequest{
		WorkingDirectory: binding.Checkout.CanonicalPath, JournalID: "00000000-0000-4000-8000-000000000033",
		Selection: types.ConfirmedIdentitySelection{DisplayName: "Alice Example", Email: "alice@example.test"}, ExpectedPriorDigest: DigestSetupIdentityUnselected(),
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(readback)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 4096 || bytes.Contains(encoded, []byte("alice@example.test")) || bytes.Contains(bytes.ToLower(encoded), []byte("private")) {
		t.Fatalf("private or unbounded setup readback: %s", encoded)
	}
}

func TestPrivateSetupRPCEndToEndAcknowledgesPublicationAndImportedBase(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	projectID := "00000000-0000-4000-8000-000000000001"
	tree, treeDigest := privateRoutingWorkspaceTree(t, projectID, types.RepositoryIdentity{})
	for _, file := range tree {
		path := filepath.Join(root, ".wormhole", filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, file.Data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	setupGitRepository(t, root)
	commit := setupGitOutput(t, root, "rev-parse", "HEAD")

	store, err := localstore.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service, err := projectstate.NewService(localstore.NewWorkspaceRepo(store.DB()), projectstate.ServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := localidentity.Open(filepath.Join(t.TempDir(), "identities"))
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{projectState: service, identityStore: identity, actorResolver: identity}
	workspace, err := server.PrivateSetupRegisterWorkspaceRPC(t.Context(), SetupWorkspaceRequest{
		WorkingDirectory: root, ExpectedProjectID: projectID, ExpectedRepository: types.RepositoryIdentity{}, ExpectedCommit: commit, ExpectedPriorDigest: DigestSetupWorkspaceAbsent(),
	})
	if err != nil || workspace.AcceptedTreeDigest != treeDigest {
		t.Fatalf("register = %+v, err %v", workspace, err)
	}
	profile, err := server.PrivateSetupEnsureIdentityRPC(t.Context(), SetupIdentityRequest{
		WorkingDirectory: root, JournalID: "00000000-0000-4000-8000-000000000033", Selection: types.ConfirmedIdentitySelection{DisplayName: "Alice Example"}, ExpectedPriorDigest: DigestSetupIdentityUnselected(),
	})
	if err != nil {
		t.Fatal(err)
	}
	origin, err := projectstate.InspectPublicationOrigin(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	bindingDigest, err := projectstate.DigestPublicationBindingConstraint(types.RepositoryIdentity{}, origin)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := server.PrivateSetupPublicationRPC(t.Context(), SetupPublicationRequest{
		WorkingDirectory: root, Classification: types.PublicationLocalOnly, ExpectedBindingDigest: config.StateDigest(bindingDigest),
		ExpectedPriorDigest: DigestSetupPublicationPredicate(SetupPublicationPredicate{
			Classification: types.PublicationUnclassified, PolicyRevision: 1, ObservedOriginDigest: origin, TransitionKind: "bootstrap",
		}),
	})
	if err != nil || publication.BindingDigest != config.StateDigest(bindingDigest) || publication.ChangedByHumanID != profile.HumanPrincipalID {
		t.Fatalf("publication = %+v, err %v", publication, err)
	}
	imported, err := server.PrivateSetupImportRPC(t.Context(), SetupImportRequest{
		WorkingDirectory: root, ExpectedCommitSHA: commit, ExpectedTreeDigest: state.Digest(treeDigest),
	})
	if err != nil || imported.Conflicted || imported.ImportedCandidateDigest != state.Digest(treeDigest) || imported.AcceptedCommitSHA != commit {
		t.Fatalf("import = %+v, err %v", imported, err)
	}
	verified, err := server.PrivateSetupVerifyRPC(t.Context(), SetupWorkingDirectoryRequest{WorkingDirectory: root, Identity: types.ConfirmedIdentitySelection{DisplayName: "Alice Example"}, ExpectedTree: state.Digest(treeDigest)})
	if err != nil || verified.Workspace.WorkspaceID != workspace.WorkspaceID || verified.Identity.HumanPrincipalID != profile.HumanPrincipalID || verified.Publication.BindingDigest != config.StateDigest(bindingDigest) {
		t.Fatalf("verify = %+v, err %v", verified, err)
	}
}

func setupGitRepository(t *testing.T, root string) {
	t.Helper()
	setupGitOutput(t, root, "init", "-b", "main")
	setupGitOutput(t, root, "config", "user.name", "Setup Test")
	setupGitOutput(t, root, "config", "user.email", "setup@example.test")
	setupGitOutput(t, root, "add", ".wormhole")
	setupGitOutput(t, root, "commit", "-m", "base")
}

func setupGitOutput(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(t.TempDir(), "global.gitconfig"))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return string(bytes.TrimSpace(output))
}

func setupIdentityTestServer(t *testing.T, identity *localidentity.Store, binding types.WorkspaceBinding) *Server {
	t.Helper()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := localstore.NewWorkspaceRepo(store.DB())
	tree, digest := privateRoutingWorkspaceTree(t, binding.Scope.ProjectID, binding.Repository)
	binding.AcceptedTreeDigest = digest
	if _, _, err := repo.RegisterWorkspace(context.Background(), binding, tree); err != nil {
		t.Fatal(err)
	}
	service, err := projectstate.NewService(repo, projectstate.ServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return &Server{projectState: service, identityStore: identity, actorResolver: identity}
}

func TestProductionIdentityRootComesOnlyFromPrivateDataHomeOutsideRepositories(t *testing.T) {
	dataHome := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_DATA_HOME", dataHome)
	store, err := OpenProductionIdentityStore()
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.EnsureSelectedForSetup(context.Background(), "00000000-0000-4000-8000-000000000032", types.ConfirmedIdentitySelection{DisplayName: "Alice Example"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataHome, "wormhole", "identities", "selected.json")); err != nil {
		t.Fatalf("selected identity not under XDG data home: %v", err)
	}

	repository := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", filepath.Join(repository, "private-data"))
	if _, err := OpenProductionIdentityStore(); !errors.Is(err, ErrIdentityRootInsideRepository) {
		t.Fatalf("repository-contained identity root error = %v", err)
	}
}
