//go:build linux

package projectstate

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCheckpointLinuxPublicationRequiresNoReplaceWithoutExchange(t *testing.T) {
	input := checkpointArtifactTestInput(t)
	operations := normalizeCheckpointArtifactRenameOperations(defaultCheckpointArtifactPlatformOperations())
	realRename := operations.rename
	var renameFlags []uint
	operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
		renameFlags = append(renameFlags, flags)
		if flags != unix.RENAME_NOREPLACE {
			t.Errorf("capability probe requested rename flag %d, want RENAME_NOREPLACE only", flags)
			return unix.EOPNOTSUPP
		}
		return realRename(fromFD, from, toFD, to, flags)
	}
	artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, checkpointArtifactDependencies{
		readGit: readOnlyGitLimited, operations: operations,
		newJournalID: func() (string, error) { return "88888888-8888-4888-8888-888888888888", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.close()
	if len(renameFlags) == 0 {
		t.Fatal("capability probe did not request RENAME_NOREPLACE")
	}
	for _, flags := range renameFlags {
		if flags != unix.RENAME_NOREPLACE {
			t.Fatalf("capability probe flags = %v, want RENAME_NOREPLACE only", renameFlags)
		}
	}
	evidence := artifact.evidence()
	if filepath.Base(evidence.StagePath) != "88888888-8888-4888-8888-888888888888.stage" ||
		filepath.Base(evidence.BackupPath) != "88888888-8888-4888-8888-888888888888.backup" {
		t.Fatalf("artifact evidence = %+v, want existing stage and backup names", evidence)
	}
}

func TestCheckpointLinuxMountUsesUniqueThenLegacyFallback(t *testing.T) {
	for _, test := range []struct {
		name      string
		firstErr  error
		firstMask uint32
		wantCalls int
	}{
		{name: "unique", firstMask: unix.STATX_MNT_ID_UNIQUE, wantCalls: 1},
		{name: "legacy mask from unique request", firstMask: unix.STATX_MNT_ID, wantCalls: 1},
		{name: "legacy request after EINVAL", firstErr: unix.EINVAL, wantCalls: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			mount, err := checkpointLinuxMount(123, checkpointArtifactMountOperations{statx: func(fd int, path string, flags, mask int, stat *unix.Statx_t) error {
				calls++
				if calls == 1 && test.firstErr != nil {
					return test.firstErr
				}
				stat.Mask = test.firstMask
				if calls > 1 {
					stat.Mask = unix.STATX_MNT_ID
				}
				stat.Mnt_id = 42
				return nil
			}})
			if err != nil || mount != 42 || calls != test.wantCalls {
				t.Fatalf("mount probe = (%d, %v, calls %d), want (42, nil, %d)", mount, err, calls, test.wantCalls)
			}
		})
	}
}

func TestCheckpointLinuxMountProofDoesNotMixIdentityDomains(t *testing.T) {
	calls := make(map[int]int)
	proof, err := checkpointLinuxMountProof(10, 11, 12, checkpointArtifactMountOperations{statx: func(fd int, path string, flags, mask int, stat *unix.Statx_t) error {
		calls[fd]++
		if mask == unix.STATX_MNT_ID_UNIQUE {
			stat.Mnt_id = uint64(100 + fd)
			if fd == 11 {
				stat.Mask = unix.STATX_MNT_ID
			} else {
				stat.Mask = unix.STATX_MNT_ID_UNIQUE
			}
			return nil
		}
		stat.Mask = unix.STATX_MNT_ID
		stat.Mnt_id = 42
		return nil
	}})
	if err != nil || proof.unique || proof.checkout != 42 || proof.live != 42 || proof.private != 42 {
		t.Fatalf("mixed mount proof = (%+v, %v), want coherent legacy IDs", proof, err)
	}
	if !reflect.DeepEqual(calls, map[int]int{10: 2, 11: 2, 12: 2}) {
		t.Fatalf("mixed mount calls = %v, want unique plus legacy for every descriptor", calls)
	}
}

func TestCheckpointLinuxRequiresMountRootAttributeCapability(t *testing.T) {
	for _, test := range []struct {
		name       string
		attributes uint64
		mask       uint64
	}{
		{name: "missing attribute", mask: 0},
		{name: "live mount root", mask: unix.STATX_ATTR_MOUNT_ROOT, attributes: unix.STATX_ATTR_MOUNT_ROOT},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := checkpointLinuxValidateLiveMountRoot(10, checkpointArtifactMountOperations{statx: func(fd int, path string, flags, mask int, stat *unix.Statx_t) error {
				stat.Mask = unix.STATX_MNT_ID
				stat.Attributes_mask = test.mask
				stat.Attributes = test.attributes
				return nil
			}})
			if !errors.Is(err, ErrCheckpointUnsupported) {
				t.Fatalf("mount-root capability error = %v, want ErrCheckpointUnsupported", err)
			}
		})
	}
}

func TestCheckpointLinuxRejectsDifferentSameDomainMountIDs(t *testing.T) {
	for _, unique := range []bool{false, true} {
		proof := checkpointMountProof{checkout: 1, live: 2, private: 1, unique: unique}
		if err := checkpointLinuxValidateMountProof(proof); !errors.Is(err, ErrCheckpointUnsupported) {
			t.Fatalf("different mount IDs (%+v) error = %v", proof, err)
		}
	}
}

