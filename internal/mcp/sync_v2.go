package mcp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	coregit "github.com/H4RL33/wormhole/internal/core/git"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

type syncAttachCoordinator interface {
	ExecuteInitialAttach(context.Context, InitialAttachCommand) (InitialAttachResult, error)
	ReplayInitialAttach(context.Context, InitialAttachReplayCommand) (InitialAttachResult, error)
}

func (h *SyncV2AttachHandler) Handle(ctx context.Context, raw json.RawMessage, proof types.PublicRequestProof) (SyncAttachV2Result, error) {
	if !h.ready() {
		return SyncAttachV2Result{}, syncAttachFailure("internal_error")
	}
	var arguments SyncAttachV2Args
	if decodePublicArguments(raw, &arguments) != nil || !isCanonicalJSONObject(raw) {
		if attachVersion(raw) != 0 && attachVersion(raw) != projectstate.SyncProtocolVersionV2 {
			return SyncAttachV2Result{}, syncAttachFailure("unknown_version")
		}
		return SyncAttachV2Result{}, syncAttachFailure("invalid_request")
	}
	if !validSyncAttachArguments(arguments) {
		return SyncAttachV2Result{}, syncAttachFailure("invalid_request")
	}
	verified, err := h.verifier.VerifyInitialAttach("wormhole.sync.attach", arguments.Repository, arguments.CanonicalRef, raw, proof)
	if err != nil {
		return SyncAttachV2Result{}, syncAttachFailure("authentication_failed")
	}
	observation, tree, err := h.observer.ObserveRef(ctx, arguments.Repository, arguments.CanonicalRef, h.credentialRef)
	if err != nil {
		return SyncAttachV2Result{}, syncAttachFailure("sync_observer_unavailable")
	}
	digest, err := projectstate.DigestTree(tree)
	if err != nil || observation.Repository != arguments.Repository || observation.RefName != arguments.CanonicalRef ||
		observation.CommitSHA != arguments.BaseCommitSHA || digest != arguments.BaseTreeDigest ||
		observation.ObservedAt.IsZero() || observation.ObservedAt.Location() != time.UTC {
		return SyncAttachV2Result{}, syncAttachFailure("authentication_failed")
	}
	snapshot, err := projectstate.DecodeTree(tree)
	if err != nil || projectstate.Validate(snapshot) != nil || snapshot.Config.ProjectID == "" || snapshot.Config.Repository != arguments.Repository {
		return SyncAttachV2Result{}, syncAttachFailure("authentication_failed")
	}
	human, err := resolveVerifiedTrackedHuman(snapshot, verified)
	if err != nil {
		return SyncAttachV2Result{}, syncAttachFailure("authentication_failed")
	}
	policy, err := h.policySource.InitialActivityPolicy(ctx, snapshot.Config.ProjectID, arguments.Repository, arguments.CanonicalRef)
	if err != nil {
		return SyncAttachV2Result{}, syncAttachFailure("activity_policy_required")
	}
	if _, err := projectstate.CanonicalActivityPolicy(policy); err != nil {
		return SyncAttachV2Result{}, syncAttachFailure("activity_policy_required")
	}
	command := InitialAttachCommand{
		ProjectID: snapshot.Config.ProjectID, FabricInstanceID: h.fabricInstanceID,
		Repository: arguments.Repository, CanonicalRef: arguments.CanonicalRef,
		Observation: observation, ObservedTree: tree, ObservedHuman: human,
		TransportActor: types.ActorEnvelope{
			ActorKind: types.ActorHuman, HumanPrincipalID: human.ID,
			Assurance: types.AssurancePublicKeyContinuity, OccurredAt: verified.Timestamp,
		},
		KeyFingerprint: verified.KeyFingerprint, PublicKey: verified.PublicKey,
		Nonce: verified.Claim, Policy: policy, CanonicalRequest: bytes.Clone(raw),
	}
	result, err := h.coordinator.ExecuteInitialAttach(ctx, command)
	if err != nil {
		return SyncAttachV2Result{}, syncAttachFailure(syncAttachErrorCode(err))
	}
	if !validSyncAttachResult(result, command) {
		return SyncAttachV2Result{}, syncAttachFailure("internal_error")
	}
	return SyncAttachV2Result{
		Version: projectstate.SyncProtocolVersionV2, AttachmentRef: result.Attachment.AttachmentRef,
		RemoteProjectID: result.Attachment.Key.ProjectID, StreamID: result.Attachment.Key.StreamID,
		StreamVersion: result.State.Version, EffectiveActivityPolicy: result.Policy,
	}, nil
}

