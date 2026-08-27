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
	assertRoleSkills(t, bySlug)
	assertNoUnsupportedTools(t, registry, rendered.Files)
	assertGeneratedManifest(t, rendered)
}

func assertOrientationSkill(t *testing.T, content string) {
	t.Helper()
	content = strings.Join(strings.Fields(content), " ")
	for _, required := range []string{
		"organisational context", "not source code", "Git", "authoritative for source",
		"Gateway", "local MCP endpoint", "local-only", "optional Fabric",
		"Portable channels and KB articles", "tracked project state", "clone-private operational state",
		"Workspace tools", "before broad repository exploration",
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
	tool, ok := mutated.Get("wormhole.kb.get")
	if !ok {
		t.Fatal("kb.get missing")
	}
	tool.ArgumentExamples = singleArgument(struct {
		ArticleID  string `json:"article_id"`
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
	if len(registry.List()) != 17 {
		t.Fatalf("live tool count = %d, want 17", len(registry.List()))
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
		"session start:", "inspect workspace.status", "retrieve relevant KB context with kb.list or kb.get", "inspect recent clone-local channel.events", "confirm intended work before broad exploration",
		"before changing code:", "check portable decisions and constraints", "preserve Git as source and acceptance authority", "inspect workspace.diff before checkpointing",
		"during work:", "record durable discoveries in KB", "use channel.post only for clone-local operational activity", "do not narrate every command", "check for duplicate channels and KB articles",
		"completion:", "run required verification", "publication review digest", "checkpoint without staging, committing, or pushing Git", "accept portable state through ordinary Git", "leave sufficient context for another agent",
	} {
		if !strings.Contains(content, line) {
			t.Errorf("operating-loop skill lacks %q", line)
		}
	}
}

func assertRoleSkills(t *testing.T, files map[string]generatedGuidanceFile) {
	t.Helper()
	for _, required := range []string{"work intent", "scoped implementation", "clone-local operational context", "verification", "durable discoveries", "workspace.diff"} {
		if !strings.Contains(files["wormhole-contributor"].Content, required) {
			t.Errorf("contributor skill lacks %q", required)
		}
	}
	for _, required := range []string{"work intent", "workspace.diff", "changed paths", "Git", "current source", "actionable findings", "silent redesign", "checkpoint"} {
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
	if len(rendered.Manifest.Entries) != 5 {
		t.Fatalf("manifest entries = %d, want 5", len(rendered.Manifest.Entries))
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
