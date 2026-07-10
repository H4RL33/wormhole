package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/H4RL33/wormhole/internal/core/events"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/core/roles"
)

// testEventsStore returns a real events.Store backed by Postgres. Skips the
// test if Postgres is not reachable (mirrors testTasksStore pattern).
func testEventsStore(t *testing.T) *events.Store {
	t.Helper()
	db := testDB(t)
	return events.NewStore(db)
}

// testRolesStore returns a real roles.Store backed by Postgres, sharing the
// same skip-if-unreachable pattern as testEventsStore/testIdentityStore.
func testRolesStore(t *testing.T) *roles.Store {
	t.Helper()
	db := testDB(t)
	return roles.NewStore(db)
}

// mustRegisterAgent registers an agent in the given project and returns its ID
// and bearer token. This also creates the passport row required by PublishEvent.
func mustRegisterAgent(t *testing.T, projectID string) (agentID string, token string) {
	t.Helper()
	store := testIdentityStore(t)
	agent, _, tok, err := store.Register(context.Background(), projectID, []string{"event.publish"}, "harley", "claude", nil, nil, nil)
	if err != nil {
		t.Fatalf("register agent: %v", err)
	}
	return agent.ID, tok
}

// mustBuildScope constructs a minimal AuthenticatedScope for handler tests that
// need scope.Agent.ID (e.g. PostEventTool) without a full HTTP round-trip.
func mustBuildScope(agentID, projectID string) *identity.AuthenticatedScope {
	return &identity.AuthenticatedScope{
		Agent:       identity.Agent{ID: agentID},
		ProjectID:   projectID,
		Permissions: []string{"event.publish"},
	}
}

func TestChannelTools_CreateChannel(t *testing.T) {
	store := testEventsStore(t)
	tool := CreateChannelTool(store)

	if tool.Name != "wormhole.channel.create" {
		t.Fatalf("Name: got %q, want %q", tool.Name, "wormhole.channel.create")
	}
	if !tool.RequiresAuth {
		t.Fatalf("RequiresAuth: got false, want true")
	}

	projectID := mustCreateProject(t, "mcp-channel-create")
	arguments, _ := json.Marshal(CreateChannelInput{Name: "ci-alerts"})

	result, err := tool.Handler(context.Background(), nil, projectID, arguments)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out, ok := result.(CreateChannelOutput)
	if !ok {
		t.Fatalf("result type: got %T, want CreateChannelOutput", result)
	}
	if out.ChannelID == "" {
		t.Fatalf("output missing ChannelID: %+v", out)
	}
	if out.ProjectID != projectID {
		t.Fatalf("ProjectID: got %q, want %q", out.ProjectID, projectID)
	}
	if out.Name != "ci-alerts" {
		t.Fatalf("Name: got %q, want %q", out.Name, "ci-alerts")
	}
	if out.CreatedAt.IsZero() {
		t.Fatalf("CreatedAt: got zero value")
	}
}

func TestChannelTools_PostEvent(t *testing.T) {
	store := testEventsStore(t)
	projectID := mustCreateProject(t, "mcp-channel-post")

	// Create the channel first.
	channel, err := store.CreateChannel(context.Background(), projectID, "events")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	// Register an agent so the passport row exists (required by PublishEvent).
	agentID, _ := mustRegisterAgent(t, projectID)

	tool := PostEventTool(store)
	if tool.Name != "wormhole.channel.post" {
		t.Fatalf("Name: got %q, want %q", tool.Name, "wormhole.channel.post")
	}
	if !tool.RequiresAuth {
		t.Fatalf("RequiresAuth: got false, want true")
	}

	// Build a minimal AuthenticatedScope so the handler can read Agent.ID.
	scope := mustBuildScope(agentID, projectID)

	arguments, _ := json.Marshal(PostEventInput{
		ChannelID: channel.ID,
		EventType: "message.posted",
		Payload:   json.RawMessage(`{"text":"hello"}`),
	})

	result, err := tool.Handler(context.Background(), scope, projectID, arguments)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out, ok := result.(PostEventOutput)
	if !ok {
		t.Fatalf("result type: got %T, want PostEventOutput", result)
	}
	if out.EventID == "" {
		t.Fatalf("output missing EventID: %+v", out)
	}
	if out.ProjectID != projectID {
		t.Fatalf("ProjectID: got %q, want %q", out.ProjectID, projectID)
	}
	if out.ChannelID != channel.ID {
		t.Fatalf("ChannelID: got %q, want %q", out.ChannelID, channel.ID)
	}
	if out.AgentID != agentID {
		t.Fatalf("AgentID: got %q, want %q", out.AgentID, agentID)
	}
	if out.EventType != "message.posted" {
		t.Fatalf("EventType: got %q, want %q", out.EventType, "message.posted")
	}
}

