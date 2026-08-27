//go:build linux

package localapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localidentity"
	"github.com/H4RL33/wormhole/internal/types"
)

func TestRealSocketMCPConnectionsBindDurableHarnessAgentsAndFreshSessions(t *testing.T) {
	identity := selectedSocketIdentity(t)
	binding := privateRoutingTestBinding(t, filepath.Join(t.TempDir(), "checkout"), "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011")
	server := setupIdentityTestServer(t, identity, binding)
	recorder := &recordingLocalActorResolver{store: identity}
	server.actorResolver = recorder
	socket := serveIdentitySocket(t, server)

	for _, client := range []map[string]any{
		{"name": "codex", "version": "0.150", "modelName": "gpt", "modelVersion": "5.6"},
		{"name": "CODEX", "version": "0.151"},
		{"name": "claude-code", "version": "2.1"},
	} {
		result := callIdentitySocketTool(t, socket, client, map[string]any{
			privateRequestContextKey: map[string]any{"working_directory": binding.Checkout.CanonicalPath},
		}, nil)
		if result.IsError {
			t.Fatalf("status as %v = %+v", client, result)
		}
	}
	actors := recorder.snapshot()
	if len(actors) != 3 {
		t.Fatalf("resolved actors = %d, want 3: %+v", len(actors), actors)
	}
	first, second, third := actors[0], actors[1], actors[2]
	if first.ActorKind != types.ActorAgent || first.AgentID == "" || first.AgentID != second.AgentID || first.SessionID == second.SessionID || third.AgentID == first.AgentID || first.AccountableHumanID == "" || first.AccountableHumanID != second.AccountableHumanID || first.AccountableHumanID != third.AccountableHumanID {
		t.Fatalf("socket actors = first %+v, second %+v, third %+v", first, second, third)
	}
	if first.HarnessName != "codex" || first.HarnessVersion != "0.150" || first.ModelName != "gpt" || first.ModelVersion != "5.6" || second.ModelName != "" || second.ModelVersion != "" || third.HarnessName != "claude-code" {
		t.Fatalf("socket provenance = first %+v, second %+v, third %+v", first, second, third)
	}
	for _, actor := range actors {
		deadline := time.Now().Add(2 * time.Second)
		for {
			session, err := identity.Session(t.Context(), actor.SessionID)
			if err == nil && session.EndedAt != nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("connection session %s did not become terminal: %+v, %v", actor.SessionID, session, err)
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func TestRealSocketRejectsForgedInitializeAndToolProvenanceBeforeActorResolution(t *testing.T) {
	identity := selectedSocketIdentity(t)
	binding := privateRoutingTestBinding(t, filepath.Join(t.TempDir(), "checkout"), "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011")
	server := setupIdentityTestServer(t, identity, binding)
	recorder := &recordingLocalActorResolver{store: identity}
	server.actorResolver = recorder
	socket := serveIdentitySocket(t, server)

	for _, forged := range []map[string]any{
		{"clientInfo": map[string]any{"name": "codex", "version": "1", "agent_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}},
		{"clientInfo": map[string]any{"name": "codex", "version": "1"}, "session_id": "forged"},
	} {
		conn, err := net.Dial("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		writeIdentityRPC(t, conn, rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "initialize", Params: mustIdentityJSON(t, forged)})
		response := readIdentityRPC(t, bufio.NewReader(conn))
		_ = conn.Close()
		if response.Error == nil || response.Error.Code != rpcInvalidParams {
			t.Fatalf("forged initialize %v response = %+v", forged, response)
		}
	}

	for _, field := range []string{"agent_id", "session_id", "harness_name", "harness_version", "model_name", "model_version", "accountable_human_id", "assurance"} {
		arguments := map[string]any{
			privateRequestContextKey: map[string]any{"working_directory": binding.Checkout.CanonicalPath},
			field:                    "forged",
		}
		result := callIdentitySocketTool(t, socket, map[string]any{"name": "codex", "version": "1"}, arguments, nil)
		if !result.IsError {
			t.Fatalf("forged %s result = %+v", field, result)
		}
	}
	if got := len(recorder.snapshot()); got != 0 {
		t.Fatalf("forged provenance reached actor resolution %d time(s)", got)
	}
}

func TestRealSocketPrivateMethodsRequireCapabilityAndInitializedHumanSession(t *testing.T) {
	identity := selectedSocketIdentity(t)
	binding := privateRoutingTestBinding(t, filepath.Join(t.TempDir(), "checkout"), "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011")
	server := setupIdentityTestServer(t, identity, binding)
	recorder := &recordingLocalActorResolver{store: identity}
	server.actorResolver = recorder
	server.cliCapability = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	socket := serveIdentitySocket(t, server)

	privateMethods := []string{
		PrivateSetupRegisterWorkspaceRPCMethod, PrivateSetupEnsureIdentityRPCMethod,
		PrivateSetupPublicationRPCMethod, PrivateSetupImportRPCMethod, PrivateSetupVerifyRPCMethod,
		PrivateWorkspaceRPCMethod, integrationPlanRPCMethod, integrationCommitRPCMethod,
		"wormhole.private.future",
	}
	for _, method := range privateMethods {
		response := callIdentitySocketRPC(t, socket, map[string]any{"name": "codex", "version": "1"}, method, map[string]any{
			"capability": server.cliCapability, "request": map[string]any{},
		})
		if response.Error == nil || response.Error.Code != rpcInvalidParams || response.Error.Message != ErrPrivateCLIAuthorization.Error() {
			t.Fatalf("agent %s response = %+v", method, response)
		}
	}
	if got := len(recorder.snapshot()); got != 0 {
		t.Fatalf("agent private calls reached actor resolution %d time(s)", got)
	}

	for name, params := range map[string]json.RawMessage{
		"missing":   json.RawMessage(`{"request":{}}`),
		"wrong":     json.RawMessage(`{"capability":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","request":{}}`),
		"duplicate": json.RawMessage(`{"capability":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","capability":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","request":{}}`),
	} {
		response := callIdentitySocketRawRPC(t, socket, map[string]any{"name": "wormhole-cli", "version": "test"}, PrivateWorkspaceRPCMethod, params)
		if response.Error == nil || response.Error.Message != ErrPrivateCLIAuthorization.Error() || strings.Contains(response.Error.Message, server.cliCapability) {
			t.Fatalf("%s capability response = %+v", name, response)
		}
	}

	response := callIdentitySocketRPC(t, socket, map[string]any{"name": "wormhole-cli", "version": "test"}, PrivateWorkspaceRPCMethod, map[string]any{
		"capability": server.cliCapability,
		"request":    PrivateWorkspaceCommandRequest{WorkingDirectory: binding.Checkout.CanonicalPath, Command: WorkspaceCommandRequest{Operation: WorkspaceOperationStatus}},
	})
	if response.Error != nil {
		t.Fatalf("authorized CLI response = %+v", response)
	}
	actors := recorder.snapshot()
	if len(actors) != 1 || actors[0].ActorKind != types.ActorHuman || actors[0].SessionID == "" || actors[0].HarnessName != "wormhole-cli" || actors[0].HarnessVersion != "test" || actors[0].AgentID != "" || actors[0].AccountableHumanID != "" || actors[0].ModelName != "" {
		t.Fatalf("authorized CLI actors = %+v", actors)
	}
}

func TestRealSocketRejectsDuplicateInitializeWithExactlyOneSession(t *testing.T) {
	identity := selectedSocketIdentity(t)
	binding := privateRoutingTestBinding(t, filepath.Join(t.TempDir(), "checkout"), "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011")
	server := setupIdentityTestServer(t, identity, binding)
	socket := serveIdentitySocket(t, server)
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	params := mustIdentityJSON(t, map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "codex", "version": "1"}})
	writeIdentityRPC(t, conn, rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "initialize", Params: params})
	if response := readIdentityRPC(t, reader); response.Error != nil {
		t.Fatalf("first initialize = %+v", response)
	}
	writeIdentityRPC(t, conn, rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("2"), Method: "initialize", Params: params})
	if response := readIdentityRPC(t, reader); response.Error == nil || response.Error.Code != rpcInvalidParams {
		t.Fatalf("duplicate initialize = %+v", response)
	}
	sessions, err := identity.ConnectionSessions(t.Context())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions after duplicate initialize = %+v, %v; want exactly one", sessions, err)
	}
}

func callIdentitySocketRPC(t *testing.T, socket string, clientInfo map[string]any, method string, params any) rpcResponse {
	t.Helper()
	return callIdentitySocketRawRPC(t, socket, clientInfo, method, mustIdentityJSON(t, params))
}

func callIdentitySocketRawRPC(t *testing.T, socket string, clientInfo map[string]any, method string, params json.RawMessage) rpcResponse {
	t.Helper()
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	initialize := mustIdentityJSON(t, map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": clientInfo})
	writeIdentityRPC(t, conn, rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "initialize", Params: initialize})
	if response := readIdentityRPC(t, reader); response.Error != nil {
		t.Fatalf("initialize = %+v", response)
	}
	writeIdentityRPC(t, conn, rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"})
	writeIdentityRPC(t, conn, rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("2"), Method: method, Params: params})
	return readIdentityRPC(t, reader)
}

type recordingLocalActorResolver struct {
	store  *localidentity.Store
	mu     sync.Mutex
	actors []types.ActorEnvelope
}

func (r *recordingLocalActorResolver) ResolveLocalActor(ctx context.Context, connection ConnectionIdentity) (types.ActorEnvelope, error) {
	actor, err := r.store.ResolveLocalActor(ctx, connection)
	if err == nil {
		r.mu.Lock()
		r.actors = append(r.actors, actor)
		r.mu.Unlock()
	}
	return actor, err
}

func (r *recordingLocalActorResolver) snapshot() []types.ActorEnvelope {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]types.ActorEnvelope(nil), r.actors...)
}

