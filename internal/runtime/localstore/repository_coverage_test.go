package localstore

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openCoverageStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestRepositoriesCreateReadListAndDefaults(t *testing.T) {
	store := openCoverageStore(t)
	ctx := context.Background()
	er := NewEventRepo(store.DB())
	tr := NewTaskRepo(store.DB(), er)
	kb := NewKBRepo(store.DB())

	channelID, err := er.CreateChannel(ctx, "project", "general")
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	name, err := er.GetChannel(ctx, "project", channelID)
	if err != nil || name != "general" {
		t.Fatalf("GetChannel = %q, %v", name, err)
	}
	channels, err := er.ListChannels(ctx, "project")
	if err != nil || len(channels) != 1 || channels[0].ID != channelID {
		t.Fatalf("ListChannels = %#v, %v", channels, err)
	}

	event, err := er.PublishEvent(ctx, "project", channelID, "agent", "note", nil, nil)
	if err != nil {
		t.Fatalf("PublishEvent: %v", err)
	}
	if string(event.Payload) != "{}" || event.Note != nil {
		t.Fatalf("unexpected default event: %#v", event)
	}
	gotEvent, err := er.GetEvent(ctx, "project", event.ID)
	if err != nil || gotEvent.ID != event.ID {
		t.Fatalf("GetEvent = %#v, %v", gotEvent, err)
	}
	events, err := er.ListEvents(ctx, "project", channelID, 10, 0)
	if err != nil || len(events) != 1 || events[0].ID != event.ID {
		t.Fatalf("ListEvents = %#v, %v", events, err)
	}
	byNamespace, err := er.ListEventsByNamespace(ctx, "project", 10, 0)
	if err != nil || len(byNamespace) != 1 || byNamespace[0].ID != event.ID {
		t.Fatalf("ListEventsByNamespace = %#v, %v", byNamespace, err)
	}

	task, err := tr.CreateTask(ctx, "project", "title", "body", nil, 3, nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if task.Status != "todo" || task.Priority != 3 {
		t.Fatalf("unexpected created task: %#v", task)
	}
	gotTask, err := tr.GetTask(ctx, "project", task.ID)
	if err != nil || gotTask.ID != task.ID {
		t.Fatalf("GetTask = %#v, %v", gotTask, err)
	}
	tasks, err := tr.ListTasks(ctx, "project", nil)
	if err != nil || len(tasks) != 1 || tasks[0].ID != task.ID {
		t.Fatalf("ListTasks = %#v, %v", tasks, err)
	}
	status := "todo"
	tasks, err = tr.ListTasks(ctx, "project", &status)
	if err != nil || len(tasks) != 1 || tasks[0].ID != task.ID {
		t.Fatalf("ListTasks filtered = %#v, %v", tasks, err)
	}

	article, err := kb.WriteArticle(ctx, "project", "agent", "title", "body", nil)
	if err != nil {
		t.Fatalf("WriteArticle: %v", err)
	}
	if string(article.Frontmatter) != "{}" {
		t.Fatalf("default frontmatter = %s", article.Frontmatter)
	}
	gotArticle, err := kb.GetArticle(ctx, "project", article.ID)
	if err != nil || gotArticle.ID != article.ID {
		t.Fatalf("GetArticle = %#v, %v", gotArticle, err)
	}
	articles, err := kb.ListArticles(ctx, "project")
	if err != nil || len(articles) != 1 || articles[0].ID != article.ID {
		t.Fatalf("ListArticles = %#v, %v", articles, err)
	}
	links, err := kb.GetArticleLinks(ctx, "project", article.ID)
	if err != nil || links == nil || len(links) != 0 {
		t.Fatalf("GetArticleLinks = %#v, %v", links, err)
	}
}

func TestRepositoriesNotFoundAndCancelledContext(t *testing.T) {
	store := openCoverageStore(t)
	er := NewEventRepo(store.DB())
	tr := NewTaskRepo(store.DB(), er)
	kb := NewKBRepo(store.DB())
	ctx := context.Background()
	if _, err := er.GetChannel(ctx, "project", "missing"); !errors.Is(err, ErrEventNotFound) {
		t.Fatalf("GetChannel missing: %v", err)
	}
	if _, err := er.GetEvent(ctx, "project", "missing"); !errors.Is(err, ErrEventNotFound) {
		t.Fatalf("GetEvent missing: %v", err)
	}
	if _, err := er.ListEvents(ctx, "project", "missing", 10, 0); !errors.Is(err, ErrEventNotFound) {
		t.Fatalf("ListEvents missing channel: %v", err)
	}
	if _, err := tr.GetTask(ctx, "project", "missing"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("GetTask missing: %v", err)
	}
	if _, err := tr.Assign(ctx, "project", "missing", "agent"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("Assign missing: %v", err)
	}
	if _, err := kb.GetArticle(ctx, "project", "missing"); !errors.Is(err, ErrArticleNotFound) {
		t.Fatalf("GetArticle missing: %v", err)
	}
	if _, err := kb.GetArticleLinks(ctx, "project", "missing"); !errors.Is(err, ErrArticleNotFound) {
		t.Fatalf("GetArticleLinks missing: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := er.CreateChannel(cancelled, "project", "cancelled"); err == nil {
		t.Fatal("CreateChannel cancelled unexpectedly succeeded")
	}
	if _, err := tr.CreateTask(cancelled, "project", "cancelled", "", nil, 0, nil); err == nil {
		t.Fatal("CreateTask cancelled unexpectedly succeeded")
	}
	if _, err := kb.WriteArticle(cancelled, "project", "agent", "cancelled", "", nil); err == nil {
		t.Fatal("WriteArticle cancelled unexpectedly succeeded")
	}
}

func TestUpsertTaskAndArticleReplaceExistingRows(t *testing.T) {
	store := openCoverageStore(t)
	ctx := context.Background()
	tr := NewTaskRepo(store.DB(), NewEventRepo(store.DB()))
	kb := NewKBRepo(store.DB())
	due := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	parent, owner := "parent", "owner"
	task, err := tr.UpsertTask(ctx, "project-a", "task", "first", "body", &parent, &owner, "wip", 1, &due)
	if err != nil {
		t.Fatalf("first UpsertTask: %v", err)
	}
	if task.Status != "wip" || task.DueBy == nil || !task.DueBy.Equal(due) {
		t.Fatalf("first task = %#v", task)
	}
	task, err = tr.UpsertTask(ctx, "project-a", "task", "second", "changed", nil, nil, "done", 9, nil)
	if err != nil {
		t.Fatalf("second UpsertTask: %v", err)
	}
	if task.NamespaceID != "project-a" || task.Title != "second" || task.Status != "done" || task.ParentTaskID != nil || task.OwnerAgentID != nil || task.DueBy != nil {
		t.Fatalf("replaced task = %#v", task)
	}
	if _, err := tr.UpsertTask(ctx, "project", "bad", "", "", nil, nil, "unknown", 0, nil); err == nil {
		t.Fatal("UpsertTask accepted unknown status")
	}

	created := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	updated := created.Add(time.Hour)
	article, err := kb.UpsertArticle(ctx, "project-a", "article", "first", "body", json.RawMessage(`{"kind":"a"}`), "agent-a", created, updated)
	if err != nil {
		t.Fatalf("first UpsertArticle: %v", err)
	}
	if !article.CreatedAt.Equal(created) || !article.UpdatedAt.Equal(updated) {
		t.Fatalf("first article timestamps = %#v", article)
	}
	article, err = kb.UpsertArticle(ctx, "project-a", "article", "second", "changed", nil, "agent-b", updated, updated.Add(time.Hour))
	if err != nil {
		t.Fatalf("second UpsertArticle: %v", err)
	}
	if article.NamespaceID != "project-a" || article.Title != "second" || string(article.Frontmatter) != "{}" || article.AuthorAgentID != "agent-b" {
		t.Fatalf("replaced article = %#v", article)
	}
}

func TestCacheWhoAmIRejectsMalformedStoredJSONAndOpenRejectsDirectory(t *testing.T) {
	store := openCoverageStore(t)
	ctx := context.Background()
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO whoami_cache (agent_id, owner, model, capabilities, project_id, permissions, cached_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, "bad-caps", "owner", "model", "{", "project", "[]", time.Now()); err != nil {
		t.Fatalf("insert malformed capabilities: %v", err)
	}
	if _, err := store.GetCachedWhoAmI(ctx, "bad-caps"); err == nil {
		t.Fatal("GetCachedWhoAmI accepted malformed capabilities")
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO whoami_cache (agent_id, owner, model, capabilities, project_id, permissions, cached_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, "bad-perms", "owner", "model", "[]", "project", "{", time.Now()); err != nil {
		t.Fatalf("insert malformed permissions: %v", err)
	}
	if _, err := store.GetCachedWhoAmI(ctx, "bad-perms"); err == nil {
		t.Fatal("GetCachedWhoAmI accepted malformed permissions")
	}
	if _, err := Open(t.TempDir()); err == nil {
		t.Fatal("Open directory unexpectedly succeeded")
	}
}
