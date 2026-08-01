package projectstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestPublicationConfigurationOriginRaceReturnsErrGitOriginChangedWithoutMutation(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/one.git")
	outside, err := observePublicationOrigin(context.Background(), fixture.repository.root)
	if err != nil {
		t.Fatal(err)
	}
	inside := outside
	inside.origin.Path = "acme/two"
	inside.digest, err = digestObservedOrigin(inside.origin)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	fixture.service.observePublicationOrigin = func(context.Context, string) (publicationOriginObservation, error) {
		calls++
		if calls == 1 {
			return outside, nil
		}
		return inside, nil
	}
	fixture.service.now = func() time.Time { panic("read origin race consulted clock") }
	before := capturePublicationRawState(t, fixture.store)

	got, err := fixture.service.PublicationConfiguration(context.Background(), fixture.binding.Scope)
	if !errors.Is(err, ErrGitOriginChanged) || got != (PublicationConfiguration{}) || calls != 2 {
		t.Fatalf("origin-raced PublicationConfiguration()=(%+v,%v) calls=%d", got, err, calls)
	}
	if after := capturePublicationRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
		t.Fatalf("read origin race changed publication state\nbefore=%v\nafter=%v", before, after)
	}
}

func TestPublicationConfigurationUnknownCommitConfirmsStickyInvalidation(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/one.git")
	configured := configurePublicationForTest(t, fixture, types.PublicationPublicGit, diffActorEnvelope(), time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
	runGit(t, fixture.repository.root, "remote", "set-url", "origin", "https://github.com/acme/two.git")
	changedAt := time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC)
	fixture.service.now = func() time.Time { return changedAt }
	realTransaction := fixture.service.withImmediateWorkspace
	transactionCalls := 0
	fixture.service.withImmediateWorkspace = func(
		ctx context.Context,
		scope types.WorkspaceScope,
		callback func(*localstore.WorkspaceMutationTx) error,
	) error {
		transactionCalls++
		if err := realTransaction(ctx, scope, callback); err != nil {
			return err
		}
		return fmt.Errorf("%w: synthetic post-commit ambiguity", localstore.ErrCommitOutcomeUnknown)
	}

	got, err := fixture.service.PublicationConfiguration(context.Background(), fixture.binding.Scope)
	if err != nil || got.PolicyRevision != configured.PolicyRevision+1 || got.Classification != types.PublicationUnclassified ||
		got.TransitionKind != "origin_invalidated" || got.ChangedAt == nil || !got.ChangedAt.Equal(changedAt) || transactionCalls != 1 {
		t.Fatalf("read unknown-commit confirmation=(%+v,%v) transactionCalls=%d", got, err, transactionCalls)
	}
}

func TestPublicationConfigurationStableMismatchCommitsStickyInvalidation(t *testing.T) {
	t.Run("origin", func(t *testing.T) {
		fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/one.git")
		configured := configurePublicationForTest(t, fixture, types.PublicationPublicGit, diffActorEnvelope(), time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
		runGit(t, fixture.repository.root, "remote", "set-url", "origin", "https://github.com/acme/two.git")
		wantOrigin, err := InspectPublicationOrigin(context.Background(), fixture.repository.root)
		if err != nil {
			t.Fatal(err)
		}
		changedAt := time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC)
		clockCalls := 0
		fixture.service.now = func() time.Time { clockCalls++; return changedAt }

		got, err := fixture.service.PublicationConfiguration(context.Background(), fixture.binding.Scope)
		if err != nil || got.Classification != types.PublicationUnclassified ||
			got.PolicyRevision != configured.PolicyRevision+1 || got.ObservedOriginDigest != wantOrigin ||
			got.ConfiguredOriginDigest == nil || *got.ConfiguredOriginDigest != wantOrigin ||
			got.TransitionKind != "origin_invalidated" || got.ChangedBy != nil || got.ChangedAt == nil ||
			!got.ChangedAt.Equal(changedAt) || clockCalls != 1 {
			t.Fatalf("origin-invalidated PublicationConfiguration()=(%+v,%v) clockCalls=%d", got, err, clockCalls)
		}
		if rows := capturePublicationRawState(t, fixture.store).history; len(rows) != 3 {
			t.Fatalf("origin invalidation history rows=%d, want 3", len(rows))
		}
		fixture.service.now = func() time.Time { panic("current invalidation read consulted clock") }
		again, err := fixture.service.PublicationConfiguration(context.Background(), fixture.binding.Scope)
		if err != nil || !reflect.DeepEqual(again, got) {
			t.Fatalf("current invalidation read=(%+v,%v), want %+v", again, err, got)
		}
	})

	t.Run("repository governs kind", func(t *testing.T) {
		fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
		configured := configurePublicationForTest(t, fixture, types.PublicationPrivateGit, diffActorEnvelope(), time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
		oldRepository := types.RepositoryIdentity{
			Provider: "github", ImmutableID: "old-repository", CanonicalRemote: "https://github.com/acme/old",
		}
		encoded, err := json.Marshal(oldRepository)
		if err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"workspace_publication_policies", "workspace_publication_policy_history"} {
			if _, err := fixture.store.DB().Exec(`UPDATE `+table+` SET repository_identity_json=?
				WHERE project_id=? AND workspace_id=?`, string(encoded), fixture.binding.Scope.ProjectID, fixture.binding.Scope.WorkspaceID); err != nil {
				t.Fatal(err)
			}
		}
		changedAt := time.Date(2026, 8, 2, 14, 0, 0, 0, time.UTC)
		fixture.service.now = func() time.Time { return changedAt }

		got, err := fixture.service.PublicationConfiguration(context.Background(), fixture.binding.Scope)
		if err != nil || got.Classification != types.PublicationUnclassified ||
			got.PolicyRevision != configured.PolicyRevision+1 || got.TransitionKind != "repository_invalidated" ||
			got.ConfiguredOriginDigest == nil || *got.ConfiguredOriginDigest != got.ObservedOriginDigest ||
			got.ChangedBy != nil || got.ChangedAt == nil || !got.ChangedAt.Equal(changedAt) {
			t.Fatalf("repository-invalidated PublicationConfiguration()=(%+v,%v)", got, err)
		}
	})
}

