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

func TestMcp_WriteArticle_ConcisenessViolation(t *testing.T) {
	db := testDB(t)
	store := kb.NewStore(db, kb.StubEmbedder{}, 0.85, 10, 1, 1, 1)
	identityStore := testIdentityStore(t)
	projectID := mustCreateProject(t, "mcp-kb-conciseness-violation")
	_, token := mustRegisterAgent(t, projectID)

	registry := NewRegistry()
	registry.Register(WriteArticleTool(store))
	handler := NewCallHandler(registry, identityStore)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	writeArgs, _ := json.Marshal(WriteArticleInput{
		Title: "too long article",
		Body:  "123456789012345",
	})
	reqBody, _ := json.Marshal(CallRequest{
		Tool:      "wormhole.kb.write",
		ProjectID: projectID,
		Arguments: writeArgs,
	})

	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("write POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("write status: got %d, want 400 (BadRequest)", resp.StatusCode)
	}

	var callResp CallResponse
	if err := json.NewDecoder(resp.Body).Decode(&callResp); err != nil {
		t.Fatalf("decode call response: %v", err)
	}

	if callResp.Error == "" {
		t.Fatal("expected error field in CallResponse, got empty string")
	}

	var parsedErr struct {
		Error   string `json:"error"`
		Code    string `json:"code"`
		Details struct {
			Length    int `json:"length"`
			MaxLength int `json:"max_length"`
		} `json:"details"`
		Suggestion string `json:"suggestion"`
	}
	if err := json.Unmarshal([]byte(callResp.Error), &parsedErr); err != nil {
		t.Fatalf("expected CallResponse.Error to be valid raw JSON, got: %q (unmarshal error: %v)", callResp.Error, err)
	}

	if parsedErr.Code != "CONCISENESS_VIOLATION" {
		t.Errorf("expected Code to be 'CONCISENESS_VIOLATION', got: %q", parsedErr.Code)
	}
	if parsedErr.Details.Length != 15 {
		t.Errorf("expected Details.Length to be 15, got: %d", parsedErr.Details.Length)
	}
	if parsedErr.Details.MaxLength != 10 {
		t.Errorf("expected Details.MaxLength to be 10, got: %d", parsedErr.Details.MaxLength)
	}
}

func TestMcp_WriteArticle_ConcisenessBypass(t *testing.T) {
	db := testDB(t)
	store := kb.NewStore(db, kb.StubEmbedder{}, 0.85, 10, 1, 1, 1)
	identityStore := testIdentityStore(t)
	projectID := mustCreateProject(t, "mcp-kb-conciseness-bypass")
	_, token := mustRegisterAgent(t, projectID)

	registry := NewRegistry()
	registry.Register(WriteArticleTool(store))
	handler := NewCallHandler(registry, identityStore)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	writeArgs, _ := json.Marshal(WriteArticleInput{
		Title: "too long article but forced",
		Body:  "123456789012345",
		Force: true,
	})
	reqBody, _ := json.Marshal(CallRequest{
		Tool:      "wormhole.kb.write",
		ProjectID: projectID,
		Arguments: writeArgs,
	})

	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("write POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("write status: got %d, want 200 (OK)", resp.StatusCode)
	}
}

func TestMcp_WriteArticle_RequiredLinksViolation(t *testing.T) {
	store := testKBStore(t)
	identityStore := testIdentityStore(t)
	projectID := mustCreateProject(t, "mcp-kb-req-links-violation")
	_, token := mustRegisterAgent(t, projectID)

	registry := NewRegistry()
	registry.Register(WriteArticleTool(store))
	handler := NewCallHandler(registry, identityStore)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// 1. Create a background article so we have a suggestion candidate.
	writeArgs1, _ := json.Marshal(WriteArticleInput{
		Title: "Existing Helpful Article",
		Body:  "Relevant content about something related to our database architecture",
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

	// Decode the first write response to get the ID.
	var write1Out CallResponse
	if err := json.NewDecoder(resp1.Body).Decode(&write1Out); err != nil {
		t.Fatalf("decode first write response: %v", err)
	}
	if write1Out.Error != "" {
		t.Fatalf("expected no error in first write, got: %q", write1Out.Error)
	}

	var write1OutVal WriteArticleOutput
	resultBytes, _ := json.Marshal(write1Out.Result)
	if err := json.Unmarshal(resultBytes, &write1OutVal); err != nil {
		t.Fatalf("unmarshal first write output: %v", err)
	}
	existingID := write1OutVal.ArticleID

	// 2. Write the decision article without links.
	fm, _ := json.Marshal(map[string]string{"type": "decision"})
	writeArgs2, _ := json.Marshal(WriteArticleInput{
		Title:       "Architecture Decision",
		Body:        "We decide to use PostgreSQL and pgvector for our semantic search implementation",
		Frontmatter: fm,
		Links:       nil,
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
		Error   string `json:"error"`
		Code    string `json:"code"`
		Details struct {
			ArticleType string `json:"article_type"`
			LinkCount   int    `json:"link_count"`
			MinLinks    int    `json:"min_links"`
			Suggestions []struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			} `json:"suggestions"`
		} `json:"details"`
		Suggestion string `json:"suggestion"`
	}
	if err := json.Unmarshal([]byte(callResp.Error), &parsedErr); err != nil {
		t.Fatalf("expected CallResponse.Error to be valid raw JSON, got: %q (unmarshal error: %v)", callResp.Error, err)
	}

	if parsedErr.Code != "REQUIRED_LINKS_VIOLATION" {
		t.Errorf("expected Code to be 'REQUIRED_LINKS_VIOLATION', got: %q", parsedErr.Code)
	}
	if parsedErr.Details.ArticleType != "decision" {
		t.Errorf("expected ArticleType to be 'decision', got: %q", parsedErr.Details.ArticleType)
	}
	if parsedErr.Details.LinkCount != 0 {
		t.Errorf("expected LinkCount to be 0, got: %d", parsedErr.Details.LinkCount)
	}
	if parsedErr.Details.MinLinks != 1 {
		t.Errorf("expected MinLinks to be 1, got: %d", parsedErr.Details.MinLinks)
	}
	if len(parsedErr.Details.Suggestions) != 1 {
		t.Fatalf("expected 1 suggestion, got: %d", len(parsedErr.Details.Suggestions))
	}
	if parsedErr.Details.Suggestions[0].ID != existingID {
		t.Errorf("expected suggested ID to be %q, got: %q", existingID, parsedErr.Details.Suggestions[0].ID)
	}
	if parsedErr.Details.Suggestions[0].Title != "Existing Helpful Article" {
		t.Errorf("expected suggested Title to be 'Existing Helpful Article', got: %q", parsedErr.Details.Suggestions[0].Title)
	}
}


