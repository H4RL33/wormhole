package config

import (
	"os"
	"path/filepath"
)

// GlobalConfigPath returns $XDG_CONFIG_HOME/wormhole/config.toml, falling back to ~/.config/wormhole/config.toml.
func GlobalConfigPath() string {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, _ := os.UserHomeDir()
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "wormhole", "config.toml")
}

// LocalConfigPath searches from cwd up to root for .wormhole/config.toml.
// Returns "" if not found.
func LocalConfigPath() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		path := filepath.Join(cwd, ".wormhole", "config.toml")
		if _, err := os.Stat(path); err == nil {
			return path
		}
		parent := filepath.Dir(cwd)
		if parent == cwd {
			break
		}
		cwd = parent
	}
	return ""
}
