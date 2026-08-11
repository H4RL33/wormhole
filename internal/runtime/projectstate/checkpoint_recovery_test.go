package projectstate

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestProveCheckpointRecoveryDispositionOwnsPreparedAndPublishedState(t *testing.T) {
	workspace, priorCandidate, prepared, operations := checkpointRecoveryProofFixture(t)

	t.Run("prepared", func(t *testing.T) {
		workspaceInput, err := cloneImportWorkspace(workspace)
		if err != nil {
			t.Fatal(err)
		}
		disposition := localstore.WorkspaceMaterializationDisposition{
			Journals:   []localstore.WorkspaceMaterializationRecord{cloneMaterializationRecord(prepared)},
			Operations: cloneImportDisposition(localstore.WorkspaceMaterializationDisposition{Operations: operations}).Operations,
		}
		candidate, err := cloneImportCandidate(priorCandidate)
		if err != nil {
			t.Fatal(err)
		}
		proof, err := proveCheckpointRecoveryDisposition(workspaceInput, candidate, disposition)
		if err != nil {
			t.Fatal(err)
		}
		if proof.kind != checkpointRecoveryPrepared || proof.driver == nil || proof.driver.State != "prepared" ||
			len(proof.operations.Operations) != len(operations) || !reflect.DeepEqual(proof.status, WorkspaceStatus{}) {
			t.Fatalf("prepared proof=%+v", proof)
		}

		wantDriverByte := proof.driver.CandidateTree[0].Data[0]
		wantCandidateName := proof.candidate.DirectSnapshot.Project.Name
		workspaceInput.Snapshot.Project.Name = "mutated workspace"
		candidate.DirectSnapshot.Project.Name = "mutated candidate"
		disposition.Journals[0].CandidateTree[0].Data[0] ^= 0xff
		disposition.Operations[0].OperationJSON[0] ^= 0xff
		proof.disposition.Journals[0].CandidateTree[0].Data[0] ^= 0xff
		if proof.workspace.Snapshot.Project.Name == workspaceInput.Snapshot.Project.Name ||
			proof.candidate.DirectSnapshot.Project.Name != wantCandidateName ||
			proof.driver.CandidateTree[0].Data[0] != wantDriverByte ||
			bytes.Equal(proof.disposition.Operations[0].OperationJSON, disposition.Operations[0].OperationJSON) {
			t.Fatal("prepared proof retained mutable input or internal aliases")
		}
	})

	for _, journalState := range []string{"published", "recovered_new"} {
		t.Run(journalState, func(t *testing.T) {
			journal := cloneMaterializationRecord(prepared)
			journal.State = journalState
			publishedCandidate, err := checkpointPublicationPostimage(journal)
			if err != nil {
				t.Fatal(err)
			}
			materialized := cloneImportDisposition(localstore.WorkspaceMaterializationDisposition{Operations: operations}).Operations
			for index := range materialized {
				materialized[index].State = "materialized"
			}
			proof, err := proveCheckpointRecoveryDisposition(workspace, &publishedCandidate, localstore.WorkspaceMaterializationDisposition{
				Journals: []localstore.WorkspaceMaterializationRecord{journal}, Operations: materialized,
			})
			if err != nil {
				t.Fatal(err)
			}
			wantKind := checkpointRecoveryPublished
			if journalState == "recovered_new" {
				wantKind = checkpointRecoveryNoWork
			}
			if proof.kind != wantKind || (journalState == "published") != (proof.driver != nil) ||
				len(proof.operations.Operations) != len(operations) {
				t.Fatalf("%s proof=%+v", journalState, proof)
			}
			if journalState == "recovered_new" && !reflect.DeepEqual(proof.status, WorkspaceStatus{}) {
				t.Fatalf("pure recovered-new proof unexpectedly composed status: %+v", proof.status)
			}
		})
	}

	t.Run("terminal accepted", func(t *testing.T) {
		accepted := cloneMaterializationRecord(prepared)
		accepted.State = "accepted"
		materialized := cloneImportDisposition(localstore.WorkspaceMaterializationDisposition{Operations: operations}).Operations
		for index := range materialized {
			materialized[index].State = "materialized"
		}
		proof, err := proveCheckpointRecoveryDisposition(workspace, nil, localstore.WorkspaceMaterializationDisposition{
			Journals: []localstore.WorkspaceMaterializationRecord{accepted}, Operations: materialized,
		})
		if err != nil || proof.kind != checkpointRecoveryNoWork || proof.driver != nil {
			t.Fatalf("terminal accepted proof=(%+v,%v)", proof, err)
		}
	})
}

