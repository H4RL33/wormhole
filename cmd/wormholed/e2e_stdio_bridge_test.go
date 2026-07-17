// e2e_stdio_bridge_test.go
// Issue #20 subtask 6: proves the full transport chain a real MCP client
// (Claude Code or any other stdio-speaking harness) actually uses --
// stdio bridge subprocess (cmd/wormhole-mcp-stdio) -> wormholed's Unix
// socket -> wormholed's MCP dispatch -> local SQLite write + sync enqueue
// -> sync engine push -> real Coordination Server -> real Postgres.
//
// Every existing test in this repo (wormholed_test.go's
// TestRun_EndToEndWhoAmI, p7_e2e_integration_test.go's TestP7_*) dials
// wormholed's socket directly, bypassing the stdio bridge entirely. That
// bypass is exactly the gap this subtask exists to close, so Leg 3 below
// deliberately does NOT reuse wormholed_test.go's newline-delimited
// mcpInitialize/mcpCallTool helpers -- it speaks the stdio transport's
// Content-Length framing over the subprocess's stdin/stdout pipes, which
// is what a real harness (and cmd/wormhole-mcp-stdio on the other end)
// actually speaks.
package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
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
		Permissions:  []string{"task.create", "event.publish", "kb.write"},
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
// It speaks Content-Length-framed MCP directly against the built
// cmd/wormhole-mcp-stdio subprocess's stdin/stdout -- matching that
// binary's own readContentLengthFrame (cmd/wormhole-mcp-stdio/main.go)
// wire format ("Content-Length: %d\r\n\r\n<body>", no header besides
// Content-Length required, body has no trailing newline requirement).
// wormholed_test.go's mcpInitialize/mcpCallTool helpers are NOT reused
// here: those write "<json>\n" straight onto a raw net.Conn, which is
// wormholed's *socket* framing, not the stdio transport's framing a real
// harness or cmd/wormhole-mcp-stdio speaks on its own stdin/stdout.
// -----------------------------------------------------------------------

var (
	stdioBridgeBinOnce sync.Once
	stdioBridgeBinPath string
	stdioBridgeBinErr  error
)

// e2eBuildStdioBridgeBinary builds cmd/wormhole-mcp-stdio once per test
// binary run and returns the path to the resulting executable. No existing
// "go build a sibling binary for a subprocess test" helper exists anywhere
// in this repo (checked: no `_test.go` file anywhere invokes exec.Command
// with "go", "build"), so this is a new small helper local to this test
// file, not a new package.
func e2eBuildStdioBridgeBinary(t *testing.T) string {
	t.Helper()
	stdioBridgeBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "wormhole-mcp-stdio-bin-*")
		if err != nil {
			stdioBridgeBinErr = fmt.Errorf("mkdir temp: %w", err)
			return
		}
		binPath := filepath.Join(dir, "wormhole-mcp-stdio")
		cmd := exec.Command("go", "build", "-o", binPath, "./cmd/wormhole-mcp-stdio")
		cmd.Dir = repoRootForTest(t)
		out, err := cmd.CombinedOutput()
		if err != nil {
			stdioBridgeBinErr = fmt.Errorf("go build cmd/wormhole-mcp-stdio: %w\n%s", err, out)
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

// e2eStdioClient wraps a running cmd/wormhole-mcp-stdio subprocess and
// speaks real Content-Length-framed MCP over its stdin/stdout pipes.
type e2eStdioClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	nextID int
}

// writeContentLengthFrame is this test's own client-side encoder for the
// MCP stdio transport's wire format -- deliberately not reusing anything
// from wormholed_test.go, since that file's helpers write newline-delimited
// JSON onto a raw socket connection, a different wire format entirely.
func writeContentLengthFrame(w io.Writer, body []byte) error {
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := io.WriteString(w, frame); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}

