//go:build linux

package projectstate

import (
	"context"
	"errors"
	"fmt"
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
		name           string
		arrange        func(*testing.T, checkpointRecoveryLinuxFixture, state.Tree)
		wantLive       func(checkpointRecoveryLinuxFixture, state.Tree) state.Tree
		wantBackupTree func(checkpointRecoveryLinuxFixture, state.Tree) state.Tree
		wantStage      bool
		wantBackup     bool
		wantFsync      []string
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
			name: "live candidate stage candidate backup absent", wantStage: true,
			arrange: func(t *testing.T, f checkpointRecoveryLinuxFixture, _ state.Tree) {
				checkpointFallbackWriteTree(t, f.livePath(), f.journal.CandidateTree)
			},
			wantLive:  func(f checkpointRecoveryLinuxFixture, _ state.Tree) state.Tree { return f.journal.CandidateTree },
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
			name: "live absent stage candidate backup candidate", wantStage: true,
			arrange: func(t *testing.T, f checkpointRecoveryLinuxFixture, _ state.Tree) {
				checkpointFallbackWriteTree(t, f.livePath(), f.journal.CandidateTree)
				if err := os.Rename(f.livePath(), f.journal.BackupPath); err != nil {
					t.Fatal(err)
				}
			},
			wantLive:  func(f checkpointRecoveryLinuxFixture, _ state.Tree) state.Tree { return f.journal.CandidateTree },
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
			wantLive:       func(_ checkpointRecoveryLinuxFixture, opaque state.Tree) state.Tree { return opaque },
			wantBackupTree: func(f checkpointRecoveryLinuxFixture, _ state.Tree) state.Tree { return f.journal.PriorTree },
			wantFsync:      []string{"checkout", "private"},
		},
		{
			name: "stable live preserves opaque backup", wantStage: true, wantBackup: true,
			arrange: func(t *testing.T, f checkpointRecoveryLinuxFixture, opaque state.Tree) {
				checkpointFallbackCreateTree(t, f.journal.BackupPath, opaque)
			},
			wantLive:       func(f checkpointRecoveryLinuxFixture, _ state.Tree) state.Tree { return f.journal.PriorTree },
			wantBackupTree: func(_ checkpointRecoveryLinuxFixture, opaque state.Tree) state.Tree { return opaque },
			wantFsync:      []string{"checkout", "private"},
		},
		{
			name: "stable prior live preserves candidate backup", wantStage: true, wantBackup: true,
			arrange: func(t *testing.T, f checkpointRecoveryLinuxFixture, _ state.Tree) {
				checkpointFallbackCreateTree(t, f.journal.BackupPath, f.journal.CandidateTree)
			},
			wantLive:       func(f checkpointRecoveryLinuxFixture, _ state.Tree) state.Tree { return f.journal.PriorTree },
			wantBackupTree: func(f checkpointRecoveryLinuxFixture, _ state.Tree) state.Tree { return f.journal.CandidateTree },
			wantFsync:      []string{"checkout", "private"},
		},
		{
			name: "stable candidate live preserves candidate backup", wantStage: true, wantBackup: true,
			arrange: func(t *testing.T, f checkpointRecoveryLinuxFixture, _ state.Tree) {
				checkpointFallbackWriteTree(t, f.livePath(), f.journal.CandidateTree)
				checkpointFallbackCreateTree(t, f.journal.BackupPath, f.journal.CandidateTree)
			},
			wantLive:       func(f checkpointRecoveryLinuxFixture, _ state.Tree) state.Tree { return f.journal.CandidateTree },
			wantBackupTree: func(f checkpointRecoveryLinuxFixture, _ state.Tree) state.Tree { return f.journal.CandidateTree },
			wantFsync:      []string{"checkout", "private"},
		},
		{
			name: "stable opaque live preserves candidate backup", wantStage: true, wantBackup: true,
			arrange: func(t *testing.T, f checkpointRecoveryLinuxFixture, opaque state.Tree) {
				checkpointFallbackReplaceTree(t, f.livePath(), opaque, ".retained-prior")
				checkpointFallbackCreateTree(t, f.journal.BackupPath, f.journal.CandidateTree)
			},
			wantLive:       func(_ checkpointRecoveryLinuxFixture, opaque state.Tree) state.Tree { return opaque },
			wantBackupTree: func(f checkpointRecoveryLinuxFixture, _ state.Tree) state.Tree { return f.journal.CandidateTree },
			wantFsync:      []string{"checkout", "private"},
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
			if err != nil || !reflect.DeepEqual(*fsyncOrder, test.wantFsync) {
				t.Fatalf("Recover=(%+v,%v), fsync=%v want %v", got, err, *fsyncOrder, test.wantFsync)
			}
			recoveryAssertTree(t, fixture.livePath(), test.wantLive(fixture, opaque))
			recoveryAssertPath(t, fixture.journal.StagePath, test.wantStage, fixture.journal.CandidateTree)
			var wantBackupTree state.Tree
			if test.wantBackupTree != nil {
				wantBackupTree = test.wantBackupTree(fixture, opaque)
			}
			recoveryAssertPath(t, fixture.journal.BackupPath, test.wantBackup, wantBackupTree)
			after := recoveryDatabaseState(t, fixture.service, fixture.request.Scope)
			recoveryAssertOldDatabase(t, before, after)
			recoveryAssertReturnedStatus(t, fixture.service, fixture.request.Scope, got)
		})
	}
}

func TestRecoverPreparedStageAbsentCandidateBackupIsBlocked(t *testing.T) {
	fixture := newCheckpointRecoveryLinuxFixture(t, "prepared")
	if err := os.RemoveAll(fixture.journal.StagePath); err != nil {
		t.Fatal(err)
	}
	checkpointFallbackCreateTree(t, fixture.journal.BackupPath, fixture.journal.CandidateTree)
	beforeDatabase := recoveryDatabaseState(t, fixture.service, fixture.request.Scope)
	beforePaths := recoveryScopePaths(t, beforeDatabase)
	operations := normalizeCheckpointArtifactRenameOperations(defaultCheckpointArtifactPlatformOperations())
	realRename := operations.rename
	renames := 0
	operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
		renames++
		return realRename(fromFD, from, toFD, to, flags)
	}
	fixture.service.recoverCheckpointFilesystem = func(ctx context.Context, proof checkpointRecoveryProof) (checkpointRecoveryFilesystemOutcome, error) {
		return recoverCheckpointFilesystemWithDependencies(ctx, proof, checkpointArtifactDependencies{
			readGit: readOnlyGitLimited, operations: operations,
		})
	}

	got, err := fixture.service.Recover(context.Background(), fixture.request.Scope)
	if !reflect.DeepEqual(got, WorkspaceStatus{}) || !errors.Is(err, ErrCheckpointRecoveryBlocked) ||
		errors.Is(err, ErrCheckpointRecoveryPrecondition) || renames != 0 {
		t.Fatalf("stage-absent candidate-backup recovery = (%+v, %v), renames=%d", got, err, renames)
	}
	if after := recoveryDatabaseState(t, fixture.service, fixture.request.Scope); !reflect.DeepEqual(after, beforeDatabase) {
		t.Fatal("stage-absent candidate-backup recovery changed database")
	}
	if after := recoveryScopePaths(t, beforeDatabase); !reflect.DeepEqual(after, beforePaths) {
		t.Fatal("stage-absent candidate-backup recovery changed filesystem evidence")
	}
}

func TestRecoverRejectsPublishedAndRecoveredNewPartialWorkspaceStateBeforeIO(t *testing.T) {
	for _, driverState := range []string{"published", "recovered_new"} {
		for _, workspaceState := range []string{"clean", "conflicted"} {
			t.Run(driverState+"/"+workspaceState, func(t *testing.T) {
				fixture := newCheckpointRecoveryLinuxFixture(t, "published")
				if driverState == "recovered_new" {
					if _, err := fixture.service.Recover(context.Background(), fixture.request.Scope); err != nil {
						t.Fatal(err)
					}
				}
				setServiceWorkspaceState(t, fixture.store, fixture.request.Scope, workspaceState)
				before := recoveryDatabaseState(t, fixture.service, fixture.request.Scope)
				gitCalls, pathCalls := 0, 0
				realObserve := fixture.service.observeCheckpointRecoveryGit
				if realObserve == nil {
					realObserve = observeCheckpointRecoveryGit
				}
				fixture.service.observeCheckpointRecoveryGit = func(ctx context.Context, proof checkpointRecoveryProof) (checkpointRecoveryGitObservation, error) {
					gitCalls++
					return realObserve(ctx, proof)
				}
				fixture.service.recoverCheckpointFilesystem = func(context.Context, checkpointRecoveryProof) (checkpointRecoveryFilesystemOutcome, error) {
					pathCalls++
					return 0, errors.New("unexpected recovery path call")
				}

				got, err := fixture.service.Recover(context.Background(), fixture.request.Scope)
				if !reflect.DeepEqual(got, WorkspaceStatus{}) || !errors.Is(err, ErrCheckpointRecoveryPrecondition) ||
					gitCalls != 0 || pathCalls != 0 {
					t.Fatalf("Recover partial %s/%s = (%+v, %v), git=%d path=%d", driverState, workspaceState, got, err, gitCalls, pathCalls)
				}
				if after := recoveryDatabaseState(t, fixture.service, fixture.request.Scope); !reflect.DeepEqual(after, before) {
					t.Fatal("partial driver state changed database")
				}
			})
		}
	}
}

func TestRecoverRestartConvergesExactCandidatePreLinearizationEvidence(t *testing.T) {
	fixture, request := newCheckpointRecoveryPublisherFixture(t)
	livePath := filepath.Join(request.Root, ".wormhole")
	var prepared localstore.WorkspaceMaterializationRecord
	checkpointRecoveryCapturePreparedJournal(t, fixture, &prepared)
	checkpointRecoveryUseRealPublisher(t, fixture, func(artifact *checkpointArtifact) {
		mutated := false
		artifact.dependencies.fault = func(stage checkpointArtifactFaultStage) error {
			if !mutated && stage == checkpointArtifactBeforeLiveMutation {
				mutated = true
				checkpointFallbackWriteTree(t, livePath, artifact.proof.candidate.tree)
			}
			return nil
		}
	})

	injected := errors.New("rollback recovered-old finalization")
	realWithImmediate := fixture.service.withImmediateWorkspace
	transactions := 0
	fixture.service.withImmediateWorkspace = func(
		ctx context.Context,
		scope types.WorkspaceScope,
		fn func(*localstore.WorkspaceMutationTx) error,
	) error {
		transactions++
		transaction := transactions
		return realWithImmediate(ctx, scope, func(tx *localstore.WorkspaceMutationTx) error {
			if err := fn(tx); err != nil {
				return err
			}
			if transaction == 2 {
				return injected
			}
			return nil
		})
	}
	if result, err := fixture.service.Checkpoint(context.Background(), request); result != (CheckpointResult{}) || !errors.Is(err, injected) {
		t.Fatalf("candidate-live checkpoint rollback = (%+v, %v)", result, err)
	}
	before := recoveryDatabaseState(t, fixture.service, request.Scope)
	checkpointRecoveryAssertPreparedDatabase(t, before)
	if !recoveryJournalEqualExceptState(prepared, before.disposition.Journals[0]) {
		t.Fatal("prepared journal differs after recovered-old rollback")
	}
	recoveryAssertTree(t, livePath, prepared.CandidateTree)
	recoveryAssertTree(t, prepared.StagePath, prepared.CandidateTree)
	checkpointFallbackAssertAbsent(t, prepared.BackupPath)

	checkpointRecoveryRestartService(t, fixture)
	operations := normalizeCheckpointArtifactRenameOperations(defaultCheckpointArtifactPlatformOperations())
	realRename := operations.rename
	renames := 0
	operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
		renames++
		return realRename(fromFD, from, toFD, to, flags)
	}
	fixture.service.recoverCheckpointFilesystem = func(ctx context.Context, proof checkpointRecoveryProof) (checkpointRecoveryFilesystemOutcome, error) {
		return recoverCheckpointFilesystemWithDependencies(ctx, proof, checkpointArtifactDependencies{
			readGit: readOnlyGitLimited, operations: operations,
		})
	}
	got, err := fixture.service.Recover(context.Background(), request.Scope)
	if err != nil || renames != 0 {
		t.Fatalf("candidate-live restart recovery = (%+v, %v), renames %d", got, err, renames)
	}
	after := recoveryDatabaseState(t, fixture.service, request.Scope)
	recoveryAssertOldDatabase(t, before, after)
	recoveryAssertTree(t, livePath, prepared.CandidateTree)
	recoveryAssertTree(t, prepared.StagePath, prepared.CandidateTree)
	checkpointFallbackAssertAbsent(t, prepared.BackupPath)
	recoveryAssertReturnedStatus(t, fixture.service, request.Scope, got)
}

