package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestSyncV2MCPAliasesAreTypeIdentical(t *testing.T) {
	pairs := [][2]any{
		{SyncV2Scope{}, projectstate.SyncV2Scope{}}, {SyncStateV2{}, projectstate.SyncStateV2{}},
		{SyncAttachV2Args{}, projectstate.SyncAttachV2Args{}}, {SyncAttachV2Result{}, projectstate.SyncAttachV2Result{}},
		{PublicAgentSessionIssueV2Args{}, projectstate.PublicAgentSessionIssueV2Args{}}, {PublicAgentSessionIssueV2Result{}, projectstate.PublicAgentSessionIssueV2Result{}},
		{SyncBootstrapV2Args{}, projectstate.SyncBootstrapV2Args{}}, {SyncBootstrapV2Result{}, projectstate.SyncBootstrapV2Result{}},
		{SyncPullV2Args{}, projectstate.SyncPullV2Args{}}, {SyncPullV2Result{}, projectstate.SyncPullV2Result{}},
		{SyncPushV2Args{}, projectstate.SyncPushV2Args{}}, {SyncPushAppliedV2Result{}, projectstate.SyncPushAppliedV2Result{}},
		{SyncPushConflictV2Result{}, projectstate.SyncPushConflictV2Result{}}, {SyncConflictV2Args{}, projectstate.SyncConflictV2Args{}},
		{SyncConflictResolvedV2Result{}, projectstate.SyncConflictResolvedV2Result{}},
		{ActivityAcceptV1Args{}, projectstate.ActivityAcceptV1Args{}}, {ActivityAcceptedV1Result{}, projectstate.ActivityAcceptedV1Result{}},
		{ActivityPolicyChangedV1Result{}, projectstate.ActivityPolicyChangedV1Result{}}, {ActivityPresenceV1Args{}, projectstate.ActivityPresenceV1Args{}},
		{ActivityPresenceAcceptedV1Result{}, projectstate.ActivityPresenceAcceptedV1Result{}}, {ActivityPullV1Args{}, projectstate.ActivityPullV1Args{}},
		{ActivityPolicyEvidenceV1{}, projectstate.ActivityPolicyEvidenceV1{}}, {ActivityDeliveryV1{}, projectstate.ActivityDeliveryV1{}},
		{ActivityPullV1Result{}, projectstate.ActivityPullV1Result{}}, {ActivityLifecycleV1Args{}, projectstate.ActivityLifecycleV1Args{}},
		{ActivityLifecycleV1Result{}, projectstate.ActivityLifecycleV1Result{}},
	}
	for i, pair := range pairs {
		if reflect.TypeOf(pair[0]) != reflect.TypeOf(pair[1]) {
			t.Fatalf("alias pair %d differs: %T != %T", i, pair[0], pair[1])
		}
	}
}

func TestPublicFabricToolDescriptorsAreExactSortedDescriptorValues(t *testing.T) {
	want := []string{
		"wormhole.activity.accept", "wormhole.activity.lifecycle", "wormhole.activity.presence", "wormhole.activity.pull",
		"wormhole.sync.attach", "wormhole.sync.bootstrap", "wormhole.sync.conflict", "wormhole.sync.issue_agent_session",
		"wormhole.sync.pull", "wormhole.sync.push",
	}
	descriptors := PublicFabricToolDescriptors()
	if len(descriptors) != len(want) {
		t.Fatalf("descriptor count = %d, want %d", len(descriptors), len(want))
	}
	got := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		got = append(got, descriptor.Name)
		if descriptor.Description == "" || descriptor.AuthFamily != PublicProofAuth {
			t.Fatalf("descriptor %q = %+v", descriptor.Name, descriptor)
		}
		if descriptor.InputSchema["additionalProperties"] != false {
			t.Fatalf("%s input is not closed: %#v", descriptor.Name, descriptor.InputSchema)
		}
		properties := descriptor.InputSchema["properties"].(map[string]any)
		version := properties["version"].(map[string]any)
		wantVersion := any(2)
		if descriptor.Name[:18] == "wormhole.activity." {
			wantVersion = 1
		}
		if version["const"] != wantVersion {
			t.Fatalf("%s version schema = %#v, want const %v", descriptor.Name, version, wantVersion)
		}
		if _, ok := descriptor.OutputSchema["oneOf"]; !ok {
			t.Fatalf("%s result lacks oneOf: %#v", descriptor.Name, descriptor.OutputSchema)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("descriptor names = %q, want %q", got, want)
	}
}

type publicDescriptorGolden struct {
	Definitions map[string]map[string]any
	Descriptors []ToolDescriptor
}

