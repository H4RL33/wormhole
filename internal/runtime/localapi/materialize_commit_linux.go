//go:build linux

package localapi

import "golang.org/x/sys/unix"

func materializationRenameNoReplace(parentFD int, temporary, target string) error {
	return unix.Renameat2(parentFD, temporary, parentFD, target, unix.RENAME_NOREPLACE)
}

func materializationExchange(parentFD int, temporary, target string) error {
	return unix.Renameat2(parentFD, temporary, parentFD, target, unix.RENAME_EXCHANGE)
}
