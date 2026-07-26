package localapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const generatedGuidanceFixtureDir = "../../../testdata/alpha/manifests/generated-guidance"

var generatedSkillContracts = []struct {
	slug        string
	roles       []string
	golden      string
	manifestDst string
}{
	{"wormhole-orientation", []string{}, "wormhole-orientation.md", ".agents/skills/wormhole-orientation/SKILL.md"},
	{"wormhole-tool-use", []string{}, "wormhole-tool-use.md", ".agents/skills/wormhole-tool-use/SKILL.md"},
	{"wormhole-code-graph", []string{}, "wormhole-code-graph.md", ".agents/skills/wormhole-code-graph/SKILL.md"},
	{"wormhole-operating-loop", []string{}, "wormhole-operating-loop.md", ".agents/skills/wormhole-operating-loop/SKILL.md"},
	{"wormhole-contributor", []string{"contributor"}, "wormhole-contributor.md", ".agents/skills/wormhole-contributor/SKILL.md"},
	{"wormhole-reviewer", []string{"reviewer"}, "wormhole-reviewer.md", ".agents/skills/wormhole-reviewer/SKILL.md"},
}

func TestRenderGatewayGuidanceGoldenContract(t *testing.T) {
	registry := newLocalRegistry(&Server{})
	rendered, err := renderGatewayGuidance(registry, registry.Guidance())
	if err != nil {
		t.Fatalf("render guidance: %v", err)
	}
	if len(rendered.Files) != len(generatedSkillContracts) {
		t.Fatalf("rendered file count = %d, want %d", len(rendered.Files), len(generatedSkillContracts))
	}

	bySlug := make(map[string]generatedGuidanceFile, len(rendered.Files))
	for _, file := range rendered.Files {
		if _, duplicate := bySlug[file.Slug]; duplicate {
			t.Fatalf("duplicate generated skill %q", file.Slug)
		}
		bySlug[file.Slug] = file
		assertGeneratedSkillShape(t, file)
	}
	for _, contract := range generatedSkillContracts {
		file, ok := bySlug[contract.slug]
		if !ok {
			t.Fatalf("missing generated skill %q", contract.slug)
		}
		if file.Target != contract.manifestDst || !reflect.DeepEqual(file.RoleFilters, contract.roles) {
			t.Fatalf("%s target/roles = %q/%v, want %q/%v", contract.slug, file.Target, file.RoleFilters, contract.manifestDst, contract.roles)
		}
		assertGoldenBytes(t, contract.golden, []byte(file.Content))
	}

	assertToolUseSkill(t, registry, bySlug["wormhole-tool-use"].Content)
	assertOrientationSkill(t, bySlug["wormhole-orientation"].Content)
	assertOperatingLoopSkill(t, bySlug["wormhole-operating-loop"].Content)
	assertTask18Guidance(t, bySlug)
	assertCodeGraphSkill(t, bySlug["wormhole-code-graph"].Content)
	assertRoleSkills(t, bySlug)
	assertNoUnsupportedTools(t, registry, rendered.Files)
	assertGeneratedManifest(t, rendered)
}

func assertOrientationSkill(t *testing.T, content string) {
	t.Helper()
	content = strings.Join(strings.Fields(content), " ")
	for _, required := range []string{
		"organisational context", "not source code", "Git", "authoritative for source",
		"Gateway", "local MCP endpoint", "Fabric", "shared state", "Tasks", "intended work",
		"KB articles", "facts, decisions, discoveries, and procedures", "typed Events", "chatter",
		"Identity and permissions", "before reconstructing project context",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("orientation skill lacks %q", required)
		}
	}
}

