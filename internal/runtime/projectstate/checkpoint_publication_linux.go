//go:build linux

package projectstate

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	state "github.com/H4RL33/wormhole/internal/types/projectstate"
	"golang.org/x/sys/unix"
)

type checkpointPublicationEntryKind uint8

const (
	checkpointPublicationAbsent checkpointPublicationEntryKind = iota + 1
	checkpointPublicationPrior
	checkpointPublicationCandidate
	checkpointPublicationOpaque
)

type checkpointPublicationEntry struct {
	kind checkpointPublicationEntryKind
	tree state.Tree
}

type checkpointPublicationEntryProof struct {
	entry    checkpointPublicationEntry
	metadata workingTreeMetadata
	fd       int
	stage    bool
}

type checkpointPublicationTopology struct {
	live   checkpointPublicationEntry
	stage  checkpointPublicationEntry
	backup checkpointPublicationEntry
}

func publishCheckpointArtifactFallback(ctx context.Context, artifact *checkpointArtifact) (checkpointPublicationDisposition, error) {
	terminal := artifact.checkout.ancestry[len(artifact.checkout.ancestry)-1]
	stageName := filepath.Base(artifact.evidenceValue.StagePath)
	backupName := filepath.Base(artifact.evidenceValue.BackupPath)

	if err := checkpointArtifactFault(artifact.dependencies, checkpointArtifactBeforeLiveMutation); err != nil {
		return 0, err
	}
	entry, err := checkpointPublicationClassify(ctx, artifact, stageName, backupName)
	if err != nil {
		return 0, checkpointPublicationClassificationError(err)
	}
	if (entry.live.kind == checkpointPublicationCandidate || entry.live.kind == checkpointPublicationOpaque) &&
		entry.stage.kind == checkpointPublicationCandidate && entry.backup.kind == checkpointPublicationAbsent {
		return checkpointPublicationPreservedConcurrentOld, nil
	}
	if !checkpointPublicationIs(entry, checkpointPublicationPrior, checkpointPublicationCandidate, checkpointPublicationAbsent) {
		return 0, checkpointPublicationBlocked("invalid entry topology", nil)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	renameErr := artifact.dependencies.operations.rename(terminal.fd, ".wormhole", artifact.private.fd, backupName, checkpointNoReplaceRenameFlag())
	afterErr := checkpointArtifactFault(artifact.dependencies, checkpointArtifactAfterLiveMutation)
	if renameErr != nil {
		observed, classifyErr := checkpointPublicationClassify(ctx, artifact, stageName, backupName)
		if classifyErr != nil {
			return 0, checkpointPublicationBlocked("classify live-to-backup rename error", classifyErr)
		}
		switch {
		case checkpointPublicationIs(observed, checkpointPublicationPrior, checkpointPublicationCandidate, checkpointPublicationAbsent):
			return 0, fmt.Errorf("projectstate: retain checkpoint live backup: %w", renameErr)
		case checkpointPublicationIs(observed, checkpointPublicationAbsent, checkpointPublicationCandidate, checkpointPublicationPrior):
		default:
			return 0, checkpointPublicationBlocked("live-to-backup rename reached third topology", renameErr)
		}
	} else if afterErr != nil {
		return 0, afterErr
	}

	if err := checkpointPublicationFsyncParents(artifact, true); err != nil {
		return 0, err
	}
	observed, err := checkpointPublicationClassify(ctx, artifact, stageName, backupName)
	if err != nil {
		return 0, checkpointPublicationClassificationError(err)
	}
	if observed.stage.kind != checkpointPublicationCandidate {
		return 0, checkpointPublicationBlocked("candidate stage is unavailable after backup rename", nil)
	}
	if observed.live.kind != checkpointPublicationAbsent {
		if observed.backup.kind == checkpointPublicationPrior || observed.backup.kind == checkpointPublicationOpaque {
			return checkpointPublicationPreservedConcurrentOld, nil
		}
		return 0, checkpointPublicationBlocked("recreated live has ambiguous backup", nil)
	}
	if observed.backup.kind == checkpointPublicationOpaque {
		return checkpointPublicationCompensate(ctx, artifact, stageName, backupName, observed.backup.tree)
	}
	if observed.backup.kind != checkpointPublicationPrior {
		return 0, checkpointPublicationBlocked("backup is not the exact prior tree", nil)
	}

	if err := checkpointArtifactFault(artifact.dependencies, checkpointArtifactBeforeSecondLiveMutation); err != nil {
		return 0, err
	}
	observed, err = checkpointPublicationClassify(ctx, artifact, stageName, backupName)
	if err != nil {
		return 0, checkpointPublicationClassificationError(err)
	}
	if observed.live.kind != checkpointPublicationAbsent {
		if observed.stage.kind == checkpointPublicationCandidate && (observed.backup.kind == checkpointPublicationPrior || observed.backup.kind == checkpointPublicationOpaque) {
			return checkpointPublicationPreservedConcurrentOld, nil
		}
		return 0, checkpointPublicationBlocked("publication boundary changed before stage rename", nil)
	}
	if !checkpointPublicationIs(observed, checkpointPublicationAbsent, checkpointPublicationCandidate, checkpointPublicationPrior) {
		return 0, checkpointPublicationBlocked("publication preimage changed before stage rename", nil)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	renameErr = artifact.dependencies.operations.rename(artifact.private.fd, stageName, terminal.fd, ".wormhole", checkpointNoReplaceRenameFlag())
	afterErr = checkpointArtifactFault(artifact.dependencies, checkpointArtifactAfterSecondLiveMutation)
	if renameErr != nil {
		observed, classifyErr := checkpointPublicationClassify(ctx, artifact, stageName, backupName)
		if classifyErr != nil {
			return 0, checkpointPublicationBlocked("classify stage-to-live rename error", classifyErr)
		}
		switch {
		case checkpointPublicationIs(observed, checkpointPublicationAbsent, checkpointPublicationCandidate, checkpointPublicationPrior):
			return 0, fmt.Errorf("projectstate: publish staged checkpoint tree: %w", renameErr)
		case checkpointPublicationReachedPoint(observed):
		default:
			return 0, checkpointPublicationBlocked("stage-to-live rename reached third topology", renameErr)
		}
	} else if afterErr != nil {
		return 0, afterErr
	}
	if err := checkpointPublicationFsyncParents(artifact, false); err != nil {
		return 0, err
	}
	observed, err = checkpointPublicationClassify(ctx, artifact, stageName, backupName)
	if err != nil {
		return 0, checkpointPublicationClassificationError(err)
	}
	if !checkpointPublicationReachedPoint(observed) {
		return 0, checkpointPublicationBlocked("publication postimage is ambiguous", nil)
	}
	return checkpointPublicationPublished, nil
}

func checkpointPublicationCompensate(ctx context.Context, artifact *checkpointArtifact, stageName, backupName string, opaque state.Tree) (checkpointPublicationDisposition, error) {
	terminal := artifact.checkout.ancestry[len(artifact.checkout.ancestry)-1]
	if err := checkpointArtifactFault(artifact.dependencies, checkpointArtifactBeforeSecondLiveMutation); err != nil {
		return 0, err
	}
	observed, err := checkpointPublicationClassify(ctx, artifact, stageName, backupName)
	if err != nil {
		return 0, checkpointPublicationClassificationError(err)
	}
	if observed.live.kind != checkpointPublicationAbsent {
		if observed.stage.kind == checkpointPublicationCandidate && observed.backup.kind == checkpointPublicationOpaque {
			return checkpointPublicationPreservedConcurrentOld, nil
		}
		return 0, checkpointPublicationBlocked("compensation boundary changed before rename", nil)
	}
	if !checkpointPublicationCompensationPrior(observed, opaque) {
		return 0, checkpointPublicationBlocked("compensation preimage changed before rename", nil)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	renameErr := artifact.dependencies.operations.rename(artifact.private.fd, backupName, terminal.fd, ".wormhole", checkpointNoReplaceRenameFlag())
	afterErr := checkpointArtifactFault(artifact.dependencies, checkpointArtifactAfterSecondLiveMutation)
	if renameErr != nil {
		observed, classifyErr := checkpointPublicationClassify(ctx, artifact, stageName, backupName)
		if classifyErr != nil {
			return 0, checkpointPublicationBlocked("classify compensation rename error", classifyErr)
		}
		switch {
		case checkpointPublicationCompensationPrior(observed, opaque):
			return 0, fmt.Errorf("projectstate: restore concurrent checkpoint live tree: %w", renameErr)
		case checkpointPublicationCompensationNext(observed, opaque):
		default:
			return 0, checkpointPublicationBlocked("compensation rename reached third topology", renameErr)
		}
	} else if afterErr != nil {
		return 0, afterErr
	} else {
		observed, classifyErr := checkpointPublicationClassify(ctx, artifact, stageName, backupName)
		if classifyErr != nil {
			return 0, checkpointPublicationClassificationError(classifyErr)
		}
		if !checkpointPublicationCompensationNext(observed, opaque) {
			return 0, checkpointPublicationBlocked("compensation postimage is ambiguous", nil)
		}
	}
	if err := checkpointPublicationFsyncParents(artifact, false); err != nil {
		return 0, err
	}
	return checkpointPublicationPreservedConcurrentOld, nil
}

func checkpointPublicationClassify(ctx context.Context, artifact *checkpointArtifact, stageName, backupName string) (_ checkpointPublicationTopology, resultErr error) {
	if err := checkpointPublicationRevalidateParents(ctx, artifact); err != nil {
		return checkpointPublicationTopology{}, err
	}
	terminal := artifact.checkout.ancestry[len(artifact.checkout.ancestry)-1]
	entries := []*checkpointPublicationEntryProof{}
	defer func() {
		for _, entry := range entries {
			if entry.fd >= 0 {
				_ = artifact.dependencies.operations.close(entry.fd)
				entry.fd = -1
			}
		}
	}()
	open := func(parentFD int, name string, ownerOnly, stage bool) (checkpointPublicationEntryProof, error) {
		entry, err := checkpointPublicationOpenEntry(ctx, artifact, parentFD, name, ownerOnly, stage)
		if entry.fd >= 0 {
			entries = append(entries, &entry)
		}
		return entry, err
	}
	live, err := open(terminal.fd, ".wormhole", false, false)
	if err != nil {
		return checkpointPublicationTopology{}, err
	}
	stage, err := open(artifact.private.fd, stageName, true, true)
	if err != nil {
		return checkpointPublicationTopology{}, err
	}
	backup, err := open(artifact.private.fd, backupName, false, false)
	if err != nil {
		return checkpointPublicationTopology{}, err
	}
	topology := checkpointPublicationTopology{live: live.entry, stage: stage.entry, backup: backup.entry}
	for _, proof := range []struct {
		parentFD int
		name     string
		entry    *checkpointPublicationEntryProof
	}{
		{terminal.fd, ".wormhole", &live},
		{artifact.private.fd, stageName, &stage},
		{artifact.private.fd, backupName, &backup},
	} {
		if err := checkpointPublicationRevalidateEntry(ctx, artifact, proof.parentFD, proof.name, proof.entry); err != nil {
			return checkpointPublicationTopology{}, err
		}
	}
	return topology, nil
}

func checkpointPublicationOpenEntry(ctx context.Context, artifact *checkpointArtifact, parentFD int, name string, ownerOnly, stage bool) (checkpointPublicationEntryProof, error) {
	operations := artifact.dependencies.operations
	var linked unix.Stat_t
	err := operations.fstatat(parentFD, name, &linked, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		if err := checkpointPublicationProveAbsent(artifact, parentFD, name); err != nil {
			return checkpointPublicationEntryProof{}, err
		}
		return checkpointPublicationEntryProof{entry: checkpointPublicationEntry{kind: checkpointPublicationAbsent}, fd: -1, stage: stage}, nil
	}
	if err != nil {
		return checkpointPublicationEntryProof{}, err
	}
	linkedMetadata := workingTreeStatMetadata(&linked)
	if linkedMetadata.mode&unix.S_IFMT != unix.S_IFDIR || ownerOnly && !checkpointPublicationOwnerDirectory(linkedMetadata) {
		return checkpointPublicationEntryProof{}, fmt.Errorf("unsafe checkpoint publication entry %q", name)
	}
	fd, err := operations.openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return checkpointPublicationEntryProof{}, err
	}
	proof := checkpointPublicationEntryProof{fd: fd, metadata: linkedMetadata, stage: stage}
	fail := func(err error) (checkpointPublicationEntryProof, error) {
		_ = operations.close(fd)
		proof.fd = -1
		return proof, err
	}
	opened, err := checkpointArtifactFstatMetadata(fd, operations)
	if err != nil || opened != linkedMetadata {
		if err == nil {
			err = fmt.Errorf("checkpoint publication entry %q changed while opening", name)
		}
		return fail(err)
	}
	if err := checkpointPublicationProveMount(artifact, fd); err != nil {
		return fail(err)
	}
	tree, err := readCheckpointArtifactTree(ctx, heldWorkingTreeDirectory{fd: fd, parentFD: parentFD, name: name, path: name, metadata: opened})
	if err != nil {
		return fail(err)
	}
	if stage {
		if err := checkpointPublicationValidateStageShape(ctx, heldWorkingTreeDirectory{fd: fd, parentFD: parentFD, name: name, path: name, metadata: opened}, tree); err != nil {
			return fail(err)
		}
	}
	proof.entry.tree = tree
	switch {
	case stage && sameCheckpointArtifactTree(tree, artifact.proof.candidate.tree):
		proof.entry.kind = checkpointPublicationCandidate
	case stage:
		return fail(fmt.Errorf("checkpoint stage is not the exact candidate tree"))
	case sameCheckpointArtifactTree(tree, artifact.proof.prior.tree):
		proof.entry.kind = checkpointPublicationPrior
	case sameCheckpointArtifactTree(tree, artifact.proof.candidate.tree):
		proof.entry.kind = checkpointPublicationCandidate
	default:
		proof.entry.kind = checkpointPublicationOpaque
	}
	return proof, nil
}

func checkpointPublicationValidateStageShape(ctx context.Context, root heldWorkingTreeDirectory, tree state.Tree) error {
	walker := newWorkingTreeWalker(defaultWorkingTreeLimits(), func(workingTreeReadStage, string) error { return ctx.Err() })
	if err := walker.walkDirectory(root, "."); err != nil {
		return err
	}
	sort.Slice(walker.files, func(i, j int) bool { return walker.files[i].Path < walker.files[j].Path })
	if !sameCheckpointArtifactTree(walker.files, tree) {
		return fmt.Errorf("checkpoint stage bytes changed during shape proof")
	}
	expectedDirectories := map[string]struct{}{".": {}}
	for _, file := range tree {
		for directory := filepath.ToSlash(filepath.Dir(file.Path)); directory != "."; directory = filepath.ToSlash(filepath.Dir(directory)) {
			expectedDirectories[directory] = struct{}{}
		}
	}
	if len(walker.directoryMetadata) != len(expectedDirectories) {
		return fmt.Errorf("checkpoint stage directory inventory differs")
	}
	for directory, metadata := range walker.directoryMetadata {
		if _, ok := expectedDirectories[directory]; !ok || !checkpointPublicationOwnerDirectory(metadata) {
			return fmt.Errorf("unsafe checkpoint stage directory %q", directory)
		}
	}
	for path, metadata := range walker.fileMetadata {
		if metadata.mode&unix.S_IFMT != unix.S_IFREG || metadata.uid != uint32(unix.Geteuid()) || metadata.links != 1 || metadata.mode&0o777 != 0o600 || metadata.mode&0o7000 != 0 {
			return fmt.Errorf("unsafe checkpoint stage file %q", path)
		}
	}
	return verifyCheckpointArtifactCapture(ctx, root, defaultWorkingTreeLimits(), walker)
}

func checkpointPublicationRevalidateEntry(ctx context.Context, artifact *checkpointArtifact, parentFD int, name string, proof *checkpointPublicationEntryProof) error {
	if proof.entry.kind == checkpointPublicationAbsent {
		return checkpointPublicationProveAbsent(artifact, parentFD, name)
	}
	operations := artifact.dependencies.operations
	opened, err := checkpointArtifactFstatMetadata(proof.fd, operations)
	if err != nil {
		return fmt.Errorf("checkpoint publication entry %q final descriptor stat: %w", name, err)
	}
	if opened != proof.metadata {
		return fmt.Errorf("checkpoint publication entry %q changed after capture", name)
	}
	var linked unix.Stat_t
	if err := operations.fstatat(parentFD, name, &linked, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("checkpoint publication path %q final stat: %w", name, err)
	}
	if workingTreeStatMetadata(&linked) != proof.metadata {
		return fmt.Errorf("checkpoint publication path %q changed after capture", name)
	}
	if err := checkpointPublicationProveMount(artifact, proof.fd); err != nil {
		return err
	}
	root := heldWorkingTreeDirectory{fd: proof.fd, parentFD: parentFD, name: name, path: name, metadata: proof.metadata}
	tree, err := readCheckpointArtifactTree(ctx, root)
	if err != nil {
		return fmt.Errorf("checkpoint publication bytes at %q final read: %w", name, err)
	}
	if !sameCheckpointArtifactTree(tree, proof.entry.tree) {
		return fmt.Errorf("checkpoint publication bytes at %q changed after capture", name)
	}
	if proof.stage {
		if err := checkpointPublicationValidateStageShape(ctx, root, tree); err != nil {
			return err
		}
	}
	return nil
}

func checkpointPublicationProveAbsent(artifact *checkpointArtifact, parentFD int, name string) error {
	operations := artifact.dependencies.operations
	before, err := checkpointArtifactFstatMetadata(parentFD, operations)
	if err != nil {
		return err
	}
	if err := checkpointPublicationProveParentMount(artifact, parentFD); err != nil {
		return err
	}
	after, err := checkpointArtifactFstatMetadata(parentFD, operations)
	if err != nil {
		return fmt.Errorf("checkpoint publication parent final stat while proving %q absent: %w", name, err)
	}
	if before != after {
		return fmt.Errorf("checkpoint publication parent changed while proving %q absent", name)
	}
	var linked unix.Stat_t
	err = operations.fstatat(parentFD, name, &linked, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err == nil {
		return fmt.Errorf("checkpoint publication entry %q appeared", name)
	}
	return err
}

func checkpointPublicationProveMount(artifact *checkpointArtifact, fd int) error {
	if err := checkpointPublicationProveParentMount(artifact, fd); err != nil {
		return err
	}
	if err := checkpointLinuxValidateLiveMountRoot(fd, artifact.dependencies.mount); err != nil {
		return err
	}
	return nil
}

func checkpointPublicationProveParentMount(artifact *checkpointArtifact, fd int) error {
	var id uint64
	var err error
	if artifact.mountProof.unique {
		var unique bool
		id, unique, err = checkpointLinuxTryUniqueMount(fd, artifact.dependencies.mount)
		if err == nil && !unique {
			err = fmt.Errorf("unique mount identity unavailable")
		}
	} else {
		id, err = checkpointLinuxLegacyMount(fd, artifact.dependencies.mount)
	}
	if err != nil {
		return err
	}
	if id == 0 || id != artifact.mountProof.checkout {
		return fmt.Errorf("checkpoint publication entry changed mount")
	}
	return nil
}

func checkpointPublicationRevalidateParents(ctx context.Context, artifact *checkpointArtifact) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	paths, err := resolveCheckpointGitPathsWithReader(ctx, artifact.checkoutIdentity.CanonicalPath, artifact.dependencies.readGit)
	if err != nil {
		return fmt.Errorf("checkpoint publication resolve Git paths: %w", err)
	}
	if paths != artifact.paths {
		return fmt.Errorf("checkpoint publication Git paths changed")
	}
	if err := artifact.checkout.revalidate(); err != nil {
		return err
	}
	if err := artifact.git.revalidate(); err != nil {
		return err
	}
	for _, ancestor := range artifact.privateAncestors {
		if err := revalidateWorkingTreeDirectoryIdentity(ancestor); err != nil {
			return err
		}
	}
	if _, err := checkpointArtifactValidateOwnerDirectory(artifact.private, artifact.dependencies.operations); err != nil {
		return err
	}
	terminal := artifact.checkout.ancestry[len(artifact.checkout.ancestry)-1]
	if err := checkpointPublicationProveParentMount(artifact, terminal.fd); err != nil {
		return err
	}
	return checkpointPublicationProveParentMount(artifact, artifact.private.fd)
}

func checkpointPublicationFsyncParents(artifact *checkpointArtifact, destinationPrivate bool) error {
	terminal := artifact.checkout.ancestry[len(artifact.checkout.ancestry)-1]
	fsync := func(fd int, before, after checkpointArtifactFaultStage, role string) error {
		if err := checkpointArtifactFault(artifact.dependencies, before); err != nil {
			return err
		}
		if err := artifact.dependencies.operations.fsync(fd); err != nil {
			return fmt.Errorf("projectstate: fsync checkpoint %s parent: %w", role, err)
		}
		return checkpointArtifactFault(artifact.dependencies, after)
	}
	if destinationPrivate {
		if err := fsync(artifact.private.fd, checkpointArtifactBeforePrivateParentFsync, checkpointArtifactAfterPrivateParentFsync, "private"); err != nil {
			return err
		}
		return fsync(terminal.fd, checkpointArtifactBeforeLiveParentFsync, checkpointArtifactAfterLiveParentFsync, "live")
	}
	if err := fsync(terminal.fd, checkpointArtifactBeforeLiveParentFsync, checkpointArtifactAfterLiveParentFsync, "live"); err != nil {
		return err
	}
	return fsync(artifact.private.fd, checkpointArtifactBeforePrivateParentFsync, checkpointArtifactAfterPrivateParentFsync, "private")
}

func checkpointPublicationIs(topology checkpointPublicationTopology, live, stage, backup checkpointPublicationEntryKind) bool {
	return topology.live.kind == live && topology.stage.kind == stage && topology.backup.kind == backup
}

func checkpointPublicationReachedPoint(topology checkpointPublicationTopology) bool {
	return topology.live.kind != checkpointPublicationAbsent && topology.stage.kind == checkpointPublicationAbsent && topology.backup.kind == checkpointPublicationPrior
}

func checkpointPublicationCompensationPrior(topology checkpointPublicationTopology, opaque state.Tree) bool {
	return checkpointPublicationIs(topology, checkpointPublicationAbsent, checkpointPublicationCandidate, checkpointPublicationOpaque) && sameCheckpointArtifactTree(topology.backup.tree, opaque)
}

func checkpointPublicationCompensationNext(topology checkpointPublicationTopology, opaque state.Tree) bool {
	return topology.live.kind == checkpointPublicationOpaque && topology.stage.kind == checkpointPublicationCandidate && topology.backup.kind == checkpointPublicationAbsent && sameCheckpointArtifactTree(topology.live.tree, opaque)
}

func checkpointPublicationOwnerDirectory(metadata workingTreeMetadata) bool {
	return metadata.mode&unix.S_IFMT == unix.S_IFDIR && metadata.uid == uint32(unix.Geteuid()) && metadata.mode&0o777 == 0o700 && metadata.mode&0o7000 == 0
}

func checkpointPublicationClassificationError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return checkpointPublicationBlocked("unsafe or unstable publication evidence", err)
}

func checkpointPublicationBlocked(message string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", ErrCheckpointRecoveryBlocked, message)
	}
	return fmt.Errorf("%w: %s: %w", ErrCheckpointRecoveryBlocked, message, cause)
}