func TestRecoverRestartRestoresCandidateBackupFromInterruptedRenameRaceOnce(t *testing.T) {
	fixture, request := newCheckpointRecoveryPublisherFixture(t)
	acceptedBefore := recoveryDatabaseState(t, fixture.service, request.Scope).workspace.Binding
	injected := errors.New("interrupt exact candidate backup race")
	checkpointRecoveryUseRealPublisher(t, fixture, func(artifact *checkpointArtifact) {
		realRename := artifact.dependencies.operations.rename
		renames := 0
		artifact.dependencies.operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
			renames++
			if renames == 1 {
				checkpointFallbackWriteTree(t, filepath.Join(request.Root, ".wormhole"), artifact.proof.candidate.tree)
			}
			if err := realRename(fromFD, from, toFD, to, flags); err != nil {
				return err
			}
			return nil
		}
		artifact.dependencies.fault = func(stage checkpointArtifactFaultStage) error {
			if stage == checkpointArtifactAfterLiveMutation {
				return injected
			}
			return nil
		}
	})

	gotCheckpoint, checkpointErr := fixture.service.Checkpoint(context.Background(), request)
	if gotCheckpoint != (CheckpointResult{}) || !errors.Is(checkpointErr, injected) {
		t.Fatalf("interrupted candidate-backup Checkpoint = (%+v, %v)", gotCheckpoint, checkpointErr)
	}
	beforeRecovery := recoveryDatabaseState(t, fixture.service, request.Scope)
	checkpointRecoveryAssertPreparedDatabase(t, beforeRecovery)
	journal := beforeRecovery.disposition.Journals[0]
	checkpointFallbackAssertAbsent(t, filepath.Join(request.Root, ".wormhole"))
	recoveryAssertTree(t, journal.StagePath, journal.CandidateTree)
	recoveryAssertTree(t, journal.BackupPath, journal.CandidateTree)

	checkpointRecoveryRestartService(t, fixture)
	operations := normalizeCheckpointArtifactRenameOperations(defaultCheckpointArtifactPlatformOperations())
	realRename := operations.rename
	renames := 0
	operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
		renames++
		return realRename(fromFD, from, toFD, to, flags)
	}
	fixture.service.recoverCheckpointFilesystem = func(ctx context.Context, proof checkpointRecoveryProof) (checkpointRecoveryFilesystemOutcome, error) {
		return recoverCheckpointFilesystemWithDependencies(ctx, proof, checkpointArtifactDependencies{
			readGit: readOnlyGitLimited, operations: operations,
		})
	}
	got, err := fixture.service.Recover(context.Background(), request.Scope)
	if err != nil || renames != 1 {
		t.Fatalf("candidate-backup restart recovery = (%+v, %v), renames=%d", got, err, renames)
	}
	after := recoveryDatabaseState(t, fixture.service, request.Scope)
	recoveryAssertOldDatabase(t, beforeRecovery, after)
	if after.workspace.Binding != acceptedBefore {
		t.Fatalf("candidate-backup restart moved accepted binding\nbefore=%+v\nafter=%+v", acceptedBefore, after.workspace.Binding)
	}
	recoveryAssertTree(t, filepath.Join(request.Root, ".wormhole"), journal.CandidateTree)
	recoveryAssertTree(t, journal.StagePath, journal.CandidateTree)
	checkpointFallbackAssertAbsent(t, journal.BackupPath)
	recoveryAssertReturnedStatus(t, fixture.service, request.Scope, got)

	replayed, replayErr := fixture.service.Recover(context.Background(), request.Scope)
	if replayErr != nil || replayed.Binding != got.Binding || replayed.State != got.State ||
		replayed.CandidateDigest != got.CandidateDigest || replayed.OverlayGeneration != got.OverlayGeneration || renames != 1 {
		t.Fatalf("candidate-backup recovery replay = (%+v, %v), renames=%d", replayed, replayErr, renames)
	}
}

func TestCheckpointPostJournalMountFailurePreservesCASAndSyscallCause(t *testing.T) {
	fixture, request := newCheckpointRecoveryPublisherFixture(t)
	before := recoveryDatabaseState(t, fixture.service, request.Scope)
	var prepared localstore.WorkspaceMaterializationRecord
	checkpointRecoveryCapturePreparedJournal(t, fixture, &prepared)
	mountCalls, renames := 0, 0
	checkpointRecoveryUseRealPublisher(t, fixture, func(artifact *checkpointArtifact) {
		realStatx := artifact.dependencies.mount.statx
		artifact.dependencies.mount.statx = func(fd int, path string, flags, mask int, stat *unix.Statx_t) error {
			mountCalls++
			if mountCalls == 1 {
				return unix.EIO
			}
			return realStatx(fd, path, flags, mask, stat)
		}
		realRename := artifact.dependencies.operations.rename
		artifact.dependencies.operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
			renames++
			return realRename(fromFD, from, toFD, to, flags)
		}
	})

	got, err := fixture.service.Checkpoint(context.Background(), request)
	if got != (CheckpointResult{}) || !errors.Is(err, ErrCheckpointCAS) || !errors.Is(err, unix.EIO) ||
		mountCalls != 1 || renames != 0 {
		t.Fatalf("post-journal mount failure = (%+v, %v), mount=%d rename=%d", got, err, mountCalls, renames)
	}
	after := recoveryDatabaseState(t, fixture.service, request.Scope)
	checkpointRecoveryAssertCheckpointOutcomeDatabase(t, before, after, prepared, "prepared")
	recoveryAssertTree(t, filepath.Join(request.Root, ".wormhole"), prepared.PriorTree)
	recoveryAssertTree(t, prepared.StagePath, prepared.CandidateTree)
	checkpointFallbackAssertAbsent(t, prepared.BackupPath)
}

func TestCheckpointPostJournalContextFailureRemainsRaw(t *testing.T) {
	fixture, request := newCheckpointRecoveryPublisherFixture(t)
	before := recoveryDatabaseState(t, fixture.service, request.Scope)
	var prepared localstore.WorkspaceMaterializationRecord
	checkpointRecoveryCapturePreparedJournal(t, fixture, &prepared)
	renamed := false
	checkpointRecoveryUseRealPublisher(t, fixture, func(artifact *checkpointArtifact) {
		artifact.dependencies.readGit = func(context.Context, string, int, ...string) ([]byte, error) {
			return nil, context.Canceled
		}
		realRename := artifact.dependencies.operations.rename
		artifact.dependencies.operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
			renamed = true
			return realRename(fromFD, from, toFD, to, flags)
		}
	})

	got, err := fixture.service.Checkpoint(context.Background(), request)
	if got != (CheckpointResult{}) || !errors.Is(err, context.Canceled) || errors.Is(err, ErrCheckpointCAS) || renamed {
		t.Fatalf("post-journal context failure = (%+v, %v), renamed=%t", got, err, renamed)
	}
	after := recoveryDatabaseState(t, fixture.service, request.Scope)
	checkpointRecoveryAssertCheckpointOutcomeDatabase(t, before, after, prepared, "prepared")
	recoveryAssertTree(t, filepath.Join(request.Root, ".wormhole"), prepared.PriorTree)
	recoveryAssertTree(t, prepared.StagePath, prepared.CandidateTree)
	checkpointFallbackAssertAbsent(t, prepared.BackupPath)
}

func TestCheckpointPublisherRenameClassificationFailurePreservesBothCauses(t *testing.T) {
	for _, role := range []string{"live-to-backup", "stage-to-live", "compensation"} {
		t.Run(role, func(t *testing.T) {
			fixture, request := newCheckpointRecoveryPublisherFixture(t)
			before := recoveryDatabaseState(t, fixture.service, request.Scope)
			acceptedBefore := before.workspace.Binding
			var prepared localstore.WorkspaceMaterializationRecord
			checkpointRecoveryCapturePreparedJournal(t, fixture, &prepared)
			renameCause := errors.New(role + " rename uncertainty")
			classificationCause := errors.New(role + " classifier failure")
			targetRenames, fsyncAfterTarget := 0, 0
			checkpointRecoveryUseRealPublisher(t, fixture, func(artifact *checkpointArtifact) {
				realReadGit := artifact.dependencies.readGit
				targetReturned := false
				artifact.dependencies.readGit = func(ctx context.Context, root string, limit int, args ...string) ([]byte, error) {
					if targetReturned {
						return nil, classificationCause
					}
					return realReadGit(ctx, root, limit, args...)
				}
				stageName := filepath.Base(artifact.evidenceValue.StagePath)
				backupName := filepath.Base(artifact.evidenceValue.BackupPath)
				realRename := artifact.dependencies.operations.rename
				artifact.dependencies.operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
					target := role == "live-to-backup" && from == ".wormhole" && to == backupName ||
						role == "stage-to-live" && from == stageName && to == ".wormhole" ||
						role == "compensation" && from == backupName && to == ".wormhole"
					if target {
						targetRenames++
						targetReturned = true
						return renameCause
					}
					return realRename(fromFD, from, toFD, to, flags)
				}
				realFsync := artifact.dependencies.operations.fsync
				artifact.dependencies.operations.fsync = func(fd int) error {
					if targetReturned {
						fsyncAfterTarget++
					}
					return realFsync(fd)
				}
				if role == "compensation" {
					artifact.dependencies.fault = func(stage checkpointArtifactFaultStage) error {
						if stage == checkpointArtifactAfterLiveMutation {
							checkpointFallbackWriteTree(t, artifact.evidenceValue.BackupPath, artifact.proof.candidate.tree)
						}
						return nil
					}
				}
			})

			got, err := fixture.service.Checkpoint(context.Background(), request)
			if got != (CheckpointResult{}) || !errors.Is(err, ErrCheckpointRecoveryBlocked) ||
				!errors.Is(err, renameCause) || !errors.Is(err, classificationCause) ||
				targetRenames != 1 || fsyncAfterTarget != 0 {
				t.Fatalf("%s classifier failure = (%+v, %v), target renames=%d fsync-after=%d", role, got, err, targetRenames, fsyncAfterTarget)
			}
			after := recoveryDatabaseState(t, fixture.service, request.Scope)
			checkpointRecoveryAssertCheckpointOutcomeDatabase(t, before, after, prepared, "prepared")
			if after.workspace.Binding != acceptedBefore {
				t.Fatalf("%s classifier failure moved accepted binding", role)
			}
			switch role {
			case "live-to-backup":
				recoveryAssertTree(t, filepath.Join(request.Root, ".wormhole"), prepared.PriorTree)
				recoveryAssertTree(t, prepared.StagePath, prepared.CandidateTree)
				checkpointFallbackAssertAbsent(t, prepared.BackupPath)
			case "stage-to-live":
				checkpointFallbackAssertAbsent(t, filepath.Join(request.Root, ".wormhole"))
				recoveryAssertTree(t, prepared.StagePath, prepared.CandidateTree)
				recoveryAssertTree(t, prepared.BackupPath, prepared.PriorTree)
			case "compensation":
				checkpointFallbackAssertAbsent(t, filepath.Join(request.Root, ".wormhole"))
				recoveryAssertTree(t, prepared.StagePath, prepared.CandidateTree)
				recoveryAssertTree(t, prepared.BackupPath, prepared.CandidateTree)
			}
		})
	}
}

