package localstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestWorkspaceStashCRUDRejectsNilTransaction(t *testing.T) {
	ctx := context.Background()
	var tx *WorkspaceMutationTx
	if err := tx.InsertStash(ctx, WorkspaceStashInsert{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("InsertStash error=%v, want ErrNotFound", err)
	}
	if got, err := tx.Stash(ctx, "00000000-0000-4000-8000-000000000001"); !errors.Is(err, ErrNotFound) || got != nil {
		t.Fatalf("Stash=(%+v,%v), want (nil,ErrNotFound)", got, err)
	}
	if err := tx.DeleteStash(ctx, "00000000-0000-4000-8000-000000000001"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteStash error=%v, want ErrNotFound", err)
	}
	var _ WorkspaceStashRecord
}

func TestWorkspaceStashCRUDTransactionRoundTrip(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011",
		"/checkout", 1, 11,
	)
	want := validWorkspaceStash(t, binding, "00000000-0000-4000-8000-000000000031")

	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		if err := tx.InsertStash(context.Background(), want); err != nil {
			return err
		}
		got, err := tx.Stash(context.Background(), want.StashID)
		if err != nil {
			return err
		}
		assertWorkspaceStash(t, got, want)
		if err := tx.DeleteStash(context.Background(), want.StashID); err != nil {
			return err
		}
		got, err = tx.Stash(context.Background(), want.StashID)
		if err != nil || got != nil {
			t.Fatalf("deleted Stash=(%+v,%v), want (nil,nil)", got, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceStashCRUDRestartOwnershipAbsenceAndIsolation(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	a := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	b := createBinding(t, repo, a.Scope.ProjectID, "00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	c := createBinding(t, repo, "00000000-0000-4000-8000-000000000002", string(a.Scope.WorkspaceID), "/checkout-c", 3, 13)
	stashID := "00000000-0000-4000-8000-000000000031"
	fixtures := []struct {
		binding types.WorkspaceBinding
		stash   WorkspaceStashInsert
	}{
		{binding: a, stash: validWorkspaceStash(t, a, stashID)},
		{binding: b, stash: validWorkspaceStash(t, b, stashID)},
		{binding: c, stash: validWorkspaceStash(t, c, stashID)},
	}
	for index := range fixtures {
		fixtures[index].stash.Label = fmt.Sprintf("scope-%d", index)
		fixture := fixtures[index]
		if err := repo.WithImmediateWorkspace(ctx, fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
			return tx.InsertStash(ctx, fixture.stash)
		}); err != nil {
			t.Fatal(err)
		}
	}
	wantSource := bytes.Clone(fixtures[0].stash.SourceTree[0].Data)
	wantComposed := bytes.Clone(fixtures[0].stash.ComposedTree[0].Data)
	fixtures[0].stash.SourceTree[0].Data[0] ^= 0xff
	fixtures[0].stash.ComposedTree[0].Data[0] ^= 0xff
	fixtures[0].stash.SourceTree[0].Data = wantSource
	fixtures[0].stash.ComposedTree[0].Data = wantComposed

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo = NewWorkspaceRepo(store.DB())
	for _, fixture := range fixtures {
		if err := repo.WithImmediateWorkspace(ctx, fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
			got, err := tx.Stash(ctx, stashID)
			if err != nil {
				return err
			}
			assertWorkspaceStash(t, got, fixture.stash)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	missingID := "00000000-0000-4000-8000-000000000099"
	if err := repo.WithImmediateWorkspace(ctx, a.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.Stash(ctx, missingID)
		if err != nil || got != nil {
			t.Fatalf("absent stash=(%+v,%v), want (nil,nil)", got, err)
		}
		first, err := tx.Stash(ctx, stashID)
		if err != nil {
			return err
		}
		first.SourceTree[0].Data[0] ^= 0xff
		first.ComposedTree[0].Data[0] ^= 0xff
		first.ActorJSON = "mutated"
		first.OperationsJSON = "mutated"
		second, err := tx.Stash(ctx, stashID)
		if err != nil {
			return err
		}
		assertWorkspaceStash(t, second, fixtures[0].stash)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	conn, err := store.DB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	missingScope := types.WorkspaceScope{ProjectID: "00000000-0000-4000-8000-000000000098", WorkspaceID: "00000000-0000-4000-8000-000000000097"}
	got, err := (&WorkspaceMutationTx{conn: conn, scope: missingScope}).Stash(ctx, stashID)
	if !errors.Is(err, ErrNotFound) || got != nil {
		t.Fatalf("missing workspace Stash=(%+v,%v), want (nil,ErrNotFound)", got, err)
	}
}

func TestWorkspaceStashCRUDRejectsInvalidInsertWithoutWrites(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011",
		"/checkout", 1, 11,
	)
	valid := validWorkspaceStash(t, binding, "00000000-0000-4000-8000-000000000031")
	otherRepository := types.RepositoryIdentity{
		Provider: "github", ImmutableID: "repository-2", CanonicalRemote: "https://github.com/acme/other",
	}
	tests := []struct {
		name string
		edit func(*WorkspaceStashInsert)
	}{
		{name: "non-v4 stash ID", edit: func(stash *WorkspaceStashInsert) { stash.StashID = "00000000-0000-1000-8000-000000000031" }},
		{name: "source digest", edit: func(stash *WorkspaceStashInsert) { stash.SourceBaseDigest = state.Digest("BAD") }},
		{name: "candidate digest", edit: func(stash *WorkspaceStashInsert) {
			stash.CandidateDigest = state.Digest("sha256:" + strings.Repeat("A", 64))
		}},
		{name: "source digest mismatch", edit: func(stash *WorkspaceStashInsert) {
			stash.SourceBaseDigest = state.Digest("sha256:" + strings.Repeat("a", 64))
		}},
		{name: "candidate digest mismatch", edit: func(stash *WorkspaceStashInsert) {
			stash.CandidateDigest = state.Digest("sha256:" + strings.Repeat("a", 64))
		}},
		{name: "negative generation", edit: func(stash *WorkspaceStashInsert) { stash.ThroughGeneration = -1 }},
		{name: "empty operations bytes", edit: func(stash *WorkspaceStashInsert) { stash.OperationsJSON = "" }},
		{name: "NUL operations bytes", edit: func(stash *WorkspaceStashInsert) { stash.OperationsJSON = "a\x00b" }},
		{name: "invalid UTF-8 operations bytes", edit: func(stash *WorkspaceStashInsert) { stash.OperationsJSON = string([]byte{0xff}) }},
		{name: "invalid historical actor", edit: func(stash *WorkspaceStashInsert) { stash.Actor.HumanPrincipalID = "BAD" }},
		{name: "empty label", edit: func(stash *WorkspaceStashInsert) { stash.Label = "" }},
		{name: "oversize label", edit: func(stash *WorkspaceStashInsert) { stash.Label = strings.Repeat("x", 257) }},
		{name: "control label", edit: func(stash *WorkspaceStashInsert) { stash.Label = "bad\nlabel" }},
		{name: "invalid UTF-8 label", edit: func(stash *WorkspaceStashInsert) { stash.Label = string([]byte{0xff}) }},
		{name: "nil source tree", edit: func(stash *WorkspaceStashInsert) { stash.SourceTree = nil }},
		{name: "unsorted source tree", edit: func(stash *WorkspaceStashInsert) {
			stash.SourceTree[0], stash.SourceTree[1] = stash.SourceTree[1], stash.SourceTree[0]
		}},
		{name: "changed source bytes", edit: func(stash *WorkspaceStashInsert) { stash.SourceTree[0].Data = append(stash.SourceTree[0].Data, ' ') }},
		{name: "source project mismatch", edit: func(stash *WorkspaceStashInsert) {
			stash.SourceTree = workspaceTree(t, "00000000-0000-4000-8000-000000000099", binding.Repository)
			snapshot, err := state.DecodeTree(stash.SourceTree)
			if err != nil {
				t.Fatal(err)
			}
			stash.SourceBaseDigest = snapshot.Digest
		}},
		{name: "composed repository mismatch", edit: func(stash *WorkspaceStashInsert) {
			stash.ComposedTree = workspaceTree(t, binding.Scope.ProjectID, otherRepository)
			snapshot, err := state.DecodeTree(stash.ComposedTree)
			if err != nil {
				t.Fatal(err)
			}
			stash.CandidateDigest = snapshot.Digest
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stash := cloneWorkspaceStash(valid)
			test.edit(&stash)
			err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				return tx.InsertStash(context.Background(), stash)
			})
			if err == nil || errors.Is(err, ErrNotFound) {
				t.Fatalf("InsertStash error=%v, want content validation error", err)
			}
		})
	}
	var count int
	if err := store.DB().QueryRow(`SELECT count(*) FROM workspace_stashes`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid inserts left count=%d err=%v", count, err)
	}
}

func TestWorkspaceStashCRUDRejectsNonLocalInsertActors(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011",
		"/checkout", 1, 11,
	)
	for index, assurance := range []types.Assurance{
		types.AssuranceLegacy,
		types.AssuranceUnknown,
		types.AssurancePublicKeyContinuity,
		types.AssurancePrivateAuthenticated,
	} {
		t.Run(string(assurance), func(t *testing.T) {
			stashID := fmt.Sprintf("00000000-0000-4000-8000-%012d", 40+index)
			stash := validWorkspaceStash(t, binding, stashID)
			stash.Actor.Assurance = assurance
			err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				return tx.InsertStash(context.Background(), stash)
			})
			if err == nil || errors.Is(err, ErrNotFound) {
				t.Fatalf("InsertStash assurance %q error=%v, want local-action error", assurance, err)
			}
		})
	}
	var count int
	if err := store.DB().QueryRow(`SELECT count(*) FROM workspace_stashes`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("non-local inserts left count=%d err=%v", count, err)
	}
}

