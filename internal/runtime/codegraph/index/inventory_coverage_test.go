package index_test

import (
	"context"
	"errors"
	"testing"

	"github.com/H4RL33/wormhole/internal/runtime/codegraph/index"
)

func TestLoadGitInventoryRejectsInvalidAndAggregateLimits(t *testing.T) {
	repository := newInventoryRepository(t)
	writeInventoryFile(t, repository, "a.go", "package fixture\n")
	writeInventoryFile(t, repository, "b.go", "package fixture\n")
	runGit(t, repository, "add", "a.go", "b.go")
	runGit(t, repository, "commit", "-m", "aggregate limits")

	for _, limits := range []index.InventoryLimits{
		{MaxFiles: 0, MaxFileBytes: 1, MaxTotalBytes: 1},
		{MaxFiles: 1, MaxFileBytes: 0, MaxTotalBytes: 1},
		{MaxFiles: 1, MaxFileBytes: 1, MaxTotalBytes: 0},
	} {
		if _, err := index.LoadGitInventoryWithLimits(context.Background(), repository, testCanonicalRemote, limits); !errors.Is(err, index.ErrInventoryLimit) {
			t.Fatalf("invalid limits %#v error = %v, want ErrInventoryLimit", limits, err)
		}
	}

	limits := index.InventoryLimits{MaxFiles: 10, MaxFileBytes: 64, MaxTotalBytes: 20}
	if _, err := index.LoadGitInventoryWithLimits(context.Background(), repository, testCanonicalRemote, limits); !errors.Is(err, index.ErrInventoryLimit) {
		t.Fatalf("aggregate limit error = %v, want ErrInventoryLimit", err)
	}
}

func TestLoadGitInventoryRejectsMissingOriginAndUnbornRepository(t *testing.T) {
	repository := newInventoryRepository(t)
	runGit(t, repository, "remote", "remove", "origin")
	if _, err := index.LoadGitInventory(context.Background(), repository, testCanonicalRemote); err == nil {
		t.Fatal("missing origin unexpectedly accepted")
	}

	unborn := newInventoryRepository(t)
	if _, err := index.LoadGitInventory(context.Background(), unborn, testCanonicalRemote); err == nil {
		t.Fatal("unborn repository unexpectedly accepted")
	}
}
