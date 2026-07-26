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
	"sort"
	"strings"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
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

var (
	// ErrEnrolmentConflict rejects reuse of one project-scoped idempotency key
	// for a different canonical request.
	ErrEnrolmentConflict = errors.New("identity: enrolment idempotency conflict")
	// ErrEnrolmentInProgress rejects live concurrent execution for one
	// project-scoped idempotency key. The caller may retry the same request.
	ErrEnrolmentInProgress = errors.New("identity: enrolment already in progress")
	// ErrEnrolmentReissueExhausted is returned after the single controlled
	// credential recovery permitted for an enrolment has already been used.
	ErrEnrolmentReissueExhausted = errors.New("identity: enrolment credential reissue exhausted")
	ErrInvalidEnrolment          = errors.New("identity: invalid enrolment")
)

// tokenTTL is an inferred alpha default — neither RFC-0001 nor RFC-0002
// specifies a token lifetime.
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
	// Roles is the calling agent's passport role tags for ProjectID
	// (RFC-0001 §8.4's free-text roles tags, plus any Chapter-6-resolved
	// role template names folded in at registration). Empty when the
	// agent's passport carries no role tags.
	Roles []string
}

// HasPermission reports whether this scope's Passport grants the named
// permission. Exact string match against the resolved permission set; no
// wildcards or hierarchy (RFC-0001 §8.4 permissions are a flat action list).
// Empty name never matches.
func (s AuthenticatedScope) HasPermission(name string) bool {
	if name == "" {
		return false
	}
	for _, p := range s.Permissions {
		if p == name {
			return true
		}
	}
	return false
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

// EnrolmentRegistrationInput is Fabric's durable identity-registration
// contract. RequestHash is the Gateway-computed SHA-256 digest of the
// canonical local request. Reissue requests preserve identity and Passport.
type EnrolmentRegistrationInput struct {
	ProjectID      string
	IdempotencyKey string
	RequestHash    string
	Permissions    []string
	Owner          string
	Model          string
	Capabilities   []string
	Repositories   []string
	Roles          []string
	Reissue        bool
}

// EnrolmentRegistrationResult contains a raw token only for a fresh issuance
// or the one controlled reissue. Completed ordinary replays return references
// only, so Fabric never attempts to recover raw credential material.
type EnrolmentRegistrationResult struct {
	Agent    Agent
	Passport Passport
	RawToken string
	Replay   bool
	Reissued bool
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

// BeginProjectTx starts a transaction with the project RLS context set. It is
// used by MCP workflows that must coordinate writes across isolated Core stores;
// the caller owns commit and rollback.
func (s *Store) BeginProjectTx(ctx context.Context, projectID string) (*sql.Tx, error) {
	if projectID == "" {
		return nil, ErrInvalidScope
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("identity: begin project tx: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectID); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("identity: begin project tx: set project id: %w", err)
	}
	return tx, nil
}

// BeginBootstrapSnapshotTx starts the single repeatable-read, project-scoped
// transaction used to compose a complete bootstrap snapshot (RFC-0003 §8.1).
func (s *Store) BeginBootstrapSnapshotTx(ctx context.Context, projectID string) (*sql.Tx, error) {
	if projectID == "" {
		return nil, ErrInvalidScope
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("identity: begin bootstrap snapshot tx: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectID); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("identity: begin bootstrap snapshot tx: set project id: %w", err)
	}
	return tx, nil
}

// ReadBootstrapIdentityInTx returns project, agent, Passport, and the already
// authenticated permission scope from the caller-owned snapshot transaction.
func (s *Store) ReadBootstrapIdentityInTx(ctx context.Context, tx *sql.Tx, projectID, agentID string, permissions []string) (types.BootstrapProjectV1, types.BootstrapIdentityV1, error) {
	if tx == nil || projectID == "" || agentID == "" || permissions == nil {
		return types.BootstrapProjectV1{}, types.BootstrapIdentityV1{}, ErrInvalidScope
	}
	var project types.BootstrapProjectV1
	if err := tx.QueryRowContext(ctx,
		`SELECT id, name, owner, created_at FROM projects WHERE id = $1`, projectID,
	).Scan(&project.ID, &project.Name, &project.Owner, &project.CreatedAt); err != nil {
		return types.BootstrapProjectV1{}, types.BootstrapIdentityV1{}, fmt.Errorf("identity: read bootstrap project: %w", err)
	}

	var identity types.BootstrapIdentityV1
	var capabilities, repositories, roles []byte
	if err := tx.QueryRowContext(ctx, `
		SELECT a.id, a.owner, a.model, a.capabilities, a.created_at,
		       p.id, p.agent_id, p.project_id, p.repositories, p.roles, p.issued_at
		FROM agents a
		JOIN passports p ON p.agent_id = a.id AND p.project_id = $1
		WHERE a.id = $2`, projectID, agentID,
	).Scan(
		&identity.Agent.ID, &identity.Agent.Owner, &identity.Agent.Model, &capabilities, &identity.Agent.CreatedAt,
		&identity.Passport.ID, &identity.Passport.AgentID, &identity.Passport.ProjectID, &repositories, &roles, &identity.Passport.IssuedAt,
	); err != nil {
		return types.BootstrapProjectV1{}, types.BootstrapIdentityV1{}, fmt.Errorf("identity: read bootstrap identity: %w", err)
	}
	if err := json.Unmarshal(capabilities, &identity.Agent.Capabilities); err != nil {
		return types.BootstrapProjectV1{}, types.BootstrapIdentityV1{}, fmt.Errorf("identity: decode bootstrap capabilities: %w", err)
	}
	if err := json.Unmarshal(repositories, &identity.Passport.Repositories); err != nil {
		return types.BootstrapProjectV1{}, types.BootstrapIdentityV1{}, fmt.Errorf("identity: decode bootstrap repositories: %w", err)
	}
	if err := json.Unmarshal(roles, &identity.Passport.Roles); err != nil {
		return types.BootstrapProjectV1{}, types.BootstrapIdentityV1{}, fmt.Errorf("identity: decode bootstrap roles: %w", err)
	}
	identity.Agent.Capabilities = sortedUniqueStrings(identity.Agent.Capabilities)
	identity.Passport.Repositories = sortedUniqueStrings(identity.Passport.Repositories)
	identity.Passport.Roles = sortedUniqueStrings(identity.Passport.Roles)
	identity.Permissions = sortedUniqueStrings(permissions)
	return project, identity, nil
}

// BootstrapTimestampInTx reads Postgres's stable transaction timestamp so the
// response cursor describes the same repeatable-read snapshot as every row.
func (s *Store) BootstrapTimestampInTx(ctx context.Context, tx *sql.Tx) (time.Time, error) {
	if tx == nil {
		return time.Time{}, ErrInvalidScope
	}
	var timestamp time.Time
	if err := tx.QueryRowContext(ctx, `SELECT transaction_timestamp()`).Scan(&timestamp); err != nil {
		return time.Time{}, fmt.Errorf("identity: read bootstrap timestamp: %w", err)
	}
	return timestamp.UTC(), nil
}

func sortedUniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// Register creates a new agent identity, issues its passport for
// projectID, and issues a bearer token for it. The raw token is returned
// exactly once — only its SHA-256 hash is persisted, so the raw value can
// never be recovered from storage.
func (s *Store) Register(ctx context.Context, projectID string, permissions []string, owner, model string, capabilities, repositories, roles []string) (Agent, Passport, string, error) {
	tx, err := s.BeginProjectTx(ctx, projectID)
	if err != nil {
		return Agent{}, Passport{}, "", err
	}
	defer tx.Rollback()

	agent, passport, rawToken, err := s.RegisterInTx(ctx, tx, projectID, permissions, owner, model, capabilities, repositories, roles)
	if err != nil {
		return Agent{}, Passport{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: commit: %w", err)
	}
	return agent, passport, rawToken, nil
}

// RegisterEnrolment durably serializes one project-scoped Gateway enrolment.
// A completed replay returns the original identity references without a raw
// token. Reissue=true permits exactly one replacement credential and expires
// the unusable prior token in the same transaction.
func (s *Store) RegisterEnrolment(ctx context.Context, in EnrolmentRegistrationInput) (EnrolmentRegistrationResult, error) {
	if in.ProjectID == "" || in.IdempotencyKey == "" || !validRequestHash(in.RequestHash) || in.Permissions == nil {
		return EnrolmentRegistrationResult{}, ErrInvalidEnrolment
	}
	tx, err := s.BeginProjectTx(ctx, in.ProjectID)
	if err != nil {
		return EnrolmentRegistrationResult{}, err
	}
	defer tx.Rollback()

	result, err := s.RegisterEnrolmentInTx(ctx, tx, in)
	if err != nil {
		return EnrolmentRegistrationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return EnrolmentRegistrationResult{}, fmt.Errorf("identity: commit enrolment: %w", err)
	}
	return result, nil
}

// RegisterEnrolmentInTx is the transaction-scoped core used by Fabric's MCP
// handler so fixed registration bootstrap rows share the same commit.
func (s *Store) RegisterEnrolmentInTx(ctx context.Context, tx *sql.Tx, in EnrolmentRegistrationInput) (EnrolmentRegistrationResult, error) {
	if tx == nil || in.ProjectID == "" || in.IdempotencyKey == "" || !validRequestHash(in.RequestHash) || in.Permissions == nil {
		return EnrolmentRegistrationResult{}, ErrInvalidEnrolment
	}
	lockName := in.ProjectID + ":" + in.IdempotencyKey
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockName); err != nil {
		return EnrolmentRegistrationResult{}, fmt.Errorf("identity: acquire enrolment lock: %w", err)
	}

	var storedHash, state string
	var agentID, passportID, tokenID sql.NullString
	var reissueCount int
	err := tx.QueryRowContext(ctx, `
		SELECT request_hash, state, agent_id, passport_id, token_id, reissue_count
		FROM agent_enrolments
		WHERE project_id = $1 AND idempotency_key = $2
		FOR UPDATE`, in.ProjectID, in.IdempotencyKey,
	).Scan(&storedHash, &state, &agentID, &passportID, &tokenID, &reissueCount)
	if errors.Is(err, sql.ErrNoRows) {
		return s.createEnrolmentInTx(ctx, tx, in)
	}
	if err != nil {
		return EnrolmentRegistrationResult{}, fmt.Errorf("identity: read enrolment: %w", err)
	}
	if storedHash != in.RequestHash {
		return EnrolmentRegistrationResult{}, ErrEnrolmentConflict
	}
	if state != "registered" || !agentID.Valid || !passportID.Valid || !tokenID.Valid {
		return EnrolmentRegistrationResult{}, ErrEnrolmentInProgress
	}

	agent, passport, err := loadEnrolmentIdentity(ctx, tx, agentID.String, passportID.String)
	if err != nil {
		return EnrolmentRegistrationResult{}, err
	}
	result := EnrolmentRegistrationResult{Agent: agent, Passport: passport, Replay: true}
	if !in.Reissue {
		return result, nil
	}
	if reissueCount >= 1 {
		return EnrolmentRegistrationResult{}, ErrEnrolmentReissueExhausted
	}

	rawToken, tokenHash, err := generateToken()
	if err != nil {
		return EnrolmentRegistrationResult{}, err
	}
	permissionsJSON, err := json.Marshal(in.Permissions)
	if err != nil {
		return EnrolmentRegistrationResult{}, fmt.Errorf("identity: marshal reissue permissions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_tokens SET expires_at = now() WHERE id = $1 AND project_id = $2`, tokenID.String, in.ProjectID); err != nil {
		return EnrolmentRegistrationResult{}, fmt.Errorf("identity: supersede enrolment token: %w", err)
	}
	var newTokenID string
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO agent_tokens (agent_id, project_id, permissions, token_hash, expires_at)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		agent.ID, in.ProjectID, permissionsJSON, tokenHash, time.Now().Add(tokenTTL),
	).Scan(&newTokenID); err != nil {
		return EnrolmentRegistrationResult{}, fmt.Errorf("identity: insert reissued enrolment token: %w", err)
	}
	if _, err := recordAction(ctx, tx, agent.ID, in.ProjectID, ActionTokenIssued); err != nil {
		return EnrolmentRegistrationResult{}, fmt.Errorf("identity: record reissued token audit entry: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_enrolments
		SET token_id = $3, reissue_count = reissue_count + 1, updated_at = now()
		WHERE project_id = $1 AND idempotency_key = $2`, in.ProjectID, in.IdempotencyKey, newTokenID,
	); err != nil {
		return EnrolmentRegistrationResult{}, fmt.Errorf("identity: complete enrolment reissue: %w", err)
	}
	result.RawToken = rawToken
	result.Reissued = true
	return result, nil
}

func (s *Store) createEnrolmentInTx(ctx context.Context, tx *sql.Tx, in EnrolmentRegistrationInput) (EnrolmentRegistrationResult, error) {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_enrolments (project_id, idempotency_key, request_hash, state)
		VALUES ($1, $2, $3, 'registration_in_progress')`, in.ProjectID, in.IdempotencyKey, in.RequestHash,
	); err != nil {
		return EnrolmentRegistrationResult{}, fmt.Errorf("identity: create enrolment: %w", err)
	}
	agent, passport, rawToken, err := s.RegisterInTx(ctx, tx, in.ProjectID, in.Permissions, in.Owner, in.Model, in.Capabilities, in.Repositories, in.Roles)
	if err != nil {
		return EnrolmentRegistrationResult{}, err
	}
	tokenSum := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(tokenSum[:])
	var tokenID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM agent_tokens WHERE project_id = $1 AND token_hash = $2`, in.ProjectID, tokenHash).Scan(&tokenID); err != nil {
		return EnrolmentRegistrationResult{}, fmt.Errorf("identity: resolve enrolment token reference: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_enrolments
		SET state = 'registered', agent_id = $3, passport_id = $4, token_id = $5, updated_at = now()
		WHERE project_id = $1 AND idempotency_key = $2`, in.ProjectID, in.IdempotencyKey, agent.ID, passport.ID, tokenID,
	); err != nil {
		return EnrolmentRegistrationResult{}, fmt.Errorf("identity: complete enrolment: %w", err)
	}
	return EnrolmentRegistrationResult{Agent: agent, Passport: passport, RawToken: rawToken}, nil
}

func loadEnrolmentIdentity(ctx context.Context, tx *sql.Tx, agentID, passportID string) (Agent, Passport, error) {
	var agent Agent
	var capsRaw []byte
	if err := tx.QueryRowContext(ctx, `SELECT id, owner, model, capabilities, created_at FROM agents WHERE id = $1`, agentID).Scan(&agent.ID, &agent.Owner, &agent.Model, &capsRaw, &agent.CreatedAt); err != nil {
		return Agent{}, Passport{}, fmt.Errorf("identity: load enrolled agent: %w", err)
	}
	if err := json.Unmarshal(capsRaw, &agent.Capabilities); err != nil {
		return Agent{}, Passport{}, fmt.Errorf("identity: unmarshal enrolled capabilities: %w", err)
	}
	var passport Passport
	var repositoriesRaw, rolesRaw []byte
	if err := tx.QueryRowContext(ctx, `
		SELECT id, agent_id, project_id, repositories, roles, issued_at
		FROM passports WHERE id = $1`, passportID,
	).Scan(&passport.ID, &passport.AgentID, &passport.ProjectID, &repositoriesRaw, &rolesRaw, &passport.IssuedAt); err != nil {
		return Agent{}, Passport{}, fmt.Errorf("identity: load enrolled passport: %w", err)
	}
	if err := json.Unmarshal(repositoriesRaw, &passport.Repositories); err != nil {
		return Agent{}, Passport{}, fmt.Errorf("identity: unmarshal enrolled repositories: %w", err)
	}
	if err := json.Unmarshal(rolesRaw, &passport.Roles); err != nil {
		return Agent{}, Passport{}, fmt.Errorf("identity: unmarshal enrolled roles: %w", err)
	}
	return agent, passport, nil
}

func validRequestHash(hash string) bool {
	if len(hash) != sha256.Size*2 {
		return false
	}
	for _, r := range hash {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// RegisterInTx is the transaction-scoped core of Register. It creates the
// identity, passport, token, and audit entries but leaves commit/rollback to
// the caller so registration bootstrap records can share the same transaction.
func (s *Store) RegisterInTx(ctx context.Context, tx *sql.Tx, projectID string, permissions []string, owner, model string, capabilities, repositories, roles []string) (Agent, Passport, string, error) {
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
	tx, err := s.BeginProjectTx(ctx, projectID)
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
	if projectID == "" {
		return AuditEntry{}, ErrInvalidScope
	}
	tx, err := s.BeginProjectTx(ctx, projectID)
	if err != nil {
		return AuditEntry{}, err
	}
	defer tx.Rollback()

	entry, err := recordAction(ctx, tx, agentID, projectID, action)
	if err != nil {
		return AuditEntry{}, err
	}
	if err := tx.Commit(); err != nil {
		return AuditEntry{}, fmt.Errorf("identity: commit audit entry: %w", err)
	}
	return entry, nil
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
	if projectID == "" {
		return nil, ErrInvalidScope
	}
	tx, err := s.BeginProjectTx(ctx, projectID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx,
		`SELECT id, agent_id, project_id, action, created_at, seq
		 FROM audit_log
		 WHERE agent_id = $1 AND project_id = $2
		 ORDER BY seq ASC`,
		agentID, projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("identity: list audit trail: %w", err)
	}

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
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("identity: close audit trail rows: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("identity: commit audit trail read: %w", err)
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

	tx, err := s.BeginProjectTx(ctx, projectID)
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

	// passports is LEFT JOINed, not INNER JOINed: IssueToken (unlike
	// Register) issues a token for a project without requiring a passport
	// to already exist there (see TestWhoAmI_RejectsSameAgentTokenInDifferentProject),
	// so a token can legitimately resolve with no matching passport row.
	// p.roles is nil in that case and treated as empty below, not an error.
	query := `SELECT a.id, a.owner, a.model, a.capabilities, a.created_at, t.permissions, t.project_id, p.roles
		 FROM agents a
		 JOIN agent_tokens t ON t.agent_id = a.id
		 LEFT JOIN passports p ON p.agent_id = a.id AND p.project_id = t.project_id
		 WHERE t.token_hash = $1 AND t.expires_at > now()`
	args := []any{hash}
	if projectID != "" {
		query += ` AND t.project_id = $2`
		args = append(args, projectID)
	}

	var agent Agent
	var capsRaw []byte
	var permissionsRaw []byte
	var rolesRaw []byte
	var resolvedProjectID string
	err := s.db.QueryRowContext(ctx, query, args...).
		Scan(&agent.ID, &agent.Owner, &agent.Model, &capsRaw, &agent.CreatedAt, &permissionsRaw, &resolvedProjectID, &rolesRaw)
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
	var roles []string
	if rolesRaw != nil {
		if err := json.Unmarshal(rolesRaw, &roles); err != nil {
			return AuthenticatedScope{}, fmt.Errorf("identity: unmarshal roles: %w", err)
		}
	}

	return AuthenticatedScope{Agent: agent, ProjectID: resolvedProjectID, Permissions: permissions, Roles: roles}, nil
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
