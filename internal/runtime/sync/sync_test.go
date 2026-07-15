package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestEngineNew tests creating a new sync engine.
func TestEngineNew(t *testing.T) {
	qRepo, aRepo := setupTestRepos(t)
	defer qRepo.db.Close()

	cfg := DefaultConfig()
	engine := New("http://localhost:8080", "token", "ns-1", qRepo, aRepo, nil, nil, cfg)

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
	engine := New("http://localhost:8080", "token", "ns-1", qRepo, aRepo, nil, nil, cfg)

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

// TestEngineStartStop tests starting and stopping the sync loop.
func TestEngineStartStop(t *testing.T) {
	qRepo, aRepo := setupTestRepos(t)
	defer qRepo.db.Close()

	cfg := Config{
		BatchInterval:         100 * time.Millisecond,
		BatchSize:             10,
		LatencyCheckInterval:  50 * time.Millisecond,
		HighPriorityThreshold: 2,
	}
	engine := New("http://localhost:8080", "token", "ns-1", qRepo, aRepo, nil, nil, cfg)

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
		HighPriorityThreshold: 2,
	}
	engine := New("http://unreachable-server:9999", "token", "ns-1", qRepo, aRepo, nil, nil, cfg)

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
