package types

import (
	"os"
	"testing"
)

func TestLoadConfig_AdminKey(t *testing.T) {
	t.Run("unset by default", func(t *testing.T) {
		orig, wasSet := os.LookupEnv("WORMHOLE_ADMIN_KEY")
		os.Unsetenv("WORMHOLE_ADMIN_KEY")
		t.Cleanup(func() {
			if wasSet {
				os.Setenv("WORMHOLE_ADMIN_KEY", orig)
			}
		})
		cfg := LoadConfig()
		if cfg.AdminKey != "" {
			t.Fatalf("AdminKey: got %q, want empty when WORMHOLE_ADMIN_KEY is unset", cfg.AdminKey)
		}
	})

	t.Run("read from env", func(t *testing.T) {
		t.Setenv("WORMHOLE_ADMIN_KEY", "test-secret-123")
		cfg := LoadConfig()
		if cfg.AdminKey != "test-secret-123" {
			t.Fatalf("AdminKey: got %q, want %q", cfg.AdminKey, "test-secret-123")
		}
	})
}
