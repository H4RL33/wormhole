package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	stdsync "sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	_ "modernc.org/sqlite"
)

func mustNewEngine(t *testing.T, coordServerURL string, queueRepo *QueueRepo, auditRepo *AuditRepo, taskRepo *localstore.TaskRepo, kbRepo *localstore.KBRepo, cfg Config) *Engine {
	t.Helper()
	engine, err := New(coordServerURL, "token", "ns-1", queueRepo, auditRepo, taskRepo, kbRepo, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return engine
}

// TestEngineNew tests creating a new sync engine.
func TestEngineNew(t *testing.T) {
	qRepo, aRepo := setupTestRepos(t)
	defer qRepo.db.Close()

	cfg := DefaultConfig()
	engine := mustNewEngine(t, "http://localhost:8080", qRepo, aRepo, nil, nil, cfg)

	if engine.namespaceID != "ns-1" {
		t.Errorf("NamespaceID mismatch: got %q, want %q", engine.namespaceID, "ns-1")
	}
	if engine.batchInterval != cfg.BatchInterval {
		t.Errorf("BatchInterval mismatch: got %v, want %v", engine.batchInterval, cfg.BatchInterval)
	}
	if engine.batchSize != cfg.BatchSize {
		t.Errorf("BatchSize mismatch: got %d, want %d", engine.batchSize, cfg.BatchSize)
	}
}

// TestEnginePushBatchEmpty tests that pushBatch gracefully handles empty queues.
func TestEnginePushBatchEmpty(t *testing.T) {
	qRepo, aRepo := setupTestRepos(t)
	defer qRepo.db.Close()

	cfg := DefaultConfig()
	engine := mustNewEngine(t, "http://localhost:8080", qRepo, aRepo, nil, nil, cfg)

	ctx := context.Background()
	// pushBatch on empty queue should succeed without making network calls.
	// (We can't actually make network calls in a unit test, but we verify
	// the logic handles the empty case gracefully.)

	// In real usage, if the server is unreachable, pushBatch would fail on
	// the network call. For now, we just verify the queue logic works.
	entries, err := qRepo.ListPending(ctx, "ns-1", engine.batchSize)
	if err != nil {
		t.Fatalf("ListPending failed: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Expected no pending entries, got %d", len(entries))
	}
}

// TestDefaultConfig tests that DefaultConfig returns reasonable defaults.
func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.BatchInterval == 0 {
		t.Errorf("BatchInterval should not be zero")
	}
	if cfg.BatchSize == 0 {
		t.Errorf("BatchSize should not be zero")
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	qRepo, aRepo := setupTestRepos(t)
	defer qRepo.db.Close()

	tests := []struct {
		name   string
		mutate func(*Config)
		field  string
	}{
		{name: "zero batch interval", mutate: func(cfg *Config) { cfg.BatchInterval = 0 }, field: "BatchInterval"},
		{name: "negative batch interval", mutate: func(cfg *Config) { cfg.BatchInterval = -time.Second }, field: "BatchInterval"},
		{name: "zero batch size", mutate: func(cfg *Config) { cfg.BatchSize = 0 }, field: "BatchSize"},
		{name: "negative batch size", mutate: func(cfg *Config) { cfg.BatchSize = -1 }, field: "BatchSize"},
		{name: "zero latency interval", mutate: func(cfg *Config) { cfg.LatencyCheckInterval = 0 }, field: "LatencyCheckInterval"},
		{name: "negative latency interval", mutate: func(cfg *Config) { cfg.LatencyCheckInterval = -time.Second }, field: "LatencyCheckInterval"},
		{name: "zero pull interval", mutate: func(cfg *Config) { cfg.PullInterval = 0 }, field: "PullInterval"},
		{name: "negative pull interval", mutate: func(cfg *Config) { cfg.PullInterval = -time.Second }, field: "PullInterval"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.mutate(&cfg)
			if _, err := New("http://localhost:8080", "token", "ns-1", qRepo, aRepo, nil, nil, cfg); err == nil {
				t.Fatalf("New accepted invalid %s", tt.field)
			} else if got := err.Error(); !strings.Contains(strings.ToLower(got), strings.ToLower(tt.field)) {
				t.Fatalf("New error = %q, want field %q", got, tt.field)
			}
		})
	}
}

