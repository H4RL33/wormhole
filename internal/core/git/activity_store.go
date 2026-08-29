package git

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
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
	SourceRef      string
	ActivityJSON   []byte
	ActivityDigest projectstate.Digest
	Receipt        projectstate.ActivityReceiptV1
}

// ActivityPolicyEvidence is canonical immutable policy evidence for receipts in a
// pull result. It is intentionally internal transport state, not a public wire record.
type ActivityPolicyEvidence struct {
	Stream       FabricActivityStreamKey
	PolicyJSON   []byte
	PolicyDigest projectstate.Digest
}

type PullActivityResult struct {
	PolicyJSON         []byte
	PolicyDigest       projectstate.Digest
	HistoricalPolicies []ActivityPolicyEvidence
	Deliveries         []ActivityDelivery
	NextSequence       int64
	HasMore            bool
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return projectstate.ActivityReceiptV1{}, fmt.Errorf("git: accept activity: begin: %w", err)
	}
	defer tx.Rollback()
	receipt, err := s.AcceptInTx(ctx, tx, input)
	if err != nil {
		return projectstate.ActivityReceiptV1{}, err
	}
	if err := tx.Commit(); err != nil {
		return projectstate.ActivityReceiptV1{}, fmt.Errorf("git: accept activity: commit: %w", err)
	}
	return receipt, nil
}

