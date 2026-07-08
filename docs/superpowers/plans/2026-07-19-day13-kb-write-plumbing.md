# Day 13: KB Schema + `wormhole.kb.write` Plumbing

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land the real `kb_articles`/`kb_links` migration (RFC-0001 §8.3, `docs/kb-schema.md`'s draft) and wire `wormhole.kb.write` as plumbing only, no compliance checks (dedup/conciseness/required-links), no embedding generation. Both are explicitly Day 13 roadmap scope boundaries: compliance checks are deferred (RFC-0001 §15 open-question territory per `docs/kb-schema.md`), and embedding generation is Day 14's item ("Embedding generation pipeline on write").

**Architecture:** This is the Knowledge Base pillar's first code (`internal/core/kb` is currently a stub, `doc.go` with just a package declaration). Mirrors `internal/core/events`'s shape exactly: `Store`, `NewStore(db *sql.DB)`, one method per operation, `SET LOCAL wormhole.project_id` + passport-check pattern, single tx, `RETURNING` + scan, sentinel errors. `internal/core/kb`'s allowed imports are already fixed in architecture.md's module map (`internal/types`, stdlib) — do not add `internal/core/tasks` or `internal/core/events` imports.

**Scope boundary (do not build ahead of the roadmap):** `wormhole.kb.get` and `wormhole.kb.search` are RFC-0001 §9's other two KB tools but are NOT part of Day 13 (`wormhole.kb.search` is explicitly Day 14's item; `wormhole.kb.get` isn't scheduled yet). Build only `WriteArticle`/`wormhole.kb.write` this task. No compliance-check logic (dedup similarity, length ceiling, required-link enforcement) — an article write always succeeds if its inputs are well-formed and its link targets exist, exactly like `events.PublishEvent` never validates payload *content*, only shape and referential existence.

**Tech Stack:** Go, Postgres + pgvector (already the running image, `pgvector/pgvector:pg16`, extension not yet enabled in any migration).

## Global Constraints

- All MCP handlers follow the pattern in `internal/mcp/channel.go` / `internal/mcp/git.go` exactly.
- `RequiresAuth: true` on the new tool.
- No em-dashes (commas, colons, semicolons, parentheses instead).
- No ORM, no web framework, stdlib `database/sql` only.
- R2 (architecture.md §2): `internal/core/kb` imports only `internal/types` and stdlib, matching its already-fixed module-map row. Do not import `internal/core/events` or `internal/core/tasks`.
- T1-T4 (architecture.md §7): real-Postgres tests (`t.Skipf` if unreachable), happy path + each sentinel error + isolation/RLS test, `go build ./...` / `go vet ./...` / `go test ./...` all passing with output observed before any commit.
- `embedding` column: pgvector's `vector` type without a fixed dimension (no model/dimension has been chosen yet, that's Day 14's concern when the embedding generation pipeline picks a model). Leave it nullable, unpopulated by this task, and flag in the migration's header comment that the dimension is deliberately unspecified pending Day 14. Do not guess a dimension (e.g. 1536) and hardcode it now.

---

### Task 1: KB Schema Migration + Core Write Plumbing + MCP Tool

**Files:**
- Create: `migrations/000009_kb_articles.up.sql`, `migrations/000009_kb_articles.down.sql`
- Modify: `internal/core/kb/doc.go` or create `internal/core/kb/kb.go` (package currently has only a bare `doc.go`, decide during implementation whether the package doc comment belongs in `doc.go` or at the top of `kb.go`, matching whichever convention `internal/core/events`/`internal/core/git` actually use, check both before choosing)
- Create: `internal/core/kb/kb_test.go`
- Create: `internal/mcp/kb.go`
- Create: `internal/mcp/kb_test.go`
- Modify: `cmd/wormhole-server/main.go` (register the new tool)

**Interfaces:**
- Produces:
  - `func NewStore(db *sql.DB) *Store` (package `kb`)
  - `type Article struct { ID, ProjectID, Title, Body string; Frontmatter json.RawMessage; AuthorAgentID string; CreatedAt, UpdatedAt time.Time }` (no `Embedding` field yet, Day 14 adds it once the pipeline exists, don't stub an unused field now)
  - `func (s *Store) WriteArticle(ctx context.Context, projectID, agentID, title, body string, frontmatter json.RawMessage, linkTargetIDs []string) (Article, error)`
  - `func WriteArticleTool(store *kb.Store) Tool`

- [ ] **Step 1: Migration**
  Follow `migrations/000007_event_channels.up.sql`'s exact style: header comment citing RFC-0001 §8.3, `docs/kb-schema.md`, and this migration's number; enable the extension (`CREATE EXTENSION IF NOT EXISTS vector;`, matching Day 1's `CREATE EXTENSION IF NOT EXISTS pgcrypto;` placement/style in `migrations/000001_init_schema.up.sql`); then the two tables.
  `kb_articles` per `docs/kb-schema.md` verbatim: `id` (uuid PK, `gen_random_uuid()`), `project_id` (uuid NOT NULL, FK `projects(id)` ON DELETE CASCADE), `title` (text NOT NULL), `body` (text NOT NULL), `frontmatter` (jsonb NOT NULL DEFAULT `'{}'`), `embedding` (`vector`, nullable, no dimension, comment inline flagging this is deliberately unspecified pending Day 14's model choice), `author_agent_id` (uuid NOT NULL, FK `agents(id)` ON DELETE CASCADE), `created_at`, `updated_at` (timestamptz NOT NULL DEFAULT now()).
  `kb_links` per `docs/kb-schema.md` verbatim: `id` (uuid PK), `project_id` (uuid NOT NULL, FK `projects(id)` ON DELETE CASCADE, same D3/RLS deviation rationale as `task_links`/`git_links`, note it in a comment same as `migrations/000006_task_graph.up.sql`'s task_links comment does), `from_article_id` (uuid NOT NULL, FK `kb_articles(id)` ON DELETE CASCADE), `to_article_id` (uuid NOT NULL, FK `kb_articles(id)` ON DELETE CASCADE), `created_at` (timestamptz NOT NULL DEFAULT now()).
  Indexes: `project_id` on both tables, `from_article_id` on `kb_links`. RLS enabled on both with the standard `project_id = current_setting('wormhole.project_id', true)::uuid` policy, verbatim style. `down.sql` drops both tables (and does NOT drop the `vector` extension, matching how `pgcrypto` is never dropped by any existing down migration, extensions are shared/system-level, not per-feature).
  Apply and verify with the real `migrate` CLI (`/home/harley/go/bin/migrate -path migrations -database "postgres://wormhole:wormhole@127.0.0.1:5432/wormhole?sslmode=disable" up`, then `down 1`, then `up` again) to confirm the pair round-trips cleanly, the same verification the controller ran for Day 11's git_links migration.

- [ ] **Step 2: `internal/core/kb` write plumbing**
  Mirror `internal/core/events/events.go`'s `PublishEvent` shape: single tx, `SELECT set_config('wormhole.project_id', $1, true)`, passport check (copy the exact block from `events.PublishEvent`, same query/error style), `INSERT INTO kb_articles (...) VALUES (...) RETURNING ...`, scan into `Article`.
  For each ID in `linkTargetIDs`, within the SAME tx: verify the target article exists in-project (raw SQL `SELECT 1 FROM kb_articles WHERE id = $1 AND project_id = $2`, matching the existing-entity-check pattern used everywhere else), then `INSERT INTO kb_links (project_id, from_article_id, to_article_id) VALUES (...)`. If any link target doesn't exist, return `ErrLinkedArticleNotFound` and let `defer tx.Rollback()` undo the whole write, no partial article with dangling/missing links.
  Sentinel errors: `ErrPassportNotFound` (naming precedent from `tasks`/`git`), `ErrLinkedArticleNotFound`.
  Do not implement any compliance check (no similarity/length/required-link enforcement). An empty `linkTargetIDs` slice is valid, RFC-0001 doesn't require every article to link somewhere, that's a Day-13-out-of-scope compliance concern (`docs/kb-schema.md`'s "required links... depending on article type" section), not this task's job.

- [ ] **Step 3: `internal/mcp/kb.go`**
  Mirror `internal/mcp/git.go`. Tool name per RFC-0001 §9 verbatim: `wormhole.kb.write`.
  `WriteArticleInput`: `Title string`, `Body string`, `Frontmatter json.RawMessage` (optional, default `{}` if omitted, matching RFC-0001 §9's `wormhole.kb.write(title, body, links[])` core args plus the frontmatter field `docs/kb-schema.md` already establishes as part of the entity), `Links []string` (json tag `links`, matching RFC-0001 §9's argument name exactly).
  `WriteArticleOutput`: `ArticleID string`, `ProjectID string`, `Title string`, `CreatedAt time.Time` (mirror `CreateChannelOutput`'s shape/style).
  `RequiresAuth: true`. Errors wrapped `fmt.Errorf("mcp: wormhole.kb.write: %w", err)`.

- [ ] **Step 4: Register in `cmd/wormhole-server/main.go`**
  Add `kbStore := kb.NewStore(db)`, register `mcp.WriteArticleTool(kbStore)` alongside the existing tool registrations.

- [ ] **Step 5: Tests**
  `internal/core/kb/kb_test.go`: mirror `internal/core/git/git_test.go`'s helper pattern (`testDB(t)`, `t.Skipf` if Postgres unreachable). Cover: `WriteArticle` happy path with zero links (row persisted, correct fields, `embedding` NULL), happy path with one or more valid links (both the article and the `kb_links` rows persisted correctly), `WriteArticle` with an unknown link target returns `ErrLinkedArticleNotFound` AND leaves no partial article row behind (query `kb_articles` directly to confirm zero rows for that title, proving the whole-tx rollback), unregistered/no-passport agent returns `ErrPassportNotFound`, cross-project isolation test (an article from project A is invisible under project B's RLS context, matching the pattern from `internal/core/git/git_test.go`'s isolation test using a non-owner restricted role, not the table-owner connection).
  `internal/mcp/kb_test.go`: mirror `internal/mcp/git_test.go`'s pattern, one happy-path test for the tool (with and without links) and one error-surfacing test (`TestKBTools_WriteArticleUnknownLink` or similar).

- [ ] **Step 6: Run full test suite**
  Run: `go build ./...`, `go vet ./...`, `go test ./...`
  Expected: PASS.

- [ ] **Step 7: Commit**
  Commit: `feat(kb): wire wormhole.kb.write plumbing (no compliance checks, no embeddings yet)`
