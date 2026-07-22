package localstore

import (
	"context"
	"database/sql"
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

func TestWhoAmICache_SameAgentKeepsIndependentProjectScopes(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	for _, cached := range []WhoAmICache{
		{AgentID: "shared-agent", ProjectID: "project-a", Permissions: []string{"task.create"}, CachedAt: time.Now().UTC()},
		{AgentID: "shared-agent", ProjectID: "project-b", Permissions: []string{"kb.write"}, CachedAt: time.Now().UTC().Add(time.Second)},
	} {
		if err := store.CacheWhoAmI(ctx, cached); err != nil {
			t.Fatalf("CacheWhoAmI(%s): %v", cached.ProjectID, err)
		}
	}
	a, err := store.GetCachedWhoAmIForProject(ctx, "project-a")
	if err != nil || len(a.Permissions) != 1 || a.Permissions[0] != "task.create" {
		t.Fatalf("project A cache = %+v err=%v", a, err)
	}
	b, err := store.GetCachedWhoAmIForProject(ctx, "project-b")
	if err != nil || len(b.Permissions) != 1 || b.Permissions[0] != "kb.write" {
		t.Fatalf("project B cache = %+v err=%v", b, err)
	}
}

func TestOpen_MigratesLegacyAgentOnlyWhoAmICacheKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE whoami_cache (agent_id TEXT PRIMARY KEY, owner TEXT NOT NULL, model TEXT NOT NULL, capabilities TEXT NOT NULL DEFAULT '[]', project_id TEXT NOT NULL, permissions TEXT NOT NULL DEFAULT '[]', cached_at TIMESTAMP NOT NULL)`); err != nil {
		t.Fatalf("create legacy cache: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO whoami_cache VALUES ('shared-agent','','','[]','project-a','["task.create"]',CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("seed legacy cache: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open migrated db: %v", err)
	}
	defer store.Close()
	if err := store.CacheWhoAmI(context.Background(), WhoAmICache{AgentID: "shared-agent", ProjectID: "project-b", Permissions: []string{"kb.write"}, CachedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("cache second project: %v", err)
	}
	for _, projectID := range []string{"project-a", "project-b"} {
		if _, err := store.GetCachedWhoAmIForProject(context.Background(), projectID); err != nil {
			t.Fatalf("project %s missing after migration: %v", projectID, err)
		}
	}
}