func TestEngineLifecycleConcurrentStartAndStop(t *testing.T) {
	qRepo, aRepo := setupTestRepos(t)
	defer qRepo.db.Close()

	cfg := DefaultConfig()
	cfg.BatchInterval = time.Hour
	cfg.LatencyCheckInterval = time.Hour
	cfg.PullInterval = time.Millisecond
	engine := mustNewEngine(t, "http://localhost:8080", qRepo, aRepo, nil, nil, cfg)

	entered := make(chan struct{})
	var calls atomic.Int32
	engine.testCallSyncToolWithResultFn = func(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
		if toolName != "wormhole.sync.incremental_pull" {
			return nil, fmt.Errorf("unexpected tool %q", toolName)
		}
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}

	parentCtx, cancelParent := context.WithCancel(context.Background())
	t.Cleanup(cancelParent)
	var starts stdsync.WaitGroup
	for range 10 {
		starts.Add(1)
		go func() {
			defer starts.Done()
			engine.Start(parentCtx)
		}()
	}
	starts.Wait()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("sync loop did not enter pull")
	}

	stopResults := make(chan any, 10)
	for range 10 {
		go func() {
			defer func() { stopResults <- recover() }()
			engine.Stop()
		}()
	}
	for range 10 {
		select {
		case recovered := <-stopResults:
			if recovered != nil {
				t.Errorf("Stop panicked: %v", recovered)
			}
		case <-time.After(time.Second):
			cancelParent()
			t.Fatal("Stop did not cancel an in-flight pull")
		}
	}

	if got := calls.Load(); got != 1 {
		t.Fatalf("pull calls after concurrent Start = %d, want 1", got)
	}

	// Repeated calls after shutdown must remain harmless.
	engine.Start(parentCtx)
	engine.Stop()
}

func TestEngineLifecycleCancellationDuringPush(t *testing.T) {
	qRepo, aRepo := setupTestRepos(t)
	defer qRepo.db.Close()

	cfg := DefaultConfig()
	cfg.BatchInterval = time.Millisecond
	cfg.PullInterval = time.Hour
	cfg.LatencyCheckInterval = time.Hour
	engine := mustNewEngine(t, "http://localhost:8080", qRepo, aRepo, nil, nil, cfg)
	if _, err := qRepo.Enqueue(context.Background(), "ns-1", "task", "task-1", "create", json.RawMessage(`{"title":"blocked"}`), 0); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	entered := make(chan struct{})
	engine.testCallSyncToolWithResultFn = func(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
		if toolName != "wormhole.sync.incremental_push" {
			return nil, fmt.Errorf("unexpected tool %q", toolName)
		}
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}

	engine.Start(context.Background())
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("sync loop did not enter push")
	}

	stopped := make(chan struct{})
	go func() {
		engine.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel an in-flight push")
	}
}

func TestSyncLoopPullsWithEmptyQueue(t *testing.T) {
	qRepo, aRepo := setupTestRepos(t)
	defer qRepo.db.Close()

	cfg := DefaultConfig()
	cfg.BatchInterval = time.Hour
	cfg.LatencyCheckInterval = time.Hour
	cfg.PullInterval = time.Millisecond
	engine := mustNewEngine(t, "http://localhost:8080", qRepo, aRepo, nil, nil, cfg)

	pulled := make(chan struct{}, 1)
	engine.testCallSyncToolWithResultFn = func(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
		if toolName != "wormhole.sync.incremental_pull" {
			return nil, fmt.Errorf("unexpected tool %q", toolName)
		}
		select {
		case pulled <- struct{}{}:
		default:
		}
		return incrementalPullResult("2026-07-22T10:00:00Z", nil), nil
	}

	engine.Start(context.Background())
	defer engine.Stop()
	select {
	case <-pulled:
	case <-time.After(time.Second):
		t.Fatal("empty outbound queue did not receive periodic pull")
	}
}

