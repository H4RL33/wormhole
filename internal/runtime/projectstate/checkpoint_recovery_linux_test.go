//go:build linux

package projectstate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
	"golang.org/x/sys/unix"
)

func TestRecoverPreparedTopologyMatrix(t *testing.T) {
	tests := []struct {
		name       string
		arrange    func(*testing.T, checkpointRecoveryLinuxFixture, state.Tree)
		wantLive   func(checkpointRecoveryLinuxFixture, state.Tree) state.Tree
		wantStage  bool
		wantBackup bool
		wantFsync  []string
	}{
		{
			name: "live prior stage candidate backup absent", wantStage: true,
			wantLive:  func(f checkpointRecoveryLinuxFixture, _ state.Tree) state.Tree { return f.journal.PriorTree },
			wantFsync: []string{"checkout", "private"},
		},
		{
			name: "live opaque stage candidate backup absent", wantStage: true,
			arrange: func(t *testing.T, f checkpointRecoveryLinuxFixture, opaque state.Tree) {
				checkpointFallbackReplaceTree(t, f.livePath(), opaque, ".retained-prior")
			},
			wantLive:  func(_ checkpointRecoveryLinuxFixture, opaque state.Tree) state.Tree { return opaque },
			wantFsync: []string{"checkout", "private"},
		},
		{
			name: "live absent stage candidate backup prior", wantStage: true,
			arrange: func(t *testing.T, f checkpointRecoveryLinuxFixture, _ state.Tree) {
				if err := os.Rename(f.livePath(), f.journal.BackupPath); err != nil {
					t.Fatal(err)
				}
			},
			wantLive:  func(f checkpointRecoveryLinuxFixture, _ state.Tree) state.Tree { return f.journal.PriorTree },
			wantFsync: []string{"checkout", "private"},
		},
		{
			name: "live absent stage candidate backup opaque", wantStage: true,
			arrange: func(t *testing.T, f checkpointRecoveryLinuxFixture, opaque state.Tree) {
				checkpointFallbackReplaceTree(t, f.livePath(), opaque, ".retained-prior")
				if err := os.Rename(f.livePath(), f.journal.BackupPath); err != nil {
					t.Fatal(err)
				}
			},
			wantLive:  func(_ checkpointRecoveryLinuxFixture, opaque state.Tree) state.Tree { return opaque },
			wantFsync: []string{"checkout", "private"},
		},
		{
			name: "recreated live preserves prior backup", wantStage: true, wantBackup: true,
			arrange: func(t *testing.T, f checkpointRecoveryLinuxFixture, opaque state.Tree) {
				if err := os.Rename(f.livePath(), f.journal.BackupPath); err != nil {
					t.Fatal(err)
				}
				checkpointFallbackCreateTree(t, f.livePath(), opaque)
			},
			wantLive: func(_ checkpointRecoveryLinuxFixture, opaque state.Tree) state.Tree { return opaque },
		},
		{
			name: "stable live preserves opaque backup", wantStage: true, wantBackup: true,
			arrange: func(t *testing.T, f checkpointRecoveryLinuxFixture, opaque state.Tree) {
				checkpointFallbackCreateTree(t, f.journal.BackupPath, opaque)
			},
			wantLive: func(f checkpointRecoveryLinuxFixture, _ state.Tree) state.Tree { return f.journal.PriorTree },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCheckpointRecoveryLinuxFixture(t, "prepared")
			opaque := checkpointRecoveryOpaqueTree()
			before := recoveryDatabaseState(t, fixture.service, fixture.request.Scope)
			if test.arrange != nil {
				test.arrange(t, fixture, opaque)
			}
			fsyncOrder := checkpointRecoveryInstallFsyncRecorder(t, fixture)
			got, err := fixture.service.Recover(context.Background(), fixture.request.Scope)
			if err != nil || got.Binding.Scope != fixture.request.Scope || !reflect.DeepEqual(*fsyncOrder, test.wantFsync) {
				t.Fatalf("Recover=(%+v,%v), fsync=%v want %v", got, err, *fsyncOrder, test.wantFsync)
			}
			recoveryAssertTree(t, fixture.livePath(), test.wantLive(fixture, opaque))
			recoveryAssertPath(t, fixture.journal.StagePath, test.wantStage, fixture.journal.CandidateTree)
			recoveryAssertPath(t, fixture.journal.BackupPath, test.wantBackup, func() state.Tree {
				if test.name == "stable live preserves opaque backup" {
					return opaque
				}
				return fixture.journal.PriorTree
			}())
			after := recoveryDatabaseState(t, fixture.service, fixture.request.Scope)
			recoveryAssertOldDatabase(t, before, after)
		})
	}
}

