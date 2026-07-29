package localstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
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

func TestWorkspaceMutationTxAdvanceAcceptedBaseAPI(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	observedTree := workspaceTree(t, binding.Scope.ProjectID, binding.Repository)
	observedSnapshot, err := state.DecodeTree(observedTree)
	if err != nil {
		t.Fatal(err)
	}
	observedSnapshot.Project.Name = "Observed"
	observedSnapshot.Project.UpdatedAt = observedSnapshot.Project.UpdatedAt.Add(time.Minute)
	observedTree, err = state.EncodeTree(observedSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	observedSnapshot, err = state.DecodeTree(observedTree)
	if err != nil {
		t.Fatal(err)
	}

	var got WorkspaceRecord
	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		expected, err := tx.Workspace(context.Background())
		if err != nil {
			return err
		}
		got, err = tx.AdvanceAcceptedBase(context.Background(), WorkspaceAcceptedBaseTransition{
			Expected: expected, ObservedRef: "refs/heads/next",
			ObservedCommitSHA: strings.Repeat("c", 40), ObservedTree: observedTree,
			NextState: "pending",
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Binding.AcceptedRef != "refs/heads/next" || got.Binding.AcceptedCommitSHA != strings.Repeat("c", 40) ||
		got.Binding.AcceptedTreeDigest != string(observedSnapshot.Digest) || got.Snapshot.Digest != observedSnapshot.Digest || got.State != "pending" {
		t.Fatalf("advanced workspace=%+v", got)
	}
}

func TestWorkspaceMutationTxAdvanceAcceptedBaseRejectsStaleExpectedStateWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*WorkspaceAcceptedBaseTransition)
	}{
		{"scope", func(value *WorkspaceAcceptedBaseTransition) {
			value.Expected.Binding.Scope.WorkspaceID = "00000000-0000-4000-8000-000000000099"
		}},
		{"project scope", func(value *WorkspaceAcceptedBaseTransition) {
			value.Expected.Binding.Scope.ProjectID = "00000000-0000-4000-8000-000000000099"
		}},
		{"checkout path", func(value *WorkspaceAcceptedBaseTransition) { value.Expected.Binding.Checkout.CanonicalPath = "/other" }},
		{"checkout device", func(value *WorkspaceAcceptedBaseTransition) { value.Expected.Binding.Checkout.Device++ }},
		{"checkout inode", func(value *WorkspaceAcceptedBaseTransition) { value.Expected.Binding.Checkout.Inode++ }},
		{"repository identity", func(value *WorkspaceAcceptedBaseTransition) {
			value.Expected.Binding.Repository = types.RepositoryIdentity{Provider: "github", ImmutableID: "other", CanonicalRemote: "https://github.com/acme/other"}
		}},
		{"accepted ref", func(value *WorkspaceAcceptedBaseTransition) { value.Expected.Binding.AcceptedRef = "refs/heads/stale" }},
		{"accepted commit", func(value *WorkspaceAcceptedBaseTransition) {
			value.Expected.Binding.AcceptedCommitSHA = strings.Repeat("d", 40)
		}},
		{"accepted digest", func(value *WorkspaceAcceptedBaseTransition) {
			value.Expected.Binding.AcceptedTreeDigest = "sha256:" + strings.Repeat("d", 64)
		}},
		{"status", func(value *WorkspaceAcceptedBaseTransition) { value.Expected.State = "pending" }},
		{"snapshot", func(value *WorkspaceAcceptedBaseTransition) { value.Expected.Snapshot.Project.Name = "stale" }},
		{"snapshot digest", func(value *WorkspaceAcceptedBaseTransition) {
			value.Expected.Snapshot.Digest = state.Digest("sha256:" + strings.Repeat("d", 64))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, repo := openWorkspaceStore(t)
			binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
			before, err := repo.Workspace(context.Background(), binding.Scope)
			if err != nil {
				t.Fatal(err)
			}
			transition := WorkspaceAcceptedBaseTransition{
				Expected: before, ObservedRef: "refs/heads/next", ObservedCommitSHA: strings.Repeat("c", 40),
				ObservedTree: changedWorkspaceTree(t, binding, "Observed"), NextState: "pending",
			}
			test.mutate(&transition)
			err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				got, err := tx.AdvanceAcceptedBase(context.Background(), transition)
				if err == nil || !reflect.DeepEqual(got, WorkspaceRecord{}) {
					t.Fatalf("stale transition=(%+v,%v), want zero,error", got, err)
				}
				return err
			})
			if err == nil {
				t.Fatal("stale accepted-base transition succeeded")
			}
			after, err := repo.Workspace(context.Background(), binding.Scope)
			if err != nil || !equalWorkspaceRecords(after, before) {
				t.Fatalf("failed transition state=(%+v,%v), want %+v", after, err, before)
			}
		})
	}
}

func TestWorkspaceMutationTxAdvanceAcceptedBaseRejectsInvalidObservedInputs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*WorkspaceAcceptedBaseTransition)
	}{
		{"ref", func(value *WorkspaceAcceptedBaseTransition) { value.ObservedRef = "main" }},
		{"commit", func(value *WorkspaceAcceptedBaseTransition) { value.ObservedCommitSHA = "bad" }},
		{"next state", func(value *WorkspaceAcceptedBaseTransition) { value.NextState = "bad" }},
		{"noncanonical tree", func(value *WorkspaceAcceptedBaseTransition) {
			value.ObservedTree[0], value.ObservedTree[1] = value.ObservedTree[1], value.ObservedTree[0]
		}},
		{"wrong project tree", func(value *WorkspaceAcceptedBaseTransition) {
			value.ObservedTree = workspaceTree(t, "00000000-0000-4000-8000-000000000099", value.Expected.Binding.Repository)
		}},
		{"wrong repository tree", func(value *WorkspaceAcceptedBaseTransition) {
			value.ObservedTree = workspaceTree(t, value.Expected.Binding.Scope.ProjectID, types.RepositoryIdentity{
				Provider: "github", ImmutableID: "other", CanonicalRemote: "https://github.com/acme/other",
			})
		}},
		{"nil tree", func(value *WorkspaceAcceptedBaseTransition) { value.ObservedTree = nil }},
		{"missing tree file", func(value *WorkspaceAcceptedBaseTransition) { value.ObservedTree = value.ObservedTree[1:] }},
		{"malformed tree bytes", func(value *WorkspaceAcceptedBaseTransition) {
			value.ObservedTree[0].Data = []byte("not canonical state")
		}},
		{"invalid tree path", func(value *WorkspaceAcceptedBaseTransition) {
			value.ObservedTree[0].Path = "../config.toml"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, repo := openWorkspaceStore(t)
			binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
			before, err := repo.Workspace(context.Background(), binding.Scope)
			if err != nil {
				t.Fatal(err)
			}
			transition := WorkspaceAcceptedBaseTransition{
				Expected: before, ObservedRef: "refs/heads/next", ObservedCommitSHA: strings.Repeat("c", 40),
				ObservedTree: changedWorkspaceTree(t, binding, "Observed"), NextState: "pending",
			}
			test.mutate(&transition)
			err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				_, err := tx.AdvanceAcceptedBase(context.Background(), transition)
				return err
			})
			if err == nil {
				t.Fatal("invalid observed accepted-base transition succeeded")
			}
			after, readErr := repo.Workspace(context.Background(), binding.Scope)
			if readErr != nil || !equalWorkspaceRecords(after, before) {
				t.Fatalf("invalid transition state=(%+v,%v), want %+v", after, readErr, before)
			}
		})
	}
}

func TestWorkspaceMutationTxAdvanceAcceptedBaseDetectsTriggerDriftAndRollsBack(t *testing.T) {
	for _, test := range []struct {
		name, trigger string
	}{
		{"write failure", `CREATE TRIGGER fail_accepted_base BEFORE UPDATE OF accepted_commit ON workspace_bindings BEGIN SELECT RAISE(ABORT,'injected accepted-base failure'); END`},
		{"after trigger drift", `CREATE TRIGGER drift_accepted_base AFTER UPDATE OF accepted_commit ON workspace_bindings BEGIN UPDATE workspace_bindings SET accepted_ref='refs/heads/drifted' WHERE project_id=NEW.project_id AND workspace_id=NEW.workspace_id; END`},
		{"after trigger hidden timestamp drift", `CREATE TRIGGER drift_accepted_base_timestamp AFTER UPDATE OF accepted_commit ON workspace_bindings BEGIN UPDATE workspace_bindings SET updated_at='2099-01-01 00:00:00+00:00' WHERE project_id=NEW.project_id AND workspace_id=NEW.workspace_id; END`},
		{"after trigger created timestamp drift", `CREATE TRIGGER drift_accepted_base_created AFTER UPDATE OF accepted_commit ON workspace_bindings BEGIN UPDATE workspace_bindings SET created_at='2000-01-01 00:00:00+00:00' WHERE project_id=NEW.project_id AND workspace_id=NEW.workspace_id; END`},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, repo := openWorkspaceStore(t)
			binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
			before, err := repo.Workspace(context.Background(), binding.Scope)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.DB().Exec(test.trigger); err != nil {
				t.Fatal(err)
			}
			err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				_, err := tx.AdvanceAcceptedBase(context.Background(), WorkspaceAcceptedBaseTransition{
					Expected: before, ObservedRef: "refs/heads/next", ObservedCommitSHA: strings.Repeat("c", 40),
					ObservedTree: changedWorkspaceTree(t, binding, "Observed"), NextState: "pending",
				})
				return err
			})
			if err == nil {
				t.Fatal("triggered accepted-base transition succeeded")
			}
			after, readErr := repo.Workspace(context.Background(), binding.Scope)
			if readErr != nil || !equalWorkspaceRecords(after, before) {
				t.Fatalf("trigger failure state=(%+v,%v), want %+v", after, readErr, before)
			}
		})
	}
}

func TestWorkspaceMutationTxAdvanceAcceptedBaseRejectsTimestampRegression(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	if _, err := store.DB().Exec(`
		UPDATE workspace_bindings
		SET created_at='2000-01-01 00:00:00+00:00', updated_at='2099-01-01 00:00:00+00:00'
		WHERE project_id=? AND workspace_id=?
	`, binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	expected, err := repo.Workspace(context.Background(), binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	before := readAtomicWorkspaceRawSnapshot(t, store.DB())
	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.AdvanceAcceptedBase(context.Background(), WorkspaceAcceptedBaseTransition{
			Expected: expected, ObservedRef: "refs/heads/next", ObservedCommitSHA: strings.Repeat("c", 40),
			ObservedTree: changedWorkspaceTree(t, binding, "Observed"), NextState: "pending",
		})
		return err
	})
	if err == nil {
		t.Fatal("accepted-base transition moved updated_at backwards")
	}
	after := readAtomicWorkspaceRawSnapshot(t, store.DB())
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("timestamp regression changed raw state: got %#v want %#v", after, before)
	}
}

