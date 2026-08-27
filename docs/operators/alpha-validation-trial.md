# Closed alpha validation trial

This runbook is for the controlled external trial that follows Gate C. It
prepares and operates the trial; it is not evidence that the trial happened.
Do not create the dated result files until real participants have completed the
procedure against the release candidate containing this tooling.

## Entry gate and roles

Begin only after the Gate C automated and manual loops in
[`docs/testing/alpha-validation.md`](../testing/alpha-validation.md) pass and a
releasable alpha build is identified. Record its Git commit as the
`release_candidate` in every submitted export. Do not collect trial evidence
against an earlier build.

Recruit at least three technically capable external participants who already
use coding agents. Prefer people who use multiple models or harnesses, work in
non-trivial repositories, or regularly reconstruct context across sessions.
Invitations and incomplete attempts do not satisfy the cohort requirement:
three external participants must complete the trial and one controlled
comparison each.

Assign these roles:

- the participant installs and operates Wormhole, decides what to submit, and
  may withdraw at any time;
- the operator observes the procedure, records timing and count measurements,
  provides support, and performs redaction; and
- a second operator reviews redaction and the Gate D report before it enters
  the repository.

Use a lowercase trial-local slug such as `participant-a`. Never use a Wormhole
project, agent, Passport, account, repository, or employer identifier as the
participant ID, and do not use a UUID-shaped value for either the participant
ID or export label.

## Consent and data handling

Obtain recorded consent before installation or observation. Use consent
version `closed-alpha-v1`, explain the following in plain language, and record
the version and timestamp:

- participation is voluntary and may stop without penalty;
- the measurements are the fields in
  [`docs/testing/closed-trial-metrics.md`](../testing/closed-trial-metrics.md),
  plus environment categories, support interventions, failures, and
  omissions;
- the export is produced locally, is shown to the participant, and is sent
  only after the participant explicitly submits it;
- Wormhole does not upload trial metrics automatically and the operator must
  not add phone-home collection;
- private-query categorisation is off by default and requires a separate
  affirmative consent and timestamp; declining it does not prevent
  participation, and query text itself never enters the structured export;
- source bodies, bearer tokens or authorization headers, unrelated repository
  content, and cross-project identifiers are never collected;
- the participant may skip a measurement; it remains `null` and its exact
  whitelisted measurement name is recorded under `omissions` rather than
  guessed; and
- withdrawal causes the participant-level raw export and working copies to be
  deleted within seven calendar days, followed by written confirmation.

Generate an export only after its recorded consent events. If a participant
withdraws, record withdrawal before deletion and generate the unlinked receipt
only after deletion completes; the validator rejects timestamps outside that
order. Record each environment or observation category once rather than
duplicating a code to imply frequency.

The participant keeps the first export in a path they choose. After explicit
submission, store the raw export only in an access-restricted, encrypted trial
workspace available to the named operators. Do not store raw exports in Git,
Gateway SQLite, Fabric, issue trackers, chat, or shared support logs. Delete raw
exports and operator working copies no later than 30 calendar days after the
Gate D decision. The repository may retain only the reviewed, redacted result
and report described by the implementation plan.

Before submission and again before repository publication, remove:

- source or generated-code bodies and source excerpts;
- bearer credentials, authorization headers, credential paths, or environment
  dumps;
- all query text; separate consent permits only a query-category code;
- repository names, remotes, file paths, issue text, and unrelated repository
  material; and
- Wormhole project, agent, Passport, Task, KB, Channel, Event, and other
  durable identifiers when they are not essential to aggregate analysis.

Counts and byte totals are permitted; the content counted is not. The JSON
contains only documented environment, support, failure, omission, comparison,
query-category, and Gate D evidence codes. It has no arbitrary observation or
task-label fields. Run the privacy-schema validator after redaction. Treat a
rejected export as a redaction failure, not a reason to weaken the validator.

## Participant procedure

Run each participant separately. Keep a timestamped operator worksheet outside
the repository and preserve successful calls, permission/policy denials,
non-denial tool failures, false leads, support, and missing measurements. Count
each attempted tool call in exactly one of success, denial, or failure.

### 1. Install and enrol

