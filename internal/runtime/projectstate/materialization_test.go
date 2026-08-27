package projectstate

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestDecodeCheckpointOperationsCanonicalRoundTrip(t *testing.T) {
	operation := checkpointTestOperation(t, "99999999-9999-4999-8999-999999999991")
	want := CheckpointOperationsV1{
		SchemaVersion:            1,
		InitialThroughGeneration: 1,
		Operations: []CheckpointOperationV1{{
			Generation: 2, OperationID: operation.ID,
			OperationJSON: checkpointOperationJSON(t, operation), PrepublicationState: "active",
		}},
	}
	wantRaw, err := state.CanonicalJSON(want)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encodeCheckpointOperations(want)
	if err != nil {
		t.Fatal(err)
	}
	if raw != string(wantRaw) || !strings.HasSuffix(want.Operations[0].OperationJSON, "\n") {
		t.Fatalf("encoded envelope=%q, want exact canonical %q with inner LF", raw, wantRaw)
	}
	got, err := decodeCheckpointOperations(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded envelope=%+v, want %+v", got, want)
	}
}

func TestDecodeCheckpointOperationsRejectsEnvelopeAndOperationCorruption(t *testing.T) {
	first := checkpointTestOperation(t, "99999999-9999-4999-8999-999999999991")
	second := checkpointTestOperation(t, "99999999-9999-4999-8999-999999999992")
	valid := CheckpointOperationsV1{
		SchemaVersion:            1,
		InitialThroughGeneration: 1,
		Operations: []CheckpointOperationV1{
			{Generation: 1, OperationID: first.ID, OperationJSON: checkpointOperationJSON(t, first), PrepublicationState: "rebased"},
			{Generation: 2, OperationID: second.ID, OperationJSON: checkpointOperationJSON(t, second), PrepublicationState: "active"},
		},
	}
	tests := []struct {
		name string
		edit func(*CheckpointOperationsV1)
	}{
		{"nil operations", func(value *CheckpointOperationsV1) { value.Operations = nil }},
		{"unknown version", func(value *CheckpointOperationsV1) { value.SchemaVersion = 2 }},
		{"negative initial boundary", func(value *CheckpointOperationsV1) { value.InitialThroughGeneration = -1 }},
		{"zero generation", func(value *CheckpointOperationsV1) { value.Operations[0].Generation = 0 }},
		{"unordered generations", func(value *CheckpointOperationsV1) { value.Operations[1].Generation = 1 }},
		{"duplicate operation IDs", func(value *CheckpointOperationsV1) {
			value.Operations[1].OperationID = first.ID
			value.Operations[1].OperationJSON = checkpointOperationJSON(t, first)
		}},
		{"invalid operation ID", func(value *CheckpointOperationsV1) {
			value.Operations[0].OperationID = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
		}},
		{"unknown prepublication state", func(value *CheckpointOperationsV1) { value.Operations[0].PrepublicationState = "materialized" }},
		{"rebased above initial boundary", func(value *CheckpointOperationsV1) { value.Operations[1].PrepublicationState = "rebased" }},
		{"active at initial boundary", func(value *CheckpointOperationsV1) { value.Operations[0].PrepublicationState = "active" }},
		{"inner operation missing LF", func(value *CheckpointOperationsV1) {
			value.Operations[0].OperationJSON = strings.TrimSuffix(value.Operations[0].OperationJSON, "\n")
		}},
		{"altered inner operation bytes", func(value *CheckpointOperationsV1) {
			value.Operations[0].OperationJSON = strings.TrimSuffix(value.Operations[0].OperationJSON, "\n") + " \n"
		}},
		{"inner operation ID mismatch", func(value *CheckpointOperationsV1) { value.Operations[0].OperationID = second.ID }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			corrupt := cloneCheckpointEnvelope(valid)
			test.edit(&corrupt)
			raw, err := state.CanonicalJSON(corrupt)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := decodeCheckpointOperations(string(raw)); err == nil || !reflect.DeepEqual(got, CheckpointOperationsV1{}) {
				t.Fatalf("decode corrupt envelope=(%+v,%v), want zero error result", got, err)
			}
		})
	}

	raw, err := state.CanonicalJSON(valid)
	if err != nil {
		t.Fatal(err)
	}
	invalidRaw := []struct {
		name string
		raw  string
	}{
		{"unknown field", strings.TrimSuffix(string(raw), "}\n") + ",\"unknown\":true}\n"},
		{"trailing JSON", string(raw) + "{}\n"},
		{"noncanonical outer bytes", strings.TrimSuffix(string(raw), "\n")},
	}
	for _, test := range invalidRaw {
		t.Run(test.name, func(t *testing.T) {
			if got, err := decodeCheckpointOperations(test.raw); err == nil || !reflect.DeepEqual(got, CheckpointOperationsV1{}) {
				t.Fatalf("decode corrupt raw=(%+v,%v), want zero error result", got, err)
			}
		})
	}
}

