package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/H4RL33/wormhole/internal/runtime/localapi"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	runtimesync "github.com/H4RL33/wormhole/internal/runtime/sync"
)

type statusCoverageEngine struct {
	status runtimesync.Status
	err    error
}

func (*statusCoverageEngine) Bootstrap(context.Context) error { return nil }
func (*statusCoverageEngine) Start(context.Context)           {}
func (*statusCoverageEngine) Stop()                           {}
func (e *statusCoverageEngine) Status(context.Context) (runtimesync.Status, error) {
	return e.status, e.err
}

func TestSyncGroupStatusReportsConfigurationCapabilityReadinessAndErrors(t *testing.T) {
	ctx := context.Background()
	statusErr := errors.New("status failed")
	ready := &statusCoverageEngine{status: runtimesync.Status{State: runtimesync.StateOnline}}
	failing := &statusCoverageEngine{err: statusErr}
	plain := &fakeGroupEngine{events: &[]string{}}
	group := &syncGroup{
		projects: map[string]syncEngine{"ready": ready, "recovering": ready, "failing": failing, "plain": plain},
		notReady: map[string]bool{"recovering": true},
	}

	if _, err := group.Status(ctx, "missing"); err == nil {
		t.Fatal("Status accepted an unconfigured project")
	}
	if _, err := group.Status(ctx, "plain"); err == nil {
		t.Fatal("Status accepted an engine without status capability")
	}
	if _, err := group.Status(ctx, "failing"); !errors.Is(err, statusErr) {
		t.Fatalf("Status error = %v, want %v", err, statusErr)
	}
	status, err := group.Status(ctx, "ready")
	if err != nil || status.State != runtimesync.StateOnline {
		t.Fatalf("ready status = %+v, err=%v", status, err)
	}
	status, err = group.Status(ctx, "recovering")
	if err != nil || status.State != runtimesync.StateAttentionRequired {
		t.Fatalf("recovering status = %+v, err=%v", status, err)
	}
}

func TestSplitPolicyValuesNormalizesOperatorLists(t *testing.T) {
	for _, test := range []struct {
		input string
		want  []string
	}{
		{"", []string{}},
		{"   ", []string{}},
		{"reader", []string{"reader"}},
		{" reader, ,writer , auditor ", []string{"reader", "writer", "auditor"}},
	} {
		if got := splitPolicyValues(test.input); !reflect.DeepEqual(got, test.want) {
			t.Fatalf("splitPolicyValues(%q) = %#v, want %#v", test.input, got, test.want)
		}
	}
}

func TestEnvironmentEnrolmentPolicyHonorsAbsentAndConfiguredCeilings(t *testing.T) {
	const rolesEnv = "WORMHOLE_ENROLMENT_ROLES"
	const permissionsEnv = "WORMHOLE_ENROLMENT_PERMISSIONS"
	originalRoles, rolesSet := os.LookupEnv(rolesEnv)
	originalPermissions, permissionsSet := os.LookupEnv(permissionsEnv)
	t.Cleanup(func() {
		if rolesSet {
			_ = os.Setenv(rolesEnv, originalRoles)
		} else {
			_ = os.Unsetenv(rolesEnv)
		}
		if permissionsSet {
			_ = os.Setenv(permissionsEnv, originalPermissions)
		} else {
			_ = os.Unsetenv(permissionsEnv)
		}
	})

	_ = os.Unsetenv(rolesEnv)
	_ = os.Unsetenv(permissionsEnv)
	if policy := loadEnrolmentPolicy(); policy != nil {
		t.Fatalf("absent environment policy = %#v, want nil", policy)
	}

	if err := os.Setenv(rolesEnv, " reader, writer "); err != nil {
		t.Fatal(err)
	}
	policy := loadEnrolmentPolicy()
	if policy == nil {
		t.Fatal("configured environment returned a nil policy")
	}
	envelope, err := policy.EnrolmentPermissionEnvelope(context.Background(), "project-1")
	if err != nil {
		t.Fatalf("EnrolmentPermissionEnvelope: %v", err)
	}
	if !reflect.DeepEqual(envelope.Roles, []string{"reader", "writer"}) || len(envelope.Permissions) != 0 {
		t.Fatalf("envelope = %+v, want configured roles and empty permissions", envelope)
	}
}

func TestServeWithSyncClosesServerWhenSyncGroupIsAlreadyStopped(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("open local store: %v", err)
	}
	defer store.Close()
	events := localstore.NewEventRepo(store.DB())
	server, err := localapi.New(
		filepath.Join(t.TempDir(), "gateway.sock"), "", "", "project-1", store,
		localstore.NewTaskRepo(store.DB(), events), events, localstore.NewKBRepo(store.DB()),
		runtimesync.NewQueueRepo(store.DB()),
	)
	if err != nil {
		t.Fatalf("create local API: %v", err)
	}
	defer server.Close()

	group := &syncGroup{}
	group.Stop()
	err = serveWithSync(context.Background(), server, group)
	if err == nil || !strings.Contains(err.Error(), "start sync engines") || !errors.Is(err, errSyncGroupStopped) {
		t.Fatalf("serveWithSync error = %v, want wrapped errSyncGroupStopped", err)
	}
}
