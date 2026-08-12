//go:build linux

package projectstate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
	"golang.org/x/sys/unix"
)

func TestCheckpointArtifactPreparationValidatesBeforeGitResolution(t *testing.T) {
	valid := checkpointArtifactTestInput(t)
	otherProject := checkpointArtifactRetargetProject(t, valid, "00000000-0000-4000-8000-000000000002")
	otherRepository := checkpointArtifactRetargetRepository(t, valid, types.RepositoryIdentity{
		Provider: "github", ImmutableID: "2", CanonicalRemote: "https://github.com/acme/other",
	})
	rootIdentity, err := checkoutIdentity(string(filepath.Separator))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(checkpointArtifactInput) checkpointArtifactInput
	}{
		{name: "empty checkout", mutate: func(input checkpointArtifactInput) checkpointArtifactInput {
			input.Checkout = types.CheckoutIdentity{}
			return input
		}},
		{name: "relative checkout", mutate: func(input checkpointArtifactInput) checkpointArtifactInput {
			input.Checkout.CanonicalPath = "relative"
			return input
		}},
		{name: "unclean checkout", mutate: func(input checkpointArtifactInput) checkpointArtifactInput {
			input.Checkout.CanonicalPath += string(filepath.Separator) + "."
			return input
		}},
		{name: "filesystem root", mutate: func(input checkpointArtifactInput) checkpointArtifactInput {
			input.Checkout = rootIdentity
			return input
		}},
		{name: "zero device", mutate: func(input checkpointArtifactInput) checkpointArtifactInput { input.Checkout.Device = 0; return input }},
		{name: "zero inode", mutate: func(input checkpointArtifactInput) checkpointArtifactInput { input.Checkout.Inode = 0; return input }},
		{name: "wrong device", mutate: func(input checkpointArtifactInput) checkpointArtifactInput { input.Checkout.Device++; return input }},
		{name: "wrong inode", mutate: func(input checkpointArtifactInput) checkpointArtifactInput { input.Checkout.Inode++; return input }},
		{name: "nil prior", mutate: func(input checkpointArtifactInput) checkpointArtifactInput { input.PriorTree = nil; return input }},
		{name: "nil candidate", mutate: func(input checkpointArtifactInput) checkpointArtifactInput { input.CandidateTree = nil; return input }},
		{name: "unsorted prior", mutate: func(input checkpointArtifactInput) checkpointArtifactInput {
			input.PriorTree[0], input.PriorTree[1] = input.PriorTree[1], input.PriorTree[0]
			return input
		}},
		{name: "duplicate candidate", mutate: func(input checkpointArtifactInput) checkpointArtifactInput {
			input.CandidateTree = append(input.CandidateTree, input.CandidateTree[len(input.CandidateTree)-1])
			return input
		}},
		{name: "prior digest", mutate: func(input checkpointArtifactInput) checkpointArtifactInput {
			input.PriorTreeDigest = state.Digest("sha256:" + strings.Repeat("0", 64))
			return input
		}},
		{name: "candidate digest", mutate: func(input checkpointArtifactInput) checkpointArtifactInput {
			input.CandidateDigest = state.Digest("sha256:" + strings.Repeat("0", 64))
			return input
		}},
		{name: "project identity", mutate: func(input checkpointArtifactInput) checkpointArtifactInput {
			input.CandidateTree, input.CandidateDigest = otherProject.CandidateTree, otherProject.CandidateDigest
			return input
		}},
		{name: "repository identity", mutate: func(input checkpointArtifactInput) checkpointArtifactInput {
			input.CandidateTree, input.CandidateDigest = otherRepository.CandidateTree, otherRepository.CandidateDigest
			return input
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			input := checkpointArtifactCloneInput(valid)
			artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), test.mutate(input), checkpointArtifactDependencies{
				readGit: func(context.Context, string, int, ...string) ([]byte, error) {
					calls++
					return nil, errors.New("unexpected Git call")
				},
			})
			if err == nil || artifact != nil {
				t.Fatalf("prepare invalid input = (%v, %v), want nil artifact and error", artifact, err)
			}
			if calls != 0 {
				t.Fatalf("Git resolution calls = %d, want 0", calls)
			}
		})
	}
}

func TestCheckpointArtifactTreeProofOwnsIndependentCanonicalTrees(t *testing.T) {
	input := checkpointArtifactTestInput(t)
	proof, err := proveCheckpointArtifactTrees(input, defaultCheckpointArtifactTreeLimits())
	if err != nil {
		t.Fatal(err)
	}
	wantPrior := bytes.Clone(proof.prior.tree[0].Data)
	wantCandidate := bytes.Clone(proof.candidate.tree[0].Data)
	input.PriorTree[0].Data[0] ^= 0xff
	input.CandidateTree[0].Data[0] ^= 0xff
	input.PriorTree[0].Path = "changed"
	input.CandidateTree[0].Path = "changed"
	if proof.prior.tree[0].Path == "changed" || proof.candidate.tree[0].Path == "changed" ||
		!bytes.Equal(proof.prior.tree[0].Data, wantPrior) || !bytes.Equal(proof.candidate.tree[0].Data, wantCandidate) {
		t.Fatal("tree proof retained caller-owned paths or bytes")
	}
	proof.prior.tree[0].Data[0] ^= 0xff
	if bytes.Equal(proof.prior.tree[0].Data, proof.candidate.tree[0].Data) {
		t.Fatal("prior and candidate proofs share file bytes")
	}
}

