package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

type ActivityAcceptV1Args = projectstate.ActivityAcceptV1Args
type ActivityAcceptedV1Result = projectstate.ActivityAcceptedV1Result
type ActivityPolicyChangedV1Result = projectstate.ActivityPolicyChangedV1Result
type ActivityPresenceV1Args = projectstate.ActivityPresenceV1Args
type ActivityPresenceAcceptedV1Result = projectstate.ActivityPresenceAcceptedV1Result
type ActivityPullV1Args = projectstate.ActivityPullV1Args
type ActivityPolicyEvidenceV1 = projectstate.ActivityPolicyEvidenceV1
type ActivityDeliveryV1 = projectstate.ActivityDeliveryV1
type ActivityPullV1Result = projectstate.ActivityPullV1Result
type ActivityLifecycleV1Args = projectstate.ActivityLifecycleV1Args
type ActivityLifecycleV1Result = projectstate.ActivityLifecycleV1Result

type ActivityPublicCaller interface {
	// CallActivity owns proof creation and transport. It returns JSON-RPC's raw
	// tools/call result object. Stage 3 Slice 7 supplies the signing implementation.
	CallActivity(ctx context.Context, profile types.FabricProfile, credentialRef, tool string, canonicalArguments json.RawMessage) (json.RawMessage, error)
}

type ActivityPublicClientFactory struct {
	caller ActivityPublicCaller
}

func NewActivityPublicClientFactory(caller ActivityPublicCaller) (*ActivityPublicClientFactory, error) {
	if activityNilDependency(caller) {
		return nil, ErrFabricUnavailable
	}
	return &ActivityPublicClientFactory{caller: caller}, nil
}

func (f *ActivityPublicClientFactory) Client(_ context.Context, profile types.FabricProfile, credentialMaterial string) (ActivityFabricClient, error) {
	if f == nil || activityNilDependency(f.caller) || profile.Validate() != nil || profile.Mode != types.FabricModePublic || profile.CredentialRef == "" || credentialMaterial == "" {
		return nil, ErrFabricUnavailable
	}
	return &ActivityPublicClient{caller: f.caller, profile: profile, credentialRef: profile.CredentialRef}, nil
}

type ActivityPublicClient struct {
	caller        ActivityPublicCaller
	profile       types.FabricProfile
	credentialRef string
}

var errInvalidActivityMCPResult = errors.New("sync: invalid Activity MCP result")

type activityToolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type activityToolResult struct {
	Content []activityToolContent `json:"content"`
	IsError bool                  `json:"isError,omitempty"`
}

type activityPublicFailure struct {
	Code      string `json:"code"`
	Operation string `json:"operation"`
}

func (c *ActivityPublicClient) Accept(ctx context.Context, request ActivityAcceptRequest) (ActivityAcceptResponse, error) {
	activity, err := projectstate.DecodeActivity(request.ActivityJSON)
	if err != nil {
		return ActivityAcceptResponse{}, err
	}
	canonical, err := projectstate.CanonicalActivity(activity)
	if err != nil || !bytes.Equal(canonical, request.ActivityJSON) {
		return ActivityAcceptResponse{}, projectstate.ErrInvalidActivity
	}
	digest, err := projectstate.DigestActivity(activity)
	if err != nil || digest != request.ActivityDigest {
		return ActivityAcceptResponse{}, projectstate.ErrInvalidActivity
	}
	raw, err := c.call(ctx, "wormhole.activity.accept", ActivityAcceptV1Args{
		Version:        1,
		AttachmentRef:  request.AttachmentRef,
		PolicyVersion:  request.PolicyVersion,
		PolicyDigest:   request.PolicyDigest,
		Activity:       activity,
		ActivityDigest: digest,
	})
	if err != nil {
		return ActivityAcceptResponse{}, err
	}
	fields, err := activityResultFields(raw)
	if err != nil {
		return ActivityAcceptResponse{}, ErrFabricUnavailable
	}
	switch string(fields["status"]) {
	case `"accepted"`:
		var value ActivityAcceptedV1Result
		if decodeClosedActivityJSON(raw, &value) != nil || value.Version != 1 || value.Status != "accepted" {
			return ActivityAcceptResponse{}, ErrFabricUnavailable
		}
		policyJSON, policyDigest, err := canonicalRuntimePolicy(value.EffectiveActivityPolicy, value.PolicyDigest)
		if err != nil {
			return ActivityAcceptResponse{}, err
		}
		if _, err := projectstate.CanonicalActivityReceipt(value.Receipt); err != nil ||
			value.Receipt.ActivityID != activity.ID || value.Receipt.ActivityDigest != digest ||
			value.Receipt.PolicyVersion != value.EffectiveActivityPolicy.PolicyVersion ||
			value.Receipt.PolicyDigest != policyDigest {
			return ActivityAcceptResponse{}, localstore.ErrActivityReplayConflict
		}
		return ActivityAcceptResponse{
			Receipt: value.Receipt, PolicyJSON: policyJSON, PolicyDigest: policyDigest,
		}, nil
	case `"policy_changed"`:
		var value ActivityPolicyChangedV1Result
		if decodeClosedActivityJSON(raw, &value) != nil || value.Version != 1 || value.Status != "policy_changed" {
			return ActivityAcceptResponse{}, ErrFabricUnavailable
		}
		policyJSON, policyDigest, err := canonicalRuntimePolicy(value.EffectiveActivityPolicy, value.PolicyDigest)
		if err != nil {
			return ActivityAcceptResponse{}, err
		}
		return ActivityAcceptResponse{
			PolicyJSON: policyJSON, PolicyDigest: policyDigest, PolicyChanged: true,
		}, nil
	default:
		return ActivityAcceptResponse{}, ErrFabricUnavailable
	}
}

