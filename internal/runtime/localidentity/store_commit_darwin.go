//go:build darwin

package localidentity

import "golang.org/x/sys/unix"

func commitLocalIdentityNoReplace(oldDirectoryFD int, oldName string, newDirectoryFD int, newName string) error {
	return unix.RenameatxNp(oldDirectoryFD, oldName, newDirectoryFD, newName, unix.RENAME_EXCL)
}
