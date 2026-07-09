package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoggingMiddleware_LogsMethodPathStatus(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(loggingMiddleware(next))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	resp.Body.Close()

	got := buf.String()
	for _, want := range []string{"GET", "/healthz", "204"} {
		if !strings.Contains(got, want) {
			t.Fatalf("log output missing %q: got %q", want, got)
		}
	}
}

func TestLoggingMiddleware_LogsJSONRPCMethodForMCP(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"method":"initialize"`) {
			t.Fatalf("downstream handler did not see original body: %q", body)
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(loggingMiddleware(next))
	defer srv.Close()

	reqBody, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	resp, err := http.Post(srv.URL+"/mcp", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	resp.Body.Close()

	got := buf.String()
	for _, want := range []string{"POST", "/mcp", "200", "initialize"} {
		if !strings.Contains(got, want) {
			t.Fatalf("log output missing %q: got %q", want, got)
		}
	}
}

func TestLoggingMiddleware_LogsToolNameForToolsCall(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(loggingMiddleware(next))
	defer srv.Close()

	params, _ := json.Marshal(map[string]any{"name": "wormhole.task.list", "arguments": map[string]any{}})
	reqBody, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": json.RawMessage(params)})
	resp, err := http.Post(srv.URL+"/mcp", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	resp.Body.Close()

	got := buf.String()
	for _, want := range []string{"tools/call", "wormhole.task.list"} {
		if !strings.Contains(got, want) {
			t.Fatalf("log output missing %q: got %q", want, got)
		}
	}
}
