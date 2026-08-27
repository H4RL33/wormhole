package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	stdsync "sync"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	projectstate "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var routedTestKeys stdsync.Map

const routedTestProjectID = "00000000-0000-4000-8000-000000000001"

func bindRoutedTestQueue(t *testing.T, store *localstore.Store, queue *QueueRepo) types.RemoteBindingKey {
	t.Helper()
	key := types.RemoteBindingKey{
		ProjectID: routedTestProjectID, WorkspaceID: "00000000-0000-4000-8000-000000000011",
		FabricInstanceID: "20000000-0000-4000-8000-000000000001",
		RemoteProjectID:  "30000000-0000-4000-8000-000000000001",
		StreamID:         "40000000-0000-4000-8000-000000000001",
	}
	if _, err := store.DB().Exec(`
		INSERT OR IGNORE INTO workspace_bindings
		(project_id,workspace_id,checkout_path,checkout_device,checkout_inode,repository_identity_json,
		 accepted_ref,accepted_commit,accepted_digest,accepted_snapshot,status)
		VALUES (?,?, '/routed-test',1,11,
		 '{"provider":"github","immutable_id":"routed-test","canonical_remote":"https://example.test/routed-test"}',
		 'refs/heads/main','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
		 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',x'00','clean')`,
		key.ProjectID, key.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
		INSERT OR IGNORE INTO fabric_profiles
		(profile_id,alias,fabric_instance_id,base_url,mode,credential_ref)
		VALUES ('10000000-0000-4000-8000-000000000001','routed-test',?,
		 'https://fabric.example.test','private','keyring:test')`, key.FabricInstanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
		INSERT OR IGNORE INTO workspace_fabric_bindings
		(project_id,workspace_id,profile_id,fabric_instance_id,remote_project_id,stream_id,attachment_ref,
		 repository_provider,repository_immutable_id,canonical_ref,writable,state)
		VALUES (?,?,'10000000-0000-4000-8000-000000000001',?,?,?,
		 '50000000-0000-4000-8000-000000000001','github','routed-test','refs/heads/main',1,'active')`,
		key.ProjectID, key.WorkspaceID, key.FabricInstanceID, key.RemoteProjectID, key.StreamID); err != nil {
		t.Fatal(err)
	}
	routedTestKeys.Store(queue, key)
	t.Cleanup(func() { routedTestKeys.Delete(queue) })
	return key
}

func bootstrapForProject(projectID string) bootstrapResultWire {
	out := validBootstrapWire()
	out.OrgConfig.Project.ID = projectID
	out.OrgConfig.Identity.Passport.ProjectID = projectID
	for index := range out.OrgConfig.Channels {
		out.OrgConfig.Channels[index].ProjectID = projectID
	}
	for index := range out.OrgConfig.Events {
		out.OrgConfig.Events[index].ProjectID = projectID
	}
	for index := range out.OrgConfig.Tasks {
		out.OrgConfig.Tasks[index].ProjectID = projectID
	}
	for index := range out.OrgConfig.KB.Articles {
		out.OrgConfig.KB.Articles[index].ProjectID = projectID
	}
	out.TaskList = append([]types.BootstrapTaskV1(nil), out.OrgConfig.Tasks...)
	out.KBList = append([]types.BootstrapArticleV1(nil), out.OrgConfig.KB.Articles...)
	return out
}

func setupTestRepos(t *testing.T) (*QueueRepo, *AuditRepo) {
	t.Helper()
	store, queue, key, _ := queueRouteFixture(t)
	routedTestKeys.Store(queue, key)
	t.Cleanup(func() {
		routedTestKeys.Delete(queue)
		_ = store.Close()
	})
	return queue, NewAuditRepo(store.DB())
}

func testRemoteKey(t *testing.T, queue *QueueRepo) types.RemoteBindingKey {
	t.Helper()
	value, ok := routedTestKeys.Load(queue)
	if !ok {
		t.Fatal("queue is not registered with a routed test fixture")
	}
	return value.(types.RemoteBindingKey)
}

