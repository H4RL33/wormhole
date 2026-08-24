package localstore

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
	"modernc.org/sqlite"
)

func TestWorkspaceMutationTxPrepareMaterializationPersistsExactOwnedJournal(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	prepared := validPreparedMaterialization(t, binding, "00000000-0000-1000-8000-000000000051")
	input := cloneWorkspaceMaterializationRecord(prepared)

	historyProof := "legacy operations\n"
	insertMaterializationRow(t, store, binding, "accepted-history", "accepted", prepared.PriorTree, prepared.CandidateTree,
		prepared.PriorTreeDigest, prepared.CandidateDigest, &historyProof)
	insertMaterializationRow(t, store, binding, "recovered-history", "recovered_old", prepared.PriorTree, prepared.CandidateTree,
		prepared.PriorTreeDigest, prepared.CandidateDigest, nil)
	insertWorkspaceOperation(t, store, binding.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "active")
	candidate := workspaceCandidateRecord(t, binding, false, 0)
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.UpsertCandidate(context.Background(), candidate)
	}); err != nil {
		t.Fatal(err)
	}
	beforeOperations := readWorkspaceOperations(t, store, binding.Scope)

	var got WorkspaceMaterializationRecord
	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		var err error
		got, err = tx.PrepareMaterialization(context.Background(), prepared)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prepared, input) {
		t.Fatalf("prepare mutated input: got %+v want %+v", prepared, input)
	}
	if !equalWorkspaceMaterializationPublicForTest(got, prepared) || !validWorkspaceMaterializationMutationMetadata(got.mutationMetadata) {
		t.Fatalf("prepared journal=%+v", got)
	}
	if got.mutationMetadata.CreatedRaw == "" || got.mutationMetadata.UpdatedRaw == "" {
		t.Fatalf("missing SQLite timestamp evidence: %+v", got.mutationMetadata)
	}
	if got.IncludedOperationsJSON == prepared.IncludedOperationsJSON || got.PublicationReviewJSON == prepared.PublicationReviewJSON ||
		got.PriorCandidateJSON == prepared.PriorCandidateJSON || &got.PriorTree[0].Data[0] == &prepared.PriorTree[0].Data[0] ||
		&got.CandidateTree[0].Data[0] == &prepared.CandidateTree[0].Data[0] {
		t.Fatal("prepare result aliases input")
	}
	got.PriorTree[0].Data[0] ^= 0xff
	got.CandidateTree[0].Data[0] ^= 0xff
	*got.IncludedOperationsJSON = "mutated"
	*got.PublicationReviewJSON = "mutated"
	*got.PriorCandidateJSON = "mutated"
	if !reflect.DeepEqual(readWorkspaceOperations(t, store, binding.Scope), beforeOperations) {
		t.Fatal("prepare changed adjacent operations")
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	after := readMaterializationDisposition(t, NewWorkspaceRepo(restarted.DB()), binding.Scope)
	if len(after.Journals) != 3 {
		t.Fatalf("restart journals=%d, want 3", len(after.Journals))
	}
	var persisted *WorkspaceMaterializationRecord
	for index := range after.Journals {
		if after.Journals[index].JournalID == prepared.JournalID {
			persisted = &after.Journals[index]
		}
	}
	if persisted == nil || !equalWorkspaceMaterializationPublicForTest(*persisted, prepared) {
		t.Fatalf("restart prepared journal=%+v", persisted)
	}
}

func TestWorkspaceMutationTxPrepareMaterializationRollbackAndScopeIsolation(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	a := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	b := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	c := createBinding(t, repo, "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000013", "/checkout-c", 3, 13)
	insertMaterializationRow(t, store, b, "sibling-b", "accepted", workspaceTree(t, b.Scope.ProjectID, b.Repository),
		workspaceTree(t, b.Scope.ProjectID, b.Repository), state.Digest(b.AcceptedTreeDigest), state.Digest(b.AcceptedTreeDigest), nil)
	insertMaterializationRow(t, store, c, "sibling-c", "recovered_old", workspaceTree(t, c.Scope.ProjectID, c.Repository),
		workspaceTree(t, c.Scope.ProjectID, c.Repository), state.Digest(c.AcceptedTreeDigest), state.Digest(c.AcceptedTreeDigest), nil)
	before := readAtomicWorkspaceRawSnapshot(t, store.DB())
	wantErr := errors.New("rollback prepared journal")
	prepared := validPreparedMaterialization(t, a, "00000000-0000-1000-8000-000000000051")
	err := repo.WithImmediateWorkspace(context.Background(), a.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.PrepareMaterialization(context.Background(), prepared)
		if err != nil || got.JournalID != prepared.JournalID {
			t.Fatalf("prepare before callback rollback=(%+v,%v)", got, err)
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("rollback error=%v, want %v", err, wantErr)
	}
	if after := readAtomicWorkspaceRawSnapshot(t, store.DB()); !reflect.DeepEqual(after, before) {
		t.Fatalf("callback rollback changed raw state: got %#v want %#v", after, before)
	}
	if err := repo.WithImmediateWorkspace(context.Background(), a.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.PrepareMaterialization(context.Background(), prepared)
		return err
	}); err != nil {
		t.Fatalf("writer not reusable after rollback: %v", err)
	}
	if got := readMaterializationDisposition(t, repo, b.Scope); len(got.Journals) != 1 || got.Journals[0].JournalID != "sibling-b" {
		t.Fatalf("workspace B changed: %+v", got)
	}
	if got := readMaterializationDisposition(t, repo, c.Scope); len(got.Journals) != 1 || got.Journals[0].JournalID != "sibling-c" {
		t.Fatalf("project C changed: %+v", got)
	}
}

func TestWorkspaceMutationTxPrepareMaterializationRejectsInvalidRecord(t *testing.T) {
	badDigest := state.Digest("sha256:" + strings.Repeat("f", 64))
	mutations := []struct {
		name   string
		mutate func(*testing.T, types.WorkspaceBinding, *WorkspaceMaterializationRecord)
	}{
		{"state published", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) { r.State = "published" }},
		{"state accepted", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) { r.State = "accepted" }},
		{"state recovered old", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			r.State = "recovered_old"
		}},
		{"state recovered new", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			r.State = "recovered_new"
		}},
		{"empty UUID", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) { r.JournalID = "" }},
		{"zero UUID", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			r.JournalID = "00000000-0000-0000-0000-000000000000"
		}},
		{"uppercase UUID", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			r.JournalID = "00000000-0000-1000-8000-00000000005A"
		}},
		{"expected digest", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			r.ExpectedLiveDigest = badDigest
		}},
		{"accepted digest", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			r.AcceptedBaseDigest = badDigest
		}},
		{"prior digest", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			r.PriorTreeDigest = badDigest
		}},
		{"candidate digest", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			r.CandidateDigest = badDigest
		}},
		{"checkout path", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			r.Checkout.CanonicalPath = "/other"
		}},
		{"checkout device", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) { r.Checkout.Device++ }},
		{"checkout inode", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) { r.Checkout.Inode++ }},
		{"negative generation", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			r.ThroughGeneration = -1
		}},
		{"prior tree bytes", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			r.PriorTree[0].Data = append(r.PriorTree[0].Data, ' ')
		}},
		{"candidate tree bytes", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			r.CandidateTree[0].Data = append(r.CandidateTree[0].Data, ' ')
		}},
		{"noncanonical prior order", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			r.PriorTree[0], r.PriorTree[1] = r.PriorTree[1], r.PriorTree[0]
		}},
		{"candidate project", func(t *testing.T, b types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			r.CandidateTree = workspaceTree(t, "00000000-0000-4000-8000-000000000099", b.Repository)
			r.CandidateDigest = digestWorkspaceTree(t, r.CandidateTree)
		}},
		{"candidate repository", func(t *testing.T, b types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			repository := types.RepositoryIdentity{Provider: "github", ImmutableID: "repository-other", CanonicalRemote: "https://github.com/acme/other"}
			r.CandidateTree = workspaceTree(t, b.Scope.ProjectID, repository)
			r.CandidateDigest = digestWorkspaceTree(t, r.CandidateTree)
		}},
		{"relative stage", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			r.StagePath = "relative.stage"
		}},
		{"dirty stage", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			r.StagePath = "/tmp/../bad.stage"
		}},
		{"same paths", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			r.BackupPath = r.StagePath
		}},
		{"unequal parents", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			r.BackupPath = filepath.Join("/other", r.JournalID+".backup")
		}},
		{"wrong stage basename", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			r.StagePath = filepath.Join(filepath.Dir(r.StagePath), "wrong.stage")
		}},
		{"wrong backup basename", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			r.BackupPath = filepath.Join(filepath.Dir(r.BackupPath), "wrong.backup")
		}},
		{"private metadata", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			r.mutationMetadata.StageClass = "text"
		}},
		{"nil operations", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			r.IncludedOperationsJSON = nil
		}},
		{"empty operations", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			v := ""
			r.IncludedOperationsJSON = &v
		}},
		{"invalid UTF8 operations", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			v := string([]byte{0xff})
			r.IncludedOperationsJSON = &v
		}},
		{"NUL operations", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			v := "ops\x00"
			r.IncludedOperationsJSON = &v
		}},
		{"proof version zero", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			r.PublicationReviewProofVersion = 0
		}},
		{"proof version two", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			r.PublicationReviewProofVersion = 2
		}},
		{"nil review", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			r.PublicationReviewJSON = nil
		}},
		{"empty review", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			v := ""
			r.PublicationReviewJSON = &v
		}},
		{"invalid UTF8 review", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			v := string([]byte{0xff})
			r.PublicationReviewJSON = &v
		}},
		{"NUL review", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			v := "review\x00"
			r.PublicationReviewJSON = &v
		}},
		{"nil prior candidate", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			r.PriorCandidateJSON = nil
		}},
		{"empty prior candidate", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			v := ""
			r.PriorCandidateJSON = &v
		}},
		{"invalid UTF8 prior candidate", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			v := string([]byte{0xff})
			r.PriorCandidateJSON = &v
		}},
		{"NUL prior candidate", func(_ *testing.T, _ types.WorkspaceBinding, r *WorkspaceMaterializationRecord) {
			v := "prior\x00"
			r.PriorCandidateJSON = &v
		}},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			store, repo := openWorkspaceStore(t)
			binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
			prepared := validPreparedMaterialization(t, binding, "00000000-0000-1000-8000-000000000051")
			test.mutate(t, binding, &prepared)
			before := readAtomicWorkspaceRawSnapshot(t, store.DB())
			err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				got, err := tx.PrepareMaterialization(context.Background(), prepared)
				if err == nil || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
					t.Fatalf("invalid prepare=(%+v,%v), want zero,error", got, err)
				}
				return err
			})
			if err == nil {
				t.Fatal("invalid prepared record succeeded")
			}
			if after := readAtomicWorkspaceRawSnapshot(t, store.DB()); !reflect.DeepEqual(after, before) {
				t.Fatalf("invalid prepare changed state: got %#v want %#v", after, before)
			}
		})
	}
}

func TestWorkspaceMutationTxPrepareMaterializationRejectsInvalidTransactionAndHistory(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	prepared := validPreparedMaterialization(t, binding, "00000000-0000-1000-8000-000000000051")
	var nilTx *WorkspaceMutationTx
	for name, tx := range map[string]*WorkspaceMutationTx{"nil": nilTx, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			if got, err := tx.PrepareMaterialization(context.Background(), prepared); !errors.Is(err, ErrNotFound) || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
				t.Fatalf("invalid tx prepare=(%+v,%v), want zero,ErrNotFound", got, err)
			}
		})
	}
	conn, err := store.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := (&WorkspaceMutationTx{conn: conn, scope: types.WorkspaceScope{}}).PrepareMaterialization(context.Background(), prepared); !errors.Is(err, ErrNotFound) || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
		t.Fatalf("invalid-scope prepare=(%+v,%v)", got, err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := (&WorkspaceMutationTx{conn: conn, scope: binding.Scope}).PrepareMaterialization(context.Background(), prepared); !errors.Is(err, ErrNotFound) || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
		t.Fatalf("closed-connection prepare=(%+v,%v)", got, err)
	}
	var retained *WorkspaceMutationTx
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error { retained = tx; return nil }); err != nil {
		t.Fatal(err)
	}
	if got, err := retained.PrepareMaterialization(context.Background(), prepared); !errors.Is(err, ErrNotFound) || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
		t.Fatalf("retained-closed prepare=(%+v,%v)", got, err)
	}
	conn, err = store.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	unregistered := &WorkspaceMutationTx{conn: conn, scope: types.WorkspaceScope{
		ProjectID: "00000000-0000-4000-8000-000000000099", WorkspaceID: "00000000-0000-4000-8000-000000000098",
	}}
	if got, err := unregistered.PrepareMaterialization(context.Background(), prepared); !errors.Is(err, ErrNotFound) || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
		t.Fatalf("unregistered prepare=(%+v,%v)", got, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := (&WorkspaceMutationTx{conn: conn, scope: binding.Scope}).PrepareMaterialization(canceled, prepared); err == nil || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
		t.Fatalf("canceled prepare=(%+v,%v)", got, err)
	}

	for _, existingState := range []string{"prepared", "published", "recovered_new"} {
		t.Run("pending "+existingState, func(t *testing.T) {
			fixture := newMaterializationFixture(t, existingState, stringPointerForTest("proof\n"))
			candidate := validPreparedMaterialization(t, fixture.binding, "00000000-0000-1000-8000-000000000051")
			before := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB())
			err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				got, err := tx.PrepareMaterialization(context.Background(), candidate)
				if err == nil || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
					t.Fatalf("pending prepare=(%+v,%v)", got, err)
				}
				return err
			})
			if err == nil {
				t.Fatal("prepare beside pending journal succeeded")
			}
			if after := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB()); !reflect.DeepEqual(after, before) {
				t.Fatal("pending rejection mutated state")
			}
		})
	}

	t.Run("duplicate terminal journal ID", func(t *testing.T) {
		fixture := newMaterializationFixture(t, "accepted", nil)
		candidate := validPreparedMaterialization(t, fixture.binding, "legacy-journal")
		before := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB())
		err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
			got, err := tx.PrepareMaterialization(context.Background(), candidate)
			if err == nil || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
				t.Fatalf("duplicate prepare=(%+v,%v)", got, err)
			}
			return err
		})
		if err == nil {
			t.Fatal("duplicate journal succeeded")
		}
		if after := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB()); !reflect.DeepEqual(after, before) {
			t.Fatal("duplicate rejection mutated state")
		}
	})

	for _, states := range [][]string{{"prepared", "prepared"}, {"prepared", "published"}, {"prepared", "recovered_new"}} {
		name := strings.Join(states, "+")
		t.Run(name, func(t *testing.T) {
			fixture := newMaterializationFixture(t, states[0], stringPointerForTest("proof\n"))
			if _, err := fixture.store.DB().Exec(`DROP INDEX workspace_one_current_materialization`); err != nil {
				t.Fatal(err)
			}
			insertMaterializationRow(t, fixture.store, fixture.binding, "second-pending", states[1], fixture.priorTree, fixture.candidateTree, fixture.priorDigest, fixture.candidateDigest, stringPointerForTest("proof\n"))
			candidate := validPreparedMaterialization(t, fixture.binding, "00000000-0000-1000-8000-000000000051")
			before := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB())
			err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				_, err := tx.PrepareMaterialization(context.Background(), candidate)
				return err
			})
			if err == nil {
				t.Fatal("prepare beside corrupt pending set succeeded")
			}
			if after := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB()); !reflect.DeepEqual(after, before) {
				t.Fatal("corrupt pending rejection mutated state")
			}
		})
	}
}

