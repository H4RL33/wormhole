package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/H4RL33/wormhole/internal/core/git"
	"github.com/H4RL33/wormhole/internal/core/identity"
)

// LinkCommitInput is the wormhole.git.link_commit argument shape.
type LinkCommitInput struct {
	TaskID    string `json:"task_id"`
	Repo      string `json:"repo"`
	CommitSHA string `json:"commit_sha"`
	Summary   string `json:"summary"`
}

// LinkCommitOutput is the wormhole.git.link_commit result shape.
type LinkCommitOutput struct {
	GitLinkID string    `json:"git_link_id"`
	ProjectID string    `json:"project_id"`
	TaskID    string    `json:"task_id"`
	Repo      string    `json:"repo"`
	CommitSHA string    `json:"commit_sha"`
	Summary   string    `json:"summary"`
	CreatedAt time.Time `json:"created_at"`
}

// LinkCommitTool wires wormhole.git.link_commit. The authenticated agent's ID
// is recorded as the link's author.
func LinkCommitTool(store *git.Store) Tool {
	return Tool{
		Name:             "wormhole.git.link_commit",
		Description:      "Records a manual pointer from a task to a commit. Wormhole never stores or mirrors code, only the pointer and a summary.",
		RequiresAuth:     true,
		ArgumentsExample: LinkCommitInput{},
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			var in LinkCommitInput
			if err := json.Unmarshal(arguments, &in); err != nil {
				return nil, fmt.Errorf("mcp: decode wormhole.git.link_commit arguments: %w", err)
			}
			taskID := in.TaskID
			link, err := store.LinkCommit(ctx, projectID, scope.Agent.ID, &taskID, in.Repo, in.CommitSHA, in.Summary)
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.git.link_commit: %w", err)
			}
			var linkedTaskID string
			if link.TaskID != nil {
				linkedTaskID = *link.TaskID
			}
			var commitSHA string
			if link.CommitSHA != nil {
				commitSHA = *link.CommitSHA
			}
			return LinkCommitOutput{
				GitLinkID: link.ID,
				ProjectID: link.ProjectID,
				TaskID:    linkedTaskID,
				Repo:      link.Repo,
				CommitSHA: commitSHA,
				Summary:   link.Summary,
				CreatedAt: link.CreatedAt,
			}, nil
		},
	}
}

// RequestReviewInput is the wormhole.git.request_review argument shape.
type RequestReviewInput struct {
	Repo    string `json:"repo"`
	PRUrl   string `json:"pr_url"`
	Summary string `json:"summary"`
}

// RequestReviewOutput is the wormhole.git.request_review result shape.
type RequestReviewOutput struct {
	GitLinkID string    `json:"git_link_id"`
	ProjectID string    `json:"project_id"`
	Repo      string    `json:"repo"`
	PRUrl     string    `json:"pr_url"`
	Summary   string    `json:"summary"`
	CreatedAt time.Time `json:"created_at"`
}

// RequestReviewTool wires wormhole.git.request_review. This operation has no
// task_id per RFC-0001 §9's signature.
func RequestReviewTool(store *git.Store) Tool {
	return Tool{
		Name:             "wormhole.git.request_review",
		Description:      "Records a manual review request pointer for a repo and PR URL, with no task association.",
		RequiresAuth:     true,
		ArgumentsExample: RequestReviewInput{},
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			var in RequestReviewInput
			if err := json.Unmarshal(arguments, &in); err != nil {
				return nil, fmt.Errorf("mcp: decode wormhole.git.request_review arguments: %w", err)
			}
			link, err := store.RequestReview(ctx, projectID, scope.Agent.ID, in.Repo, in.PRUrl, in.Summary)
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.git.request_review: %w", err)
			}
			var prURL string
			if link.PRUrl != nil {
				prURL = *link.PRUrl
			}
			return RequestReviewOutput{
				GitLinkID: link.ID,
				ProjectID: link.ProjectID,
				Repo:      link.Repo,
				PRUrl:     prURL,
				Summary:   link.Summary,
				CreatedAt: link.CreatedAt,
			}, nil
		},
	}
}
