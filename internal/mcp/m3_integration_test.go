package mcp

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/H4RL33/wormhole/internal/core/kb"
)

// TestM3_KBWriteSearchComplianceLoop is the M3 exit-bar test (RFC-0001
// §8.3): it drives the KB's MCP boundary end-to-end through a real HTTP
// server, proving the three pieces built across Days 13-17 work together
// rather than only in isolation: a written article is retrievable via
// semantic search, a semantic duplicate is rejected on write, and an
// over-length body is rejected on write. Mirrors the shape of
// TestM2_TaskLifecycleEventsOnChannel.
func TestM3_KBWriteSearchComplianceLoop(t *testing.T) {
	db := testDB(t)
	// maxBodyLength=120 keeps the conciseness case below deliberately small
	// so the test doesn't need a multi-KB body; dedupThreshold=0.85 and
	// minLinks=1/1/1 match the values wired in cmd/wormhole-server/main.go's
	// default config.
	store := kb.NewStore(db, kb.StubEmbedder{}, 0.85, 120, 1, 1, 1)
	identityStore := testIdentityStore(t)
	projectID := mustCreateProject(t, "m3-kb-write-search-compliance")
	_, token := mustRegisterAgentWithPerms(t, projectID, []string{"kb.write", "kb.search"})

	registry := NewRegistry()
	registry.Register(WriteArticleTool(store))
	registry.Register(SearchArticlesTool(store))
	srv := httptest.NewServer(NewMCPHandler(registry, identityStore))
	defer srv.Close()

	// 1. Write an article.
	writeRaw := mustToolResult(t, srv, token, "wormhole.kb.write", projectID, mustMarshal(t, WriteArticleInput{
		Title: "deploy runbook",
		Body:  "run deploy.sh then verify the health endpoint returns 200",
	}))
	var writeOut WriteArticleOutput
	json.Unmarshal(writeRaw, &writeOut)
	if writeOut.ArticleID == "" {
		t.Fatalf("write output missing article_id: %+v", writeOut)
	}

	// 2. Search retrieves it.
	searchRaw := mustToolResult(t, srv, token, "wormhole.kb.search", projectID, mustMarshal(t, SearchArticlesInput{Query: "deploy runbook"}))
	var searchOut SearchArticlesOutput
	json.Unmarshal(searchRaw, &searchOut)
	found := false
	for _, a := range searchOut.Articles {
		if a.ArticleID == writeOut.ArticleID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("search results missing written article %s: %+v", writeOut.ArticleID, searchOut.Articles)
	}

	// 3. Dedup check fires on a semantic duplicate (same body, stub
	// embedder is deterministic so identical body -> identical embedding
	// -> similarity 1.0 >= threshold). Under the JSON-RPC protocol this is
	// a tool-level failure: rpcResp.Error == nil, but the decoded
	// toolCallResult.IsError == true, and Content[0].Text carries the same
	// JSON error string kb.ErrDedupViolation.Error() always produced.
	_, dedupRPCResp := toolsCallRPC(t, srv, token, "wormhole.kb.write", projectID, mustMarshal(t, WriteArticleInput{
		Title: "deploy runbook (copy)",
		Body:  "run deploy.sh then verify the health endpoint returns 200",
	}))
	if dedupRPCResp.Error != nil {
		t.Fatalf("dedup write: unexpected RPC error: %+v", dedupRPCResp.Error)
	}
	var dedupResult toolCallResult
	if err := json.Unmarshal(mustMarshal(t, dedupRPCResp.Result), &dedupResult); err != nil {
		t.Fatalf("dedup write: decode result wrapper: %v", err)
	}
	if !dedupResult.IsError {
		t.Fatalf("dedup write: expected IsError true, got false")
	}
	var dedupErr struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(dedupResult.Content[0].Text), &dedupErr); err != nil {
		t.Fatalf("dedup error not valid JSON: %q (%v)", dedupResult.Content[0].Text, err)
	}
	if dedupErr.Code != "DEDUP_VIOLATION" {
		t.Fatalf("dedup error code: got %q, want DEDUP_VIOLATION", dedupErr.Code)
	}

	// 4. Conciseness check fires on an over-length body (store configured
	// with maxBodyLength=120 above; this body is 130 runes).
	longBody := strings.Repeat("x", 130)
	_, concisenessRPCResp := toolsCallRPC(t, srv, token, "wormhole.kb.write", projectID, mustMarshal(t, WriteArticleInput{
		Title: "too long",
		Body:  longBody,
	}))
	if concisenessRPCResp.Error != nil {
		t.Fatalf("conciseness write: unexpected RPC error: %+v", concisenessRPCResp.Error)
	}
	var concisenessResult toolCallResult
	if err := json.Unmarshal(mustMarshal(t, concisenessRPCResp.Result), &concisenessResult); err != nil {
		t.Fatalf("conciseness write: decode result wrapper: %v", err)
	}
	if !concisenessResult.IsError {
		t.Fatalf("conciseness write: expected IsError true, got false")
	}
	var concisenessErr struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(concisenessResult.Content[0].Text), &concisenessErr); err != nil {
		t.Fatalf("conciseness error not valid JSON: %q (%v)", concisenessResult.Content[0].Text, err)
	}
	if concisenessErr.Code != "CONCISENESS_VIOLATION" {
		t.Fatalf("conciseness error code: got %q, want CONCISENESS_VIOLATION", concisenessErr.Code)
	}
}
