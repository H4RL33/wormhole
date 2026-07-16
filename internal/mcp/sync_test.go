package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestIncrementalPushTool_AppliesTaskCreate(t *testing.T) {
	tasksStore := testTasksStore(t)
	tool := IncrementalPushTool(tasksStore, testKBStore(t), testEventsStore(t), NewSyncRateLimiter(30, time.Minute))

	projectID := mustCreateProject(t, "mcp-sync-push-task")

	payload, _ := json.Marshal(syncTaskCreatePayload{
		Title:       "pushed task",
		Description: "d",
		Priority:    1,
	})
	in := IncrementalPushInput{
		NamespaceID: projectID,
		Version:     SyncProtocolVersion,
		Items: []struct {
			EntityType string          `json:"entity_type"`
			EntityID   string          `json:"entity_id"`
			Operation  string          `json:"operation"`
			Payload    json.RawMessage `json:"payload"`
		}{
			{EntityType: "task", EntityID: "client-generated-id", Operation: "create", Payload: payload},
		},
	}
	arguments := mustMarshal(t, in)

	result, err := tool.Handler(context.Background(), nil, projectID, arguments)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out, ok := result.(IncrementalPushOutput)
	if !ok {
		t.Fatalf("result type: got %T, want IncrementalPushOutput", result)
	}
	if out.ItemsReceived != 1 {
		t.Fatalf("ItemsReceived: got %d, want 1", out.ItemsReceived)
	}
	if len(out.Applied) != 1 || out.Applied[0].Error != "" {
		t.Fatalf("Applied: got %+v, want one item with no error", out.Applied)
	}
	if out.Applied[0].ID != "client-generated-id" || out.Applied[0].Type != "task" {
		t.Fatalf("Applied[0]: got %+v", out.Applied[0])
	}

	list, err := tasksStore.List(context.Background(), projectID, nil)
	if err != nil || len(list) != 1 || list[0].Title != "pushed task" || list[0].Description != "d" || list[0].Priority != 1 {
		t.Fatalf("push was not applied to server store: list=%+v err=%v", list, err)
	}
}

func TestIncrementalPushTool_PartialFailureDoesNotAbortBatch(t *testing.T) {
	tasksStore := testTasksStore(t)
	tool := IncrementalPushTool(tasksStore, testKBStore(t), testEventsStore(t), NewSyncRateLimiter(30, time.Minute))
	projectID := mustCreateProject(t, "mcp-sync-push-partial")

	goodPayload, _ := json.Marshal(syncTaskCreatePayload{Title: "good task", Description: "d", Priority: 1})
	in := IncrementalPushInput{
		NamespaceID: projectID,
		Version:     SyncProtocolVersion,
		Items: []struct {
			EntityType string          `json:"entity_type"`
			EntityID   string          `json:"entity_id"`
			Operation  string          `json:"operation"`
			Payload    json.RawMessage `json:"payload"`
		}{
			{EntityType: "widget", EntityID: "bad-item", Operation: "create", Payload: json.RawMessage(`{}`)},
			{EntityType: "task", EntityID: "good-item", Operation: "create", Payload: goodPayload},
		},
	}
	arguments := mustMarshal(t, in)

	result, err := tool.Handler(context.Background(), nil, projectID, arguments)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out := result.(IncrementalPushOutput)
	if out.ItemsReceived != 2 {
		t.Fatalf("ItemsReceived: got %d, want 2", out.ItemsReceived)
	}
	if len(out.Applied) != 2 {
		t.Fatalf("Applied: got %d entries, want 2", len(out.Applied))
	}
	if out.Applied[0].ID != "bad-item" || out.Applied[0].Error == "" {
		t.Fatalf("Applied[0] (bad item): got %+v, want a non-empty Error", out.Applied[0])
	}
	if out.Applied[1].ID != "good-item" || out.Applied[1].Error != "" {
		t.Fatalf("Applied[1] (good item): got %+v, want empty Error", out.Applied[1])
	}

	list, err := tasksStore.List(context.Background(), projectID, nil)
	if err != nil || len(list) != 1 || list[0].Title != "good task" {
		t.Fatalf("good item was not applied despite bad item in same batch: list=%+v err=%v", list, err)
	}
}

