package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func fakeStatusGateway(t *testing.T, state string, pending int) <-chan json.RawMessage {
	t.Helper()
	runtimeDir, err := os.MkdirTemp("", "wh-status-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	socketPath := filepath.Join(runtimeDir, "wormhole", "wormholed.sock")
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	received := make(chan json.RawMessage, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		line, _ := reader.ReadBytes('\n')
		var init rpcRequest
		_ = json.Unmarshal(bytes.TrimSpace(line), &init)
		response, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: init.ID, Result: json.RawMessage(`{}`)})
		_, _ = conn.Write(append(response, '\n'))
		_, _ = reader.ReadBytes('\n')
		line, _ = reader.ReadBytes('\n')
		var call rpcRequest
		_ = json.Unmarshal(bytes.TrimSpace(line), &call)
		var params toolsCallParams
		_ = json.Unmarshal(call.Params, &params)
		if params.Name != "wormhole.sync.status" {
			return
		}
		received <- params.Arguments
		resultText, _ := json.Marshal(map[string]any{"state": state, "pending_writes": pending})
		result, _ := json.Marshal(toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: string(resultText)}}})
		response, _ = json.Marshal(rpcResponse{JSONRPC: "2.0", ID: call.ID, Result: result})
		_, _ = conn.Write(append(response, '\n'))
	}()
	return received
}

func TestRunStatusUsesProfileProjectAndPrintsLocalState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeTestCredentials(t, filepath.Join(home, ".wormhole", "credentials"), "offline-profile", credentials{ProjectID: "project-offline"})
	received := fakeStatusGateway(t, "offline", 3)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"status", "--profile", "offline-profile"}, &stdout, &stderr); code != 0 {
		t.Fatalf("status exit=%d stderr=%q", code, stderr.String())
	}
	if got, want := stdout.String(), "state=offline pending_writes=3\n"; got != want {
		t.Fatalf("stdout=%q want=%q", got, want)
	}
	var args struct {
		ProjectID string `json:"project_id"`
	}
	if err := json.Unmarshal(<-received, &args); err != nil || args.ProjectID != "project-offline" {
		t.Fatalf("status args=%+v err=%v", args, err)
	}
}

func TestRunStatusRejectsMissingProfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := run([]string{"status", "--profile", "missing"}, &stdout, &stderr); code != 1 {
		t.Fatalf("status exit=%d stderr=%q", code, stderr.String())
	}
}
