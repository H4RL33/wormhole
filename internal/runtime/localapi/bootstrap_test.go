package localapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	syncpkg "github.com/H4RL33/wormhole/internal/runtime/sync"
	"github.com/H4RL33/wormhole/internal/types"
)

func enrolmentBootstrapOutput() map[string]any {
	now := time.Date(2026, 7, 25, 12, 0, 0, 123, time.UTC)
	task := types.BootstrapTaskV1{ID: "task-1", ProjectID: "project-1", Title: "task", Description: "task description", Status: "todo", CreatedAt: now, UpdatedAt: now}
	article := types.BootstrapArticleV1{ID: "kb-1", ProjectID: "project-1", Title: "article", Body: "body", Frontmatter: json.RawMessage(`{}`), AuthorAgentID: "agent-1", CreatedAt: now, UpdatedAt: now}
	org := types.BootstrapOrgConfigV1{
		SchemaVersion: types.BootstrapSchemaVersionV1,
		Project:       types.BootstrapProjectV1{ID: "project-1", Name: "project", Owner: "owner", CreatedAt: now},
		Identity: types.BootstrapIdentityV1{
			Agent:       types.BootstrapAgentV1{ID: "agent-1", Owner: "owner", Model: "model", Capabilities: []string{}, CreatedAt: now},
			Passport:    types.BootstrapPassportV1{ID: "passport-1", AgentID: "agent-1", ProjectID: "project-1", Repositories: []string{}, Roles: []string{}, IssuedAt: now},
			Permissions: []string{"kb.write", "task.create"},
		},
		Channels:                    []types.BootstrapChannelV1{},
		Events:                      []types.BootstrapEventV1{},
		Tasks:                       []types.BootstrapTaskV1{task},
		KB:                          types.BootstrapKBV1{Articles: []types.BootstrapArticleV1{article}},
		IntegrationManifestMetadata: json.RawMessage(`null`),
	}
	return map[string]any{"org_config": org, "project_list": []string{}, "task_list": org.Tasks, "kb_list": org.KB.Articles, "timestamp": now.Format(time.RFC3339Nano), "version": 1}
}

func TestBootstrapFailureCheckpointsRecoveryAfterRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fabric := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		var params toolsCallParams
		if err := json.Unmarshal(request.Params, &params); err != nil {
			t.Errorf("decode params: %v", err)
			return
		}
		switch params.Name {
		case EnrolmentToolName:
			writeFabricToolResponse(t, w, request.ID, fabricEnrolAgentOutput{AgentID: "agent-1", PassportID: "passport-1", Token: "secret", IssuedAt: time.Now().UTC()}, false)
		case "wormhole.sync.bootstrap":
			cancel()
			writeFabricToolResponse(t, w, request.ID, "forced bootstrap failure after cancellation", true)
		default:
			t.Errorf("unexpected Fabric tool %q", params.Name)
		}
	}))
	defer fabric.Close()

	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	er := localstore.NewEventRepo(store.DB())
	server := &Server{store: store, tr: localstore.NewTaskRepo(store.DB(), er), er: er, kb: localstore.NewKBRepo(store.DB()), qr: syncpkg.NewQueueRepo(store.DB()), httpClient: fabric.Client()}
	server.SetEnrolmentRuntime(staticEnrolmentPolicySource{envelope: validEnrolmentEnvelope()}, filepath.Join(t.TempDir(), "credentials"))
	server.EnableEnrolmentBootstrap(syncpkg.Config{BatchInterval: time.Hour, BatchSize: 10, LatencyCheckInterval: time.Hour, PullInterval: time.Hour, HighPriorityThreshold: 2})
	req := validEnrolmentRequest()
	req.FabricAddress = fabric.URL

	result := server.executeEnrolment(ctx, req)
	if result.Code != EnrolmentBootstrapFailedAfterEnrolment || result.State != EnrolmentRecoveryRequired {
		t.Fatalf("result = %+v, want recoverable bootstrap failure", result)
	}
	var state string
	if err := store.DB().QueryRow(`SELECT state FROM enrolment_attempts WHERE project_id = ?`, req.ProjectID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(EnrolmentRecoveryRequired) {
		t.Fatalf("durable state = %q, want recovery_required", state)
	}
}

