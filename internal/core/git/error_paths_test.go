package git

import (
	"context"
	"strings"
	"testing"
)

func TestGitWritesRejectMalformedIdentifiers(t *testing.T) {
	s := testStore(t)
	projectID := createProject(t, s, "git-malformed-identifiers")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	t.Run("link commit agent", func(t *testing.T) {
		taskID := createTask(t, s, projectID, "valid task")
		_, err := s.LinkCommit(context.Background(), projectID, "not-a-uuid", &taskID, "example/repo", "abc", "summary")
		if err == nil || !strings.Contains(err.Error(), "passport lookup") {
			t.Fatalf("LinkCommit error = %v, want passport lookup failure", err)
		}
	})

	t.Run("link commit task", func(t *testing.T) {
		malformedTaskID := "not-a-uuid"
		_, err := s.LinkCommit(context.Background(), projectID, agentID, &malformedTaskID, "example/repo", "abc", "summary")
		if err == nil || !strings.Contains(err.Error(), "task lookup") {
			t.Fatalf("LinkCommit error = %v, want task lookup failure", err)
		}
	})

	t.Run("request review agent", func(t *testing.T) {
		_, err := s.RequestReview(context.Background(), projectID, "not-a-uuid", "example/repo", "https://example.test/pr/1", "summary")
		if err == nil || !strings.Contains(err.Error(), "passport lookup") {
			t.Fatalf("RequestReview error = %v, want passport lookup failure", err)
		}
	})
}

func TestGitWritesPropagateCanceledContext(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	taskID := "00000000-0000-0000-0000-000000000000"

	if _, err := s.LinkCommit(ctx, taskID, taskID, &taskID, "example/repo", "abc", "summary"); err == nil || !strings.Contains(err.Error(), "begin tx") {
		t.Fatalf("LinkCommit error = %v, want begin tx cancellation", err)
	}
	if _, err := s.RequestReview(ctx, taskID, taskID, "example/repo", "https://example.test/pr/1", "summary"); err == nil || !strings.Contains(err.Error(), "begin tx") {
		t.Fatalf("RequestReview error = %v, want begin tx cancellation", err)
	}
}
