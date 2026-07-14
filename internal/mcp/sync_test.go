package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestIncrementalPushTool_AppliesTaskCreate(t *testing.T) {
	tasksStore := testTasksStore(t)
	tool := IncrementalPushTool(tasksStore, testKBStore(t), testEventsStore(t))

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
	tool := IncrementalPushTool(tasksStore, testKBStore(t), testEventsStore(t))
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

func TestIncrementalPushTool_RejectsNamespaceMismatch(t *testing.T) {
	tasksStore := testTasksStore(t)
	tool := IncrementalPushTool(tasksStore, testKBStore(t), testEventsStore(t))
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

	tool := BootstrapTool(tasksStore, kbStore, eventsStore)
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

	tool := IncrementalPullTool(tasksStore, kbStore, eventsStore)
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

	tool := ConflictReportTool(tasksStore, kbStore, eventsStore)
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
