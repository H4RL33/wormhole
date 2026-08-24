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

func TestPublicationReviewInTransactionReturnsOwnedTask5Evidence(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
	workspace, err := fixture.service.repo.Workspace(context.Background(), fixture.binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	observer := fixture.service.publicationTrustObserver()
	outside, err := observer(context.Background(), workspace.Binding)
	if err != nil {
		t.Fatal(err)
	}
	var evidence publicationReviewTransactionEvidence
	var attempt publicationTransitionAttempt
	if err := fixture.service.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		var transactionErr error
		evidence, transactionErr = fixture.service.publicationReviewInTransaction(
			context.Background(), tx, workspace, outside, observer, &attempt,
		)
		return transactionErr
	}); err != nil {
		t.Fatal(err)
	}
	if evidence.composed.status.Binding != fixture.binding || evidence.trust.origin.digest == "" ||
		evidence.policy.PolicyRevision != 1 || evidence.semanticDiffDigest == "" ||
		evidence.envelope.Scope != fixture.binding.Scope || evidence.reviewDigest == "" ||
		evidence.status.PublicationReviewDigest != evidence.reviewDigest ||
		evidence.diff.PublicationReviewDigest != evidence.reviewDigest {
		t.Fatalf("Task-5 transaction evidence is incomplete: %+v", evidence)
	}
	evidence.composed.status.AcceptedSnapshot.Project.Name = "caller mutation"
	evidence.semanticDiff.Changes = append(evidence.semanticDiff.Changes, Change{})
	fresh := mustServiceStatus(t, fixture.service, fixture.binding.Scope)
	if fresh.AcceptedSnapshot.Project.Name == "caller mutation" || len(fresh.AcceptedSnapshot.Tasks) != 0 {
		t.Fatalf("transaction evidence aliased persisted state: %+v", fresh)
	}
}

func TestObservePublicationTrustFinalGitPositionWinsOverOriginOutcome(t *testing.T) {
	for _, failingOrigin := range []bool{false, true} {
		t.Run(fmt.Sprintf("origin_failing_%t", failingOrigin), func(t *testing.T) {
			fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
			stableOrigin, err := observePublicationOrigin(context.Background(), fixture.repository.root)
			if err != nil {
				t.Fatal(err)
			}
			originCause := errors.New("synthetic origin failure")
			_, err = observePublicationTrustWithObservers(
				context.Background(), fixture.binding, observeGitBaseOutside,
				func(context.Context, string) (publicationOriginObservation, error) {
					runGit(t, fixture.repository.root, "switch", "-c", "raced")
					runGit(t, fixture.repository.root, "commit", "--allow-empty", "-m", "race trust observation")
					if failingOrigin {
						return publicationOriginObservation{}, originCause
					}
					return stableOrigin, nil
				},
			)
			if !errors.Is(err, ErrGitObservationChanged) {
				t.Fatalf("Git-raced trust observation error=%v, want ErrGitObservationChanged", err)
			}
		})
	}
}

func TestPublicationReviewComponentObserverFailuresAreNormalized(t *testing.T) {
	tests := []struct {
		name   string
		inside bool
		git    bool
		want   error
	}{
		{name: "outside git", git: true, want: ErrGitObservationChanged},
		{name: "inside git", inside: true, git: true, want: ErrGitObservationChanged},
		{name: "outside origin", want: ErrGitOriginChanged},
		{name: "inside origin", inside: true, want: ErrGitOriginChanged},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
			cause := errors.New("component observer failed")
			gitObserver := fixture.service.observeGitBase
			originObserver := fixture.service.observePublicationOrigin
			calls := 0
			if test.git {
				fixture.service.observeGitBase = func(ctx context.Context, req ObserveGitBaseRequest) (gitBaseObservation, error) {
					calls++
					if !test.inside || calls == 2 {
						return gitBaseObservation{}, cause
					}
					return gitObserver(ctx, req)
				}
			} else {
				fixture.service.observePublicationOrigin = func(ctx context.Context, root string) (publicationOriginObservation, error) {
					calls++
					if !test.inside || calls == 2 {
						return publicationOriginObservation{}, cause
					}
					return originObserver(ctx, root)
				}
			}
			before := capturePublicationRawState(t, fixture.store)
			for _, surface := range []string{"status", "diff"} {
				calls = 0
				if surface == "status" {
					got, err := fixture.service.Status(context.Background(), fixture.binding.Scope)
					if !errors.Is(err, test.want) || !errors.Is(err, cause) || !reflect.DeepEqual(got, WorkspaceStatus{}) {
						t.Fatalf("failed Status=(%+v,%v)", got, err)
					}
				} else {
					got, err := fixture.service.Diff(context.Background(), fixture.binding.Scope)
					if !errors.Is(err, test.want) || !errors.Is(err, cause) || !reflect.DeepEqual(got, WorkspaceDiff{}) {
						t.Fatalf("failed Diff=(%+v,%v)", got, err)
					}
				}
			}
			if after := capturePublicationRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
				t.Fatalf("observer failure changed policy\nbefore=%v\nafter=%v", before, after)
			}
		})
	}
}

