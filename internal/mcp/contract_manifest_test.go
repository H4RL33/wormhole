package mcp

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
)

type alphaMCPContract struct {
	Mode     string         `json:"mode"`
	MCPTools []alphaMCPTool `json:"mcp_tools"`
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

func TestAlphaContractMCPRegistry(t *testing.T) {
	manifest := readAlphaMCPContract(t)
	if manifest.Mode != "alpha-inventory" {
		t.Fatalf("mode = %q, want alpha-inventory", manifest.Mode)
	}

	registry := buildFullRegistry()
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