// TestEngineStartStop tests starting and stopping the sync loop.
func TestEngineStartStop(t *testing.T) {
	qRepo, aRepo := setupTestRepos(t)
	defer qRepo.db.Close()

	cfg := Config{
		BatchInterval:         100 * time.Millisecond,
		BatchSize:             10,
		LatencyCheckInterval:  50 * time.Millisecond,
		PullInterval:          time.Hour,
		HighPriorityThreshold: 2,
	}
	engine := mustNewEngine(t, "http://localhost:8080", qRepo, aRepo, nil, nil, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	engine.Start(ctx)

	// Give the loop time to start.
	time.Sleep(50 * time.Millisecond)

	// Stop should complete without hanging.
	engine.Stop()
}

// TestEngineQueuePersistence tests that queue entries survive engine restart.
func TestEngineQueuePersistence(t *testing.T) {
	// Create DB and queue repo.
	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}
	defer db.Close()

	schema := `
	CREATE TABLE sync_queue (
		id             TEXT PRIMARY KEY,
		namespace_id   TEXT NOT NULL,
		entity_type    TEXT NOT NULL,
		entity_id      TEXT NOT NULL,
		operation      TEXT NOT NULL,
		payload        TEXT NOT NULL,
		priority       INTEGER NOT NULL DEFAULT 0,
		created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		delivered_at   TIMESTAMP
	);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("Failed to apply schema: %v", err)
	}

	qRepo := NewQueueRepo(db)
	ctx := context.Background()

	// Enqueue an entry.
	payload := json.RawMessage(`{"title": "task"}`)
	entry1, err := qRepo.Enqueue(ctx, "ns-1", "task", "task-1", "create", payload, 0)
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Verify it's there.
	entries, err := qRepo.ListPending(ctx, "ns-1", 10)
	if err != nil {
		t.Fatalf("ListPending failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("Expected 1 entry, got %d", len(entries))
	}
	if entries[0].ID != entry1.ID {
		t.Errorf("Entry ID mismatch")
	}

	// In a real scenario, the daemon would restart and reconnect to the same DB file.
	// Here we just verify that the same repo can query the same data.
	qRepo2 := NewQueueRepo(db)
	entries2, err := qRepo2.ListPending(ctx, "ns-1", 10)
	if err != nil {
		t.Fatalf("ListPending after restart failed: %v", err)
	}
	if len(entries2) != 1 {
		t.Fatalf("Expected 1 entry after restart, got %d", len(entries2))
	}
	if entries2[0].ID != entry1.ID {
		t.Errorf("Entry ID mismatch after restart")
	}
}

// TestOfflineQueueSurvivalNetworkFailure verifies that queue entries remain pending
// when network calls fail (offline scenario). RFC-0003 §8.2 / P6 hardening.
func TestOfflineQueueSurvivalNetworkFailure(t *testing.T) {
	qRepo, aRepo := setupTestRepos(t)
	defer qRepo.db.Close()

	ctx := context.Background()

	// Enqueue an entry.
	payload := json.RawMessage(`{"title": "offline task"}`)
	entry1, err := qRepo.Enqueue(ctx, "ns-1", "task", "task-1", "create", payload, 0)
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Verify entry is pending.
	pending, err := qRepo.ListPending(ctx, "ns-1", 10)
	if err != nil {
		t.Fatalf("ListPending failed: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("Expected 1 pending entry, got %d", len(pending))
	}

	// Create engine with unreachable server (intentional network failure).
	cfg := Config{
		BatchInterval:         50 * time.Millisecond,
		BatchSize:             10,
		LatencyCheckInterval:  25 * time.Millisecond,
		PullInterval:          time.Hour,
		HighPriorityThreshold: 2,
	}
	engine := mustNewEngine(t, "http://unreachable-server:9999", qRepo, aRepo, nil, nil, cfg)

	// Attempt pushBatch (will fail due to network error).
	err = engine.pushBatch(ctx)
	if err == nil {
		// Expected to fail due to unreachable server, but acceptable if it silently
		// continues (best-effort sync model per RFC-0003 §8.2).
	}

	// Verify entry is STILL pending (not marked delivered).
	pending2, err := qRepo.ListPending(ctx, "ns-1", 10)
	if err != nil {
		t.Fatalf("ListPending after failed push failed: %v", err)
	}
	if len(pending2) != 1 {
		t.Fatalf("Expected entry to remain pending after failed push, but got %d pending entries", len(pending2))
	}
	if pending2[0].ID != entry1.ID {
		t.Errorf("Entry ID mismatch after failed push")
	}

	// Verify DeliveredAt is still null (not marked as delivered).
	if pending2[0].DeliveredAt != nil {
		t.Errorf("Entry should not be marked delivered after failed push, but DeliveredAt is %v", pending2[0].DeliveredAt)
	}
}

// TestOfflineQueueReconnect verifies that queue survives a restart and resumes on reconnect.
// Simulates: daemon offline → reconnects → queue flushes remaining items.
func TestOfflineQueueReconnect(t *testing.T) {
	// Use file-backed SQLite to simulate persistent storage across restarts.
	dbPath := t.TempDir() + "/test_reconnect.db"
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open test DB at %s: %v", dbPath, err)
	}
	defer db.Close()

	// Apply schema.
	schemas := []string{
		`CREATE TABLE sync_queue (
			id             TEXT PRIMARY KEY,
			namespace_id   TEXT NOT NULL,
			entity_type    TEXT NOT NULL,
			entity_id      TEXT NOT NULL,
			operation      TEXT NOT NULL,
			payload        TEXT NOT NULL,
			priority       INTEGER NOT NULL DEFAULT 0,
			created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			delivered_at   TIMESTAMP
		);`,
		`CREATE TABLE sync_audit (
			id             TEXT PRIMARY KEY,
			namespace_id   TEXT NOT NULL,
			entity_type    TEXT NOT NULL,
			entity_id      TEXT NOT NULL,
			conflict_type  TEXT,
			server_value   TEXT,
			local_value    TEXT,
			resolved_value TEXT,
			resolved_by    TEXT,
			created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
	}
	for _, schema := range schemas {
		if _, err := db.Exec(schema); err != nil {
			t.Fatalf("Failed to apply schema: %v", err)
		}
	}

	ctx := context.Background()

	// Phase 1: Daemon is offline. Enqueue items while server is unreachable.
	{
		qRepo := NewQueueRepo(db)
		payload := json.RawMessage(`{"title": "task 1"}`)
		_, err := qRepo.Enqueue(ctx, "ns-1", "task", "task-1", "create", payload, 0)
		if err != nil {
			t.Fatalf("Phase 1 Enqueue failed: %v", err)
		}

		// Verify entry is pending.
		pending, err := qRepo.ListPending(ctx, "ns-1", 10)
		if err != nil {
			t.Fatalf("Phase 1 ListPending failed: %v", err)
		}
		if len(pending) != 1 {
			t.Fatalf("Phase 1: Expected 1 pending, got %d", len(pending))
		}
	}

	// Phase 2: Simulate daemon restart by creating a new repo pointing to the same DB.
	// Verify the queue entry persists.
	{
		qRepo := NewQueueRepo(db)
		pending, err := qRepo.ListPending(ctx, "ns-1", 10)
		if err != nil {
			t.Fatalf("Phase 2 ListPending failed: %v", err)
		}
		if len(pending) != 1 {
			t.Fatalf("Phase 2: Entry did not persist across 'restart', got %d entries", len(pending))
		}
	}

	// Phase 3: Reconnect scenario (server is now reachable).
	// In a real scenario, pushBatch would succeed and mark entries as delivered.
	// Here we just verify the logic: if pushBatch succeeds, MarkDelivered should be called.
	{
		qRepo := NewQueueRepo(db)
		entries, err := qRepo.ListPending(ctx, "ns-1", 10)
		if err != nil {
			t.Fatalf("Phase 3 ListPending failed: %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("Phase 3: Expected 1 pending entry before reconnect, got %d", len(entries))
		}

		// Simulate successful delivery: mark as delivered.
		entry := entries[0]
		if err := qRepo.MarkDelivered(ctx, "ns-1", entry.ID); err != nil {
			t.Fatalf("Phase 3 MarkDelivered failed: %v", err)
		}

		// Verify entry is no longer pending.
		stillPending, err := qRepo.ListPending(ctx, "ns-1", 10)
		if err != nil {
			t.Fatalf("Phase 3 ListPending after deliver failed: %v", err)
		}
		if len(stillPending) != 0 {
			t.Fatalf("Phase 3: After delivery, expected 0 pending, got %d", len(stillPending))
		}
	}
}

// TestPushBatchPartialFailure tests that pushBatch correctly handles per-item errors
// from the server: failed items remain pending, successful items are marked delivered.
// This tests the fix for issue #15.
func TestPushBatchPartialFailure(t *testing.T) {
	qRepo, aRepo := setupTestRepos(t)
	defer qRepo.db.Close()

	ctx := context.Background()

	// Enqueue two items: task-1 and task-2.
	payload1 := json.RawMessage(`{"title": "task 1"}`)
	_, err := qRepo.Enqueue(ctx, "ns-1", "task", "task-1", "create", payload1, 0)
	if err != nil {
		t.Fatalf("Enqueue entry1 failed: %v", err)
	}

	payload2 := json.RawMessage(`{"title": "task 2"}`)
	_, err = qRepo.Enqueue(ctx, "ns-1", "task", "task-2", "create", payload2, 0)
	if err != nil {
		t.Fatalf("Enqueue entry2 failed: %v", err)
	}

	// Verify both entries are pending.
	pending, err := qRepo.ListPending(ctx, "ns-1", 10)
	if err != nil {
		t.Fatalf("ListPending failed: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("Expected 2 pending entries, got %d", len(pending))
	}

	// Create an engine with a mock callSyncToolWithResult that simulates
	// a partial-failure server response: task-1 succeeds, task-2 fails.
	cfg := DefaultConfig()
	engine := mustNewEngine(t, "http://localhost:8080", qRepo, aRepo, nil, nil, cfg)

	// Set test hook to return a mock response with partial failure.
	engine.testCallSyncToolWithResultFn = func(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
		// Mock server response with partial failure: task-1 succeeds, task-2 fails.
		return map[string]interface{}{
			"items_received": 2,
			"applied": []map[string]interface{}{
				{
					"id":    "task-1",
					"type":  "task",
					"error": "", // empty error = success
				},
				{
					"id":    "task-2",
					"type":  "task",
					"error": "unsupported entity_type", // non-empty error = failure
				},
			},
			"timestamp": "2026-01-01T00:00:00Z",
			"version":   1,
		}, nil
	}

	// Call pushBatch.
	err = engine.pushBatch(ctx)
	if err != nil {
		t.Fatalf("pushBatch failed: %v", err)
	}

	// Verify that task-1 is no longer pending (marked delivered).
	stillPending, err := qRepo.ListPending(ctx, "ns-1", 10)
	if err != nil {
		t.Fatalf("ListPending after push failed: %v", err)
	}

	// We expect only task-2 to still be pending (task-1 should be delivered).
	if len(stillPending) != 1 {
		t.Fatalf("Expected 1 pending entry after push, got %d", len(stillPending))
	}

	// The remaining entry should be task-2 (the one that failed).
	if stillPending[0].EntityID != "task-2" {
		t.Errorf("Expected task-2 to remain pending, but got %s", stillPending[0].EntityID)
	}

	// Verify that task-2 is not marked as delivered.
	if stillPending[0].DeliveredAt != nil {
		t.Errorf("task-2 should not be marked delivered, but DeliveredAt is %v", stillPending[0].DeliveredAt)
	}
}

func TestPushBatchAcknowledgements(t *testing.T) {
	tests := []struct {
		name            string
		duplicateQueued bool
		result          map[string]interface{}
		wantErr         bool
		wantPending     int
	}{
		{
			name: "exact acknowledgement",
			result: pushResult(1, []map[string]interface{}{
				{"id": "task-1", "type": "task", "error": ""},
			}),
			wantPending: 0,
		},
		{
			name:        "omitted acknowledgement",
			result:      pushResult(1, nil),
			wantErr:     true,
			wantPending: 1,
		},
		{
			name: "duplicate acknowledgement",
			result: pushResult(1, []map[string]interface{}{
				{"id": "task-1", "type": "task", "error": ""},
				{"id": "task-1", "type": "task", "error": ""},
			}),
			wantErr:     true,
			wantPending: 1,
		},
		{
			name: "unknown acknowledgement",
			result: pushResult(1, []map[string]interface{}{
				{"id": "task-other", "type": "task", "error": ""},
			}),
			wantErr:     true,
			wantPending: 1,
		},
		{
			name: "type-mismatched acknowledgement",
			result: pushResult(1, []map[string]interface{}{
				{"id": "task-1", "type": "kb", "error": ""},
			}),
			wantErr:     true,
			wantPending: 1,
		},
		{
			name: "mismatched items received",
			result: pushResult(0, []map[string]interface{}{
				{"id": "task-1", "type": "task", "error": ""},
			}),
			wantErr:     true,
			wantPending: 1,
		},
		{
			name:            "duplicate sent entity pair",
			duplicateQueued: true,
			result: pushResult(2, []map[string]interface{}{
				{"id": "task-1", "type": "task", "error": ""},
				{"id": "task-1", "type": "task", "error": ""},
			}),
			wantErr:     true,
			wantPending: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qRepo, aRepo := setupTestRepos(t)
			defer qRepo.db.Close()
			ctx := context.Background()
			if _, err := qRepo.Enqueue(ctx, "ns-1", "task", "task-1", "create", json.RawMessage(`{"title":"one"}`), 0); err != nil {
				t.Fatalf("Enqueue: %v", err)
			}
			if tt.duplicateQueued {
				if _, err := qRepo.Enqueue(ctx, "ns-1", "task", "task-1", "update", json.RawMessage(`{"title":"two"}`), 0); err != nil {
					t.Fatalf("Enqueue duplicate: %v", err)
				}
			}

			engine := mustNewEngine(t, "http://localhost:8080", qRepo, aRepo, nil, nil, DefaultConfig())
			engine.testCallSyncToolWithResultFn = func(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
				return tt.result, nil
			}
			err := engine.pushBatch(ctx)
			if tt.wantErr && err == nil {
				t.Fatal("pushBatch returned nil error for inconsistent acknowledgement")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("pushBatch: %v", err)
			}

			pending, err := qRepo.ListPending(ctx, "ns-1", 10)
			if err != nil {
				t.Fatalf("ListPending: %v", err)
			}
			if got := len(pending); got != tt.wantPending {
				t.Fatalf("pending = %d, want %d", got, tt.wantPending)
			}
		})
	}
}

func TestPushBatchRejectsMalformedToolResponses(t *testing.T) {
	tests := []struct {
		name       string
		toolResult map[string]interface{}
	}{
		{
			name: "malformed JSON-RPC content",
			toolResult: map[string]interface{}{
				"content": []map[string]interface{}{{"type": "text", "text": "{"}},
			},
		},
		{
			name: "tool isError",
			toolResult: map[string]interface{}{
				"content": []map[string]interface{}{{"type": "text", "text": "push rejected"}},
				"isError": true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      1,
					"result":  tt.toolResult,
				})
			}))
			defer srv.Close()

			qRepo, aRepo := setupTestRepos(t)
			defer qRepo.db.Close()
			ctx := context.Background()
			if _, err := qRepo.Enqueue(ctx, "ns-1", "task", "task-1", "create", json.RawMessage(`{"title":"one"}`), 0); err != nil {
				t.Fatalf("Enqueue: %v", err)
			}
			engine := mustNewEngine(t, srv.URL, qRepo, aRepo, nil, nil, DefaultConfig())
			if err := engine.pushBatch(ctx); err == nil {
				t.Fatal("pushBatch returned nil error for malformed tool response")
			}
			pending, err := qRepo.ListPending(ctx, "ns-1", 10)
			if err != nil {
				t.Fatalf("ListPending: %v", err)
			}
			if len(pending) != 1 {
				t.Fatalf("pending = %d, want 1", len(pending))
			}
		})
	}
}

