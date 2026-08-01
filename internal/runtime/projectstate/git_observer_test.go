package projectstate

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

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
