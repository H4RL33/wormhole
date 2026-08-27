---
name: wormhole-tool-use
description: Use when choosing or calling a live Wormhole Gateway tool and checking its permissions, schema, freshness, or side effects.
---

# Wormhole Gateway tool use

Call only tools in this live Gateway inventory. Each request still requires its live schema and permissions.

## Portable local context

Use kb.list and kb.get for deterministic portable KB reads; semantic Fabric search is not in this live Gateway inventory.

## `wormhole.agent.list`

- Purpose: List agents known to the local scheduler.
- Use when: Before routing work by capability or checking local availability.
- Do not use when: Do not use it as a complete organisation-wide identity directory.
- Mutates state: false
- Required permissions: none
- Prerequisites: A bound Gateway project.
- Freshness implications: Results cover only the current clone-local scheduler state.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Update presence when a registered agent's local availability changes.
- Minimal request example: `{"project_id":"example"}`
- Live request schema: `{"properties":{"project_id":{"type":"string"}},"required":["project_id"],"type":"object"}`
- Misuse warning: Do not infer shared identity, remote presence, or authorization from this local list.

## `wormhole.agent.presence`

- Purpose: Update an existing locally registered agent's availability.
- Use when: When an agent starts, becomes busy, or stops accepting work.
- Do not use when: Do not use it to create an agent or assign work.
- Mutates state: true
- Required permissions: none
- Prerequisites: The agent must already be locally registered.
- Freshness implications: Presence is Gateway-local and is not durable shared task state.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Use agent.list to verify the current local scheduler view.
- Minimal request example: `{"agent_id":"example","project_id":"example","status":"example"}`
- Live request schema: `{"properties":{"agent_id":{"type":"string"},"project_id":{"type":"string"},"status":{"type":"string"}},"required":["agent_id","status","project_id"],"type":"object"}`
- Misuse warning: Do not assume a local presence update grants permissions.

## `wormhole.agent.register`

- Purpose: Register an existing agent's local presence and declared capabilities.
- Use when: When making a known agent available to local routing.
- Do not use when: Do not use it for a routine presence heartbeat.
- Mutates state: true
- Required permissions: none
- Prerequisites: A server-resolved workspace and an existing agent identifier.
- Freshness implications: Presence registration is Gateway-local scheduler state.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Set presence after local registration, then verify with agent.list.
- Minimal request example: `{"agent_id":"example","project_id":"example"}`
- Live request schema: `{"additionalProperties":false,"properties":{"agent_id":{"type":"string"},"capabilities":{"items":{"type":"string"},"type":"array"},"project_id":{"type":"string"}},"required":["agent_id","project_id"],"type":"object"}`
- Misuse warning: Do not infer shared identity creation or new permissions from local presence registration.

## `wormhole.channel.create`

- Purpose: Create a portable channel in this workspace's private candidate overlay.
- Use when: When durable event routing needs a new named channel.
- Do not use when: Do not use it for one-off messages better represented by an existing channel.
- Mutates state: true
- Required permissions: channel.create
- Prerequisites: A Gateway-resolved workspace and channel.create permission.
- Freshness implications: The channel is immediately visible in the composed candidate and becomes portable through checkpoint and Git acceptance.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Post a typed event after creation.
- Minimal request example: `{"name":"example","project_id":"example"}`
- Live request schema: `{"properties":{"name":{"type":"string"},"project_id":{"type":"string"}},"required":["name","project_id"],"type":"object"}`
- Misuse warning: Do not create duplicate channels to represent the same ongoing topic.

## `wormhole.channel.events`

- Purpose: List recent durable events from local channels.
- Use when: When reconstructing recent local collaboration context.
- Do not use when: Do not use it as a live subscription or a portable audit history.
- Mutates state: false
- Required permissions: none
- Prerequisites: A Gateway-resolved workspace.
- Freshness implications: Reads clone-private operational activity for the resolved workspace only.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Use channel.subscribe for subsequent local events or inspect a referenced KB article.
- Minimal request example: `{"project_id":"example"}`
- Live request schema: `{"properties":{"project_id":{"type":"string"}},"required":["project_id"],"type":"object"}`
- Misuse warning: Operational events do not enter checkpointed portable state or another clone.

## `wormhole.channel.list`

- Purpose: List channels from this workspace's composed portable project state.
- Use when: When choosing an existing durable channel for a typed event.
- Do not use when: Do not use it to inspect event history.
- Mutates state: false
- Required permissions: none
- Prerequisites: A Gateway-resolved workspace.
- Freshness implications: Includes accepted tracked state plus the current private candidate overlay.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Read channel.events or create a missing channel.
- Minimal request example: `{"project_id":"example"}`
- Live request schema: `{"properties":{"project_id":{"type":"string"}},"required":["project_id"],"type":"object"}`
- Misuse warning: A candidate channel is not accepted Git state until checkpoint and ordinary Git acceptance.

