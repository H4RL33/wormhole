# Closed alpha validation trial

This runbook applies the local-only Stage 2 acceptance boundary to a controlled
external trial. It prepares a trial; it is not evidence that one happened. Do
not create dated results until participants complete the procedure against one
recorded release-candidate commit.

## Entry gate and roles

Begin only after the automated and manual gates in
[`docs/testing/alpha-validation.md`](../testing/alpha-validation.md) pass. The
hermetic process gate must have exercised real `gatewayd` and `wormhole mcp`
processes, the exact 17-tool Gateway surface, restart, ordinary Git acceptance,
and a second fresh clone with fresh private state.

Recruit at least three external participants who use coding agents. Each must
complete the same predeclared local workflow and comparison. Record a
lowercase trial-local slug such as `participant-a`; never use a repository,
project, agent, account, employer, or UUID-shaped identifier.

The participant operates Wormhole and decides what to submit. One operator
records observations and performs redaction; a second operator reviews the
redacted Gate D evidence.

## Consent and data handling

Obtain recorded consent using `closed-alpha-v1` before installation or
observation. Explain that participation is voluntary, metrics are local and
explicitly submitted, omitted values remain omitted, and withdrawal removes
participant-level material within seven calendar days.

Wormhole must not upload trial metrics automatically. Raw exports stay in an
access-restricted encrypted operator workspace, never Git, Gateway SQLite,
issue trackers, chat, or support logs, and are deleted no later than 30 days
after the Gate D decision. Before submission and repository publication remove:

- source bodies, generated code, source excerpts, repository names, remotes,
  paths, and issue text;
- credentials, authorization headers, environment dumps, and credential paths;
- query text and durable project, human, agent, session, Channel, KB, activity,
  workspace, or receipt identifiers; and
- any arbitrary observation text outside the documented schema.

Counts and byte totals are permitted; the content counted is not. A separate
affirmative consent may permit a query-category code, never query text. Validate
the redacted export with the trial-metrics privacy schema; do not weaken the
validator to admit a rejected export.

## Participant procedure

Run each participant separately and keep the operator worksheet outside the
repository. Count each attempted tool call in exactly one of success, policy
denial, or non-denial failure.

### 1. Install and set up the local runtime

1. Give the participant the release-candidate binaries and commit. Use a
   supported Linux or WSL environment and an isolated Git repository.
2. Run `wormhole setup --publication local_only`. Review the complete confirmed
   plan before approval. Setup must register the workspace, select the local
   human, import the Git base, and configure the owner-private service without
   a test-only bypass.
3. Install one first-party connector if needed with
   `wormhole connector install --yes <codex|claude>`.
4. Confirm the harness launches the real `wormhole mcp` stdio bridge and reaches
   the Gateway Unix socket. List tools and compare them with the exact 17-tool
   Gateway inventory in the automated validation guide.

This Stage 2 trial does not exercise Fabric or PostgreSQL. It does not provision
profiles, enrol remote identities, approve managed guidance, simulate a sync
outage, or claim live Task, semantic search, Git-link, or guidance tools. The
optional Fabric server's exact 16-tool live private registry is tested
separately. Its ten public sync-v2 and Activity-v1 contracts are descriptor-only
and non-callable in Slice 1; neither surface is a participant harness endpoint.

### 2. Exercise portable records and private operations

From both CLI and MCP, check workspace status. Use the Gateway tools to register
or observe the selected agent/session, create one portable Channel, post one
portable KB record, and add operational Channel activity. Record only outcome
codes and timings, not content or identifiers.

Restart `gatewayd` and `wormhole mcp`. The selected private identity, candidate,
portable records, and operational activity must still be available in that
checkout. Presence may be ephemeral; it is never portable evidence.

### 3. Review, checkpoint, and accept through Git

Perform one predeclared non-sensitive mutation. If the tracked portable tree was
edited directly, run `wormhole import`, then inspect `wormhole diff`. Run
`wormhole checkpoint` and prove that Git HEAD, index, and remote did not change.
The checkpoint only materialises an uncommitted `.wormhole/state/v1/`
candidate.

The participant reviews and accepts with ordinary Git add/commit/push. Git is
the sole acceptance authority; no SQLite flag, receipt, operational event, or
Fabric response accepts portable state.

### 4. Verify the second clone boundary

Create a second fresh clone of the accepted commit with a new HOME/XDG data
root, socket, SQLite database, setup-selected human, agent/session, and workspace
binding. Run setup and the real stdio bridge again.

The accepted portable digest, Channel, KB record, and portable actor record must
match Git. The first clone's activity, presence, overlays, stashes, receipts,
workspace identity, selected private identity, credentials, legacy rows, and
sync rows must be absent. The clone must not contact Fabric.

### 5. Controlled comparison

Choose one representative coding task before either arm starts. Freeze its
checkout revision, permissions, success criteria, and measurement method. Use a
normal single-session baseline and a Stage 2 arm that uses the portable
Channel/KB context. Do not invent Task records or managed guidance as trial
features.

Counterbalance arm order when practical. Measure context-reconstruction time,
useful state writes, human correction, output quality, unnecessary tool volume,
false leads, restart recovery, and missing values. Do not restart or discard an
arm to improve the result.

### 6. Support and escalation

Use only documented support categories: `installation`, `service`, `socket`,
`workspace`, `publication`, `git_acceptance`, `restart`, `clone_boundary`,
`measurement`, or `privacy`. Record unsupported platforms, setup-plan failures,
owner-permission failures, socket failures, inventory drift, unexpected private
state portability, and any accidental network dependency as failures. Never ask
the participant to paste a credential or private database.

## Withdrawal

On withdrawal, delete the raw export, redacted draft, backups, derived rows, and
support attachments; remove the participant from aggregates; and send written
confirmation within seven calendar days. Retain at most an unlinked aggregate
withdrawal count. A withdrawal is not one of the three completed participants.

## Gate D report

After at least three qualifying completions, publish only reviewed, redacted
aggregate evidence. Include supporting, contrary, negative, incomplete, and
missing results for setup, exact tool discovery, portable-state usefulness,
Git acceptance, restart, fresh-clone equivalence, privacy, learnability,
failures, and support cost.

Record exactly one decision:

```text
continue towards beta planning
continue with narrowed scope
repeat alpha after corrective work
stop the current direction
```

A continuation decision does not authorise beta scope or create a compatibility
promise.
