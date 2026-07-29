package localstore

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestWorkspaceMaterializationDispositionReturnsOrderedCompleteIsolatedState(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	a := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	b := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	c := createBinding(t, repo, "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000013", "/checkout-c", 3, 13)
	fixtureA := makeMaterializationFixture(t, store, repo, a, "prepared", nil)
	makeMaterializationFixture(t, store, repo, b, "accepted", nil)
	makeMaterializationFixture(t, store, repo, c, "accepted", nil)
	if _, err := store.DB().Exec(`DROP INDEX workspace_one_acceptance_eligible_candidate`); err != nil {
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
	first.PriorTree[0].Data[0] ^= 0xff
	first.CandidateTree[0].Data[0] ^= 0xff
	if first.IncludedOperationsJSON != nil {
		t.Fatal("legacy SQL NULL became non-nil")
	}
	second := readEligibleMaterialization(t, repo, binding.Scope)
	if !bytes.Equal(second.PriorTree[0].Data, wantPrior) || !bytes.Equal(second.CandidateTree[0].Data, wantCandidate) {
		t.Fatal("mutating returned trees aliased persisted/read state")
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
	makeMaterializationFixture(t, store, repo, b, "published", nil)
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
	if gotB := readEligibleMaterialization(t, repo, b.Scope); gotB == nil || gotB.Checkout != b.Checkout {
		t.Fatalf("workspace B read=%+v", gotB)
	}
}

func TestWorkspaceMaterializationReaderValidatesFullSetBeforeDigestFiltering(t *testing.T) {
	fixture := newMaterializationFixture(t, "published", nil)
	if _, err := fixture.store.DB().Exec(`DROP INDEX workspace_one_acceptance_eligible_candidate`); err != nil {
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
	mustExecMaterialization(t, store, `
		INSERT INTO workspace_materializations
		(project_id,workspace_id,journal_id,expected_live_digest,accepted_base_digest,
		 checkout_path,checkout_device,checkout_inode,prior_tree_digest,candidate_digest,
		 through_generation,prior_tree,candidate_tree,stage_path,backup_path,state,
		 created_at,updated_at,included_operations_json)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	`, binding.Scope.ProjectID, binding.Scope.WorkspaceID, journalID, priorDigest, binding.AcceptedTreeDigest,
		binding.Checkout.CanonicalPath, binding.Checkout.Device, binding.Checkout.Inode, priorDigest, candidateDigest,
		int64(3), priorBytes, candidateBytes, "/stage", "/backup", materializationState, created, created.Add(time.Second), included)
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
		got.ThroughGeneration != 3 || got.State != materializationState ||
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
