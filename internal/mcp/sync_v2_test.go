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
	canonical, err := projectstate.CanonicalJSON(arguments)
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
	canonical, err := projectstate.CanonicalJSON(arguments)
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
