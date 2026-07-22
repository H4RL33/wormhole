//go:build linux && (amd64 || arm64)

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"
)

const renameNoReplaceFlag = 1

// quarantineAndRemoveSocket atomically moves the current path into a private
// same-directory quarantine before comparing its inode with the socket that
// was checked. A replacement is restored with RENAME_NOREPLACE and is never
// unlinked. The private directory closes the check/unlink race under RFC-0003
// OQ4's same-user trust model.
func quarantineAndRemoveSocket(socketPath string, expected os.FileInfo, hooks staleSocketRemovalHooks) error {
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
	moved, err := os.Lstat(quarantinePath)
	if err != nil {
		removeQuarantineDir = false
		return fmt.Errorf("wormholed: inspect quarantined socket %s: %w", quarantinePath, err)
	}
	if !os.SameFile(expected, moved) {
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
