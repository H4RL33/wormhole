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
	if _, err := os.Stat(filepath.Join(root, "private_schema_v7.sql")); !os.IsNotExist(err) {
		t.Fatalf("former private v7 production snapshot remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "private_schema_v8.sql")); err != nil {
		t.Fatalf("consolidated private v8 snapshot missing: %v", err)
	}
}

func TestCurrentPrivateFormatDocsDescribeV8HardCut(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
	for name, required := range map[string][]string{
		"README.md":                        {"schema-v8 format", "former exact v7"},
		"agents/README.md":                 {"originally initialized fresh Gateway state directly as schema v6", "approved ActivityV1 retention amendment advances"},
		"docs/compatibility.md":            {"schema-v8", "initialized atomically as v8", "former exact v7"},
		"docs/implementation-rules.md":     {"originally at schema v6. It completed in approved implementation commits", "exact singleton ledger identity `{8}`"},
		"docs/wiki/Security-Model.md":      {"schema-v8 SQLite rows", "exact current schema-v8", "former exact v7"},
		"docs/testing/alpha-validation.md": {"schema-v8 SQLite"},
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
