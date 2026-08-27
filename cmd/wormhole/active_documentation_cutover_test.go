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
		regexp.MustCompile(`(?i)\bwormhole\s+config\s+(?:code[-_ ]graph|graph)\b`),
		regexp.MustCompile(`(?i)\bwormhole\.code[_-]?graph\b`),
		regexp.MustCompile(`(?i)\bcode\.graph\b`),
		regexp.MustCompile(`(?i)\b25[- ]tools?\b`),
		regexp.MustCompile(`(?i)\b(?:enable|disable|configure|status|query|rebuild)\s+(?:the\s+)?code[-_ ]graph\b`),
		regexp.MustCompile(`(?i)\bcode[-_ ]graph\s+(?:enable|disable|status|query|rebuild)\b`),
		regexp.MustCompile(`(?i)(?:\bwormhole(?:\s+|[-_.])warpspeed\b|--warpspeed\b)`),
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
		for _, pattern := range removed {
			if match := pattern.FindIndex(content); match != nil {
				line := 1 + strings.Count(string(content[:match[0]]), "\n")
				relative, _ := filepath.Rel(repoRoot, path)
				t.Errorf("%s:%d retains removed public surface matching %q", filepath.ToSlash(relative), line, pattern)
			}
		}
	}
}

func TestStage2AlphaValidationDocumentsExactMCPInventories(t *testing.T) {
	t.Parallel()

	content, err := os.ReadFile("../../docs/testing/alpha-validation.md")
	if err != nil {
		t.Fatal(err)
	}
	wantGateway := []string{
		"wormhole.agent.enrol", "wormhole.agent.get_guidance", "wormhole.agent.list", "wormhole.agent.presence", "wormhole.agent.register", "wormhole.agent.whoami",
		"wormhole.channel.create", "wormhole.channel.events", "wormhole.channel.list", "wormhole.channel.post", "wormhole.channel.subscribe",
		"wormhole.git.link_commit",
		"wormhole.kb.get", "wormhole.kb.list", "wormhole.kb.search", "wormhole.kb.write",
		"wormhole.sync.status",
		"wormhole.task.create", "wormhole.task.get", "wormhole.task.list", "wormhole.task.route", "wormhole.task.update_status",
		"wormhole.workspace.checkpoint", "wormhole.workspace.diff", "wormhole.workspace.import", "wormhole.workspace.stash", "wormhole.workspace.status",
	}
	wantFabric := []string{
		"wormhole.agent.enrol", "wormhole.agent.whoami",
		"wormhole.channel.create", "wormhole.channel.list", "wormhole.channel.post", "wormhole.channel.subscribe",
		"wormhole.git.link_commit", "wormhole.git.request_review",
		"wormhole.kb.get", "wormhole.kb.get_links", "wormhole.kb.search", "wormhole.kb.write",
		"wormhole.sync.bootstrap", "wormhole.sync.conflict_report", "wormhole.sync.incremental_pull", "wormhole.sync.incremental_push",
		"wormhole.task.assign", "wormhole.task.create", "wormhole.task.list", "wormhole.task.update_status",
	}
	for _, inventory := range []struct {
		heading string
		want    []string
	}{
		{heading: "### Gateway MCP (27 tools)", want: wantGateway},
		{heading: "### Fabric MCP (20 tools)", want: wantFabric},
	} {
		got := markdownToolInventory(t, string(content), inventory.heading)
		sort.Strings(got)
		sort.Strings(inventory.want)
		if strings.Join(got, "\n") != strings.Join(inventory.want, "\n") {
			t.Errorf("%s inventory = %q, want exact %q", inventory.heading, got, inventory.want)
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
