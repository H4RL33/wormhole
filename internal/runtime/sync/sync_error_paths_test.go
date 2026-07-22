package sync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPushBatchEmptyReturnsWithoutCallingServer(t *testing.T) {
	qRepo, aRepo := setupTestRepos(t)
	defer qRepo.db.Close()

	engine := mustNewEngine(t, "http://unused.invalid", qRepo, aRepo, nil, nil, DefaultConfig())
	engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
		t.Fatal("empty queue called coordination server")
		return nil, nil
	}
	if err := engine.pushBatch(context.Background()); err != nil {
		t.Fatalf("pushBatch: %v", err)
	}
}

func TestPushBatchErrorPathsLeaveEntryPending(t *testing.T) {
	tests := []struct {
		name       string
		payload    json.RawMessage
		toolResult interface{}
		toolErr    error
		want       string
	}{
		{name: "coordination error", payload: json.RawMessage(`{}`), toolErr: errors.New("offline"), want: "call server"},
		{name: "undecodable acknowledgement", payload: json.RawMessage(`{}`), toolResult: map[string]interface{}{"items_received": "one"}, want: "decode result"},
		{name: "non-JSON payload is transmitted as text", payload: json.RawMessage(`not-json`), toolResult: pushResult(1, []map[string]interface{}{{"id": "task-1", "type": "task"}})},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qRepo, aRepo := setupTestRepos(t)
			defer qRepo.db.Close()
			if _, err := qRepo.Enqueue(context.Background(), "ns-1", "task", "task-1", "create", tt.payload, 0); err != nil {
				t.Fatalf("Enqueue: %v", err)
			}

			engine := mustNewEngine(t, "http://unused.invalid", qRepo, aRepo, nil, nil, DefaultConfig())
			engine.testCallSyncToolWithResultFn = func(_ context.Context, _ string, args map[string]interface{}) (interface{}, error) {
				if tt.name == "non-JSON payload is transmitted as text" {
					items := args["items"].([]map[string]interface{})
					if got := items[0]["payload"]; got != "not-json" {
						t.Fatalf("payload = %#v, want raw text", got)
					}
				}
				return tt.toolResult, tt.toolErr
			}

			err := engine.pushBatch(context.Background())
			if tt.want == "" {
				if err != nil {
					t.Fatalf("pushBatch: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("pushBatch error = %v, want containing %q", err, tt.want)
			}
			pending, listErr := qRepo.ListPending(context.Background(), "ns-1", 10)
			if listErr != nil {
				t.Fatalf("ListPending: %v", listErr)
			}
			if len(pending) != 1 {
				t.Fatalf("pending = %d, want 1", len(pending))
			}
		})
	}
}

func TestPushBatchAndLatencySurfaceQueueReadErrors(t *testing.T) {
	qRepo, aRepo := setupTestRepos(t)
	engine := mustNewEngine(t, "http://unused.invalid", qRepo, aRepo, nil, nil, DefaultConfig())
	if err := qRepo.db.Close(); err != nil {
		t.Fatalf("close DB: %v", err)
	}

	if err := engine.pushBatch(context.Background()); err == nil || !strings.Contains(err.Error(), "list pending") {
		t.Fatalf("pushBatch error = %v, want queue read error", err)
	}
	if err := engine.checkLatencySensitive(context.Background()); err == nil || !strings.Contains(err.Error(), "list pending") {
		t.Fatalf("checkLatencySensitive error = %v, want queue read error", err)
	}
}

func TestPullIncrementalRejectsInvalidServerUpdates(t *testing.T) {
	taskJSON, _ := json.Marshal(taskSummaryWire{TaskID: "task-1", Title: "task", Status: "todo"})
	kbJSON, _ := json.Marshal(articleSummaryWire{ArticleID: "kb-1", Title: "article"})
	tests := []struct {
		name   string
		result interface{}
		want   string
	}{
		{name: "invalid timestamp", result: incrementalPullResult("not-a-time", nil), want: "decode timestamp"},
		{name: "invalid task JSON", result: incrementalPullResult("2026-07-22T10:00:00Z", []syncUpdateEnvelopeWire{{Type: "task", Data: json.RawMessage(`"not-an-object"`)}}), want: "decode task update"},
		{name: "invalid KB JSON", result: incrementalPullResult("2026-07-22T10:00:00Z", []syncUpdateEnvelopeWire{{Type: "kb", Data: json.RawMessage(`"not-an-object"`)}}), want: "decode kb update"},
		{name: "missing task repository", result: incrementalPullResult("2026-07-22T10:00:00Z", []syncUpdateEnvelopeWire{{Type: "task", Data: taskJSON}}), want: "no taskRepo"},
		{name: "missing KB repository", result: incrementalPullResult("2026-07-22T10:00:00Z", []syncUpdateEnvelopeWire{{Type: "kb", Data: kbJSON}}), want: "no kbRepo"},
		{name: "unknown entity", result: incrementalPullResult("2026-07-22T10:00:00Z", []syncUpdateEnvelopeWire{{Type: "agent", Data: json.RawMessage(`{}`)}}), want: "unknown update type"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qRepo, aRepo := setupTestRepos(t)
			defer qRepo.db.Close()
			engine := mustNewEngine(t, "http://unused.invalid", qRepo, aRepo, nil, nil, DefaultConfig())
			engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
				return tt.result, nil
			}
			if err := engine.PullIncremental(context.Background()); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("PullIncremental error = %v, want containing %q", err, tt.want)
			}
			if engine.lastSyncCursor != "" {
				t.Fatalf("cursor advanced to %q after rejected update", engine.lastSyncCursor)
			}
		})
	}
}

