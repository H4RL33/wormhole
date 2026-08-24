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

func checkpointRecoveryMixedOperationInput(t *testing.T) checkpointPlanInput {
	t.Helper()
	input := checkpointPlanFixture(t)
	direct := checkpointPlanMutatedSnapshot(t, input.Composed.status.AcceptedSnapshot, "recovery mixed direct")
	rebasedOperation, rebased := checkpointPlanProjectOperation(
		t, direct, "90000000-0000-4000-8000-000000000041", "recovery mixed rebased",
	)
	activeOperation, _ := checkpointPlanProjectOperation(
		t, rebased, "90000000-0000-4000-8000-000000000042", "recovery mixed active",
	)
	input.Current = checkpointPlanCandidate(input.Binding, direct, &rebased, 1)
	input.Disposition.Operations = []localstore.WorkspaceOperation{
		checkpointPlanOperationRow(t, 1, "rebased", rebasedOperation),
		checkpointPlanOperationRow(t, 2, "active", activeOperation),
	}
	checkpointPlanRefresh(t, &input)
	return input
}

func TestProveCheckpointRecoveryDispositionRequiresDriverWorkspaceState(t *testing.T) {
	_, workspace, priorCandidate, prepared, operations := checkpointRecoveryProofFixtureWithInput(t)
	publicationCandidate := func(journal localstore.WorkspaceMaterializationRecord) *localstore.WorkspaceCandidateRecord {
		candidate, err := checkpointPublicationPostimage(journal)
		if err != nil {
			t.Fatal(err)
		}
		return &candidate
	}
	publicationRows := func() []localstore.WorkspaceOperation {
		rows := cloneImportCurrentMaterialization(localstore.WorkspaceCurrentMaterialization{Operations: operations}).Operations
		for index := range rows {
			rows[index].State = "materialized"
		}
		return rows
	}
	tests := []struct {
		name         string
		journalState string
		workspace    string
		wantKind     checkpointRecoveryKind
		wantError    bool
	}{
		{name: "prepared clean", journalState: "prepared", workspace: "clean", wantKind: checkpointRecoveryPrepared},
		{name: "prepared pending", journalState: "prepared", workspace: "pending", wantKind: checkpointRecoveryPrepared},
		{name: "prepared conflicted", journalState: "prepared", workspace: "conflicted", wantError: true},
		{name: "published pending", journalState: "published", workspace: "pending", wantKind: checkpointRecoveryPublished},
		{name: "published clean", journalState: "published", workspace: "clean", wantError: true},
		{name: "published conflicted", journalState: "published", workspace: "conflicted", wantError: true},
		{name: "recovered-new pending", journalState: "recovered_new", workspace: "pending", wantKind: checkpointRecoveryNoWork},
		{name: "recovered-new clean", journalState: "recovered_new", workspace: "clean", wantError: true},
		{name: "recovered-new conflicted", journalState: "recovered_new", workspace: "conflicted", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			currentWorkspace := workspace
			currentWorkspace.State = test.workspace
			journal := cloneMaterializationRecord(prepared)
			journal.State = test.journalState
			candidate := priorCandidate
			rows := operations
			if test.journalState != "prepared" {
				candidate = publicationCandidate(journal)
				rows = publicationRows()
			}
			proof, err := proveCheckpointRecoveryDisposition(currentWorkspace, candidate, localstore.WorkspaceCurrentMaterialization{
				Journals: []localstore.WorkspaceMaterializationRecord{journal}, Operations: rows,
			})
			if test.wantError {
				if err == nil || !errors.Is(err, ErrCheckpointRecoveryPrecondition) || !reflect.DeepEqual(proof, checkpointRecoveryProof{}) {
					t.Fatalf("driver state proof = (%+v, %v), want exact zero precondition", proof, err)
				}
				return
			}
			if err != nil || proof.kind != test.wantKind {
				t.Fatalf("valid driver state proof = (%+v, %v), want kind %d", proof, err, test.wantKind)
			}
		})
	}

	t.Run("partial ownership fails before workspace state", func(t *testing.T) {
		currentWorkspace := workspace
		currentWorkspace.State = "conflicted"
		rows := cloneImportCurrentMaterialization(localstore.WorkspaceCurrentMaterialization{Operations: operations}).Operations
		rows[0].State = "materialized"
		proof, err := proveCheckpointRecoveryDisposition(currentWorkspace, priorCandidate, localstore.WorkspaceCurrentMaterialization{
			Journals: []localstore.WorkspaceMaterializationRecord{prepared}, Operations: rows,
		})
		if err == nil || !errors.Is(err, ErrCheckpointRecoveryPrecondition) ||
			!strings.Contains(err.Error(), "driver operation ownership") ||
			strings.Contains(err.Error(), "workspace state differs") || !reflect.DeepEqual(proof, checkpointRecoveryProof{}) {
			t.Fatalf("partial ownership precedence = (%+v, %v)", proof, err)
		}
	})
}