func (c *ActivityPublicClient) Pull(ctx context.Context, request ActivityPullRequest) (ActivityPullResponse, error) {
	if request.AfterSequence < 0 || request.AfterSequence > 9_007_199_254_740_991 || request.Limit < 1 || request.Limit > 500 {
		return ActivityPullResponse{}, localstore.ErrActivityCursorConflict
	}
	raw, err := c.call(ctx, "wormhole.activity.pull", ActivityPullV1Args{
		Version:       1,
		AttachmentRef: request.AttachmentRef,
		AfterSequence: request.AfterSequence,
		Limit:         request.Limit,
	})
	if err != nil {
		return ActivityPullResponse{}, err
	}
	var value ActivityPullV1Result
	if decodeClosedActivityJSON(raw, &value) != nil || value.Version != 1 || value.HistoricalPolicies == nil || value.Deliveries == nil {
		return ActivityPullResponse{}, ErrFabricUnavailable
	}
	return runtimePullResponse(value, request)
}

func (c *ActivityPublicClient) SendPresence(ctx context.Context, request ActivityPresenceRequest) (ActivityPresenceResponse, error) {
	activity, err := projectstate.DecodeActivity(request.ActivityJSON)
	if err != nil {
		return ActivityPresenceResponse{}, err
	}
	canonical, err := projectstate.CanonicalActivity(activity)
	if err != nil || !bytes.Equal(canonical, request.ActivityJSON) {
		return ActivityPresenceResponse{}, projectstate.ErrInvalidActivity
	}
	digest, err := projectstate.DigestActivity(activity)
	if err != nil || digest != request.ActivityDigest || activity.Class != projectstate.ActivityPresenceV1 {
		return ActivityPresenceResponse{}, projectstate.ErrInvalidActivity
	}
	raw, err := c.call(ctx, "wormhole.activity.presence", ActivityPresenceV1Args{
		Version:        1,
		AttachmentRef:  request.AttachmentRef,
		PolicyVersion:  request.PolicyVersion,
		PolicyDigest:   request.PolicyDigest,
		Activity:       activity,
		ActivityDigest: digest,
	})
	if err != nil {
		return ActivityPresenceResponse{}, err
	}
	fields, err := activityResultFields(raw)
	if err != nil {
		return ActivityPresenceResponse{}, ErrFabricUnavailable
	}
	if string(fields["status"]) == `"accepted"` {
		var value ActivityPresenceAcceptedV1Result
		if decodeClosedActivityJSON(raw, &value) != nil || value.Version != 1 || value.Status != "accepted" {
			return ActivityPresenceResponse{}, ErrFabricUnavailable
		}
		return ActivityPresenceResponse{}, nil
	}
	var changed ActivityPolicyChangedV1Result
	if string(fields["status"]) != `"policy_changed"` || decodeClosedActivityJSON(raw, &changed) != nil || changed.Version != 1 {
		return ActivityPresenceResponse{}, ErrFabricUnavailable
	}
	policyJSON, policyDigest, err := canonicalRuntimePolicy(changed.EffectiveActivityPolicy, changed.PolicyDigest)
	return ActivityPresenceResponse{
		PolicyJSON: policyJSON, PolicyDigest: policyDigest, PolicyChanged: true,
	}, err
}

