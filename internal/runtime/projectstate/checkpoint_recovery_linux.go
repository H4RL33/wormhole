//go:build linux

package projectstate

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func recoverCheckpointFilesystem(
	ctx context.Context,
	proof checkpointRecoveryProof,
) (checkpointRecoveryFilesystemOutcome, error) {
	return recoverCheckpointFilesystemWithDependencies(ctx, proof, checkpointArtifactDependencies{
		readGit: readOnlyGitLimited,
	})
}

func recoverCheckpointFilesystemWithDependencies(
	ctx context.Context,
	proof checkpointRecoveryProof,
	dependencies checkpointArtifactDependencies,
) (checkpointRecoveryFilesystemOutcome, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if (proof.kind != checkpointRecoveryPrepared && proof.kind != checkpointRecoveryPublished) || proof.driver == nil {
		return 0, checkpointRecoveryPrecondition("filesystem recovery driver is unavailable", nil)
	}
	artifact, stageName, backupName, err := openCheckpointRecoveryArtifact(ctx, proof, dependencies)
	if err != nil {
		return 0, err
	}
	defer artifact.close()

	topology, err := checkpointRecoveryClassify(ctx, artifact, stageName, backupName)
	if err != nil {
		return 0, err
	}
	if proof.kind == checkpointRecoveryPublished {
		if topology.live.kind == checkpointPublicationAbsent ||
			topology.stage.kind != checkpointPublicationAbsent ||
			topology.backup.kind == checkpointPublicationAbsent {
			return 0, checkpointPublicationBlocked("published recovery topology is incomplete", nil)
		}
		if err := checkpointPublicationFsyncParents(artifact, false); err != nil {
			return 0, err
		}
		return checkpointRecoveryFilesystemRecoveredNew, nil
	}

	liveStable := topology.live.kind != checkpointPublicationAbsent
	backupOld := topology.backup.kind == checkpointPublicationPrior || topology.backup.kind == checkpointPublicationOpaque
	switch {
	case (topology.live.kind == checkpointPublicationPrior || topology.live.kind == checkpointPublicationOpaque) &&
		topology.stage.kind == checkpointPublicationCandidate && topology.backup.kind == checkpointPublicationAbsent:
		if err := checkpointPublicationFsyncParents(artifact, false); err != nil {
			return 0, err
		}
		return checkpointRecoveryFilesystemRecoveredOld, nil

	case topology.live.kind == checkpointPublicationAbsent &&
		topology.stage.kind == checkpointPublicationCandidate && backupOld:
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		terminal := artifact.checkout.ancestry[len(artifact.checkout.ancestry)-1]
		backup := checkpointPublicationEntry{
			kind: topology.backup.kind,
			tree: cloneCheckpointTree(topology.backup.tree),
		}
		renameErr := artifact.dependencies.operations.rename(
			artifact.private.fd, backupName,
			terminal.fd, ".wormhole",
			checkpointNoReplaceRenameFlag(),
		)
		if renameErr != nil {
			observed, classifyErr := checkpointRecoveryClassify(ctx, artifact, stageName, backupName)
			if classifyErr != nil {
				return 0, checkpointPublicationBlocked(
					"classify recovery backup-to-live rename error",
					fmt.Errorf("%w: %w", renameErr, classifyErr),
				)
			}
			switch {
			case checkpointRecoveryRestorePrior(observed, backup):
				return 0, fmt.Errorf("projectstate: restore checkpoint backup: %w", renameErr)
			case checkpointRecoveryRestoreNext(observed, backup):
			default:
				return 0, checkpointPublicationBlocked(
					"recovery backup-to-live rename reached third topology",
					renameErr,
				)
			}
		}
		if err := checkpointPublicationFsyncParents(artifact, false); err != nil {
			return 0, err
		}
		restored, err := checkpointRecoveryClassify(ctx, artifact, stageName, backupName)
		if err != nil {
			return 0, err
		}
		if !checkpointRecoveryRestoreNext(restored, backup) {
			return 0, checkpointPublicationBlocked("restored checkpoint topology differs", nil)
		}
		return checkpointRecoveryFilesystemRecoveredOld, nil

	case liveStable && topology.stage.kind == checkpointPublicationCandidate && backupOld:
		if err := checkpointPublicationFsyncParents(artifact, false); err != nil {
			return 0, err
		}
		return checkpointRecoveryFilesystemRecoveredOld, nil

	case liveStable && topology.stage.kind == checkpointPublicationAbsent && topology.backup.kind == checkpointPublicationPrior:
		if err := checkpointPublicationFsyncParents(artifact, false); err != nil {
			return 0, err
		}
		return checkpointRecoveryFilesystemRecoveredNew, nil

	case liveStable && topology.stage.kind == checkpointPublicationAbsent && topology.backup.kind == checkpointPublicationOpaque:
		return 0, checkpointPublicationBlocked("prepared recovery has an ambiguous backup", nil)

	default:
		return 0, checkpointPublicationBlocked("prepared recovery topology is unsafe or unlisted", nil)
	}
}

