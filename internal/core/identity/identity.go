package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"
)

// ErrInvalidToken is returned by WhoAmI when the provided token doesn't
// resolve to any agent — forged, tampered, or unknown tokens all collapse
// to this single error so callers can't distinguish "wrong token" from
// "token for a different agent" (RFC-0001 §13: identities must be
// unforgeable within scope).
var ErrInvalidToken = errors.New("identity: invalid token")

// ErrInvalidScope is returned when token issuance omits its project or
// permission-set context. A non-nil empty permission set is valid.
var ErrInvalidScope = errors.New("identity: invalid scope")

// ErrPassportExists is returned when a passport is issued for an
// agent+project pair that already has one — passports are append-only
// and unique per (agent, project) (migration 000001, UNIQUE(agent_id,
// project_id)).
var ErrPassportExists = errors.New("identity: passport already issued for this agent and project")

type Agent struct {
	ID           string
	Owner        string
	Model        string
	Capabilities []string
	CreatedAt    time.Time
}

// AuthenticatedScope is the identity and complete authorization context
// established by a project-scoped token. Later middleware can enforce the
// returned permissions without another token lookup.
type AuthenticatedScope struct {
	Agent       Agent
	ProjectID   string
	Permissions []string
}

// Passport is the portable, project-scoped identity record an agent
// presents when joining a project: its declared repository scope and
// resolved roles (RFC-0001 §8.4, §8.5).
type Passport struct {
	ID           string
	AgentID      string
	ProjectID    string
	Repositories []string
	Roles        []string
	IssuedAt     time.Time
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Register creates a new agent identity, issues its passport for
// projectID, and issues a bearer token for it. The raw token is returned
// exactly once — only its SHA-256 hash is persisted, so the raw value can
// never be recovered from storage.
func (s *Store) Register(ctx context.Context, projectID string, permissions []string, owner, model string, capabilities, repositories, roles []string) (Agent, Passport, string, error) {
	if projectID == "" || permissions == nil {
		return Agent{}, Passport{}, "", ErrInvalidScope
	}
	if capabilities == nil {
		capabilities = []string{}
	}
	capsJSON, err := json.Marshal(capabilities)
	if err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: marshal capabilities: %w", err)
	}

	rawToken, tokenHash, err := generateToken()
	if err != nil {
		return Agent{}, Passport{}, "", err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: begin tx: %w", err)
	}
	defer tx.Rollback()

	var agent Agent
	var capsRaw []byte
	err = tx.QueryRowContext(ctx,
		`INSERT INTO agents (owner, model, capabilities) VALUES ($1, $2, $3)
		 RETURNING id, owner, model, capabilities, created_at`,
		owner, model, capsJSON,
	).Scan(&agent.ID, &agent.Owner, &agent.Model, &capsRaw, &agent.CreatedAt)
	if err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: insert agent: %w", err)
	}

	passport, err := issuePassport(ctx, tx, agent.ID, projectID, repositories, roles)
	if err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: issue passport: %w", err)
	}

	permissionsJSON, err := json.Marshal(permissions)
	if err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: marshal permissions: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO agent_tokens (agent_id, project_id, permissions, token_hash) VALUES ($1, $2, $3, $4)`,
		agent.ID, projectID, permissionsJSON, tokenHash,
	); err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: insert token: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: commit: %w", err)
	}

	if err := json.Unmarshal(capsRaw, &agent.Capabilities); err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: unmarshal capabilities: %w", err)
	}

	return agent, passport, rawToken, nil
}

// IssuePassport creates the portable identity record an agent presents
// when joining projectID. Nil repositories/roles are treated as empty,
// never as an error. A second passport for the same agent+project pair
// is rejected — passports are append-only.
func (s *Store) IssuePassport(ctx context.Context, agentID, projectID string, repositories, roles []string) (Passport, error) {
	return issuePassport(ctx, s.db, agentID, projectID, repositories, roles)
}

// dbtx is satisfied by both *sql.DB and *sql.Tx, letting issuePassport run
// standalone (Store.IssuePassport) or inside Register's transaction.
type dbtx interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func issuePassport(ctx context.Context, db dbtx, agentID, projectID string, repositories, roles []string) (Passport, error) {
	if repositories == nil {
		repositories = []string{}
	}
	if roles == nil {
		roles = []string{}
	}
	reposJSON, err := json.Marshal(repositories)
	if err != nil {
		return Passport{}, fmt.Errorf("identity: marshal repositories: %w", err)
	}
	rolesJSON, err := json.Marshal(roles)
	if err != nil {
		return Passport{}, fmt.Errorf("identity: marshal roles: %w", err)
	}

	var passport Passport
	var reposRaw, rolesRaw []byte
	err = db.QueryRowContext(ctx,
		`INSERT INTO passports (agent_id, project_id, repositories, roles) VALUES ($1, $2, $3, $4)
		 RETURNING id, agent_id, project_id, repositories, roles, issued_at`,
		agentID, projectID, reposJSON, rolesJSON,
	).Scan(&passport.ID, &passport.AgentID, &passport.ProjectID, &reposRaw, &rolesRaw, &passport.IssuedAt)
	if err != nil {
		if pqErr, ok := err.(*pq.Error); ok && pqErr.Code == "23505" {
			return Passport{}, ErrPassportExists
		}
		return Passport{}, fmt.Errorf("identity: insert passport: %w", err)
	}
	if err := json.Unmarshal(reposRaw, &passport.Repositories); err != nil {
		return Passport{}, fmt.Errorf("identity: unmarshal repositories: %w", err)
	}
	if err := json.Unmarshal(rolesRaw, &passport.Roles); err != nil {
		return Passport{}, fmt.Errorf("identity: unmarshal roles: %w", err)
	}
	return passport, nil
}

// IssueToken issues a separately scoped token for an existing agent.
func (s *Store) IssueToken(ctx context.Context, agentID, projectID string, permissions []string) (string, error) {
	if agentID == "" || projectID == "" || permissions == nil {
		return "", ErrInvalidScope
	}
	permissionsJSON, err := json.Marshal(permissions)
	if err != nil {
		return "", fmt.Errorf("identity: marshal permissions: %w", err)
	}
	rawToken, tokenHash, err := generateToken()
	if err != nil {
		return "", err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_tokens (agent_id, project_id, permissions, token_hash) VALUES ($1, $2, $3, $4)`,
		agentID, projectID, permissionsJSON, tokenHash,
	); err != nil {
		return "", fmt.Errorf("identity: insert token: %w", err)
	}
	return rawToken, nil
}

