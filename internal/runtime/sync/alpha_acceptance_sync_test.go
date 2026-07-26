package sync

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
)

func TestAlphaAcceptancePullAppliesEventAndGitStableIDsExactlyOnce(t *testing.T) {
	ctx := context.Background()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "gateway-b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events := localstore.NewEventRepo(store.DB())
	tasks := localstore.NewTaskRepo(store.DB(), events)
	kb := localstore.NewKBRepo(store.DB())
	gitLinks := localstore.NewGitRepo(store.DB())
	engine, err := New("http://fabric.invalid", "token", "ns-1", NewQueueRepo(store.DB()), NewAuditRepo(store.DB()), tasks, kb, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	engine.ConfigureEventAndGitReplicas(events, gitLinks)
	now := time.Now().UTC().Truncate(time.Microsecond)
	updates := []map[string]any{
		{"type": "task", "data": map[string]any{"task_id": "task-1", "title": "handoff", "description": "review intent", "status": "wip", "priority": 1, "created_at": now, "updated_at": now}},
		{"type": "channel", "data": map[string]any{"id": "channel-1", "project_id": "ns-1", "name": "acceptance", "created_at": now}},
		{"type": "event", "data": map[string]any{"id": "event-1", "project_id": "ns-1", "channel_id": "channel-1", "agent_id": "agent-a", "event_type": "discovery.logged", "payload": map[string]any{"finding": "stable"}, "note": "handoff", "created_at": now}},
		{"type": "git_link", "data": map[string]any{"git_link_id": "git-1", "project_id": "ns-1", "task_id": "task-1", "repo": "H4RL33/wormhole", "commit_sha": "abc123", "summary": "handoff", "agent_id": "agent-a", "created_at": now}},
	}
	engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
		return map[string]any{"updates": updates, "timestamp": now.Add(time.Second).Format(time.RFC3339), "version": 1}, nil
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if err := engine.PullIncremental(ctx); err != nil {
			t.Fatalf("pull attempt %d: %v", attempt, err)
		}
		if attempt == 1 {
			// Fabric assigns authoritative create timestamps after accepting a
			// local-first stable ID. A later pull of identical semantic content
			// must reconcile that timestamp without treating it as equivocation.
			for _, index := range []int{1, 2, 3} {
				updates[index]["data"].(map[string]any)["created_at"] = now.Add(2 * time.Second)
			}
		}
		engine.lastSyncCursor = "" // force exact server replay to prove stable local application
	}
	if got, err := events.GetChannel(ctx, "ns-1", "channel-1"); err != nil || got != "acceptance" {
		t.Fatalf("stable channel = %q, err %v", got, err)
	}
	gotEvent, err := events.GetEvent(ctx, "ns-1", "event-1")
	if err != nil || gotEvent.EventType != "discovery.logged" || gotEvent.AgentID != "agent-a" || string(gotEvent.Payload) != `{"finding":"stable"}` {
		t.Fatalf("stable event = %+v, err %v", gotEvent, err)
	}
	for table, idColumn := range map[string]string{"events": "id", "git_links": "id"} {
		var count int
		if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE `+idColumn+` = ?`, map[string]string{"events": "event-1", "git_links": "git-1"}[table]).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s stable replay count = %d, err %v", table, count, err)
		}
	}
}

func TestAlphaAcceptanceUnknownOrMalformedPullNeverAdvancesCursor(t *testing.T) {
	for _, tc := range []struct {
		name   string
		update map[string]any
		want   string
	}{
		{"unknown", map[string]any{"type": "widget", "data": map[string]any{}}, "unknown update type"},
		{"malformed event", map[string]any{"type": "event", "data": map[string]any{"id": 42}}, "decode event update"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := localstore.Open(filepath.Join(t.TempDir(), "gateway.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			events := localstore.NewEventRepo(store.DB())
			engine, err := New("http://fabric.invalid", "token", "ns-1", NewQueueRepo(store.DB()), NewAuditRepo(store.DB()), localstore.NewTaskRepo(store.DB(), events), localstore.NewKBRepo(store.DB()), DefaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			engine.ConfigureEventAndGitReplicas(events, localstore.NewGitRepo(store.DB()))
			engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
				return map[string]any{"updates": []map[string]any{tc.update}, "timestamp": "2026-07-26T12:00:00Z", "version": 1}, nil
			}
			err = engine.PullIncremental(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("PullIncremental error = %v, want %q", err, tc.want)
			}
			if engine.lastSyncCursor != "" {
				t.Fatalf("failed pull advanced cursor to %q", engine.lastSyncCursor)
			}
		})
	}
}

func mustRawJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
