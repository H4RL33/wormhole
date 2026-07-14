package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestQueueEnqueue tests basic enqueue operations.
func TestQueueEnqueue(t *testing.T) {
	repo := setupQueueRepo(t)
	defer closeQueueRepo(t, repo)

	ctx := context.Background()
	payload := json.RawMessage(`{"title": "test task"}`)

	entry, err := repo.Enqueue(ctx, "ns-1", "task", "task-1", "create", payload, 0)
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	if entry.NamespaceID != "ns-1" {
		t.Errorf("NamespaceID mismatch: got %q, want %q", entry.NamespaceID, "ns-1")
	}
	if entry.EntityType != "task" {
		t.Errorf("EntityType mismatch: got %q, want %q", entry.EntityType, "task")
	}
	if entry.Operation != "create" {
		t.Errorf("Operation mismatch: got %q, want %q", entry.Operation, "create")
	}
	if entry.DeliveredAt != nil {
		t.Errorf("DeliveredAt should be nil for new entry, got %v", entry.DeliveredAt)
	}
}

// TestQueueInvalidOperation tests that invalid operations are rejected.
func TestQueueInvalidOperation(t *testing.T) {
	repo := setupQueueRepo(t)
	defer closeQueueRepo(t, repo)

	ctx := context.Background()
	payload := json.RawMessage(`{}`)

	_, err := repo.Enqueue(ctx, "ns-1", "task", "task-1", "invalid", payload, 0)
	if err == nil {
		t.Fatal("Expected error for invalid operation, got nil")
	}
}

// TestQueueListPending tests listing undelivered entries.
func TestQueueListPending(t *testing.T) {
	repo := setupQueueRepo(t)
	defer closeQueueRepo(t, repo)

	ctx := context.Background()
	payload := json.RawMessage(`{}`)

	// Enqueue three entries with different priorities.
	repo.Enqueue(ctx, "ns-1", "task", "task-1", "create", payload, 0)
	repo.Enqueue(ctx, "ns-1", "kb", "kb-1", "create", payload, 1)
	repo.Enqueue(ctx, "ns-1", "event", "event-1", "create", payload, 2)

	entries, err := repo.ListPending(ctx, "ns-1", 10)
	if err != nil {
		t.Fatalf("ListPending failed: %v", err)
	}

	if len(entries) != 3 {
		t.Errorf("Expected 3 entries, got %d", len(entries))
	}

	// Verify ordering: highest priority first.
	if entries[0].Priority != 2 {
		t.Errorf("First entry priority: got %d, want 2", entries[0].Priority)
	}
	if entries[1].Priority != 1 {
		t.Errorf("Second entry priority: got %d, want 1", entries[1].Priority)
	}
	if entries[2].Priority != 0 {
		t.Errorf("Third entry priority: got %d, want 0", entries[2].Priority)
	}
}

// TestQueueMarkDelivered tests marking entries as delivered.
func TestQueueMarkDelivered(t *testing.T) {
	repo := setupQueueRepo(t)
	defer closeQueueRepo(t, repo)

	ctx := context.Background()
	payload := json.RawMessage(`{}`)

	entry, err := repo.Enqueue(ctx, "ns-1", "task", "task-1", "create", payload, 0)
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	if err := repo.MarkDelivered(ctx, "ns-1", entry.ID); err != nil {
		t.Fatalf("MarkDelivered failed: %v", err)
	}

	// Verify it no longer appears in pending list.
	entries, err := repo.ListPending(ctx, "ns-1", 10)
	if err != nil {
		t.Fatalf("ListPending failed: %v", err)
	}

	if len(entries) != 0 {
		t.Errorf("Expected 0 pending entries after marking delivered, got %d", len(entries))
	}

	// Verify we can get it by ID and it shows delivered_at.
	retrieved, err := repo.GetEntry(ctx, "ns-1", entry.ID)
	if err != nil {
		t.Fatalf("GetEntry failed: %v", err)
	}

	if retrieved.DeliveredAt == nil {
		t.Errorf("DeliveredAt should not be nil after MarkDelivered")
	}
}

// TestQueueCrossNamespaceIsolation tests that entries are scoped to namespace (RFC-0003 §7.2).
func TestQueueCrossNamespaceIsolation(t *testing.T) {
	repo := setupQueueRepo(t)
	defer closeQueueRepo(t, repo)

	ctx := context.Background()
	payload := json.RawMessage(`{}`)

	// Enqueue in two namespaces.
	repo.Enqueue(ctx, "ns-1", "task", "task-1", "create", payload, 0)
	repo.Enqueue(ctx, "ns-2", "task", "task-2", "create", payload, 0)

	// List pending in ns-1 should only return ns-1 entry.
	entries, err := repo.ListPending(ctx, "ns-1", 10)
	if err != nil {
		t.Fatalf("ListPending ns-1 failed: %v", err)
	}

	if len(entries) != 1 {
		t.Errorf("Expected 1 entry in ns-1, got %d", len(entries))
	}
	if entries[0].NamespaceID != "ns-1" {
		t.Errorf("Entry should be scoped to ns-1, got %q", entries[0].NamespaceID)
	}

	// List pending in ns-2 should only return ns-2 entry.
	entries, err = repo.ListPending(ctx, "ns-2", 10)
	if err != nil {
		t.Fatalf("ListPending ns-2 failed: %v", err)
	}

	if len(entries) != 1 {
		t.Errorf("Expected 1 entry in ns-2, got %d", len(entries))
	}
	if entries[0].NamespaceID != "ns-2" {
		t.Errorf("Entry should be scoped to ns-2, got %q", entries[0].NamespaceID)
	}
}

