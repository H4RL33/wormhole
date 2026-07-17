package main

import (
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

// callResponse is a test-only convenience type: a callback returns either
// its tool's real output or a *callResponse carrying an error message,
// which fakeServerExtended wraps into a JSON-RPC isError:true result
// (docs/mcp-protocol.md §3.1 — a tool-handler failure is a successful RPC
// call whose result carries isError:true, never a JSON-RPC error object).
type callResponse struct {
	Error string
}

// fakeServer builds an httptest.Server that answers wormhole.agent.register
// with a fixed successful registration and wormhole.kb.search with
// searchArticles (a caller-supplied stand-in for the tool handler), so
// tests can exercise the full two-call join sequence without a real
// Postgres-backed server.
func fakeServer(t *testing.T, searchArticles func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse)) *httptest.Server {
	return fakeServerExtended(t, searchArticles, nil, nil, nil)
}

func fakeServerExtended(
	t *testing.T,
	searchArticles func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse),
	listChannels func(t *testing.T) (listChannelsOutput, *callResponse),
	postEvent func(t *testing.T, in postEventInput) (postEventOutput, *callResponse),
	listTasks func(t *testing.T) (listTasksOutput, *callResponse),
) *httptest.Server {
	t.Helper()
	issuedAt := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		var params toolsCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("decode tools/call params: %v", err)
		}

		writeResult := func(resultOut any) {
			resultRaw, _ := json.Marshal(resultOut)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: resultRaw})
		}
		writeToolResult := func(out any, errResp *callResponse) {
			if errResp != nil {
				writeResult(toolCallResult{
					Content: []toolCallResultContent{{Type: "text", Text: errResp.Error}},
					IsError: true,
				})
				return
			}
			outRaw, _ := json.Marshal(out)
			writeResult(toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: string(outRaw)}}})
		}

		switch params.Name {
		case "wormhole.agent.register":
			var in registerAgentInput
			if err := json.Unmarshal(params.Arguments, &in); err != nil {
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
			writeToolResult(out, nil)
		case "wormhole.kb.search":
			if got := r.Header.Get("Authorization"); got != "Bearer sekrit-token" {
				t.Fatalf("kb.search Authorization header: got %q, want %q", got, "Bearer sekrit-token")
			}
			var in searchArticlesInput
			if err := json.Unmarshal(params.Arguments, &in); err != nil {
				t.Fatalf("decode search arguments: %v", err)
			}
			out, errResp := searchArticles(t, in)
			writeToolResult(out, errResp)
		case "wormhole.channel.list":
			if got := r.Header.Get("Authorization"); got != "Bearer sekrit-token" {
				t.Fatalf("channel.list Authorization header: got %q, want %q", got, "Bearer sekrit-token")
			}
			var out listChannelsOutput
			var errResp *callResponse
			if listChannels != nil {
				out, errResp = listChannels(t)
			} else {
				out = listChannelsOutput{
					Channels: []channelSummary{
						{ChannelID: "chan-1", Name: "introductions"},
					},
				}
			}
			writeToolResult(out, errResp)
		case "wormhole.channel.post":
			if got := r.Header.Get("Authorization"); got != "Bearer sekrit-token" {
				t.Fatalf("channel.post Authorization header: got %q, want %q", got, "Bearer sekrit-token")
			}
			var in postEventInput
			if err := json.Unmarshal(params.Arguments, &in); err != nil {
				t.Fatalf("decode post event arguments: %v", err)
			}
			var out postEventOutput
			var errResp *callResponse
			if postEvent != nil {
				out, errResp = postEvent(t, in)
			} else {
				out = postEventOutput{EventID: "evt-1"}
			}
			writeToolResult(out, errResp)
		case "wormhole.task.list":
			if got := r.Header.Get("Authorization"); got != "Bearer sekrit-token" {
				t.Fatalf("task.list Authorization header: got %q, want %q", got, "Bearer sekrit-token")
			}
			var out listTasksOutput
			var errResp *callResponse
			if listTasks != nil {
				out, errResp = listTasks(t)
			} else {
				out = listTasksOutput{Tasks: []taskSummary{}}
			}
			writeToolResult(out, errResp)
		default:
			t.Fatalf("unexpected tool: %s", params.Name)
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

// TestRunJoin_Role_SendsRoleInRegisterRequest confirms --role is wired
// through to wormhole.agent.register's request body as the "role" field
// (Chapter 6: registerAgentInput gains a singular Role field distinct from
// the existing plural Roles tag slice).
func TestRunJoin_Role_SendsRoleInRegisterRequest(t *testing.T) {
	issuedAt := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	var gotIn registerAgentInput
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		var params toolsCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("decode tools/call params: %v", err)
		}
		switch params.Name {
		case "wormhole.agent.register":
			if err := json.Unmarshal(params.Arguments, &gotIn); err != nil {
				t.Fatalf("decode register arguments: %v", err)
			}
			out := registerAgentOutput{
				AgentID:      "agent-1",
				PassportID:   "passport-1",
				Token:        "sekrit-token",
				Repositories: []string{},
				Roles:        []string{"backend-engineer"},
				IssuedAt:     issuedAt,
				Role:         "backend-engineer",
			}
			outRaw, _ := json.Marshal(out)
			resultRaw, _ := json.Marshal(toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: string(outRaw)}}})
			json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: resultRaw})
		case "wormhole.kb.search":
			outRaw, _ := json.Marshal(searchArticlesOutput{Articles: []articleSummary{}})
			resultRaw, _ := json.Marshal(toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: string(outRaw)}}})
			json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: resultRaw})
		case "wormhole.channel.list":
			outRaw, _ := json.Marshal(listChannelsOutput{Channels: []channelSummary{}})
			resultRaw, _ := json.Marshal(toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: string(outRaw)}}})
			json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: resultRaw})
		case "wormhole.task.list":
			outRaw, _ := json.Marshal(listTasksOutput{Tasks: []taskSummary{}})
			resultRaw, _ := json.Marshal(toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: string(outRaw)}}})
			json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: resultRaw})
		default:
			t.Fatalf("unexpected tool call: %s", params.Name)
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
		"--role", "backend-engineer",
		"--token-file", tokenFile,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}

	if gotIn.Role != "backend-engineer" {
		t.Fatalf("registerAgentInput.Role: got %q, want %q", gotIn.Role, "backend-engineer")
	}
}

