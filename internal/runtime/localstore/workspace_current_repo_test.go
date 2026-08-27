package localstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestCurrentWorkspaceMaterializationReturnsNilOrOneCurrentOwner(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		_, repo := openWorkspaceStore(t)
		binding := createBinding(t, repo,
			"00000000-0000-4000-8000-000000000001",
			"00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
		if got := readCurrentWorkspaceMaterialization(t, repo, binding.Scope); got != nil {
			t.Fatalf("CurrentMaterialization=%+v, want nil", got)
		}
	})

	for _, currentState := range []string{"prepared", "published", "recovered_new"} {
		t.Run(currentState, func(t *testing.T) {
			raw := "{\"schema_version\":1,\"initial_through_generation\":0,\"operations\":[]}\n"
			fixture := newMaterializationFixture(t, currentState, &raw)
			insertMaterializationRow(t, fixture.store, fixture.binding, "terminal-corrupt", "accepted",
				fixture.priorTree, fixture.candidateTree, fixture.priorDigest, fixture.candidateDigest, &raw)
			mustExecMaterialization(t, fixture.store, `
				UPDATE workspace_materializations SET candidate_digest='corrupt-terminal'
				WHERE project_id=? AND workspace_id=? AND journal_id='terminal-corrupt'
			`, fixture.binding.Scope.ProjectID, fixture.binding.Scope.WorkspaceID)

			got := readCurrentWorkspaceMaterialization(t, fixture.repo, fixture.binding.Scope)
			if got == nil || got.JournalID != "legacy-journal" || got.State != currentState {
				t.Fatalf("CurrentMaterialization=%+v, want legacy-journal/%s", got, currentState)
			}
		})
	}

	t.Run("index bypass remains fail closed", func(t *testing.T) {
		raw := "{\"schema_version\":1,\"initial_through_generation\":0,\"operations\":[]}\n"
		fixture := newMaterializationFixture(t, "prepared", &raw)
		mustExecMaterialization(t, fixture.store, `DROP INDEX workspace_one_current_materialization`)
		insertMaterializationRow(t, fixture.store, fixture.binding, "second-current", "published",
			fixture.priorTree, fixture.candidateTree, fixture.priorDigest, fixture.candidateDigest, &raw)
		var got *WorkspaceMaterializationRecord
		err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
			var err error
			got, err = tx.CurrentMaterialization(context.Background())
			return err
		})
		if err == nil || got != nil {
			t.Fatalf("CurrentMaterialization=(%+v,%v), want nil corruption error", got, err)
		}
	})
}

