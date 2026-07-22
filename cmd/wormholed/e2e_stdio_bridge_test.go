// e2e_stdio_bridge_test.go
// Issue #20 subtask 6: proves the full transport chain a real MCP client
// (Claude Code or any other stdio-speaking harness) actually uses --
// stdio bridge subprocess (`wormhole mcp`, cmd/wormhole/mcp.go) ->
// wormholed's Unix socket -> wormholed's MCP dispatch -> local SQLite write
// + sync enqueue -> sync engine push -> real Coordination Server -> real
// Postgres.
//
// Every existing test in this repo (wormholed_test.go's
// TestRun_EndToEndWhoAmI, p7_e2e_integration_test.go's TestP7_*) dials
// wormholed's socket directly, bypassing the stdio bridge entirely. That
// bypass is exactly the gap this subtask exists to close: Leg 3 below drives
// the real `wormhole mcp` subprocess over its stdin/stdout pipes (the leg a
// real harness talks to), speaking the bridge's newline-delimited JSON-RPC
// framing.
package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/H4RL33/wormhole/internal/core/events"
	"github.com/H4RL33/wormhole/internal/core/git"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/core/kb"
	"github.com/H4RL33/wormhole/internal/core/roles"
	"github.com/H4RL33/wormhole/internal/core/tasks"
	"github.com/H4RL33/wormhole/internal/mcp"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
)

// -----------------------------------------------------------------------
// Real-Postgres helper (mirrors internal/mcp/server_test.go's testDB /
// cmd/wormhole-server/m3_integration_test.go's testDB skip pattern
// exactly -- this package has no existing testDB of its own since
// p7_e2e_integration_test.go only ever talks to a fake HTTP coord server).
// -----------------------------------------------------------------------

