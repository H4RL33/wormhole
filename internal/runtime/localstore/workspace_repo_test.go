package localstore

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestWorkspaceScopeMismatchIsRejected(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	a := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	b := createBinding(t, repo, "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	appendOperation(t, store, a, 1)
	_, err := repo.ListWorkspaceOperations(context.Background(), a.Scope.ProjectID, b.Scope.WorkspaceID, 0)
	requireNotFound(t, err)
}

func TestValidWorkspacesRemainIsolated(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	a := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	b := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	appendOperation(t, store, a, 1)
	got, err := repo.ListWorkspaceOperations(context.Background(), b.Scope.ProjectID, b.Scope.WorkspaceID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("workspace B operations=%+v, want none", got)
	}
}

func TestWorkspaceRegistrationIsIdempotent(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	binding := workspaceBinding("00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	tree := workspaceTree(t, binding.Scope.ProjectID, binding.Repository)
	binding = bindingWithTreeDigest(t, binding, tree)
	first, created, err := repo.RegisterWorkspace(context.Background(), binding, tree)
	if err != nil || !created {
		t.Fatalf("first registration created=%v err=%v", created, err)
	}
	second, created, err := repo.RegisterWorkspace(context.Background(), binding, tree)
	if err != nil || created {
		t.Fatalf("repeat registration created=%v err=%v", created, err)
	}
	if second != first {
		t.Fatalf("repeat binding=%+v, want %+v", second, first)
	}
}

func TestWorkspaceRegistrationCheckoutCollision(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	first := workspaceBinding("00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	firstTree := workspaceTree(t, first.Scope.ProjectID, first.Repository)
	first = bindingWithTreeDigest(t, first, firstTree)
	if _, _, err := repo.RegisterWorkspace(context.Background(), first, firstTree); err != nil {
		t.Fatal(err)
	}
	second := workspaceBinding("00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000012", "/checkout-a", 1, 11)
	secondTree := workspaceTree(t, second.Scope.ProjectID, second.Repository)
	second = bindingWithTreeDigest(t, second, secondTree)
	_, _, err := repo.RegisterWorkspace(context.Background(), second, secondTree)
	if !errors.Is(err, ErrCheckoutCollision) {
		t.Fatalf("collision error=%v, want ErrCheckoutCollision", err)
	}
}

func TestWorkspaceTreeCodecRoundTrip(t *testing.T) {
	tree := workspaceTree(t, "00000000-0000-4000-8000-000000000001", types.RepositoryIdentity{})
	encoded, err := encodeFileList(tree)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeFileList(encoded)
	if err != nil {
		t.Fatal(err)
	}
	want, err := state.DecodeTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	got, err := state.DecodeTree(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if got.Digest != want.Digest {
		t.Fatalf("round-trip digest=%s, want %s", got.Digest, want.Digest)
	}
	reencoded, err := encodeFileList(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if string(reencoded) != string(encoded) {
		t.Fatal("file-list codec is not deterministic")
	}
}

func TestWorkspaceRepoResolveWorkingDirectoryAndStableList(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	outer := createBinding(t, repo, "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000021", "/checkout", 1, 11)
	inner := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout/nested", 2, 12)
	got, err := repo.ResolveWorkingDirectory(context.Background(), types.WorkspaceContext{WorkingDirectory: "/checkout/nested/child"})
	if err != nil {
		t.Fatal(err)
	}
	if got != inner {
		t.Fatalf("resolved=%+v, want inner %+v", got, inner)
	}
	if _, err := repo.ResolveWorkingDirectory(context.Background(), types.WorkspaceContext{WorkingDirectory: "/checkout-sibling"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("sibling resolution error=%v, want ErrNotFound", err)
	}
	bindings, err := repo.RegisteredWorkspaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 2 || bindings[0] != inner || bindings[1] != outer {
		t.Fatalf("stable bindings=%+v", bindings)
	}
}

func TestWorkspaceOperationGenerationAndIsolation(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	for index, operationID := range []string{
		"00000000-0000-4000-8000-000000000091",
		"00000000-0000-4000-8000-000000000092",
	} {
		got, err := repo.AppendWorkspaceOperation(context.Background(), binding.Scope, validWorkspaceOperation(operationID))
		if err != nil {
			t.Fatal(err)
		}
		if got.Generation != int64(index+1) || got.OperationID != operationID || got.State != "active" {
			t.Fatalf("operation=%+v", got)
		}
	}
	operations, err := repo.ListWorkspaceOperations(context.Background(), binding.Scope.ProjectID, binding.Scope.WorkspaceID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 || operations[0].Generation != 2 {
		t.Fatalf("operations after generation 1=%+v", operations)
	}
	wrong := binding.Scope
	wrong.ProjectID = "00000000-0000-4000-8000-000000000002"
	if _, err := repo.AppendWorkspaceOperation(context.Background(), wrong, validWorkspaceOperation("00000000-0000-4000-8000-000000000093")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong-scope append error=%v, want ErrNotFound", err)
	}
}

func TestWorkspaceTreeCodecRejectsMalformedInput(t *testing.T) {
	for _, encoded := range [][]byte{
		nil,
		append([]byte{0, 0, 0, 0, 0, 0, 0, 1}, []byte{0, 0, 0, 0, 0, 0, 0, 9}...),
	} {
		if _, err := decodeFileList(encoded); err == nil {
			t.Fatalf("decodeFileList(%x) succeeded", encoded)
		}
	}
	invalid := state.Tree{{Path: "../escape", Data: []byte("x")}}
	if _, err := encodeFileList(invalid); err == nil {
		t.Fatal("encodeFileList accepted a traversal path")
	}
}

func openWorkspaceStore(t *testing.T) (*Store, *WorkspaceRepo) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, NewWorkspaceRepo(store.DB())
}

func createBinding(t *testing.T, repo *WorkspaceRepo, projectID, workspaceID, path string, device, inode uint64) types.WorkspaceBinding {
	t.Helper()
	binding := workspaceBinding(projectID, workspaceID, path, device, inode)
	tree := workspaceTree(t, projectID, binding.Repository)
	binding = bindingWithTreeDigest(t, binding, tree)
	created, ok, err := repo.RegisterWorkspace(context.Background(), binding, tree)
	if err != nil || !ok {
		t.Fatalf("RegisterWorkspace: created=%v binding=%+v err=%v", ok, created, err)
	}
	return created
}

func bindingWithTreeDigest(t *testing.T, binding types.WorkspaceBinding, tree state.Tree) types.WorkspaceBinding {
	t.Helper()
	snapshot, err := state.DecodeTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	binding.AcceptedTreeDigest = string(snapshot.Digest)
	return binding
}

func workspaceBinding(projectID, workspaceID, path string, device, inode uint64) types.WorkspaceBinding {
	if path[0] != filepath.Separator {
		path = string(filepath.Separator) + path
	}
	return types.WorkspaceBinding{
		Scope:       types.WorkspaceScope{ProjectID: projectID, WorkspaceID: types.WorkspaceID(workspaceID)},
		Checkout:    types.CheckoutIdentity{CanonicalPath: filepath.Clean(path), Device: device, Inode: inode},
		Repository:  types.RepositoryIdentity{},
		AcceptedRef: "refs/heads/main", AcceptedCommitSHA: strings.Repeat("a", 40),
		AcceptedTreeDigest: "sha256:" + strings.Repeat("b", 64),
	}
}

func workspaceTree(t *testing.T, projectID string, repository types.RepositoryIdentity) state.Tree {
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
		t.Fatalf("EncodeTree: %v", err)
	}
	return tree
}

func appendOperation(t *testing.T, store *Store, binding types.WorkspaceBinding, generation int64) {
	t.Helper()
	operation := validWorkspaceOperation("00000000-0000-4000-8000-000000000099")
	operationJSON, err := state.CanonicalOperation(operation)
	if err != nil {
		t.Fatalf("canonical operation: %v", err)
	}
	_, err = store.DB().Exec(`
		INSERT INTO workspace_overlay_operations
		(project_id, workspace_id, generation, operation_id, operation_json, state)
		VALUES (?, ?, ?, ?, ?, 'active')
	`, binding.Scope.ProjectID, binding.Scope.WorkspaceID, generation,
		operation.ID, operationJSON)
	if err != nil {
		t.Fatalf("append operation: %v", err)
	}
}

func validWorkspaceOperation(operationID string) state.OperationV1 {
	const (
		humanID = "00000000-0000-4000-8000-000000000061"
		actorID = "00000000-0000-4000-8000-000000000062"
	)
	now := time.Date(2026, 7, 28, 13, 0, 0, 0, time.UTC)
	actor := state.ActorV1{
		SchemaVersion: 1, Kind: "actor", ID: actorID, ActorKind: types.ActorHuman,
		DisplayName: "Workspace Fixture", PublicKeys: []state.PublicKeyV1{}, Extensions: state.ExtensionsV1{},
	}
	return state.OperationV1{
		SchemaVersion: 1, ID: operationID, Kind: state.OperationPutRecord,
		ExpectedViewDigest: state.Digest("sha256:" + strings.Repeat("a", 64)),
		Actor: types.ActorEnvelope{
			ActorKind: types.ActorHuman, HumanPrincipalID: humanID,
			Assurance: types.AssuranceLocal, OccurredAt: now,
		},
		PutRecord: &state.PutRecordV1{Record: state.RecordValueV1{Actor: &actor}},
	}
}

func requireNotFound(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error=%v, want ErrNotFound", err)
	}
}
