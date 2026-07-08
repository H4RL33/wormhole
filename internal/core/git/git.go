// Package git implements the Git integration supporting flow (RFC-0001 §8.6):
// manual pointers only (repo, commit SHA or PR URL, and a summary). Wormhole
// never stores or mirrors code, only pointers plus commentary
// (architecture.md §6 "Git integration"). Alpha scope is link_commit and
// request_review; no webhooks, no CI hooks, no repo cloning, no diff storage.
//
// This package stays isolated per architecture.md R2: it does not import
// internal/core/tasks or internal/core/events. Task-existence checks are done
// with raw SQL against the tasks table directly, the same way
// events.PublishEvent checks channel existence without importing tasks.
package git

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var ErrTaskNotFound = errors.New("git: task not found")
var ErrPassportNotFound = errors.New("git: agent not registered or has no passport for this project")

// GitLink is a manual pointer from a project (and optionally a task) to a
// commit or a pull request. Exactly one of CommitSHA/PRUrl is set.
type GitLink struct {
	ID        string
	ProjectID string
	TaskID    *string
	Repo      string
	CommitSHA *string
	PRUrl     *string
	Summary   string
	AgentID   string
	CreatedAt time.Time
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

const gitLinkColumns = `id, project_id, task_id, repo, commit_sha, pr_url, summary, agent_id, created_at`

// LinkCommit records a pointer from a task to a commit (RFC-0001 §9
// wormhole.git.link_commit(task_id, repo, commit_sha, summary)).
func (s *Store) LinkCommit(ctx context.Context, projectID, agentID string, taskID *string, repo, commitSHA, summary string) (GitLink, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return GitLink{}, fmt.Errorf("git: link commit: begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectID); err != nil {
		return GitLink{}, fmt.Errorf("git: link commit: set project id: %w", err)
	}

	// Verify agent has a passport for this project.
	var dummy int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM passports WHERE agent_id = $1 AND project_id = $2", agentID, projectID).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return GitLink{}, fmt.Errorf("git: link commit: agent not registered or has no passport for this project: %w", ErrPassportNotFound)
	} else if err != nil {
		return GitLink{}, fmt.Errorf("git: link commit: passport lookup: %w", err)
	}

	// Verify the task exists in this project.
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM tasks WHERE id = $1 AND project_id = $2", taskID, projectID).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return GitLink{}, ErrTaskNotFound
	} else if err != nil {
		return GitLink{}, fmt.Errorf("git: link commit: task lookup: %w", err)
	}

	row := tx.QueryRowContext(ctx,
		`INSERT INTO git_links (project_id, task_id, repo, commit_sha, pr_url, summary, agent_id)
		 VALUES ($1, $2, $3, $4, NULL, $5, $6)
		 RETURNING `+gitLinkColumns,
		projectID, taskID, repo, commitSHA, summary, agentID,
	)

	var link GitLink
	err = row.Scan(&link.ID, &link.ProjectID, &link.TaskID, &link.Repo, &link.CommitSHA, &link.PRUrl, &link.Summary, &link.AgentID, &link.CreatedAt)
	if err != nil {
		return GitLink{}, fmt.Errorf("git: link commit: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return GitLink{}, fmt.Errorf("git: link commit: commit: %w", err)
	}
	return link, nil
}

// RequestReview records a review request pointer with no task association
// (RFC-0001 §9 wormhole.git.request_review(repo, pr_url, summary)).
func (s *Store) RequestReview(ctx context.Context, projectID, agentID, repo, prURL, summary string) (GitLink, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return GitLink{}, fmt.Errorf("git: request review: begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectID); err != nil {
		return GitLink{}, fmt.Errorf("git: request review: set project id: %w", err)
	}

	// Verify agent has a passport for this project.
	var dummy int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM passports WHERE agent_id = $1 AND project_id = $2", agentID, projectID).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return GitLink{}, fmt.Errorf("git: request review: agent not registered or has no passport for this project: %w", ErrPassportNotFound)
	} else if err != nil {
		return GitLink{}, fmt.Errorf("git: request review: passport lookup: %w", err)
	}

	row := tx.QueryRowContext(ctx,
		`INSERT INTO git_links (project_id, task_id, repo, commit_sha, pr_url, summary, agent_id)
		 VALUES ($1, NULL, $2, NULL, $3, $4, $5)
		 RETURNING `+gitLinkColumns,
		projectID, repo, prURL, summary, agentID,
	)

	var link GitLink
	err = row.Scan(&link.ID, &link.ProjectID, &link.TaskID, &link.Repo, &link.CommitSHA, &link.PRUrl, &link.Summary, &link.AgentID, &link.CreatedAt)
	if err != nil {
		return GitLink{}, fmt.Errorf("git: request review: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return GitLink{}, fmt.Errorf("git: request review: commit: %w", err)
	}
	return link, nil
}
