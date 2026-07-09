package mcp

import (
	"context"
	"encoding/json"
	"errors"
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
	srv := httptest.NewServer(NewMCPHandler(registry, identityStore))
	defer srv.Close()

	// 1. Write the first article.
	writeArgs1, _ := json.Marshal(WriteArticleInput{
		Title: "first article",
		Body:  "unique article body content for dedup test",
	})
	mustToolResult(t, srv, token, "wormhole.kb.write", projectID, writeArgs1)

	// 2. Write the duplicate article.
	writeArgs2, _ := json.Marshal(WriteArticleInput{
		Title: "second article",
		Body:  "unique article body content for dedup test",
	})
	_, rpcResp := toolsCallRPC(t, srv, token, "wormhole.kb.write", projectID, writeArgs2)
	if rpcResp.Error != nil {
		t.Fatalf("unexpected RPC error: %+v", rpcResp.Error)
	}
	var result toolCallResult
	if err := json.Unmarshal(mustMarshal(t, rpcResp.Result), &result); err != nil {
		t.Fatalf("decode result wrapper: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected tool result IsError true, got false")
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
	if err := json.Unmarshal([]byte(result.Content[0].Text), &parsedErr); err != nil {
		t.Fatalf("expected result.Content[0].Text to be valid raw JSON, got: %q (unmarshal error: %v)", result.Content[0].Text, err)
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
	srv := httptest.NewServer(NewMCPHandler(registry, identityStore))
	defer srv.Close()

	writeArgs, _ := json.Marshal(WriteArticleInput{
		Title: "too long article",
		Body:  "123456789012345",
	})
	_, rpcResp := toolsCallRPC(t, srv, token, "wormhole.kb.write", projectID, writeArgs)
	if rpcResp.Error != nil {
		t.Fatalf("unexpected RPC error: %+v", rpcResp.Error)
	}
	var result toolCallResult
	if err := json.Unmarshal(mustMarshal(t, rpcResp.Result), &result); err != nil {
		t.Fatalf("decode result wrapper: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected tool result IsError true, got false")
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
	if err := json.Unmarshal([]byte(result.Content[0].Text), &parsedErr); err != nil {
		t.Fatalf("expected result.Content[0].Text to be valid raw JSON, got: %q (unmarshal error: %v)", result.Content[0].Text, err)
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
	srv := httptest.NewServer(NewMCPHandler(registry, identityStore))
	defer srv.Close()

	writeArgs, _ := json.Marshal(WriteArticleInput{
		Title: "too long article but forced",
		Body:  "123456789012345",
		Force: true,
	})
	mustToolResult(t, srv, token, "wormhole.kb.write", projectID, writeArgs)
}

func TestMcp_WriteArticle_ConcisenessUTF8(t *testing.T) {
	db := testDB(t)
	store := kb.NewStore(db, kb.StubEmbedder{}, 0.85, 5, 1, 1, 1)
	identityStore := testIdentityStore(t)
	projectID := mustCreateProject(t, "mcp-kb-conciseness-utf8")
	_, token := mustRegisterAgent(t, projectID)

	registry := NewRegistry()
	registry.Register(WriteArticleTool(store))
	srv := httptest.NewServer(NewMCPHandler(registry, identityStore))
	defer srv.Close()

	// "🚀🤖🌟🔥💫" has 5 characters (emojis) but 20 bytes.
	// Since maxBodyLength is 5, it should succeed.
	writeArgsValid, _ := json.Marshal(WriteArticleInput{
		Title: "valid utf8",
		Body:  "🚀🤖🌟🔥💫",
	})
	mustToolResult(t, srv, token, "wormhole.kb.write", projectID, writeArgsValid)

	// "🚀🤖🌟🔥💫✨" has 6 characters (emojis) but 24 bytes.
	// Since maxBodyLength is 5, it should violate the ceiling.
	writeArgsInvalid, _ := json.Marshal(WriteArticleInput{
		Title: "invalid utf8",
		Body:  "🚀🤖🌟🔥💫✨",
	})
	_, rpcResp := toolsCallRPC(t, srv, token, "wormhole.kb.write", projectID, writeArgsInvalid)
	if rpcResp.Error != nil {
		t.Fatalf("unexpected RPC error: %+v", rpcResp.Error)
	}
	var result toolCallResult
	if err := json.Unmarshal(mustMarshal(t, rpcResp.Result), &result); err != nil {
		t.Fatalf("decode result wrapper: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected tool result IsError true, got false")
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
	if err := json.Unmarshal([]byte(result.Content[0].Text), &parsedErr); err != nil {
		t.Fatalf("expected result.Content[0].Text to be valid raw JSON, got: %q", result.Content[0].Text)
	}

	if parsedErr.Code != "CONCISENESS_VIOLATION" {
		t.Errorf("expected Code to be 'CONCISENESS_VIOLATION', got: %q", parsedErr.Code)
	}
	if parsedErr.Details.Length != 6 {
		t.Errorf("expected Details.Length to be 6, got: %d", parsedErr.Details.Length)
	}
	if parsedErr.Details.MaxLength != 5 {
		t.Errorf("expected Details.MaxLength to be 5, got: %d", parsedErr.Details.MaxLength)
	}
}

func TestMcp_WriteArticle_RequiredLinksViolation(t *testing.T) {
	store := testKBStore(t)
	identityStore := testIdentityStore(t)
	projectID := mustCreateProject(t, "mcp-kb-req-links-violation")
	_, token := mustRegisterAgent(t, projectID)

	registry := NewRegistry()
	registry.Register(WriteArticleTool(store))
	srv := httptest.NewServer(NewMCPHandler(registry, identityStore))
	defer srv.Close()

	// 1. Create a background article so we have a suggestion candidate.
	writeArgs1, _ := json.Marshal(WriteArticleInput{
		Title: "Existing Helpful Article",
		Body:  "Relevant content about something related to our database architecture",
	})
	write1Result := mustToolResult(t, srv, token, "wormhole.kb.write", projectID, writeArgs1)
	var write1OutVal WriteArticleOutput
	if err := json.Unmarshal(write1Result, &write1OutVal); err != nil {
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
	_, rpcResp := toolsCallRPC(t, srv, token, "wormhole.kb.write", projectID, writeArgs2)
	if rpcResp.Error != nil {
		t.Fatalf("unexpected RPC error: %+v", rpcResp.Error)
	}
	var result toolCallResult
	if err := json.Unmarshal(mustMarshal(t, rpcResp.Result), &result); err != nil {
		t.Fatalf("decode result wrapper: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected tool result IsError true, got false")
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
	if err := json.Unmarshal([]byte(result.Content[0].Text), &parsedErr); err != nil {
		t.Fatalf("expected result.Content[0].Text to be valid raw JSON, got: %q (unmarshal error: %v)", result.Content[0].Text, err)
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

func TestMcp_GetArticle_HappyPath(t *testing.T) {
	store := testKBStore(t)
	identityStore := testIdentityStore(t)
	projectID := mustCreateProject(t, "mcp-kb-get-article")
	_, token := mustRegisterAgent(t, projectID)

	registry := NewRegistry()
	registry.Register(WriteArticleTool(store))
	registry.Register(GetArticleTool(store))
	srv := httptest.NewServer(NewMCPHandler(registry, identityStore))
	defer srv.Close()

	// 1. Write an article via the write tool.
	writeArgs, _ := json.Marshal(WriteArticleInput{
		Title: "retrievable article",
		Body:  "body content of the article",
	})
	writeResult := mustToolResult(t, srv, token, "wormhole.kb.write", projectID, writeArgs)
	var writeOut WriteArticleOutput
	if err := json.Unmarshal(writeResult, &writeOut); err != nil {
		t.Fatalf("unmarshal write output: %v", err)
	}

	// 2. Retrieve the article via wormhole.kb.get.
	getArgs, _ := json.Marshal(GetArticleInput{ArticleID: writeOut.ArticleID})
	getResult := mustToolResult(t, srv, token, "wormhole.kb.get", projectID, getArgs)
	var getOut GetArticleOutput
	if err := json.Unmarshal(getResult, &getOut); err != nil {
		t.Fatalf("unmarshal get output: %v", err)
	}
	if getOut.ArticleID != writeOut.ArticleID {
		t.Errorf("ArticleID: got %q, want %q", getOut.ArticleID, writeOut.ArticleID)
	}
	if getOut.ProjectID != projectID {
		t.Errorf("ProjectID: got %q, want %q", getOut.ProjectID, projectID)
	}
	if getOut.Title != "retrievable article" {
		t.Errorf("Title: got %q, want %q", getOut.Title, "retrievable article")
	}
	if getOut.Body != "body content of the article" {
		t.Errorf("Body: got %q, want %q", getOut.Body, "body content of the article")
	}
	if getOut.AuthorAgentID == "" {
		t.Error("AuthorAgentID is empty")
	}
	if getOut.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}
	if getOut.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero")
	}
}

func TestMcp_GetArticleLinks_HappyPath(t *testing.T) {
	store := testKBStore(t)
	identityStore := testIdentityStore(t)
	projectID := mustCreateProject(t, "mcp-kb-get-links")
	_, token := mustRegisterAgent(t, projectID)

	registry := NewRegistry()
	registry.Register(WriteArticleTool(store))
	registry.Register(GetArticleLinksTool(store))
	srv := httptest.NewServer(NewMCPHandler(registry, identityStore))
	defer srv.Close()

	// 1. Write the target article (B).
	writeArgsB, _ := json.Marshal(WriteArticleInput{
		Title: "target article B",
		Body:  "body of target article B",
	})
	writeBResult := mustToolResult(t, srv, token, "wormhole.kb.write", projectID, writeArgsB)
	var writeBOut WriteArticleOutput
	if err := json.Unmarshal(writeBResult, &writeBOut); err != nil {
		t.Fatalf("unmarshal write B output: %v", err)
	}

	// 2. Write the source article (A) linking to B.
	writeArgsA, _ := json.Marshal(WriteArticleInput{
		Title: "source article A",
		Body:  "body of source article A",
		Links: []string{writeBOut.ArticleID},
	})
	writeAResult := mustToolResult(t, srv, token, "wormhole.kb.write", projectID, writeArgsA)
	var writeAOut WriteArticleOutput
	if err := json.Unmarshal(writeAResult, &writeAOut); err != nil {
		t.Fatalf("unmarshal write A output: %v", err)
	}

	// 3. Call wormhole.kb.get_links on article A.
	getLinksArgs, _ := json.Marshal(GetArticleLinksInput{ArticleID: writeAOut.ArticleID})
	linksResult := mustToolResult(t, srv, token, "wormhole.kb.get_links", projectID, getLinksArgs)
	var linksOut GetArticleLinksOutput
	if err := json.Unmarshal(linksResult, &linksOut); err != nil {
		t.Fatalf("unmarshal get_links output: %v", err)
	}
	if linksOut.ArticleID != writeAOut.ArticleID {
		t.Errorf("ArticleID: got %q, want %q", linksOut.ArticleID, writeAOut.ArticleID)
	}
	if len(linksOut.Links) != 1 {
		t.Fatalf("expected 1 link, got %d", len(linksOut.Links))
	}
	if linksOut.Links[0].ArticleID != writeBOut.ArticleID {
		t.Errorf("Links[0].ArticleID: got %q, want %q", linksOut.Links[0].ArticleID, writeBOut.ArticleID)
	}
	if linksOut.Links[0].Title != "target article B" {
		t.Errorf("Links[0].Title: got %q, want %q", linksOut.Links[0].Title, "target article B")
	}
}
