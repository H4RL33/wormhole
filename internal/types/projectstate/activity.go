package projectstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/H4RL33/wormhole/internal/types"
)

const (
	activitySchemaVersion           = 1
	maximumJSONSafeInteger    int64 = 9_007_199_254_740_991
	ordinaryMaxAgeSeconds     int64 = 2_592_000
	ordinaryMaxRows           int64 = 10_000
	terminalDefaultAgeSeconds int64 = 2_592_000
	terminalMaximumAgeSeconds int64 = 31_536_000
)

var (
	ErrInvalidActivity        = errors.New("projectstate: invalid activity")
	ErrUnknownActivityVersion = errors.New("projectstate: unknown activity version")
	ErrInvalidActivityPolicy  = errors.New("projectstate: invalid activity policy")
)

type ActivityClassV1 string

const (
	ActivityPresenceV1  ActivityClassV1 = "presence"
	ActivityOrdinaryV1  ActivityClassV1 = "ordinary"
	ActivityLifecycleV1 ActivityClassV1 = "lifecycle"
)

type ActivityLifecycleKindV1 string

const (
	ActivityLifecycleDeliveryV1 ActivityLifecycleKindV1 = "delivery"
	ActivityLifecycleConflictV1 ActivityLifecycleKindV1 = "conflict"
	ActivityLifecycleRecoveryV1 ActivityLifecycleKindV1 = "recovery"
	ActivityLifecycleReceiptV1  ActivityLifecycleKindV1 = "receipt"
)

type ActivityLifecycleProjectionV1 struct {
	Kind        ActivityLifecycleKindV1 `json:"kind"`
	ReferenceID string                  `json:"reference_id"`
}

type ActivityEventProjectionV1 struct {
	ChannelID string          `json:"channel_id"`
	ActorID   string          `json:"actor_id"`
	EventType string          `json:"event_type"`
	Payload   json.RawMessage `json:"payload"`
	Note      *string         `json:"note"`
	CreatedAt time.Time       `json:"created_at"`
}

type ActivityV1 struct {
	SchemaVersion int                            `json:"schema_version"`
	ID            string                         `json:"id"`
	Class         ActivityClassV1                `json:"class"`
	Actor         types.ActorEnvelope            `json:"actor"`
	Event         *ActivityEventProjectionV1     `json:"event,omitempty"`
	Lifecycle     *ActivityLifecycleProjectionV1 `json:"lifecycle,omitempty"`
	CreatedAt     time.Time                      `json:"created_at"`
}

type EffectiveActivityPolicyV1 struct {
	SchemaVersion             int   `json:"schema_version"`
	PolicyVersion             int64 `json:"policy_version"`
	OrdinaryMaxAgeSeconds     int64 `json:"ordinary_max_age_seconds"`
	OrdinaryMaxRows           int64 `json:"ordinary_max_rows"`
	TerminalDefaultAgeSeconds int64 `json:"terminal_default_age_seconds"`
	TerminalMaximumAgeSeconds int64 `json:"terminal_maximum_age_seconds"`
	TerminalRetentionSeconds  int64 `json:"terminal_retention_seconds"`
}

type ActivityReceiptV1 struct {
	SchemaVersion  int       `json:"schema_version"`
	ActivityID     string    `json:"activity_id"`
	ActivityDigest Digest    `json:"activity_digest"`
	Sequence       int64     `json:"sequence"`
	PolicyVersion  int64     `json:"policy_version"`
	PolicyDigest   Digest    `json:"policy_digest"`
	AcceptedAt     time.Time `json:"accepted_at"`
}

func CanonicalActivity(activity ActivityV1) ([]byte, error) {
	if err := validateActivity(activity); err != nil {
		return nil, err
	}
	canonical, err := CanonicalJSON(activity)
	if err != nil {
		return nil, ErrInvalidActivity
	}
	return canonical, nil
}

func DecodeActivity(raw []byte) (ActivityV1, error) {
	var activity ActivityV1
	if err := decodeClosedActivityJSON(raw, &activity); err != nil {
		return ActivityV1{}, ErrInvalidActivity
	}
	if err := validateActivity(activity); err != nil {
		return ActivityV1{}, err
	}
	canonical, err := CanonicalActivity(activity)
	if err != nil {
		return ActivityV1{}, err
	}
	if !bytes.Equal(canonical, raw) {
		return ActivityV1{}, ErrInvalidActivity
	}
	return activity, nil
}