func mustNewEngine(t *testing.T, coordServerURL string, queueRepo *QueueRepo, auditRepo *AuditRepo, taskRepo *localstore.TaskRepo, kbRepo *localstore.KBRepo, cfg Config) *Engine {
	t.Helper()
	key := testRemoteKey(t, queueRepo)
	routes := routeSourceForKey(key, coordServerURL, "keyring:test")
	engine, err := NewRouted(context.Background(), routes.binding.Workspace.Scope, routes,
		&recordingCredentials{values: map[string]string{"keyring:test": "token"}},
		&fixedConflictGate{}, queueRepo, auditRepo, taskRepo, kbRepo, cfg)
	if err != nil {
		t.Fatalf("NewRouted: %v", err)
	}
	return engine
}

func retainedOperation(sequence int) projectstate.OperationV1 {
	return queueOperation(fmt.Sprintf("90000000-0000-4000-8000-%012d", sequence))
}

func acknowledge(operation projectstate.OperationV1, message string) map[string]interface{} {
	return map[string]interface{}{"id": operation.ID, "type": string(operation.Kind), "error": message}
}

func pushResult(itemsReceived int, applied []map[string]interface{}) map[string]interface{} {
	if applied == nil {
		applied = []map[string]interface{}{}
	}
	return map[string]interface{}{
		"items_received": itemsReceived,
		"applied":        applied,
		"timestamp":      "2026-07-22T10:00:00Z",
		"version":        SyncProtocolVersion,
	}
}

func pullResult() map[string]interface{} {
	return map[string]interface{}{
		"updates":   []interface{}{},
		"timestamp": "2026-07-22T10:00:00Z",
		"version":   SyncProtocolVersion,
	}
}

func TestEngineNew(t *testing.T) {
	queue, audit := setupTestRepos(t)
	cfg := DefaultConfig()
	engine := mustNewEngine(t, "http://localhost:8080", queue, audit, nil, nil, cfg)
	if engine.remoteKey != testRemoteKey(t, queue) || engine.namespaceID != engine.remoteKey.ProjectID {
		t.Fatalf("routed identity=(%+v,%q)", engine.remoteKey, engine.namespaceID)
	}
	if engine.batchInterval != cfg.BatchInterval || engine.batchSize != cfg.BatchSize {
		t.Fatalf("batch config=(%v,%d)", engine.batchInterval, engine.batchSize)
	}
}

func TestDefaultConfigPullIntervalLeavesSharedNamespaceCapacity(t *testing.T) {
	if got, want := DefaultConfig().PullInterval, 10*time.Second; got != want {
		t.Fatalf("PullInterval=%v, want %v", got, want)
	}
}

func TestEnginePushBatchEmpty(t *testing.T) {
	queue, audit := setupTestRepos(t)
	engine := mustNewEngine(t, "http://unused.invalid", queue, audit, nil, nil, DefaultConfig())
	called := false
	engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
		called = true
		return nil, errors.New("unexpected network call")
	}
	if err := engine.pushBatch(context.Background()); err != nil || called {
		t.Fatalf("pushBatch empty=(%v, called=%v)", err, called)
	}
}

func TestPullIncrementalDefersWhileOutboundWritesArePending(t *testing.T) {
	queue, audit := setupTestRepos(t)
	if _, err := queue.Enqueue(context.Background(), testRemoteKey(t, queue), retainedOperation(1), 0); err != nil {
		t.Fatal(err)
	}
	engine := mustNewEngine(t, "http://unused.invalid", queue, audit, nil, nil, DefaultConfig())
	called := false
	engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
		called = true
		return pullResult(), nil
	}
	if err := engine.PullIncremental(context.Background()); err != nil || called {
		t.Fatalf("PullIncremental pending=(%v, called=%v)", err, called)
	}
}

func TestEngineStartImmediatelyPushesBeforePull(t *testing.T) {
	queue, audit := setupTestRepos(t)
	operation := retainedOperation(2)
	if _, err := queue.Enqueue(context.Background(), testRemoteKey(t, queue), operation, 0); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.BatchInterval, cfg.LatencyCheckInterval, cfg.PullInterval = time.Hour, time.Hour, time.Hour
	engine := mustNewEngine(t, "http://unused.invalid", queue, audit, nil, nil, cfg)
	var mu stdsync.Mutex
	var calls []string
	pulled := make(chan struct{})
	engine.testCallSyncToolWithResultFn = func(_ context.Context, tool string, _ map[string]interface{}) (interface{}, error) {
		mu.Lock()
		calls = append(calls, tool)
		mu.Unlock()
		if tool == "wormhole.sync.incremental_push" {
			return pushResult(1, []map[string]interface{}{acknowledge(operation, "")}), nil
		}
		close(pulled)
		return pullResult(), nil
	}
	engine.Start(context.Background())
	select {
	case <-pulled:
	case <-time.After(time.Second):
		engine.Stop()
		t.Fatal("startup pull did not run")
	}
	engine.Stop()
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(calls) != "[wormhole.sync.incremental_push wormhole.sync.incremental_pull]" {
		t.Fatalf("startup order=%v", calls)
	}
}

