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
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	coregit "github.com/H4RL33/wormhole/internal/core/git"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
	"github.com/google/uuid"
)

type attachPolicySourceStub struct {
	policy       projectstate.EffectiveActivityPolicyV1
	err          error
	calls        int
	projectID    string
	repository   types.RepositoryIdentity
	canonicalRef string
}

type staticAttachPolicySource struct {
	policy projectstate.EffectiveActivityPolicyV1
}

func (s staticAttachPolicySource) InitialActivityPolicy(context.Context, string, types.RepositoryIdentity, string) (projectstate.EffectiveActivityPolicyV1, error) {
	return s.policy, nil
}

func (s *attachPolicySourceStub) InitialActivityPolicy(_ context.Context, projectID string, repository types.RepositoryIdentity, canonicalRef string) (projectstate.EffectiveActivityPolicyV1, error) {
	s.calls++
	s.projectID, s.repository, s.canonicalRef = projectID, repository, canonicalRef
	return s.policy, s.err
}

type attachCoordinatorStub struct {
	executeCalls int
	replayCalls  int
	command      InitialAttachCommand
	result       InitialAttachResult
	err          error
}

type attachObserverFunc func(context.Context, types.RepositoryIdentity, string, string) (coregit.RefObservation, projectstate.Tree, error)

func (f attachObserverFunc) ObserveRef(ctx context.Context, repository types.RepositoryIdentity, ref, credential string) (coregit.RefObservation, projectstate.Tree, error) {
	return f(ctx, repository, ref, credential)
}

func (s *attachCoordinatorStub) ExecuteInitialAttach(_ context.Context, command InitialAttachCommand) (InitialAttachResult, error) {
	s.executeCalls++
	s.command = command
	if s.err != nil {
		return InitialAttachResult{}, s.err
	}
	return s.result, nil
}

func (s *attachCoordinatorStub) ReplayInitialAttach(context.Context, InitialAttachReplayCommand) (InitialAttachResult, error) {
	s.replayCalls++
	return InitialAttachResult{}, errors.New("unexpected replay call")
}

