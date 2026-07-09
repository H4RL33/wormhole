package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/H4RL33/wormhole/internal/core/events"
	"github.com/H4RL33/wormhole/internal/core/identity"
)

// defaultChannelNames are bootstrapped into every project the first time an
// agent registers into it (RFC-0001 §8.5 joining flow).
var defaultChannelNames = []string{"introductions", "general"}

// ensureDefaultChannels creates any of defaultChannelNames missing from the
// project. It lists existing channels first and only creates names that are
// absent, since events.Store.CreateChannel has no unique constraint on
// (project_id, name) and would otherwise duplicate channels on every
// registration into the same project.
func ensureDefaultChannels(ctx context.Context, store *events.Store, projectID string) error {
	existing, err := store.ListChannels(ctx, projectID)
	if err != nil {
		return fmt.Errorf("ensure default channels: list channels: %w", err)
	}
	have := make(map[string]bool, len(existing))
	for _, c := range existing {
		have[c.Name] = true
	}
	for _, name := range defaultChannelNames {
		if have[name] {
			continue
		}
		if _, err := store.CreateChannel(ctx, projectID, name); err != nil {
			return fmt.Errorf("ensure default channels: create channel %q: %w", name, err)
		}
	}
	return nil
}

// RegisterAgentInput is the wormhole.agent.register argument shape.
// Schema is indicative per architecture.md M1 — frozen here at
// implementation time, not finalized by any RFC text.
type RegisterAgentInput struct {
	Name         string   `json:"name,omitempty"`
	Permissions  []string `json:"permissions"`
	Owner        string   `json:"owner"`
	Model        string   `json:"model"`
	Capabilities []string `json:"capabilities"`
	Repositories []string `json:"repositories"`
	Roles        []string `json:"roles"`
}

// RegisterAgentOutput is the wormhole.agent.register result shape. Token
// is the raw bearer token, returned exactly once (identity.Store.Register
// never persists or re-derives it).
type RegisterAgentOutput struct {
	AgentID      string    `json:"agent_id"`
	PassportID   string    `json:"passport_id"`
	Token        string    `json:"token"`
	Repositories []string  `json:"repositories"`
	Roles        []string  `json:"roles"`
	IssuedAt     time.Time `json:"issued_at"`
}

// RegisterAgentTool wires wormhole.agent.register: no auth required, since
// registration is how an identity first comes into existence (RFC-0001
// §8.5 joining flow, step 1).
func RegisterAgentTool(store *identity.Store, eventsStore *events.Store) Tool {
	return Tool{
		Name:             "wormhole.agent.register",
		Description:      "Registers a new agent identity, issues its passport and a project-scoped bearer token.",
		RequiresAuth:     false,
		ArgumentsExample: RegisterAgentInput{},
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			var in RegisterAgentInput
			if err := json.Unmarshal(arguments, &in); err != nil {
				return nil, fmt.Errorf("mcp: decode wormhole.agent.register arguments: %w", err)
			}
			if in.Owner == "" && in.Name != "" {
				in.Owner = in.Name
			}
			agent, passport, token, err := store.Register(ctx, projectID, in.Permissions, in.Owner, in.Model, in.Capabilities, in.Repositories, in.Roles)
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.agent.register: %w", err)
			}
			if err := ensureDefaultChannels(ctx, eventsStore, projectID); err != nil {
				log.Printf("mcp: wormhole.agent.register: default channel bootstrap failed: %v", err)
			}
			return RegisterAgentOutput{
				AgentID:      agent.ID,
				PassportID:   passport.ID,
				Token:        token,
				Repositories: passport.Repositories,
				Roles:        passport.Roles,
				IssuedAt:     passport.IssuedAt,
			}, nil
		},
	}
}

// WhoAmIOutput is the wormhole.agent.whoami result shape: the identity and
// authorization scope the auth middleware already resolved.
type WhoAmIOutput struct {
	AgentID      string   `json:"agent_id"`
	Owner        string   `json:"owner"`
	Model        string   `json:"model"`
	Capabilities []string `json:"capabilities"`
	ProjectID    string   `json:"project_id"`
	Permissions  []string `json:"permissions"`
}

// WhoAmITool wires wormhole.agent.whoami: requires auth, and its handler
// does no identity lookup of its own — the resolved scope from the
// middleware (architecture.md M4) is the entire answer.
func WhoAmITool() Tool {
	return Tool{
		Name:             "wormhole.agent.whoami",
		Description:      "Returns the identity and authorization scope resolved from the caller's bearer token.",
		RequiresAuth:     true,
		ArgumentsExample: nil,
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			return WhoAmIOutput{
				AgentID:      scope.Agent.ID,
				Owner:        scope.Agent.Owner,
				Model:        scope.Agent.Model,
				Capabilities: scope.Agent.Capabilities,
				ProjectID:    scope.ProjectID,
				Permissions:  scope.Permissions,
			}, nil
		},
	}
}
