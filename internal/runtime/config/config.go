// Package config resolves Gateway's local paths and owns credential profile
// persistence (RFC-0003 §6.1, §8.1). It duplicates the
// minimal credentials JSON shape from cmd/wormhole rather than
// importing it: main packages are not importable, and this matches the
// existing wire-shape-duplication precedent at the cmd/wormhole module
// boundary.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrCredentialsNotFound is returned when the named profile has no
// credentials file under ~/.wormhole/credentials/.
var ErrCredentialsNotFound = errors.New("config: credentials not found")

// ErrInvalidProfileName is returned when the profile name passed to Load
// could escape ~/.wormhole/credentials/ (path separators, ".." traversal,
// empty string) — mirrors cmd/wormhole/main.go's
// validateProfileName, since profileName here also originates as a
// command-line argument (os.Args[1] in cmd/gatewayd/main.go).
var ErrInvalidProfileName = errors.New("config: invalid profile name")

// ErrCredentialProfileExists is returned when an atomic enrolment commit
// would replace an existing filesystem object.
var ErrCredentialProfileExists = errors.New("config: credential profile already exists")

// ErrUnsafeCredentialProfile is returned for symlinks, non-regular files,
// unsafe permissions, or unexpected ownership in the credential path.
var ErrUnsafeCredentialProfile = errors.New("config: unsafe credential profile")

// ErrCredentialFilesystemUnsupported is returned on platforms where the
// credential store cannot guarantee handle-based, no-follow reads and an
// atomic no-replace commit. Credential access fails closed on those platforms.
var ErrCredentialFilesystemUnsupported = errors.New("config: secure credential filesystem operations unsupported on this platform")

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
// that Gateway needs to proxy calls to Fabric.
type Credentials struct {
	Server     string    `json:"server"`
	ProjectID  string    `json:"project_id"`
	AgentID    string    `json:"agent_id"`
	PassportID string    `json:"passport_id"`
	Token      string    `json:"token"`
	IssuedAt   time.Time `json:"issued_at"`
	Role       string    `json:"role,omitempty"`
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

// Config is Gateway's resolved local configuration for one run.
type Config struct {
	SocketPath  string
	DBPath      string
	Credentials Credentials
}

// RuntimePaths are available before any credential profile exists, allowing
// a fresh Gateway to expose the same-user enrolment socket.
type RuntimePaths struct {
	SocketPath     string
	DBPath         string
	CredentialsDir string
}

// ResolveRuntimePaths resolves Gateway-owned local paths without reading or
// creating credentials.
func ResolveRuntimePaths() (RuntimePaths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return RuntimePaths{}, fmt.Errorf("config: resolve home directory: %w", err)
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = filepath.Join(os.TempDir(), "wormhole-runtime")
	}
	dataDir := os.Getenv("XDG_DATA_HOME")
	if dataDir == "" {
		dataDir = filepath.Join(home, ".local", "share")
	}
	return RuntimePaths{
		SocketPath:     filepath.Join(runtimeDir, "wormhole", "wormholed.sock"),
		DBPath:         filepath.Join(dataDir, "wormhole", "wormholed.db"),
		CredentialsDir: filepath.Join(home, ".wormhole", "credentials"),
	}, nil
}

// WriteCredentialProfile atomically commits one validated profile beneath
// credentialsDir. Temporary and final files are owner-only; the directory is
// owner-only and synced after rename so a successful return is durable.
func WriteCredentialProfile(credentialsDir, profile string, credentials Credentials) (path string, err error) {
	if err := validateProfileName(profile); err != nil {
		return "", err
	}
	if err := os.MkdirAll(credentialsDir, 0o700); err != nil {
		return "", fmt.Errorf("config: create credentials directory: %w", err)
	}
	info, err := os.Lstat(credentialsDir)
	if err != nil {
		return "", fmt.Errorf("config: inspect credentials directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("config: credentials root is not a directory")
	}
	if err := os.Chmod(credentialsDir, 0o700); err != nil {
		return "", fmt.Errorf("config: restrict credentials directory: %w", err)
	}
	if err := validateCredentialRoot(credentialsDir); err != nil {
		return "", err
	}
	encoded, err := json.MarshalIndent(credentials, "", "  ")
	if err != nil {
		return "", fmt.Errorf("config: encode credential profile: %w", err)
	}
	encoded = append(encoded, '\n')

	temporary, err := os.CreateTemp(credentialsDir, "."+profile+"-*.tmp")
	if err != nil {
		return "", fmt.Errorf("config: create temporary credential profile: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return "", fmt.Errorf("config: restrict temporary credential profile: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		return "", fmt.Errorf("config: write temporary credential profile: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return "", fmt.Errorf("config: sync temporary credential profile: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("config: close temporary credential profile: %w", err)
	}
	path = filepath.Join(credentialsDir, profile+".json")
	if err := commitCredentialNoReplace(temporaryPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", ErrCredentialProfileExists
		}
		return "", fmt.Errorf("config: commit credential profile: %w", err)
	}
	committed = true
	directory, err := os.Open(credentialsDir)
	if err != nil {
		return "", fmt.Errorf("config: open credentials directory for sync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return "", fmt.Errorf("config: sync credentials directory: %w", err)
	}
	return path, nil
}

// ReadCredentialProfile reads one validated profile from a caller-supplied
// Gateway credential root. It is used only to resume a completed enrolment;
// the raw token remains inside Gateway's process.
func ReadCredentialProfile(credentialsDir, profile string) (Credentials, error) {
	if err := validateProfileName(profile); err != nil {
		return Credentials{}, err
	}
	path := filepath.Join(credentialsDir, profile+".json")
	data, err := readCredentialProfileFile(credentialsDir, profile+".json")
	if err != nil {
		return Credentials{}, err
	}
	var credentials Credentials
	if err := json.Unmarshal(data, &credentials); err != nil {
		return Credentials{}, fmt.Errorf("config: decode credential profile %s: %w", path, err)
	}
	return credentials, nil
}

// MultiOrgConfig is Gateway's configuration for multi-org support (P5+).
type MultiOrgConfig struct {
	SocketPath string
	DBPath     string
	Orgs       map[string]Org   // org_name → Org credentials
	Bindings   []ProjectBinding // harness project → (org, project) mappings
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
// and returns them as an org map. Supports multi-org Gateway (RFC-0003 §7.1, P5).
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
