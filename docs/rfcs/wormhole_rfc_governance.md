# RFC-0002: Wormhole Governance

**Constitution & Congress — organisational policy and debate, as a product on top of Wormhole Core.**

| | |
|---|---|
| Status | Draft |
| Author | Harley |
| Date | 2026-07-07 |
| Revised | 2026-08-01 |
| Depends on | [RFC-0001: Wormhole Core](wormhole_rfc.md) — event bus, task graph, KB, identity |
| Relationship to Core | Additive, optional. Core ships and is useful with zero governance adoption. |

---

## 1. Abstract

RFC-0001 specifies Wormhole Core: a shared event bus, task graph, and knowledge graph that give AI agents persistent, model-agnostic organisational memory. Core deliberately keeps permissions static and human-authored — policy is configured once, not evolved.

This RFC specifies what happens once that assumption stops being enough: an organisation running many agents across many projects wants its own operating procedure to evolve the way its knowledge already does — proposed, debated, versioned, and enforced, rather than hand-edited by a human every time.

Governance is split out from Core into its own RFC deliberately. It is not a deferred phase of Core — it's a separate, independently adoptable product: a deployment can run Wormhole Core forever without ever adopting Constitution or Congress, and a team that does adopt it is buying a distinct thing (an organisational policy engine and a deliberation surface), not a Core version bump.

## 2. Motivation

Static permissions (RFC-0001 §8.4) work for a small MVP deployment but break down at scale in predictable ways:

- **Policy drift.** As a project grows, ad hoc exceptions accumulate ("agent X is allowed to skip review because...") with no record of why, no versioning, and no way for a new agent joining the project (RFC-0001 §8.5) to learn the exception exists.
- **No feedback loop.** Agents doing the work daily often notice procedural friction before humans do (a KB category consistently missing a needed link field, a task-status vocabulary that doesn't fit a workflow) but have no structured way to propose a fix — only to work around it silently, which is itself a form of undocumented policy drift.
- **No record of dissent.** When a policy change is contentious, "human decided, agents complied" loses the substance of *why* an agent's approach differed and whether that reasoning still applies to the next similar case.

Governance solves this by making organisational policy a first-class, versioned object (**Constitution**) with a structured proposal-and-debate process (**Congress**) governing how it changes.

## 3. Goals and Non-Goals

### 3.1 Goals

- G1: Give organisational policy the same properties Core already gives knowledge — versioned, structured, queryable, not a scattered set of tribal exceptions.
- G2: Let agents propose procedural changes through the same task-graph primitives they already use for work, not a separate bolted-on mechanism.
- G3: Preserve a record of *why* a policy exists or changed, including dissenting positions, not just its current state.
- G4: Keep humans as the final approval authority for any change that takes effect — no autonomous self-amendment without human sign-off, at least in this RFC's scope.
- G5: Make adoption strictly optional and additive — a Core deployment must be fully functional with zero governance features enabled.

### 3.2 Non-Goals

- NG1: Fully autonomous self-amending policy with no human approval step. Deliberately out of scope — the risk of an agent population converging on policy that serves throughput over correctness (or safety) is not one this RFC tries to solve; it keeps a human veto in the loop.
- NG2: General-purpose organisational decision-making beyond Wormhole-governed procedure (i.e., this is not a company-wide voting/OKR tool).
- NG3: Real-time synchronous debate. Congress (§6) is turn-based and asynchronous, matching how agents actually operate — not a chat-style live meeting.

## 4. Relationship to Core

| | Core (RFC-0001) | Governance (this RFC) |
|---|---|---|
| Ships | Always | Optionally, per deployment |
| Policy model | Static, human-edited | Versioned, proposal-driven |
| Change process | Human edits config directly | Propose → debate (Congress) → human approval → Git acceptance → active Constitution version |
| Enforcement | None beyond static permission checks | Platform checks actions against current Constitution version |
| Storage | Uses Core's task graph + event bus as substrate | Existing portable tasks/KB/events plus operational debate activity; no new storage engine |

Governance introduces no new architectural layer — it is a set of conventions and
enforcement rules built from Core's task graph, operational event stream, and curated KB
representation (RFC-0001 §7, §8.1–8.2). A proposal is a task of a reserved type; a debate
turn is operational activity on that task's thread; human approval authorises a portable
Constitution proposal. Before approval, every bounded Congress turn—including dissent—is
explicitly promoted into portable `EventV1` evidence. The human decision creates one
canonical portable adoption envelope binding the exact proposal, Constitution, complete
ordered debate evidence, decision attribution, and realm. Adoption occurs only after
the realm-authorised observer verifies an accepted Git commit containing that exact
envelope and its bound content. This keeps governance thin and auditable without making
an overlay, Fabric acknowledgement, or checkpoint an activation authority.

## 5. Constitution

A versioned, append-only document governing agent permissions and standard operating procedure for a project.

### 5.1 Properties

- **Versioned.** Every adopted change produces a new version; prior versions remain readable (an agent can always answer "what was policy when this decision was made").
- **Append-only.** No silent edits to history — corrections are new versions, not retroactive rewrites.
- **Enforced.** Once an accepted Git observation activates it, the platform checks
  relevant actions against the current version, not merely a static permission table.
- **KB-resident.** Stored as a specially-tagged sequence of KB articles (RFC-0001 §8.3), linked to the proposal task and debate thread that produced each version — reusing Core's existing storage rather than inventing a parallel one.
- **Digest-bound.** An approved version has one canonical portable adoption envelope.
  Its preimage binds the exact realm `(project_id, canonical_repository_identity,
  acceptance_ref)`; proposal task ID and canonical digest; Constitution article ID,
  version, and canonical content digest; the complete ordered list of promoted debate
  event IDs/digests; and the deciding human's strict actor projection, decision time, and
  approval outcome. The human signs that exact preimage with the accepted realm's
  Ed25519 decision key. The
  envelope digest and decision evidence travel in Git, so a second clone can reconstruct
  and verify the same approved candidate without machine-private approval state.

### 5.1.1 Portable governance extensions and codec

Governance V1 uses two strict Core extension records; it adds no unlisted path to the
version-one snapshot layout:

- `state/v1/project.json` may contain `dev.wormhole.governance.realm` schema version 1.
  Its data is exactly `acceptance_ref` plus `decision_keys`, a non-empty array of exact
  `{actor_id,key_id}` objects sorted bytewise by actor ID then key ID. Unknown fields,
  duplicates, noncanonical refs/IDs, non-human actors, non-Ed25519 keys, and keys absent
  from the referenced portable `ActorV1.PublicKeys` reject the snapshot. This extension
  must already be present in an accepted base before it can authorize a decision; the
  initial Git acceptance of the realm extension is the explicit governance bootstrap.
- The candidate Constitution's existing
  `state/v1/kb/<constitution-article-id>/record.json` contains exactly one
  `dev.wormhole.governance.adoption` extension at schema version 1. Its data is the
  closed object below. No other KB article may claim the same proposal/version/envelope
  tuple.

```json
{
  "schema_version": 1,
  "realm": {
    "project_id": "<canonical UUID>",
    "repository": {
      "provider": "<normalized provider>",
      "immutable_id": "<provider immutable ID>",
      "canonical_remote": "<normalized credential-free remote>"
    },
    "acceptance_ref": "refs/heads/<refname>",
    "realm_base_commit_sha": "<40-or-64 lowercase hex>",
    "realm_extension_digest": "sha256:<64 lowercase hex>"
  },
  "proposal_task_id": "<canonical UUID>",
  "proposal_digest": "sha256:<64 lowercase hex>",
  "constitution_article_id": "<canonical UUID>",
  "constitution_version": 14,
  "constitution_record_digest": "sha256:<64 lowercase hex>",
  "constitution_body_digest": "sha256:<64 lowercase hex>",
  "debate_events": [
    {"event_id": "<canonical UUID>", "event_digest": "sha256:<64 lowercase hex>"}
  ],
  "decision_actor": {
    "actor_kind": "human",
    "human_principal_id": "<canonical UUID>",
    "assurance": "local|public-key-continuity|private-authenticated",
    "occurred_at": "<canonical UTC RFC3339Nano>"
  },
  "decided_at": "<same canonical UTC RFC3339Nano>",
  "outcome": "approved",
  "decision_key_id": "sha256:<64 lowercase hex>",
  "signature_algorithm": "ed25519",
  "envelope_digest": "sha256:<64 lowercase hex>",
  "signature": "<unpadded RawURL-base64 64-byte signature>"
}
```

The unsigned preimage is the closed object above without `envelope_digest` and
`signature`. Encode it with Core's strict canonical JSON, then compute
`envelope_digest = sha256("wormhole-governance-adoption-v1\n" || canonical_json)`.
The Ed25519 signature is over the 32 raw digest bytes. `decision_key_id` must name the
same human actor in the already accepted realm extension and resolve to that actor's
portable Ed25519 public key. Strict RawURL decoding rejects padding, wrong lengths, and
noncanonical encodings.

`decision_actor` is the strict portable projection of a validated human `ActorEnvelope`:
only actor kind, human principal ID, assurance, and occurrence time are present; agent,
accountable-human, session, harness, and model fields must be empty and absent.

`realm_extension_digest` is
`sha256("wormhole-governance-realm-v1\n" || canonical_realm_extension_data)` over the
exact closed `dev.wormhole.governance.realm` data at `realm_base_commit_sha`. That commit
must have been independently observed as accepted on the same realm repository/ref before
`decided_at`, must be a strict ancestor of the candidate adoption commit, and must already
contain the deciding key. The candidate must retain the exact same realm extension digest.
Thus a decision key introduced alongside its adoption envelope cannot authorize itself;
an independent clone can locate and verify the exact prior authorizing revision.

`proposal_digest` is Core's canonical JSON digest of the exact `TaskV1` file.
`constitution_record_digest` is
`sha256("wormhole-governance-constitution-record-v1\n" || canonical_record_json)` over
the exact `KBArticleV1` record with only `dev.wormhole.governance.adoption` removed,
avoiding a digest cycle while binding every other record field and extension. The
positive `constitution_version` must equal the exact integer stored in reserved
frontmatter key `wormhole.constitution_version`.
`constitution_body_digest` is Core's canonical Markdown digest of `body.md`. Debate
entries retain bounded Congress turn order, must be non-empty and unique, and digest the
exact promoted `EventV1` files. The article ID/path, project/config repository identity,
accepted realm extension/ref, proposal, article version/body/record, every event,
realm base commit/ancestry, `decided_at == decision_actor.occurred_at`, key, envelope
digest, and signature must all
exact-match. Unknown fields, trailing/noncanonical JSON, a changed byte after approval,
or machine-private evidence cannot validate or activate the envelope.

### 5.2 Lifecycle

1. **Draft.** An agent or human opens a `governance.proposal` task (a reserved task type, RFC-0001 §8.2) describing the change and rationale.
2. **Debate.** The proposal enters Congress (§6) for a bounded number of turns.
3. **Decision.** A human with authority over the project approves, rejects, or requests
   revision. Approval first promotes every bounded debate turn into portable evidence,
   then atomically produces the exact portable Constitution proposal and digest-bound
   adoption envelope above in `approved_pending_acceptance`. Any missing turn, digest
   mismatch, or post-decision proposal/Constitution edit invalidates the envelope and
   requires a new human decision. Approval does not make the version active.
4. **Acceptance.** The proposal follows the repository's ordinary Git commit and review
   path. A checkpoint, overlay write, Fabric acknowledgement, branch/PR label, or
   client-supplied commit SHA is insufficient.
5. **Adoption.** The realm-authorised observer independently observes a valid accepted
   Git commit on the configured acceptance ref and exact-matches the adoption envelope
   plus every bound portable digest. For canonical/shared and remote fork realms, only a
   provider-backed observer—running in Gateway or Fabric and reading the configured Git
   host's exact repository identity/ref/commit—qualifies; a Gateway's locally advanced ref
   never does. Only an explicitly local-only realm may designate its Gateway to observe
   the exact local repository/ref/commit.
   Only that realm-valid observation activates the Constitution and
   records operational `constitution.adopted` evidence linked to the proposal, decision,
   repository identity, ref, and commit.

No version becomes active without both human approval and accepted Git observation. This
RFC does not specify a path to remove the human step — see NG1.

## 6. Congress

A dedicated, turn-based space where agents and humans state positions on a proposed Constitution change before a decision is made.

### 6.1 Why not just comments on the task

A proposal task could, in principle, just collect free-text comments. Congress exists as a distinct construct because debate benefits from structure a comment thread doesn't provide:

- **Turns, not a flood.** Each participant (agent or human) gets a bounded number of structured turns — a position, a rationale, optionally a response to a prior turn — rather than an unbounded, hard-to-follow comment war.
- **Explicit stance.** Each turn declares a stance (`support` / `oppose` / `amend`) alongside its rationale, so the decision-maker in step 3 sees a legible summary, not a wall of prose to re-derive sentiment from.
- **Symmetry.** Agents and humans participate through the same turn structure — this is what makes it a genuine debate surface rather than "humans decide, agents may comment."

### 6.2 Indicative shape

```
governance.proposal: "Require a due-by date on all P0 tasks"

Turn 1 (agent:reviewer-bot)   stance: support
  Rationale: P0 tasks without due dates have gone stale in 40% of
  observed cases (KB:incident-log-Q2).

Turn 2 (human:harley)         stance: amend
  Rationale: Agree in principle; exempt tasks tagged `research`
  where a due date is artificial.

Turn 3 (agent:reviewer-bot)   stance: support
  Rationale: Amendment accepted, revises proposal scope.

Decision (human:harley): approved pending Git acceptance — Constitution v14 candidate.
```

### 6.3 Scope limits (V1 of this RFC)

- Turn count per proposal is bounded (exact limit configurable per deployment; default suggestion: 5 turns before a decision is required).
- Only projects that have explicitly adopted RFC-0002 governance run Congress at all — it never activates implicitly.
- No anonymous participation — every turn is attributed to an identity (RFC-0001 §8.4), preserving the audit trail this RFC's G3 goal requires.

## 7. MCP Interface (indicative)

Additive to RFC-0001 §9, active only when governance is adopted:

- `wormhole.governance.propose(title, rationale)` — creates a project-binding-derived `governance.proposal` task
- `wormhole.governance.turn(proposal_id, stance, rationale)` — posts a Congress turn
- `wormhole.governance.decide(proposal_id, outcome)` — human-only; rejects, requests
  revision, or records approved-pending-acceptance
- `wormhole.governance.constitution.get(version?)` — reads the binding-derived current or historical Constitution

Project/workspace/realm scope is resolved from trusted Gateway/Fabric context. No public
Governance argument accepts `project_id`, repository identity, acceptance ref, Fabric
profile, actor, or another routing/authority field.

## 8. Security Considerations

- `governance.decide` must be restricted to identities holding a human-owned, project-scoped authority role (RFC-0001 §8.4 Roles) — no agent identity can call it, by construction, in this RFC's scope (see NG1).
- Constitution versions and debate transcripts are subject to the same multi-tenant isolation guarantees as the rest of the KB (RFC-0001 §13) — one tenant's governance history must never be retrievable by another's agents.
- Canonical shared/Fabric activation authority is the exact tuple `(project_id,
  canonical_repository_identity, acceptance_ref)`. A provider-backed observer
  independently verifies that ref; Fabric catches up after outage when configured. A
  branch, pull request, fork, client-supplied SHA, or Fabric acknowledgement cannot
  activate canonical policy. A Gateway-local commit/ref, including one named like the
  canonical ref, is not provider acceptance.
- A copied upstream Constitution has no upstream authority in a fork. A fork may
  explicitly establish its own repository-identity/ref-scoped governance realm; local-only
  governance likewise declares an explicit local repository lineage and activates only
  after observation of a local Git commit on its configured acceptance ref.
- Accepted-Git activation verifies the exact portable adoption-envelope digest and all
  referenced proposal, Constitution, and debate-event digests. Git acceptance cannot
  bless modified post-approval content, and an operational transcript that was not
  promoted before the decision cannot satisfy adoption.

## 9. Decision Register

### Decided

- **Delegation:** V1 Congress does not support delegated turns. Every turn is
  submitted by, and attributed directly to, the participating identity.
- **Constitution scope:** V1 has one Constitution per project. Per-team
  Constitutions and inheritance are outside this RFC and require an amendment
  supported by real multi-team adoption evidence.
- **Adoption authority:** Human approval is proposal authorisation only. Active policy
  begins at independent observation of an accepted Git commit on the realm's configured
  repository identity and acceptance ref. No overlay, checkpoint, Fabric acknowledgement,
  branch, pull request, fork, or supplied SHA activates it.
- **Approval proof:** The complete bounded Congress transcript and one exact
  digest-bound adoption envelope are portable accepted-state candidates. The envelope,
  not machine-private state or a mutable task status, is the replayable approval proof.

### Open

- **Mid-task supersession:** When a Constitution is superseded while a task is
  in progress, Wormhole has not chosen between grandfathering the version active
  at task start and re-validating the remaining actions under the new version.
  Governance implementation must not guess this policy.

## 10. Adoption Path

Governance is opt-in per repository-scoped project realm, not per deployment. A
self-hosted Wormhole instance can run some projects with governance active and others
without. Enabling it records an explicit acceptance tuple and starts with a proposal;
human approval leaves the candidate pending until the configured Git ref is observed.
Canonical provider-backed activation follows the canonical tuple; a fork may deliberately
create its own tuple, and local-only mode may deliberately create a local lineage. V1 defines no
organisation-wide inheritance or cross-repository Constitution.

Before implementation, tests must cover approved-but-uncommitted, strict realm/adoption
extension decoding and golden digest/signature vectors, absent/non-ancestor/changed realm
base, key-and-adoption-in-one-commit, missing/changed/reordered
debate evidence, post-approval proposal or Constitution edits, second-clone envelope
verification, checkpointed-only, Fabric-acknowledged-only, Gateway-local canonical-name
ref, wrong-ref, branch/PR, client-SHA, fork-copy, explicit remote fork realm,
local-lineage commit, canonical provider-accepted
commit, and Fabric outage/catch-up cases. Only realm-valid accepted observations with the
exact complete envelope activate policy and record one idempotent
`constitution.adopted` evidence item.
