package config

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
)

type ResolveInput struct {
	Flag    string // explicit flag value (may be empty)
	Local   string // value from local config
	Global  string // value from global config
	EnvKey  string // environment variable key to check (optional)
	Default string // fallback default (optional)
}

// Resolve implements precedence: Flag > Local > Global > EnvKey > Default > error
// If required=true and all are empty, returns error.
func Resolve(input ResolveInput, required bool) (string, error) {
	if input.Flag != "" {
		return input.Flag, nil
	}
	if input.Local != "" {
		return input.Local, nil
	}
	if input.Global != "" {
		return input.Global, nil
	}
	if input.EnvKey != "" {
		if val := os.Getenv(input.EnvKey); val != "" {
			return val, nil
		}
	}
	if input.Default != "" {
		return input.Default, nil
	}
	if required {
		return "", fmt.Errorf("required value not resolved (no flag, config, or default)")
	}
	return "", nil
}

// ResolveOwner derives --owner from git config user.name, falling back to $USER.
func ResolveOwner(flagValue string, localConfig, globalConfig Config) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}

	// Try git config user.name
	if owner, err := gitConfigUserName(); err == nil && owner != "" {
		return owner, nil
	}

	// Fall back to $USER
	if u, err := user.Current(); err == nil {
		return u.Username, nil
	}

	return "", fmt.Errorf("owner not resolved")
}

// ResolveRepositories derives --repositories from git remote get-url origin, empty if no repo.
func ResolveRepositories(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	// Try git remote get-url origin
	if repo, err := gitRemoteGetURL("origin"); err == nil {
		return repo, nil
	}
	// Not an error; empty repositories is valid
	return "", nil
}

// ResolveProject derives --project from local config (mandatory if not in flag).
func ResolveProject(flagValue string, localConfig Config) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if localConfig.Project != "" {
		return localConfig.Project, nil
	}
	return "", fmt.Errorf("project not resolved; required (use flag, local .wormhole/config.toml, or wormhole init)")
}

// ResolveServer derives --server from global/local config, error if missing.
func ResolveServer(flagValue string, localConfig, globalConfig Config) (string, error) {
	input := ResolveInput{
		Flag:   flagValue,
		Local:  localConfig.Server,
		Global: globalConfig.Server,
	}
	return Resolve(input, true) // required
}

// gitConfigUserName shells to git config user.name
func gitConfigUserName() (string, error) {
	cmd := exec.Command("git", "config", "user.name")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// gitRemoteGetURL shells to git remote get-url <remote>
func gitRemoteGetURL(remote string) (string, error) {
	cmd := exec.Command("git", "remote", "get-url", remote)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
