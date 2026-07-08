package kb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	_ "github.com/lib/pq"

	"github.com/H4RL33/wormhole/internal/types"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	cfg := types.LoadConfig()
	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		if os.Getenv("WORMHOLE_INTEGRATION_REQUIRED") == "1" {
			t.Fatalf("postgres required but not reachable: %v", err)
		}
		t.Skipf("postgres not reachable (%v); run `docker compose up -d db` and apply migrations before running this test", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db, StubEmbedder{}, 0.85, 2000, 1, 1, 1)
}

func createProject(t *testing.T, s *Store, name string) string {
	t.Helper()
	var id string
	if err := s.db.QueryRow(`INSERT INTO projects (name, owner) VALUES ($1, $2) RETURNING id`, name, "harley").Scan(&id); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		if _, err := s.db.Exec(`DELETE FROM projects WHERE id = $1`, id); err != nil {
			t.Logf("cleanup: delete project %s: %v", id, err)
		}
	})
	return id
}

func createAgent(t *testing.T, s *Store) string {
	t.Helper()
	var id string
	if err := s.db.QueryRow(`INSERT INTO agents (owner, model, capabilities) VALUES ($1, $2, $3) RETURNING id`,
		"harley", "claude", `[]`).Scan(&id); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() {
		if _, err := s.db.Exec(`DELETE FROM agents WHERE id = $1`, id); err != nil {
			t.Logf("cleanup: delete agent %s: %v", id, err)
		}
	})
	return id
}

func createPassport(t *testing.T, s *Store, agentID, projectID string) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO passports (agent_id, project_id) VALUES ($1, $2)`, agentID, projectID); err != nil {
		t.Fatalf("create passport: %v", err)
	}
	t.Cleanup(func() {
		if _, err := s.db.Exec(`DELETE FROM passports WHERE agent_id = $1 AND project_id = $2`, agentID, projectID); err != nil {
			t.Logf("cleanup: delete passport for agent %s in project %s: %v", agentID, projectID, err)
		}
	})
}

func TestWriteArticle_SuccessNoLinks(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "kb-write-success-no-links")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	article, err := s.WriteArticle(ctx, projectID, agentID, "how to deploy", "run the deploy script", nil, nil, false)
	if err != nil {
		t.Fatalf("WriteArticle: %v", err)
	}

	if article.ID == "" {
		t.Error("article.ID is empty")
	}
	if article.ProjectID != projectID {
		t.Errorf("article.ProjectID = %q, want %q", article.ProjectID, projectID)
	}
	if article.Title != "how to deploy" {
		t.Errorf("article.Title = %q, want %q", article.Title, "how to deploy")
	}
	if article.Body != "run the deploy script" {
		t.Errorf("article.Body = %q, want %q", article.Body, "run the deploy script")
	}
	if string(article.Frontmatter) != "{}" {
		t.Errorf("article.Frontmatter = %q, want %q", article.Frontmatter, "{}")
	}
	if article.AuthorAgentID != agentID {
		t.Errorf("article.AuthorAgentID = %q, want %q", article.AuthorAgentID, agentID)
	}
	if article.CreatedAt.IsZero() {
		t.Error("article.CreatedAt is zero")
	}
	if article.UpdatedAt.IsZero() {
		t.Error("article.UpdatedAt is zero")
	}

	// Day 14 wires the stub embedder into every write, so the embedding
	// column is now populated (Day 13 left it NULL; see
	// TestWriteArticle_EmbeddingPopulated for the dedicated coverage of
	// this behavior).
	var embeddingIsNull bool
	if err := s.db.QueryRow(`SELECT embedding IS NULL FROM kb_articles WHERE id = $1`, article.ID).Scan(&embeddingIsNull); err != nil {
		t.Fatalf("query embedding: %v", err)
	}
	if embeddingIsNull {
		t.Error("expected embedding column to be populated, got NULL")
	}
}

func TestWriteArticle_SuccessWithLinks(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "kb-write-success-with-links")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	target, err := s.WriteArticle(ctx, projectID, agentID, "target article", "target body", nil, nil, false)
	if err != nil {
		t.Fatalf("WriteArticle (target): %v", err)
	}

	frontmatter := json.RawMessage(`{"type":"decision"}`)
	article, err := s.WriteArticle(ctx, projectID, agentID, "linking article", "linking body", frontmatter, []string{target.ID}, false)
	if err != nil {
		t.Fatalf("WriteArticle (linking): %v", err)
	}

	var gotFrontmatter map[string]string
	if err := json.Unmarshal(article.Frontmatter, &gotFrontmatter); err != nil {
		t.Fatalf("article.Frontmatter is not valid JSON: %v", err)
	}
	if gotFrontmatter["type"] != "decision" {
		t.Errorf("article.Frontmatter[type] = %q, want %q", gotFrontmatter["type"], "decision")
	}

	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM kb_links WHERE from_article_id = $1 AND to_article_id = $2 AND project_id = $3`,
		article.ID, target.ID, projectID).Scan(&count); err != nil {
		t.Fatalf("query kb_links: %v", err)
	}
	if count != 1 {
		t.Errorf("kb_links row count = %d, want 1", count)
	}
}

