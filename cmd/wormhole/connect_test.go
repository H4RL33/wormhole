package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// fakeClaude creates a fake claude binary and adds it to PATH
func fakeClaude(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	claudePath := filepath.Join(dir, "claude")
	script := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(claudePath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude binary: %v", err)
	}
	// Add to PATH
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", dir+":"+oldPath)
	t.Cleanup(func() {
		os.Setenv("PATH", oldPath)
	})
	return claudePath
}

// fakeWormhole creates a fake wormhole binary and adds it to PATH
func fakeWormhole(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	wormholePath := filepath.Join(dir, "wormhole")
	script := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(wormholePath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake wormhole binary: %v", err)
	}
	// Add to PATH
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", dir+":"+oldPath)
	t.Cleanup(func() {
		os.Setenv("PATH", oldPath)
	})
	return wormholePath
}

// TestDetectHarnesses_FindsClaude confirms detectHarnesses finds the claude binary
func TestDetectHarnesses_FindsClaude(t *testing.T) {
	// Use fakeClaude to add a mock claude binary to PATH
	fakeClaude(t)

	harnesses, err := detectHarnesses()
	if err != nil {
		t.Fatalf("detectHarnesses failed: %v", err)
	}

	found := false
	for _, h := range harnesses {
		if h.Name == "claude" && h.Type == "stdio" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected claude harness to be detected, got: %+v", harnesses)
	}
}

// TestDetectHarnesses_FindsOpenCode confirms detectHarnesses finds opencode.json
func TestDetectHarnesses_FindsOpenCode(t *testing.T) {
	tmpDir := t.TempDir()
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get current directory: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("change to temp directory: %v", err)
	}
	defer os.Chdir(oldCwd)

	// Create opencode.json
	opencodeFile := filepath.Join(tmpDir, "opencode.json")
	if err := os.WriteFile(opencodeFile, []byte("{}"), 0644); err != nil {
		t.Fatalf("create opencode.json: %v", err)
	}

	harnesses, err := detectHarnesses()
	if err != nil {
		t.Fatalf("detectHarnesses failed: %v", err)
	}

	found := false
	for _, h := range harnesses {
		if h.Name == "opencode" && h.Type == "config" {
			found = true
			if h.Path != opencodeFile {
				t.Errorf("opencode path: expected %q, got %q", opencodeFile, h.Path)
			}
			break
		}
	}
	if !found {
		t.Errorf("expected opencode harness to be detected, got: %+v", harnesses)
	}
}

// TestDetectHarnesses_FindsOpenCodeJsonc confirms detectHarnesses finds opencode.jsonc
func TestDetectHarnesses_FindsOpenCodeJsonc(t *testing.T) {
	tmpDir := t.TempDir()
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get current directory: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("change to temp directory: %v", err)
	}
	defer os.Chdir(oldCwd)

	// Create opencode.jsonc
	opencodeFile := filepath.Join(tmpDir, "opencode.jsonc")
	if err := os.WriteFile(opencodeFile, []byte("{}"), 0644); err != nil {
		t.Fatalf("create opencode.jsonc: %v", err)
	}

	harnesses, err := detectHarnesses()
	if err != nil {
		t.Fatalf("detectHarnesses failed: %v", err)
	}

	found := false
	for _, h := range harnesses {
		if h.Name == "opencode" && h.Type == "config" {
			found = true
			if h.Path != opencodeFile {
				t.Errorf("opencode path: expected %q, got %q", opencodeFile, h.Path)
			}
			break
		}
	}
	if !found {
		t.Errorf("expected opencode harness to be detected, got: %+v", harnesses)
	}
}

// TestDetectHarnesses_WalksUpForOpenCode confirms detectHarnesses walks up to find opencode.json
func TestDetectHarnesses_WalksUpForOpenCode(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "subdir", "nested")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("create nested directory: %v", err)
	}

	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get current directory: %v", err)
	}
	if err := os.Chdir(subDir); err != nil {
		t.Fatalf("change to nested directory: %v", err)
	}
	defer os.Chdir(oldCwd)

	// Create opencode.json at the top level
	opencodeFile := filepath.Join(tmpDir, "opencode.json")
	if err := os.WriteFile(opencodeFile, []byte("{}"), 0644); err != nil {
		t.Fatalf("create opencode.json: %v", err)
	}

	harnesses, err := detectHarnesses()
	if err != nil {
		t.Fatalf("detectHarnesses failed: %v", err)
	}

	found := false
	for _, h := range harnesses {
		if h.Name == "opencode" && h.Type == "config" {
			found = true
			if h.Path != opencodeFile {
				t.Errorf("opencode path: expected %q, got %q", opencodeFile, h.Path)
			}
			break
		}
	}
	if !found {
		t.Errorf("expected opencode harness to be detected by walking up, got: %+v", harnesses)
	}
}

// TestDetectHarnesses_NoHarnesses confirms detectHarnesses returns empty list when nothing is found
func TestDetectHarnesses_NoHarnesses(t *testing.T) {
	tmpDir := t.TempDir()
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get current directory: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("change to temp directory: %v", err)
	}
	defer os.Chdir(oldCwd)

	// Modify PATH to exclude claude
	os.Setenv("PATH", tmpDir)
	defer os.Unsetenv("PATH")

	harnesses, err := detectHarnesses()
	if err != nil {
		t.Fatalf("detectHarnesses failed: %v", err)
	}

	if len(harnesses) != 0 {
		t.Errorf("expected no harnesses, got: %+v", harnesses)
	}
}