func selectedSocketIdentity(t *testing.T) *localidentity.Store {
	t.Helper()
	identity, err := localidentity.Open(filepath.Join(t.TempDir(), "identities"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identity.EnsureSelectedForSetup(t.Context(), "00000000-0000-4000-8000-000000000031", types.ConfirmedIdentitySelection{DisplayName: "Socket User"}); err != nil {
		t.Fatal(err)
	}
	return identity
}

func serveIdentitySocket(t *testing.T, server *Server) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "gateway.sock")
	listener, err := listenLocalSocket(socket)
	if err != nil {
		t.Fatal(err)
	}
	server.listener = listener
	server.socketPath = socket
	server.handlers = make(chan struct{}, maxActiveConnections)
	server.serveReady = make(chan struct{})
	server.registry = newLocalRegistry(server)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	<-server.Serving()
	t.Cleanup(func() {
		cancel()
		_ = server.Close()
		<-done
	})
	return socket
}

func callIdentitySocketTool(t *testing.T, socket string, clientInfo map[string]any, arguments map[string]any, mutateInitialize func(map[string]any)) toolCallResult {
	t.Helper()
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	params := map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": clientInfo}
	if mutateInitialize != nil {
		mutateInitialize(params)
	}
	writeIdentityRPC(t, conn, rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "initialize", Params: mustIdentityJSON(t, params)})
	if response := readIdentityRPC(t, reader); response.Error != nil {
		t.Fatalf("initialize response = %+v", response)
	}
	writeIdentityRPC(t, conn, rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"})
	paramsRaw := mustIdentityJSON(t, toolsCallParams{Name: "wormhole.workspace.status", Arguments: mustIdentityJSON(t, arguments)})
	writeIdentityRPC(t, conn, rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("2"), Method: "tools/call", Params: paramsRaw})
	response := readIdentityRPC(t, reader)
	if response.Error != nil {
		t.Fatalf("tools/call response = %+v", response)
	}
	var result toolCallResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func writeIdentityRPC(t *testing.T, conn net.Conn, request rpcRequest) {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(append(raw, '\n')); err != nil {
		t.Fatal(err)
	}
}

func readIdentityRPC(t *testing.T, reader *bufio.Reader) rpcResponse {
	t.Helper()
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response rpcResponse
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func mustIdentityJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