func TestObserveCheckpointRecoveryGitAllowsOnlyStoredOrSameRefCandidate(t *testing.T) {
	workspace, priorCandidate, prepared, operations := checkpointRecoveryProofFixture(t)
	preparedProof, err := proveCheckpointRecoveryDisposition(workspace, priorCandidate, localstore.WorkspaceCurrentMaterialization{
		Journals: []localstore.WorkspaceMaterializationRecord{prepared}, Operations: operations,
	})
	if err != nil || preparedProof.kind != checkpointRecoveryPrepared {
		t.Fatalf("prepared Git-observation proof=(kind=%d,%v)", preparedProof.kind, err)
	}
	journal := cloneMaterializationRecord(prepared)
	journal.State = "published"
	publishedCandidate, err := checkpointPublicationPostimage(journal)
	if err != nil {
		t.Fatal(err)
	}
	materialized := cloneImportCurrentMaterialization(localstore.WorkspaceCurrentMaterialization{Operations: operations}).Operations
	for index := range materialized {
		materialized[index].State = "materialized"
	}
	publishedWorkspace, err := cloneImportWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	publishedWorkspace.State = "pending"
	publishedProof, err := proveCheckpointRecoveryDisposition(publishedWorkspace, &publishedCandidate, localstore.WorkspaceCurrentMaterialization{
		Journals: []localstore.WorkspaceMaterializationRecord{journal}, Operations: materialized,
	})
	if err != nil || publishedProof.kind != checkpointRecoveryPublished {
		t.Fatalf("published Git-observation proof=(kind=%d,%v)", publishedProof.kind, err)
	}

	acceptedTree, err := state.EncodeTree(workspace.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	originValue := observedOriginV1{SchemaVersion: 1, Kind: "network", Host: "github.com", Path: "acme/wormhole"}
	originDigest, err := digestObservedOrigin(originValue)
	if err != nil {
		t.Fatal(err)
	}
	origin := publicationOriginObservation{
		root: workspace.Binding.Checkout.CanonicalPath, checkout: workspace.Binding.Checkout,
		origin: originValue, digest: originDigest,
	}

	type observationEdits struct {
		initial   func(*gitBasePosition)
		committed func(*committedWorkspace)
		final     func(*gitBasePosition)
		origin    func(*publicationOriginObservation)
		originErr error
	}
	run := func(t *testing.T, proof checkpointRecoveryProof, commit string, tree state.Tree, edits observationEdits) (checkpointRecoveryGitObservation, error, []string) {
		t.Helper()
		position := gitBasePosition{
			root: workspace.Binding.Checkout.CanonicalPath, checkout: workspace.Binding.Checkout,
			acceptedRef: workspace.Binding.AcceptedRef, commit: commit,
		}
		if edits.initial != nil {
			edits.initial(&position)
		}
		snapshot, decodeErr := state.DecodeTree(cloneCheckpointTree(tree))
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		committed := committedWorkspace{
			root: position.root, checkout: position.checkout, acceptedRef: position.acceptedRef,
			commit: position.commit, tree: cloneCheckpointTree(tree), snapshot: snapshot,
		}
		originForPosition := origin
		originForPosition.root = position.root
		originForPosition.checkout = position.checkout
		var calls []string
		positionCalls := 0
		readers := checkpointRecoveryGitReaders{
			position: func(_ context.Context, root string) (gitBasePosition, error) {
				calls = append(calls, "position")
				if root != position.root {
					t.Fatalf("position root=%q, want %q", root, position.root)
				}
				positionCalls++
				got := position
				if positionCalls == 2 && edits.final != nil {
					edits.final(&got)
				}
				return got, nil
			},
			committed: func(_ context.Context, root, requestedCommit string) (committedWorkspace, error) {
				calls = append(calls, "committed")
				if root != position.root || requestedCommit != position.commit {
					t.Fatalf("committed request=(%q,%q), want (%q,%q)", root, requestedCommit, position.root, position.commit)
				}
				got := committed
				if edits.committed != nil {
					edits.committed(&got)
				}
				return got, nil
			},
			origin: func(_ context.Context, root string) (publicationOriginObservation, error) {
				calls = append(calls, "origin")
				if root != position.root {
					t.Fatalf("origin root=%q, want %q", root, position.root)
				}
				got := originForPosition
				if edits.origin != nil {
					edits.origin(&got)
				}
				return got, edits.originErr
			},
		}
		observed, observeErr := observeCheckpointRecoveryGitWithReaders(context.Background(), proof, readers)
		committed.tree[0].Data[0] ^= 0xff
		return observed, observeErr, calls
	}

	candidateCommit := strings.Repeat("b", 40)
	assertSuccess := func(t *testing.T, proof checkpointRecoveryProof, commit string, tree state.Tree) {
		t.Helper()
		observed, err, calls := run(t, proof, commit, tree, observationEdits{})
		snapshot, decodeErr := state.DecodeTree(cloneCheckpointTree(tree))
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		position := gitBasePosition{
			root: workspace.Binding.Checkout.CanonicalPath, checkout: workspace.Binding.Checkout,
			acceptedRef: workspace.Binding.AcceptedRef, commit: commit,
		}
		want := checkpointRecoveryGitObservation{
			position: position,
			committed: committedWorkspace{
				root: position.root, checkout: position.checkout, acceptedRef: position.acceptedRef,
				commit: position.commit, tree: cloneCheckpointTree(tree), snapshot: snapshot,
			},
			origin: origin, finalPosition: position,
		}
		if err != nil || !reflect.DeepEqual(calls, []string{"position", "committed", "origin", "position"}) ||
			!reflect.DeepEqual(observed, want) {
			t.Fatalf("successful observation=(exact=%t, err=%v), calls=%v", reflect.DeepEqual(observed, want), err, calls)
		}
	}
	for _, driver := range []struct {
		name  string
		proof checkpointRecoveryProof
	}{
		{name: "prepared", proof: preparedProof},
		{name: "published", proof: publishedProof},
	} {
		t.Run(driver.name, func(t *testing.T) {
			t.Run("stored base", func(t *testing.T) {
				assertSuccess(t, driver.proof, workspace.Binding.AcceptedCommitSHA, acceptedTree)
			})
			t.Run("same-ref exact candidate", func(t *testing.T) {
				assertSuccess(t, driver.proof, candidateCommit, journal.CandidateTree)
			})
		})
	}

	for _, test := range []struct {
		name               string
		commit             string
		tree               state.Tree
		edits              observationEdits
		wantInitialOnly    bool
		wantCompleteBundle bool
		wantRacePrecedence bool
	}{
		{name: "different commit wrong tree", commit: candidateCommit, tree: acceptedTree},
		{name: "stored commit candidate tree", commit: workspace.Binding.AcceptedCommitSHA, tree: journal.CandidateTree},
		{name: "replaced initial checkout", commit: candidateCommit, tree: journal.CandidateTree,
			edits: observationEdits{initial: func(value *gitBasePosition) {
				value.checkout.Inode++
			}}, wantInitialOnly: true},
		{name: "changed checkout", commit: candidateCommit, tree: journal.CandidateTree, edits: observationEdits{
			committed: func(value *committedWorkspace) { value.checkout.Inode++ },
		}},
		{name: "changed project", commit: candidateCommit, tree: journal.CandidateTree, edits: observationEdits{
			committed: func(value *committedWorkspace) {
				value.snapshot = checkpointPlanRetargetProject(t, value.snapshot)
				value.tree = mustCheckpointPlanTree(t, value.snapshot)
			},
		}},
		{name: "changed repository", commit: candidateCommit, tree: journal.CandidateTree, edits: observationEdits{
			committed: func(value *committedWorkspace) {
				value.snapshot = checkpointPlanRetargetRepository(t, value.snapshot)
				value.tree = mustCheckpointPlanTree(t, value.snapshot)
			},
		}},
		{name: "position race", commit: candidateCommit, tree: journal.CandidateTree,
			edits:              observationEdits{final: func(value *gitBasePosition) { value.commit = strings.Repeat("c", 40) }},
			wantCompleteBundle: true, wantRacePrecedence: true},
		{name: "malformed origin", commit: candidateCommit, tree: journal.CandidateTree,
			edits: observationEdits{origin: func(value *publicationOriginObservation) {
				value.digest = "sha256:" + state.Digest(strings.Repeat("f", 64))
			}}, wantCompleteBundle: true},
		{name: "malformed origin plus position race", commit: candidateCommit, tree: journal.CandidateTree,
			edits: observationEdits{
				origin: func(value *publicationOriginObservation) {
					value.digest = "sha256:" + state.Digest(strings.Repeat("f", 64))
				},
				final: func(value *gitBasePosition) { value.commit = strings.Repeat("c", 40) },
			}, wantCompleteBundle: true, wantRacePrecedence: true},
		{name: "failing origin plus position race", commit: candidateCommit, tree: journal.CandidateTree,
			edits: observationEdits{
				originErr: errors.New("injected origin failure"),
				final:     func(value *gitBasePosition) { value.commit = strings.Repeat("c", 40) },
			}, wantCompleteBundle: true, wantRacePrecedence: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			observed, err, calls := run(t, publishedProof, test.commit, test.tree, test.edits)
			if err == nil || !errors.Is(err, ErrCheckpointRecoveryPrecondition) ||
				!reflect.DeepEqual(observed, checkpointRecoveryGitObservation{}) {
				t.Fatalf("rejected observation=(%+v,%v), calls=%v", observed, err, calls)
			}
			if test.wantInitialOnly && !reflect.DeepEqual(calls, []string{"position"}) {
				t.Fatalf("initial-checkout calls=%v, want rejection before committed/origin reads", calls)
			}
			if test.wantCompleteBundle && !reflect.DeepEqual(calls, []string{"position", "committed", "origin", "position"}) {
				t.Fatalf("race/origin calls=%v, want complete ordered bundle", calls)
			}
			if test.wantRacePrecedence && !strings.Contains(err.Error(), "Git position changed across recovery observation") {
				t.Fatalf("combined race error=%v, want final-position-race precedence", err)
			}
		})
	}

	t.Run("changed ref", func(t *testing.T) {
		calls := 0
		observed, err := observeCheckpointRecoveryGitWithReaders(context.Background(), publishedProof, checkpointRecoveryGitReaders{
			position: func(_ context.Context, root string) (gitBasePosition, error) {
				calls++
				if root != workspace.Binding.Checkout.CanonicalPath {
					t.Fatalf("changed-ref position root=%q", root)
				}
				return gitBasePosition{
					root: workspace.Binding.Checkout.CanonicalPath, checkout: workspace.Binding.Checkout,
					acceptedRef: "refs/heads/other", commit: workspace.Binding.AcceptedCommitSHA,
				}, nil
			},
			committed: func(context.Context, string, string) (committedWorkspace, error) {
				panic("committed tree read after ref drift")
			},
			origin: func(context.Context, string) (publicationOriginObservation, error) {
				panic("origin read after ref drift")
			},
		})
		if calls != 1 || err == nil || !errors.Is(err, ErrCheckpointRecoveryPrecondition) ||
			!reflect.DeepEqual(observed, checkpointRecoveryGitObservation{}) {
			t.Fatalf("changed ref observation=(%+v,%v), calls=%d", observed, err, calls)
		}
	})
}

