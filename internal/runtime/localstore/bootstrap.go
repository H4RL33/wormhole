package localstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
)

// ValidateReadyCheckpoint verifies that credentials select the same durable
// identity committed with a complete bootstrap snapshot. It performs no
// network access and is therefore safe on every Gateway restart.
func (s *Store) ValidateReadyCheckpoint(ctx context.Context, namespaceID, agentID, passportID, credentialProfile string) error {
	if namespaceID == "" || agentID == "" || passportID == "" || credentialProfile == "" {
		return ErrNotFound
	}
	var schemaVersion int
	err := s.db.QueryRowContext(ctx, `
		SELECT bm.schema_version
		FROM bootstrap_metadata bm
		JOIN projects p ON p.namespace_id = bm.namespace_id AND p.id = bm.namespace_id
		JOIN agents a ON a.namespace_id = bm.namespace_id AND a.id = ?
		JOIN passports pp ON pp.namespace_id = bm.namespace_id AND pp.id = ?
			AND pp.agent_id = a.id AND pp.project_id = bm.namespace_id
		JOIN auth_scopes scope ON scope.namespace_id = bm.namespace_id
			AND scope.agent_id = a.id AND scope.passport_id = pp.id
		JOIN whoami_cache wc ON wc.agent_id = a.id AND wc.project_id = bm.namespace_id
		JOIN enrolment_attempts attempt ON attempt.project_id = bm.namespace_id
			AND attempt.agent_id = a.id AND attempt.passport_id = pp.id
			AND attempt.credential_profile = ?
			AND attempt.state = 'ready' AND attempt.terminal = 1
		WHERE bm.namespace_id = ?`, agentID, passportID, credentialProfile, namespaceID).Scan(&schemaVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("localstore: validate ready checkpoint: %w", err)
	}
	if schemaVersion != types.BootstrapSchemaVersionV1 {
		return fmt.Errorf("localstore: validate ready checkpoint: unsupported schema version %d", schemaVersion)
	}
	return nil
}