func TestRunJoin_ServerError_PrintsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		result := toolCallResult{
			Content: []toolCallResultContent{{Type: "text", Text: `{"error":"identity: invalid scope","code":"INVALID_SCOPE"}`}},
			IsError: true,
		}
		resultRaw, _ := json.Marshal(result)
		json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: resultRaw})
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

func TestRunJoin_Step3_PostsToIntroductionsChannel(t *testing.T) {
	var gotChannelID string
	var gotPayloadText string
	var gotNote *string

	srv := fakeServerExtended(t,
		func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
			return searchArticlesOutput{Articles: []articleSummary{}}, nil
		},
		func(t *testing.T) (listChannelsOutput, *callResponse) {
			return listChannelsOutput{
				Channels: []channelSummary{
					{ChannelID: "chan-general", Name: "general"},
					{ChannelID: "chan-intro", Name: "introductions"},
				},
			}, nil
		},
		func(t *testing.T, in postEventInput) (postEventOutput, *callResponse) {
			gotChannelID = in.ChannelID
			var payload struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(in.Payload, &payload); err != nil {
				t.Fatalf("unmarshal post payload: %v", err)
			}
			gotPayloadText = payload.Text
			gotNote = in.Note
			return postEventOutput{EventID: "evt-123"}, nil
		},
		nil,
	)
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	code := run([]string{
		"join",
		"--server", srv.URL,
		"--project", "proj-1",
		"--owner", "harley",
		"--model", "claude",
		"--token-file", tokenFile,
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}
	if gotChannelID != "chan-intro" {
		t.Fatalf("posted to channel %q, want %q", gotChannelID, "chan-intro")
	}
	wantText := "harley (claude) joined the project."
	if gotPayloadText != wantText {
		t.Fatalf("posted payload text %q, want %q", gotPayloadText, wantText)
	}
	if gotNote == nil || *gotNote != wantText {
		t.Fatalf("posted note field: got %v, want %q", gotNote, wantText)
	}
	if !strings.Contains(stdout.String(), "Introducing agent to #introductions...") {
		t.Fatalf("stdout missing introduction notice: %q", stdout.String())
	}
}

func TestRunJoin_Step3_IntroFallbackToAgentID(t *testing.T) {
	var gotPayloadText string
	var gotNote *string

	srv := fakeServerExtended(t,
		func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
			return searchArticlesOutput{Articles: []articleSummary{}}, nil
		},
		func(t *testing.T) (listChannelsOutput, *callResponse) {
			return listChannelsOutput{
				Channels: []channelSummary{
					{ChannelID: "chan-intro", Name: "introductions"},
				},
			}, nil
		},
		func(t *testing.T, in postEventInput) (postEventOutput, *callResponse) {
			var payload struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(in.Payload, &payload); err != nil {
				t.Fatalf("unmarshal post payload: %v", err)
			}
			gotPayloadText = payload.Text
			gotNote = in.Note
			return postEventOutput{EventID: "evt-123"}, nil
		},
		nil,
	)
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	code := run([]string{
		"join",
		"--server", srv.URL,
		"--project", "proj-1",
		"--token-file", tokenFile,
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}
	wantText := "agent-1 joined the project."
	if gotPayloadText != wantText {
		t.Fatalf("posted payload text %q, want %q", gotPayloadText, wantText)
	}
	if gotNote == nil || *gotNote != wantText {
		t.Fatalf("posted note field: got %v, want %q", gotNote, wantText)
	}
}

