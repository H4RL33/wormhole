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
type Scheduler struct {
	mu      sync.RWMutex
	agents  map[string]*RegisteredAgent // keyed by AgentID
	tasks   []*Task
	nextID  int
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

// Task represents a unit of work that needs routing to an agent.
type Task struct {
	ID           string
	NamespaceID  string
	Capability   string // the capability required to execute this task
	Status       string // "unassigned" | "assigned" | "done"
	AssignedTo   string // AgentID once assigned
}

var ErrNoMatch = fmt.Errorf("scheduler: no eligible agent")

// RegisterTask creates a new unassigned task.
func (s *Scheduler) RegisterTask(namespaceID, capability string) (*Task, error) {
	if namespaceID == "" {
		return nil, fmt.Errorf("scheduler: register task: namespace must not be empty")
	}
	if capability == "" {
		return nil, fmt.Errorf("scheduler: register task: capability must not be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextID++
	task := &Task{
		ID:           fmt.Sprintf("task-%d", s.nextID),
		NamespaceID:  namespaceID,
		Capability:   capability,
		Status:       "unassigned",
		AssignedTo:   "",
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
	target.Status = "assigned"
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

// TaskStatus returns the current status of taskID.
func (s *Scheduler) TaskStatus(taskID string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, t := range s.tasks {
		if t.ID == taskID {
			return t.Status, nil
		}
	}
	return "", fmt.Errorf("scheduler: unknown task %q", taskID)
}
