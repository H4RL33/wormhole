package localapi

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestGatewayToolGuidanceCoversLiveRegistry(t *testing.T) {
	registry := newLocalRegistry(&Server{})
	assertGatewayToolGuidance(t, registry)
}

func assertGatewayToolGuidance(t *testing.T, registry *localRegistry) {
	t.Helper()
	tools := registry.List()
	guidance := registry.Guidance()
	if len(tools) != 21 {
		t.Fatalf("live Gateway tool count = %d, want 21", len(tools))
	}
	if len(guidance) != len(tools) {
		t.Fatalf("guidance count = %d, live tool count = %d", len(guidance), len(tools))
	}

	byTool := make(map[string]toolGuidance, len(guidance))
	for _, record := range guidance {
		if _, exists := registry.Get(record.ToolName); !exists {
			t.Fatalf("guidance record points to missing live tool %q", record.ToolName)
		}
		if _, exists := byTool[record.ToolName]; exists {
			t.Fatalf("duplicate guidance record for %q", record.ToolName)
		}
		byTool[record.ToolName] = record
		assertCompleteToolGuidance(t, record)
		if !allowedGuidanceConcepts[record.Concept] {
			t.Fatalf("%s guidance has unsupported concept %q", record.ToolName, record.Concept)
		}
	}

	for _, tool := range tools {
		record, ok := byTool[tool.Name]
		if !ok {
			t.Fatalf("live tool %q has no guidance record", tool.Name)
		}
		if !reflect.DeepEqual(record.RequiredPermissions, tool.RequiredPermissions) {
			t.Fatalf("%s guidance permissions = %v, registry permissions = %v", tool.Name, record.RequiredPermissions, tool.RequiredPermissions)
		}
		wantExample := minimalInputExample(tool)
		if !reflect.DeepEqual(record.MinimalExample, wantExample) {
			t.Fatalf("%s minimal example was not generated from its live schema\ngot:  %#v\nwant: %#v", tool.Name, record.MinimalExample, wantExample)
		}
		validateGuidanceExample(t, buildInputSchema(tool), record.MinimalExample)
	}

	for _, name := range []string{"wormhole.code_graph.query", "wormhole.code_graph.status", "wormhole.code_graph.rebuild"} {
		record := byTool[name]
		if record.FreshnessImplications == "" || record.SourceAccessImplications == "" {
			t.Fatalf("%s must describe freshness and source-access implications", name)
		}
	}
	assertCodeGraphGuidanceSemantics(t, byTool)
	assertGuidanceMutationSet(t, byTool)
	if _, exists := byTool["wormhole.agent.get_guidance"]; exists {
		t.Fatal("designed-only wormhole.agent.get_guidance must not have a live guidance record")
	}
}

var allowedGuidanceConcepts = map[string]bool{
	"identity": true, "tasks": true, "channels and events": true, "knowledge": true,
	"local status and synchronisation": true, "Code Graph": true,
}

func assertCodeGraphGuidanceSemantics(t *testing.T, byTool map[string]toolGuidance) {
	t.Helper()
	for _, name := range []string{"wormhole.code_graph.query", "wormhole.code_graph.status", "wormhole.code_graph.rebuild"} {
		if !strings.Contains(byTool[name].FreshnessImplications, "Git HEAD") || !strings.Contains(byTool[name].FreshnessImplications, "working-tree") {
			t.Fatalf("%s must require Git HEAD and working-tree verification", name)
		}
	}
	query := byTool["wormhole.code_graph.query"]
	if !strings.Contains(query.SourceAccessImplications, "code_graph.source.read") || !strings.Contains(query.SourceAccessImplications, "source budget") || !strings.Contains(query.MisuseWarning, "heuristic") {
		t.Fatal("Code Graph query guidance must explain source permission, bounded budget, and heuristic edges")
	}
	rebuild := byTool["wormhole.code_graph.rebuild"]
	if !strings.Contains(rebuild.Purpose, "balanced copy-on-write") || !strings.Contains(rebuild.SourceAccessImplications, "code_graph.source.read") || !strings.Contains(rebuild.MisuseWarning, "heuristic") {
		t.Fatal("Code Graph rebuild guidance must explain balanced rebuild, source access, and heuristic edges")
	}
}

func assertGuidanceMutationSet(t *testing.T, byTool map[string]toolGuidance) {
	t.Helper()
	mutating := map[string]bool{
		"wormhole.agent.enrol": true, "wormhole.agent.register": true, "wormhole.agent.presence": true,
		"wormhole.code_graph.rebuild": true, "wormhole.task.create": true, "wormhole.task.route": true,
		"wormhole.channel.create": true, "wormhole.channel.post": true, "wormhole.channel.subscribe": true,
		"wormhole.kb.write": true,
	}
	for toolName, record := range byTool {
		if record.MutatesState != mutating[toolName] {
			t.Fatalf("%s mutates_state = %t, want %t", toolName, record.MutatesState, mutating[toolName])
		}
	}
}

func assertCompleteToolGuidance(t *testing.T, record toolGuidance) {
	t.Helper()
	values := map[string]string{
		"tool name":                  record.ToolName,
		"concept":                    record.Concept,
		"purpose":                    record.Purpose,
		"use when":                   record.UseWhen,
		"do not use when":            record.DoNotUseWhen,
		"prerequisites":              record.Prerequisites,
		"freshness implications":     record.FreshnessImplications,
		"source-access implications": record.SourceAccessImplications,
		"recommended follow-up":      record.RecommendedFollowUp,
		"misuse warning":             record.MisuseWarning,
	}
	for field, value := range values {
		if value == "" {
			t.Fatalf("%s guidance has empty %s", record.ToolName, field)
		}
	}
	if record.MinimalExample == nil {
		t.Fatalf("%s guidance has no minimal example", record.ToolName)
	}
}