func TestRecoverPublishedTopologyConvergesNew(t *testing.T) {
	for _, test := range []struct {
		name    string
		arrange func(*testing.T, checkpointRecoveryLinuxFixture, state.Tree)
	}{
		{name: "candidate live prior backup"},
		{name: "opaque live and backup", arrange: func(t *testing.T, fixture checkpointRecoveryLinuxFixture, opaque state.Tree) {
			checkpointFallbackReplaceTree(t, fixture.livePath(), opaque, ".retained-candidate")
			checkpointFallbackReplaceTree(t, fixture.journal.BackupPath, opaque, ".retained-prior")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCheckpointRecoveryLinuxFixture(t, "published")
			opaque := checkpointRecoveryOpaqueTree()
			if test.arrange != nil {
				test.arrange(t, fixture, opaque)
			}
			beforeLive, beforeBackup := fixture.journal.CandidateTree, fixture.journal.PriorTree
			if test.arrange != nil {
				beforeLive, beforeBackup = opaque, opaque
			}
			before := recoveryDatabaseState(t, fixture.service, fixture.request.Scope)
			fsyncOrder := checkpointRecoveryInstallFsyncRecorder(t, fixture)
			got, err := fixture.service.Recover(context.Background(), fixture.request.Scope)
			if err != nil || got.Binding.Scope != fixture.request.Scope || !reflect.DeepEqual(*fsyncOrder, []string{"checkout", "private"}) {
				t.Fatalf("Recover published=(%+v,%v), fsync=%v", got, err, *fsyncOrder)
			}
			after := recoveryDatabaseState(t, fixture.service, fixture.request.Scope)
			recoveryAssertNewDatabase(t, before, after, false)
			recoveryAssertTree(t, fixture.livePath(), beforeLive)
			recoveryAssertTree(t, fixture.journal.BackupPath, beforeBackup)
			checkpointFallbackAssertAbsent(t, fixture.journal.StagePath)
		})
	}
}

