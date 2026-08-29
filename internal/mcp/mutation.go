package mcp

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	coregit "github.com/H4RL33/wormhole/internal/core/git"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

var errInvalidMutation = errors.New("mcp: invalid mutation")

type VerifiedMutation struct {
	Scope      types.ActorScope
	Attachment coregit.StreamAttachment
	State      coregit.StreamTransition
}

type MutationFunc func(context.Context, *sql.Tx, VerifiedMutation) error

type MutationCoordinator struct {
	identity *identity.Store
	streams  *coregit.StreamStore
	activity *coregit.ActivityStore
}

func NewMutationCoordinator(identityStore *identity.Store, streams *coregit.StreamStore, activity *coregit.ActivityStore) (*MutationCoordinator, error) {
	if identityStore == nil || streams == nil || activity == nil {
		return nil, errInvalidMutation
	}
	return &MutationCoordinator{identity: identityStore, streams: streams, activity: activity}, nil
}

func (m *MutationCoordinator) Execute(ctx context.Context, authority identity.MutationAuthority, action string, canonicalPayload []byte, callback MutationFunc) error {
	if !validMutationCoordinator(m) || callback == nil || authority.Scope.Validate() != nil || !validAuditAction(action) || !isCanonicalJSONObject(canonicalPayload) {
		return errInvalidMutation
	}
	tx, err := m.identity.BeginProjectTx(ctx, authority.Scope.ProjectID)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	locked, err := m.streams.LockAttachmentInTx(ctx, tx, coregit.AttachmentLookup{
		ProjectID: authority.Scope.ProjectID, FabricInstanceID: authority.FabricInstanceID, AttachmentRef: authority.AttachmentRef,
	})
	if err != nil {
		return err
	}
	if !authorityMatchesAttachment(authority, locked) {
		return identity.ErrPublicAuthentication
	}
	evidence := authorityEvidence(locked)
	scope, err := m.identity.RevalidateMutationAuthorityInTx(ctx, tx, authority, evidence)
	if err != nil {
		return err
	}
	if err := callback(ctx, tx, VerifiedMutation{Scope: scope, Attachment: locked.Attachment, State: locked.State}); err != nil {
		return err
	}
	if _, err := m.identity.RecordActorActionInTx(ctx, tx, scope, action, canonicalPayload); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mcp: commit mutation: %w", err)
	}
	return nil
}

type InitialAttachCommand struct {
	ProjectID, FabricInstanceID string
	Repository                  types.RepositoryIdentity
	CanonicalRef                string
	Observation                 coregit.RefObservation
	ObservedTree                projectstate.Tree
	ObservedHuman               projectstate.ActorV1
	TransportActor              types.ActorEnvelope
	KeyFingerprint              string
	PublicKey                   [ed25519.PublicKeySize]byte
	Nonce                       identity.PublicNonceClaim
	Policy                      projectstate.EffectiveActivityPolicyV1
	CanonicalRequest            []byte
}

type InitialAttachReplayCommand struct {
	Initial InitialAttachCommand
}

type InitialAttachResult struct {
	Attachment coregit.StreamAttachment
	State      coregit.StreamTransition
	Policy     projectstate.EffectiveActivityPolicyV1
}

func (m *MutationCoordinator) ExecuteInitialAttach(ctx context.Context, command InitialAttachCommand) (InitialAttachResult, error) {
	if !validMutationCoordinator(m) || validateInitialAttachCommand(command) != nil {
		return InitialAttachResult{}, identity.ErrInvalidPublicIdentity
	}
	tx, err := m.identity.BeginProjectTx(ctx, command.ProjectID)
	if err != nil {
		return InitialAttachResult{}, err
	}
	defer tx.Rollback()
	lockKey := strings.Join([]string{command.ProjectID, command.FabricInstanceID, command.Repository.Provider, command.Repository.ImmutableID, command.Repository.CanonicalRemote, command.CanonicalRef, command.KeyFingerprint}, ":")
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, lockKey); err != nil {
		return InitialAttachResult{}, fmt.Errorf("mcp: lock initial attach route: %w", err)
	}
	_, resolveErr := m.streams.ResolvePublicAttachmentByIssuerInTx(ctx, tx, coregit.PublicAttachIssuerLookup{
		ProjectID: command.ProjectID, FabricInstanceID: command.FabricInstanceID,
		Repository: command.Repository, CanonicalRef: command.CanonicalRef,
		IssuerKeyFingerprint: command.KeyFingerprint,
	})
	if resolveErr == nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return InitialAttachResult{}, fmt.Errorf("mcp: rollback existing initial attach: %w", rollbackErr)
		}
		return m.ReplayInitialAttach(ctx, InitialAttachReplayCommand{Initial: command})
	}
	if !errors.Is(resolveErr, coregit.ErrStreamNotFound) {
		return InitialAttachResult{}, resolveErr
	}

	result, err := m.executeFirstAttachInTx(ctx, tx, command)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return InitialAttachResult{}, fmt.Errorf("mcp: commit initial attach: %w", err)
		}
		return result, nil
	}
	if !errors.Is(err, coregit.ErrPublicAttachActivationConflict) && !errors.Is(err, coregit.ErrPublicAttachClaimConflict) {
		return InitialAttachResult{}, err
	}
	if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
		return InitialAttachResult{}, fmt.Errorf("mcp: rollback duplicate initial attach: %w", rollbackErr)
	}
	return m.ReplayInitialAttach(ctx, InitialAttachReplayCommand{Initial: command})
}

