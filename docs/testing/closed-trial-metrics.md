# Closed-trial metrics and privacy schema

This document defines schema version 1 for the closed external alpha trial.
The implementation is in `internal/runtime/localapi/trial_metrics.go`. It is
local, opt-in tooling: it registers no MCP tool, background collector, network
client, database, or phone-home path. The `wormhole trial-metrics` command and
marshal functions run only when the trial operator explicitly asks them to
validate or format local JSON.

This schema is not trial evidence. Do not add example participant results or
the dated evidence files during the tooling step.

## Export envelope

Each participant first receives a preview with the `TrialParticipantExport`
shape: the four envelope fields below and exactly one `participant` object. A
preview has no cohort or Gate D requirement and may keep
`consent.participant_submission: false`, so the participant can review their
record before deciding whether to submit it. A submitted participant export has
the same JSON shape but must set that flag to `true`. The operator later
combines reviewed submitted records in `TrialMetricsExport`, whose final
envelope contains:

| Field | Meaning |
|---|---|
| `schema_version` | Must be `1`. |
| `export_id` | Trial-local label; never a Wormhole project or account ID. |
| `release_candidate` | Git commit of the build actually exercised. |
| `generated_at` | RFC 3339 timestamp for the local export. |
| `participants` | Participant-reviewed structured records. |
| `gate_d_decisions` | An array that must contain exactly one decision. |

Each non-withdrawn participant has a trial-local slug, external/completion
status, consent version `closed-alpha-v1`, recorded collection consent, coarse
enumerated environment categories, the measurement block, controlled
comparisons, and typed support, failure, and omission codes. Submitted and
aggregate records additionally require participant-submission consent. Query
text never appears. Optional query-category codes require both
`consent.private_query_collection: true` and a separate RFC 3339
`private_query_consent_at`. Source bodies are prohibited regardless of consent.
Consent must precede the export, separate private-query consent must fall
between general consent and export, and withdrawal/deletion timestamps must be
ordered from consent through withdrawal, deletion, and export.

`completed`, `incomplete`, and `withdrawn` are the only participant statuses.
Incomplete records may be retained as negative evidence. A withdrawal receipt
is identifier-free and contains only status plus consent-version, collection
consent, the optional unchanged participant-submission flag, and consent,
withdrawal, and deletion timestamps. It may retain
`participant_submission: false` when withdrawal precedes submission. Any
participant ID, environment, metric, comparison, observation code, or query
category makes it a privacy violation.
Neither status counts towards the cohort. A valid Gate D
export requires at least three distinct participants who are both `external`
and `completed`. Every completed participant requires at least one controlled
comparison.

## Measurement block

The measurement names and units are fixed:

| JSON field | Unit or interpretation |
|---|---|
| `installation_completed` | Boolean installation outcome. |
| `time_to_first_gateway_mcp_call_ms` | Milliseconds from installation start; `null` if not observed. |
| `time_to_productive_work_ms` | Milliseconds from enrolment to productive work; `null` if not reached or observed. |
| `tool_success_count` | Successful tool calls. |
| `tool_denial_count` | Permission/policy denials; never filter expected denials. |
| `tool_failure_count` | Unsuccessful non-denial tool calls, including tool/runtime errors and invalid results. |
| `context_retrieved_at_session_start` | Whether useful Wormhole context was retrieved; `null` if not observed. |
| `human_coaching_interventions` | Count of operator explanations or corrections. |
| `model_handoff_succeeded` | Whether the planned handoff met its success criteria; `null` if not attempted. |
| `sync_recovery_succeeded` | Whether the outage/restart exercise recovered; `null` if not attempted. |
| `kb_relevant_results` | Results judged relevant. |
| `kb_results_considered` | Total results considered for relevance. |
| `duplicate_or_low_value_kb_contributions` | Contributions later judged duplicate or low value. |
| `code_graph_useful_queries` | Queries judged useful under the predeclared criterion. |
| `code_graph_queries` | All Code Graph queries, including failed, incomplete, and useless calls. |
| `files_read_before_correct_edit` | File count before the first correct edit; `null` if no correct edit or unavailable. |
| `source_bytes_read_before_correct_edit` | Byte count only, never bytes/content themselves; `null` if unavailable. |
| `event_count` | Events observed in the trial window. |
| `event_noise_count` | Events judged redundant, irrelevant, or excessive. |
| `task_state_accurate` | Whether recorded Task state matched observed work; `null` if not assessed. |
| `context_reconstructions_avoided` | Explicit instances where retained context avoided repeated reconstruction. |
| `tokens_before_productive_work` | Harness-reported tokens before productive work; `null` when unavailable. |

Every count is nullable. Counts cannot be negative, and
relevant/useful/noise counts cannot exceed their corresponding totals. Every
missing measurement—including a null count—requires the exact whitelisted JSON
field name under `metrics.omissions`. An omission code alongside a non-null
value is also rejected. Never infer or backfill a missing value. Comparison-arm
counts follow the same value-or-exact-omission rule.

Rates are derived during analysis:

```text
tool attempt count = tool_success_count + tool_denial_count + tool_failure_count
tool success rate = tool_success_count / tool attempt count
tool denial rate = tool_denial_count / tool attempt count
KB relevance rate = kb_relevant_results / kb_results_considered
Code Graph useful-query rate = code_graph_useful_queries / code_graph_queries
Event noise rate = event_noise_count / event_count
```