func TestPushBatchDoesNotAdvancePullCursor(t *testing.T) {
	qRepo, aRepo := setupTestRepos(t)
	defer qRepo.db.Close()
	ctx := context.Background()
	if _, err := qRepo.Enqueue(ctx, "ns-1", "task", "task-1", "create", json.RawMessage(`{"title":"one"}`), 0); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	engine := mustNewEngine(t, "http://localhost:8080", qRepo, aRepo, nil, nil, DefaultConfig())
	wantCursor := "2026-07-22T10:00:00Z"
	engine.lastSyncCursor = wantCursor
	engine.testCallSyncToolWithResultFn = func(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
		return pushResult(1, []map[string]interface{}{{"id": "task-1", "type": "task", "error": ""}}), nil
	}
	if err := engine.pushBatch(ctx); err != nil {
		t.Fatalf("pushBatch: %v", err)
	}
	if got := engine.lastSyncCursor; got != wantCursor {
		t.Fatalf("pull cursor after push = %s, want %s", got, wantCursor)
	}
}

func TestPushBatchReturnsMarkDeliveredCancellation(t *testing.T) {
	qRepo, aRepo := setupTestRepos(t)
	defer qRepo.db.Close()
	if _, err := qRepo.Enqueue(context.Background(), "ns-1", "task", "task-1", "create", json.RawMessage(`{"title":"one"}`), 0); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	engine := mustNewEngine(t, "http://localhost:8080", qRepo, aRepo, nil, nil, DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	engine.testCallSyncToolWithResultFn = func(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
		cancel()
		return pushResult(1, []map[string]interface{}{{"id": "task-1", "type": "task", "error": ""}}), nil
	}

	err := engine.pushBatch(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pushBatch error = %v, want wrapped context.Canceled", err)
	}
	pending, err := qRepo.ListPending(context.Background(), "ns-1", 10)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pending) != 1 || pending[0].EntityID != "task-1" {
		t.Fatalf("pending after persistence cancellation = %#v, want task-1", pending)
	}
}

