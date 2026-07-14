package localstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/H4RL33/wormhole/internal/runtime/sync"
)

// TestSyncQueueCrossNamespaceRejection verifies that sync queue items are isolated by namespace.
// RFC-0003 §7.2 — mandatory cross-namespace rejection test.
func TestSyncQueueCrossNamespaceRejection(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

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
		t.Fatalf("Apply schema: %v", err)
	}

	ctx := context.Background()
	queueRepo := sync.NewQueueRepo(db)
	nsA := "namespace-a"
	nsB := "namespace-b"

	// Enqueue item in namespace A.
	payload := json.RawMessage(`{"task_id":"t-1"}`)
	entryA, err := queueRepo.Enqueue(ctx, nsA, "task", "task-1", "create", payload, 0)
	if err != nil {
		t.Fatalf("Enqueue(nsA): %v", err)
	}

	// Verify it appears in namespace A's pending queue.
	pendingA, err := queueRepo.ListPending(ctx, nsA, 10)
	if err != nil {
		t.Fatalf("ListPending(nsA): %v", err)
	}
	found := false
	for _, e := range pendingA {
		if e.ID == entryA.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Enqueued item not found in namespace A's pending queue")
	}

	// Verify it does NOT appear in namespace B's pending queue.
	pendingB, err := queueRepo.ListPending(ctx, nsB, 10)
	if err != nil {
		t.Fatalf("ListPending(nsB): %v", err)
	}
	for _, e := range pendingB {
		if e.ID == entryA.ID {
			t.Errorf("ListPending(nsB) leaked item from namespace A: %s", e.ID)
		}
	}

	// Verify MarkDelivered(nsB, entryA.ID) fails (namespace filter).
	err = queueRepo.MarkDelivered(ctx, nsB, entryA.ID)
	if !errors.Is(err, sync.ErrQueueNotFound) {
		t.Fatalf("MarkDelivered(nsB): got %v, want sync.ErrQueueNotFound", err)
	}

	// Verify MarkDelivered(nsA, entryA.ID) succeeds.
	if err := queueRepo.MarkDelivered(ctx, nsA, entryA.ID); err != nil {
		t.Fatalf("MarkDelivered(nsA): %v", err)
	}

	// Verify item no longer appears in pending queue for nsA.
	pendingA2, err := queueRepo.ListPending(ctx, nsA, 10)
	if err != nil {
		t.Fatalf("ListPending(nsA) after deliver: %v", err)
	}
	if len(pendingA2) != 0 {
		t.Errorf("After delivery, expected 0 pending in nsA, got %d", len(pendingA2))
	}
}
