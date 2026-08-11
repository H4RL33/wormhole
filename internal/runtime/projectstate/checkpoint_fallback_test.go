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
	disposition, err := publishPreparedCheckpointArtifact(context.Background(), artifact)
	if err != nil || disposition != checkpointPublicationPublished {
		t.Fatalf("publish durable fallback = (%d, %v), want published and nil", disposition, err)
	}
	assertCheckpointPublishedTopology(t, input, artifact.evidence())
	if _, err := publishPreparedCheckpointArtifact(context.Background(), artifact); !errors.Is(err, ErrCheckpointUnsupported) {
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
		if _, err := publishPreparedCheckpointArtifact(context.Background(), artifact); !errors.Is(err, ErrCheckpointUnsupported) {
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
		if _, err := publishPreparedCheckpointArtifact(ctx, artifact); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled publication error = %v", err)
		}
		assertCheckpointPreparedTopology(t, input, artifact.evidence())
		if _, err := publishPreparedCheckpointArtifact(context.Background(), artifact); !errors.Is(err, ErrCheckpointUnsupported) {
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
				_, err := publishPreparedCheckpointArtifact(context.Background(), artifact)
				results <- err
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
		name            string
		mutate          func(*testing.T, checkpointArtifactInput, *checkpointArtifact)
		wantDisposition checkpointPublicationDisposition
	}{
		{name: "live bytes", wantDisposition: checkpointPublicationPreservedConcurrentOld, mutate: func(t *testing.T, input checkpointArtifactInput, _ *checkpointArtifact) {
			if err := os.WriteFile(filepath.Join(input.Checkout.CanonicalPath, ".wormhole", "config.toml"), []byte("changed\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "stage bytes", mutate: func(t *testing.T, _ checkpointArtifactInput, artifact *checkpointArtifact) {
			if err := os.WriteFile(filepath.Join(artifact.evidenceValue.StagePath, "config.toml"), []byte("changed\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "backup collision", mutate: func(t *testing.T, _ checkpointArtifactInput, artifact *checkpointArtifact) {
			if err := os.Mkdir(artifact.evidenceValue.BackupPath, 0o700); err != nil {
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
			disposition, err := publishPreparedCheckpointArtifact(context.Background(), artifact)
			if test.wantDisposition != 0 {
				if disposition != test.wantDisposition || err != nil {
					t.Fatalf("publication disposition = (%d, %v), want %d and nil", disposition, err, test.wantDisposition)
				}
			} else if disposition != 0 || err == nil {
				t.Fatalf("publication accepted invalid preflight evidence = (%d, %v)", disposition, err)
			}
			if _, err := os.Lstat(artifact.evidence().StagePath); err != nil {
				t.Fatalf("preflight removed stage: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(input.Checkout.CanonicalPath, ".wormhole")); err != nil {
				t.Fatalf("preflight removed live: %v", err)
			}
			if _, err := publishPreparedCheckpointArtifact(context.Background(), artifact); !errors.Is(err, ErrCheckpointUnsupported) {
				t.Fatalf("failed preflight replay error = %v", err)
			}
		})
	}
}

func TestCheckpointArtifactFinalBoundaryRejectsHookDriftBeforeFirstRename(t *testing.T) {
	for _, test := range []struct {
		name        string
		mutate      func(context.CancelFunc, checkpointArtifactInput, *checkpointArtifact) error
		want        error
		disposition checkpointPublicationDisposition
	}{
		{name: "cancel", want: context.Canceled, mutate: func(cancel context.CancelFunc, _ checkpointArtifactInput, _ *checkpointArtifact) error {
			cancel()
			return nil
		}},
		{name: "live", disposition: checkpointPublicationPreservedConcurrentOld, mutate: func(_ context.CancelFunc, input checkpointArtifactInput, _ *checkpointArtifact) error {
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
			disposition, err := publishPreparedCheckpointArtifact(ctx, artifact)
			if test.disposition != 0 {
				if disposition != test.disposition || err != nil || !mutated {
					t.Fatalf("hook publication = (%d, %v, mutated %t)", disposition, err, mutated)
				}
			} else if disposition != 0 || err == nil || !mutated {
				t.Fatalf("hook drift publication = (%d, %v, mutated %t)", disposition, err, mutated)
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
	for _, test := range []struct {
		drift   string
		publish bool
	}{
		{drift: "stage"},
		{drift: "Git indirection"},
		{drift: "private root mode"},
		{drift: "candidate mode"},
		{drift: "candidate root churn", publish: true},
		{drift: "private root churn", publish: true},
		{drift: "checkout root churn", publish: true},
	} {
		t.Run(test.drift, func(t *testing.T) {
			drift := test.drift
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
			disposition, err := publishPreparedCheckpointArtifact(context.Background(), artifact)
			wantRenames := 1
			if test.publish {
				wantRenames = 2
				if disposition != checkpointPublicationPublished || err != nil {
					t.Fatalf("freshly stable second-boundary evidence = (%d, %v)", disposition, err)
				}
			} else if disposition != 0 || err == nil {
				t.Fatalf("unsafe second-boundary evidence = (%d, %v)", disposition, err)
			}
			if !mutated || renameCalls != wantRenames {
				t.Fatalf("second-boundary drift = mutated %t renames %d, want true/%d", mutated, renameCalls, wantRenames)
			}
		})
	}
}

func TestCheckpointFallbackPublisherOrdersRenamesAndParentFsyncs(t *testing.T) {
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
	if _, err := publishPreparedCheckpointArtifact(context.Background(), artifact); err != nil {
		t.Fatal(err)
	}
	stageName, backupName := filepath.Base(artifact.evidence().StagePath), filepath.Base(artifact.evidence().BackupPath)
	want := []string{
		fmt.Sprintf("rename:.wormhole:%s:%d", backupName, checkpointNoReplaceRenameFlag()),
		"fsync:private-parent", "fsync:live-parent",
		fmt.Sprintf("rename:%s:.wormhole:%d", stageName, checkpointNoReplaceRenameFlag()),
		"fsync:live-parent", "fsync:private-parent",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("publication calls = %v, want %v", calls, want)
	}

	for _, test := range []struct {
		name       string
		failRole   string
		occurrence int
	}{
		{name: "backup destination", failRole: "private", occurrence: 1},
		{name: "publication destination", failRole: "live", occurrence: 1},
	} {
		t.Run("preserves "+test.name+" fsync cause", func(t *testing.T) {
			input := checkpointArtifactCandidateWithNestedFile(t)
			artifact := prepareCheckpointFallbackArtifact(t, input)
			defer artifact.close()
			terminalFD := artifact.checkout.ancestry[len(artifact.checkout.ancestry)-1].fd
			privateFD := artifact.private.fd
			realFsync := artifact.dependencies.operations.fsync
			injected := errors.New("ordinary parent fsync failure")
			counts := map[int]int{}
			artifact.dependencies.operations.fsync = func(fd int) error {
				if fd != terminalFD && fd != privateFD {
					return realFsync(fd)
				}
				counts[fd]++
				role := "private"
				if fd == terminalFD {
					role = "live"
				}
				if role == test.failRole && counts[fd] == test.occurrence {
					return injected
				}
				return realFsync(fd)
			}
			disposition, err := publishPreparedCheckpointArtifact(context.Background(), artifact)
			if disposition != 0 || !errors.Is(err, injected) || errors.Is(err, ErrCheckpointUnsupported) {
				t.Fatalf("fsync failure = (%d, %v), want zero and original non-unsupported cause", disposition, err)
			}
		})
	}
}

func TestCheckpointFallbackPublisherPreservesConcurrentOldAndNeverOverwritesRecreatedLive(t *testing.T) {
	t.Run("pre-rename opaque live", func(t *testing.T) {
		input := checkpointArtifactCandidateWithNestedFile(t)
		artifact := prepareCheckpointFallbackArtifact(t, input)
		defer artifact.close()
		livePath := filepath.Join(input.Checkout.CanonicalPath, ".wormhole")
		opaque := checkpointFallbackOpaqueTree(input.PriorTree, "pre-rename opaque")
		mutated := false
		artifact.dependencies.fault = func(stage checkpointArtifactFaultStage) error {
			if !mutated && stage == checkpointArtifactBeforeLiveMutation {
				mutated = true
				checkpointFallbackReplaceTree(t, livePath, opaque, ".retained-prior")
			}
			return nil
		}
		renames := checkpointFallbackCountRenames(artifact)
		disposition, err := publishPreparedCheckpointArtifact(context.Background(), artifact)
		if disposition != checkpointPublicationPreservedConcurrentOld || err != nil || *renames != 0 {
			t.Fatalf("pre-rename opaque = (%d, %v), renames %d", disposition, err, *renames)
		}
		assertCheckpointPathTree(t, livePath, opaque)
		assertCheckpointPathTree(t, artifact.evidence().StagePath, input.CandidateTree)
		checkpointFallbackAssertAbsent(t, artifact.evidence().BackupPath)
		assertCheckpointPathTree(t, livePath+".retained-prior", input.PriorTree)
	})

	t.Run("byte-identical live replacement", func(t *testing.T) {
		input := checkpointArtifactCandidateWithNestedFile(t)
		artifact := prepareCheckpointFallbackArtifact(t, input)
		defer artifact.close()
		livePath := filepath.Join(input.Checkout.CanonicalPath, ".wormhole")
		original, err := os.Stat(livePath)
		if err != nil {
			t.Fatal(err)
		}
		mutated := false
		artifact.dependencies.fault = func(stage checkpointArtifactFaultStage) error {
			if !mutated && stage == checkpointArtifactBeforeLiveMutation {
				mutated = true
				checkpointFallbackReplaceTree(t, livePath, input.PriorTree, ".retained-identical")
			}
			return nil
		}
		disposition, err := publishPreparedCheckpointArtifact(context.Background(), artifact)
		if disposition != checkpointPublicationPublished || err != nil {
			t.Fatalf("byte-identical replacement = (%d, %v)", disposition, err)
		}
		replacement, err := os.Stat(artifact.evidence().BackupPath)
		if err != nil || os.SameFile(original, replacement) {
			t.Fatalf("byte-identical replacement inode proof = (%v, %v)", replacement, err)
		}
		assertCheckpointPublishedTopology(t, input, artifact.evidence())
	})

	t.Run("opaque backup compensation", func(t *testing.T) {
		input := checkpointArtifactCandidateWithNestedFile(t)
		artifact := prepareCheckpointFallbackArtifact(t, input)
		defer artifact.close()
		opaque := checkpointFallbackOpaqueTree(input.PriorTree, "opaque backup")
		evidence := artifact.evidence()
		realRename := artifact.dependencies.operations.rename
		renames := 0
		artifact.dependencies.operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
			renames++
			if err := realRename(fromFD, from, toFD, to, flags); err != nil {
				return err
			}
			if renames == 1 {
				checkpointFallbackWriteTree(t, evidence.BackupPath, opaque)
			}
			return nil
		}
		disposition, err := publishPreparedCheckpointArtifact(context.Background(), artifact)
		if disposition != checkpointPublicationPreservedConcurrentOld || err != nil || renames != 2 {
			t.Fatalf("opaque-backup compensation = (%d, %v), renames %d", disposition, err, renames)
		}
		assertCheckpointPathTree(t, filepath.Join(input.Checkout.CanonicalPath, ".wormhole"), opaque)
		assertCheckpointPathTree(t, artifact.evidence().StagePath, input.CandidateTree)
		checkpointFallbackAssertAbsent(t, artifact.evidence().BackupPath)
	})

	t.Run("recreated live", func(t *testing.T) {
		input := checkpointArtifactCandidateWithNestedFile(t)
		artifact := prepareCheckpointFallbackArtifact(t, input)
		defer artifact.close()
		livePath := filepath.Join(input.Checkout.CanonicalPath, ".wormhole")
		opaque := checkpointFallbackOpaqueTree(input.PriorTree, "recreated live")
		mutated := false
		artifact.dependencies.fault = func(stage checkpointArtifactFaultStage) error {
			if !mutated && stage == checkpointArtifactBeforeSecondLiveMutation {
				mutated = true
				checkpointFallbackCreateTree(t, livePath, opaque)
			}
			return nil
		}
		renames := checkpointFallbackCountRenames(artifact)
		disposition, err := publishPreparedCheckpointArtifact(context.Background(), artifact)
		if disposition != checkpointPublicationPreservedConcurrentOld || err != nil || *renames != 1 {
			t.Fatalf("recreated live = (%d, %v), renames %d", disposition, err, *renames)
		}
		assertCheckpointPathTree(t, livePath, opaque)
		assertCheckpointPathTree(t, artifact.evidence().StagePath, input.CandidateTree)
		assertCheckpointPathTree(t, artifact.evidence().BackupPath, input.PriorTree)
	})
}

func TestCheckpointFallbackPublisherClassifiesRenamePriorNextThird(t *testing.T) {
	for _, role := range []struct {
		name string
		call int
	}{
		{name: "live to backup", call: 1},
		{name: "stage to live", call: 2},
	} {
		for _, outcome := range []string{"prior", "next", "third"} {
			t.Run(role.name+"/"+outcome, func(t *testing.T) {
				input := checkpointArtifactCandidateWithNestedFile(t)
				artifact := prepareCheckpointFallbackArtifact(t, input)
				defer artifact.close()
				evidence := artifact.evidence()
				realRename := artifact.dependencies.operations.rename
				renames := 0
				artifact.dependencies.operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
					renames++
					if renames != role.call {
						return realRename(fromFD, from, toFD, to, flags)
					}
					switch outcome {
					case "prior":
						return unix.EIO
					case "next":
						if err := realRename(fromFD, from, toFD, to, flags); err != nil {
							return err
						}
						return unix.EIO
					case "third":
						if err := realRename(fromFD, from, toFD, to, flags); err != nil {
							return err
						}
						if role.call == 1 {
							checkpointFallbackCreateTree(t, filepath.Join(input.Checkout.CanonicalPath, ".wormhole"), checkpointFallbackOpaqueTree(input.PriorTree, "third live"))
						} else {
							checkpointFallbackWriteTree(t, evidence.BackupPath, checkpointFallbackOpaqueTree(input.PriorTree, "third backup"))
						}
						return unix.EIO
					}
					panic("unreachable")
				}
				disposition, err := publishPreparedCheckpointArtifact(context.Background(), artifact)
				switch outcome {
				case "prior":
					if disposition != 0 || !errors.Is(err, unix.EIO) || errors.Is(err, ErrCheckpointRecoveryBlocked) {
						t.Fatalf("prior = (%d, %v)", disposition, err)
					}
				case "next":
					if disposition != checkpointPublicationPublished || err != nil {
						t.Fatalf("next = (%d, %v)", disposition, err)
					}
				case "third":
					if disposition != 0 || !errors.Is(err, ErrCheckpointRecoveryBlocked) {
						t.Fatalf("third = (%d, %v)", disposition, err)
					}
				}
				wantRenames := role.call
				if role.call == 1 && outcome == "next" {
					wantRenames = 2
				}
				if renames != wantRenames {
					t.Fatalf("rename attempts = %d, want %d", renames, wantRenames)
				}
			})
		}
	}
}

func TestCheckpointFallbackPublisherBlocksUnknownBackupOrDualUnknownTopology(t *testing.T) {
	for _, dual := range []bool{false, true} {
		name := "unknown backup"
		if dual {
			name = "dual unknown"
		}
		t.Run(name, func(t *testing.T) {
			input := checkpointArtifactCandidateWithNestedFile(t)
			artifact := prepareCheckpointFallbackArtifact(t, input)
			defer artifact.close()
			evidence := artifact.evidence()
			realRename := artifact.dependencies.operations.rename
			renames := 0
			artifact.dependencies.operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
				renames++
				if err := realRename(fromFD, from, toFD, to, flags); err != nil {
					return err
				}
				if renames == 2 {
					checkpointFallbackWriteTree(t, evidence.BackupPath, checkpointFallbackOpaqueTree(input.PriorTree, "later backup"))
					if dual {
						checkpointFallbackReplaceTree(t, filepath.Join(input.Checkout.CanonicalPath, ".wormhole"), checkpointFallbackOpaqueTree(input.CandidateTree, "later live"), ".retained-published")
					}
				}
				return nil
			}
			disposition, err := publishPreparedCheckpointArtifact(context.Background(), artifact)
			if disposition != 0 || !errors.Is(err, ErrCheckpointRecoveryBlocked) || renames != 2 {
				t.Fatalf("ambiguous topology = (%d, %v), renames %d", disposition, err, renames)
			}
		})
	}
}

func TestCheckpointFallbackCompensationRenameClassifiesPriorNextThird(t *testing.T) {
	for _, outcome := range []string{"prior", "next", "third"} {
		t.Run(outcome, func(t *testing.T) {
			input := checkpointArtifactCandidateWithNestedFile(t)
			artifact := prepareCheckpointFallbackArtifact(t, input)
			defer artifact.close()
			opaque := checkpointFallbackOpaqueTree(input.PriorTree, "compensation source")
			evidence := artifact.evidence()
			realRename := artifact.dependencies.operations.rename
			renames := 0
			artifact.dependencies.operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
				renames++
				if renames == 1 {
					if err := realRename(fromFD, from, toFD, to, flags); err != nil {
						return err
					}
					checkpointFallbackWriteTree(t, evidence.BackupPath, opaque)
					return nil
				}
				if renames != 2 {
					t.Fatalf("unexpected rename attempt %d", renames)
				}
				switch outcome {
				case "prior":
					return unix.EIO
				case "next":
					if err := realRename(fromFD, from, toFD, to, flags); err != nil {
						return err
					}
					return unix.EIO
				case "third":
					checkpointFallbackCreateTree(t, filepath.Join(input.Checkout.CanonicalPath, ".wormhole"), checkpointFallbackOpaqueTree(input.PriorTree, "appeared live"))
					return unix.EIO
				}
				panic("unreachable")
			}
			disposition, err := publishPreparedCheckpointArtifact(context.Background(), artifact)
			switch outcome {
			case "prior":
				if disposition != 0 || !errors.Is(err, unix.EIO) || errors.Is(err, ErrCheckpointRecoveryBlocked) {
					t.Fatalf("compensation prior = (%d, %v)", disposition, err)
				}
			case "next":
				if disposition != checkpointPublicationPreservedConcurrentOld || err != nil {
					t.Fatalf("compensation next = (%d, %v)", disposition, err)
				}
			case "third":
				if disposition != 0 || !errors.Is(err, ErrCheckpointRecoveryBlocked) {
					t.Fatalf("compensation third = (%d, %v)", disposition, err)
				}
			}
			if renames != 2 {
				t.Fatalf("compensation rename attempts = %d, want 2", renames)
			}
		})
	}
}

func TestCheckpointFallbackPublisherPreservesLaterLivePAfterLinearization(t *testing.T) {
	input := checkpointArtifactCandidateWithNestedFile(t)
	artifact := prepareCheckpointFallbackArtifact(t, input)
	defer artifact.close()
	livePath := filepath.Join(input.Checkout.CanonicalPath, ".wormhole")
	realRename := artifact.dependencies.operations.rename
	renames := 0
	artifact.dependencies.operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
		renames++
		if err := realRename(fromFD, from, toFD, to, flags); err != nil {
			return err
		}
		if renames == 2 {
			checkpointFallbackReplaceTree(t, livePath, input.PriorTree, ".retained-linearized-candidate")
		}
		return nil
	}
	disposition, err := publishPreparedCheckpointArtifact(context.Background(), artifact)
	if disposition != checkpointPublicationPublished || err != nil || renames != 2 {
		t.Fatalf("later live P = (%d, %v), renames %d", disposition, err, renames)
	}
	assertCheckpointPathTree(t, livePath, input.PriorTree)
	assertCheckpointPathTree(t, artifact.evidence().BackupPath, input.PriorTree)
	checkpointFallbackAssertAbsent(t, artifact.evidence().StagePath)
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
			if _, err := publishPreparedCheckpointArtifact(context.Background(), artifact); !errors.Is(err, injected) || seen != test.occurrence {
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
		topology        string
		success         bool
	}{
		{name: "backup rename unchanged error", renameCall: 1},
		{name: "backup rename mutated error", renameCall: 1, mutate: true, topology: "published", success: true},
		{name: "first live parent fsync", fsyncRole: "live", fsyncOccurrence: 1, topology: "backed-up"},
		{name: "first private parent fsync", fsyncRole: "private", fsyncOccurrence: 1, topology: "backed-up"},
		{name: "publish rename unchanged error", renameCall: 2, topology: "backed-up"},
		{name: "publish rename mutated error", renameCall: 2, mutate: true, topology: "published", success: true},
		{name: "final live parent fsync", fsyncRole: "live", fsyncOccurrence: 2, topology: "published"},
		{name: "final private parent fsync", fsyncRole: "private", fsyncOccurrence: 2, topology: "published"},
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
			disposition, err := publishPreparedCheckpointArtifact(context.Background(), artifact)
			if test.success {
				if disposition != checkpointPublicationPublished || err != nil {
					t.Fatalf("observably applied rename = (%d, %v)", disposition, err)
				}
			} else if disposition != 0 || err == nil {
				t.Fatalf("fallback syscall fault = (%d, %v), want zero and error", disposition, err)
			}
			switch test.topology {
			case "":
				assertCheckpointPreparedTopology(t, input, artifact.evidence())
			case "backed-up":
				assertCheckpointBackedUpTopology(t, input, artifact.evidence())
			case "published":
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
	go func() {
		_, err := publishPreparedCheckpointArtifact(context.Background(), artifact)
		published <- err
	}()
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

func checkpointFallbackOpaqueTree(tree state.Tree, marker string) state.Tree {
	opaque := checkpointArtifactCloneTree(tree)
	for index := range opaque {
		if opaque[index].Path == "config.toml" {
			opaque[index].Data = []byte("opaque = \"" + marker + "\"\n")
			return opaque
		}
	}
	panic("checkpoint test tree lacks config.toml")
}

func checkpointFallbackCountRenames(artifact *checkpointArtifact) *int {
	realRename := artifact.dependencies.operations.rename
	count := new(int)
	artifact.dependencies.operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
		*count++
		return realRename(fromFD, from, toFD, to, flags)
	}
	return count
}

func checkpointFallbackCreateTree(t *testing.T, path string, tree state.Tree) {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	checkpointFallbackWriteTree(t, path, tree)
}

func checkpointFallbackWriteTree(t *testing.T, path string, tree state.Tree) {
	t.Helper()
	for _, file := range tree {
		fullPath := filepath.Join(path, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, file.Data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func checkpointFallbackReplaceTree(t *testing.T, path string, tree state.Tree, retainedSuffix string) {
	t.Helper()
	retained := path + retainedSuffix
	if err := os.Rename(path, retained); err != nil {
		t.Fatal(err)
	}
	checkpointFallbackCreateTree(t, path, tree)
}

func checkpointFallbackAssertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path %q is not absent: %v", path, err)
	}
}
