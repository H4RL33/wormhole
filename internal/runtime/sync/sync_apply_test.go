// sync_apply_test.go exercises the local-apply path: Bootstrap and
// PullIncremental must not just fetch the server's task/KB payload, they
// must write it into localstore.TaskRepo/KBRepo so a fresh Gateway
// daemon's SQLite replica actually ends up populated (RFC-0003 §8).
package sync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
)

// newApplyTestRepos opens a real localstore-schema SQLite file (tasks,
// kb_articles, sync_queue, sync_audit all present) so TaskRepo/KBRepo
// upserts exercise the real schema, not a hand-rolled subset.
func newApplyTestRepos(t *testing.T) (*localstore.Store, *QueueRepo, *AuditRepo, *localstore.TaskRepo, *localstore.KBRepo) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "wormhole.db")
	store, err := localstore.Open(dbPath)
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	db := store.DB()
	er := localstore.NewEventRepo(db)
	return store, NewQueueRepo(db), NewAuditRepo(db), localstore.NewTaskRepo(db, er), localstore.NewKBRepo(db)
}

// fakeBootstrapServer serves wormhole.sync.bootstrap / incremental_pull
// with one task and one KB article, mirroring internal/mcp/sync.go's
// BootstrapOutput/IncrementalPullOutput wire shape.
func fakeBootstrapServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		task := taskSummaryWire{
			TaskID:      "task-1",
			Title:       "Server task",
			Description: "from server",
			Status:      "todo",
			Priority:    2,
			CreatedAt:   time.Date(2026, 7, 26, 1, 2, 3, 123456000, time.UTC),
			UpdatedAt:   time.Date(2026, 7, 26, 2, 3, 4, 654321000, time.UTC),
		}
		article := articleSummaryWire{
			ArticleID:     "kb-1",
			ProjectID:     "ns-1",
			Title:         "Server article",
			Body:          "server body",
			Frontmatter:   json.RawMessage(`{}`),
			AuthorAgentID: "agent-1",
			CreatedAt:     time.Now().UTC(),
			UpdatedAt:     time.Now().UTC(),
		}

		var resultData interface{}
		switch params.Name {
		case "wormhole.sync.bootstrap":
			bootstrap := validBootstrapWire()
			bootstrap.OrgConfig.Tasks = bootstrap.OrgConfig.Tasks[:1]
			bootstrap.OrgConfig.Tasks[0].ID = "task-1"
			bootstrap.OrgConfig.Tasks[0].Title = "Server task"
			bootstrap.OrgConfig.Tasks[0].Description = "from server"
			bootstrap.OrgConfig.Tasks[0].Priority = 2
			bootstrap.TaskList = append([]types.BootstrapTaskV1(nil), bootstrap.OrgConfig.Tasks...)
			bootstrap.OrgConfig.KB.Articles[0].Title = "Server article"
			bootstrap.OrgConfig.KB.Articles[0].Body = "server body"
			bootstrap.KBList = append([]types.BootstrapArticleV1(nil), bootstrap.OrgConfig.KB.Articles...)
			resultData = bootstrap
		case "wormhole.sync.incremental_pull":
			taskData, _ := json.Marshal(task)
			articleData, _ := json.Marshal(article)
			resultData = map[string]interface{}{
				"updates": []syncUpdateEnvelopeWire{
					{Type: "task", Data: taskData},
					{Type: "kb", Data: articleData},
				},
				"timestamp": time.Now().UTC().Format(time.RFC3339),
				"version":   1,
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}

		resultRaw, _ := json.Marshal(resultData)
		toolResult := map[string]interface{}{
			"content": []map[string]string{{"type": "text", "text": string(resultRaw)}},
		}
		toolResultRaw, _ := json.Marshal(toolResult)
		resp := map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": json.RawMessage(toolResultRaw)}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
}

// TestBootstrap_AppliesServerTasksAndKBToLocalStore proves a fresh
// localstore ends up containing the server's tasks/KB articles after
// Bootstrap runs — not just that the HTTP round-trip succeeds.
func TestBootstrap_AppliesServerTasksAndKBToLocalStore(t *testing.T) {
	srv := fakeBootstrapServer(t)
	defer srv.Close()

	store, qRepo, aRepo, taskRepo, kbRepo := newApplyTestRepos(t)
	engine := mustNewEngine(t, srv.URL, qRepo, aRepo, taskRepo, kbRepo, DefaultConfig())
	if err := engine.ConfigureBootstrap(store, "agent-1", "passport-1", nil); err != nil {
		t.Fatalf("ConfigureBootstrap: %v", err)
	}

	ctx := context.Background()
	if err := engine.Bootstrap(ctx); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	gotTask, err := taskRepo.GetTask(ctx, "ns-1", "task-1")
	if err != nil {
		t.Fatalf("GetTask after Bootstrap: %v", err)
	}
	if gotTask.Title != "Server task" {
		t.Errorf("task title = %q, want %q", gotTask.Title, "Server task")
	}

	gotArticle, err := kbRepo.GetArticle(ctx, "ns-1", "kb-1")
	if err != nil {
		t.Fatalf("GetArticle after Bootstrap: %v", err)
	}
	if gotArticle.Title != "Server article" {
		t.Errorf("article title = %q, want %q", gotArticle.Title, "Server article")
	}
}

