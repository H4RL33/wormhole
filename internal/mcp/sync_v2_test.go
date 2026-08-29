package mcp

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	verifier, err := NewPublicProofVerifier("11111111-1111-4111-8111-111111111111", func() time.Time {
		return time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	observer := coregit.CanonicalGitObserver(&coregit.FakeObserver{})
	coordinator := syncAttachCoordinator(&attachCoordinatorStub{})
	policy := SyncAttachPolicySource(&attachPolicySourceStub{})

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
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewSyncV2AttachHandler("11111111-1111-4111-8111-111111111111", "git-observer", test.observer, test.coordinator, test.policy, test.verifier); !errors.Is(err, identity.ErrInvalidPublicIdentity) {
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

	retryProof := signedAttachProof(t, f.fabricID, command.CanonicalRequest, f.transport.OccurredAt, bytesOf(4, 32), seed[:])
	retry, err := handler.Handle(context.Background(), command.CanonicalRequest, retryProof)
	if err != nil || retry != first {
		t.Fatalf("exact retry = %+v, %v; want %+v", retry, err, first)
	}
	afterRetry := mutationCounts(t, f.db, f.projectID)
	for table := range afterFirst {
		want := 0
		if table == "public_request_nonces" {
			want = 1
		}
		if afterRetry[table]-afterFirst[table] != want {
			t.Errorf("%s retry delta = %d, want %d", table, afterRetry[table]-afterFirst[table], want)
		}
	}

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
	afterDenied := mutationCounts(t, f.db, f.projectID)
	for table := range afterRetry {
		want := 0
		if table == "public_request_nonces" {
			want = 1
		}
		if afterDenied[table]-afterRetry[table] != want {
			t.Errorf("%s denied delta = %d, want %d", table, afterDenied[table]-afterRetry[table], want)
		}
	}
	_, err = handler.Handle(context.Background(), changedRaw, deniedProof)
	assertAttachFailure(t, err, "authentication_failed")
	if got := mutationCounts(t, f.db, f.projectID); !reflect.DeepEqual(got, afterDenied) {
		t.Fatalf("reused denied nonce mutated state: before=%v after=%v", afterDenied, got)
	}
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
	before := mutationCounts(t, f.db, f.projectID)
	raw := f.command(10).CanonicalRequest
	seed := sha256.Sum256([]byte(f.projectID))
	proof := signedAttachProof(t, f.fabricID, raw, f.transport.OccurredAt, bytesOf(10, 32), seed[:])
	_, err := handler.Handle(context.Background(), raw, proof)
	assertAttachFailure(t, err, "internal_error")
	after := mutationCounts(t, f.db, f.projectID)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("audit failure changed state: before=%v after=%v", before, after)
	}
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
