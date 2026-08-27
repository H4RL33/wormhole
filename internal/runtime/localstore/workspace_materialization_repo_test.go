package localstore

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

type materializationFixture struct {
	store                        *Store
	repo                         *WorkspaceRepo
	binding                      types.WorkspaceBinding
	priorTree, candidateTree     state.Tree
	priorDigest, candidateDigest state.Digest
}

func newMaterializationFixture(t *testing.T, materializationState string, included *string) *materializationFixture {
	t.Helper()
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	return makeMaterializationFixture(t, store, repo, binding, materializationState, included)
}

func makeMaterializationFixture(t *testing.T, store *Store, repo *WorkspaceRepo, binding types.WorkspaceBinding, materializationState string, included *string) *materializationFixture {
	t.Helper()
	priorTree := workspaceTree(t, binding.Scope.ProjectID, binding.Repository)
	priorSnapshot, err := state.DecodeTree(priorTree)
	if err != nil {
		t.Fatal(err)
	}
	candidateSnapshot := priorSnapshot
	candidateSnapshot.Project.Name = "Candidate"
	candidateSnapshot.Project.UpdatedAt = candidateSnapshot.Project.UpdatedAt.Add(time.Minute)
	candidateTree, err := state.EncodeTree(candidateSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	priorDigest, err := state.DigestTree(priorTree)
	if err != nil {
		t.Fatal(err)
	}
	candidateDigest, err := state.DigestTree(candidateTree)
	if err != nil {
		t.Fatal(err)
	}
	insertMaterializationRow(t, store, binding, "legacy-journal", materializationState, priorTree, candidateTree, priorDigest, candidateDigest, included)
	return &materializationFixture{store: store, repo: repo, binding: binding, priorTree: priorTree, candidateTree: candidateTree, priorDigest: priorDigest, candidateDigest: candidateDigest}
}

func insertMaterializationRow(t *testing.T, store *Store, binding types.WorkspaceBinding, journalID, materializationState string, priorTree, candidateTree state.Tree, priorDigest, candidateDigest state.Digest, included *string) {
	t.Helper()
	priorBytes, err := encodeFileList(priorTree)
	if err != nil {
		t.Fatal(err)
	}
	candidateBytes, err := encodeFileList(candidateTree)
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	review, priorCandidate := " review\n", " prior-candidate\n"
	mustExecMaterialization(t, store, `
		INSERT INTO workspace_materializations
		(project_id,workspace_id,journal_id,expected_live_digest,accepted_base_digest,
		 checkout_path,checkout_device,checkout_inode,prior_tree_digest,candidate_digest,
		 through_generation,prior_tree,candidate_tree,stage_path,backup_path,state,
		 created_at,updated_at,included_operations_json,publication_review_json,prior_candidate_json,publication_review_proof_version)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	`, binding.Scope.ProjectID, binding.Scope.WorkspaceID, journalID, priorDigest, binding.AcceptedTreeDigest,
		binding.Checkout.CanonicalPath, binding.Checkout.Device, binding.Checkout.Inode, priorDigest, candidateDigest,
		int64(3), priorBytes, candidateBytes, "/stage", "/backup", materializationState, created, created.Add(time.Second), included,
		review, priorCandidate, 1)
}

func mustExecMaterialization(t *testing.T, store *Store, query string, args ...any) {
	t.Helper()
	if _, err := store.DB().Exec(query, args...); err != nil {
		t.Fatalf("materialization SQL: %v", err)
	}
}

func validPreparedMaterialization(t *testing.T, binding types.WorkspaceBinding, journalID string) WorkspaceMaterializationRecord {
	t.Helper()
	priorTree := workspaceTree(t, binding.Scope.ProjectID, binding.Repository)
	priorDigest := digestWorkspaceTree(t, priorTree)
	if priorDigest != state.Digest(binding.AcceptedTreeDigest) {
		t.Fatalf("fixture prior digest=%q, binding=%q", priorDigest, binding.AcceptedTreeDigest)
	}
	snapshot, err := state.DecodeTree(priorTree)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Project.Name = "Prepared candidate"
	snapshot.Project.UpdatedAt = snapshot.Project.UpdatedAt.Add(time.Minute)
	candidateTree, err := state.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	operations, review, priorCandidate := "operation proof\n", "publication review\n", "prior candidate\n"
	parent := filepath.Join("/var/tmp", "wormhole-checkpoints")
	return WorkspaceMaterializationRecord{
		JournalID:                     journalID,
		ExpectedLiveDigest:            priorDigest,
		AcceptedBaseDigest:            priorDigest,
		Checkout:                      binding.Checkout,
		PriorTreeDigest:               priorDigest,
		CandidateDigest:               digestWorkspaceTree(t, candidateTree),
		ThroughGeneration:             0,
		PriorTree:                     priorTree,
		CandidateTree:                 candidateTree,
		StagePath:                     filepath.Join(parent, journalID+".stage"),
		BackupPath:                    filepath.Join(parent, journalID+".backup"),
		IncludedOperationsJSON:        &operations,
		PublicationReviewProofVersion: 1,
		PublicationReviewJSON:         &review,
		PriorCandidateJSON:            &priorCandidate,
		State:                         "prepared",
	}
}

func newMaterializationFixtureWithoutJournal(t *testing.T) *materializationFixture {
	t.Helper()
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	prepared := validPreparedMaterialization(t, binding, "00000000-0000-1000-8000-000000000051")
	return &materializationFixture{
		store: store, repo: repo, binding: binding,
		priorTree: prepared.PriorTree, candidateTree: prepared.CandidateTree,
		priorDigest: prepared.PriorTreeDigest, candidateDigest: prepared.CandidateDigest,
	}
}

func digestWorkspaceTree(t *testing.T, tree state.Tree) state.Digest {
	t.Helper()
	digest, err := state.DigestTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func equalWorkspaceMaterializationPublicForTest(left, right WorkspaceMaterializationRecord) bool {
	return equalWorkspaceMaterializationRecords(left, right)
}