func TestRecoverPreservesLaterLiveEditAfterPublication(t *testing.T) {
	fixture := newCheckpointRecoveryLinuxFixture(t, "prepared")
	if err := os.Rename(fixture.livePath(), fixture.journal.BackupPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fixture.journal.StagePath, fixture.livePath()); err != nil {
		t.Fatal(err)
	}
	opaque := checkpointRecoveryOpaqueTree()
	checkpointFallbackReplaceTree(t, fixture.livePath(), opaque, ".retained-candidate")
	candidateSnapshot, err := state.DecodeTree(cloneCheckpointTree(fixture.journal.CandidateTree))
	if err != nil {
		t.Fatal(err)
	}
	laterOperation := servicePutTaskOperation(
		candidateSnapshot,
		"99999999-9999-4999-8999-999999999992",
		"22222222-2222-4222-8222-222222222223",
		"later active operation",
	)
	laterSnapshot, err := state.ApplyOperation(candidateSnapshot, laterOperation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Apply(context.Background(), fixture.request.Scope, laterOperation); err != nil {
		t.Fatal(err)
	}
	before := recoveryDatabaseState(t, fixture.service, fixture.request.Scope)
	fsyncOrder := checkpointRecoveryInstallFsyncRecorder(t, fixture)
	got, err := fixture.service.Recover(context.Background(), fixture.request.Scope)
	if err != nil || got.Binding.Scope != fixture.request.Scope || !reflect.DeepEqual(*fsyncOrder, []string{"checkout", "private"}) {
		t.Fatalf("Recover later-live=(%+v,%v), fsync=%v", got, err, *fsyncOrder)
	}
	if got.CandidateDigest != laterSnapshot.Digest || got.OverlayGeneration != 2 {
		t.Fatalf("later-live status=%+v, want digest %q generation 2", got, laterSnapshot.Digest)
	}
	recoveryAssertTree(t, fixture.livePath(), opaque)
	recoveryAssertTree(t, fixture.journal.BackupPath, fixture.journal.PriorTree)
	checkpointFallbackAssertAbsent(t, fixture.journal.StagePath)
	after := recoveryDatabaseState(t, fixture.service, fixture.request.Scope)
	recoveryAssertNewDatabase(t, before, after, true)
}

func TestRecoverBlocksAmbiguousOrUnsafeTopologyWithoutMutation(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(*testing.T, checkpointRecoveryLinuxFixture, state.Tree)
		assert  func(*testing.T, checkpointRecoveryLinuxFixture, state.Tree)
	}{
		{
			name: "stage has opaque bytes",
			arrange: func(t *testing.T, f checkpointRecoveryLinuxFixture, opaque state.Tree) {
				checkpointFallbackReplaceTree(t, f.journal.StagePath, opaque, ".retained-candidate")
			},
			assert: func(t *testing.T, f checkpointRecoveryLinuxFixture, opaque state.Tree) {
				recoveryAssertTree(t, f.livePath(), f.journal.PriorTree)
				recoveryAssertTree(t, f.journal.StagePath, opaque)
				checkpointFallbackAssertAbsent(t, f.journal.BackupPath)
			},
		},
		{
			name: "ambiguous opaque backup after publication",
			arrange: func(t *testing.T, f checkpointRecoveryLinuxFixture, opaque state.Tree) {
				checkpointFallbackReplaceTree(t, f.livePath(), opaque, ".retained-prior")
				if err := os.Rename(f.livePath(), f.journal.BackupPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(f.journal.StagePath, f.livePath()); err != nil {
					t.Fatal(err)
				}
			},
			assert: func(t *testing.T, f checkpointRecoveryLinuxFixture, opaque state.Tree) {
				recoveryAssertTree(t, f.livePath(), f.journal.CandidateTree)
				recoveryAssertTree(t, f.journal.BackupPath, opaque)
				checkpointFallbackAssertAbsent(t, f.journal.StagePath)
			},
		},
		{
			name: "unsafe stage symlink",
			arrange: func(t *testing.T, f checkpointRecoveryLinuxFixture, _ state.Tree) {
				realStage := f.journal.StagePath + ".retained"
				if err := os.Rename(f.journal.StagePath, realStage); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(realStage, f.journal.StagePath); err != nil {
					t.Fatal(err)
				}
			},
			assert: func(t *testing.T, f checkpointRecoveryLinuxFixture, _ state.Tree) {
				info, err := os.Lstat(f.journal.StagePath)
				if err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("unsafe stage changed=(%v,%v)", info, err)
				}
				recoveryAssertTree(t, f.livePath(), f.journal.PriorTree)
			},
		},
		{
			name: "unlisted missing live and stage",
			arrange: func(t *testing.T, f checkpointRecoveryLinuxFixture, _ state.Tree) {
				if err := os.Rename(f.livePath(), f.journal.BackupPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(f.journal.StagePath, f.journal.StagePath+".retained"); err != nil {
					t.Fatal(err)
				}
			},
			assert: func(t *testing.T, f checkpointRecoveryLinuxFixture, _ state.Tree) {
				checkpointFallbackAssertAbsent(t, f.livePath())
				checkpointFallbackAssertAbsent(t, f.journal.StagePath)
				recoveryAssertTree(t, f.journal.BackupPath, f.journal.PriorTree)
			},
		},
	}

	t.Run("published intermediate stage blocks", func(t *testing.T) {
		fixture := newCheckpointRecoveryLinuxFixture(t, "published")
		checkpointFallbackCreateTree(t, fixture.journal.StagePath, fixture.journal.CandidateTree)
		before := recoveryDatabaseState(t, fixture.service, fixture.request.Scope)
		got, err := fixture.service.Recover(context.Background(), fixture.request.Scope)
		if !reflect.DeepEqual(got, WorkspaceStatus{}) || !errors.Is(err, ErrCheckpointRecoveryBlocked) {
			t.Fatalf("published intermediate Recover=(%+v,%v)", got, err)
		}
		if after := recoveryDatabaseState(t, fixture.service, fixture.request.Scope); !reflect.DeepEqual(after, before) {
			t.Fatal("published intermediate changed database state")
		}
		recoveryAssertTree(t, fixture.livePath(), fixture.journal.CandidateTree)
		recoveryAssertTree(t, fixture.journal.StagePath, fixture.journal.CandidateTree)
		recoveryAssertTree(t, fixture.journal.BackupPath, fixture.journal.PriorTree)
	})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCheckpointRecoveryLinuxFixture(t, "prepared")
			opaque := checkpointRecoveryOpaqueTree()
			test.arrange(t, fixture, opaque)
			before := recoveryDatabaseState(t, fixture.service, fixture.request.Scope)
			fsyncOrder := checkpointRecoveryInstallFsyncRecorder(t, fixture)
			got, err := fixture.service.Recover(context.Background(), fixture.request.Scope)
			if !reflect.DeepEqual(got, WorkspaceStatus{}) || !errors.Is(err, ErrCheckpointRecoveryBlocked) || len(*fsyncOrder) != 0 {
				t.Fatalf("blocked Recover=(%+v,%v), fsync=%v", got, err, *fsyncOrder)
			}
			if after := recoveryDatabaseState(t, fixture.service, fixture.request.Scope); !reflect.DeepEqual(after, before) {
				t.Fatal("blocked recovery changed database state")
			}
			test.assert(t, fixture, opaque)
		})
	}

	t.Run("missing Git-private root is a precondition", func(t *testing.T) {
		fixture := newCheckpointRecoveryLinuxFixture(t, "prepared")
		privateRoot := filepath.Dir(fixture.journal.StagePath)
		movedRoot := privateRoot + ".moved"
		if err := os.Rename(privateRoot, movedRoot); err != nil {
			t.Fatal(err)
		}
		before := recoveryDatabaseState(t, fixture.service, fixture.request.Scope)
		got, err := fixture.service.Recover(context.Background(), fixture.request.Scope)
		if !reflect.DeepEqual(got, WorkspaceStatus{}) || !errors.Is(err, ErrCheckpointRecoveryPrecondition) {
			t.Fatalf("missing-root Recover=(%+v,%v)", got, err)
		}
		if after := recoveryDatabaseState(t, fixture.service, fixture.request.Scope); !reflect.DeepEqual(after, before) {
			t.Fatal("missing-root recovery changed database state")
		}
		recoveryAssertTree(t, filepath.Join(movedRoot, filepath.Base(fixture.journal.StagePath)), fixture.journal.CandidateTree)
	})
}

