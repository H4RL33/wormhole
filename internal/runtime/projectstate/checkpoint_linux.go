//go:build linux

package projectstate

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

const checkpointPlatform = "linux"

type checkpointArtifactMountOperations struct {
	statx func(int, string, int, int, *unix.Statx_t) error
}

type checkpointMountProof struct {
	checkout uint64
	live     uint64
	private  uint64
	unique   bool
}

func normalizeCheckpointArtifactMountOperations(operations checkpointArtifactMountOperations) checkpointArtifactMountOperations {
	if operations.statx == nil {
		operations.statx = unix.Statx
	}
	return operations
}

func normalizeCheckpointArtifactRenameOperations(operations checkpointArtifactPlatformOperations) checkpointArtifactPlatformOperations {
	if operations.rename == nil {
		operations.rename = unix.Renameat2
	}
	return operations
}

func checkpointNoReplaceRenameFlag() uint { return unix.RENAME_NOREPLACE }

func freezeCheckpointPlatformCapabilities(checkoutFD, liveFD int, private heldWorkingTreeDirectory, _ string, dependencies checkpointArtifactDependencies) (checkpointMountProof, error) {
	proof, err := checkpointLinuxMountProof(checkoutFD, liveFD, private.fd, dependencies.mount)
	if err != nil {
		return checkpointMountProof{}, fmt.Errorf("%w: checkpoint mount proof unavailable: %v", ErrCheckpointUnsupported, err)
	}
	if err := checkpointLinuxValidateMountProof(proof); err != nil {
		return checkpointMountProof{}, err
	}
	if err := checkpointLinuxValidateLiveMountRoot(liveFD, dependencies.mount); err != nil {
		return checkpointMountProof{}, fmt.Errorf("%w: live mount-root proof unavailable: %v", ErrCheckpointUnsupported, err)
	}
	if err := dependencies.operations.fsync(checkoutFD); err != nil {
		return checkpointMountProof{}, fmt.Errorf("%w: checkout directory fsync unsupported: %v", ErrCheckpointUnsupported, err)
	}
	if err := dependencies.operations.fsync(private.fd); err != nil {
		return checkpointMountProof{}, fmt.Errorf("%w: private directory fsync unsupported: %v", ErrCheckpointUnsupported, err)
	}
	if err := checkpointArtifactCapabilityProbe(private, dependencies, checkpointNoReplaceRenameFlag()); err != nil {
		return checkpointMountProof{}, err
	}
	return proof, nil
}

func checkpointLinuxMount(fd int, operations checkpointArtifactMountOperations) (uint64, error) {
	id, _, err := checkpointLinuxTryUniqueMount(fd, operations)
	if err != nil {
		return 0, err
	}
	if id != 0 {
		return id, nil
	}
	return checkpointLinuxLegacyMount(fd, operations)
}

func checkpointLinuxTryUniqueMount(fd int, operations checkpointArtifactMountOperations) (uint64, bool, error) {
	var stat unix.Statx_t
	err := operations.statx(fd, "", unix.AT_EMPTY_PATH, unix.STATX_MNT_ID_UNIQUE, &stat)
	if err == nil && stat.Mask&unix.STATX_MNT_ID_UNIQUE != 0 {
		return stat.Mnt_id, true, nil
	}
	if err != nil && !errors.Is(err, unix.EINVAL) {
		return 0, false, err
	}
	if err == nil && stat.Mask&unix.STATX_MNT_ID != 0 {
		return stat.Mnt_id, false, nil
	}
	return 0, false, nil
}

func checkpointLinuxLegacyMount(fd int, operations checkpointArtifactMountOperations) (uint64, error) {
	var stat unix.Statx_t
	err := operations.statx(fd, "", unix.AT_EMPTY_PATH, unix.STATX_MNT_ID, &stat)
	if err != nil {
		return 0, err
	}
	if stat.Mask&unix.STATX_MNT_ID == 0 {
		return 0, fmt.Errorf("%w: mount ID unavailable", ErrCheckpointUnsupported)
	}
	return stat.Mnt_id, nil
}

