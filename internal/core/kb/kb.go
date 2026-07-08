// Package kb implements the Knowledge Base pillar's write path (RFC-0001
// §8.3): atomic, explicitly linked articles. Day 13 shipped this as
// plumbing only: no compliance checks (semantic dedup, conciseness, required
// links, all RFC-0001 §15 open-question territory per docs/kb-schema.md).
// Day 14 wires in a deterministic stub embedding pipeline (see StubEmbedder)
// so every write populates the embedding column; the real embedding
// provider choice stays open (RFC-0001 §15).
//
// This package stays isolated per architecture.md R2: it does not import
// internal/core/tasks or internal/core/events. Link-target existence checks
// are done with raw SQL against the kb_articles table directly, the same way
// git.LinkCommit checks task existence without importing tasks.
package kb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var ErrPassportNotFound = errors.New("kb: agent not registered or has no passport for this project")
var ErrLinkedArticleNotFound = errors.New("kb: linked article not found")

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


// Article is a single atomic knowledge base entry (RFC-0001 §8.3: one
// article = one fact, decision, or procedure).
type Article struct {
	ID            string
	ProjectID     string
	Title         string
	Body          string
	Frontmatter   json.RawMessage
	AuthorAgentID string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Embedder produces a vector embedding for a piece of text. It is the seam
// between this package's write/search plumbing and whatever embedding
// provider is in use; RFC-0001 §15 leaves the provider choice open, so
// callers plug in an implementation rather than this package hardcoding one.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// StubEmbedder is a deterministic, dependency-free placeholder Embedder.
//
// It is NOT semantically meaningful: its output does not capture anything
// about the meaning of the input text. It exists only to make the
// wormhole.kb.write / wormhole.kb.search pipeline real and testable end to
// end (populated embedding column, working pgvector distance ranking)
// without committing to an external embedding API or a new Go dependency
// before that choice is made (docs/superpowers/plans/2026-07-20-day14-kb-embeddings-search.md,
// "Embedding provider decision"; RFC-0001 §15 open question). The real
// provider is deferred to a later day.
//
// Algorithm: sha256 the input text, take the first 16 bytes of the digest,
// map each byte b to a float32 via (float32(b)-128)/128.0, producing a
// fixed 16-dimensional vector in roughly [-1, 1). The dimension (16) is an
// arbitrary placeholder, not a modeling decision; a future real-provider
// swap should not assume this dimension is load-bearing.
type StubEmbedder struct{}

// stubEmbeddingDim is the fixed, arbitrary dimension of StubEmbedder's
// output. Not a modeling decision, see StubEmbedder's doc comment.
const stubEmbeddingDim = 16

func (StubEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	digest := sha256.Sum256([]byte(text))
	vec := make([]float32, stubEmbeddingDim)
	for i := 0; i < stubEmbeddingDim; i++ {
		vec[i] = (float32(digest[i]) - 128) / 128.0
	}
	return vec, nil
}

// formatVectorLiteral renders a []float32 as a pgvector text-input literal,
// e.g. "[0.1,0.2,0.3]" (comma-separated, no spaces), for use with a
// $N::vector cast. lib/pq has no native vector type, so this is done by
// hand.
func formatVectorLiteral(vec []float32) string {
	parts := make([]string, len(vec))
	for i, v := range vec {
		parts[i] = strconv.FormatFloat(float64(v), 'f', -1, 32)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

type Store struct {
	db             *sql.DB
	embedder       Embedder
	dedupThreshold float64
}

func NewStore(db *sql.DB, embedder Embedder, dedupThreshold float64) *Store {
	return &Store{db: db, embedder: embedder, dedupThreshold: dedupThreshold}
}

const articleColumns = `id, project_id, title, body, frontmatter, author_agent_id, created_at, updated_at`

// WriteArticle inserts a new KB article and, within the same transaction,
// validates and inserts each requested link to an existing article
// (RFC-0001 §9 wormhole.kb.write(title, body, links[])). If any link target
// does not exist in-project, the whole write rolls back: no partial article
// with dangling links is ever left behind.
func (s *Store) WriteArticle(ctx context.Context, projectID, agentID, title, body string, frontmatter json.RawMessage, linkTargetIDs []string, force bool) (Article, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Article{}, fmt.Errorf("kb: write article: begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectID); err != nil {
		return Article{}, fmt.Errorf("kb: write article: set project id: %w", err)
	}

	// Verify agent has a passport for this project.
	var dummy int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM passports WHERE agent_id = $1 AND project_id = $2", agentID, projectID).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return Article{}, fmt.Errorf("kb: write article: agent not registered or has no passport for this project: %w", ErrPassportNotFound)
	} else if err != nil {
		return Article{}, fmt.Errorf("kb: write article: passport lookup: %w", err)
	}

	if len(frontmatter) == 0 {
		frontmatter = json.RawMessage(`{}`)
	}

	embedding, err := s.embedder.Embed(ctx, body)
	if err != nil {
		return Article{}, fmt.Errorf("kb: write article: embed: %w", err)
	}

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

	row := tx.QueryRowContext(ctx,
		`INSERT INTO kb_articles (project_id, title, body, frontmatter, author_agent_id, embedding)
		 VALUES ($1, $2, $3, $4, $5, $6::vector)
		 RETURNING `+articleColumns,
		projectID, title, body, frontmatter, agentID, formatVectorLiteral(embedding),
	)

	var article Article
	err = row.Scan(&article.ID, &article.ProjectID, &article.Title, &article.Body, &article.Frontmatter, &article.AuthorAgentID, &article.CreatedAt, &article.UpdatedAt)
	if err != nil {
		return Article{}, fmt.Errorf("kb: write article: %w", err)
	}

	for _, targetID := range linkTargetIDs {
		err = tx.QueryRowContext(ctx, "SELECT 1 FROM kb_articles WHERE id = $1 AND project_id = $2", targetID, projectID).Scan(&dummy)
		if errors.Is(err, sql.ErrNoRows) {
			return Article{}, ErrLinkedArticleNotFound
		} else if err != nil {
			return Article{}, fmt.Errorf("kb: write article: link target lookup: %w", err)
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO kb_links (project_id, from_article_id, to_article_id) VALUES ($1, $2, $3)`,
			projectID, article.ID, targetID,
		); err != nil {
			return Article{}, fmt.Errorf("kb: write article: insert link: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return Article{}, fmt.Errorf("kb: write article: commit: %w", err)
	}
	return article, nil
}

// SearchArticles performs semantic search on KB articles using pgvector.
// It retrieves articles in the project ranked by cosine distance to the query embedding.
func (s *Store) SearchArticles(ctx context.Context, projectID, agentID, query string, limit int) ([]Article, error) {
	if limit <= 0 {
		limit = 10
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("kb: search articles: begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectID); err != nil {
		return nil, fmt.Errorf("kb: search articles: set project id: %w", err)
	}

	// Verify agent has a passport for this project.
	var dummy int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM passports WHERE agent_id = $1 AND project_id = $2", agentID, projectID).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("kb: search articles: agent not registered or has no passport for this project: %w", ErrPassportNotFound)
	} else if err != nil {
		return nil, fmt.Errorf("kb: search articles: passport lookup: %w", err)
	}

	embedding, err := s.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("kb: search articles: embed query: %w", err)
	}

	// We use the cosine distance operator (<=>) for semantic search ranking.
	// Unlike dot product, pgvector's cosine distance operator computes the normalized angle
	// (1 - cosine similarity) and does not require the vectors to be pre-normalized.
	// It only requires the vectors to be non-zero, which our stub embedding naturally guarantees.
	rows, err := tx.QueryContext(ctx,
		`SELECT `+articleColumns+`
		 FROM kb_articles
		 WHERE project_id = $1 AND embedding IS NOT NULL
		 ORDER BY embedding <=> $2::vector
		 LIMIT $3`,
		projectID, formatVectorLiteral(embedding), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("kb: search articles: query: %w", err)
	}
	defer rows.Close()

	var articles []Article
	for rows.Next() {
		var article Article
		err = rows.Scan(&article.ID, &article.ProjectID, &article.Title, &article.Body, &article.Frontmatter, &article.AuthorAgentID, &article.CreatedAt, &article.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("kb: search articles: scan: %w", err)
		}
		articles = append(articles, article)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("kb: search articles: iterate: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("kb: search articles: commit: %w", err)
	}

	return articles, nil
}

