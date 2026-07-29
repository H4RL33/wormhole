package localstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestWorkspaceMutationTxRestoreRetryStateRejectsNilTransaction(t *testing.T) {
	ctx := context.Background()
	var tx *WorkspaceMutationTx
	got, err := tx.RestoreRetryState(ctx, "00000000-0000-4000-8000-000000000031")
	if !errors.Is(err, ErrNotFound) || !reflect.DeepEqual(got, WorkspaceRestoreRetryState{}) {
		t.Fatalf("RestoreRetryState=(%+v,%v), want (zero,ErrNotFound)", got, err)
	}
	var _ state.Digest = digestWorkspaceBlobBytesV1([]byte("canonical blob"))
}

func TestWorkspaceMutationTxRestoreRetryStateFailsClosedWhenUnavailable(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011",
		"/checkout", 1, 11,
	)
	stash := validWorkspaceStash(t, binding, "00000000-0000-4000-8000-000000000031")
	var closed *WorkspaceMutationTx
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		closed = tx
		return tx.InsertStash(context.Background(), stash)
	}); err != nil {
		t.Fatal(err)
	}
	got, err := closed.RestoreRetryState(context.Background(), stash.StashID)
	if err == nil || !reflect.DeepEqual(got, WorkspaceRestoreRetryState{}) {
		t.Fatalf("closed RestoreRetryState=(%+v,%v), want (zero,error)", got, err)
	}
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		got, err := tx.RestoreRetryState(ctx, stash.StashID)
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, WorkspaceRestoreRetryState{}) {
			t.Fatalf("canceled RestoreRetryState=(%+v,%v), want (zero,context.Canceled)", got, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDigestWorkspaceBlobBytesV1FixedGoldens(t *testing.T) {
	tests := []struct {
		name, literalHex, want string
	}{
		{"accepted snapshot", "0000000000000000", "sha256:af5570f5a1810b7af78caf4bc70a660f0df51e42baf91d4de5b2328de0e83dfc"},
		{"candidate direct", "0000000000000001000000000000000161000000000000000162", "sha256:2d9908d9e07a035886a6323dfd3b1718a7281b18a176170d802d10bb9762cd6c"},
		{"candidate rebased", "00000000000000010000000000000001780000000000000002797a", "sha256:40be52dee2610b6bc009ac253091d574b063ae11daadbbe2b24ba2106b130e61"},
		{"stash source", "0000000000000002000000000000000161000000000000000031000000000000000162000000000000000032", "sha256:166b0c11bf5541c1879795fd03953ee964b3dd5832f1437acaea318ba4cb6b4c"},
		{"stash composed", "00000000000000010000000000000003616263000000000000000464617461", "sha256:7d93c82cad21a2fe9f43d491e9683bc0ea45577d9f3e04edbd2eb34ee8d8c545"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, err := hex.DecodeString(test.literalHex)
			if err != nil {
				t.Fatal(err)
			}
			if got := digestWorkspaceBlobBytesV1(raw); string(got) != test.want {
				t.Fatalf("digest=%q, want hard-coded %q", got, test.want)
			}
		})
	}
}

func TestWorkspaceMutationTxRestoreRetryStateCompleteRestartAndOwnership(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011",
		"/checkout-a", 1, 11,
	)
	createdAt := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Minute)
	if _, err := store.DB().Exec(`UPDATE workspace_bindings SET created_at=?,updated_at=? WHERE project_id=? AND workspace_id=?`, createdAt, updatedAt, binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	candidate := workspaceCandidateRecord(t, binding, true, 1)
	stashID := "00000000-0000-4000-8000-000000000031"
	stash := validWorkspaceStash(t, binding, stashID)
	operationJSON := insertWorkspaceOperationOwned(t, store, binding.Scope, 1,
		validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "stashed", &stashID)
	insertWorkspaceOperation(t, store, binding.Scope, 2,
		validWorkspaceOperation("00000000-0000-4000-8000-000000000092"), "active")
	insertWorkspaceOperation(t, store, binding.Scope, 3,
		validWorkspaceOperation("00000000-0000-4000-8000-000000000093"), "rebased")
	insertWorkspaceOperation(t, store, binding.Scope, 4,
		validWorkspaceOperation("00000000-0000-4000-8000-000000000094"), "materialized")
	insertWorkspaceOperation(t, store, binding.Scope, 5,
		validWorkspaceOperation("00000000-0000-4000-8000-000000000095"), "discarded")
	evidence := WorkspaceConflictEvidence{
		ConflictID: "sha256:" + strings.Repeat("a", 64),
		Key:        state.RecordKey{Kind: "task", ID: "00000000-0000-4000-8000-000000000021"},
		FieldPath:  "/title", ConflictKind: "same_field",
		BaseJSON:   `{"present":true,"value":"base"}`,
		OursJSON:   `{"present":true,"value":"ours"}`,
		TheirsJSON: `{"present":true,"value":"theirs"}`,
	}
	if err := repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
		if err := tx.UpsertCandidate(ctx, candidate); err != nil {
			return err
		}
		if err := tx.InsertStash(ctx, stash); err != nil {
			return err
		}
		_, err := tx.ReplaceOpenConflictOccurrences(ctx, []WorkspaceConflictEvidence{evidence}, updatedAt.Add(time.Minute))
		return err
	}); err != nil {
		t.Fatal(err)
	}

	got := mustRestoreRetryState(t, repo, binding.Scope, stashID)
	assertCompleteRestoreRetryState(t, got, binding, candidate, stash, operationJSON, createdAt, updatedAt, evidence)
	assertRestoreRetryDigestsMatchRawColumns(t, store, binding.Scope, stashID, got)

	// Mutating every returned reference must not alter a fresh read.
	got.Workspace.Snapshot.Project.Name = "mutated"
	got.Candidate.DirectSnapshot.Project.Name = "mutated"
	got.Candidate.RebasedSnapshot.Project.Name = "mutated"
	got.Operations[0].OperationJSON[0] ^= 0xff
	*got.Operations[0].StashedByStashID = "00000000-0000-4000-8000-000000000099"
	got.Stash.SourceTree[0].Data[0] ^= 0xff
	got.Stash.ComposedTree[0].Data[0] ^= 0xff
	*got.CandidateDirectTreeBlobDigest = state.Digest("sha256:" + strings.Repeat("f", 64))
	got.OpenConflicts[0].BaseJSON = "mutated"
	again := mustRestoreRetryState(t, repo, binding.Scope, stashID)
	assertCompleteRestoreRetryState(t, again, binding, candidate, stash, operationJSON, createdAt, updatedAt, evidence)

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo = NewWorkspaceRepo(store.DB())
	restarted := mustRestoreRetryState(t, repo, binding.Scope, stashID)
	assertCompleteRestoreRetryState(t, restarted, binding, candidate, stash, operationJSON, createdAt, updatedAt, evidence)
}

