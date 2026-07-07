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
)

// ErrInvalidToken is returned by WhoAmI when the provided token doesn't
// resolve to any agent — forged, tampered, or unknown tokens all collapse
// to this single error so callers can't distinguish "wrong token" from
// "token for a different agent" (RFC-0001 §13: identities must be
// unforgeable within scope).
var ErrInvalidToken = errors.New("identity: invalid token")

type Agent struct {
	ID           string
	Owner        string
	Model        string
	Capabilities []string
	CreatedAt    time.Time
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Register creates a new agent identity and issues a bearer token for it.
// The raw token is returned exactly once — only its SHA-256 hash is
// persisted, so the raw value can never be recovered from storage.
func (s *Store) Register(ctx context.Context, owner, model string, capabilities []string) (Agent, string, error) {
	if capabilities == nil {
		capabilities = []string{}
	}
	capsJSON, err := json.Marshal(capabilities)
	if err != nil {
		return Agent{}, "", fmt.Errorf("identity: marshal capabilities: %w", err)
	}

	rawToken, tokenHash, err := generateToken()
	if err != nil {
		return Agent{}, "", err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Agent{}, "", fmt.Errorf("identity: begin tx: %w", err)
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
		return Agent{}, "", fmt.Errorf("identity: insert agent: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO agent_tokens (agent_id, token_hash) VALUES ($1, $2)`,
		agent.ID, tokenHash,
	); err != nil {
		return Agent{}, "", fmt.Errorf("identity: insert token: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Agent{}, "", fmt.Errorf("identity: commit: %w", err)
	}

	if err := json.Unmarshal(capsRaw, &agent.Capabilities); err != nil {
		return Agent{}, "", fmt.Errorf("identity: unmarshal capabilities: %w", err)
	}

	return agent, rawToken, nil
}

// WhoAmI resolves a raw bearer token to the agent identity that owns it.
// Returns ErrInvalidToken for any token that doesn't match a stored hash —
// forged, expired-format, or simply unknown.
func (s *Store) WhoAmI(ctx context.Context, rawToken string) (Agent, error) {
	if rawToken == "" {
		return Agent{}, ErrInvalidToken
	}

	sum := sha256.Sum256([]byte(rawToken))
	hash := hex.EncodeToString(sum[:])

	var agent Agent
	var capsRaw []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT a.id, a.owner, a.model, a.capabilities, a.created_at
		 FROM agents a
		 JOIN agent_tokens t ON t.agent_id = a.id
		 WHERE t.token_hash = $1`,
		hash,
	).Scan(&agent.ID, &agent.Owner, &agent.Model, &capsRaw, &agent.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, ErrInvalidToken
	}
	if err != nil {
		return Agent{}, fmt.Errorf("identity: whoami query: %w", err)
	}

	if err := json.Unmarshal(capsRaw, &agent.Capabilities); err != nil {
		return Agent{}, fmt.Errorf("identity: unmarshal capabilities: %w", err)
	}

	return agent, nil
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
