package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"
)

// statusRecorder wraps http.ResponseWriter to capture the status code
// written by the downstream handler, since http.ResponseWriter has no
// getter for it and loggingMiddleware needs it after the handler returns.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// rpcLogProbe is a partial decode of a JSON-RPC request body, used only to
// extract the method (and, for tools/call, the tool name) for the activity
// log line. Decode failures are silently ignored — the request still
// reaches the real handler, which does its own full validation and error
// reporting; this is best-effort observability, not a second validator.
type rpcLogProbe struct {
	Method string `json:"method"`
	Params json.RawMessage `json:"params"`
}

type toolsCallLogProbe struct {
	Name string `json:"name"`
}

// describeMCPRequest reads r's body (if r is a POST to /mcp), restores it
// via io.NopCloser so the downstream handler sees the same bytes it would
// without logging, and returns a short description for the log line
// ("initialize", "tools/list", or "tools/call wormhole.task.list"). Returns
// "" for any non-/mcp request or any body it can't parse.
func describeMCPRequest(r *http.Request) string {
	if r.URL.Path != "/mcp" || r.Method != http.MethodPost {
		return ""
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return ""
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	var probe rpcLogProbe
	if err := json.Unmarshal(body, &probe); err != nil || probe.Method == "" {
		return ""
	}
	if probe.Method != "tools/call" {
		return probe.Method
	}
	var toolProbe toolsCallLogProbe
	if err := json.Unmarshal(probe.Params, &toolProbe); err != nil || toolProbe.Name == "" {
		return probe.Method
	}
	return probe.Method + " " + toolProbe.Name
}

// loggingMiddleware logs one line per request to stdout via the standard
// log package: method, path, status, latency, and (for /mcp requests) the
// JSON-RPC method and tool name, so `wormhole-server`'s stdout shows real
// activity during a demo or test run instead of only its startup line.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		mcpDesc := describeMCPRequest(r)

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		elapsed := time.Since(start)
		if mcpDesc != "" {
			log.Printf("%s %s %d %s %s", r.Method, r.URL.Path, rec.status, elapsed, mcpDesc)
		} else {
			log.Printf("%s %s %d %s", r.Method, r.URL.Path, rec.status, elapsed)
		}
	})
}