func checkpointRecoveryProofFixture(t *testing.T) (
	localstore.WorkspaceRecord,
	*localstore.WorkspaceCandidateRecord,
	localstore.WorkspaceMaterializationRecord,
	[]localstore.WorkspaceOperation,
) {
	t.Helper()
	_, workspace, candidate, journal, operations := checkpointRecoveryProofFixtureWithInput(t)
	return workspace, candidate, journal, operations
}

func checkpointRecoveryProofFixtureWithInput(t *testing.T) (
	checkpointPlanInput,
	localstore.WorkspaceRecord,
	*localstore.WorkspaceCandidateRecord,
	localstore.WorkspaceMaterializationRecord,
	[]localstore.WorkspaceOperation,
) {
	t.Helper()
	input := checkpointPlanActiveInput(t, checkpointPlanDirectInput(t, checkpointPlanFixture(t)))
	workspace, candidate, journal, operations := checkpointRecoveryProofFixtureForInput(t, input)
	return input, workspace, candidate, journal, operations
}

func checkpointRecoveryProofFixtureForInput(
	t *testing.T,
	input checkpointPlanInput,
) (
	localstore.WorkspaceRecord,
	*localstore.WorkspaceCandidateRecord,
	localstore.WorkspaceMaterializationRecord,
	[]localstore.WorkspaceOperation,
) {
	t.Helper()
	journal := checkpointJournalFromPlan(t, input)
	privateRoot := t.TempDir()
	journal.StagePath = filepath.Join(privateRoot, journal.JournalID+".stage")
	journal.BackupPath = filepath.Join(privateRoot, journal.JournalID+".backup")
	candidate, err := cloneImportCandidate(input.Current)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := cloneImportWorkspace(localstore.WorkspaceRecord{
		Binding: input.Binding, Snapshot: input.Composed.status.AcceptedSnapshot, State: input.Composed.status.State,
	})
	if err != nil {
		t.Fatal(err)
	}
	return workspace, candidate, journal, cloneImportCurrentMaterialization(input.Disposition).Operations
}

