package localapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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
		})
	}
}

func TestProxyWhoAmIFallsBackToExactCachedCredentialIdentityOffline(t *testing.T) {
	srv := newProxyTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Fabric unavailable", http.StatusServiceUnavailable)
	})
	want := localstore.WhoAmICache{
		AgentID: "credential-agent", Owner: "cached owner", Model: "cached model",
		Capabilities: []string{"code"}, ProjectID: "project-1", Permissions: []string{"task.create"}, CachedAt: time.Now().UTC(),
	}
	if err := srv.store.CacheWhoAmI(context.Background(), want); err != nil {
		t.Fatalf("cache credential identity: %v", err)
	}
	if err := srv.store.CacheWhoAmI(context.Background(), localstore.WhoAmICache{
		AgentID: "stale-other-agent", Owner: "wrong", Model: "wrong", ProjectID: "project-1",
		Permissions: []string{"*"}, CachedAt: want.CachedAt.Add(time.Hour),
	}); err != nil {
		t.Fatalf("cache stale identity: %v", err)
	}
	srv.SetAuthorizationAgent("project-1", "credential-agent")
	out, err := srv.proxyWhoAmI(context.Background())
	if err != nil {
		t.Fatalf("proxyWhoAmI offline: %v", err)
	}
	if out.AgentID != want.AgentID || out.Owner != want.Owner || out.Model != want.Model ||
		out.ProjectID != want.ProjectID || !reflect.DeepEqual(out.Capabilities, want.Capabilities) || !reflect.DeepEqual(out.Permissions, want.Permissions) {
		t.Fatalf("offline whoami = %+v, want exact credential cache %+v", out, want)
	}
}