func TestSyncV2AttachConstructorRejectsNilDependencies(t *testing.T) {
	fabricID := "11111111-1111-4111-8111-111111111111"
	verifier, err := NewPublicProofVerifier("11111111-1111-4111-8111-111111111111", func() time.Time {
		return time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	observer := coregit.CanonicalGitObserver(&coregit.FakeObserver{})
	coordinator := syncAttachCoordinator(&attachCoordinatorStub{})
	policy := SyncAttachPolicySource(&attachPolicySourceStub{})

	wrongFabricVerifier, err := NewPublicProofVerifier("11111111-1111-4111-8111-111111111112", func() time.Time {
		return time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		observer    coregit.CanonicalGitObserver
		coordinator syncAttachCoordinator
		policy      SyncAttachPolicySource
		verifier    *PublicProofVerifier
	}{
		{name: "observer", coordinator: coordinator, policy: policy, verifier: verifier},
		{name: "coordinator", observer: observer, policy: policy, verifier: verifier},
		{name: "policy", observer: observer, coordinator: coordinator, verifier: verifier},
		{name: "verifier", observer: observer, coordinator: coordinator, policy: policy},
		{name: "wrong verifier Fabric", observer: observer, coordinator: coordinator, policy: policy, verifier: wrongFabricVerifier},
		{name: "zero verifier", observer: observer, coordinator: coordinator, policy: policy, verifier: &PublicProofVerifier{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewSyncV2AttachHandler(fabricID, "git-observer", test.observer, test.coordinator, test.policy, test.verifier); !errors.Is(err, identity.ErrInvalidPublicIdentity) {
				t.Fatalf("error = %v, want ErrInvalidPublicIdentity", err)
			}
		})
	}
	for name, fixture := range map[string]struct{ fabricID, credential string }{
		"fabric id":          {fabricID: "not-a-uuid", credential: "git-observer"},
		"empty credential":   {fabricID: "11111111-1111-4111-8111-111111111111"},
		"trimmed credential": {fabricID: "11111111-1111-4111-8111-111111111111", credential: " git-observer"},
		"newline credential": {fabricID: "11111111-1111-4111-8111-111111111111", credential: "git\nobserver"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewSyncV2AttachHandler(fixture.fabricID, fixture.credential, observer, coordinator, policy, verifier); !errors.Is(err, identity.ErrInvalidPublicIdentity) {
				t.Fatalf("error = %v, want ErrInvalidPublicIdentity", err)
			}
		})
	}
}

func TestSyncV2AttachRejectsNoncanonicalAndForbiddenArgumentsBeforeDependencies(t *testing.T) {
	fabricID := "11111111-1111-4111-8111-111111111111"
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	verifier, err := NewPublicProofVerifier(fabricID, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	observer := &coregit.FakeObserver{}
	coordinator := &attachCoordinatorStub{}
	policy := &attachPolicySourceStub{}
	handler, err := NewSyncV2AttachHandler(fabricID, "git-observer", observer, coordinator, policy, verifier)
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("a", 40)
	digest := "sha256:" + strings.Repeat("b", 64)
	valid := `{"base_commit_sha":"` + commit + `","base_tree_digest":"` + digest + `","canonical_ref":"refs/heads/main","repository":{"canonical_remote":"https://github.com/wormhole/project","immutable_id":"123","provider":"github"},"version":2}`
	proof := signedAttachProof(t, fabricID, json.RawMessage(valid), now, make([]byte, 32), make([]byte, ed25519.SeedSize))
	tests := map[string]struct {
		raw      string
		wantCode string
	}{
		"unknown":         {strings.TrimSuffix(valid, `}`) + `,"extra":true}`, "invalid_request"},
		"duplicate":       {strings.Replace(valid, `"version":2`, `"version":2,"version":2`, 1), "invalid_request"},
		"missing":         {strings.Replace(valid, `,"canonical_ref":"refs/heads/main"`, ``, 1), "invalid_request"},
		"null":            {strings.Replace(valid, `"canonical_ref":"refs/heads/main"`, `"canonical_ref":null`, 1), "invalid_request"},
		"wrong kind":      {strings.Replace(valid, `"canonical_ref":"refs/heads/main"`, `"canonical_ref":2`, 1), "invalid_request"},
		"trailing":        {valid + `{}`, "invalid_request"},
		"noncanonical":    {strings.Replace(valid, `,"canonical_ref"`, `, "canonical_ref"`, 1), "invalid_request"},
		"wrong version":   {strings.Replace(valid, `"version":2`, `"version":3`, 1), "unknown_version"},
		"project route":   {strings.TrimSuffix(valid, `}`) + `,"project_id":"22222222-2222-4222-8222-222222222222"}`, "invalid_request"},
		"workspace route": {strings.TrimSuffix(valid, `}`) + `,"workspace_id":"22222222-2222-4222-8222-222222222222"}`, "invalid_request"},
		"fabric route":    {strings.TrimSuffix(valid, `}`) + `,"fabric_instance_id":"22222222-2222-4222-8222-222222222222"}`, "invalid_request"},
		"remote project":  {strings.TrimSuffix(valid, `}`) + `,"remote_project_id":"22222222-2222-4222-8222-222222222222"}`, "invalid_request"},
		"stream route":    {strings.TrimSuffix(valid, `}`) + `,"stream_id":"22222222-2222-4222-8222-222222222222"}`, "invalid_request"},
		"actor route":     {strings.TrimSuffix(valid, `}`) + `,"actor_id":"22222222-2222-4222-8222-222222222222"}`, "invalid_request"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := handler.Handle(context.Background(), json.RawMessage(test.raw), proof)
			assertAttachFailure(t, err, test.wantCode)
		})
	}
	if len(observer.Calls()) != 0 || coordinator.executeCalls != 0 || coordinator.replayCalls != 0 || policy.calls != 0 {
		t.Fatalf("dependency calls = observer %d execute %d replay %d policy %d, want zero", len(observer.Calls()), coordinator.executeCalls, coordinator.replayCalls, policy.calls)
	}
}

func TestSyncV2AttachRejectsNoncanonicalRepositoryEvidenceBeforeDependencies(t *testing.T) {
	fabricID := "11111111-1111-4111-8111-111111111111"
	verifier, err := NewPublicProofVerifier(fabricID, func() time.Time { return time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	observer := &coregit.FakeObserver{}
	coordinator := &attachCoordinatorStub{}
	policy := &attachPolicySourceStub{}
	handler, err := NewSyncV2AttachHandler(fabricID, "git-observer", observer, coordinator, policy, verifier)
	if err != nil {
		t.Fatal(err)
	}
	base := SyncAttachV2Args{
		Version:      2,
		Repository:   types.RepositoryIdentity{Provider: "github", ImmutableID: "123", CanonicalRemote: "https://github.com/wormhole/project"},
		CanonicalRef: "refs/heads/main", BaseCommitSHA: strings.Repeat("a", 40),
		BaseTreeDigest: projectstate.Digest("sha256:" + strings.Repeat("b", 64)),
	}
	tests := map[string]func(*SyncAttachV2Args){
		"local repository": func(a *SyncAttachV2Args) { a.Repository = types.RepositoryIdentity{} },
		"remote userinfo":  func(a *SyncAttachV2Args) { a.Repository.CanonicalRemote = "https://token@github.com/wormhole/project" },
		"non-head ref":     func(a *SyncAttachV2Args) { a.CanonicalRef = "main" },
		"uppercase commit": func(a *SyncAttachV2Args) { a.BaseCommitSHA = strings.Repeat("A", 40) },
		"short commit":     func(a *SyncAttachV2Args) { a.BaseCommitSHA = strings.Repeat("a", 39) },
		"uppercase digest": func(a *SyncAttachV2Args) { a.BaseTreeDigest = projectstate.Digest("sha256:" + strings.Repeat("B", 64)) },
		"short digest":     func(a *SyncAttachV2Args) { a.BaseTreeDigest = projectstate.Digest("sha256:" + strings.Repeat("b", 63)) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			arguments := base
			mutate(&arguments)
			_, err := handler.Handle(context.Background(), canonicalAttachArguments(t, arguments), types.PublicRequestProof{})
			assertAttachFailure(t, err, "invalid_request")
		})
	}
	if len(observer.Calls()) != 0 || coordinator.executeCalls != 0 || coordinator.replayCalls != 0 || policy.calls != 0 {
		t.Fatalf("dependency calls = observer %d execute %d replay %d policy %d, want zero", len(observer.Calls()), coordinator.executeCalls, coordinator.replayCalls, policy.calls)
	}
}

func TestSyncV2AttachBuildsCommandFromVerifiedObservedValues(t *testing.T) {
	f := newMutationFixture(t)
	observer := &coregit.FakeObserver{}
	observer.SetRef(f.repository, f.observation.RefName, f.observation.CommitSHA, f.tree)
	draft := f.command(1)
	coordinator := &attachCoordinatorStub{result: InitialAttachResult{
		Attachment: coregit.StreamAttachment{
			Key:         coregit.StreamKey{ProjectID: f.projectID, FabricInstanceID: f.fabricID, StreamID: "44444444-4444-4444-8444-444444444444"},
			WorkspaceID: "55555555-5555-4555-8555-555555555555", AttachmentRef: "66666666-6666-4666-8666-666666666666",
			ActivitySourceRef: "77777777-7777-4777-8777-777777777777", CanonicalRef: f.observation.RefName,
			Repository: f.repository, IssuerKeyFingerprint: f.fingerprint, Writable: true,
		},
		State: coregit.StreamTransition{Key: coregit.StreamKey{ProjectID: f.projectID, FabricInstanceID: f.fabricID, StreamID: "44444444-4444-4444-8444-444444444444"}, Version: 7}, Policy: f.policy,
	}}
	policy := &attachPolicySourceStub{policy: f.policy}
	verifier, err := NewPublicProofVerifier(f.fabricID, func() time.Time { return f.transport.OccurredAt })
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewSyncV2AttachHandler(f.fabricID, "fabric-git-credential", observer, coordinator, policy, verifier)
	if err != nil {
		t.Fatal(err)
	}
	seed := sha256.Sum256([]byte(f.projectID))
	proof := signedAttachProof(t, f.fabricID, draft.CanonicalRequest, f.transport.OccurredAt, bytesOf(1, 32), seed[:])
	got, err := handler.Handle(context.Background(), draft.CanonicalRequest, proof)
	if err != nil {
		t.Fatal(err)
	}
	want := SyncAttachV2Result{
		Version: projectstate.SyncProtocolVersionV2, AttachmentRef: coordinator.result.Attachment.AttachmentRef,
		RemoteProjectID: f.projectID, StreamID: coordinator.result.Attachment.Key.StreamID,
		StreamVersion: 7, EffectiveActivityPolicy: f.policy,
	}
	if got != want {
		t.Fatalf("result = %+v, want %+v", got, want)
	}
	if coordinator.executeCalls != 1 || coordinator.replayCalls != 0 {
		t.Fatalf("coordinator calls = execute %d replay %d", coordinator.executeCalls, coordinator.replayCalls)
	}
	command := coordinator.command
	if command.ProjectID != f.projectID || command.FabricInstanceID != f.fabricID || command.Repository != f.repository ||
		command.CanonicalRef != f.observation.RefName || command.Observation.Repository != f.repository ||
		command.Observation.RefName != f.observation.RefName || command.Observation.CommitSHA != f.observation.CommitSHA ||
		command.ObservedHuman.ID != f.actor.ID || command.TransportActor.HumanPrincipalID != f.actor.ID ||
		command.TransportActor.Assurance != types.AssurancePublicKeyContinuity || command.TransportActor.OccurredAt != f.transport.OccurredAt ||
		command.KeyFingerprint != proof.KeyID || command.Policy != f.policy || string(command.CanonicalRequest) != string(draft.CanonicalRequest) {
		t.Fatalf("command = %+v", command)
	}
	if policy.calls != 1 || policy.projectID != f.projectID || policy.repository != f.repository || policy.canonicalRef != f.observation.RefName {
		t.Fatalf("policy source = calls %d project %q repository %+v ref %q", policy.calls, policy.projectID, policy.repository, policy.canonicalRef)
	}
	calls := observer.Calls()
	if len(calls) != 1 || calls[0].Repository != f.repository || calls[0].RefName != f.observation.RefName || calls[0].ObserverCredentialRef != "fabric-git-credential" {
		t.Fatalf("observer calls = %+v", calls)
	}
}

func TestSyncV2AttachRejectsInvalidCoordinatorResultSafely(t *testing.T) {
	f := newMutationFixture(t)
	observer := &coregit.FakeObserver{}
	observer.SetRef(f.repository, f.observation.RefName, f.observation.CommitSHA, f.tree)
	coordinator := &attachCoordinatorStub{}
	policy := &attachPolicySourceStub{policy: f.policy}
	verifier, err := NewPublicProofVerifier(f.fabricID, func() time.Time { return f.transport.OccurredAt })
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewSyncV2AttachHandler(f.fabricID, "fabric-git-credential", observer, coordinator, policy, verifier)
	if err != nil {
		t.Fatal(err)
	}
	command := f.command(2)
	seed := sha256.Sum256([]byte(f.projectID))
	proof := signedAttachProof(t, f.fabricID, command.CanonicalRequest, f.transport.OccurredAt, bytesOf(2, 32), seed[:])
	_, err = handler.Handle(context.Background(), command.CanonicalRequest, proof)
	assertAttachFailure(t, err, "internal_error")
}

func TestSyncV2AttachInitialExactAndDeniedRetryNonceSemantics(t *testing.T) {
	f := newMutationFixture(t)
	handler, observer := realAttachHandler(t, f, f.tree)
	command := f.command(3)
	seed := sha256.Sum256([]byte(f.projectID))
	before := mutationCounts(t, f.db, f.projectID)
	firstProof := signedAttachProof(t, f.fabricID, command.CanonicalRequest, f.transport.OccurredAt, bytesOf(3, 32), seed[:])
	first, err := handler.Handle(context.Background(), command.CanonicalRequest, firstProof)
	if err != nil {
		t.Fatalf("first attach: %v", err)
	}
	afterFirst := mutationCounts(t, f.db, f.projectID)
	for table, want := range map[string]int{
		"fabric_streams": 1, "fabric_stream_versions": 1, "fabric_workspace_stream_bindings": 1,
		"fabric_activity_policy_versions": 1, "fabric_activity_policy_current": 1,
		"fabric_public_actor_keys": 1, "public_request_nonces": 1, "audit_log": 1,
	} {
		if afterFirst[table]-before[table] != want {
			t.Errorf("%s first delta = %d, want %d", table, afterFirst[table]-before[table], want)
		}
	}
	afterFirstSnapshot := task2MutationSnapshot(t, f.db, f.projectID)

	retryProof := signedAttachProof(t, f.fabricID, command.CanonicalRequest, f.transport.OccurredAt, bytesOf(4, 32), seed[:])
	retry, err := handler.Handle(context.Background(), command.CanonicalRequest, retryProof)
	if err != nil || retry != first {
		t.Fatalf("exact retry = %+v, %v; want %+v", retry, err, first)
	}
	afterRetrySnapshot := task2MutationSnapshot(t, f.db, f.projectID)
	assertTask2MutationDelta(t, afterFirstSnapshot, afterRetrySnapshot, 1)

	changedCommit := strings.Repeat("b", 40)
	observer.SetRef(f.repository, f.observation.RefName, changedCommit, f.tree)
	digest, err := projectstate.DigestTree(f.tree)
	if err != nil {
		t.Fatal(err)
	}
	changedRaw := canonicalAttachArguments(t, SyncAttachV2Args{
		Version: 2, Repository: f.repository, CanonicalRef: f.observation.RefName,
		BaseCommitSHA: changedCommit, BaseTreeDigest: digest,
	})
	deniedProof := signedAttachProof(t, f.fabricID, changedRaw, f.transport.OccurredAt, bytesOf(5, 32), seed[:])
	_, err = handler.Handle(context.Background(), changedRaw, deniedProof)
	assertAttachFailure(t, err, "sync_replay_conflict")
	afterDeniedSnapshot := task2MutationSnapshot(t, f.db, f.projectID)
	assertTask2MutationDelta(t, afterRetrySnapshot, afterDeniedSnapshot, 1)
	_, err = handler.Handle(context.Background(), changedRaw, deniedProof)
	assertAttachFailure(t, err, "authentication_failed")
	assertTask2MutationDelta(t, afterDeniedSnapshot, task2MutationSnapshot(t, f.db, f.projectID), 0)
}

func TestSyncV2AttachDistinctHumansReceiveDistinctWorkspaces(t *testing.T) {
	f := newMutationFixture(t)
	snapshot, err := projectstate.DecodeTree(f.tree)
	if err != nil {
		t.Fatal(err)
	}
	secondSeed := sha256.Sum256([]byte("second-human:" + f.projectID))
	secondPrivate := ed25519.NewKeyFromSeed(secondSeed[:])
	secondPublic := secondPrivate.Public().(ed25519.PublicKey)
	secondDigest := sha256.Sum256(secondPublic)
	secondID := uuid.NewString()
	second := projectstate.ActorV1{
		SchemaVersion: 1, Kind: "actor", ID: secondID, ActorKind: types.ActorHuman, DisplayName: "Second Human",
		PublicKeys: []projectstate.PublicKeyV1{{
			KeyID: "sha256:" + hex.EncodeToString(secondDigest[:]), Algorithm: "ed25519",
			PublicKeyBase64: base64.StdEncoding.EncodeToString(secondPublic),
		}}, Extensions: projectstate.ExtensionsV1{},
	}
	snapshot.Actors[secondID] = projectstate.Record[projectstate.ActorV1]{Value: &second}
	tree, err := projectstate.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := realAttachHandler(t, f, tree)
	digest, err := projectstate.DigestTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	raw := canonicalAttachArguments(t, SyncAttachV2Args{
		Version: 2, Repository: f.repository, CanonicalRef: f.observation.RefName,
		BaseCommitSHA: f.observation.CommitSHA, BaseTreeDigest: digest,
	})
	firstSeed := sha256.Sum256([]byte(f.projectID))
	firstProof := signedAttachProof(t, f.fabricID, raw, f.transport.OccurredAt, bytesOf(6, 32), firstSeed[:])
	secondProof := signedAttachProof(t, f.fabricID, raw, f.transport.OccurredAt, bytesOf(7, 32), secondSeed[:])
	first, err := handler.Handle(context.Background(), raw, firstProof)
	if err != nil {
		t.Fatal(err)
	}
	secondResult, err := handler.Handle(context.Background(), raw, secondProof)
	if err != nil {
		t.Fatal(err)
	}
	if first.AttachmentRef == secondResult.AttachmentRef || first.StreamID != secondResult.StreamID {
		t.Fatalf("attachments = first %+v second %+v", first, secondResult)
	}
	var firstWorkspace, secondWorkspace string
	if err := f.db.QueryRow(`SELECT workspace_id FROM fabric_workspace_stream_bindings WHERE project_id=$1 AND attachment_ref=$2`, f.projectID, first.AttachmentRef).Scan(&firstWorkspace); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT workspace_id FROM fabric_workspace_stream_bindings WHERE project_id=$1 AND attachment_ref=$2`, f.projectID, secondResult.AttachmentRef).Scan(&secondWorkspace); err != nil {
		t.Fatal(err)
	}
	if firstWorkspace == secondWorkspace {
		t.Fatalf("workspace = %q for both distinct humans", firstWorkspace)
	}
}

func TestSyncV2AttachConcurrent(t *testing.T) {
	f := newMutationFixture(t)
	handler, _ := realAttachHandler(t, f, f.tree)
	raw := f.command(8).CanonicalRequest
	seed := sha256.Sum256([]byte(f.projectID))
	proofs := []types.PublicRequestProof{
		signedAttachProof(t, f.fabricID, raw, f.transport.OccurredAt, bytesOf(8, 32), seed[:]),
		signedAttachProof(t, f.fabricID, raw, f.transport.OccurredAt, bytesOf(9, 32), seed[:]),
	}
	results := make([]SyncAttachV2Result, len(proofs))
	errs := make([]error, len(proofs))
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range proofs {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			<-start
			results[i], errs[i] = handler.Handle(context.Background(), raw, proofs[i])
		}(index)
	}
	close(start)
	wait.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("claim %d: %v", index, err)
		}
	}
	if results[0] != results[1] {
		t.Fatalf("concurrent results = %+v and %+v", results[0], results[1])
	}
	counts := mutationCounts(t, f.db, f.projectID)
	if counts["fabric_workspace_stream_bindings"] != 1 || counts["public_request_nonces"] != 2 || counts["audit_log"] != 1 {
		t.Fatalf("concurrent counts = %v", counts)
	}
}

func TestSyncV2AttachAuditRollbackIsRedacted(t *testing.T) {
	f := newMutationFixture(t)
	handler, _ := realAttachHandler(t, f, f.tree)
	installAuditFailure(t, f.db, f.projectID, "sync.attach")
	before := task2MutationSnapshot(t, f.db, f.projectID)
	raw := f.command(10).CanonicalRequest
	seed := sha256.Sum256([]byte(f.projectID))
	proof := signedAttachProof(t, f.fabricID, raw, f.transport.OccurredAt, bytesOf(10, 32), seed[:])
	_, err := handler.Handle(context.Background(), raw, proof)
	assertAttachFailure(t, err, "internal_error")
	after := task2MutationSnapshot(t, f.db, f.projectID)
	assertTask2MutationDelta(t, before, after, 0)
}

func TestSyncV2AttachRejectsMalformedProofBeforeObservation(t *testing.T) {
	f := newMutationFixture(t)
	handler, observer := realAttachHandler(t, f, f.tree)
	raw := f.command(11).CanonicalRequest
	seed := sha256.Sum256([]byte(f.projectID))
	valid := signedAttachProof(t, f.fabricID, raw, f.transport.OccurredAt, bytesOf(11, 32), seed[:])
	tests := map[string]func(*types.PublicRequestProof){
		"noncanonical timestamp": func(p *types.PublicRequestProof) { p.Timestamp = "2026-08-29T11:00:00+01:00" },
		"padded public key":      func(p *types.PublicRequestProof) { p.PublicKey += "=" },
		"padded nonce":           func(p *types.PublicRequestProof) { p.Nonce += "=" },
		"padded signature":       func(p *types.PublicRequestProof) { p.Signature += "=" },
		"wrong alphabet":         func(p *types.PublicRequestProof) { p.PublicKey = strings.Repeat("+", 43) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			proof := valid
			mutate(&proof)
			_, err := handler.Handle(context.Background(), raw, proof)
			assertAttachFailure(t, err, "authentication_failed")
		})
	}
	if len(observer.Calls()) != 0 {
		t.Fatalf("observer calls = %d, want zero", len(observer.Calls()))
	}
}

func TestSyncV2AttachObserverPolicyAndCoordinatorFailuresAreSafe(t *testing.T) {
	f := newMutationFixture(t)
	raw := f.command(12).CanonicalRequest
	seed := sha256.Sum256([]byte(f.projectID))
	proof := signedAttachProof(t, f.fabricID, raw, f.transport.OccurredAt, bytesOf(12, 32), seed[:])
	verifier, err := NewPublicProofVerifier(f.fabricID, func() time.Time { return f.transport.OccurredAt })
	if err != nil {
		t.Fatal(err)
	}
	validObservation := func(context.Context, types.RepositoryIdentity, string, string) (coregit.RefObservation, projectstate.Tree, error) {
		return f.observation, f.tree, nil
	}
	tests := map[string]struct {
		observer attachObserverFunc
		policy   *attachPolicySourceStub
		coord    *attachCoordinatorStub
		code     string
	}{
		"observer unavailable": {
			observer: func(context.Context, types.RepositoryIdentity, string, string) (coregit.RefObservation, projectstate.Tree, error) {
				return coregit.RefObservation{}, nil, errors.New("secret observer credential /private/path")
			}, policy: &attachPolicySourceStub{policy: f.policy}, coord: &attachCoordinatorStub{}, code: "sync_observer_unavailable",
		},
		"repository mismatch": {
			observer: func(context.Context, types.RepositoryIdentity, string, string) (coregit.RefObservation, projectstate.Tree, error) {
				observation := f.observation
				observation.Repository.ImmutableID = "999"
				return observation, f.tree, nil
			}, policy: &attachPolicySourceStub{policy: f.policy}, coord: &attachCoordinatorStub{}, code: "authentication_failed",
		},
		"ref mismatch": {
			observer: func(context.Context, types.RepositoryIdentity, string, string) (coregit.RefObservation, projectstate.Tree, error) {
				observation := f.observation
				observation.RefName = "refs/heads/other"
				return observation, f.tree, nil
			}, policy: &attachPolicySourceStub{policy: f.policy}, coord: &attachCoordinatorStub{}, code: "authentication_failed",
		},
		"commit mismatch": {
			observer: func(context.Context, types.RepositoryIdentity, string, string) (coregit.RefObservation, projectstate.Tree, error) {
				observation := f.observation
				observation.CommitSHA = strings.Repeat("b", 40)
				return observation, f.tree, nil
			}, policy: &attachPolicySourceStub{policy: f.policy}, coord: &attachCoordinatorStub{}, code: "authentication_failed",
		},
		"noncanonical observation time": {
			observer: func(context.Context, types.RepositoryIdentity, string, string) (coregit.RefObservation, projectstate.Tree, error) {
				observation := f.observation
				observation.ObservedAt = time.Time{}
				return observation, f.tree, nil
			}, policy: &attachPolicySourceStub{policy: f.policy}, coord: &attachCoordinatorStub{}, code: "authentication_failed",
		},
		"tree mismatch": {
			observer: func(context.Context, types.RepositoryIdentity, string, string) (coregit.RefObservation, projectstate.Tree, error) {
				return f.observation, append(f.tree, projectstate.File{Path: "state/v1/extra.json", Data: []byte("{}")}), nil
			}, policy: &attachPolicySourceStub{policy: f.policy}, coord: &attachCoordinatorStub{}, code: "authentication_failed",
		},
		"policy unavailable": {
			observer: validObservation, policy: &attachPolicySourceStub{err: errors.New("secret policy database")}, coord: &attachCoordinatorStub{}, code: "activity_policy_required",
		},
		"policy invalid": {
			observer: validObservation, policy: &attachPolicySourceStub{policy: projectstate.EffectiveActivityPolicyV1{}}, coord: &attachCoordinatorStub{}, code: "activity_policy_required",
		},
		"coordinator internal": {
			observer: validObservation, policy: &attachPolicySourceStub{policy: f.policy}, coord: &attachCoordinatorStub{err: errors.New("secret SQL /private/path")}, code: "internal_error",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			handler, err := NewSyncV2AttachHandler(f.fabricID, "fabric-git-credential", test.observer, test.coord, test.policy, verifier)
			if err != nil {
				t.Fatal(err)
			}
			_, err = handler.Handle(context.Background(), raw, proof)
			assertAttachFailure(t, err, test.code)
		})
	}
}

func realAttachHandler(t *testing.T, f *mutationFixture, tree projectstate.Tree) (*SyncV2AttachHandler, *coregit.FakeObserver) {
	t.Helper()
	observer := &coregit.FakeObserver{}
	observer.SetRef(f.repository, f.observation.RefName, f.observation.CommitSHA, tree)
	verifier, err := NewPublicProofVerifier(f.fabricID, func() time.Time { return f.transport.OccurredAt })
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewSyncV2AttachHandler(f.fabricID, "fabric-git-credential", observer, f.coordinator, staticAttachPolicySource{policy: f.policy}, verifier)
	if err != nil {
		t.Fatal(err)
	}
	return handler, observer
}

func canonicalAttachArguments(t *testing.T, arguments SyncAttachV2Args) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	return canonicalMutationJSON(t, raw)
}

func signedAttachProof(t *testing.T, fabricID string, arguments json.RawMessage, at time.Time, nonceSeed, keySeed []byte) types.PublicRequestProof {
	t.Helper()
	privateKey := ed25519.NewKeyFromSeed(keySeed)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	var attach SyncAttachV2Args
	if err := json.Unmarshal(arguments, &attach); err != nil {
		t.Fatal(err)
	}
	scope, err := projectstate.RepositoryScopeKey(attach.Repository, attach.CanonicalRef)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := projectstate.CanonicalJSONObject(arguments)
	if err != nil {
		t.Fatal(err)
	}
	var nonce [32]byte
	copy(nonce[:], nonceSeed)
	message, err := projectstate.PublicProofMessage(fabricID, "wormhole.sync.attach", scope, canonical, at, nonce)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256(publicKey)
	return types.PublicRequestProof{
		KeyID: "sha256:" + hex.EncodeToString(fingerprint[:]), PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
		Timestamp: at.Format(time.RFC3339Nano), Nonce: base64.RawURLEncoding.EncodeToString(nonce[:]), Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, message)),
	}
}

func bytesOf(value byte, count int) []byte {
	return bytes.Repeat([]byte{value}, count)
}

func assertAttachFailure(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want %s", code)
	}
	want := `{"code":"` + code + `","operation":"wormhole.sync.attach"}`
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

func TestSyncV2BootstrapAndPullReturnCompleteValidatedState(t *testing.T) {
	f := newMutationFixture(t)
	attached := f.attach(13)
	resolver := realBoundResolver(t, f)
	bootstrap, err := NewSyncV2BootstrapHandler(resolver, coregit.NewActivityStore(f.db))
	if err != nil {
		t.Fatal(err)
	}
	pull, err := NewSyncV2PullHandler(resolver)
	if err != nil {
		t.Fatal(err)
	}

	arguments := boundReadArguments(attached, 0)
	raw := canonicalBoundArguments(t, arguments)
	seed := sha256.Sum256([]byte(f.projectID))
	beforeBootstrap := task2MutationSnapshot(t, f.db, f.projectID)
	bootstrapResult, err := bootstrap.Handle(context.Background(), raw, signedBoundProof(t, f.fabricID, "wormhole.sync.bootstrap", raw, arguments.AttachmentRef, f.transport.OccurredAt, bytesOf(14, 32), seed[:]))
	if err != nil {
		t.Fatal(err)
	}
	if bootstrapResult.Version != 2 || bootstrapResult.Changed || bootstrapResult.EffectiveActivityPolicy != f.policy {
		t.Fatalf("bootstrap result = %+v", bootstrapResult)
	}
	assertSyncReadState(t, bootstrapResult.State, attached.State, f.tree)
	afterBootstrap := task2MutationSnapshot(t, f.db, f.projectID)
	assertTask2MutationDelta(t, beforeBootstrap, afterBootstrap, 1)

	beforePull := afterBootstrap
	pullResult, err := pull.Handle(context.Background(), raw, signedBoundProof(t, f.fabricID, "wormhole.sync.pull", raw, arguments.AttachmentRef, f.transport.OccurredAt, bytesOf(15, 32), seed[:]))
	if err != nil {
		t.Fatal(err)
	}
	if pullResult.Version != 2 || pullResult.Changed {
		t.Fatalf("pull result = %+v", pullResult)
	}
	assertSyncReadState(t, pullResult.State, attached.State, f.tree)
	assertTask2MutationDelta(t, beforePull, task2MutationSnapshot(t, f.db, f.projectID), 1)
}

func TestSyncV2PullCorruptStreamRollsBackNonceForCorrectedRetry(t *testing.T) {
	f := newMutationFixture(t)
	attached := f.attach(61)
	handler, err := NewSyncV2PullHandler(realBoundResolver(t, f))
	if err != nil {
		t.Fatal(err)
	}
	arguments := boundReadArguments(attached, 0)
	raw := canonicalBoundArguments(t, arguments)
	seed := sha256.Sum256([]byte(f.projectID))
	proof := signedBoundProof(t, f.fabricID, "wormhole.sync.pull", raw, arguments.AttachmentRef, f.transport.OccurredAt, bytesOf(62, 32), seed[:])

	corruptTask2CurrentStream(t, f.db, attached.Attachment, projectstate.Digest("sha256:"+strings.Repeat("f", 64)))
	beforeFailure := task2MutationSnapshot(t, f.db, f.projectID)
	_, err = handler.Handle(context.Background(), raw, proof)
	assertSyncReadFailure(t, err, "wormhole.sync.pull", "internal_error")
	assertTask2MutationDelta(t, beforeFailure, task2MutationSnapshot(t, f.db, f.projectID), 0)

	corruptTask2CurrentStream(t, f.db, attached.Attachment, attached.State.Live.Digest)
	beforeRetry := task2MutationSnapshot(t, f.db, f.projectID)
	result, err := handler.Handle(context.Background(), raw, proof)
	if err != nil {
		t.Fatalf("corrected corrupt-stream retry with same nonce: %v", err)
	}
	assertSyncReadState(t, result.State, attached.State, f.tree)
	assertTask2MutationDelta(t, beforeRetry, task2MutationSnapshot(t, f.db, f.projectID), 1)
}

func TestSyncV2BootstrapPolicyFailureRollsBackNonceForCorrectedRetry(t *testing.T) {
	f := newMutationFixture(t)
	attached := f.attach(16)
	resolver := realBoundResolver(t, f)
	handler, err := NewSyncV2BootstrapHandler(resolver, coregit.NewActivityStore(f.db))
	if err != nil {
		t.Fatal(err)
	}
	arguments := boundReadArguments(attached, 0)
	raw := canonicalBoundArguments(t, arguments)
	seed := sha256.Sum256([]byte(f.projectID))
	proof := signedBoundProof(t, f.fabricID, "wormhole.sync.bootstrap", raw, arguments.AttachmentRef, f.transport.OccurredAt, bytesOf(17, 32), seed[:])
	if _, err := f.db.Exec(`DELETE FROM fabric_activity_policy_current WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3`, f.projectID, f.fabricID, attached.Attachment.Key.StreamID); err != nil {
		t.Fatal(err)
	}
	before := task2MutationSnapshot(t, f.db, f.projectID)
	_, err = handler.Handle(context.Background(), raw, proof)
	assertSyncReadFailure(t, err, "wormhole.sync.bootstrap", "activity_policy_required")
	assertTask2MutationDelta(t, before, task2MutationSnapshot(t, f.db, f.projectID), 0)
	if _, err := f.db.Exec(`INSERT INTO fabric_activity_policy_current(project_id,fabric_instance_id,stream_id,canonical_ref,policy_version) VALUES($1,$2,$3,$4,$5)`, f.projectID, f.fabricID, attached.Attachment.Key.StreamID, attached.Attachment.CanonicalRef, f.policy.PolicyVersion); err != nil {
		t.Fatal(err)
	}
	beforeRetry := task2MutationSnapshot(t, f.db, f.projectID)
	if _, err := handler.Handle(context.Background(), raw, proof); err != nil {
		t.Fatalf("corrected retry with same nonce: %v", err)
	}
	assertTask2MutationDelta(t, beforeRetry, task2MutationSnapshot(t, f.db, f.projectID), 1)
}

func TestSyncV2BootstrapRejectsMalformedUnknownAndUnboundedPolicies(t *testing.T) {
	for name, corrupt := range map[string]func([]byte) []byte{
		"malformed": func([]byte) []byte { return []byte(`{`) },
		"unknown": func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1)
		},
		"unbounded": func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"terminal_retention_seconds":2592000`), []byte(`"terminal_retention_seconds":31536001`), 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newMutationFixture(t)
			attached := f.attach(byte(50 + len(name)))
			handler, err := NewSyncV2BootstrapHandler(realBoundResolver(t, f), coregit.NewActivityStore(f.db))
			if err != nil {
				t.Fatal(err)
			}
			canonical, err := projectstate.CanonicalActivityPolicy(f.policy)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := f.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(`SET LOCAL session_replication_role=replica`); err == nil {
				_, err = tx.Exec(`UPDATE fabric_activity_policy_versions SET canonical_policy_json=$1 WHERE project_id=$2 AND fabric_instance_id=$3 AND stream_id=$4`, corrupt(canonical), f.projectID, f.fabricID, attached.Attachment.Key.StreamID)
			}
			if err == nil {
				err = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
			if err != nil {
				t.Fatal(err)
			}
			arguments := boundReadArguments(attached, 0)
			raw := canonicalBoundArguments(t, arguments)
			seed := sha256.Sum256([]byte(f.projectID))
			before := task2MutationSnapshot(t, f.db, f.projectID)
			_, err = handler.Handle(context.Background(), raw, signedBoundProof(t, f.fabricID, "wormhole.sync.bootstrap", raw, arguments.AttachmentRef, f.transport.OccurredAt, bytesOf(60, 32), seed[:]))
			assertSyncReadFailure(t, err, "wormhole.sync.bootstrap", "activity_policy_required")
			assertTask2MutationDelta(t, before, task2MutationSnapshot(t, f.db, f.projectID), 0)
		})
	}
}

func TestPublicProofNonceReplayConcurrentPullConsumesOnce(t *testing.T) {
	f := newMutationFixture(t)
	attached := f.attach(18)
	handler, err := NewSyncV2PullHandler(realBoundResolver(t, f))
	if err != nil {
		t.Fatal(err)
	}
	arguments := boundReadArguments(attached, 0)
	raw := canonicalBoundArguments(t, arguments)
	seed := sha256.Sum256([]byte(f.projectID))
	proof := signedBoundProof(t, f.fabricID, "wormhole.sync.pull", raw, arguments.AttachmentRef, f.transport.OccurredAt, bytesOf(19, 32), seed[:])
	before := task2MutationSnapshot(t, f.db, f.projectID)
	start := make(chan struct{})
	errs := make([]error, 2)
	var wait sync.WaitGroup
	for i := range errs {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			_, errs[index] = handler.Handle(context.Background(), raw, proof)
		}(i)
	}
	close(start)
	wait.Wait()
	successes, failures := 0, 0
	for _, err := range errs {
		if err == nil {
			successes++
		} else if err.Error() == `{"code":"authentication_failed","operation":"wormhole.sync.pull"}` {
			failures++
		} else {
			t.Fatalf("unexpected concurrent error: %v", err)
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("concurrent outcomes = %d success, %d authentication failure: %v", successes, failures, errs)
	}
	assertTask2MutationDelta(t, before, task2MutationSnapshot(t, f.db, f.projectID), 1)
}

func TestSyncV2PullChangedStateAndCursorRollback(t *testing.T) {
	f := newMutationFixture(t)
	attached := f.attach(20)
	streams := coregit.NewStreamStore(f.db)
	tx, err := identity.NewStore(f.db).BeginProjectTx(context.Background(), f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	advanced, err := streams.AdvanceAcceptedObservedRefInTx(context.Background(), tx, types.ActorScope{ProjectID: f.projectID, Actor: f.transport}, coregit.AdvanceAcceptedInput{
		Key:  attached.Attachment.Key,
		Ref:  coregit.RefObservation{Repository: f.repository, RefName: f.observation.RefName, CommitSHA: strings.Repeat("b", 40), ObservedAt: f.observation.ObservedAt.Add(time.Minute)},
		Tree: f.tree, ExpectedVersion: attached.State.Version,
		ExpectedAcceptedCommitSHA: attached.State.AcceptedCommitSHA, ExpectedAcceptedTreeDigest: attached.State.Accepted.Digest,
		ExpectedLiveTreeDigest: attached.State.Live.Digest,
	})
	if err == nil {
		err = tx.Commit()
	} else {
		_ = tx.Rollback()
	}
	if err != nil {
		t.Fatal(err)
	}
	attached.State = advanced
	handler, err := NewSyncV2PullHandler(realBoundResolver(t, f))
	if err != nil {
		t.Fatal(err)
	}
	seed := sha256.Sum256([]byte(f.projectID))
	nonce := bytesOf(21, 32)

	wrong := boundReadArguments(attached, advanced.Version+1)
	wrongRaw := canonicalBoundArguments(t, wrong)
	beforeWrong := task2MutationSnapshot(t, f.db, f.projectID)
	_, err = handler.Handle(context.Background(), wrongRaw, signedBoundProof(t, f.fabricID, "wormhole.sync.pull", wrongRaw, wrong.AttachmentRef, f.transport.OccurredAt, nonce, seed[:]))
	assertSyncReadFailure(t, err, "wormhole.sync.pull", "sync_precondition_failed")
	afterWrong := task2MutationSnapshot(t, f.db, f.projectID)
	assertTask2MutationDelta(t, beforeWrong, afterWrong, 0)

	corrected := boundReadArguments(attached, 0)
	correctedRaw := canonicalBoundArguments(t, corrected)
	before := afterWrong
	result, err := handler.Handle(context.Background(), correctedRaw, signedBoundProof(t, f.fabricID, "wormhole.sync.pull", correctedRaw, corrected.AttachmentRef, f.transport.OccurredAt, nonce, seed[:]))
	if err != nil {
		t.Fatalf("corrected cursor reused rolled-back nonce: %v", err)
	}
	if !result.Changed || result.State.StreamVersion != advanced.Version || result.State.AcceptedCommitSHA != advanced.AcceptedCommitSHA {
		t.Fatalf("changed pull = %+v, want version %d", result, advanced.Version)
	}
	assertTask2MutationDelta(t, before, task2MutationSnapshot(t, f.db, f.projectID), 1)
}

func TestSyncV2PullRejectsEveryMismatchedSignedScopeAndWrongIssuer(t *testing.T) {
	f := newMutationFixture(t)
	attached := f.attach(22)
	handler, err := NewSyncV2PullHandler(realBoundResolver(t, f))
	if err != nil {
		t.Fatal(err)
	}
	base := boundReadArguments(attached, 0)
	seed := sha256.Sum256([]byte(f.projectID))
	mutations := map[string]func(*SyncPullV2Args){
		"repository": func(a *SyncPullV2Args) { a.Repository.ImmutableID = "987654321" },
		"ref":        func(a *SyncPullV2Args) { a.CanonicalRef = "refs/heads/other" },
		"commit":     func(a *SyncPullV2Args) { a.BaseCommitSHA = strings.Repeat("b", 40) },
		"base tree":  func(a *SyncPullV2Args) { a.BaseTreeDigest = projectstate.Digest("sha256:" + strings.Repeat("b", 64)) },
		"version":    func(a *SyncPullV2Args) { a.ExpectedStreamVersion++ },
		"live tree": func(a *SyncPullV2Args) {
			a.ExpectedLiveTreeDigest = projectstate.Digest("sha256:" + strings.Repeat("c", 64))
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			arguments := base
			mutate(&arguments)
			raw := canonicalBoundArguments(t, arguments)
			proof := signedBoundProof(t, f.fabricID, "wormhole.sync.pull", raw, arguments.AttachmentRef, f.transport.OccurredAt, bytesOf(byte(30+len(name)), 32), seed[:])
			before := task2MutationSnapshot(t, f.db, f.projectID)
			_, err := handler.Handle(context.Background(), raw, proof)
			assertSyncReadFailure(t, err, "wormhole.sync.pull", "sync_precondition_failed")
			assertTask2MutationDelta(t, before, task2MutationSnapshot(t, f.db, f.projectID), 0)
		})
	}

	raw := canonicalBoundArguments(t, base)
	wrongSeed := sha256.Sum256([]byte("wrong-issuer"))
	beforeWrongIssuer := task2MutationSnapshot(t, f.db, f.projectID)
	_, err = handler.Handle(context.Background(), raw, signedBoundProof(t, f.fabricID, "wormhole.sync.pull", raw, base.AttachmentRef, f.transport.OccurredAt, bytesOf(40, 32), wrongSeed[:]))
	assertSyncReadFailure(t, err, "wormhole.sync.pull", "authentication_failed")
	assertTask2MutationDelta(t, beforeWrongIssuer, task2MutationSnapshot(t, f.db, f.projectID), 0)
	unknown := base
	unknown.AttachmentRef = uuid.NewString()
	unknownRaw := canonicalBoundArguments(t, unknown)
	beforeUnknown := task2MutationSnapshot(t, f.db, f.projectID)
	_, err = handler.Handle(context.Background(), unknownRaw, signedBoundProof(t, f.fabricID, "wormhole.sync.pull", unknownRaw, unknown.AttachmentRef, f.transport.OccurredAt, bytesOf(41, 32), seed[:]))
	assertSyncReadFailure(t, err, "wormhole.sync.pull", "attachment_not_found")
	assertTask2MutationDelta(t, beforeUnknown, task2MutationSnapshot(t, f.db, f.projectID), 0)
}

func TestSyncV2PullRejectsInvalidArgumentsBeforeBoundResolution(t *testing.T) {
	f := newMutationFixture(t)
	attached := f.attach(23)
	handler, err := NewSyncV2PullHandler(realBoundResolver(t, f))
	if err != nil {
		t.Fatal(err)
	}
	valid := string(canonicalBoundArguments(t, boundReadArguments(attached, 0)))
	tests := map[string]struct {
		raw, code string
	}{
		"unknown":        {strings.TrimSuffix(valid, `}`) + `,"project_id":"` + f.projectID + `"}`, "invalid_request"},
		"duplicate":      {strings.Replace(valid, `"version":2`, `"version":2,"version":2`, 1), "invalid_request"},
		"noncanonical":   {strings.Replace(valid, `{`, `{ `, 1), "invalid_request"},
		"wrong version":  {strings.Replace(valid, `"version":2`, `"version":3`, 1), "unknown_version"},
		"negative after": {strings.Replace(valid, `"after_version":0`, `"after_version":-1`, 1), "invalid_request"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			before := task2MutationSnapshot(t, f.db, f.projectID)
			_, err := handler.Handle(context.Background(), json.RawMessage(test.raw), types.PublicRequestProof{})
			assertSyncReadFailure(t, err, "wormhole.sync.pull", test.code)
			assertTask2MutationDelta(t, before, task2MutationSnapshot(t, f.db, f.projectID), 0)
		})
	}
}

