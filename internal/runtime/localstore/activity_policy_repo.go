package localstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

func (r *ActivityRepo) CurrentPolicy(ctx context.Context, route types.ActivityRouteKey) (ActivityPolicyRecord, error) {
	if r == nil || r.db == nil {
		return ActivityPolicyRecord{}, fmt.Errorf("localstore: current Activity policy: %w", ErrActivityPolicyUnavailable)
	}
	if err := validateActivityRoute(route); err != nil {
		return ActivityPolicyRecord{}, err
	}
	if err := requireActiveActivityRoute(ctx, r.db, route); err != nil {
		return ActivityPolicyRecord{}, err
	}
	return currentActivityPolicy(ctx, r.db, route)
}

func currentActivityPolicy(ctx context.Context, db activityDB, route types.ActivityRouteKey) (ActivityPolicyRecord, error) {
	var raw []byte
	var storedDigest string
	var receivedAt sql.NullTime
	err := db.QueryRowContext(ctx, `SELECT v.canonical_policy_json,v.policy_digest,v.received_at
		FROM activity_policy_current c JOIN activity_policy_versions v
		USING(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,policy_version,policy_digest)
		WHERE c.project_id=? AND c.workspace_id=? AND c.fabric_instance_id=? AND c.remote_project_id=?
		AND c.stream_id=? AND c.canonical_ref=?`, activityRouteArgs(route)...).Scan(&raw, &storedDigest, &receivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ActivityPolicyRecord{}, fmt.Errorf("localstore: current Activity policy: %w", ErrActivityPolicyUnavailable)
	}
	if err != nil || !receivedAt.Valid {
		if err == nil {
			err = ErrActivityPolicyUnavailable
		}
		return ActivityPolicyRecord{}, fmt.Errorf("localstore: current Activity policy: %w", err)
	}
	policy, err := projectstate.DecodeActivityPolicy(raw)
	if err != nil {
		return ActivityPolicyRecord{}, fmt.Errorf("localstore: current Activity policy: %w", ErrActivityPolicyUnavailable)
	}
	digest, err := projectstate.DigestActivityPolicy(policy)
	if err != nil || string(digest) != storedDigest {
		return ActivityPolicyRecord{}, fmt.Errorf("localstore: current Activity policy: %w", ErrActivityPolicyUnavailable)
	}
	return ActivityPolicyRecord{
		Route: route, Policy: policy, PolicyJSON: append([]byte(nil), raw...),
		PolicyDigest: digest, ReceivedAt: receivedAt.Time.UTC(),
	}, nil
}

func activityPolicyVersion(ctx context.Context, db activityDB, route types.ActivityRouteKey, version int64, digest projectstate.Digest) (ActivityPolicyRecord, error) {
	if version < 1 || version > maximumActivityInteger || !activityDigestPattern.MatchString(string(digest)) {
		return ActivityPolicyRecord{}, fmt.Errorf("localstore: Activity policy version: %w", ErrActivityPolicyUnavailable)
	}
	var raw []byte
	var retention int64
	var receivedAt time.Time
	arguments := activityRouteArgs(route)
	arguments = append(arguments, version, string(digest))
	err := db.QueryRowContext(ctx, `SELECT canonical_policy_json,terminal_retention_seconds,received_at
		FROM activity_policy_versions WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=?
		AND stream_id=? AND canonical_ref=? AND policy_version=? AND policy_digest=?`, arguments...).Scan(&raw, &retention, &receivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ActivityPolicyRecord{}, fmt.Errorf("localstore: Activity policy version: %w", ErrActivityPolicyUnavailable)
	}
	if err != nil {
		return ActivityPolicyRecord{}, fmt.Errorf("localstore: Activity policy version: %w", err)
	}
	policy, err := projectstate.DecodeActivityPolicy(raw)
	if err != nil || policy.PolicyVersion != version || policy.TerminalRetentionSeconds != retention {
		return ActivityPolicyRecord{}, fmt.Errorf("localstore: Activity policy version: %w", ErrActivityPolicyUnavailable)
	}
	computed, err := projectstate.DigestActivityPolicy(policy)
	if err != nil || computed != digest {
		return ActivityPolicyRecord{}, fmt.Errorf("localstore: Activity policy version: %w", ErrActivityPolicyUnavailable)
	}
	return ActivityPolicyRecord{Route: route, Policy: policy, PolicyJSON: append([]byte(nil), raw...), PolicyDigest: computed, ReceivedAt: receivedAt.UTC()}, nil
}

