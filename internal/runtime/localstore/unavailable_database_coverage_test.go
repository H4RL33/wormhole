package localstore

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestLocalReplicaAndEnrolmentOperationsFailClosedWhenDatabaseIsUnavailable(t *testing.T) {
	store := openBootstrapCoverageStore(t)
	db := store.DB()
	events := NewEventRepo(db)
	tasks := NewTaskRepo(db, events)
	articles := NewKBRepo(db)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	record := EnrolmentAttemptRecord{ProjectID: "project", IdempotencyKey: "key", RequestHash: "hash", State: "requested", CredentialProfile: "profile"}
	checks := []struct {
		name string
		call func() error
	}{
		{"resolve enrolment", func() error { _, _, err := store.ResolveEnrolmentAttempt(ctx, record); return err }},
		{"update enrolment", func() error { return store.UpdateEnrolmentAttempt(ctx, record, "failed", "agent", "passport", true) }},
		{"cache identity", func() error {
			return store.CacheWhoAmI(ctx, WhoAmICache{AgentID: "agent", ProjectID: "project", CachedAt: now})
		}},
		{"get identity", func() error { _, err := store.GetCachedWhoAmI(ctx, "agent"); return err }},
		{"get project identity", func() error { _, err := store.GetCachedWhoAmIForProject(ctx, "project"); return err }},
		{"get scoped identity", func() error { _, err := store.GetCachedWhoAmIForAgentProject(ctx, "agent", "project"); return err }},
		{"create channel", func() error { _, err := events.CreateChannel(ctx, "project", "general"); return err }},
		{"get channel", func() error { _, err := events.GetChannel(ctx, "project", "channel"); return err }},
		{"list channels", func() error { _, err := events.ListChannels(ctx, "project"); return err }},
		{"publish event", func() error {
			_, err := events.PublishEvent(ctx, "project", "channel", "agent", "message", json.RawMessage(`{}`), nil)
			return err
		}},
		{"get event", func() error { _, err := events.GetEvent(ctx, "project", "event"); return err }},
		{"list events", func() error { _, err := events.ListEvents(ctx, "project", "channel", 10, 0); return err }},
		{"list project events", func() error { _, err := events.ListEventsByNamespace(ctx, "project", 10, 0); return err }},
		{"create task", func() error {
			_, err := tasks.CreateTask(ctx, "project", "title", "description", nil, 1, nil)
			return err
		}},
		{"get task", func() error { _, err := tasks.GetTask(ctx, "project", "task"); return err }},
		{"list tasks", func() error { _, err := tasks.ListTasks(ctx, "project", nil); return err }},
		{"update task", func() error {
			_, err := tasks.UpdateStatus(ctx, "project", "task", "done", "channel", "agent")
			return err
		}},
		{"assign task", func() error { _, err := tasks.Assign(ctx, "project", "task", "agent"); return err }},
		{"upsert task", func() error {
			_, err := tasks.UpsertTask(ctx, "project", "task", "title", "description", nil, nil, "todo", 1, nil)
			return err
		}},
		{"upsert server task", func() error {
			_, err := tasks.UpsertTaskFromServer(ctx, "project", "task", "title", "description", nil, nil, "todo", 1, nil, now, now)
			return err
		}},
		{"write article", func() error {
			_, err := articles.WriteArticle(ctx, "project", "agent", "title", "body", json.RawMessage(`{}`))
			return err
		}},
		{"get article", func() error { _, err := articles.GetArticle(ctx, "project", "article"); return err }},
		{"list articles", func() error { _, err := articles.ListArticles(ctx, "project"); return err }},
		{"get article links", func() error { _, err := articles.GetArticleLinks(ctx, "project", "article"); return err }},
		{"upsert article", func() error {
			_, err := articles.UpsertArticle(ctx, "project", "article", "title", "body", json.RawMessage(`{}`), "agent", now, now)
			return err
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); err == nil {
				t.Fatal("operation succeeded with closed database")
			}
		})
	}
}
