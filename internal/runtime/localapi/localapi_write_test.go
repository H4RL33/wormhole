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
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
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
	if err := store.CacheWhoAmI(context.Background(), localstore.WhoAmICache{
		AgentID: "agent-1", ProjectID: "ns-1",
		Permissions: []string{"task.create", "task.list", "kb.write", "kb.search", "kb.get", "channel.create", "channel.post", "channel.list", "channel.subscribe"},
		CachedAt:    time.Now().UTC(),
	}); err != nil {
		store.Close()
		t.Fatalf("cache authenticated scope: %v", err)
	}

	er = localstore.NewEventRepo(store.DB())
	tr = localstore.NewTaskRepo(store.DB(), er)
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

// dialAndCall dials srv's socket, performs the initialize ->
// notifications/initialized handshake, sends a single "tools/call" for
// tool with args, and decodes the response.
func dialAndCall(t *testing.T, srv *Server, tool string, args map[string]interface{}) callResponse {
	t.Helper()

	conn := dialLocalSocket(t, srv.socketPath)
	defer conn.Close()
	reader := bufio.NewReader(conn)
	mcpInitialize(t, conn, reader)
	resp := mcpCallTool(t, conn, reader, 2, tool, args)
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

	// GH-19 regression: the task's priority must be threaded through to the
	// queue entry, not hardcoded to 0 — otherwise the sync engine's
	// latency-sensitive bypass (HighPriorityThreshold, checkLatencySensitive)
	// can never trigger from a real request path.
	if pending[0].Priority != 2 {
		t.Fatalf("expected enqueued priority 2 (matching task priority), got %d", pending[0].Priority)
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
		"frontmatter":  map[string]interface{}{"kind": "runbook", "version": float64(2)},
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
	if err != nil || len(pending) != 1 || pending[0].EntityID != articleID || pending[0].Operation != "create" || pending[0].EntityType != "kb" {
		t.Fatalf("expected article enqueued for sync with entity_type=kb, got pending=%+v err=%v", pending, err)
	}
	var queued map[string]interface{}
	if err := json.Unmarshal(pending[0].Payload, &queued); err != nil {
		t.Fatalf("decode queued KB payload: %v", err)
	}
	if _, ok := queued["frontmatter"].(map[string]interface{}); !ok {
		t.Fatalf("queued frontmatter type = %T, want JSON object: %#v", queued["frontmatter"], queued["frontmatter"])
	}
}

func TestLocalKBGetReadsOnlyTheResolvedProject(t *testing.T) {
	srv, _, _, kb, _, cleanup := newTestServerWithQueue(t)
	defer cleanup()

	article, err := kb.WriteArticle(context.Background(), "ns-1", "agent-1", "local runbook", "read from sqlite", json.RawMessage(`{"kind":"runbook"}`))
	if err != nil {
		t.Fatalf("WriteArticle: %v", err)
	}
	if _, err := kb.WriteArticle(context.Background(), "other-project", "agent-2", "other", "must stay isolated", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("WriteArticle(other project): %v", err)
	}

	resp := dialAndCall(t, srv, "wormhole.kb.get", map[string]interface{}{"article_id": article.ID})
	if resp.Error != nil {
		t.Fatalf("kb.get existing article: %v", resp.Error)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("decode kb.get result: %v", err)
	}
	if got["id"] != article.ID || got["title"] != "local runbook" || got["body"] != "read from sqlite" {
		t.Fatalf("kb.get result = %#v", got)
	}

	resp = dialAndCall(t, srv, "wormhole.kb.get", map[string]interface{}{"article_id": "missing"})
	if resp.Error == nil || !strings.Contains(resp.Error.Error(), "article not found") {
		t.Fatalf("kb.get missing error = %v, want article not found", resp.Error)
	}

	if _, err := srv.localGetArticle(context.Background(), json.RawMessage(`{"article_id":`)); err == nil || !strings.Contains(err.Error(), "invalid args") {
		t.Fatalf("localGetArticle malformed args error = %v, want invalid args", err)
	}
}

func TestLocalReadHandlersReturnDurableReplicaState(t *testing.T) {
	srv, tasks, events, kb, _, cleanup := newTestServerWithQueue(t)
	defer cleanup()
	ctx := context.Background()

	task, err := tasks.CreateTask(ctx, "ns-1", "local task", "durable", nil, 3, nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	channelID, err := events.CreateChannel(ctx, "ns-1", "general")
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if _, err := events.PublishEvent(ctx, "ns-1", channelID, "agent-1", "discovery.logged", json.RawMessage(`{"found":"test coverage"}`), nil); err != nil {
		t.Fatalf("PublishEvent: %v", err)
	}
	article, err := kb.WriteArticle(ctx, "ns-1", "agent-1", "local article", "durable replica", json.RawMessage(`{"kind":"note"}`))
	if err != nil {
		t.Fatalf("WriteArticle: %v", err)
	}

	for _, tt := range []struct {
		name string
		call func() (map[string]interface{}, error)
		key  string
		want string
	}{
		{"list tasks", func() (map[string]interface{}, error) {
			return srv.localListTasks(ctx, json.RawMessage(`{"status":"todo"}`))
		}, "tasks", task.ID},
		{"get task", func() (map[string]interface{}, error) {
			return srv.localGetTask(ctx, json.RawMessage(`{"task_id":"`+task.ID+`"}`))
		}, "id", task.ID},
		{"list channels", func() (map[string]interface{}, error) { return srv.localListChannels(ctx, nil) }, "channels", channelID},
		{"list channel events", func() (map[string]interface{}, error) { return srv.localListChannelEvents(ctx, nil) }, "events", channelID},
		{"list articles", func() (map[string]interface{}, error) { return srv.localListArticles(ctx, nil) }, "articles", article.ID},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out, err := tt.call()
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			if tt.key == "id" {
				if out[tt.key] != tt.want {
					t.Fatalf("handler result = %#v, want id %q", out, tt.want)
				}
				return
			}
			items, ok := out[tt.key].([]interface{})
			if !ok || len(items) != 1 {
				t.Fatalf("handler result %q = %#v, want one item", tt.key, out[tt.key])
			}
			item, ok := items[0].(map[string]interface{})
			if !ok || (item["id"] != tt.want && item["channel_id"] != tt.want) {
				t.Fatalf("handler item = %#v, want %q", items[0], tt.want)
			}
		})
	}

	for _, tt := range []struct {
		name string
		call func() error
	}{
		{"get task missing id", func() error { _, err := srv.localGetTask(ctx, nil); return err }},
		{"get task malformed args", func() error { _, err := srv.localGetTask(ctx, json.RawMessage(`{`)); return err }},
		{"list tasks malformed args", func() error { _, err := srv.localListTasks(ctx, json.RawMessage(`{`)); return err }},
		{"list channels malformed args", func() error { _, err := srv.localListChannels(ctx, json.RawMessage(`{`)); return err }},
		{"list events malformed args", func() error { _, err := srv.localListChannelEvents(ctx, json.RawMessage(`{`)); return err }},
		{"list articles malformed args", func() error { _, err := srv.localListArticles(ctx, json.RawMessage(`{`)); return err }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); err == nil || !strings.Contains(err.Error(), "invalid args") && !strings.Contains(err.Error(), "missing task_id") {
				t.Fatalf("handler error = %v, want validated argument error", err)
			}
		})
	}
}

