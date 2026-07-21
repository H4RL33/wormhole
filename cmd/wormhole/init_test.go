package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/H4RL33/wormhole/internal/config"
)

// runInit rejects any positional argument before touching stdin.
func TestRunInitRejectsArgs(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runInit([]string{"foo"}, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "takes no arguments") {
		t.Fatalf("stderr = %q, want 'takes no arguments'", errOut.String())
	}
}

// initWizard writes a config from the prompted values when none exists yet.
func TestInitWizardFreshWrite(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".wormhole", "config.toml")

	stdin := strings.NewReader("https://wh.example.org\nproj-9\nplatform-engineer\n")
	var out, errOut bytes.Buffer
	code := initWizard(stdin, &out, &errOut, configPath)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, errOut.String())
	}

	got := loadConfig(t, configPath)
	want := config.Config{Server: "https://wh.example.org", Project: "proj-9", Role: "platform-engineer"}
	if got != want {
		t.Fatalf("config = %+v, want %+v", got, want)
	}
	if !strings.Contains(out.String(), "Configuration saved to") {
		t.Fatalf("stdout missing save confirmation: %q", out.String())
	}
}

// Blank role falls back to the default; blank server is left empty (not a placeholder).
func TestInitWizardDefaultsAndBlanks(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".wormhole", "config.toml")

	// server blank, project blank, role blank
	stdin := strings.NewReader("\n\n\n")
	var out, errOut bytes.Buffer
	code := initWizard(stdin, &out, &errOut, configPath)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, errOut.String())
	}

	got := loadConfig(t, configPath)
	if got.Server != "" {
		t.Fatalf("server = %q, want empty (no placeholder written)", got.Server)
	}
	if got.Project != "" {
		t.Fatalf("project = %q, want empty", got.Project)
	}
	if got.Role != "backend-engineer" {
		t.Fatalf("role = %q, want default backend-engineer", got.Role)
	}
}

// An existing config is preserved when the overwrite prompt is declined.
func TestInitWizardOverwriteDeclined(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".wormhole", "config.toml")
	original := config.Config{Server: "https://keep.me", Project: "orig", Role: "orig-role"}
	if err := original.Save(configPath); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	// answer "n" to overwrite prompt (also cover the default: empty == No)
	for _, answer := range []string{"n\n", "\n"} {
		stdin := strings.NewReader(answer)
		var out, errOut bytes.Buffer
		code := initWizard(stdin, &out, &errOut, configPath)
		if code != 1 {
			t.Fatalf("answer %q: exit code = %d, want 1", answer, code)
		}
		if !strings.Contains(out.String(), "Aborted") {
			t.Fatalf("answer %q: stdout missing 'Aborted': %q", answer, out.String())
		}
		if got := loadConfig(t, configPath); got != original {
			t.Fatalf("answer %q: config mutated to %+v, want %+v", answer, got, original)
		}
	}
}

// Answering "y" to the overwrite prompt replaces the existing config.
func TestInitWizardOverwriteAccepted(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".wormhole", "config.toml")
	original := config.Config{Server: "https://old", Project: "old", Role: "old"}
	if err := original.Save(configPath); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	// "y" then the three field answers
	stdin := strings.NewReader("y\nhttps://new\nnewproj\nnewrole\n")
	var out, errOut bytes.Buffer
	code := initWizard(stdin, &out, &errOut, configPath)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, errOut.String())
	}

	got := loadConfig(t, configPath)
	want := config.Config{Server: "https://new", Project: "newproj", Role: "newrole"}
	if got != want {
		t.Fatalf("config = %+v, want %+v", got, want)
	}
}

func loadConfig(t *testing.T, path string) config.Config {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config %s: %v", path, err)
	}
	var c config.Config
	if _, err := toml.Decode(string(data), &c); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	return c
}
