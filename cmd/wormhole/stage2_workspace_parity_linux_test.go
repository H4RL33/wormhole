//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localapi"
	"github.com/H4RL33/wormhole/internal/runtime/localidentity"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/runtime/projectstate"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

const stage2ParityProjectID = "00000000-0000-4000-8000-000000000001"

func TestStage2WorkspaceCLIAndStdioMCPParity(t *testing.T) {
	for _, operation := range []localapi.WorkspaceOperation{
		localapi.WorkspaceOperationStatus,
		localapi.WorkspaceOperationDiff,
		localapi.WorkspaceOperationImport,
		localapi.WorkspaceOperationCheckpoint,
		localapi.WorkspaceOperationStash,
	} {
		t.Run(string(operation), func(t *testing.T) {
			fixture := newStage2ParityGitFixture(t)
			cli := newStage2ParityRuntime(t, fixture, "cli", operation == localapi.WorkspaceOperationCheckpoint, operation == localapi.WorkspaceOperationCheckpoint || operation == localapi.WorkspaceOperationStash)
			mcp := newStage2ParityRuntime(t, fixture, "mcp", operation == localapi.WorkspaceOperationCheckpoint, operation == localapi.WorkspaceOperationCheckpoint || operation == localapi.WorkspaceOperationStash)
			requestCLI := stage2ParityRequest(t, cli, operation, stage2ParityCLI)
			requestMCP := stage2ParityRequest(t, mcp, operation, stage2ParityMCP)
			gotCLI, err := executeStage2CLI(cli, requestCLI)
			if err != nil {
				t.Fatalf("CLI %s: %v", operation, err)
			}
			gotMCP, err := executeStage2MCP(mcp, requestMCP, nil)
			if err != nil {
				t.Fatalf("MCP %s: %v", operation, err)
			}
			normalizeStage2ParityResult(&gotCLI)
			normalizeStage2ParityResult(&gotMCP)
			if !reflect.DeepEqual(gotCLI, gotMCP) {
				t.Fatalf("%s semantic parity:\nCLI %+v\nMCP %+v", operation, gotCLI, gotMCP)
			}
		})
	}
}

func TestStage2PublicGitAcknowledgementFailsBeforeMutationOnCLIAndMCP(t *testing.T) {
	for _, surface := range []stage2ParitySurface{stage2ParityCLI, stage2ParityMCP} {
		for _, acknowledgement := range []struct {
			name  string
			value func(localapi.WorkspaceDiffReadback) string
		}{
			{name: "missing", value: func(localapi.WorkspaceDiffReadback) string { return "" }},
			{name: "stale", value: func(localapi.WorkspaceDiffReadback) string { return "sha256:" + strings.Repeat("0", 64) }},
			{name: "mismatched", value: func(diff localapi.WorkspaceDiffReadback) string { return string(diff.CandidateDigest) }},
		} {
			t.Run(string(surface)+"/"+acknowledgement.name, func(t *testing.T) {
				fixture := newStage2ParityGitFixture(t)
				runtime := newStage2ParityRuntime(t, fixture, string(surface), true, true)
				if _, err := executeStage2Surface(runtime, surface, localapi.WorkspaceCommandRequest{Operation: localapi.WorkspaceOperationImport}); err != nil {
					t.Fatalf("prepare import: %v", err)
				}
				diffResult, err := executeStage2Surface(runtime, surface, localapi.WorkspaceCommandRequest{Operation: localapi.WorkspaceOperationDiff})
				if err != nil || diffResult.Diff == nil {
					t.Fatalf("prepare diff: %+v, %v", diffResult, err)
				}
				journalBefore := stage2MaterializationCount(t, runtime.store)
				treeBefore := stage2PortableDigest(t, runtime.root)
				_, err = executeStage2Surface(runtime, surface, localapi.WorkspaceCommandRequest{
					Operation:               localapi.WorkspaceOperationCheckpoint,
					PublicationReviewDigest: acknowledgement.value(*diffResult.Diff),
				})
				if err == nil {
					t.Fatal("invalid public_git acknowledgement succeeded")
				}
				if got := stage2MaterializationCount(t, runtime.store); got != journalBefore {
					t.Fatalf("failed acknowledgement journal rows = %d, want %d", got, journalBefore)
				}
				if got := stage2PortableDigest(t, runtime.root); got != treeBefore {
					t.Fatalf("failed acknowledgement filesystem digest = %s, want %s", got, treeBefore)
				}
			})
		}
	}
}