## `wormhole.channel.post`

- Purpose: Publish clone-private operational activity after validating its portable channel.
- Use when: When recording a handoff, discovery, decision, or progress update.
- Do not use when: Do not use it for sensitive credentials or unstructured chatter.
- Mutates state: true
- Required permissions: channel.post
- Prerequisites: A Gateway-resolved workspace, a live portable channel ID, and channel.post permission.
- Freshness implications: The event is durable in this clone's private operational store and is not queued for Fabric.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Use channel.events to confirm the local activity or reference a relevant KB article.
- Minimal request example: `{"channel_id":"example","event_type":"example","project_id":"example"}`
- Live request schema: `{"properties":{"channel_id":{"type":"string"},"event_type":{"type":"string"},"note":{"type":"string"},"payload":{},"project_id":{"type":"string"}},"required":["channel_id","event_type","project_id"],"type":"object"}`
- Misuse warning: Do not put secrets, source copies, or unsupported event types in the payload.

## `wormhole.channel.subscribe`

- Purpose: Subscribe this MCP connection to all future event notifications in its resolved workspace.
- Use when: When subsequent events from the exact local workspace are needed during the active session.
- Do not use when: Do not use it to recover historical events or create durable shared state.
- Mutates state: true
- Required permissions: none
- Prerequisites: An initialized MCP connection and a bound project.
- Freshness implications: Notifications reflect future clone-local delivery only and are not replayed after reconnect.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Keep the connection open and use channel.events for prior context.
- Minimal request example: `{"project_id":"example"}`
- Live request schema: `{"properties":{"project_id":{"type":"string"}},"required":["project_id"],"type":"object"}`
- Misuse warning: Do not assume a subscription survives reconnects or provides a complete audit stream.

## `wormhole.kb.get`

- Purpose: Get a named KB article or list articles when no ID is supplied.
- Use when: When reading a known durable procedure, decision, or discovery.
- Do not use when: Do not use it to retrieve code as an authoritative source.
- Mutates state: false
- Required permissions: none
- Prerequisites: A Gateway-resolved workspace; supply an article ID for a specific record.
- Freshness implications: Reads the current composed portable view, including uncheckpointed candidate operations.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Verify referenced Git pointers and update stale durable knowledge with kb.write.
- Minimal request example: `{"project_id":"example"}`
- Live request schema: `{"properties":{"article_id":{"type":"string"},"project_id":{"type":"string"}},"required":["project_id"],"type":"object"}`
- Misuse warning: Git remains code truth; do not rely on KB prose instead of current source verification.

## `wormhole.kb.list`

- Purpose: List KB articles from this workspace's composed portable project state.
- Use when: When locating durable organisational context by article metadata.
- Do not use when: Do not use it when a known article ID can be fetched directly.
- Mutates state: false
- Required permissions: none
- Prerequisites: A Gateway-resolved workspace.
- Freshness implications: Includes accepted tracked state plus the current private candidate overlay.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Read a selected article or write a new durable fact.
- Minimal request example: `{"project_id":"example"}`
- Live request schema: `{"properties":{"project_id":{"type":"string"}},"required":["project_id"],"type":"object"}`
- Misuse warning: This is deterministic listing, not semantic search.

## `wormhole.kb.write`

- Purpose: Write a portable KB article into this workspace's private candidate overlay.
- Use when: When preserving a durable fact, decision, discovery, or procedure.
- Do not use when: Do not use it for transient status chatter or source-file copies.
- Mutates state: true
- Required permissions: kb.write
- Prerequisites: A Gateway-resolved workspace, a published matching portable actor, and kb.write permission.
- Freshness implications: The article is immediately visible in the composed candidate and becomes portable through checkpoint and Git acceptance.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Use kb.get to verify the article, then inspect workspace.diff before checkpointing.
- Minimal request example: `{"project_id":"example","title":"example"}`
- Live request schema: `{"properties":{"body":{"type":"string"},"frontmatter":{},"project_id":{"type":"string"},"title":{"type":"string"}},"required":["title","project_id"],"type":"object"}`
- Misuse warning: Do not store credentials or present Git-derived prose as authoritative code.

## `wormhole.sync.status`

