package mcp

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type alphaMCPContract struct {
	Mode         string            `json:"mode"`
	MCPTools     []alphaMCPTool    `json:"mcp_tools"`
	SyncProtocol alphaSyncProtocol `json:"sync_protocol"`
}

type alphaMCPTool struct {
	Name               string           `json:"name"`
	RequiresAuth       bool             `json:"requires_auth"`
	RequiredPermission string           `json:"required_permission"`
	InputSchema        alphaInputSchema `json:"input_schema"`
}

type alphaInputSchema struct {
	Type       string                `json:"type"`
	Properties []alphaSchemaProperty `json:"properties"`
	Required   []string              `json:"required"`
}

type alphaSchemaProperty struct {
	Name   string   `json:"name"`
	Type   string   `json:"type"`
	Items  string   `json:"items,omitempty"`
	Format string   `json:"format,omitempty"`
	Enum   []string `json:"enum,omitempty"`
}

type alphaSyncProtocol struct {
	Version   int             `json:"version"`
	WireTypes []alphaWireType `json:"wire_types"`
}

type alphaWireType struct {
	Name   string   `json:"name"`
	Fields []string `json:"fields"`
}

func TestAlphaContractMCPRegistry(t *testing.T) {
	manifest := readAlphaMCPContract(t)
	if manifest.Mode != "alpha-inventory" {
		t.Fatalf("mode = %q, want alpha-inventory", manifest.Mode)
	}

	registry := NewFabricRegistry(FabricRegistryDependencies{})
	actual := make([]alphaMCPTool, 0, len(registry.List()))
	for _, tool := range registry.List() {
		actual = append(actual, alphaMCPTool{
			Name:               tool.Name,
			RequiresAuth:       tool.RequiresAuth,
			RequiredPermission: tool.RequiredPermission,
			InputSchema:        toolSchemaSnapshot(t, buildInputSchema(tool)),
		})
	}
	sort.Slice(actual, func(i, j int) bool { return actual[i].Name < actual[j].Name })

	if !reflect.DeepEqual(actual, manifest.MCPTools) {
		got, _ := json.MarshalIndent(actual, "", "  ")
		want, _ := json.MarshalIndent(manifest.MCPTools, "", "  ")
		t.Fatalf("MCP contract drifted\nactual:\n%s\nmanifest:\n%s", got, want)
	}
}

func TestAlphaContractFabricSyncProtocol(t *testing.T) {
	manifest := readAlphaMCPContract(t)
	if SyncProtocolVersion != manifest.SyncProtocol.Version {
		t.Fatalf("Fabric SyncProtocolVersion = %d, manifest = %d", SyncProtocolVersion, manifest.SyncProtocol.Version)
	}

	pushItems, ok := reflect.TypeOf(IncrementalPushInput{}).FieldByName("Items")
	if !ok {
		t.Fatal("IncrementalPushInput has no Items field")
	}
	actual := []alphaWireType{
		{Name: "applied_item", Fields: jsonFieldNames(t, reflect.TypeOf(AppliedItem{}))},
		{Name: "article_summary", Fields: jsonFieldNames(t, reflect.TypeOf(ArticleSummary{}))},
		{Name: "bootstrap_request", Fields: jsonFieldNames(t, reflect.TypeOf(BootstrapInput{}))},
		{Name: "bootstrap_response", Fields: jsonFieldNames(t, reflect.TypeOf(BootstrapOutput{}))},
		{Name: "conflict_report_request", Fields: jsonFieldNames(t, reflect.TypeOf(ConflictReportInput{}))},
		{Name: "conflict_report_response", Fields: jsonFieldNames(t, reflect.TypeOf(ConflictReportOutput{}))},
		{Name: "incremental_pull_request", Fields: jsonFieldNames(t, reflect.TypeOf(IncrementalPullInput{}))},
		{Name: "incremental_pull_response", Fields: jsonFieldNames(t, reflect.TypeOf(IncrementalPullOutput{}))},
		{Name: "incremental_pull_update", Fields: jsonFieldNames(t, reflect.TypeOf(syncUpdateEnvelope{}))},
		{Name: "incremental_push_item", Fields: jsonFieldNames(t, pushItems.Type.Elem())},
		{Name: "incremental_push_request", Fields: jsonFieldNames(t, reflect.TypeOf(IncrementalPushInput{}))},
		{Name: "incremental_push_response", Fields: jsonFieldNames(t, reflect.TypeOf(IncrementalPushOutput{}))},
		{Name: "sync_channel_create_payload", Fields: jsonFieldNames(t, reflect.TypeOf(syncChannelCreatePayload{}))},
		{Name: "sync_conflict_audit_payload", Fields: jsonFieldNames(t, reflect.TypeOf(syncConflictAuditPayload{}))},
		{Name: "sync_event_create_payload", Fields: jsonFieldNames(t, reflect.TypeOf(syncEventCreatePayload{}))},
		{Name: "sync_kb_create_payload", Fields: jsonFieldNames(t, reflect.TypeOf(syncKBCreatePayload{}))},
		{Name: "sync_task_create_payload", Fields: jsonFieldNames(t, reflect.TypeOf(syncTaskCreatePayload{}))},
		{Name: "task_summary", Fields: jsonFieldNames(t, reflect.TypeOf(TaskSummary{}))},
	}
	sort.Slice(actual, func(i, j int) bool { return actual[i].Name < actual[j].Name })
	if !reflect.DeepEqual(actual, manifest.SyncProtocol.WireTypes) {
		got, _ := json.MarshalIndent(actual, "", "  ")
		want, _ := json.MarshalIndent(manifest.SyncProtocol.WireTypes, "", "  ")
		t.Fatalf("Fabric sync wire contract drifted\nactual:\n%s\nmanifest:\n%s", got, want)
	}
}

