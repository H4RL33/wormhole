// P3 integration tests: two agents on same machine see each other's presence
// and have a task routed between them without a Coordination Server round trip.

package localapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/eventbus"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/runtime/scheduler"
)

// dialLocalSocket dials socketPath with retry. Transport-agnostic (design
// doc §3): unchanged by the P1 bespoke-protocol -> MCP JSON-RPC migration.
func dialLocalSocket(t *testing.T, socketPath string) net.Conn {
	t.Helper()
	var conn net.Conn
	for i := 0; i < 50; i++ {
		conn, _ = net.Dial("unix", socketPath)
		if conn != nil {
			return conn
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("could not dial socket")
	return nil
}

// sendRequest performs the initialize -> notifications/initialized
// handshake on a fresh connection to socketPath, then issues one
// "tools/call", and returns the decoded result. Each call gets its own
// connection (matching this suite's original one-call-per-connection test
// style), unlike a real MCP client which would reuse one persistent
// connection for many calls.
func sendRequest(t *testing.T, socketPath string, tool string, args map[string]interface{}) mcpToolResponse {
	t.Helper()
	conn := dialLocalSocket(t, socketPath)
	defer conn.Close()
	reader := bufio.NewReader(conn)
	mcpInitialize(t, conn, reader)
	return mcpCallTool(t, conn, reader, 2, tool, args)
}

// TestTwoAgentsPresenceWithoutCoordinationServer proves two agents on the same
// machine see each other's presence without contacting the Coordination Server.
func TestTwoAgentsPresenceWithoutCoordinationServer(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	bus := eventbus.NewEventBus()
	sched := scheduler.NewScheduler()

	socketPath := filepath.Join(t.TempDir(), "p3.sock")
	er := localstore.NewEventRepo(store.DB())
	srv, err := NewWithRuntime(socketPath, coord.URL, "test-token", "project-1",
		store, localstore.NewTaskRepo(store.DB(), er), er,
		localstore.NewKBRepo(store.DB()), bus, sched, nil)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	conn := dialLocalSocket(t, socketPath)
	defer conn.Close()

	// Agent-a registers.
	respA := sendRequest(t, socketPath, "wormhole.agent.register", map[string]interface{}{
		"agent_id":     "agent-a",
		"capabilities": []string{"code", "review"},
	})
	if respA.Error != "" {
		t.Fatalf("agent-a register: %s", respA.Error)
	}

	// Agent-b registers.
	respB := sendRequest(t, socketPath, "wormhole.agent.register", map[string]interface{}{
		"agent_id":     "agent-b",
		"capabilities": []string{"code"},
	})
	if respB.Error != "" {
		t.Fatalf("agent-b register: %s", respB.Error)
	}

	// List agents should show both.
	respList := sendRequest(t, socketPath, "wormhole.agent.list", nil)
	if respList.Error != "" {
		t.Fatalf("list agents: %s", respList.Error)
	}
	if len(respList.Result) == 0 && respList.Error == "" {
		// writeResponse may send neither result nor error on marshal failure.
		// Return a debug response so we can see what actually arrived.
		t.Fatalf("list agents: empty response (no Result, no Error)")
	}
	var listResult map[string]interface{}
	if err := json.Unmarshal(respList.Result, &listResult); err != nil {
		t.Fatalf("unmarshal list result: %v (raw: %s)", err, string(respList.Result))
	}
	agentsRaw, ok := listResult["agents"]
	if !ok || agentsRaw == nil {
		t.Fatalf("list result missing 'agents' key or nil; full result: %s", string(respList.Result))
	}
	agentsArr := agentsRaw.([]interface{})
	if len(agentsArr) != 2 {
		t.Fatalf("listed %d agents, want 2 for two-agent scenario", len(agentsArr))
	}

	// Agent-a updates presence to busy.
	respPresence := sendRequest(t, socketPath, "wormhole.agent.presence", map[string]interface{}{
		"agent_id": "agent-a",
		"status":   "busy",
	})
	if respPresence.Error != "" {
		t.Fatalf("presence update: %s", respPresence.Error)
	}

	// Verify agent-a is now busy via list.
	respList2 := sendRequest(t, socketPath, "wormhole.agent.list", nil)
	var listResult2 map[string]interface{}
	json.Unmarshal(respList2.Result, &listResult2)
	agentsArr2 := listResult2["agents"].([]interface{})

	// Find agent-a in the list and verify status.
	for _, a := range agentsArr2 {
		agent := a.(map[string]interface{})
		if agent["agent_id"] == "agent-a" {
			if agent["status"] != "busy" {
				t.Errorf("agent-a status = %s, want busy", agent["status"])
			}
			break
		}
	}

	// Verify scheduler internal state.
	a, err := sched.Agent("agent-a")
	if err != nil {
		t.Fatalf("scheduler Agent(a): %v", err)
	}
	if a.Status != scheduler.StatusBusy {
		t.Errorf("scheduler status = %s, want busy", a.Status)
	}
}

// TestTaskRoutedWithoutCoordinationServer proves a task can be created and routed
// to a locally-registered agent without any Coordination Server call.
func TestTaskRoutedWithoutCoordinationServer(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	if err := store.CacheWhoAmI(context.Background(), localstore.WhoAmICache{AgentID: "task-agent", ProjectID: "project-1", Permissions: []string{"task.create"}, CachedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("cache task-route scope: %v", err)
	}

	bus := eventbus.NewEventBus()
	sched := scheduler.NewScheduler()

	// No fake coordination server — this test must work entirely without one.
	socketPath := filepath.Join(t.TempDir(), "p3-task.sock")
	er := localstore.NewEventRepo(store.DB())
	srv, err := NewWithRuntime(socketPath, "", "", "project-1",
		store, localstore.NewTaskRepo(store.DB(), er), er,
		localstore.NewKBRepo(store.DB()), bus, sched, nil)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	conn := dialLocalSocket(t, socketPath)
	defer conn.Close()

	// Register an agent with "code" capability.
	sendRequest(t, socketPath, "wormhole.agent.register", map[string]interface{}{
		"agent_id":     "task-agent",
		"capabilities": []string{"code"},
	})

	// Route a task requiring "code" capability.
	resp := sendRequest(t, socketPath, "wormhole.task.route", map[string]interface{}{
		"capability":  "code",
		"title":       "implement feature x",
		"description": "do the thing",
	})
	if resp.Error != "" {
		t.Fatalf("task route: %s", resp.Error)
	}

	var taskResult map[string]interface{}
	json.Unmarshal(resp.Result, &taskResult)

	taskID, _ := taskResult["task_id"].(string)
	if taskID == "" {
		t.Fatal("task route returned no task_id")
	}

	assignedTo, _ := taskResult["assigned_to"].(string)
	if assignedTo == "" {
		t.Fatal("task route returned empty assigned_to — no agent matched")
	}

	// Assignment is an ownership change, not a status transition (Findings
	// 1/2): a freshly routed task stays at RFC-0001 §8.2's initial "todo"
	// status until an explicit status transition moves it.
	status, _ := taskResult["status"].(string)
	if status != "todo" {
		t.Errorf("task status = %s, want todo", status)
	}

	// Verify the scheduler recorded the assignment.
	schedAssignedTo, err := sched.AssignedAgent(taskID)
	if err != nil {
		t.Fatalf("scheduler AssignedAgent: %v", err)
	}
	if schedAssignedTo != assignedTo {
		t.Errorf("scheduler AssignedAgent = %s, want %s", schedAssignedTo, assignedTo)
	}

	// Verify the task is retrievable via wormhole.task.get using the same ID
	// (Finding 1: the response task_id must be the localstore-generated ID).
	respGet := sendRequest(t, socketPath, "wormhole.task.get", map[string]interface{}{
		"task_id": taskID,
	})
	if respGet.Error != "" {
		t.Fatalf("task get: %s", respGet.Error)
	}
	var getResult map[string]interface{}
	json.Unmarshal(respGet.Result, &getResult)
	if getResult["owner_agent_id"] != assignedTo {
		t.Errorf("task.get owner_agent_id = %v, want %s", getResult["owner_agent_id"], assignedTo)
	}

	// Verify the task is assigned to the correct agent.
	agent, err := sched.Agent(assignedTo)
	if err != nil {
		t.Fatalf("scheduler Agent(%s): %v", assignedTo, err)
	}
	if agent.Capabilities == nil {
		t.Fatal("assigned agent has no capabilities")
	}
	hasCode := false
	for _, c := range agent.Capabilities {
		if c == "code" {
			hasCode = true
			break
		}
	}
	if !hasCode {
		t.Errorf("assigned agent %s missing 'code' capability", assignedTo)
	}
}

// TestSubscriptionDeliversEvents proves an eventbus subscription delivers
// ephemeral events (presence signals) across the localapi.
func TestSubscriptionDeliversEvents(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	bus := eventbus.NewEventBus()
	sched := scheduler.NewScheduler()

	socketPath := filepath.Join(t.TempDir(), "p3-sub.sock")
	er := localstore.NewEventRepo(store.DB())
	srv, err := NewWithRuntime(socketPath, "", "", "project-1",
		store, localstore.NewTaskRepo(store.DB(), er), er,
		localstore.NewKBRepo(store.DB()), bus, sched, nil)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	// Register two agents (using sendRequest for one-shot calls).
	sendRequest(t, socketPath, "wormhole.agent.register", map[string]interface{}{
		"agent_id":     "agent-x",
		"capabilities": []string{"code"},
	})
	sendRequest(t, socketPath, "wormhole.agent.register", map[string]interface{}{
		"agent_id":     "agent-y",
		"capabilities": []string{"review"},
	})

	// Open a persistent connection for subscription that will keep listening
	// for notifications/wormhole.event messages after the subscribe ack.
	subConn := dialLocalSocket(t, socketPath)
	defer subConn.Close()
	subReader := bufio.NewReader(subConn)
	mcpInitialize(t, subConn, subReader)

	// tools/call wormhole.channel.subscribe: the ack comes back as a normal
	// tools/call result; event delivery afterward is
	// notifications/wormhole.event messages on this same connection (design
	// doc §1's resolved subscription-delivery question).
	subResp := mcpCallTool(t, subConn, subReader, 2, "wormhole.channel.subscribe", map[string]interface{}{"namespace": "project-1"})
	if subResp.Error != "" {
		t.Fatalf("subscribe error: %s", subResp.Error)
	}

	var subResult map[string]interface{}
	json.Unmarshal(subResp.Result, &subResult)
	subID, _ := subResult["subscription_id"].(string)
	if subID == "" {
		t.Fatal("subscribe returned no subscription_id")
	}

	// Give the subscription time to be registered in the eventbus.
	time.Sleep(50 * time.Millisecond)

	// Now publish a presence event via a fresh connection request.
	sendRequest(t, socketPath, "wormhole.agent.presence", map[string]interface{}{
		"agent_id": "agent-y",
		"status":   "busy",
	})

	// Give the event time to propagate through the eventbus → delivery goroutine → socket.
	time.Sleep(50 * time.Millisecond)

	// Read the delivered notification from subConn.
	subConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	line, err := subReader.ReadBytes('\n')
	subConn.SetReadDeadline(time.Time{}) // clear deadline
	if err != nil {
		t.Fatalf("no event delivered over subscription: %v", err)
	}
	var note rpcRequest
	if err := json.Unmarshal(bytes.TrimSpace(line), &note); err != nil {
		t.Fatalf("decode notification: %v", err)
	}
	if note.Method != "notifications/wormhole.event" {
		t.Fatalf("notification method = %q, want notifications/wormhole.event", note.Method)
	}

	// Verify the payload contains the agent-y presence data.
	var result map[string]interface{}
	json.Unmarshal(note.Params, &result)
	if agent, ok := result["agent"].(string); !ok || agent != "agent-y" {
		t.Errorf("subscription event agent = %v, want agent-y", result["agent"])
	}

	// The subscription ID should match what was returned.
	if subID != "sub-0" {
		t.Fatalf("subscription_id = %s, want sub-0 (first subscribe)", subID)
	}
}