func TestRunJoin_Step3_NoIntroductionsChannel_NonFatal(t *testing.T) {
	srv := fakeServerExtended(t,
		func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
			return searchArticlesOutput{Articles: []articleSummary{}}, nil
		},
		func(t *testing.T) (listChannelsOutput, *callResponse) {
			return listChannelsOutput{
				Channels: []channelSummary{
					{ChannelID: "chan-general", Name: "general"},
				},
			}, nil
		},
		func(t *testing.T, in postEventInput) (postEventOutput, *callResponse) {
			t.Fatal("should not call post event if introductions channel is absent")
			return postEventOutput{}, nil
		},
		nil,
	)
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	code := run([]string{
		"join",
		"--server", srv.URL,
		"--project", "proj-1",
		"--token-file", tokenFile,
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "introductions channel not found") {
		t.Fatalf("stderr missing introductions warning: %q", stderr.String())
	}
}

func TestRunJoin_Step3_ListChannelsFailure_NonFatal(t *testing.T) {
	srv := fakeServerExtended(t,
		func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
			return searchArticlesOutput{Articles: []articleSummary{}}, nil
		},
		func(t *testing.T) (listChannelsOutput, *callResponse) {
			return listChannelsOutput{}, &callResponse{Error: "list channels failed"}
		},
		nil,
		nil,
	)
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	code := run([]string{
		"join",
		"--server", srv.URL,
		"--project", "proj-1",
		"--token-file", tokenFile,
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "self-introduction failed") {
		t.Fatalf("stderr missing self-introduction warning: %q", stderr.String())
	}
}

func TestRunJoin_Step3_PostEventFailure_NonFatal(t *testing.T) {
	srv := fakeServerExtended(t,
		func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
			return searchArticlesOutput{Articles: []articleSummary{}}, nil
		},
		func(t *testing.T) (listChannelsOutput, *callResponse) {
			return listChannelsOutput{
				Channels: []channelSummary{
					{ChannelID: "chan-intro", Name: "introductions"},
				},
			}, nil
		},
		func(t *testing.T, in postEventInput) (postEventOutput, *callResponse) {
			return postEventOutput{}, &callResponse{Error: "post failed"}
		},
		nil,
	)
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	code := run([]string{
		"join",
		"--server", srv.URL,
		"--project", "proj-1",
		"--token-file", tokenFile,
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "self-introduction failed") {
		t.Fatalf("stderr missing self-introduction warning: %q", stderr.String())
	}
}

func TestRunJoin_Step4_PrintsCorrectTaskCounts(t *testing.T) {
	srv := fakeServerExtended(t,
		func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
			return searchArticlesOutput{Articles: []articleSummary{}}, nil
		},
		nil,
		nil,
		func(t *testing.T) (listTasksOutput, *callResponse) {
			return listTasksOutput{
				Tasks: []taskSummary{
					{Status: "todo"},
					{Status: "wip"},
					{Status: "blocked"},
					{Status: "done"},
					{Status: "done"},
					{Status: "unknown"},
				},
			}, nil
		},
	)
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	code := run([]string{
		"join",
		"--server", srv.URL,
		"--project", "proj-1",
		"--token-file", tokenFile,
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}
	wantStdout := "Ready. 3 open tasks, 2 done."
	if !strings.Contains(stdout.String(), wantStdout) {
		t.Fatalf("stdout missing expected task summary %q: got %q", wantStdout, stdout.String())
	}
}

func TestRunJoin_Step4_ListTasksFailure_NonFatal(t *testing.T) {
	srv := fakeServerExtended(t,
		func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
			return searchArticlesOutput{Articles: []articleSummary{}}, nil
		},
		nil,
		nil,
		func(t *testing.T) (listTasksOutput, *callResponse) {
			return listTasksOutput{}, &callResponse{Error: "list tasks failed"}
		},
	)
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	code := run([]string{
		"join",
		"--server", srv.URL,
		"--project", "proj-1",
		"--token-file", tokenFile,
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "task list failed") {
		t.Fatalf("stderr missing task list warning: %q", stderr.String())
	}
	if strings.Contains(stdout.String(), "Ready.") {
		t.Fatalf("stdout should not contain Ready tasks summary when task list fails: %q", stdout.String())
	}
}

