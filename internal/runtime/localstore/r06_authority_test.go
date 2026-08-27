package localstore

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestR06PrivateFormatHardCutAuthorities(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Dir(thisFile)
	for _, name := range []string{"migrations.go", "localstore.go", "workspace_materialization_repo.go"} {
		contents, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		source := string(contents)
		for _, forbidden := range []string{"applyGatewayMigrations", "migrateWhoAmICacheProjectKey", "migrateChannelCreatedAt", "PublicationReviewProofVersion == 0"} {
			if strings.Contains(source, forbidden) {
				t.Fatalf("%s still contains removed private compatibility authority %q", name, forbidden)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(root, "migrations")); err == nil {
		entries, readErr := os.ReadDir(filepath.Join(root, "migrations"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(entries) != 0 {
			t.Fatalf("incremental private migrations remain: %v", entries)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "private_schema_v6.sql")); !os.IsNotExist(err) {
		t.Fatalf("former private v6 snapshot remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "private_schema_v7.sql")); err != nil {
		t.Fatalf("consolidated private v7 snapshot missing: %v", err)
	}
}

func TestCurrentPrivateFormatDocsDescribeV7HardCut(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
	for name, required := range map[string][]string{
		"README.md":                        {"schema-v7 format", "former exact v6"},
		"agents/README.md":                 {"originally initialized fresh Gateway state directly as schema v6", "supersession advances the current consolidated hard-cut epoch from v6 to v7"},
		"docs/compatibility.md":            {"schema-v7", "initialized atomically as v7", "former exact v6"},
		"docs/implementation-rules.md":     {"originally at schema v6. It completed in approved implementation commits", "exact singleton ledger identity `{7}`"},
		"docs/wiki/Security-Model.md":      {"schema-v7 SQLite rows", "exact current schema-v7", "former exact v6"},
		"docs/testing/alpha-validation.md": {"schema-v7 SQLite"},
	} {
		contents, err := os.ReadFile(filepath.Join(repositoryRoot, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, snippet := range required {
			if !strings.Contains(string(contents), snippet) {
				t.Errorf("current document %s does not describe amended private format with %q", name, snippet)
			}
		}
	}
}