func TestReconfigurePublicationStableMismatchInvalidatesBeforeCallerCAS(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/one.git")
	configured := configurePublicationForTest(t, fixture, types.PublicationPublicGit, diffActorEnvelope(), time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
	request := publicationRequest(t, fixture.binding, configured, types.PublicationPrivateGit, diffActorEnvelope())
	runGit(t, fixture.repository.root, "remote", "set-url", "origin", "https://github.com/acme/two.git")
	changedAt := time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC)
	fixture.service.now = func() time.Time { return changedAt }

	got, err := fixture.service.ReconfigurePublication(context.Background(), request)
	if !errors.Is(err, ErrPublicationConfigurationInvalidated) || got.Classification != types.PublicationUnclassified ||
		got.PolicyRevision != configured.PolicyRevision+1 || got.TransitionKind != "origin_invalidated" ||
		got.ConfiguredOriginDigest == nil || *got.ConfiguredOriginDigest != got.ObservedOriginDigest ||
		got.ChangedBy != nil || got.ChangedAt == nil || !got.ChangedAt.Equal(changedAt) {
		t.Fatalf("stale-origin ReconfigurePublication()=(%+v,%v)", got, err)
	}
	if rows := capturePublicationRawState(t, fixture.store).history; len(rows) != 3 {
		t.Fatalf("invalidation precedence history rows=%d, want 3", len(rows))
	}
}

func TestReconfigurePublicationUnknownCommitConfirmsConfiguredTransition(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
	current := mustPublicationConfiguration(t, fixture.service, fixture.binding.Scope)
	request := publicationRequest(t, fixture.binding, current, types.PublicationPrivateGit, diffActorEnvelope())
	changedAt := time.Date(2026, 8, 2, 15, 0, 0, 0, time.UTC)
	fixture.service.now = func() time.Time { return changedAt }
	realTransaction := fixture.service.withImmediateWorkspace
	transactionCalls := 0
	fixture.service.withImmediateWorkspace = func(
		ctx context.Context,
		scope types.WorkspaceScope,
		callback func(*localstore.WorkspaceMutationTx) error,
	) error {
		transactionCalls++
		if err := realTransaction(ctx, scope, callback); err != nil {
			return err
		}
		return fmt.Errorf("%w: synthetic post-commit ambiguity", localstore.ErrCommitOutcomeUnknown)
	}

	got, err := fixture.service.ReconfigurePublication(context.Background(), request)
	if err != nil || got.Classification != types.PublicationPrivateGit || got.PolicyRevision != 2 ||
		got.TransitionKind != "configured" || got.ChangedAt == nil || !got.ChangedAt.Equal(changedAt) || transactionCalls != 1 {
		t.Fatalf("configured unknown-commit confirmation=(%+v,%v) transactionCalls=%d", got, err, transactionCalls)
	}
	if rows := capturePublicationRawState(t, fixture.store).history; len(rows) != 2 {
		t.Fatalf("configured unknown-commit history rows=%d, want 2", len(rows))
	}
}

func TestReconfigurePublicationUnknownCommitConfirmsInvalidation(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/one.git")
	configured := configurePublicationForTest(t, fixture, types.PublicationPublicGit, diffActorEnvelope(), time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
	request := publicationRequest(t, fixture.binding, configured, types.PublicationPrivateGit, diffActorEnvelope())
	runGit(t, fixture.repository.root, "remote", "set-url", "origin", "https://github.com/acme/two.git")
	changedAt := time.Date(2026, 8, 2, 16, 0, 0, 0, time.UTC)
	fixture.service.now = func() time.Time { return changedAt }
	realTransaction := fixture.service.withImmediateWorkspace
	fixture.service.withImmediateWorkspace = func(
		ctx context.Context,
		scope types.WorkspaceScope,
		callback func(*localstore.WorkspaceMutationTx) error,
	) error {
		if err := realTransaction(ctx, scope, callback); err != nil {
			return err
		}
		return fmt.Errorf("%w: synthetic post-commit ambiguity", localstore.ErrCommitOutcomeUnknown)
	}

	got, err := fixture.service.ReconfigurePublication(context.Background(), request)
	if !errors.Is(err, ErrPublicationConfigurationInvalidated) || errors.Is(err, localstore.ErrCommitOutcomeUnknown) ||
		got.Classification != types.PublicationUnclassified || got.PolicyRevision != configured.PolicyRevision+1 ||
		got.TransitionKind != "origin_invalidated" || got.ChangedAt == nil || !got.ChangedAt.Equal(changedAt) {
		t.Fatalf("invalidation unknown-commit confirmation=(%+v,%v)", got, err)
	}
	if rows := capturePublicationRawState(t, fixture.store).history; len(rows) != 3 {
		t.Fatalf("invalidation unknown-commit history rows=%d, want 3", len(rows))
	}
}

func TestReconfigurePublicationUnknownCommitExactAbsenceRetainsOriginalError(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
	current := mustPublicationConfiguration(t, fixture.service, fixture.binding.Scope)
	request := publicationRequest(t, fixture.binding, current, types.PublicationLocalOnly, diffActorEnvelope())
	fixture.service.now = func() time.Time { return time.Date(2026, 8, 2, 17, 0, 0, 0, time.UTC) }
	if _, err := fixture.store.DB().Exec(`
		CREATE TABLE publication_deferred_failure(
		  project_id TEXT NOT NULL,
		  workspace_id TEXT NOT NULL,
		  FOREIGN KEY(project_id,workspace_id) REFERENCES workspace_bindings(project_id,workspace_id)
		    DEFERRABLE INITIALLY DEFERRED
		);
		CREATE TRIGGER publication_fail_commit AFTER UPDATE ON workspace_publication_policies BEGIN
		  INSERT INTO publication_deferred_failure(project_id,workspace_id)
		  VALUES ('00000000-0000-4000-8000-000000000099','00000000-0000-4000-8000-000000000098');
		END;
	`); err != nil {
		t.Fatal(err)
	}
	before := capturePublicationRawState(t, fixture.store)

	got, err := fixture.service.ReconfigurePublication(context.Background(), request)
	if !errors.Is(err, localstore.ErrCommitOutcomeUnknown) || got != (PublicationConfiguration{}) {
		t.Fatalf("absent unknown-commit confirmation=(%+v,%v)", got, err)
	}
	if after := capturePublicationRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
		t.Fatalf("failed publication commit changed state\nbefore=%v\nafter=%v", before, after)
	}
}

