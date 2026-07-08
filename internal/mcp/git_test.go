package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/H4RL33/wormhole/internal/core/git"
)

// testGitStore returns a real git.Store backed by Postgres. Skips the test
// if Postgres is not reachable (mirrors testEventsStore's pattern).
func testGitStore(t *testing.T) *git.Store {
	t.Helper()
	db := testDB(t)
	return git.NewStore(db)
}

// mustCreateTask inserts a task row directly (the tasks MCP tools are out of
// scope for these tests; we only need a task id for git_links to point at).
func mustCreateTask(t *testing.T, db *sql.DB, projectID, title string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`INSERT INTO tasks (project_id, title) VALUES ($1, $2) RETURNING id`, projectID, title).Scan(&id); err != nil {
		t.Fatalf("create task: %v", err)
	}
	return id
}

func TestGitTools_LinkCommit(t *testing.T) {
	store := testGitStore(t)
	projectID := mustCreateProject(t, "mcp-git-link-commit")
	agentID, _ := mustRegisterAgent(t, projectID)
	taskID := mustCreateTask(t, testDB(t), projectID, "fix the thing")

	tool := LinkCommitTool(store)
	if tool.Name != "wormhole.git.link_commit" {
		t.Fatalf("Name: got %q, want %q", tool.Name, "wormhole.git.link_commit")
	}
	if !tool.RequiresAuth {
		t.Fatalf("RequiresAuth: got false, want true")
	}

	scope := mustBuildScope(agentID, projectID)
	arguments, _ := json.Marshal(LinkCommitInput{
		TaskID:    taskID,
		Repo:      "github.com/example/repo",
		CommitSHA: "deadbeef",
		Summary:   "fixed the thing",
	})

	result, err := tool.Handler(context.Background(), scope, projectID, arguments)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out, ok := result.(LinkCommitOutput)
	if !ok {
		t.Fatalf("result type: got %T, want LinkCommitOutput", result)
	}
	if out.GitLinkID == "" {
		t.Fatalf("output missing GitLinkID: %+v", out)
	}
	if out.ProjectID != projectID {
		t.Fatalf("ProjectID: got %q, want %q", out.ProjectID, projectID)
	}
	if out.TaskID != taskID {
		t.Fatalf("TaskID: got %q, want %q", out.TaskID, taskID)
	}
	if out.CommitSHA != "deadbeef" {
		t.Fatalf("CommitSHA: got %q, want %q", out.CommitSHA, "deadbeef")
	}
	if out.Summary != "fixed the thing" {
		t.Fatalf("Summary: got %q, want %q", out.Summary, "fixed the thing")
	}
}

func TestGitTools_LinkCommitUnknownTask(t *testing.T) {
	store := testGitStore(t)
	projectID := mustCreateProject(t, "mcp-git-link-commit-unknown-task")
	agentID, _ := mustRegisterAgent(t, projectID)

	tool := LinkCommitTool(store)
	scope := mustBuildScope(agentID, projectID)
	arguments, _ := json.Marshal(LinkCommitInput{
		TaskID:    "00000000-0000-0000-0000-000000000000",
		Repo:      "github.com/example/repo",
		CommitSHA: "deadbeef",
		Summary:   "fixed the thing",
	})

	_, err := tool.Handler(context.Background(), scope, projectID, arguments)
	if err == nil {
		t.Fatalf("Handler: got nil error, want error for unknown task id")
	}
	if !errors.Is(err, git.ErrTaskNotFound) {
		t.Fatalf("Handler error: got %v, want to wrap ErrTaskNotFound", err)
	}
}

func TestGitTools_RequestReview(t *testing.T) {
	store := testGitStore(t)
	projectID := mustCreateProject(t, "mcp-git-request-review")
	agentID, _ := mustRegisterAgent(t, projectID)

	tool := RequestReviewTool(store)
	if tool.Name != "wormhole.git.request_review" {
		t.Fatalf("Name: got %q, want %q", tool.Name, "wormhole.git.request_review")
	}
	if !tool.RequiresAuth {
		t.Fatalf("RequiresAuth: got false, want true")
	}

	scope := mustBuildScope(agentID, projectID)
	arguments, _ := json.Marshal(RequestReviewInput{
		Repo:    "github.com/example/repo",
		PRUrl:   "https://github.com/example/repo/pull/9",
		Summary: "please review",
	})

	result, err := tool.Handler(context.Background(), scope, projectID, arguments)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out, ok := result.(RequestReviewOutput)
	if !ok {
		t.Fatalf("result type: got %T, want RequestReviewOutput", result)
	}
	if out.GitLinkID == "" {
		t.Fatalf("output missing GitLinkID: %+v", out)
	}
	if out.ProjectID != projectID {
		t.Fatalf("ProjectID: got %q, want %q", out.ProjectID, projectID)
	}
	if out.PRUrl != "https://github.com/example/repo/pull/9" {
		t.Fatalf("PRUrl: got %q, want %q", out.PRUrl, "https://github.com/example/repo/pull/9")
	}
	if out.Summary != "please review" {
		t.Fatalf("Summary: got %q, want %q", out.Summary, "please review")
	}
}