func TestBootstrapFailureReturnsCheckpointAttentionWhenRecoveryCommitFails(t *testing.T) {
	var store *localstore.Store
	fabric := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		var params toolsCallParams
		if err := json.Unmarshal(request.Params, &params); err != nil {
			t.Errorf("decode params: %v", err)
			return
		}
		switch params.Name {
		case EnrolmentToolName:
			writeFabricToolResponse(t, w, request.ID, fabricEnrolAgentOutput{AgentID: "agent-1", PassportID: "passport-1", Token: "secret", IssuedAt: time.Now().UTC()}, false)
		case "wormhole.sync.bootstrap":
			if err := store.Close(); err != nil {
				t.Errorf("inject store close: %v", err)
			}
			writeFabricToolResponse(t, w, request.ID, "forced bootstrap failure before checkpoint error", true)
		default:
			t.Errorf("unexpected Fabric tool %q", params.Name)
		}
	}))
	defer fabric.Close()

	var err error
	store, err = localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatal(err)
	}
	er := localstore.NewEventRepo(store.DB())
	server := &Server{store: store, tr: localstore.NewTaskRepo(store.DB(), er), er: er, kb: localstore.NewKBRepo(store.DB()), qr: syncpkg.NewQueueRepo(store.DB()), httpClient: fabric.Client()}
	server.SetEnrolmentRuntime(staticEnrolmentPolicySource{envelope: validEnrolmentEnvelope()}, filepath.Join(t.TempDir(), "credentials"))
	server.EnableEnrolmentBootstrap(syncpkg.Config{BatchInterval: time.Hour, BatchSize: 10, LatencyCheckInterval: time.Hour, PullInterval: time.Hour, HighPriorityThreshold: 2})
	req := validEnrolmentRequest()
	req.FabricAddress = fabric.URL

	result := server.executeEnrolment(context.Background(), req)
	if string(result.Code) != "checkpoint_persistence_failed" || string(result.State) != "attention_required" || result.Retryable {
		t.Fatalf("result = %+v, want nonretryable checkpoint attention", result)
	}
}

func writeFabricToolResponse(t *testing.T, w http.ResponseWriter, id json.RawMessage, value any, toolError bool) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: string(raw)}}, IsError: toolError})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: id, Result: result}); err != nil {
		t.Fatal(err)
	}
}

func TestEnrolmentBootstrapFailureRecoversWithoutReregistrationAndWaitsForImmutableRoute(t *testing.T) {
	var mu sync.Mutex
	registrationCalls := 0
	bootstrapCalls := 0
	incrementalArgs := make([]map[string]any, 0)
	fabric := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		var params toolsCallParams
		if err := json.Unmarshal(request.Params, &params); err != nil {
			t.Errorf("decode params: %v", err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch params.Name {
		case EnrolmentToolName:
			registrationCalls++
			writeFabricToolResponse(t, w, request.ID, fabricEnrolAgentOutput{AgentID: "agent-1", PassportID: "passport-1", Token: "secret", IssuedAt: time.Now().UTC()}, false)
		case "wormhole.sync.bootstrap":
			bootstrapCalls++
			if bootstrapCalls == 1 {
				writeFabricToolResponse(t, w, request.ID, "forced bootstrap failure", true)
				return
			}
			writeFabricToolResponse(t, w, request.ID, enrolmentBootstrapOutput(), false)
		case "wormhole.sync.incremental_pull":
			var args map[string]any
			if err := json.Unmarshal(params.Arguments, &args); err != nil {
				t.Errorf("decode incremental args: %v", err)
			}
			incrementalArgs = append(incrementalArgs, args)
			writeFabricToolResponse(t, w, request.ID, map[string]any{"updates": []any{}, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "version": 1}, false)
		case "wormhole.sync.incremental_push":
			writeFabricToolResponse(t, w, request.ID, map[string]any{"items_received": 0, "applied": []any{}, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "version": 1}, false)
		default:
			t.Errorf("unexpected Fabric tool %q", params.Name)
		}
	}))
	defer fabric.Close()

	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	er := localstore.NewEventRepo(store.DB())
	server := &Server{
		store: store, tr: localstore.NewTaskRepo(store.DB(), er), er: er, kb: localstore.NewKBRepo(store.DB()),
		qr: syncpkg.NewQueueRepo(store.DB()), httpClient: fabric.Client(),
	}
	server.SetEnrolmentRuntime(staticEnrolmentPolicySource{envelope: validEnrolmentEnvelope()}, filepath.Join(t.TempDir(), "credentials"))
	server.EnableEnrolmentBootstrap(syncpkg.Config{BatchInterval: time.Hour, BatchSize: 10, LatencyCheckInterval: time.Hour, PullInterval: 10 * time.Millisecond, HighPriorityThreshold: 2})
	defer server.stopEnrolmentSyncEngines()
	req := validEnrolmentRequest()
	req.FabricAddress = fabric.URL

	first := server.executeEnrolment(context.Background(), req)
	if first.Code != EnrolmentBootstrapFailedAfterEnrolment || first.State != EnrolmentRecoveryRequired {
		t.Fatalf("first result = %+v", first)
	}
	for _, table := range []string{"projects", "tasks", "kb_articles", "bootstrap_metadata"} {
		var count int
		if err := store.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("failed bootstrap left %d rows in %s", count, table)
		}
	}

	second := server.executeEnrolment(context.Background(), req)
	if second.Code != EnrolmentSuccess || second.State != EnrolmentReady || second.Retryable {
		t.Fatalf("recovery result = %+v", second)
	}
	third := server.executeEnrolment(context.Background(), req)
	if third.Code != EnrolmentSuccess {
		t.Fatalf("ready replay result = %+v", third)
	}
	mu.Lock()
	defer mu.Unlock()
	if registrationCalls != 1 || bootstrapCalls != 2 {
		t.Fatalf("Fabric registration/bootstrap calls = %d/%d, want 1/2", registrationCalls, bootstrapCalls)
	}
	if len(incrementalArgs) != 0 {
		t.Fatalf("project-only enrolment started incremental sync without immutable route: %#v", incrementalArgs)
	}
}
