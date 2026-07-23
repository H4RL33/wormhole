//go:build !linux

package main

import (
	"fmt"
	"runtime"
)

func ensureSupportedPlatform() error {
	return fmt.Errorf("unsupported platform %s/%s: gatewayd currently requires Linux; Windows users should run it in WSL", runtime.GOOS, runtime.GOARCH)
}
