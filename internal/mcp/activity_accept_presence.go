package mcp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"

	coregit "github.com/H4RL33/wormhole/internal/core/git"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

var errInvalidActivityHandler = errors.New("mcp: invalid Activity handler")

type ActivityAcceptHandler struct {
	auth      *PublicBoundProofResolver
	mutations *MutationCoordinator
	activity  *coregit.ActivityStore
}

func NewActivityAcceptHandler(auth *PublicBoundProofResolver, mutations *MutationCoordinator, activity *coregit.ActivityStore) (*ActivityAcceptHandler, error) {
	handler := &ActivityAcceptHandler{auth: auth, mutations: mutations, activity: activity}
	if !handler.ready() {
		return nil, errInvalidActivityHandler
	}
	return handler, nil
}

func (h *ActivityAcceptHandler) ready() bool {
	return h != nil && h.auth != nil && h.mutations != nil && h.activity != nil
}

func (h *ActivityAcceptHandler) Handle(ctx context.Context, raw json.RawMessage, proof types.PublicRequestProof) (any, error) {
	const operation = "wormhole.activity.accept"
	if !h.ready() {
		return nil, activityFailure(operation, errInvalidActivityHandler)
	}
	var args ActivityAcceptV1Args
	if decodeActivityArguments(raw, &args) != nil || args.Version != 1 {
		return nil, activityDecodeFailure(operation, raw)
	}
	_, err := validateActivityAcceptArguments(args)
	if err != nil {
		return nil, activityFailure(operation, err)
	}
	// Keep the wire value intact: the coordinator journals the authenticated
	// canonical request, so validation must never normalise or rewrite it.
	canonical := append([]byte(nil), raw...)
	authorized, err := h.auth.AuthorizeActivityMutation(ctx, operation, args.AttachmentRef, raw, proof)
	if err != nil {
		return nil, activityFailure(operation, err)
	}
	var accepted ActivityAcceptedV1Result
	err = h.mutations.Execute(ctx, authorized.Authority, "activity.accept", canonical, func(ctx context.Context, tx *sql.Tx, verified VerifiedMutation) error {
		if err := validateHistoricalActivityAuthorshipInTx(ctx, tx, h.auth.identity, authorityEvidence(coregit.StreamAttachmentState{Attachment: verified.Attachment, State: verified.State}), args.Activity.Actor, verified.Scope.Actor); err != nil {
			return err
		}
		receipt, err := h.activity.AcceptInTx(ctx, tx, coregit.AcceptActivityInput{
			Key:           activityOrigin(verified.Attachment, args.Activity.ID),
			Activity:      args.Activity,
			IssuedActor:   args.Activity.Actor,
			PolicyVersion: args.PolicyVersion,
			PolicyDigest:  args.PolicyDigest,
		})
		if err != nil {
			return err
		}
		policy, err := h.activity.CurrentPolicyInTx(ctx, tx, activityStream(verified.Attachment))
		if err != nil {
			return err
		}
		policyDigest, err := projectstate.DigestActivityPolicy(policy)
		if err != nil {
			return err
		}
		accepted = ActivityAcceptedV1Result{
			Version: 1, Status: "accepted", Receipt: receipt,
			EffectiveActivityPolicy: policy, PolicyDigest: policyDigest,
		}
		return nil
	})
	if err == nil {
		return accepted, nil
	}
	var changed *coregit.ActivityPolicyChangedError
	if errors.As(err, &changed) {
		policy, decodeErr := projectstate.DecodeActivityPolicy(append([]byte(nil), changed.CurrentPolicyJSON...))
		if decodeErr != nil {
			return nil, activityFailure(operation, decodeErr)
		}
		policyDigest, digestErr := projectstate.DigestActivityPolicy(policy)
		if digestErr != nil || policyDigest != changed.CurrentDigest {
			return nil, activityFailure(operation, coregit.ErrActivityPolicyUnavailable)
		}
		return ActivityPolicyChangedV1Result{
			Version: 1, Status: "policy_changed",
			EffectiveActivityPolicy: policy, PolicyDigest: policyDigest,
		}, nil
	}
	return nil, activityFailure(operation, err)
}

