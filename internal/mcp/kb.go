package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/core/kb"
)

// WriteArticleInput is the wormhole.kb.write argument shape.
type WriteArticleInput struct {
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	Frontmatter json.RawMessage `json:"frontmatter,omitempty"`
	Links       []string        `json:"links"`
	Force       bool            `json:"force"`
}

// WriteArticleOutput is the wormhole.kb.write result shape.
type WriteArticleOutput struct {
	ArticleID string    `json:"article_id"`
	ProjectID string    `json:"project_id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
}

// WriteArticleTool wires wormhole.kb.write. The authenticated agent's ID is
// recorded as the article's author.
func WriteArticleTool(store *kb.Store) Tool {
	return Tool{
		Name:         "wormhole.kb.write",
		Description:  "Writes an atomic knowledge base article, optionally linked to existing articles. Validates semantic deduplication unless bypassed with force=true.",
		RequiresAuth: true,
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			var in WriteArticleInput
			if err := json.Unmarshal(arguments, &in); err != nil {
				return nil, fmt.Errorf("mcp: decode wormhole.kb.write arguments: %w", err)
			}
			frontmatter := in.Frontmatter
			if len(frontmatter) == 0 {
				frontmatter = json.RawMessage(`{}`)
			}
			article, err := store.WriteArticle(ctx, projectID, scope.Agent.ID, in.Title, in.Body, frontmatter, in.Links, in.Force)
			if err != nil {
				var dedupErr *kb.ErrDedupViolation
				if errors.As(err, &dedupErr) {
					return nil, dedupErr
				}
				return nil, fmt.Errorf("mcp: wormhole.kb.write: %w", err)
			}
			return WriteArticleOutput{
				ArticleID: article.ID,
				ProjectID: article.ProjectID,
				Title:     article.Title,
				CreatedAt: article.CreatedAt,
			}, nil
		},
	}
}

// SearchArticlesInput is the wormhole.kb.search argument shape.
type SearchArticlesInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

// ArticleSummary is one article's shape within SearchArticlesOutput.
type ArticleSummary struct {
	ArticleID     string          `json:"article_id"`
	ProjectID     string          `json:"project_id"`
	Title         string          `json:"title"`
	Body          string          `json:"body"`
	Frontmatter   json.RawMessage `json:"frontmatter,omitempty"`
	AuthorAgentID string          `json:"author_agent_id"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// SearchArticlesOutput is the wormhole.kb.search result shape.
type SearchArticlesOutput struct {
	Articles []ArticleSummary `json:"articles"`
}

// SearchArticlesTool wires wormhole.kb.search.
func SearchArticlesTool(store *kb.Store) Tool {
	return Tool{
		Name:         "wormhole.kb.search",
		Description:  "Searches the knowledge base using semantic search, ranked by similarity to the query.",
		RequiresAuth: true,
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			var in SearchArticlesInput
			if err := json.Unmarshal(arguments, &in); err != nil {
				return nil, fmt.Errorf("mcp: decode wormhole.kb.search arguments: %w", err)
			}
			if in.Limit == 0 {
				in.Limit = 10
			}
			articleList, err := store.SearchArticles(ctx, projectID, scope.Agent.ID, in.Query, in.Limit)
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.kb.search: %w", err)
			}
			out := SearchArticlesOutput{Articles: make([]ArticleSummary, 0, len(articleList))}
			for _, article := range articleList {
				out.Articles = append(out.Articles, ArticleSummary{
					ArticleID:     article.ID,
					ProjectID:     article.ProjectID,
					Title:         article.Title,
					Body:          article.Body,
					Frontmatter:   article.Frontmatter,
					AuthorAgentID: article.AuthorAgentID,
					CreatedAt:     article.CreatedAt,
					UpdatedAt:     article.UpdatedAt,
				})
			}
			return out, nil
		},
	}
}