func TestPublicationReviewStatusAndDiffAgreeFromOneSnapshot(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
	accepted := fixture.mustAcceptedSnapshot(t)
	operation := servicePutTaskOperation(
		accepted,
		"99999999-9999-4999-8999-999999999991",
		"22222222-2222-4222-8222-222222222222",
		"publication review",
	)
	if _, err := fixture.service.Apply(context.Background(), fixture.binding.Scope, operation); err != nil {
		t.Fatal(err)
	}

	status, err := fixture.service.Status(context.Background(), fixture.binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := fixture.service.Diff(context.Background(), fixture.binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if diff.CandidateDigest != status.CandidateDigest || diff.OverlayGeneration != status.OverlayGeneration ||
		diff.PublicationClassification != status.PublicationClassification ||
		diff.PublicationReviewDigest != status.PublicationReviewDigest {
		t.Fatalf("Status and Diff disagree: status=%+v diff=%+v", status, diff)
	}
	if status.PublicationClassification != types.PublicationUnclassified || !validPublicationDigest(status.PublicationReviewDigest) {
		t.Fatalf("unclassified Status review = %+v", status)
	}
	if len(diff.SemanticDiff.Changes) != 1 || len(diff.SemanticDiff.Changes[0].Fields) != 1 ||
		diff.SemanticDiff.Changes[0].Fields[0].Actor == nil ||
		!equalPublicationActors(*diff.SemanticDiff.Changes[0].Fields[0].Actor, operation.Actor) {
		t.Fatalf("attributed Diff = %+v", diff.SemanticDiff)
	}
	_, semanticDigest, err := encodePublicationSemanticDiff(diff.SemanticDiff)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := InspectPublicationOrigin(context.Background(), fixture.repository.root)
	if err != nil {
		t.Fatal(err)
	}
	_, wantReview, err := encodePublicationReviewEnvelope(publicationReviewEnvelopeV1{
		SchemaVersion:       publicationReviewSchemaVersion,
		Kind:                publicationReviewKind,
		Scope:               fixture.binding.Scope,
		Repository:          fixture.binding.Repository,
		OriginDigest:        origin,
		Classification:      types.PublicationUnclassified,
		PolicyRevision:      1,
		AcceptedRef:         fixture.binding.AcceptedRef,
		AcceptedCommitSHA:   fixture.binding.AcceptedCommitSHA,
		AcceptedTreeDigest:  state.Digest(fixture.binding.AcceptedTreeDigest),
		CandidateTreeDigest: diff.CandidateDigest,
		SemanticDiffDigest:  semanticDigest,
		OverlayGeneration:   diff.OverlayGeneration,
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.PublicationReviewDigest != wantReview {
		t.Fatalf("review digest=%q, want manually encoded %q", status.PublicationReviewDigest, wantReview)
	}
}

func TestPublicationReviewGitDriftReturnsExactZeroWithoutPolicyMutation(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
	configurePublicationForTest(t, fixture, types.PublicationPublicGit, diffActorEnvelope(), time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
	before := capturePublicationRawState(t, fixture.store)
	runGit(t, fixture.repository.root, "commit", "--allow-empty", "-m", "change head")

	status, statusErr := fixture.service.Status(context.Background(), fixture.binding.Scope)
	diff, diffErr := fixture.service.Diff(context.Background(), fixture.binding.Scope)
	if !errors.Is(statusErr, ErrGitObservationChanged) || !reflect.DeepEqual(status, WorkspaceStatus{}) {
		t.Fatalf("HEAD-drift Status=(%+v,%v)", status, statusErr)
	}
	if !errors.Is(diffErr, ErrGitObservationChanged) || !reflect.DeepEqual(diff, WorkspaceDiff{}) {
		t.Fatalf("HEAD-drift Diff=(%+v,%v)", diff, diffErr)
	}
	if after := capturePublicationRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
		t.Fatalf("HEAD drift changed publication state\nbefore=%v\nafter=%v", before, after)
	}
}

func TestPublicationReviewFullBundleRacesFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*publicationTrustObservation) error
		want   error
	}{
		{name: "root", want: ErrGitObservationChanged, mutate: func(observed *publicationTrustObservation) error {
			observed.git.root = filepath.Join(observed.git.root, "raced")
			return nil
		}},
		{name: "checkout device", want: ErrGitObservationChanged, mutate: func(observed *publicationTrustObservation) error {
			observed.git.checkout.Device++
			return nil
		}},
		{name: "checkout inode", want: ErrGitObservationChanged, mutate: func(observed *publicationTrustObservation) error {
			observed.git.checkout.Inode++
			return nil
		}},
		{name: "ref", want: ErrGitObservationChanged, mutate: func(observed *publicationTrustObservation) error {
			observed.git.acceptedRef = "refs/heads/raced"
			return nil
		}},
		{name: "commit", want: ErrGitObservationChanged, mutate: func(observed *publicationTrustObservation) error {
			observed.git.commit = strings.Repeat("a", 40)
			return nil
		}},
		{name: "tree bytes", want: ErrGitObservationChanged, mutate: func(observed *publicationTrustObservation) error {
			observed.git.tree[0].Data = append([]byte(nil), observed.git.tree[0].Data...)
			observed.git.tree[0].Data[0] ^= 1
			return nil
		}},
		{name: "snapshot digest", want: ErrGitObservationChanged, mutate: func(observed *publicationTrustObservation) error {
			observed.git.snapshot.Digest = state.Digest("sha256:" + strings.Repeat("a", 64))
			return nil
		}},
		{name: "snapshot content", want: ErrGitObservationChanged, mutate: func(observed *publicationTrustObservation) error {
			observed.git.snapshot.Project.Name = "raced"
			return nil
		}},
		{name: "snapshot project", want: ErrGitObservationChanged, mutate: func(observed *publicationTrustObservation) error {
			observed.git.snapshot.Config.ProjectID = "00000000-0000-4000-8000-000000000099"
			return nil
		}},
		{name: "snapshot repository", want: ErrGitObservationChanged, mutate: func(observed *publicationTrustObservation) error {
			observed.git.snapshot.Config.Repository = types.RepositoryIdentity{
				Provider: "github", ImmutableID: "raced", CanonicalRemote: "https://github.com/acme/raced",
			}
			return nil
		}},
		{name: "origin root", want: ErrGitOriginChanged, mutate: func(observed *publicationTrustObservation) error {
			observed.origin.root = filepath.Join(observed.origin.root, "raced")
			return nil
		}},
		{name: "origin checkout device", want: ErrGitOriginChanged, mutate: func(observed *publicationTrustObservation) error {
			observed.origin.checkout.Device++
			return nil
		}},
		{name: "origin checkout inode", want: ErrGitOriginChanged, mutate: func(observed *publicationTrustObservation) error {
			observed.origin.checkout.Inode++
			return nil
		}},
		{name: "origin preimage", want: ErrGitOriginChanged, mutate: func(observed *publicationTrustObservation) error {
			observed.origin.origin.Path = "acme/raced"
			return nil
		}},
		{name: "origin digest", want: ErrGitOriginChanged, mutate: func(observed *publicationTrustObservation) error {
			observed.origin.digest = state.Digest("sha256:" + strings.Repeat("a", 64))
			return nil
		}},
		{name: "Git wins simultaneous origin mismatch", want: ErrGitObservationChanged, mutate: func(observed *publicationTrustObservation) error {
			observed.git.acceptedRef = "refs/heads/raced"
			observed.origin.digest = state.Digest("sha256:" + strings.Repeat("a", 64))
			return nil
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
			stable, err := observePublicationTrustOutside(context.Background(), fixture.binding)
			if err != nil {
				t.Fatal(err)
			}
			raced := clonePublicationTrustObservation(stable)
			if err := test.mutate(&raced); err != nil {
				t.Fatal(err)
			}
			calls := 0
			fixture.service.observePublicationTrust = func(context.Context, types.WorkspaceBinding) (publicationTrustObservation, error) {
				calls++
				if calls == 1 {
					return clonePublicationTrustObservation(stable), nil
				}
				return raced, nil
			}
			before := capturePublicationRawState(t, fixture.store)
			got, err := fixture.service.Status(context.Background(), fixture.binding.Scope)
			if !errors.Is(err, test.want) || !reflect.DeepEqual(got, WorkspaceStatus{}) || calls != 2 {
				t.Fatalf("raced Status=(%+v,%v) calls=%d", got, err, calls)
			}
			if after := capturePublicationRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
				t.Fatalf("bundle race changed publication state\nbefore=%v\nafter=%v", before, after)
			}
		})
	}
}

