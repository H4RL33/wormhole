// Package scheduler implements wormholed's local scheduling surface
// (RFC-0003 §6.1, design brief "Scheduling"/"Presence"). It tracks registered
// agents, their presence and capabilities, and routes tasks to matching agents
// without a Coordination Server round trip.
package scheduler

import (
	"fmt"
	"sync"
)

// AgentStatus captures the presence state an agent reports.
type AgentStatus string

const (
	StatusOnline AgentStatus = "online"
	StatusBusy   AgentStatus = "busy"
	StatusIdle   AgentStatus = "idle"
)

// RegisteredAgent is a wormholed-registered agent identity with its capabilities
// and current presence state.
type RegisteredAgent struct {
	AgentID      string
	NamespaceID  string // project/org scope
	Capabilities []string
	Status       AgentStatus
}

// ErrAgentUnknown is returned when an operation references an unregistered agent.
var ErrAgentUnknown = fmt.Errorf("scheduler: agent unknown")

// Scheduler manages agent registration, presence tracking, capability matching,
// and local task routing (RFC-0003 §6.1). It is safe for concurrent use.
//
// Task identity and workflow status are NOT owned here (Findings 1/2 of the
// P3 review): the scheduler's only job is capability matching — picking which
// locally-registered agent a given (already-persisted) task should go to. The
// task's one true ID and its RFC-0001 §8.2 status (todo/wip/blocked/done) live
// in localstore.TaskRepo; the scheduler caches just enough (ID, capability,
// AssignedTo) to do routing without a round trip.
type Scheduler struct {
	mu     sync.RWMutex
	agents map[string]*RegisteredAgent // keyed by AgentID
	tasks  []*Task
}

// NewScheduler creates a fresh scheduler instance.
func NewScheduler() *Scheduler {
	return &Scheduler{
		agents: make(map[string]*RegisteredAgent),
	}
}

// RegisterAgent records an agent with the given namespace and capabilities.
// If the agent is already registered, its capabilities are merged (union).
func (s *Scheduler) RegisterAgent(agentID, namespaceID string, capabilities []string) (*RegisteredAgent, error) {
	if agentID == "" {
		return nil, fmt.Errorf("scheduler: register: agentID must not be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.agents[agentID]
	if !ok {
		agent := &RegisteredAgent{
			AgentID:      agentID,
			NamespaceID:  namespaceID,
			Capabilities: make([]string, len(capabilities)),
			Status:       StatusOnline,
		}
		copy(agent.Capabilities, capabilities)
		s.agents[agentID] = agent
		return agent, nil
	}

	// Merge capabilities: existing gets the union.
	capSet := make(map[string]bool)
	for _, c := range existing.Capabilities {
		capSet[c] = true
	}
	for _, c := range capabilities {
		capSet[c] = true
	}
	existing.Capabilities = make([]string, 0, len(capSet))
	for c := range capSet {
		existing.Capabilities = append(existing.Capabilities, c)
	}
	return existing, nil
}

// UpdatePresence updates the presence status for agentID.
func (s *Scheduler) UpdatePresence(agentID string, status AgentStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	agent, ok := s.agents[agentID]
	if !ok {
		return ErrAgentUnknown
	}

	agent.Status = status
	return nil
}

// Task is the scheduler's routing-only view of a task: just enough to match
// capabilities and record which agent got it. It deliberately has no Status
// field — that vocabulary is RFC-0001 §8.2's todo/wip/blocked/done, owned and
// validated by localstore.TaskRepo, not invented here (Findings 1/2).
type Task struct {
	ID          string
	NamespaceID string
	Capability  string // the capability required to execute this task
	AssignedTo  string // AgentID once assigned
}

var ErrNoMatch = fmt.Errorf("scheduler: no eligible agent")

// RegisterTask records a task needing capability-based routing. taskID must
// be the caller-supplied, already-persisted task identifier (the localstore-
// generated UUID) — the scheduler does not mint its own task IDs (Finding 1).
func (s *Scheduler) RegisterTask(namespaceID, capability, taskID string) (*Task, error) {
	if namespaceID == "" {
		return nil, fmt.Errorf("scheduler: register task: namespace must not be empty")
	}
	if capability == "" {
		return nil, fmt.Errorf("scheduler: register task: capability must not be empty")
	}
	if taskID == "" {
		return nil, fmt.Errorf("scheduler: register task: taskID must not be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	task := &Task{
		ID:          taskID,
		NamespaceID: namespaceID,
		Capability:  capability,
	}
	s.tasks = append(s.tasks, task)
	return task, nil
}

// AssignTask routes a task to the first registered agent in the same namespace
// that has the required capability. Uses round-robin among eligible agents.
func (s *Scheduler) AssignTask(taskID string) (*RegisteredAgent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Find the task.
	var target *Task
	for _, t := range s.tasks {
		if t.ID == taskID {
			target = t
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("scheduler: assign: unknown task %q", taskID)
	}

	// Find eligible agents: same namespace + has capability.
	var eligible []*RegisteredAgent
	for _, agent := range s.agents {
		if agent.NamespaceID != target.NamespaceID {
			continue
		}
		if !hasCapability(agent.Capabilities, target.Capability) {
			continue
		}
		eligible = append(eligible, agent)
	}

	if len(eligible) == 0 {
		return nil, ErrNoMatch
	}

	// Pick the first eligible agent (simple assignment; round-robin is v1).
	agent := eligible[0]
	target.AssignedTo = agent.AgentID
	return agent, nil
}

func hasCapability(caps []string, required string) bool {
	for _, c := range caps {
		if c == required {
			return true
		}
	}
	return false
}

// Agent returns the registered agent for agentID, or ErrAgentUnknown.
func (s *Scheduler) Agent(agentID string) (*RegisteredAgent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	a, ok := s.agents[agentID]
	if !ok {
		return nil, ErrAgentUnknown
	}
	return a, nil
}

// ListAgents returns all registered agents.
func (s *Scheduler) ListAgents() []*RegisteredAgent {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*RegisteredAgent, 0, len(s.agents))
	for _, a := range s.agents {
		result = append(result, a)
	}
	return result
}

// AssignedAgent returns the AgentID assigned to taskID, or "" if the task is
// registered but not yet assigned. Task workflow status is not tracked here
// (Findings 1/2) — callers needing RFC-0001 §8.2 status must read it from
// localstore.TaskRepo, the source of truth.
func (s *Scheduler) AssignedAgent(taskID string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, t := range s.tasks {
		if t.ID == taskID {
			return t.AssignedTo, nil
		}
	}
	return "", fmt.Errorf("scheduler: unknown task %q", taskID)
}

// RemoveTask forgets taskID from the in-memory routing view. It is idempotent
// so callers can use it as compensation whenever a durable route transaction
// fails after scheduler registration.
func (s *Scheduler) RemoveTask(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	kept := s.tasks[:0]
	for _, task := range s.tasks {
		if task.ID != taskID {
			kept = append(kept, task)
		}
	}
	for i := len(kept); i < len(s.tasks); i++ {
		s.tasks[i] = nil
	}
	s.tasks = kept
}

// TaskCount reports the number of tasks held in the routing view.
func (s *Scheduler) TaskCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.tasks)
}