func TestIncrementalPushTool_RejectsNonCreateOperation(t *testing.T) {
	tasksStore := testTasksStore(t)
	tool := IncrementalPushTool(tasksStore, testKBStore(t), testEventsStore(t), NewSyncRateLimiter(30, time.Minute))
	projectID := mustCreateProject(t, "mcp-sync-push-non-create")

	goodPayload, _ := json.Marshal(syncTaskCreatePayload{Title: "good task", Description: "d", Priority: 1})
	in := IncrementalPushInput{
		NamespaceID: projectID,
		Version:     SyncProtocolVersion,
		Items: []struct {
			EntityType string          `json:"entity_type"`
			EntityID   string          `json:"entity_id"`
			Operation  string          `json:"operation"`
			Payload    json.RawMessage `json:"payload"`
		}{
			{EntityType: "task", EntityID: "update-item", Operation: "update", Payload: goodPayload},
			{EntityType: "kb", EntityID: "delete-item", Operation: "delete", Payload: json.RawMessage(`{}`)},
			{EntityType: "task", EntityID: "good-item", Operation: "create", Payload: goodPayload},
		},
	}
	arguments := mustMarshal(t, in)

	result, err := tool.Handler(context.Background(), nil, projectID, arguments)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out := result.(IncrementalPushOutput)
	if out.ItemsReceived != 3 {
		t.Fatalf("ItemsReceived: got %d, want 3", out.ItemsReceived)
	}
	if len(out.Applied) != 3 {
		t.Fatalf("Applied: got %d entries, want 3", len(out.Applied))
	}
	// First item: "update" operation should be rejected
	if out.Applied[0].ID != "update-item" || out.Applied[0].Error == "" {
		t.Fatalf("Applied[0] (update item): got %+v, want a non-empty Error", out.Applied[0])
	}
	if out.Applied[0].Error != `unsupported operation "update"` {
		t.Fatalf("Applied[0].Error: got %q, want %q", out.Applied[0].Error, `unsupported operation "update"`)
	}
	// Second item: "delete" operation should be rejected
	if out.Applied[1].ID != "delete-item" || out.Applied[1].Error == "" {
		t.Fatalf("Applied[1] (delete item): got %+v, want a non-empty Error", out.Applied[1])
	}
	if out.Applied[1].Error != `unsupported operation "delete"` {
		t.Fatalf("Applied[1].Error: got %q, want %q", out.Applied[1].Error, `unsupported operation "delete"`)
	}
	// Third item: "create" operation should succeed
	if out.Applied[2].ID != "good-item" || out.Applied[2].Error != "" {
		t.Fatalf("Applied[2] (good item): got %+v, want empty Error", out.Applied[2])
	}

	// Verify only the good item was applied to the store
	list, err := tasksStore.List(context.Background(), projectID, nil)
	if err != nil || len(list) != 1 || list[0].Title != "good task" {
		t.Fatalf("only the create operation should have been applied to store: list=%+v err=%v", list, err)
	}
}

func TestIncrementalPushTool_RejectsNamespaceMismatch(t *testing.T) {
	tasksStore := testTasksStore(t)
	tool := IncrementalPushTool(tasksStore, testKBStore(t), testEventsStore(t), NewSyncRateLimiter(30, time.Minute))
	projectID := mustCreateProject(t, "mcp-sync-push-ns-mismatch")
	otherProjectID := mustCreateProject(t, "mcp-sync-push-ns-mismatch-other")

	payload, _ := json.Marshal(syncTaskCreatePayload{Title: "x", Description: "y", Priority: 1})
	in := IncrementalPushInput{
		NamespaceID: otherProjectID, // client claims a different namespace than it authenticated as
		Version:     SyncProtocolVersion,
		Items: []struct {
			EntityType string          `json:"entity_type"`
			EntityID   string          `json:"entity_id"`
			Operation  string          `json:"operation"`
			Payload    json.RawMessage `json:"payload"`
		}{
			{EntityType: "task", EntityID: "id-1", Operation: "create", Payload: payload},
		},
	}
	arguments := mustMarshal(t, in)

	if _, err := tool.Handler(context.Background(), nil, projectID, arguments); err == nil {
		t.Fatalf("Handler: expected namespace mismatch error, got nil")
	}

	list, err := tasksStore.List(context.Background(), projectID, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("namespace-mismatched push should not have written anything: list=%+v", list)
	}
}

