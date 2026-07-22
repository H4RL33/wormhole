// Tests the socket-based join registration path.
package main

import (
	"bufio"
	"bytes"
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

// fakeWormholed starts a fake wormholed local socket at the path
// wormholedSocketPath() would derive under XDG_RUNTIME_DIR (set by the
// caller via t.Setenv before calling this), and speaks the real MCP
// handshake (initialize -> notifications/initialized -> tools/call) that
// doRegisterViaSocket now uses (RFC-0003 §8.1 join proxy).
// Answers exactly one wormhole.agent.register tools/call with a canned
// result. Returns the socket path.
func fakeWormholed(t *testing.T, out registerAgentOutput) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "runtime")
	t.Setenv("XDG_RUNTIME_DIR", dir)
	socketPath := filepath.Join(dir, "wormhole", "wormholed.sock")
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)

		// initialize
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var initReq rpcRequest
		if err := json.Unmarshal(bytes.TrimSpace(line), &initReq); err != nil || initReq.Method != "initialize" {
			return
		}
		initResp, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: initReq.ID, Result: json.RawMessage(`{}`)})
		conn.Write(append(initResp, '\n'))

		// notifications/initialized (no response)
		line, err = reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var notif rpcRequest
		if err := json.Unmarshal(bytes.TrimSpace(line), &notif); err != nil || notif.Method != "notifications/initialized" {
			return
		}

		// tools/call
		line, err = reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var callReq rpcRequest
		if err := json.Unmarshal(bytes.TrimSpace(line), &callReq); err != nil || callReq.Method != "tools/call" {
			return
		}
		var params toolsCallParams
		if err := json.Unmarshal(callReq.Params, &params); err != nil {
			return
		}
		if params.Name != "wormhole.agent.register" {
			result, _ := json.Marshal(toolCallResult{
				Content: []toolCallResultContent{{Type: "text", Text: "unexpected tool: " + params.Name}},
				IsError: true,
			})
			resp, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: callReq.ID, Result: result})
			conn.Write(append(resp, '\n'))
			return
		}
		outRaw, _ := json.Marshal(out)
		result, _ := json.Marshal(toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: string(outRaw)}}})
		resp, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: callReq.ID, Result: result})
		conn.Write(append(resp, '\n'))
	}()

	return socketPath
}

// TestRunJoin_WormholedRunning_UsesLocalSocket proves RFC-0003 §8.1: when
// wormholed's local socket is reachable, `wormhole join` registers through
// it instead of calling the Coordination Server's wormhole.agent.register
// directly. The httptest server below fails the test if wormhole.agent.register
// is called on it, proving the socket path was used exclusively for step 1.
func TestRunJoin_WormholedRunning_UsesLocalSocket(t *testing.T) {
	issuedAt := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	fakeWormholed(t, registerAgentOutput{
		AgentID:    "agent-socket",
		PassportID: "passport-socket",
		Token:      "socket-token",
		IssuedAt:   issuedAt,
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		var params toolsCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("decode params: %v", err)
		}
		if params.Name == "wormhole.agent.register" {
			t.Fatal("wormhole.agent.register called directly on Coordination Server; should have gone through wormholed's local socket")
		}
		switch params.Name {
		case "wormhole.kb.search":
			if got := r.Header.Get("Authorization"); got != "Bearer socket-token" {
				t.Fatalf("kb.search Authorization: got %q, want Bearer socket-token", got)
			}
			out, _ := json.Marshal(searchArticlesOutput{Articles: []articleSummary{}})
			result, _ := json.Marshal(toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: string(out)}}})
			json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
		case "wormhole.channel.list":
			out, _ := json.Marshal(listChannelsOutput{Channels: nil})
			result, _ := json.Marshal(toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: string(out)}}})
			json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
		case "wormhole.task.list":
			out, _ := json.Marshal(listTasksOutput{Tasks: nil})
			result, _ := json.Marshal(toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: string(out)}}})
			json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
		default:
			t.Fatalf("unexpected tool: %s", params.Name)
		}
	}))
	defer srv.Close()

	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"join",
		"--server", srv.URL,
		"--project", "proj-1",
		"--owner", "harley",
		"--model", "claude",
		"--capabilities", "code",
		"--permissions", "task.create",
		"--token-file", tokenFile,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}

	out := stdout.String()
	for _, want := range []string{"agent_id=agent-socket", "passport_id=passport-socket"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q: got %q", want, out)
		}
	}

	data, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatalf("read credentials file: %v", err)
	}
	var creds credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		t.Fatalf("decode credentials file: %v", err)
	}
	if creds.Token != "socket-token" || creds.AgentID != "agent-socket" {
		t.Fatalf("credentials: got %+v", creds)
	}
}

// TestRunJoin_WormholedNotRunning_FallsBackToDirectServer proves RFC-0003
// doesn't mandate wormholed's availability (§3.2 NG2/§6.1 pattern): with no
// socket reachable at the derived XDG path, join falls back to the existing
// direct-to-Coordination-Server path, unchanged.
func TestRunJoin_WormholedNotRunning_FallsBackToDirectServer(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "runtime"))

	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		return searchArticlesOutput{Articles: []articleSummary{}}, nil
	})
	defer srv.Close()

	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"join",
		"--server", srv.URL,
		"--project", "proj-1",
		"--owner", "harley",
		"--model", "claude",
		"--capabilities", "code",
		"--permissions", "task.create",
		"--token-file", tokenFile,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "agent_id=agent-1") {
		t.Fatalf("stdout missing direct-path result: got %q", stdout.String())
	}
}