func TestWorkspaceMutationTxPrepareMaterializationRejectsDuplicateAndCorruptHistory(t *testing.T) {
	t.Run("logical duplicate terminal journal", func(t *testing.T) {
		fixture := newMaterializationFixture(t, "accepted", nil)
		mustExecMaterialization(t, fixture.store, `
			INSERT INTO workspace_materializations
			(project_id,workspace_id,journal_id,expected_live_digest,accepted_base_digest,
			 checkout_path,checkout_device,checkout_inode,prior_tree_digest,candidate_digest,
			 through_generation,prior_tree,candidate_tree,stage_path,backup_path,state,
			 created_at,updated_at,included_operations_json,publication_review_json,prior_candidate_json,publication_review_proof_version)
			SELECT project_id,workspace_id,CAST(journal_id AS BLOB),expected_live_digest,accepted_base_digest,
			 checkout_path,checkout_device,checkout_inode,prior_tree_digest,candidate_digest,
			 through_generation,prior_tree,candidate_tree,stage_path,backup_path,state,
			 created_at,updated_at,included_operations_json,publication_review_json,prior_candidate_json,publication_review_proof_version
			FROM workspace_materializations WHERE journal_id='legacy-journal'
		`)
		prepared := validPreparedMaterialization(t, fixture.binding, "00000000-0000-1000-8000-000000000051")
		before := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB())
		err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
			got, err := tx.PrepareMaterialization(context.Background(), prepared)
			if err == nil || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
				t.Fatalf("duplicate-history prepare=(%+v,%v)", got, err)
			}
			return err
		})
		if err == nil {
			t.Fatal("prepare accepted logical duplicate history")
		}
		if after := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB()); !reflect.DeepEqual(after, before) {
			t.Fatal("duplicate-history rejection mutated state")
		}
	})

	for _, test := range []struct {
		name string
		seed func(*testing.T, *materializationFixture)
	}{
		{"corrupt operation", func(t *testing.T, f *materializationFixture) {
			insertWorkspaceOperationRaw(t, f.store, f.binding.Scope, 1, "00000000-0000-4000-8000-000000000091", []byte("{}"), "active")
		}},
		{"corrupt candidate", func(t *testing.T, f *materializationFixture) {
			candidate := workspaceCandidateRecord(t, f.binding, false, 0)
			if err := f.repo.WithImmediateWorkspace(context.Background(), f.binding.Scope, func(tx *WorkspaceMutationTx) error {
				return tx.UpsertCandidate(context.Background(), candidate)
			}); err != nil {
				t.Fatal(err)
			}
			mustExecMaterialization(t, f.store, `UPDATE workspace_candidates SET imported_by='invalid'`)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMaterializationFixtureWithoutJournal(t)
			test.seed(t, fixture)
			prepared := validPreparedMaterialization(t, fixture.binding, "00000000-0000-1000-8000-000000000051")
			before := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB())
			err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				got, err := tx.PrepareMaterialization(context.Background(), prepared)
				if err == nil || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
					t.Fatalf("corrupt-adjacency prepare=(%+v,%v)", got, err)
				}
				return err
			})
			if err == nil {
				t.Fatal("prepare accepted corrupt adjacency")
			}
			if after := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB()); !reflect.DeepEqual(after, before) {
				t.Fatal("corrupt-adjacency rejection mutated state")
			}
		})
	}
}

func TestWorkspaceMutationTxPrepareMaterializationDetectsSQLAndAdjacentDrift(t *testing.T) {
	for _, test := range []struct {
		name    string
		seed    func(*testing.T, *materializationFixture)
		trigger string
	}{
		{"abort", nil, `CREATE TRIGGER prepare_abort BEFORE INSERT ON workspace_materializations BEGIN SELECT RAISE(ABORT,'prepare abort'); END`},
		{"ignore", nil, `CREATE TRIGGER prepare_ignore BEFORE INSERT ON workspace_materializations BEGIN SELECT RAISE(IGNORE); END`},
		{"inserted row drift", nil, `CREATE TRIGGER prepare_target_drift AFTER INSERT ON workspace_materializations BEGIN UPDATE workspace_materializations SET through_generation=through_generation+1 WHERE rowid=NEW.rowid; END`},
		{"invalid returned timestamp evidence", nil, `CREATE TRIGGER prepare_time_drift AFTER INSERT ON workspace_materializations BEGIN UPDATE workspace_materializations SET created_at='2099-01-01 00:00:00+00:00' WHERE rowid=NEW.rowid; END`},
		{"history drift", func(t *testing.T, f *materializationFixture) {
			insertMaterializationRow(t, f.store, f.binding, "history", "accepted", f.priorTree, f.candidateTree, f.priorDigest, f.candidateDigest, nil)
		}, `CREATE TRIGGER prepare_history_drift AFTER INSERT ON workspace_materializations WHEN NEW.journal_id!='history' BEGIN UPDATE workspace_materializations SET through_generation=through_generation+1 WHERE journal_id='history'; END`},
		{"operation drift", func(t *testing.T, f *materializationFixture) {
			insertWorkspaceOperation(t, f.store, f.binding.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "active")
		}, `CREATE TRIGGER prepare_operation_drift AFTER INSERT ON workspace_materializations BEGIN UPDATE workspace_overlay_operations SET state='discarded' WHERE project_id=NEW.project_id AND workspace_id=NEW.workspace_id; END`},
		{"operation timestamp raw drift", func(t *testing.T, f *materializationFixture) {
			insertWorkspaceOperation(t, f.store, f.binding.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "active")
		}, `CREATE TRIGGER prepare_operation_time_raw_drift AFTER INSERT ON workspace_materializations BEGIN UPDATE workspace_overlay_operations SET created_at=strftime('%Y-%m-%dT%H:%M:%SZ',created_at) WHERE project_id=NEW.project_id AND workspace_id=NEW.workspace_id; END`},
		{"operation timestamp storage drift", func(t *testing.T, f *materializationFixture) {
			insertWorkspaceOperation(t, f.store, f.binding.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "active")
		}, `CREATE TRIGGER prepare_operation_time_storage_drift AFTER INSERT ON workspace_materializations BEGIN UPDATE workspace_overlay_operations SET created_at=CAST(created_at AS BLOB) WHERE project_id=NEW.project_id AND workspace_id=NEW.workspace_id; END`},
		{"candidate drift", func(t *testing.T, f *materializationFixture) {
			candidate := workspaceCandidateRecord(t, f.binding, false, 0)
			if err := f.repo.WithImmediateWorkspace(context.Background(), f.binding.Scope, func(tx *WorkspaceMutationTx) error { return tx.UpsertCandidate(context.Background(), candidate) }); err != nil {
				t.Fatal(err)
			}
		}, `CREATE TRIGGER prepare_candidate_drift AFTER INSERT ON workspace_materializations BEGIN UPDATE workspace_candidates SET imported_by='00000000-0000-4000-8000-000000000072' WHERE project_id=NEW.project_id AND workspace_id=NEW.workspace_id; END`},
		{"candidate tree storage drift", func(t *testing.T, f *materializationFixture) {
			candidate := workspaceCandidateRecord(t, f.binding, false, 0)
			if err := f.repo.WithImmediateWorkspace(context.Background(), f.binding.Scope, func(tx *WorkspaceMutationTx) error { return tx.UpsertCandidate(context.Background(), candidate) }); err != nil {
				t.Fatal(err)
			}
		}, `CREATE TRIGGER prepare_candidate_tree_storage_drift AFTER INSERT ON workspace_materializations BEGIN UPDATE workspace_candidates SET direct_tree=CAST(direct_tree AS TEXT) WHERE project_id=NEW.project_id AND workspace_id=NEW.workspace_id; END`},
		{"candidate timestamp raw drift", func(t *testing.T, f *materializationFixture) {
			candidate := workspaceCandidateRecord(t, f.binding, false, 0)
			if err := f.repo.WithImmediateWorkspace(context.Background(), f.binding.Scope, func(tx *WorkspaceMutationTx) error { return tx.UpsertCandidate(context.Background(), candidate) }); err != nil {
				t.Fatal(err)
			}
		}, `CREATE TRIGGER prepare_candidate_time_raw_drift AFTER INSERT ON workspace_materializations BEGIN UPDATE workspace_candidates SET imported_at='2026-07-28T14:00:00Z' WHERE project_id=NEW.project_id AND workspace_id=NEW.workspace_id; END`},
		{"candidate timestamp storage drift", func(t *testing.T, f *materializationFixture) {
			candidate := workspaceCandidateRecord(t, f.binding, false, 0)
			if err := f.repo.WithImmediateWorkspace(context.Background(), f.binding.Scope, func(tx *WorkspaceMutationTx) error { return tx.UpsertCandidate(context.Background(), candidate) }); err != nil {
				t.Fatal(err)
			}
		}, `CREATE TRIGGER prepare_candidate_time_storage_drift AFTER INSERT ON workspace_materializations BEGIN UPDATE workspace_candidates SET imported_at=CAST(imported_at AS BLOB) WHERE project_id=NEW.project_id AND workspace_id=NEW.workspace_id; END`},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMaterializationFixtureWithoutJournal(t)
			if test.seed != nil {
				test.seed(t, fixture)
			}
			before := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB())
			if _, err := fixture.store.DB().Exec(test.trigger); err != nil {
				t.Fatal(err)
			}
			prepared := validPreparedMaterialization(t, fixture.binding, "00000000-0000-1000-8000-000000000051")
			err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				got, err := tx.PrepareMaterialization(context.Background(), prepared)
				if err == nil || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
					t.Fatalf("drift prepare=(%+v,%v)", got, err)
				}
				return err
			})
			if err == nil {
				t.Fatal("triggered prepare succeeded")
			}
			if after := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB()); !reflect.DeepEqual(after, before) {
				t.Fatalf("trigger rollback drift: got %#v want %#v", after, before)
			}
			if _, err := fixture.store.DB().Exec(`DROP TRIGGER ` + strings.Fields(test.trigger)[2]); err != nil {
				t.Fatal(err)
			}
			if err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				_, err := tx.PrepareMaterialization(context.Background(), prepared)
				return err
			}); err != nil {
				t.Fatalf("writer not reusable after rollback: %v", err)
			}
		})
	}
}

func TestWorkspaceMutationTxPrepareMaterializationRejectsBlobScopedJournalAlias(t *testing.T) {
	for _, column := range []string{"project_id", "workspace_id"} {
		t.Run(column, func(t *testing.T) {
			fixture := newMaterializationFixtureWithoutJournal(t)
			proof := "operation proof\n"
			insertMaterializationRow(t, fixture.store, fixture.binding, "hidden-pending", "prepared",
				fixture.priorTree, fixture.candidateTree, fixture.priorDigest, fixture.candidateDigest, &proof)
			corruptMaterializationScopeKeyToBlob(t, fixture.store, "hidden-pending", column)
			before := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB())
			prepared := validPreparedMaterialization(t, fixture.binding, "00000000-0000-1000-8000-000000000051")
			var got WorkspaceMaterializationRecord
			err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				var err error
				got, err = tx.PrepareMaterialization(context.Background(), prepared)
				return err
			})
			if err == nil || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
				t.Fatalf("BLOB-scoped journal prepare=(%+v,%v), want zero,error", got, err)
			}
			if after := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB()); !reflect.DeepEqual(after, before) {
				t.Fatalf("BLOB-scoped journal rejection changed raw state: got %#v want %#v", after, before)
			}
		})
	}
}

func TestWorkspaceMutationTxPrepareMaterializationConcurrentWritersSerialize(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := NewWorkspaceRepo(store.DB())
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	first := validPreparedMaterialization(t, binding, "00000000-0000-1000-8000-000000000051")
	second := validPreparedMaterialization(t, binding, "00000000-0000-1000-8000-000000000052")
	secondDB, secondBeginIssued := openMaterializationAdmissionDB(t, databasePath)
	secondRepo := NewWorkspaceRepo(secondDB)
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
			if _, err := tx.PrepareMaterialization(context.Background(), first); err != nil {
				return err
			}
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- secondRepo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
			_, err := tx.PrepareMaterialization(context.Background(), second)
			return err
		})
	}()
	<-secondBeginIssued
	select {
	case err := <-secondDone:
		t.Fatalf("second writer returned before first committed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first prepare: %v", err)
	}
	if err := <-secondDone; err == nil {
		t.Fatal("second concurrent prepare succeeded")
	}
	disposition := readMaterializationDisposition(t, repo, binding.Scope)
	if len(disposition.Journals) != 1 || disposition.Journals[0].JournalID != first.JournalID {
		t.Fatalf("serialized journals=%+v", disposition.Journals)
	}
}

func TestWorkspaceMutationTxTransitionMaterializationSupportsOnlyFiveEdges(t *testing.T) {
	for _, edge := range []struct{ source, target string }{
		{"prepared", "published"}, {"prepared", "recovered_old"}, {"prepared", "recovered_new"},
		{"published", "recovered_old"}, {"published", "recovered_new"},
	} {
		t.Run(edge.source+" to "+edge.target, func(t *testing.T) {
			proof := "operation proof\n"
			fixture := newMaterializationFixture(t, edge.source, &proof)
			insertWorkspaceOperation(t, fixture.store, fixture.binding.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "active")
			candidate := workspaceCandidateRecord(t, fixture.binding, false, 0)
			if err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error { return tx.UpsertCandidate(context.Background(), candidate) }); err != nil {
				t.Fatal(err)
			}
			expected := readMaterializationDisposition(t, fixture.repo, fixture.binding.Scope).Journals[0]
			input := cloneWorkspaceMaterializationRecord(expected)
			before := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB())
			var got WorkspaceMaterializationRecord
			err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				var err error
				got, err = tx.TransitionMaterialization(context.Background(), expected, edge.target)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(expected, input) {
				t.Fatal("transition mutated input")
			}
			want := input
			want.State = edge.target
			want.mutationMetadata.UpdatedAt = got.mutationMetadata.UpdatedAt
			want.mutationMetadata.UpdatedRaw = got.mutationMetadata.UpdatedRaw
			want.mutationMetadata.UpdatedClass = got.mutationMetadata.UpdatedClass
			if !equalWorkspaceMaterializationRecords(got, want) {
				t.Fatalf("transition result=%+v want %+v", got, want)
			}
			if got.IncludedOperationsJSON == expected.IncludedOperationsJSON || got.PublicationReviewJSON == expected.PublicationReviewJSON || got.PriorCandidateJSON == expected.PriorCandidateJSON ||
				&got.PriorTree[0].Data[0] == &expected.PriorTree[0].Data[0] || &got.CandidateTree[0].Data[0] == &expected.CandidateTree[0].Data[0] {
				t.Fatal("transition result aliases input")
			}
			assertAtomicWorkspaceMaterializationRawDelta(t, before, readAtomicWorkspaceRawSnapshot(t, fixture.store.DB()), fixture.binding.Scope,
				map[string]string{"project_id": quoteSQLiteTextLiteral(fixture.binding.Scope.ProjectID), "workspace_id": quoteSQLiteTextLiteral(string(fixture.binding.Scope.WorkspaceID)), "journal_id": quoteSQLiteTextLiteral(expected.JournalID)}, "state", "updated_at")
		})
	}
}

