package sync

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestActivityTransportLifecycleDerivesAuthorityAndConverges(t *testing.T) {
	typ := reflect.TypeOf(ActivityLifecycleCommand{})
	wantFields := []string{"ActivityID", "Change"}
	if typ.NumField() != len(wantFields) {
		t.Fatalf("fields=%d", typ.NumField())
	}
	for index, want := range wantFields {
		if typ.Field(index).Name != want {
			t.Fatalf("field[%d]=%s want %s", index, typ.Field(index).Name, want)
		}
	}
	fixture := newActivityTransportFixture(t, 1, true)
	scope := fixture.bindings[0].Workspace.Scope
	client := &activityTestClient{lifecycleResponse: ActivityLifecycleResponse{State: "cancelled"}}
	transport := activityTestTransport(t, fixture, activityRouteSourceForFixture(fixture),
		&activityTestCredentials{values: map[string]string{"keyring:activity-0": "token"}},
		&activityTestConflictGate{open: map[types.WorkspaceScope]bool{}}, &activityTestClientFactory{client: client})
	activity := activityTestOrdinary(activityTestIDOne, time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC))
	if err := transport.Queue(context.Background(), scope, activity); err != nil {
		t.Fatal(err)
	}
	command := ActivityLifecycleCommand{ActivityID: activity.ID, Change: localstore.ActivityLifecycleChange{Kind: "delivery", ReferenceID: activity.ID, ExpectedState: "pending", NextState: "cancelled"}}
	if err := transport.Lifecycle(context.Background(), scope, command); err != nil {
		t.Fatal(err)
	}
	if len(client.lifecycleRequests) != 1 || client.lifecycleRequests[0].AttachmentRef != fixture.bindings[0].AttachmentRef || client.lifecycleRequests[0].ActivityID != activity.ID {
		t.Fatalf("request=%#v", client.lifecycleRequests)
	}
	var state string
	if err := fixture.store.DB().QueryRow(`SELECT state FROM activity_lifecycle WHERE project_id=? AND workspace_id=? AND activity_id=? AND lifecycle_kind='delivery'`, scope.ProjectID, scope.WorkspaceID, activity.ID).Scan(&state); err != nil || state != "cancelled" {
		t.Fatalf("state=(%q,%v)", state, err)
	}
}

func TestActivityTransportLifecycleConflictOpenedAfterRemoteHasZeroLocalDeltaAndRetryConverges(t *testing.T) {
	fixture := newActivityTransportFixture(t, 1, true)
	scope := fixture.bindings[0].Workspace.Scope
	ctx := context.Background()
	routes := activityRouteSourceForFixture(fixture)
	credentials := &activityTestCredentials{values: map[string]string{"keyring:activity-0": "token"}}
	gate := &activityTestConflictGate{open: map[types.WorkspaceScope]bool{}}
	client := &activityTestClient{lifecycleResponse: ActivityLifecycleResponse{State: "cancelled"}}
	transport := activityTestTransport(t, fixture, routes, credentials, gate, &activityTestClientFactory{client: client})
	activity := activityTestOrdinary(activityTestIDOne, time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC))
	if err := transport.Queue(ctx, scope, activity); err != nil {
		t.Fatal(err)
	}
	command := ActivityLifecycleCommand{ActivityID: activity.ID, Change: localstore.ActivityLifecycleChange{Kind: "delivery", ReferenceID: activity.ID, ExpectedState: "pending", NextState: "cancelled"}}
	var before string
	if err := fixture.store.DB().QueryRow(`SELECT state FROM activity_lifecycle WHERE project_id=? AND workspace_id=? AND activity_id=? AND lifecycle_kind='delivery'`, scope.ProjectID, scope.WorkspaceID, activity.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	evidence := localstore.WorkspaceConflictEvidence{ConflictID: "sha256:" + strings.Repeat("a", 64), Key: projectstate.RecordKey{Kind: "task", ID: activityTestTaskID}, FieldPath: "/title", ConflictKind: "same_field", BaseJSON: "{}", OursJSON: "{}", TheirsJSON: "{}"}
	client.afterLifecycle = func() {
		err := fixture.workspaces.WithImmediateWorkspace(ctx, scope, func(tx *localstore.WorkspaceMutationTx) error {
			_, err := tx.ReplaceOpenConflictOccurrences(ctx, []localstore.WorkspaceConflictEvidence{evidence}, time.Date(2026, 8, 30, 12, 1, 0, 0, time.UTC))
			return err
		})
		if err != nil {
			t.Error(err)
		}
	}
	err := transport.Lifecycle(ctx, scope, command)
	if !errors.Is(err, localstore.ErrWorkspaceConflicted) {
		t.Fatalf("Lifecycle error=%v", err)
	}
	var after string
	if err := fixture.store.DB().QueryRow(`SELECT state FROM activity_lifecycle WHERE project_id=? AND workspace_id=? AND activity_id=? AND lifecycle_kind='delivery'`, scope.ProjectID, scope.WorkspaceID, activity.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("state changed %q -> %q", before, after)
	}
	if err := fixture.workspaces.WithImmediateWorkspace(ctx, scope, func(tx *localstore.WorkspaceMutationTx) error {
		_, err := tx.ReplaceOpenConflictOccurrences(ctx, nil, time.Date(2026, 8, 30, 12, 2, 0, 0, time.UTC))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	client.afterLifecycle = nil
	if err := transport.Lifecycle(ctx, scope, command); err != nil {
		t.Fatal(err)
	}
	if len(client.lifecycleRequests) != 2 || !reflect.DeepEqual(client.lifecycleRequests[0], client.lifecycleRequests[1]) {
		t.Fatalf("requests=%#v", client.lifecycleRequests)
	}
	if err := fixture.store.DB().QueryRow(`SELECT state FROM activity_lifecycle WHERE project_id=? AND workspace_id=? AND activity_id=? AND lifecycle_kind='delivery'`, scope.ProjectID, scope.WorkspaceID, activity.ID).Scan(&after); err != nil || after != "cancelled" {
		t.Fatalf("final state=(%q,%v)", after, err)
	}
}

func TestActivityTransportLifecycleRejectsResponseMismatchWithoutLocalDelta(t *testing.T) {
	fixture := newActivityTransportFixture(t, 1, true)
	scope := fixture.bindings[0].Workspace.Scope
	client := &activityTestClient{lifecycleResponse: ActivityLifecycleResponse{State: "delivered"}}
	transport := activityTestTransport(t, fixture, activityRouteSourceForFixture(fixture), &activityTestCredentials{values: map[string]string{"keyring:activity-0": "token"}}, &activityTestConflictGate{open: map[types.WorkspaceScope]bool{}}, &activityTestClientFactory{client: client})
	activity := activityTestOrdinary(activityTestIDOne, time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC))
	if err := transport.Queue(context.Background(), scope, activity); err != nil {
		t.Fatal(err)
	}
	command := ActivityLifecycleCommand{ActivityID: activity.ID, Change: localstore.ActivityLifecycleChange{Kind: "delivery", ReferenceID: activity.ID, ExpectedState: "pending", NextState: "cancelled"}}
	if err := transport.Lifecycle(context.Background(), scope, command); !errors.Is(err, localstore.ErrActivityLifecycleConflict) {
		t.Fatalf("Lifecycle=%v", err)
	}
	var state string
	if err := fixture.store.DB().QueryRow(`SELECT state FROM activity_lifecycle WHERE activity_id=? AND lifecycle_kind='delivery'`, activity.ID).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("state=(%q,%v)", state, err)
	}
}

