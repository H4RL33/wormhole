package identity

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
)

// ErrInvalidViewerKey is returned by ResolveViewerKey for any key that doesn't
// match a stored hash for the requested project — forged, unknown, or a real
// key presented against a project it wasn't issued for all collapse to this
// one error (docs/implementation-rules.md §5: security-relevant lookups must not let
// a caller distinguish failure modes).
var ErrInvalidViewerKey = errors.New("identity: invalid viewer key")

// CreateViewerKey issues a new project-scoped read-only viewer key. The raw
// key is returned exactly once; only its SHA-256 hash is persisted.
func (s *Store) CreateViewerKey(ctx context.Context, projectID, label string) (rawKey string, id string, err error) {
	rawKey, keyHash, err := generateToken()
	if err != nil {
		return "", "", err
	}

	row := s.db.QueryRowContext(ctx,
		`INSERT INTO viewer_keys (project_id, label, key_hash) VALUES ($1, $2, $3) RETURNING id`,
		projectID, label, keyHash,
	)
	var newID string
	if err := row.Scan(&newID); err != nil {
		return "", "", fmt.Errorf("identity: create viewer key: %w", err)
	}
	return rawKey, newID, nil
}

// ResolveViewerKey resolves a raw viewer key to the project it grants
// read-only access to. Returns ErrInvalidViewerKey if the key doesn't match
// any stored hash, or if projectID is non-empty and doesn't match the key's
// own project (cross-tenant isolation, same principle as identity.WhoAmI).
func (s *Store) ResolveViewerKey(ctx context.Context, projectID, rawKey string) (resolvedProjectID string, err error) {
	if rawKey == "" {
		return "", ErrInvalidViewerKey
	}
	sum := sha256.Sum256([]byte(rawKey))
	hash := hex.EncodeToString(sum[:])

	var gotProjectID string
	err = s.db.QueryRowContext(ctx,
		`SELECT project_id FROM viewer_keys WHERE key_hash = $1`,
		hash,
	).Scan(&gotProjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrInvalidViewerKey
	}
	if err != nil {
		return "", fmt.Errorf("identity: resolve viewer key: %w", err)
	}
	if projectID != "" && gotProjectID != projectID {
		return "", ErrInvalidViewerKey
	}
	return gotProjectID, nil
}
