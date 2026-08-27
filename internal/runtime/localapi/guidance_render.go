package localapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

type generatedGuidanceFile struct {
	Slug        string
	Target      string
	Content     string
	Required    bool
	RoleFilters []string
}

type generatedGuidance struct {
	Files    []generatedGuidanceFile
	Manifest generatedGuidanceManifest
}

type generatedGuidanceManifest struct {
	SchemaVersion      int                              `json:"schema_version"`
	ManifestID         string                           `json:"manifest_id"`
	ManifestVersion    int                              `json:"manifest_version"`
	ProjectID          string                           `json:"project_id"`
	Source             string                           `json:"source"`
	CreatedAt          string                           `json:"created_at"`
	ToolContractDigest string                           `json:"tool_contract_digest"`
	ManifestDigest     string                           `json:"manifest_digest"`
	RoleFilters        []string                         `json:"role_filters"`
	Entries            []generatedGuidanceManifestEntry `json:"entries"`
}

type generatedGuidanceManifestEntry struct {
	Kind          string   `json:"kind"`
	Target        string   `json:"target"`
	Content       string   `json:"content"`
	ContentDigest string   `json:"content_digest"`
	MergePolicy   string   `json:"merge_policy"`
	Required      bool     `json:"required"`
	RoleFilters   []string `json:"role_filters"`
}

type generatedContractTool struct {
	Name                string                     `json:"name"`
	RequiredPermissions []string                   `json:"required_permissions"`
	RequestSchemas      []generatedContractVariant `json:"request_schemas"`
	ResponseSchemas     []generatedContractVariant `json:"response_schemas"`
}

type generatedContractVariant struct {
	Variant string                  `json:"variant"`
	Schema  generatedContractSchema `json:"schema"`
}

type generatedContractSchema struct {
	Type                 string                      `json:"type,omitempty"`
	Format               string                      `json:"format,omitempty"`
	Enum                 []string                    `json:"enum,omitempty"`
	BooleanEnum          []bool                      `json:"boolean_enum,omitempty"`
	Properties           []generatedContractProperty `json:"properties,omitempty"`
	Required             []string                    `json:"required,omitempty"`
	Items                *generatedContractSchema    `json:"items,omitempty"`
	AnyOf                []generatedContractSchema   `json:"anyOf,omitempty"`
	AdditionalProperties *bool                       `json:"additional_properties,omitempty"`
	MinLength            int                         `json:"min_length,omitempty"`
	Minimum              int                         `json:"minimum,omitempty"`
	Const                int                         `json:"const,omitempty"`
}

type generatedContractProperty struct {
	Name   string                  `json:"name"`
	Schema generatedContractSchema `json:"schema"`
}

type generatedSkillSpec struct {
	slug        string
	target      string
	description string
	roles       []string
	render      func([]localTool, map[string]toolGuidance) (string, error)
}

