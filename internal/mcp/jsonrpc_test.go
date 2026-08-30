package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
)

type jsonRPCV2Arguments struct {
	Version int    `json:"version" const:"2"`
	Value   string `json:"value"`
}

type jsonRPCV2Result struct {
	Version int    `json:"version" const:"2"`
	Value   string `json:"value"`
}

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

func TestHandleToolsList_AllPrivateToolsPresent(t *testing.T) {
	result := HandleToolsList(NewFabricRegistry(FabricRegistryDependencies{}))
	entries := result.(map[string]any)["tools"].([]toolListEntry)
	if len(entries) != 16 {
		t.Fatalf("got %d live private tools, want 16", len(entries))
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name, "wormhole.sync.") || strings.HasPrefix(entry.Name, "wormhole.activity.") {
			t.Fatalf("unexpected public protocol registration %q", entry.Name)
		}
	}
}

func TestJSONRPCPublicDispatchSelectsAndStrictDecodesV2BeforeInvocation(t *testing.T) {
	invocations := 0
	registry := NewRegistry()
	registry.Register(Tool{
		Name:             "wormhole.sync.pull",
		Description:      "test public dispatch",
		ArgumentVariants: map[int]any{2: jsonRPCV2Arguments{}},
		ResultVariants:   map[int]any{2: jsonRPCV2Result{}},
		PublicHandler: func(_ context.Context, raw json.RawMessage, proof types.PublicRequestProof) (any, error) {
			invocations++
			var arguments jsonRPCV2Arguments
			if err := json.Unmarshal(raw, &arguments); err != nil {
				return nil, err
			}
			return jsonRPCV2Result{Version: arguments.Version, Value: arguments.Value}, nil
		},
	})

	valid := publicToolsCallParams(t, "wormhole.sync.pull", json.RawMessage(`{"version":2,"value":"ok"}`))
	result, rpcErr := HandleToolsCall(context.Background(), registry, nil, "", valid)
	if rpcErr != nil {
		t.Fatalf("valid public dispatch RPC error = %+v", rpcErr)
	}
	callResult := result.(toolCallResult)
	if callResult.IsError || len(callResult.Content) != 1 || callResult.Content[0].Text != `{"version":2,"value":"ok"}` {
		t.Fatalf("valid public result = %+v", callResult)
	}
	if invocations != 1 {
		t.Fatalf("valid public invocations = %d, want 1", invocations)
	}

	for name, fixture := range map[string]struct {
		arguments string
		wantCode  string
	}{
		"missing":         {`{"value":"bad"}`, "invalid_request"},
		"string":          {`{"version":"2","value":"bad"}`, "invalid_request"},
		"fractional":      {`{"version":2.5,"value":"bad"}`, "invalid_request"},
		"null":            {`{"version":null,"value":"bad"}`, "invalid_request"},
		"unknown version": {`{"version":3,"value":"bad"}`, "unknown_version"},
		"duplicate":       {`{"version":2,"version":2,"value":"bad"}`, "invalid_request"},
		"unknown member":  {`{"version":2,"value":"bad","extra":true}`, "invalid_request"},
	} {
		t.Run(name, func(t *testing.T) {
			result, rpcErr := HandleToolsCall(context.Background(), registry, nil, "", publicToolsCallParams(t, "wormhole.sync.pull", json.RawMessage(fixture.arguments)))
			if rpcErr != nil {
				t.Fatalf("public rejection RPC error = %+v", rpcErr)
			}
			failure := result.(toolCallResult)
			if !failure.IsError || len(failure.Content) != 1 {
				t.Fatalf("public rejection result = %+v", failure)
			}
			var decoded ToolFailureV1
			if err := json.Unmarshal([]byte(failure.Content[0].Text), &decoded); err != nil {
				t.Fatalf("decode public failure: %v", err)
			}
			if decoded != (ToolFailureV1{Code: fixture.wantCode, Operation: "wormhole.sync.pull"}) {
				t.Fatalf("public failure = %+v, want code %q", decoded, fixture.wantCode)
			}
			if invocations != 1 {
				t.Fatalf("invalid public request invoked handler; invocations = %d", invocations)
			}
		})
	}
}

func TestJSONRPCPublicDispatchRejectsWrongSelectedResultVersion(t *testing.T) {
	for _, version := range []int{0, 3} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			registry := NewRegistry()
			registry.Register(Tool{
				Name:             "wormhole.sync.pull",
				Description:      "test public result validation",
				ArgumentVariants: map[int]any{2: jsonRPCV2Arguments{}},
				ResultVariants:   map[int]any{2: jsonRPCV2Result{}},
				PublicHandler: func(context.Context, json.RawMessage, types.PublicRequestProof) (any, error) {
					return jsonRPCV2Result{Version: version, Value: "wrong-version"}, nil
				},
			})
			result, rpcErr := HandleToolsCall(context.Background(), registry, nil, "", publicToolsCallParams(t, "wormhole.sync.pull", json.RawMessage(`{"version":2,"value":"ok"}`)))
			if rpcErr != nil {
				t.Fatalf("wrong-version public result RPC error = %+v", rpcErr)
			}
			failure := result.(toolCallResult)
			if !failure.IsError || len(failure.Content) != 1 {
				t.Fatalf("wrong-version public result = %+v, want safe tool failure", failure)
			}
			var decoded ToolFailureV1
			if err := json.Unmarshal([]byte(failure.Content[0].Text), &decoded); err != nil {
				t.Fatalf("decode wrong-version failure: %v", err)
			}
			if decoded != (ToolFailureV1{Code: "internal_error", Operation: "wormhole.sync.pull"}) {
				t.Fatalf("wrong-version failure = %+v", decoded)
			}
		})
	}
}