// fakeClaudeScript writes an executable shell script to t.TempDir() that
// appends every invocation's arguments as one line to <script-dir>/calls.log,
// then always exits 0. Tests use this instead of invoking a real `claude`
// binary, which cannot be assumed present in any environment running this
// suite.
func fakeClaudeScript(t *testing.T) (scriptPath, logPath string) {
	t.Helper()
	dir := t.TempDir()
	scriptPath = filepath.Join(dir, "fake-claude.sh")
	logPath = filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\necho \"$@\" >> \"" + logPath + "\"\nexit 0\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude script: %v", err)
	}
	return scriptPath, logPath
}

// fakeStdioBinary writes a simple executable script that just exits 0,
// and adds it to PATH via a temporary directory. Returns the binary path
// and the parent directory (for PATH manipulation if needed).
func fakeStdioBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	binPath := filepath.Join(dir, "wormhole-mcp-stdio")
	script := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake stdio binary: %v", err)
	}
	// Add to PATH
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", dir+":"+oldPath)
	t.Cleanup(func() {
		os.Setenv("PATH", oldPath)
	})
	return binPath
}

// fakeWormholedSocket creates a listening Unix socket at a custom location
// (via XDG_RUNTIME_DIR override) that accepts connections. This allows tests
// to make the socket reachability check pass without running the actual
// wormholed daemon. The socket is closed when t.Cleanup runs.
func fakeWormholedSocket(t *testing.T) (runtimeDir string) {
	t.Helper()
	tempDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", tempDir)

	socketDir := filepath.Join(tempDir, "wormhole")
	if err := os.Mkdir(socketDir, 0o700); err != nil {
		t.Fatalf("create wormhole socket dir: %v", err)
	}
	socketPath := filepath.Join(socketDir, "wormholed.sock")

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("create fake wormholed socket: %v", err)
	}

	// Accept connections in background (for socket reachability check)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	t.Cleanup(func() {
		listener.Close()
	})

	return tempDir
}

func TestRunConnect_Success_RegistersAndWiresConnector(t *testing.T) {
	fakeWormholedSocket(t) // make socket reachable
	fakeStdioBinary(t)     // add stdio binary to PATH

	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		t.Fatal("connect must not call wormhole.kb.search")
		return searchArticlesOutput{}, nil
	})
	defer srv.Close()

	claudeBin, logPath := fakeClaudeScript(t)
	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"connect",
		"--server", srv.URL,
		"--project", "proj-1",
		"--owner", "harley",
		"--model", "claude-code",
		"--permissions", "task.read",
		"--token-file", tokenFile,
		"--claude-bin", claudeBin,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}

	data, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatalf("read credentials file: %v", err)
	}
	var creds credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		t.Fatalf("decode credentials file: %v", err)
	}
	if creds.Token != "sekrit-token" {
		t.Fatalf("credentials.Token: got %q, want %q", creds.Token, "sekrit-token")
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake claude call log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(logData)), "\n")
	if len(lines) != 2 {
		t.Fatalf("fake claude invocation count: got %d, want 2 (remove, add): %q", len(lines), logData)
	}
	if !strings.Contains(lines[0], "mcp remove wormhole -s local") {
		t.Fatalf("first invocation: got %q, want it to contain %q", lines[0], "mcp remove wormhole -s local")
	}
	// NEW: assert stdio wiring (mcp add <name> -- <binary_path>)
	if !strings.Contains(lines[1], "mcp add wormhole -- ") {
		t.Fatalf("second invocation: got %q, want it to contain stdio args (mcp add wormhole -- ...)", lines[1])
	}
	if !strings.Contains(lines[1], "wormhole-mcp-stdio") {
		t.Fatalf("second invocation should reference wormhole-mcp-stdio binary: got %q", lines[1])
	}

	out := stdout.String()
	for _, want := range []string{"Passport created.", "Connector \"wormhole\" registered"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q: got %q", want, out)
		}
	}
}