func TestSyncV2PullForcedRLSCrossProjectResolution(t *testing.T) {
	first := newMutationFixture(t)
	second := newMutationFixture(t)
	oldFabricID := second.fabricID
	if _, err := second.db.Exec(`UPDATE project_repository_bindings SET fabric_instance_id=$1 WHERE project_id=$2 AND fabric_instance_id=$3`, first.fabricID, second.projectID, oldFabricID); err != nil {
		t.Fatal(err)
	}
	second.fabricID = first.fabricID
	firstAttached := first.attach(42)
	secondAttached := second.attach(43)

	runtimeDB := publicRuntimeDB(t)
	verifier, err := NewPublicProofVerifier(first.fabricID, func() time.Time { return first.transport.OccurredAt })
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewPublicBoundProofResolver(first.fabricID, identity.NewStore(runtimeDB), coregit.NewStreamStore(runtimeDB), verifier)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewSyncV2PullHandler(resolver)
	if err != nil {
		t.Fatal(err)
	}

	for index, fixture := range []struct {
		owner    *mutationFixture
		attached InitialAttachResult
	}{{first, firstAttached}, {second, secondAttached}} {
		arguments := boundReadArguments(fixture.attached, 0)
		raw := canonicalBoundArguments(t, arguments)
		seed := sha256.Sum256([]byte(fixture.owner.projectID))
		beforeFirst := task2MutationSnapshot(t, first.db, first.projectID)
		beforeSecond := task2MutationSnapshot(t, second.db, second.projectID)
		result, err := handler.Handle(context.Background(), raw, signedBoundProof(t, first.fabricID, "wormhole.sync.pull", raw, arguments.AttachmentRef, fixture.owner.transport.OccurredAt, bytesOf(byte(44+index), 32), seed[:]))
		if err != nil || result.State.StreamVersion != fixture.attached.State.Version || result.State.LiveTreeDigest != fixture.attached.State.Live.Digest {
			t.Fatalf("project %d forced-RLS pull = (%+v, %v)", index, result, err)
		}
		firstNonceDelta, secondNonceDelta := 0, 0
		if fixture.owner == first {
			firstNonceDelta = 1
		} else {
			secondNonceDelta = 1
		}
		assertTask2MutationDelta(t, beforeFirst, task2MutationSnapshot(t, first.db, first.projectID), firstNonceDelta)
		assertTask2MutationDelta(t, beforeSecond, task2MutationSnapshot(t, second.db, second.projectID), secondNonceDelta)
	}

	spoofed := boundReadArguments(secondAttached, 0)
	spoofed.Repository = first.repository
	spoofedRaw := canonicalBoundArguments(t, spoofed)
	secondSeed := sha256.Sum256([]byte(second.projectID))
	beforeFirst := task2MutationSnapshot(t, first.db, first.projectID)
	beforeSecond := task2MutationSnapshot(t, second.db, second.projectID)
	_, err = handler.Handle(context.Background(), spoofedRaw, signedBoundProof(t, first.fabricID, "wormhole.sync.pull", spoofedRaw, spoofed.AttachmentRef, second.transport.OccurredAt, bytesOf(46, 32), secondSeed[:]))
	assertSyncReadFailure(t, err, "wormhole.sync.pull", "sync_precondition_failed")
	assertTask2MutationDelta(t, beforeFirst, task2MutationSnapshot(t, first.db, first.projectID), 0)
	assertTask2MutationDelta(t, beforeSecond, task2MutationSnapshot(t, second.db, second.projectID), 0)
}

func TestSyncV2PullResolvesFreshAgentSessionScope(t *testing.T) {
	f := newMutationFixture(t)
	snapshot, err := projectstate.DecodeTree(f.tree)
	if err != nil {
		t.Fatal(err)
	}
	agentID := uuid.NewString()
	agent := projectstate.ActorV1{
		SchemaVersion: 1, Kind: "actor", ID: agentID, ActorKind: types.ActorAgent,
		DisplayName: "Read Agent", PublicKeys: []projectstate.PublicKeyV1{}, Extensions: projectstate.ExtensionsV1{},
	}
	snapshot.Actors[agentID] = projectstate.Record[projectstate.ActorV1]{Value: &agent}
	f.tree, err = projectstate.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	attached := f.attach(47)
	tx, err := identity.NewStore(f.db).BeginProjectTx(context.Background(), f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	session, err := f.coordinator.identity.IssuePublicAgentSessionInTx(context.Background(), tx, identity.PublicAgentSessionIssue{
		ProjectID: f.projectID, FabricInstanceID: f.fabricID, StreamID: attached.Attachment.Key.StreamID,
		WorkspaceID: attached.Attachment.WorkspaceID, CanonicalRef: attached.Attachment.CanonicalRef,
		AttachmentRef: attached.Attachment.AttachmentRef, IssuerKeyFingerprint: f.fingerprint,
		AgentID: agentID, HarnessName: "codex", HarnessVersion: "1", ModelName: "gpt", ModelVersion: "5",
		SourceVersion: attached.Attachment.SourceVersion, IssuedAt: f.transport.OccurredAt,
	})
	if err == nil {
		err = tx.Commit()
	} else {
		_ = tx.Rollback()
	}
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewSyncV2PullHandler(realBoundResolver(t, f))
	if err != nil {
		t.Fatal(err)
	}
	arguments := boundReadArguments(attached, 0)
	raw := canonicalBoundArguments(t, arguments)
	seed := sha256.Sum256([]byte(f.projectID))
	proof := signedBoundSessionProof(t, f.fabricID, "wormhole.sync.pull", raw, arguments.AttachmentRef, session.SessionID, f.transport.OccurredAt, bytesOf(48, 32), seed[:])
	beforeSuccess := task2MutationSnapshot(t, f.db, f.projectID)
	result, err := handler.Handle(context.Background(), raw, proof)
	if err != nil || result.State.StreamVersion != attached.State.Version {
		t.Fatalf("agent-session pull = (%+v, %v)", result, err)
	}
	assertTask2MutationDelta(t, beforeSuccess, task2MutationSnapshot(t, f.db, f.projectID), 1)
	if _, err := f.db.Exec(`UPDATE fabric_public_agent_sessions SET revoked_at=now() WHERE project_id=$1 AND session_id=$2`, f.projectID, session.SessionID); err != nil {
		t.Fatal(err)
	}
	proof = signedBoundSessionProof(t, f.fabricID, "wormhole.sync.pull", raw, arguments.AttachmentRef, session.SessionID, f.transport.OccurredAt, bytesOf(49, 32), seed[:])
	beforeRevoked := task2MutationSnapshot(t, f.db, f.projectID)
	_, err = handler.Handle(context.Background(), raw, proof)
	assertSyncReadFailure(t, err, "wormhole.sync.pull", "authentication_failed")
	assertTask2MutationDelta(t, beforeRevoked, task2MutationSnapshot(t, f.db, f.projectID), 0)
}

func publicRuntimeDB(t *testing.T) *sql.DB {
	t.Helper()
	databaseURL, err := url.Parse(types.LoadConfig().DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := databaseURL.Query()
	query.Set("options", "-c role=wormhole_fabric_runtime")
	databaseURL.RawQuery = query.Encode()
	db, err := sql.Open("postgres", databaseURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var currentUser string
	if err := db.QueryRow(`SELECT current_user`).Scan(&currentUser); err != nil || currentUser != "wormhole_fabric_runtime" {
		t.Fatalf("runtime current_user = (%q, %v)", currentUser, err)
	}
	return db
}

func realBoundResolver(t *testing.T, f *mutationFixture) *PublicBoundProofResolver {
	t.Helper()
	verifier, err := NewPublicProofVerifier(f.fabricID, func() time.Time { return f.transport.OccurredAt })
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewPublicBoundProofResolver(f.fabricID, identity.NewStore(f.db), coregit.NewStreamStore(f.db), verifier)
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func boundReadArguments(attached InitialAttachResult, afterVersion int64) SyncPullV2Args {
	return SyncPullV2Args{SyncV2Scope: SyncV2Scope{
		Version: 2, AttachmentRef: attached.Attachment.AttachmentRef,
		Repository: attached.Attachment.Repository, CanonicalRef: attached.Attachment.CanonicalRef,
		BaseCommitSHA: attached.State.AcceptedCommitSHA, BaseTreeDigest: attached.State.Accepted.Digest,
		ExpectedStreamVersion: attached.State.Version, ExpectedLiveTreeDigest: attached.State.Live.Digest,
	}, AfterVersion: afterVersion}
}

func canonicalBoundArguments(t *testing.T, arguments SyncPullV2Args) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	return canonicalMutationJSON(t, raw)
}

func signedBoundProof(t *testing.T, fabricID, tool string, arguments json.RawMessage, attachment string, at time.Time, nonceSeed, keySeed []byte) types.PublicRequestProof {
	return signedBoundSessionProof(t, fabricID, tool, arguments, attachment, "", at, nonceSeed, keySeed)
}

func signedBoundSessionProof(t *testing.T, fabricID, tool string, arguments json.RawMessage, attachment, sessionID string, at time.Time, nonceSeed, keySeed []byte) types.PublicRequestProof {
	t.Helper()
	privateKey := ed25519.NewKeyFromSeed(keySeed)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	canonical, err := projectstate.CanonicalJSONObject(arguments)
	if err != nil {
		t.Fatal(err)
	}
	var nonce [32]byte
	copy(nonce[:], nonceSeed)
	scope := "attachment:" + attachment
	if sessionID != "" {
		scope += ":session:" + sessionID
	}
	message, err := projectstate.PublicProofMessage(fabricID, tool, scope, canonical, at, nonce)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256(publicKey)
	return types.PublicRequestProof{
		KeyID: "sha256:" + hex.EncodeToString(fingerprint[:]), PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
		Timestamp: at.Format(time.RFC3339Nano), Nonce: base64.RawURLEncoding.EncodeToString(nonce[:]), Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, message)),
		SessionID: sessionID,
	}
}

func assertSyncReadState(t *testing.T, got SyncStateV2, want coregit.StreamTransition, wantTree projectstate.Tree) {
	t.Helper()
	if got.StreamVersion != want.Version || got.AcceptedCommitSHA != want.AcceptedCommitSHA || got.AcceptedTreeDigest != want.Accepted.Digest || got.LiveTreeDigest != want.Live.Digest || len(got.OpenConflictIDs) != 0 || !reflect.DeepEqual(got.AcceptedTree, wantTree) || !reflect.DeepEqual(got.LiveTree, wantTree) {
		t.Fatalf("sync state = %+v, want transition %+v tree %#v", got, want, wantTree)
	}
}

func assertSyncReadFailure(t *testing.T, err error, operation, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want %s", code)
	}
	want := `{"code":"` + code + `","operation":"` + operation + `"}`
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

type task2MutationState map[string][]string

var task2MutationTables = []string{
	"project_repository_bindings",
	"fabric_streams",
	"fabric_stream_versions",
	"fabric_workspace_stream_bindings",
	"fabric_stream_requests",
	"fabric_stream_conflicts",
	"fabric_activity_policy_versions",
	"fabric_activity_policy_current",
	"fabric_activity_stream_sequences",
	"fabric_activities",
	"fabric_activity_ingress_receipts",
	"fabric_activity_lifecycle",
	"fabric_public_actor_keys",
	"fabric_public_agent_sessions",
	"public_request_nonces",
	"audit_log",
}

func task2MutationSnapshot(t *testing.T, db *sql.DB, projectID string) task2MutationState {
	t.Helper()
	state := make(task2MutationState, len(task2MutationTables))
	for _, table := range task2MutationTables {
		rows, err := db.Query(`SELECT to_jsonb(snapshot_row)::text FROM `+table+` snapshot_row WHERE project_id=$1 ORDER BY to_jsonb(snapshot_row)::text`, projectID)
		if err != nil {
			t.Fatalf("snapshot %s: %v", table, err)
		}
		for rows.Next() {
			var row string
			if err := rows.Scan(&row); err != nil {
				rows.Close()
				t.Fatalf("snapshot %s row: %v", table, err)
			}
			state[table] = append(state[table], row)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("snapshot %s close: %v", table, err)
		}
	}
	return state
}

func assertTask2MutationDelta(t *testing.T, before, after task2MutationState, nonceDelta int) {
	t.Helper()
	for _, table := range task2MutationTables {
		if table != "public_request_nonces" || nonceDelta == 0 {
			if !reflect.DeepEqual(after[table], before[table]) {
				t.Errorf("%s changed: before=%v after=%v", table, before[table], after[table])
			}
			continue
		}
		if len(after[table]) != len(before[table])+nonceDelta {
			t.Errorf("%s row delta = %d, want %d", table, len(after[table])-len(before[table]), nonceDelta)
			continue
		}
		remaining := make(map[string]int, len(after[table]))
		for _, row := range after[table] {
			remaining[row]++
		}
		for _, row := range before[table] {
			remaining[row]--
		}
		added := 0
		for _, count := range remaining {
			if count < 0 {
				t.Errorf("%s removed or changed an existing row", table)
			}
			added += count
		}
		if added != nonceDelta {
			t.Errorf("%s added row count = %d, want %d", table, added, nonceDelta)
		}
	}
}

func assertTask2ExactRowDeltas(t *testing.T, before, after task2MutationState, want map[string]int) {
	t.Helper()
	assertTask2ExactRowChanges(t, before, after, want, nil)
}

func assertTask2ExactRowChanges(t *testing.T, before, after task2MutationState, want, replacements map[string]int) {
	t.Helper()
	for _, table := range task2MutationTables {
		delta := want[table]
		if len(after[table]) != len(before[table])+delta {
			t.Errorf("%s row delta = %d, want %d", table, len(after[table])-len(before[table]), delta)
			continue
		}
		remaining := make(map[string]int, len(after[table]))
		for _, row := range after[table] {
			remaining[row]++
		}
		for _, row := range before[table] {
			remaining[row]--
		}
		added, removed := 0, 0
		for _, count := range remaining {
			if count < 0 {
				removed -= count
			} else {
				added += count
			}
		}
		wantReplacements := replacements[table]
		if added != delta+wantReplacements || removed != wantReplacements {
			t.Errorf("%s row changes = (added=%d,removed=%d), want (added=%d,removed=%d)", table, added, removed, delta+wantReplacements, wantReplacements)
		}
	}
}

func corruptTask2CurrentStream(t *testing.T, db *sql.DB, attachment coregit.StreamAttachment, liveDigest projectstate.Digest) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	result, err := tx.Exec(`UPDATE fabric_streams SET live_tree_digest=$1 WHERE project_id=$2 AND fabric_instance_id=$3 AND stream_id=$4 AND canonical_ref=$5`,
		string(liveDigest), attachment.Key.ProjectID, attachment.Key.FabricInstanceID, attachment.Key.StreamID, attachment.CanonicalRef)
	if err != nil {
		t.Fatalf("corrupt current stream: %v", err)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		t.Fatalf("corrupt current stream rows = (%d, %v), want 1", rows, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

type syncV2PushFixture struct {
	owner       *mutationFixture
	attached    InitialAttachResult
	handler     *SyncV2PushHandler
	resolver    *PublicBoundProofResolver
	coordinator *MutationCoordinator
	streams     *coregit.StreamStore
}

func newSyncV2PushFixture(t *testing.T, attachNonce byte) *syncV2PushFixture {
	t.Helper()
	owner := newMutationFixture(t)
	attached := owner.attach(attachNonce)
	return newSyncV2PushFixtureForAttached(t, owner, attached)
}

func newSyncV2PushFixtureForAttached(t *testing.T, owner *mutationFixture, attached InitialAttachResult) *syncV2PushFixture {
	t.Helper()
	runtimeDB := publicRuntimeDB(t)
	streams := coregit.NewStreamStore(runtimeDB)
	resolver := realBoundResolverForDB(t, owner, runtimeDB)
	coordinator, err := NewMutationCoordinator(identity.NewStore(runtimeDB), streams, coregit.NewActivityStore(runtimeDB))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewSyncV2PushHandler(resolver, coordinator, streams)
	if err != nil {
		t.Fatal(err)
	}
	return &syncV2PushFixture{owner: owner, attached: attached, handler: handler, resolver: resolver, coordinator: coordinator, streams: streams}
}

func newSyncV2PushAgentFixture(t *testing.T, attachNonce byte, durableAuditAgent bool) (*syncV2PushFixture, identity.PublicAgentSession, string) {
	t.Helper()
	owner := newMutationFixture(t)
	snapshot, err := projectstate.DecodeTree(owner.tree)
	if err != nil {
		t.Fatal(err)
	}
	agentID := uuid.NewString()
	agent := projectstate.ActorV1{
		SchemaVersion: 1, Kind: "actor", ID: agentID, ActorKind: types.ActorAgent,
		DisplayName: "Push Session Agent", PublicKeys: []projectstate.PublicKeyV1{}, Extensions: projectstate.ExtensionsV1{},
	}
	snapshot.Actors[agentID] = projectstate.Record[projectstate.ActorV1]{Value: &agent}
	owner.tree, err = projectstate.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if durableAuditAgent {
		if _, err := owner.db.Exec(`INSERT INTO agents(id,owner,model) VALUES($1,'sync-v2-push-test','gpt')`, agentID); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := cleanupSyncV2PushAgent(owner.db, owner.projectID, agentID); err != nil {
				t.Errorf("cleanup sync-v2 push agent %s: %v", agentID, err)
			}
		})
	}
	attached := owner.attach(attachNonce)
	tx, err := owner.coordinator.identity.BeginProjectTx(context.Background(), owner.projectID)
	if err != nil {
		t.Fatal(err)
	}
	session, err := owner.coordinator.identity.IssuePublicAgentSessionInTx(context.Background(), tx, identity.PublicAgentSessionIssue{
		ProjectID: owner.projectID, FabricInstanceID: owner.fabricID, StreamID: attached.Attachment.Key.StreamID,
		WorkspaceID: attached.Attachment.WorkspaceID, CanonicalRef: attached.Attachment.CanonicalRef,
		AttachmentRef: attached.Attachment.AttachmentRef, IssuerKeyFingerprint: owner.fingerprint,
		AgentID: agentID, HarnessName: "codex", HarnessVersion: "1", ModelName: "gpt", ModelVersion: "5",
		SourceVersion: attached.Attachment.SourceVersion, IssuedAt: owner.transport.OccurredAt,
	})
	if err == nil {
		err = tx.Commit()
	} else {
		_ = tx.Rollback()
	}
	if err != nil {
		t.Fatal(err)
	}
	if !durableAuditAgent {
		t.Cleanup(func() {
			if err := cleanupSyncV2PushAgentSession(owner.db, owner.projectID, agentID, session.SessionID); err != nil {
				t.Errorf("cleanup sync-v2 push agent session %s: %v", session.SessionID, err)
			}
		})
	}
	return newSyncV2PushFixtureForAttached(t, owner, attached), session, agentID
}

func cleanupSyncV2PushAgentSession(db *sql.DB, projectID, agentID, sessionID string) (err error) {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() {
		if err == nil {
			return
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rollback agent session cleanup: %w", rollbackErr))
		}
	}()
	var version, immutableTriggers int
	var dirty bool
	if err = tx.QueryRow(`SELECT
		(SELECT version FROM schema_migrations),
		(SELECT dirty FROM schema_migrations),
		(SELECT count(*) FROM pg_trigger WHERE tgrelid='audit_log'::regclass AND tgname='audit_log_immutable' AND tgenabled='O')`).
		Scan(&version, &dirty, &immutableTriggers); err != nil {
		return fmt.Errorf("read agent session cleanup schema: %w", err)
	}
	if version != 22 || dirty || immutableTriggers != 1 {
		return fmt.Errorf("refuse agent session cleanup at schema (%d,%v) immutable_triggers=%d", version, dirty, immutableTriggers)
	}
	result, err := tx.Exec(`DELETE FROM fabric_public_agent_sessions WHERE project_id=$1 AND agent_id=$2 AND session_id=$3`, projectID, agentID, sessionID)
	if err != nil {
		return fmt.Errorf("delete exact public agent session: %w", err)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		return fmt.Errorf("public agent session delete rows = (%d,%v), want (1,nil)", rows, rowsErr)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit public agent session cleanup: %w", err)
	}
	var sessions, ownedAgents, enabledTriggers int
	var replicationRole string
	if err = db.QueryRow(`SELECT
		(SELECT count(*) FROM fabric_public_agent_sessions WHERE project_id=$1 AND agent_id=$2 AND session_id=$3),
		(SELECT count(*) FROM agents WHERE id=$2 AND owner='sync-v2-push-test'),
		(SELECT count(*) FROM pg_trigger WHERE tgrelid='audit_log'::regclass AND tgname='audit_log_immutable' AND tgenabled='O'),
		current_setting('session_replication_role')`, projectID, agentID, sessionID).
		Scan(&sessions, &ownedAgents, &enabledTriggers, &replicationRole); err != nil {
		return fmt.Errorf("verify public agent session cleanup: %w", err)
	}
	if sessions != 0 || ownedAgents != 0 || enabledTriggers != 1 || replicationRole != "origin" {
		return fmt.Errorf("public agent session cleanup retained sessions=%d owned_agents=%d immutable_triggers=%d replication_role=%q", sessions, ownedAgents, enabledTriggers, replicationRole)
	}
	return nil
}

func cleanupSyncV2PushAgent(db *sql.DB, projectID, agentID string) (err error) {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var version int
	var dirty bool
	if err = tx.QueryRow(`SELECT version,dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version != 22 || dirty {
		return fmt.Errorf("refuse agent cleanup at schema (%d,%v)", version, dirty)
	}
	var owner string
	if err = tx.QueryRow(`SELECT owner FROM agents WHERE id=$1 FOR UPDATE`, agentID).Scan(&owner); err != nil {
		return fmt.Errorf("lock owned agent: %w", err)
	}
	if owner != "sync-v2-push-test" {
		return fmt.Errorf("refuse cleanup of agent owner %q", owner)
	}
	if _, err = tx.Exec(`LOCK TABLE audit_log IN ACCESS EXCLUSIVE MODE`); err != nil {
		return fmt.Errorf("lock audit log: %w", err)
	}
	if _, err = tx.Exec(`DELETE FROM fabric_public_agent_sessions WHERE project_id=$1 AND agent_id=$2`, projectID, agentID); err != nil {
		return fmt.Errorf("delete public agent sessions: %w", err)
	}
	if _, err = tx.Exec(`SET LOCAL session_replication_role=replica`); err != nil {
		return fmt.Errorf("enter owner cleanup replication role: %w", err)
	}
	if _, err = tx.Exec(`DELETE FROM audit_log WHERE project_id=$1 AND agent_id=$2`, projectID, agentID); err != nil {
		return fmt.Errorf("delete owned agent audit: %w", err)
	}
	if _, err = tx.Exec(`SET LOCAL session_replication_role=origin`); err != nil {
		return fmt.Errorf("leave owner cleanup replication role: %w", err)
	}
	result, err := tx.Exec(`DELETE FROM agents WHERE id=$1 AND owner='sync-v2-push-test'`, agentID)
	if err != nil {
		return fmt.Errorf("delete owned agent: %w", err)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		return fmt.Errorf("owned agent delete rows = (%d,%v), want (1,nil)", rows, rowsErr)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit owned agent cleanup: %w", err)
	}
	var agents, sessions, audits, immutableTriggers int
	if err = db.QueryRow(`SELECT
		(SELECT count(*) FROM agents WHERE id=$1),
		(SELECT count(*) FROM fabric_public_agent_sessions WHERE project_id=$2 AND agent_id=$1),
		(SELECT count(*) FROM audit_log WHERE project_id=$2 AND agent_id=$1),
		(SELECT count(*) FROM pg_trigger WHERE tgrelid='audit_log'::regclass AND tgname='audit_log_immutable' AND tgenabled='O')`, agentID, projectID).Scan(&agents, &sessions, &audits, &immutableTriggers); err != nil {
		return fmt.Errorf("verify owned agent cleanup: %w", err)
	}
	if agents != 0 || sessions != 0 || audits != 0 || immutableTriggers != 1 {
		return fmt.Errorf("owned agent cleanup retained agents=%d sessions=%d audits=%d immutable_triggers=%d", agents, sessions, audits, immutableTriggers)
	}
	return nil
}

func installSyncV2PushCASConflict(t *testing.T, db *sql.DB, projectID string) {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	schemaName := "sync_v2_push_cas_conflict_" + suffix
	functionName := schemaName + ".advance_stream"
	triggerName := "sync_v2_push_cas_conflict_tr_" + suffix
	statement := fmt.Sprintf(`CREATE SCHEMA %s;
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.project_id=%s::uuid AND NEW.transition_kind='operation' THEN
				UPDATE fabric_streams SET current_version=NEW.version,updated_at=now()
				WHERE project_id=NEW.project_id AND fabric_instance_id=NEW.fabric_instance_id
				AND stream_id=NEW.stream_id AND canonical_ref=NEW.canonical_ref AND current_version=NEW.version-1;
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER %s BEFORE INSERT ON fabric_stream_versions FOR EACH ROW EXECUTE FUNCTION %s()`,
		schemaName, functionName, quoteLiteral(projectID), triggerName, functionName)
	if _, err := db.Exec(statement); err != nil {
		t.Fatalf("install sync v2 push CAS conflict: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON fabric_stream_versions; DROP SCHEMA IF EXISTS %s CASCADE`, triggerName, schemaName)); err != nil {
			t.Errorf("remove sync v2 push CAS conflict: %v", err)
		}
	})
}

