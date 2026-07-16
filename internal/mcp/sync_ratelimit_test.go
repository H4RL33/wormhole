package mcp

import (
	"context"
	"testing"
	"time"
)

// TestSyncRateLimiter_AllowsUpToLimit confirms exactly `limit` calls within
// `window` succeed and the next one is rejected (P6 hardening: rate
// limiting on wormhole.sync.* handlers, previously deferred to beta).
func TestSyncRateLimiter_AllowsUpToLimit(t *testing.T) {
	rl := NewSyncRateLimiter(3, time.Minute)
	now := time.Now()

	for i := 0; i < 3; i++ {
		if !rl.allow("ns-1", now) {
			t.Fatalf("call %d: expected allowed", i)
		}
	}
	if rl.allow("ns-1", now) {
		t.Fatalf("4th call within window: expected rejected")
	}
}

// TestSyncRateLimiter_NamespacesIndependent confirms one namespace hitting
// its limit does not affect another namespace's budget.
func TestSyncRateLimiter_NamespacesIndependent(t *testing.T) {
	rl := NewSyncRateLimiter(1, time.Minute)
	now := time.Now()

	if !rl.allow("ns-1", now) {
		t.Fatalf("ns-1 first call: expected allowed")
	}
	if rl.allow("ns-1", now) {
		t.Fatalf("ns-1 second call: expected rejected")
	}
	if !rl.allow("ns-2", now) {
		t.Fatalf("ns-2 first call: expected allowed despite ns-1 exhausted")
	}
}

// TestSyncRateLimiter_WindowExpires confirms a call outside the window no
// longer counts against the limit.
func TestSyncRateLimiter_WindowExpires(t *testing.T) {
	rl := NewSyncRateLimiter(1, time.Minute)
	now := time.Now()

	if !rl.allow("ns-1", now) {
		t.Fatalf("first call: expected allowed")
	}
	later := now.Add(2 * time.Minute)
	if !rl.allow("ns-1", later) {
		t.Fatalf("call after window expiry: expected allowed")
	}
}

// TestBootstrapTool_RateLimitRejectsCleanly confirms the handler itself
// (not just the limiter struct in isolation) returns a clean error once the
// per-namespace budget is exhausted.
func TestBootstrapTool_RateLimitRejectsCleanly(t *testing.T) {
	tasksStore := testTasksStore(t)
	kbStore := testKBStore(t)
	eventsStore := testEventsStore(t)
	projectID := mustCreateProject(t, "mcp-sync-ratelimit")
	limiter := NewSyncRateLimiter(1, time.Minute)
	tool := BootstrapTool(tasksStore, kbStore, eventsStore, limiter)

	in := BootstrapInput{NamespaceID: projectID, Version: SyncProtocolVersion}
	argsFirst := mustMarshal(t, in)
	if _, err := tool.Handler(context.Background(), nil, projectID, argsFirst); err != nil {
		t.Fatalf("first call: expected success, got %v", err)
	}
	if _, err := tool.Handler(context.Background(), nil, projectID, argsFirst); err == nil {
		t.Fatalf("second call within window: expected rate-limit rejection, got nil error")
	}
}