func TestEngineStatusReportsExactStatesAndDurablePendingCount(t *testing.T) {
	queue, audit := setupTestRepos(t)
	operation := retainedOperation(3)
	if _, err := queue.Enqueue(context.Background(), testRemoteKey(t, queue), operation, 0); err != nil {
		t.Fatal(err)
	}
	engine := mustNewEngine(t, "http://unused.invalid", queue, audit, nil, nil, DefaultConfig())
	status, err := engine.Status(context.Background())
	if err != nil || status != (Status{State: StateOffline, PendingWrites: 1}) {
		t.Fatalf("Status=(%+v,%v)", status, err)
	}
	engine.setConnectionState(StateAttentionRequired)
	status, err = engine.Status(context.Background())
	if err != nil || status.State != StateAttentionRequired || status.PendingWrites != 1 {
		t.Fatalf("attention Status=(%+v,%v)", status, err)
	}
}

func TestEngineStatusClassifiesSuccessfulAndInvalidSynchronization(t *testing.T) {
	queue, audit := setupTestRepos(t)
	engine := mustNewEngine(t, "http://unused.invalid", queue, audit, nil, nil, DefaultConfig())
	engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
		return pullResult(), nil
	}
	if err := engine.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, _ := engine.Status(context.Background())
	if status.State != StateOnline {
		t.Fatalf("successful state=%q", status.State)
	}
	engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
		return map[string]interface{}{"updates": []interface{}{}, "timestamp": "bad", "version": SyncProtocolVersion}, nil
	}
	if err := engine.syncOnce(context.Background()); err == nil {
		t.Fatal("invalid synchronization succeeded")
	}
	status, _ = engine.Status(context.Background())
	if status.State != StateAttentionRequired {
		t.Fatalf("invalid state=%q", status.State)
	}
}

func TestEngineReportsUnderlyingBackgroundSyncError(t *testing.T) {
	queue, audit := setupTestRepos(t)
	engine := mustNewEngine(t, "http://unused.invalid", queue, audit, nil, nil, DefaultConfig())
	want := errors.New("underlying sync failure")
	reported := make(chan error, 1)
	engine.syncErrorReporter = func(err error) { reported <- err }
	engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
		return nil, want
	}
	engine.Start(context.Background())
	select {
	case got := <-reported:
		if !errors.Is(got, want) {
			t.Fatalf("reported=%v", got)
		}
	case <-time.After(time.Second):
		engine.Stop()
		t.Fatal("background error not reported")
	}
	engine.Stop()
}

func TestEngineStatusRequiresAttentionForRejectedQueuedMutation(t *testing.T) {
	queue, audit := setupTestRepos(t)
	operation := retainedOperation(4)
	if _, err := queue.Enqueue(context.Background(), testRemoteKey(t, queue), operation, 0); err != nil {
		t.Fatal(err)
	}
	engine := mustNewEngine(t, "http://unused.invalid", queue, audit, nil, nil, DefaultConfig())
	engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
		return pushResult(1, []map[string]interface{}{acknowledge(operation, "rejected")}), nil
	}
	if err := engine.syncOnce(context.Background()); !errors.Is(err, ErrAttentionRequired) {
		t.Fatalf("syncOnce=%v", err)
	}
	status, _ := engine.Status(context.Background())
	if status.State != StateAttentionRequired || status.PendingWrites != 1 {
		t.Fatalf("rejected Status=%+v", status)
	}
}