func TestConfirmPublicationCommitRejectsThirdStateAndReadError(t *testing.T) {
	commitErr := fmt.Errorf("%w: confirmation fixture", localstore.ErrCommitOutcomeUnknown)
	t.Run("exact next", func(t *testing.T) {
		fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
		prior, priorHistory := readPublicationPolicyState(t, fixture.service, fixture.binding.Scope)
		configured := configurePublicationForTest(t, fixture, types.PublicationPublicGit, diffActorEnvelope(), time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
		next, _ := readPublicationPolicyState(t, fixture.service, fixture.binding.Scope)
		attempt := publicationTransitionAttempt{
			prior: prior, next: next, priorHistory: priorHistory,
			configuration: configured, completed: true,
		}

		got, err := confirmPublicationCommit(context.Background(), fixture.service.repo, fixture.binding.Scope, attempt, commitErr)
		if err != nil || !reflect.DeepEqual(got, configured) {
			t.Fatalf("exact confirmation=(%+v,%v), want %+v", got, err, configured)
		}
	})

	t.Run("third valid revision", func(t *testing.T) {
		fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
		prior, priorHistory := readPublicationPolicyState(t, fixture.service, fixture.binding.Scope)
		configured := configurePublicationForTest(t, fixture, types.PublicationPublicGit, diffActorEnvelope(), time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
		next, _ := readPublicationPolicyState(t, fixture.service, fixture.binding.Scope)
		attempt := publicationTransitionAttempt{
			prior: prior, next: next, priorHistory: priorHistory,
			configuration: configured, completed: true,
		}
		fixture.service.now = func() time.Time { return time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC) }
		if _, err := fixture.service.ReconfigurePublication(context.Background(), publicationRequest(
			t, fixture.binding, configured, types.PublicationPrivateGit, diffActorEnvelope(),
		)); err != nil {
			t.Fatal(err)
		}

		got, err := confirmPublicationCommit(context.Background(), fixture.service.repo, fixture.binding.Scope, attempt, commitErr)
		if !errors.Is(err, commitErr) || got != (PublicationConfiguration{}) {
			t.Fatalf("third-state confirmation=(%+v,%v)", got, err)
		}
	})

	t.Run("read error", func(t *testing.T) {
		fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
		prior, priorHistory := readPublicationPolicyState(t, fixture.service, fixture.binding.Scope)
		configured := configurePublicationForTest(t, fixture, types.PublicationPublicGit, diffActorEnvelope(), time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
		next, _ := readPublicationPolicyState(t, fixture.service, fixture.binding.Scope)
		attempt := publicationTransitionAttempt{
			prior: prior, next: next, priorHistory: priorHistory,
			configuration: configured, completed: true,
		}
		if err := fixture.store.Close(); err != nil {
			t.Fatal(err)
		}

		got, err := confirmPublicationCommit(context.Background(), fixture.service.repo, fixture.binding.Scope, attempt, commitErr)
		if !errors.Is(err, commitErr) || got != (PublicationConfiguration{}) {
			t.Fatalf("read-error confirmation=(%+v,%v)", got, err)
		}
	})
}

func TestReconfigurePublicationRejectsInvalidCompleteRequestBeforeIO(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
	current := mustPublicationConfiguration(t, fixture.service, fixture.binding.Scope)
	valid := publicationRequest(t, fixture.binding, current, types.PublicationPublicGit, diffActorEnvelope())
	tests := []struct {
		name   string
		mutate func(*ReconfigurePublicationRequest)
	}{
		{name: "scope", mutate: func(req *ReconfigurePublicationRequest) { req.Scope = types.WorkspaceScope{} }},
		{name: "binding", mutate: func(req *ReconfigurePublicationRequest) { req.ExpectedBinding.Checkout.Device = 0 }},
		{name: "constraint", mutate: func(req *ReconfigurePublicationRequest) { req.ExpectedPublicationBindingDigest = "SHA256:bad" }},
		{name: "expected revision", mutate: func(req *ReconfigurePublicationRequest) { req.Expected.PolicyRevision = 0 }},
		{name: "configured revision one", mutate: func(req *ReconfigurePublicationRequest) {
			origin := req.Expected.ObservedOriginDigest
			actor := diffActorEnvelope()
			changedAt := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
			req.Expected.ConfiguredOriginDigest = &origin
			req.Expected.TransitionKind = "configured"
			req.Expected.ChangedBy = &actor
			req.Expected.ChangedAt = &changedAt
		}},
		{name: "invalidated revision one", mutate: func(req *ReconfigurePublicationRequest) {
			origin := req.Expected.ObservedOriginDigest
			changedAt := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
			req.Expected.ConfiguredOriginDigest = &origin
			req.Expected.TransitionKind = "origin_invalidated"
			req.Expected.ChangedAt = &changedAt
		}},
		{name: "expected observed origin", mutate: func(req *ReconfigurePublicationRequest) { req.Expected.ObservedOriginDigest = "" }},
		{name: "classification", mutate: func(req *ReconfigurePublicationRequest) { req.Classification = "future" }},
		{name: "agent actor", mutate: func(req *ReconfigurePublicationRequest) {
			req.Actor = types.ActorEnvelope{
				ActorKind: types.ActorAgent, AgentID: "00000000-0000-4000-8000-000000000031",
				AccountableHumanID: "00000000-0000-4000-8000-000000000021", SessionID: "session",
				HarnessName: "codex", HarnessVersion: "1", Assurance: types.AssuranceLocal,
				OccurredAt: time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC),
			}
		}},
		{name: "non-local human", mutate: func(req *ReconfigurePublicationRequest) { req.Actor.Assurance = types.AssurancePublicKeyContinuity }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			request.Expected = clonePublicationConfiguration(valid.Expected)
			test.mutate(&request)
			service := &Service{
				observePublicationOrigin: func(context.Context, string) (publicationOriginObservation, error) {
					panic("invalid request performed origin I/O")
				},
				withImmediateWorkspace: func(context.Context, types.WorkspaceScope, func(*localstore.WorkspaceMutationTx) error) error {
					panic("invalid request entered transaction")
				},
				now: func() time.Time { panic("invalid request consulted clock") },
			}
			got, err := service.ReconfigurePublication(context.Background(), request)
			if err == nil || errors.Is(err, localstore.ErrNotFound) || got != (PublicationConfiguration{}) {
				t.Fatalf("invalid request ReconfigurePublication()=(%+v,%v)", got, err)
			}
		})
	}
}

