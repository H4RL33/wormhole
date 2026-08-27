//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package config

import "os/exec"

// Platforms without the verified process-group primitive fail before start;
// killing only the leader would leak descendants holding inherited pipes.
func prepareCommandProcessGroup(*exec.Cmd) error { return ErrCommandPlatformUnsupported }

func terminateCommandProcessGroup(*exec.Cmd) error { return ErrCommandPlatformUnsupported }
