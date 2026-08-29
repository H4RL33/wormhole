package mcp

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

type publicProofTestFixture struct {
	verifier   *PublicProofVerifier
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
	now        time.Time
	arguments  json.RawMessage
}

func newPublicProofTestFixture(t *testing.T) publicProofTestFixture {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	verifier, err := NewPublicProofVerifier("11111111-1111-4111-8111-111111111111", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return publicProofTestFixture{verifier: verifier, privateKey: privateKey, publicKey: publicKey, now: now, arguments: json.RawMessage(`{"version":2}`)}
}

func (f publicProofTestFixture) signedProof(t *testing.T, tool, scope string, at time.Time, sessionID string) types.PublicRequestProof {
	t.Helper()
	canonical, err := projectstate.CanonicalJSON(f.arguments)
	if err != nil {
		t.Fatal(err)
	}
	var nonce [32]byte
	copy(nonce[:], []byte("01234567890123456789012345678901"))
	message, err := projectstate.PublicProofMessage("11111111-1111-4111-8111-111111111111", tool, scope, canonical, at, nonce)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256(f.publicKey)
	return types.PublicRequestProof{
		KeyID:     "sha256:" + hex.EncodeToString(fingerprint[:]),
		PublicKey: base64.RawURLEncoding.EncodeToString(f.publicKey),
		Timestamp: at.Format(time.RFC3339Nano),
		Nonce:     base64.RawURLEncoding.EncodeToString(nonce[:]),
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(f.privateKey, message)),
		SessionID: sessionID,
	}
}