func TestLocalWriteHandlersRejectInvalidInputBeforePersistence(t *testing.T) {
	srv, _, _, _, queue, cleanup := newTestServerWithQueue(t)
	defer cleanup()
	ctx := context.Background()

	for _, tt := range []struct {
		name string
		call func(json.RawMessage) error
		args json.RawMessage
		want string
	}{
		{"task malformed", func(args json.RawMessage) error { _, err := srv.handleTaskCreate(ctx, args); return err }, json.RawMessage(`{`), "invalid args"},
		{"task missing title", func(args json.RawMessage) error { _, err := srv.handleTaskCreate(ctx, args); return err }, json.RawMessage(`{}`), "missing title"},
		{"kb malformed", func(args json.RawMessage) error { _, err := srv.handleKBWrite(ctx, args); return err }, json.RawMessage(`{`), "invalid args"},
		{"kb missing title", func(args json.RawMessage) error { _, err := srv.handleKBWrite(ctx, args); return err }, json.RawMessage(`{}`), "missing title"},
		{"event malformed", func(args json.RawMessage) error { _, err := srv.handleChannelPost(ctx, args); return err }, json.RawMessage(`{`), "invalid args"},
		{"event missing fields", func(args json.RawMessage) error { _, err := srv.handleChannelPost(ctx, args); return err }, json.RawMessage(`{}`), "missing channel_id"},
		{"channel malformed", func(args json.RawMessage) error { _, err := srv.handleChannelCreate(ctx, args); return err }, json.RawMessage(`{`), "invalid args"},
		{"channel missing name", func(args json.RawMessage) error { _, err := srv.handleChannelCreate(ctx, args); return err }, json.RawMessage(`{}`), "missing name"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(tt.args); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("handler error = %v, want %q", err, tt.want)
			}
		})
	}

	withoutQueue := &Server{}
	for _, tt := range []struct {
		name string
		call func() error
	}{
		{"task", func() error {
			_, err := withoutQueue.handleTaskCreate(ctx, json.RawMessage(`{"title":"no queue"}`))
			return err
		}},
		{"kb", func() error {
			_, err := withoutQueue.handleKBWrite(ctx, json.RawMessage(`{"title":"no queue"}`))
			return err
		}},
		{"event", func() error {
			_, err := withoutQueue.handleChannelPost(ctx, json.RawMessage(`{"channel_id":"x","event_type":"test"}`))
			return err
		}},
		{"channel", func() error {
			_, err := withoutQueue.handleChannelCreate(ctx, json.RawMessage(`{"name":"no queue"}`))
			return err
		}},
	} {
		t.Run(tt.name+" missing queue", func(t *testing.T) {
			if err := tt.call(); err == nil || !strings.Contains(err.Error(), "sync queue not available") {
				t.Fatalf("handler error = %v, want sync queue unavailable", err)
			}
		})
	}

	pending, err := queue.ListPending(ctx, "ns-1", 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("invalid requests reached the queue: pending=%+v err=%v", pending, err)
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
	var queued map[string]interface{}
	if err := json.Unmarshal(pending[0].Payload, &queued); err != nil {
		t.Fatalf("decode queued event payload: %v", err)
	}
	if _, ok := queued["payload"].(map[string]interface{}); !ok {
		t.Fatalf("queued event payload type = %T, want JSON object: %#v", queued["payload"], queued["payload"])
	}
}

