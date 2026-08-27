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
