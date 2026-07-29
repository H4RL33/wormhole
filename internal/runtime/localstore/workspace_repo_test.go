package localstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestWorkspaceScopeMismatchIsRejected(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	a := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	b := createBinding(t, repo, "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	wrong := types.WorkspaceScope{ProjectID: a.Scope.ProjectID, WorkspaceID: b.Scope.WorkspaceID}
	err := repo.WithImmediateWorkspace(context.Background(), wrong, func(*WorkspaceMutationTx) error { return nil })
	requireNotFound(t, err)
}

func TestValidWorkspacesRemainIsolated(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	a := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	b := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	insertWorkspaceOperation(t, store, a.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000099"), "active")
	if err := repo.WithImmediateWorkspace(context.Background(), b.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.ActiveOperationsAfter(context.Background(), 0)
		if err != nil {
			return err
		}
		if len(got) != 0 {
			t.Fatalf("workspace B operations=%+v, want none", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceRegistrationIsIdempotent(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	binding := workspaceBinding("00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	tree := workspaceTree(t, binding.Scope.ProjectID, binding.Repository)
	binding = bindingWithTreeDigest(t, binding, tree)
	first, created, err := repo.RegisterWorkspace(context.Background(), binding, tree)
	if err != nil || !created {
		t.Fatalf("first registration created=%v err=%v", created, err)
	}
	second, created, err := repo.RegisterWorkspace(context.Background(), binding, tree)
	if err != nil || created {
		t.Fatalf("repeat registration created=%v err=%v", created, err)
	}
	if second != first {
		t.Fatalf("repeat binding=%+v, want %+v", second, first)
	}
}

func TestWorkspaceRegistrationCheckoutCollision(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	first := workspaceBinding("00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	firstTree := workspaceTree(t, first.Scope.ProjectID, first.Repository)
	first = bindingWithTreeDigest(t, first, firstTree)
	if _, _, err := repo.RegisterWorkspace(context.Background(), first, firstTree); err != nil {
		t.Fatal(err)
	}
	second := workspaceBinding("00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000012", "/checkout-a", 1, 11)
	secondTree := workspaceTree(t, second.Scope.ProjectID, second.Repository)
	second = bindingWithTreeDigest(t, second, secondTree)
	_, _, err := repo.RegisterWorkspace(context.Background(), second, secondTree)
	if !errors.Is(err, ErrCheckoutCollision) {
		t.Fatalf("collision error=%v, want ErrCheckoutCollision", err)
	}
}

func TestWorkspaceTreeCodecRoundTrip(t *testing.T) {
	tree := workspaceTree(t, "00000000-0000-4000-8000-000000000001", types.RepositoryIdentity{})
	encoded, err := encodeFileList(tree)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeFileList(encoded)
	if err != nil {
		t.Fatal(err)
	}
	want, err := state.DecodeTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	got, err := state.DecodeTree(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if got.Digest != want.Digest {
		t.Fatalf("round-trip digest=%s, want %s", got.Digest, want.Digest)
	}
	reencoded, err := encodeFileList(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if string(reencoded) != string(encoded) {
		t.Fatal("file-list codec is not deterministic")
	}
}

func TestWorkspaceRepoResolveWorkingDirectoryAndStableList(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	outer := createBinding(t, repo, "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000021", "/checkout", 1, 11)
	inner := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout/nested", 2, 12)
	got, err := repo.ResolveWorkingDirectory(context.Background(), types.WorkspaceContext{WorkingDirectory: "/checkout/nested/child"})
	if err != nil {
		t.Fatal(err)
	}
	if got != inner {
		t.Fatalf("resolved=%+v, want inner %+v", got, inner)
	}
	if _, err := repo.ResolveWorkingDirectory(context.Background(), types.WorkspaceContext{WorkingDirectory: "/checkout-sibling"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("sibling resolution error=%v, want ErrNotFound", err)
	}
	bindings, err := repo.RegisteredWorkspaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 2 || bindings[0] != inner || bindings[1] != outer {
		t.Fatalf("stable bindings=%+v", bindings)
	}
}

func TestWorkspaceRepoHasOpenConflictsExactScope(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	ctx := context.Background()
	a := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	b := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	c := createBinding(t, repo, "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000011", "/checkout-c", 3, 13)

	insertWorkspaceConflict(t, store, a.Scope, "conflict-a-resolved", state.RecordKey{Kind: "task", ID: "00000000-0000-4000-8000-000000000021"}, "resolved")
	insertWorkspaceConflict(t, store, b.Scope, "conflict-b-open", state.RecordKey{Kind: "task", ID: "00000000-0000-4000-8000-000000000022"}, "open")
	insertWorkspaceConflict(t, store, c.Scope, "conflict-c-open", state.RecordKey{Kind: "task", ID: "00000000-0000-4000-8000-000000000023"}, "open")

	for _, test := range []struct {
		name  string
		scope types.WorkspaceScope
		want  bool
	}{
		{name: "resolved only", scope: a.Scope, want: false},
		{name: "same project other workspace", scope: b.Scope, want: true},
		{name: "other project", scope: c.Scope, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := repo.HasOpenConflicts(ctx, test.scope)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("HasOpenConflicts(%+v)=%v, want %v", test.scope, got, test.want)
			}
		})
	}

	insertWorkspaceConflict(t, store, a.Scope, "conflict-a-open", state.RecordKey{Kind: "channel", ID: "00000000-0000-4000-8000-000000000024"}, "open")
	got, err := repo.HasOpenConflicts(ctx, a.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("HasOpenConflicts returned false for an exact-scope open conflict")
	}
}

func TestWorkspaceRepoHasOpenConflictsQueryFailure(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.HasOpenConflicts(context.Background(), binding.Scope); err == nil {
		t.Fatal("HasOpenConflicts hid a query failure")
	}
}

func TestWorkspaceMutationTxHasOpenConflictsExactScope(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	a := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	b := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	insertWorkspaceConflict(t, store, b.Scope, "conflict-b-open", state.RecordKey{Kind: "task", ID: "00000000-0000-4000-8000-000000000021"}, "open")

	err := repo.WithImmediateWorkspace(context.Background(), a.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.HasOpenConflicts(context.Background())
		if err != nil {
			return err
		}
		if got {
			t.Fatal("transaction observed another workspace's conflict")
		}
		_, err = tx.conn.ExecContext(context.Background(), `
			INSERT INTO workspace_conflicts
			(project_id, workspace_id, conflict_id, record_kind, record_id, field_path,
			 conflict_kind, base_json, ours_json, theirs_json, state)
			VALUES (?, ?, 'conflict-a-open', 'task', ?, '/title', 'same_field', '{}', '{}', '{}', 'open')
		`, a.Scope.ProjectID, a.Scope.WorkspaceID, "00000000-0000-4000-8000-000000000022")
		if err != nil {
			return err
		}
		got, err = tx.HasOpenConflicts(context.Background())
		if err != nil {
			return err
		}
		if !got {
			t.Fatal("transaction did not observe its own exact-scope conflict write")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceMutationTxHasOpenConflictForKeys(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	neighbor := createBinding(t, repo, binding.Scope.ProjectID, "00000000-0000-4000-8000-000000000012", "/checkout-neighbor", 2, 12)
	otherProject := createBinding(t, repo, "00000000-0000-4000-8000-000000000002", string(binding.Scope.WorkspaceID), "/checkout-other-project", 3, 13)
	taskKey := state.RecordKey{Kind: "task", ID: "00000000-0000-4000-8000-000000000021"}
	channelKey := state.RecordKey{Kind: "channel", ID: "00000000-0000-4000-8000-000000000022"}
	resolvedKey := state.RecordKey{Kind: "actor", ID: "00000000-0000-4000-8000-000000000023"}
	transactionKey := state.RecordKey{Kind: "event", ID: "00000000-0000-4000-8000-000000000024"}
	neighborKey := state.RecordKey{Kind: "task_link", ID: "00000000-0000-4000-8000-000000000025"}
	insertWorkspaceConflict(t, store, binding.Scope, "conflict-task-open", taskKey, "open")
	insertWorkspaceConflict(t, store, binding.Scope, "conflict-channel-open", channelKey, "open")
	insertWorkspaceConflict(t, store, binding.Scope, "conflict-actor-resolved", resolvedKey, "resolved")
	insertWorkspaceConflict(t, store, neighbor.Scope, "conflict-neighbor-open", neighborKey, "open")
	insertWorkspaceConflict(t, store, otherProject.Scope, "conflict-other-project-open", neighborKey, "open")

	err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		for _, test := range []struct {
			name string
			keys []state.RecordKey
			want bool
		}{
			{name: "matching deduplicated key", keys: []state.RecordKey{taskKey, taskKey}, want: true},
			{name: "second matching key", keys: []state.RecordKey{channelKey}, want: true},
			{name: "resolved key", keys: []state.RecordKey{resolvedKey}, want: false},
			{name: "unrelated key", keys: []state.RecordKey{{Kind: "task", ID: "00000000-0000-4000-8000-000000000024"}}, want: false},
			{name: "same ID different kind", keys: []state.RecordKey{{Kind: "channel", ID: taskKey.ID}}, want: false},
			{name: "neighbor scopes", keys: []state.RecordKey{neighborKey}, want: false},
			{name: "empty keys", keys: nil, want: false},
		} {
			t.Run(test.name, func(t *testing.T) {
				got, err := tx.HasOpenConflictForKeys(context.Background(), test.keys)
				if err != nil {
					t.Fatal(err)
				}
				if got != test.want {
					t.Fatalf("HasOpenConflictForKeys(%+v)=%v, want %v", test.keys, got, test.want)
				}
			})
		}
		for _, key := range []state.RecordKey{
			{Kind: "unknown", ID: taskKey.ID},
			{Kind: "task", ID: "BAD"},
			{Kind: "project", ID: otherProject.Scope.ProjectID},
		} {
			if _, err := tx.HasOpenConflictForKeys(context.Background(), []state.RecordKey{key}); err == nil {
				t.Fatalf("HasOpenConflictForKeys accepted malformed key %+v", key)
			}
		}
		_, err := tx.conn.ExecContext(context.Background(), `
			INSERT INTO workspace_conflicts
			(project_id, workspace_id, conflict_id, record_kind, record_id, field_path,
			 conflict_kind, base_json, ours_json, theirs_json, state)
			VALUES (?, ?, 'conflict-transaction-open', ?, ?, '', 'immutable_record', '{}', '{}', '{}', 'open')
		`, binding.Scope.ProjectID, binding.Scope.WorkspaceID, transactionKey.Kind, transactionKey.ID)
		if err != nil {
			return err
		}
		matched, err := tx.HasOpenConflictForKeys(context.Background(), []state.RecordKey{transactionKey})
		if err != nil {
			return err
		}
		if !matched {
			t.Fatal("HasOpenConflictForKeys did not observe its transaction-local matching conflict")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceMutationTxHasOpenConflictForKeysFailsClosedOnMalformedRow(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	matching := state.RecordKey{Kind: "task", ID: "00000000-0000-4000-8000-000000000021"}
	insertWorkspaceConflict(t, store, binding.Scope, "a-conflict-matching", matching, "open")
	insertWorkspaceConflict(t, store, binding.Scope, "z-conflict-malformed", state.RecordKey{Kind: "unknown", ID: "BAD"}, "open")
	err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.HasOpenConflictForKeys(context.Background(), []state.RecordKey{matching})
		return err
	})
	if err == nil {
		t.Fatal("HasOpenConflictForKeys hid a malformed open-conflict row")
	}
	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.HasOpenConflictForKeys(context.Background(), nil)
		return err
	})
	if err == nil {
		t.Fatal("HasOpenConflictForKeys skipped malformed-row validation for empty targets")
	}
}

func TestWithImmediateWorkspaceRollsBackCallbackFailure(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	rollbackErr := errors.New("rollback fixture")
	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.conn.ExecContext(context.Background(), `
			INSERT INTO workspace_conflicts
			(project_id, workspace_id, conflict_id, record_kind, record_id, field_path,
			 conflict_kind, base_json, ours_json, theirs_json, state)
			VALUES (?, ?, 'rolled-back-conflict', 'task', ?, '/title', 'same_field', '{}', '{}', '{}', 'open')
		`, binding.Scope.ProjectID, binding.Scope.WorkspaceID, "00000000-0000-4000-8000-000000000021")
		if err != nil {
			return err
		}
		return rollbackErr
	})
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("WithImmediateWorkspace error=%v, want rollback fixture", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo = NewWorkspaceRepo(store.DB())
	conflicted, err := repo.HasOpenConflicts(context.Background(), binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if conflicted {
		t.Fatal("callback failure persisted a conflict after reopen")
	}
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(*WorkspaceMutationTx) error { return nil }); err != nil {
		t.Fatalf("subsequent workspace transaction: %v", err)
	}
}

func TestWithImmediateWorkspaceRollsBackCommitFailure(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	operation := validWorkspaceOperation("00000000-0000-4000-8000-000000000099")
	operationJSON, err := state.CanonicalOperation(operation)
	if err != nil {
		t.Fatal(err)
	}

	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		if _, err := tx.conn.ExecContext(context.Background(), `PRAGMA defer_foreign_keys=ON`); err != nil {
			return err
		}
		if err := tx.InsertActiveOperations(context.Background(), []WorkspaceOperationInsert{{
			Generation: 1, OperationID: operation.ID, OperationJSON: operationJSON,
		}}); err != nil {
			return err
		}
		if err := tx.SetStatus(context.Background(), "pending"); err != nil {
			return err
		}
		_, err := tx.conn.ExecContext(context.Background(), `
			INSERT INTO workspace_conflicts
			(project_id, workspace_id, conflict_id, record_kind, record_id, field_path,
			 conflict_kind, base_json, ours_json, theirs_json, state)
			VALUES (?, ?, 'deferred-invalid-fk', 'task', ?, '/title',
			 'same_field', '{}', '{}', '{}', 'open')
		`, "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000012",
			"00000000-0000-4000-8000-000000000021")
		return err
	})
	if err == nil {
		t.Fatal("WithImmediateWorkspace hid a deferred foreign-key COMMIT failure")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo = NewWorkspaceRepo(store.DB())
	record, err := repo.Workspace(context.Background(), binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if record.State != "clean" {
		t.Fatalf("state after failed COMMIT=%q, want clean", record.State)
	}
	if operations := readWorkspaceOperations(t, store, binding.Scope); len(operations) != 0 {
		t.Fatalf("failed COMMIT persisted operations: %+v", operations)
	}
	var invalidConflicts int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM workspace_conflicts WHERE conflict_id='deferred-invalid-fk'`).Scan(&invalidConflicts); err != nil {
		t.Fatal(err)
	}
	if invalidConflicts != 0 {
		t.Fatalf("failed COMMIT persisted %d invalid conflicts", invalidConflicts)
	}

	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		if err := tx.InsertActiveOperations(context.Background(), []WorkspaceOperationInsert{{
			Generation: 1, OperationID: operation.ID, OperationJSON: operationJSON,
		}}); err != nil {
			return err
		}
		return tx.SetStatus(context.Background(), "pending")
	}); err != nil {
		t.Fatalf("transaction after failed COMMIT: %v", err)
	}
	record, err = repo.Workspace(context.Background(), binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if record.State != "pending" || len(readWorkspaceOperations(t, store, binding.Scope)) != 1 {
		t.Fatalf("later transaction state=%q operations=%+v", record.State, readWorkspaceOperations(t, store, binding.Scope))
	}
}

func TestWorkspaceMutationTxCandidateDecode(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	ctx := context.Background()
	if err := repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
		candidate, err := tx.Candidate(ctx)
		if err != nil {
			return err
		}
		if candidate != nil {
			t.Fatalf("missing candidate decoded as %+v", candidate)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	directSnapshot, directBytes := encodedWorkspaceSnapshot(t, binding.Scope.ProjectID, binding.Repository)
	rebasedSnapshot, rebasedBytes := encodedWorkspaceSnapshot(t, binding.Scope.ProjectID, binding.Repository)
	insertWorkspaceCandidate(t, store, binding.Scope, state.Digest(binding.AcceptedTreeDigest), directSnapshot.Digest, directBytes, rebasedBytes, 3)
	if err := repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
		candidate, err := tx.Candidate(ctx)
		if err != nil {
			return err
		}
		if candidate == nil || candidate.AcceptedBaseDigest != state.Digest(binding.AcceptedTreeDigest) ||
			candidate.WorkingTreeDigest != directSnapshot.Digest || candidate.DirectSnapshot.Digest != directSnapshot.Digest ||
			candidate.RebasedSnapshot == nil || candidate.RebasedSnapshot.Digest != rebasedSnapshot.Digest ||
			candidate.RebasedThroughGeneration != 3 {
			t.Fatalf("candidate=%+v", candidate)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceMutationTxCandidateAcceptsRetainedOldBaseHandleAndRemotes(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	oldSnapshot, oldBytes := encodedWorkspaceSnapshot(t, binding.Scope.ProjectID, binding.Repository)
	currentSnapshot := oldSnapshot
	currentSnapshot.Config.Handle = types.ProjectHandle{Namespace: "acme", Name: "current"}
	currentSnapshot.Remotes = &state.RemotesV1{Version: 1, Fabrics: []state.FabricHintV1{{
		Alias: "current", URL: "https://fabric.example.test",
		InstanceID: "00000000-0000-4000-8000-000000000031", RemoteProjectID: "current-project",
		ExpectedRepository: binding.Repository, Mode: "public",
	}}}
	currentSnapshot, currentBytes := encodedSnapshot(t, currentSnapshot)
	if currentSnapshot.Config.Handle == oldSnapshot.Config.Handle || currentSnapshot.Remotes == nil || oldSnapshot.Remotes != nil {
		t.Fatal("candidate regression fixture does not differ in Git-owned fields")
	}
	if _, err := store.DB().Exec(`
		UPDATE workspace_bindings
		SET accepted_digest=?, accepted_snapshot=?
		WHERE project_id=? AND workspace_id=?
	`, currentSnapshot.Digest, currentBytes, binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	insertWorkspaceCandidate(t, store, binding.Scope, currentSnapshot.Digest, oldSnapshot.Digest, oldBytes, nil, 0)
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		workspace, err := tx.Workspace(context.Background())
		if err != nil {
			return err
		}
		candidate, err := tx.Candidate(context.Background())
		if err != nil {
			return err
		}
		if candidate == nil || candidate.DirectSnapshot.Config.Handle != oldSnapshot.Config.Handle || candidate.DirectSnapshot.Remotes != nil {
			t.Fatalf("retained old-base candidate=%+v", candidate)
		}
		if workspace.Snapshot.Config.Handle != currentSnapshot.Config.Handle || workspace.Snapshot.Remotes == nil {
			t.Fatalf("current accepted snapshot=%+v", workspace.Snapshot.Config)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceMutationTxCandidateDecodeRejectsCorruption(t *testing.T) {
	const otherProject = "00000000-0000-4000-8000-000000000002"
	wrongRepository := types.RepositoryIdentity{Provider: "github", ImmutableID: "R_other", CanonicalRemote: "https://github.com/acme/other"}
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, binding types.WorkspaceBinding, accepted *state.Digest, working *state.Digest, direct *[]byte, rebased *[]byte, boundary *int64)
	}{
		{name: "corrupt direct file list", mutate: func(_ *testing.T, _ types.WorkspaceBinding, _ *state.Digest, _ *state.Digest, direct *[]byte, _ *[]byte, _ *int64) {
			*direct = []byte{1}
		}},
		{name: "wrong working digest", mutate: func(_ *testing.T, _ types.WorkspaceBinding, _ *state.Digest, working *state.Digest, _ *[]byte, _ *[]byte, _ *int64) {
			*working = state.Digest("sha256:" + strings.Repeat("a", 64))
		}},
		{name: "stale accepted base digest", mutate: func(_ *testing.T, _ types.WorkspaceBinding, accepted *state.Digest, _ *state.Digest, _ *[]byte, _ *[]byte, _ *int64) {
			*accepted = state.Digest("sha256:" + strings.Repeat("a", 64))
		}},
		{name: "boundary without rebased tree", mutate: func(_ *testing.T, _ types.WorkspaceBinding, _ *state.Digest, _ *state.Digest, _ *[]byte, rebased *[]byte, boundary *int64) {
			*rebased, *boundary = nil, 1
		}},
		{name: "negative rebased boundary", mutate: func(_ *testing.T, _ types.WorkspaceBinding, _ *state.Digest, _ *state.Digest, _ *[]byte, _ *[]byte, boundary *int64) {
			*boundary = -1
		}},
		{name: "corrupt rebased file list", mutate: func(_ *testing.T, _ types.WorkspaceBinding, _ *state.Digest, _ *state.Digest, _ *[]byte, rebased *[]byte, _ *int64) {
			*rebased = []byte{1}
		}},
		{name: "cross-project direct snapshot", mutate: func(t *testing.T, _ types.WorkspaceBinding, _ *state.Digest, working *state.Digest, direct *[]byte, _ *[]byte, _ *int64) {
			snapshot, encoded := encodedWorkspaceSnapshot(t, otherProject, types.RepositoryIdentity{})
			*working, *direct = snapshot.Digest, encoded
		}},
		{name: "cross-repository direct snapshot", mutate: func(t *testing.T, binding types.WorkspaceBinding, _ *state.Digest, working *state.Digest, direct *[]byte, _ *[]byte, _ *int64) {
			snapshot, encoded := encodedWorkspaceSnapshot(t, binding.Scope.ProjectID, wrongRepository)
			*working, *direct = snapshot.Digest, encoded
		}},
		{name: "cross-project rebased snapshot", mutate: func(t *testing.T, _ types.WorkspaceBinding, _ *state.Digest, _ *state.Digest, _ *[]byte, rebased *[]byte, _ *int64) {
			_, *rebased = encodedWorkspaceSnapshot(t, otherProject, types.RepositoryIdentity{})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, repo := openWorkspaceStore(t)
			binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
			directSnapshot, direct := encodedWorkspaceSnapshot(t, binding.Scope.ProjectID, binding.Repository)
			_, rebased := encodedWorkspaceSnapshot(t, binding.Scope.ProjectID, binding.Repository)
			accepted := state.Digest(binding.AcceptedTreeDigest)
			working := directSnapshot.Digest
			boundary := int64(1)
			test.mutate(t, binding, &accepted, &working, &direct, &rebased, &boundary)
			insertWorkspaceCandidate(t, store, binding.Scope, accepted, working, direct, rebased, boundary)
			err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				_, err := tx.Candidate(context.Background())
				return err
			})
			if err == nil {
				t.Fatal("Candidate served corrupt persisted state")
			}
		})
	}
}

func TestWorkspaceMutationTxWorkspaceAndActiveOperationsAfter(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	a := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	b := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	op1 := validWorkspaceOperation("00000000-0000-4000-8000-000000000091")
	op2 := validWorkspaceOperation("00000000-0000-4000-8000-000000000092")
	op3 := validWorkspaceOperation("00000000-0000-4000-8000-000000000093")
	op4 := validWorkspaceOperation("00000000-0000-4000-8000-000000000094")
	insertWorkspaceOperation(t, store, a.Scope, 1, op1, "rebased")
	secondBytes := insertWorkspaceOperation(t, store, a.Scope, 2, op2, "active")
	insertWorkspaceOperation(t, store, b.Scope, 3, op3, "active")
	fourthBytes := insertWorkspaceOperation(t, store, a.Scope, 4, op4, "active")

	err := repo.WithImmediateWorkspace(context.Background(), a.Scope, func(tx *WorkspaceMutationTx) error {
		workspace, err := tx.Workspace(context.Background())
		if err != nil {
			return err
		}
		if workspace.Binding != a || workspace.State != "clean" {
			t.Fatalf("workspace=%+v", workspace)
		}
		operations, err := tx.ActiveOperationsAfter(context.Background(), 0)
		if err != nil {
			return err
		}
		if len(operations) != 2 || operations[0].Generation != 2 || operations[1].Generation != 4 ||
			operations[0].OperationID != op2.ID || operations[1].OperationID != op4.ID ||
			operations[0].State != "active" || operations[1].State != "active" ||
			!bytes.Equal(operations[0].OperationJSON, secondBytes) || !bytes.Equal(operations[1].OperationJSON, fourthBytes) {
			t.Fatalf("active operations=%+v", operations)
		}
		operations, err = tx.ActiveOperationsAfter(context.Background(), 2)
		if err != nil {
			return err
		}
		if len(operations) != 1 || operations[0].Generation != 4 {
			t.Fatalf("active operations after 2=%+v", operations)
		}
		if _, err := tx.ActiveOperationsAfter(context.Background(), -1); err == nil {
			t.Fatal("ActiveOperationsAfter accepted a negative boundary")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceMutationTxActiveOperationsAfterRejectsCorruption(t *testing.T) {
	canonicalOperation := validWorkspaceOperation("00000000-0000-4000-8000-000000000091")
	canonicalBytes, err := state.CanonicalOperation(canonicalOperation)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name        string
		generation  int64
		operationID string
		operation   []byte
		rowState    string
	}{
		{name: "malformed JSON", generation: 1, operationID: canonicalOperation.ID, operation: []byte("{"), rowState: "active"},
		{name: "unknown field", generation: 1, operationID: canonicalOperation.ID, operation: append(bytes.TrimSuffix(bytes.Clone(canonicalBytes), []byte("}\n")), []byte(",\"unknown\":true}\n")...), rowState: "active"},
		{name: "trailing JSON", generation: 1, operationID: canonicalOperation.ID, operation: append(bytes.Clone(canonicalBytes), []byte("{}")...), rowState: "active"},
		{name: "non-canonical bytes", generation: 1, operationID: canonicalOperation.ID, operation: bytes.TrimSuffix(bytes.Clone(canonicalBytes), []byte("\n")), rowState: "active"},
		{name: "row ID mismatch", generation: 1, operationID: "00000000-0000-4000-8000-000000000092", operation: canonicalBytes, rowState: "active"},
		{name: "invalid row ID", generation: 1, operationID: "BAD", operation: canonicalBytes, rowState: "active"},
		{name: "invalid generation", generation: 0, operationID: canonicalOperation.ID, operation: canonicalBytes, rowState: "active"},
		{name: "invalid state", generation: 1, operationID: canonicalOperation.ID, operation: canonicalBytes, rowState: "poison"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, repo := openWorkspaceStore(t)
			store.DB().SetMaxOpenConns(1)
			binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
			if test.generation <= 0 || test.rowState == "poison" {
				if _, err := store.DB().Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
					t.Fatal(err)
				}
			}
			insertWorkspaceOperationRaw(t, store, binding.Scope, test.generation, test.operationID, test.operation, test.rowState)
			err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				_, err := tx.ActiveOperationsAfter(context.Background(), 0)
				return err
			})
			if err == nil {
				t.Fatal("ActiveOperationsAfter served corrupt persisted state")
			}
		})
	}
}

func TestWorkspaceMutationTxNextGenerationAndInsertActiveOperations(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	insertWorkspaceOperation(t, store, binding.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "rebased")
	insertWorkspaceOperation(t, store, binding.Scope, 3, validWorkspaceOperation("00000000-0000-4000-8000-000000000093"), "materialized")
	op4 := validWorkspaceOperation("00000000-0000-4000-8000-000000000094")
	op5 := validWorkspaceOperation("00000000-0000-4000-8000-000000000095")
	bytes4, err := state.CanonicalOperation(op4)
	if err != nil {
		t.Fatal(err)
	}
	bytes5, err := state.CanonicalOperation(op5)
	if err != nil {
		t.Fatal(err)
	}
	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		next, err := tx.NextGeneration(context.Background())
		if err != nil {
			return err
		}
		if next != 4 {
			t.Fatalf("NextGeneration=%d, want 4", next)
		}
		if err := tx.InsertActiveOperations(context.Background(), []WorkspaceOperationInsert{
			{Generation: 4, OperationID: op4.ID, OperationJSON: bytes4},
			{Generation: 5, OperationID: op5.ID, OperationJSON: bytes5},
		}); err != nil {
			return err
		}
		next, err = tx.NextGeneration(context.Background())
		if err != nil {
			return err
		}
		if next != 6 {
			t.Fatalf("NextGeneration after insert=%d, want 6", next)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	operations := readWorkspaceOperations(t, store, binding.Scope)
	if len(operations) != 4 || operations[2].Generation != 4 || operations[3].Generation != 5 ||
		operations[2].State != "active" || operations[3].State != "active" ||
		!bytes.Equal(operations[2].OperationJSON, bytes4) || !bytes.Equal(operations[3].OperationJSON, bytes5) {
		t.Fatalf("persisted operations=%+v", operations)
	}
}

func TestWorkspaceMutationTxNextGenerationRejectsNonpositivePersistedRows(t *testing.T) {
	for _, test := range []struct {
		name                string
		positiveGenerations []int64
	}{
		{name: "zero only"},
		{name: "mixed positive and zero", positiveGenerations: []int64{2}},
	} {
		t.Run(test.name, func(t *testing.T) {
			databasePath := filepath.Join(t.TempDir(), "gateway.db")
			store, err := Open(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			store.DB().SetMaxOpenConns(1)
			repo := NewWorkspaceRepo(store.DB())
			binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
			if _, err := store.DB().Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
				t.Fatal(err)
			}
			zeroOperation := validWorkspaceOperation("00000000-0000-4000-8000-000000000090")
			insertWorkspaceOperation(t, store, binding.Scope, 0, zeroOperation, "active")
			for index, generation := range test.positiveGenerations {
				operationID := fmt.Sprintf("00000000-0000-4000-8000-%012d", 91+index)
				insertWorkspaceOperation(t, store, binding.Scope, generation, validWorkspaceOperation(operationID), "rebased")
			}
			newOperation := validWorkspaceOperation("00000000-0000-4000-8000-000000000099")
			newBytes, err := state.CanonicalOperation(newOperation)
			if err != nil {
				t.Fatal(err)
			}
			mutationErr := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				if err := tx.SetStatus(context.Background(), "pending"); err != nil {
					return err
				}
				next, err := tx.NextGeneration(context.Background())
				if err != nil {
					return err
				}
				return tx.InsertActiveOperations(context.Background(), []WorkspaceOperationInsert{{
					Generation: next, OperationID: newOperation.ID, OperationJSON: newBytes,
				}})
			})
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = Open(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			repo = NewWorkspaceRepo(store.DB())
			if mutationErr == nil {
				t.Fatal("NextGeneration accepted a nonpositive persisted generation")
			}
			operations := readWorkspaceOperations(t, store, binding.Scope)
			if len(operations) != 1+len(test.positiveGenerations) {
				t.Fatalf("generation failure left later write=%+v", operations)
			}
			workspace, err := repo.Workspace(context.Background(), binding.Scope)
			if err != nil {
				t.Fatal(err)
			}
			if workspace.State != "clean" {
				t.Fatalf("generation failure committed status=%q", workspace.State)
			}
			if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(*WorkspaceMutationTx) error { return nil }); err != nil {
				t.Fatalf("subsequent workspace transaction: %v", err)
			}
		})
	}
}

func TestWorkspaceMutationTxInsertActiveOperationsRejectsInvalidBatchWithoutWrites(t *testing.T) {
	valid := validWorkspaceOperation("00000000-0000-4000-8000-000000000091")
	validBytes, err := state.CanonicalOperation(valid)
	if err != nil {
		t.Fatal(err)
	}
	other := validWorkspaceOperation("00000000-0000-4000-8000-000000000092")
	otherBytes, err := state.CanonicalOperation(other)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		operations []WorkspaceOperationInsert
	}{
		{name: "nonpositive generation", operations: []WorkspaceOperationInsert{{Generation: 0, OperationID: valid.ID, OperationJSON: validBytes}}},
		{name: "generation gap", operations: []WorkspaceOperationInsert{{Generation: 2, OperationID: valid.ID, OperationJSON: validBytes}}},
		{name: "nonconsecutive batch", operations: []WorkspaceOperationInsert{{Generation: 1, OperationID: valid.ID, OperationJSON: validBytes}, {Generation: 3, OperationID: other.ID, OperationJSON: otherBytes}}},
		{name: "invalid operation ID", operations: []WorkspaceOperationInsert{{Generation: 1, OperationID: "BAD", OperationJSON: validBytes}}},
		{name: "row ID mismatch", operations: []WorkspaceOperationInsert{{Generation: 1, OperationID: other.ID, OperationJSON: validBytes}}},
		{name: "malformed operation", operations: []WorkspaceOperationInsert{{Generation: 1, OperationID: valid.ID, OperationJSON: []byte("{")}}},
		{name: "duplicate operation ID", operations: []WorkspaceOperationInsert{{Generation: 1, OperationID: valid.ID, OperationJSON: validBytes}, {Generation: 2, OperationID: valid.ID, OperationJSON: validBytes}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, repo := openWorkspaceStore(t)
			binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
			err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				return tx.InsertActiveOperations(context.Background(), test.operations)
			})
			if err == nil {
				t.Fatal("InsertActiveOperations accepted invalid batch")
			}
			if operations := readWorkspaceOperations(t, store, binding.Scope); len(operations) != 0 {
				t.Fatalf("invalid batch persisted operations=%+v", operations)
			}
		})
	}
}

func TestWorkspaceMutationTxInsertActiveOperationsCollisionRollsBackBatch(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	existing := validWorkspaceOperation("00000000-0000-4000-8000-000000000091")
	insertWorkspaceOperation(t, store, binding.Scope, 1, existing, "rebased")
	newOperation := validWorkspaceOperation("00000000-0000-4000-8000-000000000092")
	newBytes, err := state.CanonicalOperation(newOperation)
	if err != nil {
		t.Fatal(err)
	}
	existingBytes, err := state.CanonicalOperation(existing)
	if err != nil {
		t.Fatal(err)
	}
	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.InsertActiveOperations(context.Background(), []WorkspaceOperationInsert{
			{Generation: 2, OperationID: newOperation.ID, OperationJSON: newBytes},
			{Generation: 3, OperationID: existing.ID, OperationJSON: existingBytes},
		})
	})
	if err == nil {
		t.Fatal("InsertActiveOperations accepted an existing operation ID collision")
	}
	operations := readWorkspaceOperations(t, store, binding.Scope)
	if len(operations) != 1 || operations[0].OperationID != existing.ID {
		t.Fatalf("collision left partial batch=%+v", operations)
	}
}

func TestWorkspaceMutationTxInsertActiveOperationsRejectsIgnoredInsert(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	first := validWorkspaceOperation("00000000-0000-4000-8000-000000000091")
	ignored := validWorkspaceOperation("00000000-0000-4000-8000-000000000092")
	firstBytes, err := state.CanonicalOperation(first)
	if err != nil {
		t.Fatal(err)
	}
	ignoredBytes, err := state.CanonicalOperation(ignored)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(fmt.Sprintf(`
		CREATE TRIGGER ignore_second_workspace_operation
		BEFORE INSERT ON workspace_overlay_operations
		WHEN NEW.operation_id='%s'
		BEGIN SELECT RAISE(IGNORE); END
	`, ignored.ID)); err != nil {
		t.Fatal(err)
	}
	mutationErr := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		if err := tx.InsertActiveOperations(context.Background(), []WorkspaceOperationInsert{
			{Generation: 1, OperationID: first.ID, OperationJSON: firstBytes},
			{Generation: 2, OperationID: ignored.ID, OperationJSON: ignoredBytes},
		}); err != nil {
			return err
		}
		return tx.SetStatus(context.Background(), "pending")
	})
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo = NewWorkspaceRepo(store.DB())
	if mutationErr == nil {
		t.Fatal("InsertActiveOperations accepted an ignored insert")
	}
	if operations := readWorkspaceOperations(t, store, binding.Scope); len(operations) != 0 {
		t.Fatalf("ignored insert left partial batch=%+v", operations)
	}
	workspace, err := repo.Workspace(context.Background(), binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if workspace.State != "clean" {
		t.Fatalf("ignored insert committed status=%q", workspace.State)
	}
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(*WorkspaceMutationTx) error { return nil }); err != nil {
		t.Fatalf("subsequent workspace transaction: %v", err)
	}
}

func TestWorkspaceMutationTxSetStatusValidStatesAndExactScope(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	a := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	b := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	for _, status := range []string{"pending", "conflicted", "blocked", "clean"} {
		if err := repo.WithImmediateWorkspace(context.Background(), a.Scope, func(tx *WorkspaceMutationTx) error {
			if err := tx.SetStatus(context.Background(), status); err != nil {
				return err
			}
			workspace, err := tx.Workspace(context.Background())
			if err != nil {
				return err
			}
			if workspace.State != status {
				t.Fatalf("transaction status=%q, want %q", workspace.State, status)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	neighbor, err := repo.Workspace(context.Background(), b.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if neighbor.State != "clean" {
		t.Fatalf("neighbor status=%q, want clean", neighbor.State)
	}
	if err := repo.WithImmediateWorkspace(context.Background(), a.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.SetStatus(context.Background(), "unknown")
	}); err == nil {
		t.Fatal("SetStatus accepted an invalid state")
	}
	workspace, err := repo.Workspace(context.Background(), a.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if workspace.State != "clean" {
		t.Fatalf("invalid status changed workspace to %q", workspace.State)
	}
}

func TestWorkspaceMutationTxSetStatusFailureRollsBackPriorWrites(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	if _, err := store.DB().Exec(`
		CREATE TRIGGER reject_workspace_pending
		BEFORE UPDATE OF status ON workspace_bindings
		WHEN NEW.project_id='00000000-0000-4000-8000-000000000001'
		 AND NEW.workspace_id='00000000-0000-4000-8000-000000000011'
		 AND NEW.status='pending'
		BEGIN SELECT RAISE(ABORT, 'status rejected'); END
	`); err != nil {
		t.Fatal(err)
	}
	operation := validWorkspaceOperation("00000000-0000-4000-8000-000000000091")
	operationJSON, err := state.CanonicalOperation(operation)
	if err != nil {
		t.Fatal(err)
	}
	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		if err := tx.InsertActiveOperations(context.Background(), []WorkspaceOperationInsert{{
			Generation: 1, OperationID: operation.ID, OperationJSON: operationJSON,
		}}); err != nil {
			return err
		}
		return tx.SetStatus(context.Background(), "pending")
	})
	if err == nil {
		t.Fatal("SetStatus hid an update failure")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo = NewWorkspaceRepo(store.DB())
	if operations := readWorkspaceOperations(t, store, binding.Scope); len(operations) != 0 {
		t.Fatalf("status failure left prior operations=%+v", operations)
	}
	workspace, err := repo.Workspace(context.Background(), binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if workspace.State != "clean" {
		t.Fatalf("status failure changed workspace to %q", workspace.State)
	}
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(*WorkspaceMutationTx) error { return nil }); err != nil {
		t.Fatalf("subsequent workspace transaction: %v", err)
	}
}

func TestWorkspaceTreeCodecRejectsMalformedInput(t *testing.T) {
	for _, encoded := range [][]byte{
		nil,
		append([]byte{0, 0, 0, 0, 0, 0, 0, 1}, []byte{0, 0, 0, 0, 0, 0, 0, 9}...),
	} {
		if _, err := decodeFileList(encoded); err == nil {
			t.Fatalf("decodeFileList(%x) succeeded", encoded)
		}
	}
	invalid := state.Tree{{Path: "../escape", Data: []byte("x")}}
	if _, err := encodeFileList(invalid); err == nil {
		t.Fatal("encodeFileList accepted a traversal path")
	}
}

func openWorkspaceStore(t *testing.T) (*Store, *WorkspaceRepo) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, NewWorkspaceRepo(store.DB())
}

func createBinding(t *testing.T, repo *WorkspaceRepo, projectID, workspaceID, path string, device, inode uint64) types.WorkspaceBinding {
	t.Helper()
	binding := workspaceBinding(projectID, workspaceID, path, device, inode)
	tree := workspaceTree(t, projectID, binding.Repository)
	binding = bindingWithTreeDigest(t, binding, tree)
	created, ok, err := repo.RegisterWorkspace(context.Background(), binding, tree)
	if err != nil || !ok {
		t.Fatalf("RegisterWorkspace: created=%v binding=%+v err=%v", ok, created, err)
	}
	return created
}

func bindingWithTreeDigest(t *testing.T, binding types.WorkspaceBinding, tree state.Tree) types.WorkspaceBinding {
	t.Helper()
	snapshot, err := state.DecodeTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	binding.AcceptedTreeDigest = string(snapshot.Digest)
	return binding
}

func workspaceBinding(projectID, workspaceID, path string, device, inode uint64) types.WorkspaceBinding {
	if path[0] != filepath.Separator {
		path = string(filepath.Separator) + path
	}
	return types.WorkspaceBinding{
		Scope:       types.WorkspaceScope{ProjectID: projectID, WorkspaceID: types.WorkspaceID(workspaceID)},
		Checkout:    types.CheckoutIdentity{CanonicalPath: filepath.Clean(path), Device: device, Inode: inode},
		Repository:  types.RepositoryIdentity{},
		AcceptedRef: "refs/heads/main", AcceptedCommitSHA: strings.Repeat("a", 40),
		AcceptedTreeDigest: "sha256:" + strings.Repeat("b", 64),
	}
}

func workspaceTree(t *testing.T, projectID string, repository types.RepositoryIdentity) state.Tree {
	t.Helper()
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	snapshot := state.Snapshot{
		Config:  state.ConfigV1{SnapshotVersion: 1, ProjectID: projectID, Handle: types.ProjectHandle{Namespace: "acme", Name: "wormhole"}, Repository: repository},
		Project: state.ProjectV1{SchemaVersion: 1, Kind: "project", ID: projectID, Name: "Wormhole", Aliases: []string{}, CreatedAt: now, UpdatedAt: now, Extensions: state.ExtensionsV1{}},
		Actors:  map[string]state.Record[state.ActorV1]{}, Tasks: map[string]state.Record[state.TaskV1]{},
		TaskLinks: map[string]state.Record[state.TaskLinkV1]{}, Articles: map[string]state.KBRecord{},
		Channels: map[string]state.Record[state.ChannelV1]{}, Events: map[string]state.EventV1{},
		GitLinks: map[string]state.Record[state.GitLinkV1]{},
	}
	tree, err := state.EncodeTree(snapshot)
	if err != nil {
		t.Fatalf("EncodeTree: %v", err)
	}
	return tree
}

func insertWorkspaceOperation(t *testing.T, store *Store, scope types.WorkspaceScope, generation int64, operation state.OperationV1, operationState string) []byte {
	t.Helper()
	operationJSON, err := state.CanonicalOperation(operation)
	if err != nil {
		t.Fatalf("canonical operation: %v", err)
	}
	insertWorkspaceOperationRaw(t, store, scope, generation, operation.ID, operationJSON, operationState)
	return operationJSON
}

func insertWorkspaceOperationRaw(t *testing.T, store *Store, scope types.WorkspaceScope, generation int64, operationID string, operationJSON []byte, operationState string) {
	t.Helper()
	_, err := store.DB().Exec(`
		INSERT INTO workspace_overlay_operations
		(project_id, workspace_id, generation, operation_id, operation_json, state)
		VALUES (?, ?, ?, ?, ?, ?)
	`, scope.ProjectID, scope.WorkspaceID, generation, operationID, operationJSON, operationState)
	if err != nil {
		t.Fatalf("insert workspace operation: %v", err)
	}
}

func readWorkspaceOperations(t *testing.T, store *Store, scope types.WorkspaceScope) []WorkspaceOperation {
	t.Helper()
	rows, err := store.DB().Query(`
		SELECT generation, operation_id, operation_json, state
		FROM workspace_overlay_operations
		WHERE project_id=? AND workspace_id=?
		ORDER BY generation
	`, scope.ProjectID, scope.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	operations := make([]WorkspaceOperation, 0)
	for rows.Next() {
		var operation WorkspaceOperation
		var operationJSON []byte
		if err := rows.Scan(&operation.Generation, &operation.OperationID, &operationJSON, &operation.State); err != nil {
			t.Fatal(err)
		}
		operation.OperationJSON = bytes.Clone(operationJSON)
		operations = append(operations, operation)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return operations
}

func insertWorkspaceConflict(t *testing.T, store *Store, scope types.WorkspaceScope, conflictID string, key state.RecordKey, conflictState string) {
	t.Helper()
	_, err := store.DB().Exec(`
		INSERT INTO workspace_conflicts
		(project_id, workspace_id, conflict_id, record_kind, record_id, field_path,
		 conflict_kind, base_json, ours_json, theirs_json, state)
		VALUES (?, ?, ?, ?, ?, '/title', 'same_field', '{}', '{}', '{}', ?)
	`, scope.ProjectID, scope.WorkspaceID, conflictID, key.Kind, key.ID, conflictState)
	if err != nil {
		t.Fatalf("insert workspace conflict: %v", err)
	}
}

func encodedWorkspaceSnapshot(t *testing.T, projectID string, repository types.RepositoryIdentity) (state.Snapshot, []byte) {
	t.Helper()
	tree := workspaceTree(t, projectID, repository)
	snapshot, err := state.DecodeTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := encodeFileList(tree)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot, encoded
}

func encodedSnapshot(t *testing.T, snapshot state.Snapshot) (state.Snapshot, []byte) {
	t.Helper()
	tree, err := state.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := state.DecodeTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := encodeFileList(tree)
	if err != nil {
		t.Fatal(err)
	}
	return decoded, encoded
}

func insertWorkspaceCandidate(t *testing.T, store *Store, scope types.WorkspaceScope, acceptedBase, working state.Digest, direct, rebased []byte, boundary int64) {
	t.Helper()
	_, err := store.DB().Exec(`
		INSERT INTO workspace_candidates
		(project_id, workspace_id, accepted_base_digest, working_tree_digest, direct_tree,
		 rebased_tree, rebased_through_generation, imported_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'test')
	`, scope.ProjectID, scope.WorkspaceID, acceptedBase, working, direct, rebased, boundary)
	if err != nil {
		t.Fatalf("insert workspace candidate: %v", err)
	}
}

func validWorkspaceOperation(operationID string) state.OperationV1 {
	const (
		humanID = "00000000-0000-4000-8000-000000000061"
		actorID = "00000000-0000-4000-8000-000000000062"
	)
	now := time.Date(2026, 7, 28, 13, 0, 0, 0, time.UTC)
	actor := state.ActorV1{
		SchemaVersion: 1, Kind: "actor", ID: actorID, ActorKind: types.ActorHuman,
		DisplayName: "Workspace Fixture", PublicKeys: []state.PublicKeyV1{}, Extensions: state.ExtensionsV1{},
	}
	return state.OperationV1{
		SchemaVersion: 1, ID: operationID, Kind: state.OperationPutRecord,
		ExpectedViewDigest: state.Digest("sha256:" + strings.Repeat("a", 64)),
		Actor: types.ActorEnvelope{
			ActorKind: types.ActorHuman, HumanPrincipalID: humanID,
			Assurance: types.AssuranceLocal, OccurredAt: now,
		},
		PutRecord: &state.PutRecordV1{Record: state.RecordValueV1{Actor: &actor}},
	}
}

func requireNotFound(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error=%v, want ErrNotFound", err)
	}
}
