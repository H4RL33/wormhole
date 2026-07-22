package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/H4RL33/wormhole/internal/core/identity"
)

type pushTestItem struct {
	entityType string
	entityID   string
	operation  string
	payload    json.RawMessage
}

func pushInput(projectID string, items ...pushTestItem) IncrementalPushInput {
	in := IncrementalPushInput{NamespaceID: projectID, Version: SyncProtocolVersion}
	for _, item := range items {
		in.Items = append(in.Items, struct {
			EntityType string          `json:"entity_type"`
			EntityID   string          `json:"entity_id"`
			Operation  string          `json:"operation"`
			Payload    json.RawMessage `json:"payload"`
		}{item.entityType, item.entityID, item.operation, item.payload})
	}
	return in
}

func TestSyncToolsRejectMissingNamespace(t *testing.T) {
	tasksStore := testTasksStore(t)
	kbStore := testKBStore(t)
	eventsStore := testEventsStore(t)
	projectID := mustCreateProject(t, "mcp-sync-missing-namespace")

	tests := []struct {
		name string
		tool Tool
		args any
	}{
		{"bootstrap", BootstrapTool(tasksStore, kbStore, eventsStore, NewSyncRateLimiter(10, time.Minute)), BootstrapInput{Version: SyncProtocolVersion}},
		{"pull", IncrementalPullTool(tasksStore, kbStore, eventsStore, NewSyncRateLimiter(10, time.Minute)), IncrementalPullInput{Version: SyncProtocolVersion}},
		{"push", IncrementalPushTool(tasksStore, kbStore, eventsStore, NewSyncRateLimiter(10, time.Minute)), IncrementalPushInput{Version: SyncProtocolVersion}},
		{"conflict", ConflictReportTool(tasksStore, kbStore, eventsStore, NewSyncRateLimiter(10, time.Minute)), ConflictReportInput{Version: SyncProtocolVersion}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.tool.Handler(context.Background(), nil, projectID, mustMarshal(t, tt.args))
			if err == nil || !strings.Contains(err.Error(), "missing namespace_id") {
				t.Fatalf("Handler error = %v, want missing namespace_id", err)
			}
		})
	}
}

func TestIncrementalPullRejectsInvalidCursorAndRateLimit(t *testing.T) {
	tasksStore := testTasksStore(t)
	kbStore := testKBStore(t)
	eventsStore := testEventsStore(t)
	projectID := mustCreateProject(t, "mcp-sync-pull-validation")

	t.Run("invalid cursor", func(t *testing.T) {
		invalid := "not-a-timestamp"
		tool := IncrementalPullTool(tasksStore, kbStore, eventsStore, NewSyncRateLimiter(10, time.Minute))
		_, err := tool.Handler(context.Background(), nil, projectID, mustMarshal(t, IncrementalPullInput{
			NamespaceID: projectID,
			LastSync:    &invalid,
			Version:     SyncProtocolVersion,
		}))
		if err == nil || !strings.Contains(err.Error(), "invalid last_sync") {
			t.Fatalf("Handler error = %v, want invalid last_sync", err)
		}
	})

	t.Run("rate limit", func(t *testing.T) {
		tool := IncrementalPullTool(tasksStore, kbStore, eventsStore, NewSyncRateLimiter(0, time.Minute))
		_, err := tool.Handler(context.Background(), nil, projectID, mustMarshal(t, IncrementalPullInput{
			NamespaceID: projectID,
			Version:     SyncProtocolVersion,
		}))
		if err == nil || !strings.Contains(err.Error(), "rate limit exceeded") {
			t.Fatalf("Handler error = %v, want rate limit exceeded", err)
		}
	})
}