func TestEncodeCheckpointOperationsRejectsInvalidEnvelopeThroughDecoder(t *testing.T) {
	for _, envelope := range []CheckpointOperationsV1{
		{SchemaVersion: 1, InitialThroughGeneration: 0, Operations: nil},
		{SchemaVersion: 2, InitialThroughGeneration: 0, Operations: []CheckpointOperationV1{}},
	} {
		if raw, err := encodeCheckpointOperations(envelope); err == nil || raw != "" {
			t.Fatalf("encode invalid envelope=(%q,%v), want empty error result", raw, err)
		}
	}
}

func TestRequireMatchingMaterializationAcceptsPublishedAndRecoveredNewWithoutAliasing(t *testing.T) {
	for _, journalState := range []string{"published", "recovered_new"} {
		t.Run(journalState, func(t *testing.T) {
			fixture := newCheckpointMaterializationFixture(t)
			journal := fixture.journal(t, "journal-a", journalState, 1, fixture.entries[:2])
			disposition := localstore.WorkspaceCurrentMaterialization{
				Journals:   []localstore.WorkspaceMaterializationRecord{cloneMaterializationRecord(journal)},
				Operations: fixture.rows("materialized", "materialized"),
			}
			proof, err := proveCurrentMaterializationWorkset(disposition)
			if err != nil {
				t.Fatal(err)
			}
			eligible := cloneMaterializationRecord(journal)
			match, err := requireMatchingMaterialization(proof, &eligible, fixture.binding, fixture.priorTree, fixture.candidateTree, fixture.candidateDigest)
			if err != nil {
				t.Fatal(err)
			}
			if match.journalID != journal.JournalID || match.throughGeneration != journal.ThroughGeneration ||
				match.includedOperationsJSON != *journal.IncludedOperationsJSON {
				t.Fatalf("match=%+v", match)
			}

			eligible.CandidateTree[0].Data[0] ^= 0xff
			*eligible.IncludedOperationsJSON = "changed"
			eligible.StagePath = "/eligible-stage"
			eligible.BackupPath = "/eligible-backup"
			eligible.PublicationReviewProofVersion = 0
			*eligible.PublicationReviewJSON = "eligible review"
			*eligible.PriorCandidateJSON = "eligible prior"
			disposition.Journals[0].PriorTree[0].Data[0] ^= 0xff
			disposition.Journals[0].StagePath = "/disposition-stage"
			disposition.Journals[0].BackupPath = "/disposition-backup"
			disposition.Journals[0].PublicationReviewProofVersion = 0
			*disposition.Journals[0].PublicationReviewJSON = "disposition review"
			*disposition.Journals[0].PriorCandidateJSON = "disposition prior"
			disposition.Operations[0].OperationJSON[0] ^= 0xff
			freshEligible := cloneMaterializationRecord(journal)
			fresh, err := requireMatchingMaterialization(proof, &freshEligible, fixture.binding, fixture.priorTree, fixture.candidateTree, fixture.candidateDigest)
			if err != nil || fresh != match {
				t.Fatalf("proof retained caller aliases: fresh=(%+v,%v), want %+v", fresh, err, match)
			}
		})
	}
}