func TestWorkspaceStashCRUDReadsCanonicalHistoricalActor(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011",
		"/checkout", 1, 11,
	)
	stash := validWorkspaceStash(t, binding, "00000000-0000-4000-8000-000000000031")
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.InsertStash(context.Background(), stash)
	}); err != nil {
		t.Fatal(err)
	}
	stash.Actor.Assurance = types.AssuranceLegacy
	actorJSON, err := state.CanonicalJSON(stash.Actor)
	if err != nil {
		t.Fatal(err)
	}
	updateWorkspaceStashValues(t, store, binding.Scope, stash.StashID, "actor_json=?", string(actorJSON))
	assertWorkspaceStashRead(t, repo, binding.Scope, stash)
}

func TestWorkspaceStashCRUDPlainInsertExactDeleteAndRollback(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011",
		"/checkout", 1, 11,
	)
	ctx := context.Background()
	stash := validWorkspaceStash(t, binding, "00000000-0000-4000-8000-000000000031")
	rollback := errors.New("rollback stash insert")
	if err := repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
		if err := tx.InsertStash(ctx, stash); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("rolled-back InsertStash error=%v, want sentinel", err)
	}
	assertWorkspaceStashAbsent(t, repo, binding.Scope, stash.StashID)

	if err := repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.InsertStash(ctx, stash)
	}); err != nil {
		t.Fatal(err)
	}
	changed := cloneWorkspaceStash(stash)
	changed.Label = "must not replace"
	if err := repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.InsertStash(ctx, changed)
	}); err == nil {
		t.Fatal("duplicate stash insert succeeded")
	}
	assertWorkspaceStashRead(t, repo, binding.Scope, stash)

	ignored := validWorkspaceStash(t, binding, "00000000-0000-4000-8000-000000000032")
	if _, err := store.DB().Exec(`
		CREATE TRIGGER ignore_workspace_stash_insert
		BEFORE INSERT ON workspace_stashes
		WHEN NEW.stash_id='00000000-0000-4000-8000-000000000032'
		BEGIN SELECT RAISE(IGNORE); END
	`); err != nil {
		t.Fatal(err)
	}
	if err := repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.InsertStash(ctx, ignored)
	}); err == nil {
		t.Fatal("ignored stash insert succeeded")
	}
	assertWorkspaceStashAbsent(t, repo, binding.Scope, ignored.StashID)

	if _, err := store.DB().Exec(`
		CREATE TRIGGER ignore_workspace_stash_delete
		BEFORE DELETE ON workspace_stashes
		WHEN OLD.stash_id='00000000-0000-4000-8000-000000000031'
		BEGIN SELECT RAISE(IGNORE); END
	`); err != nil {
		t.Fatal(err)
	}
	if err := repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.DeleteStash(ctx, stash.StashID)
	}); err == nil {
		t.Fatal("ignored stash delete succeeded")
	}
	assertWorkspaceStashRead(t, repo, binding.Scope, stash)
	if _, err := store.DB().Exec(`DROP TRIGGER ignore_workspace_stash_delete`); err != nil {
		t.Fatal(err)
	}
	if err := repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
		if err := tx.DeleteStash(ctx, stash.StashID); err != nil {
			return err
		}
		return tx.DeleteStash(ctx, stash.StashID)
	}); err == nil {
		t.Fatal("second stash delete in one transaction succeeded")
	}
	assertWorkspaceStashRead(t, repo, binding.Scope, stash)
	if err := repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.DeleteStash(ctx, stash.StashID)
	}); err != nil {
		t.Fatal(err)
	}
	assertWorkspaceStashAbsent(t, repo, binding.Scope, stash.StashID)
	if err := repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.DeleteStash(ctx, stash.StashID)
	}); err == nil {
		t.Fatal("delete of absent stash succeeded")
	}
}

