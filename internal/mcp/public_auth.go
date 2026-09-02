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
	"fmt"
	"strings"
	"time"

	coregit "github.com/H4RL33/wormhole/internal/core/git"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

type VerifiedPublicProof struct {
	KeyFingerprint string
	PublicKey      [ed25519.PublicKeySize]byte
	Timestamp      time.Time
	Claim          identity.PublicNonceClaim
	SessionID      string
}
type PublicProofVerifier struct {
	fabricInstanceID string
	now              func() time.Time
}

type VerifiedPublicBoundRead struct {
	Proof      VerifiedPublicProof
	Attachment coregit.StreamAttachment
	State      coregit.StreamTransition
}

type PublicBoundReadFunc func(context.Context, *sql.Tx, VerifiedPublicBoundRead) error

type boundPublicAuthority struct {
	proof       VerifiedPublicProof
	attached    coregit.StreamAttachmentState
	authority   identity.MutationAuthority
	decisionErr error
}

// PublicBoundProofResolver owns the complete authorization transaction for a
// request signed against an opaque attachment. The callback may read additional
// same-project evidence, but it does not own commit or rollback.
type PublicBoundProofResolver struct {
	fabricInstanceID string
	identity         *identity.Store
	streams          *coregit.StreamStore
	verifier         *PublicProofVerifier
}

func NewPublicBoundProofResolver(fabricInstanceID string, identityStore *identity.Store, streams *coregit.StreamStore, verifier *PublicProofVerifier) (*PublicBoundProofResolver, error) {
	if identityStore == nil || streams == nil || !verifier.readyForFabric(fabricInstanceID) {
		return nil, identity.ErrInvalidPublicIdentity
	}
	return &PublicBoundProofResolver{fabricInstanceID: fabricInstanceID, identity: identityStore, streams: streams, verifier: verifier}, nil
}

