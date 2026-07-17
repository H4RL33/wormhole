package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunConnect_OpenCodeTarget_CreatesFreshConfig confirms `wormhole connect
// --target opencode` writes a brand-new OpenCode config file (parent dirs
// created as needed) with the $schema key set and the connector's MCP entry
// under mcp.<connector-name>, using the confirmed opencode.ai/config.json
// schema (type: "local", command array, enabled).
func TestRunConnect_OpenCodeTarget_CreatesFreshConfig(t *testing.T) {
	fakeWormholedSocket(t) // make socket reachable
	fakeStdioBinary(t)     // add stdio binary to PATH

	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		t.Fatal("connect must not call wormhole.kb.search")
		return searchArticlesOutput{}, nil
	})
	defer srv.Close()

	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	configPath := filepath.Join(t.TempDir(), "nested", "opencode.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"connect",
		"--server", srv.URL,
		"--project", "proj-1",
		"--permissions", "task.read",
		"--token-file", tokenFile,
		"--target", "opencode",
		"--opencode-config", configPath,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read opencode config file: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("decode opencode config: %v (data: %s)", err, data)
	}
	if cfg["$schema"] != "https://opencode.ai/config.json" {
		t.Fatalf("$schema: got %v, want opencode.ai/config.json", cfg["$schema"])
	}
	mcp, ok := cfg["mcp"].(map[string]any)
	if !ok {
		t.Fatalf("mcp key missing or wrong type: %v", cfg["mcp"])
	}
	entry, ok := mcp["wormhole"].(map[string]any)
	if !ok {
		t.Fatalf("mcp.wormhole entry missing or wrong type: %v", mcp["wormhole"])
	}
	if entry["type"] != "local" {
		t.Fatalf("mcp.wormhole.type: got %v, want local", entry["type"])
	}
	// Assert command array with wormhole-mcp-stdio binary
	cmd, ok := entry["command"].([]any)
	if !ok {
		t.Fatalf("mcp.wormhole.command missing or wrong type: %v", entry["command"])
	}
	if len(cmd) != 1 {
		t.Fatalf("mcp.wormhole.command: got %d elements, want 1", len(cmd))
	}
	if !strings.Contains(cmd[0].(string), "wormhole-mcp-stdio") {
		t.Fatalf("mcp.wormhole.command[0] should reference wormhole-mcp-stdio: got %v", cmd[0])
	}
	if entry["enabled"] != true {
		t.Fatalf("mcp.wormhole.enabled: got %v, want true", entry["enabled"])
	}
	// Assert no url or headers fields
	if _, hasURL := entry["url"]; hasURL {
		t.Fatalf("local config should not have url field: %v", entry)
	}
	if _, hasHeaders := entry["headers"]; hasHeaders {
		t.Fatalf("local config should not have headers field: %v", entry)
	}

	if !strings.Contains(stdout.String(), "wormhole") || !strings.Contains(stdout.String(), configPath) {
		t.Fatalf("stdout missing confirmation of written config: %q", stdout.String())
	}
}

// TestRunConnect_OpenCodeTarget_MergesExistingConfig confirms an existing
// config file's unrelated top-level keys and other mcp.* entries survive the
// merge untouched, and an existing $schema is left exactly as found (not
// overwritten with the opencode.ai default).
func TestRunConnect_OpenCodeTarget_MergesExistingConfig(t *testing.T) {
	fakeWormholedSocket(t) // make socket reachable
	fakeStdioBinary(t)     // add stdio binary to PATH

	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		t.Fatal("connect must not call wormhole.kb.search")
		return searchArticlesOutput{}, nil
	})
	defer srv.Close()

	configPath := filepath.Join(t.TempDir(), "opencode.json")
	existing := `{
  "$schema": "https://example.com/custom-schema.json",
  "theme": "dark",
  "mcp": {
    "other-server": {
      "type": "local",
      "command": ["some-binary"]
    }
  }
}`
	if err := os.WriteFile(configPath, []byte(existing), 0o644); err != nil {
		t.Fatalf("seed existing opencode config: %v", err)
	}

	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"connect",
		"--server", srv.URL,
		"--project", "proj-1",
		"--permissions", "task.read",
		"--token-file", tokenFile,
		"--target", "opencode",
		"--opencode-config", configPath,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read opencode config file: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("decode opencode config: %v (data: %s)", err, data)
	}
	if cfg["$schema"] != "https://example.com/custom-schema.json" {
		t.Fatalf("$schema should be preserved unchanged: got %v", cfg["$schema"])
	}
	if cfg["theme"] != "dark" {
		t.Fatalf("unrelated top-level key 'theme' should be preserved: got %v", cfg["theme"])
	}
	mcp, ok := cfg["mcp"].(map[string]any)
	if !ok {
		t.Fatalf("mcp key missing or wrong type: %v", cfg["mcp"])
	}
	other, ok := mcp["other-server"].(map[string]any)
	if !ok {
		t.Fatalf("pre-existing mcp.other-server entry should be preserved: %v", mcp["other-server"])
	}
	if other["type"] != "local" {
		t.Fatalf("mcp.other-server.type should be preserved: got %v", other["type"])
	}
	wormhole, ok := mcp["wormhole"].(map[string]any)
	if !ok {
		t.Fatalf("mcp.wormhole entry should have been added: %v", mcp["wormhole"])
	}
	if wormhole["type"] != "local" {
		t.Fatalf("mcp.wormhole.type: got %v, want local", wormhole["type"])
	}
}

