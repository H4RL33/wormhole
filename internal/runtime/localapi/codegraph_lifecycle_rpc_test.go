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
	"strings"
	"testing"
	"time"

	codegraphconfig "github.com/H4RL33/wormhole/internal/runtime/codegraph/config"
	runtimeconfig "github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
)

const privateCodeGraphLifecycleTestMethod = "wormhole/code-graph/lifecycle"

func TestPrivateCodeGraphLifecycleIsHiddenHandshakeOnlyAndClosed(t *testing.T) {
	srv, socketPath := newMCPTestServer(t)
	notificationOnly := dialLocalSocket(t, socketPath)
	notificationReader := bufio.NewReader(notificationOnly)
	writeMCPRequest(t, notificationOnly, rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"})
	notificationResponse := callPrivateCodeGraphLifecycle(t, notificationOnly, notificationReader, 0, json.RawMessage(`{"operation":"status","project_id":"project-1"}`))
	_ = notificationOnly.Close()
	if notificationResponse.Error == nil || notificationResponse.Error.Code != rpcServerNotInitialized {
		t.Fatalf("private lifecycle after notification-only handshake error = %+v, want server not initialized", notificationResponse.Error)
	}

	conn := dialLocalSocket(t, socketPath)
	defer conn.Close()
	reader := bufio.NewReader(conn)

	response := callPrivateCodeGraphLifecycle(t, conn, reader, 1, json.RawMessage(`{"operation":"status","project_id":"project-1"}`))
	if response.Error == nil || response.Error.Code != rpcServerNotInitialized {
		t.Fatalf("private lifecycle before handshake error = %+v, want server not initialized", response.Error)
	}
	mcpInitialize(t, conn, reader)

	writeMCPRequest(t, conn, rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("2"), Method: "tools/list"})
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var listResponse rpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(line), &listResponse); err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Tools []toolListEntry `json:"tools"`
	}
	if err := json.Unmarshal(listResponse.Result, &listed); err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name == privateCodeGraphLifecycleTestMethod {
			t.Fatal("private Code Graph lifecycle method leaked through tools/list")
		}
	}
	if throughTools := mcpCallTool(t, conn, reader, 3, privateCodeGraphLifecycleTestMethod, map[string]any{
		"operation": "status", "project_id": "project-1",
	}); throughTools.Error == "" {
		t.Fatal("private Code Graph lifecycle method was callable through tools/call")
	}

	for index, field := range []string{"credential", "credential_profile", "profile", "agent_id", "passport", "passport_id", "readiness", "ready"} {
		params := json.RawMessage(`{"operation":"status","project_id":"project-1","` + field + `":true}`)
		response = callPrivateCodeGraphLifecycle(t, conn, reader, 10+index, params)
		if response.Error == nil || response.Error.Code != rpcInvalidParams {
			t.Errorf("private lifecycle accepted caller-controlled %s: %+v", field, response)
		}
	}
	var trailing CodeGraphLifecycleRequest
	if err := decodeClosedJSON(json.RawMessage(`{"operation":"status","project_id":"project-1"} {}`), &trailing); err == nil {
		t.Error("private lifecycle decoder accepted trailing JSON")
	}

	for index, operation := range []CodeGraphLifecycleOperation{CodeGraphStatus, CodeGraphCheckoutShow, CodeGraphDisable} {
		params, _ := json.Marshal(CodeGraphLifecycleRequest{Operation: operation, ProjectID: "project-1"})
		response = callPrivateCodeGraphLifecycle(t, conn, reader, 30+index, params)
		if response.Error != nil {
			t.Fatalf("credential-free %s error = %+v", operation, response.Error)
		}
		var status CodeGraphLifecycleStatus
		if err := json.Unmarshal(response.Result, &status); err != nil {
			t.Fatal(err)
		}
		if operation != CodeGraphDisable && status.ProjectID != "project-1" {
			t.Fatalf("credential-free %s status = %+v", operation, status)
		}
	}
	if _, loaded := srv.codeGraphs.Load("project-1"); !loaded {
		t.Fatal("credential-free lifecycle did not use the daemon-owned project runtime")
	}
}

