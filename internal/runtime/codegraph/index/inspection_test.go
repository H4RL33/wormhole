package index_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/H4RL33/wormhole/internal/runtime/codegraph/index"
)

func TestInspectCheckoutCountsUniqueTrackedGoPathsWithoutMutation(t *testing.T) {
	repository := newInventoryRepository(t)
	for path, content := range map[string]string{
		"conflict.go":           "package fixture\nconst Value = 0\n",
		"nested/staged.go":      "package nested\nconst Staged = 0\n",
		"nested/unstaged.go":    "package nested\nconst Unstaged = 0\n",
		"nested/deleted.go":     "package nested\nconst Deleted = 0\n",
		"nested/line\nbreak.go": "package nested\nconst Newline = 0\n",
	} {
		writeInventoryFile(t, repository, path, content)
	}
	runGit(t, repository, "add", ".")
	runGit(t, repository, "commit", "-m", "base")

	runGit(t, repository, "checkout", "-b", "conflicting-side")
	writeInventoryFile(t, repository, "conflict.go", "package fixture\nconst Value = 1\n")
	runGit(t, repository, "commit", "-am", "side")
	runGit(t, repository, "checkout", "master")
	writeInventoryFile(t, repository, "conflict.go", "package fixture\nconst Value = 2\n")
	runGit(t, repository, "commit", "-am", "main")
	if output := runGitAllowFailure(t, repository, "merge", "conflicting-side"); output == "" {
		t.Fatal("merge unexpectedly returned no conflict output")
	}

	writeInventoryFile(t, repository, "nested/staged.go", "package nested\nconst Staged = 1\n")
	runGit(t, repository, "add", "nested/staged.go")
	writeInventoryFile(t, repository, "nested/unstaged.go", "package nested\nconst Unstaged = 1\n")
	if err := os.Remove(filepath.Join(repository, "nested", "deleted.go")); err != nil {
		t.Fatal(err)
	}
	writeInventoryFile(t, repository, "untracked.go", "package fixture\nconst Untracked = true\n")

	beforeHEAD := runGit(t, repository, "rev-parse", "HEAD")
	beforeStatus := runGit(t, repository, "status", "--porcelain=v1", "-z")
	inspection, err := index.InspectCheckout(context.Background(), repository)
	if err != nil {
		t.Fatalf("InspectCheckout() error = %v", err)
	}
	if inspection.Commit != beforeHEAD {
		t.Fatalf("commit = %q, want HEAD %q", inspection.Commit, beforeHEAD)
	}
	if inspection.TrackedGoFileCount != 5 {
		t.Fatalf("tracked Go count = %d, want 5 unique paths", inspection.TrackedGoFileCount)
	}
	if inspection.DirtyTrackedFileCount != 4 {
		t.Fatalf("dirty tracked Go count = %d, want conflict + staged + unstaged + deleted", inspection.DirtyTrackedFileCount)
	}
	if after := runGit(t, repository, "status", "--porcelain=v1", "-z"); after != beforeStatus {
		t.Fatalf("InspectCheckout mutated Git state\nbefore=%q\nafter=%q", beforeStatus, after)
	}
}

func TestInspectCheckoutIgnoresInheritedGitRedirection(t *testing.T) {
	repository := newInventoryRepository(t)
	writeInventoryFile(t, repository, "nested/tracked.go", "package nested\n")
	runGit(t, repository, "add", ".")
	runGit(t, repository, "commit", "-m", "tracked")

	outside := t.TempDir()
	t.Setenv("GIT_DIR", filepath.Join(outside, "malicious-git-dir"))
	t.Setenv("GIT_WORK_TREE", outside)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(outside, "malicious-index"))
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(outside, "malicious-objects"))
	inspection, err := index.InspectCheckout(context.Background(), repository)
	if err != nil {
		t.Fatalf("InspectCheckout() with inherited Git redirection: %v", err)
	}
	if inspection.TrackedGoFileCount != 1 || inspection.DirtyTrackedFileCount != 0 {
		t.Fatalf("inspection = %+v, want one clean tracked Go file", inspection)
	}
	for _, path := range []string{"malicious-index", "malicious-objects"} {
		if _, err := os.Lstat(filepath.Join(outside, path)); !os.IsNotExist(err) {
			t.Fatalf("inherited Git path %q was used: %v", path, err)
		}
	}
}

func runGitAllowFailure(t *testing.T, repository string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repository}, args...)...)
	output, _ := command.CombinedOutput()
	return string(output)
}