func TestCheckpointArtifactTreeProofLimits(t *testing.T) {
	input := checkpointArtifactTestInput(t)
	defaults := defaultCheckpointArtifactTreeLimits()
	if defaults.maxFiles != 10_000 || defaults.maxPathBytes != 4<<10 || defaults.maxFileBytes != 16<<20 ||
		defaults.maxTotalBytes != 64<<20 || defaults.maxPathDepth != maxWorkingTreePathDepth ||
		defaults.maxDirectories != maxWorkingTreeDirectories {
		t.Fatalf("default checkpoint tree limits = %+v", defaults)
	}
	tests := []struct {
		name   string
		limits checkpointArtifactTreeLimits
	}{
		{name: "files", limits: checkpointArtifactTreeLimits{maxFiles: 1, maxDirectories: defaults.maxDirectories, maxPathBytes: defaults.maxPathBytes, maxPathDepth: defaults.maxPathDepth, maxFileBytes: defaults.maxFileBytes, maxTotalBytes: defaults.maxTotalBytes}},
		{name: "directories", limits: checkpointArtifactTreeLimits{maxFiles: defaults.maxFiles, maxDirectories: 1, maxPathBytes: defaults.maxPathBytes, maxPathDepth: defaults.maxPathDepth, maxFileBytes: defaults.maxFileBytes, maxTotalBytes: defaults.maxTotalBytes}},
		{name: "path bytes", limits: checkpointArtifactTreeLimits{maxFiles: defaults.maxFiles, maxDirectories: defaults.maxDirectories, maxPathBytes: 1, maxPathDepth: defaults.maxPathDepth, maxFileBytes: defaults.maxFileBytes, maxTotalBytes: defaults.maxTotalBytes}},
		{name: "path depth", limits: checkpointArtifactTreeLimits{maxFiles: defaults.maxFiles, maxDirectories: defaults.maxDirectories, maxPathBytes: defaults.maxPathBytes, maxPathDepth: 1, maxFileBytes: defaults.maxFileBytes, maxTotalBytes: defaults.maxTotalBytes}},
		{name: "file bytes", limits: checkpointArtifactTreeLimits{maxFiles: defaults.maxFiles, maxDirectories: defaults.maxDirectories, maxPathBytes: defaults.maxPathBytes, maxPathDepth: defaults.maxPathDepth, maxFileBytes: 1, maxTotalBytes: defaults.maxTotalBytes}},
		{name: "total bytes", limits: checkpointArtifactTreeLimits{maxFiles: defaults.maxFiles, maxDirectories: defaults.maxDirectories, maxPathBytes: defaults.maxPathBytes, maxPathDepth: defaults.maxPathDepth, maxFileBytes: defaults.maxFileBytes, maxTotalBytes: 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := proveCheckpointArtifactTrees(checkpointArtifactCloneInput(input), test.limits); err == nil {
				t.Fatal("tree proof accepted exceeded limit")
			}
		})
	}
}

func TestCheckpointArtifactGitPathsRealRepositories(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	paths, err := resolveCheckpointGitPaths(context.Background(), repository.root)
	if err != nil {
		t.Fatal(err)
	}
	wantGitDir := filepath.Join(repository.root, ".git")
	if paths.gitDir != wantGitDir || paths.checkpointRoot != filepath.Join(wantGitDir, "wormhole", "checkpoints") {
		t.Fatalf("normal Git paths = %+v", paths)
	}

	linked := filepath.Join(t.TempDir(), "linked")
	runGit(t, repository.root, "worktree", "add", "-b", "checkpoint-linked", linked)
	linkedPaths, err := resolveCheckpointGitPaths(context.Background(), linked)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(linkedPaths.gitDir) || linkedPaths.checkpointRoot != filepath.Join(linkedPaths.gitDir, "wormhole", "checkpoints") || linkedPaths.gitDir == wantGitDir {
		t.Fatalf("linked-worktree Git paths = %+v", linkedPaths)
	}
}

func TestCheckpointArtifactGitPathOutputRejected(t *testing.T) {
	for _, output := range []string{"/tmp/path", "/tmp/path\n"} {
		if _, err := parseCheckpointGitPathOutput([]byte(output)); err != nil {
			t.Fatalf("rejected valid Git path output %q: %v", output, err)
		}
	}
	tests := []string{"", "relative\n", "/tmp/path\nextra\n", "/tmp/pa\x00th\n", "/tmp/pa\rth\n", "/tmp/not/../clean\n", "/" + strings.Repeat("a", maxCheckpointGitPathBytes), "/" + strings.Repeat("a", maxCheckpointGitPathBytes) + "\n"}
	for _, output := range tests {
		if _, err := parseCheckpointGitPathOutput([]byte(output)); err == nil {
			t.Fatalf("accepted Git path output %q", output)
		}
	}
}

func TestCheckpointArtifactGitResolutionRejectsReboundTailAndOverlap(t *testing.T) {
	checkout := filepath.Join(string(filepath.Separator), "tmp", "checkout")
	tests := []struct {
		name    string
		outputs []string
	}{
		{name: "Git dir rebound", outputs: []string{"/tmp/git-a\n", "/tmp/git-a/wormhole/checkpoints\n", "/tmp/git-b\n"}},
		{name: "wrong tail", outputs: []string{"/tmp/git\n", "/tmp/elsewhere/wormhole/checkpoints\n", "/tmp/git\n"}},
		{name: "live overlap", outputs: []string{checkout + "/.wormhole\n", checkout + "/.wormhole/wormhole/checkpoints\n", checkout + "/.wormhole\n"}},
		{name: "live contains checkpoint root", outputs: []string{checkout + "/.wormhole/wormhole/checkpoints\n", checkout + "/.wormhole/wormhole/checkpoints/wormhole/checkpoints\n", checkout + "/.wormhole/wormhole/checkpoints\n"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			call := 0
			_, err := resolveCheckpointGitPathsWithReader(context.Background(), checkout, func(_ context.Context, root string, limit int, arguments ...string) ([]byte, error) {
				if root != checkout || limit != maxCheckpointGitPathOutputBytes {
					t.Fatalf("Git read = root %q limit %d", root, limit)
				}
				wantArgs := [][]string{{"rev-parse", "--path-format=absolute", "--git-dir"}, {"rev-parse", "--path-format=absolute", "--git-path", "wormhole/checkpoints"}, {"rev-parse", "--path-format=absolute", "--git-dir"}}
				if strings.Join(arguments, "\x00") != strings.Join(wantArgs[call], "\x00") {
					t.Fatalf("Git arguments = %q, want %q", arguments, wantArgs[call])
				}
				output := test.outputs[call]
				call++
				return []byte(output), nil
			})
			if err == nil {
				t.Fatal("accepted unsafe Git path resolution")
			}
		})
	}
}

