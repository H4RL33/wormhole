# Stage 1A R01-R05 reduction scorecard

Date: 2026-08-24

## Result and immutable boundaries

The measured result is **simplified / pass to the mandatory human go/no-go
pause**. It is not authorisation to begin R06 or any later tranche.

The immutable comparison boundaries are:

- baseline: `4d84903eba1efb36a4348f5f1c81db9e6eb5c624`;
- R01 implementation boundary:
  `01da36d69ac11cd6e2c56a896e3d8961853822ff`; and
- R05 implementation boundary:
  `41683b94a06c7b5faa07ad97010cec2462af47f3`.

`R05_END` is the exact production/test commit. This scorecard and the rest of
the pause packet are documentation-only and remain outside that boundary.

Every objective simplification condition passes:

| Condition | Reproduced evidence | Result |
| --- | --- | --- |
| Overall production is subtractive | baseline..R05 production including migration `+2820/-3670`, net `-850` | pass |
| R02-R05 production is subtractive | R01..R05 production including migration `+2301/-3504`, net `-1203` | pass |
| Tests are subtractive after architecture replacement | R01..R05 tests `+5669/-9371`, net `-3702`, including the `+989` architecture-test subset | pass |
| Total implementation decreases | baseline..R05 net `-2982`; R01..R05 net `-4905` | pass |
| Sole authorities remain | one database opener, revision tracker/finaliser, current-workset family, explicit audit, and compact confirmer | pass |
| Old complete token/fallback is unreachable | all superseded Go authority names absent | pass |
| Ordinary lifecycles do not scan terminal history | ProjectState complete-history caller counts are all zero | pass |
| Verification and coverage floor pass | all final gates green; merged atomic statement coverage `85.0%` against the unchanged `80.0%` floor | pass |

## Final verification record

Independent final specification and quality reviews approved the complete
production/test range before `R05_END`. No production or test change followed
the frozen boundary.

| Gate | Exact evidence | Result |
| --- | --- | --- |
| Aggregate G suite | the five commands in Shared Evidence | `5/5` green |
| A01-A25 ledger | every executable command in the subsumption ledger, duplicates retained | `91/91` green |
| A19 platform gates | Linux no-replace test; Darwin arm64 and FreeBSD amd64 binary plus test compilation | all five green |
| Focused packages | `go test ./cmd/gatewayd ./cmd/wormhole ./internal/runtime/localapi ./internal/runtime/localstore ./internal/runtime/projectstate -count=1` | green |
| Migration/architecture subsets | the two exact Task-17 subset commands | green |
| Standalone race | `go test -race ./cmd/gatewayd ./cmd/wormhole ./internal/runtime/localapi ./internal/runtime/localstore ./internal/runtime/projectstate -count=1` | green, no race report |
| Repository check | `make check` using a migrated temporary PostgreSQL 18 plus pgvector service | green |
| Coverage | repository-wide atomic `-coverpkg=./...` profile checked by the unchanged repository checker | `85.0%`, floor `80.0%` |
| Release tests | `make release-test` | green |
| Release rehearsal | `make release-rehearsal` under isolated rootless Docker | green; output confined to ignored `dist/` |
| Diff | `git diff --check` | exit 0 |
| Clean clone | exact Task-17 clone/checkout and four affected test commands | green at detached `41683b94a06c7b5faa07ad97010cec2462af47f3` |

The initial release-rehearsal attempt failed because the privileged Docker
daemon was inactive. Verification then used an isolated, user-owned RootlessKit
3.1.0 daemon with `slirp4netns`, the `vfs` storage driver, and a custom
`DOCKER_HOST`. External `PATH` wrappers, kept outside the repository, adjusted
only verifier-created `mktemp` bind-directory modes to emulate rootful
same-UID writes under subordinate-ID mapping. This was an environment
compatibility workaround, not a supported product setup; it changed no source,
production, or test file. The exact final `make release-rehearsal` exited 0,
and the temporary daemon was torn down afterward.

The earlier `77.6%` profile was incomplete evidence from an unavailable
PostgreSQL environment and was not treated as a pass. The final supported,
migrated integration environment produced the authoritative `85.0%` result.
Host timings are raw execution evidence only, not performance claims.

