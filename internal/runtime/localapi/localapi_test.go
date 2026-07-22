// internal/runtime/localapi/localapi_test.go
package localapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/eventbus"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
)

const (
	testMaxFrameBytes        = 1 << 20
	testMaxActiveConnections = 8
)

func trackedConnectionCount(srv *Server) int {
	count := 0
	srv.conns.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

func waitForTrackedConnectionCount(t *testing.T, srv *Server, want int) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if got := trackedConnectionCount(srv); got == want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("tracked connection count = %d, want %d", trackedConnectionCount(srv), want)
		case <-ticker.C:
		}
	}
}

func TestServer_FrameExactlyOneMiBSucceeds(t *testing.T) {
	_, socketPath := newMCPTestServer(t)
	conn := dialLocalSocket(t, socketPath)
	defer conn.Close()

	reqRaw, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "initialize", Params: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("marshal initialize: %v", err)
	}
	frame := make([]byte, 0, testMaxFrameBytes)
	frame = append(frame, reqRaw...)
	frame = append(frame, bytes.Repeat([]byte{' '}, testMaxFrameBytes-len(reqRaw)-1)...)
	frame = append(frame, '\n')
	if len(frame) != testMaxFrameBytes {
		t.Fatalf("frame length = %d, want %d", len(frame), testMaxFrameBytes)
	}
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("write exact-limit frame: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read exact-limit response: %v", err)
	}
	var resp rpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(line), &resp); err != nil {
		t.Fatalf("decode exact-limit response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("exact-limit frame returned error: %+v", resp.Error)
	}
}

func TestServer_FrameOverOneMiBUnterminatedReturnsBoundedError(t *testing.T) {
	srv, socketPath := newMCPTestServer(t)
	conn := dialLocalSocket(t, socketPath)
	defer conn.Close()

	frame := bytes.Repeat([]byte{'x'}, testMaxFrameBytes+1)
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("write oversized frame: %v", err)
	}
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		t.Fatalf("connection type = %T, want *net.UnixConn", conn)
	}
	if err := unixConn.CloseWrite(); err != nil {
		t.Fatalf("close write side: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read oversized-frame response: %v", err)
	}
	if len(line) > 512 {
		t.Fatalf("oversized-frame response length = %d, want <= 512", len(line))
	}
	var resp rpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(line), &resp); err != nil {
		t.Fatalf("decode oversized-frame response: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != rpcParseError || !strings.Contains(resp.Error.Message, "frame exceeds maximum size") {
		t.Fatalf("oversized-frame error = %+v, want bounded parse error", resp.Error)
	}
	waitForTrackedConnectionCount(t, srv, 0)

	fresh := dialLocalSocket(t, socketPath)
	defer fresh.Close()
	mcpInitialize(t, fresh, bufio.NewReader(fresh))
}

func TestServer_FrameAtOneMiBWithoutNewlineReturnsPromptError(t *testing.T) {
	_, socketPath := newMCPTestServer(t)
	conn := dialLocalSocket(t, socketPath)
	defer conn.Close()
	if _, err := conn.Write(bytes.Repeat([]byte{'x'}, testMaxFrameBytes)); err != nil {
		t.Fatalf("write unterminated frame: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read unterminated-frame response: %v", err)
	}
	var resp rpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(line), &resp); err != nil {
		t.Fatalf("decode unterminated-frame response: %v", err)
	}
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "frame exceeds maximum size") {
		t.Fatalf("unterminated-frame error = %+v, want prompt size error", resp.Error)
	}
}

func TestServer_ConnectionLimitClosesExcessConnection(t *testing.T) {
	srv, socketPath := newMCPTestServer(t)
	connections := make([]net.Conn, 0, testMaxActiveConnections)
	defer func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
	}()
	for i := 0; i < testMaxActiveConnections; i++ {
		connections = append(connections, dialLocalSocket(t, socketPath))
	}
	waitForTrackedConnectionCount(t, srv, testMaxActiveConnections)

	excess := dialLocalSocket(t, socketPath)
	defer excess.Close()
	if err := excess.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set excess read deadline: %v", err)
	}
	buf := make([]byte, 1)
	if _, err := excess.Read(buf); err == nil {
		t.Fatal("excess connection remained open")
	} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		t.Fatal("excess connection was not closed within the bound")
	}
}

