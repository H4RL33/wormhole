package mcp

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

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

func NewPublicProofVerifier(id string, now func() time.Time) (*PublicProofVerifier, error) {
	if !types.CanonicalUUID(id) || now == nil {
		return nil, identity.ErrInvalidPublicIdentity
	}
	return &PublicProofVerifier{id, now}, nil
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
	canonical, e := projectstate.CanonicalJSON(args)
	if e != nil {
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
