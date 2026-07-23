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

func TestCallTool_ReportsProtocolFailures(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "json rpc error",
			body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"denied"}}`,
			want: "denied",
		},
		{
			name: "malformed response",
			body: `not json`,
			want: "decode response",
		},
		{
			name: "malformed tool result",
			body: `{"jsonrpc":"2.0","id":1,"result":"not-a-tool-result"}`,
			want: "decode tools/call result",
		},
		{
			name: "empty tool content",
			body: `{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`,
			want: "empty tool result content",
		},
		{
			name: "tool reported error",
			body: `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"validation failed"}],"isError":true}}`,
			want: "validation failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/mcp" {
					t.Fatalf("request path = %q, want /mcp", r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer token" {
					t.Fatalf("authorization = %q, want bearer token", got)
				}
				var request rpcRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatalf("decode request: %v", err)
				}
				var params toolsCallParams
				if err := json.Unmarshal(request.Params, &params); err != nil {
					t.Fatalf("decode params: %v", err)
				}
				var args map[string]any
				if err := json.Unmarshal(params.Arguments, &args); err != nil {
					t.Fatalf("decode arguments: %v", err)
				}
				if got := args["project_id"]; got != "project-a" {
					t.Fatalf("project_id = %v, want project-a", got)
				}
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			_, err := callTool(srv.Client(), srv.URL+"/", "wormhole.task.list", "project-a", "token", struct{}{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("callTool error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestCallTool_RejectsNonObjectArguments(t *testing.T) {
	_, err := callTool(http.DefaultClient, "https://example.invalid", "wormhole.task.list", "project-a", "", []string{"not", "an", "object"})
	if err == nil || !strings.Contains(err.Error(), "decode wormhole.task.list arguments") {
		t.Fatalf("callTool error = %v, want non-object argument error", err)
	}
}

func TestDoRegisterViaSocket_ReportsReachableProtocolFailures(t *testing.T) {
	tests := []struct {
		name    string
		initial string
		call    string
		want    string
	}{
		{
			name:    "initialize error",
			initial: `{"jsonrpc":"2.0","id":1,"error":{"code":-1,"message":"not ready"}}`,
			want:    "initialize: not ready",
		},
		{
			name:    "malformed initialize response",
			initial: `not json`,
			want:    "decode initialize response",
		},
		{
			name:    "tool call error",
			initial: `{"jsonrpc":"2.0","id":1,"result":{}}`,
			call:    `{"jsonrpc":"2.0","id":2,"error":{"code":-1,"message":"registration denied"}}`,
			want:    "registration denied",
		},
		{
			name:    "empty tool result",
			initial: `{"jsonrpc":"2.0","id":1,"result":{}}`,
			call:    `{"jsonrpc":"2.0","id":2,"result":{"content":[]}}`,
			want:    "empty register result",
		},
		{
			name:    "tool reported error",
			initial: `{"jsonrpc":"2.0","id":1,"result":{}}`,
			call:    `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"bad request"}],"isError":true}}`,
			want:    "bad request",
		},
		{
			name:    "malformed registration payload",
			initial: `{"jsonrpc":"2.0","id":1,"result":{}}`,
			call:    `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"not-json"}]}}`,
			want:    "decode register result",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			socketPath := registerFailureSocket(t, tt.initial, tt.call)
			_, reachable, err := doRegisterViaSocket(socketPath, "project-a", registerAgentInput{})
			if !reachable {
				t.Fatal("reachable = false, want true for accepted socket")
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("doRegisterViaSocket error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func registerFailureSocket(t *testing.T, initial, call string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wormholed.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		if _, err := reader.ReadBytes('\n'); err != nil {
			return
		}
		_, _ = conn.Write(append([]byte(initial), '\n'))
		if call == "" {
			return
		}
		if _, err := reader.ReadBytes('\n'); err != nil {
			return
		}
		if _, err := reader.ReadBytes('\n'); err != nil {
			return
		}
		_, _ = conn.Write(append([]byte(call), '\n'))
	}()
	return path
}

func TestCredentialProfiles_RejectMalformedCredentials(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("not json"), 0o600); err != nil {
		t.Fatalf("write malformed profile: %v", err)
	}
	if _, err := listCredentialProfiles(dir); err == nil || !strings.Contains(err.Error(), "read profile") {
		t.Fatalf("listCredentialProfiles error = %v, want malformed profile error", err)
	}
	if _, err := readCredentials(filepath.Join(dir, "broken.json")); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("readCredentials error = %v, want decode error", err)
	}
}

func TestWriteCredentials_RejectsFileAsParent(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	err := writeCredentials(filepath.Join(blocker, "credentials.json"), credentials{})
	if err == nil || !strings.Contains(err.Error(), "create credentials directory") {
		t.Fatalf("writeCredentials error = %v, want directory error", err)
	}
}

func TestRunWhoami_RejectsUnsafeProfileAndUnknownFlag(t *testing.T) {
	for _, args := range [][]string{{"--profile", "../escape"}, {"--unknown"}} {
		var stdout, stderr bytes.Buffer
		if code := runWhoami(args, &stdout, &stderr); code != 2 {
			t.Fatalf("runWhoami(%q) = %d, want 2 (stderr=%q)", args, code, stderr.String())
		}
	}
}

func TestWireOpenCodeConfig_ReportsUnreadableAndMalformedFiles(t *testing.T) {
	dir := t.TempDir()
	malformed := filepath.Join(dir, "malformed.json")
	if err := os.WriteFile(malformed, []byte("{"), 0o600); err != nil {
		t.Fatalf("write malformed config: %v", err)
	}
	if err := wireOpenCodeConfig(malformed, "wormhole", "wormhole"); err == nil || !strings.Contains(err.Error(), "parse existing") {
		t.Fatalf("malformed config error = %v, want parse error", err)
	}

	fileParent := filepath.Join(dir, "file-parent")
	if err := os.WriteFile(fileParent, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file parent: %v", err)
	}
	if err := wireOpenCodeConfig(filepath.Join(fileParent, "opencode.json"), "wormhole", "wormhole"); err == nil || !strings.Contains(err.Error(), "read") {
		t.Fatalf("unreadable config error = %v, want read error", err)
	}
}

func TestRunViewerKeyCreate_ReportsEmptyErrorAndMalformedSuccess(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{name: "empty server error", status: http.StatusBadGateway, body: `{}`, want: "server returned status 502"},
		{name: "malformed success", status: http.StatusCreated, body: `not json`, want: "decode response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			var stdout, stderr bytes.Buffer
			code := runViewerKeyCreate([]string{"--server", srv.URL, "--project", "p", "--label", "viewer", "--admin-key", "admin"}, &stdout, &stderr)
			if code != 1 || !strings.Contains(stderr.String(), tt.want) {
				t.Fatalf("runViewerKeyCreate code=%d stderr=%q, want code 1 and %q", code, stderr.String(), tt.want)
			}
		})
	}
}

func TestRunConnect_TargetClaudeReportsAddFailure(t *testing.T) {
	fakeGatewaySocket(t)
	fakeStdioBinary(t)
	failingClaude := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(failingClaude, []byte("#!/bin/sh\nexit 7\n"), 0o755); err != nil {
		t.Fatalf("write failing claude: %v", err)
	}
	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		t.Fatal("connect must not search KB")
		return searchArticlesOutput{}, nil
	})
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := runConnect([]string{
		"--server", srv.URL, "--project", "project-a", "--permissions", "task.read",
		"--token-file", filepath.Join(t.TempDir(), "credentials.json"),
		"--target", "claude", "--claude-bin", failingClaude,
	}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "claude mcp add failed") {
		t.Fatalf("runConnect code=%d stderr=%q, want add failure", code, stderr.String())
	}
}

func TestRunProfileList_RejectsUnexpectedArgument(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runProfileList([]string{"unexpected"}, &stdout, &stderr); code != 2 {
		t.Fatalf("runProfileList code = %d, want 2 (stderr=%q)", code, stderr.String())
	}
}

func TestProfileEntryExpirationUsesFixedTTL(t *testing.T) {
	issued := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	writeTestCredentials(t, dir, "profile", credentials{IssuedAt: issued})
	entries, err := listCredentialProfiles(dir)
	if err != nil {
		t.Fatalf("listCredentialProfiles: %v", err)
	}
	if got, want := entries[0].ExpiresAt, issued.Add(cliTokenTTL); !got.Equal(want) {
		t.Fatalf("expires_at = %s, want %s", got, want)
	}
}
