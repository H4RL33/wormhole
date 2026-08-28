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

type activityReceiptEvidence struct {
	receipt   projectstate.ActivityReceiptV1
	canonical []byte
}

type activityQueueEvidence struct {
	state                             string
	expectedVersion, createdVersion   int64
	expectedDigest, createdDigest     projectstate.Digest
	attemptCount                      int64
	nextAttemptAt, createdAt, updated time.Time
	deliveredAt                       *time.Time
}

type activityLifecycleEvidence struct {
	kind, reference, state string
	policyVersion          int64
	policyDigest           projectstate.Digest
	retention              int64
	terminalAt, expiresAt  *time.Time
	updatedAt              time.Time
}

type activityEvidence struct {
	record         ActivityRecord
	ledgerSequence *int64
	receipt        *activityReceiptEvidence
	queue          *activityQueueEvidence
	lifecycles     []activityLifecycleEvidence
	promoted       bool
}

type activityPolicyEvidenceKey struct {
	route   types.ActivityRouteKey
	version int64
	digest  projectstate.Digest
}

type activityEvidenceLoader struct {
	policies map[activityPolicyEvidenceKey]ActivityPolicyRecord
}

func newActivityEvidenceLoader() *activityEvidenceLoader {
	return &activityEvidenceLoader{policies: make(map[activityPolicyEvidenceKey]ActivityPolicyRecord)}
}

func activityEvidenceConflict(operation string, sentinel error) error {
	return fmt.Errorf("localstore: %s Activity evidence: %w", operation, sentinel)
}

func (l *activityEvidenceLoader) policy(ctx context.Context, db activityDB, route types.ActivityRouteKey, version int64, digest projectstate.Digest) (ActivityPolicyRecord, error) {
	key := activityPolicyEvidenceKey{route: route, version: version, digest: digest}
	if policy, ok := l.policies[key]; ok {
		return policy, nil
	}
	policy, err := activityPolicyVersion(ctx, db, route, version, digest)
	if err != nil {
		return ActivityPolicyRecord{}, err
	}
	l.policies[key] = policy
	return policy, nil
}

func (l *activityEvidenceLoader) load(ctx context.Context, db activityDB, key types.ActivityOriginKey) (activityEvidence, error) {
	var evidence activityEvidence
	var raw, actorJSON []byte
	var storedDigest, class, createdAtRaw, acceptedAtRaw string
	var sequence sql.NullInt64
	var projection activityProjection
	err := db.QueryRowContext(ctx, `SELECT activity_class,canonical_activity_json,activity_digest,source_actor_json,
		event_channel_id,event_actor_id,event_type,event_payload,event_note,CAST(event_created_at AS TEXT),
		embedded_lifecycle_kind,embedded_lifecycle_reference_id,CAST(created_at AS TEXT),CAST(accepted_at AS TEXT),sequence
		FROM activity_ledger WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=?
		AND stream_id=? AND canonical_ref=? AND source_workspace_id=? AND activity_id=?`, activityOriginArgs(key)...).Scan(
		&class, &raw, &storedDigest, &actorJSON,
		&projection.eventChannelID, &projection.eventActorID, &projection.eventType, &projection.eventPayload,
		&projection.eventNote, &projection.eventCreatedAt, &projection.lifecycleKind, &projection.lifecycleReference,
		&createdAtRaw, &acceptedAtRaw, &sequence,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return activityEvidence{}, activityEvidenceConflict("read", ErrActivityNotFound)
	}
	if err != nil {
		return activityEvidence{}, activityEvidenceConflict("scan ledger", ErrActivityReplayConflict)
	}
	activity, err := projectstate.DecodeActivity(raw)
	if err != nil || activity.ID != key.ActivityID || string(activity.Class) != class || !validRemoteActivity(activity) {
		return activityEvidence{}, activityEvidenceConflict("decode ledger", ErrActivityReplayConflict)
	}
	digest, err := projectstate.DigestActivity(activity)
	if err != nil || string(digest) != storedDigest || !activityDigestPattern.MatchString(storedDigest) || !projection.matches(activity) {
		return activityEvidence{}, activityEvidenceConflict("validate ledger projection", ErrActivityReplayConflict)
	}
	canonicalActor, err := projectstate.CanonicalJSON(activity.Actor)
	if err != nil || !bytes.Equal(actorJSON, canonicalActor) {
		return activityEvidence{}, activityEvidenceConflict("validate ledger actor", ErrActivityReplayConflict)
	}
	createdAt, err := parseSQLiteActivityTimestamp(createdAtRaw)
	if err != nil || !createdAt.Equal(activity.CreatedAt) {
		return activityEvidence{}, activityEvidenceConflict("validate ledger creation", ErrActivityReplayConflict)
	}
	acceptedAt, err := parseSQLiteActivityTimestamp(acceptedAtRaw)
	if err != nil {
		return activityEvidence{}, activityEvidenceConflict("validate ledger acceptance", ErrActivityReplayConflict)
	}
	evidence.record = ActivityRecord{
		Key: key, Activity: activity, ActivityJSON: append([]byte(nil), raw...), ActivityDigest: digest, AcceptedAt: acceptedAt,
	}
	if sequence.Valid {
		if sequence.Int64 < 1 || sequence.Int64 > maximumActivityInteger {
			return activityEvidence{}, activityEvidenceConflict("validate ledger sequence", ErrActivityReplayConflict)
		}
		value := sequence.Int64
		evidence.ledgerSequence = &value
		evidence.record.Sequence = &value
	}

	if err := l.loadReceipt(ctx, db, &evidence); err != nil {
		return activityEvidence{}, err
	}
	if err := l.loadQueue(ctx, db, &evidence); err != nil {
		return activityEvidence{}, err
	}
	if err := l.loadLifecycles(ctx, db, &evidence); err != nil {
		return activityEvidence{}, err
	}
	if err := l.loadPromotionEvidence(ctx, db, &evidence); err != nil {
		return activityEvidence{}, err
	}
	if err := l.validateRelations(&evidence); err != nil {
		return activityEvidence{}, err
	}
	return evidence, nil
}

