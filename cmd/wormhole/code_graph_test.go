package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localapi"
)

func TestCodeGraphCommandsAreRegistered(t *testing.T) {
	commands := [][]string{
		{"config", "code-graph", "enable", "--help"},
		{"config", "code-graph", "disable", "--help"},
		{"config", "code-graph", "status", "--help"},
		{"config", "code-graph", "rebuild", "--help"},
		{"config", "code-graph", "checkout", "set", "--help"},
		{"config", "code-graph", "checkout", "show", "--help"},
	}
	for _, args := range commands {
		t.Run(strings.Join(args[:len(args)-1], " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(args, &stdout, &stderr); code != 0 {
				t.Fatalf("run(%v) = %d, stderr=%q", args, code, stderr.String())
			}
		})
	}
}

func TestCodeGraphNoninteractiveConfirmInvokesEnableAndDisable(t *testing.T) {
	var operations []localapi.CodeGraphLifecycleOperation
	call := func(_ context.Context, request localapi.CodeGraphLifecycleRequest) (localapi.CodeGraphLifecycleStatus, error) {
		operations = append(operations, request.Operation)
		return localapi.CodeGraphLifecycleStatus{ProjectID: request.ProjectID}, nil
	}
	for _, args := range [][]string{{"enable", "--project", "project-a", "--confirm"}, {"disable", "--project", "project-a", "--confirm"}} {
		var stdout, stderr bytes.Buffer
		if code := runConfigCodeGraph(args, strings.NewReader(""), &stdout, &stderr, false, call); code != 0 {
			t.Fatalf("runConfigCodeGraph(%v) = %d, stderr=%q", args, code, stderr.String())
		}
	}
	if len(operations) != 2 || operations[0] != localapi.CodeGraphEnable || operations[1] != localapi.CodeGraphDisable {
		t.Fatalf("operations = %v", operations)
	}
}

