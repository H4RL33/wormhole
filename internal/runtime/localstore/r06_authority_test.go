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
}
