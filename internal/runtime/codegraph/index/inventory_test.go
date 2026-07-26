package index_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/H4RL33/wormhole/internal/runtime/codegraph/index"
)

const testCanonicalRemote = "https://example.com/acme/codegraph-fixture.git"

func TestLoadGitInventoryUsesOnlyTrackedGoWorkingTreeBytes(t *testing.T) {
	repository := newInventoryRepository(t)
	writeInventoryFile(t, repository, ".gitignore", "ignored.go\n")
	writeInventoryFile(t, repository, "a.go", "package fixture\n\nconst A = 1\n")
	writeInventoryFile(t, repository, "nested/b.go", "package nested\n")
	writeInventoryFile(t, repository, "notes.txt", "tracked non-Go\n")
	runGit(t, repository, "add", ".gitignore", "a.go", "nested/b.go", "notes.txt")
	runGit(t, repository, "commit", "-m", "fixture")
	modified := []byte("package fixture\n\nconst A = 2\n")
	writeInventoryFile(t, repository, "a.go", string(modified))
	writeInventoryFile(t, repository, "untracked.go", "package fixture\n\nvar Untracked = true\n")
	writeInventoryFile(t, repository, "ignored.go", "package fixture\n\nvar Ignored = true\n")

	inventory, err := index.LoadGitInventory(context.Background(), repository, testCanonicalRemote)
	if err != nil {
		t.Fatalf("LoadGitInventory() error = %v", err)
	}
	if inventory.Root != repository || inventory.CanonicalRemote != testCanonicalRemote || inventory.Commit == "" {
		t.Fatalf("inventory identity = %#v", inventory)
	}
	paths := make([]string, 0, len(inventory.Files))
	for _, file := range inventory.Files {
		paths = append(paths, file.Path)
	}
	if !reflect.DeepEqual(paths, []string{"a.go", "nested/b.go"}) {
		t.Fatalf("tracked Go paths = %v", paths)
	}
	if !reflect.DeepEqual(inventory.Files[0].Bytes, modified) {
		t.Fatalf("a.go bytes = %q, want modified tracked bytes", inventory.Files[0].Bytes)
	}
	digest := sha256.Sum256(modified)
	if inventory.Files[0].SHA256 != "sha256:"+hex.EncodeToString(digest[:]) {
		t.Fatalf("a.go SHA256 = %q", inventory.Files[0].SHA256)
	}
}

func TestUntrackedFixtureExcludesUntrackedAndIgnoredGoFiles(t *testing.T) {
	repository := newInventoryRepository(t)
	fixtureRoot := filepath.Join("..", "..", "..", "..", "testdata", "codegraph", "untracked")
	for _, fixturePath := range []string{".gitignore", "go.mod", "tracked.go", "untracked.go"} {
		content, err := os.ReadFile(filepath.Join(fixtureRoot, fixturePath))
		if err != nil {
			t.Fatal(err)
		}
		writeInventoryFile(t, repository, fixturePath, string(content))
	}
	ignoredTemplate, err := os.ReadFile(filepath.Join(fixtureRoot, "ignored.go"))
	if err != nil {
		t.Fatal(err)
	}
	writeInventoryFile(t, repository, "runtime-ignored.go", string(ignoredTemplate))
	runGit(t, repository, "add", ".gitignore", "go.mod", "tracked.go")
	runGit(t, repository, "commit", "-m", "untracked fixture")
	if ignored := runGit(t, repository, "check-ignore", "runtime-ignored.go"); ignored != "runtime-ignored.go" {
		t.Fatalf("ignored fixture path = %q", ignored)
	}

	inventory, err := index.LoadGitInventory(context.Background(), repository, testCanonicalRemote)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Files) != 1 || inventory.Files[0].Path != "tracked.go" {
		t.Fatalf("fixture inventory = %#v, want tracked.go only", inventory.Files)
	}
}

func TestLoadGitInventoryRejectsRemoteAndNonRootCheckout(t *testing.T) {
	repository := newInventoryRepository(t)
	writeInventoryFile(t, repository, "nested/a.go", "package nested\n")
	runGit(t, repository, "add", "nested/a.go")
	runGit(t, repository, "commit", "-m", "fixture")

	if _, err := index.LoadGitInventory(context.Background(), repository, "https://example.com/other.git"); !errors.Is(err, index.ErrRemoteMismatch) {
		t.Fatalf("remote mismatch error = %v, want ErrRemoteMismatch", err)
	}
	if _, err := index.LoadGitInventory(context.Background(), filepath.Join(repository, "nested"), testCanonicalRemote); !errors.Is(err, index.ErrCheckoutRoot) {
		t.Fatalf("subdirectory checkout error = %v, want ErrCheckoutRoot", err)
	}
}

func TestLoadGitInventoryRejectsMissingTrackedFile(t *testing.T) {
	repository := newInventoryRepository(t)
	writeInventoryFile(t, repository, "missing.go", "package fixture\n")
	runGit(t, repository, "add", "missing.go")
	runGit(t, repository, "commit", "-m", "fixture")
	if err := os.Remove(filepath.Join(repository, "missing.go")); err != nil {
		t.Fatal(err)
	}

	if _, err := index.LoadGitInventory(context.Background(), repository, testCanonicalRemote); !errors.Is(err, index.ErrTrackedFileChanged) {
		t.Fatalf("missing tracked file error = %v, want ErrTrackedFileChanged", err)
	}
}