func TestReconfigurePublicationCompleteExpectedStateAndBindingCAS(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ReconfigurePublicationRequest)
	}{
		{name: "checkout path", mutate: func(req *ReconfigurePublicationRequest) { req.ExpectedBinding.Checkout.CanonicalPath += "-stale" }},
		{name: "checkout device", mutate: func(req *ReconfigurePublicationRequest) { req.ExpectedBinding.Checkout.Device++ }},
		{name: "checkout inode", mutate: func(req *ReconfigurePublicationRequest) { req.ExpectedBinding.Checkout.Inode++ }},
		{name: "repository", mutate: func(req *ReconfigurePublicationRequest) {
			req.ExpectedBinding.Repository = types.RepositoryIdentity{
				Provider: "github", ImmutableID: "stale", CanonicalRemote: "https://github.com/acme/stale",
			}
		}},
		{name: "accepted ref", mutate: func(req *ReconfigurePublicationRequest) { req.ExpectedBinding.AcceptedRef = "refs/heads/stale" }},
		{name: "accepted commit", mutate: func(req *ReconfigurePublicationRequest) {
			req.ExpectedBinding.AcceptedCommitSHA = strings.Repeat("b", 40)
		}},
		{name: "accepted digest", mutate: func(req *ReconfigurePublicationRequest) {
			req.ExpectedBinding.AcceptedTreeDigest = publicationDigest('b')
		}},
		{name: "constraint", mutate: func(req *ReconfigurePublicationRequest) {
			req.ExpectedPublicationBindingDigest = state.Digest(publicationDigest('f'))
		}},
		{name: "expected classification", mutate: func(req *ReconfigurePublicationRequest) { req.Expected.Classification = types.PublicationPrivateGit }},
		{name: "expected revision", mutate: func(req *ReconfigurePublicationRequest) { req.Expected.PolicyRevision++ }},
		{name: "expected observed origin", mutate: func(req *ReconfigurePublicationRequest) {
			req.Expected.ObservedOriginDigest = state.Digest(publicationDigest('b'))
		}},
		{name: "expected configured origin", mutate: func(req *ReconfigurePublicationRequest) {
			value := state.Digest(publicationDigest('b'))
			req.Expected.ConfiguredOriginDigest = &value
		}},
		{name: "expected transition", mutate: func(req *ReconfigurePublicationRequest) {
			req.Expected.Classification = types.PublicationUnclassified
			req.Expected.TransitionKind = "origin_invalidated"
			req.Expected.ChangedBy = nil
		}},
		{name: "expected actor", mutate: func(req *ReconfigurePublicationRequest) {
			req.Expected.ChangedBy.HumanPrincipalID = "00000000-0000-4000-8000-000000000099"
		}},
		{name: "expected changed at", mutate: func(req *ReconfigurePublicationRequest) {
			value := req.Expected.ChangedAt.Add(time.Minute)
			req.Expected.ChangedAt = &value
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
			configured := configurePublicationForTest(t, fixture, types.PublicationPublicGit, diffActorEnvelope(), time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
			request := publicationRequest(t, fixture.binding, configured, configured.Classification, diffActorEnvelope())
			test.mutate(&request)
			fixture.service.now = func() time.Time { panic("stale CAS consulted clock") }
			before := capturePublicationRawState(t, fixture.store)

			got, err := fixture.service.ReconfigurePublication(context.Background(), request)
			if !errors.Is(err, ErrPublicationConfigurationCAS) || !errors.Is(err, localstore.ErrPublicationConfigurationCAS) ||
				got != (PublicationConfiguration{}) {
				t.Fatalf("stale %s ReconfigurePublication()=(%+v,%v)", test.name, got, err)
			}
			if after := capturePublicationRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
				t.Fatalf("stale %s changed publication state\nbefore=%v\nafter=%v", test.name, before, after)
			}
		})
	}
}

func TestReconfigurePublicationStaleConstraintAgainstBootstrapIsZeroMutation(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
	current := mustPublicationConfiguration(t, fixture.service, fixture.binding.Scope)
	request := publicationRequest(t, fixture.binding, current, types.PublicationUnclassified, diffActorEnvelope())
	request.ExpectedPublicationBindingDigest = state.Digest(publicationDigest('f'))
	fixture.service.now = func() time.Time { panic("bootstrap constraint CAS consulted clock") }
	before := capturePublicationRawState(t, fixture.store)

	got, err := fixture.service.ReconfigurePublication(context.Background(), request)
	if !errors.Is(err, ErrPublicationConfigurationCAS) || got != (PublicationConfiguration{}) {
		t.Fatalf("stale bootstrap constraint ReconfigurePublication()=(%+v,%v)", got, err)
	}
	if after := capturePublicationRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
		t.Fatal("stale bootstrap constraint changed publication state")
	}
}

