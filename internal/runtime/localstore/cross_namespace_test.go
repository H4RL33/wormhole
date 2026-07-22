package localstore

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestUpsertTask_CrossNamespaceIDCollisionRejected(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	tr := NewTaskRepo(store.db, NewEventRepo(store.db))
	const (
		nsA      = "namespace-a"
		nsB      = "namespace-b"
		taskID   = "shared-id"
		original = "task in namespace A"
		updated  = "updated task in namespace A"
	)

	if _, err := tr.UpsertTask(ctx, nsA, taskID, original, "original", nil, nil, "todo", 1, nil); err != nil {
		t.Fatalf("UpsertTask(nsA): %v", err)
	}

	_, err = tr.UpsertTask(ctx, nsB, taskID, "task in namespace B", "collision", nil, nil, "todo", 2, nil)
	if !errors.Is(err, ErrNamespaceCollision) {
		t.Fatalf("upsert collision error = %v, want ErrNamespaceCollision", err)
	}

	gotA, err := tr.GetTask(ctx, nsA, taskID)
	if err != nil {
		t.Fatalf("GetTask(nsA): %v", err)
	}
	if gotA.Title != original {
		t.Errorf("GetTask(nsA).Title = %q, want %q", gotA.Title, original)
	}
	if _, err := tr.GetTask(ctx, nsB, taskID); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("GetTask(nsB): got %v, want ErrTaskNotFound", err)
	}

	gotA, err = tr.UpsertTask(ctx, nsA, taskID, updated, "updated", nil, nil, "wip", 3, nil)
	if err != nil {
		t.Fatalf("UpsertTask same namespace: %v", err)
	}
	if gotA.Title != updated {
		t.Errorf("UpsertTask same namespace title = %q, want %q", gotA.Title, updated)
	}
}

func TestUpsertArticle_CrossNamespaceIDCollisionRejected(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	kb := NewKBRepo(store.db)
	const (
		nsA       = "namespace-a"
		nsB       = "namespace-b"
		articleID = "shared-id"
		original  = "article in namespace A"
		updated   = "updated article in namespace A"
	)
	now := time.Now().UTC().Truncate(time.Second)

	if _, err := kb.UpsertArticle(ctx, nsA, articleID, original, "original", json.RawMessage(`{"type":"decision"}`), "agent-a", now, now); err != nil {
		t.Fatalf("UpsertArticle(nsA): %v", err)
	}

	_, err = kb.UpsertArticle(ctx, nsB, articleID, "article in namespace B", "collision", json.RawMessage(`{"type":"policy"}`), "agent-b", now, now)
	if !errors.Is(err, ErrNamespaceCollision) {
		t.Fatalf("upsert collision error = %v, want ErrNamespaceCollision", err)
	}

	gotA, err := kb.GetArticle(ctx, nsA, articleID)
	if err != nil {
		t.Fatalf("GetArticle(nsA): %v", err)
	}
	if gotA.Title != original {
		t.Errorf("GetArticle(nsA).Title = %q, want %q", gotA.Title, original)
	}
	if _, err := kb.GetArticle(ctx, nsB, articleID); !errors.Is(err, ErrArticleNotFound) {
		t.Fatalf("GetArticle(nsB): got %v, want ErrArticleNotFound", err)
	}

	gotA, err = kb.UpsertArticle(ctx, nsA, articleID, updated, "updated", json.RawMessage(`{"type":"decision"}`), "agent-a", now, now)
	if err != nil {
		t.Fatalf("UpsertArticle same namespace: %v", err)
	}
	if gotA.Title != updated {
		t.Errorf("UpsertArticle same namespace title = %q, want %q", gotA.Title, updated)
	}
}