func TestCheckpointAndRecoverRejectNestedMountSubstitutionBeforeRename(t *testing.T) {
	for _, operation := range []string{"checkpoint", "recover"} {
		for _, test := range []struct {
			name       string
			targetKind string
			occurrence int
			evidence   string
			wantCause  error
		}{
			{name: "directory/mount-root/capture-1", targetKind: "directory", occurrence: 1, evidence: "mount-root", wantCause: ErrCheckpointUnsupported},
			{name: "directory/mount-root/capture-2", targetKind: "directory", occurrence: 2, evidence: "mount-root", wantCause: ErrCheckpointUnsupported},
			{name: "file/different-mount/capture-1", targetKind: "file", occurrence: 1, evidence: "different-mount", wantCause: ErrCheckpointUnsupported},
			{name: "file/different-mount/capture-2", targetKind: "file", occurrence: 2, evidence: "different-mount", wantCause: ErrCheckpointUnsupported},
			{name: "file/statx-EIO/capture-2", targetKind: "file", occurrence: 2, evidence: "EIO", wantCause: unix.EIO},
		} {
			t.Run(fmt.Sprintf("%s/%s", operation, test.name), func(t *testing.T) {
				fixture, request := newCheckpointRecoveryPublisherFixture(t)
				before := recoveryDatabaseState(t, fixture.service, request.Scope)
				var prepared localstore.WorkspaceMaterializationRecord
				var targetCalls, renames int
				installMountSubstitution := func(artifact *checkpointArtifact, root string) {
					targetPath := filepath.Join(root, "state", "v1")
					if test.targetKind == "file" {
						targetPath = filepath.Join(targetPath, "project.json")
					}
					var target unix.Stat_t
					if err := unix.Stat(targetPath, &target); err != nil {
						t.Fatal(err)
					}
					realStatx := artifact.dependencies.mount.statx
					artifact.dependencies.mount.statx = func(fd int, path string, flags, mask int, stat *unix.Statx_t) error {
						if err := realStatx(fd, path, flags, mask, stat); err != nil {
							return err
						}
						var opened unix.Stat_t
						if err := unix.Fstat(fd, &opened); err != nil {
							return err
						}
						if opened.Dev != target.Dev || opened.Ino != target.Ino {
							return nil
						}
						mountIdentity := mask&unix.STATX_BASIC_STATS == 0 && mask&(unix.STATX_MNT_ID|unix.STATX_MNT_ID_UNIQUE) != 0
						if mountIdentity {
							targetCalls++
							if targetCalls == test.occurrence && test.evidence == "different-mount" {
								stat.Mnt_id++
							}
							if targetCalls == test.occurrence && test.evidence == "EIO" {
								return unix.EIO
							}
						}
						if mask&unix.STATX_BASIC_STATS != 0 && targetCalls == test.occurrence && test.evidence == "mount-root" {
							stat.Attributes_mask |= unix.STATX_ATTR_MOUNT_ROOT
							stat.Attributes |= unix.STATX_ATTR_MOUNT_ROOT
						}
						return nil
					}
					realRename := artifact.dependencies.operations.rename
					artifact.dependencies.operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
						renames++
						return realRename(fromFD, from, toFD, to, flags)
					}
				}

				switch operation {
				case "checkpoint":
					checkpointRecoveryCapturePreparedJournal(t, fixture, &prepared)
					checkpointRecoveryUseRealPublisher(t, fixture, func(artifact *checkpointArtifact) {
						installMountSubstitution(artifact, artifact.evidenceValue.StagePath)
					})
					got, err := fixture.service.Checkpoint(context.Background(), request)
					if got != (CheckpointResult{}) || !errors.Is(err, ErrCheckpointRecoveryBlocked) ||
						errors.Is(err, ErrCheckpointRecoveryPrecondition) || !errors.Is(err, test.wantCause) ||
						targetCalls != test.occurrence || renames != 0 {
						t.Fatalf("nested mount checkpoint = (%+v, %v), target=%d rename=%d", got, err, targetCalls, renames)
					}
					after := recoveryDatabaseState(t, fixture.service, request.Scope)
					checkpointRecoveryAssertCheckpointOutcomeDatabase(t, before, after, prepared, "prepared")
					recoveryAssertTree(t, filepath.Join(request.Root, ".wormhole"), prepared.PriorTree)
					recoveryAssertTree(t, prepared.StagePath, prepared.CandidateTree)
					checkpointFallbackAssertAbsent(t, prepared.BackupPath)
				case "recover":
					preparedFixture := newCheckpointRecoveryLinuxFixture(t, "prepared")
					if err := os.Rename(preparedFixture.livePath(), preparedFixture.journal.BackupPath); err != nil {
						t.Fatal(err)
					}
					before = recoveryDatabaseState(t, preparedFixture.service, preparedFixture.request.Scope)
					beforePaths := recoveryScopePaths(t, before)
					prepared = preparedFixture.journal
					preparedFixture.service.recoverCheckpointFilesystem = func(ctx context.Context, proof checkpointRecoveryProof) (checkpointRecoveryFilesystemOutcome, error) {
						dependencies := checkpointArtifactDependencies{readGit: readOnlyGitLimited}
						artifact, _, _, err := openCheckpointRecoveryArtifact(ctx, proof, dependencies)
						if err != nil {
							return 0, err
						}
						installMountSubstitution(artifact, prepared.BackupPath)
						dependencies = artifact.dependencies
						artifact.close()
						return recoverCheckpointFilesystemWithDependencies(ctx, proof, dependencies)
					}
					got, err := preparedFixture.service.Recover(context.Background(), preparedFixture.request.Scope)
					if !reflect.DeepEqual(got, WorkspaceStatus{}) || !errors.Is(err, ErrCheckpointRecoveryBlocked) ||
						errors.Is(err, ErrCheckpointRecoveryPrecondition) || !errors.Is(err, test.wantCause) ||
						targetCalls != test.occurrence || renames != 0 {
						t.Fatalf("nested mount recover = (%+v, %v), target=%d rename=%d", got, err, targetCalls, renames)
					}
					if after := recoveryDatabaseState(t, preparedFixture.service, preparedFixture.request.Scope); !reflect.DeepEqual(after, before) {
						t.Fatal("nested mount recovery changed database")
					}
					if afterPaths := recoveryScopePaths(t, before); !reflect.DeepEqual(afterPaths, beforePaths) {
						t.Fatal("nested mount recovery changed filesystem evidence")
					}
				}
			})
		}
	}
}

func TestCheckpointAndRecoverRevalidatePersistentRootsAtClassifierTail(t *testing.T) {
	for _, operation := range []string{"checkpoint", "recover"} {
		for _, churn := range []string{"checkout-rebind", "private-rebind", "private-mode"} {
			t.Run(operation+"/"+churn, func(t *testing.T) {
				var mutated bool
				var restore func()
				var targetRenames, fsyncAfterMutation int
				if operation == "checkpoint" {
					fixture, request := newCheckpointRecoveryPublisherFixture(t)
					before := recoveryDatabaseState(t, fixture.service, request.Scope)
					acceptedBefore := before.workspace.Binding
					var prepared localstore.WorkspaceMaterializationRecord
					checkpointRecoveryCapturePreparedJournal(t, fixture, &prepared)
					checkpointRecoveryUseRealPublisher(t, fixture, func(artifact *checkpointArtifact) {
						armed, armedReads := false, 0
						realReadGit := artifact.dependencies.readGit
						artifact.dependencies.readGit = func(ctx context.Context, root string, limit int, args ...string) ([]byte, error) {
							output, err := realReadGit(ctx, root, limit, args...)
							if err == nil && armed {
								armedReads++
								if armedReads == 3 {
									mutated = true
									restore = checkpointRecoveryApplyRootChurn(t, churn, request.Root, filepath.Dir(prepared.StagePath))
								}
							}
							return output, err
						}
						backupName := filepath.Base(artifact.evidenceValue.BackupPath)
						realFstatat := artifact.dependencies.operations.fstatat
						backupStats := 0
						artifact.dependencies.operations.fstatat = func(fd int, name string, stat *unix.Stat_t, flags int) error {
							err := realFstatat(fd, name, stat, flags)
							if name == backupName {
								backupStats++
								if backupStats == 3 {
									armed = true
								}
							}
							return err
						}
						realRename := artifact.dependencies.operations.rename
						artifact.dependencies.operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
							targetRenames++
							return realRename(fromFD, from, toFD, to, flags)
						}
						realFsync := artifact.dependencies.operations.fsync
						artifact.dependencies.operations.fsync = func(fd int) error {
							if mutated {
								fsyncAfterMutation++
							}
							return realFsync(fd)
						}
					})
					got, err := fixture.service.Checkpoint(context.Background(), request)
					if restore != nil {
						restore()
					}
					if got != (CheckpointResult{}) || !mutated || targetRenames != 0 || fsyncAfterMutation != 0 ||
						!errors.Is(err, ErrCheckpointRecoveryBlocked) || errors.Is(err, ErrCheckpointRecoveryPrecondition) {
						t.Fatalf("classifier-tail checkpoint = (%+v, %v), mutated=%t renames=%d fsync-after=%d", got, err, mutated, targetRenames, fsyncAfterMutation)
					}
					checkpointRecoveryAssertRootChurnCause(t, churn, err)
					after := recoveryDatabaseState(t, fixture.service, request.Scope)
					checkpointRecoveryAssertCheckpointOutcomeDatabase(t, before, after, prepared, "prepared")
					if after.workspace.Binding != acceptedBefore {
						t.Fatal("classifier-tail checkpoint moved accepted binding")
					}
					recoveryAssertTree(t, filepath.Join(request.Root, ".wormhole"), prepared.PriorTree)
					recoveryAssertTree(t, prepared.StagePath, prepared.CandidateTree)
					checkpointFallbackAssertAbsent(t, prepared.BackupPath)
					return
				}

				fixture := newCheckpointRecoveryLinuxFixture(t, "prepared")
				before := recoveryDatabaseState(t, fixture.service, fixture.request.Scope)
				beforePaths := recoveryScopePaths(t, before)
				armed, armedReads := false, 0
				dependencies := checkpointArtifactDependencies{readGit: func(ctx context.Context, root string, limit int, args ...string) ([]byte, error) {
					output, err := readOnlyGitLimited(ctx, root, limit, args...)
					if err == nil && armed {
						armedReads++
						if armedReads == 3 {
							mutated = true
							restore = checkpointRecoveryApplyRootChurn(t, churn, fixture.request.Root, filepath.Dir(fixture.journal.StagePath))
						}
					}
					return output, err
				}}
				operations := normalizeCheckpointArtifactRenameOperations(defaultCheckpointArtifactPlatformOperations())
				realFstatat := operations.fstatat
				backupName := filepath.Base(fixture.journal.BackupPath)
				backupStats := 0
				operations.fstatat = func(fd int, name string, stat *unix.Stat_t, flags int) error {
					err := realFstatat(fd, name, stat, flags)
					if name == backupName {
						backupStats++
						if backupStats == 3 {
							armed = true
						}
					}
					return err
				}
				realRename := operations.rename
				operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
					targetRenames++
					return realRename(fromFD, from, toFD, to, flags)
				}
				realFsync := operations.fsync
				operations.fsync = func(fd int) error {
					if mutated {
						fsyncAfterMutation++
					}
					return realFsync(fd)
				}
				dependencies.operations = operations
				fixture.service.recoverCheckpointFilesystem = func(ctx context.Context, proof checkpointRecoveryProof) (checkpointRecoveryFilesystemOutcome, error) {
					return recoverCheckpointFilesystemWithDependencies(ctx, proof, dependencies)
				}
				got, err := fixture.service.Recover(context.Background(), fixture.request.Scope)
				if restore != nil {
					restore()
				}
				if !reflect.DeepEqual(got, WorkspaceStatus{}) || !mutated || targetRenames != 0 || fsyncAfterMutation != 0 ||
					!errors.Is(err, ErrCheckpointRecoveryPrecondition) || errors.Is(err, ErrCheckpointRecoveryBlocked) {
					t.Fatalf("classifier-tail recover = (%+v, %v), mutated=%t renames=%d fsync-after=%d", got, err, mutated, targetRenames, fsyncAfterMutation)
				}
				checkpointRecoveryAssertRootChurnCause(t, churn, err)
				if after := recoveryDatabaseState(t, fixture.service, fixture.request.Scope); !reflect.DeepEqual(after, before) {
					t.Fatal("classifier-tail recovery changed database")
				}
				if after := recoveryScopePaths(t, before); !reflect.DeepEqual(after, beforePaths) {
					t.Fatal("classifier-tail recovery changed filesystem evidence")
				}
			})
		}
	}
}