func checkpointRecoveryRestorePrior(topology checkpointPublicationTopology, backup checkpointPublicationEntry) bool {
	return topology.live.kind == checkpointPublicationAbsent &&
		topology.stage.kind == checkpointPublicationCandidate &&
		topology.backup.kind == backup.kind &&
		sameCheckpointArtifactTree(topology.backup.tree, backup.tree)
}

func checkpointRecoveryRestoreNext(topology checkpointPublicationTopology, backup checkpointPublicationEntry) bool {
	return topology.live.kind != checkpointPublicationAbsent &&
		topology.stage.kind == checkpointPublicationCandidate &&
		topology.backup.kind == checkpointPublicationAbsent &&
		sameCheckpointArtifactTree(topology.live.tree, backup.tree)
}

func checkpointRecoveryClassify(
	ctx context.Context,
	artifact *checkpointArtifact,
	stageName, backupName string,
) (checkpointPublicationTopology, error) {
	terminalFD := artifact.checkout.ancestry[len(artifact.checkout.ancestry)-1].fd
	privateFD := artifact.private.fd
	containedEntryInspectionStarted := false
	originalFstatat := artifact.dependencies.operations.fstatat
	artifact.dependencies.operations.fstatat = func(parentFD int, name string, stat *unix.Stat_t, flags int) error {
		if (parentFD == terminalFD && name == ".wormhole") ||
			(parentFD == privateFD && (name == stageName || name == backupName)) {
			containedEntryInspectionStarted = true
		}
		return originalFstatat(parentFD, name, stat, flags)
	}
	defer func() {
		artifact.dependencies.operations.fstatat = originalFstatat
	}()

	topology, err := checkpointPublicationClassify(ctx, artifact, stageName, backupName)
	if err == nil {
		return topology, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return checkpointPublicationTopology{}, err
	}
	if !containedEntryInspectionStarted {
		return checkpointPublicationTopology{}, checkpointRecoveryPrecondition(
			"recovery root validation failed",
			err,
		)
	}
	return checkpointPublicationTopology{}, checkpointPublicationBlocked(
		"unsafe or unstable contained recovery evidence",
		err,
	)
}

