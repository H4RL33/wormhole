package localstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestActivityLifecycleRowsStayProtectedUntilTerminal(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	reference := "c0000000-0000-4000-8000-000000000001"
	record, err := fixture.repo.QueueOutbound(context.Background(), fixture.route,
		localLifecycleActivity(localActivityIDOne, projectstate.ActivityLifecycleRecoveryV1, reference, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	change := ActivityLifecycleChange{Kind: "recovery", ReferenceID: reference, ExpectedState: "pending", NextState: "blocked"}
	if err := fixture.repo.TransitionLifecycle(context.Background(), record.Key, change); err != nil {
		t.Fatal(err)
	}
	if pruned, err := fixture.repo.Prune(context.Background(), fixture.route, fixture.route.WorkspaceID, 10); err != nil || pruned != 0 {
		t.Fatalf("blocked lifecycle prune=(%d,%v)", pruned, err)
	}
	if err := fixture.repo.TransitionLifecycle(context.Background(), record.Key,
		ActivityLifecycleChange{Kind: "recovery", ReferenceID: reference, ExpectedState: "blocked", NextState: "recovered"}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repo.TransitionLifecycle(context.Background(), record.Key,
		ActivityLifecycleChange{Kind: "recovery", ReferenceID: reference, ExpectedState: "blocked", NextState: "recovered"}); err != nil {
		t.Fatalf("exact terminal replay=%v", err)
	}
	if err := fixture.repo.TransitionLifecycle(context.Background(), record.Key,
		ActivityLifecycleChange{Kind: "recovery", ReferenceID: reference, ExpectedState: "cancelled", NextState: "recovered"}); !errors.Is(err, ErrActivityLifecycleConflict) {
		t.Fatalf("non-edge terminal replay=%v", err)
	}
	if err := fixture.repo.TransitionLifecycle(context.Background(), record.Key,
		ActivityLifecycleChange{Kind: "recovery", ReferenceID: reference, ExpectedState: "blocked", NextState: "pending"}); !errors.Is(err, ErrActivityLifecycleConflict) {
		t.Fatalf("terminal rewrite=%v", err)
	}
}