// TestQueueGetEntry tests retrieving a single entry.
func TestQueueGetEntry(t *testing.T) {
	repo := setupQueueRepo(t)
	defer closeQueueRepo(t, repo)

	ctx := context.Background()
	payload := json.RawMessage(`{"data": "test"}`)

	entry, err := repo.Enqueue(ctx, "ns-1", "task", "task-1", "create", payload, 0)
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	retrieved, err := repo.GetEntry(ctx, "ns-1", entry.ID)
	if err != nil {
		t.Fatalf("GetEntry failed: %v", err)
	}

	if retrieved.ID != entry.ID {
		t.Errorf("ID mismatch: got %q, want %q", retrieved.ID, entry.ID)
	}
	if string(retrieved.Payload) != string(payload) {
		t.Errorf("Payload mismatch: got %q, want %q", retrieved.Payload, payload)
	}
}

// TestQueueGetEntryNotFound tests that GetEntry returns ErrQueueNotFound for missing entries.
func TestQueueGetEntryNotFound(t *testing.T) {
	repo := setupQueueRepo(t)
	defer closeQueueRepo(t, repo)

	ctx := context.Background()
	_, err := repo.GetEntry(ctx, "ns-1", "nonexistent")
	if err != ErrQueueNotFound {
		t.Errorf("Expected ErrQueueNotFound, got %v", err)
	}
}

// TestQueueDeleteEntry tests deleting an entry.
func TestQueueDeleteEntry(t *testing.T) {
	repo := setupQueueRepo(t)
	defer closeQueueRepo(t, repo)

	ctx := context.Background()
	payload := json.RawMessage(`{}`)

	entry, err := repo.Enqueue(ctx, "ns-1", "task", "task-1", "create", payload, 0)
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	if err := repo.DeleteEntry(ctx, "ns-1", entry.ID); err != nil {
		t.Fatalf("DeleteEntry failed: %v", err)
	}

	_, err = repo.GetEntry(ctx, "ns-1", entry.ID)
	if err != ErrQueueNotFound {
		t.Errorf("Expected ErrQueueNotFound after deletion, got %v", err)
	}
}

// TestAuditLogConflict tests logging a conflict to the audit trail.
func TestAuditLogConflict(t *testing.T) {
	qRepo, aRepo := setupAuditRepo(t)
	defer closeAuditRepo(t, qRepo)

	ctx := context.Background()
	entry, err := aRepo.LogConflict(ctx, "ns-1", "task", "task-1", "overwrite", "server-val", "local-val", "resolved-val", "last_write_wins")
	if err != nil {
		t.Fatalf("LogConflict failed: %v", err)
	}

	if entry.NamespaceID != "ns-1" {
		t.Errorf("NamespaceID mismatch: got %q, want %q", entry.NamespaceID, "ns-1")
	}
	if entry.EntityType != "task" {
		t.Errorf("EntityType mismatch: got %q, want %q", entry.EntityType, "task")
	}
	if entry.ConflictType == nil || *entry.ConflictType != "overwrite" {
		t.Errorf("ConflictType mismatch")
	}
}

// TestAuditListAudit tests listing audit entries.
func TestAuditListAudit(t *testing.T) {
	qRepo, aRepo := setupAuditRepo(t)
	defer closeAuditRepo(t, qRepo)

	ctx := context.Background()

	aRepo.LogConflict(ctx, "ns-1", "task", "task-1", "overwrite", "sv1", "lv1", "rv1", "last_write_wins")
	time.Sleep(10 * time.Millisecond) // Ensure different timestamps
	aRepo.LogConflict(ctx, "ns-1", "kb", "kb-1", "overwrite", "sv2", "lv2", "rv2", "last_write_wins")

	entries, err := aRepo.ListAudit(ctx, "ns-1", 10)
	if err != nil {
		t.Fatalf("ListAudit failed: %v", err)
	}

	if len(entries) != 2 {
		t.Errorf("Expected 2 audit entries, got %d", len(entries))
	}

	// Verify ordering: most recent first.
	if entries[0].EntityType != "kb" {
		t.Errorf("First entry should be kb, got %q", entries[0].EntityType)
	}
	if entries[1].EntityType != "task" {
		t.Errorf("Second entry should be task, got %q", entries[1].EntityType)
	}
}

// Helper functions for tests.

func setupQueueRepo(t *testing.T) *QueueRepo {
	db, err := openTestDB()
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}

	// Apply schema.
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

	return NewQueueRepo(db)
}

func setupAuditRepo(t *testing.T) (*QueueRepo, *AuditRepo) {
	db, err := openTestDB()
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}

	schema := `
	CREATE TABLE sync_audit (
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
	);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("Failed to apply schema: %v", err)
	}

	return NewQueueRepo(db), NewAuditRepo(db)
}

func closeQueueRepo(t *testing.T, repo *QueueRepo) {
	if err := repo.db.Close(); err != nil {
		t.Logf("Error closing database: %v", err)
	}
}

func closeAuditRepo(t *testing.T, qRepo *QueueRepo) {
	if err := qRepo.db.Close(); err != nil {
		t.Logf("Error closing database: %v", err)
	}
}

func openTestDB() (*sql.DB, error) {
	// Use in-memory SQLite for testing.
	return sql.Open("sqlite", "file::memory:?cache=shared")
}