func TestDefaultConfig(t *testing.T) {
	got := DefaultConfig()
	if got.BatchInterval != 5*time.Second || got.BatchSize != 50 || got.LatencyCheckInterval != 500*time.Millisecond ||
		got.PullInterval != 10*time.Second || got.HighPriorityThreshold != 2 {
		t.Fatalf("DefaultConfig=%+v", got)
	}
}

func TestCallSyncToolDelegatesToResultCall(t *testing.T) {
	queue, audit := setupTestRepos(t)
	engine := mustNewEngine(t, "http://unused.invalid", queue, audit, nil, nil, DefaultConfig())
	var got string
	engine.testCallSyncToolWithResultFn = func(_ context.Context, tool string, _ map[string]interface{}) (interface{}, error) {
		got = tool
		return map[string]interface{}{"ok": true}, nil
	}
	if err := engine.callSyncTool(context.Background(), "wormhole.sync.status", nil); err != nil || got != "wormhole.sync.status" {
		t.Fatalf("delegation=(%q,%v)", got, err)
	}
}

func TestEngineReportConflictPersistsAuthoritativeResolution(t *testing.T) {
	queue, audit := setupTestRepos(t)
	engine := mustNewEngine(t, "http://unused.invalid", queue, audit, nil, nil, DefaultConfig())
	engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
		return map[string]interface{}{"resolved_value": "server"}, nil
	}
	if err := engine.ReportConflict(context.Background(), "task", "70000000-0000-4000-8000-000000000001", "changed", "server", "local"); err != nil {
		t.Fatal(err)
	}
	entries, err := audit.ListAudit(context.Background(), testRemoteKey(t, queue), 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("audit=(%+v,%v)", entries, err)
	}
	var conflict map[string]interface{}
	if err := json.Unmarshal(entries[0].ConflictJSON, &conflict); err != nil || conflict["resolved_value"] != "server" {
		t.Fatalf("conflict=%s err=%v", entries[0].ConflictJSON, err)
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	queue, audit := setupTestRepos(t)
	cases := []func(*Config){
		func(c *Config) { c.BatchInterval = 0 },
		func(c *Config) { c.BatchSize = 0 },
		func(c *Config) { c.LatencyCheckInterval = 0 },
		func(c *Config) { c.PullInterval = 0 },
	}
	for index, mutate := range cases {
		cfg := DefaultConfig()
		mutate(&cfg)
		if _, err := New("http://unused.invalid", "token", "project", queue, audit, nil, nil, cfg); err == nil {
			t.Fatalf("invalid config %d accepted", index)
		}
	}
}

func TestEngineLifecycleConcurrentStartAndStop(t *testing.T) {
	queue, audit := setupTestRepos(t)
	cfg := DefaultConfig()
	cfg.BatchInterval, cfg.LatencyCheckInterval, cfg.PullInterval = time.Hour, time.Hour, time.Hour
	engine := mustNewEngine(t, "http://unused.invalid", queue, audit, nil, nil, cfg)
	engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
		return pullResult(), nil
	}
	var wg stdsync.WaitGroup
	for range 16 {
		wg.Add(2)
		go func() { defer wg.Done(); engine.Start(context.Background()) }()
		go func() { defer wg.Done(); engine.Stop() }()
	}
	wg.Wait()
	engine.Stop()
}

func TestEngineLifecycleCancellationDuringPush(t *testing.T) {
	queue, audit := setupTestRepos(t)
	operation := retainedOperation(5)
	if _, err := queue.Enqueue(context.Background(), testRemoteKey(t, queue), operation, 0); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.BatchInterval, cfg.LatencyCheckInterval, cfg.PullInterval = time.Hour, time.Hour, time.Hour
	engine := mustNewEngine(t, "http://unused.invalid", queue, audit, nil, nil, cfg)
	started := make(chan struct{})
	engine.testCallSyncToolWithResultFn = func(ctx context.Context, _ string, _ map[string]interface{}) (interface{}, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	engine.Start(context.Background())
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("push did not start")
	}
	engine.Stop()
	count, err := queue.PendingCount(context.Background(), testRemoteKey(t, queue))
	if err != nil || count != 1 {
		t.Fatalf("pending=(%d,%v)", count, err)
	}
}

