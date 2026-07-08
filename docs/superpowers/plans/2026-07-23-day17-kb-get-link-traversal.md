# Day 17 — KB Get and Link Graph Traversal

**Date:** 2026-07-23
**Roadmap items:**
- `wormhole.kb.get` — article retrieval by ID
- `[[link]]` resolution / graph traversal between articles

**Source:** ROADMAP.md Day 17; RFC-0001 §8.3 (Knowledge Base); architecture.md §6 (Knowledge Base section).

---

## Context

M3 KB write/search pipeline is complete (Days 13–16):
- `wormhole.kb.write` stores articles with embeddings and compliance checks (dedup, conciseness, required links).
- `wormhole.kb.search` returns semantically ranked articles.
- `kb_links` graph edges are stored on write (from_article_id → to_article_id).

What's missing: direct retrieval by ID and the ability to traverse the link graph between articles. Day 17 delivers both. Day 18 closes M3 with an integration test and review.

**Key files:**
- `internal/core/kb/kb.go` — Store, Article struct (canonical shape to follow)
- `internal/core/kb/kb_test.go` — test pattern (testDB helper, full Postgres)
- `internal/mcp/kb.go` — MCP tools for KB (WriteArticleTool, SearchArticlesTool patterns)
- `internal/mcp/registry.go` — tool registration
- `cmd/wormhole-server/main.go` — where tools are wired
- `migrations/000009_kb_articles.up.sql` — kb_articles + kb_links schema

---

## Global Constraints

These apply to ALL tasks in this plan and must be enforced verbatim:

1. **No new Go dependencies** (R4). No ORM, no web framework, stdlib net/http only. `lib/pq` and existing dependencies only.
2. **No new top-level packages** (R4). Work within `internal/core/kb` and `internal/mcp`.
3. **Project isolation required** (D3, RFC-0001 §13). Every DB query sets `wormhole.project_id` session config and relies on RLS. Cross-project isolation tests are mandatory where new data access is introduced (T3).
4. **No schema changes** — the `kb_articles` and `kb_links` tables are already correct. No new migrations needed for these tasks.
5. **Pattern fidelity** — follow `internal/core/identity/identity.go` (Store struct, sentinel errors, wrapped errors, `context.Context` first param, hand-written SQL). Match exactly the shapes already in `internal/core/kb/kb.go`.
6. **MCP tool naming**: `wormhole.kb.get`, `wormhole.kb.get_links` (verb grammar from architecture.md M2).
7. **Tests against real Postgres** (T1, T2, T3). No mocked DB. Mirror the existing `testDB(t)` helper pattern from `internal/core/kb/kb_test.go`.
8. **Passport verification**: every store method verifies the calling agent has a passport for the project, matching `WriteArticle` and `SearchArticles`.
9. **Sentinel error for not-found**: `kb.ErrArticleNotFound` (new sentinel, message `"kb: article not found"`).
10. **architecture.md §0.7**: Do not claim done without running `go build ./...`, `go vet ./...`, and `go test ./...` and reading the output. Paste decisive lines in the report.

---

## Task 1: `wormhole.kb.get` — Article Retrieval by ID

**Task sentence:** Done when `Store.GetArticle` fetches one article by ID within the calling agent's project scope, and the `wormhole.kb.get` MCP tool exposes it with auth, and tests cover happy path + not-found + cross-project isolation.

### Store method

Add to `internal/core/kb/kb.go`:

```go
var ErrArticleNotFound = errors.New("kb: article not found")

// GetArticle retrieves a single KB article by ID within the calling agent's
// project scope (RFC-0001 §8.3). Returns ErrArticleNotFound if the article
// does not exist or belongs to a different project. Returns
// ErrPassportNotFound if the agent has no passport for this project.
func (s *Store) GetArticle(ctx context.Context, projectID, agentID, articleID string) (Article, error)
```

Implementation shape (mirror SearchArticles/WriteArticle exactly):
1. Begin tx.
2. `SET LOCAL wormhole.project_id` via `set_config`.
3. Verify agent passport for project (same query as SearchArticles).
4. `SELECT <articleColumns> FROM kb_articles WHERE id = $1 AND project_id = $2` — if `sql.ErrNoRows`, return `ErrArticleNotFound` (not wrapped — callers match with `errors.Is`). Other errors: wrapped.
5. Commit. Return article.

New sentinel: `var ErrArticleNotFound = errors.New("kb: article not found")` — add near the other sentinel errors at the top of kb.go.

### MCP tool

Add to `internal/mcp/kb.go`:

```go
type GetArticleInput struct {
    ArticleID string `json:"article_id"`
}

type GetArticleOutput struct {
    ArticleID     string          `json:"article_id"`
    ProjectID     string          `json:"project_id"`
    Title         string          `json:"title"`
    Body          string          `json:"body"`
    Frontmatter   json.RawMessage `json:"frontmatter,omitempty"`
    AuthorAgentID string          `json:"author_agent_id"`
    CreatedAt     time.Time       `json:"created_at"`
    UpdatedAt     time.Time       `json:"updated_at"`
}

func GetArticleTool(store *kb.Store) Tool
```

Handler: decode input, call `store.GetArticle`, map Article → GetArticleOutput. On `kb.ErrArticleNotFound` or `kb.ErrPassportNotFound`: propagate with `fmt.Errorf("mcp: wormhole.kb.get: %w", err)`.

Register in `internal/mcp/registry.go` and wire in `cmd/wormhole-server/main.go` alongside the existing KB tools.

### Tests

Add to `internal/core/kb/kb_test.go` (new test functions, not new file):

