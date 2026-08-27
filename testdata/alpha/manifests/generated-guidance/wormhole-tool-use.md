---
name: wormhole-tool-use
description: Use when choosing or calling a live Wormhole Gateway tool and checking its permissions, schema, freshness, or side effects.
---

# Wormhole Gateway tool use

Call only tools in this live Gateway inventory. Each request still requires its live schema and permissions.

## Shared KB

Use shared KB semantic search for organisational decisions, procedures, and discoveries before broad repository reconstruction when that context could answer the question.

If the semantic provider or active index is unavailable, there is no lexical fallback; do not label degraded retrieval as semantic ranking.

## `wormhole.agent.enrol`

- Purpose: Enroll a Gateway project and persist its credential profile before Passport credentials exist.
- Use when: During explicit Gateway-owned project enrollment or credential recovery.
- Do not use when: Do not use it for normal authenticated agent registration.
- Mutates state: true
- Required permissions: none
- Prerequisites: A human-approved project binding, Fabric address, and credential-profile identifier beneath the Gateway credential root.
- Freshness implications: Enrollment performs durable local lifecycle work and may need recovery or sync after an interrupted attempt.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Inspect sync.status and follow the returned lifecycle code before retrying.
- Minimal request example: `{"capabilities":[],"credential_profile":"example","fabric_address":"example","idempotency_key":"example","model":"example","owner":"example","project_id":"example","repositories":[],"requested_permissions":[],"roles":[],"version":0}`
- Live request schema: `{"additionalProperties":false,"properties":{"capabilities":{"items":{"type":"string"},"type":"array"},"credential_profile":{"minLength":1,"type":"string"},"fabric_address":{"type":"string"},"idempotency_key":{"type":"string"},"model":{"type":"string"},"owner":{"type":"string"},"project_id":{"type":"string"},"repositories":{"items":{"type":"string"},"type":"array"},"requested_permissions":{"items":{"type":"string"},"type":"array"},"roles":{"items":{"type":"string"},"type":"array"},"version":{"type":"integer"}},"required":["version","project_id","owner","model","capabilities","repositories","roles","requested_permissions","fabric_address","idempotency_key","credential_profile"],"type":"object"}`
- Misuse warning: Do not expose credential material or reuse an attempt key for a different enrollment.

## `wormhole.agent.get_guidance`

- Purpose: Read the current approved, role-applicable integration guidance and its lifecycle state from Gateway's local cache.
- Use when: At session start or before relying on managed organisational guidance for this project.
- Do not use when: Do not use it to approve, apply, update, remove, roll back, refresh, or repair guidance.
- Mutates state: false
- Required permissions: none
- Prerequisites: An explicitly bound project and a compatible approved manifest cached by Gateway.
- Freshness implications: The result is one local cached read; offline responses retain approved content while separately reporting newer unapproved pending state.
- Source-access implications: This tool returns approved Markdown only; it does not read repository files or expose materialisation target paths.
- Recommended follow-up: Use applicable returned guidance, or ask a human to inspect the integration CLI when approval, compatibility, drift, or recovery needs attention.
- Minimal request example: `{"project_id":"example"}`
- Live request schema: `{"additionalProperties":false,"properties":{"project_id":{"format":"uuid","type":"string"}},"required":["project_id"],"type":"object"}`
- Misuse warning: Empty guidance can mean no approved cache, revocation, or incompatibility; never infer approval or trigger a mutation from this read.

## `wormhole.agent.list`

- Purpose: List agents known to the local scheduler.
- Use when: Before routing work by capability or checking local availability.
- Do not use when: Do not use it as a complete organisation-wide identity directory.
- Mutates state: false
- Required permissions: none
- Prerequisites: A bound Gateway project.
- Freshness implications: Results cover current local scheduler state, not necessarily remote changes.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Use task.route only after choosing a suitable local agent.
- Minimal request example: `{"project_id":"example"}`
- Live request schema: `{"properties":{"project_id":{"type":"string"}},"required":["project_id"],"type":"object"}`
- Misuse warning: Do not infer remote presence or authorization from this local list.

## `wormhole.agent.presence`

- Purpose: Update an existing locally registered agent's availability.
- Use when: When an agent starts, becomes busy, or stops accepting work.
- Do not use when: Do not use it to create an agent or assign work.
- Mutates state: true
- Required permissions: none
- Prerequisites: The agent must already be locally registered.
- Freshness implications: Presence is Gateway-local and is not durable shared task state.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Use agent.list or task.route after advertising a capability.
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