func (s *ActivityStore) AcceptInTx(ctx context.Context, tx *sql.Tx, input AcceptActivityInput) (projectstate.ActivityReceiptV1, error) {
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
	if !validRemoteActivityAssurance(input.IssuedActor.Assurance) ||
		!validRemoteActivityAssurance(input.Activity.Actor.Assurance) {
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

	if tx == nil {
		return projectstate.ActivityReceiptV1{}, fmt.Errorf("git: accept activity: %w", ErrActivityNotFound)
	}
	if err := setActivityProject(ctx, tx, input.Key.Stream.ProjectID); err != nil {
		return projectstate.ActivityReceiptV1{}, err
	}
	arguments := activityAcceptArguments(input, canonical, digest, embeddedActor)
	// PostgreSQL marks the transaction failed when the function raises an
	// exception. A savepoint lets us clear that state while retaining the
	// caller's transaction (and allows policyChangedErrorInTx to read the
	// authoritative current policy).
	savepoint := newActivitySavepoint("wormhole_accept_activity")
	if _, err := tx.ExecContext(ctx, `SAVEPOINT `+savepoint); err != nil {
		return projectstate.ActivityReceiptV1{}, fmt.Errorf("git: accept activity: savepoint: %w", err)
	}
	var receipt projectstate.ActivityReceiptV1
	var returnedDigest string
	err = tx.QueryRowContext(ctx, `SELECT activity_digest,sequence,policy_version,policy_digest,accepted_at
		FROM fabric_accept_activity_v1(
		$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`, arguments...).
		Scan(&returnedDigest, &receipt.Sequence, &receipt.PolicyVersion, &receipt.PolicyDigest, &receipt.AcceptedAt)
	if err != nil {
		if recoveryErr := recoverActivitySavepoint(ctx, tx, savepoint); recoveryErr != nil {
			return projectstate.ActivityReceiptV1{}, fmt.Errorf("git: accept activity: recover savepoint: %w (original: %v)", recoveryErr, err)
		}
		switch activityDatabaseMessage(err) {
		case "activity replay conflict":
			return projectstate.ActivityReceiptV1{}, fmt.Errorf("git: accept activity: %w", ErrActivityReplayConflict)
		case "activity binding unavailable":
			return projectstate.ActivityReceiptV1{}, fmt.Errorf("git: accept activity: %w", ErrActivityNotFound)
		case "activity policy unavailable":
			return projectstate.ActivityReceiptV1{}, fmt.Errorf("git: accept activity: %w", ErrActivityPolicyUnavailable)
		case "activity policy changed":
			return projectstate.ActivityReceiptV1{}, s.policyChangedErrorInTx(ctx, tx, input.Key.Stream)
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
		if recoveryErr := recoverActivitySavepoint(ctx, tx, savepoint); recoveryErr != nil {
			return projectstate.ActivityReceiptV1{}, fmt.Errorf("git: accept activity: recover invalid receipt: %w", recoveryErr)
		}
		return projectstate.ActivityReceiptV1{}, fmt.Errorf("git: accept activity: %w", ErrActivityReplayConflict)
	}
	if _, err := projectstate.CanonicalActivityReceipt(receipt); err != nil {
		if recoveryErr := recoverActivitySavepoint(ctx, tx, savepoint); recoveryErr != nil {
			return projectstate.ActivityReceiptV1{}, fmt.Errorf("git: accept activity: recover invalid receipt: %w", recoveryErr)
		}
		return projectstate.ActivityReceiptV1{}, fmt.Errorf("git: accept activity: invalid receipt: %w", ErrActivityReplayConflict)
	}
	if _, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT `+savepoint); err != nil {
		return projectstate.ActivityReceiptV1{}, fmt.Errorf("git: accept activity: release savepoint: %w", err)
	}
	return receipt, nil
}

func newActivitySavepoint(prefix string) string {
	return prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func recoverActivitySavepoint(ctx context.Context, tx *sql.Tx, name string) error {
	if _, err := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT `+name); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT `+name)
	return err
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

func (s *ActivityStore) policyChangedErrorInTx(ctx context.Context, tx *sql.Tx, key FabricActivityStreamKey) error {
	policy, err := s.CurrentPolicyInTx(ctx, tx, key)
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
	currentPolicy, policyJSON, policyDigest, err := currentActivityPolicyTx(ctx, tx, input.Stream)
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
	rows, err := tx.QueryContext(ctx, `SELECT b.activity_source_ref,a.canonical_activity_json,a.activity_digest,
		r.sequence,r.policy_version,r.policy_digest,r.accepted_at,p.canonical_policy_json
		FROM fabric_activities a JOIN fabric_activity_ingress_receipts r
		USING(project_id,fabric_instance_id,stream_id,canonical_ref,source_workspace_id,activity_id)
		JOIN fabric_workspace_stream_bindings b ON b.project_id=a.project_id AND b.fabric_instance_id=a.fabric_instance_id
		AND b.stream_id=a.stream_id AND b.canonical_ref=a.canonical_ref AND b.workspace_id=a.source_workspace_id AND b.detached_at IS NULL
		LEFT JOIN fabric_activity_policy_versions p ON p.project_id=r.project_id AND p.fabric_instance_id=r.fabric_instance_id
		AND p.stream_id=r.stream_id AND p.canonical_ref=r.canonical_ref AND p.policy_version=r.policy_version
		AND p.policy_digest=r.policy_digest
		WHERE a.project_id=$1 AND a.fabric_instance_id=$2 AND a.stream_id=$3 AND a.canonical_ref=$4
		AND a.sequence>$5 AND a.sequence<=$6 ORDER BY a.sequence LIMIT $7`,
		input.Stream.ProjectID, input.Stream.FabricInstanceID, input.Stream.StreamID, input.Stream.CanonicalRef,
		input.AfterSequence, highWatermark, input.Limit+1)
	if err != nil {
		return PullActivityResult{}, fmt.Errorf("git: pull activity: query: %w", err)
	}
	defer rows.Close()
	type pulledDelivery struct {
		delivery ActivityDelivery
		policy   ActivityPolicyEvidence
	}
	pulled := make([]pulledDelivery, 0, input.Limit+1)
	for rows.Next() {
		var delivery ActivityDelivery
		var digest string
		var acceptedAt time.Time
		var policyJSON []byte
		if err := rows.Scan(&delivery.SourceRef, &delivery.ActivityJSON, &digest,
			&delivery.Receipt.Sequence, &delivery.Receipt.PolicyVersion, &delivery.Receipt.PolicyDigest, &acceptedAt, &policyJSON); err != nil {
			return PullActivityResult{}, fmt.Errorf("git: pull activity: scan: %w", err)
		}
		activity, err := projectstate.DecodeActivity(delivery.ActivityJSON)
		if err != nil || activity.ID == "" {
			return PullActivityResult{}, fmt.Errorf("git: pull activity: invalid retained activity: %w", ErrActivityReplayConflict)
		}
		if !validRemoteActivityAssurance(activity.Actor.Assurance) {
			return PullActivityResult{}, fmt.Errorf("git: pull activity: invalid retained actor: %w", ErrActivityReplayConflict)
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
		if delivery.Receipt.PolicyVersion > currentPolicy.PolicyVersion {
			return PullActivityResult{}, fmt.Errorf("git: pull activity: future retained policy: %w", ErrActivityReplayConflict)
		}
		if _, err := projectstate.CanonicalActivityReceipt(delivery.Receipt); err != nil {
			return PullActivityResult{}, fmt.Errorf("git: pull activity: invalid retained receipt: %w", ErrActivityReplayConflict)
		}
		policy, err := projectstate.DecodeActivityPolicy(policyJSON)
		if err != nil || policy.PolicyVersion != delivery.Receipt.PolicyVersion ||
			policy.PolicyVersion > currentPolicy.PolicyVersion {
			return PullActivityResult{}, fmt.Errorf("git: pull activity: invalid retained policy: %w", ErrActivityReplayConflict)
		}
		canonicalPolicyJSON, err := projectstate.CanonicalActivityPolicy(policy)
		if err != nil || !bytes.Equal(canonicalPolicyJSON, policyJSON) {
			return PullActivityResult{}, fmt.Errorf("git: pull activity: invalid retained policy: %w", ErrActivityReplayConflict)
		}
		policyDigest, err := projectstate.DigestActivityPolicy(policy)
		if err != nil || policyDigest != delivery.Receipt.PolicyDigest {
			return PullActivityResult{}, fmt.Errorf("git: pull activity: invalid retained policy: %w", ErrActivityReplayConflict)
		}
		pulled = append(pulled, pulledDelivery{delivery: delivery, policy: ActivityPolicyEvidence{
			Stream: input.Stream, PolicyJSON: append([]byte(nil), policyJSON...), PolicyDigest: policyDigest,
		}})
	}
	if err := rows.Err(); err != nil {
		return PullActivityResult{}, fmt.Errorf("git: pull activity: iterate: %w", err)
	}
	hasMore := len(pulled) > input.Limit
	if hasMore {
		pulled = pulled[:input.Limit]
	}
	deliveries := make([]ActivityDelivery, 0, len(pulled))
	historicalByVersion := make(map[int64]ActivityPolicyEvidence, len(pulled))
	for _, value := range pulled {
		deliveries = append(deliveries, value.delivery)
		version := value.delivery.Receipt.PolicyVersion
		if prior, found := historicalByVersion[version]; found &&
			(prior.PolicyDigest != value.policy.PolicyDigest || !bytes.Equal(prior.PolicyJSON, value.policy.PolicyJSON)) {
			return PullActivityResult{}, fmt.Errorf("git: pull activity: inconsistent retained policy: %w", ErrActivityReplayConflict)
		}
		historicalByVersion[version] = value.policy
	}
	versions := make([]int64, 0, len(historicalByVersion))
	for version := range historicalByVersion {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
	historical := make([]ActivityPolicyEvidence, 0, len(versions))
	for _, version := range versions {
		evidence := historicalByVersion[version]
		evidence.PolicyJSON = append([]byte(nil), evidence.PolicyJSON...)
		historical = append(historical, evidence)
	}
	next := highWatermark
	if hasMore && len(deliveries) > 0 {
		next = deliveries[len(deliveries)-1].Receipt.Sequence
	}
	if err := tx.Commit(); err != nil {
		return PullActivityResult{}, fmt.Errorf("git: pull activity: commit: %w", err)
	}
	return PullActivityResult{
		PolicyJSON:         append([]byte(nil), policyJSON...),
		PolicyDigest:       policyDigest,
		HistoricalPolicies: historical,
		Deliveries:         deliveries,
		NextSequence:       next,
		HasMore:            hasMore,
	}, nil
}

func validRemoteActivityAssurance(assurance types.Assurance) bool {
	return assurance == types.AssurancePublicKeyContinuity || assurance == types.AssurancePrivateAuthenticated
}

func activityDatabaseMessage(err error) string {
	var databaseError *pq.Error
	if errors.As(err, &databaseError) {
		return databaseError.Message
	}
	return ""
}
