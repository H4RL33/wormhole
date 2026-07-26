package events

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestEventOperationsPropagateCanceledContext(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	operations := map[string]func() error{
		"ensure channel": func() error {
			_, err := s.EnsureChannel(ctx, uuid.NewString(), "general")
			return err
		},
		"create channel": func() error {
			_, err := s.CreateChannel(ctx, uuid.NewString(), "general")
			return err
		},
		"list channels": func() error {
			_, err := s.ListChannels(ctx, uuid.NewString())
			return err
		},
		"get channel": func() error {
			_, err := s.GetChannel(ctx, uuid.NewString(), uuid.NewString())
			return err
		},
		"publish event": func() error {
			_, err := s.PublishEvent(ctx, uuid.NewString(), uuid.NewString(), uuid.NewString(), "build.failed", nil, nil)
			return err
		},
		"publish event with id": func() error {
			_, err := s.PublishEventWithID(ctx, uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), "build.failed", nil, nil)
			return err
		},
		"list events": func() error {
			_, err := s.ListEvents(ctx, uuid.NewString(), uuid.NewString(), 10, 0)
			return err
		},
		"list project events": func() error {
			_, err := s.ListEventsByProject(ctx, uuid.NewString(), 10, 0)
			return err
		},
	}

	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			if err := operation(); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
		})
	}
}

func TestPublishEventRejectsUnknownChannelWithoutWriting(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "publish-unknown-channel")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	_, err := s.PublishEvent(ctx, projectID, uuid.NewString(), agentID, "build.failed", json.RawMessage(`{"build":"failed"}`), nil)
	if !errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("PublishEvent error = %v, want ErrChannelNotFound", err)
	}

	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE project_id = $1`, projectID).Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != 0 {
		t.Fatalf("event count = %d, want 0", count)
	}
}

func TestListEventsRejectsUnknownChannel(t *testing.T) {
	s := testStore(t)
	projectID := createProject(t, s, "list-unknown-channel")

	_, err := s.ListEvents(context.Background(), projectID, uuid.NewString(), 10, 0)
	if !errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("ListEvents error = %v, want ErrChannelNotFound", err)
	}
}

func TestPublishEventWithIDDuplicateRollsBack(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "duplicate-event-id")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)
	channel, err := s.CreateChannel(ctx, projectID, "builds")
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	id := uuid.NewString()

	if _, err := s.PublishEventWithID(ctx, id, projectID, channel.ID, agentID, "build.failed", nil, nil); err != nil {
		t.Fatalf("first PublishEventWithID: %v", err)
	}
	replayed, err := s.PublishEventWithID(ctx, id, projectID, channel.ID, agentID, "build.failed", nil, nil)
	if err != nil {
		t.Fatalf("identical PublishEventWithID replay: %v", err)
	}
	if replayed.ID != id || replayed.EventType != "build.failed" {
		t.Fatalf("identical replay = %+v, want original event", replayed)
	}
	if _, err := s.PublishEventWithID(ctx, id, projectID, channel.ID, agentID, "discovery.logged", nil, nil); !errors.Is(err, ErrStableIDConflict) || !strings.Contains(err.Error(), "publish event") {
		t.Fatalf("mismatched PublishEventWithID error = %v, want ErrStableIDConflict", err)
	}

	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE id = $1`, id).Scan(&count); err != nil {
		t.Fatalf("count duplicate event id: %v", err)
	}
	if count != 1 {
		t.Fatalf("event count = %d, want 1", count)
	}
}

func TestCreateChannelWithIDReplayRequiresIdenticalContent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "channel-id-replay")
	id := uuid.NewString()
	if _, err := s.CreateChannelWithID(ctx, id, projectID, "general"); err != nil {
		t.Fatalf("first CreateChannelWithID: %v", err)
	}
	replayed, err := s.CreateChannelWithID(ctx, id, projectID, "general")
	if err != nil {
		t.Fatalf("identical CreateChannelWithID replay: %v", err)
	}
	if replayed.ID != id || replayed.Name != "general" {
		t.Fatalf("identical replay = %+v, want original channel", replayed)
	}
	if _, err := s.CreateChannelWithID(ctx, id, projectID, "changed"); !errors.Is(err, ErrStableIDConflict) || !strings.Contains(err.Error(), "create channel") {
		t.Fatalf("mismatched CreateChannelWithID error = %v, want ErrStableIDConflict", err)
	}
}

func TestCreateChannelDuplicateNamePreservesOriginal(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "duplicate-channel-name")
	original, err := s.CreateChannel(ctx, projectID, "general")
	if err != nil {
		t.Fatalf("first CreateChannel: %v", err)
	}

	if _, err := s.CreateChannel(ctx, projectID, "general"); err == nil || !strings.Contains(err.Error(), "create channel") {
		t.Fatalf("duplicate CreateChannel error = %v, want wrapped uniqueness error", err)
	}
	channels, err := s.ListChannels(ctx, projectID)
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if len(channels) != 1 || channels[0].ID != original.ID {
		t.Fatalf("channels after duplicate = %+v, want original only", channels)
	}
}

func TestEnsureChannelInTxPropagatesQueryFailure(t *testing.T) {
	s := testStore(t)
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = s.EnsureChannelInTx(ctx, tx, uuid.NewString(), "general")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("EnsureChannelInTx error = %v, want context.Canceled", err)
	}
}