func TestLocalDurableWrites_RequireSameProjectActionPermission(t *testing.T) {
	srv, _, er, _, qr, cleanup := newTestServerWithQueue(t)
	defer cleanup()
	if _, err := srv.store.DB().Exec(`UPDATE whoami_cache SET permissions = '[]' WHERE project_id = 'ns-1'`); err != nil {
		t.Fatalf("restrict cached permissions: %v", err)
	}
	srv.SetAuthorizationAgent("ns-1", "agent-1")
	if err := srv.store.CacheWhoAmI(context.Background(), localstore.WhoAmICache{AgentID: "stale-admin", ProjectID: "ns-1", Permissions: []string{"task.create", "kb.write", "channel.create", "channel.post"}, CachedAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatalf("cache stale higher-privilege identity: %v", err)
	}
	channelID, err := er.CreateChannel(context.Background(), "ns-1", "denied")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	for _, tt := range []struct {
		name, tool string
		args       map[string]interface{}
	}{
		{name: "kb", tool: "wormhole.kb.write", args: map[string]interface{}{"title": "denied", "agent_id": "agent-1"}},
		{name: "channel create", tool: "wormhole.channel.create", args: map[string]interface{}{"name": "denied-new"}},
		{name: "event", tool: "wormhole.channel.post", args: map[string]interface{}{"channel_id": channelID, "agent_id": "agent-1", "event_type": "denied", "payload": map[string]interface{}{"x": true}}},
		{name: "task route", tool: "wormhole.task.route", args: map[string]interface{}{"capability": "code", "title": "denied route"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp := dialAndCall(t, srv, tt.tool, tt.args)
			if resp.Error == nil || !strings.Contains(resp.Error.Error(), "permission denied") {
				t.Fatalf("response error = %v, want permission denied", resp.Error)
			}
		})
	}
	pending, err := qr.ListPending(context.Background(), "ns-1", 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("denied same-project actions reached queue: pending=%+v err=%v", pending, err)
	}
}

func TestLocalDurableWrites_RollBackWhenQueueInsertFails(t *testing.T) {
	for _, tt := range durableWriteCases() {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, er, _, _, cleanup := newTestServerWithQueue(t)
			defer cleanup()
			if _, err := srv.store.DB().Exec(`
				CREATE TRIGGER fail_sync_queue_insert
				BEFORE INSERT ON sync_queue
				BEGIN
					SELECT RAISE(FAIL, 'injected queue failure');
				END`); err != nil {
				t.Fatalf("create queue failure trigger: %v", err)
			}

			resp := dialAndCall(t, srv, tt.tool, tt.args(t, er))
			if resp.Error == nil {
				t.Fatal("write succeeded despite injected queue failure")
			}
			if !strings.Contains(resp.Error.Error(), "injected queue failure") {
				t.Fatalf("write failed before queue injection: %v", resp.Error)
			}

			var count int
			query := "SELECT count(*) FROM " + tt.table + " WHERE " + tt.whereSQL
			if err := srv.store.DB().QueryRow(query, tt.whereArgs...).Scan(&count); err != nil {
				t.Fatalf("count durable rows: %v", err)
			}
			if count != 0 {
				t.Fatalf("failed response left %d silently unsyncable %s row(s)", count, tt.name)
			}
		})
	}
}