- `TestGetArticle_HappyPath`: write an article via WriteArticle (or direct INSERT to avoid compliance overhead), then GetArticle by ID — verify all fields match.
- `TestGetArticle_NotFound`: call GetArticle with a random UUID — expect ErrArticleNotFound.
- `TestGetArticle_CrossProjectIsolation`: write an article under project A, call GetArticle from project B's scoped context — expect ErrArticleNotFound (RLS blocks it). Follow the existing restricted-role pattern from the isolation tests already in kb_test.go.
- `TestGetArticle_NoPassport`: call GetArticle with an agent that has no passport — expect ErrPassportNotFound.

Add to `internal/mcp/kb_test.go` (existing file):
- One HTTP-level test: write an article, then call `wormhole.kb.get` with a valid token — verify the returned JSON contains the article fields. Mirror the pattern in the existing MCP KB tests.

---

## Task 2: `[[link]]` Resolution / Graph Traversal

**Task sentence:** Done when `Store.GetArticleLinks` returns the outbound links of an article (as a slice of linked Articles), and the `wormhole.kb.get_links` MCP tool exposes it with auth, and tests cover happy path (article with links) + empty links + not-found article + cross-project isolation.

### What "[[link]] resolution" means here

RFC-0001 §8.3 describes `[[link]]` as explicit graph edges between articles. The roadmap item "[[link]] resolution / graph traversal" means: given an article ID, return the articles it links to (the outbound neighbors in the graph, one hop). The `kb_links` table already stores these edges (`from_article_id → to_article_id`). This task reads them.

**Scope boundary:** One-hop outbound traversal only. Full multi-hop traversal is not in scope and would be Day 17+ feature creep. The RFC does not mandate multi-hop in the MVP.

### Store method

Add to `internal/core/kb/kb.go`:

```go
// GetArticleLinks returns the articles that the given article links to
// (one-hop outbound traversal of the kb_links graph, RFC-0001 §8.3).
// Returns ErrArticleNotFound if the source article does not exist in this
// project. Returns an empty slice (not nil) if the article has no outbound
// links. Returns ErrPassportNotFound if the agent has no passport for this
// project.
func (s *Store) GetArticleLinks(ctx context.Context, projectID, agentID, articleID string) ([]Article, error)
```

Implementation shape:
1. Begin tx.
2. `set_config` wormhole.project_id.
3. Verify agent passport.
4. Verify the source article exists in-project (`SELECT 1 FROM kb_articles WHERE id = $1 AND project_id = $2`). If `sql.ErrNoRows`, return `ErrArticleNotFound`.
5. Query:
   ```sql
   SELECT a.id, a.project_id, a.title, a.body, a.frontmatter, a.author_agent_id, a.created_at, a.updated_at
   FROM kb_articles a
   JOIN kb_links l ON l.to_article_id = a.id
   WHERE l.from_article_id = $1 AND l.project_id = $2
   ORDER BY a.created_at ASC
   ```
   (Both tables are RLS-protected by wormhole.project_id; `l.project_id = $2` is defense-in-depth matching the pattern of defensive WHERE clauses already in the KB package.)
6. Scan into `[]Article`. If zero rows, return `[]Article{}` (not nil), no error.
7. Commit. Return slice.

### MCP tool

Add to `internal/mcp/kb.go`:

```go
type GetArticleLinksInput struct {
    ArticleID string `json:"article_id"`
}

type GetArticleLinksOutput struct {
    ArticleID string           `json:"article_id"`
    Links     []ArticleSummary `json:"links"` // reuse existing ArticleSummary
}

func GetArticleLinksTool(store *kb.Store) Tool
```

Handler: decode input, call `store.GetArticleLinks`, map to output. On `ErrArticleNotFound` / `ErrPassportNotFound`: propagate with `fmt.Errorf("mcp: wormhole.kb.get_links: %w", err)`.

Register in `internal/mcp/registry.go` and wire in `cmd/wormhole-server/main.go`.

### Tests

Add to `internal/core/kb/kb_test.go`:

- `TestGetArticleLinks_HappyPath`: write two articles (A and B), write A with B as a link target, call GetArticleLinks(A's ID) — verify returned slice contains B with correct fields.
- `TestGetArticleLinks_NoLinks`: write an article with no link targets, call GetArticleLinks — verify empty slice returned, no error.
- `TestGetArticleLinks_ArticleNotFound`: call GetArticleLinks with a random UUID — expect ErrArticleNotFound.
- `TestGetArticleLinks_CrossProjectIsolation`: write linked articles under project A, call GetArticleLinks from project B's scoped context on A's article ID — expect ErrArticleNotFound. This verifies the graph traversal respects RLS on both the source article check and the join.
- `TestGetArticleLinks_NoPassport`: call GetArticleLinks with agent lacking a passport — expect ErrPassportNotFound.

Add to `internal/mcp/kb_test.go`:
- One HTTP-level test: write two linked articles, call `wormhole.kb.get_links` — verify the links slice in the response contains the target article.

---

## Commit Guidelines

- Task 1 commits: `feat(kb): add GetArticle store method and wormhole.kb.get MCP tool`
- Task 2 commits: `feat(kb): add GetArticleLinks store method and wormhole.kb.get_links MCP tool`
- May use more granular commits (e.g., separate store + MCP commits per task) if that's cleaner.
- Conventional Commits format, ≤ 50 char subject.

---

## Verification (T4)

Before reporting DONE:

```sh
go build ./...
go vet ./...
go test ./internal/core/kb/... ./internal/mcp/... -v -count=1
```

Paste the final summary line (pass count, any failures) in the report.
