package localapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	codegraphconfig "github.com/H4RL33/wormhole/internal/runtime/codegraph/config"
	codegraphindex "github.com/H4RL33/wormhole/internal/runtime/codegraph/index"
	codegraphstore "github.com/H4RL33/wormhole/internal/runtime/codegraph/store"
	runtimeconfig "github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
)

func TestCodeGraphRebuildUsesPersistedBalancedConfigurationAndReplacesActive(t *testing.T) {
	srv, runtime, checkout := newCodeGraphStatusFixture(t)
	buildCodeGraphFixture(t, runtime, "old")
	before, err := runtime.Store.ProjectConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	writeCodeGraphFile(t, checkout, "target.go", "package fixture\nfunc Target() int { return 2 }\nfunc Added() {}\n")
	runCodeGraphGit(t, checkout, "add", "target.go")
	runCodeGraphGit(t, checkout, "commit", "-m", "new graph")

	value, err := srv.handleCodeGraphRebuild(context.Background(), json.RawMessage(`{"project_id":"project-1"}`))
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	result := value.(codeGraphRebuildResult)
	if result.GraphRevision == "" || result.GraphRevision == "old" || result.IndexedCommit != runCodeGraphGit(t, checkout, "rev-parse", "HEAD") || result.State != "ready" {
		t.Fatalf("rebuild result = %+v", result)
	}
	after, _ := runtime.Store.ProjectConfig(context.Background())
	before.LastSuccessfulBuild, after.LastSuccessfulBuild = nil, nil
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("rebuild mutated persisted configuration\nbefore=%+v\nafter=%+v", before, after)
	}
	active, _ := runtime.Store.ActiveRevision(context.Background())
	if active.ID != result.GraphRevision {
		t.Fatalf("active = %+v, result=%+v", active, result)
	}
	old, _ := runtime.Store.Revision(context.Background(), "old")
	if old.State != codegraphstore.RevisionRetired {
		t.Fatalf("old state = %q", old.State)
	}
}

func TestCodeGraphRebuildFailurePreservesActiveAndReportsDegraded(t *testing.T) {
	srv, runtime, checkout := newCodeGraphStatusFixture(t)
	buildCodeGraphFixture(t, runtime, "old")
	writeCodeGraphFile(t, checkout, "target.go", "package fixture\nfunc Broken( {\n")
	if _, err := srv.handleCodeGraphRebuild(context.Background(), json.RawMessage(`{"project_id":"project-1"}`)); err == nil {
		t.Fatal("broken rebuild unexpectedly succeeded")
	}
	active, err := runtime.Store.ActiveRevision(context.Background())
	if err != nil || active.ID != "old" {
		t.Fatalf("active after failure = %+v err=%v", active, err)
	}
	status := codeGraphStatus(t, srv)
	if status.State != "degraded" || status.ActiveRevision != "old" || !hasErrorDiagnostics(status.LatestDiagnostics) {
		t.Fatalf("failed rebuild status = %+v", status)
	}
}