// WhoAmI resolves a raw bearer token to the agent identity that owns it.
// Returns ErrInvalidToken for any token that doesn't match a stored hash —
// forged, expired-format, or simply unknown.
func (s *Store) WhoAmI(ctx context.Context, projectID, rawToken string) (AuthenticatedScope, error) {
	if projectID == "" || rawToken == "" {
		return AuthenticatedScope{}, ErrInvalidToken
	}

	sum := sha256.Sum256([]byte(rawToken))
	hash := hex.EncodeToString(sum[:])

	var agent Agent
	var capsRaw []byte
	var permissionsRaw []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT a.id, a.owner, a.model, a.capabilities, a.created_at, t.permissions
		 FROM agents a
		 JOIN agent_tokens t ON t.agent_id = a.id
		 WHERE t.token_hash = $1 AND t.project_id = $2`,
		hash, projectID,
	).Scan(&agent.ID, &agent.Owner, &agent.Model, &capsRaw, &agent.CreatedAt, &permissionsRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthenticatedScope{}, ErrInvalidToken
	}
	if err != nil {
		return AuthenticatedScope{}, fmt.Errorf("identity: whoami query: %w", err)
	}

	if err := json.Unmarshal(capsRaw, &agent.Capabilities); err != nil {
		return AuthenticatedScope{}, fmt.Errorf("identity: unmarshal capabilities: %w", err)
	}
	var permissions []string
	if err := json.Unmarshal(permissionsRaw, &permissions); err != nil {
		return AuthenticatedScope{}, fmt.Errorf("identity: unmarshal permissions: %w", err)
	}

	return AuthenticatedScope{Agent: agent, ProjectID: projectID, Permissions: permissions}, nil
}

func generateToken() (raw string, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("identity: generate token: %w", err)
	}
	raw = hex.EncodeToString(b)
	sum := sha256.Sum256([]byte(raw))
	hash = hex.EncodeToString(sum[:])
	return raw, hash, nil
}
