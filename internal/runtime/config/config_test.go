package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeFakeCredentials(t *testing.T, home, profile string) {
	t.Helper()
	dir := filepath.Join(home, ".wormhole", "credentials")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	data, err := json.Marshal(map[string]string{
		"server":     "http://localhost:8080",
		"project_id": "project-1",
		"agent_id":   "agent-1",
		"token":      "test-token",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, profile+".json"), data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestLoad_ReadsCredentialsAndDerivesPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(home, "run"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	writeFakeCredentials(t, home, "default")

	cfg, err := Load("default")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Credentials.Server != "http://localhost:8080" {
		t.Fatalf("got server %q", cfg.Credentials.Server)
	}
	if cfg.Credentials.Token != "test-token" {
		t.Fatalf("got token %q", cfg.Credentials.Token)
	}
	if cfg.SocketPath != filepath.Join(home, "run", "wormhole", "wormholed.sock") {
		t.Fatalf("got socket path %q", cfg.SocketPath)
	}
	if cfg.DBPath != filepath.Join(home, "data", "wormhole", "wormholed.db") {
		t.Fatalf("got db path %q", cfg.DBPath)
	}
}

func TestLoad_MissingProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	_, err := Load("nonexistent")
	if !errors.Is(err, ErrCredentialsNotFound) {
		t.Fatalf("got err %v, want ErrCredentialsNotFound", err)
	}
}

func TestLoad_InvalidProfileName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cases := []string{"", "../escape", "a/b", "a\\b", ".."}
	for _, name := range cases {
		_, err := Load(name)
		if !errors.Is(err, ErrInvalidProfileName) {
			t.Fatalf("Load(%q): got err %v, want ErrInvalidProfileName", name, err)
		}
	}
}

func TestLoad_FallsBackToHomeWhenXDGUnset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("XDG_DATA_HOME", "")
	writeFakeCredentials(t, home, "default")

	cfg, err := Load("default")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DBPath != filepath.Join(home, ".local", "share", "wormhole", "wormholed.db") {
		t.Fatalf("got db path %q, want XDG default fallback under home", cfg.DBPath)
	}
}
