package localstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ErrArticleNotFound is returned when a KB article lookup has no matching row.
var ErrArticleNotFound = errors.New("localstore/kb: not found")

// KBArticle is a local replica of one knowledge base article (mirrors internal/core/kb.Article).
type KBArticle struct {
	ID            string
	NamespaceID   string // project_id in coordination-server terminology
	Title         string
	Body          string
	Frontmatter   json.RawMessage
	AuthorAgentID string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// KBLink represents a link between two KB articles.
type KBLink struct {
	FromArticleID string
	ToArticleID   string
}

// KBRepo provides a SQLite-backed KB article repository (mirrors internal/core/kb.Store shape).
// P2 scope: read-only path — no compliance checks (those stay server-side per RFC-0001 §13),
// no embedding/search (pgvector unavailable in SQLite).
type KBRepo struct {
	db *sql.DB
}

// NewKBRepo returns a new KB article repository backed by db.
func NewKBRepo(db *sql.DB) *KBRepo {
	return &KBRepo{db: db}
}

// WriteArticle inserts a new KB article in namespaceID (no compliance checks — server-side only).
func (r *KBRepo) WriteArticle(ctx context.Context, namespaceID, agentID, title, body string, frontmatter json.RawMessage) (KBArticle, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return KBArticle{}, fmt.Errorf("localstore/kb: write: begin tx: %w", err)
	}
	defer tx.Rollback()

	if len(frontmatter) == 0 {
		frontmatter = json.RawMessage(`{}`)
	}

	articleID := uuid.New().String()
	row := tx.QueryRowContext(ctx,
		`INSERT INTO kb_articles (id, namespace_id, title, body, frontmatter, author_agent_id)
		 VALUES (?, ?, ?, ?, ?, ?)
		 RETURNING id, namespace_id, title, body, frontmatter, author_agent_id, created_at, updated_at`,
		articleID, namespaceID, title, body, string(frontmatter), agentID,
	)
	article, err := scanKBArticle(row)
	if err != nil {
		return KBArticle{}, fmt.Errorf("localstore/kb: write: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return KBArticle{}, fmt.Errorf("localstore/kb: write: commit: %w", err)
	}
	return article, nil
}

// GetArticle returns the KB article in namespaceID with articleID, or ErrArticleNotFound.
func (r *KBRepo) GetArticle(ctx context.Context, namespaceID, articleID string) (KBArticle, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return KBArticle{}, fmt.Errorf("localstore/kb: get: begin tx: %w", err)
	}
	defer tx.Rollback()

	article, err := queryKBArticle(ctx, tx, namespaceID, articleID)
	if errors.Is(err, sql.ErrNoRows) {
		return KBArticle{}, ErrArticleNotFound
	}
	if err != nil {
		return KBArticle{}, fmt.Errorf("localstore/kb: get: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return KBArticle{}, fmt.Errorf("localstore/kb: get: commit: %w", err)
	}
	return article, nil
}

// ListArticles returns all KB articles in namespaceID, newest first.
func (r *KBRepo) ListArticles(ctx context.Context, namespaceID string) ([]KBArticle, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("localstore/kb: list: begin tx: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx,
		`SELECT id, namespace_id, title, body, frontmatter, author_agent_id, created_at, updated_at
		 FROM kb_articles WHERE namespace_id = ? ORDER BY created_at DESC`,
		namespaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("localstore/kb: list: %w", err)
	}
	defer rows.Close()

	articles := []KBArticle{}
	for rows.Next() {
		article, err := scanKBArticleRows(rows)
		if err != nil {
			return nil, fmt.Errorf("localstore/kb: list scan: %w", err)
		}
		articles = append(articles, article)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("localstore/kb: list iterate: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("localstore/kb: list: commit: %w", err)
	}
	return articles, nil
}

// GetArticleLinks returns the articles that articleID links to (outbound kb_links).
func (r *KBRepo) GetArticleLinks(ctx context.Context, namespaceID, articleID string) ([]KBLink, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("localstore/kb: get links: begin tx: %w", err)
	}
	defer tx.Rollback()

	// Verify source article exists in this namespace.
	var dummy int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM kb_articles WHERE id = ? AND namespace_id = ?", articleID, namespaceID).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrArticleNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("localstore/kb: get links: source lookup: %w", err)
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT from_article_id, to_article_id FROM kb_links WHERE from_article_id = ? AND namespace_id = ?`,
		articleID, namespaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("localstore/kb: get links: query: %w", err)
	}
	defer rows.Close()

	links := make([]KBLink, 0)
	for rows.Next() {
		var link KBLink
		if err := rows.Scan(&link.FromArticleID, &link.ToArticleID); err != nil {
			return nil, fmt.Errorf("localstore/kb: get links scan: %w", err)
		}
		links = append(links, link)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("localstore/kb: get links iterate: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("localstore/kb: get links: commit: %w", err)
	}
	return links, nil
}

// UpsertArticle inserts or replaces the KB article identified by articleID
// (server is authoritative — sync local-apply path, RFC-0003 §8.1/§8.2; the
// server already ran WriteArticle's compliance checks before returning this
// row, so they are not repeated here per this file's read-only-path scope
// note above).
func (r *KBRepo) UpsertArticle(ctx context.Context, namespaceID, articleID, title, body string, frontmatter json.RawMessage, authorAgentID string, createdAt, updatedAt time.Time) (KBArticle, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return KBArticle{}, fmt.Errorf("localstore/kb: upsert: begin tx: %w", err)
	}
	defer tx.Rollback()

	if len(frontmatter) == 0 {
		frontmatter = json.RawMessage(`{}`)
	}

	row := tx.QueryRowContext(ctx,
		`INSERT INTO kb_articles (id, namespace_id, title, body, frontmatter, author_agent_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
			namespace_id = excluded.namespace_id,
			title = excluded.title,
			body = excluded.body,
			frontmatter = excluded.frontmatter,
			author_agent_id = excluded.author_agent_id,
			created_at = excluded.created_at,
			updated_at = excluded.updated_at
		 RETURNING id, namespace_id, title, body, frontmatter, author_agent_id, created_at, updated_at`,
		articleID, namespaceID, title, body, string(frontmatter), authorAgentID, createdAt, updatedAt,
	)
	article, err := scanKBArticle(row)
	if err != nil {
		return KBArticle{}, fmt.Errorf("localstore/kb: upsert: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return KBArticle{}, fmt.Errorf("localstore/kb: upsert: commit: %w", err)
	}
	return article, nil
}

func queryKBArticle(ctx context.Context, tx *sql.Tx, namespaceID, articleID string) (KBArticle, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT id, namespace_id, title, body, frontmatter, author_agent_id, created_at, updated_at
		 FROM kb_articles WHERE id = ? AND namespace_id = ?`,
		articleID, namespaceID,
	)
	return scanKBArticle(row)
}

func scanKBArticle(row interface {
	Scan(...interface{}) error
}) (KBArticle, error) {
	return scanKBArticleRows(row)
}

func scanKBArticleRows(row interface {
	Scan(...interface{}) error
}) (KBArticle, error) {
	var article KBArticle
	var fmStr string
	err := row.Scan(
		&article.ID, &article.NamespaceID, &article.Title, &article.Body, &fmStr,
		&article.AuthorAgentID, &article.CreatedAt, &article.UpdatedAt,
	)
	if err != nil {
		return KBArticle{}, err
	}
	article.Frontmatter = json.RawMessage(fmStr)
	return article, nil
}
