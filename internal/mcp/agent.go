package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/H4RL33/wormhole/internal/core/events"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/core/kb"
	"github.com/H4RL33/wormhole/internal/core/roles"
)

// defaultChannelNames are bootstrapped into every project the first time an
// agent registers into it (RFC-0001 §8.5 joining flow).
var defaultChannelNames = []string{"introductions", "general"}

// ensureDefaultChannels atomically creates each fixed channel if absent.
func ensureDefaultChannelsInTx(ctx context.Context, tx *sql.Tx, store *events.Store, projectID string) error {
	for _, name := range defaultChannelNames {
		if _, err := store.EnsureChannelInTx(ctx, tx, projectID, name); err != nil {
			return fmt.Errorf("ensure default channels: create channel %q: %w", name, err)
		}
	}
	return nil
}

// onboardingArticleTitle is the fixed title used both to write the
// onboarding article and to check for its existence idempotently — kept
// as a named constant so Task 3's test and this seeding logic can't drift.
const onboardingArticleTitle = "How This Project Works"

const onboardingArticleBootstrapKey = "project-onboarding"

// onboardingArticleBody is seeded once per project, on the first agent
// registration into it (see design note above Task 3 in the plan: there
// is no project-creation hook to attach this to, so first-registration is
// the earliest point a real authoring agent with a passport exists).
const onboardingArticleBody = `This project uses Wormhole's MCP tool surface for coordination. Three things every joining agent should know:

**Task status values:** exactly ` + "`todo`, `wip`, `blocked`, `done`" + `. Valid transitions: todo->wip, wip->blocked, wip->done, blocked->wip. done is terminal.

**Channel event types:** exactly ` + "`task.status_changed`, `review.requested`, `build.failed`, `discovery.logged`, `message.posted`" + `. ` + "`message.posted`" + ` requires a non-empty note (free-text message content); the other four carry structured payload instead.

**The channel is the changelog:** ` + "`wormhole.channel.subscribe`" + ` returns a project's full event history — read it to see what other agents have done and how they've used these values in practice, the same way you'd read git log to learn a team's commit conventions.`

// ensureOnboardingArticle atomically creates the fixed onboarding KB article
// for projectID if its dedicated bootstrap marker is absent.
func ensureOnboardingArticleInTx(ctx context.Context, tx *sql.Tx, kbStore *kb.Store, projectID, authorAgentID string, embedding []float32) error {
	if _, err := kbStore.EnsureBootstrapArticleInTx(ctx, tx, projectID, authorAgentID, onboardingArticleBootstrapKey, onboardingArticleTitle, onboardingArticleBody, nil, embedding); err != nil {
		return fmt.Errorf("ensure onboarding article: write: %w", err)
	}
	return nil
}

// PrepareOnboardingArticleEmbedding performs the fixed paid provider call once
// during Fabric startup. Unauthenticated registration handlers only read the
// resulting in-memory vector after their database validation/reservation step.
func PrepareOnboardingArticleEmbedding(ctx context.Context, kbStore *kb.Store) error {
	embedding, err := kbStore.PrepareArticleEmbedding(ctx, onboardingArticleTitle, onboardingArticleBody)
	if err != nil {
		return fmt.Errorf("prepare onboarding article embedding: %w", err)
	}
	return kbStore.SetPreparedBootstrapEmbedding(onboardingArticleBootstrapKey, embedding)
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
	Role         string   `json:"role,omitempty"`
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
	Role         string    `json:"role,omitempty"`
}

// EnrolAgentInput is Gateway's Fabric-facing registration contract. The
// local request itself never crosses this package boundary unchanged: Gateway
// supplies its canonical SHA-256 digest and a project-scoped attempt key.
type EnrolAgentInput struct {
	IdempotencyKey string   `json:"idempotency_key"`
	RequestHash    string   `json:"request_hash"`
	Permissions    []string `json:"permissions"`
	Owner          string   `json:"owner"`
	Model          string   `json:"model"`
	Capabilities   []string `json:"capabilities"`
	Repositories   []string `json:"repositories"`
	Roles          []string `json:"roles"`
	Reissue        bool     `json:"reissue,omitempty"`
}

// EnrolAgentOutput is an internal Gateway/Fabric response. Token is present
// only on initial issuance or the single controlled recovery reissue. Gateway
// consumes it into an atomic credential write and never forwards it locally.
type EnrolAgentOutput struct {
	AgentID    string    `json:"agent_id"`
	PassportID string    `json:"passport_id"`
	Token      string    `json:"token,omitempty"`
	IssuedAt   time.Time `json:"issued_at"`
	Replay     bool      `json:"replay"`
	Reissued   bool      `json:"reissued"`
}

