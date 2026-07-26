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
	"sync/atomic"
	"testing"

	"github.com/H4RL33/wormhole/internal/runtime/localapi"
)

func fakeEnrolmentGateway(t *testing.T, out localapi.EnrolmentResult) <-chan localapi.EnrolmentRequest {
	t.Helper()
	runtimeDir, err := os.MkdirTemp("", "wh-")
	if err != nil {
		t.Fatalf("create short runtime dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	socketPath := filepath.Join(runtimeDir, "wormhole", "wormholed.sock")
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	t.Cleanup(func() { listener.Close() })
	received := make(chan localapi.EnrolmentRequest, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var initialize rpcRequest
		if json.Unmarshal(bytes.TrimSpace(line), &initialize) != nil || initialize.Method != "initialize" {
			return
		}
		initializeResult, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: initialize.ID, Result: json.RawMessage(`{}`)})
		_, _ = conn.Write(append(initializeResult, '\n'))
		if _, err := reader.ReadBytes('\n'); err != nil { // notifications/initialized
			return
		}
		line, err = reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var call rpcRequest
		if json.Unmarshal(bytes.TrimSpace(line), &call) != nil || call.Method != "tools/call" {
			return
		}
		var params toolsCallParams
		if json.Unmarshal(call.Params, &params) != nil || params.Name != localapi.EnrolmentToolName {
			return
		}
		var request localapi.EnrolmentRequest
		if json.Unmarshal(params.Arguments, &request) != nil {
			return
		}
		received <- request
		if out.IdempotencyKey == "" {
			out.IdempotencyKey = request.IdempotencyKey
		}
		out.CredentialProfile = request.CredentialProfile
		outRaw, _ := json.Marshal(out)
		toolResult, _ := json.Marshal(toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: string(outRaw)}}})
		response, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: call.ID, Result: toolResult})
		_, _ = conn.Write(append(response, '\n'))
	}()
	return received
}

func persistedEnrolmentResult() localapi.EnrolmentResult {
	return localapi.EnrolmentResult{
		Version: localapi.EnrolmentProtocolVersion, Code: localapi.EnrolmentCredentialsPersistedResult,
		State:     localapi.EnrolmentCredentialsPersisted,
		Retryable: true, AgentID: "agent-gateway", PassportID: "passport-gateway", CredentialProfile: "project-1__contributor",
	}
}

func TestRunJoinAcceptsGatewayResumedAttemptKey(t *testing.T) {
	result := persistedEnrolmentResult()
	result.IdempotencyKey = "318f47a2-7b1d-7e42-8d4b-1c99c6a8f2b1"
	fakeEnrolmentGateway(t, result)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"join", "--server", "https://fabric.example", "--project", "project-1", "--owner", "harley", "--profile", "project-1__contributor",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "credentials persisted by gatewayd") {
		t.Fatalf("stdout missing persistence confirmation: %q", stdout.String())
	}
}

func TestRunJoinDelegatesOnlyToGateway(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	received := fakeEnrolmentGateway(t, persistedEnrolmentResult())
	var fabricCalls atomic.Int32
	fabric := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		fabricCalls.Add(1)
	}))
	defer fabric.Close()

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"join", "--server", fabric.URL, "--project", "project-1", "--owner", "harley", "--model", "gpt-5",
		"--capabilities", "code", "--roles", "contributor", "--permissions", "task.create", "--profile", "project-1__contributor",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, stderr.String())
	}
	request := <-received
	if request.Version != localapi.EnrolmentProtocolVersion || request.ProjectID != "project-1" || request.FabricAddress != fabric.URL ||
		request.CredentialProfile != "project-1__contributor" || request.IdempotencyKey == "" {
		t.Fatalf("Gateway request = %+v", request)
	}
	if fabricCalls.Load() != 0 {
		t.Fatalf("CLI made %d direct Fabric calls, want 0", fabricCalls.Load())
	}
	if _, err := os.Stat(filepath.Join(home, ".wormhole", "credentials", "project-1__contributor.json")); !os.IsNotExist(err) {
		t.Fatalf("CLI wrote credential profile: %v", err)
	}
	for _, want := range []string{"agent_id=agent-gateway", "passport_id=passport-gateway", "credentials persisted by gatewayd"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q: %q", want, stdout.String())
		}
	}
}

func TestRunJoinDoesNotFallbackToFabricWhenGatewayIsUnavailable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "missing-runtime"))
	var fabricCalls atomic.Int32
	fabric := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { fabricCalls.Add(1) }))
	defer fabric.Close()
	var stdout, stderr bytes.Buffer
	code := run([]string{"join", "--server", fabric.URL, "--project", "project-1", "--owner", "harley", "--profile", "profile"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "gatewayd not running") {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if fabricCalls.Load() != 0 {
		t.Fatalf("CLI made %d fallback Fabric calls, want 0", fabricCalls.Load())
	}
}

func TestRunJoinRejectsLegacyTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{"join", "--server", "https://fabric.example", "--project", "project-1", "--owner", "harley", "--token-file", path}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "--token-file is no longer supported") {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("legacy token path was written: %v", err)
	}
}

func TestRunConnectDelegatesOnlyToGatewayThenWiresHarness(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	received := fakeEnrolmentGateway(t, persistedEnrolmentResult())
	var fabricCalls atomic.Int32
	fabric := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { fabricCalls.Add(1) }))
	defer fabric.Close()
	openCodeConfig := filepath.Join(t.TempDir(), "opencode.json")

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"connect", "--server", fabric.URL, "--project", "project-1", "--owner", "harley",
		"--roles", "contributor", "--permissions", "task.create", "--profile", "project-1__contributor",
		"--target", "opencode", "--opencode-config", openCodeConfig, "--stdio-bin", os.Args[0],
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	request := <-received
	if request.ProjectID != "project-1" || request.FabricAddress != fabric.URL || request.CredentialProfile != "project-1__contributor" {
		t.Fatalf("Gateway request = %+v", request)
	}
	if fabricCalls.Load() != 0 {
		t.Fatalf("CLI made %d direct Fabric calls, want 0", fabricCalls.Load())
	}
	if _, err := os.Stat(filepath.Join(home, ".wormhole", "credentials", "project-1__contributor.json")); !os.IsNotExist(err) {
		t.Fatalf("CLI wrote credential profile: %v", err)
	}
	if _, err := os.Stat(openCodeConfig); err != nil {
		t.Fatalf("connector config was not written: %v", err)
	}
}

func TestRunConnectRejectsLegacyTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{"connect", "--server", "https://fabric.example", "--project", "project-1", "--owner", "harley", "--token-file", path}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "--token-file is no longer supported") {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
}