func corruptSyncV2PushRequest(t *testing.T, db *sql.DB, projectID, operationID string) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	result, err := tx.Exec(`UPDATE fabric_stream_requests SET canonical_operation_json=$1 WHERE project_id=$2 AND operation_id=$3`, []byte("{"), projectID, operationID)
	if err != nil {
		t.Fatalf("corrupt push request: %v", err)
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		t.Fatalf("corrupt push request rows = (%d,%v), want (1,nil)", rows, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func installSyncV2PushVersionFailure(t *testing.T, db *sql.DB, projectID string) {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	schemaName := "sync_v2_push_sql_fail_" + suffix
	functionName := schemaName + ".reject_version"
	triggerName := "sync_v2_push_sql_fail_tr_" + suffix
	statement := fmt.Sprintf(`CREATE SCHEMA %s;
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.project_id=%s::uuid AND NEW.transition_kind='operation' THEN
				RAISE EXCEPTION 'forced sync v2 push SQL failure';
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER %s BEFORE INSERT ON fabric_stream_versions FOR EACH ROW EXECUTE FUNCTION %s()`,
		schemaName, functionName, quoteLiteral(projectID), triggerName, functionName)
	if _, err := db.Exec(statement); err != nil {
		t.Fatalf("install sync v2 push SQL failure: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON fabric_stream_versions; DROP SCHEMA IF EXISTS %s CASCADE`, triggerName, schemaName)); err != nil {
			t.Errorf("remove sync v2 push SQL failure: %v", err)
		}
	})
}

func syncV2PushOperation(f *mutationFixture, state coregit.StreamTransition, operationID, recordID string) projectstate.OperationV1 {
	record := projectstate.ActorV1{
		SchemaVersion: 1, Kind: "actor", ID: recordID, ActorKind: types.ActorAgent,
		DisplayName: "Push Agent", PublicKeys: []projectstate.PublicKeyV1{}, Extensions: projectstate.ExtensionsV1{},
	}
	actor := f.transport
	actor.Assurance = types.AssuranceLocal
	return projectstate.OperationV1{
		SchemaVersion: 1, ID: operationID, Kind: projectstate.OperationPutRecord,
		ExpectedViewDigest: state.Live.Digest, Actor: actor,
		PutRecord: &projectstate.PutRecordV1{Record: projectstate.RecordValueV1{Actor: &record}},
	}
}

func syncV2PushArguments(attached InitialAttachResult, operation projectstate.OperationV1) SyncPushV2Args {
	return SyncPushV2Args{SyncV2Scope: boundReadArguments(attached, 0).SyncV2Scope, Operation: operation}
}

func canonicalSyncV2PushArguments(t *testing.T, arguments SyncPushV2Args) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	return canonicalMutationJSON(t, raw)
}

func syncV2PushProof(t *testing.T, f *mutationFixture, raw json.RawMessage, attachment string, nonce byte) types.PublicRequestProof {
	t.Helper()
	seed := sha256.Sum256([]byte(f.projectID))
	return signedBoundProof(t, f.fabricID, "wormhole.sync.push", raw, attachment, f.transport.OccurredAt, bytesOf(nonce, 32), seed[:])
}

func assertSyncV2PushFailure(t *testing.T, err error, code string) {
	t.Helper()
	assertSyncReadFailure(t, err, "wormhole.sync.push", code)
}

func TestSyncV2PushConstructorFailsClosed(t *testing.T) {
	db := testDB(t)
	streams := coregit.NewStreamStore(db)
	coordinator, err := NewMutationCoordinator(identity.NewStore(db), streams, coregit.NewActivityStore(db))
	if err != nil {
		t.Fatal(err)
	}
	fabricID := uuid.NewString()
	verifier, err := NewPublicProofVerifier(fabricID, func() time.Time { return time.Now().UTC() })
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewPublicBoundProofResolver(fabricID, identity.NewStore(db), streams, verifier)
	if err != nil {
		t.Fatal(err)
	}
	for name, dependencies := range map[string]struct {
		resolver    *PublicBoundProofResolver
		coordinator *MutationCoordinator
		streams     *coregit.StreamStore
	}{
		"resolver":    {coordinator: coordinator, streams: streams},
		"coordinator": {resolver: resolver, streams: streams},
		"streams":     {resolver: resolver, coordinator: coordinator},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewSyncV2PushHandler(dependencies.resolver, dependencies.coordinator, dependencies.streams); !errors.Is(err, identity.ErrInvalidPublicIdentity) {
				t.Fatalf("constructor error = %v, want ErrInvalidPublicIdentity", err)
			}
		})
	}
	var zero *SyncV2PushHandler
	if _, err := zero.Handle(context.Background(), nil, types.PublicRequestProof{}); err == nil || err.Error() != `{"code":"internal_error","operation":"wormhole.sync.push"}` {
		t.Fatalf("nil handler error = %v", err)
	}
}

func TestSyncV2PushRejectsInvalidArgumentsBeforeAuthorization(t *testing.T) {
	f := newSyncV2PushFixture(t, 70)
	operation := syncV2PushOperation(f.owner, f.attached.State, uuid.NewString(), uuid.NewString())
	validRaw := canonicalSyncV2PushArguments(t, syncV2PushArguments(f.attached, operation))
	valid := string(validRaw)
	var missingObject map[string]any
	if err := json.Unmarshal(validRaw, &missingObject); err != nil {
		t.Fatal(err)
	}
	delete(missingObject, "operation")
	missingRaw, err := json.Marshal(missingObject)
	if err != nil {
		t.Fatal(err)
	}
	var nullObject map[string]any
	if err := json.Unmarshal(validRaw, &nullObject); err != nil {
		t.Fatal(err)
	}
	nullObject["operation"] = nil
	nullRaw, err := json.Marshal(nullObject)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]struct {
		raw, code string
	}{
		"unknown":              {strings.TrimSuffix(valid, `}`) + `,"project_id":"` + f.owner.projectID + `"}`, "invalid_request"},
		"duplicate":            {strings.Replace(valid, `"version":2`, `"version":2,"version":2`, 1), "invalid_request"},
		"missing":              {string(missingRaw), "invalid_request"},
		"null":                 {string(nullRaw), "invalid_request"},
		"trailing":             {valid + `{}`, "invalid_request"},
		"noncanonical":         {strings.Replace(valid, `{`, `{ `, 1), "invalid_request"},
		"wrong version":        {strings.Replace(valid, `"version":2`, `"version":3`, 1), "unknown_version"},
		"private route":        {strings.TrimSuffix(valid, `}`) + `,"workspace_id":"` + uuid.NewString() + `"}`, "invalid_request"},
		"malformed attachment": {strings.Replace(valid, f.attached.Attachment.AttachmentRef, "not-a-uuid", 1), "invalid_request"},
		"wrong kind":           {strings.Replace(valid, `"expected_stream_version":0`, `"expected_stream_version":"zero"`, 1), "invalid_request"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			before := task2MutationSnapshot(t, f.owner.db, f.owner.projectID)
			_, err := f.handler.Handle(context.Background(), json.RawMessage(test.raw), types.PublicRequestProof{})
			assertSyncV2PushFailure(t, err, test.code)
			assertTask2MutationDelta(t, before, task2MutationSnapshot(t, f.owner.db, f.owner.projectID), 0)
		})
	}

	unknown := syncV2PushArguments(f.attached, operation)
	unknown.AttachmentRef = uuid.NewString()
	unknownRaw := canonicalSyncV2PushArguments(t, unknown)
	beforeUnknown := task2MutationSnapshot(t, f.owner.db, f.owner.projectID)
	_, err = f.handler.Handle(context.Background(), unknownRaw, syncV2PushProof(t, f.owner, unknownRaw, unknown.AttachmentRef, 71))
	assertSyncV2PushFailure(t, err, "attachment_not_found")
	assertTask2MutationDelta(t, beforeUnknown, task2MutationSnapshot(t, f.owner.db, f.owner.projectID), 0)

	if _, err := f.owner.db.Exec(`UPDATE fabric_workspace_stream_bindings SET writable=false,detached_at=now() WHERE project_id=$1 AND attachment_ref=$2`, f.owner.projectID, f.attached.Attachment.AttachmentRef); err != nil {
		t.Fatal(err)
	}
	detachedRaw := json.RawMessage(valid)
	beforeDetached := task2MutationSnapshot(t, f.owner.db, f.owner.projectID)
	_, err = f.handler.Handle(context.Background(), detachedRaw, syncV2PushProof(t, f.owner, detachedRaw, f.attached.Attachment.AttachmentRef, 72))
	assertSyncV2PushFailure(t, err, "attachment_not_found")
	assertTask2MutationDelta(t, beforeDetached, task2MutationSnapshot(t, f.owner.db, f.owner.projectID), 0)
}

