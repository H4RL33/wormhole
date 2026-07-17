# TODO — Known Non-Blocking Issues

Minor findings surfaced by task/final reviews during the subagent-driven-development
loop. None blocked their originating day; tracked here so they aren't lost. Source:
`.superpowers/sdd/progress.md`.

## Test infrastructure

- **Flaky `TestRLSIsolation`** — `internal/core/tasks`. Fails intermittently under
  full-suite concurrent execution with `pq: tuple concurrently updated (XX000)`.
  Passes reliably in isolation (`go test ./internal/core/tasks/... -run TestRLSIsolation`).
  Confirmed pre-existing as of Day 18, unrelated to any diff since. Root cause not
  investigated — likely Postgres row-lock contention between parallel test
  transactions sharing a fixture, not an RLS policy bug. Needs a look before alpha
  hardening (Day 23).

## Code quality

- **`internal/core/kb/kb.go`** — unchecked `rows.Err()` in the dedup/suggestion
  retrieval path (flagged Day 16 Task 3 review).
- **`internal/core/kb`** — `ErrPassportNotFound` is wrapped (`fmt.Errorf`) while
  `ErrArticleNotFound` is returned bare in `GetArticleLinks`; both are
  `errors.Is`-safe, but the inconsistency is an inherited pattern worth a uniformity
  pass later (flagged Day 17 Task 2 review).
- **`cmd/wormhole-cli/main.go`** — on a `wormhole join` server error, the raw JSON
  error string from `CallResponse.Error` (e.g.
  `{"error":"...","code":"..."}`) is printed to stderr unparsed rather than
  extracted into a clean human message. Cosmetic: against the real server the
  error is already a plain wrapped string (`internal/mcp/server.go`'s `err.Error()`
  path), not JSON, so this is lower priority than it first looked (flagged Day 19
  Task 1 review, refined by Day 19 final review).

## Future work

- **Mid-session model switching not tracked** — Agent Identity's `Model` field
  (RFC-0001 §8.4) is a point-in-time snapshot from `join`/`connect`
  registration; a harness-side model switch (e.g. Claude Code `/model`)
  mid-session doesn't update it. RFC-0001's `Sessions` field would be the
  natural home for per-session model tracking, but no `Sessions` entity
  exists yet (schema/storage work, not scoped here — flagged during CLI
  consolidation design, `docs/superpowers/specs/2026-07-18-cli-consolidation-design.md`).

## Test coverage gaps

- **`internal/core/kb`** — `GetArticle_CrossProjectIsolation` lacks the sanity
  sub-cases (no-context raw query + project-A sanity check) present in
  `TestWriteArticle_CrossProjectIsolation`. Not blocking; existing test covers the
  same property at the policy level (flagged Day 17 Task 1 review).
- **`internal/core/kb`** — no dedicated cross-project isolation test for the
  suggestion-retrieval path added in Day 16 (flagged Day 16 Task 3 review).
