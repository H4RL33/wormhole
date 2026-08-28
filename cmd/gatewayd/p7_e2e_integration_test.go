// Retained optional-sync subsystem and local durability acceptance tests.
package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"strings"
	stdsync "sync"
	"syscall"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	syncpkg "github.com/H4RL33/wormhole/internal/runtime/sync"
	"github.com/H4RL33/wormhole/internal/types"
	projectstate "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func gatewayQueueFixture(t *testing.T, path string) (*localstore.Store, *syncpkg.QueueRepo, types.RemoteBindingKey) {
	t.Helper()
	store, err := localstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	key := types.RemoteBindingKey{
		ProjectID: "00000000-0000-4000-8000-000000000001", WorkspaceID: "00000000-0000-4000-8000-000000000011",
		FabricInstanceID: "20000000-0000-4000-8000-000000000001",
		RemoteProjectID:  "30000000-0000-4000-8000-000000000001",
		StreamID:         "40000000-0000-4000-8000-000000000001",
	}
	repository := types.RepositoryIdentity{Provider: "github", ImmutableID: "gateway-test", CanonicalRemote: "https://example.test/gateway-test"}
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	snapshot := projectstate.Snapshot{
		Config: projectstate.ConfigV1{SnapshotVersion: 1, ProjectID: key.ProjectID,
			Handle: types.ProjectHandle{Namespace: "gateway", Name: "test"}, Repository: repository},
		Project: projectstate.ProjectV1{SchemaVersion: 1, Kind: "project", ID: key.ProjectID,
			Name: "Gateway test", Aliases: []string{}, CreatedAt: now, UpdatedAt: now, Extensions: projectstate.ExtensionsV1{}},
		Actors: map[string]projectstate.Record[projectstate.ActorV1]{}, Tasks: map[string]projectstate.Record[projectstate.TaskV1]{},
		TaskLinks: map[string]projectstate.Record[projectstate.TaskLinkV1]{}, Articles: map[string]projectstate.KBRecord{},
		Channels: map[string]projectstate.Record[projectstate.ChannelV1]{}, Events: map[string]projectstate.EventV1{},
		GitLinks: map[string]projectstate.Record[projectstate.GitLinkV1]{},
	}
	tree, err := projectstate.EncodeTree(snapshot)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	decoded, err := projectstate.DecodeTree(tree)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	workspace := types.WorkspaceBinding{
		Scope:    types.WorkspaceScope{ProjectID: key.ProjectID, WorkspaceID: key.WorkspaceID},
		Checkout: types.CheckoutIdentity{CanonicalPath: "/gateway-test", Device: 1, Inode: 11}, Repository: repository,
		AcceptedRef: "refs/heads/main", AcceptedCommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		AcceptedTreeDigest: string(decoded.Digest),
	}
	if _, _, err := localstore.NewWorkspaceRepo(store.DB()).RegisterWorkspace(context.Background(), workspace, tree); err != nil {
		store.Close()
		t.Fatal(err)
	}
	routes := localstore.NewFabricRouteRepo(store.DB())
	profile := types.FabricProfile{ProfileID: "10000000-0000-4000-8000-000000000001", Alias: "gateway-test",
		FabricInstanceID: key.FabricInstanceID, BaseURL: "https://fabric.example.test", Mode: types.FabricModePrivate,
		CredentialRef: "keyring:test"}
	if err := routes.CreateProfile(context.Background(), profile); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := routes.AttachWorkspace(context.Background(), types.FabricBinding{
		Workspace: workspace, ProfileID: profile.ProfileID, FabricInstanceID: key.FabricInstanceID,
		RemoteProjectID: key.RemoteProjectID, StreamID: key.StreamID,
		AttachmentRef: "50000000-0000-4000-8000-000000000001", CanonicalRef: workspace.AcceptedRef, Writable: true,
	}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, syncpkg.NewQueueRepo(store.DB()), key
}

func gatewayQueueOperation(sequence string) projectstate.OperationV1 {
	return projectstate.OperationV1{
		SchemaVersion: 1, ID: sequence, Kind: projectstate.OperationTombstone,
		ExpectedViewDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Actor: types.ActorEnvelope{ActorKind: types.ActorHuman,
			HumanPrincipalID: "80000000-0000-4000-8000-000000000001",
			Assurance:        types.AssuranceLocal, OccurredAt: time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)},
		Tombstone: &projectstate.TombstoneOperationV1{
			Key:                   projectstate.RecordKey{Kind: "task", ID: "70000000-0000-4000-8000-000000000001"},
			ExpectedContentDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
	}
}