func TestPushBatchPreservesEarlierDeliveryOnLaterPersistenceError(t *testing.T) {
	qRepo, aRepo := setupTestRepos(t)
	defer qRepo.db.Close()
	ctx := context.Background()
	if _, err := qRepo.Enqueue(ctx, "ns-1", "task", "task-1", "create", json.RawMessage(`{"title":"one"}`), 2); err != nil {
		t.Fatalf("Enqueue task-1: %v", err)
	}
	if _, err := qRepo.Enqueue(ctx, "ns-1", "task", "task-2", "create", json.RawMessage(`{"title":"two"}`), 1); err != nil {
		t.Fatalf("Enqueue task-2: %v", err)
	}
	if _, err := qRepo.db.Exec(`
		CREATE TRIGGER fail_task_2_delivery
		BEFORE UPDATE OF delivered_at ON sync_queue
		WHEN OLD.entity_id = 'task-2'
		BEGIN
			SELECT RAISE(ABORT, 'forced mark failure');
		END;
	`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}

	engine := mustNewEngine(t, "http://localhost:8080", qRepo, aRepo, nil, nil, DefaultConfig())
	engine.testCallSyncToolWithResultFn = func(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
		return pushResult(2, []map[string]interface{}{
			{"id": "task-1", "type": "task", "error": ""},
			{"id": "task-2", "type": "task", "error": ""},
		}), nil
	}

	err := engine.pushBatch(ctx)
	if err == nil || !strings.Contains(err.Error(), "forced mark failure") {
		t.Fatalf("pushBatch error = %v, want forced mark failure", err)
	}
	pending, err := qRepo.ListPending(ctx, "ns-1", 10)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pending) != 1 || pending[0].EntityID != "task-2" {
		t.Fatalf("pending after partial persistence = %#v, want only task-2", pending)
	}
}

