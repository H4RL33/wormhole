package localstore

import (
	"context"
	"errors"
	"reflect"
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

func TestActivityPolicyCASAcceptsStrictObservedVersionJump(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	current, err := fixture.repo.CurrentPolicy(context.Background(), fixture.route)
	if err != nil {
		t.Fatal(err)
	}
	observed := localActivityPolicy(3, 3_000_000)
	replaced, err := fixture.repo.ReplacePolicy(context.Background(), fixture.route,
		current.Policy.PolicyVersion, current.PolicyDigest, observed)
	if err != nil {
		t.Fatalf("replace observed v1->v3: %v", err)
	}
	if !reflect.DeepEqual(replaced.Policy, observed) {
		t.Fatalf("replaced policy=%+v, want %+v", replaced.Policy, observed)
	}
	rows, err := fixture.store.DB().Query(`SELECT policy_version FROM activity_policy_versions
		WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=?
		AND stream_id=? AND canonical_ref=? ORDER BY policy_version`, activityRouteArgs(fixture.route)...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var versions []int64
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(versions, []int64{1, 3}) {
		t.Fatalf("observed immutable policy versions=%v, want [1 3]", versions)
	}
	if _, err := fixture.repo.ReplacePolicy(context.Background(), fixture.route,
		3, replaced.PolicyDigest, localActivityPolicy(2, 3_000_000)); !errors.Is(err, ErrActivityPolicyChanged) {
		t.Fatalf("policy downgrade=%v, want ErrActivityPolicyChanged", err)
	}
}

func TestActivityPolicyFailedCASAndInjectedWritePreserveCurrentVersion(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	current, err := fixture.repo.CurrentPolicy(context.Background(), fixture.route)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.DB().Exec(`CREATE TRIGGER activity_policy_current_fault BEFORE UPDATE ON activity_policy_current
		BEGIN SELECT RAISE(ABORT,'injected Activity policy fault'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repo.ReplacePolicy(context.Background(), fixture.route, 1, current.PolicyDigest, localActivityPolicy(2, 3_000_000)); err == nil {
		t.Fatal("injected policy update unexpectedly succeeded")
	}
	var versions int
	if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM activity_policy_versions`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	after, err := fixture.repo.CurrentPolicy(context.Background(), fixture.route)
	if err != nil {
		t.Fatal(err)
	}
	if versions != 1 || after.PolicyDigest != current.PolicyDigest || !reflect.DeepEqual(after.Policy, current.Policy) {
		t.Fatalf("failed policy CAS changed state: versions=%d current=%+v after=%+v", versions, current, after)
	}
}

func TestActivityPolicyAndQueueResultsAreDeepOwned(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	policy, err := fixture.repo.CurrentPolicy(context.Background(), fixture.route)
	if err != nil {
		t.Fatal(err)
	}
	policy.PolicyJSON[0] = 'x'
	again, err := fixture.repo.CurrentPolicy(context.Background(), fixture.route)
	if err != nil {
		t.Fatal(err)
	}
	if again.PolicyJSON[0] == 'x' {
		t.Fatal("CurrentPolicy returned aliased bytes")
	}
	record, err := fixture.repo.QueueOutbound(context.Background(), fixture.route,
		localOrdinaryActivity(localActivityIDOne, "owned", testUTCNow()))
	if err != nil {
		t.Fatal(err)
	}
	record.ActivityJSON[0] = 'x'
	record.Activity.Event.Payload[0] = 'x'
	pending, err := fixture.repo.PendingOutbound(context.Background(), fixture.route, 1)
	if err != nil {
		t.Fatal(err)
	}
	if pending[0].ActivityJSON[0] == 'x' || pending[0].Activity.Event.Payload[0] == 'x' {
		t.Fatal("ActivityRecord returned aliased bytes")
	}
}
