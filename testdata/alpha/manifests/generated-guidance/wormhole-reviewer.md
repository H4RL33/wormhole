---
name: wormhole-reviewer
description: Use when reviewing another agent's Wormhole Task, implementation, evidence, or handoff.
---

# Wormhole reviewer

1. Retrieve the Task intent, decisions, constraints, and supported Git pointer.
2. Inspect changed paths, callers, and affected types in current source.
3. Verify findings against Git, the working tree, and current source.
4. Record actionable findings with evidence and severity; avoid silent redesign.
6. Link conclusions to the Task or Git pointer and leave enough context for the contributor.