func TestReconfigurePublicationExpectedZeroOffsetTimesCompareByInstant(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
	configured := configurePublicationForTest(t, fixture, types.PublicationLocalOnly, diffActorEnvelope(), time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
	request := publicationRequest(t, fixture.binding, configured, configured.Classification, diffActorEnvelope())
	zero := time.FixedZone("semantic-zero", 0)
	changedAt := request.Expected.ChangedAt.In(zero)
	request.Expected.ChangedAt = &changedAt
	request.Expected.ChangedBy.OccurredAt = request.Expected.ChangedBy.OccurredAt.In(zero)
	fixture.service.now = func() time.Time { panic("semantic no-op consulted clock") }

	got, err := fixture.service.ReconfigurePublication(context.Background(), request)
	if err != nil || !reflect.DeepEqual(got, configured) {
		t.Fatalf("semantic-time no-op=(%+v,%v), want %+v", got, err, configured)
	}
}

func TestReconfigurePublicationInvalidationToExplicitUnclassifiedAdvancesRevision(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/one.git")
	configured := configurePublicationForTest(t, fixture, types.PublicationPublicGit, diffActorEnvelope(), time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
	runGit(t, fixture.repository.root, "remote", "set-url", "origin", "https://github.com/acme/two.git")
	fixture.service.now = func() time.Time { return time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC) }
	invalidated := mustPublicationConfiguration(t, fixture.service, fixture.binding.Scope)
	if invalidated.PolicyRevision != configured.PolicyRevision+1 || invalidated.TransitionKind != "origin_invalidated" {
		t.Fatalf("invalidated=%+v", invalidated)
	}
	changedAt := time.Date(2026, 8, 2, 14, 0, 0, 0, time.UTC)
	fixture.service.now = func() time.Time { return changedAt }

	got, err := fixture.service.ReconfigurePublication(context.Background(), publicationRequest(
		t, fixture.binding, invalidated, types.PublicationUnclassified, diffActorEnvelope(),
	))
	if err != nil || got.Classification != types.PublicationUnclassified ||
		got.PolicyRevision != invalidated.PolicyRevision+1 || got.TransitionKind != "configured" ||
		got.ChangedBy == nil || got.ChangedAt == nil || !got.ChangedAt.Equal(changedAt) {
		t.Fatalf("explicit unclassified after invalidation=(%+v,%v)", got, err)
	}
}

func TestReconfigurePublicationClassAndOriginReversionNeverReactivatesHistory(t *testing.T) {
	t.Run("public private public", func(t *testing.T) {
		fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
		actor := diffActorEnvelope()
		public := configurePublicationForTest(t, fixture, types.PublicationPublicGit, actor, time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
		fixture.service.now = func() time.Time { return time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC) }
		private, err := fixture.service.ReconfigurePublication(context.Background(), publicationRequest(
			t, fixture.binding, public, types.PublicationPrivateGit, actor,
		))
		if err != nil {
			t.Fatal(err)
		}
		fixture.service.now = func() time.Time { return time.Date(2026, 8, 2, 14, 0, 0, 0, time.UTC) }
		publicAgain, err := fixture.service.ReconfigurePublication(context.Background(), publicationRequest(
			t, fixture.binding, private, types.PublicationPublicGit, actor,
		))
		if err != nil || private.PolicyRevision != public.PolicyRevision+1 || publicAgain.PolicyRevision != private.PolicyRevision+1 ||
			private.Classification != types.PublicationPrivateGit || publicAgain.Classification != types.PublicationPublicGit {
			t.Fatalf("public/private/public=(%+v,%+v,%+v,%v)", public, private, publicAgain, err)
		}
		if rows := capturePublicationRawState(t, fixture.store).history; len(rows) != 4 {
			t.Fatalf("public/private/public history rows=%d, want 4", len(rows))
		}
	})

	t.Run("origin A B A", func(t *testing.T) {
		fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/a.git")
		actor := diffActorEnvelope()
		configuredA := configurePublicationForTest(t, fixture, types.PublicationPublicGit, actor, time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
		originA := *configuredA.ConfiguredOriginDigest
		runGit(t, fixture.repository.root, "remote", "set-url", "origin", "https://github.com/acme/b.git")
		fixture.service.now = func() time.Time { return time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC) }
		invalidatedB := mustPublicationConfiguration(t, fixture.service, fixture.binding.Scope)
		fixture.service.now = func() time.Time { return time.Date(2026, 8, 2, 14, 0, 0, 0, time.UTC) }
		configuredB, err := fixture.service.ReconfigurePublication(context.Background(), publicationRequest(
			t, fixture.binding, invalidatedB, types.PublicationPrivateGit, actor,
		))
		if err != nil {
			t.Fatal(err)
		}
		originB := *configuredB.ConfiguredOriginDigest
		runGit(t, fixture.repository.root, "remote", "set-url", "origin", "https://github.com/acme/a.git")
		fixture.service.now = func() time.Time { return time.Date(2026, 8, 2, 15, 0, 0, 0, time.UTC) }
		returnedA := mustPublicationConfiguration(t, fixture.service, fixture.binding.Scope)
		if originA == originB || returnedA.PolicyRevision != configuredB.PolicyRevision+1 ||
			returnedA.Classification != types.PublicationUnclassified || returnedA.TransitionKind != "origin_invalidated" ||
			returnedA.ConfiguredOriginDigest == nil || *returnedA.ConfiguredOriginDigest != originA {
			t.Fatalf("A/B/A lineage=(A=%+v invalidB=%+v B=%+v returnedA=%+v)", configuredA, invalidatedB, configuredB, returnedA)
		}
		if rows := capturePublicationRawState(t, fixture.store).history; len(rows) != 5 {
			t.Fatalf("A/B/A history rows=%d, want 5", len(rows))
		}
	})
}

func TestReconfigurePublicationPublicForkRequiresExplicitPublicGit(t *testing.T) {
	projectID := "00000000-0000-4000-8000-000000000001"
	repository := createGitRepository(t, projectID)
	repository.identity = types.RepositoryIdentity{
		Provider: "github", ImmutableID: "upstream-repository", CanonicalRemote: "https://github.com/upstream/wormhole",
	}
	repository.commit = commitSnapshot(t, repository.root, projectID, repository.identity)
	runGit(t, repository.root, "remote", "add", "origin", "https://github.com/contributor/wormhole.git")
	_, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	current := mustPublicationConfiguration(t, service, registered.Binding.Scope)
	if current.Classification != types.PublicationUnclassified {
		t.Fatalf("fork bootstrap inferred classification=%+v", current)
	}
	service.now = func() time.Time { return time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC) }

	got, err := service.ReconfigurePublication(context.Background(), publicationRequest(
		t, registered.Binding, current, types.PublicationPublicGit, diffActorEnvelope(),
	))
	if err != nil || got.Classification != types.PublicationPublicGit || got.TransitionKind != "configured" {
		t.Fatalf("explicit public fork configuration=(%+v,%v)", got, err)
	}
}

func TestReconfigurePublicationNonGitSSHUsernameRejectsWithoutLeakOrDigestPersistence(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "ssh://git@example.com/acme/wormhole.git")
	configured := configurePublicationForTest(t, fixture, types.PublicationPrivateGit, diffActorEnvelope(), time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
	request := publicationRequest(t, fixture.binding, configured, types.PublicationPublicGit, diffActorEnvelope())
	runGit(t, fixture.repository.root, "remote", "set-url", "origin", "ssh://private-user@example.com/acme/wormhole.git")
	fixture.service.now = func() time.Time { panic("invalid SSH origin consulted clock") }
	before := capturePublicationRawState(t, fixture.store)

	got, err := fixture.service.ReconfigurePublication(context.Background(), request)
	if err == nil || got != (PublicationConfiguration{}) || strings.Contains(err.Error(), "private-user") ||
		strings.Contains(err.Error(), "ssh://") {
		t.Fatalf("non-git SSH username ReconfigurePublication()=(%+v,%v)", got, err)
	}
	if after := capturePublicationRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
		t.Fatalf("invalid SSH origin persisted a digest\nbefore=%v\nafter=%v", before, after)
	}
}

func TestPublicationServiceIsolatesProjectsWorkspacesAndSurvivesRestart(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, service := openProjectStateServiceAt(t, databasePath)
	targetRepository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	runGit(t, targetRepository.root, "remote", "add", "origin", "https://github.com/acme/wormhole.git")
	target := registerGitRepository(t, service, targetRepository)
	siblingRoot := filepath.Join(t.TempDir(), "sibling-worktree")
	runGit(t, targetRepository.root, "worktree", "add", "-b", "publication-sibling", siblingRoot, targetRepository.commit)
	siblingRepository := gitRepository{
		root: siblingRoot, projectID: targetRepository.projectID, identity: targetRepository.identity, commit: targetRepository.commit,
	}
	sibling := registerGitRepository(t, service, siblingRepository)
	otherRepository := createGitRepository(t, "00000000-0000-4000-8000-000000000002")
	runGit(t, otherRepository.root, "remote", "add", "origin", "https://github.com/acme/wormhole.git")
	other := registerGitRepository(t, service, otherRepository)
	targetConfigured := configurePublicationForTest(t, publicationServiceFixture{
		repository: targetRepository, store: store, service: service, binding: target.Binding,
	}, types.PublicationPrivateGit, diffActorEnvelope(), time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
	siblingBootstrap := mustPublicationConfiguration(t, service, sibling.Binding.Scope)
	otherBootstrap := mustPublicationConfiguration(t, service, other.Binding.Scope)
	before := capturePublicationRawState(t, store)

	for _, test := range []struct {
		name    string
		binding types.WorkspaceBinding
	}{
		{name: "same-project sibling", binding: sibling.Binding},
		{name: "other project", binding: other.Binding},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := publicationRequest(t, test.binding, targetConfigured, types.PublicationPrivateGit, diffActorEnvelope())
			service.now = func() time.Time { panic("cross-scope CAS consulted clock") }
			got, err := service.ReconfigurePublication(context.Background(), request)
			if !errors.Is(err, ErrPublicationConfigurationCAS) || got != (PublicationConfiguration{}) {
				t.Fatalf("cross-scope ReconfigurePublication()=(%+v,%v)", got, err)
			}
		})
	}
	if after := capturePublicationRawState(t, store); !reflect.DeepEqual(after, before) {
		t.Fatalf("cross-scope rejection changed publication state\nbefore=%v\nafter=%v", before, after)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedStore, reopenedService := openProjectStateServiceAt(t, databasePath)
	if got := mustPublicationConfiguration(t, reopenedService, target.Binding.Scope); !reflect.DeepEqual(got, targetConfigured) {
		t.Fatalf("restarted target configuration=%+v, want %+v", got, targetConfigured)
	}
	if got := mustPublicationConfiguration(t, reopenedService, sibling.Binding.Scope); !reflect.DeepEqual(got, siblingBootstrap) {
		t.Fatalf("restarted sibling configuration=%+v, want %+v", got, siblingBootstrap)
	}
	if got := mustPublicationConfiguration(t, reopenedService, other.Binding.Scope); !reflect.DeepEqual(got, otherBootstrap) {
		t.Fatalf("restarted other configuration=%+v, want %+v", got, otherBootstrap)
	}
	if after := capturePublicationRawState(t, reopenedStore); !reflect.DeepEqual(after, before) {
		t.Fatal("restart changed publication state")
	}
}

