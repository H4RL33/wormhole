# Update AGENTS.md with Local Agents and Skills Path

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Update `AGENTS.md` to instruct agents to check the local `.agents` directory for custom skills and subagents.

**Architecture:** Append a new section "9. Local Skills and Subagents" to `AGENTS.md` containing clear instructions for any agent working in this repo to find and use resources within the `.agents` folder.

**Tech Stack:** Markdown

## Global Constraints

- Keep the document's structure, tone, and protocols intact.
- Follow all existing formatting, styles, and communication rules.
- Do not use em-dashes (commas, colons, semicolons, parentheses instead).

---

### Task 1: Update AGENTS.md with new section

**Files:**
- Modify: `AGENTS.md`

**Interfaces:**
- Consumes: Existing `AGENTS.md` content
- Produces: Updated `AGENTS.md` with Section 9 added

- [ ] **Step 1: Check existing AGENTS.md ending**
  Verify the exact content at the end of `AGENTS.md` to ensure precise targeting for modification.

- [ ] **Step 2: Modify AGENTS.md**
  Add a new section `9. Local Skills and Subagents` at the end of the file.

  Code diff to apply:
  ```diff
  --- a/AGENTS.md
  +++ b/AGENTS.md
  @@ -128,2 +128,11 @@
   - Main thread orchestrates and reviews; it does not do the edit itself, even for a one-liner.
  +
  +---
  +
  +## 9. Local Skills and Subagents
  +
  +Wormhole contains custom agent workflows, scripts, and instructions defined locally.
  +
  +- Before starting feature work, planning, or executing tasks, search and read the local `.agents` directory.
  +- All custom skills are stored under `.agents/skills/`. Read the corresponding `SKILL.md` before using a skill.
  +- Custom subagents and plugins are defined under `.agents/agents/` and `.agents/plugins/`. Look in these directories to understand available subagents and their capabilities.
  ```

- [ ] **Step 3: Run git diff and verify**
  Run: `git diff AGENTS.md`
  Expected: The diff matches the planned changes exactly, without introducing em-dashes or syntax errors.

- [ ] **Step 4: Commit the change**
  Run: `git add AGENTS.md` and `git commit -m "docs: instruct agents to look in local .agents directory for skills and subagents"`
  Expected: Commit successfully created.