func TestSyncV2PushBurnsNonceForAuthenticatedDomainAndScopeFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SyncPushV2Args)
		code   string
	}{
		{"wrong signed scope", func(arguments *SyncPushV2Args) { arguments.BaseCommitSHA = strings.Repeat("b", 40) }, "sync_precondition_failed"},
		{"invalid operation schema", func(arguments *SyncPushV2Args) { arguments.Operation.SchemaVersion = 2 }, "sync_precondition_failed"},
		{"invalid operation kind", func(arguments *SyncPushV2Args) { arguments.Operation.Kind = projectstate.OperationKind("unknown") }, "sync_precondition_failed"},
		{"invalid operation payload", func(arguments *SyncPushV2Args) { arguments.Operation.PutRecord = nil }, "sync_precondition_failed"},
		{"stable human mismatch", func(arguments *SyncPushV2Args) { arguments.Operation.Actor.HumanPrincipalID = uuid.NewString() }, "permission_denied"},
		{"legacy assurance", func(arguments *SyncPushV2Args) { arguments.Operation.Actor.Assurance = types.AssuranceLegacy }, "sync_precondition_failed"},
		{"unknown assurance", func(arguments *SyncPushV2Args) { arguments.Operation.Actor.Assurance = types.AssuranceUnknown }, "sync_precondition_failed"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSyncV2PushFixture(t, byte(80+index*2))
			arguments := syncV2PushArguments(fixture.attached, syncV2PushOperation(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString()))
			test.mutate(&arguments)
			raw := canonicalSyncV2PushArguments(t, arguments)
			before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
			_, err := fixture.handler.Handle(context.Background(), raw, syncV2PushProof(t, fixture.owner, raw, arguments.AttachmentRef, byte(81+index*2)))
			assertSyncV2PushFailure(t, err, test.code)
			assertTask2MutationDelta(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), 1)
		})
	}

	t.Run("audit failure", func(t *testing.T) {
		fixture := newSyncV2PushFixture(t, 100)
		installAuditFailure(t, fixture.owner.db, fixture.owner.projectID, "sync.push")
		arguments := syncV2PushArguments(fixture.attached, syncV2PushOperation(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString()))
		raw := canonicalSyncV2PushArguments(t, arguments)
		before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		_, err := fixture.handler.Handle(context.Background(), raw, syncV2PushProof(t, fixture.owner, raw, arguments.AttachmentRef, 101))
		assertSyncV2PushFailure(t, err, "internal_error")
		assertTask2MutationDelta(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), 1)
	})

	t.Run("deferred commit rejection", func(t *testing.T) {
		fixture := newSyncV2PushFixture(t, 104)
		if _, err := fixture.owner.db.Exec(`CREATE FUNCTION wormhole_test_reject_sync_push_commit() RETURNS trigger
			LANGUAGE plpgsql AS $$
			BEGIN
				IF NEW.action = 'sync.push' THEN
					RAISE EXCEPTION 'forced deferred sync push commit failure';
				END IF;
				RETURN NEW;
			END
			$$;
			CREATE CONSTRAINT TRIGGER wormhole_test_reject_sync_push_commit
			AFTER INSERT ON audit_log
			DEFERRABLE INITIALLY DEFERRED
			FOR EACH ROW EXECUTE FUNCTION wormhole_test_reject_sync_push_commit()`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = fixture.owner.db.Exec(`DROP TRIGGER IF EXISTS wormhole_test_reject_sync_push_commit ON audit_log;
				DROP FUNCTION IF EXISTS wormhole_test_reject_sync_push_commit()`)
		})
		arguments := syncV2PushArguments(fixture.attached, syncV2PushOperation(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString()))
		raw := canonicalSyncV2PushArguments(t, arguments)
		before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		_, err := fixture.handler.Handle(context.Background(), raw, syncV2PushProof(t, fixture.owner, raw, arguments.AttachmentRef, 105))
		assertSyncV2PushFailure(t, err, "internal_error")
		assertTask2MutationDelta(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), 1)
	})

	t.Run("post-authorization detach", func(t *testing.T) {
		fixture := newSyncV2PushFixture(t, 102)
		arguments := syncV2PushArguments(fixture.attached, syncV2PushOperation(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString()))
		raw := canonicalSyncV2PushArguments(t, arguments)
		beforeAuthorization := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		authorized, err := fixture.resolver.AuthorizeMutation(context.Background(), "wormhole.sync.push", raw, arguments.SyncV2Scope, syncV2PushProof(t, fixture.owner, raw, arguments.AttachmentRef, 103))
		if err != nil {
			t.Fatal(err)
		}
		assertTask2MutationDelta(t, beforeAuthorization, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), 1)
		if _, err := fixture.owner.db.Exec(`UPDATE fabric_workspace_stream_bindings SET writable=false,detached_at=now() WHERE project_id=$1 AND attachment_ref=$2`, fixture.owner.projectID, fixture.attached.Attachment.AttachmentRef); err != nil {
			t.Fatal(err)
		}
		beforeExecute := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		err = fixture.coordinator.ExecutePublic(context.Background(), authorized, "sync.push", raw, func(ctx context.Context, tx *sql.Tx, verified VerifiedMutation) error {
			_, applyErr := fixture.streams.ApplyPublicOperationInTx(ctx, tx, verified.Scope, coregit.ApplyPublicOperationInput{Attachment: verified.Attachment, Precondition: syncMutationPrecondition(arguments.SyncV2Scope), Operation: arguments.Operation})
			return applyErr
		})
		if !errors.Is(err, coregit.ErrStreamNotFound) {
			t.Fatalf("ExecutePublic error = %v, want ErrStreamNotFound", err)
		}
		assertTask2MutationDelta(t, beforeExecute, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), 0)
	})
}

func TestSyncV2PushRealPostAuthorizationFailuresBurnNonceAndRollBackCompleteRows(t *testing.T) {
	db := testDB(t)
	var negativeProjectID, negativeAgentID, negativeSessionID string
	t.Run("stable agent mismatch", func(t *testing.T) {
		fixture, session, agentID := newSyncV2PushAgentFixture(t, 106, false)
		negativeProjectID, negativeAgentID, negativeSessionID = fixture.owner.projectID, agentID, session.SessionID
		operation := syncV2PushOperation(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString())
		operation.Actor = types.ActorEnvelope{
			ActorKind: types.ActorAgent, AgentID: uuid.NewString(), AccountableHumanID: fixture.owner.actor.ID,
			SessionID: uuid.NewString(), HarnessName: "historical", HarnessVersion: "0",
			ModelName: "old", ModelVersion: "1", Assurance: types.AssuranceLocal,
			OccurredAt: fixture.owner.transport.OccurredAt.Add(-time.Minute),
		}
		if operation.Actor.AgentID == agentID {
			t.Fatal("agent mismatch fixture accidentally matched the live session")
		}
		arguments := syncV2PushArguments(fixture.attached, operation)
		raw := canonicalSyncV2PushArguments(t, arguments)
		seed := sha256.Sum256([]byte(fixture.owner.projectID))
		proof := signedBoundSessionProof(t, fixture.owner.fabricID, "wormhole.sync.push", raw, arguments.AttachmentRef, session.SessionID, fixture.owner.transport.OccurredAt, bytesOf(107, 32), seed[:])
		before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		_, err := fixture.handler.Handle(context.Background(), raw, proof)
		assertSyncV2PushFailure(t, err, "permission_denied")
		assertTask2ExactRowDeltas(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), map[string]int{"public_request_nonces": 1})
	})
	if negativeSessionID != "" {
		var retainedSessions, retainedOwnedAgents int
		if err := db.QueryRow(`SELECT
			(SELECT count(*) FROM fabric_public_agent_sessions WHERE project_id=$1 AND agent_id=$2 AND session_id=$3),
			(SELECT count(*) FROM agents WHERE id=$2 AND owner='sync-v2-push-test')`, negativeProjectID, negativeAgentID, negativeSessionID).
			Scan(&retainedSessions, &retainedOwnedAgents); err != nil {
			t.Fatal(err)
		}
		if retainedSessions != 0 || retainedOwnedAgents != 0 {
			t.Fatalf("negative agent fixture %s/%s/%s retained sessions=%d owned_agents=%d, want zero exact rows", negativeProjectID, negativeAgentID, negativeSessionID, retainedSessions, retainedOwnedAgents)
		}
	}

	t.Run("reducer invalid semantic collision", func(t *testing.T) {
		fixture := newSyncV2PushFixture(t, 108)
		operation := syncV2PushOperation(fixture.owner, fixture.attached.State, uuid.NewString(), fixture.owner.projectID)
		if err := projectstate.ValidateOperationForApply(operation); err != nil {
			t.Fatalf("operation did not reach reducer: %v", err)
		}
		if _, err := projectstate.ApplyOperation(fixture.attached.State.Live, operation); !errors.Is(err, projectstate.ErrInvalidSnapshot) {
			t.Fatalf("reducer error = %v, want ErrInvalidSnapshot", err)
		} else if _, classified := projectstate.ClassifyOperationFailure(err); classified {
			t.Fatalf("reducer ErrInvalidSnapshot was classified: %v", err)
		}
		arguments := syncV2PushArguments(fixture.attached, operation)
		raw := canonicalSyncV2PushArguments(t, arguments)
		before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		_, err := fixture.handler.Handle(context.Background(), raw, syncV2PushProof(t, fixture.owner, raw, arguments.AttachmentRef, 109))
		assertSyncV2PushFailure(t, err, "sync_precondition_failed")
		assertTask2ExactRowDeltas(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), map[string]int{"public_request_nonces": 1})
	})

	t.Run("reachable stream conflict", func(t *testing.T) {
		fixture := newSyncV2PushFixture(t, 150)
		installSyncV2PushCASConflict(t, fixture.owner.db, fixture.owner.projectID)
		operation := syncV2PushOperation(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString())
		arguments := syncV2PushArguments(fixture.attached, operation)
		raw := canonicalSyncV2PushArguments(t, arguments)
		before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		_, err := fixture.handler.Handle(context.Background(), raw, syncV2PushProof(t, fixture.owner, raw, arguments.AttachmentRef, 151))
		assertSyncV2PushFailure(t, err, "sync_conflict")
		assertTask2ExactRowDeltas(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), map[string]int{"public_request_nonces": 1})
	})

	t.Run("corrupt stored push evidence", func(t *testing.T) {
		fixture := newSyncV2PushFixture(t, 152)
		operation := syncV2PushOperation(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString())
		arguments := syncV2PushArguments(fixture.attached, operation)
		raw := canonicalSyncV2PushArguments(t, arguments)
		if result, err := fixture.handler.Handle(context.Background(), raw, syncV2PushProof(t, fixture.owner, raw, arguments.AttachmentRef, 153)); err != nil {
			t.Fatalf("seed push: %v", err)
		} else if _, ok := result.(SyncPushAppliedV2Result); !ok {
			t.Fatalf("seed push result type = %T", result)
		}
		corruptSyncV2PushRequest(t, fixture.owner.db, fixture.owner.projectID, operation.ID)
		before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		_, err := fixture.handler.Handle(context.Background(), raw, syncV2PushProof(t, fixture.owner, raw, arguments.AttachmentRef, 154))
		assertSyncV2PushFailure(t, err, "internal_error")
		assertTask2ExactRowDeltas(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), map[string]int{"public_request_nonces": 1})
	})

	t.Run("injected internal SQL failure", func(t *testing.T) {
		fixture := newSyncV2PushFixture(t, 155)
		installSyncV2PushVersionFailure(t, fixture.owner.db, fixture.owner.projectID)
		operation := syncV2PushOperation(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString())
		arguments := syncV2PushArguments(fixture.attached, operation)
		raw := canonicalSyncV2PushArguments(t, arguments)
		before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		_, err := fixture.handler.Handle(context.Background(), raw, syncV2PushProof(t, fixture.owner, raw, arguments.AttachmentRef, 156))
		assertSyncV2PushFailure(t, err, "internal_error")
		assertTask2ExactRowDeltas(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), map[string]int{"public_request_nonces": 1})
	})
}

func TestSyncV2PushSafeFailureMappingAndRedaction(t *testing.T) {
	tests := []struct {
		err  error
		code string
	}{
		{identity.ErrPublicAuthentication, "authentication_failed"},
		{identity.ErrPublicNonceReplay, "authentication_failed"},
		{identity.ErrInvalidPublicIdentity, "authentication_failed"},
		{coregit.ErrStreamNotFound, "attachment_not_found"},
		{coregit.ErrStreamActor, "permission_denied"},
		{coregit.ErrStreamPrecondition, "sync_precondition_failed"},
		{coregit.ErrStreamConflict, "sync_conflict"},
		{coregit.ErrOperationReplay, "sync_replay_conflict"},
		{projectstate.ErrInvalidSnapshot, "sync_precondition_failed"},
		{projectstate.ErrUnknownVersion, "sync_precondition_failed"},
		{projectstate.ErrUnknownKind, "sync_precondition_failed"},
		{projectstate.ErrInvalidActorEnvelope, "sync_precondition_failed"},
		{projectstate.ErrBrokenReference, "sync_precondition_failed"},
		{projectstate.ErrTrackedSecret, "sync_precondition_failed"},
		{projectstate.ErrOperationPrecondition, "sync_precondition_failed"},
		{projectstate.ErrImmutableRecord, "sync_precondition_failed"},
		{projectstate.ErrTombstoneDigest, "sync_precondition_failed"},
		{projectstate.ErrResurrectionDigest, "sync_precondition_failed"},
		{errors.New("pq: secret SQL /private/path attachment operation body"), "internal_error"},
	}
	for _, test := range tests {
		if got := syncMutationErrorCode(test.err); got != test.code {
			t.Errorf("syncMutationErrorCode(%v) = %q, want %q", test.err, got, test.code)
		}
		err := syncMutationFailure("wormhole.sync.push", test.code)
		want := `{"code":"` + test.code + `","operation":"wormhole.sync.push"}`
		if err == nil || err.Error() != want {
			t.Errorf("safe error = %v, want %s", err, want)
		}
		for _, secret := range []string{"secret SQL", "/private/path", "attachment-ref-secret", "operation body", "pq:"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("safe error leaked %q: %s", secret, err)
			}
		}
	}
}

func TestSyncV2PushAppliedPersistsCanonicalOperationBytesAndTypedHumanAudit(t *testing.T) {
	fixture := newSyncV2PushFixture(t, 110)
	operation := syncV2PushOperation(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString())
	arguments := syncV2PushArguments(fixture.attached, operation)
	raw := canonicalSyncV2PushArguments(t, arguments)
	wantOperation, err := projectstate.CanonicalOperation(operation)
	if err != nil {
		t.Fatal(err)
	}
	before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	got, err := fixture.handler.Handle(context.Background(), raw, syncV2PushProof(t, fixture.owner, raw, arguments.AttachmentRef, 111))
	if err != nil {
		t.Fatal(err)
	}
	result, ok := got.(SyncPushAppliedV2Result)
	if !ok || result.Version != 2 || result.Status != "applied" || result.OperationID != operation.ID || result.StreamVersion != 1 || result.LiveTreeDigest == fixture.attached.State.Live.Digest {
		t.Fatalf("push result = (%T)%+v", got, got)
	}
	var requestOperation, versionOperation, requestActor, versionActor, auditPayload, auditActor []byte
	var auditAction string
	err = fixture.owner.db.QueryRow(`SELECT r.canonical_operation_json,v.canonical_operation_json,r.actor_envelope_json,v.actor_envelope_json,a.action,a.canonical_payload_json::text::bytea,a.actor_envelope_json::text::bytea
		FROM fabric_stream_requests r JOIN fabric_stream_versions v
		ON v.project_id=r.project_id AND v.fabric_instance_id=r.fabric_instance_id AND v.stream_id=r.stream_id AND v.operation_id=r.operation_id
		JOIN audit_log a ON a.project_id=r.project_id AND a.action='sync.push'
		WHERE r.project_id=$1 AND r.operation_id=$2`, fixture.owner.projectID, operation.ID).
		Scan(&requestOperation, &versionOperation, &requestActor, &versionActor, &auditAction, &auditPayload, &auditActor)
	if err != nil {
		t.Fatal(err)
	}
	wantAuditActor, err := json.Marshal(fixture.owner.transport)
	if err != nil {
		t.Fatal(err)
	}
	wantPortableActor, err := projectstate.CanonicalJSON(operation.Actor)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(requestOperation, wantOperation) || !bytes.Equal(versionOperation, wantOperation) || !bytes.Equal(requestActor, wantPortableActor) || !bytes.Equal(versionActor, wantPortableActor) || auditAction != "sync.push" || !bytes.Equal(auditPayload, raw) || !bytes.Equal(auditActor, wantAuditActor) {
		t.Fatalf("stored push/audit evidence changed")
	}
	after := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	for table, delta := range map[string]int{"fabric_stream_versions": 1, "fabric_stream_requests": 1, "public_request_nonces": 1, "audit_log": 1} {
		if len(after[table])-len(before[table]) != delta {
			t.Errorf("%s delta = %d, want %d", table, len(after[table])-len(before[table]), delta)
		}
	}
	if len(after["fabric_stream_conflicts"]) != len(before["fabric_stream_conflicts"]) {
		t.Fatal("applied push created conflict")
	}
}

func TestSyncV2PushAgentStableAttributionUsesLiveSessionWithoutRewritingOperation(t *testing.T) {
	fixture, session, agentID := newSyncV2PushAgentFixture(t, 112, true)
	owner := fixture.owner
	attached := fixture.attached
	operation := syncV2PushOperation(owner, attached.State, uuid.NewString(), uuid.NewString())
	operation.Actor = types.ActorEnvelope{
		ActorKind: types.ActorAgent, AgentID: agentID, AccountableHumanID: owner.actor.ID,
		SessionID: uuid.NewString(), HarnessName: "historical", HarnessVersion: "0",
		ModelName: "old", ModelVersion: "1", Assurance: types.AssuranceLocal,
		OccurredAt: owner.transport.OccurredAt.Add(-time.Minute),
	}
	arguments := syncV2PushArguments(attached, operation)
	raw := canonicalSyncV2PushArguments(t, arguments)
	seed := sha256.Sum256([]byte(owner.projectID))
	proof := signedBoundSessionProof(t, owner.fabricID, "wormhole.sync.push", raw, arguments.AttachmentRef, session.SessionID, owner.transport.OccurredAt, bytesOf(113, 32), seed[:])
	got, err := fixture.handler.Handle(context.Background(), raw, proof)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.(SyncPushAppliedV2Result); !ok {
		t.Fatalf("result type = %T, want SyncPushAppliedV2Result", got)
	}
	wantOperation, _ := projectstate.CanonicalOperation(operation)
	wantPortableActor, _ := projectstate.CanonicalJSON(operation.Actor)
	var requestOperation, versionOperation, requestActor, versionActor, auditActor []byte
	if err := owner.db.QueryRow(`SELECT r.canonical_operation_json,v.canonical_operation_json,r.actor_envelope_json,v.actor_envelope_json,a.actor_envelope_json::text::bytea
		FROM fabric_stream_requests r JOIN fabric_stream_versions v
		ON v.project_id=r.project_id AND v.fabric_instance_id=r.fabric_instance_id AND v.stream_id=r.stream_id AND v.operation_id=r.operation_id
		JOIN audit_log a ON a.project_id=r.project_id AND a.action='sync.push'
		WHERE r.project_id=$1 AND r.operation_id=$2`, owner.projectID, operation.ID).
		Scan(&requestOperation, &versionOperation, &requestActor, &versionActor, &auditActor); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(requestOperation, wantOperation) || !bytes.Equal(versionOperation, wantOperation) || !bytes.Equal(requestActor, wantPortableActor) || !bytes.Equal(versionActor, wantPortableActor) {
		t.Fatal("portable agent operation or actor bytes were rewritten")
	}
	var transport types.ActorEnvelope
	if err := json.Unmarshal(auditActor, &transport); err != nil {
		t.Fatal(err)
	}
	if transport.ActorKind != types.ActorAgent || transport.AgentID != agentID || transport.AccountableHumanID != owner.actor.ID || transport.SessionID != session.SessionID ||
		transport.HarnessName != session.HarnessName || transport.HarnessVersion != session.HarnessVersion || transport.ModelName != session.ModelName || transport.ModelVersion != session.ModelVersion ||
		transport.Assurance != types.AssurancePublicKeyContinuity || transport.OccurredAt != owner.transport.OccurredAt {
		t.Fatalf("audit transport actor = %+v, session=%+v", transport, session)
	}
}

func TestSyncV2PushExactReplayReturnsOriginalAppliedResultAfterAdvance(t *testing.T) {
	fixture := newSyncV2PushFixture(t, 114)
	firstOperation := syncV2PushOperation(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString())
	firstArguments := syncV2PushArguments(fixture.attached, firstOperation)
	firstRaw := canonicalSyncV2PushArguments(t, firstArguments)
	firstAny, err := fixture.handler.Handle(context.Background(), firstRaw, syncV2PushProof(t, fixture.owner, firstRaw, firstArguments.AttachmentRef, 115))
	if err != nil {
		t.Fatal(err)
	}
	first := firstAny.(SyncPushAppliedV2Result)
	secondState := fixture.attached.State
	secondState.Version, secondState.Live.Digest = first.StreamVersion, first.LiveTreeDigest
	secondOperation := syncV2PushOperation(fixture.owner, secondState, uuid.NewString(), uuid.NewString())
	secondArguments := syncV2PushArguments(fixture.attached, secondOperation)
	secondArguments.ExpectedStreamVersion, secondArguments.ExpectedLiveTreeDigest = first.StreamVersion, first.LiveTreeDigest
	secondRaw := canonicalSyncV2PushArguments(t, secondArguments)
	secondAny, err := fixture.handler.Handle(context.Background(), secondRaw, syncV2PushProof(t, fixture.owner, secondRaw, secondArguments.AttachmentRef, 116))
	if err != nil {
		t.Fatal(err)
	}
	second := secondAny.(SyncPushAppliedV2Result)
	if second.StreamVersion <= first.StreamVersion {
		t.Fatalf("second result = %+v, first=%+v", second, first)
	}
	replayed, err := fixture.handler.Handle(context.Background(), firstRaw, syncV2PushProof(t, fixture.owner, firstRaw, firstArguments.AttachmentRef, 117))
	if err != nil || replayed != first {
		t.Fatalf("exact historical replay = (%+v,%v), want %+v", replayed, err, first)
	}
}