func TestWorkspaceStashCRUDDeleteRetainsOwnedTerminalOperations(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011",
		"/checkout", 1, 11,
	)
	ctx := context.Background()
	stash := validWorkspaceStash(t, binding, "00000000-0000-4000-8000-000000000031")
	operation := validWorkspaceOperation("00000000-0000-4000-8000-000000000091")
	operationJSON := insertWorkspaceOperationOwned(t, store, binding.Scope, 1, operation, "stashed", &stash.StashID)
	if err := repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
		if err := tx.InsertStash(ctx, stash); err != nil {
			return err
		}
		return tx.DeleteStash(ctx, stash.StashID)
	}); err != nil {
		t.Fatal(err)
	}
	assertWorkspaceStashAbsent(t, repo, binding.Scope, stash.StashID)
	if err := repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.StashedOperationsByStashID(ctx, stash.StashID)
		if err != nil {
			return err
		}
		if len(got) != 1 || got[0].Generation != 1 || got[0].OperationID != operation.ID ||
			got[0].State != "stashed" || got[0].StashedByStashID == nil || *got[0].StashedByStashID != stash.StashID ||
			!bytes.Equal(got[0].OperationJSON, operationJSON) {
			t.Fatalf("owned terminal operations=%+v, want exact retained row", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceStashCRUDDeleteStrictPreflight(t *testing.T) {
	for _, test := range []struct {
		name     string
		corrupt  func(*testing.T, *Store, types.WorkspaceBinding, WorkspaceStashInsert)
		wantRows int
	}{
		{name: "BLOB-only logical key", wantRows: 1, corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashRaw(t, store, binding.Scope, stash.StashID, "stash_id=CAST(stash_id AS BLOB)")
		}},
		{name: "TEXT and BLOB logical duplicate", wantRows: 2, corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			_, err := store.DB().Exec(`
				INSERT INTO workspace_stashes
				(project_id,workspace_id,stash_id,source_base_digest,candidate_digest,source_tree,
				 composed_tree,operations_json,through_generation,actor_json,label,created_at)
				SELECT project_id,workspace_id,CAST(stash_id AS BLOB),source_base_digest,candidate_digest,
				       source_tree,composed_tree,operations_json,through_generation,actor_json,label,created_at
				FROM workspace_stashes
				WHERE project_id=? AND workspace_id=? AND stash_id=?
			`, binding.Scope.ProjectID, binding.Scope.WorkspaceID, stash.StashID)
			if err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, repo := openWorkspaceStore(t)
			binding := createBinding(t, repo,
				"00000000-0000-4000-8000-000000000001",
				"00000000-0000-4000-8000-000000000011",
				"/checkout", 1, 11,
			)
			stash := validWorkspaceStash(t, binding, "00000000-0000-4000-8000-000000000031")
			if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				return tx.InsertStash(context.Background(), stash)
			}); err != nil {
				t.Fatal(err)
			}
			test.corrupt(t, store, binding, stash)
			if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				return tx.DeleteStash(context.Background(), stash.StashID)
			}); err == nil {
				t.Fatal("DeleteStash succeeded without strict preflight")
			}
			var rows int
			if err := store.DB().QueryRow(`
				SELECT count(*) FROM workspace_stashes
				WHERE CAST(project_id AS TEXT)=? AND CAST(workspace_id AS TEXT)=? AND CAST(stash_id AS TEXT)=?
			`, binding.Scope.ProjectID, binding.Scope.WorkspaceID, stash.StashID).Scan(&rows); err != nil || rows != test.wantRows {
				t.Fatalf("rows after rejected delete=%d err=%v, want %d", rows, err, test.wantRows)
			}
		})
	}

	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011",
		"/checkout", 1, 11,
	)
	stashID := "00000000-0000-4000-8000-000000000031"
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.DeleteStash(context.Background(), stashID)
	}); err == nil {
		t.Fatal("DeleteStash accepted an absent stash")
	}
	conn, err := store.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	missingScope := types.WorkspaceScope{ProjectID: "00000000-0000-4000-8000-000000000098", WorkspaceID: "00000000-0000-4000-8000-000000000097"}
	if err := (&WorkspaceMutationTx{conn: conn, scope: missingScope}).DeleteStash(context.Background(), stashID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing-workspace DeleteStash error=%v, want ErrNotFound", err)
	}
}