## Repository numstat

Every changed non-documentation path is classified exactly once in the path
table below. Non-test Go/support is production; Gateway migration SQL is
reported separately and included in production; `_test.go` is test; and
`internal/runtime/projectstate/architecture_test.go` is the labelled
architecture-test subset. There are no changed generated/vendor files,
testdata/goldens, binaries, or other implementation classes. The R01 change to
`agents/README.md` (`+13/-6`) is documentation/context and is excluded from
implementation LOC, as are all `docs/` and `.superpowers/` paths.

| Range | Production Go/support | Migration SQL | Production incl. migration | Tests | Architecture subset | Total implementation |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| baseline..R01 | `+519/-166` (`+353`) | `+0/-0` | `+519/-166` (`+353`) | `+1670/-100` (`+1570`) | `+0/-0` | `+2189/-266` (`+1923`) |
| R01..R05 | `+2292/-3504` (`-1212`) | `+9/-0` (`+9`) | `+2301/-3504` (`-1203`) | `+5669/-9371` (`-3702`) | `+989/-0` | `+7970/-12875` (`-4905`) |
| baseline..R05 | `+2811/-3670` (`-859`) | `+9/-0` (`+9`) | `+2820/-3670` (`-850`) | `+7339/-9471` (`-2132`) | `+989/-0` | `+10159/-13141` (`-2982`) |

R01 is the deliberately isolated, additive sole-owner/private-RPC
prerequisite. The selected R02-R05 reduction more than repays it in production,
tests, and total implementation.

## Reproducible workspace manifests

The manifests and physical LOC follow design section 11.1 exactly.

Baseline production: 8 files, 5,524 physical lines.

```text
internal/runtime/localstore/workspace_checkpoint_commit_repo.go
internal/runtime/localstore/workspace_conflict_repo.go
internal/runtime/localstore/workspace_materialization_repo.go
internal/runtime/localstore/workspace_publication_repo.go
internal/runtime/localstore/workspace_repo.go
internal/runtime/localstore/workspace_restore_retry_repo.go
internal/runtime/localstore/workspace_stash_repo.go
internal/runtime/localstore/workspace_transition_repo.go
```

Baseline tests: 9 files, 12,010 physical lines.

```text
internal/runtime/localstore/workspace_checkpoint_commit_repo_test.go
internal/runtime/localstore/workspace_conflict_repo_test.go
internal/runtime/localstore/workspace_materialization_repo_test.go
internal/runtime/localstore/workspace_publication_repo_test.go
internal/runtime/localstore/workspace_repo_test.go
internal/runtime/localstore/workspace_restore_retry_repo_test.go
internal/runtime/localstore/workspace_stash_repo_test.go
internal/runtime/localstore/workspace_transition_boundary_test.go
internal/runtime/localstore/workspace_transition_repo_test.go
```

R05 production: 11 files, 4,587 physical lines. The successor revision,
current-workset, audit, and compact-confirmation files are included explicitly.

```text
internal/runtime/localstore/workspace_commit_confirmation.go
internal/runtime/localstore/workspace_conflict_repo.go
internal/runtime/localstore/workspace_current_mutation_repo.go
internal/runtime/localstore/workspace_history_audit.go
internal/runtime/localstore/workspace_materialization_repo.go
internal/runtime/localstore/workspace_publication_repo.go
internal/runtime/localstore/workspace_repo.go
internal/runtime/localstore/workspace_restore_retry_repo.go
internal/runtime/localstore/workspace_revision_repo.go
internal/runtime/localstore/workspace_stash_repo.go
internal/runtime/localstore/workspace_transition_repo.go
```

R05 tests: 13 files, 9,439 physical lines.

