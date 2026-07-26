package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestRuntimePathsResolveWithoutCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(home, "run"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))

	paths, err := ResolveRuntimePaths()
	if err != nil {
		t.Fatalf("ResolveRuntimePaths: %v", err)
	}
	if paths.SocketPath != filepath.Join(home, "run", "wormhole", "wormholed.sock") ||
		paths.DBPath != filepath.Join(home, "data", "wormhole", "wormholed.db") ||
		paths.CredentialsDir != filepath.Join(home, ".wormhole", "credentials") {
		t.Fatalf("runtime paths = %+v", paths)
	}
	if _, err := os.Stat(paths.CredentialsDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ResolveRuntimePaths created credential directory: %v", err)
	}
}

func TestWriteCredentialProfileIsAtomicAndRestrictive(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	issuedAt := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	want := Credentials{
		Server: "https://fabric.example", ProjectID: "project-1", AgentID: "agent-1",
		PassportID: "passport-1", Token: "raw-secret-token", IssuedAt: issuedAt, Role: "contributor",
	}
	path, err := WriteCredentialProfile(root, "project-1__contributor", want)
	if err != nil {
		t.Fatalf("WriteCredentialProfile: %v", err)
	}
	if path != filepath.Join(root, "project-1__contributor.json") {
		t.Fatalf("path = %q", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat credential: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode = %o, want 0600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(root)
	if err != nil {
		t.Fatalf("stat credential root: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("credential root mode = %o, want 0700", dirInfo.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read credential: %v", err)
	}
	var got Credentials
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode credential: %v", err)
	}
	if got != want {
		t.Fatalf("credential mismatch: server=%q project=%q agent=%q passport=%q role=%q issued_at_match=%v token_match=%v",
			got.Server, got.ProjectID, got.AgentID, got.PassportID, got.Role, got.IssuedAt.Equal(want.IssuedAt), got.Token == want.Token)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read credential root: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "project-1__contributor.json" {
		t.Fatalf("credential root entries = %v, want committed profile only", entries)
	}
}

func TestWriteCredentialProfileRejectsPathsAndCleansFailedTemporaryFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	for _, profile := range []string{"", "../escape", "nested/profile", `nested\profile`, ".."} {
		if _, err := WriteCredentialProfile(root, profile, Credentials{}); !errors.Is(err, ErrInvalidProfileName) {
			t.Fatalf("WriteCredentialProfile(%q) error = %v, want ErrInvalidProfileName", profile, err)
		}
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "blocked.json"), 0o700); err != nil {
		t.Fatalf("mkdir blocked target: %v", err)
	}
	if _, err := WriteCredentialProfile(root, "blocked", Credentials{Token: "must-not-leak"}); err == nil {
		t.Fatal("WriteCredentialProfile to directory target returned nil")
	} else if strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("credential write error exposed token: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read root: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") {
			t.Fatalf("failed write left temporary file %q", entry.Name())
		}
	}
}

func TestWriteCredentialProfileDoesNotClobberOccupiedProfile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	first := Credentials{Server: "https://fabric-a.example", ProjectID: "project-a", AgentID: "agent-a", PassportID: "passport-a", Token: "first-secret"}
	if _, err := WriteCredentialProfile(root, "occupied", first); err != nil {
		t.Fatalf("first WriteCredentialProfile: %v", err)
	}
	if _, err := WriteCredentialProfile(root, "occupied", Credentials{Token: "second-secret"}); !errors.Is(err, ErrCredentialProfileExists) {
		t.Fatalf("second WriteCredentialProfile error = %v, want ErrCredentialProfileExists", err)
	}
	got, err := ReadCredentialProfile(root, "occupied")
	if err != nil {
		t.Fatalf("ReadCredentialProfile: %v", err)
	}
	if got.Server != first.Server || got.ProjectID != first.ProjectID || got.AgentID != first.AgentID || got.PassportID != first.PassportID || got.Token != first.Token {
		t.Fatalf("occupied profile changed: server=%q project=%q agent=%q passport=%q token_matches_first=%v",
			got.Server, got.ProjectID, got.AgentID, got.PassportID, got.Token == first.Token)
	}
}

func TestReadCredentialProfileRejectsUnsafeFilesystemObjects(t *testing.T) {
	writeJSON := func(t *testing.T, path string, mode os.FileMode) {
		t.Helper()
		data, err := json.Marshal(Credentials{Server: "https://fabric.example", ProjectID: "project-a", AgentID: "agent-a", PassportID: "passport-a", Token: "secret"})
		if err != nil {
			t.Fatalf("marshal credentials: %v", err)
		}
		if err := os.WriteFile(path, data, mode); err != nil {
			t.Fatalf("write credentials: %v", err)
		}
	}

	t.Run("final symlink", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(t.TempDir(), "target.json")
		writeJSON(t, target, 0o600)
		if err := os.Symlink(target, filepath.Join(root, "profile.json")); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		if _, err := ReadCredentialProfile(root, "profile"); !errors.Is(err, ErrUnsafeCredentialProfile) {
			t.Fatalf("ReadCredentialProfile final symlink error = %v, want ErrUnsafeCredentialProfile", err)
		}
	})

	t.Run("ancestor symlink", func(t *testing.T) {
		parent := t.TempDir()
		realRoot := filepath.Join(parent, "real")
		if err := os.Mkdir(realRoot, 0o700); err != nil {
			t.Fatalf("mkdir real root: %v", err)
		}
		writeJSON(t, filepath.Join(realRoot, "profile.json"), 0o600)
		linkedRoot := filepath.Join(parent, "linked")
		if err := os.Symlink(realRoot, linkedRoot); err != nil {
			t.Fatalf("symlink root: %v", err)
		}
		if _, err := ReadCredentialProfile(linkedRoot, "profile"); !errors.Is(err, ErrUnsafeCredentialProfile) {
			t.Fatalf("ReadCredentialProfile ancestor symlink error = %v, want ErrUnsafeCredentialProfile", err)
		}
	})

	t.Run("non regular final", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "profile.json"), 0o700); err != nil {
			t.Fatalf("mkdir final: %v", err)
		}
		if _, err := ReadCredentialProfile(root, "profile"); !errors.Is(err, ErrUnsafeCredentialProfile) {
			t.Fatalf("ReadCredentialProfile directory error = %v, want ErrUnsafeCredentialProfile", err)
		}
	})

	t.Run("group readable final", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "profile.json")
		writeJSON(t, path, 0o640)
		if _, err := ReadCredentialProfile(root, "profile"); !errors.Is(err, ErrUnsafeCredentialProfile) {
			t.Fatalf("ReadCredentialProfile mode error = %v, want ErrUnsafeCredentialProfile", err)
		}
	})
}

func TestReadCredentialProfileMissingFilePreservesNotExist(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("chmod credential root: %v", err)
	}
	_, err := ReadCredentialProfile(root, "missing")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadCredentialProfile missing error = %v, want os.ErrNotExist", err)
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
