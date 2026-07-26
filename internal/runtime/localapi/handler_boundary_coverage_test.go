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
	"time"

	codegraphquery "github.com/H4RL33/wormhole/internal/runtime/codegraph/query"
	codegraphstore "github.com/H4RL33/wormhole/internal/runtime/codegraph/store"
	"github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
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

func TestLocalPermissionChecksCoverScopeCacheAndStorageBoundaries(t *testing.T) {
	ctx := context.Background()
	srv, _ := newMCPTestServer(t)
	if err := srv.authorizeLocalPermission(ctx, "", nil); err != nil {
		t.Fatalf("empty permission: %v", err)
	}
	if _, err := srv.localPermissionGranted(ctx, "task.create", "other-project"); err == nil {
		t.Fatal("localPermissionGranted accepted a mismatched single-org project")
	}
	if _, err := srv.localPermissionGranted(ctx, "task.create", "project-1"); err == nil {
		t.Fatal("localPermissionGranted accepted a missing cached scope")
	}
	if err := srv.store.CacheWhoAmI(ctx, localstore.WhoAmICache{
		AgentID: "agent", ProjectID: "project-1", Permissions: []string{"task.create"}, CachedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if granted, err := srv.localPermissionGranted(ctx, "task.create", "project-1"); err != nil || !granted {
		t.Fatalf("localPermissionGranted = %t, err=%v", granted, err)
	}

	srv.isMultiOrg = true
	if _, err := srv.localPermissionGranted(ctx, "task.create", "unbound"); err == nil {
		t.Fatal("localPermissionGranted accepted an unbound multi-org project")
	}
	srv.isMultiOrg = false

	unavailable, _ := newMCPTestServer(t)
	if err := unavailable.store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := unavailable.localPermissionGranted(ctx, "task.create", "project-1"); err == nil || !strings.Contains(err.Error(), "authorize") {
		t.Fatalf("unavailable permission store error = %v", err)
	}
	if err := unavailable.authorizeLocalPermission(ctx, "task.create", json.RawMessage(`{"project_id":"project-1"}`)); err == nil || !strings.Contains(err.Error(), "authorize") {
		t.Fatalf("unavailable authorization store error = %v", err)
	}
}

func TestCodeGraphHandlersRejectEveryPreExecutionBoundary(t *testing.T) {
	ctx := context.Background()
	srv, _ := newMCPTestServer(t)
	for _, projectID := range []string{"", "project-1"} {
		if _, err := srv.resolveCodeGraphRuntime(projectID); err == nil {
			t.Fatalf("resolveCodeGraphRuntime(%q) succeeded without runtime", projectID)
		}
	}
	if _, err := srv.handleCodeGraphQuery(ctx, json.RawMessage(`{`)); err == nil {
		t.Fatal("code graph query accepted malformed arguments")
	}
	if _, err := srv.handleCodeGraphQuery(ctx, json.RawMessage(`{"project_id":"project-1","intent":"find"}`)); err == nil {
		t.Fatal("code graph query accepted a missing runtime")
	}
	if _, err := srv.handleCodeGraphStatus(ctx, json.RawMessage(`{`)); err == nil {
		t.Fatal("code graph status accepted malformed arguments")
	}
	if _, err := srv.handleCodeGraphStatus(ctx, json.RawMessage(`{"project_id":"project-1"}`)); err == nil {
		t.Fatal("code graph status accepted a missing runtime")
	}

	srv.isMultiOrg = true
	if _, err := srv.resolveCodeGraphRuntime("unbound"); err == nil {
		t.Fatal("code graph runtime resolved an unbound multi-org project")
	}
	srv.isMultiOrg = false

	graphServer, _, _ := newCodeGraphStatusFixture(t)
	query := json.RawMessage(`{"project_id":"project-1","intent":"find"}`)
	if _, err := graphServer.handleCodeGraphQuery(ctx, query); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("query without cached permission error = %v", err)
	}
	if err := graphServer.store.CacheWhoAmI(ctx, localstore.WhoAmICache{
		AgentID: "agent", ProjectID: "project-1", Permissions: []string{codeGraphSourcePermission}, CachedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	invalidEdge := json.RawMessage(`{"project_id":"project-1","intent":"find","include_edges":["invalid"]}`)
	if _, err := graphServer.handleCodeGraphQuery(ctx, invalidEdge); err == nil || !strings.Contains(err.Error(), "invalid include_edges") {
		t.Fatalf("invalid include_edges error = %v", err)
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

func TestCodeGraphResultAndStatusCoverCollectionAndCorruptActiveSnapshotBranches(t *testing.T) {
	result := queryResultForMCP(codegraphquery.Result{
		Edges:           []codegraphquery.Edge{{ID: "edge", Relationship: codegraphstore.RelationshipCalls}},
		StructuralPaths: []codegraphquery.StructuralPath{{FromNodeID: "from", EdgeID: "edge", ToNodeID: "to", Depth: 1}},
	}, 0)
	if len(result.Edges) != 1 || len(result.StructuralPaths) != 1 {
		t.Fatalf("queryResultForMCP = %+v", result)
	}

	for _, table := range []string{"codegraph_nodes", "codegraph_revisions"} {
		t.Run(table, func(t *testing.T) {
			srv, runtime, _ := newCodeGraphStatusFixture(t)
			buildCodeGraphFixture(t, runtime, "active")
			if _, err := srv.store.DB().Exec(`DROP TABLE ` + table); err != nil {
				t.Fatal(err)
			}
			if _, err := srv.handleCodeGraphStatus(context.Background(), json.RawMessage(`{"project_id":"project-1"}`)); err == nil {
				t.Fatalf("status ignored missing %s", table)
			}
		})
	}
}
