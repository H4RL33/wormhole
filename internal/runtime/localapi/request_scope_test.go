package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localidentity"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/runtime/projectstate"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestResolvedBindingRequiresGatewayOwnedValidatedValue(t *testing.T) {
	binding := privateRoutingTestBinding(t, t.TempDir(), "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011")
	ctx := WithResolvedBinding(context.Background(), binding)
	got, err := ResolvedBinding(ctx)
	if err != nil || got != binding {
		t.Fatalf("ResolvedBinding = (%+v, %v), want %+v", got, err, binding)
	}
	if _, err := ResolvedBinding(context.Background()); err == nil {
		t.Fatal("missing resolved binding accepted")
	}
	if _, err := ResolvedBinding(WithResolvedBinding(context.Background(), types.WorkspaceBinding{})); err == nil {
		t.Fatal("invalid resolved binding accepted")
	}
}

func TestPrivateContextIsStrippedBeforePublicDecoding(t *testing.T) {
	binding := privateRoutingTestBinding(t, t.TempDir(), "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011")
	actor := privateRoutingTestActor("00000000-0000-4000-8000-000000000021")
	server := privateRoutingTestServer(t, actor, binding)
	raw := json.RawMessage(`{"_wormhole_workspace":{"working_directory":"` + binding.Checkout.CanonicalPath + `"},"status":"todo"}`)

	ctx, public, err := server.resolvePrivateRequest(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(public, []byte("_wormhole_workspace")) || bytes.Contains(public, []byte("working_directory")) {
		t.Fatalf("private envelope reached public decoding: %s", public)
	}
	if got, err := ResolvedBinding(ctx); err != nil || got != binding {
		t.Fatalf("binding = (%+v, %v), want %+v", got, err, binding)
	}
	if got, err := ServerOwnedActor(ctx); err != nil || got != actor {
		t.Fatalf("actor = (%+v, %v), want %+v", got, err, actor)
	}
}

func TestPrivateContextRejectsDuplicateEnvelopeBeforeResolution(t *testing.T) {
	binding := privateRoutingTestBinding(t, t.TempDir(), "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011")
	server := privateRoutingTestServer(t, privateRoutingTestActor("00000000-0000-4000-8000-000000000021"), binding)
	raw := json.RawMessage(`{"_wormhole_workspace":{"working_directory":"` + binding.Checkout.CanonicalPath + `"},"_wormhole_workspace":{"working_directory":"` + binding.Checkout.CanonicalPath + `"}}`)
	if _, _, err := server.resolvePrivateRequest(context.Background(), raw); !errors.Is(err, ErrPrivateRequestContext) {
		t.Fatalf("duplicate private envelope error = %v", err)
	}
}

func TestPrivateContextConfiguresExactProjectStateAndIdentityOnce(t *testing.T) {
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
	server := &Server{}
	if err := server.ConfigurePrivateRuntime(service, identity); err != nil {
		t.Fatal(err)
	}
	if server.projectState != service || server.identityStore != identity || server.actorResolver != identity {
		t.Fatal("private runtime did not retain the exact supplied authorities")
	}
	if err := server.ConfigurePrivateRuntime(service, identity); !errors.Is(err, ErrPrivateRuntimeAlreadyConfigured) {
		t.Fatalf("replacement error = %v", err)
	}
	if err := (&Server{}).ConfigurePrivateRuntime(nil, identity); err == nil {
		t.Fatal("incomplete private runtime accepted")
	}
}

func TestForgedRoutingAndActorClaimsRejectedRegistryWide(t *testing.T) {
	binding := privateRoutingTestBinding(t, t.TempDir(), "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011")
	server := privateRoutingTestServer(t, privateRoutingTestActor("00000000-0000-4000-8000-000000000021"), binding)
	registry := newLocalRegistry(server)
	for _, tool := range registry.List() {
		for _, field := range []string{"workspace_id", "binding", "working_directory", "actor", "assurance", "session_id", "accountable_human_id"} {
			t.Run(tool.Name+"/"+field, func(t *testing.T) {
				args := map[string]any{
					"_wormhole_workspace": map[string]any{"working_directory": binding.Checkout.CanonicalPath},
					field:                 "forged",
				}
				raw, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := server.resolvePrivateRequest(context.Background(), raw); !errors.Is(err, ErrPrivateAuthorityClaim) {
					t.Fatalf("forged %s accepted for %s: %v", field, tool.Name, err)
				}
			})
		}
	}
}

func TestForgedProjectClaimIsOverwrittenByResolvedBinding(t *testing.T) {
	binding := privateRoutingTestBinding(t, t.TempDir(), "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011")
	server := privateRoutingTestServer(t, privateRoutingTestActor("00000000-0000-4000-8000-000000000021"), binding)
	raw := json.RawMessage(`{"_wormhole_workspace":{"working_directory":"` + binding.Checkout.CanonicalPath + `"},"project_id":"00000000-0000-4000-8000-000000000099"}`)
	ctx, public, err := server.resolvePrivateRequest(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := bindResolvedProjectArguments(ctx, public)
	if err != nil {
		t.Fatal(err)
	}
	var arguments map[string]string
	if err := json.Unmarshal(bound, &arguments); err != nil {
		t.Fatal(err)
	}
	if arguments["project_id"] != binding.Scope.ProjectID {
		t.Fatalf("bound project = %q, want %q", arguments["project_id"], binding.Scope.ProjectID)
	}
}

func TestForgedAuthorityFieldsAreAbsentFromEveryPublicToolSchema(t *testing.T) {
	server := &Server{}
	for _, tool := range newLocalRegistry(server).List() {
		encoded, err := json.Marshal(buildInputSchema(tool))
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"workspace_id", "namespace", "binding", "working_directory", "actor_kind", "assurance", "session_id", "accountable_human_id", "fabric_instance_id"} {
			if bytes.Contains(encoded, []byte(`"`+forbidden+`"`)) {
				t.Fatalf("%s public schema exposes private authority %s: %s", tool.Name, forbidden, encoded)
			}
		}
	}
}

func TestCrossWorkspacePrivateContextResolvesSiblingExactly(t *testing.T) {
	root := t.TempDir()
	first := privateRoutingTestBinding(t, filepath.Join(root, "a"), "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011")
	second := privateRoutingTestBinding(t, filepath.Join(root, "b"), "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000012")
	server := privateRoutingTestServer(t, privateRoutingTestActor("00000000-0000-4000-8000-000000000021"), first, second)

	for _, want := range []types.WorkspaceBinding{first, second} {
		raw := json.RawMessage(`{"_wormhole_workspace":{"working_directory":"` + want.Checkout.CanonicalPath + `"}}`)
		ctx, _, err := server.resolvePrivateRequest(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ResolvedBinding(ctx)
		if err != nil || got.Scope != want.Scope {
			t.Fatalf("cwd %q resolved %+v, want %+v (err %v)", want.Checkout.CanonicalPath, got.Scope, want.Scope, err)
		}
	}
}

func privateRoutingTestServer(t *testing.T, actor types.ActorEnvelope, bindings ...types.WorkspaceBinding) *Server {
	t.Helper()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "routing.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := localstore.NewWorkspaceRepo(store.DB())
	for index := range bindings {
		tree, digest := privateRoutingWorkspaceTree(t, bindings[index].Scope.ProjectID, bindings[index].Repository)
		bindings[index].AcceptedTreeDigest = digest
		if _, _, err := repo.RegisterWorkspace(context.Background(), bindings[index], tree); err != nil {
			t.Fatalf("register routing workspace: %v", err)
		}
	}
	service, err := projectstate.NewService(repo, projectstate.ServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return &Server{
		projectState: service,
		actorResolver: localActorResolverFunc(func(_ context.Context, connection ConnectionIdentity) (types.ActorEnvelope, error) {
			actor.OccurredAt = connection.OccurredAt
			return actor, nil
		}),
		clock: func() time.Time { return time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC) },
	}
}

func privateRoutingWorkspaceTree(t *testing.T, projectID string, repository types.RepositoryIdentity) (state.Tree, string) {
	t.Helper()
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
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
	decoded, err := state.DecodeTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	return tree, string(decoded.Digest)
}

type localActorResolverFunc func(context.Context, ConnectionIdentity) (types.ActorEnvelope, error)

func (f localActorResolverFunc) ResolveLocalActor(ctx context.Context, identity ConnectionIdentity) (types.ActorEnvelope, error) {
	return f(ctx, identity)
}

func privateRoutingTestActor(id string) types.ActorEnvelope {
	return types.ActorEnvelope{ActorKind: types.ActorHuman, HumanPrincipalID: id, Assurance: types.AssuranceLocal, OccurredAt: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)}
}

func privateRoutingTestBinding(t *testing.T, root, projectID, workspaceID string) types.WorkspaceBinding {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	tree, digest := privateRoutingWorkspaceTree(t, projectID, types.RepositoryIdentity{})
	for _, file := range tree {
		path := filepath.Join(root, ".wormhole", filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, file.Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	privateRoutingGit(t, root, "init", "-b", "main")
	privateRoutingGit(t, root, "config", "user.name", "Private Routing Fixture")
	privateRoutingGit(t, root, "config", "user.email", "fixture@example.test")
	privateRoutingGit(t, root, "add", ".wormhole")
	privateRoutingGit(t, root, "commit", "-m", "test: seed private routing workspace")
	info, _ := os.Stat(root)
	stat := info.Sys().(*syscall.Stat_t)
	return types.WorkspaceBinding{
		Scope:              types.WorkspaceScope{ProjectID: projectID, WorkspaceID: types.WorkspaceID(workspaceID)},
		Checkout:           types.CheckoutIdentity{CanonicalPath: root, Device: uint64(stat.Dev), Inode: uint64(stat.Ino)},
		AcceptedRef:        "refs/heads/main",
		AcceptedCommitSHA:  privateRoutingGitOutput(t, root, "rev-parse", "HEAD"),
		AcceptedTreeDigest: digest,
	}
}

func privateRoutingGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

func privateRoutingGitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

var _ LocalActorResolver = (*localidentity.Store)(nil)
