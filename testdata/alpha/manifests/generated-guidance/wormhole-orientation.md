---
name: wormhole-orientation
description: Use when starting work in a project connected to Wormhole or reconstructing shared project context.
---

# Wormhole orientation

Wormhole stores shared organisational context, not source code. Git and the
current working tree remain authoritative for source.

- Gateway is the local MCP endpoint for every live agent-facing call.
- This Stage 2 inventory is local-only and does not contact optional Fabric.
- Portable channels and KB articles live in tracked project state after ordinary Git acceptance.
- Channel activity, agent registration, and presence remain clone-private operational state.
- Workspace tools inspect, import, diff, checkpoint, and stash portable candidates.

Consult live KB and channel context before broad repository exploration when
that context could answer the question. Verify every code conclusion against
Git and current source.
