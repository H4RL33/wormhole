package localstore

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestEventRepoChannelAndEventContracts(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	repo := NewEventRepo(store.DB())
	alpha, err := repo.CreateChannel(ctx, "project-a", "alpha")
	if err != nil {
		t.Fatalf("CreateChannel(alpha): %v", err)
	}
	beta, err := repo.CreateChannel(ctx, "project-a", "beta")
	if err != nil {
		t.Fatalf("CreateChannel(beta): %v", err)
	}
	if _, err := repo.CreateChannel(ctx, "project-b", "private"); err != nil {
		t.Fatalf("CreateChannel(project-b): %v", err)
	}

	channels, err := repo.ListChannels(ctx, "project-a")
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if len(channels) != 2 {
		t.Fatalf("ListChannels count = %d, want 2", len(channels))
	}
	if got, want := []string{channels[0].Name, channels[1].Name}, []string{"alpha", "beta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ListChannels order = %v, want %v", got, want)
	}
	if _, err := repo.GetChannel(ctx, "project-b", alpha); !errors.Is(err, ErrEventNotFound) {
		t.Fatalf("GetChannel across project error = %v, want ErrEventNotFound", err)
	}

	first, err := repo.PublishEvent(ctx, "project-a", beta, "agent-a", "first", nil, nil)
	if err != nil {
		t.Fatalf("PublishEvent(empty payload): %v", err)
	}
	second, err := repo.PublishEvent(ctx, "project-a", beta, "agent-a", "second", json.RawMessage(`{"n":2}`), nil)
	if err != nil {
		t.Fatalf("PublishEvent(second): %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE events SET created_at = ? WHERE id = ?`, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), first.ID); err != nil {
		t.Fatalf("set first timestamp: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE events SET created_at = ? WHERE id = ?`, time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC), second.ID); err != nil {
		t.Fatalf("set second timestamp: %v", err)
	}

	listed, err := repo.ListEvents(ctx, "project-a", beta, 1, 0)
	if err != nil {
		t.Fatalf("ListEvents page one: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != second.ID {
		t.Fatalf("ListEvents page one = %+v, want second event", listed)
	}
	if got := string(listed[0].Payload); got != `{"n":2}` {
		t.Fatalf("second payload = %s, want JSON payload", got)
	}
	listed, err = repo.ListEvents(ctx, "project-a", beta, 1, 1)
	if err != nil {
		t.Fatalf("ListEvents page two: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != first.ID || string(listed[0].Payload) != "{}" {
		t.Fatalf("ListEvents page two = %+v, want default-payload first event", listed)
	}
	if _, err := repo.GetEvent(ctx, "project-a", "missing"); !errors.Is(err, ErrEventNotFound) {
		t.Fatalf("GetEvent(missing) error = %v, want ErrEventNotFound", err)
	}
	if _, err := repo.ListEvents(ctx, "project-a", "missing", 1, 0); !errors.Is(err, ErrEventNotFound) {
		t.Fatalf("ListEvents(missing channel) error = %v, want ErrEventNotFound", err)
	}
}

func TestTaskAndKBUpsertsRejectInvalidOrCrossProjectState(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	events := NewEventRepo(store.DB())
	tasks := NewTaskRepo(store.DB(), events)
	kb := NewKBRepo(store.DB())
	if _, err := tasks.UpsertTask(ctx, "project-a", "task-1", "task", "body", nil, nil, "invalid", 0, nil); err == nil {
		t.Fatal("UpsertTask accepted invalid status")
	}
	task, err := tasks.UpsertTask(ctx, "project-a", "task-1", "task", "body", nil, nil, "todo", 2, nil)
	if err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}
	assigned, err := tasks.Assign(ctx, "project-a", task.ID, "agent-a")
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}
	if assigned.OwnerAgentID == nil || *assigned.OwnerAgentID != "agent-a" {
		t.Fatalf("Assign owner = %v, want agent-a", assigned.OwnerAgentID)
	}
	if _, err := tasks.Assign(ctx, "project-b", task.ID, "agent-b"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("cross-project Assign error = %v, want ErrTaskNotFound", err)
	}

	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	article, err := kb.UpsertArticle(ctx, "project-a", "article-1", "title", "body", nil, "agent-a", created, created)
	if err != nil {
		t.Fatalf("UpsertArticle: %v", err)
	}
	if got := string(article.Frontmatter); got != "{}" {
		t.Fatalf("UpsertArticle default frontmatter = %s, want {}", got)
	}
	if _, err := kb.GetArticleLinks(ctx, "project-b", article.ID); !errors.Is(err, ErrArticleNotFound) {
		t.Fatalf("GetArticleLinks across project error = %v, want ErrArticleNotFound", err)
	}
	other, err := kb.WriteArticle(ctx, "project-a", "agent-a", "other", "body", nil)
	if err != nil {
		t.Fatalf("WriteArticle(other): %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO kb_links (id, namespace_id, from_article_id, to_article_id) VALUES (?, ?, ?, ?)`, "link-1", "project-a", article.ID, other.ID); err != nil {
		t.Fatalf("insert link: %v", err)
	}
	links, err := kb.GetArticleLinks(ctx, "project-a", article.ID)
	if err != nil {
		t.Fatalf("GetArticleLinks: %v", err)
	}
	if len(links) != 1 || links[0].ToArticleID != other.ID {
		t.Fatalf("GetArticleLinks = %+v, want link to %s", links, other.ID)
	}
}

func TestWhoAmICacheExactIdentityScope(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	when := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.FixedZone("west", -5*60*60))
	for _, identity := range []WhoAmICache{
		{AgentID: "agent-a", ProjectID: "project-a", Permissions: []string{"task.read"}, CachedAt: when},
		{AgentID: "agent-b", ProjectID: "project-a", Permissions: []string{"task.write"}, CachedAt: when.Add(time.Minute)},
	} {
		if err := store.CacheWhoAmI(ctx, identity); err != nil {
			t.Fatalf("CacheWhoAmI(%s): %v", identity.AgentID, err)
		}
	}
	got, err := store.GetCachedWhoAmIForAgentProject(ctx, "agent-a", "project-a")
	if err != nil {
		t.Fatalf("GetCachedWhoAmIForAgentProject: %v", err)
	}
	if got.AgentID != "agent-a" || !reflect.DeepEqual(got.Permissions, []string{"task.read"}) || got.CachedAt.Location() != time.UTC {
		t.Fatalf("exact cached identity = %+v, want agent-a's UTC cache", got)
	}
	if _, err := store.GetCachedWhoAmIForAgentProject(ctx, "agent-a", "project-b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetCachedWhoAmIForAgentProject(wrong project) error = %v, want ErrNotFound", err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE whoami_cache SET cached_at = ? WHERE agent_id = ?`, "2026-01-02 03:04:05.123456789 -0500 west", "agent-a"); err != nil {
		t.Fatalf("set legacy fixed-zone cached_at fixture: %v", err)
	}
	legacy, err := store.GetCachedWhoAmI(ctx, "agent-a")
	if err != nil || !legacy.CachedAt.Equal(when.UTC()) {
		t.Fatalf("GetCachedWhoAmI legacy fixed zone = %+v, err=%v", legacy, err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE whoami_cache SET cached_at = ? WHERE agent_id = ?`, "not-a-timestamp", "agent-a"); err != nil {
		t.Fatalf("corrupt cached_at fixture: %v", err)
	}
	if _, err := store.GetCachedWhoAmI(ctx, "agent-a"); err == nil {
		t.Fatal("GetCachedWhoAmI accepted malformed cached_at")
	}
}
