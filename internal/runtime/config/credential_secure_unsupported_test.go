//go:build !linux && !darwin

package config

import (
	"errors"
	"testing"
)

func TestUnsupportedCredentialFilesystemFailsClosed(t *testing.T) {
	root := t.TempDir()
	if _, err := WriteCredentialProfile(root, "profile", Credentials{Token: "secret"}); !errors.Is(err, ErrCredentialFilesystemUnsupported) {
		t.Fatalf("WriteCredentialProfile() error = %v, want ErrCredentialFilesystemUnsupported", err)
	}
	if _, err := ReadCredentialProfile(root, "profile"); !errors.Is(err, ErrCredentialFilesystemUnsupported) {
		t.Fatalf("ReadCredentialProfile() error = %v, want ErrCredentialFilesystemUnsupported", err)
	}
}
