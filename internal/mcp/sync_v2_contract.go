package mcp

import (
	"bytes"
	"errors"
	"reflect"
	"sort"

	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

type SyncV2Scope = projectstate.SyncV2Scope
type SyncStateV2 = projectstate.SyncStateV2
type SyncAttachV2Args = projectstate.SyncAttachV2Args
type SyncAttachV2Result = projectstate.SyncAttachV2Result
type PublicAgentSessionIssueV2Args = projectstate.PublicAgentSessionIssueV2Args
type PublicAgentSessionIssueV2Result = projectstate.PublicAgentSessionIssueV2Result
type SyncBootstrapV2Args = projectstate.SyncBootstrapV2Args
type SyncBootstrapV2Result = projectstate.SyncBootstrapV2Result
type SyncPullV2Args = projectstate.SyncPullV2Args
type SyncPullV2Result = projectstate.SyncPullV2Result
type SyncPushV2Args = projectstate.SyncPushV2Args
type SyncPushAppliedV2Result = projectstate.SyncPushAppliedV2Result
type SyncPushConflictV2Result = projectstate.SyncPushConflictV2Result
type SyncConflictV2Args = projectstate.SyncConflictV2Args
type SyncConflictResolvedV2Result = projectstate.SyncConflictResolvedV2Result
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

type ToolAuthFamily string

const PublicProofAuth ToolAuthFamily = "public_proof"

type ToolDescriptor struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	AuthFamily   ToolAuthFamily `json:"auth_family"`
	InputSchema  map[string]any `json:"input_schema"`
	OutputSchema map[string]any `json:"output_schema"`
}

func PublicFabricToolDescriptors() []ToolDescriptor {
	return []ToolDescriptor{
		publicDescriptor("wormhole.activity.accept", "Accept durable Activity v1 or return current policy evidence.", ActivityAcceptV1Args{}, ActivityAcceptedV1Result{}, ActivityPolicyChangedV1Result{}),
		publicDescriptor("wormhole.activity.lifecycle", "Apply a source-owned Activity v1 lifecycle transition.", ActivityLifecycleV1Args{}, ActivityLifecycleV1Result{}),
		publicDescriptor("wormhole.activity.presence", "Accept ephemeral Activity v1 presence without durable Activity state.", ActivityPresenceV1Args{}, ActivityPresenceAcceptedV1Result{}, ActivityPolicyChangedV1Result{}),
		publicDescriptor("wormhole.activity.pull", "Pull ordered Activity v1 deliveries and policy evidence.", ActivityPullV1Args{}, ActivityPullV1Result{}),
		publicDescriptor("wormhole.sync.attach", "Attach an observed canonical repository ref to public sync v2.", SyncAttachV2Args{}, SyncAttachV2Result{}),
		publicDescriptor("wormhole.sync.bootstrap", "Read one complete validated sync v2 stream state and finite Activity policy.", SyncBootstrapV2Args{}, SyncBootstrapV2Result{}),
		publicDescriptor("wormhole.sync.conflict", "Resolve one durable sync v2 conflict with a canonical operation.", SyncConflictV2Args{}, SyncConflictResolvedV2Result{}),
		publicDescriptor("wormhole.sync.issue_agent_session", "Issue an accountable public agent session from a tracked human key.", PublicAgentSessionIssueV2Args{}, PublicAgentSessionIssueV2Result{}),
		publicDescriptor("wormhole.sync.pull", "Read one complete validated sync v2 stream state.", SyncPullV2Args{}, SyncPullV2Result{}),
		publicDescriptor("wormhole.sync.push", "Apply one canonical sync v2 operation or return its durable conflict.", SyncPushV2Args{}, SyncPushAppliedV2Result{}, SyncPushConflictV2Result{}),
	}
}

func publicDescriptor(name, description string, input any, outputs ...any) ToolDescriptor {
	return ToolDescriptor{
		Name: name, Description: description, AuthFamily: PublicProofAuth,
		InputSchema: closedJSONSchemaForType(reflect.TypeOf(input)), OutputSchema: schemaOneOf(outputs...),
	}
}

type ToolFailureV1 struct {
	Code      string `json:"code"`
	Operation string `json:"operation"`
}

var errInvalidPublicToolFailure = errors.New("mcp: invalid public tool failure")

var publicFailureCodeSet = map[string]struct{}{
	"invalid_request": {}, "unknown_version": {}, "authentication_failed": {}, "permission_denied": {},
	"attachment_not_found": {}, "sync_precondition_failed": {}, "sync_conflict": {}, "sync_replay_conflict": {},
	"sync_observer_unavailable": {}, "internal_error": {}, "invalid_activity": {}, "unknown_activity_version": {},
	"activity_policy_required": {}, "activity_policy_changed": {}, "activity_not_found": {}, "activity_replay_conflict": {},
	"activity_cursor_invalid": {}, "activity_lifecycle_conflict": {},
}

func publicToolFailureCodes() []string {
	codes := make([]string, 0, len(publicFailureCodeSet))
	for code := range publicFailureCodeSet {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

func isPublicFabricTool(operation string) bool {
	for _, descriptor := range PublicFabricToolDescriptors() {
		if descriptor.Name == operation {
			return true
		}
	}
	return false
}

func toolFailureResult(operation, code string) (toolCallResult, error) {
	if !isPublicFabricTool(operation) {
		return toolCallResult{}, errInvalidPublicToolFailure
	}
	if _, ok := publicFailureCodeSet[code]; !ok {
		return toolCallResult{}, errInvalidPublicToolFailure
	}
	canonical, err := projectstate.CanonicalJSON(ToolFailureV1{Code: code, Operation: operation})
	if err != nil {
		return toolCallResult{}, errInvalidPublicToolFailure
	}
	canonical = bytes.TrimSuffix(canonical, []byte{'\n'})
	return toolCallResult{
		Content: []toolCallResultContent{{Type: "text", Text: string(canonical)}}, IsError: true,
	}, nil
}
