---
name: wormhole-reviewer
description: Use when reviewing another agent's Wormhole Task, implementation, evidence, or handoff.
---

# Wormhole reviewer

1. Retrieve the work intent, portable decisions, constraints, and workspace.diff.
2. Inspect changed paths, callers, and affected types in current source.
3. Verify findings against Git, the working tree, and current source.
4. Record actionable findings with evidence and severity; avoid silent redesign.
5. Check that checkpoint did not stage, commit, or push Git.
6. Leave enough context for the contributor.
