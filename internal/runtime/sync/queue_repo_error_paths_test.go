package sync

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestQueueEnqueueTxUsesCallerTransaction(t *testing.T) {
	repo := setupQueueRepo(t)
	defer closeQueueRepo(t, repo)

	tx, err := repo.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	entry, err := repo.EnqueueTx(context.Background(), tx, "ns-1", "task", "task-1", "create", json.RawMessage(`{}`), 1)
	if err != nil {
		t.Fatalf("EnqueueTx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if _, err := repo.GetEntry(context.Background(), "ns-1", entry.ID); !errors.Is(err, ErrQueueNotFound) {
		t.Fatalf("GetEntry after rollback = %v, want ErrQueueNotFound", err)
	}
}

func TestQueueWriteMethodsSurfaceDatabaseErrors(t *testing.T) {
	repo := setupQueueRepo(t)
	if err := repo.db.Close(); err != nil {
		t.Fatalf("close DB: %v", err)
	}

	if _, err := repo.Enqueue(context.Background(), "ns-1", "task", "task-1", "create", json.RawMessage(`{}`), 0); err == nil || !strings.Contains(err.Error(), "enqueue") {
		t.Fatalf("Enqueue error = %v, want database error", err)
	}
	if err := repo.MarkDelivered(context.Background(), "ns-1", "entry-1"); err == nil || !strings.Contains(err.Error(), "mark delivered") {
		t.Fatalf("MarkDelivered error = %v, want database error", err)
	}
	if err := repo.DeleteEntry(context.Background(), "ns-1", "entry-1"); err == nil || !strings.Contains(err.Error(), "delete entry") {
		t.Fatalf("DeleteEntry error = %v, want database error", err)
	}
}

func TestQueueReadMethodsSurfaceDatabaseErrors(t *testing.T) {
	repo := setupQueueRepo(t)
	if err := repo.db.Close(); err != nil {
		t.Fatalf("close DB: %v", err)
	}

	if _, err := repo.ListPending(context.Background(), "ns-1", 10); err == nil || !strings.Contains(err.Error(), "list pending") {
		t.Fatalf("ListPending error = %v, want database error", err)
	}
	if _, err := repo.GetEntry(context.Background(), "ns-1", "entry-1"); err == nil || !strings.Contains(err.Error(), "get entry") {
		t.Fatalf("GetEntry error = %v, want database error", err)
	}
}

func TestQueueAndAuditMethodsPreserveCancellation(t *testing.T) {
	t.Run("queue", func(t *testing.T) {
		repo := setupQueueRepo(t)
		defer closeQueueRepo(t, repo)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if _, err := repo.Enqueue(ctx, "ns-1", "task", "task-1", "create", json.RawMessage(`{}`), 0); !errors.Is(err, context.Canceled) {
			t.Fatalf("Enqueue error = %v, want context.Canceled", err)
		}
		if _, err := repo.ListPending(ctx, "ns-1", 10); !errors.Is(err, context.Canceled) {
			t.Fatalf("ListPending error = %v, want context.Canceled", err)
		}
		if err := repo.MarkDelivered(ctx, "ns-1", "entry-1"); !errors.Is(err, context.Canceled) {
			t.Fatalf("MarkDelivered error = %v, want context.Canceled", err)
		}
		if _, err := repo.GetEntry(ctx, "ns-1", "entry-1"); !errors.Is(err, context.Canceled) {
			t.Fatalf("GetEntry error = %v, want context.Canceled", err)
		}
		if err := repo.DeleteEntry(ctx, "ns-1", "entry-1"); !errors.Is(err, context.Canceled) {
			t.Fatalf("DeleteEntry error = %v, want context.Canceled", err)
		}
	})

	t.Run("audit", func(t *testing.T) {
		qRepo, aRepo := setupAuditRepo(t)
		defer closeAuditRepo(t, qRepo)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if _, err := aRepo.LogConflict(ctx, "ns-1", "task", "task-1", "overwrite", "server", "local", "server", "last_write_wins"); !errors.Is(err, context.Canceled) {
			t.Fatalf("LogConflict error = %v, want context.Canceled", err)
		}
		if _, err := aRepo.ListAudit(ctx, "ns-1", 10); !errors.Is(err, context.Canceled) {
			t.Fatalf("ListAudit error = %v, want context.Canceled", err)
		}
	})
}

func TestQueueListPendingSurfacesScanError(t *testing.T) {
	repo := setupQueueRepo(t)
	defer closeQueueRepo(t, repo)
	if _, err := repo.db.Exec(`
		INSERT INTO sync_queue (id, namespace_id, entity_type, entity_id, operation, payload, priority, created_at, updated_at)
		VALUES ('bad-time', 'ns-1', 'task', 'task-1', 'create', '{}', 0, 'not-a-time', 'not-a-time')
	`); err != nil {
		t.Fatalf("insert malformed row: %v", err)
	}
	if _, err := repo.ListPending(context.Background(), "ns-1", 10); err == nil || !strings.Contains(err.Error(), "list pending scan") {
		t.Fatalf("ListPending error = %v, want scan error", err)
	}
}

func TestQueueMutationNotFoundIsNamespaceScoped(t *testing.T) {
	repo := setupQueueRepo(t)
	defer closeQueueRepo(t, repo)
	entry, err := repo.Enqueue(context.Background(), "ns-1", "task", "task-1", "create", json.RawMessage(`{}`), 0)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if err := repo.MarkDelivered(context.Background(), "ns-2", entry.ID); !errors.Is(err, ErrQueueNotFound) {
		t.Fatalf("MarkDelivered cross-namespace = %v, want ErrQueueNotFound", err)
	}
	if err := repo.DeleteEntry(context.Background(), "ns-2", entry.ID); !errors.Is(err, ErrQueueNotFound) {
		t.Fatalf("DeleteEntry cross-namespace = %v, want ErrQueueNotFound", err)
	}
	if err := repo.DeleteEntry(context.Background(), "ns-1", entry.ID); err != nil {
		t.Fatalf("DeleteEntry owner namespace: %v", err)
	}
	if err := repo.DeleteEntry(context.Background(), "ns-1", entry.ID); !errors.Is(err, ErrQueueNotFound) {
		t.Fatalf("DeleteEntry missing = %v, want ErrQueueNotFound", err)
	}
}

func TestAuditMethodsSurfaceDatabaseErrors(t *testing.T) {
	qRepo, aRepo := setupAuditRepo(t)
	if err := qRepo.db.Close(); err != nil {
		t.Fatalf("close DB: %v", err)
	}

	if _, err := aRepo.LogConflict(context.Background(), "ns-1", "task", "task-1", "overwrite", "server", "local", "server", "last_write_wins"); err == nil || !strings.Contains(err.Error(), "log conflict") {
		t.Fatalf("LogConflict error = %v, want database error", err)
	}
	if _, err := aRepo.ListAudit(context.Background(), "ns-1", 10); err == nil || !strings.Contains(err.Error(), "list") {
		t.Fatalf("ListAudit error = %v, want database error", err)
	}
}

func TestAuditListSurfacesScanErrorAndHandlesNullableValues(t *testing.T) {
	t.Run("scan error", func(t *testing.T) {
		qRepo, aRepo := setupAuditRepo(t)
		defer closeAuditRepo(t, qRepo)
		if _, err := qRepo.db.Exec(`
			INSERT INTO sync_audit (id, namespace_id, entity_type, entity_id, created_at)
			VALUES ('bad-time', 'ns-1', 'task', 'task-1', 'not-a-time')
		`); err != nil {
			t.Fatalf("insert malformed row: %v", err)
		}
		if _, err := aRepo.ListAudit(context.Background(), "ns-1", 10); err == nil || !strings.Contains(err.Error(), "list scan") {
			t.Fatalf("ListAudit error = %v, want scan error", err)
		}
	})

	t.Run("nullable fields", func(t *testing.T) {
		qRepo, aRepo := setupAuditRepo(t)
		defer closeAuditRepo(t, qRepo)
		if _, err := qRepo.db.Exec(`
			INSERT INTO sync_audit (id, namespace_id, entity_type, entity_id, created_at)
			VALUES ('nullable', 'ns-1', 'task', 'task-1', CURRENT_TIMESTAMP)
		`); err != nil {
			t.Fatalf("insert nullable row: %v", err)
		}
		entries, err := aRepo.ListAudit(context.Background(), "ns-1", 10)
		if err != nil {
			t.Fatalf("ListAudit: %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("entries = %d, want 1", len(entries))
		}
		entry := entries[0]
		if entry.ConflictType != nil || entry.ServerValue != nil || entry.LocalValue != nil || entry.ResolvedValue != nil || entry.ResolvedBy != nil {
			t.Fatalf("nullable audit fields = %#v, want nil", entry)
		}
	})
}
