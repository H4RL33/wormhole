// p7_e2e_integration_test.go exercises the retained legacy sync engine and
// queue mechanics against test HTTP peers. These are subsystem tests, not the
// local-only Stage 2 Gateway process topology.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"strings"
	stdsync "sync"
	"syscall"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/runtime/sync"
	"github.com/H4RL33/wormhole/internal/types"
)

// TestP7_LocalQueueDeliveryLifecycle verifies only the SQLite queue state
// transitions used by the retained sync engine. Network delivery is exercised
// separately by sync package tests and the Fabric HTTP acceptance test.
func TestP7_LocalQueueDeliveryLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "wormhole.db")
	const projectID = "project-1"

	// Open local store
	store, err := localstore.Open(dbPath)
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	queueRepo := sync.NewQueueRepo(store.DB())

	// Step 1: Verify queue is empty initially
	queuedItems, err := queueRepo.ListPending(ctx, projectID, 100)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(queuedItems) != 0 {
		t.Fatalf("queue not empty initially: %d items", len(queuedItems))
	}

	// Step 2: Enqueue a task creation event (simulating what localapi.handleTaskCreate would do)
	taskPayload := map[string]interface{}{
		"id":          "task-p7-001",
		"title":       "P7 test task",
		"description": "Created during offline mode",
		"status":      "todo",
		"priority":    1,
	}
	taskPayloadRaw, _ := json.Marshal(taskPayload)

	queued, err := queueRepo.Enqueue(ctx, projectID, "task", "task-p7-001", "create", taskPayloadRaw, 1)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if queued.ID == "" {
		t.Fatalf("Enqueue returned empty ID")
	}

	// Step 3: Verify task is queued
	queuedItems, err = queueRepo.ListPending(ctx, projectID, 100)
	if err != nil {
		t.Fatalf("ListPending after enqueue: %v", err)
	}
	if len(queuedItems) != 1 {
		t.Fatalf("queue should have 1 item, got %d", len(queuedItems))
	}
	if queuedItems[0].EntityID != "task-p7-001" {
		t.Fatalf("queued task ID mismatch: got %s, want task-p7-001", queuedItems[0].EntityID)
	}

	// Step 4: Mark item as delivered, as the sync engine does after a successful
	// remote acknowledgement.
	if err := queueRepo.MarkDelivered(ctx, projectID, queuedItems[0].ID); err != nil {
		t.Fatalf("MarkDelivered: %v", err)
	}

	// Step 5: Verify item was marked delivered.
	queuedItems, err = queueRepo.ListPending(ctx, projectID, 100)
	if err != nil {
		t.Fatalf("ListPending after sync: %v", err)
	}
	if len(queuedItems) != 0 {
		t.Fatalf("queue should be empty after marking delivered, got %d items", len(queuedItems))
	}

}

// TestP7_LocalTaskPersistence verifies that task writes to localstore survive restarts.
func TestP7_LocalTaskPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "wormhole.db")
	ctx := context.Background()

	// First session: create a task
	{
		store, err := localstore.Open(dbPath)
		if err != nil {
			t.Fatalf("Open 1: %v", err)
		}

		taskRepo := localstore.NewTaskRepo(store.DB(), localstore.NewEventRepo(store.DB()))
		task, err := taskRepo.CreateTask(ctx, "project-1", "Task title", "Task description", nil, 1, nil)
		if err != nil {
			t.Fatalf("CreateTask 1: %v", err)
		}
		if task.ID == "" {
			t.Fatalf("CreateTask returned empty ID")
		}
		taskID := task.ID

		store.Close()

		// Second session: verify task persists
		store2, err := localstore.Open(dbPath)
		if err != nil {
			t.Fatalf("Open 2: %v", err)
		}
		defer store2.Close()

		taskRepo2 := localstore.NewTaskRepo(store2.DB(), localstore.NewEventRepo(store2.DB()))
		retrieved, err := taskRepo2.GetTask(ctx, "project-1", taskID)
		if err != nil {
			t.Fatalf("GetTask 2: %v", err)
		}
		if retrieved.ID != taskID {
			t.Fatalf("retrieved task ID mismatch: got %s, want %s", retrieved.ID, taskID)
		}
		if retrieved.Title != "Task title" {
			t.Fatalf("retrieved task title mismatch: got %s, want Task title", retrieved.Title)
		}
	}
}