func validSyncAttachArguments(arguments SyncAttachV2Args) bool {
	if arguments.Repository.Provider == "" {
		return false
	}
	probe := types.WorkspaceBinding{
		Scope: types.WorkspaceScope{
			ProjectID:   "00000000-0000-4000-8000-000000000001",
			WorkspaceID: types.WorkspaceID("00000000-0000-4000-8000-000000000002"),
		},
		Checkout:   types.CheckoutIdentity{CanonicalPath: "/wormhole-public-attach-validation", Device: 1, Inode: 1},
		Repository: arguments.Repository, AcceptedRef: arguments.CanonicalRef,
		AcceptedCommitSHA: arguments.BaseCommitSHA, AcceptedTreeDigest: string(arguments.BaseTreeDigest),
	}
	return probe.Validate() == nil
}

func validSyncAttachResult(result InitialAttachResult, command InitialAttachCommand) bool {
	attachment := result.Attachment
	return attachment.Key.ProjectID == command.ProjectID && attachment.Key.FabricInstanceID == command.FabricInstanceID &&
		types.CanonicalUUID(attachment.Key.StreamID) && types.CanonicalUUID(attachment.WorkspaceID) &&
		types.CanonicalUUID(attachment.AttachmentRef) && types.CanonicalUUID(attachment.ActivitySourceRef) &&
		attachment.Repository == command.Repository && attachment.CanonicalRef == command.CanonicalRef &&
		attachment.IssuerKeyFingerprint == command.KeyFingerprint && attachment.SourceVersion >= 0 && attachment.Writable &&
		result.State.Key == attachment.Key && result.State.Version >= attachment.SourceVersion && result.Policy == command.Policy
}

