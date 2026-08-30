package mcp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	coregit "github.com/H4RL33/wormhole/internal/core/git"
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

func TestPublicProofBoundResolverRejectsInvalidDependencies(t *testing.T) {
	f := newPublicProofTestFixture(t)
	fabricID := "11111111-1111-4111-8111-111111111111"
	streams := coregit.NewStreamStore(testDB(t))
	identities := identity.NewStore(testDB(t))
	for name, dependencies := range map[string]struct {
		fabric   string
		identity *identity.Store
		streams  *coregit.StreamStore
		verifier *PublicProofVerifier
	}{
		"nil identity": {fabric: fabricID, streams: streams, verifier: f.verifier},
		"nil streams":  {fabric: fabricID, identity: identities, verifier: f.verifier},
		"nil verifier": {fabric: fabricID, identity: identities, streams: streams},
		"bad fabric":   {fabric: "not-a-uuid", identity: identities, streams: streams, verifier: f.verifier},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewPublicBoundProofResolver(dependencies.fabric, dependencies.identity, dependencies.streams, dependencies.verifier); !errors.Is(err, identity.ErrInvalidPublicIdentity) {
				t.Fatalf("error = %v, want ErrInvalidPublicIdentity", err)
			}
		})
	}
}

func TestPublicProofBoundRejectsMalformedProofBeforeSQL(t *testing.T) {
	f := newPublicProofTestFixture(t)
	resolver, err := NewPublicBoundProofResolver(
		"11111111-1111-4111-8111-111111111111",
		identity.NewStore(nil), coregit.NewStreamStore(nil), f.verifier,
	)
	if err != nil {
		t.Fatal(err)
	}
	scope := SyncV2Scope{AttachmentRef: "44444444-4444-4444-8444-444444444444"}
	called := false
	err = resolver.Resolve(context.Background(), "wormhole.sync.pull", f.arguments, scope, types.PublicRequestProof{}, func(context.Context, *sql.Tx, VerifiedPublicBoundRead) error {
		called = true
		return nil
	})
	if !errors.Is(err, identity.ErrPublicAuthentication) || called {
		t.Fatalf("Resolve = (%v, callback=%v), want authentication failure before callback", err, called)
	}
}

func TestPublicProofResolveVerifiedTrackedHumanRequiresOneCanonicalLiveKey(t *testing.T) {
	f := newPublicProofTestFixture(t)
	fingerprint := sha256.Sum256(f.publicKey)
	verified := VerifiedPublicProof{
		KeyFingerprint: "sha256:" + hex.EncodeToString(fingerprint[:]),
	}
	copy(verified.PublicKey[:], f.publicKey)
	humanID := "22222222-2222-4222-8222-222222222222"
	human := projectstate.ActorV1{
		SchemaVersion: 1, Kind: "actor", ID: humanID, ActorKind: types.ActorHuman, DisplayName: "Human",
		PublicKeys: []projectstate.PublicKeyV1{{KeyID: "primary", Algorithm: "ed25519", PublicKeyBase64: base64.StdEncoding.EncodeToString(f.publicKey)}},
		Extensions: projectstate.ExtensionsV1{},
	}
	snapshot := projectstate.Snapshot{Actors: map[string]projectstate.Record[projectstate.ActorV1]{humanID: {Value: &human}}}
	got, err := resolveVerifiedTrackedHuman(snapshot, verified)
	if err != nil || got.ID != humanID {
		t.Fatalf("resolved human = %+v, %v", got, err)
	}

	tests := map[string]func(*projectstate.Snapshot){
		"missing": func(s *projectstate.Snapshot) { s.Actors = map[string]projectstate.Record[projectstate.ActorV1]{} },
		"tombstoned": func(s *projectstate.Snapshot) {
			record := s.Actors[humanID]
			record.Tombstone = &projectstate.TombstoneV1{}
			s.Actors[humanID] = record
		},
		"agent": func(s *projectstate.Snapshot) { s.Actors[humanID].Value.ActorKind = types.ActorAgent },
		"noncanonical base64": func(s *projectstate.Snapshot) {
			s.Actors[humanID].Value.PublicKeys[0].PublicKeyBase64 = strings.TrimSuffix(s.Actors[humanID].Value.PublicKeys[0].PublicKeyBase64, "=")
		},
		"duplicate key on human": func(s *projectstate.Snapshot) {
			s.Actors[humanID].Value.PublicKeys = append(s.Actors[humanID].Value.PublicKeys, s.Actors[humanID].Value.PublicKeys[0])
		},
		"same key on another human": func(s *projectstate.Snapshot) {
			other := *s.Actors[humanID].Value
			other.ID = "33333333-3333-4333-8333-333333333333"
			s.Actors[other.ID] = projectstate.Record[projectstate.ActorV1]{Value: &other}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			copyHuman := human
			copyHuman.PublicKeys = append([]projectstate.PublicKeyV1(nil), human.PublicKeys...)
			candidate := projectstate.Snapshot{Actors: map[string]projectstate.Record[projectstate.ActorV1]{humanID: {Value: &copyHuman}}}
			mutate(&candidate)
			if _, err := resolveVerifiedTrackedHuman(candidate, verified); !errors.Is(err, identity.ErrPublicAuthentication) {
				t.Fatalf("error = %v, want ErrPublicAuthentication", err)
			}
		})
	}
}