func TestRenderGatewayGuidanceTracksSchemaAndRejectsGuidanceDrift(t *testing.T) {
	registry := newLocalRegistry(&Server{})
	baseline, err := renderGatewayGuidance(registry, registry.Guidance())
	if err != nil {
		t.Fatal(err)
	}
	mutated := newLocalRegistry(&Server{})
	tool, ok := mutated.Get("wormhole.task.get")
	if !ok {
		t.Fatal("task.get missing")
	}
	tool.ArgumentExamples = singleArgument(struct {
		TaskID     string `json:"task_id"`
		RevisionID string `json:"revision_id"`
	}{})
	mutated.tools[tool.Name] = tool
	changed, err := renderGatewayGuidance(mutated, mutated.Guidance())
	if err != nil {
		t.Fatal(err)
	}
	if generatedFileContent(baseline.Files, "wormhole-tool-use") == generatedFileContent(changed.Files, "wormhole-tool-use") {
		t.Fatal("tool-use output did not change after live request schema changed")
	}

	guidance := registry.Guidance()
	tests := []struct {
		name    string
		records []toolGuidance
		needle  string
	}{
		{"missing", guidance[1:], guidance[0].ToolName},
		{"duplicate", append(append([]toolGuidance{}, guidance...), guidance[0]), guidance[0].ToolName},
		{"phantom", append(append([]toolGuidance{}, guidance...), toolGuidance{ToolName: "wormhole.agent.mutate_guidance"}), "wormhole.agent.mutate_guidance"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := renderGatewayGuidance(registry, tc.records); err == nil || !strings.Contains(err.Error(), tc.needle) {
				t.Fatalf("render error = %v, want diagnostic containing %q", err, tc.needle)
			}
		})
	}
}

func assertGeneratedSkillShape(t *testing.T, file generatedGuidanceFile) {
	t.Helper()
	if !strings.HasPrefix(file.Content, "---\nname: "+file.Slug+"\ndescription: Use when ") {
		t.Fatalf("%s has invalid frontmatter", file.Slug)
	}
	if strings.ContainsAny(file.Content, "\r\x00") || !strings.HasSuffix(file.Content, "\n") || strings.HasSuffix(file.Content, "\n\n") {
		t.Fatalf("%s must be LF-only with exactly one trailing LF", file.Slug)
	}
}

func assertToolUseSkill(t *testing.T, registry *localRegistry, content string) {
	t.Helper()
	if len(registry.List()) != 22 {
		t.Fatalf("live tool count = %d, want 22", len(registry.List()))
	}
	guidance := map[string]toolGuidance{}
	for _, record := range registry.Guidance() {
		guidance[record.ToolName] = record
	}
	for _, tool := range registry.List() {
		heading := "## `" + tool.Name + "`"
		start := strings.Index(content, heading)
		if start < 0 {
			t.Fatalf("tool-use guidance lacks %s", heading)
		}
		section := content[start+len(heading):]
		if next := strings.Index(section, "\n## `"); next >= 0 {
			section = section[:next]
		}
		record := guidance[tool.Name]
		schema, err := compactJSON(buildInputSchema(tool))
		if err != nil {
			t.Fatal(err)
		}
		example, err := compactJSON(record.MinimalExample)
		if err != nil {
			t.Fatal(err)
		}
		permissions := "none"
		if len(tool.RequiredPermissions) > 0 {
			permissions = strings.Join(tool.RequiredPermissions, ", ")
		}
		for _, required := range []string{
			"Purpose: " + record.Purpose, "Use when: " + record.UseWhen,
			"Do not use when: " + record.DoNotUseWhen,
			"Mutates state: " + strconv.FormatBool(record.MutatesState),
			"Required permissions: " + permissions, "Prerequisites: " + record.Prerequisites,
			"Freshness implications: " + record.FreshnessImplications,
			"Source-access implications: " + record.SourceAccessImplications,
			"Recommended follow-up: " + record.RecommendedFollowUp,
			"Minimal request example: `" + example + "`",
			"Live request schema: `" + schema + "`", "Misuse warning: " + record.MisuseWarning,
		} {
			if !strings.Contains(section, required) {
				t.Fatalf("tool-use guidance for %s lacks %q", tool.Name, required)
			}
		}
	}
}

