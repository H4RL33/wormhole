//go:build linux

package projectstate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCheckpointLinuxExchangeProbeFallbackWhitelistAndResidue(t *testing.T) {
	for _, test := range []struct {
		name    string
		err     error
		want    checkpointPublicationStrategy
		residue bool
		mutate  bool
	}{
		{name: "enosys", err: unix.ENOSYS, want: checkpointPublicationFallback},
		{name: "einval", err: unix.EINVAL, want: checkpointPublicationFallback},
		{name: "eopnotsupp", err: unix.EOPNOTSUPP, want: checkpointPublicationFallback},
		{name: "eio", err: unix.EIO, residue: true},
		{name: "eperm", err: unix.EPERM, residue: true},
		{name: "eacces", err: unix.EACCES, residue: true},
		{name: "exdev", err: unix.EXDEV, residue: true},
		{name: "unknown errno", err: unix.Errno(0x7fff), residue: true},
		{name: "whitelisted errno with changed topology", err: unix.ENOSYS, residue: true, mutate: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			fd, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(fd)
			operations := defaultCheckpointArtifactPlatformOperations()
			operations.rename = func(fromFD int, from string, toFD int, target string, flags uint) error {
				if flags == unix.RENAME_NOREPLACE {
					return unix.Renameat2(fromFD, from, toFD, target, flags)
				}
				if test.mutate {
					if err := unix.Renameat2(fromFD, from, toFD, target, flags); err != nil {
						return err
					}
				}
				return test.err
			}
			got, err := checkpointLinuxExchangeProbe(fd, checkpointArtifactDependencies{operations: operations})
			if test.want == 0 {
				if err == nil {
					t.Fatal("ambiguous exchange error selected a strategy")
				}
			} else if err != nil || got != test.want {
				t.Fatalf("probe = (%v, %v), want (%v, nil)", got, err, test.want)
			}
			for _, name := range []string{".checkpoint-probe-a", ".checkpoint-probe-b"} {
				_, statErr := os.Lstat(directory + "/" + name)
				if test.residue && statErr != nil {
					t.Fatalf("missing retained ambiguous residue %q: %v", name, statErr)
				}
				if !test.residue && !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("probe residue %q: %v", name, statErr)
				}
			}
		})
	}
}

func TestCheckpointExchangePublishesRealLinux(t *testing.T) {
	input := checkpointArtifactCandidateWithNestedFile(t)
	artifact, err := prepareCheckpointArtifact(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.close()
	if artifact.strategy != checkpointPublicationExchange {
		t.Skip("host filesystem selected durable fallback")
	}
	if err := publishPreparedCheckpointArtifact(context.Background(), artifact); err != nil {
		t.Fatalf("publish real Linux exchange: %v", err)
	}
	assertCheckpointPublishedTopology(t, input, artifact.evidence())
}

func TestCheckpointExchangeAllowsOrdinaryLiveDirectoryMode(t *testing.T) {
	input := checkpointArtifactCandidateWithNestedFile(t)
	livePath := filepath.Join(input.Checkout.CanonicalPath, ".wormhole")
	if err := os.Chmod(livePath, 0o755); err != nil {
		t.Fatal(err)
	}
	artifact, err := prepareCheckpointArtifact(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.close()
	if artifact.strategy != checkpointPublicationExchange {
		t.Skip("host filesystem selected durable fallback")
	}
	if err := publishPreparedCheckpointArtifact(context.Background(), artifact); err != nil {
		t.Fatalf("publish with ordinary live mode: %v", err)
	}
	assertCheckpointPublishedTopology(t, input, artifact.evidence())
}

func TestCheckpointLinuxExchangeProbeProvesNoReplaceAndClosesOnce(t *testing.T) {
	directory := t.TempDir()
	fd, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	operations := defaultCheckpointArtifactPlatformOperations()
	operations = normalizeCheckpointArtifactRenameOperations(operations)
	realRename := operations.rename
	var flags []uint
	operations.rename = func(fromFD int, from string, toFD int, target string, flag uint) error {
		flags = append(flags, flag)
		return realRename(fromFD, from, toFD, target, flag)
	}
	realOpenat := operations.openat
	liveDescriptor := make(map[int]bool)
	doubleClose := false
	operations.openat = func(parentFD int, name string, flags int, mode uint32) (int, error) {
		opened, openErr := realOpenat(parentFD, name, flags, mode)
		if openErr == nil {
			if liveDescriptor[opened] {
				t.Fatalf("open reused still-live descriptor %d", opened)
			}
			liveDescriptor[opened] = true
		}
		return opened, openErr
	}
	realClose := operations.close
	operations.close = func(fd int) error {
		if !liveDescriptor[fd] {
			doubleClose = true
		} else {
			delete(liveDescriptor, fd)
		}
		return realClose(fd)
	}
	strategy, err := checkpointLinuxExchangeProbe(fd, checkpointArtifactDependencies{operations: operations})
	if err != nil || strategy != checkpointPublicationExchange {
		t.Fatalf("real capability probe = (%v, %v)", strategy, err)
	}
	wantFlags := []uint{unix.RENAME_NOREPLACE, unix.RENAME_NOREPLACE, unix.RENAME_NOREPLACE, unix.RENAME_EXCHANGE, unix.RENAME_EXCHANGE}
	if !reflect.DeepEqual(flags, wantFlags) {
		t.Fatalf("rename flags = %v, want %v", flags, wantFlags)
	}
	if doubleClose || len(liveDescriptor) != 0 {
		t.Fatalf("probe descriptor ownership = double-close %t live %v", doubleClose, liveDescriptor)
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
	_, _, err := freezeCheckpointPlatformCapabilities(10, 11, heldWorkingTreeDirectory{fd: 12}, "", dependencies)
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
	if err := publishPreparedCheckpointArtifact(context.Background(), artifact); !errors.Is(err, ErrCheckpointUnsupported) {
		t.Fatalf("fresh current mount mismatch = %v, want ErrCheckpointUnsupported", err)
	}
	if renameCalls != 0 {
		t.Fatalf("fresh current mount mismatch rename calls = %d, want 0", renameCalls)
	}
}

func TestCheckpointLinuxSecondBoundaryRejectsFreshCurrentMountMismatch(t *testing.T) {
	for _, strategy := range []checkpointPublicationStrategy{checkpointPublicationExchange, checkpointPublicationFallback} {
		t.Run([]string{"", "exchange", "fallback"}[strategy], func(t *testing.T) {
			input := checkpointArtifactCandidateWithNestedFile(t)
			operations := normalizeCheckpointArtifactRenameOperations(defaultCheckpointArtifactPlatformOperations())
			if strategy == checkpointPublicationFallback {
				realRename := operations.rename
				operations.rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
					if flags == checkpointExchangeRenameFlag() {
						return unix.EOPNOTSUPP
					}
					return realRename(fromFD, from, toFD, to, flags)
				}
			}
			artifact, err := prepareCheckpointArtifactWithDependencies(context.Background(), input, checkpointArtifactDependencies{readGit: readOnlyGitLimited, operations: operations})
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
			if err := publishPreparedCheckpointArtifact(context.Background(), artifact); !errors.Is(err, ErrCheckpointUnsupported) {
				t.Fatalf("second-boundary fresh mount mismatch = %v, want ErrCheckpointUnsupported", err)
			}
			if !armed || renameCalls != 1 {
				t.Fatalf("second-boundary fresh mount mismatch = armed %t renames %d, want true/1", armed, renameCalls)
			}
		})
	}
}