func (c *ActivityPublicClient) Lifecycle(ctx context.Context, request ActivityLifecycleRequest) (ActivityLifecycleResponse, error) {
	if !types.CanonicalUUID(request.AttachmentRef) || !types.CanonicalUUID(request.ActivityID) || !types.CanonicalUUID(request.Change.ReferenceID) {
		return ActivityLifecycleResponse{}, localstore.ErrActivityLifecycleConflict
	}
	raw, err := c.call(ctx, "wormhole.activity.lifecycle", ActivityLifecycleV1Args{
		Version:       1,
		AttachmentRef: request.AttachmentRef,
		ActivityID:    request.ActivityID,
		Kind:          request.Change.Kind,
		ReferenceID:   request.Change.ReferenceID,
		ExpectedState: request.Change.ExpectedState,
		NextState:     request.Change.NextState,
	})
	if err != nil {
		return ActivityLifecycleResponse{}, err
	}
	var value ActivityLifecycleV1Result
	if decodeClosedActivityJSON(raw, &value) != nil || value.Version != 1 || value.State != request.Change.NextState {
		return ActivityLifecycleResponse{}, localstore.ErrActivityLifecycleConflict
	}
	return ActivityLifecycleResponse{State: value.State}, nil
}

func rejectActivityDuplicateMembers(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var walk func(json.Token) error
	walk = func(token json.Token) error {
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errInvalidActivityMCPResult
				}
				if _, exists := seen[key]; exists {
					return errInvalidActivityMCPResult
				}
				seen[key] = struct{}{}
				value, err := decoder.Token()
				if err != nil {
					return err
				}
				if err := walk(value); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return errInvalidActivityMCPResult
			}
		case '[':
			for decoder.More() {
				value, err := decoder.Token()
				if err != nil {
					return err
				}
				if err := walk(value); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return errInvalidActivityMCPResult
			}
		}
		return nil
	}
	first, err := decoder.Token()
	if err != nil {
		return err
	}
	if err := walk(first); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errInvalidActivityMCPResult
	}
	return nil
}

func decodeClosedActivityJSON(raw []byte, destination any) error {
	if err := rejectActivityDuplicateMembers(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errInvalidActivityMCPResult
	}
	return nil
}

func activityClientFailure(code string) error {
	switch code {
	case "invalid_activity":
		return projectstate.ErrInvalidActivity
	case "unknown_activity_version":
		return projectstate.ErrUnknownActivityVersion
	case "activity_policy_required":
		return localstore.ErrActivityPolicyUnavailable
	case "activity_policy_changed":
		return localstore.ErrActivityPolicyChanged
	case "activity_not_found":
		return localstore.ErrActivityNotFound
	case "activity_replay_conflict":
		return localstore.ErrActivityReplayConflict
	case "activity_cursor_invalid":
		return localstore.ErrActivityCursorConflict
	case "activity_lifecycle_conflict":
		return localstore.ErrActivityLifecycleConflict
	case "authentication_failed", "attachment_not_found":
		return ErrAttentionRequired
	default:
		return ErrFabricUnavailable
	}
}

func unwrapActivityToolResult(raw json.RawMessage, operation string) (json.RawMessage, error) {
	var wrapper activityToolResult
	if err := decodeClosedActivityJSON(raw, &wrapper); err != nil || len(wrapper.Content) != 1 || wrapper.Content[0].Type != "text" || wrapper.Content[0].Text == "" {
		return nil, ErrFabricUnavailable
	}
	var wrapperFields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wrapperFields); err != nil {
		return nil, ErrFabricUnavailable
	}
	if flag, present := wrapperFields["isError"]; present {
		flag = bytes.TrimSpace(flag)
		if !bytes.Equal(flag, []byte("true")) && !bytes.Equal(flag, []byte("false")) {
			return nil, ErrFabricUnavailable
		}
	}
	inner := json.RawMessage(wrapper.Content[0].Text)
	if err := rejectActivityDuplicateMembers(inner); err != nil {
		return nil, ErrFabricUnavailable
	}
	var failure activityPublicFailure
	failureErr := decodeClosedActivityJSON(inner, &failure)
	if wrapper.IsError {
		if failureErr != nil || failure.Operation != operation || failure.Code == "" {
			return nil, ErrFabricUnavailable
		}
		return nil, activityClientFailure(failure.Code)
	}
	if failureErr == nil && failure.Operation != "" && failure.Code != "" {
		return nil, ErrFabricUnavailable
	}
	return append(json.RawMessage(nil), inner...), nil
}

func (c *ActivityPublicClient) call(ctx context.Context, operation string, arguments any) (json.RawMessage, error) {
	if c == nil || activityNilDependency(c.caller) || c.profile.Mode != types.FabricModePublic || c.credentialRef == "" {
		return nil, ErrFabricUnavailable
	}
	canonical, err := projectstate.CanonicalJSONObject(arguments)
	if err != nil {
		return nil, ErrFabricUnavailable
	}
	raw, err := c.caller.CallActivity(ctx, c.profile, c.credentialRef, operation, canonical)
	if err != nil {
		return nil, ErrFabricUnavailable
	}
	return unwrapActivityToolResult(raw, operation)
}