func assertOperatingLoopSkill(t *testing.T, content string) {
	t.Helper()
	for _, line := range []string{
		"session start:", "inspect identity and permissions", "inspect assigned and relevant Tasks", "retrieve relevant KB context", "inspect recent relevant Events", "inspect Code Graph status for code tasks", "confirm intended work before broad exploration",
		"before changing code:", "retrieve the Task and links", "check decisions and constraints", "use Code Graph when ready and useful", "report work begun when supported", "preserve Git as authority",
		"during work:", "record meaningful blockers", "publish only durable discoveries", "do not narrate every command", "prefer typed Events", "check for duplicate Tasks and KB articles before creating them",
		"completion:", "run required verification", "update Task state", "link the commit or pull request where supported", "record durable knowledge", "publish one concise completion Event", "leave sufficient context for another Agent",
		"if Code Graph is ready:", "query it before broad code discovery", "else:", "continue with normal filesystem and repository tools",
	} {
		if !strings.Contains(content, line) {
			t.Errorf("operating-loop skill lacks %q", line)
		}
	}
}

func assertTask18Guidance(t *testing.T, files map[string]generatedGuidanceFile) {
	t.Helper()
	data, err := os.ReadFile("../../../testdata/alpha/kb/semantic-low-overlap.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Requirements map[string]string `json:"generated_guidance_requirements"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"wormhole-tool-use", "wormhole-operating-loop"} {
		for name, statement := range fixture.Requirements {
			if !strings.Contains(files[slug].Content, statement) {
				t.Errorf("%s lacks Task18 %s statement", slug, name)
			}
		}
	}
}

func assertCodeGraphSkill(t *testing.T, content string) {
	t.Helper()
	lower := strings.ToLower(content)
	for _, required := range []string{"status", "bounded source budget", "code_graph.source.read", "heuristic", "Git HEAD", "working tree", "known file", "non-code", "approved checkout", "does not replace Git", "direct verification", "builds", "tests"} {
		if !strings.Contains(lower, strings.ToLower(required)) {
			t.Errorf("Code Graph skill lacks %q", required)
		}
	}
}

func assertRoleSkills(t *testing.T, files map[string]generatedGuidanceFile) {
	t.Helper()
	for _, required := range []string{"task pickup", "scoped implementation", "blocker", "verification", "durable discovery"} {
		if !strings.Contains(files["wormhole-contributor"].Content, required) {
			t.Errorf("contributor skill lacks %q", required)
		}
	}
	for _, required := range []string{"Task intent", "Code Graph", "Git", "current source", "actionable findings", "silent redesign", "Git pointer", "hypotheses", "not proof"} {
		if !strings.Contains(files["wormhole-reviewer"].Content, required) {
			t.Errorf("reviewer skill lacks %q", required)
		}
	}
}

func assertNoUnsupportedTools(t *testing.T, registry *localRegistry, files []generatedGuidanceFile) {
	t.Helper()
	live := map[string]bool{}
	for _, tool := range registry.List() {
		live[tool.Name] = true
	}
	toolPattern := regexp.MustCompile(`wormhole\.[a-z_]+\.[a-z_]+`)
	for _, file := range files {
		for _, name := range toolPattern.FindAllString(file.Content, -1) {
			if !live[name] {
				t.Errorf("%s names unsupported tool %q", file.Slug, name)
			}
		}
	}
}

