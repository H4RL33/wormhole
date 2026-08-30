package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"testing"
	"time"

	coregit "github.com/H4RL33/wormhole/internal/core/git"
	"github.com/H4RL33/wormhole/internal/core/identity"
)

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	tool := Tool{
		Name:         "wormhole.agent.whoami",
		Description:  "test tool",
		RequiresAuth: true,
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			return "ok", nil
		},
	}
	r.Register(tool)

	got, ok := r.Get("wormhole.agent.whoami")
	if !ok {
		t.Fatalf("Get: tool not found")
	}
	if got.Name != tool.Name || got.RequiresAuth != tool.RequiresAuth {
		t.Fatalf("Get: got %+v, want matching Name/RequiresAuth of %+v", got, tool)
	}
	if got.Handler == nil {
		t.Fatalf("Get: Handler is nil")
	}
}

func TestFabricRegistryRetainsExactPrivateSixteen(t *testing.T) {
	want := []string{
		"wormhole.agent.enrol", "wormhole.agent.whoami",
		"wormhole.channel.create", "wormhole.channel.list", "wormhole.channel.post", "wormhole.channel.subscribe",
		"wormhole.git.link_commit", "wormhole.git.request_review",
		"wormhole.kb.get", "wormhole.kb.get_links", "wormhole.kb.search", "wormhole.kb.write",
		"wormhole.task.assign", "wormhole.task.create", "wormhole.task.list", "wormhole.task.update_status",
	}
	registry := NewFabricRegistry(FabricRegistryDependencies{})
	got := make([]string, 0, len(registry.List()))
	for _, tool := range registry.List() {
		got = append(got, tool.Name)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("private Fabric tools = %q, want %q", got, want)
	}
	for _, descriptor := range PublicFabricToolDescriptors() {
		if _, live := registry.Get(descriptor.Name); live {
			t.Fatalf("public descriptor %q is live before production assembly", descriptor.Name)
		}
	}
}

func TestPublicFabricRegistryExposesOnlyCompletedSyncV2Handlers(t *testing.T) {
	registry := NewPublicFabricRegistry(readyPublicRegistryDependencies(t))
	want := []string{"wormhole.sync.attach", "wormhole.sync.bootstrap", "wormhole.sync.conflict", "wormhole.sync.pull", "wormhole.sync.push"}
	got := make([]string, 0, len(registry.List()))
	for _, tool := range registry.List() {
		got = append(got, tool.Name)
		if tool.Handler != nil || tool.PublicHandler == nil || tool.RequiresAuth {
			t.Fatalf("public tool %q has private dispatch metadata: %+v", tool.Name, tool)
		}
		if len(tool.ArgumentVariants) != 1 || tool.ArgumentVariants[2] == nil {
			t.Fatalf("public tool %q argument variants = %#v, want v2 only", tool.Name, tool.ArgumentVariants)
		}
		if len(tool.ResultVariants) != 1 || len(tool.ResultVariants[2]) == 0 {
			t.Fatalf("public tool %q result variants = %#v, want v2 only", tool.Name, tool.ResultVariants)
		}
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("public Fabric tools = %q, want %q", got, want)
	}
	for _, descriptor := range PublicFabricToolDescriptors() {
		_, live := registry.Get(descriptor.Name)
		wantLive := descriptor.Name == "wormhole.sync.attach" || descriptor.Name == "wormhole.sync.bootstrap" || descriptor.Name == "wormhole.sync.conflict" || descriptor.Name == "wormhole.sync.pull" || descriptor.Name == "wormhole.sync.push"
		if live != wantLive {
			t.Fatalf("public descriptor %q live = %v, want %v", descriptor.Name, live, wantLive)
		}
	}
}

