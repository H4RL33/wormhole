package sync

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

const maximumActivityTransportBatch = 500

type ActivityAcceptRequest struct {
	AttachmentRef  string
	PolicyVersion  int64
	PolicyDigest   projectstate.Digest
	ActivityJSON   []byte
	ActivityDigest projectstate.Digest
}

type ActivityAcceptResponse struct {
	Receipt       projectstate.ActivityReceiptV1
	PolicyJSON    []byte
	PolicyDigest  projectstate.Digest
	PolicyChanged bool
}

type ActivityPullRequest struct {
	AttachmentRef string
	AfterSequence int64
	Limit         int
}

type ActivityPullPolicyStreamKey struct {
	ProjectID, FabricInstanceID, StreamID, CanonicalRef string
}

type ActivityPullPolicyEvidence struct {
	Stream       ActivityPullPolicyStreamKey
	PolicyJSON   []byte
	PolicyDigest projectstate.Digest
}

type ActivityPullResponse struct {
	PolicyJSON         []byte
	PolicyDigest       projectstate.Digest
	HistoricalPolicies []ActivityPullPolicyEvidence
	Deliveries         []localstore.ActivityPullDelivery
	NextSequence       int64
	HasMore            bool
}

type ActivityPresenceRequest struct {
	AttachmentRef  string
	PolicyVersion  int64
	PolicyDigest   projectstate.Digest
	ActivityJSON   []byte
	ActivityDigest projectstate.Digest
}

type ActivityPresenceResponse struct {
	PolicyJSON    []byte
	PolicyDigest  projectstate.Digest
	PolicyChanged bool
}

type ActivityFabricClient interface {
	Accept(context.Context, ActivityAcceptRequest) (ActivityAcceptResponse, error)
	Pull(context.Context, ActivityPullRequest) (ActivityPullResponse, error)
	SendPresence(context.Context, ActivityPresenceRequest) (ActivityPresenceResponse, error)
}

type ActivityClientFactory interface {
	Client(context.Context, types.FabricProfile, string) (ActivityFabricClient, error)
}

type ActivityTransport struct {
	routes      FabricRouteSource
	credentials CredentialSource
	conflicts   localstore.WorkspaceConflictGate
	activities  *localstore.ActivityRepo
	clients     ActivityClientFactory
}

func NewActivityTransport(routes FabricRouteSource, credentials CredentialSource,
	conflicts localstore.WorkspaceConflictGate, activities *localstore.ActivityRepo,
	clients ActivityClientFactory) (*ActivityTransport, error) {
	if activityNilDependency(routes) || activityNilDependency(credentials) ||
		activityNilDependency(conflicts) || activities == nil || activityNilDependency(clients) {
		return nil, errors.New("sync: Activity transport requires routes, credentials, conflict gate, activity repository, and client factory")
	}
	return &ActivityTransport{
		routes: routes, credentials: credentials, conflicts: conflicts,
		activities: activities, clients: clients,
	}, nil
}

func (s *ActivityTransport) Queue(ctx context.Context, scope types.WorkspaceScope, activity projectstate.ActivityV1) error {
	if s == nil {
		return errors.New("sync: Activity transport unavailable")
	}
	if err := validateRemoteActivity(activity, false); err != nil {
		return err
	}
	resolved, err := s.resolveRoutePolicy(ctx, scope)
	if err != nil {
		return err
	}
	if err := validateRemoteActivityForProfile(activity, false, resolved.profile.Mode); err != nil {
		return err
	}
	if _, err := s.activities.QueueOutbound(ctx, resolved.route, activity); err != nil {
		return classifyActivityError("queue", err, ErrAttentionRequired)
	}
	return nil
}

func (s *ActivityTransport) DeliverPending(ctx context.Context, scope types.WorkspaceScope, limit int) error {
	if s == nil {
		return errors.New("sync: Activity transport unavailable")
	}
	if limit < 1 || limit > maximumActivityTransportBatch {
		return fmt.Errorf("sync: Activity delivery limit: %w", localstore.ErrActivityNotFound)
	}
	resolved, err := s.resolveRoutePolicy(ctx, scope)
	if err != nil {
		return err
	}
	records, err := s.activities.PendingOutbound(ctx, resolved.route, limit)
	if err != nil {
		return classifyActivityError("pending queue", err, ErrAttentionRequired)
	}
	for _, record := range records {
		if err := s.deliverRecord(ctx, scope, record); err != nil {
			return err
		}
	}
	return nil
}

