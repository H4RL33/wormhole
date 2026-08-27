package sync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
)

type mutableRouteSource struct {
	mu      sync.Mutex
	binding types.FabricBinding
	profile types.FabricProfile
}

func (s *mutableRouteSource) GetRoute(context.Context, types.WorkspaceScope) (types.FabricBinding, types.FabricProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.binding, s.profile, nil
}

func (s *mutableRouteSource) rotate(baseURL, credentialRef string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.profile.BaseURL = baseURL
	s.profile.CredentialRef = credentialRef
}

type recordingCredentials struct {
	mu     sync.Mutex
	values map[string]string
	reads  []string
}

func (s *recordingCredentials) Read(_ context.Context, ref string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, ref)
	value, ok := s.values[ref]
	if !ok {
		return "", errors.New("secret source contained sensitive detail")
	}
	return value, nil
}

type fixedConflictGate struct {
	open bool
	err  error
}

func (g *fixedConflictGate) HasOpenConflicts(context.Context, types.WorkspaceScope) (bool, error) {
	return g.open, g.err
}

func TestCredentialRotationKeepsOneEngine(t *testing.T) {
	store, queue, key, _ := queueRouteFixture(t)
	defer store.Close()
	var mu sync.Mutex
	var authorizations []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authorizations = append(authorizations, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"ok\":true}"}]}}`)
	}))
	defer server.Close()
	routes := routeSourceForKey(key, server.URL, "keyring:first")
	credentials := &recordingCredentials{values: map[string]string{"keyring:first": "token-one", "keyring:second": "token-two"}}
	engine, err := NewRouted(context.Background(), routes.binding.Workspace.Scope, routes, credentials,
		&fixedConflictGate{}, queue, NewAuditRepo(store.DB()), nil, nil, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.callSyncTool(context.Background(), "wormhole.sync.status", map[string]interface{}{}); err != nil {
		t.Fatal(err)
	}
	routes.rotate(server.URL, "keyring:second")
	if err := engine.callSyncTool(context.Background(), "wormhole.sync.status", map[string]interface{}{}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	gotAuth := append([]string(nil), authorizations...)
	mu.Unlock()
	if fmt.Sprint(gotAuth) != "[Bearer token-one Bearer token-two]" {
		t.Fatalf("Authorization rotations=%v", gotAuth)
	}
	credentials.mu.Lock()
	gotReads := append([]string(nil), credentials.reads...)
	credentials.mu.Unlock()
	if fmt.Sprint(gotReads) != "[keyring:first keyring:second]" {
		t.Fatalf("credential reads=%v", gotReads)
	}
}

func TestFabricFailureDoesNotBlockLocalWriteOrOtherFabric(t *testing.T) {
	store, queue, first, second := queueRouteFixture(t)
	defer store.Close()
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) }))
	defer failing.Close()
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"ok\":true}"}]}}`)
	}))
	defer healthy.Close()
	credentials := &recordingCredentials{values: map[string]string{"keyring:first": "one", "keyring:second": "two"}}
	firstRoutes := routeSourceForKey(first, failing.URL, "keyring:first")
	secondRoutes := routeSourceForKey(second, healthy.URL, "keyring:second")
	firstEngine, err := NewRouted(context.Background(), firstRoutes.binding.Workspace.Scope, firstRoutes, credentials,
		&fixedConflictGate{}, queue, NewAuditRepo(store.DB()), nil, nil, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	secondEngine, err := NewRouted(context.Background(), secondRoutes.binding.Workspace.Scope, secondRoutes, credentials,
		&fixedConflictGate{}, queue, NewAuditRepo(store.DB()), nil, nil, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Enqueue(context.Background(), first, queueOperation("90000000-0000-4000-8000-000000000005"), 0); err != nil {
		t.Fatalf("local write while Fabric unavailable: %v", err)
	}
	if err := firstEngine.callSyncTool(context.Background(), "wormhole.sync.status", nil); !errors.Is(err, ErrFabricUnavailable) {
		t.Fatalf("failing Fabric error=%v, want ErrFabricUnavailable", err)
	}
	if err := secondEngine.callSyncTool(context.Background(), "wormhole.sync.status", nil); err != nil {
		t.Fatalf("other Fabric blocked: %v", err)
	}
	if count, err := queue.PendingCount(context.Background(), first); err != nil || count != 1 {
		t.Fatalf("failed-Fabric pending write=(%d,%v), want (1,nil)", count, err)
	}
}

func TestWorkspaceConflictIsAttentionRequiredAndCredentialErrorsAreRedacted(t *testing.T) {
	if got := stateForSyncError(localstore.ErrWorkspaceConflicted); got != StateAttentionRequired {
		t.Fatalf("stateForSyncError(conflict)=%q", got)
	}
	store, queue, key, _ := queueRouteFixture(t)
	defer store.Close()
	routes := routeSourceForKey(key, "https://fabric.example.test", "keyring:missing")
	engine, err := NewRouted(context.Background(), routes.binding.Workspace.Scope, routes,
		&recordingCredentials{values: map[string]string{}}, &fixedConflictGate{}, queue,
		NewAuditRepo(store.DB()), nil, nil, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.callSyncToolWithResult(context.Background(), "wormhole.sync.status", nil)
	if err == nil || errors.Is(err, ErrFabricUnavailable) || fmt.Sprint(err) != "sync: resolve Fabric credential" {
		t.Fatalf("credential error=%v", err)
	}
}

func TestRoutedEnginePushPullStatusAndConflictLifecycle(t *testing.T) {
	store, queue, key, _ := queueRouteFixture(t)
	defer store.Close()
	ctx := context.Background()
	operation := queueOperation("90000000-0000-4000-8000-000000000007")
	if _, err := queue.Enqueue(ctx, key, operation, 3); err != nil {
		t.Fatal(err)
	}
	routes := routeSourceForKey(key, "https://fabric.example.test", "keyring:test")
	credentials := &recordingCredentials{values: map[string]string{"keyring:test": "token"}}
	cfg := DefaultConfig()
	cfg.BatchInterval, cfg.LatencyCheckInterval, cfg.PullInterval = time.Hour, time.Hour, time.Hour
	engine, err := NewRouted(ctx, routes.binding.Workspace.Scope, routes, credentials,
		&fixedConflictGate{}, queue, NewAuditRepo(store.DB()), nil, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := engine.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if initial != (Status{State: StateOffline, PendingWrites: 1}) {
		t.Fatalf("initial status=%+v", initial)
	}

	var callsMu sync.Mutex
	var calls []string
	pulled := make(chan struct{})
	var pulledOnce sync.Once
	engine.testCallSyncToolWithResultFn = func(_ context.Context, toolName string, _ map[string]interface{}) (interface{}, error) {
		callsMu.Lock()
		calls = append(calls, toolName)
		callsMu.Unlock()
		switch toolName {
		case "wormhole.sync.incremental_push":
			return map[string]interface{}{
				"items_received": 1,
				"applied":        []map[string]interface{}{{"id": operation.ID, "type": string(operation.Kind), "error": ""}},
				"timestamp":      "2026-08-27T12:00:00Z", "version": SyncProtocolVersion,
			}, nil
		case "wormhole.sync.incremental_pull":
			pulledOnce.Do(func() { close(pulled) })
			return map[string]interface{}{"updates": []interface{}{}, "timestamp": "2026-08-27T12:00:01Z", "version": SyncProtocolVersion}, nil
		case "wormhole.sync.conflict_report":
			return map[string]interface{}{"resolved_value": "server"}, nil
		default:
			return nil, fmt.Errorf("unexpected sync tool %q", toolName)
		}
	}
	engine.Start(ctx)
	select {
	case <-pulled:
	case <-time.After(2 * time.Second):
		engine.Stop()
		t.Fatal("routed engine did not complete startup push/pull")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		status, statusErr := engine.Status(ctx)
		if statusErr != nil {
			engine.Stop()
			t.Fatal(statusErr)
		}
		if status == (Status{State: StateOnline, PendingWrites: 0}) {
			break
		}
		if time.Now().After(deadline) {
			engine.Stop()
			t.Fatalf("final status=%+v, want online/0", status)
		}
		time.Sleep(time.Millisecond)
	}
	engine.Stop()
	entry, err := queue.GetEntry(ctx, key, operation.ID)
	if err != nil || entry.DeliveredAt == nil {
		t.Fatalf("delivered entry=(%+v,%v)", entry, err)
	}
	if err := engine.ReportConflict(ctx, "task", "70000000-0000-4000-8000-000000000001", "changed", "server", "local"); err != nil {
		t.Fatal(err)
	}
	audit, err := NewAuditRepo(store.DB()).ListAudit(ctx, key, 10)
	if err != nil || len(audit) != 1 {
		t.Fatalf("conflict audit=(%+v,%v)", audit, err)
	}
	callsMu.Lock()
	gotCalls := append([]string(nil), calls...)
	callsMu.Unlock()
	if fmt.Sprint(gotCalls) != "[wormhole.sync.incremental_push wormhole.sync.incremental_pull wormhole.sync.conflict_report]" {
		t.Fatalf("sync calls=%v", gotCalls)
	}
}

func routeSourceForKey(key types.RemoteBindingKey, baseURL, credentialRef string) *mutableRouteSource {
	profile := types.FabricProfile{ProfileID: "10000000-0000-4000-8000-000000000001", Alias: "primary",
		FabricInstanceID: key.FabricInstanceID, BaseURL: baseURL, Mode: types.FabricModePrivate, CredentialRef: credentialRef}
	return &mutableRouteSource{
		binding: types.FabricBinding{Workspace: types.WorkspaceBinding{Scope: types.WorkspaceScope{ProjectID: key.ProjectID, WorkspaceID: key.WorkspaceID}},
			ProfileID: profile.ProfileID, FabricInstanceID: key.FabricInstanceID, RemoteProjectID: key.RemoteProjectID, StreamID: key.StreamID},
		profile: profile,
	}
}