// TestTaskCrossNamespaceRejection verifies that tasks are isolated by namespace.
// RFC-0003 §7.2 — mandatory cross-namespace rejection test.
func TestTaskCrossNamespaceRejection(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	er := NewEventRepo(store.db)
	tr := NewTaskRepo(store.db, er)
	nsA := "namespace-a"
	nsB := "namespace-b"

	// Create task in namespace A.
	taskA, err := tr.CreateTask(ctx, nsA, "Task A", "desc", nil, 0, nil)
	if err != nil {
		t.Fatalf("CreateTask(nsA): %v", err)
	}

	// Verify task is visible in its own namespace.
	got, err := tr.GetTask(ctx, nsA, taskA.ID)
	if err != nil {
		t.Fatalf("GetTask(nsA): %v", err)
	}
	if got.ID != taskA.ID {
		t.Fatalf("GetTask(nsA) = %q, want %q", got.ID, taskA.ID)
	}

	// Verify task is NOT visible in namespace B.
	_, err = tr.GetTask(ctx, nsB, taskA.ID)
	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("GetTask(nsB): got %v, want ErrTaskNotFound", err)
	}

	// ListTasks in namespace B should not include task A.
	tasksB, err := tr.ListTasks(ctx, nsB, nil)
	if err != nil {
		t.Fatalf("ListTasks(nsB): %v", err)
	}
	for _, tsk := range tasksB {
		if tsk.ID == taskA.ID {
			t.Errorf("ListTasks(nsB) returned task from namespace A: %s", tsk.ID)
		}
	}

	// Verify ListTasks in namespace A includes task A.
	tasksA, err := tr.ListTasks(ctx, nsA, nil)
	if err != nil {
		t.Fatalf("ListTasks(nsA): %v", err)
	}
	found := false
	for _, tsk := range tasksA {
		if tsk.ID == taskA.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ListTasks(nsA) did not include task %s", taskA.ID)
	}

	// UpdateStatus should fail for a task in the wrong namespace.
	_, err = tr.UpdateStatus(ctx, nsB, taskA.ID, "wip", "channel-1", "agent-1")
	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("UpdateStatus(nsB): got %v, want ErrTaskNotFound", err)
	}
}

// TestEventCrossNamespaceRejection verifies that events are isolated by namespace.
func TestEventCrossNamespaceRejection(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	er := NewEventRepo(store.db)
	nsA := "namespace-a"
	nsB := "namespace-b"

	// Create channels in both namespaces.
	chA, err := er.CreateChannel(ctx, nsA, "ch-a")
	if err != nil {
		t.Fatalf("CreateChannel(nsA): %v", err)
	}
	chB, err := er.CreateChannel(ctx, nsB, "ch-b")
	if err != nil {
		t.Fatalf("CreateChannel(nsB): %v", err)
	}

	// Publish event in namespace A.
	payload := json.RawMessage(`{"test":true}`)
	eventA, err := er.PublishEvent(ctx, nsA, chA, "agent-1", "task.status_changed", payload, nil)
	if err != nil {
		t.Fatalf("PublishEvent(nsA): %v", err)
	}

	// Verify event is visible in its own namespace.
	got, err := er.GetEvent(ctx, nsA, eventA.ID)
	if err != nil {
		t.Fatalf("GetEvent(nsA): %v", err)
	}
	if got.ID != eventA.ID {
		t.Fatalf("GetEvent(nsA) = %q, want %q", got.ID, eventA.ID)
	}

	// GetEvent in namespace B should return ErrEventNotFound.
	_, err = er.GetEvent(ctx, nsB, eventA.ID)
	if !errors.Is(err, ErrEventNotFound) {
		t.Fatalf("GetEvent(nsB): got %v, want ErrEventNotFound", err)
	}

	// ListEvents in namespace B should not include events from A.
	// First publish an event in B to make sure we get zero (not "none because empty").
	_, err = er.PublishEvent(ctx, nsB, chB, "agent-2", "discovery.logged", json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("PublishEvent(nsB): %v", err)
	}
	eventsB, err := er.ListEvents(ctx, nsB, chB, 10, 0)
	if err != nil {
		t.Fatalf("ListEvents(nsB): %v", err)
	}
	for _, ev := range eventsB {
		if ev.ID == eventA.ID {
			t.Errorf("ListEvents(nsB) returned event from namespace A: %s", ev.ID)
		}
	}

	// ListEventsByNamespace for nsB should not include event from nsA.
	eventsByNS, err := er.ListEventsByNamespace(ctx, nsB, 10, 0)
	if err != nil {
		t.Fatalf("ListEventsByNamespace(nsB): %v", err)
	}
	for _, ev := range eventsByNS {
		if ev.ID == eventA.ID {
			t.Errorf("ListEventsByNamespace(nsB) returned event from namespace A: %s", ev.ID)
		}
	}
}