func TestCodeGraphRebuildRejectsConcurrentDisabledForeignAndDangerousRequestsWithoutMutation(t *testing.T) {
	srv, runtime, _ := newCodeGraphStatusFixture(t)
	buildCodeGraphFixture(t, runtime, "old")
	count := func() int {
		var value int
		if err := srv.store.DB().QueryRow(`SELECT COUNT(*) FROM codegraph_revisions WHERE project_id = 'project-1'`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	assertActive := func(wantCount int) {
		active, err := runtime.Store.ActiveRevision(context.Background())
		if err != nil || active.ID != "old" || count() != wantCount {
			t.Fatalf("mutation: active=%+v err=%v count=%d", active, err, count())
		}
	}
	wantCount := count()
	runtime.rebuildMu.Lock()
	if _, err := srv.handleCodeGraphRebuild(context.Background(), json.RawMessage(`{"project_id":"project-1"}`)); err == nil || !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("concurrent rebuild error = %v", err)
	}
	runtime.rebuildMu.Unlock()
	assertActive(wantCount)

	if _, err := srv.handleCodeGraphRebuild(context.Background(), json.RawMessage(`{"project_id":"project-2"}`)); err == nil {
		t.Fatal("foreign rebuild resolved")
	}
	assertActive(wantCount)
	for _, field := range []string{"enabled", "checkout", "canonical_remote", "source_byte_ceiling", "warpspeed", "in_place", "limits"} {
		raw := json.RawMessage(`{"project_id":"project-1","` + field + `":true}`)
		if _, err := srv.handleCodeGraphRebuild(context.Background(), raw); err == nil {
			t.Fatalf("dangerous field %s accepted", field)
		}
		assertActive(wantCount)
	}

	project, _ := runtime.Store.ProjectConfig(context.Background())
	project.Enabled = false
	if err := runtime.Store.PutProjectConfig(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.handleCodeGraphRebuild(context.Background(), json.RawMessage(`{"project_id":"project-1"}`)); !errors.Is(err, codegraphindex.ErrProjectDisabled) {
		t.Fatalf("disabled rebuild error = %v", err)
	}
	assertActive(wantCount)
}

func TestMCPRecoverySurfaceHidesAndDeniesCodeGraph(t *testing.T) {
	srv, socketPath := newMCPTestServer(t)
	srv.SetRecoveryOnlyProjects([]string{"project-1"}, true)
	conn := dialLocalSocket(t, socketPath)
	defer conn.Close()
	reader := bufio.NewReader(conn)
	mcpInitialize(t, conn, reader)

	listed := mcpToolsList(t, conn, reader, 70)
	for _, tool := range listed {
		if strings.HasPrefix(tool.Name, "wormhole.code_graph.") {
			t.Fatalf("recovery inventory exposed %s", tool.Name)
		}
	}
	resp := mcpCallTool(t, conn, reader, 71, "wormhole.code_graph.status", map[string]interface{}{"project_id": "project-1"})
	if !strings.Contains(resp.Error, "recovery required") {
		t.Fatalf("recovery direct call error = %q", resp.Error)
	}
}

func TestMCPCodeGraphExactPermissionMatrixAndMissingPinnedCache(t *testing.T) {
	srv, runtime, socketPath := func() (*Server, CodeGraphRuntime, string) {
		srv, runtime, _ := newCodeGraphStatusFixture(t)
		return srv, runtime, srv.socketPath
	}()
	buildCodeGraphFixture(t, runtime, "permission-active")
	srv.SetAuthorizationAgent("project-1", "agent-1")
	cache := func(permissions []string) {
		if err := srv.store.CacheWhoAmI(context.Background(), localstore.WhoAmICache{AgentID: "agent-1", ProjectID: "project-1", Permissions: permissions, CachedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	conn := dialLocalSocket(t, socketPath)
	defer conn.Close()
	reader := bufio.NewReader(conn)
	mcpInitialize(t, conn, reader)
	type call struct {
		name string
		args map[string]interface{}
	}
	calls := []call{
		{"wormhole.code_graph.query", map[string]interface{}{"project_id": "project-1", "entry_symbols": []string{"Target"}}},
		{"wormhole.code_graph.status", map[string]interface{}{"project_id": "project-1"}},
		{"wormhole.code_graph.rebuild", map[string]interface{}{"project_id": "project-1"}},
	}
	for row, test := range []struct {
		permissions []string
		allowed     string
	}{
		{[]string{"code_graph.query"}, "wormhole.code_graph.query"},
		{[]string{"code_graph.status"}, "wormhole.code_graph.status"},
		{[]string{"code_graph.rebuild"}, "wormhole.code_graph.rebuild"},
		{[]string{"code_graph.source.read"}, ""},
		{[]string{}, ""},
	} {
		cache(test.permissions)
		for column, call := range calls {
			resp := mcpCallTool(t, conn, reader, 100+row*10+column, call.name, call.args)
			if call.name == test.allowed && resp.Error != "" {
				t.Errorf("permissions %v: %s denied: %s", test.permissions, call.name, resp.Error)
			}
			if call.name != test.allowed && resp.Error == "" {
				t.Errorf("permissions %v: %s unexpectedly allowed", test.permissions, call.name)
			}
		}
	}
	srv.SetAuthorizationAgent("project-1", "uncached-agent")
	if resp := mcpCallTool(t, conn, reader, 150, calls[0].name, calls[0].args); !strings.Contains(resp.Error, "no authenticated scope") {
		t.Fatalf("missing pinned cache error = %q", resp.Error)
	}
}

func TestMCPProjectACredentialCannotInspectOrMutateProjectBCodeGraph(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	er := localstore.NewEventRepo(store.DB())
	socketPath := filepath.Join(t.TempDir(), "gateway.sock")
	srv, err := NewMultiOrg(socketPath, map[string]runtimeconfig.Org{
		"a": {Name: "a", Credentials: runtimeconfig.Credentials{ProjectID: "project-a", AgentID: "agent-a"}},
		"b": {Name: "b", Credentials: runtimeconfig.Credentials{ProjectID: "project-b", AgentID: "agent-b"}},
	}, []runtimeconfig.ProjectBinding{{ProjectID: "project-a", OrgName: "a"}, {ProjectID: "project-b", OrgName: "b"}}, store, localstore.NewTaskRepo(store.DB(), er), er, localstore.NewKBRepo(store.DB()), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go srv.Serve(ctx)
	t.Cleanup(func() { cancel(); _ = srv.Close() })

	checkout := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "test@example.invalid"}, {"config", "user.name", "test"}, {"remote", "add", "origin", "https://example.invalid/b.git"}} {
		runCodeGraphGit(t, checkout, args...)
	}
	writeCodeGraphFile(t, checkout, "go.mod", "module example.invalid/b\n\ngo 1.26\n")
	writeCodeGraphFile(t, checkout, "b.go", "package b\nfunc TargetB() {}\n")
	runCodeGraphGit(t, checkout, "add", ".")
	runCodeGraphGit(t, checkout, "commit", "-m", "b")
	runtimeB, err := NewCodeGraphRuntime(context.Background(), store.DB(), "project-b")
	if err != nil {
		t.Fatal(err)
	}
	if err := runtimeB.Store.PutProjectConfig(context.Background(), codegraphconfig.Project{ProjectID: "project-b", Enabled: true, CanonicalRemote: "https://example.invalid/b.git", ActiveCheckout: checkout, ProjectSourceByteCeiling: codegraphconfig.DefaultProjectSourceByteCeiling}); err != nil {
		t.Fatal(err)
	}
	if err := runtimeB.Index.Build(context.Background(), codegraphindex.BuildRequest{ProjectID: "project-b", RevisionID: "b-active"}); err != nil {
		t.Fatal(err)
	}
	srv.SetCodeGraphRuntime("project-b", runtimeB)
	srv.SetAuthorizationAgent("project-b", "agent-b")
	if err := store.CacheWhoAmI(context.Background(), localstore.WhoAmICache{AgentID: "agent-a", ProjectID: "project-a", Permissions: []string{"code_graph.query", "code_graph.status", "code_graph.rebuild"}, CachedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	conn := dialLocalSocket(t, socketPath)
	defer conn.Close()
	reader := bufio.NewReader(conn)
	mcpInitialize(t, conn, reader)
	for index, call := range []struct {
		name string
		args map[string]interface{}
	}{
		{"wormhole.code_graph.query", map[string]interface{}{"project_id": "project-b", "entry_symbols": []string{"TargetB"}}},
		{"wormhole.code_graph.status", map[string]interface{}{"project_id": "project-b"}},
		{"wormhole.code_graph.rebuild", map[string]interface{}{"project_id": "project-b"}},
	} {
		if resp := mcpCallTool(t, conn, reader, 200+index, call.name, call.args); !strings.Contains(resp.Error, "no authenticated scope") {
			t.Errorf("%s cross-project error = %q", call.name, resp.Error)
		}
	}
	active, err := runtimeB.Store.ActiveRevision(context.Background())
	if err != nil || active.ID != "b-active" {
		t.Fatalf("project B graph mutated: active=%+v err=%v", active, err)
	}
}

func TestMCPCodeGraphFreshnessComparesPinnedTrackedGoInventory(t *testing.T) {
	srv, runtime, checkout := newCodeGraphStatusFixture(t)
	buildCodeGraphFixture(t, runtime, "clean-base")
	srv.SetAuthorizationAgent("project-1", "agent-1")
	if err := srv.store.CacheWhoAmI(context.Background(), localstore.WhoAmICache{
		AgentID: "agent-1", ProjectID: "project-1",
		Permissions: []string{"code_graph.query", "code_graph.source.read", "code_graph.status", "code_graph.rebuild"},
		CachedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	conn := dialLocalSocket(t, srv.socketPath)
	defer conn.Close()
	reader := bufio.NewReader(conn)
	mcpInitialize(t, conn, reader)
	queryArgs := map[string]interface{}{"project_id": "project-1", "entry_symbols": []string{"Target"}}
	statusArgs := map[string]interface{}{"project_id": "project-1"}

	writeCodeGraphFile(t, checkout, "target.go", "package fixture\nfunc Target() int { return 99 }\n")
	if response := mcpCallTool(t, conn, reader, 300, "wormhole.code_graph.rebuild", statusArgs); response.Error != "" {
		t.Fatalf("dirty Go rebuild: %s", response.Error)
	}
	assertMCPCodeGraphFreshness(t, mcpCallTool(t, conn, reader, 301, "wormhole.code_graph.query", queryArgs), "dirty", false)
	assertMCPCodeGraphStatusFreshness(t, mcpCallTool(t, conn, reader, 302, "wormhole.code_graph.status", statusArgs), "ready", 1)

	runCodeGraphGit(t, checkout, "restore", "--", "target.go")
	assertMCPCodeGraphFreshness(t, mcpCallTool(t, conn, reader, 303, "wormhole.code_graph.query", queryArgs), "clean", true)
	assertMCPCodeGraphStatusFreshness(t, mcpCallTool(t, conn, reader, 304, "wormhole.code_graph.status", statusArgs), "stale", 0)

	if response := mcpCallTool(t, conn, reader, 305, "wormhole.code_graph.rebuild", statusArgs); response.Error != "" {
		t.Fatalf("clean Go rebuild: %s", response.Error)
	}
	if err := os.WriteFile(filepath.Join(checkout, "go.mod"), []byte("module example.invalid/status\n\ngo 1.26\n// dirty non-Go metadata\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertMCPCodeGraphFreshness(t, mcpCallTool(t, conn, reader, 306, "wormhole.code_graph.query", queryArgs), "dirty", false)
	assertMCPCodeGraphStatusFreshness(t, mcpCallTool(t, conn, reader, 307, "wormhole.code_graph.status", statusArgs), "ready", 1)
	runCodeGraphGit(t, checkout, "add", "go.mod")
	runCodeGraphGit(t, checkout, "commit", "-m", "non-Go-only commit")
	assertMCPCodeGraphFreshness(t, mcpCallTool(t, conn, reader, 308, "wormhole.code_graph.query", queryArgs), "clean", false)
	assertMCPCodeGraphStatusFreshness(t, mcpCallTool(t, conn, reader, 309, "wormhole.code_graph.status", statusArgs), "ready", 0)
}

func assertMCPCodeGraphFreshness(t *testing.T, response mcpToolResponse, wantWorking string, wantNotCurrent bool) {
	t.Helper()
	if response.Error != "" {
		t.Fatalf("query: %s", response.Error)
	}
	var result codeGraphQueryResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.WorkingTreeStatus != wantWorking || result.GraphNotCurrent != wantNotCurrent || result.RebuildRecommended != wantNotCurrent {
		t.Fatalf("query freshness = working:%q graph_not_current:%v rebuild:%v, want %q/%v/%v", result.WorkingTreeStatus, result.GraphNotCurrent, result.RebuildRecommended, wantWorking, wantNotCurrent, wantNotCurrent)
	}
}

func assertMCPCodeGraphStatusFreshness(t *testing.T, response mcpToolResponse, wantState string, wantDirty int) {
	t.Helper()
	if response.Error != "" {
		t.Fatalf("status: %s", response.Error)
	}
	var result codeGraphStatusResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.State != wantState || result.DirtyTrackedFileCount != wantDirty {
		t.Fatalf("status freshness = state:%q dirty:%d, want %q/%d", result.State, result.DirtyTrackedFileCount, wantState, wantDirty)
	}
}

func mcpToolsList(t *testing.T, conn net.Conn, reader *bufio.Reader, id int) []toolListEntry {
	t.Helper()
	req, _ := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage(strconv.Itoa(id)), Method: "tools/list"})
	if _, err := conn.Write(append(req, '\n')); err != nil {
		t.Fatal(err)
	}
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response rpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(line), &response); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Tools []toolListEntry `json:"tools"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result.Tools
}