func TestWriteArticle_UnknownLinkTargetLeavesNoPartialRow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "kb-write-unknown-link")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	const title = "orphaned article attempt"
	unknownTargetID := "00000000-0000-0000-0000-000000000000"
	_, err := s.WriteArticle(ctx, projectID, agentID, title, "body", nil, []string{unknownTargetID}, false)
	if !errors.Is(err, ErrLinkedArticleNotFound) {
		t.Fatalf("expected ErrLinkedArticleNotFound, got: %v", err)
	}

	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM kb_articles WHERE project_id = $1 AND title = $2`, projectID, title).Scan(&count); err != nil {
		t.Fatalf("query kb_articles: %v", err)
	}
	if count != 0 {
		t.Fatalf("kb_articles row count for %q = %d, want 0 (partial write leaked past rollback)", title, count)
	}
}

func TestWriteArticle_PassportRequired(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "kb-write-passport-required")
	agentID := createAgent(t, s)

	_, err := s.WriteArticle(ctx, projectID, agentID, "title", "body", nil, nil, false)
	if !errors.Is(err, ErrPassportNotFound) {
		t.Fatalf("expected ErrPassportNotFound, got: %v", err)
	}
}

// TestWriteArticle_CrossProjectIsolation mirrors git_test.go's
// TestGitLinks_CrossProjectIsolation: a plain project_id-scoped connection
// using the table owner role bypasses RLS entirely (Postgres does not
// enforce RLS against the table owner), so this test creates a restricted,
// non-owner role to prove the policy itself hides project A's article when
// project B's context is set.
func TestWriteArticle_CrossProjectIsolation(t *testing.T) {
	ownerStore := testStore(t)
	ctx := context.Background()

	roleName := "kb_rls_test_user"
	rolePassword := "kb_rls_test_password"

	t.Cleanup(func() {
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE kb_articles FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE kb_links FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE projects FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE agents FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE passports FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("DROP ROLE IF EXISTS %s", roleName))
	})

	if _, err := ownerStore.db.Exec(fmt.Sprintf("DROP ROLE IF EXISTS %s", roleName)); err != nil {
		t.Fatalf("failed to drop pre-existing role: %v", err)
	}
	if _, err := ownerStore.db.Exec(fmt.Sprintf("CREATE ROLE %s WITH LOGIN PASSWORD '%s'", roleName, rolePassword)); err != nil {
		t.Fatalf("failed to create role: %v", err)
	}
	if _, err := ownerStore.db.Exec(fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE kb_articles, kb_links, projects, agents, passports TO %s", roleName)); err != nil {
		t.Fatalf("failed to grant table privileges: %v", err)
	}

	cfg := types.LoadConfig()
	u, err := url.Parse(cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("failed to parse database URL: %v", err)
	}
	u.User = url.UserPassword(roleName, rolePassword)
	restrictedDSN := u.String()

	restrictedDB, err := sql.Open("postgres", restrictedDSN)
	if err != nil {
		t.Fatalf("failed to open restricted db connection: %v", err)
	}
	t.Cleanup(func() { restrictedDB.Close() })

	if err := restrictedDB.PingContext(ctx); err != nil {
		t.Fatalf("failed to ping restricted database: %v", err)
	}

	projectA := createProject(t, ownerStore, "kb-isolation-project-a")
	projectB := createProject(t, ownerStore, "kb-isolation-project-b")
	agentID := createAgent(t, ownerStore)
	createPassport(t, ownerStore, agentID, projectA)

	// Create the article via the (RLS-bypassing) owner store so the
	// restricted connection below has done nothing but Ping before its first
	// query; this avoids the restricted session's wormhole.project_id
	// placeholder GUC being left at '' (rather than unset) by an earlier
	// local SET on the same pooled connection, which would make the "no
	// context set" check below fail with a cast error instead of exercising
	// RLS.
	article, err := ownerStore.WriteArticle(ctx, projectA, agentID, "project a article", "body", nil, nil, false)
	if err != nil {
		t.Fatalf("WriteArticle (project A): %v", err)
	}

	// 1. No project context set: RLS must hide the row entirely.
	var found string
	err = restrictedDB.QueryRowContext(ctx, "SELECT id FROM kb_articles WHERE id = $1", article.ID).Scan(&found)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected kb_articles row to be hidden with no project context set, got err=%v found=%q", err, found)
	}

	// 2. Project B's context set: project A's row must still be invisible.
	tx, err := restrictedDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectB); err != nil {
		t.Fatalf("set project id: %v", err)
	}
	err = tx.QueryRowContext(ctx, "SELECT id FROM kb_articles WHERE id = $1", article.ID).Scan(&found)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected kb_articles row from project A to be hidden under project B's RLS context, got err=%v found=%q", err, found)
	}

	// 3. Project A's own context set: the row must be visible (sanity check
	// that RLS scopes rather than blanket-denies).
	tx2, err := restrictedDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx2.Rollback()
	if _, err := tx2.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectA); err != nil {
		t.Fatalf("set project id: %v", err)
	}
	if err := tx2.QueryRowContext(ctx, "SELECT id FROM kb_articles WHERE id = $1", article.ID).Scan(&found); err != nil {
		t.Fatalf("expected kb_articles row to be visible under its own project context, got err=%v", err)
	}
}

// TestWriteArticle_EmbeddingPopulated proves WriteArticle actually stores a
// stub embedding (not just leaving the column NULL as Day 13 did): the
// pgvector text representation must round-trip a 16-element vector.
func TestWriteArticle_EmbeddingPopulated(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "kb-embedding-populated")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	article, err := s.WriteArticle(ctx, projectID, agentID, "title", "the article body text", nil, nil, false)
	if err != nil {
		t.Fatalf("WriteArticle: %v", err)
	}

	var embeddingText sql.NullString
	if err := s.db.QueryRow(`SELECT embedding::text FROM kb_articles WHERE id = $1`, article.ID).Scan(&embeddingText); err != nil {
		t.Fatalf("query embedding: %v", err)
	}
	if !embeddingText.Valid || embeddingText.String == "" {
		t.Fatal("expected embedding column to be non-null and non-empty")
	}

	trimmed := strings.Trim(embeddingText.String, "[]")
	components := strings.Split(trimmed, ",")
	if len(components) != 16 {
		t.Fatalf("stored embedding has %d dimensions, want 16 (raw: %q)", len(components), embeddingText.String)
	}
}

// TestWriteArticle_EmbeddingDeterministic proves two articles with identical
// body text produce identical stored embeddings. Task 2's search test
// depends on this: searching with an article's own body text must find it
// at distance 0, since the same text hashes to the same stub vector.
func TestWriteArticle_EmbeddingDeterministic(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "kb-embedding-deterministic")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	const body = "identical body text for both articles"
	first, err := s.WriteArticle(ctx, projectID, agentID, "first", body, nil, nil, false)
	if err != nil {
		t.Fatalf("WriteArticle (first): %v", err)
	}
	second, err := s.WriteArticle(ctx, projectID, agentID, "second", body, nil, nil, true)
	if err != nil {
		t.Fatalf("WriteArticle (second): %v", err)
	}

	var firstEmbedding, secondEmbedding string
	if err := s.db.QueryRow(`SELECT embedding::text FROM kb_articles WHERE id = $1`, first.ID).Scan(&firstEmbedding); err != nil {
		t.Fatalf("query embedding (first): %v", err)
	}
	if err := s.db.QueryRow(`SELECT embedding::text FROM kb_articles WHERE id = $1`, second.ID).Scan(&secondEmbedding); err != nil {
		t.Fatalf("query embedding (second): %v", err)
	}

	if firstEmbedding != secondEmbedding {
		t.Fatalf("embeddings for identical body text differ: first=%q second=%q", firstEmbedding, secondEmbedding)
	}
}

func TestSearchArticles_SuccessAndLimit(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "kb-search-success-limit")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	// Create 3 articles with distinct body texts.
	_, err := s.WriteArticle(ctx, projectID, agentID, "deploy guide", "run the production deploy script carefully", nil, nil, false)
	if err != nil {
		t.Fatalf("WriteArticle 1: %v", err)
	}
	a2, err := s.WriteArticle(ctx, projectID, agentID, "setup guide", "install go and docker compose first", nil, nil, false)
	if err != nil {
		t.Fatalf("WriteArticle 2: %v", err)
	}
	_, err = s.WriteArticle(ctx, projectID, agentID, "database backup", "backup postgres daily using pg_dump", nil, nil, false)
	if err != nil {
		t.Fatalf("WriteArticle 3: %v", err)
	}

	// 1. Search with exact body of a2. Under StubEmbedder, identical text produces identical embedding.
	// Cosine distance should be 0, so a2 must rank first.
	results, err := s.SearchArticles(ctx, projectID, agentID, "install go and docker compose first", 10)
	if err != nil {
		t.Fatalf("SearchArticles: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if results[0].ID != a2.ID {
		t.Errorf("expected first ranked article to be %s (a2), got %s", a2.ID, results[0].ID)
	}

	// 2. Test limit parameter caps results.
	resultsCap, err := s.SearchArticles(ctx, projectID, agentID, "install go and docker compose first", 2)
	if err != nil {
		t.Fatalf("SearchArticles limit 2: %v", err)
	}
	if len(resultsCap) != 2 {
		t.Errorf("expected 2 results, got %d", len(resultsCap))
	}
	if resultsCap[0].ID != a2.ID {
		t.Errorf("expected first ranked article to be %s (a2), got %s", a2.ID, resultsCap[0].ID)
	}

	// 3. Test limit defaulting to 10 when <= 0.
	resultsDefault, err := s.SearchArticles(ctx, projectID, agentID, "install go and docker compose first", 0)
	if err != nil {
		t.Fatalf("SearchArticles limit 0: %v", err)
	}
	if len(resultsDefault) != 3 {
		t.Errorf("expected 3 results due to defaulting limit to 10, got %d", len(resultsDefault))
	}
}

func TestSearchArticles_PassportRequired(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "kb-search-passport-required")
	agentID := createAgent(t, s)

	_, err := s.SearchArticles(ctx, projectID, agentID, "some query", 10)
	if !errors.Is(err, ErrPassportNotFound) {
		t.Fatalf("expected ErrPassportNotFound, got: %v", err)
	}
}

func TestSearchArticles_ExcludeNullEmbedding(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "kb-search-exclude-null")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	// Create a normal article (has embedding).
	_, err := s.WriteArticle(ctx, projectID, agentID, "normal article", "normal body", nil, nil, false)
	if err != nil {
		t.Fatalf("WriteArticle: %v", err)
	}

	// Manually insert an article with NULL embedding (simulating pre-Task-1 legacy row).
	var legacyID string
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO kb_articles (project_id, title, body, frontmatter, author_agent_id, embedding)
		 VALUES ($1, $2, $3, $4, $5, NULL)
		 RETURNING id`,
		projectID, "legacy article", "legacy body", json.RawMessage(`{}`), agentID,
	).Scan(&legacyID)
	if err != nil {
		t.Fatalf("manual insert of null embedding: %v", err)
	}
	defer func() {
		_, _ = s.db.ExecContext(ctx, "DELETE FROM kb_articles WHERE id = $1", legacyID)
	}()

	// Search should only return the normal article, completely excluding the legacy one.
	results, err := s.SearchArticles(ctx, projectID, agentID, "normal body", 10)
	if err != nil {
		t.Fatalf("SearchArticles: %v", err)
	}

	for _, res := range results {
		if res.ID == legacyID {
			t.Errorf("search results included legacy article %s which has a NULL embedding", legacyID)
		}
	}
}