func TestWorkspaceMutationTxRestoreRetryStateCandidateNullabilityAndEmptySlices(t *testing.T) {
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
	got := mustRestoreRetryState(t, repo, binding.Scope, stash.StashID)
	if got.Candidate != nil || got.CandidateDirectTreeBlobDigest != nil || got.CandidateRebasedTreeBlobDigest != nil {
		t.Fatalf("candidate nullability=(%+v,%v,%v), want all nil", got.Candidate, got.CandidateDirectTreeBlobDigest, got.CandidateRebasedTreeBlobDigest)
	}
	if got.Operations == nil || len(got.Operations) != 0 || got.OpenConflicts == nil || len(got.OpenConflicts) != 0 {
		t.Fatalf("empty slices operations=%#v conflicts=%#v", got.Operations, got.OpenConflicts)
	}
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.UpsertCandidate(context.Background(), workspaceCandidateRecord(t, binding, false, 0))
	}); err != nil {
		t.Fatal(err)
	}
	got = mustRestoreRetryState(t, repo, binding.Scope, stash.StashID)
	if got.Candidate == nil || got.CandidateDirectTreeBlobDigest == nil || got.CandidateRebasedTreeBlobDigest != nil || got.Candidate.RebasedSnapshot != nil {
		t.Fatalf("direct candidate nullability=(%+v,%v,%v)", got.Candidate, got.CandidateDirectTreeBlobDigest, got.CandidateRebasedTreeBlobDigest)
	}
}

