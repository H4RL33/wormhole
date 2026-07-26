//go:build darwin

package localapi

import "golang.org/x/sys/unix"

func materializationRenameNoReplace(parentFD int, temporary, target string) error {
	return unix.RenameatxNp(parentFD, temporary, parentFD, target, unix.RENAME_EXCL)
}

func materializationExchange(parentFD int, temporary, target string) error {
	return unix.RenameatxNp(parentFD, temporary, parentFD, target, unix.RENAME_SWAP)
}