## `wormhole.agent.whoami`

- Purpose: Inspect the calling identity, capabilities, and permissions.
- Use when: At session start or before a permission-sensitive operation.
- Do not use when: Do not use it to register an agent or change permissions.
- Mutates state: false
- Required permissions: none
- Prerequisites: An authenticated Gateway session.
- Freshness implications: Reads reflect this Gateway's local replica; check sync status when remote freshness matters.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Use the reported permissions to select an allowed tool.
- Minimal request example: `{}`
- Live request schema: `{"properties":{},"required":[],"type":"object"}`
- Misuse warning: Do not treat local identity information as a substitute for checking a specific operation's result.

## `wormhole.channel.create`

- Purpose: Create a local channel and enqueue it for synchronization.
- Use when: When durable event routing needs a new named channel.
- Do not use when: Do not use it for one-off messages better represented by an existing channel.
- Mutates state: true
- Required permissions: channel.create
- Prerequisites: A bound project and channel.create permission.
- Freshness implications: The channel exists locally before it becomes shared through sync.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Post a typed event after creation.
- Minimal request example: `{"name":"example","project_id":"example"}`
- Live request schema: `{"properties":{"name":{"type":"string"},"project_id":{"type":"string"}},"required":["name","project_id"],"type":"object"}`
- Misuse warning: Do not create duplicate channels to represent the same ongoing topic.

## `wormhole.channel.events`

- Purpose: List recent durable events from local channels.
- Use when: When reconstructing recent local collaboration context.
- Do not use when: Do not use it as a live subscription or a guarantee of complete remote history.
- Mutates state: false
- Required permissions: none
- Prerequisites: A bound project.
- Freshness implications: Reads reflect this Gateway's local replica; check sync status when remote freshness matters.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Subscribe for subsequent events or inspect a referenced task or KB article.
- Minimal request example: `{"project_id":"example"}`
- Live request schema: `{"properties":{"project_id":{"type":"string"}},"required":["project_id"],"type":"object"}`
- Misuse warning: Do not treat the local event window as an audit-complete remote log.

## `wormhole.channel.list`

- Purpose: List channels in the local event-bus replica.
- Use when: When choosing an existing durable channel for a typed event.
- Do not use when: Do not use it to inspect event history.
- Mutates state: false
- Required permissions: none
- Prerequisites: A bound project.
- Freshness implications: Reads reflect this Gateway's local replica; check sync status when remote freshness matters.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Read channel.events or create a missing channel.
- Minimal request example: `{"project_id":"example"}`
- Live request schema: `{"properties":{"project_id":{"type":"string"}},"required":["project_id"],"type":"object"}`
- Misuse warning: Do not infer that a locally absent channel is absent from Fabric while offline.

## `wormhole.channel.post`

- Purpose: Publish a durable typed event locally and enqueue it for synchronization.
- Use when: When recording a handoff, discovery, decision, or progress update.
- Do not use when: Do not use it for sensitive credentials or unstructured chatter.
- Mutates state: true
- Required permissions: channel.post
- Prerequisites: A bound project, a channel ID, and channel.post permission.
- Freshness implications: The event is durable locally first; remote delivery follows synchronization.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Link the event to the relevant task or KB article in its payload or note.
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
- Freshness implications: Notifications reflect future local delivery and can be delayed by synchronization.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Keep the connection open and use channel.events for prior context.
- Minimal request example: `{"project_id":"example"}`
- Live request schema: `{"properties":{"project_id":{"type":"string"}},"required":["project_id"],"type":"object"}`
- Misuse warning: Do not assume a subscription survives reconnects or provides a complete audit stream.

## `wormhole.git.link_commit`

- Purpose: Record a metadata-only task-to-commit pointer locally and enqueue it for synchronization.
- Use when: When a verified commit materially advances or completes a tracked task and reviewers need the exact Git reference.
- Do not use when: Do not use it before the commit exists, for a pull-request review request, or to copy source into Wormhole.
- Mutates state: true
- Required permissions: git.link_commit
- Prerequisites: A bound project, existing task, repository identifier, exact commit SHA, concise summary, and git.link_commit permission.
- Freshness implications: The pointer is durable locally first and becomes visible to other Gateways after synchronization.
- Source-access implications: This tool stores only a Git pointer and summary; it never reads, mirrors, or proves repository source.
- Recommended follow-up: Verify the commit directly with Git, check sync.status, and include the pointer in the reviewer handoff.
- Minimal request example: `{"commit_sha":"example","project_id":"example","repo":"example","summary":"example","task_id":"example"}`
- Live request schema: `{"properties":{"commit_sha":{"type":"string"},"project_id":{"type":"string"},"repo":{"type":"string"},"summary":{"type":"string"},"task_id":{"type":"string"}},"required":["task_id","repo","commit_sha","summary","project_id"],"type":"object"}`
- Misuse warning: A stored pointer is not proof that the commit is correct, reachable, reviewed, or remotely synchronized.

