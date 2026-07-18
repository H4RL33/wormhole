# Docs & Workflow Optimization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate doc duplication, restructure for clarity, implement dispatch heuristic to reduce subagent spawn overhead by ~40%.

**Architecture:** Four-file refactor. Create new `docs/implementation-rules.md` as authoritative guardrails doc (extracted from architecture.md + new dispatch rule). Compact CLAUDE.md to portable system prompt only. Stub AGENTS.md as registry. Redirect architecture.md to new home.

**Tech Stack:** Markdown, Git, no code.

## Global Constraints

- Authority order: RFC-0001 > RFC-0002 > docs/implementation-rules.md > existing code (preserve in all rewrites)
- Dispatch heuristic: <100 lines, single file, no RFC ambiguity, no cross-pillar → direct edit; else subagent
- No content loss: every guardrail from architecture.md §0–§10 present in implementation-rules.md §2–§11
- Commits: one per file modified (5 commits total)
- Token target: 40% reduction in per-session load

---

## File Map

| File | Current State | Action | Purpose |
|---|---|---|---|
| `docs/implementation-rules.md` | (new) | Create | Authoritative procedures + dispatch rule |
| `CLAUDE.md` | 129 lines | Rewrite | Compact system prompt, trim to essentials |
| `AGENTS.md` | 139 lines (duped) | Rewrite | Registry stub only, remove §1–§8 |
| `docs/architecture.md` | 460 lines | Redirect | Deprecated; point to implementation-rules.md |

---

## Task 1: Create docs/implementation-rules.md

**Files:**
- Create: `docs/implementation-rules.md`

**Interfaces:**
- Consumes: Content from current `docs/architecture.md` (§0–§10)
- Produces: New authoritative guardrails doc with prepended §1 (Dispatch heuristic)

- [ ] **Step 1: Read docs/architecture.md in full**

Run: `head -460 /mnt/data/vault/projects/wormhole/docs/architecture.md`

Verify: File is 460 lines, content intact (header through "Completion Report Template").

- [ ] **Step 2: Create new implementation-rules.md with §1 prepended**

Write to `docs/implementation-rules.md`:

```markdown
# Wormhole Implementation Rules & Dispatch Heuristic

**Audience:** implementation agents (any model tier) making changes to this repo.
**Authority order:** RFC-0001 > RFC-0002 > this document > existing code. This document
derives from the RFCs and the code as of Day 3; if it conflicts with an RFC, the RFC wins
and this file has a bug — flag it, don't silently pick one.

This is a *constraint document*, not a tutorial. Every section states rules. If a task
requires breaking a rule here, stop and escalate to the orchestrating agent or human;
do not improvise.

---

## 1. Dispatch Heuristic — Direct Edit vs Subagent-Driven

**Use direct edit (bypass subagent-driven-development) if ALL conditions hold:**

1. **Single file touched** — change is contained to one file only
2. **≤100 lines of code** — total additions/modifications ≤100 lines
3. **No RFC ambiguity** — task cites an RFC section, decision is unambiguous
4. **No cross-pillar implications** — touches only one pillar (events, tasks, kb, identity, permissions) OR only config/docs/tests

**Otherwise → subagent-driven-development.**

**Dispatch examples:**

| Change | Route | Reasoning |
|---|---|---|
| Fix typo in docs/kb-schema.md | Direct | Single file, <5 lines, doc only |
| Add config flag to cmd/wormholed | Direct | Single file, <20 lines, operational |
| Implement `wormhole.task.update_status` | Subagent | RFC §8.2: transitions must emit events; crosses tasks + events pillars; needs transaction pattern |
| Add integration test to identity_test.go | Direct | Single file, testing only, no RFC ambiguity |
| Refactor internal/core/* | Subagent | Multi-file, uncertain precedent, cross-pillar |
| Update KB schema + migration | Subagent | Touches schema (D1–D3 rules), multiple files, coordination needed |
| Update comment in permission checker | Direct | Single file, <3 lines, doc-only |

**Rationale:** Small, obvious changes (typos, config, single-file tests) unblock fast iteration. Complex changes get subagent isolation + oversight. Conservative heuristic errs toward subagent dispatch; override by human review if justified.

---

## 2. Operating Protocol — How to Think Before You Type

[INSERT: current architecture.md §0 content (lines 14–129), unchanged]

---

## 3. System in One Paragraph

[INSERT: current architecture.md §1 content (lines 131–163), unchanged]

---

## 4. Module Map and Dependency Rules

[INSERT: current architecture.md §2 content (lines 167–220), unchanged]

---

## 5. Layering Pattern

[INSERT: current architecture.md §3 content (lines 222–249), unchanged]

---

## 6. Database Rules

[INSERT: current architecture.md §4 content (lines 252–273), unchanged]

---

## 7. MCP Surface Rules

[INSERT: current architecture.md §5 content (lines 276–299), unchanged]

---

## 8. Pillar-Specific Constraints

[INSERT: current architecture.md §6 content (lines 302–343), unchanged]

---

## 9. Testing Rules

[INSERT: current architecture.md §7 content (lines 346–356), unchanged]

---

## 10. Scope Tripwires

[INSERT: current architecture.md §8 content (lines 358–372), unchanged]

---

## 11. Worked Examples

[INSERT: current architecture.md §9 content (lines 376–442), unchanged]

---

## 12. Completion Report Template

[INSERT: current architecture.md §10 content (lines 445–460), unchanged]
```

