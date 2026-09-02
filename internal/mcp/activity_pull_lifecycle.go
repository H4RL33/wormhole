package mcp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"

	coregit "github.com/H4RL33/wormhole/internal/core/git"
	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

const maximumActivityWireInteger int64 = 9_007_199_254_740_991

type ActivityPullHandler struct {
	auth     *PublicBoundProofResolver
	activity *coregit.ActivityStore
}

func NewActivityPullHandler(auth *PublicBoundProofResolver, activity *coregit.ActivityStore) (*ActivityPullHandler, error) {
	handler := &ActivityPullHandler{auth: auth, activity: activity}
	if !handler.ready() {
		return nil, errInvalidActivityHandler
	}
	return handler, nil
}

func (h *ActivityPullHandler) ready() bool {
	return h != nil && h.auth != nil && h.activity != nil
}

func (h *ActivityPullHandler) Handle(ctx context.Context, raw json.RawMessage, proof types.PublicRequestProof) (ActivityPullV1Result, error) {
	const operation = "wormhole.activity.pull"
	if !h.ready() {
		return ActivityPullV1Result{}, activityFailure(operation, errInvalidActivityHandler)
	}
	var args ActivityPullV1Args
	if decodeActivityArguments(raw, &args) != nil || args.Version != 1 {
		return ActivityPullV1Result{}, activityDecodeFailure(operation, raw)
	}
	if !types.CanonicalUUID(args.AttachmentRef) || args.AfterSequence < 0 ||
		args.AfterSequence > maximumActivityWireInteger || args.Limit < 1 || args.Limit > 500 {
		return ActivityPullV1Result{}, activityFailure(operation, coregit.ErrActivityCursorConflict)
	}
	canonical := append([]byte(nil), raw...)
	var result ActivityPullV1Result
	err := h.auth.ResolveActivityRead(ctx, operation, args.AttachmentRef, canonical, proof, func(ctx context.Context, tx *sql.Tx, verified VerifiedActivityRead) error {
		coreResult, err := h.activity.PullInTx(ctx, tx, coregit.PullActivityInput{
			Stream: activityStream(verified.Attachment), AttachmentRef: verified.Attachment.AttachmentRef,
			AfterSequence: args.AfterSequence, Limit: args.Limit,
		})
		if err != nil {
			return err
		}
		result, err = activityPullWireResult(coreResult)
		return err
	})
	if err != nil {
		return ActivityPullV1Result{}, activityFailure(operation, err)
	}
	return result, nil
}

func activityPullWireResult(in coregit.PullActivityResult) (ActivityPullV1Result, error) {
	current, err := projectstate.DecodeActivityPolicy(in.PolicyJSON)
	if err != nil {
		return ActivityPullV1Result{}, err
	}
	currentJSON, err := projectstate.CanonicalActivityPolicy(current)
	if err != nil || !bytes.Equal(currentJSON, in.PolicyJSON) {
		return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict
	}
	currentDigest, err := projectstate.DigestActivityPolicy(current)
	if err != nil || currentDigest != in.PolicyDigest {
		return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict
	}

	policies := make([]ActivityPolicyEvidenceV1, 0, len(in.HistoricalPolicies))
	policyDigests := make(map[int64]projectstate.Digest, len(in.HistoricalPolicies))
	var priorPolicy int64
	for _, item := range in.HistoricalPolicies {
		policy, err := projectstate.DecodeActivityPolicy(item.PolicyJSON)
		if err != nil {
			return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict
		}
		canonical, err := projectstate.CanonicalActivityPolicy(policy)
		if err != nil || !bytes.Equal(canonical, item.PolicyJSON) {
			return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict
		}
		digest, err := projectstate.DigestActivityPolicy(policy)
		if err != nil || digest != item.PolicyDigest || policy.PolicyVersion > current.PolicyVersion || policy.PolicyVersion <= priorPolicy {
			return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict
		}
		if _, duplicate := policyDigests[policy.PolicyVersion]; duplicate {
			return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict
		}
		policyDigests[policy.PolicyVersion], priorPolicy = digest, policy.PolicyVersion
		policies = append(policies, ActivityPolicyEvidenceV1{Policy: policy, PolicyDigest: digest})
	}

	deliveries := make([]ActivityDeliveryV1, 0, len(in.Deliveries))
	required := make(map[int64]projectstate.Digest, len(in.Deliveries))
	var priorSequence int64
	if len(in.Deliveries) > 500 {
		return ActivityPullV1Result{}, coregit.ErrActivityCursorConflict
	}
	for _, item := range in.Deliveries {
		if !types.CanonicalUUID(item.SourceRef) {
			return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict
		}
		activity, err := projectstate.DecodeActivity(item.ActivityJSON)
		if err != nil {
			return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict
		}
		canonical, err := projectstate.CanonicalActivity(activity)
		if err != nil || !bytes.Equal(canonical, item.ActivityJSON) {
			return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict
		}
		digest, err := projectstate.DigestActivity(activity)
		if err != nil || digest != item.ActivityDigest || item.Receipt.ActivityID != activity.ID ||
			item.Receipt.ActivityDigest != digest || item.Receipt.Sequence <= priorSequence ||
			item.Receipt.PolicyVersion > current.PolicyVersion {
			return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict
		}
		if _, err := projectstate.CanonicalActivityReceipt(item.Receipt); err != nil {
			return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict
		}
		if existing, ok := required[item.Receipt.PolicyVersion]; ok && existing != item.Receipt.PolicyDigest {
			return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict
		}
		required[item.Receipt.PolicyVersion], priorSequence = item.Receipt.PolicyDigest, item.Receipt.Sequence
		deliveries = append(deliveries, ActivityDeliveryV1{
			SourceRef: item.SourceRef, Activity: activity, ActivityDigest: digest, Receipt: item.Receipt,
		})
	}
	if len(required) != len(policyDigests) {
		return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict
	}
	for version, digest := range required {
		if policyDigests[version] != digest {
			return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict
		}
	}
	if in.NextSequence < priorSequence || (in.HasMore && (len(deliveries) == 0 || in.NextSequence != priorSequence)) {
		return ActivityPullV1Result{}, coregit.ErrActivityCursorConflict
	}
	return ActivityPullV1Result{
		Version: 1, EffectivePolicy: current, PolicyDigest: currentDigest,
		HistoricalPolicies: policies, Deliveries: deliveries,
		NextSequence: in.NextSequence, HasMore: in.HasMore,
	}, nil
}

