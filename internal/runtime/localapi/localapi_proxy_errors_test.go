package localapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
)

func newProxyTestServer(t *testing.T, handler http.HandlerFunc) *Server {
	t.Helper()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	coord := httptest.NewServer(handler)
	t.Cleanup(coord.Close)
	events := localstore.NewEventRepo(store.DB())
	srv, err := New(filepath.Join(t.TempDir(), "wormholed.sock"), coord.URL, "test-token", "project-1", store, localstore.NewTaskRepo(store.DB(), events), events, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv
}

func TestLocalAPIRemoteProxyFailuresRemainActionable(t *testing.T) {
	for _, tt := range []struct {
		name  string
		write func(http.ResponseWriter)
		want  string
	}{
		{"malformed response", func(w http.ResponseWriter) { _, _ = w.Write([]byte("{")) }, "decode coordination server response"},
		{"rpc error", func(w http.ResponseWriter) {
			_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", Error: &rpcError{Message: "remote denied"}})
		}, "remote denied"},
		{"invalid result", func(w http.ResponseWriter) {
			_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", Result: json.RawMessage(`[]`)})
		}, "decode tools/call result"},
		{"empty content", func(w http.ResponseWriter) {
			raw, _ := json.Marshal(toolCallResult{})
			_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", Result: raw})
		}, "empty"},
		{"tool error", func(w http.ResponseWriter) {
			raw, _ := json.Marshal(toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: "tool rejected"}}, IsError: true})
			_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", Result: raw})
		}, "tool rejected"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := newProxyTestServer(t, func(w http.ResponseWriter, r *http.Request) { tt.write(w) })
			if _, err := srv.proxyWhoAmI(context.Background()); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("proxyWhoAmI error = %v, want %q", err, tt.want)
			}
			if _, err := srv.proxyRegister(context.Background(), json.RawMessage(`{"owner":"owner","project_id":"project-1"}`)); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("proxyRegister error = %v, want %q", err, tt.want)
			}
		})
	}
}