func TestPrivateCodeGraphLifecycleAllOperationsDeriveReadyBinding(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	checkoutA := lifecycleGitRepository(t, "https://example.invalid/approved.git", "package approved\nfunc A() {}\n")
	checkoutB := lifecycleGitRepository(t, "https://example.invalid/approved.git", "package approved\nfunc B() {}\n")
	srv, socketPath := newMCPTestServer(t)
	seedPrivateLifecycleReadyCheckpoint(t, srv, "project-1", "profile", "agent", "passport", []string{"https://example.invalid/approved.git"})
	writePrivateLifecycleCredential(t, "profile", runtimeconfig.Credentials{
		Server: "https://fabric.example", ProjectID: "project-1", AgentID: "agent", PassportID: "passport", Token: "secret",
	})

	conn := dialLocalSocket(t, socketPath)
	defer conn.Close()
	reader := bufio.NewReader(conn)
	mcpInitialize(t, conn, reader)
	requests := []CodeGraphLifecycleRequest{
		{Operation: CodeGraphEnable, ProjectID: "project-1", Checkout: checkoutA},
		{Operation: CodeGraphStatus, ProjectID: "project-1"},
		{Operation: CodeGraphCheckoutShow, ProjectID: "project-1"},
		{Operation: CodeGraphCheckoutSet, ProjectID: "project-1", Checkout: checkoutB},
		{Operation: CodeGraphRebuild, ProjectID: "project-1"},
		{Operation: CodeGraphDisable, ProjectID: "project-1"},
	}
	for index, request := range requests {
		params, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		response := callPrivateCodeGraphLifecycle(t, conn, reader, 100+index, params)
		if response.Error != nil {
			t.Fatalf("%s error = %+v", request.Operation, response.Error)
		}
	}
	runtime, err := srv.resolveCodeGraphRuntime("project-1")
	if err != nil {
		t.Fatal(err)
	}
	status, err := runtime.Lifecycle.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled || status.ActiveCheckout != "" || status.ActiveRevision != "" {
		t.Fatalf("disable did not remove project graph state: %+v", status)
	}
}

func TestPrivateCodeGraphLifecycleRejectsInvalidBindingsWithoutGraphMutation(t *testing.T) {
	tests := []struct {
		name           string
		requestProject string
		credentials    []runtimeconfig.Credentials
		seedReady      bool
		markUnready    bool
		want           string
	}{
		{name: "missing", requestProject: "project-1", want: "no credential profile"},
		{name: "ambiguous", requestProject: "project-1", credentials: []runtimeconfig.Credentials{
			{ProjectID: "project-1", AgentID: "agent", PassportID: "passport", Token: "one"},
			{ProjectID: "project-1", AgentID: "agent", PassportID: "passport", Token: "two"},
		}, want: "multiple credential profiles"},
		{name: "project mismatch", requestProject: "project-2", credentials: []runtimeconfig.Credentials{
			{ProjectID: "project-1", AgentID: "agent", PassportID: "passport", Token: "secret"},
		}, want: "no credential profile"},
		{name: "identity mismatch", requestProject: "project-1", credentials: []runtimeconfig.Credentials{
			{ProjectID: "project-1", AgentID: "other-agent", PassportID: "other-passport", Token: "secret"},
		}, seedReady: true, want: "not a ready bootstrapped checkpoint"},
		{name: "unready", requestProject: "project-1", credentials: []runtimeconfig.Credentials{
			{ProjectID: "project-1", AgentID: "agent", PassportID: "passport", Token: "secret"},
		}, seedReady: true, markUnready: true, want: "not a ready bootstrapped checkpoint"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", filepath.Join(root, "home"))
			srv, socketPath := newMCPTestServer(t)
			if tt.seedReady {
				seedPrivateLifecycleReadyCheckpoint(t, srv, "project-1", "profile-a", "agent", "passport", []string{"https://example.invalid/approved.git"})
			}
			if tt.markUnready {
				if _, err := srv.store.DB().Exec(`UPDATE enrolment_attempts SET state = 'recovery_required', terminal = 0 WHERE project_id = 'project-1'`); err != nil {
					t.Fatal(err)
				}
			}
			for index, credentials := range tt.credentials {
				profile := "profile-" + string(rune('a'+index))
				credentials.Server = "https://fabric.example"
				writePrivateLifecycleCredential(t, profile, credentials)
			}
			before := sqliteTotalChanges(t, srv)
			conn := dialLocalSocket(t, socketPath)
			reader := bufio.NewReader(conn)
			mcpInitialize(t, conn, reader)
			for index, request := range []CodeGraphLifecycleRequest{
				{Operation: CodeGraphEnable, ProjectID: tt.requestProject, Checkout: "/enable"},
				{Operation: CodeGraphCheckoutSet, ProjectID: tt.requestProject, Checkout: "/set"},
				{Operation: CodeGraphRebuild, ProjectID: tt.requestProject},
			} {
				params, _ := json.Marshal(request)
				response := callPrivateCodeGraphLifecycle(t, conn, reader, 200+index, params)
				if response.Error == nil || !strings.Contains(response.Error.Message, tt.want) {
					t.Errorf("%s invalid binding error = %+v, want containing %q", request.Operation, response.Error, tt.want)
				}
			}
			_ = conn.Close()
			if after := sqliteTotalChanges(t, srv); after != before {
				t.Fatalf("invalid binding changed SQLite rows: before=%d after=%d", before, after)
			}
			if _, loaded := srv.codeGraphs.Load(tt.requestProject); loaded {
				t.Fatal("invalid binding created a Code Graph runtime before authority validation")
			}
		})
	}
}

