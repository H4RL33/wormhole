# Day 14: Embedding Pipeline (Stub) + `wormhole.kb.search`

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire an embedding-generation pipeline into `wormhole.kb.write` and add `wormhole.kb.search` for semantic search via pgvector, ranked results (RFC-0001 §9, roadmap Day 14).

**Embedding provider decision (human sign-off already given, do not re-litigate):** RFC-0001 does not specify an embedding model or provider (RFC-0001 §15 open-question territory), and choosing an external API (e.g. OpenAI) would be a new external dependency requiring architecture.md R4 sign-off plus a real API key/network dependency, in tension with RFC-0001 §2.5's "self-hostable day one" framing. The controller asked the human directly: build a **pluggable `Embedder` interface with a deterministic stub implementation now**, defer the real provider choice to a later day. This ships a fully real, testable pipeline end to end (write populates `embedding`, search queries it, ranking genuinely works) without adding any new dependency or committing to a specific model/provider today. The stub's output is NOT semantically meaningful (it does not capture text meaning), it exists only to make the plumbing real and testable; this must be stated plainly in the package doc comment, not hidden.

**Architecture:** Two tasks. Task 1 defines `Embedder` and wires it into `kb.Store.WriteArticle` so every write populates `embedding`. Task 2 adds `kb.Store.SearchArticles` and `wormhole.kb.search`, which embeds the query text with the same `Embedder` and ranks by pgvector distance. `internal/core/kb` stays isolated per R2 (no import of `internal/core/tasks`/`internal/core/events`); the `Embedder` interface and stub live inside `internal/core/kb` itself, no new package.

**Stub design (fixed for both tasks, do not change mid-implementation):**
- `type Embedder interface { Embed(ctx context.Context, text string) ([]float32, error) }`
- `StubEmbedder` (or similarly named, exported): deterministic, no network call, no external dependency. Hash the input text with `sha256`, take the first 16 bytes of the digest, map each byte `b` to a `float32` via `(float32(b) - 128) / 128.0` (range roughly [-1, 1)), producing a fixed 16-dimensional vector. Same input text always produces the exact same vector (this determinism is what Task 2's tests rely on: searching with an article's own exact body text must return that article first, at distance 0, since the same text hashes to the same vector).
- Fixed dimension: **16**. This is an arbitrary placeholder dimension chosen only for this stub, not a modeling decision; state this in comments so a future real-provider swap knows the column's dimension is not load-bearing yet.
- pgvector storage: `lib/pq` has no native vector type, format the `[]float32` as a Postgres vector literal string (`fmt.Sprintf("[%s]", strings.Join(...))` style, comma-separated floats, no spaces) and cast with `::vector` in the SQL, e.g. `$N::vector`. Match whatever minimal formatting pgvector's text input format requires (`[0.1,0.2,...]`), verify by inserting and re-selecting a round-trip in a test.