func TestSyncLoopPullsWithEmptyQueue(t *testing.T) {
	queue, audit := setupTestRepos(t)
	cfg := DefaultConfig()
	cfg.BatchInterval, cfg.LatencyCheckInterval, cfg.PullInterval = time.Hour, time.Hour, time.Hour
	engine := mustNewEngine(t, "http://unused.invalid", queue, audit, nil, nil, cfg)
	pulled := make(chan struct{})
	engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
		close(pulled)
		return pullResult(), nil
	}
	engine.Start(context.Background())
	select {
	case <-pulled:
	case <-time.After(time.Second):
		engine.Stop()
		t.Fatal("empty queue was not pulled")
	}
	engine.Stop()
}

func TestSyncBatchTickDrainsWritesWithoutPullOrClearingAttention(t *testing.T) {
	queue, audit := setupTestRepos(t)
	operation := retainedOperation(6)
	if _, err := queue.Enqueue(context.Background(), testRemoteKey(t, queue), operation, 0); err != nil {
		t.Fatal(err)
	}
	engine := mustNewEngine(t, "http://unused.invalid", queue, audit, nil, nil, DefaultConfig())
	engine.setConnectionState(StateAttentionRequired)
	var calls []string
	engine.testCallSyncToolWithResultFn = func(_ context.Context, tool string, _ map[string]interface{}) (interface{}, error) {
		calls = append(calls, tool)
		return pushResult(1, []map[string]interface{}{acknowledge(operation, "")}), nil
	}
	engine.syncBatch(context.Background())
	status, _ := engine.Status(context.Background())
	if fmt.Sprint(calls) != "[wormhole.sync.incremental_push]" || status.State != StateAttentionRequired || status.PendingWrites != 0 {
		t.Fatalf("batch calls=%v status=%+v", calls, status)
	}
}

func TestSyncBatchTickFailureSetsStateBeforeReporting(t *testing.T) {
	queue, audit := setupTestRepos(t)
	operation := retainedOperation(7)
	if _, err := queue.Enqueue(context.Background(), testRemoteKey(t, queue), operation, 0); err != nil {
		t.Fatal(err)
	}
	engine := mustNewEngine(t, "http://unused.invalid", queue, audit, nil, nil, DefaultConfig())
	engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
		return nil, ErrFabricUnavailable
	}
	stateAtReport := StateOnline
	engine.syncErrorReporter = func(error) {
		status, _ := engine.Status(context.Background())
		stateAtReport = status.State
	}
	engine.syncBatch(context.Background())
	if stateAtReport != StateOffline {
		t.Fatalf("state at report=%q", stateAtReport)
	}
}

func TestSyncBatchTickWithPendingWriteMovesOfflineToSynchronizing(t *testing.T) {
	queue, audit := setupTestRepos(t)
	operation := retainedOperation(8)
	if _, err := queue.Enqueue(context.Background(), testRemoteKey(t, queue), operation, 0); err != nil {
		t.Fatal(err)
	}
	engine := mustNewEngine(t, "http://unused.invalid", queue, audit, nil, nil, DefaultConfig())
	engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
		return pushResult(1, []map[string]interface{}{acknowledge(operation, "")}), nil
	}
	engine.syncBatch(context.Background())
	status, _ := engine.Status(context.Background())
	if status.State != StateSynchronizing || status.PendingWrites != 0 {
		t.Fatalf("batch status=%+v", status)
	}
}

func TestEngineStartStop(t *testing.T) {
	queue, audit := setupTestRepos(t)
	cfg := DefaultConfig()
	cfg.BatchInterval, cfg.LatencyCheckInterval, cfg.PullInterval = time.Hour, time.Hour, time.Hour
	engine := mustNewEngine(t, "http://unused.invalid", queue, audit, nil, nil, cfg)
	engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
		return pullResult(), nil
	}
	engine.Start(context.Background())
	engine.Stop()
	engine.Start(context.Background())
	engine.Stop()
}