func TestPublicProofVerifierInitialAndBoundScopes(t *testing.T) {
	f := newPublicProofTestFixture(t)
	repository := types.RepositoryIdentity{Provider: "github", ImmutableID: "123", CanonicalRemote: "https://github.com/H4RL33/wormhole"}
	repositoryScope, err := projectstate.RepositoryScopeKey(repository, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	initial := f.signedProof(t, "wormhole.sync.attach", repositoryScope, f.now, "")
	got, err := f.verifier.VerifyInitialAttach("wormhole.sync.attach", repository, "refs/heads/main", f.arguments, initial)
	if err != nil {
		t.Fatal(err)
	}
	if got.KeyFingerprint != initial.KeyID || got.SessionID != "" || got.Claim.ExpiresAt != f.now.Add(5*time.Minute) {
		t.Fatalf("initial proof=%+v", got)
	}

	attachment := "44444444-4444-4444-8444-444444444444"
	session := "55555555-5555-4555-8555-555555555555"
	bound := f.signedProof(t, "wormhole.sync.push", "attachment:"+attachment+":session:"+session, f.now, session)
	got, err = f.verifier.VerifyBound("wormhole.sync.push", attachment, f.arguments, bound)
	if err != nil || got.SessionID != session {
		t.Fatalf("bound proof=%+v err=%v", got, err)
	}
}

func TestPublicProofVerifierRejectsEncodingLengthTimeScopeAndTamper(t *testing.T) {
	f := newPublicProofTestFixture(t)
	attachment := "44444444-4444-4444-8444-444444444444"
	scope := "attachment:" + attachment
	valid := f.signedProof(t, "wormhole.sync.pull", scope, f.now, "")
	tests := map[string]func(*types.PublicRequestProof, *json.RawMessage){
		"padded public key":  func(p *types.PublicRequestProof, _ *json.RawMessage) { p.PublicKey += "=" },
		"non URL public key": func(p *types.PublicRequestProof, _ *json.RawMessage) { p.PublicKey = strings.Repeat("+", 43) },
		"public key 31": func(p *types.PublicRequestProof, _ *json.RawMessage) {
			p.PublicKey = base64.RawURLEncoding.EncodeToString(make([]byte, 31))
		},
		"public key 33": func(p *types.PublicRequestProof, _ *json.RawMessage) {
			p.PublicKey = base64.RawURLEncoding.EncodeToString(make([]byte, 33))
		},
		"nonce 31": func(p *types.PublicRequestProof, _ *json.RawMessage) {
			p.Nonce = base64.RawURLEncoding.EncodeToString(make([]byte, 31))
		},
		"nonce 33": func(p *types.PublicRequestProof, _ *json.RawMessage) {
			p.Nonce = base64.RawURLEncoding.EncodeToString(make([]byte, 33))
		},
		"signature 63": func(p *types.PublicRequestProof, _ *json.RawMessage) {
			p.Signature = base64.RawURLEncoding.EncodeToString(make([]byte, 63))
		},
		"signature 65": func(p *types.PublicRequestProof, _ *json.RawMessage) {
			p.Signature = base64.RawURLEncoding.EncodeToString(make([]byte, 65))
		},
		"empty key id":           func(p *types.PublicRequestProof, _ *json.RawMessage) { p.KeyID = "" },
		"uppercase key id":       func(p *types.PublicRequestProof, _ *json.RawMessage) { p.KeyID = strings.ToUpper(p.KeyID) },
		"wrong key id":           func(p *types.PublicRequestProof, _ *json.RawMessage) { p.KeyID = "sha256:" + strings.Repeat("0", 64) },
		"noncanonical timestamp": func(p *types.PublicRequestProof, _ *json.RawMessage) { p.Timestamp = "2026-08-29T13:00:00+01:00" },
		"argument tamper":        func(_ *types.PublicRequestProof, raw *json.RawMessage) { *raw = json.RawMessage(`{"version":3}`) },
		"signature tamper": func(p *types.PublicRequestProof, _ *json.RawMessage) {
			decoded, _ := base64.RawURLEncoding.DecodeString(p.Signature)
			decoded[0] ^= 1
			p.Signature = base64.RawURLEncoding.EncodeToString(decoded)
		},
		"unexpected session": func(p *types.PublicRequestProof, _ *json.RawMessage) {
			p.SessionID = "55555555-5555-4555-8555-555555555555"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			proof := valid
			arguments := append(json.RawMessage(nil), f.arguments...)
			mutate(&proof, &arguments)
			if _, err := f.verifier.VerifyBound("wormhole.sync.pull", attachment, arguments, proof); !errors.Is(err, identity.ErrPublicAuthentication) {
				t.Fatalf("error=%v, want ErrPublicAuthentication", err)
			}
		})
	}

	for name, at := range map[string]time.Time{
		"lower inclusive": f.now.Add(-5 * time.Minute),
		"upper inclusive": f.now.Add(30 * time.Second),
	} {
		t.Run(name, func(t *testing.T) {
			proof := f.signedProof(t, "wormhole.sync.pull", scope, at, "")
			if _, err := f.verifier.VerifyBound("wormhole.sync.pull", attachment, f.arguments, proof); err != nil {
				t.Fatal(err)
			}
		})
	}
	for name, at := range map[string]time.Time{
		"lower minus 1ns": f.now.Add(-5*time.Minute - time.Nanosecond),
		"upper plus 1ns":  f.now.Add(30*time.Second + time.Nanosecond),
	} {
		t.Run(name, func(t *testing.T) {
			proof := f.signedProof(t, "wormhole.sync.pull", scope, at, "")
			if _, err := f.verifier.VerifyBound("wormhole.sync.pull", attachment, f.arguments, proof); !errors.Is(err, identity.ErrPublicAuthentication) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestPublicProofVerifierRejectsWrongFabricToolAndScope(t *testing.T) {
	f := newPublicProofTestFixture(t)
	attachment := "44444444-4444-4444-8444-444444444444"
	proof := f.signedProof(t, "wormhole.sync.pull", "attachment:"+attachment, f.now, "")
	wrongFabric, err := NewPublicProofVerifier("11111111-1111-4111-8111-111111111112", func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	for name, verify := range map[string]func() error{
		"fabric": func() error {
			_, err := wrongFabric.VerifyBound("wormhole.sync.pull", attachment, f.arguments, proof)
			return err
		},
		"tool": func() error {
			_, err := f.verifier.VerifyBound("wormhole.sync.push", attachment, f.arguments, proof)
			return err
		},
		"scope": func() error {
			_, err := f.verifier.VerifyBound("wormhole.sync.pull", "44444444-4444-4444-8444-444444444445", f.arguments, proof)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := verify(); !errors.Is(err, identity.ErrPublicAuthentication) {
				t.Fatalf("error=%v, want ErrPublicAuthentication", err)
			}
		})
	}
}