func TestCurrentWorkspaceReadersComposeOnlyBoundedAuthority(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	stashID := "00000000-0000-4000-8000-000000000031"
	otherStashID := "00000000-0000-4000-8000-000000000032"
	requestID := "00000000-0000-4000-8000-000000000041"
	otherRequestID := "00000000-0000-4000-8000-000000000042"

	operations := []struct {
		generation int64
		id, state  string
		stashID    *string
	}{
		{1, "00000000-0000-4000-8000-000000000091", "rebased", nil},
		{2, "00000000-0000-4000-8000-000000000092", "materialized", nil},
		{3, "00000000-0000-4000-8000-000000000093", "active", nil},
		{4, "00000000-0000-4000-8000-000000000094", "active", nil},
		{5, "00000000-0000-4000-8000-000000000095", "stashed", &stashID},
		{6, "00000000-0000-4000-8000-000000000096", "stashed", &otherStashID},
	}
	operationJSON := make(map[int64][]byte, len(operations))
	for _, operation := range operations {
		encoded := insertWorkspaceOperationOwned(t, store, binding.Scope, operation.generation,
			validWorkspaceOperation(operation.id), operation.state, operation.stashID)
		operationJSON[operation.generation] = encoded
	}
	insertWorkspaceOperationRaw(t, store, binding.Scope, 7,
		"00000000-0000-4000-8000-000000000097", []byte("{"), "discarded")

	envelope, err := state.CanonicalJSON(currentWorkspaceOperationsEnvelope{
		SchemaVersion: 1, InitialThroughGeneration: 1,
		Operations: []currentWorkspaceOperationEnvelope{{
			Generation: 2, OperationID: operations[1].id,
			OperationJSON: string(operationJSON[2]), PrepublicationState: "active",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rawEnvelope := string(envelope)
	makeMaterializationFixture(t, store, repo, binding, "prepared", &rawEnvelope)

	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		if err := tx.InsertStash(context.Background(), validWorkspaceStash(t, binding, stashID)); err != nil {
			return err
		}
		if err := tx.InsertStash(context.Background(), validWorkspaceStash(t, binding, otherStashID)); err != nil {
			return err
		}
		if err := tx.InsertTransitionReceipt(context.Background(), validWorkspaceTransitionReceipt(t, requestID, "stash", "clean")); err != nil {
			return err
		}
		return tx.InsertTransitionReceipt(context.Background(), validWorkspaceTransitionReceipt(t, otherRequestID, "restore", "clean"))
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
		UPDATE workspace_stashes SET label='bad'||char(13)||'label'
		WHERE project_id=? AND workspace_id=? AND stash_id=?;
		UPDATE workspace_transition_receipts SET result_json='{}'
		WHERE project_id=? AND workspace_id=? AND request_id=?;
		UPDATE workspace_publication_policy_history SET repository_identity_json='not-json'
		WHERE project_id=? AND workspace_id=? AND policy_revision=1
	`, binding.Scope.ProjectID, binding.Scope.WorkspaceID, otherStashID,
		binding.Scope.ProjectID, binding.Scope.WorkspaceID, otherRequestID,
		binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}

	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		current, err := tx.CurrentMaterialization(context.Background())
		if err != nil {
			return err
		}
		if current == nil || current.JournalID != "legacy-journal" || current.IncludedOperationsJSON == nil {
			t.Fatalf("current materialization=%+v", current)
		}
		var owned currentWorkspaceOperationsEnvelope
		if err := json.Unmarshal([]byte(*current.IncludedOperationsJSON), &owned); err != nil {
			t.Fatal(err)
		}
		generations := []int64{owned.Operations[0].Generation}
		exact, err := tx.OperationsByGenerations(context.Background(), generations)
		if err != nil {
			return err
		}
		if len(exact) != 1 || exact[0].Generation != 2 || exact[0].OperationID != operations[1].id ||
			!bytes.Equal(exact[0].OperationJSON, operationJSON[2]) {
			t.Fatalf("journal-owned operations=%+v", exact)
		}
		rebased, err := tx.RebasedOperationsAtOrBefore(context.Background(), 2)
		if err != nil {
			return err
		}
		active, err := tx.ActiveOperationsAfter(context.Background(), 2)
		if err != nil {
			return err
		}
		if got := workspaceOperationGenerations(rebased); !reflect.DeepEqual(got, []int64{1}) {
			t.Fatalf("rebased generations=%v, want [1]", got)
		}
		if got := workspaceOperationGenerations(active); !reflect.DeepEqual(got, []int64{3, 4}) {
			t.Fatalf("active generations=%v, want [3 4]", got)
		}
		stash, err := tx.Stash(context.Background(), stashID)
		if err != nil {
			return err
		}
		stashed, err := tx.StashedOperationsByStashID(context.Background(), stashID)
		if err != nil {
			return err
		}
		receipt, err := tx.TransitionReceipt(context.Background(), requestID)
		if err != nil {
			return err
		}
		policy, err := tx.PublicationPolicy(context.Background())
		if err != nil {
			return err
		}
		if stash == nil || stash.StashID != stashID || len(stashed) != 1 || stashed[0].Generation != 5 ||
			receipt == nil || receipt.RequestID != requestID || policy.PolicyRevision != 1 {
			t.Fatalf("named/current authority stash=%+v operations=%+v receipt=%+v policy=%+v", stash, stashed, receipt, policy)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("bounded current readers were poisoned by terminal/unrelated history: %v", err)
	}

	if err := repo.AuditWorkspaceHistory(context.Background(), binding.Scope); err == nil {
		t.Fatal("AuditWorkspaceHistory accepted corrupt retained history")
	}
}

func TestCurrentWorkspaceMaterializationStrictCorruptionAndSiblingIsolation(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	target := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	sibling := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	raw := "{\"schema_version\":1,\"initial_through_generation\":0,\"operations\":[]}\n"
	makeMaterializationFixture(t, store, repo, target, "prepared", &raw)
	makeMaterializationFixture(t, store, repo, sibling, "published", &raw)
	mustExecMaterialization(t, store, `
		UPDATE workspace_materializations SET included_operations_json=''
		WHERE project_id=? AND workspace_id=?
	`, target.Scope.ProjectID, target.Scope.WorkspaceID)
	before := readAtomicWorkspaceRawSnapshot(t, store.DB())

	var got *WorkspaceMaterializationRecord
	err := repo.WithImmediateWorkspace(context.Background(), target.Scope, func(tx *WorkspaceMutationTx) error {
		var err error
		got, err = tx.CurrentMaterialization(context.Background())
		return err
	})
	if err == nil || got != nil {
		t.Fatalf("corrupt CurrentMaterialization=(%+v,%v), want nil error", got, err)
	}
	if siblingCurrent := readCurrentWorkspaceMaterialization(t, repo, sibling.Scope); siblingCurrent == nil || siblingCurrent.State != "published" {
		t.Fatalf("sibling CurrentMaterialization=%+v", siblingCurrent)
	}
	after := readAtomicWorkspaceRawSnapshot(t, store.DB())
	if !reflect.DeepEqual(after, before) {
		t.Fatal("current materialization read changed durable evidence")
	}
}

func TestCurrentWorkspaceReaderInvalidTransactions(t *testing.T) {
	var nilTx *WorkspaceMutationTx
	if got, err := nilTx.CurrentMaterialization(context.Background()); !errors.Is(err, ErrNotFound) || got != nil {
		t.Fatalf("nil CurrentMaterialization=(%+v,%v), want nil ErrNotFound", got, err)
	}
	if got, err := (&WorkspaceMutationTx{}).CurrentMaterialization(context.Background()); !errors.Is(err, ErrNotFound) || got != nil {
		t.Fatalf("empty CurrentMaterialization=(%+v,%v), want nil ErrNotFound", got, err)
	}
}

type currentWorkspaceOperationsEnvelope struct {
	SchemaVersion            int                                 `json:"schema_version"`
	InitialThroughGeneration int64                               `json:"initial_through_generation"`
	Operations               []currentWorkspaceOperationEnvelope `json:"operations"`
}

type currentWorkspaceOperationEnvelope struct {
	Generation          int64  `json:"generation"`
	OperationID         string `json:"operation_id"`
	OperationJSON       string `json:"operation_json"`
	PrepublicationState string `json:"prepublication_state"`
}

func readCurrentWorkspaceMaterialization(t *testing.T, repo *WorkspaceRepo, scope types.WorkspaceScope) *WorkspaceMaterializationRecord {
	t.Helper()
	var current *WorkspaceMaterializationRecord
	if err := repo.WithImmediateWorkspace(context.Background(), scope, func(tx *WorkspaceMutationTx) error {
		var err error
		current, err = tx.CurrentMaterialization(context.Background())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return current
}

func workspaceOperationGenerations(operations []WorkspaceOperation) []int64 {
	generations := make([]int64, len(operations))
	for index := range operations {
		generations[index] = operations[index].Generation
	}
	return generations
}