func TestEngineQueuePersistence(t *testing.T) {
	store, queue, key, _ := queueRouteFixture(t)
	path := storePath(t, store)
	operation := retainedOperation(9)
	if _, err := queue.Enqueue(context.Background(), key, operation, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := localstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	entries, err := NewQueueRepo(reopened.DB()).ListPending(context.Background(), key, 10)
	if err != nil || len(entries) != 1 || entries[0].Operation.ID != operation.ID {
		t.Fatalf("reopened entries=(%+v,%v)", entries, err)
	}
}

func TestOfflineQueueSurvivalNetworkFailure(t *testing.T) {
	queue, audit := setupTestRepos(t)
	operation := retainedOperation(10)
	if _, err := queue.Enqueue(context.Background(), testRemoteKey(t, queue), operation, 0); err != nil {
		t.Fatal(err)
	}
	engine := mustNewEngine(t, "http://127.0.0.1:1", queue, audit, nil, nil, DefaultConfig())
	if err := engine.pushBatch(context.Background()); err == nil {
		t.Fatal("unreachable Fabric push succeeded")
	}
	count, err := queue.PendingCount(context.Background(), testRemoteKey(t, queue))
	if err != nil || count != 1 {
		t.Fatalf("pending=(%d,%v)", count, err)
	}
}

func TestOfflineQueueReconnect(t *testing.T) {
	queue, audit := setupTestRepos(t)
	operation := retainedOperation(11)
	if _, err := queue.Enqueue(context.Background(), testRemoteKey(t, queue), operation, 0); err != nil {
		t.Fatal(err)
	}
	var unavailable bool = true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if unavailable {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":%q}]}}`,
			string(mustMarshalJSON(t, pushResult(1, []map[string]interface{}{acknowledge(operation, "")}))))
	}))
	defer server.Close()
	engine := mustNewEngine(t, server.URL, queue, audit, nil, nil, DefaultConfig())
	if err := engine.pushBatch(context.Background()); !errors.Is(err, ErrFabricUnavailable) {
		t.Fatalf("offline push=%v", err)
	}
	unavailable = false
	if err := engine.pushBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	count, _ := queue.PendingCount(context.Background(), testRemoteKey(t, queue))
	if count != 0 {
		t.Fatalf("pending=%d", count)
	}
}

func mustMarshalJSON(t *testing.T, value interface{}) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPushBatchPartialFailure(t *testing.T) {
	queue, audit := setupTestRepos(t)
	first, second := retainedOperation(12), retainedOperation(13)
	for _, operation := range []projectstate.OperationV1{first, second} {
		if _, err := queue.Enqueue(context.Background(), testRemoteKey(t, queue), operation, 0); err != nil {
			t.Fatal(err)
		}
	}
	engine := mustNewEngine(t, "http://unused.invalid", queue, audit, nil, nil, DefaultConfig())
	engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
		return pushResult(2, []map[string]interface{}{acknowledge(first, ""), acknowledge(second, "rejected")}), nil
	}
	if err := engine.pushBatch(context.Background()); !errors.Is(err, ErrAttentionRequired) {
		t.Fatalf("pushBatch=%v", err)
	}
	firstEntry, _ := queue.GetEntry(context.Background(), testRemoteKey(t, queue), first.ID)
	secondEntry, _ := queue.GetEntry(context.Background(), testRemoteKey(t, queue), second.ID)
	if firstEntry.DeliveredAt == nil || secondEntry.DeliveredAt != nil {
		t.Fatalf("delivery=(%v,%v)", firstEntry.DeliveredAt, secondEntry.DeliveredAt)
	}
}

func TestPushBatchAcknowledgements(t *testing.T) {
	tests := []struct {
		name    string
		result  map[string]interface{}
		wantErr bool
	}{
		{"exact", nil, false},
		{"wrong count", pushResult(2, nil), true},
		{"missing", pushResult(1, nil), true},
		{"unknown", pushResult(1, []map[string]interface{}{{"id": retainedOperation(99).ID, "type": "tombstone", "error": ""}}), true},
		{"duplicate", nil, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			queue, audit := setupTestRepos(t)
			operation := retainedOperation(14)
			if _, err := queue.Enqueue(context.Background(), testRemoteKey(t, queue), operation, 0); err != nil {
				t.Fatal(err)
			}
			result := test.result
			if result == nil {
				result = pushResult(1, []map[string]interface{}{acknowledge(operation, "")})
				if test.name == "duplicate" {
					result = pushResult(1, []map[string]interface{}{acknowledge(operation, ""), acknowledge(operation, "")})
				}
			}
			engine := mustNewEngine(t, "http://unused.invalid", queue, audit, nil, nil, DefaultConfig())
			engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
				return result, nil
			}
			err := engine.pushBatch(context.Background())
			if (err != nil) != test.wantErr {
				t.Fatalf("pushBatch=%v wantErr=%v", err, test.wantErr)
			}
		})
	}
}