## `wormhole.kb.get`

- Purpose: Get a named KB article or list articles when no ID is supplied.
- Use when: When reading a known durable procedure, decision, or discovery.
- Do not use when: Do not use it to retrieve code as an authoritative source.
- Mutates state: false
- Required permissions: none
- Prerequisites: A bound project; supply an article ID for a specific record.
- Freshness implications: Reads reflect this Gateway's local replica; check sync status when remote freshness matters.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Verify referenced Git pointers and update stale durable knowledge with kb.write.
- Minimal request example: `{"project_id":"example"}`
- Live request schema: `{"properties":{"article_id":{"type":"string"},"project_id":{"type":"string"}},"required":["project_id"],"type":"object"}`
- Misuse warning: Git remains code truth; do not rely on KB prose instead of current source verification.

## `wormhole.kb.list`

- Purpose: List KB articles in the local knowledge-base replica.
- Use when: When locating durable organisational context by article metadata.
- Do not use when: Do not use it when a known article ID can be fetched directly.
- Mutates state: false
- Required permissions: none
- Prerequisites: A bound project.
- Freshness implications: Reads reflect this Gateway's local replica; check sync status when remote freshness matters.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Read a selected article or write a new durable fact.
- Minimal request example: `{"project_id":"example"}`
- Live request schema: `{"properties":{"project_id":{"type":"string"}},"required":["project_id"],"type":"object"}`
- Misuse warning: Do not treat this local inventory as a substitute for a remote freshness check.

## `wormhole.kb.search`

- Purpose: Search the shared Fabric knowledge base with generation-scoped semantic ranking.
- Use when: When organisational decisions, procedures, or durable discoveries could answer the question before broad repository reconstruction.
- Do not use when: Do not use it as source-code authority or silently substitute lexical/local search when semantic ranking is unavailable.
- Mutates state: false
- Required permissions: kb.search
- Prerequisites: An online project-bound Fabric connection and kb.search permission.
- Freshness implications: Results come from Fabric's active semantic generation; provider or index degradation returns a structured error with fallback=none.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Read relevant durable context, then verify any code claim against Git and current source.
- Minimal request example: `{"project_id":"example","query":"example"}`
- Live request schema: `{"properties":{"limit":{"type":"integer"},"project_id":{"type":"string"},"query":{"type":"string"}},"required":["query","project_id"],"type":"object"}`
- Misuse warning: Never reinterpret a semantic degradation error as a successful empty result or permission to fall back silently.

## `wormhole.kb.write`

- Purpose: Write a KB article locally and enqueue it for synchronization.
- Use when: When preserving a durable fact, decision, discovery, or procedure.
- Do not use when: Do not use it for transient status chatter or source-file copies.
- Mutates state: true
- Required permissions: kb.write
- Prerequisites: A bound project and kb.write permission.
- Freshness implications: The article is durable locally before synchronization makes it shared.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Post a typed event or link the article from the relevant task.
- Minimal request example: `{"project_id":"example","title":"example"}`
- Live request schema: `{"properties":{"body":{"type":"string"},"frontmatter":{},"project_id":{"type":"string"},"title":{"type":"string"}},"required":["title","project_id"],"type":"object"}`
- Misuse warning: Do not store credentials or present Git-derived prose as authoritative code.

## `wormhole.sync.status`

- Purpose: Inspect Gateway-to-Fabric connection state and queued durable writes.
- Use when: Before relying on a remote observer seeing recent local changes.
- Do not use when: Do not use it as a Fabric health probe or to force synchronization.
- Mutates state: false
- Required permissions: none
- Prerequisites: A bound Gateway project.
- Freshness implications: Reports the current local queue and connection state; it does not refresh remote data.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Retry or defer remote-dependent work when the state needs attention.
- Minimal request example: `{"project_id":"example"}`
- Live request schema: `{"properties":{"project_id":{"type":"string"}},"required":["project_id"],"type":"object"}`
- Misuse warning: Do not assume pending_writes is zero merely because a local write succeeded.