func (m *MutationCoordinator) executeFirstAttachInTx(ctx context.Context, tx *sql.Tx, command InitialAttachCommand) (InitialAttachResult, error) {
	scope := types.ActorScope{ProjectID: command.ProjectID, Actor: command.TransportActor}
	draft, err := m.streams.BeginPublicAttachInTx(ctx, tx, scope, coregit.PublicAttachInput{
		ProjectID: command.ProjectID, FabricInstanceID: command.FabricInstanceID,
		Repository: command.Repository, Ref: command.Observation, Tree: command.ObservedTree,
	})
	if err != nil {
		return InitialAttachResult{}, err
	}
	activityKey := coregit.FabricActivityStreamKey{
		ProjectID: draft.Key.ProjectID, FabricInstanceID: draft.Key.FabricInstanceID,
		StreamID: draft.Key.StreamID, CanonicalRef: draft.CanonicalRef,
	}
	policy, err := m.activity.PublishPolicyInTx(ctx, tx, activityKey, command.Policy)
	if err != nil {
		return InitialAttachResult{}, err
	}
	activated, err := m.identity.ActivatePublicHumanInTx(ctx, tx, identity.PublicHumanActivation{
		ProjectID: command.ProjectID, FabricInstanceID: command.FabricInstanceID,
		StreamID: draft.Key.StreamID, CanonicalRef: draft.CanonicalRef, SourceVersion: draft.SourceVersion,
		ObservedHuman: command.ObservedHuman, TransportActor: command.TransportActor,
		KeyFingerprint: command.KeyFingerprint, PublicKey: command.PublicKey,
	})
	if err != nil {
		return InitialAttachResult{}, err
	}
	claimed, err := m.streams.ClaimPublicAttachInTx(ctx, tx, draft, command.KeyFingerprint)
	if err != nil {
		return InitialAttachResult{}, err
	}
	if err := m.identity.ConsumePublicNonceInTx(ctx, tx, publicNonceUse(command, claimed.Attachment)); err != nil {
		return InitialAttachResult{}, err
	}
	if _, err := m.identity.RecordActorActionInTx(ctx, tx, activated, "sync.attach", command.CanonicalRequest); err != nil {
		return InitialAttachResult{}, err
	}
	return InitialAttachResult{Attachment: claimed.Attachment, State: claimed.State, Policy: policy}, nil
}