func readAlphaMCPContract(t *testing.T) alphaMCPContract {
	t.Helper()
	data, err := os.ReadFile("../../docs/contracts/alpha-contract.json")
	if err != nil {
		t.Fatalf("read alpha contract: %v", err)
	}
	var manifest alphaMCPContract
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode alpha contract: %v", err)
	}
	return manifest
}

func toolSchemaSnapshot(t *testing.T, schema map[string]any) alphaInputSchema {
	t.Helper()
	snapshot := alphaInputSchema{
		Type:       schema["type"].(string),
		Properties: []alphaSchemaProperty{},
		Required:   []string{},
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties = %T", schema["properties"])
	}
	for name, rawProperty := range properties {
		propertyMap, ok := rawProperty.(map[string]any)
		if !ok {
			t.Fatalf("schema property %s = %T", name, rawProperty)
		}
		property := alphaSchemaProperty{Name: name, Type: propertyMap["type"].(string)}
		if rawItems, ok := propertyMap["items"]; ok {
			property.Items = rawItems.(map[string]any)["type"].(string)
		}
		if rawFormat, ok := propertyMap["format"]; ok {
			property.Format = rawFormat.(string)
		}
		if rawEnum, ok := propertyMap["enum"]; ok {
			switch values := rawEnum.(type) {
			case []string:
				property.Enum = append(property.Enum, values...)
			case []any:
				for _, value := range values {
					property.Enum = append(property.Enum, value.(string))
				}
			default:
				t.Fatalf("schema property %s enum = %T", name, rawEnum)
			}
			sort.Strings(property.Enum)
		}
		snapshot.Properties = append(snapshot.Properties, property)
	}
	sort.Slice(snapshot.Properties, func(i, j int) bool {
		return snapshot.Properties[i].Name < snapshot.Properties[j].Name
	})
	snapshot.Required = append(snapshot.Required, schema["required"].([]string)...)
	sort.Strings(snapshot.Required)
	return snapshot
}

func jsonFieldNames(t *testing.T, valueType reflect.Type) []string {
	t.Helper()
	fields := make([]string, 0, valueType.NumField())
	for i := 0; i < valueType.NumField(); i++ {
		name := valueType.Field(i).Tag.Get("json")
		name = strings.Split(name, ",")[0]
		if name == "" || name == "-" {
			t.Fatalf("%s field %s has no JSON name", valueType, valueType.Field(i).Name)
		}
		fields = append(fields, name)
	}
	sort.Strings(fields)
	return fields
}
