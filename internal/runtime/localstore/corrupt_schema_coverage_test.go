package localstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestRepositoriesFailClosedWhenReplicaTablesAreMissing(t *testing.T) {
	ctx := context.Background()

	t.Run("channels", func(t *testing.T) {
		store := openCoverageStore(t)
		repo := NewEventRepo(store.DB())
		if _, err := store.DB().ExecContext(ctx, `DROP TABLE channels`); err != nil {
			t.Fatal(err)
		}
		calls := []func() error{
			func() error { _, err := repo.CreateChannel(ctx, "project", "general"); return err },
			func() error { _, err := repo.GetChannel(ctx, "project", "channel"); return err },
			func() error { _, err := repo.ListChannels(ctx, "project"); return err },
			func() error {
				_, err := repo.PublishEvent(ctx, "project", "channel", "agent", "message.posted", nil, nil)
				return err
			},
			func() error { _, err := repo.ListEvents(ctx, "project", "channel", 10, 0); return err },
		}
		assertReplicaCallsFail(t, calls)
	})

	t.Run("events", func(t *testing.T) {
		store := openCoverageStore(t)
		repo := NewEventRepo(store.DB())
		channelID, err := repo.CreateChannel(ctx, "project", "general")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.DB().ExecContext(ctx, `DROP TABLE events`); err != nil {
			t.Fatal(err)
		}
		calls := []func() error{
			func() error {
				_, err := repo.PublishEvent(ctx, "project", channelID, "agent", "message.posted", json.RawMessage(`{}`), nil)
				return err
			},
			func() error { _, err := repo.GetEvent(ctx, "project", "event"); return err },
			func() error { _, err := repo.ListEvents(ctx, "project", channelID, 10, 0); return err },
			func() error { _, err := repo.ListEventsByNamespace(ctx, "project", 10, 0); return err },
		}
		assertReplicaCallsFail(t, calls)
	})

	t.Run("tasks", func(t *testing.T) {
		store := openCoverageStore(t)
		eventsRepo := NewEventRepo(store.DB())
		repo := NewTaskRepo(store.DB(), eventsRepo)
		if _, err := store.DB().ExecContext(ctx, `DROP TABLE tasks`); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		calls := []func() error{
			func() error { _, err := repo.CreateTask(ctx, "project", "title", "", nil, 1, nil); return err },
			func() error { _, err := repo.GetTask(ctx, "project", "task"); return err },
			func() error { _, err := repo.ListTasks(ctx, "project", nil); return err },
			func() error {
				_, err := repo.UpdateStatus(ctx, "project", "task", "wip", "channel", "agent")
				return err
			},
			func() error { _, err := repo.Assign(ctx, "project", "task", "agent"); return err },
			func() error {
				_, err := repo.UpsertTask(ctx, "project", "task", "title", "", nil, nil, "todo", 1, nil)
				return err
			},
			func() error {
				_, err := repo.UpsertTaskFromServer(ctx, "project", "task", "title", "", nil, nil, "todo", 1, nil, now, now)
				return err
			},
		}
		assertReplicaCallsFail(t, calls)
	})

	t.Run("articles", func(t *testing.T) {
		store := openCoverageStore(t)
		repo := NewKBRepo(store.DB())
		if _, err := store.DB().ExecContext(ctx, `DROP TABLE kb_articles`); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		calls := []func() error{
			func() error { _, err := repo.WriteArticle(ctx, "project", "agent", "title", "body", nil); return err },
			func() error { _, err := repo.GetArticle(ctx, "project", "article"); return err },
			func() error { _, err := repo.ListArticles(ctx, "project"); return err },
			func() error { _, err := repo.GetArticleLinks(ctx, "project", "article"); return err },
			func() error {
				_, err := repo.UpsertArticle(ctx, "project", "article", "title", "body", nil, "agent", now, now)
				return err
			},
		}
		assertReplicaCallsFail(t, calls)
	})

	t.Run("article links", func(t *testing.T) {
		store := openCoverageStore(t)
		repo := NewKBRepo(store.DB())
		article, err := repo.WriteArticle(ctx, "project", "agent", "title", "body", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.DB().ExecContext(ctx, `DROP TABLE kb_links`); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.GetArticleLinks(ctx, "project", article.ID); err == nil {
			t.Fatal("GetArticleLinks succeeded without link table")
		}
	})
}

func assertReplicaCallsFail(t *testing.T, calls []func() error) {
	t.Helper()
	for index, call := range calls {
		if err := call(); err == nil {
			t.Errorf("corrupt replica call %d unexpectedly succeeded", index)
		}
	}
}

func openRawCoverageDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteDSN(filepath.Join(t.TempDir(), "raw.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestOpenRejectsCorruptGatewayMigrationLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt-gateway.db")
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE gateway_schema_migrations (version TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err := Open(path); err == nil {
		_ = store.Close()
		t.Fatal("Open succeeded with malformed gateway migration ledger")
	}
}