func mutationAuthorizationRequest(t *testing.T, f *mutationFixture, scope SyncV2Scope, nonceByte byte, sessionID string) (json.RawMessage, types.PublicRequestProof) {
	t.Helper()
	raw, err := json.Marshal(scope)
	if err != nil {
		t.Fatal(err)
	}
	raw = canonicalMutationJSON(t, raw)
	seed := sha256.Sum256([]byte(f.projectID))
	proof := signedBoundSessionProof(t, f.fabricID, "wormhole.sync.push", raw, scope.AttachmentRef, sessionID, f.transport.OccurredAt, bytesOf(nonceByte, 32), seed[:])
	return raw, proof
}

func realBoundResolverForDB(t *testing.T, f *mutationFixture, db *sql.DB) *PublicBoundProofResolver {
	t.Helper()
	verifier, err := NewPublicProofVerifier(f.fabricID, func() time.Time { return f.transport.OccurredAt })
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewPublicBoundProofResolver(f.fabricID, identity.NewStore(db), coregit.NewStreamStore(db), verifier)
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func TestPublicBoundMutationAuthorizationBurnsNonceBeforeSignedRouteDenial(t *testing.T) {
	tests := map[string]func(*SyncV2Scope){
		"repository provider":         func(scope *SyncV2Scope) { scope.Repository.Provider = "gitlab" },
		"repository immutable id":     func(scope *SyncV2Scope) { scope.Repository.ImmutableID = "987654321" },
		"repository canonical remote": func(scope *SyncV2Scope) { scope.Repository.CanonicalRemote += "-moved" },
		"canonical ref":               func(scope *SyncV2Scope) { scope.CanonicalRef = "refs/heads/other" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			f := newMutationFixture(t)
			attached := f.attach(1)
			scope := boundReadArguments(attached, 0).SyncV2Scope
			mutate(&scope)
			raw, proof := mutationAuthorizationRequest(t, f, scope, 2, "")
			before := task2MutationSnapshot(t, f.db, f.projectID)
			resolver := realBoundResolverForDB(t, f, publicRuntimeDB(t))
			_, err := resolver.AuthorizeMutation(context.Background(), "wormhole.sync.push", raw, scope, proof)
			if !errors.Is(err, coregit.ErrStreamPrecondition) {
				t.Fatalf("AuthorizeMutation error = %v, want ErrStreamPrecondition", err)
			}
			assertTask2MutationDelta(t, before, task2MutationSnapshot(t, f.db, f.projectID), 1)
		})
	}
}

