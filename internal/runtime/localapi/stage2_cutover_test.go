package localapi

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

var stage2FinalGatewayToolNames = []string{
	"wormhole.sync.status",
	"wormhole.workspace.status",
	"wormhole.workspace.diff",
	"wormhole.workspace.import",
	"wormhole.workspace.checkpoint",
	"wormhole.workspace.stash",
	"wormhole.channel.list",
	"wormhole.channel.create",
	"wormhole.channel.events",
	"wormhole.channel.post",
	"wormhole.channel.subscribe",
	"wormhole.kb.list",
	"wormhole.kb.get",
	"wormhole.kb.write",
	"wormhole.agent.register",
	"wormhole.agent.presence",
	"wormhole.agent.list",
}

func TestStage2FinalPublicMCPInventory(t *testing.T) {
	registry := newLocalRegistry(&Server{})
	gotNames := make([]string, 0, len(registry.List()))
	for _, tool := range registry.List() {
		gotNames = append(gotNames, tool.Name)
	}
	if !reflect.DeepEqual(gotNames, stage2FinalGatewayToolNames) {
		t.Fatalf("Stage 2 Gateway registry = %v, want exact truthful 17-tool inventory %v", gotNames, stage2FinalGatewayToolNames)
	}

	for _, tool := range registry.List() {
		if err := authorizePrivateToolProvider(tool, nil); err != nil {
			t.Errorf("registered configured tool %q rejected by provider selection: %v", tool.Name, err)
		}
		lower := strings.ToLower(tool.Name + " " + tool.Description)
		for _, forbidden := range []string{"code" + "_graph", "code" + "-graph", "code" + " graph"} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("public MCP surface retains %q in %q", forbidden, lower)
			}
		}
	}

}

func TestStage2ConfiguredSyncStatusReportsQueueFreeLocalOnlyState(t *testing.T) {
	first, second := privateDispatchSiblingBindings(t)
	server := privateDispatchTestServer(t, privateRoutingTestActor("00000000-0000-4000-8000-000000000021"), first, second)

	status := privateDispatchSuccess(t, server, first, "wormhole.sync.status", nil, nil)
	if status["state"] != "offline" || status["pending_writes"] != float64(0) {
		t.Fatalf("configured local-only sync status = %#v, want offline with no pending Fabric writes", status)
	}
	if pending, err := server.qr.PendingCount(context.Background(), string(first.Scope.WorkspaceID)); err != nil || pending != 0 {
		t.Fatalf("configured local-only sync queue = (%d, %v), want no Fabric writes", pending, err)
	}
}
