// Package config resolves wormholed's local paths and reads the credential
// profile `wormhole join` already wrote (RFC-0003 §6.1). It duplicates the
// minimal credentials JSON shape from cmd/wormhole rather than
// importing it: main packages are not importable, and this matches the
// existing wire-shape-duplication precedent at the cmd/wormhole module
// boundary. wormholed does not write this file — it only reads what
// `wormhole join` already produced.
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
// empty string) — mirrors cmd/wormhole/main.go's
// validateProfileName, since profileName here also originates as a
// command-line argument (os.Args[1] in cmd/wormholed/main.go).
var ErrInvalidProfileName = errors.New("config: invalid profile name")

// ErrNoCredentials is returned when LoadMultiOrg finds no credential profiles.
var ErrNoCredentials = errors.New("config: no credential profiles found")

// validateProfileName rejects a profile name that could escape the
// credentials directory when joined into a file path. Mirrors
// cmd/wormhole/main.go's validateProfileName rules exactly.
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

// Credentials mirrors the fields of cmd/wormhole's credentials struct
// that wormholed needs to proxy calls to the Coordination Server.
type Credentials struct {
	Server    string `json:"server"`
	ProjectID string `json:"project_id"`
	AgentID   string `json:"agent_id"`
	Token     string `json:"token"`
}

// Org wraps credentials with an org identifier (RFC-0003 §7.1: multi-org support).
type Org struct {
	Name        string      // org identifier (e.g. "acme-corp")
	Credentials Credentials // server, projectID, agentID, token for this org
}

// ProjectBinding maps a harness project context to a specific (org, project)
// tuple (RFC-0003 §7.1: explicit project bindings, no implicit default).
type ProjectBinding struct {
	ProjectID string // the harness project context
	OrgName   string // which org to use for this project
}

// Config is wormholed's resolved local configuration for one run.
type Config struct {
	SocketPath string
	DBPath     string
	Credentials Credentials
}

// MultiOrgConfig is wormholed's configuration for multi-org support (P5+).
type MultiOrgConfig struct {
	SocketPath string
	DBPath     string
	Orgs       map[string]Org        // org_name → Org credentials
	Bindings   []ProjectBinding      // harness project → (org, project) mappings
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

// LoadMultiOrg reads all credential profiles from ~/.wormhole/credentials/
// and returns them as an org map. Supports multi-org wormholed (RFC-0003 §7.1, P5).
// Returns ErrNoCredentials if no profiles are found.
// RFC-0003 §7.1 requires explicit project bindings: each org's ProjectID (if non-empty)
// is automatically bound to that org, with no implicit default.
func LoadMultiOrg() (MultiOrgConfig, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return MultiOrgConfig{}, fmt.Errorf("config: resolve home directory: %w", err)
	}

	credDir := filepath.Join(home, ".wormhole", "credentials")
	entries, err := os.ReadDir(credDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return MultiOrgConfig{}, fmt.Errorf("%w: credentials directory does not exist", ErrNoCredentials)
		}
		return MultiOrgConfig{}, fmt.Errorf("config: list credentials directory: %w", err)
	}

	orgs := make(map[string]Org)
	bindings := []ProjectBinding{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		profileName := strings.TrimSuffix(entry.Name(), ".json")
		if err := validateProfileName(profileName); err != nil {
			continue // skip invalid profile names silently
		}

		credPath := filepath.Join(credDir, entry.Name())
		data, err := os.ReadFile(credPath)
		if err != nil {
			continue // skip unreadable files silently
		}
		var creds Credentials
		if err := json.Unmarshal(data, &creds); err != nil {
			continue // skip malformed files silently
		}

		orgs[profileName] = Org{Name: profileName, Credentials: creds}

		// Build bindings: each org with a non-empty ProjectID gets a binding.
		// This ensures explicit bindings per RFC-0003 §7.1 with no implicit default.
		if creds.ProjectID != "" {
			bindings = append(bindings, ProjectBinding{
				ProjectID: creds.ProjectID,
				OrgName:   profileName,
			})
		}
	}

	if len(orgs) == 0 {
		return MultiOrgConfig{}, fmt.Errorf("%w: no valid credential profiles found in %s", ErrNoCredentials, credDir)
	}

	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = filepath.Join(os.TempDir(), "wormhole-runtime")
	}
	dataDir := os.Getenv("XDG_DATA_HOME")
	if dataDir == "" {
		dataDir = filepath.Join(home, ".local", "share")
	}

	return MultiOrgConfig{
		SocketPath: filepath.Join(runtimeDir, "wormhole", "wormholed.sock"),
		DBPath:     filepath.Join(dataDir, "wormhole", "wormholed.db"),
		Orgs:       orgs,
		Bindings:   bindings,
	}, nil
}