func TestRunConnect_CustomConnectorName(t *testing.T) {
	fakeWormholedSocket(t) // make socket reachable
	fakeStdioBinary(t)     // add stdio binary to PATH

	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		t.Fatal("connect must not call wormhole.kb.search")
		return searchArticlesOutput{}, nil
	})
	defer srv.Close()

	claudeBin, logPath := fakeClaudeScript(t)
	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"connect",
		"--server", srv.URL,
		"--project", "proj-1",
		"--permissions", "task.read",
		"--token-file", tokenFile,
		"--claude-bin", claudeBin,
		"--connector-name", "wh-staging",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake claude call log: %v", err)
	}
	if !strings.Contains(string(logData), "wh-staging") {
		t.Fatalf("fake claude call log missing custom connector name: %q", logData)
	}
	// NEW: assert stdio wiring
	if !strings.Contains(string(logData), "mcp add wh-staging -- ") {
		t.Fatalf("expected stdio wiring (mcp add wh-staging -- ...) but got: %q", string(logData))
	}
}

func TestRunConnect_RegisterFailure_NeverInvokesClaude(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)
		result := toolCallResult{
			Content: []toolCallResultContent{{Type: "text", Text: `{"error":"identity: invalid scope"}`}},
			IsError: true,
		}
		resultRaw, _ := json.Marshal(result)
		json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: resultRaw})
	}))
	defer srv.Close()

	claudeBin, logPath := fakeClaudeScript(t)
	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"connect", "--server", srv.URL, "--project", "proj-1", "--permissions", "task.read",
		"--token-file", tokenFile, "--claude-bin", claudeBin,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code: got %d, want 1", code)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("fake claude should never have been invoked on register failure")
	}
	if _, err := os.Stat(tokenFile); !os.IsNotExist(err) {
		t.Fatalf("credentials file should not have been written on register failure")
	}
}

func TestRunConnect_ClaudeBinaryNotFound_PrintsManualFallback(t *testing.T) {
	fakeWormholedSocket(t) // make socket reachable
	fakeStdioBinary(t)     // add stdio binary to PATH

	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		t.Fatal("connect must not call wormhole.kb.search")
		return searchArticlesOutput{}, nil
	})
	defer srv.Close()

	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"connect", "--server", srv.URL, "--project", "proj-1", "--permissions", "task.read",
		"--token-file", tokenFile, "--claude-bin", "definitely-not-a-real-binary-xyz",
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code: got %d, want 1", code)
	}
	// Registration itself must still have succeeded and been persisted —
	// only the auto-wire step failed.
	if _, err := os.Stat(tokenFile); err != nil {
		t.Fatalf("credentials file should have been written even though claude binary was missing: %v", err)
	}
	// NEW: assert updated message (stdio bridge, not bearer token)
	errMsg := stderr.String()
	if !strings.Contains(errMsg, "not found in PATH") {
		t.Fatalf("stderr should mention binary not found: %q", errMsg)
	}
	if !strings.Contains(errMsg, "claude mcp add") {
		t.Fatalf("stderr should show manual fallback command: %q", errMsg)
	}
	if !strings.Contains(errMsg, "--") {
		t.Fatalf("stderr should show stdio indicator (--): %q", errMsg)
	}
}

func TestRunConnect_SocketUnreachable_ReturnsError(t *testing.T) {
	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		t.Fatal("connect must not call wormhole.kb.search if socket is unreachable")
		return searchArticlesOutput{}, nil
	})
	defer srv.Close()

	claudeBin, _ := fakeClaudeScript(t)
	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	var stdout, stderr bytes.Buffer
	// Use a nonexistent socket path via env var override
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir()) // empty dir, socket won't exist

	code := run([]string{
		"connect",
		"--server", srv.URL,
		"--project", "proj-1",
		"--permissions", "task.read",
		"--token-file", tokenFile,
		"--claude-bin", claudeBin,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code: got %d, want 1 when socket unreachable", code)
	}
	errMsg := stderr.String()
	if !strings.Contains(errMsg, "wormholed not running") {
		t.Fatalf("stderr should mention wormholed not running: %q", errMsg)
	}
	if !strings.Contains(errMsg, "start wormholed") {
		t.Fatalf("stderr should prompt to start wormholed: %q", errMsg)
	}
	// Credentials ARE written before socket check (step 1 succeeds, then step 2 fails on socket check)
	if _, err := os.Stat(tokenFile); err != nil {
		t.Fatalf("credentials file should be written even when socket is unreachable: %v", err)
	}
}

