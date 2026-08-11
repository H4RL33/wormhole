//go:build linux

package projectstate

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
	"golang.org/x/sys/unix"
)

const (
	maxCheckpointGitPathBytes       = 4 << 10
	maxCheckpointGitPathOutputBytes = maxCheckpointGitPathBytes + 1
)

type checkpointArtifactDependencies struct {
	readGit      func(context.Context, string, int, ...string) ([]byte, error)
	newJournalID func() (string, error)
	fault        func(checkpointArtifactFaultStage) error
	operations   checkpointArtifactPlatformOperations
	mount        checkpointArtifactMountOperations
}

type checkpointArtifactPlatformOperations struct {
	mkdirat  func(int, string, uint32) error
	openat   func(int, string, int, uint32) (int, error)
	fstatat  func(int, string, *unix.Stat_t, int) error
	fstat    func(int, *unix.Stat_t) error
	fsync    func(int) error
	write    func(int, []byte) (int, error)
	close    func(int) error
	unlinkat func(int, string, int) error
	rename   func(int, string, int, string, uint) error
}

func defaultCheckpointArtifactPlatformOperations() checkpointArtifactPlatformOperations {
	return checkpointArtifactPlatformOperations{
		mkdirat: unix.Mkdirat, openat: unix.Openat, fstatat: unix.Fstatat, fstat: unix.Fstat,
		fsync: unix.Fsync, write: unix.Write, close: unix.Close, unlinkat: unix.Unlinkat,
	}
}

func normalizeCheckpointArtifactOperations(operations checkpointArtifactPlatformOperations) checkpointArtifactPlatformOperations {
	defaults := defaultCheckpointArtifactPlatformOperations()
	if operations.mkdirat == nil {
		operations.mkdirat = defaults.mkdirat
	}
	if operations.openat == nil {
		operations.openat = defaults.openat
	}
	if operations.fstatat == nil {
		operations.fstatat = defaults.fstatat
	}
	if operations.fsync == nil {
		operations.fsync = defaults.fsync
	}
	if operations.fstat == nil {
		operations.fstat = defaults.fstat
	}
	if operations.write == nil {
		operations.write = defaults.write
	}
	if operations.close == nil {
		operations.close = defaults.close
	}
	if operations.unlinkat == nil {
		operations.unlinkat = defaults.unlinkat
	}
	return operations
}

type checkpointArtifactFaultStage uint8

const (
	checkpointArtifactBeforeCapabilityFreeze checkpointArtifactFaultStage = iota + 1
	checkpointArtifactBeforeStageCreate
	checkpointArtifactBeforeRender
	checkpointArtifactAfterStageCreate
	checkpointArtifactBeforeMkdir
	checkpointArtifactAfterMkdir
	checkpointArtifactBeforeWrite
	checkpointArtifactAfterWrite
	checkpointArtifactBeforeFileFsync
	checkpointArtifactAfterFileFsync
	checkpointArtifactBeforeDirectoryFsync
	checkpointArtifactAfterDirectoryFsync
	checkpointArtifactBeforePrivateFsync
	checkpointArtifactAfterPrivateFsync
	checkpointArtifactBeforeLiveMutation
	checkpointArtifactAfterLiveMutation
	checkpointArtifactBeforeSecondLiveMutation
	checkpointArtifactAfterSecondLiveMutation
	checkpointArtifactBeforeLiveParentFsync
	checkpointArtifactAfterLiveParentFsync
	checkpointArtifactBeforePrivateParentFsync
	checkpointArtifactAfterPrivateParentFsync
	checkpointArtifactBeforeStageMetadataRefresh
	checkpointArtifactAfterStageMetadataRefresh
)

type checkpointArtifactTreeLimits struct {
	maxFiles       int
	maxDirectories int
	maxPathBytes   int
	maxPathDepth   int
	maxFileBytes   int64
	maxTotalBytes  int64
}

type checkpointArtifactTreeProof struct {
	tree     state.Tree
	snapshot state.Snapshot
	digest   state.Digest
}

type checkpointArtifactTreesProof struct {
	prior     checkpointArtifactTreeProof
	candidate checkpointArtifactTreeProof
}

type checkpointArtifactDurableStageProof struct {
	files       map[string]workingTreeMetadata
	directories map[string]workingTreeMetadata
	private     workingTreeMetadata
}

type checkpointArtifactPublicationParentsProof struct {
	liveParent workingTreeMetadata
	private    workingTreeMetadata
}

type checkpointGitPaths struct {
	gitDir         string
	checkpointRoot string
}

type checkpointArtifact struct {
	mu               sync.Mutex
	evidenceValue    checkpointArtifactEvidence
	closed           bool
	claimed          bool
	checkout         *workingTreeRootHandle
	live             heldWorkingTreeDirectory
	git              *workingTreeRootHandle
	private          heldWorkingTreeDirectory
	privateAncestors []heldWorkingTreeDirectory
	stage            heldWorkingTreeDirectory
	proof            checkpointArtifactTreesProof
	checkoutIdentity types.CheckoutIdentity
	paths            checkpointGitPaths
	mountProof       checkpointMountProof
	durableProof     checkpointArtifactDurableStageProof
	dependencies     checkpointArtifactDependencies
}

func prepareCheckpointArtifact(ctx context.Context, input checkpointArtifactInput) (*checkpointArtifact, error) {
	return prepareCheckpointArtifactWithDependencies(ctx, input, checkpointArtifactDependencies{readGit: readOnlyGitLimited, newJournalID: newCheckpointArtifactJournalID})
}

func prepareCheckpointArtifactWithDependencies(ctx context.Context, input checkpointArtifactInput, dependencies checkpointArtifactDependencies) (*checkpointArtifact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	checkout, err := openCheckpointArtifactCheckout(input.Checkout)
	if err != nil {
		return nil, err
	}
	defer func() {
		if checkout != nil {
			checkout.close()
		}
	}()
	proof, err := proveCheckpointArtifactTrees(input, defaultCheckpointArtifactTreeLimits())
	if err != nil {
		return nil, err
	}
	readGit := dependencies.readGit
	if readGit == nil {
		readGit = readOnlyGitLimited
	}
	dependencies.operations = normalizeCheckpointArtifactOperations(dependencies.operations)
	dependencies.operations = normalizeCheckpointArtifactRenameOperations(dependencies.operations)
	dependencies.mount = normalizeCheckpointArtifactMountOperations(dependencies.mount)
	if dependencies.newJournalID == nil {
		dependencies.newJournalID = newCheckpointArtifactJournalID
	}
	paths, err := resolveCheckpointGitPathsWithReader(ctx, input.Checkout.CanonicalPath, readGit)
	if err != nil {
		return nil, err
	}
	dependencies.readGit = readGit
	artifact, err := prepareCheckpointArtifactFilesystem(ctx, input, proof, paths, checkout, dependencies)
	if err == nil {
		checkout = nil
	}
	return artifact, err
}

func validateCheckpointArtifactCheckout(checkout types.CheckoutIdentity) error {
	handle, err := openCheckpointArtifactCheckout(checkout)
	if err != nil {
		return err
	}
	handle.close()
	return nil
}

func openCheckpointArtifactCheckout(checkout types.CheckoutIdentity) (*workingTreeRootHandle, error) {
	if checkout.CanonicalPath == "" || !filepath.IsAbs(checkout.CanonicalPath) ||
		filepath.Clean(checkout.CanonicalPath) != checkout.CanonicalPath ||
		checkout.CanonicalPath == string(filepath.Separator) || checkout.Device == 0 || checkout.Inode == 0 {
		return nil, fmt.Errorf("projectstate: invalid checkpoint checkout identity")
	}
	handle, err := openWorkingTreeRoot(checkout.CanonicalPath)
	if err != nil {
		return nil, fmt.Errorf("projectstate: open checkpoint checkout: %w", err)
	}
	terminal := handle.ancestry[len(handle.ancestry)-1]
	if terminal.metadata.device != checkout.Device || terminal.metadata.inode != checkout.Inode {
		handle.close()
		return nil, fmt.Errorf("projectstate: checkpoint checkout identity differs from opened root")
	}
	if err := handle.revalidate(); err != nil {
		handle.close()
		return nil, fmt.Errorf("projectstate: revalidate checkpoint checkout: %w", err)
	}
	return handle, nil
}

func defaultCheckpointArtifactTreeLimits() checkpointArtifactTreeLimits {
	return checkpointArtifactTreeLimits{
		maxFiles: 10_000, maxDirectories: maxWorkingTreeDirectories,
		maxPathBytes: 4 << 10, maxPathDepth: maxWorkingTreePathDepth,
		maxFileBytes: 16 << 20, maxTotalBytes: 64 << 20,
	}
}

func proveCheckpointArtifactTrees(input checkpointArtifactInput, limits checkpointArtifactTreeLimits) (checkpointArtifactTreesProof, error) {
	if limits.maxFiles <= 0 || limits.maxDirectories <= 0 || limits.maxPathBytes <= 0 || limits.maxPathDepth <= 0 || limits.maxFileBytes <= 0 || limits.maxTotalBytes <= 0 {
		return checkpointArtifactTreesProof{}, fmt.Errorf("projectstate: invalid checkpoint tree limits")
	}
	prior, err := proveCheckpointArtifactTree(input.PriorTree, input.PriorTreeDigest, limits)
	if err != nil {
		return checkpointArtifactTreesProof{}, fmt.Errorf("projectstate: invalid checkpoint prior tree: %w", err)
	}
	candidate, err := proveCheckpointArtifactTree(input.CandidateTree, input.CandidateDigest, limits)
	if err != nil {
		return checkpointArtifactTreesProof{}, fmt.Errorf("projectstate: invalid checkpoint candidate tree: %w", err)
	}
	if prior.snapshot.Config.ProjectID != candidate.snapshot.Config.ProjectID ||
		prior.snapshot.Config.Repository != candidate.snapshot.Config.Repository {
		return checkpointArtifactTreesProof{}, fmt.Errorf("projectstate: checkpoint trees have different project or repository identity")
	}
	return checkpointArtifactTreesProof{prior: prior, candidate: candidate}, nil
}