func TestLocalDurableWrites_RollBackWhenAbortedBeforeCommit(t *testing.T) {
	tests := durableWriteCases()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, er, _, qr, cleanup := newTestServerWithQueue(t)
			defer cleanup()
			srv.testBeforeLocalWriteCommit = func(*sql.Tx) error { return errors.New("injected pre-commit abort") }

			resp := dialAndCall(t, srv, tt.tool, tt.args(t, er))
			if resp.Error == nil || !strings.Contains(resp.Error.Error(), "injected pre-commit abort") {
				t.Fatalf("response error = %v, want injected pre-commit abort", resp.Error)
			}

			var count int
			if err := srv.store.DB().QueryRow("SELECT count(*) FROM "+tt.table+" WHERE "+tt.whereSQL, tt.whereArgs...).Scan(&count); err != nil {
				t.Fatalf("count durable rows: %v", err)
			}
			if count != 0 {
				t.Fatalf("failed commit left %d %s row(s)", count, tt.name)
			}
			pending, err := qr.ListPending(context.Background(), "ns-1", 10)
			if err != nil || len(pending) != 0 {
				t.Fatalf("failed commit left queue entries: pending=%+v err=%v", pending, err)
			}
		})
	}
}

func TestLocalDurableWrites_SuccessSurvivesRestartWithPendingQueue(t *testing.T) {
	for _, tt := range durableWriteCases() {
		t.Run(tt.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "wormholed.db")
			srv, er, cleanup := newTestServerAtPath(t, dbPath)
			resp := dialAndCall(t, srv, tt.tool, tt.args(t, er))
			if resp.Error != nil {
				t.Fatalf("write: %v", resp.Error)
			}
			var out map[string]interface{}
			if err := json.Unmarshal(resp.Result, &out); err != nil {
				t.Fatalf("decode result: %v", err)
			}
			entityID, _ := out["id"].(string)
			if entityID == "" {
				t.Fatal("write returned empty entity id")
			}
			cleanup()

			store, err := localstore.Open(dbPath)
			if err != nil {
				t.Fatalf("reopen localstore: %v", err)
			}
			defer store.Close()
			var count int
			if err := store.DB().QueryRow("SELECT count(*) FROM "+tt.table+" WHERE id = ? AND namespace_id = ?", entityID, "ns-1").Scan(&count); err != nil {
				t.Fatalf("read durable row after restart: %v", err)
			}
			if count != 1 {
				t.Fatalf("durable row count after restart = %d, want 1", count)
			}
			pending, err := sync.NewQueueRepo(store.DB()).ListPending(context.Background(), "ns-1", 10)
			if err != nil {
				t.Fatalf("list queue after restart: %v", err)
			}
			found := false
			for _, item := range pending {
				if item.EntityID == entityID && item.EntityType == tt.entityType {
					found = true
				}
			}
			if !found {
				t.Fatalf("pending queue after restart missing %s %s: %+v", tt.entityType, entityID, pending)
			}
		})
	}
}