func e2eTestDB(t *testing.T) *sql.DB {
	t.Helper()
	cfg := types.LoadConfig()
	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		if os.Getenv("WORMHOLE_INTEGRATION_REQUIRED") == "1" {
			t.Fatalf("postgres required but not reachable: %v", err)
		}
		db.Close()
		t.Skipf("postgres not reachable (%v); run `docker compose up -d db` and apply migrations before running this test", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func e2eMustCreateProject(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`INSERT INTO projects (name, owner) VALUES ($1, $2) RETURNING id`, name, "harley").Scan(&id); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM projects WHERE id = $1`, id); err != nil {
			t.Logf("cleanup: delete project %s: %v", id, err)
		}
	})
	return id
}

// -----------------------------------------------------------------------
// Leg 1: real Coordination Server, real Postgres. Mirrors
// cmd/wormhole-server/m3_integration_test.go's construction and
// cmd/wormhole-server/main.go:39-58's exact tool registration list/order
// (16 base tools + 4 sync tools), so this test exercises the real
// production tool surface, not a subset. m3CallTool/m3ToolsCallParams etc.
// live in package main under cmd/wormhole-server, a different package, and
// are unexported -- this file keeps its own equivalent copy, matching this
// codebase's established posture of small per-package client-shape
// duplicates (see m3_integration_test.go:22-28's own comment on the same
// tradeoff).
// -----------------------------------------------------------------------

type e2eToolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type e2eToolCallResultContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type e2eToolCallResult struct {
	Content []e2eToolCallResultContent `json:"content"`
	IsError bool                       `json:"isError,omitempty"`
}

func e2eCallTool(t *testing.T, srvURL, tool, projectID, token string, args any) json.RawMessage {
	t.Helper()

	argsRaw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal %s arguments: %v", tool, err)
	}
	m := map[string]json.RawMessage{}
	if len(argsRaw) > 0 && string(argsRaw) != "null" {
		if err := json.Unmarshal(argsRaw, &m); err != nil {
			t.Fatalf("decode %s arguments: %v", tool, err)
		}
	}
	pidJSON, err := json.Marshal(projectID)
	if err != nil {
		t.Fatalf("marshal project_id: %v", err)
	}
	m["project_id"] = pidJSON
	mergedArgs, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal merged %s arguments: %v", tool, err)
	}

	params, err := json.Marshal(e2eToolsCallParams{Name: tool, Arguments: mergedArgs})
	if err != nil {
		t.Fatalf("marshal %s params: %v", tool, err)
	}
	reqBody, err := json.Marshal(mcp.RPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "tools/call",
		Params:  params,
	})
	if err != nil {
		t.Fatalf("marshal %s rpc request: %v", tool, err)
	}

	req, err := http.NewRequest(http.MethodPost, srvURL+"/mcp", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("build %s request: %v", tool, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("call %s: %v", tool, err)
	}
	defer resp.Body.Close()

	var rpcResp mcp.RPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		t.Fatalf("decode %s rpc response: %v", tool, err)
	}
	if rpcResp.Error != nil {
		t.Fatalf("%s rpc error: code=%d message=%s", tool, rpcResp.Error.Code, rpcResp.Error.Message)
	}

	resultRaw, err := json.Marshal(rpcResp.Result)
	if err != nil {
		t.Fatalf("marshal %s rpc result: %v", tool, err)
	}
	var result e2eToolCallResult
	if err := json.Unmarshal(resultRaw, &result); err != nil {
		t.Fatalf("decode %s tool call result: %v", tool, err)
	}
	if result.IsError {
		t.Fatalf("%s tool error: %s", tool, result.Content[0].Text)
	}
	if len(result.Content) == 0 {
		t.Fatalf("%s: empty tool result content", tool)
	}
	return json.RawMessage(result.Content[0].Text)
}

// e2eStartCoordServer builds and starts a real Coordination Server (real
// stores, real Postgres, the exact registry from main.go) and returns its
// httptest.Server plus a registered agent's token/project for wiring
// wormholed's credentials in Leg 2.
func e2eStartCoordServer(t *testing.T, db *sql.DB) (srvURL, projectID, agentID, token string) {
	t.Helper()

	identityStore := identity.NewStore(db)
	eventsStore := events.NewStore(db)
	tasksStore := tasks.NewStore(db, eventsStore)
	gitStore := git.NewStore(db)
	kbStore := kb.NewStore(db, kb.StubEmbedder{}, 0.85, 4000, 1, 1, 1)
	rolesStore := roles.NewStore(db)

	syncRateLimiter := mcp.NewSyncRateLimiter(30, time.Minute)

	registry := mcp.NewRegistry()
	registry.Register(mcp.RegisterAgentTool(identityStore, eventsStore, rolesStore, kbStore))
	registry.Register(mcp.WhoAmITool())
	registry.Register(mcp.CreateTaskTool(tasksStore))
	registry.Register(mcp.AssignTaskTool(tasksStore))
	registry.Register(mcp.ListTasksTool(tasksStore, rolesStore))
	registry.Register(mcp.UpdateTaskStatusTool(tasksStore))
	registry.Register(mcp.CreateChannelTool(eventsStore))
	registry.Register(mcp.PostEventTool(eventsStore))
	registry.Register(mcp.SubscribeChannelTool(eventsStore))
	registry.Register(mcp.ListChannelsTool(eventsStore))
	registry.Register(mcp.LinkCommitTool(gitStore))
	registry.Register(mcp.RequestReviewTool(gitStore))
	registry.Register(mcp.WriteArticleTool(kbStore))
	registry.Register(mcp.SearchArticlesTool(kbStore))
	registry.Register(mcp.GetArticleTool(kbStore))
	registry.Register(mcp.GetArticleLinksTool(kbStore))
	registry.Register(mcp.BootstrapTool(tasksStore, kbStore, eventsStore, syncRateLimiter))
	registry.Register(mcp.IncrementalPullTool(tasksStore, kbStore, eventsStore, syncRateLimiter))
	registry.Register(mcp.IncrementalPushTool(tasksStore, kbStore, eventsStore, syncRateLimiter))
	registry.Register(mcp.ConflictReportTool(tasksStore, kbStore, eventsStore, syncRateLimiter))

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", mcp.NewMCPHandler(registry, identityStore))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	projectID = e2eMustCreateProject(t, db, "e2e-stdio-bridge-project")

	registerResultRaw := e2eCallTool(t, srv.URL, "wormhole.agent.register", projectID, "", mcp.RegisterAgentInput{
		Permissions:  []string{"task.create", "task.assign", "task.list", "kb.write", "channel.create", "channel.post"},
		Owner:        "harley",
		Model:        "claude",
		Capabilities: []string{"code_review"},
	})
	var registerOut mcp.RegisterAgentOutput
	if err := json.Unmarshal(registerResultRaw, &registerOut); err != nil {
		t.Fatalf("decode register result: %v", err)
	}
	if registerOut.AgentID == "" || registerOut.Token == "" {
		t.Fatalf("register output missing fields: %+v", registerOut)
	}

	return srv.URL, projectID, registerOut.AgentID, registerOut.Token
}

// -----------------------------------------------------------------------
// Leg 3: the stdio transport. This is the piece no existing test exercises.
// It speaks newline-delimited JSON-RPC directly against the built
// `wormhole mcp` subprocess's stdin/stdout -- matching that bridge's own
// framing (cmd/wormhole/mcp.go stdinToSocket/socketToStdout: one JSON
// object per line, terminated by \n, no length header).
// wormholed_test.go's mcpInitialize/mcpCallTool helpers happen to use the
// same newline framing, but against a raw net.Conn to wormholed's socket;
// this test drives it through the real `wormhole mcp` subprocess's
// stdin/stdout instead, the leg a real harness talks to.
// -----------------------------------------------------------------------

var (
	stdioBridgeBinOnce sync.Once
	stdioBridgeBinPath string
	stdioBridgeBinErr  error
)

// e2eBuildStdioBridgeBinary builds the wormhole CLI once per test binary
// run and returns the path to the resulting executable. The stdio bridge is
// the `wormhole mcp` subcommand (cmd/wormhole/mcp.go); it was previously a
// standalone cmd/wormhole-mcp-stdio binary. No existing "go build a sibling
// binary for a subprocess test" helper exists anywhere in this repo
// (checked: no `_test.go` file anywhere invokes exec.Command with "go",
// "build"), so this is a new small helper local to this test file, not a
// new package.
func e2eBuildStdioBridgeBinary(t *testing.T) string {
	t.Helper()
	stdioBridgeBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "wormhole-mcp-stdio-bin-*")
		if err != nil {
			stdioBridgeBinErr = fmt.Errorf("mkdir temp: %w", err)
			return
		}
		binPath := filepath.Join(dir, "wormhole")
		cmd := exec.Command("go", "build", "-o", binPath, "./cmd/wormhole")
		cmd.Dir = repoRootForTest(t)
		out, err := cmd.CombinedOutput()
		if err != nil {
			stdioBridgeBinErr = fmt.Errorf("go build cmd/wormhole: %w\n%s", err, out)
			return
		}
		stdioBridgeBinPath = binPath
	})
	return stdioBridgeBinPath
}

// repoRootForTest returns the repo root (two levels up from
// cmd/wormholed, where this test file lives), so `go build ./cmd/...`
// resolves regardless of the working directory `go test` happens to use.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// cmd/wormholed -> repo root
	return filepath.Join(wd, "..", "..")
}

// e2eStdioClient wraps a running `wormhole mcp` subprocess and speaks
// newline-delimited JSON-RPC over its stdin/stdout pipes.
type e2eStdioClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	nextID int
}

// writeNewlineFrame is this test's client-side encoder for the MCP stdio
// transport's wire format. The `wormhole mcp` bridge (cmd/wormhole/mcp.go)
// relays newline-delimited JSON-RPC in both directions (stdinToSocket /
// socketToStdout), so each message is a single JSON object terminated by a
// newline, with no length header. The body itself must not contain a raw
// newline; encoding/json.Marshal never emits one, so a single trailing \n
// unambiguously frames the message.
func writeNewlineFrame(w io.Writer, body []byte) error {
	if _, err := w.Write(body); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

// readNewlineFrame is this test's client-side decoder, matching the bridge's
// socketToStdout framing (one JSON-RPC message per line). It reads up to and
// including the next newline and returns the trimmed body.
func readNewlineFrame(r *bufio.Reader) ([]byte, error) {
	line, err := r.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return nil, err
	}
	return []byte(strings.TrimRight(string(line), "\r\n")), nil
}

func e2eStartStdioBridge(t *testing.T, binPath, runDir string) *e2eStdioClient {
	t.Helper()

	cmd := exec.Command(binPath, "mcp")
	cmd.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+runDir)
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start wormhole mcp: %v", err)
	}

	c := &e2eStdioClient{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}
	t.Cleanup(func() {
		stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return c
}

type e2eStdioRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type e2eStdioRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type e2eStdioRPCResponse struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      json.RawMessage   `json:"id,omitempty"`
	Result  json.RawMessage   `json:"result,omitempty"`
	Error   *e2eStdioRPCError `json:"error,omitempty"`
}

// send writes a newline-framed JSON-RPC request/notification to the
// subprocess's stdin.
func (c *e2eStdioClient) send(t *testing.T, method string, params json.RawMessage, withID bool) json.RawMessage {
	t.Helper()
	req := e2eStdioRPCRequest{JSONRPC: "2.0", Method: method, Params: params}
	var id json.RawMessage
	if withID {
		c.nextID++
		id = json.RawMessage(strconv.Itoa(c.nextID))
		req.ID = id
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal %s request: %v", method, err)
	}
	if err := writeNewlineFrame(c.stdin, body); err != nil {
		t.Fatalf("write %s frame: %v", method, err)
	}
	return id
}

// call sends a newline-framed request and reads back the matching
// newline-framed response.
func (c *e2eStdioClient) call(t *testing.T, method string, params json.RawMessage) e2eStdioRPCResponse {
	t.Helper()
	c.send(t, method, params, true)
	body, err := readNewlineFrame(c.stdout)
	if err != nil {
		t.Fatalf("read %s response frame: %v", method, err)
	}
	var resp e2eStdioRPCResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode %s response %q: %v", method, body, err)
	}
	return resp
}

// notify sends a newline-framed notification (no response expected).
func (c *e2eStdioClient) notify(t *testing.T, method string) {
	t.Helper()
	c.send(t, method, nil, false)
}

func (c *e2eStdioClient) initialize(t *testing.T) {
	t.Helper()
	resp := c.call(t, "initialize", json.RawMessage(`{}`))
	if resp.Error != nil {
		t.Fatalf("initialize error: %+v", resp.Error)
	}
	c.notify(t, "notifications/initialized")
}

func (c *e2eStdioClient) callTool(t *testing.T, tool string, args map[string]interface{}) (json.RawMessage, string) {
	t.Helper()
	var argsRaw json.RawMessage
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			t.Fatalf("marshal %s args: %v", tool, err)
		}
		argsRaw = b
	} else {
		argsRaw = json.RawMessage(`{}`)
	}
	params, err := json.Marshal(e2eToolsCallParams{Name: tool, Arguments: argsRaw})
	if err != nil {
		t.Fatalf("marshal %s params: %v", tool, err)
	}
	resp := c.call(t, "tools/call", params)
	if resp.Error != nil {
		return nil, resp.Error.Message
	}
	var result e2eToolCallResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode %s tool call result: %v", tool, err)
	}
	if result.IsError {
		text := ""
		if len(result.Content) > 0 {
			text = result.Content[0].Text
		}
		return nil, text
	}
	if len(result.Content) == 0 {
		return nil, ""
	}
	return json.RawMessage(result.Content[0].Text), ""
}

// -----------------------------------------------------------------------
// The test.
// -----------------------------------------------------------------------

// TestE2E_StdioBridgeToPostgres drives a real MCP client through the real
// stdio bridge subprocess, through wormholed's Unix socket, through
// wormholed's MCP dispatch, into the local SQLite replica and sync queue,
// through the sync engine, into a real Coordination Server, and finally
// into real Postgres -- the full chain subtask 6 of issue #20 asks for,
// which no other test in this repo currently exercises (see file header).
//
// This test does NOT delete, modify, or attempt to fix the existing
// TestP7_* tests in p7_e2e_integration_test.go -- they remain queue-
// mechanics-only coverage via wormholed's internal test harness (direct
// sync.QueueRepo manipulation, or a direct socket dial for
// TestP7_MultiDaemonSync), a different and still-useful layer. This test
// is the complementary full-transport layer subtask 6 was written for.
func TestE2E_StdioBridgeToPostgres(t *testing.T) {
	db := e2eTestDB(t)

	binPath := e2eBuildStdioBridgeBinary(t)
	if stdioBridgeBinErr != nil {
		t.Fatalf("build wormhole mcp bridge: %v", stdioBridgeBinErr)
	}

	// --- Leg 1: real Coordination Server, real Postgres. ---
	coordURL, projectID, agentID, token := e2eStartCoordServer(t, db)
	var coordOnline atomic.Bool
	coordOnline.Store(true)
	coordProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !coordOnline.Load() {
			http.Error(w, "coordination server offline", http.StatusServiceUnavailable)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		upstream, err := http.NewRequestWithContext(r.Context(), r.Method, coordURL+r.URL.Path, bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		upstream.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(upstream)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		responseBody, _ := io.ReadAll(resp.Body)
		_, _ = w.Write(responseBody)
	}))
	defer coordProxy.Close()

	// --- Leg 2: real wormholed, in-process (mirrors
	// TestRun_EndToEndWhoAmI's env-var setup exactly). ---
	home := t.TempDir()
	t.Setenv("HOME", home)
	runDir := filepath.Join(home, "run")
	t.Setenv("XDG_RUNTIME_DIR", runDir)
	dataDir := filepath.Join(home, "data")
	t.Setenv("XDG_DATA_HOME", dataDir)

	credDir := filepath.Join(home, ".wormhole", "credentials")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatalf("mkdir credentials dir: %v", err)
	}
	credData, err := json.Marshal(map[string]string{
		"server": coordProxy.URL, "project_id": projectID, "agent_id": "e2e-agent", "token": token,
	})
	if err != nil {
		t.Fatalf("marshal credentials: %v", err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "default.json"), credData, 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}

	socketPath := filepath.Join(runDir, "wormhole", "wormholed.sock")
	daemon := startTestDaemon(t, "default", socketPath)
	defer daemon.stop(t)

	// --- Leg 3: the real transport. Spawn the stdio bridge subprocess
	// with XDG_RUNTIME_DIR pointed at the same runDir wormholed used, so
	// its own wormholedSocketPath() resolves to the same socket. Speak
	// genuine newline-delimited JSON-RPC over its stdin/stdout. ---
	client := e2eStartStdioBridge(t, binPath, runDir)
	client.initialize(t)
	listResp := client.call(t, "tools/list", json.RawMessage(`{}`))
	if listResp.Error != nil {
		t.Fatalf("tools/list error: %+v", listResp.Error)
	}
	if !bytes.Contains(listResp.Result, []byte(`"wormhole.task.create"`)) || !bytes.Contains(listResp.Result, []byte(`"wormhole.channel.create"`)) {
		t.Fatalf("tools/list missing durable write tools: %s", listResp.Result)
	}
	if _, errMsg := client.callTool(t, "wormhole.agent.whoami", nil); errMsg != "" {
		t.Fatalf("warm authenticated local scope: %s", errMsg)
	}
	if _, errMsg := client.callTool(t, "wormhole.agent.register", map[string]interface{}{
		"agent_id": agentID, "capabilities": []string{"code"},
	}); errMsg != "" {
		t.Fatalf("register local route agent: %s", errMsg)
	}
	coordOnline.Store(false)

	// Create a task through the full chain. Local-first writes never
	// block on Coordination Server reachability (confirmed by reading
	// internal/runtime/localapi/localapi.go's handleTaskCreate: it writes
	// via s.tr.CreateTask then unconditionally calls s.qr.Enqueue, with no
	// synchronous call to the Coordination Server in between), so this
	// single call proves client -> stdio bridge -> wormholed socket -> MCP
	// dispatch -> local store write -> sync queue enqueue all worked, and
	// the subsequent poll proves the sync engine delivered it onward.
	taskTitle := "e2e stdio bridge task"
	resultRaw, errMsg := client.callTool(t, "wormhole.task.create", map[string]interface{}{
		"title":       taskTitle,
		"description": "created through the real stdio bridge -> wormholed -> Postgres transport",
		"priority":    1,
	})
	if errMsg != "" {
		t.Fatalf("wormhole.task.create returned error: %s", errMsg)
	}
	var taskOut struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(resultRaw, &taskOut); err != nil {
		t.Fatalf("decode task create result %q: %v", resultRaw, err)
	}
	if taskOut.ID == "" {
		t.Fatalf("task create result missing id: %s", resultRaw)
	}
	if taskOut.Status != "todo" {
		t.Fatalf("task status = %q, want todo", taskOut.Status)
	}
	localReadRaw, errMsg := client.callTool(t, "wormhole.task.get", map[string]interface{}{"task_id": taskOut.ID})
	if errMsg != "" {
		t.Fatalf("local wormhole.task.get returned error: %s", errMsg)
	}
	if !bytes.Contains(localReadRaw, []byte(taskTitle)) {
		t.Fatalf("local task readback = %s, want title %q", localReadRaw, taskTitle)
	}
	var preRestartCount int
	if err := db.QueryRow(`SELECT count(*) FROM tasks WHERE id = $1`, taskOut.ID).Scan(&preRestartCount); err != nil {
		t.Fatalf("count offline task before restart: %v", err)
	}
	if preRestartCount != 0 {
		t.Fatalf("offline task reached Postgres before reconnect: count=%d", preRestartCount)
	}
	routeRaw, errMsg := client.callTool(t, "wormhole.task.route", map[string]interface{}{
		"capability": "code",
		"title":      "e2e routed owner fidelity",
	})
	if errMsg != "" {
		t.Fatalf("wormhole.task.route returned error: %s", errMsg)
	}
	var routeOut struct {
		TaskID     string `json:"task_id"`
		AssignedTo string `json:"assigned_to"`
		Status     string `json:"status"`
	}
	if err := json.Unmarshal(routeRaw, &routeOut); err != nil {
		t.Fatalf("decode routed task result %s: %v", routeRaw, err)
	}
	if routeOut.TaskID == "" || routeOut.AssignedTo != agentID || routeOut.Status != "todo" {
		t.Fatalf("routed task result = %+v, want assigned_to=%s status=todo", routeOut, agentID)
	}
	kbRaw, errMsg := client.callTool(t, "wormhole.kb.write", map[string]interface{}{
		"agent_id": agentID, "title": "e2e native JSON article", "body": "native frontmatter survives sync",
		"frontmatter": map[string]interface{}{"kind": "runbook", "nested": map[string]interface{}{"version": 2}},
	})
	if errMsg != "" {
		t.Fatalf("wormhole.kb.write returned error: %s", errMsg)
	}
	var kbOut struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(kbRaw, &kbOut); err != nil || kbOut.ID == "" {
		t.Fatalf("decode KB result %s: %v", kbRaw, err)
	}
	channelRaw, errMsg := client.callTool(t, "wormhole.channel.create", map[string]interface{}{"name": "e2e-native-json"})
	if errMsg != "" {
		t.Fatalf("wormhole.channel.create returned error: %s", errMsg)
	}
	var channelOut struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(channelRaw, &channelOut); err != nil || channelOut.ID == "" {
		t.Fatalf("decode channel result %s: %v", channelRaw, err)
	}
	eventRaw, errMsg := client.callTool(t, "wormhole.channel.post", map[string]interface{}{
		"channel_id": channelOut.ID, "agent_id": agentID, "event_type": "discovery.logged",
		"payload": map[string]interface{}{"found": true, "nested": map[string]interface{}{"count": 3}},
	})
	if errMsg != "" {
		t.Fatalf("wormhole.channel.post returned error: %s", errMsg)
	}
	var eventOut struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(eventRaw, &eventOut); err != nil || eventOut.ID == "" {
		t.Fatalf("decode event result %s: %v", eventRaw, err)
	}

	daemon.stop(t)
	bridgeExited := make(chan error, 1)
	go func() { bridgeExited <- client.cmd.Wait() }()
	select {
	case err := <-bridgeExited:
		if err != nil {
			t.Fatalf("original stdio bridge after daemon shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original stdio bridge did not exit after daemon shutdown")
	}

	// Simulate the exact overwrite risk this test guards against: remove the
	// local owner after routing while leaving the durable queue payload intact.
	// Only a server push that applies owner_agent_id followed by a server pull
	// can restore it.
	localStore, err := localstore.Open(filepath.Join(dataDir, "wormhole", "wormholed.db"))
	if err != nil {
		t.Fatalf("open local store between daemon runs: %v", err)
	}
	result, err := localStore.DB().Exec(`UPDATE tasks SET owner_agent_id = NULL WHERE id = ? AND namespace_id = ?`, routeOut.TaskID, projectID)
	if err != nil {
		localStore.Close()
		t.Fatalf("clear routed owner before reconnect: %v", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		localStore.Close()
		t.Fatalf("clear routed owner affected %d row(s), err=%v, want 1", affected, err)
	}
	if err := localStore.Close(); err != nil {
		t.Fatalf("close local store between daemon runs: %v", err)
	}

	coordOnline.Store(true)
	restarted := startTestDaemon(t, "default", socketPath)
	defer restarted.stop(t)
	reconnectedClient := e2eStartStdioBridge(t, binPath, runDir)
	reconnectedClient.initialize(t)
	restartedReadRaw, restartedErr := reconnectedClient.callTool(t, "wormhole.task.get", map[string]interface{}{"task_id": taskOut.ID})
	if restartedErr != "" || !bytes.Contains(restartedReadRaw, []byte(taskTitle)) {
		t.Fatalf("local task after daemon/bridge restart = %s error=%q", restartedReadRaw, restartedErr)
	}

	// --- Offline-write -> reconnect -> sync (P7 path), now through the
	// real transport: poll the real Coordination Server's own Postgres
	// directly for the task's arrival, bounded, not a fixed sleep. Default
	// sync batch interval is 5s (sync.DefaultConfig, internal/runtime/sync/sync.go),
	// so this proves the sync engine actually delivered the offline-queued
	// write to the real Coordination Server, which persisted it to real
	// Postgres. ---
	var gotTitle string
	waitForCondition(t, 20*time.Second, "stdio task to sync into Postgres", func() (bool, error) {
		err := db.QueryRow(`SELECT title FROM tasks WHERE id = $1 AND project_id = $2`, taskOut.ID, projectID).Scan(&gotTitle)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return err == nil, err
	})
	if gotTitle != taskTitle {
		t.Fatalf("task title in Postgres = %q, want %q", gotTitle, taskTitle)
	}
	var routedServerOwner sql.NullString
	waitForCondition(t, 20*time.Second, "routed owner to sync into Postgres", func() (bool, error) {
		err := db.QueryRow(`SELECT owner_agent_id FROM tasks WHERE id = $1 AND project_id = $2`, routeOut.TaskID, projectID).Scan(&routedServerOwner)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return routedServerOwner.Valid && routedServerOwner.String == agentID, nil
	})
	waitForCondition(t, 20*time.Second, "routed owner to return through incremental pull", func() (bool, error) {
		raw, toolErr := reconnectedClient.callTool(t, "wormhole.task.get", map[string]interface{}{"task_id": routeOut.TaskID})
		if toolErr != "" {
			return false, fmt.Errorf("task.get: %s", toolErr)
		}
		var task struct {
			OwnerAgentID *string `json:"owner_agent_id"`
		}
		if err := json.Unmarshal(raw, &task); err != nil {
			return false, err
		}
		return task.OwnerAgentID != nil && *task.OwnerAgentID == agentID, nil
	})
	var kbJSON, eventJSON []byte
	waitForCondition(t, 20*time.Second, "native event JSON to sync into Postgres", func() (bool, error) {
		if err := db.QueryRow(`SELECT payload FROM events WHERE id = $1 AND project_id = $2`, eventOut.ID, projectID).Scan(&eventJSON); errors.Is(err, sql.ErrNoRows) {
			return false, nil
		} else if err != nil {
			return false, err
		}
		return true, nil
	})
	waitForCondition(t, 20*time.Second, "native KB JSON to sync into Postgres", func() (bool, error) {
		if err := db.QueryRow(`SELECT frontmatter FROM kb_articles WHERE id = $1 AND project_id = $2`, kbOut.ID, projectID).Scan(&kbJSON); errors.Is(err, sql.ErrNoRows) {
			return false, nil
		} else if err != nil {
			return false, err
		}
		return true, nil
	})
	var gotKB, gotEvent map[string]interface{}
	if err := json.Unmarshal(kbJSON, &gotKB); err != nil {
		t.Fatalf("Postgres KB frontmatter is not an object: %s: %v", kbJSON, err)
	}
	if err := json.Unmarshal(eventJSON, &gotEvent); err != nil {
		t.Fatalf("Postgres event payload is not an object: %s: %v", eventJSON, err)
	}
	if gotKB["kind"] != "runbook" {
		t.Fatalf("Postgres KB frontmatter = %#v", gotKB)
	}
	if gotEvent["found"] != true {
		t.Fatalf("Postgres event payload = %#v", gotEvent)
	}

	// Secondary confirmation the task is visible through the Coordination
	// Server's own MCP surface too (wormhole.task.list), not just via a
	// raw SQL query -- proving the row landed server-side through the real
	// production read path as well.
	listResultRaw := e2eCallTool(t, coordURL, "wormhole.task.list", projectID, token, mcp.ListTasksInput{})
	var listOut mcp.ListTasksOutput
	if err := json.Unmarshal(listResultRaw, &listOut); err != nil {
		t.Fatalf("decode task list result: %v", err)
	}
	found := false
	routedFound := false
	for _, task := range listOut.Tasks {
		if task.TaskID == taskOut.ID {
			found = true
		}
		if task.TaskID == routeOut.TaskID && task.OwnerAgentID != nil && *task.OwnerAgentID == agentID {
			routedFound = true
		}
	}
	if !found {
		t.Fatalf("wormhole.task.list on Coordination Server did not include synced task %s: %+v", taskOut.ID, listOut.Tasks)
	}
	if !routedFound {
		t.Fatalf("wormhole.task.list did not preserve routed owner for task %s: %+v", routeOut.TaskID, listOut.Tasks)
	}
}

func TestE2E_StdioBridgeProtocolAndSignalBoundaries(t *testing.T) {
	binPath := e2eBuildStdioBridgeBinary(t)
	if stdioBridgeBinErr != nil {
		t.Fatalf("build wormhole mcp bridge: %v", stdioBridgeBinErr)
	}
	socketPath, factory := configureSecurityTestDaemon(t)
	runner := func(ctx context.Context, profileName string) error {
		return runWithSyncEngineFactory(ctx, profileName, factory)
	}
	daemon := startTestDaemonWithRunner(t, "default", socketPath, runner)
	defer daemon.stop(t)
	runDir := os.Getenv("XDG_RUNTIME_DIR")

	t.Run("partial JSON and oversized input", func(t *testing.T) {
		client := e2eStartStdioBridge(t, binPath, runDir)
		client.initialize(t)
		req, _ := json.Marshal(e2eStdioRPCRequest{JSONRPC: "2.0", ID: json.RawMessage("91"), Method: "tools/list", Params: json.RawMessage(`{}`)})
		mid := len(req) / 2
		if _, err := client.stdin.Write(req[:mid]); err != nil {
			t.Fatalf("write first partial request segment: %v", err)
		}
		if _, err := client.stdin.Write(req[mid:]); err != nil {
			t.Fatalf("write second partial request segment: %v", err)
		}
		if _, err := io.WriteString(client.stdin, "\n"); err != nil {
			t.Fatalf("terminate partial request: %v", err)
		}
		body, err := readNewlineFrame(client.stdout)
		if err != nil {
			t.Fatalf("read partial request response: %v", err)
		}
		var resp e2eStdioRPCResponse
		if err := json.Unmarshal(body, &resp); err != nil || resp.Error != nil {
			t.Fatalf("partial request response = %s decode=%v rpc=%+v", body, err, resp.Error)
		}

		oversized := bytes.Repeat([]byte("x"), (1<<20)+1)
		if err := writeNewlineFrame(client.stdin, oversized); err != nil {
			t.Fatalf("write oversized stdio input: %v", err)
		}
		body, err = readNewlineFrame(client.stdout)
		if err != nil {
			t.Fatalf("read oversized response: %v", err)
		}
		if err := json.Unmarshal(body, &resp); err != nil || resp.Error == nil || resp.Error.Code != -32700 {
			t.Fatalf("oversized response = %s decode=%v rpc=%+v", body, err, resp.Error)
		}
	})

	t.Run("concurrent clients", func(t *testing.T) {
		clients := make([]*e2eStdioClient, 4)
		for i := range clients {
			clients[i] = e2eStartStdioBridge(t, binPath, runDir)
			clients[i].initialize(t)
		}
		errs := make(chan error, len(clients))
		for i, client := range clients {
			go func(id int, c *e2eStdioClient) {
				req, _ := json.Marshal(e2eStdioRPCRequest{JSONRPC: "2.0", ID: json.RawMessage(strconv.Itoa(100 + id)), Method: "tools/list", Params: json.RawMessage(`{}`)})
				if err := writeNewlineFrame(c.stdin, req); err != nil {
					errs <- err
					return
				}
				body, err := readNewlineFrame(c.stdout)
				if err == nil {
					var resp e2eStdioRPCResponse
					err = json.Unmarshal(body, &resp)
					if err == nil && resp.Error != nil {
						err = fmt.Errorf("rpc error: %+v", resp.Error)
					}
				}
				errs <- err
			}(i, client)
		}
		for range clients {
			if err := <-errs; err != nil {
				t.Fatalf("concurrent stdio client: %v", err)
			}
		}
	})

	for _, signal := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			client := e2eStartStdioBridge(t, binPath, runDir)
			client.initialize(t)
			if _, err := io.WriteString(client.stdin, `{"jsonrpc":"2.0"`); err != nil {
				t.Fatalf("write in-flight partial request: %v", err)
			}
			if err := client.cmd.Process.Signal(signal); err != nil {
				t.Fatalf("signal stdio bridge: %v", err)
			}
			exited := make(chan error, 1)
			go func() { exited <- client.cmd.Wait() }()
			select {
			case err := <-exited:
				if err != nil {
					t.Fatalf("stdio bridge signal exit: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("stdio bridge did not exit after signal with request in flight")
			}
		})
	}
}