func proveCheckpointArtifactTree(tree state.Tree, recorded state.Digest, limits checkpointArtifactTreeLimits) (checkpointArtifactTreeProof, error) {
	if tree == nil {
		return checkpointArtifactTreeProof{}, fmt.Errorf("tree is nil")
	}
	if err := validateCheckpointArtifactTreeLimits(tree, limits); err != nil {
		return checkpointArtifactTreeProof{}, err
	}
	snapshot, err := state.DecodeTree(tree)
	if err != nil {
		return checkpointArtifactTreeProof{}, err
	}
	canonical, err := state.EncodeTree(snapshot)
	if err != nil {
		return checkpointArtifactTreeProof{}, err
	}
	if !sameCheckpointArtifactTree(tree, canonical) {
		return checkpointArtifactTreeProof{}, fmt.Errorf("tree is not exactly canonical")
	}
	digest, err := state.DigestTree(tree)
	if err != nil {
		return checkpointArtifactTreeProof{}, err
	}
	if digest != recorded {
		return checkpointArtifactTreeProof{}, fmt.Errorf("tree digest differs from recorded digest")
	}
	return checkpointArtifactTreeProof{tree: cloneCheckpointArtifactTree(canonical), snapshot: snapshot, digest: digest}, nil
}

func validateCheckpointArtifactTreeLimits(tree state.Tree, limits checkpointArtifactTreeLimits) error {
	if len(tree) > limits.maxFiles {
		return fmt.Errorf("tree exceeds file count limit")
	}
	total := int64(0)
	directories := map[string]struct{}{".": {}}
	for _, file := range tree {
		if err := validateWorkingTreeRelativePath(file.Path, workingTreeLimits{maxPathBytes: limits.maxPathBytes, maxPathDepth: limits.maxPathDepth}); err != nil {
			return err
		}
		if int64(len(file.Data)) > limits.maxFileBytes {
			return fmt.Errorf("tree file exceeds size limit")
		}
		total += int64(len(file.Path)) + int64(len(file.Data))
		if total > limits.maxTotalBytes {
			return fmt.Errorf("tree exceeds aggregate size limit")
		}
		for directory := filepath.ToSlash(filepath.Dir(file.Path)); directory != "."; directory = filepath.ToSlash(filepath.Dir(directory)) {
			directories[directory] = struct{}{}
		}
	}
	if len(directories) > limits.maxDirectories {
		return fmt.Errorf("tree exceeds directory count limit")
	}
	return nil
}

func cloneCheckpointArtifactTree(tree state.Tree) state.Tree {
	clone := make(state.Tree, len(tree))
	for index, file := range tree {
		clone[index] = state.File{Path: file.Path, Data: bytes.Clone(file.Data)}
	}
	return clone
}

func checkpointArtifactCloneTree(tree state.Tree) state.Tree {
	return cloneCheckpointArtifactTree(tree)
}

func sameCheckpointArtifactTree(left, right state.Tree) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Path != right[index].Path || !bytes.Equal(left[index].Data, right[index].Data) {
			return false
		}
	}
	return true
}

func resolveCheckpointGitPaths(ctx context.Context, checkout string) (checkpointGitPaths, error) {
	return resolveCheckpointGitPathsWithReader(ctx, checkout, readOnlyGitLimited)
}

func resolveCheckpointGitPathsWithReader(ctx context.Context, checkout string, readGit func(context.Context, string, int, ...string) ([]byte, error)) (checkpointGitPaths, error) {
	if readGit == nil {
		return checkpointGitPaths{}, fmt.Errorf("projectstate: missing checkpoint Git reader")
	}
	gitDirOutput, err := readGit(ctx, checkout, maxCheckpointGitPathOutputBytes, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return checkpointGitPaths{}, fmt.Errorf("projectstate: resolve checkpoint Git directory: %w", err)
	}
	gitDir, err := parseCheckpointGitPathOutput(gitDirOutput)
	if err != nil {
		return checkpointGitPaths{}, fmt.Errorf("projectstate: parse checkpoint Git directory: %w", err)
	}
	checkpointOutput, err := readGit(ctx, checkout, maxCheckpointGitPathOutputBytes, "rev-parse", "--path-format=absolute", "--git-path", "wormhole/checkpoints")
	if err != nil {
		return checkpointGitPaths{}, fmt.Errorf("projectstate: resolve checkpoint Git path: %w", err)
	}
	checkpointRoot, err := parseCheckpointGitPathOutput(checkpointOutput)
	if err != nil {
		return checkpointGitPaths{}, fmt.Errorf("projectstate: parse checkpoint Git path: %w", err)
	}
	gitDirAgainOutput, err := readGit(ctx, checkout, maxCheckpointGitPathOutputBytes, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return checkpointGitPaths{}, fmt.Errorf("projectstate: re-resolve checkpoint Git directory: %w", err)
	}
	gitDirAgain, err := parseCheckpointGitPathOutput(gitDirAgainOutput)
	if err != nil || gitDirAgain != gitDir {
		return checkpointGitPaths{}, fmt.Errorf("projectstate: checkpoint Git directory changed during resolution")
	}
	if checkpointRoot != filepath.Join(gitDir, "wormhole", "checkpoints") {
		return checkpointGitPaths{}, fmt.Errorf("projectstate: checkpoint Git path is not derived from Git directory")
	}
	live := filepath.Join(checkout, ".wormhole")
	if checkpointRoot == live || checkpointPathWithin(checkpointRoot, live) || checkpointPathWithin(live, checkpointRoot) {
		return checkpointGitPaths{}, fmt.Errorf("projectstate: checkpoint private root overlaps live working tree")
	}
	return checkpointGitPaths{gitDir: gitDir, checkpointRoot: checkpointRoot}, nil
}

func parseCheckpointGitPathOutput(output []byte) (string, error) {
	if len(output) == 0 || len(output) > maxCheckpointGitPathOutputBytes {
		return "", fmt.Errorf("invalid checkpoint Git path output size")
	}
	if output[len(output)-1] == '\n' {
		output = output[:len(output)-1]
	}
	if len(output) == 0 || len(output) > maxCheckpointGitPathBytes || strings.ContainsAny(string(output), "\x00\r\n") {
		return "", fmt.Errorf("invalid checkpoint Git path output")
	}
	value := string(output)
	if !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return "", fmt.Errorf("checkpoint Git path is not absolute and clean")
	}
	return value, nil
}