func TestRequireMatchingMaterializationRejectsRecordBindingTreeAndDigestMismatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*checkpointMaterializationFixture, *localstore.WorkspaceMaterializationRecord, *types.WorkspaceBinding, *state.Tree, *state.Digest)
	}{
		{"checkout binding", func(_ *checkpointMaterializationFixture, _ *localstore.WorkspaceMaterializationRecord, binding *types.WorkspaceBinding, _ *state.Tree, _ *state.Digest) {
			binding.Checkout.Inode++
		}},
		{"accepted base binding", func(fixture *checkpointMaterializationFixture, _ *localstore.WorkspaceMaterializationRecord, binding *types.WorkspaceBinding, _ *state.Tree, _ *state.Digest) {
			binding.AcceptedTreeDigest = string(fixture.candidateDigest)
		}},
		{"project binding", func(_ *checkpointMaterializationFixture, _ *localstore.WorkspaceMaterializationRecord, binding *types.WorkspaceBinding, _ *state.Tree, _ *state.Digest) {
			binding.Scope.ProjectID = "66666666-6666-4666-8666-666666666666"
		}},
		{"repository binding", func(_ *checkpointMaterializationFixture, _ *localstore.WorkspaceMaterializationRecord, binding *types.WorkspaceBinding, _ *state.Tree, _ *state.Digest) {
			binding.Repository = types.RepositoryIdentity{Provider: "github", ImmutableID: "other", CanonicalRemote: "https://github.com/acme/other"}
		}},
		{"expected live differs from prior", func(fixture *checkpointMaterializationFixture, journal *localstore.WorkspaceMaterializationRecord, _ *types.WorkspaceBinding, _ *state.Tree, _ *state.Digest) {
			journal.ExpectedLiveDigest = fixture.candidateDigest
		}},
		{"noncanonical prior tree", func(_ *checkpointMaterializationFixture, journal *localstore.WorkspaceMaterializationRecord, _ *types.WorkspaceBinding, _ *state.Tree, _ *state.Digest) {
			journal.PriorTree[0], journal.PriorTree[1] = journal.PriorTree[1], journal.PriorTree[0]
		}},
		{"prior tree digest", func(fixture *checkpointMaterializationFixture, journal *localstore.WorkspaceMaterializationRecord, _ *types.WorkspaceBinding, _ *state.Tree, _ *state.Digest) {
			journal.PriorTree = checkpointCloneTree(fixture.candidateTree)
		}},
		{"candidate tree digest", func(fixture *checkpointMaterializationFixture, journal *localstore.WorkspaceMaterializationRecord, _ *types.WorkspaceBinding, _ *state.Tree, _ *state.Digest) {
			journal.CandidateTree = checkpointCloneTree(fixture.priorTree)
		}},
		{"noncanonical candidate tree", func(_ *checkpointMaterializationFixture, journal *localstore.WorkspaceMaterializationRecord, _ *types.WorkspaceBinding, _ *state.Tree, _ *state.Digest) {
			journal.CandidateTree[0], journal.CandidateTree[1] = journal.CandidateTree[1], journal.CandidateTree[0]
		}},
		{"persisted candidate digest", func(fixture *checkpointMaterializationFixture, journal *localstore.WorkspaceMaterializationRecord, _ *types.WorkspaceBinding, _ *state.Tree, _ *state.Digest) {
			journal.CandidateDigest = fixture.priorDigest
		}},
		{"captured candidate tree", func(fixture *checkpointMaterializationFixture, _ *localstore.WorkspaceMaterializationRecord, _ *types.WorkspaceBinding, candidate *state.Tree, _ *state.Digest) {
			*candidate = checkpointCloneTree(fixture.priorTree)
		}},
		{"captured candidate noncanonical", func(_ *checkpointMaterializationFixture, _ *localstore.WorkspaceMaterializationRecord, _ *types.WorkspaceBinding, candidate *state.Tree, _ *state.Digest) {
			(*candidate)[0], (*candidate)[1] = (*candidate)[1], (*candidate)[0]
		}},
		{"captured candidate digest", func(fixture *checkpointMaterializationFixture, _ *localstore.WorkspaceMaterializationRecord, _ *types.WorkspaceBinding, _ *state.Tree, digest *state.Digest) {
			*digest = fixture.priorDigest
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCheckpointMaterializationFixture(t)
			journal := fixture.journal(t, "journal-a", "published", 1, fixture.entries[:2])
			binding := fixture.binding
			prior := checkpointCloneTree(fixture.priorTree)
			candidate := checkpointCloneTree(fixture.candidateTree)
			candidateDigest := fixture.candidateDigest
			test.mutate(&fixture, &journal, &binding, &candidate, &candidateDigest)
			proof, err := proveCurrentMaterializationWorkset(localstore.WorkspaceCurrentMaterialization{
				Journals:   []localstore.WorkspaceMaterializationRecord{cloneMaterializationRecord(journal)},
				Operations: fixture.rows("materialized", "materialized"),
			})
			if err != nil {
				t.Fatalf("ownership prerequisite unexpectedly failed: %v", err)
			}
			eligible := cloneMaterializationRecord(journal)
			if match, err := requireMatchingMaterialization(proof, &eligible, binding, prior, candidate, candidateDigest); err == nil || match != (matchingMaterializationProof{}) {
				t.Fatalf("mismatched materialization=(%+v,%v), want zero error result", match, err)
			}
		})
	}

	for _, test := range []struct {
		name   string
		mutate func(*localstore.WorkspaceMaterializationRecord)
	}{
		{"included operations", func(record *localstore.WorkspaceMaterializationRecord) { *record.IncludedOperationsJSON += " " }},
		{"stage path", func(record *localstore.WorkspaceMaterializationRecord) { record.StagePath = "/other-stage" }},
		{"backup path", func(record *localstore.WorkspaceMaterializationRecord) { record.BackupPath = "/other-backup" }},
		{"proof version", func(record *localstore.WorkspaceMaterializationRecord) { record.PublicationReviewProofVersion = 2 }},
		{"publication review", func(record *localstore.WorkspaceMaterializationRecord) { *record.PublicationReviewJSON += " " }},
		{"prior candidate", func(record *localstore.WorkspaceMaterializationRecord) { *record.PriorCandidateJSON += " " }},
	} {
		t.Run("separate eligible "+test.name+" differs from proof", func(t *testing.T) {
			fixture := newCheckpointMaterializationFixture(t)
			journal := fixture.journal(t, "journal-a", "published", 1, fixture.entries[:2])
			proof, err := proveCurrentMaterializationWorkset(localstore.WorkspaceCurrentMaterialization{
				Journals: []localstore.WorkspaceMaterializationRecord{journal}, Operations: fixture.rows("materialized", "materialized"),
			})
			if err != nil {
				t.Fatal(err)
			}
			eligible := cloneMaterializationRecord(journal)
			test.mutate(&eligible)
			if _, err := requireMatchingMaterialization(proof, &eligible, fixture.binding, fixture.priorTree, fixture.candidateTree, fixture.candidateDigest); err == nil {
				t.Fatal("eligible record differing from proof succeeded")
			}
		})
	}

	t.Run("unknown proof journal", func(t *testing.T) {
		fixture := newCheckpointMaterializationFixture(t)
		journal := fixture.journal(t, "journal-a", "published", 1, fixture.entries[:2])
		proof, err := proveCurrentMaterializationWorkset(localstore.WorkspaceCurrentMaterialization{
			Journals: []localstore.WorkspaceMaterializationRecord{journal}, Operations: fixture.rows("materialized", "materialized"),
		})
		if err != nil {
			t.Fatal(err)
		}
		eligible := cloneMaterializationRecord(journal)
		eligible.JournalID = "journal-missing"
		if _, err := requireMatchingMaterialization(proof, &eligible, fixture.binding, fixture.priorTree, fixture.candidateTree, fixture.candidateDigest); err == nil {
			t.Fatal("unknown proof journal succeeded")
		}
	})

	t.Run("nil eligible and zero proof", func(t *testing.T) {
		fixture := newCheckpointMaterializationFixture(t)
		if match, err := requireMatchingMaterialization(currentMaterializationProof{}, nil, fixture.binding, fixture.priorTree, fixture.candidateTree, fixture.candidateDigest); err == nil || match != (matchingMaterializationProof{}) {
			t.Fatalf("unavailable matching proof=(%+v,%v)", match, err)
		}
	})

	for _, testName := range []string{"captured prior mismatch", "captured prior noncanonical"} {
		t.Run(testName, func(t *testing.T) {
			fixture := newCheckpointMaterializationFixture(t)
			journal := fixture.journal(t, "journal-a", "published", 1, fixture.entries[:2])
			proof, err := proveCurrentMaterializationWorkset(localstore.WorkspaceCurrentMaterialization{
				Journals: []localstore.WorkspaceMaterializationRecord{journal}, Operations: fixture.rows("materialized", "materialized"),
			})
			if err != nil {
				t.Fatal(err)
			}
			prior := checkpointCloneTree(fixture.priorTree)
			if testName == "captured prior mismatch" {
				prior = checkpointCloneTree(fixture.candidateTree)
			} else {
				prior[0], prior[1] = prior[1], prior[0]
			}
			eligible := cloneMaterializationRecord(journal)
			if _, err := requireMatchingMaterialization(proof, &eligible, fixture.binding, prior, fixture.candidateTree, fixture.candidateDigest); err == nil {
				t.Fatal("mismatched captured prior succeeded")
			}
		})
	}
}