func TestReconfigurePublicationConcurrentSameExpectedAllowsOneTransition(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
	current := mustPublicationConfiguration(t, fixture.service, fixture.binding.Scope)
	request := publicationRequest(t, fixture.binding, current, types.PublicationPublicGit, diffActorEnvelope())
	fixture.service.now = func() time.Time { return time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC) }
	realTransaction := fixture.service.withImmediateWorkspace
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	fixture.service.withImmediateWorkspace = func(
		ctx context.Context,
		scope types.WorkspaceScope,
		callback func(*localstore.WorkspaceMutationTx) error,
	) error {
		ready <- struct{}{}
		<-release
		return realTransaction(ctx, scope, callback)
	}
	type outcome struct {
		configuration PublicationConfiguration
		err           error
	}
	outcomes := make(chan outcome, 2)
	for range 2 {
		go func() {
			got, err := fixture.service.ReconfigurePublication(context.Background(), request)
			outcomes <- outcome{configuration: got, err: err}
		}()
	}
	<-ready
	<-ready
	close(release)
	var successes, stale int
	for range 2 {
		result := <-outcomes
		switch {
		case result.err == nil && result.configuration.PolicyRevision == 2:
			successes++
		case errors.Is(result.err, ErrPublicationConfigurationCAS) && result.configuration == (PublicationConfiguration{}):
			stale++
		default:
			t.Fatalf("concurrent outcome=(%+v,%v)", result.configuration, result.err)
		}
	}
	if successes != 1 || stale != 1 {
		t.Fatalf("concurrent outcomes successes=%d stale=%d", successes, stale)
	}
	if rows := capturePublicationRawState(t, fixture.store).history; len(rows) != 2 {
		t.Fatalf("concurrent history rows=%d, want 2", len(rows))
	}
}

func TestPublicationServiceRejectsCorruptPolicyWithoutMutation(t *testing.T) {
	tests := []struct {
		name        string
		reconfigure bool
		mutate      func(*testing.T, publicationServiceFixture)
	}{
		{name: "current differs from history", mutate: func(t *testing.T, fixture publicationServiceFixture) {
			if _, err := fixture.store.DB().Exec(`UPDATE workspace_publication_policies SET classification='local_only'
				WHERE project_id=? AND workspace_id=?`, fixture.binding.Scope.ProjectID, fixture.binding.Scope.WorkspaceID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "configured agent actor", reconfigure: true, mutate: func(t *testing.T, fixture publicationServiceFixture) {
			agent := types.ActorEnvelope{
				ActorKind: types.ActorAgent, AgentID: "00000000-0000-4000-8000-000000000031",
				AccountableHumanID: "00000000-0000-4000-8000-000000000021", SessionID: "session",
				HarnessName: "codex", HarnessVersion: "1", Assurance: types.AssuranceLocal,
				OccurredAt: time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC),
			}
			encoded, err := state.CanonicalJSON(agent)
			if err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"workspace_publication_policies", "workspace_publication_policy_history"} {
				if _, err := fixture.store.DB().Exec(`UPDATE `+table+` SET changed_actor_json=?
					WHERE project_id=? AND workspace_id=? AND policy_revision=2`, string(encoded),
					fixture.binding.Scope.ProjectID, fixture.binding.Scope.WorkspaceID); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{name: "history gap", mutate: func(t *testing.T, fixture publicationServiceFixture) {
			if _, err := fixture.store.DB().Exec(`DELETE FROM workspace_publication_policy_history
				WHERE project_id=? AND workspace_id=? AND policy_revision=1`, fixture.binding.Scope.ProjectID, fixture.binding.Scope.WorkspaceID); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
			configured := configurePublicationForTest(t, fixture, types.PublicationPublicGit, diffActorEnvelope(), time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
			request := publicationRequest(t, fixture.binding, configured, types.PublicationPrivateGit, diffActorEnvelope())
			test.mutate(t, fixture)
			fixture.service.now = func() time.Time { panic("corrupt policy consulted clock") }
			before := capturePublicationRawState(t, fixture.store)
			var got PublicationConfiguration
			var err error
			if test.reconfigure {
				got, err = fixture.service.ReconfigurePublication(context.Background(), request)
			} else {
				got, err = fixture.service.PublicationConfiguration(context.Background(), fixture.binding.Scope)
			}
			if err == nil || errors.Is(err, ErrPublicationConfigurationCAS) || got != (PublicationConfiguration{}) {
				t.Fatalf("corrupt policy service call=(%+v,%v)", got, err)
			}
			if after := capturePublicationRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
				t.Fatalf("corruption failure changed state\nbefore=%v\nafter=%v", before, after)
			}
		})
	}
}

func TestReconfigurePublicationWriteFailuresRollBackPolicyAndHistory(t *testing.T) {
	for _, test := range []struct {
		name    string
		trigger string
	}{
		{name: "current update", trigger: `
			CREATE TRIGGER publication_reject_update BEFORE UPDATE ON workspace_publication_policies
			BEGIN SELECT RAISE(ABORT,'reject publication update'); END;`},
		{name: "history append", trigger: `
			CREATE TRIGGER publication_reject_history BEFORE INSERT ON workspace_publication_policy_history
			BEGIN SELECT RAISE(ABORT,'reject publication history'); END;`},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
			current := mustPublicationConfiguration(t, fixture.service, fixture.binding.Scope)
			request := publicationRequest(t, fixture.binding, current, types.PublicationLocalOnly, diffActorEnvelope())
			if _, err := fixture.store.DB().Exec(test.trigger); err != nil {
				t.Fatal(err)
			}
			clockCalls := 0
			fixture.service.now = func() time.Time {
				clockCalls++
				return time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
			}
			before := capturePublicationRawState(t, fixture.store)

			got, err := fixture.service.ReconfigurePublication(context.Background(), request)
			if err == nil || errors.Is(err, localstore.ErrCommitOutcomeUnknown) ||
				errors.Is(err, ErrPublicationConfigurationCAS) || got != (PublicationConfiguration{}) || clockCalls != 1 {
				t.Fatalf("write-failed ReconfigurePublication()=(%+v,%v) clockCalls=%d", got, err, clockCalls)
			}
			if after := capturePublicationRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
				t.Fatalf("write failure changed publication state\nbefore=%v\nafter=%v", before, after)
			}
		})
	}
}

