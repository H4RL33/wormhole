package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// toolsCallRPC posts a tools/call JSON-RPC request to srv, merging
// projectID into arguments per docs/mcp-protocol.md §4.1 (project_id
// lives inside arguments, not a sibling field — this helper exists so
// call sites don't hand-roll that merge). Returns the raw HTTP status and
// decoded RPCResponse for callers that need to assert on protocol-level
// failure (RPCResponse.Error) or a tool-level failure
// (result.isError) without the helper pre-judging pass/fail.
func toolsCallRPC(t *testing.T, srv *httptest.Server, token, toolName, projectID string, arguments json.RawMessage) (int, RPCResponse) {
	t.Helper()
	merged := mergeProjectID(t, arguments, projectID)
	reqBody, _ := json.Marshal(RPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "tools/call",
		Params:  mustMarshal(t, toolsCallParams{Name: toolName, Arguments: merged}),
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("tools/call POST: %v", err)
	}
	defer resp.Body.Close()
	var rpcResp RPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		t.Fatalf("decode tools/call response: %v", err)
	}
	return resp.StatusCode, rpcResp
}

// mustToolResult calls toolsCallRPC and asserts both RPC-level and
// tool-level success, then returns the tool's own JSON result bytes
// (Content[0].Text) ready for the caller to unmarshal into a specific
// Output struct — the common "happy path decode" pattern most existing
// tests use.
func mustToolResult(t *testing.T, srv *httptest.Server, token, toolName, projectID string, arguments json.RawMessage) json.RawMessage {
	t.Helper()
	status, rpcResp := toolsCallRPC(t, srv, token, toolName, projectID, arguments)
	if status != http.StatusOK {
		t.Fatalf("tools/call %s: HTTP status got %d, want 200", toolName, status)
	}
	if rpcResp.Error != nil {
		t.Fatalf("tools/call %s: unexpected RPC error: %+v", toolName, rpcResp.Error)
	}
	var result toolCallResult
	if err := json.Unmarshal(mustMarshal(t, rpcResp.Result), &result); err != nil {
		t.Fatalf("tools/call %s: decode result wrapper: %v", toolName, err)
	}
	if result.IsError {
		t.Fatalf("tools/call %s: tool returned isError: %s", toolName, result.Content[0].Text)
	}
	return json.RawMessage(result.Content[0].Text)
}

// mergeProjectID adds project_id into a raw JSON arguments object
// (docs/mcp-protocol.md §4.1). arguments must already be a JSON object
// (possibly `{}`).
func mergeProjectID(t *testing.T, arguments json.RawMessage, projectID string) json.RawMessage {
	t.Helper()
	m := map[string]json.RawMessage{}
	if len(arguments) > 0 {
		if err := json.Unmarshal(arguments, &m); err != nil {
			t.Fatalf("mergeProjectID: decode arguments: %v", err)
		}
	}
	m["project_id"] = mustMarshal(t, projectID)
	return mustMarshal(t, m)
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
