package localapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/scheduler"
	syncpkg "github.com/H4RL33/wormhole/internal/runtime/sync"
)

func TestLocalSyncStatusRejectsMalformedUnboundAndUnavailableRequests(t *testing.T) {
	srv, _ := newMCPTestServer(t)
	ctx := context.Background()
	for _, raw := range []json.RawMessage{json.RawMessage(`{`), json.RawMessage(`{}`)} {
		if _, err := srv.localSyncStatus(ctx, raw); err == nil {
			t.Fatalf("localSyncStatus(%s) succeeded", raw)
		}
	}
	if _, err := srv.localSyncStatus(ctx, json.RawMessage(`{"project_id":"project-1"}`)); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("nil-provider status error = %v", err)
	}

	srv.isMultiOrg = true
	if _, err := srv.localSyncStatus(ctx, json.RawMessage(`{"project_id":"unbound"}`)); err == nil {
		t.Fatal("multi-org sync status accepted an unbound project")
	}
	srv.isMultiOrg = false
	provider := &fixedSyncStatusProvider{status: syncpkg.Status{State: syncpkg.StateOnline}}
	srv.SetSyncStatusProvider(provider)
	if status, err := srv.localSyncStatus(ctx, json.RawMessage(`{"project_id":"project-1"}`)); err != nil || status.State != syncpkg.StateOnline {
		t.Fatalf("localSyncStatus = %+v, err=%v", status, err)
	}
}

func TestActiveLocalPermissionCheckCoversStorageBoundary(t *testing.T) {
	ctx := context.Background()
	srv, _ := newMCPTestServer(t)
	if err := srv.authorizeLocalPermission(ctx, "", nil); err != nil {
		t.Fatalf("empty permission: %v", err)
	}
	unavailable, _ := newMCPTestServer(t)
	if err := unavailable.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := unavailable.authorizeLocalPermission(ctx, "task.create", json.RawMessage(`{"project_id":"project-1"}`)); err == nil || !strings.Contains(err.Error(), "authorize") {
		t.Fatalf("unavailable authorization store error = %v", err)
	}
}

func TestTaskRouteRequiresSyncQueueAfterScheduler(t *testing.T) {
	srv := &Server{scheduler: scheduler.NewScheduler()}
	if _, err := srv.handleTaskRoute(context.Background(), json.RawMessage(`{"capability":"code"}`)); err == nil || !strings.Contains(err.Error(), "sync queue") {
		t.Fatalf("handleTaskRoute nil queue error = %v", err)
	}
}

func TestAgentAndRouteHandlersCoverExplicitScopeAndFailureBoundaries(t *testing.T) {
	ctx := context.Background()
	srv, store, _, _, _ := newTaskRouteTestRuntime(t, "project-1")
	register := json.RawMessage(`{"agent_id":"agent","capabilities":["code"],"project_id":"project-1"}`)
	if _, err := srv.handleAgentRegister(ctx, register); err != nil {
		t.Fatalf("explicit-project register: %v", err)
	}
	if _, err := srv.handleAgentPresence(ctx, json.RawMessage(`{"agent_id":"agent","status":"busy","project_id":"project-1"}`)); err != nil {
		t.Fatalf("explicit-project presence: %v", err)
	}
	if _, err := srv.handleAgentList(ctx, json.RawMessage(`{"project_id":"project-1"}`)); err != nil {
		t.Fatalf("explicit-project list: %v", err)
	}
	if _, err := srv.handleAgentPresence(ctx, json.RawMessage(`{"agent_id":"missing","status":"busy"}`)); err == nil {
		t.Fatal("presence update accepted an unknown agent")
	}

	srv.isMultiOrg = true
	for name, call := range map[string]func() error{
		"register": func() error {
			_, err := srv.handleAgentRegister(ctx, json.RawMessage(`{"agent_id":"other","project_id":"unbound"}`))
			return err
		},
		"presence": func() error {
			_, err := srv.handleAgentPresence(ctx, json.RawMessage(`{"agent_id":"agent","status":"busy","project_id":"unbound"}`))
			return err
		},
		"list": func() error {
			_, err := srv.handleAgentList(ctx, json.RawMessage(`{"project_id":"unbound"}`))
			return err
		},
		"route": func() error {
			_, err := srv.handleTaskRoute(ctx, json.RawMessage(`{"capability":"code","project_id":"unbound"}`))
			return err
		},
	} {
		if err := call(); err == nil {
			t.Fatalf("%s accepted an unbound multi-org project", name)
		}
	}
	srv.isMultiOrg = false

	srv.testBeforeLocalWriteCommit = func(*sql.Tx) error { return errors.New("commit rejected") }
	if _, err := srv.handleTaskRoute(ctx, json.RawMessage(`{"capability":"code","project_id":"project-1"}`)); err == nil || !strings.Contains(err.Error(), "commit") {
		t.Fatalf("route commit-hook error = %v", err)
	}
	srv.testBeforeLocalWriteCommit = nil

	if _, err := store.DB().Exec(`CREATE TRIGGER reject_task_assignment BEFORE UPDATE ON tasks BEGIN SELECT RAISE(ABORT, 'assignment rejected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.handleTaskRoute(ctx, json.RawMessage(`{"capability":"code"}`)); err == nil || !strings.Contains(err.Error(), "assign") {
		t.Fatalf("route assignment error = %v", err)
	}
	unavailable, unavailableStore, _, _, _ := newTaskRouteTestRuntime(t, "project-1")
	if err := unavailableStore.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := unavailable.handleTaskRoute(ctx, json.RawMessage(`{"capability":"code"}`)); err == nil || !strings.Contains(err.Error(), "begin transaction") {
		t.Fatalf("route unavailable-store error = %v", err)
	}
}

func TestWhoAmIFetchAndFallbackCoverRequestTransportDecodeAndCacheFailures(t *testing.T) {
	ctx := context.Background()
	srv, _ := newMCPTestServer(t)
	if _, err := srv.fetchAndCacheWhoAmI(ctx, OrgContext{ProjectID: "project-1", Creds: config.Credentials{Server: ":"}}); err == nil || !strings.Contains(err.Error(), "build request") {
		t.Fatalf("invalid coordination URL error = %v", err)
	}
	if _, err := srv.fetchAndCacheWhoAmI(ctx, OrgContext{ProjectID: "project-1", Creds: config.Credentials{Server: "http://127.0.0.1:0"}}); err == nil || !strings.Contains(err.Error(), "call coordination") {
		t.Fatalf("coordination transport error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		result, _ := json.Marshal(toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: "not-json"}}})
		_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("1"), Result: result})
	}))
	defer server.Close()
	if _, err := srv.fetchAndCacheWhoAmI(ctx, OrgContext{ProjectID: "project-1", Creds: config.Credentials{Server: server.URL}}); err == nil || !strings.Contains(err.Error(), "decode whoami output") {
		t.Fatalf("invalid whoami output error = %v", err)
	}

	unavailable, _ := newMCPTestServer(t)
	unavailable.coordServer = "http://127.0.0.1:0"
	if err := unavailable.store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := unavailable.proxyWhoAmI(ctx); err == nil || !strings.Contains(err.Error(), "read cached whoami") {
		t.Fatalf("unavailable whoami cache error = %v", err)
	}
}