// readContentLengthFrame is this test's own client-side decoder, matching
// cmd/wormhole-mcp-stdio/main.go's readContentLengthFrame wire format
// (header block terminated by a blank line, then exactly Content-Length
// body bytes). Reimplemented here rather than imported, since the
// production function is unexported in package main of a different binary.
func readContentLengthFrame(r *bufio.Reader) ([]byte, error) {
	contentLength := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			continue
		}
		n, convErr := strconv.Atoi(strings.TrimSpace(value))
		if convErr != nil {
			return nil, fmt.Errorf("malformed Content-Length header %q: %w", line, convErr)
		}
		contentLength = n
	}
	if contentLength < 0 {
		return nil, fmt.Errorf("message frame missing Content-Length header")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

func e2eStartStdioBridge(t *testing.T, binPath, runDir string) *e2eStdioClient {
	t.Helper()

	cmd := exec.Command(binPath)
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
		t.Fatalf("start wormhole-mcp-stdio: %v", err)
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
	JSONRPC string           `json:"jsonrpc"`
	ID      json.RawMessage  `json:"id,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *e2eStdioRPCError `json:"error,omitempty"`
}

// send writes a Content-Length-framed JSON-RPC request/notification to the
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
	if err := writeContentLengthFrame(c.stdin, body); err != nil {
		t.Fatalf("write %s frame: %v", method, err)
	}
	return id
}

// call sends a Content-Length-framed request and reads back the matching
// Content-Length-framed response.
func (c *e2eStdioClient) call(t *testing.T, method string, params json.RawMessage) e2eStdioRPCResponse {
	t.Helper()
	c.send(t, method, params, true)
	body, err := readContentLengthFrame(c.stdout)
	if err != nil {
		t.Fatalf("read %s response frame: %v", method, err)
	}
	var resp e2eStdioRPCResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode %s response %q: %v", method, body, err)
	}
	return resp
}

// notify sends a Content-Length-framed notification (no response expected).
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
		t.Fatalf("build wormhole-mcp-stdio: %v", stdioBridgeBinErr)
	}

	// --- Leg 1: real Coordination Server, real Postgres. ---
	coordURL, projectID, _, token := e2eStartCoordServer(t, db)

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
		"server": coordURL, "project_id": projectID, "agent_id": "e2e-agent", "token": token,
	})
	if err != nil {
		t.Fatalf("marshal credentials: %v", err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "default.json"), credData, 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, "default") }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
			t.Log("wormholed did not shut down within 5s of cancel")
		}
	})

	socketPath := filepath.Join(runDir, "wormhole", "wormholed.sock")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("wormholed socket did not appear at %s within deadline", socketPath)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// --- Leg 3: the real transport. Spawn the stdio bridge subprocess
	// with XDG_RUNTIME_DIR pointed at the same runDir wormholed used, so
	// its own wormholedSocketPath() resolves to the same socket. Speak
	// genuine Content-Length-framed MCP over its stdin/stdout. ---
	client := e2eStartStdioBridge(t, binPath, runDir)
	client.initialize(t)

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

	// --- Offline-write -> reconnect -> sync (P7 path), now through the
	// real transport: poll the real Coordination Server's own Postgres
	// directly for the task's arrival, bounded, not a fixed sleep. Default
	// sync batch interval is 5s (sync.DefaultConfig, internal/runtime/sync/sync.go),
	// so this proves the sync engine actually delivered the offline-queued
	// write to the real Coordination Server, which persisted it to real
	// Postgres. ---
	var gotTitle string
	pollDeadline := time.Now().Add(20 * time.Second)
	for {
		err := db.QueryRow(`SELECT title FROM tasks WHERE id = $1 AND project_id = $2`, taskOut.ID, projectID).Scan(&gotTitle)
		if err == nil {
			break
		}
		if err != sql.ErrNoRows {
			t.Fatalf("query task %s from Postgres: %v", taskOut.ID, err)
		}
		if time.Now().After(pollDeadline) {
			t.Fatalf("task %s did not appear in Postgres via sync within deadline", taskOut.ID)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if gotTitle != taskTitle {
		t.Fatalf("task title in Postgres = %q, want %q", gotTitle, taskTitle)
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
	for _, task := range listOut.Tasks {
		if task.TaskID == taskOut.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("wormhole.task.list on Coordination Server did not include synced task %s: %+v", taskOut.ID, listOut.Tasks)
	}
}