func TestSearchArticles_CrossProjectIsolation(t *testing.T) {
	ownerStore := testStore(t)
	ctx := context.Background()

	roleName := "kb_search_rls_test_user"
	rolePassword := "kb_search_rls_test_password"

	t.Cleanup(func() {
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE kb_articles FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE kb_links FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE projects FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE agents FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE passports FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("DROP ROLE IF EXISTS %s", roleName))
	})

	if _, err := ownerStore.db.Exec(fmt.Sprintf("DROP ROLE IF EXISTS %s", roleName)); err != nil {
		t.Fatalf("failed to drop pre-existing role: %v", err)
	}
	if _, err := ownerStore.db.Exec(fmt.Sprintf("CREATE ROLE %s WITH LOGIN PASSWORD '%s'", roleName, rolePassword)); err != nil {
		t.Fatalf("failed to create role: %v", err)
	}
	if _, err := ownerStore.db.Exec(fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE kb_articles, kb_links, projects, agents, passports TO %s", roleName)); err != nil {
		t.Fatalf("failed to grant table privileges: %v", err)
	}

	cfg := types.LoadConfig()
	u, err := url.Parse(cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("failed to parse database URL: %v", err)
	}
	u.User = url.UserPassword(roleName, rolePassword)
	restrictedDSN := u.String()

	restrictedDB, err := sql.Open("postgres", restrictedDSN)
	if err != nil {
		t.Fatalf("failed to open restricted db connection: %v", err)
	}
	t.Cleanup(func() { restrictedDB.Close() })

	if err := restrictedDB.PingContext(ctx); err != nil {
		t.Fatalf("failed to ping restricted database: %v", err)
	}

	projectA := createProject(t, ownerStore, "kb-search-isolation-a")
	projectB := createProject(t, ownerStore, "kb-search-isolation-b")
	agentID := createAgent(t, ownerStore)
	createPassport(t, ownerStore, agentID, projectA)
	createPassport(t, ownerStore, agentID, projectB)

	// Create articles in both projects.
	_, err = ownerStore.WriteArticle(ctx, projectA, agentID, "project a article", "body a", nil, nil, false)
	if err != nil {
		t.Fatalf("WriteArticle (project A): %v", err)
	}
	_, err = ownerStore.WriteArticle(ctx, projectB, agentID, "project b article", "body b", nil, nil, false)
	if err != nil {
		t.Fatalf("WriteArticle (project B): %v", err)
	}

	// Using a restricted connection with project A context, search should return only project A articles.
	restrictedStore := NewStore(restrictedDB, StubEmbedder{}, 0.85, 2000, 1, 1, 1)

	resultsA, err := restrictedStore.SearchArticles(ctx, projectA, agentID, "body a", 10)
	if err != nil {
		t.Fatalf("SearchArticles restricted A: %v", err)
	}

	if len(resultsA) == 0 {
		t.Fatal("expected results, got 0")
	}
	for _, res := range resultsA {
		if res.ProjectID != projectA {
			t.Errorf("leaked article from other project: %+v", res)
		}
	}
}