func TestCheckpointArtifactRejectsUnsafePrivateRootsBeforeJournalAllocation(t *testing.T) {
	for _, kind := range []string{"symlink", "file", "mode"} {
		t.Run(kind, func(t *testing.T) {
			input := checkpointArtifactTestInput(t)
			path := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole")
			switch kind {
			case "symlink":
				if err := os.Symlink(t.TempDir(), path); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			allocated := false
			artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, checkpointArtifactDependencies{
				readGit:      readOnlyGitLimited,
				newJournalID: func() (string, error) { allocated = true; return "88888888-8888-4888-8888-888888888888", nil },
			})
			if artifact != nil || err == nil || allocated {
				t.Fatalf("unsafe private root = (%v, %v, allocated %t)", artifact, err, allocated)
			}
		})
	}
}

func TestCheckpointArtifactCapabilityFailuresPrecedeStageCreation(t *testing.T) {
	for _, kind := range []string{"fsync", "no-replace"} {
		t.Run(kind, func(t *testing.T) {
			input := checkpointArtifactTestInput(t)
			privateRoot := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints")
			if err := os.MkdirAll(privateRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Dir(privateRoot), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(privateRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			operations := normalizeCheckpointArtifactRenameOperations(defaultCheckpointArtifactPlatformOperations())
			if kind == "fsync" {
				operations.fsync = func(int) error { return unix.EIO }
			} else {
				realRename := operations.rename
				operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
					if flags == checkpointNoReplaceRenameFlag() {
						return unix.EIO
					}
					return realRename(fromFD, from, toFD, to, flags)
				}
			}
			allocated := false
			artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, checkpointArtifactDependencies{
				readGit: readOnlyGitLimited, operations: operations,
				newJournalID: func() (string, error) { allocated = true; return "99999999-9999-4999-8999-999999999999", nil },
			})
			if artifact != nil || !errors.Is(err, ErrCheckpointUnsupported) || allocated {
				t.Fatalf("capability failure = (%v, %v, allocated %t)", artifact, err, allocated)
			}
		})
	}
}

func TestCheckpointArtifactCapabilityProbeNeverRetriesCloseAfterError(t *testing.T) {
	for _, targetName := range []string{".checkpoint-probe-file", ".checkpoint-probe-a", ".checkpoint-probe-b"} {
		t.Run(targetName, func(t *testing.T) {
			path := t.TempDir()
			fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(fd)
			metadata, err := workingTreeFstat(fd)
			if err != nil {
				t.Fatal(err)
			}
			private := heldWorkingTreeDirectory{fd: fd, parentFD: -1, path: path, metadata: metadata}
			operations := normalizeCheckpointArtifactRenameOperations(defaultCheckpointArtifactPlatformOperations())
			realOpenat, realClose := operations.openat, operations.close
			targetFD, targetKnown, closeCalls := -1, false, 0
			operations.openat = func(parentFD int, name string, flags int, mode uint32) (int, error) {
				opened, openErr := realOpenat(parentFD, name, flags, mode)
				if openErr == nil && name == targetName {
					targetFD, targetKnown = opened, true
				}
				return opened, openErr
			}
			operations.close = func(closingFD int) error {
				if targetKnown && closingFD == targetFD {
					closeCalls++
					if err := realClose(closingFD); err != nil {
						return err
					}
					return unix.EIO
				}
				return realClose(closingFD)
			}
			err = checkpointArtifactCapabilityProbe(private, checkpointArtifactDependencies{operations: operations}, checkpointNoReplaceRenameFlag())
			if !errors.Is(err, ErrCheckpointUnsupported) {
				t.Fatalf("close failure = %v, want ErrCheckpointUnsupported", err)
			}
			if closeCalls != 1 {
				t.Fatalf("close calls after real-close-then-error = %d, want 1", closeCalls)
			}
		})
	}
}

func TestCheckpointArtifactValidPreparationStagesCandidate(t *testing.T) {
	input := checkpointArtifactTestInput(t)
	candidate, err := state.DecodeTree(input.CandidateTree)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Project.Name = "Checkpoint candidate"
	input.CandidateTree, err = state.EncodeTree(candidate)
	if err != nil {
		t.Fatal(err)
	}
	input.CandidateDigest, err = state.DigestTree(input.CandidateTree)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := prepareCheckpointArtifact(context.Background(), input)
	if err != nil || artifact == nil {
		t.Fatalf("prepare valid checkpoint artifact = (%v, %v)", artifact, err)
	}
	evidence := artifact.evidence()
	if !types.CanonicalUUID(evidence.JournalID) || evidence.StagePath != filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints", evidence.JournalID+".stage") ||
		evidence.BackupPath != filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints", evidence.JournalID+".backup") {
		t.Fatalf("artifact evidence = %+v", evidence)
	}
	if _, statErr := os.Stat(evidence.StagePath); statErr != nil {
		t.Fatalf("stage missing: %v", statErr)
	}
	staged, readErr := readCheckpointArtifactTree(context.Background(), artifact.stage, artifact.mountProof, artifact.dependencies.mount)
	if readErr != nil || !sameCheckpointArtifactTree(staged, input.CandidateTree) || sameCheckpointArtifactTree(staged, input.PriorTree) {
		t.Fatalf("staged candidate bytes = (%v, %v)", staged, readErr)
	}
	if _, statErr := os.Lstat(evidence.BackupPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("backup = %v, want absent", statErr)
	}
	privateRoot := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints")
	if info, statErr := os.Stat(privateRoot); statErr != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("private root = (%v, %v), want 0700 directory", info, statErr)
	}
	artifact.close()
	if _, statErr := os.Stat(evidence.StagePath); statErr != nil {
		t.Fatalf("close removed retained stage: %v", statErr)
	}
}