func (s *ActivityTransport) Pull(ctx context.Context, scope types.WorkspaceScope, limit int) error {
	if s == nil {
		return errors.New("sync: Activity transport unavailable")
	}
	if limit < 1 || limit > maximumActivityTransportBatch {
		return fmt.Errorf("sync: Activity pull limit: %w", localstore.ErrActivityCursorConflict)
	}
	cycle, err := s.resolveNetworkCycle(ctx, scope)
	if err != nil {
		return err
	}
	after, err := s.activities.Cursor(ctx, cycle.route)
	if err != nil {
		return classifyActivityError("pull cursor", err, ErrAttentionRequired)
	}
	response, err := cycle.client.Pull(ctx, ActivityPullRequest{
		AttachmentRef: cycle.binding.AttachmentRef, AfterSequence: after, Limit: limit,
	})
	if err != nil {
		return classifyActivityError("pull", err, ErrFabricUnavailable)
	}
	_, policyJSON, _, err := validateActivityPolicyEvidence(response.PolicyJSON, response.PolicyDigest)
	if err != nil {
		return err
	}
	if len(response.Deliveries) > limit {
		return fmt.Errorf("sync: Activity pull response: %w", localstore.ErrActivityCursorConflict)
	}
	deliveries, historicalPolicies, err := validatePulledBatchForProfile(
		response.Deliveries, response.HistoricalPolicies, cycle.route, cycle.profile.Mode)
	if err != nil {
		return err
	}
	if err := s.requireUnconflicted(ctx, scope); err != nil {
		return err
	}
	err = s.activities.AcceptPullBatch(ctx, cycle.route, localstore.ActivityPullBatch{
		PolicyJSON: append([]byte(nil), policyJSON...), HistoricalPolicies: historicalPolicies,
		ExpectedPolicyVersion: cycle.policy.Policy.PolicyVersion, ExpectedPolicyDigest: cycle.policy.PolicyDigest,
		ExpectedAfter: after, NextSequence: response.NextSequence,
		HasMore: response.HasMore, Deliveries: deliveries,
	})
	return classifyActivityError("accept pull", err, ErrAttentionRequired)
}

func (s *ActivityTransport) SendPresence(ctx context.Context, scope types.WorkspaceScope, activity projectstate.ActivityV1) error {
	if s == nil {
		return errors.New("sync: Activity transport unavailable")
	}
	if err := validateRemoteActivity(activity, true); err != nil {
		return err
	}
	canonical, err := projectstate.CanonicalActivity(activity)
	if err != nil {
		return err
	}
	digest, err := projectstate.DigestActivity(activity)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		resolved, err := s.resolveRoutePolicy(ctx, scope)
		if err != nil {
			return err
		}
		if err := validateRemoteActivityForProfile(activity, true, resolved.profile.Mode); err != nil {
			return err
		}
		cycle, err := s.completeNetworkCycle(ctx, scope, resolved)
		if err != nil {
			return err
		}
		response, err := cycle.client.SendPresence(ctx, ActivityPresenceRequest{
			AttachmentRef: cycle.binding.AttachmentRef,
			PolicyVersion: cycle.policy.Policy.PolicyVersion, PolicyDigest: cycle.policy.PolicyDigest,
			ActivityJSON: append([]byte(nil), canonical...), ActivityDigest: digest,
		})
		if err != nil {
			return classifyActivityError("presence", err, ErrFabricUnavailable)
		}
		if !response.PolicyChanged {
			if len(response.PolicyJSON) != 0 || response.PolicyDigest != "" {
				return fmt.Errorf("sync: Activity presence policy response: %w", localstore.ErrActivityPolicyChanged)
			}
			return nil
		}
		policy, policyJSON, policyDigest, err := validateActivityPolicyEvidence(response.PolicyJSON, response.PolicyDigest)
		if err != nil {
			return err
		}
		replaced, err := s.activities.ReplacePolicy(ctx, cycle.route, cycle.policy.Policy.PolicyVersion,
			cycle.policy.PolicyDigest, policy)
		if err != nil {
			return classifyActivityError("presence policy", err, ErrAttentionRequired)
		}
		if !sameActivityPolicy(replaced, policy, policyJSON, policyDigest) {
			return fmt.Errorf("sync: Activity presence policy response: %w", localstore.ErrActivityPolicyChanged)
		}
		if attempt != 0 {
			return fmt.Errorf("sync: Activity presence policy changed twice: %w", localstore.ErrActivityPolicyChanged)
		}
	}
	return fmt.Errorf("sync: Activity presence policy retry exhausted: %w", localstore.ErrActivityPolicyChanged)
}