func TestWriteArticle_DedupViolation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "kb-dedup-violation")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	const title1 = "first article"
	const body = "This is the body of the article."
	a1, err := s.WriteArticle(ctx, projectID, agentID, title1, body, nil, nil, false)
	if err != nil {
		t.Fatalf("WriteArticle first: %v", err)
	}

	const title2 = "duplicate article"
	_, err = s.WriteArticle(ctx, projectID, agentID, title2, body, nil, nil, false)
	if err == nil {
		t.Fatal("expected ErrDedupViolation error, got nil")
	}

	var dedupErr *ErrDedupViolation
	if !errors.As(err, &dedupErr) {
		t.Fatalf("expected ErrDedupViolation, got type %T: %v", err, err)
	}

	if dedupErr.ExistingID != a1.ID {
		t.Errorf("dedupErr.ExistingID = %q, want %q", dedupErr.ExistingID, a1.ID)
	}
	if dedupErr.ExistingTitle != a1.Title {
		t.Errorf("dedupErr.ExistingTitle = %q, want %q", dedupErr.ExistingTitle, a1.Title)
	}
	if dedupErr.Similarity < 0.99 {
		t.Errorf("dedupErr.Similarity = %f, want ~1.0", dedupErr.Similarity)
	}
	if dedupErr.Threshold != s.dedupThreshold {
		t.Errorf("dedupErr.Threshold = %f, want %f", dedupErr.Threshold, s.dedupThreshold)
	}

	// Verify rollback: title2 should not exist in the database.
	var count int
	err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM kb_articles WHERE project_id = $1 AND title = $2", projectID, title2).Scan(&count)
	if err != nil {
		t.Fatalf("query count of title2: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 articles for title %q due to transaction rollback, got %d", title2, count)
	}
}

func TestWriteArticle_DedupBypass(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "kb-dedup-bypass")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	const title1 = "first article"
	const body = "This is the body of the article."
	_, err := s.WriteArticle(ctx, projectID, agentID, title1, body, nil, nil, false)
	if err != nil {
		t.Fatalf("WriteArticle first: %v", err)
	}

	const title2 = "duplicate article"
	_, err = s.WriteArticle(ctx, projectID, agentID, title2, body, nil, nil, true)
	if err != nil {
		t.Fatalf("expected successful bypass, got error: %v", err)
	}

	// Verify both exist.
	var count int
	err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM kb_articles WHERE project_id = $1", projectID).Scan(&count)
	if err != nil {
		t.Fatalf("query count: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 articles in project, got %d", count)
	}
}

func TestWriteArticle_DedupCrossProject(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectA := createProject(t, s, "kb-dedup-cross-a")
	projectB := createProject(t, s, "kb-dedup-cross-b")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectA)
	createPassport(t, s, agentID, projectB)

	const body = "This is the body of the article."
	_, err := s.WriteArticle(ctx, projectA, agentID, "title a", body, nil, nil, false)
	if err != nil {
		t.Fatalf("WriteArticle project A: %v", err)
	}

	// Write same body in project B without force, should succeed due to isolation.
	_, err = s.WriteArticle(ctx, projectB, agentID, "title b", body, nil, nil, false)
	if err != nil {
		t.Fatalf("expected WriteArticle in project B to succeed, got error: %v", err)
	}
}

