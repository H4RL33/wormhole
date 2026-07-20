package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Harness represents a detected harness (Claude Code or OpenCode)
type Harness struct {
	Name string // "claude" or "opencode"
	Path string // path to binary or config file
	Type string // "stdio" or "config"
}

// detectHarnesses finds all present harnesses: Claude binary and opencode.json
func detectHarnesses() ([]Harness, error) {
	var harnesses []Harness

	// Detect Claude Code
	if claudePath, err := exec.LookPath("claude"); err == nil {
		harnesses = append(harnesses, Harness{
			Name: "claude",
			Path: claudePath,
			Type: "stdio",
		})
	}

	// Detect OpenCode (search from cwd upward for opencode.json or opencode.jsonc)
	cwd, err := os.Getwd()
	if err == nil {
		opencodeConfig := detectOpenCodeConfigPath(cwd)
		if opencodeConfig != "" {
			harnesses = append(harnesses, Harness{
				Name: "opencode",
				Path: opencodeConfig,
				Type: "config",
			})
		}
	}

	return harnesses, nil
}

// detectOpenCodeConfigPath walks up from dir looking for opencode.json or opencode.jsonc
// Returns empty string if not found (does not fall back to home directory)
func detectOpenCodeConfigPath(dir string) string {
	for {
		for _, name := range []string{"opencode.json", "opencode.jsonc"} {
			candidate := filepath.Join(dir, name)
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// wireHarness wires a detected harness to credentials
func wireHarness(h Harness, server, project string) error {
	if h.Type == "stdio" {
		// Claude Code: claude mcp add wormhole -- wormhole mcp
		return wireClaudeMCP(h.Path, server, project)
	} else if h.Type == "config" {
		// OpenCode: update opencode.json
		return wireOpenCodeMCP(h.Path, server, project)
	}
	return fmt.Errorf("unknown harness type: %s", h.Type)
}

// wireClaudeMCP wires Claude Code via `claude mcp add`
func wireClaudeMCP(claudePath, server, project string) error {
	wormholePath, err := exec.LookPath("wormhole")
	if err != nil {
		return fmt.Errorf("wormhole binary not found: %v", err)
	}

	// claude mcp add wormhole -- wormhole mcp
	cmd := exec.Command(claudePath, "mcp", "add", "wormhole", "--", wormholePath, "mcp")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("claude mcp add failed: %v", err)
	}
	return nil
}

// wireOpenCodeMCP wires OpenCode by updating opencode.json config
func wireOpenCodeMCP(configPath, server, project string) error {
	// OpenCode wiring is handled by runConnectOpenCode, which already exists
	// This function is a placeholder for future direct wiring if needed
	return nil
}
