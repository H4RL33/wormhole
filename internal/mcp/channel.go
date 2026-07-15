package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/H4RL33/wormhole/internal/core/events"
	"github.com/H4RL33/wormhole/internal/core/identity"
)

// CreateChannelInput is the wormhole.channel.create argument shape.
type CreateChannelInput struct {
	ProjectID string `json:"project_id,omitempty"`
	Name      string `json:"name"`
}

// CreateChannelOutput is the wormhole.channel.create result shape.
type CreateChannelOutput struct {
	ChannelID string    `json:"channel_id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateChannelTool wires wormhole.channel.create.
func CreateChannelTool(store *events.Store) Tool {
	return Tool{
		Name:             "wormhole.channel.create",
		Description:      "Creates a new event channel within the project.",
		RequiresAuth:     true,
		ArgumentsExample: CreateChannelInput{},
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			var in CreateChannelInput
			if err := json.Unmarshal(arguments, &in); err != nil {
				return nil, fmt.Errorf("mcp: decode wormhole.channel.create arguments: %w", err)
			}
			if in.ProjectID != "" && in.ProjectID != projectID {
				return nil, fmt.Errorf("mcp: project_id mismatch: got %q, authenticated as %q", in.ProjectID, projectID)
			}
			channel, err := store.CreateChannel(ctx, projectID, in.Name)
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.channel.create: %w", err)
			}
			return CreateChannelOutput{
				ChannelID: channel.ID,
				ProjectID: channel.ProjectID,
				Name:      channel.Name,
				CreatedAt: channel.CreatedAt,
			}, nil
		},
	}
}

// PostEventInput is the wormhole.channel.post argument shape.
type PostEventInput struct {
	ChannelID string          `json:"channel_id"`
	EventType string          `json:"event_type" enum:"task.status_changed,review.requested,build.failed,discovery.logged,message.posted"`
	Payload   json.RawMessage `json:"payload"`
	Note      *string         `json:"note"`
}

// PostEventOutput is the wormhole.channel.post result shape.
type PostEventOutput struct {
	EventID   string    `json:"event_id"`
	ProjectID string    `json:"project_id"`
	ChannelID string    `json:"channel_id"`
	AgentID   string    `json:"agent_id"`
	EventType string    `json:"event_type"`
	CreatedAt time.Time `json:"created_at"`
}

// PostEventTool wires wormhole.channel.post. The authenticated agent's ID is
// passed as agentID to the store so the event is attributed correctly.
func PostEventTool(store *events.Store) Tool {
	return Tool{
		Name:             "wormhole.channel.post",
		Description:      "Publishes an event to a project channel. The calling agent is recorded as the author.",
		RequiresAuth:     true,
		ArgumentsExample: PostEventInput{},
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			var in PostEventInput
			if err := json.Unmarshal(arguments, &in); err != nil {
				return nil, fmt.Errorf("mcp: decode wormhole.channel.post arguments: %w", err)
			}
			event, err := store.PublishEvent(ctx, projectID, in.ChannelID, scope.Agent.ID, in.EventType, in.Payload, in.Note)
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.channel.post: %w", err)
			}
			return PostEventOutput{
				EventID:   event.ID,
				ProjectID: event.ProjectID,
				ChannelID: event.ChannelID,
				AgentID:   event.AgentID,
				EventType: event.EventType,
				CreatedAt: event.CreatedAt,
			}, nil
		},
	}
}

// ListChannelsInput is the wormhole.channel.list argument shape. No fields:
// project scoping comes from the authenticated call, matching
// wormhole.task.list's pattern of implicit project scoping.
type ListChannelsInput struct{}

// ChannelSummary is one channel's shape within ListChannelsOutput.
type ChannelSummary struct {
	ChannelID string    `json:"channel_id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// ListChannelsOutput is the wormhole.channel.list result shape.
type ListChannelsOutput struct {
	Channels []ChannelSummary `json:"channels"`
}

// ListChannelsTool wires wormhole.channel.list.
func ListChannelsTool(store *events.Store) Tool {
	return Tool{
		Name:             "wormhole.channel.list",
		Description:      "Lists the event channels within the project.",
		RequiresAuth:     true,
		ArgumentsExample: ListChannelsInput{},
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			channelList, err := store.ListChannels(ctx, projectID)
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.channel.list: %w", err)
			}
			out := ListChannelsOutput{Channels: make([]ChannelSummary, 0, len(channelList))}
			for _, c := range channelList {
				out.Channels = append(out.Channels, ChannelSummary{
					ChannelID: c.ID,
					ProjectID: c.ProjectID,
					Name:      c.Name,
					CreatedAt: c.CreatedAt,
				})
			}
			return out, nil
		},
	}
}

// SubscribeChannelInput is the wormhole.channel.subscribe argument shape.
// Limit and Offset default to 50 and 0 respectively when absent or zero.
type SubscribeChannelInput struct {
	ChannelID string `json:"channel_id"`
	Limit     int    `json:"limit,omitempty"`
	Offset    int    `json:"offset,omitempty"`
}

// EventSummary is one event's shape within SubscribeChannelOutput.
type EventSummary struct {
	EventID   string          `json:"event_id"`
	ChannelID string          `json:"channel_id"`
	AgentID   string          `json:"agent_id"`
	EventType string          `json:"event_type"`
	Payload   json.RawMessage `json:"payload"`
	Note      *string         `json:"note"`
	CreatedAt time.Time       `json:"created_at"`
}

// SubscribeChannelOutput is the wormhole.channel.subscribe result shape.
type SubscribeChannelOutput struct {
	Events []EventSummary `json:"events"`
}

// SubscribeChannelTool wires wormhole.channel.subscribe. Defaults: limit=50,
// offset=0 when those fields are absent or zero in the input.
func SubscribeChannelTool(store *events.Store) Tool {
	return Tool{
		Name:             "wormhole.channel.subscribe",
		Description:      "Returns a page of events from a project channel, ordered oldest-first.",
		RequiresAuth:     true,
		ArgumentsExample: SubscribeChannelInput{},
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			var in SubscribeChannelInput
			if err := json.Unmarshal(arguments, &in); err != nil {
				return nil, fmt.Errorf("mcp: decode wormhole.channel.subscribe arguments: %w", err)
			}
			if in.Limit == 0 {
				in.Limit = 50
			}
			// in.Offset is already 0 when absent, which is the desired default.
			eventList, err := store.ListEvents(ctx, projectID, in.ChannelID, in.Limit, in.Offset)
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.channel.subscribe: %w", err)
			}
			out := SubscribeChannelOutput{Events: make([]EventSummary, 0, len(eventList))}
			for _, e := range eventList {
				out.Events = append(out.Events, EventSummary{
					EventID:   e.ID,
					ChannelID: e.ChannelID,
					AgentID:   e.AgentID,
					EventType: e.EventType,
					Payload:   e.Payload,
					Note:      e.Note,
					CreatedAt: e.CreatedAt,
				})
			}
			return out, nil
		},
	}
}