// TestPullIncremental_AppliesServerUpdatesToLocalStore proves the same for
// the incremental_pull path, which uses a different response envelope
// (Updates []{type, data}) than Bootstrap's (TaskList/KBList).
func TestPullIncremental_AppliesServerUpdatesToLocalStore(t *testing.T) {
	srv := fakeBootstrapServer(t)
	defer srv.Close()

	_, qRepo, aRepo, taskRepo, kbRepo := newApplyTestRepos(t)
	engine := mustNewEngine(t, srv.URL, qRepo, aRepo, taskRepo, kbRepo, DefaultConfig())

	ctx := context.Background()
	if err := engine.PullIncremental(ctx); err != nil {
		t.Fatalf("PullIncremental: %v", err)
	}

	if _, err := taskRepo.GetTask(ctx, "ns-1", "task-1"); err != nil {
		t.Fatalf("GetTask after PullIncremental: %v", err)
	}
	if _, err := kbRepo.GetArticle(ctx, "ns-1", "kb-1"); err != nil {
		t.Fatalf("GetArticle after PullIncremental: %v", err)
	}
}

func TestPullIncrementalAppliesAuthoritativeTaskTimestamps(t *testing.T) {
	store, qRepo, aRepo, taskRepo, kbRepo := newApplyTestRepos(t)
	ctx := context.Background()
	if _, err := taskRepo.UpsertTask(ctx, "ns-1", "task-1", "local", "local", nil, nil, "todo", 1, nil); err != nil {
		t.Fatal(err)
	}
	localCreated := time.Date(2025, 1, 2, 3, 4, 5, 111111000, time.UTC)
	localUpdated := localCreated.Add(time.Hour)
	if _, err := store.DB().ExecContext(ctx, `UPDATE tasks SET created_at = ?, updated_at = ? WHERE id = ?`, localCreated, localUpdated, "task-1"); err != nil {
		t.Fatal(err)
	}
	serverCreated := time.Date(2026, 7, 26, 3, 4, 5, 123456000, time.UTC)
	serverUpdated := time.Date(2026, 7, 26, 4, 5, 6, 654321000, time.UTC)
	data, _ := json.Marshal(map[string]interface{}{
		"task_id": "task-1", "parent_task_id": nil, "title": "server", "description": "authoritative",
		"owner_agent_id": nil, "status": "wip", "priority": 2, "due_by": nil,
		"created_at": serverCreated, "updated_at": serverUpdated,
	})
	engine := mustNewEngine(t, "http://unused", qRepo, aRepo, taskRepo, kbRepo, DefaultConfig())
	engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
		return incrementalPullResultWire{Updates: []syncUpdateEnvelopeWire{{Type: "task", Data: data}}, Timestamp: "2026-07-26T04:05:07Z", Version: 1}, nil
	}
	if err := engine.PullIncremental(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := taskRepo.GetTask(ctx, "ns-1", "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "server" || !got.CreatedAt.Equal(serverCreated) || !got.UpdatedAt.Equal(serverUpdated) {
		t.Fatalf("pulled task = %+v, want authoritative timestamps %s/%s", got, serverCreated.Format(time.RFC3339Nano), serverUpdated.Format(time.RFC3339Nano))
	}
}

func TestPullIncrementalDoesNotClobberPendingTaskTimestamps(t *testing.T) {
	store, qRepo, aRepo, taskRepo, kbRepo := newApplyTestRepos(t)
	ctx := context.Background()
	if _, err := taskRepo.UpsertTask(ctx, "ns-1", "task-1", "pending local", "local", nil, nil, "todo", 1, nil); err != nil {
		t.Fatal(err)
	}
	localCreated := time.Date(2026, 7, 26, 1, 2, 3, 111111000, time.UTC)
	localUpdated := time.Date(2026, 7, 26, 2, 3, 4, 222222000, time.UTC)
	if _, err := store.DB().ExecContext(ctx, `UPDATE tasks SET created_at = ?, updated_at = ? WHERE id = ?`, localCreated, localUpdated, "task-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := qRepo.Enqueue(ctx, "ns-1", "task", "task-1", "create", json.RawMessage(`{"title":"pending local"}`), 1); err != nil {
		t.Fatal(err)
	}
	engine := mustNewEngine(t, "http://unused", qRepo, aRepo, taskRepo, kbRepo, DefaultConfig())
	engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
		t.Fatal("Fabric contacted while task write pending")
		return nil, nil
	}
	if err := engine.PullIncremental(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := taskRepo.GetTask(ctx, "ns-1", "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "pending local" || !got.CreatedAt.Equal(localCreated) || !got.UpdatedAt.Equal(localUpdated) {
		t.Fatalf("pending task was clobbered: %+v", got)
	}
}