// TestRunConnect_OpenCodeTarget_CustomConnectorName confirms --connector-name
// is used as the mcp.<name> key for the OpenCode path too, matching how
// Claude's connector name is used positionally.
func TestRunConnect_OpenCodeTarget_CustomConnectorName(t *testing.T) {
	fakeWormholedSocket(t) // make socket reachable
	fakeStdioBinary(t)     // add stdio binary to PATH

	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		t.Fatal("connect must not call wormhole.kb.search")
		return searchArticlesOutput{}, nil
	})
	defer srv.Close()

	configPath := filepath.Join(t.TempDir(), "opencode.json")
	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"connect",
		"--server", srv.URL,
		"--project", "proj-1",
		"--permissions", "task.read",
		"--token-file", tokenFile,
		"--target", "opencode",
		"--opencode-config", configPath,
		"--connector-name", "wh-staging",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read opencode config file: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("decode opencode config: %v", err)
	}
	mcp := cfg["mcp"].(map[string]any)
	if _, ok := mcp["wh-staging"]; !ok {
		t.Fatalf("mcp.wh-staging entry missing: %v", mcp)
	}
	if _, ok := mcp["wormhole"]; ok {
		t.Fatalf("default connector name 'wormhole' should not appear when --connector-name overrides it: %v", mcp)
	}
}

// TestRunConnect_OpenCodeTarget_ProtectsConfigFilePermissions confirms the
// OpenCode config file (which would contain sensitive config) is written with
// owner-only permissions (0o600), and the parent directory is created with
// owner-only access (0o700).
func TestRunConnect_OpenCodeTarget_ProtectsConfigFilePermissions(t *testing.T) {
	fakeWormholedSocket(t) // make socket reachable
	fakeStdioBinary(t)     // add stdio binary to PATH

	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		t.Fatal("connect must not call wormhole.kb.search")
		return searchArticlesOutput{}, nil
	})
	defer srv.Close()

	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	configPath := filepath.Join(t.TempDir(), "nested", "opencode.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"connect",
		"--server", srv.URL,
		"--project", "proj-1",
		"--permissions", "task.read",
		"--token-file", tokenFile,
		"--target", "opencode",
		"--opencode-config", configPath,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}

	// Verify config file has owner-only read/write (0o600)
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat config file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config file permissions: got %o, want 0o600", info.Mode().Perm())
	}

	// Verify parent directory has owner-only access (0o700)
	parentDir := filepath.Dir(configPath)
	parentInfo, err := os.Stat(parentDir)
	if err != nil {
		t.Fatalf("stat parent directory: %v", err)
	}
	if parentInfo.Mode().Perm() != 0o700 {
		t.Fatalf("parent directory permissions: got %o, want 0o700", parentInfo.Mode().Perm())
	}
}

// TestRunConnect_UnknownTarget_Errors confirms an invalid --target value is
// rejected before any network call.
func TestRunConnect_UnknownTarget_Errors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"connect",
		"--server", "http://localhost:9999",
		"--project", "proj-1",
		"--target", "bogus-ide",
	}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code: got %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--target") {
		t.Fatalf("stderr missing --target error text: %q", stderr.String())
	}
}

// TestResolveOpenCodeConfigPath_ExplicitFlagWins confirms --opencode-config
// short-circuits all directory-walking logic.
func TestResolveOpenCodeConfigPath_ExplicitFlagWins(t *testing.T) {
	got, err := resolveOpenCodeConfigPath("/explicit/path/opencode.json", t.TempDir())
	if err != nil {
		t.Fatalf("resolveOpenCodeConfigPath: %v", err)
	}
	if got != "/explicit/path/opencode.json" {
		t.Fatalf("got %q, want explicit path unchanged", got)
	}
}

// TestResolveOpenCodeConfigPath_FindsProjectRootConfig confirms the
// walk-up-to-.git behavior: an opencode.json sitting next to .git is found
// even when cwd is a nested subdirectory.
func TestResolveOpenCodeConfigPath_FindsProjectRootConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	projectConfig := filepath.Join(root, "opencode.json")
	if err := os.WriteFile(projectConfig, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("seed project config: %v", err)
	}
	sub := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir nested cwd: %v", err)
	}

	got, err := resolveOpenCodeConfigPath("", sub)
	if err != nil {
		t.Fatalf("resolveOpenCodeConfigPath: %v", err)
	}
	if got != projectConfig {
		t.Fatalf("got %q, want %q", got, projectConfig)
	}
}

// TestResolveOpenCodeConfigPath_NoProjectConfig_FallsBackGlobal confirms
// that when no opencode.json/.jsonc exists on the way up to (and including)
// the nearest .git directory, resolution falls back to the global path
// under $HOME/.config/opencode/opencode.json.
func TestResolveOpenCodeConfigPath_NoProjectConfig_FallsBackGlobal(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir nested cwd: %v", err)
	}

	got, err := resolveOpenCodeConfigPath("", sub)
	if err != nil {
		t.Fatalf("resolveOpenCodeConfigPath: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	want := filepath.Join(home, ".config", "opencode", "opencode.json")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
