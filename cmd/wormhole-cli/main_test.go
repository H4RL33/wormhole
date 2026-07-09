package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRun_NoArgs_PrintsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code: got %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "usage: wormhole") {
		t.Fatalf("stderr missing usage text: %q", stderr.String())
	}
}

func TestRun_UnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"bogus"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code: got %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), `unknown command "bogus"`) {
		t.Fatalf("stderr missing unknown-command text: %q", stderr.String())
	}
}

func TestRunJoin_MissingRequiredFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"join"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code: got %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--server and --project are required") {
		t.Fatalf("stderr missing required-flags text: %q", stderr.String())
	}
}

func TestRunJoin_MissingProjectOnly(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"join", "--server", "http://localhost:8080"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code: got %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--server and --project are required") {
		t.Fatalf("stderr missing required-flags text: %q", stderr.String())
	}
}

// TestRunJoin_Success_RegistersAndPersistsCredentials drives runJoin
// against a fake /mcp/tools/call server, asserting both the outbound
// request shape (matches internal/mcp.RegisterAgentInput's JSON tags, and
// permissions is never nil) and that a successful response is persisted
// to the credentials file with 0600 permissions.
func TestRunJoin_Success_RegistersAndPersistsCredentials(t *testing.T) {
	issuedAt := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp/tools/call" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var req callRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Tool != "wormhole.agent.register" {
			t.Fatalf("tool: got %q, want wormhole.agent.register", req.Tool)
		}
		if req.ProjectID != "proj-1" {
			t.Fatalf("project_id: got %q, want proj-1", req.ProjectID)
		}
		var in registerAgentInput
		if err := json.Unmarshal(req.Arguments, &in); err != nil {
			t.Fatalf("decode arguments: %v", err)
		}
		if in.Permissions == nil {
			t.Fatal("permissions: got nil, want non-nil (identity.Store.Register rejects nil permissions)")
		}
		if len(in.Capabilities) != 1 || in.Capabilities[0] != "code" {
			t.Fatalf("capabilities: got %v, want [code]", in.Capabilities)
		}
		out := registerAgentOutput{
			AgentID:      "agent-1",
			PassportID:   "passport-1",
			Token:        "sekrit-token",
			Repositories: []string{},
			Roles:        []string{},
			IssuedAt:     issuedAt,
		}
		resultRaw, _ := json.Marshal(out)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(callResponse{Result: resultRaw})
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
		"--permissions", "task.create,kb.write",
		"--token-file", tokenFile,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}

	out := stdout.String()
	for _, want := range []string{"Passport created.", "agent_id=agent-1", "passport_id=passport-1", "project=proj-1", tokenFile} {
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
	if creds.Token != "sekrit-token" || creds.AgentID != "agent-1" || creds.PassportID != "passport-1" || creds.ProjectID != "proj-1" || creds.Server != srv.URL {
		t.Fatalf("credentials: got %+v", creds)
	}

	info, err := os.Stat(tokenFile)
	if err != nil {
		t.Fatalf("stat credentials file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials file mode: got %o, want 0600", info.Mode().Perm())
	}
}

// TestRunJoin_ServerError_PrintsError confirms a tool-level rejection
// (HTTP 400 + CallResponse.Error, per internal/mcp/server.go) surfaces to
// stderr and does not write a credentials file.
func TestRunJoin_ServerError_PrintsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(callResponse{Error: `{"error":"identity: invalid scope","code":"INVALID_SCOPE"}`})
	}))
	defer srv.Close()

	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"join", "--server", srv.URL, "--project", "proj-1", "--permissions", "task.create",
		"--token-file", tokenFile,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code: got %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "invalid scope") {
		t.Fatalf("stderr missing server error text: %q", stderr.String())
	}
	if _, err := os.Stat(tokenFile); !os.IsNotExist(err) {
		t.Fatalf("credentials file should not have been written on error")
	}
}

// TestRunJoin_NetworkError_PrintsError confirms an unreachable server
// surfaces a clean error instead of a panic or a silent empty exit.
func TestRunJoin_NetworkError_PrintsError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"join", "--server", "http://127.0.0.1:1", "--project", "proj-1", "--permissions", "task.create",
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code: got %d, want 1", code)
	}
	if stderr.String() == "" {
		t.Fatalf("expected stderr to contain network error, got empty")
	}
}

func TestDefaultTokenFilePath_UnderWormholeDir(t *testing.T) {
	path, err := defaultTokenFilePath()
	if err != nil {
		t.Fatalf("defaultTokenFilePath: %v", err)
	}
	want := filepath.Join(".wormhole", "credentials.json")
	if !strings.HasSuffix(path, want) {
		t.Fatalf("path: got %q, want suffix %q", path, want)
	}
}
