package localstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// GitLink is Gateway's local durable copy of one task-to-commit pointer.
// It contains metadata only; Wormhole never stores repository code.
type GitLink struct {
	ID        string
	ProjectID string
	TaskID    string
	Repo      string
	CommitSHA string
	Summary   string
	AgentID   string
	CreatedAt time.Time
}

// GitRepo persists local-first Git pointers in Gateway's SQLite store.
type GitRepo struct {
	db *sql.DB
}

var ErrGitStableIDConflict = errors.New("localstore/git: stable id conflict")

func NewGitRepo(db *sql.DB) *GitRepo {
	return &GitRepo{db: db}
}

// LinkCommitTx validates and inserts a Git pointer using the caller's
// transaction so the pointer and its outbound sync entry commit atomically.
func (r *GitRepo) LinkCommitTx(ctx context.Context, tx *sql.Tx, projectID, agentID, taskID, repo, commitSHA, summary string) (GitLink, error) {
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(agentID) == "" || strings.TrimSpace(taskID) == "" || strings.TrimSpace(repo) == "" || strings.TrimSpace(commitSHA) == "" || strings.TrimSpace(summary) == "" {
		return GitLink{}, errors.New("localstore/git: project_id, agent_id, task_id, repo, commit_sha, and summary are required")
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM tasks WHERE id = ? AND namespace_id = ?`, taskID, projectID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return GitLink{}, ErrTaskNotFound
	} else if err != nil {
		return GitLink{}, fmt.Errorf("localstore/git: inspect task: %w", err)
	}
	link := GitLink{
		ID: uuid.NewString(), ProjectID: projectID, TaskID: taskID, Repo: repo,
		CommitSHA: commitSHA, Summary: summary, AgentID: agentID, CreatedAt: time.Now().UTC(),
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO git_links (id, project_id, task_id, repo, commit_sha, summary, agent_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		link.ID, link.ProjectID, link.TaskID, link.Repo, link.CommitSHA, link.Summary, link.AgentID, link.CreatedAt); err != nil {
		return GitLink{}, fmt.Errorf("localstore/git: link commit: %w", err)
	}
	return link, nil
}

// UpsertFromServer applies one Fabric Git pointer under its stable ID.
func (r *GitRepo) UpsertFromServer(ctx context.Context, link GitLink) error {
	var taskExists int
	if err := r.db.QueryRowContext(ctx, `SELECT 1 FROM tasks WHERE id = ? AND namespace_id = ?`, link.TaskID, link.ProjectID).Scan(&taskExists); err != nil {
		return fmt.Errorf("localstore/git: apply pointer task: %w", err)
	}
	result, err := r.db.ExecContext(ctx, `INSERT INTO git_links (id, project_id, task_id, repo, commit_sha, summary, agent_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
		link.ID, link.ProjectID, link.TaskID, link.Repo, link.CommitSHA, link.Summary, link.AgentID, link.CreatedAt)
	if err != nil {
		return fmt.Errorf("localstore/git: apply pointer: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 1 {
		return nil
	}
	var actual GitLink
	if err := r.db.QueryRowContext(ctx, `SELECT id, project_id, task_id, repo, commit_sha, summary, agent_id, created_at FROM git_links WHERE id = ?`, link.ID).
		Scan(&actual.ID, &actual.ProjectID, &actual.TaskID, &actual.Repo, &actual.CommitSHA, &actual.Summary, &actual.AgentID, &actual.CreatedAt); err != nil {
		return fmt.Errorf("localstore/git: inspect pointer replay: %w", err)
	}
	if actual.ProjectID != link.ProjectID || actual.TaskID != link.TaskID || actual.Repo != link.Repo || actual.CommitSHA != link.CommitSHA || actual.Summary != link.Summary || actual.AgentID != link.AgentID {
		return ErrGitStableIDConflict
	}
	if !actual.CreatedAt.Equal(link.CreatedAt) {
		if _, err := r.db.ExecContext(ctx, `UPDATE git_links SET created_at = ? WHERE id = ? AND project_id = ?`, link.CreatedAt, link.ID, link.ProjectID); err != nil {
			return fmt.Errorf("localstore/git: reconcile pointer timestamp: %w", err)
		}
	}
	return nil
}