## `wormhole.task.create`

- Purpose: Create a local task and enqueue it for synchronization.
- Use when: When intended work needs durable ownership-independent tracking.
- Do not use when: Do not use it for ephemeral discussion or an already-existing task.
- Mutates state: true
- Required permissions: task.create
- Prerequisites: A bound project and task.create permission.
- Freshness implications: The write is durable locally first and becomes shared after synchronization.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Route the new task or post a typed channel event with the resulting ID.
- Minimal request example: `{"project_id":"example","title":"example"}`
- Live request schema: `{"properties":{"description":{"type":"string"},"due_by":{"type":"string"},"parent_task_id":{"type":"string"},"priority":{"type":"integer"},"project_id":{"type":"string"},"title":{"type":"string"}},"required":["title","project_id"],"type":"object"}`
- Misuse warning: Do not claim a created task is remotely visible until sync has caught up.

## `wormhole.task.get`

- Purpose: Get one task from the local task-graph replica.
- Use when: When a task ID is known and its details are needed.
- Do not use when: Do not use it to discover tasks by broad status.
- Mutates state: false
- Required permissions: none
- Prerequisites: A bound project and task ID.
- Freshness implications: Reads reflect this Gateway's local replica; check sync status when remote freshness matters.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Use task.list for discovery or record a durable event for progress.
- Minimal request example: `{"project_id":"example","task_id":"example"}`
- Live request schema: `{"properties":{"project_id":{"type":"string"},"task_id":{"type":"string"}},"required":["task_id","project_id"],"type":"object"}`
- Misuse warning: Do not assume an absent task has never existed remotely while offline.

## `wormhole.task.list`

- Purpose: List tasks in the local task-graph replica.
- Use when: When orienting to available or status-filtered work.
- Do not use when: Do not use it when a known task ID is all that is needed.
- Mutates state: false
- Required permissions: none
- Prerequisites: A bound project.
- Freshness implications: Reads reflect this Gateway's local replica; check sync status when remote freshness matters.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Fetch a selected task or create/route an agreed item of work.
- Minimal request example: `{"project_id":"example"}`
- Live request schema: `{"properties":{"project_id":{"type":"string"},"status":{"type":"string"}},"required":["project_id"],"type":"object"}`
- Misuse warning: Do not treat a local list as proof that remote task updates have already synchronized.

## `wormhole.task.route`

- Purpose: Create a task and route it to a capable locally registered agent.
- Use when: When work should be created and assigned in one local scheduling action.
- Do not use when: Do not use it when assignment must target a remote or unregistered agent.
- Mutates state: true
- Required permissions: task.create, task.assign
- Prerequisites: A bound project, task.create and task.assign permissions, and a matching local agent capability.
- Freshness implications: Routing is local; remote observers see the task only after synchronization.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Confirm the assigned agent's local presence and post an event for material handoffs.
- Minimal request example: `{"capability":"example","project_id":"example"}`
- Live request schema: `{"properties":{"capability":{"type":"string"},"description":{"type":"string"},"project_id":{"type":"string"},"title":{"type":"string"}},"required":["capability","project_id"],"type":"object"}`
- Misuse warning: Do not assume capability matching proves workload capacity or remote availability.

## `wormhole.task.update_status`

- Purpose: Transition a task through the validated local workflow and enqueue the status update for synchronization.
- Use when: When meaningful work begins, blocks, resumes, or completes and the shared task state should reflect it.
- Do not use when: Do not use it for narration, an invalid workflow jump, or without a durable status-event channel.
- Mutates state: true
- Required permissions: task.update_status
- Prerequisites: A bound project, existing task and channel, and task.update_status permission.
- Freshness implications: The validated transition and event commit locally first; Fabric and other Gateways observe them after synchronization.
- Source-access implications: This tool does not read or return repository source.
- Recommended follow-up: Verify task.get and sync.status, then leave concise handoff context before marking work done.
- Minimal request example: `{"channel_id":"example","new_status":"todo","project_id":"example","task_id":"example"}`
- Live request schema: `{"properties":{"channel_id":{"type":"string"},"new_status":{"enum":["todo","wip","blocked","done"],"type":"string"},"project_id":{"type":"string"},"task_id":{"type":"string"}},"required":["task_id","new_status","channel_id","project_id"],"type":"object"}`
- Misuse warning: Do not report remote completion while the durable update is still pending synchronization.

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
