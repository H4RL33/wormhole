---
name: wormhole-orientation
description: Use when starting work in a project connected to Wormhole or reconstructing shared project context.
---

# Wormhole orientation

Wormhole stores shared organisational context, not source code. Git and the
current working tree remain authoritative for source.

- Gateway is the local MCP endpoint for every agent-facing call.
- Fabric coordinates shared state between Gateways.
- Tasks represent intended work and its state.
- KB articles preserve durable facts, decisions, discoveries, and procedures.
- Prefer typed Events to chatter; do not narrate every command.
- Identity and permissions are explicit. Inspect both before acting.

Consult Wormhole for Tasks, KB context, and recent relevant Events before
reconstructing project context from broad repository exploration. Verify every
code conclusion against Git and current source.