func TestPrivateCodeGraphLifecycleRejectsUnsupportedOperationBeforeGraphRuntime(t *testing.T) {
	srv, socketPath := newMCPTestServer(t)
	before := sqliteTotalChanges(t, srv)
	conn := dialLocalSocket(t, socketPath)
	defer conn.Close()
	reader := bufio.NewReader(conn)
	mcpInitialize(t, conn, reader)
	response := callPrivateCodeGraphLifecycle(t, conn, reader, 250, json.RawMessage(`{"operation":"unknown","project_id":"project-1"}`))
	if response.Error == nil || !strings.Contains(response.Error.Message, "unsupported operation") {
		t.Fatalf("unsupported operation response = %+v", response)
	}
	if after := sqliteTotalChanges(t, srv); after != before {
		t.Fatalf("unsupported operation changed SQLite rows: before=%d after=%d", before, after)
	}
	if _, loaded := srv.codeGraphs.Load("project-1"); loaded {
		t.Fatal("unsupported operation created a Code Graph runtime")
	}
}

func callPrivateCodeGraphLifecycle(t *testing.T, conn net.Conn, reader *bufio.Reader, id int, params json.RawMessage) rpcResponse {
	t.Helper()
	response, err := callPrivateCodeGraphLifecycleConn(conn, reader, id, params)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func callPrivateCodeGraphLifecycleConn(conn net.Conn, reader *bufio.Reader, id int, params json.RawMessage) (rpcResponse, error) {
	idRaw, _ := json.Marshal(id)
	requestRaw, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: idRaw, Method: privateCodeGraphLifecycleTestMethod, Params: params})
	if err != nil {
		return rpcResponse{}, err
	}
	if _, err := conn.Write(append(requestRaw, '\n')); err != nil {
		return rpcResponse{}, err
	}
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return rpcResponse{}, err
	}
	var response rpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(line), &response); err != nil {
		return rpcResponse{}, err
	}
	return response, nil
}

func writePrivateLifecycleCredential(t *testing.T, profile string, credentials runtimeconfig.Credentials) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeconfig.WriteCredentialProfile(filepath.Join(home, ".wormhole", "credentials"), profile, credentials); err != nil {
		t.Fatal(err)
	}
}

