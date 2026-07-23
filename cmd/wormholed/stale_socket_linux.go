//go:build linux && (amd64 || arm64)

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const renameNoReplaceFlag = 1

func openStaleSocketIdentity(socketPath string) (staleSocketIdentity, error) {
	fd, err := unix.Open(socketPath, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return staleSocketIdentity{}, fmt.Errorf("wormholed: open stale socket path: %w", err)
	}

	var expected unix.Stat_t
	if err := unix.Fstat(fd, &expected); err != nil {
		_ = unix.Close(fd)
		return staleSocketIdentity{}, fmt.Errorf("wormholed: stat stale socket descriptor: %w", err)
	}
	if expected.Mode&unix.S_IFMT != unix.S_IFSOCK {
		_ = unix.Close(fd)
		return staleSocketIdentity{}, fmt.Errorf("wormholed: stale socket path %s is not a socket", socketPath)
	}
	return staleSocketIdentity{
		dev: uint64(expected.Dev),
		ino: expected.Ino,
		close: func() {
			_ = unix.Close(fd)
		},
	}, nil
}

// quarantineAndRemoveSocket atomically moves the current path into a private
// same-directory quarantine before comparing its inode with the socket that
// was checked. A replacement is restored with RENAME_NOREPLACE and is never
// unlinked. The private directory closes the check/unlink race under RFC-0003
// OQ4's same-user trust model.
func quarantineAndRemoveSocket(socketPath string, expectedDev, expectedIno uint64, hooks staleSocketRemovalHooks) error {
	if hooks.beforeQuarantine != nil {
		hooks.beforeQuarantine()
	}

	quarantineDir, err := os.MkdirTemp(filepath.Dir(socketPath), ".wormholed-stale-")
	if err != nil {
		return fmt.Errorf("wormholed: create stale-socket quarantine: %w", err)
	}
	quarantinePath := filepath.Join(quarantineDir, "socket")
	removeQuarantineDir := true
	defer func() {
		if removeQuarantineDir {
			_ = os.Remove(quarantineDir)
		}
	}()

	if err := os.Rename(socketPath, quarantinePath); err != nil {
		return fmt.Errorf("wormholed: socket changed during stale-socket removal: %w", err)
	}
	if hooks.afterQuarantine != nil {
		hooks.afterQuarantine(quarantinePath)
	}
	var moved unix.Stat_t
	if err := unix.Lstat(quarantinePath, &moved); err != nil {
		removeQuarantineDir = false
		return fmt.Errorf("wormholed: inspect quarantined socket %s: %w", quarantinePath, err)
	}
	if moved.Dev != expectedDev || moved.Ino != expectedIno {
		if err := renameNoReplace(quarantinePath, socketPath); err != nil {
			removeQuarantineDir = false
			return fmt.Errorf("wormholed: socket changed during stale-socket removal; replacement preserved at %s: restore: %w", quarantinePath, err)
		}
		return fmt.Errorf("wormholed: socket changed during stale-socket removal")
	}
	if err := os.Remove(quarantinePath); err != nil {
		removeQuarantineDir = false
		return fmt.Errorf("wormholed: remove quarantined stale socket %s: %w", quarantinePath, err)
	}
	return nil
}

func renameNoReplace(oldPath, newPath string) error {
	oldPtr, err := syscall.BytePtrFromString(oldPath)
	if err != nil {
		return err
	}
	newPtr, err := syscall.BytePtrFromString(newPath)
	if err != nil {
		return err
	}
	var trap uintptr
	switch runtime.GOARCH {
	case "amd64":
		trap = 316
	case "arm64":
		trap = 276
	default:
		return syscall.ENOSYS
	}
	atFDCWD := ^uintptr(99) // -100, represented as uintptr for syscall.Syscall6.
	_, _, errno := syscall.Syscall6(
		trap,
		atFDCWD, uintptr(unsafe.Pointer(oldPtr)),
		atFDCWD, uintptr(unsafe.Pointer(newPtr)),
		renameNoReplaceFlag, 0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}