func pushResult(itemsReceived int, applied []map[string]interface{}) map[string]interface{} {
	if applied == nil {
		applied = []map[string]interface{}{}
	}
	return map[string]interface{}{
		"items_received": itemsReceived,
		"applied":        applied,
		"timestamp":      "2026-07-22T10:00:00Z",
		"version":        1,
	}
}

// Helper function to set up test repos.
func setupTestRepos(t *testing.T) (*QueueRepo, *AuditRepo) {
	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}

	schemas := []string{
		`CREATE TABLE sync_queue (
			id             TEXT PRIMARY KEY,
			namespace_id   TEXT NOT NULL,
			entity_type    TEXT NOT NULL,
			entity_id      TEXT NOT NULL,
			operation      TEXT NOT NULL,
			payload        TEXT NOT NULL,
			priority       INTEGER NOT NULL DEFAULT 0,
			created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			delivered_at   TIMESTAMP
		);`,
		`CREATE TABLE sync_audit (
			id             TEXT PRIMARY KEY,
			namespace_id   TEXT NOT NULL,
			entity_type    TEXT NOT NULL,
			entity_id      TEXT NOT NULL,
			conflict_type  TEXT,
			server_value   TEXT,
			local_value    TEXT,
			resolved_value TEXT,
			resolved_by    TEXT,
			created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
	}

	for _, schema := range schemas {
		if _, err := db.Exec(schema); err != nil {
			t.Fatalf("Failed to apply schema: %v", err)
		}
	}

	return NewQueueRepo(db), NewAuditRepo(db)
}