func (l *activityEvidenceLoader) loadReceipt(ctx context.Context, db activityDB, evidence *activityEvidence) error {
	var digest, policyDigest, acceptedAtRaw string
	var sequence, policyVersion int64
	err := db.QueryRowContext(ctx, `SELECT activity_digest,sequence,policy_version,policy_digest,CAST(accepted_at AS TEXT)
		FROM activity_ingress_receipts WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=?
		AND stream_id=? AND canonical_ref=? AND source_workspace_id=? AND activity_id=?`, activityOriginArgs(evidence.record.Key)...).Scan(
		&digest, &sequence, &policyVersion, &policyDigest, &acceptedAtRaw,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return activityEvidenceConflict("read receipt", ErrActivityReplayConflict)
	}
	acceptedAt, err := parseSQLiteActivityTimestamp(acceptedAtRaw)
	if err != nil {
		return activityEvidenceConflict("read receipt", ErrActivityReplayConflict)
	}
	receipt := projectstate.ActivityReceiptV1{
		SchemaVersion: 1, ActivityID: evidence.record.Key.ActivityID, ActivityDigest: projectstate.Digest(digest),
		Sequence: sequence, PolicyVersion: policyVersion, PolicyDigest: projectstate.Digest(policyDigest), AcceptedAt: acceptedAt,
	}
	canonical, err := projectstate.CanonicalActivityReceipt(receipt)
	if err != nil || receipt.ActivityDigest != evidence.record.ActivityDigest {
		return activityEvidenceConflict("read receipt", ErrActivityReplayConflict)
	}
	if _, err := l.policy(ctx, db, evidence.record.Key.Route, receipt.PolicyVersion, receipt.PolicyDigest); err != nil {
		return err
	}
	if evidence.ledgerSequence != nil && (*evidence.ledgerSequence != receipt.Sequence || !evidence.record.AcceptedAt.Equal(receipt.AcceptedAt)) {
		return activityEvidenceConflict("read receipt", ErrActivityReplayConflict)
	}
	evidence.receipt = &activityReceiptEvidence{receipt: receipt, canonical: canonical}
	return nil
}