func checkpointLinuxMountProof(checkoutFD, liveFD, privateFD int, operations checkpointArtifactMountOperations) (checkpointMountProof, error) {
	checkout, checkoutUnique, err := checkpointLinuxTryUniqueMount(checkoutFD, operations)
	if err != nil {
		return checkpointMountProof{}, err
	}
	live, liveUnique, err := checkpointLinuxTryUniqueMount(liveFD, operations)
	if err != nil {
		return checkpointMountProof{}, err
	}
	private, privateUnique, err := checkpointLinuxTryUniqueMount(privateFD, operations)
	if err != nil {
		return checkpointMountProof{}, err
	}
	if checkoutUnique && liveUnique && privateUnique {
		return checkpointMountProof{checkout: checkout, live: live, private: private, unique: true}, nil
	}
	checkout, err = checkpointLinuxLegacyMount(checkoutFD, operations)
	if err != nil {
		return checkpointMountProof{}, err
	}
	live, err = checkpointLinuxLegacyMount(liveFD, operations)
	if err != nil {
		return checkpointMountProof{}, err
	}
	private, err = checkpointLinuxLegacyMount(privateFD, operations)
	if err != nil {
		return checkpointMountProof{}, err
	}
	return checkpointMountProof{checkout: checkout, live: live, private: private}, nil
}

func revalidateCheckpointPlatformMounts(checkoutFD, liveFD, privateFD int, _ string, expected checkpointMountProof, dependencies checkpointArtifactDependencies) error {
	var got checkpointMountProof
	var err error
	if expected.unique {
		got, err = checkpointLinuxMountProof(checkoutFD, liveFD, privateFD, dependencies.mount)
		if err == nil && !got.unique {
			err = fmt.Errorf("%w: unique mount identity became unavailable", ErrCheckpointUnsupported)
		}
	} else {
		got.checkout, err = checkpointLinuxLegacyMount(checkoutFD, dependencies.mount)
		if err == nil {
			got.live, err = checkpointLinuxLegacyMount(liveFD, dependencies.mount)
		}
		if err == nil {
			got.private, err = checkpointLinuxLegacyMount(privateFD, dependencies.mount)
		}
	}
	if err != nil {
		return fmt.Errorf("%w: checkpoint mount revalidation unavailable: %v", ErrCheckpointUnsupported, err)
	}
	if got != expected {
		return fmt.Errorf("%w: checkpoint mount identity changed", ErrCheckpointUnsupported)
	}
	if err := checkpointLinuxValidateMountProof(got); err != nil {
		return err
	}
	if err := checkpointLinuxValidateLiveMountRoot(liveFD, dependencies.mount); err != nil {
		return fmt.Errorf("%w: live mount-root revalidation unavailable: %v", ErrCheckpointUnsupported, err)
	}
	return nil
}

func checkpointLinuxValidateMountProof(proof checkpointMountProof) error {
	if proof.checkout == 0 || proof.live == 0 || proof.private == 0 || proof.checkout != proof.live || proof.live != proof.private {
		return fmt.Errorf("%w: checkpoint mount identities differ", ErrCheckpointUnsupported)
	}
	return nil
}

func checkpointLinuxValidateLiveMountRoot(liveFD int, operations checkpointArtifactMountOperations) error {
	var stat unix.Statx_t
	if err := operations.statx(liveFD, "", unix.AT_EMPTY_PATH, unix.STATX_MNT_ID|unix.STATX_BASIC_STATS, &stat); err != nil {
		return err
	}
	if stat.Attributes_mask&unix.STATX_ATTR_MOUNT_ROOT == 0 {
		return fmt.Errorf("%w: mount-root attribute unavailable", ErrCheckpointUnsupported)
	}
	if stat.Attributes&unix.STATX_ATTR_MOUNT_ROOT != 0 {
		return fmt.Errorf("%w: live .wormhole is a mount root", ErrCheckpointUnsupported)
	}
	return nil
}