func TestJSONRPCPublicVersionDecodeRejectsTrailingJSON(t *testing.T) {
	tool := Tool{ArgumentVariants: map[int]any{2: jsonRPCV2Arguments{}}}
	if _, code := decodeVersionedPublicArguments(tool, json.RawMessage(`{"version":2,"value":"bad"}{}`)); code != "invalid_request" {
		t.Fatalf("trailing JSON code = %q, want invalid_request", code)
	}
}

func TestJSONRPCMalformedKnownPublicEnvelopeNeverFallsBackToPrivateDispatch(t *testing.T) {
	registry := NewRegistry()
	registry.Register(Tool{
		Name: "wormhole.sync.pull", ArgumentVariants: map[int]any{2: jsonRPCV2Arguments{}},
		ResultVariants: map[int]any{2: jsonRPCV2Result{}},
		PublicHandler: func(context.Context, json.RawMessage, types.PublicRequestProof) (any, error) {
			t.Fatal("malformed public envelope invoked handler")
			return nil, nil
		},
	})
	_, rpcErr := HandleToolsCall(context.Background(), registry, nil, "", json.RawMessage(
		`{"name":"wormhole.sync.pull","name":"wormhole.sync.pull","arguments":{"version":2,"value":"bad"}}`,
	))
	if rpcErr == nil || rpcErr.Code != RPCInvalidParams {
		t.Fatalf("malformed public envelope error = %+v, want invalid params", rpcErr)
	}
}

func publicToolsCallParams(t *testing.T, name string, arguments json.RawMessage) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(ToolsCallParams{Name: name, Arguments: arguments, Proof: publicProofFixture()})
	if err != nil {
		t.Fatal(err)
	}
	return raw
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
	want = map[string]any{}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("jsonSchemaForType(json.RawMessage) = %#v, want %#v", got, want)
	}
}

func TestJSONResponseSchemaMatchesEncodingSemantics(t *testing.T) {
	type response struct {
		RequiredPointer *string         `json:"required_pointer"`
		OptionalPointer *string         `json:"optional_pointer,omitempty"`
		RequiredSlice   []string        `json:"required_slice"`
		OptionalSlice   []string        `json:"optional_slice,omitempty"`
		RequiredMap     map[string]int  `json:"required_map"`
		OptionalMap     map[string]int  `json:"optional_map,omitempty"`
		Payload         json.RawMessage `json:"payload"`
		OptionalPayload json.RawMessage `json:"optional_payload,omitempty"`
	}

	schema := jsonResponseSchemaForType(reflect.TypeOf(response{}))
	properties := schema["properties"].(map[string]any)
	required := schema["required"].([]string)

	for _, name := range []string{"required_pointer", "required_slice", "required_map", "payload"} {
		if !containsStr(required, name) {
			t.Errorf("required = %v, want %q", required, name)
		}
	}
	for _, name := range []string{"optional_pointer", "optional_slice", "optional_map", "optional_payload"} {
		if containsStr(required, name) {
			t.Errorf("required = %v, want %q optional", required, name)
		}
	}

	requiredPointer := properties["required_pointer"].(map[string]any)
	wantNullableString := []map[string]any{{"type": "string"}, {"type": "null"}}
	if got := requiredPointer["anyOf"]; !reflect.DeepEqual(got, wantNullableString) {
		t.Errorf("required pointer schema = %#v, want anyOf %v", requiredPointer, wantNullableString)
	}
	optionalPointer := properties["optional_pointer"].(map[string]any)
	if optionalPointer["type"] != "string" {
		t.Errorf("optional pointer schema = %#v, want optional string", optionalPointer)
	}
	if _, nullable := optionalPointer["anyOf"]; nullable {
		t.Errorf("optional pointer schema = %#v, want no null union", optionalPointer)
	}
	for name, wantType := range map[string]string{
		"required_slice": "array",
		"required_map":   "object",
	} {
		property := properties[name].(map[string]any)
		alternatives, ok := property["anyOf"].([]map[string]any)
		if !ok || len(alternatives) != 2 || alternatives[0]["type"] != wantType || alternatives[1]["type"] != "null" {
			t.Errorf("%s schema = %#v, want %s|null", name, property, wantType)
		}
	}
	for name, wantType := range map[string]string{
		"optional_slice": "array",
		"optional_map":   "object",
	} {
		property := properties[name].(map[string]any)
		if property["type"] != wantType {
			t.Errorf("%s schema = %#v, want optional %s", name, property, wantType)
		}
		if _, nullable := property["anyOf"]; nullable {
			t.Errorf("%s schema = %#v, want no null union", name, property)
		}
	}
	for _, name := range []string{"payload", "optional_payload"} {
		if got := properties[name].(map[string]any); len(got) != 0 {
			t.Errorf("%s schema = %#v, want unconstrained JSON", name, got)
		}
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
