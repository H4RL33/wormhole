// Package eventbus provides wormholed's in-memory pub/sub for ephemeral events
// (RFC-0003 §6.1, design brief "Local Event Bus"). Ephemeral events stay in
// memory only — presence signals, heartbeats, temporary status. They never touch
// localstore (those persist via the durable event tier).
package eventbus

import (
	"context"
	"fmt"
	"sync"
)

// Subscription identifies a single channel of event delivery. Callers receive
// events on Sub.ID and may cancel by calling Close().
type Subscription struct {
	ID        string
	ch        chan []byte // raw JSON bytes matching the event shape
	done      chan struct{}
	closeOnce sync.Once     // guards closing ch and done
}

// Close unsubscribes and releases the underlying channel. Safe to call multiple
// times; subsequent calls are no-ops (idempotent).
func (s *Subscription) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		close(s.ch)
	})
}

// Done returns a channel that is closed when the subscription is closed.
func (s *Subscription) Done() <-chan struct{} {
	return s.done
}

// Events returns the receive-only channel for this subscription.
func (s *Subscription) Events() <-chan []byte {
	return s.ch
}

// EventBus is an in-memory pub/sub hub for ephemeral events. It is safe for
// concurrent use.
type EventBus struct {
	mu         sync.RWMutex
	subscribers map[string][]*Subscription // key = namespace or event type (see Subscribe)
	nextID      int
}

// NewEventBus creates a fresh in-memory event bus.
func NewEventBus() *EventBus {
	return &EventBus{
		subscribers: make(map[string][]*Subscription),
	}
}

// Publish sends raw JSON bytes to every subscriber whose scoping matches any
// of namespace, eventType, capability, or agentID (empty dimensions are not
// matched). A subscription registered under more than one matching dimension
// still receives the payload exactly once per Publish call (Finding 3): the
// dedup is by Subscription.ID across every key visited, not per-key.
func (eb *EventBus) Publish(ctx context.Context, namespace, eventType, capability, agentID string, payload []byte) {
	eb.mu.RLock()
	defer eb.mu.RUnlock()

	seen := make(map[string]bool)
	keys := []string{namespace, eventType, capability, agentID}
	for _, key := range keys {
		if key == "" {
			continue
		}
		for _, sub := range eb.subscribers[key] {
			if seen[sub.ID] {
				continue
			}
			seen[sub.ID] = true
			select {
			case <-ctx.Done():
				return
			case sub.ch <- payload:
			default:
				// subscriber is slow — drop the message rather than blocking publishers
			}
		}
	}
}

// Subscribe registers a new subscription scoped to any combination of
// namespace, eventType, capability, and agentID. At least one dimension must
// be non-empty. The subscription receives events matching ANY of its
// non-empty dimensions (broadcast semantics), delivered exactly once per
// Publish call regardless of how many dimensions match (see Publish).
func (eb *EventBus) Subscribe(namespace, eventType, capability, agentID string) (*Subscription, error) {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	if namespace == "" && eventType == "" && capability == "" && agentID == "" {
		return nil, fmt.Errorf("eventbus: subscribe: at least one of namespace, event type, capability, or agent id must be non-empty")
	}

	sub := &Subscription{
		ID:   fmt.Sprintf("sub-%d", eb.nextID),
		ch:   make(chan []byte, 64), // bounded to avoid unbounded growth
		done: make(chan struct{}),
	}

	for _, key := range []string{namespace, eventType, capability, agentID} {
		if key != "" {
			eb.subscribers[key] = append(eb.subscribers[key], sub)
		}
	}

	eb.nextID++
	return sub, nil
}

// Unsubscribe removes a subscription from all keys it was registered under.
// Safe to call multiple times; no-op if already unsubscribed.
func (eb *EventBus) Unsubscribe(sub *Subscription) {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	for key, subs := range eb.subscribers {
		filtered := make([]*Subscription, 0, len(subs))
		for _, s := range subs {
			if s.ID != sub.ID {
				filtered = append(filtered, s)
			}
		}
		eb.subscribers[key] = filtered
	}
	sub.Close()
}

// SubscriberCount returns the total number of unique active subscriptions
// across all keys.
func (eb *EventBus) SubscriberCount() int {
	eb.mu.RLock()
	defer eb.mu.RUnlock()

	seen := make(map[string]bool, len(eb.subscribers)*2)
	for _, subs := range eb.subscribers {
		for _, sub := range subs {
			seen[sub.ID] = true
		}
	}
	return len(seen)
}