func checkpointPathWithin(path, parent string) bool {
	relative, err := filepath.Rel(parent, path)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func prepareCheckpointArtifactFilesystem(ctx context.Context, input checkpointArtifactInput, proof checkpointArtifactTreesProof, paths checkpointGitPaths, checkout *workingTreeRootHandle, dependencies checkpointArtifactDependencies) (_ *checkpointArtifact, resultErr error) {
	cleanup := func(handle *workingTreeRootHandle, directory heldWorkingTreeDirectory) {
		if directory.fd >= 0 {
			_ = dependencies.operations.close(directory.fd)
		}
		if handle != nil {
			handle.close()
		}
	}
	var git *workingTreeRootHandle
	var private, stage, live heldWorkingTreeDirectory
	var privateAncestors []heldWorkingTreeDirectory
	private.fd, stage.fd, live.fd = -1, -1, -1
	defer func() {
		if resultErr != nil {
			cleanup(git, private)
			for index := len(privateAncestors) - 1; index >= 0; index-- {
				_ = dependencies.operations.close(privateAncestors[index].fd)
			}
			cleanup(nil, stage)
			cleanup(nil, live)
		}
		if resultErr == nil {
			checkout = nil
			git = nil
			private.fd = -1
			stage.fd = -1
			live.fd = -1
		}
	}()

	terminal := checkout.ancestry[len(checkout.ancestry)-1]
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	openedLive, exists, err := openWormholeDirectory(terminal.fd)
	if err != nil || !exists {
		if err == nil {
			err = fmt.Errorf("projectstate: live .wormhole is absent")
		}
		return nil, err
	}
	live = openedLive
	git, err = openWorkingTreeRoot(paths.gitDir)
	if err != nil {
		return nil, fmt.Errorf("projectstate: open checkpoint Git directory: %w", err)
	}
	gitTerminal := git.ancestry[len(git.ancestry)-1]
	private, privateAncestors, err = openCheckpointPrivateRoot(gitTerminal, paths.checkpointRoot, dependencies)
	if err != nil {
		return nil, err
	}
	for index := range privateAncestors {
		baseline, baselineErr := checkpointArtifactFstatMetadata(privateAncestors[index].fd, dependencies.operations)
		if baselineErr != nil {
			return nil, baselineErr
		}
		if baselineErr := checkpointArtifactRequireDurableDirectory(privateAncestors[index], baseline, dependencies.operations, true); baselineErr != nil {
			return nil, fmt.Errorf("projectstate: freeze checkpoint private ancestry: %w", baselineErr)
		}
		privateAncestors[index].metadata = baseline
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := checkout.revalidate(); err != nil {
		return nil, err
	}
	if err := revalidateWorkingTreeDirectory(live); err != nil {
		return nil, err
	}
	if err := git.revalidate(); err != nil {
		return nil, err
	}
	if err := revalidateWorkingTreeDirectory(private); err != nil {
		return nil, err
	}
	if terminal.metadata.device != live.metadata.device || live.metadata.device != private.metadata.device {
		return nil, fmt.Errorf("%w: checkpoint paths use different devices", ErrCheckpointUnsupported)
	}
	if err := checkpointArtifactFault(dependencies, checkpointArtifactBeforeCapabilityFreeze); err != nil {
		return nil, err
	}
	mountProof, err := freezeCheckpointPlatformCapabilities(terminal.fd, live.fd, private, filepath.Join(input.Checkout.CanonicalPath, ".wormhole"), dependencies)
	if err != nil {
		return nil, err
	}
	journalID, err := dependencies.newJournalID()
	if err != nil {
		return nil, err
	}
	if !types.CanonicalUUID(journalID) {
		return nil, fmt.Errorf("projectstate: generated invalid checkpoint journal ID")
	}
	stageName, backupName := journalID+".stage", journalID+".backup"
	if err := checkpointArtifactNameAbsent(private.fd, stageName, dependencies.operations); err != nil {
		return nil, err
	}
	if err := checkpointArtifactNameAbsent(private.fd, backupName, dependencies.operations); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := checkpointArtifactFault(dependencies, checkpointArtifactBeforeStageCreate); err != nil {
		return nil, err
	}
	if err := dependencies.operations.mkdirat(private.fd, stageName, 0o700); err != nil {
		return nil, fmt.Errorf("projectstate: create checkpoint stage: %w", err)
	}
	if err := checkpointArtifactFault(dependencies, checkpointArtifactAfterStageCreate); err != nil {
		return nil, err
	}
	stage, err = openCheckpointArtifactChildDirectoryWithOperations(private, stageName, dependencies.operations)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := checkpointArtifactFault(dependencies, checkpointArtifactBeforeRender); err != nil {
		return nil, err
	}
	durableProof, err := renderCheckpointArtifactTree(ctx, stage, proof.candidate.tree, dependencies)
	if err != nil {
		return nil, err
	}
	if err := checkpointArtifactFault(dependencies, checkpointArtifactBeforeStageMetadataRefresh); err != nil {
		return nil, err
	}
	refreshedStage, err := checkpointArtifactFstatMetadata(stage.fd, dependencies.operations)
	if err != nil {
		return nil, err
	}
	if refreshedStage.mode&unix.S_IFMT != unix.S_IFDIR || refreshedStage.uid != uint32(unix.Geteuid()) || refreshedStage.mode&0o777 != 0o700 || refreshedStage.mode&0o7000 != 0 ||
		!sameWorkingTreeDirectoryIdentity(refreshedStage, stage.metadata) {
		return nil, fmt.Errorf("%w: unsafe checkpoint stage root after render", ErrCheckpointUnsupported)
	}
	stage.metadata = refreshedStage
	if err := revalidateWorkingTreeDirectory(stage); err != nil {
		return nil, err
	}
	if err := checkpointArtifactFault(dependencies, checkpointArtifactAfterStageMetadataRefresh); err != nil {
		return nil, err
	}
	if err := checkpointArtifactFault(dependencies, checkpointArtifactBeforeDirectoryFsync); err != nil {
		return nil, err
	}
	if err := dependencies.operations.fsync(stage.fd); err != nil {
		return nil, fmt.Errorf("projectstate: fsync checkpoint stage: %w", err)
	}
	durableStageRoot, err := checkpointArtifactFstatMetadata(stage.fd, dependencies.operations)
	if err != nil {
		return nil, err
	}
	if err := checkpointArtifactRequireFullDirectoryPath(stage.parentFD, stage.name, stage.metadata, durableStageRoot, dependencies.operations, true); err != nil {
		return nil, err
	}
	durableProof.directories["."] = durableStageRoot
	stage.metadata = durableStageRoot
	if err := checkpointArtifactFault(dependencies, checkpointArtifactAfterDirectoryFsync); err != nil {
		return nil, err
	}
	if err := checkpointArtifactFault(dependencies, checkpointArtifactBeforePrivateFsync); err != nil {
		return nil, err
	}
	if err := dependencies.operations.fsync(private.fd); err != nil {
		return nil, fmt.Errorf("projectstate: fsync checkpoint private root: %w", err)
	}
	durablePrivate, err := checkpointArtifactFstatMetadata(private.fd, dependencies.operations)
	if err != nil {
		return nil, err
	}
	if err := checkpointArtifactRequireFullDirectoryPath(private.parentFD, private.name, private.metadata, durablePrivate, dependencies.operations, true); err != nil {
		return nil, err
	}
	durableProof.private = durablePrivate
	private.metadata = durablePrivate
	if err := checkpointArtifactFault(dependencies, checkpointArtifactAfterPrivateFsync); err != nil {
		return nil, err
	}
	if err := checkpointArtifactRequireDurableDirectory(private, durableProof.private, dependencies.operations, true); err != nil {
		return nil, fmt.Errorf("projectstate: checkpoint private root changed after fsync: %w", err)
	}
	readback, err := readCheckpointArtifactTree(ctx, stage)
	if err != nil || !sameCheckpointArtifactTree(readback, proof.candidate.tree) {
		if err == nil {
			err = fmt.Errorf("checkpoint stage bytes differ")
		}
		return nil, fmt.Errorf("projectstate: verify checkpoint stage: %w", err)
	}
	if err := verifyCheckpointArtifactStageFiles(ctx, stage, proof.candidate.tree, durableProof, true, dependencies.operations); err != nil {
		return nil, err
	}
	liveTree, err := readCheckpointArtifactTree(ctx, live)
	if err != nil || !sameCheckpointArtifactTree(liveTree, proof.prior.tree) {
		return nil, ErrCheckpointCAS
	}
	for index := range privateAncestors {
		if err := checkpointArtifactRequireDurableDirectory(privateAncestors[index], privateAncestors[index].metadata, dependencies.operations, true); err != nil {
			return nil, fmt.Errorf("projectstate: checkpoint private ancestry changed after freeze: %w", err)
		}
	}
	private.metadata, err = checkpointArtifactValidateOwnerDirectory(private, dependencies.operations)
	if err != nil {
		return nil, err
	}
	if private.metadata != durableProof.private {
		return nil, fmt.Errorf("%w: checkpoint private root changed after fsync", ErrCheckpointUnsupported)
	}
	if err := checkout.revalidate(); err != nil {
		return nil, err
	}
	if err := git.revalidate(); err != nil {
		return nil, err
	}
	for _, ancestor := range privateAncestors {
		if err := revalidateWorkingTreeDirectory(ancestor); err != nil {
			return nil, err
		}
	}
	if err := revalidateWorkingTreeDirectory(private); err != nil {
		return nil, err
	}
	if err := revalidateWorkingTreeDirectory(stage); err != nil {
		return nil, err
	}
	if err := revalidateWorkingTreeDirectory(live); err != nil {
		return nil, err
	}
	if err := checkpointArtifactNameAbsent(private.fd, backupName, dependencies.operations); err != nil {
		return nil, fmt.Errorf("projectstate: checkpoint backup appeared during preparation: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	artifact := &checkpointArtifact{checkout: checkout, live: live, git: git, private: private, privateAncestors: privateAncestors, stage: stage, proof: proof,
		checkoutIdentity: input.Checkout, paths: paths, mountProof: mountProof, durableProof: durableProof, dependencies: dependencies,
		evidenceValue: checkpointArtifactEvidence{JournalID: journalID, StagePath: filepath.Join(paths.checkpointRoot, stageName), BackupPath: filepath.Join(paths.checkpointRoot, backupName)}}
	checkout, git = nil, nil
	privateAncestors = nil
	private.fd, stage.fd, live.fd = -1, -1, -1
	return artifact, nil
}

func openCheckpointPrivateRoot(git heldWorkingTreeDirectory, expected string, dependencies checkpointArtifactDependencies) (heldWorkingTreeDirectory, []heldWorkingTreeDirectory, error) {
	if !filepath.IsAbs(expected) || filepath.Clean(expected) != expected {
		return heldWorkingTreeDirectory{}, nil, fmt.Errorf("projectstate: checkpoint private root changed")
	}
	parent := git
	ancestors := make([]heldWorkingTreeDirectory, 0, 1)
	for _, name := range []string{"wormhole", "checkpoints"} {
		child, created, err := openOrCreateCheckpointPrivateDirectory(parent, name, dependencies.operations)
		if err != nil {
			for index := len(ancestors) - 1; index >= 0; index-- {
				_ = dependencies.operations.close(ancestors[index].fd)
			}
			return heldWorkingTreeDirectory{}, nil, err
		}
		if created {
			if err := dependencies.operations.fsync(parent.fd); err != nil {
				_ = dependencies.operations.close(child.fd)
				for index := len(ancestors) - 1; index >= 0; index-- {
					_ = dependencies.operations.close(ancestors[index].fd)
				}
				return heldWorkingTreeDirectory{}, nil, err
			}
		}
		if parent.fd != git.fd {
			ancestors = append(ancestors, parent)
		}
		parent = child
	}
	if filepath.Base(expected) != parent.name || filepath.Base(filepath.Dir(expected)) != "wormhole" {
		_ = dependencies.operations.close(parent.fd)
		for index := len(ancestors) - 1; index >= 0; index-- {
			_ = dependencies.operations.close(ancestors[index].fd)
		}
		return heldWorkingTreeDirectory{}, nil, fmt.Errorf("projectstate: checkpoint private root path changed")
	}
	if err := revalidateWorkingTreeDirectory(parent); err != nil {
		_ = dependencies.operations.close(parent.fd)
		for index := len(ancestors) - 1; index >= 0; index-- {
			_ = dependencies.operations.close(ancestors[index].fd)
		}
		return heldWorkingTreeDirectory{}, nil, err
	}
	return parent, ancestors, nil
}

func openOrCreateCheckpointPrivateDirectory(parent heldWorkingTreeDirectory, name string, operations checkpointArtifactPlatformOperations) (heldWorkingTreeDirectory, bool, error) {
	var stat unix.Stat_t
	created := false
	err := operations.fstatat(parent.fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		if err = operations.mkdirat(parent.fd, name, 0o700); err != nil {
			return heldWorkingTreeDirectory{}, false, err
		}
		created = true
		if err = operations.fstatat(parent.fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return heldWorkingTreeDirectory{}, false, err
		}
	} else if err != nil {
		return heldWorkingTreeDirectory{}, false, err
	}
	metadata := workingTreeStatMetadata(&stat)
	if metadata.mode&unix.S_IFMT != unix.S_IFDIR || metadata.uid != uint32(unix.Geteuid()) || metadata.mode&0o777 != 0o700 || metadata.mode&0o7000 != 0 {
		return heldWorkingTreeDirectory{}, false, fmt.Errorf("%w: unsafe checkpoint private directory", ErrCheckpointUnsupported)
	}
	fd, err := operations.openat(parent.fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return heldWorkingTreeDirectory{}, false, err
	}
	var openedStat unix.Stat_t
	err = operations.fstat(fd, &openedStat)
	opened := workingTreeStatMetadata(&openedStat)
	if err != nil || !sameWorkingTreeDirectory(opened, metadata) {
		_ = operations.close(fd)
		if err == nil {
			err = fmt.Errorf("checkpoint private directory changed")
		}
		return heldWorkingTreeDirectory{}, false, err
	}
	return heldWorkingTreeDirectory{fd: fd, parentFD: parent.fd, name: name, path: filepath.Join(parent.path, name), metadata: opened}, created, nil
}

func checkpointArtifactNameAbsent(parentFD int, name string, operations checkpointArtifactPlatformOperations) error {
	var stat unix.Stat_t
	err := operations.fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err == nil {
		return fmt.Errorf("projectstate: checkpoint artifact name already exists")
	}
	return err
}

func checkpointArtifactCapabilityProbe(private heldWorkingTreeDirectory, dependencies checkpointArtifactDependencies, noReplaceFlag uint) error {
	operations := dependencies.operations
	const fileName = ".checkpoint-probe-file"
	fileFD, err := operations.openat(private.fd, fileName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("%w: create regular-file fsync probe: %v", ErrCheckpointUnsupported, err)
	}
	fileClosed := false
	defer func() {
		if !fileClosed {
			_ = operations.close(fileFD)
		}
	}()
	var fileStat unix.Stat_t
	if err := operations.fstat(fileFD, &fileStat); err != nil {
		return fmt.Errorf("%w: stat regular-file fsync probe: %v", ErrCheckpointUnsupported, err)
	}
	fileMetadata := workingTreeStatMetadata(&fileStat)
	if fileMetadata.mode&unix.S_IFMT != unix.S_IFREG || fileMetadata.uid != uint32(unix.Geteuid()) || fileMetadata.links != 1 || fileMetadata.mode&0o777 != 0o600 || fileMetadata.mode&0o7000 != 0 {
		return fmt.Errorf("%w: unsafe regular-file fsync probe", ErrCheckpointUnsupported)
	}
	if err := operations.fsync(fileFD); err != nil {
		return fmt.Errorf("%w: regular-file fsync unsupported: %v", ErrCheckpointUnsupported, err)
	}
	closingFileFD := fileFD
	fileClosed = true
	fileFD = -1
	if err := operations.close(closingFileFD); err != nil {
		return fmt.Errorf("%w: close regular-file fsync probe: %v", ErrCheckpointUnsupported, err)
	}
	if err := operations.unlinkat(private.fd, fileName, 0); err != nil {
		return fmt.Errorf("%w: remove regular-file fsync probe: %v", ErrCheckpointUnsupported, err)
	}

	const nameA, nameB, nameC = ".checkpoint-probe-a", ".checkpoint-probe-b", ".checkpoint-probe-c"
	for _, name := range []string{nameA, nameB} {
		if err := operations.mkdirat(private.fd, name, 0o700); err != nil {
			return fmt.Errorf("%w: create directory rename probe %q: %v", ErrCheckpointUnsupported, name, err)
		}
	}
	a, err := openCheckpointArtifactChildDirectoryWithOperations(private, nameA, operations)
	if err != nil {
		return fmt.Errorf("%w: hold first directory rename probe: %v", ErrCheckpointUnsupported, err)
	}
	defer func() {
		if a.fd >= 0 {
			_ = operations.close(a.fd)
		}
	}()
	b, err := openCheckpointArtifactChildDirectoryWithOperations(private, nameB, operations)
	if err != nil {
		return fmt.Errorf("%w: hold second directory rename probe: %v", ErrCheckpointUnsupported, err)
	}
	defer func() {
		if b.fd >= 0 {
			_ = operations.close(b.fd)
		}
	}()

	if err := operations.rename(private.fd, nameA, private.fd, nameB, noReplaceFlag); !errors.Is(err, unix.EEXIST) {
		return fmt.Errorf("%w: occupied-target no-replace directory rename unsupported: %v", ErrCheckpointUnsupported, err)
	}
	if err := checkpointArtifactRequireTopology(private.fd, operations, map[string]*heldWorkingTreeDirectory{nameA: &a, nameB: &b, nameC: nil}); err != nil {
		return fmt.Errorf("%w: occupied-target no-replace probe: %v", ErrCheckpointUnsupported, err)
	}
	if err := operations.rename(private.fd, nameA, private.fd, nameC, noReplaceFlag); err != nil {
		return fmt.Errorf("%w: absent-target no-replace directory rename unsupported: %v", ErrCheckpointUnsupported, err)
	}
	if err := checkpointArtifactRequireTopology(private.fd, operations, map[string]*heldWorkingTreeDirectory{nameA: nil, nameB: &b, nameC: &a}); err != nil {
		return fmt.Errorf("%w: absent-target no-replace probe: %v", ErrCheckpointUnsupported, err)
	}
	if err := operations.rename(private.fd, nameC, private.fd, nameA, noReplaceFlag); err != nil {
		return fmt.Errorf("%w: reverse no-replace directory rename unsupported: %v", ErrCheckpointUnsupported, err)
	}
	if err := checkpointArtifactRequireTopology(private.fd, operations, map[string]*heldWorkingTreeDirectory{nameA: &a, nameB: &b, nameC: nil}); err != nil {
		return fmt.Errorf("%w: reverse no-replace probe: %v", ErrCheckpointUnsupported, err)
	}

	closingAFD := a.fd
	a.fd = -1
	if err := operations.close(closingAFD); err != nil {
		return fmt.Errorf("%w: close first directory rename probe: %v", ErrCheckpointUnsupported, err)
	}
	closingBFD := b.fd
	b.fd = -1
	if err := operations.close(closingBFD); err != nil {
		return fmt.Errorf("%w: close second directory rename probe: %v", ErrCheckpointUnsupported, err)
	}
	for _, name := range []string{nameA, nameB} {
		if err := operations.unlinkat(private.fd, name, unix.AT_REMOVEDIR); err != nil {
			return fmt.Errorf("%w: remove directory rename probe %q: %v", ErrCheckpointUnsupported, name, err)
		}
	}
	if err := checkpointArtifactRequireTopology(private.fd, operations, map[string]*heldWorkingTreeDirectory{nameA: nil, nameB: nil, nameC: nil}); err != nil {
		return fmt.Errorf("%w: cleaned directory rename probe topology: %v", ErrCheckpointUnsupported, err)
	}
	if err := operations.fsync(private.fd); err != nil {
		return fmt.Errorf("%w: durably clean directory rename probe: %v", ErrCheckpointUnsupported, err)
	}
	return nil
}

func checkpointArtifactRequireTopology(parentFD int, operations checkpointArtifactPlatformOperations, topology map[string]*heldWorkingTreeDirectory) error {
	for name, expected := range topology {
		if expected == nil {
			var linked unix.Stat_t
			err := operations.fstatat(parentFD, name, &linked, unix.AT_SYMLINK_NOFOLLOW)
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			if err == nil {
				return fmt.Errorf("path %q unexpectedly exists", name)
			}
			return err
		}
		if err := checkpointArtifactRequireHeldDirectoryPath(parentFD, name, expected, operations, true); err != nil {
			return err
		}
	}
	return nil
}

func openCheckpointArtifactChildDirectory(parent heldWorkingTreeDirectory, name string) (heldWorkingTreeDirectory, error) {
	return openCheckpointArtifactChildDirectoryWithOperations(parent, name, defaultCheckpointArtifactPlatformOperations())
}

func openCheckpointArtifactChildDirectoryWithOperations(parent heldWorkingTreeDirectory, name string, operations checkpointArtifactPlatformOperations) (heldWorkingTreeDirectory, error) {
	var stat unix.Stat_t
	if err := operations.fstatat(parent.fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return heldWorkingTreeDirectory{}, err
	}
	metadata := workingTreeStatMetadata(&stat)
	if metadata.mode&unix.S_IFMT != unix.S_IFDIR || metadata.uid != uint32(unix.Geteuid()) || metadata.mode&0o777 != 0o700 || metadata.mode&0o7000 != 0 {
		return heldWorkingTreeDirectory{}, fmt.Errorf("%w: unsafe checkpoint stage", ErrCheckpointUnsupported)
	}
	fd, err := operations.openat(parent.fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return heldWorkingTreeDirectory{}, err
	}
	var openedStat unix.Stat_t
	err = operations.fstat(fd, &openedStat)
	opened := workingTreeStatMetadata(&openedStat)
	if err != nil || !sameWorkingTreeDirectory(opened, metadata) {
		_ = operations.close(fd)
		if err == nil {
			err = fmt.Errorf("projectstate: checkpoint stage directory changed while opening")
		}
		return heldWorkingTreeDirectory{}, err
	}
	return heldWorkingTreeDirectory{fd: fd, parentFD: parent.fd, name: name, path: filepath.Join(parent.path, name), metadata: opened}, nil
}

func newCheckpointArtifactJournalID() (string, error) {
	var raw [16]byte
	if _, err := io.ReadFull(rand.Reader, raw[:]); err != nil {
		return "", err
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	encoded := hex.EncodeToString(raw[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

func checkpointArtifactFault(dependencies checkpointArtifactDependencies, stage checkpointArtifactFaultStage) error {
	if dependencies.fault == nil {
		return nil
	}
	return dependencies.fault(stage)
}

func renderCheckpointArtifactTree(ctx context.Context, stage heldWorkingTreeDirectory, tree state.Tree, dependencies checkpointArtifactDependencies) (checkpointArtifactDurableStageProof, error) {
	operations := dependencies.operations
	durable := checkpointArtifactDurableStageProof{files: make(map[string]workingTreeMetadata), directories: make(map[string]workingTreeMetadata)}
	directories := map[string]heldWorkingTreeDirectory{".": stage}
	defer func() {
		for key, directory := range directories {
			if key != "." && directory.fd >= 0 {
				_ = operations.close(directory.fd)
			}
		}
	}()
	for _, file := range tree {
		if err := ctx.Err(); err != nil {
			return checkpointArtifactDurableStageProof{}, err
		}
		parts := strings.Split(filepath.ToSlash(filepath.Dir(file.Path)), "/")
		current, key := stage, "."
		if parts[0] != "." {
			for _, part := range parts {
				key = filepath.ToSlash(filepath.Join(key, part))
				child, ok := directories[key]
				if !ok {
					if err := checkpointArtifactFault(dependencies, checkpointArtifactBeforeMkdir); err != nil {
						return checkpointArtifactDurableStageProof{}, err
					}
					if err := operations.mkdirat(current.fd, part, 0o700); err != nil {
						return checkpointArtifactDurableStageProof{}, err
					}
					if err := checkpointArtifactFault(dependencies, checkpointArtifactAfterMkdir); err != nil {
						return checkpointArtifactDurableStageProof{}, err
					}
					var err error
					child, err = openCheckpointArtifactChildDirectoryWithOperations(current, part, operations)
					if err != nil {
						return checkpointArtifactDurableStageProof{}, err
					}
					directories[key] = child
				}
				current = child
			}
		}
		name := filepath.Base(file.Path)
		fd, err := operations.openat(current.fd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if err != nil {
			return checkpointArtifactDurableStageProof{}, err
		}
		var fileStat unix.Stat_t
		if err = operations.fstat(fd, &fileStat); err == nil {
			metadata := workingTreeStatMetadata(&fileStat)
			if metadata.mode&unix.S_IFMT != unix.S_IFREG || metadata.uid != uint32(unix.Geteuid()) || metadata.links != 1 || metadata.mode&0o777 != 0o600 || metadata.mode&0o7000 != 0 {
				err = fmt.Errorf("%w: unsafe newly created checkpoint file %q", ErrCheckpointUnsupported, file.Path)
			}
		}
		if err == nil {
			err = writeCheckpointArtifactFile(fd, file.Data, dependencies)
		}
		if err == nil {
			if err = checkpointArtifactFault(dependencies, checkpointArtifactBeforeFileFsync); err == nil {
				err = operations.fsync(fd)
			}
			if err == nil {
				durable.files[file.Path], err = checkpointArtifactFstatMetadata(fd, operations)
			}
			if err == nil {
				err = checkpointArtifactFault(dependencies, checkpointArtifactAfterFileFsync)
			}
		}
		closeErr := operations.close(fd)
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return checkpointArtifactDurableStageProof{}, err
		}
	}
	keys := make([]string, 0, len(directories))
	for key := range directories {
		if key != "." {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		leftDepth, rightDepth := strings.Count(keys[i], "/"), strings.Count(keys[j], "/")
		if leftDepth != rightDepth {
			return leftDepth > rightDepth
		}
		return keys[i] < keys[j]
	})
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return checkpointArtifactDurableStageProof{}, err
		}
		if err := checkpointArtifactFault(dependencies, checkpointArtifactBeforeDirectoryFsync); err != nil {
			return checkpointArtifactDurableStageProof{}, err
		}
		if err := operations.fsync(directories[key].fd); err != nil {
			return checkpointArtifactDurableStageProof{}, err
		}
		metadata, err := checkpointArtifactFstatMetadata(directories[key].fd, operations)
		if err != nil {
			return checkpointArtifactDurableStageProof{}, err
		}
		durable.directories[key] = metadata
		if err := checkpointArtifactFault(dependencies, checkpointArtifactAfterDirectoryFsync); err != nil {
			return checkpointArtifactDurableStageProof{}, err
		}
		directory := directories[key]
		_ = operations.close(directory.fd)
		directory.fd = -1
		directories[key] = directory
	}
	return durable, nil
}

func writeCheckpointArtifactFile(fd int, data []byte, dependencies checkpointArtifactDependencies) error {
	for len(data) > 0 {
		if err := checkpointArtifactFault(dependencies, checkpointArtifactBeforeWrite); err != nil {
			return err
		}
		written, err := dependencies.operations.write(fd, data)
		if err != nil {
			return err
		}
		if err := checkpointArtifactFault(dependencies, checkpointArtifactAfterWrite); err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func readCheckpointArtifactTree(ctx context.Context, root heldWorkingTreeDirectory) (state.Tree, error) {
	walker := newWorkingTreeWalker(defaultWorkingTreeLimits(), func(workingTreeReadStage, string) error { return ctx.Err() })
	if err := walker.walkDirectory(root, "."); err != nil {
		return nil, err
	}
	sort.Slice(walker.files, func(i, j int) bool { return walker.files[i].Path < walker.files[j].Path })
	if err := verifyCheckpointArtifactCapture(ctx, root, defaultWorkingTreeLimits(), walker); err != nil {
		return nil, err
	}
	if err := revalidateWorkingTreeDirectory(root); err != nil {
		return nil, err
	}
	return walker.files, nil
}

func verifyCheckpointArtifactStageFiles(ctx context.Context, root heldWorkingTreeDirectory, tree state.Tree, durable checkpointArtifactDurableStageProof, strictRoot bool, _ checkpointArtifactPlatformOperations) error {
	expectedDirectories := map[string]struct{}{".": {}}
	for _, file := range tree {
		for directory := filepath.ToSlash(filepath.Dir(file.Path)); directory != "."; directory = filepath.ToSlash(filepath.Dir(directory)) {
			expectedDirectories[directory] = struct{}{}
		}
	}
	walker := newWorkingTreeWalker(defaultWorkingTreeLimits(), func(workingTreeReadStage, string) error { return ctx.Err() })
	if err := walker.walkDirectory(root, "."); err != nil {
		return err
	}
	sort.Slice(walker.files, func(i, j int) bool { return walker.files[i].Path < walker.files[j].Path })
	if !sameCheckpointArtifactTree(walker.files, tree) {
		return fmt.Errorf("%w: checkpoint stage bytes changed during shape proof", ErrCheckpointUnsupported)
	}
	wantDigest, err := state.DigestTree(tree)
	if err != nil {
		return err
	}
	gotDigest, err := state.DigestTree(walker.files)
	if err != nil || gotDigest != wantDigest {
		return fmt.Errorf("%w: checkpoint stage digest changed during shape proof", ErrCheckpointUnsupported)
	}
	if len(walker.directoryMetadata) != len(expectedDirectories) {
		return fmt.Errorf("%w: checkpoint stage directory inventory differs", ErrCheckpointUnsupported)
	}
	for directory, metadata := range walker.directoryMetadata {
		if _, ok := expectedDirectories[directory]; !ok {
			return fmt.Errorf("%w: unexpected checkpoint stage directory %q", ErrCheckpointUnsupported, directory)
		}
		if metadata.mode&unix.S_IFMT != unix.S_IFDIR || metadata.uid != uint32(unix.Geteuid()) || metadata.mode&0o777 != 0o700 || metadata.mode&0o7000 != 0 {
			return fmt.Errorf("%w: unsafe checkpoint stage directory %q", ErrCheckpointUnsupported, directory)
		}
		expected, ok := durable.directories[directory]
		if !ok {
			return fmt.Errorf("%w: checkpoint stage directory lacks durable proof %q", ErrCheckpointUnsupported, directory)
		}
		if directory == "." && !strictRoot {
			if !sameWorkingTreeDirectoryIdentity(metadata, expected) {
				return fmt.Errorf("%w: checkpoint stage root identity differs from durable proof", ErrCheckpointUnsupported)
			}
		} else if metadata != expected {
			return fmt.Errorf("%w: checkpoint stage directory metadata differs from durable proof %q", ErrCheckpointUnsupported, directory)
		}
	}
	if len(walker.fileMetadata) != len(tree) {
		return fmt.Errorf("%w: checkpoint stage file inventory differs", ErrCheckpointUnsupported)
	}
	for path, metadata := range walker.fileMetadata {
		if metadata.mode&unix.S_IFMT != unix.S_IFREG || metadata.uid != uint32(unix.Geteuid()) || metadata.links != 1 || metadata.mode&0o777 != 0o600 || metadata.mode&0o7000 != 0 {
			return fmt.Errorf("%w: unsafe checkpoint stage file %q", ErrCheckpointUnsupported, path)
		}
		if expected, ok := durable.files[path]; !ok || metadata != expected {
			return fmt.Errorf("%w: checkpoint stage file metadata differs from durable proof %q", ErrCheckpointUnsupported, path)
		}
	}
	if err := verifyCheckpointArtifactCapture(ctx, root, defaultWorkingTreeLimits(), walker); err != nil {
		return err
	}
	if err := revalidateWorkingTreeDirectory(root); err != nil {
		return err
	}
	return ctx.Err()
}

func verifyCheckpointArtifactCapture(ctx context.Context, root heldWorkingTreeDirectory, limits workingTreeLimits, expected workingTreeWalker) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	verification := newWorkingTreeWalker(limits, func(workingTreeReadStage, string) error { return ctx.Err() })
	if err := verification.walkDirectory(root, "."); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("%w: final checkpoint capture verification: %w", ErrWorkingTreeChanged, err)
	}
	sort.Slice(verification.files, func(i, j int) bool { return verification.files[i].Path < verification.files[j].Path })
	if !sameWorkingTreeCapture(expected.files, verification.files) ||
		!sameWorkingTreeFileMetadata(expected.fileMetadata, verification.fileMetadata) ||
		!sameWorkingTreeDirectoryMetadata(expected.directoryMetadata, verification.directoryMetadata) {
		return fmt.Errorf("%w: final checkpoint capture bytes or metadata differ", ErrWorkingTreeChanged)
	}
	return ctx.Err()
}

func (artifact *checkpointArtifact) evidence() checkpointArtifactEvidence {
	if artifact == nil {
		return checkpointArtifactEvidence{}
	}
	artifact.mu.Lock()
	defer artifact.mu.Unlock()
	return artifact.evidenceValue
}

func publishPreparedCheckpointArtifact(ctx context.Context, artifact *checkpointArtifact) (checkpointPublicationDisposition, error) {
	if artifact == nil {
		return 0, ErrCheckpointUnsupported
	}
	artifact.mu.Lock()
	defer artifact.mu.Unlock()
	if artifact.closed || artifact.claimed {
		return 0, ErrCheckpointUnsupported
	}
	artifact.claimed = true
	if err := preflightCheckpointArtifactPublication(ctx, artifact); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := publishCheckpointArtifactFallback(ctx, artifact); err != nil {
		return 0, err
	}
	return checkpointPublicationPublished, nil
}

func preflightCheckpointArtifactPublication(ctx context.Context, artifact *checkpointArtifact) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	paths, err := resolveCheckpointGitPathsWithReader(ctx, artifact.checkoutIdentity.CanonicalPath, artifact.dependencies.readGit)
	if err != nil {
		return err
	}
	if paths != artifact.paths {
		return fmt.Errorf("%w: checkpoint Git paths changed", ErrCheckpointCAS)
	}
	if artifact.checkout == nil || artifact.git == nil || artifact.live.fd < 0 || artifact.private.fd < 0 || artifact.stage.fd < 0 {
		return ErrCheckpointUnsupported
	}
	if err := artifact.checkout.revalidate(); err != nil {
		return fmt.Errorf("%w: checkout identity changed: %v", ErrCheckpointCAS, err)
	}
	terminal := artifact.checkout.ancestry[len(artifact.checkout.ancestry)-1]
	if terminal.metadata.device != artifact.checkoutIdentity.Device || terminal.metadata.inode != artifact.checkoutIdentity.Inode {
		return fmt.Errorf("%w: checkout identity changed", ErrCheckpointCAS)
	}
	if err := artifact.git.revalidate(); err != nil {
		return fmt.Errorf("%w: Git directory identity changed: %v", ErrCheckpointCAS, err)
	}
	for _, ancestor := range artifact.privateAncestors {
		if err := revalidateWorkingTreeDirectory(ancestor); err != nil {
			return fmt.Errorf("%w: private-root ancestry changed: %v", ErrCheckpointCAS, err)
		}
	}
	if err := revalidateWorkingTreeDirectory(artifact.private); err != nil {
		return fmt.Errorf("%w: private root changed: %v", ErrCheckpointCAS, err)
	}
	if err := checkpointArtifactRequireDurableDirectory(artifact.private, artifact.durableProof.private, artifact.dependencies.operations, true); err != nil {
		return fmt.Errorf("%w: private root changed after fsync: %v", ErrCheckpointCAS, err)
	}
	if err := revalidateWorkingTreeDirectory(artifact.stage); err != nil {
		return fmt.Errorf("%w: stage changed: %v", ErrCheckpointCAS, err)
	}
	if err := revalidateWorkingTreeDirectory(artifact.live); err != nil {
		return fmt.Errorf("%w: live tree changed: %v", ErrCheckpointCAS, err)
	}
	stageTree, err := readCheckpointArtifactTree(ctx, artifact.stage)
	if err != nil || !sameCheckpointArtifactTree(stageTree, artifact.proof.candidate.tree) {
		return fmt.Errorf("%w: staged candidate changed", ErrCheckpointCAS)
	}
	stageDigest, err := state.DigestTree(stageTree)
	if err != nil || stageDigest != artifact.proof.candidate.digest {
		return fmt.Errorf("%w: staged candidate digest changed", ErrCheckpointCAS)
	}
	if err := verifyCheckpointArtifactStageFiles(ctx, artifact.stage, artifact.proof.candidate.tree, artifact.durableProof, true, artifact.dependencies.operations); err != nil {
		return fmt.Errorf("%w: staged candidate shape changed: %v", ErrCheckpointCAS, err)
	}
	liveTree, err := readCheckpointArtifactTree(ctx, artifact.live)
	if err != nil || !sameCheckpointArtifactTree(liveTree, artifact.proof.prior.tree) {
		return ErrCheckpointCAS
	}
	liveDigest, err := state.DigestTree(liveTree)
	if err != nil || liveDigest != artifact.proof.prior.digest {
		return ErrCheckpointCAS
	}
	if err := checkpointArtifactNameAbsent(artifact.private.fd, filepath.Base(artifact.evidenceValue.BackupPath), artifact.dependencies.operations); err != nil {
		return fmt.Errorf("%w: checkpoint backup is no longer absent", ErrCheckpointCAS)
	}
	privateMetadata, err := checkpointArtifactFstatMetadata(artifact.private.fd, artifact.dependencies.operations)
	if err != nil {
		return err
	}
	liveMetadata, err := checkpointArtifactFstatMetadata(artifact.live.fd, artifact.dependencies.operations)
	if err != nil {
		return err
	}
	if terminal.metadata.device != liveMetadata.device || liveMetadata.device != privateMetadata.device {
		return fmt.Errorf("%w: checkpoint device identity changed", ErrCheckpointUnsupported)
	}
	if err := revalidateCheckpointPlatformMounts(terminal.fd, artifact.live.fd, artifact.private.fd, filepath.Join(artifact.checkoutIdentity.CanonicalPath, ".wormhole"), artifact.mountProof, artifact.dependencies); err != nil {
		return err
	}
	if err := artifact.dependencies.operations.fsync(terminal.fd); err != nil {
		return fmt.Errorf("%w: live parent fsync capability changed: %v", ErrCheckpointUnsupported, err)
	}
	if err := artifact.dependencies.operations.fsync(artifact.private.fd); err != nil {
		return fmt.Errorf("%w: private parent fsync capability changed: %v", ErrCheckpointUnsupported, err)
	}
	return ctx.Err()
}

func publishCheckpointArtifactFallback(ctx context.Context, artifact *checkpointArtifact) error {
	terminal := artifact.checkout.ancestry[len(artifact.checkout.ancestry)-1]
	stageName := filepath.Base(artifact.evidenceValue.StagePath)
	backupName := filepath.Base(artifact.evidenceValue.BackupPath)
	if err := checkpointArtifactFault(artifact.dependencies, checkpointArtifactBeforeLiveMutation); err != nil {
		return err
	}
	if err := checkpointArtifactRevalidatePreparedMutationBoundary(ctx, artifact, stageName, backupName); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	renameErr := artifact.dependencies.operations.rename(terminal.fd, ".wormhole", artifact.private.fd, backupName, checkpointNoReplaceRenameFlag())
	afterErr := checkpointArtifactFault(artifact.dependencies, checkpointArtifactAfterLiveMutation)
	if renameErr != nil || afterErr != nil {
		_ = checkpointArtifactRevalidateAnyFallbackTopology(artifact, stageName, backupName)
		if renameErr != nil {
			return fmt.Errorf("projectstate: retain checkpoint live backup: %w", renameErr)
		}
		return afterErr
	}
	if err := checkpointArtifactRequirePublishedTopology(artifact, stageName, backupName, checkpointTopologyBackedUp); err != nil {
		return err
	}
	reachedParents, err := checkpointArtifactFsyncPublicationParents(artifact, true)
	if err != nil {
		return err
	}
	if err := checkpointArtifactFault(artifact.dependencies, checkpointArtifactBeforeSecondLiveMutation); err != nil {
		return err
	}
	if err := checkpointArtifactRevalidateReachedMutationBoundary(artifact, stageName, backupName, checkpointTopologyBackedUp, &reachedParents); err != nil {
		return err
	}
	renameErr = artifact.dependencies.operations.rename(artifact.private.fd, stageName, terminal.fd, ".wormhole", checkpointNoReplaceRenameFlag())
	afterErr = checkpointArtifactFault(artifact.dependencies, checkpointArtifactAfterSecondLiveMutation)
	if renameErr != nil || afterErr != nil {
		_ = checkpointArtifactRevalidateAnyFallbackTopology(artifact, stageName, backupName)
		if renameErr != nil {
			return fmt.Errorf("projectstate: publish staged checkpoint tree: %w", renameErr)
		}
		return afterErr
	}
	if err := checkpointArtifactRequirePublishedTopology(artifact, stageName, backupName, checkpointTopologyPublished); err != nil {
		return err
	}
	_, err = checkpointArtifactFsyncPublicationParents(artifact, false)
	return err
}

type checkpointArtifactTopology uint8

const (
	checkpointTopologyBackedUp checkpointArtifactTopology = iota + 1
	checkpointTopologyPublished
	checkpointTopologyPrepared
)

func checkpointArtifactRequirePublishedTopology(artifact *checkpointArtifact, stageName, backupName string, topology checkpointArtifactTopology) error {
	terminal := artifact.checkout.ancestry[len(artifact.checkout.ancestry)-1]
	operations := artifact.dependencies.operations
	var live, stage, backup *heldWorkingTreeDirectory
	switch topology {
	case checkpointTopologyBackedUp:
		stage, backup = &artifact.stage, &artifact.live
	case checkpointTopologyPublished:
		live, backup = &artifact.stage, &artifact.live
	default:
		return ErrCheckpointUnsupported
	}
	if err := checkpointArtifactRequireHeldDirectoryPath(terminal.fd, ".wormhole", live, operations, false); err != nil {
		return fmt.Errorf("projectstate: revalidate checkpoint live topology: %w", err)
	}
	if err := checkpointArtifactRequireHeldDirectoryPath(artifact.private.fd, stageName, stage, operations, stage == &artifact.stage); err != nil {
		return fmt.Errorf("projectstate: revalidate checkpoint stage topology: %w", err)
	}
	if err := checkpointArtifactRequireHeldDirectoryPath(artifact.private.fd, backupName, backup, operations, false); err != nil {
		return fmt.Errorf("projectstate: revalidate checkpoint backup topology: %w", err)
	}
	switch topology {
	case checkpointTopologyBackedUp:
		if err := checkpointArtifactRequireTreeAt(context.Background(), artifact.private.fd, backupName, artifact.live, artifact.proof.prior, operations); err != nil {
			return fmt.Errorf("projectstate: fallback prior backup changed: %w", err)
		}
		if err := checkpointArtifactRequireCandidateAt(context.Background(), artifact.private.fd, stageName, artifact.stage, artifact, true, operations); err != nil {
			return fmt.Errorf("projectstate: fallback candidate stage changed: %w", err)
		}
	case checkpointTopologyPublished:
		if err := checkpointArtifactRequireCandidateAt(context.Background(), terminal.fd, ".wormhole", artifact.stage, artifact, false, operations); err != nil {
			return fmt.Errorf("projectstate: published candidate changed: %w", err)
		}
		if err := checkpointArtifactRequireTreeAt(context.Background(), artifact.private.fd, backupName, artifact.live, artifact.proof.prior, operations); err != nil {
			return fmt.Errorf("projectstate: published prior backup changed: %w", err)
		}
	}
	return nil
}

func checkpointArtifactRequireTreeAt(ctx context.Context, parentFD int, name string, held heldWorkingTreeDirectory, expected checkpointArtifactTreeProof, operations checkpointArtifactPlatformOperations) error {
	got, err := readCheckpointArtifactTreeAt(ctx, parentFD, name, held, operations)
	if err != nil || !sameCheckpointArtifactTree(got, expected.tree) {
		if err != nil {
			return err
		}
		return fmt.Errorf("tree bytes differ")
	}
	digest, err := state.DigestTree(got)
	if err != nil || digest != expected.digest {
		if err != nil {
			return err
		}
		return fmt.Errorf("tree digest differs")
	}
	return nil
}

func checkpointArtifactRequireCandidateAt(ctx context.Context, parentFD int, name string, held heldWorkingTreeDirectory, artifact *checkpointArtifact, strictRoot bool, operations checkpointArtifactPlatformOperations) error {
	if err := checkpointArtifactRequireHeldDirectoryPath(parentFD, name, &held, operations, true); err != nil {
		return err
	}
	metadata, err := checkpointArtifactFstatMetadata(held.fd, operations)
	if err != nil {
		return err
	}
	held.parentFD, held.name, held.path, held.metadata = parentFD, name, name, metadata
	return verifyCheckpointArtifactStageFiles(ctx, held, artifact.proof.candidate.tree, artifact.durableProof, strictRoot, operations)
}

func checkpointArtifactRequireFullDirectoryPath(parentFD int, name string, original, durable workingTreeMetadata, operations checkpointArtifactPlatformOperations, ownerOnly bool) error {
	var linked unix.Stat_t
	if err := operations.fstatat(parentFD, name, &linked, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	linkedMetadata := workingTreeStatMetadata(&linked)
	if !sameWorkingTreeDirectoryIdentity(linkedMetadata, original) || linkedMetadata != durable {
		return fmt.Errorf("%w: durable checkpoint directory path metadata differs", ErrCheckpointUnsupported)
	}
	if ownerOnly && (durable.mode&unix.S_IFMT != unix.S_IFDIR || durable.uid != uint32(unix.Geteuid()) || durable.mode&0o777 != 0o700 || durable.mode&0o7000 != 0) {
		return fmt.Errorf("%w: durable checkpoint directory is not owner-only", ErrCheckpointUnsupported)
	}
	return nil
}

func checkpointArtifactRequireDurableDirectory(directory heldWorkingTreeDirectory, durable workingTreeMetadata, operations checkpointArtifactPlatformOperations, ownerOnly bool) error {
	if directory.parentFD >= 0 {
		if err := checkpointArtifactRequireFullDirectoryPath(directory.parentFD, directory.name, directory.metadata, durable, operations, ownerOnly); err != nil {
			return err
		}
	}
	opened, err := checkpointArtifactFstatMetadata(directory.fd, operations)
	if err != nil {
		return err
	}
	if opened != durable {
		return fmt.Errorf("%w: durable checkpoint directory metadata differs", ErrCheckpointUnsupported)
	}
	if directory.parentFD >= 0 {
		if err := checkpointArtifactRequireFullDirectoryPath(directory.parentFD, directory.name, directory.metadata, durable, operations, ownerOnly); err != nil {
			return err
		}
	}
	return nil
}

func checkpointArtifactRevalidateReachedMutationBoundary(artifact *checkpointArtifact, stageName, backupName string, topology checkpointArtifactTopology, reachedParents *checkpointArtifactPublicationParentsProof) error {
	paths, err := resolveCheckpointGitPathsWithReader(context.Background(), artifact.checkoutIdentity.CanonicalPath, artifact.dependencies.readGit)
	if err != nil {
		return err
	}
	if paths != artifact.paths {
		return fmt.Errorf("%w: checkpoint Git paths changed before second rename", ErrCheckpointCAS)
	}
	if err := artifact.checkout.revalidate(); err != nil {
		return fmt.Errorf("%w: checkout changed before checkpoint rename", ErrCheckpointCAS)
	}
	terminal := artifact.checkout.ancestry[len(artifact.checkout.ancestry)-1]
	if reachedParents != nil {
		if err := checkpointArtifactRequireDurableDirectory(terminal, reachedParents.liveParent, artifact.dependencies.operations, false); err != nil {
			return fmt.Errorf("%w: live parent changed after publication fsync", ErrCheckpointCAS)
		}
	}
	if err := artifact.git.revalidate(); err != nil {
		return fmt.Errorf("%w: Git directory changed before checkpoint rename", ErrCheckpointCAS)
	}
	for _, ancestor := range artifact.privateAncestors {
		if err := checkpointArtifactRequireDurableDirectory(ancestor, ancestor.metadata, artifact.dependencies.operations, true); err != nil {
			return fmt.Errorf("%w: private ancestry changed before checkpoint rename", ErrCheckpointCAS)
		}
	}
	if reachedParents == nil {
		if _, err := checkpointArtifactValidateOwnerDirectory(artifact.private, artifact.dependencies.operations); err != nil {
			return fmt.Errorf("%w: private root changed before checkpoint rename", ErrCheckpointCAS)
		}
	} else if err := checkpointArtifactRequireDurableDirectory(artifact.private, reachedParents.private, artifact.dependencies.operations, true); err != nil {
		return fmt.Errorf("%w: private root changed after publication fsync", ErrCheckpointCAS)
	}
	if err := checkpointArtifactRequirePublishedTopology(artifact, stageName, backupName, topology); err != nil {
		return err
	}
	if err := revalidateCheckpointPlatformMounts(terminal.fd, artifact.live.fd, artifact.private.fd, filepath.Join(artifact.checkoutIdentity.CanonicalPath, ".wormhole"), artifact.mountProof, artifact.dependencies); err != nil {
		return err
	}
	return checkpointArtifactRevalidateCurrentMounts(artifact, topology, reachedParents)
}

func checkpointArtifactRevalidatePreparedMutationBoundary(ctx context.Context, artifact *checkpointArtifact, stageName, backupName string) error {
	paths, err := resolveCheckpointGitPathsWithReader(ctx, artifact.checkoutIdentity.CanonicalPath, artifact.dependencies.readGit)
	if err != nil {
		return err
	}
	if paths != artifact.paths {
		return fmt.Errorf("%w: checkpoint Git paths changed before publication", ErrCheckpointCAS)
	}
	if err := artifact.checkout.revalidate(); err != nil {
		return fmt.Errorf("%w: checkout changed before checkpoint publication", ErrCheckpointCAS)
	}
	if err := artifact.git.revalidate(); err != nil {
		return fmt.Errorf("%w: Git directory changed before checkpoint publication", ErrCheckpointCAS)
	}
	for _, ancestor := range artifact.privateAncestors {
		if err := checkpointArtifactRequireDurableDirectory(ancestor, ancestor.metadata, artifact.dependencies.operations, true); err != nil {
			return fmt.Errorf("%w: private ancestry changed before checkpoint publication", ErrCheckpointCAS)
		}
	}
	if _, err := checkpointArtifactValidateOwnerDirectory(artifact.private, artifact.dependencies.operations); err != nil {
		return fmt.Errorf("%w: private root changed before checkpoint publication", ErrCheckpointCAS)
	}
	if err := checkpointArtifactRequireDurableDirectory(artifact.private, artifact.durableProof.private, artifact.dependencies.operations, true); err != nil {
		return fmt.Errorf("%w: private root changed after fsync: %v", ErrCheckpointCAS, err)
	}
	terminal := artifact.checkout.ancestry[len(artifact.checkout.ancestry)-1]
	operations := artifact.dependencies.operations
	if err := checkpointArtifactRequireHeldDirectoryPath(terminal.fd, ".wormhole", &artifact.live, operations, false); err != nil {
		return fmt.Errorf("%w: live path changed before checkpoint publication", ErrCheckpointCAS)
	}
	if err := checkpointArtifactRequireHeldDirectoryPath(artifact.private.fd, stageName, &artifact.stage, operations, true); err != nil {
		return fmt.Errorf("%w: stage path changed before checkpoint publication", ErrCheckpointCAS)
	}
	if err := checkpointArtifactRequireHeldDirectoryPath(artifact.private.fd, backupName, nil, operations, false); err != nil {
		return fmt.Errorf("%w: backup path changed before checkpoint publication", ErrCheckpointCAS)
	}
	if err := checkpointArtifactRequireCandidateAt(ctx, artifact.private.fd, stageName, artifact.stage, artifact, true, operations); err != nil {
		return fmt.Errorf("%w: candidate stage changed before checkpoint publication", ErrCheckpointCAS)
	}
	if err := checkpointArtifactRequireTreeAt(ctx, terminal.fd, ".wormhole", artifact.live, artifact.proof.prior, operations); err != nil {
		return fmt.Errorf("%w: prior live tree changed before checkpoint publication", ErrCheckpointCAS)
	}
	if err := revalidateCheckpointPlatformMounts(terminal.fd, artifact.live.fd, artifact.private.fd, filepath.Join(artifact.checkoutIdentity.CanonicalPath, ".wormhole"), artifact.mountProof, artifact.dependencies); err != nil {
		return err
	}
	if err := checkpointArtifactRevalidateCurrentMounts(artifact, checkpointTopologyPrepared, nil); err != nil {
		return err
	}
	return ctx.Err()
}

func checkpointArtifactRevalidateCurrentMounts(artifact *checkpointArtifact, topology checkpointArtifactTopology, reachedParents *checkpointArtifactPublicationParentsProof) error {
	checkout, err := openWorkingTreeRoot(artifact.checkoutIdentity.CanonicalPath)
	if err != nil {
		return fmt.Errorf("%w: freshly open current checkpoint checkout: %v", ErrCheckpointCAS, err)
	}
	defer checkout.close()
	sourcePath := filepath.Join(artifact.checkoutIdentity.CanonicalPath, ".wormhole")
	expectedSource := artifact.live.metadata
	switch topology {
	case checkpointTopologyPrepared:
	case checkpointTopologyBackedUp:
		sourcePath = artifact.evidenceValue.StagePath
		expectedSource = artifact.stage.metadata
	default:
		return ErrCheckpointUnsupported
	}
	source, err := openWorkingTreeRoot(sourcePath)
	if err != nil {
		return fmt.Errorf("%w: freshly open current checkpoint publication source: %v", ErrCheckpointCAS, err)
	}
	defer source.close()
	private, err := openWorkingTreeRoot(artifact.paths.checkpointRoot)
	if err != nil {
		return fmt.Errorf("%w: freshly open current checkpoint private root: %v", ErrCheckpointCAS, err)
	}
	defer private.close()

	heldCheckout := artifact.checkout.ancestry[len(artifact.checkout.ancestry)-1]
	freshCheckout := checkout.ancestry[len(checkout.ancestry)-1]
	freshSource := source.ancestry[len(source.ancestry)-1]
	freshPrivate := private.ancestry[len(private.ancestry)-1]
	if !sameWorkingTreeDirectoryIdentity(freshCheckout.metadata, heldCheckout.metadata) || !sameWorkingTreeDirectoryIdentity(freshSource.metadata, expectedSource) || !sameWorkingTreeDirectoryIdentity(freshPrivate.metadata, artifact.private.metadata) {
		return fmt.Errorf("%w: freshly opened checkpoint paths differ from held objects", ErrCheckpointCAS)
	}
	ownerPrivate := freshPrivate.metadata.mode&unix.S_IFMT == unix.S_IFDIR && freshPrivate.metadata.uid == uint32(unix.Geteuid()) && freshPrivate.metadata.mode&0o777 == 0o700 && freshPrivate.metadata.mode&0o7000 == 0
	if !ownerPrivate {
		return fmt.Errorf("%w: freshly opened checkpoint private root is not owner-only", ErrCheckpointCAS)
	}
	switch topology {
	case checkpointTopologyPrepared:
		if freshCheckout.metadata != heldCheckout.metadata || freshSource.metadata != artifact.live.metadata || freshPrivate.metadata != artifact.durableProof.private {
			return fmt.Errorf("%w: freshly opened prepared checkpoint metadata differs", ErrCheckpointCAS)
		}
	case checkpointTopologyBackedUp:
		durableStage, ok := artifact.durableProof.directories["."]
		if !ok || freshSource.metadata != durableStage || reachedParents == nil || freshCheckout.metadata != reachedParents.liveParent || freshPrivate.metadata != reachedParents.private {
			return fmt.Errorf("%w: freshly opened fallback checkpoint metadata differs", ErrCheckpointCAS)
		}
	}
	if err := revalidateCheckpointPlatformMounts(freshCheckout.fd, freshSource.fd, freshPrivate.fd, sourcePath, artifact.mountProof, artifact.dependencies); err != nil {
		return err
	}
	if err := checkpointArtifactRequireDurableDirectory(freshCheckout, freshCheckout.metadata, artifact.dependencies.operations, false); err != nil {
		return fmt.Errorf("%w: freshly opened checkpoint checkout changed during mount proof", ErrCheckpointCAS)
	}
	if err := checkpointArtifactRequireDurableDirectory(freshSource, freshSource.metadata, artifact.dependencies.operations, false); err != nil {
		return fmt.Errorf("%w: freshly opened checkpoint publication source changed during mount proof", ErrCheckpointCAS)
	}
	if err := checkpointArtifactRequireDurableDirectory(freshPrivate, freshPrivate.metadata, artifact.dependencies.operations, true); err != nil {
		return fmt.Errorf("%w: freshly opened checkpoint private root changed during mount proof", ErrCheckpointCAS)
	}
	if err := checkout.revalidate(); err != nil {
		return fmt.Errorf("%w: freshly opened checkpoint checkout changed: %v", ErrCheckpointCAS, err)
	}
	if err := source.revalidate(); err != nil {
		return fmt.Errorf("%w: freshly opened checkpoint publication source changed: %v", ErrCheckpointCAS, err)
	}
	if err := private.revalidate(); err != nil {
		return fmt.Errorf("%w: freshly opened checkpoint private root changed: %v", ErrCheckpointCAS, err)
	}
	return nil
}

func checkpointArtifactRequireHeldDirectoryPath(parentFD int, name string, expected *heldWorkingTreeDirectory, operations checkpointArtifactPlatformOperations, ownerOnly bool) error {
	var linked unix.Stat_t
	err := operations.fstatat(parentFD, name, &linked, unix.AT_SYMLINK_NOFOLLOW)
	if expected == nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		if err == nil {
			return fmt.Errorf("path %q unexpectedly exists", name)
		}
		return err
	}
	if err != nil {
		return err
	}
	linkedMetadata := workingTreeStatMetadata(&linked)
	openedMetadata, err := checkpointArtifactFstatMetadata(expected.fd, operations)
	if err != nil {
		return err
	}
	if !sameWorkingTreeDirectoryIdentity(linkedMetadata, expected.metadata) || !sameWorkingTreeDirectoryIdentity(openedMetadata, expected.metadata) {
		return fmt.Errorf("path %q does not name held directory", name)
	}
	if ownerOnly {
		ownerShape := func(metadata workingTreeMetadata) bool {
			return metadata.mode&unix.S_IFMT == unix.S_IFDIR && metadata.uid == uint32(unix.Geteuid()) && metadata.mode&0o777 == 0o700 && metadata.mode&0o7000 == 0
		}
		if !ownerShape(linkedMetadata) || !ownerShape(openedMetadata) {
			return fmt.Errorf("path %q is not owner-only", name)
		}
		var linkedAgain unix.Stat_t
		if err := operations.fstatat(parentFD, name, &linkedAgain, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		againMetadata := workingTreeStatMetadata(&linkedAgain)
		if !sameWorkingTreeDirectoryIdentity(againMetadata, expected.metadata) || !ownerShape(againMetadata) {
			return fmt.Errorf("path %q owner-only shape changed during proof", name)
		}
	}
	return nil
}

func checkpointArtifactValidateOwnerDirectory(directory heldWorkingTreeDirectory, operations checkpointArtifactPlatformOperations) (workingTreeMetadata, error) {
	if directory.parentFD >= 0 {
		if err := checkpointArtifactRequireHeldDirectoryPath(directory.parentFD, directory.name, &directory, operations, true); err != nil {
			return workingTreeMetadata{}, err
		}
	}
	metadata, err := checkpointArtifactFstatMetadata(directory.fd, operations)
	if err != nil {
		return workingTreeMetadata{}, err
	}
	if metadata.mode&unix.S_IFMT != unix.S_IFDIR || metadata.uid != uint32(unix.Geteuid()) || metadata.mode&0o777 != 0o700 || metadata.mode&0o7000 != 0 ||
		!sameWorkingTreeDirectoryIdentity(metadata, directory.metadata) {
		return workingTreeMetadata{}, fmt.Errorf("%w: Git-private checkpoint directory is not owner-only", ErrCheckpointUnsupported)
	}
	return metadata, nil
}

func readCheckpointArtifactTreeAt(ctx context.Context, parentFD int, name string, held heldWorkingTreeDirectory, operations checkpointArtifactPlatformOperations) (state.Tree, error) {
	if err := checkpointArtifactRequireHeldDirectoryPath(parentFD, name, &held, operations, false); err != nil {
		return nil, err
	}
	metadata, err := checkpointArtifactFstatMetadata(held.fd, operations)
	if err != nil {
		return nil, err
	}
	held.parentFD, held.name, held.path, held.metadata = parentFD, name, name, metadata
	return readCheckpointArtifactTree(ctx, held)
}

func checkpointArtifactFstatMetadata(fd int, operations checkpointArtifactPlatformOperations) (workingTreeMetadata, error) {
	var stat unix.Stat_t
	if err := operations.fstat(fd, &stat); err != nil {
		return workingTreeMetadata{}, err
	}
	return workingTreeStatMetadata(&stat), nil
}

func checkpointArtifactFsyncPublicationParents(artifact *checkpointArtifact, destinationPrivate bool) (checkpointArtifactPublicationParentsProof, error) {
	terminal := artifact.checkout.ancestry[len(artifact.checkout.ancestry)-1]
	fsyncLiveParent := func() (workingTreeMetadata, error) {
		if err := checkpointArtifactFault(artifact.dependencies, checkpointArtifactBeforeLiveParentFsync); err != nil {
			return workingTreeMetadata{}, err
		}
		if err := artifact.dependencies.operations.fsync(terminal.fd); err != nil {
			return workingTreeMetadata{}, fmt.Errorf("projectstate: fsync checkpoint live parent: %w", err)
		}
		durableLiveParent, err := checkpointArtifactFstatMetadata(terminal.fd, artifact.dependencies.operations)
		if err != nil {
			return workingTreeMetadata{}, err
		}
		if err := checkpointArtifactRequireDurableDirectory(terminal, durableLiveParent, artifact.dependencies.operations, false); err != nil {
			return workingTreeMetadata{}, fmt.Errorf("projectstate: prove durable checkpoint live parent: %w", err)
		}
		if err := checkpointArtifactFault(artifact.dependencies, checkpointArtifactAfterLiveParentFsync); err != nil {
			return workingTreeMetadata{}, err
		}
		return durableLiveParent, nil
	}
	fsyncPrivateParent := func() (workingTreeMetadata, error) {
		if err := checkpointArtifactFault(artifact.dependencies, checkpointArtifactBeforePrivateParentFsync); err != nil {
			return workingTreeMetadata{}, err
		}
		if err := artifact.dependencies.operations.fsync(artifact.private.fd); err != nil {
			return workingTreeMetadata{}, fmt.Errorf("projectstate: fsync checkpoint private parent: %w", err)
		}
		durablePrivate, err := checkpointArtifactFstatMetadata(artifact.private.fd, artifact.dependencies.operations)
		if err != nil {
			return workingTreeMetadata{}, err
		}
		if err := checkpointArtifactRequireDurableDirectory(artifact.private, durablePrivate, artifact.dependencies.operations, true); err != nil {
			return workingTreeMetadata{}, fmt.Errorf("projectstate: prove durable checkpoint private parent: %w", err)
		}
		if err := checkpointArtifactFault(artifact.dependencies, checkpointArtifactAfterPrivateParentFsync); err != nil {
			return workingTreeMetadata{}, err
		}
		return durablePrivate, nil
	}
	var durableLiveParent, durablePrivate workingTreeMetadata
	var err error
	if destinationPrivate {
		durablePrivate, err = fsyncPrivateParent()
		if err == nil {
			durableLiveParent, err = fsyncLiveParent()
		}
	} else {
		durableLiveParent, err = fsyncLiveParent()
		if err == nil {
			durablePrivate, err = fsyncPrivateParent()
		}
	}
	if err != nil {
		return checkpointArtifactPublicationParentsProof{}, err
	}
	return checkpointArtifactPublicationParentsProof{liveParent: durableLiveParent, private: durablePrivate}, nil
}

func checkpointArtifactRevalidateAnyFallbackTopology(artifact *checkpointArtifact, stageName, backupName string) error {
	if err := checkpointArtifactRequirePublishedTopology(artifact, stageName, backupName, checkpointTopologyBackedUp); err == nil {
		return nil
	}
	return checkpointArtifactRequirePublishedTopology(artifact, stageName, backupName, checkpointTopologyPublished)
}

func (artifact *checkpointArtifact) close() {
	if artifact == nil {
		return
	}
	artifact.mu.Lock()
	if artifact.closed {
		artifact.mu.Unlock()
		return
	}
	artifact.closed = true
	operations := artifact.dependencies.operations
	if artifact.stage.fd >= 0 {
		_ = operations.close(artifact.stage.fd)
		artifact.stage.fd = -1
	}
	if artifact.private.fd >= 0 {
		_ = operations.close(artifact.private.fd)
		artifact.private.fd = -1
	}
	for index := len(artifact.privateAncestors) - 1; index >= 0; index-- {
		_ = operations.close(artifact.privateAncestors[index].fd)
	}
	artifact.privateAncestors = nil
	if artifact.live.fd >= 0 {
		_ = operations.close(artifact.live.fd)
		artifact.live.fd = -1
	}
	if artifact.git != nil {
		artifact.git.close()
		artifact.git = nil
	}
	if artifact.checkout != nil {
		artifact.checkout.close()
		artifact.checkout = nil
	}
	artifact.mu.Unlock()
}
