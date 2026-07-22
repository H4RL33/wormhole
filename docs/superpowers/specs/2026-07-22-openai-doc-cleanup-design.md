# OpenAI Development Workflow Documentation Cleanup

## Goal

Replace Claude-specific project-development instructions with OpenAI/Codex-oriented,
agent-efficient guidance. Preserve Wormhole product support for Claude and other MCP
harnesses. Remove stale planning material that can mislead agents about current state.

## Retained Documentation

- Product and contributor entrypoints: `README.md`, `CONTRIBUTING.md`, `SECURITY.md`.
- Binding design: RFC-0001, RFC-0002, RFC-0003.
- Live engineering references: implementation rules, database entities, KB schema,
  MCP protocol, Claude Code product connector.
- Product references to Claude where they describe supported harness behavior.

## Removed Documentation

- `CLAUDE.md` and tracked build-workflow prompts under `agents/prompts/`.
- Root roadmaps and completed alpha status document.
- Dated implementation plans/specs, TODO ledger, deprecated architecture document.

Git history remains source for removed historical plans. Untracked or ignored `.agents/`
skills/plugins remain untouched. Existing user edits under `.superpowers/sdd/` remain
untouched.

## Canonical Agent Context

Create `agents/README.md` in terse caveman language. Include verified purpose,
architecture, authority order, package map, dependency rules, security constraints,
development workflow, commands, test expectations, and documentation map. Avoid roadmap,
dated status, provider-specific roleplay, and duplicated product prose.

Reduce root `AGENTS.md` to stable bootstrap instructions pointing agents to
`agents/README.md`, `.agents/` workflows, and implementation rules. Update surviving docs
only where removed paths or stale development-workflow language would leave broken or
misleading references.

## Safety And Verification

- Do not modify Git config, credential helpers, remotes, hooks, or commit history.
- Any created commit uses existing identity `Harley Welsh <git@h4rl3y.xyz>`.
- Stage only intended files; never stage existing `.superpowers/sdd/*` edits.
- Verify no broken references to removed files, no Claude-specific build prompt remains,
  Markdown links resolve, `go test ./...` passes, and Git identity/config sources match
  pre-change values.