func (s *ActivityTransport) Retained(ctx context.Context, scope types.WorkspaceScope, limit int) ([]localstore.ActivityRecord, error) {
	if s == nil {
		return nil, errors.New("sync: Activity transport unavailable")
	}
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("sync: retained Activity limit: %w", localstore.ErrActivityNotFound)
	}
	resolved, err := s.resolveRoutePolicy(ctx, scope)
	if err != nil {
		return nil, err
	}
	records, err := s.activities.Retained(ctx, resolved.route, limit)
	if err != nil {
		return nil, classifyActivityError("retained read", err, ErrAttentionRequired)
	}
	for _, record := range records {
		if record.Key.Route != resolved.route {
			return nil, fmt.Errorf("sync: retained Activity route changed: %w", ErrAttentionRequired)
		}
		if err := validateRemoteActivityForProfile(record.Activity, false, resolved.profile.Mode); err != nil {
			return nil, err
		}
	}
	return records, nil
}

type resolvedActivityRoute struct {
	binding types.FabricBinding
	profile types.FabricProfile
	route   types.ActivityRouteKey
	policy  localstore.ActivityPolicyRecord
}

type activityNetworkCycle struct {
	resolvedActivityRoute
	client ActivityFabricClient
}

func (s *ActivityTransport) deliverRecord(ctx context.Context, scope types.WorkspaceScope, record localstore.ActivityRecord) error {
	immutableJSON := append([]byte(nil), record.ActivityJSON...)
	immutableDigest := record.ActivityDigest
	for attempt := 0; attempt < 2; attempt++ {
		resolved, err := s.resolveRoutePolicy(ctx, scope)
		if err != nil {
			return err
		}
		if resolved.route != record.Key.Route {
			return fmt.Errorf("sync: Activity delivery route changed: %w", ErrAttentionRequired)
		}
		if err := validateRemoteActivityForProfile(record.Activity, false, resolved.profile.Mode); err != nil {
			return err
		}
		cycle, err := s.completeNetworkCycle(ctx, scope, resolved)
		if err != nil {
			return err
		}
		request := ActivityAcceptRequest{
			AttachmentRef: cycle.binding.AttachmentRef,
			PolicyVersion: cycle.policy.Policy.PolicyVersion, PolicyDigest: cycle.policy.PolicyDigest,
			ActivityJSON: append([]byte(nil), immutableJSON...), ActivityDigest: immutableDigest,
		}
		response, err := cycle.client.Accept(ctx, request)
		if err != nil {
			return classifyActivityError("accept", err, ErrFabricUnavailable)
		}
		policy, policyJSON, policyDigest, err := validateActivityPolicyEvidence(response.PolicyJSON, response.PolicyDigest)
		if err != nil {
			return err
		}
		if response.PolicyChanged {
			if response.Receipt != (projectstate.ActivityReceiptV1{}) {
				return fmt.Errorf("sync: Activity policy response: %w", localstore.ErrActivityPolicyChanged)
			}
			replaced, err := s.activities.ReplacePolicy(ctx, cycle.route, cycle.policy.Policy.PolicyVersion,
				cycle.policy.PolicyDigest, policy)
			if err != nil {
				return classifyActivityError("accept policy", err, ErrAttentionRequired)
			}
			if !sameActivityPolicy(replaced, policy, policyJSON, policyDigest) {
				return fmt.Errorf("sync: Activity policy response: %w", localstore.ErrActivityPolicyChanged)
			}
			if attempt == 0 {
				continue
			}
			return fmt.Errorf("sync: Activity policy changed twice: %w", localstore.ErrActivityPolicyChanged)
		}
		if !sameActivityPolicy(cycle.policy, policy, policyJSON, policyDigest) {
			return fmt.Errorf("sync: Activity accept policy: %w", localstore.ErrActivityPolicyChanged)
		}
		if err := validateActivityReceiptForRecord(response.Receipt, record); err != nil {
			return err
		}
		if err := s.requireUnconflicted(ctx, scope); err != nil {
			return err
		}
		err = s.activities.AcknowledgeOutbound(ctx, record.Key, response.Receipt)
		return classifyActivityError("acknowledge", err, ErrAttentionRequired)
	}
	return fmt.Errorf("sync: Activity policy retry exhausted: %w", localstore.ErrActivityPolicyChanged)
}

