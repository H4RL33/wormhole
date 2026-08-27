package git

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

const maximumActivitySequence int64 = 9_007_199_254_740_991

type FabricActivityStreamKey struct {
	ProjectID, FabricInstanceID, StreamID, CanonicalRef string
}

type FabricActivityOriginKey struct {
	Stream            FabricActivityStreamKey
	SourceWorkspaceID string
	ActivityID        string
}

type AcceptActivityInput struct {
	Key           FabricActivityOriginKey
	Activity      projectstate.ActivityV1
	IssuedActor   types.ActorEnvelope
	PolicyVersion int64
	PolicyDigest  projectstate.Digest
}

type PullActivityInput struct {
	Stream        FabricActivityStreamKey
	AttachmentRef string
	AfterSequence int64
	Limit         int
}

type ActivityDelivery struct {
	SourceWorkspaceID string
	ActivityJSON      []byte
	ActivityDigest    projectstate.Digest
	Receipt           projectstate.ActivityReceiptV1
}

type PullActivityResult struct {
	PolicyJSON   []byte
	PolicyDigest projectstate.Digest
	Deliveries   []ActivityDelivery
	NextSequence int64
	HasMore      bool
}

type ActivityLifecycleTransition struct {
	Kind, ReferenceID, ExpectedState, NextState string
}

type ActivityStore struct {
	db *sql.DB
}

func NewActivityStore(db *sql.DB) *ActivityStore { return &ActivityStore{db: db} }

func (key FabricActivityOriginKey) validate() error {
	if err := validateFabricActivityStreamKey(key.Stream); err != nil {
		return err
	}
	if !types.CanonicalUUID(key.SourceWorkspaceID) || !types.CanonicalUUID(key.ActivityID) {
		return fmt.Errorf("git: activity origin invalid: %w", ErrActivityNotFound)
	}
	return nil
}

func (s *ActivityStore) Accept(ctx context.Context, input AcceptActivityInput) (projectstate.ActivityReceiptV1, error) {
	if s == nil || s.db == nil {
		return projectstate.ActivityReceiptV1{}, fmt.Errorf("git: accept activity: %w", ErrActivityNotFound)
	}
	if err := input.Key.validate(); err != nil {
		return projectstate.ActivityReceiptV1{}, err
	}
	if input.Key.ActivityID != input.Activity.ID || input.Activity.Class == projectstate.ActivityPresenceV1 {
		return projectstate.ActivityReceiptV1{}, projectstate.ErrInvalidActivity
	}
	canonical, err := projectstate.CanonicalActivity(input.Activity)
	if err != nil {
		return projectstate.ActivityReceiptV1{}, err
	}
	digest, err := projectstate.DigestActivity(input.Activity)
	if err != nil {
		return projectstate.ActivityReceiptV1{}, err
	}
	if err := input.IssuedActor.ValidateHistorical(); err != nil {
		return projectstate.ActivityReceiptV1{}, projectstate.ErrInvalidActivity
	}
	embeddedActor, err := projectstate.CanonicalJSON(input.Activity.Actor)
	if err != nil {
		return projectstate.ActivityReceiptV1{}, projectstate.ErrInvalidActivity
	}
	issuedActor, err := projectstate.CanonicalJSON(input.IssuedActor)
	if err != nil || !bytes.Equal(embeddedActor, issuedActor) {
		return projectstate.ActivityReceiptV1{}, projectstate.ErrInvalidActivity
	}
	if input.PolicyVersion < 1 || input.PolicyVersion > maximumActivitySequence {
		return projectstate.ActivityReceiptV1{}, fmt.Errorf("git: accept activity: %w", ErrActivityPolicyChanged)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return projectstate.ActivityReceiptV1{}, fmt.Errorf("git: accept activity: begin: %w", err)
	}
	defer tx.Rollback()
	if err := setActivityProject(ctx, tx, input.Key.Stream.ProjectID); err != nil {
		return projectstate.ActivityReceiptV1{}, err
	}
	arguments := activityAcceptArguments(input, canonical, digest, embeddedActor)
	var receipt projectstate.ActivityReceiptV1
	var returnedDigest string
	err = tx.QueryRowContext(ctx, `SELECT activity_digest,sequence,policy_version,policy_digest,accepted_at
		FROM fabric_accept_activity_v1(
		$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`, arguments...).
		Scan(&returnedDigest, &receipt.Sequence, &receipt.PolicyVersion, &receipt.PolicyDigest, &receipt.AcceptedAt)
	if err != nil {
		_ = tx.Rollback()
		switch activityDatabaseMessage(err) {
		case "activity replay conflict":
			return projectstate.ActivityReceiptV1{}, fmt.Errorf("git: accept activity: %w", ErrActivityReplayConflict)
		case "activity binding unavailable":
			return projectstate.ActivityReceiptV1{}, fmt.Errorf("git: accept activity: %w", ErrActivityNotFound)
		case "activity policy unavailable":
			return projectstate.ActivityReceiptV1{}, fmt.Errorf("git: accept activity: %w", ErrActivityPolicyUnavailable)
		case "activity policy changed":
			return projectstate.ActivityReceiptV1{}, s.policyChangedError(ctx, input.Key.Stream)
		case "activity sequence unavailable":
			return projectstate.ActivityReceiptV1{}, fmt.Errorf("git: accept activity: %w", ErrActivityCursorConflict)
		default:
			return projectstate.ActivityReceiptV1{}, fmt.Errorf("git: accept activity: database: %w", err)
		}
	}
	receipt.SchemaVersion = 1
	receipt.ActivityID = input.Activity.ID
	receipt.ActivityDigest = projectstate.Digest(returnedDigest)
	receipt.AcceptedAt = receipt.AcceptedAt.UTC()
	if receipt.ActivityDigest != digest {
		return projectstate.ActivityReceiptV1{}, fmt.Errorf("git: accept activity: %w", ErrActivityReplayConflict)
	}
	if _, err := projectstate.CanonicalActivityReceipt(receipt); err != nil {
		return projectstate.ActivityReceiptV1{}, fmt.Errorf("git: accept activity: invalid receipt: %w", ErrActivityReplayConflict)
	}
	if err := tx.Commit(); err != nil {
		return projectstate.ActivityReceiptV1{}, fmt.Errorf("git: accept activity: commit: %w", err)
	}
	return receipt, nil
}