func TestPublicFabricRegistryRequiresEachCompleteHandler(t *testing.T) {
	if got := len(NewPublicFabricRegistry(PublicFabricRegistryDependencies{}).List()); got != 0 {
		t.Fatalf("empty public dependencies registered %d tools, want 0", got)
	}
	for name, dependencies := range map[string]PublicFabricRegistryDependencies{
		"attach":    {Attach: &SyncV2AttachHandler{}},
		"bootstrap": {Bootstrap: &SyncV2BootstrapHandler{}},
		"conflict":  {Conflict: &SyncV2ConflictHandler{}},
		"pull":      {Pull: &SyncV2PullHandler{}},
		"push":      {Push: &SyncV2PushHandler{}},
	} {
		t.Run("incomplete "+name, func(t *testing.T) {
			if got := len(NewPublicFabricRegistry(dependencies).List()); got != 0 {
				t.Fatalf("incomplete non-nil %s registered %d tools, want 0", name, got)
			}
		})
	}
	ready := readyPublicRegistryDependencies(t)
	registry := NewPublicFabricRegistry(PublicFabricRegistryDependencies{Pull: ready.Pull})
	if got := len(registry.List()); got != 1 {
		t.Fatalf("pull-only public dependencies registered %d tools, want 1", got)
	}
	if _, ok := registry.Get("wormhole.sync.pull"); !ok {
		t.Fatal("pull-only public dependencies did not register wormhole.sync.pull")
	}
}

func TestPublicFabricRegistryPushHasExactTwoResultVariants(t *testing.T) {
	registry := NewPublicFabricRegistry(readyPublicRegistryDependencies(t))
	push, ok := registry.Get("wormhole.sync.push")
	if !ok {
		t.Fatal("wormhole.sync.push is not live")
	}
	wantPush := []any{SyncPushAppliedV2Result{}, SyncPushConflictV2Result{}}
	if len(push.ResultVariants) != 1 || !reflect.DeepEqual(push.ResultVariants[2], wantPush) {
		t.Fatalf("push result variants = %#v, want %#v", push.ResultVariants, map[int][]any{2: wantPush})
	}
	for _, name := range []string{"wormhole.sync.attach", "wormhole.sync.bootstrap", "wormhole.sync.conflict", "wormhole.sync.pull"} {
		tool, ok := registry.Get(name)
		if !ok || len(tool.ResultVariants) != 1 || len(tool.ResultVariants[2]) != 1 || tool.ResultVariants[2][0] == nil {
			t.Fatalf("%s result variants = %#v, want one non-nil v2 result", name, tool.ResultVariants)
		}
	}
}

func TestPublicFabricRegistryRejectsAttachVerifierForWrongFabricOrWithoutClock(t *testing.T) {
	for name, verifier := range map[string]*PublicProofVerifier{
		"wrong Fabric": {fabricInstanceID: "11111111-1111-4111-8111-111111111112", now: func() time.Time { return time.Now().UTC() }},
		"zero value":   {},
	} {
		t.Run(name, func(t *testing.T) {
			ready := readyPublicRegistryDependencies(t)
			ready.Attach.verifier = verifier
			registry := NewPublicFabricRegistry(PublicFabricRegistryDependencies{Attach: ready.Attach})
			if got := len(registry.List()); got != 0 {
				t.Fatalf("invalid attach verifier registered %d tools, want 0", got)
			}
			if _, ok := registry.Get("wormhole.sync.attach"); ok {
				t.Fatal("invalid attach verifier registered wormhole.sync.attach")
			}
		})
	}
}

