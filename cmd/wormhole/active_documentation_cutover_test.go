package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestStage2ActiveDocumentationHasNoRemovedPublicSurfaces(t *testing.T) {
	t.Parallel()

	removed := []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bwormhole\s+(?:init|join|connect)\b`),
		regexp.MustCompile(`(?i)\bwormhole\s+(?:agent\s+)?enrol(?:l)?\b`),
		regexp.MustCompile(`(?i)\bwormhole\s+config\s+(?:code[-_ ]graph|graph)\b`),
		regexp.MustCompile(`(?i)\bwormhole\.code[_-]?graph\b`),
		regexp.MustCompile(`(?i)\bcode\.graph\b`),
		regexp.MustCompile(`(?i)\b25[- ]tools?\b`),
		regexp.MustCompile(`(?i)\b(?:enable|disable|configure|status|query|rebuild)\s+(?:the\s+)?code[-_ ]graph\b`),
		regexp.MustCompile(`(?i)\bcode[-_ ]graph\s+(?:enable|disable|status|query|rebuild)\b`),
		regexp.MustCompile(`(?i)(?:\bwormhole(?:\s+|[-_.])warpspeed\b|--warpspeed\b)`),
		regexp.MustCompile("(?i)`?--token-file`?"),
		regexp.MustCompile(`(?is)\benrol(?:l)?ment\b.{0,160}\b(?:the|a) CLI\s+(?:collects|generates|resolves|sends|submits|invokes|accepts|retries)\b`),
		regexp.MustCompile(`(?is)\b(?:the|a later) CLI\s+(?:process\s+)?(?:collects|generates|resolves|sends|submits|invokes|accepts|retries|may present)\b.{0,160}\b(?:enrol(?:l)?ment|candidate key|idempotency key|attempt key)\b`),
		regexp.MustCompile(`(?is)\b(?:enrol(?:l)?ment|candidate key|idempotency key|attempt key)\b.{0,160}\b(?:the|a) CLI\s+(?:collects|generates|resolves|sends|submits|invokes|accepts|retries)\b`),
	}
	for _, pattern := range removed {
		if pattern.MatchString("wormhole connector install --yes claude") {
			t.Fatalf("removed-surface expression %q has a connector prefix false positive", pattern)
		}
	}

	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	paths := []string{
		filepath.Join(repoRoot, "AGENTS.md"),
		filepath.Join(repoRoot, "README.md"),
		filepath.Join(repoRoot, "agents", "README.md"),
	}
	err := filepath.WalkDir(filepath.Join(repoRoot, "docs"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if isHistoricalOrInternalDocumentation(relative) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) == ".md" && !isHistoricalOrInternalDocumentation(relative) {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk active documentation: %v", err)
	}

	sort.Strings(paths)
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		relative, _ := filepath.Rel(repoRoot, path)
		if filepath.ToSlash(relative) == "docs/architecture/gateway-enrolment-lifecycle.md" && strings.Contains(string(content), "--profile") {
			t.Errorf("%s retains a nonexistent public enrolment profile flag", filepath.ToSlash(relative))
		}
		for _, pattern := range removed {
			if match := pattern.FindIndex(content); match != nil {
				line := 1 + strings.Count(string(content[:match[0]]), "\n")
				t.Errorf("%s:%d retains removed public surface matching %q", filepath.ToSlash(relative), line, pattern)
			}
		}
	}
}

func TestActiveValidationDocumentsLiveAndDescriptorOnlyMCPInventories(t *testing.T) {
	t.Parallel()
	content, err := os.ReadFile("../../docs/testing/alpha-validation.md")
	if err != nil {
		t.Fatal(err)
	}
	wantGateway := []string{
		"wormhole.agent.list", "wormhole.agent.presence", "wormhole.agent.register",
		"wormhole.channel.create", "wormhole.channel.events", "wormhole.channel.list", "wormhole.channel.post", "wormhole.channel.subscribe",
		"wormhole.kb.get", "wormhole.kb.list", "wormhole.kb.write",
		"wormhole.sync.status",
		"wormhole.workspace.checkpoint", "wormhole.workspace.diff", "wormhole.workspace.import", "wormhole.workspace.stash", "wormhole.workspace.status",
	}
	wantPrivateFabric := []string{
		"wormhole.agent.enrol", "wormhole.agent.whoami",
		"wormhole.channel.create", "wormhole.channel.list", "wormhole.channel.post", "wormhole.channel.subscribe",
		"wormhole.git.link_commit", "wormhole.git.request_review",
		"wormhole.kb.get", "wormhole.kb.get_links", "wormhole.kb.search", "wormhole.kb.write",
		"wormhole.task.assign", "wormhole.task.create", "wormhole.task.list", "wormhole.task.update_status",
	}
	wantPublicContract := []string{
		"wormhole.activity.accept", "wormhole.activity.lifecycle", "wormhole.activity.presence", "wormhole.activity.pull",
		"wormhole.sync.attach", "wormhole.sync.bootstrap", "wormhole.sync.conflict", "wormhole.sync.issue_agent_session",
		"wormhole.sync.pull", "wormhole.sync.push",
	}
	for _, inventory := range []struct {
		heading string
		want    []string
	}{
		{heading: "### Gateway MCP (17 live tools)", want: wantGateway},
		{heading: "### Fabric private MCP (16 live tools)", want: wantPrivateFabric},
		{heading: "### Fabric public contract (10 descriptor-only tools)", want: wantPublicContract},
	} {
		got := markdownToolInventory(t, string(content), inventory.heading)
		sort.Strings(got)
		sort.Strings(inventory.want)
		if strings.Join(got, "\n") != strings.Join(inventory.want, "\n") {
			t.Errorf("%s inventory = %q, want exact %q", inventory.heading, got, inventory.want)
		}
	}
}

func TestStage2ActiveDocumentationStatesLocalOnlyAcceptanceBoundary(t *testing.T) {
	t.Parallel()

	requirements := map[string][]string{
		"SECURITY.md": {
			"local-only Stage 2 Gateway", "exactly 17 agent-facing tools", "Git is the sole acceptance authority",
			"same-OS-user boundary", "selected human", "durable agent", "connection session",
			"optional Fabric", "16-tool", "descriptor-only", "does not return a raw token",
		},
		"README.md": {
			"17 agent-facing tools", "Git is the sole acceptance authority", "optional Fabric binary",
			"hostile same-user processes", "operational activity", "machine-private",
		},
		"agents/README.md": {
			"exactly 17", "optional Fabric", "Git is the sole accepted", "same-user", "exactly 16", "descriptor-only",
		},
		"docs/wiki/Security-Model.md": {
			"Git acceptance authority", "Portable project state", "Operational state", "Machine-private state",
			"hostile same-user process", "does not contact Fabric",
			"exactly 16 private tools", "ten public", "descriptor-only", "non-callable",
		},
		"docs/testing/alpha-validation.md": {
			"TestStage2LocalOnlyRealProcessAcceptance", "requires neither PostgreSQL nor Fabric",
			"real `wormhole mcp` stdio bridge", "Gateway MCP (17 live tools)",
			"Fabric private MCP (16 live tools)", "Fabric public contract (10 descriptor-only tools)",
			"service-manager installation is covered separately",
		},
		"docs/architecture/gateway-enrolment-lifecycle.md": {
			"Historical/future optional-Fabric design", "not a live Stage 2 Gateway operation",
			"exactly 16", "ten public", "descriptor-only", "credentials_persisted",
			"bootstrap_in_progress", "recovery_required", "controlled reissue",
		},
		"docs/mcp-protocol.md": {
			"17-tool", "private working-directory context", "removed before public schema validation",
			"optional Fabric", "wormhole.workspace.status", "16-tool private", "ten non-callable public descriptor",
		},
		"docs/contracts/README.md": {
			"exactly 17", "exactly 16", "exactly ten", "descriptor-only",
			"not in the Stage 2 Gateway inventory",
		},
		"docs/compatibility.md": {
			"17-tool", "16-tool private live registry", "ten-tool", "descriptor-only",
			"Git acceptance", "machine-private",
		},
		"docs/wiki/Home.md": {
			"local-only Stage 2", "Git is the sole acceptance authority", "Fabric is optional",
			"exactly 16", "ten public", "descriptor-only", "non-callable",
		},
		"docs/wiki/CLI-Guide.md": {
			"exact 17-tool Gateway", "not a Stage 2 runtime dependency", "ordinary Git",
			"16 live private tools", "ten descriptor-only public contracts", "non-callable",
		},
		"docs/operators/alpha-validation-trial.md": {
			"local-only Stage 2", "17-tool Gateway", "does not exercise Fabric", "second fresh clone",
			"16-tool live private registry", "ten public", "descriptor-only", "non-callable",
		},
		"docs/implementation-rules.md": {
			"exact 17-tool Gateway", "Stage 2 local-only runtime does not instantiate", "exactly 16", "descriptor-only",
		},
		"docs/testing/closed-trial-metrics.md": {
			"current local-only Stage 2 trial", "compatibility-only schema labels",
			"no live managed-guidance feature", "no live Task MCP feature",
		},
	}

	for relative, fragments := range requirements {
		content, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(relative)))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		for _, fragment := range fragments {
			if !strings.Contains(string(content), fragment) {
				t.Errorf("%s does not state required Stage 2 boundary %q", relative, fragment)
			}
		}
	}

	alpha, err := os.ReadFile("../../docs/testing/alpha-validation.md")
	if err != nil {
		t.Fatal(err)
	}
	stage2Section := strings.SplitN(string(alpha), "## Optional Fabric/PostgreSQL coverage", 2)[0]
	for _, forbidden := range []string{
		"TestAlphaValidation_FullAutomatedAcceptanceLoop", "WORMHOLE_INTEGRATION_REQUIRED=1",
		"wormhole.agent.enrol", "wormhole.agent.get_guidance", "wormhole.agent.whoami",
		"wormhole.kb.search", "wormhole.task.list", "wormhole.git.link_commit",
	} {
		if strings.Contains(stage2Section, forbidden) {
			t.Errorf("Stage 2 alpha-validation section retains non-local claim %q", forbidden)
		}
	}

	security, err := os.ReadFile("../../SECURITY.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, staleCurrentClaim := range []string{
		"### 1. Database Row-Level Security (RLS)",
		"Agents authenticate using bearer tokens at the MCP boundary.",
		"Access to any project requires a `Passport`",
		"Registration returns the caller's newly issued raw token",
	} {
		if strings.Contains(string(security), staleCurrentClaim) {
			t.Errorf("SECURITY.md retains stale current-boundary claim %q", staleCurrentClaim)
		}
	}
}

func TestActiveOwnershipCommentsDescribeRetainedTransportNeutralBoundaries(t *testing.T) {
	t.Parallel()

	requirements := map[string][]string{
		"docs/implementation-rules.md": {
			"Activity-v1 transport, durable queue/audit repositories, the v2 status shell",
			"shared route and credential interfaces",
		},
		"internal/runtime/localapi/localapi.go": {
			"per-action permission contract at the local",
			"MCP boundary before a handler can read or mutate replica state",
			"fails closed before dispatch",
		},
		"internal/runtime/localstore/task_repo.go": {
			"applies a complete caller-supplied task projection",
			"does not claim a live remote transport",
		},
		"internal/mcp/jsonrpc_toolscall_test.go": {
			"private dispatch derives project identity",
			"authenticated bearer scope",
			"Descriptor-only public tools do not use this private path.",
		},
	}
	forbidden := map[string][]string{
		"docs/implementation-rules.md": {
			"bootstrap/incremental streams",
		},
		"internal/runtime/localapi/localapi.go": {
			"online for sync bootstrap",
			"incremental_push independently rechecks every queued item server-side",
		},
		"internal/runtime/localstore/task_repo.go": {
			"Incremental pull calls this",
		},
		"internal/mcp/jsonrpc_toolscall_test.go": {
			"sync engine's",
			"internal/runtime/sync.Engine",
			"namespace_id) — auth resolves",
		},
	}

	for relative, fragments := range requirements {
		content, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(relative)))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		for _, fragment := range fragments {
			if !strings.Contains(string(content), fragment) {
				t.Errorf("%s does not state retained ownership %q", relative, fragment)
			}
		}
		for _, fragment := range forbidden[relative] {
			if strings.Contains(string(content), fragment) {
				t.Errorf("%s retains deleted sync-v1 ownership claim %q", relative, fragment)
			}
		}
	}
}

func markdownToolInventory(t *testing.T, content, heading string) []string {
	t.Helper()
	start := strings.Index(content, heading)
	if start < 0 {
		t.Errorf("missing inventory heading %q", heading)
		return nil
	}
	content = content[start+len(heading):]
	fence := strings.Index(content, "```text\n")
	if fence < 0 {
		t.Errorf("%s has no text inventory", heading)
		return nil
	}
	content = content[fence+len("```text\n"):]
	end := strings.Index(content, "\n```")
	if end < 0 {
		t.Errorf("%s has an unterminated text inventory", heading)
		return nil
	}
	var tools []string
	for _, line := range strings.Split(content[:end], "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			tools = append(tools, line)
		}
	}
	return tools
}

func isHistoricalOrInternalDocumentation(path string) bool {
	path = filepath.ToSlash(path)
	if path == "docs/superpowers" || strings.HasPrefix(path, "docs/superpowers/") ||
		path == "docs/testing/results" || strings.HasPrefix(path, "docs/testing/results/") {
		return true
	}
	switch path {
	case "docs/architecture/code-graph-alpha-contract.md",
		"docs/github-open-issue-reconciliation.md",
		"docs/testing/code-graph-benchmarks.md",
		"docs/testing/manual-alpha-validation-2026-07.md":
		return true
	default:
		return false
	}
}
