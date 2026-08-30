package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	coregit "github.com/H4RL33/wormhole/internal/core/git"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/types"
)

type ActivityMutationAuthority struct{ Authority identity.MutationAuthority }
type VerifiedActivityRead struct {
	Authority  types.ActorScope
	Attachment coregit.StreamAttachment
	State      coregit.StreamTransition
}
type ActivityReadFunc func(context.Context, *sql.Tx, VerifiedActivityRead) error

func activityNonceUse(projectID string, bound boundPublicAuthority, verified VerifiedPublicProof) identity.PublicNonceUse {
	a := bound.attached.Attachment
	return identity.PublicNonceUse{ProjectID: projectID, FabricInstanceID: a.Key.FabricInstanceID, StreamID: a.Key.StreamID, CanonicalRef: a.CanonicalRef, KeyFingerprint: verified.KeyFingerprint, Claim: verified.Claim}
}

func (r *PublicBoundProofResolver) AuthorizeActivityMutation(ctx context.Context, tool, attachmentRef string, raw json.RawMessage, proof types.PublicRequestProof) (ActivityMutationAuthority, error) {
	if r == nil || r.identity == nil || r.streams == nil || !r.verifier.readyForFabric(r.fabricInstanceID) {
		return ActivityMutationAuthority{}, identity.ErrInvalidPublicIdentity
	}
	verified, err := r.verifier.VerifyBound(tool, attachmentRef, raw, proof)
	if err != nil {
		return ActivityMutationAuthority{}, err
	}
	projectID, err := r.streams.ResolveAttachmentProject(ctx, r.fabricInstanceID, attachmentRef)
	if err != nil {
		return ActivityMutationAuthority{}, err
	}
	tx, err := r.identity.BeginProjectTx(ctx, projectID)
	if err != nil {
		return ActivityMutationAuthority{}, err
	}
	defer tx.Rollback()
	bound, err := r.resolveAttachmentAuthorityInTx(ctx, tx, projectID, attachmentRef, verified)
	if err != nil {
		return ActivityMutationAuthority{}, err
	}
	if err := r.identity.ConsumePublicNonceInTx(ctx, tx, activityNonceUse(projectID, bound, verified)); err != nil {
		return ActivityMutationAuthority{}, err
	}
	if err := tx.Commit(); err != nil {
		return ActivityMutationAuthority{}, fmt.Errorf("mcp: commit Activity mutation authorization: %w", err)
	}
	if bound.decisionErr != nil {
		return ActivityMutationAuthority{}, bound.decisionErr
	}
	return ActivityMutationAuthority{Authority: bound.authority}, nil
}

func (r *PublicBoundProofResolver) ResolveActivityRead(ctx context.Context, tool, attachmentRef string, raw json.RawMessage, proof types.PublicRequestProof, callback ActivityReadFunc) error {
	if r == nil || r.identity == nil || r.streams == nil || !r.verifier.readyForFabric(r.fabricInstanceID) || callback == nil {
		return identity.ErrInvalidPublicIdentity
	}
	verified, err := r.verifier.VerifyBound(tool, attachmentRef, raw, proof)
	if err != nil {
		return err
	}
	projectID, err := r.streams.ResolveAttachmentProject(ctx, r.fabricInstanceID, attachmentRef)
	if err != nil {
		return err
	}
	tx, err := r.identity.BeginProjectTxWithOptions(ctx, projectID, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	bound, err := r.resolveAttachmentAuthorityInTx(ctx, tx, projectID, attachmentRef, verified)
	if err != nil {
		return err
	}
	if err := r.identity.ConsumePublicNonceInTx(ctx, tx, activityNonceUse(projectID, bound, verified)); err != nil {
		return err
	}
	if bound.decisionErr != nil {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("mcp: commit denied Activity read authorization: %w", err)
		}
		return bound.decisionErr
	}
	read := VerifiedActivityRead{Authority: bound.authority.Scope, Attachment: bound.attached.Attachment, State: bound.attached.State}
	if err := callback(ctx, tx, read); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mcp: commit Activity read: %w", err)
	}
	return nil
}
