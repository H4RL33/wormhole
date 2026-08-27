package sync

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestQueueEnqueueTxUsesCallerTransaction(t *testing.T) {
	store, repo, key, _ := queueRouteFixture(t)
	defer store.Close()
	tx, err := repo.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	operation := retainedOperation(50)
	if _, err := repo.EnqueueTx(context.Background(), tx, key, operation, 1); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetEntry(context.Background(), key, operation.ID); !errors.Is(err, ErrQueueNotFound) {
		t.Fatalf("GetEntry after rollback=%v", err)
	}
}

func TestQueueWriteMethodsSurfaceDatabaseErrors(t *testing.T) {
	store, repo, key, _ := queueRouteFixture(t)
	operation := retainedOperation(51)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Enqueue(context.Background(), key, operation, 0); err == nil || !strings.Contains(err.Error(), "enqueue") {
		t.Fatalf("Enqueue=%v", err)
	}
	if err := repo.MarkDelivered(context.Background(), key, operation.ID); err == nil || !strings.Contains(err.Error(), "acquire delivery") {
		t.Fatalf("MarkDelivered=%v", err)
	}
	if err := repo.DeleteEntry(context.Background(), key, operation.ID); err == nil || !strings.Contains(err.Error(), "delete entry") {
		t.Fatalf("DeleteEntry=%v", err)
	}
}

func TestQueueReadMethodsSurfaceDatabaseErrors(t *testing.T) {
	store, repo, key, _ := queueRouteFixture(t)
	operation := retainedOperation(52)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ListPending(context.Background(), key, 10); err == nil || !strings.Contains(err.Error(), "list pending") {
		t.Fatalf("ListPending=%v", err)
	}
	if _, err := repo.GetEntry(context.Background(), key, operation.ID); err == nil || !strings.Contains(err.Error(), "get entry") {
		t.Fatalf("GetEntry=%v", err)
	}
}

func TestQueueAndAuditMethodsPreserveCancellation(t *testing.T) {
	store, repo, key, _ := queueRouteFixture(t)
	defer store.Close()
	audit := NewAuditRepo(store.DB())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	operation := retainedOperation(53)
	if _, err := repo.Enqueue(ctx, key, operation, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("Enqueue=%v", err)
	}
	if _, err := repo.ListPending(ctx, key, 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListPending=%v", err)
	}
	if err := repo.MarkDelivered(ctx, key, operation.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("MarkDelivered=%v", err)
	}
	if _, err := repo.GetEntry(ctx, key, operation.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetEntry=%v", err)
	}
	if err := repo.DeleteEntry(ctx, key, operation.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteEntry=%v", err)
	}
	if _, err := audit.LogConflict(ctx, key, json.RawMessage(`{"kind":"changed"}`), json.RawMessage(`{"actor":"human"}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("LogConflict=%v", err)
	}
	if _, err := audit.ListAudit(ctx, key, 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListAudit=%v", err)
	}
}

func TestQueueListPendingSurfacesScanError(t *testing.T) {
	store, repo, key, _ := queueRouteFixture(t)
	defer store.Close()
	operation := retainedOperation(54)
	if _, err := repo.Enqueue(context.Background(), key, operation, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE sync_queue SET created_at='not-a-time' WHERE
		project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=? AND stream_id=? AND id=?`,
		key.ProjectID, key.WorkspaceID, key.FabricInstanceID, key.RemoteProjectID, key.StreamID, operation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ListPending(context.Background(), key, 10); err == nil || !strings.Contains(err.Error(), "list pending scan") {
		t.Fatalf("ListPending=%v", err)
	}
}

func TestQueueMutationNotFoundIsNamespaceScoped(t *testing.T) {
	store, repo, key, other := queueRouteFixture(t)
	defer store.Close()
	operation := retainedOperation(55)
	if _, err := repo.Enqueue(context.Background(), key, operation, 0); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkDelivered(context.Background(), other, operation.ID); !errors.Is(err, ErrQueueNotFound) {
		t.Fatalf("cross-route MarkDelivered=%v", err)
	}
	if err := repo.DeleteEntry(context.Background(), other, operation.ID); !errors.Is(err, ErrQueueNotFound) {
		t.Fatalf("cross-route DeleteEntry=%v", err)
	}
	if err := repo.DeleteEntry(context.Background(), key, operation.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteEntry(context.Background(), key, operation.ID); !errors.Is(err, ErrQueueNotFound) {
		t.Fatalf("missing DeleteEntry=%v", err)
	}
}

func TestAuditMethodsSurfaceDatabaseErrors(t *testing.T) {
	store, _, key, _ := queueRouteFixture(t)
	audit := NewAuditRepo(store.DB())
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := audit.LogConflict(context.Background(), key, json.RawMessage(`{}`), json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "log conflict") {
		t.Fatalf("LogConflict=%v", err)
	}
	if _, err := audit.ListAudit(context.Background(), key, 10); err == nil || !strings.Contains(err.Error(), "list") {
		t.Fatalf("ListAudit=%v", err)
	}
}

func TestAuditListSurfacesScanErrorAndHandlesNullableValues(t *testing.T) {
	t.Run("scan error", func(t *testing.T) {
		store, _, key, _ := queueRouteFixture(t)
		defer store.Close()
		audit := NewAuditRepo(store.DB())
		entry, err := audit.LogConflict(context.Background(), key, json.RawMessage(`{"kind":"changed"}`), json.RawMessage(`{"actor":"human"}`))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.DB().Exec(`UPDATE sync_audit SET created_at='not-a-time' WHERE
			project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=? AND stream_id=? AND id=?`,
			key.ProjectID, key.WorkspaceID, key.FabricInstanceID, key.RemoteProjectID, key.StreamID, entry.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := audit.ListAudit(context.Background(), key, 10); err == nil || !strings.Contains(err.Error(), "list scan") {
			t.Fatalf("ListAudit=%v", err)
		}
	})
	t.Run("canonical JSON fields", func(t *testing.T) {
		store, _, key, _ := queueRouteFixture(t)
		defer store.Close()
		audit := NewAuditRepo(store.DB())
		conflict, actor := json.RawMessage(`{"kind":"changed"}`), json.RawMessage(`{"actor":"human"}`)
		if _, err := audit.LogConflict(context.Background(), key, conflict, actor); err != nil {
			t.Fatal(err)
		}
		entries, err := audit.ListAudit(context.Background(), key, 10)
		if err != nil || len(entries) != 1 || string(entries[0].ConflictJSON) != string(conflict) || string(entries[0].ActorJSON) != string(actor) {
			t.Fatalf("entries=(%+v,%v)", entries, err)
		}
	})
}
