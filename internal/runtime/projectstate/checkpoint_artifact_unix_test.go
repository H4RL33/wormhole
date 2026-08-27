//go:build linux

package projectstate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	probeID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	probeNames := checkpointArtifactProbeNames(probeID)
	for _, targetName := range []string{probeNames.file, probeNames.a, probeNames.b} {
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
			err = checkpointArtifactCapabilityProbe(private, checkpointArtifactDependencies{
				operations: operations,
				newProbeID: func() (string, error) { return probeID, nil },
			}, checkpointNoReplaceRenameFlag())
			if !errors.Is(err, ErrCheckpointUnsupported) {
				t.Fatalf("close failure = %v, want ErrCheckpointUnsupported", err)
			}
			if closeCalls != 1 {
				t.Fatalf("close calls after real-close-then-error = %d, want 1", closeCalls)
			}
		})
	}
}

type checkpointFDOwnershipRecorder struct {
	generation map[int]checkpointFDGeneration
	opened     map[int]int
	closed     map[int]int
	unowned    []int
}

type checkpointFDGeneration struct {
	live    bool
	tracked bool
}

func checkpointArtifactTrackedOperations(trackName func(string) bool) (checkpointArtifactPlatformOperations, *checkpointFDOwnershipRecorder) {
	operations := normalizeCheckpointArtifactRenameOperations(defaultCheckpointArtifactPlatformOperations())
	recorder := &checkpointFDOwnershipRecorder{
		generation: make(map[int]checkpointFDGeneration),
		opened:     make(map[int]int),
		closed:     make(map[int]int),
	}
	// Track only child descriptors acquired through the injected operations seam.
	// Checkout, Git, and live-root handles use their own direct Unix ownership.
	realOpenat, realClose := operations.openat, operations.close
	operations.openat = func(parentFD int, name string, flags int, mode uint32) (int, error) {
		fd, err := realOpenat(parentFD, name, flags, mode)
		if err == nil {
			tracked := trackName(name)
			recorder.generation[fd] = checkpointFDGeneration{live: true, tracked: tracked}
			if tracked {
				recorder.opened[fd]++
			}
		}
		return fd, err
	}
	operations.close = func(fd int) error {
		if generation, known := recorder.generation[fd]; known {
			if !generation.live {
				recorder.unowned = append(recorder.unowned, fd)
				return unix.EBADF
			}
			generation.live = false
			recorder.generation[fd] = generation
			if generation.tracked {
				recorder.closed[fd]++
			}
			return realClose(fd)
		}
		if fd == 0 {
			recorder.unowned = append(recorder.unowned, fd)
			return unix.EBADF
		}
		return realClose(fd)
	}
	return operations, recorder
}

func (recorder *checkpointFDOwnershipRecorder) assertBalanced(t *testing.T) {
	t.Helper()
	if len(recorder.unowned) != 0 {
		t.Fatalf("closed unowned descriptors: %v", recorder.unowned)
	}
	for fd, generation := range recorder.generation {
		if generation.live {
			t.Fatalf("descriptor %d has a live seam-open generation (tracked=%t)", fd, generation.tracked)
		}
	}
	for fd, opened := range recorder.opened {
		if recorder.closed[fd] != opened {
			t.Fatalf("descriptor %d opened %d times, closed %d times", fd, opened, recorder.closed[fd])
		}
	}
}