func TestPublicBoundMutationAuthorizationBurnsNonceBeforeSessionDenial(t *testing.T) {
	f := newMutationFixture(t)
	attached := f.attach(3)
	scope := boundReadArguments(attached, 0).SyncV2Scope
	raw, proof := mutationAuthorizationRequest(t, f, scope, 4, "00000000-0000-4000-8000-000000000099")
	before := task2MutationSnapshot(t, f.db, f.projectID)
	_, err := realBoundResolverForDB(t, f, publicRuntimeDB(t)).AuthorizeMutation(context.Background(), "wormhole.sync.push", raw, scope, proof)
	if !errors.Is(err, identity.ErrPublicAuthentication) {
		t.Fatalf("AuthorizeMutation error = %v, want ErrPublicAuthentication", err)
	}
	assertTask2MutationDelta(t, before, task2MutationSnapshot(t, f.db, f.projectID), 1)
}

func TestPublicBoundMutationAuthorizationRejectsUnverifiedAndWrongIssuerWithoutNonce(t *testing.T) {
	for _, name := range []string{"unverified", "wrong issuer"} {
		t.Run(name, func(t *testing.T) {
			f := newMutationFixture(t)
			attached := f.attach(5)
			scope := boundReadArguments(attached, 0).SyncV2Scope
			raw, proof := mutationAuthorizationRequest(t, f, scope, 6, "")
			if name == "wrong issuer" {
				wrongSeed := sha256.Sum256([]byte("wrong-issuer-" + f.projectID))
				proof = signedBoundProof(t, f.fabricID, "wormhole.sync.push", raw, scope.AttachmentRef, f.transport.OccurredAt, bytesOf(6, 32), wrongSeed[:])
			} else {
				signature, _ := base64.RawURLEncoding.DecodeString(proof.Signature)
				signature[0] ^= 1
				proof.Signature = base64.RawURLEncoding.EncodeToString(signature)
			}
			before := task2MutationSnapshot(t, f.db, f.projectID)
			_, err := realBoundResolverForDB(t, f, publicRuntimeDB(t)).AuthorizeMutation(context.Background(), "wormhole.sync.push", raw, scope, proof)
			if !errors.Is(err, identity.ErrPublicAuthentication) {
				t.Fatalf("AuthorizeMutation error = %v, want ErrPublicAuthentication", err)
			}
			assertTask2MutationDelta(t, before, task2MutationSnapshot(t, f.db, f.projectID), 0)
		})
	}
}

func TestPublicBoundReadResolverStillRollsBackNonceOnReadFailure(t *testing.T) {
	f := newMutationFixture(t)
	attached := f.attach(7)
	scope := boundReadArguments(attached, 0).SyncV2Scope
	raw, _ := mutationAuthorizationRequest(t, f, scope, 8, "")
	seed := sha256.Sum256([]byte(f.projectID))
	proof := signedBoundProof(t, f.fabricID, "wormhole.sync.pull", raw, scope.AttachmentRef, f.transport.OccurredAt, bytesOf(8, 32), seed[:])
	before := task2MutationSnapshot(t, f.db, f.projectID)
	want := errors.New("read callback failed")
	err := realBoundResolverForDB(t, f, publicRuntimeDB(t)).Resolve(context.Background(), "wormhole.sync.pull", raw, scope, proof, func(context.Context, *sql.Tx, VerifiedPublicBoundRead) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("Resolve error = %v, want callback sentinel", err)
	}
	assertTask2MutationDelta(t, before, task2MutationSnapshot(t, f.db, f.projectID), 0)
}