func TestCheckpointArtifactPreparationFaultBeforeStageRetainsNoStage(t *testing.T) {
	input := checkpointArtifactTestInput(t)
	journalID := "11111111-1111-4111-8111-111111111111"
	artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, checkpointArtifactDependencies{
		readGit: readOnlyGitLimited, newJournalID: func() (string, error) { return journalID, nil },
		fault: func(stage checkpointArtifactFaultStage) error {
			if stage == checkpointArtifactBeforeStageCreate {
				return errors.New("injected before stage")
			}
			return nil
		},
	})
	if artifact != nil || err == nil {
		t.Fatalf("faulted preparation = (%v, %v), want nil artifact and error", artifact, err)
	}
	stage := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints", journalID+".stage")
	if _, statErr := os.Lstat(stage); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("fault created stage: %v", statErr)
	}
}

func TestCheckpointArtifactStageCreationUsesPerCallOperations(t *testing.T) {
	input := checkpointArtifactTestInput(t)
	operations := defaultCheckpointArtifactPlatformOperations()
	wantErr := errors.New("injected stage mkdir")
	realMkdirat := operations.mkdirat
	operations.mkdirat = func(parentFD int, name string, mode uint32) error {
		if strings.HasSuffix(name, ".stage") {
			return wantErr
		}
		return realMkdirat(parentFD, name, mode)
	}
	artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, checkpointArtifactDependencies{
		readGit: readOnlyGitLimited, operations: operations,
	})
	if artifact != nil || !errors.Is(err, wantErr) {
		t.Fatalf("stage mkdir injection = (%v, %v), want (nil, injected error)", artifact, err)
	}
}

func TestCheckpointArtifactPreparationRetainsStageAtDurabilityFaults(t *testing.T) {
	for _, faultStage := range []checkpointArtifactFaultStage{
		checkpointArtifactAfterStageCreate,
		checkpointArtifactAfterMkdir,
		checkpointArtifactAfterWrite,
		checkpointArtifactAfterFileFsync,
		checkpointArtifactAfterDirectoryFsync,
		checkpointArtifactAfterPrivateFsync,
	} {
		t.Run(fmt.Sprint(faultStage), func(t *testing.T) {
			input := checkpointArtifactCandidateWithNestedFile(t)
			journalID := "22222222-2222-4222-8222-222222222222"
			injected := errors.New("injected durability fault")
			triggered := false
			artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, checkpointArtifactDependencies{
				readGit:      readOnlyGitLimited,
				newJournalID: func() (string, error) { return journalID, nil },
				fault: func(got checkpointArtifactFaultStage) error {
					if !triggered && got == faultStage {
						triggered = true
						return injected
					}
					return nil
				},
			})
			if artifact != nil || !errors.Is(err, injected) || !triggered {
				t.Fatalf("faulted preparation = (%v, %v, triggered %t)", artifact, err, triggered)
			}
			stage := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints", journalID+".stage")
			if info, statErr := os.Lstat(stage); statErr != nil || !info.IsDir() {
				t.Fatalf("retained partial stage = (%v, %v)", info, statErr)
			}
			backup := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints", journalID+".backup")
			if _, statErr := os.Lstat(backup); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("fault created backup: %v", statErr)
			}
		})
	}
}

func TestCheckpointArtifactPreparationEnforcesOwnerOnlyStageShape(t *testing.T) {
	input := checkpointArtifactCandidateWithNestedFile(t)
	artifact, err := prepareCheckpointArtifact(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.close()
	for _, relative := range []string{"", "state", "state/v1"} {
		info, statErr := os.Lstat(filepath.Join(artifact.evidence().StagePath, relative))
		if statErr != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			t.Fatalf("stage directory %q = (%v, %v), want exact 0700", relative, info, statErr)
		}
	}
	for _, file := range input.CandidateTree {
		info, statErr := os.Lstat(filepath.Join(artifact.evidence().StagePath, filepath.FromSlash(file.Path)))
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			t.Fatalf("stage file %q = (%v, %v), want exact 0600", file.Path, info, statErr)
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Nlink != 1 {
			t.Fatalf("stage file %q link metadata = %#v, want nlink 1", file.Path, info.Sys())
		}
	}
}

func TestCheckpointArtifactRejectsStageRootModeRaceBeforeMetadataRefresh(t *testing.T) {
	input := checkpointArtifactCandidateWithNestedFile(t)
	journalID := "55555555-5555-4555-8555-555555555555"
	changed := false
	artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, checkpointArtifactDependencies{
		readGit:      readOnlyGitLimited,
		newJournalID: func() (string, error) { return journalID, nil },
		fault: func(stage checkpointArtifactFaultStage) error {
			if !changed && stage == checkpointArtifactBeforeStageMetadataRefresh {
				changed = true
				path := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints", journalID+".stage")
				return os.Chmod(path, 0o755)
			}
			return nil
		},
	})
	if artifact != nil || err == nil || !changed {
		t.Fatalf("stage root mode race = (%v, %v, changed %t), want retained rejection", artifact, err, changed)
	}
	path := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints", journalID+".stage")
	if info, statErr := os.Lstat(path); statErr != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("retained raced stage = (%v, %v)", info, statErr)
	}
}