func TestSyncV2PushChangedBytesForOperationIDReturnsSafeReplayConflict(t *testing.T) {
	fixture := newSyncV2PushFixture(t, 118)
	operation := syncV2PushOperation(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString())
	arguments := syncV2PushArguments(fixture.attached, operation)
	raw := canonicalSyncV2PushArguments(t, arguments)
	if _, err := fixture.handler.Handle(context.Background(), raw, syncV2PushProof(t, fixture.owner, raw, arguments.AttachmentRef, 119)); err != nil {
		t.Fatal(err)
	}
	changed := []struct {
		name   string
		mutate func(*projectstate.OperationV1)
		code   string
	}{
		{"record bytes", func(candidate *projectstate.OperationV1) {
			record := *operation.PutRecord.Record.Actor
			record.DisplayName = "Changed push actor"
			candidate.PutRecord = &projectstate.PutRecordV1{Record: projectstate.RecordValueV1{Actor: &record}}
		}, "sync_replay_conflict"},
		{"kind and payload", func(candidate *projectstate.OperationV1) {
			candidate.Kind, candidate.PutRecord = projectstate.OperationPutKBArticle, nil
			candidate.PutKBArticle = &projectstate.PutKBArticleV1{Record: projectstate.KBArticleV1{
				SchemaVersion: 1, Kind: "kb_article", ID: uuid.NewString(), Title: "Changed kind",
				Frontmatter: map[string]json.RawMessage{}, AuthorActorID: fixture.owner.actor.ID,
				RelatedArticleIDs: []string{}, CreatedAt: fixture.owner.transport.OccurredAt,
				UpdatedAt: fixture.owner.transport.OccurredAt, Extensions: projectstate.ExtensionsV1{},
			}, Body: "changed kind\n"}
		}, "sync_replay_conflict"},
		{"stable actor tuple", func(candidate *projectstate.OperationV1) {
			candidate.Actor.HumanPrincipalID = uuid.NewString()
		}, "permission_denied"},
	}
	for index, test := range changed {
		t.Run(test.name, func(t *testing.T) {
			candidate := arguments
			candidate.Operation = operation
			test.mutate(&candidate.Operation)
			changedRaw := canonicalSyncV2PushArguments(t, candidate)
			before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
			_, err := fixture.handler.Handle(context.Background(), changedRaw, syncV2PushProof(t, fixture.owner, changedRaw, candidate.AttachmentRef, byte(120+index)))
			assertSyncV2PushFailure(t, err, test.code)
			assertTask2MutationDelta(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), 1)
		})
	}
}

func TestSyncV2PushHistoricalReplayRejectsEveryChangedSignedFieldAndActorBytes(t *testing.T) {
	fixture := newSyncV2PushFixture(t, 121)
	operation := syncV2PushOperation(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString())
	arguments := syncV2PushArguments(fixture.attached, operation)
	raw := canonicalSyncV2PushArguments(t, arguments)
	firstAny, err := fixture.handler.Handle(context.Background(), raw, syncV2PushProof(t, fixture.owner, raw, arguments.AttachmentRef, 122))
	if err != nil {
		t.Fatal(err)
	}
	first := firstAny.(SyncPushAppliedV2Result)
	advancedState := fixture.attached.State
	advancedState.Version, advancedState.Live.Digest = first.StreamVersion, first.LiveTreeDigest
	advanceOperation := syncV2PushOperation(fixture.owner, advancedState, uuid.NewString(), uuid.NewString())
	advanceArguments := syncV2PushArguments(fixture.attached, advanceOperation)
	advanceArguments.ExpectedStreamVersion, advanceArguments.ExpectedLiveTreeDigest = first.StreamVersion, first.LiveTreeDigest
	advanceRaw := canonicalSyncV2PushArguments(t, advanceArguments)
	if _, err := fixture.handler.Handle(context.Background(), advanceRaw, syncV2PushProof(t, fixture.owner, advanceRaw, advanceArguments.AttachmentRef, 128)); err != nil {
		t.Fatalf("advance before historical replay table: %v", err)
	}
	mutations := []struct {
		name   string
		mutate func(*SyncPushV2Args)
	}{
		{"base commit", func(candidate *SyncPushV2Args) { candidate.BaseCommitSHA = strings.Repeat("b", 40) }},
		{"base tree", func(candidate *SyncPushV2Args) {
			candidate.BaseTreeDigest = projectstate.Digest("sha256:" + strings.Repeat("b", 64))
		}},
		{"expected stream version", func(candidate *SyncPushV2Args) { candidate.ExpectedStreamVersion++ }},
		{"expected live tree", func(candidate *SyncPushV2Args) {
			candidate.ExpectedLiveTreeDigest = projectstate.Digest("sha256:" + strings.Repeat("c", 64))
		}},
		{"portable actor bytes", func(candidate *SyncPushV2Args) {
			candidate.Operation.Actor.OccurredAt = candidate.Operation.Actor.OccurredAt.Add(time.Second)
		}},
	}
	for index, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			candidate := arguments
			candidate.Operation = operation
			test.mutate(&candidate)
			candidateRaw := canonicalSyncV2PushArguments(t, candidate)
			before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
			_, err := fixture.handler.Handle(context.Background(), candidateRaw, syncV2PushProof(t, fixture.owner, candidateRaw, candidate.AttachmentRef, byte(123+index)))
			assertSyncV2PushFailure(t, err, "sync_replay_conflict")
			assertTask2MutationDelta(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), 1)
		})
	}
}

func TestSyncV2PushReturnsSuccessfulDurableConflictAndExactReplay(t *testing.T) {
	fixture := newSyncV2PushFixture(t, 130)
	operation := projectstate.OperationV1{
		SchemaVersion: 1, ID: uuid.NewString(), Kind: projectstate.OperationTombstone,
		ExpectedViewDigest: fixture.attached.State.Live.Digest,
		Actor:              types.ActorEnvelope{ActorKind: types.ActorHuman, HumanPrincipalID: fixture.owner.actor.ID, Assurance: types.AssuranceLocal, OccurredAt: fixture.owner.transport.OccurredAt},
		Tombstone:          &projectstate.TombstoneOperationV1{Key: projectstate.RecordKey{Kind: "actor", ID: fixture.owner.actor.ID}, ExpectedContentDigest: projectstate.Digest("sha256:" + strings.Repeat("f", 64))},
	}
	arguments := syncV2PushArguments(fixture.attached, operation)
	raw := canonicalSyncV2PushArguments(t, arguments)
	beforeFirst := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	firstAny, err := fixture.handler.Handle(context.Background(), raw, syncV2PushProof(t, fixture.owner, raw, arguments.AttachmentRef, 131))
	if err != nil {
		t.Fatal(err)
	}
	first, ok := firstAny.(SyncPushConflictV2Result)
	if !ok || first.Version != 2 || first.Status != "conflict" || first.OperationID != operation.ID || first.StreamVersion != fixture.attached.State.Version || first.LiveTreeDigest != fixture.attached.State.Live.Digest || !types.CanonicalUUID(first.ConflictID) {
		t.Fatalf("conflict result = (%T)%+v", firstAny, firstAny)
	}
	afterFirst := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	assertTask2ExactRowDeltas(t, beforeFirst, afterFirst, map[string]int{
		"fabric_stream_requests":  1,
		"fabric_stream_conflicts": 1,
		"public_request_nonces":   1,
		"audit_log":               1,
	})
	var openConflicts int
	if err := fixture.owner.db.QueryRow(`SELECT count(*) FROM fabric_stream_conflicts WHERE project_id=$1 AND conflict_id=$2 AND state='open'`, fixture.owner.projectID, first.ConflictID).Scan(&openConflicts); err != nil || openConflicts != 1 {
		t.Fatalf("open conflict rows = (%d,%v), want (1,nil)", openConflicts, err)
	}
	beforeReplay := afterFirst
	replayed, err := fixture.handler.Handle(context.Background(), raw, syncV2PushProof(t, fixture.owner, raw, arguments.AttachmentRef, 132))
	if err != nil || replayed != first {
		t.Fatalf("conflict replay = (%+v,%v), want %+v", replayed, err, first)
	}
	afterReplay := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	assertTask2ExactRowDeltas(t, beforeReplay, afterReplay, map[string]int{"public_request_nonces": 1, "audit_log": 1})
	changed := arguments
	changed.Operation = operation
	changedTombstone := *operation.Tombstone
	changedTombstone.ExpectedContentDigest = projectstate.Digest("sha256:" + strings.Repeat("e", 64))
	changed.Operation.Tombstone = &changedTombstone
	changedRaw := canonicalSyncV2PushArguments(t, changed)
	beforeChanged := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	_, err = fixture.handler.Handle(context.Background(), changedRaw, syncV2PushProof(t, fixture.owner, changedRaw, changed.AttachmentRef, 133))
	assertSyncV2PushFailure(t, err, "sync_replay_conflict")
	assertTask2MutationDelta(t, beforeChanged, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), 1)
}

func TestSyncV2PushForcedRLSCrossProjectScopeIsolation(t *testing.T) {
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
	}{{first, first.attach(134)}, {second, second.attach(135)}}
	runtimeDB := publicRuntimeDB(t)
	streams := coregit.NewStreamStore(runtimeDB)
	resolver := realBoundResolverForDB(t, first, runtimeDB)
	coordinator, err := NewMutationCoordinator(identity.NewStore(runtimeDB), streams, coregit.NewActivityStore(runtimeDB))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewSyncV2PushHandler(resolver, coordinator, streams)
	if err != nil {
		t.Fatal(err)
	}
	for index, item := range fixtures {
		operation := syncV2PushOperation(item.owner, item.attached.State, uuid.NewString(), uuid.NewString())
		arguments := syncV2PushArguments(item.attached, operation)
		raw := canonicalSyncV2PushArguments(t, arguments)
		seed := sha256.Sum256([]byte(item.owner.projectID))
		beforeFirst := task2MutationSnapshot(t, first.db, first.projectID)
		beforeSecond := task2MutationSnapshot(t, second.db, second.projectID)
		got, err := handler.Handle(context.Background(), raw, signedBoundProof(t, first.fabricID, "wormhole.sync.push", raw, arguments.AttachmentRef, item.owner.transport.OccurredAt, bytesOf(byte(136+index), 32), seed[:]))
		if err != nil {
			t.Fatalf("project %d push: %v", index, err)
		}
		if result, ok := got.(SyncPushAppliedV2Result); !ok || result.OperationID != operation.ID {
			t.Fatalf("project %d result = (%T)%+v", index, got, got)
		}
		afterFirst := task2MutationSnapshot(t, first.db, first.projectID)
		afterSecond := task2MutationSnapshot(t, second.db, second.projectID)
		if item.owner == first {
			if reflect.DeepEqual(afterFirst, beforeFirst) || !reflect.DeepEqual(afterSecond, beforeSecond) {
				t.Fatal("first-project push crossed RLS scope")
			}
		} else if reflect.DeepEqual(afterSecond, beforeSecond) || !reflect.DeepEqual(afterFirst, beforeFirst) {
			t.Fatal("second-project push crossed RLS scope")
		}
	}
}

func executeAuthorizedSyncV2Push(ctx context.Context, fixture *syncV2PushFixture, authorized PublicMutationAuthority, arguments SyncPushV2Args, raw json.RawMessage) (coregit.StreamTransition, error) {
	var transition coregit.StreamTransition
	err := fixture.coordinator.ExecutePublic(ctx, authorized, "sync.push", bytes.Clone(raw), func(ctx context.Context, tx *sql.Tx, verified VerifiedMutation) error {
		var applyErr error
		transition, applyErr = fixture.streams.ApplyPublicOperationInTx(ctx, tx, verified.Scope, coregit.ApplyPublicOperationInput{
			Attachment:   verified.Attachment,
			Precondition: syncMutationPrecondition(arguments.SyncV2Scope),
			Operation:    arguments.Operation,
		})
		return applyErr
	})
	return transition, err
}

func TestSyncV2PushConcurrentExactOperationDifferentNoncesConverges(t *testing.T) {
	fixture := newSyncV2PushFixture(t, 140)
	operation := syncV2PushOperation(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString())
	arguments := syncV2PushArguments(fixture.attached, operation)
	raw := canonicalSyncV2PushArguments(t, arguments)
	beforeAuthorization := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	authorized := make([]PublicMutationAuthority, 2)
	for index, nonce := range []byte{141, 142} {
		var err error
		authorized[index], err = fixture.resolver.AuthorizeMutation(context.Background(), "wormhole.sync.push", raw, arguments.SyncV2Scope, syncV2PushProof(t, fixture.owner, raw, arguments.AttachmentRef, nonce))
		if err != nil {
			t.Fatalf("AuthorizeMutation %d: %v", index, err)
		}
	}
	afterAuthorization := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	assertTask2ExactRowDeltas(t, beforeAuthorization, afterAuthorization, map[string]int{"public_request_nonces": 2})
	before := afterAuthorization
	results := make([]coregit.StreamTransition, 2)
	errs := raceAtRealAttachmentLock(t, fixture.owner.db, fixture.coordinator, coregit.AttachmentLookup{
		ProjectID: fixture.owner.projectID, FabricInstanceID: fixture.owner.fabricID, AttachmentRef: fixture.attached.Attachment.AttachmentRef,
	}, []func() error{
		func() error {
			var err error
			results[0], err = executeAuthorizedSyncV2Push(context.Background(), fixture, authorized[0], arguments, raw)
			return err
		},
		func() error {
			var err error
			results[1], err = executeAuthorizedSyncV2Push(context.Background(), fixture, authorized[1], arguments, raw)
			return err
		},
	})
	for index, err := range errs {
		if err != nil {
			t.Fatalf("concurrent exact %d: %v", index, err)
		}
	}
	if !reflect.DeepEqual(results[0], results[1]) || results[0].Version != 1 || results[0].ConflictID != "" {
		t.Fatalf("concurrent exact results = %+v / %+v", results[0], results[1])
	}
	after := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	assertTask2ExactRowChanges(t, before, after,
		map[string]int{"fabric_stream_versions": 1, "fabric_stream_requests": 1, "audit_log": 2},
		map[string]int{"fabric_streams": 1})
}

func TestSyncV2PushConcurrentChangedBytesSameOperationIDHasOneReplayConflict(t *testing.T) {
	fixture := newSyncV2PushFixture(t, 143)
	operationID := uuid.NewString()
	arguments := []SyncPushV2Args{
		syncV2PushArguments(fixture.attached, syncV2PushOperation(fixture.owner, fixture.attached.State, operationID, uuid.NewString())),
		syncV2PushArguments(fixture.attached, syncV2PushOperation(fixture.owner, fixture.attached.State, operationID, uuid.NewString())),
	}
	raw := []json.RawMessage{canonicalSyncV2PushArguments(t, arguments[0]), canonicalSyncV2PushArguments(t, arguments[1])}
	beforeAuthorization := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	authorized := make([]PublicMutationAuthority, 2)
	for index := range authorized {
		var err error
		authorized[index], err = fixture.resolver.AuthorizeMutation(context.Background(), "wormhole.sync.push", raw[index], arguments[index].SyncV2Scope, syncV2PushProof(t, fixture.owner, raw[index], arguments[index].AttachmentRef, byte(144+index)))
		if err != nil {
			t.Fatal(err)
		}
	}
	afterAuthorization := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	assertTask2ExactRowDeltas(t, beforeAuthorization, afterAuthorization, map[string]int{"public_request_nonces": 2})
	before := afterAuthorization
	results := make([]coregit.StreamTransition, 2)
	errs := raceAtRealAttachmentLock(t, fixture.owner.db, fixture.coordinator, coregit.AttachmentLookup{
		ProjectID: fixture.owner.projectID, FabricInstanceID: fixture.owner.fabricID, AttachmentRef: fixture.attached.Attachment.AttachmentRef,
	}, []func() error{
		func() error {
			var err error
			results[0], err = executeAuthorizedSyncV2Push(context.Background(), fixture, authorized[0], arguments[0], raw[0])
			return err
		},
		func() error {
			var err error
			results[1], err = executeAuthorizedSyncV2Push(context.Background(), fixture, authorized[1], arguments[1], raw[1])
			return err
		},
	})
	successes, replays := 0, 0
	for index, err := range errs {
		switch {
		case err == nil:
			successes++
			if results[index].Version != 1 {
				t.Fatalf("winner transition = %+v", results[index])
			}
		case errors.Is(err, coregit.ErrOperationReplay):
			replays++
			if syncMutationErrorCode(err) != "sync_replay_conflict" {
				t.Fatalf("replay safe mapping = %q", syncMutationErrorCode(err))
			}
		default:
			t.Fatalf("race error %d = %v", index, err)
		}
	}
	if successes != 1 || replays != 1 {
		t.Fatalf("race outcomes successes=%d replays=%d", successes, replays)
	}
	after := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	assertTask2ExactRowChanges(t, before, after,
		map[string]int{"fabric_stream_versions": 1, "fabric_stream_requests": 1, "audit_log": 1},
		map[string]int{"fabric_streams": 1})
}

func TestSyncV2PushConcurrentSameNonceAuthorizesOnce(t *testing.T) {
	fixture := newSyncV2PushFixture(t, 146)
	operation := syncV2PushOperation(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString())
	arguments := syncV2PushArguments(fixture.attached, operation)
	raw := canonicalSyncV2PushArguments(t, arguments)
	proof := syncV2PushProof(t, fixture.owner, raw, arguments.AttachmentRef, 147)
	before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	results := make([]any, 2)
	errs := raceAtRealAttachmentLock(t, fixture.owner.db, fixture.coordinator, coregit.AttachmentLookup{
		ProjectID: fixture.owner.projectID, FabricInstanceID: fixture.owner.fabricID, AttachmentRef: fixture.attached.Attachment.AttachmentRef,
	}, []func() error{
		func() error {
			var err error
			results[0], err = fixture.handler.Handle(context.Background(), raw, proof)
			return err
		},
		func() error {
			var err error
			results[1], err = fixture.handler.Handle(context.Background(), raw, proof)
			return err
		},
	})
	successes, authenticationFailures := 0, 0
	for index, err := range errs {
		switch {
		case err == nil:
			successes++
			if _, ok := results[index].(SyncPushAppliedV2Result); !ok {
				t.Fatalf("winner result type = %T", results[index])
			}
		case err.Error() == `{"code":"authentication_failed","operation":"wormhole.sync.push"}`:
			authenticationFailures++
		default:
			t.Fatalf("same-nonce race error %d = %v", index, err)
		}
	}
	if successes != 1 || authenticationFailures != 1 {
		t.Fatalf("same-nonce outcomes success=%d auth=%d", successes, authenticationFailures)
	}
	after := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	for table, delta := range map[string]int{"fabric_stream_versions": 1, "fabric_stream_requests": 1, "public_request_nonces": 1, "audit_log": 1} {
		if len(after[table])-len(before[table]) != delta {
			t.Errorf("%s delta=%d, want %d", table, len(after[table])-len(before[table]), delta)
		}
	}
}

type syncV2ConflictFixture struct {
	*syncV2PushFixture
	handler           *SyncV2ConflictHandler
	conflict          SyncPushConflictV2Result
	conflictOperation projectstate.OperationV1
}

func newSyncV2ConflictHandlerFixture(t *testing.T, attachNonce byte) *syncV2ConflictFixture {
	t.Helper()
	push := newSyncV2PushFixture(t, attachNonce)
	handler, err := NewSyncV2ConflictHandler(push.resolver, push.coordinator, push.streams)
	if err != nil {
		t.Fatal(err)
	}
	return &syncV2ConflictFixture{syncV2PushFixture: push, handler: handler}
}

func newSyncV2ConflictFixture(t *testing.T, attachNonce, pushNonce byte) *syncV2ConflictFixture {
	t.Helper()
	fixture := newSyncV2ConflictHandlerFixture(t, attachNonce)
	fixture.seedDurableConflict(t, pushNonce)
	return fixture
}

func (f *syncV2ConflictFixture) seedDurableConflict(t *testing.T, nonce byte) {
	t.Helper()
	operation := projectstate.OperationV1{
		SchemaVersion: 1, ID: uuid.NewString(), Kind: projectstate.OperationTombstone,
		ExpectedViewDigest: f.attached.State.Live.Digest,
		Actor: types.ActorEnvelope{
			ActorKind: types.ActorHuman, HumanPrincipalID: f.owner.actor.ID,
			Assurance: types.AssuranceLocal, OccurredAt: f.owner.transport.OccurredAt,
		},
		Tombstone: &projectstate.TombstoneOperationV1{
			Key:                   projectstate.RecordKey{Kind: "actor", ID: f.owner.actor.ID},
			ExpectedContentDigest: projectstate.Digest("sha256:" + strings.Repeat("f", 64)),
		},
	}
	arguments := syncV2PushArguments(f.attached, operation)
	raw := canonicalSyncV2PushArguments(t, arguments)
	got, err := f.syncV2PushFixture.handler.Handle(context.Background(), raw, syncV2PushProof(t, f.owner, raw, arguments.AttachmentRef, nonce))
	if err != nil {
		t.Fatalf("seed typed push conflict: %v", err)
	}
	conflict, ok := got.(SyncPushConflictV2Result)
	if !ok || !types.CanonicalUUID(conflict.ConflictID) || conflict.OperationID != operation.ID {
		t.Fatalf("seed typed push conflict = (%T)%+v", got, got)
	}
	f.conflict, f.conflictOperation = conflict, operation
}

