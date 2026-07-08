package mcp

import (
	"context"
	"encoding/json"
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
}

// WriteArticleOutput is the wormhole.kb.write result shape.
type WriteArticleOutput struct {
	ArticleID string    `json:"article_id"`
	ProjectID string    `json:"project_id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
}

// WriteArticleTool wires wormhole.kb.write. The authenticated agent's ID is
// recorded as the article's author. No compliance checks (dedup,
// conciseness, required links) run here; that's deferred per docs/kb-schema.md.
func WriteArticleTool(store *kb.Store) Tool {
	return Tool{
		Name:         "wormhole.kb.write",
		Description:  "Writes an atomic knowledge base article, optionally linked to existing articles. No compliance checks or embeddings yet.",
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
			article, err := store.WriteArticle(ctx, projectID, scope.Agent.ID, in.Title, in.Body, frontmatter, in.Links)
			if err != nil {
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