func TestWorkspaceMutationTxTransitionMaterializationRejectsIllegalEdgesAndProofs(t *testing.T) {
	legal := map[string]bool{"prepared/published": true, "prepared/recovered_old": true, "prepared/recovered_new": true, "published/recovered_old": true, "published/recovered_new": true}
	states := []string{"prepared", "published", "accepted", "recovered_old", "recovered_new"}
	for _, source := range states {
		for _, target := range states {
			if legal[source+"/"+target] {
				continue
			}
			t.Run(source+" to "+target, func(t *testing.T) {
				proof := "operation proof\n"
				fixture := newMaterializationFixture(t, source, &proof)
				expected := readMaterializationDisposition(t, fixture.repo, fixture.binding.Scope).Journals[0]
				before := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB())
				err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
					got, err := tx.TransitionMaterialization(context.Background(), expected, target)
					if err == nil || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
						t.Fatalf("illegal transition=(%+v,%v)", got, err)
					}
					return err
				})
				if err == nil {
					t.Fatal("illegal transition succeeded")
				}
				if after := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB()); !reflect.DeepEqual(after, before) {
					t.Fatal("illegal transition mutated state")
				}
			})
		}
	}

	for _, test := range []struct {
		name   string
		mutate func(*WorkspaceMaterializationRecord)
	}{
		{"version zero", func(r *WorkspaceMaterializationRecord) { r.PublicationReviewProofVersion = 0 }},
		{"nil operations", func(r *WorkspaceMaterializationRecord) { r.IncludedOperationsJSON = nil }},
		{"empty operations", func(r *WorkspaceMaterializationRecord) { v := ""; r.IncludedOperationsJSON = &v }},
		{"nil review", func(r *WorkspaceMaterializationRecord) { r.PublicationReviewJSON = nil }},
		{"NUL review", func(r *WorkspaceMaterializationRecord) { v := "bad\x00"; r.PublicationReviewJSON = &v }},
		{"nil prior", func(r *WorkspaceMaterializationRecord) { r.PriorCandidateJSON = nil }},
		{"invalid UTF8 prior", func(r *WorkspaceMaterializationRecord) { v := string([]byte{0xff}); r.PriorCandidateJSON = &v }},
	} {
		t.Run(test.name, func(t *testing.T) {
			proof := "operation proof\n"
			fixture := newMaterializationFixture(t, "prepared", &proof)
			expected := readMaterializationDisposition(t, fixture.repo, fixture.binding.Scope).Journals[0]
			test.mutate(&expected)
			before := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB())
			err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				got, err := tx.TransitionMaterialization(context.Background(), expected, "published")
				if err == nil || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
					t.Fatalf("invalid proof transition=(%+v,%v)", got, err)
				}
				return err
			})
			if err == nil {
				t.Fatal("invalid proof transition succeeded")
			}
			if after := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB()); !reflect.DeepEqual(after, before) {
				t.Fatal("invalid proof transition mutated state")
			}
		})
	}
}

func TestWorkspaceMutationTxTransitionMaterializationRejectsEveryStaleField(t *testing.T) {
	badDigest := state.Digest("sha256:" + strings.Repeat("f", 64))
	mutations := []struct {
		name   string
		mutate func(*WorkspaceMaterializationRecord)
	}{
		{"journal", func(r *WorkspaceMaterializationRecord) { r.JournalID = "other" }},
		{"expected digest", func(r *WorkspaceMaterializationRecord) { r.ExpectedLiveDigest = badDigest }},
		{"accepted digest", func(r *WorkspaceMaterializationRecord) { r.AcceptedBaseDigest = badDigest }},
		{"checkout path", func(r *WorkspaceMaterializationRecord) { r.Checkout.CanonicalPath = "/other" }},
		{"checkout device", func(r *WorkspaceMaterializationRecord) { r.Checkout.Device++ }},
		{"checkout inode", func(r *WorkspaceMaterializationRecord) { r.Checkout.Inode++ }},
		{"prior digest", func(r *WorkspaceMaterializationRecord) { r.PriorTreeDigest = badDigest }},
		{"candidate digest", func(r *WorkspaceMaterializationRecord) { r.CandidateDigest = badDigest }},
		{"generation", func(r *WorkspaceMaterializationRecord) { r.ThroughGeneration++ }},
		{"prior tree", func(r *WorkspaceMaterializationRecord) { r.PriorTree[0].Data = append(r.PriorTree[0].Data, ' ') }},
		{"candidate tree", func(r *WorkspaceMaterializationRecord) {
			r.CandidateTree[0].Data = append(r.CandidateTree[0].Data, ' ')
		}},
		{"stage", func(r *WorkspaceMaterializationRecord) { r.StagePath = "/other-stage" }},
		{"backup", func(r *WorkspaceMaterializationRecord) { r.BackupPath = "/other-backup" }},
		{"operations", func(r *WorkspaceMaterializationRecord) { v := "other operations\n"; r.IncludedOperationsJSON = &v }},
		{"proof version", func(r *WorkspaceMaterializationRecord) { r.PublicationReviewProofVersion++ }},
		{"review", func(r *WorkspaceMaterializationRecord) { v := "other review\n"; r.PublicationReviewJSON = &v }},
		{"prior candidate", func(r *WorkspaceMaterializationRecord) { v := "other prior\n"; r.PriorCandidateJSON = &v }},
		{"created time", func(r *WorkspaceMaterializationRecord) {
			r.mutationMetadata.CreatedAt = r.mutationMetadata.CreatedAt.Add(time.Second)
		}},
		{"updated time", func(r *WorkspaceMaterializationRecord) {
			r.mutationMetadata.UpdatedAt = r.mutationMetadata.UpdatedAt.Add(time.Second)
		}},
		{"created raw", func(r *WorkspaceMaterializationRecord) { r.mutationMetadata.CreatedRaw += " " }},
		{"updated raw", func(r *WorkspaceMaterializationRecord) { r.mutationMetadata.UpdatedRaw += " " }},
		{"stage class", func(r *WorkspaceMaterializationRecord) { r.mutationMetadata.StageClass = "blob" }},
		{"backup class", func(r *WorkspaceMaterializationRecord) { r.mutationMetadata.BackupClass = "blob" }},
		{"created class", func(r *WorkspaceMaterializationRecord) { r.mutationMetadata.CreatedClass = "blob" }},
		{"updated class", func(r *WorkspaceMaterializationRecord) { r.mutationMetadata.UpdatedClass = "blob" }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			proof := "operation proof\n"
			fixture := newMaterializationFixture(t, "prepared", &proof)
			expected := readMaterializationDisposition(t, fixture.repo, fixture.binding.Scope).Journals[0]
			test.mutate(&expected)
			before := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB())
			err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				got, err := tx.TransitionMaterialization(context.Background(), expected, "published")
				if err == nil || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
					t.Fatalf("stale transition=(%+v,%v)", got, err)
				}
				return err
			})
			if err == nil {
				t.Fatal("stale transition succeeded")
			}
			if after := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB()); !reflect.DeepEqual(after, before) {
				t.Fatal("stale transition mutated state")
			}
		})
	}
}

func TestWorkspaceMutationTxTransitionMaterializationRejectsInvalidTransactionMissingTargetAndOtherPending(t *testing.T) {
	proof := "operation proof\n"
	fixture := newMaterializationFixture(t, "prepared", &proof)
	expected := readMaterializationDisposition(t, fixture.repo, fixture.binding.Scope).Journals[0]
	var nilTx *WorkspaceMutationTx
	for name, tx := range map[string]*WorkspaceMutationTx{"nil": nilTx, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			if got, err := tx.TransitionMaterialization(context.Background(), expected, "published"); !errors.Is(err, ErrNotFound) || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
				t.Fatalf("invalid tx transition=(%+v,%v)", got, err)
			}
		})
	}
	conn, err := fixture.store.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	invalidScope := &WorkspaceMutationTx{conn: conn, scope: types.WorkspaceScope{}}
	if got, err := invalidScope.TransitionMaterialization(context.Background(), expected, "published"); !errors.Is(err, ErrNotFound) || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
		t.Fatalf("invalid-scope transition=(%+v,%v)", got, err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := (&WorkspaceMutationTx{conn: conn, scope: fixture.binding.Scope}).TransitionMaterialization(context.Background(), expected, "published"); !errors.Is(err, ErrNotFound) || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
		t.Fatalf("closed-connection transition=(%+v,%v)", got, err)
	}
	var retained *WorkspaceMutationTx
	if err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
		retained = tx
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := retained.TransitionMaterialization(context.Background(), expected, "published"); !errors.Is(err, ErrNotFound) || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
		t.Fatalf("retained-closed transition=(%+v,%v)", got, err)
	}
	t.Run("missing target", func(t *testing.T) {
		before := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB())
		err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
			if _, err := tx.conn.ExecContext(context.Background(), `DELETE FROM workspace_materializations WHERE project_id=? AND workspace_id=? AND journal_id=?`, fixture.binding.Scope.ProjectID, fixture.binding.Scope.WorkspaceID, expected.JournalID); err != nil {
				return err
			}
			got, err := tx.TransitionMaterialization(context.Background(), expected, "published")
			if err == nil || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
				t.Fatalf("missing transition=(%+v,%v)", got, err)
			}
			return err
		})
		if err == nil {
			t.Fatal("missing target transition succeeded")
		}
		if after := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB()); !reflect.DeepEqual(after, before) {
			t.Fatal("missing-target callback did not roll back")
		}
	})
	t.Run("other pending", func(t *testing.T) {
		fixture := newMaterializationFixture(t, "prepared", &proof)
		if _, err := fixture.store.DB().Exec(`DROP INDEX workspace_one_current_materialization`); err != nil {
			t.Fatal(err)
		}
		insertMaterializationRow(t, fixture.store, fixture.binding, "other-pending", "published", fixture.priorTree, fixture.candidateTree, fixture.priorDigest, fixture.candidateDigest, &proof)
		expected := readMaterializationDisposition(t, fixture.repo, fixture.binding.Scope).Journals[1]
		before := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB())
		err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
			_, err := tx.TransitionMaterialization(context.Background(), expected, "recovered_old")
			return err
		})
		if err == nil {
			t.Fatal("transition with another pending journal succeeded")
		}
		if after := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB()); !reflect.DeepEqual(after, before) {
			t.Fatal("other-pending rejection mutated state")
		}
	})
}

func TestWorkspaceMutationTxTransitionMaterializationRejectsV0AndDuplicateHistory(t *testing.T) {
	t.Run("persisted version zero", func(t *testing.T) {
		proof := "operation proof\n"
		fixture := newMaterializationFixture(t, "prepared", &proof)
		mustExecMaterialization(t, fixture.store, `
			UPDATE workspace_materializations
			SET publication_review_proof_version=0,publication_review_json=NULL,prior_candidate_json=NULL
		`)
		expected := readMaterializationDisposition(t, fixture.repo, fixture.binding.Scope).Journals[0]
		before := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB())
		err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
			got, err := tx.TransitionMaterialization(context.Background(), expected, "published")
			if err == nil || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
				t.Fatalf("v0 transition=(%+v,%v)", got, err)
			}
			return err
		})
		if err == nil {
			t.Fatal("persisted v0 transition succeeded")
		}
		if after := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB()); !reflect.DeepEqual(after, before) {
			t.Fatal("v0 rejection mutated state")
		}
	})

	t.Run("logical duplicate terminal neighbor", func(t *testing.T) {
		proof := "operation proof\n"
		fixture := newMaterializationFixture(t, "prepared", &proof)
		insertMaterializationRow(t, fixture.store, fixture.binding, "history", "accepted", fixture.priorTree, fixture.candidateTree, fixture.priorDigest, fixture.candidateDigest, nil)
		mustExecMaterialization(t, fixture.store, `
			INSERT INTO workspace_materializations
			(project_id,workspace_id,journal_id,expected_live_digest,accepted_base_digest,
			 checkout_path,checkout_device,checkout_inode,prior_tree_digest,candidate_digest,
			 through_generation,prior_tree,candidate_tree,stage_path,backup_path,state,
			 created_at,updated_at,included_operations_json,publication_review_json,prior_candidate_json,publication_review_proof_version)
			SELECT project_id,workspace_id,CAST(journal_id AS BLOB),expected_live_digest,accepted_base_digest,
			 checkout_path,checkout_device,checkout_inode,prior_tree_digest,candidate_digest,
			 through_generation,prior_tree,candidate_tree,stage_path,backup_path,state,
			 created_at,updated_at,included_operations_json,publication_review_json,prior_candidate_json,publication_review_proof_version
			FROM workspace_materializations WHERE journal_id='history'
		`)
		journals := readMaterializationDisposition(t, fixture.repo, fixture.binding.Scope).Journals
		var expected WorkspaceMaterializationRecord
		for _, journal := range journals {
			if journal.JournalID == "legacy-journal" {
				expected = journal
			}
		}
		if expected.State != "prepared" {
			t.Fatalf("target=%+v, want prepared legacy-journal", expected)
		}
		before := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB())
		err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
			got, err := tx.TransitionMaterialization(context.Background(), expected, "published")
			if err == nil || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
				t.Fatalf("duplicate-history transition=(%+v,%v)", got, err)
			}
			return err
		})
		if err == nil {
			t.Fatal("transition accepted logical duplicate history")
		}
		if after := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB()); !reflect.DeepEqual(after, before) {
			t.Fatal("duplicate-history transition mutated state")
		}
	})
}

func TestWorkspaceMutationTxTransitionMaterializationRejectsBlobScopedJournalAlias(t *testing.T) {
	for _, column := range []string{"project_id", "workspace_id"} {
		t.Run(column, func(t *testing.T) {
			proof := "operation proof\n"
			fixture := newMaterializationFixture(t, "prepared", &proof)
			expected := readMaterializationDisposition(t, fixture.repo, fixture.binding.Scope).Journals[0]
			if _, err := fixture.store.DB().Exec(`DROP INDEX workspace_one_current_materialization`); err != nil {
				t.Fatal(err)
			}
			insertMaterializationRow(t, fixture.store, fixture.binding, "hidden-pending", "prepared",
				fixture.priorTree, fixture.candidateTree, fixture.priorDigest, fixture.candidateDigest, &proof)
			corruptMaterializationScopeKeyToBlob(t, fixture.store, "hidden-pending", column)
			before := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB())
			var got WorkspaceMaterializationRecord
			err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				var err error
				got, err = tx.TransitionMaterialization(context.Background(), expected, "published")
				return err
			})
			if err == nil || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
				t.Fatalf("BLOB-scoped journal transition=(%+v,%v), want zero,error", got, err)
			}
			if after := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB()); !reflect.DeepEqual(after, before) {
				t.Fatalf("BLOB-scoped journal rejection changed raw state: got %#v want %#v", after, before)
			}
		})
	}
}

func TestWorkspaceMutationTxTransitionMaterializationCallbackRollbackReleasesWriter(t *testing.T) {
	proof := "operation proof\n"
	fixture := newMaterializationFixture(t, "prepared", &proof)
	expected := readMaterializationDisposition(t, fixture.repo, fixture.binding.Scope).Journals[0]
	before := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB())
	wantErr := errors.New("rollback transitioned journal")
	err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.TransitionMaterialization(context.Background(), expected, "published")
		if err != nil || got.State != "published" {
			t.Fatalf("transition before callback rollback=(%+v,%v)", got, err)
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("callback rollback error=%v, want %v", err, wantErr)
	}
	if after := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB()); !reflect.DeepEqual(after, before) {
		t.Fatal("transition callback rollback changed state")
	}
	if err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.TransitionMaterialization(context.Background(), expected, "published")
		return err
	}); err != nil {
		t.Fatalf("writer not reusable after transition rollback: %v", err)
	}
}