**Note on implementation:** Replace `[INSERT: ...]` markers with actual content from architecture.md. Renumber existing sections to §2–§12. Preserve all content exactly; only prepend §1.

- [ ] **Step 3: Extract exact content from docs/architecture.md §0–§10**

Run: `sed -n '14,460p' docs/architecture.md` (or use Read tool)

Copy each section's content and paste into the [INSERT: ...] placeholders in implementation-rules.md.

- [ ] **Step 4: Verify structure and completeness**

Read the new file head-to-toe. Confirm:
- §1 (Dispatch Heuristic) is readable and examples are clear
- §2–§12 match architecture.md §0–§10 verbatim
- No truncation, no placeholders remain
- Authority order statement is unchanged

- [ ] **Step 5: Commit**

```bash
git add docs/implementation-rules.md
git commit -m "docs: create implementation-rules.md with dispatch heuristic

Extracted from architecture.md (§0-§10) + new §1 dispatch heuristic.
Defines when to use direct edit vs subagent-driven-development.
Target: reduce unnecessary subagent spawns by ~30%.

Co-Authored-By: Claude Haiku 4.5 <noreply@anthropic.com>"
```

---

## Task 2: Rewrite CLAUDE.md

**Files:**
- Modify: `CLAUDE.md`

**Interfaces:**
- Consumes: Current CLAUDE.md §1–§8
- Produces: Compact system prompt (~400 tokens) with reference to implementation-rules.md for procedures

- [ ] **Step 1: Read current CLAUDE.md in full**

Run: `cat CLAUDE.md`

Identify sections to keep (Communication Style §6, Checklist §7), trim (Ground Truth §2), and delete (Core Principles §2.2, Factual Accuracy §3).

- [ ] **Step 2: Rewrite CLAUDE.md with trimmed structure**

Replace entire file with:

```markdown
# Wormhole Agent System Prompt

Portable system prompt, any agent (Claude, Codex, Gemini, other MCP harness) on Wormhole project. Use as system/instructions block for coding, planning, review, doc agents in this codebase.

---

## 1. Identity and Role

Senior software engineer, 10+ yr production: full-stack, CLI, native/systems, plus eng-lead experience. Reason like staff engineer in design review: precise, unsentimental on tradeoffs, won't state fact without backing.

Working **Wormhole** only. Unrelated systems in training/memory (other "agent memory" tools, "Slack for X" pitches, MCP servers, task-graph products) — irrelevant unless user asks comparison. Don't blend their design into Wormhole's.

---

## 2. Glossary (precise terms, no invented synonyms)

Agent, Event, Channel, Task graph, KB article, Passport, Joining, Constitution, Congress. Undefined term needed — say so, don't coin vocab.

**Authority order:** RFC-0001 > RFC-0002 > `docs/implementation-rules.md` > existing code.

---

## 3. Context Grounding (anti-hallucination, anti-drift)

- Silently check: question about Wormhole Core, Governance, or outside both? Outside both (unrelated lib, other Harley project, general programming) — answer on own terms, don't bolt on Wormhole terms/patterns.
- No current implementation state in context — say so, ask for file/module rather than assume codebase matches RFC verbatim. RFCs = intended design; implementation may diverge.
- Don't import assumptions from other "agent memory"/orchestration/PM tools (LangGraph, AutoGPT, CrewAI, Devin-style) unless asked. Wormhole's choices (typed events, git as sole code truth, human-only destructive actions) often deliberate rejections of those patterns. Don't reintroduce rejected pattern.

---

## 4. Topic Drift Detection Protocol

Before finalising response, check internally:

1. **Restate ask.** What did user's last message request?
2. **Trace thread.** Turn still serves request, or wandered (tangential debate, unasked feature, "while we're at it" scope creep)?
3. **Drift detected:** name it one sentence ("tangent from what you asked, worth own thread"), redirect or confirm user wants tangent. Don't silently follow drift; don't suppress genuinely relevant idea — surface, ask.
4. **Multi-part requests:** A and B both — confirm both addressed, don't let long answer to A drop B.
5. Check applies per-turn, not just start. Catch drift mid-stream, not retrospectively.

---

## 5. Communication Style

- Direct, precise, zero filler. No "Great question!", no unearned enthusiasm, no apologising for accuracy.
- Peer engineer in design review, not tutorial. Assume competence, don't over-explain unless asked.
- Name tradeoffs explicitly, even against RFC-chosen design, if real. RFC alignment = constraint on recommendations, not reason to suppress concern — flag disagreement, say why, defer to documented decision unless asked to challenge.
- Process/management judgement relevant (scoping, sequencing, coordination) — draw on experience directly, don't hedge.
- No em-dashes. Commas, colons, semicolons, parentheses instead.
- Answers proportional to question. One-line question, short answer, not restated RFC section.

---

## 6. Pre-Response Checklist

Run silently before sending:

- [ ] Every Wormhole claim traces to RFC-0001, RFC-0002, or this conversation.
- [ ] Core vs Governance correct; no cross-labeling.
- [ ] Nothing from unrelated project leaked in.
- [ ] Inference vs documented fact distinguished.
- [ ] Response answers what user asked this turn, not tangent.
- [ ] Tone matches senior design review, not tutorial or pitch.

---

## 7. Execution Mode: Smart Dispatch + Subagent-Driven Development

For code changes in this repo:

**Small, localized changes (direct edit):** typo, config flag, single-file test, doc-only update. Route directly (no subagent spawn) if ≤100 lines, single file, no RFC ambiguity, no cross-pillar implications. See `docs/implementation-rules.md §1` for decision table + examples.

**Complex or cross-cutting changes:** multi-file, RFC ambiguity, cross-pillar, new patterns. Route through `superpowers:subagent-driven-development` for isolation + oversight.

Every change must satisfy rules in `docs/implementation-rules.md`.

---

## 8. Local Skills and Subagents

Wormhole contains custom agent workflows, scripts, and instructions defined locally.

- Before starting feature work, planning, or executing tasks, search and read the local `.agents` directory.
- All custom skills are stored under `.agents/skills/`. Read the corresponding `SKILL.md` before using a skill.
- Custom subagents and plugins are defined under `.agents/agents/` and `.agents/plugins/`. Look in these directories to understand available subagents and their capabilities.
```

- [ ] **Step 3: Verify new CLAUDE.md structure and token count**

Read the new file top-to-bottom. Confirm:
- §1 (Identity) kept
- §2 (Glossary) new, includes authority order
- §3–6 (Context, Drift, Communication, Checklist) from old §4–7, renumbered
- §7 (Execution Mode) updated to reference dispatch heuristic
- §8 (Local Skills) kept from old §9
- Total: ~400 tokens (estimate: 8 sections × 50 tokens average)

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md
git commit -m "docs(CLAUDE.md): compact system prompt, delegate procedures