type durableWriteCase struct {
	name       string
	tool       string
	table      string
	entityType string
	args       func(t *testing.T, er *localstore.EventRepo) map[string]interface{}
	whereSQL   string
	whereArgs  []interface{}
}

func durableWriteCases() []durableWriteCase {
	return []durableWriteCase{
		{name: "task", tool: "wormhole.task.create", table: "tasks", entityType: "task", args: func(_ *testing.T, _ *localstore.EventRepo) map[string]interface{} {
			return map[string]interface{}{"title": "commit-failure-task"}
		}, whereSQL: "title = ?", whereArgs: []interface{}{"commit-failure-task"}},
		{name: "kb", tool: "wormhole.kb.write", table: "kb_articles", entityType: "kb", args: func(_ *testing.T, _ *localstore.EventRepo) map[string]interface{} {
			return map[string]interface{}{"agent_id": "agent-1", "title": "commit-failure-kb", "body": "body"}
		}, whereSQL: "title = ?", whereArgs: []interface{}{"commit-failure-kb"}},
		{name: "channel", tool: "wormhole.channel.create", table: "channels", entityType: "channel", args: func(_ *testing.T, _ *localstore.EventRepo) map[string]interface{} {
			return map[string]interface{}{"name": "commit-failure-channel"}
		}, whereSQL: "name = ?", whereArgs: []interface{}{"commit-failure-channel"}},
		{name: "event", tool: "wormhole.channel.post", table: "events", entityType: "event", args: func(t *testing.T, er *localstore.EventRepo) map[string]interface{} {
			channelID, err := er.CreateChannel(context.Background(), "ns-1", "commit-failure-event-channel")
			if err != nil {
				t.Fatalf("CreateChannel: %v", err)
			}
			return map[string]interface{}{"channel_id": channelID, "agent_id": "agent-1", "event_type": "message.posted"}
		}, whereSQL: "event_type = ?", whereArgs: []interface{}{"message.posted"}},
	}
}

