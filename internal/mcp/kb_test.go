package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/H4RL33/wormhole/internal/core/kb"
)

// testKBStore returns a real kb.Store backed by Postgres. Skips the test if
// Postgres is not reachable (mirrors testGitStore's pattern).
func testKBStore(t *testing.T) *kb.Store {
	t.Helper()
	db := testDB(t)
	return kb.NewStore(db, kb.StubEmbedder{})
}

func TestKBTools_WriteArticle(t *testing.T) {
	store := testKBStore(t)
	projectID := mustCreateProject(t, "mcp-kb-write-article")
	agentID, _ := mustRegisterAgent(t, projectID)

	tool := WriteArticleTool(store)
	if tool.Name != "wormhole.kb.write" {
		t.Fatalf("Name: got %q, want %q", tool.Name, "wormhole.kb.write")
	}
	if !tool.RequiresAuth {
		t.Fatalf("RequiresAuth: got false, want true")
	}

	scope := mustBuildScope(agentID, projectID)
	arguments, _ := json.Marshal(WriteArticleInput{
		Title: "how to deploy",
		Body:  "run the deploy script",
		Links: nil,
	})

	result, err := tool.Handler(context.Background(), scope, projectID, arguments)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out, ok := result.(WriteArticleOutput)
	if !ok {
		t.Fatalf("result type: got %T, want WriteArticleOutput", result)
	}
	if out.ArticleID == "" {
		t.Fatalf("output missing ArticleID: %+v", out)
	}
	if out.ProjectID != projectID {
		t.Fatalf("ProjectID: got %q, want %q", out.ProjectID, projectID)
	}
	if out.Title != "how to deploy" {
		t.Fatalf("Title: got %q, want %q", out.Title, "how to deploy")
	}
	if out.CreatedAt.IsZero() {
		t.Fatalf("CreatedAt is zero")
	}
}

func TestKBTools_WriteArticleWithLinks(t *testing.T) {
	store := testKBStore(t)
	projectID := mustCreateProject(t, "mcp-kb-write-article-links")
	agentID, _ := mustRegisterAgent(t, projectID)
	scope := mustBuildScope(agentID, projectID)

	tool := WriteArticleTool(store)

	targetArgs, _ := json.Marshal(WriteArticleInput{
		Title: "target article",
		Body:  "target body",
	})
	targetResult, err := tool.Handler(context.Background(), scope, projectID, targetArgs)
	if err != nil {
		t.Fatalf("Handler (target): %v", err)
	}
	targetOut := targetResult.(WriteArticleOutput)

	linkingArgs, _ := json.Marshal(WriteArticleInput{
		Title: "linking article",
		Body:  "linking body",
		Links: []string{targetOut.ArticleID},
	})
	result, err := tool.Handler(context.Background(), scope, projectID, linkingArgs)
	if err != nil {
		t.Fatalf("Handler (linking): %v", err)
	}
	out, ok := result.(WriteArticleOutput)
	if !ok {
		t.Fatalf("result type: got %T, want WriteArticleOutput", result)
	}
	if out.ArticleID == "" {
		t.Fatalf("output missing ArticleID: %+v", out)
	}
}

func TestKBTools_WriteArticleUnknownLink(t *testing.T) {
	store := testKBStore(t)
	projectID := mustCreateProject(t, "mcp-kb-write-article-unknown-link")
	agentID, _ := mustRegisterAgent(t, projectID)

	tool := WriteArticleTool(store)
	scope := mustBuildScope(agentID, projectID)
	arguments, _ := json.Marshal(WriteArticleInput{
		Title: "orphaned article",
		Body:  "body",
		Links: []string{"00000000-0000-0000-0000-000000000000"},
	})

	_, err := tool.Handler(context.Background(), scope, projectID, arguments)
	if err == nil {
		t.Fatalf("Handler: got nil error, want error for unknown link target")
	}
	if !errors.Is(err, kb.ErrLinkedArticleNotFound) {
		t.Fatalf("Handler error: got %v, want to wrap ErrLinkedArticleNotFound", err)
	}
}
