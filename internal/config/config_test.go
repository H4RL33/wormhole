package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGlobalConfigPath(t *testing.T) {
	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	defer os.Setenv("XDG_CONFIG_HOME", oldXDG)

	os.Setenv("XDG_CONFIG_HOME", "/custom/config")
	path := GlobalConfigPath()
	if path != "/custom/config/wormhole/config.toml" {
		t.Errorf("expected /custom/config/wormhole/config.toml, got %s", path)
	}

	os.Unsetenv("XDG_CONFIG_HOME")
	path = GlobalConfigPath()
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".config", "wormhole", "config.toml")
	if path != expected {
		t.Errorf("expected %s, got %s", expected, path)
	}
}

func TestLocalConfigPath(t *testing.T) {
	tmpdir := t.TempDir()
	wormholeDir := filepath.Join(tmpdir, ".wormhole")
	os.MkdirAll(wormholeDir, 0755)
	configPath := filepath.Join(wormholeDir, "config.toml")
	os.WriteFile(configPath, []byte(""), 0644)

	oldCwd, _ := os.Getwd()
	os.Chdir(tmpdir)
	defer os.Chdir(oldCwd)

	path := LocalConfigPath()
	if path != configPath {
		t.Errorf("expected %s, got %s", configPath, path)
	}
}

func TestLoadSaveConfig(t *testing.T) {
	c := Config{
		Server:  "https://example.com",
		Project: "proj-123",
		Role:    "backend-engineer",
	}

	tmpPath := filepath.Join(t.TempDir(), "test.toml")
	if err := c.Save(tmpPath); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := loadFile(tmpPath)
	if err != nil {
		t.Fatalf("loadFile failed: %v", err)
	}

	if loaded.Server != c.Server || loaded.Project != c.Project || loaded.Role != c.Role {
		t.Errorf("config mismatch: %+v != %+v", loaded, c)
	}
}