func TestWorkspaceMutationTxAdvanceAcceptedBaseIgnoredUpdateRollsBackRawStateAndReleasesWriter(t *testing.T) {
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
	expected, err := repo.Workspace(context.Background(), binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	before := readAtomicWorkspaceRawSnapshot(t, store.DB())
	if _, err := store.DB().Exec(`
		CREATE TRIGGER ignore_accepted_base_update
		BEFORE UPDATE OF accepted_commit ON workspace_bindings
		WHEN OLD.project_id='00000000-0000-4000-8000-000000000001'
		 AND OLD.workspace_id='00000000-0000-4000-8000-000000000011'
		BEGIN SELECT RAISE(IGNORE); END
	`); err != nil {
		t.Fatal(err)
	}
	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.AdvanceAcceptedBase(context.Background(), WorkspaceAcceptedBaseTransition{
			Expected: expected, ObservedRef: "refs/heads/next", ObservedCommitSHA: strings.Repeat("c", 40),
			ObservedTree: changedWorkspaceTree(t, binding, "Observed"), NextState: "pending",
		})
		if err == nil || !reflect.DeepEqual(got, WorkspaceRecord{}) {
			t.Fatalf("ignored update=(%+v,%v), want zero,error", got, err)
		}
		return err
	})
	if err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrCommitOutcomeUnknown) {
		t.Fatalf("ignored update error=%v, want ordinary mutation error", err)
	}
	if after := readAtomicWorkspaceRawSnapshot(t, store.DB()); !reflect.DeepEqual(after, before) {
		t.Fatalf("ignored update raw state changed immediately: got %#v want %#v", after, before)
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
		t.Fatalf("ignored update raw state changed after reopen: got %#v want %#v", after, before)
	}
	if _, err := restarted.DB().Exec(`DROP TRIGGER ignore_accepted_base_update`); err != nil {
		t.Fatal(err)
	}
	restartedRepo := NewWorkspaceRepo(restarted.DB())
	err = restartedRepo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		current, err := tx.Workspace(context.Background())
		if err != nil {
			return err
		}
		_, err = tx.AdvanceAcceptedBase(context.Background(), WorkspaceAcceptedBaseTransition{
			Expected: current, ObservedRef: "refs/heads/next", ObservedCommitSHA: strings.Repeat("c", 40),
			ObservedTree: changedWorkspaceTree(t, binding, "Observed"), NextState: "pending",
		})
		return err
	})
	if err != nil {
		t.Fatalf("next accepted-base transaction failed: %v", err)
	}
}