1. Give the participant the release-candidate build and its commit. Have them
   follow the [README quickstart](../../README.md#quickstart) from a supported
   Linux or WSL environment.
2. Before handing over the checkout, have the operator provision any approved
   Fabric project membership and credential profile through the Gateway-owned
   enrolment control path. There is no public CLI enrolment command. Do not copy
   credentials into the worksheet.
3. From the participant's checkout, run the canonical resumable setup and
   review its complete plan before approval:

   ```bash
   wormhole setup --publication private_git
   ```

   Use `local_only` for an isolated single-device trial or `public_git` only
   when the participant has explicitly approved tracked-state publication.
   Setup ensures the owner-only Gateway service, binds the checkout, selects
   the local identity, imports the accepted portable Git base, records the
   publication policy, and proposes detected connector changes.
4. Inspect the participant's harness connector and install it explicitly if
   setup did not already do so:

   ```bash
   wormhole connector list <codex|claude>
   wormhole connector install --yes <codex|claude>
   ```

5. Record installation completion and elapsed time to the first successful
   Gateway MCP call. Confirm the harness launches the local `wormhole mcp`
   bridge and never calls Fabric directly.
6. Run `wormhole status`. Ask the harness to list tools, compare the result with
   the exact 27-tool Gateway inventory in
   [`docs/testing/alpha-validation.md`](../testing/alpha-validation.md), and
   call `wormhole.workspace.status`. Fabric's distinct server registry has
   exactly 20 tools and no `wormhole.agent.register`; it is not a harness
   endpoint. Record success or denial without recording private arguments.

Escalate unsupported platforms, malformed enrolment, profile permission
problems, and inability to reach the local socket. Never ask the participant to
paste a credential file or authorization header.

### 2. Review and approve managed guidance

1. Run `wormhole integration preview --project <project-uuid>` and let the
   participant inspect the complete proposed diff and expected digest.
2. Let the participant approve, postpone, or reject. If approved, run
   `wormhole integration apply --project <project-uuid>` interactively, or use
   `--confirm-digest <full-digest>` only after the participant has verified it.
3. Run `wormhole integration status --project <project-uuid>` and record only
   the corresponding documented procedure/failure code. Do not copy repository
   guidance bodies into trial data.

Record coaching needed to understand or use the managed guidance. Operator
coaching is evidence and must not be silently omitted.

### 3. Exercise the portable workspace loop

Use the same checkout binding for both the CLI and harness. Start with
`wormhole status` and `wormhole.workspace.status`, then perform the predeclared
non-sensitive benchmark mutation. If the participant directly edits the
tracked `.wormhole/state/v1/` tree, run `wormhole import` before continuing.
Review the candidate and its semantic digest with:

```bash
wormhole diff
```

For `local_only` or `private_git`, checkpoint the reviewed candidate with
`wormhole checkpoint`. For `public_git`, acknowledge only the exact current
digest printed by `diff`:

```bash
wormhole checkpoint --publication-review-digest sha256:<review-digest>
```

Checkpoint may materialise `.wormhole/state/v1/` in the working tree, but
Wormhole must not stage, commit, or push it. Record Git HEAD, remotes, and index
before and after the operation. Only the participant may accept the candidate
with ordinary Git commands.

When the procedure deliberately tests pausing unpublished work, use a unique
UUID and an explicit label, then confirm the result through status:

```bash
wormhole stash --request-id <uuid> --label "trial pause"
wormhole status
```

The harness equivalents are
`wormhole.workspace.{status,diff,import,checkpoint,stash}` and must return the
same semantic results. Gateway resolves the workspace binding and actor; never
put either into trial-authored MCP arguments.

### 4. Run the benchmark Task and comparison

Choose one representative coding Task before either arm starts. Record only
its whitelisted task kind (`feature`, `bugfix`, `review`, `refactor`,
`documentation`, or `other`), then freeze its
checkout revision, permissions, success criteria, and measurement method. Use
managed guidance off as the baseline and the participant-approved guidance as
the alpha arm. The removed structural-discovery surface is not a current
comparison arm.

Use the same Task in both arms. Counterbalance arm order across participants
when practical and record the order in the operator worksheet. Measure tool
selection, operating-loop adherence, useful shared-state writes, human
correction, Task quality, unnecessary tool volume, source-discovery breadth,
and review quality. Capture successes, denials, and non-denial failures as
separate counts so the tool-success denominator includes all attempts. Also
capture incomplete or useless queries, false leads, and missing values. Do not
restart or discard an arm to improve the outcome.

During the alpha arm, observe context retrieval at session start, time from
enrolment to productive work, KB relevance, low-value KB writes, Event noise,
Task-state accuracy, model handoff, context reconstruction avoided, and token
use before productive work. Use tool or harness counters where available;
otherwise record the measurement as missing with the method limitation.

### 5. Exercise interruption and recovery

1. Confirm the local replica is current and record the pending-write count.
2. Stop or isolate Fabric using the trial environment's approved outage
   mechanism. Do not change Gateway credentials or corrupt local files.
3. Perform the preselected local read/write step, restart `gatewayd` once while
   offline, and record local availability and queued state.
4. Restore Fabric, wait for normal synchronization, and verify that the pending
   count drains without duplicate durable state.
5. Record sync recovery, model handoff after interruption, support required,
   every failure, and every omitted check.

If the exercise risks real repository work or shared production data, stop and
rerun in an isolated trial project. Do not improvise destructive recovery.

### 6. Support and escalation

Classify support using only `installation`, `enrolment`, `permissions`,
`guidance`, `sync_outage`, `task_workflow`, `measurement`, or `privacy` for a
current Stage 2 run. The metrics schema retains an older structural-discovery
category solely so dated July evidence remains readable; do not select it for
this procedure. Record failures and procedure omissions using the code lists
in the metrics schema, without source, query, credential, or project content.

Stop the affected step and escalate to the trial lead for credential exposure,
suspected cross-project data, data-loss risk, repeated sync state, validator
failure, or a participant withdrawal. Product defects remain failures in the
record; do not coach around them and report the run as unassisted.

### 7. Export, review, and submission

Populate `localapi.TrialParticipantExport` from this participant's worksheet
with `consent.participant_submission: false`. Use
`localapi.MarshalTrialParticipantPreview` to validate and generate the
pre-submission preview. For an operator-runnable path, prepare the same strict
JSON shape locally and run:

```bash
umask 077
wormhole trial-metrics format --kind participant-preview draft.json > participant-preview.json
wormhole trial-metrics validate --kind participant-preview participant-preview.json
```

The API and CLI perform no network I/O. The individual preview contains no
other participants and no Gate D decision. `validate` prints only `valid` on
success, and `format` writes no participant JSON when validation fails.

The participant must inspect the entire export, confirm the consent version,
timestamps, and flags, and
choose whether to submit it. Submission must be an affirmative action through
the agreed restricted channel. Silence, continued product use, or an existing
Wormhole Passport is not trial consent. Only after that affirmative choice, set
`consent.participant_submission: true`, then format and validate the exact file
as a submitted export:

```bash
wormhole trial-metrics format --kind participant participant-approved.json > participant-submitted.json
wormhole trial-metrics validate --kind participant participant-submitted.json
```

The receiving operator runs `wormhole trial-metrics validate --kind
participant` over the exact submitted bytes, redacts them, and validates the
redacted bytes again. After the real cohort completes, combine only the
reviewed participant records into `localapi.TrialMetricsExport`, add the
evidence-based Gate D choice, and run:

```bash
wormhole trial-metrics format --kind aggregate aggregate-draft.json > aggregate.json
wormhole trial-metrics validate --kind aggregate aggregate.json
```

Do not copy raw exports into the repository. Real redacted evidence is created
only in the later trial-evidence step.

## Withdrawal and deletion

On withdrawal, stop collection immediately, acknowledge the request, and:

1. delete the participant's local operator worksheet, submitted export,
   backups, derived rows, and support attachments from the operator workspace;
2. ask the participant whether they want help deleting their local copy;
3. remove participant-level material from any draft aggregate and recompute it;
4. record deletion completion outside the deleted dataset and send written
   confirmation within seven calendar days; and
5. retain no link between the trial-local pseudonym and the person.

A withdrawal cannot be counted as one of the three completed external
participants. A retained withdrawal receipt must omit the participant ID,
external flag, environment, metrics, comparisons, support, failures, omissions,
and query categories. It may contain only status plus versioned collection
consent, the unchanged participant-submission flag, and consent, withdrawal,
and deletion timestamps. The flag may remain `false` when withdrawal occurs
before submission. The validator rejects a linked or full withdrawal record as
a privacy violation. Otherwise retain only an unlinked aggregate withdrawal
count.

## Gate D report checklist

After at least three qualifying completions and comparisons, the redacted
report must include supporting, contrary, negative, incomplete, and missing
evidence. The JSON records only Gate D criterion codes; the separately reviewed
Markdown report carries redacted analysis. Evaluate manual context relay, repeated reconstruction, cross-model
continuation, interruption recovery, managed-guidance learnability, source
discovery, maintenance cost, Event noise, and incorrect confidence.

Record exactly one of:

```text
continue towards beta planning
continue with narrowed scope
repeat alpha after corrective work
stop the current direction
```

If evidence supports only one component, choose narrowed scope rather than
preserving the complete platform by assumption. A continuation decision does
not itself authorise beta planning or create a compatibility promise.
