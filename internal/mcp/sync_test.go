package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/core/tasks"
	"github.com/H4RL33/wormhole/internal/types"
)

func TestIncrementalPushTool_DeniesSameProjectItemWithoutActionPermission(t *testing.T) {
	tasksStore := testTasksStore(t)
	tool := IncrementalPushTool(tasksStore, testKBStore(t), testEventsStore(t), NewSyncRateLimiter(30, time.Minute))
	projectID := mustCreateProject(t, "mcp-sync-push-permission-denied")
	clientID := uuid.NewString()
	payload, _ := json.Marshal(syncTaskCreatePayload{Title: "must not land"})
	in := IncrementalPushInput{NamespaceID: projectID, Version: SyncProtocolVersion, Items: []struct {
		EntityType string          `json:"entity_type"`
		EntityID   string          `json:"entity_id"`
		Operation  string          `json:"operation"`
		Payload    json.RawMessage `json:"payload"`
	}{{EntityType: "task", EntityID: clientID, Operation: "create", Payload: payload}}}
	scope := &identity.AuthenticatedScope{ProjectID: projectID, Permissions: []string{"task.list"}}
	result, err := tool.Handler(context.Background(), scope, projectID, mustMarshal(t, in))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out := result.(IncrementalPushOutput)
	if len(out.Applied) != 1 || out.Applied[0].Error != "permission denied: requires task.create" {
		t.Fatalf("Applied = %+v, want same-project permission denial", out.Applied)
	}
	rows, err := tasksStore.List(context.Background(), projectID, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("permission-denied item was persisted: %+v", rows)
	}
}

func TestIncrementalPushTool_AppliesTaskCreate(t *testing.T) {
	tasksStore := testTasksStore(t)
	tool := IncrementalPushTool(tasksStore, testKBStore(t), testEventsStore(t), NewSyncRateLimiter(30, time.Minute))

	projectID := mustCreateProject(t, "mcp-sync-push-task")

	payload, _ := json.Marshal(syncTaskCreatePayload{
		Title:       "pushed task",
		Description: "d",
		Priority:    1,
	})
	// clientID is the client's own local-first task id (a real UUID, as a
	// local SQLite-backed store would generate — see architecture.md §1 and
	// RFC-0003 §7.2). incremental_push must preserve it: this is the id the
	// Gateway client will look the row up afterward.
	clientID := uuid.NewString()
	in := IncrementalPushInput{
		NamespaceID: projectID,
		Version:     SyncProtocolVersion,
		Items: []struct {
			EntityType string          `json:"entity_type"`
			EntityID   string          `json:"entity_id"`
			Operation  string          `json:"operation"`
			Payload    json.RawMessage `json:"payload"`
		}{
			{EntityType: "task", EntityID: clientID, Operation: "create", Payload: payload},
		},
	}
	arguments := mustMarshal(t, in)

	scope := &identity.AuthenticatedScope{ProjectID: projectID, Permissions: []string{"task.create"}}
	result, err := tool.Handler(context.Background(), scope, projectID, arguments)
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
	if out.Applied[0].ID != clientID || out.Applied[0].Type != "task" {
		t.Fatalf("Applied[0]: got %+v", out.Applied[0])
	}

	list, err := tasksStore.List(context.Background(), projectID, nil)
	if err != nil || len(list) != 1 || list[0].Title != "pushed task" || list[0].Description != "d" || list[0].Priority != 1 {
		t.Fatalf("push was not applied to server store: list=%+v err=%v", list, err)
	}
	// The row must be findable server-side by the client's own local id
	// (the bug this task fixes): Postgres must not have assigned a
	// different id than the one the client sent.
	if list[0].ID != clientID {
		t.Fatalf("server-side task id = %q, want client id %q (client entity id was not preserved)", list[0].ID, clientID)
	}
}