func TestWorkspaceStashCRUDDeleteRollsBackWithCallerTransaction(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011",
		"/checkout", 1, 11,
	)
	stash := validWorkspaceStash(t, binding, "00000000-0000-4000-8000-000000000031")
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.InsertStash(context.Background(), stash)
	}); err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("rollback delete")
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		if err := tx.DeleteStash(context.Background(), stash.StashID); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("DeleteStash rollback error=%v, want sentinel", err)
	}
	assertWorkspaceStashRead(t, repo, binding.Scope, stash)
}

func TestWorkspaceStashCRUDPreservesRuntimeUnknownOperationsBytes(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011",
		"/checkout", 1, 11,
	)
	stash := validWorkspaceStash(t, binding, "00000000-0000-4000-8000-000000000031")
	stash.OperationsJSON = "future-runtime-codec:v99\x1fopaque\r\n"
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.InsertStash(context.Background(), stash)
	}); err != nil {
		t.Fatal(err)
	}
	var persisted, actorJSON, operationsClass, sourceClass, composedClass string
	var sourceBytes, composedBytes []byte
	if err := store.DB().QueryRow(`
		SELECT source_tree, composed_tree, operations_json, actor_json,
		       typeof(source_tree), typeof(composed_tree), typeof(operations_json)
		FROM workspace_stashes
		WHERE project_id=? AND workspace_id=? AND stash_id=?
	`, binding.Scope.ProjectID, binding.Scope.WorkspaceID, stash.StashID).Scan(
		&sourceBytes, &composedBytes, &persisted, &actorJSON, &sourceClass, &composedClass, &operationsClass,
	); err != nil {
		t.Fatal(err)
	}
	wantSource, err := encodeFileList(stash.SourceTree)
	if err != nil {
		t.Fatal(err)
	}
	wantComposed, err := encodeFileList(stash.ComposedTree)
	if err != nil {
		t.Fatal(err)
	}
	wantActor, err := state.CanonicalJSON(stash.Actor)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sourceBytes, wantSource) || !bytes.Equal(composedBytes, wantComposed) ||
		persisted != stash.OperationsJSON || actorJSON != string(wantActor) ||
		sourceClass != "blob" || composedClass != "blob" || operationsClass != "text" {
		t.Fatalf("persisted stash bytes/classes differ from exact canonical input")
	}
	assertWorkspaceStashRead(t, repo, binding.Scope, stash)
}

