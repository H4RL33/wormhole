//go:build linux

package connector

import (
	"os"
	"syscall"
)

func nativeConnectorFileOwner(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}
