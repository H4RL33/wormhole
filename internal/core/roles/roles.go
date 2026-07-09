// Package roles implements pre-defined role template storage and retrieval
// (RFC-0001 §8.1). Roles are immutable templates seeded at migration time,
// containing permission bundles and default task views for common team roles.
// This package stays isolated per architecture.md R2: it does not import
// internal/core/tasks, internal/core/events, or internal/core/kb.
package roles

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrTemplateNotFound = errors.New("roles: template not found")

type Template struct {
	Name             string
	PermissionBundle []string
	DefaultTaskView  json.RawMessage
	CreatedAt        time.Time
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// GetTemplate retrieves a single role template by name. Returns
// ErrTemplateNotFound if no template with that name exists.
func (s *Store) GetTemplate(ctx context.Context, name string) (Template, error) {
	var t Template
	var permBundle []byte
	var defaultView []byte

	err := s.db.QueryRowContext(
		ctx,
		`SELECT name, permission_bundle, default_task_view, created_at
		 FROM role_templates
		 WHERE name = $1`,
		name,
	).Scan(&t.Name, &permBundle, &defaultView, &t.CreatedAt)

	if err == sql.ErrNoRows {
		return Template{}, ErrTemplateNotFound
	}
	if err != nil {
		return Template{}, fmt.Errorf("roles: get template: %w", err)
	}

	// Unmarshal permission bundle from JSONB.
	if err := json.Unmarshal(permBundle, &t.PermissionBundle); err != nil {
		return Template{}, fmt.Errorf("roles: unmarshal permission bundle: %w", err)
	}

	// DefaultTaskView remains raw JSON to match the spec (free-form JSONB,
	// no Go-side struct validation).
	t.DefaultTaskView = json.RawMessage(defaultView)

	return t, nil
}

// ListTemplates returns all role templates, ordered by name ascending
// for deterministic ordering in tests and output.
func (s *Store) ListTemplates(ctx context.Context) ([]Template, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT name, permission_bundle, default_task_view, created_at
		 FROM role_templates
		 ORDER BY name ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("roles: list templates: %w", err)
	}
	defer rows.Close()

	var templates []Template
	for rows.Next() {
		var t Template
		var permBundle []byte
		var defaultView []byte

		if err := rows.Scan(&t.Name, &permBundle, &defaultView, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("roles: scan row: %w", err)
		}

		// Unmarshal permission bundle.
		if err := json.Unmarshal(permBundle, &t.PermissionBundle); err != nil {
			return nil, fmt.Errorf("roles: unmarshal permission bundle: %w", err)
		}

		// DefaultTaskView remains raw.
		t.DefaultTaskView = json.RawMessage(defaultView)

		templates = append(templates, t)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("roles: iterate rows: %w", err)
	}

	return templates, nil
}