func TestReconfigurePublicationInvalidClockRollsBackWithoutWrite(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
	current := mustPublicationConfiguration(t, fixture.service, fixture.binding.Scope)
	request := publicationRequest(t, fixture.binding, current, types.PublicationPublicGit, diffActorEnvelope())
	fixture.service.now = func() time.Time { return time.Time{} }
	before := capturePublicationRawState(t, fixture.store)

	got, err := fixture.service.ReconfigurePublication(context.Background(), request)
	if err == nil || got != (PublicationConfiguration{}) {
		t.Fatalf("invalid-clock ReconfigurePublication()=(%+v,%v)", got, err)
	}
	if after := capturePublicationRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
		t.Fatal("invalid clock changed publication state")
	}
}

func TestReconfigurePublicationOriginRaceReturnsErrGitOriginChangedWithoutMutation(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/one.git")
	current := mustPublicationConfiguration(t, fixture.service, fixture.binding.Scope)
	request := publicationRequest(t, fixture.binding, current, types.PublicationPublicGit, diffActorEnvelope())
	outside, err := observePublicationOrigin(context.Background(), fixture.repository.root)
	if err != nil {
		t.Fatal(err)
	}
	inside := outside
	inside.origin.Path = "acme/two"
	inside.digest, err = digestObservedOrigin(inside.origin)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	fixture.service.observePublicationOrigin = func(context.Context, string) (publicationOriginObservation, error) {
		calls++
		if calls == 1 {
			return outside, nil
		}
		return inside, nil
	}
	fixture.service.now = func() time.Time { panic("origin race consulted clock") }
	before := capturePublicationRawState(t, fixture.store)

	got, err := fixture.service.ReconfigurePublication(context.Background(), request)
	if !errors.Is(err, ErrGitOriginChanged) || got != (PublicationConfiguration{}) || calls != 2 {
		t.Fatalf("origin-raced ReconfigurePublication()=(%+v,%v) calls=%d", got, err, calls)
	}
	if after := capturePublicationRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
		t.Fatalf("origin race changed publication state\nbefore=%v\nafter=%v", before, after)
	}
}

func TestReconfigurePublicationCurrentExactSameHumanIsNoOpWithoutClockOrWrite(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
	actor := diffActorEnvelope()
	configured := configurePublicationForTest(t, fixture, types.PublicationPrivateGit, actor, time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
	if _, err := fixture.store.DB().Exec(`
		CREATE TRIGGER publication_noop_reject_update BEFORE UPDATE ON workspace_publication_policies
		BEGIN SELECT RAISE(ABORT,'publication no-op updated'); END;
		CREATE TRIGGER publication_noop_reject_history BEFORE INSERT ON workspace_publication_policy_history
		BEGIN SELECT RAISE(ABORT,'publication no-op appended'); END;
	`); err != nil {
		t.Fatal(err)
	}
	retryActor := actor
	retryActor.OccurredAt = retryActor.OccurredAt.Add(time.Hour)
	request := publicationRequest(t, fixture.binding, configured, configured.Classification, retryActor)
	fixture.service.now = func() time.Time { panic("same-human no-op consulted clock") }
	before := capturePublicationRawState(t, fixture.store)

	got, err := fixture.service.ReconfigurePublication(context.Background(), request)
	if err != nil || !reflect.DeepEqual(got, configured) {
		t.Fatalf("same-human no-op=(%+v,%v), want %+v", got, err, configured)
	}
	if after := capturePublicationRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
		t.Fatalf("same-human no-op changed publication state\nbefore=%v\nafter=%v", before, after)
	}
}

func TestReconfigurePublicationSameClassDifferentHumanAdvancesAttribution(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
	firstActor := diffActorEnvelope()
	configured := configurePublicationForTest(t, fixture, types.PublicationPublicGit, firstActor, time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
	secondActor := firstActor
	secondActor.HumanPrincipalID = "00000000-0000-4000-8000-000000000099"
	secondActor.OccurredAt = secondActor.OccurredAt.Add(time.Hour)
	changedAt := time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC)
	clockCalls := 0
	fixture.service.now = func() time.Time { clockCalls++; return changedAt }

	got, err := fixture.service.ReconfigurePublication(context.Background(), publicationRequest(
		t, fixture.binding, configured, configured.Classification, secondActor,
	))
	if err != nil || got.PolicyRevision != configured.PolicyRevision+1 || got.ChangedBy == nil ||
		!reflect.DeepEqual(*got.ChangedBy, secondActor) || got.ChangedAt == nil || !got.ChangedAt.Equal(changedAt) || clockCalls != 1 {
		t.Fatalf("different-human ReconfigurePublication()=(%+v,%v) clockCalls=%d", got, err, clockCalls)
	}
	if rows := capturePublicationRawState(t, fixture.store).history; len(rows) != 3 {
		t.Fatalf("different-human history rows=%d, want 3", len(rows))
	}
}