func TestPublicationReviewRejectsBundleWithDisagreeingHalves(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
	stable, err := observePublicationTrustOutside(context.Background(), fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	stable.origin.checkout.Inode++
	fixture.service.observePublicationTrust = func(context.Context, types.WorkspaceBinding) (publicationTrustObservation, error) {
		return clonePublicationTrustObservation(stable), nil
	}
	got, err := fixture.service.Status(context.Background(), fixture.binding.Scope)
	if !errors.Is(err, ErrGitOriginChanged) || !reflect.DeepEqual(got, WorkspaceStatus{}) {
		t.Fatalf("disagreeing trust halves Status=(%+v,%v)", got, err)
	}
}

func TestPublicationReviewStableMismatchInvalidatesOnceAndContinues(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/one.git")
	configured := configurePublicationForTest(t, fixture, types.PublicationPublicGit, diffActorEnvelope(), time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
	runGit(t, fixture.repository.root, "remote", "set-url", "origin", "https://github.com/acme/two.git")
	fixture.service.now = func() time.Time { return time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC) }

	first, err := fixture.service.Status(context.Background(), fixture.binding.Scope)
	if err != nil || first.PublicationClassification != types.PublicationUnclassified || !validPublicationDigest(first.PublicationReviewDigest) {
		t.Fatalf("first invalidated Status=(%+v,%v)", first, err)
	}
	policy := readPublicationPolicy(t, fixture.service, fixture.binding.Scope)
	if policy.PolicyRevision != configured.PolicyRevision+1 || policy.TransitionKind != "origin_invalidated" {
		t.Fatalf("invalidated policy=%+v", policy)
	}
	fixture.service.now = func() time.Time { panic("sticky invalidation consulted clock twice") }
	second, err := fixture.service.Status(context.Background(), fixture.binding.Scope)
	if err != nil || !reflect.DeepEqual(second, first) {
		t.Fatalf("second invalidated Status=(%+v,%v), want %+v", second, err, first)
	}
}

func TestPublicationReviewCompositionCorruptionRollsBackInvalidation(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/one.git")
	configurePublicationForTest(t, fixture, types.PublicationPublicGit, diffActorEnvelope(), time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
	runGit(t, fixture.repository.root, "remote", "set-url", "origin", "https://github.com/acme/two.git")
	if _, err := fixture.store.DB().Exec(`
		INSERT INTO workspace_candidates
		(project_id,workspace_id,accepted_base_digest,working_tree_digest,direct_tree,
		 rebased_tree,rebased_through_generation,imported_by,imported_at)
		VALUES (?,?,?,?,X'00',NULL,0,?,?)
	`, fixture.binding.Scope.ProjectID, fixture.binding.Scope.WorkspaceID,
		fixture.binding.AcceptedTreeDigest, fixture.binding.AcceptedTreeDigest,
		"00000000-0000-4000-8000-000000000071", time.Date(2026, 8, 2, 12, 30, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	fixture.service.now = func() time.Time { return time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC) }
	before := capturePublicationRawState(t, fixture.store)

	got, err := fixture.service.Status(context.Background(), fixture.binding.Scope)
	if err == nil || !reflect.DeepEqual(got, WorkspaceStatus{}) {
		t.Fatalf("corrupt candidate Status=(%+v,%v)", got, err)
	}
	if after := capturePublicationRawState(t, fixture.store); !reflect.DeepEqual(after, before) {
		t.Fatalf("composition corruption committed invalidation\nbefore=%v\nafter=%v", before, after)
	}
}

func TestPublicationReviewRepositoryMismatchPrecedesOriginMismatch(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/one.git")
	configured := configurePublicationForTest(t, fixture, types.PublicationPrivateGit, diffActorEnvelope(), time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
	oldRepository := types.RepositoryIdentity{Provider: "github", ImmutableID: "old-repository", CanonicalRemote: "https://github.com/acme/old"}
	encoded, err := json.Marshal(oldRepository)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"workspace_publication_policies", "workspace_publication_policy_history"} {
		if _, err := fixture.store.DB().Exec(`UPDATE `+table+` SET repository_identity_json=? WHERE project_id=? AND workspace_id=?`,
			string(encoded), fixture.binding.Scope.ProjectID, fixture.binding.Scope.WorkspaceID); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, fixture.repository.root, "remote", "set-url", "origin", "https://github.com/acme/two.git")
	fixture.service.now = func() time.Time { return time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC) }

	got, err := fixture.service.Status(context.Background(), fixture.binding.Scope)
	if err != nil || got.PublicationClassification != types.PublicationUnclassified {
		t.Fatalf("repository mismatch Status=(%+v,%v)", got, err)
	}
	policy := readPublicationPolicy(t, fixture.service, fixture.binding.Scope)
	if policy.PolicyRevision != configured.PolicyRevision+1 || policy.TransitionKind != "repository_invalidated" {
		t.Fatalf("repository precedence policy=%+v", policy)
	}
}

func TestPublicationReviewUnknownCommitConfirmationDoesNotRetry(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/one.git")
	configured := configurePublicationForTest(t, fixture, types.PublicationPublicGit, diffActorEnvelope(), time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
	runGit(t, fixture.repository.root, "remote", "set-url", "origin", "https://github.com/acme/two.git")
	fixture.service.now = func() time.Time { return time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC) }
	realTransaction := fixture.service.withImmediateWorkspace
	calls := 0
	fixture.service.withImmediateWorkspace = func(ctx context.Context, scope types.WorkspaceScope, callback func(*localstore.WorkspaceMutationTx) error) error {
		calls++
		if err := realTransaction(ctx, scope, callback); err != nil {
			return err
		}
		return fmt.Errorf("%w: synthetic post-commit ambiguity", localstore.ErrCommitOutcomeUnknown)
	}

	got, err := fixture.service.Status(context.Background(), fixture.binding.Scope)
	if err != nil || got.PublicationClassification != types.PublicationUnclassified || !validPublicationDigest(got.PublicationReviewDigest) || calls != 1 {
		t.Fatalf("unknown-commit Status=(%+v,%v) calls=%d", got, err, calls)
	}
	policy := readPublicationPolicy(t, fixture.service, fixture.binding.Scope)
	if policy.PolicyRevision != configured.PolicyRevision+1 {
		t.Fatalf("confirmed policy revision=%d", policy.PolicyRevision)
	}
}

func TestPublicationReviewUnknownCommitWithoutTransitionReturnsZero(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
	realTransaction := fixture.service.withImmediateWorkspace
	fixture.service.withImmediateWorkspace = func(ctx context.Context, scope types.WorkspaceScope, callback func(*localstore.WorkspaceMutationTx) error) error {
		if err := realTransaction(ctx, scope, callback); err != nil {
			return err
		}
		return fmt.Errorf("%w: synthetic read-only ambiguity", localstore.ErrCommitOutcomeUnknown)
	}

	got, err := fixture.service.Diff(context.Background(), fixture.binding.Scope)
	if !errors.Is(err, localstore.ErrCommitOutcomeUnknown) || !reflect.DeepEqual(got, WorkspaceDiff{}) {
		t.Fatalf("read-only unknown-commit Diff=(%+v,%v)", got, err)
	}
}

func TestApplyBatchDoesNotObservePublicationReview(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
	accepted := fixture.mustAcceptedSnapshot(t)
	fixture.service.observePublicationTrust = func(context.Context, types.WorkspaceBinding) (publicationTrustObservation, error) {
		panic("ApplyBatch observed publication trust")
	}
	fixture.service.now = func() time.Time { panic("ApplyBatch consulted publication clock") }
	operation := servicePutTaskOperation(
		accepted,
		"99999999-9999-4999-8999-999999999991",
		"22222222-2222-4222-8222-222222222222",
		"apply without review",
	)

	got, err := fixture.service.ApplyBatch(context.Background(), fixture.binding.Scope, []state.OperationV1{operation})
	if err != nil {
		t.Fatal(err)
	}
	if got.PublicationClassification != "" || got.PublicationReviewDigest != "" {
		t.Fatalf("ApplyBatch populated review fields: %+v", got)
	}
}

func TestPublicationReviewDigestIsWorkspaceScopedForOtherwiseEqualReviews(t *testing.T) {
	projectID := "00000000-0000-4000-8000-000000000001"
	firstRepository := createGitRepository(t, projectID)
	runGit(t, firstRepository.root, "remote", "add", "origin", firstRepository.root)
	cloneParent := t.TempDir()
	secondRoot := filepath.Join(cloneParent, "second")
	runGit(t, cloneParent, "clone", firstRepository.root, secondRoot)
	secondRepository := gitRepository{
		root: secondRoot, projectID: projectID, identity: firstRepository.identity, commit: firstRepository.commit,
	}
	_, service := openProjectStateService(t, "")
	first := registerGitRepository(t, service, firstRepository)
	second := registerGitRepository(t, service, secondRepository)
	firstAccepted, err := service.repo.Workspace(context.Background(), first.Binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	operation := servicePutTaskOperation(
		firstAccepted.Snapshot,
		"99999999-9999-4999-8999-999999999991",
		"22222222-2222-4222-8222-222222222222",
		"same candidate",
	)
	for _, scope := range []types.WorkspaceScope{first.Binding.Scope, second.Binding.Scope} {
		if _, err := service.Apply(context.Background(), scope, operation); err != nil {
			t.Fatal(err)
		}
	}

	firstDiff, err := service.Diff(context.Background(), first.Binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	secondDiff, err := service.Diff(context.Background(), second.Binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(firstDiff.SemanticDiff, secondDiff.SemanticDiff) ||
		firstDiff.CandidateDigest != secondDiff.CandidateDigest ||
		firstDiff.OverlayGeneration != secondDiff.OverlayGeneration ||
		firstDiff.PublicationClassification != secondDiff.PublicationClassification {
		t.Fatalf("otherwise-equal workspace reviews differ before envelope binding\nfirst=%+v\nsecond=%+v", firstDiff, secondDiff)
	}
	if firstDiff.PublicationReviewDigest == secondDiff.PublicationReviewDigest {
		t.Fatalf("workspace-scoped review digests were reusable: %q", firstDiff.PublicationReviewDigest)
	}
}

func TestPublicationReviewResultsAreOwnedAndRestartStable(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, service := openProjectStateServiceAt(t, databasePath)
	registered := registerGitRepository(t, service, repository)
	accepted, err := service.repo.Workspace(context.Background(), registered.Binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	operation := servicePutTaskOperation(
		accepted.Snapshot,
		"99999999-9999-4999-8999-999999999991",
		"22222222-2222-4222-8222-222222222222",
		"owned result",
	)
	if _, err := service.Apply(context.Background(), registered.Binding.Scope, operation); err != nil {
		t.Fatal(err)
	}
	status, err := service.Status(context.Background(), registered.Binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := service.Diff(context.Background(), registered.Binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	wantStatus, err := clonePublicationReviewStatus(status)
	if err != nil {
		t.Fatal(err)
	}
	wantDiff, err := clonePublicationReviewDiff(diff)
	if err != nil {
		t.Fatal(err)
	}
	status.AcceptedSnapshot.Project.Name = "caller mutation"
	delete(status.AcceptedSnapshot.Tasks, "22222222-2222-4222-8222-222222222222")
	diff.SemanticDiff.Changes[0].Fields[0].After.Value[0] ^= 1
	diff.SemanticDiff.Changes[0].Fields[0].Actor = nil

	freshStatus, err := service.Status(context.Background(), registered.Binding.Scope)
	if err != nil || !reflect.DeepEqual(freshStatus, wantStatus) {
		t.Fatalf("fresh owned Status=(%+v,%v), want %+v", freshStatus, err, wantStatus)
	}
	freshDiff, err := service.Diff(context.Background(), registered.Binding.Scope)
	if err != nil || !reflect.DeepEqual(freshDiff, wantDiff) {
		t.Fatalf("fresh owned Diff=(%+v,%v), want %+v", freshDiff, err, wantDiff)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	_, reopened := openProjectStateServiceAt(t, databasePath)
	restartedStatus, err := reopened.Status(context.Background(), registered.Binding.Scope)
	if err != nil || !reflect.DeepEqual(restartedStatus, wantStatus) {
		t.Fatalf("restarted Status=(%+v,%v), want %+v", restartedStatus, err, wantStatus)
	}
	restartedDiff, err := reopened.Diff(context.Background(), registered.Binding.Scope)
	if err != nil || !reflect.DeepEqual(restartedDiff, wantDiff) {
		t.Fatalf("restarted Diff=(%+v,%v), want %+v", restartedDiff, err, wantDiff)
	}
}

func TestPublicationReviewWriterBarrierExcludesConcurrentApply(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
	accepted := fixture.mustAcceptedSnapshot(t)
	stable, err := observePublicationTrustOutside(context.Background(), fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	insideStarted := make(chan struct{})
	releaseInside := make(chan struct{})
	calls := 0
	fixture.service.observePublicationTrust = func(context.Context, types.WorkspaceBinding) (publicationTrustObservation, error) {
		calls++
		if calls == 2 {
			close(insideStarted)
			<-releaseInside
		}
		return clonePublicationTrustObservation(stable), nil
	}
	statusResult := make(chan struct {
		status WorkspaceStatus
		err    error
	}, 1)
	go func() {
		status, err := fixture.service.Status(context.Background(), fixture.binding.Scope)
		statusResult <- struct {
			status WorkspaceStatus
			err    error
		}{status: status, err: err}
	}()
	<-insideStarted
	operation := servicePutTaskOperation(
		accepted,
		"99999999-9999-4999-8999-999999999991",
		"22222222-2222-4222-8222-222222222222",
		"concurrent apply",
	)
	applyResult := make(chan struct {
		status WorkspaceStatus
		err    error
	}, 1)
	go func() {
		status, err := fixture.service.Apply(context.Background(), fixture.binding.Scope, operation)
		applyResult <- struct {
			status WorkspaceStatus
			err    error
		}{status: status, err: err}
	}()
	select {
	case early := <-applyResult:
		t.Fatalf("Apply completed through held review writer barrier: %+v", early)
	case <-time.After(100 * time.Millisecond):
	}
	if count := countServiceOperations(t, fixture.store, fixture.binding.Scope); count != 0 {
		t.Fatalf("Apply committed %d operations through held writer barrier", count)
	}
	close(releaseInside)
	reviewed := <-statusResult
	if reviewed.err != nil || reviewed.status.CandidateDigest != accepted.Digest || reviewed.status.OverlayGeneration != 0 {
		t.Fatalf("pre-writer review=(%+v,%v)", reviewed.status, reviewed.err)
	}
	applied := <-applyResult
	if applied.err != nil || applied.status.OverlayGeneration != 1 || applied.status.CandidateDigest == accepted.Digest {
		t.Fatalf("released Apply=(%+v,%v)", applied.status, applied.err)
	}
	fresh := mustServiceStatus(t, fixture.service, fixture.binding.Scope)
	if fresh.OverlayGeneration != 1 || fresh.CandidateDigest != applied.status.CandidateDigest ||
		fresh.PublicationReviewDigest == reviewed.status.PublicationReviewDigest {
		t.Fatalf("post-writer review=%+v, pre-writer=%+v", fresh, reviewed.status)
	}
}

func TestPublicationReviewDigestIsProjectScopedForOtherwiseEqualEnvelope(t *testing.T) {
	first := publicationReviewFixture()
	second := first
	second.Scope.ProjectID = "00000000-0000-4000-8000-000000000099"
	_, firstDigest, err := encodePublicationReviewEnvelope(first)
	if err != nil {
		t.Fatal(err)
	}
	_, secondDigest, err := encodePublicationReviewEnvelope(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest == secondDigest {
		t.Fatalf("cross-project review digest was reusable: %q", firstDigest)
	}
}

func TestPublicationReviewClassificationSurvivesHumanAndAgentChanges(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
	configured := configurePublicationForTest(t, fixture, types.PublicationPublicGit, diffActorEnvelope(), time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
	accepted := fixture.mustAcceptedSnapshot(t)
	human := servicePutTaskOperation(
		accepted,
		"99999999-9999-4999-8999-999999999991",
		"22222222-2222-4222-8222-222222222222",
		"human change",
	)
	intermediate, err := state.ApplyOperation(accepted, human)
	if err != nil {
		t.Fatal(err)
	}
	agent := servicePutTaskOperation(
		intermediate,
		"99999999-9999-4999-8999-999999999992",
		"33333333-3333-4333-8333-333333333333",
		"agent change",
	)
	agent.Actor = types.ActorEnvelope{
		ActorKind: types.ActorAgent, AgentID: "11111111-1111-4111-8111-111111111112",
		AccountableHumanID: "11111111-1111-4111-8111-111111111111",
		SessionID:          "session", HarnessName: "codex", HarnessVersion: "1",
		ModelName: "model", ModelVersion: "1", Assurance: types.AssuranceLocal,
		OccurredAt: time.Date(2026, 8, 2, 12, 30, 0, 0, time.UTC),
	}
	if _, err := fixture.service.ApplyBatch(context.Background(), fixture.binding.Scope, []state.OperationV1{human, agent}); err != nil {
		t.Fatal(err)
	}
	status := mustServiceStatus(t, fixture.service, fixture.binding.Scope)
	policy := readPublicationPolicy(t, fixture.service, fixture.binding.Scope)
	if status.PublicationClassification != types.PublicationPublicGit ||
		policy.PolicyRevision != configured.PolicyRevision {
		t.Fatalf("data actors changed classification: status=%+v policy=%+v", status, policy)
	}
}

func TestPublicationReviewStickyInvalidationSurvivesRestart(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	runGit(t, repository.root, "remote", "add", "origin", "https://github.com/acme/one.git")
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, service := openProjectStateServiceAt(t, databasePath)
	registered := registerGitRepository(t, service, repository)
	fixture := publicationServiceFixture{repository: repository, store: store, service: service, binding: registered.Binding}
	configured := configurePublicationForTest(t, fixture, types.PublicationPrivateGit, diffActorEnvelope(), time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
	runGit(t, repository.root, "remote", "set-url", "origin", "https://github.com/acme/two.git")
	service.now = func() time.Time { return time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC) }
	want := mustServiceStatus(t, service, registered.Binding.Scope)
	if want.PublicationClassification != types.PublicationUnclassified {
		t.Fatalf("sticky invalidation Status=%+v", want)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	_, reopened := openProjectStateServiceAt(t, databasePath)
	got := mustServiceStatus(t, reopened, registered.Binding.Scope)
	policy := readPublicationPolicy(t, reopened, registered.Binding.Scope)
	if !reflect.DeepEqual(got, want) || policy.PolicyRevision != configured.PolicyRevision+1 ||
		policy.TransitionKind != "origin_invalidated" {
		t.Fatalf("restarted invalidation status=%+v policy=%+v, want=%+v", got, policy, want)
	}
}

func (fixture publicationServiceFixture) mustAcceptedSnapshot(t *testing.T) state.Snapshot {
	t.Helper()
	record, err := fixture.service.repo.Workspace(context.Background(), fixture.binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	return record.Snapshot
}
