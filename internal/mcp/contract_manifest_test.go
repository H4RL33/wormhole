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
	Mode         string              `json:"mode"`
	MCPTools     alphaMCPInventories `json:"mcp_tools"`
	SyncProtocol alphaSyncProtocol   `json:"sync_protocol"`
}

type alphaMCPInventories struct {
	Fabric []alphaFabricMCPTool `json:"fabric"`
}

type alphaFabricMCPTool struct {
	Name               string          `json:"name"`
	RequiresAuth       bool            `json:"requires_auth"`
	RequiredPermission string          `json:"required_permission"`
	RequestSchema      alphaSchema     `json:"request_schema"`
	ResponseSchemas    []alphaResponse `json:"response_schemas"`
}

type alphaResponse struct {
	Variant string      `json:"variant"`
	Schema  alphaSchema `json:"schema"`
}

type alphaSchema struct {
	Type       string                `json:"type"`
	Format     string                `json:"format,omitempty"`
	Enum       []string              `json:"enum,omitempty"`
	Properties []alphaSchemaProperty `json:"properties,omitempty"`
	Required   []string              `json:"required,omitempty"`
	Items      *alphaSchema          `json:"items,omitempty"`
}

type alphaSchemaProperty struct {
	Name   string      `json:"name"`
	Schema alphaSchema `json:"schema"`
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

	actual := fabricMCPContract(t)
	if !reflect.DeepEqual(actual, manifest.MCPTools.Fabric) {
		got, _ := json.MarshalIndent(actual, "", "  ")
		want, _ := json.MarshalIndent(manifest.MCPTools.Fabric, "", "  ")
		t.Fatalf("Fabric MCP contract drifted\nactual:\n%s\nmanifest:\n%s", got, want)
	}
}

func fabricMCPContract(t *testing.T) []alphaFabricMCPTool {
	t.Helper()
	registry := NewFabricRegistry(FabricRegistryDependencies{})
	actual := make([]alphaFabricMCPTool, 0, len(registry.List()))
	for _, tool := range registry.List() {
		actual = append(actual, alphaFabricMCPTool{
			Name:               tool.Name,
			RequiresAuth:       tool.RequiresAuth,
			RequiredPermission: tool.RequiredPermission,
			RequestSchema:      schemaSnapshot(t, buildInputSchema(tool)),
			ResponseSchemas:    responseSchemaSnapshots(t, tool.ResultExamples),
		})
	}
	sort.Slice(actual, func(i, j int) bool { return actual[i].Name < actual[j].Name })
	return actual
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

func responseSchemaSnapshots(t *testing.T, examples map[string]any) []alphaResponse {
	t.Helper()
	if len(examples) == 0 {
		t.Fatal("tool descriptor has no response examples")
	}
	variants := make([]string, 0, len(examples))
	for variant := range examples {
		variants = append(variants, variant)
	}
	sort.Strings(variants)
	snapshots := make([]alphaResponse, 0, len(variants))
	for _, variant := range variants {
		exampleType := reflect.TypeOf(examples[variant])
		if exampleType == nil {
			t.Fatalf("response variant %q has nil example", variant)
		}
		snapshots = append(snapshots, alphaResponse{
			Variant: variant,
			Schema:  schemaSnapshot(t, jsonSchemaForType(exampleType)),
		})
	}
	return snapshots
}

func schemaSnapshot(t *testing.T, schema map[string]any) alphaSchema {
	t.Helper()
	snapshot := alphaSchema{Type: schemaType(t, schema)}
	if format, ok := schema["format"].(string); ok {
		snapshot.Format = format
	}
	if rawEnum, ok := schema["enum"]; ok {
		switch values := rawEnum.(type) {
		case []string:
			snapshot.Enum = append(snapshot.Enum, values...)
		case []any:
			for _, value := range values {
				item, ok := value.(string)
				if !ok {
					t.Fatalf("schema enum item = %T", value)
				}
				snapshot.Enum = append(snapshot.Enum, item)
			}
		default:
			t.Fatalf("schema enum = %T", rawEnum)
		}
		sort.Strings(snapshot.Enum)
	}
	if rawItems, ok := schema["items"]; ok {
		items, ok := rawItems.(map[string]any)
		if !ok {
			t.Fatalf("schema items = %T", rawItems)
		}
		itemSnapshot := schemaSnapshot(t, items)
		snapshot.Items = &itemSnapshot
	}
	if rawProperties, ok := schema["properties"]; ok {
		properties, ok := rawProperties.(map[string]any)
		if !ok {
			t.Fatalf("schema properties = %T", rawProperties)
		}
		for name, rawProperty := range properties {
			propertyMap, ok := rawProperty.(map[string]any)
			if !ok {
				t.Fatalf("schema property %s = %T", name, rawProperty)
			}
			snapshot.Properties = append(snapshot.Properties, alphaSchemaProperty{
				Name:   name,
				Schema: schemaSnapshot(t, propertyMap),
			})
		}
		sort.Slice(snapshot.Properties, func(i, j int) bool {
			return snapshot.Properties[i].Name < snapshot.Properties[j].Name
		})
	}
	if rawRequired, ok := schema["required"]; ok {
		switch values := rawRequired.(type) {
		case []string:
			snapshot.Required = append(snapshot.Required, values...)
		case []any:
			for _, value := range values {
				item, ok := value.(string)
				if !ok {
					t.Fatalf("schema required item = %T", value)
				}
				snapshot.Required = append(snapshot.Required, item)
			}
		default:
			t.Fatalf("schema required = %T", rawRequired)
		}
		sort.Strings(snapshot.Required)
	}
	return snapshot
}

func schemaType(t *testing.T, schema map[string]any) string {
	t.Helper()
	value, ok := schema["type"].(string)
	if !ok {
		t.Fatalf("schema type = %T", schema["type"])
	}
	return value
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