func TestPullIncrementalUsesLastSuccessfulCursor(t *testing.T) {
	qRepo, aRepo := setupTestRepos(t)
	defer qRepo.db.Close()
	engine := mustNewEngine(t, "http://localhost:8080", qRepo, aRepo, nil, nil, DefaultConfig())

	const firstTimestamp = "2026-07-22T10:00:00Z"
	const secondTimestamp = "2026-07-22T10:00:05Z"
	call := 0
	engine.testCallSyncToolWithResultFn = func(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
		call++
		switch call {
		case 1:
			if got, ok := args["last_sync"]; ok {
				t.Fatalf("first cursor = %#v, want omitted", got)
			}
			return incrementalPullResult(firstTimestamp, nil), nil
		case 2:
			if got, ok := args["last_sync"]; !ok || got != firstTimestamp {
				t.Fatalf("second cursor = %#v, want %q", got, firstTimestamp)
			}
			return incrementalPullResult(secondTimestamp, nil), nil
		default:
			return nil, errors.New("unexpected pull")
		}
	}

	if err := engine.PullIncremental(context.Background()); err != nil {
		t.Fatalf("first PullIncremental: %v", err)
	}
	if err := engine.PullIncremental(context.Background()); err != nil {
		t.Fatalf("second PullIncremental: %v", err)
	}
}

func TestPullIncrementalResendsRawCursorByteForByte(t *testing.T) {
	qRepo, aRepo := setupTestRepos(t)
	defer qRepo.db.Close()
	engine := mustNewEngine(t, "http://localhost:8080", qRepo, aRepo, nil, nil, DefaultConfig())

	const rawTimestamp = "2026-07-22T10:00:00.123456789+05:30"
	call := 0
	engine.testCallSyncToolWithResultFn = func(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
		call++
		if call == 1 {
			return incrementalPullResult(rawTimestamp, nil), nil
		}
		if got := args["last_sync"]; got != rawTimestamp {
			t.Fatalf("last_sync = %#v, want raw server timestamp %q", got, rawTimestamp)
		}
		return incrementalPullResult("2026-07-22T10:00:05Z", nil), nil
	}

	if err := engine.PullIncremental(context.Background()); err != nil {
		t.Fatalf("first PullIncremental: %v", err)
	}
	if err := engine.PullIncremental(context.Background()); err != nil {
		t.Fatalf("second PullIncremental: %v", err)
	}
}

func TestPullIncrementalFailureDoesNotAdvanceCursor(t *testing.T) {
	const firstTimestamp = "2026-07-22T10:00:00Z"
	const failedTimestamp = "2026-07-22T10:00:05Z"

	taskData, err := json.Marshal(taskSummaryWire{
		TaskID: "task-1", Title: "server task", Status: "todo",
	})
	if err != nil {
		t.Fatalf("marshal task: %v", err)
	}

	tests := []struct {
		name       string
		failure    interface{}
		failureErr error
	}{
		{name: "server", failureErr: errors.New("server unavailable")},
		{name: "decode", failure: map[string]interface{}{"updates": "not-an-array", "timestamp": failedTimestamp}},
		{name: "apply", failure: incrementalPullResult(failedTimestamp, []syncUpdateEnvelopeWire{{Type: "task", Data: taskData}})},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qRepo, aRepo := setupTestRepos(t)
			defer qRepo.db.Close()
			engine := mustNewEngine(t, "http://localhost:8080", qRepo, aRepo, nil, nil, DefaultConfig())

			call := 0
			engine.testCallSyncToolWithResultFn = func(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
				call++
				switch call {
				case 1:
					return incrementalPullResult(firstTimestamp, nil), nil
				case 2:
					return tt.failure, tt.failureErr
				case 3:
					if got, ok := args["last_sync"]; !ok || got != firstTimestamp {
						t.Fatalf("cursor after failed pull = %#v, want %q", got, firstTimestamp)
					}
					return incrementalPullResult(failedTimestamp, nil), nil
				default:
					return nil, errors.New("unexpected pull")
				}
			}

			if err := engine.PullIncremental(context.Background()); err != nil {
				t.Fatalf("initial PullIncremental: %v", err)
			}
			if err := engine.PullIncremental(context.Background()); err == nil {
				t.Fatal("failed PullIncremental returned nil error")
			}
			if err := engine.PullIncremental(context.Background()); err != nil {
				t.Fatalf("retry PullIncremental: %v", err)
			}
		})
	}
}

func incrementalPullResult(timestamp string, updates []syncUpdateEnvelopeWire) map[string]interface{} {
	if updates == nil {
		updates = []syncUpdateEnvelopeWire{}
	}
	return map[string]interface{}{
		"updates":   updates,
		"timestamp": timestamp,
		"version":   1,
	}
}