func TestCheckpointAndRecoverRevalidatePersistentRootsImmediatelyBeforeEveryRename(t *testing.T) {
	for _, role := range []string{"live-to-backup", "stage-to-live", "compensation", "recovery-restore"} {
		for _, churn := range []string{"checkout-rebind", "private-rebind", "private-mode"} {
			t.Run(role+"/"+churn, func(t *testing.T) {
				if role == "recovery-restore" {
					fixture := newCheckpointRecoveryLinuxFixture(t, "prepared")
					if err := os.Rename(fixture.livePath(), fixture.journal.BackupPath); err != nil {
						t.Fatal(err)
					}
					before := recoveryDatabaseState(t, fixture.service, fixture.request.Scope)
					beforePaths := recoveryScopePaths(t, before)
					readCalls, targetRenames, fsyncAfterMutation := 0, 0, 0
					mutated := false
					var restore func()
					operations := normalizeCheckpointArtifactRenameOperations(defaultCheckpointArtifactPlatformOperations())
					realRename := operations.rename
					operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
						targetRenames++
						return realRename(fromFD, from, toFD, to, flags)
					}
					realFsync := operations.fsync
					operations.fsync = func(fd int) error {
						if mutated {
							fsyncAfterMutation++
						}
						return realFsync(fd)
					}
					fixture.service.recoverCheckpointFilesystem = func(ctx context.Context, proof checkpointRecoveryProof) (checkpointRecoveryFilesystemOutcome, error) {
						return recoverCheckpointFilesystemWithDependencies(ctx, proof, checkpointArtifactDependencies{
							readGit: func(ctx context.Context, root string, limit int, args ...string) ([]byte, error) {
								output, err := readOnlyGitLimited(ctx, root, limit, args...)
								if err == nil {
									readCalls++
									if readCalls == 12 {
										mutated = true
										restore = checkpointRecoveryApplyRootChurn(t, churn, fixture.request.Root, filepath.Dir(fixture.journal.StagePath))
									}
								}
								return output, err
							}, operations: operations,
						})
					}
					got, err := fixture.service.Recover(context.Background(), fixture.request.Scope)
					if restore != nil {
						restore()
					}
					if !reflect.DeepEqual(got, WorkspaceStatus{}) || !mutated || targetRenames != 0 || fsyncAfterMutation != 0 ||
						!errors.Is(err, ErrCheckpointRecoveryPrecondition) || errors.Is(err, ErrCheckpointRecoveryBlocked) {
						t.Fatalf("adjacent recovery restore = (%+v, %v), reads=%d mutated=%t renames=%d fsync-after=%d", got, err, readCalls, mutated, targetRenames, fsyncAfterMutation)
					}
					checkpointRecoveryAssertRootChurnCause(t, churn, err)
					if after := recoveryDatabaseState(t, fixture.service, fixture.request.Scope); !reflect.DeepEqual(after, before) {
						t.Fatal("adjacent recovery restore changed database")
					}
					if after := recoveryScopePaths(t, before); !reflect.DeepEqual(after, beforePaths) {
						t.Fatal("adjacent recovery restore changed filesystem evidence")
					}
					return
				}

				fixture, request := newCheckpointRecoveryPublisherFixture(t)
				before := recoveryDatabaseState(t, fixture.service, request.Scope)
				acceptedBefore := before.workspace.Binding
				var prepared localstore.WorkspaceMaterializationRecord
				checkpointRecoveryCapturePreparedJournal(t, fixture, &prepared)
				mutated, armed, afterArmReads := false, false, 0
				targetRenames, fsyncAfterMutation := 0, 0
				var restore func()
				opaque := checkpointRecoveryOpaqueTree()
				checkpointRecoveryUseRealPublisher(t, fixture, func(artifact *checkpointArtifact) {
					realReadGit := artifact.dependencies.readGit
					artifact.dependencies.readGit = func(ctx context.Context, root string, limit int, args ...string) ([]byte, error) {
						output, err := realReadGit(ctx, root, limit, args...)
						if err == nil && armed {
							afterArmReads++
							if afterArmReads == 9 {
								mutated = true
								restore = checkpointRecoveryApplyRootChurn(t, churn, request.Root, filepath.Dir(prepared.StagePath))
							}
						}
						return output, err
					}
					seenLiveFsync := 0
					artifact.dependencies.fault = func(stage checkpointArtifactFaultStage) error {
						if role == "live-to-backup" && stage == checkpointArtifactBeforeLiveMutation {
							armed = true
						}
						if (role == "stage-to-live" || role == "compensation") && stage == checkpointArtifactBeforeSecondLiveMutation {
							armed = true
						}
						if role == "compensation" && stage == checkpointArtifactAfterLiveParentFsync {
							seenLiveFsync++
							if seenLiveFsync == 1 {
								checkpointFallbackWriteTree(t, artifact.evidenceValue.BackupPath, opaque)
								opaque = recoveryReadEvidenceTree(t, artifact.evidenceValue.BackupPath)
							}
						}
						return nil
					}
					realRename := artifact.dependencies.operations.rename
					stageName, backupName := filepath.Base(artifact.evidenceValue.StagePath), filepath.Base(artifact.evidenceValue.BackupPath)
					artifact.dependencies.operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
						target := role == "live-to-backup" && from == ".wormhole" && to == backupName ||
							role == "stage-to-live" && from == stageName && to == ".wormhole" ||
							role == "compensation" && from == backupName && to == ".wormhole"
						if target {
							targetRenames++
						}
						return realRename(fromFD, from, toFD, to, flags)
					}
					realFsync := artifact.dependencies.operations.fsync
					artifact.dependencies.operations.fsync = func(fd int) error {
						if mutated {
							fsyncAfterMutation++
						}
						return realFsync(fd)
					}
				})
				got, err := fixture.service.Checkpoint(context.Background(), request)
				if restore != nil {
					restore()
				}
				if got != (CheckpointResult{}) || !mutated || targetRenames != 0 || fsyncAfterMutation != 0 ||
					!errors.Is(err, ErrCheckpointRecoveryBlocked) || errors.Is(err, ErrCheckpointRecoveryPrecondition) {
					t.Fatalf("adjacent publisher %s = (%+v, %v), reads=%d mutated=%t target-renames=%d fsync-after=%d", role, got, err, afterArmReads, mutated, targetRenames, fsyncAfterMutation)
				}
				checkpointRecoveryAssertRootChurnCause(t, churn, err)
				after := recoveryDatabaseState(t, fixture.service, request.Scope)
				checkpointRecoveryAssertCheckpointOutcomeDatabase(t, before, after, prepared, "prepared")
				if after.workspace.Binding != acceptedBefore {
					t.Fatal("adjacent publisher moved accepted binding")
				}
				switch role {
				case "live-to-backup":
					recoveryAssertTree(t, filepath.Join(request.Root, ".wormhole"), prepared.PriorTree)
					recoveryAssertTree(t, prepared.StagePath, prepared.CandidateTree)
					checkpointFallbackAssertAbsent(t, prepared.BackupPath)
				case "stage-to-live":
					checkpointFallbackAssertAbsent(t, filepath.Join(request.Root, ".wormhole"))
					recoveryAssertTree(t, prepared.StagePath, prepared.CandidateTree)
					recoveryAssertTree(t, prepared.BackupPath, prepared.PriorTree)
				case "compensation":
					checkpointFallbackAssertAbsent(t, filepath.Join(request.Root, ".wormhole"))
					recoveryAssertTree(t, prepared.StagePath, prepared.CandidateTree)
					recoveryAssertTree(t, prepared.BackupPath, opaque)
				}
			})
		}
	}
}

func checkpointRecoveryAssertRootChurnCause(t *testing.T, kind string, err error) {
	t.Helper()
	if kind == "checkout-rebind" && !errors.Is(err, ErrWorkingTreeChanged) {
		t.Fatalf("root churn cause = %v, want %v", err, ErrWorkingTreeChanged)
	}
	if kind == "private-mode" && !errors.Is(err, ErrCheckpointUnsupported) {
		t.Fatalf("root churn cause = %v, want %v", err, ErrCheckpointUnsupported)
	}
	if kind == "private-rebind" && errors.Is(err, ErrCheckpointUnsupported) {
		t.Fatalf("root churn cause = %v, do not classify identity rebind as unsupported", err)
	}
}

