package main

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

type alphaPlannedIntegrationManifest struct {
	Status                string                     `json:"status"`
	DesignDocument        string                     `json:"design_document"`
	ManifestSchema        alphaPlannedManifestSchema `json:"manifest_schema"`
	EntrySchema           alphaPlannedEntrySchema    `json:"entry_schema"`
	ManagedSectionMarkers alphaPlannedMarkers        `json:"managed_section_markers"`
	CLICommands           []alphaPlannedCommand      `json:"cli_commands"`
	MCPTool               alphaPlannedMCPTool        `json:"mcp_tool"`
}

type alphaPlannedManifestSchema struct {
	AdditionalProperties bool     `json:"additional_properties"`
	Required             []string `json:"required"`
	SchemaVersion        int      `json:"schema_version"`
	Source               string   `json:"source"`
	DigestFormat         string   `json:"digest_format"`
	ManifestDigestInput  string   `json:"manifest_digest_input"`
	EntriesMin           int      `json:"entries_min"`
	EntriesMax           int      `json:"entries_max"`
	TotalContentMaxBytes int      `json:"total_content_max_bytes"`
}

type alphaPlannedEntrySchema struct {
	AdditionalProperties bool              `json:"additional_properties"`
	Required             []string          `json:"required"`
	Kinds                []string          `json:"kinds"`
	Targets              map[string]string `json:"targets"`
	MergePolicies        map[string]string `json:"merge_policies"`
	ContentEncoding      string            `json:"content_encoding"`
	ContentTerminator    string            `json:"content_terminator"`
	ContentMaxBytes      int               `json:"content_max_bytes"`
}

type alphaPlannedMarkers struct {
	Begin    string `json:"begin"`
	Metadata string `json:"metadata"`
	End      string `json:"end"`
}

type alphaPlannedCommand struct {
	Name      string   `json:"name"`
	Flags     []string `json:"flags"`
	ExitCodes []int    `json:"exit_codes"`
}

type alphaPlannedMCPTool struct {
	Name                string          `json:"name"`
	RequiredPermissions []string        `json:"required_permissions"`
	MutatesState        bool            `json:"mutates_state"`
	RequestSchema       json.RawMessage `json:"request_schema"`
	ResponseSchema      json.RawMessage `json:"response_schema"`
}

type alphaIntegrationDesignContract struct {
	DesignedInterfaces struct {
		IntegrationManifestV1 alphaPlannedIntegrationManifest `json:"integration_manifest_v1"`
	} `json:"designed_interfaces"`
	CLI struct {
		Commands []struct {
			Name string `json:"name"`
		} `json:"commands"`
	} `json:"cli"`
	MCPTools struct {
		Gateway []struct {
			Name string `json:"name"`
		} `json:"gateway"`
		Fabric []struct {
			Name string `json:"name"`
		} `json:"fabric"`
	} `json:"mcp_tools"`
}

func TestAlphaContractIntegrationManifestDesignCompleteness(t *testing.T) {
	design, err := os.ReadFile("../../docs/architecture/integration-manifest-design.md")
	if err != nil {
		t.Fatalf("read integration-manifest design: %v", err)
	}
	for _, required := range []string{
		"## Strict version-one schemas",
		"### Manifest schema",
		"### Entry schema and target matrix",
		"## Canonical digests",
		"## Trust and binding selection",
		"## Ownership and safe materialisation",
		"## Lifecycle, offline operation, revocation, and rollback",
		"## CLI contract",
		"## Read-only MCP contract",
		"## Audit contract",
		"## Threat model and prohibited capabilities",
		"## Compatibility and cache",
		"## Test strategy",
		"<!-- wormhole:managed-begin integration-manifest/v1 -->",
		"<!-- wormhole:manifest id=<uuid> version=<n> digest=sha256:<hex> -->",
		"<!-- wormhole:managed-end integration-manifest/v1 -->",
		"wormhole.agent.get_guidance",
		"RFC 8785",
		"detached signatures",
		"hard links",
		"executable scripts",
		"post-install hooks",
		"arbitrary environment mutation",
		"descriptor-relative",
		"held root and ancestor directory handles",
		"post-operation identity",
		"removal_required",
		"integration_manifest.revocation_removal_required",
	} {
		if !strings.Contains(string(design), required) {
			t.Errorf("design is missing %q", required)
		}
	}
	for _, command := range []string{
		"wormhole integration preview",
		"wormhole integration apply",
		"wormhole integration status",
		"wormhole integration update",
		"wormhole integration remove",
		"wormhole integration rollback",
	} {
		if !strings.Contains(string(design), command) {
			t.Errorf("design is missing command %q", command)
		}
	}
}

