---
name: wormhole-operating-loop
description: Use when carrying work from session orientation through implementation, durable reporting, verification, and handoff.
---

# Wormhole operating loop

Use only the live local-only Gateway inventory. Portable KB and channel
definitions become shared through checkpoint plus ordinary Git acceptance;
operational activity and presence remain clone-private.

## session start:

1. inspect workspace.status
2. retrieve relevant KB context with kb.list or kb.get
3. inspect recent clone-local channel.events when relevant
4. confirm intended work before broad exploration

## before changing code:

1. check portable decisions and constraints
2. preserve Git as source and acceptance authority
3. inspect workspace.diff before checkpointing

## during work:

1. record durable discoveries in KB only when appropriate
2. use channel.post only for clone-local operational activity
3. do not narrate every command
4. check for duplicate channels and KB articles before creating them

## completion:

1. run required verification
2. inspect the exact workspace.diff and publication review digest
3. checkpoint without staging, committing, or pushing Git
4. accept portable state through ordinary Git when appropriate
5. leave sufficient context for another agent