// TestP7_SyncQueueDurability verifies that sync queue entries survive server restarts.
func TestP7_SyncQueueDurability(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "wormhole.db")
	ctx := context.Background()

	taskPayload := map[string]interface{}{
		"title":       "Test task",
		"description": "Test description",
	}
	taskPayloadRaw, _ := json.Marshal(taskPayload)

	// First session: enqueue an item
	{
		store, err := localstore.Open(dbPath)
		if err != nil {
			t.Fatalf("Open 1: %v", err)
		}

		queueRepo := sync.NewQueueRepo(store.DB())
		queued, err := queueRepo.Enqueue(ctx, "project-1", "task", "task-123", "create", taskPayloadRaw, 1)
		if err != nil {
			t.Fatalf("Enqueue 1: %v", err)
		}
		queueID := queued.ID

		store.Close()

		// Second session: verify queue entry persists
		store2, err := localstore.Open(dbPath)
		if err != nil {
			t.Fatalf("Open 2: %v", err)
		}
		defer store2.Close()

		queueRepo2 := sync.NewQueueRepo(store2.DB())
		pending, err := queueRepo2.ListPending(ctx, "project-1", 100)
		if err != nil {
			t.Fatalf("ListPending 2: %v", err)
		}
		if len(pending) != 1 {
			t.Fatalf("queue should have 1 item, got %d", len(pending))
		}
		if pending[0].ID != queueID {
			t.Fatalf("queue ID mismatch: got %s, want %s", pending[0].ID, queueID)
		}
		if pending[0].EntityID != "task-123" {
			t.Fatalf("entity ID mismatch: got %s, want task-123", pending[0].EntityID)
		}
	}
}

