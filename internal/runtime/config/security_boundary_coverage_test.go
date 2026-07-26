package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCredentialStoreRejectsUnsafeRootsProfilesAndMalformedPayloads(t *testing.T) {
	t.Run("write root is file", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "credentials")
		if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := WriteCredentialProfile(root, "profile", Credentials{}); err == nil {
			t.Fatal("file credential root unexpectedly accepted")
		}
	})

	t.Run("write root is symlink", func(t *testing.T) {
		parent := t.TempDir()
		target := filepath.Join(parent, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(parent, "credentials")
		if err := os.Symlink(target, root); err != nil {
			t.Fatal(err)
		}
		if _, err := WriteCredentialProfile(root, "profile", Credentials{}); err == nil {
			t.Fatal("symlink credential root unexpectedly accepted")
		}
	})

	t.Run("read unsafe profile objects", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "credentials")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside.json")
		if err := os.WriteFile(outside, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "symlink.json")); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadCredentialProfile(root, "symlink"); !errors.Is(err, ErrUnsafeCredentialProfile) {
			t.Fatalf("symlink profile error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, "permissive.json"), []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadCredentialProfile(root, "permissive"); !errors.Is(err, ErrUnsafeCredentialProfile) {
			t.Fatalf("permissive profile error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, "malformed.json"), []byte(`{`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadCredentialProfile(root, "malformed"); err == nil {
			t.Fatal("malformed credential profile unexpectedly decoded")
		}
		if _, err := ReadCredentialProfile(root, "../escape"); !errors.Is(err, ErrInvalidProfileName) {
			t.Fatalf("unsafe profile name error = %v", err)
		}
	})
}

func TestResolveRuntimePathsUsesDocumentedFallbacks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("XDG_DATA_HOME", "")
	paths, err := ResolveRuntimePaths()
	if err != nil {
		t.Fatal(err)
	}
	if paths.SocketPath == "" || paths.DBPath == "" || paths.CredentialsDir == "" {
		t.Fatalf("fallback paths = %#v", paths)
	}
}

func TestCredentialStoreFailsClosedAcrossRootTraversalBoundaries(t *testing.T) {
	t.Run("nested root under file", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "blocker")
		if err := os.WriteFile(parent, []byte("block"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := WriteCredentialProfile(filepath.Join(parent, "credentials"), "profile", Credentials{}); err == nil {
			t.Fatal("credential writer created a root beneath a regular file")
		}
	})

	t.Run("missing root and profile", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing")
		if _, err := openCredentialRootNoFollow(missing); err == nil {
			t.Fatal("secure root open accepted a missing directory")
		}
		root := filepath.Join(t.TempDir(), "credentials")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadCredentialProfile(root, "missing"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing profile error = %v, want os.ErrNotExist", err)
		}
	})

	t.Run("permissive root", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "credentials")
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if fd, err := openCredentialRootNoFollow(root); !errors.Is(err, ErrUnsafeCredentialProfile) {
			if fd >= 0 {
				_ = fd
			}
			t.Fatalf("permissive root error = %v", err)
		}
	})

}
