package git

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

var (
	ErrActivityPolicyUnavailable = errors.New("git: activity policy unavailable")
	ErrActivityPolicyChanged     = errors.New("git: activity policy changed")
	ErrActivityNotFound          = errors.New("git: activity not found")
	ErrActivityReplayConflict    = errors.New("git: activity replay conflict")
	ErrActivityCursorConflict    = errors.New("git: activity cursor conflict")
	ErrActivityLifecycleConflict = errors.New("git: activity lifecycle conflict")
)

// ActivityPolicyChangedError returns the current canonical policy without
// reflecting request bytes or route details into the error string.
type ActivityPolicyChangedError struct {
	CurrentPolicyJSON []byte
	CurrentDigest     projectstate.Digest
}

func (e *ActivityPolicyChangedError) Error() string { return "git: accept activity: policy changed" }
func (e *ActivityPolicyChangedError) Unwrap() error { return ErrActivityPolicyChanged }

func validateFabricActivityStreamKey(key FabricActivityStreamKey) error {
	probe := types.ActivityRouteKey{
		ProjectID:        key.ProjectID,
		WorkspaceID:      types.WorkspaceID("00000000-0000-4000-8000-000000000001"),
		FabricInstanceID: key.FabricInstanceID,
		RemoteProjectID:  key.ProjectID,
		StreamID:         key.StreamID,
		CanonicalRef:     key.CanonicalRef,
	}
	if err := probe.Validate(); err != nil {
		return fmt.Errorf("git: activity route invalid: %w", ErrActivityNotFound)
	}
	return nil
}

func setActivityProject(ctx context.Context, tx *sql.Tx, projectID string) error {
	if _, err := tx.ExecContext(ctx, `SELECT set_config('wormhole.project_id',$1,true)`, projectID); err != nil {
		return fmt.Errorf("git: activity transaction: set project: %w", err)
	}
	return nil
}