**Tech Stack:** Go, Postgres + pgvector (already enabled by Day 13's migration 000009), `crypto/sha256` (stdlib, no new dependency).

## Global Constraints

- No em-dashes anywhere, including comments and commit messages.
- No ORM, no web framework, stdlib only (`crypto/sha256`, `database/sql`, `encoding/json`, etc.), no new external Go dependencies (R4).
- R2 (architecture.md §2): `internal/core/kb` still imports only `internal/types` and stdlib, no new cross-core imports.
- `RequiresAuth: true` on the new `wormhole.kb.search` tool.
- T1-T4 (architecture.md §7): real-Postgres tests, happy path + edge cases, `go build ./...` / `go vet ./...` / `go test ./...` all passing with output observed before any commit.
- Still no compliance-check logic (dedup/conciseness/required-links stay deferred, unrelated to this day's scope).
- Do not add `wormhole.kb.get` (still not scheduled).

---

### Task 1: `Embedder` interface + stub, wired into `wormhole.kb.write`

**Files:**
- Modify: `internal/core/kb/kb.go` (add `Embedder` interface, `StubEmbedder`, wire into `WriteArticle`, change `Store`/`NewStore` to take an `Embedder`)
- Modify: `internal/core/kb/kb_test.go` (update every `kb.NewStore(db)` call site for the new signature; existing Day 13 tests must keep passing)
- Modify: `internal/mcp/kb_test.go` (update any `testKBStore`-style helper for the new `NewStore` signature)
- Modify: `cmd/wormhole-server/main.go` (construct a `kb.StubEmbedder{}` and pass it to `kb.NewStore`)

**Interfaces:**
- Produces:
  - `type Embedder interface { Embed(ctx context.Context, text string) ([]float32, error) }`
  - `type StubEmbedder struct{}` implementing `Embedder` per the stub design above
  - `func NewStore(db *sql.DB, embedder Embedder) *Store` (signature change, update every call site)

- [ ] **Step 1: Add `Embedder` and `StubEmbedder` to `internal/core/kb/kb.go`**
  Exactly per the Stub design section above. Document clearly in the doc comment above `StubEmbedder` that it is NOT semantically meaningful, exists only to make the write/search pipeline real and testable, and that the real provider choice is deferred (cite this plan's decision and RFC-0001 §15).

- [ ] **Step 2: Wire embedding generation into `WriteArticle`**
  `Store` gains an `embedder Embedder` field, `NewStore` takes it as a parameter. Inside `WriteArticle`, after validating inputs but within the same transaction as the article insert, call `s.embedder.Embed(ctx, body)` (embed the article's body text, not the title, per the retrieval use case: agents search by content meaning) and format the result as a pgvector literal, include it in the `INSERT INTO kb_articles (..., embedding, ...)` statement so `embedding` is populated on every write (Day 13 left it NULL, this task ends that). If `Embed` returns an error, the whole write must still roll back atomically like any other mid-transaction failure (the existing `defer tx.Rollback()` already covers this, just make sure the embed call happens before `tx.Commit()` and its error is returned/wrapped like every other step).
  Update `articleColumns`/`scanArticle` (or wherever the row is read back) if the `Article` struct or select list needs to expose the embedding for tests to verify round-trip storage; check `internal/core/git/git.go`'s style for how it handles nullable/typed columns if a pattern is needed, but keep `Article`'s exported struct fields exactly as Day 13 defined them, unless a field is genuinely needed for this task, do not add an `Embedding []float32` field to `Article` unless Task 2 needs it (it doesn't, since `SearchArticles` in Task 2 returns ranked article rows, not the raw vectors).

- [ ] **Step 3: Update all call sites for the new `NewStore` signature**
  `cmd/wormhole-server/main.go`: `kbStore := kb.NewStore(db, kb.StubEmbedder{})`.
  `internal/core/kb/kb_test.go` and `internal/mcp/kb_test.go`: every `kb.NewStore(db)` call becomes `kb.NewStore(db, kb.StubEmbedder{})`.

- [ ] **Step 4: Tests**
  Add a test proving the embedding column is actually populated after `WriteArticle` (query `kb_articles` directly, e.g. cast `embedding::text` and assert it's non-null/non-empty, or use pgvector's text representation to confirm a 16-element vector was stored). Add a test proving determinism: writing two articles with identical body text produces identical stored embeddings (query both rows' `embedding::text` and assert equality), this is the property Task 2's search test will depend on. Confirm all existing Day 13 tests still pass unmodified in behavior (only their `NewStore` call sites change).

- [ ] **Step 5: Run full test suite**
  Run: `go build ./...`, `go vet ./...`, `go test ./...`
  Expected: PASS.

- [ ] **Step 6: Commit**
  Commit: `feat(kb): wire deterministic stub embedding pipeline into wormhole.kb.write`

---

### Task 2: `wormhole.kb.search`

**Files:**
- Modify: `internal/core/kb/kb.go` (add `SearchArticles`)
- Modify: `internal/core/kb/kb_test.go` (tests for `SearchArticles`)
- Create/Modify: `internal/mcp/kb.go` (add `SearchArticlesTool`)
- Modify: `internal/mcp/kb_test.go` (tests for the tool)
- Modify: `cmd/wormhole-server/main.go` (register the new tool)

**Interfaces:**
- Produces:
  - `func (s *Store) SearchArticles(ctx context.Context, projectID, agentID, query string, limit int) ([]Article, error)`
  - `func SearchArticlesTool(store *kb.Store) Tool`

- [ ] **Step 1: `SearchArticles` in `internal/core/kb/kb.go`**
  Single tx (or a plain query if no write is involved, match whichever style `events.ListEvents` uses for read-only project-scoped queries, since this is a read not a write): `SET LOCAL wormhole.project_id`, passport check (same block as `WriteArticle`, reuse rather than re-derive), embed the query text via `s.embedder.Embed(ctx, query)`, then `SELECT ... FROM kb_articles WHERE project_id = $1 ORDER BY embedding <=> $2::vector LIMIT $3` (cosine distance operator `<=>`, ascending, nearest first; if pgvector's cosine operator requires normalized vectors and the stub doesn't guarantee that, use `<->` (Euclidean/L2) instead, whichever is simpler and correct for arbitrary un-normalized float vectors, document which operator was chosen and why in a code comment). Default `limit` to a sane value (e.g. 10) if zero/unset, matching `events.ListEvents`'s existing default-limit pattern from Day 10 if one exists (check `internal/mcp/channel.go`'s `SubscribeChannelInput` default-limit handling and mirror it).
  Exclude rows where `embedding IS NULL` from the ranking (defensive, in case any pre-Task-1 row somehow lacks one, though after Task 1 every new write populates it).

- [ ] **Step 2: `SearchArticlesTool` in `internal/mcp/kb.go`**
  Tool name per RFC-0001 §9: `wormhole.kb.search`. Input: `Query string` (json tag `query`), `Limit int` (json tag `limit`, optional). Output: list of results, each with at minimum `ArticleID`, `Title`, `Body` (mirror `ListTasksOutput`'s list-of-results shape/style from `internal/mcp/task.go`). `RequiresAuth: true`. Error wrap `mcp: wormhole.kb.search: %w`.

- [ ] **Step 3: Register in `cmd/wormhole-server/main.go`**
  Register `mcp.SearchArticlesTool(kbStore)` alongside the existing KB tool.

- [ ] **Step 4: Tests**
  `internal/core/kb/kb_test.go`: write two or three articles with distinct body text, then search using one article's exact body text as the query and assert that article is ranked first (distance 0, per the stub's determinism proven in Task 1). Test the `limit` parameter actually caps results. Test cross-project isolation (search in project A never returns project B's articles), matching the existing RLS-isolation-test pattern with a restricted non-owner role.
  `internal/mcp/kb_test.go`: one happy-path test for `wormhole.kb.search` through the tool handler, mirroring `internal/mcp/kb_test.go`'s existing `wormhole.kb.write` test style.

- [ ] **Step 5: Run full test suite**
  Run: `go build ./...`, `go vet ./...`, `go test ./...`
  Expected: PASS.

- [ ] **Step 6: Commit**
  Commit: `feat(kb): wire wormhole.kb.search (pgvector-ranked, stub embeddings)`
