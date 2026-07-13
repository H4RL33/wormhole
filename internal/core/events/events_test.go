package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	_ "github.com/lib/pq"

	"github.com/H4RL33/wormhole/internal/types"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	cfg := types.LoadConfig()
	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		if os.Getenv("WORMHOLE_INTEGRATION_REQUIRED") == "1" {
			t.Fatalf("postgres required but not reachable: %v", err)
		}
		t.Skipf("postgres not reachable (%v); run `docker compose up -d db` and apply migrations before running this test", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db)
}

func createProject(t *testing.T, s *Store, name string) string {
	t.Helper()
	var id string
	if err := s.db.QueryRow(`INSERT INTO projects (name, owner) VALUES ($1, $2) RETURNING id`, name, "harley").Scan(&id); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		if _, err := s.db.Exec(`DELETE FROM projects WHERE id = $1`, id); err != nil {
			t.Logf("cleanup: delete project %s: %v", id, err)
		}
	})
	return id
}

func createAgent(t *testing.T, s *Store) string {
	t.Helper()
	var id string
	if err := s.db.QueryRow(`INSERT INTO agents (owner, model, capabilities) VALUES ($1, $2, $3) RETURNING id`,
		"harley", "claude", `[]`).Scan(&id); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() {
		if _, err := s.db.Exec(`DELETE FROM agents WHERE id = $1`, id); err != nil {
			t.Logf("cleanup: delete agent %s: %v", id, err)
		}
	})
	return id
}

