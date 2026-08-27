package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	projectstate "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var _ func(*QueueRepo, context.Context, types.RemoteBindingKey, projectstate.OperationV1, int) (QueueEntry, error) = (*QueueRepo).Enqueue
var _ func(*QueueRepo, context.Context, types.RemoteBindingKey, int) ([]QueueEntry, error) = (*QueueRepo).ListPending
var _ func(*QueueRepo, context.Context, types.RemoteBindingKey, string) error = (*QueueRepo).MarkDelivered

func TestCompleteKeyQueueIsolation(t *testing.T) {
	store, repo, first, second := queueRouteFixture(t)
	defer store.Close()
	operation := queueOperation("90000000-0000-4000-8000-000000000001")
	if _, err := repo.Enqueue(context.Background(), first, operation, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Enqueue(context.Background(), second, operation, 2); err != nil {
		t.Fatal(err)
	}
	for key, priority := range map[types.RemoteBindingKey]int{first: 1, second: 2} {
		entries, err := repo.ListPending(context.Background(), key, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Key != key || entries[0].Priority != priority {
			t.Fatalf("ListPending(%+v)=%+v", key, entries)
		}
	}
	if err := repo.MarkDelivered(context.Background(), first, operation.ID); err != nil {
		t.Fatal(err)
	}
	if count, err := repo.PendingCount(context.Background(), second); err != nil || count != 1 {
		t.Fatalf("second PendingCount=(%d,%v), want (1,nil)", count, err)
	}
}

func TestQueueEnqueueNilRepositoryReturnsError(t *testing.T) {
	var repo *QueueRepo
	if _, err := repo.Enqueue(context.Background(), types.RemoteBindingKey{}, projectstate.OperationV1{}, 0); err == nil {
		t.Fatal("Enqueue() error = nil, want unavailable repository error")
	}
}

func TestQueueRejectsBindingMismatchByDirectSQL(t *testing.T) {
	store, _, first, _ := queueRouteFixture(t)
	defer store.Close()
	operation := queueOperation("90000000-0000-4000-8000-000000000002")
	canonical, err := projectstate.CanonicalOperation(operation)
	if err != nil {
		t.Fatal(err)
	}
	first.StreamID = "49999999-9999-4999-8999-999999999999"
	_, err = store.DB().Exec(`
		INSERT INTO sync_queue
		(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,id,operation_json,operation_digest)
		VALUES (?,?,?,?,?,?,?,?)
	`, first.ProjectID, first.WorkspaceID, first.FabricInstanceID, first.RemoteProjectID, first.StreamID,
		operation.ID, string(canonical), operationDigest(canonical))
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		t.Fatalf("mismatched direct queue insert error=%v, want SQLite foreign-key constraint", err)
	}
}

func TestQueueMarkDeliveredRechecksOpenConflictAtomically(t *testing.T) {
	store, repo, key, _ := queueRouteFixture(t)
	operation := queueOperation("90000000-0000-4000-8000-000000000003")
	entry, err := repo.Enqueue(context.Background(), key, operation, 7)
	if err != nil {
		t.Fatal(err)
	}
	before := readRawQueueRow(t, store.DB(), key, entry.ID)
	insertQueueConflict(t, store.DB(), key.ProjectID, string(key.WorkspaceID), "open")
	if err := repo.MarkDelivered(context.Background(), key, entry.ID); !errors.Is(err, localstore.ErrWorkspaceConflicted) {
		t.Fatalf("MarkDelivered error=%v, want ErrWorkspaceConflicted", err)
	}
	databasePath := storePath(t, store)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := localstore.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after := readRawQueueRow(t, reopened.DB(), key, entry.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("conflicted pending row changed:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestQueueMarkDeliveredConflictScopeIsolation(t *testing.T) {
	tests := []struct {
		name         string
		projectDelta bool
		workspace    int
		state        string
		wantConflict bool
	}{
		{name: "resolved exact", state: "resolved"},
		{name: "same project different workspace", workspace: 1, state: "open"},
		{name: "different project", projectDelta: true, state: "open"},
		{name: "exact open", state: "open", wantConflict: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, repo, key, other := queueRouteFixture(t)
			defer store.Close()
			operation := queueOperation("90000000-0000-4000-8000-000000000004")
			if _, err := repo.Enqueue(context.Background(), key, operation, 0); err != nil {
				t.Fatal(err)
			}
			projectID, workspaceID := key.ProjectID, string(key.WorkspaceID)
			if test.workspace != 0 {
				workspaceID = "00000000-0000-4000-8000-000000000013"
				if _, err := store.DB().Exec(`INSERT INTO workspace_bindings
					(project_id,workspace_id,checkout_path,checkout_device,checkout_inode,repository_identity_json,
					 accepted_ref,accepted_commit,accepted_digest,accepted_snapshot,status)
					VALUES (?,?, '/checkout-c',3,13,'{"provider":"github","immutable_id":"repo","canonical_remote":"https://example.test/repo"}',
					 'refs/heads/main','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',?,x'00','clean')`,
					projectID, workspaceID, "sha256:"+strings.Repeat("a", 64)); err != nil {
					t.Fatal(err)
				}
			}
			if test.projectDelta {
				projectID, workspaceID = other.ProjectID, string(other.WorkspaceID)
			}
			insertQueueConflict(t, store.DB(), projectID, workspaceID, test.state)
			err := repo.MarkDelivered(context.Background(), key, operation.ID)
			if test.wantConflict != errors.Is(err, localstore.ErrWorkspaceConflicted) {
				t.Fatalf("MarkDelivered error=%v, wantConflict=%v", err, test.wantConflict)
			}
			if !test.wantConflict && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCompleteKeyQueueAndAuditLifecycle(t *testing.T) {
	store, queue, key, _ := queueRouteFixture(t)
	defer store.Close()
	ctx := context.Background()
	operation := queueOperation("90000000-0000-4000-8000-000000000006")

	tx, err := store.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.EnqueueTx(ctx, tx, key, operation, 4); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	entry, err := queue.GetEntry(ctx, key, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Key != key || entry.Operation.ID != operation.ID || entry.Priority != 4 || entry.DeliveredAt != nil {
		t.Fatalf("queued entry=%+v", entry)
	}
	if err := queue.DeleteEntry(ctx, key, operation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.GetEntry(ctx, key, operation.ID); !errors.Is(err, ErrQueueNotFound) {
		t.Fatalf("deleted entry error=%v, want ErrQueueNotFound", err)
	}

	audit := NewAuditRepo(store.DB())
	for _, conflict := range []json.RawMessage{json.RawMessage(`{"field":"title"}`), json.RawMessage(`{"field":"status"}`)} {
		if _, err := audit.LogConflict(ctx, key, conflict, json.RawMessage(`{"actor":"human"}`)); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := audit.ListAudit(ctx, key, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("audit entries=%d, want 2", len(entries))
	}
	for _, entry := range entries {
		if entry.Key != key || !json.Valid(entry.ConflictJSON) || !json.Valid(entry.ActorJSON) {
			t.Fatalf("audit entry=%+v", entry)
		}
	}
}

func queueRouteFixture(t *testing.T) (*localstore.Store, *QueueRepo, types.RemoteBindingKey, types.RemoteBindingKey) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gateway.db")
	store, err := localstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for index, ids := range [][5]string{
		{"00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "20000000-0000-4000-8000-000000000001", "30000000-0000-4000-8000-000000000001", "40000000-0000-4000-8000-000000000001"},
		{"00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000012", "20000000-0000-4000-8000-000000000002", "30000000-0000-4000-8000-000000000002", "40000000-0000-4000-8000-000000000002"},
	} {
		profileID := []string{"10000000-0000-4000-8000-000000000001", "10000000-0000-4000-8000-000000000002"}[index]
		_, err := store.DB().Exec(`
			INSERT INTO workspace_bindings
			(project_id,workspace_id,checkout_path,checkout_device,checkout_inode,repository_identity_json,
			 accepted_ref,accepted_commit,accepted_digest,accepted_snapshot,status)
			VALUES (?,?,?,?,?,'{"provider":"github","immutable_id":"repo","canonical_remote":"https://example.test/repo"}',
			 'refs/heads/main','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',?,x'00','clean')
		`, ids[0], ids[1], "/checkout-"+string(rune('a'+index)), index+1, index+11,
			"sha256:"+strings.Repeat("a", 64))
		if err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
		if _, err := store.DB().Exec(`INSERT INTO fabric_profiles
			(profile_id,alias,fabric_instance_id,base_url,mode,credential_ref)
			VALUES (?,?,?,?, 'private','keyring:test')`, profileID, "profile-"+string(rune('a'+index)),
			ids[2], "https://fabric.example.test"); err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
		if _, err := store.DB().Exec(`INSERT INTO workspace_fabric_bindings
			(project_id,workspace_id,profile_id,fabric_instance_id,remote_project_id,stream_id,attachment_ref,
			 repository_provider,repository_immutable_id,canonical_ref,writable,state)
			VALUES (?,?,?,?,?,?,?,'github','repo','refs/heads/main',1,'active')`,
			ids[0], ids[1], profileID, ids[2], ids[3], ids[4],
			[]string{"50000000-0000-4000-8000-000000000001", "50000000-0000-4000-8000-000000000002"}[index]); err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
	}
	first := types.RemoteBindingKey{ProjectID: "00000000-0000-4000-8000-000000000001", WorkspaceID: "00000000-0000-4000-8000-000000000011", FabricInstanceID: "20000000-0000-4000-8000-000000000001", RemoteProjectID: "30000000-0000-4000-8000-000000000001", StreamID: "40000000-0000-4000-8000-000000000001"}
	second := types.RemoteBindingKey{ProjectID: "00000000-0000-4000-8000-000000000002", WorkspaceID: "00000000-0000-4000-8000-000000000012", FabricInstanceID: "20000000-0000-4000-8000-000000000002", RemoteProjectID: "30000000-0000-4000-8000-000000000002", StreamID: "40000000-0000-4000-8000-000000000002"}
	return store, NewQueueRepo(store.DB()), first, second
}

func queueOperation(id string) projectstate.OperationV1 {
	return projectstate.OperationV1{
		SchemaVersion: 1, ID: id, Kind: projectstate.OperationTombstone,
		ExpectedViewDigest: projectstate.Digest("sha256:" + strings.Repeat("a", 64)),
		Actor: types.ActorEnvelope{ActorKind: types.ActorHuman,
			HumanPrincipalID: "80000000-0000-4000-8000-000000000001",
			Assurance:        types.AssuranceLocal, OccurredAt: time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)},
		Tombstone: &projectstate.TombstoneOperationV1{
			Key:                   projectstate.RecordKey{Kind: "task", ID: "70000000-0000-4000-8000-000000000001"},
			ExpectedContentDigest: projectstate.Digest("sha256:" + strings.Repeat("b", 64)),
		},
	}
}

type rawQueueRow struct {
	ProjectID, WorkspaceID, FabricID, RemoteProjectID, StreamID, ID string
	OperationJSON, OperationDigest                                  string
	Priority                                                        int
	CreatedAt, UpdatedAt                                            time.Time
	DeliveredAt                                                     sql.NullTime
}

func readRawQueueRow(t *testing.T, db *sql.DB, key types.RemoteBindingKey, id string) rawQueueRow {
	t.Helper()
	var row rawQueueRow
	err := db.QueryRow(`SELECT project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,id,
		operation_json,operation_digest,priority,created_at,updated_at,delivered_at FROM sync_queue
		WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=? AND stream_id=? AND id=?`,
		key.ProjectID, key.WorkspaceID, key.FabricInstanceID, key.RemoteProjectID, key.StreamID, id).Scan(
		&row.ProjectID, &row.WorkspaceID, &row.FabricID, &row.RemoteProjectID, &row.StreamID, &row.ID,
		&row.OperationJSON, &row.OperationDigest, &row.Priority, &row.CreatedAt, &row.UpdatedAt, &row.DeliveredAt)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func insertQueueConflict(t *testing.T, db *sql.DB, projectID, workspaceID, state string) {
	t.Helper()
	resolvedAt := any(nil)
	if state == "resolved" {
		resolvedAt = time.Date(2026, 8, 27, 13, 0, 0, 0, time.UTC)
	}
	_, err := db.Exec(`INSERT INTO workspace_conflicts
		(project_id,workspace_id,occurrence_id,conflict_id,record_kind,record_id,field_path,conflict_kind,
		 base_json,ours_json,theirs_json,state,resolved_at)
		VALUES (?,?,?,?, 'task',?,'title','changed','{}','{}','{}',?,?)`, projectID, workspaceID,
		"60000000-0000-4000-8000-000000000001", "61000000-0000-4000-8000-000000000001",
		"70000000-0000-4000-8000-000000000001", state, resolvedAt)
	if err != nil {
		t.Fatal(err)
	}
}

func storePath(t *testing.T, store *localstore.Store) string {
	t.Helper()
	var path string
	if err := store.DB().QueryRow(`PRAGMA database_list`).Scan(new(int), new(string), &path); err != nil {
		t.Fatal(err)
	}
	return path
}
