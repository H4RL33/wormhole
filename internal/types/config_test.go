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

func TestLoadConfig_DefaultsAndNumericEnvironment(t *testing.T) {
	envKeys := []string{
		"WORMHOLE_LISTEN_ADDR",
		"WORMHOLE_DATABASE_URL",
		"WORMHOLE_KB_DEDUP_THRESHOLD",
		"WORMHOLE_KB_MAX_BODY_LENGTH",
		"WORMHOLE_KB_MIN_LINKS_DECISION",
		"WORMHOLE_KB_MIN_LINKS_POLICY",
		"WORMHOLE_KB_MIN_LINKS_PROCEDURE",
		"WORMHOLE_COHERE_API_KEY",
		"WORMHOLE_ADMIN_KEY",
	}
	for _, key := range envKeys {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
	}

	defaults := LoadConfig()
	if defaults.ListenAddr != ":8080" {
		t.Fatalf("default ListenAddr: got %q", defaults.ListenAddr)
	}
	if defaults.KBDedupThreshold != 0.85 || defaults.KBMaxBodyLength != 2000 {
		t.Fatalf("default KB config: got threshold=%v max=%d", defaults.KBDedupThreshold, defaults.KBMaxBodyLength)
	}
	if defaults.KBMinLinksDecision != 1 || defaults.KBMinLinksPolicy != 1 || defaults.KBMinLinksProcedure != 1 {
		t.Fatalf("default minimum links: got decision=%d policy=%d procedure=%d", defaults.KBMinLinksDecision, defaults.KBMinLinksPolicy, defaults.KBMinLinksProcedure)
	}

	t.Setenv("WORMHOLE_LISTEN_ADDR", "127.0.0.1:9090")
	t.Setenv("WORMHOLE_DATABASE_URL", "postgres://example/test")
	t.Setenv("WORMHOLE_KB_DEDUP_THRESHOLD", "0.42")
	t.Setenv("WORMHOLE_KB_MAX_BODY_LENGTH", "4096")
	t.Setenv("WORMHOLE_KB_MIN_LINKS_DECISION", "2")
	t.Setenv("WORMHOLE_KB_MIN_LINKS_POLICY", "3")
	t.Setenv("WORMHOLE_KB_MIN_LINKS_PROCEDURE", "4")

	configured := LoadConfig()
	if configured.ListenAddr != "127.0.0.1:9090" || configured.DatabaseURL != "postgres://example/test" {
		t.Fatalf("string config: got listen=%q database=%q", configured.ListenAddr, configured.DatabaseURL)
	}
	if configured.KBDedupThreshold != 0.42 || configured.KBMaxBodyLength != 4096 {
		t.Fatalf("numeric KB config: got threshold=%v max=%d", configured.KBDedupThreshold, configured.KBMaxBodyLength)
	}
	if configured.KBMinLinksDecision != 2 || configured.KBMinLinksPolicy != 3 || configured.KBMinLinksProcedure != 4 {
		t.Fatalf("configured minimum links: got decision=%d policy=%d procedure=%d", configured.KBMinLinksDecision, configured.KBMinLinksPolicy, configured.KBMinLinksProcedure)
	}
}

func TestLoadConfig_InvalidNumbersKeepDefaults(t *testing.T) {
	t.Setenv("WORMHOLE_KB_DEDUP_THRESHOLD", "not-a-float")
	t.Setenv("WORMHOLE_KB_MAX_BODY_LENGTH", "not-an-int")
	t.Setenv("WORMHOLE_KB_MIN_LINKS_DECISION", "not-an-int")
	t.Setenv("WORMHOLE_KB_MIN_LINKS_POLICY", "not-an-int")
	t.Setenv("WORMHOLE_KB_MIN_LINKS_PROCEDURE", "not-an-int")

	cfg := LoadConfig()
	if cfg.KBDedupThreshold != 0.85 || cfg.KBMaxBodyLength != 2000 {
		t.Fatalf("invalid numeric values changed defaults: threshold=%v max=%d", cfg.KBDedupThreshold, cfg.KBMaxBodyLength)
	}
	if cfg.KBMinLinksDecision != 1 || cfg.KBMinLinksPolicy != 1 || cfg.KBMinLinksProcedure != 1 {
		t.Fatalf("invalid minimum links changed defaults: decision=%d policy=%d procedure=%d", cfg.KBMinLinksDecision, cfg.KBMinLinksPolicy, cfg.KBMinLinksProcedure)
	}
}

func TestLoadConfig_ApprovedEmbeddingConfiguration(t *testing.T) {
	t.Setenv("WORMHOLE_COHERE_API_KEY", "cohere-secret")
	cfg := LoadConfig()
	want := EmbeddingConfig{
		Provider: "cohere", Model: "embed-v4.0", Version: "4.0", Dimension: 1024,
		APIKey: "cohere-secret",
	}
	if cfg.KBEmbedding != want {
		t.Fatalf("KBEmbedding = %+v, want approved descriptor", cfg.KBEmbedding)
	}
	if err := ValidateEmbeddingConfig(cfg.KBEmbedding); err != nil {
		t.Fatalf("ValidateEmbeddingConfig: %v", err)
	}
}

func TestValidateEmbeddingConfigRejectsMissingOrUnapprovedValues(t *testing.T) {
	approved := EmbeddingConfig{
		Provider: "cohere", Model: "embed-v4.0", Version: "4.0", Dimension: 1024, APIKey: "key",
	}
	tests := []struct {
		name   string
		mutate func(*EmbeddingConfig)
	}{
		{"missing key", func(c *EmbeddingConfig) { c.APIKey = "" }},
		{"provider", func(c *EmbeddingConfig) { c.Provider = "other" }},
		{"model", func(c *EmbeddingConfig) { c.Model = "embed-v3.0" }},
		{"version", func(c *EmbeddingConfig) { c.Version = "3.0" }},
		{"dimension", func(c *EmbeddingConfig) { c.Dimension = 16 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := approved
			test.mutate(&cfg)
			if err := ValidateEmbeddingConfig(cfg); err == nil {
				t.Fatal("ValidateEmbeddingConfig accepted missing/unapproved configuration")
			}
		})
	}
}

func TestLoadConfig_PrivateGitCredential(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ambient-token-must-not-be-used")

	cfg := LoadConfig()
	if cfg.GitHubObserver.APIBaseURL != "https://api.github.com" || cfg.GitHubObserver.CredentialRef != "" || cfg.GitHubObserver.Credential != "" {
		t.Fatal("LoadConfig imported an ambient credential or selected an unexpected GitHub API origin")
	}
}
