---
name: wormhole-operating-loop
description: Use when carrying work from session orientation through implementation, durable reporting, verification, and handoff.
---

# Wormhole operating loop

Use shared KB semantic search for organisational decisions, procedures, and discoveries before broad repository reconstruction when that context could answer the question.

Use Code Graph only for project-local source structure and bounded code discovery when it is enabled and current; it is separate from the shared KB and does not replace Git or verification.

If the semantic provider or active index is unavailable, there is no lexical fallback; do not label degraded retrieval as semantic ranking.

if Code Graph is ready:
    query it before broad code discovery
else:
    continue with normal filesystem and repository tools

## session start:

1. inspect identity and permissions
2. inspect assigned and relevant Tasks
3. retrieve relevant KB context
4. inspect recent relevant Events
5. inspect Code Graph status for code tasks
6. confirm intended work before broad exploration

## before changing code:

1. retrieve the Task and links
2. check decisions and constraints
3. use Code Graph when ready and useful
4. report work begun when supported
5. preserve Git as authority

## during work:

1. record meaningful blockers
2. publish only durable discoveries
3. do not narrate every command
4. prefer typed Events
5. check for duplicate Tasks and KB articles before creating them

## completion:

1. run required verification
2. update Task state
3. link the commit or pull request where supported
4. record durable knowledge
5. publish one concise completion Event
6. leave sufficient context for another Agent
