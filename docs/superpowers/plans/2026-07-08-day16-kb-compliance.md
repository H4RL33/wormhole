# Knowledge Base Compliance Checks Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the conciseness ceiling and required-link compliance checks for Knowledge Base article writes, returning structured JSON errors unless bypassed via the `force` flag.

**Architecture:** Add `KBMaxBodyLength`, `KBMinLinksDecision`, `KBMinLinksPolicy`, and `KBMinLinksProcedure` to `types.Config` (with defaults 2000, 1, 1, 1). Update `kb.NewStore` and `kb.Store` to store these values. In `kb.Store.WriteArticle`, if `force` is false, validate that the body length does not exceed `KBMaxBodyLength`. Parse frontmatter for the article type; if the type is decision, policy, or procedure, validate that the number of links meets the minimum requirement. If links are insufficient, search for the top 3 semantically closest articles in the same project using pgvector and return them as suggestions within a custom `ErrRequiredLinksViolation`.

**Tech Stack:** Go, Postgres + pgvector, standard library only (`encoding/json`, `database/sql`, `fmt`, `errors`).

## Global Constraints

- No em-dashes anywhere, including comments and commit messages.
- No ORM, no web framework, stdlib only, no new external Go dependencies (R4).
- R2 (architecture.md §2): `internal/core/kb` still imports only `internal/types` and stdlib, no new cross-core imports.
- T1-T4 (architecture.md §7): real-Postgres tests, happy path + edge cases, `go build ./...` / `go vet ./...` / `go test ./...` all passing with output observed before any commit.

---

### Task 1: Update Config and NewStore Signatures

**Files:**
- Modify: `internal/types/config.go`
- Modify: `internal/core/kb/kb.go`
- Modify: `cmd/wormhole-server/main.go`
- Modify: `internal/core/kb/kb_test.go`
- Modify: `internal/mcp/kb_test.go`

**Interfaces:**
- Consumes: none
- Produces:
  - `types.Config` fields: `KBMaxBodyLength`, `KBMinLinksDecision`, `KBMinLinksPolicy`, `KBMinLinksProcedure`
  - `func NewStore(db *sql.DB, embedder Embedder, dedupThreshold float64, maxBodyLength int, minLinksDecision, minLinksPolicy, minLinksProcedure int) *Store`

- [ ] **Step 1: Add configuration fields to `internal/types/config.go`**
  Modify `internal/types/config.go` to add the new config settings:
  ```go
  type Config struct {
  	ListenAddr        string
  	DatabaseURL       string
  	KBDedupThreshold  float64
  	KBMaxBodyLength   int
  	KBMinLinksDecision int
  	KBMinLinksPolicy   int
  	KBMinLinksProcedure int
  }
  ```
  And update `LoadConfig()` to parse them:
  ```go
  func LoadConfig() Config {
  	threshold := 0.85
  	if val, ok := os.LookupEnv("WORMHOLE_KB_DEDUP_THRESHOLD"); ok {
  		if parsed, err := strconv.ParseFloat(val, 64); err == nil {
  			threshold = parsed
  		}
  	}
  	maxBodyLength := 2000
  	if val, ok := os.LookupEnv("WORMHOLE_KB_MAX_BODY_LENGTH"); ok {
  		if parsed, err := strconv.Atoi(val); err == nil {
  			maxBodyLength = parsed
  		}
  	}
  	minLinksDecision := 1
  	if val, ok := os.LookupEnv("WORMHOLE_KB_MIN_LINKS_DECISION"); ok {
  		if parsed, err := strconv.Atoi(val); err == nil {
  			minLinksDecision = parsed
  		}
  	}
  	minLinksPolicy := 1
  	if val, ok := os.LookupEnv("WORMHOLE_KB_MIN_LINKS_POLICY"); ok {
  		if parsed, err := strconv.Atoi(val); err == nil {
  			minLinksPolicy = parsed
  		}
  	}
  	minLinksProcedure := 1
  	if val, ok := os.LookupEnv("WORMHOLE_KB_MIN_LINKS_PROCEDURE"); ok {
  		if parsed, err := strconv.Atoi(val); err == nil {
  			minLinksProcedure = parsed
  		}
  	}
  	return Config{
  		ListenAddr:          getEnv("WORMHOLE_LISTEN_ADDR", ":8080"),
  		DatabaseURL:         getEnv("WORMHOLE_DATABASE_URL", "postgres://wormhole:wormhole@localhost:5432/wormhole?sslmode=prefer"),
  		KBDedupThreshold:    threshold,
  		KBMaxBodyLength:     maxBodyLength,
  		KBMinLinksDecision:  minLinksDecision,
  		KBMinLinksPolicy:    minLinksPolicy,
  		KBMinLinksProcedure: minLinksProcedure,
  	}
  }
  ```