func (s *ActivityTransport) resolveNetworkCycle(ctx context.Context, scope types.WorkspaceScope) (activityNetworkCycle, error) {
	resolved, err := s.resolveRoutePolicy(ctx, scope)
	if err != nil {
		return activityNetworkCycle{}, err
	}
	return s.completeNetworkCycle(ctx, scope, resolved)
}

func (s *ActivityTransport) completeNetworkCycle(ctx context.Context, scope types.WorkspaceScope, resolved resolvedActivityRoute) (activityNetworkCycle, error) {
	if err := s.requireUnconflicted(ctx, scope); err != nil {
		return activityNetworkCycle{}, err
	}
	if resolved.profile.CredentialRef == "" {
		return activityNetworkCycle{}, fmt.Errorf("sync: Activity credential unavailable: %w", ErrAttentionRequired)
	}
	token, err := s.credentials.Read(ctx, resolved.profile.CredentialRef)
	if err != nil {
		return activityNetworkCycle{}, classifyActivityError("credential", err, ErrAttentionRequired)
	}
	if token == "" {
		return activityNetworkCycle{}, fmt.Errorf("sync: Activity credential unavailable: %w", ErrAttentionRequired)
	}
	client, err := s.clients.Client(ctx, resolved.profile, token)
	if err != nil {
		return activityNetworkCycle{}, classifyActivityError("client", err, ErrFabricUnavailable)
	}
	if activityNilDependency(client) {
		return activityNetworkCycle{}, fmt.Errorf("sync: Activity client unavailable: %w", ErrFabricUnavailable)
	}
	return activityNetworkCycle{resolvedActivityRoute: resolved, client: client}, nil
}

func (s *ActivityTransport) resolveRoutePolicy(ctx context.Context, scope types.WorkspaceScope) (resolvedActivityRoute, error) {
	if s == nil || activityNilDependency(s.routes) || s.activities == nil {
		return resolvedActivityRoute{}, errors.New("sync: Activity transport unavailable")
	}
	binding, profile, err := s.routes.GetRoute(ctx, scope)
	if err != nil {
		return resolvedActivityRoute{}, classifyActivityError("route", err, ErrAttentionRequired)
	}
	if binding.Workspace.Scope != scope || binding.AttachmentRef == "" || binding.CanonicalRef == "" ||
		binding.ValidateWithProfile(profile) != nil {
		return resolvedActivityRoute{}, fmt.Errorf("sync: validate Activity route: %w", ErrAttentionRequired)
	}
	route := activityRouteForTransport(binding)
	if err := route.Validate(); err != nil {
		return resolvedActivityRoute{}, fmt.Errorf("sync: validate Activity route: %w", ErrAttentionRequired)
	}
	policy, err := s.activities.CurrentPolicy(ctx, route)
	if err != nil {
		return resolvedActivityRoute{}, classifyActivityError("policy read", err, ErrAttentionRequired)
	}
	decoded, canonical, digest, err := validateActivityPolicyEvidence(policy.PolicyJSON, policy.PolicyDigest)
	if err != nil || policy.Route != route || policy.Policy != decoded || !sameActivityPolicy(policy, decoded, canonical, digest) {
		return resolvedActivityRoute{}, fmt.Errorf("sync: validate Activity policy: %w", localstore.ErrActivityPolicyUnavailable)
	}
	return resolvedActivityRoute{binding: binding, profile: profile, route: route, policy: policy}, nil
}

func (s *ActivityTransport) requireUnconflicted(ctx context.Context, scope types.WorkspaceScope) error {
	conflicted, err := s.conflicts.HasOpenConflicts(ctx, scope)
	if err != nil {
		return classifyActivityError("conflict check", err, ErrAttentionRequired)
	}
	if conflicted {
		return localstore.ErrWorkspaceConflicted
	}
	return nil
}