func TestRecoverTerminalOrEmptyHistoryReturnsDatabaseComposedStatusWithoutGitOrPathIO(t *testing.T) {
	for _, history := range []string{"empty", "accepted", "recovered_old", "recovered_new"} {
		t.Run(history, func(t *testing.T) {
			service, scope, root := recoveryNoWorkServiceFixture(t, history)
			want := recoveryComposedStatus(t, service, scope)
			before := recoveryDatabaseState(t, service, scope)
			if err := os.Rename(root, root+"-moved-for-recovery"); err != nil {
				t.Fatal(err)
			}
			service.observeCheckpointRecoveryGit = func(context.Context, checkpointRecoveryProof) (checkpointRecoveryGitObservation, error) {
				panic("no-work recovery observed Git")
			}
			service.recoverCheckpointFilesystem = func(context.Context, checkpointRecoveryProof) (checkpointRecoveryFilesystemOutcome, error) {
				panic("no-work recovery touched paths")
			}

			got, err := service.Recover(context.Background(), scope)
			if err != nil || !reflect.DeepEqual(got, want) || got.PublicationClassification != "" || got.PublicationReviewDigest != "" {
				t.Fatalf("Recover(%s)=(%+v,%v), want %+v", history, got, err, want)
			}
			got.AcceptedSnapshot.Project.Name = "mutated caller result"
			fresh := recoveryComposedStatus(t, service, scope)
			if fresh.AcceptedSnapshot.Project.Name == got.AcceptedSnapshot.Project.Name {
				t.Fatal("Recover returned status aliases durable state")
			}
			if after := recoveryDatabaseState(t, service, scope); !reflect.DeepEqual(after, before) {
				t.Fatal("no-work Recover changed database state")
			}
		})
	}

}