func TestWorkspaceMaterializationProofRejectsV0PendingJournal(t *testing.T) {
	fixture := newCheckpointMaterializationFixture(t)
	journal := fixture.journal(t, "journal-a", "published", 1, fixture.entries[:2])
	journal.PublicationReviewProofVersion = 0
	journal.PublicationReviewJSON = nil
	journal.PriorCandidateJSON = nil
	if _, err := proveCurrentMaterializationWorkset(localstore.WorkspaceCurrentMaterialization{
		Journals: []localstore.WorkspaceMaterializationRecord{journal}, Operations: fixture.rows("materialized", "materialized"),
	}); err == nil {
		t.Fatal("v0 published materialization proof succeeded")
	}
}

func checkpointTestOperation(t *testing.T, id string) state.OperationV1 {
	t.Helper()
	snapshot := composeFixtureSnapshot(t)
	return composeTaskOperation(snapshot, id, func(*state.TaskV1) {})
}

func checkpointOperationJSON(t *testing.T, operation state.OperationV1) string {
	t.Helper()
	canonical, err := state.CanonicalOperation(operation)
	if err != nil {
		t.Fatal(err)
	}
	return string(canonical)
}

func cloneCheckpointEnvelope(value CheckpointOperationsV1) CheckpointOperationsV1 {
	cloned := value
	cloned.Operations = append([]CheckpointOperationV1(nil), value.Operations...)
	return cloned
}