- [ ] **Step 2: Update `Store` struct and `NewStore` signature in `internal/core/kb/kb.go`**
  Update the fields in `Store` and `NewStore` signature:
  ```go
  type Store struct {
  	db                  *sql.DB
  	embedder            Embedder
  	dedupThreshold      float64
  	maxBodyLength       int
  	minLinksDecision    int
  	minLinksPolicy      int
  	minLinksProcedure   int
  }

  func NewStore(db *sql.DB, embedder Embedder, dedupThreshold float64, maxBodyLength int, minLinksDecision, minLinksPolicy, minLinksProcedure int) *Store {
  	return &Store{
  		db:                  db,
  		embedder:            embedder,
  		dedupThreshold:      dedupThreshold,
  		maxBodyLength:       maxBodyLength,
  		minLinksDecision:    minLinksDecision,
  		minLinksPolicy:      minLinksPolicy,
  		minLinksProcedure:   minLinksProcedure,
  	}
  }
  ```

- [ ] **Step 3: Update `cmd/wormhole-server/main.go`**
  Update construction of `kbStore` in `main.go`:
  ```go
  kbStore := kb.NewStore(db, kb.StubEmbedder{}, cfg.KBDedupThreshold, cfg.KBMaxBodyLength, cfg.KBMinLinksDecision, cfg.KBMinLinksPolicy, cfg.KBMinLinksProcedure)
  ```

- [ ] **Step 4: Update test calls in `internal/core/kb/kb_test.go` and `internal/mcp/kb_test.go`**
  Update test helpers to pass default config values.
  In `internal/core/kb/kb_test.go`:
  ```go
  return NewStore(db, StubEmbedder{}, 0.85, 2000, 1, 1, 1)
  ```
  In `internal/mcp/kb_test.go`:
  ```go
  return kb.NewStore(db, kb.StubEmbedder{}, 0.85, 2000, 1, 1, 1)
  ```

- [ ] **Step 5: Run tests to ensure compilation succeeds**
  Run: `go build ./...` and `go test ./...`
  Expected: PASS

- [ ] **Step 6: Commit**
  Run:
  ```bash
  git add internal/types/config.go internal/core/kb/kb.go cmd/wormhole-server/main.go internal/core/kb/kb_test.go internal/mcp/kb_test.go
  git commit -m "chore(kb): update config and NewStore signature for compliance checks"
  ```

---

### Task 2: Implement Conciseness Ceiling Check

**Files:**
- Modify: `internal/core/kb/kb.go` (add `ErrConcisenessViolation` and validation check)
- Modify: `internal/mcp/kb.go` (un-wrap `ErrConcisenessViolation` at MCP boundary)
- Modify: `internal/core/kb/kb_test.go` (unit tests for conciseness limits)
- Modify: `internal/mcp/kb_test.go` (integration test for conciseness limits)

**Interfaces:**
- Consumes: `types.Config.KBMaxBodyLength`, `kb.Store`
- Produces:
  - `type ErrConcisenessViolation struct`

