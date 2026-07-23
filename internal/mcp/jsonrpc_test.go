package mcp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestHandleInitialize(t *testing.T) {
	result := HandleInitialize("0.2.4-alpha")
	init, ok := result.(initializeResult)
	if !ok {
		t.Fatalf("HandleInitialize() returned %T, want initializeResult", result)
	}

	if init.ProtocolVersion != "2025-11-25" {
		t.Errorf("ProtocolVersion = %q, want %q", init.ProtocolVersion, "2025-11-25")
	}
	wantCaps := map[string]any{"tools": map[string]any{}}
	if !reflect.DeepEqual(init.Capabilities, wantCaps) {
		t.Errorf("Capabilities = %#v, want %#v", init.Capabilities, wantCaps)
	}
	wantInfo := map[string]string{"name": "wormhole", "version": "0.2.4-alpha"}
	if !reflect.DeepEqual(init.ServerInfo, wantInfo) {
		t.Errorf("ServerInfo = %#v, want %#v", init.ServerInfo, wantInfo)
	}
}

func TestHandleInitializeReportsConfiguredVersion(t *testing.T) {
	result := HandleInitialize("9.8.7-test")
	init, ok := result.(initializeResult)
	if !ok {
		t.Fatalf("HandleInitialize() returned %T, want initializeResult", result)
	}
	wantInfo := map[string]string{"name": "wormhole", "version": "9.8.7-test"}
	if !reflect.DeepEqual(init.ServerInfo, wantInfo) {
		t.Fatalf("ServerInfo = %#v, want %#v", init.ServerInfo, wantInfo)
	}
}

func TestHandleToolsList_AllToolsPresent(t *testing.T) {
	registry := NewFabricRegistry(FabricRegistryDependencies{})

	result := HandleToolsList(registry)
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("HandleToolsList() returned %T, want map[string]any", result)
	}
	entries, ok := m["tools"].([]toolListEntry)
	if !ok {
		t.Fatalf("tools field is %T, want []toolListEntry", m["tools"])
	}

	if len(entries) != 20 {
		t.Fatalf("got %d tools, want 20", len(entries))
	}

	wantNames := []string{
		"wormhole.agent.register",
		"wormhole.agent.whoami",
		"wormhole.task.create",
		"wormhole.task.assign",
		"wormhole.task.list",
		"wormhole.task.update_status",
		"wormhole.channel.create",
		"wormhole.channel.post",
		"wormhole.channel.list",
		"wormhole.channel.subscribe",
		"wormhole.kb.write",
		"wormhole.kb.search",
		"wormhole.kb.get",
		"wormhole.kb.get_links",
		"wormhole.git.link_commit",
		"wormhole.git.request_review",
		"wormhole.sync.bootstrap",
		"wormhole.sync.incremental_pull",
		"wormhole.sync.incremental_push",
		"wormhole.sync.conflict_report",
	}

	got := map[string]bool{}
	for _, e := range entries {
		got[e.Name] = true
	}
	for _, name := range wantNames {
		if !got[name] {
			t.Errorf("missing tool %q in tools/list result", name)
		}
	}
}

