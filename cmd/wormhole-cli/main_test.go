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

// fakeServer builds an httptest.Server that answers wormhole.agent.register
// with a fixed successful registration and wormhole.kb.search with
// searchArticles (a caller-supplied stand-in for the tool handler), so
// tests can exercise the full two-call join sequence without a real
// Postgres-backed server.
func fakeServer(t *testing.T, searchArticles func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse)) *httptest.Server {
	t.Helper()
	issuedAt := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp/tools/call" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var req callRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Tool {
		case "wormhole.agent.register":
			var in registerAgentInput
			if err := json.Unmarshal(req.Arguments, &in); err != nil {
				t.Fatalf("decode register arguments: %v", err)
			}
			if in.Permissions == nil {
				t.Fatal("permissions: got nil, want non-nil")
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
		case "wormhole.kb.search":
			if got := r.Header.Get("Authorization"); got != "Bearer sekrit-token" {
				t.Fatalf("kb.search Authorization header: got %q, want %q", got, "Bearer sekrit-token")
			}
			var in searchArticlesInput
			if err := json.Unmarshal(req.Arguments, &in); err != nil {
				t.Fatalf("decode search arguments: %v", err)
			}
			out, errResp := searchArticles(t, in)
			if errResp != nil {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(*errResp)
				return
			}
			resultRaw, _ := json.Marshal(out)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(callResponse{Result: resultRaw})
		default:
			t.Fatalf("unexpected tool: %s", req.Tool)
		}
	}))
}

// TestRunJoin_Success_RegistersAndPersistsCredentials confirms step 1
// (registration + credential persistence) still behaves exactly as Day 19
// left it, now routed through the callTool refactor.
func TestRunJoin_Success_RegistersAndPersistsCredentials(t *testing.T) {
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

// TestRunJoin_KBSync_UsesCapabilitiesAndRolesAsQuery confirms that when no
// --context is given, the query sent to wormhole.kb.search is built from
// owner/model/capabilities/roles, and that the returned articles are
// printed.
func TestRunJoin_KBSync_UsesCapabilitiesAndRolesAsQuery(t *testing.T) {
	var gotQuery string
	var gotLimit int
	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		gotQuery = in.Query
		gotLimit = in.Limit
		return searchArticlesOutput{Articles: []articleSummary{
			{ArticleID: "art-1", Title: "deploy runbook"},
			{ArticleID: "art-2", Title: "on-call rotation"},
		}}, nil
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
		"--capabilities", "deploy,review",
		"--roles", "contributor",
		"--permissions", "kb.write",
		"--token-file", tokenFile,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}

	for _, want := range []string{"harley", "claude", "deploy", "review", "contributor"} {
		if !strings.Contains(gotQuery, want) {
			t.Fatalf("kb.search query: got %q, want it to contain %q", gotQuery, want)
		}
	}
	if gotLimit != 10 {
		t.Fatalf("kb.search limit: got %d, want default 10", gotLimit)
	}

	out := stdout.String()
	for _, want := range []string{"Synchronising knowledge graph (2 relevant)", "deploy runbook (art-1)", "on-call rotation (art-2)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q: got %q", want, out)
		}
	}
}

// TestRunJoin_KBSync_ExplicitContextAndLimit confirms --context overrides
// the derived query and --kb-limit is forwarded.
func TestRunJoin_KBSync_ExplicitContextAndLimit(t *testing.T) {
	var gotQuery string
	var gotLimit int
	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		gotQuery = in.Query
		gotLimit = in.Limit
		return searchArticlesOutput{Articles: []articleSummary{}}, nil
	})
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"join",
		"--server", srv.URL,
		"--project", "proj-1",
		"--permissions", "kb.write",
		"--context", "billing service architecture",
		"--kb-limit", "5",
		"--token-file", filepath.Join(t.TempDir(), "credentials.json"),
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}
	if gotQuery != "billing service architecture" {
		t.Fatalf("kb.search query: got %q, want %q", gotQuery, "billing service architecture")
	}
	if gotLimit != 5 {
		t.Fatalf("kb.search limit: got %d, want 5", gotLimit)
	}
	if !strings.Contains(stdout.String(), "Synchronising knowledge graph (0 relevant)") {
		t.Fatalf("stdout missing sync summary: %q", stdout.String())
	}
}

// TestRunJoin_KBSync_SkippedWhenNoContext confirms the sync call is
// skipped entirely (no HTTP request made) when nothing was supplied to
// build a query from.
func TestRunJoin_KBSync_SkippedWhenNoContext(t *testing.T) {
	called := false
	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		called = true
		return searchArticlesOutput{Articles: []articleSummary{}}, nil
	})
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"join",
		"--server", srv.URL,
		"--project", "proj-1",
		"--permissions", "kb.write",
		"--token-file", filepath.Join(t.TempDir(), "credentials.json"),
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}
	if called {
		t.Fatalf("expected wormhole.kb.search to be skipped, but it was called")
	}
	if !strings.Contains(stdout.String(), "skipped") {
		t.Fatalf("stdout missing skip notice: %q", stdout.String())
	}
}

// TestRunJoin_KBSync_FailureIsNonFatal confirms a failed KB sync doesn't
// erase step 1's already-persisted credentials or flip the exit code.
func TestRunJoin_KBSync_FailureIsNonFatal(t *testing.T) {
	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		return searchArticlesOutput{}, &callResponse{Error: `{"error":"kb: search: boom"}`}
	})
	defer srv.Close()

	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"join",
		"--server", srv.URL,
		"--project", "proj-1",
		"--owner", "harley",
		"--permissions", "kb.write",
		"--token-file", tokenFile,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0 (KB sync failure must not fail the whole join), stderr: %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "KB sync") {
		t.Fatalf("stderr missing KB sync warning: %q", stderr.String())
	}
	if _, err := os.Stat(tokenFile); err != nil {
		t.Fatalf("credentials file should still exist after a KB sync failure: %v", err)
	}
}
