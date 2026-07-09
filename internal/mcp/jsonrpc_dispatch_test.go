package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/H4RL33/wormhole/internal/core/identity"
)

// TestMCPHandler_Initialize verifies POST /mcp routes "initialize" to
// HandleInitialize (docs/mcp-protocol.md §2, §4).
func TestMCPHandler_Initialize(t *testing.T) {
	handler := NewMCPHandler(NewRegistry(), nil)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	rec := doPost(handler, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp RPCResponse
	decodeBody(t, rec, &resp)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	resultJSON, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var result initializeResult
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.ProtocolVersion != "2025-11-25" {
		t.Errorf("protocolVersion = %q, want 2025-11-25", result.ProtocolVersion)
	}
	if result.ServerInfo["name"] != "wormhole" {
		t.Errorf("serverInfo.name = %q, want wormhole", result.ServerInfo["name"])
	}
}

// TestMCPHandler_ToolsList verifies POST /mcp routes "tools/list" to
// HandleToolsList and returns the registered tool.
func TestMCPHandler_ToolsList(t *testing.T) {
	registry := NewRegistry()
	registry.Register(Tool{
		Name:        "wormhole.test.echo",
		Description: "echoes back",
	})
	handler := NewMCPHandler(registry, nil)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	rec := doPost(handler, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp RPCResponse
	decodeBody(t, rec, &resp)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if !strings.Contains(rec.Body.String(), "wormhole.test.echo") {
		t.Fatalf("response %s does not contain registered tool", rec.Body.String())
	}
}

// TestMCPHandler_UnknownMethod verifies an unrecognized method produces a
// -32601 Method not found JSON-RPC error over HTTP 200.
func TestMCPHandler_UnknownMethod(t *testing.T) {
	handler := NewMCPHandler(NewRegistry(), nil)

	body := `{"jsonrpc":"2.0","id":1,"method":"bogus/method"}`
	rec := doPost(handler, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp RPCResponse
	decodeBody(t, rec, &resp)
	if resp.Error == nil || resp.Error.Code != RPCMethodNotFound {
		t.Fatalf("error = %+v, want code %d", resp.Error, RPCMethodNotFound)
	}
}

// TestMCPHandler_ParseError verifies invalid JSON produces a -32700 Parse
// error JSON-RPC error over HTTP 200 (docs/mcp-protocol.md's HTTP-status
// paragraph in §2: malformed bodies never become a 4xx).
func TestMCPHandler_ParseError(t *testing.T) {
	handler := NewMCPHandler(NewRegistry(), nil)

	rec := doPost(handler, "not valid json")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp RPCResponse
	decodeBody(t, rec, &resp)
	if resp.Error == nil || resp.Error.Code != RPCParseError {
		t.Fatalf("error = %+v, want code %d", resp.Error, RPCParseError)
	}
}

// TestMCPHandler_InvalidRequest verifies a request missing "jsonrpc"
// produces a -32600 Invalid Request error.
func TestMCPHandler_InvalidRequest(t *testing.T) {
	handler := NewMCPHandler(NewRegistry(), nil)

	body := `{"method":"initialize"}`
	rec := doPost(handler, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp RPCResponse
	decodeBody(t, rec, &resp)
	if resp.Error == nil || resp.Error.Code != RPCInvalidRequest {
		t.Fatalf("error = %+v, want code %d", resp.Error, RPCInvalidRequest)
	}
}

// TestMCPHandler_Notification verifies a request with no "id" field (a
// notification) gets an empty 202, never a JSON-RPC response body.
func TestMCPHandler_Notification(t *testing.T) {
	handler := NewMCPHandler(NewRegistry(), nil)

	body := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	rec := doPost(handler, body)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty", rec.Body.String())
	}
}

// TestMCPHandler_GetMethodNotAllowed verifies GET /mcp is rejected with 405
// (docs/mcp-protocol.md §2 — no SSE stream implemented).
func TestMCPHandler_GetMethodNotAllowed(t *testing.T) {
	handler := NewMCPHandler(NewRegistry(), nil)

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// TestMCPHandler_ToolsCallRoutesThrough is a cheap integration check that
// the dispatcher's "tools/call" case actually invokes HandleToolsCall; deep
// coverage of HandleToolsCall itself is Task 1's job.
func TestMCPHandler_ToolsCallRoutesThrough(t *testing.T) {
	registry := NewRegistry()
	registry.Register(Tool{
		Name:         "wormhole.test.noop",
		Description:  "no-op test tool",
		RequiresAuth: false,
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			return map[string]string{"ok": "true"}, nil
		},
	})
	handler := NewMCPHandler(registry, nil)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wormhole.test.noop","arguments":{"project_id":"p1"}}}`
	rec := doPost(handler, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp RPCResponse
	decodeBody(t, rec, &resp)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	resultJSON, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var result toolCallResult
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0].Type != "text" {
		t.Fatalf("result = %+v, want single text content item", result)
	}
	if !strings.Contains(result.Content[0].Text, `"ok":"true"`) {
		t.Fatalf("content text = %q, want tool's own result encoded", result.Content[0].Text)
	}
}

func doPost(handler http.HandlerFunc, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode response body %q: %v", rec.Body.String(), err)
	}
}
