package projectstate

import (
	"bytes"
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

	t.Run("valid preimage and operation envelopes", func(t *testing.T) {
		fixtures := []struct {
			name          string
			input         checkpointPlanInput
			wantPriorNil  bool
			wantRowStates []string
		}{
			{
				name: "nil prior candidate", input: checkpointPlanActiveInput(t, checkpointPlanFixture(t)),
				wantPriorNil: true, wantRowStates: []string{"active"},
			},
			{
				name: "mixed rebased and active", input: checkpointRecoveryMixedOperationInput(t),
				wantRowStates: []string{"rebased", "active"},
			},
		}
		for _, fixture := range fixtures {
			t.Run(fixture.name, func(t *testing.T) {
				fixtureWorkspace, fixtureCandidate, fixturePrepared, fixtureRows := checkpointRecoveryProofFixtureForInput(t, fixture.input)
				prior, err := decodeCheckpointPriorCandidate(*fixturePrepared.PriorCandidateJSON)
				if err != nil || (prior.Candidate == nil) != fixture.wantPriorNil {
					t.Fatalf("%s prior preimage=(%+v,%v), want nil=%t", fixture.name, prior, err, fixture.wantPriorNil)
				}
				envelope, err := decodeCheckpointOperations(*fixturePrepared.IncludedOperationsJSON)
				if err != nil || len(envelope.Operations) != len(fixture.wantRowStates) {
					t.Fatalf("%s operation envelope=(%+v,%v)", fixture.name, envelope, err)
				}
				for index, wantState := range fixture.wantRowStates {
					if envelope.Operations[index].PrepublicationState != wantState || fixtureRows[index].State != wantState {
						t.Fatalf("%s row %d states=(%q,%q), want %q", fixture.name, index,
							envelope.Operations[index].PrepublicationState, fixtureRows[index].State, wantState)
					}
				}

				for _, journalState := range []string{"prepared", "published", "recovered_new"} {
					t.Run(journalState, func(t *testing.T) {
						journal := cloneMaterializationRecord(fixturePrepared)
						journal.State = journalState
						rows := cloneImportDisposition(localstore.WorkspaceMaterializationDisposition{Operations: fixtureRows}).Operations
						candidate, err := cloneImportCandidate(fixtureCandidate)
						if err != nil {
							t.Fatal(err)
						}
						wantKind := checkpointRecoveryPrepared
						if journalState != "prepared" {
							postimage, err := checkpointPublicationPostimage(journal)
							if err != nil {
								t.Fatal(err)
							}
							candidate = &postimage
							for index := range rows {
								rows[index].State = "materialized"
							}
							wantKind = checkpointRecoveryPublished
							if journalState == "recovered_new" {
								wantKind = checkpointRecoveryNoWork
							}
						}
						disposition := localstore.WorkspaceMaterializationDisposition{
							Journals: []localstore.WorkspaceMaterializationRecord{journal}, Operations: rows,
						}
						proof, err := proveCheckpointRecoveryDisposition(fixtureWorkspace, candidate, disposition)
						if err != nil {
							t.Fatal(err)
						}
						wantDriver := journalState != "recovered_new"
						if proof.kind != wantKind || (proof.driver != nil) != wantDriver ||
							!equalCheckpointRecoveryCandidates(proof.candidate, candidate) || len(proof.operations.Operations) != len(envelope.Operations) {
							t.Fatalf("%s/%s proof semantics=(kind=%d, driver=%t, candidateEqual=%t, operations=%d)",
								fixture.name, journalState, proof.kind, proof.driver != nil,
								equalCheckpointRecoveryCandidates(proof.candidate, candidate), len(proof.operations.Operations))
						}
						for index, wantOperation := range envelope.Operations {
							gotOperation := proof.operations.Operations[index]
							wantPersistedState := fixture.wantRowStates[index]
							if journalState != "prepared" {
								wantPersistedState = "materialized"
							}
							if gotOperation.Generation != wantOperation.Generation || gotOperation.OperationID != wantOperation.OperationID ||
								gotOperation.OperationJSON != wantOperation.OperationJSON || gotOperation.PrepublicationState != wantOperation.PrepublicationState ||
								proof.disposition.Operations[index].State != wantPersistedState {
								t.Fatalf("%s/%s operation %d differs from recorded envelope/materialization", fixture.name, journalState, index)
							}
						}

						wantDriverByte := byte(0)
						if proof.driver != nil {
							wantDriverByte = proof.driver.CandidateTree[0].Data[0]
						}
						wantCandidateName := ""
						if proof.candidate != nil {
							wantCandidateName = proof.candidate.DirectSnapshot.Project.Name
						}
						wantOperationBytes := append([]byte(nil), proof.disposition.Operations[0].OperationJSON...)
						disposition.Journals[0].CandidateTree[0].Data[0] ^= 0xff
						disposition.Operations[0].OperationJSON[0] ^= 0xff
						if candidate != nil {
							candidate.DirectSnapshot.Project.Name = "mutated recovery candidate"
						}
						if (proof.driver != nil && proof.driver.CandidateTree[0].Data[0] != wantDriverByte) ||
							(proof.candidate != nil && proof.candidate.DirectSnapshot.Project.Name != wantCandidateName) ||
							!bytes.Equal(proof.disposition.Operations[0].OperationJSON, wantOperationBytes) {
							t.Fatalf("%s/%s proof retained mutable driver, candidate, or operation input", fixture.name, journalState)
						}
					})
				}
			})
		}
	})

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
	workspace, priorCandidate, prepared, operations := checkpointRecoveryProofFixture(t)
	preparedProof, err := proveCheckpointRecoveryDisposition(workspace, priorCandidate, localstore.WorkspaceMaterializationDisposition{
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
	materialized := cloneImportDisposition(localstore.WorkspaceMaterializationDisposition{Operations: operations}).Operations
	for index := range materialized {
		materialized[index].State = "materialized"
	}
	publishedProof, err := proveCheckpointRecoveryDisposition(workspace, &publishedCandidate, localstore.WorkspaceMaterializationDisposition{
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
	return workspace, candidate, journal, cloneImportDisposition(input.Disposition).Operations
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
	if len(disposition.Journals) != 1 || disposition.Journals[0].State != "recovered_old" {
		t.Fatalf("recovery disposition=%+v", disposition)
	}
	recoveryAssertReturnedStatus(t, fixture.service, request.Scope, got)
}

func TestRecoverUnknownCommitConfirmationMatrix(t *testing.T) {
	tests := []struct {
		name       string
		driver     string
		filesystem checkpointRecoveryFilesystemOutcome
		match      localstore.WorkspaceCheckpointCommitMatch
		confirmErr error
		wantOK     bool
		want       error
	}{
		{name: "prepared recovered old next", driver: "prepared", filesystem: checkpointRecoveryFilesystemRecoveredOld, match: localstore.WorkspaceCheckpointCommitNext, wantOK: true},
		{name: "prepared recovered new next", driver: "prepared", filesystem: checkpointRecoveryFilesystemRecoveredNew, match: localstore.WorkspaceCheckpointCommitNext, wantOK: true},
		{name: "published recovered new next", driver: "published", filesystem: checkpointRecoveryFilesystemRecoveredNew, match: localstore.WorkspaceCheckpointCommitNext, wantOK: true},
		{name: "prepared prior", driver: "prepared", filesystem: checkpointRecoveryFilesystemRecoveredOld, match: localstore.WorkspaceCheckpointCommitPrior, want: localstore.ErrCommitOutcomeUnknown},
		{name: "published prior", driver: "published", filesystem: checkpointRecoveryFilesystemRecoveredNew, match: localstore.WorkspaceCheckpointCommitPrior, want: localstore.ErrCommitOutcomeUnknown},
		{name: "third", driver: "prepared", filesystem: checkpointRecoveryFilesystemRecoveredOld, match: localstore.WorkspaceCheckpointCommitThird, want: ErrCheckpointRecoveryBlocked},
		{name: "invalid", driver: "prepared", filesystem: checkpointRecoveryFilesystemRecoveredOld, match: localstore.WorkspaceCheckpointCommitMatch(99), want: ErrCheckpointRecoveryBlocked},
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
			fixture.service.confirmCheckpointCommit = func(
				context.Context,
				localstore.WorkspaceCheckpointCommitState,
				localstore.WorkspaceCheckpointCommitState,
			) (localstore.WorkspaceCheckpointCommitMatch, error) {
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
	disposition localstore.WorkspaceMaterializationDisposition
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
		result.disposition, err = tx.MaterializationDisposition(context.Background())
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
