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

func TestLoadGlobalAndLocal(t *testing.T) {
	globalRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", globalRoot)

	global, err := LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal missing file: %v", err)
	}
	if global != (Config{}) {
		t.Fatalf("LoadGlobal missing file: got %+v, want empty config", global)
	}

	wantGlobal := Config{Server: "https://global.example", Role: "reviewer"}
	if err := wantGlobal.Save(GlobalConfigPath()); err != nil {
		t.Fatalf("save global: %v", err)
	}
	global, err = LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if global != wantGlobal {
		t.Fatalf("LoadGlobal: got %+v, want %+v", global, wantGlobal)
	}

	root := t.TempDir()
	nested := filepath.Join(root, "one", "two")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	oldCWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir nested: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })

	local, err := LoadLocal()
	if err != nil {
		t.Fatalf("LoadLocal missing file: %v", err)
	}
	if local != (Config{}) {
		t.Fatalf("LoadLocal missing file: got %+v, want empty config", local)
	}

	wantLocal := Config{Project: "project-nearest", Server: "https://local.example"}
	localPath := filepath.Join(root, ".wormhole", "config.toml")
	if err := wantLocal.Save(localPath); err != nil {
		t.Fatalf("save local: %v", err)
	}
	local, err = LoadLocal()
	if err != nil {
		t.Fatalf("LoadLocal ancestor file: %v", err)
	}
	if local != wantLocal {
		t.Fatalf("LoadLocal ancestor file: got %+v, want %+v", local, wantLocal)
	}
}

func TestConfigFilesystemErrors(t *testing.T) {
	t.Run("Save create directory failure", func(t *testing.T) {
		parentFile := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(parentFile, []byte("file"), 0o600); err != nil {
			t.Fatalf("write parent file: %v", err)
		}
		if err := (Config{}).Save(filepath.Join(parentFile, "config.toml")); err == nil {
			t.Fatal("Save below a regular file: got nil error")
		}
	})

	t.Run("Save create file failure", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatalf("mkdir target: %v", err)
		}
		if err := (Config{}).Save(path); err == nil {
			t.Fatal("Save to directory: got nil error")
		}
	})

	t.Run("load stat failure", func(t *testing.T) {
		parentFile := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(parentFile, []byte("file"), 0o600); err != nil {
			t.Fatalf("write parent file: %v", err)
		}
		if _, err := loadFile(filepath.Join(parentFile, "config.toml")); err == nil {
			t.Fatal("loadFile below a regular file: got nil error")
		}
	})

	t.Run("load malformed TOML", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte("server = ["), 0o600); err != nil {
			t.Fatalf("write malformed config: %v", err)
		}
		if _, err := loadFile(path); err == nil {
			t.Fatal("loadFile malformed TOML: got nil error")
		}
	})
}
