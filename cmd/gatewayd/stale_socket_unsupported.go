//go:build !linux || (!amd64 && !arm64)

package main

import (
	"fmt"
	"os"
	"runtime"
)

func openStaleSocketIdentity(socketPath string) (staleSocketIdentity, error) {
	info, err := os.Lstat(socketPath)
	if err != nil {
		return staleSocketIdentity{}, err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return staleSocketIdentity{}, fmt.Errorf("gatewayd: stale socket path %s is not a socket", socketPath)
	}
	return staleSocketIdentity{}, unsupportedStaleSocketRemovalError(socketPath)
}

func quarantineAndRemoveSocket(socketPath string, _, _ uint64, _ staleSocketRemovalHooks) error {
	return unsupportedStaleSocketRemovalError(socketPath)
}

func unsupportedStaleSocketRemovalError(socketPath string) error {
	return fmt.Errorf("gatewayd: safe stale-socket removal is unsupported on %s/%s; refusing to remove %s; verify it is stale, remove it manually, and restart on Linux", runtime.GOOS, runtime.GOARCH, socketPath)
}