func readPublicDescriptorGolden(t *testing.T) publicDescriptorGolden {
	t.Helper()
	raw, err := os.ReadFile("../../docs/contracts/public-fabric-descriptors.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden publicDescriptorGolden
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	if len(golden.Definitions) == 0 || len(golden.Descriptors) != 10 {
		t.Fatalf("public descriptor golden = %d definitions, %d descriptors", len(golden.Definitions), len(golden.Descriptors))
	}
	return golden
}

func expandGoldenSchema(t *testing.T, definitions map[string]map[string]any, value any) any {
	t.Helper()
	switch value := value.(type) {
	case []any:
		expanded := make([]any, len(value))
		for index := range value {
			expanded[index] = expandGoldenSchema(t, definitions, value[index])
		}
		return expanded
	case map[string]any:
		if rawRef, ok := value["$ref"]; ok {
			if len(value) != 1 {
				t.Fatalf("$ref has siblings: %#v", value)
			}
			ref, ok := rawRef.(string)
			const prefix = "#/definitions/"
			if !ok || !strings.HasPrefix(ref, prefix) {
				t.Fatalf("invalid local $ref: %#v", rawRef)
			}
			definition, ok := definitions[strings.TrimPrefix(ref, prefix)]
			if !ok {
				t.Fatalf("missing definition %q", ref)
			}
			return expandGoldenSchema(t, definitions, definition)
		}
		expanded := make(map[string]any, len(value))
		for key, nested := range value {
			expanded[key] = expandGoldenSchema(t, definitions, nested)
		}
		return expanded
	default:
		return value
	}
}

func TestPublicFabricToolDescriptorsMatchCompleteIndependentGolden(t *testing.T) {
	golden := readPublicDescriptorGolden(t)
	want := make([]ToolDescriptor, len(golden.Descriptors))
	copy(want, golden.Descriptors)
	for index := range want {
		want[index].InputSchema = expandGoldenSchema(t, golden.Definitions, want[index].InputSchema).(map[string]any)
		want[index].OutputSchema = expandGoldenSchema(t, golden.Definitions, want[index].OutputSchema).(map[string]any)
	}
	gotJSON, err := json.Marshal(PublicFabricToolDescriptors())
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("public descriptor schema drift\ngot:  %s\nwant: %s", gotJSON, wantJSON)
	}
}

func TestPublicFabricRegistryVariantsMatchFrozenDescriptorSchemas(t *testing.T) {
	registry := NewPublicFabricRegistry(readyPublicRegistryDependencies(t))
	descriptors := make(map[string]ToolDescriptor)
	for _, descriptor := range PublicFabricToolDescriptors() {
		descriptors[descriptor.Name] = descriptor
	}
	for _, tool := range registry.List() {
		descriptor, ok := descriptors[tool.Name]
		if !ok {
			t.Fatalf("live public tool %q has no frozen descriptor", tool.Name)
		}
		gotInput, err := json.Marshal(buildInputSchema(tool))
		if err != nil {
			t.Fatal(err)
		}
		wantInput, err := json.Marshal(descriptor.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(gotInput, wantInput) {
			t.Fatalf("%s live input schema drift\ngot:  %s\nwant: %s", tool.Name, gotInput, wantInput)
		}
		gotOutput, err := json.Marshal(schemaOneOf(tool.ResultVariants[2]...))
		if err != nil {
			t.Fatal(err)
		}
		wantOutput, err := json.Marshal(descriptor.OutputSchema)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(gotOutput, wantOutput) {
			t.Fatalf("%s live output schema drift\ngot:  %s\nwant: %s", tool.Name, gotOutput, wantOutput)
		}
	}
}

func TestPublicDescriptorSchemasRejectPrivateRoutingFields(t *testing.T) {
	forbidden := map[string]bool{
		"project_id": true, "workspace_id": true, "fabric_instance_id": true,
		"remote_project_id": true, "stream_id": true, "actor_scope": true,
	}
	for _, descriptor := range PublicFabricToolDescriptors() {
		walkSchemaProperties(t, descriptor.Name, descriptor.InputSchema, forbidden)
	}
}

func walkSchemaProperties(t *testing.T, tool string, schema map[string]any, forbidden map[string]bool) {
	t.Helper()
	if properties, ok := schema["properties"].(map[string]any); ok {
		for name, raw := range properties {
			if forbidden[name] {
				t.Fatalf("%s exposes forbidden input %q", tool, name)
			}
			if nested, ok := raw.(map[string]any); ok {
				walkSchemaProperties(t, tool, nested, forbidden)
			}
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		walkSchemaProperties(t, tool, items, forbidden)
	}
}

func TestRetainedPrivateCreateTaskSchemaGolden(t *testing.T) {
	got := buildInputSchema(CreateTaskTool(nil))
	want := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"project_id": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"},
			"description": map[string]any{"type": "string"}, "parent_task_id": map[string]any{"type": "string"},
			"priority": map[string]any{"type": "integer"}, "due_by": map[string]any{"type": "string", "format": "date-time"},
		},
		"required": []string{"title", "description", "priority", "project_id"},
	}
	if !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(want)
		t.Fatalf("retained private schema changed\ngot:  %s\nwant: %s", gotJSON, wantJSON)
	}
}