func TestCheckpointArtifactPreparationPrivateRootFailureOwnsOnlyAcquiredFDs(t *testing.T) {
	for _, boundary := range []string{"early stat", "post-open stat", "created parent fsync", "final path", "final stability"} {
		t.Run(boundary, func(t *testing.T) {
			input := checkpointArtifactTestInput(t)
			operations, recorder := checkpointArtifactTrackedOperations(func(name string) bool {
				return name == "wormhole" || name == "checkpoints"
			})
			cause := errors.New("private root " + boundary)
			privateRoot := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints")
			journalAllocated := false
			wormholeFD := -1
			switch boundary {
			case "early stat":
				realFstatat := operations.fstatat
				operations.fstatat = func(fd int, name string, stat *unix.Stat_t, flags int) error {
					if name == "wormhole" {
						return cause
					}
					return realFstatat(fd, name, stat, flags)
				}
			case "post-open stat":
				realOpenat, realFstat := operations.openat, operations.fstat
				operations.openat = func(fd int, name string, flags int, mode uint32) (int, error) {
					opened, err := realOpenat(fd, name, flags, mode)
					if err == nil && name == "wormhole" {
						wormholeFD = opened
					}
					return opened, err
				}
				operations.fstat = func(fd int, stat *unix.Stat_t) error {
					if fd == wormholeFD {
						return cause
					}
					return realFstat(fd, stat)
				}
			case "created parent fsync":
				realMkdirat, realFsync := operations.mkdirat, operations.fsync
				createdParentFD := -1
				operations.mkdirat = func(fd int, name string, mode uint32) error {
					err := realMkdirat(fd, name, mode)
					if err == nil && name == "wormhole" {
						createdParentFD = fd
					}
					return err
				}
				operations.fsync = func(fd int) error {
					if fd == createdParentFD {
						return cause
					}
					return realFsync(fd)
				}
			case "final path":
				git, openErr := openWorkingTreeRoot(filepath.Join(input.Checkout.CanonicalPath, ".git"))
				if openErr != nil {
					t.Fatal(openErr)
				}
				gitTerminal := git.ancestry[len(git.ancestry)-1]
				private, ancestors, openErr := openCheckpointPrivateRoot(gitTerminal, privateRoot+"-wrong", checkpointArtifactDependencies{operations: operations})
				if private.fd >= 0 {
					_ = operations.close(private.fd)
				}
				for index := len(ancestors) - 1; index >= 0; index-- {
					_ = operations.close(ancestors[index].fd)
				}
				git.close()
				if openErr == nil {
					t.Fatal("private root accepted wrong final path")
				}
				recorder.assertBalanced(t)
				return
			case "final stability":
				realOpenat, realFstat := operations.openat, operations.fstat
				checkpointsFD := -1
				operations.openat = func(fd int, name string, flags int, mode uint32) (int, error) {
					opened, err := realOpenat(fd, name, flags, mode)
					if err == nil && name == "checkpoints" {
						checkpointsFD = opened
					}
					return opened, err
				}
				operations.fstat = func(fd int, stat *unix.Stat_t) error {
					if err := realFstat(fd, stat); err != nil {
						return err
					}
					if fd == checkpointsFD {
						checkpointsFD = -1
						if err := os.Rename(privateRoot, privateRoot+"-retained"); err != nil {
							return err
						}
						if err := os.Mkdir(privateRoot, 0o700); err != nil {
							return err
						}
					}
					return nil
				}
			}
			artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, checkpointArtifactDependencies{
				readGit: readOnlyGitLimited, operations: operations,
				newJournalID: func() (string, error) { journalAllocated = true; return "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", nil },
			})
			if artifact != nil || err == nil || journalAllocated {
				t.Fatalf("private-root %s = (%v, %v), journal allocated=%t", boundary, artifact, err, journalAllocated)
			}
			if boundary != "final stability" && !errors.Is(err, cause) {
				t.Fatalf("private-root %s lost cause: %v", boundary, err)
			}
			recorder.assertBalanced(t)
			if boundary == "created parent fsync" {
				wormholePath := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole")
				if info, statErr := os.Lstat(wormholePath); statErr != nil || !info.IsDir() {
					t.Fatalf("retained applied-create %q = (%v, %v)", wormholePath, info, statErr)
				}
			}
			if boundary == "final stability" {
				for _, path := range []string{privateRoot, privateRoot + "-retained"} {
					if info, statErr := os.Lstat(path); statErr != nil || !info.IsDir() {
						t.Fatalf("retained stability path %q = (%v, %v)", path, info, statErr)
					}
				}
			}
		})
	}
}

