package git

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestActivityStorePolicyPublicationExactReplayAndConflict(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-policy-replay")
	replayed, err := fixture.store.PublishPolicy(context.Background(), fixture.stream, fixture.policy)
	if err != nil || !reflect.DeepEqual(replayed, fixture.policy) {
		t.Fatalf("exact policy replay = (%+v,%v), want %+v", replayed, err, fixture.policy)
	}
	changed := fixture.policy
	changed.TerminalRetentionSeconds = 3_000_000
	if _, err := fixture.store.PublishPolicy(context.Background(), fixture.stream, changed); !errors.Is(err, ErrActivityPolicyChanged) {
		t.Fatalf("changed same-version policy error = %v, want ErrActivityPolicyChanged", err)
	}
	skipped := testActivityPolicy(3, 3_000_000)
	if _, err := fixture.store.PublishPolicy(context.Background(), fixture.stream, skipped); !errors.Is(err, ErrActivityPolicyChanged) {
		t.Fatalf("skipped policy version error = %v, want ErrActivityPolicyChanged", err)
	}
	current, err := fixture.store.CurrentPolicy(context.Background(), fixture.stream)
	if err != nil || !reflect.DeepEqual(current, fixture.policy) {
		t.Fatalf("CurrentPolicy after conflicts = (%+v,%v), want original", current, err)
	}
}

func TestActivityStoreRejectsInvalidPolicyBeforeMutation(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-invalid-policy")
	invalid := fixture.policy
	invalid.TerminalRetentionSeconds = 0
	if _, err := fixture.store.PublishPolicy(context.Background(), fixture.stream, invalid); !errors.Is(err, projectstate.ErrInvalidActivityPolicy) {
		t.Fatalf("invalid policy error = %v, want ErrInvalidActivityPolicy", err)
	}
	current, err := fixture.store.CurrentPolicy(context.Background(), fixture.stream)
	if err != nil || !reflect.DeepEqual(current, fixture.policy) {
		t.Fatalf("invalid policy changed current = (%+v,%v)", current, err)
	}
}