```text
internal/runtime/localstore/workspace_commit_confirmation_test.go
internal/runtime/localstore/workspace_conflict_repo_test.go
internal/runtime/localstore/workspace_corruption_boundary_test.go
internal/runtime/localstore/workspace_current_repo_test.go
internal/runtime/localstore/workspace_history_audit_test.go
internal/runtime/localstore/workspace_materialization_repo_test.go
internal/runtime/localstore/workspace_publication_repo_test.go
internal/runtime/localstore/workspace_repo_test.go
internal/runtime/localstore/workspace_restore_retry_repo_test.go
internal/runtime/localstore/workspace_revision_repo_test.go
internal/runtime/localstore/workspace_stash_repo_test.go
internal/runtime/localstore/workspace_transition_boundary_test.go
internal/runtime/localstore/workspace_transition_repo_test.go
```

## Structural scorecard

| Metric | Baseline | R05 | Change |
| --- | ---: | ---: | ---: |
| workspace localstore production LOC | 5,524 | 4,587 | -937 |
| matching workspace localstore test LOC | 12,010 | 9,439 | -2,571 |
| checkpoint/confirmation production LOC | 606 | 536 | -70 |
| checkpoint/confirmation test LOC | 944 | 708 | -236 |
| old complete-token fields | 7 | 0 (type deleted) | -7 |
| compact successor-token fields | n/a | 10 | bounded replacement |
| dedicated confirmation functions | 35 | 15 | -20 |
| top-level checkpoint/targeted capture reader calls | 5 complete-state calls | 6 materialisation / 2 publication | split bounded exact-target compositions |
| production `CAST(` | 63 | 0 | -63 |
| production `typeof(` | 179 | 1 | -178 |
| production `StorageClasses` | 79 | 0 | -79 |
| production identifier tokens ending in `Raw` | 167 | 0 | -167 |
| ProjectState `.OperationAudit(` callers | 2 | 0 | -2 |
| ProjectState `.MaterializationDisposition(` callers | 6 | 0 | -6 |
| ProjectState `.PublicationPolicyHistory(` callers | 2 | 0 | -2 |

The old seven fields were `version`, `scope`, complete binding, current policy,
complete policy history, complete materialisation disposition, and adjacent
candidate/operation evidence. The ten compact fields are `formatVersion`,
`scope`, `revision`, `targetKind`, `targetID`, `targetState`, `currentOwnerID`,
`transitionClass`, `authorityDigest`, and optional `postimageDigest`.

The old complete capture had five direct state-reader calls:
`publicationBindingEvidence`, `publicationPolicyState`,
`MaterializationDisposition`, `Candidate`, and
`materializationAdjacentEvidence`. That complete capture has zero R05 call
sites because its type/API are deleted. The targeted materialisation successor
has six direct reader/projection calls: `MaterializationByJournalID`,
`CurrentMaterialization`, `projectedWorkspaceRevision`, `Workspace`,
`Candidate`, and `OperationsByGenerations`. The separate publication successor
has two: `PublicationPolicy` and `projectedWorkspaceRevision`. The increase from
five to six on the checkpoint path is a bounded exact-target composition, not
a complete-state fallback: it reads one named journal, one current owner, the
projected revision, and only the journal-owned logical postimage.

## Compact semantic authority

The compact token does not hash raw SQLite representation.

- Materialisation `authorityDigest` is canonical JSON over schema version,
  exact scope, journal ID/presence/state, expected-live and accepted-base
  digests, checkout identity, prior/candidate digests, through-generation,
  stage/backup child names, and semantic digests of included operations,
  publication review, and prior candidate.
- Materialisation `postimageDigest` is canonical JSON over the exact logical
  current status, semantic candidate, and journal-owned operations, or the
  explicit absent postimage.
- Publication `authorityDigest` is canonical JSON over schema version, exact
  scope, requested configured/sticky transition class, repository identity,
  origin digest, classification, policy revision, transition kind, actor, and
  changed time.

Both forms bind projected workspace revision in the envelope. Confirmation
reads one coherent transaction, classifies exact prior/exact next/third, and
never replays a writer, Git, filesystem, or callback operation.

## Removed and retained representation paths