// TestWireHarness_Claude confirms wireHarness calls claude mcp add for stdio harness
func TestWireHarness_Claude(t *testing.T) {
	fakeClaude(t)
	fakeWormhole(t)

	claudeHarness := Harness{
		Name: "claude",
		Path: "claude",
		Type: "stdio",
	}

	err := wireHarness(claudeHarness, "https://example.com", "proj-1")
	if err != nil {
		t.Fatalf("wireHarness failed: %v", err)
	}
}

// TestWireHarness_OpenCode confirms wireHarness handles config harness type
func TestWireHarness_OpenCode(t *testing.T) {
	fakeWormhole(t) // wiring resolves the wormhole stdio binary from PATH

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "opencode.json")

	opencodeHarness := Harness{
		Name: "opencode",
		Path: configPath,
		Type: "config",
	}

	err := wireHarness(opencodeHarness, "https://example.com", "proj-1")
	if err != nil {
		t.Fatalf("wireHarness failed: %v", err)
	}

	// The harness must actually be wired into the config, not silently skipped.
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("opencode config not written: %v", err)
	}
	if !bytes.Contains(data, []byte("wormhole")) {
		t.Fatalf("opencode config missing wormhole connector: %s", data)
	}
}

// TestWireHarness_InvalidType confirms wireHarness returns error for unknown type
func TestWireHarness_InvalidType(t *testing.T) {
	harness := Harness{
		Name: "unknown",
		Path: "/some/path",
		Type: "invalid",
	}

	err := wireHarness(harness, "https://example.com", "proj-1")
	if err == nil {
		t.Errorf("expected error for invalid harness type, got nil")
	}
}

// TestRunConnect_AutoDetectsHarnesses confirms connect auto-detects and wires all harnesses
func TestRunConnect_AutoDetectsHarnesses(t *testing.T) {
	fakeWormholedSocket(t)
	fakeClaude(t)
	fakeWormhole(t)
	fakeStdioBinary(t)

	// Create opencode.json in a temp directory
	tmpDir := t.TempDir()
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get current directory: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("change to temp directory: %v", err)
	}
	defer os.Chdir(oldCwd)

	opencodeFile := filepath.Join(tmpDir, "opencode.json")
	if err := os.WriteFile(opencodeFile, []byte("{}"), 0644); err != nil {
		t.Fatalf("create opencode.json: %v", err)
	}

	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		t.Fatal("connect must not call wormhole.kb.search")
		return searchArticlesOutput{}, nil
	})
	defer srv.Close()

	tokenFile := filepath.Join(tmpDir, "credentials.json")
	var stdout, stderr bytes.Buffer
	// Call connect without --target (should auto-detect)
	code := run([]string{
		"connect",
		"--server", srv.URL,
		"--project", "proj-1",
		"--permissions", "task.read",
		"--token-file", tokenFile,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}

	// Verify output indicates both harnesses were wired
	output := stdout.String()
	if !bytes.Contains(stdout.Bytes(), []byte("wired")) {
		t.Errorf("expected wiring output, got: %q", output)
	}
}

// TestRunConnect_TargetFlagDeprecated confirms --target flag still works but is deprecated
func TestRunConnect_TargetFlagDeprecated(t *testing.T) {
	fakeWormholedSocket(t)
	fakeClaude(t)
	fakeWormhole(t)
	fakeStdioBinary(t)

	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		t.Fatal("connect must not call wormhole.kb.search")
		return searchArticlesOutput{}, nil
	})
	defer srv.Close()

	tokenFile := filepath.Join(t.TempDir(), "credentials.json")
	var stdout, stderr bytes.Buffer
	// Call connect with --target claude (should still work)
	code := run([]string{
		"connect",
		"--server", srv.URL,
		"--project", "proj-1",
		"--permissions", "task.read",
		"--token-file", tokenFile,
		"--target", "claude",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}
}

// TestRunConnect_NoHarnesses confirms connect fails gracefully when no harnesses are detected
func TestRunConnect_NoHarnesses(t *testing.T) {
	fakeWormholedSocket(t)
	// Isolate PATH to an empty dir so a real claude/opencode installed on the
	// dev machine isn't picked up by detectHarnesses (which walks the ambient
	// PATH). Without this, /usr/bin/claude would be detected and the test would
	// never see the no-harness path.
	t.Setenv("PATH", t.TempDir())

	// Modify PATH to exclude claude and remove opencode.json
	tmpDir := t.TempDir()
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get current directory: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("change to temp directory: %v", err)
	}
	defer os.Chdir(oldCwd)

	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		t.Fatal("connect must not call wormhole.kb.search")
		return searchArticlesOutput{}, nil
	})
	defer srv.Close()

	tokenFile := filepath.Join(tmpDir, "credentials.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"connect",
		"--server", srv.URL,
		"--project", "proj-1",
		"--permissions", "task.read",
		"--token-file", tokenFile,
	}, &stdout, &stderr)
	// Should fail because no harnesses detected
	if code == 0 {
		t.Fatalf("exit code: got %d, want non-zero (no harnesses detected)", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("no harnesses detected")) {
		t.Errorf("expected 'no harnesses detected' error, got stderr: %q", stderr.String())
	}
}