func TestWorkspaceMutationTxRestoreRetryStateSameAndCrossProjectIsolation(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	a := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	b := createBinding(t, repo, a.Scope.ProjectID, "00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	c := createBinding(t, repo, "00000000-0000-4000-8000-000000000002", string(a.Scope.WorkspaceID), "/checkout-c", 3, 13)
	stashID := "00000000-0000-4000-8000-000000000031"
	bindings := []types.WorkspaceBinding{a, b, c}
	wantCandidates := make([]WorkspaceCandidateRecord, len(bindings))
	wantOperations := make([][]byte, len(bindings))
	wantConflicts := make([]WorkspaceConflictEvidence, len(bindings))
	for index, binding := range []types.WorkspaceBinding{a, b, c} {
		stash := validWorkspaceStash(t, binding, stashID)
		stash.Label = []string{"scope-a", "scope-b", "scope-c"}[index]
		candidate := workspaceCandidateRecord(t, binding, false, 0)
		candidate.DirectSnapshot.Project.Name = []string{"candidate-a", "candidate-b", "candidate-c"}[index]
		candidate.DirectSnapshot.Project.UpdatedAt = candidate.DirectSnapshot.Project.UpdatedAt.Add(time.Duration(index+1) * time.Minute)
		candidate.DirectSnapshot, _ = encodedSnapshot(t, candidate.DirectSnapshot)
		candidate.WorkingTreeDigest = candidate.DirectSnapshot.Digest
		wantCandidates[index] = candidate
		operationID := []string{
			"00000000-0000-4000-8000-000000000091",
			"00000000-0000-4000-8000-000000000092",
			"00000000-0000-4000-8000-000000000093",
		}[index]
		wantOperations[index] = insertWorkspaceOperation(t, store, binding.Scope, 1, validWorkspaceOperation(operationID), "active")
		conflict := WorkspaceConflictEvidence{
			ConflictID: "sha256:" + strings.Repeat([]string{"a", "b", "c"}[index], 64),
			Key: state.RecordKey{Kind: "task", ID: []string{
				"00000000-0000-4000-8000-000000000021",
				"00000000-0000-4000-8000-000000000022",
				"00000000-0000-4000-8000-000000000023",
			}[index]},
			FieldPath: "/title", ConflictKind: "same_field",
			BaseJSON: []string{`{"scope":"a"}`, `{"scope":"b"}`, `{"scope":"c"}`}[index],
			OursJSON: `{"side":"ours"}`, TheirsJSON: `{"side":"theirs"}`,
		}
		wantConflicts[index] = conflict
		if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
			if err := tx.UpsertCandidate(context.Background(), candidate); err != nil {
				return err
			}
			if err := tx.InsertStash(context.Background(), stash); err != nil {
				return err
			}
			_, err := tx.ReplaceOpenConflictOccurrences(context.Background(), []WorkspaceConflictEvidence{conflict}, time.Date(2026, 7, 29, 12, index, 0, 0, time.UTC))
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	for index, binding := range bindings {
		got := mustRestoreRetryState(t, repo, binding.Scope, stashID)
		wantLabel := []string{"scope-a", "scope-b", "scope-c"}[index]
		if got.Workspace.Binding.Scope != binding.Scope || got.Stash.Label != wantLabel ||
			got.Candidate == nil || !reflect.DeepEqual(*got.Candidate, wantCandidates[index]) ||
			len(got.Operations) != 1 || !bytes.Equal(got.Operations[0].OperationJSON, wantOperations[index]) ||
			len(got.OpenConflicts) != 1 || got.OpenConflicts[0].WorkspaceConflictEvidence != wantConflicts[index] {
			t.Fatalf("scope %d projection=%+v", index, got)
		}
	}
}

func TestWorkspaceMutationTxRestoreRetryStateRejectsAbsentAndCorruptState(t *testing.T) {
	t.Run("invalid stash ID is validation error", func(t *testing.T) {
		_, repo := openWorkspaceStore(t)
		binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
		if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
			got, err := tx.RestoreRetryState(context.Background(), "not-a-stash-id")
			if err == nil || errors.Is(err, ErrNotFound) || !reflect.DeepEqual(got, WorkspaceRestoreRetryState{}) {
				t.Fatalf("RestoreRetryState invalid ID=(%+v,%v), want (zero,validation error)", got, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("absent stash", func(t *testing.T) {
		_, repo := openWorkspaceStore(t)
		binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
		assertRestoreRetryError(t, repo, binding.Scope, "00000000-0000-4000-8000-000000000031")
	})

	tests := []struct {
		name    string
		corrupt func(*testing.T, *Store, types.WorkspaceBinding, WorkspaceStashInsert)
	}{
		{"binding repository BLOB", func(t *testing.T, store *Store, binding types.WorkspaceBinding, _ WorkspaceStashInsert) {
			execRestoreCorruption(t, store, `UPDATE workspace_bindings SET repository_identity_json=CAST(repository_identity_json AS BLOB) WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		}},
		{"binding accepted snapshot TEXT", func(t *testing.T, store *Store, binding types.WorkspaceBinding, _ WorkspaceStashInsert) {
			execRestoreCorruption(t, store, `UPDATE workspace_bindings SET accepted_snapshot=CAST(accepted_snapshot AS TEXT) WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		}},
		{"binding timestamp storage", func(t *testing.T, store *Store, binding types.WorkspaceBinding, _ WorkspaceStashInsert) {
			execRestoreCorruption(t, store, `UPDATE workspace_bindings SET created_at=1 WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		}},
		{"binding timestamp order", func(t *testing.T, store *Store, binding types.WorkspaceBinding, _ WorkspaceStashInsert) {
			execRestoreCorruption(t, store, `UPDATE workspace_bindings SET created_at='2026-07-29 12:00:00',updated_at='2026-07-29 11:00:00' WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		}},
		{"candidate direct storage", func(t *testing.T, store *Store, binding types.WorkspaceBinding, _ WorkspaceStashInsert) {
			execRestoreCorruption(t, store, `UPDATE workspace_candidates SET direct_tree=CAST(direct_tree AS TEXT) WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		}},
		{"candidate direct trailing bytes", func(t *testing.T, store *Store, binding types.WorkspaceBinding, _ WorkspaceStashInsert) {
			execRestoreCorruption(t, store, `UPDATE workspace_candidates SET direct_tree=CAST(direct_tree || X'00' AS BLOB) WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		}},
		{"candidate rebased bytes", func(t *testing.T, store *Store, binding types.WorkspaceBinding, _ WorkspaceStashInsert) {
			execRestoreCorruption(t, store, `UPDATE workspace_candidates SET rebased_tree=CAST(rebased_tree || X'00' AS BLOB) WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		}},
		{"candidate timestamp", func(t *testing.T, store *Store, binding types.WorkspaceBinding, _ WorkspaceStashInsert) {
			execRestoreCorruption(t, store, `UPDATE workspace_candidates SET imported_at=1 WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		}},
		{"stash source storage", func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			execRestoreCorruption(t, store, `UPDATE workspace_stashes SET source_tree=CAST(source_tree AS TEXT) WHERE project_id=? AND workspace_id=? AND stash_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID, stash.StashID)
		}},
		{"stash source trailing bytes", func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			execRestoreCorruption(t, store, `UPDATE workspace_stashes SET source_tree=CAST(source_tree || X'00' AS BLOB) WHERE project_id=? AND workspace_id=? AND stash_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID, stash.StashID)
		}},
		{"stash composed bytes", func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			execRestoreCorruption(t, store, `UPDATE workspace_stashes SET composed_tree=CAST(composed_tree || X'00' AS BLOB) WHERE project_id=? AND workspace_id=? AND stash_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID, stash.StashID)
		}},
		{"stash actor bytes", func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			execRestoreCorruption(t, store, `UPDATE workspace_stashes SET actor_json=' ' || actor_json WHERE project_id=? AND workspace_id=? AND stash_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID, stash.StashID)
		}},
		{"stash operations bytes", func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			execRestoreCorruption(t, store, `UPDATE workspace_stashes SET operations_json='' WHERE project_id=? AND workspace_id=? AND stash_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID, stash.StashID)
		}},
		{"stash timestamp", func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			execRestoreCorruption(t, store, `UPDATE workspace_stashes SET created_at=1 WHERE project_id=? AND workspace_id=? AND stash_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID, stash.StashID)
		}},
		{"operation payload storage", func(t *testing.T, store *Store, binding types.WorkspaceBinding, _ WorkspaceStashInsert) {
			execRestoreCorruption(t, store, `UPDATE workspace_overlay_operations SET operation_json=CAST(operation_json AS BLOB) WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		}},
		{"operation timestamp", func(t *testing.T, store *Store, binding types.WorkspaceBinding, _ WorkspaceStashInsert) {
			execRestoreCorruption(t, store, `UPDATE workspace_overlay_operations SET created_at=1 WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		}},
		{"conflict evidence storage", func(t *testing.T, store *Store, binding types.WorkspaceBinding, _ WorkspaceStashInsert) {
			execRestoreCorruption(t, store, `UPDATE workspace_conflicts SET base_json=CAST(base_json AS BLOB) WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		}},
		{"conflict timestamp", func(t *testing.T, store *Store, binding types.WorkspaceBinding, _ WorkspaceStashInsert) {
			execRestoreCorruption(t, store, `UPDATE workspace_conflicts SET created_at=1 WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, repo, binding, stash := restoreRetryCorruptionFixture(t)
			test.corrupt(t, store, binding, stash)
			assertRestoreRetryError(t, repo, binding.Scope, stash.StashID)
		})
	}
}

func TestWorkspaceMutationTxRestoreRetryStateRejectsCASTEquivalentScopeAliases(t *testing.T) {
	tests := []struct {
		name   string
		insert func(*testing.T, *Store, types.WorkspaceBinding, WorkspaceStashInsert)
	}{
		{"binding", func(t *testing.T, store *Store, binding types.WorkspaceBinding, _ WorkspaceStashInsert) {
			execRestoreCorruption(t, store, `
				INSERT INTO workspace_bindings
				SELECT CAST(project_id AS BLOB), workspace_id, checkout_path || '-alias',
				       checkout_device + 100, checkout_inode + 100, repository_identity_json,
				       accepted_ref, accepted_commit, accepted_digest, accepted_snapshot,
				       status, created_at, updated_at
				FROM workspace_bindings WHERE project_id=? AND workspace_id=?
			`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		}},
		{"candidate", func(t *testing.T, store *Store, binding types.WorkspaceBinding, _ WorkspaceStashInsert) {
			disableRestoreForeignKeys(t, store)
			execRestoreCorruption(t, store, `
				INSERT INTO workspace_candidates
				SELECT CAST(project_id AS BLOB), workspace_id, accepted_base_digest,
				       working_tree_digest, direct_tree, rebased_tree,
				       rebased_through_generation, imported_by, imported_at
				FROM workspace_candidates WHERE project_id=? AND workspace_id=?
			`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		}},
		{"stash", func(t *testing.T, store *Store, binding types.WorkspaceBinding, stash WorkspaceStashInsert) {
			disableRestoreForeignKeys(t, store)
			execRestoreCorruption(t, store, `
				INSERT INTO workspace_stashes
				SELECT CAST(project_id AS BLOB), workspace_id, stash_id, source_base_digest,
				       candidate_digest, source_tree, composed_tree, operations_json,
				       through_generation, actor_json, label, created_at
				FROM workspace_stashes WHERE project_id=? AND workspace_id=? AND stash_id=?
			`, binding.Scope.ProjectID, binding.Scope.WorkspaceID, stash.StashID)
		}},
		{"conflict", func(t *testing.T, store *Store, binding types.WorkspaceBinding, _ WorkspaceStashInsert) {
			disableRestoreForeignKeys(t, store)
			execRestoreCorruption(t, store, `
				INSERT INTO workspace_conflicts
				SELECT CAST(project_id AS BLOB), workspace_id, occurrence_id, conflict_id,
				       record_kind, record_id, field_path, conflict_kind, base_json,
				       ours_json, theirs_json, state, created_at, resolved_at
				FROM workspace_conflicts WHERE project_id=? AND workspace_id=? AND state='open'
			`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, repo, binding, stash := restoreRetryCorruptionFixture(t)
			test.insert(t, store, binding, stash)
			assertRestoreRetryError(t, repo, binding.Scope, stash.StashID)
		})
	}
}

func TestWorkspaceMutationTxRestoreRetryStateRejectsTrailingAcceptedSnapshotBytes(t *testing.T) {
	_, repo, binding, stash := restoreRetryCorruptionFixture(t)
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		if _, err := tx.conn.ExecContext(context.Background(), `
			UPDATE workspace_bindings
			SET accepted_snapshot=CAST(accepted_snapshot || X'00' AS BLOB)
			WHERE project_id=? AND workspace_id=?
		`, binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
			return err
		}
		got, err := tx.RestoreRetryState(context.Background(), stash.StashID)
		if err == nil || !reflect.DeepEqual(got, WorkspaceRestoreRetryState{}) {
			t.Fatalf("RestoreRetryState trailing accepted snapshot=(%+v,%v), want (zero,error)", got, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceMutationTxRestoreRetryStateSortsAndRejectsDuplicateOpenConflicts(t *testing.T) {
	t.Run("stable semantic order", func(t *testing.T) {
		_, repo := openWorkspaceStore(t)
		binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
		stash := validWorkspaceStash(t, binding, "00000000-0000-4000-8000-000000000031")
		conflicts := []WorkspaceConflictEvidence{
			{
				ConflictID: "sha256:" + strings.Repeat("c", 64),
				Key:        state.RecordKey{Kind: "task", ID: "00000000-0000-4000-8000-000000000023"},
				FieldPath:  "/title", ConflictKind: "same_field", BaseJSON: `{"v":"task"}`,
				OursJSON: `{"v":"ours"}`, TheirsJSON: `{"v":"theirs"}`,
			},
			{
				ConflictID: "sha256:" + strings.Repeat("a", 64),
				Key:        state.RecordKey{Kind: "project", ID: binding.Scope.ProjectID},
				FieldPath:  "/name", ConflictKind: "same_field", BaseJSON: `{"v":"project"}`,
				OursJSON: `{"v":"ours"}`, TheirsJSON: `{"v":"theirs"}`,
			},
			{
				ConflictID: "sha256:" + strings.Repeat("b", 64),
				Key:        state.RecordKey{Kind: "actor", ID: "00000000-0000-4000-8000-000000000022"},
				FieldPath:  "/display_name", ConflictKind: "same_field", BaseJSON: `{"v":"actor"}`,
				OursJSON: `{"v":"ours"}`, TheirsJSON: `{"v":"theirs"}`,
			},
		}
		if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
			if err := tx.InsertStash(context.Background(), stash); err != nil {
				return err
			}
			_, err := tx.ReplaceOpenConflictOccurrences(context.Background(), conflicts, time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		got := mustRestoreRetryState(t, repo, binding.Scope, stash.StashID)
		wantKinds := []string{"project", "actor", "task"}
		if len(got.OpenConflicts) != len(wantKinds) {
			t.Fatalf("open conflicts=%+v", got.OpenConflicts)
		}
		for index, wantKind := range wantKinds {
			if got.OpenConflicts[index].Key.Kind != wantKind {
				t.Fatalf("open conflict order=%+v, index %d want kind %q", got.OpenConflicts, index, wantKind)
			}
		}
	})

	t.Run("duplicate semantic ID", func(t *testing.T) {
		store, repo, binding, stash := restoreRetryCorruptionFixture(t)
		execRestoreCorruption(t, store, `DROP INDEX workspace_one_open_semantic_conflict`)
		execRestoreCorruption(t, store, `
			INSERT INTO workspace_conflicts
			(project_id,workspace_id,occurrence_id,conflict_id,record_kind,record_id,
			 field_path,conflict_kind,base_json,ours_json,theirs_json,state,created_at,resolved_at)
			SELECT project_id,workspace_id,'00000000-0000-4000-8000-000000000099',
			       conflict_id,record_kind,record_id,field_path,conflict_kind,base_json,
			       ours_json,theirs_json,state,created_at,resolved_at
			FROM workspace_conflicts
			WHERE project_id=? AND workspace_id=? AND state='open'
			LIMIT 1
		`, binding.Scope.ProjectID, binding.Scope.WorkspaceID)
		assertRestoreRetryError(t, repo, binding.Scope, stash.StashID)
	})
}

func restoreRetryCorruptionFixture(t *testing.T) (*Store, *WorkspaceRepo, types.WorkspaceBinding, WorkspaceStashInsert) {
	t.Helper()
	store, repo := openWorkspaceStore(t)
	store.DB().SetMaxOpenConns(1)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	stash := validWorkspaceStash(t, binding, "00000000-0000-4000-8000-000000000031")
	stashID := stash.StashID
	insertWorkspaceOperationOwned(t, store, binding.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "stashed", &stashID)
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		if err := tx.UpsertCandidate(context.Background(), workspaceCandidateRecord(t, binding, true, 1)); err != nil {
			return err
		}
		if err := tx.InsertStash(context.Background(), stash); err != nil {
			return err
		}
		_, err := tx.ReplaceOpenConflictOccurrences(context.Background(), []WorkspaceConflictEvidence{{
			ConflictID: "sha256:" + strings.Repeat("a", 64),
			Key:        state.RecordKey{Kind: "task", ID: "00000000-0000-4000-8000-000000000021"},
			FieldPath:  "/title", ConflictKind: "same_field", BaseJSON: `{"v":"base"}`,
			OursJSON: `{"v":"ours"}`, TheirsJSON: `{"v":"theirs"}`,
		}}, time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return store, repo, binding, stash
}

func assertRestoreRetryError(t *testing.T, repo *WorkspaceRepo, scope types.WorkspaceScope, stashID string) {
	t.Helper()
	called := false
	if err := repo.WithImmediateWorkspace(context.Background(), scope, func(tx *WorkspaceMutationTx) error {
		called = true
		got, err := tx.RestoreRetryState(context.Background(), stashID)
		if err == nil || !reflect.DeepEqual(got, WorkspaceRestoreRetryState{}) {
			t.Fatalf("RestoreRetryState=(%+v,%v), want (zero,error)", got, err)
		}
		return nil
	}); err != nil {
		t.Fatalf("open exact transaction: %v", err)
	}
	if !called {
		t.Fatal("RestoreRetryState callback was not called")
	}
}

func execRestoreCorruption(t *testing.T, store *Store, query string, args ...any) {
	t.Helper()
	if _, err := store.DB().Exec(query, args...); err != nil {
		t.Fatalf("corrupt restore retry fixture: %v", err)
	}
}

func disableRestoreForeignKeys(t *testing.T, store *Store) {
	t.Helper()
	if _, err := store.DB().Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
}

func mustRestoreRetryState(t *testing.T, repo *WorkspaceRepo, scope types.WorkspaceScope, stashID string) WorkspaceRestoreRetryState {
	t.Helper()
	var got WorkspaceRestoreRetryState
	if err := repo.WithImmediateWorkspace(context.Background(), scope, func(tx *WorkspaceMutationTx) error {
		var err error
		got, err = tx.RestoreRetryState(context.Background(), stashID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return got
}

func assertCompleteRestoreRetryState(t *testing.T, got WorkspaceRestoreRetryState, binding types.WorkspaceBinding, candidate WorkspaceCandidateRecord, stash WorkspaceStashInsert, firstOperationJSON []byte, createdAt, updatedAt time.Time, evidence WorkspaceConflictEvidence) {
	t.Helper()
	if got.Workspace.Binding != binding || !got.BindingCreatedAt.Equal(createdAt) || !got.BindingUpdatedAt.Equal(updatedAt) {
		t.Fatalf("binding state=%+v created=%v updated=%v", got.Workspace.Binding, got.BindingCreatedAt, got.BindingUpdatedAt)
	}
	if got.Candidate == nil || !reflect.DeepEqual(*got.Candidate, candidate) || got.CandidateDirectTreeBlobDigest == nil || got.CandidateRebasedTreeBlobDigest == nil {
		t.Fatalf("candidate state=%+v direct=%v rebased=%v", got.Candidate, got.CandidateDirectTreeBlobDigest, got.CandidateRebasedTreeBlobDigest)
	}
	if len(got.Operations) != 5 || !bytes.Equal(got.Operations[0].OperationJSON, firstOperationJSON) || got.Operations[0].StashedByStashID == nil || *got.Operations[0].StashedByStashID != stash.StashID {
		t.Fatalf("operations=%+v", got.Operations)
	}
	wantStates := []string{"stashed", "active", "rebased", "materialized", "discarded"}
	for index, wantState := range wantStates {
		if got.Operations[index].Generation != int64(index+1) || got.Operations[index].State != wantState {
			t.Fatalf("operation %d=%+v, want generation=%d state=%q", index, got.Operations[index], index+1, wantState)
		}
	}
	assertWorkspaceStash(t, &got.Stash, stash)
	if len(got.OpenConflicts) != 1 || got.OpenConflicts[0].WorkspaceConflictEvidence != evidence {
		t.Fatalf("open conflicts=%+v", got.OpenConflicts)
	}
	for _, digest := range []state.Digest{got.AcceptedSnapshotBlobDigest, *got.CandidateDirectTreeBlobDigest, *got.CandidateRebasedTreeBlobDigest, got.StashSourceTreeBlobDigest, got.StashComposedTreeBlobDigest} {
		if !strings.HasPrefix(string(digest), "sha256:") || len(digest) != 71 {
			t.Fatalf("invalid blob digest %q", digest)
		}
	}
}

func assertRestoreRetryDigestsMatchRawColumns(t *testing.T, store *Store, scope types.WorkspaceScope, stashID string, got WorkspaceRestoreRetryState) {
	t.Helper()
	var accepted, direct, rebased, source, composed []byte
	if err := store.DB().QueryRow(`
		SELECT binding.accepted_snapshot, candidate.direct_tree, candidate.rebased_tree,
		       stash.source_tree, stash.composed_tree
		FROM workspace_bindings binding
		JOIN workspace_candidates candidate USING(project_id,workspace_id)
		JOIN workspace_stashes stash USING(project_id,workspace_id)
		WHERE binding.project_id=? AND binding.workspace_id=? AND stash.stash_id=?
	`, scope.ProjectID, scope.WorkspaceID, stashID).Scan(&accepted, &direct, &rebased, &source, &composed); err != nil {
		t.Fatal(err)
	}
	wants := []state.Digest{
		digestWorkspaceBlobBytesV1ForTest(accepted), digestWorkspaceBlobBytesV1ForTest(direct),
		digestWorkspaceBlobBytesV1ForTest(rebased), digestWorkspaceBlobBytesV1ForTest(source),
		digestWorkspaceBlobBytesV1ForTest(composed),
	}
	gots := []state.Digest{got.AcceptedSnapshotBlobDigest, *got.CandidateDirectTreeBlobDigest, *got.CandidateRebasedTreeBlobDigest, got.StashSourceTreeBlobDigest, got.StashComposedTreeBlobDigest}
	if !reflect.DeepEqual(gots, wants) {
		t.Fatalf("blob digests=%v, want exact raw-byte digests %v", gots, wants)
	}
}

// This deliberately does not call the production digest helper. The fixed
// literal goldens above freeze that helper; this independent calculation ties
// RestoreRetryState's returned values to the exact selected BLOB bytes.
func digestWorkspaceBlobBytesV1ForTest(raw []byte) state.Digest {
	// Filled by the standard-library SHA-256 primitive, independently of the
	// production wrapper whose framing/domain is frozen above.
	sum := sha256.Sum256(raw)
	return state.Digest("sha256:" + hex.EncodeToString(sum[:]))
}
