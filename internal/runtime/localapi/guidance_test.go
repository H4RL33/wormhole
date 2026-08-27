package localapi

import (
	"context"
	"encoding/json"
	"errors"
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

func TestIntegrationGuidanceReadsApprovedRoleFilteredCacheOfflineWithoutMutation(t *testing.T) {
	const projectID = "e724dd25-5bc9-40db-bcad-0b21716d1ca4"
	manifestID := "52b860cd-0db7-4ee0-a3fd-672ad9da0c95"
	manifestDigest := "sha256:719a185b670128590f3522d2b34e2c213edf0305a114de3ca53661141826e054"
	pendingVersion := int64(2)
	pendingDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	manifestVersion := int64(1)
	manifest := IntegrationManifest{
		SchemaVersion: 1, ManifestID: manifestID, ManifestVersion: manifestVersion,
		ProjectID: projectID, ManifestDigest: manifestDigest, RoleFilters: []string{},
		Entries: []IntegrationManifestEntry{
			{Kind: "agents_bootstrap", Target: "AGENTS.md", Content: "Contributor operating guidance.\n", ContentDigest: materializationDigest([]byte("Contributor operating guidance.\n")), MergePolicy: "managed_section", Required: true, RoleFilters: []string{"contributor"}},
			{Kind: "skill", Target: ".agents/skills/wormhole-orientation/SKILL.md", Content: "Shared orientation.\n", ContentDigest: materializationDigest([]byte("Shared orientation.\n")), MergePolicy: "managed_file", Required: true, RoleFilters: []string{}},
			{Kind: "skill", Target: ".agents/skills/wormhole-reviewer/SKILL.md", Content: "Reviewer-only guidance.\n", ContentDigest: materializationDigest([]byte("Reviewer-only guidance.\n")), MergePolicy: "managed_file", Required: true, RoleFilters: []string{"reviewer"}},
		},
	}
	provider := &recordingIntegrationGuidanceProvider{snapshot: IntegrationGuidanceSnapshot{
		State: IntegrationState{
			SchemaVersion: 1, ProjectID: projectID,
			ActiveManifestID: &manifestID, ActiveManifestVersion: &manifestVersion, ActiveManifestDigest: &manifestDigest,
			PendingManifestVersion: &pendingVersion, PendingManifestDigest: &pendingDigest,
			ResolvedRole: "contributor", ApprovalState: "awaiting_approval", MaterializationState: "applied",
			ConnectionState: "offline", GuidanceActive: true, CompatibilityState: "compatible",
			LastVerifiedAt: "2026-07-26T12:00:00.123456789Z",
		},
		Manifest: &manifest,
	}}
	before := provider.snapshot
	srv := &Server{projectID: projectID}
	srv.SetIntegrationGuidanceProvider(provider)

	result, err := srv.handleIntegrationGuidance(context.Background(), json.RawMessage(`{"project_id":"`+projectID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != 1 || result.ProjectID != projectID || result.ManifestID == nil || *result.ManifestID != manifestID ||
		result.ManifestVersion == nil || *result.ManifestVersion != 1 || result.ManifestDigest == nil || *result.ManifestDigest != manifestDigest ||
		result.ResolvedRole == nil || *result.ResolvedRole != "contributor" || result.MaterializationState != "applied" ||
		result.ApprovalState != "awaiting_approval" || result.PendingManifestVersion == nil || *result.PendingManifestVersion != 2 ||
		result.PendingManifestDigest == nil || *result.PendingManifestDigest != pendingDigest || result.ConnectionState != "offline" ||
		result.LastVerifiedAt == nil || *result.LastVerifiedAt != "2026-07-26T12:00:00.123456789Z" {
		t.Fatalf("guidance result metadata = %+v", result)
	}
	wantGuidance := []integrationGuidanceItem{
		{Kind: "skill", Content: "Shared orientation.\n", ContentDigest: materializationDigest([]byte("Shared orientation.\n")), Required: true},
		{Kind: "agents_bootstrap", Content: "Contributor operating guidance.\n", ContentDigest: materializationDigest([]byte("Contributor operating guidance.\n")), Required: true},
	}
	if !reflect.DeepEqual(result.Guidance, wantGuidance) {
		t.Fatalf("applicable guidance = %+v, want %+v", result.Guidance, wantGuidance)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "target") || strings.Contains(string(encoded), ".agents/") || strings.Contains(string(encoded), "merge_policy") {
		t.Fatalf("guidance response exposed repository materialisation fields: %s", encoded)
	}
	if !reflect.DeepEqual(provider.snapshot, before) {
		t.Fatalf("read mutated provider snapshot\nbefore=%+v\nafter=%+v", before, provider.snapshot)
	}
	if !reflect.DeepEqual(provider.projects, []string{projectID}) {
		t.Fatalf("provider reads = %v, want one project-scoped read", provider.projects)
	}
}

func TestIntegrationGuidanceHidesUnavailableRevokedAndIncompatibleContent(t *testing.T) {
	const projectID = "e724dd25-5bc9-40db-bcad-0b21716d1ca4"
	manifestID := "52b860cd-0db7-4ee0-a3fd-672ad9da0c95"
	manifestVersion := int64(1)
	manifestDigest := "sha256:719a185b670128590f3522d2b34e2c213edf0305a114de3ca53661141826e054"
	pendingVersion := int64(2)
	pendingDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	manifest := materializationTestManifest("Approved guidance.\n")

	tests := []struct {
		name           string
		snapshot       IntegrationGuidanceSnapshot
		wantApproval   string
		wantMaterial   string
		wantConnection string
		wantActive     bool
		wantPending    bool
	}{
		{
			name: "no approved cache with newer offered version",
			snapshot: IntegrationGuidanceSnapshot{State: IntegrationState{
				SchemaVersion: 1, ProjectID: projectID, ResolvedRole: "contributor", ApprovalState: "offered",
				MaterializationState: "not_applied", ConnectionState: "online",
				PendingManifestVersion: &pendingVersion, PendingManifestDigest: &pendingDigest,
			}},
			wantApproval: "offered", wantMaterial: "not_applied", wantConnection: "online", wantPending: true,
		},
		{
			name: "revoked before removal finishes",
			snapshot: IntegrationGuidanceSnapshot{State: IntegrationState{
				SchemaVersion: 1, ProjectID: projectID, ActiveManifestID: &manifestID, ActiveManifestVersion: &manifestVersion,
				ActiveManifestDigest: &manifestDigest, ResolvedRole: "contributor", ApprovalState: "revoked",
				MaterializationState: "removal_required", ConnectionState: "attention_required", GuidanceActive: false,
				CompatibilityState: "compatible",
			}, Manifest: &manifest},
			wantApproval: "revoked", wantMaterial: "removal_required", wantConnection: "attention_required", wantActive: true,
		},
		{
			name: "approved cache incompatible with current tools",
			snapshot: IntegrationGuidanceSnapshot{State: IntegrationState{
				SchemaVersion: 1, ProjectID: projectID, ActiveManifestID: &manifestID, ActiveManifestVersion: &manifestVersion,
				ActiveManifestDigest: &manifestDigest, ResolvedRole: "contributor", ApprovalState: "approved",
				MaterializationState: "applied", ConnectionState: "online", GuidanceActive: true,
				CompatibilityState: "tool_contract_mismatch",
			}, Manifest: &manifest},
			wantApproval: "approved", wantMaterial: "applied", wantConnection: "attention_required", wantActive: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &recordingIntegrationGuidanceProvider{snapshot: test.snapshot}
			srv := &Server{projectID: projectID}
			srv.SetIntegrationGuidanceProvider(provider)
			result, err := srv.handleIntegrationGuidance(context.Background(), json.RawMessage(`{"project_id":"`+projectID+`"}`))
			if err != nil {
				t.Fatal(err)
			}
			if result.Guidance == nil || len(result.Guidance) != 0 {
				t.Fatalf("inactive guidance = %#v, want non-nil empty array", result.Guidance)
			}
			if result.ApprovalState != test.wantApproval || result.MaterializationState != test.wantMaterial || result.ConnectionState != test.wantConnection {
				t.Fatalf("inactive states = %q/%q/%q", result.ApprovalState, result.MaterializationState, result.ConnectionState)
			}
			if (result.ManifestID != nil) != test.wantActive || (result.PendingManifestVersion != nil) != test.wantPending {
				t.Fatalf("active/pending visibility = %v/%v", result.ManifestID, result.PendingManifestVersion)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var object map[string]any
			if err := json.Unmarshal(encoded, &object); err != nil {
				t.Fatal(err)
			}
			if got := sortedKeys(object); !reflect.DeepEqual(got, []string{"approval_state", "connection_state", "guidance", "last_verified_at", "manifest_digest", "manifest_id", "manifest_version", "materialization_state", "pending_manifest_digest", "pending_manifest_version", "project_id", "resolved_role", "schema_version"}) {
				t.Fatalf("response keys = %v", got)
			}
		})
	}
}

func TestIntegrationGuidanceRejectsMutationInputsAndInvalidProjectWithoutReading(t *testing.T) {
	const projectID = "e724dd25-5bc9-40db-bcad-0b21716d1ca4"
	for _, raw := range []string{
		`{}`,
		`{"project_id":"not-a-uuid"}`,
		`{"project_id":"00000000-0000-0000-0000-000000000000"}`,
		`{"project_id":"E724DD25-5BC9-40DB-BCAD-0B21716D1CA4"}`,
		`{"project_id":"` + projectID + `","role":"reviewer"}`,
		`{"project_id":"` + projectID + `","approve":true}`,
		`{"project_id":"` + projectID + `"} {}`,
	} {
		provider := &recordingIntegrationGuidanceProvider{}
		srv := &Server{projectID: projectID}
		srv.SetIntegrationGuidanceProvider(provider)
		if _, err := srv.handleIntegrationGuidance(context.Background(), json.RawMessage(raw)); err == nil {
			t.Fatalf("get_guidance accepted %s", raw)
		}
		if len(provider.projects) != 0 {
			t.Fatalf("invalid request %s read provider for %v", raw, provider.projects)
		}
	}
}

func TestIntegrationGuidanceFailsClosedOnProviderAndProjectMismatch(t *testing.T) {
	const projectID = "e724dd25-5bc9-40db-bcad-0b21716d1ca4"
	request := json.RawMessage(`{"project_id":"` + projectID + `"}`)
	if _, err := (&Server{projectID: projectID}).handleIntegrationGuidance(context.Background(), request); err == nil {
		t.Fatal("get_guidance succeeded without authoritative cache provider")
	}

	provider := &recordingIntegrationGuidanceProvider{err: errors.New("cache unavailable")}
	srv := &Server{projectID: projectID}
	srv.SetIntegrationGuidanceProvider(provider)
	if _, err := srv.handleIntegrationGuidance(context.Background(), request); err == nil || !strings.Contains(err.Error(), "cache unavailable") {
		t.Fatalf("provider failure = %v", err)
	}

	provider = &recordingIntegrationGuidanceProvider{snapshot: IntegrationGuidanceSnapshot{State: IntegrationState{
		SchemaVersion: 1, ProjectID: "f724dd25-5bc9-40db-bcad-0b21716d1ca4", ApprovalState: "none",
		MaterializationState: "not_applied", ConnectionState: "online",
	}}}
	srv.SetIntegrationGuidanceProvider(provider)
	if _, err := srv.handleIntegrationGuidance(context.Background(), request); err == nil || !strings.Contains(err.Error(), "project") {
		t.Fatalf("provider project mismatch = %v", err)
	}
}

func TestIntegrationGuidanceSanitizesInvalidCachedManifestErrors(t *testing.T) {
	const projectID = "e724dd25-5bc9-40db-bcad-0b21716d1ca4"
	manifest := materializationTestManifest("Approved guidance.\n")
	manifest.Entries[1].Target = ".agents/skills/private-repository-layout/SKILL.md"
	manifest.Entries[1].ContentDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	manifestID := manifest.ManifestID
	manifestVersion := manifest.ManifestVersion
	manifestDigest := manifest.ManifestDigest
	provider := &recordingIntegrationGuidanceProvider{snapshot: IntegrationGuidanceSnapshot{
		State: IntegrationState{
			SchemaVersion: 1, ProjectID: projectID,
			ActiveManifestID: &manifestID, ActiveManifestVersion: &manifestVersion, ActiveManifestDigest: &manifestDigest,
			ResolvedRole: "contributor", ApprovalState: "approved", MaterializationState: "applied",
			ConnectionState: "online", GuidanceActive: true, CompatibilityState: "compatible",
		},
		Manifest: &manifest,
	}}
	srv := &Server{projectID: projectID}
	srv.SetIntegrationGuidanceProvider(provider)

	_, err := srv.handleIntegrationGuidance(context.Background(), json.RawMessage(`{"project_id":"`+projectID+`"}`))
	if err == nil {
		t.Fatal("get_guidance accepted an invalid cached manifest")
	}
	if got, want := err.Error(), "integration guidance: cached approved manifest is invalid"; got != want {
		t.Fatalf("caller-visible cache validation error = %q, want %q", got, want)
	}
	for _, leaked := range []string{"private-repository-layout", ".agents/", "SKILL.md", "target", "merge policy"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("caller-visible cache validation error leaked %q: %v", leaked, err)
		}
	}
}

type recordingIntegrationGuidanceProvider struct {
	snapshot IntegrationGuidanceSnapshot
	err      error
	projects []string
}

func (provider *recordingIntegrationGuidanceProvider) ReadIntegrationGuidance(_ context.Context, projectID string) (IntegrationGuidanceSnapshot, error) {
	provider.projects = append(provider.projects, projectID)
	return provider.snapshot, provider.err
}

func assertGatewayToolGuidance(t *testing.T, registry *localRegistry) {
	t.Helper()
	tools := registry.List()
	guidance := registry.Guidance()
	if len(tools) != 17 {
		t.Fatalf("live Gateway tool count = %d, want 17", len(tools))
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

	assertGuidanceMutationSet(t, byTool)
}

var allowedGuidanceConcepts = map[string]bool{
	"identity": true, "channels and events": true, "knowledge": true,
	"local status and synchronisation": true, "portable workspace": true,
}

func assertGuidanceMutationSet(t *testing.T, byTool map[string]toolGuidance) {
	t.Helper()
	mutating := map[string]bool{
		"wormhole.agent.register": true, "wormhole.agent.presence": true,
		"wormhole.workspace.import": true, "wormhole.workspace.checkpoint": true, "wormhole.workspace.stash": true,
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
	if !reflect.DeepEqual(keys, []string{"agent_id", "project_id"}) {
		t.Fatalf("agent.register generated example fields = %v", keys)
	}
}

func TestGuidanceExampleErrorRejectsLiveSchemaViolations(t *testing.T) {
	registry := newLocalRegistry(&Server{})

	t.Run("closed workspace schema with extra property", func(t *testing.T) {
		tool, ok := registry.Get("wormhole.workspace.checkpoint")
		if !ok {
			t.Fatal("workspace checkpoint is not registered")
		}
		example := minimalInputExample(tool)
		example["unexpected"] = true
		if err := guidanceExampleError(buildInputSchema(tool), example); err == nil {
			t.Fatalf("closed schema accepted extra property: %#v", example)
		}
	})

	t.Run("agent register without agent id", func(t *testing.T) {
		tool, ok := registry.Get("wormhole.agent.register")
		if !ok {
			t.Fatal("agent.register is not registered")
		}
		example := minimalInputExample(tool)
		delete(example, "agent_id")
		if err := guidanceExampleError(buildInputSchema(tool), example); err == nil {
			t.Fatalf("presence schema accepted no agent_id: %#v", example)
		}
	})
}