type checkpointRecoveryLinuxFixture struct {
	*checkpointCoordinatorFixture
	request CheckpointRequest
	journal localstore.WorkspaceMaterializationRecord
}

func newCheckpointRecoveryLinuxFixture(t *testing.T, driverState string) checkpointRecoveryLinuxFixture {
	t.Helper()
	fixture, request, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
	accepted := fixture.mustAcceptedSnapshot(t)
	operation := servicePutTaskOperation(
		accepted,
		"99999999-9999-4999-8999-999999999991",
		"22222222-2222-4222-8222-222222222222",
		"recovery candidate",
	)
	if _, err := fixture.service.Apply(context.Background(), request.Scope, operation); err != nil {
		t.Fatal(err)
	}
	setServiceWorkspaceState(t, fixture.store, request.Scope, "clean")
	if driverState == "prepared" {
		injected := errors.New("retain prepared recovery journal")
		fixture.service.prepareCheckpointArtifact = func(ctx context.Context, input checkpointArtifactInput) (checkpointArtifactHandle, error) {
			handle, err := defaultPrepareCheckpointArtifact(ctx, input)
			if err == nil {
				handle.publish = func(context.Context) (checkpointPublicationDisposition, error) { return 0, injected }
			}
			return handle, err
		}
		if result, err := fixture.service.Checkpoint(context.Background(), request); result != (CheckpointResult{}) || !errors.Is(err, injected) {
			t.Fatalf("prepare recovery fixture=(%+v,%v)", result, err)
		}
	} else if driverState == "published" {
		fixture.service.prepareCheckpointArtifact = nil
		if result, err := fixture.service.Checkpoint(context.Background(), request); err != nil || result.JournalID == "" {
			t.Fatalf("publish recovery fixture=(%+v,%v)", result, err)
		}
	} else {
		t.Fatalf("unknown recovery driver %q", driverState)
	}
	disposition := readCheckpointDisposition(t, fixture.service, request.Scope)
	if len(disposition.Journals) != 1 || disposition.Journals[0].State != driverState {
		t.Fatalf("recovery fixture disposition=%+v", disposition)
	}
	return checkpointRecoveryLinuxFixture{checkpointCoordinatorFixture: fixture, request: request, journal: disposition.Journals[0]}
}