func TestIncrementalPushTool_IdenticalReplaySucceedsWithoutDuplicateEffects(t *testing.T) {
	tasksStore := testTasksStore(t)
	kbStore := testKBStore(t)
	eventsStore := testEventsStore(t)
	tool := IncrementalPushTool(tasksStore, kbStore, eventsStore, NewSyncRateLimiter(30, time.Minute))
	projectID := mustCreateProject(t, "mcp-sync-push-replay")
	agentID, _ := mustRegisterAgent(t, projectID)
	scope := &identity.AuthenticatedScope{
		Agent: identity.Agent{ID: agentID}, ProjectID: projectID,
		Permissions: []string{"task.create", "kb.write", "channel.create", "channel.post"},
	}
	taskID, articleID, channelID, eventID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	items := []struct {
		EntityType string
		EntityID   string
		Payload    json.RawMessage
	}{
		{"channel", channelID, mustMarshal(t, syncChannelCreatePayload{Name: "offline"})},
		{"task", taskID, mustMarshal(t, syncTaskCreatePayload{Title: "offline task", Description: "durable", Status: "todo", Priority: 1})},
		{"kb", articleID, mustMarshal(t, syncKBCreatePayload{Title: "offline fact", Body: "durable fact", Links: []string{}, Force: true})},
		{"event", eventID, mustMarshal(t, syncEventCreatePayload{ChannelID: channelID, EventType: "build.failed", Payload: json.RawMessage(`{"offline":true}`)})},
	}
	in := IncrementalPushInput{NamespaceID: projectID, Version: SyncProtocolVersion}
	for _, item := range items {
		in.Items = append(in.Items, struct {
			EntityType string          `json:"entity_type"`
			EntityID   string          `json:"entity_id"`
			Operation  string          `json:"operation"`
			Payload    json.RawMessage `json:"payload"`
		}{EntityType: item.EntityType, EntityID: item.EntityID, Operation: "create", Payload: item.Payload})
	}
	for attempt := 1; attempt <= 2; attempt++ {
		result, err := tool.Handler(context.Background(), scope, projectID, mustMarshal(t, in))
		if err != nil {
			t.Fatalf("attempt %d Handler: %v", attempt, err)
		}
		out := result.(IncrementalPushOutput)
		if len(out.Applied) != len(items) {
			t.Fatalf("attempt %d Applied = %+v", attempt, out.Applied)
		}
		for _, applied := range out.Applied {
			if applied.Error != "" {
				t.Fatalf("attempt %d replay item %+v", attempt, applied)
			}
		}
	}

	db := testDB(t)
	for table, id := range map[string]string{"tasks": taskID, "kb_articles": articleID, "channels": channelID, "events": eventID} {
		var count int
		if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM `+table+` WHERE id = $1 AND project_id = $2`, id, projectID).Scan(&count); err != nil {
			t.Fatalf("count %s replay effects: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("%s replay effect count = %d, want 1", table, count)
		}
	}
}

func TestIncrementalPushTool_AppliesRoutedTaskOwner(t *testing.T) {
	tasksStore := testTasksStore(t)
	tool := IncrementalPushTool(tasksStore, testKBStore(t), testEventsStore(t), NewSyncRateLimiter(30, time.Minute))
	projectID := mustCreateProject(t, "mcp-sync-push-routed-owner")
	ownerID, _ := mustRegisterAgent(t, projectID)
	clientID := uuid.NewString()
	payload, _ := json.Marshal(syncTaskCreatePayload{
		Title:        "routed task",
		Status:       "todo",
		OwnerAgentID: &ownerID,
	})
	in := IncrementalPushInput{NamespaceID: projectID, Version: SyncProtocolVersion, Items: []struct {
		EntityType string          `json:"entity_type"`
		EntityID   string          `json:"entity_id"`
		Operation  string          `json:"operation"`
		Payload    json.RawMessage `json:"payload"`
	}{{EntityType: "task", EntityID: clientID, Operation: "create", Payload: payload}}}
	scope := &identity.AuthenticatedScope{ProjectID: projectID, Permissions: []string{"task.create", "task.assign"}}

	result, err := tool.Handler(context.Background(), scope, projectID, mustMarshal(t, in))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out := result.(IncrementalPushOutput)
	if len(out.Applied) != 1 || out.Applied[0].Error != "" {
		t.Fatalf("Applied = %+v, want routed task success", out.Applied)
	}
	rows, err := tasksStore.List(context.Background(), projectID, nil)
	if err != nil || len(rows) != 1 {
		t.Fatalf("List = %+v, %v, want one task", rows, err)
	}
	if rows[0].ID != clientID || rows[0].OwnerAgentID == nil || *rows[0].OwnerAgentID != ownerID {
		t.Fatalf("stored routed task = %+v, want id=%s owner=%s", rows[0], clientID, ownerID)
	}
}

func TestIncrementalPushTool_RoutedOwnerRequiresTaskAssign(t *testing.T) {
	tasksStore := testTasksStore(t)
	tool := IncrementalPushTool(tasksStore, testKBStore(t), testEventsStore(t), NewSyncRateLimiter(30, time.Minute))
	projectID := mustCreateProject(t, "mcp-sync-push-routed-owner-permission")
	ownerID, _ := mustRegisterAgent(t, projectID)
	payload, _ := json.Marshal(syncTaskCreatePayload{Title: "denied route", Status: "todo", OwnerAgentID: &ownerID})
	in := IncrementalPushInput{NamespaceID: projectID, Version: SyncProtocolVersion, Items: []struct {
		EntityType string          `json:"entity_type"`
		EntityID   string          `json:"entity_id"`
		Operation  string          `json:"operation"`
		Payload    json.RawMessage `json:"payload"`
	}{{EntityType: "task", EntityID: uuid.NewString(), Operation: "create", Payload: payload}}}
	scope := &identity.AuthenticatedScope{ProjectID: projectID, Permissions: []string{"task.create"}}

	result, err := tool.Handler(context.Background(), scope, projectID, mustMarshal(t, in))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out := result.(IncrementalPushOutput)
	if len(out.Applied) != 1 || out.Applied[0].Error != "permission denied: requires task.assign" {
		t.Fatalf("Applied = %+v, want task.assign denial", out.Applied)
	}
	assertNoTasksForProject(t, tasksStore, projectID)
}

func TestIncrementalPushTool_RejectsCrossProjectRoutedOwner(t *testing.T) {
	tasksStore := testTasksStore(t)
	tool := IncrementalPushTool(tasksStore, testKBStore(t), testEventsStore(t), NewSyncRateLimiter(30, time.Minute))
	projectA := mustCreateProject(t, "mcp-sync-push-owner-project-a")
	projectB := mustCreateProject(t, "mcp-sync-push-owner-project-b")
	ownerID, _ := mustRegisterAgent(t, projectB)
	payload, _ := json.Marshal(syncTaskCreatePayload{Title: "cross-project route", Status: "todo", OwnerAgentID: &ownerID})
	in := IncrementalPushInput{NamespaceID: projectA, Version: SyncProtocolVersion, Items: []struct {
		EntityType string          `json:"entity_type"`
		EntityID   string          `json:"entity_id"`
		Operation  string          `json:"operation"`
		Payload    json.RawMessage `json:"payload"`
	}{{EntityType: "task", EntityID: uuid.NewString(), Operation: "create", Payload: payload}}}
	scope := &identity.AuthenticatedScope{ProjectID: projectA, Permissions: []string{"task.create", "task.assign"}}

	result, err := tool.Handler(context.Background(), scope, projectA, mustMarshal(t, in))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out := result.(IncrementalPushOutput)
	if len(out.Applied) != 1 || out.Applied[0].Error == "" {
		t.Fatalf("Applied = %+v, want cross-project owner error", out.Applied)
	}
	assertNoTasksForProject(t, tasksStore, projectA)
}

func TestIncrementalPushTool_RejectsNonTodoTaskCreateStatus(t *testing.T) {
	tasksStore := testTasksStore(t)
	tool := IncrementalPushTool(tasksStore, testKBStore(t), testEventsStore(t), NewSyncRateLimiter(30, time.Minute))
	projectID := mustCreateProject(t, "mcp-sync-push-task-status")
	payload, _ := json.Marshal(syncTaskCreatePayload{Title: "invalid status", Status: "wip"})
	in := IncrementalPushInput{NamespaceID: projectID, Version: SyncProtocolVersion, Items: []struct {
		EntityType string          `json:"entity_type"`
		EntityID   string          `json:"entity_id"`
		Operation  string          `json:"operation"`
		Payload    json.RawMessage `json:"payload"`
	}{{EntityType: "task", EntityID: uuid.NewString(), Operation: "create", Payload: payload}}}
	scope := &identity.AuthenticatedScope{ProjectID: projectID, Permissions: []string{"task.create"}}

	result, err := tool.Handler(context.Background(), scope, projectID, mustMarshal(t, in))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out := result.(IncrementalPushOutput)
	if len(out.Applied) != 1 || out.Applied[0].Error != `task create status must be "todo"` {
		t.Fatalf("Applied = %+v, want non-todo status error", out.Applied)
	}
	assertNoTasksForProject(t, tasksStore, projectID)
}

func assertNoTasksForProject(t *testing.T, store interface {
	List(context.Context, string, *string) ([]tasks.Task, error)
}, projectID string) {
	t.Helper()
	rows, err := store.List(context.Background(), projectID, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rejected push persisted tasks: %+v", rows)
	}
}

func TestIncrementalPushTool_PartialFailureDoesNotAbortBatch(t *testing.T) {
	tasksStore := testTasksStore(t)
	tool := IncrementalPushTool(tasksStore, testKBStore(t), testEventsStore(t), NewSyncRateLimiter(30, time.Minute))
	projectID := mustCreateProject(t, "mcp-sync-push-partial")

	goodPayload, _ := json.Marshal(syncTaskCreatePayload{Title: "good task", Description: "d", Priority: 1})
	goodID := uuid.NewString()
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
			{EntityType: "task", EntityID: goodID, Operation: "create", Payload: goodPayload},
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
	if out.Applied[1].ID != goodID || out.Applied[1].Error != "" {
		t.Fatalf("Applied[1] (good item): got %+v, want empty Error", out.Applied[1])
	}

	list, err := tasksStore.List(context.Background(), projectID, nil)
	if err != nil || len(list) != 1 || list[0].Title != "good task" {
		t.Fatalf("good item was not applied despite bad item in same batch: list=%+v err=%v", list, err)
	}
}

func TestIncrementalPushTool_RejectsMalformedUpdateAndUnsupportedDelete(t *testing.T) {
	tasksStore := testTasksStore(t)
	tool := IncrementalPushTool(tasksStore, testKBStore(t), testEventsStore(t), NewSyncRateLimiter(30, time.Minute))
	projectID := mustCreateProject(t, "mcp-sync-push-non-create")

	goodPayload, _ := json.Marshal(syncTaskCreatePayload{Title: "good task", Description: "d", Priority: 1})
	goodID := uuid.NewString()
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
			{EntityType: "task", EntityID: goodID, Operation: "create", Payload: goodPayload},
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
	// A supported task update still rejects a create-shaped payload.
	if out.Applied[0].ID != "update-item" || out.Applied[0].Error == "" {
		t.Fatalf("Applied[0] (update item): got %+v, want a non-empty Error", out.Applied[0])
	}
	if out.Applied[0].Error != `task update payload must match entity_id and include from_status, new_status, channel_id, and a canonical UUID event_id` {
		t.Fatalf("Applied[0].Error: got %q", out.Applied[0].Error)
	}
	// Second item: "delete" operation should be rejected
	if out.Applied[1].ID != "delete-item" || out.Applied[1].Error == "" {
		t.Fatalf("Applied[1] (delete item): got %+v, want a non-empty Error", out.Applied[1])
	}
	if out.Applied[1].Error != `unsupported operation "delete"` {
		t.Fatalf("Applied[1].Error: got %q, want %q", out.Applied[1].Error, `unsupported operation "delete"`)
	}
	// Third item: "create" operation should succeed
	if out.Applied[2].ID != goodID || out.Applied[2].Error != "" {
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

func TestBootstrapTool_ReturnsCompleteDeterministicSnapshot(t *testing.T) {
	identityStore := testIdentityStore(t)
	tasksStore := testTasksStore(t)
	kbStore := testKBStore(t)
	eventsStore := testEventsStore(t)
	projectID := mustCreateProject(t, "mcp-sync-bootstrap")
	agent, passport, token, err := identityStore.Register(context.Background(), projectID,
		[]string{"task.list", "event.publish", "task.list"}, "harley", "claude",
		[]string{"review", "code", "review"}, []string{"z-repo", "a-repo", "z-repo"}, []string{"reviewer", "builder", "reviewer"})
	if err != nil {
		t.Fatalf("register agent: %v", err)
	}
	scope, err := identityStore.WhoAmI(context.Background(), projectID, token)
	if err != nil {
		t.Fatalf("WhoAmI: %v", err)
	}

	channelB, err := eventsStore.CreateChannel(context.Background(), projectID, "zeta")
	if err != nil {
		t.Fatalf("create zeta channel: %v", err)
	}
	channelA, err := eventsStore.CreateChannel(context.Background(), projectID, "alpha")
	if err != nil {
		t.Fatalf("create alpha channel: %v", err)
	}
	note := "bootstrap"
	if _, err := eventsStore.PublishEvent(context.Background(), projectID, channelB.ID, agent.ID, "message.posted", json.RawMessage(`{"body":"bootstrap"}`), &note); err != nil {
		t.Fatalf("publish event: %v", err)
	}

	parent, err := tasksStore.Create(context.Background(), projectID, "parent", "desc", nil, 2, nil)
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	if _, err := tasksStore.Create(context.Background(), projectID, "child", "desc", &parent.ID, 3, nil); err != nil {
		t.Fatalf("create child task: %v", err)
	}
	if _, err := kbStore.WriteArticle(context.Background(), projectID, agent.ID, "bootstrap article", "body text", nil, nil, false); err != nil {
		t.Fatalf("write article: %v", err)
	}

	tool := BootstrapTool(identityStore, tasksStore, kbStore, eventsStore, NewSyncRateLimiter(30, time.Minute))
	arguments := mustMarshal(t, BootstrapInput{NamespaceID: projectID, Version: SyncProtocolVersion})

	result, err := tool.Handler(context.Background(), &scope, projectID, arguments)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out := result.(BootstrapOutput)
	if out.Version != SyncProtocolVersion || out.OrgConfig.SchemaVersion != types.BootstrapSchemaVersionV1 {
		t.Fatalf("versions: outer=%d nested=%d", out.Version, out.OrgConfig.SchemaVersion)
	}
	if out.ProjectList == nil || len(out.ProjectList) != 0 {
		t.Fatalf("ProjectList = %#v, want non-nil empty", out.ProjectList)
	}
	if out.OrgConfig.Project.ID != projectID || out.OrgConfig.Identity.Agent.ID != agent.ID || out.OrgConfig.Identity.Passport.ID != passport.ID {
		t.Fatalf("snapshot identity/project mismatch: %+v", out.OrgConfig)
	}
	if got := out.OrgConfig.Identity.Agent.Capabilities; !reflect.DeepEqual(got, []string{"code", "review"}) {
		t.Fatalf("capabilities = %#v", got)
	}
	if got := out.OrgConfig.Identity.Passport.Repositories; !reflect.DeepEqual(got, []string{"a-repo", "z-repo"}) {
		t.Fatalf("repositories = %#v", got)
	}
	if got := out.OrgConfig.Identity.Passport.Roles; !reflect.DeepEqual(got, []string{"builder", "reviewer"}) {
		t.Fatalf("roles = %#v", got)
	}
	if got := out.OrgConfig.Identity.Permissions; !reflect.DeepEqual(got, []string{"event.publish", "task.list"}) {
		t.Fatalf("permissions = %#v", got)
	}
	if len(out.OrgConfig.Channels) != 2 || out.OrgConfig.Channels[0].ID != channelA.ID || out.OrgConfig.Channels[1].ID != channelB.ID {
		t.Fatalf("channels = %+v, want name/id order", out.OrgConfig.Channels)
	}
	if len(out.OrgConfig.Events) != 1 || out.OrgConfig.Events[0].ChannelID != channelB.ID {
		t.Fatalf("events = %+v", out.OrgConfig.Events)
	}
	if len(out.OrgConfig.Tasks) != 2 || out.OrgConfig.Tasks[0].ID != parent.ID || out.OrgConfig.Tasks[1].ParentTaskID == nil || *out.OrgConfig.Tasks[1].ParentTaskID != parent.ID {
		t.Fatalf("tasks = %+v, want parent before child", out.OrgConfig.Tasks)
	}
	if !reflect.DeepEqual(out.TaskList, out.OrgConfig.Tasks) || !reflect.DeepEqual(out.KBList, out.OrgConfig.KB.Articles) {
		t.Fatalf("top-level mirrors differ: tasks=%+v/%+v kb=%+v/%+v", out.TaskList, out.OrgConfig.Tasks, out.KBList, out.OrgConfig.KB.Articles)
	}
	if string(out.OrgConfig.IntegrationManifestMetadata) != "null" {
		t.Fatalf("integration_manifest_metadata = %s, want null", out.OrgConfig.IntegrationManifestMetadata)
	}
	if parsed, err := time.Parse(time.RFC3339Nano, out.Timestamp); err != nil || parsed.IsZero() {
		t.Fatalf("timestamp = %q: %v", out.Timestamp, err)
	}
}

func TestIncrementalPullTool_FiltersByCursor(t *testing.T) {
	tasksStore := testTasksStore(t)
	kbStore := testKBStore(t)
	eventsStore := testEventsStore(t)
	projectID := mustCreateProject(t, "mcp-sync-pull-cursor")
	agentID, _ := mustRegisterAgent(t, projectID)
	scope := mustBuildScope(agentID, projectID)

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

	result, err := tool.Handler(context.Background(), scope, projectID, arguments)
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
	tool := BootstrapTool(testIdentityStore(t), testTasksStore(t), testKBStore(t), testEventsStore(t), NewSyncRateLimiter(30, time.Minute))
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
	tool := BootstrapTool(testIdentityStore(t), testTasksStore(t), testKBStore(t), testEventsStore(t), NewSyncRateLimiter(30, time.Minute))
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

func TestIncrementalPullTool_RejectsMismatchedAuthenticatedScopeBeforeManifestRead(t *testing.T) {
	projectA := mustCreateProject(t, "mcp-sync-pull-scope-a")
	projectB := mustCreateProject(t, "mcp-sync-pull-scope-b")
	agentA, _ := mustRegisterAgent(t, projectA)
	agentB, _ := mustRegisterAgentWithPerms(t, projectB, []string{IntegrationManifestPublishPermission})
	manifestStore := NewIntegrationManifestStore(testDB(t))
	manifest := readFabricManifestFixture(t, "valid.json")
	manifest.ProjectID = projectB
	publisher := &identity.AuthenticatedScope{
		Agent: identity.Agent{ID: agentB}, ProjectID: projectB,
		Permissions: []string{IntegrationManifestPublishPermission}, Roles: []string{"contributor"},
	}
	if _, err := manifestStore.Publish(context.Background(), publisher, manifest); err != nil {
		t.Fatal(err)
	}

	wrongScope := &identity.AuthenticatedScope{
		Agent: identity.Agent{ID: agentA}, ProjectID: projectA, Roles: []string{"contributor"},
	}
	tool := IncrementalPullTool(
		testTasksStore(t), testKBStore(t), testEventsStore(t),
		NewSyncRateLimiter(30, time.Minute), manifestStore,
	)
	if _, err := tool.Handler(context.Background(), wrongScope, projectB, mustMarshal(t, IncrementalPullInput{
		NamespaceID: projectB, Version: SyncProtocolVersion,
	})); err == nil {
		t.Fatal("incremental pull accepted a mismatched authenticated project scope")
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

func TestBootstrapTool_FiltersManifestByBoundRole(t *testing.T) {
	projectID := mustCreateProject(t, "mcp-sync-manifest-role-filter")
	agentID, _ := mustRegisterAgentWithPerms(t, projectID, []string{IntegrationManifestPublishPermission})
	publisher := &identity.AuthenticatedScope{
		Agent: identity.Agent{ID: agentID}, ProjectID: projectID,
		Permissions: []string{IntegrationManifestPublishPermission}, Roles: []string{"contributor"},
	}
	manifest := readFabricManifestFixture(t, "valid.json")
	manifest.ProjectID = projectID
	manifest.RoleFilters = []string{}
	manifestStore := NewIntegrationManifestStore(testDB(t))
	if _, err := manifestStore.Publish(context.Background(), publisher, manifest); err != nil {
		t.Fatal(err)
	}

	tool := BootstrapTool(
		testIdentityStore(t), testTasksStore(t), testKBStore(t), testEventsStore(t),
		NewSyncRateLimiter(30, time.Minute), manifestStore,
	)
	for _, roles := range [][]string{nil, {"contributor", "reviewer"}, {"Contributor"}} {
		bound := *publisher
		bound.Roles = roles
		result, err := tool.Handler(context.Background(), &bound, projectID, mustMarshal(t, BootstrapInput{
			NamespaceID: projectID, Version: SyncProtocolVersion,
		}))
		if err != nil {
			t.Fatal(err)
		}
		if metadata := string(result.(BootstrapOutput).OrgConfig.IntegrationManifestMetadata); metadata != "null" {
			t.Fatalf("manifest metadata for invalid bound roles %v = %s, want null", roles, metadata)
		}
	}
}
