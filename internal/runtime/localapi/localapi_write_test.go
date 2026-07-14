// internal/runtime/localapi/localapi_write_test.go
//
// Tests for the local write tools (wormhole.task.create, wormhole.kb.write,
// wormhole.channel.post) added to close the "local write path" functional-alpha
// gap: agents connected to wormholed must be able to create tasks, write KB
// articles, and post channel events locally, with each write enqueued to the
// outbound sync queue (RFC-0003 §8.2) for later delivery to the Coordination
// Server.
package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/runtime/sync"
)

// newTestServerWithQueue wires a fresh localstore + sync queue repo into a
// Server (single-org mode, no coordination server needed for these local-only
// write paths) and starts it serving on a temp socket. Returns the repos the
// tests need to assert against directly, plus a cleanup func.
func newTestServerWithQueue(t *testing.T) (srv *Server, tr *localstore.TaskRepo, er *localstore.EventRepo, kb *localstore.KBRepo, qr *sync.QueueRepo, cleanup func()) {
	t.Helper()

	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}

	tr = localstore.NewTaskRepo(store.DB())
	er = localstore.NewEventRepo(store.DB())
	kb = localstore.NewKBRepo(store.DB())
	qr = sync.NewQueueRepo(store.DB())

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	srv, err = New(socketPath, "", "", "ns-1", store, tr, er, kb, qr)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go srv.Serve(ctx)

	cleanup = func() {
		cancel()
		srv.Close()
		store.Close()
	}

	return srv, tr, er, kb, qr, cleanup
}

// callResponse is the decoded result of dialAndCall: either a non-nil Result
// or a non-nil Error, mirroring localResponse but with Error as an error
// value so callers can do `if resp.Error != nil`.
type callResponse struct {
	Result json.RawMessage
	Error  error
}

// dialAndCall dials srv's socket, sends a single localRequest for tool with
// args marshaled as the request's Args, and decodes the response.
func dialAndCall(t *testing.T, srv *Server, tool string, args map[string]interface{}) callResponse {
	t.Helper()

	var conn net.Conn
	var err error
	for i := 0; i < 50; i++ {
		conn, err = net.Dial("unix", srv.socketPath)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial socket: %v", err)
	}
	defer conn.Close()

	argsRaw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	reqRaw, err := json.Marshal(localRequest{Tool: tool, Args: argsRaw})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if _, err := conn.Write(append(reqRaw, '\n')); err != nil {
		t.Fatalf("write request: %v", err)
	}

	var resp localResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != "" {
		return callResponse{Error: errors.New(resp.Error)}
	}
	return callResponse{Result: resp.Result}
}

func TestLocalTaskCreate_EnqueuesForSync(t *testing.T) {
	srv, tr, _, _, qr, cleanup := newTestServerWithQueue(t)
	defer cleanup()

	resp := dialAndCall(t, srv, "wormhole.task.create", map[string]interface{}{
		"namespace_id": "ns-1",
		"title":        "write the alpha",
		"description":  "close the gaps",
		"priority":     2,
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	var out map[string]interface{}
	json.Unmarshal(resp.Result, &out)
	taskID, _ := out["id"].(string)
	if taskID == "" {
		t.Fatal("expected non-empty task id in response")
	}

	// verify localstore actually has it
	got, err := tr.GetTask(context.Background(), "ns-1", taskID)
	if err != nil || got.Title != "write the alpha" {
		t.Fatalf("task not persisted: got=%+v err=%v", got, err)
	}

	// verify it was enqueued for sync
	pending, err := qr.ListPending(context.Background(), "ns-1", 10)
	if err != nil || len(pending) != 1 || pending[0].EntityID != taskID || pending[0].Operation != "create" {
		t.Fatalf("expected task enqueued for sync, got pending=%+v err=%v", pending, err)
	}
}

func TestLocalKBWrite_EnqueuesForSync(t *testing.T) {
	srv, _, _, kb, qr, cleanup := newTestServerWithQueue(t)
	defer cleanup()

	resp := dialAndCall(t, srv, "wormhole.kb.write", map[string]interface{}{
		"namespace_id": "ns-1",
		"agent_id":     "agent-1",
		"title":        "how to close alpha gaps",
		"body":         "enqueue every local write",
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	var out map[string]interface{}
	json.Unmarshal(resp.Result, &out)
	articleID, _ := out["id"].(string)
	if articleID == "" {
		t.Fatal("expected non-empty article id in response")
	}

	got, err := kb.GetArticle(context.Background(), "ns-1", articleID)
	if err != nil || got.Title != "how to close alpha gaps" {
		t.Fatalf("article not persisted: got=%+v err=%v", got, err)
	}

	pending, err := qr.ListPending(context.Background(), "ns-1", 10)
	if err != nil || len(pending) != 1 || pending[0].EntityID != articleID || pending[0].Operation != "create" {
		t.Fatalf("expected article enqueued for sync, got pending=%+v err=%v", pending, err)
	}
}

func TestLocalChannelPost_EnqueuesForSync(t *testing.T) {
	srv, _, er, _, qr, cleanup := newTestServerWithQueue(t)
	defer cleanup()

	channelID, err := er.CreateChannel(context.Background(), "ns-1", "general")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	resp := dialAndCall(t, srv, "wormhole.channel.post", map[string]interface{}{
		"namespace_id": "ns-1",
		"channel_id":   channelID,
		"agent_id":     "agent-1",
		"event_type":   "discovery.logged",
		"payload":      map[string]interface{}{"found": "a bug"},
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	var out map[string]interface{}
	json.Unmarshal(resp.Result, &out)
	eventID, _ := out["id"].(string)
	if eventID == "" {
		t.Fatal("expected non-empty event id in response")
	}

	pending, err := qr.ListPending(context.Background(), "ns-1", 10)
	if err != nil || len(pending) != 1 || pending[0].EntityID != eventID || pending[0].Operation != "create" {
		t.Fatalf("expected event enqueued for sync, got pending=%+v err=%v", pending, err)
	}
}