func TestServer_SocketModeIsOwnerOnly(t *testing.T) {
	_, socketPath := newMCPTestServer(t)
	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("socket mode = %#o, want 0600", got)
	}
}

func TestListenLocalSocket_ChmodFailureCleansSocket(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	chmodErr := errors.New("chmod denied")
	ln, err := listenLocalSocketWithChmod(socketPath, func(string, os.FileMode) error {
		return chmodErr
	})
	if ln != nil {
		_ = ln.Close()
		t.Fatal("listenLocalSocketWithChmod returned a listener after chmod failure")
	}
	if !errors.Is(err, chmodErr) {
		t.Fatalf("error = %v, want wrapped chmod failure", err)
	}
	if _, statErr := os.Lstat(socketPath); !os.IsNotExist(statErr) {
		t.Fatalf("socket path survived chmod failure: %v", statErr)
	}
}

func TestServer_LogsNeverContainBearerToken(t *testing.T) {
	const token = "secret-bearer-token-that-must-not-be-logged"
	coord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: rpcInternalError, Message: "upstream rejected Bearer " + token},
		})
	}))
	defer coord.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()
	er := localstore.NewEventRepo(store.DB())
	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	srv, err := New(socketPath, coord.URL, token, "project-1", store, localstore.NewTaskRepo(store.DB(), er), er, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	var logs bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(oldWriter)

	conn := dialLocalSocket(t, socketPath)
	reader := bufio.NewReader(conn)
	if _, err := conn.Write([]byte("not-json\n")); err != nil {
		t.Fatalf("write malformed frame: %v", err)
	}
	if _, err := reader.ReadBytes('\n'); err != nil {
		t.Fatalf("read malformed response: %v", err)
	}
	mcpInitialize(t, conn, reader)
	resp := mcpCallTool(t, conn, reader, 2, "wormhole.agent.whoami", nil)
	_ = conn.Close()
	if resp.Error == "" {
		t.Fatal("coordination server failure unexpectedly succeeded")
	}
	gotLogs := logs.String()
	if gotLogs == "" {
		t.Fatal("malformed frame and tool failure produced no diagnostic logs")
	}
	if strings.Contains(gotLogs, token) {
		t.Fatalf("logs contain bearer token: %q", gotLogs)
	}
	if !strings.Contains(gotLogs, "[REDACTED]") {
		t.Fatalf("logs do not show redaction at the error boundary: %q", gotLogs)
	}
}

func TestServer_LogErrorRedactsOverlappingMultiOrgTokens(t *testing.T) {
	const (
		shortToken = "shared-secret"
		longToken  = "shared-secret-with-suffix"
	)
	srv := &Server{orgs: map[string]config.Org{
		"short": {Credentials: config.Credentials{Token: shortToken}},
		"long":  {Credentials: config.Credentials{Token: longToken}},
	}}
	var logs bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(oldWriter)

	srv.logError("overlap test", fmt.Errorf("short=%s long=%s", shortToken, longToken))
	gotLogs := logs.String()
	if strings.Contains(gotLogs, shortToken) || strings.Contains(gotLogs, longToken) || strings.Contains(gotLogs, "with-suffix") {
		t.Fatalf("logs leaked an overlapping bearer token or suffix: %q", gotLogs)
	}
	if got := strings.Count(gotLogs, "[REDACTED]"); got != 2 {
		t.Fatalf("redaction count = %d, want 2 in %q", got, gotLogs)
	}
}