- [ ] **Step 1: Define `ErrConcisenessViolation` and implement validation in `internal/core/kb/kb.go`**
  Add definition for `ErrConcisenessViolation`:
  ```go
  type ErrConcisenessViolation struct {
  	Length    int `json:"length"`
  	MaxLength int `json:"max_length"`
  }

  func (e *ErrConcisenessViolation) Error() string {
  	m := map[string]any{
  		"error": "kb: write article: conciseness ceiling exceeded",
  		"code":  "CONCISENESS_VIOLATION",
  		"details": map[string]any{
  			"length":     e.Length,
  			"max_length": e.MaxLength,
  		},
  		"suggestion": fmt.Sprintf("The article body is too long (%d characters). The maximum allowed length is %d characters. Please summarize the article to make it more concise.", e.Length, e.MaxLength),
  	}
  	b, _ := json.Marshal(m)
  	return string(b)
  }
  ```
  Inside `WriteArticle`, before inserting the article, perform the conciseness check if `!force`:
  ```go
  	if !force && s.maxBodyLength > 0 && len(body) > s.maxBodyLength {
  		return Article{}, &ErrConcisenessViolation{
  			Length:    len(body),
  			MaxLength: s.maxBodyLength,
  		}
  	}
  ```

- [ ] **Step 2: Update MCP WriteArticleTool to propagate the error in `internal/mcp/kb.go`**
  Add check for `ErrConcisenessViolation` to `WriteArticleTool`:
  ```go
  			if err != nil {
  				var dedupErr *kb.ErrDedupViolation
  				if errors.As(err, &dedupErr) {
  					return nil, dedupErr
  				}
  				var concisenessErr *kb.ErrConcisenessViolation
  				if errors.As(err, &concisenessErr) {
  					return nil, concisenessErr
  				}
  				return nil, fmt.Errorf("mcp: wormhole.kb.write: %w", err)
  			}
  ```

- [ ] **Step 3: Write unit tests in `internal/core/kb/kb_test.go`**
  Add unit tests validating:
  - An article with a body exceeding the maximum length ceiling returns `ErrConcisenessViolation`.
  - An article with a body exceeding the ceiling but written with `force=true` succeeds.
  - Test helper that sets up a store with a smaller limit (e.g. 10 chars) to make testing simple.

- [ ] **Step 4: Write integration tests in `internal/mcp/kb_test.go`**
  Add an integration test asserting that when calling `wormhole.kb.write` with an excessively long body, it returns a 400 Bad Request with the structured `CONCISENESS_VIOLATION` JSON error string.

- [ ] **Step 5: Run tests**
  Run: `go test ./...`
  Expected: PASS

- [ ] **Step 6: Commit**
  Run:
  ```bash
  git add internal/core/kb/kb.go internal/mcp/kb.go internal/core/kb/kb_test.go internal/mcp/kb_test.go
  git commit -m "feat(kb): add conciseness ceiling validation to write pipeline"
  ```

---

### Task 3: Implement Required-Link Validation and Semantic Suggestions

**Files:**
- Modify: `internal/core/kb/kb.go` (add `LinkSuggestion`, `ErrRequiredLinksViolation`, parse frontmatter for type, validate link counts, perform semantic query for suggestions)
- Modify: `internal/mcp/kb.go` (un-wrap `ErrRequiredLinksViolation` at MCP boundary)
- Modify: `internal/core/kb/kb_test.go` (unit tests for required link validation)
- Modify: `internal/mcp/kb_test.go` (integration test for required link validation)

**Interfaces:**
- Consumes: `types.Config` fields, `kb.Store`
- Produces:
  - `type LinkSuggestion struct`
  - `type ErrRequiredLinksViolation struct`