func TestBootstrapTool_ReturnsRealTaskAndKBLists(t *testing.T) {
	tasksStore := testTasksStore(t)
	kbStore := testKBStore(t)
	eventsStore := testEventsStore(t)
	projectID := mustCreateProject(t, "mcp-sync-bootstrap")
	agentID, _ := mustRegisterAgent(t, projectID)

	if _, err := tasksStore.Create(context.Background(), projectID, "bootstrap task", "desc", nil, 2, nil); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := kbStore.WriteArticle(context.Background(), projectID, agentID, "bootstrap article", "body text", nil, nil, false); err != nil {
		t.Fatalf("write article: %v", err)
	}

	tool := BootstrapTool(tasksStore, kbStore, eventsStore, NewSyncRateLimiter(30, time.Minute))
	arguments := mustMarshal(t, BootstrapInput{NamespaceID: projectID, Version: SyncProtocolVersion})

	result, err := tool.Handler(context.Background(), nil, projectID, arguments)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out := result.(BootstrapOutput)
	if len(out.TaskList) != 1 || out.TaskList[0].Title != "bootstrap task" {
		t.Fatalf("TaskList: got %+v, want one task titled %q", out.TaskList, "bootstrap task")
	}
	if len(out.KBList) != 1 || out.KBList[0].Title != "bootstrap article" {
		t.Fatalf("KBList: got %+v, want one article titled %q", out.KBList, "bootstrap article")
	}
	if out.Version != SyncProtocolVersion {
		t.Fatalf("Version: got %d, want %d", out.Version, SyncProtocolVersion)
	}
}

func TestIncrementalPullTool_FiltersByCursor(t *testing.T) {
	tasksStore := testTasksStore(t)
	kbStore := testKBStore(t)
	eventsStore := testEventsStore(t)
	projectID := mustCreateProject(t, "mcp-sync-pull-cursor")

	if _, err := tasksStore.Create(context.Background(), projectID, "old task", "before cursor", nil, 1, nil); err != nil {
		t.Fatalf("create old task: %v", err)
	}

	cursor := time.Now().UTC().Add(1 * time.Second)
	time.Sleep(1200 * time.Millisecond)

	newTask, err := tasksStore.Create(context.Background(), projectID, "new task", "after cursor", nil, 1, nil)
	if err != nil {
		t.Fatalf("create new task: %v", err)
	}

	tool := IncrementalPullTool(tasksStore, kbStore, eventsStore, NewSyncRateLimiter(30, time.Minute))
	lastSync := cursor.Format(time.RFC3339)
	arguments := mustMarshal(t, IncrementalPullInput{NamespaceID: projectID, Version: SyncProtocolVersion, LastSync: &lastSync})

	result, err := tool.Handler(context.Background(), nil, projectID, arguments)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out := result.(IncrementalPullOutput)

	var found []string
	for _, raw := range out.Updates {
		var envelope syncUpdateEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatalf("decode update envelope: %v", err)
		}
		if envelope.Type != "task" {
			continue
		}
		var task TaskSummary
		if err := json.Unmarshal(envelope.Data, &task); err != nil {
			t.Fatalf("decode task update: %v", err)
		}
		found = append(found, task.TaskID)
	}
	if len(found) != 1 || found[0] != newTask.ID {
		t.Fatalf("Updates task ids: got %v, want exactly [%s] (only tasks updated after cursor)", found, newTask.ID)
	}
}

func TestConflictReportTool_PublishesAuditEvent(t *testing.T) {
	tasksStore := testTasksStore(t)
	kbStore := testKBStore(t)
	eventsStore := testEventsStore(t)
	projectID := mustCreateProject(t, "mcp-sync-conflict")
	agentID, _ := mustRegisterAgent(t, projectID)
	scope := mustBuildScope(agentID, projectID)

	tool := ConflictReportTool(tasksStore, kbStore, eventsStore, NewSyncRateLimiter(30, time.Minute))
	arguments := mustMarshal(t, ConflictReportInput{
		NamespaceID:  projectID,
		Version:      SyncProtocolVersion,
		EntityType:   "task",
		EntityID:     "task-123",
		ConflictType: "update_conflict",
		ServerValue:  "server wins",
		LocalValue:   "local loses",
	})

	result, err := tool.Handler(context.Background(), scope, projectID, arguments)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out := result.(ConflictReportOutput)
	if out.ResolvedValue != "server wins" {
		t.Fatalf("ResolvedValue: got %q, want %q", out.ResolvedValue, "server wins")
	}
	if out.ResolutionMethod != "last_write_wins" {
		t.Fatalf("ResolutionMethod: got %q, want %q", out.ResolutionMethod, "last_write_wins")
	}

	channels, err := eventsStore.ListChannels(context.Background(), projectID)
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	var channelID string
	for _, c := range channels {
		if c.Name == SyncAuditChannelID {
			channelID = c.ID
		}
	}
	if channelID == "" {
		t.Fatalf("sync audit channel %q was not created in project", SyncAuditChannelID)
	}

	events, err := eventsStore.ListEvents(context.Background(), projectID, channelID, 10, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events on audit channel: got %d, want 1", len(events))
	}
	if events[0].EventType != "sync.conflict_resolved" {
		t.Fatalf("EventType: got %q, want %q", events[0].EventType, "sync.conflict_resolved")
	}
	var payload syncConflictAuditPayload
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatalf("decode audit payload: %v", err)
	}
	if payload.EntityID != "task-123" || payload.WinningValue != "server wins" || payload.LosingValue != "local loses" {
		t.Fatalf("audit payload: got %+v, want winning/losing values to match the reported conflict", payload)
	}
}

