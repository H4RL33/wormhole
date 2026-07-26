//go:build darwin

package config

import "golang.org/x/sys/unix"

func commitCredentialNoReplace(temporaryPath, finalPath string) error {
	return unix.RenameatxNp(unix.AT_FDCWD, temporaryPath, unix.AT_FDCWD, finalPath, unix.RENAME_EXCL)
}