| Family | Removed private proof | Retained semantic/schema authority |
| --- | --- | --- |
| binding, accepted base, status, operations | raw timestamp/full-row CAS, `CAST`/`typeof` projections, storage-class and post-reread echo | typed transitions, exact scope/state, strict logical operation decoding, affected-row proof, real statement rollback, R03 revision CAS |
| conflicts | raw occurrence/resolution metadata, storage-class/trigger-rewrite proof | semantic conflict identity/occurrences/resolution, exact scope, atomic rollback, revision authority |
| named stash | rowid/count aliases, storage-class and BLOB/TEXT key proofs, repository JSON byte-remarshal equality | exact logical scope/key, repository/tree/digest/actor/operation validation, collision/absence/restart/isolation and rollback behavior |
| transition receipts | storage classes, `CAST` scope joins, duplicate representation proofs | immutable exact request/scope semantics, receipt-first idempotency, V2 committed revision and named semantic-postimage digest |
| materialisation | complete disposition/history scans, raw optional-proof/storage metadata and adjacent proof forest | exact named journal, strict tree/digest/proof decoding, current-owner cardinality, current workset, explicit audit and durability behavior |
| publication | full binding evidence, binding/policy/bootstrap post-rereads, raw `changed_at` CAS, storage metadata, repository JSON byte equality | strict logical repository/policy/actor/digest/time decoding, semantic expected transition, affected row plus history write, revision CAS, ordered explicit audit |
| checkpoint COMMIT | universal field-complete binding/policy/history/materialisation/adjacent token and whole-state fallback | compact scope/revision/target/current-owner/transition envelope plus canonical authority/postimage digests |
| restore retry and history | complete terminal restore/operation/materialisation proof forests and raw timestamp/blob echoes | exact named current stash, V2 committed revision/postimage digest, explicit read-only ordered history audit, preserved R10 recovery matrix |

The only retained `typeof(` in the R05 workspace production manifest is
`typeof(workspace_revision)` in `workspace_repo.go`. It is semantic R03
validation that the current private revision is integer and positive. The
migration SQL's separate `CHECK(typeof(workspace_revision)='integer')` is
schema evidence outside this lexical manifest. Schema foreign keys, unique and
immutability constraints, strict logical decoders, semantic digests, exact
scope/current ownership, real `RAISE(ABORT)` rollback seams, restart/durability
tests, the explicit history audit, and R10 remain retained.

## Sole-authority evidence

- One production database open remains:
  `cmd/gatewayd/gatewayd.go:314`, using `ownerLock.DatabasePath()`.
- `workspaceRevisionTracker` and its four methods exist only in
  `workspace_revision_repo.go`. Exactly two transaction wrappers call the one
  finaliser. One production `SET workspace_revision=?` performs the exact
  scope/expected-revision CAS; SQL `workspace_revision + 1` is absent.
- `CurrentMaterialization` has one definition in
  `workspace_current_mutation_repo.go`; `RestoreCurrentState` has one named
  current-workset definition in `workspace_restore_retry_repo.go`.
- `AuditWorkspaceHistory` has one definition in
  `workspace_history_audit.go` and is not an ordinary lifecycle reader.
- `ConfirmWorkspaceCommit` has one definition in
  `workspace_commit_confirmation.go`.
- `WorkspaceCheckpointCommitState`, `CaptureCheckpointCommitState`,
  `ConfirmCheckpointCommit`, `WorkspaceMaterializationDisposition`,
  `MaterializationDisposition`, `WorkspaceRestoreRetryState`,
  `RestoreRetryState`, `.OperationAudit(`, and
  `.PublicationPolicyHistory(` have zero Go occurrences.

## G01-G05 causal evidence

The final aggregate G suite is `5/5` green. Its replacement oracles were shown
to fail for the displaced mechanism, then restored byte-exact:

| Gap | Causal RED evidence | Restored/final evidence |
| --- | --- | --- |
| G01 sole owner/private RPC | bypassing `gatewayd`'s owner lock and opening the database path directly failed the losing-owner no-mutation and unsafe-owner WAL checks; pre/restored file SHA `fe05a11f7a0271f5919b852939834bf939764e17c6d5f71b266fc7a78d7f85a5`, temporary `aeb2bc2288c38434e23b9287a280668c67490f54a35329d04657a071d871d3d1`, temporary diff `6695b852182222ac2c494f4dc584d23c42986f6cb07607d1495a72626b15c20f` | owner/quiescence/private-RPC/stopped-daemon aggregate green |
| G02 semantic corruption boundary | bypassing the negative stash operation-count decoder changed `4ea47ce1a13acfe81762ad253b9b7aba24628d136f24422199996d6783226bac` to `5f6b72b532f085fcc840d9f6af06146eabfeb493f7df85dfdc661a3a9b83bd72` (temporary diff `1f28243bb42e6bdc7783c6ce75e4877cb14e1588591e99e1c33707411e8ef9ee`), admitted `OperationCount:-1`, and failed the named malformed-receipt row | coarse target+sibling/Git/worktree/artifact boundary and real-ABORT wrapper rollback green |
| G03 sole revision | removing the publication writer mark changed `f05e7722bb6465e2550e544cc3dceeeb4e7d07cdd94b6e4b2877252fe2f5e1ca` to `a1b48fabed9df7ca7789f1e42406ba797a9eef932c7ff4b83a4a943666050008` (temporary diff `fc14cce436f7d7364438b3220da2dbb18e448a6374de56fbe4eb1df04357b6ff`) and committed revision 1 instead of 2; direct test constructions also prove double finalisation and unchecked overflow fail | writer inventory, no-op/rollback rules, projection, restart, malformed/overflow and single-CAS authority green |
| G04 current workset versus audit | coupling import to `AuditWorkspaceHistory` changed `34ee80200c8967c2ac780d2e37ce4a79352c59caae3e4a76f2181477c9ddc795` to `c962a63bb18a15c6e9fda106eb2dca79770d81d5bde57da266836057c46734ca` (temporary diff `f390b337d2d96ac9adbfadbe4b884d1db5387fe839f1c4bef5c03866748e5362`) and made valid import fail on unrelated terminal `schema_version=0` | import/stash/restore/observe/checkpoint/recovery/publication ignore unrelated terminal corruption while explicit audit reports it green |
| G05 compact confirmation | direct test tables omit/substitute target, revision, alternate transition, or current owner and require Third rather than prior/next | journal and publication exact prior/next/third, read-failure, unrelated-history and no-replay matrices green |

The full hashes, temporary diff hashes, exact RED output, restoration evidence,
and row-level deleted tests are retained in the executable subsumption ledger.

## A01-A25 retained architecture evidence

All 91 executable A-row commands passed at `R05_END`; duplicated commands in
different A rows were executed rather than deduplicated. The complete ledger
contains 44 green rows: 15 G-family causal/subsumption rows, four R02
raw-proof-family subsumption rows, and 25 A-family retained-guarantee rows. The
exact test symbols, commands, removed symbols/queries/tests, and replacement
owners remain in that ledger.

| ID | Retained observable invariant | Final status |
| --- | --- | --- |
| A01 | Git is sole code truth; overlay mutation is not publication; checkpoint publishes only the selected plan. | green |
| A02 | Registration/resolution is idempotent and rejects replacement, ambiguity, and collision. | green |
| A03 | Project/workspace scopes cannot read or mutate siblings. | green |
| A04 | Repository reads cannot escape, follow hostile links, fetch, or exceed bounds. | green |
| A05 | Canonical state, semantic diff, digests, and conflict identities are deterministic. | green |
| A06 | Overlay mutation is attributed, ordered, atomic, and restart durable. | green |
| A07 | Status/diff are coherent read-only views and never repair or advance state. | green |
| A08 | Import is a semantic three-way rebase with lossless conflicts and atomic failure. | green |
| A09 | Stash/restore preserve provenance, scope, idempotency, restart safety, and conflicts. | green |
| A10 | Publication review binds one stable scoped view; drift/invalidation fail closed durably. | green |
| A11 | Checkpoint requires acknowledgement/actor parity and rejects conflict or drift before publication. | green |
| A12 | Checkpoint publishes exact reviewed bytes durably without overwriting concurrent input or mutating Git. | green |
| A13 | Checkpoint/recovery interruption preserves durable old/new state or blocked evidence without replay. | green |
| A14 | Recovery converges unambiguous evidence and preserves ambiguous/unsafe evidence without replay. | green |
| A15 | Unknown outcomes classify unchanged/effective/unproved without replay. | green |
| A16 | Pre-mutation cancellation is clean; later failure retains recoverable evidence and causes. | green |
| A17 | No-pending recovery is database-only, stable, and idempotent. | green |
| A18 | Same-scope writers serialize; cancellation releases; scopes remain independent; descriptor ownership does not leak. | green |
| A19 | Linux runtime/no-replace requirements passed; Darwin/FreeBSD binaries and test binaries compile the unsupported-platform guidance without claiming cross-host execution. | green |
| A20 | Released schema migration is atomic, rejects future ledgers, and preserves composed state. | green |
| A21 | Portable state/remotes are canonical, secret-shaped keys are rejected, and checkpoint postimages use durable provenance. | green, including clean clone |
| A22 | Accepted base advances only through trusted Git observation with safe retry behavior. | green |
| A23 | At most one outstanding checkpoint exists, blocked before artifact allocation. | green |
| A24 | Invalid canonical portable shapes and credential-shaped remote keys are rejected. | green |
| A25 | Nested-mount substitution is rejected and persistent roots are revalidated before every rename. | green |