func TestPushBatchRejectsMalformedToolResponses(t *testing.T) {
	cases := []interface{}{
		nil,
		map[string]interface{}{},
		map[string]interface{}{"items_received": "one", "applied": []interface{}{}, "timestamp": "2026-01-01T00:00:00Z", "version": SyncProtocolVersion},
		map[string]interface{}{"items_received": 1, "applied": []interface{}{}, "timestamp": "2026-01-01T00:00:00Z", "version": SyncProtocolVersion + 1},
	}
	for index, result := range cases {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			queue, audit := setupTestRepos(t)
			operation := retainedOperation(20 + index)
			if _, err := queue.Enqueue(context.Background(), testRemoteKey(t, queue), operation, 0); err != nil {
				t.Fatal(err)
			}
			engine := mustNewEngine(t, "http://unused.invalid", queue, audit, nil, nil, DefaultConfig())
			engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) { return result, nil }
			if err := engine.pushBatch(context.Background()); err == nil {
				t.Fatal("malformed response accepted")
			}
			count, _ := queue.PendingCount(context.Background(), testRemoteKey(t, queue))
			if count != 1 {
				t.Fatalf("pending=%d", count)
			}
		})
	}
}

func TestPushBatchDoesNotAdvancePullCursor(t *testing.T) {
	queue, audit := setupTestRepos(t)
	operation := retainedOperation(30)
	if _, err := queue.Enqueue(context.Background(), testRemoteKey(t, queue), operation, 0); err != nil {
		t.Fatal(err)
	}
	engine := mustNewEngine(t, "http://unused.invalid", queue, audit, nil, nil, DefaultConfig())
	engine.lastSyncCursor = "cursor-before"
	engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
		return pushResult(1, []map[string]interface{}{acknowledge(operation, "")}), nil
	}
	if err := engine.pushBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if engine.lastSyncCursor != "cursor-before" {
		t.Fatalf("cursor=%q", engine.lastSyncCursor)
	}
}

func TestPushBatchReturnsMarkDeliveredCancellation(t *testing.T) {
	queue, audit := setupTestRepos(t)
	operation := retainedOperation(31)
	if _, err := queue.Enqueue(context.Background(), testRemoteKey(t, queue), operation, 0); err != nil {
		t.Fatal(err)
	}
	engine := mustNewEngine(t, "http://unused.invalid", queue, audit, nil, nil, DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
		cancel()
		return pushResult(1, []map[string]interface{}{acknowledge(operation, "")}), nil
	}
	if err := engine.pushBatch(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("pushBatch=%v", err)
	}
}

func TestPushBatchPreservesEarlierDeliveryOnLaterPersistenceError(t *testing.T) {
	queue, audit := setupTestRepos(t)
	first, second := retainedOperation(32), retainedOperation(33)
	for _, operation := range []projectstate.OperationV1{first, second} {
		if _, err := queue.Enqueue(context.Background(), testRemoteKey(t, queue), operation, 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := queue.db.Exec(`CREATE TRIGGER fail_second_delivery BEFORE UPDATE OF delivered_at ON sync_queue
		WHEN OLD.id='` + second.ID + `' BEGIN SELECT RAISE(FAIL,'injected delivery failure'); END`); err != nil {
		t.Fatal(err)
	}
	engine := mustNewEngine(t, "http://unused.invalid", queue, audit, nil, nil, DefaultConfig())
	engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
		return pushResult(2, []map[string]interface{}{acknowledge(first, ""), acknowledge(second, "")}), nil
	}
	if err := engine.pushBatch(context.Background()); err == nil {
		t.Fatal("injected delivery failure was hidden")
	}
	firstEntry, _ := queue.GetEntry(context.Background(), testRemoteKey(t, queue), first.ID)
	secondEntry, _ := queue.GetEntry(context.Background(), testRemoteKey(t, queue), second.ID)
	if firstEntry.DeliveredAt == nil || secondEntry.DeliveredAt != nil {
		t.Fatalf("delivery=(%v,%v)", firstEntry.DeliveredAt, secondEntry.DeliveredAt)
	}
}