func activityResultFields(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if err := rejectActivityDuplicateMembers(raw); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

func canonicalRuntimePolicy(policy projectstate.EffectiveActivityPolicyV1, want projectstate.Digest) ([]byte, projectstate.Digest, error) {
	raw, err := projectstate.CanonicalActivityPolicy(policy)
	if err != nil {
		return nil, "", err
	}
	digest, err := projectstate.DigestActivityPolicy(policy)
	if err != nil || digest != want {
		return nil, "", localstore.ErrActivityPolicyUnavailable
	}
	return raw, digest, nil
}

func runtimePullResponse(value ActivityPullV1Result, request ActivityPullRequest) (ActivityPullResponse, error) {
	policyJSON, policyDigest, err := canonicalRuntimePolicy(value.EffectivePolicy, value.PolicyDigest)
	if err != nil {
		return ActivityPullResponse{}, err
	}
	policies := make([]ActivityPullPolicyEvidence, 0, len(value.HistoricalPolicies))
	policyDigests := map[int64]projectstate.Digest{}
	var priorPolicy int64
	for _, item := range value.HistoricalPolicies {
		raw, digest, err := canonicalRuntimePolicy(item.Policy, item.PolicyDigest)
		if err != nil || item.Policy.PolicyVersion <= priorPolicy || item.Policy.PolicyVersion > value.EffectivePolicy.PolicyVersion {
			return ActivityPullResponse{}, localstore.ErrActivityReplayConflict
		}
		if _, duplicate := policyDigests[item.Policy.PolicyVersion]; duplicate {
			return ActivityPullResponse{}, localstore.ErrActivityReplayConflict
		}
		policyDigests[item.Policy.PolicyVersion] = digest
		priorPolicy = item.Policy.PolicyVersion
		policies = append(policies, ActivityPullPolicyEvidence{PolicyJSON: raw, PolicyDigest: digest})
	}
	if len(value.Deliveries) > request.Limit {
		return ActivityPullResponse{}, localstore.ErrActivityCursorConflict
	}
	deliveries := make([]localstore.ActivityPullDelivery, 0, len(value.Deliveries))
	required := map[int64]projectstate.Digest{}
	last := request.AfterSequence
	for _, item := range value.Deliveries {
		if !types.CanonicalUUID(item.SourceRef) {
			return ActivityPullResponse{}, localstore.ErrActivityReplayConflict
		}
		activityJSON, err := projectstate.CanonicalActivity(item.Activity)
		if err != nil {
			return ActivityPullResponse{}, err
		}
		digest, err := projectstate.DigestActivity(item.Activity)
		if err != nil || digest != item.ActivityDigest || item.Receipt.ActivityID != item.Activity.ID ||
			item.Receipt.ActivityDigest != digest || item.Receipt.Sequence <= last ||
			item.Receipt.PolicyVersion > value.EffectivePolicy.PolicyVersion {
			return ActivityPullResponse{}, localstore.ErrActivityReplayConflict
		}
		receiptJSON, err := projectstate.CanonicalActivityReceipt(item.Receipt)
		if err != nil {
			return ActivityPullResponse{}, localstore.ErrActivityReplayConflict
		}
		if prior, ok := required[item.Receipt.PolicyVersion]; ok && prior != item.Receipt.PolicyDigest {
			return ActivityPullResponse{}, localstore.ErrActivityReplayConflict
		}
		required[item.Receipt.PolicyVersion] = item.Receipt.PolicyDigest
		last = item.Receipt.Sequence
		deliveries = append(deliveries, localstore.ActivityPullDelivery{
			SourceWorkspaceID: types.WorkspaceID(item.SourceRef),
			ActivityJSON:      activityJSON,
			ActivityDigest:    digest,
			ReceiptJSON:       receiptJSON,
		})
	}
	if len(required) != len(policyDigests) {
		return ActivityPullResponse{}, localstore.ErrActivityPolicyUnavailable
	}
	for version, digest := range required {
		if policyDigests[version] != digest {
			return ActivityPullResponse{}, localstore.ErrActivityPolicyUnavailable
		}
	}
	if value.NextSequence < last || value.NextSequence > 9_007_199_254_740_991 ||
		(value.HasMore && (len(deliveries) == 0 || value.NextSequence != last)) {
		return ActivityPullResponse{}, localstore.ErrActivityCursorConflict
	}
	return ActivityPullResponse{
		PolicyJSON:         policyJSON,
		PolicyDigest:       policyDigest,
		HistoricalPolicies: policies,
		Deliveries:         deliveries,
		NextSequence:       value.NextSequence,
		HasMore:            value.HasMore,
	}, nil
}