func TestActivityTransportLifecycleRestartExactRetryConverges(t *testing.T) {
	fixture := newActivityTransportFixture(t, 1, true)
	scope := fixture.bindings[0].Workspace.Scope
	routes := activityRouteSourceForFixture(fixture)
	credentials := &activityTestCredentials{values: map[string]string{"keyring:activity-0": "token"}}
	gate := &activityTestConflictGate{open: map[types.WorkspaceScope]bool{}}
	client := &activityTestClient{lifecycleResponse: ActivityLifecycleResponse{State: "cancelled"}}
	factory := &activityTestClientFactory{client: client}
	transport := activityTestTransport(t, fixture, routes, credentials, gate, factory)
	activity := activityTestOrdinary(activityTestIDOne, time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC))
	if err := transport.Queue(context.Background(), scope, activity); err != nil {
		t.Fatal(err)
	}
	command := ActivityLifecycleCommand{ActivityID: activity.ID, Change: localstore.ActivityLifecycleChange{Kind: "delivery", ReferenceID: activity.ID, ExpectedState: "pending", NextState: "cancelled"}}
	if _, err := fixture.store.DB().Exec(`CREATE TRIGGER fail_activity_lifecycle BEFORE UPDATE ON activity_lifecycle BEGIN SELECT RAISE(ABORT,'forced lifecycle failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := transport.Lifecycle(context.Background(), scope, command); err == nil {
		t.Fatal("forced local failure succeeded")
	}
	var state string
	if err := fixture.store.DB().QueryRow(`SELECT state FROM activity_lifecycle WHERE activity_id=? AND lifecycle_kind='delivery'`, activity.ID).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("state after failure=(%q,%v)", state, err)
	}
	if _, err := fixture.store.DB().Exec(`DROP TRIGGER fail_activity_lifecycle`); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := localstore.Open(fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted, err := NewActivityTransport(routes, credentials, gate, localstore.NewActivityRepo(reopened.DB()), factory)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Lifecycle(context.Background(), scope, command); err != nil {
		t.Fatal(err)
	}
	if len(client.lifecycleRequests) != 2 || !reflect.DeepEqual(client.lifecycleRequests[0], client.lifecycleRequests[1]) {
		t.Fatalf("requests=%#v", client.lifecycleRequests)
	}
	if err := reopened.DB().QueryRow(`SELECT state FROM activity_lifecycle WHERE activity_id=? AND lifecycle_kind='delivery'`, activity.ID).Scan(&state); err != nil || state != "cancelled" {
		t.Fatalf("final state=(%q,%v)", state, err)
	}
}