// ApplyBootstrap commits one already-validated complete Fabric snapshot and
// the ready enrolment checkpoint in a single namespace-scoped SQLite
// transaction. Callers record recovery_required separately after rollback.
func (s *Store) ApplyBootstrap(ctx context.Context, namespaceID string, snapshot types.BootstrapOrgConfigV1, timestamp time.Time, attempt *EnrolmentAttemptRecord) error {
	if namespaceID == "" || snapshot.Project.ID != namespaceID || snapshot.Identity.Passport.ProjectID != namespaceID {
		return fmt.Errorf("localstore: apply bootstrap: namespace mismatch")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("localstore: apply bootstrap: begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO projects (namespace_id, id, name, owner, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(namespace_id) DO UPDATE SET id=excluded.id, name=excluded.name, owner=excluded.owner, created_at=excluded.created_at`,
		namespaceID, snapshot.Project.ID, snapshot.Project.Name, snapshot.Project.Owner, snapshot.Project.CreatedAt.UTC()); err != nil {
		return fmt.Errorf("localstore: apply bootstrap project: %w", err)
	}

	capabilities, err := json.Marshal(snapshot.Identity.Agent.Capabilities)
	if err != nil {
		return fmt.Errorf("localstore: apply bootstrap capabilities: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agents (namespace_id, id, owner, model, capabilities, created_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(namespace_id, id) DO UPDATE SET owner=excluded.owner, model=excluded.model, capabilities=excluded.capabilities, created_at=excluded.created_at`,
		namespaceID, snapshot.Identity.Agent.ID, snapshot.Identity.Agent.Owner, snapshot.Identity.Agent.Model, string(capabilities), snapshot.Identity.Agent.CreatedAt.UTC()); err != nil {
		return fmt.Errorf("localstore: apply bootstrap agent: %w", err)
	}
	repositories, err := json.Marshal(snapshot.Identity.Passport.Repositories)
	if err != nil {
		return fmt.Errorf("localstore: apply bootstrap repositories: %w", err)
	}
	roles, err := json.Marshal(snapshot.Identity.Passport.Roles)
	if err != nil {
		return fmt.Errorf("localstore: apply bootstrap roles: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO passports (namespace_id, id, agent_id, project_id, repositories, roles, issued_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(namespace_id, id) DO UPDATE SET agent_id=excluded.agent_id, project_id=excluded.project_id, repositories=excluded.repositories, roles=excluded.roles, issued_at=excluded.issued_at`,
		namespaceID, snapshot.Identity.Passport.ID, snapshot.Identity.Passport.AgentID, snapshot.Identity.Passport.ProjectID,
		string(repositories), string(roles), snapshot.Identity.Passport.IssuedAt.UTC()); err != nil {
		return fmt.Errorf("localstore: apply bootstrap passport: %w", err)
	}
	permissions, err := json.Marshal(snapshot.Identity.Permissions)
	if err != nil {
		return fmt.Errorf("localstore: apply bootstrap permissions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO auth_scopes (namespace_id, agent_id, passport_id, permissions) VALUES (?, ?, ?, ?)
		ON CONFLICT(namespace_id, agent_id, passport_id) DO UPDATE SET permissions=excluded.permissions`,
		namespaceID, snapshot.Identity.Agent.ID, snapshot.Identity.Passport.ID, string(permissions)); err != nil {
		return fmt.Errorf("localstore: apply bootstrap auth scope: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO whoami_cache (agent_id, owner, model, capabilities, project_id, permissions, cached_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent_id, project_id) DO UPDATE SET owner=excluded.owner, model=excluded.model, capabilities=excluded.capabilities, permissions=excluded.permissions, cached_at=excluded.cached_at`,
		snapshot.Identity.Agent.ID, snapshot.Identity.Agent.Owner, snapshot.Identity.Agent.Model, string(capabilities), namespaceID, string(permissions), timestamp.UTC()); err != nil {
		return fmt.Errorf("localstore: apply bootstrap authorization cache: %w", err)
	}

	for _, channel := range snapshot.Channels {
		if err := rejectNamespaceCollision(ctx, tx, "channels", channel.ID, namespaceID); err != nil {
			return fmt.Errorf("localstore: apply bootstrap channel %q: %w", channel.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO channels (id, namespace_id, name, created_at) VALUES (?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET name=excluded.name, created_at=excluded.created_at`,
			channel.ID, namespaceID, channel.Name, channel.CreatedAt.UTC()); err != nil {
			return fmt.Errorf("localstore: apply bootstrap channel %q: %w", channel.ID, err)
		}
	}
	for _, task := range snapshot.Tasks {
		if err := rejectNamespaceCollision(ctx, tx, "tasks", task.ID, namespaceID); err != nil {
			return fmt.Errorf("localstore: apply bootstrap task %q: %w", task.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO tasks (id, namespace_id, parent_task_id, title, description, owner_agent_id, status, priority, due_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET parent_task_id=excluded.parent_task_id, title=excluded.title, description=excluded.description,
			owner_agent_id=excluded.owner_agent_id, status=excluded.status, priority=excluded.priority, due_by=excluded.due_by,
			created_at=excluded.created_at, updated_at=excluded.updated_at`, task.ID, namespaceID, task.ParentTaskID, task.Title,
			task.Description, task.OwnerAgentID, task.Status, task.Priority, task.DueBy, task.CreatedAt.UTC(), task.UpdatedAt.UTC()); err != nil {
			return fmt.Errorf("localstore: apply bootstrap task %q: %w", task.ID, err)
		}
	}
	for _, article := range snapshot.KB.Articles {
		if err := rejectNamespaceCollision(ctx, tx, "kb_articles", article.ID, namespaceID); err != nil {
			return fmt.Errorf("localstore: apply bootstrap article %q: %w", article.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO kb_articles (id, namespace_id, title, body, frontmatter, author_agent_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET title=excluded.title, body=excluded.body, frontmatter=excluded.frontmatter,
			author_agent_id=excluded.author_agent_id, created_at=excluded.created_at, updated_at=excluded.updated_at`,
			article.ID, namespaceID, article.Title, article.Body, string(article.Frontmatter), article.AuthorAgentID, article.CreatedAt.UTC(), article.UpdatedAt.UTC()); err != nil {
			return fmt.Errorf("localstore: apply bootstrap article %q: %w", article.ID, err)
		}
	}
	for _, event := range snapshot.Events {
		if err := rejectNamespaceCollision(ctx, tx, "events", event.ID, namespaceID); err != nil {
			return fmt.Errorf("localstore: apply bootstrap event %q: %w", event.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO events (id, namespace_id, channel_id, agent_id, event_type, payload, note, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET channel_id=excluded.channel_id, agent_id=excluded.agent_id, event_type=excluded.event_type,
			payload=excluded.payload, note=excluded.note, created_at=excluded.created_at`, event.ID, namespaceID, event.ChannelID,
			event.AgentID, event.EventType, string(event.Payload), event.Note, event.CreatedAt.UTC()); err != nil {
			return fmt.Errorf("localstore: apply bootstrap event %q: %w", event.ID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO bootstrap_metadata (namespace_id, schema_version, integration_manifest_metadata, bootstrap_timestamp)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(namespace_id) DO UPDATE SET schema_version=excluded.schema_version,
		integration_manifest_metadata=excluded.integration_manifest_metadata, bootstrap_timestamp=excluded.bootstrap_timestamp`,
		namespaceID, snapshot.SchemaVersion, string(snapshot.IntegrationManifestMetadata), timestamp.UTC()); err != nil {
		return fmt.Errorf("localstore: apply bootstrap metadata: %w", err)
	}
	if attempt != nil {
		if attempt.ProjectID != namespaceID || attempt.AgentID != snapshot.Identity.Agent.ID || attempt.PassportID != snapshot.Identity.Passport.ID {
			return fmt.Errorf("localstore: apply bootstrap: enrolment attempt identity mismatch")
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE enrolment_attempts SET state = 'ready', terminal = 1, updated_at = CURRENT_TIMESTAMP
			WHERE project_id = ? AND idempotency_key = ? AND request_hash = ? AND credential_profile = ?
			  AND agent_id = ? AND passport_id = ?`, attempt.ProjectID, attempt.IdempotencyKey, attempt.RequestHash,
			attempt.CredentialProfile, attempt.AgentID, attempt.PassportID)
		if err != nil {
			return fmt.Errorf("localstore: apply bootstrap ready checkpoint: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("localstore: apply bootstrap ready checkpoint rows: %w", err)
		}
		if rows != 1 {
			return ErrNotFound
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("localstore: apply bootstrap: commit: %w", err)
	}
	return nil
}

func rejectNamespaceCollision(ctx context.Context, tx *sql.Tx, table, id, namespaceID string) error {
	var found int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM `+table+` WHERE id = ? AND namespace_id <> ?`, id, namespaceID).Scan(&found)
	if err == nil {
		return ErrNamespaceCollision
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}