- Purpose: Report the truthful local-only synchronization state and pending Fabric-write count.
- Use when: When confirming that this Stage 2 Gateway is offline and has no Fabric queue.
- Do not use when: Do not use it as a Fabric health probe or assume it contacts a remote service.
- Mutates state: false
- Required permissions: none
- Prerequisites: A bound Gateway project.
- Freshness implications: Always reports offline with zero pending writes in the local-only Stage 2 runtime.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Use workspace.status or workspace.diff to inspect local portable state.
- Minimal request example: `{"project_id":"example"}`
- Live request schema: `{"properties":{"project_id":{"type":"string"}},"required":["project_id"],"type":"object"}`
- Misuse warning: Offline is the expected local-only state, not a failed network probe.

## `wormhole.workspace.checkpoint`

- Purpose: Materialize the current portable candidate without performing Git publication.
- Use when: After reviewing the semantic diff and any required public-Git acknowledgement.
- Do not use when: Do not use it as a substitute for Git staging, commit, or push.
- Mutates state: true
- Required permissions: none
- Prerequisites: A registered workspace; public Git requires the exact current publication review digest.
- Freshness implications: The supplied acknowledgement is rejected if the candidate changed after review.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Review the unstaged tracked tree, then accept it with ordinary Git commands.
- Minimal request example: `{"project_id":"example"}`
- Live request schema: `{"additionalProperties":false,"properties":{"project_id":{"type":"string"},"publication_review_digest":{"type":"string"}},"required":["project_id"],"type":"object"}`
- Misuse warning: Checkpoint never stages, commits, or pushes Git.

## `wormhole.workspace.diff`

- Purpose: Return the attributed semantic portable-state diff and exact publication review digest.
- Use when: Before checkpointing or reviewing tracked portable-state changes.
- Do not use when: Do not use it to inspect arbitrary source-code changes.
- Mutates state: false
- Required permissions: none
- Prerequisites: A registered workspace with accepted portable state.
- Freshness implications: Compares the current composed candidate against the accepted portable snapshot.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Review changes and pass the exact digest to checkpoint when public publication requires acknowledgement.
- Minimal request example: `{"project_id":"example"}`
- Live request schema: `{"additionalProperties":false,"properties":{"project_id":{"type":"string"}},"required":["project_id"],"type":"object"}`
- Misuse warning: A review digest is bound to the exact candidate and becomes stale after any mutation.

## `wormhole.workspace.import`

- Purpose: Import direct tracked portable-state edits into the attributed workspace candidate.
- Use when: After editing .wormhole/state/v1 through ordinary repository tools.
- Do not use when: Do not use it to import private databases, credentials, or operational journals.
- Mutates state: true
- Required permissions: none
- Prerequisites: A registered workspace and a valid portable working tree.
- Freshness implications: Reads the exact current portable tree and rebases the private overlay through its imported generation.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Inspect workspace.diff before checkpointing.
- Minimal request example: `{"project_id":"example"}`
- Live request schema: `{"additionalProperties":false,"properties":{"project_id":{"type":"string"}},"required":["project_id"],"type":"object"}`
- Misuse warning: Import does not stage, commit, or push Git.

## `wormhole.workspace.stash`

- Purpose: Durably stash the current private overlay under an explicit request ID and label.
- Use when: When pausing attributed work without changing accepted portable state.
- Do not use when: Do not use it as Git stash or as a publication action.
- Mutates state: true
- Required permissions: none
- Prerequisites: A registered workspace, unique request ID, and non-empty label.
- Freshness implications: Captures the exact current overlay and candidate digest in private state.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Confirm workspace.status and resume through the supported workspace lifecycle.
- Minimal request example: `{"label":"example","project_id":"example","request_id":"example"}`
- Live request schema: `{"additionalProperties":false,"properties":{"label":{"type":"string"},"project_id":{"type":"string"},"request_id":{"type":"string"}},"required":["request_id","label","project_id"],"type":"object"}`
- Misuse warning: Private stash rows are not portable and never enter tracked Git state.

## `wormhole.workspace.status`

- Purpose: Inspect the bound workspace candidate, overlay generation, and publication review state.
- Use when: Before importing, checkpointing, or deciding whether a public-Git acknowledgement is required.
- Do not use when: Do not use it as a Git status replacement or to mutate portable state.
- Mutates state: false
- Required permissions: none
- Prerequisites: A registered workspace resolved by Gateway.
- Freshness implications: Reports current private workspace bookkeeping and the accepted tracked snapshot without changing either.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Use workspace.diff to inspect exact portable changes.
- Minimal request example: `{"project_id":"example"}`
- Live request schema: `{"additionalProperties":false,"properties":{"project_id":{"type":"string"}},"required":["project_id"],"type":"object"}`
- Misuse warning: Do not treat candidate presence as Git acceptance.
