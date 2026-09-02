package mcp

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	runtimesync "github.com/H4RL33/wormhole/internal/runtime/sync"
	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

type activityRegistryCaller struct {
	t             *testing.T
	registry      *Registry
	verifier      *PublicProofVerifier
	profile       types.FabricProfile
	attachmentRef string
	privateKey    ed25519.PrivateKey
	nonce         byte
	operations    []string
}

func (c *activityRegistryCaller) CallActivity(ctx context.Context, profile types.FabricProfile, credentialRef, tool string, arguments json.RawMessage) (json.RawMessage, error) {
	c.t.Helper()
	if profile != c.profile || credentialRef != c.profile.CredentialRef {
		c.t.Fatalf("caller route = (%+v,%q), want (%+v,%q)", profile, credentialRef, c.profile, c.profile.CredentialRef)
	}
	if !isCanonicalJSONObject(arguments) {
		c.t.Fatalf("%s client arguments are not the handler's canonical object: %s", tool, arguments)
	}

	publicKey := c.privateKey.Public().(ed25519.PublicKey)
	fingerprint := sha256.Sum256(publicKey)
	var nonce [32]byte
	copy(nonce[:], bytes.Repeat([]byte{c.nonce}, len(nonce)))
	c.nonce++
	scope := "attachment:" + c.attachmentRef
	message, err := projectstate.PublicProofMessage(profile.FabricInstanceID, tool, scope, arguments, c.verifier.now(), nonce)
	if err != nil {
		c.t.Fatal(err)
	}
	proof := types.PublicRequestProof{
		KeyID:     "sha256:" + hex.EncodeToString(fingerprint[:]),
		PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
		Timestamp: c.verifier.now().Format(time.RFC3339Nano),
		Nonce:     base64.RawURLEncoding.EncodeToString(nonce[:]),
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(c.privateKey, message)),
	}
	if _, err := c.verifier.VerifyBound(tool, c.attachmentRef, arguments, proof); err != nil {
		c.t.Fatalf("%s proof did not verify over exact client bytes: %v", tool, err)
	}

	params, err := json.Marshal(ToolsCallParams{Name: tool, Arguments: arguments, Proof: &proof})
	if err != nil {
		c.t.Fatal(err)
	}
	result, rpcErr := HandleToolsCall(ctx, c.registry, nil, "", params)
	if rpcErr != nil {
		c.t.Fatalf("%s JSON-RPC error: %+v", tool, rpcErr)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		c.t.Fatal(err)
	}
	c.operations = append(c.operations, tool)
	return raw, nil
}

func TestActivityPublicClientArgumentsPassPublicRegistryHandlersAndExactProofVerification(t *testing.T) {
	fixture := newActivityPullLifecycleFixture(t, 180)
	registry := NewPublicFabricRegistry(PublicFabricRegistryDependencies{
		ActivityAccept: fixture.accept, ActivityPresence: fixture.presence,
		ActivityPull: fixture.pull, ActivityLifecycle: fixture.lifecycle,
	})
	seed := sha256.Sum256([]byte(fixture.owner.projectID))
	profile := types.FabricProfile{
		ProfileID: uuid.NewString(), Alias: "public", FabricInstanceID: fixture.owner.fabricID,
		BaseURL: "https://fabric.example.test", Mode: types.FabricModePublic, CredentialRef: "keyring:public",
	}
	caller := &activityRegistryCaller{
		t: t, registry: registry, verifier: fixture.resolver.verifier, profile: profile,
		attachmentRef: fixture.attached.Attachment.AttachmentRef,
		privateKey:    ed25519.NewKeyFromSeed(seed[:]), nonce: 181,
	}
	factory, err := runtimesync.NewActivityPublicClientFactory(caller)
	if err != nil {
		t.Fatal(err)
	}
	client, err := factory.Client(context.Background(), profile, "resolved credential material")
	if err != nil {
		t.Fatal(err)
	}

	activity := activityLifecycleProjection(fixture.owner.transport, uuid.NewString(), "delivery", uuid.NewString())
	activityJSON, err := projectstate.CanonicalActivity(activity)
	if err != nil {
		t.Fatal(err)
	}
	activityDigest, err := projectstate.DigestActivity(activity)
	if err != nil {
		t.Fatal(err)
	}
	policyDigest, err := projectstate.DigestActivityPolicy(fixture.owner.policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Accept(context.Background(), runtimesync.ActivityAcceptRequest{
		AttachmentRef: fixture.attached.Attachment.AttachmentRef,
		PolicyVersion: fixture.owner.policy.PolicyVersion, PolicyDigest: policyDigest,
		ActivityJSON: activityJSON, ActivityDigest: activityDigest,
	}); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	presence := activityHandlerPresence(fixture.owner.transport, uuid.NewString())
	presenceJSON, err := projectstate.CanonicalActivity(presence)
	if err != nil {
		t.Fatal(err)
	}
	presenceDigest, err := projectstate.DigestActivity(presence)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SendPresence(context.Background(), runtimesync.ActivityPresenceRequest{
		AttachmentRef: fixture.attached.Attachment.AttachmentRef,
		PolicyVersion: fixture.owner.policy.PolicyVersion, PolicyDigest: policyDigest,
		ActivityJSON: presenceJSON, ActivityDigest: presenceDigest,
	}); err != nil {
		t.Fatalf("SendPresence: %v", err)
	}
	if _, err := client.Pull(context.Background(), runtimesync.ActivityPullRequest{
		AttachmentRef: fixture.attached.Attachment.AttachmentRef, AfterSequence: 0, Limit: 10,
	}); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if _, err := client.Lifecycle(context.Background(), runtimesync.ActivityLifecycleRequest{
		AttachmentRef: fixture.attached.Attachment.AttachmentRef, ActivityID: activity.ID,
		Change: localstore.ActivityLifecycleChange{
			Kind: string(activity.Lifecycle.Kind), ReferenceID: activity.Lifecycle.ReferenceID,
			ExpectedState: "pending", NextState: "delivered",
		},
	}); err != nil {
		t.Fatalf("Lifecycle: %v", err)
	}

	want := []string{
		"wormhole.activity.accept", "wormhole.activity.presence",
		"wormhole.activity.pull", "wormhole.activity.lifecycle",
	}
	if len(caller.operations) != len(want) {
		t.Fatalf("operations = %q, want %q", caller.operations, want)
	}
	for index := range want {
		if caller.operations[index] != want[index] {
			t.Fatalf("operations = %q, want %q", caller.operations, want)
		}
	}
}
