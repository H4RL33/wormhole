//go:build !linux || (!amd64 && !arm64)

package main

import (
	"fmt"
	"os"
	"runtime"
)

func quarantineAndRemoveSocket(socketPath string, _ os.FileInfo, _ func()) error {
	return fmt.Errorf("wormholed: safe stale-socket removal is unsupported on %s/%s; refusing to remove %s", runtime.GOOS, runtime.GOARCH, socketPath)
}