func TestBootstrapErrorPaths(t *testing.T) {
	task := taskSummaryWire{TaskID: "task-1", Title: "task", Status: "todo"}
	article := articleSummaryWire{ArticleID: "kb-1", Title: "article"}
	tests := []struct {
		name   string
		result interface{}
		err    error
		want   string
	}{
		{name: "coordination error", err: errors.New("offline"), want: "call server"},
		{name: "invalid result", result: map[string]interface{}{"task_list": "not-a-list"}, want: "decode result"},
		{name: "missing task repository", result: bootstrapResultWire{TaskList: []taskSummaryWire{task}}, want: "no taskRepo"},
		{name: "missing KB repository", result: bootstrapResultWire{KBList: []articleSummaryWire{article}}, want: "no kbRepo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qRepo, aRepo := setupTestRepos(t)
			defer qRepo.db.Close()
			engine := mustNewEngine(t, "http://unused.invalid", qRepo, aRepo, nil, nil, DefaultConfig())
			engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
				return tt.result, tt.err
			}
			if err := engine.Bootstrap(context.Background()); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Bootstrap error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestWireDecodersRejectUnsupportedOrMismatchedValues(t *testing.T) {
	tests := []struct {
		name string
		call func(interface{}) error
	}{
		{name: "bootstrap marshal", call: func(v interface{}) error { _, err := decodeBootstrapResult(v); return err }},
		{name: "pull marshal", call: func(v interface{}) error { _, err := decodeIncrementalPullResult(v); return err }},
		{name: "push marshal", call: func(v interface{}) error { _, err := decodeIncrementalPushResult(v); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(make(chan int)); err == nil || !strings.Contains(err.Error(), "marshal") {
				t.Fatalf("decoder error = %v, want marshal error", err)
			}
		})
	}
}

func TestCallSyncToolWithResultHTTPContractAndErrors(t *testing.T) {
	tests := []struct {
		name     string
		response string
		want     string
	}{
		{name: "invalid JSON", response: `{`, want: "decode coordination server response"},
		{name: "RPC error", response: `{"jsonrpc":"2.0","id":1,"error":{"code":-1,"message":"denied"}}`, want: "server error"},
		{name: "missing result", response: `{"jsonrpc":"2.0","id":1}`, want: "no result"},
		{name: "invalid result wrapper", response: `{"jsonrpc":"2.0","id":1,"result":"text"}`, want: "decode tools/call result"},
		{name: "empty content", response: `{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`, want: "empty result"},
		{name: "invalid tool output", response: `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{"}]}}`, want: "decode tool output"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/mcp" {
					t.Errorf("path = %q, want /mcp", r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer token" {
					t.Errorf("Authorization = %q", got)
				}
				_, _ = io.WriteString(w, tt.response)
			}))
			defer srv.Close()

			qRepo, aRepo := setupTestRepos(t)
			defer qRepo.db.Close()
			engine := mustNewEngine(t, srv.URL+"/", qRepo, aRepo, nil, nil, DefaultConfig())
			if _, err := engine.callSyncToolWithResult(context.Background(), "wormhole.sync.bootstrap", map[string]interface{}{}); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("callSyncToolWithResult error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestCallSyncToolWithResultTransportAndReadErrors(t *testing.T) {
	qRepo, aRepo := setupTestRepos(t)
	defer qRepo.db.Close()
	engine := mustNewEngine(t, "http://coordination.invalid", qRepo, aRepo, nil, nil, DefaultConfig())

	transportErr := errors.New("transport failed")
	engine.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportErr
	})
	if _, err := engine.callSyncToolWithResult(context.Background(), "wormhole.sync.bootstrap", nil); !errors.Is(err, transportErr) {
		t.Fatalf("transport error = %v, want %v", err, transportErr)
	}

	readErr := errors.New("read failed")
	engine.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: errorReadCloser{err: readErr}, Header: make(http.Header)}, nil
	})
	if _, err := engine.callSyncToolWithResult(context.Background(), "wormhole.sync.bootstrap", nil); !errors.Is(err, readErr) {
		t.Fatalf("read error = %v, want %v", err, readErr)
	}
}

func TestReportConflictToleratesMalformedResolutionAndAuditFailure(t *testing.T) {
	qRepo, aRepo := setupTestRepos(t)
	engine := mustNewEngine(t, "http://unused.invalid", qRepo, aRepo, nil, nil, DefaultConfig())
	engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
		return make(chan int), nil
	}
	if err := qRepo.db.Close(); err != nil {
		t.Fatalf("close DB: %v", err)
	}
	if err := engine.ReportConflict(context.Background(), "task", "task-1", "overwrite", "server", "local"); err != nil {
		t.Fatalf("ReportConflict must not fail on best-effort audit: %v", err)
	}
}

func TestApplyArticlePropagatesStoreCancellation(t *testing.T) {
	_, qRepo, aRepo, _, kbRepo := newApplyTestRepos(t)
	engine := mustNewEngine(t, "http://unused.invalid", qRepo, aRepo, nil, kbRepo, DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := engine.applyArticle(ctx, articleSummaryWire{
		ArticleID: "kb-1", Title: "article", Body: "body", Frontmatter: json.RawMessage(`{}`),
		AuthorAgentID: "agent-1", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("applyArticle error = %v, want context.Canceled", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

type errorReadCloser struct{ err error }

func (r errorReadCloser) Read([]byte) (int, error) { return 0, r.err }
func (errorReadCloser) Close() error               { return nil }
