// This external-package test proves complete Fabric-route isolation without
// creating an import cycle between localstore and sync.
package localstore_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	syncpkg "github.com/H4RL33/wormhole/internal/runtime/sync"
	"github.com/H4RL33/wormhole/internal/types"
	projectstate "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestSyncQueueCrossNamespaceRejection(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	keys := []types.RemoteBindingKey{
		{ProjectID: "00000000-0000-4000-8000-000000000001", WorkspaceID: "00000000-0000-4000-8000-000000000011", FabricInstanceID: "20000000-0000-4000-8000-000000000001", RemoteProjectID: "30000000-0000-4000-8000-000000000001", StreamID: "40000000-0000-4000-8000-000000000001"},
		{ProjectID: "00000000-0000-4000-8000-000000000002", WorkspaceID: "00000000-0000-4000-8000-000000000012", FabricInstanceID: "20000000-0000-4000-8000-000000000002", RemoteProjectID: "30000000-0000-4000-8000-000000000002", StreamID: "40000000-0000-4000-8000-000000000002"},
	}
	for index, key := range keys {
		profileID := []string{"10000000-0000-4000-8000-000000000001", "10000000-0000-4000-8000-000000000002"}[index]
		if _, err := store.DB().Exec(`INSERT INTO workspace_bindings
			(project_id,workspace_id,checkout_path,checkout_device,checkout_inode,repository_identity_json,
			 accepted_ref,accepted_commit,accepted_digest,accepted_snapshot,status)
			VALUES (?,?,?,?,?,'{"provider":"github","immutable_id":"repo","canonical_remote":"https://example.test/repo"}',
			 'refs/heads/main','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',?,x'00','clean')`,
			key.ProjectID, key.WorkspaceID, "/checkout-"+string(rune('a'+index)), index+1, index+11,
			"sha256:"+strings.Repeat("a", 64)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.DB().Exec(`INSERT INTO fabric_profiles
			(profile_id,alias,fabric_instance_id,base_url,mode,credential_ref)
			VALUES (?,?,?,?, 'private','keyring:test')`, profileID, "profile-"+string(rune('a'+index)),
			key.FabricInstanceID, "https://fabric.example.test"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.DB().Exec(`INSERT INTO workspace_fabric_bindings
			(project_id,workspace_id,profile_id,fabric_instance_id,remote_project_id,stream_id,attachment_ref,
			 repository_provider,repository_immutable_id,canonical_ref,writable,state)
			VALUES (?,?,?,?,?,?,?,'github','repo','refs/heads/main',1,'active')`,
			key.ProjectID, key.WorkspaceID, profileID, key.FabricInstanceID, key.RemoteProjectID, key.StreamID,
			[]string{"50000000-0000-4000-8000-000000000001", "50000000-0000-4000-8000-000000000002"}[index]); err != nil {
			t.Fatal(err)
		}
	}
	operation := projectstate.OperationV1{
		SchemaVersion: 1, ID: "90000000-0000-4000-8000-000000000060", Kind: projectstate.OperationTombstone,
		ExpectedViewDigest: projectstate.Digest("sha256:" + strings.Repeat("a", 64)),
		Actor: types.ActorEnvelope{ActorKind: types.ActorHuman,
			HumanPrincipalID: "80000000-0000-4000-8000-000000000001",
			Assurance:        types.AssuranceLocal, OccurredAt: time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)},
		Tombstone: &projectstate.TombstoneOperationV1{
			Key:                   projectstate.RecordKey{Kind: "task", ID: "70000000-0000-4000-8000-000000000001"},
			ExpectedContentDigest: projectstate.Digest("sha256:" + strings.Repeat("b", 64)),
		},
	}
	queue := syncpkg.NewQueueRepo(store.DB())
	entry, err := queue.Enqueue(context.Background(), keys[0], operation, 0)
	if err != nil {
		t.Fatal(err)
	}
	if pending, err := queue.ListPending(context.Background(), keys[0], 10); err != nil || len(pending) != 1 {
		t.Fatalf("owner pending=(%+v,%v)", pending, err)
	}
	if pending, err := queue.ListPending(context.Background(), keys[1], 10); err != nil || len(pending) != 0 {
		t.Fatalf("sibling pending=(%+v,%v)", pending, err)
	}
	if err := queue.MarkDelivered(context.Background(), keys[1], entry.ID); !errors.Is(err, syncpkg.ErrQueueNotFound) {
		t.Fatalf("cross-route MarkDelivered=%v", err)
	}
	if err := queue.MarkDelivered(context.Background(), keys[0], entry.ID); err != nil {
		t.Fatal(err)
	}
	if pending, err := queue.ListPending(context.Background(), keys[0], 10); err != nil || len(pending) != 0 {
		t.Fatalf("delivered pending=(%+v,%v)", pending, err)
	}
}