If a denominator is zero, report the rate as missing, not zero or 100%.

## Controlled comparison

For at least one representative Task per completed participant, choose
`guidance_off` or `code_graph_off` as `baseline_kind`. The baseline and alpha
arms must use the same checkout revision, permissions, success criteria, and
measurement method; all four matching-control fields must be `true`.

Both arms record:

- tool selection;
- operating-loop adherence;
- useful shared-state write count;
- human-correction count;
- Task quality;
- unnecessary tool-call count;
- source files and byte counts; and
- review quality.

`task_kind` is one of `feature`, `bugfix`, `review`, `refactor`,
`documentation`, or `other`; there is no task-label field. Tool selection,
operating-loop adherence, Task quality, and review quality are closed enums,
not prose. Do not change the predeclared rubric after seeing the first arm.

## Gate D decision

`gate_d_decisions` must contain exactly one object and its `decision` must be
one of:

```text
continue towards beta planning
continue with narrowed scope
repeat alpha after corrective work
stop the current direction
```

The evaluation records `supports`, `contrary`, or `missing` for each Gate D
criterion: manual context relay, repeated project reconstruction, cross-model
continuation, interruption recovery, managed-guidance learnability, source
discovery narrowing, proportionate maintenance, proportionate Event noise, and
appropriate confidence. Both `supporting_evidence` and `contrary_evidence`
must be non-empty arrays of those criterion codes, and each code must match its
`supports` or `contrary` rating. The JSON contains no evidence prose.

## Privacy boundary

The schema has no arbitrary participant, comparison, or Gate D observation
text. Environment, support, failure, procedure omission, query category,
comparison assessment, measurement omission, and Gate D evidence are closed
code sets; set-like code arrays reject duplicate values. The schema has no
fields for source bodies, credentials, repository
content, or Wormhole project/agent/Passport identifiers. The decoder rejects
unknown or duplicate JSON keys and classifies excluded keys as privacy
violations. It also rejects Bearer, Basic, GitHub/GitLab PAT, and `sk-`-shaped
credential values before typed decoding.

Private query text is never exported. Optional query-category codes have a
separate per-participant consent switch and timestamp. General trial consent,
Passport possession, or use of Code Graph is not private-query consent.

Input and formatted-output JSON are each limited to 1 MiB, with at most 64
nested containers. Participants and every array are capped at 100 items, and
every key or string at 256 bytes before repeated parsing. Export and participant
IDs are bounded lowercase trial-local slugs; canonical UUID-shaped values are
rejected so a Wormhole project, agent, Passport, or account identifier cannot
be mistaken for a local label. `release_candidate` is a full 40-character
lowercase Git commit. The participant reviews the complete local export before
submission, and a second operator reviews it before publication.

## Local API and validation

The local instrumentation surface is:

```go
data, err := localapi.MarshalTrialMetricsExport(export)
decoded, err := localapi.DecodeTrialMetricsExport(data)
err := localapi.ValidateTrialMetricsExport(export)

participantData, err := localapi.MarshalTrialParticipantExport(participantExport)
participant, err := localapi.DecodeTrialParticipantExport(participantData)
err := localapi.ValidateTrialParticipantExport(participantExport)

previewData, err := localapi.MarshalTrialParticipantPreview(participantExport)
preview, err := localapi.DecodeTrialParticipantPreview(previewData)
err := localapi.ValidateTrialParticipantPreview(participantExport)
```

All marshal functions validate and return indented JSON bytes. They do not
write a file or send data. Preview validation permits a non-withdrawn record to
keep `participant_submission: false`; submitted-participant and aggregate
validation require it to be `true`. An identifier-free withdrawal receipt is
valid in either state. All decoders reject malformed or trailing JSON,
duplicate/unknown fields, size-limit violations, privacy exclusions, invalid
codes, and invalid or unaccounted-for measurements. The aggregate
decoder additionally rejects insufficient completed external participants,
missing comparisons, and an invalid Gate D selection. The operator chooses the
local file and submission channel.

The shipped CLI exposes those same validators without network access. It reads
one file operand, `-`, or stdin by default. `validate` prints only `valid` after
success; `format` emits indented JSON only after strict validation succeeds.
Exit status is `0` for valid input and successful output, `1` for input,
validation, privacy, or output failures, and `2` for command/flag/operand usage
errors. Validation errors are content-free classifications; they never echo
participant JSON or an unknown JSON key.

```bash
wormhole trial-metrics validate --kind participant-preview [FILE|-]
wormhole trial-metrics format --kind participant-preview [FILE|-]
wormhole trial-metrics validate --kind participant [FILE|-]
wormhole trial-metrics format --kind aggregate [FILE|-]
```

Run the schema and privacy tests from the repository root:

```bash
go test ./internal/runtime/localapi -run 'TrialMetrics|TrialPrivacy' -count=1
```

Before the later evidence commit, run `wormhole trial-metrics validate --kind
participant` over each exact participant-submitted file and its redacted copy,
then run it with `--kind aggregate` over the redacted aggregate. Run the focused
tests,
`git diff --check`, and the repository checks required by
[`docs/testing/alpha-validation.md`](alpha-validation.md). Do not create
`docs/testing/results/closed-alpha-trial-2026-07.json` or its Markdown report
until the real controlled trial and comparative evaluation are complete.