func (fixture checkpointRecoveryLinuxFixture) livePath() string {
	return filepath.Join(fixture.request.Root, ".wormhole")
}

func checkpointRecoveryInstallFsyncRecorder(t *testing.T, fixture checkpointRecoveryLinuxFixture) *[]string {
	t.Helper()
	order := new([]string)
	checkoutIdentity := checkpointRecoveryDirectoryIdentity(t, fixture.request.Root)
	privateIdentity := checkpointRecoveryDirectoryIdentity(t, filepath.Dir(fixture.journal.StagePath))
	operations := defaultCheckpointArtifactPlatformOperations()
	realFsync := operations.fsync
	operations.fsync = func(fd int) error {
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			return err
		}
		switch [2]uint64{uint64(stat.Dev), stat.Ino} {
		case checkoutIdentity:
			*order = append(*order, "checkout")
		case privateIdentity:
			*order = append(*order, "private")
		default:
			t.Fatalf("fsync unexpected directory dev=%d ino=%d", stat.Dev, stat.Ino)
		}
		return realFsync(fd)
	}
	fixture.service.recoverCheckpointFilesystem = func(ctx context.Context, proof checkpointRecoveryProof) (checkpointRecoveryFilesystemOutcome, error) {
		return recoverCheckpointFilesystemWithDependencies(ctx, proof, checkpointArtifactDependencies{
			readGit: readOnlyGitLimited, operations: operations,
		})
	}
	return order
}

func checkpointRecoveryDirectoryIdentity(t *testing.T, path string) [2]uint64 {
	t.Helper()
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		t.Fatal(err)
	}
	return [2]uint64{uint64(stat.Dev), stat.Ino}
}

func checkpointRecoveryOpaqueTree() state.Tree {
	return state.Tree{{Path: "opaque.txt", Data: []byte("later opaque bytes\n")}}
}

func recoveryAssertTree(t *testing.T, path string, want state.Tree) {
	t.Helper()
	got, err := readCheckpointArtifactPathTree(context.Background(), path)
	if err != nil || !sameCheckpointArtifactTree(got, want) {
		t.Fatalf("tree %q=(%v,%v), want %v", path, got, err, want)
	}
}

