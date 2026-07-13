// Package config resolves wormholed's local paths and reads the credential
// profile wormhole-cli already wrote (RFC-0003 §6.1). It duplicates the
// minimal credentials JSON shape from cmd/wormhole-cli/main.go rather than
// importing it: main packages are not importable, and this matches the
// existing wire-shape-duplication precedent at the cmd/wormhole-cli module
// boundary (docs/architecture.md §2). wormholed does not write this file
// in P1 — only reads what wormhole-cli's `wormhole join` already produced.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrCredentialsNotFound is returned when the named profile has no
// credentials file under ~/.wormhole/credentials/.
var ErrCredentialsNotFound = errors.New("config: credentials not found")

// ErrInvalidProfileName is returned when the profile name passed to Load
// could escape ~/.wormhole/credentials/ (path separators, ".." traversal,
// empty string) — mirrors cmd/wormhole-cli/profiles.go's
// validateProfileName, since profileName here also originates as a
// command-line argument (os.Args[1] in cmd/wormholed/main.go).
var ErrInvalidProfileName = errors.New("config: invalid profile name")

// validateProfileName rejects a profile name that could escape the
// credentials directory when joined into a file path. Mirrors
// cmd/wormhole-cli/profiles.go's validateProfileName rules exactly.
func validateProfileName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: %q: must not be empty", ErrInvalidProfileName, name)
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("%w: %q: must not contain path separators", ErrInvalidProfileName, name)
	}
	if name == "." || name == ".." || strings.Contains(name, "..") {
		return fmt.Errorf("%w: %q: must not contain %q", ErrInvalidProfileName, name, "..")
	}
	return nil
}

// Credentials mirrors the fields of cmd/wormhole-cli's credentials struct
// that wormholed needs to proxy calls to the Coordination Server.
type Credentials struct {
	Server    string `json:"server"`
	ProjectID string `json:"project_id"`
	AgentID   string `json:"agent_id"`
	Token     string `json:"token"`
}

// Config is wormholed's resolved local configuration for one run.
type Config struct {
	SocketPath  string
	DBPath      string
	Credentials Credentials
}

// Load resolves paths and reads the named credential profile.
func Load(profileName string) (Config, error) {
	if err := validateProfileName(profileName); err != nil {
		return Config{}, err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, fmt.Errorf("config: resolve home directory: %w", err)
	}

	credPath := filepath.Join(home, ".wormhole", "credentials", profileName+".json")
	data, err := os.ReadFile(credPath)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("%w: profile %q at %s", ErrCredentialsNotFound, profileName, credPath)
	}
	if err != nil {
		return Config{}, fmt.Errorf("config: read credentials %s: %w", credPath, err)
	}
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return Config{}, fmt.Errorf("config: decode credentials %s: %w", credPath, err)
	}

	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = filepath.Join(os.TempDir(), "wormhole-runtime")
	}
	dataDir := os.Getenv("XDG_DATA_HOME")
	if dataDir == "" {
		dataDir = filepath.Join(home, ".local", "share")
	}

	return Config{
		SocketPath:  filepath.Join(runtimeDir, "wormhole", "wormholed.sock"),
		DBPath:      filepath.Join(dataDir, "wormhole", "wormholed.db"),
		Credentials: creds,
	}, nil
}
