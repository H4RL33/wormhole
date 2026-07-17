package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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

// mcpInitialize sends initialize and notifications/initialized handshake.
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

	notif := mcpRpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"}
	notifRaw, _ := json.Marshal(notif)
	if _, err := conn.Write(append(notifRaw, '\n')); err != nil {
		t.Fatalf("write notifications/initialized: %v", err)
	}
}

// mcpCallTool sends a tools/call request and returns the result.
func mcpCallTool(t *testing.T, conn net.Conn, reader *bufio.Reader, id int, tool string, args map[string]interface{}) mcpToolResponse {
	t.Helper()

	var argsRaw json.RawMessage
	if args != nil {
		b, _ := json.Marshal(args)
		argsRaw = b
	} else {
		argsRaw = json.RawMessage(`{}`)
	}

	params := mcpToolsCallParams{Name: tool, Arguments: argsRaw}
	paramsRaw, _ := json.Marshal(params)
	idRaw, _ := json.Marshal(id)
	req := mcpRpcRequest{JSONRPC: "2.0", ID: idRaw, Method: "tools/call", Params: paramsRaw}
	reqRaw, _ := json.Marshal(req)
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
	if result.IsError {
		text := ""
		if len(result.Content) > 0 {
			text = result.Content[0].Text
		}
		return mcpToolResponse{Error: text}
	}
	if len(result.Content) == 0 {
		return mcpToolResponse{}
	}
	return mcpToolResponse{Result: json.RawMessage(result.Content[0].Text)}
}

func TestRun_EndToEndWhoAmI(t *testing.T) {
	coord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req mcpRpcRequest
		json.NewDecoder(r.Body).Decode(&req)
		out := map[string]any{
			"agent_id": "agent-1", "owner": "harley", "model": "claude-sonnet-5",
			"capabilities": []string{"code"}, "project_id": "project-1", "permissions": []string{"read_kb"},
		}
		outRaw, _ := json.Marshal(out)
		result := map[string]any{"content": []map[string]string{{"type": "text", "text": string(outRaw)}}}
		resultRaw, _ := json.Marshal(result)
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": json.RawMessage(resultRaw)}
		json.NewEncoder(w).Encode(resp)
	}))
	defer coord.Close()

	home := t.TempDir()
	os.Setenv("HOME", home)
	defer os.Unsetenv("HOME")
	runDir := filepath.Join(home, "run")
	os.Setenv("XDG_RUNTIME_DIR", runDir)
	defer os.Unsetenv("XDG_RUNTIME_DIR")
	dataDir := filepath.Join(home, "data")
	os.Setenv("XDG_DATA_HOME", dataDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	credDir := filepath.Join(home, ".wormhole", "credentials")
	os.MkdirAll(credDir, 0o700)
	credData, _ := json.Marshal(map[string]string{
		"server": coord.URL, "project_id": "project-1", "agent_id": "agent-1", "token": "test-token",
	})
	os.WriteFile(filepath.Join(credDir, "default.json"), credData, 0o600)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, "default") }()

	socketPath := filepath.Join(runDir, "wormhole", "wormholed.sock")
	var conn net.Conn
	var err error
	for i := 0; i < 100; i++ {
		conn, err = net.Dial("unix", socketPath)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		cancel()
		t.Fatalf("dial socket: %v", err)
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)
	mcpInitialize(t, conn, reader)

	resp := mcpCallTool(t, conn, reader, 2, "wormhole.agent.whoami", nil)
	if resp.Error != "" {
		cancel()
		t.Fatalf("got error: %s", resp.Error)
	}
	var out struct {
		AgentID string `json:"agent_id"`
	}
	json.Unmarshal(resp.Result, &out)
	if out.AgentID != "agent-1" {
		cancel()
		t.Fatalf("got agent_id %q, want agent-1", out.AgentID)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned error after cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not shut down after context cancel")
	}
}

// TestRun_AgentRegisterReachesSchedulerAndEventBus asserts that Run wires a
// real scheduler.Scheduler and eventbus.EventBus into the localapi.Server it
// starts (P3), rather than the plain localapi.New(...) call that leaves
// sched/eb nil and makes wormhole.agent.register fail with "scheduler not
// available" (internal/runtime/localapi/localapi.go:657-658). A single
// credential profile should resolve to single-org NewWithRuntime wiring.
func TestRun_AgentRegisterReachesSchedulerAndEventBus(t *testing.T) {
	coord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "result": json.RawMessage(`{}`)})
	}))
	defer coord.Close()

	home := t.TempDir()
	os.Setenv("HOME", home)
	defer os.Unsetenv("HOME")
	runDir := filepath.Join(home, "run")
	os.Setenv("XDG_RUNTIME_DIR", runDir)
	defer os.Unsetenv("XDG_RUNTIME_DIR")
	dataDir := filepath.Join(home, "data")
	os.Setenv("XDG_DATA_HOME", dataDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	credDir := filepath.Join(home, ".wormhole", "credentials")
	os.MkdirAll(credDir, 0o700)
	credData, _ := json.Marshal(map[string]string{
		"server": coord.URL, "project_id": "project-1", "agent_id": "agent-1", "token": "test-token",
	})
	os.WriteFile(filepath.Join(credDir, "default.json"), credData, 0o600)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, "default") }()

	socketPath := filepath.Join(runDir, "wormhole", "wormholed.sock")
	var conn net.Conn
	var err error
	for i := 0; i < 100; i++ {
		conn, err = net.Dial("unix", socketPath)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		cancel()
		t.Fatalf("dial socket: %v", err)
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)
	mcpInitialize(t, conn, reader)

	resp := mcpCallTool(t, conn, reader, 2, "wormhole.agent.register", map[string]interface{}{
		"agent_id":     "agent-1",
		"capabilities": []string{"code"},
	})
	if resp.Error != "" {
		cancel()
		t.Fatalf("wormhole.agent.register returned error: %s", resp.Error)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned error after cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not shut down after context cancel")
	}
}
