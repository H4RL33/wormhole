package localstore

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestActivityRepoResolveOriginRequiresOneCurrentRouteRecord(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	want := installOriginForTest(t, fixture, types.WorkspaceID("b0000000-0000-4000-8000-000000000001"), localActivityIDOne, 0, 1)
	got, err := fixture.repo.ResolveOrigin(context.Background(), want.Route, want.ActivityID)
	if err != nil || got != want {
		t.Fatalf("ResolveOrigin = (%+v,%v), want %+v", got, err, want)
	}
	installOriginForTest(t, fixture, types.WorkspaceID("b0000000-0000-4000-8000-000000000002"), localActivityIDOne, 1, 2)
	if _, err := fixture.repo.ResolveOrigin(context.Background(), want.Route, want.ActivityID); !errors.Is(err, ErrActivityReplayConflict) {
		t.Fatal(err)
	}
}

func installOriginForTest(t *testing.T, fixture localActivityFixture, source types.WorkspaceID, activityID string, after, sequence int64) types.ActivityOriginKey {
	t.Helper()
	activity := localOrdinaryActivity(activityID, fmt.Sprintf("origin-%d", sequence), testUTCNow().Add(time.Duration(sequence)*time.Second))
	delivery := localPullDelivery(t, activity, source, sequence, fixture.policy)
	policyJSON, err := projectstate.CanonicalActivityPolicy(fixture.policy)
	if err != nil {
		t.Fatal(err)
	}
	policyDigest, err := projectstate.DigestActivityPolicy(fixture.policy)
	if err != nil {
		t.Fatal(err)
	}
	err = fixture.repo.AcceptPullBatch(context.Background(), fixture.route, ActivityPullBatch{
		PolicyJSON: policyJSON, HistoricalPolicies: []ActivityPolicyEvidence{{Route: fixture.route, PolicyJSON: policyJSON, PolicyDigest: policyDigest}},
		ExpectedPolicyVersion: fixture.policy.PolicyVersion, ExpectedPolicyDigest: policyDigest,
		ExpectedAfter: after, NextSequence: sequence, Deliveries: []ActivityPullDelivery{delivery},
	})
	if err != nil {
		t.Fatal(err)
	}
	return types.ActivityOriginKey{Route: fixture.route, SourceWorkspaceID: source, ActivityID: activityID}
}