// TestRunConnect_StdioBinaryNotFound_ClaudeTarget_PrintsError confirms that
// when wormhole-mcp-stdio is not in PATH and target is "claude" (default),
// runConnect returns exit code 1 and prints the claude-specific manual fallback message.
func TestRunConnect_StdioBinaryNotFound_ClaudeTarget_PrintsError(t *testing.T) {
	fakeWormholedSocket(t) // make socket reachable, so registration + socket check pass
	// Deliberately NOT calling fakeStdioBinary(t), so LookPath fails

	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		t.Fatal("connect must not call wormhole.kb.search if stdio binary is not found")
		return searchArticlesOutput{}, nil
	})
	defer srv.Close()

	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"connect",
		"--server", srv.URL,
		"--project", "proj-1",
		"--permissions", "task.read",
		"--token-file", tokenFile,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code: got %d, want 1", code)
	}

	// Registration and socket check must have succeeded; only stdio binary resolution failed
	if _, err := os.Stat(tokenFile); err != nil {
		t.Fatalf("credentials file should have been written even though stdio binary was missing: %v", err)
	}

	errMsg := stderr.String()
	// Check for the claude-target error message (line 770)
	if !strings.Contains(errMsg, "not found in PATH") {
		t.Fatalf("stderr should mention binary not found: %q", errMsg)
	}
	if !strings.Contains(errMsg, "claude mcp add") {
		t.Fatalf("stderr should show claude manual fallback command (claude mcp add): %q", errMsg)
	}
	if !strings.Contains(errMsg, "--") {
		t.Fatalf("stderr should show stdio indicator (--): %q", errMsg)
	}
	// Ensure it's the default connector name
	if !strings.Contains(errMsg, "wormhole") {
		t.Fatalf("stderr should reference default connector name 'wormhole': %q", errMsg)
	}
}

// TestRunConnect_StdioBinaryNotFound_OpenCodeTarget_PrintsError confirms that
// when wormhole-mcp-stdio is not in PATH and target is "opencode",
// runConnect returns exit code 1 and prints the opencode-specific manual fallback message.
func TestRunConnect_StdioBinaryNotFound_OpenCodeTarget_PrintsError(t *testing.T) {
	fakeWormholedSocket(t) // make socket reachable, so registration + socket check pass
	// Deliberately NOT calling fakeStdioBinary(t), so LookPath fails

	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		t.Fatal("connect must not call wormhole.kb.search if stdio binary is not found")
		return searchArticlesOutput{}, nil
	})
	defer srv.Close()

	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"connect",
		"--server", srv.URL,
		"--project", "proj-1",
		"--permissions", "task.read",
		"--target", "opencode",
		"--token-file", tokenFile,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code: got %d, want 1", code)
	}

	// Registration and socket check must have succeeded; only stdio binary resolution failed
	if _, err := os.Stat(tokenFile); err != nil {
		t.Fatalf("credentials file should have been written even though stdio binary was missing: %v", err)
	}

	errMsg := stderr.String()
	// Check for the opencode-target error message (line 768)
	if !strings.Contains(errMsg, "not found in PATH") {
		t.Fatalf("stderr should mention binary not found: %q", errMsg)
	}
	if !strings.Contains(errMsg, "add mcp config") {
		t.Fatalf("stderr should show opencode manual fallback command (add mcp config): %q", errMsg)
	}
	if !strings.Contains(errMsg, "type:\"local\"") {
		t.Fatalf("stderr should reference MCP type local config: %q", errMsg)
	}
	if !strings.Contains(errMsg, "command:") {
		t.Fatalf("stderr should reference command config key: %q", errMsg)
	}
}

// TestRunJoin_DefaultProfile_DerivedFromProjectAndRole confirms Chapter 8:
// with neither --token-file nor --profile given, join writes into
// ~/.wormhole/credentials/<project>__<role>.json instead of the old fixed
// ~/.wormhole/credentials.json.
func TestRunJoin_DefaultProfile_DerivedFromProjectAndRole(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		return searchArticlesOutput{Articles: []articleSummary{}}, nil
	})
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"join",
		"--server", srv.URL,
		"--project", "proj-1",
		"--role", "backend-engineer",
		"--owner", "harley",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}

	wantPath := filepath.Join(home, ".wormhole", "credentials", "proj-1__backend-engineer.json")
	data, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read %s: %v", wantPath, err)
	}
	var creds credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		t.Fatalf("decode credentials: %v", err)
	}
	if creds.Role != "backend-engineer" || creds.ProjectID != "proj-1" {
		t.Fatalf("credentials: got %+v", creds)
	}
}

// TestRunJoin_TwoRoles_DoNotClobberEachOther confirms Chapter 8's core
// requirement: joining the same project with two different roles produces
// two separate credential files, neither overwriting the other.
func TestRunJoin_TwoRoles_DoNotClobberEachOther(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		return searchArticlesOutput{Articles: []articleSummary{}}, nil
	})
	defer srv.Close()

	for _, role := range []string{"backend-engineer", "frontend-engineer"} {
		var stdout, stderr bytes.Buffer
		code := run([]string{
			"join",
			"--server", srv.URL,
			"--project", "proj-1",
			"--role", role,
			"--owner", "harley",
		}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("join role=%s exit code: got %d, want 0, stderr: %q", role, code, stderr.String())
		}
	}

	backendPath := filepath.Join(home, ".wormhole", "credentials", "proj-1__backend-engineer.json")
	frontendPath := filepath.Join(home, ".wormhole", "credentials", "proj-1__frontend-engineer.json")
	if _, err := os.Stat(backendPath); err != nil {
		t.Fatalf("backend profile missing: %v", err)
	}
	if _, err := os.Stat(frontendPath); err != nil {
		t.Fatalf("frontend profile missing: %v", err)
	}
}