func (l *activityEvidenceLoader) loadQueue(ctx context.Context, db activityDB, evidence *activityEvidence) error {
	var queue activityQueueEvidence
	var expectedDigest, createdDigest, nextAttemptAt, createdAt, updatedAt string
	var deliveredAt sql.NullString
	err := db.QueryRowContext(ctx, `SELECT state,expected_policy_version,expected_policy_digest,created_policy_version,
		created_policy_digest,attempt_count,CAST(next_attempt_at AS TEXT),CAST(created_at AS TEXT),CAST(updated_at AS TEXT),CAST(delivered_at AS TEXT)
		FROM activity_outbound_queue WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=?
		AND stream_id=? AND canonical_ref=? AND source_workspace_id=? AND activity_id=?`, activityOriginArgs(evidence.record.Key)...).Scan(
		&queue.state, &queue.expectedVersion, &expectedDigest, &queue.createdVersion, &createdDigest,
		&queue.attemptCount, &nextAttemptAt, &createdAt, &updatedAt, &deliveredAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return activityEvidenceConflict("read queue", ErrActivityReplayConflict)
	}
	queue.expectedDigest, queue.createdDigest = projectstate.Digest(expectedDigest), projectstate.Digest(createdDigest)
	if queue.attemptCount < 0 || evidence.record.Key.SourceWorkspaceID != evidence.record.Key.Route.WorkspaceID {
		return activityEvidenceConflict("read queue", ErrActivityReplayConflict)
	}
	if _, err := l.policy(ctx, db, evidence.record.Key.Route, queue.expectedVersion, queue.expectedDigest); err != nil {
		return err
	}
	if _, err := l.policy(ctx, db, evidence.record.Key.Route, queue.createdVersion, queue.createdDigest); err != nil {
		return err
	}
	queue.nextAttemptAt, err = parseSQLiteActivityTimestamp(nextAttemptAt)
	if err != nil {
		return activityEvidenceConflict("read queue", ErrActivityReplayConflict)
	}
	queue.createdAt, err = parseSQLiteActivityTimestamp(createdAt)
	if err != nil {
		return activityEvidenceConflict("read queue", ErrActivityReplayConflict)
	}
	queue.updated, err = parseSQLiteActivityTimestamp(updatedAt)
	if err != nil {
		return activityEvidenceConflict("read queue", ErrActivityReplayConflict)
	}
	if deliveredAt.Valid {
		value, parseErr := parseSQLiteActivityTimestamp(deliveredAt.String)
		if parseErr != nil {
			return activityEvidenceConflict("read queue", ErrActivityReplayConflict)
		}
		queue.deliveredAt = &value
	}
	if (queue.state == "pending" && queue.deliveredAt != nil) || (queue.state == "delivered" && queue.deliveredAt == nil) ||
		(queue.state != "pending" && queue.state != "delivered") {
		return activityEvidenceConflict("read queue", ErrActivityReplayConflict)
	}
	evidence.queue = &queue
	return nil
}

func (l *activityEvidenceLoader) loadLifecycles(ctx context.Context, db activityDB, evidence *activityEvidence) error {
	rows, err := db.QueryContext(ctx, `SELECT lifecycle_kind,reference_id,state,policy_version,policy_digest,
		terminal_retention_seconds,CAST(terminal_at AS TEXT),CAST(expires_at AS TEXT),CAST(updated_at AS TEXT)
		FROM activity_lifecycle WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=?
		AND stream_id=? AND canonical_ref=? AND source_workspace_id=? AND activity_id=?
		ORDER BY lifecycle_kind,reference_id`, activityOriginArgs(evidence.record.Key)...)
	if err != nil {
		return activityEvidenceConflict("read lifecycle", ErrActivityLifecycleConflict)
	}
	defer rows.Close()
	for rows.Next() {
		var lifecycle activityLifecycleEvidence
		var policyDigest, updatedAt string
		var terminalAt, expiresAt sql.NullString
		if err := rows.Scan(&lifecycle.kind, &lifecycle.reference, &lifecycle.state, &lifecycle.policyVersion,
			&policyDigest, &lifecycle.retention, &terminalAt, &expiresAt, &updatedAt); err != nil {
			return activityEvidenceConflict("read lifecycle", ErrActivityLifecycleConflict)
		}
		lifecycle.policyDigest = projectstate.Digest(policyDigest)
		if !types.CanonicalUUID(lifecycle.reference) || !validActivityLifecycleState(lifecycle.kind, lifecycle.state) {
			return activityEvidenceConflict("read lifecycle", ErrActivityLifecycleConflict)
		}
		policy, err := l.policy(ctx, db, evidence.record.Key.Route, lifecycle.policyVersion, lifecycle.policyDigest)
		if err != nil || lifecycle.retention != policy.Policy.TerminalRetentionSeconds {
			return activityEvidenceConflict("read lifecycle", ErrActivityLifecycleConflict)
		}
		lifecycle.updatedAt, err = parseSQLiteActivityTimestamp(updatedAt)
		if err != nil {
			return activityEvidenceConflict("read lifecycle", ErrActivityLifecycleConflict)
		}
		terminal := terminalActivityLifecycleState(lifecycle.kind, lifecycle.state)
		if terminalAt.Valid != terminal || expiresAt.Valid != terminal {
			return activityEvidenceConflict("read lifecycle", ErrActivityLifecycleConflict)
		}
		if terminal {
			terminalValue, terminalErr := parseSQLiteActivityTimestamp(terminalAt.String)
			expiresValue, expiresErr := parseSQLiteActivityTimestamp(expiresAt.String)
			if terminalErr != nil || expiresErr != nil || !expiresValue.Equal(terminalValue.Add(time.Duration(lifecycle.retention)*time.Second)) {
				return activityEvidenceConflict("read lifecycle", ErrActivityLifecycleConflict)
			}
			lifecycle.terminalAt, lifecycle.expiresAt = &terminalValue, &expiresValue
		}
		evidence.lifecycles = append(evidence.lifecycles, lifecycle)
	}
	if err := rows.Err(); err != nil {
		return activityEvidenceConflict("read lifecycle", ErrActivityLifecycleConflict)
	}
	return nil
}

