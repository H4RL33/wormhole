//go:build linux

package projectstate

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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
		if err := artifact.dependencies.operations.rename(
			artifact.private.fd, backupName,
			terminal.fd, ".wormhole",
			checkpointNoReplaceRenameFlag(),
		); err != nil {
			return 0, fmt.Errorf("projectstate: restore checkpoint backup: %w", err)
		}
		if err := checkpointPublicationFsyncParents(artifact, false); err != nil {
			return 0, err
		}
		restored, err := checkpointRecoveryClassify(ctx, artifact, stageName, backupName)
		if err != nil {
			return 0, err
		}
		if restored.live.kind == checkpointPublicationAbsent ||
			restored.stage.kind != checkpointPublicationCandidate ||
			restored.backup.kind != checkpointPublicationAbsent ||
			!sameCheckpointArtifactTree(restored.live.tree, topology.backup.tree) {
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

func checkpointRecoveryClassify(
	ctx context.Context,
	artifact *checkpointArtifact,
	stageName, backupName string,
) (checkpointPublicationTopology, error) {
	topology, err := checkpointPublicationClassify(ctx, artifact, stageName, backupName)
	if err == nil {
		return topology, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return checkpointPublicationTopology{}, err
	}
	rootErr := checkpointPublicationRevalidateParents(ctx, artifact)
	if rootErr != nil {
		if errors.Is(rootErr, context.Canceled) || errors.Is(rootErr, context.DeadlineExceeded) {
			return checkpointPublicationTopology{}, rootErr
		}
		return checkpointPublicationTopology{}, checkpointRecoveryPrecondition(
			"persistent recovery root drift",
			errors.Join(err, rootErr),
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