// statefulCoordServer is a fake HTTP sync peer that retains pushed tasks in
// memory, so a second sync runtime's Bootstrap/PullIncremental can observe what
// the first pushed. It lets TestP7_MultiRuntimeSync prove the second runtime's
// SQLite replica — not the peer — ends up with the task.
func statefulCoordServer(t *testing.T) *httptest.Server {
	t.Helper()
	type serverTask struct {
		TaskID      string `json:"task_id"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Status      string `json:"status"`
		Priority    int    `json:"priority"`
	}
	var mu stdsync.Mutex
	tasks := map[string]serverTask{}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		var resultData interface{}
		switch params.Name {
		case "wormhole.sync.incremental_push":
			var pushArgs struct {
				Items []struct {
					EntityType string          `json:"entity_type"`
					EntityID   string          `json:"entity_id"`
					Operation  string          `json:"operation"`
					Payload    json.RawMessage `json:"payload"`
				} `json:"items"`
			}
			if err := json.Unmarshal(params.Arguments, &pushArgs); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mu.Lock()
			for _, item := range pushArgs.Items {
				if item.EntityType != "task" {
					continue
				}
				var payload struct {
					Title       string `json:"title"`
					Description string `json:"description"`
				}
				_ = json.Unmarshal(item.Payload, &payload)
				tasks[item.EntityID] = serverTask{
					TaskID:      item.EntityID,
					Title:       payload.Title,
					Description: payload.Description,
					Status:      "todo",
					Priority:    1,
				}
			}
			mu.Unlock()
			resultData = map[string]interface{}{
				"items_received": len(pushArgs.Items),
				"applied": func() []map[string]interface{} {
					applied := make([]map[string]interface{}, 0, len(pushArgs.Items))
					for _, item := range pushArgs.Items {
						applied = append(applied, map[string]interface{}{"id": item.EntityID, "type": item.EntityType, "error": ""})
					}
					return applied
				}(),
				"timestamp": time.Now().UTC().Format(time.RFC3339),
				"version":   1,
			}

		case "wormhole.sync.bootstrap":
			mu.Lock()
			taskList := make([]types.BootstrapTaskV1, 0, len(tasks))
			now := time.Now().UTC()
			for _, task := range tasks {
				taskList = append(taskList, types.BootstrapTaskV1{ID: task.TaskID, ProjectID: "project-1", Title: task.Title,
					Description: task.Description, Status: task.Status, Priority: task.Priority, CreatedAt: now, UpdatedAt: now})
			}
			mu.Unlock()
			result := gatewayTestBootstrapOutput("project-1", "test-agent", "test-passport")
			org := result["org_config"].(types.BootstrapOrgConfigV1)
			org.Tasks = taskList
			result["org_config"] = org
			result["task_list"] = taskList
			resultData = result

		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}

		resultRaw, _ := json.Marshal(resultData)
		toolResult := map[string]interface{}{
			"content": []map[string]string{{"type": "text", "text": string(resultRaw)}},
		}
		toolResultRaw, _ := json.Marshal(toolResult)
		resp := map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": json.RawMessage(toolResultRaw)}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
}

// TestP7_MultiRuntimeSync composes two local stores and sync engines against
// one fake HTTP peer. Runtime A writes and pushes a task; runtime B calls
// Bootstrap and must end up with that task in its own SQLite replica. This exercises
// internal/runtime/sync.Engine's local-apply path (sync.go's applyTask,
// wired through TaskRepo.UpsertTask) added to close the gap this test used
// to be skipped for — see internal/runtime/sync/sync_apply_test.go for the
// focused unit coverage of that path.
func TestP7_MultiRuntimeSync(t *testing.T) {
	coordSrv := statefulCoordServer(t)
	defer coordSrv.Close()

	ctx := context.Background()
	tmpDir := t.TempDir()

	// Runtime A: writes and pushes a task.
	storeA, err := localstore.Open(filepath.Join(tmpDir, "a.db"))
	if err != nil {
		t.Fatalf("open store A: %v", err)
	}
	defer storeA.Close()
	queueA := sync.NewQueueRepo(storeA.DB())
	auditA := sync.NewAuditRepo(storeA.DB())
	taskRepoA := localstore.NewTaskRepo(storeA.DB(), localstore.NewEventRepo(storeA.DB()))
	kbRepoA := localstore.NewKBRepo(storeA.DB())
	fastCfg := sync.DefaultConfig()
	fastCfg.BatchInterval = 20 * time.Millisecond
	engineA, err := sync.New(coordSrv.URL, "test-token", "project-1", queueA, auditA, taskRepoA, kbRepoA, fastCfg)
	if err != nil {
		t.Fatalf("New engine A: %v", err)
	}

	task, err := taskRepoA.CreateTask(ctx, "project-1", "Daemon A task", "written offline", nil, 1, nil)
	if err != nil {
		t.Fatalf("CreateTask on runtime A: %v", err)
	}
	payload, _ := json.Marshal(map[string]interface{}{"title": task.Title, "description": task.Description})
	if _, err := queueA.Enqueue(ctx, "project-1", "task", task.ID, "create", payload, 0); err != nil {
		t.Fatalf("Enqueue on runtime A: %v", err)
	}

	// pushBatch is unexported (called only from Engine's own background
	// loop), so drive the push via Start/Stop like the queue-durability
	// tests above do, and poll until the queue drains rather than assume a
	// fixed sleep is long enough.
	syncCtx, syncCancel := context.WithCancel(ctx)
	engineA.Start(syncCtx)
	deadline := time.Now().Add(5 * time.Second)
	for {
		pending, err := queueA.ListPending(ctx, "project-1", 10)
		if err != nil {
			t.Fatalf("ListPending on runtime A: %v", err)
		}
		if len(pending) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("runtime A push did not drain queue within deadline")
		}
		time.Sleep(10 * time.Millisecond)
	}
	syncCancel()
	engineA.Stop()

	// Runtime B never saw runtime A's write locally. Bootstrap must pull it
	// from the shared fake peer and land it in runtime B's
	// own SQLite replica.
	storeB, err := localstore.Open(filepath.Join(tmpDir, "b.db"))
	if err != nil {
		t.Fatalf("open store B: %v", err)
	}
	defer storeB.Close()
	queueB := sync.NewQueueRepo(storeB.DB())
	auditB := sync.NewAuditRepo(storeB.DB())
	taskRepoB := localstore.NewTaskRepo(storeB.DB(), localstore.NewEventRepo(storeB.DB()))
	kbRepoB := localstore.NewKBRepo(storeB.DB())
	engineB, err := sync.New(coordSrv.URL, "test-token", "project-1", queueB, auditB, taskRepoB, kbRepoB, sync.DefaultConfig())
	if err != nil {
		t.Fatalf("New engine B: %v", err)
	}
	if err := engineB.ConfigureBootstrap(storeB, "test-agent", "test-passport", nil); err != nil {
		t.Fatalf("ConfigureBootstrap engine B: %v", err)
	}

	if err := engineB.Bootstrap(ctx); err != nil {
		t.Fatalf("Bootstrap on runtime B: %v", err)
	}

	gotOnB, err := taskRepoB.GetTask(ctx, "project-1", task.ID)
	if err != nil {
		t.Fatalf("runtime B did not receive runtime A's task via Bootstrap: %v", err)
	}
	if gotOnB.Title != "Daemon A task" {
		t.Errorf("runtime B task title = %q, want %q", gotOnB.Title, "Daemon A task")
	}
}

var (
	task4GatewayBinOnce stdsync.Once
	task4GatewayBinPath string
	task4GatewayBinErr  error
)

func TestTask4FabricBuildUsesTestOnlyEmbedderWiring(t *testing.T) {
	got := task4FabricBuildArgs("/tmp/fabric-test")
	want := []string{"build", "-tags", "wormhole_test_embedder", "-o", "/tmp/fabric-test", "./cmd/fabric"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Fabric build args = %q, want %q", got, want)
	}
	for _, argument := range got {
		if argument == "-ldflags" || strings.Contains(argument, "cohereEmbedEndpoint") {
			t.Fatalf("Fabric build mutates production endpoint: %q", got)
		}
	}
}

func task4BuildGatewayBinary(t *testing.T) string {
	t.Helper()
	task4GatewayBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "wormhole-gatewayd-task4-*")
		if err != nil {
			task4GatewayBinErr = err
			return
		}
		task4GatewayBinPath = filepath.Join(dir, "gatewayd")
		command := exec.Command("go", "build", "-o", task4GatewayBinPath, "./cmd/gatewayd")
		command.Dir = repoRootForTest(t)
		if currentUser, err := user.Current(); err == nil && currentUser.HomeDir != "" {
			// Earlier legacy tests in this package mutate HOME without restoring
			// it. Keep the Go tool's module-cache resolution independent of test
			// ordering while leaving the spawned Gateway's isolated HOME intact.
			command.Env = append(os.Environ(), "HOME="+currentUser.HomeDir)
		}
		if output, err := command.CombinedOutput(); err != nil {
			task4GatewayBinErr = errors.New("build gatewayd: " + err.Error() + ": " + string(output))
		}
	})
	return task4GatewayBinPath
}

func task4FabricBuildArgs(outputPath string) []string {
	return []string{"build", "-tags", "wormhole_test_embedder", "-o", outputPath, "./cmd/fabric"}
}

type task4ProcessDaemon struct {
	command *exec.Cmd
	done    chan error
	stderr  bytes.Buffer
	stop    stdsync.Once
}

func startTask4ProcessDaemon(t *testing.T, gatewayBin, profile string, env []string, socketPath string) *task4ProcessDaemon {
	t.Helper()
	daemon := &task4ProcessDaemon{done: make(chan error, 1)}
	daemon.command = exec.Command(gatewayBin, profile)
	daemon.command.Env = env
	daemon.command.Stderr = &daemon.stderr
	if err := daemon.command.Start(); err != nil {
		t.Fatalf("start gatewayd process: %v", err)
	}
	go func() { daemon.done <- daemon.command.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Lstat(socketPath); err == nil {
			break
		}
		select {
		case err := <-daemon.done:
			t.Fatalf("gatewayd exited before socket was ready: %v stderr=%q", err, daemon.stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("gatewayd socket was not ready: stderr=%q", daemon.stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { daemon.Stop(t) })
	return daemon
}

func (daemon *task4ProcessDaemon) Stop(t *testing.T) {
	t.Helper()
	daemon.stop.Do(func() {
		_ = daemon.command.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-daemon.done:
			var exitError *exec.ExitError
			if err != nil && (!errors.As(err, &exitError) || !exitError.ProcessState.Sys().(syscall.WaitStatus).Signaled()) {
				t.Errorf("gatewayd process exit: %v stderr=%q", err, daemon.stderr.String())
			}
		case <-time.After(5 * time.Second):
			_ = daemon.command.Process.Kill()
			t.Errorf("gatewayd process did not stop: stderr=%q", daemon.stderr.String())
		}
	})
}
