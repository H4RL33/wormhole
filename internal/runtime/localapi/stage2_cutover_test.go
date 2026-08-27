package localapi

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestStage2FinalPublicMCPInventory(t *testing.T) {
	registry := newLocalRegistry(&Server{})
	wantWorkspace := map[string]bool{
		"wormhole.workspace.status": true, "wormhole.workspace.diff": true,
		"wormhole.workspace.import": true, "wormhole.workspace.checkpoint": true,
		"wormhole.workspace.stash": true,
	}
	seenWorkspace := map[string]bool{}
	for _, tool := range registry.List() {
		lower := strings.ToLower(tool.Name + " " + tool.Description)
		for _, forbidden := range []string{"code" + "_graph", "code" + "-graph", "code" + " graph"} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("public MCP surface retains %q in %q", forbidden, lower)
			}
		}
		if wantWorkspace[tool.Name] {
			seenWorkspace[tool.Name] = true
		}
	}
	if !reflect.DeepEqual(seenWorkspace, wantWorkspace) {
		t.Fatalf("workspace MCP tools = %v, want %v", seenWorkspace, wantWorkspace)
	}

	register, ok := registry.Get("wormhole.agent.register")
	if !ok {
		t.Fatal("presence registration tool is missing")
	}
	if got := stage2SortedMapKeys(register.ArgumentExamples); !reflect.DeepEqual(got, []string{"default"}) {
		t.Fatalf("agent.register variants = %v, want presence-only default", got)
	}
	if got := stage2SortedMapKeys(register.ResultExamples); !reflect.DeepEqual(got, []string{"default"}) {
		t.Fatalf("agent.register results = %v, want presence-only default", got)
	}
	if strings.Contains(strings.ToLower(register.Description), "join") || strings.Contains(strings.ToLower(register.Description), "passport") {
		t.Fatalf("agent.register description retains removed behavior: %q", register.Description)
	}
}

func stage2SortedMapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
