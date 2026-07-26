//go:build darwin

package config

import (
	"testing"

	"golang.org/x/sys/unix"
)

// This test is cross-compiled in Linux CI. Keeping the syscall-backed helper
// in the Darwin build prevents a path-check-then-read fallback from returning.
func TestDarwinCredentialRootUsesHandleBasedNoFollow(t *testing.T) {
	fd, err := openCredentialRootNoFollow(t.TempDir())
	if err != nil {
		t.Fatalf("openCredentialRootNoFollow() error = %v", err)
	}
	if err := unix.Close(fd); err != nil {
		t.Fatalf("close credential root: %v", err)
	}
}
