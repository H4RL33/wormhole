package projectstate

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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
	input, workspace, priorCandidate, prepared, operations := checkpointRecoveryProofFixtureWithInput(t)
	acceptedOperation := func(generation int64, operationID string) (localstore.WorkspaceMaterializationRecord, localstore.WorkspaceOperation) {
		operation, _ := checkpointPlanProjectOperation(t, workspace.Snapshot, operationID, "accepted recovery history")
		row := checkpointPlanOperationRow(t, generation, "materialized", operation)
		journal := checkpointPlanAcceptedHistory(t, input, row)
		journal.JournalID = "10000000-0000-4000-8000-000000000001"
		return journal, row
	}
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
		{
			name: "malformed terminal sibling", candidate: func() *localstore.WorkspaceCandidateRecord { return priorCandidate },
			dispose: func() localstore.WorkspaceMaterializationDisposition {
				terminal := checkpointPlanHistoricalInput(t, input, "accepted").Disposition.Journals[0]
				terminal.JournalID = "10000000-0000-4000-8000-000000000001"
				bad := "{}\n"
				terminal.PublicationReviewJSON = &bad
				return localstore.WorkspaceMaterializationDisposition{
					Journals: []localstore.WorkspaceMaterializationRecord{terminal, prepared}, Operations: operations,
				}
			},
		},
		{
			name: "driver terminal generation overlap",
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
				driver := cloneMaterializationRecord(prepared)
				driver.State = "published"
				terminal := cloneMaterializationRecord(driver)
				terminal.JournalID = "10000000-0000-4000-8000-000000000001"
				terminal.StagePath = filepath.Join(filepath.Dir(terminal.StagePath), terminal.JournalID+".stage")
				terminal.BackupPath = filepath.Join(filepath.Dir(terminal.BackupPath), terminal.JournalID+".backup")
				terminal.State = "accepted"
				row := cloneImportOperation(operations[0])
				row.State = "materialized"
				return localstore.WorkspaceMaterializationDisposition{
					Journals: []localstore.WorkspaceMaterializationRecord{terminal, driver}, Operations: []localstore.WorkspaceOperation{row},
				}
			},
		},
		{
			name: "driver terminal ID overlap", candidate: func() *localstore.WorkspaceCandidateRecord { return priorCandidate },
			dispose: func() localstore.WorkspaceMaterializationDisposition {
				terminal, terminalRow := acceptedOperation(operations[0].Generation+1, operations[0].OperationID)
				return localstore.WorkspaceMaterializationDisposition{
					Journals:   []localstore.WorkspaceMaterializationRecord{terminal, prepared},
					Operations: []localstore.WorkspaceOperation{cloneImportOperation(operations[0]), terminalRow},
				}
			},
		},
		{
			name: "operation JSON mismatch", candidate: func() *localstore.WorkspaceCandidateRecord { return priorCandidate },
			dispose: func() localstore.WorkspaceMaterializationDisposition {
				row := cloneImportOperation(operations[0])
				changed, _ := checkpointPlanProjectOperation(t, priorCandidate.DirectSnapshot, row.OperationID, "different row operation")
				row.OperationJSON = checkpointPlanOperationRow(t, row.Generation, row.State, changed).OperationJSON
				return localstore.WorkspaceMaterializationDisposition{
					Journals: []localstore.WorkspaceMaterializationRecord{prepared}, Operations: []localstore.WorkspaceOperation{row},
				}
			},
		},
		{
			name: "operation ID mismatch", candidate: func() *localstore.WorkspaceCandidateRecord { return priorCandidate },
			dispose: func() localstore.WorkspaceMaterializationDisposition {
				changed, _ := checkpointPlanProjectOperation(t, priorCandidate.DirectSnapshot, "90000000-0000-4000-8000-000000000021", "different row ID")
				row := checkpointPlanOperationRow(t, operations[0].Generation, operations[0].State, changed)
				return localstore.WorkspaceMaterializationDisposition{
					Journals: []localstore.WorkspaceMaterializationRecord{prepared}, Operations: []localstore.WorkspaceOperation{row},
				}
			},
		},
		{
			name: "operation stash owner", candidate: func() *localstore.WorkspaceCandidateRecord { return priorCandidate },
			dispose: func() localstore.WorkspaceMaterializationDisposition {
				row := cloneImportOperation(operations[0])
				stashID := "80000000-0000-4000-8000-000000000001"
				row.StashedByStashID = &stashID
				return localstore.WorkspaceMaterializationDisposition{
					Journals: []localstore.WorkspaceMaterializationRecord{prepared}, Operations: []localstore.WorkspaceOperation{row},
				}
			},
		},
		{
			name: "prepared active versus rebased state mismatch", candidate: func() *localstore.WorkspaceCandidateRecord { return priorCandidate },
			dispose: func() localstore.WorkspaceMaterializationDisposition {
				row := cloneImportOperation(operations[0])
				row.State = "rebased"
				return localstore.WorkspaceMaterializationDisposition{
					Journals: []localstore.WorkspaceMaterializationRecord{prepared}, Operations: []localstore.WorkspaceOperation{row},
				}
			},
		},
		{
			name: "non-nil prior candidate drift",
			candidate: func() *localstore.WorkspaceCandidateRecord {
				candidate, err := cloneImportCandidate(priorCandidate)
				if err != nil {
					t.Fatal(err)
				}
				candidate.ImportedBy = "70000000-0000-4000-8000-000000000099"
				return candidate
			},
			dispose: func() localstore.WorkspaceMaterializationDisposition {
				return localstore.WorkspaceMaterializationDisposition{Journals: []localstore.WorkspaceMaterializationRecord{prepared}, Operations: operations}
			},
		},
		{
			name: "publication postimage candidate drift",
			candidate: func() *localstore.WorkspaceCandidateRecord {
				journal := cloneMaterializationRecord(prepared)
				journal.State = "published"
				candidate, err := checkpointPublicationPostimage(journal)
				if err != nil {
					t.Fatal(err)
				}
				candidate.ImportedBy = "70000000-0000-4000-8000-000000000099"
				return &candidate
			},
			dispose: func() localstore.WorkspaceMaterializationDisposition {
				journal := cloneMaterializationRecord(prepared)
				journal.State = "published"
				rows := cloneImportDisposition(localstore.WorkspaceMaterializationDisposition{Operations: operations}).Operations
				for index := range rows {
					rows[index].State = "materialized"
				}
				return localstore.WorkspaceMaterializationDisposition{Journals: []localstore.WorkspaceMaterializationRecord{journal}, Operations: rows}
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

	t.Run("candidate exactness", func(t *testing.T) {
		input := checkpointPlanRebasedInput(t, checkpointPlanFixture(t), 0)
		candidateWorkspace, prior, candidatePrepared, candidateOperations := checkpointRecoveryProofFixtureForInput(t, input)
		candidatePublished := cloneMaterializationRecord(candidatePrepared)
		candidatePublished.State = "published"
		postimage, err := checkpointPublicationPostimage(candidatePublished)
		if err != nil {
			t.Fatal(err)
		}
		materialized := cloneImportDisposition(localstore.WorkspaceMaterializationDisposition{Operations: candidateOperations}).Operations
		for index := range materialized {
			materialized[index].State = "materialized"
		}

		mutations := []struct {
			name   string
			mutate func(*localstore.WorkspaceCandidateRecord)
		}{
			{name: "accepted base digest", mutate: func(candidate *localstore.WorkspaceCandidateRecord) {
				candidate.AcceptedBaseDigest = publicationRepeatedDigest('f')
			}},
			{name: "working tree digest", mutate: func(candidate *localstore.WorkspaceCandidateRecord) {
				candidate.WorkingTreeDigest = publicationRepeatedDigest('e')
			}},
			{name: "direct canonical tree", mutate: func(candidate *localstore.WorkspaceCandidateRecord) {
				candidate.DirectSnapshot = checkpointPlanMutatedSnapshot(t, candidate.DirectSnapshot, "recovery direct candidate drift")
			}},
			{name: "rebased tree nilness", mutate: func(candidate *localstore.WorkspaceCandidateRecord) {
				candidate.RebasedSnapshot = nil
			}},
			{name: "rebased tree content", mutate: func(candidate *localstore.WorkspaceCandidateRecord) {
				if candidate.RebasedSnapshot == nil {
					t.Fatal("candidate exactness fixture lacks rebased snapshot")
				}
				changed := checkpointPlanMutatedSnapshot(t, *candidate.RebasedSnapshot, "recovery rebased candidate drift")
				candidate.RebasedSnapshot = &changed
			}},
			{name: "rebased boundary", mutate: func(candidate *localstore.WorkspaceCandidateRecord) {
				candidate.RebasedThroughGeneration++
			}},
			{name: "import origin", mutate: func(candidate *localstore.WorkspaceCandidateRecord) {
				candidate.ImportedBy = "70000000-0000-4000-8000-000000000099"
			}},
			{name: "import timestamp", mutate: func(candidate *localstore.WorkspaceCandidateRecord) {
				candidate.ImportedAt = candidate.ImportedAt.Add(time.Second)
			}},
		}
		drivers := []struct {
			name        string
			kind        checkpointRecoveryKind
			candidate   *localstore.WorkspaceCandidateRecord
			disposition localstore.WorkspaceMaterializationDisposition
		}{
			{
				name: "prepared", kind: checkpointRecoveryPrepared, candidate: prior,
				disposition: localstore.WorkspaceMaterializationDisposition{
					Journals: []localstore.WorkspaceMaterializationRecord{candidatePrepared}, Operations: candidateOperations,
				},
			},
			{
				name: "published", kind: checkpointRecoveryPublished, candidate: &postimage,
				disposition: localstore.WorkspaceMaterializationDisposition{
					Journals: []localstore.WorkspaceMaterializationRecord{candidatePublished}, Operations: materialized,
				},
			},
		}
		for _, driver := range drivers {
			t.Run(driver.name, func(t *testing.T) {
				baseline, err := proveCheckpointRecoveryDisposition(candidateWorkspace, driver.candidate, driver.disposition)
				if err != nil || baseline.kind != driver.kind || baseline.driver == nil {
					t.Fatalf("valid %s candidate baseline=(%+v,%v)", driver.name, baseline, err)
				}
				for _, mutation := range mutations {
					t.Run(mutation.name, func(t *testing.T) {
						candidate, err := cloneImportCandidate(driver.candidate)
						if err != nil {
							t.Fatal(err)
						}
						mutation.mutate(candidate)
						proof, err := proveCheckpointRecoveryDisposition(candidateWorkspace, candidate, driver.disposition)
						if err == nil || !errors.Is(err, ErrCheckpointRecoveryPrecondition) ||
							!reflect.DeepEqual(proof, checkpointRecoveryProof{}) {
							t.Fatalf("%s candidate drift=(kind=%d, zero=%t, err=%v), want exact zero precondition error",
								mutation.name, proof.kind, reflect.DeepEqual(proof, checkpointRecoveryProof{}), err)
						}
					})
				}
			})
		}
	})
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

	type observationEdits struct {
		initial   func(*gitBasePosition)
		committed func(*committedWorkspace)
		final     func(*gitBasePosition)
		origin    func(*publicationOriginObservation)
		originErr error
	}
	run := func(t *testing.T, commit string, tree state.Tree, edits observationEdits) (checkpointRecoveryGitObservation, error, []string) {
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

	stored, err, calls := run(t, workspace.Binding.AcceptedCommitSHA, acceptedTree, observationEdits{})
	if err != nil || !reflect.DeepEqual(calls, []string{"position", "committed", "origin", "position"}) ||
		stored.position.commit != workspace.Binding.AcceptedCommitSHA || !equalCheckpointTree(stored.committed.tree, acceptedTree) {
		t.Fatalf("stored observation=(%+v,%v), calls=%v", stored, err, calls)
	}

	candidateCommit := strings.Repeat("b", 40)
	candidate, err, calls := run(t, candidateCommit, journal.CandidateTree, observationEdits{})
	if err != nil || !reflect.DeepEqual(calls, []string{"position", "committed", "origin", "position"}) ||
		candidate.position.commit != candidateCommit || !equalCheckpointTree(candidate.committed.tree, journal.CandidateTree) {
		t.Fatalf("same-ref candidate observation=(%+v,%v), calls=%v", candidate, err, calls)
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
			observed, err, calls := run(t, test.commit, test.tree, test.edits)
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
		observed, err := observeCheckpointRecoveryGitWithReaders(context.Background(), proof, checkpointRecoveryGitReaders{
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
	return workspace, candidate, journal, cloneImportDisposition(input.Disposition).Operations
}
