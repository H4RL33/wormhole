package localstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestCacheAndGetWhoAmI(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	want := WhoAmICache{
		AgentID:      "agent-1",
		Owner:        "harley",
		Model:        "claude-sonnet-5",
		Capabilities: []string{"code", "review"},
		ProjectID:    "project-1",
		Permissions:  []string{"read_kb", "create_task"},
		CachedAt:     time.Now().UTC().Truncate(time.Second),
	}

	if err := store.CacheWhoAmI(ctx, want); err != nil {
		t.Fatalf("CacheWhoAmI: %v", err)
	}

	got, err := store.GetCachedWhoAmI(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetCachedWhoAmI: %v", err)
	}
	if got.AgentID != want.AgentID || got.Owner != want.Owner || got.Model != want.Model ||
		got.ProjectID != want.ProjectID || !got.CachedAt.Equal(want.CachedAt) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if len(got.Capabilities) != 2 || got.Capabilities[0] != "code" || got.Capabilities[1] != "review" {
		t.Fatalf("capabilities mismatch: got %v", got.Capabilities)
	}
	if len(got.Permissions) != 2 || got.Permissions[0] != "read_kb" || got.Permissions[1] != "create_task" {
		t.Fatalf("permissions mismatch: got %v", got.Permissions)
	}
}

func TestGetCachedWhoAmI_NotFound(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	_, err = store.GetCachedWhoAmI(context.Background(), "no-such-agent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got err %v, want ErrNotFound", err)
	}
}

func TestCacheWhoAmI_Overwrite(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	first := WhoAmICache{AgentID: "agent-1", Owner: "harley", Model: "claude-sonnet-5", ProjectID: "project-1", CachedAt: time.Now().UTC().Truncate(time.Second)}
	if err := store.CacheWhoAmI(ctx, first); err != nil {
		t.Fatalf("CacheWhoAmI (first): %v", err)
	}
	second := first
	second.Model = "claude-opus-4-8"
	if err := store.CacheWhoAmI(ctx, second); err != nil {
		t.Fatalf("CacheWhoAmI (second): %v", err)
	}

	got, err := store.GetCachedWhoAmI(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetCachedWhoAmI: %v", err)
	}
	if got.Model != "claude-opus-4-8" {
		t.Fatalf("got model %q, want overwrite to take effect", got.Model)
	}
}
