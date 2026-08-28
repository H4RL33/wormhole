package localstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

const (
	maximumActivityInteger  int64 = 9_007_199_254_740_991
	activityTimestampLayout       = "2006-01-02T15:04:05.000000000Z"
)

var (
	ErrActivityPolicyUnavailable = errors.New("localstore: activity policy unavailable")
	ErrActivityPolicyChanged     = errors.New("localstore: activity policy changed")
	ErrActivityNotFound          = errors.New("localstore: activity not found")
	ErrActivityReplayConflict    = errors.New("localstore: activity replay conflict")
	ErrActivityCursorConflict    = errors.New("localstore: activity cursor conflict")
	ErrActivityLifecycleConflict = errors.New("localstore: activity lifecycle conflict")

	activityDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type ActivityRecord struct {
	Key            types.ActivityOriginKey
	Activity       projectstate.ActivityV1
	ActivityJSON   []byte
	ActivityDigest projectstate.Digest
	PolicyVersion  int64
	PolicyDigest   projectstate.Digest
	Sequence       *int64
	AcceptedAt     time.Time
}

type ActivityPolicyRecord struct {
	Route        types.ActivityRouteKey
	Policy       projectstate.EffectiveActivityPolicyV1
	PolicyJSON   []byte
	PolicyDigest projectstate.Digest
	ReceivedAt   time.Time
}

type ActivityPullDelivery struct {
	SourceWorkspaceID types.WorkspaceID
	ActivityJSON      []byte
	ActivityDigest    projectstate.Digest
	ReceiptJSON       []byte
}

type ActivityPullBatch struct {
	PolicyJSON    []byte
	ExpectedAfter int64
	NextSequence  int64
	HasMore       bool
	Deliveries    []ActivityPullDelivery
}

type ActivityLifecycleChange struct {
	Kind, ReferenceID, ExpectedState, NextState string
}

type ActivityRepo struct{ db *sql.DB }

func NewActivityRepo(db *sql.DB) *ActivityRepo { return &ActivityRepo{db: db} }

type activityDB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (r *ActivityRepo) withImmediate(ctx context.Context, operation string, fn func(*sql.Conn) error) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("localstore: %s Activity: %w", operation, ErrActivityNotFound)
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("localstore: %s Activity: acquire connection: %w", operation, err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("localstore: %s Activity: begin: %w", operation, err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	if err := fn(conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("localstore: %s Activity: commit: %w", operation, err)
	}
	committed = true
	return nil
}

func validateActivityRoute(route types.ActivityRouteKey) error {
	if err := route.Validate(); err != nil {
		return fmt.Errorf("localstore: Activity route: %w", ErrActivityNotFound)
	}
	return nil
}

func requireActiveActivityRoute(ctx context.Context, db activityDB, route types.ActivityRouteKey) error {
	var present int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM workspace_fabric_bindings
		WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=?
		AND stream_id=? AND canonical_ref=? AND state='active'`, activityRouteArgs(route)...).Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("localstore: Activity route: %w", ErrActivityNotFound)
	}
	if err != nil {
		return fmt.Errorf("localstore: Activity route lookup: %w", err)
	}
	return nil
}

func requireUnconflictedActivityWorkspace(ctx context.Context, conn *sql.Conn, route types.ActivityRouteKey) error {
	scope := types.WorkspaceScope{ProjectID: route.ProjectID, WorkspaceID: route.WorkspaceID}
	conflicted, err := (&WorkspaceMutationTx{conn: conn, scope: scope}).HasOpenConflicts(ctx)
	if err != nil {
		return fmt.Errorf("localstore: inspect Activity workspace conflicts: %w", err)
	}
	if conflicted {
		return ErrWorkspaceConflicted
	}
	return nil
}

func activityRouteArgs(route types.ActivityRouteKey) []any {
	return []any{route.ProjectID, route.WorkspaceID, route.FabricInstanceID, route.RemoteProjectID, route.StreamID, route.CanonicalRef}
}

func activityOriginArgs(key types.ActivityOriginKey) []any {
	arguments := activityRouteArgs(key.Route)
	return append(arguments, key.SourceWorkspaceID, key.ActivityID)
}

func databaseActivityNow(ctx context.Context, db activityDB) (time.Time, error) {
	var encoded string
	if err := db.QueryRowContext(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&encoded); err != nil {
		return time.Time{}, fmt.Errorf("localstore: Activity clock: %w", err)
	}
	now, err := time.Parse("2006-01-02 15:04:05", encoded)
	if err != nil {
		return time.Time{}, fmt.Errorf("localstore: Activity clock: %w", err)
	}
	return now.UTC(), nil
}

func sqliteActivityTimestamp(value time.Time) string {
	return value.UTC().Format(activityTimestampLayout)
}

func parseSQLiteActivityTimestamp(value string) (time.Time, error) {
	parsed, err := time.Parse(activityTimestampLayout, value)
	if err != nil || parsed.Format(activityTimestampLayout) != value {
		return time.Time{}, ErrActivityReplayConflict
	}
	return parsed.UTC(), nil
}

func canonicalActivityEvidence(activity projectstate.ActivityV1) ([]byte, projectstate.Digest, []byte, error) {
	canonical, err := projectstate.CanonicalActivity(activity)
	if err != nil {
		return nil, "", nil, err
	}
	digest, err := projectstate.DigestActivity(activity)
	if err != nil {
		return nil, "", nil, err
	}
	actor, err := projectstate.CanonicalJSON(activity.Actor)
	if err != nil {
		return nil, "", nil, projectstate.ErrInvalidActivity
	}
	return canonical, digest, actor, nil
}

func validRemoteActivity(activity projectstate.ActivityV1) bool {
	return activity.Actor.Assurance == types.AssurancePublicKeyContinuity ||
		activity.Actor.Assurance == types.AssurancePrivateAuthenticated
}

type activityProjection struct {
	eventChannelID, eventActorID, eventType sql.NullString
	eventPayload                            []byte
	eventNote                               sql.NullString
	eventCreatedAt                          sql.NullString
	lifecycleKind, lifecycleReference       sql.NullString
}

func activityProjectionArgs(activity projectstate.ActivityV1) []any {
	var eventChannelID, eventActorID, eventType, eventPayload, eventNote, eventCreatedAt any
	if activity.Event != nil {
		eventChannelID, eventActorID, eventType = activity.Event.ChannelID, activity.Event.ActorID, activity.Event.EventType
		eventPayload = []byte(activity.Event.Payload)
		if activity.Event.Note != nil {
			eventNote = *activity.Event.Note
		}
		eventCreatedAt = sqliteActivityTimestamp(activity.Event.CreatedAt)
	}
	var lifecycleKind, lifecycleReference any
	if activity.Lifecycle != nil {
		lifecycleKind, lifecycleReference = string(activity.Lifecycle.Kind), activity.Lifecycle.ReferenceID
	}
	return []any{eventChannelID, eventActorID, eventType, eventPayload, eventNote, eventCreatedAt, lifecycleKind, lifecycleReference}
}

func (p activityProjection) matches(activity projectstate.ActivityV1) bool {
	if activity.Event == nil {
		if p.eventChannelID.Valid || p.eventActorID.Valid || p.eventType.Valid || p.eventPayload != nil || p.eventNote.Valid || p.eventCreatedAt.Valid {
			return false
		}
	} else if !p.eventChannelID.Valid || p.eventChannelID.String != activity.Event.ChannelID ||
		!p.eventActorID.Valid || p.eventActorID.String != activity.Event.ActorID ||
		!p.eventType.Valid || p.eventType.String != activity.Event.EventType ||
		!bytes.Equal(p.eventPayload, activity.Event.Payload) ||
		p.eventNote.Valid != (activity.Event.Note != nil) ||
		(activity.Event.Note != nil && p.eventNote.String != *activity.Event.Note) ||
		!p.eventCreatedAt.Valid || p.eventCreatedAt.String != sqliteActivityTimestamp(activity.Event.CreatedAt) {
		return false
	}
	if activity.Lifecycle == nil {
		return !p.lifecycleKind.Valid && !p.lifecycleReference.Valid
	}
	return p.lifecycleKind.Valid && p.lifecycleKind.String == string(activity.Lifecycle.Kind) &&
		p.lifecycleReference.Valid && p.lifecycleReference.String == activity.Lifecycle.ReferenceID
}

func readActivityRecord(ctx context.Context, db activityDB, key types.ActivityOriginKey, policyVersion int64, policyDigest projectstate.Digest) (ActivityRecord, error) {
	evidence, err := newActivityEvidenceLoader().load(ctx, db, key)
	if err != nil {
		return ActivityRecord{}, err
	}
	return evidence.recordForPolicy(policyVersion, policyDigest)
}

func insertActivityLedger(ctx context.Context, db activityDB, key types.ActivityOriginKey, activity projectstate.ActivityV1, raw []byte, digest projectstate.Digest, actorJSON []byte, acceptedAt time.Time, sequence *int64) error {
	arguments := activityOriginArgs(key)
	arguments = append(arguments, string(activity.Class), raw, string(digest), actorJSON)
	arguments = append(arguments, activityProjectionArgs(activity)...)
	arguments = append(arguments, sqliteActivityTimestamp(activity.CreatedAt), sqliteActivityTimestamp(acceptedAt), sequence)
	_, err := db.ExecContext(ctx, `INSERT INTO activity_ledger
		(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,source_workspace_id,activity_id,
		 activity_class,canonical_activity_json,activity_digest,source_actor_json,
		 event_channel_id,event_actor_id,event_type,event_payload,event_note,event_created_at,
		 embedded_lifecycle_kind,embedded_lifecycle_reference_id,created_at,accepted_at,sequence)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, arguments...)
	if err != nil {
		return fmt.Errorf("localstore: insert Activity ledger: %w", err)
	}
	return nil
}

func cloneActivityRecord(record ActivityRecord) ActivityRecord {
	record.ActivityJSON = append([]byte(nil), record.ActivityJSON...)
	if record.Sequence != nil {
		value := *record.Sequence
		record.Sequence = &value
	}
	if record.Activity.Event != nil {
		event := *record.Activity.Event
		event.Payload = append([]byte(nil), event.Payload...)
		if event.Note != nil {
			note := *event.Note
			event.Note = &note
		}
		record.Activity.Event = &event
	}
	if record.Activity.Lifecycle != nil {
		lifecycle := *record.Activity.Lifecycle
		record.Activity.Lifecycle = &lifecycle
	}
	return record
}