func recoveryAssertPath(t *testing.T, path string, exists bool, want state.Tree) {
	t.Helper()
	if !exists {
		checkpointFallbackAssertAbsent(t, path)
		return
	}
	recoveryAssertTree(t, path, want)
}

func recoveryAssertOldDatabase(t *testing.T, before, after checkpointRecoveryDatabaseState) {
	t.Helper()
	if !reflect.DeepEqual(after.workspace, before.workspace) || !reflect.DeepEqual(after.candidate, before.candidate) ||
		!reflect.DeepEqual(after.disposition.Operations, before.disposition.Operations) ||
		len(before.disposition.Journals) != 1 || len(after.disposition.Journals) != 1 ||
		after.disposition.Journals[0].State != "recovered_old" ||
		!recoveryJournalEqualExceptState(before.disposition.Journals[0], after.disposition.Journals[0]) {
		t.Fatalf("old database mismatch\nbefore=%+v\nafter=%+v", before, after)
	}
}

func recoveryAssertNewDatabase(t *testing.T, before, after checkpointRecoveryDatabaseState, prepared bool) {
	t.Helper()
	if before.workspace.Binding != after.workspace.Binding || !checkpointSnapshotsEqual(before.workspace.Snapshot, after.workspace.Snapshot) ||
		after.workspace.State != "pending" || len(before.disposition.Journals) != 1 || len(after.disposition.Journals) != 1 ||
		after.disposition.Journals[0].State != "recovered_new" ||
		!recoveryJournalEqualExceptState(before.disposition.Journals[0], after.disposition.Journals[0]) {
		t.Fatalf("new database journal/workspace mismatch\nbefore=%+v\nafter=%+v", before, after)
	}
	wantCandidate, err := checkpointPublicationPostimage(before.disposition.Journals[0])
	if err != nil || !equalCheckpointRecoveryCandidates(after.candidate, &wantCandidate) {
		t.Fatalf("new candidate=(%+v,%v), want %+v", after.candidate, err, wantCandidate)
	}
	for index, operation := range after.disposition.Operations {
		if operation.Generation <= before.disposition.Journals[0].ThroughGeneration {
			if operation.State != "materialized" {
				t.Fatalf("new owned operation=%+v, want materialized", operation)
			}
			continue
		}
		if index >= len(before.disposition.Operations) || !reflect.DeepEqual(operation, before.disposition.Operations[index]) || operation.State != "active" {
			t.Fatalf("new later operation=%+v, want unchanged active", operation)
		}
	}
	if prepared && before.candidate != nil {
		t.Fatalf("prepared fixture unexpectedly had candidate %+v", before.candidate)
	}
}

func recoveryJournalEqualExceptState(left, right localstore.WorkspaceMaterializationRecord) bool {
	return left.JournalID == right.JournalID && left.ExpectedLiveDigest == right.ExpectedLiveDigest &&
		left.AcceptedBaseDigest == right.AcceptedBaseDigest && left.Checkout == right.Checkout &&
		left.PriorTreeDigest == right.PriorTreeDigest && left.CandidateDigest == right.CandidateDigest &&
		left.ThroughGeneration == right.ThroughGeneration && sameCheckpointArtifactTree(left.PriorTree, right.PriorTree) &&
		sameCheckpointArtifactTree(left.CandidateTree, right.CandidateTree) && left.StagePath == right.StagePath &&
		left.BackupPath == right.BackupPath && reflect.DeepEqual(left.IncludedOperationsJSON, right.IncludedOperationsJSON) &&
		left.PublicationReviewProofVersion == right.PublicationReviewProofVersion &&
		reflect.DeepEqual(left.PublicationReviewJSON, right.PublicationReviewJSON) &&
		reflect.DeepEqual(left.PriorCandidateJSON, right.PriorCandidateJSON)
}
