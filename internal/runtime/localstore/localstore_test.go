package localstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenSupportsRelativeSQLitePaths(t *testing.T) {
	t.Chdir(t.TempDir())
	store, err := Open("wormholed.db")
	if err != nil {
		t.Fatalf("Open(relative path): %v", err)
	}
	defer store.Close()

	if err := store.CacheWhoAmI(context.Background(), WhoAmICache{
		AgentID:   "relative-path-agent",
		ProjectID: "project",
		CachedAt:  time.Date(2026, 7, 23, 9, 8, 7, 6, time.UTC),
	}); err != nil {
		t.Fatalf("CacheWhoAmI: %v", err)
	}
}

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
		CachedAt:     time.Date(2026, 7, 23, 9, 8, 7, 123456789, time.FixedZone("west", -5*60*60)),
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

func TestEventRepoGetChannelRespectsNamespace(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	events := NewEventRepo(store.DB())
	channelID, err := events.CreateChannel(ctx, "project-a", "engineering")
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}

	name, err := events.GetChannel(ctx, "project-a", channelID)
	if err != nil {
		t.Fatalf("GetChannel(project-a): %v", err)
	}
	if name != "engineering" {
		t.Fatalf("GetChannel name = %q, want engineering", name)
	}

	for _, namespaceID := range []string{"project-b", "project-a"} {
		id := channelID
		if namespaceID == "project-a" {
			id = "missing"
		}
		if _, err := events.GetChannel(ctx, namespaceID, id); !errors.Is(err, ErrEventNotFound) {
			t.Fatalf("GetChannel(%q, %q) error = %v, want ErrEventNotFound", namespaceID, id, err)
		}
	}
}

func TestTaskRepoAssignPersistsOnlyInItsNamespace(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	events := NewEventRepo(store.DB())
	tasks := NewTaskRepo(store.DB(), events)
	task, err := tasks.CreateTask(ctx, "project-a", "route me", "", nil, 0, nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	assigned, err := tasks.Assign(ctx, "project-a", task.ID, "agent-a")
	if err != nil {
		t.Fatalf("Assign(project-a): %v", err)
	}
	if assigned.OwnerAgentID == nil || *assigned.OwnerAgentID != "agent-a" {
		t.Fatalf("Assign owner = %v, want agent-a", assigned.OwnerAgentID)
	}

	persisted, err := tasks.GetTask(ctx, "project-a", task.ID)
	if err != nil {
		t.Fatalf("GetTask(project-a): %v", err)
	}
	if persisted.OwnerAgentID == nil || *persisted.OwnerAgentID != "agent-a" {
		t.Fatalf("persisted owner = %v, want agent-a", persisted.OwnerAgentID)
	}

	if _, err := tasks.Assign(ctx, "project-b", task.ID, "agent-b"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("Assign(project-b) error = %v, want ErrTaskNotFound", err)
	}
	persisted, err = tasks.GetTask(ctx, "project-a", task.ID)
	if err != nil {
		t.Fatalf("GetTask after cross-namespace assign: %v", err)
	}
	if persisted.OwnerAgentID == nil || *persisted.OwnerAgentID != "agent-a" {
		t.Fatalf("cross-namespace Assign changed owner to %v", persisted.OwnerAgentID)
	}
}

func TestLocalRepositoriesPreserveDurableTaskEventAndKBState(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	events := NewEventRepo(store.DB())
	tasks := NewTaskRepo(store.DB(), events)
	kb := NewKBRepo(store.DB())
	channelID, err := events.CreateChannel(ctx, "project-a", "general")
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	task, err := tasks.CreateTask(ctx, "project-a", "build coverage", "exercise durable methods", nil, 2, nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	updated, err := tasks.UpdateStatus(ctx, "project-a", task.ID, "wip", channelID, "agent-a")
	if err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if updated.Status != "wip" {
		t.Fatalf("updated status = %q, want wip", updated.Status)
	}
	if _, err := tasks.UpdateStatus(ctx, "project-a", task.ID, "todo", channelID, "agent-a"); err == nil {
		t.Fatal("UpdateStatus accepted an illegal wip -> todo transition")
	}
	status := "wip"
	listedTasks, err := tasks.ListTasks(ctx, "project-a", &status)
	if err != nil || len(listedTasks) != 1 || listedTasks[0].ID != task.ID {
		t.Fatalf("ListTasks(wip) = %+v, err=%v", listedTasks, err)
	}

	event, err := events.PublishEvent(ctx, "project-a", channelID, "agent-a", "coverage.checked", json.RawMessage(`{"passed":true}`), nil)
	if err != nil {
		t.Fatalf("PublishEvent: %v", err)
	}
	gotEvent, err := events.GetEvent(ctx, "project-a", event.ID)
	if err != nil || gotEvent.EventType != "coverage.checked" {
		t.Fatalf("GetEvent = %+v, err=%v", gotEvent, err)
	}
	listedEvents, err := events.ListEvents(ctx, "project-a", channelID, 10, 0)
	if err != nil || len(listedEvents) != 2 {
		t.Fatalf("ListEvents = %+v, err=%v", listedEvents, err)
	}
	projectEvents, err := events.ListEventsByNamespace(ctx, "project-a", 10, 0)
	if err != nil || len(projectEvents) != 2 {
		t.Fatalf("ListEventsByNamespace = %+v, err=%v", projectEvents, err)
	}
	channels, err := events.ListChannels(ctx, "project-a")
	if err != nil || len(channels) != 1 || channels[0].ID != channelID {
		t.Fatalf("ListChannels = %+v, err=%v", channels, err)
	}

	first, err := kb.WriteArticle(ctx, "project-a", "agent-a", "first", "first body", json.RawMessage(`{"kind":"note"}`))
	if err != nil {
		t.Fatalf("WriteArticle(first): %v", err)
	}
	second, err := kb.WriteArticle(ctx, "project-a", "agent-a", "second", "second body", nil)
	if err != nil {
		t.Fatalf("WriteArticle(second): %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO kb_links (id, namespace_id, from_article_id, to_article_id) VALUES (?, ?, ?, ?)`, "link-1", "project-a", first.ID, second.ID); err != nil {
		t.Fatalf("insert link fixture: %v", err)
	}
	gotArticle, err := kb.GetArticle(ctx, "project-a", first.ID)
	if err != nil || gotArticle.Title != "first" {
		t.Fatalf("GetArticle = %+v, err=%v", gotArticle, err)
	}
	articles, err := kb.ListArticles(ctx, "project-a")
	if err != nil || len(articles) != 2 {
		t.Fatalf("ListArticles = %+v, err=%v", articles, err)
	}
	links, err := kb.GetArticleLinks(ctx, "project-a", first.ID)
	if err != nil || len(links) != 1 || links[0].ToArticleID != second.ID {
		t.Fatalf("GetArticleLinks = %+v, err=%v", links, err)
	}
}