func syncV2ConflictResolution(f *mutationFixture, state coregit.StreamTransition, operationID, recordID string) projectstate.OperationV1 {
	operation := syncV2PushOperation(f, state, operationID, recordID)
	operation.PutRecord.Record.Actor.DisplayName = "Conflict Resolution Agent"
	return operation
}

func syncV2ConflictArguments(attached InitialAttachResult, conflictID string, resolution projectstate.OperationV1) SyncConflictV2Args {
	return SyncConflictV2Args{
		SyncV2Scope: boundReadArguments(attached, 0).SyncV2Scope,
		ConflictID:  conflictID,
		Resolution:  resolution,
	}
}

func canonicalSyncV2ConflictArguments(t *testing.T, arguments SyncConflictV2Args) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	return canonicalMutationJSON(t, raw)
}

func syncV2ConflictProof(t *testing.T, f *mutationFixture, raw json.RawMessage, attachment string, nonce byte) types.PublicRequestProof {
	t.Helper()
	seed := sha256.Sum256([]byte(f.projectID))
	return signedBoundProof(t, f.fabricID, "wormhole.sync.conflict", raw, attachment, f.transport.OccurredAt, bytesOf(nonce, 32), seed[:])
}

func assertSyncV2ConflictFailure(t *testing.T, err error, code string) {
	t.Helper()
	assertSyncReadFailure(t, err, "wormhole.sync.conflict", code)
}

func resolveSyncV2Conflict(t *testing.T, fixture *syncV2ConflictFixture, arguments SyncConflictV2Args, nonce byte) SyncConflictResolvedV2Result {
	t.Helper()
	raw := canonicalSyncV2ConflictArguments(t, arguments)
	got, err := fixture.handler.Handle(context.Background(), raw, syncV2ConflictProof(t, fixture.owner, raw, arguments.AttachmentRef, nonce))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func executeAuthorizedSyncV2Conflict(ctx context.Context, fixture *syncV2ConflictFixture, authorized PublicMutationAuthority, arguments SyncConflictV2Args, raw json.RawMessage) (coregit.StreamTransition, error) {
	var transition coregit.StreamTransition
	err := fixture.coordinator.ExecutePublic(ctx, authorized, "sync.conflict", bytes.Clone(raw), func(ctx context.Context, tx *sql.Tx, verified VerifiedMutation) error {
		var resolveErr error
		transition, resolveErr = fixture.streams.ResolveConflictInTx(ctx, tx, verified.Scope, coregit.ResolveStreamConflictInput{
			Attachment: verified.Attachment, ConflictID: arguments.ConflictID,
			Precondition: syncMutationPrecondition(arguments.SyncV2Scope), Resolution: arguments.Resolution,
		})
		return resolveErr
	})
	return transition, err
}

func TestSyncV2ConflictConstructorFailsClosed(t *testing.T) {
	db := testDB(t)
	streams := coregit.NewStreamStore(db)
	coordinator, err := NewMutationCoordinator(identity.NewStore(db), streams, coregit.NewActivityStore(db))
	if err != nil {
		t.Fatal(err)
	}
	fabricID := uuid.NewString()
	verifier, err := NewPublicProofVerifier(fabricID, func() time.Time { return time.Now().UTC() })
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewPublicBoundProofResolver(fabricID, identity.NewStore(db), streams, verifier)
	if err != nil {
		t.Fatal(err)
	}
	for name, dependencies := range map[string]struct {
		resolver    *PublicBoundProofResolver
		coordinator *MutationCoordinator
		streams     *coregit.StreamStore
	}{
		"resolver":    {coordinator: coordinator, streams: streams},
		"coordinator": {resolver: resolver, streams: streams},
		"streams":     {resolver: resolver, coordinator: coordinator},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewSyncV2ConflictHandler(dependencies.resolver, dependencies.coordinator, dependencies.streams); !errors.Is(err, identity.ErrInvalidPublicIdentity) {
				t.Fatalf("constructor error = %v, want ErrInvalidPublicIdentity", err)
			}
		})
	}
	var zero *SyncV2ConflictHandler
	if _, err := zero.Handle(context.Background(), nil, types.PublicRequestProof{}); err == nil || err.Error() != `{"code":"internal_error","operation":"wormhole.sync.conflict"}` {
		t.Fatalf("nil handler error = %v", err)
	}
}

func TestSyncV2ConflictRejectsInvalidArgumentsBeforeAuthorization(t *testing.T) {
	fixture := newSyncV2ConflictHandlerFixture(t, 160)
	resolution := syncV2ConflictResolution(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString())
	validArguments := syncV2ConflictArguments(fixture.attached, uuid.NewString(), resolution)
	validRaw := canonicalSyncV2ConflictArguments(t, validArguments)
	valid := string(validRaw)
	missingConflict, missingResolution := map[string]any{}, map[string]any{}
	if err := json.Unmarshal(validRaw, &missingConflict); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(validRaw, &missingResolution); err != nil {
		t.Fatal(err)
	}
	delete(missingConflict, "conflict_id")
	delete(missingResolution, "resolution")
	missingConflictRaw, _ := json.Marshal(missingConflict)
	missingResolutionRaw, _ := json.Marshal(missingResolution)
	nullConflict, nullResolution := map[string]any{}, map[string]any{}
	_ = json.Unmarshal(validRaw, &nullConflict)
	_ = json.Unmarshal(validRaw, &nullResolution)
	nullConflict["conflict_id"], nullResolution["resolution"] = nil, nil
	nullConflictRaw, _ := json.Marshal(nullConflict)
	nullResolutionRaw, _ := json.Marshal(nullResolution)
	tests := map[string]struct{ raw, code string }{
		"unknown field":         {strings.TrimSuffix(valid, `}`) + `,"project_id":"` + fixture.owner.projectID + `"}`, "invalid_request"},
		"duplicate":             {strings.Replace(valid, `"conflict_id":`, `"conflict_id":"`+uuid.NewString()+`","conflict_id":`, 1), "invalid_request"},
		"missing conflict":      {string(missingConflictRaw), "invalid_request"},
		"missing resolution":    {string(missingResolutionRaw), "invalid_request"},
		"null conflict":         {string(nullConflictRaw), "invalid_request"},
		"null resolution":       {string(nullResolutionRaw), "invalid_request"},
		"trailing":              {valid + `{}`, "invalid_request"},
		"noncanonical":          {strings.Replace(valid, `{`, `{ `, 1), "invalid_request"},
		"wrong version":         {strings.Replace(valid, `"version":2`, `"version":3`, 1), "unknown_version"},
		"noncanonical conflict": {strings.Replace(valid, validArguments.ConflictID, strings.ToUpper(validArguments.ConflictID), 1), "invalid_request"},
		"malformed conflict":    {strings.Replace(valid, validArguments.ConflictID, "not-a-uuid", 1), "invalid_request"},
		"private route":         {strings.TrimSuffix(valid, `}`) + `,"workspace_id":"` + uuid.NewString() + `"}`, "invalid_request"},
		"wrong kind":            {strings.Replace(valid, `"expected_stream_version":0`, `"expected_stream_version":"zero"`, 1), "invalid_request"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
			_, err := fixture.handler.Handle(context.Background(), json.RawMessage(test.raw), types.PublicRequestProof{})
			assertSyncV2ConflictFailure(t, err, test.code)
			assertTask2MutationDelta(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), 0)
		})
	}

	unknown := validArguments
	unknown.AttachmentRef = uuid.NewString()
	unknownRaw := canonicalSyncV2ConflictArguments(t, unknown)
	beforeUnknown := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	_, err := fixture.handler.Handle(context.Background(), unknownRaw, syncV2ConflictProof(t, fixture.owner, unknownRaw, unknown.AttachmentRef, 161))
	assertSyncV2ConflictFailure(t, err, "attachment_not_found")
	assertTask2MutationDelta(t, beforeUnknown, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), 0)

	if _, err := fixture.owner.db.Exec(`UPDATE fabric_workspace_stream_bindings SET writable=false,detached_at=now() WHERE project_id=$1 AND attachment_ref=$2`, fixture.owner.projectID, fixture.attached.Attachment.AttachmentRef); err != nil {
		t.Fatal(err)
	}
	beforeDetached := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	_, err = fixture.handler.Handle(context.Background(), validRaw, syncV2ConflictProof(t, fixture.owner, validRaw, validArguments.AttachmentRef, 162))
	assertSyncV2ConflictFailure(t, err, "attachment_not_found")
	assertTask2MutationDelta(t, beforeDetached, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), 0)
}

func TestSyncV2ConflictBurnsNonceForAuthenticatedDenialsAndDomainFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SyncConflictV2Args)
		code   string
	}{
		{"wrong signed scope", func(arguments *SyncConflictV2Args) { arguments.BaseCommitSHA = strings.Repeat("b", 40) }, "sync_precondition_failed"},
		{"wrong stable actor", func(arguments *SyncConflictV2Args) { arguments.Resolution.Actor.HumanPrincipalID = uuid.NewString() }, "permission_denied"},
		{"malformed resolution", func(arguments *SyncConflictV2Args) { arguments.Resolution.SchemaVersion = 2 }, "sync_precondition_failed"},
		{"malformed resolution id", func(arguments *SyncConflictV2Args) { arguments.Resolution.ID = "not-a-uuid" }, "sync_precondition_failed"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSyncV2ConflictFixture(t, byte(163+index*3), byte(164+index*3))
			arguments := syncV2ConflictArguments(fixture.attached, fixture.conflict.ConflictID, syncV2ConflictResolution(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString()))
			test.mutate(&arguments)
			raw := canonicalSyncV2ConflictArguments(t, arguments)
			before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
			_, err := fixture.handler.Handle(context.Background(), raw, syncV2ConflictProof(t, fixture.owner, raw, arguments.AttachmentRef, byte(165+index*3)))
			assertSyncV2ConflictFailure(t, err, test.code)
			assertTask2ExactRowDeltas(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), map[string]int{"public_request_nonces": 1})
		})
	}

	t.Run("unknown conflict", func(t *testing.T) {
		fixture := newSyncV2ConflictFixture(t, 172, 173)
		arguments := syncV2ConflictArguments(fixture.attached, uuid.NewString(), syncV2ConflictResolution(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString()))
		raw := canonicalSyncV2ConflictArguments(t, arguments)
		before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		_, err := fixture.handler.Handle(context.Background(), raw, syncV2ConflictProof(t, fixture.owner, raw, arguments.AttachmentRef, 174))
		assertSyncV2ConflictFailure(t, err, "sync_conflict")
		assertTask2ExactRowDeltas(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), map[string]int{"public_request_nonces": 1})
	})

	t.Run("cross route conflict", func(t *testing.T) {
		fixture := newSyncV2ConflictFixture(t, 175, 176)
		other := newSyncV2ConflictFixture(t, 177, 178)
		arguments := syncV2ConflictArguments(fixture.attached, other.conflict.ConflictID, syncV2ConflictResolution(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString()))
		raw := canonicalSyncV2ConflictArguments(t, arguments)
		before, otherBefore := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), task2MutationSnapshot(t, other.owner.db, other.owner.projectID)
		_, err := fixture.handler.Handle(context.Background(), raw, syncV2ConflictProof(t, fixture.owner, raw, arguments.AttachmentRef, 179))
		assertSyncV2ConflictFailure(t, err, "sync_conflict")
		assertTask2ExactRowDeltas(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), map[string]int{"public_request_nonces": 1})
		assertTask2MutationDelta(t, otherBefore, task2MutationSnapshot(t, other.owner.db, other.owner.projectID), 0)
	})

	t.Run("reachable nested conflict", func(t *testing.T) {
		fixture := newSyncV2ConflictFixture(t, 180, 181)
		resolution := fixture.conflictOperation
		resolution.ID = uuid.NewString()
		resolutionTombstone := *fixture.conflictOperation.Tombstone
		resolutionTombstone.ExpectedContentDigest = projectstate.Digest("sha256:" + strings.Repeat("e", 64))
		resolution.Tombstone = &resolutionTombstone
		arguments := syncV2ConflictArguments(fixture.attached, fixture.conflict.ConflictID, resolution)
		raw := canonicalSyncV2ConflictArguments(t, arguments)
		before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		_, err := fixture.handler.Handle(context.Background(), raw, syncV2ConflictProof(t, fixture.owner, raw, arguments.AttachmentRef, 182))
		assertSyncV2ConflictFailure(t, err, "sync_precondition_failed")
		assertTask2ExactRowDeltas(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), map[string]int{"public_request_nonces": 1})
	})

	t.Run("already resolved with other operation", func(t *testing.T) {
		fixture := newSyncV2ConflictFixture(t, 183, 184)
		first := syncV2ConflictArguments(fixture.attached, fixture.conflict.ConflictID, syncV2ConflictResolution(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString()))
		_ = resolveSyncV2Conflict(t, fixture, first, 185)
		changed := first
		changed.Resolution = syncV2ConflictResolution(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString())
		raw := canonicalSyncV2ConflictArguments(t, changed)
		before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		_, err := fixture.handler.Handle(context.Background(), raw, syncV2ConflictProof(t, fixture.owner, raw, changed.AttachmentRef, 186))
		assertSyncV2ConflictFailure(t, err, "sync_replay_conflict")
		assertTask2ExactRowDeltas(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), map[string]int{"public_request_nonces": 1})
	})

	t.Run("stored resolution request corruption", func(t *testing.T) {
		fixture := newSyncV2ConflictFixture(t, 187, 188)
		arguments := syncV2ConflictArguments(fixture.attached, fixture.conflict.ConflictID, syncV2ConflictResolution(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString()))
		_ = resolveSyncV2Conflict(t, fixture, arguments, 189)
		corruptSyncV2PushRequest(t, fixture.owner.db, fixture.owner.projectID, arguments.Resolution.ID)
		raw := canonicalSyncV2ConflictArguments(t, arguments)
		before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		_, err := fixture.handler.Handle(context.Background(), raw, syncV2ConflictProof(t, fixture.owner, raw, arguments.AttachmentRef, 190))
		assertSyncV2ConflictFailure(t, err, "internal_error")
		assertTask2ExactRowDeltas(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), map[string]int{"public_request_nonces": 1})
	})

	t.Run("audit failure", func(t *testing.T) {
		fixture := newSyncV2ConflictFixture(t, 191, 192)
		installAuditFailure(t, fixture.owner.db, fixture.owner.projectID, "sync.conflict")
		arguments := syncV2ConflictArguments(fixture.attached, fixture.conflict.ConflictID, syncV2ConflictResolution(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString()))
		raw := canonicalSyncV2ConflictArguments(t, arguments)
		before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		_, err := fixture.handler.Handle(context.Background(), raw, syncV2ConflictProof(t, fixture.owner, raw, arguments.AttachmentRef, 193))
		assertSyncV2ConflictFailure(t, err, "internal_error")
		assertTask2ExactRowDeltas(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), map[string]int{"public_request_nonces": 1})
	})

	t.Run("internal SQL failure", func(t *testing.T) {
		fixture := newSyncV2ConflictFixture(t, 194, 195)
		installSyncV2PushVersionFailure(t, fixture.owner.db, fixture.owner.projectID)
		arguments := syncV2ConflictArguments(fixture.attached, fixture.conflict.ConflictID, syncV2ConflictResolution(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString()))
		raw := canonicalSyncV2ConflictArguments(t, arguments)
		before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		_, err := fixture.handler.Handle(context.Background(), raw, syncV2ConflictProof(t, fixture.owner, raw, arguments.AttachmentRef, 196))
		assertSyncV2ConflictFailure(t, err, "internal_error")
		assertTask2ExactRowDeltas(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), map[string]int{"public_request_nonces": 1})
	})

	t.Run("post authorization detach", func(t *testing.T) {
		fixture := newSyncV2ConflictFixture(t, 197, 198)
		arguments := syncV2ConflictArguments(fixture.attached, fixture.conflict.ConflictID, syncV2ConflictResolution(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString()))
		raw := canonicalSyncV2ConflictArguments(t, arguments)
		beforeAuthorization := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		authorized, err := fixture.resolver.AuthorizeMutation(context.Background(), "wormhole.sync.conflict", raw, arguments.SyncV2Scope, syncV2ConflictProof(t, fixture.owner, raw, arguments.AttachmentRef, 199))
		if err != nil {
			t.Fatal(err)
		}
		assertTask2ExactRowDeltas(t, beforeAuthorization, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), map[string]int{"public_request_nonces": 1})
		if _, err := fixture.owner.db.Exec(`UPDATE fabric_workspace_stream_bindings SET writable=false,detached_at=now() WHERE project_id=$1 AND attachment_ref=$2`, fixture.owner.projectID, fixture.attached.Attachment.AttachmentRef); err != nil {
			t.Fatal(err)
		}
		beforeExecute := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		_, err = executeAuthorizedSyncV2Conflict(context.Background(), fixture, authorized, arguments, raw)
		if !errors.Is(err, coregit.ErrStreamNotFound) {
			t.Fatalf("ExecutePublic error = %v, want ErrStreamNotFound", err)
		}
		assertTask2MutationDelta(t, beforeExecute, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), 0)
	})
}

func TestSyncV2ConflictSafeFailureMappingAndRedaction(t *testing.T) {
	for _, test := range []struct {
		err  error
		code string
	}{
		{identity.ErrPublicAuthentication, "authentication_failed"},
		{identity.ErrPublicNonceReplay, "authentication_failed"},
		{identity.ErrInvalidPublicIdentity, "authentication_failed"},
		{coregit.ErrStreamNotFound, "attachment_not_found"},
		{coregit.ErrStreamActor, "permission_denied"},
		{coregit.ErrStreamPrecondition, "sync_precondition_failed"},
		{coregit.ErrStreamConflict, "sync_conflict"},
		{coregit.ErrOperationReplay, "sync_replay_conflict"},
		{projectstate.ErrInvalidActorEnvelope, "sync_precondition_failed"},
		{errors.New("pq: secret SQL /private/path attachment resolution body"), "internal_error"},
	} {
		if got := syncMutationErrorCode(test.err); got != test.code {
			t.Errorf("syncMutationErrorCode(%v) = %q, want %q", test.err, got, test.code)
		}
		err := syncMutationFailure("wormhole.sync.conflict", test.code)
		want := `{"code":"` + test.code + `","operation":"wormhole.sync.conflict"}`
		if err == nil || err.Error() != want {
			t.Errorf("safe error = %v, want %s", err, want)
		}
		for _, secret := range []string{"secret SQL", "/private/path", "attachment-ref-secret", "resolution body", "pq:"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("safe error leaked %q: %s", secret, err)
			}
		}
	}
}

func TestSyncV2ConflictResolvesDurableConflictWithExactOperationEvidenceAndAudit(t *testing.T) {
	fixture := newSyncV2ConflictFixture(t, 200, 201)
	resolution := syncV2ConflictResolution(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString())
	arguments := syncV2ConflictArguments(fixture.attached, fixture.conflict.ConflictID, resolution)
	raw := canonicalSyncV2ConflictArguments(t, arguments)
	wantOperation, err := projectstate.CanonicalOperation(resolution)
	if err != nil {
		t.Fatal(err)
	}
	wantPortableActor, err := projectstate.CanonicalJSON(resolution.Actor)
	if err != nil {
		t.Fatal(err)
	}
	wantAuditActor, err := json.Marshal(fixture.owner.transport)
	if err != nil {
		t.Fatal(err)
	}
	before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	got, err := fixture.handler.Handle(context.Background(), raw, syncV2ConflictProof(t, fixture.owner, raw, arguments.AttachmentRef, 202))
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 2 || got.Status != "resolved" || got.ConflictID != fixture.conflict.ConflictID || got.OperationID != resolution.ID || got.StreamVersion != 1 || got.LiveTreeDigest == fixture.attached.State.Live.Digest {
		t.Fatalf("resolved result = %+v", got)
	}
	var state, resolutionOperationID, auditAction string
	var resolutionVersion int64
	var requestOperation, versionOperation, requestActor, versionActor, auditPayload, auditActor []byte
	if err := fixture.owner.db.QueryRow(`SELECT c.state,c.resolution_operation_id::text,c.resolution_version,
		r.canonical_operation_json,v.canonical_operation_json,r.actor_envelope_json,v.actor_envelope_json,
		a.action,a.canonical_payload_json::text::bytea,a.actor_envelope_json::text::bytea
		FROM fabric_stream_conflicts c
		JOIN fabric_stream_requests r ON r.project_id=c.project_id AND r.fabric_instance_id=c.fabric_instance_id AND r.stream_id=c.stream_id AND r.operation_id=c.resolution_operation_id
		JOIN fabric_stream_versions v ON v.project_id=c.project_id AND v.fabric_instance_id=c.fabric_instance_id AND v.stream_id=c.stream_id AND v.version=c.resolution_version AND v.operation_id=c.resolution_operation_id
		JOIN audit_log a ON a.project_id=c.project_id AND a.action='sync.conflict'
		WHERE c.project_id=$1 AND c.conflict_id=$2`, fixture.owner.projectID, fixture.conflict.ConflictID).Scan(
		&state, &resolutionOperationID, &resolutionVersion, &requestOperation, &versionOperation, &requestActor, &versionActor, &auditAction, &auditPayload, &auditActor); err != nil {
		t.Fatal(err)
	}
	if state != "resolved" || resolutionOperationID != resolution.ID || resolutionVersion != got.StreamVersion || !bytes.Equal(requestOperation, wantOperation) || !bytes.Equal(versionOperation, wantOperation) || !bytes.Equal(requestActor, wantPortableActor) || !bytes.Equal(versionActor, wantPortableActor) || auditAction != "sync.conflict" || !bytes.Equal(auditPayload, raw) || !bytes.Equal(auditActor, wantAuditActor) {
		t.Fatal("stored conflict resolution or audit evidence changed")
	}
	after := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	assertTask2ExactRowChanges(t, before, after,
		map[string]int{"fabric_stream_versions": 1, "fabric_stream_requests": 1, "public_request_nonces": 1, "audit_log": 1},
		map[string]int{"fabric_streams": 1, "fabric_stream_conflicts": 1})
}