func TestRecoverFailsScopeCheckoutRootAndGitPreconditionsBeforePathMutation(t *testing.T) {
	t.Run("service context and scope", func(t *testing.T) {
		var nilService *Service
		if got, err := nilService.Recover(context.Background(), types.WorkspaceScope{}); !reflect.DeepEqual(got, WorkspaceStatus{}) || !errors.Is(err, localstore.ErrNotFound) {
			t.Fatalf("nil-service Recover=(%+v,%v)", got, err)
		}
		service, scope, _ := recoveryNoWorkServiceFixture(t, "empty")
		if got, err := service.Recover(nil, scope); !reflect.DeepEqual(got, WorkspaceStatus{}) || !errors.Is(err, localstore.ErrNotFound) {
			t.Fatalf("nil-context Recover=(%+v,%v)", got, err)
		}
		scope.WorkspaceID = "invalid"
		if got, err := service.Recover(context.Background(), scope); !reflect.DeepEqual(got, WorkspaceStatus{}) || !errors.Is(err, localstore.ErrNotFound) {
			t.Fatalf("invalid-scope Recover=(%+v,%v)", got, err)
		}
	})

	gitFailure := errors.New("injected recovery Git failure")
	for _, test := range []struct {
		name      string
		mutate    func(*checkpointCoordinatorFixture, CheckpointRequest)
		wantCause error
	}{
		{name: "Git bundle", wantCause: gitFailure, mutate: func(fixture *checkpointCoordinatorFixture, _ CheckpointRequest) {
			fixture.service.observeCheckpointRecoveryGit = func(context.Context, checkpointRecoveryProof) (checkpointRecoveryGitObservation, error) {
				return checkpointRecoveryGitObservation{}, gitFailure
			}
		}},
		{name: "checkout", mutate: func(fixture *checkpointCoordinatorFixture, _ CheckpointRequest) {
			if err := os.Rename(fixture.repository.root, fixture.repository.root+"-moved"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, request := recoveryDriverPlanFixture(t, "prepared")
			before := recoveryDatabaseState(t, fixture.service, request.Scope)
			test.mutate(fixture, request)
			pathCalls := 0
			fixture.service.recoverCheckpointFilesystem = func(context.Context, checkpointRecoveryProof) (checkpointRecoveryFilesystemOutcome, error) {
				pathCalls++
				return checkpointRecoveryFilesystemOutcome(99), nil
			}
			got, err := fixture.service.Recover(context.Background(), request.Scope)
			if !reflect.DeepEqual(got, WorkspaceStatus{}) || !errors.Is(err, ErrCheckpointRecoveryPrecondition) || pathCalls != 0 {
				t.Fatalf("%s Recover=(%+v,%v), pathCalls=%d", test.name, got, err, pathCalls)
			}
			if test.wantCause != nil && !errors.Is(err, test.wantCause) {
				t.Fatalf("%s Recover error=%v, want cause %v", test.name, err, test.wantCause)
			}
			if after := recoveryDatabaseState(t, fixture.service, request.Scope); !reflect.DeepEqual(after, before) {
				t.Fatalf("%s precondition changed database state", test.name)
			}
		})
	}

	t.Run("journal direct-child name", func(t *testing.T) {
		fixture, request := recoveryDriverPlanFixture(t, "prepared")
		invalidStage := filepath.Join(filepath.Dir(readCheckpointDisposition(t, fixture.service, request.Scope).Journals[0].StagePath), "not-the-journal.stage")
		if _, err := fixture.store.DB().Exec(`
			UPDATE workspace_materializations SET stage_path=? WHERE project_id=? AND workspace_id=?
		`, invalidStage, request.Scope.ProjectID, request.Scope.WorkspaceID); err != nil {
			t.Fatal(err)
		}
		before := recoveryDatabaseState(t, fixture.service, request.Scope)
		fixture.service.observeCheckpointRecoveryGit = func(context.Context, checkpointRecoveryProof) (checkpointRecoveryGitObservation, error) {
			panic("invalid journal path observed Git")
		}
		fixture.service.recoverCheckpointFilesystem = func(context.Context, checkpointRecoveryProof) (checkpointRecoveryFilesystemOutcome, error) {
			panic("invalid journal path touched filesystem")
		}
		got, err := fixture.service.Recover(context.Background(), request.Scope)
		if !reflect.DeepEqual(got, WorkspaceStatus{}) || !errors.Is(err, ErrCheckpointRecoveryPrecondition) {
			t.Fatalf("invalid journal path Recover=(%+v,%v)", got, err)
		}
		if after := recoveryDatabaseState(t, fixture.service, request.Scope); !reflect.DeepEqual(after, before) {
			t.Fatal("invalid journal path recovery changed database state")
		}
	})
}

func TestRecoverRejectsCrossProjectDriverWithoutMutatingEitherScope(t *testing.T) {
	fixture, request := recoveryDriverPlanFixture(t, "prepared")
	recoveryEnsureDriverArtifactEvidence(t, fixture.service, request.Scope)
	neighborRepository := createGitRepository(t, "00000000-0000-4000-8000-000000000002")
	neighbor := registerGitRepository(t, fixture.service, neighborRepository)
	recoveryAssertCrossScopeRejected(t, fixture.service, request.Scope, neighbor.Binding.Scope, types.WorkspaceScope{
		ProjectID: neighbor.Binding.Scope.ProjectID, WorkspaceID: request.Scope.WorkspaceID,
	})
}

func TestRecoverRejectsCrossWorkspaceDriverWithoutMutatingEitherScope(t *testing.T) {
	fixture, request := recoveryDriverPlanFixture(t, "prepared")
	recoveryEnsureDriverArtifactEvidence(t, fixture.service, request.Scope)
	neighborRepository := createGitRepository(t, "00000000-0000-4000-8000-000000000002")
	neighbor := registerGitRepository(t, fixture.service, neighborRepository)
	recoveryAssertCrossScopeRejected(t, fixture.service, request.Scope, neighbor.Binding.Scope, types.WorkspaceScope{
		ProjectID: request.Scope.ProjectID, WorkspaceID: neighbor.Binding.Scope.WorkspaceID,
	})
}

func TestRecoverHoldsOneImmediateTransactionAcrossOneGitBundleAndConvergence(t *testing.T) {
	fixture, request := recoveryDriverPlanFixture(t, "prepared")
	if _, err := fixture.store.DB().Exec(`CREATE TABLE checkpoint_recovery_writer_probe(value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	writerConn, err := fixture.store.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer writerConn.Close()

	realWithImmediate := fixture.service.repo.WithImmediateWorkspace
	transactionCalls := 0
	fixture.service.withImmediateWorkspace = func(ctx context.Context, scope types.WorkspaceScope, fn func(*localstore.WorkspaceMutationTx) error) error {
		transactionCalls++
		return realWithImmediate(ctx, scope, fn)
	}
	gitCalls := 0
	fixture.service.observeCheckpointRecoveryGit = func(_ context.Context, proof checkpointRecoveryProof) (checkpointRecoveryGitObservation, error) {
		gitCalls++
		return recoveryPlannerObservation(t, proof, "prepared"), nil
	}
	filesystemCalls := 0
	fixture.service.recoverCheckpointFilesystem = func(ctx context.Context, proof checkpointRecoveryProof) (checkpointRecoveryFilesystemOutcome, error) {
		filesystemCalls++
		blockedCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancel()
		_, blockedErr := writerConn.ExecContext(blockedCtx, `INSERT INTO checkpoint_recovery_writer_probe(value) VALUES ('blocked')`)
		if !errors.Is(blockedErr, context.DeadlineExceeded) {
			t.Fatalf("writer during recovery=%v, want context deadline exceeded", blockedErr)
		}
		return checkpointRecoveryFilesystemRecoveredOld, nil
	}

	got, err := fixture.service.Recover(context.Background(), request.Scope)
	if err != nil || got.Binding.Scope != request.Scope || transactionCalls != 1 || gitCalls != 1 || filesystemCalls != 1 {
		t.Fatalf("Recover=(%+v,%v), transactions=%d Git=%d filesystem=%d", got, err, transactionCalls, gitCalls, filesystemCalls)
	}
	if _, err := writerConn.ExecContext(context.Background(), `INSERT INTO checkpoint_recovery_writer_probe(value) VALUES ('after')`); err != nil {
		t.Fatalf("writer after recovery: %v", err)
	}
	var markers int
	if err := fixture.store.DB().QueryRow(`SELECT COUNT(*) FROM checkpoint_recovery_writer_probe`).Scan(&markers); err != nil || markers != 1 {
		t.Fatalf("writer markers=(%d,%v), want 1", markers, err)
	}
	disposition := readCheckpointDisposition(t, fixture.service, request.Scope)
	if len(disposition.Journals) != 0 {
		t.Fatalf("recovery disposition=%+v", disposition)
	}
	recoveryAssertReturnedStatus(t, fixture.service, request.Scope, got)
}

func TestWorkspaceRevisionRecoveryFinalizationAdvancesOnce(t *testing.T) {
	for _, test := range []struct {
		name             string
		driver           string
		filesystem       checkpointRecoveryFilesystemOutcome
		wantJournalState string
	}{
		{name: "recovered old", driver: "prepared", filesystem: checkpointRecoveryFilesystemRecoveredOld, wantJournalState: "recovered_old"},
		{name: "recovered new", driver: "published", filesystem: checkpointRecoveryFilesystemRecoveredNew, wantJournalState: "recovered_new"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, request := recoveryDriverPlanFixture(t, test.driver)
			beforeRevision := workspaceRevisionForProjectStateTest(t, fixture.service, request.Scope)
			fixture.service.observeCheckpointRecoveryGit = func(_ context.Context, proof checkpointRecoveryProof) (checkpointRecoveryGitObservation, error) {
				return recoveryPlannerObservation(t, proof, test.driver), nil
			}
			fixture.service.recoverCheckpointFilesystem = func(context.Context, checkpointRecoveryProof) (checkpointRecoveryFilesystemOutcome, error) {
				return test.filesystem, nil
			}

			got, err := fixture.service.Recover(context.Background(), request.Scope)
			if err != nil || got.Binding.Scope != request.Scope {
				t.Fatalf("Recover()=(%+v,%v)", got, err)
			}
			afterRevision := workspaceRevisionForProjectStateTest(t, fixture.service, request.Scope)
			if afterRevision != beforeRevision+1 {
				t.Fatalf("%s recovery workspace revision=%d, want %d", test.name, afterRevision, beforeRevision+1)
			}
			disposition := readCheckpointDisposition(t, fixture.service, request.Scope)
			if test.wantJournalState == "recovered_old" && len(disposition.Journals) != 0 {
				t.Fatalf("%s recovery disposition=%+v", test.name, disposition)
			}
			if test.wantJournalState != "recovered_old" &&
				(len(disposition.Journals) != 1 || disposition.Journals[0].State != test.wantJournalState) {
				t.Fatalf("%s recovery disposition=%+v", test.name, disposition)
			}
			recoveryAssertReturnedStatus(t, fixture.service, request.Scope, got)
		})
	}
}

func TestRecoverUnknownCommitConfirmationMatrix(t *testing.T) {
	tests := []struct {
		name       string
		driver     string
		filesystem checkpointRecoveryFilesystemOutcome
		match      localstore.WorkspaceCommitMatch
		confirmErr error
		wantOK     bool
		want       error
	}{
		{name: "prepared recovered old next", driver: "prepared", filesystem: checkpointRecoveryFilesystemRecoveredOld, match: localstore.WorkspaceCommitNext, wantOK: true},
		{name: "prepared recovered new next", driver: "prepared", filesystem: checkpointRecoveryFilesystemRecoveredNew, match: localstore.WorkspaceCommitNext, wantOK: true},
		{name: "published recovered new next", driver: "published", filesystem: checkpointRecoveryFilesystemRecoveredNew, match: localstore.WorkspaceCommitNext, wantOK: true},
		{name: "prepared prior", driver: "prepared", filesystem: checkpointRecoveryFilesystemRecoveredOld, match: localstore.WorkspaceCommitPrior, want: localstore.ErrCommitOutcomeUnknown},
		{name: "published prior", driver: "published", filesystem: checkpointRecoveryFilesystemRecoveredNew, match: localstore.WorkspaceCommitPrior, want: localstore.ErrCommitOutcomeUnknown},
		{name: "third", driver: "prepared", filesystem: checkpointRecoveryFilesystemRecoveredOld, match: localstore.WorkspaceCommitThird, want: ErrCheckpointRecoveryBlocked},
		{name: "invalid", driver: "prepared", filesystem: checkpointRecoveryFilesystemRecoveredOld, match: localstore.WorkspaceCommitMatch(99), want: ErrCheckpointRecoveryBlocked},
		{name: "read failure", driver: "prepared", filesystem: checkpointRecoveryFilesystemRecoveredOld, confirmErr: errors.New("recovery confirmation read failed"), want: ErrCheckpointRecoveryBlocked},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, request := recoveryDriverPlanFixture(t, test.driver)
			before := recoveryDatabaseState(t, fixture.service, request.Scope)
			acceptedBefore := before.workspace.Binding
			fixture.service.observeCheckpointRecoveryGit = func(_ context.Context, proof checkpointRecoveryProof) (checkpointRecoveryGitObservation, error) {
				return recoveryPlannerObservation(t, proof, test.driver), nil
			}
			filesystemCalls := 0
			fixture.service.recoverCheckpointFilesystem = func(context.Context, checkpointRecoveryProof) (checkpointRecoveryFilesystemOutcome, error) {
				filesystemCalls++
				return test.filesystem, nil
			}
			realWithImmediate := fixture.service.repo.WithImmediateWorkspace
			transactionCalls := 0
			unknown := fmt.Errorf("synthetic recovery final commit: %w", localstore.ErrCommitOutcomeUnknown)
			fixture.service.withImmediateWorkspace = func(
				ctx context.Context,
				scope types.WorkspaceScope,
				fn func(*localstore.WorkspaceMutationTx) error,
			) error {
				transactionCalls++
				err := realWithImmediate(ctx, scope, fn)
				if err == nil {
					return unknown
				}
				return err
			}
			confirmCalls := 0
			fixture.service.confirmWorkspaceCommit = func(
				context.Context,
				localstore.WorkspaceCommitConfirmation,
				localstore.WorkspaceCommitConfirmation,
			) (localstore.WorkspaceCommitMatch, error) {
				confirmCalls++
				return test.match, test.confirmErr
			}

			got, err := fixture.service.Recover(context.Background(), request.Scope)
			if test.wantOK {
				if err != nil || got.Binding.Scope != request.Scope {
					t.Fatalf("confirmed recovery next = (%+v,%v)", got, err)
				}
			} else if !reflect.DeepEqual(got, WorkspaceStatus{}) || !errors.Is(err, test.want) || !errors.Is(err, unknown) {
				t.Fatalf("confirmed recovery uncertainty = (%+v,%v), want zero, %v, and unknown cause", got, err, test.want)
			}
			if test.confirmErr != nil && !errors.Is(err, test.confirmErr) {
				t.Fatalf("confirmed recovery error = %v, want read cause %v", err, test.confirmErr)
			}
			if transactionCalls != 1 || filesystemCalls != 1 || confirmCalls != 1 {
				t.Fatalf("recovery uncertainty calls: transactions=%d filesystem=%d confirm=%d", transactionCalls, filesystemCalls, confirmCalls)
			}
			after := recoveryDatabaseState(t, fixture.service, request.Scope)
			if after.workspace.Binding != acceptedBefore {
				t.Fatalf("recovery uncertainty moved accepted binding\nbefore=%+v\nafter=%+v", acceptedBefore, after.workspace.Binding)
			}
			if test.wantOK {
				recoveryAssertReturnedStatus(t, fixture.service, request.Scope, got)
			}
		})
	}
}

type checkpointRecoveryDatabaseState struct {
	workspace   localstore.WorkspaceRecord
	candidate   *localstore.WorkspaceCandidateRecord
	disposition localstore.WorkspaceCurrentMaterialization
}

func recoveryDatabaseState(t *testing.T, service *Service, scope types.WorkspaceScope) checkpointRecoveryDatabaseState {
	t.Helper()
	var result checkpointRecoveryDatabaseState
	if err := service.repo.WithImmediateWorkspace(context.Background(), scope, func(tx *localstore.WorkspaceMutationTx) error {
		var err error
		result.workspace, err = tx.Workspace(context.Background())
		if err != nil {
			return err
		}
		result.candidate, err = tx.Candidate(context.Background())
		if err != nil {
			return err
		}
		result.disposition, err = readCurrentMaterializationWorkset(context.Background(), tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func recoveryComposedStatus(t *testing.T, service *Service, scope types.WorkspaceScope) WorkspaceStatus {
	t.Helper()
	var result WorkspaceStatus
	if err := service.repo.WithImmediateWorkspace(context.Background(), scope, func(tx *localstore.WorkspaceMutationTx) error {
		workspace, err := tx.Workspace(context.Background())
		if err != nil {
			return err
		}
		composed, err := loadComposedWorkspaceRecord(context.Background(), tx, workspace)
		if err != nil {
			return err
		}
		result, err = clonePublicationReviewStatus(composed.status)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	result.PublicationClassification = ""
	result.PublicationReviewDigest = ""
	return result
}

func recoveryAssertReturnedStatus(
	t *testing.T,
	service *Service,
	scope types.WorkspaceScope,
	got WorkspaceStatus,
) {
	t.Helper()
	want := recoveryComposedStatus(t, service, scope)
	if !reflect.DeepEqual(got, want) || got.PublicationClassification != "" || got.PublicationReviewDigest != "" {
		t.Fatalf("Recover status=%+v, want independently composed %+v with zero review fields", got, want)
	}
	if got.AcceptedSnapshot.Project.Extensions == nil {
		t.Fatal("Recover accepted snapshot extensions are nil")
	}
	got.AcceptedSnapshot.Project.Extensions["com.wormhole.recovery-result-mutation"] = state.ExtensionV1{}
	fresh := recoveryComposedStatus(t, service, scope)
	if !reflect.DeepEqual(fresh, want) {
		t.Fatalf("mutating Recover result changed durable status\nwant=%+v\nfresh=%+v", want, fresh)
	}
}

func recoveryAssertCrossScopeRejected(
	t *testing.T,
	service *Service,
	driverScope, neighborScope, requested types.WorkspaceScope,
) {
	t.Helper()
	driverBefore := recoveryDatabaseState(t, service, driverScope)
	neighborBefore := recoveryDatabaseState(t, service, neighborScope)
	driverPathsBefore := recoveryScopePaths(t, driverBefore)
	neighborPathsBefore := recoveryScopePaths(t, neighborBefore)
	service.observeCheckpointRecoveryGit = func(context.Context, checkpointRecoveryProof) (checkpointRecoveryGitObservation, error) {
		panic("cross-scope recovery observed Git")
	}
	service.recoverCheckpointFilesystem = func(context.Context, checkpointRecoveryProof) (checkpointRecoveryFilesystemOutcome, error) {
		panic("cross-scope recovery touched paths")
	}
	got, err := service.Recover(context.Background(), requested)
	if !reflect.DeepEqual(got, WorkspaceStatus{}) || !errors.Is(err, localstore.ErrNotFound) {
		t.Fatalf("cross-scope Recover=(%+v,%v)", got, err)
	}
	if after := recoveryDatabaseState(t, service, driverScope); !reflect.DeepEqual(after, driverBefore) {
		t.Fatal("cross-scope recovery changed driver state")
	}
	if after := recoveryDatabaseState(t, service, neighborScope); !reflect.DeepEqual(after, neighborBefore) {
		t.Fatal("cross-scope recovery changed neighbor state")
	}
	if after := recoveryScopePaths(t, driverBefore); !reflect.DeepEqual(after, driverPathsBefore) {
		t.Fatal("cross-scope recovery changed driver paths")
	}
	if after := recoveryScopePaths(t, neighborBefore); !reflect.DeepEqual(after, neighborPathsBefore) {
		t.Fatal("cross-scope recovery changed neighbor paths")
	}
}

func recoveryEnsureDriverArtifactEvidence(t *testing.T, service *Service, scope types.WorkspaceScope) {
	t.Helper()
	database := recoveryDatabaseState(t, service, scope)
	if len(database.disposition.Journals) != 1 {
		t.Fatalf("driver journal count=%d, want 1", len(database.disposition.Journals))
	}
	journal := database.disposition.Journals[0]
	for _, evidence := range []struct {
		path string
		tree state.Tree
	}{
		{path: journal.StagePath, tree: journal.CandidateTree},
		{path: journal.BackupPath, tree: journal.PriorTree},
	} {
		if _, err := os.Lstat(evidence.path); errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(evidence.path, 0o700); err != nil {
				t.Fatal(err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
		for _, file := range evidence.tree {
			path := filepath.Join(evidence.path, filepath.FromSlash(file.Path))
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, file.Data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

type checkpointRecoveryPathState struct {
	live    state.Tree
	stages  map[string]checkpointRecoveryOptionalTree
	backups map[string]checkpointRecoveryOptionalTree
}

type checkpointRecoveryOptionalTree struct {
	exists bool
	tree   state.Tree
}

func recoveryScopePaths(t *testing.T, database checkpointRecoveryDatabaseState) checkpointRecoveryPathState {
	t.Helper()
	live, err := ReadWorkingTreeNoFollow(database.workspace.Binding.Checkout.CanonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	result := checkpointRecoveryPathState{
		live: live, stages: make(map[string]checkpointRecoveryOptionalTree),
		backups: make(map[string]checkpointRecoveryOptionalTree),
	}
	capture := func(path string) checkpointRecoveryOptionalTree {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return checkpointRecoveryOptionalTree{}
		} else if err != nil {
			t.Fatal(err)
		}
		return checkpointRecoveryOptionalTree{exists: true, tree: recoveryReadEvidenceTree(t, path)}
	}
	for _, journal := range database.disposition.Journals {
		result.stages[journal.StagePath] = capture(journal.StagePath)
		result.backups[journal.BackupPath] = capture(journal.BackupPath)
	}
	return result
}

func recoveryReadEvidenceTree(t *testing.T, root string) state.Tree {
	t.Helper()
	result := state.Tree{}
	if err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return errors.New("recovery evidence contains a non-regular file")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result = append(result, state.File{Path: filepath.ToSlash(relative), Data: data})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}