func (m *MutationCoordinator) ReplayInitialAttach(ctx context.Context, replay InitialAttachReplayCommand) (InitialAttachResult, error) {
	command := replay.Initial
	if !validMutationCoordinator(m) || validateInitialAttachCommand(command) != nil {
		return InitialAttachResult{}, identity.ErrInvalidPublicIdentity
	}

	// This authorization transaction intentionally commits the fresh nonce
	// independently. Later changed source evidence therefore cannot make a
	// signed denied retry reusable.
	tx, err := m.identity.BeginProjectTx(ctx, command.ProjectID)
	if err != nil {
		return InitialAttachResult{}, err
	}
	resolved, err := m.streams.ResolvePublicAttachmentByIssuerInTx(ctx, tx, coregit.PublicAttachIssuerLookup{
		ProjectID: command.ProjectID, FabricInstanceID: command.FabricInstanceID,
		Repository: command.Repository, CanonicalRef: command.CanonicalRef,
		IssuerKeyFingerprint: command.KeyFingerprint,
	})
	if err != nil {
		_ = tx.Rollback()
		return InitialAttachResult{}, err
	}
	authority := identity.MutationAuthority{
		Scope:            types.ActorScope{ProjectID: command.ProjectID, Actor: command.TransportActor},
		FabricInstanceID: resolved.Attachment.Key.FabricInstanceID,
		StreamID:         resolved.Attachment.Key.StreamID, WorkspaceID: resolved.Attachment.WorkspaceID,
		CanonicalRef: resolved.Attachment.CanonicalRef, AttachmentRef: resolved.Attachment.AttachmentRef,
		IssuerKeyFingerprint: resolved.Attachment.IssuerKeyFingerprint,
		SessionID:            command.TransportActor.SessionID,
	}
	if !authorityMatchesAttachment(authority, resolved) {
		_ = tx.Rollback()
		return InitialAttachResult{}, identity.ErrPublicAuthentication
	}
	if _, err := m.identity.RevalidateMutationAuthorityInTx(ctx, tx, authority, authorityEvidence(resolved)); err != nil {
		_ = tx.Rollback()
		return InitialAttachResult{}, err
	}
	if err := m.identity.ConsumePublicNonceInTx(ctx, tx, publicNonceUse(command, resolved.Attachment)); err != nil {
		_ = tx.Rollback()
		return InitialAttachResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return InitialAttachResult{}, fmt.Errorf("mcp: commit attach replay authorization: %w", err)
	}

	readTx, err := m.identity.BeginProjectTx(ctx, command.ProjectID)
	if err != nil {
		return InitialAttachResult{}, err
	}
	defer readTx.Rollback()
	replayed, err := m.streams.ReplayPublicAttachInTx(ctx, readTx, coregit.PublicAttachReplayInput{
		ProjectID: command.ProjectID, FabricInstanceID: command.FabricInstanceID,
		Repository: command.Repository, Ref: command.Observation, Tree: command.ObservedTree,
		IssuerKeyFingerprint: command.KeyFingerprint,
	})
	if err != nil {
		return InitialAttachResult{}, err
	}
	current := coregit.StreamAttachmentState{Attachment: replayed.Attachment, State: replayed.State}
	if !authorityMatchesAttachment(authority, current) {
		return InitialAttachResult{}, identity.ErrPublicAuthentication
	}
	if _, err := m.identity.RevalidateMutationAuthorityInTx(ctx, readTx, authority, authorityEvidence(current)); err != nil {
		return InitialAttachResult{}, err
	}
	policy, err := m.activity.CurrentPolicyInTx(ctx, readTx, replayed.ActivityKey)
	if err != nil {
		return InitialAttachResult{}, err
	}
	if err := readTx.Commit(); err != nil {
		return InitialAttachResult{}, fmt.Errorf("mcp: commit attach replay read: %w", err)
	}
	return InitialAttachResult{Attachment: replayed.Attachment, State: replayed.State, Policy: policy}, nil
}

func validMutationCoordinator(m *MutationCoordinator) bool {
	return m != nil && m.identity != nil && m.streams != nil && m.activity != nil
}

func validAuditAction(action string) bool {
	return action != "" && len(action) <= 256 && strings.TrimSpace(action) == action && !strings.ContainsAny(action, "\r\n")
}

func isCanonicalJSONObject(raw []byte) bool {
	if len(raw) == 0 || rejectDuplicateJSONMembers(raw) != nil {
		return false
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return false
	}
	if _, ok := decoded.(map[string]any); !ok {
		return false
	}
	canonical, err := json.Marshal(decoded)
	return err == nil && bytes.Equal(canonical, raw)
}

func authorityMatchesAttachment(authority identity.MutationAuthority, state coregit.StreamAttachmentState) bool {
	attachment := state.Attachment
	return authority.Scope.ProjectID == attachment.Key.ProjectID &&
		authority.FabricInstanceID == attachment.Key.FabricInstanceID &&
		authority.StreamID == attachment.Key.StreamID && authority.WorkspaceID == attachment.WorkspaceID &&
		authority.CanonicalRef == attachment.CanonicalRef && authority.AttachmentRef == attachment.AttachmentRef &&
		authority.IssuerKeyFingerprint == attachment.IssuerKeyFingerprint &&
		attachment.Repository.Provider != "" && attachment.Repository.Validate() == nil &&
		types.CanonicalUUID(attachment.ActivitySourceRef) && attachment.SourceVersion >= 0 &&
		attachment.SourceVersion <= state.State.Version && attachment.Writable && state.State.Key == attachment.Key
}

func authorityEvidence(state coregit.StreamAttachmentState) identity.PublicAuthorityEvidence {
	attachment := state.Attachment
	return identity.PublicAuthorityEvidence{
		ProjectID: attachment.Key.ProjectID, FabricInstanceID: attachment.Key.FabricInstanceID,
		StreamID: attachment.Key.StreamID, WorkspaceID: attachment.WorkspaceID,
		CanonicalRef: attachment.CanonicalRef, AttachmentRef: attachment.AttachmentRef,
		IssuerKeyFingerprint: attachment.IssuerKeyFingerprint, AttachmentSourceVersion: attachment.SourceVersion,
		CurrentStreamVersion: state.State.Version, Accepted: state.State.Accepted,
	}
}