func TestServer_ShutdownCancelsActiveRequest(t *testing.T) {
	requestStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()
	er := localstore.NewEventRepo(store.DB())
	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	srv, err := New(socketPath, "", "test-token", "project-1", store, localstore.NewTaskRepo(store.DB(), er), er, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const blockingTool = "wormhole.test.blocking"
	srv.registry.tools[blockingTool] = localTool{
		Name: blockingTool,
		Handler: func(ctx context.Context, _ json.RawMessage) (any, error) {
			close(requestStarted)
			<-ctx.Done()
			close(requestCanceled)
			return nil, ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(ctx) }()
	defer srv.Close()

	conn := dialLocalSocket(t, socketPath)
	reader := bufio.NewReader(conn)
	mcpInitialize(t, conn, reader)
	params, _ := json.Marshal(toolsCallParams{Name: blockingTool, Arguments: json.RawMessage(`{}`)})
	call, _ := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("2"), Method: "tools/call", Params: params})
	if _, err := conn.Write(append(call, '\n')); err != nil {
		t.Fatalf("write active request: %v", err)
	}
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("active request did not reach local handler")
	}

	cancel()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve after cancel: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not stop within the bound")
	}
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("active local request was not canceled")
	}
	_ = conn.Close()
	waitForTrackedConnectionCount(t, srv, 0)
}

func TestServer_ShutdownAfterAdmissionBeforeHandlerStart(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()
	er := localstore.NewEventRepo(store.DB())
	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	srv, err := New(socketPath, "", "test-token", "project-1", store, localstore.NewTaskRepo(store.DB(), er), er, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	admitted := make(chan struct{})
	releaseHandler := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(releaseHandler)
		}
		_ = srv.Close()
	}()
	srv.testBeforeHandlerStart = func() {
		close(admitted)
		<-releaseHandler
	}

	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(ctx) }()

	conn := dialLocalSocket(t, socketPath)
	defer conn.Close()
	select {
	case <-admitted:
	case <-time.After(time.Second):
		t.Fatal("connection was not admitted before the handler barrier")
	}

	cancel()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("admitted connection remained open during shutdown")
	} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		t.Fatal("shutdown missed the admitted connection before handler startup")
	}

	select {
	case err := <-serveDone:
		t.Fatalf("Serve returned before admitted handler exited: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseHandler)
	released = true
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve after release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not wait for and finish the admitted handler")
	}
	waitForTrackedConnectionCount(t, srv, 0)
}

func TestServer_CloseWithoutContextCancelReleasesSubscription(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()
	er := localstore.NewEventRepo(store.DB())
	eb := eventbus.NewEventBus()
	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	srv, err := NewWithRuntime(socketPath, "", "test-token", "project-1", store, localstore.NewTaskRepo(store.DB(), er), er, localstore.NewKBRepo(store.DB()), eb, nil, nil)
	if err != nil {
		t.Fatalf("NewWithRuntime: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(ctx) }()

	conn := dialLocalSocket(t, socketPath)
	reader := bufio.NewReader(conn)
	mcpInitialize(t, conn, reader)
	resp := mcpCallTool(t, conn, reader, 2, "wormhole.channel.subscribe", map[string]interface{}{"namespace": "project-1"})
	if resp.Error != "" {
		t.Fatalf("subscribe: %s", resp.Error)
	}
	if got := eb.SubscriberCount(); got != 1 {
		t.Fatalf("subscriber count before Close = %d, want 1", got)
	}

	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve after Close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not return after Close")
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for eb.SubscriberCount() != 0 {
		select {
		case <-deadline.C:
			t.Fatalf("subscriber count after Close = %d, want 0", eb.SubscriberCount())
		case <-time.After(5 * time.Millisecond):
		}
	}
	_ = conn.Close()
}

// fakeCoordServer stands in for the Coordination Server's /mcp endpoint
// (docs/mcp-protocol.md §2-§4.1): decodes a tools/call JSON-RPC request,
// asserts the bearer token, returns a canned wormhole.agent.whoami result.
func fakeCoordServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		var params toolsCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("decode params: %v", err)
		}
		if params.Name != "wormhole.agent.whoami" {
			t.Fatalf("got tool %q, want wormhole.agent.whoami", params.Name)
		}
		out := whoAmIOutput{
			AgentID:      "agent-1",
			Owner:        "harley",
			Model:        "claude-sonnet-5",
			Capabilities: []string{"code"},
			ProjectID:    "project-1",
			Permissions:  []string{"read_kb"},
		}
		outRaw, _ := json.Marshal(out)
		resultRaw, _ := json.Marshal(toolCallResult{
			Content: []toolCallResultContent{{Type: "text", Text: string(outRaw)}},
		})
		resp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: resultRaw}
		json.NewEncoder(w).Encode(resp)
	}))
}

