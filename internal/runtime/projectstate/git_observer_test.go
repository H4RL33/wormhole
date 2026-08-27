package projectstate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestObserveGitBaseAndRefreshWorkspacePublicMethodsExist(t *testing.T) {
	t.Helper()
	_ = (*Service).ObserveGitBase
	_ = (*Service).RefreshWorkspace
}

func TestBranchSwitchDiscardNotApplicableSentinelText(t *testing.T) {
	const want = "projectstate: branch switch discard not applicable"
	if got := ErrBranchSwitchDiscardNotApplicable.Error(); got != want {
		t.Fatalf("ErrBranchSwitchDiscardNotApplicable=%q, want %q", got, want)
	}
}

func TestObserveGitBaseAdvancesCleanBranchSwitch(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	_, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	runGit(t, repository.root, "switch", "-c", "next")

	got, err := service.ObserveGitBase(context.Background(), ObserveGitBaseRequest{
		Scope: registered.Binding.Scope, ExpectedBinding: registered.Binding,
		Root: repository.root, ExpectedCommit: repository.commit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.PreviousRef != "refs/heads/main" || got.ObservedRef != "refs/heads/next" ||
		got.PreviousCommit != repository.commit || got.ObservedCommit != repository.commit ||
		got.PreviousBaseDigest != state.Digest(registered.Binding.AcceptedTreeDigest) ||
		got.ObservedBaseDigest != got.PreviousBaseDigest || got.CandidateAccepted || got.Rebased ||
		got.AcceptedJournalID != nil || got.Conflicts == nil || len(got.Conflicts) != 0 {
		t.Fatalf("ObserveGitBase()=%+v", got)
	}
	status := mustServiceStatus(t, service, registered.Binding.Scope)
	if status.Binding.AcceptedRef != "refs/heads/next" || status.Binding.AcceptedCommitSHA != repository.commit || status.State != "clean" {
		t.Fatalf("Status()=%+v", status)
	}
}

func TestObserveGitBaseRejectsAndDiscardsBranchSwitchProposal(t *testing.T) {
	for _, action := range []BranchSwitchAction{BranchSwitchReject, BranchSwitchDiscard} {
		t.Run(string(action), func(t *testing.T) {
			repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
			_, service := openProjectStateService(t, "")
			registered := registerGitRepository(t, service, repository)
			actor := prepareObserveGitBaseCandidate(t, service, registered.Binding, repository.root)
			runGit(t, repository.root, "switch", "-c", "next")
			req := ObserveGitBaseRequest{
				Scope: registered.Binding.Scope, ExpectedBinding: registered.Binding,
				Root: repository.root, ExpectedCommit: repository.commit, BranchAction: action,
			}
			if action == BranchSwitchDiscard {
				req.RequestID = "10000000-0000-4000-8000-a00000000001"
				req.Actor = actor
			}

			got, err := service.ObserveGitBase(context.Background(), req)
			if action == BranchSwitchReject {
				if !errors.Is(err, ErrBranchSwitchPending) || !reflect.DeepEqual(got, ObserveGitBaseResult{}) {
					t.Fatalf("ObserveGitBase()=(%+v,%v)", got, err)
				}
				status, statusErr := service.Status(context.Background(), registered.Binding.Scope)
				if !errors.Is(statusErr, ErrGitObservationChanged) || !reflect.DeepEqual(status, WorkspaceStatus{}) {
					t.Fatalf("rejected Status()=(%+v,%v)", status, statusErr)
				}
				return
			}
			if err != nil || got.PreviousRef != "refs/heads/main" || got.ObservedRef != "refs/heads/next" ||
				got.CandidateAccepted || got.Rebased || got.Conflicts == nil || len(got.Conflicts) != 0 {
				t.Fatalf("ObserveGitBase()=(%+v,%v)", got, err)
			}
			status := mustServiceStatus(t, service, registered.Binding.Scope)
			if status.Binding.AcceptedRef != "refs/heads/next" || status.State != "clean" ||
				status.CandidateDigest != status.AcceptedSnapshot.Digest {
				t.Fatalf("discarded Status()=%+v", status)
			}
			if err := os.Rename(repository.root, repository.root+"-unavailable"); err != nil {
				t.Fatal(err)
			}
			service.gitBase.now = nil
			retry, err := service.ObserveGitBase(context.Background(), req)
			if err != nil || !reflect.DeepEqual(retry, got) || retry.Conflicts == nil {
				t.Fatalf("retry ObserveGitBase()=(%+v,%v), want %+v", retry, err, got)
			}
		})
	}
}

func TestBranchSwitchDiscardApplicabilityAndDetachedSameSHA(t *testing.T) {
	for _, test := range []struct {
		name                 string
		proposal, activeOnly bool
		rebasedOnly          bool
		changeRef            func(*testing.T, gitRepository)
		applicable           bool
		wantRef              string
	}{
		{name: "same ref with proposal", proposal: true, wantRef: "refs/heads/main"},
		{name: "changed ref without proposal", changeRef: func(t *testing.T, repository gitRepository) {
			runGit(t, repository.root, "switch", "-c", "next")
		}, wantRef: "refs/heads/next"},
		{name: "branch to detached with proposal", proposal: true, changeRef: func(t *testing.T, repository gitRepository) {
			runGit(t, repository.root, "checkout", "--detach", repository.commit)
		}, applicable: true, wantRef: ""},
		{name: "changed ref with active row only", activeOnly: true, changeRef: func(t *testing.T, repository gitRepository) {
			runGit(t, repository.root, "switch", "-c", "next")
		}, applicable: true, wantRef: "refs/heads/next"},
		{name: "changed ref with rebased row only", rebasedOnly: true, changeRef: func(t *testing.T, repository gitRepository) {
			runGit(t, repository.root, "switch", "-c", "next")
		}, applicable: true, wantRef: "refs/heads/next"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
			_, service := openProjectStateService(t, "")
			registered := registerGitRepository(t, service, repository)
			actor := diffActorEnvelope()
			if test.proposal {
				actor = prepareObserveGitBaseCandidate(t, service, registered.Binding, repository.root)
			}
			if test.activeOnly || test.rebasedOnly {
				accepted := mustServiceStatus(t, service, registered.Binding.Scope).AcceptedSnapshot
				operation := servicePutTaskOperation(accepted,
					"99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "row-only proposal")
				actor = operation.Actor
				if _, err := service.Apply(context.Background(), registered.Binding.Scope, operation); err != nil {
					t.Fatal(err)
				}
				if test.rebasedOnly {
					if err := service.registration.repo.WithImmediateWorkspace(context.Background(), registered.Binding.Scope, func(tx *localstore.WorkspaceMutationTx) error {
						audit, err := readAllOperationRows(context.Background(), tx)
						if err != nil {
							return err
						}
						if len(audit) != 1 {
							return fmt.Errorf("operation audit has %d rows", len(audit))
						}
						return tx.TransitionOperations(context.Background(), []localstore.WorkspaceOperation{audit[0]}, "rebased", nil)
					}); err != nil {
						t.Fatal(err)
					}
				}
			}
			if test.changeRef != nil {
				test.changeRef(t, repository)
			}
			req := ObserveGitBaseRequest{
				Scope: registered.Binding.Scope, ExpectedBinding: registered.Binding,
				Root: repository.root, ExpectedCommit: repository.commit,
				BranchAction: BranchSwitchDiscard, RequestID: "10000000-0000-4000-8000-a00000000002", Actor: actor,
			}
			got, err := service.ObserveGitBase(context.Background(), req)
			if test.applicable {
				if err != nil || got.ObservedRef != test.wantRef || got.Conflicts == nil {
					t.Fatalf("ObserveGitBase()=(%+v,%v)", got, err)
				}
				return
			}
			if !errors.Is(err, ErrBranchSwitchDiscardNotApplicable) || !reflect.DeepEqual(got, ObserveGitBaseResult{}) {
				t.Fatalf("ObserveGitBase()=(%+v,%v), want not applicable", got, err)
			}
			workspace, readErr := service.registration.repo.Workspace(context.Background(), registered.Binding.Scope)
			if readErr != nil || workspace.Binding != registered.Binding {
				t.Fatalf("workspace after not-applicable=(%+v,%v)", workspace, readErr)
			}
			receipt, readErr := service.registration.repo.TransitionReceiptByKey(context.Background(), req.Scope, req.RequestID)
			if readErr != nil || receipt != nil {
				t.Fatalf("receipt after not-applicable=(%+v,%v)", receipt, readErr)
			}
		})
	}
}

func TestBranchSwitchDiscardExactRetryPrecedesMissingGitBindingAndClock(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	_, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	actor := prepareObserveGitBaseCandidate(t, service, registered.Binding, repository.root)
	runGit(t, repository.root, "switch", "-c", "next")
	req := ObserveGitBaseRequest{
		Scope: registered.Binding.Scope, ExpectedBinding: registered.Binding,
		Root: repository.root, ExpectedCommit: repository.commit,
		BranchAction: BranchSwitchDiscard, RequestID: "10000000-0000-4000-8000-a00000000003", Actor: actor,
	}
	want, err := service.ObserveGitBase(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(repository.root, repository.root+"-missing"); err != nil {
		t.Fatal(err)
	}
	service.gitBase.now = nil
	got, err := service.ObserveGitBase(context.Background(), req)
	if err != nil || !reflect.DeepEqual(got, want) || got.Conflicts == nil {
		t.Fatalf("receipt-first retry=(%+v,%v), want %+v", got, err, want)
	}
	collision := req
	collision.Actor.OccurredAt = collision.Actor.OccurredAt.Add(time.Second)
	got, err = service.ObserveGitBase(context.Background(), collision)
	if !errors.Is(err, ErrIdempotencyConflict) || !reflect.DeepEqual(got, ObserveGitBaseResult{}) {
		t.Fatalf("receipt-first collision=(%+v,%v)", got, err)
	}
}

func TestConfirmDiscardCommitUsesExactReceiptWithoutGitAndPreservesUnknownOnFailure(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	_, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	req := ObserveGitBaseRequest{
		Scope: registered.Binding.Scope, ExpectedBinding: registered.Binding,
		Root: repository.root, ExpectedCommit: repository.commit,
		BranchAction: BranchSwitchDiscard, RequestID: "10000000-0000-4000-8000-a00000000009", Actor: diffActorEnvelope(),
	}
	result := ObserveGitBaseResult{
		PreviousCommit:     registered.Binding.AcceptedCommitSHA,
		ObservedCommit:     repository.commit,
		PreviousRef:        registered.Binding.AcceptedRef,
		ObservedRef:        "refs/heads/next",
		PreviousBaseDigest: state.Digest(registered.Binding.AcceptedTreeDigest),
		ObservedBaseDigest: state.Digest(registered.Binding.AcceptedTreeDigest),
		Conflicts:          []Conflict{},
	}
	digest, err := discardRequestDigest(req)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := encodeDiscardReceipt(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.registration.repo.WithImmediateWorkspace(context.Background(), req.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		return tx.InsertTransitionReceipt(context.Background(), localstore.WorkspaceTransitionReceiptInsert{
			RequestID: req.RequestID, Action: "discard", RequestDigest: digest,
			Actor: req.Actor, ResultJSON: encoded, Outcome: "clean",
		})
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(repository.root, repository.root+"-missing"); err != nil {
		t.Fatal(err)
	}
	commitErr := fmt.Errorf("%w: synthetic commit ambiguity", localstore.ErrCommitOutcomeUnknown)

	got, err := confirmDiscardCommit(context.Background(), service.registration.repo, req, digest, result, commitErr)
	if err != nil || !reflect.DeepEqual(got, result) || got.Conflicts == nil {
		t.Fatalf("exact confirmation=(%+v,%v), want %+v", got, err, result)
	}

	for _, test := range []struct {
		name     string
		request  ObserveGitBaseRequest
		expected ObserveGitBaseResult
	}{
		{name: "absent", request: editObserveRequest(req, func(value *ObserveGitBaseRequest) {
			value.RequestID = "10000000-0000-4000-8000-a0000000000a"
		}), expected: result},
		{name: "attempted result mismatch", request: req, expected: func() ObserveGitBaseResult {
			mismatched := result
			mismatched.ObservedRef = "refs/heads/other"
			return mismatched
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			requestDigest, digestErr := discardRequestDigest(test.request)
			if digestErr != nil {
				t.Fatal(digestErr)
			}
			got, confirmErr := confirmDiscardCommit(
				context.Background(), service.registration.repo, test.request, requestDigest, test.expected, commitErr,
			)
			if !errors.Is(confirmErr, commitErr) || !errors.Is(confirmErr, localstore.ErrCommitOutcomeUnknown) ||
				errors.Is(confirmErr, ErrIdempotencyConflict) || !reflect.DeepEqual(got, ObserveGitBaseResult{}) {
				t.Fatalf("failed confirmation=(%+v,%v), want original unknown without idempotency", got, confirmErr)
			}
		})
	}
}

func TestBranchSwitchDiscardUnknownCommitConfirmsExactReceiptWithoutGit(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	_, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	actor := prepareObserveGitBaseCandidate(t, service, registered.Binding, repository.root)
	runGit(t, repository.root, "switch", "-c", "next")
	req := ObserveGitBaseRequest{
		Scope: registered.Binding.Scope, ExpectedBinding: registered.Binding,
		Root: repository.root, ExpectedCommit: repository.commit,
		BranchAction: BranchSwitchDiscard, RequestID: "10000000-0000-4000-8000-a0000000000c", Actor: actor,
	}
	realTransition := service.gitBase.withImmediateWorkspaceTransition
	if realTransition == nil {
		t.Fatal("service has no real workspace-transition dependency")
	}
	service.gitBase.withImmediateWorkspaceTransition = func(
		ctx context.Context,
		scope types.WorkspaceScope,
		requestID string,
		callback func(*localstore.WorkspaceMutationTx, *localstore.WorkspaceTransitionReceiptRecord) error,
	) error {
		if err := realTransition(ctx, scope, requestID, callback); err != nil {
			return err
		}
		if err := os.Rename(repository.root, repository.root+"-missing"); err != nil {
			return err
		}
		return fmt.Errorf("%w: synthetic post-commit ambiguity", localstore.ErrCommitOutcomeUnknown)
	}

	got, err := service.ObserveGitBase(context.Background(), req)
	if err != nil || got.PreviousCommit != registered.Binding.AcceptedCommitSHA ||
		got.ObservedCommit != repository.commit || got.PreviousRef != registered.Binding.AcceptedRef ||
		got.ObservedRef != "refs/heads/next" || got.PreviousBaseDigest != state.Digest(registered.Binding.AcceptedTreeDigest) ||
		got.ObservedBaseDigest != state.Digest(registered.Binding.AcceptedTreeDigest) || got.CandidateAccepted ||
		got.AcceptedJournalID != nil || got.Rebased || got.Conflicts == nil || len(got.Conflicts) != 0 {
		t.Fatalf("unknown-commit confirmation=(%+v,%v)", got, err)
	}
	digest, err := discardRequestDigest(req)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := service.registration.repo.TransitionReceiptByKey(context.Background(), req.Scope, req.RequestID)
	if err != nil || receipt == nil {
		t.Fatalf("confirmed receipt=(%+v,%v)", receipt, err)
	}
	decoded, err := decodeDiscardReceipt(receipt, req, digest)
	if err != nil || !reflect.DeepEqual(decoded, got) {
		t.Fatalf("confirmed receipt result=(%+v,%v), want %+v", decoded, err, got)
	}
}

func TestBranchSwitchDiscardConcurrentReceiptInsideWriterBarrierWins(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	store, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	actor := prepareObserveGitBaseCandidate(t, service, registered.Binding, repository.root)
	runGit(t, repository.root, "switch", "-c", "next")
	req := ObserveGitBaseRequest{
		Scope: registered.Binding.Scope, ExpectedBinding: registered.Binding,
		Root: repository.root, ExpectedCommit: repository.commit,
		BranchAction: BranchSwitchDiscard, RequestID: "10000000-0000-4000-8000-a0000000000b", Actor: actor,
	}
	if receipt, err := service.registration.repo.TransitionReceiptByKey(context.Background(), req.Scope, req.RequestID); err != nil || receipt != nil {
		t.Fatalf("initial receipt=(%+v,%v), want absent", receipt, err)
	}
	competitor, err := NewService(localstore.NewWorkspaceRepo(store.DB()), ServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var want ObserveGitBaseResult
	service.gitBase.observeGitBase = func(ctx context.Context, observedRequest ObserveGitBaseRequest) (gitBaseObservation, error) {
		outside, observeErr := observeGitBaseOutside(ctx, observedRequest)
		if observeErr != nil {
			return gitBaseObservation{}, observeErr
		}
		want, observeErr = competitor.ObserveGitBase(ctx, observedRequest)
		if observeErr != nil {
			return gitBaseObservation{}, observeErr
		}
		if renameErr := os.Rename(repository.root, repository.root+"-missing"); renameErr != nil {
			return gitBaseObservation{}, renameErr
		}
		service.gitBase.now = nil
		return outside, nil
	}

	got, err := service.ObserveGitBase(context.Background(), req)
	if err != nil || !reflect.DeepEqual(got, want) || got.Conflicts == nil {
		t.Fatalf("concurrent receipt path=(%+v,%v), want %+v", got, err, want)
	}
}

func TestObserveGitBaseRequiresCompleteExpectedBindingCAS(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	_, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	mismatched := registered.Binding
	mismatched.AcceptedRef = "refs/heads/other"
	got, err := service.ObserveGitBase(context.Background(), ObserveGitBaseRequest{
		Scope: mismatched.Scope, ExpectedBinding: mismatched,
		Root: repository.root, ExpectedCommit: repository.commit,
	})
	if err == nil || !reflect.DeepEqual(got, ObserveGitBaseResult{}) {
		t.Fatalf("ObserveGitBase()=(%+v,%v), want complete-binding mismatch", got, err)
	}
	workspace, err := service.registration.repo.Workspace(context.Background(), registered.Binding.Scope)
	if err != nil || workspace.Binding != registered.Binding || workspace.State != "clean" {
		t.Fatalf("workspace after CAS mismatch=(%+v,%v)", workspace, err)
	}
}

func TestObserveGitBaseSameRefAcceptsExactMaterialization(t *testing.T) {
	for _, test := range []struct {
		name                 string
		materializationState string
		restart              bool
	}{
		{name: "published", materializationState: "published"},
		{name: "recovered_new after restart", materializationState: "recovered_new", restart: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := prepareObserveExactMaterialization(t, test.materializationState)
			if test.restart {
				if err := fixture.store.Close(); err != nil {
					t.Fatal(err)
				}
				fixture.store, fixture.service = openProjectStateServiceAt(t, fixture.databasePath)
			}
			beforeRevision := workspaceRevisionForProjectStateTest(t, fixture.service, fixture.registered.Binding.Scope)

			got, err := fixture.service.ObserveGitBase(context.Background(), ObserveGitBaseRequest{
				Scope: fixture.registered.Binding.Scope, ExpectedBinding: fixture.registered.Binding,
				Root: fixture.repository.root, ExpectedCommit: fixture.newCommit,
			})
			if err != nil || !got.CandidateAccepted || got.Rebased || got.AcceptedJournalID == nil ||
				*got.AcceptedJournalID != "observe-journal" || got.Conflicts == nil || len(got.Conflicts) != 0 {
				t.Fatalf("ObserveGitBase()=(%+v,%v)", got, err)
			}
			status := mustServiceStatus(t, fixture.service, fixture.registered.Binding.Scope)
			if status.Binding.AcceptedCommitSHA != fixture.newCommit || status.Binding.AcceptedTreeDigest != string(fixture.materialized.Digest) ||
				status.State != "clean" || status.CandidateDigest != fixture.materialized.Digest {
				t.Fatalf("accepted Status()=%+v", status)
			}
			var candidate *localstore.WorkspaceCandidateRecord
			var eligible *localstore.WorkspaceMaterializationRecord
			if err := fixture.service.registration.repo.WithImmediateWorkspace(context.Background(), fixture.registered.Binding.Scope, func(tx *localstore.WorkspaceMutationTx) error {
				var err error
				candidate, err = tx.Candidate(context.Background())
				if err != nil {
					return err
				}
				eligible, err = tx.CurrentMaterialization(context.Background())
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if candidate != nil || eligible != nil {
				t.Fatalf("candidate=%+v eligible=%+v, want consumed candidate and accepted journal", candidate, eligible)
			}
			if afterRevision := workspaceRevisionForProjectStateTest(t, fixture.service, fixture.registered.Binding.Scope); afterRevision != beforeRevision+1 {
				t.Fatalf("Git materialization acceptance workspace revision=%d, want %d", afterRevision, beforeRevision+1)
			}
		})
	}
}

func TestObserveGitBaseMaterializationCandidateMismatchIsPrecondition(t *testing.T) {
	fixture := prepareObserveExactMaterialization(t, "published")
	if _, err := fixture.store.DB().Exec(`
		UPDATE workspace_candidates SET rebased_through_generation=1
		WHERE project_id=? AND workspace_id=?
	`, fixture.registered.Binding.Scope.ProjectID, fixture.registered.Binding.Scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}

	got, err := fixture.service.ObserveGitBase(context.Background(), ObserveGitBaseRequest{
		Scope: fixture.registered.Binding.Scope, ExpectedBinding: fixture.registered.Binding,
		Root: fixture.repository.root, ExpectedCommit: fixture.newCommit,
	})
	if !errors.Is(err, ErrGitMaterializationPrecondition) || !reflect.DeepEqual(got, ObserveGitBaseResult{}) {
		t.Fatalf("ObserveGitBase()=(%+v,%v), want materialization precondition", got, err)
	}
	workspace, err := fixture.service.registration.repo.Workspace(context.Background(), fixture.registered.Binding.Scope)
	if err != nil || workspace.Binding != fixture.registered.Binding || workspace.State != "pending" {
		t.Fatalf("workspace after mismatch=(%+v,%v)", workspace, err)
	}
	if err := fixture.service.registration.repo.WithImmediateWorkspace(context.Background(), fixture.registered.Binding.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		candidate, err := tx.Candidate(context.Background())
		if err != nil {
			return err
		}
		eligible, err := tx.CurrentMaterialization(context.Background())
		if err != nil {
			return err
		}
		if candidate == nil || candidate.RebasedThroughGeneration != 1 || eligible == nil || eligible.State != "published" {
			return fmt.Errorf("candidate=%+v eligible=%+v", candidate, eligible)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestObserveGitBaseExactAcceptanceRejectsRebasedRowAboveBoundary(t *testing.T) {
	fixture := prepareObserveExactMaterialization(t, "published")
	seed, err := readObserveGitBaseAdjacentState(fixture.service.registration.repo, fixture.registered.Binding.Scope)
	if err != nil || seed.candidate == nil {
		t.Fatalf("candidate seed=(%+v,%v)", seed, err)
	}
	operation := servicePutTaskOperation(seed.candidate.DirectSnapshot,
		"99999999-9999-4999-8999-999999999994", "22222222-2222-4222-8222-222222222225", "stranded rebased task")
	if _, err := fixture.service.Apply(context.Background(), fixture.registered.Binding.Scope, operation); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.registration.repo.WithImmediateWorkspace(context.Background(), fixture.registered.Binding.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		audit, err := readAllOperationRows(context.Background(), tx)
		if err != nil {
			return err
		}
		if len(audit) != 1 || audit[0].State != "active" || audit[0].Generation != 1 {
			return fmt.Errorf("active audit fixture=%+v", audit)
		}
		return tx.TransitionOperations(context.Background(), []localstore.WorkspaceOperation{audit[0]}, "rebased", nil)
	}); err != nil {
		t.Fatal(err)
	}
	beforeWorkspace, err := fixture.service.registration.repo.Workspace(context.Background(), fixture.registered.Binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	before, err := readObserveGitBaseAdjacentState(fixture.service.registration.repo, fixture.registered.Binding.Scope)
	if err != nil || len(before.audit) != 1 || before.audit[0].State != "rebased" ||
		before.audit[0].Generation <= before.candidate.RebasedThroughGeneration {
		t.Fatalf("rebased-above-boundary fixture=(%+v,%v)", before, err)
	}

	got, err := fixture.service.ObserveGitBase(context.Background(), ObserveGitBaseRequest{
		Scope: fixture.registered.Binding.Scope, ExpectedBinding: fixture.registered.Binding,
		Root: fixture.repository.root, ExpectedCommit: fixture.newCommit,
	})
	if !errors.Is(err, ErrGitMaterializationPrecondition) || !reflect.DeepEqual(got, ObserveGitBaseResult{}) {
		t.Fatalf("ObserveGitBase()=(%+v,%v), want exact-classification precondition", got, err)
	}
	afterWorkspace, err := fixture.service.registration.repo.Workspace(context.Background(), fixture.registered.Binding.Scope)
	if err != nil || !reflect.DeepEqual(afterWorkspace, beforeWorkspace) {
		t.Fatalf("workspace changed after failed exact classification\nbefore=%+v\nafter=(%+v,%v)", beforeWorkspace, afterWorkspace, err)
	}
	after, err := readObserveGitBaseAdjacentState(fixture.service.registration.repo, fixture.registered.Binding.Scope)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("journal/candidate/operations/conflicts changed after failed exact classification\nbefore=%+v\nafter=(%+v,%v)", before, after, err)
	}
}

func TestBranchSwitchDiscardNonmatchingEligibleIsPreconditionAndRetained(t *testing.T) {
	fixture := prepareObserveExactMaterialization(t, "published")
	workspace, err := fixture.service.registration.repo.Workspace(context.Background(), fixture.registered.Binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	writeImportSnapshot(t, fixture.repository.root, workspace.Snapshot)
	runGit(t, fixture.repository.root, "add", ".wormhole")
	runGit(t, fixture.repository.root, "commit", "-m", "different observed candidate")
	newCommit := strings.TrimSpace(runGit(t, fixture.repository.root, "rev-parse", "HEAD"))
	runGit(t, fixture.repository.root, "switch", "-c", "next")
	req := ObserveGitBaseRequest{
		Scope: fixture.registered.Binding.Scope, ExpectedBinding: fixture.registered.Binding,
		Root: fixture.repository.root, ExpectedCommit: newCommit,
		BranchAction: BranchSwitchDiscard, RequestID: "10000000-0000-4000-8000-a00000000005", Actor: diffActorEnvelope(),
	}
	got, err := fixture.service.ObserveGitBase(context.Background(), req)
	if !errors.Is(err, ErrGitMaterializationPrecondition) || errors.Is(err, ErrIdempotencyConflict) ||
		!reflect.DeepEqual(got, ObserveGitBaseResult{}) {
		t.Fatalf("ObserveGitBase()=(%+v,%v), want materialization precondition", got, err)
	}
	after, err := fixture.service.registration.repo.Workspace(context.Background(), fixture.registered.Binding.Scope)
	if err != nil || after.Binding != fixture.registered.Binding || after.State != "pending" {
		t.Fatalf("workspace after mismatch=(%+v,%v)", after, err)
	}
	if err := fixture.service.registration.repo.WithImmediateWorkspace(context.Background(), fixture.registered.Binding.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		candidate, err := tx.Candidate(context.Background())
		if err != nil {
			return err
		}
		eligible, err := tx.CurrentMaterialization(context.Background())
		if err != nil {
			return err
		}
		if candidate == nil || eligible == nil || eligible.State != "published" {
			return fmt.Errorf("candidate=%+v eligible=%+v", candidate, eligible)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if receipt, err := fixture.service.registration.repo.TransitionReceiptByKey(context.Background(), req.Scope, req.RequestID); err != nil || receipt != nil {
		t.Fatalf("receipt after mismatch=(%+v,%v)", receipt, err)
	}
}

func TestBranchSwitchDiscardExactEligibleIsNotApplicableAndRetained(t *testing.T) {
	fixture := prepareObserveExactMaterialization(t, "published")
	runGit(t, fixture.repository.root, "switch", "-c", "next")
	req := ObserveGitBaseRequest{
		Scope: fixture.registered.Binding.Scope, ExpectedBinding: fixture.registered.Binding,
		Root: fixture.repository.root, ExpectedCommit: fixture.newCommit,
		BranchAction: BranchSwitchDiscard, RequestID: "10000000-0000-4000-8000-a00000000007", Actor: diffActorEnvelope(),
	}
	got, err := fixture.service.ObserveGitBase(context.Background(), req)
	if !errors.Is(err, ErrBranchSwitchDiscardNotApplicable) || errors.Is(err, ErrGitMaterializationPrecondition) ||
		!reflect.DeepEqual(got, ObserveGitBaseResult{}) {
		t.Fatalf("ObserveGitBase()=(%+v,%v), want exact eligible not-applicable", got, err)
	}
	workspace, err := fixture.service.registration.repo.Workspace(context.Background(), fixture.registered.Binding.Scope)
	if err != nil || workspace.Binding != fixture.registered.Binding || workspace.State != "pending" {
		t.Fatalf("workspace after exact not-applicable=(%+v,%v)", workspace, err)
	}
	if err := fixture.service.registration.repo.WithImmediateWorkspace(context.Background(), fixture.registered.Binding.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		candidate, err := tx.Candidate(context.Background())
		if err != nil {
			return err
		}
		eligible, err := tx.CurrentMaterialization(context.Background())
		if err != nil {
			return err
		}
		audit, err := readAllOperationRows(context.Background(), tx)
		if err != nil {
			return err
		}
		if candidate == nil || eligible == nil || eligible.State != "published" || len(audit) != 0 {
			return fmt.Errorf("candidate=%+v eligible=%+v audit=%+v", candidate, eligible, audit)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if receipt, err := fixture.service.registration.repo.TransitionReceiptByKey(context.Background(), req.Scope, req.RequestID); err != nil || receipt != nil {
		t.Fatalf("receipt after exact not-applicable=(%+v,%v)", receipt, err)
	}
}

func TestObserveGitBaseExactAcceptancePreservesLaterActiveRows(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	store, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	prepareObserveGitBaseCandidate(t, service, registered.Binding, repository.root)
	var candidate *localstore.WorkspaceCandidateRecord
	if err := service.registration.repo.WithImmediateWorkspace(context.Background(), registered.Binding.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		var err error
		candidate, err = tx.Candidate(context.Background())
		return err
	}); err != nil || candidate == nil {
		t.Fatalf("candidate=(%+v,%v)", candidate, err)
	}
	first := servicePutTaskOperation(candidate.DirectSnapshot,
		"99999999-9999-4999-8999-999999999992", "22222222-2222-4222-8222-222222222223", "materialized task")
	materialized, err := state.ApplyOperation(candidate.DirectSnapshot, first)
	if err != nil {
		t.Fatal(err)
	}
	materialized = diffCanonicalSnapshot(t, materialized)
	second := servicePutTaskOperation(materialized,
		"99999999-9999-4999-8999-999999999993", "22222222-2222-4222-8222-222222222224", "later task")
	composed, err := state.ApplyOperation(materialized, second)
	if err != nil {
		t.Fatal(err)
	}
	composed = diffCanonicalSnapshot(t, composed)
	if _, err := service.ApplyBatch(context.Background(), registered.Binding.Scope, []state.OperationV1{first, second}); err != nil {
		t.Fatal(err)
	}
	var firstRow, secondRow localstore.WorkspaceOperation
	if err := service.registration.repo.WithImmediateWorkspace(context.Background(), registered.Binding.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		audit, err := readAllOperationRows(context.Background(), tx)
		if err != nil {
			return err
		}
		if len(audit) != 2 {
			return fmt.Errorf("operation audit has %d rows", len(audit))
		}
		firstRow, secondRow = audit[0], audit[1]
		rebased := materialized
		if err := tx.UpsertCandidate(context.Background(), localstore.WorkspaceCandidateRecord{
			AcceptedBaseDigest: state.Digest(registered.Binding.AcceptedTreeDigest),
			WorkingTreeDigest:  candidate.WorkingTreeDigest, DirectSnapshot: candidate.DirectSnapshot,
			RebasedSnapshot: &rebased, RebasedThroughGeneration: 1,
			ImportedBy: candidate.ImportedBy, ImportedAt: candidate.ImportedAt,
		}); err != nil {
			return err
		}
		return tx.TransitionOperations(context.Background(), []localstore.WorkspaceOperation{firstRow}, "materialized", nil)
	}); err != nil {
		t.Fatal(err)
	}
	firstJSON, err := state.CanonicalOperation(first)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := encodeCheckpointOperations(CheckpointOperationsV1{
		SchemaVersion: 1, InitialThroughGeneration: 0,
		Operations: []CheckpointOperationV1{{
			Generation: 1, OperationID: first.ID, OperationJSON: string(firstJSON), PrepublicationState: "active",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
		INSERT INTO workspace_materializations
		(project_id,workspace_id,journal_id,expected_live_digest,accepted_base_digest,
		 checkout_path,checkout_device,checkout_inode,prior_tree_digest,candidate_digest,
		 through_generation,prior_tree,candidate_tree,stage_path,backup_path,state,included_operations_json,publication_review_json,prior_candidate_json,publication_review_proof_version)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	`, registered.Binding.Scope.ProjectID, registered.Binding.Scope.WorkspaceID, "observe-with-later",
		candidate.DirectSnapshot.Digest, state.Digest(registered.Binding.AcceptedTreeDigest), registered.Binding.Checkout.CanonicalPath,
		registered.Binding.Checkout.Device, registered.Binding.Checkout.Inode, candidate.DirectSnapshot.Digest, materialized.Digest,
		int64(1), encodeServiceSnapshot(t, candidate.DirectSnapshot), encodeServiceSnapshot(t, materialized),
		filepath.Join(repository.root, ".wormhole-stage"), filepath.Join(repository.root, ".wormhole-backup"), "published", envelope, " review\n", " prior-candidate\n", 1); err != nil {
		t.Fatal(err)
	}
	writeImportSnapshot(t, repository.root, materialized)
	runGit(t, repository.root, "add", ".wormhole")
	runGit(t, repository.root, "commit", "-m", "materialized candidate with later overlay")
	newCommit := strings.TrimSpace(runGit(t, repository.root, "rev-parse", "HEAD"))

	got, err := service.ObserveGitBase(context.Background(), ObserveGitBaseRequest{
		Scope: registered.Binding.Scope, ExpectedBinding: registered.Binding,
		Root: repository.root, ExpectedCommit: newCommit,
	})
	if err != nil || !got.CandidateAccepted || got.AcceptedJournalID == nil || *got.AcceptedJournalID != "observe-with-later" {
		t.Fatalf("ObserveGitBase()=(%+v,%v)", got, err)
	}
	status := mustServiceStatus(t, service, registered.Binding.Scope)
	if status.State != "pending" || status.AcceptedSnapshot.Digest != materialized.Digest ||
		status.CandidateDigest != composed.Digest || status.OverlayGeneration != 2 {
		t.Fatalf("Status()=%+v, want accepted=%s composed=%s", status, materialized.Digest, composed.Digest)
	}
	if err := service.registration.repo.WithImmediateWorkspace(context.Background(), registered.Binding.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		audit, err := readAllOperationRows(context.Background(), tx)
		if err != nil {
			return err
		}
		if len(audit) != 2 || audit[0].State != "materialized" ||
			audit[1].State != "active" || !reflect.DeepEqual(audit[1], secondRow) {
			return fmt.Errorf("post-acceptance audit=%+v", audit)
		}
		candidate, err := tx.Candidate(context.Background())
		if err != nil {
			return err
		}
		if candidate != nil {
			return fmt.Errorf("candidate remains after acceptance")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestObserveGitBaseCommitOnlyChangeRebasesProposal(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	_, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	prepareObserveGitBaseCandidate(t, service, registered.Binding, repository.root)
	var before *localstore.WorkspaceCandidateRecord
	if err := service.registration.repo.WithImmediateWorkspace(context.Background(), registered.Binding.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		var err error
		before, err = tx.Candidate(context.Background())
		return err
	}); err != nil || before == nil {
		t.Fatalf("candidate before rebase=(%+v,%v)", before, err)
	}
	runGit(t, repository.root, "commit", "--allow-empty", "-m", "metadata-only base change")
	newCommit := strings.TrimSpace(runGit(t, repository.root, "rev-parse", "HEAD"))

	got, err := service.ObserveGitBase(context.Background(), ObserveGitBaseRequest{
		Scope: registered.Binding.Scope, ExpectedBinding: registered.Binding,
		Root: repository.root, ExpectedCommit: newCommit,
	})
	if err != nil || !got.Rebased || got.CandidateAccepted || got.Conflicts == nil || len(got.Conflicts) != 0 ||
		got.ObservedCommit != newCommit || got.ObservedBaseDigest != got.PreviousBaseDigest {
		t.Fatalf("ObserveGitBase()=(%+v,%v)", got, err)
	}
	status := mustServiceStatus(t, service, registered.Binding.Scope)
	if status.Binding.AcceptedCommitSHA != newCommit || status.State != "pending" {
		t.Fatalf("Status()=%+v", status)
	}
	var after *localstore.WorkspaceCandidateRecord
	if err := service.registration.repo.WithImmediateWorkspace(context.Background(), registered.Binding.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		var err error
		after, err = tx.Candidate(context.Background())
		return err
	}); err != nil || after == nil {
		t.Fatalf("candidate after rebase=(%+v,%v)", after, err)
	}
	if after.ImportedBy != before.ImportedBy || !after.ImportedAt.Equal(before.ImportedAt) ||
		after.WorkingTreeDigest != before.WorkingTreeDigest || !reflect.DeepEqual(after.DirectSnapshot, before.DirectSnapshot) ||
		after.AcceptedBaseDigest != got.ObservedBaseDigest {
		t.Fatalf("candidate provenance/direct representation changed\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestObserveGitBaseRebaseWithoutCandidateUsesSystemProvenance(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	_, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	accepted := mustServiceStatus(t, service, registered.Binding.Scope).AcceptedSnapshot
	operation := servicePutTaskOperation(accepted,
		"99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "overlay only")
	if _, err := service.Apply(context.Background(), registered.Binding.Scope, operation); err != nil {
		t.Fatal(err)
	}
	fixedNow := time.Date(2026, 8, 1, 17, 30, 0, 123, time.FixedZone("fixture", -4*60*60))
	service.gitBase.now = func() time.Time { return fixedNow }
	runGit(t, repository.root, "commit", "--allow-empty", "-m", "metadata-only base change")
	newCommit := strings.TrimSpace(runGit(t, repository.root, "rev-parse", "HEAD"))

	got, err := service.ObserveGitBase(context.Background(), ObserveGitBaseRequest{
		Scope: registered.Binding.Scope, ExpectedBinding: registered.Binding,
		Root: repository.root, ExpectedCommit: newCommit,
	})
	if err != nil || !got.Rebased || got.Conflicts == nil || len(got.Conflicts) != 0 {
		t.Fatalf("ObserveGitBase()=(%+v,%v)", got, err)
	}
	if err := service.registration.repo.WithImmediateWorkspace(context.Background(), registered.Binding.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		candidate, err := tx.Candidate(context.Background())
		if err != nil {
			return err
		}
		audit, err := readAllOperationRows(context.Background(), tx)
		if err != nil {
			return err
		}
		if candidate == nil || candidate.ImportedBy != types.CandidateImportOriginGitObservationRebaseV1 ||
			!candidate.ImportedAt.Equal(fixedNow.UTC()) || candidate.WorkingTreeDigest != accepted.Digest ||
			!reflect.DeepEqual(candidate.DirectSnapshot, accepted) || candidate.RebasedThroughGeneration != 1 ||
			len(audit) != 1 || audit[0].State != "rebased" {
			return fmt.Errorf("candidate=%+v audit=%+v", candidate, audit)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestObserveGitBaseRebaseRepresentativeWriteFailuresRollback(t *testing.T) {
	for _, test := range []struct {
		name    string
		trigger string
	}{
		{
			name: "accepted base advance first write",
			trigger: `CREATE TRIGGER observe_rebase_fail_base BEFORE UPDATE ON workspace_bindings
				BEGIN SELECT RAISE(ABORT,'reject accepted base advance'); END`,
		},
		{
			name: "candidate upsert after accepted base advance",
			trigger: `CREATE TRIGGER observe_rebase_fail_candidate BEFORE UPDATE ON workspace_candidates
				BEGIN SELECT RAISE(ABORT,'reject candidate update'); END`,
		},
		{
			name: "operation transition after candidate upsert",
			trigger: `CREATE TRIGGER observe_rebase_fail_operation BEFORE UPDATE OF state ON workspace_overlay_operations
				BEGIN SELECT RAISE(ABORT,'reject operation transition'); END`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
			store, service := openProjectStateService(t, "")
			registered := registerGitRepository(t, service, repository)
			prepareObserveGitBaseCandidate(t, service, registered.Binding, repository.root)
			seed, seedErr := readObserveGitBaseAdjacentState(service.registration.repo, registered.Binding.Scope)
			if seedErr != nil || seed.candidate == nil {
				t.Fatalf("candidate seed=(%+v,%v)", seed, seedErr)
			}
			operation := servicePutTaskOperation(seed.candidate.DirectSnapshot,
				"99999999-9999-4999-8999-999999999992", "22222222-2222-4222-8222-222222222223", "later active task")
			if _, err := service.Apply(context.Background(), registered.Binding.Scope, operation); err != nil {
				t.Fatal(err)
			}
			beforeWorkspace, err := service.registration.repo.Workspace(context.Background(), registered.Binding.Scope)
			if err != nil || beforeWorkspace.State != "pending" {
				t.Fatalf("workspace before failure=(%+v,%v)", beforeWorkspace, err)
			}
			before, err := readObserveGitBaseAdjacentState(service.registration.repo, registered.Binding.Scope)
			if err != nil || before.candidate == nil || len(before.audit) == 0 {
				t.Fatalf("adjacent state before failure=(%+v,%v)", before, err)
			}
			if _, err := store.DB().Exec(test.trigger); err != nil {
				t.Fatal(err)
			}
			runGit(t, repository.root, "commit", "--allow-empty", "-m", "metadata-only base change")
			newCommit := strings.TrimSpace(runGit(t, repository.root, "rev-parse", "HEAD"))
			got, err := service.ObserveGitBase(context.Background(), ObserveGitBaseRequest{
				Scope: registered.Binding.Scope, ExpectedBinding: registered.Binding,
				Root: repository.root, ExpectedCommit: newCommit,
			})
			if err == nil || errors.Is(err, localstore.ErrCommitOutcomeUnknown) || !reflect.DeepEqual(got, ObserveGitBaseResult{}) {
				t.Fatalf("ObserveGitBase()=(%+v,%v), want deterministic rollback", got, err)
			}
			afterWorkspace, err := service.registration.repo.Workspace(context.Background(), registered.Binding.Scope)
			if err != nil || !reflect.DeepEqual(afterWorkspace, beforeWorkspace) {
				t.Fatalf("workspace changed across rollback\nbefore=%+v\nafter=(%+v,%v)", beforeWorkspace, afterWorkspace, err)
			}
			after, err := readObserveGitBaseAdjacentState(service.registration.repo, registered.Binding.Scope)
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("adjacent state changed across rollback\nbefore=%+v\nafter=(%+v,%v)", before, after, err)
			}
		})
	}
}

func TestObserveGitBaseRejectAndRefreshUnknownCommitReturnZero(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		name := "observe"
		if refresh {
			name = "refresh"
		}
		t.Run(name, func(t *testing.T) {
			repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
			store, service := openProjectStateService(t, "")
			registered := registerGitRepository(t, service, repository)
			runGit(t, repository.root, "switch", "-c", "next")
			if _, err := store.DB().Exec(`
				CREATE TABLE observe_reject_deferred_failure(
				 project_id TEXT NOT NULL, workspace_id TEXT NOT NULL,
				 FOREIGN KEY(project_id,workspace_id) REFERENCES workspace_bindings(project_id,workspace_id)
				 DEFERRABLE INITIALLY DEFERRED);
				CREATE TRIGGER observe_reject_fail_commit AFTER UPDATE OF accepted_ref ON workspace_bindings BEGIN
				 INSERT INTO observe_reject_deferred_failure(project_id,workspace_id)
				 VALUES ('00000000-0000-4000-8000-000000000099','00000000-0000-4000-8000-000000000098');
				END;
			`); err != nil {
				t.Fatal(err)
			}
			if refresh {
				got, err := service.RefreshWorkspace(context.Background(), registered.Binding)
				if !errors.Is(err, localstore.ErrCommitOutcomeUnknown) || !reflect.DeepEqual(got, types.WorkspaceBinding{}) {
					t.Fatalf("RefreshWorkspace()=(%+v,%v)", got, err)
				}
			} else {
				got, err := service.ObserveGitBase(context.Background(), ObserveGitBaseRequest{
					Scope: registered.Binding.Scope, ExpectedBinding: registered.Binding,
					Root: repository.root, ExpectedCommit: repository.commit,
				})
				if !errors.Is(err, localstore.ErrCommitOutcomeUnknown) || !reflect.DeepEqual(got, ObserveGitBaseResult{}) {
					t.Fatalf("ObserveGitBase()=(%+v,%v)", got, err)
				}
			}
			workspace, err := service.registration.repo.Workspace(context.Background(), registered.Binding.Scope)
			if err != nil || workspace.Binding != registered.Binding || workspace.State != "clean" {
				t.Fatalf("workspace after unknown commit=(%+v,%v)", workspace, err)
			}
		})
	}
}

func TestBranchSwitchDiscardUnknownCommitReturnsZeroAndRollsBack(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	store, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	actor := prepareObserveGitBaseCandidate(t, service, registered.Binding, repository.root)
	runGit(t, repository.root, "switch", "-c", "next")
	if _, err := store.DB().Exec(`
		CREATE TABLE observe_discard_deferred_failure(
		 project_id TEXT NOT NULL, workspace_id TEXT NOT NULL,
		 FOREIGN KEY(project_id,workspace_id) REFERENCES workspace_bindings(project_id,workspace_id)
		 DEFERRABLE INITIALLY DEFERRED);
		CREATE TRIGGER observe_discard_fail_commit AFTER INSERT ON workspace_transition_receipts
		WHEN NEW.action='discard' BEGIN
		 INSERT INTO observe_discard_deferred_failure(project_id,workspace_id)
		 VALUES ('00000000-0000-4000-8000-000000000099','00000000-0000-4000-8000-000000000098');
		END;
	`); err != nil {
		t.Fatal(err)
	}
	req := ObserveGitBaseRequest{
		Scope: registered.Binding.Scope, ExpectedBinding: registered.Binding,
		Root: repository.root, ExpectedCommit: repository.commit,
		BranchAction: BranchSwitchDiscard, RequestID: "10000000-0000-4000-8000-a00000000006", Actor: actor,
	}
	got, err := service.ObserveGitBase(context.Background(), req)
	if !errors.Is(err, localstore.ErrCommitOutcomeUnknown) || errors.Is(err, ErrIdempotencyConflict) ||
		!reflect.DeepEqual(got, ObserveGitBaseResult{}) {
		t.Fatalf("ObserveGitBase()=(%+v,%v), want unknown commit", got, err)
	}
	workspace, err := service.registration.repo.Workspace(context.Background(), registered.Binding.Scope)
	if err != nil || workspace.Binding != registered.Binding || workspace.State != "pending" {
		t.Fatalf("workspace after unknown discard=(%+v,%v)", workspace, err)
	}
	if receipt, err := service.registration.repo.TransitionReceiptByKey(context.Background(), req.Scope, req.RequestID); err != nil || receipt != nil {
		t.Fatalf("receipt after unknown discard=(%+v,%v)", receipt, err)
	}
	if err := service.registration.repo.WithImmediateWorkspace(context.Background(), registered.Binding.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		candidate, err := tx.Candidate(context.Background())
		if err != nil {
			return err
		}
		if candidate == nil {
			return fmt.Errorf("discard rollback lost candidate")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBranchSwitchDiscardRepresentativeWriteFailuresRollback(t *testing.T) {
	for _, test := range []struct {
		name    string
		trigger string
	}{
		{
			name: "operation transition first write",
			trigger: `CREATE TRIGGER observe_discard_fail_operation BEFORE UPDATE OF state ON workspace_overlay_operations
				BEGIN SELECT RAISE(ABORT,'reject operation transition'); END`,
		},
		{
			name: "candidate delete after operation transition",
			trigger: `CREATE TRIGGER observe_discard_fail_candidate BEFORE DELETE ON workspace_candidates
				BEGIN SELECT RAISE(ABORT,'reject candidate delete'); END`,
		},
		{
			name: "receipt insert after conflict resolution",
			trigger: `CREATE TRIGGER observe_discard_fail_receipt BEFORE INSERT ON workspace_transition_receipts
				WHEN NEW.action='discard' BEGIN SELECT RAISE(ABORT,'reject receipt insert'); END`,
		},
		{
			name: "accepted base advance after receipt insert",
			trigger: `CREATE TRIGGER observe_discard_fail_base BEFORE UPDATE ON workspace_bindings
				BEGIN SELECT RAISE(ABORT,'reject final accepted base'); END`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := prepareConflictingImportFixture(t)
			if _, err := fixture.service.Import(context.Background(), fixture.request); err != nil {
				t.Fatal(err)
			}
			workspace, err := fixture.service.registration.repo.Workspace(context.Background(), fixture.request.Scope)
			if err != nil || workspace.State != "conflicted" {
				t.Fatalf("conflicted workspace fixture=(%+v,%v)", workspace, err)
			}
			before, err := readObserveGitBaseAdjacentState(fixture.service.registration.repo, fixture.request.Scope)
			if err != nil || before.candidate == nil || len(before.audit) == 0 || len(before.conflicts) == 0 {
				t.Fatalf("adjacent fixture=(%+v,%v)", before, err)
			}
			runGit(t, fixture.request.Root, "switch", "-c", "next")
			if _, err := fixture.store.DB().Exec(test.trigger); err != nil {
				t.Fatal(err)
			}
			req := ObserveGitBaseRequest{
				Scope: fixture.request.Scope, ExpectedBinding: workspace.Binding,
				Root: fixture.request.Root, ExpectedCommit: workspace.Binding.AcceptedCommitSHA,
				BranchAction: BranchSwitchDiscard, RequestID: "10000000-0000-4000-8000-a00000000008", Actor: fixture.request.Actor,
			}
			got, err := fixture.service.ObserveGitBase(context.Background(), req)
			if err == nil || errors.Is(err, localstore.ErrCommitOutcomeUnknown) || !reflect.DeepEqual(got, ObserveGitBaseResult{}) {
				t.Fatalf("ObserveGitBase()=(%+v,%v), want deterministic write failure", got, err)
			}
			afterWorkspace, err := fixture.service.registration.repo.Workspace(context.Background(), fixture.request.Scope)
			if err != nil || !reflect.DeepEqual(afterWorkspace, workspace) {
				t.Fatalf("workspace changed across rollback\nbefore=%+v\nafter=(%+v,%v)", workspace, afterWorkspace, err)
			}
			after, err := readObserveGitBaseAdjacentState(fixture.service.registration.repo, fixture.request.Scope)
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("adjacent state changed across rollback\nbefore=%+v\nafter=(%+v,%v)", before, after, err)
			}
			if receipt, err := fixture.service.registration.repo.TransitionReceiptByKey(context.Background(), req.Scope, req.RequestID); err != nil || receipt != nil {
				t.Fatalf("receipt survived rollback=(%+v,%v)", receipt, err)
			}
		})
	}
}

func TestRefreshWorkspaceReadsHEADAndReturnsUpdatedBinding(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	_, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	runGit(t, repository.root, "switch", "-c", "next")

	got, err := service.RefreshWorkspace(context.Background(), registered.Binding)
	if err != nil {
		t.Fatal(err)
	}
	if got.Scope != registered.Binding.Scope || got.Checkout != registered.Binding.Checkout ||
		got.Repository != registered.Binding.Repository || got.AcceptedRef != "refs/heads/next" ||
		got.AcceptedCommitSHA != repository.commit || got.AcceptedTreeDigest != registered.Binding.AcceptedTreeDigest {
		t.Fatalf("RefreshWorkspace()=%+v", got)
	}
}

type observeGitBaseAdjacentState struct {
	candidate   *localstore.WorkspaceCandidateRecord
	audit       []localstore.WorkspaceOperation
	conflicts   []localstore.WorkspaceConflictOccurrence
	disposition localstore.WorkspaceCurrentMaterialization
}

func readAllOperationRows(ctx context.Context, tx *localstore.WorkspaceMutationTx) ([]localstore.WorkspaceOperation, error) {
	next, err := tx.NextGeneration(ctx)
	if err != nil {
		return nil, err
	}
	if next == 1 {
		return []localstore.WorkspaceOperation{}, nil
	}
	generations := make([]int64, next-1)
	for index := range generations {
		generations[index] = int64(index + 1)
	}
	return tx.OperationsByGenerations(ctx, generations)
}

func readObserveGitBaseAdjacentState(
	repo *localstore.WorkspaceRepo,
	scope types.WorkspaceScope,
) (observeGitBaseAdjacentState, error) {
	var got observeGitBaseAdjacentState
	err := repo.WithImmediateWorkspace(context.Background(), scope, func(tx *localstore.WorkspaceMutationTx) error {
		var err error
		got.candidate, err = tx.Candidate(context.Background())
		if err != nil {
			return err
		}
		got.audit, err = readAllOperationRows(context.Background(), tx)
		if err != nil {
			return err
		}
		got.conflicts, err = tx.OpenConflictOccurrences(context.Background())
		if err != nil {
			return err
		}
		got.disposition, err = readCurrentMaterializationWorkset(context.Background(), tx)
		return err
	})
	return got, err
}

type observeExactMaterializationFixture struct {
	repository   gitRepository
	store        *localstore.Store
	service      *Service
	registered   RegisterWorkspaceResult
	materialized state.Snapshot
	newCommit    string
	databasePath string
}

func prepareObserveExactMaterialization(t *testing.T, materializationState string) observeExactMaterializationFixture {
	t.Helper()
	if materializationState != "published" && materializationState != "recovered_new" {
		t.Fatalf("unsupported exact materialization state %q", materializationState)
	}
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, service := openProjectStateServiceAt(t, databasePath)
	registered := registerGitRepository(t, service, repository)
	prepareObserveGitBaseCandidate(t, service, registered.Binding, repository.root)
	var candidate *localstore.WorkspaceCandidateRecord
	if err := service.registration.repo.WithImmediateWorkspace(context.Background(), registered.Binding.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		var err error
		candidate, err = tx.Candidate(context.Background())
		return err
	}); err != nil || candidate == nil || candidate.RebasedSnapshot == nil {
		t.Fatalf("candidate fixture=(%+v,%v)", candidate, err)
	}
	materialized := *candidate.RebasedSnapshot
	envelope, err := encodeCheckpointOperations(CheckpointOperationsV1{
		SchemaVersion: 1, InitialThroughGeneration: 0, Operations: []CheckpointOperationV1{},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded := encodeServiceSnapshot(t, materialized)
	if _, err := store.DB().Exec(`
		INSERT INTO workspace_materializations
		(project_id,workspace_id,journal_id,expected_live_digest,accepted_base_digest,
		 checkout_path,checkout_device,checkout_inode,prior_tree_digest,candidate_digest,
		 through_generation,prior_tree,candidate_tree,stage_path,backup_path,state,included_operations_json,publication_review_json,prior_candidate_json,publication_review_proof_version)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	`, registered.Binding.Scope.ProjectID, registered.Binding.Scope.WorkspaceID, "observe-journal",
		candidate.DirectSnapshot.Digest, state.Digest(registered.Binding.AcceptedTreeDigest), registered.Binding.Checkout.CanonicalPath,
		registered.Binding.Checkout.Device, registered.Binding.Checkout.Inode, candidate.DirectSnapshot.Digest, materialized.Digest,
		int64(0), encodeServiceSnapshot(t, candidate.DirectSnapshot), encoded,
		filepath.Join(repository.root, ".wormhole-stage"), filepath.Join(repository.root, ".wormhole-backup"),
		materializationState, envelope, " review\n", " prior-candidate\n", 1); err != nil {
		t.Fatal(err)
	}
	writeImportSnapshot(t, repository.root, materialized)
	runGit(t, repository.root, "add", ".wormhole")
	runGit(t, repository.root, "commit", "-m", "materialized candidate")
	return observeExactMaterializationFixture{
		repository: repository, store: store, service: service, registered: registered,
		materialized: materialized,
		newCommit:    strings.TrimSpace(runGit(t, repository.root, "rev-parse", "HEAD")),
		databasePath: databasePath,
	}
}

func prepareObserveGitBaseCandidate(t *testing.T, service *Service, binding types.WorkspaceBinding, root string) types.ActorEnvelope {
	t.Helper()
	accepted := mustServiceStatus(t, service, binding.Scope).AcceptedSnapshot
	operation := servicePutTaskOperation(
		accepted,
		"99999999-9999-4999-8999-999999999991",
		"22222222-2222-4222-8222-222222222222",
		"candidate task",
	)
	direct, err := state.ApplyOperation(accepted, operation)
	if err != nil {
		t.Fatal(err)
	}
	direct = diffCanonicalSnapshot(t, direct)
	writeImportSnapshot(t, root, direct)
	if _, err := service.Import(context.Background(), ImportRequest{Scope: binding.Scope, Root: root, Actor: operation.Actor}); err != nil {
		t.Fatal(err)
	}
	return operation.Actor
}

func TestValidateObserveGitBaseRequestMatrixWithoutIO(t *testing.T) {
	validReject := observeRequestFixture(t, filepath.Join(t.TempDir(), "does-not-exist"))
	validDiscard := validReject
	validDiscard.BranchAction = BranchSwitchDiscard
	validDiscard.RequestID = "10000000-0000-4000-8000-a00000000001"
	validDiscard.Actor = diffActorEnvelope()

	zeroInNamedLocation := time.Time{}.In(time.FixedZone("UTC-like", 0))
	if !zeroInNamedLocation.IsZero() || zeroInNamedLocation == (time.Time{}) {
		t.Fatal("zero-time location fixture does not distinguish exact struct zero")
	}

	tests := []struct {
		name    string
		request ObserveGitBaseRequest
		wantErr error
		valid   bool
	}{
		{name: "reject SHA-256", request: validReject, valid: true},
		{name: "reject SHA-1", request: editObserveRequest(validReject, func(req *ObserveGitBaseRequest) { req.ExpectedCommit = strings.Repeat("b", 40) }), valid: true},
		{name: "discard", request: validDiscard, valid: true},
		{name: "reject request ID", request: editObserveRequest(validReject, func(req *ObserveGitBaseRequest) { req.RequestID = validDiscard.RequestID })},
		{name: "reject actor", request: editObserveRequest(validReject, func(req *ObserveGitBaseRequest) { req.Actor.ActorKind = types.ActorHuman })},
		{name: "reject zero instant with location", request: editObserveRequest(validReject, func(req *ObserveGitBaseRequest) { req.Actor.OccurredAt = zeroInNamedLocation })},
		{name: "discard empty request ID", request: editObserveRequest(validDiscard, func(req *ObserveGitBaseRequest) { req.RequestID = "" })},
		{name: "discard noncanonical request ID", request: editObserveRequest(validDiscard, func(req *ObserveGitBaseRequest) { req.RequestID = strings.ToUpper(req.RequestID) })},
		{name: "discard invalid actor", request: editObserveRequest(validDiscard, func(req *ObserveGitBaseRequest) { req.Actor.Assurance = types.AssuranceLegacy }), wantErr: types.ErrInvalidActorEnvelope},
		{name: "other action", request: editObserveRequest(validReject, func(req *ObserveGitBaseRequest) { req.BranchAction = "stash" })},
		{name: "invalid binding", request: editObserveRequest(validReject, func(req *ObserveGitBaseRequest) { req.ExpectedBinding.Checkout.Device = 0 }), wantErr: types.ErrInvalidWorkspaceBinding},
		{name: "scope mismatch", request: editObserveRequest(validReject, func(req *ObserveGitBaseRequest) { req.Scope.WorkspaceID = "20000000-0000-4000-8000-000000000002" })},
		{name: "relative root", request: editObserveRequest(validReject, func(req *ObserveGitBaseRequest) { req.Root = "checkout" })},
		{name: "unclean root", request: editObserveRequest(validReject, func(req *ObserveGitBaseRequest) { req.Root += "/.." })},
		{name: "root mismatch", request: editObserveRequest(validReject, func(req *ObserveGitBaseRequest) { req.Root = filepath.Join(filepath.Dir(req.Root), "other") })},
		{name: "short commit", request: editObserveRequest(validReject, func(req *ObserveGitBaseRequest) { req.ExpectedCommit = strings.Repeat("a", 39) })},
		{name: "uppercase commit", request: editObserveRequest(validReject, func(req *ObserveGitBaseRequest) { req.ExpectedCommit = strings.Repeat("A", 40) })},
		{name: "nonhex commit", request: editObserveRequest(validReject, func(req *ObserveGitBaseRequest) { req.ExpectedCommit = strings.Repeat("g", 40) })},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateObserveGitBaseRequest(test.request)
			if (err == nil) != test.valid || test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("validateObserveGitBaseRequest()=%v, want valid=%v sentinel=%v", err, test.valid, test.wantErr)
			}
		})
	}
}

func TestValidateObserveGitBaseRejectInvalidUTF8UsesGenericError(t *testing.T) {
	req := observeRequestFixture(t, "/checkout/\xff")
	err := validateObserveGitBaseRequest(req)
	if err == nil || strings.Contains(err.Error(), "discard request") || !strings.Contains(err.Error(), "Git observation request") {
		t.Fatalf("validateObserveGitBaseRequest()=%v", err)
	}
}

func TestDiscardDigestUsesObserveGitBaseValidation(t *testing.T) {
	valid := observeRequestFixture(t, "/checkout")
	valid.BranchAction = BranchSwitchDiscard
	valid.RequestID = "10000000-0000-4000-8000-a00000000001"
	valid.Actor = diffActorEnvelope()

	for _, edit := range []func(*ObserveGitBaseRequest){
		func(req *ObserveGitBaseRequest) { req.ExpectedBinding.Checkout.Device = 0 },
		func(req *ObserveGitBaseRequest) { req.Scope.WorkspaceID = "20000000-0000-4000-8000-000000000002" },
		func(req *ObserveGitBaseRequest) { req.Root = "/other" },
		func(req *ObserveGitBaseRequest) { req.ExpectedCommit = strings.Repeat("A", 40) },
		func(req *ObserveGitBaseRequest) { req.Actor.Assurance = types.AssuranceLegacy },
	} {
		req := editObserveRequest(valid, edit)
		if validateErr := validateObserveGitBaseRequest(req); validateErr == nil {
			t.Fatal("validation unexpectedly accepted mutated request")
		}
		if digest, digestErr := discardRequestDigest(req); digestErr == nil || digest != "" {
			t.Fatalf("discardRequestDigest()=(%q,%v), want zero and error", digest, digestErr)
		}
	}
}

func TestReadGitBaseObservationValidatesBeforeReader(t *testing.T) {
	req := observeRequestFixture(t, "/checkout")
	req.ExpectedCommit = "invalid"
	called := false
	reader := func(context.Context, string, string) (committedWorkspace, error) {
		called = true
		return committedWorkspace{}, errors.New("reader must not run")
	}
	if _, err := readGitBaseObservation(context.Background(), req, reader); err == nil || called {
		t.Fatalf("readGitBaseObservation() error=%v reader_called=%v", err, called)
	}
}

func TestReadGitBaseObservationValidatesAndOwnsResult(t *testing.T) {
	req := observeRequestFixture(t, "/checkout")
	sourceTree := testSnapshotTree(t, req.Scope.ProjectID, req.ExpectedBinding.Repository)
	sourceSnapshot, err := state.DecodeTree(sourceTree)
	if err != nil {
		t.Fatal(err)
	}
	reader := func(_ context.Context, root, commit string) (committedWorkspace, error) {
		return committedWorkspace{
			root: root, checkout: req.ExpectedBinding.Checkout, acceptedRef: "refs/heads/next",
			commit: commit, tree: sourceTree, snapshot: sourceSnapshot,
		}, nil
	}

	observed, err := readGitBaseObservation(context.Background(), req, reader)
	if err != nil {
		t.Fatal(err)
	}
	wantTree := cloneCheckpointTree(observed.tree)
	wantSnapshotTree, err := state.EncodeTree(observed.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	sourceTree[0].Data[0] ^= 0xff
	if !reflect.DeepEqual(observed.tree, wantTree) {
		t.Fatal("observation aliases reader tree")
	}
	afterSourceMutation, err := state.EncodeTree(observed.snapshot)
	if err != nil || !reflect.DeepEqual(afterSourceMutation, wantSnapshotTree) {
		t.Fatal("observation snapshot aliases reader result")
	}
	observed.tree[0].Data[0] ^= 0xff
	afterTreeMutation, err := state.EncodeTree(observed.snapshot)
	if err != nil || !reflect.DeepEqual(afterTreeMutation, wantSnapshotTree) {
		t.Fatal("observation tree and snapshot alias each other")
	}
}

func TestReadGitBaseObservationRejectsMismatches(t *testing.T) {
	req := observeRequestFixture(t, "/checkout")
	validTree := testSnapshotTree(t, req.Scope.ProjectID, req.ExpectedBinding.Repository)
	otherRepository := types.RepositoryIdentity{Provider: "github", ImmutableID: "repo-2", CanonicalRemote: "https://github.com/acme/other"}
	tests := []struct {
		name string
		edit func(*committedWorkspace)
	}{
		{name: "root", edit: func(got *committedWorkspace) { got.root = "/other" }},
		{name: "checkout", edit: func(got *committedWorkspace) { got.checkout.Inode++ }},
		{name: "head", edit: func(got *committedWorkspace) { got.commit = strings.Repeat("b", 40) }},
		{name: "ref", edit: func(got *committedWorkspace) { got.acceptedRef = "refs/tags/not-a-branch" }},
		{name: "project", edit: func(got *committedWorkspace) {
			got.tree = testSnapshotTree(t, "20000000-0000-4000-8000-000000000002", req.ExpectedBinding.Repository)
		}},
		{name: "repository", edit: func(got *committedWorkspace) { got.tree = testSnapshotTree(t, req.Scope.ProjectID, otherRepository) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := func(_ context.Context, root, commit string) (committedWorkspace, error) {
				got := committedWorkspace{root: root, checkout: req.ExpectedBinding.Checkout, acceptedRef: "", commit: commit, tree: cloneCheckpointTree(validTree)}
				test.edit(&got)
				return got, nil
			}
			if _, err := readGitBaseObservation(context.Background(), req, reader); err == nil {
				t.Fatal("readGitBaseObservation() unexpectedly succeeded")
			}
		})
	}
}

func TestReadGitBaseObservationUsesSafeReaderForBranchAndDetachedHEAD(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	_, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	req := ObserveGitBaseRequest{Scope: registered.Binding.Scope, ExpectedBinding: registered.Binding, Root: repository.root, ExpectedCommit: repository.commit}

	branch, err := observeGitBaseOutside(context.Background(), req)
	if err != nil || branch.acceptedRef != "refs/heads/main" || branch.commit != repository.commit {
		t.Fatalf("branch observation=(%+v,%v)", branch, err)
	}
	runGit(t, repository.root, "checkout", "--detach", repository.commit)
	detached, err := observeGitBaseOutside(context.Background(), req)
	if err != nil || detached.acceptedRef != "" || detached.commit != repository.commit {
		t.Fatalf("detached observation=(%+v,%v)", detached, err)
	}
}

func TestReobserveGitBaseComparesOnlyCheckoutRefAndHEAD(t *testing.T) {
	outside := gitBaseObservation{
		root: "/checkout", checkout: types.CheckoutIdentity{CanonicalPath: "/checkout", Device: 1, Inode: 2},
		acceptedRef: "refs/heads/main", commit: strings.Repeat("a", 40),
	}
	base := gitBasePosition{root: outside.root, checkout: outside.checkout, acceptedRef: outside.acceptedRef, commit: outside.commit}
	tests := []struct {
		name    string
		edit    func(*gitBasePosition)
		readErr error
		wantErr bool
	}{
		{name: "unchanged"},
		{name: "canonical root", edit: func(got *gitBasePosition) { got.root = "/other" }, wantErr: true},
		{name: "checkout", edit: func(got *gitBasePosition) { got.checkout.Inode++ }, wantErr: true},
		{name: "same SHA ref", edit: func(got *gitBasePosition) { got.acceptedRef = "refs/heads/other" }, wantErr: true},
		{name: "head", edit: func(got *gitBasePosition) { got.commit = strings.Repeat("b", 40) }, wantErr: true},
		{name: "read failure", readErr: errors.New("git unavailable"), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := func(context.Context, string) (gitBasePosition, error) {
				got := base
				if test.edit != nil {
					test.edit(&got)
				}
				return got, test.readErr
			}
			err := reobserveGitBaseWithReader(context.Background(), outside, reader)
			if (err != nil) != test.wantErr || test.wantErr && !errors.Is(err, ErrGitObservationChanged) {
				t.Fatalf("reobserveGitBase()=%v, want changed=%v", err, test.wantErr)
			}
			if test.readErr != nil && !errors.Is(err, test.readErr) {
				t.Fatalf("reobserveGitBase()=%v does not preserve reader error", err)
			}
		})
	}
}

func TestReadGitBasePositionRevalidatesCheckoutAfterGitReads(t *testing.T) {
	wantRoot := "/checkout"
	wantCheckout := types.CheckoutIdentity{CanonicalPath: wantRoot, Device: 1, Inode: 2}
	wantRef := "refs/heads/main"
	wantCommit := strings.Repeat("a", 40)
	cause := errors.New("final identity unavailable")
	tests := []struct {
		name          string
		finalRoot     string
		finalCheckout types.CheckoutIdentity
		finalError    error
		wantError     bool
	}{
		{name: "stable", finalRoot: wantRoot, finalCheckout: wantCheckout},
		{name: "root changed", finalRoot: "/replacement", finalCheckout: wantCheckout, wantError: true},
		{name: "checkout changed", finalRoot: wantRoot, finalCheckout: types.CheckoutIdentity{CanonicalPath: wantRoot, Device: 1, Inode: 3}, wantError: true},
		{name: "final root error", finalError: cause, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			canonicalCalls := 0
			readers := gitBasePositionReaders{
				canonicalRoot: func(string) (string, error) {
					calls = append(calls, "root")
					canonicalCalls++
					if canonicalCalls == 1 {
						return wantRoot, nil
					}
					if test.finalError != nil {
						return "", test.finalError
					}
					return test.finalRoot, nil
				},
				checkoutIdentity: func(string) (types.CheckoutIdentity, error) {
					calls = append(calls, "checkout")
					if len(calls) == 2 {
						return wantCheckout, nil
					}
					return test.finalCheckout, nil
				},
				symbolicHead: func(context.Context, string) (string, error) {
					calls = append(calls, "ref")
					return wantRef, nil
				},
				headCommit: func(context.Context, string) (string, error) {
					calls = append(calls, "head")
					return wantCommit, nil
				},
			}
			outside := gitBaseObservation{root: wantRoot, checkout: wantCheckout, acceptedRef: wantRef, commit: wantCommit}
			reader := func(ctx context.Context, root string) (gitBasePosition, error) {
				return readGitBasePositionWithReaders(ctx, root, readers)
			}
			err := reobserveGitBaseWithReader(context.Background(), outside, reader)
			if (err != nil) != test.wantError || test.wantError && !errors.Is(err, ErrGitObservationChanged) {
				t.Fatalf("reobserveGitBaseWithReader()=%v, want changed=%v", err, test.wantError)
			}
			if test.finalError != nil && !errors.Is(err, test.finalError) {
				t.Fatalf("reobserveGitBaseWithReader()=%v does not preserve final error", err)
			}
			wantCalls := []string{"root", "checkout", "ref", "head", "root"}
			if test.finalError == nil && test.finalRoot == wantRoot {
				wantCalls = append(wantCalls, "checkout")
			}
			if !reflect.DeepEqual(calls, wantCalls) {
				t.Fatalf("reader calls=%v, want %v", calls, wantCalls)
			}
		})
	}
}

func TestCommittedWorkspaceObservationFinalChecksPreserveErrors(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	checkout, err := checkoutIdentity(repository.root)
	if err != nil {
		t.Fatal(err)
	}
	headCause := errors.New("final HEAD unavailable")
	checkoutCause := errors.New("final checkout unavailable")
	tests := []struct {
		name      string
		finalHead func(context.Context, string) (string, error)
		checkout  func(string) (types.CheckoutIdentity, error)
		cause     error
	}{
		{
			name: "HEAD error", cause: headCause,
			finalHead: func(context.Context, string) (string, error) { return "", headCause },
			checkout:  checkoutIdentity,
		},
		{
			name:      "HEAD changed",
			finalHead: func(context.Context, string) (string, error) { return strings.Repeat("b", 40), nil },
			checkout:  checkoutIdentity,
		},
		{
			name: "checkout error", cause: checkoutCause,
			finalHead: func(context.Context, string) (string, error) { return repository.commit, nil },
			checkout:  func(string) (types.CheckoutIdentity, error) { return types.CheckoutIdentity{}, checkoutCause },
		},
		{
			name:      "checkout changed",
			finalHead: func(context.Context, string) (string, error) { return repository.commit, nil },
			checkout: func(string) (types.CheckoutIdentity, error) {
				changed := checkout
				changed.Inode++
				return changed, nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := inspectCommittedWorkspaceWithFinalReaders(context.Background(), repository.root, repository.commit, true, committedWorkspaceFinalReaders{
				headCommit: test.finalHead, checkoutIdentity: test.checkout,
			})
			if !errors.Is(err, ErrGitObservationChanged) {
				t.Fatalf("inspectCommittedWorkspaceWithFinalReaders()=%v, want ErrGitObservationChanged", err)
			}
			if test.cause != nil && !errors.Is(err, test.cause) {
				t.Fatalf("inspectCommittedWorkspaceWithFinalReaders()=%v does not preserve cause", err)
			}
		})
	}
}

func TestCommittedWorkspaceRegistrationFinalCheckErrorsRemainUnchanged(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	cause := errors.New("final check unavailable")
	_, headErr := inspectCommittedWorkspaceWithFinalReaders(context.Background(), repository.root, repository.commit, false, committedWorkspaceFinalReaders{
		headCommit: func(context.Context, string) (string, error) { return "", cause }, checkoutIdentity: checkoutIdentity,
	})
	if headErr == nil || headErr.Error() != "projectstate: Git HEAD changed during registration" || errors.Is(headErr, cause) || errors.Is(headErr, ErrGitObservationChanged) {
		t.Fatalf("registration final HEAD error=%v", headErr)
	}
	_, checkoutErr := inspectCommittedWorkspaceWithFinalReaders(context.Background(), repository.root, repository.commit, false, committedWorkspaceFinalReaders{
		headCommit:       func(context.Context, string) (string, error) { return repository.commit, nil },
		checkoutIdentity: func(string) (types.CheckoutIdentity, error) { return types.CheckoutIdentity{}, cause },
	})
	if !errors.Is(checkoutErr, cause) || errors.Is(checkoutErr, ErrGitObservationChanged) {
		t.Fatalf("registration final checkout error=%v", checkoutErr)
	}
}

func TestReobserveGitBaseDetectsRealRefAndHEADChanges(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	_, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	req := ObserveGitBaseRequest{Scope: registered.Binding.Scope, ExpectedBinding: registered.Binding, Root: repository.root, ExpectedCommit: repository.commit}
	outside, err := observeGitBaseOutside(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if err := reobserveGitBase(context.Background(), outside); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository.root, "switch", "-c", "other")
	if err := reobserveGitBase(context.Background(), outside); !errors.Is(err, ErrGitObservationChanged) {
		t.Fatalf("same-SHA ref switch error=%v", err)
	}
	runGit(t, repository.root, "commit", "--allow-empty", "-m", "new head")
	current := outside
	current.acceptedRef = "refs/heads/other"
	if err := reobserveGitBase(context.Background(), current); !errors.Is(err, ErrGitObservationChanged) {
		t.Fatalf("HEAD change error=%v", err)
	}
}

func observeRequestFixture(t *testing.T, root string) ObserveGitBaseRequest {
	t.Helper()
	projectID := "00000000-0000-4000-8000-000000000001"
	tree := testSnapshotTree(t, projectID, types.RepositoryIdentity{})
	snapshot, err := state.DecodeTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	scope := types.WorkspaceScope{ProjectID: projectID, WorkspaceID: "00000000-0000-4000-8000-000000000010"}
	binding := types.WorkspaceBinding{
		Scope: scope, Checkout: types.CheckoutIdentity{CanonicalPath: root, Device: 1, Inode: 2},
		Repository: types.RepositoryIdentity{}, AcceptedRef: "refs/heads/main",
		AcceptedCommitSHA: strings.Repeat("a", 40), AcceptedTreeDigest: string(snapshot.Digest),
	}
	return ObserveGitBaseRequest{Scope: scope, ExpectedBinding: binding, Root: root, ExpectedCommit: strings.Repeat("b", 64), BranchAction: BranchSwitchReject}
}

func editObserveRequest(request ObserveGitBaseRequest, edit func(*ObserveGitBaseRequest)) ObserveGitBaseRequest {
	edit(&request)
	return request
}

func TestGitObservationReaderErrorsPreserveChangeSentinel(t *testing.T) {
	req := observeRequestFixture(t, "/checkout")
	reader := func(context.Context, string, string) (committedWorkspace, error) {
		return committedWorkspace{}, fmt.Errorf("reader race: %w", ErrGitObservationChanged)
	}
	if _, err := readGitBaseObservation(context.Background(), req, reader); !errors.Is(err, ErrGitObservationChanged) {
		t.Fatalf("readGitBaseObservation() error=%v", err)
	}
}

func TestGitBaseObservationTreeMatchesSnapshot(t *testing.T) {
	req := observeRequestFixture(t, "/checkout")
	tree := testSnapshotTree(t, req.Scope.ProjectID, req.ExpectedBinding.Repository)
	reader := func(_ context.Context, root, commit string) (committedWorkspace, error) {
		return committedWorkspace{root: root, checkout: req.ExpectedBinding.Checkout, acceptedRef: "", commit: commit, tree: tree}, nil
	}
	observed, err := readGitBaseObservation(context.Background(), req, reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := state.EncodeTree(observed.snapshot)
	if err != nil || !reflect.DeepEqual(encoded, observed.tree) {
		t.Fatalf("observation tree/snapshot mismatch: %v", err)
	}
}