// P6 hardening: malformed-payload rejection. Each sync tool must return a
// clean error from json.Unmarshal on invalid JSON arguments, not panic.
// mustNotPanic wraps tool.Handler so a regression (a handler that panics on
// bad input instead of erroring) fails the test with a message rather than
// crashing the whole `go test` run.
func mustNotPanic(t *testing.T, call func() (any, error)) (result any, err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("handler panicked on malformed input instead of returning an error: %v", r)
		}
	}()
	return call()
}

func TestBootstrapTool_RejectsMalformedJSON(t *testing.T) {
	tool := BootstrapTool(testTasksStore(t), testKBStore(t), testEventsStore(t), NewSyncRateLimiter(30, time.Minute))
	projectID := mustCreateProject(t, "mcp-sync-bootstrap-malformed")

	_, err := mustNotPanic(t, func() (any, error) {
		return tool.Handler(context.Background(), nil, projectID, json.RawMessage(`{not valid json`))
	})
	if err == nil {
		t.Fatalf("Handler: expected error on malformed JSON, got nil")
	}
}

func TestIncrementalPullTool_RejectsMalformedJSON(t *testing.T) {
	tool := IncrementalPullTool(testTasksStore(t), testKBStore(t), testEventsStore(t), NewSyncRateLimiter(30, time.Minute))
	projectID := mustCreateProject(t, "mcp-sync-pull-malformed")

	_, err := mustNotPanic(t, func() (any, error) {
		return tool.Handler(context.Background(), nil, projectID, json.RawMessage(`{not valid json`))
	})
	if err == nil {
		t.Fatalf("Handler: expected error on malformed JSON, got nil")
	}
}

func TestIncrementalPushTool_RejectsMalformedJSON(t *testing.T) {
	tool := IncrementalPushTool(testTasksStore(t), testKBStore(t), testEventsStore(t), NewSyncRateLimiter(30, time.Minute))
	projectID := mustCreateProject(t, "mcp-sync-push-malformed")

	_, err := mustNotPanic(t, func() (any, error) {
		return tool.Handler(context.Background(), nil, projectID, json.RawMessage(`{not valid json`))
	})
	if err == nil {
		t.Fatalf("Handler: expected error on malformed JSON, got nil")
	}
}

func TestIncrementalPushTool_RejectsEmptyItems(t *testing.T) {
	tool := IncrementalPushTool(testTasksStore(t), testKBStore(t), testEventsStore(t), NewSyncRateLimiter(30, time.Minute))
	projectID := mustCreateProject(t, "mcp-sync-push-empty-items")

	arguments := mustMarshal(t, IncrementalPushInput{NamespaceID: projectID, Version: SyncProtocolVersion})
	_, err := mustNotPanic(t, func() (any, error) {
		return tool.Handler(context.Background(), nil, projectID, arguments)
	})
	if err == nil {
		t.Fatalf("Handler: expected error on empty items array, got nil")
	}
}

func TestConflictReportTool_RejectsMalformedJSON(t *testing.T) {
	tool := ConflictReportTool(testTasksStore(t), testKBStore(t), testEventsStore(t), NewSyncRateLimiter(30, time.Minute))
	projectID := mustCreateProject(t, "mcp-sync-conflict-malformed")

	_, err := mustNotPanic(t, func() (any, error) {
		return tool.Handler(context.Background(), nil, projectID, json.RawMessage(`{not valid json`))
	})
	if err == nil {
		t.Fatalf("Handler: expected error on malformed JSON, got nil")
	}
}

