# Knowledge Base Semantic Deduplication Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the semantic deduplication check (dedup check) for Knowledge Base article contributions, rejecting duplicate writes with a structured JSON error unless bypassed via the `force` flag.

**Architecture:** Add `KBDedupThreshold` to `types.Config` (defaulting to 0.85). Update `kb.NewStore` and `kb.Store.WriteArticle` signatures to pass the threshold and the `force` flag. Query the nearest article in the same project using pgvector cosine distance, returning a custom `ErrDedupViolation` on similarity threshold violations. Format the error as a structured JSON object at the MCP boundary.

**Tech Stack:** Go, Postgres + pgvector, standard library only (`crypto/sha256`, `database/sql`, `encoding/json`).

## Global Constraints

- No em-dashes anywhere, including comments and commit messages.
- No ORM, no web framework, stdlib only, no new external Go dependencies (R4).
- R2 (architecture.md §2): `internal/core/kb` still imports only `internal/types` and stdlib, no new cross-core imports.
- T1-T4 (architecture.md §7): real-Postgres tests, happy path + edge cases, `go build ./...` / `go vet ./...` / `go test ./...` all passing with output observed before any commit.

---

### Task 1: Update Config and NewStore Signatures

**Files:**
- Modify: `internal/types/config.go` (add `KBDedupThreshold`)
- Modify: `internal/core/kb/kb.go` (update `Store` struct and `NewStore` signature)
- Modify: `internal/core/kb/kb_test.go` (update `kb.NewStore` calls)
- Modify: `internal/mcp/kb_test.go` (update `kb.NewStore` calls)
- Modify: `cmd/wormhole-server/main.go` (load threshold and pass to `kb.NewStore`)

**Interfaces:**
- Consumes: none
- Produces:
  - `types.Config.KBDedupThreshold` (float64)
  - `func NewStore(db *sql.DB, embedder Embedder, dedupThreshold float64) *Store`

- [ ] **Step 1: Add configuration to `internal/types/config.go`**
  Modify `internal/types/config.go` to add `KBDedupThreshold` to the `Config` struct. Parse it from `WORMHOLE_KB_DEDUP_THRESHOLD` as a float64 using `strconv.ParseFloat` with fallback to `0.85`.
  Ensure `strconv` is added to imports.
  ```go
  package types

  import (
  	"os"
  	"strconv"
  )

  type Config struct {
  	ListenAddr       string
  	DatabaseURL      string
  	KBDedupThreshold float64
  }

  func LoadConfig() Config {
  	threshold := 0.85
  	if val, ok := os.LookupEnv("WORMHOLE_KB_DEDUP_THRESHOLD"); ok {
  		if parsed, err := strconv.ParseFloat(val, 64); err == nil {
  			threshold = parsed
  		}
  	}
  	return Config{
  		ListenAddr:       getEnv("WORMHOLE_LISTEN_ADDR", ":8080"),
  		DatabaseURL:      getEnv("WORMHOLE_DATABASE_URL", "postgres://wormhole:wormhole@localhost:5432/wormhole?sslmode=prefer"),
  		KBDedupThreshold: threshold,
  	}
  }
  ```

- [ ] **Step 2: Update `Store` and `NewStore` in `internal/core/kb/kb.go`**
  Add `dedupThreshold float64` field to the `Store` struct. Update the `NewStore` signature to accept `dedupThreshold float64`.
  ```go
  type Store struct {
  	db             *sql.DB
  	embedder       Embedder
  	dedupThreshold float64
  }

  func NewStore(db *sql.DB, embedder Embedder, dedupThreshold float64) *Store {
  	return &Store{db: db, embedder: embedder, dedupThreshold: dedupThreshold}
  }
  ```

- [ ] **Step 3: Update `cmd/wormhole-server/main.go`**
  Modify `main.go` to pass `cfg.KBDedupThreshold` to `kb.NewStore`.
  ```go
  kbStore := kb.NewStore(db, kb.StubEmbedder{}, cfg.KBDedupThreshold)
  ```

- [ ] **Step 4: Update test calls in `internal/core/kb/kb_test.go` and `internal/mcp/kb_test.go`**
  Update every occurrence of `kb.NewStore(db, ...)` to pass a threshold value, e.g., `0.85`.
  ```go
  store := kb.NewStore(db, kb.StubEmbedder{}, 0.85)
  ```

- [ ] **Step 5: Run tests to ensure everything compiles**
  Run: `go build ./...` and `go test ./...`
  Expected: PASS

- [ ] **Step 6: Commit**
  Run:
  ```bash
  git add internal/types/config.go internal/core/kb/kb.go internal/core/kb/kb_test.go internal/mcp/kb_test.go cmd/wormhole-server/main.go
  git commit -m "chore(kb): update config and NewStore signatures to accept dedupThreshold"
  ```

---

### Task 2: Implement Deduplication Check in Write Pipeline