func TestCheckpointArtifactRejectsPrivateRootModeRaceAfterDurableStage(t *testing.T) {
	input := checkpointArtifactCandidateWithNestedFile(t)
	journalID := "66666666-6666-4666-8666-666666666666"
	changed := false
	artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, checkpointArtifactDependencies{
		readGit:      readOnlyGitLimited,
		newJournalID: func() (string, error) { return journalID, nil },
		fault: func(stage checkpointArtifactFaultStage) error {
			if !changed && stage == checkpointArtifactAfterPrivateFsync {
				changed = true
				root := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints")
				return os.Chmod(root, 0o755)
			}
			return nil
		},
	})
	if artifact != nil || err == nil || !changed {
		t.Fatalf("private root mode race = (%v, %v, changed %t)", artifact, err, changed)
	}
	stage := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints", journalID+".stage")
	if _, statErr := os.Lstat(stage); statErr != nil {
		t.Fatalf("private mode race removed durable stage: %v", statErr)
	}
}

func TestCheckpointArtifactRejectsPrivateRootRenameChurnAfterFsync(t *testing.T) {
	input := checkpointArtifactCandidateWithNestedFile(t)
	journalID := "67676767-6767-4767-8767-676767676767"
	seeded := false
	changed := false
	artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, checkpointArtifactDependencies{
		readGit:      readOnlyGitLimited,
		newJournalID: func() (string, error) { return journalID, nil },
		fault: func(stage checkpointArtifactFaultStage) error {
			root := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints")
			markerPath := filepath.Join(root, journalID+".marker")
			if !seeded && stage == checkpointArtifactBeforePrivateFsync {
				seeded = true
				return os.WriteFile(markerPath, []byte("marker"), 0o600)
			}
			if !changed && stage == checkpointArtifactAfterPrivateFsync {
				changed = true
				movedPath := filepath.Join(root, journalID+".moved")
				if err := os.Rename(markerPath, movedPath); err != nil {
					return err
				}
				return os.Rename(movedPath, markerPath)
			}
			return nil
		},
	})
	if artifact != nil || err == nil || !seeded || !changed {
		t.Fatalf("private root rename churn = (%v, %v, seeded %t, changed %t), want retained rejection", artifact, err, seeded, changed)
	}
	stagePath := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints", journalID+".stage")
	assertCheckpointPathTree(t, stagePath, input.CandidateTree)
}

func TestCheckpointArtifactRejectsPrivateAncestorRenameChurnAfterFsync(t *testing.T) {
	input := checkpointArtifactCandidateWithNestedFile(t)
	journalID := "68686868-6868-4686-8686-686868686868"
	changed := false
	artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, checkpointArtifactDependencies{
		readGit:      readOnlyGitLimited,
		newJournalID: func() (string, error) { return journalID, nil },
		fault: func(stage checkpointArtifactFaultStage) error {
			if !changed && stage == checkpointArtifactAfterPrivateFsync {
				changed = true
				gitDir := filepath.Join(input.Checkout.CanonicalPath, ".git")
				path := filepath.Join(gitDir, "wormhole")
				moved := filepath.Join(gitDir, "wormhole-moved")
				if err := os.Rename(path, moved); err != nil {
					return err
				}
				return os.Rename(moved, path)
			}
			return nil
		},
	})
	if artifact != nil || err == nil || !changed {
		t.Fatalf("private ancestor rename churn = (%v, %v, changed %t), want retained rejection", artifact, err, changed)
	}
	stagePath := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints", journalID+".stage")
	assertCheckpointPathTree(t, stagePath, input.CandidateTree)
}

func TestCheckpointArtifactRejectsExtraEmptyStageDirectory(t *testing.T) {
	input := checkpointArtifactCandidateWithNestedFile(t)
	journalID := "77777777-7777-4777-8777-777777777777"
	changed := false
	artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, checkpointArtifactDependencies{
		readGit:      readOnlyGitLimited,
		newJournalID: func() (string, error) { return journalID, nil },
		fault: func(stage checkpointArtifactFaultStage) error {
			if !changed && stage == checkpointArtifactAfterPrivateFsync {
				changed = true
				stagePath := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints", journalID+".stage")
				return os.Mkdir(filepath.Join(stagePath, "unexpected-empty"), 0o700)
			}
			return nil
		},
	})
	if artifact != nil || err == nil || !changed {
		t.Fatalf("extra empty stage directory = (%v, %v, changed %t)", artifact, err, changed)
	}
	extra := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints", journalID+".stage", "unexpected-empty")
	if info, statErr := os.Lstat(extra); statErr != nil || !info.IsDir() {
		t.Fatalf("extra empty directory residue = (%v, %v)", info, statErr)
	}
}

func TestCheckpointArtifactRejectsByteIdenticalFileReplacementAfterFsync(t *testing.T) {
	input := checkpointArtifactCandidateWithNestedFile(t)
	journalID := "78787878-7878-4787-8787-787878787878"
	mutated := false
	artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, checkpointArtifactDependencies{
		readGit:      readOnlyGitLimited,
		newJournalID: func() (string, error) { return journalID, nil },
		fault: func(stage checkpointArtifactFaultStage) error {
			if mutated || stage != checkpointArtifactAfterFileFsync {
				return nil
			}
			mutated = true
			root := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints", journalID+".stage")
			path := filepath.Join(root, "config.toml")
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			replacement := filepath.Join(root, ".replacement")
			if err := os.WriteFile(replacement, contents, 0o600); err != nil {
				return err
			}
			return os.Rename(replacement, path)
		},
	})
	if artifact != nil || err == nil || !mutated {
		t.Fatalf("byte-identical file replacement = (%v, %v, mutated %t), want retained rejection", artifact, err, mutated)
	}
	assertCheckpointPathTree(t, filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints", journalID+".stage"), input.CandidateTree)
}

