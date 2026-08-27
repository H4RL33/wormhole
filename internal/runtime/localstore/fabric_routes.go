package localstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
)

// FabricRouteRepo persists machine-private Fabric profiles and exact workspace routes.
type FabricRouteRepo struct {
	db *sql.DB
}

func NewFabricRouteRepo(db *sql.DB) *FabricRouteRepo {
	return &FabricRouteRepo{db: db}
}

func (r *FabricRouteRepo) CreateProfile(ctx context.Context, profile types.FabricProfile) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("localstore: create Fabric profile: unavailable repository")
	}
	if err := profile.Validate(); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO fabric_profiles
		(profile_id,alias,fabric_instance_id,base_url,mode,credential_ref)
		VALUES (?,?,?,?,?,?)
	`, profile.ProfileID, profile.Alias, profile.FabricInstanceID, profile.BaseURL, profile.Mode, profile.CredentialRef)
	if err != nil {
		return fmt.Errorf("localstore: create Fabric profile: %w", err)
	}
	return nil
}

func (r *FabricRouteRepo) UpdateProfile(ctx context.Context, profileID, expectedInstanceID, baseURL, credentialRef string) error {
	profile, err := r.GetProfile(ctx, profileID)
	if err != nil {
		return err
	}
	if profile.FabricInstanceID != expectedInstanceID {
		return ErrNotFound
	}
	profile.BaseURL = baseURL
	profile.CredentialRef = credentialRef
	if err := profile.Validate(); err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE fabric_profiles SET base_url=?,credential_ref=?,updated_at=?
		WHERE profile_id=? AND fabric_instance_id=?
	`, baseURL, credentialRef, time.Now().UTC(), profileID, expectedInstanceID)
	if err != nil {
		return fmt.Errorf("localstore: update Fabric profile: %w", err)
	}
	return requireFabricRows(result, "update profile")
}

func (r *FabricRouteRepo) GetProfile(ctx context.Context, profileID string) (types.FabricProfile, error) {
	if r == nil || r.db == nil || !types.CanonicalUUID(profileID) {
		return types.FabricProfile{}, ErrNotFound
	}
	return scanFabricProfile(r.db.QueryRowContext(ctx, `
		SELECT profile_id,alias,fabric_instance_id,base_url,mode,credential_ref
		FROM fabric_profiles WHERE profile_id=?
	`, profileID))
}

func (r *FabricRouteRepo) ListProfiles(ctx context.Context) ([]types.FabricProfile, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("localstore: list Fabric profiles: unavailable repository")
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT profile_id,alias,fabric_instance_id,base_url,mode,credential_ref
		FROM fabric_profiles ORDER BY profile_id
	`)
	if err != nil {
		return nil, fmt.Errorf("localstore: list Fabric profiles: %w", err)
	}
	defer rows.Close()
	profiles := make([]types.FabricProfile, 0)
	for rows.Next() {
		profile, err := scanFabricProfile(rows)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("localstore: iterate Fabric profiles: %w", err)
	}
	return profiles, nil
}

func (r *FabricRouteRepo) AttachWorkspace(ctx context.Context, binding types.FabricBinding) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("localstore: attach Fabric workspace: unavailable repository")
	}
	return NewWorkspaceRepo(r.db).WithImmediateWorkspace(ctx, binding.Workspace.Scope, func(tx *WorkspaceMutationTx) error {
		workspace, err := tx.Workspace(ctx)
		if err != nil {
			return err
		}
		profile, err := scanFabricProfile(tx.conn.QueryRowContext(ctx, `
			SELECT profile_id,alias,fabric_instance_id,base_url,mode,credential_ref
			FROM fabric_profiles WHERE profile_id=?
		`, binding.ProfileID))
		if err != nil {
			return err
		}
		if workspace.Binding != binding.Workspace {
			return fmt.Errorf("%w: workspace binding mismatch", types.ErrInvalidFabricRoute)
		}
		if err := binding.ValidateWithProfile(profile); err != nil {
			return err
		}
		_, err = tx.conn.ExecContext(ctx, `
			INSERT INTO workspace_fabric_bindings
			(project_id,workspace_id,profile_id,fabric_instance_id,remote_project_id,stream_id,
			 attachment_ref,repository_provider,repository_immutable_id,canonical_ref,writable,state)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,'active')
		`, binding.Workspace.Scope.ProjectID, binding.Workspace.Scope.WorkspaceID,
			binding.ProfileID, binding.FabricInstanceID, binding.RemoteProjectID, binding.StreamID,
			binding.AttachmentRef, binding.Workspace.Repository.Provider, binding.Workspace.Repository.ImmutableID,
			binding.CanonicalRef, binding.Writable)
		if err != nil {
			return fmt.Errorf("localstore: attach Fabric workspace: %w", err)
		}
		_, err = tx.conn.ExecContext(ctx, `
			INSERT INTO fabric_cursors
			(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,stream_version,pull_cursor)
			VALUES (?,?,?,?,?,?,0,'')
		`, binding.RemoteKey().ProjectID, binding.RemoteKey().WorkspaceID, binding.RemoteKey().FabricInstanceID,
			binding.RemoteKey().RemoteProjectID, binding.RemoteKey().StreamID, binding.CanonicalRef)
		if err != nil {
			return fmt.Errorf("localstore: initialize Fabric cursor: %w", err)
		}
		_, err = tx.conn.ExecContext(ctx, `
			INSERT INTO activity_cursors
			(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,after_sequence,updated_at)
			VALUES (?,?,?,?,?,?,0,CURRENT_TIMESTAMP)
		`, binding.Workspace.Scope.ProjectID, binding.Workspace.Scope.WorkspaceID, binding.FabricInstanceID,
			binding.RemoteProjectID, binding.StreamID, binding.CanonicalRef)
		if err != nil {
			return fmt.Errorf("localstore: initialize Activity cursor: %w", err)
		}
		return nil
	})
}

func (r *FabricRouteRepo) DetachWorkspace(ctx context.Context, scope types.WorkspaceScope, expectedInstanceID string) error {
	if r == nil || r.db == nil || !validWorkspaceScope(scope) || !types.CanonicalUUID(expectedInstanceID) {
		return ErrNotFound
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE workspace_fabric_bindings
		SET writable=0,state='detached',detached_at=?
		WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND state='active'
	`, time.Now().UTC(), scope.ProjectID, scope.WorkspaceID, expectedInstanceID)
	if err != nil {
		return fmt.Errorf("localstore: detach Fabric workspace: %w", err)
	}
	return requireFabricRows(result, "detach workspace")
}

