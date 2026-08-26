package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	codegraphconfig "github.com/H4RL33/wormhole/internal/runtime/codegraph/config"
	"github.com/H4RL33/wormhole/internal/runtime/codegraph/index"
	codegraphstore "github.com/H4RL33/wormhole/internal/runtime/codegraph/store"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
)

func TestGateBActualGatewayTwoClientsConcurrentHumanDisable(t *testing.T) {
	const (
		projectID  = "project-gate-b"
		agentID    = "agent-gate-b"
		passportID = "passport-gate-b"
		profile    = "default"
		token      = "gate-b-token"
		remote     = "https://example.invalid/gate-b-process.git"
		revisionID = "gate-b-process-active"
	)
	permissions := []string{"code_graph.query", "code_graph.source.read", "code_graph.status", "code_graph.rebuild"}
	fabric := gateBFabricServer(t, projectID, agentID, passportID, token, remote, permissions)
	defer fabric.Close()

	home := t.TempDir()
	runtimeDir := filepath.Join(home, "run")
	dataDir := filepath.Join(home, "data")
	env := append(os.Environ(), "HOME="+home, "XDG_RUNTIME_DIR="+runtimeDir, "XDG_DATA_HOME="+dataDir)
	credentialDir := filepath.Join(home, ".wormhole", "credentials")
	if err := os.MkdirAll(credentialDir, 0o700); err != nil {
		t.Fatal(err)
	}
	credential, err := json.Marshal(map[string]string{
		"server": fabric.URL, "project_id": projectID, "agent_id": agentID, "passport_id": passportID, "token": token,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credentialDir, profile+".json"), credential, 0o600); err != nil {
		t.Fatal(err)
	}

	checkout := gateBProcessRepository(t, remote)
	databasePath := filepath.Join(dataDir, "wormhole", "wormholed.db")
	seedEnrolledGatewayCheckpoint(t, databasePath, fabric.URL, projectID, agentID, passportID, token, profile)
	gatewayStore, err := localstore.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	graphStore, err := codegraphstore.Open(context.Background(), gatewayStore.DB(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if err := graphStore.PutProjectConfig(context.Background(), codegraphconfig.Project{
		ProjectID: projectID, Enabled: true, CanonicalRemote: remote, ActiveCheckout: checkout,
		ProjectSourceByteCeiling: codegraphconfig.DefaultProjectSourceByteCeiling,
	}); err != nil {
		t.Fatal(err)
	}
	if err := index.New(graphStore).Build(context.Background(), index.BuildRequest{ProjectID: projectID, RevisionID: revisionID}); err != nil {
		t.Fatal(err)
	}
	if err := gatewayStore.CacheWhoAmI(context.Background(), localstore.WhoAmICache{
		AgentID: agentID, ProjectID: projectID, Permissions: permissions, CachedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := gatewayStore.Close(); err != nil {
		t.Fatal(err)
	}

	gatewayBinary := task4BuildGatewayBinary(t)
	if task4GatewayBinErr != nil {
		t.Fatal(task4GatewayBinErr)
	}
	wormholeBinary := e2eBuildStdioBridgeBinary(t)
	if stdioBridgeBinErr != nil {
		t.Fatal(stdioBridgeBinErr)
	}
	socketPath := filepath.Join(runtimeDir, "wormhole", "wormholed.sock")
	startTask4ProcessDaemon(t, gatewayBinary, profile, env, socketPath)

	clientA := gateBDialMCPClient(t, socketPath)
	defer clientA.Close()
	clientB := gateBDialMCPClient(t, socketPath)
	defer clientB.Close()
	contractA := clientA.mustListCodeGraphTools(t)
	contractB := clientB.mustListCodeGraphTools(t)
	if !reflect.DeepEqual(contractA, contractB) {
		t.Fatalf("independent Code Graph tool contracts differ:\nA=%#v\nB=%#v", contractA, contractB)
	}
	wantToolNames := []string{"wormhole.code_graph.query", "wormhole.code_graph.rebuild", "wormhole.code_graph.status"}
	gotToolNames := make([]string, 0, len(contractA))
	for _, tool := range contractA {
		gotToolNames = append(gotToolNames, tool.Name)
		if len(tool.InputSchema) == 0 || tool.InputSchema["type"] != "object" {
			t.Fatalf("%s returned empty or non-object input schema: %#v", tool.Name, tool.InputSchema)
		}
	}
	if !reflect.DeepEqual(gotToolNames, wantToolNames) {
		t.Fatalf("Code Graph tools = %q, want exact three %q", gotToolNames, wantToolNames)
	}
	statusArgs := map[string]interface{}{"project_id": projectID}
	queryArgs := map[string]interface{}{"project_id": projectID, "entry_symbols": []string{"GateBTarget"}}
	blocked, err := clientA.call("wormhole.code_graph.status", statusArgs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(blocked.Error, "invalid private request context") {
		t.Fatalf("configured Code Graph status = %+v, want binding-aware fail-closed error", blocked)
	}
	if blocked.Error != "" {
		return
	}

	statusA := gateBDecodeStatus(t, clientA.mustCall(t, "wormhole.code_graph.status", statusArgs))
	statusB := gateBDecodeStatus(t, clientB.mustCall(t, "wormhole.code_graph.status", statusArgs))
	if statusA.State != "ready" || statusA.ActiveRevision != revisionID || statusB != statusA {
		t.Fatalf("two-client status contract A=%+v B=%+v", statusA, statusB)
	}
	queryA := gateBDecodeQuery(t, clientA.mustCall(t, "wormhole.code_graph.query", queryArgs))
	queryB := gateBDecodeQuery(t, clientB.mustCall(t, "wormhole.code_graph.query", queryArgs))
	if queryA.GraphRevision != revisionID || queryB.GraphRevision != revisionID || len(queryA.Matches) == 0 || len(queryB.Matches) == 0 {
		t.Fatalf("two-client query contract A=%+v B=%+v", queryA, queryB)
	}

	type spanningResult struct {
		response    mcpToolResponse
		err         error
		completedAt time.Time
	}
	requestWritten := make(chan struct{})
	releaseResponseRead := make(chan struct{})
	spanningDone := make(chan spanningResult, 1)
	go func() {
		response, callErr := clientA.callAfterWrite("wormhole.code_graph.query", queryArgs, func() {
			close(requestWritten)
			<-releaseResponseRead
		})
		spanningDone <- spanningResult{response: response, err: callErr, completedAt: time.Now()}
	}()
	<-requestWritten

	disable := exec.Command(wormholeBinary, "config", "code-graph", "disable", "--project", projectID, "--confirm")
	disable.Env = env
	var disableOutput bytes.Buffer
	disable.Stdout, disable.Stderr = &disableOutput, &disableOutput
	if err := disable.Start(); err != nil {
		close(releaseResponseRead)
		t.Fatal(err)
	}
	disableStartedAt := time.Now()
	close(releaseResponseRead)
	if err := disable.Wait(); err != nil {
		t.Fatalf("human CLI disable: %v output=%q", err, disableOutput.String())
	}
	if !strings.Contains(disableOutput.String(), "enabled=false") {
		t.Fatalf("human CLI disable output = %q", disableOutput.String())
	}

	select {
	case result := <-spanningDone:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.completedAt.Before(disableStartedAt) {
			t.Fatalf("spanning request completed at %s before human disable started at %s", result.completedAt, disableStartedAt)
		}
		if result.response.Error == "" {
			completed, decodeErr := gateBDecodeQueryResult(result.response.Result)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if completed.GraphRevision != revisionID {
				t.Fatalf("request spanning disable observed mixed revision %q", completed.GraphRevision)
			}
		} else if !gateBDisableDenial(result.response.Error) {
			t.Fatalf("request spanning disable returned unrelated tool error %q", result.response.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request spanning human disable neither completed nor returned a tool denial")
	}
	if denied, callErr := clientB.call("wormhole.code_graph.query", queryArgs); callErr != nil || denied.Error == "" {
		t.Fatalf("second client after disable transport=%v response=%+v", callErr, denied)
	}
	statusB = gateBDecodeStatus(t, clientB.mustCall(t, "wormhole.code_graph.status", statusArgs))
	if statusB.State != "disabled" || statusB.ActiveRevision != "" {
		t.Fatalf("post-disable status = %+v", statusB)
	}
	cleanupStore, err := localstore.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupStore.Close()
	for _, table := range []string{
		"codegraph_config", "codegraph_revisions", "codegraph_nodes", "codegraph_files",
		"codegraph_symbols", "codegraph_edges", "codegraph_diagnostics", "codegraph_lifecycle",
	} {
		var rows int
		if err := cleanupStore.DB().QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE project_id = ?`, projectID).Scan(&rows); err != nil {
			t.Fatalf("count %s cleanup rows: %v", table, err)
		}
		if rows != 0 {
			t.Fatalf("%s retains %d rows for disabled project", table, rows)
		}
	}
}

func gateBDisableDenial(message string) bool {
	for _, fragment := range []string{"disabled", "disabling", "not found", "unavailable"} {
		if strings.Contains(strings.ToLower(message), fragment) {
			return true
		}
	}
	return false
}

type gateBMCPClient struct {
	connection net.Conn
	reader     *bufio.Reader
	nextID     int
}

func gateBDialMCPClient(t *testing.T, socketPath string) *gateBMCPClient {
	t.Helper()
	connection, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	client := &gateBMCPClient{connection: connection, reader: bufio.NewReader(connection), nextID: 10}
	mcpInitialize(t, connection, client.reader)
	return client
}

func (client *gateBMCPClient) Close() { _ = client.connection.Close() }

func (client *gateBMCPClient) mustCall(t *testing.T, tool string, args map[string]interface{}) json.RawMessage {
	t.Helper()
	result, err := client.call(tool, args)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("%s: %s", tool, result.Error)
	}
	return result.Result
}

func (client *gateBMCPClient) call(tool string, args map[string]interface{}) (mcpToolResponse, error) {
	return client.callAfterWrite(tool, args, nil)
}

func (client *gateBMCPClient) callAfterWrite(tool string, args map[string]interface{}, afterWrite func()) (mcpToolResponse, error) {
	client.nextID++
	argumentBytes, err := json.Marshal(args)
	if err != nil {
		return mcpToolResponse{}, err
	}
	paramsBytes, err := json.Marshal(mcpToolsCallParams{Name: tool, Arguments: argumentBytes})
	if err != nil {
		return mcpToolResponse{}, err
	}
	idBytes, err := json.Marshal(client.nextID)
	if err != nil {
		return mcpToolResponse{}, err
	}
	requestBytes, err := json.Marshal(mcpRpcRequest{JSONRPC: "2.0", ID: idBytes, Method: "tools/call", Params: paramsBytes})
	if err != nil {
		return mcpToolResponse{}, err
	}
	if err := client.connection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return mcpToolResponse{}, err
	}
	defer client.connection.SetDeadline(time.Time{})
	if _, err := client.connection.Write(append(requestBytes, '\n')); err != nil {
		return mcpToolResponse{}, err
	}
	if afterWrite != nil {
		afterWrite()
	}
	line, err := client.reader.ReadBytes('\n')
	if err != nil {
		return mcpToolResponse{}, err
	}
	var response mcpRpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(line), &response); err != nil {
		return mcpToolResponse{}, err
	}
	if response.Error != nil {
		return mcpToolResponse{Error: response.Error.Message}, nil
	}
	var result mcpToolCallResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		return mcpToolResponse{}, err
	}
	if result.IsError {
		if len(result.Content) == 0 {
			return mcpToolResponse{Error: "tool failed without content"}, nil
		}
		return mcpToolResponse{Error: result.Content[0].Text}, nil
	}
	if len(result.Content) == 0 {
		return mcpToolResponse{}, errors.New("tool returned no content")
	}
	return mcpToolResponse{Result: json.RawMessage(result.Content[0].Text)}, nil
}

type gateBListedTool struct {
	Name        string                 `json:"name"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

func (client *gateBMCPClient) mustListCodeGraphTools(t *testing.T) []gateBListedTool {
	t.Helper()
	client.nextID++
	idBytes, err := json.Marshal(client.nextID)
	if err != nil {
		t.Fatal(err)
	}
	requestBytes, err := json.Marshal(mcpRpcRequest{JSONRPC: "2.0", ID: idBytes, Method: "tools/list", Params: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.connection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	defer client.connection.SetDeadline(time.Time{})
	if _, err := client.connection.Write(append(requestBytes, '\n')); err != nil {
		t.Fatal(err)
	}
	line, err := client.reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response mcpRpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(line), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != nil {
		t.Fatalf("tools/list: %s", response.Error.Message)
	}
	var listed struct {
		Tools []gateBListedTool `json:"tools"`
	}
	if err := json.Unmarshal(response.Result, &listed); err != nil {
		t.Fatal(err)
	}
	codeGraphTools := make([]gateBListedTool, 0, 3)
	for _, tool := range listed.Tools {
		if strings.HasPrefix(tool.Name, "wormhole.code_graph.") {
			codeGraphTools = append(codeGraphTools, tool)
		}
	}
	sort.Slice(codeGraphTools, func(i, j int) bool { return codeGraphTools[i].Name < codeGraphTools[j].Name })
	return codeGraphTools
}

type gateBStatus struct {
	State          string `json:"state"`
	ActiveRevision string `json:"active_revision"`
}

type gateBQuery struct {
	GraphRevision string `json:"graph_revision"`
	Matches       []struct {
		QualifiedName string `json:"qualified_name"`
	} `json:"matches"`
}

func gateBDecodeStatus(t *testing.T, raw json.RawMessage) gateBStatus {
	t.Helper()
	var result gateBStatus
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func gateBDecodeQuery(t *testing.T, raw json.RawMessage) gateBQuery {
	t.Helper()
	result, err := gateBDecodeQueryResult(raw)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func gateBDecodeQueryResult(raw json.RawMessage) (gateBQuery, error) {
	var result gateBQuery
	err := json.Unmarshal(raw, &result)
	return result, err
}

func gateBProcessRepository(t *testing.T, remote string) string {
	t.Helper()
	directory := t.TempDir()
	for _, arguments := range [][]string{
		{"init"}, {"config", "user.email", "gate-b@example.invalid"}, {"config", "user.name", "Gate B"},
		{"remote", "add", "origin", remote},
	} {
		gateBProcessGit(t, directory, arguments...)
	}
	if err := os.WriteFile(filepath.Join(directory, "go.mod"), []byte("module example.invalid/gatebprocess\n\ngo 1.25\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "target.go"), []byte("package gatebprocess\n\nfunc GateBTarget() string { return \"gate-b\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gateBProcessGit(t, directory, "add", "go.mod", "target.go")
	gateBProcessGit(t, directory, "commit", "-m", "gate b fixture")
	return directory
}

func gateBProcessGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
}

func gateBFabricServer(t *testing.T, projectID, agentID, passportID, token, remote string, permissions []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+token {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		var rpc mcpRpcRequest
		if err := json.NewDecoder(request.Body).Decode(&rpc); err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		var params mcpToolsCallParams
		if err := json.Unmarshal(rpc.Params, &params); err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		var output interface{}
		switch params.Name {
		case "wormhole.agent.whoami":
			output = map[string]interface{}{
				"agent_id": agentID, "owner": "gate-b", "model": "gate-b-model", "capabilities": []string{"code"},
				"project_id": projectID, "permissions": permissions,
			}
		case "wormhole.sync.bootstrap":
			output = gatewayTestBootstrapOutput(projectID, agentID, passportID)
			org := output.(map[string]interface{})["org_config"].(types.BootstrapOrgConfigV1)
			org.Identity.Permissions = permissions
			org.Identity.Passport.Repositories = []string{remote}
			output.(map[string]interface{})["org_config"] = org
		case "wormhole.sync.incremental_pull":
			output = map[string]interface{}{"updates": []interface{}{}, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "version": 1}
		case "wormhole.sync.incremental_push":
			output = map[string]interface{}{"items_received": 0, "applied": []interface{}{}, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "version": 1}
		default:
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		outputBytes, err := json.Marshal(output)
		if err != nil {
			t.Errorf("marshal Fabric output: %v", err)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		resultBytes, err := json.Marshal(map[string]interface{}{"content": []map[string]string{{"type": "text", "text": string(outputBytes)}}})
		if err != nil {
			t.Errorf("marshal Fabric tool result: %v", err)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(writer).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": rpc.ID, "result": json.RawMessage(resultBytes)}); err != nil {
			t.Errorf("encode Fabric response: %v", err)
		}
	}))
}