func (r *PublicBoundProofResolver) Resolve(ctx context.Context, tool string, raw json.RawMessage, scope SyncV2Scope, proof types.PublicRequestProof, callback PublicBoundReadFunc) error {
	if r == nil || r.identity == nil || r.streams == nil || !r.verifier.readyForFabric(r.fabricInstanceID) || callback == nil {
		return identity.ErrInvalidPublicIdentity
	}
	verified, err := r.verifier.VerifyBound(tool, scope.AttachmentRef, raw, proof)
	if err != nil {
		return err
	}
	projectID, err := r.streams.ResolveAttachmentProject(ctx, r.fabricInstanceID, scope.AttachmentRef)
	if err != nil {
		return err
	}
	tx, err := r.identity.BeginProjectTx(ctx, projectID)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	bound, err := r.resolveBoundAuthorityInTx(ctx, tx, projectID, scope, verified)
	if err != nil {
		return err
	}
	if bound.decisionErr != nil {
		return bound.decisionErr
	}
	if !syncScopeMatchesAttachment(scope, bound.attached) {
		return coregit.ErrStreamPrecondition
	}
	if err := r.identity.ConsumePublicNonceInTx(ctx, tx, identity.PublicNonceUse{
		ProjectID: projectID, FabricInstanceID: bound.attached.Attachment.Key.FabricInstanceID,
		StreamID: bound.attached.Attachment.Key.StreamID, CanonicalRef: bound.attached.Attachment.CanonicalRef,
		KeyFingerprint: verified.KeyFingerprint, Claim: verified.Claim,
	}); err != nil {
		return err
	}
	if err := callback(ctx, tx, VerifiedPublicBoundRead{Proof: bound.proof, Attachment: bound.attached.Attachment, State: bound.attached.State}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (r *PublicBoundProofResolver) resolveBoundAuthorityInTx(ctx context.Context, tx *sql.Tx, projectID string, scope SyncV2Scope, verified VerifiedPublicProof) (boundPublicAuthority, error) {
	bound, err := r.resolveAttachmentAuthorityInTx(ctx, tx, projectID, scope.AttachmentRef, verified)
	if err != nil {
		return boundPublicAuthority{}, err
	}
	if bound.decisionErr == nil && !syncMutationScopeMatchesRoute(scope, bound.attached) {
		bound.decisionErr = coregit.ErrStreamPrecondition
	}
	return bound, nil
}

func (r *PublicBoundProofResolver) resolveAttachmentAuthorityInTx(ctx context.Context, tx *sql.Tx, projectID, attachmentRef string, verified VerifiedPublicProof) (boundPublicAuthority, error) {
	attached, err := r.streams.LockAttachmentInTx(ctx, tx, coregit.AttachmentLookup{ProjectID: projectID, FabricInstanceID: r.fabricInstanceID, AttachmentRef: attachmentRef})
	if err != nil {
		return boundPublicAuthority{}, err
	}
	if !completePublicAttachment(attached) {
		return boundPublicAuthority{}, coregit.ErrStreamCorrupt
	}
	if verified.KeyFingerprint != attached.Attachment.IssuerKeyFingerprint {
		return boundPublicAuthority{}, identity.ErrPublicAuthentication
	}
	bound := boundPublicAuthority{proof: verified, attached: attached}
	human, actorErr := resolveVerifiedTrackedHuman(attached.State.Accepted, verified)
	actor := types.ActorEnvelope{}
	if actorErr == nil {
		actor = types.ActorEnvelope{ActorKind: types.ActorHuman, HumanPrincipalID: human.ID, Assurance: types.AssurancePublicKeyContinuity, OccurredAt: verified.Timestamp}
		if verified.SessionID != "" {
			actor, actorErr = r.identity.ResolveHistoricalPublicSessionActorInTx(ctx, tx, authorityEvidence(attached), verified.SessionID, verified.Timestamp)
			if actorErr == nil && actor.AccountableHumanID != human.ID {
				actorErr = identity.ErrPublicAuthentication
			}
		}
	}
	if actorErr == nil {
		a := attached.Attachment
		bound.authority = identity.MutationAuthority{Scope: types.ActorScope{ProjectID: projectID, Actor: actor}, FabricInstanceID: a.Key.FabricInstanceID, StreamID: a.Key.StreamID, WorkspaceID: a.WorkspaceID, CanonicalRef: a.CanonicalRef, AttachmentRef: a.AttachmentRef, IssuerKeyFingerprint: a.IssuerKeyFingerprint, SessionID: verified.SessionID}
		_, actorErr = r.identity.RevalidateMutationAuthorityInTx(ctx, tx, bound.authority, authorityEvidence(attached))
	}
	if actorErr != nil {
		bound.decisionErr = identity.ErrPublicAuthentication
	}
	return bound, nil
}

func (r *PublicBoundProofResolver) AuthorizeMutation(ctx context.Context, tool string, raw json.RawMessage, scope SyncV2Scope, proof types.PublicRequestProof) (PublicMutationAuthority, error) {
	if r == nil || r.identity == nil || r.streams == nil || !r.verifier.readyForFabric(r.fabricInstanceID) {
		return PublicMutationAuthority{}, identity.ErrInvalidPublicIdentity
	}
	verified, err := r.verifier.VerifyBound(tool, scope.AttachmentRef, raw, proof)
	if err != nil {
		return PublicMutationAuthority{}, err
	}
	projectID, err := r.streams.ResolveAttachmentProject(ctx, r.fabricInstanceID, scope.AttachmentRef)
	if err != nil {
		return PublicMutationAuthority{}, err
	}
	tx, err := r.identity.BeginProjectTx(ctx, projectID)
	if err != nil {
		return PublicMutationAuthority{}, err
	}
	defer tx.Rollback()

	bound, err := r.resolveBoundAuthorityInTx(ctx, tx, projectID, scope, verified)
	if err != nil {
		return PublicMutationAuthority{}, err
	}
	if err := r.identity.ConsumePublicNonceInTx(ctx, tx, identity.PublicNonceUse{
		ProjectID: projectID, FabricInstanceID: bound.attached.Attachment.Key.FabricInstanceID,
		StreamID: bound.attached.Attachment.Key.StreamID, CanonicalRef: bound.attached.Attachment.CanonicalRef,
		KeyFingerprint: verified.KeyFingerprint, Claim: verified.Claim,
	}); err != nil {
		return PublicMutationAuthority{}, err
	}
	if err := tx.Commit(); err != nil {
		return PublicMutationAuthority{}, fmt.Errorf("mcp: commit public mutation authorization: %w", err)
	}
	if bound.decisionErr != nil {
		return PublicMutationAuthority{}, bound.decisionErr
	}
	return PublicMutationAuthority{Authority: bound.authority, SignedScope: scope}, nil
}

func syncScopeMatchesAttachment(scope SyncV2Scope, attached coregit.StreamAttachmentState) bool {
	attachment, state := attached.Attachment, attached.State
	return scope.Version == projectstate.SyncProtocolVersionV2 &&
		scope.AttachmentRef == attachment.AttachmentRef && scope.Repository == attachment.Repository &&
		scope.CanonicalRef == attachment.CanonicalRef && scope.BaseCommitSHA == state.AcceptedCommitSHA &&
		scope.BaseTreeDigest == state.Accepted.Digest && scope.ExpectedStreamVersion == state.Version &&
		scope.ExpectedLiveTreeDigest == state.Live.Digest
}

func syncMutationScopeMatchesRoute(scope SyncV2Scope, attached coregit.StreamAttachmentState) bool {
	return validSyncReadArguments(scope, 0) && completePublicAttachment(attached) &&
		scope.AttachmentRef == attached.Attachment.AttachmentRef &&
		scope.Repository == attached.Attachment.Repository &&
		scope.CanonicalRef == attached.Attachment.CanonicalRef
}

func completePublicAttachment(attached coregit.StreamAttachmentState) bool {
	attachment := attached.Attachment
	return authorityMatchesAttachment(identity.MutationAuthority{
		Scope: types.ActorScope{ProjectID: attachment.Key.ProjectID}, FabricInstanceID: attachment.Key.FabricInstanceID,
		StreamID: attachment.Key.StreamID, WorkspaceID: attachment.WorkspaceID, CanonicalRef: attachment.CanonicalRef,
		AttachmentRef: attachment.AttachmentRef, IssuerKeyFingerprint: attachment.IssuerKeyFingerprint,
	}, attached)
}

func NewPublicProofVerifier(id string, now func() time.Time) (*PublicProofVerifier, error) {
	verifier := &PublicProofVerifier{fabricInstanceID: id, now: now}
	if !verifier.readyForFabric(id) {
		return nil, identity.ErrInvalidPublicIdentity
	}
	return verifier, nil
}
func (v *PublicProofVerifier) readyForFabric(fabricInstanceID string) bool {
	return v != nil && types.CanonicalUUID(fabricInstanceID) && v.fabricInstanceID == fabricInstanceID && v.now != nil
}
func (v *PublicProofVerifier) VerifyInitialAttach(tool string, repo types.RepositoryIdentity, ref string, args json.RawMessage, p types.PublicRequestProof) (VerifiedPublicProof, error) {
	if p.SessionID != "" {
		return VerifiedPublicProof{}, identity.ErrPublicAuthentication
	}
	scope, e := projectstate.RepositoryScopeKey(repo, ref)
	if e != nil {
		return VerifiedPublicProof{}, identity.ErrPublicAuthentication
	}
	return v.verify(tool, scope, args, p)
}
func (v *PublicProofVerifier) VerifyBound(tool, attachment string, args json.RawMessage, p types.PublicRequestProof) (VerifiedPublicProof, error) {
	if !types.CanonicalUUID(attachment) || (p.SessionID != "" && !types.CanonicalUUID(p.SessionID)) {
		return VerifiedPublicProof{}, identity.ErrPublicAuthentication
	}
	scope := "attachment:" + attachment
	if p.SessionID != "" {
		scope += ":session:" + p.SessionID
	}
	return v.verify(tool, scope, args, p)
}
func (v *PublicProofVerifier) verify(tool, scope string, args json.RawMessage, p types.PublicRequestProof) (VerifiedPublicProof, error) {
	if v == nil || !v.readyForFabric(v.fabricInstanceID) {
		return VerifiedPublicProof{}, identity.ErrPublicAuthentication
	}
	key, ok := strictRawURL(p.PublicKey, ed25519.PublicKeySize)
	if !ok {
		return VerifiedPublicProof{}, identity.ErrPublicAuthentication
	}
	nonce, ok := strictRawURL(p.Nonce, 32)
	if !ok {
		return VerifiedPublicProof{}, identity.ErrPublicAuthentication
	}
	sig, ok := strictRawURL(p.Signature, ed25519.SignatureSize)
	if !ok {
		return VerifiedPublicProof{}, identity.ErrPublicAuthentication
	}
	sum := sha256.Sum256(key)
	want := "sha256:" + hex.EncodeToString(sum[:])
	if p.KeyID != want || strings.ToLower(p.KeyID) != p.KeyID {
		return VerifiedPublicProof{}, identity.ErrPublicAuthentication
	}
	at, e := time.Parse(time.RFC3339Nano, p.Timestamp)
	if e != nil || at.Location() != time.UTC || at.Format(time.RFC3339Nano) != p.Timestamp {
		return VerifiedPublicProof{}, identity.ErrPublicAuthentication
	}
	now := v.now()
	if now.Location() != time.UTC || at.Before(now.Add(-5*time.Minute)) || at.After(now.Add(30*time.Second)) {
		return VerifiedPublicProof{}, identity.ErrPublicAuthentication
	}
	canonical, e := projectstate.CanonicalJSONObject(args)
	if e != nil || !bytes.Equal(canonical, args) {
		return VerifiedPublicProof{}, identity.ErrPublicAuthentication
	}
	var n [32]byte
	copy(n[:], nonce)
	msg, e := projectstate.PublicProofMessage(v.fabricInstanceID, tool, scope, canonical, at, n)
	if e != nil || !ed25519.Verify(ed25519.PublicKey(key), msg, sig) {
		return VerifiedPublicProof{}, identity.ErrPublicAuthentication
	}
	nh := sha256.Sum256(nonce)
	var ka [ed25519.PublicKeySize]byte
	copy(ka[:], key)
	return VerifiedPublicProof{want, ka, at, identity.PublicNonceClaim{NonceHash: hex.EncodeToString(nh[:]), ExpiresAt: at.Add(5 * time.Minute)}, p.SessionID}, nil
}
func strictRawURL(value string, size int) ([]byte, bool) {
	d, e := base64.RawURLEncoding.Strict().DecodeString(value)
	return d, e == nil && len(d) == size && base64.RawURLEncoding.EncodeToString(d) == value
}

func resolveVerifiedTrackedHuman(snapshot projectstate.Snapshot, proof VerifiedPublicProof) (projectstate.ActorV1, error) {
	var matched *projectstate.ActorV1
	matches := 0
	for _, record := range snapshot.Actors {
		if record.Value == nil || record.Tombstone != nil || record.Value.ActorKind != types.ActorHuman {
			continue
		}
		for _, key := range record.Value.PublicKeys {
			decoded, err := base64.StdEncoding.DecodeString(key.PublicKeyBase64)
			if err != nil || key.Algorithm != "ed25519" ||
				base64.StdEncoding.EncodeToString(decoded) != key.PublicKeyBase64 || !bytes.Equal(decoded, proof.PublicKey[:]) {
				continue
			}
			matches++
			candidate := *record.Value
			candidate.PublicKeys = append([]projectstate.PublicKeyV1(nil), record.Value.PublicKeys...)
			matched = &candidate
		}
	}
	if matches != 1 || matched == nil {
		return projectstate.ActorV1{}, identity.ErrPublicAuthentication
	}
	return *matched, nil
}