// EnrolAgentTool executes the Gateway-owned enrolment registration step in
// Fabric. Identity, Passport, token hash, idempotency row, and fixed project
// bootstrap records commit atomically in Postgres.
func EnrolAgentTool(store *identity.Store, eventsStore *events.Store, kbStore *kb.Store) Tool {
	return Tool{
		Name:             "wormhole.agent.enrol",
		Description:      "Idempotently registers a Gateway enrolment and issues its project-scoped Passport credential.",
		RequiresAuth:     false,
		ArgumentsExample: EnrolAgentInput{},
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			var in EnrolAgentInput
			if err := json.Unmarshal(arguments, &in); err != nil {
				return nil, fmt.Errorf("mcp: decode wormhole.agent.enrol arguments: %w", err)
			}
			tx, err := store.BeginProjectTx(ctx, projectID)
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.agent.enrol: %w", err)
			}
			defer tx.Rollback()

			registration, err := store.RegisterEnrolmentInTx(ctx, tx, identity.EnrolmentRegistrationInput{
				ProjectID: projectID, IdempotencyKey: in.IdempotencyKey, RequestHash: in.RequestHash,
				Permissions: in.Permissions, Owner: in.Owner, Model: in.Model, Capabilities: in.Capabilities,
				Repositories: in.Repositories, Roles: in.Roles, Reissue: in.Reissue,
			})
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.agent.enrol: %w", err)
			}
			if !registration.Replay {
				onboardingEmbedding, err := kbStore.PreparedBootstrapEmbedding(onboardingArticleBootstrapKey)
				if err != nil {
					return nil, fmt.Errorf("mcp: wormhole.agent.enrol: onboarding article embedding: %w", err)
				}
				if err := ensureDefaultChannelsInTx(ctx, tx, eventsStore, projectID); err != nil {
					return nil, fmt.Errorf("mcp: wormhole.agent.enrol: default channel bootstrap: %w", err)
				}
				if err := ensureOnboardingArticleInTx(ctx, tx, kbStore, projectID, registration.Agent.ID, onboardingEmbedding); err != nil {
					return nil, fmt.Errorf("mcp: wormhole.agent.enrol: onboarding article bootstrap: %w", err)
				}
			}
			if err := tx.Commit(); err != nil {
				return nil, fmt.Errorf("mcp: wormhole.agent.enrol: commit registration: %w", err)
			}
			return EnrolAgentOutput{
				AgentID: registration.Agent.ID, PassportID: registration.Passport.ID,
				Token: registration.RawToken, IssuedAt: registration.Passport.IssuedAt,
				Replay: registration.Replay, Reissued: registration.Reissued,
			}, nil
		},
	}
}

// unionAppend returns a new slice containing base's elements followed by
// any of extra's elements not already present in base, preserving base's
// original order and appending new elements in extra's order. Used to
// merge caller-supplied permissions with a resolved role template's
// permission bundle (and to add a resolved role name into the roles tag
// slice) deterministically.
func unionAppend(base, extra []string) []string {
	seen := make(map[string]bool, len(base))
	out := make([]string, 0, len(base)+len(extra))
	for _, v := range base {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for _, v := range extra {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// RegisterAgentTool wires wormhole.agent.register: no auth required, since
// registration is how an identity first comes into existence (RFC-0001
// §8.5 joining flow, step 1).
func RegisterAgentTool(store *identity.Store, eventsStore *events.Store, rolesStore *roles.Store, kbStore *kb.Store) Tool {
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
			if in.Role != "" {
				template, err := rolesStore.GetTemplate(ctx, in.Role)
				if errors.Is(err, roles.ErrTemplateNotFound) {
					return nil, fmt.Errorf("mcp: wormhole.agent.register: unknown role template %q: %w", in.Role, err)
				}
				if err != nil {
					return nil, fmt.Errorf("mcp: wormhole.agent.register: %w", err)
				}
				in.Permissions = unionAppend(in.Permissions, template.PermissionBundle)
				in.Roles = unionAppend(in.Roles, []string{in.Role})

				// Merge default capabilities and roles from template
				if len(in.Capabilities) == 0 && len(template.DefaultCapabilities) > 0 {
					in.Capabilities = template.DefaultCapabilities
				}
				if len(in.Roles) == 1 && len(template.DefaultRoles) > 0 {
					in.Roles = unionAppend(in.Roles, template.DefaultRoles)
				}
			}
			tx, err := store.BeginProjectTx(ctx, projectID)
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.agent.register: %w", err)
			}
			defer tx.Rollback()

			agent, passport, token, err := store.RegisterInTx(ctx, tx, projectID, in.Permissions, in.Owner, in.Model, in.Capabilities, in.Repositories, in.Roles)
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.agent.register: %w", err)
			}
			onboardingEmbedding, err := kbStore.PreparedBootstrapEmbedding(onboardingArticleBootstrapKey)
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.agent.register: onboarding article embedding: %w", err)
			}
			if err := ensureDefaultChannelsInTx(ctx, tx, eventsStore, projectID); err != nil {
				return nil, fmt.Errorf("mcp: wormhole.agent.register: default channel bootstrap: %w", err)
			}
			if err := ensureOnboardingArticleInTx(ctx, tx, kbStore, projectID, agent.ID, onboardingEmbedding); err != nil {
				return nil, fmt.Errorf("mcp: wormhole.agent.register: onboarding article bootstrap: %w", err)
			}
			if err := tx.Commit(); err != nil {
				return nil, fmt.Errorf("mcp: wormhole.agent.register: commit bootstrap transaction: %w", err)
			}
			return RegisterAgentOutput{
				AgentID:      agent.ID,
				PassportID:   passport.ID,
				Token:        token,
				Repositories: passport.Repositories,
				Roles:        passport.Roles,
				IssuedAt:     passport.IssuedAt,
				Role:         in.Role,
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
		Name:         "wormhole.agent.whoami",
		Description:  "Returns the identity and authorization scope resolved from the caller's bearer token.",
		RequiresAuth: true,
		// Auth-only: self-identification must not require a specific
		// permission (gating whoami would be circular).
		RequiredPermission: "",
		ArgumentsExample:   nil,
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