func TestProveCheckpointRecoveryDispositionRejectsMixedOrOrphanState(t *testing.T) {
	workspace, priorCandidate, prepared, operations := checkpointRecoveryProofFixture(t)
	tests := []struct {
		name      string
		candidate func() *localstore.WorkspaceCandidateRecord
		dispose   func() localstore.WorkspaceMaterializationDisposition
	}{
		{
			name: "mixed drivers", candidate: func() *localstore.WorkspaceCandidateRecord { return priorCandidate },
			dispose: func() localstore.WorkspaceMaterializationDisposition {
				first := cloneMaterializationRecord(prepared)
				second := cloneMaterializationRecord(prepared)
				second.JournalID = "40000000-0000-4000-8000-000000000001"
				second.StagePath = filepath.Join(filepath.Dir(second.StagePath), second.JournalID+".stage")
				second.BackupPath = filepath.Join(filepath.Dir(second.BackupPath), second.JournalID+".backup")
				return localstore.WorkspaceMaterializationDisposition{Journals: []localstore.WorkspaceMaterializationRecord{first, second}, Operations: operations}
			},
		},
		{
			name: "prepared candidate absent", candidate: func() *localstore.WorkspaceCandidateRecord { return nil },
			dispose: func() localstore.WorkspaceMaterializationDisposition {
				return localstore.WorkspaceMaterializationDisposition{Journals: []localstore.WorkspaceMaterializationRecord{prepared}, Operations: operations}
			},
		},
		{
			name: "prepared operation materialized", candidate: func() *localstore.WorkspaceCandidateRecord { return priorCandidate },
			dispose: func() localstore.WorkspaceMaterializationDisposition {
				rows := cloneImportDisposition(localstore.WorkspaceMaterializationDisposition{Operations: operations}).Operations
				rows[0].State = "materialized"
				return localstore.WorkspaceMaterializationDisposition{Journals: []localstore.WorkspaceMaterializationRecord{prepared}, Operations: rows}
			},
		},
		{
			name: "orphan materialized row", candidate: func() *localstore.WorkspaceCandidateRecord { return nil },
			dispose: func() localstore.WorkspaceMaterializationDisposition {
				rows := cloneImportDisposition(localstore.WorkspaceMaterializationDisposition{Operations: operations}).Operations
				rows[0].State = "materialized"
				return localstore.WorkspaceMaterializationDisposition{Journals: []localstore.WorkspaceMaterializationRecord{}, Operations: rows}
			},
		},
		{
			name: "published operation not materialized",
			candidate: func() *localstore.WorkspaceCandidateRecord {
				journal := cloneMaterializationRecord(prepared)
				journal.State = "published"
				candidate, err := checkpointPublicationPostimage(journal)
				if err != nil {
					t.Fatal(err)
				}
				return &candidate
			},
			dispose: func() localstore.WorkspaceMaterializationDisposition {
				journal := cloneMaterializationRecord(prepared)
				journal.State = "published"
				return localstore.WorkspaceMaterializationDisposition{Journals: []localstore.WorkspaceMaterializationRecord{journal}, Operations: operations}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			proof, err := proveCheckpointRecoveryDisposition(workspace, test.candidate(), test.dispose())
			if err == nil || !errors.Is(err, ErrCheckpointRecoveryPrecondition) ||
				!reflect.DeepEqual(proof, checkpointRecoveryProof{}) {
				t.Fatalf("invalid recovery disposition=(%+v,%v), want zero precondition error", proof, err)
			}
		})
	}
}

