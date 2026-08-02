//go:build darwin

package projectstate

import (
	"errors"
	"fmt"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const checkpointPlatform = "darwin"

type checkpointArtifactMountOperations struct {
	fstatfs func(int, *unix.Statfs_t) error
}

type checkpointDarwinMountIdentity struct {
	fsid       unix.Fsid
	mountPoint string
}

type checkpointMountProof struct {
	checkout checkpointDarwinMountIdentity
	live     checkpointDarwinMountIdentity
	private  checkpointDarwinMountIdentity
}

func normalizeCheckpointArtifactMountOperations(operations checkpointArtifactMountOperations) checkpointArtifactMountOperations {
	if operations.fstatfs == nil {
		operations.fstatfs = unix.Fstatfs
	}
	return operations
}

func normalizeCheckpointArtifactRenameOperations(operations checkpointArtifactPlatformOperations) checkpointArtifactPlatformOperations {
	if operations.rename == nil {
		operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
			return unix.RenameatxNp(fromFD, from, toFD, to, uint32(flags))
		}
	}
	return operations
}

func checkpointExchangeRenameFlag() uint  { return unix.RENAME_SWAP }
func checkpointNoReplaceRenameFlag() uint { return unix.RENAME_EXCL }

func freezeCheckpointPlatformCapabilities(checkoutFD, liveFD int, private heldWorkingTreeDirectory, livePath string, dependencies checkpointArtifactDependencies) (checkpointPublicationStrategy, checkpointMountProof, error) {
	proof, err := checkpointDarwinMountProof(checkoutFD, liveFD, private.fd, dependencies.mount)
	if err != nil {
		return 0, checkpointMountProof{}, fmt.Errorf("%w: checkpoint volume proof unavailable: %v", ErrCheckpointUnsupported, err)
	}
	if proof.checkout != proof.live || proof.live != proof.private {
		return 0, checkpointMountProof{}, fmt.Errorf("%w: checkpoint volume identities differ", ErrCheckpointUnsupported)
	}
	if filepath.Clean(proof.live.mountPoint) == filepath.Clean(livePath) {
		return 0, checkpointMountProof{}, fmt.Errorf("%w: live .wormhole is a mount root", ErrCheckpointUnsupported)
	}
	if err := dependencies.operations.fsync(checkoutFD); err != nil {
		return 0, checkpointMountProof{}, fmt.Errorf("%w: checkout directory fsync unsupported: %v", ErrCheckpointUnsupported, err)
	}
	if err := dependencies.operations.fsync(private.fd); err != nil {
		return 0, checkpointMountProof{}, fmt.Errorf("%w: private directory fsync unsupported: %v", ErrCheckpointUnsupported, err)
	}
	strategy, err := checkpointArtifactCapabilityProbe(private, dependencies, unix.RENAME_EXCL, unix.RENAME_SWAP, func(err error) bool {
		return errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP)
	})
	return strategy, proof, err
}

func checkpointDarwinMount(fd int, operations checkpointArtifactMountOperations) (checkpointDarwinMountIdentity, error) {
	var stat unix.Statfs_t
	if err := operations.fstatfs(fd, &stat); err != nil {
		return checkpointDarwinMountIdentity{}, err
	}
	end := 0
	for end < len(stat.Mntonname) && stat.Mntonname[end] != 0 {
		end++
	}
	if end == 0 {
		return checkpointDarwinMountIdentity{}, fmt.Errorf("%w: mount point unavailable", ErrCheckpointUnsupported)
	}
	return checkpointDarwinMountIdentity{fsid: stat.Fsid, mountPoint: string(stat.Mntonname[:end])}, nil
}

func checkpointDarwinMountProof(checkoutFD, liveFD, privateFD int, operations checkpointArtifactMountOperations) (checkpointMountProof, error) {
	checkout, err := checkpointDarwinMount(checkoutFD, operations)
	if err != nil {
		return checkpointMountProof{}, err
	}
	live, err := checkpointDarwinMount(liveFD, operations)
	if err != nil {
		return checkpointMountProof{}, err
	}
	private, err := checkpointDarwinMount(privateFD, operations)
	if err != nil {
		return checkpointMountProof{}, err
	}
	return checkpointMountProof{checkout: checkout, live: live, private: private}, nil
}

func revalidateCheckpointPlatformMounts(checkoutFD, liveFD, privateFD int, livePath string, expected checkpointMountProof, dependencies checkpointArtifactDependencies) error {
	got, err := checkpointDarwinMountProof(checkoutFD, liveFD, privateFD, dependencies.mount)
	if err != nil {
		return fmt.Errorf("%w: checkpoint volume revalidation unavailable: %v", ErrCheckpointUnsupported, err)
	}
	if got != expected || got.checkout != got.live || got.live != got.private || filepath.Clean(got.live.mountPoint) == filepath.Clean(livePath) {
		return fmt.Errorf("%w: checkpoint volume identity changed", ErrCheckpointUnsupported)
	}
	return nil
}
