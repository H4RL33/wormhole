//go:build linux

package main

import "testing"

func TestSupportedPlatform(t *testing.T) {
	if err := ensureSupportedPlatform(); err != nil {
		t.Fatalf("Linux platform rejected: %v", err)
	}
}
