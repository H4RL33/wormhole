// Package kb implements the Knowledge Base pillar's write path (RFC-0001
// §8.3): atomic, explicitly linked articles. This is plumbing only, per Day
// 13 scope: no compliance checks (semantic dedup, conciseness, required
// links, all RFC-0001 §15 open-question territory per docs/kb-schema.md) and
// no embedding generation (Day 14's concern; the embedding column exists but
// stays NULL/unpopulated here).
//
// This package stays isolated per architecture.md R2: it does not import
// internal/core/tasks or internal/core/events. Link-target existence checks
// are done with raw SQL against the kb_articles table directly, the same way
// git.LinkCommit checks task existence without importing tasks.
package kb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrPassportNotFound = errors.New("kb: agent not registered or has no passport for this project")
var ErrLinkedArticleNotFound = errors.New("kb: linked article not found")

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

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

const articleColumns = `id, project_id, title, body, frontmatter, author_agent_id, created_at, updated_at`

// WriteArticle inserts a new KB article and, within the same transaction,
// validates and inserts each requested link to an existing article
// (RFC-0001 §9 wormhole.kb.write(title, body, links[])). If any link target
// does not exist in-project, the whole write rolls back: no partial article
// with dangling links is ever left behind.
func (s *Store) WriteArticle(ctx context.Context, projectID, agentID, title, body string, frontmatter json.RawMessage, linkTargetIDs []string) (Article, error) {
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

	row := tx.QueryRowContext(ctx,
		`INSERT INTO kb_articles (project_id, title, body, frontmatter, author_agent_id)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING `+articleColumns,
		projectID, title, body, frontmatter, agentID,
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
