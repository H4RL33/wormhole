# Day 24 — Restore Philosophy & Goals Section to README

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore the "Who is Wormhole for?", "Goals for Wormhole", "The Social Good", and "Being Open-Source" sections into a new `Philosophy & Goals` section in `README.md`.

## Global Constraints

- R1 (`docs/architecture.md:174`): `internal/core/*` packages never import `internal/mcp`.
- No new external Go dependencies.
- T4 (`docs/architecture.md` §7): must pass `go build ./...`, `go vet ./...`, `go test ./...` before commit.

---

### Task 1: Restore Philosophy & Goals in README.md

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Edit `README.md`**
  - Add a new section `## Philosophy & Goals` after the project introduction and before `## Status`.
  - Include:
    - `### Who is Wormhole for?`
    - `### Goals for Wormhole`
    - `### The Social Good`
    - `#### Being Open-Source` (with the open-source stance and the MCP note)
  - Verify that markdown formatting and all links read cleanly.

- [ ] **Step 2: Verify the build**
  Run: `go build ./...` and `go test ./...` to verify everything compiles and passes.

- [ ] **Step 3: Commit**
  Commit the changes:
  ```bash
  git add README.md
  git commit -m "docs: restore philosophy and goals sections to README"
  ```