func TestCheckpointArtifactRejectsByteIdenticalFileRewriteAfterFsync(t *testing.T) {
	input := checkpointArtifactCandidateWithNestedFile(t)
	journalID := "79797979-7979-4797-8797-797979797979"
	mutated := false
	artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, checkpointArtifactDependencies{
		readGit:      readOnlyGitLimited,
		newJournalID: func() (string, error) { return journalID, nil },
		fault: func(stage checkpointArtifactFaultStage) error {
			if mutated || stage != checkpointArtifactAfterFileFsync {
				return nil
			}
			mutated = true
			path := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints", journalID+".stage", "config.toml")
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(path, contents, 0o600)
		},
	})
	if artifact != nil || err == nil || !mutated {
		t.Fatalf("byte-identical file rewrite = (%v, %v, mutated %t), want retained rejection", artifact, err, mutated)
	}
	assertCheckpointPathTree(t, filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints", journalID+".stage"), input.CandidateTree)
}

func TestCheckpointArtifactRejectsByteIdenticalNestedDirectoryRebindAfterFsync(t *testing.T) {
	input := checkpointArtifactCandidateWithNestedFile(t)
	journalID := "80808080-8080-4808-8808-808080808080"
	mutated := false
	artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, checkpointArtifactDependencies{
		readGit:      readOnlyGitLimited,
		newJournalID: func() (string, error) { return journalID, nil },
		fault: func(stage checkpointArtifactFaultStage) error {
			if mutated || stage != checkpointArtifactAfterDirectoryFsync {
				return nil
			}
			mutated = true
			stateRoot := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints", journalID+".stage", "state")
			path := filepath.Join(stateRoot, "v1")
			old := filepath.Join(stateRoot, "v1-old")
			contents, err := os.ReadFile(filepath.Join(path, "project.json"))
			if err != nil {
				return err
			}
			if err := os.Rename(path, old); err != nil {
				return err
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(path, "project.json"), contents, 0o600); err != nil {
				return err
			}
			if err := os.Remove(filepath.Join(old, "project.json")); err != nil {
				return err
			}
			return os.Remove(old)
		},
	})
	if artifact != nil || err == nil || !mutated {
		t.Fatalf("byte-identical nested directory rebind = (%v, %v, mutated %t), want retained rejection", artifact, err, mutated)
	}
	assertCheckpointPathTree(t, filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints", journalID+".stage"), input.CandidateTree)
}

func TestCheckpointArtifactOwnerProofRejectsFstatInterleavingChmod(t *testing.T) {
	root := t.TempDir()
	childPath := filepath.Join(root, "child")
	if err := os.Mkdir(childPath, 0o700); err != nil {
		t.Fatal(err)
	}
	handle, err := openWorkingTreeRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.close()
	parent := handle.ancestry[len(handle.ancestry)-1]
	operations := defaultCheckpointArtifactPlatformOperations()
	child, err := openCheckpointArtifactChildDirectoryWithOperations(parent, "child", operations)
	if err != nil {
		t.Fatal(err)
	}
	defer operations.close(child.fd)
	realFstat := operations.fstat
	changed := false
	operations.fstat = func(fd int, stat *unix.Stat_t) error {
		if err := realFstat(fd, stat); err != nil {
			return err
		}
		if !changed && fd == child.fd {
			changed = true
			return os.Chmod(childPath, 0o755)
		}
		return nil
	}
	if err := checkpointArtifactRequireHeldDirectoryPath(parent.fd, "child", &child, operations, true); err == nil || !changed {
		t.Fatalf("interleaving owner proof = (%v, changed %t), want rejection", err, changed)
	}
}

func TestCheckpointArtifactDurableDirectoryProofRejectsFstatInterleavingRebind(t *testing.T) {
	root := t.TempDir()
	childPath := filepath.Join(root, "child")
	if err := os.Mkdir(childPath, 0o700); err != nil {
		t.Fatal(err)
	}
	handle, err := openWorkingTreeRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.close()
	parent := handle.ancestry[len(handle.ancestry)-1]
	operations := defaultCheckpointArtifactPlatformOperations()
	child, err := openCheckpointArtifactChildDirectoryWithOperations(parent, "child", operations)
	if err != nil {
		t.Fatal(err)
	}
	defer operations.close(child.fd)
	durable, err := checkpointArtifactFstatMetadata(child.fd, operations)
	if err != nil {
		t.Fatal(err)
	}
	realFstat := operations.fstat
	changed := false
	operations.fstat = func(fd int, stat *unix.Stat_t) error {
		if err := realFstat(fd, stat); err != nil {
			return err
		}
		if !changed && fd == child.fd {
			changed = true
			if err := os.Rename(childPath, childPath+"-old"); err != nil {
				return err
			}
			return os.Mkdir(childPath, 0o700)
		}
		return nil
	}
	if err := checkpointArtifactRequireDurableDirectory(child, durable, operations, true); err == nil || !changed {
		t.Fatalf("interleaving durable proof = (%v, changed %t), want rejection", err, changed)
	}
}

func TestCheckpointArtifactCancellationAfterStageCreationRetainsStage(t *testing.T) {
	input := checkpointArtifactTestInput(t)
	journalID := "33333333-3333-4333-8333-333333333333"
	ctx, cancel := context.WithCancel(context.Background())
	artifact, err := prepareCheckpointArtifactWithDependencies(ctx, input, checkpointArtifactDependencies{
		readGit:      readOnlyGitLimited,
		newJournalID: func() (string, error) { return journalID, nil },
		fault: func(stage checkpointArtifactFaultStage) error {
			if stage == checkpointArtifactAfterStageCreate {
				cancel()
			}
			return nil
		},
	})
	if artifact != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled preparation = (%v, %v), want retained stage and context.Canceled", artifact, err)
	}
	if info, statErr := os.Lstat(filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints", journalID+".stage")); statErr != nil || !info.IsDir() {
		t.Fatalf("cancelled retained stage = (%v, %v)", info, statErr)
	}
}

