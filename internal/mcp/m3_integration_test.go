package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
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
	_, token := mustRegisterAgent(t, projectID)

	registry := NewRegistry()
	registry.Register(WriteArticleTool(store))
	registry.Register(SearchArticlesTool(store))
	handler := NewCallHandler(registry, identityStore)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	callTool := func(tool string, args any) (*http.Response, CallResponse) {
		t.Helper()
		argBytes, _ := json.Marshal(args)
		body, _ := json.Marshal(CallRequest{Tool: tool, ProjectID: projectID, Arguments: argBytes})
		req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s POST: %v", tool, err)
		}
		defer resp.Body.Close()
		var callResp CallResponse
		if decodeErr := json.NewDecoder(resp.Body).Decode(&callResp); decodeErr != nil {
			t.Fatalf("%s decode: %v", tool, decodeErr)
		}
		return resp, callResp
	}

	// 1. Write an article.
	writeResp, writeCall := callTool("wormhole.kb.write", WriteArticleInput{
		Title: "deploy runbook",
		Body:  "run deploy.sh then verify the health endpoint returns 200",
	})
	if writeResp.StatusCode != http.StatusOK {
		t.Fatalf("write status: got %d, want 200, body %+v", writeResp.StatusCode, writeCall)
	}
	writeRaw, _ := json.Marshal(writeCall.Result)
	var writeOut WriteArticleOutput
	json.Unmarshal(writeRaw, &writeOut)
	if writeOut.ArticleID == "" {
		t.Fatalf("write output missing article_id: %+v", writeOut)
	}

	// 2. Search retrieves it.
	searchResp, searchCall := callTool("wormhole.kb.search", SearchArticlesInput{Query: "deploy runbook"})
	if searchResp.StatusCode != http.StatusOK {
		t.Fatalf("search status: got %d, want 200, body %+v", searchResp.StatusCode, searchCall)
	}
	searchRaw, _ := json.Marshal(searchCall.Result)
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
	// -> similarity 1.0 >= threshold).
	dedupResp, dedupCall := callTool("wormhole.kb.write", WriteArticleInput{
		Title: "deploy runbook (copy)",
		Body:  "run deploy.sh then verify the health endpoint returns 200",
	})
	if dedupResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("dedup write status: got %d, want 400, body %+v", dedupResp.StatusCode, dedupCall)
	}
	var dedupErr struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(dedupCall.Error), &dedupErr); err != nil {
		t.Fatalf("dedup error not valid JSON: %q (%v)", dedupCall.Error, err)
	}
	if dedupErr.Code != "DEDUP_VIOLATION" {
		t.Fatalf("dedup error code: got %q, want DEDUP_VIOLATION", dedupErr.Code)
	}

	// 4. Conciseness check fires on an over-length body (store configured
	// with maxBodyLength=120 above; this body is 130 runes).
	longBody := strings.Repeat("x", 130)
	concisenessResp, concisenessCall := callTool("wormhole.kb.write", WriteArticleInput{
		Title: "too long",
		Body:  longBody,
	})
	if concisenessResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("conciseness write status: got %d, want 400, body %+v", concisenessResp.StatusCode, concisenessCall)
	}
	var concisenessErr struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(concisenessCall.Error), &concisenessErr); err != nil {
		t.Fatalf("conciseness error not valid JSON: %q (%v)", concisenessCall.Error, err)
	}
	if concisenessErr.Code != "CONCISENESS_VIOLATION" {
		t.Fatalf("conciseness error code: got %q, want CONCISENESS_VIOLATION", concisenessErr.Code)
	}
}