func TestWorkspaceStashCRUDRejectsBlobStashIDInsteadOfReportingAbsent(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011",
		"/checkout", 1, 11,
	)
	stash := validWorkspaceStash(t, binding, "00000000-0000-4000-8000-000000000031")
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.InsertStash(context.Background(), stash)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
		UPDATE workspace_stashes SET stash_id=CAST(stash_id AS BLOB)
		WHERE project_id=? AND workspace_id=?
	`, binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	var got *WorkspaceStashRecord
	err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		var err error
		got, err = tx.Stash(context.Background(), stash.StashID)
		return err
	})
	if err == nil || got != nil {
		t.Fatalf("blob stash ID read=(%+v,%v), want nil and corruption error", got, err)
	}
}

func TestWorkspaceStashCRUDRejectsPersistedCorruption(t *testing.T) {
	tests := []workspaceStashCorruptionCase{
		{name: "BLOB project ID", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashScopeStorage(t, store, binding.Scope, stash.StashID, "project_id")
		}},
		{name: "BLOB workspace ID", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashScopeStorage(t, store, binding.Scope, stash.StashID, "workspace_id")
		}},
		{name: "duplicate textual key across storage classes", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			_, err := store.DB().Exec(`
				INSERT INTO workspace_stashes
				(project_id,workspace_id,stash_id,source_base_digest,candidate_digest,source_tree,
				 composed_tree,operations_json,through_generation,actor_json,label,created_at)
				SELECT project_id,workspace_id,CAST(stash_id AS BLOB),source_base_digest,candidate_digest,
				       source_tree,composed_tree,operations_json,through_generation,actor_json,label,created_at
				FROM workspace_stashes
				WHERE project_id=? AND workspace_id=? AND stash_id=?
			`, binding.Scope.ProjectID, binding.Scope.WorkspaceID, stash.StashID)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{name: "BLOB source digest", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashRaw(t, store, binding.Scope, stash.StashID, "source_base_digest=CAST(source_base_digest AS BLOB)")
		}},
		{name: "BLOB candidate digest", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashRaw(t, store, binding.Scope, stash.StashID, "candidate_digest=CAST(candidate_digest AS BLOB)")
		}},
		{name: "TEXT source tree", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashRaw(t, store, binding.Scope, stash.StashID, "source_tree=CAST(source_tree AS TEXT)")
		}},
		{name: "TEXT composed tree", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashRaw(t, store, binding.Scope, stash.StashID, "composed_tree=CAST(composed_tree AS TEXT)")
		}},
		{name: "BLOB operations bytes", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashRaw(t, store, binding.Scope, stash.StashID, "operations_json=CAST(operations_json AS BLOB)")
		}},
		{name: "REAL generation", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashRaw(t, store, binding.Scope, stash.StashID, "through_generation=7.5")
		}},
		{name: "BLOB actor", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashRaw(t, store, binding.Scope, stash.StashID, "actor_json=CAST(actor_json AS BLOB)")
		}},
		{name: "BLOB label", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashRaw(t, store, binding.Scope, stash.StashID, "label=CAST(label AS BLOB)")
		}},
		{name: "INTEGER timestamp", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashRaw(t, store, binding.Scope, stash.StashID, "created_at=1")
		}},
		{name: "invalid source digest", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashRaw(t, store, binding.Scope, stash.StashID, "source_base_digest='BAD'")
		}},
		{name: "source digest mismatch", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashRaw(t, store, binding.Scope, stash.StashID, "source_base_digest='sha256:"+strings.Repeat("a", 64)+"'")
		}},
		{name: "candidate digest mismatch", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashRaw(t, store, binding.Scope, stash.StashID, "candidate_digest='sha256:"+strings.Repeat("a", 64)+"'")
		}},
	}
	runWorkspaceStashCorruptionCases(t, tests)
}

func TestWorkspaceStashCRUDRejectsTreeActorTextAndTimestampCorruption(t *testing.T) {
	tests := []workspaceStashCorruptionCase{
		{name: "trailing source tree bytes", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashRaw(t, store, binding.Scope, stash.StashID, "source_tree=CAST(source_tree || X'00' AS BLOB)")
		}},
		{name: "trailing composed tree bytes", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashRaw(t, store, binding.Scope, stash.StashID, "composed_tree=CAST(composed_tree || X'00' AS BLOB)")
		}},
		{name: "source tree project mismatch", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			tree := workspaceTree(t, "00000000-0000-4000-8000-000000000099", binding.Repository)
			snapshot, err := state.DecodeTree(tree)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := encodeFileList(tree)
			if err != nil {
				t.Fatal(err)
			}
			updateWorkspaceStashValues(t, store, binding.Scope, stash.StashID,
				"source_tree=?, source_base_digest=?", encoded, snapshot.Digest)
		}},
		{name: "composed tree repository mismatch", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			repository := types.RepositoryIdentity{Provider: "github", ImmutableID: "other", CanonicalRemote: "https://github.com/acme/other"}
			tree := workspaceTree(t, binding.Scope.ProjectID, repository)
			snapshot, err := state.DecodeTree(tree)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := encodeFileList(tree)
			if err != nil {
				t.Fatal(err)
			}
			updateWorkspaceStashValues(t, store, binding.Scope, stash.StashID,
				"composed_tree=?, candidate_digest=?", encoded, snapshot.Digest)
		}},
		{name: "empty operations bytes", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashRaw(t, store, binding.Scope, stash.StashID, "operations_json='' ")
		}},
		{name: "NUL operations bytes", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashValues(t, store, binding.Scope, stash.StashID, "operations_json=?", "a\x00b")
		}},
		{name: "invalid UTF-8 operations bytes", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashRaw(t, store, binding.Scope, stash.StashID, "operations_json=CAST(X'FF' AS TEXT)")
		}},
		{name: "negative generation", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashRaw(t, store, binding.Scope, stash.StashID, "through_generation=-1")
		}},
		{name: "noncanonical actor", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashRaw(t, store, binding.Scope, stash.StashID, "actor_json=' ' || actor_json")
		}},
		{name: "invalid historical actor", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			actor := stash.Actor
			actor.HumanPrincipalID = "BAD"
			raw, err := state.CanonicalJSON(actor)
			if err != nil {
				t.Fatal(err)
			}
			updateWorkspaceStashValues(t, store, binding.Scope, stash.StashID, "actor_json=?", string(raw))
		}},
		{name: "empty label", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashRaw(t, store, binding.Scope, stash.StashID, "label='' ")
		}},
		{name: "control label", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashValues(t, store, binding.Scope, stash.StashID, "label=?", "bad\rlabel")
		}},
		{name: "oversize label", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashValues(t, store, binding.Scope, stash.StashID, "label=?", strings.Repeat("x", 257))
		}},
		{name: "invalid UTF-8 label", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashRaw(t, store, binding.Scope, stash.StashID, "label=CAST(X'FF' AS TEXT)")
		}},
		{name: "non-UTC timestamp", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashValues(t, store, binding.Scope, stash.StashID, "created_at=?", "2026-07-29T13:00:00+01:00")
		}},
		{name: "zero timestamp", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			updateWorkspaceStashValues(t, store, binding.Scope, stash.StashID, "created_at=?", "0001-01-01 00:00:00")
		}},
	}
	runWorkspaceStashCorruptionCases(t, tests)
}

func validWorkspaceStash(t *testing.T, binding types.WorkspaceBinding, stashID string) WorkspaceStashInsert {
	t.Helper()
	source := workspaceTree(t, binding.Scope.ProjectID, binding.Repository)
	sourceSnapshot, err := state.DecodeTree(source)
	if err != nil {
		t.Fatal(err)
	}
	composedSnapshot := sourceSnapshot
	composedSnapshot.Project.Name = "Stashed Wormhole"
	composedSnapshot.Project.UpdatedAt = composedSnapshot.Project.UpdatedAt.Add(time.Minute)
	composed, err := state.EncodeTree(composedSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	composedSnapshot, err = state.DecodeTree(composed)
	if err != nil {
		t.Fatal(err)
	}
	return WorkspaceStashInsert{
		StashID:           stashID,
		SourceBaseDigest:  sourceSnapshot.Digest,
		CandidateDigest:   composedSnapshot.Digest,
		SourceTree:        source,
		ComposedTree:      composed,
		OperationsJSON:    " {\n  \"runtime_owned\" : true\n}\n",
		ThroughGeneration: 7,
		Actor: types.ActorEnvelope{
			ActorKind: types.ActorHuman, HumanPrincipalID: "00000000-0000-4000-8000-000000000061",
			Assurance: types.AssuranceLocal, OccurredAt: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC),
		},
		Label: "branch work",
	}
}

func assertWorkspaceStash(t *testing.T, got *WorkspaceStashRecord, want WorkspaceStashInsert) {
	t.Helper()
	if got == nil {
		t.Fatal("Stash returned nil")
	}
	gotInsert := WorkspaceStashInsert{
		StashID: got.StashID, SourceBaseDigest: got.SourceBaseDigest, CandidateDigest: got.CandidateDigest,
		SourceTree: got.SourceTree, ComposedTree: got.ComposedTree, OperationsJSON: got.OperationsJSON,
		ThroughGeneration: got.ThroughGeneration, Actor: got.Actor, Label: got.Label,
	}
	if !reflect.DeepEqual(gotInsert, want) {
		t.Fatalf("Stash=%+v, want %+v", gotInsert, want)
	}
	wantActorJSON, err := state.CanonicalJSON(want.Actor)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(got.ActorJSON), wantActorJSON) {
		t.Fatalf("ActorJSON=%q, want %q", got.ActorJSON, wantActorJSON)
	}
	if !validUTCTimestamp(got.CreatedAt) || got.CreatedAt.Location() != time.UTC {
		t.Fatalf("CreatedAt=%v in %v, want non-zero UTC", got.CreatedAt, got.CreatedAt.Location())
	}
}

func cloneWorkspaceStash(stash WorkspaceStashInsert) WorkspaceStashInsert {
	cloned := stash
	cloned.SourceTree = cloneWorkspaceStashTree(stash.SourceTree)
	cloned.ComposedTree = cloneWorkspaceStashTree(stash.ComposedTree)
	return cloned
}

func cloneWorkspaceStashTree(tree state.Tree) state.Tree {
	cloned := make(state.Tree, len(tree))
	for index, file := range tree {
		cloned[index] = state.File{Path: file.Path, Data: bytes.Clone(file.Data)}
	}
	return cloned
}

func assertWorkspaceStashRead(t *testing.T, repo *WorkspaceRepo, scope types.WorkspaceScope, want WorkspaceStashInsert) {
	t.Helper()
	if err := repo.WithImmediateWorkspace(context.Background(), scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.Stash(context.Background(), want.StashID)
		if err != nil {
			return err
		}
		assertWorkspaceStash(t, got, want)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func assertWorkspaceStashAbsent(t *testing.T, repo *WorkspaceRepo, scope types.WorkspaceScope, stashID string) {
	t.Helper()
	if err := repo.WithImmediateWorkspace(context.Background(), scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.Stash(context.Background(), stashID)
		if err != nil || got != nil {
			t.Fatalf("Stash=(%+v,%v), want (nil,nil)", got, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type workspaceStashCorruptionCase struct {
	name    string
	corrupt func(*testing.T, *Store, types.WorkspaceBinding, WorkspaceStashInsert)
}

func runWorkspaceStashCorruptionCases(t *testing.T, tests []workspaceStashCorruptionCase) {
	t.Helper()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			databasePath := filepath.Join(t.TempDir(), "gateway.db")
			store, err := Open(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			repo := NewWorkspaceRepo(store.DB())
			binding := createBinding(t, repo,
				"00000000-0000-4000-8000-000000000001",
				"00000000-0000-4000-8000-000000000011",
				"/checkout", 1, 11,
			)
			stash := validWorkspaceStash(t, binding, "00000000-0000-4000-8000-000000000031")
			if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				return tx.InsertStash(context.Background(), stash)
			}); err != nil {
				t.Fatal(err)
			}
			test.corrupt(t, store, binding, stash)
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = Open(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			repo = NewWorkspaceRepo(store.DB())
			var got *WorkspaceStashRecord
			err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				var err error
				got, err = tx.Stash(context.Background(), stash.StashID)
				return err
			})
			if err == nil || got != nil {
				t.Fatalf("corrupt Stash=(%+v,%v), want nil and error", got, err)
			}
		})
	}
}

func updateWorkspaceStashRaw(t *testing.T, store *Store, scope types.WorkspaceScope, stashID, assignment string) {
	t.Helper()
	updateWorkspaceStashValues(t, store, scope, stashID, assignment)
}

func updateWorkspaceStashValues(t *testing.T, store *Store, scope types.WorkspaceScope, stashID, assignment string, values ...any) {
	t.Helper()
	query := "UPDATE workspace_stashes SET " + assignment + " WHERE CAST(project_id AS TEXT)=? AND CAST(workspace_id AS TEXT)=? AND CAST(stash_id AS TEXT)=?"
	args := append(values, scope.ProjectID, scope.WorkspaceID, stashID)
	result, err := store.DB().Exec(query, args...)
	if err != nil {
		t.Fatalf("corrupt workspace stash: %v", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		t.Fatalf("corrupt workspace stash affected=%d err=%v, want 1", affected, err)
	}
}

func updateWorkspaceStashScopeStorage(t *testing.T, store *Store, scope types.WorkspaceScope, stashID, column string) {
	t.Helper()
	conn, err := store.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	query := "UPDATE workspace_stashes SET " + column + "=CAST(" + column + " AS BLOB) WHERE project_id=? AND workspace_id=? AND stash_id=?"
	result, err := conn.ExecContext(context.Background(), query, scope.ProjectID, scope.WorkspaceID, stashID)
	if err != nil {
		t.Fatalf("corrupt workspace stash scope: %v", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		t.Fatalf("corrupt workspace stash scope affected=%d err=%v, want 1", affected, err)
	}
	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
}