func TestCheckpointArtifactPreparationSecondPrivateChildFailureClosesCurrentParent(t *testing.T) {
	for _, boundary := range []string{"initial stat", "unsafe shape", "mkdir", "post-create stat", "open", "post-open stat", "metadata mismatch", "created parent fsync"} {
		t.Run(boundary, func(t *testing.T) {
			input := checkpointArtifactTestInput(t)
			if boundary == "unsafe shape" {
				wormholePath := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole")
				if err := os.Mkdir(wormholePath, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(wormholePath, "checkpoints"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			operations, recorder := checkpointArtifactTrackedOperations(func(name string) bool {
				return name == "wormhole" || name == "checkpoints"
			})
			cause := errors.New("second private child " + boundary)
			checkpointsFD, checkpointsStats := -1, 0
			realFstatat, realOpenat, realFstat := operations.fstatat, operations.openat, operations.fstat
			operations.fstatat = func(fd int, name string, stat *unix.Stat_t, flags int) error {
				err := realFstatat(fd, name, stat, flags)
				if name == "checkpoints" {
					checkpointsStats++
					switch boundary {
					case "initial stat":
						return cause
					case "post-create stat":
						if checkpointsStats == 2 {
							return cause
						}
					case "metadata mismatch":
						if err == nil {
							stat.Ino++
						}
					}
				}
				return err
			}
			if boundary == "mkdir" {
				realMkdirat := operations.mkdirat
				operations.mkdirat = func(fd int, name string, mode uint32) error {
					if name == "checkpoints" {
						return cause
					}
					return realMkdirat(fd, name, mode)
				}
			}
			operations.openat = func(fd int, name string, flags int, mode uint32) (int, error) {
				if name == "checkpoints" && boundary == "open" {
					return -1, cause
				}
				opened, err := realOpenat(fd, name, flags, mode)
				if err == nil && name == "checkpoints" {
					checkpointsFD = opened
				}
				return opened, err
			}
			operations.fstat = func(fd int, stat *unix.Stat_t) error {
				if fd == checkpointsFD && boundary == "post-open stat" {
					return cause
				}
				return realFstat(fd, stat)
			}
			if boundary == "created parent fsync" {
				realFsync := operations.fsync
				operations.fsync = func(fd int) error {
					var stat unix.Stat_t
					if err := unix.Fstat(fd, &stat); err != nil {
						return err
					}
					wormholePath := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole")
					var wormhole unix.Stat_t
					if err := unix.Stat(wormholePath, &wormhole); err == nil && stat.Dev == wormhole.Dev && stat.Ino == wormhole.Ino {
						return cause
					}
					return realFsync(fd)
				}
			}

			journalAllocated := false
			artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, checkpointArtifactDependencies{
				readGit: readOnlyGitLimited, operations: operations,
				newJournalID: func() (string, error) { journalAllocated = true; return "abababab-abab-4bab-8bab-abababababab", nil },
			})
			if artifact != nil || err == nil || journalAllocated {
				t.Fatalf("second-child %s = (%v, %v), journal allocated=%t", boundary, artifact, err, journalAllocated)
			}
			if boundary != "unsafe shape" && boundary != "metadata mismatch" && !errors.Is(err, cause) {
				t.Fatalf("second-child %s lost cause: %v", boundary, err)
			}
			recorder.assertBalanced(t)
			if boundary == "created parent fsync" || boundary == "post-create stat" {
				checkpointsPath := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints")
				if info, statErr := os.Lstat(checkpointsPath); statErr != nil || !info.IsDir() {
					t.Fatalf("retained applied-create %q = (%v, %v)", checkpointsPath, info, statErr)
				}
			}
		})
	}
}

func TestCheckpointFDOwnershipRecorderRejectsDoubleCloseAndTracksReuse(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"child", "untracked"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	parentFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(parentFD)
	operations, recorder := checkpointArtifactTrackedOperations(func(name string) bool { return name == "child" })
	openChild := func(name string) int {
		fd, openErr := operations.openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if openErr != nil {
			t.Fatal(openErr)
		}
		return fd
	}
	firstFD := openChild("child")
	if err := operations.close(firstFD); err != nil {
		t.Fatal(err)
	}
	if err := operations.close(firstFD); !errors.Is(err, unix.EBADF) {
		t.Fatalf("double close = %v, want EBADF", err)
	}
	secondFD := openChild("untracked")
	if secondFD != firstFD {
		t.Fatalf("descriptor was not reused: first=%d second=%d", firstFD, secondFD)
	}
	if err := operations.close(secondFD); err != nil {
		t.Fatalf("close untracked reused generation: %v", err)
	}
	thirdFD := openChild("child")
	if thirdFD != firstFD {
		t.Fatalf("descriptor was not reused again: first=%d third=%d", firstFD, thirdFD)
	}
	if err := operations.close(thirdFD); err != nil {
		t.Fatalf("close tracked reused generation: %v", err)
	}
	if len(recorder.unowned) != 1 || recorder.unowned[0] != firstFD {
		t.Fatalf("unsafe closes = %v, want [%d]", recorder.unowned, firstFD)
	}
	recorder.unowned = nil
	recorder.assertBalanced(t)
}

func TestCheckpointArtifactPreparationStageOpenFailureOwnsOnlyAcquiredFDs(t *testing.T) {
	for _, boundary := range []string{"initial stat", "unsafe shape", "open", "post-open stat", "metadata mismatch"} {
		t.Run(boundary, func(t *testing.T) {
			input := checkpointArtifactTestInput(t)
			operations, recorder := checkpointArtifactTrackedOperations(func(name string) bool {
				return strings.HasSuffix(name, ".stage")
			})
			cause := errors.New("stage " + boundary)
			stageFD, stageStats := -1, 0
			realFstatat, realOpenat, realFstat := operations.fstatat, operations.openat, operations.fstat
			operations.fstatat = func(fd int, name string, stat *unix.Stat_t, flags int) error {
				err := realFstatat(fd, name, stat, flags)
				if strings.HasSuffix(name, ".stage") {
					stageStats++
					if stageStats == 2 {
						switch boundary {
						case "initial stat":
							return cause
						case "unsafe shape":
							stat.Mode = unix.S_IFDIR | 0o755
						}
					}
				}
				return err
			}
			operations.openat = func(fd int, name string, flags int, mode uint32) (int, error) {
				if strings.HasSuffix(name, ".stage") && boundary == "open" {
					return -1, cause
				}
				opened, err := realOpenat(fd, name, flags, mode)
				if err == nil && strings.HasSuffix(name, ".stage") {
					stageFD = opened
				}
				return opened, err
			}
			operations.fstat = func(fd int, stat *unix.Stat_t) error {
				if fd == stageFD {
					if boundary == "post-open stat" {
						return cause
					}
					if err := realFstat(fd, stat); err != nil {
						return err
					}
					if boundary == "metadata mismatch" {
						stat.Ino++
					}
					return nil
				}
				return realFstat(fd, stat)
			}
			if boundary == "post-open stat" {
				realClose := operations.close
				operations.close = func(fd int) error {
					err := realClose(fd)
					if err == nil && fd == stageFD {
						return unix.EIO
					}
					return err
				}
			}
			journalID := "ffffffff-ffff-4fff-8fff-ffffffffffff"
			artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, checkpointArtifactDependencies{
				readGit: readOnlyGitLimited, operations: operations,
				newJournalID: func() (string, error) { return journalID, nil },
			})
			if artifact != nil || err == nil {
				t.Fatalf("stage %s = (%v, %v)", boundary, artifact, err)
			}
			if (boundary == "initial stat" || boundary == "open" || boundary == "post-open stat") && !errors.Is(err, cause) {
				t.Fatalf("stage %s lost cause: %v", boundary, err)
			}
			recorder.assertBalanced(t)
			stagePath := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints", journalID+".stage")
			if info, statErr := os.Lstat(stagePath); statErr != nil || !info.IsDir() {
				t.Fatalf("retained stage = (%v, %v)", info, statErr)
			}
		})
	}
}

func TestCheckpointArtifactCapabilityProbeResidueDoesNotPoisonNextPreparation(t *testing.T) {
	firstProbeID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	secondProbeID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	journalID := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	firstNames := checkpointArtifactProbeNames(firstProbeID)
	tests := []struct {
		name   string
		inject func(*checkpointArtifactPlatformOperations, *bool)
	}{
		{name: "file fstat", inject: func(operations *checkpointArtifactPlatformOperations, injected *bool) {
			realOpenat, realFstat := operations.openat, operations.fstat
			fileFD := -1
			operations.openat = func(parentFD int, name string, flags int, mode uint32) (int, error) {
				fd, err := realOpenat(parentFD, name, flags, mode)
				if err == nil && name == firstNames.file {
					fileFD = fd
				}
				return fd, err
			}
			operations.fstat = func(fd int, stat *unix.Stat_t) error {
				if err := realFstat(fd, stat); err != nil {
					return err
				}
				if !*injected && fd == fileFD {
					*injected = true
					return unix.EIO
				}
				return nil
			}
		}},
		{name: "file fsync", inject: checkpointArtifactProbeFsyncFailure(firstNames.file)},
		{name: "file close", inject: checkpointArtifactProbeCloseFailure(firstNames.file)},
		{name: "first mkdir", inject: checkpointArtifactProbeMkdirFailure(firstNames.a)},
		{name: "second mkdir", inject: checkpointArtifactProbeMkdirFailure(firstNames.b)},
		{name: "occupied rename", inject: checkpointArtifactProbeRenameFailure(firstNames.a, firstNames.b)},
		{name: "forward rename", inject: checkpointArtifactProbeRenameFailure(firstNames.a, firstNames.c)},
		{name: "reverse rename", inject: checkpointArtifactProbeRenameFailure(firstNames.c, firstNames.a)},
		{name: "first directory close", inject: checkpointArtifactProbeCloseFailure(firstNames.a)},
		{name: "second directory close", inject: checkpointArtifactProbeCloseFailure(firstNames.b)},
		{name: "file unlink", inject: checkpointArtifactProbeUnlinkFailure(firstNames.file)},
		{name: "first directory unlink", inject: checkpointArtifactProbeUnlinkFailure(firstNames.a)},
		{name: "second directory unlink", inject: checkpointArtifactProbeUnlinkFailure(firstNames.b)},
		{name: "private fsync", inject: checkpointArtifactProbeFsyncFailure("")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := checkpointArtifactCandidateWithNestedFile(t)
			operations := normalizeCheckpointArtifactRenameOperations(defaultCheckpointArtifactPlatformOperations())
			injected := false
			test.inject(&operations, &injected)
			probeIDs := []string{firstProbeID, secondProbeID}
			probeAllocations, journalAllocations := 0, 0
			dependencies := checkpointArtifactDependencies{
				readGit:    readOnlyGitLimited,
				operations: operations,
				newProbeID: func() (string, error) {
					id := probeIDs[probeAllocations]
					probeAllocations++
					return id, nil
				},
				newJournalID: func() (string, error) {
					journalAllocations++
					return journalID, nil
				},
			}
			first, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, dependencies)
			if first != nil || !errors.Is(err, ErrCheckpointUnsupported) || !injected ||
				probeAllocations != 1 || journalAllocations != 0 {
				t.Fatalf("first preparation = (%v, %v), injected=%t probes=%d journals=%d", first, err, injected, probeAllocations, journalAllocations)
			}
			privateRoot := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints")
			before := checkpointArtifactProbeNamespaceSnapshot(t, privateRoot, firstProbeID)

			second, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, dependencies)
			if err != nil || second == nil || probeAllocations != 2 || journalAllocations != 1 {
				t.Fatalf("second preparation = (%v, %v), probes=%d journals=%d", second, err, probeAllocations, journalAllocations)
			}
			defer second.close()
			after := checkpointArtifactProbeNamespaceSnapshot(t, privateRoot, firstProbeID)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("second probe changed first residue\nbefore=%+v\nafter=%+v", before, after)
			}
			if got := checkpointArtifactProbeNamespaceSnapshot(t, privateRoot, secondProbeID); len(got) != 0 {
				t.Fatalf("second probe residue = %+v", got)
			}
			evidence := second.evidence()
			if evidence.JournalID != journalID {
				t.Fatalf("journal ID = %q, want %q", evidence.JournalID, journalID)
			}
			assertCheckpointPathTree(t, evidence.StagePath, input.CandidateTree)
		})
	}
}