func renderGatewayGuidance(registry *localRegistry, records []toolGuidance) (generatedGuidance, error) {
	tools := registry.List()
	byTool, err := validateRenderedGuidance(tools, records)
	if err != nil {
		return generatedGuidance{}, err
	}
	sortedTools := append([]localTool(nil), tools...)
	sort.Slice(sortedTools, func(i, j int) bool { return sortedTools[i].Name < sortedTools[j].Name })

	specs := []generatedSkillSpec{
		{"wormhole-orientation", ".agents/skills/wormhole-orientation/SKILL.md", "starting work in a project connected to Wormhole or reconstructing shared project context.", []string{}, renderOrientationSkill},
		{"wormhole-tool-use", ".agents/skills/wormhole-tool-use/SKILL.md", "choosing or calling a live Wormhole Gateway tool and checking its permissions, schema, freshness, or side effects.", []string{}, renderToolUseSkill},
		{"wormhole-operating-loop", ".agents/skills/wormhole-operating-loop/SKILL.md", "carrying work from session orientation through implementation, durable reporting, verification, and handoff.", []string{}, renderOperatingLoopSkill},
		{"wormhole-contributor", ".agents/skills/wormhole-contributor/SKILL.md", "acting as the contributor assigned to implement a scoped Wormhole Task.", []string{"contributor"}, renderContributorSkill},
		{"wormhole-reviewer", ".agents/skills/wormhole-reviewer/SKILL.md", "reviewing another agent's Wormhole Task, implementation, evidence, or handoff.", []string{"reviewer"}, renderReviewerSkill},
	}

	files := make([]generatedGuidanceFile, 0, len(specs))
	for _, spec := range specs {
		body, err := spec.render(sortedTools, byTool)
		if err != nil {
			return generatedGuidance{}, fmt.Errorf("render %s: %w", spec.slug, err)
		}
		files = append(files, generatedGuidanceFile{
			Slug: spec.slug, Target: spec.target,
			Content:  skillDocument(spec.slug, spec.description, body),
			Required: true, RoleFilters: append([]string{}, spec.roles...),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Target < files[j].Target })

	toolDigest, err := generatedToolContractDigest(sortedTools)
	if err != nil {
		return generatedGuidance{}, err
	}
	manifest := generatedGuidanceManifest{
		SchemaVersion: 1,
		ManifestID:    "52b860cd-0db7-4ee0-a3fd-672ad9da0c95", ManifestVersion: 1,
		ProjectID: "e724dd25-5bc9-40db-bcad-0b21716d1ca4", Source: "fabric",
		CreatedAt: "2026-07-26T12:00:00Z", ToolContractDigest: toolDigest,
		RoleFilters: []string{}, Entries: make([]generatedGuidanceManifestEntry, 0, len(files)),
	}
	for _, file := range files {
		manifest.Entries = append(manifest.Entries, generatedGuidanceManifestEntry{
			Kind: "skill", Target: file.Target, Content: file.Content,
			ContentDigest: digestString([]byte(file.Content)), MergePolicy: "managed_file",
			Required: file.Required, RoleFilters: append([]string{}, file.RoleFilters...),
		})
	}
	manifest.ManifestDigest, err = generatedManifestDigest(manifest)
	if err != nil {
		return generatedGuidance{}, err
	}
	return generatedGuidance{Files: files, Manifest: manifest}, nil
}

func validateRenderedGuidance(tools []localTool, records []toolGuidance) (map[string]toolGuidance, error) {
	live := make(map[string]localTool, len(tools))
	for _, tool := range tools {
		live[tool.Name] = tool
	}
	byTool := make(map[string]toolGuidance, len(records))
	for _, record := range records {
		if _, ok := live[record.ToolName]; !ok {
			return nil, fmt.Errorf("guidance for non-live tool %s", record.ToolName)
		}
		if _, duplicate := byTool[record.ToolName]; duplicate {
			return nil, fmt.Errorf("duplicate guidance for tool %s", record.ToolName)
		}
		byTool[record.ToolName] = record
	}
	for _, tool := range tools {
		if _, ok := byTool[tool.Name]; !ok {
			return nil, fmt.Errorf("missing guidance for tool %s", tool.Name)
		}
	}
	if len(byTool) != len(live) {
		return nil, fmt.Errorf("guidance count %d does not match live tool count %d", len(byTool), len(live))
	}
	return byTool, nil
}

func skillDocument(name, description, body string) string {
	return "---\nname: " + name + "\ndescription: Use when " + description + "\n---\n\n" + strings.TrimSpace(body) + "\n"
}

func renderOrientationSkill(_ []localTool, _ map[string]toolGuidance) (string, error) {
	return `# Wormhole orientation

Wormhole stores shared organisational context, not source code. Git and the
current working tree remain authoritative for source.

- Gateway is the local MCP endpoint for every live agent-facing call.
- This Stage 2 inventory is local-only and does not contact optional Fabric.
- Portable channels and KB articles live in tracked project state after ordinary Git acceptance.
- Channel activity, agent registration, and presence remain clone-private operational state.
- Workspace tools inspect, import, diff, checkpoint, and stash portable candidates.

Consult live KB and channel context before broad repository exploration when
that context could answer the question. Verify every code conclusion against
Git and current source.`, nil
}

func renderToolUseSkill(tools []localTool, guidance map[string]toolGuidance) (string, error) {
	var out strings.Builder
	out.WriteString("# Wormhole Gateway tool use\n\nCall only tools in this live Gateway inventory. Each request still requires its live schema and permissions.\n\n")
	out.WriteString("## Portable local context\n\nUse kb.list and kb.get for deterministic portable KB reads; semantic Fabric search is not in this live Gateway inventory.\n")
	for _, tool := range tools {
		record := guidance[tool.Name]
		schema, err := compactJSON(buildInputSchema(tool))
		if err != nil {
			return "", err
		}
		example, err := compactJSON(record.MinimalExample)
		if err != nil {
			return "", err
		}
		permissions := "none"
		if len(tool.RequiredPermissions) > 0 {
			permissions = strings.Join(tool.RequiredPermissions, ", ")
		}
		fmt.Fprintf(&out, "\n## `%s`\n\n", tool.Name)
		fmt.Fprintf(&out, "- Purpose: %s\n- Use when: %s\n- Do not use when: %s\n", record.Purpose, record.UseWhen, record.DoNotUseWhen)
		fmt.Fprintf(&out, "- Mutates state: %t\n- Required permissions: %s\n- Prerequisites: %s\n", record.MutatesState, permissions, record.Prerequisites)
		fmt.Fprintf(&out, "- Freshness implications: %s\n- Source-access implications: %s\n", record.FreshnessImplications, record.SourceAccessImplications)
		fmt.Fprintf(&out, "- Recommended follow-up: %s\n- Minimal request example: `%s`\n", record.RecommendedFollowUp, example)
		fmt.Fprintf(&out, "- Live request schema: `%s`\n- Misuse warning: %s\n", schema, record.MisuseWarning)
	}
	return out.String(), nil
}

func renderOperatingLoopSkill(_ []localTool, _ map[string]toolGuidance) (string, error) {
	return `# Wormhole operating loop

Use only the live local-only Gateway inventory. Portable KB and channel
definitions become shared through checkpoint plus ordinary Git acceptance;
operational activity and presence remain clone-private.

## session start:

1. inspect workspace.status
2. retrieve relevant KB context with kb.list or kb.get
3. inspect recent clone-local channel.events when relevant
4. confirm intended work before broad exploration

## before changing code:

1. check portable decisions and constraints
2. preserve Git as source and acceptance authority
3. inspect workspace.diff before checkpointing

## during work:

1. record durable discoveries in KB only when appropriate
2. use channel.post only for clone-local operational activity
3. do not narrate every command
4. check for duplicate channels and KB articles before creating them

## completion:

1. run required verification
2. inspect the exact workspace.diff and publication review digest
3. checkpoint without staging, committing, or pushing Git
4. accept portable state through ordinary Git when appropriate
5. leave sufficient context for another agent`, nil
}

func renderContributorSkill(_ []localTool, _ map[string]toolGuidance) (string, error) {
	return `# Wormhole contributor

1. Begin with explicit work intent, decisions, constraints, and the current workspace status.
2. Keep a scoped implementation; do not silently expand or redesign the work.
3. Use channel activity only for concise clone-local operational context.
4. Run required verification against current Git and source before completion.
5. Capture only durable discoveries in portable KB, checking for duplicates first.
6. Review workspace.diff before checkpointing portable state.`, nil
}

func renderReviewerSkill(_ []localTool, _ map[string]toolGuidance) (string, error) {
	return `# Wormhole reviewer

1. Retrieve the work intent, portable decisions, constraints, and workspace.diff.
2. Inspect changed paths, callers, and affected types in current source.
3. Verify findings against Git, the working tree, and current source.
4. Record actionable findings with evidence and severity; avoid silent redesign.
5. Check that checkpoint did not stage, commit, or push Git.
6. Leave enough context for the contributor.`, nil
}

func compactJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func digestString(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func generatedToolContractDigest(tools []localTool) (string, error) {
	contract, err := generatedToolContract(tools)
	if err != nil {
		return "", err
	}
	canonical, err := canonicalGeneratedJSON(contract)
	if err != nil {
		return "", err
	}
	return digestString(canonical), nil
}

func generatedToolContract(tools []localTool) ([]generatedContractTool, error) {
	tools = append([]localTool(nil), tools...)
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	contract := make([]generatedContractTool, 0, len(tools))
	for _, tool := range tools {
		entry := generatedContractTool{
			Name: tool.Name, RequiredPermissions: append([]string{}, tool.RequiredPermissions...),
		}
		requestVariants := sortedMapKeys(buildInputSchemas(tool))
		for _, variant := range requestVariants {
			schema, err := generatedSchemaSnapshot(buildInputSchemas(tool)[variant])
			if err != nil {
				return nil, fmt.Errorf("request schema %s/%s: %w", tool.Name, variant, err)
			}
			entry.RequestSchemas = append(entry.RequestSchemas, generatedContractVariant{Variant: variant, Schema: schema})
		}
		for _, variant := range sortedMapKeys(tool.ResultExamples) {
			example := tool.ResultExamples[variant]
			if example == nil {
				return nil, fmt.Errorf("response schema %s/%s has nil example", tool.Name, variant)
			}
			raw := jsonResponseSchemaForType(reflect.TypeOf(example))
			if enrolment, ok := example.(EnrolmentResult); ok {
				raw["additionalProperties"] = false
				properties := raw["properties"].(map[string]any)
				properties["code"].(map[string]any)["enum"] = []any{string(enrolment.Code)}
				properties["state"].(map[string]any)["enum"] = []any{string(enrolment.State)}
				properties["retryable"].(map[string]any)["enum"] = []any{enrolment.Retryable}
			}
			schema, err := generatedSchemaSnapshot(raw)
			if err != nil {
				return nil, fmt.Errorf("response schema %s/%s: %w", tool.Name, variant, err)
			}
			entry.ResponseSchemas = append(entry.ResponseSchemas, generatedContractVariant{Variant: variant, Schema: schema})
		}
		contract = append(contract, entry)
	}
	return contract, nil
}

func generatedSchemaSnapshot(schema map[string]any) (generatedContractSchema, error) {
	out := generatedContractSchema{}
	if value, ok := schema["type"]; ok {
		var typeOK bool
		out.Type, typeOK = value.(string)
		if !typeOK {
			return out, fmt.Errorf("type is %T", value)
		}
	}
	out.Format, _ = schema["format"].(string)
	if values, ok := schema["enum"]; ok {
		switch enum := values.(type) {
		case []string:
			out.Enum = append(out.Enum, enum...)
		case []any:
			for _, value := range enum {
				switch typed := value.(type) {
				case string:
					out.Enum = append(out.Enum, typed)
				case bool:
					out.BooleanEnum = append(out.BooleanEnum, typed)
				default:
					return out, fmt.Errorf("enum contains %T", value)
				}
			}
		default:
			return out, fmt.Errorf("enum is %T", values)
		}
		sort.Strings(out.Enum)
	}
	if value, ok := schema["additionalProperties"].(bool); ok {
		out.AdditionalProperties = &value
	}
	out.MinLength, _ = schema["minLength"].(int)
	out.Minimum, _ = schema["minimum"].(int)
	out.Const, _ = schema["const"].(int)
	if value, ok := schema["required"]; ok {
		var requiredOK bool
		out.Required, requiredOK = value.([]string)
		if !requiredOK {
			return out, fmt.Errorf("required is %T", value)
		}
		out.Required = append([]string(nil), out.Required...)
		sort.Strings(out.Required)
	}
	if value, ok := schema["items"]; ok {
		item, itemOK := value.(map[string]any)
		if !itemOK {
			return out, fmt.Errorf("items is %T", value)
		}
		snapshot, err := generatedSchemaSnapshot(item)
		if err != nil {
			return out, err
		}
		out.Items = &snapshot
	}
	if value, ok := schema["anyOf"]; ok {
		alternatives, alternativesOK := value.([]map[string]any)
		if !alternativesOK {
			return out, fmt.Errorf("anyOf is %T", value)
		}
		for _, alternative := range alternatives {
			snapshot, err := generatedSchemaSnapshot(alternative)
			if err != nil {
				return out, err
			}
			out.AnyOf = append(out.AnyOf, snapshot)
		}
	}
	if value, ok := schema["properties"]; ok {
		properties, propertiesOK := value.(map[string]any)
		if !propertiesOK {
			return out, fmt.Errorf("properties is %T", value)
		}
		for _, name := range sortedMapKeys(properties) {
			property, propertyOK := properties[name].(map[string]any)
			if !propertyOK {
				return out, fmt.Errorf("property %s is %T", name, properties[name])
			}
			snapshot, err := generatedSchemaSnapshot(property)
			if err != nil {
				return out, err
			}
			out.Properties = append(out.Properties, generatedContractProperty{Name: name, Schema: snapshot})
		}
	}
	return out, nil
}

func sortedMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func generatedManifestDigest(manifest generatedGuidanceManifest) (string, error) {
	value, err := generatedJSONValue(manifest)
	if err != nil {
		return "", err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return "", fmt.Errorf("manifest canonical value is %T", value)
	}
	delete(object, "manifest_digest")
	canonical, err := canonicalGeneratedJSON(object)
	if err != nil {
		return "", err
	}
	return digestString(canonical), nil
}

func canonicalGeneratedJSON(value any) ([]byte, error) {
	decoded, err := generatedJSONValue(value)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(decoded); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

func generatedJSONValue(value any) (any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func marshalGeneratedGuidanceManifest(manifest generatedGuidanceManifest) ([]byte, error) {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
