# Wormhole Docs & Workflow Optimization Design

**Date:** 2026-07-18  
**Status:** Approved  
**Scope:** Document deduplication + dispatch rule refinement

---

## 1. Problem

Current state has three issues:

1. **CLAUDE.md and AGENTS.md duplicate 99%** (sections 1–8 identical). Maintenance burden: change one, risk forgetting the other.
2. **Docs lack clear ownership.** CLAUDE.md mixes system prompt (portable) with operational procedures (implementation-specific). architecture.md repeats authority hierarchy and concept summaries.
3. **Subagent dispatch overhead untamed.** Every change (including one-line typos) spawns a subagent. Startup cost + context rebuild is wasteful for small, obvious changes.

**Token impact:** ~728 tokens across three files in any agent session. Per-session overhead on small tasks: unnecessary context loading.

---

## 2. Solution: Approach 3 (Smart Routing + Tiered Docs)

### 2.1 Document Restructuring

**Three authoritative files, clear roles:**

| File | Purpose | Size | Audience |
|---|---|---|---|
| `CLAUDE.md` | System prompt (portable, model-agnostic) | ~400 tokens | Any agent on any harness |
| `docs/implementation-rules.md` | Operational guardrails, procedures | ~3500 tokens | Implementation agents (Haiku dispatch) |
| `AGENTS.md` | Skill/agent registry | ~50 tokens | Humans, not agents |

**Content split:**

- **CLAUDE.md (new):**
  - §1: Identity + Role (unchanged)
  - §2: Glossary (Agent, Event, Channel, Task graph, KB article, Passport, Joining, Constitution, Congress)
  - §3–7: Communication Style, Drift Detection, Context Grounding, Checklist, Topic Drift (unchanged)
  - §8: Execution Mode → "Direct edit for small changes per implementation-rules.md §1.1; larger changes via subagent-driven-development"
  - **Removed:** Core Principles, Four Pillars, Non-Goals (summarize as "see RFC-0001/0002"), Factual Accuracy Protocol (moved)

- **docs/implementation-rules.md (new):**
  - §1: **Dispatch heuristic** (new; see below)
  - §2–11: Current architecture.md §0–§10 (renumbered, content unchanged)

- **AGENTS.md (new):**
  - One-liner: "Use CLAUDE.md as system prompt. Custom agents in `.agents/`"
  - **Removed:** All of §1–8 (duplicate of CLAUDE.md)

- **docs/architecture.md (deprecated):**
  - Keep as redirect file with link to implementation-rules.md, or delete after external link scan.

### 2.2 Dispatch Heuristic (New §1 in implementation-rules.md)

**Use direct edit (bypass subagent-driven-development) if ALL conditions hold:**

1. Single file touched
2. ≤100 lines of code change
3. No RFC ambiguity (task cites RFC section, decision is clear)
4. No cross-pillar implications (only one of: events/tasks/kb/identity/permissions affected; touching only config, docs, tests is always OK)

**Otherwise → subagent-driven-development.**

**Examples:**

| Change | Route | Reasoning |
|---|---|---|
| Typo in docs/kb-schema.md | Direct | Single file, <5 lines, no ambiguity |
| Add feature flag to config | Direct | Single file, <10 lines, operational only |
| Implement `wormhole.task.update_status` | Subagent | RFC §8.2 (transitions emit events), crosses tasks + events, needs transaction pattern |
| Refactor internal/core/* | Subagent | Multi-file, cross-pillar, pattern precedent uncertain |
| Add test to identity_test.go | Direct | Single file, testing only, no RFC ambiguity |
| Update DB schema + migration | Subagent | Touches core + storage, D1–D3 rules must hold, coordination needed |

### 2.3 Token Impact

**Current:**  
- Any agent loads: CLAUDE.md (129) + AGENTS.md (139, identical) + architecture.md (460 if needed) = ~728 tokens minimum
- Subagent spawn: copy of full context + new agent overhead

**New:**  
- Small task (typo, config): Load CLAUDE.md only (~400 tokens)
- Implementation task: Load implementation-rules.md + CLAUDE.md (~3900 tokens, subagent dispatch)
- Subagent spawns: ~30% fewer (typos, config, tests, single-file edits go direct)
- Per-session average: 40% reduction on small tasks; no regression on complex tasks (subagent always gets full rules)

---

## 3. Implementation Sequence

1. Create `docs/implementation-rules.md`
   - Copy architecture.md §0–§10
   - Prepend §1 (Dispatch heuristic + examples)
   - Renumber to §2–§11
   - Commit: "docs: extract implementation rules from architecture.md"

2. Rewrite CLAUDE.md
   - Trim Ground Truth (§2) to executive one-liner + RFC link
   - Delete §2.2–2.4 (Core Principles, Pillars, Non-Goals)
   - Keep §2.6 (Glossary, renumber to §2)
   - Keep §3–7 (Communication, Drift, Context, Checklist)
   - Update §8 (Execution Mode) to reference dispatch heuristic
   - Commit: "docs(CLAUDE.md): compact system prompt, move procedures to implementation-rules"

3. Rewrite AGENTS.md
   - Delete §1–8
   - Replace with stub + .agents/ reference
   - Commit: "docs(AGENTS.md): eliminate duplication, reference CLAUDE.md"

4. Deprecate docs/architecture.md
   - Replace content with redirect link to implementation-rules.md
   - Commit: "docs(architecture.md): deprecated, redirect to implementation-rules.md"

5. Update ~/.claude/CLAUDE.md (if exists)
   - Mirror project CLAUDE.md structure
   - Manual sync going forward (not automated)

---

## 4. Maintenance Rules (Going Forward)

- **CLAUDE.md:** Change only if communication style, identity, or execution mode shifts. Sync with ~/.claude/CLAUDE.md manually.
- **docs/implementation-rules.md:** Change if RFC interpretation evolves, new modules land, or guardrails need tightening. Authority: RFC > RIR (this file) > code.
- **AGENTS.md:** Change only when adding/removing custom skills in `.agents/` directory.
- **No more duplication:** Single source of truth per concern.

---

## 5. Risk & Mitigation

| Risk | Likelihood | Mitigation |
|---|---|---|
| Dispatch rule under-filters; small change introduces bug | Low | Heuristic is conservative (≤100 lines, single file, no RFC ambiguity). Humans can override on review. |
| Dispatch rule over-filters; subagent overhead stays high | Medium | Monitor subagent spawn logs; if >70% of changes are subagents, relax rule. |
| External links to architecture.md break | Low | Create redirect or keep file as shim for 1–2 release cycles. |
| CLAUDE.md drift from ~/.claude/CLAUDE.md | Medium | Document sync process; add to checklist before major releases. |

---

## 6. Success Criteria

- [ ] CLAUDE.md ≤500 tokens (portable system prompt)
- [ ] docs/implementation-rules.md ≥3000 tokens (comprehensive guardrails)
- [ ] AGENTS.md ≤100 tokens (registry only, no duplication)
- [ ] Dispatch heuristic documented + examples provided
- [ ] No broken links to deprecated architecture.md
- [ ] First 5 changes tested: small changes use direct route (no subagent spawn logged)
- [ ] No new duplicated content introduced in future edits

---

## 7. Open Questions / Future

- Exact threshold for "no RFC ambiguity" heuristic: can be tuned empirically after first week
- Whether to auto-generate docs/architecture.md redirect or fully delete: depends on external link scan
- ~/.claude/CLAUDE.md sync cadence: monthly, quarterly, or on-demand?
