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

// tokenTTL is an inferred alpha default — neither RFC-0001 nor RFC-0002
// specifies a token lifetime. See Global Constraints in
// docs/superpowers/plans/2026-07-11-day5-mcp-wiring.md.
const tokenTTL = 30 * 24 * time.Hour

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

// AuditEntry is one append-only record in an identity's audit trail
// (RFC-0001 §8.4).
type AuditEntry struct {
	ID        string
	AgentID   string
	ProjectID string
	Action    string
	CreatedAt time.Time
	Seq       int64
}

// Audit action names recorded by the identity service.
const (
	ActionAgentRegistered = "agent.registered"
	ActionTokenIssued     = "token.issued"
	ActionPassportIssued  = "passport.issued"
)

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
	if _, err := recordAction(ctx, tx, agent.ID, projectID, ActionAgentRegistered); err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: record audit entry: %w", err)
	}

	passport, err := issuePassport(ctx, tx, agent.ID, projectID, repositories, roles)
	if err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: issue passport: %w", err)
	}
	if _, err := recordAction(ctx, tx, agent.ID, projectID, ActionPassportIssued); err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: record audit entry: %w", err)
	}

	permissionsJSON, err := json.Marshal(permissions)
	if err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: marshal permissions: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO agent_tokens (agent_id, project_id, permissions, token_hash, expires_at) VALUES ($1, $2, $3, $4, $5)`,
		agent.ID, projectID, permissionsJSON, tokenHash, time.Now().Add(tokenTTL),
	); err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: insert token: %w", err)
	}
	if _, err := recordAction(ctx, tx, agent.ID, projectID, ActionTokenIssued); err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: record audit entry: %w", err)
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
	if agentID == "" || projectID == "" {
		return Passport{}, ErrInvalidScope
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Passport{}, fmt.Errorf("identity: begin tx: %w", err)
	}
	defer tx.Rollback()

	passport, err := issuePassport(ctx, tx, agentID, projectID, repositories, roles)
	if err != nil {
		return Passport{}, err
	}
	if _, err := recordAction(ctx, tx, agentID, projectID, ActionPassportIssued); err != nil {
		return Passport{}, fmt.Errorf("identity: record audit entry: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Passport{}, fmt.Errorf("identity: commit: %w", err)
	}

	return passport, nil
}

// RecordAction appends one entry to agentID's audit trail for projectID.
func (s *Store) RecordAction(ctx context.Context, agentID, projectID, action string) (AuditEntry, error) {
	return recordAction(ctx, s.db, agentID, projectID, action)
}

func recordAction(ctx context.Context, db dbtx, agentID, projectID, action string) (AuditEntry, error) {
	var entry AuditEntry
	err := db.QueryRowContext(ctx,
		`INSERT INTO audit_log (agent_id, project_id, action) VALUES ($1, $2, $3)
		 RETURNING id, agent_id, project_id, action, created_at, seq`,
		agentID, projectID, action,
	).Scan(&entry.ID, &entry.AgentID, &entry.ProjectID, &entry.Action, &entry.CreatedAt, &entry.Seq)
	if err != nil {
		return AuditEntry{}, fmt.Errorf("identity: insert audit entry: %w", err)
	}
	return entry, nil
}

// ListAuditTrail returns agentID's audit trail for projectID, oldest
// first.
func (s *Store) ListAuditTrail(ctx context.Context, agentID, projectID string) ([]AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, agent_id, project_id, action, created_at, seq
		 FROM audit_log
		 WHERE agent_id = $1 AND project_id = $2
		 ORDER BY seq ASC`,
		agentID, projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("identity: list audit trail: %w", err)
	}
	defer rows.Close()

	entries := []AuditEntry{}
	for rows.Next() {
		var entry AuditEntry
		if err := rows.Scan(&entry.ID, &entry.AgentID, &entry.ProjectID, &entry.Action, &entry.CreatedAt, &entry.Seq); err != nil {
			return nil, fmt.Errorf("identity: scan audit entry: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("identity: iterate audit trail: %w", err)
	}
	return entries, nil
}

// dbtx is satisfied by both *sql.DB and *sql.Tx, letting issuePassport and
// recordAction run standalone (Store methods) or inside Register's
// transaction.
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

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("identity: begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO agent_tokens (agent_id, project_id, permissions, token_hash, expires_at) VALUES ($1, $2, $3, $4, $5)`,
		agentID, projectID, permissionsJSON, tokenHash, time.Now().Add(tokenTTL),
	); err != nil {
		return "", fmt.Errorf("identity: insert token: %w", err)
	}
	if _, err := recordAction(ctx, tx, agentID, projectID, ActionTokenIssued); err != nil {
		return "", fmt.Errorf("identity: record audit entry: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("identity: commit: %w", err)
	}

	return rawToken, nil
}

// WhoAmI resolves a raw bearer token to the agent identity that owns it.
// Returns ErrInvalidToken for any token that doesn't match a stored hash —
// forged, expired-format, or simply unknown. projectID is optional: when
// non-empty, the token must belong to exactly that project (cross-tenant
// isolation — a token issued for project A must never resolve under
// project B's id, github.com/H4RL33/wormhole/issues/11 comment thread).
// When empty (wormhole.agent.whoami's schema is exempted from requiring
// project_id per RFC-0001 §9 — see internal/mcp/jsonrpc.go's
// buildInputSchema), the token's own project is resolved from
// agent_tokens.project_id instead of requiring the caller to already know
// it.
func (s *Store) WhoAmI(ctx context.Context, projectID, rawToken string) (AuthenticatedScope, error) {
	if rawToken == "" {
		return AuthenticatedScope{}, ErrInvalidToken
	}

	sum := sha256.Sum256([]byte(rawToken))
	hash := hex.EncodeToString(sum[:])

	query := `SELECT a.id, a.owner, a.model, a.capabilities, a.created_at, t.permissions, t.project_id
		 FROM agents a
		 JOIN agent_tokens t ON t.agent_id = a.id
		 WHERE t.token_hash = $1 AND t.expires_at > now()`
	args := []any{hash}
	if projectID != "" {
		query += ` AND t.project_id = $2`
		args = append(args, projectID)
	}

	var agent Agent
	var capsRaw []byte
	var permissionsRaw []byte
	var resolvedProjectID string
	err := s.db.QueryRowContext(ctx, query, args...).
		Scan(&agent.ID, &agent.Owner, &agent.Model, &capsRaw, &agent.CreatedAt, &permissionsRaw, &resolvedProjectID)
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

	return AuthenticatedScope{Agent: agent, ProjectID: resolvedProjectID, Permissions: permissions}, nil
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