func (s *ActivityStore) CurrentPolicy(ctx context.Context, key FabricActivityStreamKey) (projectstate.EffectiveActivityPolicyV1, error) {
	if s == nil || s.db == nil {
		return projectstate.EffectiveActivityPolicyV1{}, fmt.Errorf("git: current activity policy: %w", ErrActivityPolicyUnavailable)
	}
	if err := validateFabricActivityStreamKey(key); err != nil {
		return projectstate.EffectiveActivityPolicyV1{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return projectstate.EffectiveActivityPolicyV1{}, fmt.Errorf("git: current activity policy: begin: %w", err)
	}
	defer tx.Rollback()
	if err := setActivityProject(ctx, tx, key.ProjectID); err != nil {
		return projectstate.EffectiveActivityPolicyV1{}, err
	}
	policy, _, _, err := currentActivityPolicyTx(ctx, tx, key)
	if err != nil {
		return projectstate.EffectiveActivityPolicyV1{}, err
	}
	if err := tx.Commit(); err != nil {
		return projectstate.EffectiveActivityPolicyV1{}, fmt.Errorf("git: current activity policy: commit: %w", err)
	}
	return policy, nil
}

func currentActivityPolicyTx(ctx context.Context, tx *sql.Tx, key FabricActivityStreamKey) (projectstate.EffectiveActivityPolicyV1, []byte, projectstate.Digest, error) {
	var raw []byte
	var storedDigest string
	err := tx.QueryRowContext(ctx, `SELECT v.canonical_policy_json,v.policy_digest
		FROM fabric_activity_policy_current c
		JOIN fabric_activity_policy_versions v
		USING(project_id,fabric_instance_id,stream_id,canonical_ref,policy_version)
		WHERE c.project_id=$1 AND c.fabric_instance_id=$2 AND c.stream_id=$3 AND c.canonical_ref=$4`,
		key.ProjectID, key.FabricInstanceID, key.StreamID, key.CanonicalRef).Scan(&raw, &storedDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return projectstate.EffectiveActivityPolicyV1{}, nil, "", fmt.Errorf("git: current activity policy: %w", ErrActivityPolicyUnavailable)
	}
	if err != nil {
		return projectstate.EffectiveActivityPolicyV1{}, nil, "", fmt.Errorf("git: current activity policy: read: %w", err)
	}
	policy, err := projectstate.DecodeActivityPolicy(raw)
	if err != nil {
		return projectstate.EffectiveActivityPolicyV1{}, nil, "", fmt.Errorf("git: current activity policy: %w", ErrActivityPolicyUnavailable)
	}
	digest, err := projectstate.DigestActivityPolicy(policy)
	if err != nil || string(digest) != storedDigest {
		return projectstate.EffectiveActivityPolicyV1{}, nil, "", fmt.Errorf("git: current activity policy: %w", ErrActivityPolicyUnavailable)
	}
	return policy, append([]byte(nil), raw...), digest, nil
}

func (s *ActivityStore) PublishPolicy(ctx context.Context, key FabricActivityStreamKey, policy projectstate.EffectiveActivityPolicyV1) (projectstate.EffectiveActivityPolicyV1, error) {
	if s == nil || s.db == nil {
		return projectstate.EffectiveActivityPolicyV1{}, fmt.Errorf("git: publish activity policy: %w", ErrActivityPolicyUnavailable)
	}
	if err := validateFabricActivityStreamKey(key); err != nil {
		return projectstate.EffectiveActivityPolicyV1{}, err
	}
	canonical, err := projectstate.CanonicalActivityPolicy(policy)
	if err != nil {
		return projectstate.EffectiveActivityPolicyV1{}, err
	}
	digest, err := projectstate.DigestActivityPolicy(policy)
	if err != nil {
		return projectstate.EffectiveActivityPolicyV1{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return projectstate.EffectiveActivityPolicyV1{}, fmt.Errorf("git: publish activity policy: begin: %w", err)
	}
	defer tx.Rollback()
	if err := setActivityProject(ctx, tx, key.ProjectID); err != nil {
		return projectstate.EffectiveActivityPolicyV1{}, err
	}
	var returnedJSON []byte
	var returnedDigest string
	var returnedVersion int64
	err = tx.QueryRowContext(ctx, `SELECT canonical_policy_json,policy_digest,policy_version
		FROM fabric_publish_activity_policy_v1($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		key.ProjectID, key.FabricInstanceID, key.StreamID, key.CanonicalRef,
		policy.PolicyVersion, canonical, string(digest), policy.SchemaVersion,
		policy.OrdinaryMaxAgeSeconds, policy.OrdinaryMaxRows, policy.TerminalDefaultAgeSeconds,
		policy.TerminalMaximumAgeSeconds, policy.TerminalRetentionSeconds).
		Scan(&returnedJSON, &returnedDigest, &returnedVersion)
	if err != nil {
		_ = tx.Rollback()
		if activityDatabaseMessage(err) == "activity policy conflict" {
			return projectstate.EffectiveActivityPolicyV1{}, fmt.Errorf("git: publish activity policy: %w", ErrActivityPolicyChanged)
		}
		if activityDatabaseMessage(err) == "activity stream unavailable" {
			return projectstate.EffectiveActivityPolicyV1{}, fmt.Errorf("git: publish activity policy: %w", ErrActivityNotFound)
		}
		return projectstate.EffectiveActivityPolicyV1{}, fmt.Errorf("git: publish activity policy: database: %w", err)
	}
	if returnedVersion != policy.PolicyVersion || returnedDigest != string(digest) || !bytes.Equal(returnedJSON, canonical) {
		return projectstate.EffectiveActivityPolicyV1{}, fmt.Errorf("git: publish activity policy: %w", ErrActivityPolicyChanged)
	}
	decoded, err := projectstate.DecodeActivityPolicy(returnedJSON)
	if err != nil {
		return projectstate.EffectiveActivityPolicyV1{}, fmt.Errorf("git: publish activity policy: invalid response: %w", ErrActivityPolicyChanged)
	}
	if err := tx.Commit(); err != nil {
		return projectstate.EffectiveActivityPolicyV1{}, fmt.Errorf("git: publish activity policy: commit: %w", err)
	}
	return decoded, nil
}
