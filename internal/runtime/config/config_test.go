package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestLoad_ReportsHomeReadAndDecodeErrors(t *testing.T) {
	t.Run("home unavailable", func(t *testing.T) {
		t.Setenv("HOME", "")
		_, err := Load("default")
		if err == nil || !strings.Contains(err.Error(), "resolve home directory") {
			t.Fatalf("Load: got err %v, want home resolution error", err)
		}
	})

	t.Run("credentials unreadable", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		path := filepath.Join(home, ".wormhole", "credentials", "default.json")
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatalf("mkdir credential path: %v", err)
		}
		_, err := Load("default")
		if err == nil || !strings.Contains(err.Error(), "read credentials") {
			t.Fatalf("Load: got err %v, want read credentials error", err)
		}
	})

	t.Run("credentials malformed", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		dir := filepath.Join(home, ".wormhole", "credentials")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir credentials: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "default.json"), []byte("{"), 0o600); err != nil {
			t.Fatalf("write malformed credentials: %v", err)
		}
		_, err := Load("default")
		if err == nil || !strings.Contains(err.Error(), "decode credentials") {
			t.Fatalf("Load: got err %v, want decode credentials error", err)
		}
	})
}

func writeFakeCredentialsWithProjectID(t *testing.T, home, profile string, projectID string) {
	t.Helper()
	dir := filepath.Join(home, ".wormhole", "credentials")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	data, err := json.Marshal(map[string]string{
		"server":     "http://localhost:8080",
		"project_id": projectID,
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

func TestLoadMultiOrg_NoCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	_, err := LoadMultiOrg()
	if !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("got err %v, want ErrNoCredentials", err)
	}
}

func TestLoadMultiOrg_PopulatesBindings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(home, "run"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))

	// Write multiple profiles with different project IDs
	writeFakeCredentialsWithProjectID(t, home, "acme-corp", "proj-acme")
	writeFakeCredentialsWithProjectID(t, home, "widgets-inc", "proj-widgets")
	// Profile with empty project ID should be skipped
	writeFakeCredentialsWithProjectID(t, home, "no-project", "")

	cfg, err := LoadMultiOrg()
	if err != nil {
		t.Fatalf("LoadMultiOrg: %v", err)
	}

	// Verify orgs are loaded
	if len(cfg.Orgs) != 3 {
		t.Fatalf("got %d orgs, want 3", len(cfg.Orgs))
	}

	// Verify bindings are populated and correct
	if len(cfg.Bindings) != 2 {
		t.Fatalf("got %d bindings, want 2 (empty project_id should be skipped)", len(cfg.Bindings))
	}

	// Find the bindings and verify them
	bindingMap := make(map[string]string)
	for _, b := range cfg.Bindings {
		bindingMap[b.ProjectID] = b.OrgName
	}

	if bindingMap["proj-acme"] != "acme-corp" {
		t.Fatalf("binding for proj-acme: got %q, want acme-corp", bindingMap["proj-acme"])
	}
	if bindingMap["proj-widgets"] != "widgets-inc" {
		t.Fatalf("binding for proj-widgets: got %q, want widgets-inc", bindingMap["proj-widgets"])
	}
	if _, hasNoProject := bindingMap[""]; hasNoProject {
		t.Fatalf("binding for empty project_id should not exist")
	}

	// Verify paths are set correctly
	if cfg.SocketPath != filepath.Join(home, "run", "wormhole", "wormholed.sock") {
		t.Fatalf("got socket path %q", cfg.SocketPath)
	}
	if cfg.DBPath != filepath.Join(home, "data", "wormhole", "wormholed.db") {
		t.Fatalf("got db path %q", cfg.DBPath)
	}
}

func TestLoadMultiOrg_FiltersInvalidEntriesAndUsesFallbackPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("XDG_DATA_HOME", "")
	credDir := filepath.Join(home, ".wormhole", "credentials")
	if err := os.MkdirAll(filepath.Join(credDir, "nested.json"), 0o700); err != nil {
		t.Fatalf("mkdir ignored directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "notes.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatalf("write ignored file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "...json"), []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write invalid-name profile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "malformed.json"), []byte("{"), 0o600); err != nil {
		t.Fatalf("write malformed profile: %v", err)
	}
	if err := os.Symlink(credDir, filepath.Join(credDir, "unreadable.json")); err != nil {
		t.Fatalf("symlink unreadable profile: %v", err)
	}
	writeFakeCredentialsWithProjectID(t, home, "valid", "project-valid")

	cfg, err := LoadMultiOrg()
	if err != nil {
		t.Fatalf("LoadMultiOrg: %v", err)
	}
	if len(cfg.Orgs) != 1 || cfg.Orgs["valid"].Credentials.ProjectID != "project-valid" {
		t.Fatalf("filtered orgs: got %+v", cfg.Orgs)
	}
	if cfg.SocketPath != filepath.Join(os.TempDir(), "wormhole-runtime", "wormhole", "wormholed.sock") {
		t.Fatalf("fallback socket path: got %q", cfg.SocketPath)
	}
	if cfg.DBPath != filepath.Join(home, ".local", "share", "wormhole", "wormholed.db") {
		t.Fatalf("fallback db path: got %q", cfg.DBPath)
	}
}

func TestLoadMultiOrg_ReportsDirectoryAndProfileErrors(t *testing.T) {
	t.Run("home unavailable", func(t *testing.T) {
		t.Setenv("HOME", "")
		_, err := LoadMultiOrg()
		if err == nil || !strings.Contains(err.Error(), "resolve home directory") {
			t.Fatalf("LoadMultiOrg: got err %v, want home resolution error", err)
		}
	})

	t.Run("credentials path is not a directory", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		path := filepath.Join(home, ".wormhole", "credentials")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir parent: %v", err)
		}
		if err := os.WriteFile(path, []byte("file"), 0o600); err != nil {
			t.Fatalf("write credentials path: %v", err)
		}
		_, err := LoadMultiOrg()
		if err == nil || !strings.Contains(err.Error(), "list credentials directory") {
			t.Fatalf("LoadMultiOrg: got err %v, want list error", err)
		}
	})

	t.Run("no valid profiles", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		dir := filepath.Join(home, ".wormhole", "credentials")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir credentials: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "malformed.json"), []byte("{"), 0o600); err != nil {
			t.Fatalf("write malformed profile: %v", err)
		}
		_, err := LoadMultiOrg()
		if !errors.Is(err, ErrNoCredentials) {
			t.Fatalf("LoadMultiOrg: got err %v, want ErrNoCredentials", err)
		}
	})
}