type schemaEmbeddedFixture struct {
	Version int       `json:"version" const:"2"`
	When    time.Time `json:"when"`
}

type schemaFixture struct {
	schemaEmbeddedFixture
	Raw      json.RawMessage   `json:"raw"`
	Bytes    []byte            `json:"bytes"`
	Labels   map[string]string `json:"labels"`
	Anything any               `json:"anything"`
	Choice   string            `json:"choice" enum:"one,two"`
}

type schemaPointerFixture struct {
	Required *string `json:"required"`
	Optional *string `json:"optional,omitempty"`
}

func TestClosedSchemaDistinguishesRequiredNullableAndOptionalPointers(t *testing.T) {
	closedProperties, closedRequired := reflectStructSchemaWithOptions(
		reflect.TypeOf(schemaPointerFixture{}), schemaOptions{closedObjects: true},
	)
	if !reflect.DeepEqual(closedRequired, []string{"required"}) {
		t.Fatalf("closed required = %#v, want required pointer only", closedRequired)
	}
	wantRequired := map[string]any{"anyOf": []any{
		map[string]any{"type": "string"}, map[string]any{"type": "null"},
	}}
	if !reflect.DeepEqual(closedProperties["required"], wantRequired) {
		t.Fatalf("closed required pointer = %#v, want %#v", closedProperties["required"], wantRequired)
	}
	if !reflect.DeepEqual(closedProperties["optional"], map[string]any{"type": "string"}) {
		t.Fatalf("closed optional pointer = %#v", closedProperties["optional"])
	}

	openProperties, openRequired := reflectStructSchema(reflect.TypeOf(schemaPointerFixture{}))
	if len(openRequired) != 0 ||
		!reflect.DeepEqual(openProperties["required"], map[string]any{"type": "string"}) ||
		!reflect.DeepEqual(openProperties["optional"], map[string]any{"type": "string"}) {
		t.Fatalf("legacy open pointer schema changed: properties=%#v required=%#v", openProperties, openRequired)
	}
}

func TestClosedSchemaHandlesAnonymousTimeBytesRawMapsInterfacesConstAndOneOf(t *testing.T) {
	schema := closedJSONSchemaForType(reflect.TypeOf(schemaFixture{}))
	if schema["additionalProperties"] != false {
		t.Fatalf("outer schema is open: %#v", schema)
	}
	properties := schema["properties"].(map[string]any)
	for _, name := range []string{"version", "when", "raw", "bytes", "labels", "anything", "choice"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("flattened schema lacks %q: %#v", name, properties)
		}
	}
	if properties["version"].(map[string]any)["const"] != 2 {
		t.Fatalf("version const = %#v", properties["version"])
	}
	if properties["when"].(map[string]any)["format"] != "date-time" {
		t.Fatalf("time schema = %#v", properties["when"])
	}
	if len(properties["raw"].(map[string]any)) != 0 || len(properties["anything"].(map[string]any)) != 0 {
		t.Fatalf("raw/interface schemas = %#v/%#v", properties["raw"], properties["anything"])
	}
	if properties["bytes"].(map[string]any)["contentEncoding"] != "base64" {
		t.Fatalf("bytes schema = %#v", properties["bytes"])
	}
	labels := properties["labels"].(map[string]any)
	if labels["type"] != "object" || labels["additionalProperties"].(map[string]any)["type"] != "string" {
		t.Fatalf("map schema = %#v", labels)
	}
	oneOf := schemaOneOf(projectstate.SyncPushAppliedV2Result{}, projectstate.SyncPushConflictV2Result{})
	if variants, ok := oneOf["oneOf"].([]any); !ok || len(variants) != 2 {
		t.Fatalf("oneOf schema = %#v", oneOf)
	}
	for _, typ := range []reflect.Type{nil, reflect.TypeOf((*any)(nil)).Elem(), reflect.TypeOf(map[int]string{}), reflect.TypeOf((chan int)(nil))} {
		_ = closedJSONSchemaForType(typ)
	}
}

func TestPublicDescriptorsAreReturnedInFreshOrder(t *testing.T) {
	first := PublicFabricToolDescriptors()
	first[0].Name = "mutated"
	second := PublicFabricToolDescriptors()
	names := make([]string, 0, len(second))
	for _, descriptor := range second {
		names = append(names, descriptor.Name)
	}
	if !sort.StringsAreSorted(names) || names[0] == "mutated" {
		t.Fatalf("descriptor copy/order = %q", names)
	}
	_ = types.PublicRequestProof{}
}
