package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRun_EndToEndWhoAmI(t *testing.T) {
	coord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		type rpcRequest struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id,omitempty"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params,omitempty"`
		}
		var req rpcRequest
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

	reqRaw, _ := json.Marshal(map[string]string{"tool": "wormhole.agent.whoami"})
	conn.Write(append(reqRaw, '\n'))

	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		cancel()
		t.Fatalf("decode response: %v", err)
	}
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

	argsRaw, _ := json.Marshal(map[string]any{"agent_id": "agent-1", "capabilities": []string{"code"}})
	reqRaw, _ := json.Marshal(map[string]any{"tool": "wormhole.agent.register", "args": json.RawMessage(argsRaw)})
	conn.Write(append(reqRaw, '\n'))

	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		cancel()
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != "" {
		cancel()
		t.Fatalf("wormhole.agent.register returned error: %s", resp.Error)
	}
	if strings.Contains(resp.Error, "scheduler not available") {
		cancel()
		t.Fatalf("scheduler unreachable from Run: %s", resp.Error)
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
