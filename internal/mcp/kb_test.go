package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/H4RL33/wormhole/internal/core/kb"
)

// testKBStore returns a real kb.Store backed by Postgres. Skips the test if
// Postgres is not reachable (mirrors testGitStore's pattern).
func testKBStore(t *testing.T) *kb.Store {
	t.Helper()
	db := testDB(t)
	return kb.NewStore(db, kb.StubEmbedder{}, 0.85, 2000, 1, 1, 1)
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

func TestKBTools_SearchArticles(t *testing.T) {
	store := testKBStore(t)
	projectID := mustCreateProject(t, "mcp-kb-search-articles")
	agentID, _ := mustRegisterAgent(t, projectID)
	scope := mustBuildScope(agentID, projectID)

	writeTool := WriteArticleTool(store)
	searchTool := SearchArticlesTool(store)

	if searchTool.Name != "wormhole.kb.search" {
		t.Fatalf("Name: got %q, want %q", searchTool.Name, "wormhole.kb.search")
	}
	if !searchTool.RequiresAuth {
		t.Fatalf("RequiresAuth: got false, want true")
	}

	// Write two articles
	args1, _ := json.Marshal(WriteArticleInput{
		Title: "deployment instructions",
		Body:  "run script using production config",
	})
	if _, err := writeTool.Handler(context.Background(), scope, projectID, args1); err != nil {
		t.Fatalf("write article 1: %v", err)
	}

	args2, _ := json.Marshal(WriteArticleInput{
		Title: "setup instructions",
		Body:  "install docker daemon and run compose",
	})
	if _, err := writeTool.Handler(context.Background(), scope, projectID, args2); err != nil {
		t.Fatalf("write article 2: %v", err)
	}

	// Search for the second article's body
	searchArgs, _ := json.Marshal(SearchArticlesInput{
		Query: "install docker daemon and run compose",
		Limit: 10,
	})
	result, err := searchTool.Handler(context.Background(), scope, projectID, searchArgs)
	if err != nil {
		t.Fatalf("Search handler: %v", err)
	}

	out, ok := result.(SearchArticlesOutput)
	if !ok {
		t.Fatalf("result type: got %T, want SearchArticlesOutput", result)
	}

	if len(out.Articles) != 2 {
		t.Fatalf("expected 2 search results, got %d", len(out.Articles))
	}

	// The first article in results should be the second one we wrote (distance 0)
	if out.Articles[0].Title != "setup instructions" {
		t.Errorf("expected first search result to be 'setup instructions', got %q", out.Articles[0].Title)
	}
}

func TestMcp_WriteArticle_DedupViolation(t *testing.T) {
	store := testKBStore(t)
	identityStore := testIdentityStore(t)
	projectID := mustCreateProject(t, "mcp-kb-dedup-violation")
	_, token := mustRegisterAgent(t, projectID)

	registry := NewRegistry()
	registry.Register(WriteArticleTool(store))
	handler := NewCallHandler(registry, identityStore)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// 1. Write the first article.
	writeArgs1, _ := json.Marshal(WriteArticleInput{
		Title: "first article",
		Body:  "unique article body content for dedup test",
	})
	reqBody1, _ := json.Marshal(CallRequest{
		Tool:      "wormhole.kb.write",
		ProjectID: projectID,
		Arguments: writeArgs1,
	})

	req1, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(reqBody1))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("Authorization", "Bearer "+token)
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("first write POST: %v", err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first write status: got %d, want 200", resp1.StatusCode)
	}

	// 2. Write the duplicate article.
	writeArgs2, _ := json.Marshal(WriteArticleInput{
		Title: "second article",
		Body:  "unique article body content for dedup test",
	})
	reqBody2, _ := json.Marshal(CallRequest{
		Tool:      "wormhole.kb.write",
		ProjectID: projectID,
		Arguments: writeArgs2,
	})

	req2, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(reqBody2))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer "+token)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("second write POST: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("second write status: got %d, want 400 (BadRequest)", resp2.StatusCode)
	}

	var callResp CallResponse
	if err := json.NewDecoder(resp2.Body).Decode(&callResp); err != nil {
		t.Fatalf("decode call response: %v", err)
	}

	if callResp.Error == "" {
		t.Fatal("expected error field in CallResponse, got empty string")
	}

	// Verify the error is valid raw JSON (not wrapped/prefixed)
	var parsedErr struct {
		Error          string `json:"error"`
		Code           string `json:"code"`
		ClosestArticle struct {
			ID         string  `json:"id"`
			Title      string  `json:"title"`
			Similarity float64 `json:"similarity"`
		} `json:"closest_article"`
		Suggestion string `json:"suggestion"`
	}
	if err := json.Unmarshal([]byte(callResp.Error), &parsedErr); err != nil {
		t.Fatalf("expected CallResponse.Error to be valid raw JSON, got: %q (unmarshal error: %v)", callResp.Error, err)
	}

	if parsedErr.Code != "DEDUP_VIOLATION" {
		t.Errorf("expected Code to be 'DEDUP_VIOLATION', got: %q", parsedErr.Code)
	}
	if parsedErr.Error != "kb: write article: semantic duplicate found" {
		t.Errorf("expected Error to be 'kb: write article: semantic duplicate found', got: %q", parsedErr.Error)
	}
}

