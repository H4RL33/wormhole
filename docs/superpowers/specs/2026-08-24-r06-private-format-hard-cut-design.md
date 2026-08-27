# R06 Private-Format Hard-Cut Design

**Status:** Approved design
**Date:** 2026-08-24
**Scope:** R06 only

## 1. Decision

Wormhole is closed pre-alpha software with no public users and no private-format
compatibility promise. R06 therefore makes a complete private Gateway format
break. The new runtime supports only its consolidated current private schema
and current private proof formats. It does not migrate, normalize, export, or
otherwise interpret a database created by a pre-R06 binary.

The first R06 private schema is identified as Gateway schema version 6. Version
6 is a format epoch, not an incremental migration from version 5. A fresh
database is created directly at the complete version-6 shape in one atomic
initialization. An existing exact version-6 database opens without mutation.
Every database at version 5 or below is unsupported and is refused before any
mutation. A future, incomplete, malformed, or ambiguous ledger is likewise
refused before mutation.

This decision supersedes only private runtime compatibility. The tracked
portable `.wormhole/state/v1/` format remains supported because it is Git
project state, not private Gateway storage.

## 2. Goals

- Delete the private v1-to-v5 upgrade, backfill, normalization, and rollback
  compatibility surface.
- Delete compatibility readers for unreleased version-zero checkpoint and
  materialization proof shapes, nil legacy envelopes, and old private-history
  forms.
- Create fresh private databases directly at one complete version-6 schema.
- Refuse every pre-R06 private database before mutation with concise,
  actionable preservation/reset guidance.
- Preserve unknown or unsupported private evidence unchanged.
- Preserve every retained R01-R05 guarantee and the approved R10 recovery
  matrix for current version-6/current-proof data.
- Update operator, contributor, agent, compatibility, and programme
  documentation to state the closed-pre-alpha compatibility policy.
- Stop all reduction work after R06 so subsequent work can return to feature
  delivery toward the Git-native branch goal.

## 3. Non-goals

R06 does not:

- provide an exporter, converter, or in-process compatibility migrator;
- auto-reset, delete, rename, quarantine, or overwrite an unsupported database;
- remove `legacy_integration_state_migrations`,
  `LegacyIntegrationBackupRoot`, or other W11/Task-6 seams;
- change portable Git v1 codecs, validation, canonical encoding, or clean-clone
  reconstruction;
- change filesystem containment, publication classification, R10 recovery
  topology, durability, terminal-history retention, or pruning;
- implement R07-R14, lifecycle extraction, package restructuring, Tasks
  6/6A/7/8, Stage 2, or unrelated cleanup.

## 4. Supported private-format floor

The new runtime recognizes two successful open states:

1. **Fresh:** no Wormhole private schema or migration ledger exists. The
   runtime atomically installs the complete version-6 schema and exact current
   ledger identity.
2. **Exact current:** the database has the complete, contiguous version-6
   ledger and exact current schema/proof invariants. It opens without a schema
   write.

All other states fail closed before mutation:

- a version-1 through version-5 ledger;
- an older base schema without a current ledger;
- a future version;
- a missing, gapped, duplicated, malformed, or partially initialized ledger;
- a schema whose required current objects or invariants do not match version
  6; or
- current tables containing unsupported legacy journal/proof evidence.

The runtime must not infer provenance from table resemblance. Only the fresh
classification may initialize. Only the exact-current classification may open.

## 5. Initialization and open flow

`localstore.Open` performs a read-only private-format preflight before any
schema, cache, journal, or workspace mutation.

For a fresh database, initialization runs in one transaction:

- install the complete current schema directly;
- include the unchanged dormant Task-6 seam required by the current schema;
- seed the exact version-6 ledger identity;
- validate the installed current shape; and
- commit only after all statements and validation succeed.

Any initialization failure rolls back to the fresh preimage. No incremental
migration runner remains in the normal runtime.

For an exact-current database, preflight validates the ledger, current schema,
and current private proof floor before returning the store. It performs no
normalization or repair. Ordinary schema constraints and semantic readers
remain the authorities after open.

## 6. Unsupported-format refusal and human workflow

Unsupported databases remain byte-for-byte under operator control. Wormhole
does not automatically move or delete them. The error identifies:

- that the database uses an unsupported closed-pre-alpha private format;
- that the new runtime made no mutation;
- the resolved database path;
- that unpublished overlays, stashes, or pending checkpoints may exist;
- that valuable unpublished state must be inspected or checkpointed with the
  old binary that created it; and
- that otherwise the operator may back up the database, stop Gateway, remove
  the private database through an explicit manual action, and rerun setup.

Reset documentation must put unpublished-work detection and backup before any
removal instruction. The current binary offers no export or reset command.

## 7. Current proof floor and recovery

New and current materialization/checkpoint journals use only the current
version-1 proof shape. Version-zero/null proof tuples and nil legacy accepted
envelopes are invalid. They are not normalized into current values.

Unsupported old pending evidence is preserved and refused. It is never
automatically recovered by the current binary. Current version-1 evidence in
an exact version-6 database retains the four approved R10 automatic recovery
topologies, containment checks, no-replace restoration, stable-storage
contract, and recovery-blocked behavior for every noncanonical topology.

## 8. Retained guarantees

R06 retains:

- Git as sole code truth;
- exact project/workspace isolation;
- atomic and durable attributed local overlays;
- human/agent project-operation parity;
- deterministic portable Git v1 codecs and clean-clone reconstruction;
- repository hostility and filesystem containment boundaries;
- semantic three-way reconciliation without silent meaning loss;
- publication review and classification behavior;
- no blind replay after indeterminate database or filesystem mutation;
- the sole daemon/database authority, workspace revision authority, bounded
  current worksets, explicit audit, and compact confirmation established by
  R01-R05; and
- host/power-loss durability and the approved R10 recovery matrix.

## 9. Testing and evidence

Implementation uses test-driven slices. Required causal evidence includes:

- an old version-5 database is refused before any bytes or filesystem evidence
  change;
- a malformed/future/partial ledger is refused before mutation;
- failed fresh initialization leaves no partial schema;
- a fresh database becomes exact version 6 atomically;
- an exact version-6 database reopens without schema mutation;
- version-zero/null proof data is rejected and preserved;
- current version-1 checkpoint/recovery behavior remains green;
- portable tracked Git v1 round-trips and clean-clone reconstruction remain
  green; and
- the dormant Task-6 table and backup-root plumbing remain present and
  unchanged.

Final verification reruns focused localstore/projectstate tests, the complete
G/A architecture evidence, the full repository integration and race suites,
`make check` at at least 80 percent merged coverage, release tests, release
rehearsal, diff/format checks, and clean-clone verification.

## 10. Documentation and completion boundary

R06 completion updates at least:

- root `README.md`;
- `agents/README.md`;
- `docs/implementation-rules.md`;
- `docs/compatibility.md`;
- operator setup/reset or troubleshooting guidance;
- the Git-native programme plan;
- the R06 implementation record and `.superpowers/sdd/progress.md`.

Documentation must distinguish tracked portable Git v1 stability from private
Gateway format instability and must not describe a current-binary exporter or
automatic reset.

R06 ends with a reviewed implementation boundary and a separate documentation
record. The branch then pauses every remaining reduction item. R07-R14 remain
unauthorized. The next authorized work is feature building toward the branch's
Git-native product goal unless the human later reopens reduction explicitly.
