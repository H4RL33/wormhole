package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	stdsync "sync"
	"syscall"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localapi"
	"github.com/H4RL33/wormhole/internal/runtime/localidentity"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/runtime/projectstate"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestGatewayWiresExactWorkspaceConflictGate(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repositories, err := newRoutedSyncRepositories(store)
	if err != nil {
		t.Fatal(err)
	}
	gate, ok := repositories.conflicts.(*localstore.WorkspaceRepo)
	if !ok || gate == nil || gate != repositories.workspaces {
		t.Fatalf("routed sync conflict gate=%T %p, workspace repo=%p", repositories.conflicts, gate, repositories.workspaces)
	}
}

func TestSupervisorDependenciesConstructLocalOnlyGraph(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	identity, err := localidentity.Open(filepath.Join(t.TempDir(), "identities"))
	if err != nil {
		t.Fatal(err)
	}
	supervisor, err := newLocalSupervisor(store, identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().PingContext(context.Background()); err != nil {
		t.Fatalf("supervisor took ownership of injected Store: %v", err)
	}
	if _, err := newLocalSupervisor(nil, identity); err == nil {
		t.Fatal("incomplete production graph accepted")
	}
}

func TestSetupPrivateRPCStaysOutsideToolsOnLocalOnlyDisabledCodeGraphSupervisor(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	identity, err := localidentity.Open(filepath.Join(t.TempDir(), "identities"))
	if err != nil {
		t.Fatal(err)
	}
	supervisor, err := newLocalSupervisor(store, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	socket := filepath.Join(t.TempDir(), "gateway.sock")
	server, err := supervisor.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(t.Context()) }()
	connection, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	reader := bufio.NewReader(connection)
	mcpInitialize(t, connection, reader)

	request, _ := json.Marshal(mcpRpcRequest{JSONRPC: "2.0", ID: json.RawMessage("2"), Method: localapi.PrivateSetupEnsureIdentityRPCMethod, Params: json.RawMessage(`{}`)})
	if _, err := connection.Write(append(request, '\n')); err != nil {
		t.Fatal(err)
	}
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response mcpRpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(line), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Message != localapi.ErrPrivateCLIAuthorization.Error() {
		t.Fatalf("private setup dispatch = %+v", response)
	}

	listRequest, _ := json.Marshal(mcpRpcRequest{JSONRPC: "2.0", ID: json.RawMessage("3"), Method: "tools/list", Params: json.RawMessage(`{}`)})
	if _, err := connection.Write(append(listRequest, '\n')); err != nil {
		t.Fatal(err)
	}
	line, err = reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(line, []byte(localapi.PrivateSetupEnsureIdentityRPCMethod)) {
		t.Fatalf("private setup method leaked into tools/list: %s", line)
	}
}

func TestRun_FreshSupervisorTruthfulInventory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runDir := filepath.Join(home, "run")
	t.Setenv("XDG_RUNTIME_DIR", runDir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, "default") }()

	socketPath := filepath.Join(runDir, "wormhole", "wormholed.sock")
	var conn net.Conn
	var err error
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.Dial("unix", socketPath)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		cancel()
		select {
		case runErr := <-errCh:
			t.Fatalf("fresh Gateway did not expose socket: dial=%v run=%v", err, runErr)
		default:
			t.Fatalf("fresh Gateway did not expose socket: %v", err)
		}
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	mcpInitialize(t, conn, reader)
	removed := mcpCallTool(t, conn, reader, 2, localapi.EnrolmentToolName, map[string]interface{}{})
	if want := "unknown tool: " + localapi.EnrolmentToolName; removed.Error != want {
		cancel()
		t.Fatalf("fresh Gateway removed enrolment error = %q, want %q", removed.Error, want)
	}
	resp := mcpCallTool(t, conn, reader, 3, "wormhole.sync.status", map[string]interface{}{})
	if !strings.Contains(resp.Error, "invalid private request context") {
		cancel()
		t.Fatalf("fresh Gateway retained sync.status error = %q, want binding-aware fail-closed error", resp.Error)
	}

	cancel()
	select {
	case runErr := <-errCh:
		if runErr != nil {
			t.Fatalf("fresh Gateway shutdown: %v", runErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fresh Gateway did not shut down")
	}
}

func TestRun_RecoversCrashLeftIdentitySessionAfterOwnerLockAndPreservesAgent(t *testing.T) {
	home, err := os.MkdirTemp("", "gw-id-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	runtimeDir := filepath.Join(home, "run")
	dataHome := filepath.Join(home, "data")
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("XDG_DATA_HOME", dataHome)
	identityRoot := filepath.Join(dataHome, "wormhole", "identities")
	identity, err := localidentity.Open(identityRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identity.EnsureSelectedForSetup(t.Context(), "00000000-0000-4000-8000-000000000031", types.ConfirmedIdentitySelection{DisplayName: "Restart Owner"}); err != nil {
		t.Fatal(err)
	}
	crashed, err := identity.OpenMCP(t.Context(), localidentity.MCPClientInfo{Name: "codex", Version: "0.150"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := identity.ResolveLocalActor(t.Context(), crashed)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, "default") }()
	socketPath := filepath.Join(runtimeDir, "wormhole", "wormholed.sock")
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case runErr := <-done:
			t.Fatalf("Gateway failed before serving: %v", runErr)
		default:
		}
		if _, err := os.Lstat(socketPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("Gateway did not serve after identity recovery")
		}
		time.Sleep(10 * time.Millisecond)
	}
	recovered, err := identity.Session(t.Context(), crashed.SessionID)
	if err != nil || recovered.EndedAt == nil {
		cancel()
		t.Fatalf("Gateway startup did not terminalize crash-left session: %+v, %v", recovered, err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Gateway shutdown: %v", err)
	}

	reopened, err := localidentity.Open(identityRoot)
	if err != nil {
		t.Fatal(err)
	}
	next, err := reopened.OpenMCP(t.Context(), localidentity.MCPClientInfo{Name: "CODEX", Version: "0.151"})
	if err != nil {
		t.Fatal(err)
	}
	after, err := reopened.ResolveLocalActor(t.Context(), next)
	if err != nil {
		t.Fatal(err)
	}
	if after.AgentID != before.AgentID || after.AccountableHumanID != before.AccountableHumanID || after.SessionID == before.SessionID {
		t.Fatalf("restart actor = %+v, before %+v", after, before)
	}
}

func TestRun_BlockedWorkspaceFailsBeforeSocketMutationAndReleasesOwnerLock(t *testing.T) {
	home, err := os.MkdirTemp("", "gw-life-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	runtimeDir := filepath.Join(home, "run")
	dataHome := filepath.Join(home, "data")
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("XDG_DATA_HOME", dataHome)
	databasePath := filepath.Join(dataHome, "wormhole", "wormholed.db")
	prepareBlockedGatewayWorkspace(t, databasePath)

	socketPath := filepath.Join(runtimeDir, "wormhole", "wormholed.sock")
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		t.Fatal(err)
	}
	stale, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if unixListener, ok := stale.(*net.UnixListener); ok {
		unixListener.SetUnlinkOnClose(false)
	}
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	socketBefore, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err = Run(ctx, "default")
	if !errors.Is(err, projectstate.ErrBranchSwitchPending) {
		t.Fatalf("blocked startup error = %v, want ErrBranchSwitchPending", err)
	}
	socketAfter, statErr := os.Lstat(socketPath)
	if statErr != nil || !os.SameFile(socketBefore, socketAfter) {
		t.Fatalf("blocked startup mutated socket before lifecycle acceptance: stat=%v same=%t", statErr, statErr == nil && os.SameFile(socketBefore, socketAfter))
	}
	owner, err := acquireDatabaseOwnerLock(databasePath)
	if err != nil {
		t.Fatalf("blocked startup retained database owner lock: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(databasePath), "identities")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("blocked startup touched identity authority before project lifecycle: %v", err)
	}
}

func TestRun_RefreshesExternalGitAdvanceBeforeMCPStatusAndDiff(t *testing.T) {
	home, err := os.MkdirTemp("", "gw-refresh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	runtimeDir := filepath.Join(home, "run")
	dataHome := filepath.Join(home, "data")
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("XDG_DATA_HOME", dataHome)
	databasePath := filepath.Join(dataHome, "wormhole", "wormholed.db")
	root, _ := prepareRegisteredGatewayWorkspace(t, databasePath)
	identity, err := localidentity.Open(filepath.Join(filepath.Dir(databasePath), "identities"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identity.EnsureSelectedForSetup(context.Background(), "aaaaaaaa-0000-4000-8000-000000000005", types.ConfirmedIdentitySelection{DisplayName: "Gateway Lifecycle"}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, "default") }()
	socketPath := filepath.Join(runtimeDir, "wormhole", "wormholed.sock")
	var connection net.Conn
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		connection, err = net.Dial("unix", socketPath)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		cancel()
		t.Fatalf("dial production Gateway: %v", err)
	}
	defer connection.Close()
	reader := bufio.NewReader(connection)
	mcpInitialize(t, connection, reader)

	gatewayGit(t, root, "commit", "--allow-empty", "-m", "test: external advance after Gateway listen")
	wantCommit := gatewayGitOutput(t, root, "rev-parse", "HEAD")
	arguments := map[string]interface{}{"_wormhole_workspace": map[string]string{"working_directory": root}}
	statusResponse := mcpCallTool(t, connection, reader, 2, "wormhole.workspace.status", arguments)
	if statusResponse.Error != "" {
		t.Fatalf("production status error: %s", statusResponse.Error)
	}
	var status localapi.WorkspaceStatusReadback
	if err := json.Unmarshal(statusResponse.Result, &status); err != nil || status.AcceptedCommitSHA != wantCommit {
		t.Fatalf("production status = (%+v, %v), want commit %s", status, err, wantCommit)
	}
	diffResponse := mcpCallTool(t, connection, reader, 3, "wormhole.workspace.diff", arguments)
	if diffResponse.Error != "" {
		t.Fatalf("production diff error: %s", diffResponse.Error)
	}
	var diff localapi.WorkspaceDiffReadback
	if err := json.Unmarshal(diffResponse.Result, &diff); err != nil || diff.BaseDigest != diff.ViewDigest || len(diff.Changes) != 0 {
		t.Fatalf("production diff = (%+v, %v), want clean", diff, err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("production Gateway shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("production Gateway did not shut down")
	}
}

func TestRun_RecoversInterruptedCheckpointBeforeIdentitySocketAndServing(t *testing.T) {
	for _, test := range []struct {
		driverState string
		wantState   string
	}{
		{driverState: "prepared", wantState: "recovered_old"},
		{driverState: "published", wantState: "recovered_new"},
	} {
		t.Run(test.driverState, func(t *testing.T) {
			home, err := os.MkdirTemp("", "gw-recover-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(home) })
			t.Setenv("HOME", home)
			runtimeDir := filepath.Join(home, "run")
			dataHome := filepath.Join(home, "data")
			t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
			t.Setenv("XDG_DATA_HOME", dataHome)
			databasePath := filepath.Join(dataHome, "wormhole", "wormholed.db")
			journalID := prepareInterruptedGatewayCheckpoint(t, databasePath, test.driverState)
			identityPath := filepath.Join(filepath.Dir(databasePath), "identities")
			socketPath := filepath.Join(runtimeDir, "wormhole", "wormholed.sock")
			if _, err := os.Stat(identityPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s fixture created identity authority: %v", test.driverState, err)
			}
			if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s fixture created socket: %v", test.driverState, err)
			}
			if err := os.WriteFile(identityPath, []byte("block identity creation\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			startupErr := Run(t.Context(), "default")
			if startupErr == nil || !strings.Contains(startupErr.Error(), "open local identity store") {
				t.Fatalf("%s blocked identity startup error = %v", test.driverState, startupErr)
			}
			verification, err := localstore.Open(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			var journalState string
			err = verification.DB().QueryRow(`SELECT state FROM workspace_materializations WHERE journal_id=?`, journalID).Scan(&journalState)
			if closeErr := verification.Close(); err == nil {
				err = closeErr
			}
			if err != nil || journalState != test.wantState {
				t.Fatalf("%s journal before identity creation = (%q, %v), want %q", test.driverState, journalState, err, test.wantState)
			}
			if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s identity-blocked startup created socket: %v", test.driverState, err)
			}
			if err := os.Remove(identityPath); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			errCh := make(chan error, 1)
			go func() { errCh <- Run(ctx, "default") }()
			var connection net.Conn
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
				connection, err = net.Dial("unix", socketPath)
				if err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err != nil {
				cancel()
				t.Fatalf("dial %s recovered Gateway: %v", test.driverState, err)
			}
			reader := bufio.NewReader(connection)
			mcpInitialize(t, connection, reader)
			if info, err := os.Stat(identityPath); err != nil || !info.IsDir() {
				connection.Close()
				cancel()
				t.Fatalf("%s identity authority after recovery = (%v, %v)", test.driverState, info, err)
			}
			if err := connection.Close(); err != nil {
				t.Fatal(err)
			}
			cancel()
			select {
			case err := <-errCh:
				if err != nil {
					t.Fatalf("%s recovered Gateway shutdown: %v", test.driverState, err)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("%s recovered Gateway did not shut down", test.driverState)
			}
		})
	}
}

func prepareInterruptedGatewayCheckpoint(t *testing.T, databasePath, driverState string) string {
	t.Helper()
	root, binding := prepareRegisteredGatewayWorkspace(t, databasePath)
	store, err := localstore.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := projectstate.NewService(localstore.NewWorkspaceRepo(store.DB()), projectstate.ServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	actor := types.ActorEnvelope{
		ActorKind: types.ActorHuman, HumanPrincipalID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		Assurance: types.AssuranceLocal, OccurredAt: time.Date(2026, 8, 27, 3, 30, 0, 0, time.UTC),
	}
	publication, err := service.PublicationConfiguration(t.Context(), binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	constraint, err := projectstate.DigestPublicationBindingConstraint(binding.Repository, publication.ObservedOriginDigest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReconfigurePublication(t.Context(), projectstate.ReconfigurePublicationRequest{
		Scope: binding.Scope, ExpectedBinding: binding, ExpectedPublicationBindingDigest: constraint,
		Expected: publication, Classification: types.PublicationLocalOnly, Actor: actor,
	}); err != nil {
		t.Fatal(err)
	}
	status, err := service.Status(t.Context(), binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	task := *status.AcceptedSnapshot.Tasks["22222222-2222-4222-8222-222222222222"].Value
	task.Description = "restart recovery " + driverState
	task.UpdatedAt = actor.OccurredAt
	if _, err := service.Apply(t.Context(), binding.Scope, state.OperationV1{
		SchemaVersion: 1, ID: "bbbbbbbb-0000-4000-8000-000000000001", Kind: state.OperationPutRecord,
		ExpectedViewDigest: status.CandidateDigest, Actor: actor,
		PutRecord: &state.PutRecordV1{Record: state.RecordValueV1{Task: &task}},
	}); err != nil {
		t.Fatal(err)
	}
	live, err := projectstate.ReadWorkingTreeNoFollow(root)
	if err != nil {
		t.Fatal(err)
	}
	liveDigest, err := state.DigestTree(live)
	if err != nil {
		t.Fatal(err)
	}
	if driverState == "prepared" {
		if err := os.Chmod(root, 0o500); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
		}()
	}
	result, checkpointErr := service.Checkpoint(t.Context(), projectstate.CheckpointRequest{
		Scope: binding.Scope, Root: root, ExpectedWorkingTreeDigest: liveDigest, Actor: actor,
	})
	if driverState == "prepared" {
		if checkpointErr == nil || result != (projectstate.CheckpointResult{}) {
			t.Fatalf("prepared checkpoint interruption = (%+v, %v)", result, checkpointErr)
		}
	} else if driverState == "published" {
		if checkpointErr != nil || result.JournalID == "" {
			t.Fatalf("published checkpoint = (%+v, %v)", result, checkpointErr)
		}
	} else {
		t.Fatalf("unknown checkpoint driver state %q", driverState)
	}
	var journalID, stateValue string
	if err := store.DB().QueryRow(`SELECT journal_id, state FROM workspace_materializations`).Scan(&journalID, &stateValue); err != nil {
		t.Fatal(err)
	}
	if stateValue != driverState {
		t.Fatalf("checkpoint fixture state = %q, want %q", stateValue, driverState)
	}
	return journalID
}

func prepareBlockedGatewayWorkspace(t *testing.T, databasePath string) string {
	t.Helper()
	root, binding := prepareRegisteredGatewayWorkspace(t, databasePath)
	store, err := localstore.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	service, err := projectstate.NewService(localstore.NewWorkspaceRepo(store.DB()), projectstate.ServiceConfig{})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	status, err := service.Status(context.Background(), binding.Scope)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	actor := types.ActorEnvelope{ActorKind: types.ActorHuman, HumanPrincipalID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Assurance: types.AssuranceLocal, OccurredAt: time.Date(2026, 8, 27, 3, 0, 0, 0, time.UTC)}
	task := *status.AcceptedSnapshot.Tasks["22222222-2222-4222-8222-222222222222"].Value
	task.Description = "pending workspace state blocks startup on a branch switch"
	task.UpdatedAt = actor.OccurredAt
	if _, err := service.Apply(context.Background(), binding.Scope, state.OperationV1{
		SchemaVersion: 1, ID: "aaaaaaaa-0000-4000-8000-000000000004", Kind: state.OperationPutRecord,
		ExpectedViewDigest: status.CandidateDigest, Actor: actor,
		PutRecord: &state.PutRecordV1{Record: state.RecordValueV1{Task: &task}},
	}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	gatewayGit(t, root, "switch", "-c", "next")
	return root
}

func prepareRegisteredGatewayWorkspace(t *testing.T, databasePath string) (string, types.WorkspaceBinding) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "checkout")
	source := filepath.Join("..", "..", "internal", "types", "projectstate", "testdata", "v1", "valid", ".wormhole")
	if err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(root, ".wormhole", relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	}); err != nil {
		t.Fatal(err)
	}
	gatewayGit(t, root, "init", "-b", "main")
	gatewayGit(t, root, "config", "user.name", "Gateway Lifecycle Fixture")
	gatewayGit(t, root, "config", "user.email", "fixture@example.test")
	gatewayGit(t, root, "add", ".wormhole")
	gatewayGit(t, root, "commit", "-m", "test: seed lifecycle fixture")

	store, err := localstore.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	service, err := projectstate.NewService(localstore.NewWorkspaceRepo(store.DB()), projectstate.ServiceConfig{})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	commit := gatewayGitOutput(t, root, "rev-parse", "HEAD")
	registered, err := service.RegisterWorkspace(context.Background(), projectstate.RegisterWorkspaceRequest{
		Root: root, ExpectedProjectID: "00000000-0000-4000-8000-000000000001",
		ExpectedRepository: types.RepositoryIdentity{Provider: "github", ImmutableID: "R_kgDOExample-1", CanonicalRemote: "https://github.com/acme/wormhole"},
		ExpectedCommit:     commit,
	})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return root, registered.Binding
}

func gatewayGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

func gatewayGitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

// Local MCP types (duplicated from internal/runtime/localapi for test use).
type mcpRpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpRpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *mcpRpcError    `json:"error,omitempty"`
}

type mcpRpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpToolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type mcpToolCallResultContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpToolCallResult struct {
	Content []mcpToolCallResultContent `json:"content"`
	IsError bool                       `json:"isError,omitempty"`
}

// mcpToolResponse mirrors the MCP response for test convenience.
type mcpToolResponse struct {
	Result json.RawMessage
	Error  string
}

type runningTestDaemon struct {
	cancel   context.CancelFunc
	errCh    chan error
	stopOnce stdsync.Once
}

func configureSecurityTestDaemon(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	runDir := filepath.Join(home, "run")
	t.Setenv("XDG_RUNTIME_DIR", runDir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	credDir := filepath.Join(home, ".wormhole", "credentials")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatalf("create credentials directory: %v", err)
	}
	credData, err := json.Marshal(map[string]string{
		"server": "http://127.0.0.1:1", "project_id": "project-1", "agent_id": "agent-1", "passport_id": "passport-1", "token": "test-token",
	})
	if err != nil {
		t.Fatalf("marshal credentials: %v", err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "default.json"), credData, 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	return filepath.Join(runDir, "wormhole", "wormholed.sock")
}

func TestRun_StalePathSocketReplaced(t *testing.T) {
	socketPath := configureSecurityTestDaemon(t)
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		t.Fatalf("create socket directory: %v", err)
	}
	addr, err := net.ResolveUnixAddr("unix", socketPath)
	if err != nil {
		t.Fatalf("resolve socket address: %v", err)
	}
	stale, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatalf("create stale socket: %v", err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatalf("close stale socket: %v", err)
	}

	runner := Run
	daemon := startTestDaemonWithRunner(t, "default", socketPath, runner)
	info, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatalf("lstat replacement socket: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("replacement path mode = %v, want socket", info.Mode())
	}
	daemon.stop(t)
}

func TestRun_StalePathRegularFileRejectedWithoutRemoval(t *testing.T) {
	socketPath := configureSecurityTestDaemon(t)
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		t.Fatalf("create socket directory: %v", err)
	}
	const contents = "do not remove"
	if err := os.WriteFile(socketPath, []byte(contents), 0o600); err != nil {
		t.Fatalf("write stale-path sentinel: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, "default") }()
	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "not a socket") {
			t.Fatalf("Run error = %v, want non-socket stale-path rejection", err)
		}
	case <-time.After(10 * time.Second):
		cancel()
		select {
		case <-errCh:
		case <-time.After(time.Second):
		}
		t.Fatal("Run did not reject a regular file at the socket path")
	}
	got, err := os.ReadFile(socketPath)
	if err != nil {
		t.Fatalf("read preserved stale-path sentinel: %v", err)
	}
	if string(got) != contents {
		t.Fatalf("stale-path sentinel = %q, want %q", got, contents)
	}
	databasePath := filepath.Join(os.Getenv("XDG_DATA_HOME"), "wormhole", "wormholed.db")
	owner, err := acquireDatabaseOwnerLock(databasePath)
	if err != nil {
		t.Fatalf("Run error retained owner lock: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := localstore.Open(databasePath)
	if err != nil {
		t.Fatalf("Run error retained or closed Store incorrectly: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveStaleSocket_ActiveDaemonPreserved(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "active.sock")
	addr, err := net.ResolveUnixAddr("unix", socketPath)
	if err != nil {
		t.Fatalf("resolve socket address: %v", err)
	}
	listener, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatalf("listen active socket: %v", err)
	}
	defer listener.Close()

	err = removeStaleSocket(socketPath)
	if err == nil || !strings.Contains(err.Error(), "active daemon") {
		t.Fatalf("removeStaleSocket error = %v, want active-daemon rejection", err)
	}
	info, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatalf("active socket was removed: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("active path mode = %v, want socket", info.Mode())
	}
	conn, err := net.DialTimeout("unix", socketPath, time.Second)
	if err != nil {
		t.Fatalf("dial preserved active socket: %v", err)
	}
	_ = conn.Close()
}

func TestRemoveStaleSocket_ReplacementAfterInitialInspectionPreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wormholed.sock")
	addr, err := net.ResolveUnixAddr("unix", path)
	if err != nil {
		t.Fatalf("resolve stale socket: %v", err)
	}
	stale, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatalf("create stale socket: %v", err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatalf("close stale socket: %v", err)
	}

	var replacement os.FileInfo
	err = removeStaleSocketWithHooks(path, staleSocketRemovalHooks{
		afterInitialInspection: func() {
			if err := os.Remove(path); err != nil {
				t.Fatalf("remove initially inspected socket: %v", err)
			}
			replacementListener, err := net.ListenUnix("unix", addr)
			if err != nil {
				t.Fatalf("create replacement socket: %v", err)
			}
			replacementListener.SetUnlinkOnClose(false)
			if err := replacementListener.Close(); err != nil {
				t.Fatalf("close replacement socket: %v", err)
			}
			replacement, err = os.Lstat(path)
			if err != nil {
				t.Fatalf("lstat replacement socket: %v", err)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "changed during stale-socket removal") {
		t.Fatalf("removeStaleSocketWithHooks error = %v, want replacement rejection", err)
	}
	preserved, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat preserved replacement: %v", err)
	}
	if !os.SameFile(replacement, preserved) {
		t.Fatal("replacement created after initial inspection was not preserved")
	}
}

func TestRemoveStaleSocket_NonSocketsPreserved(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
		mode  os.FileMode
	}{
		{
			name: "symlink",
			setup: func(t *testing.T, path string) {
				t.Helper()
				target := filepath.Join(filepath.Dir(path), "target")
				if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
					t.Fatalf("write symlink target: %v", err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatalf("create symlink: %v", err)
				}
			},
			mode: os.ModeSymlink,
		},
		{
			name: "directory",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatalf("create directory: %v", err)
				}
			},
			mode: os.ModeDir,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wormholed.sock")
			tt.setup(t, path)
			if err := removeStaleSocket(path); err == nil || !strings.Contains(err.Error(), "not a socket") {
				t.Fatalf("removeStaleSocket error = %v, want non-socket rejection", err)
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatalf("replacement was removed: %v", err)
			}
			if info.Mode()&tt.mode == 0 {
				t.Fatalf("preserved path mode = %v, want %v", info.Mode(), tt.mode)
			}
		})
	}
}

func requireOpenFileIdentity(t *testing.T, expected os.FileInfo) {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("read process descriptors: %v", err)
	}
	for _, entry := range entries {
		info, err := os.Stat(filepath.Join("/proc/self/fd", entry.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatalf("stat process descriptor %s: %v", entry.Name(), err)
		}
		if os.SameFile(expected, info) {
			return
		}
	}
	t.Fatal("checked socket inode is no longer referenced")
}

func TestRemoveStaleSocket_InodeSwapPreservesReplacement(t *testing.T) {
	tests := []struct {
		name    string
		replace func(*testing.T, string)
		assert  func(*testing.T, string)
	}{
		{
			name: "regular file",
			replace: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
					t.Fatalf("write replacement: %v", err)
				}
			},
			assert: func(t *testing.T, path string) {
				t.Helper()
				got, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read replacement: %v", err)
				}
				if string(got) != "replacement" {
					t.Fatalf("replacement contents = %q", got)
				}
			},
		},
		{
			name: "symlink",
			replace: func(t *testing.T, path string) {
				t.Helper()
				target := filepath.Join(filepath.Dir(path), "target")
				if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
					t.Fatalf("write target: %v", err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatalf("create replacement symlink: %v", err)
				}
			},
			assert: func(t *testing.T, path string) {
				t.Helper()
				info, err := os.Lstat(path)
				if err != nil {
					t.Fatalf("lstat replacement symlink: %v", err)
				}
				if info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("replacement mode = %v, want symlink", info.Mode())
				}
			},
		},
		{
			name: "unix socket",
			replace: func(t *testing.T, path string) {
				t.Helper()
				addr, err := net.ResolveUnixAddr("unix", path)
				if err != nil {
					t.Fatalf("resolve replacement socket: %v", err)
				}
				replacement, err := net.ListenUnix("unix", addr)
				if err != nil {
					t.Fatalf("create replacement socket: %v", err)
				}
				replacement.SetUnlinkOnClose(false)
				if err := replacement.Close(); err != nil {
					t.Fatalf("close replacement socket: %v", err)
				}
			},
			assert: func(t *testing.T, path string) {
				t.Helper()
				info, err := os.Lstat(path)
				if err != nil {
					t.Fatalf("lstat replacement socket: %v", err)
				}
				if info.Mode()&os.ModeSocket == 0 {
					t.Fatalf("replacement mode = %v, want socket", info.Mode())
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wormholed.sock")
			addr, err := net.ResolveUnixAddr("unix", path)
			if err != nil {
				t.Fatalf("resolve stale socket: %v", err)
			}
			stale, err := net.ListenUnix("unix", addr)
			if err != nil {
				t.Fatalf("create stale socket: %v", err)
			}
			stale.SetUnlinkOnClose(false)
			if err := stale.Close(); err != nil {
				t.Fatalf("close stale socket: %v", err)
			}
			checked, err := os.Lstat(path)
			if err != nil {
				t.Fatalf("lstat checked socket: %v", err)
			}

			err = removeStaleSocketWithHooks(path, staleSocketRemovalHooks{
				beforeQuarantine: func() {
					if err := os.Remove(path); err != nil {
						t.Fatalf("replace: remove checked socket: %v", err)
					}
					requireOpenFileIdentity(t, checked)
					tt.replace(t, path)
					replacement, err := os.Lstat(path)
					if err != nil {
						t.Fatalf("lstat replacement: %v", err)
					}
					if os.SameFile(checked, replacement) {
						t.Fatal("checked socket inode was released and reused")
					}
				},
				afterQuarantine: func(string) {
					requireOpenFileIdentity(t, checked)
				},
			})
			if err == nil || !strings.Contains(err.Error(), "changed during stale-socket removal") {
				t.Fatalf("removeStaleSocketWithHook error = %v, want inode-change rejection", err)
			}
			tt.assert(t, path)
		})
	}
}

func TestRemoveStaleSocket_PostQuarantineCollisionPreservesBothPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wormholed.sock")
	addr, err := net.ResolveUnixAddr("unix", path)
	if err != nil {
		t.Fatalf("resolve stale socket: %v", err)
	}
	stale, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatalf("create stale socket: %v", err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatalf("close stale socket: %v", err)
	}
	checked, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat checked socket: %v", err)
	}

	var quarantinePath string
	err = removeStaleSocketWithHooks(path, staleSocketRemovalHooks{
		beforeQuarantine: func() {
			if err := os.Remove(path); err != nil {
				t.Fatalf("remove checked socket: %v", err)
			}
			requireOpenFileIdentity(t, checked)
			if err := os.WriteFile(path, []byte("displaced"), 0o600); err != nil {
				t.Fatalf("write displaced replacement: %v", err)
			}
			replacement, err := os.Lstat(path)
			if err != nil {
				t.Fatalf("lstat displaced replacement: %v", err)
			}
			if os.SameFile(checked, replacement) {
				t.Fatal("checked socket inode was released and reused")
			}
		},
		afterQuarantine: func(movedPath string) {
			quarantinePath = movedPath
			requireOpenFileIdentity(t, checked)
			if err := os.WriteFile(path, []byte("newer"), 0o600); err != nil {
				t.Fatalf("write newer public path: %v", err)
			}
		},
	})
	if !errors.Is(err, syscall.EEXIST) {
		t.Fatalf("removeStaleSocketWithHooks error = %v, want EEXIST restoration collision", err)
	}
	if quarantinePath == "" || !strings.Contains(err.Error(), quarantinePath) {
		t.Fatalf("error %q does not report quarantine path %q", err, quarantinePath)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != "newer" {
		t.Fatalf("public path = %q, %v; want newer", got, readErr)
	}
	if got, readErr := os.ReadFile(quarantinePath); readErr != nil || string(got) != "displaced" {
		t.Fatalf("quarantined path = %q, %v; want displaced", got, readErr)
	}
}

func startTestDaemon(t *testing.T, profileName, socketPath string) *runningTestDaemon {
	t.Helper()
	return startTestDaemonWithRunner(t, profileName, socketPath, Run)
}

func startTestDaemonWithRunner(t *testing.T, profileName, socketPath string, runner func(context.Context, string) error) *runningTestDaemon {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d := &runningTestDaemon{cancel: cancel, errCh: make(chan error, 1)}
	go func() { d.errCh <- runner(ctx, profileName) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.Dial("unix", socketPath)
		if err == nil {
			_ = conn.Close()
			break
		}
		select {
		case runErr := <-d.errCh:
			cancel()
			t.Fatalf("Run returned before socket became ready: %v", runErr)
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("gatewayd socket did not become ready at %s", socketPath)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { d.stop(t) })
	return d
}

func (d *runningTestDaemon) stop(t *testing.T) {
	t.Helper()
	d.stopOnce.Do(func() {
		d.cancel()
		select {
		case err := <-d.errCh:
			if err != nil {
				t.Errorf("Run returned after cancellation: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Run did not stop within 5 seconds")
		}
	})
}

func waitForCondition(t *testing.T, timeout time.Duration, description string, condition func() (bool, error)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		ok, err := condition()
		if err != nil {
			t.Fatalf("wait for %s: %v", description, err)
		}
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func mcpInitialize(t *testing.T, conn net.Conn, reader *bufio.Reader) {
	t.Helper()
	req := mcpRpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "initialize", Params: json.RawMessage(`{}`)}
	reqRaw, _ := json.Marshal(req)
	if _, err := conn.Write(append(reqRaw, '\n')); err != nil {
		t.Fatalf("write initialize: %v", err)
	}
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read initialize response: %v", err)
	}
	var resp mcpRpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(line), &resp); err != nil {
		t.Fatalf("decode initialize response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("initialize error: %+v", resp.Error)
	}
	notification := mcpRpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"}
	notificationRaw, _ := json.Marshal(notification)
	if _, err := conn.Write(append(notificationRaw, '\n')); err != nil {
		t.Fatalf("write notifications/initialized: %v", err)
	}
}

func mcpCallTool(t *testing.T, conn net.Conn, reader *bufio.Reader, id int, tool string, args map[string]interface{}) mcpToolResponse {
	t.Helper()
	argsRaw := json.RawMessage(`{}`)
	if args != nil {
		argsRaw, _ = json.Marshal(args)
	}
	paramsRaw, _ := json.Marshal(mcpToolsCallParams{Name: tool, Arguments: argsRaw})
	idRaw, _ := json.Marshal(id)
	reqRaw, _ := json.Marshal(mcpRpcRequest{JSONRPC: "2.0", ID: idRaw, Method: "tools/call", Params: paramsRaw})
	if _, err := conn.Write(append(reqRaw, '\n')); err != nil {
		t.Fatalf("write tools/call: %v", err)
	}
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read tools/call response: %v", err)
	}
	var resp mcpRpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(line), &resp); err != nil {
		t.Fatalf("decode tools/call response: %v", err)
	}
	if resp.Error != nil {
		return mcpToolResponse{Error: resp.Error.Message}
	}
	var result mcpToolCallResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode tools/call result: %v", err)
	}
	if len(result.Content) == 0 {
		return mcpToolResponse{}
	}
	if result.IsError {
		return mcpToolResponse{Error: result.Content[0].Text}
	}
	return mcpToolResponse{Result: json.RawMessage(result.Content[0].Text)}
}