func TestCheckpointArtifactProbeIdentityFailurePrecedesProbeAndJournalMutation(t *testing.T) {
	for _, test := range []struct {
		name     string
		allocate func() (string, error)
	}{
		{name: "allocation error", allocate: func() (string, error) { return "", errors.New("probe allocation failed") }},
		{name: "invalid ID", allocate: func() (string, error) { return "not-a-canonical-uuid", nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := checkpointArtifactTestInput(t)
			journalAllocated := false
			artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, checkpointArtifactDependencies{
				readGit:    readOnlyGitLimited,
				newProbeID: test.allocate,
				newJournalID: func() (string, error) {
					journalAllocated = true
					return "dddddddd-dddd-4ddd-8ddd-dddddddddddd", nil
				},
			})
			if artifact != nil || !errors.Is(err, ErrCheckpointUnsupported) || journalAllocated {
				t.Fatalf("probe identity failure = (%v, %v), journal allocated=%t", artifact, err, journalAllocated)
			}
			privateRoot := filepath.Join(input.Checkout.CanonicalPath, ".git", "wormhole", "checkpoints")
			entries, readErr := os.ReadDir(privateRoot)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("probe identity failure created private entries: %v", entries)
			}
		})
	}
}

func checkpointArtifactProbeFsyncFailure(targetName string) func(*checkpointArtifactPlatformOperations, *bool) {
	return func(operations *checkpointArtifactPlatformOperations, injected *bool) {
		realOpenat, realFsync := operations.openat, operations.fsync
		targetFD, privateFD := -1, -1
		operations.openat = func(parentFD int, name string, flags int, mode uint32) (int, error) {
			fd, err := realOpenat(parentFD, name, flags, mode)
			if name == targetName && err == nil {
				targetFD = fd
			}
			if strings.HasPrefix(name, ".checkpoint-probe-") {
				privateFD = parentFD
			}
			return fd, err
		}
		operations.fsync = func(fd int) error {
			if err := realFsync(fd); err != nil {
				return err
			}
			wantFD := targetFD
			if targetName == "" {
				wantFD = privateFD
			}
			if !*injected && wantFD >= 0 && fd == wantFD {
				*injected = true
				return unix.EIO
			}
			return nil
		}
	}
}

