# README Quickstart and Usage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish a concise, verified Linux/WSL quickstart and usage reference for the current Wormhole server, daemon, CLI, and harness workflow.

**Architecture:** Preserve the README's conceptual material and replace only its operational onboarding section. Commands use the repository-built `dist` binaries so readers follow one consistent installation model, while links route connector-specific detail to the existing connector guide.

**Tech Stack:** Markdown, Go CLI binaries, Docker Compose, PostgreSQL/pgvector, golang-migrate, Git, GitHub CLI.

## Global Constraints

- `wormholed` runtime support is Linux-only; Windows users use WSL.
- Do not document a direct harness-to-server path; harnesses use `wormhole mcp` and the daemon Unix socket.
- Use the exact current CLI flags and the exact credential profile when starting `wormholed`.
- Preserve unrelated scratch changes and commit the README scope explicitly.
- Merge the published pull request into the remote default branch only after verification succeeds.

---

### Task 1: Rewrite Quickstart and Usage

**Files:**
- Modify: `README.md`
- Modify: `docs/claude-code-connector.md`
- Create: `docs/superpowers/specs/2026-07-23-readme-quickstart-design.md`
- Create: `docs/superpowers/plans/2026-07-23-readme-quickstart.md`

**Interfaces:**
- Consumes: current `wormhole`, `wormholed`, and `wormhole-server` command behavior.
- Produces: one copy-pastable onboarding sequence plus usage and troubleshooting references.

- [ ] **Step 1: Replace the stale operational section**

Replace `## Quickstart / Local Demo` through the Design Documents divider with
the approved flow: prerequisites, build, database, migration, project, server,
connect/join, daemon, verification, usage, locations, precedence, and
troubleshooting.

- [ ] **Step 2: Validate documented commands**

Run:

```bash
go run ./cmd/wormhole help
go run ./cmd/wormhole connect --help
go run ./cmd/wormhole join --help
go run ./cmd/wormhole whoami --help
go run ./cmd/wormhole viewer-key create --help
```

Expected: the README uses only displayed commands and flags. Flag help exits
with status 2 by Go `flag` convention; its usage text must still match.

- [ ] **Step 3: Verify the documentation diff**

Run:

```bash
git diff --check
make fmt-check
make build
make vet
```

Expected: every command exits 0.

- [ ] **Step 4: Commit the documentation scope**

Stage `README.md` and the two README design/plan files explicitly, then commit:

```bash
git commit -m "docs: refresh quickstart and usage"
```

- [ ] **Step 5: Publish and merge**

Push `qa-simplification` to `origin`, open a pull request into the remote
default branch, merge it after checks allow, and verify the remote default
branch contains the documentation commit.