func TestWorkspaceMutationTxTransitionMaterializationDetectsSQLTimestampAndAdjacentDrift(t *testing.T) {
	for _, test := range []struct {
		name    string
		seed    func(*testing.T, *materializationFixture)
		trigger string
		future  bool
	}{
		{"abort", nil, `CREATE TRIGGER transition_abort BEFORE UPDATE OF state ON workspace_materializations BEGIN SELECT RAISE(ABORT,'transition abort'); END`, false},
		{"ignore", nil, `CREATE TRIGGER transition_ignore BEFORE UPDATE OF state ON workspace_materializations BEGIN SELECT RAISE(IGNORE); END`, false},
		{"target drift", nil, `CREATE TRIGGER transition_target_drift AFTER UPDATE OF state ON workspace_materializations BEGIN UPDATE workspace_materializations SET through_generation=through_generation+1 WHERE rowid=NEW.rowid; END`, false},
		{"neighbor drift", func(t *testing.T, f *materializationFixture) {
			insertMaterializationRow(t, f.store, f.binding, "history", "accepted", f.priorTree, f.candidateTree, f.priorDigest, f.candidateDigest, nil)
		}, `CREATE TRIGGER transition_neighbor_drift AFTER UPDATE OF state ON workspace_materializations WHEN NEW.journal_id!='history' BEGIN UPDATE workspace_materializations SET through_generation=through_generation+1 WHERE journal_id='history'; END`, false},
		{"operation drift", func(t *testing.T, f *materializationFixture) {
			insertWorkspaceOperation(t, f.store, f.binding.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "active")
		}, `CREATE TRIGGER transition_operation_drift AFTER UPDATE OF state ON workspace_materializations BEGIN UPDATE workspace_overlay_operations SET state='discarded' WHERE project_id=NEW.project_id AND workspace_id=NEW.workspace_id; END`, false},
		{"operation timestamp raw drift", func(t *testing.T, f *materializationFixture) {
			insertWorkspaceOperation(t, f.store, f.binding.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "active")
		}, `CREATE TRIGGER transition_operation_time_raw_drift AFTER UPDATE OF state ON workspace_materializations BEGIN UPDATE workspace_overlay_operations SET created_at=strftime('%Y-%m-%dT%H:%M:%SZ',created_at) WHERE project_id=NEW.project_id AND workspace_id=NEW.workspace_id; END`, false},
		{"operation timestamp storage drift", func(t *testing.T, f *materializationFixture) {
			insertWorkspaceOperation(t, f.store, f.binding.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "active")
		}, `CREATE TRIGGER transition_operation_time_storage_drift AFTER UPDATE OF state ON workspace_materializations BEGIN UPDATE workspace_overlay_operations SET created_at=CAST(created_at AS BLOB) WHERE project_id=NEW.project_id AND workspace_id=NEW.workspace_id; END`, false},
		{"candidate drift", func(t *testing.T, f *materializationFixture) {
			candidate := workspaceCandidateRecord(t, f.binding, false, 0)
			if err := f.repo.WithImmediateWorkspace(context.Background(), f.binding.Scope, func(tx *WorkspaceMutationTx) error { return tx.UpsertCandidate(context.Background(), candidate) }); err != nil {
				t.Fatal(err)
			}
		}, `CREATE TRIGGER transition_candidate_drift AFTER UPDATE OF state ON workspace_materializations BEGIN UPDATE workspace_candidates SET imported_by='00000000-0000-4000-8000-000000000072' WHERE project_id=NEW.project_id AND workspace_id=NEW.workspace_id; END`, false},
		{"candidate tree storage drift", func(t *testing.T, f *materializationFixture) {
			candidate := workspaceCandidateRecord(t, f.binding, false, 0)
			if err := f.repo.WithImmediateWorkspace(context.Background(), f.binding.Scope, func(tx *WorkspaceMutationTx) error { return tx.UpsertCandidate(context.Background(), candidate) }); err != nil {
				t.Fatal(err)
			}
		}, `CREATE TRIGGER transition_candidate_tree_storage_drift AFTER UPDATE OF state ON workspace_materializations BEGIN UPDATE workspace_candidates SET direct_tree=CAST(direct_tree AS TEXT) WHERE project_id=NEW.project_id AND workspace_id=NEW.workspace_id; END`, false},
		{"candidate timestamp raw drift", func(t *testing.T, f *materializationFixture) {
			candidate := workspaceCandidateRecord(t, f.binding, false, 0)
			if err := f.repo.WithImmediateWorkspace(context.Background(), f.binding.Scope, func(tx *WorkspaceMutationTx) error { return tx.UpsertCandidate(context.Background(), candidate) }); err != nil {
				t.Fatal(err)
			}
		}, `CREATE TRIGGER transition_candidate_time_raw_drift AFTER UPDATE OF state ON workspace_materializations BEGIN UPDATE workspace_candidates SET imported_at='2026-07-28T14:00:00Z' WHERE project_id=NEW.project_id AND workspace_id=NEW.workspace_id; END`, false},
		{"candidate timestamp storage drift", func(t *testing.T, f *materializationFixture) {
			candidate := workspaceCandidateRecord(t, f.binding, false, 0)
			if err := f.repo.WithImmediateWorkspace(context.Background(), f.binding.Scope, func(tx *WorkspaceMutationTx) error { return tx.UpsertCandidate(context.Background(), candidate) }); err != nil {
				t.Fatal(err)
			}
		}, `CREATE TRIGGER transition_candidate_time_storage_drift AFTER UPDATE OF state ON workspace_materializations BEGIN UPDATE workspace_candidates SET imported_at=CAST(imported_at AS BLOB) WHERE project_id=NEW.project_id AND workspace_id=NEW.workspace_id; END`, false},
		{"timestamp regression", nil, `CREATE TRIGGER transition_noop AFTER UPDATE OF state ON workspace_materializations BEGIN SELECT 1; END`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			proof := "operation proof\n"
			fixture := newMaterializationFixture(t, "prepared", &proof)
			if test.seed != nil {
				test.seed(t, fixture)
			}
			if test.future {
				mustExecMaterialization(t, fixture.store, `UPDATE workspace_materializations SET created_at='2000-01-01 00:00:00+00:00',updated_at='2099-01-01 00:00:00+00:00' WHERE journal_id='legacy-journal'`)
			}
			expected := readMaterializationDisposition(t, fixture.repo, fixture.binding.Scope).Journals[0]
			before := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB())
			if _, err := fixture.store.DB().Exec(test.trigger); err != nil {
				t.Fatal(err)
			}
			err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				got, err := tx.TransitionMaterialization(context.Background(), expected, "published")
				if err == nil || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
					t.Fatalf("drift transition=(%+v,%v)", got, err)
				}
				return err
			})
			if err == nil {
				t.Fatal("triggered transition succeeded")
			}
			if after := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB()); !reflect.DeepEqual(after, before) {
				t.Fatalf("transition rollback drift: got %#v want %#v", after, before)
			}
		})
	}
}

func TestWorkspaceMutationTxTransitionMaterializationRestartAndScopeIsolation(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	a := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	b := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	c := createBinding(t, repo, "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000013", "/checkout-c", 3, 13)
	proof := "operation proof\n"
	fixtureA := makeMaterializationFixture(t, store, repo, a, "prepared", &proof)
	makeMaterializationFixture(t, store, repo, b, "prepared", &proof)
	makeMaterializationFixture(t, store, repo, c, "prepared", &proof)
	expected := readMaterializationDisposition(t, repo, a.Scope).Journals[0]
	if err := repo.WithImmediateWorkspace(context.Background(), a.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.TransitionMaterialization(context.Background(), expected, "published")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restartedRepo := NewWorkspaceRepo(restarted.DB())
	if got := readMaterializationDisposition(t, restartedRepo, a.Scope); len(got.Journals) != 1 || got.Journals[0].State != "published" || got.Journals[0].CandidateDigest != fixtureA.candidateDigest {
		t.Fatalf("workspace A restart=%+v", got)
	}
	if got := readMaterializationDisposition(t, restartedRepo, b.Scope); len(got.Journals) != 1 || got.Journals[0].State != "prepared" {
		t.Fatalf("workspace B changed=%+v", got)
	}
	if got := readMaterializationDisposition(t, restartedRepo, c.Scope); len(got.Journals) != 1 || got.Journals[0].State != "prepared" {
		t.Fatalf("project C changed=%+v", got)
	}
}

func TestWorkspaceMutationTxAcceptMaterializationAPI(t *testing.T) {
	raw := "{\"schema_version\":1,\"initial_through_generation\":3,\"operations\":[]}\n"
	fixture := newMaterializationFixture(t, "published", &raw)
	var got WorkspaceMaterializationRecord
	err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
		expected, err := tx.AcceptanceEligibleMaterialization(context.Background())
		if err != nil {
			return err
		}
		got, err = tx.AcceptMaterialization(context.Background(), *expected)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "accepted" || got.JournalID != "legacy-journal" || got.IncludedOperationsJSON == nil || *got.IncludedOperationsJSON != raw {
		t.Fatalf("accepted materialization=%+v", got)
	}
}

func TestWorkspaceMutationTxAcceptMaterializationRejectsIneligibleAndStaleRows(t *testing.T) {
	for _, materializationState := range []string{"published", "recovered_new"} {
		t.Run("missing proof "+materializationState, func(t *testing.T) {
			fixture := newMaterializationFixture(t, materializationState, nil)
			before := readMaterializationDisposition(t, fixture.repo, fixture.binding.Scope)
			err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				_, err := tx.AcceptMaterialization(context.Background(), before.Journals[0])
				return err
			})
			if err == nil {
				t.Fatal("materialization without operation proof accepted")
			}
			after := readMaterializationDisposition(t, fixture.repo, fixture.binding.Scope)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("missing-proof acceptance changed state: got %+v want %+v", after, before)
			}
		})
	}

	for _, materializationState := range []string{"prepared", "accepted", "recovered_old"} {
		t.Run(materializationState, func(t *testing.T) {
			fixture := newMaterializationFixture(t, materializationState, nil)
			disposition := readMaterializationDisposition(t, fixture.repo, fixture.binding.Scope)
			err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				got, err := tx.AcceptMaterialization(context.Background(), disposition.Journals[0])
				if err == nil || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
					t.Fatalf("ineligible acceptance=(%+v,%v), want zero,error", got, err)
				}
				return err
			})
			if err == nil {
				t.Fatal("ineligible materialization accepted")
			}
			after := readMaterializationDisposition(t, fixture.repo, fixture.binding.Scope)
			if !reflect.DeepEqual(after, disposition) {
				t.Fatalf("ineligible acceptance changed state: got %+v want %+v", after, disposition)
			}
		})
	}

	mutations := []struct {
		name   string
		mutate func(*WorkspaceMaterializationRecord)
	}{
		{"journal", func(value *WorkspaceMaterializationRecord) { value.JournalID = "other" }},
		{"expected digest", func(value *WorkspaceMaterializationRecord) {
			value.ExpectedLiveDigest = state.Digest("sha256:" + strings.Repeat("f", 64))
		}},
		{"accepted digest", func(value *WorkspaceMaterializationRecord) {
			value.AcceptedBaseDigest = state.Digest("sha256:" + strings.Repeat("f", 64))
		}},
		{"checkout path", func(value *WorkspaceMaterializationRecord) { value.Checkout.CanonicalPath = "/other" }},
		{"checkout device", func(value *WorkspaceMaterializationRecord) { value.Checkout.Device++ }},
		{"checkout inode", func(value *WorkspaceMaterializationRecord) { value.Checkout.Inode++ }},
		{"prior digest", func(value *WorkspaceMaterializationRecord) {
			value.PriorTreeDigest = state.Digest("sha256:" + strings.Repeat("f", 64))
		}},
		{"candidate digest", func(value *WorkspaceMaterializationRecord) {
			value.CandidateDigest = state.Digest("sha256:" + strings.Repeat("f", 64))
		}},
		{"generation", func(value *WorkspaceMaterializationRecord) { value.ThroughGeneration++ }},
		{"state", func(value *WorkspaceMaterializationRecord) { value.State = "recovered_new" }},
		{"prior tree data", func(value *WorkspaceMaterializationRecord) {
			value.PriorTree[0].Data = append(bytes.Clone(value.PriorTree[0].Data), ' ')
		}},
		{"prior tree path", func(value *WorkspaceMaterializationRecord) { value.PriorTree[0].Path = "other.toml" }},
		{"candidate tree data", func(value *WorkspaceMaterializationRecord) {
			value.CandidateTree[0].Data = append(bytes.Clone(value.CandidateTree[0].Data), ' ')
		}},
		{"candidate tree path", func(value *WorkspaceMaterializationRecord) { value.CandidateTree[0].Path = "other.toml" }},
		{"included", func(value *WorkspaceMaterializationRecord) {
			other := "{\"other\":true}\n"
			value.IncludedOperationsJSON = &other
		}},
		{"missing included", func(value *WorkspaceMaterializationRecord) { value.IncludedOperationsJSON = nil }},
	}
	for _, test := range mutations {
		t.Run("stale "+test.name, func(t *testing.T) {
			raw := "{\"schema_version\":1,\"initial_through_generation\":3,\"operations\":[]}\n"
			fixture := newMaterializationFixture(t, "published", &raw)
			before := readMaterializationDisposition(t, fixture.repo, fixture.binding.Scope)
			expected := cloneWorkspaceMaterializationRecord(before.Journals[0])
			test.mutate(&expected)
			err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				_, err := tx.AcceptMaterialization(context.Background(), expected)
				return err
			})
			if err == nil {
				t.Fatal("stale materialization accepted")
			}
			after := readMaterializationDisposition(t, fixture.repo, fixture.binding.Scope)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("stale acceptance changed state: got %+v want %+v", after, before)
			}
		})
	}
}

func TestWorkspaceMaterializationProofDirectAcceptanceRejectsV0(t *testing.T) {
	for _, materializationState := range []string{"published", "recovered_new"} {
		t.Run(materializationState, func(t *testing.T) {
			raw := "{\"schema_version\":1,\"initial_through_generation\":3,\"operations\":[]}\n"
			fixture := newMaterializationFixture(t, materializationState, &raw)
			mustExecMaterialization(t, fixture.store, `
				UPDATE workspace_materializations
				SET publication_review_json=NULL,prior_candidate_json=NULL,publication_review_proof_version=0
			`)
			disposition := readMaterializationDisposition(t, fixture.repo, fixture.binding.Scope)
			if len(disposition.Journals) != 1 || disposition.Journals[0].IncludedOperationsJSON == nil ||
				disposition.Journals[0].PublicationReviewProofVersion != 0 {
				t.Fatalf("v0 fixture=%+v", disposition)
			}
			before := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB())
			err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				got, err := tx.AcceptMaterialization(context.Background(), disposition.Journals[0])
				if err == nil || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
					t.Fatalf("v0 direct acceptance=(%+v,%v), want zero,error", got, err)
				}
				return err
			})
			if err == nil {
				t.Fatal("v0 direct acceptance succeeded")
			}
			after := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB())
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("v0 direct acceptance changed raw state: got %#v want %#v", after, before)
			}
		})
	}
}

