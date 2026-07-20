package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Server  string `toml:"server,omitempty"`
	Project string `toml:"project,omitempty"`
	Role    string `toml:"role,omitempty"`
}

// LoadGlobal loads global config from GlobalConfigPath. Returns empty Config if file not found.
func LoadGlobal() (Config, error) {
	path := GlobalConfigPath()
	return loadFile(path)
}

// LoadLocal loads local config from LocalConfigPath (walks up from cwd). Returns empty Config if not found.
func LoadLocal() (Config, error) {
	path := LocalConfigPath()
	if path == "" {
		return Config{}, nil
	}
	return loadFile(path)
}

// Save writes config to file path, creating directories as needed.
func (c Config) Save(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(c)
}

// loadFile loads TOML config from file. Returns empty Config if file not found (not an error).
func loadFile(path string) (Config, error) {
	var c Config
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return c, nil
	} else if err != nil {
		return c, err
	}
	_, err := toml.DecodeFile(path, &c)
	return c, err
}