// TestKBCrossNamespaceRejection verifies that KB articles are isolated by namespace.
func TestKBCrossNamespaceRejection(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	kb := NewKBRepo(store.db)
	nsA := "namespace-a"
	nsB := "namespace-b"

	// Write article in namespace A.
	fm := json.RawMessage(`{"type":"decision"}`)
	articleA, err := kb.WriteArticle(ctx, nsA, "agent-1", "Decision A", "body A", fm)
	if err != nil {
		t.Fatalf("WriteArticle(nsA): %v", err)
	}

	// Verify article is visible in its own namespace.
	got, err := kb.GetArticle(ctx, nsA, articleA.ID)
	if err != nil {
		t.Fatalf("GetArticle(nsA): %v", err)
	}
	if got.ID != articleA.ID || got.Title != "Decision A" {
		t.Fatalf("GetArticle(nsA) = %+v, want %+v", got, articleA)
	}

	// GetArticle in namespace B should return ErrArticleNotFound.
	_, err = kb.GetArticle(ctx, nsB, articleA.ID)
	if !errors.Is(err, ErrArticleNotFound) {
		t.Fatalf("GetArticle(nsB): got %v, want ErrArticleNotFound", err)
	}

	// ListArticles in namespace B should not include articles from A.
	fm2 := json.RawMessage(`{"type":"policy"}`)
	articleB, err := kb.WriteArticle(ctx, nsB, "agent-2", "Decision B", "body B", fm2)
	if err != nil {
		t.Fatalf("WriteArticle(nsB): %v", err)
	}
	articlesB, err := kb.ListArticles(ctx, nsB)
	if err != nil {
		t.Fatalf("ListArticles(nsB): %v", err)
	}
	for _, art := range articlesB {
		if art.ID == articleA.ID {
			t.Errorf("ListArticles(nsB) returned article from namespace A: %s", art.ID)
		}
	}

	// GetArticleLinks should fail for an article in the wrong namespace.
	links, err := kb.GetArticleLinks(ctx, nsB, articleA.ID)
	if !errors.Is(err, ErrArticleNotFound) {
		t.Fatalf("GetArticleLinks(nsB): got %v, want ErrArticleNotFound", err)
	}
	// Links should be empty (not nil) when no links exist.
	if err == nil {
		if links == nil {
			t.Error("GetArticleLinks returned nil links, want empty slice")
		}
	}

	// GetArticleLinks for a real article in namespace B should work (links = empty slice).
	linksB, err := kb.GetArticleLinks(ctx, nsB, articleB.ID)
	if err != nil {
		t.Fatalf("GetArticleLinks(nsB for articleB): %v", err)
	}
	if linksB == nil {
		t.Error("GetArticleLinks returned nil links for real article, want empty slice")
	}
	if len(linksB) != 0 {
		t.Errorf("GetArticleLinks returned %d links, want 0", len(linksB))
	}
}

// TestTaskStatusTransitions verifies the status state machine.
func TestTaskStatusTransitions(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	er := NewEventRepo(store.db)
	tr := NewTaskRepo(store.db, er)
	ns := "ns-transitions"
	// Create a channel for event publishing.
	chID, err := er.CreateChannel(ctx, ns, "test-channel")
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}

	// todo -> wip
	task, err := tr.CreateTask(ctx, ns, "Transition task", "", nil, 0, nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	transitions := []struct {
		from string
		to   string
	}{
		{"todo", "wip"},
		{"wip", "blocked"},
		{"blocked", "wip"},
		{"wip", "done"},
	}

	for i, tt := range transitions {
		// Verify current status.
		current, err := tr.GetTask(ctx, ns, task.ID)
		if err != nil {
			t.Fatalf("GetTask before transition %d: %v", i, err)
		}
		wantStatus := "todo"
		if i > 0 {
			wantStatus = transitions[i-1].to
		}
		if current.Status != wantStatus {
			t.Fatalf("before transition %d: status = %q, want %q", i, current.Status, wantStatus)
		}

		task, err = tr.UpdateStatus(ctx, ns, task.ID, tt.to, chID, "agent-test")
		if err != nil {
			t.Fatalf("UpdateStatus(%s->%s): %v", tt.from, tt.to, err)
		}
		if task.Status != tt.to {
			t.Errorf("after transition %d: status = %q, want %q", i, task.Status, tt.to)
		}

		// Verify the update persisted.
		stored, err := tr.GetTask(ctx, ns, task.ID)
		if err != nil || stored.Status != tt.to {
			t.Fatalf("after transition %d: stored status = %q, want %q", i, stored.Status, tt.to)
		}
	}

	// done -> todo should fail (no legal transition out of done).
	_, err = tr.UpdateStatus(ctx, ns, task.ID, "todo", chID, "agent-test")
	if err == nil {
		t.Fatal("UpdateStatus(done->todo) should have failed, got nil")
	}

	// Invalid status should fail.
	_, err = tr.UpdateStatus(ctx, ns, task.ID, "invalid-status", chID, "agent-test")
	if err == nil {
		t.Fatal("UpdateStatus with invalid status should have failed")
	}
}