func TestObserveCheckpointRecoveryGitAllowsOnlyStoredOrSameRefCandidate(t *testing.T) {
	workspace, _, prepared, operations := checkpointRecoveryProofFixture(t)
	journal := cloneMaterializationRecord(prepared)
	journal.State = "published"
	publishedCandidate, err := checkpointPublicationPostimage(journal)
	if err != nil {
		t.Fatal(err)
	}
	materialized := cloneImportDisposition(localstore.WorkspaceMaterializationDisposition{Operations: operations}).Operations
	for index := range materialized {
		materialized[index].State = "materialized"
	}
	proof, err := proveCheckpointRecoveryDisposition(workspace, &publishedCandidate, localstore.WorkspaceMaterializationDisposition{
		Journals: []localstore.WorkspaceMaterializationRecord{journal}, Operations: materialized,
	})
	if err != nil {
		t.Fatal(err)
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

	run := func(t *testing.T, commit string, tree state.Tree, editFinal func(*gitBasePosition), editOrigin func(*publicationOriginObservation)) (checkpointRecoveryGitObservation, error, []string) {
		t.Helper()
		position := gitBasePosition{
			root: workspace.Binding.Checkout.CanonicalPath, checkout: workspace.Binding.Checkout,
			acceptedRef: workspace.Binding.AcceptedRef, commit: commit,
		}
		snapshot, decodeErr := state.DecodeTree(cloneCheckpointTree(tree))
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		committed := committedWorkspace{
			root: position.root, checkout: position.checkout, acceptedRef: position.acceptedRef,
			commit: position.commit, tree: cloneCheckpointTree(tree), snapshot: snapshot,
		}
		var calls []string
		positionCalls := 0
		readers := checkpointRecoveryGitReaders{
			position: func(context.Context, string) (gitBasePosition, error) {
				calls = append(calls, "position")
				positionCalls++
				got := position
				if positionCalls == 2 && editFinal != nil {
					editFinal(&got)
				}
				return got, nil
			},
			committed: func(context.Context, string, string) (committedWorkspace, error) {
				calls = append(calls, "committed")
				return committed, nil
			},
			origin: func(context.Context, string) (publicationOriginObservation, error) {
				calls = append(calls, "origin")
				got := origin
				if editOrigin != nil {
					editOrigin(&got)
				}
				return got, nil
			},
		}
		observed, observeErr := observeCheckpointRecoveryGitWithReaders(context.Background(), proof, readers)
		committed.tree[0].Data[0] ^= 0xff
		return observed, observeErr, calls
	}

	stored, err, calls := run(t, workspace.Binding.AcceptedCommitSHA, acceptedTree, nil, nil)
	if err != nil || !reflect.DeepEqual(calls, []string{"position", "committed", "origin", "position"}) ||
		stored.position.commit != workspace.Binding.AcceptedCommitSHA || !equalCheckpointTree(stored.committed.tree, acceptedTree) {
		t.Fatalf("stored observation=(%+v,%v), calls=%v", stored, err, calls)
	}

	candidateCommit := strings.Repeat("b", 40)
	candidate, err, calls := run(t, candidateCommit, journal.CandidateTree, nil, nil)
	if err != nil || !reflect.DeepEqual(calls, []string{"position", "committed", "origin", "position"}) ||
		candidate.position.commit != candidateCommit || !equalCheckpointTree(candidate.committed.tree, journal.CandidateTree) {
		t.Fatalf("same-ref candidate observation=(%+v,%v), calls=%v", candidate, err, calls)
	}

	for _, test := range []struct {
		name       string
		commit     string
		tree       state.Tree
		editFinal  func(*gitBasePosition)
		editOrigin func(*publicationOriginObservation)
	}{
		{name: "different commit wrong tree", commit: candidateCommit, tree: acceptedTree},
		{name: "stored commit candidate tree", commit: workspace.Binding.AcceptedCommitSHA, tree: journal.CandidateTree},
		{name: "position race", commit: candidateCommit, tree: journal.CandidateTree, editFinal: func(value *gitBasePosition) { value.commit = strings.Repeat("c", 40) }},
		{name: "malformed origin", commit: candidateCommit, tree: journal.CandidateTree, editOrigin: func(value *publicationOriginObservation) {
			value.digest = "sha256:" + state.Digest(strings.Repeat("f", 64))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			observed, err, calls := run(t, test.commit, test.tree, test.editFinal, test.editOrigin)
			if err == nil || !errors.Is(err, ErrCheckpointRecoveryPrecondition) ||
				!reflect.DeepEqual(observed, checkpointRecoveryGitObservation{}) {
				t.Fatalf("rejected observation=(%+v,%v), calls=%v", observed, err, calls)
			}
			if (test.editFinal != nil || test.editOrigin != nil) && !reflect.DeepEqual(calls, []string{"position", "committed", "origin", "position"}) {
				t.Fatalf("race/origin calls=%v, want complete ordered bundle", calls)
			}
		})
	}

	t.Run("changed ref", func(t *testing.T) {
		calls := 0
		observed, err := observeCheckpointRecoveryGitWithReaders(context.Background(), proof, checkpointRecoveryGitReaders{
			position: func(context.Context, string) (gitBasePosition, error) {
				calls++
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
	input := checkpointPlanActiveInput(t, checkpointPlanDirectInput(t, checkpointPlanFixture(t)))
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
	return workspace, candidate, journal, cloneImportDisposition(input.Disposition).Operations
}