func TestReconfigurePublicationConfiguresEveryClassificationWithCompleteOwnedReadback(t *testing.T) {
	for _, classification := range []types.PublicationClassification{
		types.PublicationUnclassified,
		types.PublicationLocalOnly,
		types.PublicationPublicGit,
		types.PublicationPrivateGit,
	} {
		t.Run(string(classification), func(t *testing.T) {
			repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
			runGit(t, repository.root, "remote", "add", "origin", "https://github.com/acme/wormhole.git")
			store, service := openProjectStateService(t, "")
			registered := registerGitRepository(t, service, repository)
			current, err := service.PublicationConfiguration(context.Background(), registered.Binding.Scope)
			if err != nil {
				t.Fatal(err)
			}
			constraint, err := DigestPublicationBindingConstraint(registered.Binding.Repository, current.ObservedOriginDigest)
			if err != nil {
				t.Fatal(err)
			}
			actor := diffActorEnvelope()
			changedAt := time.Date(2026, 8, 2, 12, 34, 56, 789, time.FixedZone("fixture", 3600))
			clockCalls := 0
			service.now = func() time.Time { clockCalls++; return changedAt }

			got, err := service.ReconfigurePublication(context.Background(), ReconfigurePublicationRequest{
				Scope: registered.Binding.Scope, ExpectedBinding: registered.Binding,
				ExpectedPublicationBindingDigest: constraint, Expected: current,
				Classification: classification, Actor: actor,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.Classification != classification || got.PolicyRevision != 2 ||
				got.ObservedOriginDigest != current.ObservedOriginDigest || got.ConfiguredOriginDigest == nil ||
				*got.ConfiguredOriginDigest != current.ObservedOriginDigest || got.TransitionKind != "configured" ||
				got.ChangedBy == nil || !reflect.DeepEqual(*got.ChangedBy, actor) || got.ChangedAt == nil ||
				!got.ChangedAt.Equal(changedAt.UTC()) || got.ChangedAt.Location() != time.UTC || clockCalls != 1 {
				t.Fatalf("ReconfigurePublication(%q)=%+v clockCalls=%d", classification, got, clockCalls)
			}
			readback, err := service.PublicationConfiguration(context.Background(), registered.Binding.Scope)
			if err != nil || !reflect.DeepEqual(readback, got) {
				t.Fatalf("PublicationConfiguration()=(%+v,%v), want %+v", readback, err, got)
			}
			if raw := capturePublicationRawState(t, store); len(raw.history) != 2 {
				t.Fatalf("configured history rows=%d, want 2", len(raw.history))
			}

			*got.ConfiguredOriginDigest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
			got.ChangedBy.HumanPrincipalID = "00000000-0000-4000-8000-000000000099"
			*got.ChangedAt = got.ChangedAt.Add(time.Hour)
			again, err := service.PublicationConfiguration(context.Background(), registered.Binding.Scope)
			if err != nil || !reflect.DeepEqual(again, readback) {
				t.Fatalf("mutated caller result changed readback=(%+v,%v), want %+v", again, err, readback)
			}
		})
	}
}

func TestPublicationConfigurationReadsBootstrapWithIndependentObservedOrigin(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	runGit(t, repository.root, "remote", "add", "origin", "https://github.com/acme/wormhole.git")
	store, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	wantOrigin, err := InspectPublicationOrigin(context.Background(), repository.root)
	if err != nil {
		t.Fatal(err)
	}
	before := capturePublicationRawState(t, store)

	got, err := service.PublicationConfiguration(context.Background(), registered.Binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if got.Classification != types.PublicationUnclassified || got.PolicyRevision != 1 ||
		got.ObservedOriginDigest != wantOrigin || got.ConfiguredOriginDigest != nil ||
		got.TransitionKind != "bootstrap" || got.ChangedBy != nil || got.ChangedAt != nil {
		t.Fatalf("bootstrap PublicationConfiguration()=%+v", got)
	}
	if after := capturePublicationRawState(t, store); !reflect.DeepEqual(after, before) {
		t.Fatalf("bootstrap read mutated publication state\nbefore=%v\nafter=%v", before, after)
	}
}

type publicationRawState struct {
	policies [][]string
	history  [][]string
}

func capturePublicationRawState(t *testing.T, store *localstore.Store) publicationRawState {
	t.Helper()
	return publicationRawState{
		policies: queryImportRawRows(t, store, `
			SELECT quote(project_id),quote(workspace_id),quote(repository_identity_json),quote(origin_digest),
			       quote(classification),quote(policy_revision),quote(transition_kind),quote(changed_actor_json),
			       quote(changed_at),quote(created_at),quote(updated_at)
			FROM workspace_publication_policies ORDER BY project_id,workspace_id`),
		history: queryImportRawRows(t, store, `
			SELECT quote(project_id),quote(workspace_id),quote(policy_revision),quote(repository_identity_json),
			       quote(origin_digest),quote(classification),quote(transition_kind),quote(changed_actor_json),
			       quote(changed_at),quote(recorded_at)
			FROM workspace_publication_policy_history ORDER BY project_id,workspace_id,policy_revision`),
	}
}

type publicationServiceFixture struct {
	repository gitRepository
	store      *localstore.Store
	service    *Service
	binding    types.WorkspaceBinding
}

func newPublicationServiceFixture(t *testing.T, projectID, origin string) publicationServiceFixture {
	t.Helper()
	repository := createGitRepository(t, projectID)
	if origin != "" {
		runGit(t, repository.root, "remote", "add", "origin", origin)
	}
	store, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	return publicationServiceFixture{repository: repository, store: store, service: service, binding: registered.Binding}
}

func mustPublicationConfiguration(t *testing.T, service *Service, scope types.WorkspaceScope) PublicationConfiguration {
	t.Helper()
	configuration, err := service.PublicationConfiguration(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	return configuration
}

func publicationRequest(
	t *testing.T,
	binding types.WorkspaceBinding,
	expected PublicationConfiguration,
	classification types.PublicationClassification,
	actor types.ActorEnvelope,
) ReconfigurePublicationRequest {
	t.Helper()
	constraint, err := DigestPublicationBindingConstraint(binding.Repository, expected.ObservedOriginDigest)
	if err != nil {
		t.Fatal(err)
	}
	return ReconfigurePublicationRequest{
		Scope: binding.Scope, ExpectedBinding: binding,
		ExpectedPublicationBindingDigest: constraint, Expected: clonePublicationConfiguration(expected),
		Classification: classification, Actor: actor,
	}
}

func configurePublicationForTest(
	t *testing.T,
	fixture publicationServiceFixture,
	classification types.PublicationClassification,
	actor types.ActorEnvelope,
	changedAt time.Time,
) PublicationConfiguration {
	t.Helper()
	current := mustPublicationConfiguration(t, fixture.service, fixture.binding.Scope)
	fixture.service.now = func() time.Time { return changedAt }
	configured, err := fixture.service.ReconfigurePublication(context.Background(), publicationRequest(
		t, fixture.binding, current, classification, actor,
	))
	if err != nil {
		t.Fatal(err)
	}
	return configured
}

func publicationDigest(value byte) string {
	return "sha256:" + strings.Repeat(string([]byte{value}), 64)
}

func readPublicationPolicyState(
	t *testing.T,
	service *Service,
	scope types.WorkspaceScope,
) (localstore.WorkspacePublicationPolicyRecord, []localstore.WorkspacePublicationPolicyRecord) {
	t.Helper()
	var current localstore.WorkspacePublicationPolicyRecord
	var history []localstore.WorkspacePublicationPolicyRecord
	if err := service.repo.WithImmediateWorkspace(context.Background(), scope, func(tx *localstore.WorkspaceMutationTx) error {
		var err error
		current, err = tx.PublicationPolicy(context.Background())
		if err != nil {
			return err
		}
		history, err = tx.PublicationPolicyHistory(context.Background())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return current, history
}