- [ ] **Step 1: Define error types and implement validation in `internal/core/kb/kb.go`**
  Add structs:
  ```go
  type LinkSuggestion struct {
  	ID    string `json:"id"`
  	Title string `json:"title"`
  }

  type ErrRequiredLinksViolation struct {
  	ArticleType string           `json:"article_type"`
  	LinkCount   int              `json:"link_count"`
  	MinLinks    int              `json:"min_links"`
  	Suggestions []LinkSuggestion `json:"suggestions"`
  }

  func (e *ErrRequiredLinksViolation) Error() string {
  	m := map[string]any{
  		"error": "kb: write article: missing required links",
  		"code":  "REQUIRED_LINKS_VIOLATION",
  		"details": map[string]any{
  			"article_type": e.ArticleType,
  			"link_count":   e.LinkCount,
  			"min_links":    e.MinLinks,
  			"suggestions":  e.Suggestions,
  		},
  		"suggestion": fmt.Sprintf("Articles of type '%s' require at least %d links (got %d). We suggest linking to related articles such as: %s.", e.ArticleType, e.MinLinks, e.LinkCount, formatSuggestions(e.Suggestions)),
  	}
  	b, _ := json.Marshal(m)
  	return string(b)
  }

  func formatSuggestions(s []LinkSuggestion) string {
  	var parts []string
  	for _, item := range s {
  		parts = append(parts, fmt.Sprintf("'%s' (%s)", item.Title, item.ID))
  	}
  	if len(parts) == 0 {
  		return "none found"
  	}
  	return strings.Join(parts, ", ")
  }
  ```
  Inside `WriteArticle`, before inserting the article, perform the validation if `!force`:
  ```go
  	if !force {
  		var fm struct {
  			Type string `json:"type"`
  		}
  		if len(frontmatter) > 0 {
  			_ = json.Unmarshal(frontmatter, &fm)
  		}
  		minLinks := 0
  		switch strings.ToLower(fm.Type) {
  		case "decision":
  			minLinks = s.minLinksDecision
  		case "policy":
  			minLinks = s.minLinksPolicy
  		case "procedure":
  			minLinks = s.minLinksProcedure
  		}
  		if minLinks > 0 && len(linkTargetIDs) < minLinks {
  			// Retrieve closest articles as suggestions using pgvector
  			rows, err := tx.QueryContext(ctx,
  				`SELECT id, title
  				 FROM kb_articles
  				 WHERE project_id = $1 AND embedding IS NOT NULL
  				 ORDER BY embedding <=> $2::vector
  				 LIMIT 3`,
  				projectID, formatVectorLiteral(embedding),
  			)
  			if err != nil {
  				return Article{}, fmt.Errorf("kb: write article: link suggestions query: %w", err)
  			}
  			defer rows.Close()

  			var suggestions []LinkSuggestion
  			for rows.Next() {
  				var sugg LinkSuggestion
  				if err := rows.Scan(&sugg.ID, &sugg.Title); err != nil {
  					return Article{}, fmt.Errorf("kb: write article: scan link suggestion: %w", err)
  				}
  				suggestions = append(suggestions, sugg)
  			}
  			return Article{}, &ErrRequiredLinksViolation{
  				ArticleType: fm.Type,
  				LinkCount:   len(linkTargetIDs),
  				MinLinks:    minLinks,
  				Suggestions: suggestions,
  			}
  		}
  	}
  ```

- [ ] **Step 2: Update MCP WriteArticleTool to propagate the error in `internal/mcp/kb.go`**
  Add check for `ErrRequiredLinksViolation`:
  ```go
  				var requiredLinksErr *kb.ErrRequiredLinksViolation
  				if errors.As(err, &requiredLinksErr) {
  					return nil, requiredLinksErr
  				}
  ```

- [ ] **Step 3: Write unit tests in `internal/core/kb/kb_test.go`**
  Add unit tests validating:
  - An article with type `"decision"`, `"policy"`, or `"procedure"` having 0 links returns `ErrRequiredLinksViolation`.
  - The violation error contains the correct article type, counts, and suggestions list (sorted by closest semantic distance).
  - An article with type `"decision"` having 0 links but written with `force=true` succeeds.
  - Normal writes with sufficient links succeed.

- [ ] **Step 4: Write integration tests in `internal/mcp/kb_test.go`**
  Add an integration test asserting that calling `wormhole.kb.write` with frontmatter type `"decision"` and 0 links returns a 400 Bad Request with the structured `REQUIRED_LINKS_VIOLATION` JSON error string, including suggested link targets.

- [ ] **Step 5: Run tests**
  Run: `go test ./...`
  Expected: PASS

- [ ] **Step 6: Commit**
  Run:
  ```bash
  git add internal/core/kb/kb.go internal/mcp/kb.go internal/core/kb/kb_test.go internal/mcp/kb_test.go
  git commit -m "feat(kb): enforce required-link validation with pgvector semantic suggestions"
  ```