## R01-R05 subsumption summary

The executable ledger is the canonical row-level old-test/symbol/query
inventory. Its reduction-level crosswalk is:

| Reduction | Displaced authority | Replacement owner/evidence | Result |
| --- | --- | --- | --- |
| R01 | independent CLI/database ownership and direct Code Graph lifecycle | lifetime Gateway owner lock, hidden private RPC, daemon-derived binding, shared executor; G01 + A18 | old path unreachable |
| R02 (four ledger rows) | raw timestamp/storage-class/row-shape and hostile-trigger proofs across current binding/status/conflict, named stash, named receipt, and publication/materialisation/audit/repository families | semantic corruption boundary, exact scope/current ownership, affected rows, strict logical decoding, real rollback, explicit audit; G02 + required A rows | raw proof paths deleted |
| R03 | statement-count increments, shadow counters, revision-blind writers | one lazy transaction tracker, projected revision and exact final CAS; G03 + required A rows | one revision authority |
| R04 | `WorkspaceCheckpointCommitState`, universal complete-state comparison and publication-specific confirmation | `WorkspaceCommitConfirmation`, canonical target authority/postimage digests, exact prior/next/third; G03/G05 + required A rows | complete token/fallback deleted |
| R05 | ordinary complete operation/materialisation/restore/publication history scans | exact current/named worksets plus `AuditWorkspaceHistory`; G04 + required A rows | hot-path complete-history callers zero |

No completed replacement family leaves both old and new authority reachable.

## Complete non-documentation path classification

The two numstat columns are the isolated implementation ranges. Each of the 79
changed implementation paths appears exactly once.