func TestSyncV2ConflictExactReplayReturnsRecordedResolution(t *testing.T) {
	fixture := newSyncV2ConflictFixture(t, 203, 204)
	arguments := syncV2ConflictArguments(fixture.attached, fixture.conflict.ConflictID, syncV2ConflictResolution(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString()))
	first := resolveSyncV2Conflict(t, fixture, arguments, 205)
	before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	replayed := resolveSyncV2Conflict(t, fixture, arguments, 206)
	if replayed != first {
		t.Fatalf("exact conflict replay = %+v, want %+v", replayed, first)
	}
	after := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	assertTask2ExactRowDeltas(t, before, after, map[string]int{"public_request_nonces": 1, "audit_log": 1})
}

func TestSyncV2ConflictRejectsHistoricalAppliedOperationFromAnotherConflict(t *testing.T) {
	fixture := newSyncV2ConflictHandlerFixture(t, 240)
	historicalState := fixture.attached.State
	a := syncV2PushOperation(fixture.owner, historicalState, uuid.NewString(), uuid.NewString())
	historicalScope := syncV2PushArguments(fixture.attached, a).SyncV2Scope
	tx, err := fixture.owner.coordinator.identity.BeginProjectTx(context.Background(), fixture.owner.projectID)
	if err != nil {
		t.Fatal(err)
	}
	appliedA, err := fixture.streams.ApplyPublicOperationInTx(context.Background(), tx,
		types.ActorScope{ProjectID: fixture.owner.projectID, Actor: fixture.owner.transport}, coregit.ApplyPublicOperationInput{
			Attachment: fixture.attached.Attachment, Precondition: syncMutationPrecondition(historicalScope), Operation: a,
		})
	if err == nil {
		err = tx.Commit()
	} else {
		_ = tx.Rollback()
	}
	if err != nil {
		t.Fatal(err)
	}
	acceptedOperation := syncV2PushOperation(fixture.owner, coregit.StreamTransition{Live: appliedA.Accepted}, uuid.NewString(), uuid.NewString())
	accepted, err := projectstate.ApplyOperation(appliedA.Accepted, acceptedOperation)
	if err != nil {
		t.Fatal(err)
	}
	acceptedTree, err := projectstate.EncodeTree(accepted)
	if err != nil {
		t.Fatal(err)
	}
	tx, err = fixture.owner.coordinator.identity.BeginProjectTx(context.Background(), fixture.owner.projectID)
	if err != nil {
		t.Fatal(err)
	}
	conflict, err := fixture.streams.AdvanceAcceptedObservedRefInTx(context.Background(), tx,
		types.ActorScope{ProjectID: fixture.owner.projectID, Actor: fixture.owner.transport}, coregit.AdvanceAcceptedInput{
			Key: fixture.attached.Attachment.Key,
			Ref: coregit.RefObservation{Repository: fixture.owner.repository, RefName: fixture.owner.observation.RefName,
				CommitSHA: strings.Repeat("b", 40), ObservedAt: fixture.owner.observation.ObservedAt.Add(time.Minute)},
			Tree: acceptedTree, ExpectedVersion: appliedA.Version, ExpectedAcceptedCommitSHA: appliedA.AcceptedCommitSHA,
			ExpectedAcceptedTreeDigest: appliedA.Accepted.Digest, ExpectedLiveTreeDigest: appliedA.Live.Digest,
		})
	if err == nil {
		err = tx.Commit()
	} else {
		_ = tx.Rollback()
	}
	if err != nil || !types.CanonicalUUID(conflict.ConflictID) {
		t.Fatalf("seed accepted/live conflict = (%+v,%v)", conflict, err)
	}
	historical := syncV2ConflictArguments(fixture.attached, conflict.ConflictID, a)
	historical.SyncV2Scope = historicalScope
	historicalRaw := canonicalSyncV2ConflictArguments(t, historical)
	before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	_, err = fixture.handler.Handle(context.Background(), historicalRaw, syncV2ConflictProof(t, fixture.owner, historicalRaw, historical.AttachmentRef, 241))
	assertSyncV2ConflictFailure(t, err, "sync_replay_conflict")
	assertTask2ExactRowDeltas(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), map[string]int{"public_request_nonces": 1})
	var state string
	if err := fixture.owner.db.QueryRow(`SELECT state FROM fabric_stream_conflicts WHERE project_id=$1 AND conflict_id=$2`, fixture.owner.projectID, conflict.ConflictID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "open" {
		t.Fatalf("conflict state=%s", state)
	}
	fresh := syncV2ConflictResolution(fixture.owner, conflict, uuid.NewString(), uuid.NewString())
	current := fixture.attached
	current.State = conflict
	resolvedArgs := syncV2ConflictArguments(current, conflict.ConflictID, fresh)
	first := resolveSyncV2Conflict(t, fixture, resolvedArgs, 242)
	if first.Status != "resolved" || first.ConflictID != conflict.ConflictID || first.OperationID != fresh.ID || first.StreamVersion != conflict.Version+1 {
		t.Fatalf("fresh resolution=%+v", first)
	}
	beforeReplay := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	replay := resolveSyncV2Conflict(t, fixture, resolvedArgs, 243)
	if replay != first {
		t.Fatalf("fresh resolution replay=%+v, first=%+v", replay, first)
	}
	assertTask2ExactRowDeltas(t, beforeReplay, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), map[string]int{"public_request_nonces": 1, "audit_log": 1})
}

func TestSyncV2ConflictChangedResolutionBytesReturnSafeReplayConflict(t *testing.T) {
	fixture := newSyncV2ConflictFixture(t, 207, 208)
	arguments := syncV2ConflictArguments(fixture.attached, fixture.conflict.ConflictID, syncV2ConflictResolution(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString()))
	_ = resolveSyncV2Conflict(t, fixture, arguments, 209)
	changed := arguments
	changed.Resolution = arguments.Resolution
	changedRecord := *arguments.Resolution.PutRecord.Record.Actor
	changedRecord.DisplayName = "Changed conflict resolution bytes"
	changed.Resolution.PutRecord = &projectstate.PutRecordV1{Record: projectstate.RecordValueV1{Actor: &changedRecord}}
	raw := canonicalSyncV2ConflictArguments(t, changed)
	before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	_, err := fixture.handler.Handle(context.Background(), raw, syncV2ConflictProof(t, fixture.owner, raw, changed.AttachmentRef, 210))
	assertSyncV2ConflictFailure(t, err, "sync_replay_conflict")
	after := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	assertTask2ExactRowDeltas(t, before, after, map[string]int{"public_request_nonces": 1})
}

func TestSyncV2ConflictAgentStableAttributionAndForcedRLSIsolation(t *testing.T) {
	t.Run("agent stable attribution", func(t *testing.T) {
		push, session, agentID := newSyncV2PushAgentFixture(t, 211, true)
		fixture := &syncV2ConflictFixture{syncV2PushFixture: push}
		var err error
		fixture.handler, err = NewSyncV2ConflictHandler(push.resolver, push.coordinator, push.streams)
		if err != nil {
			t.Fatal(err)
		}
		fixture.seedDurableConflict(t, 212)
		resolution := syncV2ConflictResolution(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString())
		resolution.Actor = types.ActorEnvelope{
			ActorKind: types.ActorAgent, AgentID: agentID, AccountableHumanID: fixture.owner.actor.ID,
			SessionID: uuid.NewString(), HarnessName: "historical", HarnessVersion: "0", ModelName: "old", ModelVersion: "1",
			Assurance: types.AssuranceLocal, OccurredAt: fixture.owner.transport.OccurredAt.Add(-time.Minute),
		}
		arguments := syncV2ConflictArguments(fixture.attached, fixture.conflict.ConflictID, resolution)
		raw := canonicalSyncV2ConflictArguments(t, arguments)
		seed := sha256.Sum256([]byte(fixture.owner.projectID))
		proof := signedBoundSessionProof(t, fixture.owner.fabricID, "wormhole.sync.conflict", raw, arguments.AttachmentRef, session.SessionID, fixture.owner.transport.OccurredAt, bytesOf(213, 32), seed[:])
		got, err := fixture.handler.Handle(context.Background(), raw, proof)
		if err != nil || got.OperationID != resolution.ID {
			t.Fatalf("agent conflict resolution = (%+v,%v)", got, err)
		}
		wantOperation, _ := projectstate.CanonicalOperation(resolution)
		wantActor, _ := projectstate.CanonicalJSON(resolution.Actor)
		var requestOperation, requestActor, auditActor []byte
		if err := fixture.owner.db.QueryRow(`SELECT r.canonical_operation_json,r.actor_envelope_json,a.actor_envelope_json::text::bytea
			FROM fabric_stream_requests r JOIN audit_log a ON a.project_id=r.project_id AND a.action='sync.conflict'
			WHERE r.project_id=$1 AND r.operation_id=$2`, fixture.owner.projectID, resolution.ID).Scan(&requestOperation, &requestActor, &auditActor); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(requestOperation, wantOperation) || !bytes.Equal(requestActor, wantActor) {
			t.Fatal("agent conflict resolution rewrote portable attribution")
		}
		var transport types.ActorEnvelope
		if err := json.Unmarshal(auditActor, &transport); err != nil {
			t.Fatal(err)
		}
		if transport.ActorKind != types.ActorAgent || transport.AgentID != agentID || transport.AccountableHumanID != fixture.owner.actor.ID || transport.SessionID != session.SessionID || transport.HarnessName != session.HarnessName || transport.ModelName != session.ModelName || transport.Assurance != types.AssurancePublicKeyContinuity {
			t.Fatalf("agent conflict audit actor = %+v, session=%+v", transport, session)
		}
	})

	t.Run("forced RLS route isolation", func(t *testing.T) {
		first, second := newMutationFixture(t), newMutationFixture(t)
		oldFabricID := second.fabricID
		if _, err := second.db.Exec(`UPDATE project_repository_bindings SET fabric_instance_id=$1 WHERE project_id=$2 AND fabric_instance_id=$3`, first.fabricID, second.projectID, oldFabricID); err != nil {
			t.Fatal(err)
		}
		second.fabricID = first.fabricID
		owners := []*mutationFixture{first, second}
		runtimeDB := publicRuntimeDB(t)
		streams := coregit.NewStreamStore(runtimeDB)
		resolver := realBoundResolverForDB(t, first, runtimeDB)
		coordinator, err := NewMutationCoordinator(identity.NewStore(runtimeDB), streams, coregit.NewActivityStore(runtimeDB))
		if err != nil {
			t.Fatal(err)
		}
		handler, err := NewSyncV2ConflictHandler(resolver, coordinator, streams)
		if err != nil {
			t.Fatal(err)
		}
		fixtures := make([]*syncV2ConflictFixture, 0, 2)
		for index, owner := range owners {
			attached := owner.attach(byte(214 + index*3))
			push := newSyncV2PushFixtureForAttached(t, owner, attached)
			fixture := &syncV2ConflictFixture{syncV2PushFixture: push, handler: handler}
			fixture.seedDurableConflict(t, byte(215+index*3))
			fixtures = append(fixtures, fixture)
		}
		for index, fixture := range fixtures {
			arguments := syncV2ConflictArguments(fixture.attached, fixture.conflict.ConflictID, syncV2ConflictResolution(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString()))
			raw := canonicalSyncV2ConflictArguments(t, arguments)
			beforeFirst, beforeSecond := task2MutationSnapshot(t, first.db, first.projectID), task2MutationSnapshot(t, second.db, second.projectID)
			seed := sha256.Sum256([]byte(fixture.owner.projectID))
			got, err := handler.Handle(context.Background(), raw, signedBoundProof(t, first.fabricID, "wormhole.sync.conflict", raw, arguments.AttachmentRef, fixture.owner.transport.OccurredAt, bytesOf(byte(216+index*3), 32), seed[:]))
			if err != nil || got.OperationID != arguments.Resolution.ID {
				t.Fatalf("project %d resolution = (%+v,%v)", index, got, err)
			}
			afterFirst, afterSecond := task2MutationSnapshot(t, first.db, first.projectID), task2MutationSnapshot(t, second.db, second.projectID)
			if fixture.owner == first {
				if reflect.DeepEqual(afterFirst, beforeFirst) || !reflect.DeepEqual(afterSecond, beforeSecond) {
					t.Fatal("first-project conflict resolution crossed RLS scope")
				}
			} else if reflect.DeepEqual(afterSecond, beforeSecond) || !reflect.DeepEqual(afterFirst, beforeFirst) {
				t.Fatal("second-project conflict resolution crossed RLS scope")
			}
		}
	})
}

func TestSyncV2ConflictConcurrentExactResolutionConverges(t *testing.T) {
	fixture := newSyncV2ConflictFixture(t, 220, 221)
	arguments := syncV2ConflictArguments(fixture.attached, fixture.conflict.ConflictID, syncV2ConflictResolution(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString()))
	raw := canonicalSyncV2ConflictArguments(t, arguments)
	beforeAuthorization := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	authorized := make([]PublicMutationAuthority, 2)
	for index, nonce := range []byte{222, 223} {
		var err error
		authorized[index], err = fixture.resolver.AuthorizeMutation(context.Background(), "wormhole.sync.conflict", raw, arguments.SyncV2Scope, syncV2ConflictProof(t, fixture.owner, raw, arguments.AttachmentRef, nonce))
		if err != nil {
			t.Fatal(err)
		}
	}
	afterAuthorization := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	assertTask2ExactRowDeltas(t, beforeAuthorization, afterAuthorization, map[string]int{"public_request_nonces": 2})
	results := make([]coregit.StreamTransition, 2)
	errs := raceAtRealAttachmentLock(t, fixture.owner.db, fixture.coordinator, coregit.AttachmentLookup{ProjectID: fixture.owner.projectID, FabricInstanceID: fixture.owner.fabricID, AttachmentRef: fixture.attached.Attachment.AttachmentRef}, []func() error{
		func() error {
			var err error
			results[0], err = executeAuthorizedSyncV2Conflict(context.Background(), fixture, authorized[0], arguments, raw)
			return err
		},
		func() error {
			var err error
			results[1], err = executeAuthorizedSyncV2Conflict(context.Background(), fixture, authorized[1], arguments, raw)
			return err
		},
	})
	if errs[0] != nil || errs[1] != nil || !reflect.DeepEqual(results[0], results[1]) || results[0].Version != 1 || results[0].ConflictID != "" {
		t.Fatalf("exact resolution race = (%+v,%v) / (%+v,%v)", results[0], errs[0], results[1], errs[1])
	}
	after := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	assertTask2ExactRowChanges(t, afterAuthorization, after,
		map[string]int{"fabric_stream_versions": 1, "fabric_stream_requests": 1, "audit_log": 2},
		map[string]int{"fabric_streams": 1, "fabric_stream_conflicts": 1})
}

func TestSyncV2ConflictConcurrentChangedResolutionHasOneWinner(t *testing.T) {
	fixture := newSyncV2ConflictFixture(t, 224, 225)
	operationID, recordID := uuid.NewString(), uuid.NewString()
	firstResolution := syncV2ConflictResolution(fixture.owner, fixture.attached.State, operationID, recordID)
	secondResolution := firstResolution
	changedRecord := *firstResolution.PutRecord.Record.Actor
	changedRecord.DisplayName = "Changed concurrent resolution"
	secondResolution.PutRecord = &projectstate.PutRecordV1{Record: projectstate.RecordValueV1{Actor: &changedRecord}}
	arguments := []SyncConflictV2Args{
		syncV2ConflictArguments(fixture.attached, fixture.conflict.ConflictID, firstResolution),
		syncV2ConflictArguments(fixture.attached, fixture.conflict.ConflictID, secondResolution),
	}
	raw := []json.RawMessage{canonicalSyncV2ConflictArguments(t, arguments[0]), canonicalSyncV2ConflictArguments(t, arguments[1])}
	beforeAuthorization := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	authorized := make([]PublicMutationAuthority, 2)
	for index := range authorized {
		var err error
		authorized[index], err = fixture.resolver.AuthorizeMutation(context.Background(), "wormhole.sync.conflict", raw[index], arguments[index].SyncV2Scope, syncV2ConflictProof(t, fixture.owner, raw[index], arguments[index].AttachmentRef, byte(226+index)))
		if err != nil {
			t.Fatal(err)
		}
	}
	afterAuthorization := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	assertTask2ExactRowDeltas(t, beforeAuthorization, afterAuthorization, map[string]int{"public_request_nonces": 2})
	results := make([]coregit.StreamTransition, 2)
	errs := raceAtRealAttachmentLock(t, fixture.owner.db, fixture.coordinator, coregit.AttachmentLookup{ProjectID: fixture.owner.projectID, FabricInstanceID: fixture.owner.fabricID, AttachmentRef: fixture.attached.Attachment.AttachmentRef}, []func() error{
		func() error {
			var err error
			results[0], err = executeAuthorizedSyncV2Conflict(context.Background(), fixture, authorized[0], arguments[0], raw[0])
			return err
		},
		func() error {
			var err error
			results[1], err = executeAuthorizedSyncV2Conflict(context.Background(), fixture, authorized[1], arguments[1], raw[1])
			return err
		},
	})
	successes, replays := 0, 0
	for index, err := range errs {
		switch {
		case err == nil:
			successes++
			if results[index].Version != 1 {
				t.Fatalf("winner transition = %+v", results[index])
			}
		case errors.Is(err, coregit.ErrOperationReplay):
			replays++
		default:
			t.Fatalf("changed resolution race error %d = %v", index, err)
		}
	}
	if successes != 1 || replays != 1 {
		t.Fatalf("changed resolution race success=%d replay=%d", successes, replays)
	}
	after := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	assertTask2ExactRowChanges(t, afterAuthorization, after,
		map[string]int{"fabric_stream_versions": 1, "fabric_stream_requests": 1, "audit_log": 1},
		map[string]int{"fabric_streams": 1, "fabric_stream_conflicts": 1})
}

func TestSyncV2ConflictConcurrentSameNonceAuthorizesOnce(t *testing.T) {
	fixture := newSyncV2ConflictFixture(t, 228, 229)
	arguments := syncV2ConflictArguments(fixture.attached, fixture.conflict.ConflictID, syncV2ConflictResolution(fixture.owner, fixture.attached.State, uuid.NewString(), uuid.NewString()))
	raw := canonicalSyncV2ConflictArguments(t, arguments)
	proof := syncV2ConflictProof(t, fixture.owner, raw, arguments.AttachmentRef, 230)
	before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	results := make([]SyncConflictResolvedV2Result, 2)
	errs := raceAtRealAttachmentLock(t, fixture.owner.db, fixture.coordinator, coregit.AttachmentLookup{ProjectID: fixture.owner.projectID, FabricInstanceID: fixture.owner.fabricID, AttachmentRef: fixture.attached.Attachment.AttachmentRef}, []func() error{
		func() error {
			var err error
			results[0], err = fixture.handler.Handle(context.Background(), raw, proof)
			return err
		},
		func() error {
			var err error
			results[1], err = fixture.handler.Handle(context.Background(), raw, proof)
			return err
		},
	})
	successes, authenticationFailures := 0, 0
	for index, err := range errs {
		switch {
		case err == nil:
			successes++
			if results[index].Status != "resolved" {
				t.Fatalf("winner result = %+v", results[index])
			}
		case err.Error() == `{"code":"authentication_failed","operation":"wormhole.sync.conflict"}`:
			authenticationFailures++
		default:
			t.Fatalf("same-nonce conflict race error %d = %v", index, err)
		}
	}
	if successes != 1 || authenticationFailures != 1 {
		t.Fatalf("same-nonce conflict outcomes success=%d auth=%d", successes, authenticationFailures)
	}
	after := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	assertTask2ExactRowChanges(t, before, after,
		map[string]int{"fabric_stream_versions": 1, "fabric_stream_requests": 1, "public_request_nonces": 1, "audit_log": 1},
		map[string]int{"fabric_streams": 1, "fabric_stream_conflicts": 1})
}
