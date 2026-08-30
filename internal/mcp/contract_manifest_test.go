package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
)

type alphaMCPContract struct {
	Mode         string                  `json:"mode"`
	MCPTools     alphaMCPInventories     `json:"mcp_tools"`
	SyncProtocol alphaPublicSyncProtocol `json:"sync_protocol"`
}

type alphaMCPInventories struct {
	Fabric               []alphaFabricMCPTool `json:"fabric"`
	PublicFabricContract []ToolDescriptor     `json:"public_fabric_contract"`
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
	Type       string                `json:"type,omitempty"`
	Format     string                `json:"format,omitempty"`
	Enum       []string              `json:"enum,omitempty"`
	Properties []alphaSchemaProperty `json:"properties,omitempty"`
	Required   []string              `json:"required,omitempty"`
	Items      *alphaSchema          `json:"items,omitempty"`
	AnyOf      []alphaSchema         `json:"anyOf,omitempty"`
}

type alphaSchemaProperty struct {
	Name   string      `json:"name"`
	Schema alphaSchema `json:"schema"`
}

type alphaPublicSyncProtocol struct {
	PublicSchemaDefinitions map[string]map[string]any `json:"public_schema_definitions"`
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

func TestAlphaContractLiveProjectionHasFivePublicSyncTools(t *testing.T) {
	manifest := readAlphaMCPContract(t)
	golden := readPublicDescriptorGolden(t)
	if !reflect.DeepEqual(manifest.MCPTools.PublicFabricContract, golden.Descriptors) ||
		!reflect.DeepEqual(manifest.SyncProtocol.PublicSchemaDefinitions, golden.Definitions) {
		t.Fatal("alpha public projection differs from checked-in descriptor authority")
	}
	want := make([]ToolDescriptor, len(golden.Descriptors))
	copy(want, golden.Descriptors)
	for index := range want {
		want[index].InputSchema = expandGoldenSchema(t, golden.Definitions, want[index].InputSchema).(map[string]any)
		want[index].OutputSchema = expandGoldenSchema(t, golden.Definitions, want[index].OutputSchema).(map[string]any)
	}
	actual := PublicFabricToolDescriptors()
	publicRegistry := NewPublicFabricRegistry(readyPublicRegistryDependencies(t))
	liveCount := 0
	for _, descriptor := range actual {
		if _, live := NewFabricRegistry(FabricRegistryDependencies{}).Get(descriptor.Name); live {
			t.Fatalf("public contract %q leaked into private registry", descriptor.Name)
		}
		_, live := publicRegistry.Get(descriptor.Name)
		wantLive := descriptor.Name == "wormhole.sync.attach" || descriptor.Name == "wormhole.sync.bootstrap" || descriptor.Name == "wormhole.sync.conflict" || descriptor.Name == "wormhole.sync.pull" || descriptor.Name == "wormhole.sync.push"
		if live != wantLive {
			t.Fatalf("public contract %q live = %v, want %v", descriptor.Name, live, wantLive)
		}
		if live {
			liveCount++
		}
	}
	if liveCount != 5 || len(publicRegistry.List()) != 5 {
		t.Fatalf("live public projection count = %d/%d, want 5/5", liveCount, len(publicRegistry.List()))
	}
	actualJSON, err := json.Marshal(actual)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actualJSON, wantJSON) {
		t.Fatalf("live public descriptors differ from alpha golden\ngot:  %s\nwant: %s", actualJSON, wantJSON)
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
			Schema:  schemaSnapshot(t, jsonResponseSchemaForType(exampleType)),
		})
	}
	return snapshots
}

func schemaSnapshot(t *testing.T, schema map[string]any) alphaSchema {
	t.Helper()
	snapshot := alphaSchema{}
	if rawType, ok := schema["type"]; ok {
		schemaType, ok := rawType.(string)
		if !ok {
			t.Fatalf("schema type = %T", rawType)
		}
		snapshot.Type = schemaType
	}
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
	if rawAnyOf, ok := schema["anyOf"]; ok {
		switch alternatives := rawAnyOf.(type) {
		case []map[string]any:
			for _, alternative := range alternatives {
				snapshot.AnyOf = append(snapshot.AnyOf, schemaSnapshot(t, alternative))
			}
		case []any:
			for _, rawAlternative := range alternatives {
				alternative, ok := rawAlternative.(map[string]any)
				if !ok {
					t.Fatalf("schema anyOf item = %T", rawAlternative)
				}
				snapshot.AnyOf = append(snapshot.AnyOf, schemaSnapshot(t, alternative))
			}
		default:
			t.Fatalf("schema anyOf = %T", rawAnyOf)
		}
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