type ActivityPresenceHandler struct {
	auth     *PublicBoundProofResolver
	activity *coregit.ActivityStore
}

func NewActivityPresenceHandler(auth *PublicBoundProofResolver, activity *coregit.ActivityStore) (*ActivityPresenceHandler, error) {
	handler := &ActivityPresenceHandler{auth: auth, activity: activity}
	if !handler.ready() {
		return nil, errInvalidActivityHandler
	}
	return handler, nil
}

func (h *ActivityPresenceHandler) ready() bool {
	return h != nil && h.auth != nil && h.activity != nil
}

func (h *ActivityPresenceHandler) Handle(ctx context.Context, raw json.RawMessage, proof types.PublicRequestProof) (any, error) {
	const operation = "wormhole.activity.presence"
	if !h.ready() {
		return nil, activityFailure(operation, errInvalidActivityHandler)
	}
	var args ActivityPresenceV1Args
	if decodeActivityArguments(raw, &args) != nil || args.Version != 1 {
		return nil, activityDecodeFailure(operation, raw)
	}
	if _, err := validateActivityAcceptArguments(args); err != nil || args.Activity.Class != projectstate.ActivityPresenceV1 {
		return nil, activityFailure(operation, projectstate.ErrInvalidActivity)
	}
	var result any
	err := h.auth.ResolveActivityRead(ctx, operation, args.AttachmentRef, raw, proof, func(ctx context.Context, tx *sql.Tx, verified VerifiedActivityRead) error {
		if err := validateHistoricalActivityAuthorshipInTx(ctx, tx, h.auth.identity, authorityEvidence(coregit.StreamAttachmentState{Attachment: verified.Attachment, State: verified.State}), args.Activity.Actor, verified.Authority.Actor); err != nil {
			return err
		}
		policy, err := h.activity.CurrentPolicyInTx(ctx, tx, activityStream(verified.Attachment))
		if err != nil {
			return err
		}
		policyDigest, err := projectstate.DigestActivityPolicy(policy)
		if err != nil {
			return err
		}
		if args.PolicyVersion != policy.PolicyVersion || args.PolicyDigest != policyDigest {
			result = ActivityPolicyChangedV1Result{
				Version: 1, Status: "policy_changed",
				EffectiveActivityPolicy: policy, PolicyDigest: policyDigest,
			}
		} else {
			result = ActivityPresenceAcceptedV1Result{Version: 1, Status: "accepted"}
		}
		return nil
	})
	if err != nil {
		return nil, activityFailure(operation, err)
	}
	return result, nil
}

// validateHistoricalActivityAuthorshipInTx proves the immutable embedded actor
// at the Activity occurrence time, then compares only stable identity with the
// separately authenticated current request actor.
func validateHistoricalActivityAuthorshipInTx(ctx context.Context, tx *sql.Tx, identities *identity.Store, evidence identity.PublicAuthorityEvidence, embedded, current types.ActorEnvelope) error {
	if tx == nil || identities == nil || embedded.ValidateHistorical() != nil || current.Validate() != nil ||
		embedded.Assurance != types.AssurancePublicKeyContinuity || current.Assurance != types.AssurancePublicKeyContinuity {
		return projectstate.ErrInvalidActivity
	}
	historical := types.ActorEnvelope{}
	switch embedded.ActorKind {
	case types.ActorHuman:
		historical = types.ActorEnvelope{
			ActorKind: embedded.ActorKind, HumanPrincipalID: embedded.HumanPrincipalID,
			Assurance: types.AssurancePublicKeyContinuity, OccurredAt: embedded.OccurredAt,
		}
	case types.ActorAgent:
		var err error
		historical, err = identities.ResolveHistoricalPublicSessionActorInTx(ctx, tx, evidence, embedded.SessionID, embedded.OccurredAt)
		if err != nil {
			return projectstate.ErrInvalidActivity
		}
	default:
		return projectstate.ErrInvalidActivity
	}
	if historical != embedded || !sameStableActivityAttribution(historical, current) {
		return projectstate.ErrInvalidActivity
	}
	return nil
}