| Path | Classification | baseline..R01 | R01..R05 |
| --- | --- | ---: | ---: |
| `cmd/gatewayd/gatewayd.go` | production Go/support | +9/-12 | +0/-0 |
| `cmd/gatewayd/gatewayd_test.go` | test | +9/-4 | +0/-0 |
| `cmd/gatewayd/owner_lock_linux.go` | production Go/support | +218/-0 | +0/-0 |
| `cmd/gatewayd/owner_lock_linux_test.go` | test | +783/-0 | +0/-0 |
| `cmd/gatewayd/owner_lock_unsupported.go` | production Go/support | +21/-0 | +0/-0 |
| `cmd/wormhole/code_graph.go` | production Go/support | +3/-38 | +0/-0 |
| `cmd/wormhole/code_graph_coverage_additional_test.go` | test | +84/-33 | +0/-0 |
| `cmd/wormhole/code_graph_test.go` | test | +113/-49 | +0/-0 |
| `cmd/wormhole/gateway_private.go` | production Go/support | +88/-0 | +0/-0 |
| `cmd/wormhole/integration.go` | production Go/support | +1/-64 | +0/-0 |
| `cmd/wormhole/integration_test.go` | test | +15/-0 | +0/-0 |
| `internal/runtime/localapi/codegraph.go` | production Go/support | +89/-25 | +0/-0 |
| `internal/runtime/localapi/codegraph_coverage_test.go` | test | +3/-1 | +0/-0 |
| `internal/runtime/localapi/codegraph_lifecycle_rpc_test.go` | test | +467/-0 | +0/-0 |
| `internal/runtime/localapi/codegraph_lifecycle_test.go` | test | +9/-9 | +0/-0 |
| `internal/runtime/localapi/codegraph_rebuild_test.go` | test | +2/-2 | +0/-0 |
| `internal/runtime/localapi/codegraph_status_test.go` | test | +2/-2 | +0/-0 |
| `internal/runtime/localapi/localapi.go` | production Go/support | +58/-22 | +0/-0 |
| `internal/runtime/localapi/localapi_test.go` | test | +183/-0 | +0/-0 |
| `internal/runtime/localapi/mcp.go` | production Go/support | +32/-5 | +0/-0 |
| `internal/runtime/localstore/migrations.go` | production Go/support | +0/-0 | +1/-1 |
| `internal/runtime/localstore/migrations/000005_workspace_revision.sql` | migration SQL (production) | +0/-0 | +9/-0 |
| `internal/runtime/localstore/migrations_test.go` | test | +0/-0 | +715/-11 |
| `internal/runtime/localstore/workspace_checkpoint_commit_repo.go` | production Go/support | +0/-0 | +0/-606 |
| `internal/runtime/localstore/workspace_checkpoint_commit_repo_test.go` | test | +0/-0 | +0/-944 |
| `internal/runtime/localstore/workspace_commit_confirmation.go` | production Go/support | +0/-0 | +536/-0 |
| `internal/runtime/localstore/workspace_commit_confirmation_test.go` | test | +0/-0 | +708/-0 |
| `internal/runtime/localstore/workspace_conflict_repo.go` | production Go/support | +0/-0 | +68/-47 |
| `internal/runtime/localstore/workspace_conflict_repo_test.go` | test | +0/-0 | +2/-115 |
| `internal/runtime/localstore/workspace_corruption_boundary_test.go` | test | +0/-0 | +184/-0 |
| `internal/runtime/localstore/workspace_current_mutation_repo.go` | production Go/support | +0/-0 | +351/-0 |
| `internal/runtime/localstore/workspace_current_repo_test.go` | test | +0/-0 | +276/-0 |
| `internal/runtime/localstore/workspace_history_audit.go` | production Go/support | +0/-0 | +209/-0 |
| `internal/runtime/localstore/workspace_history_audit_test.go` | test | +0/-0 | +259/-0 |
| `internal/runtime/localstore/workspace_materialization_repo.go` | production Go/support | +0/-0 | +31/-869 |
| `internal/runtime/localstore/workspace_materialization_repo_test.go` | test | +0/-0 | +0/-2706 |
| `internal/runtime/localstore/workspace_publication_repo.go` | production Go/support | +0/-0 | +100/-340 |
| `internal/runtime/localstore/workspace_publication_repo_test.go` | test | +0/-0 | +57/-209 |
| `internal/runtime/localstore/workspace_repo.go` | production Go/support | +0/-0 | +122/-207 |
| `internal/runtime/localstore/workspace_repo_test.go` | test | +0/-0 | +40/-738 |
| `internal/runtime/localstore/workspace_restore_retry_repo.go` | production Go/support | +0/-0 | +36/-322 |
| `internal/runtime/localstore/workspace_restore_retry_repo_test.go` | test | +0/-0 | +21/-601 |
| `internal/runtime/localstore/workspace_revision_repo.go` | production Go/support | +0/-0 | +79/-0 |
| `internal/runtime/localstore/workspace_revision_repo_test.go` | test | +0/-0 | +1568/-0 |
| `internal/runtime/localstore/workspace_stash_repo.go` | production Go/support | +0/-0 | +18/-76 |
| `internal/runtime/localstore/workspace_stash_repo_test.go` | test | +0/-0 | +8/-193 |
| `internal/runtime/localstore/workspace_transition_boundary_test.go` | test | +0/-0 | +3/-49 |
| `internal/runtime/localstore/workspace_transition_repo.go` | production Go/support | +0/-0 | +51/-71 |
| `internal/runtime/localstore/workspace_transition_repo_test.go` | test | +0/-0 | +5/-147 |
| `internal/runtime/projectstate/architecture_test.go` | architecture test | +0/-0 | +989/-0 |
| `internal/runtime/projectstate/checkpoint.go` | production Go/support | +0/-0 | +75/-136 |
| `internal/runtime/projectstate/checkpoint_plan.go` | production Go/support | +0/-0 | +5/-101 |
| `internal/runtime/projectstate/checkpoint_plan_test.go` | test | +0/-0 | +4/-272 |
| `internal/runtime/projectstate/checkpoint_recovery.go` | production Go/support | +0/-0 | +17/-65 |
| `internal/runtime/projectstate/checkpoint_recovery_linux_test.go` | test | +0/-0 | +27/-12 |
| `internal/runtime/projectstate/checkpoint_recovery_test.go` | test | +0/-0 | +64/-534 |
| `internal/runtime/projectstate/checkpoint_test.go` | test | +0/-0 | +144/-200 |
| `internal/runtime/projectstate/git_observer.go` | production Go/support | +0/-0 | +10/-38 |
| `internal/runtime/projectstate/git_observer_test.go` | test | +0/-0 | +37/-65 |
| `internal/runtime/projectstate/import.go` | production Go/support | +0/-0 | +143/-15 |
| `internal/runtime/projectstate/import_test.go` | test | +0/-0 | +0/-81 |
| `internal/runtime/projectstate/materialization.go` | production Go/support | +0/-0 | +2/-99 |
| `internal/runtime/projectstate/materialization_test.go` | test | +0/-0 | +8/-230 |
| `internal/runtime/projectstate/publication_policy.go` | production Go/support | +0/-0 | +34/-64 |
| `internal/runtime/projectstate/publication_policy_test.go` | test | +0/-0 | +131/-55 |
| `internal/runtime/projectstate/publication_review_service.go` | production Go/support | +0/-0 | +3/-1 |
| `internal/runtime/projectstate/publication_review_service_test.go` | test | +0/-0 | +11/-11 |
| `internal/runtime/projectstate/restore.go` | production Go/support | +0/-0 | +47/-41 |
| `internal/runtime/projectstate/restore_codec.go` | production Go/support | +0/-0 | +115/-2 |
| `internal/runtime/projectstate/restore_codec_test.go` | test | +0/-0 | +43/-0 |
| `internal/runtime/projectstate/restore_plan.go` | production Go/support | +0/-0 | +50/-49 |
| `internal/runtime/projectstate/restore_plan_test.go` | test | +0/-0 | +0/-691 |
| `internal/runtime/projectstate/restore_retry.go` | production Go/support | +0/-0 | +151/-320 |
| `internal/runtime/projectstate/restore_retry_test.go` | test | +0/-0 | +0/-1390 |
| `internal/runtime/projectstate/restore_test.go` | test | +0/-0 | +264/-33 |
| `internal/runtime/projectstate/service.go` | production Go/support | +0/-0 | +27/-25 |
| `internal/runtime/projectstate/service_test.go` | test | +0/-0 | +83/-3 |
| `internal/runtime/projectstate/stash.go` | production Go/support | +0/-0 | +11/-9 |
| `internal/runtime/projectstate/stash_test.go` | test | +0/-0 | +18/-81 |

## Scope exclusions and mandatory stop

The implementation range contains only the approved R01-R05 tranche. It does
not implement R06-R14, lifecycle extraction, dormant Tasks 6/6A/7/8, Stage 2,
filesystem/publication-class/recovery-policy simplification, terminal-history
pruning, schema work beyond the approved workspace-revision migration, or
unrelated cleanup. Existing filesystem containment, publication modes, host
durability, automatic R10 recovery, semantic validation, and secret/privacy
boundaries remain retained.

The objective result is not **layered / no-go**: production, tests, and total
implementation are subtractive; no displaced authority remains; all retained
A01-A25 and new G01-G05 evidence is green; and coverage exceeds the floor.
Stage 1A nevertheless stops here. A new explicit human decision must name the
next authorised scope before any R06+, lifecycle, Task 6+, or Stage 2 work.