func checkpointArtifactProbeCloseFailure(targetName string) func(*checkpointArtifactPlatformOperations, *bool) {
	return func(operations *checkpointArtifactPlatformOperations, injected *bool) {
		realOpenat, realClose := operations.openat, operations.close
		targetFD := -1
		operations.openat = func(parentFD int, name string, flags int, mode uint32) (int, error) {
			fd, err := realOpenat(parentFD, name, flags, mode)
			if err == nil && name == targetName {
				targetFD = fd
			}
			return fd, err
		}
		operations.close = func(fd int) error {
			if err := realClose(fd); err != nil {
				return err
			}
			if !*injected && fd == targetFD {
				*injected = true
				return unix.EIO
			}
			return nil
		}
	}
}

func checkpointArtifactProbeMkdirFailure(targetName string) func(*checkpointArtifactPlatformOperations, *bool) {
	return func(operations *checkpointArtifactPlatformOperations, injected *bool) {
		realMkdirat := operations.mkdirat
		operations.mkdirat = func(parentFD int, name string, mode uint32) error {
			if err := realMkdirat(parentFD, name, mode); err != nil {
				return err
			}
			if !*injected && name == targetName {
				*injected = true
				return unix.EIO
			}
			return nil
		}
	}
}

func checkpointArtifactProbeRenameFailure(fromName, toName string) func(*checkpointArtifactPlatformOperations, *bool) {
	return func(operations *checkpointArtifactPlatformOperations, injected *bool) {
		realRename := operations.rename
		operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
			err := realRename(fromFD, from, toFD, to, flags)
			if !*injected && from == fromName && to == toName {
				*injected = true
				return unix.EIO
			}
			return err
		}
	}
}