func sameStableActivityAttribution(historical, current types.ActorEnvelope) bool {
	if historical.ActorKind != current.ActorKind {
		return false
	}
	switch historical.ActorKind {
	case types.ActorHuman:
		return historical.HumanPrincipalID == current.HumanPrincipalID
	case types.ActorAgent:
		return historical.AgentID == current.AgentID && historical.AccountableHumanID == current.AccountableHumanID
	default:
		return false
	}
}

func validateActivityAcceptArguments(args ActivityAcceptV1Args) (projectstate.Digest, error) {
	if !types.CanonicalUUID(args.AttachmentRef) || args.PolicyVersion < 1 || args.PolicyVersion > 9_007_199_254_740_991 || !validActivityWireDigest(args.PolicyDigest) {
		return "", projectstate.ErrInvalidActivity
	}
	if _, err := projectstate.CanonicalActivity(args.Activity); err != nil {
		return "", projectstate.ErrInvalidActivity
	}
	digest, err := projectstate.DigestActivity(args.Activity)
	if err != nil || digest != args.ActivityDigest {
		return "", projectstate.ErrInvalidActivity
	}
	return digest, nil
}

func decodeActivityArguments(raw json.RawMessage, destination any) error {
	if decodePublicArguments(raw, destination) != nil || !isCanonicalJSONObject(raw) {
		return errInvalidActivityHandler
	}
	return nil
}

var activityWireDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func validActivityWireDigest(digest projectstate.Digest) bool {
	return activityWireDigestPattern.MatchString(string(digest))
}

func activityDecodeFailure(operation string, raw json.RawMessage) error {
	fields, err := decodeUniqueJSONObject(raw, nil)
	if err != nil || fields["version"] == nil {
		return syncReadFailure(operation, "invalid_request")
	}
	versionRaw := bytes.TrimSpace(fields["version"])
	if len(versionRaw) == 0 || bytes.Equal(versionRaw, []byte("null")) || bytes.ContainsAny(versionRaw, ".eE") {
		return syncReadFailure(operation, "invalid_request")
	}
	version, err := strconv.Atoi(string(versionRaw))
	if err != nil {
		return syncReadFailure(operation, "invalid_request")
	}
	if version != 1 {
		return syncReadFailure(operation, "unknown_activity_version")
	}
	return syncReadFailure(operation, "invalid_request")
}

func activityErrorCode(err error) string {
	switch {
	case errors.Is(err, identity.ErrPublicAuthentication), errors.Is(err, identity.ErrPublicNonceReplay), errors.Is(err, identity.ErrInvalidPublicIdentity):
		return "authentication_failed"
	case errors.Is(err, coregit.ErrStreamNotFound):
		return "attachment_not_found"
	case errors.Is(err, projectstate.ErrInvalidActivity), errors.Is(err, projectstate.ErrInvalidActorEnvelope):
		return "invalid_activity"
	case errors.Is(err, coregit.ErrActivityPolicyUnavailable):
		return "activity_policy_required"
	case errors.Is(err, coregit.ErrActivityPolicyChanged):
		return "activity_policy_changed"
	case errors.Is(err, coregit.ErrActivityNotFound):
		return "activity_not_found"
	case errors.Is(err, coregit.ErrActivityReplayConflict):
		return "activity_replay_conflict"
	case errors.Is(err, coregit.ErrActivityCursorConflict):
		return "activity_cursor_invalid"
	case errors.Is(err, coregit.ErrActivityLifecycleConflict):
		return "activity_lifecycle_conflict"
	default:
		return "internal_error"
	}
}

func activityFailure(operation string, cause error) error {
	return syncReadFailure(operation, activityErrorCode(cause))
}

func activityStream(attachment coregit.StreamAttachment) coregit.FabricActivityStreamKey {
	return coregit.FabricActivityStreamKey{
		ProjectID: attachment.Key.ProjectID, FabricInstanceID: attachment.Key.FabricInstanceID,
		StreamID: attachment.Key.StreamID, CanonicalRef: attachment.CanonicalRef,
	}
}

func activityOrigin(attachment coregit.StreamAttachment, activityID string) coregit.FabricActivityOriginKey {
	return coregit.FabricActivityOriginKey{
		Stream: activityStream(attachment), SourceWorkspaceID: string(attachment.WorkspaceID), ActivityID: activityID,
	}
}