func attachVersion(raw json.RawMessage) int {
	if rejectDuplicateJSONMembers(raw) != nil {
		return 0
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil {
		return 0
	}
	var version int
	if json.Unmarshal(value["version"], &version) != nil {
		return 0
	}
	return version
}

func syncAttachFailure(code string) error {
	canonical, err := projectstate.CanonicalJSON(ToolFailureV1{Code: code, Operation: "wormhole.sync.attach"})
	if err != nil {
		return errors.New(`{"code":"internal_error","operation":"wormhole.sync.attach"}`)
	}
	return errors.New(string(bytes.TrimSuffix(canonical, []byte{'\n'})))
}

func syncAttachErrorCode(err error) string {
	switch {
	case errors.Is(err, identity.ErrPublicAuthentication), errors.Is(err, identity.ErrPublicNonceReplay):
		return "authentication_failed"
	case errors.Is(err, coregit.ErrPublicAttachReplay):
		return "sync_replay_conflict"
	case errors.Is(err, coregit.ErrStreamPrecondition):
		return "sync_precondition_failed"
	case errors.Is(err, coregit.ErrPublicAttachActivationConflict), errors.Is(err, coregit.ErrPublicAttachClaimConflict):
		return "sync_conflict"
	default:
		return "internal_error"
	}
}

// SyncAttachPolicySource supplies Fabric's configured finite policy for a new
// public stream. It deliberately receives only server-derived project scope.
type SyncAttachPolicySource interface {
	InitialActivityPolicy(context.Context, string, types.RepositoryIdentity, string) (projectstate.EffectiveActivityPolicyV1, error)
}

type SyncV2AttachHandler struct {
	fabricInstanceID string
	credentialRef    string
	observer         coregit.CanonicalGitObserver
	coordinator      syncAttachCoordinator
	policySource     SyncAttachPolicySource
	verifier         *PublicProofVerifier
}

func (h *SyncV2AttachHandler) ready() bool {
	return h != nil && types.CanonicalUUID(h.fabricInstanceID) && h.credentialRef != "" &&
		strings.TrimSpace(h.credentialRef) == h.credentialRef && !strings.ContainsAny(h.credentialRef, "\r\n") &&
		h.observer != nil && h.coordinator != nil && h.policySource != nil && h.verifier.readyForFabric(h.fabricInstanceID)
}

func NewSyncV2AttachHandler(fabricInstanceID, credentialRef string, observer coregit.CanonicalGitObserver, coordinator syncAttachCoordinator, policySource SyncAttachPolicySource, verifier *PublicProofVerifier) (*SyncV2AttachHandler, error) {
	handler := &SyncV2AttachHandler{
		fabricInstanceID: fabricInstanceID,
		credentialRef:    credentialRef,
		observer:         observer,
		coordinator:      coordinator,
		policySource:     policySource,
		verifier:         verifier,
	}
	if !handler.ready() {
		return nil, identity.ErrInvalidPublicIdentity
	}
	return handler, nil
}

type SyncV2BootstrapHandler struct {
	resolver *PublicBoundProofResolver
	activity *coregit.ActivityStore
}

func (h *SyncV2BootstrapHandler) ready() bool {
	return h != nil && h.resolver != nil && h.activity != nil
}

func NewSyncV2BootstrapHandler(resolver *PublicBoundProofResolver, activity *coregit.ActivityStore) (*SyncV2BootstrapHandler, error) {
	handler := &SyncV2BootstrapHandler{resolver: resolver, activity: activity}
	if !handler.ready() {
		return nil, identity.ErrInvalidPublicIdentity
	}
	return handler, nil
}

func (h *SyncV2BootstrapHandler) Handle(ctx context.Context, raw json.RawMessage, proof types.PublicRequestProof) (SyncBootstrapV2Result, error) {
	if !h.ready() {
		return SyncBootstrapV2Result{}, syncReadFailure("wormhole.sync.bootstrap", "internal_error")
	}
	var arguments SyncBootstrapV2Args
	if decodePublicArguments(raw, &arguments) != nil || !isCanonicalJSONObject(raw) {
		return SyncBootstrapV2Result{}, syncReadDecodeFailure("wormhole.sync.bootstrap", raw)
	}
	if !validSyncReadArguments(arguments.SyncV2Scope, arguments.AfterVersion) {
		return SyncBootstrapV2Result{}, syncReadFailure("wormhole.sync.bootstrap", "invalid_request")
	}
	var result SyncBootstrapV2Result
	err := h.resolver.Resolve(ctx, "wormhole.sync.bootstrap", raw, arguments.SyncV2Scope, proof, func(ctx context.Context, tx *sql.Tx, read VerifiedPublicBoundRead) error {
		state, err := syncReadStateInTx(ctx, tx, h.resolver.streams, read)
		if err != nil {
			return err
		}
		if arguments.AfterVersion > state.StreamVersion {
			return coregit.ErrStreamPrecondition
		}
		policy, err := h.activity.CurrentPolicyInTx(ctx, tx, coregit.FabricActivityStreamKey{
			ProjectID: read.Attachment.Key.ProjectID, FabricInstanceID: read.Attachment.Key.FabricInstanceID,
			StreamID: read.Attachment.Key.StreamID, CanonicalRef: read.Attachment.CanonicalRef,
		})
		if err != nil {
			return err
		}
		if _, err := projectstate.CanonicalActivityPolicy(policy); err != nil {
			return coregit.ErrActivityPolicyUnavailable
		}
		result = SyncBootstrapV2Result{
			Version: projectstate.SyncProtocolVersionV2, Changed: state.StreamVersion > arguments.AfterVersion,
			State: state, EffectiveActivityPolicy: policy,
		}
		return validateSyncBootstrapResult(result, read, arguments.AfterVersion)
	})
	if err != nil {
		code := syncReadErrorCode(err)
		if errors.Is(err, coregit.ErrActivityPolicyUnavailable) || errors.Is(err, projectstate.ErrInvalidActivityPolicy) {
			code = "activity_policy_required"
		}
		return SyncBootstrapV2Result{}, syncReadFailure("wormhole.sync.bootstrap", code)
	}
	return result, nil
}

type SyncV2PullHandler struct {
	resolver *PublicBoundProofResolver
}

func (h *SyncV2PullHandler) ready() bool {
	return h != nil && h.resolver != nil
}

func NewSyncV2PullHandler(resolver *PublicBoundProofResolver) (*SyncV2PullHandler, error) {
	handler := &SyncV2PullHandler{resolver: resolver}
	if !handler.ready() {
		return nil, identity.ErrInvalidPublicIdentity
	}
	return handler, nil
}

func (h *SyncV2PullHandler) Handle(ctx context.Context, raw json.RawMessage, proof types.PublicRequestProof) (SyncPullV2Result, error) {
	if !h.ready() {
		return SyncPullV2Result{}, syncReadFailure("wormhole.sync.pull", "internal_error")
	}
	var arguments SyncPullV2Args
	if decodePublicArguments(raw, &arguments) != nil || !isCanonicalJSONObject(raw) {
		return SyncPullV2Result{}, syncReadDecodeFailure("wormhole.sync.pull", raw)
	}
	if !validSyncReadArguments(arguments.SyncV2Scope, arguments.AfterVersion) {
		return SyncPullV2Result{}, syncReadFailure("wormhole.sync.pull", "invalid_request")
	}
	var result SyncPullV2Result
	err := h.resolver.Resolve(ctx, "wormhole.sync.pull", raw, arguments.SyncV2Scope, proof, func(ctx context.Context, tx *sql.Tx, read VerifiedPublicBoundRead) error {
		state, err := syncReadStateInTx(ctx, tx, h.resolver.streams, read)
		if err != nil {
			return err
		}
		if arguments.AfterVersion > state.StreamVersion {
			return coregit.ErrStreamPrecondition
		}
		result = SyncPullV2Result{
			Version: projectstate.SyncProtocolVersionV2, Changed: state.StreamVersion > arguments.AfterVersion, State: state,
		}
		return validateSyncPullResult(result, read, arguments.AfterVersion)
	})
	if err != nil {
		return SyncPullV2Result{}, syncReadFailure("wormhole.sync.pull", syncReadErrorCode(err))
	}
	return result, nil
}

type SyncV2PushHandler struct {
	resolver    *PublicBoundProofResolver
	coordinator *MutationCoordinator
	streams     *coregit.StreamStore
}

func (h *SyncV2PushHandler) ready() bool {
	return h != nil && h.resolver != nil && h.coordinator != nil && h.streams != nil
}

func NewSyncV2PushHandler(resolver *PublicBoundProofResolver, coordinator *MutationCoordinator, streams *coregit.StreamStore) (*SyncV2PushHandler, error) {
	handler := &SyncV2PushHandler{resolver: resolver, coordinator: coordinator, streams: streams}
	if !handler.ready() {
		return nil, identity.ErrInvalidPublicIdentity
	}
	return handler, nil
}

func (h *SyncV2PushHandler) Handle(ctx context.Context, raw json.RawMessage, proof types.PublicRequestProof) (any, error) {
	if !h.ready() {
		return nil, syncMutationFailure("wormhole.sync.push", "internal_error")
	}
	var arguments SyncPushV2Args
	if decodePublicArguments(raw, &arguments) != nil || !isCanonicalJSONObject(raw) {
		return nil, syncReadDecodeFailure("wormhole.sync.push", raw)
	}
	if arguments.Version != projectstate.SyncProtocolVersionV2 || !types.CanonicalUUID(arguments.AttachmentRef) {
		return nil, syncMutationFailure("wormhole.sync.push", "invalid_request")
	}
	authorized, err := h.resolver.AuthorizeMutation(ctx, "wormhole.sync.push", raw, arguments.SyncV2Scope, proof)
	if err != nil {
		return nil, syncMutationFailure("wormhole.sync.push", syncMutationErrorCode(err))
	}
	var transition coregit.StreamTransition
	err = h.coordinator.ExecutePublic(ctx, authorized, "sync.push", bytes.Clone(raw), func(ctx context.Context, tx *sql.Tx, verified VerifiedMutation) error {
		var err error
		transition, err = h.streams.ApplyPublicOperationInTx(ctx, tx, verified.Scope, coregit.ApplyPublicOperationInput{
			Attachment:   verified.Attachment,
			Precondition: syncMutationPrecondition(arguments.SyncV2Scope),
			Operation:    arguments.Operation,
		})
		return err
	})
	if err != nil {
		return nil, syncMutationFailure("wormhole.sync.push", syncMutationErrorCode(err))
	}
	if transition.Key.ProjectID != authorized.Authority.Scope.ProjectID || transition.Version < 0 || transition.Version > maximumPublicSyncVersion || !validPublicSyncDigest(transition.Live.Digest) || transition.AcceptedCommitSHA == "" {
		return nil, syncMutationFailure("wormhole.sync.push", "internal_error")
	}
	if transition.ConflictID != "" {
		if !types.CanonicalUUID(transition.ConflictID) {
			return nil, syncMutationFailure("wormhole.sync.push", "internal_error")
		}
		return SyncPushConflictV2Result{
			Version: projectstate.SyncProtocolVersionV2, Status: "conflict", OperationID: arguments.Operation.ID,
			StreamVersion: transition.Version, LiveTreeDigest: transition.Live.Digest, ConflictID: transition.ConflictID,
		}, nil
	}
	return SyncPushAppliedV2Result{
		Version: projectstate.SyncProtocolVersionV2, Status: "applied", OperationID: arguments.Operation.ID,
		StreamVersion: transition.Version, LiveTreeDigest: transition.Live.Digest,
	}, nil
}

type SyncV2ConflictHandler struct {
	resolver    *PublicBoundProofResolver
	coordinator *MutationCoordinator
	streams     *coregit.StreamStore
}

func (h *SyncV2ConflictHandler) ready() bool {
	return h != nil && h.resolver != nil && h.coordinator != nil && h.streams != nil
}

func NewSyncV2ConflictHandler(resolver *PublicBoundProofResolver, coordinator *MutationCoordinator, streams *coregit.StreamStore) (*SyncV2ConflictHandler, error) {
	handler := &SyncV2ConflictHandler{resolver: resolver, coordinator: coordinator, streams: streams}
	if !handler.ready() {
		return nil, identity.ErrInvalidPublicIdentity
	}
	return handler, nil
}

func (h *SyncV2ConflictHandler) Handle(ctx context.Context, raw json.RawMessage, proof types.PublicRequestProof) (SyncConflictResolvedV2Result, error) {
	if !h.ready() {
		return SyncConflictResolvedV2Result{}, syncMutationFailure("wormhole.sync.conflict", "internal_error")
	}
	var arguments SyncConflictV2Args
	if decodePublicArguments(raw, &arguments) != nil || !isCanonicalJSONObject(raw) {
		return SyncConflictResolvedV2Result{}, syncReadDecodeFailure("wormhole.sync.conflict", raw)
	}
	if arguments.Version != projectstate.SyncProtocolVersionV2 || !types.CanonicalUUID(arguments.AttachmentRef) || !types.CanonicalUUID(arguments.ConflictID) {
		return SyncConflictResolvedV2Result{}, syncMutationFailure("wormhole.sync.conflict", "invalid_request")
	}
	authorized, err := h.resolver.AuthorizeMutation(ctx, "wormhole.sync.conflict", raw, arguments.SyncV2Scope, proof)
	if err != nil {
		return SyncConflictResolvedV2Result{}, syncMutationFailure("wormhole.sync.conflict", syncMutationErrorCode(err))
	}
	var transition coregit.StreamTransition
	err = h.coordinator.ExecutePublic(ctx, authorized, "sync.conflict", bytes.Clone(raw), func(ctx context.Context, tx *sql.Tx, verified VerifiedMutation) error {
		var resolveErr error
		transition, resolveErr = h.streams.ResolveConflictInTx(ctx, tx, verified.Scope, coregit.ResolveStreamConflictInput{
			Attachment:   verified.Attachment,
			ConflictID:   arguments.ConflictID,
			Precondition: syncMutationPrecondition(arguments.SyncV2Scope),
			Resolution:   arguments.Resolution,
		})
		return resolveErr
	})
	if err != nil {
		return SyncConflictResolvedV2Result{}, syncMutationFailure("wormhole.sync.conflict", syncMutationErrorCode(err))
	}
	if transition.ConflictID != "" || transition.Key.ProjectID != authorized.Authority.Scope.ProjectID || transition.Version < 0 || transition.Version > maximumPublicSyncVersion || !validPublicSyncDigest(transition.Live.Digest) {
		return SyncConflictResolvedV2Result{}, syncMutationFailure("wormhole.sync.conflict", "internal_error")
	}
	return SyncConflictResolvedV2Result{
		Version: projectstate.SyncProtocolVersionV2, Status: "resolved", ConflictID: arguments.ConflictID,
		OperationID: arguments.Resolution.ID, StreamVersion: transition.Version, LiveTreeDigest: transition.Live.Digest,
	}, nil
}

func syncMutationPrecondition(scope SyncV2Scope) coregit.SyncPrecondition {
	return coregit.SyncPrecondition{
		Repository: scope.Repository, CanonicalRef: scope.CanonicalRef,
		BaseCommitSHA: scope.BaseCommitSHA, BaseTreeDigest: scope.BaseTreeDigest,
		ExpectedStreamVersion: scope.ExpectedStreamVersion, ExpectedLiveTreeDigest: scope.ExpectedLiveTreeDigest,
	}
}

func syncMutationFailure(operation, code string) error {
	return syncReadFailure(operation, code)
}

func syncMutationErrorCode(err error) string {
	switch {
	case errors.Is(err, identity.ErrPublicAuthentication), errors.Is(err, identity.ErrPublicNonceReplay), errors.Is(err, identity.ErrInvalidPublicIdentity):
		return "authentication_failed"
	case errors.Is(err, coregit.ErrStreamNotFound):
		return "attachment_not_found"
	case errors.Is(err, coregit.ErrStreamActor):
		return "permission_denied"
	case errors.Is(err, coregit.ErrStreamPrecondition):
		return "sync_precondition_failed"
	case errors.Is(err, coregit.ErrStreamConflict):
		return "sync_conflict"
	case errors.Is(err, coregit.ErrOperationReplay):
		return "sync_replay_conflict"
	case errors.Is(err, projectstate.ErrInvalidSnapshot), errors.Is(err, projectstate.ErrUnknownVersion),
		errors.Is(err, projectstate.ErrUnknownKind), errors.Is(err, projectstate.ErrBrokenReference),
		errors.Is(err, projectstate.ErrInvalidActorEnvelope), errors.Is(err, projectstate.ErrTrackedSecret),
		errors.Is(err, projectstate.ErrOperationPrecondition), errors.Is(err, projectstate.ErrImmutableRecord),
		errors.Is(err, projectstate.ErrTombstoneDigest), errors.Is(err, projectstate.ErrResurrectionDigest):
		return "sync_precondition_failed"
	default:
		return "internal_error"
	}
}

const maximumPublicSyncVersion int64 = 9_007_199_254_740_991

func validSyncReadArguments(scope SyncV2Scope, afterVersion int64) bool {
	if scope.Version != projectstate.SyncProtocolVersionV2 || !types.CanonicalUUID(scope.AttachmentRef) ||
		scope.ExpectedStreamVersion < 0 || scope.ExpectedStreamVersion > maximumPublicSyncVersion ||
		afterVersion < 0 || afterVersion > maximumPublicSyncVersion ||
		!validPublicSyncDigest(scope.ExpectedLiveTreeDigest) {
		return false
	}
	probe := types.WorkspaceBinding{
		Scope:      types.WorkspaceScope{ProjectID: "00000000-0000-4000-8000-000000000001", WorkspaceID: "00000000-0000-4000-8000-000000000002"},
		Checkout:   types.CheckoutIdentity{CanonicalPath: "/wormhole-public-read-validation", Device: 1, Inode: 1},
		Repository: scope.Repository, AcceptedRef: scope.CanonicalRef, AcceptedCommitSHA: scope.BaseCommitSHA,
		AcceptedTreeDigest: string(scope.BaseTreeDigest),
	}
	return scope.Repository.Provider != "" && probe.Validate() == nil
}

func validPublicSyncDigest(digest projectstate.Digest) bool {
	value := string(digest)
	return len(value) == 71 && strings.HasPrefix(value, "sha256:") && strings.Trim(value[7:], "0123456789abcdef") == ""
}

func syncReadStateInTx(ctx context.Context, tx *sql.Tx, streams *coregit.StreamStore, read VerifiedPublicBoundRead) (SyncStateV2, error) {
	if err := streams.ValidateCurrentAttachmentStateInTx(ctx, tx, read.Attachment, read.State); err != nil {
		return SyncStateV2{}, err
	}
	acceptedTree, err := projectstate.EncodeTree(read.State.Accepted)
	if err != nil {
		return SyncStateV2{}, coregit.ErrStreamCorrupt
	}
	liveTree, err := projectstate.EncodeTree(read.State.Live)
	if err != nil {
		return SyncStateV2{}, coregit.ErrStreamCorrupt
	}
	acceptedDigest, err := projectstate.DigestTree(acceptedTree)
	if err != nil || acceptedDigest != read.State.Accepted.Digest {
		return SyncStateV2{}, coregit.ErrStreamCorrupt
	}
	liveDigest, err := projectstate.DigestTree(liveTree)
	if err != nil || liveDigest != read.State.Live.Digest {
		return SyncStateV2{}, coregit.ErrStreamCorrupt
	}
	conflicts, err := streams.OpenConflictIDsInTx(ctx, tx, read.Attachment)
	if err != nil {
		return SyncStateV2{}, err
	}
	if conflicts == nil {
		conflicts = []string{}
	}
	return SyncStateV2{
		StreamVersion: read.State.Version, AcceptedCommitSHA: read.State.AcceptedCommitSHA,
		AcceptedTreeDigest: acceptedDigest, LiveTreeDigest: liveDigest,
		AcceptedTree: acceptedTree, LiveTree: liveTree, OpenConflictIDs: conflicts,
	}, nil
}

func validateSyncPullResult(result SyncPullV2Result, read VerifiedPublicBoundRead, afterVersion int64) error {
	if result.Version != projectstate.SyncProtocolVersionV2 || result.Changed != (result.State.StreamVersion > afterVersion) {
		return coregit.ErrStreamCorrupt
	}
	return validateSyncReadState(result.State, read)
}

func validateSyncBootstrapResult(result SyncBootstrapV2Result, read VerifiedPublicBoundRead, afterVersion int64) error {
	if result.Version != projectstate.SyncProtocolVersionV2 || result.Changed != (result.State.StreamVersion > afterVersion) {
		return coregit.ErrStreamCorrupt
	}
	if _, err := projectstate.CanonicalActivityPolicy(result.EffectiveActivityPolicy); err != nil {
		return coregit.ErrActivityPolicyUnavailable
	}
	return validateSyncReadState(result.State, read)
}

func validateSyncReadState(state SyncStateV2, read VerifiedPublicBoundRead) error {
	if state.StreamVersion != read.State.Version || state.AcceptedCommitSHA != read.State.AcceptedCommitSHA ||
		state.AcceptedTreeDigest != read.State.Accepted.Digest || state.LiveTreeDigest != read.State.Live.Digest ||
		state.StreamVersion < 0 || state.StreamVersion > maximumPublicSyncVersion || state.OpenConflictIDs == nil {
		return coregit.ErrStreamCorrupt
	}
	if !sort.StringsAreSorted(state.OpenConflictIDs) {
		return coregit.ErrStreamCorrupt
	}
	for index, id := range state.OpenConflictIDs {
		if !types.CanonicalUUID(id) || index > 0 && state.OpenConflictIDs[index-1] == id {
			return coregit.ErrStreamCorrupt
		}
	}
	for _, tree := range []struct {
		value  projectstate.Tree
		digest projectstate.Digest
	}{{state.AcceptedTree, state.AcceptedTreeDigest}, {state.LiveTree, state.LiveTreeDigest}} {
		digest, err := projectstate.DigestTree(tree.value)
		if err != nil || digest != tree.digest {
			return coregit.ErrStreamCorrupt
		}
		snapshot, err := projectstate.DecodeTree(tree.value)
		if err != nil || projectstate.Validate(snapshot) != nil || snapshot.Config.ProjectID != read.Attachment.Key.ProjectID || snapshot.Config.Repository != read.Attachment.Repository {
			return coregit.ErrStreamCorrupt
		}
	}
	return nil
}

func syncReadDecodeFailure(operation string, raw json.RawMessage) error {
	version := attachVersion(raw)
	if version != 0 && version != projectstate.SyncProtocolVersionV2 {
		return syncReadFailure(operation, "unknown_version")
	}
	return syncReadFailure(operation, "invalid_request")
}

func syncReadErrorCode(err error) string {
	switch {
	case errors.Is(err, identity.ErrPublicAuthentication), errors.Is(err, identity.ErrPublicNonceReplay), errors.Is(err, identity.ErrInvalidPublicIdentity):
		return "authentication_failed"
	case errors.Is(err, coregit.ErrStreamNotFound):
		return "attachment_not_found"
	case errors.Is(err, coregit.ErrStreamPrecondition):
		return "sync_precondition_failed"
	default:
		return "internal_error"
	}
}

func syncReadFailure(operation, code string) error {
	canonical, err := projectstate.CanonicalJSON(ToolFailureV1{Code: code, Operation: operation})
	if err != nil {
		return errors.New(`{"code":"internal_error","operation":"` + operation + `"}`)
	}
	return errors.New(string(bytes.TrimSuffix(canonical, []byte{'\n'})))
}
