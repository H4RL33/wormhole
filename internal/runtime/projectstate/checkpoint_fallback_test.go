//go:build linux

package projectstate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	state "github.com/H4RL33/wormhole/internal/types/projectstate"
	"golang.org/x/sys/unix"
)

func TestCheckpointFallbackPublishesDurably(t *testing.T) {
	input := checkpointArtifactCandidateWithNestedFile(t)
	artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, checkpointArtifactDependencies{
		readGit: readOnlyGitLimited,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.close()
	if err := publishPreparedCheckpointArtifact(context.Background(), artifact); err != nil {
		t.Fatalf("publish durable fallback: %v", err)
	}
	assertCheckpointPublishedTopology(t, input, artifact.evidence())
	if err := publishPreparedCheckpointArtifact(context.Background(), artifact); !errors.Is(err, ErrCheckpointUnsupported) {
		t.Fatalf("repeated publication error = %v, want closed/claimed rejection", err)
	}
}

func TestCheckpointArtifactPublicationLifecycleAndCancellation(t *testing.T) {
	t.Run("closed", func(t *testing.T) {
		input := checkpointArtifactCandidateWithNestedFile(t)
		artifact, err := prepareCheckpointArtifact(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		artifact.close()
		if err := publishPreparedCheckpointArtifact(context.Background(), artifact); !errors.Is(err, ErrCheckpointUnsupported) {
			t.Fatalf("closed publication error = %v", err)
		}
		assertCheckpointPreparedTopology(t, input, artifact.evidence())
	})

	t.Run("cancelled", func(t *testing.T) {
		input := checkpointArtifactCandidateWithNestedFile(t)
		artifact, err := prepareCheckpointArtifact(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		defer artifact.close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := publishPreparedCheckpointArtifact(ctx, artifact); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled publication error = %v", err)
		}
		assertCheckpointPreparedTopology(t, input, artifact.evidence())
		if err := publishPreparedCheckpointArtifact(context.Background(), artifact); !errors.Is(err, ErrCheckpointUnsupported) {
			t.Fatalf("cancelled claimed artifact replay error = %v", err)
		}
	})

	t.Run("concurrent", func(t *testing.T) {
		input := checkpointArtifactCandidateWithNestedFile(t)
		artifact, err := prepareCheckpointArtifact(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		defer artifact.close()
		const callers = 8
		start := make(chan struct{})
		results := make(chan error, callers)
		var group sync.WaitGroup
		for range callers {
			group.Add(1)
			go func() {
				defer group.Done()
				<-start
				results <- publishPreparedCheckpointArtifact(context.Background(), artifact)
			}()
		}
		close(start)
		group.Wait()
		close(results)
		successes := 0
		for err := range results {
			if err == nil {
				successes++
			} else if !errors.Is(err, ErrCheckpointUnsupported) {
				t.Fatalf("concurrent publication error = %v", err)
			}
		}
		if successes != 1 {
			t.Fatalf("concurrent publication successes = %d, want 1", successes)
		}
		assertCheckpointPublishedTopology(t, input, artifact.evidence())
	})
}

func TestCheckpointArtifactPublicationPreflightDriftIsZeroMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, checkpointArtifactInput, *checkpointArtifact)
	}{
		{name: "live bytes", mutate: func(t *testing.T, input checkpointArtifactInput, _ *checkpointArtifact) {
			if err := os.WriteFile(filepath.Join(input.Checkout.CanonicalPath, ".wormhole", "config.toml"), []byte("changed\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "stage bytes", mutate: func(t *testing.T, _ checkpointArtifactInput, artifact *checkpointArtifact) {
			if err := os.WriteFile(filepath.Join(artifact.evidence().StagePath, "config.toml"), []byte("changed\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "backup collision", mutate: func(t *testing.T, _ checkpointArtifactInput, artifact *checkpointArtifact) {
			if err := os.Mkdir(artifact.evidence().BackupPath, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "Git path resolution", mutate: func(t *testing.T, _ checkpointArtifactInput, artifact *checkpointArtifact) {
			call := 0
			original := artifact.dependencies.readGit
			artifact.dependencies.readGit = func(ctx context.Context, root string, limit int, args ...string) ([]byte, error) {
				call++
				if call == 1 {
					return []byte(filepath.Join(t.TempDir(), "other.git") + "\n"), nil
				}
				return original(ctx, root, limit, args...)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := checkpointArtifactCandidateWithNestedFile(t)
			artifact, err := prepareCheckpointArtifact(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			defer artifact.close()
			test.mutate(t, input, artifact)
			if err := publishPreparedCheckpointArtifact(context.Background(), artifact); err == nil {
				t.Fatal("publication accepted preflight drift")
			}
			if _, err := os.Lstat(artifact.evidence().StagePath); err != nil {
				t.Fatalf("preflight removed stage: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(input.Checkout.CanonicalPath, ".wormhole")); err != nil {
				t.Fatalf("preflight removed live: %v", err)
			}
			if err := publishPreparedCheckpointArtifact(context.Background(), artifact); !errors.Is(err, ErrCheckpointUnsupported) {
				t.Fatalf("failed preflight replay error = %v", err)
			}
		})
	}
}

func TestCheckpointArtifactFinalBoundaryRejectsHookDriftBeforeFirstRename(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(context.CancelFunc, checkpointArtifactInput, *checkpointArtifact) error
		want   error
	}{
		{name: "cancel", want: context.Canceled, mutate: func(cancel context.CancelFunc, _ checkpointArtifactInput, _ *checkpointArtifact) error {
			cancel()
			return nil
		}},
		{name: "live", mutate: func(_ context.CancelFunc, input checkpointArtifactInput, _ *checkpointArtifact) error {
			return os.WriteFile(filepath.Join(input.Checkout.CanonicalPath, ".wormhole", "config.toml"), []byte("changed\n"), 0o600)
		}},
		{name: "stage", mutate: func(_ context.CancelFunc, _ checkpointArtifactInput, artifact *checkpointArtifact) error {
			return os.WriteFile(filepath.Join(artifact.evidenceValue.StagePath, "config.toml"), []byte("changed\n"), 0o600)
		}},
		{name: "nested stage mode", mutate: func(_ context.CancelFunc, _ checkpointArtifactInput, artifact *checkpointArtifact) error {
			return os.Chmod(filepath.Join(artifact.evidenceValue.StagePath, "config.toml"), 0o644)
		}},
		{name: "extra empty stage directory", mutate: func(_ context.CancelFunc, _ checkpointArtifactInput, artifact *checkpointArtifact) error {
			return os.Mkdir(filepath.Join(artifact.evidenceValue.StagePath, "unexpected-empty"), 0o700)
		}},
		{name: "backup", mutate: func(_ context.CancelFunc, _ checkpointArtifactInput, artifact *checkpointArtifact) error {
			return os.Mkdir(artifact.evidenceValue.BackupPath, 0o700)
		}},
		{name: "private root mode", mutate: func(_ context.CancelFunc, _ checkpointArtifactInput, artifact *checkpointArtifact) error {
			return os.Chmod(filepath.Dir(artifact.evidenceValue.StagePath), 0o755)
		}},
		{name: "Git indirection", mutate: func(_ context.CancelFunc, _ checkpointArtifactInput, artifact *checkpointArtifact) error {
			artifact.dependencies.readGit = func(context.Context, string, int, ...string) ([]byte, error) {
				return []byte("/tmp/changed-checkpoint-git-dir\n"), nil
			}
			return nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := checkpointArtifactCandidateWithNestedFile(t)
			artifact, err := prepareCheckpointArtifact(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			defer artifact.close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			mutated := false
			artifact.dependencies.fault = func(stage checkpointArtifactFaultStage) error {
				if !mutated && stage == checkpointArtifactBeforeLiveMutation {
					mutated = true
					return test.mutate(cancel, input, artifact)
				}
				return nil
			}
			renameCalls := 0
			realRename := artifact.dependencies.operations.rename
			artifact.dependencies.operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
				renameCalls++
				return realRename(fromFD, from, toFD, to, flags)
			}
			err = publishPreparedCheckpointArtifact(ctx, artifact)
			if err == nil || !mutated {
				t.Fatalf("hook drift publication = (%v, mutated %t)", err, mutated)
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("hook drift error = %v, want %v", err, test.want)
			}
			if renameCalls != 0 {
				t.Fatalf("hook drift rename calls = %d, want 0", renameCalls)
			}
		})
	}
}

func TestCheckpointArtifactFinalBoundaryRejectsHookDriftBeforeSecondRename(t *testing.T) {
	for _, drift := range []string{"stage", "Git indirection", "private root mode", "candidate mode", "candidate root churn", "private root churn", "checkout root churn"} {
		t.Run(drift, func(t *testing.T) {
			input := checkpointArtifactCandidateWithNestedFile(t)
			artifact := prepareCheckpointFallbackArtifact(t, input)
			defer artifact.close()
			realRename := artifact.dependencies.operations.rename
			mutated := false
			artifact.dependencies.fault = func(stage checkpointArtifactFaultStage) error {
				if mutated || stage != checkpointArtifactBeforeSecondLiveMutation {
					return nil
				}
				mutated = true
				switch drift {
				case "stage":
					return os.WriteFile(filepath.Join(artifact.evidenceValue.StagePath, "config.toml"), []byte("changed\n"), 0o600)
				case "Git indirection":
					artifact.dependencies.readGit = func(context.Context, string, int, ...string) ([]byte, error) {
						return []byte("/tmp/changed-checkpoint-git-dir\n"), nil
					}
				case "private root mode":
					return os.Chmod(filepath.Dir(artifact.evidenceValue.StagePath), 0o755)
				case "candidate mode":
					return os.Chmod(filepath.Join(artifact.evidenceValue.StagePath, "config.toml"), 0o644)
				case "candidate root churn":
					marker := filepath.Join(artifact.evidenceValue.StagePath, ".checkpoint-churn")
					if err := os.WriteFile(marker, []byte("churn"), 0o600); err != nil {
						return err
					}
					return os.Remove(marker)
				case "private root churn":
					marker := filepath.Join(filepath.Dir(artifact.evidenceValue.StagePath), ".checkpoint-private-churn")
					if err := os.WriteFile(marker, []byte("churn"), 0o600); err != nil {
						return err
					}
					return os.Remove(marker)
				case "checkout root churn":
					marker := filepath.Join(input.Checkout.CanonicalPath, ".checkpoint-checkout-churn")
					if err := os.WriteFile(marker, []byte("churn"), 0o600); err != nil {
						return err
					}
					return os.Remove(marker)
				}
				return nil
			}
			renameCalls := 0
			artifact.dependencies.operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
				renameCalls++
				return realRename(fromFD, from, toFD, to, flags)
			}
			if err := publishPreparedCheckpointArtifact(context.Background(), artifact); err == nil {
				t.Fatal("second-boundary drift publication succeeded")
			}
			if !mutated || renameCalls != 1 {
				t.Fatalf("second-boundary drift = mutated %t renames %d, want true/1", mutated, renameCalls)
			}
		})
	}
}

func TestCheckpointArtifactPublicationExactOrdering(t *testing.T) {
	input := checkpointArtifactCandidateWithNestedFile(t)
	operations := normalizeCheckpointArtifactRenameOperations(defaultCheckpointArtifactPlatformOperations())
	realRename, realFsync := operations.rename, operations.fsync
	artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, checkpointArtifactDependencies{readGit: readOnlyGitLimited, operations: operations})
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.close()
	terminalFD, privateFD := artifact.checkout.ancestry[len(artifact.checkout.ancestry)-1].fd, artifact.private.fd
	var calls []string
	artifact.dependencies.operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
		calls = append(calls, fmt.Sprintf("rename:%s:%s:%d", from, to, flags))
		return realRename(fromFD, from, toFD, to, flags)
	}
	artifact.dependencies.operations.fsync = func(fd int) error {
		switch fd {
		case terminalFD:
			calls = append(calls, "fsync:live-parent")
		case privateFD:
			calls = append(calls, "fsync:private-parent")
		default:
			calls = append(calls, fmt.Sprintf("fsync:%d", fd))
		}
		return realFsync(fd)
	}
	if err := publishPreparedCheckpointArtifact(context.Background(), artifact); err != nil {
		t.Fatal(err)
	}
	stageName, backupName := filepath.Base(artifact.evidence().StagePath), filepath.Base(artifact.evidence().BackupPath)
	want := []string{
		"fsync:live-parent", "fsync:private-parent",
		fmt.Sprintf("rename:.wormhole:%s:%d", backupName, checkpointNoReplaceRenameFlag()),
		"fsync:live-parent", "fsync:private-parent",
		fmt.Sprintf("rename:%s:.wormhole:%d", stageName, checkpointNoReplaceRenameFlag()),
		"fsync:live-parent", "fsync:private-parent",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("publication calls = %v, want %v", calls, want)
	}
}

func TestCheckpointFallbackFaultTopologyMatrix(t *testing.T) {
	tests := []struct {
		stage      checkpointArtifactFaultStage
		occurrence int
		published  bool
	}{
		{stage: checkpointArtifactAfterLiveMutation, occurrence: 1},
		{stage: checkpointArtifactBeforeLiveParentFsync, occurrence: 1},
		{stage: checkpointArtifactAfterLiveParentFsync, occurrence: 1},
		{stage: checkpointArtifactBeforePrivateParentFsync, occurrence: 1},
		{stage: checkpointArtifactAfterPrivateParentFsync, occurrence: 1},
		{stage: checkpointArtifactBeforeSecondLiveMutation, occurrence: 1},
		{stage: checkpointArtifactAfterSecondLiveMutation, occurrence: 1, published: true},
		{stage: checkpointArtifactBeforeLiveParentFsync, occurrence: 2, published: true},
		{stage: checkpointArtifactAfterLiveParentFsync, occurrence: 2, published: true},
		{stage: checkpointArtifactBeforePrivateParentFsync, occurrence: 2, published: true},
		{stage: checkpointArtifactAfterPrivateParentFsync, occurrence: 2, published: true},
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("%d/%d", test.stage, test.occurrence), func(t *testing.T) {
			input := checkpointArtifactCandidateWithNestedFile(t)
			artifact := prepareCheckpointFallbackArtifact(t, input)
			defer artifact.close()
			injected := errors.New("fallback publication fault")
			seen := 0
			artifact.dependencies.fault = func(got checkpointArtifactFaultStage) error {
				if got == test.stage {
					seen++
					if seen == test.occurrence {
						return injected
					}
				}
				return nil
			}
			if err := publishPreparedCheckpointArtifact(context.Background(), artifact); !errors.Is(err, injected) || seen != test.occurrence {
				t.Fatalf("fallback fault = (%v, occurrence %d)", err, seen)
			}
			if test.published {
				assertCheckpointPublishedTopology(t, input, artifact.evidence())
			} else {
				assertCheckpointBackedUpTopology(t, input, artifact.evidence())
			}
		})
	}
}

func TestCheckpointFallbackSyscallFaultTopologyMatrix(t *testing.T) {
	tests := []struct {
		name            string
		renameCall      int
		mutate          bool
		fsyncRole       string
		fsyncOccurrence int
		topology        checkpointArtifactTopology
	}{
		{name: "backup rename unchanged error", renameCall: 1},
		{name: "backup rename mutated error", renameCall: 1, mutate: true, topology: checkpointTopologyBackedUp},
		{name: "first live parent fsync", fsyncRole: "live", fsyncOccurrence: 2, topology: checkpointTopologyBackedUp},
		{name: "first private parent fsync", fsyncRole: "private", fsyncOccurrence: 2, topology: checkpointTopologyBackedUp},
		{name: "publish rename unchanged error", renameCall: 2, topology: checkpointTopologyBackedUp},
		{name: "publish rename mutated error", renameCall: 2, mutate: true, topology: checkpointTopologyPublished},
		{name: "final live parent fsync", fsyncRole: "live", fsyncOccurrence: 3, topology: checkpointTopologyPublished},
		{name: "final private parent fsync", fsyncRole: "private", fsyncOccurrence: 3, topology: checkpointTopologyPublished},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := checkpointArtifactCandidateWithNestedFile(t)
			artifact := prepareCheckpointFallbackArtifact(t, input)
			defer artifact.close()
			terminalFD, privateFD := artifact.checkout.ancestry[len(artifact.checkout.ancestry)-1].fd, artifact.private.fd
			realRename, realFsync := artifact.dependencies.operations.rename, artifact.dependencies.operations.fsync
			renameCalls := 0
			artifact.dependencies.operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
				renameCalls++
				if renameCalls == test.renameCall {
					if test.mutate {
						if err := realRename(fromFD, from, toFD, to, flags); err != nil {
							return err
						}
					}
					return unix.EIO
				}
				return realRename(fromFD, from, toFD, to, flags)
			}
			fsyncCounts := map[int]int{}
			artifact.dependencies.operations.fsync = func(fd int) error {
				fsyncCounts[fd]++
				if fsyncCounts[fd] == test.fsyncOccurrence && ((test.fsyncRole == "live" && fd == terminalFD) || (test.fsyncRole == "private" && fd == privateFD)) {
					return unix.EIO
				}
				return realFsync(fd)
			}
			if err := publishPreparedCheckpointArtifact(context.Background(), artifact); err == nil {
				t.Fatal("fallback syscall fault succeeded")
			}
			switch test.topology {
			case 0:
				assertCheckpointPreparedTopology(t, input, artifact.evidence())
			case checkpointTopologyBackedUp:
				assertCheckpointBackedUpTopology(t, input, artifact.evidence())
			case checkpointTopologyPublished:
				assertCheckpointPublishedTopology(t, input, artifact.evidence())
			}
		})
	}
}

func TestCheckpointArtifactCloseSerializesWithPublicationAndIsIdempotent(t *testing.T) {
	input := checkpointArtifactCandidateWithNestedFile(t)
	artifact, err := prepareCheckpointArtifact(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	artifact.dependencies.fault = func(stage checkpointArtifactFaultStage) error {
		if stage == checkpointArtifactBeforeLiveMutation {
			close(entered)
			<-release
		}
		return nil
	}
	published := make(chan error, 1)
	go func() { published <- publishPreparedCheckpointArtifact(context.Background(), artifact) }()
	<-entered
	closed := make(chan struct{})
	go func() {
		artifact.close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("close raced through publication lock")
	default:
	}
	close(release)
	if err := <-published; err != nil {
		t.Fatal(err)
	}
	<-closed
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() { defer group.Done(); artifact.close() }()
	}
	group.Wait()
	assertCheckpointPublishedTopology(t, input, artifact.evidenceValue)
}

func prepareCheckpointFallbackArtifact(t *testing.T, input checkpointArtifactInput) *checkpointArtifact {
	t.Helper()
	artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, checkpointArtifactDependencies{readGit: readOnlyGitLimited})
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func assertCheckpointBackedUpTopology(t *testing.T, input checkpointArtifactInput, evidence checkpointArtifactEvidence) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(input.Checkout.CanonicalPath, ".wormhole")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backed-up live exists: %v", err)
	}
	assertCheckpointPathTree(t, evidence.StagePath, input.CandidateTree)
	assertCheckpointPathTree(t, evidence.BackupPath, input.PriorTree)
}

func assertCheckpointPathTree(t *testing.T, path string, want state.Tree) {
	t.Helper()
	got, err := readCheckpointArtifactPathTree(context.Background(), path)
	if err != nil || !sameCheckpointArtifactTree(got, want) {
		t.Fatalf("tree at %q = (%v, %v)", path, got, err)
	}
}

func assertCheckpointPreparedTopology(t *testing.T, input checkpointArtifactInput, evidence checkpointArtifactEvidence) {
	t.Helper()
	live, err := readCheckpointArtifactPathTree(context.Background(), filepath.Join(input.Checkout.CanonicalPath, ".wormhole"))
	if err != nil || !sameCheckpointArtifactTree(live, input.PriorTree) {
		t.Fatalf("prepared live = (%v, %v)", live, err)
	}
	stage, err := readCheckpointArtifactPathTree(context.Background(), evidence.StagePath)
	if err != nil || !sameCheckpointArtifactTree(stage, input.CandidateTree) {
		t.Fatalf("prepared stage = (%v, %v)", stage, err)
	}
	if _, err := os.Lstat(evidence.BackupPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prepared backup exists: %v", err)
	}
}
