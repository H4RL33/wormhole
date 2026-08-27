package localstore

import (
	"context"
	"errors"
	"testing"
)

func TestActivityPolicyCASUpdatesOnlyPendingExpectedPolicy(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	record, err := fixture.repo.QueueOutbound(context.Background(), fixture.route,
		localOrdinaryActivity(localActivityIDOne, "policy-cas", testUTCNow()))
	if err != nil {
		t.Fatal(err)
	}
	next := localActivityPolicy(2, 3_000_000)
	if _, err := fixture.repo.ReplacePolicy(context.Background(), fixture.route,
		fixture.policy.PolicyVersion, record.PolicyDigest, next); err != nil {
		t.Fatal(err)
	}
	var expected, created int64
	if err := fixture.store.DB().QueryRow(`SELECT expected_policy_version,created_policy_version FROM activity_outbound_queue WHERE activity_id=?`, record.Key.ActivityID).Scan(&expected, &created); err != nil {
		t.Fatal(err)
	}
	if expected != 2 || created != 1 {
		t.Fatalf("queue policy=(expected %d,created %d), want (2,1)", expected, created)
	}
	if _, err := fixture.repo.ReplacePolicy(context.Background(), fixture.route, 1, record.PolicyDigest, localActivityPolicy(3, 3_000_000)); !errors.Is(err, ErrActivityPolicyChanged) {
		t.Fatalf("stale policy replace=%v", err)
	}
}