func DigestActivity(activity ActivityV1) (Digest, error) {
	canonical, err := CanonicalActivity(activity)
	if err != nil {
		return "", err
	}
	return digestCanonicalBytes(canonical), nil
}

func CanonicalActivityPolicy(policy EffectiveActivityPolicyV1) ([]byte, error) {
	if err := validateActivityPolicy(policy); err != nil {
		return nil, err
	}
	canonical, err := CanonicalJSON(policy)
	if err != nil {
		return nil, ErrInvalidActivityPolicy
	}
	return canonical, nil
}

func DecodeActivityPolicy(raw []byte) (EffectiveActivityPolicyV1, error) {
	var policy EffectiveActivityPolicyV1
	if err := decodeClosedActivityJSON(raw, &policy); err != nil {
		return EffectiveActivityPolicyV1{}, ErrInvalidActivityPolicy
	}
	if err := validateActivityPolicy(policy); err != nil {
		return EffectiveActivityPolicyV1{}, err
	}
	canonical, err := CanonicalActivityPolicy(policy)
	if err != nil {
		return EffectiveActivityPolicyV1{}, err
	}
	if !bytes.Equal(canonical, raw) {
		return EffectiveActivityPolicyV1{}, ErrInvalidActivityPolicy
	}
	return policy, nil
}

func DigestActivityPolicy(policy EffectiveActivityPolicyV1) (Digest, error) {
	canonical, err := CanonicalActivityPolicy(policy)
	if err != nil {
		return "", err
	}
	return digestCanonicalBytes(canonical), nil
}

func CanonicalActivityReceipt(receipt ActivityReceiptV1) ([]byte, error) {
	if err := validateActivityReceipt(receipt); err != nil {
		return nil, err
	}
	canonical, err := CanonicalJSON(receipt)
	if err != nil {
		return nil, ErrInvalidActivity
	}
	return canonical, nil
}

func DecodeActivityReceipt(raw []byte) (ActivityReceiptV1, error) {
	var receipt ActivityReceiptV1
	if err := decodeClosedActivityJSON(raw, &receipt); err != nil {
		return ActivityReceiptV1{}, ErrInvalidActivity
	}
	if err := validateActivityReceipt(receipt); err != nil {
		return ActivityReceiptV1{}, err
	}
	canonical, err := CanonicalActivityReceipt(receipt)
	if err != nil {
		return ActivityReceiptV1{}, err
	}
	if !bytes.Equal(canonical, raw) {
		return ActivityReceiptV1{}, ErrInvalidActivity
	}
	return receipt, nil
}

func validateActivity(activity ActivityV1) error {
	if activity.SchemaVersion != activitySchemaVersion {
		if activity.SchemaVersion == 0 {
			return ErrInvalidActivity
		}
		return fmt.Errorf("%w: activity", ErrUnknownActivityVersion)
	}
	if !types.CanonicalUUID(activity.ID) || !validUTC(activity.CreatedAt) {
		return ErrInvalidActivity
	}
	if err := activity.Actor.ValidateHistorical(); err != nil {
		return ErrInvalidActivity
	}
	if activity.Actor.Assurance == types.AssuranceLegacy || activity.Actor.Assurance == types.AssuranceUnknown {
		return ErrInvalidActivity
	}
	if !activity.CreatedAt.Equal(activity.Actor.OccurredAt) {
		return ErrInvalidActivity
	}

	switch activity.Class {
	case ActivityPresenceV1:
		if activity.Event != nil || activity.Lifecycle != nil {
			return ErrInvalidActivity
		}
	case ActivityOrdinaryV1:
		if activity.Event == nil || activity.Lifecycle != nil {
			return ErrInvalidActivity
		}
	case ActivityLifecycleV1:
		if activity.Lifecycle == nil {
			return ErrInvalidActivity
		}
		if err := validateActivityLifecycle(*activity.Lifecycle); err != nil {
			return err
		}
	default:
		return ErrInvalidActivity
	}
	if activity.Event != nil {
		return validateActivityEvent(activity.Actor, activity.CreatedAt, *activity.Event)
	}
	return nil
}

func validateActivityLifecycle(lifecycle ActivityLifecycleProjectionV1) error {
	switch lifecycle.Kind {
	case ActivityLifecycleDeliveryV1, ActivityLifecycleConflictV1, ActivityLifecycleRecoveryV1, ActivityLifecycleReceiptV1:
	default:
		return ErrInvalidActivity
	}
	if !types.CanonicalUUID(lifecycle.ReferenceID) {
		return ErrInvalidActivity
	}
	return nil
}

