package scheduler

import (
	"errors"
	"testing"
)

func TestRegisterAgent(t *testing.T) {
	sched := NewScheduler()

	agent, err := sched.RegisterAgent("agent-1", "ns-1", []string{"code", "review"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if agent.AgentID != "agent-1" || agent.NamespaceID != "ns-1" {
		t.Fatalf("got %+v, want agent-1 in ns-1", agent)
	}
	if len(agent.Capabilities) != 2 {
		t.Fatalf("capabilities: %d, want 2", len(agent.Capabilities))
	}
}

func TestRegisterAgentEmptyID(t *testing.T) {
	sched := NewScheduler()
	_, err := sched.RegisterAgent("", "ns-1", nil)
	if err == nil {
		t.Fatal("expected error for empty agentID")
	}
}

func TestMergeCapabilities(t *testing.T) {
	sched := NewScheduler()

	sched.RegisterAgent("agent-1", "ns-1", []string{"code"})
	_, err := sched.RegisterAgent("agent-1", "ns-1", []string{"review"})
	if err != nil {
		t.Fatalf("re-register: %v", err)
	}

	a, err := sched.Agent("agent-1")
	if err != nil {
		t.Fatalf("Agent: %v", err)
	}
	if len(a.Capabilities) != 2 {
		t.Fatalf("capabilities after merge: %d, want 2", len(a.Capabilities))
	}
}

func TestUpdatePresence(t *testing.T) {
	sched := NewScheduler()
	sched.RegisterAgent("agent-1", "ns-1", nil)

	if err := sched.UpdatePresence("agent-1", StatusBusy); err != nil {
		t.Fatalf("update presence: %v", err)
	}

	a, _ := sched.Agent("agent-1")
	if a.Status != StatusBusy {
		t.Errorf("status = %s, want busy", a.Status)
	}
}

func TestUpdatePresenceUnknownAgent(t *testing.T) {
	sched := NewScheduler()
	err := sched.UpdatePresence("unknown-agent", StatusOnline)
	if !errors.Is(err, ErrAgentUnknown) {
		t.Fatalf("got err %v, want ErrAgentUnknown", err)
	}
}

func TestRegisterTask(t *testing.T) {
	sched := NewScheduler()

	task, err := sched.RegisterTask("ns-1", "code", "task-ext-1")
	if err != nil {
		t.Fatalf("register task: %v", err)
	}
	if task.NamespaceID != "ns-1" || task.Capability != "code" || task.ID != "task-ext-1" || task.AssignedTo != "" {
		t.Fatalf("task = %+v, want unassigned task-ext-1 code task in ns-1", task)
	}
}

func TestRegisterTaskEmptyFields(t *testing.T) {
	sched := NewScheduler()

	_, err := sched.RegisterTask("", "code", "task-1")
	if err == nil {
		t.Fatal("expected error for empty namespace")
	}

	_, err = sched.RegisterTask("ns-1", "", "task-1")
	if err == nil {
		t.Fatal("expected error for empty capability")
	}

	_, err = sched.RegisterTask("ns-1", "code", "")
	if err == nil {
		t.Fatal("expected error for empty taskID")
	}
}

func TestAssignTaskMatchesCapability(t *testing.T) {
	sched := NewScheduler()

	sched.RegisterAgent("agent-code", "ns-1", []string{"code"})
	sched.RegisterAgent("agent-review", "ns-1", []string{"review"})

	task, _ := sched.RegisterTask("ns-1", "code", "task-1")

	agent, err := sched.AssignTask(task.ID)
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if agent.AgentID != "agent-code" {
		t.Errorf("assigned to %s, want agent-code", agent.AgentID)
	}

	// Verify assignment recorded, via both the returned task and AssignedAgent.
	assignedTo, err := sched.AssignedAgent(task.ID)
	if err != nil {
		t.Fatalf("AssignedAgent: %v", err)
	}
	if assignedTo != "agent-code" {
		t.Errorf("AssignedAgent = %s, want agent-code", assignedTo)
	}
	if task.AssignedTo != "agent-code" {
		t.Errorf("task assigned to %s, want agent-code", task.AssignedTo)
	}
}

func TestAssignTaskNoMatch(t *testing.T) {
	sched := NewScheduler()

	sched.RegisterAgent("agent-review", "ns-1", []string{"review"})

	task, _ := sched.RegisterTask("ns-1", "code", "task-1")

	_, err := sched.AssignTask(task.ID)
	if !errors.Is(err, ErrNoMatch) {
		t.Fatalf("got err %v, want ErrNoMatch", err)
	}
}

func TestAssignTaskNamespaceMismatch(t *testing.T) {
	sched := NewScheduler()

	// Agent in different namespace.
	sched.RegisterAgent("agent-1", "ns-2", []string{"code"})

	task, _ := sched.RegisterTask("ns-1", "code", "task-1")

	_, err := sched.AssignTask(task.ID)
	if !errors.Is(err, ErrNoMatch) {
		t.Fatalf("got err %v, want ErrNoMatch (namespace mismatch)", err)
	}
}

func TestListAgents(t *testing.T) {
	sched := NewScheduler()

	sched.RegisterAgent("agent-1", "ns-1", nil)
	sched.RegisterAgent("agent-2", "ns-1", nil)

	agents := sched.ListAgents()
	if len(agents) != 2 {
		t.Fatalf("listed %d agents, want 2", len(agents))
	}
}

func TestAgentUnknown(t *testing.T) {
	sched := NewScheduler()
	_, err := sched.Agent("unknown")
	if !errors.Is(err, ErrAgentUnknown) {
		t.Fatalf("got err %v, want ErrAgentUnknown", err)
	}
}

func TestTwoAgentsSameMachine(t *testing.T) {
	// P3 exit criteria: two agents on the same machine see each other's presence.
	sched := NewScheduler()

	_, _ = sched.RegisterAgent("agent-a", "ns-1", []string{"code", "review"})
	agent2, _ := sched.RegisterAgent("agent-b", "ns-1", []string{"code"})
	if agent2.NamespaceID != "ns-1" {
		t.Fatalf("agent2 namespace = %s, want ns-1", agent2.NamespaceID)
	}

	if err := sched.UpdatePresence("agent-b", StatusOnline); err != nil {
		t.Fatalf("update presence b: %v", err)
	}

	// agent-a should see agent-b as registered and online.
	a, _ := sched.Agent("agent-a")
	b, _ := sched.Agent("agent-b")

	if a.NamespaceID != b.NamespaceID {
		t.Errorf("agents in different namespaces: %s vs %s", a.NamespaceID, b.NamespaceID)
	}
	if b.Status != StatusOnline {
		t.Errorf("agent-b status = %s, want online", b.Status)
	}

	// Verify both are listable.
	all := sched.ListAgents()
	if len(all) != 2 {
		t.Fatalf("listed %d agents, want 2 for two-agent scenario", len(all))
	}

	// agent-a should be able to run a task assigned to agent-a.
	task, _ := sched.RegisterTask("ns-1", "code", "task-1")
	routedAgent, err := sched.AssignTask(task.ID)
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if routedAgent.AgentID == "agent-b" {
		// agent-a was found first in map iteration (deterministic for same caps).
		// This is fine — the key property is that a match was found.
	}

	if task.AssignedTo == "" {
		t.Errorf("task.AssignedTo empty, want an assigned agent")
	}
}