func TestIncrementalPushValidatesRequiredItemFields(t *testing.T) {
	tasksStore := testTasksStore(t)
	kbStore := testKBStore(t)
	eventsStore := testEventsStore(t)
	projectID := mustCreateProject(t, "mcp-sync-push-required-fields")

	tests := []struct {
		name string
		item pushTestItem
		want string
	}{
		{"entity type", pushTestItem{entityID: uuid.NewString(), operation: "create", payload: json.RawMessage(`{}`)}, "missing entity_type"},
		{"entity id", pushTestItem{entityType: "task", operation: "create", payload: json.RawMessage(`{}`)}, "missing entity_id"},
		{"operation", pushTestItem{entityType: "task", entityID: uuid.NewString(), payload: json.RawMessage(`{}`)}, "missing operation"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := IncrementalPushTool(tasksStore, kbStore, eventsStore, NewSyncRateLimiter(10, time.Minute))
			_, err := tool.Handler(context.Background(), nil, projectID, mustMarshal(t, pushInput(projectID, tt.item)))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Handler error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestIncrementalPushAppliesKBChannelAndEventCreates(t *testing.T) {
	tasksStore := testTasksStore(t)
	kbStore := testKBStore(t)
	eventsStore := testEventsStore(t)
	projectID := mustCreateProject(t, "mcp-sync-push-entity-types")
	agentID, _ := mustRegisterAgent(t, projectID)
	channelID := uuid.NewString()
	articleID := uuid.NewString()
	eventID := uuid.NewString()

	kbPayload := mustMarshal(t, syncKBCreatePayload{Title: "synced article", Body: "durable body"})
	channelPayload := mustMarshal(t, syncChannelCreatePayload{Name: "synced-channel"})
	eventPayload := mustMarshal(t, syncEventCreatePayload{
		ChannelID: channelID,
		EventType: "discovery.logged",
		Payload:   json.RawMessage(`{"ok":true}`),
	})
	in := pushInput(projectID,
		pushTestItem{"kb", articleID, "create", kbPayload},
		pushTestItem{"channel", channelID, "create", channelPayload},
		pushTestItem{"event", eventID, "create", eventPayload},
	)
	scope := &identity.AuthenticatedScope{
		ProjectID:   projectID,
		Agent:       identity.Agent{ID: agentID},
		Permissions: []string{"kb.write", "channel.create", "channel.post"},
	}

	result, err := IncrementalPushTool(tasksStore, kbStore, eventsStore, NewSyncRateLimiter(10, time.Minute)).Handler(
		context.Background(), scope, projectID, mustMarshal(t, in),
	)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out := result.(IncrementalPushOutput)
	if len(out.Applied) != 3 {
		t.Fatalf("Applied = %+v, want three results", out.Applied)
	}
	for _, applied := range out.Applied {
		if applied.Error != "" {
			t.Fatalf("Applied = %+v, want every entity persisted", out.Applied)
		}
	}

	article, err := kbStore.GetArticle(context.Background(), projectID, agentID, articleID)
	if err != nil || article.Title != "synced article" {
		t.Fatalf("GetArticle = %+v, %v, want synced article", article, err)
	}
	channels, err := eventsStore.ListChannels(context.Background(), projectID)
	if err != nil || len(channels) == 0 || channels[0].ID != channelID {
		t.Fatalf("ListChannels = %+v, %v, want channel %s", channels, err, channelID)
	}
	eventRows, err := eventsStore.ListEvents(context.Background(), projectID, channelID, 10, 0)
	if err != nil || len(eventRows) != 1 || eventRows[0].ID != eventID {
		t.Fatalf("ListEvents = %+v, %v, want event %s", eventRows, err, eventID)
	}
}

func TestIncrementalPushReportsMalformedEntityPayloads(t *testing.T) {
	projectID := mustCreateProject(t, "mcp-sync-push-malformed-entities")
	scope := &identity.AuthenticatedScope{
		ProjectID: projectID,
		Permissions: []string{
			"task.create", "kb.write", "channel.create", "channel.post",
		},
	}
	items := []pushTestItem{
		{"task", uuid.NewString(), "create", json.RawMessage(`"bad"`)},
		{"kb", uuid.NewString(), "create", json.RawMessage(`"bad"`)},
		{"channel", uuid.NewString(), "create", json.RawMessage(`"bad"`)},
		{"event", uuid.NewString(), "create", json.RawMessage(`"bad"`)},
	}
	result, err := IncrementalPushTool(testTasksStore(t), testKBStore(t), testEventsStore(t), NewSyncRateLimiter(10, time.Minute)).Handler(
		context.Background(), scope, projectID, mustMarshal(t, pushInput(projectID, items...)),
	)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out := result.(IncrementalPushOutput)
	if len(out.Applied) != len(items) {
		t.Fatalf("Applied = %+v, want %d per-item errors", out.Applied, len(items))
	}
	for _, applied := range out.Applied {
		if !strings.Contains(applied.Error, "decode "+applied.Type+" payload") {
			t.Errorf("Applied[%s].Error = %q, want decode error", applied.Type, applied.Error)
		}
	}
}

func TestIncrementalPullIncludesKBUpdates(t *testing.T) {
	tasksStore := testTasksStore(t)
	kbStore := testKBStore(t)
	eventsStore := testEventsStore(t)
	projectID := mustCreateProject(t, "mcp-sync-pull-kb")
	agentID, _ := mustRegisterAgent(t, projectID)
	article, err := kbStore.WriteArticle(context.Background(), projectID, agentID, "pull me", "body", nil, nil, false)
	if err != nil {
		t.Fatalf("WriteArticle: %v", err)
	}

	result, err := IncrementalPullTool(tasksStore, kbStore, eventsStore, NewSyncRateLimiter(10, time.Minute)).Handler(
		context.Background(), nil, projectID, mustMarshal(t, IncrementalPullInput{NamespaceID: projectID, Version: SyncProtocolVersion}),
	)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out := result.(IncrementalPullOutput)
	if len(out.Updates) != 1 {
		t.Fatalf("Updates = %+v, want one KB update", out.Updates)
	}
	var envelope syncUpdateEnvelope
	if err := json.Unmarshal(out.Updates[0], &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	var got ArticleSummary
	if err := json.Unmarshal(envelope.Data, &got); err != nil {
		t.Fatalf("decode article: %v", err)
	}
	if envelope.Type != "kb" || got.ArticleID != article.ID {
		t.Fatalf("update = type %q article %+v, want kb/%s", envelope.Type, got, article.ID)
	}
}

func TestConflictReportRequiresScopeReusesChannelAndReportsPublishFailure(t *testing.T) {
	tasksStore := testTasksStore(t)
	kbStore := testKBStore(t)
	eventsStore := testEventsStore(t)
	projectID := mustCreateProject(t, "mcp-sync-conflict-errors")
	tool := ConflictReportTool(tasksStore, kbStore, eventsStore, NewSyncRateLimiter(20, time.Minute))
	base := ConflictReportInput{
		NamespaceID: projectID, Version: SyncProtocolVersion,
		EntityType: "task", EntityID: "task-1", ServerValue: "server", LocalValue: "local",
	}

	if _, err := tool.Handler(context.Background(), nil, projectID, mustMarshal(t, base)); err == nil || !strings.Contains(err.Error(), "missing authenticated scope") {
		t.Fatalf("nil-scope error = %v, want missing authenticated scope", err)
	}

	agentID, _ := mustRegisterAgent(t, projectID)
	scope := mustBuildScope(agentID, projectID)
	for i := 0; i < 2; i++ {
		base.EntityID = string(rune('a' + i))
		if _, err := tool.Handler(context.Background(), scope, projectID, mustMarshal(t, base)); err != nil {
			t.Fatalf("Handler call %d: %v", i+1, err)
		}
	}
	channels, err := eventsStore.ListChannels(context.Background(), projectID)
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	auditChannels := 0
	for _, channel := range channels {
		if channel.Name == SyncAuditChannelID {
			auditChannels++
		}
	}
	if auditChannels != 1 {
		t.Fatalf("audit channel count = %d, want one reused channel", auditChannels)
	}

	badScope := &identity.AuthenticatedScope{ProjectID: projectID, Agent: identity.Agent{ID: uuid.NewString()}}
	_, err = tool.Handler(context.Background(), badScope, projectID, mustMarshal(t, base))
	if err == nil || !strings.Contains(err.Error(), "publish audit event") {
		t.Fatalf("unregistered-agent error = %v, want publish audit event failure", err)
	}
}

func TestConflictReportRejectsMissingEntityIDAndRateLimit(t *testing.T) {
	projectID := mustCreateProject(t, "mcp-sync-conflict-validation")
	stores := struct {
		tasks   Tool
		limited Tool
	}{
		tasks:   ConflictReportTool(testTasksStore(t), testKBStore(t), testEventsStore(t), NewSyncRateLimiter(10, time.Minute)),
		limited: ConflictReportTool(testTasksStore(t), testKBStore(t), testEventsStore(t), NewSyncRateLimiter(0, time.Minute)),
	}
	missingID := ConflictReportInput{NamespaceID: projectID, Version: SyncProtocolVersion, EntityType: "task"}
	if _, err := stores.tasks.Handler(context.Background(), nil, projectID, mustMarshal(t, missingID)); err == nil || !strings.Contains(err.Error(), "missing entity_id") {
		t.Fatalf("missing-ID error = %v, want missing entity_id", err)
	}
	valid := ConflictReportInput{NamespaceID: projectID, Version: SyncProtocolVersion, EntityType: "task", EntityID: "1"}
	if _, err := stores.limited.Handler(context.Background(), nil, projectID, mustMarshal(t, valid)); err == nil || !strings.Contains(err.Error(), "rate limit exceeded") {
		t.Fatalf("rate-limit error = %v, want rate limit exceeded", err)
	}
}

func TestSyncToolsPropagateCanceledContext(t *testing.T) {
	projectID := mustCreateProject(t, "mcp-sync-canceled")
	tasksStore := testTasksStore(t)
	kbStore := testKBStore(t)
	eventsStore := testEventsStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name string
		tool Tool
		args any
		want string
	}{
		{"bootstrap", BootstrapTool(tasksStore, kbStore, eventsStore, NewSyncRateLimiter(10, time.Minute)), BootstrapInput{NamespaceID: projectID, Version: SyncProtocolVersion}, "list tasks"},
		{"pull", IncrementalPullTool(tasksStore, kbStore, eventsStore, NewSyncRateLimiter(10, time.Minute)), IncrementalPullInput{NamespaceID: projectID, Version: SyncProtocolVersion}, "list tasks"},
		{"conflict", ConflictReportTool(tasksStore, kbStore, eventsStore, NewSyncRateLimiter(10, time.Minute)), ConflictReportInput{NamespaceID: projectID, Version: SyncProtocolVersion, EntityType: "task", EntityID: "1"}, "list channels"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.tool.Handler(ctx, &identity.AuthenticatedScope{}, projectID, mustMarshal(t, tt.args))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Handler error = %v, want %q", err, tt.want)
			}
		})
	}

	push := IncrementalPushTool(tasksStore, kbStore, eventsStore, NewSyncRateLimiter(10, time.Minute))
	result, err := push.Handler(ctx, nil, projectID, mustMarshal(t, pushInput(projectID,
		pushTestItem{"task", uuid.NewString(), "create", mustMarshal(t, syncTaskCreatePayload{Title: "canceled"})},
	)))
	if err != nil {
		t.Fatalf("push Handler: %v", err)
	}
	out := result.(IncrementalPushOutput)
	if len(out.Applied) != 1 || !strings.Contains(out.Applied[0].Error, context.Canceled.Error()) {
		t.Fatalf("push Applied = %+v, want canceled store error", out.Applied)
	}
}