func createPassport(t *testing.T, s *Store, agentID, projectID string) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO passports (agent_id, project_id) VALUES ($1, $2)`, agentID, projectID); err != nil {
		t.Fatalf("create passport: %v", err)
	}
	t.Cleanup(func() {
		if _, err := s.db.Exec(`DELETE FROM passports WHERE agent_id = $1 AND project_id = $2`, agentID, projectID); err != nil {
			t.Logf("cleanup: delete passport for agent %s in project %s: %v", agentID, projectID, err)
		}
	})
}

func TestCreateChannel_Success(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "create-channel-success")

	channel, err := s.CreateChannel(ctx, projectID, "general")
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}

	if channel.ID == "" {
		t.Error("channel.ID is empty")
	}
	if channel.ProjectID != projectID {
		t.Errorf("channel.ProjectID = %q, want %q", channel.ProjectID, projectID)
	}
	if channel.Name != "general" {
		t.Errorf("channel.Name = %q, want %q", channel.Name, "general")
	}
	if channel.CreatedAt.IsZero() {
		t.Error("channel.CreatedAt is zero")
	}

	// Verify we can get it
	got, err := s.GetChannel(ctx, projectID, channel.ID)
	if err != nil {
		t.Fatalf("GetChannel: %v", err)
	}
	if got.ID != channel.ID || got.Name != channel.Name {
		t.Errorf("GetChannel returned %+v, want %+v", got, channel)
	}
}

func TestListChannels_Scoping(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectA := createProject(t, s, "scoping-project-a")
	projectB := createProject(t, s, "scoping-project-b")

	chA, err := s.CreateChannel(ctx, projectA, "channel-a")
	if err != nil {
		t.Fatalf("CreateChannel A: %v", err)
	}
	chB, err := s.CreateChannel(ctx, projectB, "channel-b")
	if err != nil {
		t.Fatalf("CreateChannel B: %v", err)
	}

	// List channels for project A
	listA, err := s.ListChannels(ctx, projectA)
	if err != nil {
		t.Fatalf("ListChannels A: %v", err)
	}
	foundA := false
	for _, c := range listA {
		if c.ID == chB.ID {
			t.Errorf("found channel B in project A's channels list")
		}
		if c.ID == chA.ID {
			foundA = true
		}
	}
	if !foundA {
		t.Errorf("channel A not found in project A's channels list")
	}

	// Try to get channel B with project A context
	_, err = s.GetChannel(ctx, projectA, chB.ID)
	if !errors.Is(err, ErrChannelNotFound) {
		t.Errorf("GetChannel cross-project got error = %v, want %v", err, ErrChannelNotFound)
	}
}

func TestPublishEvent_Success(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "publish-event-success")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)
	channel, err := s.CreateChannel(ctx, projectID, "events-channel")
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}

	payload := json.RawMessage(`{"status":"success"}`)
	note := "Everything is fine"
	event, err := s.PublishEvent(ctx, projectID, channel.ID, agentID, "task.status_changed", payload, &note)
	if err != nil {
		t.Fatalf("PublishEvent: %v", err)
	}

	if event.ID == "" {
		t.Error("event.ID is empty")
	}
	if event.ProjectID != projectID {
		t.Errorf("event.ProjectID = %q, want %q", event.ProjectID, projectID)
	}
	if event.ChannelID != channel.ID {
		t.Errorf("event.ChannelID = %q, want %q", event.ChannelID, channel.ID)
	}
	if event.AgentID != agentID {
		t.Errorf("event.AgentID = %q, want %q", event.AgentID, agentID)
	}
	if event.EventType != "task.status_changed" {
		t.Errorf("event.EventType = %q, want %q", event.EventType, "task.status_changed")
	}
	var gotPayload map[string]string
	if err := json.Unmarshal(event.Payload, &gotPayload); err != nil {
		t.Fatalf("event.Payload is not valid JSON: %v", err)
	}
	if gotPayload["status"] != "success" {
		t.Errorf("event.Payload[status] = %q, want %q", gotPayload["status"], "success")
	}
	if event.Note == nil || *event.Note != note {
		t.Errorf("event.Note = %v, want %q", event.Note, note)
	}
	if event.CreatedAt.IsZero() {
		t.Error("event.CreatedAt is zero")
	}
}

func TestPublishEvent_InvalidTypeRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "publish-invalid-type")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)
	channel, err := s.CreateChannel(ctx, projectID, "events-channel")
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}

	payload := json.RawMessage(`{}`)
	_, err = s.PublishEvent(ctx, projectID, channel.ID, agentID, "invalid.type", payload, nil)
	if !errors.Is(err, ErrInvalidEventType) {
		t.Errorf("PublishEvent(invalid.type) error = %v, want %v", err, ErrInvalidEventType)
	}
}

func TestPublishEvent_PassportRequired(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "publish-passport-required")
	agentID := createAgent(t, s)
	channel, err := s.CreateChannel(ctx, projectID, "events-channel")
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}

	payload := json.RawMessage(`{"status":"success"}`)
	_, err = s.PublishEvent(ctx, projectID, channel.ID, agentID, "task.status_changed", payload, nil)
	if !errors.Is(err, ErrPassportNotFound) {
		t.Fatalf("expected ErrPassportNotFound, got: %v", err)
	}
}

func TestPublishEvent_CrossProjectAgentRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID1 := createProject(t, s, "cross-project-1")
	projectID2 := createProject(t, s, "cross-project-2")
	agentID := createAgent(t, s)

	// Agent has passport in project 1, but NOT in project 2
	createPassport(t, s, agentID, projectID1)

	// Create channel in project 2
	channel, err := s.CreateChannel(ctx, projectID2, "events-channel")
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}

	// Try to publish event to channel in project 2 with agentID that only has a passport in project 1
	payload := json.RawMessage(`{"status":"success"}`)
	_, err = s.PublishEvent(ctx, projectID2, channel.ID, agentID, "task.status_changed", payload, nil)
	if !errors.Is(err, ErrPassportNotFound) {
		t.Fatalf("expected ErrPassportNotFound, got: %v", err)
	}
}

func TestListEvents_Scoping(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "list-events-scoping")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)
	channelA, err := s.CreateChannel(ctx, projectID, "channel-a")
	if err != nil {
		t.Fatalf("CreateChannel A: %v", err)
	}
	channelB, err := s.CreateChannel(ctx, projectID, "channel-b")
	if err != nil {
		t.Fatalf("CreateChannel B: %v", err)
	}

	payload := json.RawMessage(`{}`)
	evA1, err := s.PublishEvent(ctx, projectID, channelA.ID, agentID, "task.status_changed", payload, nil)
	if err != nil {
		t.Fatalf("PublishEvent A1: %v", err)
	}
	evA2, err := s.PublishEvent(ctx, projectID, channelA.ID, agentID, "review.requested", payload, nil)
	if err != nil {
		t.Fatalf("PublishEvent A2: %v", err)
	}
	evB, err := s.PublishEvent(ctx, projectID, channelB.ID, agentID, "build.failed", payload, nil)
	if err != nil {
		t.Fatalf("PublishEvent B: %v", err)
	}

	// List events for channel A
	eventsA, err := s.ListEvents(ctx, projectID, channelA.ID, 10, 0)
	if err != nil {
		t.Fatalf("ListEvents A: %v", err)
	}

	if len(eventsA) != 2 {
		t.Fatalf("ListEvents A returned %d events, want 2", len(eventsA))
	}
	// Verify order is oldest first (based on created_at)
	if eventsA[0].ID != evA1.ID {
		t.Errorf("eventsA[0].ID = %q, want %q (evA1)", eventsA[0].ID, evA1.ID)
	}
	if eventsA[1].ID != evA2.ID {
		t.Errorf("eventsA[1].ID = %q, want %q (evA2)", eventsA[1].ID, evA2.ID)
	}

	// Verify evB is not in eventsA
	for _, e := range eventsA {
		if e.ID == evB.ID {
			t.Errorf("found event B in channel A's events list")
		}
	}

	// Test pagination
	paged, err := s.ListEvents(ctx, projectID, channelA.ID, 1, 1)
	if err != nil {
		t.Fatalf("ListEvents A paged: %v", err)
	}
	if len(paged) != 1 {
		t.Fatalf("ListEvents A paged returned %d events, want 1", len(paged))
	}
	if paged[0].ID != evA2.ID {
		t.Errorf("paged[0].ID = %q, want %q (evA2)", paged[0].ID, evA2.ID)
	}
}

func TestRLSIsolation(t *testing.T) {
	ownerStore := testStore(t)

	roleName := "events_rls_test_user"
	rolePassword := "events_rls_test_password"

	// Ensure clean slate and cleanup afterwards
	t.Cleanup(func() {
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE channels FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE events FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE projects FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE agents FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("DROP ROLE IF EXISTS %s", roleName))
	})

	_, err := ownerStore.db.Exec(fmt.Sprintf("DROP ROLE IF EXISTS %s", roleName))
	if err != nil {
		t.Fatalf("failed to drop pre-existing role: %v", err)
	}

	_, err = ownerStore.db.Exec(fmt.Sprintf("CREATE ROLE %s WITH LOGIN PASSWORD '%s'", roleName, rolePassword))
	if err != nil {
		t.Fatalf("failed to create role: %v", err)
	}

	_, err = ownerStore.db.Exec(fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE channels, events, projects, agents TO %s", roleName))
	if err != nil {
		t.Fatalf("failed to grant table privileges: %v", err)
	}

	cfg := types.LoadConfig()
	u, err := url.Parse(cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("failed to parse database URL: %v", err)
	}
	u.User = url.UserPassword(roleName, rolePassword)
	restrictedDSN := u.String()

	restrictedDb, err := sql.Open("postgres", restrictedDSN)
	if err != nil {
		t.Fatalf("failed to open restricted db connection: %v", err)
	}
	t.Cleanup(func() {
		restrictedDb.Close()
	})

	if err := restrictedDb.PingContext(context.Background()); err != nil {
		t.Fatalf("failed to ping restricted database: %v", err)
	}

	// Create two projects and channels using ownerStore
	projectAID := createProject(t, ownerStore, "Project A (Events)")
	projectBID := createProject(t, ownerStore, "Project B (Events)")

	ctx := context.Background()
	chA, err := ownerStore.CreateChannel(ctx, projectAID, "channel-a")
	if err != nil {
		t.Fatalf("failed to create channel in project A: %v", err)
	}

	// 1. Attempt to read Project A's channel using the restricted connection without setting RLS context, verifying RLS blocks it.
	var foundID string
	err = restrictedDb.QueryRowContext(ctx, "SELECT id FROM channels WHERE id = $1", chA.ID).Scan(&foundID)
	if err == nil {
		t.Errorf("expected channel to be hidden by RLS when no project context is set, but read ID: %s", foundID)
	} else if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("unexpected error when querying channel without project context: %v", err)
	}

	// 2. Attempt to read Project A's channel using the restricted connection with RLS context set, verifying it succeeds.
	restrictedStore := NewStore(restrictedDb)
	channels, err := restrictedStore.ListChannels(ctx, projectAID)
	if err != nil {
		t.Fatalf("failed to list channels under restricted store with correct context: %v", err)
	}
	found := false
	for _, c := range channels {
		if c.ID == chA.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected channel %s to be visible with correct project context, but it was not found in the list", chA.ID)
	}

	// 3. Attempt to read Project A's channel using the restricted connection with Project B's RLS context set, verifying it returns empty/not found.
	channelsB, err := restrictedStore.ListChannels(ctx, projectBID)
	if err != nil {
		t.Fatalf("failed to list channels under restricted store with project B context: %v", err)
	}
	for _, c := range channelsB {
		if c.ID == chA.ID {
			t.Errorf("channel %s (from project A) was visible under restricted store when project B context was set", chA.ID)
		}
	}
}

func TestListEventsByProject_HappyPath(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "events-list-by-project-happy-path")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	// Create 2 channels in the project
	channel1, err := s.CreateChannel(ctx, projectID, "channel-1")
	if err != nil {
		t.Fatalf("CreateChannel 1: %v", err)
	}

	channel2, err := s.CreateChannel(ctx, projectID, "channel-2")
	if err != nil {
		t.Fatalf("CreateChannel 2: %v", err)
	}

	// Publish 2 events to channel 1
	payload1 := json.RawMessage(`{"task_id":"task-1"}`)
	event1, err := s.PublishEvent(ctx, projectID, channel1.ID, agentID, "task.status_changed", payload1, nil)
	if err != nil {
		t.Fatalf("PublishEvent 1: %v", err)
	}

	payload2 := json.RawMessage(`{"task_id":"task-2"}`)
	event2, err := s.PublishEvent(ctx, projectID, channel1.ID, agentID, "task.status_changed", payload2, nil)
	if err != nil {
		t.Fatalf("PublishEvent 2: %v", err)
	}

	// Publish 1 event to channel 2
	payload3 := json.RawMessage(`{"task_id":"task-3"}`)
	event3, err := s.PublishEvent(ctx, projectID, channel2.ID, agentID, "task.status_changed", payload3, nil)
	if err != nil {
		t.Fatalf("PublishEvent 3: %v", err)
	}

	// List all events in the project
	events, err := s.ListEventsByProject(ctx, projectID, 100, 0)
	if err != nil {
		t.Fatalf("ListEventsByProject: %v", err)
	}

	// Should have all 3 events
	if len(events) != 3 {
		t.Errorf("expected 3 events, got %d", len(events))
	}

	// Verify ordering: newest (event3) first, oldest (event1) last
	if events[0].ID != event3.ID {
		t.Errorf("first event should be event3 (ID %q), got %q", event3.ID, events[0].ID)
	}
	if events[1].ID != event2.ID {
		t.Errorf("second event should be event2 (ID %q), got %q", event2.ID, events[1].ID)
	}
	if events[2].ID != event1.ID {
		t.Errorf("third event should be event1 (ID %q), got %q", event1.ID, events[2].ID)
	}

	// Verify events from both channels are included
	channelIDs := map[string]bool{}
	for _, event := range events {
		if event.ProjectID != projectID {
			t.Errorf("event ProjectID = %q, want %q", event.ProjectID, projectID)
		}
		channelIDs[event.ChannelID] = true
	}
	if !channelIDs[channel1.ID] {
		t.Errorf("missing events from channel1")
	}
	if !channelIDs[channel2.ID] {
		t.Errorf("missing events from channel2")
	}
}

func TestListEventsByProject_CrossProjectIsolation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectA := createProject(t, s, "events-list-isolation-a")
	projectB := createProject(t, s, "events-list-isolation-b")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectA)
	createPassport(t, s, agentID, projectB)

	// Create channels in both projects
	channelA, err := s.CreateChannel(ctx, projectA, "channel-a")
	if err != nil {
		t.Fatalf("CreateChannel in A: %v", err)
	}

	channelB, err := s.CreateChannel(ctx, projectB, "channel-b")
	if err != nil {
		t.Fatalf("CreateChannel in B: %v", err)
	}

	// Publish events to project A
	payload1 := json.RawMessage(`{"task_id":"task-a1"}`)
	_, err = s.PublishEvent(ctx, projectA, channelA.ID, agentID, "task.status_changed", payload1, nil)
	if err != nil {
		t.Fatalf("PublishEvent in A: %v", err)
	}

	payload2 := json.RawMessage(`{"task_id":"task-a2"}`)
	_, err = s.PublishEvent(ctx, projectA, channelA.ID, agentID, "task.status_changed", payload2, nil)
	if err != nil {
		t.Fatalf("PublishEvent in A: %v", err)
	}

	// Publish events to project B
	payload3 := json.RawMessage(`{"task_id":"task-b1"}`)
	_, err = s.PublishEvent(ctx, projectB, channelB.ID, agentID, "task.status_changed", payload3, nil)
	if err != nil {
		t.Fatalf("PublishEvent in B: %v", err)
	}

	// List events in project A
	eventsA, err := s.ListEventsByProject(ctx, projectA, 100, 0)
	if err != nil {
		t.Fatalf("ListEventsByProject for A: %v", err)
	}

	// Should only have 2 events from project A
	if len(eventsA) != 2 {
		t.Errorf("expected 2 events in project A, got %d", len(eventsA))
	}

	// All events should belong to project A
	for _, event := range eventsA {
		if event.ProjectID != projectA {
			t.Errorf("event in project A list has ProjectID %q, want %q", event.ProjectID, projectA)
		}
	}

	// List events in project B
	eventsB, err := s.ListEventsByProject(ctx, projectB, 100, 0)
	if err != nil {
		t.Fatalf("ListEventsByProject for B: %v", err)
	}

	// Should only have 1 event from project B
	if len(eventsB) != 1 {
		t.Errorf("expected 1 event in project B, got %d", len(eventsB))
	}

	// All events should belong to project B
	for _, event := range eventsB {
		if event.ProjectID != projectB {
			t.Errorf("event in project B list has ProjectID %q, want %q", event.ProjectID, projectB)
		}
	}
}

func TestPublishEventUnknownTypeMessage(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "event-type-msg-test")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)
	channel, err := s.CreateChannel(ctx, projectID, "general")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	_, err = s.PublishEvent(ctx, projectID, channel.ID, agentID, "bogus_type", nil, nil)
	if err == nil {
		t.Fatal("expected error for unknown event_type")
	}
	want := "events: unknown event_type \"bogus_type\", valid types: task.status_changed, review.requested, build.failed, discovery.logged, message.posted"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want it to contain %q", err.Error(), want)
	}
}

func TestPublishEventMessagePostedRequiresNote(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "message-posted-note-test")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)
	channel, err := s.CreateChannel(ctx, projectID, "general")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	_, err = s.PublishEvent(ctx, projectID, channel.ID, agentID, "message.posted", nil, nil)
	if !errors.Is(err, ErrEmptyMessagePostedNote) {
		t.Fatalf("nil note: err = %v, want ErrEmptyMessagePostedNote", err)
	}

	empty := "   "
	_, err = s.PublishEvent(ctx, projectID, channel.ID, agentID, "message.posted", nil, &empty)
	if !errors.Is(err, ErrEmptyMessagePostedNote) {
		t.Fatalf("whitespace note: err = %v, want ErrEmptyMessagePostedNote", err)
	}

	real := "hello team"
	_, err = s.PublishEvent(ctx, projectID, channel.ID, agentID, "message.posted", nil, &real)
	if err != nil {
		t.Fatalf("non-empty note: unexpected error: %v", err)
	}
}

func TestPublishEventNonMessagePostedAllowsEmptyNote(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "regression-empty-note-test")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)
	channel, err := s.CreateChannel(ctx, projectID, "general")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	_, err = s.PublishEvent(ctx, projectID, channel.ID, agentID, "discovery.logged", json.RawMessage(`{"finding":"x"}`), nil)
	if err != nil {
		t.Fatalf("discovery.logged with nil note: unexpected error: %v", err)
	}
}
