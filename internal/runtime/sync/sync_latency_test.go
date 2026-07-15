package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestCheckLatencySensitive_HighPriorityPushesImmediately confirms a
// high-priority (>= HighPriorityThreshold) queue entry is pushed to the
// server by checkLatencySensitive without waiting for the full
// batchInterval to elapse (RFC-0003 §8.2 "latency-sensitive bypass",
// P4 roadmap gap).
func TestCheckLatencySensitive_HighPriorityPushesImmediately(t *testing.T) {
	qRepo, aRepo := setupTestRepos(t)
	defer qRepo.db.Close()

	var pushCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&pushCalls, 1)
		resultData := map[string]interface{}{
			"items_received": 1,
			"applied":        []interface{}{},
			"timestamp":      time.Now().UTC().Format(time.RFC3339),
			"version":        1,
		}
		writeFakeToolResult(w, resultData)
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	engine := New(srv.URL, "token", "ns-1", qRepo, aRepo, nil, nil, cfg)

	ctx := context.Background()
	payload := json.RawMessage(`{"title":"urgent"}`)
	if _, err := qRepo.Enqueue(ctx, "ns-1", "task", "task-1", "create", payload, cfg.HighPriorityThreshold); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if err := engine.checkLatencySensitive(ctx); err != nil {
		t.Fatalf("checkLatencySensitive: %v", err)
	}

	if got := atomic.LoadInt32(&pushCalls); got != 1 {
		t.Fatalf("push calls: got %d, want 1", got)
	}

	entries, err := qRepo.ListPending(ctx, "ns-1", 10)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected entry delivered (0 pending), got %d pending", len(entries))
	}
}

// TestCheckLatencySensitive_LowPriorityDoesNotPush confirms an entry below
// HighPriorityThreshold is left for the normal batchInterval ticker instead
// of being pushed immediately.
func TestCheckLatencySensitive_LowPriorityDoesNotPush(t *testing.T) {
	qRepo, aRepo := setupTestRepos(t)
	defer qRepo.db.Close()

	var pushCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&pushCalls, 1)
		writeFakeToolResult(w, map[string]interface{}{
			"items_received": 1, "applied": []interface{}{}, "timestamp": time.Now().UTC().Format(time.RFC3339), "version": 1,
		})
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	engine := New(srv.URL, "token", "ns-1", qRepo, aRepo, nil, nil, cfg)

	ctx := context.Background()
	payload := json.RawMessage(`{"title":"routine"}`)
	if _, err := qRepo.Enqueue(ctx, "ns-1", "task", "task-1", "create", payload, cfg.HighPriorityThreshold-1); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if err := engine.checkLatencySensitive(ctx); err != nil {
		t.Fatalf("checkLatencySensitive: %v", err)
	}

	if got := atomic.LoadInt32(&pushCalls); got != 0 {
		t.Fatalf("push calls: got %d, want 0 (low priority must wait for batchInterval)", got)
	}
}

// writeFakeToolResult writes a JSON-RPC 2.0 tools/call success envelope
// wrapping resultData, matching callSyncToolWithResult's expected decode
// shape (content[0].text is the marshalled tool output).
func writeFakeToolResult(w http.ResponseWriter, resultData interface{}) {
	dataBytes, _ := json.Marshal(resultData)
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"result": map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": string(dataBytes)},
			},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
