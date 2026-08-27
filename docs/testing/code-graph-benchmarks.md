# Code Graph benchmark corpus

**Status:** preserved internal-package benchmark and historical Gate B method;
not a current public command, MCP tool, setup step, or release-trial procedure.

This is the Task 12 / Gate B measurement procedure for the Wormhole Code Graph
alpha. It records observations; it defines no latency, byte, recall, or release
threshold.

## Checked-in corpus and runner

The ordered six-query corpus is
`testdata/codegraph/benchmark-corpus.json`. Every record contains the exact
question, expected entry symbols, expected files, expected relationship path,
separate authoritative and heuristic edge expectations, and a prose
sufficiency criterion.

The reproducible runner is `BenchmarkWormholeCodeGraphCorpus` in
`internal/runtime/codegraph/query/benchmark_test.go`:

```bash
go test ./internal/runtime/codegraph/query \
  -run '^$' \
  -bench '^BenchmarkWormholeCodeGraphCorpus$' \
  -benchtime=1x \
  -count=1 \
  -v
```

The default input is the actual checkout. The runner uses the production Git
tracked-file inventory and Go loader, builds one local SQLite graph, and emits
the complete structured result between `TASK12_BENCHMARK_JSON_BEGIN` and
`TASK12_BENCHMARK_JSON_END`. Each outcome includes duration, source and
irrelevant-source bytes, omissions, completeness, selected files, matched
symbols, all observed authoritative and heuristic edges, every missing
expectation, and `sufficient`, `incomplete`, or `useless` classification.

`WORMHOLE_CODEGRAPH_BENCHMARK_CHECKOUT` may point at an explicitly prepared
checkout for development diagnosis. It is never an implicit fallback and must
not be presented as the clean baseline for a different checkout.

## Clean tracked-checkout baseline

Run on 2026-07-26 with:

- source `HEAD`: `471ef700366836e282f77e689706df917b1bbe1b`
- source `HEAD` tree: `005ef72d9f12ea861796955083ac35fe9fb959c3`
- tracked worktree: clean detached worktree at the source commit
- Go: `go1.26.5-X:nodwarf5 linux/amd64`
- kernel: `Linux 7.0.12-2-cachyos-hardened-lto x86_64`
- CPU: `AMD Ryzen 5 5600 6-Core Processor`

The reporting run used the exact default benchmark command shown above from a
clean detached worktree. Raw measured outcomes were:

| Query | Duration (ns) | Source bytes | Irrelevant bytes | Omitted nodes / edges | Selected files / matches | Result |
|---|---:|---:|---:|---:|---:|---|
| agent-registration-authentication | 229004925 | 16380 | 5518 | 797 / 1072 | 26 / 64 | sufficient |
| task-status-event | 193672691 | 16353 | 12642 | 2881 / 1124 | 33 / 29 | sufficient |
| gateway-sync-response-version | 256354276 | 16373 | 2629 | 901 / 1013 | 27 / 64 | incomplete |
| passport-audit-writes | 263865492 | 16377 | 8404 | 966 / 999 | 29 / 64 | sufficient |
| local-task-durable-queue | 194628936 | 16376 | 11847 | 2828 / 1036 | 35 / 35 | sufficient |
| local-sqlite-project-isolation | 261086615 | 16340 | 12997 | 1766 / 1284 | 45 / 64 | incomplete |

All six query results reported `partial` with
`omission_reason=match_budget_exhausted`; those raw completeness signals are
retained even where the expected answer artifacts were sufficient. The
baseline produced no `useless` result. Two outcomes were explicitly
incomplete:

- `gateway-sync-response-version` missed relationship segment
  `pushBatch calls decodeIncrementalPushResult` and authoritative edges
  `Bootstrap calls validateBootstrapResult` and
  `pushBatch calls decodeIncrementalPushResult`.
- `local-sqlite-project-isolation` missed relationship segments
  `GetTask calls queryTask` and `ListPending uses_type QueueEntry`, plus
  authoritative expectations `GetTask calls queryTask` and
  `ListPending uses QueueEntry`.

Every expected file was selected in all six outcomes, and no heuristic
expectation was missing. These measurements establish no performance
threshold.

Before commit `471ef70`, the default runner correctly failed because tracked
`cmd/fabric/main.go` referenced the then-untracked
`cmd/fabric/embedding_wiring.go`. That failure remains useful evidence that the
inventory never widened to untracked source. It was resolved by committing the
coherent cumulative source set, not by weakening tracked-only indexing.

## Interpretation rules

- `sufficient` requires a symbol match, at least one expected file, every
  expected file, every relationship segment, and every provenance-specific
  expected edge.
- `incomplete` preserves useful context but records any missing expected file,
  path segment, authoritative edge, or heuristic edge.
- `useless` means no symbol matched or no expected file was selected.
- Query-level `partial`/omission data is recorded independently of the
  answer-sufficiency classification.
- Repeated measurements must retain failures, incomplete calls, and useless
  calls. They must not filter them out or backfill thresholds after seeing the
  data.

Fresh-model use, dogfood reduction in broad source reading, and two-model or
two-harness comparison are later validation work. Task 12 does not claim them.