func (r *ActivityRepo) ReplacePolicy(ctx context.Context, route types.ActivityRouteKey, expectedVersion int64, expectedDigest projectstate.Digest, next projectstate.EffectiveActivityPolicyV1) (ActivityPolicyRecord, error) {
	if err := validateActivityRoute(route); err != nil {
		return ActivityPolicyRecord{}, err
	}
	canonical, err := projectstate.CanonicalActivityPolicy(next)
	if err != nil {
		return ActivityPolicyRecord{}, err
	}
	digest, err := projectstate.DigestActivityPolicy(next)
	if err != nil {
		return ActivityPolicyRecord{}, err
	}
	if expectedVersion < 0 || expectedVersion > maximumActivityInteger ||
		(expectedVersion == 0) != (expectedDigest == "") ||
		(expectedVersion > 0 && !activityDigestPattern.MatchString(string(expectedDigest))) {
		return ActivityPolicyRecord{}, fmt.Errorf("localstore: replace Activity policy: %w", ErrActivityPolicyChanged)
	}
	var result ActivityPolicyRecord
	err = r.withImmediate(ctx, "replace policy", func(conn *sql.Conn) error {
		if err := requireActiveActivityRoute(ctx, conn, route); err != nil {
			return err
		}
		result, err = replaceActivityPolicyTx(ctx, conn, route, expectedVersion, expectedDigest, next, canonical, digest)
		return err
	})
	if err != nil {
		return ActivityPolicyRecord{}, err
	}
	result.PolicyJSON = append([]byte(nil), result.PolicyJSON...)
	return result, nil
}

func replaceActivityPolicyTx(ctx context.Context, db activityDB, route types.ActivityRouteKey,
	expectedVersion int64, expectedDigest projectstate.Digest, next projectstate.EffectiveActivityPolicyV1,
	canonical []byte, digest projectstate.Digest) (ActivityPolicyRecord, error) {
	current, currentErr := currentActivityPolicy(ctx, db, route)
	if currentErr == nil && current.Policy.PolicyVersion == next.PolicyVersion &&
		current.PolicyDigest == digest && bytes.Equal(current.PolicyJSON, canonical) {
		return current, nil
	}
	if errors.Is(currentErr, ErrActivityPolicyUnavailable) {
		if expectedVersion != 0 || expectedDigest != "" || next.PolicyVersion != 1 {
			return ActivityPolicyRecord{}, fmt.Errorf("localstore: replace Activity policy: %w", ErrActivityPolicyChanged)
		}
	} else if currentErr != nil {
		return ActivityPolicyRecord{}, currentErr
	} else if current.Policy.PolicyVersion != expectedVersion || current.PolicyDigest != expectedDigest ||
		next.PolicyVersion <= current.Policy.PolicyVersion {
		return ActivityPolicyRecord{}, fmt.Errorf("localstore: replace Activity policy: %w", ErrActivityPolicyChanged)
	}
	now, err := databaseActivityNow(ctx, db)
	if err != nil {
		return ActivityPolicyRecord{}, err
	}
	nowEncoded := sqliteActivityTimestamp(now)
	arguments := activityRouteArgs(route)
	arguments = append(arguments, next.PolicyVersion, canonical, string(digest), next.TerminalRetentionSeconds, nowEncoded)
	if _, err := db.ExecContext(ctx, `INSERT INTO activity_policy_versions
		(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,policy_version,
		 canonical_policy_json,policy_digest,terminal_retention_seconds,received_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`, arguments...); err != nil {
		return ActivityPolicyRecord{}, fmt.Errorf("localstore: replace Activity policy: insert: %w", err)
	}
	arguments = activityRouteArgs(route)
	arguments = append(arguments, next.PolicyVersion, string(digest), nowEncoded)
	if currentErr != nil {
		_, err = db.ExecContext(ctx, `INSERT INTO activity_policy_current
			(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,policy_version,policy_digest,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?)`, arguments...)
	} else {
		arguments = []any{next.PolicyVersion, string(digest), nowEncoded, route.ProjectID, route.WorkspaceID,
			route.FabricInstanceID, route.RemoteProjectID, route.StreamID, route.CanonicalRef,
			expectedVersion, string(expectedDigest)}
		var update sql.Result
		update, err = db.ExecContext(ctx, `UPDATE activity_policy_current SET policy_version=?,policy_digest=?,updated_at=?
			WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=? AND stream_id=? AND canonical_ref=?
			AND policy_version=? AND policy_digest=?`, arguments...)
		if err == nil {
			var rows int64
			rows, err = update.RowsAffected()
			if err == nil && rows != 1 {
				err = ErrActivityPolicyChanged
			}
		}
	}
	if err != nil {
		return ActivityPolicyRecord{}, fmt.Errorf("localstore: replace Activity policy: current: %w", err)
	}
	queueArguments := []any{next.PolicyVersion, string(digest), nowEncoded}
	queueArguments = append(queueArguments, activityRouteArgs(route)...)
	if _, err := db.ExecContext(ctx, `UPDATE activity_outbound_queue
		SET expected_policy_version=?,expected_policy_digest=?,updated_at=?
		WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=? AND stream_id=? AND canonical_ref=? AND state='pending'`, queueArguments...); err != nil {
		return ActivityPolicyRecord{}, fmt.Errorf("localstore: replace Activity policy: pending queue: %w", err)
	}
	return ActivityPolicyRecord{
		Route: route, Policy: next, PolicyJSON: append([]byte(nil), canonical...), PolicyDigest: digest, ReceivedAt: now,
	}, nil
}
