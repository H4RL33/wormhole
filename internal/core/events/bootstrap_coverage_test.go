package events

import (
	"context"
	"encoding/json"
	"testing"
)

func TestListBootstrapReturnsDeterministicChannelsAndEvents(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "bootstrap-events")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	channelB, err := s.CreateChannel(ctx, projectID, "zeta")
	if err != nil {
		t.Fatal(err)
	}
	channelA, err := s.CreateChannel(ctx, projectID, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	note := "durable note"
	event, err := s.PublishEvent(ctx, projectID, channelB.ID, agentID, "discovery.logged", json.RawMessage(`{"kind":"coverage"}`), &note)
	if err != nil {
		t.Fatal(err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectID); err != nil {
		t.Fatal(err)
	}
	channels, events, err := s.ListBootstrapInTx(ctx, tx, projectID)
	if err != nil {
		t.Fatalf("ListBootstrapInTx: %v", err)
	}
	if len(channels) != 2 || channels[0].ID != channelA.ID || channels[1].ID != channelB.ID {
		t.Fatalf("channels = %#v, want alpha then zeta", channels)
	}
	if len(events) != 1 || events[0].ID != event.ID || events[0].Note == nil || *events[0].Note != note {
		t.Fatalf("events = %#v, want published event", events)
	}
}

func TestListBootstrapRejectsInvalidScope(t *testing.T) {
	s := testStore(t)
	if _, _, err := s.ListBootstrapInTx(context.Background(), nil, "project"); err == nil {
		t.Fatal("nil transaction unexpectedly accepted")
	}
}