type checkpointMaterializationFixture struct {
	binding                      types.WorkspaceBinding
	priorTree, candidateTree     state.Tree
	priorDigest, candidateDigest state.Digest
	entries                      []CheckpointOperationV1
}

func newCheckpointMaterializationFixture(t *testing.T) checkpointMaterializationFixture {
	t.Helper()
	prior := composeFixtureSnapshot(t)
	priorTree, err := state.EncodeTree(prior)
	if err != nil {
		t.Fatal(err)
	}
	candidate := composeCloneSnapshot(t, prior)
	candidate.Project.Name = "Checkpoint candidate"
	candidate.Project.UpdatedAt = candidate.Project.UpdatedAt.Add(time.Minute)
	candidateTree, err := state.EncodeTree(candidate)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err = state.DecodeTree(candidateTree)
	if err != nil {
		t.Fatal(err)
	}
	operationIDs := []string{
		"99999999-9999-4999-8999-999999999991",
		"99999999-9999-4999-8999-999999999992",
		"99999999-9999-4999-8999-999999999993",
	}
	entries := make([]CheckpointOperationV1, len(operationIDs))
	for index, id := range operationIDs {
		operation := checkpointTestOperation(t, id)
		generation := int64(index + 1)
		prestate := "active"
		if generation == 1 {
			prestate = "rebased"
		}
		entries[index] = CheckpointOperationV1{
			Generation: generation, OperationID: id,
			OperationJSON: checkpointOperationJSON(t, operation), PrepublicationState: prestate,
		}
	}
	return checkpointMaterializationFixture{
		binding: types.WorkspaceBinding{
			Scope: types.WorkspaceScope{
				ProjectID:   prior.Config.ProjectID,
				WorkspaceID: "77777777-7777-4777-8777-777777777777",
			},
			Checkout:   types.CheckoutIdentity{CanonicalPath: "/checkout", Device: 1, Inode: 2},
			Repository: prior.Config.Repository, AcceptedRef: "refs/heads/main",
			AcceptedCommitSHA: strings.Repeat("a", 40), AcceptedTreeDigest: string(prior.Digest),
		},
		priorTree: priorTree, candidateTree: candidateTree,
		priorDigest: prior.Digest, candidateDigest: candidate.Digest,
		entries: entries,
	}
}