func TestConflictReportTool_RejectsMissingRequiredFields(t *testing.T) {
	tasksStore := testTasksStore(t)
	kbStore := testKBStore(t)
	eventsStore := testEventsStore(t)
	projectID := mustCreateProject(t, "mcp-sync-conflict-missing-fields")
	agentID, _ := mustRegisterAgent(t, projectID)
	scope := mustBuildScope(agentID, projectID)

	tool := ConflictReportTool(tasksStore, kbStore, eventsStore, NewSyncRateLimiter(30, time.Minute))
	// EntityType and EntityID both omitted.
	arguments := mustMarshal(t, ConflictReportInput{
		NamespaceID: projectID,
		Version:     SyncProtocolVersion,
	})

	_, err := mustNotPanic(t, func() (any, error) {
		return tool.Handler(context.Background(), scope, projectID, arguments)
	})
	if err == nil {
		t.Fatalf("Handler: expected error on missing entity_type/entity_id, got nil")
	}
}

// P6 hardening (RFC-0003 OQ5): protocol version check. Each sync tool must
// reject an unrecognized/incompatible version cleanly rather than silently
// proceeding.
func TestBootstrapTool_RejectsUnsupportedVersion(t *testing.T) {
	tool := BootstrapTool(testTasksStore(t), testKBStore(t), testEventsStore(t), NewSyncRateLimiter(30, time.Minute))
	projectID := mustCreateProject(t, "mcp-sync-bootstrap-version")

	arguments := mustMarshal(t, BootstrapInput{NamespaceID: projectID, Version: SyncProtocolVersion + 1})
	if _, err := tool.Handler(context.Background(), nil, projectID, arguments); err == nil {
		t.Fatalf("Handler: expected error on unsupported protocol version, got nil")
	}
}

func TestIncrementalPullTool_RejectsUnsupportedVersion(t *testing.T) {
	tool := IncrementalPullTool(testTasksStore(t), testKBStore(t), testEventsStore(t), NewSyncRateLimiter(30, time.Minute))
	projectID := mustCreateProject(t, "mcp-sync-pull-version")

	arguments := mustMarshal(t, IncrementalPullInput{NamespaceID: projectID, Version: SyncProtocolVersion + 1})
	if _, err := tool.Handler(context.Background(), nil, projectID, arguments); err == nil {
		t.Fatalf("Handler: expected error on unsupported protocol version, got nil")
	}
}

func TestIncrementalPushTool_RejectsUnsupportedVersion(t *testing.T) {
	tool := IncrementalPushTool(testTasksStore(t), testKBStore(t), testEventsStore(t), NewSyncRateLimiter(30, time.Minute))
	projectID := mustCreateProject(t, "mcp-sync-push-version")

	payload, _ := json.Marshal(syncTaskCreatePayload{Title: "x", Description: "y", Priority: 1})
	arguments := mustMarshal(t, IncrementalPushInput{
		NamespaceID: projectID,
		Version:     SyncProtocolVersion + 1,
		Items: []struct {
			EntityType string          `json:"entity_type"`
			EntityID   string          `json:"entity_id"`
			Operation  string          `json:"operation"`
			Payload    json.RawMessage `json:"payload"`
		}{
			{EntityType: "task", EntityID: "id-1", Operation: "create", Payload: payload},
		},
	})
	if _, err := tool.Handler(context.Background(), nil, projectID, arguments); err == nil {
		t.Fatalf("Handler: expected error on unsupported protocol version, got nil")
	}
}

func TestConflictReportTool_RejectsUnsupportedVersion(t *testing.T) {
	tasksStore := testTasksStore(t)
	kbStore := testKBStore(t)
	eventsStore := testEventsStore(t)
	projectID := mustCreateProject(t, "mcp-sync-conflict-version")
	agentID, _ := mustRegisterAgent(t, projectID)
	scope := mustBuildScope(agentID, projectID)

	tool := ConflictReportTool(tasksStore, kbStore, eventsStore, NewSyncRateLimiter(30, time.Minute))
	arguments := mustMarshal(t, ConflictReportInput{
		NamespaceID:  projectID,
		Version:      SyncProtocolVersion + 1,
		EntityType:   "task",
		EntityID:     "task-123",
		ConflictType: "update_conflict",
		ServerValue:  "server wins",
		LocalValue:   "local loses",
	})
	if _, err := tool.Handler(context.Background(), scope, projectID, arguments); err == nil {
		t.Fatalf("Handler: expected error on unsupported protocol version, got nil")
	}
}
