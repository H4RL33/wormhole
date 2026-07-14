package eventbus

import (
	"context"
	"testing"
	"time"
)

func TestSubscribeAndPublish(t *testing.T) {
	bus := NewEventBus()
	ctx := context.Background()

	sub, err := bus.Subscribe("ns-1", "presence.online", "", "")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()

	bus.Publish(ctx, "ns-1", "presence.online", "", "", []byte(`{"agent":"a1","status":"online"}`))

	payload := <-sub.Events()
	if string(payload) != `{"agent":"a1","status":"online"}` {
		t.Fatalf("got %s, want {\"agent\":\"a1\",\"status\":\"online\"}", string(payload))
	}
}

func TestSubscribeEventTypeBroadcast(t *testing.T) {
	bus := NewEventBus()
	ctx := context.Background()

	// Subscribe to namespace only.
	nsSub, err := bus.Subscribe("ns-1", "", "", "")
	if err != nil {
		t.Fatalf("subscribe ns: %v", err)
	}
	defer nsSub.Close()

	// Subscribe to event type only.
	typeSub, err := bus.Subscribe("", "heartbeat", "", "")
	if err != nil {
		t.Fatalf("subscribe type: %v", err)
	}
	defer typeSub.Close()

	// Publish to namespace — should reach nsSub but NOT typeSub (different key).
	bus.Publish(ctx, "ns-1", "", "", "", []byte(`{"msg":"ns-only"}`))
	if payload := <-nsSub.Events(); string(payload) != `{"msg":"ns-only"}` {
		t.Fatalf("nsSub got %s", string(payload))
	}

	// Publish to event type — should reach typeSub but NOT nsSub.
	bus.Publish(ctx, "", "heartbeat", "", "", []byte(`{"msg":"hb"}`))
	if payload := <-typeSub.Events(); string(payload) != `{"msg":"hb"}` {
		t.Fatalf("typeSub got %s", string(payload))
	}
}

func TestUnsubscribe(t *testing.T) {
	bus := NewEventBus()
	ctx := context.Background()

	sub, err := bus.Subscribe("ns-1", "presence.online", "", "")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if bus.SubscriberCount() != 1 { // single unique subscriber (registered under both keys, but counts as one)
		t.Fatalf("SubscriberCount after subscribe: %d, want 1", bus.SubscriberCount())
	}

	bus.Unsubscribe(sub)
	if bus.SubscriberCount() != 0 {
		t.Fatalf("SubscriberCount after unsubscribe: %d, want 0", bus.SubscriberCount())
	}

	// After unsubscribe, publishing should not block (channel drained).
	bus.Publish(ctx, "ns-1", "presence.online", "", "", []byte(`{"msg":"after"}`))
}

func TestSubscribeEmptyKeyFails(t *testing.T) {
	bus := NewEventBus()
	_, err := bus.Subscribe("", "", "", "")
	if err == nil {
		t.Fatal("expected error for empty subscribe")
	}
}

func TestSubscriberCount(t *testing.T) {
	bus := NewEventBus()
	if bus.SubscriberCount() != 0 {
		t.Fatalf("initial count: %d, want 0", bus.SubscriberCount())
	}

	sub1, _ := bus.Subscribe("ns-1", "", "", "")
	sub2, _ := bus.Subscribe("ns-1", "presence.online", "", "")
	if bus.SubscriberCount() != 2 { // two unique subscribers
		t.Fatalf("count: %d, want 2", bus.SubscriberCount())
	}

	bus.Unsubscribe(sub1)
	if bus.SubscriberCount() != 1 {
		t.Fatalf("count after unsubscribe: %d, want 1", bus.SubscriberCount())
	}

	bus.Unsubscribe(sub2)
	if bus.SubscriberCount() != 0 {
		t.Fatalf("count after unsubscribe: %d, want 0", bus.SubscriberCount())
	}
}

func TestDuplicateSubscribeSameAgent(t *testing.T) {
	bus := NewEventBus()
	ctx := context.Background()

	sub1, _ := bus.Subscribe("ns-1", "presence.online", "", "")
	sub2, _ := bus.Subscribe("ns-1", "presence.online", "", "")
	defer sub1.Close()
	defer sub2.Close()

	bus.Publish(ctx, "ns-1", "presence.online", "", "", []byte(`{"agent":"a1"}`))

	// Both subs should receive the message.
	p1 := <-sub1.Events()
	p2 := <-sub2.Events()
	if string(p1) != `{"agent":"a1"}` || string(p2) != `{"agent":"a1"}` {
		t.Fatalf("p1=%s, p2=%s", string(p1), string(p2))
	}
}

// TestSubscriptionScopedToBothDimensionsDeliversOnce proves a single
// subscription registered under both namespace AND event type (i.e. two
// matching keys for one Publish call) receives the payload exactly once,
// not twice (Finding 3: Publish previously double-delivered in this case).
func TestSubscriptionScopedToBothDimensionsDeliversOnce(t *testing.T) {
	bus := NewEventBus()
	ctx := context.Background()

	sub, err := bus.Subscribe("ns-1", "presence.online", "", "")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()

	// Both namespace and event type match this one subscription.
	bus.Publish(ctx, "ns-1", "presence.online", "", "", []byte(`{"agent":"a1"}`))

	select {
	case payload := <-sub.Events():
		if string(payload) != `{"agent":"a1"}` {
			t.Fatalf("got %s", string(payload))
		}
	default:
		t.Fatal("expected exactly one delivery, got none")
	}

	// A second read must NOT find a duplicate delivery.
	select {
	case payload := <-sub.Events():
		t.Fatalf("got a second delivery (double-delivery bug): %s", string(payload))
	default:
		// correct: no second message
	}
}

func TestSlowSubscriberDoesNotBlock(t *testing.T) {
	bus := NewEventBus()
	ctx := context.Background()

	sub, err := bus.Subscribe("ns-1", "heartbeat", "", "")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()

	done := make(chan struct{})
	go func() {
		// Fill the buffer (64) then publish one more — that last publish should
		// drop silently (non-blocking), not block the goroutine.
		for i := 0; i < 64; i++ {
			bus.Publish(ctx, "ns-1", "heartbeat", "", "", []byte(`{}`))
		}
		bus.Publish(ctx, "ns-1", "heartbeat", "", "", []byte(`{"dropped":true}`))

		// Consume the one that actually arrived (buffer slot 0).
		<-sub.Events()

		bus.Publish(ctx, "ns-1", "heartbeat", "", "", []byte(`{"after_drop":true}`))

		done <- struct{}{}
	}()

	select {
	case <-done:
		// success — publisher didn't block on drop
	case <-ctx.Done():
		t.Fatal("publisher blocked")
	}
}

func TestConcurrentPublishSubscribe(t *testing.T) {
	bus := NewEventBus()

	sub, err := bus.Subscribe("ns-conc", "presence.online", "", "")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	done := make(chan struct{})
	go func() {
		for i := 0; i < 20; i++ {
			bus.Publish(context.Background(), "ns-conc", "presence.online", "", "", []byte(`{"i":1}`))
		}
		bus.Unsubscribe(sub)
		done <- struct{}{}
	}()

	select {
	case <-done:
		// publisher completed — no deadlock
	case <-time.After(500 * time.Millisecond):
		t.Fatal("publish loop deadlocked")
	}
}