func TestCheckpointArtifactCancellationAfterFinalBackupProofRetainsStage(t *testing.T) {
	input := checkpointArtifactCandidateWithNestedFile(t)
	journalID := "34343434-3434-4343-8343-343434343434"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	operations := defaultCheckpointArtifactPlatformOperations()
	realFstatat := operations.fstatat
	backupChecks := 0
	operations.fstatat = func(parentFD int, name string, stat *unix.Stat_t, flags int) error {
		err := realFstatat(parentFD, name, stat, flags)
		if name == journalID+".backup" && errors.Is(err, unix.ENOENT) {
			backupChecks++
			if backupChecks == 2 {
				cancel()
			}
		}
		return err
	}
	artifact, err := prepareCheckpointArtifactWithDependencies(ctx, input, checkpointArtifactDependencies{
		readGit: readOnlyGitLimited, operations: operations,
		newJournalID: func() (string, error) { return journalID, nil },
	})
	if artifact != nil || !errors.Is(err, context.Canceled) || backupChecks != 2 {
		t.Fatalf("final-proof cancellation = (%v, %v, checks %d), want nil/context.Canceled/2", artifact, err, backupChecks)
	}
	stagePath := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints", journalID+".stage")
	assertCheckpointPathTree(t, stagePath, input.CandidateTree)
}

func TestCheckpointArtifactLiveCASFailureRetainsExactCandidate(t *testing.T) {
	input := checkpointArtifactCandidateWithNestedFile(t)
	journalID := "44444444-4444-4444-8444-444444444444"
	mutated := false
	artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, checkpointArtifactDependencies{
		readGit:      readOnlyGitLimited,
		newJournalID: func() (string, error) { return journalID, nil },
		fault: func(stage checkpointArtifactFaultStage) error {
			if !mutated && stage == checkpointArtifactAfterPrivateFsync {
				mutated = true
				return os.WriteFile(filepath.Join(input.Checkout.CanonicalPath, ".wormhole", "config.toml"), []byte("changed\n"), 0o600)
			}
			return nil
		},
	})
	if artifact != nil || !errors.Is(err, ErrCheckpointCAS) || !mutated {
		t.Fatalf("live CAS change = (%v, %v, mutated %t), want nil ErrCheckpointCAS", artifact, err, mutated)
	}
	stagePath := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints", journalID+".stage")
	if got, readErr := readCheckpointArtifactPathTree(context.Background(), stagePath); readErr != nil || !sameCheckpointArtifactTree(got, input.CandidateTree) {
		t.Fatalf("retained candidate = (%v, %v)", got, readErr)
	}
	if _, statErr := os.Lstat(filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints", journalID+".backup")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("CAS failure created backup: %v", statErr)
	}
}

func TestCheckpointArtifactLiveDirectoryReplacementReturnsCAS(t *testing.T) {
	input := checkpointArtifactCandidateWithNestedFile(t)
	journalID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	replaced := false
	artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, checkpointArtifactDependencies{
		readGit:      readOnlyGitLimited,
		newJournalID: func() (string, error) { return journalID, nil },
		fault: func(stage checkpointArtifactFaultStage) error {
			if !replaced && stage == checkpointArtifactAfterPrivateFsync {
				replaced = true
				live := filepath.Join(input.Checkout.CanonicalPath, ".wormhole")
				if err := os.Rename(live, live+"-old"); err != nil {
					return err
				}
				return os.Mkdir(live, 0o755)
			}
			return nil
		},
	})
	if artifact != nil || !errors.Is(err, ErrCheckpointCAS) || !replaced {
		t.Fatalf("live replacement = (%v, %v, replaced %t), want nil ErrCheckpointCAS", artifact, err, replaced)
	}
	stage := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints", journalID+".stage")
	assertCheckpointPathTree(t, stage, input.CandidateTree)
}

func TestCheckpointArtifactBackupCollisionPreservesEntry(t *testing.T) {
	input := checkpointArtifactTestInput(t)
	journalID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	root := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(root, journalID+".backup")
	if err := os.WriteFile(backup, []byte("collision"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, checkpointArtifactDependencies{
		readGit: readOnlyGitLimited, newJournalID: func() (string, error) { return journalID, nil },
	})
	if artifact != nil || err == nil {
		t.Fatalf("backup collision = (%v, %v)", artifact, err)
	}
	if got, readErr := os.ReadFile(backup); readErr != nil || string(got) != "collision" {
		t.Fatalf("backup collision bytes = (%q, %v)", got, readErr)
	}
}

func TestCheckpointArtifactBackupCollisionAfterStageCreationRetainsAllEvidence(t *testing.T) {
	input := checkpointArtifactCandidateWithNestedFile(t)
	journalID := "bcbcbcbc-bcbc-4bcb-8bcb-bcbcbcbcbcbc"
	collided := false
	artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, checkpointArtifactDependencies{
		readGit:      readOnlyGitLimited,
		newJournalID: func() (string, error) { return journalID, nil },
		fault: func(stage checkpointArtifactFaultStage) error {
			if !collided && stage == checkpointArtifactAfterStageCreate {
				collided = true
				backup := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints", journalID+".backup")
				return os.WriteFile(backup, []byte("collision"), 0o600)
			}
			return nil
		},
	})
	if artifact != nil || err == nil || !collided {
		t.Fatalf("late backup collision = (%v, %v, collided %t), want retained rejection", artifact, err, collided)
	}
	root := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints")
	assertCheckpointPathTree(t, filepath.Join(root, journalID+".stage"), input.CandidateTree)
	if got, readErr := os.ReadFile(filepath.Join(root, journalID+".backup")); readErr != nil || string(got) != "collision" {
		t.Fatalf("retained backup collision = (%q, %v)", got, readErr)
	}
}