func publicNonceUse(command InitialAttachCommand, attachment coregit.StreamAttachment) identity.PublicNonceUse {
	return identity.PublicNonceUse{
		ProjectID: attachment.Key.ProjectID, FabricInstanceID: attachment.Key.FabricInstanceID,
		StreamID: attachment.Key.StreamID, CanonicalRef: attachment.CanonicalRef,
		KeyFingerprint: command.KeyFingerprint, Claim: command.Nonce,
	}
}

func validateInitialAttachCommand(command InitialAttachCommand) error {
	if !types.CanonicalUUID(command.ProjectID) || !types.CanonicalUUID(command.FabricInstanceID) ||
		command.Repository.Provider == "" || command.Repository.Validate() != nil ||
		command.Repository != command.Observation.Repository || command.CanonicalRef != command.Observation.RefName ||
		command.TransportActor.Validate() != nil || command.TransportActor.ActorKind != types.ActorHuman ||
		command.TransportActor.Assurance != types.AssurancePublicKeyContinuity ||
		command.ObservedHuman.ActorKind != types.ActorHuman || command.ObservedHuman.ID != command.TransportActor.HumanPrincipalID ||
		command.Observation.ObservedAt.IsZero() || command.Observation.ObservedAt.Location() != time.UTC ||
		command.Nonce.ExpiresAt.Location() != time.UTC ||
		!command.Nonce.ExpiresAt.Equal(command.TransportActor.OccurredAt.Add(5*time.Minute)) ||
		len(command.Nonce.NonceHash) != 64 || strings.Trim(command.Nonce.NonceHash, "0123456789abcdef") != "" {
		return identity.ErrInvalidPublicIdentity
	}
	keyDigest := sha256.Sum256(command.PublicKey[:])
	if command.KeyFingerprint != "sha256:"+hex.EncodeToString(keyDigest[:]) {
		return identity.ErrInvalidPublicIdentity
	}
	keyMatches := 0
	for _, key := range command.ObservedHuman.PublicKeys {
		decoded, err := base64.StdEncoding.DecodeString(key.PublicKeyBase64)
		if err == nil && key.Algorithm == "ed25519" && bytes.Equal(decoded, command.PublicKey[:]) {
			keyMatches++
		}
	}
	if _, err := projectstate.CanonicalActivityPolicy(command.Policy); keyMatches != 1 || err != nil {
		return identity.ErrInvalidPublicIdentity
	}
	digest, err := projectstate.DigestTree(command.ObservedTree)
	if err != nil {
		return identity.ErrInvalidPublicIdentity
	}
	observationProbe := types.WorkspaceBinding{
		Scope:      types.WorkspaceScope{ProjectID: command.ProjectID, WorkspaceID: types.WorkspaceID("00000000-0000-4000-8000-000000000001")},
		Checkout:   types.CheckoutIdentity{CanonicalPath: "/wormhole-public-attach-validation", Device: 1, Inode: 1},
		Repository: command.Repository, AcceptedRef: command.Observation.RefName,
		AcceptedCommitSHA: command.Observation.CommitSHA, AcceptedTreeDigest: string(digest),
	}
	if observationProbe.Validate() != nil {
		return identity.ErrInvalidPublicIdentity
	}
	snapshot, err := projectstate.DecodeTree(command.ObservedTree)
	if err != nil || projectstate.Validate(snapshot) != nil || snapshot.Config.ProjectID != command.ProjectID || snapshot.Config.Repository != command.Repository {
		return identity.ErrInvalidPublicIdentity
	}
	tracked, ok := snapshot.Actors[command.ObservedHuman.ID]
	if !ok || tracked.Value == nil || tracked.Tombstone != nil || !reflect.DeepEqual(*tracked.Value, command.ObservedHuman) {
		return identity.ErrInvalidPublicIdentity
	}
	var arguments SyncAttachV2Args
	if decodePublicArguments(command.CanonicalRequest, &arguments) != nil {
		return identity.ErrInvalidPublicIdentity
	}
	var decoded any
	if err := json.Unmarshal(command.CanonicalRequest, &decoded); err != nil {
		return identity.ErrInvalidPublicIdentity
	}
	canonical, err := json.Marshal(decoded)
	if err != nil || !bytes.Equal(canonical, command.CanonicalRequest) || arguments.Version != projectstate.SyncProtocolVersionV2 ||
		arguments.Repository != command.Repository || arguments.CanonicalRef != command.CanonicalRef ||
		arguments.BaseCommitSHA != command.Observation.CommitSHA || arguments.BaseTreeDigest != digest {
		return identity.ErrInvalidPublicIdentity
	}
	return nil
}