func (fixture checkpointMaterializationFixture) journal(t *testing.T, journalID, journalState string, initial int64, entries []CheckpointOperationV1) localstore.WorkspaceMaterializationRecord {
	t.Helper()
	if entries == nil {
		entries = []CheckpointOperationV1{}
	}
	envelope := CheckpointOperationsV1{SchemaVersion: 1, InitialThroughGeneration: initial, Operations: make([]CheckpointOperationV1, len(entries))}
	copy(envelope.Operations, entries)
	raw, err := encodeCheckpointOperations(envelope)
	if err != nil {
		t.Fatal(err)
	}
	through := initial
	if len(entries) != 0 && entries[len(entries)-1].Generation > through {
		through = entries[len(entries)-1].Generation
	}
	return localstore.WorkspaceMaterializationRecord{
		JournalID: journalID, ExpectedLiveDigest: fixture.priorDigest,
		AcceptedBaseDigest: fixture.priorDigest, Checkout: fixture.binding.Checkout,
		PriorTreeDigest: fixture.priorDigest, CandidateDigest: fixture.candidateDigest,
		ThroughGeneration: through, PriorTree: checkpointCloneTree(fixture.priorTree),
		CandidateTree: checkpointCloneTree(fixture.candidateTree), IncludedOperationsJSON: &raw,
		StagePath: "/checkpoint-stage", BackupPath: "/checkpoint-backup",
		PublicationReviewProofVersion: 1, PublicationReviewJSON: stringPointer(" review\n"), PriorCandidateJSON: stringPointer(" prior\n"),
		State: journalState,
	}
}

func stringPointer(value string) *string { return &value }

func (fixture checkpointMaterializationFixture) rows(states ...string) []localstore.WorkspaceOperation {
	rows := make([]localstore.WorkspaceOperation, len(states))
	for index, rowState := range states {
		entry := fixture.entries[index]
		rows[index] = localstore.WorkspaceOperation{
			Generation: entry.Generation, OperationID: entry.OperationID,
			OperationJSON: []byte(entry.OperationJSON), State: rowState,
		}
	}
	return rows
}

func checkpointCloneTree(tree state.Tree) state.Tree {
	cloned := make(state.Tree, len(tree))
	for index, file := range tree {
		cloned[index] = state.File{Path: file.Path, Data: bytes.Clone(file.Data)}
	}
	return cloned
}