func checkpointArtifactProbeUnlinkFailure(targetName string) func(*checkpointArtifactPlatformOperations, *bool) {
	return func(operations *checkpointArtifactPlatformOperations, injected *bool) {
		realUnlinkat := operations.unlinkat
		operations.unlinkat = func(parentFD int, name string, flags int) error {
			if err := realUnlinkat(parentFD, name, flags); err != nil {
				return err
			}
			if !*injected && name == targetName {
				*injected = true
				return unix.EIO
			}
			return nil
		}
	}
}

type checkpointArtifactProbeSnapshotEntry struct {
	name       string
	mode       os.FileMode
	device     uint64
	inode      uint64
	links      uint64
	fileBytes  []byte
	childNames []string
}

func checkpointArtifactProbeNamespaceSnapshot(t *testing.T, root, probeID string) []checkpointArtifactProbeSnapshotEntry {
	t.Helper()
	prefix := ".checkpoint-probe-" + probeID + "-"
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	result := make([]checkpointArtifactProbeSnapshotEntry, 0, 4)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatalf("probe entry %q has stat type %T", path, info.Sys())
		}
		snapshot := checkpointArtifactProbeSnapshotEntry{
			name: entry.Name(), mode: info.Mode(), device: uint64(stat.Dev), inode: stat.Ino, links: uint64(stat.Nlink),
		}
		if info.Mode().IsRegular() {
			snapshot.fileBytes, err = os.ReadFile(path)
		} else if info.IsDir() {
			children, readErr := os.ReadDir(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, child := range children {
				snapshot.childNames = append(snapshot.childNames, child.Name())
			}
		}
		result = append(result, snapshot)
	}
	return result
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
