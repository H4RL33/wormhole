// internal/runtime/localapi/localapi_test.go
package localapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
)

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
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store, localstore.NewTaskRepo(store.DB()), localstore.NewEventRepo(store.DB()), localstore.NewKBRepo(store.DB()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	// Give Serve a moment to bind the socket.
	var conn net.Conn
	for i := 0; i < 50; i++ {
		conn, err = net.Dial("unix", socketPath)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial socket: %v", err)
	}
	defer conn.Close()

	reqRaw, _ := json.Marshal(localRequest{Tool: "wormhole.agent.whoami"})
	if _, err := conn.Write(append(reqRaw, '\n')); err != nil {
		t.Fatalf("write request: %v", err)
	}

	var resp localResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
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
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store, localstore.NewTaskRepo(store.DB()), localstore.NewEventRepo(store.DB()), localstore.NewKBRepo(store.DB()))
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

func TestServer_UnknownTool(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store, localstore.NewTaskRepo(store.DB()), localstore.NewEventRepo(store.DB()), localstore.NewKBRepo(store.DB()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	var conn net.Conn
	for i := 0; i < 50; i++ {
		conn, err = net.Dial("unix", socketPath)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial socket: %v", err)
	}
	defer conn.Close()

	reqRaw, _ := json.Marshal(localRequest{Tool: "wormhole.task.create"})
	conn.Write(append(reqRaw, '\n'))

	var resp localResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == "" {
		t.Fatalf("want error for unsupported tool, got none")
	}
}