func testStoreWithLimit(t *testing.T, maxBodyLength int) *Store {
	t.Helper()
	cfg := types.LoadConfig()
	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		if os.Getenv("WORMHOLE_INTEGRATION_REQUIRED") == "1" {
			t.Fatalf("postgres required but not reachable: %v", err)
		}
		t.Skipf("postgres not reachable (%v); run `docker compose up -d db` and apply migrations before running this test", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db, StubEmbedder{}, 0.85, maxBodyLength, 1, 1, 1)
}

func TestWriteArticle_ConcisenessViolation(t *testing.T) {
	s := testStoreWithLimit(t, 10)
	ctx := context.Background()
	projectID := createProject(t, s, "kb-conciseness-violation")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	const title = "too long"
	const body = "This is a body that is longer than 10 characters."

	_, err := s.WriteArticle(ctx, projectID, agentID, title, body, nil, nil, false)
	if err == nil {
		t.Fatal("expected ErrConcisenessViolation, got nil")
	}

	var concisenessErr *ErrConcisenessViolation
	if !errors.As(err, &concisenessErr) {
		t.Fatalf("expected ErrConcisenessViolation, got type %T: %v", err, err)
	}

	if concisenessErr.Length != len(body) {
		t.Errorf("concisenessErr.Length = %d, want %d", concisenessErr.Length, len(body))
	}
	if concisenessErr.MaxLength != 10 {
		t.Errorf("concisenessErr.MaxLength = %d, want 10", concisenessErr.MaxLength)
	}

	var parsed struct {
		Error   string `json:"error"`
		Code    string `json:"code"`
		Details struct {
			Length    int `json:"length"`
			MaxLength int `json:"max_length"`
		} `json:"details"`
		Suggestion string `json:"suggestion"`
	}
	if err := json.Unmarshal([]byte(concisenessErr.Error()), &parsed); err != nil {
		t.Fatalf("expected Error() to be valid JSON, got: %s", concisenessErr.Error())
	}
	if parsed.Code != "CONCISENESS_VIOLATION" {
		t.Errorf("parsed.Code = %q, want 'CONCISENESS_VIOLATION'", parsed.Code)
	}
	if parsed.Details.Length != len(body) {
		t.Errorf("parsed.Details.Length = %d, want %d", parsed.Details.Length, len(body))
	}
	if parsed.Details.MaxLength != 10 {
		t.Errorf("parsed.Details.MaxLength = %d, want 10", parsed.Details.MaxLength)
	}

	var count int
	err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM kb_articles WHERE project_id = $1 AND title = $2", projectID, title).Scan(&count)
	if err != nil {
		t.Fatalf("query count of title: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 articles for title %q due to transaction rollback, got %d", title, count)
	}
}

func TestWriteArticle_ConcisenessBypass(t *testing.T) {
	s := testStoreWithLimit(t, 10)
	ctx := context.Background()
	projectID := createProject(t, s, "kb-conciseness-bypass")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	const title = "too long but forced"
	const body = "This is a body that is longer than 10 characters."

	article, err := s.WriteArticle(ctx, projectID, agentID, title, body, nil, nil, true)
	if err != nil {
		t.Fatalf("expected write with force=true to bypass conciseness ceiling, got: %v", err)
	}

	if article.Body != body {
		t.Errorf("article.Body = %q, want %q", article.Body, body)
	}

	var count int
	err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM kb_articles WHERE project_id = $1 AND id = $2", projectID, article.ID).Scan(&count)
	if err != nil {
		t.Fatalf("query count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 article in db, got %d", count)
	}
}

func TestWriteArticle_ConcisenessUTF8(t *testing.T) {
	s := testStoreWithLimit(t, 5)
	ctx := context.Background()
	projectID := createProject(t, s, "kb-conciseness-utf8")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	// "🚀🤖🌟🔥💫" has 5 characters (emojis) but 20 bytes.
	// Since maxBodyLength is 5, it should succeed.
	const bodyValid = "🚀🤖🌟🔥💫"
	article, err := s.WriteArticle(ctx, projectID, agentID, "valid utf8", bodyValid, nil, nil, false)
	if err != nil {
		t.Fatalf("expected 5-rune UTF-8 body to succeed under maxBodyLength=5, got error: %v", err)
	}
	if article.Body != bodyValid {
		t.Errorf("article.Body = %q, want %q", article.Body, bodyValid)
	}

	// "🚀🤖🌟🔥💫✨" has 6 characters (emojis) but 24 bytes.
	// Since maxBodyLength is 5, it should violate the ceiling.
	const bodyInvalid = "🚀🤖🌟🔥💫✨"
	_, err = s.WriteArticle(ctx, projectID, agentID, "invalid utf8", bodyInvalid, nil, nil, false)
	if err == nil {
		t.Fatal("expected ErrConcisenessViolation, got nil")
	}

	var concisenessErr *ErrConcisenessViolation
	if !errors.As(err, &concisenessErr) {
		t.Fatalf("expected ErrConcisenessViolation, got type %T: %v", err, err)
	}

	if concisenessErr.Length != 6 {
		t.Errorf("concisenessErr.Length = %d, want 6", concisenessErr.Length)
	}
	if concisenessErr.MaxLength != 5 {
		t.Errorf("concisenessErr.MaxLength = %d, want 5", concisenessErr.MaxLength)
	}
}