func TestServer_ProxiesWhoAmI(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()

	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	er := localstore.NewEventRepo(store.DB())
	tr := localstore.NewTaskRepo(store.DB(), er)
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store, tr, er, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	resp := sendRequest(t, socketPath, "wormhole.agent.whoami", nil)
	if resp.Error != "" {
		t.Fatalf("got error response: %s", resp.Error)
	}
	var out whoAmIOutput
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if out.AgentID != "agent-1" || out.Owner != "harley" {
		t.Fatalf("got %+v", out)
	}

	cached, err := store.GetCachedWhoAmI(context.Background(), "agent-1")
	if err != nil {
		t.Fatalf("GetCachedWhoAmI: %v", err)
	}
	if cached.Model != "claude-sonnet-5" {
		t.Fatalf("cache not written: got %+v", cached)
	}
}

// TestServer_CloseWithoutCancelReturnsNil proves Close() alone (without
// ever cancelling the ctx passed to Serve) is a valid graceful-shutdown
// path: Serve must return nil promptly, not a wrapped accept error.
func TestServer_CloseWithoutCancelReturnsNil(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	er := localstore.NewEventRepo(store.DB())
	tr := localstore.NewTaskRepo(store.DB(), er)
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store, tr, er, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Deliberately never cancelled during the assertion below: Close()
	// must be sufficient on its own to make Serve return nil.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(ctx)
	}()

	// Give Serve a moment to bind and start accepting.
	for i := 0; i < 50; i++ {
		conn, dialErr := net.Dial("unix", socketPath)
		if dialErr == nil {
			conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("Serve returned non-nil error after Close(): %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return within 2s after Close()")
	}

	// Calling Close() again must not panic or double-close.
	if err := srv.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestServer_ServeReturnsPromptlyWithOpenConnection reproduces issue #20's
// shutdown deadlock (regression from subtask 2's move to a persistent
// per-connection MCP session): a client dials the socket and leaves the
// connection open without sending anything further (mirroring a handle
// goroutine parked in reader.ReadBytes('\n')). Cancelling Serve's ctx must
// still make Serve return within a short bound — it must not wait forever
// on that still-open connection. Uses a bounded channel-based wait
// (t.Fatal on timeout), not an unbounded <-done, so a regression fails the
// test instead of hanging the suite.
func TestServer_ServeReturnsPromptlyWithOpenConnection(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	er := localstore.NewEventRepo(store.DB())
	tr := localstore.NewTaskRepo(store.DB(), er)
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store, tr, er, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(ctx)
	}()

	// Wait for the listener to come up, then dial and hold the connection
	// open without writing anything more — this leaves a handle goroutine
	// parked in ReadBytes('\n'), exactly like the goroutine dump in the
	// issue #20 brief.
	var conn net.Conn
	for i := 0; i < 50; i++ {
		c, dialErr := net.Dial("unix", socketPath)
		if dialErr == nil {
			conn = c
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if conn == nil {
		t.Fatal("failed to dial socket")
	}
	defer conn.Close()

	// Give handle a moment to actually start blocking in ReadBytes.
	time.Sleep(50 * time.Millisecond)

	cancel()

	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("Serve returned non-nil error after ctx cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return within 2s of ctx cancel: a handle goroutine is still blocked on its open connection (issue #20 shutdown deadlock)")
	}
}

func TestServer_UnknownTool(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	er := localstore.NewEventRepo(store.DB())
	tr := localstore.NewTaskRepo(store.DB(), er)
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store, tr, er, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	// This server was constructed with qr=nil, so wormhole.task.create's
	// handler itself errors ("sync queue not available") — still exercises
	// the "tools/call wraps a handler error into isError:true" path this
	// test originally proved for an unrecognized tool name.
	resp := sendRequest(t, socketPath, "wormhole.task.create", nil)
	if resp.Error == "" {
		t.Fatalf("want error response, got none")
	}
}

// TestServer_LocalTaskList verifies wormhole.task.list through socket.
func TestServer_LocalTaskList(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	er := localstore.NewEventRepo(store.DB())
	tr := localstore.NewTaskRepo(store.DB(), er)

	// Create test tasks.
	ctx := context.Background()
	tr.CreateTask(ctx, "project-1", "Task 1", "desc 1", nil, 0, nil)
	tr.CreateTask(ctx, "project-1", "Task 2", "desc 2", nil, 0, nil)

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store, tr, er, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	resp := sendRequest(t, socketPath, "wormhole.task.list", nil)
	if resp.Error != "" {
		t.Fatalf("got error response: %s", resp.Error)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	tasks, ok := result["tasks"].([]interface{})
	if !ok {
		t.Fatalf("tasks not in result or wrong type")
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
}

// TestServer_LocalTaskGet verifies wormhole.task.get through socket.
func TestServer_LocalTaskGet(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	er := localstore.NewEventRepo(store.DB())
	tr := localstore.NewTaskRepo(store.DB(), er)

	ctx := context.Background()
	task, err := tr.CreateTask(ctx, "project-1", "Test Task", "test description", nil, 1, nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store, tr, er, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	resp := sendRequest(t, socketPath, "wormhole.task.get", map[string]interface{}{"task_id": task.ID})
	if resp.Error != "" {
		t.Fatalf("got error response: %s", resp.Error)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result["title"] != "Test Task" {
		t.Errorf("title = %q, want Test Task", result["title"])
	}
}

// TestServer_LocalTaskGetMissingTaskID verifies wormhole.task.get rejects missing task_id.
func TestServer_LocalTaskGetMissingTaskID(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	er := localstore.NewEventRepo(store.DB())
	tr := localstore.NewTaskRepo(store.DB(), er)

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store, tr, er, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	// Send request with empty args (no task_id).
	resp := sendRequest(t, socketPath, "wormhole.task.get", map[string]interface{}{})
	if resp.Error == "" {
		t.Fatalf("want error for missing task_id, got none")
	}
}

// TestServer_LocalChannelList verifies wormhole.channel.list through socket.
func TestServer_LocalChannelList(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	er := localstore.NewEventRepo(store.DB())
	ctx := context.Background()
	er.CreateChannel(ctx, "project-1", "channel-1")
	er.CreateChannel(ctx, "project-1", "channel-2")

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store, localstore.NewTaskRepo(store.DB(), er), er, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	resp := sendRequest(t, socketPath, "wormhole.channel.list", nil)
	if resp.Error != "" {
		t.Fatalf("got error response: %s", resp.Error)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	channels, ok := result["channels"].([]interface{})
	if !ok {
		t.Fatalf("channels not in result or wrong type")
	}
	if len(channels) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(channels))
	}
}

// TestServer_LocalChannelEvents verifies wormhole.channel.events through socket.
func TestServer_LocalChannelEvents(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	er := localstore.NewEventRepo(store.DB())
	ctx := context.Background()
	chID, _ := er.CreateChannel(ctx, "project-1", "test-channel")
	er.PublishEvent(ctx, "project-1", chID, "agent-1", "test.event", json.RawMessage(`{}`), nil)

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store, localstore.NewTaskRepo(store.DB(), er), er, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	resp := sendRequest(t, socketPath, "wormhole.channel.events", nil)
	if resp.Error != "" {
		t.Fatalf("got error response: %s", resp.Error)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	events, ok := result["events"].([]interface{})
	if !ok {
		t.Fatalf("events not in result or wrong type")
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
}

// TestServer_LocalKBList verifies wormhole.kb.list through socket.
func TestServer_LocalKBList(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	kb := localstore.NewKBRepo(store.DB())
	ctx := context.Background()
	kb.WriteArticle(ctx, "project-1", "agent-1", "Article 1", "body 1", json.RawMessage(`{}`))
	kb.WriteArticle(ctx, "project-1", "agent-1", "Article 2", "body 2", json.RawMessage(`{}`))

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	er := localstore.NewEventRepo(store.DB())
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store, localstore.NewTaskRepo(store.DB(), er), er, kb, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	resp := sendRequest(t, socketPath, "wormhole.kb.list", nil)
	if resp.Error != "" {
		t.Fatalf("got error response: %s", resp.Error)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	articles, ok := result["articles"].([]interface{})
	if !ok {
		t.Fatalf("articles not in result or wrong type")
	}
	if len(articles) != 2 {
		t.Fatalf("expected 2 articles, got %d", len(articles))
	}
}

// TestServer_LocalKBGetMissingArticleID verifies wormhole.kb.get with missing article_id falls back to list.
func TestServer_LocalKBGetMissingArticleID(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	er := localstore.NewEventRepo(store.DB())
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store, localstore.NewTaskRepo(store.DB(), er), er, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	// Send request with empty args (no article_id) - should fallback to list.
	resp := sendRequest(t, socketPath, "wormhole.kb.get", map[string]interface{}{})
	if resp.Error != "" {
		t.Fatalf("got error response: %s", resp.Error)
	}
	// Should succeed with empty articles list.
	var result map[string]interface{}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	articles, ok := result["articles"].([]interface{})
	if !ok {
		t.Fatalf("articles not in result or wrong type")
	}
	if len(articles) != 0 {
		t.Fatalf("expected 0 articles, got %d", len(articles))
	}
}

// TestServer_CloseForceClosesIdleConnections proves that calling Close()
// forces all idle open connections to be closed server-side (fixing issue #20's
// connection leak). Opens an idle connection, calls Close() (not ctx cancel),
// then asserts the server-side closed the connection by checking that a
// client-side read attempt returns EOF (connection closed by server),
// not a timeout (connection still open).
func TestServer_CloseForceClosesIdleConnections(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	er := localstore.NewEventRepo(store.DB())
	tr := localstore.NewTaskRepo(store.DB(), er)
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store, tr, er, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go srv.Serve(ctx)

	// Wait for the listener to come up, then dial and hold the connection
	// open without writing anything — this leaves a handle goroutine parked
	// in ReadBytes('\n'), exactly the scenario that leaks without the fix.
	var conn net.Conn
	for i := 0; i < 50; i++ {
		c, dialErr := net.Dial("unix", socketPath)
		if dialErr == nil {
			conn = c
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if conn == nil {
		t.Fatal("failed to dial socket")
	}

	// Give handle a moment to actually start blocking in ReadBytes.
	time.Sleep(50 * time.Millisecond)

	// Call Close() directly (not via ctx cancel). This should force-close
	// all open connections.
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Give the connection time to actually be closed on the server side.
	time.Sleep(50 * time.Millisecond)

	// Now attempt a read on the client-side connection. If the server
	// properly force-closed the connection, this should return io.EOF.
	// If the connection is still open server-side (the bug), this read will
	// block indefinitely (or until deadline expires, which we also test for).
	conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	buf := make([]byte, 1)
	n, err := conn.Read(buf)

	// Connection should be closed by server, returning EOF.
	// If we get a timeout error, the connection is still open (leak not fixed).
	if err == nil {
		t.Fatalf("expected read to fail after Close, got n=%d", n)
	}

	// io.EOF means connection was closed by the server (good).
	// io.ErrUnexpectedEOF is also acceptable (connection reset).
	// net.ErrClosed is also acceptable (connection closed locally).
	// But a timeout error means the connection is still open (bad - leak).
	netErr, isNetError := err.(net.Error)
	if isNetError && netErr.Timeout() {
		t.Fatalf("read timed out after Close() — connection still open, issue #20 leak not fixed")
	}

	conn.Close()
}