func openCheckpointRecoveryArtifact(
	ctx context.Context,
	proof checkpointRecoveryProof,
	dependencies checkpointArtifactDependencies,
) (_ *checkpointArtifact, stageName, backupName string, resultErr error) {
	driver := *proof.driver
	dependencies.operations = normalizeCheckpointArtifactOperations(dependencies.operations)
	dependencies.operations = normalizeCheckpointArtifactRenameOperations(dependencies.operations)
	dependencies.mount = normalizeCheckpointArtifactMountOperations(dependencies.mount)
	if dependencies.readGit == nil {
		dependencies.readGit = readOnlyGitLimited
	}

	paths, err := resolveCheckpointGitPathsWithReader(ctx, driver.Checkout.CanonicalPath, dependencies.readGit)
	if err != nil {
		return nil, "", "", checkpointRecoveryPrecondition("resolve recovery Git-private root", err)
	}
	stageName, backupName, err = checkpointRecoveryArtifactNames(driver.JournalID, paths.checkpointRoot, driver.StagePath, driver.BackupPath)
	if err != nil {
		return nil, "", "", err
	}
	checkout, err := openCheckpointArtifactCheckout(driver.Checkout)
	if err != nil {
		return nil, "", "", checkpointRecoveryPrecondition("open recovery checkout", err)
	}
	var git *workingTreeRootHandle
	private := heldWorkingTreeDirectory{fd: -1}
	privateAncestors := make([]heldWorkingTreeDirectory, 0, 1)
	defer func() {
		if resultErr == nil {
			return
		}
		if private.fd >= 0 {
			_ = dependencies.operations.close(private.fd)
		}
		for index := len(privateAncestors) - 1; index >= 0; index-- {
			_ = dependencies.operations.close(privateAncestors[index].fd)
		}
		if git != nil {
			git.close()
		}
		checkout.close()
	}()

	git, err = openWorkingTreeRoot(paths.gitDir)
	if err != nil {
		return nil, "", "", checkpointRecoveryPrecondition("open recovery Git directory", err)
	}
	gitTerminal := git.ancestry[len(git.ancestry)-1]
	wormhole, err := openCheckpointArtifactChildDirectoryWithOperations(gitTerminal, "wormhole", dependencies.operations)
	if err != nil {
		return nil, "", "", checkpointRecoveryPrecondition("open existing recovery private ancestor", err)
	}
	privateAncestors = append(privateAncestors, wormhole)
	private, err = openCheckpointArtifactChildDirectoryWithOperations(wormhole, "checkpoints", dependencies.operations)
	if err != nil {
		return nil, "", "", checkpointRecoveryPrecondition("open existing recovery private root", err)
	}
	terminal := checkout.ancestry[len(checkout.ancestry)-1]
	mountProof, err := checkpointRecoveryMountProof(terminal.fd, private.fd, dependencies.mount)
	if err != nil {
		return nil, "", "", checkpointRecoveryPrecondition("recovery root mount proof unavailable", err)
	}
	trees, err := proveCheckpointArtifactTrees(checkpointArtifactInput{
		Checkout:  driver.Checkout,
		PriorTree: driver.PriorTree, PriorTreeDigest: driver.PriorTreeDigest,
		CandidateTree: driver.CandidateTree, CandidateDigest: driver.CandidateDigest,
	}, defaultCheckpointArtifactTreeLimits())
	if err != nil {
		return nil, "", "", checkpointRecoveryPrecondition("reprove recovery trees", err)
	}
	artifact := &checkpointArtifact{
		checkout: checkout, git: git, private: private, privateAncestors: privateAncestors,
		live: heldWorkingTreeDirectory{fd: -1}, stage: heldWorkingTreeDirectory{fd: -1},
		proof: trees, checkoutIdentity: driver.Checkout, paths: paths, mountProof: mountProof,
		dependencies: dependencies,
	}
	checkout, git = nil, nil
	private.fd = -1
	privateAncestors = nil
	return artifact, stageName, backupName, nil
}

func checkpointRecoveryArtifactNames(
	journalID, privateRoot, stagePath, backupPath string,
) (string, string, error) {
	stageName, backupName := filepath.Base(stagePath), filepath.Base(backupPath)
	validName := func(name string) bool {
		return name != "" && name != "." && name != ".." &&
			!strings.ContainsAny(name, "/\\\x00\r\n")
	}
	if !validName(stageName) || !validName(backupName) ||
		stageName != journalID+".stage" || backupName != journalID+".backup" ||
		filepath.Join(privateRoot, stageName) != stagePath ||
		filepath.Join(privateRoot, backupName) != backupPath {
		return "", "", checkpointRecoveryPrecondition("recovery artifact names are not exact direct children", nil)
	}
	return stageName, backupName, nil
}

func checkpointRecoveryMountProof(
	checkoutFD, privateFD int,
	operations checkpointArtifactMountOperations,
) (checkpointMountProof, error) {
	checkout, checkoutUnique, err := checkpointLinuxTryUniqueMount(checkoutFD, operations)
	if err != nil {
		return checkpointMountProof{}, err
	}
	private, privateUnique, err := checkpointLinuxTryUniqueMount(privateFD, operations)
	if err != nil {
		return checkpointMountProof{}, err
	}
	proof := checkpointMountProof{}
	if checkoutUnique && privateUnique {
		proof = checkpointMountProof{checkout: checkout, live: checkout, private: private, unique: true}
	} else {
		checkout, err = checkpointLinuxLegacyMount(checkoutFD, operations)
		if err != nil {
			return checkpointMountProof{}, err
		}
		private, err = checkpointLinuxLegacyMount(privateFD, operations)
		if err != nil {
			return checkpointMountProof{}, err
		}
		proof = checkpointMountProof{checkout: checkout, live: checkout, private: private}
	}
	if err := checkpointLinuxValidateMountProof(proof); err != nil {
		return checkpointMountProof{}, err
	}
	return proof, nil
}
