package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
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
	if string(event.Payload) != `{"status": "success"}` {
		t.Errorf("event.Payload = %q, want %q", string(event.Payload), `{"status": "success"}`)
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

func TestListEvents_Scoping(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "list-events-scoping")
	agentID := createAgent(t, s)
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