func TestWorkspaceMutationTxAdvanceAcceptedBaseSameStatementTimestampRawAtomicDelta(t *testing.T) {
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
		CREATE TABLE accepted_base_timestamp_probe(value TEXT NOT NULL);
		CREATE TRIGGER accepted_base_same_second
		BEFORE UPDATE OF accepted_commit ON workspace_bindings
		WHEN OLD.project_id='00000000-0000-4000-8000-000000000001'
		 AND OLD.workspace_id='00000000-0000-4000-8000-000000000011'
		BEGIN
			INSERT INTO accepted_base_timestamp_probe(value) VALUES (CURRENT_TIMESTAMP);
			UPDATE workspace_bindings SET updated_at=CURRENT_TIMESTAMP
			WHERE project_id=OLD.project_id AND workspace_id=OLD.workspace_id;
		END
	`); err != nil {
		t.Fatal(err)
	}
	before := readAtomicWorkspaceRawSnapshot(t, store.DB())
	observedTree := changedWorkspaceTree(t, binding, "Observed")
	observedSnapshot, err := state.DecodeTree(observedTree)
	if err != nil {
		t.Fatal(err)
	}
	observedBytes, err := encodeFileList(observedTree)
	if err != nil {
		t.Fatal(err)
	}
	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		expected, err := tx.Workspace(context.Background())
		if err != nil {
			return err
		}
		_, err = tx.AdvanceAcceptedBase(context.Background(), WorkspaceAcceptedBaseTransition{
			Expected: expected, ObservedRef: "refs/heads/next", ObservedCommitSHA: strings.Repeat("c", 40),
			ObservedTree: observedTree, NextState: "pending",
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	after := readAtomicWorkspaceRawSnapshot(t, store.DB())
	targetKeys := map[string]string{
		"project_id":   quoteSQLiteTextLiteral(binding.Scope.ProjectID),
		"workspace_id": quoteSQLiteTextLiteral(string(binding.Scope.WorkspaceID)),
	}
	assertAtomicWorkspaceRawDelta(t, before, after, "workspace_bindings", targetKeys, "accepted_ref", "accepted_commit",
		"accepted_digest", "accepted_snapshot", "status", "updated_at")
	target := findAtomicWorkspaceRawRow(t, after, "workspace_bindings", targetKeys)
	assertRawAtomicCell(t, target, "accepted_ref", quoteSQLiteTextLiteral("refs/heads/next"), "text")
	assertRawAtomicCell(t, target, "accepted_commit", quoteSQLiteTextLiteral(strings.Repeat("c", 40)), "text")
	assertRawAtomicCell(t, target, "accepted_digest", quoteSQLiteTextLiteral(string(observedSnapshot.Digest)), "text")
	assertRawAtomicCell(t, target, "accepted_snapshot", fmt.Sprintf("X'%X'", observedBytes), "blob")
	assertRawAtomicCell(t, target, "status", quoteSQLiteTextLiteral("pending"), "text")
	var probe, persisted string
	if err := store.DB().QueryRow(`SELECT value FROM accepted_base_timestamp_probe`).Scan(&probe); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT CAST(updated_at AS TEXT) FROM workspace_bindings WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID).Scan(&persisted); err != nil {
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
		t.Fatalf("accepted-base raw state changed after reopen: got %#v want %#v", reopened, after)
	}
}

func TestWorkspaceMutationTxAdvanceAcceptedBaseTimestampPredicate(t *testing.T) {
	previous := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name     string
		returned time.Time
		raw      string
		class    string
		want     bool
	}{
		{"equal is monotonic", previous, "2026-07-29 12:00:00+00:00", "text", true},
		{"later is monotonic", previous.Add(time.Second), "2026-07-29 12:00:01+00:00", "text", true},
		{"regression", previous.Add(-time.Nanosecond), "2026-07-29 11:59:59.999999999+00:00", "text", false},
		{"invalid storage class", previous, "2026-07-29 12:00:00+00:00", "integer", false},
		{"empty raw timestamp", previous, "", "text", false},
		{"zero timestamp", time.Time{}, "0001-01-01 00:00:00+00:00", "text", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := validMonotonicWorkspaceMutationTimestamp(test.returned, test.raw, test.class, previous); got != test.want {
				t.Fatalf("validMonotonicWorkspaceMutationTimestamp()=%v, want %v", got, test.want)
			}
		})
	}
}

func TestWorkspaceMutationTxAdvanceAcceptedBaseInvalidAPIHasNoMutation(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, *Store, *WorkspaceRepo, types.WorkspaceBinding, WorkspaceAcceptedBaseTransition) (*WorkspaceMutationTx, context.Context, WorkspaceAcceptedBaseTransition)
	}{
		{"nil transaction", func(_ *testing.T, _ *Store, _ *WorkspaceRepo, _ types.WorkspaceBinding, transition WorkspaceAcceptedBaseTransition) (*WorkspaceMutationTx, context.Context, WorkspaceAcceptedBaseTransition) {
			return nil, context.Background(), transition
		}},
		{"empty transaction", func(_ *testing.T, _ *Store, _ *WorkspaceRepo, _ types.WorkspaceBinding, transition WorkspaceAcceptedBaseTransition) (*WorkspaceMutationTx, context.Context, WorkspaceAcceptedBaseTransition) {
			return &WorkspaceMutationTx{}, context.Background(), transition
		}},
		{"invalid scope", func(t *testing.T, store *Store, _ *WorkspaceRepo, _ types.WorkspaceBinding, transition WorkspaceAcceptedBaseTransition) (*WorkspaceMutationTx, context.Context, WorkspaceAcceptedBaseTransition) {
			conn, err := store.DB().Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			return &WorkspaceMutationTx{conn: conn, scope: types.WorkspaceScope{}}, context.Background(), transition
		}},
		{"missing workspace", func(t *testing.T, store *Store, _ *WorkspaceRepo, _ types.WorkspaceBinding, _ WorkspaceAcceptedBaseTransition) (*WorkspaceMutationTx, context.Context, WorkspaceAcceptedBaseTransition) {
			missing := workspaceBinding("00000000-0000-4000-8000-000000000099", "00000000-0000-4000-8000-000000000098", "/missing", 9, 98)
			tree := workspaceTree(t, missing.Scope.ProjectID, missing.Repository)
			missing = bindingWithTreeDigest(t, missing, tree)
			snapshot, err := state.DecodeTree(tree)
			if err != nil {
				t.Fatal(err)
			}
			conn, err := store.DB().Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			return &WorkspaceMutationTx{conn: conn, scope: missing.Scope}, context.Background(), WorkspaceAcceptedBaseTransition{
				Expected:    WorkspaceRecord{Binding: missing, Snapshot: snapshot, State: "clean"},
				ObservedRef: "refs/heads/next", ObservedCommitSHA: strings.Repeat("c", 40),
				ObservedTree: changedWorkspaceTree(t, missing, "Observed"), NextState: "pending",
			}
		}},
		{"closed connection", func(t *testing.T, store *Store, _ *WorkspaceRepo, binding types.WorkspaceBinding, transition WorkspaceAcceptedBaseTransition) (*WorkspaceMutationTx, context.Context, WorkspaceAcceptedBaseTransition) {
			conn, err := store.DB().Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err := conn.Close(); err != nil {
				t.Fatal(err)
			}
			return &WorkspaceMutationTx{conn: conn, scope: binding.Scope}, context.Background(), transition
		}},
		{"canceled context", func(t *testing.T, store *Store, _ *WorkspaceRepo, binding types.WorkspaceBinding, transition WorkspaceAcceptedBaseTransition) (*WorkspaceMutationTx, context.Context, WorkspaceAcceptedBaseTransition) {
			conn, err := store.DB().Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return &WorkspaceMutationTx{conn: conn, scope: binding.Scope}, ctx, transition
		}},
		{"retained closed transaction", func(t *testing.T, _ *Store, repo *WorkspaceRepo, binding types.WorkspaceBinding, transition WorkspaceAcceptedBaseTransition) (*WorkspaceMutationTx, context.Context, WorkspaceAcceptedBaseTransition) {
			var retained *WorkspaceMutationTx
			if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				retained = tx
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			return retained, context.Background(), transition
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, repo := openWorkspaceStore(t)
			binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
			expected, err := repo.Workspace(context.Background(), binding.Scope)
			if err != nil {
				t.Fatal(err)
			}
			transition := WorkspaceAcceptedBaseTransition{
				Expected: expected, ObservedRef: "refs/heads/next", ObservedCommitSHA: strings.Repeat("c", 40),
				ObservedTree: changedWorkspaceTree(t, binding, "Observed"), NextState: "pending",
			}
			tx, ctx, transition := test.prepare(t, store, repo, binding, transition)
			before := readAtomicWorkspaceRawSnapshot(t, store.DB())
			got, err := tx.AdvanceAcceptedBase(ctx, transition)
			if err == nil || !reflect.DeepEqual(got, WorkspaceRecord{}) {
				t.Fatalf("invalid API=(%+v,%v), want zero,error", got, err)
			}
			if after := readAtomicWorkspaceRawSnapshot(t, store.DB()); !reflect.DeepEqual(after, before) {
				t.Fatalf("invalid API changed raw state: got %#v want %#v", after, before)
			}
		})
	}
}

func TestWorkspaceMutationTxAdvanceAcceptedBaseMetadataHelperErrors(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	conn, err := store.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	missing := &WorkspaceMutationTx{conn: conn, scope: types.WorkspaceScope{
		ProjectID: "00000000-0000-4000-8000-000000000099", WorkspaceID: "00000000-0000-4000-8000-000000000098",
	}}
	if _, err := missing.workspaceBindingMutationMetadata(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing metadata error=%v, want ErrNotFound", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	closed := &WorkspaceMutationTx{conn: conn, scope: binding.Scope}
	if _, err := closed.workspaceBindingMutationMetadata(context.Background()); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("closed metadata error=%v, want ordinary query error", err)
	}
	if _, err := store.DB().Exec(`UPDATE workspace_bindings SET updated_at=CAST(1 AS INTEGER)`); err != nil {
		t.Fatal(err)
	}
	conn, err = store.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	invalid := &WorkspaceMutationTx{conn: conn, scope: binding.Scope}
	if _, err := invalid.workspaceBindingMutationMetadata(context.Background()); err == nil {
		t.Fatal("invalid metadata storage succeeded")
	}
}

func TestWorkspaceMutationTxAdvanceAcceptedBaseRestartIsolationAndOwnership(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	a := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	b := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	beforeB, err := repo.Workspace(context.Background(), b.Scope)
	if err != nil {
		t.Fatal(err)
	}
	var got WorkspaceRecord
	err = repo.WithImmediateWorkspace(context.Background(), a.Scope, func(tx *WorkspaceMutationTx) error {
		expected, err := tx.Workspace(context.Background())
		if err != nil {
			return err
		}
		got, err = tx.AdvanceAcceptedBase(context.Background(), WorkspaceAcceptedBaseTransition{
			Expected: expected, ObservedRef: "", ObservedCommitSHA: expected.Binding.AcceptedCommitSHA,
			ObservedTree: changedWorkspaceTree(t, a, "Detached"), NextState: "clean",
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	wantDigest := got.Snapshot.Digest
	got.Snapshot.Project.Name = "caller mutation"
	got.Binding.AcceptedRef = "refs/heads/caller"
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restartedRepo := NewWorkspaceRepo(restarted.DB())
	afterA, err := restartedRepo.Workspace(context.Background(), a.Scope)
	if err != nil {
		t.Fatal(err)
	}
	afterB, err := restartedRepo.Workspace(context.Background(), b.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if afterA.Binding.AcceptedRef != "" || afterA.Binding.AcceptedCommitSHA != a.AcceptedCommitSHA || afterA.Snapshot.Digest != wantDigest || afterA.State != "clean" {
		t.Fatalf("restarted workspace A=%+v", afterA)
	}
	if !equalWorkspaceRecords(afterB, beforeB) {
		t.Fatalf("workspace B changed: got %+v want %+v", afterB, beforeB)
	}
}

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

func TestWorkspaceRepoHasOpenConflictsAbsentAndInvalidScopes(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	if got, err := repo.HasOpenConflicts(context.Background(), binding.Scope); err != nil || got {
		t.Fatalf("registered no-open gate=(%v,%v), want (false,nil)", got, err)
	}
	var nilRepo *WorkspaceRepo
	for _, test := range []struct {
		name  string
		repo  *WorkspaceRepo
		scope types.WorkspaceScope
	}{
		{name: "nil repo", repo: nilRepo, scope: binding.Scope},
		{name: "nil database", repo: &WorkspaceRepo{}, scope: binding.Scope},
		{name: "invalid project", repo: repo, scope: types.WorkspaceScope{ProjectID: "BAD", WorkspaceID: binding.Scope.WorkspaceID}},
		{name: "invalid workspace", repo: repo, scope: types.WorkspaceScope{ProjectID: binding.Scope.ProjectID, WorkspaceID: "BAD"}},
		{name: "unregistered scope", repo: repo, scope: types.WorkspaceScope{ProjectID: "00000000-0000-4000-8000-000000000099", WorkspaceID: "00000000-0000-4000-8000-000000000098"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, err := test.repo.HasOpenConflicts(context.Background(), test.scope); !errors.Is(err, ErrNotFound) || got {
				t.Fatalf("HasOpenConflicts invalid=(%v,%v), want (false,ErrNotFound)", got, err)
			}
		})
	}
}

func TestWorkspaceConflictGatesFailClosedOnCorruptOpenEvidence(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	key := state.RecordKey{Kind: "task", ID: "00000000-0000-4000-8000-000000000021"}
	insertWorkspaceConflict(t, store, binding.Scope, "corrupt-gate-evidence", key, "open")
	if _, err := store.DB().Exec(`
		UPDATE workspace_conflicts SET base_json=''
		WHERE project_id=? AND workspace_id=?
	`, binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.HasOpenConflicts(context.Background(), binding.Scope); err == nil || got {
		t.Fatalf("repo corrupt conflict gate=(%v,%v), want (false,error)", got, err)
	}
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		if got, err := tx.HasOpenConflicts(context.Background()); err == nil || got {
			t.Fatalf("tx corrupt conflict gate=(%v,%v), want (false,error)", got, err)
		}
		if got, err := tx.HasOpenConflictForKeys(context.Background(), []state.RecordKey{key}); err == nil || got {
			t.Fatalf("key corrupt conflict gate=(%v,%v), want (false,error)", got, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
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
			(project_id, workspace_id, occurrence_id, conflict_id, record_kind, record_id, field_path,
			 conflict_kind, base_json, ours_json, theirs_json, state)
			VALUES (?, ?, '00000000-0000-4000-8000-000000000031',
			 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			 'task', ?, '/title', 'same_field', '{}', '{}', '{}', 'open')
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
			(project_id, workspace_id, occurrence_id, conflict_id, record_kind, record_id, field_path,
			 conflict_kind, base_json, ours_json, theirs_json, state)
			VALUES (?, ?, '00000000-0000-4000-8000-000000000032',
			 'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
			 ?, ?, '', 'immutable_record', '{}', '{}', '{}', 'open')
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
			(project_id, workspace_id, occurrence_id, conflict_id, record_kind, record_id, field_path,
			 conflict_kind, base_json, ours_json, theirs_json, state)
			VALUES (?, ?, 'rolled-back-conflict', 'rolled-back-conflict', 'task', ?, '/title', 'same_field', '{}', '{}', '{}', 'open')
		`, binding.Scope.ProjectID, binding.Scope.WorkspaceID, "00000000-0000-4000-8000-000000000021")
		if err != nil {
			return err
		}
		return rollbackErr
	})
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("WithImmediateWorkspace error=%v, want rollback fixture", err)
	}
	if errors.Is(err, ErrCommitOutcomeUnknown) {
		t.Fatalf("callback error %v matched ErrCommitOutcomeUnknown", err)
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
			(project_id, workspace_id, occurrence_id, conflict_id, record_kind, record_id, field_path,
			 conflict_kind, base_json, ours_json, theirs_json, state)
			VALUES (?, ?, 'deferred-invalid-fk', 'deferred-invalid-fk', 'task', ?, '/title',
			 'same_field', '{}', '{}', '{}', 'open')
		`, "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000012",
			"00000000-0000-4000-8000-000000000021")
		return err
	})
	if err == nil {
		t.Fatal("WithImmediateWorkspace hid a deferred foreign-key COMMIT failure")
	}
	if !errors.Is(err, ErrCommitOutcomeUnknown) {
		t.Fatalf("COMMIT error=%v, want ErrCommitOutcomeUnknown", err)
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
			candidate.RebasedThroughGeneration != 3 || candidate.ImportedBy != workspaceCandidateImporter ||
			!candidate.ImportedAt.Equal(workspaceCandidateImportedAt) || candidate.ImportedAt.Location() != time.UTC {
			t.Fatalf("candidate=%+v", candidate)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceMutationTxCandidateRejectsInvalidProvenance(t *testing.T) {
	for _, test := range []struct {
		name   string
		column string
		value  any
	}{
		{name: "principal", column: "imported_by", value: "not-a-principal"},
		{name: "timestamp", column: "imported_at", value: ""},
		{name: "non UTC timestamp", column: "imported_at", value: time.Date(2026, 7, 28, 14, 0, 0, 0, time.FixedZone("offset", 3600))},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, repo := openWorkspaceStore(t)
			store.DB().SetMaxOpenConns(1)
			binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
			direct, directBytes := encodedWorkspaceSnapshot(t, binding.Scope.ProjectID, binding.Repository)
			insertWorkspaceCandidate(t, store, binding.Scope, state.Digest(binding.AcceptedTreeDigest), direct.Digest, directBytes, nil, 0)
			if _, err := store.DB().Exec("UPDATE workspace_candidates SET "+test.column+"=? WHERE project_id=? AND workspace_id=?", test.value, binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
				t.Fatal(err)
			}
			err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				_, err := tx.Candidate(context.Background())
				return err
			})
			if err == nil {
				t.Fatal("Candidate served invalid provenance")
			}
		})
	}
}

func TestWorkspaceMutationTxUpsertCandidateRoundTripAndRepeat(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	record := workspaceCandidateRecord(t, binding, true, 3)

	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		if err := tx.UpsertCandidate(context.Background(), record); err != nil {
			return err
		}
		return tx.UpsertCandidate(context.Background(), record)
	}); err != nil {
		t.Fatal(err)
	}

	var accepted, working, importedBy string
	var direct, rebased []byte
	var boundary int64
	var importedAt time.Time
	if err := store.DB().QueryRow(`
		SELECT accepted_base_digest, working_tree_digest, direct_tree, rebased_tree,
		       rebased_through_generation, imported_by, imported_at
		FROM workspace_candidates WHERE project_id=? AND workspace_id=?
	`, binding.Scope.ProjectID, binding.Scope.WorkspaceID).Scan(
		&accepted, &working, &direct, &rebased, &boundary, &importedBy, &importedAt,
	); err != nil {
		t.Fatal(err)
	}
	wantDirect, err := encodeFileList(mustEncodeWorkspaceSnapshot(t, record.DirectSnapshot))
	if err != nil {
		t.Fatal(err)
	}
	wantRebased, err := encodeFileList(mustEncodeWorkspaceSnapshot(t, *record.RebasedSnapshot))
	if err != nil {
		t.Fatal(err)
	}
	if accepted != binding.AcceptedTreeDigest || working != string(record.WorkingTreeDigest) ||
		!bytes.Equal(direct, wantDirect) || !bytes.Equal(rebased, wantRebased) || boundary != 3 ||
		importedBy != record.ImportedBy || !importedAt.Equal(record.ImportedAt) {
		t.Fatalf("persisted candidate accepted=%q working=%q boundary=%d imported_by=%q imported_at=%v", accepted, working, boundary, importedBy, importedAt)
	}

	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.Candidate(context.Background())
		if err != nil {
			return err
		}
		if got == nil || got.ImportedBy != record.ImportedBy || !got.ImportedAt.Equal(record.ImportedAt) ||
			got.DirectSnapshot.Digest != record.DirectSnapshot.Digest || got.RebasedSnapshot == nil ||
			got.RebasedSnapshot.Digest != record.RebasedSnapshot.Digest || got.RebasedThroughGeneration != record.RebasedThroughGeneration {
			t.Fatalf("candidate round trip=%+v", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceMutationTxUpsertCandidateClearsRebasedAndReplacesProvenance(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	first := workspaceCandidateRecord(t, binding, true, 3)
	second := workspaceCandidateRecord(t, binding, false, 0)
	second.ImportedBy = "00000000-0000-4000-8000-000000000072"
	second.ImportedAt = time.Date(2026, 7, 28, 15, 0, 0, 123456789, time.FixedZone("zero offset", 0))

	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.UpsertCandidate(context.Background(), first)
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.UpsertCandidate(context.Background(), second)
	}); err != nil {
		t.Fatal(err)
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
	var rebasedIsNull bool
	var boundary int64
	var importedBy string
	var importedAt time.Time
	if err := store.DB().QueryRow(`
		SELECT rebased_tree IS NULL, rebased_through_generation, imported_by, imported_at
		FROM workspace_candidates WHERE project_id=? AND workspace_id=?
	`, binding.Scope.ProjectID, binding.Scope.WorkspaceID).Scan(&rebasedIsNull, &boundary, &importedBy, &importedAt); err != nil {
		t.Fatal(err)
	}
	if !rebasedIsNull || boundary != 0 || importedBy != second.ImportedBy || !importedAt.Equal(second.ImportedAt) {
		t.Fatalf("replacement columns null=%v boundary=%d imported_by=%q imported_at=%v", rebasedIsNull, boundary, importedBy, importedAt)
	}

	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		candidate, err := tx.Candidate(context.Background())
		if err != nil {
			return err
		}
		if candidate == nil || candidate.RebasedSnapshot != nil || candidate.RebasedThroughGeneration != 0 ||
			candidate.ImportedBy != second.ImportedBy || !candidate.ImportedAt.Equal(second.ImportedAt) ||
			candidate.ImportedAt.Location() != time.UTC {
			t.Fatalf("replaced candidate=%+v", candidate)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceMutationTxUpsertCandidateRejectsInvalidRecord(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*WorkspaceCandidateRecord)
	}{
		{name: "accepted base", mutate: func(record *WorkspaceCandidateRecord) {
			record.AcceptedBaseDigest = state.Digest("sha256:" + strings.Repeat("a", 64))
		}},
		{name: "working digest", mutate: func(record *WorkspaceCandidateRecord) {
			record.WorkingTreeDigest = state.Digest("sha256:" + strings.Repeat("a", 64))
		}},
		{name: "stale direct snapshot", mutate: func(record *WorkspaceCandidateRecord) {
			record.DirectSnapshot.Digest = state.Digest("sha256:" + strings.Repeat("a", 64))
		}},
		{name: "cross project direct snapshot", mutate: func(record *WorkspaceCandidateRecord) {
			record.DirectSnapshot.Config.ProjectID = "00000000-0000-4000-8000-000000000002"
			record.DirectSnapshot.Project.ID = "00000000-0000-4000-8000-000000000002"
			record.DirectSnapshot.Digest = ""
		}},
		{name: "cross repository direct snapshot", mutate: func(record *WorkspaceCandidateRecord) {
			record.DirectSnapshot.Config.Repository = types.RepositoryIdentity{
				Provider: "github", ImmutableID: "R_other", CanonicalRemote: "https://github.com/acme/other",
			}
			tree, _ := state.EncodeTree(record.DirectSnapshot)
			record.DirectSnapshot.Digest, _ = state.DigestTree(tree)
			record.WorkingTreeDigest = record.DirectSnapshot.Digest
		}},
		{name: "stale rebased snapshot", mutate: func(record *WorkspaceCandidateRecord) {
			record.RebasedSnapshot.Digest = state.Digest("sha256:" + strings.Repeat("a", 64))
		}},
		{name: "boundary without rebased snapshot", mutate: func(record *WorkspaceCandidateRecord) {
			record.RebasedSnapshot = nil
			record.RebasedThroughGeneration = 1
		}},
		{name: "negative rebased boundary", mutate: func(record *WorkspaceCandidateRecord) { record.RebasedThroughGeneration = -1 }},
		{name: "principal", mutate: func(record *WorkspaceCandidateRecord) { record.ImportedBy = "invalid" }},
		{name: "zero timestamp", mutate: func(record *WorkspaceCandidateRecord) { record.ImportedAt = time.Time{} }},
		{name: "non UTC timestamp", mutate: func(record *WorkspaceCandidateRecord) {
			record.ImportedAt = record.ImportedAt.In(time.FixedZone("offset", 3600))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, repo := openWorkspaceStore(t)
			binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
			record := workspaceCandidateRecord(t, binding, true, 1)
			test.mutate(&record)
			err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				return tx.UpsertCandidate(context.Background(), record)
			})
			if err == nil {
				t.Fatal("UpsertCandidate accepted invalid record")
			}
			err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				candidate, err := tx.Candidate(context.Background())
				if err == nil && candidate != nil {
					t.Fatalf("invalid upsert persisted candidate %+v", candidate)
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWorkspaceMutationTxCandidateMutationCountsAndScope(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	a := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	b := createBinding(t, repo, a.Scope.ProjectID, "00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	c := createBinding(t, repo, "00000000-0000-4000-8000-000000000002", string(a.Scope.WorkspaceID), "/checkout-c", 3, 13)
	if err := repo.WithImmediateWorkspace(context.Background(), c.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.UpsertCandidate(context.Background(), workspaceCandidateRecord(t, c, false, 0))
	}); err != nil {
		t.Fatal(err)
	}

	if err := repo.WithImmediateWorkspace(context.Background(), a.Scope, func(tx *WorkspaceMutationTx) error {
		if err := tx.DeleteCandidate(context.Background(), false); err != nil {
			return err
		}
		return tx.UpsertCandidate(context.Background(), workspaceCandidateRecord(t, a, false, 0))
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.WithImmediateWorkspace(context.Background(), b.Scope, func(tx *WorkspaceMutationTx) error {
		candidate, err := tx.Candidate(context.Background())
		if err != nil {
			return err
		}
		if candidate != nil {
			t.Fatalf("workspace B observed workspace A candidate %+v", candidate)
		}
		return tx.DeleteCandidate(context.Background(), false)
	}); err != nil {
		t.Fatal(err)
	}

	err := repo.WithImmediateWorkspace(context.Background(), a.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.DeleteCandidate(context.Background(), false)
	})
	if err == nil {
		t.Fatal("DeleteCandidate expected absence despite a present row")
	}
	if err := repo.WithImmediateWorkspace(context.Background(), a.Scope, func(tx *WorkspaceMutationTx) error {
		candidate, err := tx.Candidate(context.Background())
		if err != nil || candidate == nil {
			t.Fatalf("count mismatch removed candidate: candidate=%+v err=%v", candidate, err)
		}
		return tx.DeleteCandidate(context.Background(), true)
	}); err != nil {
		t.Fatal(err)
	}
	err = repo.WithImmediateWorkspace(context.Background(), a.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.DeleteCandidate(context.Background(), true)
	})
	if err == nil {
		t.Fatal("DeleteCandidate expected a present row despite absence")
	}
	if err := repo.WithImmediateWorkspace(context.Background(), c.Scope, func(tx *WorkspaceMutationTx) error {
		candidate, err := tx.Candidate(context.Background())
		if err != nil {
			return err
		}
		if candidate == nil {
			t.Fatal("other project's same workspace ID candidate was deleted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceMutationTxUpsertCandidateRollsBackCallbackFailure(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	rollbackErr := errors.New("candidate rollback fixture")
	err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		if err := tx.UpsertCandidate(context.Background(), workspaceCandidateRecord(t, binding, false, 0)); err != nil {
			return err
		}
		return rollbackErr
	})
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("rollback error=%v, want fixture", err)
	}
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		candidate, err := tx.Candidate(context.Background())
		if err == nil && candidate != nil {
			t.Fatalf("rolled-back candidate persisted: %+v", candidate)
		}
		return err
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

func TestWorkspaceMutationTxOperationAuditReturnsNonNilEmpty(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)

	err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		operations, err := tx.OperationAudit(context.Background())
		if err != nil {
			return err
		}
		if operations == nil || len(operations) != 0 {
			t.Fatalf("OperationAudit()=%+v, want non-nil empty slice", operations)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceMutationTxOperationAuditReturnsAllStatesInStableOrder(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	stashID := "10000000-0000-4000-8000-000000000001"
	states := []string{"active", "rebased", "materialized", "stashed", "discarded", "stashed"}
	wantTimes := make([]time.Time, len(states))
	wantJSON := make([][]byte, len(states))
	for index, operationState := range states {
		operationID := fmt.Sprintf("00000000-0000-4000-8000-%012d", 91+index)
		var owner *string
		if index == 3 {
			owner = &stashID
		}
		wantJSON[index] = insertWorkspaceOperationOwned(t, store, binding.Scope, int64(index+1), validWorkspaceOperation(operationID), operationState, owner)
		wantTimes[index] = time.Date(2026, 7, 29, 10, index, 0, 0, time.UTC)
		if _, err := store.DB().Exec(`
			UPDATE workspace_overlay_operations SET created_at=?
			WHERE project_id=? AND workspace_id=? AND generation=?
		`, wantTimes[index], binding.Scope.ProjectID, binding.Scope.WorkspaceID, index+1); err != nil {
			t.Fatal(err)
		}
	}

	err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		records, err := tx.OperationAudit(context.Background())
		if err != nil {
			return err
		}
		if len(records) != len(states) {
			t.Fatalf("OperationAudit len=%d, want %d", len(records), len(states))
		}
		for index, record := range records {
			if record.Generation != int64(index+1) || record.State != states[index] ||
				!bytes.Equal(record.OperationJSON, wantJSON[index]) || !record.CreatedAt.Equal(wantTimes[index]) ||
				record.CreatedAt.Location() != time.UTC {
				t.Fatalf("OperationAudit[%d]=%+v", index, record)
			}
			if index == 3 {
				if record.StashedByStashID == nil || *record.StashedByStashID != stashID {
					t.Fatalf("owned stash=%+v", record)
				}
			} else if record.StashedByStashID != nil {
				t.Fatalf("unexpected stash owner at %d: %+v", index, record)
			}
		}
		records[0].OperationJSON[0] ^= 0xff
		*records[3].StashedByStashID = "20000000-0000-4000-8000-000000000002"
		again, err := tx.OperationAudit(context.Background())
		if err != nil {
			return err
		}
		if !bytes.Equal(again[0].OperationJSON, wantJSON[0]) || again[3].StashedByStashID == nil || *again[3].StashedByStashID != stashID {
			t.Fatal("OperationAudit aliased operation bytes or stash owner")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceMutationTxOperationAuditIsExactScopedAcrossRestart(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	a := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	b := createBinding(t, repo, a.Scope.ProjectID, "00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	c := createBinding(t, repo, "00000000-0000-4000-8000-000000000002", string(a.Scope.WorkspaceID), "/checkout-c", 3, 13)
	want := insertWorkspaceOperation(t, store, a.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "active")
	insertWorkspaceOperation(t, store, b.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000092"), "rebased")
	insertWorkspaceOperation(t, store, c.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000093"), "discarded")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo = NewWorkspaceRepo(store.DB())

	err = repo.WithImmediateWorkspace(context.Background(), a.Scope, func(tx *WorkspaceMutationTx) error {
		records, err := tx.OperationAudit(context.Background())
		if err != nil {
			return err
		}
		if len(records) != 1 || records[0].Generation != 1 || !bytes.Equal(records[0].OperationJSON, want) {
			t.Fatalf("OperationAudit()=%+v, want only workspace A", records)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceMutationTxOperationAuditRejectsUnavailableOrMissingScope(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	ctx := context.Background()

	var nilTx *WorkspaceMutationTx
	if _, err := nilTx.OperationAudit(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nil OperationAudit error=%v, want ErrNotFound", err)
	}
	if _, err := (&WorkspaceMutationTx{}).OperationAudit(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("invalid OperationAudit error=%v, want ErrNotFound", err)
	}
	conn, err := store.DB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	missing := types.WorkspaceScope{ProjectID: binding.Scope.ProjectID, WorkspaceID: "00000000-0000-4000-8000-000000000099"}
	if _, err := (&WorkspaceMutationTx{conn: conn, scope: missing}).OperationAudit(ctx); !errors.Is(err, ErrNotFound) {
		conn.Close()
		t.Fatalf("missing OperationAudit error=%v, want ErrNotFound", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := (&WorkspaceMutationTx{conn: conn, scope: binding.Scope}).OperationAudit(ctx); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("closed-connection OperationAudit error=%v, want query error", err)
	}
}

func TestWorkspaceMutationTxOperationAuditRejectsLogicalCorruption(t *testing.T) {
	canonical := validWorkspaceOperation("00000000-0000-4000-8000-000000000091")
	canonicalJSON, err := state.CanonicalOperation(canonical)
	if err != nil {
		t.Fatal(err)
	}
	owner := "10000000-0000-4000-8000-000000000001"
	for _, test := range []struct {
		name      string
		wantError string
		corrupt   func(*testing.T, *Store, types.WorkspaceBinding)
	}{
		{name: "nonpositive generation", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding) {
			insertWorkspaceOperationRaw(t, store, binding.Scope, 0, canonical.ID, canonicalJSON, "active")
		}},
		{name: "malformed operation", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding) {
			insertWorkspaceOperationRaw(t, store, binding.Scope, 1, canonical.ID, []byte("{"), "active")
		}},
		{name: "row ID mismatch", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding) {
			insertWorkspaceOperationRaw(t, store, binding.Scope, 1, "00000000-0000-4000-8000-000000000092", canonicalJSON, "active")
		}},
		{name: "noncanonical operation", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding) {
			insertWorkspaceOperationRaw(t, store, binding.Scope, 1, canonical.ID, bytes.TrimSuffix(bytes.Clone(canonicalJSON), []byte("\n")), "active")
		}},
		{name: "invalid state", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding) {
			insertWorkspaceOperationRaw(t, store, binding.Scope, 1, canonical.ID, canonicalJSON, "poison")
		}},
		{name: "owner on active", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding) {
			insertWorkspaceOperationRawOwned(t, store, binding.Scope, 1, canonical.ID, canonicalJSON, "active", &owner)
		}},
		{name: "invalid stash owner", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding) {
			invalidOwner := "10000000-0000-1000-8000-000000000001"
			insertWorkspaceOperationRawOwned(t, store, binding.Scope, 1, canonical.ID, canonicalJSON, "stashed", &invalidOwner)
		}},
		{name: "zero timestamp", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding) {
			insertWorkspaceOperationRaw(t, store, binding.Scope, 1, canonical.ID, canonicalJSON, "active")
			if _, err := store.DB().Exec(`UPDATE workspace_overlay_operations SET created_at=?`, time.Time{}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "offset timestamp", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding) {
			insertWorkspaceOperationRaw(t, store, binding.Scope, 1, canonical.ID, canonicalJSON, "active")
			if _, err := store.DB().Exec(`UPDATE workspace_overlay_operations SET created_at=?`, time.Date(2026, 7, 29, 12, 0, 0, 0, time.FixedZone("offset", 3600))); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "duplicate generation", wantError: "generations are not strictly increasing", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding) {
			recreateWorkspaceOperationsWithoutKeys(t, store)
			insertWorkspaceOperationRaw(t, store, binding.Scope, 1, canonical.ID, canonicalJSON, "active")
			second := validWorkspaceOperation("00000000-0000-4000-8000-000000000092")
			secondJSON, err := state.CanonicalOperation(second)
			if err != nil {
				t.Fatal(err)
			}
			insertWorkspaceOperationRaw(t, store, binding.Scope, 1, second.ID, secondJSON, "rebased")
			if _, err := store.DB().Exec(`UPDATE workspace_overlay_operations SET created_at=?`, time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "duplicate operation ID", wantError: "duplicate operation ID", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding) {
			recreateWorkspaceOperationsWithoutKeys(t, store)
			insertWorkspaceOperationRaw(t, store, binding.Scope, 1, canonical.ID, canonicalJSON, "active")
			insertWorkspaceOperationRaw(t, store, binding.Scope, 2, canonical.ID, canonicalJSON, "rebased")
			if _, err := store.DB().Exec(`UPDATE workspace_overlay_operations SET created_at=?`, time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, repo := openWorkspaceStore(t)
			store.DB().SetMaxOpenConns(1)
			binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
			if _, err := store.DB().Exec(`PRAGMA foreign_keys=OFF`); err != nil {
				t.Fatal(err)
			}
			if _, err := store.DB().Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
				t.Fatal(err)
			}
			test.corrupt(t, store, binding)
			err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				_, err := tx.OperationAudit(context.Background())
				return err
			})
			if err == nil {
				t.Fatal("OperationAudit served logically corrupt operation history")
			}
			if test.wantError != "" && !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("OperationAudit error=%v, want %q", err, test.wantError)
			}
		})
	}
}

func TestWorkspaceMutationTxOperationAuditRejectsSelectedStorageClassCorruption(t *testing.T) {
	for _, test := range []struct {
		name    string
		owned   bool
		corrupt func(*testing.T, *Store, types.WorkspaceBinding)
	}{
		{name: "project BLOB", corrupt: updateOperationAuditColumn("project_id", "CAST(project_id AS BLOB)")},
		{name: "workspace BLOB", corrupt: updateOperationAuditColumn("workspace_id", "CAST(workspace_id AS BLOB)")},
		{name: "generation BLOB", corrupt: updateOperationAuditColumn("generation", "CAST(generation AS BLOB)")},
		{name: "operation ID BLOB", corrupt: updateOperationAuditColumn("operation_id", "CAST(operation_id AS BLOB)")},
		{name: "operation JSON BLOB", corrupt: updateOperationAuditColumn("operation_json", "CAST(operation_json AS BLOB)")},
		{name: "state BLOB", corrupt: updateOperationAuditColumn("state", "CAST(state AS BLOB)")},
		{name: "stash owner BLOB", owned: true, corrupt: updateOperationAuditColumn("stashed_by_stash_id", "CAST(stashed_by_stash_id AS BLOB)")},
		{name: "created at BLOB", corrupt: updateOperationAuditColumn("created_at", "CAST(created_at AS BLOB)")},
		{name: "ambiguous textual cast scope", corrupt: func(t *testing.T, store *Store, binding types.WorkspaceBinding) {
			operation := validWorkspaceOperation("00000000-0000-4000-8000-000000000092")
			operationJSON, err := state.CanonicalOperation(operation)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.DB().Exec(`
				INSERT INTO workspace_overlay_operations
				(project_id,workspace_id,generation,operation_id,operation_json,state,stashed_by_stash_id,created_at)
				VALUES (CAST(? AS BLOB),?,2,?,?,'rebased',NULL,?)
			`, binding.Scope.ProjectID, binding.Scope.WorkspaceID, operation.ID, string(operationJSON), time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, repo := openWorkspaceStore(t)
			store.DB().SetMaxOpenConns(1)
			binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
			if _, err := store.DB().Exec(`PRAGMA foreign_keys=OFF`); err != nil {
				t.Fatal(err)
			}
			if _, err := store.DB().Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
				t.Fatal(err)
			}
			var owner *string
			operationState := "active"
			if test.owned {
				operationState = "stashed"
				owner = stringPointer("10000000-0000-4000-8000-000000000001")
			}
			insertWorkspaceOperationOwned(t, store, binding.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), operationState, owner)
			test.corrupt(t, store, binding)
			err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				_, err := tx.OperationAudit(context.Background())
				return err
			})
			if err == nil {
				t.Fatal("OperationAudit served selected storage-class corruption")
			}
		})
	}
}

func TestWorkspaceMutationTxActiveOperationsAfterToleratesDiscardedHistory(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	insertWorkspaceOperation(t, store, binding.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "discarded")
	want := insertWorkspaceOperation(t, store, binding.Scope, 2, validWorkspaceOperation("00000000-0000-4000-8000-000000000092"), "active")

	err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.ActiveOperationsAfter(context.Background(), 0)
		if err != nil {
			return err
		}
		if len(got) != 1 || got[0].Generation != 2 || !bytes.Equal(got[0].OperationJSON, want) {
			t.Fatalf("active operations=%+v, want only generation 2", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceMutationTxActiveOperationsAfterIgnoresCorruptTerminalPayload(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	insertWorkspaceOperationRaw(t, store, binding.Scope, 1, "00000000-0000-4000-8000-000000000091", []byte("{"), "discarded")
	want := insertWorkspaceOperation(t, store, binding.Scope, 2, validWorkspaceOperation("00000000-0000-4000-8000-000000000092"), "active")

	err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.ActiveOperationsAfter(context.Background(), 0)
		if err != nil {
			return err
		}
		if len(got) != 1 || got[0].Generation != 2 || !bytes.Equal(got[0].OperationJSON, want) {
			t.Fatalf("active operations=%+v, want only generation 2", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceMutationTxActiveOperationsAfterRejectsInvalidHistoricalOwnerMetadata(t *testing.T) {
	for _, test := range []struct {
		name, state, owner string
	}{
		{name: "owner on discarded", state: "discarded", owner: "10000000-0000-4000-8000-000000000001"},
		{name: "invalid stashed owner", state: "stashed", owner: "10000000-0000-1000-8000-000000000001"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, repo := openWorkspaceStore(t)
			store.DB().SetMaxOpenConns(1)
			binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
			if _, err := store.DB().Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
				t.Fatal(err)
			}
			insertWorkspaceOperationRawOwned(t, store, binding.Scope, 1, "00000000-0000-4000-8000-000000000091", []byte("{"), test.state, &test.owner)
			insertWorkspaceOperation(t, store, binding.Scope, 2, validWorkspaceOperation("00000000-0000-4000-8000-000000000092"), "active")
			err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				_, err := tx.ActiveOperationsAfter(context.Background(), 0)
				return err
			})
			if err == nil {
				t.Fatal("ActiveOperationsAfter ignored invalid historical owner metadata")
			}
		})
	}
}

func TestWorkspaceMutationTxOperationReadersAreStrictScopedAndNonAliasing(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	a := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	b := createBinding(t, repo, a.Scope.ProjectID, "00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	stashID := "10000000-0000-4000-8000-000000000001"
	otherStashID := "20000000-0000-4000-8000-000000000002"
	bytes1 := insertWorkspaceOperation(t, store, a.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "rebased")
	insertWorkspaceOperationOwned(t, store, a.Scope, 2, validWorkspaceOperation("00000000-0000-4000-8000-000000000092"), "stashed", &stashID)
	insertWorkspaceOperation(t, store, a.Scope, 3, validWorkspaceOperation("00000000-0000-4000-8000-000000000093"), "discarded")
	insertWorkspaceOperationOwned(t, store, a.Scope, 4, validWorkspaceOperation("00000000-0000-4000-8000-000000000094"), "stashed", &otherStashID)
	insertWorkspaceOperation(t, store, b.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000095"), "rebased")

	err := repo.WithImmediateWorkspace(context.Background(), a.Scope, func(tx *WorkspaceMutationTx) error {
		rebased, err := tx.RebasedOperationsAtOrBefore(context.Background(), 2)
		if err != nil {
			return err
		}
		if len(rebased) != 1 || rebased[0].Generation != 1 || rebased[0].StashedByStashID != nil || !bytes.Equal(rebased[0].OperationJSON, bytes1) {
			t.Fatalf("rebased operations=%+v", rebased)
		}
		stashed, err := tx.StashedOperationsByStashID(context.Background(), stashID)
		if err != nil {
			return err
		}
		if len(stashed) != 1 || stashed[0].Generation != 2 || stashed[0].StashedByStashID == nil || *stashed[0].StashedByStashID != stashID {
			t.Fatalf("stashed operations=%+v", stashed)
		}
		*stashed[0].StashedByStashID = otherStashID
		stashedAgain, err := tx.StashedOperationsByStashID(context.Background(), stashID)
		if err != nil {
			return err
		}
		if len(stashedAgain) != 1 || stashedAgain[0].StashedByStashID == nil || *stashedAgain[0].StashedByStashID != stashID {
			t.Fatal("operation reader aliased the returned stash-owner pointer")
		}
		exact, err := tx.OperationsByGenerations(context.Background(), []int64{1, 3, 4})
		if err != nil {
			return err
		}
		if len(exact) != 3 || exact[0].Generation != 1 || exact[1].Generation != 3 || exact[2].Generation != 4 {
			t.Fatalf("exact operations=%+v", exact)
		}
		rebased[0].OperationJSON[0] ^= 0xff
		again, err := tx.RebasedOperationsAtOrBefore(context.Background(), 1)
		if err != nil {
			return err
		}
		if !bytes.Equal(again[0].OperationJSON, bytes1) {
			t.Fatal("operation reader aliased persisted or prior result bytes")
		}
		for name, empty := range map[string][]WorkspaceOperation{
			"rebased": mustReadRebasedOperations(t, tx, 0),
			"stashed": mustReadStashedOperations(t, tx, "30000000-0000-4000-8000-000000000003"),
			"exact":   mustReadExactOperations(t, tx, []int64{}),
		} {
			if empty == nil || len(empty) != 0 {
				t.Fatalf("%s empty=%#v, want non-nil empty", name, empty)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceMutationTxOperationReadersTolerateUnrelatedLegacyOwnerlessStash(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	insertWorkspaceOperation(t, store, binding.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "stashed")
	insertWorkspaceOperation(t, store, binding.Scope, 2, validWorkspaceOperation("00000000-0000-4000-8000-000000000092"), "active")
	stashID := "10000000-0000-4000-8000-000000000001"

	err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		active, err := tx.ActiveOperationsAfter(context.Background(), 0)
		if err != nil || len(active) != 1 || active[0].Generation != 2 {
			t.Fatalf("active=%+v err=%v", active, err)
		}
		// Generic audit reads retain migrated history. A later stash replay/member
		// validator must reject this row if an envelope attempts to claim it.
		legacy, err := tx.OperationsByGenerations(context.Background(), []int64{1})
		if err != nil || len(legacy) != 1 || legacy[0].State != "stashed" || legacy[0].StashedByStashID != nil {
			t.Fatalf("legacy=%+v err=%v", legacy, err)
		}
		owned, err := tx.StashedOperationsByStashID(context.Background(), stashID)
		if err != nil || owned == nil || len(owned) != 0 {
			t.Fatalf("owned=%#v err=%v", owned, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceMutationTxOperationReadersRejectInvalidInputAndMembership(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		for _, generation := range []int64{-1} {
			if _, err := tx.RebasedOperationsAtOrBefore(context.Background(), generation); err == nil {
				t.Fatalf("RebasedOperationsAtOrBefore accepted %d", generation)
			}
		}
		for _, stashID := range []string{"", "00000000-0000-0000-0000-000000000000", "10000000-0000-1000-8000-000000000001", "10000000-0000-4000-7000-000000000001"} {
			if _, err := tx.StashedOperationsByStashID(context.Background(), stashID); err == nil {
				t.Fatalf("StashedOperationsByStashID accepted %q", stashID)
			}
		}
		for _, generations := range [][]int64{{0}, {-1}, {2, 1}, {1, 1}, {1}} {
			if _, err := tx.OperationsByGenerations(context.Background(), generations); err == nil {
				t.Fatalf("OperationsByGenerations accepted missing/invalid %+v", generations)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceMutationTxOperationReadersRejectCorruptSelectedRows(t *testing.T) {
	canonicalOperation := validWorkspaceOperation("00000000-0000-4000-8000-000000000091")
	canonicalBytes, err := state.CanonicalOperation(canonicalOperation)
	if err != nil {
		t.Fatal(err)
	}
	owner := "10000000-0000-4000-8000-000000000001"
	for _, test := range []struct {
		name, operationID, rowState string
		operation                   []byte
		owner                       *string
	}{
		{name: "malformed JSON", operationID: canonicalOperation.ID, operation: []byte("{"), rowState: "rebased"},
		{name: "row ID mismatch", operationID: "00000000-0000-4000-8000-000000000092", operation: canonicalBytes, rowState: "rebased"},
		{name: "invalid row ID", operationID: "BAD", operation: canonicalBytes, rowState: "rebased"},
		{name: "noncanonical JSON", operationID: canonicalOperation.ID, operation: bytes.TrimSuffix(bytes.Clone(canonicalBytes), []byte("\n")), rowState: "rebased"},
		{name: "invalid state", operationID: canonicalOperation.ID, operation: canonicalBytes, rowState: "poison"},
		{name: "owner on non-stashed", operationID: canonicalOperation.ID, operation: canonicalBytes, rowState: "rebased", owner: &owner},
		{name: "invalid stash owner", operationID: canonicalOperation.ID, operation: canonicalBytes, rowState: "stashed", owner: stringPointer("10000000-0000-1000-8000-000000000001")},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, repo := openWorkspaceStore(t)
			store.DB().SetMaxOpenConns(1)
			binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
			if test.rowState == "poison" || (test.owner != nil && test.rowState != "stashed") {
				if _, err := store.DB().Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
					t.Fatal(err)
				}
			}
			insertWorkspaceOperationRawOwned(t, store, binding.Scope, 1, test.operationID, test.operation, test.rowState, test.owner)
			err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				_, err := tx.OperationsByGenerations(context.Background(), []int64{1})
				return err
			})
			if err == nil {
				t.Fatal("OperationsByGenerations served a corrupt row")
			}
		})
	}
}

func TestWorkspaceMutationTxTransitionOperationsMatrix(t *testing.T) {
	states := []string{"active", "rebased", "stashed", "materialized", "discarded"}
	allowed := map[string]map[string]bool{
		"active":       {"rebased": true, "stashed": true, "materialized": true, "discarded": true},
		"rebased":      {"stashed": true, "materialized": true, "discarded": true},
		"materialized": {"active": true, "rebased": true},
	}
	for _, source := range states {
		for _, target := range states {
			t.Run(source+"_to_"+target, func(t *testing.T) {
				store, repo := openWorkspaceStore(t)
				binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
				owner := "10000000-0000-4000-8000-000000000001"
				var sourceOwner *string
				if source == "stashed" {
					sourceOwner = &owner
				}
				insertWorkspaceOperationOwned(t, store, binding.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), source, sourceOwner)
				before := readWorkspaceOperations(t, store, binding.Scope)
				var targetOwner *string
				if target == "stashed" {
					targetOwner = &owner
				}
				err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
					return tx.TransitionOperations(context.Background(), before, target, targetOwner)
				})
				wantAllowed := allowed[source][target]
				if wantAllowed && err != nil {
					t.Fatalf("allowed transition failed: %v", err)
				}
				if !wantAllowed && err == nil {
					t.Fatal("illegal transition succeeded")
				}
				after := readWorkspaceOperations(t, store, binding.Scope)
				wantState := source
				wantOwner := sourceOwner
				if wantAllowed {
					wantState, wantOwner = target, targetOwner
				}
				if len(after) != 1 || after[0].State != wantState || !equalOptionalString(after[0].StashedByStashID, wantOwner) {
					t.Fatalf("after=%+v, want state=%s owner=%v", after, wantState, wantOwner)
				}
			})
		}
	}
}

func TestWorkspaceMutationTxTransitionOperationsValidatesBatchAndOwner(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	insertWorkspaceOperation(t, store, binding.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "active")
	insertWorkspaceOperation(t, store, binding.Scope, 2, validWorkspaceOperation("00000000-0000-4000-8000-000000000092"), "active")
	rows := readWorkspaceOperations(t, store, binding.Scope)
	err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		if err := tx.TransitionOperations(context.Background(), nil, "not-a-state", stringPointer("bad")); err != nil {
			t.Fatalf("empty transition was not a no-op: %v", err)
		}
		for _, test := range []struct {
			name, target string
			rows         []WorkspaceOperation
			owner        *string
		}{
			{name: "unsorted", target: "rebased", rows: []WorkspaceOperation{rows[1], rows[0]}},
			{name: "duplicate generation", target: "rebased", rows: []WorkspaceOperation{rows[0], rows[0]}},
			{name: "duplicate ID", target: "rebased", rows: []WorkspaceOperation{rows[0], withOperationIdentity(rows[1], rows[0].OperationID, rows[0].OperationJSON)}},
			{name: "stash missing owner", target: "stashed", rows: rows},
			{name: "stash invalid owner", target: "stashed", rows: rows, owner: stringPointer("10000000-0000-1000-8000-000000000001")},
			{name: "stash invalid variant", target: "stashed", rows: rows, owner: stringPointer("10000000-0000-4000-7000-000000000001")},
			{name: "nonstash owner", target: "rebased", rows: rows, owner: stringPointer("10000000-0000-4000-8000-000000000001")},
			{name: "invalid target", target: "invalid", rows: rows},
		} {
			if err := tx.TransitionOperations(context.Background(), test.rows, test.target, test.owner); err == nil {
				t.Fatalf("%s transition succeeded", test.name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	after := readWorkspaceOperations(t, store, binding.Scope)
	if after[0].State != "active" || after[1].State != "active" {
		t.Fatalf("invalid transition mutated rows: %+v", after)
	}
}

func TestWorkspaceMutationTxTransitionOperationsCASAndRollback(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, *Store, types.WorkspaceScope, *WorkspaceOperation)
	}{
		{name: "stale state", prepare: func(_ *testing.T, _ *Store, _ types.WorkspaceScope, row *WorkspaceOperation) { row.State = "rebased" }},
		{name: "stale owner", prepare: func(t *testing.T, store *Store, scope types.WorkspaceScope, _ *WorkspaceOperation) {
			if _, err := store.DB().Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
				t.Fatal(err)
			}
			if _, err := store.DB().Exec(`UPDATE workspace_overlay_operations SET stashed_by_stash_id=? WHERE project_id=? AND workspace_id=? AND generation=1`, "10000000-0000-4000-8000-000000000001", scope.ProjectID, scope.WorkspaceID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "stale bytes", prepare: func(t *testing.T, _ *Store, _ types.WorkspaceScope, row *WorkspaceOperation) {
			operation := validWorkspaceOperation(row.OperationID)
			operation.ExpectedViewDigest = state.Digest("sha256:" + strings.Repeat("b", 64))
			canonical, err := state.CanonicalOperation(operation)
			if err != nil {
				t.Fatal(err)
			}
			row.OperationJSON = canonical
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, repo := openWorkspaceStore(t)
			binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
			insertWorkspaceOperation(t, store, binding.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "active")
			row := readWorkspaceOperations(t, store, binding.Scope)[0]
			test.prepare(t, store, binding.Scope, &row)
			if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				return tx.TransitionOperations(context.Background(), []WorkspaceOperation{row}, "discarded", nil)
			}); err == nil {
				t.Fatal("stale CAS input succeeded")
			}
			if got := readWorkspaceOperations(t, store, binding.Scope); got[0].State != "active" {
				t.Fatalf("stale CAS mutated row: %+v", got)
			}
		})
	}

	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	insertWorkspaceOperation(t, store, binding.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "active")
	insertWorkspaceOperation(t, store, binding.Scope, 2, validWorkspaceOperation("00000000-0000-4000-8000-000000000092"), "active")
	rows := readWorkspaceOperations(t, store, binding.Scope)
	if _, err := store.DB().Exec(`UPDATE workspace_overlay_operations SET state='rebased' WHERE project_id=? AND workspace_id=? AND generation=2`, binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.TransitionOperations(context.Background(), rows, "discarded", nil)
	}); err == nil {
		t.Fatal("second-row CAS mismatch succeeded")
	}
	after := readWorkspaceOperations(t, store, binding.Scope)
	if after[0].State != "active" || after[1].State != "rebased" {
		t.Fatalf("failed second-row CAS did not roll back first: %+v", after)
	}
}

func TestWorkspaceMutationTxTransitionOperationsExactScopeAndRecoverOldSplit(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	a := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	b := createBinding(t, repo, a.Scope.ProjectID, "00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	c := createBinding(t, repo, "00000000-0000-4000-8000-000000000002", string(a.Scope.WorkspaceID), "/checkout-c", 3, 13)
	for _, binding := range []types.WorkspaceBinding{a, b, c} {
		insertWorkspaceOperation(t, store, binding.Scope, 1, validWorkspaceOperation("00000000-0000-4000-8000-000000000091"), "materialized")
		insertWorkspaceOperation(t, store, binding.Scope, 2, validWorkspaceOperation("00000000-0000-4000-8000-000000000092"), "materialized")
	}
	err := repo.WithImmediateWorkspace(context.Background(), a.Scope, func(tx *WorkspaceMutationTx) error {
		rows, err := tx.OperationsByGenerations(context.Background(), []int64{1, 2})
		if err != nil {
			return err
		}
		if err := tx.TransitionOperations(context.Background(), rows[:1], "active", nil); err != nil {
			return err
		}
		return tx.TransitionOperations(context.Background(), rows[1:], "rebased", nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	gotA, gotB, gotC := readWorkspaceOperations(t, store, a.Scope), readWorkspaceOperations(t, store, b.Scope), readWorkspaceOperations(t, store, c.Scope)
	if gotA[0].State != "active" || gotA[1].State != "rebased" {
		t.Fatalf("workspace A recover-old=%+v", gotA)
	}
	if gotB[0].State != "materialized" || gotB[1].State != "materialized" {
		t.Fatalf("workspace B was mutated=%+v", gotB)
	}
	if gotC[0].State != "materialized" || gotC[1].State != "materialized" {
		t.Fatalf("project C with same workspace ID was mutated=%+v", gotC)
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

func TestWorkspaceMutationTxSetStatusReturningUpdatedAtAPI(t *testing.T) {
	var tx *WorkspaceMutationTx
	got, err := tx.SetStatusReturningUpdatedAt(context.Background(), "pending")
	if err == nil || !got.IsZero() {
		t.Fatalf("SetStatusReturningUpdatedAt nil tx=(%v,%v), want zero,error", got, err)
	}
}

func TestWorkspaceMutationTxSetStatusReturningUpdatedAtMatchesStrictStateAndScope(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	a := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	b := createBinding(t, repo, a.Scope.ProjectID, "00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	c := createBinding(t, repo, "00000000-0000-4000-8000-000000000002", string(a.Scope.WorkspaceID), "/checkout-c", 3, 13)
	stashID := "00000000-0000-4000-8000-000000000031"
	stash := validWorkspaceStash(t, a, stashID)
	if err := repo.WithImmediateWorkspace(context.Background(), a.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.InsertStash(context.Background(), stash)
	}); err != nil {
		t.Fatal(err)
	}

	var first, second time.Time
	if err := repo.WithImmediateWorkspace(context.Background(), a.Scope, func(tx *WorkspaceMutationTx) error {
		var err error
		first, err = tx.SetStatusReturningUpdatedAt(context.Background(), "pending")
		if err != nil {
			return err
		}
		strict, err := tx.RestoreRetryState(context.Background(), stashID)
		if err != nil {
			return err
		}
		if strict.Workspace.State != "pending" || !strict.BindingUpdatedAt.Equal(first) || first.Location() != time.UTC {
			t.Fatalf("strict state=%q updated_at=%v, returned %v in %v", strict.Workspace.State, strict.BindingUpdatedAt, first, first.Location())
		}
		second, err = tx.SetStatusReturningUpdatedAt(context.Background(), "conflicted")
		if err != nil {
			return err
		}
		if second.Before(first) {
			t.Fatalf("same-second update moved backwards: first=%v second=%v", first, second)
		}
		return tx.SetStatus(context.Background(), "blocked")
	}); err != nil {
		t.Fatal(err)
	}
	strict := mustRestoreRetryState(t, repo, a.Scope, stashID)
	if strict.Workspace.State != "blocked" || strict.BindingUpdatedAt.Before(second) {
		t.Fatalf("wrapper strict state=%q updated_at=%v, prior returned=%v", strict.Workspace.State, strict.BindingUpdatedAt, second)
	}
	for _, neighbor := range []types.WorkspaceBinding{b, c} {
		workspace, err := repo.Workspace(context.Background(), neighbor.Scope)
		if err != nil {
			t.Fatal(err)
		}
		if workspace.State != "clean" {
			t.Fatalf("neighbor %+v state=%q, want clean", neighbor.Scope, workspace.State)
		}
	}
}

func TestWorkspaceMutationTxSetStatusReturningUpdatedAtRejectsInvalidInputs(t *testing.T) {
	var nilTx *WorkspaceMutationTx
	if got, err := nilTx.SetStatusReturningUpdatedAt(context.Background(), "pending"); !got.IsZero() || !errors.Is(err, ErrNotFound) {
		t.Fatalf("nil tx=(%v,%v), want zero,ErrNotFound", got, err)
	}
	_, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	var closed *WorkspaceMutationTx
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		closed = tx
		invalidScope := &WorkspaceMutationTx{conn: tx.conn}
		if got, err := invalidScope.SetStatusReturningUpdatedAt(context.Background(), "pending"); !got.IsZero() || !errors.Is(err, ErrNotFound) {
			t.Fatalf("invalid scope=(%v,%v), want zero,ErrNotFound", got, err)
		}
		missing := &WorkspaceMutationTx{conn: tx.conn, scope: types.WorkspaceScope{
			ProjectID: "00000000-0000-4000-8000-000000000099", WorkspaceID: "00000000-0000-4000-8000-000000000098",
		}}
		if got, err := missing.SetStatusReturningUpdatedAt(context.Background(), "pending"); !got.IsZero() || !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing binding=(%v,%v), want zero,ErrNotFound", got, err)
		}
		if got, err := tx.SetStatusReturningUpdatedAt(context.Background(), "unknown"); !got.IsZero() || err == nil || errors.Is(err, ErrNotFound) {
			t.Fatalf("invalid status=(%v,%v), want zero,validation error", got, err)
		}
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		if got, err := tx.SetStatusReturningUpdatedAt(canceled, "pending"); !got.IsZero() || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled context=(%v,%v), want zero,context.Canceled", got, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := closed.SetStatusReturningUpdatedAt(context.Background(), "pending"); !got.IsZero() || err == nil {
		t.Fatalf("closed tx=(%v,%v), want zero,error", got, err)
	}
	workspace, err := repo.Workspace(context.Background(), binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if workspace.State != "clean" {
		t.Fatalf("invalid calls changed state to %q", workspace.State)
	}
}

func TestWorkspaceMutationTxSetStatusReturningUpdatedAtCallbackRollback(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	stashID := "00000000-0000-4000-8000-000000000031"
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.InsertStash(context.Background(), validWorkspaceStash(t, binding, stashID))
	}); err != nil {
		t.Fatal(err)
	}
	before := mustRestoreRetryState(t, repo, binding.Scope, stashID)
	callbackErr := errors.New("status timestamp rollback fixture")
	err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		updatedAt, err := tx.SetStatusReturningUpdatedAt(context.Background(), "pending")
		if err != nil {
			return err
		}
		strict, err := tx.RestoreRetryState(context.Background(), stashID)
		if err != nil {
			return err
		}
		if strict.Workspace.State != "pending" || !strict.BindingUpdatedAt.Equal(updatedAt) {
			t.Fatalf("transaction-local state=%q updated_at=%v, returned=%v", strict.Workspace.State, strict.BindingUpdatedAt, updatedAt)
		}
		return callbackErr
	})
	if !errors.Is(err, callbackErr) || errors.Is(err, ErrCommitOutcomeUnknown) {
		t.Fatalf("callback error=%v, want rollback fixture without unknown-commit sentinel", err)
	}
	after := mustRestoreRetryState(t, repo, binding.Scope, stashID)
	if after.Workspace.State != "clean" || !after.BindingUpdatedAt.Equal(before.BindingUpdatedAt) {
		t.Fatalf("rolled-back state=%q updated_at=%v, want clean/%v", after.Workspace.State, after.BindingUpdatedAt, before.BindingUpdatedAt)
	}
}

func TestWorkspaceMutationTxSetStatusReturningUpdatedAtIgnoredUpdateIsNotFound(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	if _, err := store.DB().Exec(`
		CREATE TRIGGER ignore_workspace_pending
		BEFORE UPDATE OF status ON workspace_bindings
		WHEN NEW.project_id='00000000-0000-4000-8000-000000000001'
		 AND NEW.workspace_id='00000000-0000-4000-8000-000000000011'
		 AND NEW.status='pending'
		BEGIN SELECT RAISE(IGNORE); END
	`); err != nil {
		t.Fatal(err)
	}
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.SetStatusReturningUpdatedAt(context.Background(), "pending")
		if !got.IsZero() || !errors.Is(err, ErrNotFound) {
			t.Fatalf("ignored update=(%v,%v), want zero,ErrNotFound", got, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	workspace, err := repo.Workspace(context.Background(), binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if workspace.State != "clean" {
		t.Fatalf("ignored update changed state to %q", workspace.State)
	}
}

func TestWorkspaceMutationTxSetStatusReturningUpdatedAtPrecedesAfterTriggerAndRollsBack(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	stashID := "00000000-0000-4000-8000-000000000031"
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		return tx.InsertStash(context.Background(), validWorkspaceStash(t, binding, stashID))
	}); err != nil {
		t.Fatal(err)
	}
	before := mustRestoreRetryState(t, repo, binding.Scope, stashID)
	wantRewritten := time.Date(2040, 1, 2, 3, 4, 5, 0, time.UTC)
	if _, err := store.DB().Exec(`
		CREATE TRIGGER rewrite_workspace_status_timestamp
		AFTER UPDATE OF status ON workspace_bindings
		WHEN NEW.project_id='00000000-0000-4000-8000-000000000001'
		 AND NEW.workspace_id='00000000-0000-4000-8000-000000000011'
		 AND NEW.status='pending'
		BEGIN
			UPDATE workspace_bindings SET updated_at='2040-01-02 03:04:05+00:00'
			WHERE project_id=NEW.project_id AND workspace_id=NEW.workspace_id;
		END
	`); err != nil {
		t.Fatal(err)
	}
	callbackErr := errors.New("after trigger rollback fixture")
	err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		returned, err := tx.SetStatusReturningUpdatedAt(context.Background(), "pending")
		if err != nil {
			return err
		}
		strict, err := tx.RestoreRetryState(context.Background(), stashID)
		if err != nil {
			return err
		}
		if strict.BindingUpdatedAt.Equal(returned) || !strict.BindingUpdatedAt.Equal(wantRewritten) {
			t.Fatalf("RETURNING=%v later strict updated_at=%v, want distinct rewritten timestamp", returned, strict.BindingUpdatedAt)
		}
		return callbackErr
	})
	if !errors.Is(err, callbackErr) || errors.Is(err, ErrCommitOutcomeUnknown) {
		t.Fatalf("callback error=%v, want after-trigger rollback fixture", err)
	}
	after := mustRestoreRetryState(t, repo, binding.Scope, stashID)
	if after.Workspace.State != "clean" || !after.BindingUpdatedAt.Equal(before.BindingUpdatedAt) {
		t.Fatalf("after-trigger rollback state=%q updated_at=%v, want clean/%v", after.Workspace.State, after.BindingUpdatedAt, before.BindingUpdatedAt)
	}
}

func TestWorkspaceMutationTxSetStatusReturningUpdatedAtCommitFailureKeepsSentinel(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		updatedAt, err := tx.SetStatusReturningUpdatedAt(context.Background(), "pending")
		if err != nil {
			return err
		}
		if !validUTCTimestamp(updatedAt) {
			t.Fatalf("returned updated_at=%v, want non-zero UTC", updatedAt)
		}
		if _, err := tx.conn.ExecContext(context.Background(), `PRAGMA defer_foreign_keys=ON`); err != nil {
			return err
		}
		_, err = tx.conn.ExecContext(context.Background(), `
			INSERT INTO workspace_conflicts
			(project_id,workspace_id,occurrence_id,conflict_id,record_kind,record_id,field_path,
			 conflict_kind,base_json,ours_json,theirs_json,state)
			VALUES ('00000000-0000-4000-8000-000000000099','00000000-0000-4000-8000-000000000098',
			 'deferred-status-timestamp','deferred-status-timestamp','task',
			 '00000000-0000-4000-8000-000000000097','/title','same_field','{}','{}','{}','open')
		`)
		return err
	})
	if !errors.Is(err, ErrCommitOutcomeUnknown) {
		t.Fatalf("commit error=%v, want ErrCommitOutcomeUnknown", err)
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
	workspace, err := repo.Workspace(context.Background(), binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if workspace.State != "clean" {
		t.Fatalf("failed commit persisted state=%q, want clean", workspace.State)
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

type rawAtomicWorkspaceCell struct {
	Quoted       string
	StorageClass string
}

type rawAtomicWorkspaceRow map[string]rawAtomicWorkspaceCell

type rawAtomicWorkspaceSnapshot map[string][]rawAtomicWorkspaceRow

func readAtomicWorkspaceRawSnapshot(t *testing.T, db *sql.DB) rawAtomicWorkspaceSnapshot {
	t.Helper()
	snapshot := make(rawAtomicWorkspaceSnapshot)
	for _, table := range []string{
		"workspace_bindings",
		"workspace_candidates",
		"workspace_overlay_operations",
		"workspace_materializations",
		"workspace_stashes",
		"workspace_conflicts",
		"workspace_transition_receipts",
	} {
		columns := readAtomicWorkspaceColumns(t, db, table)
		projections := make([]string, 0, len(columns)*2)
		for _, column := range columns {
			identifier := quoteSQLiteTestIdentifier(column)
			projections = append(projections, "quote("+identifier+")", "typeof("+identifier+")")
		}
		rows, err := db.Query(`SELECT ` + strings.Join(projections, ",") + ` FROM ` + quoteSQLiteTestIdentifier(table) + ` ORDER BY rowid`)
		if err != nil {
			t.Fatalf("read raw %s rows: %v", table, err)
		}
		for rows.Next() {
			values := make([]string, len(projections))
			destinations := make([]any, len(values))
			for index := range values {
				destinations[index] = &values[index]
			}
			if err := rows.Scan(destinations...); err != nil {
				_ = rows.Close()
				t.Fatalf("scan raw %s row: %v", table, err)
			}
			row := make(rawAtomicWorkspaceRow, len(columns))
			for index, column := range columns {
				row[column] = rawAtomicWorkspaceCell{Quoted: values[index*2], StorageClass: values[index*2+1]}
			}
			snapshot[table] = append(snapshot[table], row)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatalf("iterate raw %s rows: %v", table, err)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("close raw %s rows: %v", table, err)
		}
		if snapshot[table] == nil {
			snapshot[table] = make([]rawAtomicWorkspaceRow, 0)
		}
	}
	return snapshot
}

func readAtomicWorkspaceColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + quoteSQLiteTestIdentifier(table) + `)`)
	if err != nil {
		t.Fatalf("read %s columns: %v", table, err)
	}
	defer rows.Close()
	columns := make([]string, 0)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan %s column: %v", table, err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s columns: %v", table, err)
	}
	if len(columns) == 0 {
		t.Fatalf("table %s has no columns", table)
	}
	return columns
}

func quoteSQLiteTestIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func quoteSQLiteTextLiteral(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}

func assertAtomicWorkspaceRawDelta(t *testing.T, before, after rawAtomicWorkspaceSnapshot, targetTable string, targetKeys map[string]string, allowedColumns ...string) {
	t.Helper()
	allowed := make(map[string]struct{}, len(allowedColumns))
	for _, column := range allowedColumns {
		allowed[column] = struct{}{}
	}
	targetFound := false
	if len(before) != len(after) {
		t.Fatalf("raw table count changed: got %d want %d", len(after), len(before))
	}
	for table, beforeRows := range before {
		afterRows, ok := after[table]
		if !ok || len(afterRows) != len(beforeRows) {
			t.Fatalf("raw %s row count changed: got %d want %d", table, len(afterRows), len(beforeRows))
		}
		for rowIndex, beforeRow := range beforeRows {
			afterRow := afterRows[rowIndex]
			isTarget := table == targetTable && atomicWorkspaceRawRowMatches(beforeRow, targetKeys)
			if isTarget {
				targetFound = true
			}
			if len(afterRow) != len(beforeRow) {
				t.Fatalf("raw %s row %d column count changed", table, rowIndex)
			}
			for column, beforeCell := range beforeRow {
				afterCell, ok := afterRow[column]
				if !ok {
					t.Fatalf("raw %s row %d lost column %s", table, rowIndex, column)
				}
				if isTarget {
					if _, permitted := allowed[column]; permitted {
						if afterCell.StorageClass != beforeCell.StorageClass {
							t.Fatalf("raw %s.%s storage class changed: got %s want %s", table, column, afterCell.StorageClass, beforeCell.StorageClass)
						}
						continue
					}
				}
				if afterCell != beforeCell {
					t.Fatalf("unpermitted raw delta %s row %d column %s: got %+v want %+v", table, rowIndex, column, afterCell, beforeCell)
				}
			}
		}
	}
	if !targetFound {
		t.Fatalf("raw target %s keys=%v not found", targetTable, targetKeys)
	}
}

func findAtomicWorkspaceRawRow(t *testing.T, snapshot rawAtomicWorkspaceSnapshot, table string, targetKeys map[string]string) rawAtomicWorkspaceRow {
	t.Helper()
	for _, row := range snapshot[table] {
		if atomicWorkspaceRawRowMatches(row, targetKeys) {
			return row
		}
	}
	t.Fatalf("raw target %s keys=%v not found", table, targetKeys)
	return nil
}

func atomicWorkspaceRawRowMatches(row rawAtomicWorkspaceRow, targetKeys map[string]string) bool {
	for column, quoted := range targetKeys {
		if row[column].Quoted != quoted {
			return false
		}
	}
	return true
}

func assertRawAtomicCell(t *testing.T, row rawAtomicWorkspaceRow, column, quoted, storageClass string) {
	t.Helper()
	if got := row[column]; got.Quoted != quoted || got.StorageClass != storageClass {
		t.Fatalf("raw %s=%+v, want quoted=%s class=%s", column, got, quoted, storageClass)
	}
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

func changedWorkspaceTree(t *testing.T, binding types.WorkspaceBinding, name string) state.Tree {
	t.Helper()
	tree := workspaceTree(t, binding.Scope.ProjectID, binding.Repository)
	snapshot, err := state.DecodeTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Project.Name = name
	snapshot.Project.UpdatedAt = snapshot.Project.UpdatedAt.Add(time.Minute)
	tree, err = state.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func insertWorkspaceOperation(t *testing.T, store *Store, scope types.WorkspaceScope, generation int64, operation state.OperationV1, operationState string) []byte {
	return insertWorkspaceOperationOwned(t, store, scope, generation, operation, operationState, nil)
}

func insertWorkspaceOperationOwned(t *testing.T, store *Store, scope types.WorkspaceScope, generation int64, operation state.OperationV1, operationState string, stashID *string) []byte {
	t.Helper()
	operationJSON, err := state.CanonicalOperation(operation)
	if err != nil {
		t.Fatalf("canonical operation: %v", err)
	}
	insertWorkspaceOperationRawOwned(t, store, scope, generation, operation.ID, operationJSON, operationState, stashID)
	return operationJSON
}

func insertWorkspaceOperationRaw(t *testing.T, store *Store, scope types.WorkspaceScope, generation int64, operationID string, operationJSON []byte, operationState string) {
	insertWorkspaceOperationRawOwned(t, store, scope, generation, operationID, operationJSON, operationState, nil)
}

func insertWorkspaceOperationRawOwned(t *testing.T, store *Store, scope types.WorkspaceScope, generation int64, operationID string, operationJSON []byte, operationState string, stashID *string) {
	t.Helper()
	_, err := store.DB().Exec(`
		INSERT INTO workspace_overlay_operations
		(project_id, workspace_id, generation, operation_id, operation_json, state, stashed_by_stash_id)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, scope.ProjectID, scope.WorkspaceID, generation, operationID, string(operationJSON), operationState, stashID)
	if err != nil {
		t.Fatalf("insert workspace operation: %v", err)
	}
}