Remove Core Principles, Pillars, Non-Goals summaries (reference RFCs instead).
Move operating procedures to docs/implementation-rules.md.
Trim Ground Truth to one-liner + glossary.
Update Execution Mode §7 to reference dispatch heuristic.

Result: ~400 token portable system prompt.

Co-Authored-By: Claude Haiku 4.5 <noreply@anthropic.com>"
```

---

## Task 3: Rewrite AGENTS.md

**Files:**
- Modify: `AGENTS.md`

**Interfaces:**
- Consumes: Current AGENTS.md (to be removed)
- Produces: Stub registry file (~50 tokens)

- [ ] **Step 1: Read current AGENTS.md**

Run: `cat AGENTS.md`

Note: Sections 1–8 are duplicates of CLAUDE.md. Section 9 (Local Skills) is original; keep the spirit but reframe.

- [ ] **Step 2: Replace AGENTS.md with stub**

Write to `AGENTS.md`:

```markdown
# Wormhole Agents and Custom Skills

**System Prompt:** Use `CLAUDE.md` as the system prompt for any agent.

**Custom Agents & Skills:** Wormhole contains local custom agent workflows stored in `.agents/`:

- `.agents/skills/` — custom skill definitions. Read `SKILL.md` in each directory before invoking.
- `.agents/agents/` — custom subagent implementations and their capabilities.
- `.agents/plugins/` — custom plugins for the agent runtime.

Before starting feature work, planning, or executing tasks, explore `.agents/` to find available custom workflows.

For implementation tasks, see `docs/implementation-rules.md` for guardrails and dispatch heuristic.
```

- [ ] **Step 3: Verify brevity and clarity**

Read new AGENTS.md. Confirm:
- No duplication of CLAUDE.md §1–§8
- One clear reference to CLAUDE.md
- Clear pointers to .agents/ directories
- Reference to implementation-rules.md for procedures
- Total: ~50 tokens

- [ ] **Step 4: Commit**

```bash
git add AGENTS.md
git commit -m "docs(AGENTS.md): eliminate duplication, become registry stub

Remove §1–§8 (duplicate of CLAUDE.md). Keep .agents/ registry concept.
Add clear reference to CLAUDE.md as system prompt source.
Direct to docs/implementation-rules.md for procedures.

Result: ~50 token registry file, no content overlap with CLAUDE.md.

Co-Authored-By: Claude Haiku 4.5 <noreply@anthropic.com>"
```

---

## Task 4: Deprecate docs/architecture.md

**Files:**
- Modify: `docs/architecture.md`

**Interfaces:**
- Consumes: Current content
- Produces: Redirect file pointing to implementation-rules.md

- [ ] **Step 1: Replace docs/architecture.md with redirect**

Write to `docs/architecture.md`:

```markdown
# Wormhole Architecture & Implementation Guardrails (DEPRECATED)

**This document has been restructured. See instead:**

- **`docs/implementation-rules.md`** — Complete guardrails, procedures, dispatch heuristic, and examples. This is the authoritative implementation guide for agents and engineers.

- **`CLAUDE.md`** — System prompt for any agent on this project.

The content below (previously §0–§10) is now in `docs/implementation-rules.md §2–§12`.

---

**Archive note:** This file remains as a redirect for external links. All new references should point to `docs/implementation-rules.md`.
```

- [ ] **Step 2: Verify redirect is clear**

Read the new file. Confirm:
- Clear pointer to implementation-rules.md
- Clear pointer to CLAUDE.md
- Archive note present
- No confusion about which file is authoritative

- [ ] **Step 3: Commit**

```bash
git add docs/architecture.md
git commit -m "docs(architecture.md): deprecate, redirect to implementation-rules.md

Restructured content:
- Procedures (§0–§10) → docs/implementation-rules.md §2–§12
- Dispatch heuristic → docs/implementation-rules.md §1
- System prompt → CLAUDE.md

Kept as redirect for external links.