func TestPublicFabricRegistrySchemasMatchFrozenDescriptors(t *testing.T) {
	registry := NewPublicFabricRegistry(readyPublicRegistryDependencies(t))
	descriptors := make(map[string]ToolDescriptor)
	for _, descriptor := range PublicFabricToolDescriptors() {
		descriptors[descriptor.Name] = descriptor
	}
	for _, entry := range HandleToolsList(registry).(map[string]any)["tools"].([]toolListEntry) {
		got, err := json.Marshal(entry.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		want, err := json.Marshal(descriptors[entry.Name].InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s public input schema drift\ngot:  %s\nwant: %s", entry.Name, got, want)
		}
		for _, forbidden := range []string{"project_id", "workspace_id", "fabric_instance_id", "remote_project_id", "stream_id", "actor_scope"} {
			if bytes.Contains(got, []byte(`"`+forbidden+`"`)) {
				t.Fatalf("%s public schema exposes forbidden routing field %q: %s", entry.Name, forbidden, got)
			}
		}
	}
}

func readyPublicRegistryDependencies(t *testing.T) PublicFabricRegistryDependencies {
	t.Helper()
	fabricID := "11111111-1111-4111-8111-111111111111"
	verifier, err := NewPublicProofVerifier(fabricID, func() time.Time { return time.Now().UTC() })
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewPublicBoundProofResolver(fabricID, identity.NewStore(nil), coregit.NewStreamStore(nil), verifier)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewMutationCoordinator(identity.NewStore(nil), coregit.NewStreamStore(nil), coregit.NewActivityStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	attach, err := NewSyncV2AttachHandler(fabricID, "test-observer", &coregit.FakeObserver{}, coordinator, staticAttachPolicySource{}, verifier)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := NewSyncV2BootstrapHandler(resolver, coregit.NewActivityStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	pull, err := NewSyncV2PullHandler(resolver)
	if err != nil {
		t.Fatal(err)
	}
	push, err := NewSyncV2PushHandler(resolver, coordinator, coregit.NewStreamStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	conflict, err := NewSyncV2ConflictHandler(resolver, coordinator, coregit.NewStreamStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	return PublicFabricRegistryDependencies{Attach: attach, Bootstrap: bootstrap, Pull: pull, Push: push, Conflict: conflict}
}

func TestRegistry_GetMissing(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Get("wormhole.agent.nonexistent"); ok {
		t.Fatalf("Get: expected ok=false for unregistered tool")
	}
}

func TestTool_JSONSerialization(t *testing.T) {
	tool := Tool{
		Name:         "wormhole.agent.whoami",
		Description:  "test tool",
		RequiresAuth: true,
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			return "ok", nil
		},
	}

	data, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("failed to marshal tool: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal tool json: %v", err)
	}

	if parsed["name"] != "wormhole.agent.whoami" {
		t.Errorf("expected name to be 'wormhole.agent.whoami', got '%v'", parsed["name"])
	}
	if parsed["description"] != "test tool" {
		t.Errorf("expected description to be 'test tool', got '%v'", parsed["description"])
	}
	if parsed["requires_auth"] != true {
		t.Errorf("expected requires_auth to be true, got '%v'", parsed["requires_auth"])
	}
	if _, exists := parsed["Handler"]; exists {
		t.Errorf("Handler field should not be serialized")
	}
	if _, exists := parsed["handler"]; exists {
		t.Errorf("handler field should not be serialized")
	}
}

func TestRegistry_EveryAuthedToolDeclaresPermission(t *testing.T) {
	exempt := map[string]bool{"wormhole.agent.whoami": true}
	registry := NewFabricRegistry(FabricRegistryDependencies{})
	for _, tool := range registry.List() {
		if !tool.RequiresAuth {
			if tool.RequiredPermission != "" {
				t.Errorf("%s: RequiresAuth=false but RequiredPermission=%q; unauthenticated tools cannot gate on a permission", tool.Name, tool.RequiredPermission)
			}
			continue
		}
		if exempt[tool.Name] {
			if tool.RequiredPermission != "" {
				t.Errorf("%s: exempt tool must have empty RequiredPermission, got %q", tool.Name, tool.RequiredPermission)
			}
			continue
		}
		if tool.RequiredPermission == "" {
			t.Errorf("%s: authenticated tool must declare a RequiredPermission", tool.Name)
		}
	}
}