// TestTaskStatusChangeEmitsEvent verifies that UpdateStatus emits task.status_changed events.
func TestTaskStatusChangeEmitsEvent(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	er := NewEventRepo(store.db)
	tr := NewTaskRepo(store.db, er)
	ns := "ns-events"

	// Create channel and task.
	chID, err := er.CreateChannel(ctx, ns, "test-channel")
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}

	task, err := tr.CreateTask(ctx, ns, "Event test task", "", nil, 0, nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Update status; this should emit an event.
	_, err = tr.UpdateStatus(ctx, ns, task.ID, "wip", chID, "agent-test")
	if err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	// Verify event was written.
	events, err := er.ListEvents(ctx, ns, chID, 10, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	event := events[0]
	if event.EventType != "task.status_changed" {
		t.Errorf("event type = %q, want task.status_changed", event.EventType)
	}
	if event.AgentID != "agent-test" {
		t.Errorf("event agent_id = %q, want agent-test", event.AgentID)
	}

	// Verify payload contains expected fields.
	var payload struct {
		TaskID     string `json:"task_id"`
		FromStatus string `json:"from_status"`
		ToStatus   string `json:"to_status"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.TaskID != task.ID {
		t.Errorf("payload task_id = %q, want %q", payload.TaskID, task.ID)
	}
	if payload.FromStatus != "todo" {
		t.Errorf("payload from_status = %q, want todo", payload.FromStatus)
	}
	if payload.ToStatus != "wip" {
		t.Errorf("payload to_status = %q, want wip", payload.ToStatus)
	}
}

// TestTaskListStatusFilter verifies that ListTasks filters by status.
func TestTaskListStatusFilter(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	er := NewEventRepo(store.db)
	tr := NewTaskRepo(store.db, er)
	ns := "ns-filter"
	chID, _ := er.CreateChannel(ctx, ns, "test-channel")

	task1, _ := tr.CreateTask(ctx, ns, "Todo task", "", nil, 0, nil)
	task2, _ := tr.CreateTask(ctx, ns, "Wip task", "", nil, 0, nil)
	_, _ = tr.UpdateStatus(ctx, ns, task2.ID, "wip", chID, "agent-test")

	all, err := tr.ListTasks(ctx, ns, nil)
	if err != nil {
		t.Fatalf("ListTasks(nil): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListTasks(nil) = %d tasks, want 2", len(all))
	}

	wipStatus := "wip"
	wips, err := tr.ListTasks(ctx, ns, &wipStatus)
	if err != nil {
		t.Fatalf("ListTasks(wip): %v", err)
	}
	if len(wips) != 1 {
		t.Fatalf("ListTasks(wip) = %d tasks, want 1", len(wips))
	}
	if wips[0].ID != task2.ID {
		t.Errorf("ListTasks(wip)[0] = %q, want %q", wips[0].ID, task2.ID)
	}

	todoStatus := "todo"
	todos, err := tr.ListTasks(ctx, ns, &todoStatus)
	if err != nil {
		t.Fatalf("ListTasks(todo): %v", err)
	}
	if len(todos) != 1 {
		t.Fatalf("ListTasks(todo) = %d tasks, want 1", len(todos))
	}
	if todos[0].ID != task1.ID {
		t.Errorf("ListTasks(todo)[0] = %q, want %q", todos[0].ID, task1.ID)
	}
}

// TestDurableEventPublishAndList verifies the durable event publish/list path.
func TestDurableEventPublishAndList(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	er := NewEventRepo(store.db)
	ns := "ns-events"
	chID, _ := er.CreateChannel(ctx, ns, "test-channel")

	note := "test note"
	payload := json.RawMessage(`{"task_id":"t-1","from_status":"todo","to_status":"wip"}`)
	event, err := er.PublishEvent(ctx, ns, chID, "agent-1", "task.status_changed", payload, &note)
	if err != nil {
		t.Fatalf("PublishEvent: %v", err)
	}

	if event.EventType != "task.status_changed" {
		t.Errorf("event type = %q, want task.status_changed", event.EventType)
	}
	if event.AgentID != "agent-1" {
		t.Errorf("event agent_id = %q, want agent-1", event.AgentID)
	}

	events, err := er.ListEvents(ctx, ns, chID, 10, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("ListEvents = %d events, want 1", len(events))
	}
	if events[0].ID != event.ID {
		t.Errorf("event ID = %q, want %q", events[0].ID, event.ID)
	}

	// GetEvent by ID.
	got, err := er.GetEvent(ctx, ns, event.ID)
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got.ID != event.ID || got.EventType != event.EventType {
		t.Errorf("GetEvent = %+v, want %+v", got, event)
	}
}

// TestKBWriteAndList verifies the KB article write/list path.
func TestKBWriteAndList(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	kb := NewKBRepo(store.db)
	ns := "ns-kb"

	fm := json.RawMessage(`{"type":"decision"}`)
	article1, err := kb.WriteArticle(ctx, ns, "agent-1", "First article", "body one", fm)
	if err != nil {
		t.Fatalf("WriteArticle: %v", err)
	}

	fm2 := json.RawMessage(`{"type":"policy"}`)
	// Sleep briefly so SQLite's CURRENT_TIMESTAMP differs between the two inserts.
	time.Sleep(10 * time.Millisecond)
	article2, err := kb.WriteArticle(ctx, ns, "agent-2", "Second article", "body two", fm2)
	if err != nil {
		t.Fatalf("WriteArticle: %v", err)
	}

	// GetArticle.
	got, err := kb.GetArticle(ctx, ns, article1.ID)
	if err != nil {
		t.Fatalf("GetArticle: %v", err)
	}
	if got.Title != "First article" {
		t.Errorf("title = %q, want First article", got.Title)
	}

	// ListArticles returns both articles; verify presence (order is nondeterministic
	// when rows share the same second-level SQLite timestamp).
	articles, err := kb.ListArticles(ctx, ns)
	if err != nil {
		t.Fatalf("ListArticles: %v", err)
	}
	if len(articles) != 2 {
		t.Fatalf("ListArticles = %d articles, want 2", len(articles))
	}
	found1, found2 := false, false
	for _, a := range articles {
		if a.ID == article1.ID {
			found1 = true
		}
		if a.ID == article2.ID {
			found2 = true
		}
	}
	if !found1 || !found2 {
		t.Errorf("ListArticles missing articles: found1=%v, found2=%v", found1, found2)
	}

	// GetArticleLinks for article with no links returns empty slice.
	links, err := kb.GetArticleLinks(ctx, ns, article1.ID)
	if err != nil {
		t.Fatalf("GetArticleLinks: %v", err)
	}
	if links == nil {
		t.Error("GetArticleLinks returned nil, want empty slice")
	}
}

// TestWhoAmICache_Timezone verifies cached_at is stored and retrieved as UTC.
func TestWhoAmICache_UTC(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	want := WhoAmICache{
		AgentID:   "agent-utc",
		Owner:     "harley",
		Model:     "claude-sonnet-5",
		ProjectID: "project-utc",
		CachedAt:  time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC),
	}
	if err := store.CacheWhoAmI(ctx, want); err != nil {
		t.Fatalf("CacheWhoAmI: %v", err)
	}

	got, err := store.GetCachedWhoAmI(ctx, "agent-utc")
	if err != nil {
		t.Fatalf("GetCachedWhoAmI: %v", err)
	}
	if !got.CachedAt.Equal(want.CachedAt) {
		t.Errorf("cached_at = %v, want %v", got.CachedAt, want.CachedAt)
	}
}