func seedPrivateLifecycleReadyCheckpoint(t *testing.T, srv *Server, project, profile, agent, passport string, repositories []string) {
	t.Helper()
	repositoriesJSON, err := json.Marshal(repositories)
	if err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO projects(namespace_id,id,name,owner,created_at) VALUES (?,?,?,?,CURRENT_TIMESTAMP)`, []any{project, project, "project", "owner"}},
		{`INSERT INTO agents(namespace_id,id,owner,model,capabilities,created_at) VALUES (?,?,?,?,?,CURRENT_TIMESTAMP)`, []any{project, agent, "owner", "model", `[]`}},
		{`INSERT INTO passports(namespace_id,id,agent_id,project_id,repositories,roles,issued_at) VALUES (?,?,?,?,?,?,CURRENT_TIMESTAMP)`, []any{project, passport, agent, project, string(repositoriesJSON), `[]`}},
		{`INSERT INTO auth_scopes(namespace_id,agent_id,passport_id,permissions) VALUES (?,?,?,?)`, []any{project, agent, passport, `[]`}},
		{`INSERT INTO whoami_cache(agent_id,owner,model,capabilities,project_id,permissions,cached_at) VALUES (?,?,?,?,?,?,CURRENT_TIMESTAMP)`, []any{agent, "owner", "model", `[]`, project, `[]`}},
		{`INSERT INTO enrolment_attempts(project_id,idempotency_key,request_hash,state,credential_profile,agent_id,passport_id,terminal) VALUES (?,?,?,?,?,?,?,1)`, []any{project, "key", "hash", "ready", profile, agent, passport}},
		{`INSERT INTO bootstrap_metadata(namespace_id,schema_version,integration_manifest_metadata,bootstrap_timestamp) VALUES (?,1,'{}',CURRENT_TIMESTAMP)`, []any{project}},
	}
	for _, statement := range statements {
		if _, err := srv.store.DB().Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed ready checkpoint: %v", err)
		}
	}
}

func sqliteTotalChanges(t *testing.T, srv *Server) int64 {
	t.Helper()
	var changes int64
	if err := srv.store.DB().QueryRow(`SELECT total_changes()`).Scan(&changes); err != nil {
		t.Fatal(err)
	}
	return changes
}

func TestPrivateCodeGraphLifecycleSerializesWithPublicRebuild(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	srv, socketPath := newMCPTestServer(t)
	seedPrivateLifecycleReadyCheckpoint(t, srv, "project-1", "profile", "agent", "passport", []string{"https://example.invalid/approved.git"})
	writePrivateLifecycleCredential(t, "profile", runtimeconfig.Credentials{
		Server: "https://fabric.example", ProjectID: "project-1", AgentID: "agent", PassportID: "passport", Token: "secret",
	})
	runtime, err := NewCodeGraphRuntime(ctx, srv.store.DB(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := runtime.Lifecycle
	checkout := lifecycleGitRepository(t, "https://example.invalid/approved.git", "package approved\nfunc Ready() {}\n")
	if _, err := lifecycle.Enable(ctx, checkout); err != nil {
		t.Fatal(err)
	}
	srv.SetCodeGraphRuntime("project-1", runtime)
	conn := dialLocalSocket(t, socketPath)
	defer conn.Close()
	reader := bufio.NewReader(conn)
	mcpInitialize(t, conn, reader)

	privateEntered := make(chan struct{})
	releasePrivate := make(chan struct{})
	lifecycle.beforeBuild = func() {
		close(privateEntered)
		<-releasePrivate
	}
	privateDone := make(chan error, 1)
	go func() {
		params, _ := json.Marshal(CodeGraphLifecycleRequest{Operation: CodeGraphRebuild, ProjectID: "project-1"})
		response, callErr := callPrivateCodeGraphLifecycleConn(conn, reader, 300, params)
		if callErr == nil && response.Error != nil {
			callErr = errors.New(response.Error.Message)
		}
		privateDone <- callErr
	}()
	select {
	case <-privateEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("private lifecycle did not reach build barrier")
	}

	_, publicErr := srv.handleCodeGraphRebuild(ctx, json.RawMessage(`{"project_id":"project-1"}`))
	close(releasePrivate)
	if err := <-privateDone; err != nil {
		t.Fatalf("private rebuild: %v", err)
	}
	if publicErr == nil || !strings.Contains(publicErr.Error(), "already in progress") {
		t.Errorf("public rebuild during private lifecycle = %v, want already-in-progress rejection", publicErr)
	}

	lazyStore, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lazyStore.Close() })
	lazyServer := &Server{store: lazyStore}
	start := make(chan struct{})
	runtimes := make(chan CodeGraphRuntime, 2)
	errorsCh := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			created, createErr := lazyServer.ensureCodeGraphRuntime(ctx, "project-lazy")
			if createErr != nil {
				errorsCh <- createErr
				return
			}
			runtimes <- created
		}()
	}
	close(start)
	var first, second CodeGraphRuntime
	for i := 0; i < 2; i++ {
		select {
		case err := <-errorsCh:
			t.Fatal(err)
		case got := <-runtimes:
			if i == 0 {
				first = got
			} else {
				second = got
			}
		}
	}
	if first.lifecycleMu != second.lifecycleMu || first.Lifecycle != second.Lifecycle {
		t.Errorf("concurrent first access published distinct project executors: %p/%p != %p/%p", first.lifecycleMu, first.Lifecycle, second.lifecycleMu, second.Lifecycle)
	}
}

func TestCodeGraphProjectExecutorsAreIndependent(t *testing.T) {
	ctx := context.Background()
	_, db := newLifecycleTestState(t, "project-a", []string{"https://example.invalid/a.git"})
	runtimeA, err := NewCodeGraphRuntime(ctx, db, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	privateLifecycle := runtimeA.Lifecycle
	checkoutA := lifecycleGitRepository(t, "https://example.invalid/a.git", "package a\nfunc A() {}\n")
	if _, err := privateLifecycle.Enable(ctx, checkoutA); err != nil {
		t.Fatal(err)
	}

	checkoutB := lifecycleGitRepository(t, "https://example.invalid/b.git", "package b\nfunc B() {}\n")
	runtimeB, err := NewCodeGraphRuntime(ctx, db, "project-b")
	if err != nil {
		t.Fatal(err)
	}
	if err := runtimeB.Store.PutProjectConfig(ctx, codegraphconfig.Project{
		ProjectID: "project-b", Enabled: true, CanonicalRemote: "https://example.invalid/b.git",
		ActiveCheckout: checkoutB, ProjectSourceByteCeiling: codegraphconfig.DefaultProjectSourceByteCeiling,
	}); err != nil {
		t.Fatal(err)
	}
	srv := &Server{projectID: "project-b"}
	srv.SetCodeGraphRuntime("project-b", runtimeB)

	privateEntered := make(chan struct{})
	releasePrivate := make(chan struct{})
	privateLifecycle.beforeBuild = func() {
		close(privateEntered)
		<-releasePrivate
	}
	privateDone := make(chan error, 1)
	go func() {
		_, err := privateLifecycle.executeWithBinding(ctx, CodeGraphLifecycleRequest{
			Operation: CodeGraphRebuild, ProjectID: "project-a",
		}, codeGraphRepositoryBinding{profile: "profile", agent: "agent", passport: "passport"})
		privateDone <- err
	}()
	select {
	case <-privateEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("project A lifecycle did not reach build barrier")
	}

	publicDone := make(chan error, 1)
	go func() {
		_, err := srv.handleCodeGraphRebuild(ctx, json.RawMessage(`{"project_id":"project-b"}`))
		publicDone <- err
	}()
	var publicErr error
	select {
	case publicErr = <-publicDone:
	case <-time.After(5 * time.Second):
		close(releasePrivate)
		<-privateDone
		t.Fatal("project B rebuild waited for project A executor")
	}
	close(releasePrivate)
	if err := <-privateDone; err != nil {
		t.Fatalf("project A private rebuild: %v", err)
	}
	if publicErr != nil {
		t.Fatalf("project B rebuild while project A blocked: %v", publicErr)
	}
}