func TestCodeGraphCheckoutSetRequiresExactlyOnePath(t *testing.T) {
	called := false
	call := func(context.Context, localapi.CodeGraphLifecycleRequest) (localapi.CodeGraphLifecycleStatus, error) {
		called = true
		return localapi.CodeGraphLifecycleStatus{}, nil
	}
	for _, args := range [][]string{
		{"checkout", "set", "--project", "project-a", "--confirm"},
		{"checkout", "set", "--project", "project-a", "--confirm", "/one", "/two"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runConfigCodeGraph(args, strings.NewReader(""), &stdout, &stderr, false, call); code != 2 {
			t.Fatalf("runConfigCodeGraph(%v) = %d, want 2", args, code)
		}
	}
	if called {
		t.Fatal("invalid checkout operands invoked lifecycle")
	}
}

func TestCodeGraphDispatchesRebuildAndCheckoutSet(t *testing.T) {
	var requests []localapi.CodeGraphLifecycleRequest
	call := func(_ context.Context, request localapi.CodeGraphLifecycleRequest) (localapi.CodeGraphLifecycleStatus, error) {
		requests = append(requests, request)
		return localapi.CodeGraphLifecycleStatus{ProjectID: request.ProjectID}, nil
	}
	for _, args := range [][]string{
		{"rebuild", "--project", "project-a", "--confirm"},
		{"checkout", "set", "--project", "project-a", "--confirm", "/new-checkout"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runConfigCodeGraph(args, strings.NewReader(""), &stdout, &stderr, false, call); code != 0 {
			t.Fatalf("runConfigCodeGraph(%v) = %d, stderr=%q", args, code, stderr.String())
		}
	}
	if len(requests) != 2 || requests[0].Operation != localapi.CodeGraphRebuild ||
		requests[1].Operation != localapi.CodeGraphCheckoutSet || requests[1].Checkout != "/new-checkout" {
		t.Fatalf("requests = %+v", requests)
	}
}

func TestCodeGraphProjectDefaultsToNearestProjectConfig(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, ".wormhole")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("project = \"nearest-project\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(before) })
	call := func(_ context.Context, request localapi.CodeGraphLifecycleRequest) (localapi.CodeGraphLifecycleStatus, error) {
		if request.ProjectID != "nearest-project" {
			t.Fatalf("ProjectID = %q", request.ProjectID)
		}
		return localapi.CodeGraphLifecycleStatus{ProjectID: request.ProjectID}, nil
	}
	var stdout, stderr bytes.Buffer
	if code := runConfigCodeGraph([]string{"status"}, strings.NewReader(""), &stdout, &stderr, false, call); code != 0 {
		t.Fatalf("status exit = %d, stderr=%q", code, stderr.String())
	}
}

func TestCodeGraphMutationConfirmationAndResourceWarning(t *testing.T) {
	calls := 0
	call := func(_ context.Context, request localapi.CodeGraphLifecycleRequest) (localapi.CodeGraphLifecycleStatus, error) {
		calls++
		return localapi.CodeGraphLifecycleStatus{Enabled: true}, nil
	}

	var stdout, stderr bytes.Buffer
	if code := runConfigCodeGraph([]string{"enable", "--project", "project-a"}, strings.NewReader(""), &stdout, &stderr, false, call); code != 2 {
		t.Fatalf("noninteractive enable exit = %d, want 2", code)
	}
	if calls != 0 || !strings.Contains(stderr.String(), "--confirm") {
		t.Fatalf("noninteractive enable calls=%d stderr=%q", calls, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runConfigCodeGraph([]string{"enable", "--project", "project-a"}, strings.NewReader("yes\n"), &stdout, &stderr, true, call); code != 0 {
		t.Fatalf("interactive enable exit = %d, stderr=%q", code, stderr.String())
	}
	for _, phrase := range []string{"local", "experimental", "CPU", "memory", "disk", "I/O"} {
		if !strings.Contains(stdout.String(), phrase) {
			t.Errorf("interactive explanation missing %q: %q", phrase, stdout.String())
		}
	}

	stdout.Reset()
	stderr.Reset()
	if code := runConfigCodeGraph([]string{"disable", "--project", "project-a"}, strings.NewReader("no\n"), &stdout, &stderr, true, call); code != 1 {
		t.Fatalf("declined disable exit = %d, want 1", code)
	}
	for _, phrase := range []string{"completed revisions", "candidate revisions", "nodes", "files", "symbols", "edges", "diagnostics", "project Code Graph configuration", "Git", "working tree"} {
		if !strings.Contains(stdout.String(), phrase) {
			t.Errorf("disable warning missing %q: %q", phrase, stdout.String())
		}
	}
	if calls != 1 {
		t.Fatalf("disable confirmation calls=%d stdout=%q", calls, stdout.String())
	}
}

func TestCodeGraphStatusAndCheckoutShowDoNotRequireConfirmation(t *testing.T) {
	calls := 0
	call := func(_ context.Context, request localapi.CodeGraphLifecycleRequest) (localapi.CodeGraphLifecycleStatus, error) {
		calls++
		return localapi.CodeGraphLifecycleStatus{ProjectID: request.ProjectID, ActiveCheckout: "/repo"}, nil
	}
	for _, args := range [][]string{{"status", "--project", "project-a"}, {"checkout", "show", "--project", "project-a"}} {
		var stdout, stderr bytes.Buffer
		if code := runConfigCodeGraph(args, strings.NewReader(""), &stdout, &stderr, false, call); code != 0 {
			t.Fatalf("runConfigCodeGraph(%v) = %d, stderr=%q", args, code, stderr.String())
		}
	}
	if calls != 2 {
		t.Fatalf("read-only lifecycle calls = %d, want 2", calls)
	}
}

func TestExecuteCodeGraphLifecycleUsesPrivateRPCWithClosedClaims(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "run"))
	socketPath := gatewaySocketPath()
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	type capturedCall struct {
		method string
		params map[string]json.RawMessage
	}
	calls := make(chan capturedCall, 6)
	serverErr := make(chan error, 1)
	go func() {
		for range 6 {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				serverErr <- acceptErr
				return
			}
			reader := bufio.NewReader(conn)
			readRequest := func() (rpcRequest, error) {
				line, readErr := reader.ReadBytes('\n')
				if readErr != nil {
					return rpcRequest{}, readErr
				}
				var request rpcRequest
				readErr = json.Unmarshal(bytes.TrimSpace(line), &request)
				return request, readErr
			}
			initialize, readErr := readRequest()
			if readErr != nil || initialize.Method != "initialize" {
				_ = conn.Close()
				serverErr <- errors.New("missing Gateway initialize request")
				return
			}
			response, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: initialize.ID, Result: json.RawMessage(`{}`)})
			if _, writeErr := conn.Write(append(response, '\n')); writeErr != nil {
				_ = conn.Close()
				serverErr <- writeErr
				return
			}
			initialized, readErr := readRequest()
			if readErr != nil || initialized.Method != "notifications/initialized" {
				_ = conn.Close()
				serverErr <- errors.New("missing Gateway initialized notification")
				return
			}
			request, readErr := readRequest()
			if readErr != nil {
				_ = conn.Close()
				serverErr <- readErr
				return
			}
			var params map[string]json.RawMessage
			if readErr = json.Unmarshal(request.Params, &params); readErr != nil {
				_ = conn.Close()
				serverErr <- readErr
				return
			}
			calls <- capturedCall{method: request.Method, params: params}
			result, _ := json.Marshal(localapi.CodeGraphLifecycleStatus{ProjectID: "project-a", ActiveCheckout: "/checkout"})
			response, _ = json.Marshal(rpcResponse{JSONRPC: "2.0", ID: request.ID, Result: result})
			_, writeErr := conn.Write(append(response, '\n'))
			_ = conn.Close()
			if writeErr != nil {
				serverErr <- writeErr
				return
			}
		}
		serverErr <- nil
	}()

	requests := []localapi.CodeGraphLifecycleRequest{
		{Operation: localapi.CodeGraphEnable, ProjectID: "project-a", Checkout: "/enable"},
		{Operation: localapi.CodeGraphDisable, ProjectID: "project-a"},
		{Operation: localapi.CodeGraphStatus, ProjectID: "project-a"},
		{Operation: localapi.CodeGraphRebuild, ProjectID: "project-a"},
		{Operation: localapi.CodeGraphCheckoutSet, ProjectID: "project-a", Checkout: "/set"},
		{Operation: localapi.CodeGraphCheckoutShow, ProjectID: "project-a"},
	}
	for _, request := range requests {
		if _, callErr := executeCodeGraphLifecycle(context.Background(), request); callErr != nil {
			t.Errorf("execute %s: %v", request.Operation, callErr)
		}
	}

	for _, request := range requests {
		select {
		case captured := <-calls:
			if captured.method != "wormhole/code-graph/lifecycle" {
				t.Errorf("%s method = %q", request.Operation, captured.method)
			}
			wantKeys := map[string]bool{"operation": true, "project_id": true}
			if request.Checkout != "" {
				wantKeys["checkout"] = true
			}
			if len(captured.params) != len(wantKeys) {
				t.Errorf("%s params = %s, want only operation/project_id/optional checkout", request.Operation, requestJSON(captured.params))
			}
			for key := range captured.params {
				if !wantKeys[key] {
					t.Errorf("%s sent forbidden wire claim %q", request.Operation, key)
				}
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s did not call the private Gateway RPC", request.Operation)
		}
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func requestJSON(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func TestCodeGraphRejectsForbiddenKnobs(t *testing.T) {
	call := func(context.Context, localapi.CodeGraphLifecycleRequest) (localapi.CodeGraphLifecycleStatus, error) {
		return localapi.CodeGraphLifecycleStatus{}, errors.New("must not be called")
	}
	for _, args := range [][]string{
		{"rebuild", "--project", "project-a", "--confirm", "--warpspeed"},
		{"rebuild", "--project", "project-a", "--confirm", "--in-place"},
		{"pause", "--project", "project-a"},
		{"resume", "--project", "project-a"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runConfigCodeGraph(args, strings.NewReader(""), &stdout, &stderr, false, call); code != 2 {
			t.Fatalf("forbidden args %v exit = %d, want 2", args, code)
		}
	}
}