func validateRemoteActivity(activity projectstate.ActivityV1, presence bool) error {
	if _, err := projectstate.CanonicalActivity(activity); err != nil {
		return err
	}
	if presence != (activity.Class == projectstate.ActivityPresenceV1) {
		return projectstate.ErrInvalidActivity
	}
	if activity.Actor.Assurance != types.AssurancePublicKeyContinuity &&
		activity.Actor.Assurance != types.AssurancePrivateAuthenticated {
		return projectstate.ErrInvalidActivity
	}
	return nil
}

func validateRemoteActivityForProfile(activity projectstate.ActivityV1, presence bool, mode types.FabricMode) error {
	if err := validateRemoteActivity(activity, presence); err != nil {
		return err
	}
	want := types.AssurancePrivateAuthenticated
	if mode == types.FabricModePublic {
		want = types.AssurancePublicKeyContinuity
	} else if mode != types.FabricModePrivate {
		return projectstate.ErrInvalidActivity
	}
	if activity.Actor.Assurance != want {
		return projectstate.ErrInvalidActivity
	}
	return nil
}

func validatePulledBatchForProfile(deliveries []localstore.ActivityPullDelivery, policies []ActivityPullPolicyEvidence,
	route types.ActivityRouteKey, mode types.FabricMode) ([]localstore.ActivityPullDelivery, []localstore.ActivityPolicyEvidence, error) {
	validatedDeliveries := make([]localstore.ActivityPullDelivery, 0, len(deliveries))
	receiptPolicies := make(map[int64]projectstate.Digest, len(deliveries))
	for _, delivery := range deliveries {
		activity, err := validatePulledActivityForProfile(delivery, mode)
		if err != nil {
			return nil, nil, err
		}
		receipt, err := projectstate.DecodeActivityReceipt(delivery.ReceiptJSON)
		if err != nil || receipt.ActivityID != activity.ID || receipt.ActivityDigest != delivery.ActivityDigest {
			return nil, nil, fmt.Errorf("sync: pulled Activity receipt: %w", localstore.ErrActivityReplayConflict)
		}
		if prior, found := receiptPolicies[receipt.PolicyVersion]; found && prior != receipt.PolicyDigest {
			return nil, nil, fmt.Errorf("sync: pulled Activity receipt policy: %w", localstore.ErrActivityReplayConflict)
		}
		receiptPolicies[receipt.PolicyVersion] = receipt.PolicyDigest
		validatedDeliveries = append(validatedDeliveries, localstore.ActivityPullDelivery{
			SourceWorkspaceID: delivery.SourceWorkspaceID,
			ActivityJSON:      append([]byte(nil), delivery.ActivityJSON...), ActivityDigest: delivery.ActivityDigest,
			ReceiptJSON: append([]byte(nil), delivery.ReceiptJSON...),
		})
	}
	if len(policies) != len(receiptPolicies) {
		return nil, nil, fmt.Errorf("sync: pulled Activity policy evidence: %w", localstore.ErrActivityPolicyUnavailable)
	}
	validatedPolicies := make([]localstore.ActivityPolicyEvidence, 0, len(policies))
	seen := make(map[int64]struct{}, len(policies))
	var priorVersion int64
	for _, evidence := range policies {
		if evidence.Stream.ProjectID != route.RemoteProjectID || evidence.Stream.FabricInstanceID != route.FabricInstanceID ||
			evidence.Stream.StreamID != route.StreamID || evidence.Stream.CanonicalRef != route.CanonicalRef {
			return nil, nil, fmt.Errorf("sync: pulled Activity policy route: %w", localstore.ErrActivityPolicyUnavailable)
		}
		policy, canonical, digest, err := validateActivityPolicyEvidence(evidence.PolicyJSON, evidence.PolicyDigest)
		if err != nil {
			return nil, nil, fmt.Errorf("sync: pulled Activity policy evidence: %w", localstore.ErrActivityPolicyUnavailable)
		}
		wantedDigest, required := receiptPolicies[policy.PolicyVersion]
		if !required || wantedDigest != digest {
			return nil, nil, fmt.Errorf("sync: pulled Activity policy evidence: %w", localstore.ErrActivityReplayConflict)
		}
		if _, duplicate := seen[policy.PolicyVersion]; duplicate || (priorVersion != 0 && policy.PolicyVersion <= priorVersion) {
			return nil, nil, fmt.Errorf("sync: pulled Activity policy evidence: %w", localstore.ErrActivityReplayConflict)
		}
		seen[policy.PolicyVersion] = struct{}{}
		priorVersion = policy.PolicyVersion
		validatedPolicies = append(validatedPolicies, localstore.ActivityPolicyEvidence{
			Route: route, PolicyJSON: append([]byte(nil), canonical...), PolicyDigest: digest,
		})
	}
	return validatedDeliveries, validatedPolicies, nil
}