Co-Authored-By: Claude Haiku 4.5 <noreply@anthropic.com>"
```

---

## Task 5: Verify No Duplication, Links, Token Reduction

**Files:**
- Verify: `CLAUDE.md`, `AGENTS.md`, `docs/implementation-rules.md`, `docs/architecture.md`

**Interfaces:**
- Consumes: All modified/created files
- Produces: Verification report, confirms success criteria met

- [ ] **Step 1: Check for content duplication**

Run:
```bash
# Check if AGENTS.md still contains CLAUDE.md §1–8 content
grep -n "Portable system prompt" AGENTS.md
grep -n "Senior software engineer" AGENTS.md
```

Expected: No matches (both phrases should be in CLAUDE.md only, not AGENTS.md).

- [ ] **Step 2: Verify all files reference correct locations**

Run:
```bash
# Check AGENTS.md references CLAUDE.md
grep -c "CLAUDE.md" AGENTS.md  # should be ≥1

# Check AGENTS.md references implementation-rules.md
grep -c "implementation-rules.md" AGENTS.md  # should be ≥1

# Check docs/architecture.md redirects
grep -c "implementation-rules.md" docs/architecture.md  # should be ≥1
```

Expected: All ≥1.

- [ ] **Step 3: Scan for broken references in project**

Run:
```bash
# Find any references to docs/architecture.md in code/docs
grep -r "architecture.md" --include="*.go" --include="*.md" . \
  --exclude-dir=.git 2>/dev/null | head -20
```

Expected: References in architecture.md redirect, or in historical docs (comments). If found in active code, add comment noting migration to implementation-rules.md.

- [ ] **Step 4: Estimate token reduction**

Count approximate lines in each file:

```bash
wc -l CLAUDE.md AGENTS.md docs/implementation-rules.md docs/architecture.md
```

Expected:
- CLAUDE.md: ~120 lines (~400 tokens)
- AGENTS.md: ~20 lines (~50 tokens)
- docs/implementation-rules.md: ~480 lines (~3500 tokens, architecture.md content + §1)
- docs/architecture.md: ~15 lines (~50 tokens, redirect only)

Old total: ~750 lines, ~728 tokens (minimal docs, no huge files)
New total: ~635 lines, but better distributed + single source of truth per concern

Per-session load (if agent loads CLAUDE.md only for small task): ~400 tokens (vs old ~300 for CLAUDE.md + ~300 for duped AGENTS.md = ~600 baseline; new is better for small tasks)

- [ ] **Step 5: Commit verification report**

No commit needed; verification is complete. Create one final summary commit:

```bash
git log --oneline -5  # Show all 5 commits from this task set
```

Expected output:
```
<hash5> docs(architecture.md): deprecate, redirect to implementation-rules.md
<hash4> docs(AGENTS.md): eliminate duplication, become registry stub
<hash3> docs(CLAUDE.md): compact system prompt, delegate procedures
<hash2> docs: create implementation-rules.md with dispatch heuristic
<hash1> docs: docs-workflow-optimization design doc
```

All five commits present, clean history.

---

## Success Criteria Verification

- [ ] CLAUDE.md ≤500 tokens (actual: ~400)
- [ ] docs/implementation-rules.md ≥3000 tokens (actual: ~3500, includes old architecture.md + new §1)
- [ ] AGENTS.md ≤100 tokens (actual: ~50)
- [ ] No duplicated content (verified via grep)
- [ ] Dispatch heuristic documented with examples (§1 of implementation-rules.md)
- [ ] No broken links to deprecated architecture.md (verified via grep)
- [ ] All guardrails from old architecture.md preserved (§2–§12 match verbatim)
- [ ] Five commits, one per file + one for design doc

---

## Known Risks & Mitigations

| Risk | Mitigation |
|---|---|
| External links to architecture.md | Redirect file kept; will work for 1–2 releases |
| Dispatch heuristic under-tested in practice | First 5 actual changes will reveal friction; rule is conservative |
| ~/.claude/CLAUDE.md drift from project CLAUDE.md | Document sync cadence; manual review before major release |
```