func TestAlphaContractIntegrationManifestV1MaterializationAndGuidanceToolAreLive(t *testing.T) {
	data, err := os.ReadFile("../../docs/contracts/alpha-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var inventory alphaIntegrationDesignContract
	if err := json.Unmarshal(data, &inventory); err != nil {
		t.Fatal(err)
	}
	planned := inventory.DesignedInterfaces.IntegrationManifestV1
	if planned.Status != "materialization_and_guidance_tool_implemented_cache_binding_planned" || planned.DesignDocument != "docs/architecture/integration-manifest-design.md" {
		t.Fatalf("planned integration manifest status/document = %q/%q", planned.Status, planned.DesignDocument)
	}
	wantManifestFields := []string{
		"created_at", "entries", "manifest_digest", "manifest_id", "manifest_version",
		"project_id", "role_filters", "schema_version", "source", "tool_contract_digest",
	}
	if planned.ManifestSchema.AdditionalProperties ||
		!reflect.DeepEqual(planned.ManifestSchema.Required, wantManifestFields) ||
		planned.ManifestSchema.SchemaVersion != 1 || planned.ManifestSchema.Source != "fabric" ||
		planned.ManifestSchema.DigestFormat != "sha256:<64 lowercase hex>" ||
		planned.ManifestSchema.ManifestDigestInput != "RFC8785/JCS manifest object omitting manifest_digest" ||
		planned.ManifestSchema.EntriesMin != 1 || planned.ManifestSchema.EntriesMax != 64 ||
		planned.ManifestSchema.TotalContentMaxBytes != 1048576 {
		t.Fatalf("planned manifest schema is incomplete: %+v", planned.ManifestSchema)
	}
	wantEntryFields := []string{"content", "content_digest", "kind", "merge_policy", "required", "role_filters", "target"}
	wantKinds := []string{"agents_bootstrap", "reference", "skill"}
	wantTargets := map[string]string{
		"agents_bootstrap": "AGENTS.md",
		"reference":        ".agents/skills/wormhole-<skill-slug>/references/<reference-slug>.md",
		"skill":            ".agents/skills/wormhole-<skill-slug>/SKILL.md",
	}
	wantMergePolicies := map[string]string{"agents_bootstrap": "managed_section", "reference": "managed_file", "skill": "managed_file"}
	if planned.EntrySchema.AdditionalProperties ||
		!reflect.DeepEqual(planned.EntrySchema.Required, wantEntryFields) ||
		!reflect.DeepEqual(planned.EntrySchema.Kinds, wantKinds) ||
		!reflect.DeepEqual(planned.EntrySchema.Targets, wantTargets) ||
		!reflect.DeepEqual(planned.EntrySchema.MergePolicies, wantMergePolicies) ||
		planned.EntrySchema.ContentEncoding != "UTF-8 Markdown" ||
		planned.EntrySchema.ContentTerminator != "exactly one trailing LF; no CR or NUL" ||
		planned.EntrySchema.ContentMaxBytes != 262144 {
		t.Fatalf("planned entry schema is incomplete: %+v", planned.EntrySchema)
	}
	wantMarkers := alphaPlannedMarkers{
		Begin:    "<!-- wormhole:managed-begin integration-manifest/v1 -->",
		Metadata: "<!-- wormhole:manifest id=<uuid> version=<n> digest=sha256:<hex> -->",
		End:      "<!-- wormhole:managed-end integration-manifest/v1 -->",
	}
	if planned.ManagedSectionMarkers != wantMarkers {
		t.Fatalf("planned markers = %+v, want %+v", planned.ManagedSectionMarkers, wantMarkers)
	}
	wantCommands := []alphaPlannedCommand{
		{Name: "integration apply", Flags: []string{"confirm-digest", "project"}, ExitCodes: []int{0, 1, 2}},
		{Name: "integration preview", Flags: []string{"project"}, ExitCodes: []int{0, 1, 2}},
		{Name: "integration remove", Flags: []string{"confirm-digest", "project"}, ExitCodes: []int{0, 1, 2}},
		{Name: "integration rollback", Flags: []string{"confirm-digest", "project"}, ExitCodes: []int{0, 1, 2}},
		{Name: "integration status", Flags: []string{"json", "project"}, ExitCodes: []int{0, 1, 2}},
		{Name: "integration update", Flags: []string{"confirm-digest", "project"}, ExitCodes: []int{0, 1, 2}},
	}
	if !reflect.DeepEqual(planned.CLICommands, wantCommands) {
		t.Fatalf("planned CLI commands = %+v, want %+v", planned.CLICommands, wantCommands)
	}
	if planned.MCPTool.Name != "wormhole.agent.get_guidance" ||
		!reflect.DeepEqual(planned.MCPTool.RequiredPermissions, []string{}) || planned.MCPTool.MutatesState {
		t.Fatalf("planned MCP identity/permissions/mutation = %+v", planned.MCPTool)
	}
	assertPlannedJSONSchema(t, "request", planned.MCPTool.RequestSchema, plannedGuidanceRequestSchema)
	assertPlannedJSONSchema(t, "response", planned.MCPTool.ResponseSchema, plannedGuidanceResponseSchema)
	liveCommands := map[string]bool{}
	for _, command := range inventory.CLI.Commands {
		liveCommands[command.Name] = true
	}
	for _, command := range planned.CLICommands {
		if !liveCommands[command.Name] {
			t.Errorf("implemented CLI command %q is missing from the live inventory", command.Name)
		}
	}
	foundGateway := false
	for _, tool := range inventory.MCPTools.Gateway {
		foundGateway = foundGateway || tool.Name == planned.MCPTool.Name
	}
	if !foundGateway {
		t.Errorf("implemented MCP tool %q is missing from the live Gateway inventory", planned.MCPTool.Name)
	}
	for _, tool := range inventory.MCPTools.Fabric {
		if tool.Name == planned.MCPTool.Name {
			t.Errorf("local-only MCP tool %q appears in the live Fabric inventory", tool.Name)
		}
	}
}

func assertPlannedJSONSchema(t *testing.T, name string, got json.RawMessage, wantJSON string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode planned %s schema: %v", name, err)
	}
	if err := json.Unmarshal([]byte(wantJSON), &wantValue); err != nil {
		t.Fatalf("decode expected %s schema: %v", name, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		gotJSON, _ := json.MarshalIndent(gotValue, "", "  ")
		want, _ := json.MarshalIndent(wantValue, "", "  ")
		t.Fatalf("planned %s schema drifted\ngot:\n%s\nwant:\n%s", name, gotJSON, want)
	}
}

const plannedGuidanceRequestSchema = `{
  "type": "object",
  "properties": [
    {"name": "project_id", "schema": {"type": "string", "format": "uuid"}}
  ],
  "required": ["project_id"],
  "additional_properties": false
}`

const plannedGuidanceResponseSchema = `{
  "type": "object",
  "properties": [
    {"name": "approval_state", "schema": {"type": "string", "enum": ["approved", "awaiting_approval", "none", "offered", "postponed", "rejected", "revoked", "verification_failed", "verified"]}},
    {"name": "connection_state", "schema": {"type": "string", "enum": ["attention_required", "offline", "online", "synchronizing"]}},
    {"name": "guidance", "schema": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": [
          {"name": "content", "schema": {"type": "string", "format": "utf8-markdown"}},
          {"name": "content_digest", "schema": {"type": "string", "format": "sha256"}},
          {"name": "kind", "schema": {"type": "string", "enum": ["agents_bootstrap", "reference", "skill"]}},
          {"name": "required", "schema": {"type": "boolean"}}
        ],
        "required": ["content", "content_digest", "kind", "required"],
        "additional_properties": false
      }
    }},
    {"name": "last_verified_at", "schema": {"anyOf": [{"type": "string", "format": "date-time"}, {"type": "null"}]}},
    {"name": "manifest_digest", "schema": {"anyOf": [{"type": "string", "format": "sha256"}, {"type": "null"}]}},
    {"name": "manifest_id", "schema": {"anyOf": [{"type": "string", "format": "uuid"}, {"type": "null"}]}},
    {"name": "manifest_version", "schema": {"anyOf": [{"type": "integer", "minimum": 1}, {"type": "null"}]}},
    {"name": "materialization_state", "schema": {"type": "string", "enum": ["applied", "drifted", "not_applied", "recovery_required", "removal_required"]}},
    {"name": "pending_manifest_digest", "schema": {"anyOf": [{"type": "string", "format": "sha256"}, {"type": "null"}]}},
    {"name": "pending_manifest_version", "schema": {"anyOf": [{"type": "integer", "minimum": 1}, {"type": "null"}]}},
    {"name": "project_id", "schema": {"type": "string", "format": "uuid"}},
    {"name": "resolved_role", "schema": {"anyOf": [{"type": "string", "format": "role-slug"}, {"type": "null"}]}},
    {"name": "schema_version", "schema": {"type": "integer", "const": 1}}
  ],
  "required": ["approval_state", "connection_state", "guidance", "last_verified_at", "manifest_digest", "manifest_id", "manifest_version", "materialization_state", "pending_manifest_digest", "pending_manifest_version", "project_id", "resolved_role", "schema_version"],
  "additional_properties": false
}`