func TestP7_LocalQueueDeliveryLifecycle(t *testing.T) {
	store, queue, key := gatewayQueueFixture(t, filepath.Join(t.TempDir(), "gateway.db"))
	defer store.Close()
	ctx := context.Background()
	if pending, err := queue.ListPending(ctx, key, 100); err != nil || len(pending) != 0 {
		t.Fatalf("initial pending=(%+v,%v)", pending, err)
	}
	operation := gatewayQueueOperation("90000000-0000-4000-8000-000000000070")
	entry, err := queue.Enqueue(ctx, key, operation, 1)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := queue.ListPending(ctx, key, 100)
	if err != nil || len(pending) != 1 || pending[0].Operation.ID != operation.ID {
		t.Fatalf("queued=(%+v,%v)", pending, err)
	}
	if err := queue.MarkDelivered(ctx, key, entry.ID); err != nil {
		t.Fatal(err)
	}
	if pending, err := queue.ListPending(ctx, key, 100); err != nil || len(pending) != 0 {
		t.Fatalf("delivered pending=(%+v,%v)", pending, err)
	}
}

func TestP7_LocalTaskPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	store, err := localstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	tasks := localstore.NewTaskRepo(store.DB(), localstore.NewEventRepo(store.DB()))
	task, err := tasks.CreateTask(context.Background(), "project-1", "Task title", "Task description", nil, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := localstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := localstore.NewTaskRepo(reopened.DB(), localstore.NewEventRepo(reopened.DB())).GetTask(context.Background(), "project-1", task.ID)
	if err != nil || got.Title != task.Title {
		t.Fatalf("reopened task=(%+v,%v)", got, err)
	}
}

func TestP7_SyncQueueDurability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	store, queue, key := gatewayQueueFixture(t, path)
	operation := gatewayQueueOperation("90000000-0000-4000-8000-000000000071")
	if _, err := queue.Enqueue(context.Background(), key, operation, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := localstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	pending, err := syncpkg.NewQueueRepo(reopened.DB()).ListPending(context.Background(), key, 100)
	if err != nil || len(pending) != 1 || pending[0].Operation.ID != operation.ID {
		t.Fatalf("reopened queue=(%+v,%v)", pending, err)
	}
}

var (
	task4GatewayBinOnce stdsync.Once
	task4GatewayBinPath string
	task4GatewayBinErr  error
)

func TestTask4FabricBuildUsesTestOnlyEmbedderWiring(t *testing.T) {
	got := task4FabricBuildArgs("/tmp/fabric-test")
	want := []string{"build", "-tags", "wormhole_test_embedder", "-o", "/tmp/fabric-test", "./cmd/fabric"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Fabric build args=%q, want %q", got, want)
	}
	for _, argument := range got {
		if argument == "-ldflags" || strings.Contains(argument, "cohereEmbedEndpoint") {
			t.Fatalf("Fabric build mutates production endpoint: %q", got)
		}
	}
}

func task4BuildGatewayBinary(t *testing.T) string {
	t.Helper()
	task4GatewayBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "wormhole-gatewayd-task4-*")
		if err != nil {
			task4GatewayBinErr = err
			return
		}
		task4GatewayBinPath = filepath.Join(dir, "gatewayd")
		command := exec.Command("go", "build", "-o", task4GatewayBinPath, "./cmd/gatewayd")
		command.Dir = repoRootForTest(t)
		if currentUser, err := user.Current(); err == nil && currentUser.HomeDir != "" {
			command.Env = append(os.Environ(), "HOME="+currentUser.HomeDir)
		}
		if output, err := command.CombinedOutput(); err != nil {
			task4GatewayBinErr = errors.New("build gatewayd: " + err.Error() + ": " + string(output))
		}
	})
	return task4GatewayBinPath
}

func task4FabricBuildArgs(outputPath string) []string {
	return []string{"build", "-tags", "wormhole_test_embedder", "-o", outputPath, "./cmd/fabric"}
}

type task4ProcessDaemon struct {
	command *exec.Cmd
	done    chan error
	stderr  bytes.Buffer
	stop    stdsync.Once
}

func startTask4ProcessDaemon(t *testing.T, gatewayBin, profile string, env []string, socketPath string) *task4ProcessDaemon {
	t.Helper()
	daemon := &task4ProcessDaemon{done: make(chan error, 1)}
	daemon.command = exec.Command(gatewayBin, profile)
	daemon.command.Env = env
	daemon.command.Stderr = &daemon.stderr
	if err := daemon.command.Start(); err != nil {
		t.Fatalf("start gatewayd process: %v", err)
	}
	go func() { daemon.done <- daemon.command.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Lstat(socketPath); err == nil {
			break
		}
		select {
		case err := <-daemon.done:
			t.Fatalf("gatewayd exited before socket was ready: %v stderr=%q", err, daemon.stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("gatewayd socket was not ready: stderr=%q", daemon.stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { daemon.Stop(t) })
	return daemon
}

func (daemon *task4ProcessDaemon) Stop(t *testing.T) {
	t.Helper()
	daemon.stop.Do(func() {
		_ = daemon.command.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-daemon.done:
			var exitError *exec.ExitError
			if err != nil && (!errors.As(err, &exitError) || !exitError.ProcessState.Sys().(syscall.WaitStatus).Signaled()) {
				t.Errorf("gatewayd process exit: %v stderr=%q", err, daemon.stderr.String())
			}
		case <-time.After(5 * time.Second):
			_ = daemon.command.Process.Kill()
			t.Errorf("gatewayd process did not stop: stderr=%q", daemon.stderr.String())
		}
	})
}