// TestRunJoin_ExplicitProfile_WritesNamedFile confirms --profile picks the
// filename directly, bypassing the project/role-derived default.
func TestRunJoin_ExplicitProfile_WritesNamedFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		return searchArticlesOutput{Articles: []articleSummary{}}, nil
	})
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"join",
		"--server", srv.URL,
		"--project", "proj-1",
		"--profile", "my-manager-session",
		"--owner", "harley",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}

	wantPath := filepath.Join(home, ".wormhole", "credentials", "my-manager-session.json")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("stat %s: %v", wantPath, err)
	}
}

// TestRunJoin_ExplicitProfile_RejectsUnsafeName confirms a --profile value
// that could escape the credentials directory is rejected, not sanitized.
func TestRunJoin_ExplicitProfile_RejectsUnsafeName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"join",
		"--server", "http://example.invalid",
		"--project", "proj-1",
		"--profile", "../escape",
		"--owner", "harley",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit code: got 0, want non-zero, stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "profile") {
		t.Fatalf("stderr should mention the profile-name error: got %q", stderr.String())
	}
}

// TestRunConnect_DefaultProfile_DerivedFromProject confirms connect (no
// --role flag) derives its default profile key using the "default" role
// placeholder.
func TestRunConnect_DefaultProfile_DerivedFromProject(t *testing.T) {
	fakeWormholedSocket(t) // make socket reachable
	fakeStdioBinary(t)     // add stdio binary to PATH

	home := t.TempDir()
	t.Setenv("HOME", home)

	claudeBin := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(claudeBin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude bin: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)
		out := registerAgentOutput{AgentID: "agent-1", PassportID: "passport-1", Token: "sekrit-token", Repositories: []string{}, Roles: []string{}}
		outRaw, _ := json.Marshal(out)
		resultRaw, _ := json.Marshal(toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: string(outRaw)}}})
		json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: resultRaw})
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"connect",
		"--server", srv.URL,
		"--project", "proj-1",
		"--owner", "harley",
		"--claude-bin", claudeBin,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}

	wantPath := filepath.Join(home, ".wormhole", "credentials", "proj-1__default.json")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("stat %s: %v", wantPath, err)
	}
}

func TestRun_WhoamiCommand_NoProfiles_PrintsError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := run([]string{"whoami"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit code: got 0, want non-zero")
	}
	if !strings.Contains(stderr.String(), "no stored credential profiles") {
		t.Fatalf("stderr: got %q", stderr.String())
	}
}

func TestRun_WhoamiCommand_SingleProfile_AutoSelects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".wormhole", "credentials")
	writeTestCredentials(t, dir, "proj-1__backend-engineer", credentials{
		ProjectID: "proj-1", Role: "backend-engineer", AgentID: "agent-1",
		IssuedAt: time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC),
	})

	var stdout, stderr bytes.Buffer
	code := run([]string{"whoami"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"proj-1__backend-engineer", "proj-1", "backend-engineer", "agent-1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q: got %q", want, out)
		}
	}
}