func assertGeneratedManifest(t *testing.T, rendered generatedGuidance) {
	t.Helper()
	data, err := marshalGeneratedGuidanceManifest(rendered.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	assertGoldenBytes(t, "manifest.json", data)
	if len(rendered.Manifest.Entries) != 6 {
		t.Fatalf("manifest entries = %d, want 6", len(rendered.Manifest.Entries))
	}
	targets := make([]string, 0, len(rendered.Manifest.Entries))
	contentByTarget := map[string]string{}
	rolesByTarget := map[string][]string{}
	for _, file := range rendered.Files {
		contentByTarget[file.Target] = file.Content
		rolesByTarget[file.Target] = file.RoleFilters
	}
	for _, entry := range rendered.Manifest.Entries {
		targets = append(targets, entry.Target)
		sum := sha256.Sum256([]byte(entry.Content))
		if entry.Content != contentByTarget[entry.Target] || !reflect.DeepEqual(entry.RoleFilters, rolesByTarget[entry.Target]) || entry.ContentDigest != "sha256:"+hex.EncodeToString(sum[:]) || entry.Kind != "skill" || entry.MergePolicy != "managed_file" || !entry.Required {
			t.Errorf("invalid manifest entry for %s", entry.Target)
		}
	}
	if !sort.StringsAreSorted(targets) {
		t.Fatalf("manifest targets are not sorted: %v", targets)
	}
	if rendered.Manifest.SchemaVersion != 1 || rendered.Manifest.ManifestVersion != 1 || rendered.Manifest.Source != "fabric" || len(rendered.Manifest.RoleFilters) != 0 {
		t.Fatalf("invalid manifest root contract: %+v", rendered.Manifest)
	}
	digestPattern := regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	if !digestPattern.MatchString(rendered.Manifest.ToolContractDigest) || !digestPattern.MatchString(rendered.Manifest.ManifestDigest) {
		t.Fatal("manifest digests are not canonical lowercase SHA-256")
	}
	verifyGeneratedManifestDigests(t, data)
}

func verifyGeneratedManifestDigests(t *testing.T, data []byte) {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	assertStringKeySet(t, object, []string{"created_at", "entries", "manifest_digest", "manifest_id", "manifest_version", "project_id", "role_filters", "schema_version", "source", "tool_contract_digest"})
	entries, ok := object["entries"].([]any)
	if !ok {
		t.Fatalf("manifest entries = %T", object["entries"])
	}
	for _, rawEntry := range entries {
		entry, ok := rawEntry.(map[string]any)
		if !ok {
			t.Fatalf("manifest entry = %T", rawEntry)
		}
		assertStringKeySet(t, entry, []string{"content", "content_digest", "kind", "merge_policy", "required", "role_filters", "target"})
	}
	wantManifest, _ := object["manifest_digest"].(string)
	delete(object, "manifest_digest")
	canonical, err := testCanonicalJSON(object)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	if wantManifest != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatalf("manifest digest does not bind canonical manifest")
	}

	contractData, err := os.ReadFile("../../../docs/contracts/alpha-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		MCPTools struct {
			Gateway any `json:"gateway"`
		} `json:"mcp_tools"`
	}
	if err := json.Unmarshal(contractData, &contract); err != nil {
		t.Fatal(err)
	}
	canonical, err = testCanonicalJSON(contract.MCPTools.Gateway)
	if err != nil {
		t.Fatal(err)
	}
	sum = sha256.Sum256(canonical)
	wantTool, _ := object["tool_contract_digest"].(string)
	contractDigest := "sha256:" + hex.EncodeToString(sum[:])
	if wantTool != contractDigest {
		generatedContract, err := generatedToolContract(newLocalRegistry(&Server{}).List())
		if err != nil {
			t.Fatal(err)
		}
		generatedValue, err := generatedJSONValue(generatedContract)
		if err != nil {
			t.Fatal(err)
		}
		gotTools := generatedValue.([]any)
		wantTools := contract.MCPTools.Gateway.([]any)
		for i := range gotTools {
			if !reflect.DeepEqual(gotTools[i], wantTools[i]) {
				t.Fatalf("tool contract mismatch at index %d\ngot:  %#v\nwant: %#v", i, gotTools[i], wantTools[i])
			}
		}
		t.Fatalf("tool contract digest = %s, live alpha inventory = %s", wantTool, contractDigest)
	}
}

func assertStringKeySet(t *testing.T, object map[string]any, want []string) {
	t.Helper()
	got := make([]string, 0, len(object))
	for key := range object {
		got = append(got, key)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("object keys = %v, want %v", got, want)
	}
}

func testCanonicalJSON(value any) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

func assertGoldenBytes(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join(generatedGuidanceFixtureDir, name)
	if os.Getenv("UPDATE_GENERATED_GUIDANCE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create golden directory: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", name, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("generated %s differs from golden", name)
	}
}

func generatedFileContent(files []generatedGuidanceFile, slug string) string {
	for _, file := range files {
		if file.Slug == slug {
			return file.Content
		}
	}
	return ""
}
