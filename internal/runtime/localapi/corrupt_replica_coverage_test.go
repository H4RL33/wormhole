package localapi

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	syncpkg "github.com/H4RL33/wormhole/internal/runtime/sync"
)

func TestLocalHandlersFailClosedWhenReplicaTablesAreMissing(t *testing.T) {
	ctx := context.Background()

	t.Run("task reads and writes", func(t *testing.T) {
		srv, store := corruptReplicaServer(t)
		if _, err := store.DB().ExecContext(ctx, `DROP TABLE tasks`); err != nil {
			t.Fatal(err)
		}
		calls := []func() error{
			func() error { _, err := srv.localListTasks(ctx, nil); return err },
			func() error { _, err := srv.localGetTask(ctx, json.RawMessage(`{"task_id":"task"}`)); return err },
			func() error { _, err := srv.handleTaskCreate(ctx, json.RawMessage(`{"title":"title"}`)); return err },
		}
		assertLocalHandlerCallsFail(t, calls)
	})

	t.Run("channel reads and writes", func(t *testing.T) {
		srv, store := corruptReplicaServer(t)
		if _, err := store.DB().ExecContext(ctx, `DROP TABLE channels`); err != nil {
			t.Fatal(err)
		}
		calls := []func() error{
			func() error { _, err := srv.localListChannels(ctx, nil); return err },
			func() error {
				_, err := srv.handleChannelCreate(ctx, json.RawMessage(`{"name":"general"}`))
				return err
			},
			func() error {
				_, err := srv.handleChannelPost(ctx, json.RawMessage(`{"channel_id":"channel","event_type":"message.posted","agent_id":"agent","note":"hello"}`))
				return err
			},
		}
		assertLocalHandlerCallsFail(t, calls)
	})

	t.Run("event reads", func(t *testing.T) {
		srv, store := corruptReplicaServer(t)
		if _, err := store.DB().ExecContext(ctx, `DROP TABLE events`); err != nil {
			t.Fatal(err)
		}
		if _, err := srv.localListChannelEvents(ctx, nil); err == nil {
			t.Fatal("event listing succeeded without events table")
		}
	})

	t.Run("article reads and writes", func(t *testing.T) {
		srv, store := corruptReplicaServer(t)
		if _, err := store.DB().ExecContext(ctx, `DROP TABLE kb_articles`); err != nil {
			t.Fatal(err)
		}
		calls := []func() error{
			func() error { _, err := srv.localListArticles(ctx, nil); return err },
			func() error {
				_, err := srv.localGetArticle(ctx, json.RawMessage(`{"article_id":"article"}`))
				return err
			},
			func() error {
				_, err := srv.handleKBWrite(ctx, json.RawMessage(`{"title":"title","body":"body","agent_id":"agent"}`))
				return err
			},
		}
		assertLocalHandlerCallsFail(t, calls)
	})
}

func TestServerConstructorsReportSocketBindingFailures(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing", "wormholed.sock")
	if _, err := New(missing, "", "", "project", nil, nil, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("New binding error = %v", err)
	}
	if _, err := NewWithRuntime(missing, "", "", "project", nil, nil, nil, nil, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("NewWithRuntime binding error = %v", err)
	}
	if _, err := NewMultiOrg(missing, map[string]config.Org{"org": {}}, nil, nil, nil, nil, nil, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("NewMultiOrg binding error = %v", err)
	}
}

func corruptReplicaServer(t *testing.T) (*Server, *localstore.Store) {
	t.Helper()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	er := localstore.NewEventRepo(store.DB())
	return &Server{
		projectID: "project", store: store, er: er,
		tr: localstore.NewTaskRepo(store.DB(), er), kb: localstore.NewKBRepo(store.DB()), qr: syncpkg.NewQueueRepo(store.DB()),
	}, store
}

func assertLocalHandlerCallsFail(t *testing.T, calls []func() error) {
	t.Helper()
	for index, call := range calls {
		if err := call(); err == nil {
			t.Errorf("corrupt replica handler %d unexpectedly succeeded", index)
		}
	}
}