func TestRun_WhoamiCommand_MultipleProfiles_RequiresFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".wormhole", "credentials")
	issuedAt := time.Now()
	writeTestCredentials(t, dir, "proj-1__backend-engineer", credentials{ProjectID: "proj-1", Role: "backend-engineer", AgentID: "agent-1", IssuedAt: issuedAt})
	writeTestCredentials(t, dir, "proj-1__frontend-engineer", credentials{ProjectID: "proj-1", Role: "frontend-engineer", AgentID: "agent-2", IssuedAt: issuedAt})

	var stdout, stderr bytes.Buffer
	code := run([]string{"whoami"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit code: got 0, want non-zero (ambiguous profile)")
	}
	if !strings.Contains(stderr.String(), "--profile") {
		t.Fatalf("stderr should prompt for --profile: got %q", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"whoami", "--profile", "proj-1__frontend-engineer"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "agent-2") {
		t.Fatalf("stdout missing agent-2: got %q", stdout.String())
	}
}

func TestRun_WhoamiCommand_UnknownProfile_PrintsError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var stdout, stderr bytes.Buffer
	code := run([]string{"whoami", "--profile", "does-not-exist"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit code: got 0, want non-zero")
	}
}

func TestRun_ProfileListCommand_Empty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := run([]string{"profile", "list"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no stored credential profiles") {
		t.Fatalf("stdout: got %q", stdout.String())
	}
}

func TestRun_ProfileListCommand_ListsAll(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".wormhole", "credentials")
	issuedAt := time.Now()
	writeTestCredentials(t, dir, "proj-1__backend-engineer", credentials{ProjectID: "proj-1", Role: "backend-engineer", AgentID: "agent-1", IssuedAt: issuedAt})
	writeTestCredentials(t, dir, "proj-1__frontend-engineer", credentials{ProjectID: "proj-1", Role: "frontend-engineer", AgentID: "agent-2", IssuedAt: issuedAt})

	var stdout, stderr bytes.Buffer
	code := run([]string{"profile", "list"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"proj-1__backend-engineer", "proj-1__frontend-engineer", "agent-1", "agent-2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q: got %q", want, out)
		}
	}
}

func TestRun_ProfileCommand_UnknownSubcommand_PrintsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"profile", "bogus"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code: got %d, want 2", code)
	}
}

func TestRunConnect_OpenCode_Success_WritesLocalConfig(t *testing.T) {
	fakeWormholedSocket(t) // make socket reachable
	fakeStdioBinary(t)     // add stdio binary to PATH

	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		t.Fatal("connect must not call wormhole.kb.search")
		return searchArticlesOutput{}, nil
	})
	defer srv.Close()

	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "opencode.json")
	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"connect",
		"--server", srv.URL,
		"--project", "proj-1",
		"--owner", "harley",
		"--model", "claude-code",
		"--permissions", "task.read",
		"--token-file", tokenFile,
		"--target", "opencode",
		"--opencode-config", configPath,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read opencode config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}

	mcp, ok := cfg["mcp"].(map[string]any)
	if !ok {
		t.Fatalf("config missing mcp section: %+v", cfg)
	}
	wormhole, ok := mcp["wormhole"].(map[string]any)
	if !ok {
		t.Fatalf("config mcp missing wormhole entry: %+v", mcp)
	}

	// NEW: assert local type and command array
	if connType, ok := wormhole["type"]; !ok || connType != "local" {
		t.Fatalf("wormhole type: got %v, want \"local\"", connType)
	}
	if cmd, ok := wormhole["command"].([]any); !ok || len(cmd) != 1 {
		t.Fatalf("wormhole command: got %v, want []any with 1 element", cmd)
	}
	if cmdStr, ok := wormhole["command"].([]any)[0].(string); !ok || !strings.Contains(cmdStr, "wormhole-mcp-stdio") {
		t.Fatalf("wormhole command[0] should contain binary path: got %v", wormhole["command"].([]any)[0])
	}

	// Assert no url or headers fields in local config
	if _, hasURL := wormhole["url"]; hasURL {
		t.Fatalf("local config should not have url field: %+v", wormhole)
	}
	if _, hasHeaders := wormhole["headers"]; hasHeaders {
		t.Fatalf("local config should not have headers field: %+v", wormhole)
	}

	out := stdout.String()
	if !strings.Contains(out, "Passport created.") {
		t.Fatalf("stdout missing 'Passport created.': %q", out)
	}
}

func TestRunConnect_OpenCode_CustomConnectorName(t *testing.T) {
	fakeWormholedSocket(t) // make socket reachable
	fakeStdioBinary(t)     // add stdio binary to PATH

	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		t.Fatal("connect must not call wormhole.kb.search")
		return searchArticlesOutput{}, nil
	})
	defer srv.Close()

	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "opencode.json")
	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"connect",
		"--server", srv.URL,
		"--project", "proj-1",
		"--permissions", "task.read",
		"--token-file", tokenFile,
		"--target", "opencode",
		"--opencode-config", configPath,
		"--connector-name", "wh-staging",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read opencode config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}

	mcp, ok := cfg["mcp"].(map[string]any)
	if !ok {
		t.Fatalf("config missing mcp section: %+v", cfg)
	}
	staging, ok := mcp["wh-staging"].(map[string]any)
	if !ok {
		t.Fatalf("config mcp missing wh-staging entry: %+v", mcp)
	}

	if connType, ok := staging["type"]; !ok || connType != "local" {
		t.Fatalf("wh-staging type: got %v, want \"local\"", connType)
	}
	if _, hasURL := staging["url"]; hasURL {
		t.Fatalf("local config should not have url field: %+v", staging)
	}
}