func TestPublicBoundMutationPreAuthorizationMissingAndDetachedBurnNoNonce(t *testing.T) {
	for _, detached := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "detached"}[detached], func(t *testing.T) {
			f := newMutationFixture(t)
			attached := f.attach(9)
			scope := boundReadArguments(attached, 0).SyncV2Scope
			if !detached {
				scope.AttachmentRef = "00000000-0000-4000-8000-000000000098"
			} else if _, err := f.db.Exec(`UPDATE fabric_workspace_stream_bindings SET writable=false,detached_at=now() WHERE project_id=$1 AND attachment_ref=$2`, f.projectID, scope.AttachmentRef); err != nil {
				t.Fatal(err)
			}
			raw, proof := mutationAuthorizationRequest(t, f, scope, 10, "")
			before := task2MutationSnapshot(t, f.db, f.projectID)
			_, err := realBoundResolverForDB(t, f, publicRuntimeDB(t)).AuthorizeMutation(context.Background(), "wormhole.sync.push", raw, scope, proof)
			if !errors.Is(err, coregit.ErrStreamNotFound) {
				t.Fatalf("AuthorizeMutation error = %v, want ErrStreamNotFound", err)
			}
			assertTask2MutationDelta(t, before, task2MutationSnapshot(t, f.db, f.projectID), 0)
		})
	}
}

func TestPublicBoundMutationAuthorizationConcurrentNonceHasOneWinner(t *testing.T) {
	f := newMutationFixture(t)
	attached := f.attach(17)
	scope := boundReadArguments(attached, 0).SyncV2Scope
	raw, proof := mutationAuthorizationRequest(t, f, scope, 18, "")
	runtimeDB := publicRuntimeDB(t)
	resolver := realBoundResolverForDB(t, f, runtimeDB)
	coordinator, err := NewMutationCoordinator(identity.NewStore(runtimeDB), coregit.NewStreamStore(runtimeDB), coregit.NewActivityStore(runtimeDB))
	if err != nil {
		t.Fatal(err)
	}
	before := task2MutationSnapshot(t, f.db, f.projectID)
	authorities := make([]PublicMutationAuthority, 2)
	errs := raceAtRealAttachmentLock(t, f.db, coordinator, coregit.AttachmentLookup{
		ProjectID: f.projectID, FabricInstanceID: f.fabricID, AttachmentRef: attached.Attachment.AttachmentRef,
	}, []func() error{
		func() error {
			var authorizeErr error
			authorities[0], authorizeErr = resolver.AuthorizeMutation(context.Background(), "wormhole.sync.push", raw, scope, proof)
			return authorizeErr
		},
		func() error {
			var authorizeErr error
			authorities[1], authorizeErr = resolver.AuthorizeMutation(context.Background(), "wormhole.sync.push", raw, scope, proof)
			return authorizeErr
		},
	})
	winners, replays := 0, 0
	for index, err := range errs {
		switch {
		case err == nil:
			winners++
			if authorities[index].Authority.AttachmentRef != attached.Attachment.AttachmentRef {
				t.Fatalf("winner authority = %+v", authorities[index])
			}
		case errors.Is(err, identity.ErrPublicNonceReplay):
			replays++
		default:
			t.Fatalf("authorization %d error = %v", index, err)
		}
	}
	if winners != 1 || replays != 1 {
		t.Fatalf("authorization outcomes winners=%d replays=%d", winners, replays)
	}
	assertTask2MutationDelta(t, before, task2MutationSnapshot(t, f.db, f.projectID), 1)
}