func TestChannelTools_Subscribe(t *testing.T) {
	store := testEventsStore(t)
	projectID := mustCreateProject(t, "mcp-channel-subscribe")

	channel, err := store.CreateChannel(context.Background(), projectID, "feed")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	agentID, _ := mustRegisterAgent(t, projectID)

	// Publish two events directly via the store.
	payload := json.RawMessage(`{}`)
	if _, err := store.PublishEvent(context.Background(), projectID, channel.ID, agentID, "message.posted", payload, nil); err != nil {
		t.Fatalf("publish event 1: %v", err)
	}
	if _, err := store.PublishEvent(context.Background(), projectID, channel.ID, agentID, "build.failed", payload, nil); err != nil {
		t.Fatalf("publish event 2: %v", err)
	}

	tool := SubscribeChannelTool(store)
	if tool.Name != "wormhole.channel.subscribe" {
		t.Fatalf("Name: got %q, want %q", tool.Name, "wormhole.channel.subscribe")
	}
	if !tool.RequiresAuth {
		t.Fatalf("RequiresAuth: got false, want true")
	}

	arguments, _ := json.Marshal(SubscribeChannelInput{ChannelID: channel.ID})
	result, err := tool.Handler(context.Background(), nil, projectID, arguments)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out, ok := result.(SubscribeChannelOutput)
	if !ok {
		t.Fatalf("result type: got %T, want SubscribeChannelOutput", result)
	}
	if len(out.Events) != 2 {
		t.Fatalf("Events: got %d, want 2", len(out.Events))
	}
	if out.Events[0].EventType != "message.posted" {
		t.Fatalf("Events[0].EventType: got %q, want %q", out.Events[0].EventType, "message.posted")
	}
	if out.Events[1].EventType != "build.failed" {
		t.Fatalf("Events[1].EventType: got %q, want %q", out.Events[1].EventType, "build.failed")
	}
}

func TestChannelTools_ListChannels(t *testing.T) {
	store := testEventsStore(t)
	tool := ListChannelsTool(store)

	if tool.Name != "wormhole.channel.list" {
		t.Fatalf("Name: got %q, want %q", tool.Name, "wormhole.channel.list")
	}
	if !tool.RequiresAuth {
		t.Fatalf("RequiresAuth: got false, want true")
	}

	projectID := mustCreateProject(t, "mcp-channel-list")
	arguments, _ := json.Marshal(ListChannelsInput{})

	// Zero channels.
	result, err := tool.Handler(context.Background(), nil, projectID, arguments)
	if err != nil {
		t.Fatalf("Handler (zero channels): %v", err)
	}
	out, ok := result.(ListChannelsOutput)
	if !ok {
		t.Fatalf("result type: got %T, want ListChannelsOutput", result)
	}
	if len(out.Channels) != 0 {
		t.Fatalf("Channels (zero): got %d, want 0", len(out.Channels))
	}

	// One channel.
	first, err := store.CreateChannel(context.Background(), projectID, "alpha")
	if err != nil {
		t.Fatalf("create channel alpha: %v", err)
	}
	result, err = tool.Handler(context.Background(), nil, projectID, arguments)
	if err != nil {
		t.Fatalf("Handler (one channel): %v", err)
	}
	out = result.(ListChannelsOutput)
	if len(out.Channels) != 1 {
		t.Fatalf("Channels (one): got %d, want 1", len(out.Channels))
	}
	if out.Channels[0].ChannelID != first.ID || out.Channels[0].Name != "alpha" || out.Channels[0].ProjectID != projectID {
		t.Fatalf("Channels[0]: got %+v", out.Channels[0])
	}

	// Two channels.
	second, err := store.CreateChannel(context.Background(), projectID, "beta")
	if err != nil {
		t.Fatalf("create channel beta: %v", err)
	}
	result, err = tool.Handler(context.Background(), nil, projectID, arguments)
	if err != nil {
		t.Fatalf("Handler (two channels): %v", err)
	}
	out = result.(ListChannelsOutput)
	if len(out.Channels) != 2 {
		t.Fatalf("Channels (two): got %d, want 2", len(out.Channels))
	}
	names := map[string]string{out.Channels[0].Name: out.Channels[0].ChannelID, out.Channels[1].Name: out.Channels[1].ChannelID}
	if names["alpha"] != first.ID || names["beta"] != second.ID {
		t.Fatalf("Channels: got %+v, want alpha=%s beta=%s", out.Channels, first.ID, second.ID)
	}
}

func TestChannelTools_PostInvalidEventType(t *testing.T) {
	store := testEventsStore(t)
	projectID := mustCreateProject(t, "mcp-channel-invalid-type")

	channel, err := store.CreateChannel(context.Background(), projectID, "bad-events")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	agentID, _ := mustRegisterAgent(t, projectID)
	scope := mustBuildScope(agentID, projectID)

	tool := PostEventTool(store)
	arguments, _ := json.Marshal(PostEventInput{
		ChannelID: channel.ID,
		EventType: "not.a.valid.type",
		Payload:   json.RawMessage(`{}`),
	})

	_, err = tool.Handler(context.Background(), scope, projectID, arguments)
	if err == nil {
		t.Fatalf("Handler: got nil error, want error for invalid event type")
	}
	if !errors.Is(err, events.ErrInvalidEventType) {
		t.Fatalf("Handler error: got %v, want to wrap ErrInvalidEventType", err)
	}
}