func TestStage2BridgeRejectsForgedAuthorityAndPrivateRPCRejectsForgedBinding(t *testing.T) {
	fixture := newStage2ParityGitFixture(t)
	runtime := newStage2ParityRuntime(t, fixture, "forgery", false, false)
	journalBefore := stage2MaterializationCount(t, runtime.store)
	if _, err := executeStage2MCP(runtime, localapi.WorkspaceCommandRequest{Operation: localapi.WorkspaceOperationStatus}, map[string]any{
		"actor": map[string]any{"human_principal_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
	}); err == nil || !strings.Contains(err.Error(), "authority") {
		t.Fatalf("forged MCP actor error = %v", err)
	}
	var ignored localapi.WorkspaceCommandResult
	forgedRoot := filepath.Join(runtime.root, "forged")
	previousCapability := readGatewayCLICapability
	readGatewayCLICapability = func(context.Context) (string, error) { return runtime.capability, nil }
	err := callGatewayPrivateMethod(context.Background(), runtime.socket, localapi.PrivateWorkspaceRPCMethod, localapi.PrivateWorkspaceCommandRequest{
		WorkingDirectory: forgedRoot, Command: localapi.WorkspaceCommandRequest{Operation: localapi.WorkspaceOperationStatus},
	}, &ignored)
	readGatewayCLICapability = previousCapability
	if err == nil {
		t.Fatal("forged private binding succeeded")
	}
	if got := stage2MaterializationCount(t, runtime.store); got != journalBefore {
		t.Fatalf("forged requests mutated journal rows: got %d want %d", got, journalBefore)
	}
}

func TestStage2CLIPrivateRPCStashesPendingBranchSwitch(t *testing.T) {
	fixture := newStage2ParityGitFixture(t)
	runtime := newStage2ParityRuntime(t, fixture, "cli-branch-switch", false, true)
	if _, err := executeStage2CLI(runtime, localapi.WorkspaceCommandRequest{Operation: localapi.WorkspaceOperationImport}); err != nil {
		t.Fatalf("prepare CLI proposal: %v", err)
	}
	stage2Git(t, runtime.root, "switch", "-c", "next")

	result, err := executeStage2CLI(runtime, localapi.WorkspaceCommandRequest{
		Operation: localapi.WorkspaceOperationStash,
		RequestID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		Label:     "switch through private RPC",
	})
	if err != nil || result.Stash == nil || result.Stash.StashID == "" || result.Stash.CandidateDigest == "" {
		t.Fatalf("CLI branch-switch stash = (%+v, %v)", result, err)
	}
	binding, err := runtime.service.ResolveWorkingDirectory(t.Context(), types.WorkspaceContext{WorkingDirectory: runtime.root})
	if err != nil || binding.AcceptedRef != "refs/heads/next" {
		t.Fatalf("CLI post-stash binding = (%+v, %v), want next branch", binding, err)
	}
	status, err := runtime.service.Status(t.Context(), binding.Scope)
	if err != nil || status.State != "clean" || status.CandidatePresent || status.OverlayGeneration != 0 {
		t.Fatalf("CLI post-stash status = (%+v, %v), want clean", status, err)
	}
}

func TestStage2StdioMCPPersistsAgentProvenanceAndCLIStaysHuman(t *testing.T) {
	fixture := newStage2ParityGitFixture(t)
	runtime := newStage2ParityRuntime(t, fixture, "actor-attribution", false, true)

	if _, err := executeStage2MCPClient(runtime, localapi.WorkspaceCommandRequest{Operation: localapi.WorkspaceOperationImport}, nil, map[string]any{
		"name": "codex", "version": "0.150", "modelName": "gpt", "modelVersion": "5.6",
	}); err != nil {
		t.Fatalf("first Codex import: %v", err)
	}
	if _, err := executeStage2MCPClient(runtime, localapi.WorkspaceCommandRequest{Operation: localapi.WorkspaceOperationStash, RequestID: "10000000-0000-4000-8000-000000000001", Label: "actor-1"}, nil, map[string]any{
		"name": "codex", "version": "0.150", "modelName": "gpt", "modelVersion": "5.6",
	}); err != nil {
		t.Fatalf("first Codex stash: %v", err)
	}
	stage2ReplaceTaskStatus(t, runtime.root, "done", "blocked")
	if _, err := executeStage2MCPClient(runtime, localapi.WorkspaceCommandRequest{Operation: localapi.WorkspaceOperationImport}, nil, map[string]any{"name": "CODEX", "version": "0.151"}); err != nil {
		t.Fatalf("second Codex import: %v", err)
	}
	if _, err := executeStage2MCPClient(runtime, localapi.WorkspaceCommandRequest{Operation: localapi.WorkspaceOperationStash, RequestID: "10000000-0000-4000-8000-000000000002", Label: "actor-2"}, nil, map[string]any{"name": "CODEX", "version": "0.151"}); err != nil {
		t.Fatalf("second Codex stash: %v", err)
	}
	stage2ReplaceTaskStatus(t, runtime.root, "blocked", "todo")
	if _, err := executeStage2MCPClient(runtime, localapi.WorkspaceCommandRequest{Operation: localapi.WorkspaceOperationImport}, nil, map[string]any{"name": "claude-code", "version": "2.1"}); err != nil {
		t.Fatalf("Claude import: %v", err)
	}
	if _, err := executeStage2MCPClient(runtime, localapi.WorkspaceCommandRequest{Operation: localapi.WorkspaceOperationStash, RequestID: "10000000-0000-4000-8000-000000000003", Label: "actor-3"}, nil, map[string]any{"name": "claude-code", "version": "2.1"}); err != nil {
		t.Fatalf("Claude stash: %v", err)
	}
	stage2ReplaceTaskStatus(t, runtime.root, "todo", "wip")
	if _, err := executeStage2CLI(runtime, localapi.WorkspaceCommandRequest{Operation: localapi.WorkspaceOperationImport}); err != nil {
		t.Fatalf("CLI import: %v", err)
	}
	if _, err := executeStage2CLI(runtime, localapi.WorkspaceCommandRequest{Operation: localapi.WorkspaceOperationStash, RequestID: "10000000-0000-4000-8000-000000000004", Label: "actor-4"}); err != nil {
		t.Fatalf("CLI stash: %v", err)
	}

	actors := stage2PersistedStashActors(t, runtime.store)
	if len(actors) != 4 {
		t.Fatalf("persisted operation actors = %d, want 4: %+v", len(actors), actors)
	}
	first, second, third, human := actors[0], actors[1], actors[2], actors[3]
	if first.ActorKind != types.ActorAgent || first.AgentID == "" || first.AgentID != second.AgentID || first.SessionID == second.SessionID || third.ActorKind != types.ActorAgent || third.AgentID == first.AgentID {
		t.Fatalf("persisted harness actors = first %+v, second %+v, third %+v", first, second, third)
	}
	if first.HarnessName != "codex" || first.HarnessVersion != "0.150" || first.ModelName != "gpt" || first.ModelVersion != "5.6" || second.HarnessVersion != "0.151" || third.HarnessName != "claude-code" {
		t.Fatalf("persisted harness metadata = first %+v, second %+v, third %+v", first, second, third)
	}
	if first.AccountableHumanID != runtime.actor.HumanPrincipalID || second.AccountableHumanID != runtime.actor.HumanPrincipalID || third.AccountableHumanID != runtime.actor.HumanPrincipalID {
		t.Fatalf("persisted accountability = first %+v, second %+v, third %+v, owner %s", first, second, third, runtime.actor.HumanPrincipalID)
	}
	if human.ActorKind != types.ActorHuman || human.HumanPrincipalID != runtime.actor.HumanPrincipalID || human.AgentID != "" || human.SessionID == "" || human.HarnessName != "wormhole-cli" || human.HarnessVersion != version || human.AccountableHumanID != "" || human.ModelName != "" {
		t.Fatalf("persisted CLI actor = %+v, want selected human with CLI session provenance", human)
	}
}

type stage2ParitySurface string

const (
	stage2ParityCLI stage2ParitySurface = "cli"
	stage2ParityMCP stage2ParitySurface = "mcp"
)

type stage2ParityGitFixture struct{ remote string }

func newStage2ParityGitFixture(t *testing.T) stage2ParityGitFixture {
	t.Helper()
	root := t.TempDir()
	seed := filepath.Join(root, "seed")
	if err := os.Mkdir(seed, 0o700); err != nil {
		t.Fatal(err)
	}
	copyStage2ParityTree(t, filepath.Join("..", "..", "internal", "types", "projectstate", "testdata", "v1", "valid", ".wormhole"), filepath.Join(seed, ".wormhole"))
	stage2Git(t, seed, "init", "-b", "main")
	stage2Git(t, seed, "config", "user.name", "Parity Fixture")
	stage2Git(t, seed, "config", "user.email", "parity@example.test")
	stage2Git(t, seed, "add", ".wormhole")
	stage2Git(t, seed, "commit", "-m", "test: parity seed")
	remote := filepath.Join(root, "origin.git")
	if output, err := exec.Command("git", "clone", "--bare", seed, remote).CombinedOutput(); err != nil {
		t.Fatalf("create origin: %v: %s", err, output)
	}
	return stage2ParityGitFixture{remote: remote}
}

type stage2ParityRuntime struct {
	root, socket string
	capability   string
	store        *localstore.Store
	service      *projectstate.Service
	identity     *localidentity.Store
	binding      types.WorkspaceBinding
	actor        types.ActorEnvelope
}

func newStage2ParityRuntime(t *testing.T, fixture stage2ParityGitFixture, name string, publicGit, changed bool) *stage2ParityRuntime {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if output, err := exec.Command("git", "clone", "--no-local", fixture.remote, root).CombinedOutput(); err != nil {
		t.Fatalf("clone %s: %v: %s", name, err, output)
	}
	store, err := localstore.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := projectstate.NewService(localstore.NewWorkspaceRepo(store.DB()), projectstate.ServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	registered, err := service.RegisterWorkspace(context.Background(), projectstate.RegisterWorkspaceRequest{
		Root: root, ExpectedProjectID: stage2ParityProjectID,
		ExpectedRepository: types.RepositoryIdentity{Provider: "github", ImmutableID: "R_kgDOExample-1", CanonicalRemote: "https://github.com/acme/wormhole"},
		ExpectedCommit:     stage2GitOutput(t, root, "rev-parse", "HEAD"),
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := localidentity.Open(filepath.Join(t.TempDir(), "identities"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identity.EnsureSelectedForSetup(context.Background(), "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", types.ConfirmedIdentitySelection{DisplayName: "Parity User"}); err != nil {
		t.Fatal(err)
	}
	actor, err := identity.ResolveHumanActor(context.Background(), time.Date(2026, 8, 27, 2, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if publicGit {
		current, err := service.PublicationConfiguration(context.Background(), registered.Binding.Scope)
		if err != nil {
			t.Fatal(err)
		}
		constraint, err := projectstate.DigestPublicationBindingConstraint(registered.Binding.Repository, current.ObservedOriginDigest)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.ReconfigurePublication(context.Background(), projectstate.ReconfigurePublicationRequest{
			Scope: registered.Binding.Scope, ExpectedBinding: registered.Binding, ExpectedPublicationBindingDigest: constraint,
			Expected: current, Classification: types.PublicationPublicGit, Actor: actor,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if changed {
		taskPath := filepath.Join(root, ".wormhole", "state", "v1", "tasks", "22222222-2222-4222-8222-222222222222.json")
		data, err := os.ReadFile(taskPath)
		if err != nil {
			t.Fatal(err)
		}
		updated := strings.Replace(string(data), `"status":"wip"`, `"status":"done"`, 1)
		if updated == string(data) {
			t.Fatal("task fixture status replacement did not apply")
		}
		if err := os.WriteFile(taskPath, []byte(updated), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	supervisor, err := localapi.NewSupervisor(localapi.SupervisorDependencies{
		Store: store, ProjectState: service, Identity: identity, Fabric: localapi.NewLocalOnlyFabricRouter(), CodeGraph: localapi.NewDisabledCodeGraphProvider(),
	})
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "gateway.sock")
	server, err := supervisor.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	capability, err := identity.CLICapability(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = supervisor.Close()
		<-serveDone
		_ = store.Close()
	})
	return &stage2ParityRuntime{root: root, socket: socket, capability: capability, store: store, service: service, identity: identity, binding: registered.Binding, actor: actor}
}

func stage2ParityRequest(t *testing.T, runtime *stage2ParityRuntime, operation localapi.WorkspaceOperation, surface stage2ParitySurface) localapi.WorkspaceCommandRequest {
	t.Helper()
	request := localapi.WorkspaceCommandRequest{Operation: operation}
	switch operation {
	case localapi.WorkspaceOperationCheckpoint:
		if _, err := executeStage2Surface(runtime, surface, localapi.WorkspaceCommandRequest{Operation: localapi.WorkspaceOperationImport}); err != nil {
			t.Fatalf("checkpoint import: %v", err)
		}
		diff, err := executeStage2Surface(runtime, surface, localapi.WorkspaceCommandRequest{Operation: localapi.WorkspaceOperationDiff})
		if err != nil || diff.Diff == nil {
			t.Fatalf("checkpoint diff: %+v, %v", diff, err)
		}
		request.PublicationReviewDigest = string(diff.Diff.PublicationReviewDigest)
	case localapi.WorkspaceOperationStash:
		if _, err := executeStage2Surface(runtime, surface, localapi.WorkspaceCommandRequest{Operation: localapi.WorkspaceOperationImport}); err != nil {
			t.Fatalf("stash import: %v", err)
		}
		request.RequestID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		request.Label = "parity pause"
	}
	return request
}

func executeStage2Surface(runtime *stage2ParityRuntime, surface stage2ParitySurface, request localapi.WorkspaceCommandRequest) (localapi.WorkspaceCommandResult, error) {
	if surface == stage2ParityCLI {
		return executeStage2CLI(runtime, request)
	}
	return executeStage2MCP(runtime, request, nil)
}

func executeStage2CLI(runtime *stage2ParityRuntime, request localapi.WorkspaceCommandRequest) (localapi.WorkspaceCommandResult, error) {
	previous := workspaceBackendFactory
	workspaceBackendFactory = func() (workspaceCommandBackend, error) {
		return &gatewayWorkspaceBackend{socketPath: runtime.socket, workingDirectory: runtime.root}, nil
	}
	defer func() { workspaceBackendFactory = previous }()
	previousCapability := readGatewayCLICapability
	readGatewayCLICapability = func(context.Context) (string, error) { return runtime.capability, nil }
	defer func() { readGatewayCLICapability = previousCapability }()
	args := []string{}
	if request.Operation == localapi.WorkspaceOperationCheckpoint && request.PublicationReviewDigest != "" {
		args = append(args, "--publication-review-digest", request.PublicationReviewDigest)
	}
	if request.Operation == localapi.WorkspaceOperationStash {
		args = append(args, "--request-id", request.RequestID, "--label", request.Label)
	}
	var stdout, stderr strings.Builder
	if code := runWorkspaceCommand(request.Operation, args, &stdout, &stderr); code != 0 {
		return localapi.WorkspaceCommandResult{}, fmt.Errorf("exit %d: %s", code, strings.TrimSpace(stderr.String()))
	}
	var result localapi.WorkspaceCommandResult
	if err := json.Unmarshal([]byte(stdout.String()), &result); err != nil {
		return result, err
	}
	return result, nil
}

func executeStage2MCP(runtime *stage2ParityRuntime, request localapi.WorkspaceCommandRequest, forged map[string]any) (localapi.WorkspaceCommandResult, error) {
	return executeStage2MCPClient(runtime, request, forged, map[string]any{"name": "stage2-mcp", "version": "1"})
}

func executeStage2MCPClient(runtime *stage2ParityRuntime, request localapi.WorkspaceCommandRequest, forged map[string]any, clientInfo map[string]any) (localapi.WorkspaceCommandResult, error) {
	oldDirectory, err := os.Getwd()
	if err != nil {
		return localapi.WorkspaceCommandResult{}, err
	}
	if err := os.Chdir(runtime.root); err != nil {
		return localapi.WorkspaceCommandResult{}, err
	}
	defer os.Chdir(oldDirectory)
	conn, err := net.Dial("unix", runtime.socket)
	if err != nil {
		return localapi.WorkspaceCommandResult{}, err
	}
	stdinReader, stdinWriter := io.Pipe()
	stdout := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- bridge(stdinReader, stdout, conn) }()
	defer func() {
		_ = stdinWriter.Close()
		<-done
	}()
	write := func(value any) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = stdinWriter.Write(append(raw, '\n'))
		return err
	}
	initializeParams, err := json.Marshal(map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": clientInfo})
	if err != nil {
		return localapi.WorkspaceCommandResult{}, err
	}
	if err := write(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "initialize", Params: initializeParams}); err != nil {
		return localapi.WorkspaceCommandResult{}, err
	}
	if _, err := waitStage2BridgeResponse(stdout, "1"); err != nil {
		return localapi.WorkspaceCommandResult{}, err
	}
	if err := write(rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"}); err != nil {
		return localapi.WorkspaceCommandResult{}, err
	}
	arguments := map[string]any{}
	switch request.Operation {
	case localapi.WorkspaceOperationCheckpoint:
		if request.PublicationReviewDigest != "" {
			arguments["publication_review_digest"] = request.PublicationReviewDigest
		}
	case localapi.WorkspaceOperationStash:
		arguments["request_id"], arguments["label"] = request.RequestID, request.Label
	}
	for key, value := range forged {
		arguments[key] = value
	}
	argumentBytes, err := json.Marshal(arguments)
	if err != nil {
		return localapi.WorkspaceCommandResult{}, err
	}
	params, _ := json.Marshal(toolsCallParams{Name: "wormhole.workspace." + string(request.Operation), Arguments: argumentBytes})
	if err := write(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("2"), Method: "tools/call", Params: params}); err != nil {
		return localapi.WorkspaceCommandResult{}, err
	}
	response, err := waitStage2BridgeResponse(stdout, "2")
	if err != nil {
		return localapi.WorkspaceCommandResult{}, err
	}
	if response.Error != nil {
		return localapi.WorkspaceCommandResult{}, fmt.Errorf("%s", response.Error.Message)
	}
	var callResult toolCallResult
	if err := json.Unmarshal(response.Result, &callResult); err != nil {
		return localapi.WorkspaceCommandResult{}, err
	}
	if callResult.IsError {
		message := "tool failed"
		if len(callResult.Content) > 0 {
			message = callResult.Content[0].Text
		}
		return localapi.WorkspaceCommandResult{}, fmt.Errorf("%s", message)
	}
	if len(callResult.Content) != 1 {
		return localapi.WorkspaceCommandResult{}, fmt.Errorf("unexpected tool content")
	}
	result := localapi.WorkspaceCommandResult{Operation: request.Operation}
	switch request.Operation {
	case localapi.WorkspaceOperationStatus:
		result.Status = &localapi.WorkspaceStatusReadback{}
		err = json.Unmarshal([]byte(callResult.Content[0].Text), result.Status)
	case localapi.WorkspaceOperationDiff:
		result.Diff = &localapi.WorkspaceDiffReadback{}
		err = json.Unmarshal([]byte(callResult.Content[0].Text), result.Diff)
	case localapi.WorkspaceOperationImport:
		result.Import = &localapi.WorkspaceImportReadback{}
		err = json.Unmarshal([]byte(callResult.Content[0].Text), result.Import)
	case localapi.WorkspaceOperationCheckpoint:
		result.Checkpoint = &localapi.WorkspaceCheckpointReadback{}
		err = json.Unmarshal([]byte(callResult.Content[0].Text), result.Checkpoint)
	case localapi.WorkspaceOperationStash:
		result.Stash = &localapi.WorkspaceStashReadback{}
		err = json.Unmarshal([]byte(callResult.Content[0].Text), result.Stash)
	}
	return result, err
}

func stage2ReplaceTaskStatus(t *testing.T, root, before, after string) {
	t.Helper()
	path := filepath.Join(root, ".wormhole", "state", "v1", "tasks", "22222222-2222-4222-8222-222222222222.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(data), `"status":"`+before+`"`, `"status":"`+after+`"`, 1)
	if updated == string(data) {
		t.Fatalf("task status %q not found in %s", before, path)
	}
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
}

func stage2PersistedStashActors(t *testing.T, store *localstore.Store) []types.ActorEnvelope {
	t.Helper()
	rows, err := store.DB().Query(`SELECT actor_json FROM workspace_stashes ORDER BY label`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	actors := []types.ActorEnvelope{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var actor types.ActorEnvelope
		if err := json.Unmarshal(raw, &actor); err != nil || actor.ValidateLocalAction() != nil {
			t.Fatalf("decode persisted stash actor: %v, %+v", err, actor)
		}
		actors = append(actors, actor)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return actors
}

func waitStage2BridgeResponse(stdout *syncBuffer, id string) (rpcResponse, error) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
			var response rpcResponse
			if json.Unmarshal([]byte(line), &response) == nil && string(response.ID) == id {
				return response, nil
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	return rpcResponse{}, fmt.Errorf("timeout waiting for response %s: %s", id, stdout.String())
}

func normalizeStage2ParityResult(result *localapi.WorkspaceCommandResult) {
	if result.Status != nil {
		result.Status.WorkspaceID = ""
		result.Status.PublicationReviewDigest = ""
	}
	if result.Diff != nil {
		result.Diff.PublicationReviewDigest = ""
	}
	if result.Checkpoint != nil {
		result.Checkpoint.JournalID = ""
	}
	if result.Stash != nil {
		result.Stash.StashID = ""
	}
}

func stage2MaterializationCount(t *testing.T, store *localstore.Store) int {
	t.Helper()
	var count int
	if err := store.DB().QueryRow(`SELECT count(*) FROM workspace_materializations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func stage2PortableDigest(t *testing.T, root string) state.Digest {
	t.Helper()
	tree, err := projectstate.ReadWorkingTreeNoFollow(root)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := state.DigestTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func copyStage2ParityTree(t *testing.T, source, destination string) {
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
			return os.MkdirAll(target, 0o700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	}); err != nil {
		t.Fatal(err)
	}
}

func stage2Git(t *testing.T, root string, args ...string) {
	t.Helper()
	if output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

func stage2GitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
