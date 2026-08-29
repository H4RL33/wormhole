package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	if h == nil {
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

func NewSyncV2AttachHandler(fabricInstanceID, credentialRef string, observer coregit.CanonicalGitObserver, coordinator syncAttachCoordinator, policySource SyncAttachPolicySource, verifier *PublicProofVerifier) (*SyncV2AttachHandler, error) {
	if !types.CanonicalUUID(fabricInstanceID) || credentialRef == "" || strings.TrimSpace(credentialRef) != credentialRef || strings.ContainsAny(credentialRef, "\r\n") ||
		observer == nil || coordinator == nil || policySource == nil || verifier == nil {
		return nil, identity.ErrInvalidPublicIdentity
	}
	return &SyncV2AttachHandler{
		fabricInstanceID: fabricInstanceID,
		credentialRef:    credentialRef,
		observer:         observer,
		coordinator:      coordinator,
		policySource:     policySource,
		verifier:         verifier,
	}, nil
}
