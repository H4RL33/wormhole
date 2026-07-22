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
		Name:               "wormhole.kb.write",
		Description:        "Writes an atomic knowledge base article, optionally linked to existing articles. Validates semantic deduplication unless bypassed with force=true.",
		RequiresAuth:       true,
		RequiredPermission: "kb.write",
		ArgumentsExample:   WriteArticleInput{},
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
				var concisenessErr *kb.ErrConcisenessViolation
				if errors.As(err, &concisenessErr) {
					return nil, concisenessErr
				}
				var requiredLinksErr *kb.ErrRequiredLinksViolation
				if errors.As(err, &requiredLinksErr) {
					return nil, requiredLinksErr
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
	ProjectID string `json:"project_id,omitempty"`
	Query     string `json:"query"`
	Limit     int    `json:"limit,omitempty"`
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
		Name:               "wormhole.kb.search",
		Description:        "Searches the knowledge base using semantic search, ranked by similarity to the query.",
		RequiresAuth:       true,
		RequiredPermission: "kb.search",
		ArgumentsExample:   SearchArticlesInput{},
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			var in SearchArticlesInput
			if err := json.Unmarshal(arguments, &in); err != nil {
				return nil, fmt.Errorf("mcp: decode wormhole.kb.search arguments: %w", err)
			}
			if in.ProjectID != "" && in.ProjectID != projectID {
				return nil, fmt.Errorf("mcp: project_id mismatch: got %q, authenticated as %q", in.ProjectID, projectID)
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

// GetArticleInput is the wormhole.kb.get argument shape.
type GetArticleInput struct {
	ArticleID string `json:"article_id"`
}

// GetArticleOutput is the wormhole.kb.get result shape.
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

// GetArticleTool wires wormhole.kb.get. Retrieves one KB article by ID
// within the authenticated agent's project scope.
func GetArticleTool(store *kb.Store) Tool {
	return Tool{
		Name:               "wormhole.kb.get",
		Description:        "Retrieves a single knowledge base article by ID within the authenticated agent's project scope.",
		RequiresAuth:       true,
		RequiredPermission: "kb.get",
		ArgumentsExample:   GetArticleInput{},
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			var in GetArticleInput
			if err := json.Unmarshal(arguments, &in); err != nil {
				return nil, fmt.Errorf("mcp: decode wormhole.kb.get arguments: %w", err)
			}
			article, err := store.GetArticle(ctx, projectID, scope.Agent.ID, in.ArticleID)
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.kb.get: %w", err)
			}
			return GetArticleOutput{
				ArticleID:     article.ID,
				ProjectID:     article.ProjectID,
				Title:         article.Title,
				Body:          article.Body,
				Frontmatter:   article.Frontmatter,
				AuthorAgentID: article.AuthorAgentID,
				CreatedAt:     article.CreatedAt,
				UpdatedAt:     article.UpdatedAt,
			}, nil
		},
	}
}

// GetArticleLinksInput is the wormhole.kb.get_links argument shape.
type GetArticleLinksInput struct {
	ArticleID string `json:"article_id"`
}

// GetArticleLinksOutput is the wormhole.kb.get_links result shape.
type GetArticleLinksOutput struct {
	ArticleID string           `json:"article_id"`
	Links     []ArticleSummary `json:"links"`
}

// GetArticleLinksTool wires wormhole.kb.get_links. Returns one-hop outbound
// linked articles for the given article (RFC-0001 §8.3 graph traversal).
func GetArticleLinksTool(store *kb.Store) Tool {
	return Tool{
		Name:               "wormhole.kb.get_links",
		Description:        "Returns the articles that a given article links to (one-hop outbound graph traversal of the kb_links graph, RFC-0001 §8.3).",
		RequiresAuth:       true,
		RequiredPermission: "kb.get_links",
		ArgumentsExample:   GetArticleLinksInput{},
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			var in GetArticleLinksInput
			if err := json.Unmarshal(arguments, &in); err != nil {
				return nil, fmt.Errorf("mcp: decode wormhole.kb.get_links arguments: %w", err)
			}
			linkedArticles, err := store.GetArticleLinks(ctx, projectID, scope.Agent.ID, in.ArticleID)
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.kb.get_links: %w", err)
			}
			links := make([]ArticleSummary, 0, len(linkedArticles))
			for _, a := range linkedArticles {
				links = append(links, ArticleSummary{
					ArticleID:     a.ID,
					ProjectID:     a.ProjectID,
					Title:         a.Title,
					Body:          a.Body,
					Frontmatter:   a.Frontmatter,
					AuthorAgentID: a.AuthorAgentID,
					CreatedAt:     a.CreatedAt,
					UpdatedAt:     a.UpdatedAt,
				})
			}
			return GetArticleLinksOutput{
				ArticleID: in.ArticleID,
				Links:     links,
			}, nil
		},
	}
}
