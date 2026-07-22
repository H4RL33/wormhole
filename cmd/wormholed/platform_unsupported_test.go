//go:build !linux

package main

import (
	"strings"
	"testing"
)

func TestUnsupportedPlatformFailsWithGuidance(t *testing.T) {
	err := ensureSupportedPlatform()
	if err == nil {
		t.Fatal("unsupported platform was accepted")
	}
	if message := err.Error(); !strings.Contains(message, "requires Linux") || !strings.Contains(message, "WSL") {
		t.Fatalf("unsupported-platform error lacks Linux/WSL guidance: %q", message)
	}
}