func TestWriteArticle_RequiredLinksViolation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "kb-req-links-violation")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	// Create 4 articles to have plenty of candidates for suggestions
	_, err := s.WriteArticle(ctx, projectID, agentID, "Setup Guide", "Install all dependencies and run setup", nil, nil, false)
	if err != nil {
		t.Fatalf("WriteArticle a1: %v", err)
	}
	_, err = s.WriteArticle(ctx, projectID, agentID, "Deploy Guide", "Deploying database migrations and frontend assets", nil, nil, false)
	if err != nil {
		t.Fatalf("WriteArticle a2: %v", err)
	}
	_, err = s.WriteArticle(ctx, projectID, agentID, "Backup Policy", "Scheduled database backups everyday at midnight", nil, nil, false)
	if err != nil {
		t.Fatalf("WriteArticle a3: %v", err)
	}
	_, err = s.WriteArticle(ctx, projectID, agentID, "Monitoring", "Use Prometheus and Grafana for system metrics", nil, nil, false)
	if err != nil {
		t.Fatalf("WriteArticle a4: %v", err)
	}

	body := "Need to make a decision about architecture and links"
	embedder := StubEmbedder{}
	queryEmbedding, err := embedder.Embed(ctx, body)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	// Fetch expected top 3 closest articles from the db using pgvector
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title
		 FROM kb_articles
		 WHERE project_id = $1 AND embedding IS NOT NULL
		 ORDER BY embedding <=> $2::vector
		 LIMIT 3`,
		projectID, formatVectorLiteral(queryEmbedding),
	)
	if err != nil {
		t.Fatalf("query expected suggestions: %v", err)
	}
	defer rows.Close()

	var expectedSuggestions []LinkSuggestion
	for rows.Next() {
		var sugg LinkSuggestion
		if err := rows.Scan(&sugg.ID, &sugg.Title); err != nil {
			t.Fatalf("scan expected suggestion: %v", err)
		}
		expectedSuggestions = append(expectedSuggestions, sugg)
	}

	typesToTest := []string{"decision", "policy", "procedure"}
	for _, aType := range typesToTest {
		t.Run(aType, func(t *testing.T) {
			frontmatter := json.RawMessage(fmt.Sprintf(`{"type":"%s"}`, aType))
			_, err := s.WriteArticle(ctx, projectID, agentID, "My Article", body, frontmatter, nil, false)
			if err == nil {
				t.Fatalf("expected ErrRequiredLinksViolation, got nil")
			}

			var violationErr *ErrRequiredLinksViolation
			if !errors.As(err, &violationErr) {
				t.Fatalf("expected ErrRequiredLinksViolation, got type %T: %v", err, err)
			}

			if strings.ToLower(violationErr.ArticleType) != aType {
				t.Errorf("violationErr.ArticleType = %q, want %q", violationErr.ArticleType, aType)
			}
			if violationErr.LinkCount != 0 {
				t.Errorf("violationErr.LinkCount = %d, want 0", violationErr.LinkCount)
			}
			if violationErr.MinLinks != 1 {
				t.Errorf("violationErr.MinLinks = %d, want 1", violationErr.MinLinks)
			}

			// Validate suggestions list length and values (must match expectedSuggestions exactly, including order)
			if len(violationErr.Suggestions) != len(expectedSuggestions) {
				t.Fatalf("violationErr.Suggestions count = %d, want %d", len(violationErr.Suggestions), len(expectedSuggestions))
			}
			for i, sugg := range violationErr.Suggestions {
				if sugg.ID != expectedSuggestions[i].ID {
					t.Errorf("suggestion %d ID = %q, want %q", i, sugg.ID, expectedSuggestions[i].ID)
				}
				if sugg.Title != expectedSuggestions[i].Title {
					t.Errorf("suggestion %d Title = %q, want %q", i, sugg.Title, expectedSuggestions[i].Title)
				}
			}

			// Verify JSON format of Error()
			var parsed struct {
				Error   string `json:"error"`
				Code    string `json:"code"`
				Details struct {
					ArticleType string           `json:"article_type"`
					LinkCount   int              `json:"link_count"`
					MinLinks    int              `json:"min_links"`
					Suggestions []LinkSuggestion `json:"suggestions"`
				} `json:"details"`
				Suggestion string `json:"suggestion"`
			}
			if err := json.Unmarshal([]byte(violationErr.Error()), &parsed); err != nil {
				t.Fatalf("expected Error() to be valid JSON, got: %s", violationErr.Error())
			}
			if parsed.Code != "REQUIRED_LINKS_VIOLATION" {
				t.Errorf("parsed.Code = %q, want 'REQUIRED_LINKS_VIOLATION'", parsed.Code)
			}
			if parsed.Details.ArticleType != aType {
				t.Errorf("parsed.Details.ArticleType = %q, want %q", parsed.Details.ArticleType, aType)
			}
			if parsed.Details.LinkCount != 0 {
				t.Errorf("parsed.Details.LinkCount = %d, want 0", parsed.Details.LinkCount)
			}
			if parsed.Details.MinLinks != 1 {
				t.Errorf("parsed.Details.MinLinks = %d, want 1", parsed.Details.MinLinks)
			}
		})
	}
}

func TestWriteArticle_RequiredLinksBypass(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "kb-req-links-bypass")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	frontmatter := json.RawMessage(`{"type":"decision"}`)
	article, err := s.WriteArticle(ctx, projectID, agentID, "forced article", "body content", frontmatter, nil, true)
	if err != nil {
		t.Fatalf("expected write with force=true to bypass link requirement, got: %v", err)
	}

	if article.Title != "forced article" {
		t.Errorf("article.Title = %q, want %q", article.Title, "forced article")
	}
}

func TestWriteArticle_RequiredLinksNormal(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "kb-req-links-normal")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	target, err := s.WriteArticle(ctx, projectID, agentID, "target", "body target", nil, nil, false)
	if err != nil {
		t.Fatalf("WriteArticle (target): %v", err)
	}

	frontmatter := json.RawMessage(`{"type":"decision"}`)
	article, err := s.WriteArticle(ctx, projectID, agentID, "decision article", "body content", frontmatter, []string{target.ID}, false)
	if err != nil {
		t.Fatalf("expected write with sufficient links to succeed, got: %v", err)
	}

	if article.Title != "decision article" {
		t.Errorf("article.Title = %q, want %q", article.Title, "decision article")
	}
}



func TestGetArticle_HappyPath(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "kb-get-happy")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	written, err := s.WriteArticle(ctx, projectID, agentID, "get article title", "get article body", nil, nil, false)
	if err != nil {
		t.Fatalf("WriteArticle: %v", err)
	}

	got, err := s.GetArticle(ctx, projectID, agentID, written.ID)
	if err != nil {
		t.Fatalf("GetArticle: %v", err)
	}
	if got.ID != written.ID {
		t.Errorf("ID: got %q, want %q", got.ID, written.ID)
	}
	if got.ProjectID != projectID {
		t.Errorf("ProjectID: got %q, want %q", got.ProjectID, projectID)
	}
	if got.Title != written.Title {
		t.Errorf("Title: got %q, want %q", got.Title, written.Title)
	}
	if got.Body != written.Body {
		t.Errorf("Body: got %q, want %q", got.Body, written.Body)
	}
	if got.AuthorAgentID != agentID {
		t.Errorf("AuthorAgentID: got %q, want %q", got.AuthorAgentID, agentID)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero")
	}
}

func TestGetArticle_NotFound(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "kb-get-not-found")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	_, err := s.GetArticle(ctx, projectID, agentID, "00000000-0000-0000-0000-000000000000")
	if !errors.Is(err, ErrArticleNotFound) {
		t.Fatalf("expected ErrArticleNotFound, got: %v", err)
	}
}

// TestGetArticle_CrossProjectIsolation mirrors TestWriteArticle_CrossProjectIsolation:
// creates a restricted (non-owner) role so RLS is enforced, writes an article
// under project A via the owner store, then calls GetArticle with project B's
// context and expects ErrArticleNotFound (RLS hides project A's row).
func TestGetArticle_CrossProjectIsolation(t *testing.T) {
	ownerStore := testStore(t)
	ctx := context.Background()

	roleName := "kb_get_rls_test_user"
	rolePassword := "kb_get_rls_test_password"

	t.Cleanup(func() {
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE kb_articles FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE kb_links FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE projects FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE agents FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE passports FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("DROP ROLE IF EXISTS %s", roleName))
	})

	if _, err := ownerStore.db.Exec(fmt.Sprintf("DROP ROLE IF EXISTS %s", roleName)); err != nil {
		t.Fatalf("failed to drop pre-existing role: %v", err)
	}
	if _, err := ownerStore.db.Exec(fmt.Sprintf("CREATE ROLE %s WITH LOGIN PASSWORD '%s'", roleName, rolePassword)); err != nil {
		t.Fatalf("failed to create role: %v", err)
	}
	if _, err := ownerStore.db.Exec(fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE kb_articles, kb_links, projects, agents, passports TO %s", roleName)); err != nil {
		t.Fatalf("failed to grant table privileges: %v", err)
	}

	cfg := types.LoadConfig()
	u, err := url.Parse(cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("failed to parse database URL: %v", err)
	}
	u.User = url.UserPassword(roleName, rolePassword)
	restrictedDSN := u.String()

	restrictedDB, err := sql.Open("postgres", restrictedDSN)
	if err != nil {
		t.Fatalf("failed to open restricted db connection: %v", err)
	}
	t.Cleanup(func() { restrictedDB.Close() })

	if err := restrictedDB.PingContext(ctx); err != nil {
		t.Fatalf("failed to ping restricted database: %v", err)
	}

	projectA := createProject(t, ownerStore, "kb-get-isolation-a")
	projectB := createProject(t, ownerStore, "kb-get-isolation-b")
	agentID := createAgent(t, ownerStore)
	createPassport(t, ownerStore, agentID, projectA)
	createPassport(t, ownerStore, agentID, projectB)

	article, err := ownerStore.WriteArticle(ctx, projectA, agentID, "project a article", "body a", nil, nil, false)
	if err != nil {
		t.Fatalf("WriteArticle (project A): %v", err)
	}

	// GetArticle scoped to project B must not see project A's article (RLS).
	restrictedStore := NewStore(restrictedDB, StubEmbedder{}, 0.85, 2000, 1, 1, 1)
	_, err = restrictedStore.GetArticle(ctx, projectB, agentID, article.ID)
	if !errors.Is(err, ErrArticleNotFound) {
		t.Fatalf("expected ErrArticleNotFound (RLS isolation), got: %v", err)
	}
}

func TestGetArticle_NoPassport(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "kb-get-no-passport")
	agentID := createAgent(t, s)

	_, err := s.GetArticle(ctx, projectID, agentID, "00000000-0000-0000-0000-000000000000")
	if !errors.Is(err, ErrPassportNotFound) {
		t.Fatalf("expected ErrPassportNotFound, got: %v", err)
	}
}

func TestGetArticleLinks_HappyPath(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "kb-get-links-happy")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	// Write article B (target).
	b, err := s.WriteArticle(ctx, projectID, agentID, "article B", "body B", nil, nil, false)
	if err != nil {
		t.Fatalf("WriteArticle B: %v", err)
	}

	// Write article A linking to B.
	a, err := s.WriteArticle(ctx, projectID, agentID, "article A", "body A", nil, []string{b.ID}, false)
	if err != nil {
		t.Fatalf("WriteArticle A: %v", err)
	}

	links, err := s.GetArticleLinks(ctx, projectID, agentID, a.ID)
	if err != nil {
		t.Fatalf("GetArticleLinks: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("expected 1 link, got %d", len(links))
	}
	if links[0].ID != b.ID {
		t.Errorf("links[0].ID = %q, want %q", links[0].ID, b.ID)
	}
	if links[0].Title != "article B" {
		t.Errorf("links[0].Title = %q, want %q", links[0].Title, "article B")
	}
	if links[0].Body != "body B" {
		t.Errorf("links[0].Body = %q, want %q", links[0].Body, "body B")
	}
	if links[0].ProjectID != projectID {
		t.Errorf("links[0].ProjectID = %q, want %q", links[0].ProjectID, projectID)
	}
	if links[0].CreatedAt.IsZero() {
		t.Error("links[0].CreatedAt is zero")
	}
}

func TestGetArticleLinks_NoLinks(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "kb-get-links-empty")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	article, err := s.WriteArticle(ctx, projectID, agentID, "lonely article", "no links here", nil, nil, false)
	if err != nil {
		t.Fatalf("WriteArticle: %v", err)
	}

	links, err := s.GetArticleLinks(ctx, projectID, agentID, article.ID)
	if err != nil {
		t.Fatalf("GetArticleLinks: %v", err)
	}
	if links == nil {
		t.Fatal("expected non-nil empty slice, got nil")
	}
	if len(links) != 0 {
		t.Errorf("expected 0 links, got %d", len(links))
	}
}

func TestGetArticleLinks_ArticleNotFound(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "kb-get-links-not-found")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	_, err := s.GetArticleLinks(ctx, projectID, agentID, "00000000-0000-0000-0000-000000000000")
	if !errors.Is(err, ErrArticleNotFound) {
		t.Fatalf("expected ErrArticleNotFound, got: %v", err)
	}
}

// TestGetArticleLinks_CrossProjectIsolation verifies that calling
// GetArticleLinks from project B's context on an article that belongs to
// project A returns ErrArticleNotFound (both the source article check and the
// JOIN respect RLS). Uses a restricted (non-owner) role so RLS is enforced.
func TestGetArticleLinks_CrossProjectIsolation(t *testing.T) {
	ownerStore := testStore(t)
	ctx := context.Background()

	roleName := "kb_get_links_rls_test_user"
	rolePassword := "kb_get_links_rls_test_password"

	t.Cleanup(func() {
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE kb_articles FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE kb_links FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE projects FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE agents FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE passports FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("DROP ROLE IF EXISTS %s", roleName))
	})

	if _, err := ownerStore.db.Exec(fmt.Sprintf("DROP ROLE IF EXISTS %s", roleName)); err != nil {
		t.Fatalf("failed to drop pre-existing role: %v", err)
	}
	if _, err := ownerStore.db.Exec(fmt.Sprintf("CREATE ROLE %s WITH LOGIN PASSWORD '%s'", roleName, rolePassword)); err != nil {
		t.Fatalf("failed to create role: %v", err)
	}
	if _, err := ownerStore.db.Exec(fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE kb_articles, kb_links, projects, agents, passports TO %s", roleName)); err != nil {
		t.Fatalf("failed to grant table privileges: %v", err)
	}

	cfg := types.LoadConfig()
	u, err := url.Parse(cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("failed to parse database URL: %v", err)
	}
	u.User = url.UserPassword(roleName, rolePassword)
	restrictedDSN := u.String()

	restrictedDB, err := sql.Open("postgres", restrictedDSN)
	if err != nil {
		t.Fatalf("failed to open restricted db connection: %v", err)
	}
	t.Cleanup(func() { restrictedDB.Close() })

	if err := restrictedDB.PingContext(ctx); err != nil {
		t.Fatalf("failed to ping restricted database: %v", err)
	}

	projectA := createProject(t, ownerStore, "kb-get-links-isolation-a")
	projectB := createProject(t, ownerStore, "kb-get-links-isolation-b")
	agentID := createAgent(t, ownerStore)
	createPassport(t, ownerStore, agentID, projectA)
	createPassport(t, ownerStore, agentID, projectB)

	// Write two articles in project A and link them via owner store.
	target, err := ownerStore.WriteArticle(ctx, projectA, agentID, "target article", "target body", nil, nil, false)
	if err != nil {
		t.Fatalf("WriteArticle target (project A): %v", err)
	}
	source, err := ownerStore.WriteArticle(ctx, projectA, agentID, "source article", "source body", nil, []string{target.ID}, false)
	if err != nil {
		t.Fatalf("WriteArticle source (project A): %v", err)
	}

	// Call GetArticleLinks scoped to project B on project A's source article.
	// The source article check must return ErrArticleNotFound (RLS hides it).
	restrictedStore := NewStore(restrictedDB, StubEmbedder{}, 0.85, 2000, 1, 1, 1)
	_, err = restrictedStore.GetArticleLinks(ctx, projectB, agentID, source.ID)
	if !errors.Is(err, ErrArticleNotFound) {
		t.Fatalf("expected ErrArticleNotFound (RLS isolation), got: %v", err)
	}
}

func TestGetArticleLinks_NoPassport(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "kb-get-links-no-passport")
	agentID := createAgent(t, s)

	_, err := s.GetArticleLinks(ctx, projectID, agentID, "00000000-0000-0000-0000-000000000000")
	if !errors.Is(err, ErrPassportNotFound) {
		t.Fatalf("expected ErrPassportNotFound, got: %v", err)
	}
}
