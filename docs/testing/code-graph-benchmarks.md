# Code Graph benchmark corpus

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

## Actual-checkout run: baseline not established

Run on 2026-07-26 with:

- source `HEAD`: `6de2f67646416786cac346a0b9e48b6318569b54`
- source `HEAD` tree: `6a11c70b0a24971d694113391d23a2a66144ed58`
- worktree: 73 modified/deleted entries and 45 untracked entries
- Go: `go1.26.5-X:nodwarf5 linux/amd64`
- kernel: `Linux 7.0.12-2-cachyos-hardened-lto x86_64`
- CPU: `AMD Ryzen 5 5600 6-Core Processor`

The exact default command failed before querying:

```text
BenchmarkWormholeCodeGraphCorpus
    benchmark_test.go:206: build Wormhole benchmark graph: codegraph golang: package loading failed: <checkout>/cmd/fabric/main.go:52:19: undefined: newFabricEmbedder
--- FAIL: BenchmarkWormholeCodeGraphCorpus
FAIL
```

This is meaningful tracked-only evidence, not a benchmark defect. The modified
tracked `cmd/fabric/main.go` refers to untracked `cmd/fabric/embedding_wiring.go`;
the inventory correctly excludes the untracked implementation. No result was
fabricated by indexing untracked files, and no clean Gate B baseline is claimed
for the shared worktree. The baseline remains conditional on intentionally
committing a coherent cumulative source set.

## Prepared-snapshot development run

For development-only diagnosis, all current files except `.git` and the local
`wormhole-server` binary were copied into an isolated Git repository and
intentionally tracked there. The exact isolated identity was:

- temporary checkout: `/tmp/tmp.xOzb82UCt6`
- commit: `b6994c6f2f7e3910924714239f56f1c0e3874fc1`
- tree fingerprint: `9d074c6785c4c0646c15a1217aa81247ea0fc6d2`
- isolated tree state: clean (zero porcelain entries)
- graph revision: `task-12-wormhole-corpus`
- environment: `linux/amd64`, `go1.26.5-X:nodwarf5`

The reporting run used:

```bash
WORMHOLE_CODEGRAPH_BENCHMARK_CHECKOUT=/tmp/tmp.xOzb82UCt6 \
go test ./internal/runtime/codegraph/query \
  -run '^$' \
  -bench '^BenchmarkWormholeCodeGraphCorpus$' \
  -benchtime=1x \
  -count=1 \
  -v
```

Raw measured outcomes from that run:

| Query | Duration (ns) | Source bytes | Irrelevant bytes | Omitted nodes / edges | Selected files / matches | Result |
|---|---:|---:|---:|---:|---:|---|
| agent-registration-authentication | 285978607 | 16380 | 5518 | 794 / 1072 | 26 / 64 | sufficient |
| task-status-event | 233813093 | 16342 | 7674 | 2914 / 1212 | 39 / 32 | sufficient |
| gateway-sync-response-version | 335246002 | 16373 | 2629 | 896 / 1009 | 27 / 64 | incomplete |
| passport-audit-writes | 318767022 | 16377 | 8404 | 958 / 995 | 29 / 64 | sufficient |
| local-task-durable-queue | 254803548 | 16376 | 11847 | 2866 / 1078 | 37 / 34 | sufficient |
| local-sqlite-project-isolation | 336831979 | 16340 | 12997 | 1794 / 1272 | 45 / 64 | incomplete |

All six query results reported `partial` with
`omission_reason=match_budget_exhausted`; those raw completeness signals are
retained even where the expected answer artifacts were sufficient. The
prepared run produced no `useless` result. Two outcomes were explicitly
incomplete:

- `gateway-sync-response-version` missed relationship segment
  `pushBatch calls decodeIncrementalPushResult` and authoritative edges
  `Bootstrap calls validateBootstrapResult` and
  `pushBatch calls decodeIncrementalPushResult`.
- `local-sqlite-project-isolation` missed relationship segments
  `GetTask calls queryTask` and `ListPending uses_type QueueEntry`, plus
  authoritative expectations `GetTask calls queryTask` and
  `ListPending uses QueueEntry`.

Every expected file was selected in all six prepared-snapshot outcomes, and no
heuristic expectation was missing. These facts do not turn the prepared
snapshot into the actual-checkout baseline and do not establish a performance
threshold.

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