func TestPublicBoundMutationAuthorizationForcedRLSCrossProjectAndFabricIsolation(t *testing.T) {
	first := newMutationFixture(t)
	second := newMutationFixture(t)
	oldFabricID := second.fabricID
	if _, err := second.db.Exec(`UPDATE project_repository_bindings SET fabric_instance_id=$1 WHERE project_id=$2 AND fabric_instance_id=$3`, first.fabricID, second.projectID, oldFabricID); err != nil {
		t.Fatal(err)
	}
	second.fabricID = first.fabricID
	fixtures := []struct {
		owner    *mutationFixture
		attached InitialAttachResult
	}{{first, first.attach(19)}, {second, second.attach(20)}}
	runtimeDB := publicRuntimeDB(t)
	resolver := realBoundResolverForDB(t, first, runtimeDB)
	for index, fixture := range fixtures {
		scope := boundReadArguments(fixture.attached, 0).SyncV2Scope
		raw, proof := mutationAuthorizationRequest(t, fixture.owner, scope, byte(21+index), "")
		beforeFirst := task2MutationSnapshot(t, first.db, first.projectID)
		beforeSecond := task2MutationSnapshot(t, second.db, second.projectID)
		authorized, err := resolver.AuthorizeMutation(context.Background(), "wormhole.sync.push", raw, scope, proof)
		if err != nil || authorized.Authority.Scope.ProjectID != fixture.owner.projectID {
			t.Fatalf("project %d authorization = (%+v, %v)", index, authorized, err)
		}
		firstDelta, secondDelta := 0, 0
		if fixture.owner == first {
			firstDelta = 1
		} else {
			secondDelta = 1
		}
		assertTask2MutationDelta(t, beforeFirst, task2MutationSnapshot(t, first.db, first.projectID), firstDelta)
		assertTask2MutationDelta(t, beforeSecond, task2MutationSnapshot(t, second.db, second.projectID), secondDelta)
	}

	wrongFabric := "00000000-0000-4000-8000-000000000097"
	verifier, err := NewPublicProofVerifier(wrongFabric, func() time.Time { return first.transport.OccurredAt })
	if err != nil {
		t.Fatal(err)
	}
	wrongResolver, err := NewPublicBoundProofResolver(wrongFabric, identity.NewStore(runtimeDB), coregit.NewStreamStore(runtimeDB), verifier)
	if err != nil {
		t.Fatal(err)
	}
	scope := boundReadArguments(fixtures[0].attached, 0).SyncV2Scope
	raw := canonicalMutationJSON(t, mustMarshalJSON(t, scope))
	seed := sha256.Sum256([]byte(first.projectID))
	proof := signedBoundProof(t, wrongFabric, "wormhole.sync.push", raw, scope.AttachmentRef, first.transport.OccurredAt, bytesOf(23, 32), seed[:])
	before := task2MutationSnapshot(t, first.db, first.projectID)
	if _, err := wrongResolver.AuthorizeMutation(context.Background(), "wormhole.sync.push", raw, scope, proof); !errors.Is(err, coregit.ErrStreamNotFound) {
		t.Fatalf("wrong-fabric authorization error = %v, want ErrStreamNotFound", err)
	}
	assertTask2MutationDelta(t, before, task2MutationSnapshot(t, first.db, first.projectID), 0)
}

func mustMarshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestPublicBoundMutationPostAuthorizationDetachKeepsBurnedNonce(t *testing.T) {
	f := newMutationFixture(t)
	attached := f.attach(24)
	authorized, coordinator, raw := authorizeFixtureMutation(t, f, attached, 25)
	if _, err := f.db.Exec(`UPDATE fabric_workspace_stream_bindings SET writable=false,detached_at=now() WHERE project_id=$1 AND attachment_ref=$2`, f.projectID, attached.Attachment.AttachmentRef); err != nil {
		t.Fatal(err)
	}
	before := task2MutationSnapshot(t, f.db, f.projectID)
	called := false
	err := coordinator.ExecutePublic(context.Background(), authorized, "sync.push", raw, func(context.Context, *sql.Tx, VerifiedMutation) error {
		called = true
		return nil
	})
	if !errors.Is(err, coregit.ErrStreamNotFound) || called {
		t.Fatalf("ExecutePublic = (called=%v, error=%v), want ErrStreamNotFound before callback", called, err)
	}
	assertTask2MutationDelta(t, before, task2MutationSnapshot(t, f.db, f.projectID), 0)
}