func activityAcceptArguments(input AcceptActivityInput, canonical []byte, digest projectstate.Digest, actorJSON []byte) []any {
	var eventChannelID, eventActorID, eventType, eventPayload, eventNote, eventCreatedAt any
	if input.Activity.Event != nil {
		eventChannelID = input.Activity.Event.ChannelID
		eventActorID = input.Activity.Event.ActorID
		eventType = input.Activity.Event.EventType
		eventPayload = []byte(input.Activity.Event.Payload)
		if input.Activity.Event.Note != nil {
			eventNote = *input.Activity.Event.Note
		}
		eventCreatedAt = input.Activity.Event.CreatedAt
	}
	var lifecycleKind, lifecycleReference any
	if input.Activity.Lifecycle != nil {
		lifecycleKind = string(input.Activity.Lifecycle.Kind)
		lifecycleReference = input.Activity.Lifecycle.ReferenceID
	}
	return []any{
		input.Key.Stream.ProjectID, input.Key.Stream.FabricInstanceID, input.Key.Stream.StreamID, input.Key.Stream.CanonicalRef,
		input.Key.SourceWorkspaceID, input.Key.ActivityID, string(input.Activity.Class), canonical, string(digest), actorJSON,
		eventChannelID, eventActorID, eventType, eventPayload, eventNote, eventCreatedAt,
		lifecycleKind, lifecycleReference, input.Activity.CreatedAt, input.PolicyVersion, string(input.PolicyDigest),
	}
}

func (s *ActivityStore) policyChangedError(ctx context.Context, key FabricActivityStreamKey) error {
	policy, err := s.CurrentPolicy(ctx, key)
	if err != nil {
		return fmt.Errorf("git: accept activity: %w", ErrActivityPolicyChanged)
	}
	raw, err := projectstate.CanonicalActivityPolicy(policy)
	if err != nil {
		return fmt.Errorf("git: accept activity: %w", ErrActivityPolicyChanged)
	}
	digest, err := projectstate.DigestActivityPolicy(policy)
	if err != nil {
		return fmt.Errorf("git: accept activity: %w", ErrActivityPolicyChanged)
	}
	return &ActivityPolicyChangedError{CurrentPolicyJSON: append([]byte(nil), raw...), CurrentDigest: digest}
}

