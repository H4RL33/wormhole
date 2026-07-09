# Day 24 — README and Documentation Updates for Alpha Launch

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Update `README.md` to reflect the first alpha state (`v0.1.0-alpha`), provide step-by-step developer setup/demo guidelines, create security guidelines in `SECURITY.md`, and expand contribution rules in `CONTRIBUTING.md`.

## Global Constraints

- R1 (`docs/architecture.md:174`): `internal/core/*` packages never import `internal/mcp`.
- No new external Go dependencies.
- T4 (`docs/architecture.md` §7): must pass `go build ./...`, `go vet ./...`, `go test ./...` before commit.

---

### Task 1: Update README.md and Create SECURITY.md and CONTRIBUTING.md

**Files:**
- Modify: `README.md`
- Create: `SECURITY.md`
- Modify: `CONTRIBUTING.md`

- [ ] **Step 1: Rewrite `README.md`**
  - Update status to `Alpha Release (v0.1.0-alpha)`.
  - Tighten flow, remove outdated text, structure features around the 4 pillars (Communication, Coordination, Knowledge Base, Identity & Permissions).
  - Add a **Quickstart / Local Demo** section explaining:
    1. Running PostgreSQL via `docker compose up -d`
    2. Installing `golang-migrate` and running migrations
    3. Creating a demo project in the database via `docker compose exec db psql ...`
    4. Running `wormhole-server`
    5. Running the `wormhole-cli join` command

- [ ] **Step 2: Create `SECURITY.md`**
  - Detail security model and vulnerability disclosure.
  - Explain Postgres RLS (Row Level Security) and token authentication checks.
  - Outline cryptographic unforgeability of agent identities.

- [ ] **Step 3: Update `CONTRIBUTING.md`**
  - Outline developer contribution guidelines.
  - Mention architectural constraints (R1: no core-to-mcp imports).
  - Detail test requirements (e.g. database-backed unit tests, isolation checks).

- [ ] **Step 4: Verify the build**
  Run: `go build ./...` and `go test ./...` to verify everything compiles and passes.

- [ ] **Step 5: Commit**
  Commit the changes:
  ```bash
  git add README.md SECURITY.md CONTRIBUTING.md
  git commit -m "docs: update README, SECURITY, and CONTRIBUTING for v0.1.0-alpha"
  ```