func TestWorkspaceMutationTxAcceptMaterializationDetectsFailuresAndTriggerDrift(t *testing.T) {
	for _, test := range []struct {
		name, trigger string
	}{
		{"write failure", `CREATE TRIGGER fail_materialization_accept BEFORE UPDATE OF state ON workspace_materializations BEGIN SELECT RAISE(ABORT,'injected materialization failure'); END`},
		{"after trigger drift", `CREATE TRIGGER drift_materialization_accept AFTER UPDATE OF state ON workspace_materializations BEGIN UPDATE workspace_materializations SET through_generation=through_generation+1 WHERE project_id=NEW.project_id AND workspace_id=NEW.workspace_id AND journal_id=NEW.journal_id; END`},
		{"after trigger hidden path drift", `CREATE TRIGGER drift_materialization_path AFTER UPDATE OF state ON workspace_materializations BEGIN UPDATE workspace_materializations SET stage_path='/drifted-stage' WHERE project_id=NEW.project_id AND workspace_id=NEW.workspace_id AND journal_id=NEW.journal_id; END`},
		{"after trigger publication proof drift", `CREATE TRIGGER drift_materialization_proof AFTER UPDATE OF state ON workspace_materializations BEGIN UPDATE workspace_materializations SET publication_review_json='drifted-review',prior_candidate_json='drifted-prior' WHERE project_id=NEW.project_id AND workspace_id=NEW.workspace_id AND journal_id=NEW.journal_id; END`},
		{"after trigger hidden timestamp drift", `CREATE TRIGGER drift_materialization_timestamp AFTER UPDATE OF state ON workspace_materializations BEGIN UPDATE workspace_materializations SET updated_at='2099-01-01 00:00:00+00:00' WHERE project_id=NEW.project_id AND workspace_id=NEW.workspace_id AND journal_id=NEW.journal_id; END`},
		{"after trigger created timestamp drift", `CREATE TRIGGER drift_materialization_created AFTER UPDATE OF state ON workspace_materializations BEGIN UPDATE workspace_materializations SET created_at='2000-01-01 00:00:00+00:00' WHERE project_id=NEW.project_id AND workspace_id=NEW.workspace_id AND journal_id=NEW.journal_id; END`},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := "{\"schema_version\":1,\"initial_through_generation\":3,\"operations\":[]}\n"
			fixture := newMaterializationFixture(t, "published", &raw)
			before := readMaterializationDisposition(t, fixture.repo, fixture.binding.Scope)
			if _, err := fixture.store.DB().Exec(test.trigger); err != nil {
				t.Fatal(err)
			}
			err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				_, err := tx.AcceptMaterialization(context.Background(), before.Journals[0])
				return err
			})
			if err == nil {
				t.Fatal("triggered materialization acceptance succeeded")
			}
			after := readMaterializationDisposition(t, fixture.repo, fixture.binding.Scope)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("triggered acceptance changed state: got %+v want %+v", after, before)
			}
		})
	}
}

func TestWorkspaceMutationTxAcceptMaterializationRejectsTimestampRegression(t *testing.T) {
	raw := "{\"schema_version\":1,\"initial_through_generation\":3,\"operations\":[]}\n"
	fixture := newMaterializationFixture(t, "published", &raw)
	mustExecMaterialization(t, fixture.store, `
		UPDATE workspace_materializations
		SET created_at='2000-01-01 00:00:00+00:00', updated_at='2099-01-01 00:00:00+00:00'
	`)
	expected := readMaterializationDisposition(t, fixture.repo, fixture.binding.Scope).Journals[0]
	before := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB())
	err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.AcceptMaterialization(context.Background(), expected)
		return err
	})
	if err == nil {
		t.Fatal("materialization acceptance moved updated_at backwards")
	}
	after := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB())
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("timestamp regression changed raw state: got %#v want %#v", after, before)
	}
}

func TestWorkspaceMutationTxAcceptMaterializationRejectsStaleHiddenState(t *testing.T) {
	proof := "{\"schema_version\":1,\"initial_through_generation\":3,\"operations\":[]}\n"
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *materializationFixture)
	}{
		{"stage path", updateMaterialization("stage_path", "/stale-stage")},
		{"backup path", updateMaterialization("backup_path", "/stale-backup")},
		{"created timestamp", updateMaterialization("created_at", "2026-07-28 11:59:59+00:00")},
		{"updated timestamp", updateMaterialization("updated_at", "2026-07-28 12:00:02+00:00")},
		{"stage storage class", func(t *testing.T, fixture *materializationFixture) {
			mustExecMaterialization(t, fixture.store, `UPDATE workspace_materializations SET stage_path=CAST(stage_path AS BLOB)`)
		}},
		{"backup storage class", func(t *testing.T, fixture *materializationFixture) {
			mustExecMaterialization(t, fixture.store, `UPDATE workspace_materializations SET backup_path=CAST(backup_path AS BLOB)`)
		}},
		{"created storage class", func(t *testing.T, fixture *materializationFixture) {
			mustExecMaterialization(t, fixture.store, `UPDATE workspace_materializations SET created_at=1`)
		}},
		{"updated storage class", func(t *testing.T, fixture *materializationFixture) {
			mustExecMaterialization(t, fixture.store, `UPDATE workspace_materializations SET updated_at=1`)
		}},
		{"operation envelope", updateMaterialization("included_operations_json", "{\"stale\":true}\n")},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMaterializationFixture(t, "published", &proof)
			expected := readMaterializationDisposition(t, fixture.repo, fixture.binding.Scope).Journals[0]
			test.mutate(t, fixture)
			before := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB())
			err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				got, err := tx.AcceptMaterialization(context.Background(), expected)
				if err == nil || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
					t.Fatalf("stale hidden acceptance=(%+v,%v), want zero,error", got, err)
				}
				return err
			})
			if err == nil {
				t.Fatal("stale hidden materialization accepted")
			}
			after := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB())
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("stale hidden acceptance changed raw state: got %#v want %#v", after, before)
			}
		})
	}
}

func TestWorkspaceMaterializationProofCASRejectsEveryPublicProofMutation(t *testing.T) {
	raw := "{\"schema_version\":1,\"initial_through_generation\":3,\"operations\":[]}\n"
	fixture := newMaterializationFixture(t, "published", &raw)
	expected := readEligibleMaterialization(t, fixture.repo, fixture.binding.Scope)
	if expected == nil {
		t.Fatal("eligible materialization is nil")
	}
	for _, test := range []struct {
		name   string
		mutate func(*WorkspaceMaterializationRecord)
	}{
		{"stage path", func(record *WorkspaceMaterializationRecord) { record.StagePath = "/other-stage" }},
		{"backup path", func(record *WorkspaceMaterializationRecord) { record.BackupPath = "/other-backup" }},
		{"proof version", func(record *WorkspaceMaterializationRecord) { record.PublicationReviewProofVersion++ }},
		{"publication review", func(record *WorkspaceMaterializationRecord) {
			value := "review mutated"
			record.PublicationReviewJSON = &value
		}},
		{"prior candidate", func(record *WorkspaceMaterializationRecord) {
			value := "prior mutated"
			record.PriorCandidateJSON = &value
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutated := cloneWorkspaceMaterializationRecord(*expected)
			test.mutate(&mutated)
			before := readMaterializationDisposition(t, fixture.repo, fixture.binding.Scope)
			if err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				_, err := tx.AcceptMaterialization(context.Background(), mutated)
				return err
			}); err == nil {
				t.Fatal("mutated public proof accepted")
			}
			if after := readMaterializationDisposition(t, fixture.repo, fixture.binding.Scope); !reflect.DeepEqual(after, before) {
				t.Fatalf("mutated public proof changed state: got %+v want %+v", after, before)
			}
		})
	}
}

func TestWorkspaceMutationTxAcceptMaterializationIgnoredUpdateRollsBackRawStateAndReleasesWriter(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	raw := "{\"schema_version\":1,\"initial_through_generation\":3,\"operations\":[]}\n"
	fixture := makeMaterializationFixture(t, store, repo, binding, "published", &raw)
	seedAtomicWorkspaceAdjacency(t, store, repo, fixture)
	expected := readMaterializationDisposition(t, repo, binding.Scope).Journals[1]
	if expected.JournalID != "legacy-journal" {
		t.Fatalf("target journal=%q, want legacy-journal", expected.JournalID)
	}
	before := readAtomicWorkspaceRawSnapshot(t, store.DB())
	if _, err := store.DB().Exec(`
		CREATE TRIGGER ignore_materialization_accept
		BEFORE UPDATE OF state ON workspace_materializations
		WHEN OLD.project_id='00000000-0000-4000-8000-000000000001'
		 AND OLD.workspace_id='00000000-0000-4000-8000-000000000011'
		 AND OLD.journal_id='legacy-journal'
		BEGIN SELECT RAISE(IGNORE); END
	`); err != nil {
		t.Fatal(err)
	}
	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.AcceptMaterialization(context.Background(), expected)
		if err == nil || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
			t.Fatalf("ignored acceptance=(%+v,%v), want zero,error", got, err)
		}
		return err
	})
	if err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrCommitOutcomeUnknown) {
		t.Fatalf("ignored acceptance error=%v, want ordinary mutation error", err)
	}
	if after := readAtomicWorkspaceRawSnapshot(t, store.DB()); !reflect.DeepEqual(after, before) {
		t.Fatalf("ignored acceptance raw state changed immediately: got %#v want %#v", after, before)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if after := readAtomicWorkspaceRawSnapshot(t, restarted.DB()); !reflect.DeepEqual(after, before) {
		t.Fatalf("ignored acceptance raw state changed after reopen: got %#v want %#v", after, before)
	}
	if _, err := restarted.DB().Exec(`DROP TRIGGER ignore_materialization_accept`); err != nil {
		t.Fatal(err)
	}
	restartedRepo := NewWorkspaceRepo(restarted.DB())
	err = restartedRepo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		eligible, err := tx.AcceptanceEligibleMaterialization(context.Background())
		if err != nil {
			return err
		}
		_, err = tx.AcceptMaterialization(context.Background(), *eligible)
		return err
	})
	if err != nil {
		t.Fatalf("next materialization transaction failed: %v", err)
	}
}