func checkpointRecoveryApplyRootChurn(t *testing.T, kind, checkoutRoot, privateRoot string) func() {
	t.Helper()
	switch kind {
	case "checkout-rebind":
		moved := checkoutRoot + ".checkpoint-root-race"
		if err := os.Rename(checkoutRoot, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(checkoutRoot, 0o755); err != nil {
			t.Fatal(err)
		}
		return func() {
			if err := os.RemoveAll(checkoutRoot); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(moved, checkoutRoot); err != nil {
				t.Fatal(err)
			}
		}
	case "private-rebind":
		moved := privateRoot + ".checkpoint-root-race"
		if err := os.Rename(privateRoot, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(privateRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		return func() {
			if err := os.RemoveAll(privateRoot); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(moved, privateRoot); err != nil {
				t.Fatal(err)
			}
		}
	case "private-mode":
		if err := os.Chmod(privateRoot, 0o755); err != nil {
			t.Fatal(err)
		}
		return func() {
			if err := os.Chmod(privateRoot, 0o700); err != nil {
				t.Fatal(err)
			}
		}
	default:
		t.Fatalf("unknown root churn %q", kind)
		return func() {}
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
			if err != nil || !reflect.DeepEqual(*fsyncOrder, []string{"checkout", "private"}) {
				t.Fatalf("Recover published=(%+v,%v), fsync=%v", got, err, *fsyncOrder)
			}
			after := recoveryDatabaseState(t, fixture.service, fixture.request.Scope)
			recoveryAssertNewDatabase(t, before, after, false)
			recoveryAssertTree(t, fixture.livePath(), beforeLive)
			recoveryAssertTree(t, fixture.journal.BackupPath, beforeBackup)
			checkpointFallbackAssertAbsent(t, fixture.journal.StagePath)
			recoveryAssertReturnedStatus(t, fixture.service, fixture.request.Scope, got)
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
	if err != nil || !reflect.DeepEqual(*fsyncOrder, []string{"checkout", "private"}) {
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
	recoveryAssertReturnedStatus(t, fixture.service, fixture.request.Scope, got)
}

func TestRecoverRestartConvergesEveryListedPublisherBoundary(t *testing.T) {
	tests := []struct {
		name         string
		stage        checkpointArtifactFaultStage
		occurrence   int
		compensation bool
		wantNew      bool
	}{
		{name: "before publisher live to backup", stage: checkpointArtifactBeforeLiveMutation, occurrence: 1},
		{name: "after publisher live to backup", stage: checkpointArtifactAfterLiveMutation, occurrence: 1},
		{name: "before backup private destination fsync", stage: checkpointArtifactBeforePrivateParentFsync, occurrence: 1},
		{name: "after backup private destination fsync", stage: checkpointArtifactAfterPrivateParentFsync, occurrence: 1},
		{name: "before backup checkout source fsync", stage: checkpointArtifactBeforeLiveParentFsync, occurrence: 1},
		{name: "after backup checkout source fsync", stage: checkpointArtifactAfterLiveParentFsync, occurrence: 1},
		{name: "before publisher stage to live", stage: checkpointArtifactBeforeSecondLiveMutation, occurrence: 1},
		{name: "after publisher stage to live", stage: checkpointArtifactAfterSecondLiveMutation, occurrence: 1, wantNew: true},
		{name: "before publication checkout destination fsync", stage: checkpointArtifactBeforeLiveParentFsync, occurrence: 2, wantNew: true},
		{name: "after publication checkout destination fsync", stage: checkpointArtifactAfterLiveParentFsync, occurrence: 2, wantNew: true},
		{name: "before publication private source fsync", stage: checkpointArtifactBeforePrivateParentFsync, occurrence: 2, wantNew: true},
		{name: "after publication private source fsync", stage: checkpointArtifactAfterPrivateParentFsync, occurrence: 2, wantNew: true},
		{name: "before publisher compensation", stage: checkpointArtifactBeforeSecondLiveMutation, occurrence: 1, compensation: true},
		{name: "after publisher compensation", stage: checkpointArtifactAfterSecondLiveMutation, occurrence: 1, compensation: true},
		{name: "before compensation checkout destination fsync", stage: checkpointArtifactBeforeLiveParentFsync, occurrence: 2, compensation: true},
		{name: "after compensation checkout destination fsync", stage: checkpointArtifactAfterLiveParentFsync, occurrence: 2, compensation: true},
		{name: "before compensation private source fsync", stage: checkpointArtifactBeforePrivateParentFsync, occurrence: 2, compensation: true},
		{name: "after compensation private source fsync", stage: checkpointArtifactAfterPrivateParentFsync, occurrence: 2, compensation: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, request := newCheckpointRecoveryPublisherFixture(t)
			acceptedBefore := recoveryDatabaseState(t, fixture.service, request.Scope).workspace.Binding
			injected := errors.New("publisher restart boundary")
			opaque := checkpointRecoveryOpaqueTree()
			checkpointRecoveryUseRealPublisher(t, fixture, func(artifact *checkpointArtifact) {
				if test.compensation {
					opaque = checkpointFallbackOpaqueTree(artifact.proof.prior.tree, "restart compensation")
				}
				evidence := artifact.evidence()
				seen := make(map[checkpointArtifactFaultStage]int)
				artifact.dependencies.fault = func(stage checkpointArtifactFaultStage) error {
					seen[stage]++
					if test.compensation && stage == checkpointArtifactAfterLiveParentFsync && seen[stage] == 1 {
						checkpointFallbackWriteTree(t, evidence.BackupPath, opaque)
					}
					if stage == test.stage && seen[stage] == test.occurrence {
						return injected
					}
					return nil
				}
			})

			gotCheckpoint, checkpointErr := fixture.service.Checkpoint(context.Background(), request)
			if gotCheckpoint != (CheckpointResult{}) || !errors.Is(checkpointErr, injected) {
				t.Fatalf("interrupted Checkpoint=(%+v,%v), want zero and injected boundary", gotCheckpoint, checkpointErr)
			}
			beforeRecovery := recoveryDatabaseState(t, fixture.service, request.Scope)
			checkpointRecoveryAssertPreparedDatabase(t, beforeRecovery)
			journal := beforeRecovery.disposition.Journals[0]
			wantLive := journal.PriorTree
			wantStage, wantBackup := true, false
			if test.compensation {
				wantLive = opaque
			}
			if test.wantNew {
				laterLive := state.Tree{{Path: "later-live.txt", Data: []byte("later live bytes after publisher interruption\n")}}
				checkpointFallbackReplaceTree(t, filepath.Join(request.Root, ".wormhole"), laterLive, ".retained-published")
				wantLive, wantStage, wantBackup = laterLive, false, true
				checkpointRecoveryAddLaterOperation(t, fixture.service, request.Scope, journal)
				beforeRecovery = recoveryDatabaseState(t, fixture.service, request.Scope)
			}

			checkpointRecoveryRestartService(t, fixture)
			restarted := checkpointRecoveryLinuxFixture{checkpointCoordinatorFixture: fixture, request: request, journal: journal}
			fsyncOrder := checkpointRecoveryInstallFsyncRecorder(t, restarted)
			got, err := fixture.service.Recover(context.Background(), request.Scope)
			if err != nil || !reflect.DeepEqual(*fsyncOrder, []string{"checkout", "private"}) {
				t.Fatalf("restarted Recover=(%+v,%v), fsync=%v", got, err, *fsyncOrder)
			}
			after := recoveryDatabaseState(t, fixture.service, request.Scope)
			if test.wantNew {
				recoveryAssertNewDatabase(t, beforeRecovery, after, true)
			} else {
				recoveryAssertOldDatabase(t, beforeRecovery, after)
			}
			if after.workspace.Binding != acceptedBefore {
				t.Fatalf("restart moved accepted binding\nbefore=%+v\nafter=%+v", acceptedBefore, after.workspace.Binding)
			}
			recoveryAssertTree(t, filepath.Join(request.Root, ".wormhole"), wantLive)
			recoveryAssertPath(t, journal.StagePath, wantStage, journal.CandidateTree)
			recoveryAssertPath(t, journal.BackupPath, wantBackup, journal.PriorTree)
			recoveryAssertReturnedStatus(t, fixture.service, request.Scope, got)
		})
	}
}

func TestRecoverRestartConvergesRecoveryBackupToLiveBoundaries(t *testing.T) {
	boundaries := []struct {
		name                      string
		renamePrior               bool
		interruptClassification   bool
		fault                     checkpointArtifactFaultStage
		wantFirstFsync            []string
		wantRestartRenameAttempts int
	}{
		{name: "before applied rename", renamePrior: true, wantRestartRenameAttempts: 1},
		{name: "after applied rename", interruptClassification: true},
		{name: "before checkout destination fsync", fault: checkpointArtifactBeforeLiveParentFsync},
		{name: "after checkout destination fsync", fault: checkpointArtifactAfterLiveParentFsync, wantFirstFsync: []string{"checkout"}},
		{name: "before private source fsync", fault: checkpointArtifactBeforePrivateParentFsync, wantFirstFsync: []string{"checkout"}},
		{name: "after private source fsync", fault: checkpointArtifactAfterPrivateParentFsync, wantFirstFsync: []string{"checkout", "private"}},
	}
	for _, source := range []string{"prior", "opaque"} {
		for _, boundary := range boundaries {
			t.Run(source+"/"+boundary.name, func(t *testing.T) {
				fixture := newCheckpointRecoveryLinuxFixture(t, "prepared")
				wantLive := fixture.journal.PriorTree
				if source == "opaque" {
					wantLive = checkpointRecoveryOpaqueTree()
					checkpointFallbackReplaceTree(t, fixture.livePath(), wantLive, ".retained-prior")
				}
				if err := os.Rename(fixture.livePath(), fixture.journal.BackupPath); err != nil {
					t.Fatal(err)
				}
				before := recoveryDatabaseState(t, fixture.service, fixture.request.Scope)
				acceptedBefore := before.workspace.Binding
				firstService := fixture.service
				injected := errors.New("recovery restart boundary")

				operations := normalizeCheckpointArtifactRenameOperations(defaultCheckpointArtifactPlatformOperations())
				realRename, realFstatat, realFsync := operations.rename, operations.fstatat, operations.fsync
				firstRenameAttempts := 0
				renameApplied := false
				operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
					firstRenameAttempts++
					if boundary.renamePrior {
						return injected
					}
					if err := realRename(fromFD, from, toFD, to, flags); err != nil {
						return err
					}
					renameApplied = true
					if boundary.interruptClassification {
						return injected
					}
					return nil
				}
				classificationInterrupted := false
				operations.fstatat = func(fd int, name string, stat *unix.Stat_t, flags int) error {
					if boundary.interruptClassification && renameApplied && !classificationInterrupted {
						classificationInterrupted = true
						return injected
					}
					return realFstatat(fd, name, stat, flags)
				}
				checkoutIdentity := checkpointRecoveryDirectoryIdentity(t, fixture.request.Root)
				privateIdentity := checkpointRecoveryDirectoryIdentity(t, filepath.Dir(fixture.journal.StagePath))
				var firstFsync []string
				operations.fsync = func(fd int) error {
					var stat unix.Stat_t
					if err := unix.Fstat(fd, &stat); err != nil {
						return err
					}
					switch [2]uint64{uint64(stat.Dev), stat.Ino} {
					case checkoutIdentity:
						firstFsync = append(firstFsync, "checkout")
					case privateIdentity:
						firstFsync = append(firstFsync, "private")
					default:
						t.Fatalf("first recovery fsync unexpected directory dev=%d ino=%d", stat.Dev, stat.Ino)
					}
					return realFsync(fd)
				}
				dependencies := checkpointArtifactDependencies{
					readGit: readOnlyGitLimited,
					fault: func(stage checkpointArtifactFaultStage) error {
						if boundary.fault != 0 && stage == boundary.fault {
							return injected
						}
						return nil
					},
					operations: operations,
				}
				fixture.service.recoverCheckpointFilesystem = func(ctx context.Context, proof checkpointRecoveryProof) (checkpointRecoveryFilesystemOutcome, error) {
					return recoverCheckpointFilesystemWithDependencies(ctx, proof, dependencies)
				}

				got, err := fixture.service.Recover(context.Background(), fixture.request.Scope)
				if !reflect.DeepEqual(got, WorkspaceStatus{}) || !errors.Is(err, injected) || firstRenameAttempts != 1 ||
					!reflect.DeepEqual(firstFsync, boundary.wantFirstFsync) {
					t.Fatalf("interrupted recovery=(%+v,%v), renames=%d fsync=%v", got, err, firstRenameAttempts, firstFsync)
				}
				if afterFirst := recoveryDatabaseState(t, fixture.service, fixture.request.Scope); !reflect.DeepEqual(afterFirst, before) {
					t.Fatalf("interrupted recovery changed prepared database\nbefore=%+v\nafter=%+v", before, afterFirst)
				}
				if boundary.renamePrior {
					checkpointFallbackAssertAbsent(t, fixture.livePath())
					recoveryAssertTree(t, fixture.journal.BackupPath, wantLive)
				} else {
					recoveryAssertTree(t, fixture.livePath(), wantLive)
					checkpointFallbackAssertAbsent(t, fixture.journal.BackupPath)
				}
				recoveryAssertTree(t, fixture.journal.StagePath, fixture.journal.CandidateTree)

				checkpointRecoveryRestartService(t, fixture.checkpointCoordinatorFixture)
				if fixture.service == firstService {
					t.Fatal("recovery restart reused the interrupted service")
				}
				restartOperations := normalizeCheckpointArtifactRenameOperations(defaultCheckpointArtifactPlatformOperations())
				restartRename, restartFsync := restartOperations.rename, restartOperations.fsync
				restartRenameAttempts := 0
				restartOperations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
					restartRenameAttempts++
					return restartRename(fromFD, from, toFD, to, flags)
				}
				var restartFsyncOrder []string
				restartOperations.fsync = func(fd int) error {
					var stat unix.Stat_t
					if err := unix.Fstat(fd, &stat); err != nil {
						return err
					}
					switch [2]uint64{uint64(stat.Dev), stat.Ino} {
					case checkoutIdentity:
						restartFsyncOrder = append(restartFsyncOrder, "checkout")
					case privateIdentity:
						restartFsyncOrder = append(restartFsyncOrder, "private")
					default:
						t.Fatalf("restarted recovery fsync unexpected directory dev=%d ino=%d", stat.Dev, stat.Ino)
					}
					return restartFsync(fd)
				}
				fixture.service.recoverCheckpointFilesystem = func(ctx context.Context, proof checkpointRecoveryProof) (checkpointRecoveryFilesystemOutcome, error) {
					return recoverCheckpointFilesystemWithDependencies(ctx, proof, checkpointArtifactDependencies{
						readGit: readOnlyGitLimited, operations: restartOperations,
					})
				}

				got, err = fixture.service.Recover(context.Background(), fixture.request.Scope)
				if err != nil || restartRenameAttempts != boundary.wantRestartRenameAttempts ||
					!reflect.DeepEqual(restartFsyncOrder, []string{"checkout", "private"}) {
					t.Fatalf("restarted recovery=(%+v,%v), renames=%d fsync=%v", got, err, restartRenameAttempts, restartFsyncOrder)
				}
				after := recoveryDatabaseState(t, fixture.service, fixture.request.Scope)
				recoveryAssertOldDatabase(t, before, after)
				if after.workspace.Binding != acceptedBefore {
					t.Fatalf("restarted recovery moved accepted binding\nbefore=%+v\nafter=%+v", acceptedBefore, after.workspace.Binding)
				}
				recoveryAssertTree(t, fixture.livePath(), wantLive)
				recoveryAssertTree(t, fixture.journal.StagePath, fixture.journal.CandidateTree)
				checkpointFallbackAssertAbsent(t, fixture.journal.BackupPath)
				recoveryAssertReturnedStatus(t, fixture.service, fixture.request.Scope, got)
			})
		}
	}
}

func TestRecoverFsyncFailureRetainsPreparedThenConvergesOnRetry(t *testing.T) {
	for _, source := range []string{"prior", "opaque"} {
		for _, failRole := range []string{"checkout", "private"} {
			t.Run(source+"/"+failRole, func(t *testing.T) {
				fixture := newCheckpointRecoveryLinuxFixture(t, "prepared")
				wantLive := fixture.journal.PriorTree
				if source == "opaque" {
					wantLive = checkpointRecoveryOpaqueTree()
					checkpointFallbackReplaceTree(t, fixture.livePath(), wantLive, ".retained-prior")
				}
				if err := os.Rename(fixture.livePath(), fixture.journal.BackupPath); err != nil {
					t.Fatal(err)
				}
				before := recoveryDatabaseState(t, fixture.service, fixture.request.Scope)
				acceptedBefore := before.workspace.Binding
				operations := normalizeCheckpointArtifactRenameOperations(defaultCheckpointArtifactPlatformOperations())
				realRename, realFsync := operations.rename, operations.fsync
				renames := 0
				operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
					renames++
					return realRename(fromFD, from, toFD, to, flags)
				}
				checkoutIdentity := checkpointRecoveryDirectoryIdentity(t, fixture.request.Root)
				privateIdentity := checkpointRecoveryDirectoryIdentity(t, filepath.Dir(fixture.journal.StagePath))
				injected := errors.New("recovery parent fsync failure")
				var fsyncOrder []string
				operations.fsync = func(fd int) error {
					var stat unix.Stat_t
					if err := unix.Fstat(fd, &stat); err != nil {
						return err
					}
					role := ""
					switch [2]uint64{uint64(stat.Dev), stat.Ino} {
					case checkoutIdentity:
						role = "checkout"
					case privateIdentity:
						role = "private"
					}
					if role != "" {
						fsyncOrder = append(fsyncOrder, role)
						if role == failRole {
							return injected
						}
					}
					return realFsync(fd)
				}
				fixture.service.recoverCheckpointFilesystem = func(ctx context.Context, proof checkpointRecoveryProof) (checkpointRecoveryFilesystemOutcome, error) {
					return recoverCheckpointFilesystemWithDependencies(ctx, proof, checkpointArtifactDependencies{readGit: readOnlyGitLimited, operations: operations})
				}

				got, err := fixture.service.Recover(context.Background(), fixture.request.Scope)
				if !reflect.DeepEqual(got, WorkspaceStatus{}) || !errors.Is(err, injected) || errors.Is(err, ErrCheckpointUnsupported) || renames != 1 {
					t.Fatalf("fsync-failed Recover=(%+v,%v), renames=%d", got, err, renames)
				}
				if failRole == "checkout" && !reflect.DeepEqual(fsyncOrder, []string{"checkout"}) {
					t.Fatalf("checkout fsync failure order=%v", fsyncOrder)
				}
				if failRole == "private" && !reflect.DeepEqual(fsyncOrder, []string{"checkout", "private"}) {
					t.Fatalf("private fsync failure order=%v", fsyncOrder)
				}
				if afterFailure := recoveryDatabaseState(t, fixture.service, fixture.request.Scope); !reflect.DeepEqual(afterFailure, before) {
					t.Fatal("recovery fsync failure changed prepared database authority")
				}
				recoveryAssertTree(t, fixture.livePath(), wantLive)
				recoveryAssertTree(t, fixture.journal.StagePath, fixture.journal.CandidateTree)
				checkpointFallbackAssertAbsent(t, fixture.journal.BackupPath)

				checkpointRecoveryRestartService(t, fixture.checkpointCoordinatorFixture)
				fsyncRetry := checkpointRecoveryInstallFsyncRecorder(t, fixture)
				got, err = fixture.service.Recover(context.Background(), fixture.request.Scope)
				if err != nil || !reflect.DeepEqual(*fsyncRetry, []string{"checkout", "private"}) {
					t.Fatalf("fsync retry Recover=(%+v,%v), fsync=%v", got, err, *fsyncRetry)
				}
				after := recoveryDatabaseState(t, fixture.service, fixture.request.Scope)
				recoveryAssertOldDatabase(t, before, after)
				if after.workspace.Binding != acceptedBefore || renames != 1 {
					t.Fatalf("fsync retry moved accepted binding or replayed rename: binding=%+v renames=%d", after.workspace.Binding, renames)
				}
				recoveryAssertTree(t, fixture.livePath(), wantLive)
				recoveryAssertTree(t, fixture.journal.StagePath, fixture.journal.CandidateTree)
				checkpointFallbackAssertAbsent(t, fixture.journal.BackupPath)
				recoveryAssertReturnedStatus(t, fixture.service, fixture.request.Scope, got)
			})
		}
	}
}

func TestRecoverRenameResultClassifiesPriorNextThirdWithoutReplay(t *testing.T) {
	for _, source := range []string{"prior", "opaque"} {
		for _, outcome := range []string{"prior", "next", "third"} {
			t.Run(source+"/"+outcome, func(t *testing.T) {
				checkpointRecoveryExerciseRenameResult(t, source, outcome)
			})
		}
	}
}

func TestCheckpointAndRecoverAllRenameRolesClassifyPriorNextThirdWithoutReplay(t *testing.T) {
	for _, role := range []string{"publisher live to backup", "publisher stage to live", "publisher compensation"} {
		for _, outcome := range []string{"prior", "next", "third"} {
			t.Run(role+"/"+outcome, func(t *testing.T) {
				checkpointRecoveryExercisePublisherRenameResult(t, role, outcome)
			})
		}
	}
	for _, outcome := range []string{"prior", "next", "third"} {
		t.Run("recovery compensation/"+outcome, func(t *testing.T) {
			checkpointRecoveryExerciseRenameResult(t, "opaque", outcome)
		})
	}
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

func TestRecoverClassifiesPersistentRootsAsPreconditionsAndContainedEvidenceAsBlocked(t *testing.T) {
	for _, test := range []struct {
		name         string
		dependencies func(*testing.T, checkpointRecoveryLinuxFixture, error) checkpointArtifactDependencies
		want         error
		unwanted     error
	}{
		{
			name: "initial root mount proof",
			dependencies: func(_ *testing.T, _ checkpointRecoveryLinuxFixture, cause error) checkpointArtifactDependencies {
				return checkpointArtifactDependencies{mount: checkpointArtifactMountOperations{
					statx: func(int, string, int, int, *unix.Statx_t) error { return cause },
				}}
			},
			want: ErrCheckpointRecoveryPrecondition, unwanted: ErrCheckpointRecoveryBlocked,
		},
		{
			name: "persistent Git path reread",
			dependencies: func(_ *testing.T, _ checkpointRecoveryLinuxFixture, cause error) checkpointArtifactDependencies {
				reads := 0
				return checkpointArtifactDependencies{readGit: func(ctx context.Context, root string, limit int, args ...string) ([]byte, error) {
					reads++
					if reads > 3 {
						return nil, cause
					}
					return readOnlyGitLimited(ctx, root, limit, args...)
				}}
			},
			want: ErrCheckpointRecoveryPrecondition, unwanted: ErrCheckpointRecoveryBlocked,
		},
		{
			name: "classifier tail root reread",
			dependencies: func(_ *testing.T, _ checkpointRecoveryLinuxFixture, cause error) checkpointArtifactDependencies {
				reads := 0
				return checkpointArtifactDependencies{readGit: func(ctx context.Context, root string, limit int, args ...string) ([]byte, error) {
					reads++
					if reads == 7 {
						return nil, cause
					}
					return readOnlyGitLimited(ctx, root, limit, args...)
				}}
			},
			want: ErrCheckpointRecoveryPrecondition, unwanted: ErrCheckpointRecoveryBlocked,
		},
		{
			name: "transient Git path parent validation",
			dependencies: func(t *testing.T, fixture checkpointRecoveryLinuxFixture, cause error) checkpointArtifactDependencies {
				privateParentIdentity := checkpointRecoveryDirectoryIdentity(t, filepath.Dir(filepath.Dir(fixture.journal.StagePath)))
				privateRootName := filepath.Base(filepath.Dir(fixture.journal.StagePath))
				operations := defaultCheckpointArtifactPlatformOperations()
				realFstatat := operations.fstatat
				privateRootOpened := false
				operations.fstatat = func(fd int, name string, stat *unix.Stat_t, flags int) error {
					err := realFstatat(fd, name, stat, flags)
					if err != nil || privateRootOpened || name != privateRootName {
						return err
					}
					var parent unix.Stat_t
					if err := unix.Fstat(fd, &parent); err != nil {
						t.Fatal(err)
					}
					privateRootOpened = [2]uint64{uint64(parent.Dev), parent.Ino} == privateParentIdentity
					return nil
				}
				failed := false
				return checkpointArtifactDependencies{readGit: func(ctx context.Context, root string, limit int, args ...string) ([]byte, error) {
					if privateRootOpened && !failed {
						failed = true
						return nil, cause
					}
					return readOnlyGitLimited(ctx, root, limit, args...)
				}, operations: operations}
			},
			want: ErrCheckpointRecoveryPrecondition, unwanted: ErrCheckpointRecoveryBlocked,
		},
		{
			name: "contained stage inspection",
			dependencies: func(_ *testing.T, fixture checkpointRecoveryLinuxFixture, cause error) checkpointArtifactDependencies {
				operations := defaultCheckpointArtifactPlatformOperations()
				realFstatat := operations.fstatat
				stageName := filepath.Base(fixture.journal.StagePath)
				operations.fstatat = func(fd int, name string, stat *unix.Stat_t, flags int) error {
					if name == stageName {
						return cause
					}
					return realFstatat(fd, name, stat, flags)
				}
				return checkpointArtifactDependencies{readGit: readOnlyGitLimited, operations: operations}
			},
			want: ErrCheckpointRecoveryBlocked, unwanted: ErrCheckpointRecoveryPrecondition,
		},
		{
			name: "contained nested capture",
			dependencies: func(t *testing.T, fixture checkpointRecoveryLinuxFixture, cause error) checkpointArtifactDependencies {
				targetPath := filepath.Join(fixture.journal.StagePath, "state", "v1", "project.json")
				var target unix.Stat_t
				if err := unix.Stat(targetPath, &target); err != nil {
					t.Fatal(err)
				}
				return checkpointArtifactDependencies{readGit: readOnlyGitLimited, mount: checkpointArtifactMountOperations{
					statx: func(fd int, path string, flags, mask int, stat *unix.Statx_t) error {
						var opened unix.Stat_t
						if err := unix.Fstat(fd, &opened); err != nil {
							return err
						}
						if opened.Dev == target.Dev && opened.Ino == target.Ino {
							return cause
						}
						return unix.Statx(fd, path, flags, mask, stat)
					},
				}}
			},
			want: ErrCheckpointRecoveryBlocked, unwanted: ErrCheckpointRecoveryPrecondition,
		},
		{
			name: "contained final entry recheck",
			dependencies: func(_ *testing.T, fixture checkpointRecoveryLinuxFixture, cause error) checkpointArtifactDependencies {
				operations := defaultCheckpointArtifactPlatformOperations()
				realFstatat := operations.fstatat
				stageName := filepath.Base(fixture.journal.StagePath)
				stageStats := 0
				operations.fstatat = func(fd int, name string, stat *unix.Stat_t, flags int) error {
					if name == stageName {
						stageStats++
						if stageStats == 2 {
							return cause
						}
					}
					return realFstatat(fd, name, stat, flags)
				}
				return checkpointArtifactDependencies{readGit: readOnlyGitLimited, operations: operations}
			},
			want: ErrCheckpointRecoveryBlocked, unwanted: ErrCheckpointRecoveryPrecondition,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCheckpointRecoveryLinuxFixture(t, "prepared")
			cause := errors.New("injected " + test.name)
			beforeDatabase := recoveryDatabaseState(t, fixture.service, fixture.request.Scope)
			beforePaths := recoveryScopePaths(t, beforeDatabase)
			dependencies := test.dependencies(t, fixture, cause)
			fixture.service.recoverCheckpointFilesystem = func(ctx context.Context, proof checkpointRecoveryProof) (checkpointRecoveryFilesystemOutcome, error) {
				return recoverCheckpointFilesystemWithDependencies(ctx, proof, dependencies)
			}

			got, err := fixture.service.Recover(context.Background(), fixture.request.Scope)
			if !reflect.DeepEqual(got, WorkspaceStatus{}) || !errors.Is(err, test.want) || errors.Is(err, test.unwanted) || !errors.Is(err, cause) {
				t.Fatalf("Recover taxonomy=(%+v,%v), want zero, %v, cause %v, not %v", got, err, test.want, cause, test.unwanted)
			}
			if after := recoveryDatabaseState(t, fixture.service, fixture.request.Scope); !reflect.DeepEqual(after, beforeDatabase) {
				t.Fatal("taxonomy failure changed database state")
			}
			if after := recoveryScopePaths(t, beforeDatabase); !reflect.DeepEqual(after, beforePaths) {
				t.Fatal("taxonomy failure changed filesystem evidence")
			}
		})
	}
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
	operations := normalizeCheckpointArtifactRenameOperations(defaultCheckpointArtifactPlatformOperations())
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

func newCheckpointRecoveryPublisherFixture(t *testing.T) (*checkpointCoordinatorFixture, CheckpointRequest) {
	t.Helper()
	fixture, request, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
	accepted := fixture.mustAcceptedSnapshot(t)
	operation := servicePutTaskOperation(
		accepted,
		"99999999-9999-4999-8999-999999999991",
		"22222222-2222-4222-8222-222222222222",
		"recovery publisher candidate",
	)
	if _, err := fixture.service.Apply(context.Background(), request.Scope, operation); err != nil {
		t.Fatal(err)
	}
	setServiceWorkspaceState(t, fixture.store, request.Scope, "clean")
	return fixture, request
}

func checkpointRecoveryUseRealPublisher(
	t *testing.T,
	fixture *checkpointCoordinatorFixture,
	configure func(*checkpointArtifact),
) {
	t.Helper()
	fixture.service.prepareCheckpointArtifact = func(ctx context.Context, input checkpointArtifactInput) (checkpointArtifactHandle, error) {
		artifact, err := prepareCheckpointArtifactWithDependencies(ctx, input, checkpointArtifactDependencies{readGit: readOnlyGitLimited})
		if err != nil {
			return checkpointArtifactHandle{}, err
		}
		if configure != nil {
			configure(artifact)
		}
		return checkpointArtifactHandle{
			evidence: artifact.evidence(),
			publish: func(ctx context.Context) (checkpointPublicationDisposition, error) {
				return publishPreparedCheckpointArtifact(ctx, artifact)
			},
			close: artifact.close,
		}, nil
	}
}

func checkpointRecoveryRestartService(t *testing.T, fixture *checkpointCoordinatorFixture) {
	t.Helper()
	restarted, err := NewService(fixture.service.repo, ServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	fixture.service = restarted
}

func checkpointRecoveryAddLaterOperation(
	t *testing.T,
	service *Service,
	scope types.WorkspaceScope,
	journal localstore.WorkspaceMaterializationRecord,
) {
	t.Helper()
	candidate, err := state.DecodeTree(cloneCheckpointTree(journal.CandidateTree))
	if err != nil {
		t.Fatal(err)
	}
	operation := servicePutTaskOperation(
		candidate,
		"99999999-9999-4999-8999-999999999992",
		"22222222-2222-4222-8222-222222222223",
		"later operation retained across recovery",
	)
	if _, err := service.Apply(context.Background(), scope, operation); err != nil {
		t.Fatal(err)
	}
}

func checkpointRecoveryAssertPreparedDatabase(t *testing.T, database checkpointRecoveryDatabaseState) {
	t.Helper()
	if len(database.disposition.Journals) != 1 || database.disposition.Journals[0].State != "prepared" {
		t.Fatalf("prepared recovery database disposition=%+v", database.disposition)
	}
}

func checkpointRecoveryAssertCheckpointOutcomeDatabase(
	t *testing.T,
	before, after checkpointRecoveryDatabaseState,
	expectedPrepared localstore.WorkspaceMaterializationRecord,
	wantState string,
) {
	t.Helper()
	if len(after.disposition.Journals) != 1 {
		t.Fatalf("checkpoint outcome journals=%d, want 1", len(after.disposition.Journals))
	}
	journal := after.disposition.Journals[0]
	if journal.State != wantState || !recoveryJournalEqualExceptState(expectedPrepared, journal) {
		t.Fatalf("checkpoint journal differs from frozen prepared input\nexpected=%+v\nafter=%+v", expectedPrepared, journal)
	}
	prepared := before
	prepared.disposition = cloneImportDisposition(before.disposition)
	prepared.disposition.Journals = []localstore.WorkspaceMaterializationRecord{cloneMaterializationRecord(expectedPrepared)}
	switch wantState {
	case "prepared":
		if !reflect.DeepEqual(after.workspace, before.workspace) ||
			!reflect.DeepEqual(after.candidate, before.candidate) ||
			!reflect.DeepEqual(after.disposition.Operations, before.disposition.Operations) {
			t.Fatalf("prepared checkpoint database mismatch\nbefore=%+v\nafter=%+v", before, after)
		}
	case "published":
		normalized := after
		normalized.disposition = cloneImportDisposition(after.disposition)
		normalized.disposition.Journals[0].State = "recovered_new"
		recoveryAssertNewDatabase(t, prepared, normalized, true)
		expectedOperations := make([]localstore.WorkspaceOperation, len(before.disposition.Operations))
		for index, operation := range before.disposition.Operations {
			expectedOperations[index] = cloneImportOperation(operation)
			if operation.Generation <= expectedPrepared.ThroughGeneration {
				expectedOperations[index].State = "materialized"
			}
		}
		if !reflect.DeepEqual(after.disposition.Operations, expectedOperations) {
			t.Fatalf("published checkpoint operations differ from frozen input\nexpected=%+v\nafter=%+v", expectedOperations, after.disposition.Operations)
		}
	case "recovered_old":
		recoveryAssertOldDatabase(t, prepared, after)
	default:
		t.Fatalf("unknown checkpoint outcome state %q", wantState)
	}
}

func checkpointRecoveryCapturePreparedJournal(
	t *testing.T,
	fixture *checkpointCoordinatorFixture,
	expected *localstore.WorkspaceMaterializationRecord,
) {
	t.Helper()
	realWithImmediate := fixture.service.withImmediateWorkspace
	transactions := 0
	fixture.service.withImmediateWorkspace = func(
		ctx context.Context,
		scope types.WorkspaceScope,
		fn func(*localstore.WorkspaceMutationTx) error,
	) error {
		transactions++
		transaction := transactions
		return realWithImmediate(ctx, scope, func(tx *localstore.WorkspaceMutationTx) error {
			if err := fn(tx); err != nil {
				return err
			}
			if transaction != 1 {
				return nil
			}
			disposition, err := tx.MaterializationDisposition(ctx)
			if err != nil {
				return err
			}
			if len(disposition.Journals) != 1 || disposition.Journals[0].State != "prepared" {
				return errors.New("publisher expectation did not capture one prepared journal")
			}
			*expected = cloneMaterializationRecord(disposition.Journals[0])
			return nil
		})
	}
}

func checkpointRecoveryAssertPreparedArtifactInput(
	t *testing.T,
	prepared localstore.WorkspaceMaterializationRecord,
	input checkpointArtifactInput,
	evidence checkpointArtifactEvidence,
) {
	t.Helper()
	if prepared.State != "prepared" || prepared.JournalID != evidence.JournalID ||
		prepared.StagePath != evidence.StagePath || prepared.BackupPath != evidence.BackupPath ||
		prepared.Checkout != input.Checkout || prepared.ExpectedLiveDigest != input.PriorTreeDigest ||
		prepared.PriorTreeDigest != input.PriorTreeDigest || prepared.CandidateDigest != input.CandidateDigest ||
		!sameCheckpointArtifactTree(prepared.PriorTree, input.PriorTree) ||
		!sameCheckpointArtifactTree(prepared.CandidateTree, input.CandidateTree) {
		t.Fatalf("frozen prepared journal differs from pre-publication artifact input\nprepared=%+v\ninput=%+v\nevidence=%+v", prepared, input, evidence)
	}
}

func checkpointRecoveryExerciseRenameResult(t *testing.T, source, outcome string) {
	t.Helper()
	fixture := newCheckpointRecoveryLinuxFixture(t, "prepared")
	wantSource := fixture.journal.PriorTree
	if source == "opaque" {
		wantSource = checkpointRecoveryOpaqueTree()
		checkpointFallbackReplaceTree(t, fixture.livePath(), wantSource, ".retained-prior")
	}
	if err := os.Rename(fixture.livePath(), fixture.journal.BackupPath); err != nil {
		t.Fatal(err)
	}
	before := recoveryDatabaseState(t, fixture.service, fixture.request.Scope)
	acceptedBefore := before.workspace.Binding
	third := state.Tree{{Path: "third.txt", Data: []byte("third recovery rename topology\n")}}
	injected := errors.New("recovery backup-to-live rename uncertainty")
	operations := normalizeCheckpointArtifactRenameOperations(defaultCheckpointArtifactPlatformOperations())
	realRename, realFsync := operations.rename, operations.fsync
	renames := 0
	operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
		renames++
		switch outcome {
		case "prior":
			return injected
		case "next":
			if err := realRename(fromFD, from, toFD, to, flags); err != nil {
				return err
			}
			return injected
		case "third":
			if err := realRename(fromFD, from, toFD, to, flags); err != nil {
				return err
			}
			checkpointFallbackCreateTree(t, fixture.journal.BackupPath, third)
			return injected
		default:
			panic("unknown recovery rename outcome")
		}
	}
	checkoutIdentity := checkpointRecoveryDirectoryIdentity(t, fixture.request.Root)
	privateIdentity := checkpointRecoveryDirectoryIdentity(t, filepath.Dir(fixture.journal.StagePath))
	var fsyncOrder []string
	operations.fsync = func(fd int) error {
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			return err
		}
		switch [2]uint64{uint64(stat.Dev), stat.Ino} {
		case checkoutIdentity:
			fsyncOrder = append(fsyncOrder, "checkout")
		case privateIdentity:
			fsyncOrder = append(fsyncOrder, "private")
		}
		return realFsync(fd)
	}
	fixture.service.recoverCheckpointFilesystem = func(ctx context.Context, proof checkpointRecoveryProof) (checkpointRecoveryFilesystemOutcome, error) {
		return recoverCheckpointFilesystemWithDependencies(ctx, proof, checkpointArtifactDependencies{readGit: readOnlyGitLimited, operations: operations})
	}

	got, err := fixture.service.Recover(context.Background(), fixture.request.Scope)
	after := recoveryDatabaseState(t, fixture.service, fixture.request.Scope)
	switch outcome {
	case "prior":
		if !reflect.DeepEqual(got, WorkspaceStatus{}) || !errors.Is(err, injected) || errors.Is(err, ErrCheckpointRecoveryBlocked) || !reflect.DeepEqual(after, before) || len(fsyncOrder) != 0 {
			t.Fatalf("recovery rename prior=(%+v,%v), fsync=%v", got, err, fsyncOrder)
		}
		checkpointFallbackAssertAbsent(t, fixture.livePath())
		recoveryAssertTree(t, fixture.journal.BackupPath, wantSource)
	case "next":
		if err != nil || !reflect.DeepEqual(fsyncOrder, []string{"checkout", "private"}) {
			t.Fatalf("recovery rename next=(%+v,%v), fsync=%v", got, err, fsyncOrder)
		}
		recoveryAssertOldDatabase(t, before, after)
		recoveryAssertTree(t, fixture.livePath(), wantSource)
		checkpointFallbackAssertAbsent(t, fixture.journal.BackupPath)
		recoveryAssertReturnedStatus(t, fixture.service, fixture.request.Scope, got)
	case "third":
		if !reflect.DeepEqual(got, WorkspaceStatus{}) || !errors.Is(err, ErrCheckpointRecoveryBlocked) || !errors.Is(err, injected) || !reflect.DeepEqual(after, before) || len(fsyncOrder) != 0 {
			t.Fatalf("recovery rename third=(%+v,%v), fsync=%v", got, err, fsyncOrder)
		}
		recoveryAssertTree(t, fixture.livePath(), wantSource)
		recoveryAssertTree(t, fixture.journal.BackupPath, third)
	}
	if renames != 1 || after.workspace.Binding != acceptedBefore {
		t.Fatalf("recovery rename attempts=%d or accepted binding moved to %+v", renames, after.workspace.Binding)
	}
	recoveryAssertTree(t, fixture.journal.StagePath, fixture.journal.CandidateTree)
}

func checkpointRecoveryExercisePublisherRenameResult(t *testing.T, role, outcome string) {
	t.Helper()
	fixture, request := newCheckpointRecoveryPublisherFixture(t)
	before := recoveryDatabaseState(t, fixture.service, request.Scope)
	acceptedBefore := before.workspace.Binding
	var expectedPrepared localstore.WorkspaceMaterializationRecord
	checkpointRecoveryCapturePreparedJournal(t, fixture, &expectedPrepared)
	var expectedInput checkpointArtifactInput
	var expectedEvidence checkpointArtifactEvidence
	opaque := checkpointRecoveryOpaqueTree()
	thirdLive := state.Tree{{Path: "third-live.txt", Data: []byte("third publisher live topology\n")}}
	thirdBackup := state.Tree{{Path: "third-backup.txt", Data: []byte("third publisher backup topology\n")}}
	injected := errors.New("publisher rename uncertainty")
	targetAttempts := 0
	checkpointRecoveryUseRealPublisher(t, fixture, func(artifact *checkpointArtifact) {
		thirdBackup = checkpointFallbackOpaqueTree(artifact.proof.prior.tree, "third publisher backup")
		evidence := artifact.evidence()
		expectedEvidence = evidence
		expectedInput = checkpointArtifactInput{
			Checkout:  artifact.checkoutIdentity,
			PriorTree: cloneCheckpointTree(artifact.proof.prior.tree), PriorTreeDigest: artifact.proof.prior.digest,
			CandidateTree: cloneCheckpointTree(artifact.proof.candidate.tree), CandidateDigest: artifact.proof.candidate.digest,
		}
		stageName, backupName := filepath.Base(evidence.StagePath), filepath.Base(evidence.BackupPath)
		if role == "publisher compensation" {
			opaque = checkpointFallbackOpaqueTree(artifact.proof.prior.tree, "rename compensation")
			seenLiveFsync := 0
			artifact.dependencies.fault = func(stage checkpointArtifactFaultStage) error {
				if stage == checkpointArtifactAfterLiveParentFsync {
					seenLiveFsync++
					if seenLiveFsync == 1 {
						checkpointFallbackWriteTree(t, evidence.BackupPath, opaque)
					}
				}
				return nil
			}
		}
		realRename := artifact.dependencies.operations.rename
		artifact.dependencies.operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
			target := (role == "publisher live to backup" && from == ".wormhole" && to == backupName) ||
				(role == "publisher stage to live" && from == stageName && to == ".wormhole") ||
				(role == "publisher compensation" && from == backupName && to == ".wormhole")
			if !target {
				return realRename(fromFD, from, toFD, to, flags)
			}
			targetAttempts++
			switch outcome {
			case "prior":
				return injected
			case "next":
				if err := realRename(fromFD, from, toFD, to, flags); err != nil {
					return err
				}
				return injected
			case "third":
				switch role {
				case "publisher live to backup":
					if err := realRename(fromFD, from, toFD, to, flags); err != nil {
						return err
					}
					checkpointFallbackCreateTree(t, filepath.Join(request.Root, ".wormhole"), thirdLive)
				case "publisher stage to live":
					if err := realRename(fromFD, from, toFD, to, flags); err != nil {
						return err
					}
					checkpointFallbackWriteTree(t, evidence.BackupPath, thirdBackup)
				case "publisher compensation":
					checkpointFallbackCreateTree(t, filepath.Join(request.Root, ".wormhole"), thirdLive)
				}
				return injected
			default:
				panic("unknown publisher rename outcome")
			}
		}
	})

	got, err := fixture.service.Checkpoint(context.Background(), request)
	after := recoveryDatabaseState(t, fixture.service, request.Scope)
	checkpointRecoveryAssertPreparedArtifactInput(t, expectedPrepared, expectedInput, expectedEvidence)
	if targetAttempts != 1 || after.workspace.Binding != acceptedBefore {
		t.Fatalf("publisher target attempts=%d or accepted binding moved to %+v", targetAttempts, after.workspace.Binding)
	}
	journal := after.disposition.Journals[0]
	livePath := filepath.Join(request.Root, ".wormhole")
	switch outcome {
	case "prior":
		if got != (CheckpointResult{}) || !errors.Is(err, injected) || errors.Is(err, ErrCheckpointRecoveryBlocked) {
			t.Fatalf("publisher rename prior=(%+v,%v)", got, err)
		}
		checkpointRecoveryAssertCheckpointOutcomeDatabase(t, before, after, expectedPrepared, "prepared")
		switch role {
		case "publisher live to backup":
			recoveryAssertTree(t, livePath, journal.PriorTree)
			checkpointFallbackAssertAbsent(t, journal.BackupPath)
		case "publisher stage to live":
			checkpointFallbackAssertAbsent(t, livePath)
			recoveryAssertTree(t, journal.BackupPath, journal.PriorTree)
		case "publisher compensation":
			checkpointFallbackAssertAbsent(t, livePath)
			recoveryAssertTree(t, journal.BackupPath, opaque)
		}
		recoveryAssertTree(t, journal.StagePath, journal.CandidateTree)
	case "next":
		if role == "publisher compensation" {
			if got != (CheckpointResult{}) || !errors.Is(err, ErrCheckpointCAS) {
				t.Fatalf("publisher compensation next=(%+v,%v)", got, err)
			}
			checkpointRecoveryAssertCheckpointOutcomeDatabase(t, before, after, expectedPrepared, "recovered_old")
			recoveryAssertTree(t, livePath, opaque)
			recoveryAssertTree(t, journal.StagePath, journal.CandidateTree)
			checkpointFallbackAssertAbsent(t, journal.BackupPath)
		} else {
			if err != nil || got.JournalID == "" {
				t.Fatalf("publisher rename next=(%+v,%v)", got, err)
			}
			checkpointRecoveryAssertCheckpointOutcomeDatabase(t, before, after, expectedPrepared, "published")
			recoveryAssertTree(t, livePath, journal.CandidateTree)
			checkpointFallbackAssertAbsent(t, journal.StagePath)
			recoveryAssertTree(t, journal.BackupPath, journal.PriorTree)
		}
	case "third":
		if got != (CheckpointResult{}) || !errors.Is(err, ErrCheckpointRecoveryBlocked) || !errors.Is(err, injected) {
			t.Fatalf("publisher rename third=(%+v,%v)", got, err)
		}
		checkpointRecoveryAssertCheckpointOutcomeDatabase(t, before, after, expectedPrepared, "prepared")
		switch role {
		case "publisher live to backup":
			recoveryAssertTree(t, livePath, thirdLive)
			recoveryAssertTree(t, journal.StagePath, journal.CandidateTree)
			recoveryAssertTree(t, journal.BackupPath, journal.PriorTree)
		case "publisher stage to live":
			recoveryAssertTree(t, livePath, journal.CandidateTree)
			checkpointFallbackAssertAbsent(t, journal.StagePath)
			recoveryAssertTree(t, journal.BackupPath, thirdBackup)
		case "publisher compensation":
			recoveryAssertTree(t, livePath, thirdLive)
			recoveryAssertTree(t, journal.StagePath, journal.CandidateTree)
			recoveryAssertTree(t, journal.BackupPath, opaque)
		}
	}
}