func (s *ActivityStore) Pull(ctx context.Context, input PullActivityInput) (PullActivityResult, error) {
	if s == nil || s.db == nil {
		return PullActivityResult{}, fmt.Errorf("git: pull activity: %w", ErrActivityNotFound)
	}
	if err := validateFabricActivityStreamKey(input.Stream); err != nil {
		return PullActivityResult{}, err
	}
	if !types.CanonicalUUID(input.AttachmentRef) || input.AfterSequence < 0 || input.AfterSequence > maximumActivitySequence || input.Limit < 1 || input.Limit > 500 {
		return PullActivityResult{}, fmt.Errorf("git: pull activity: %w", ErrActivityCursorConflict)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return PullActivityResult{}, fmt.Errorf("git: pull activity: begin: %w", err)
	}
	defer tx.Rollback()
	if err := setActivityProject(ctx, tx, input.Stream.ProjectID); err != nil {
		return PullActivityResult{}, err
	}
	var attached bool
	err = tx.QueryRowContext(ctx, `SELECT true FROM fabric_workspace_stream_bindings
		WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND canonical_ref=$4
		AND attachment_ref=$5 AND detached_at IS NULL`, input.Stream.ProjectID, input.Stream.FabricInstanceID,
		input.Stream.StreamID, input.Stream.CanonicalRef, input.AttachmentRef).Scan(&attached)
	if errors.Is(err, sql.ErrNoRows) {
		return PullActivityResult{}, fmt.Errorf("git: pull activity: %w", ErrActivityNotFound)
	}
	if err != nil {
		return PullActivityResult{}, fmt.Errorf("git: pull activity: binding: %w", err)
	}
	_, policyJSON, policyDigest, err := currentActivityPolicyTx(ctx, tx, input.Stream)
	if err != nil {
		return PullActivityResult{}, err
	}
	var highWatermark int64
	if err := tx.QueryRowContext(ctx, `SELECT high_watermark FROM fabric_activity_stream_sequences
		WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND canonical_ref=$4`,
		input.Stream.ProjectID, input.Stream.FabricInstanceID, input.Stream.StreamID, input.Stream.CanonicalRef).Scan(&highWatermark); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PullActivityResult{}, fmt.Errorf("git: pull activity: %w", ErrActivityPolicyUnavailable)
		}
		return PullActivityResult{}, fmt.Errorf("git: pull activity: sequence: %w", err)
	}
	if input.AfterSequence > highWatermark {
		return PullActivityResult{}, fmt.Errorf("git: pull activity: %w", ErrActivityCursorConflict)
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.source_workspace_id,a.canonical_activity_json,a.activity_digest,
		r.sequence,r.policy_version,r.policy_digest,r.accepted_at
		FROM fabric_activities a JOIN fabric_activity_ingress_receipts r
		USING(project_id,fabric_instance_id,stream_id,canonical_ref,source_workspace_id,activity_id)
		WHERE a.project_id=$1 AND a.fabric_instance_id=$2 AND a.stream_id=$3 AND a.canonical_ref=$4
		AND a.sequence>$5 AND a.sequence<=$6 ORDER BY a.sequence LIMIT $7`,
		input.Stream.ProjectID, input.Stream.FabricInstanceID, input.Stream.StreamID, input.Stream.CanonicalRef,
		input.AfterSequence, highWatermark, input.Limit+1)
	if err != nil {
		return PullActivityResult{}, fmt.Errorf("git: pull activity: query: %w", err)
	}
	defer rows.Close()
	deliveries := make([]ActivityDelivery, 0, input.Limit)
	for rows.Next() {
		var delivery ActivityDelivery
		var digest string
		var acceptedAt time.Time
		if err := rows.Scan(&delivery.SourceWorkspaceID, &delivery.ActivityJSON, &digest,
			&delivery.Receipt.Sequence, &delivery.Receipt.PolicyVersion, &delivery.Receipt.PolicyDigest, &acceptedAt); err != nil {
			return PullActivityResult{}, fmt.Errorf("git: pull activity: scan: %w", err)
		}
		activity, err := projectstate.DecodeActivity(delivery.ActivityJSON)
		if err != nil || activity.ID == "" {
			return PullActivityResult{}, fmt.Errorf("git: pull activity: invalid retained activity: %w", ErrActivityReplayConflict)
		}
		computed, err := projectstate.DigestActivity(activity)
		if err != nil || string(computed) != digest {
			return PullActivityResult{}, fmt.Errorf("git: pull activity: invalid retained digest: %w", ErrActivityReplayConflict)
		}
		delivery.ActivityJSON = append([]byte(nil), delivery.ActivityJSON...)
		delivery.ActivityDigest = computed
		delivery.Receipt.SchemaVersion = 1
		delivery.Receipt.ActivityID = activity.ID
		delivery.Receipt.ActivityDigest = computed
		delivery.Receipt.AcceptedAt = acceptedAt.UTC()
		if _, err := projectstate.CanonicalActivityReceipt(delivery.Receipt); err != nil {
			return PullActivityResult{}, fmt.Errorf("git: pull activity: invalid retained receipt: %w", ErrActivityReplayConflict)
		}
		deliveries = append(deliveries, delivery)
	}
	if err := rows.Err(); err != nil {
		return PullActivityResult{}, fmt.Errorf("git: pull activity: iterate: %w", err)
	}
	hasMore := len(deliveries) > input.Limit
	if hasMore {
		deliveries = deliveries[:input.Limit]
	}
	next := highWatermark
	if hasMore && len(deliveries) > 0 {
		next = deliveries[len(deliveries)-1].Receipt.Sequence
	}
	if err := tx.Commit(); err != nil {
		return PullActivityResult{}, fmt.Errorf("git: pull activity: commit: %w", err)
	}
	return PullActivityResult{
		PolicyJSON:   append([]byte(nil), policyJSON...),
		PolicyDigest: policyDigest,
		Deliveries:   deliveries,
		NextSequence: next,
		HasMore:      hasMore,
	}, nil
}

func activityDatabaseMessage(err error) string {
	var databaseError *pq.Error
	if errors.As(err, &databaseError) {
		return databaseError.Message
	}
	return ""
}