func checkpointArtifactCandidateWithNestedFile(t *testing.T) checkpointArtifactInput {
	t.Helper()
	input := checkpointArtifactTestInput(t)
	snapshot, err := state.DecodeTree(input.CandidateTree)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Project.Name = "Candidate"
	input.CandidateTree, err = state.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	input.CandidateDigest, err = state.DigestTree(input.CandidateTree)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func readCheckpointArtifactPathTree(ctx context.Context, path string) (state.Tree, error) {
	handle, err := openWorkingTreeRoot(path)
	if err != nil {
		return nil, err
	}
	defer handle.close()
	root := handle.ancestry[len(handle.ancestry)-1]
	mount := normalizeCheckpointArtifactMountOperations(checkpointArtifactMountOperations{})
	mountID, unique, err := checkpointLinuxTryUniqueMount(root.fd, mount)
	if err != nil {
		return nil, err
	}
	if mountID == 0 {
		mountID, err = checkpointLinuxLegacyMount(root.fd, mount)
		if err != nil {
			return nil, err
		}
	}
	return readCheckpointArtifactTree(ctx, root, checkpointMountProof{checkout: mountID, unique: unique}, mount)
}

func assertCheckpointPublishedTopology(t *testing.T, input checkpointArtifactInput, evidence checkpointArtifactEvidence) {
	t.Helper()
	live, err := readCheckpointArtifactPathTree(context.Background(), filepath.Join(input.Checkout.CanonicalPath, ".wormhole"))
	if err != nil || !sameCheckpointArtifactTree(live, input.CandidateTree) {
		t.Fatalf("published live tree = (%v, %v)", live, err)
	}
	backup, err := readCheckpointArtifactPathTree(context.Background(), evidence.BackupPath)
	if err != nil || !sameCheckpointArtifactTree(backup, input.PriorTree) {
		t.Fatalf("published backup tree = (%v, %v)", backup, err)
	}
	if _, err := os.Lstat(evidence.StagePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published stage path remains: %v", err)
	}
}

func TestCheckpointArtifactRejectsInvalidInjectedJournalID(t *testing.T) {
	input := checkpointArtifactTestInput(t)
	artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, checkpointArtifactDependencies{
		readGit: readOnlyGitLimited, newJournalID: func() (string, error) { return "00000000-0000-0000-0000-000000000000", nil },
	})
	if artifact != nil || err == nil {
		t.Fatalf("invalid journal ID = (%v, %v), want nil artifact and error", artifact, err)
	}
}

func TestCheckpointArtifactJournalCollisionPreservesStage(t *testing.T) {
	input := checkpointArtifactTestInput(t)
	journalID := "11111111-1111-1111-8111-111111111111"
	dependencies := checkpointArtifactDependencies{readGit: readOnlyGitLimited, newJournalID: func() (string, error) { return journalID, nil }}
	first, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	stage := first.evidence().StagePath
	first.close()
	before, err := os.ReadFile(filepath.Join(stage, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, dependencies)
	if second != nil || err == nil {
		t.Fatalf("collision = (%v, %v), want nil and error", second, err)
	}
	after, err := os.ReadFile(filepath.Join(stage, "config.toml"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("collision changed retained stage: %v", err)
	}
}

func TestCheckpointArtifactCancelledBeforePreparationDoesNotCreatePrivateRoot(t *testing.T) {
	input := checkpointArtifactTestInput(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	artifact, err := prepareCheckpointArtifact(ctx, input)
	if artifact != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled preparation = (%v, %v)", artifact, err)
	}
	privateRoot := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole")
	if _, statErr := os.Lstat(privateRoot); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("cancelled preparation created private root: %v", statErr)
	}
}

func checkpointArtifactTestInput(t *testing.T) checkpointArtifactInput {
	t.Helper()
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	checkout, err := checkoutIdentity(repository.root)
	if err != nil {
		t.Fatal(err)
	}
	tree := testSnapshotTree(t, repository.projectID, repository.identity)
	digest, err := state.DigestTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	return checkpointArtifactInput{Checkout: checkout, PriorTree: tree, PriorTreeDigest: digest, CandidateTree: checkpointArtifactCloneTree(tree), CandidateDigest: digest}
}

func checkpointArtifactCloneInput(input checkpointArtifactInput) checkpointArtifactInput {
	input.PriorTree = checkpointArtifactCloneTree(input.PriorTree)
	input.CandidateTree = checkpointArtifactCloneTree(input.CandidateTree)
	return input
}

func checkpointArtifactRetargetProject(t *testing.T, input checkpointArtifactInput, projectID string) checkpointArtifactInput {
	t.Helper()
	snapshot, err := state.DecodeTree(input.CandidateTree)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Config.ProjectID = projectID
	snapshot.Project.ID = projectID
	input.CandidateTree, err = state.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	input.CandidateDigest, err = state.DigestTree(input.CandidateTree)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func checkpointArtifactRetargetRepository(t *testing.T, input checkpointArtifactInput, repository types.RepositoryIdentity) checkpointArtifactInput {
	t.Helper()
	snapshot, err := state.DecodeTree(input.CandidateTree)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Config.Repository = repository
	input.CandidateTree, err = state.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	input.CandidateDigest, err = state.DigestTree(input.CandidateTree)
	if err != nil {
		t.Fatal(err)
	}
	return input
}