func (r *FabricRouteRepo) GetBinding(ctx context.Context, scope types.WorkspaceScope) (types.FabricBinding, error) {
	if r == nil || r.db == nil || !validWorkspaceScope(scope) {
		return types.FabricBinding{}, ErrNotFound
	}
	return r.scanFabricBinding(ctx, scope, false)
}

func (r *FabricRouteRepo) GetRoute(ctx context.Context, scope types.WorkspaceScope) (types.FabricBinding, types.FabricProfile, error) {
	if r == nil || r.db == nil || !validWorkspaceScope(scope) {
		return types.FabricBinding{}, types.FabricProfile{}, ErrNotFound
	}
	binding, err := r.scanFabricBinding(ctx, scope, true)
	if err != nil {
		return types.FabricBinding{}, types.FabricProfile{}, err
	}
	profile, err := r.GetProfile(ctx, binding.ProfileID)
	if err != nil {
		return types.FabricBinding{}, types.FabricProfile{}, err
	}
	if err := binding.ValidateWithProfile(profile); err != nil {
		return types.FabricBinding{}, types.FabricProfile{}, fmt.Errorf("localstore: validate Fabric route: %w", err)
	}
	return binding, profile, nil
}

func (r *FabricRouteRepo) UpdateCursor(ctx context.Context, key types.RemoteBindingKey, streamVersion int64, pullCursor string) error {
	if r == nil || r.db == nil || key.Validate() != nil || streamVersion < 0 {
		return ErrNotFound
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE fabric_cursors SET stream_version=?,pull_cursor=?,updated_at=?
		WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=? AND stream_id=?
	`, streamVersion, pullCursor, time.Now().UTC(), key.ProjectID, key.WorkspaceID,
		key.FabricInstanceID, key.RemoteProjectID, key.StreamID)
	if err != nil {
		return fmt.Errorf("localstore: update Fabric cursor: %w", err)
	}
	return requireFabricRows(result, "update cursor")
}

func (r *FabricRouteRepo) scanFabricBinding(ctx context.Context, scope types.WorkspaceScope, activeOnly bool) (types.FabricBinding, error) {
	workspace, err := NewWorkspaceRepo(r.db).Workspace(ctx, scope)
	if err != nil {
		return types.FabricBinding{}, err
	}
	query := `SELECT profile_id,fabric_instance_id,remote_project_id,stream_id,attachment_ref,
		repository_provider,repository_immutable_id,canonical_ref,writable
		FROM workspace_fabric_bindings WHERE project_id=? AND workspace_id=?`
	if activeOnly {
		query += ` AND state='active'`
	}
	var binding types.FabricBinding
	var provider, immutableID string
	if err := r.db.QueryRowContext(ctx, query, scope.ProjectID, scope.WorkspaceID).Scan(
		&binding.ProfileID, &binding.FabricInstanceID, &binding.RemoteProjectID, &binding.StreamID,
		&binding.AttachmentRef, &provider, &immutableID, &binding.CanonicalRef, &binding.Writable,
	); errors.Is(err, sql.ErrNoRows) {
		return types.FabricBinding{}, ErrNotFound
	} else if err != nil {
		return types.FabricBinding{}, fmt.Errorf("localstore: read Fabric binding: %w", err)
	}
	if workspace.Binding.Repository.Provider != provider || workspace.Binding.Repository.ImmutableID != immutableID {
		return types.FabricBinding{}, fmt.Errorf("localstore: Fabric binding repository mismatch")
	}
	binding.Workspace = workspace.Binding
	return binding, nil
}

type fabricProfileScanner interface {
	Scan(...any) error
}

func scanFabricProfile(row fabricProfileScanner) (types.FabricProfile, error) {
	var profile types.FabricProfile
	if err := row.Scan(&profile.ProfileID, &profile.Alias, &profile.FabricInstanceID, &profile.BaseURL, &profile.Mode, &profile.CredentialRef); errors.Is(err, sql.ErrNoRows) {
		return types.FabricProfile{}, ErrNotFound
	} else if err != nil {
		return types.FabricProfile{}, fmt.Errorf("localstore: read Fabric profile: %w", err)
	}
	if err := profile.Validate(); err != nil {
		return types.FabricProfile{}, fmt.Errorf("localstore: validate Fabric profile: %w", err)
	}
	return profile, nil
}

func requireFabricRows(result sql.Result, action string) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("localstore: %s rows affected: %w", action, err)
	}
	if rows != 1 {
		return ErrNotFound
	}
	return nil
}