func TestCheckpointLinuxRevalidationRepeatsLiveMountRootProof(t *testing.T) {
	for _, attributes := range []uint64{0, unix.STATX_ATTR_MOUNT_ROOT} {
		operations := checkpointArtifactMountOperations{statx: func(fd int, path string, flags, mask int, stat *unix.Statx_t) error {
			stat.Mask = unix.STATX_MNT_ID
			stat.Mnt_id = 42
			if mask&unix.STATX_BASIC_STATS != 0 {
				stat.Attributes_mask = unix.STATX_ATTR_MOUNT_ROOT
				stat.Attributes = attributes
			}
			return nil
		}}
		err := revalidateCheckpointPlatformMounts(10, 11, 12, "", checkpointMountProof{checkout: 42, live: 42, private: 42}, checkpointArtifactDependencies{mount: operations})
		if attributes == 0 && err != nil {
			t.Fatalf("ordinary live mount-root revalidation: %v", err)
		}
		if attributes != 0 && !errors.Is(err, ErrCheckpointUnsupported) {
			t.Fatalf("live mount-root drift = %v, want ErrCheckpointUnsupported", err)
		}
	}
}

func TestCheckpointLinuxMountOperationErrorsAreUnsupported(t *testing.T) {
	dependencies := checkpointArtifactDependencies{mount: checkpointArtifactMountOperations{statx: func(int, string, int, int, *unix.Statx_t) error {
		return unix.EIO
	}}}
	_, err := freezeCheckpointPlatformCapabilities(10, 11, heldWorkingTreeDirectory{fd: 12}, "", dependencies)
	if !errors.Is(err, ErrCheckpointUnsupported) {
		t.Fatalf("freeze mount operation error = %v, want ErrCheckpointUnsupported", err)
	}
	err = revalidateCheckpointPlatformMounts(10, 11, 12, "", checkpointMountProof{checkout: 42, live: 42, private: 42}, dependencies)
	if !errors.Is(err, ErrCheckpointUnsupported) {
		t.Fatalf("revalidate mount operation error = %v, want ErrCheckpointUnsupported", err)
	}
}

func TestCheckpointLinuxFinalBoundaryRejectsFreshCurrentMountMismatch(t *testing.T) {
	input := checkpointArtifactCandidateWithNestedFile(t)
	artifact, err := prepareCheckpointArtifact(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.close()
	held := map[int]bool{
		artifact.checkout.ancestry[len(artifact.checkout.ancestry)-1].fd: true,
		artifact.live.fd:    true,
		artifact.private.fd: true,
	}
	realStatx := unix.Statx
	artifact.dependencies.mount.statx = func(fd int, path string, flags, mask int, stat *unix.Statx_t) error {
		if err := realStatx(fd, path, flags, mask, stat); err != nil {
			return err
		}
		if !held[fd] && mask&(unix.STATX_MNT_ID|unix.STATX_MNT_ID_UNIQUE) != 0 {
			stat.Mnt_id++
		}
		return nil
	}
	renameCalls := 0
	realRename := artifact.dependencies.operations.rename
	artifact.dependencies.operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
		renameCalls++
		return realRename(fromFD, from, toFD, to, flags)
	}
	if _, err := publishPreparedCheckpointArtifact(context.Background(), artifact); !errors.Is(err, ErrCheckpointUnsupported) {
		t.Fatalf("fresh current mount mismatch = %v, want ErrCheckpointUnsupported", err)
	}
	if renameCalls != 0 {
		t.Fatalf("fresh current mount mismatch rename calls = %d, want 0", renameCalls)
	}
}

func TestCheckpointLinuxSecondBoundaryRejectsFreshCurrentMountMismatch(t *testing.T) {
	input := checkpointArtifactCandidateWithNestedFile(t)
	artifact, err := prepareCheckpointArtifact(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.close()
	held := map[int]bool{
		artifact.checkout.ancestry[len(artifact.checkout.ancestry)-1].fd: true,
		artifact.live.fd:    true,
		artifact.private.fd: true,
		artifact.stage.fd:   true,
	}
	armed := false
	artifact.dependencies.fault = func(stage checkpointArtifactFaultStage) error {
		if stage == checkpointArtifactBeforeSecondLiveMutation {
			armed = true
		}
		return nil
	}
	realStatx := unix.Statx
	artifact.dependencies.mount.statx = func(fd int, path string, flags, mask int, stat *unix.Statx_t) error {
		if err := realStatx(fd, path, flags, mask, stat); err != nil {
			return err
		}
		if armed && !held[fd] && mask&(unix.STATX_MNT_ID|unix.STATX_MNT_ID_UNIQUE) != 0 {
			stat.Mnt_id++
		}
		return nil
	}
	renameCalls := 0
	publicationRename := artifact.dependencies.operations.rename
	artifact.dependencies.operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
		renameCalls++
		return publicationRename(fromFD, from, toFD, to, flags)
	}
	if _, err := publishPreparedCheckpointArtifact(context.Background(), artifact); !errors.Is(err, ErrCheckpointUnsupported) {
		t.Fatalf("second-boundary fresh mount mismatch = %v, want ErrCheckpointUnsupported", err)
	}
	if !armed || renameCalls != 1 {
		t.Fatalf("second-boundary fresh mount mismatch = armed %t renames %d, want true/1", armed, renameCalls)
	}
}