func TestWorkspaceMutationTxAcceptMaterializationSameStatementTimestampRawAtomicDelta(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	proof := "{\"schema_version\":1,\"initial_through_generation\":3,\"operations\":[]}\n"
	fixture := makeMaterializationFixture(t, store, repo, binding, "published", &proof)
	seedAtomicWorkspaceAdjacency(t, store, repo, fixture)
	if _, err := store.DB().Exec(`
		CREATE TABLE materialization_timestamp_probe(value TEXT NOT NULL);
		CREATE TRIGGER materialization_same_second
		BEFORE UPDATE OF state ON workspace_materializations
		WHEN OLD.project_id='00000000-0000-4000-8000-000000000001'
		 AND OLD.workspace_id='00000000-0000-4000-8000-000000000011'
		 AND OLD.journal_id='legacy-journal'
		BEGIN
			INSERT INTO materialization_timestamp_probe(value) VALUES (CURRENT_TIMESTAMP);
			UPDATE workspace_materializations SET updated_at=CURRENT_TIMESTAMP
			WHERE project_id=OLD.project_id AND workspace_id=OLD.workspace_id AND journal_id=OLD.journal_id;
		END
	`); err != nil {
		t.Fatal(err)
	}
	before := readAtomicWorkspaceRawSnapshot(t, store.DB())
	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		expected, err := tx.AcceptanceEligibleMaterialization(context.Background())
		if err != nil {
			return err
		}
		_, err = tx.AcceptMaterialization(context.Background(), *expected)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	after := readAtomicWorkspaceRawSnapshot(t, store.DB())
	targetKeys := map[string]string{
		"project_id":   quoteSQLiteTextLiteral(binding.Scope.ProjectID),
		"workspace_id": quoteSQLiteTextLiteral(string(binding.Scope.WorkspaceID)),
		"journal_id":   quoteSQLiteTextLiteral("legacy-journal"),
	}
	assertAtomicWorkspaceMaterializationRawDelta(t, before, after, binding.Scope, targetKeys, "state", "updated_at")
	target := findAtomicWorkspaceRawRow(t, after, "workspace_materializations", targetKeys)
	assertRawAtomicCell(t, target, "state", quoteSQLiteTextLiteral("accepted"), "text")
	var probe, persisted string
	if err := store.DB().QueryRow(`SELECT value FROM materialization_timestamp_probe`).Scan(&probe); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT CAST(updated_at AS TEXT) FROM workspace_materializations WHERE project_id=? AND workspace_id=? AND journal_id='legacy-journal'`, binding.Scope.ProjectID, binding.Scope.WorkspaceID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if probe != persisted {
		t.Fatalf("same-statement timestamp probe=%v persisted=%v", probe, persisted)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if reopened := readAtomicWorkspaceRawSnapshot(t, restarted.DB()); !reflect.DeepEqual(reopened, after) {
		t.Fatalf("materialization raw state changed after reopen: got %#v want %#v", reopened, after)
	}
}

func assertAtomicWorkspaceMaterializationRawDelta(
	t *testing.T,
	before, after rawAtomicWorkspaceSnapshot,
	scope types.WorkspaceScope,
	materializationKeys map[string]string,
	allowedMaterializationColumns ...string,
) {
	t.Helper()
	bindingKeys := map[string]string{
		"project_id":   quoteSQLiteTextLiteral(scope.ProjectID),
		"workspace_id": quoteSQLiteTextLiteral(string(scope.WorkspaceID)),
	}
	beforeBinding := findAtomicWorkspaceRawRow(t, before, "workspace_bindings", bindingKeys)
	afterBinding := findAtomicWorkspaceRawRow(t, after, "workspace_bindings", bindingKeys)
	beforeRevision := beforeBinding["workspace_revision"]
	afterRevision := afterBinding["workspace_revision"]
	parsedBefore, err := strconv.ParseInt(beforeRevision.Quoted, 10, 64)
	if err != nil {
		t.Fatalf("parse prior workspace revision %q: %v", beforeRevision.Quoted, err)
	}
	if afterRevision.StorageClass != "integer" || afterRevision.Quoted != strconv.FormatInt(parsedBefore+1, 10) {
		t.Fatalf("workspace revision delta=%+v -> %+v, want exact +1 integer", beforeRevision, afterRevision)
	}

	normalizedAfter := make(rawAtomicWorkspaceSnapshot, len(after))
	for table, rows := range after {
		normalizedAfter[table] = make([]rawAtomicWorkspaceRow, len(rows))
		for index, row := range rows {
			normalizedAfter[table][index] = make(rawAtomicWorkspaceRow, len(row))
			for column, cell := range row {
				normalizedAfter[table][index][column] = cell
			}
		}
	}
	for _, row := range normalizedAfter["workspace_bindings"] {
		if atomicWorkspaceRawRowMatches(row, bindingKeys) {
			row["workspace_revision"] = beforeRevision
		}
	}
	assertAtomicWorkspaceRawDelta(t, before, normalizedAfter, "workspace_materializations", materializationKeys, allowedMaterializationColumns...)
}

func TestWorkspaceMutationTxAcceptMaterializationInvalidAPIHasNoMutation(t *testing.T) {
	proof := "{\"schema_version\":1,\"initial_through_generation\":3,\"operations\":[]}\n"
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, *materializationFixture, WorkspaceMaterializationRecord) (*WorkspaceMutationTx, context.Context, WorkspaceMaterializationRecord)
	}{
		{"nil transaction", func(_ *testing.T, _ *materializationFixture, expected WorkspaceMaterializationRecord) (*WorkspaceMutationTx, context.Context, WorkspaceMaterializationRecord) {
			return nil, context.Background(), expected
		}},
		{"empty transaction", func(_ *testing.T, _ *materializationFixture, expected WorkspaceMaterializationRecord) (*WorkspaceMutationTx, context.Context, WorkspaceMaterializationRecord) {
			return &WorkspaceMutationTx{}, context.Background(), expected
		}},
		{"invalid scope", func(t *testing.T, fixture *materializationFixture, expected WorkspaceMaterializationRecord) (*WorkspaceMutationTx, context.Context, WorkspaceMaterializationRecord) {
			conn, err := fixture.store.DB().Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			return &WorkspaceMutationTx{conn: conn, scope: types.WorkspaceScope{}}, context.Background(), expected
		}},
		{"missing workspace", func(t *testing.T, fixture *materializationFixture, expected WorkspaceMaterializationRecord) (*WorkspaceMutationTx, context.Context, WorkspaceMaterializationRecord) {
			conn, err := fixture.store.DB().Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			return &WorkspaceMutationTx{conn: conn, scope: types.WorkspaceScope{
				ProjectID: "00000000-0000-4000-8000-000000000099", WorkspaceID: "00000000-0000-4000-8000-000000000098",
			}}, context.Background(), expected
		}},
		{"closed connection", func(t *testing.T, fixture *materializationFixture, expected WorkspaceMaterializationRecord) (*WorkspaceMutationTx, context.Context, WorkspaceMaterializationRecord) {
			conn, err := fixture.store.DB().Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err := conn.Close(); err != nil {
				t.Fatal(err)
			}
			return &WorkspaceMutationTx{conn: conn, scope: fixture.binding.Scope}, context.Background(), expected
		}},
		{"canceled context", func(t *testing.T, fixture *materializationFixture, expected WorkspaceMaterializationRecord) (*WorkspaceMutationTx, context.Context, WorkspaceMaterializationRecord) {
			conn, err := fixture.store.DB().Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return &WorkspaceMutationTx{conn: conn, scope: fixture.binding.Scope}, ctx, expected
		}},
		{"retained closed transaction", func(t *testing.T, fixture *materializationFixture, expected WorkspaceMaterializationRecord) (*WorkspaceMutationTx, context.Context, WorkspaceMaterializationRecord) {
			var retained *WorkspaceMutationTx
			if err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				retained = tx
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			return retained, context.Background(), expected
		}},
		{"no eligible row", func(t *testing.T, fixture *materializationFixture, expected WorkspaceMaterializationRecord) (*WorkspaceMutationTx, context.Context, WorkspaceMaterializationRecord) {
			mustExecMaterialization(t, fixture.store, `UPDATE workspace_materializations SET state='recovered_old'`)
			conn, err := fixture.store.DB().Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			return &WorkspaceMutationTx{conn: conn, scope: fixture.binding.Scope}, context.Background(), expected
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMaterializationFixture(t, "published", &proof)
			expected := readMaterializationDisposition(t, fixture.repo, fixture.binding.Scope).Journals[0]
			tx, ctx, expected := test.prepare(t, fixture, expected)
			before := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB())
			got, err := tx.AcceptMaterialization(ctx, expected)
			if err == nil || !reflect.DeepEqual(got, WorkspaceMaterializationRecord{}) {
				t.Fatalf("invalid API=(%+v,%v), want zero,error", got, err)
			}
			if after := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB()); !reflect.DeepEqual(after, before) {
				t.Fatalf("invalid API changed raw state: got %#v want %#v", after, before)
			}
		})
	}
}

func TestWorkspaceMutationTxAcceptMaterializationMetadataHelperErrors(t *testing.T) {
	proof := "{\"schema_version\":1,\"initial_through_generation\":3,\"operations\":[]}\n"
	fixture := newMaterializationFixture(t, "published", &proof)
	conn, err := fixture.store.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tx := &WorkspaceMutationTx{conn: conn, scope: fixture.binding.Scope}
	if _, err := tx.materializationMutationMetadata(context.Background(), "missing-journal"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing metadata error=%v, want ErrNotFound", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.materializationMutationMetadata(context.Background(), "legacy-journal"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("closed metadata error=%v, want ordinary query error", err)
	}
	mustExecMaterialization(t, fixture.store, `UPDATE workspace_materializations SET stage_path=CAST(stage_path AS BLOB)`)
	conn, err = fixture.store.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	tx = &WorkspaceMutationTx{conn: conn, scope: fixture.binding.Scope}
	if _, err := tx.materializationMutationMetadata(context.Background(), "legacy-journal"); err == nil {
		t.Fatal("invalid metadata storage succeeded")
	}
}

func TestWorkspaceMutationTxAcceptMaterializationPreservesHistoryAcrossRestartAndIsolation(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	a := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	b := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	raw := "{\"schema_version\":1,\"initial_through_generation\":3,\"operations\":[]}\n"
	makeMaterializationFixture(t, store, repo, a, "recovered_new", &raw)
	makeMaterializationFixture(t, store, repo, b, "published", &raw)
	insertWorkspaceOperation(t, store, a.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "materialized")
	beforeOperations := readWorkspaceOperations(t, store, a.Scope)
	var got WorkspaceMaterializationRecord
	err = repo.WithImmediateWorkspace(context.Background(), a.Scope, func(tx *WorkspaceMutationTx) error {
		expected, err := tx.AcceptanceEligibleMaterialization(context.Background())
		if err != nil {
			return err
		}
		got, err = tx.AcceptMaterialization(context.Background(), *expected)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	wantPrior := bytes.Clone(got.PriorTree[0].Data)
	got.PriorTree[0].Data[0] ^= 0xff
	*got.IncludedOperationsJSON = "caller mutation"
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restartedRepo := NewWorkspaceRepo(restarted.DB())
	afterA := readMaterializationDisposition(t, restartedRepo, a.Scope)
	afterB := readMaterializationDisposition(t, restartedRepo, b.Scope)
	if len(afterA.Journals) != 1 || afterA.Journals[0].State != "accepted" || afterA.Journals[0].IncludedOperationsJSON == nil ||
		*afterA.Journals[0].IncludedOperationsJSON != raw || !bytes.Equal(afterA.Journals[0].PriorTree[0].Data, wantPrior) {
		t.Fatalf("restarted accepted journal=%+v", afterA.Journals)
	}
	if !reflect.DeepEqual(afterA.Operations, beforeOperations) {
		t.Fatalf("materialized history changed: got %+v want %+v", afterA.Operations, beforeOperations)
	}
	if len(afterB.Journals) != 1 || afterB.Journals[0].State != "published" {
		t.Fatalf("sibling journal changed=%+v", afterB.Journals)
	}
}

func TestWorkspaceMaterializationDispositionReturnsOrderedCompleteIsolatedState(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	a := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	b := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	c := createBinding(t, repo, "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000013", "/checkout-c", 3, 13)
	fixtureA := makeMaterializationFixture(t, store, repo, a, "prepared", nil)
	makeMaterializationFixture(t, store, repo, b, "accepted", nil)
	makeMaterializationFixture(t, store, repo, c, "accepted", nil)
	if _, err := store.DB().Exec(`DROP INDEX workspace_one_current_materialization`); err != nil {
		t.Fatal(err)
	}
	raw := " {\"operations\": [1]}\n"
	for _, journal := range []struct {
		id, state string
		included  *string
	}{
		{"journal-z", "published", &raw},
		{"journal-b", "accepted", nil},
		{"journal-y", "recovered_new", nil},
		{"journal-a", "recovered_old", &raw},
	} {
		insertMaterializationRow(t, store, a, journal.id, journal.state, fixtureA.priorTree, fixtureA.candidateTree, fixtureA.priorDigest, fixtureA.candidateDigest, journal.included)
	}
	historicalDigest := "sha256:" + strings.Repeat("f", 64)
	mustExecMaterialization(t, store, `
		UPDATE workspace_materializations SET accepted_base_digest=?
		WHERE project_id=? AND workspace_id=? AND state IN ('accepted','recovered_old')
	`, historicalDigest, a.Scope.ProjectID, a.Scope.WorkspaceID)

	wantOperationBytes := map[int64][]byte{}
	for _, operation := range []struct {
		generation int64
		id, state  string
	}{
		{3, "00000000-0000-4000-8000-000000000093", "discarded"},
		{1, "00000000-0000-4000-8000-000000000091", "active"},
		{2, "00000000-0000-4000-8000-000000000092", "materialized"},
	} {
		wantOperationBytes[operation.generation] = insertWorkspaceOperation(t, store, a.Scope, operation.generation, validWorkspaceOperation(operation.id), operation.state)
	}
	insertWorkspaceOperation(t, store, b.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000099"), "active")
	insertWorkspaceOperation(t, store, c.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000098"), "active")

	var got WorkspaceMaterializationDisposition
	if err := repo.WithImmediateWorkspace(context.Background(), a.Scope, func(tx *WorkspaceMutationTx) error {
		var err error
		got, err = tx.MaterializationDisposition(context.Background())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	wantJournalIDs := []string{"journal-a", "journal-b", "journal-y", "journal-z", "legacy-journal"}
	if len(got.Journals) != len(wantJournalIDs) {
		t.Fatalf("journals=%d, want %d", len(got.Journals), len(wantJournalIDs))
	}
	for index, wantID := range wantJournalIDs {
		if got.Journals[index].JournalID != wantID {
			t.Fatalf("journal order=%v, want %v", materializationJournalIDs(got.Journals), wantJournalIDs)
		}
	}
	if len(got.Operations) != 3 {
		t.Fatalf("operations=%d, want 3", len(got.Operations))
	}
	for index, operation := range got.Operations {
		wantGeneration := int64(index + 1)
		if operation.Generation != wantGeneration || !bytes.Equal(operation.OperationJSON, wantOperationBytes[wantGeneration]) {
			t.Fatalf("operation[%d]=%+v", index, operation)
		}
	}
}

func TestWorkspaceMaterializationDispositionReturnsNonNilEmptySlicesAndPropagatesErrors(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	var got WorkspaceMaterializationDisposition
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		var err error
		got, err = tx.MaterializationDisposition(context.Background())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got.Journals == nil || got.Operations == nil || len(got.Journals) != 0 || len(got.Operations) != 0 {
		t.Fatalf("empty disposition=%+v, want non-nil empty slices", got)
	}

	var nilTx *WorkspaceMutationTx
	if _, err := nilTx.MaterializationDisposition(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nil transaction error=%v, want ErrNotFound", err)
	}
	store := repo.db
	conn, err := store.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&WorkspaceMutationTx{conn: conn, scope: binding.Scope}).MaterializationDisposition(ctx); err == nil {
		t.Fatal("canceled disposition read succeeded")
	}

	for _, table := range []string{"workspace_materializations", "workspace_overlay_operations"} {
		t.Run("missing "+table, func(t *testing.T) {
			fixture := newMaterializationFixture(t, "accepted", nil)
			mustExecMaterialization(t, fixture.store, `DROP TABLE `+table)
			err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				_, err := tx.MaterializationDisposition(context.Background())
				return err
			})
			if err == nil {
				t.Fatalf("disposition without %s succeeded", table)
			}
		})
	}
}

func TestWorkspaceMaterializationDispositionHistoricalAndCurrentBindingRules(t *testing.T) {
	otherDigest := "sha256:" + strings.Repeat("f", 64)
	for _, test := range []struct {
		state   string
		wantErr bool
	}{
		{"prepared", true},
		{"published", true},
		{"recovered_new", true},
		{"accepted", false},
		{"recovered_old", false},
	} {
		t.Run(test.state, func(t *testing.T) {
			fixture := newMaterializationFixture(t, test.state, nil)
			mustExecMaterialization(t, fixture.store, `UPDATE workspace_materializations SET accepted_base_digest=?`, otherDigest)
			err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				got, err := tx.MaterializationDisposition(context.Background())
				if err == nil && (len(got.Journals) != 1 || got.Journals[0].AcceptedBaseDigest != state.Digest(otherDigest)) {
					t.Fatalf("historical disposition=%+v", got)
				}
				return err
			})
			if (err != nil) != test.wantErr {
				t.Fatalf("error=%v, wantErr=%v", err, test.wantErr)
			}
		})
	}
}

func TestWorkspaceMaterializationDispositionRestartAndNonAliasing(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	raw := " {\"operations\":[]}\n"
	makeMaterializationFixture(t, store, repo, binding, "accepted", &raw)
	owner := "00000000-0000-4000-8000-000000000081"
	insertWorkspaceOperationOwned(t, store, binding.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "stashed", &owner)

	first := readMaterializationDisposition(t, repo, binding.Scope)
	want := readMaterializationDisposition(t, repo, binding.Scope)
	first.Journals[0].PriorTree[0].Data[0] ^= 0xff
	first.Journals[0].CandidateTree[0].Data[0] ^= 0xff
	*first.Journals[0].IncludedOperationsJSON = "changed"
	first.Operations[0].OperationJSON[0] ^= 0xff
	*first.Operations[0].StashedByStashID = "changed"
	second := readMaterializationDisposition(t, repo, binding.Scope)
	if !reflect.DeepEqual(second, want) {
		t.Fatalf("reread disposition=%+v, want %+v", second, want)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	afterRestart := readMaterializationDisposition(t, NewWorkspaceRepo(restarted.DB()), binding.Scope)
	if !reflect.DeepEqual(afterRestart, want) {
		t.Fatalf("restart disposition=%+v, want %+v", afterRestart, want)
	}
}

func TestWorkspaceMaterializationDispositionRejectsHistoricalBindingAndJournalCorruption(t *testing.T) {
	for _, test := range []struct {
		name   string
		state  string
		mutate func(*testing.T, *materializationFixture)
	}{
		{"accepted checkout", "accepted", updateMaterialization("checkout_path", "/other")},
		{"recovered-old path", "recovered_old", updateMaterialization("stage_path", "relative")},
		{"accepted timestamp", "accepted", updateMaterialization("updated_at", "2026-07-28T11:59:59Z")},
		{"recovered-old candidate digest", "recovered_old", updateMaterialization("candidate_digest", "sha256:"+strings.Repeat("f", 64))},
		{"prepared raw envelope", "prepared", updateMaterialization("included_operations_json", "")},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMaterializationFixture(t, test.state, nil)
			test.mutate(t, fixture)
			err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				_, err := tx.MaterializationDisposition(context.Background())
				return err
			})
			if err == nil {
				t.Fatal("corrupt historical journal read succeeded")
			}
		})
	}
}

func TestWorkspaceMaterializationDispositionRejectsBlobAndMalformedOperation(t *testing.T) {
	t.Run("BLOB included operations on historical journal", func(t *testing.T) {
		fixture := newMaterializationFixture(t, "accepted", nil)
		mustExecMaterialization(t, fixture.store, `UPDATE workspace_materializations SET included_operations_json=X'7b7d0a'`)
		err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
			_, err := tx.MaterializationDisposition(context.Background())
			return err
		})
		if err == nil {
			t.Fatal("BLOB included operations read succeeded")
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *materializationFixture)
	}{
		{"malformed operation JSON", func(t *testing.T, fixture *materializationFixture) {
			insertWorkspaceOperationRaw(t, fixture.store, fixture.binding.Scope, 1, "00000000-0000-4000-8000-000000000091", []byte("{}"), "active")
		}},
		{"invalid stash owner metadata", func(t *testing.T, fixture *materializationFixture) {
			owner := "bad"
			insertWorkspaceOperationOwned(t, fixture.store, fixture.binding.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "stashed", &owner)
		}},
		{"BLOB operation generation", func(t *testing.T, fixture *materializationFixture) {
			insertWorkspaceOperation(t, fixture.store, fixture.binding.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "active")
			mustExecMaterialization(t, fixture.store, `UPDATE workspace_overlay_operations SET generation=CAST(X'00' AS BLOB)`)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMaterializationFixture(t, "accepted", nil)
			test.mutate(t, fixture)
			err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				_, err := tx.MaterializationDisposition(context.Background())
				return err
			})
			if err == nil {
				t.Fatal("malformed operation read succeeded")
			}
		})
	}
}

func TestWorkspaceMaterializationDispositionErrorsReturnNonNilEmptySlices(t *testing.T) {
	t.Run("journal failure after valid rows", func(t *testing.T) {
		fixture := newMaterializationFixture(t, "accepted", nil)
		insertMaterializationRow(t, fixture.store, fixture.binding, "zzz-corrupt", "accepted",
			fixture.priorTree, fixture.candidateTree, fixture.priorDigest, fixture.candidateDigest, nil)
		mustExecMaterialization(t, fixture.store, `UPDATE workspace_materializations SET candidate_digest=? WHERE journal_id='zzz-corrupt'`,
			"sha256:"+strings.Repeat("f", 64))
		var got WorkspaceMaterializationDisposition
		err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
			var err error
			got, err = tx.MaterializationDisposition(context.Background())
			return err
		})
		assertEmptyMaterializationDispositionError(t, got, err)
	})

	t.Run("operation failure after valid journals and operation", func(t *testing.T) {
		fixture := newMaterializationFixture(t, "accepted", nil)
		insertWorkspaceOperation(t, fixture.store, fixture.binding.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "active")
		insertWorkspaceOperationRaw(t, fixture.store, fixture.binding.Scope, 2, "00000000-0000-4000-8000-000000000092", []byte("{}"), "active")
		var got WorkspaceMaterializationDisposition
		err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
			var err error
			got, err = tx.MaterializationDisposition(context.Background())
			return err
		})
		assertEmptyMaterializationDispositionError(t, got, err)
	})
}

func materializationJournalIDs(records []WorkspaceMaterializationRecord) []string {
	ids := make([]string, len(records))
	for index := range records {
		ids[index] = records[index].JournalID
	}
	return ids
}

func assertEmptyMaterializationDispositionError(t *testing.T, got WorkspaceMaterializationDisposition, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("corrupt disposition read succeeded")
	}
	if got.Journals == nil || got.Operations == nil || len(got.Journals) != 0 || len(got.Operations) != 0 {
		t.Fatalf("error disposition=%+v, want non-nil empty slices", got)
	}
}

func readMaterializationDisposition(t *testing.T, repo *WorkspaceRepo, scope types.WorkspaceScope) WorkspaceMaterializationDisposition {
	t.Helper()
	var got WorkspaceMaterializationDisposition
	if err := repo.WithImmediateWorkspace(context.Background(), scope, func(tx *WorkspaceMutationTx) error {
		var err error
		got, err = tx.MaterializationDisposition(context.Background())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestWorkspaceMaterializationReaderAPIAndInvalidTransactions(t *testing.T) {
	zero := state.Digest("sha256:" + strings.Repeat("0", 64))
	var nilTx *WorkspaceMutationTx
	if got, err := nilTx.AcceptanceEligibleMaterialization(context.Background()); !errors.Is(err, ErrNotFound) || got != nil {
		t.Fatalf("nil transaction read=(%+v,%v), want (nil,ErrNotFound)", got, err)
	}
	if got, err := nilTx.AcceptanceEligibleMaterializationByCandidateDigest(context.Background(), zero); !errors.Is(err, ErrNotFound) || got != nil {
		t.Fatalf("nil transaction digest read=(%+v,%v), want (nil,ErrNotFound)", got, err)
	}
	if got, err := (&WorkspaceMutationTx{}).AcceptanceEligibleMaterialization(context.Background()); !errors.Is(err, ErrNotFound) || got != nil {
		t.Fatalf("nil connection read=(%+v,%v), want (nil,ErrNotFound)", got, err)
	}

	fixture := newMaterializationFixture(t, "published", nil)
	conn, err := fixture.store.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	invalid := &WorkspaceMutationTx{conn: conn, scope: types.WorkspaceScope{ProjectID: "BAD", WorkspaceID: fixture.binding.Scope.WorkspaceID}}
	if got, err := invalid.AcceptanceEligibleMaterialization(context.Background()); !errors.Is(err, ErrNotFound) || got != nil {
		t.Fatalf("invalid scope read=(%+v,%v), want (nil,ErrNotFound)", got, err)
	}
	unregistered := &WorkspaceMutationTx{conn: conn, scope: types.WorkspaceScope{
		ProjectID: "00000000-0000-4000-8000-000000000099", WorkspaceID: "00000000-0000-4000-8000-000000000098",
	}}
	if got, err := unregistered.AcceptanceEligibleMaterialization(context.Background()); !errors.Is(err, ErrNotFound) || got != nil {
		t.Fatalf("unregistered scope read=(%+v,%v), want (nil,ErrNotFound)", got, err)
	}
}

func TestWorkspaceMaterializationReaderPublishedAndRecoveredNew(t *testing.T) {
	raw := " {\"operations\": [1, 2]}\n"
	for _, materializationState := range []string{"published", "recovered_new"} {
		t.Run(materializationState, func(t *testing.T) {
			fixture := newMaterializationFixture(t, materializationState, &raw)
			var got, matched, mismatched *WorkspaceMaterializationRecord
			err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				var err error
				got, err = tx.AcceptanceEligibleMaterialization(context.Background())
				if err != nil {
					return err
				}
				matched, err = tx.AcceptanceEligibleMaterializationByCandidateDigest(context.Background(), fixture.candidateDigest)
				if err != nil {
					return err
				}
				mismatched, err = tx.AcceptanceEligibleMaterializationByCandidateDigest(context.Background(), fixture.priorDigest)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			assertMaterializationRecord(t, got, fixture, materializationState, &raw)
			assertMaterializationRecord(t, matched, fixture, materializationState, &raw)
			if mismatched != nil {
				t.Fatalf("digest mismatch returned %+v", mismatched)
			}
			if got.IncludedOperationsJSON == matched.IncludedOperationsJSON {
				t.Fatal("separate reads aliased included-operations pointer")
			}
		})
	}

	t.Run("opaque non-JSON raw envelope", func(t *testing.T) {
		raw := "{\n"
		fixture := newMaterializationFixture(t, "published", &raw)
		got := readEligibleMaterialization(t, fixture.repo, fixture.binding.Scope)
		if got.IncludedOperationsJSON == nil || *got.IncludedOperationsJSON != raw {
			t.Fatalf("included operations=%v, want byte-exact %q", got.IncludedOperationsJSON, raw)
		}
	})
}

func TestWorkspaceMaterializationReaderNilRawRestartAndNonAliasing(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	fixture := makeMaterializationFixture(t, store, repo, binding, "published", nil)
	first := readEligibleMaterialization(t, repo, binding.Scope)
	assertMaterializationRecord(t, first, fixture, "published", nil)
	wantPrior := bytes.Clone(first.PriorTree[0].Data)
	wantCandidate := bytes.Clone(first.CandidateTree[0].Data)
	wantStage, wantBackup := first.StagePath, first.BackupPath
	wantReview, wantPriorCandidate := *first.PublicationReviewJSON, *first.PriorCandidateJSON
	cloned := cloneWorkspaceMaterializationRecord(*first)
	cloned.StagePath = "/clone-stage"
	cloned.BackupPath = "/clone-backup"
	*cloned.PublicationReviewJSON = "clone review"
	*cloned.PriorCandidateJSON = "clone prior candidate"
	if first.StagePath != wantStage || first.BackupPath != wantBackup || *first.PublicationReviewJSON != wantReview ||
		*first.PriorCandidateJSON != wantPriorCandidate {
		t.Fatalf("mutating cloned proof aliased source record: %+v", first)
	}
	first.PriorTree[0].Data[0] ^= 0xff
	first.CandidateTree[0].Data[0] ^= 0xff
	first.StagePath = "/mutated-stage"
	first.BackupPath = "/mutated-backup"
	*first.PublicationReviewJSON = "mutated review"
	*first.PriorCandidateJSON = "mutated prior candidate"
	if first.IncludedOperationsJSON != nil {
		t.Fatal("legacy SQL NULL became non-nil")
	}
	second := readEligibleMaterialization(t, repo, binding.Scope)
	if !bytes.Equal(second.PriorTree[0].Data, wantPrior) || !bytes.Equal(second.CandidateTree[0].Data, wantCandidate) {
		t.Fatal("mutating returned trees aliased persisted/read state")
	}
	if second.StagePath != wantStage || second.BackupPath != wantBackup || second.PublicationReviewProofVersion != 1 ||
		second.PublicationReviewJSON == nil || *second.PublicationReviewJSON != wantReview ||
		second.PriorCandidateJSON == nil || *second.PriorCandidateJSON != wantPriorCandidate {
		t.Fatalf("mutating returned proof aliased persisted/read state: %+v", second)
	}
	if first.PublicationReviewJSON == second.PublicationReviewJSON || first.PriorCandidateJSON == second.PriorCandidateJSON {
		t.Fatal("separate proof reads aliased pointers")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	afterRestart := readEligibleMaterialization(t, NewWorkspaceRepo(restarted.DB()), binding.Scope)
	if !reflect.DeepEqual(second, afterRestart) {
		t.Fatalf("restart record=%+v, want %+v", afterRestart, second)
	}
}

func TestWorkspaceMaterializationReaderExactScopeAndAbsent(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	a := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	b := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	c := createBinding(t, repo, "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000013", "/checkout-c", 3, 13)
	makeMaterializationFixture(t, store, repo, b, "published", nil)
	makeMaterializationFixture(t, store, repo, c, "published", nil)
	mustExecMaterialization(t, store, `
		UPDATE workspace_materializations
		SET publication_review_json=' review-b ',prior_candidate_json=' prior-b ',stage_path='/stage-b',backup_path='/backup-b'
		WHERE project_id=? AND workspace_id=?
	`, b.Scope.ProjectID, b.Scope.WorkspaceID)
	mustExecMaterialization(t, store, `
		UPDATE workspace_materializations
		SET publication_review_json=' review-c ',prior_candidate_json=' prior-c ',stage_path='/stage-c',backup_path='/backup-c'
		WHERE project_id=? AND workspace_id=?
	`, c.Scope.ProjectID, c.Scope.WorkspaceID)
	var got *WorkspaceMaterializationRecord
	if err := repo.WithImmediateWorkspace(context.Background(), a.Scope, func(tx *WorkspaceMutationTx) error {
		var err error
		got, err = tx.AcceptanceEligibleMaterialization(context.Background())
		return err
	}); err != nil || got != nil {
		t.Fatalf("other-workspace read=(%+v,%v), want (nil,nil)", got, err)
	}

	fixtureA := makeMaterializationFixture(t, store, repo, a, "recovered_new", nil)
	assertMaterializationRecord(t, readEligibleMaterialization(t, repo, a.Scope), fixtureA, "recovered_new", nil)
	mustExecMaterialization(t, store, `
		UPDATE workspace_materializations
		SET publication_review_json=' review-a ',prior_candidate_json=' prior-a ',stage_path='/stage-a',backup_path='/backup-a'
		WHERE project_id=? AND workspace_id=?
	`, a.Scope.ProjectID, a.Scope.WorkspaceID)
	gotA := readEligibleMaterialization(t, repo, a.Scope)
	if gotA == nil || gotA.StagePath != "/stage-a" || gotA.BackupPath != "/backup-a" ||
		*gotA.PublicationReviewJSON != " review-a " || *gotA.PriorCandidateJSON != " prior-a " {
		t.Fatalf("workspace A proof read=%+v", gotA)
	}
	if gotB := readEligibleMaterialization(t, repo, b.Scope); gotB == nil || gotB.Checkout != b.Checkout ||
		gotB.StagePath != "/stage-b" || gotB.BackupPath != "/backup-b" ||
		*gotB.PublicationReviewJSON != " review-b " || *gotB.PriorCandidateJSON != " prior-b " {
		t.Fatalf("workspace B read=%+v", gotB)
	}
	if gotC := readEligibleMaterialization(t, repo, c.Scope); gotC == nil || gotC.Checkout != c.Checkout ||
		gotC.StagePath != "/stage-c" || gotC.BackupPath != "/backup-c" ||
		*gotC.PublicationReviewJSON != " review-c " || *gotC.PriorCandidateJSON != " prior-c " {
		t.Fatalf("project C read=%+v", gotC)
	}
}

func TestWorkspaceMaterializationProofReaderRejectsEnvelopeCorruptionWithoutMutation(t *testing.T) {
	for _, column := range []string{"publication_review_json", "prior_candidate_json"} {
		for _, test := range []struct {
			name  string
			query string
			args  []any
		}{
			{"empty", `UPDATE workspace_materializations SET ` + column + `=?`, []any{""}},
			{"NUL", `UPDATE workspace_materializations SET ` + column + `=?`, []any{"proof\x00bytes"}},
			{"invalid UTF-8 TEXT", `UPDATE workspace_materializations SET ` + column + `=CAST(X'ff' AS TEXT)`, nil},
			{"BLOB storage", `UPDATE workspace_materializations SET ` + column + `=X'7b7d0a'`, nil},
		} {
			t.Run(column+" "+test.name, func(t *testing.T) {
				raw := "{}\n"
				fixture := newMaterializationFixture(t, "published", &raw)
				mustExecMaterialization(t, fixture.store, test.query, test.args...)
				assertCorruptMaterializationProofReadsWithoutMutation(t, fixture)
			})
		}
	}
}

func TestWorkspaceMaterializationProofReaderRejectsVersionAndShapeCorruptionWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		name  string
		query string
	}{
		{"version BLOB storage", `UPDATE workspace_materializations SET publication_review_proof_version=CAST(X'31' AS BLOB)`},
		{"unsupported version", `UPDATE workspace_materializations SET publication_review_proof_version=2`},
		{"v1 missing review", `UPDATE workspace_materializations SET publication_review_json=NULL`},
		{"v1 missing prior", `UPDATE workspace_materializations SET prior_candidate_json=NULL`},
		{"v0 with both proofs", `UPDATE workspace_materializations SET publication_review_proof_version=0`},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := "{}\n"
			fixture := newMaterializationFixture(t, "published", &raw)
			withIgnoredMaterializationChecks(t, fixture.store.DB(), test.query)
			assertCorruptMaterializationProofReadsWithoutMutation(t, fixture)
		})
	}
}

func TestWorkspaceMaterializationReaderValidatesFullSetBeforeDigestFiltering(t *testing.T) {
	fixture := newMaterializationFixture(t, "published", nil)
	if _, err := fixture.store.DB().Exec(`DROP INDEX workspace_one_current_materialization`); err != nil {
		t.Fatal(err)
	}
	secondSnapshot, err := state.DecodeTree(fixture.candidateTree)
	if err != nil {
		t.Fatal(err)
	}
	secondSnapshot.Project.Name = "Second candidate"
	secondSnapshot.Project.UpdatedAt = secondSnapshot.Project.UpdatedAt.Add(time.Minute)
	secondTree, err := state.EncodeTree(secondSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := state.DigestTree(secondTree)
	if err != nil {
		t.Fatal(err)
	}
	insertMaterializationRow(t, fixture.store, fixture.binding, "legacy-second", "recovered_new",
		fixture.priorTree, secondTree, fixture.priorDigest, secondDigest, nil)
	var got *WorkspaceMaterializationRecord
	err = fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
		var err error
		got, err = tx.AcceptanceEligibleMaterializationByCandidateDigest(context.Background(), fixture.candidateDigest)
		return err
	})
	if err == nil || got != nil {
		t.Fatalf("duplicate digest read=(%+v,%v), want corruption error", got, err)
	}

	if _, err := fixture.store.DB().Exec(`UPDATE workspace_materializations SET candidate_digest=? WHERE journal_id='legacy-second'`,
		"sha256:"+strings.Repeat("A", 64)); err != nil {
		t.Fatal(err)
	}
	err = fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.AcceptanceEligibleMaterializationByCandidateDigest(context.Background(), fixture.candidateDigest)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "candidate digest") {
		t.Fatalf("corrupt duplicate error=%v, want candidate-digest validation before uniqueness", err)
	}
}

func TestWorkspaceMaterializationReaderRejectsControlPathsAndInvalidFilterDigest(t *testing.T) {
	t.Run("control character in path", func(t *testing.T) {
		fixture := newMaterializationFixture(t, "published", nil)
		mustExecMaterialization(t, fixture.store, `UPDATE workspace_materializations SET stage_path=?`, "/stage\nname")
		err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
			_, err := tx.AcceptanceEligibleMaterialization(context.Background())
			return err
		})
		if err == nil {
			t.Fatal("control-containing stage path succeeded")
		}
	})

	t.Run("noncanonical digest with empty eligible set", func(t *testing.T) {
		_, repo := openWorkspaceStore(t)
		binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
		var got *WorkspaceMaterializationRecord
		err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
			var err error
			got, err = tx.AcceptanceEligibleMaterializationByCandidateDigest(context.Background(), state.Digest("sha256:"+strings.Repeat("A", 64)))
			return err
		})
		if err == nil || got != nil || errors.Is(err, ErrNotFound) {
			t.Fatalf("invalid digest read=(%+v,%v), want validation error", got, err)
		}
	})
}

func TestWorkspaceMaterializationReaderRejectsCorruption(t *testing.T) {
	otherDigest := "sha256:" + strings.Repeat("f", 64)
	corruptions := []struct {
		name   string
		mutate func(*testing.T, *materializationFixture)
	}{
		{"empty journal", updateMaterialization("journal_id", "")},
		{"NUL journal", updateMaterialization("journal_id", "bad\x00journal")},
		{"uppercase expected digest", updateMaterialization("expected_live_digest", "sha256:"+strings.Repeat("A", 64))},
		{"accepted base mismatch", updateMaterialization("accepted_base_digest", otherDigest)},
		{"checkout path mismatch", updateMaterialization("checkout_path", "/other")},
		{"checkout device mismatch", updateMaterialization("checkout_device", int64(99))},
		{"checkout inode mismatch", updateMaterialization("checkout_inode", int64(99))},
		{"uppercase prior digest", updateMaterialization("prior_tree_digest", "sha256:"+strings.Repeat("A", 64))},
		{"candidate digest mismatch", updateMaterialization("candidate_digest", otherDigest)},
		{"negative generation", updateMaterialization("through_generation", int64(-1))},
		{"malformed prior file list", updateMaterialization("prior_tree", []byte("broken"))},
		{"malformed candidate file list", updateMaterialization("candidate_tree", []byte("broken"))},
		{"relative stage path", updateMaterialization("stage_path", "stage")},
		{"dirty stage path", updateMaterialization("stage_path", "/tmp/../stage")},
		{"same stage and backup", updateMaterialization("backup_path", "/stage")},
		{"zero creation timestamp", updateMaterialization("created_at", "0001-01-01T00:00:00Z")},
		{"offset creation timestamp", updateMaterialization("created_at", "2026-07-28T12:00:00+01:00")},
		{"offset update timestamp", updateMaterialization("updated_at", "2026-07-28T12:00:01+01:00")},
		{"updated before created", updateMaterialization("updated_at", "2026-07-28T11:59:59Z")},
		{"empty included operations", updateMaterialization("included_operations_json", "")},
		{"NUL included operations", updateMaterialization("included_operations_json", "{}\x00")},
		{"expected differs from prior", updateMaterialization("expected_live_digest", otherDigest)},
		{"binding accepted digest corrupt", func(t *testing.T, f *materializationFixture) {
			mustExecMaterialization(t, f.store, `UPDATE workspace_bindings SET accepted_digest=? WHERE project_id=? AND workspace_id=?`,
				"sha256:"+strings.Repeat("A", 64), f.binding.Scope.ProjectID, f.binding.Scope.WorkspaceID)
		}},
		{"candidate project mismatch", func(t *testing.T, f *materializationFixture) {
			tree := workspaceTree(t, "00000000-0000-4000-8000-000000000099", f.binding.Repository)
			encoded, err := encodeFileList(tree)
			if err != nil {
				t.Fatal(err)
			}
			mustExecMaterialization(t, f.store, `UPDATE workspace_materializations SET candidate_tree=?`, encoded)
		}},
		{"candidate repository mismatch", func(t *testing.T, f *materializationFixture) {
			repository := types.RepositoryIdentity{Provider: "github", ImmutableID: "repository-2", CanonicalRemote: "https://github.com/acme/other"}
			tree := workspaceTree(t, f.binding.Scope.ProjectID, repository)
			encoded, err := encodeFileList(tree)
			if err != nil {
				t.Fatal(err)
			}
			mustExecMaterialization(t, f.store, `UPDATE workspace_materializations SET candidate_tree=?`, encoded)
		}},
		{"prior project mismatch with matching digest", func(t *testing.T, f *materializationFixture) {
			updatePriorMaterializationTree(t, f, workspaceTree(t, "00000000-0000-4000-8000-000000000099", f.binding.Repository))
		}},
		{"prior repository mismatch with matching digest", func(t *testing.T, f *materializationFixture) {
			repository := types.RepositoryIdentity{Provider: "github", ImmutableID: "repository-2", CanonicalRemote: "https://github.com/acme/other"}
			updatePriorMaterializationTree(t, f, workspaceTree(t, f.binding.Scope.ProjectID, repository))
		}},
	}
	for _, corruption := range corruptions {
		t.Run(corruption.name, func(t *testing.T) {
			raw := "{}\n"
			fixture := newMaterializationFixture(t, "published", &raw)
			corruption.mutate(t, fixture)
			err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				got, err := tx.AcceptanceEligibleMaterialization(context.Background())
				if got != nil {
					t.Fatalf("corrupt read returned %+v", got)
				}
				return err
			})
			if err == nil {
				t.Fatal("corrupt materialization read succeeded")
			}
		})
	}
}

func TestWorkspaceMaterializationReaderRejectsInvalidUTF8AndDatabaseErrors(t *testing.T) {
	t.Run("included operations BLOB storage class", func(t *testing.T) {
		fixture := newMaterializationFixture(t, "published", nil)
		mustExecMaterialization(t, fixture.store, `UPDATE workspace_materializations SET included_operations_json=X'7b7d0a'`)
		err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
			_, err := tx.AcceptanceEligibleMaterialization(context.Background())
			return err
		})
		if err == nil {
			t.Fatal("BLOB included operations read succeeded")
		}
	})

	for _, column := range []string{"journal_id", "included_operations_json"} {
		t.Run("invalid UTF-8 "+column, func(t *testing.T) {
			fixture := newMaterializationFixture(t, "published", nil)
			mustExecMaterialization(t, fixture.store, `UPDATE workspace_materializations SET `+column+`=CAST(X'ff' AS TEXT)`)
			err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				_, err := tx.AcceptanceEligibleMaterialization(context.Background())
				return err
			})
			if err == nil {
				t.Fatal("invalid UTF-8 read succeeded")
			}
		})
	}

	t.Run("canceled context", func(t *testing.T) {
		fixture := newMaterializationFixture(t, "published", nil)
		conn, err := fixture.store.DB().Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if got, err := (&WorkspaceMutationTx{conn: conn, scope: fixture.binding.Scope}).AcceptanceEligibleMaterialization(ctx); err == nil || got != nil {
			t.Fatalf("canceled read=(%+v,%v)", got, err)
		}
	})
	t.Run("closed connection", func(t *testing.T) {
		fixture := newMaterializationFixture(t, "published", nil)
		conn, err := fixture.store.DB().Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		if got, err := (&WorkspaceMutationTx{conn: conn, scope: fixture.binding.Scope}).AcceptanceEligibleMaterialization(context.Background()); err == nil || got != nil {
			t.Fatalf("closed-connection read=(%+v,%v)", got, err)
		}
	})
	t.Run("missing table", func(t *testing.T) {
		fixture := newMaterializationFixture(t, "published", nil)
		mustExecMaterialization(t, fixture.store, `DROP TABLE workspace_materializations`)
		err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
			_, err := tx.AcceptanceEligibleMaterialization(context.Background())
			return err
		})
		if err == nil {
			t.Fatal("read without materialization table succeeded")
		}
	})
	t.Run("scan type", func(t *testing.T) {
		fixture := newMaterializationFixture(t, "published", nil)
		mustExecMaterialization(t, fixture.store, `UPDATE workspace_materializations SET through_generation=CAST(X'00' AS BLOB)`)
		err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
			_, err := tx.AcceptanceEligibleMaterialization(context.Background())
			return err
		})
		if err == nil {
			t.Fatal("unscannable row succeeded")
		}
	})
}

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

func seedAtomicWorkspaceAdjacency(t *testing.T, store *Store, repo *WorkspaceRepo, fixture *materializationFixture) {
	t.Helper()
	snapshot, encoded := encodedWorkspaceSnapshot(t, fixture.binding.Scope.ProjectID, fixture.binding.Repository)
	insertWorkspaceCandidate(t, store, fixture.binding.Scope, state.Digest(fixture.binding.AcceptedTreeDigest), snapshot.Digest, encoded, nil, 0)
	insertWorkspaceOperation(t, store, fixture.binding.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "active")
	insertWorkspaceConflict(t, store, fixture.binding.Scope, "atomic-adjacent-conflict", state.RecordKey{Kind: "task", ID: "00000000-0000-4000-8000-000000000021"}, "open")
	proof := "{\"schema_version\":1,\"initial_through_generation\":0,\"operations\":[]}\n"
	insertMaterializationRow(t, store, fixture.binding, "historical-journal", "accepted", fixture.priorTree, fixture.candidateTree, fixture.priorDigest, fixture.candidateDigest, &proof)
	stash := validWorkspaceStash(t, fixture.binding, "00000000-0000-4000-8000-000000000031")
	receipt := validWorkspaceTransitionReceipt(t, "00000000-0000-4000-8000-000000000041", "stash", "clean")
	if err := repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
		if err := tx.InsertStash(context.Background(), stash); err != nil {
			return err
		}
		return tx.InsertTransitionReceipt(context.Background(), receipt)
	}); err != nil {
		t.Fatalf("seed atomic stash and transition receipt: %v", err)
	}
	createBinding(t, repo, fixture.binding.Scope.ProjectID, "00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	createBinding(t, repo, "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000013", "/checkout-c", 3, 13)
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

func readEligibleMaterialization(t *testing.T, repo *WorkspaceRepo, scope types.WorkspaceScope) *WorkspaceMaterializationRecord {
	t.Helper()
	var got *WorkspaceMaterializationRecord
	if err := repo.WithImmediateWorkspace(context.Background(), scope, func(tx *WorkspaceMutationTx) error {
		var err error
		got, err = tx.AcceptanceEligibleMaterialization(context.Background())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return got
}

func assertMaterializationRecord(t *testing.T, got *WorkspaceMaterializationRecord, fixture *materializationFixture, materializationState string, included *string) {
	t.Helper()
	if got == nil {
		t.Fatal("materialization is nil")
	}
	if got.JournalID != "legacy-journal" || got.ExpectedLiveDigest != fixture.priorDigest ||
		got.AcceptedBaseDigest != state.Digest(fixture.binding.AcceptedTreeDigest) || got.Checkout != fixture.binding.Checkout ||
		got.PriorTreeDigest != fixture.priorDigest || got.CandidateDigest != fixture.candidateDigest ||
		got.ThroughGeneration != 3 || got.StagePath != "/stage" || got.BackupPath != "/backup" ||
		got.PublicationReviewProofVersion != 1 || got.PublicationReviewJSON == nil || *got.PublicationReviewJSON != " review\n" ||
		got.PriorCandidateJSON == nil || *got.PriorCandidateJSON != " prior-candidate\n" || got.State != materializationState ||
		!reflect.DeepEqual(got.PriorTree, fixture.priorTree) || !reflect.DeepEqual(got.CandidateTree, fixture.candidateTree) {
		t.Fatalf("materialization=%+v", got)
	}
	if included == nil {
		if got.IncludedOperationsJSON != nil {
			t.Fatalf("included=%q, want nil", *got.IncludedOperationsJSON)
		}
	} else if got.IncludedOperationsJSON == nil || *got.IncludedOperationsJSON != *included {
		t.Fatalf("included=%v, want byte-exact %q", got.IncludedOperationsJSON, *included)
	}
}

func assertCorruptMaterializationProofReadsWithoutMutation(t *testing.T, fixture *materializationFixture) {
	t.Helper()
	before := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB())
	var disposition WorkspaceMaterializationDisposition
	err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
		var err error
		disposition, err = tx.MaterializationDisposition(context.Background())
		return err
	})
	assertEmptyMaterializationDispositionError(t, disposition, err)
	if after := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB()); !reflect.DeepEqual(after, before) {
		t.Fatalf("corrupt disposition read changed raw state: got %#v want %#v", after, before)
	}
	var eligible *WorkspaceMaterializationRecord
	err = fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
		var err error
		eligible, err = tx.AcceptanceEligibleMaterialization(context.Background())
		return err
	})
	if err == nil || eligible != nil {
		t.Fatalf("corrupt eligible read=(%+v,%v), want nil,error", eligible, err)
	}
	if after := readAtomicWorkspaceRawSnapshot(t, fixture.store.DB()); !reflect.DeepEqual(after, before) {
		t.Fatalf("corrupt eligible read changed raw state: got %#v want %#v", after, before)
	}
}

func withIgnoredMaterializationChecks(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `PRAGMA ignore_check_constraints=OFF`); err != nil {
		t.Fatal(err)
	}
}

func updateMaterialization(column string, value any) func(*testing.T, *materializationFixture) {
	return func(t *testing.T, fixture *materializationFixture) {
		t.Helper()
		mustExecMaterialization(t, fixture.store, `UPDATE workspace_materializations SET `+column+`=?`, value)
	}
}

func updatePriorMaterializationTree(t *testing.T, fixture *materializationFixture, tree state.Tree) {
	t.Helper()
	encoded, err := encodeFileList(tree)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := state.DigestTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	mustExecMaterialization(t, fixture.store, `
		UPDATE workspace_materializations
		SET prior_tree=?,prior_tree_digest=?,expected_live_digest=?
	`, encoded, digest, digest)
}

func mustExecMaterialization(t *testing.T, store *Store, query string, args ...any) {
	t.Helper()
	if _, err := store.DB().Exec(query, args...); err != nil {
		t.Fatalf("materialization SQL: %v", err)
	}
}

func corruptMaterializationScopeKeyToBlob(t *testing.T, store *Store, journalID, column string) {
	t.Helper()
	conn, err := store.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`); err != nil {
			t.Errorf("restore materialization foreign keys: %v", err)
		}
		if err := conn.Close(); err != nil {
			t.Errorf("close materialization corruption connection: %v", err)
		}
	}()
	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	var enabled int
	if err := conn.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 0 {
		t.Fatalf("foreign key enforcement=%d, want disabled for controlled corruption", enabled)
	}
	query := `UPDATE workspace_materializations SET ` + quoteSQLiteTestIdentifier(column) +
		`=CAST(` + quoteSQLiteTestIdentifier(column) + ` AS BLOB) WHERE journal_id=?`
	result, err := conn.ExecContext(context.Background(), query, journalID)
	if err != nil {
		t.Fatalf("corrupt materialization scope key: %v", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		t.Fatalf("corrupt materialization scope key affected=%d err=%v, want 1", affected, err)
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
	left.mutationMetadata = workspaceMaterializationMutationMetadata{}
	right.mutationMetadata = workspaceMaterializationMutationMetadata{}
	return equalWorkspaceMaterializationRecords(left, right)
}

func stringPointerForTest(value string) *string {
	return &value
}

type materializationAdmissionDriver struct {
	inner       driver.Driver
	beginIssued chan struct{}
	once        sync.Once
}

func (wrapped *materializationAdmissionDriver) Open(name string) (driver.Conn, error) {
	connection, err := wrapped.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &materializationAdmissionConn{Conn: connection, admission: wrapped}, nil
}

type materializationAdmissionConn struct {
	driver.Conn
	admission *materializationAdmissionDriver
}

func (connection *materializationAdmissionConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if strings.EqualFold(strings.TrimSpace(query), "BEGIN IMMEDIATE") {
		connection.admission.once.Do(func() { close(connection.admission.beginIssued) })
	}
	return connection.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (connection *materializationAdmissionConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return connection.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (connection *materializationAdmissionConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	return connection.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
}

func (connection *materializationAdmissionConn) Ping(ctx context.Context) error {
	return connection.Conn.(driver.Pinger).Ping(ctx)
}

var materializationAdmissionSequence atomic.Uint64

func openMaterializationAdmissionDB(t *testing.T, path string) (*sql.DB, <-chan struct{}) {
	t.Helper()
	admission := &materializationAdmissionDriver{inner: &sqlite.Driver{}, beginIssued: make(chan struct{})}
	driverName := fmt.Sprintf("localstore-materialization-admission-%d", materializationAdmissionSequence.Add(1))
	sql.Register(driverName, admission)
	db, err := sql.Open(driverName, sqliteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, admission.beginIssued
}