func newTestServerAtPath(t *testing.T, dbPath string) (*Server, *localstore.EventRepo, func()) {
	t.Helper()
	store, err := localstore.Open(dbPath)
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	if err := store.CacheWhoAmI(context.Background(), localstore.WhoAmICache{AgentID: "agent-1", ProjectID: "ns-1", Permissions: []string{"task.create", "kb.write", "channel.create", "channel.post"}, CachedAt: time.Now().UTC()}); err != nil {
		store.Close()
		t.Fatalf("cache authenticated scope: %v", err)
	}
	er := localstore.NewEventRepo(store.DB())
	srv, err := New(filepath.Join(t.TempDir(), "wormholed.sock"), "", "", "ns-1", store,
		localstore.NewTaskRepo(store.DB(), er), er, localstore.NewKBRepo(store.DB()), sync.NewQueueRepo(store.DB()))
	if err != nil {
		store.Close()
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go srv.Serve(ctx)
	return srv, er, func() {
		cancel()
		srv.Close()
		store.Close()
	}
}

// TestLocalWrites_IgnoreClientSuppliedNamespaceID proves the cross-namespace
// write vulnerability is closed: a socket bound to "ns-1" (see
// newTestServerWithQueue) must not honor a client-supplied namespace_id of
// "ns-EVIL" — every write must land in the socket's bound namespace ("ns-1")
// regardless of what the request body claims, and nothing must be written to
// or enqueued for the mismatched namespace.
func TestLocalWrites_IgnoreClientSuppliedNamespaceID(t *testing.T) {
	srv, tr, er, kb, qr, cleanup := newTestServerWithQueue(t)
	defer cleanup()

	const evilNS = "ns-EVIL"

	t.Run("task.create", func(t *testing.T) {
		resp := dialAndCall(t, srv, "wormhole.task.create", map[string]interface{}{
			"namespace_id": evilNS,
			"title":        "cross-namespace task",
			"description":  "should land in ns-1, not ns-EVIL",
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
		if ns, _ := out["namespace_id"].(string); ns != "ns-1" {
			t.Fatalf("expected task written to bound namespace ns-1, response reports namespace_id=%q", ns)
		}

		if got, err := tr.GetTask(context.Background(), "ns-1", taskID); err != nil || got.Title != "cross-namespace task" {
			t.Fatalf("task not persisted in bound namespace ns-1: got=%+v err=%v", got, err)
		}
		if _, err := tr.GetTask(context.Background(), evilNS, taskID); err == nil {
			t.Fatalf("task leaked into client-supplied namespace %q", evilNS)
		}

		pendingEvil, err := qr.ListPending(context.Background(), evilNS, 10)
		if err != nil {
			t.Fatalf("ListPending(evilNS): %v", err)
		}
		if len(pendingEvil) != 0 {
			t.Fatalf("expected nothing enqueued under client-supplied namespace %q, got %+v", evilNS, pendingEvil)
		}
	})

	t.Run("kb.write", func(t *testing.T) {
		resp := dialAndCall(t, srv, "wormhole.kb.write", map[string]interface{}{
			"namespace_id": evilNS,
			"agent_id":     "agent-1",
			"title":        "cross-namespace article",
			"body":         "should land in ns-1, not ns-EVIL",
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
		if ns, _ := out["namespace_id"].(string); ns != "ns-1" {
			t.Fatalf("expected article written to bound namespace ns-1, response reports namespace_id=%q", ns)
		}

		if got, err := kb.GetArticle(context.Background(), "ns-1", articleID); err != nil || got.Title != "cross-namespace article" {
			t.Fatalf("article not persisted in bound namespace ns-1: got=%+v err=%v", got, err)
		}
		if _, err := kb.GetArticle(context.Background(), evilNS, articleID); err == nil {
			t.Fatalf("article leaked into client-supplied namespace %q", evilNS)
		}

		pendingEvil, err := qr.ListPending(context.Background(), evilNS, 10)
		if err != nil {
			t.Fatalf("ListPending(evilNS): %v", err)
		}
		if len(pendingEvil) != 0 {
			t.Fatalf("expected nothing enqueued under client-supplied namespace %q, got %+v", evilNS, pendingEvil)
		}
	})

	t.Run("channel.post", func(t *testing.T) {
		channelID, err := er.CreateChannel(context.Background(), "ns-1", "general2")
		if err != nil {
			t.Fatalf("create channel: %v", err)
		}

		resp := dialAndCall(t, srv, "wormhole.channel.post", map[string]interface{}{
			"namespace_id": evilNS,
			"channel_id":   channelID,
			"agent_id":     "agent-1",
			"event_type":   "discovery.logged",
			"payload":      map[string]interface{}{"found": "cross-namespace attempt"},
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
		if ns, _ := out["namespace_id"].(string); ns != "ns-1" {
			t.Fatalf("expected event written to bound namespace ns-1, response reports namespace_id=%q", ns)
		}

		pendingBound, err := qr.ListPending(context.Background(), "ns-1", 10)
		if err != nil {
			t.Fatalf("ListPending(ns-1): %v", err)
		}
		found := false
		for _, p := range pendingBound {
			if p.EntityID == eventID {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected event enqueued under bound namespace ns-1, got %+v", pendingBound)
		}

		pendingEvil, err := qr.ListPending(context.Background(), evilNS, 10)
		if err != nil {
			t.Fatalf("ListPending(evilNS): %v", err)
		}
		if len(pendingEvil) != 0 {
			t.Fatalf("expected nothing enqueued under client-supplied namespace %q, got %+v", evilNS, pendingEvil)
		}
	})
}
