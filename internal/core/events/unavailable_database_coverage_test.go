package events

import (
	"context"
	"encoding/json"
	"testing"
)

func TestEventStoreOperationsFailClosedWhenDatabaseIsUnavailable(t *testing.T) {
	s := testStore(t)
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := "00000000-0000-0000-0000-000000000001"
	checks := []struct {
		name string
		call func() error
	}{
		{"create channel", func() error { _, err := s.CreateChannel(ctx, id, "general"); return err }},
		{"ensure channel", func() error { _, err := s.EnsureChannel(ctx, id, "general"); return err }},
		{"create channel with id", func() error { _, err := s.CreateChannelWithID(ctx, id, id, "general"); return err }},
		{"list channels", func() error { _, err := s.ListChannels(ctx, id); return err }},
		{"get channel", func() error { _, err := s.GetChannel(ctx, id, id); return err }},
		{"publish event", func() error {
			_, err := s.PublishEvent(ctx, id, id, id, "message", json.RawMessage(`{}`), nil)
			return err
		}},
		{"publish event with id", func() error {
			_, err := s.PublishEventWithID(ctx, id, id, id, id, "message", json.RawMessage(`{}`), nil)
			return err
		}},
		{"list events", func() error { _, err := s.ListEvents(ctx, id, id, 10, 0); return err }},
		{"list project events", func() error { _, err := s.ListEventsByProject(ctx, id, 10, 0); return err }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); err == nil {
				t.Fatal("operation succeeded with a closed database")
			}
		})
	}
}
