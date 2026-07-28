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

// TestSyncRateLimiter_TwoDefaultGatewaysFitTask22CapacityBudget models the
// shared Fabric namespace used by the two-Gateway Task 22 run. With the
// Gateway's 10-second default pull cadence, two Gateways make 12 steady pulls
// per minute. That leaves 18 of Fabric's fixed 30 calls for lifecycle and
// write traffic; this scenario consumes 16 of those slots (two restart pulls,
// ten pushes, and four conflict reports) and still leaves two calls available.
func TestSyncRateLimiter_TwoDefaultGatewaysFitTask22CapacityBudget(t *testing.T) {
	const (
		limit                 = 30
		gatewayCount          = 2
		defaultPullInterval   = 10 * time.Second
		startupPulls          = gatewayCount
		restartPulls          = gatewayCount
		task22PushBurst       = 10
		task22ConflictReports = 4
	)
	steadyPulls := gatewayCount * int(time.Minute/defaultPullInterval)
	sharedHeadroom := limit - steadyPulls
	if steadyPulls != 12 || sharedHeadroom < 16 {
		t.Fatalf("default two-Gateway budget = %d steady pulls and %d shared calls, want 12 and at least 16", steadyPulls, sharedHeadroom)
	}
	if sharedCalls := restartPulls + task22PushBurst + task22ConflictReports; sharedCalls > sharedHeadroom {
		t.Fatalf("restart/push/conflict calls = %d exceed shared capacity %d", sharedCalls, sharedHeadroom)
	}

	rl := NewSyncRateLimiter(limit, time.Minute)
	now := time.Date(2026, time.July, 28, 13, 0, 0, 0, time.UTC)
	allow := func(label string, at time.Time) {
		t.Helper()
		if !rl.allow("task22-shared-namespace", at) {
			t.Fatalf("%s at %s unexpectedly exceeded the 30/minute namespace limit", label, at.Format(time.RFC3339))
		}
	}

	for range startupPulls {
		allow("Gateway startup pull", now)
	}
	for tick := 1; tick <= 5; tick++ { // first five of six 10-second steady ticks
		for range gatewayCount {
			allow("steady Gateway pull", now.Add(time.Duration(tick)*defaultPullInterval))
		}
	}
	for range restartPulls {
		allow("Gateway restart pull", now.Add(55*time.Second))
	}
	for range task22PushBurst {
		allow("Task 22 push burst", now.Add(59*time.Second))
	}
	for range task22ConflictReports {
		allow("Task 22 conflict report", now.Add(59*time.Second))
	}
	for range gatewayCount { // sixth steady tick; startup calls expire at this boundary
		allow("sixth steady Gateway pull", now.Add(time.Minute))
	}
	for range 2 {
		allow("remaining shared capacity", now.Add(time.Minute))
	}
	if rl.allow("task22-shared-namespace", now.Add(time.Minute)) {
		t.Fatal("31st shared namespace call was allowed; the 30/minute limiter must remain enforced")
	}
}

// TestBootstrapTool_RateLimitRejectsCleanly confirms the handler itself
// (not just the limiter struct in isolation) returns a clean error once the
// per-namespace budget is exhausted.
func TestBootstrapTool_RateLimitRejectsCleanly(t *testing.T) {
	tasksStore := testTasksStore(t)
	kbStore := testKBStore(t)
	eventsStore := testEventsStore(t)
	identityStore := testIdentityStore(t)
	projectID := mustCreateProject(t, "mcp-sync-ratelimit")
	agentID, token := mustRegisterAgent(t, projectID)
	scope, err := identityStore.WhoAmI(context.Background(), projectID, token)
	if err != nil {
		t.Fatalf("WhoAmI: %v", err)
	}
	if scope.Agent.ID != agentID {
		t.Fatalf("scope agent = %q, want %q", scope.Agent.ID, agentID)
	}
	limiter := NewSyncRateLimiter(1, time.Minute)
	tool := BootstrapTool(identityStore, tasksStore, kbStore, eventsStore, limiter)

	in := BootstrapInput{NamespaceID: projectID, Version: SyncProtocolVersion}
	argsFirst := mustMarshal(t, in)
	if _, err := tool.Handler(context.Background(), &scope, projectID, argsFirst); err != nil {
		t.Fatalf("first call: expected success, got %v", err)
	}
	if _, err := tool.Handler(context.Background(), &scope, projectID, argsFirst); err == nil {
		t.Fatalf("second call within window: expected rate-limit rejection, got nil error")
	}
}
