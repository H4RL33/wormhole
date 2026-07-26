package localstore

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
)

func openBootstrapCoverageStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestApplyBootstrapRejectsEveryCrossNamespaceEntityCollision(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(existing, candidate *bootstrapCollisionFixtures)
	}{
		{"task", func(existing, candidate *bootstrapCollisionFixtures) { candidate.taskID = existing.taskID }},
		{"article", func(existing, candidate *bootstrapCollisionFixtures) { candidate.articleID = existing.articleID }},
		{"event", func(existing, candidate *bootstrapCollisionFixtures) { candidate.eventID = existing.eventID }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := openBootstrapCoverageStore(t)
			existing := bootstrapCollisionFixtures{channelID: "channel-b", taskID: "task-b", articleID: "kb-b", eventID: "event-b"}
			candidate := bootstrapCollisionFixtures{channelID: "channel-a", taskID: "task-a", articleID: "kb-a", eventID: "event-a"}
			tt.mutate(&existing, &candidate)
			if err := store.ApplyBootstrap(context.Background(), "ns-b", existing.snapshot("ns-b"), time.Now().UTC(), nil); err != nil {
				t.Fatalf("seed namespace: %v", err)
			}
			err := store.ApplyBootstrap(context.Background(), "ns-a", candidate.snapshot("ns-a"), time.Now().UTC(), nil)
			if !errors.Is(err, ErrNamespaceCollision) {
				t.Fatalf("ApplyBootstrap collision error = %v, want ErrNamespaceCollision", err)
			}
			var projects int
			if err := store.DB().QueryRow(`SELECT count(*) FROM projects WHERE namespace_id = 'ns-a'`).Scan(&projects); err != nil {
				t.Fatal(err)
			}
			if projects != 0 {
				t.Fatalf("collision left %d candidate project rows", projects)
			}
		})
	}
}

type bootstrapCollisionFixtures struct {
	channelID string
	taskID    string
	articleID string
	eventID   string
}

func (fixture bootstrapCollisionFixtures) snapshot(namespace string) types.BootstrapOrgConfigV1 {
	snapshot := localBootstrapSnapshot(namespace, fixture.channelID)
	snapshot.Tasks[0].ID = fixture.taskID
	snapshot.KB.Articles[0].ID = fixture.articleID
	snapshot.Events[0].ID = fixture.eventID
	return snapshot
}

func TestApplyBootstrapAttemptGuardsAndClosedStoreErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("identity mismatch", func(t *testing.T) {
		store := openBootstrapCoverageStore(t)
		attempt := EnrolmentAttemptRecord{ProjectID: "other", AgentID: "agent-1", PassportID: "passport-1"}
		err := store.ApplyBootstrap(ctx, "ns-1", localBootstrapSnapshot("ns-1", "channel-1"), time.Now().UTC(), &attempt)
		if err == nil || !strings.Contains(err.Error(), "identity mismatch") {
			t.Fatalf("ApplyBootstrap identity mismatch error = %v", err)
		}
		assertNoBootstrapProject(t, store, "ns-1")
	})

	t.Run("attempt not found", func(t *testing.T) {
		store := openBootstrapCoverageStore(t)
		attempt := EnrolmentAttemptRecord{
			ProjectID: "ns-1", IdempotencyKey: "missing", RequestHash: strings.Repeat("a", 64),
			CredentialProfile: "profile", AgentID: "agent-1", PassportID: "passport-1",
		}
		err := store.ApplyBootstrap(ctx, "ns-1", localBootstrapSnapshot("ns-1", "channel-1"), time.Now().UTC(), &attempt)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("ApplyBootstrap missing attempt error = %v, want ErrNotFound", err)
		}
		assertNoBootstrapProject(t, store, "ns-1")
	})

	t.Run("closed database", func(t *testing.T) {
		store := openBootstrapCoverageStore(t)
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		if err := store.ValidateReadyCheckpoint(ctx, "ns-1", "agent-1", "passport-1", "profile"); err == nil || !strings.Contains(err.Error(), "validate ready checkpoint") {
			t.Fatalf("ValidateReadyCheckpoint closed store error = %v", err)
		}
		if err := store.ApplyBootstrap(ctx, "ns-1", localBootstrapSnapshot("ns-1", "channel-1"), time.Now().UTC(), nil); err == nil || !strings.Contains(err.Error(), "begin tx") {
			t.Fatalf("ApplyBootstrap closed store error = %v", err)
		}
	})
}

func assertNoBootstrapProject(t *testing.T, store *Store, namespace string) {
	t.Helper()
	var projects int
	if err := store.DB().QueryRow(`SELECT count(*) FROM projects WHERE namespace_id = ?`, namespace).Scan(&projects); err != nil {
		t.Fatal(err)
	}
	if projects != 0 {
		t.Fatalf("failed bootstrap left %d project rows", projects)
	}
}