func validateGuidanceExample(t *testing.T, schema map[string]any, value any) {
	t.Helper()
	if err := guidanceExampleError(schema, value); err != nil {
		t.Fatal(err)
	}
}

func guidanceExampleError(schema map[string]any, value any) error {
	_, hasType := schema["type"]
	if anyOf, ok := schema["anyOf"].([]map[string]any); ok && !hasType {
		for _, alternative := range anyOf {
			if guidanceExampleError(alternative, value) == nil {
				return nil
			}
		}
		return fmt.Errorf("%#v matches no schema alternative", value)
	}
	if required := schemaStrings(schema["required"]); len(required) > 0 {
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("value %#v is not an object", value)
		}
		for _, name := range required {
			if _, ok := object[name]; !ok {
				return fmt.Errorf("object %#v misses required property %q", object, name)
			}
		}
	}

	typeName, _ := schema["type"].(string)
	switch typeName {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("value %#v is not an object", value)
		}
		properties, _ := schema["properties"].(map[string]any)
		if additionalProperties, present := schema["additionalProperties"].(bool); present && !additionalProperties {
			for name := range object {
				if _, ok := properties[name]; !ok {
					return fmt.Errorf("object includes disallowed property %q", name)
				}
			}
		}
		for name, propertyValue := range object {
			propertySchema, ok := properties[name].(map[string]any)
			if !ok {
				continue
			}
			if err := guidanceExampleError(propertySchema, propertyValue); err != nil {
				return fmt.Errorf("property %q: %w", name, err)
			}
		}
	case "array":
		values, ok := value.([]any)
		if !ok {
			return fmt.Errorf("value %#v is not an array", value)
		}
		if itemSchema, ok := schema["items"].(map[string]any); ok {
			for _, item := range values {
				if err := guidanceExampleError(itemSchema, item); err != nil {
					return err
				}
			}
		}
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("value %#v is not a string", value)
		}
	case "integer":
		if _, ok := value.(int); !ok {
			return fmt.Errorf("value %#v is not an integer", value)
		}
	case "number":
		switch value.(type) {
		case int, float64:
		default:
			return fmt.Errorf("value %#v is not a number", value)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("value %#v is not a boolean", value)
		}
	}
	if enum, ok := schema["enum"].([]any); ok && !containsSchemaValue(enum, value) {
		return fmt.Errorf("value %#v is outside enum %#v", value, enum)
	}
	if minLength, ok := schema["minLength"].(int); ok {
		stringValue, ok := value.(string)
		if !ok || len(stringValue) < minLength {
			return fmt.Errorf("value %#v is shorter than minLength %d", value, minLength)
		}
	}
	if anyOf, ok := schema["anyOf"].([]map[string]any); ok && hasType {
		for _, alternative := range anyOf {
			if guidanceExampleError(alternative, value) == nil {
				return nil
			}
		}
		return fmt.Errorf("%#v matches no required schema alternative", value)
	}
	return nil
}

func schemaStrings(value any) []string {
	strings, _ := value.([]string)
	return strings
}

func containsSchemaValue(values []any, value any) bool {
	for _, candidate := range values {
		if reflect.DeepEqual(candidate, value) {
			return true
		}
	}
	return false
}

func TestMinimalInputExampleUsesFirstSchemaAlternativeDeterministically(t *testing.T) {
	registry := newLocalRegistry(&Server{})
	tool, ok := registry.Get("wormhole.agent.register")
	if !ok {
		t.Fatal("agent.register is not registered")
	}
	first := minimalInputExample(tool)
	second := minimalInputExample(tool)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("schema-generated examples differ: %#v vs %#v", first, second)
	}
	keys := make([]string, 0, len(first))
	for key := range first {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"capabilities", "model", "owner", "permissions", "project_id", "repositories", "roles"}) {
		t.Fatalf("agent.register generated example fields = %v", keys)
	}
}

func TestGuidanceExampleErrorRejectsLiveSchemaViolations(t *testing.T) {
	registry := newLocalRegistry(&Server{})

	t.Run("closed Code Graph schema with extra property", func(t *testing.T) {
		tool, ok := registry.Get("wormhole.code_graph.query")
		if !ok {
			t.Fatal("code_graph.query is not registered")
		}
		example := minimalInputExample(tool)
		example["unexpected"] = true
		if err := guidanceExampleError(buildInputSchema(tool), example); err == nil {
			t.Fatalf("closed schema accepted extra property: %#v", example)
		}
	})

	t.Run("Code Graph query without intent or entry symbols", func(t *testing.T) {
		tool, ok := registry.Get("wormhole.code_graph.query")
		if !ok {
			t.Fatal("code_graph.query is not registered")
		}
		example := minimalInputExample(tool)
		delete(example, "intent")
		delete(example, "entry_symbols")
		if err := guidanceExampleError(buildInputSchema(tool), example); err == nil {
			t.Fatalf("query schema accepted neither intent nor entry_symbols: %#v", example)
		}
	})

	t.Run("agent register join without owner or name", func(t *testing.T) {
		tool, ok := registry.Get("wormhole.agent.register")
		if !ok {
			t.Fatal("agent.register is not registered")
		}
		example := minimalInputExample(tool)
		delete(example, "owner")
		delete(example, "name")
		if err := guidanceExampleError(buildInputSchema(tool), example); err == nil {
			t.Fatalf("join schema accepted neither owner nor name: %#v", example)
		}
	})
}
