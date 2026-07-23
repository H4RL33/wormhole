// internal/runtime/localapi/localapi_join_test.go
package localapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/eventbus"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/runtime/scheduler"
)

// TestServer_AgentRegister_NoAgentID_ProxiesJoinToCoordinationServer proves
// RFC-0003 §8.1's "wormhole join... now targets Gateway" requirement:
// a wormhole.agent.register call with no agent_id (the join/passport-creation
// shape cmd/wormhole sends — owner/model/capabilities/roles/permissions,
// RFC-0001 §9) is proxied to the Coordination Server's real
// wormhole.agent.register and returns the issued Passport, unauthenticated
// (matching cmd/wormhole's doRegister, which sends no bearer token for
// this call).
func TestServer_AgentRegister_NoAgentID_ProxiesJoinToCoordinationServer(t *testing.T) {
	coord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("register call should be unauthenticated, got Authorization: %q", got)
		}
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		var params toolsCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("decode params: %v", err)
		}
		if params.Name != "wormhole.agent.register" {
			t.Fatalf("got tool %q, want wormhole.agent.register", params.Name)
		}
		var in map[string]interface{}
		if err := json.Unmarshal(params.Arguments, &in); err != nil {
			t.Fatalf("decode arguments: %v", err)
		}
		if in["project_id"] != "proj-1" {
			t.Fatalf("project_id: got %v, want proj-1", in["project_id"])
		}
		if in["owner"] != "harley" {
			t.Fatalf("owner: got %v, want harley", in["owner"])
		}
		out := map[string]interface{}{
			"agent_id":    "agent-1",
			"passport_id": "passport-1",
			"token":       "sekrit-token",
			"issued_at":   time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
		}
		outRaw, _ := json.Marshal(out)
		resultRaw, _ := json.Marshal(toolCallResult{
			Content: []toolCallResultContent{{Type: "text", Text: string(outRaw)}},
		})
		json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: resultRaw})
	}))
	defer coord.Close()

	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	er := localstore.NewEventRepo(store.DB())
	tr := localstore.NewTaskRepo(store.DB(), er)
	srv, err := New(socketPath, coord.URL, "", "proj-1", store, tr, er, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	resp := sendRequest(t, socketPath, "wormhole.agent.register", map[string]interface{}{
		"project_id":   "proj-1",
		"owner":        "harley",
		"model":        "claude",
		"capabilities": []string{"code"},
		"permissions":  []string{"task.create"},
	})
	if resp.Error != "" {
		t.Fatalf("got error response: %s", resp.Error)
	}

	var out map[string]interface{}
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if out["agent_id"] != "agent-1" || out["passport_id"] != "passport-1" || out["token"] != "sekrit-token" {
		t.Fatalf("got %+v", out)
	}
}

// TestServer_AgentRegister_WithAgentID_StaysLocalSchedulerRegistration
// proves the dispatch-by-shape change doesn't disturb P3's existing local
// scheduler registration (agent_id + capabilities, no Coordination Server
// call): still handled by handleAgentRegister, no HTTP call made (the
// Coordination Server URL below is unreachable — a failing test would prove
// the call wrongly went over HTTP).
func TestServer_AgentRegister_WithAgentID_StaysLocalSchedulerRegistration(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	er := localstore.NewEventRepo(store.DB())
	tr := localstore.NewTaskRepo(store.DB(), er)
	eb := eventbus.NewEventBus()
	sched := scheduler.NewScheduler()
	srv, err := NewWithRuntime(socketPath, "http://unreachable.invalid", "", "proj-1", store, tr, er, localstore.NewKBRepo(store.DB()), eb, sched, nil)
	if err != nil {
		t.Fatalf("NewWithRuntime: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	resp := sendRequest(t, socketPath, "wormhole.agent.register", map[string]interface{}{
		"agent_id":     "agent-local",
		"capabilities": []string{"code"},
	})
	if resp.Error != "" {
		t.Fatalf("got error response (should have stayed local, no HTTP call): %s", resp.Error)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if out["agent_id"] != "agent-local" {
		t.Fatalf("got %+v", out)
	}
}