func (l *activityEvidenceLoader) loadPromotionEvidence(ctx context.Context, db activityDB, evidence *activityEvidence) error {
	rows, err := db.QueryContext(ctx, `SELECT source_activity_digest FROM activity_promotion_receipts
		WHERE source_project_id=? AND source_workspace_binding_id=? AND source_fabric_instance_id=?
		AND source_remote_project_id=? AND source_stream_id=? AND source_canonical_ref=?
		AND source_origin_workspace_id=? AND source_activity_id=?`, activityOriginArgs(evidence.record.Key)...)
	if err != nil {
		return activityEvidenceConflict("read promotion", ErrActivityReplayConflict)
	}
	defer rows.Close()
	for rows.Next() {
		var digest string
		if err := rows.Scan(&digest); err != nil || projectstate.Digest(digest) != evidence.record.ActivityDigest {
			return activityEvidenceConflict("read promotion", ErrActivityReplayConflict)
		}
		evidence.promoted = true
	}
	if err := rows.Err(); err != nil {
		return activityEvidenceConflict("read promotion", ErrActivityReplayConflict)
	}
	return nil
}

func (l *activityEvidenceLoader) validateRelations(evidence *activityEvidence) error {
	if evidence.receipt == nil && evidence.ledgerSequence != nil {
		return activityEvidenceConflict("validate", ErrActivityReplayConflict)
	}
	if evidence.receipt == nil && evidence.queue == nil {
		return activityEvidenceConflict("validate", ErrActivityReplayConflict)
	}
	if evidence.receipt != nil && evidence.queue != nil && evidence.queue.state != "delivered" {
		return activityEvidenceConflict("validate", ErrActivityReplayConflict)
	}
	var embedded, delivery *activityLifecycleEvidence
	for index := range evidence.lifecycles {
		lifecycle := &evidence.lifecycles[index]
		if evidence.record.Activity.Lifecycle != nil && lifecycle.kind == string(evidence.record.Activity.Lifecycle.Kind) &&
			lifecycle.reference == evidence.record.Activity.Lifecycle.ReferenceID {
			embedded = lifecycle
		}
		if lifecycle.kind == "delivery" && lifecycle.reference == evidence.record.Key.ActivityID {
			delivery = lifecycle
		}
	}
	if evidence.record.Activity.Lifecycle != nil && embedded == nil {
		return activityEvidenceConflict("validate", ErrActivityLifecycleConflict)
	}
	if evidence.queue != nil {
		if delivery == nil || (evidence.queue.state == "delivered" && delivery.state != "delivered") {
			return activityEvidenceConflict("validate", ErrActivityLifecycleConflict)
		}
	}
	return nil
}

func (e activityEvidence) recordForPolicy(version int64, digest projectstate.Digest) (ActivityRecord, error) {
	matched := e.receipt != nil && e.receipt.receipt.PolicyVersion == version && e.receipt.receipt.PolicyDigest == digest
	if e.queue != nil {
		matched = matched || (e.queue.expectedVersion == version && e.queue.expectedDigest == digest) ||
			(e.queue.createdVersion == version && e.queue.createdDigest == digest)
	}
	if !matched {
		return ActivityRecord{}, activityEvidenceConflict("project", ErrActivityReplayConflict)
	}
	record := cloneActivityRecord(e.record)
	record.PolicyVersion, record.PolicyDigest = version, digest
	if e.receipt != nil {
		sequence := e.receipt.receipt.Sequence
		record.Sequence = &sequence
		record.AcceptedAt = e.receipt.receipt.AcceptedAt
	}
	return record, nil
}

func (e activityEvidence) pendingRecord() (ActivityRecord, error) {
	if e.queue == nil || e.queue.state != "pending" {
		return ActivityRecord{}, activityEvidenceConflict("pending", ErrActivityReplayConflict)
	}
	return e.recordForPolicy(e.queue.expectedVersion, e.queue.expectedDigest)
}

func (e activityEvidence) retainedRecord() (ActivityRecord, error) {
	if e.receipt != nil {
		return e.recordForPolicy(e.receipt.receipt.PolicyVersion, e.receipt.receipt.PolicyDigest)
	}
	if e.queue != nil {
		return e.recordForPolicy(e.queue.createdVersion, e.queue.createdDigest)
	}
	return ActivityRecord{}, activityEvidenceConflict("retained", ErrActivityReplayConflict)
}