func TestHandleToolsList_ProjectIDRequiredExceptWhoAmI(t *testing.T) {
	registry := NewFabricRegistry(FabricRegistryDependencies{})

	result := HandleToolsList(registry).(map[string]any)
	entries := result["tools"].([]toolListEntry)

	var createTask, whoAmI *toolListEntry
	for i := range entries {
		switch entries[i].Name {
		case "wormhole.task.create":
			createTask = &entries[i]
		case "wormhole.agent.whoami":
			whoAmI = &entries[i]
		}
	}
	if createTask == nil {
		t.Fatal("wormhole.task.create not found in tools/list result")
	}
	if whoAmI == nil {
		t.Fatal("wormhole.agent.whoami not found in tools/list result")
	}

	requiredCreate, _ := createTask.InputSchema["required"].([]string)
	if !containsStr(requiredCreate, "project_id") {
		t.Errorf("wormhole.task.create required = %#v, want to contain %q", requiredCreate, "project_id")
	}
	props := createTask.InputSchema["properties"].(map[string]any)
	wantProjectIDSchema := map[string]any{"type": "string"}
	if !reflect.DeepEqual(props["project_id"], wantProjectIDSchema) {
		t.Errorf("wormhole.task.create properties.project_id = %#v, want %#v", props["project_id"], wantProjectIDSchema)
	}

	requiredWhoAmI, _ := whoAmI.InputSchema["required"].([]string)
	if containsStr(requiredWhoAmI, "project_id") {
		t.Errorf("wormhole.agent.whoami required = %#v, want no %q", requiredWhoAmI, "project_id")
	}
	whoAmIProps := whoAmI.InputSchema["properties"].(map[string]any)
	if _, ok := whoAmIProps["project_id"]; ok {
		t.Errorf("wormhole.agent.whoami properties has project_id, want absent")
	}

	// A nil []string and an empty []string are equal under reflect.DeepEqual,
	// but encoding/json marshals them differently (null vs []). tools/list is
	// JSON-RPC wire output, so the marshaled bytes are what actually matters
	// here, not the Go value.
	whoAmIJSON, err := json.Marshal(whoAmI.InputSchema)
	if err != nil {
		t.Fatalf("json.Marshal(whoAmI.InputSchema) error: %v", err)
	}
	if strings.Contains(string(whoAmIJSON), `"required":null`) {
		t.Errorf("wormhole.agent.whoami inputSchema marshaled with required:null, want required:[] or omitted key: %s", whoAmIJSON)
	}

	// No tool's required array should contain "project_id" twice.
	for i := range entries {
		entryJSON, err := json.Marshal(entries[i].InputSchema)
		if err != nil {
			t.Fatalf("json.Marshal(%s.InputSchema) error: %v", entries[i].Name, err)
		}
		if strings.Contains(string(entryJSON), `"required":null`) {
			t.Errorf("%s inputSchema marshaled with required:null, want required:[] or omitted key: %s", entries[i].Name, entryJSON)
		}

		required, _ := entries[i].InputSchema["required"].([]string)
		count := 0
		for _, name := range required {
			if name == "project_id" {
				count++
			}
		}
		if count > 1 {
			t.Errorf("%s required = %#v, want at most one %q", entries[i].Name, required, "project_id")
		}
	}
}

func TestReflectStructSchema_RequiredVsOptional(t *testing.T) {
	tool := CreateTaskTool(nil)
	schema := buildInputSchema(tool)

	required, _ := schema["required"].([]string)
	for _, name := range []string{"title", "description", "priority", "project_id"} {
		if !containsStr(required, name) {
			t.Errorf("required = %#v, want to contain %q", required, name)
		}
	}
	for _, name := range []string{"parent_task_id", "due_by"} {
		if containsStr(required, name) {
			t.Errorf("required = %#v, want no %q", required, name)
		}
	}
}

func TestReflectStructSchema_OmitemptyOptional(t *testing.T) {
	tool := SubscribeChannelTool(nil)
	schema := buildInputSchema(tool)

	required, _ := schema["required"].([]string)
	if containsStr(required, "limit") {
		t.Errorf("required = %#v, want no %q", required, "limit")
	}
	if containsStr(required, "offset") {
		t.Errorf("required = %#v, want no %q", required, "offset")
	}
	if !containsStr(required, "channel_id") {
		t.Errorf("required = %#v, want to contain %q", required, "channel_id")
	}
}

func TestJSONSchemaForType_TimeAndRawMessage(t *testing.T) {
	got := jsonSchemaForType(reflect.TypeOf(time.Time{}))
	want := map[string]any{"type": "string", "format": "date-time"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("jsonSchemaForType(time.Time) = %#v, want %#v", got, want)
	}

	got = jsonSchemaForType(reflect.TypeOf(json.RawMessage{}))
	want = map[string]any{"type": "object"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("jsonSchemaForType(json.RawMessage) = %#v, want %#v", got, want)
	}
}

func containsStr(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