func recreateWorkspaceOperationsWithoutKeys(t *testing.T, store *Store) {
	t.Helper()
	for _, statement := range []string{
		`DROP TABLE workspace_overlay_operations`,
		`CREATE TABLE workspace_overlay_operations (
			project_id TEXT NOT NULL,
			workspace_id TEXT NOT NULL,
			generation INTEGER NOT NULL,
			operation_id TEXT NOT NULL,
			operation_json TEXT NOT NULL,
			state TEXT NOT NULL,
			stashed_by_stash_id TEXT,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
	} {
		if _, err := store.DB().Exec(statement); err != nil {
			t.Fatalf("replace operation table for corrupt-row test: %v", err)
		}
	}
}

func updateOperationAuditColumn(column, expression string) func(*testing.T, *Store, types.WorkspaceBinding) {
	return func(t *testing.T, store *Store, _ types.WorkspaceBinding) {
		t.Helper()
		if _, err := store.DB().Exec(fmt.Sprintf(`UPDATE workspace_overlay_operations SET %s=%s`, column, expression)); err != nil {
			t.Fatalf("corrupt operation audit %s: %v", column, err)
		}
	}
}

func readWorkspaceOperations(t *testing.T, store *Store, scope types.WorkspaceScope) []WorkspaceOperation {
	t.Helper()
	rows, err := store.DB().Query(`
		SELECT generation, operation_id, operation_json, state, stashed_by_stash_id
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
		var stashID sql.NullString
		if err := rows.Scan(&operation.Generation, &operation.OperationID, &operationJSON, &operation.State, &stashID); err != nil {
			t.Fatal(err)
		}
		operation.OperationJSON = bytes.Clone(operationJSON)
		if stashID.Valid {
			value := stashID.String
			operation.StashedByStashID = &value
		}
		operations = append(operations, operation)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return operations
}

func mustReadRebasedOperations(t *testing.T, tx *WorkspaceMutationTx, generation int64) []WorkspaceOperation {
	t.Helper()
	operations, err := tx.RebasedOperationsAtOrBefore(context.Background(), generation)
	if err != nil {
		t.Fatal(err)
	}
	return operations
}

func mustReadStashedOperations(t *testing.T, tx *WorkspaceMutationTx, stashID string) []WorkspaceOperation {
	t.Helper()
	operations, err := tx.StashedOperationsByStashID(context.Background(), stashID)
	if err != nil {
		t.Fatal(err)
	}
	return operations
}

func mustReadExactOperations(t *testing.T, tx *WorkspaceMutationTx, generations []int64) []WorkspaceOperation {
	t.Helper()
	operations, err := tx.OperationsByGenerations(context.Background(), generations)
	if err != nil {
		t.Fatal(err)
	}
	return operations
}

func stringPointer(value string) *string {
	return &value
}

func equalOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func withOperationIdentity(operation WorkspaceOperation, operationID string, operationJSON []byte) WorkspaceOperation {
	operation.OperationID = operationID
	operation.OperationJSON = bytes.Clone(operationJSON)
	return operation
}

func insertWorkspaceConflict(t *testing.T, store *Store, scope types.WorkspaceScope, conflictID string, key state.RecordKey, conflictState string) {
	t.Helper()
	digest := sha256.Sum256([]byte(conflictID))
	semanticID := fmt.Sprintf("sha256:%x", digest)
	_, err := store.DB().Exec(`
		INSERT INTO workspace_conflicts
		(project_id, workspace_id, occurrence_id, conflict_id, record_kind, record_id, field_path,
		 conflict_kind, base_json, ours_json, theirs_json, state)
		VALUES (?, ?, ?, ?, ?, ?, '/title', 'same_field', '{}', '{}', '{}', ?)
	`, scope.ProjectID, scope.WorkspaceID, semanticID, semanticID, key.Kind, key.ID, conflictState)
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

const workspaceCandidateImporter = "00000000-0000-4000-8000-000000000071"

var workspaceCandidateImportedAt = time.Date(2026, 7, 28, 14, 0, 0, 0, time.UTC)

func insertWorkspaceCandidate(t *testing.T, store *Store, scope types.WorkspaceScope, acceptedBase, working state.Digest, direct, rebased []byte, boundary int64) {
	t.Helper()
	_, err := store.DB().Exec(`
		INSERT INTO workspace_candidates
		(project_id, workspace_id, accepted_base_digest, working_tree_digest, direct_tree,
		 rebased_tree, rebased_through_generation, imported_by, imported_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, scope.ProjectID, scope.WorkspaceID, acceptedBase, working, direct, rebased, boundary,
		workspaceCandidateImporter, workspaceCandidateImportedAt)
	if err != nil {
		t.Fatalf("insert workspace candidate: %v", err)
	}
}

func workspaceCandidateRecord(t *testing.T, binding types.WorkspaceBinding, withRebased bool, boundary int64) WorkspaceCandidateRecord {
	t.Helper()
	direct, _ := encodedWorkspaceSnapshot(t, binding.Scope.ProjectID, binding.Repository)
	record := WorkspaceCandidateRecord{
		AcceptedBaseDigest:       state.Digest(binding.AcceptedTreeDigest),
		WorkingTreeDigest:        direct.Digest,
		DirectSnapshot:           direct,
		RebasedThroughGeneration: boundary,
		ImportedBy:               workspaceCandidateImporter,
		ImportedAt:               workspaceCandidateImportedAt,
	}
	if withRebased {
		rebased := direct
		rebased.Config.Handle = types.ProjectHandle{Namespace: "acme", Name: "rebased"}
		rebased, _ = encodedSnapshot(t, rebased)
		record.RebasedSnapshot = &rebased
	}
	return record
}

func mustEncodeWorkspaceSnapshot(t *testing.T, snapshot state.Snapshot) state.Tree {
	t.Helper()
	tree, err := state.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return tree
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