func validateActivityEvent(actor types.ActorEnvelope, createdAt time.Time, event ActivityEventProjectionV1) error {
	if !types.CanonicalUUID(event.ChannelID) || !types.CanonicalUUID(event.ActorID) || event.ActorID != actor.PrincipalID() || !validUTC(event.CreatedAt) || !event.CreatedAt.Equal(createdAt) {
		return ErrInvalidActivity
	}
	if event.Note != nil && !validActivityText(*event.Note) {
		return ErrInvalidActivity
	}

	switch event.EventType {
	case "task.status_changed":
		var payload types.TaskStatusChangedPayload
		if !decodeActivityPayload(event.Payload, &payload) || !types.CanonicalUUID(payload.TaskID) || !validTaskStatus(payload.FromStatus) || !validTaskStatus(payload.ToStatus) || payload.FromStatus == payload.ToStatus {
			return ErrInvalidActivity
		}
	case "review.requested":
		var payload types.ReviewRequestedPayload
		if !decodeActivityPayload(event.Payload, &payload) || !validActivityText(payload.PRUrl) || !validActivityText(payload.Repo) || !validActivityText(payload.Author) {
			return ErrInvalidActivity
		}
	case "build.failed":
		var payload types.BuildFailedPayload
		if !decodeActivityPayload(event.Payload, &payload) || !validActivityText(payload.Repo) || !validActivityText(payload.CommitSHA) || !validActivityText(payload.Error) {
			return ErrInvalidActivity
		}
	case "discovery.logged":
		var payload types.DiscoveryLoggedPayload
		if !decodeActivityPayload(event.Payload, &payload) || !validActivityText(payload.Summary) || !validActivityText(payload.Detail) {
			return ErrInvalidActivity
		}
	case "message.posted":
		var payload types.MessagePostedPayload
		if !decodeActivityPayload(event.Payload, &payload) || !validActivityText(payload.Text) || event.Note == nil {
			return ErrInvalidActivity
		}
	default:
		return ErrInvalidActivity
	}
	return nil
}

func validateActivityPolicy(policy EffectiveActivityPolicyV1) error {
	if policy.SchemaVersion != activitySchemaVersion {
		if policy.SchemaVersion != 0 {
			return fmt.Errorf("%w: policy", ErrUnknownActivityVersion)
		}
		return ErrInvalidActivityPolicy
	}
	if policy.PolicyVersion < 1 || policy.PolicyVersion > maximumJSONSafeInteger ||
		policy.OrdinaryMaxAgeSeconds != ordinaryMaxAgeSeconds ||
		policy.OrdinaryMaxRows != ordinaryMaxRows ||
		policy.TerminalDefaultAgeSeconds != terminalDefaultAgeSeconds ||
		policy.TerminalMaximumAgeSeconds != terminalMaximumAgeSeconds ||
		policy.TerminalRetentionSeconds < terminalDefaultAgeSeconds ||
		policy.TerminalRetentionSeconds > terminalMaximumAgeSeconds {
		return ErrInvalidActivityPolicy
	}
	return nil
}

func validateActivityReceipt(receipt ActivityReceiptV1) error {
	if receipt.SchemaVersion != activitySchemaVersion {
		if receipt.SchemaVersion != 0 {
			return fmt.Errorf("%w: receipt", ErrUnknownActivityVersion)
		}
		return ErrInvalidActivity
	}
	if !types.CanonicalUUID(receipt.ActivityID) || !contentDigestPattern.MatchString(string(receipt.ActivityDigest)) ||
		receipt.Sequence < 1 || receipt.Sequence > maximumJSONSafeInteger ||
		receipt.PolicyVersion < 1 || receipt.PolicyVersion > maximumJSONSafeInteger ||
		!contentDigestPattern.MatchString(string(receipt.PolicyDigest)) || !validUTC(receipt.AcceptedAt) {
		return ErrInvalidActivity
	}
	return nil
}

func decodeClosedActivityJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func decodeActivityPayload(raw json.RawMessage, destination any) bool {
	if len(raw) == 0 || decodeClosedActivityJSON(raw, destination) != nil {
		return false
	}
	canonical, err := CanonicalJSON(raw)
	if err != nil {
		return false
	}
	canonical = bytes.TrimSuffix(canonical, []byte{'\n'})
	return bytes.Equal(canonical, raw)
}

func validTaskStatus(status string) bool {
	switch status {
	case "todo", "wip", "blocked", "done":
		return true
	default:
		return false
	}
}

func validActivityText(value string) bool {
	return value != "" && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsRune(value, 0)
}