func validatePulledActivityForProfile(delivery localstore.ActivityPullDelivery, mode types.FabricMode) (projectstate.ActivityV1, error) {
	activity, err := projectstate.DecodeActivity(delivery.ActivityJSON)
	if err != nil {
		return projectstate.ActivityV1{}, err
	}
	digest, err := projectstate.DigestActivity(activity)
	if err != nil {
		return projectstate.ActivityV1{}, err
	}
	if digest != delivery.ActivityDigest {
		return projectstate.ActivityV1{}, fmt.Errorf("sync: pulled Activity digest: %w", localstore.ErrActivityReplayConflict)
	}
	if err := validateRemoteActivityForProfile(activity, false, mode); err != nil {
		return projectstate.ActivityV1{}, err
	}
	return activity, nil
}

func classifyActivityError(operation string, err, fallback error) error {
	if err == nil {
		return nil
	}
	safeCandidates := []error{
		context.Canceled,
		context.DeadlineExceeded,
		projectstate.ErrInvalidActivity,
		projectstate.ErrUnknownActivityVersion,
		projectstate.ErrInvalidActivityPolicy,
		localstore.ErrActivityPolicyUnavailable,
		localstore.ErrActivityPolicyChanged,
		localstore.ErrActivityNotFound,
		localstore.ErrActivityReplayConflict,
		localstore.ErrActivityCursorConflict,
		localstore.ErrActivityLifecycleConflict,
		localstore.ErrWorkspaceConflicted,
		ErrFabricUnavailable,
		ErrAttentionRequired,
	}
	safe := make([]error, 0, 2)
	for _, candidate := range safeCandidates {
		if errors.Is(err, candidate) {
			safe = append(safe, candidate)
		}
	}
	if len(safe) == 0 {
		safe = append(safe, fallback)
	}
	return fmt.Errorf("sync: Activity %s: %w", operation, errors.Join(safe...))
}

func validateActivityPolicyEvidence(raw []byte, claimed projectstate.Digest) (projectstate.EffectiveActivityPolicyV1, []byte, projectstate.Digest, error) {
	policy, err := projectstate.DecodeActivityPolicy(raw)
	if err != nil {
		return projectstate.EffectiveActivityPolicyV1{}, nil, "", err
	}
	canonical, err := projectstate.CanonicalActivityPolicy(policy)
	if err != nil {
		return projectstate.EffectiveActivityPolicyV1{}, nil, "", err
	}
	digest, err := projectstate.DigestActivityPolicy(policy)
	if err != nil {
		return projectstate.EffectiveActivityPolicyV1{}, nil, "", err
	}
	if digest != claimed {
		return projectstate.EffectiveActivityPolicyV1{}, nil, "", projectstate.ErrInvalidActivityPolicy
	}
	return policy, canonical, digest, nil
}

func validateActivityReceiptForRecord(receipt projectstate.ActivityReceiptV1, record localstore.ActivityRecord) error {
	if _, err := projectstate.CanonicalActivityReceipt(receipt); err != nil {
		return err
	}
	if receipt.ActivityID != record.Key.ActivityID || receipt.ActivityDigest != record.ActivityDigest {
		return fmt.Errorf("sync: Activity receipt: %w", localstore.ErrActivityReplayConflict)
	}
	return nil
}

func sameActivityPolicy(record localstore.ActivityPolicyRecord, policy projectstate.EffectiveActivityPolicyV1,
	canonical []byte, digest projectstate.Digest) bool {
	return record.Policy == policy && record.PolicyDigest == digest && reflect.DeepEqual(record.PolicyJSON, canonical)
}

func activityRouteForTransport(binding types.FabricBinding) types.ActivityRouteKey {
	return types.ActivityRouteKey{
		ProjectID: binding.Workspace.Scope.ProjectID, WorkspaceID: binding.Workspace.Scope.WorkspaceID,
		FabricInstanceID: binding.FabricInstanceID, RemoteProjectID: binding.RemoteProjectID,
		StreamID: binding.StreamID, CanonicalRef: binding.CanonicalRef,
	}
}

func activityNilDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
