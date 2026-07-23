# RFC Open-Question Reconciliation Design

**Date:** 2026-07-23  
**Scope:** RFC-0001, RFC-0002, RFC-0003, and open issues in `H4RL33/wormhole`

## Goal

Replace ambiguous lists of open questions with explicit decision registers that
distinguish settled architecture, deliberate deferral, and decisions that still
require product or policy choices. Connect unresolved implementation work to its
GitHub issue so the RFCs describe policy while GitHub tracks execution.

## Classification

Each existing question receives exactly one disposition:

- **Decided:** the RFC, current implementation, or another authoritative project
  document already selects an answer. Record that answer and its evidence.
- **Deferred by scope:** the current RFC deliberately excludes the capability.
  Record the boundary and require a future RFC amendment before implementation.
- **Open:** no authoritative choice exists. Keep the question explicit, state why
  it matters, and link an existing issue when one tracks the work.

Implemented behavior can close a question only when it is consistent with the
RFC authority order. Accidental or contradictory behavior remains an issue rather
than silently becoming architecture.

## Proposed Dispositions

### RFC-0001

Close all five questions:

1. Use Postgres-backed polling for v1; do not add a message broker.
2. Use soft rejection with rewrite suggestions for KB compliance.
3. Enforce strict project-scoped KB visibility; do not merge project KBs.
4. Scope identity authority through explicit project Passport bindings. Reusing
   human-readable persona metadata does not federate credentials or permissions.
5. Wormhole exposes its own MCP surface and does not host arbitrary third-party
   MCP servers.

### RFC-0002

- Decide that v1 Congress does not support delegated participation because every
  turn must retain direct attribution.
- Keep superseded-Constitution handling open pending an explicit policy choice.
- Decide that v1 has one Constitution per project. Team-level inheritance is
  deferred until adoption evidence justifies a later RFC amendment.

### RFC-0003

- Defer CRDTs and other post-v1 conflict algorithms outside RFC-0003.
- Freeze the implemented `wormhole.sync.*` v1 schemas as the protocol contract.
- Keep cross-runtime discovery open; bootstrap placeholders are not a decision.
- Adopt same-user OS process trust for local IPC in v1, with no bearer-token layer.
- Adopt integer protocol version `1` with exact-version validation and rejection
  of incompatible peers.
- Keep integration-manifest validation and installation enforcement open; empty
  bootstrap placeholders are not an enforcement model.

## GitHub Issue Reconciliation

Add issue references beside related decisions where useful, without turning RFCs
into a backlog.

Audit every currently open issue and classify it as:

- **closure candidate:** acceptance criteria are demonstrably implemented;
- **still open:** unresolved design, security, correctness, or product work;
- **superseded/duplicate:** another completed change or issue fully replaces it.

The initial evidence pass identifies:

- likely closure candidates: #1–#10, #21, and #24;
- likely still open: #22, #23, #32, and #33.

These are hypotheses, not final issue dispositions. Verify each against current
code, tests, and RFC exit criteria before recommending closure. This documentation
pass does not mutate GitHub issue state.

## Document Changes

In each RFC:

1. Rename `Open Questions` to `Decision Register`.
2. Separate `Decided`, `Deferred by scope`, and `Open` entries.
3. State concrete behavior rather than preserving historical question wording.
4. Update earlier cross-references that still describe a closed question as open.
5. Preserve genuinely unresolved questions without inventing implementation.

Add a concise GitHub issue reconciliation document under `docs/` containing the
evidence and recommended state for every open issue. Link it from RFC entries only
where the issue materially tracks that decision.

## Validation

- Search all RFCs for stale “open question” and OQ references.
- Check every disposition against RFC text, current code, or an explicit scope
  boundary.
- Check every referenced GitHub issue number and state.
- Review the diff for contradictions with `agents/README.md` and
  `docs/implementation-rules.md`.
- Run documentation-relevant repository checks after editing.

## Non-Goals

- No production code changes.
- No new protocol or governance implementation.
- No automatic closure or modification of GitHub issues.
- No resolution of the superseded-Constitution policy without a user decision.
- No resolution of cross-runtime discovery or manifest enforcement without a
  separate approved design.