**Files:**
- Modify: `internal/core/kb/kb.go` (add `ErrDedupViolation` and deduplication check inside `WriteArticle`)
- Modify: `internal/mcp/kb.go` (add `Force` to input payload, update `WriteArticleTool`)
- Modify: `internal/core/kb/kb_test.go` (add tests for deduplication, bypass, and RLS project isolation)
- Modify: `internal/mcp/kb_test.go` (add integration tests for structured error responses)

**Interfaces:**
- Consumes: `types.Config.KBDedupThreshold`, `func NewStore(db *sql.DB, embedder Embedder, dedupThreshold float64) *Store`
- Produces:
  - `type ErrDedupViolation struct`
  - `func (s *Store) WriteArticle(ctx context.Context, projectID, agentID, title, body string, frontmatter json.RawMessage, linkTargetIDs []string, force bool) (Article, error)`
  - `type WriteArticleInput struct` with `Force` field

- [ ] **Step 1: Implement `ErrDedupViolation` and check inside `WriteArticle` in `internal/core/kb/kb.go`**
  Define `ErrDedupViolation` as a custom error type:
  ```go
  type ErrDedupViolation struct {
  	ExistingID    string  `json:"existing_article_id"`
  	ExistingTitle string  `json:"existing_article_title"`
  	Similarity    float64 `json:"similarity"`
  	Threshold     float64 `json:"threshold"`
  }

  func (e *ErrDedupViolation) Error() string {
  	m := map[string]any{
  		"error": "kb: write article: semantic duplicate found",
  		"code":  "DEDUP_VIOLATION",
  		"closest_article": map[string]any{
  			"id":         e.ExistingID,
  			"title":      e.ExistingTitle,
  			"similarity": e.Similarity,
  		},
  		"suggestion": fmt.Sprintf("The article is too similar to '%s' (similarity %f >= threshold %f). Use the existing article, update it, or set the 'force' parameter to true to write it anyway.", e.ExistingTitle, e.Similarity, e.Threshold),
  	}
  	b, _ := json.Marshal(m)
  	return string(b)
  }
  ```
  Update `WriteArticle` signature:
  ```go
  func (s *Store) WriteArticle(ctx context.Context, projectID, agentID, title, body string, frontmatter json.RawMessage, linkTargetIDs []string, force bool) (Article, error)
  ```
  Inside `WriteArticle`, after generating `embedding` but before the insert query:
  ```go
  if !force {
  	var existingID string
  	var existingTitle string
  	var similarity float64

  	// pgvector cosine distance <= 1 - threshold maps to similarity >= threshold
  	err = tx.QueryRowContext(ctx,
  		`SELECT id, title, (1 - (embedding <=> $1::vector)) AS similarity
  		 FROM kb_articles
  		 WHERE project_id = $2 AND embedding IS NOT NULL
  		 ORDER BY embedding <=> $1::vector
  		 LIMIT 1`,
  		formatVectorLiteral(embedding), projectID,
  	).Scan(&existingID, &existingTitle, &similarity)

  	if err != nil && !errors.Is(err, sql.ErrNoRows) {
  		return Article{}, fmt.Errorf("kb: write article: dedup query: %w", err)
  	}

  	if err == nil && similarity >= s.dedupThreshold {
  		return Article{}, &ErrDedupViolation{
  			ExistingID:    existingID,
  			ExistingTitle: existingTitle,
  			Similarity:    similarity,
  			Threshold:     s.dedupThreshold,
  		}
  	}
  }
  ```

- [ ] **Step 2: Update MCP input and handler in `internal/mcp/kb.go`**
  Modify `WriteArticleInput` payload schema:
  ```go
  type WriteArticleInput struct {
  	Title       string          `json:"title"`
  	Body        string          `json:"body"`
  	Frontmatter json.RawMessage `json:"frontmatter,omitempty"`
  	Links       []string        `json:"links"`
  	Force       bool            `json:"force"`
  }
  ```
  Pass `in.Force` to `store.WriteArticle`.

- [ ] **Step 3: Add unit tests in `internal/core/kb/kb_test.go`**
  Write tests covering:
  - Deduplication violation: writing a near-duplicate article returns `ErrDedupViolation` and rolls back the transaction.
  - Deduplication bypass: writing a near-duplicate article with `force=true` succeeds.
  - Cross-project isolation: writing a near-duplicate article in project B doesn't block writing the same text in project A.
  Ensure tests run against a real database setup (T1, T2, T3).

- [ ] **Step 4: Add integration tests in `internal/mcp/kb_test.go`**
  Write an integration test verifying that calling `wormhole.kb.write` through the MCP tool interface returns `IsError: true` and the structured JSON error when there is a deduplication violation.

- [ ] **Step 5: Run tests and check compiler/linter warnings**
  Run: `go build ./...`, `go vet ./...`, `go test ./...`
  Expected: PASS

- [ ] **Step 6: Commit**
  Run:
  ```bash
  git add internal/core/kb/kb.go internal/mcp/kb.go internal/core/kb/kb_test.go internal/mcp/kb_test.go
  git commit -m "feat(kb): enforce semantic deduplication check on kb.write with bypass flag"
  ```
