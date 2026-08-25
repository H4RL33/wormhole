package projectstate

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestProveCheckpointPlanAbsentCandidateGolden(t *testing.T) {
	input := checkpointPlanFixture(t)
	got, err := proveCheckpointPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	wantTree, err := state.EncodeTree(input.Composed.view.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	wantOperations, err := encodeCheckpointOperations(CheckpointOperationsV1{
		SchemaVersion: 1, InitialThroughGeneration: 0, Operations: []CheckpointOperationV1{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !equalCheckpointTree(got.PriorTree, input.PriorLiveTree) || got.PriorTreeDigest != input.Composed.status.AcceptedSnapshot.Digest ||
		!equalCheckpointTree(got.CandidateTree, wantTree) || got.CandidateDigest != input.Composed.view.Snapshot.Digest ||
		got.ThroughGeneration != 0 || got.IncludedOperations == nil || len(got.IncludedOperations) != 0 ||
		got.IncludedOperationsJSON != wantOperations || got.PriorCandidateJSON != checkpointPriorCandidateAbsentGolden ||
		got.PublicationReviewDigest != input.Review.reviewDigest {
		t.Fatalf("absent checkpoint plan = %+v", got)
	}
	publication, err := decodeCheckpointPublicationReview(got.PublicationReviewJSON)
	if err != nil || publication.Review != input.Review.envelope || publication.ReviewDigest != input.Review.reviewDigest || publication.CheckpointedBy != input.Actor {
		t.Fatalf("publication proof = (%+v, %v)", publication, err)
	}
}

func TestProveCheckpointPlanHappyShapesAndSelection(t *testing.T) {
	base := checkpointPlanFixture(t)
	tests := []struct {
		name  string
		build func(checkpointPlanInput) checkpointPlanInput
		want  []string
	}{
		{"absent", func(input checkpointPlanInput) checkpointPlanInput { return input }, nil},
		{"pending workspace", func(input checkpointPlanInput) checkpointPlanInput {
			input.Composed.status.State = "pending"
			input.Review.workspace.State = "pending"
			input.Review.composed.status.State = "pending"
			input.Review.status.State = "pending"
			return input
		}, nil},
		{"direct only", func(input checkpointPlanInput) checkpointPlanInput {
			direct := checkpointPlanMutatedSnapshot(t, input.Composed.status.AcceptedSnapshot, "direct")
			input.Current = checkpointPlanCandidate(input.Binding, direct, nil, 0)
			checkpointPlanRefresh(t, &input)
			return input
		}, nil},
		{"direct with named zero offset", func(input checkpointPlanInput) checkpointPlanInput {
			direct := checkpointPlanMutatedSnapshot(t, input.Composed.status.AcceptedSnapshot, "direct zero offset")
			input.Current = checkpointPlanCandidate(input.Binding, direct, nil, 0)
			input.Current.ImportedAt = input.Current.ImportedAt.In(time.FixedZone("semantic UTC", 0))
			checkpointPlanRefresh(t, &input)
			return input
		}, nil},
		{"rebased at zero", func(input checkpointPlanInput) checkpointPlanInput {
			direct := checkpointPlanMutatedSnapshot(t, input.Composed.status.AcceptedSnapshot, "direct")
			rebased := checkpointPlanMutatedSnapshot(t, direct, "rebased zero")
			input.Current = checkpointPlanCandidate(input.Binding, direct, &rebased, 0)
			checkpointPlanRefresh(t, &input)
			return input
		}, nil},
		{"rebased positive", func(input checkpointPlanInput) checkpointPlanInput {
			direct := checkpointPlanMutatedSnapshot(t, input.Composed.status.AcceptedSnapshot, "direct")
			rebased := checkpointPlanMutatedSnapshot(t, direct, "rebased positive")
			input.Current = checkpointPlanCandidate(input.Binding, direct, &rebased, 4)
			checkpointPlanRefresh(t, &input)
			return input
		}, nil},
		{"rebased prefix and active suffix", func(input checkpointPlanInput) checkpointPlanInput {
			direct := checkpointPlanMutatedSnapshot(t, input.Composed.status.AcceptedSnapshot, "direct")
			rebasedOperation, rebased := checkpointPlanProjectOperation(t, direct, "90000000-0000-4000-8000-000000000001", "rebased")
			activeOperation, _ := checkpointPlanProjectOperation(t, rebased, "90000000-0000-4000-8000-000000000002", "active")
			input.Current = checkpointPlanCandidate(input.Binding, direct, &rebased, 1)
			input.Disposition.Operations = []localstore.WorkspaceOperation{
				checkpointPlanOperationRow(t, 1, "rebased", rebasedOperation),
				checkpointPlanOperationRow(t, 2, "active", activeOperation),
			}
			checkpointPlanRefresh(t, &input)
			return input
		}, []string{"90000000-0000-4000-8000-000000000001", "90000000-0000-4000-8000-000000000002"}},
		{"ignored stashed and discarded rows", func(input checkpointPlanInput) checkpointPlanInput {
			first, _ := checkpointPlanProjectOperation(t, input.Composed.status.AcceptedSnapshot, "90000000-0000-4000-8000-000000000004", "ignored stash")
			second, _ := checkpointPlanProjectOperation(t, input.Composed.status.AcceptedSnapshot, "90000000-0000-4000-8000-000000000005", "ignored discard")
			active, _ := checkpointPlanProjectOperation(t, input.Composed.status.AcceptedSnapshot, "90000000-0000-4000-8000-000000000006", "selected")
			stashID := "80000000-0000-4000-8000-000000000001"
			stashed := checkpointPlanOperationRow(t, 1, "stashed", first)
			stashed.StashedByStashID = &stashID
			input.Disposition.Operations = []localstore.WorkspaceOperation{
				stashed, checkpointPlanOperationRow(t, 2, "discarded", second), checkpointPlanOperationRow(t, 3, "active", active),
			}
			checkpointPlanRefresh(t, &input)
			return input
		}, []string{"90000000-0000-4000-8000-000000000006"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := test.build(cloneCheckpointPlanInput(t, base))
			got, err := proveCheckpointPlan(input)
			if err != nil {
				t.Fatal(err)
			}
			if got.IncludedOperations == nil || len(got.IncludedOperations) != len(test.want) {
				t.Fatalf("selected operations = %+v, want IDs %v", got.IncludedOperations, test.want)
			}
			for index, operationID := range test.want {
				if got.IncludedOperations[index].OperationID != operationID {
					t.Fatalf("selected[%d]=%+v, want %s", index, got.IncludedOperations[index], operationID)
				}
			}
			candidateTree, err := state.DecodeTree(got.CandidateTree)
			if err != nil || candidateTree.Digest != got.CandidateDigest || got.CandidateDigest != input.Composed.view.Snapshot.Digest {
				t.Fatalf("candidate tree = (%+v, %v), plan digest %q", candidateTree, err, got.CandidateDigest)
			}
			priorDigest, err := state.DigestTree(got.PriorTree)
			if err != nil || priorDigest != got.PriorTreeDigest || !equalCheckpointTree(got.PriorTree, input.PriorLiveTree) {
				t.Fatalf("prior tree digest = (%q, %v), plan digest %q", priorDigest, err, got.PriorTreeDigest)
			}
			operations, err := decodeCheckpointOperations(got.IncludedOperationsJSON)
			if err != nil || operations.InitialThroughGeneration != input.Composed.boundary || len(operations.Operations) != len(got.IncludedOperations) {
				t.Fatalf("operation envelope = (%+v, %v)", operations, err)
			}
			wantThrough := input.Composed.boundary
			for index, row := range got.IncludedOperations {
				operation := operations.Operations[index]
				if operation.Generation != row.Generation || operation.OperationID != row.OperationID ||
					operation.OperationJSON != string(row.OperationJSON) || operation.PrepublicationState != row.State {
					t.Fatalf("operation envelope row %d = %+v, want %+v", index, operation, row)
				}
				if row.Generation > wantThrough {
					wantThrough = row.Generation
				}
			}
			if got.ThroughGeneration != wantThrough || got.ThroughGeneration != input.Composed.view.ThroughGeneration {
				t.Fatalf("through generation = %d, want %d", got.ThroughGeneration, wantThrough)
			}
			priorCandidate, err := decodeCheckpointPriorCandidate(got.PriorCandidateJSON)
			if err != nil || (priorCandidate.Candidate == nil) != (input.Current == nil) {
				t.Fatalf("prior candidate = (%+v, %v)", priorCandidate, err)
			}
			if input.Current != nil {
				candidate := priorCandidate.Candidate
				if candidate.AcceptedBaseDigest != input.Current.AcceptedBaseDigest || candidate.WorkingTreeDigest != input.Current.WorkingTreeDigest ||
					candidate.DirectTree.Digest != input.Current.DirectSnapshot.Digest ||
					(candidate.RebasedTree == nil) != (input.Current.RebasedSnapshot == nil) ||
					candidate.RebasedThroughGeneration != input.Current.RebasedThroughGeneration || candidate.ImportedBy != input.Current.ImportedBy ||
					!candidate.ImportedAt.Equal(input.Current.ImportedAt) {
					t.Fatalf("prior candidate projection = %+v, want %+v", candidate, input.Current)
				}
				if input.Current.RebasedSnapshot != nil && candidate.RebasedTree.Digest != input.Current.RebasedSnapshot.Digest {
					t.Fatalf("rebased candidate digest = %q, want %q", candidate.RebasedTree.Digest, input.Current.RebasedSnapshot.Digest)
				}
			}
			publication, err := decodeCheckpointPublicationReview(got.PublicationReviewJSON)
			if err != nil || publication.ReviewDigest != got.PublicationReviewDigest || publication.CheckpointedBy != input.Actor ||
				publication.Review != input.Review.envelope {
				t.Fatalf("publication envelope = (%+v, %v)", publication, err)
			}
			if input.Current != nil && equalCheckpointTree(got.PriorTree, mustCheckpointPlanTree(t, input.Current.DirectSnapshot)) {
				t.Fatal("prior live tree was incorrectly replaced with the prior candidate direct tree")
			}
			for index, row := range got.IncludedOperations {
				if !bytes.Equal(row.OperationJSON, []byte(operations.Operations[index].OperationJSON)) {
					t.Fatalf("selected row %d bytes differ from envelope", index)
				}
			}
		})
	}
}

func TestProveCheckpointPlanRejectsBindingCurrentAndWorkspaceDrift(t *testing.T) {
	base := checkpointPlanDirectInput(t, checkpointPlanFixture(t))
	tests := []struct {
		name   string
		mutate func(*checkpointPlanInput)
	}{
		{"invalid binding", func(input *checkpointPlanInput) { input.Binding.Checkout.Device = 0 }},
		{"invalid actor", func(input *checkpointPlanInput) { input.Actor.Assurance = types.AssuranceUnknown }},
		{"composed binding", func(input *checkpointPlanInput) { input.Composed.status.Binding.AcceptedRef = "refs/heads/other" }},
		{"review workspace binding", func(input *checkpointPlanInput) {
			input.Review.workspace.Binding.AcceptedCommitSHA = strings.Repeat("b", 40)
		}},
		{"review composed binding", func(input *checkpointPlanInput) { input.Review.composed.status.Binding.Checkout.Inode++ }},
		{"review status binding", func(input *checkpointPlanInput) {
			input.Review.status.Binding.Scope.WorkspaceID = "60000000-0000-4000-8000-000000000001"
		}},
		{"composed accepted snapshot", func(input *checkpointPlanInput) {
			input.Composed.status.AcceptedSnapshot.Digest = publicationRepeatedDigest('f')
		}},
		{"review workspace snapshot", func(input *checkpointPlanInput) { input.Review.workspace.Snapshot.Project.Name = "drift" }},
		{"review composed accepted snapshot", func(input *checkpointPlanInput) { input.Review.composed.status.AcceptedSnapshot.Project.Name = "drift" }},
		{"review status accepted snapshot", func(input *checkpointPlanInput) { input.Review.status.AcceptedSnapshot.Project.Name = "drift" }},
		{"state disagreement", func(input *checkpointPlanInput) { input.Review.workspace.State = "pending" }},
		{"known conflicted state", func(input *checkpointPlanInput) {
			input.Composed.status.State, input.Review.workspace.State = "conflicted", "conflicted"
			input.Review.composed.status.State, input.Review.status.State = "conflicted", "conflicted"
		}},
		{"unknown state", func(input *checkpointPlanInput) {
			input.Composed.status.State, input.Review.workspace.State = "future", "future"
			input.Review.composed.status.State, input.Review.status.State = "future", "future"
		}},
		{"candidate accepted digest", func(input *checkpointPlanInput) { input.Current.AcceptedBaseDigest = publicationRepeatedDigest('f') }},
		{"candidate working digest", func(input *checkpointPlanInput) { input.Current.WorkingTreeDigest = publicationRepeatedDigest('f') }},
		{"candidate direct digest", func(input *checkpointPlanInput) { input.Current.DirectSnapshot.Digest = publicationRepeatedDigest('f') }},
		{"candidate direct project", func(input *checkpointPlanInput) {
			input.Current.DirectSnapshot = checkpointPlanRetargetProject(t, input.Current.DirectSnapshot)
			input.Current.WorkingTreeDigest = input.Current.DirectSnapshot.Digest
		}},
		{"candidate direct repository", func(input *checkpointPlanInput) {
			input.Current.DirectSnapshot = checkpointPlanRetargetRepository(t, input.Current.DirectSnapshot)
			input.Current.WorkingTreeDigest = input.Current.DirectSnapshot.Digest
		}},
		{"candidate boundary", func(input *checkpointPlanInput) { input.Current.RebasedThroughGeneration = 1 }},
		{"candidate negative boundary", func(input *checkpointPlanInput) { input.Current.RebasedThroughGeneration = -1 }},
		{"candidate importer", func(input *checkpointPlanInput) { input.Current.ImportedBy = "system:unknown" }},
		{"candidate time", func(input *checkpointPlanInput) { input.Current.ImportedAt = time.Time{} }},
		{"candidate nonzero offset time", func(input *checkpointPlanInput) {
			input.Current.ImportedAt = input.Current.ImportedAt.In(time.FixedZone("offset", 60*60))
		}},
		{"selected start", func(input *checkpointPlanInput) { input.Composed.selectedStart.Project.Name = "wrong start" }},
		{"selected boundary", func(input *checkpointPlanInput) { input.Composed.boundary = 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := cloneCheckpointPlanInput(t, base)
			test.mutate(&input)
			assertCheckpointPlanRejected(t, input)
		})
	}

	rebased := checkpointPlanRebasedInput(t, checkpointPlanFixture(t), 3)
	for _, test := range []struct {
		name   string
		mutate func(*checkpointPlanInput)
	}{
		{"rebased digest", func(input *checkpointPlanInput) {
			input.Current.RebasedSnapshot.Digest = publicationRepeatedDigest('f')
		}},
		{"rebased project", func(input *checkpointPlanInput) {
			changed := checkpointPlanRetargetProject(t, *input.Current.RebasedSnapshot)
			input.Current.RebasedSnapshot = &changed
		}},
		{"rebased repository", func(input *checkpointPlanInput) {
			changed := checkpointPlanRetargetRepository(t, *input.Current.RebasedSnapshot)
			input.Current.RebasedSnapshot = &changed
		}},
		{"rebased negative boundary", func(input *checkpointPlanInput) {
			input.Current.RebasedThroughGeneration = -1
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := cloneCheckpointPlanInput(t, rebased)
			test.mutate(&input)
			assertCheckpointPlanRejected(t, input)
		})
	}
}

func TestProveCheckpointPlanRejectsCompositionAndDispositionDrift(t *testing.T) {
	base := checkpointPlanActiveInput(t, checkpointPlanFixture(t))
	compositionTests := []struct {
		name   string
		mutate func(*checkpointPlanInput)
	}{
		{"omitted composed operation", func(input *checkpointPlanInput) { input.Composed.operations = input.Composed.operations[:0] }},
		{"extra composed operation", func(input *checkpointPlanInput) {
			input.Composed.operations = append(input.Composed.operations, input.Composed.operations[0])
		}},
		{"changed composed operation", func(input *checkpointPlanInput) {
			input.Composed.operations[0].Operation.ID = "90000000-0000-4000-8000-000000000099"
		}},
		{"composed start", func(input *checkpointPlanInput) { input.Composed.selectedStart.Project.Name = "drift" }},
		{"composed boundary", func(input *checkpointPlanInput) { input.Composed.boundary++ }},
		{"composed result tree", func(input *checkpointPlanInput) { input.Composed.view.Snapshot.Project.Name = "drift" }},
		{"composed result digest", func(input *checkpointPlanInput) { input.Composed.view.Snapshot.Digest = publicationRepeatedDigest('f') }},
		{"composed through", func(input *checkpointPlanInput) { input.Composed.view.ThroughGeneration++ }},
		{"composed applied IDs", func(input *checkpointPlanInput) {
			input.Composed.view.AppliedOperationIDs[0] = "90000000-0000-4000-8000-000000000099"
		}},
		{"status candidate", func(input *checkpointPlanInput) {
			input.Composed.status.CandidateDigest = publicationRepeatedDigest('f')
		}},
		{"status generation", func(input *checkpointPlanInput) { input.Composed.status.OverlayGeneration++ }},
		{"review composed drift", func(input *checkpointPlanInput) { input.Review.composed.view.ThroughGeneration++ }},
	}
	for _, test := range compositionTests {
		t.Run(test.name, func(t *testing.T) {
			input := cloneCheckpointPlanInput(t, base)
			test.mutate(&input)
			assertCheckpointPlanRejected(t, input)
		})
	}
	t.Run("reordered active operations", func(t *testing.T) {
		input := checkpointPlanTwoActiveInput(t, checkpointPlanFixture(t))
		input.Composed.operations[0], input.Composed.operations[1] = input.Composed.operations[1], input.Composed.operations[0]
		assertCheckpointPlanRejected(t, input)
	})

	dispositionTests := []struct {
		name   string
		mutate func(*checkpointPlanInput)
	}{
		{"nil journals", func(input *checkpointPlanInput) { input.Disposition.Journals = nil }},
		{"nil operations", func(input *checkpointPlanInput) { input.Disposition.Operations = nil }},
		{"pending journal", func(input *checkpointPlanInput) {
			input.Disposition.Journals = []localstore.WorkspaceMaterializationRecord{{JournalID: "journal", State: "prepared"}}
		}},
		{"omitted selected row", func(input *checkpointPlanInput) { input.Disposition.Operations = []localstore.WorkspaceOperation{} }},
		{"extra selected row", func(input *checkpointPlanInput) {
			operation, _ := checkpointPlanProjectOperation(
				t, input.Composed.view.Snapshot, "90000000-0000-4000-8000-000000000097", "extra selected",
			)
			input.Disposition.Operations = append(input.Disposition.Operations, checkpointPlanOperationRow(t, 2, "active", operation))
		}},
		{"selected row stashed", func(input *checkpointPlanInput) { input.Disposition.Operations[0].State = "stashed" }},
		{"selected row discarded", func(input *checkpointPlanInput) { input.Disposition.Operations[0].State = "discarded" }},
		{"orphan materialized", func(input *checkpointPlanInput) { input.Disposition.Operations[0].State = "materialized" }},
		{"selected row unknown", func(input *checkpointPlanInput) {
			input.Disposition.Operations[0].State = "future"
			checkpointPlanRefresh(t, input)
		}},
		{"selected row stash owned", func(input *checkpointPlanInput) {
			owner := "80000000-0000-4000-8000-000000000001"
			input.Disposition.Operations[0].StashedByStashID = &owner
		}},
		{"generation zero", func(input *checkpointPlanInput) { input.Disposition.Operations[0].Generation = 0 }},
		{"operation ID malformed", func(input *checkpointPlanInput) { input.Disposition.Operations[0].OperationID = "bad" }},
		{"operation bytes malformed", func(input *checkpointPlanInput) { input.Disposition.Operations[0].OperationJSON = []byte("{") }},
		{"operation bytes noncanonical", func(input *checkpointPlanInput) {
			input.Disposition.Operations[0].OperationJSON = append([]byte(" "), input.Disposition.Operations[0].OperationJSON...)
		}},
		{"operation ID mismatch", func(input *checkpointPlanInput) {
			input.Disposition.Operations[0].OperationID = "90000000-0000-4000-8000-000000000099"
		}},
		{"duplicate generation", func(input *checkpointPlanInput) {
			input.Disposition.Operations = append(input.Disposition.Operations, cloneImportOperation(input.Disposition.Operations[0]))
		}},
		{"duplicate operation ID", func(input *checkpointPlanInput) {
			input.Disposition.Operations = append(input.Disposition.Operations, cloneImportOperation(input.Disposition.Operations[0]))
			input.Disposition.Operations[1].Generation = 2
		}},
		{"nonmonotonic generation", func(input *checkpointPlanInput) {
			second := cloneImportOperation(input.Disposition.Operations[0])
			second.Generation = 2
			second.OperationID = "90000000-0000-4000-8000-000000000098"
			input.Disposition.Operations = append([]localstore.WorkspaceOperation{second}, input.Disposition.Operations...)
		}},
	}
	for _, test := range dispositionTests {
		t.Run(test.name, func(t *testing.T) {
			input := cloneCheckpointPlanInput(t, base)
			test.mutate(&input)
			assertCheckpointPlanRejected(t, input)
		})
	}

	t.Run("active at or below boundary", func(t *testing.T) {
		input := checkpointPlanRebasedInput(t, cloneCheckpointPlanInput(t, checkpointPlanFixture(t)), 2)
		operation, _ := checkpointPlanProjectOperation(t, *input.Current.RebasedSnapshot, "90000000-0000-4000-8000-000000000030", "invalid old active")
		input.Disposition.Operations = []localstore.WorkspaceOperation{checkpointPlanOperationRow(t, 1, "active", operation)}
		checkpointPlanRefresh(t, &input)
		assertCheckpointPlanRejected(t, input)
	})
	t.Run("rebased above boundary", func(t *testing.T) {
		input := checkpointPlanRebasedInput(t, cloneCheckpointPlanInput(t, checkpointPlanFixture(t)), 1)
		operation, _ := checkpointPlanProjectOperation(t, *input.Current.RebasedSnapshot, "90000000-0000-4000-8000-000000000031", "invalid later rebased")
		input.Disposition.Operations = []localstore.WorkspaceOperation{checkpointPlanOperationRow(t, 2, "rebased", operation)}
		checkpointPlanRefresh(t, &input)
		assertCheckpointPlanRejected(t, input)
	})
}

func TestProveCheckpointPlanRejectsPublicationTrustPolicyAndProjectionDrift(t *testing.T) {
	base := checkpointPlanActiveInput(t, checkpointPlanFixture(t))
	tests := []struct {
		name   string
		mutate func(*checkpointPlanInput)
	}{
		{"trust root", func(input *checkpointPlanInput) { input.Review.trust.git.root += "/other" }},
		{"trust checkout", func(input *checkpointPlanInput) { input.Review.trust.git.checkout.Inode++ }},
		{"trust ref", func(input *checkpointPlanInput) { input.Review.trust.git.acceptedRef = "refs/heads/other" }},
		{"trust commit", func(input *checkpointPlanInput) { input.Review.trust.git.commit = strings.Repeat("b", 40) }},
		{"trust tree", func(input *checkpointPlanInput) { input.Review.trust.git.tree[0].Data[0] ^= 1 }},
		{"trust snapshot digest", func(input *checkpointPlanInput) {
			input.Review.trust.git.snapshot.Digest = publicationRepeatedDigest('f')
		}},
		{"trust snapshot body", func(input *checkpointPlanInput) { input.Review.trust.git.snapshot.Project.Name = "drift" }},
		{"coherent trust snapshot binding mismatch", func(input *checkpointPlanInput) {
			input.Review.trust.git.snapshot = checkpointPlanRetargetProject(t, input.Review.trust.git.snapshot)
			input.Review.trust.git.tree = mustCheckpointPlanTree(t, input.Review.trust.git.snapshot)
		}},
		{"origin root", func(input *checkpointPlanInput) { input.Review.trust.origin.root += "/other" }},
		{"origin checkout", func(input *checkpointPlanInput) { input.Review.trust.origin.checkout.Device++ }},
		{"origin body", func(input *checkpointPlanInput) { input.Review.trust.origin.origin.Path = "acme/other" }},
		{"origin digest", func(input *checkpointPlanInput) { input.Review.trust.origin.digest = publicationRepeatedDigest('f') }},
		{"policy repository", func(input *checkpointPlanInput) { input.Review.policy.Repository.ImmutableID = "other" }},
		{"policy origin", func(input *checkpointPlanInput) {
			digest := publicationRepeatedDigest('f')
			input.Review.policy.OriginDigest = &digest
		}},
		{"policy classification", func(input *checkpointPlanInput) { input.Review.policy.Classification = types.PublicationPrivateGit }},
		{"policy revision", func(input *checkpointPlanInput) { input.Review.policy.PolicyRevision++ }},
		{"policy transition", func(input *checkpointPlanInput) { input.Review.policy.TransitionKind = "bootstrap" }},
		{"policy actor", func(input *checkpointPlanInput) { input.Review.policy.ChangedBy.HumanPrincipalID = "bad" }},
		{"policy time", func(input *checkpointPlanInput) { value := time.Time{}; input.Review.policy.ChangedAt = &value }},
		{"semantic diff body", func(input *checkpointPlanInput) {
			input.Review.semanticDiff.Changes[0].Fields[0].After.Value = []byte(`"drift"`)
		}},
		{"semantic diff digest", func(input *checkpointPlanInput) { input.Review.semanticDiffDigest = publicationRepeatedDigest('f') }},
		{"envelope schema", func(input *checkpointPlanInput) { input.Review.envelope.SchemaVersion = 2 }},
		{"envelope scope", func(input *checkpointPlanInput) {
			input.Review.envelope.Scope.WorkspaceID = "60000000-0000-4000-8000-000000000001"
		}},
		{"envelope repository", func(input *checkpointPlanInput) { input.Review.envelope.Repository.ImmutableID = "other" }},
		{"envelope origin", func(input *checkpointPlanInput) { input.Review.envelope.OriginDigest = publicationRepeatedDigest('f') }},
		{"envelope classification", func(input *checkpointPlanInput) { input.Review.envelope.Classification = types.PublicationPrivateGit }},
		{"envelope revision", func(input *checkpointPlanInput) { input.Review.envelope.PolicyRevision++ }},
		{"envelope ref", func(input *checkpointPlanInput) { input.Review.envelope.AcceptedRef = "refs/heads/other" }},
		{"envelope commit", func(input *checkpointPlanInput) { input.Review.envelope.AcceptedCommitSHA = strings.Repeat("b", 40) }},
		{"envelope accepted tree", func(input *checkpointPlanInput) {
			input.Review.envelope.AcceptedTreeDigest = publicationRepeatedDigest('f')
		}},
		{"envelope candidate", func(input *checkpointPlanInput) {
			input.Review.envelope.CandidateTreeDigest = publicationRepeatedDigest('f')
		}},
		{"envelope semantic diff", func(input *checkpointPlanInput) {
			input.Review.envelope.SemanticDiffDigest = publicationRepeatedDigest('f')
		}},
		{"envelope generation", func(input *checkpointPlanInput) { input.Review.envelope.OverlayGeneration++ }},
		{"review digest", func(input *checkpointPlanInput) { input.Review.reviewDigest = publicationRepeatedDigest('f') }},
		{"review status candidate", func(input *checkpointPlanInput) { input.Review.status.CandidateDigest = publicationRepeatedDigest('f') }},
		{"review status generation", func(input *checkpointPlanInput) { input.Review.status.OverlayGeneration++ }},
		{"review status classification", func(input *checkpointPlanInput) {
			input.Review.status.PublicationClassification = types.PublicationPrivateGit
		}},
		{"review status digest", func(input *checkpointPlanInput) {
			input.Review.status.PublicationReviewDigest = publicationRepeatedDigest('f')
		}},
		{"review diff body", func(input *checkpointPlanInput) {
			input.Review.diff.SemanticDiff.Changes[0].Fields[0].After.Value = []byte(`"drift"`)
		}},
		{"review diff candidate", func(input *checkpointPlanInput) { input.Review.diff.CandidateDigest = publicationRepeatedDigest('f') }},
		{"review diff generation", func(input *checkpointPlanInput) { input.Review.diff.OverlayGeneration++ }},
		{"review diff classification", func(input *checkpointPlanInput) {
			input.Review.diff.PublicationClassification = types.PublicationPrivateGit
		}},
		{"review diff digest", func(input *checkpointPlanInput) {
			input.Review.diff.PublicationReviewDigest = publicationRepeatedDigest('f')
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := cloneCheckpointPlanInput(t, base)
			test.mutate(&input)
			assertCheckpointPlanRejected(t, input)
		})
	}
}

func TestProveCheckpointPlanRejectsPriorLiveFailures(t *testing.T) {
	base := checkpointPlanDirectInput(t, checkpointPlanFixture(t))
	tests := []struct {
		name   string
		mutate func(*checkpointPlanInput)
	}{
		{"nil tree", func(input *checkpointPlanInput) { input.PriorLiveTree = nil }},
		{"malformed bytes", func(input *checkpointPlanInput) { input.PriorLiveTree[0].Data = []byte("bad") }},
		{"noncanonical order", func(input *checkpointPlanInput) {
			input.PriorLiveTree[0], input.PriorLiveTree[1] = input.PriorLiveTree[1], input.PriorLiveTree[0]
		}},
		{"unknown path", func(input *checkpointPlanInput) {
			input.PriorLiveTree = append(input.PriorLiveTree, state.File{Path: "unknown", Data: []byte("x")})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := cloneCheckpointPlanInput(t, base)
			test.mutate(&input)
			assertCheckpointPlanRejected(t, input)
		})
	}

	t.Run("valid identity mismatch", func(t *testing.T) {
		input := cloneCheckpointPlanInput(t, base)
		other := checkpointPlanMutatedSnapshot(t, input.Composed.status.AcceptedSnapshot, "other identity")
		other.Config.Repository = types.RepositoryIdentity{Provider: "github", ImmutableID: "other", CanonicalRemote: "https://github.com/acme/other"}
		other = mustCheckpointPlanSnapshot(t, other)
		input.PriorLiveTree = mustCheckpointPlanTree(t, other)
		assertCheckpointPlanRejected(t, input)
	})
}

func TestProveCheckpointPlanDeterminismZeroAndOwnership(t *testing.T) {
	input := checkpointPlanActiveInput(t, checkpointPlanRebasedInput(t, checkpointPlanFixture(t), 3))
	wantInput := cloneCheckpointPlanInput(t, input)
	want, err := proveCheckpointPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	for iteration := 0; iteration < 10; iteration++ {
		got, err := proveCheckpointPlan(input)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d = (%+v, %v), want deterministic %+v", iteration, got, err, want)
		}
	}
	if !reflect.DeepEqual(input, wantInput) {
		t.Fatal("successful proof mutated its input")
	}

	input.PriorLiveTree[0].Data[0] ^= 1
	input.Current.DirectSnapshot.Project.Name = "mutated current"
	input.Current.RebasedSnapshot.Project.Name = "mutated rebased current"
	input.Disposition.Operations[0].OperationJSON[0] ^= 1
	input.Composed.view.Snapshot.Project.Name = "mutated composed"
	input.Review.trust.git.tree[0].Data[0] ^= 1
	input.Review.semanticDiff.Changes[0].Fields[0].After.Value[0] ^= 1
	if !reflect.DeepEqual(want, mustProveCheckpointPlan(t, wantInput)) {
		t.Fatal("successful plan aliases a mutated input")
	}

	mutated := want
	mutated.PriorTree[0].Data[0] ^= 1
	mutated.CandidateTree[0].Data[0] ^= 1
	mutated.IncludedOperations[0].OperationJSON[0] ^= 1
	if reflect.DeepEqual(mutated, mustProveCheckpointPlan(t, wantInput)) {
		t.Fatal("fresh successful plan aliases a previously returned plan")
	}
}

func checkpointPlanFixture(t *testing.T) checkpointPlanInput {
	t.Helper()
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
	configurePublicationForTest(t, fixture, types.PublicationLocalOnly, diffActorEnvelope(), time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
	workspace, err := fixture.service.repo.Workspace(context.Background(), fixture.binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	observer := fixture.service.publication.publicationTrustObserver()
	outside, err := observer(context.Background(), workspace.Binding)
	if err != nil {
		t.Fatal(err)
	}
	var evidence publicationReviewTransactionEvidence
	var current *localstore.WorkspaceCandidateRecord
	var disposition localstore.WorkspaceCurrentMaterialization
	var attempt publicationTransitionAttempt
	if err := fixture.service.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		var transactionErr error
		evidence, transactionErr = fixture.service.publication.publicationReviewInTransaction(
			context.Background(), tx, workspace, outside, observer, &attempt,
		)
		if transactionErr != nil {
			return transactionErr
		}
		current, transactionErr = tx.Candidate(context.Background())
		if transactionErr != nil {
			return transactionErr
		}
		disposition, transactionErr = readCurrentMaterializationWorkset(context.Background(), tx)
		return transactionErr
	}); err != nil {
		t.Fatal(err)
	}
	priorTree, err := state.EncodeTree(workspace.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return checkpointPlanInput{
		Binding: workspace.Binding, Current: current, Composed: evidence.composed,
		Disposition: disposition, Review: evidence, PriorLiveTree: priorTree, Actor: diffActorEnvelope(),
	}
}

func cloneCheckpointPlanInput(t *testing.T, input checkpointPlanInput) checkpointPlanInput {
	t.Helper()
	current, err := cloneImportCandidate(input.Current)
	if err != nil {
		t.Fatal(err)
	}
	composed, err := clonePublicationComposedWorkspace(input.Composed)
	if err != nil {
		t.Fatal(err)
	}
	review, err := clonePublicationReviewTransactionEvidence(input.Review)
	if err != nil {
		t.Fatal(err)
	}
	return checkpointPlanInput{
		Binding: input.Binding, Current: current, Composed: composed,
		Disposition: cloneImportCurrentMaterialization(input.Disposition), Review: review,
		PriorLiveTree: cloneCheckpointTree(input.PriorLiveTree), Actor: input.Actor,
	}
}

func checkpointPlanDirectInput(t *testing.T, input checkpointPlanInput) checkpointPlanInput {
	t.Helper()
	direct := checkpointPlanMutatedSnapshot(t, input.Composed.status.AcceptedSnapshot, "direct candidate")
	input.Current = checkpointPlanCandidate(input.Binding, direct, nil, 0)
	checkpointPlanRefresh(t, &input)
	return input
}

func checkpointPlanRebasedInput(t *testing.T, input checkpointPlanInput, boundary int64) checkpointPlanInput {
	t.Helper()
	direct := checkpointPlanMutatedSnapshot(t, input.Composed.status.AcceptedSnapshot, "direct candidate")
	rebased := checkpointPlanMutatedSnapshot(t, direct, "rebased candidate")
	input.Current = checkpointPlanCandidate(input.Binding, direct, &rebased, boundary)
	checkpointPlanRefresh(t, &input)
	return input
}

func checkpointPlanActiveInput(t *testing.T, input checkpointPlanInput) checkpointPlanInput {
	t.Helper()
	start, boundary := selectCandidateStart(input.Composed.status.AcceptedSnapshot, input.Current)
	operation, _ := checkpointPlanProjectOperation(t, start, "90000000-0000-4000-8000-000000000010", "active candidate")
	input.Disposition.Operations = []localstore.WorkspaceOperation{checkpointPlanOperationRow(t, boundary+1, "active", operation)}
	checkpointPlanRefresh(t, &input)
	return input
}

func checkpointPlanTwoActiveInput(t *testing.T, input checkpointPlanInput) checkpointPlanInput {
	t.Helper()
	start, _ := selectCandidateStart(input.Composed.status.AcceptedSnapshot, input.Current)
	first, afterFirst := checkpointPlanProjectOperation(t, start, "90000000-0000-4000-8000-000000000011", "first active")
	second, _ := checkpointPlanProjectOperation(t, afterFirst, "90000000-0000-4000-8000-000000000012", "second active")
	input.Disposition.Operations = []localstore.WorkspaceOperation{
		checkpointPlanOperationRow(t, 1, "active", first),
		checkpointPlanOperationRow(t, 2, "active", second),
	}
	checkpointPlanRefresh(t, &input)
	return input
}

func assertCheckpointPlanRejected(t *testing.T, input checkpointPlanInput) {
	t.Helper()
	before := fmt.Sprintf("%#v", input)
	got, err := proveCheckpointPlan(input)
	if err == nil || !reflect.DeepEqual(got, checkpointPlan{}) {
		t.Fatalf("invalid checkpoint plan = (%+v, %v), want exact zero and error", got, err)
	}
	if fmt.Sprintf("%#v", input) != before {
		t.Fatal("proveCheckpointPlan mutated its rejected input")
	}
}

func checkpointPlanCandidate(binding types.WorkspaceBinding, direct state.Snapshot, rebased *state.Snapshot, boundary int64) *localstore.WorkspaceCandidateRecord {
	return &localstore.WorkspaceCandidateRecord{
		AcceptedBaseDigest: state.Digest(binding.AcceptedTreeDigest), WorkingTreeDigest: direct.Digest,
		DirectSnapshot: direct, RebasedSnapshot: rebased, RebasedThroughGeneration: boundary,
		ImportedBy: "70000000-0000-4000-8000-000000000001", ImportedAt: time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC),
	}
}

func checkpointPlanMutatedSnapshot(t *testing.T, input state.Snapshot, name string) state.Snapshot {
	t.Helper()
	cloned, err := cloneImportSnapshot(input)
	if err != nil {
		t.Fatal(err)
	}
	cloned.Project.Name = name
	cloned.Project.UpdatedAt = cloned.Project.UpdatedAt.Add(time.Minute)
	return mustCheckpointPlanSnapshot(t, cloned)
}

func checkpointPlanRetargetProject(t *testing.T, input state.Snapshot) state.Snapshot {
	t.Helper()
	input.Config.ProjectID = "60000000-0000-4000-8000-000000000001"
	input.Project.ID = input.Config.ProjectID
	return mustCheckpointPlanSnapshot(t, input)
}

func checkpointPlanRetargetRepository(t *testing.T, input state.Snapshot) state.Snapshot {
	t.Helper()
	input.Config.Repository = types.RepositoryIdentity{
		Provider: "github", ImmutableID: "other", CanonicalRemote: "https://github.com/acme/other",
	}
	return mustCheckpointPlanSnapshot(t, input)
}

func checkpointPlanProjectOperation(t *testing.T, input state.Snapshot, operationID, name string) (state.OperationV1, state.Snapshot) {
	t.Helper()
	project := input.Project
	project.Name = name
	project.UpdatedAt = project.UpdatedAt.Add(time.Minute)
	operation := state.OperationV1{
		SchemaVersion: 1, ID: operationID, Kind: state.OperationPutRecord, ExpectedViewDigest: input.Digest,
		Actor: diffActorEnvelope(), PutRecord: &state.PutRecordV1{Record: state.RecordValueV1{Project: &project}},
	}
	next, err := state.ApplyOperation(input, operation)
	if err != nil {
		t.Fatal(err)
	}
	return operation, next
}

func checkpointPlanOperationRow(t *testing.T, generation int64, rowState string, operation state.OperationV1) localstore.WorkspaceOperation {
	t.Helper()
	raw, err := state.CanonicalOperation(operation)
	if err != nil {
		t.Fatal(err)
	}
	return localstore.WorkspaceOperation{Generation: generation, OperationID: operation.ID, OperationJSON: raw, State: rowState}
}

func checkpointPlanAcceptedHistory(
	t *testing.T,
	input checkpointPlanInput,
	row localstore.WorkspaceOperation,
) localstore.WorkspaceMaterializationRecord {
	t.Helper()
	accepted := input.Composed.status.AcceptedSnapshot
	operation, err := state.DecodeOperation(row.OperationJSON)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := state.ApplyOperation(accepted, operation)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encodeCheckpointOperations(CheckpointOperationsV1{
		SchemaVersion: 1, InitialThroughGeneration: 0,
		Operations: []CheckpointOperationV1{{
			Generation: row.Generation, OperationID: row.OperationID, OperationJSON: string(row.OperationJSON), PrepublicationState: "active",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	journal := checkpointPlanHistoricalJournal(t, input, "accepted", accepted, accepted, candidate, row.Generation)
	journal.IncludedOperationsJSON = &raw
	return journal
}

func checkpointPlanEmptyJournal(
	t *testing.T,
	input checkpointPlanInput,
	journalID, journalState string,
) localstore.WorkspaceMaterializationRecord {
	t.Helper()
	history := checkpointPlanHistoricalInput(t, input, journalState)
	journal := history.Disposition.Journals[0]
	journal.JournalID = journalID
	return journal
}

func checkpointPlanHistoricalInput(t *testing.T, input checkpointPlanInput, journalState string) checkpointPlanInput {
	t.Helper()
	accepted := checkpointPlanMutatedSnapshot(t, input.Composed.status.AcceptedSnapshot, "historical accepted")
	prior := checkpointPlanMutatedSnapshot(t, accepted, "historical prior live")
	candidate := checkpointPlanMutatedSnapshot(t, accepted, "historical candidate")
	journal := checkpointPlanHistoricalJournal(t, input, journalState, accepted, prior, candidate, 0)
	switch journalState {
	case "accepted":
		journal.IncludedOperationsJSON = nil
	case "recovered_old":
		malformed := "{"
		journal.IncludedOperationsJSON = &malformed
	default:
		operations, err := encodeCheckpointOperations(CheckpointOperationsV1{
			SchemaVersion: 1, InitialThroughGeneration: 0, Operations: []CheckpointOperationV1{},
		})
		if err != nil {
			t.Fatal(err)
		}
		journal.IncludedOperationsJSON = &operations
	}
	input.Disposition = localstore.WorkspaceCurrentMaterialization{
		Journals:   []localstore.WorkspaceMaterializationRecord{journal},
		Operations: []localstore.WorkspaceOperation{},
	}
	return input
}

func checkpointPlanHistoricalJournal(
	t *testing.T,
	input checkpointPlanInput,
	journalState string,
	accepted, prior, candidate state.Snapshot,
	throughGeneration int64,
) localstore.WorkspaceMaterializationRecord {
	t.Helper()
	review := input.Review.envelope
	review.AcceptedRef = "refs/heads/checkpoint-history"
	review.AcceptedCommitSHA = strings.Repeat("c", 40)
	review.AcceptedTreeDigest = accepted.Digest
	review.CandidateTreeDigest = candidate.Digest
	review.OverlayGeneration = throughGeneration
	_, reviewDigest, err := encodePublicationReviewEnvelope(review)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := encodeCheckpointPublicationReview(checkpointPublicationReviewV1{
		SchemaVersion: 1, Kind: "checkpoint_publication_review", Review: review,
		ReviewDigest: reviewDigest, CheckpointedBy: input.Actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	priorCandidate, err := encodeCheckpointPriorCandidate(checkpointPriorCandidateV1{
		SchemaVersion: 1, Kind: "checkpoint_prior_candidate",
	})
	if err != nil {
		t.Fatal(err)
	}
	return localstore.WorkspaceMaterializationRecord{
		JournalID:          "10000000-0000-4000-8000-000000000010",
		ExpectedLiveDigest: prior.Digest, AcceptedBaseDigest: accepted.Digest, Checkout: input.Binding.Checkout,
		PriorTreeDigest: prior.Digest, CandidateDigest: candidate.Digest, ThroughGeneration: throughGeneration,
		PriorTree: mustCheckpointPlanTree(t, prior), CandidateTree: mustCheckpointPlanTree(t, candidate),
		PublicationReviewProofVersion: 1, PublicationReviewJSON: &publication,
		PriorCandidateJSON: &priorCandidate, State: journalState,
	}
}

func checkpointPlanRewriteHistoricalReview(
	t *testing.T,
	journal *localstore.WorkspaceMaterializationRecord,
	mutate func(*publicationReviewEnvelopeV1),
) {
	t.Helper()
	publication, err := decodeCheckpointPublicationReview(*journal.PublicationReviewJSON)
	if err != nil {
		t.Fatal(err)
	}
	mutate(&publication.Review)
	_, publication.ReviewDigest, err = encodePublicationReviewEnvelope(publication.Review)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encodeCheckpointPublicationReview(publication)
	if err != nil {
		t.Fatal(err)
	}
	journal.PublicationReviewJSON = &raw
}

func checkpointPlanRewriteHistoricalPrior(
	t *testing.T,
	journal *localstore.WorkspaceMaterializationRecord,
	mutate func(*checkpointPriorCandidateV1),
) {
	t.Helper()
	prior, err := decodeCheckpointPriorCandidate(*journal.PriorCandidateJSON)
	if err != nil {
		t.Fatal(err)
	}
	mutate(&prior)
	raw, err := encodeCheckpointPriorCandidate(prior)
	if err != nil {
		t.Fatal(err)
	}
	journal.PriorCandidateJSON = &raw
}

func checkpointPlanSetHistoricalOperationBoundary(
	t *testing.T,
	journal *localstore.WorkspaceMaterializationRecord,
	boundary int64,
) {
	t.Helper()
	raw, err := encodeCheckpointOperations(CheckpointOperationsV1{
		SchemaVersion: 1, InitialThroughGeneration: boundary, Operations: []CheckpointOperationV1{},
	})
	if err != nil {
		t.Fatal(err)
	}
	journal.IncludedOperationsJSON = &raw
	journal.ThroughGeneration = boundary
	checkpointPlanRewriteHistoricalReview(t, journal, func(review *publicationReviewEnvelopeV1) {
		review.OverlayGeneration = boundary
	})
}

func checkpointPlanSetHistoricalPriorBoundary(
	t *testing.T,
	journal *localstore.WorkspaceMaterializationRecord,
	boundary int64,
) {
	t.Helper()
	direct := mustCheckpointPlanDecodeTree(t, journal.CandidateTree)
	directTree := checkpointPriorTree(mustCheckpointPlanTree(t, direct), direct.Digest)
	candidate := &checkpointPriorCandidateStateV1{
		AcceptedBaseDigest: journal.AcceptedBaseDigest, WorkingTreeDigest: direct.Digest,
		DirectTree: directTree, RebasedThroughGeneration: boundary,
		ImportedBy: "70000000-0000-4000-8000-000000000001",
		ImportedAt: time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC),
	}
	if boundary != 0 {
		rebased := directTree
		candidate.RebasedTree = &rebased
	}
	raw, err := encodeCheckpointPriorCandidate(checkpointPriorCandidateV1{
		SchemaVersion: 1, Kind: "checkpoint_prior_candidate", Candidate: candidate,
	})
	if err != nil {
		t.Fatal(err)
	}
	journal.PriorCandidateJSON = &raw
}

func checkpointPlanRefresh(t *testing.T, input *checkpointPlanInput) {
	t.Helper()
	accepted := input.Composed.status.AcceptedSnapshot
	start, boundary := selectCandidateStart(accepted, input.Current)
	activeRows := make([]localstore.WorkspaceOperation, 0)
	for _, row := range input.Disposition.Operations {
		if row.State == "active" && row.Generation > boundary && row.StashedByStashID == nil {
			activeRows = append(activeRows, cloneImportOperation(row))
		}
	}
	operations, err := decodeStoredOperations(activeRows)
	if err != nil {
		t.Fatal(err)
	}
	view, err := Compose(start, boundary, operations)
	if err != nil {
		t.Fatal(err)
	}
	status := WorkspaceStatus{
		Binding: input.Binding, State: input.Composed.status.State, AcceptedSnapshot: accepted,
		CandidateDigest: view.Snapshot.Digest, OverlayGeneration: view.ThroughGeneration,
	}
	composed := composedWorkspace{status: status, view: view, operations: operations, selectedStart: start, boundary: boundary}
	input.Composed = composed
	input.Review.workspace = localstore.WorkspaceRecord{Binding: input.Binding, Snapshot: accepted, State: status.State}
	input.Review.composed = composed
	attributed, semantic, err := publicationAttributedDiff(accepted, start, boundary, operations)
	if err != nil {
		t.Fatal(err)
	}
	semantic, err = normalizePublicationReviewDiff(semantic)
	if err != nil {
		t.Fatal(err)
	}
	_, semanticDigest, err := encodePublicationSemanticDiff(semantic)
	if err != nil {
		t.Fatal(err)
	}
	policy := input.Review.policy
	envelope := publicationReviewEnvelopeV1{
		SchemaVersion: 1, Kind: "publication_review", Scope: input.Binding.Scope,
		Repository: input.Binding.Repository, OriginDigest: input.Review.trust.origin.digest,
		Classification: policy.Classification, PolicyRevision: policy.PolicyRevision,
		AcceptedRef: input.Binding.AcceptedRef, AcceptedCommitSHA: input.Binding.AcceptedCommitSHA,
		AcceptedTreeDigest: state.Digest(input.Binding.AcceptedTreeDigest), CandidateTreeDigest: attributed.Snapshot.Digest,
		SemanticDiffDigest: semanticDigest, OverlayGeneration: attributed.ThroughGeneration,
	}
	_, reviewDigest, err := encodePublicationReviewEnvelope(envelope)
	if err != nil {
		t.Fatal(err)
	}
	status.CandidateDigest = attributed.Snapshot.Digest
	status.OverlayGeneration = attributed.ThroughGeneration
	status.PublicationClassification = policy.Classification
	status.PublicationReviewDigest = reviewDigest
	input.Composed.status = status
	input.Review.composed.status = status
	input.Review.semanticDiff = semantic
	input.Review.semanticDiffDigest = semanticDigest
	input.Review.envelope = envelope
	input.Review.reviewDigest = reviewDigest
	input.Review.status = status
	input.Review.diff = WorkspaceDiff{
		SemanticDiff: semantic, CandidateDigest: attributed.Snapshot.Digest, OverlayGeneration: attributed.ThroughGeneration,
		PublicationClassification: policy.Classification, PublicationReviewDigest: reviewDigest,
	}
}

func mustCheckpointPlanSnapshot(t *testing.T, snapshot state.Snapshot) state.Snapshot {
	t.Helper()
	return mustCheckpointPlanDecodeTree(t, mustCheckpointPlanTree(t, snapshot))
}

func mustCheckpointPlanTree(t *testing.T, snapshot state.Snapshot) state.Tree {
	t.Helper()
	tree, err := state.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func mustCheckpointPlanDecodeTree(t *testing.T, tree state.Tree) state.Snapshot {
	t.Helper()
	snapshot, err := state.DecodeTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func mustCheckpointPlanDecodePriorTree(t *testing.T, value checkpointPriorTreeV1) state.Snapshot {
	t.Helper()
	tree := make(state.Tree, len(value.Files))
	for index, file := range value.Files {
		tree[index] = state.File{Path: file.Path, Data: bytes.Clone(file.Data)}
	}
	return mustCheckpointPlanDecodeTree(t, tree)
}

func mustProveCheckpointPlan(t *testing.T, input checkpointPlanInput) checkpointPlan {
	t.Helper()
	plan, err := proveCheckpointPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