type ActivityLifecycleHandler struct {
	auth      *PublicBoundProofResolver
	mutations *MutationCoordinator
	activity  *coregit.ActivityStore
}

func NewActivityLifecycleHandler(auth *PublicBoundProofResolver, mutations *MutationCoordinator, activity *coregit.ActivityStore) (*ActivityLifecycleHandler, error) {
	handler := &ActivityLifecycleHandler{auth: auth, mutations: mutations, activity: activity}
	if !handler.ready() {
		return nil, errInvalidActivityHandler
	}
	return handler, nil
}

func (h *ActivityLifecycleHandler) ready() bool {
	return h != nil && h.auth != nil && h.mutations != nil && h.activity != nil
}

func (h *ActivityLifecycleHandler) Handle(ctx context.Context, raw json.RawMessage, proof types.PublicRequestProof) (ActivityLifecycleV1Result, error) {
	const operation = "wormhole.activity.lifecycle"
	if !h.ready() {
		return ActivityLifecycleV1Result{}, activityFailure(operation, errInvalidActivityHandler)
	}
	var args ActivityLifecycleV1Args
	if decodeActivityArguments(raw, &args) != nil || args.Version != 1 {
		return ActivityLifecycleV1Result{}, activityDecodeFailure(operation, raw)
	}
	if !types.CanonicalUUID(args.AttachmentRef) || !types.CanonicalUUID(args.ActivityID) ||
		!types.CanonicalUUID(args.ReferenceID) || args.Kind == "" || args.ExpectedState == "" || args.NextState == "" {
		return ActivityLifecycleV1Result{}, activityFailure(operation, coregit.ErrActivityLifecycleConflict)
	}
	canonical := append([]byte(nil), raw...)
	authorized, err := h.auth.AuthorizeActivityMutation(ctx, operation, args.AttachmentRef, raw, proof)
	if err != nil {
		return ActivityLifecycleV1Result{}, activityFailure(operation, err)
	}
	var result ActivityLifecycleV1Result
	err = h.mutations.Execute(ctx, authorized.Authority, "activity.lifecycle", canonical, func(ctx context.Context, tx *sql.Tx, verified VerifiedMutation) error {
		if err := h.activity.TransitionLifecycleInTx(ctx, tx,
			activityOrigin(verified.Attachment, args.ActivityID),
			coregit.ActivityLifecycleTransition{
				Kind: args.Kind, ReferenceID: args.ReferenceID,
				ExpectedState: args.ExpectedState, NextState: args.NextState,
			}); err != nil {
			return err
		}
		result = ActivityLifecycleV1Result{Version: 1, State: args.NextState}
		return nil
	})
	if err != nil {
		return ActivityLifecycleV1Result{}, activityFailure(operation, err)
	}
	return result, nil
}
