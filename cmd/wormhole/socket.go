package main

import (
	"os"
	"path/filepath"
)

// wormholedSocketPath derives wormholed's local API socket path
func wormholedSocketPath() string {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = filepath.Join(os.TempDir(), "wormhole-runtime")
	}
	return filepath.Join(runtimeDir, "wormhole", "wormholed.sock")
}
