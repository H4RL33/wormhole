//go:build linux

package localidentity

import "golang.org/x/sys/unix"

func commitLocalIdentityNoReplace(oldDirectoryFD int, oldName string, newDirectoryFD int, newName string) error {
	return unix.Renameat2(oldDirectoryFD, oldName, newDirectoryFD, newName, unix.RENAME_NOREPLACE)
}