func TestLoadGitInventoryRejectsTrackedSymlink(t *testing.T) {
	repository := newInventoryRepository(t)
	writeInventoryFile(t, repository, "target.txt", "not Go\n")
	if err := os.Symlink("target.txt", filepath.Join(repository, "link.go")); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", "link.go")
	runGit(t, repository, "commit", "-m", "symlink")

	if _, err := index.LoadGitInventory(context.Background(), repository, testCanonicalRemote); !errors.Is(err, index.ErrUnsupportedTrackedFile) {
		t.Fatalf("tracked symlink error = %v, want ErrUnsupportedTrackedFile", err)
	}
}

func TestLoadGitInventoryRejectsConflictStages(t *testing.T) {
	repository := newInventoryRepository(t)
	writeInventoryFile(t, repository, "conflict.go", "package fixture\n\nconst Value = 1\n")
	runGit(t, repository, "add", "conflict.go")
	runGit(t, repository, "commit", "-m", "base")
	runGit(t, repository, "checkout", "-b", "other")
	writeInventoryFile(t, repository, "conflict.go", "package fixture\n\nconst Value = 2\n")
	runGit(t, repository, "commit", "-am", "other")
	runGit(t, repository, "checkout", "master")
	writeInventoryFile(t, repository, "conflict.go", "package fixture\n\nconst Value = 3\n")
	runGit(t, repository, "commit", "-am", "master")
	command := exec.Command("git", "-C", repository, "merge", "other")
	if err := command.Run(); err == nil {
		t.Fatal("git merge unexpectedly succeeded")
	}

	if _, err := index.LoadGitInventory(context.Background(), repository, testCanonicalRemote); !errors.Is(err, index.ErrUnsupportedTrackedFile) {
		t.Fatalf("conflict stage error = %v, want ErrUnsupportedTrackedFile", err)
	}
}

func TestLoadGitInventoryEnforcesFileAndByteLimits(t *testing.T) {
	repository := newInventoryRepository(t)
	writeInventoryFile(t, repository, "a.go", "package fixture\n")
	writeInventoryFile(t, repository, "b.go", "package fixture\n")
	runGit(t, repository, "add", "a.go", "b.go")
	runGit(t, repository, "commit", "-m", "limits")

	limits := index.InventoryLimits{MaxFiles: 1, MaxFileBytes: 4 << 20, MaxTotalBytes: 128 << 20}
	if _, err := index.LoadGitInventoryWithLimits(context.Background(), repository, testCanonicalRemote, limits); !errors.Is(err, index.ErrInventoryLimit) {
		t.Fatalf("file limit error = %v, want ErrInventoryLimit", err)
	}
	limits = index.InventoryLimits{MaxFiles: 10_000, MaxFileBytes: 4, MaxTotalBytes: 128 << 20}
	if _, err := index.LoadGitInventoryWithLimits(context.Background(), repository, testCanonicalRemote, limits); !errors.Is(err, index.ErrInventoryLimit) {
		t.Fatalf("per-file limit error = %v, want ErrInventoryLimit", err)
	}
}

func TestInventoryDeterministicForArgumentSafeCheckoutPath(t *testing.T) {
	parent := t.TempDir()
	repository := filepath.Join(parent, "repo --literal path")
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	initializeInventoryRepository(t, repository)
	writeInventoryFile(t, repository, "b.go", "package fixture\n")
	writeInventoryFile(t, repository, "a.go", "package fixture\n")
	runGit(t, repository, "add", "a.go", "b.go")
	runGit(t, repository, "commit", "-m", "deterministic")

	first, err := index.LoadGitInventory(context.Background(), repository, testCanonicalRemote)
	if err != nil {
		t.Fatal(err)
	}
	second, err := index.LoadGitInventory(context.Background(), repository, testCanonicalRemote)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("inventory is not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if !sort.SliceIsSorted(first.Files, func(i, j int) bool { return first.Files[i].Path < first.Files[j].Path }) {
		t.Fatalf("inventory files not sorted: %#v", first.Files)
	}
}

func TestLoadGitInventoryIgnoresInheritedGitControlEnvironment(t *testing.T) {
	repository := newInventoryRepository(t)
	writeInventoryFile(t, repository, "a.go", "package fixture\n")
	runGit(t, repository, "add", "a.go")
	runGit(t, repository, "commit", "-m", "fixture")
	other := newInventoryRepository(t)
	writeInventoryFile(t, other, "other.go", "package other\n")
	runGit(t, other, "add", "other.go")
	runGit(t, other, "commit", "-m", "other")

	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(other, ".git", "index"))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "remote.origin.url")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://example.com/injected.git")
	inventory, err := index.LoadGitInventory(context.Background(), repository, testCanonicalRemote)
	if err != nil {
		t.Fatalf("LoadGitInventory() error = %v", err)
	}
	if len(inventory.Files) != 1 || inventory.Files[0].Path != "a.go" {
		t.Fatalf("inherited Git environment redirected inventory: %#v", inventory.Files)
	}
}

func newInventoryRepository(t *testing.T) string {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	initializeInventoryRepository(t, repository)
	return repository
}

func initializeInventoryRepository(t *testing.T, repository string) {
	t.Helper()
	runGit(t, repository, "init", "--initial-branch=master")
	runGit(t, repository, "config", "user.email", "fixture@example.com")
	runGit(t, repository, "config", "user.name", "Fixture")
	runGit(t, repository, "remote", "add", "origin", testCanonicalRemote)
}

func writeInventoryFile(t *testing.T, repository, path, content string) {
	t.Helper()
	fullPath := filepath.Join(repository, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, repository string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", repository}, args...)
	command := exec.Command("git", commandArgs...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
