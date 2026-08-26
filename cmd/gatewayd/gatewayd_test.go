package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	stdsync "sync"
	"syscall"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localapi"
	"github.com/H4RL33/wormhole/internal/runtime/localidentity"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
)

func TestSupervisorDependenciesConstructLocalOnlyGraph(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	identity, err := localidentity.Open(filepath.Join(t.TempDir(), "identities"))
	if err != nil {
		t.Fatal(err)
	}
	supervisor, err := newLocalSupervisor(store, identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().PingContext(context.Background()); err != nil {
		t.Fatalf("supervisor took ownership of injected Store: %v", err)
	}
	if _, err := newLocalSupervisor(nil, identity); err == nil {
		t.Fatal("incomplete production graph accepted")
	}
}

func gatewayTestBootstrapOutput(projectID, agentID, passportID string) map[string]any {
	now := time.Date(2026, 7, 25, 12, 0, 0, 123, time.UTC)
	org := types.BootstrapOrgConfigV1{
		SchemaVersion: types.BootstrapSchemaVersionV1,
		Project:       types.BootstrapProjectV1{ID: projectID, Name: "project", Owner: "owner", CreatedAt: now},
		Identity: types.BootstrapIdentityV1{
			Agent:       types.BootstrapAgentV1{ID: agentID, Owner: "owner", Model: "model", Capabilities: []string{}, CreatedAt: now},
			Passport:    types.BootstrapPassportV1{ID: passportID, AgentID: agentID, ProjectID: projectID, Repositories: []string{}, Roles: []string{}, IssuedAt: now},
			Permissions: []string{"task.create", "read_kb"},
		},
		Channels: []types.BootstrapChannelV1{}, Events: []types.BootstrapEventV1{}, Tasks: []types.BootstrapTaskV1{},
		KB: types.BootstrapKBV1{Articles: []types.BootstrapArticleV1{}}, IntegrationManifestMetadata: json.RawMessage(`null`),
	}
	return map[string]any{"org_config": org, "project_list": []string{}, "task_list": org.Tasks, "kb_list": org.KB.Articles, "timestamp": now.Format(time.RFC3339Nano), "version": 1}
}

func TestRun_FreshSupervisorRequiresBindingContext(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runDir := filepath.Join(home, "run")
	t.Setenv("XDG_RUNTIME_DIR", runDir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, "default") }()

	socketPath := filepath.Join(runDir, "wormhole", "wormholed.sock")
	var conn net.Conn
	var err error
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.Dial("unix", socketPath)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		cancel()
		select {
		case runErr := <-errCh:
			t.Fatalf("fresh Gateway did not expose socket: dial=%v run=%v", err, runErr)
		default:
			t.Fatalf("fresh Gateway did not expose socket: %v", err)
		}
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	mcpInitialize(t, conn, reader)
	resp := mcpCallTool(t, conn, reader, 2, localapi.EnrolmentToolName, map[string]interface{}{})
	if !strings.Contains(resp.Error, "invalid private request context") {
		cancel()
		t.Fatalf("fresh Gateway enrolment endpoint error = %q, want binding-aware fail-closed error", resp.Error)
	}

	cancel()
	select {
	case runErr := <-errCh:
		if runErr != nil {
			t.Fatalf("fresh Gateway shutdown: %v", runErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fresh Gateway did not shut down")
	}
}

// Local MCP types (duplicated from internal/runtime/localapi for test use).
type mcpRpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpRpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *mcpRpcError    `json:"error,omitempty"`
}

type mcpRpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpToolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type mcpToolCallResultContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpToolCallResult struct {
	Content []mcpToolCallResultContent `json:"content"`
	IsError bool                       `json:"isError,omitempty"`
}

// mcpToolResponse mirrors the MCP response for test convenience.
type mcpToolResponse struct {
	Result json.RawMessage
	Error  string
}

type runningTestDaemon struct {
	cancel   context.CancelFunc
	errCh    chan error
	stopOnce stdsync.Once
}

func configureSecurityTestDaemon(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	runDir := filepath.Join(home, "run")
	t.Setenv("XDG_RUNTIME_DIR", runDir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	credDir := filepath.Join(home, ".wormhole", "credentials")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatalf("create credentials directory: %v", err)
	}
	credData, err := json.Marshal(map[string]string{
		"server": "http://127.0.0.1:1", "project_id": "project-1", "agent_id": "agent-1", "passport_id": "passport-1", "token": "test-token",
	})
	if err != nil {
		t.Fatalf("marshal credentials: %v", err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "default.json"), credData, 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	return filepath.Join(runDir, "wormhole", "wormholed.sock")
}

func TestRun_StalePathSocketReplaced(t *testing.T) {
	socketPath := configureSecurityTestDaemon(t)
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		t.Fatalf("create socket directory: %v", err)
	}
	addr, err := net.ResolveUnixAddr("unix", socketPath)
	if err != nil {
		t.Fatalf("resolve socket address: %v", err)
	}
	stale, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatalf("create stale socket: %v", err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatalf("close stale socket: %v", err)
	}

	runner := Run
	daemon := startTestDaemonWithRunner(t, "default", socketPath, runner)
	info, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatalf("lstat replacement socket: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("replacement path mode = %v, want socket", info.Mode())
	}
	daemon.stop(t)
}

func TestRun_StalePathRegularFileRejectedWithoutRemoval(t *testing.T) {
	socketPath := configureSecurityTestDaemon(t)
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		t.Fatalf("create socket directory: %v", err)
	}
	const contents = "do not remove"
	if err := os.WriteFile(socketPath, []byte(contents), 0o600); err != nil {
		t.Fatalf("write stale-path sentinel: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, "default") }()
	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "not a socket") {
			t.Fatalf("Run error = %v, want non-socket stale-path rejection", err)
		}
	case <-time.After(10 * time.Second):
		cancel()
		select {
		case <-errCh:
		case <-time.After(time.Second):
		}
		t.Fatal("Run did not reject a regular file at the socket path")
	}
	got, err := os.ReadFile(socketPath)
	if err != nil {
		t.Fatalf("read preserved stale-path sentinel: %v", err)
	}
	if string(got) != contents {
		t.Fatalf("stale-path sentinel = %q, want %q", got, contents)
	}
}

func TestRemoveStaleSocket_ActiveDaemonPreserved(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "active.sock")
	addr, err := net.ResolveUnixAddr("unix", socketPath)
	if err != nil {
		t.Fatalf("resolve socket address: %v", err)
	}
	listener, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatalf("listen active socket: %v", err)
	}
	defer listener.Close()

	err = removeStaleSocket(socketPath)
	if err == nil || !strings.Contains(err.Error(), "active daemon") {
		t.Fatalf("removeStaleSocket error = %v, want active-daemon rejection", err)
	}
	info, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatalf("active socket was removed: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("active path mode = %v, want socket", info.Mode())
	}
	conn, err := net.DialTimeout("unix", socketPath, time.Second)
	if err != nil {
		t.Fatalf("dial preserved active socket: %v", err)
	}
	_ = conn.Close()
}

func TestRemoveStaleSocket_ReplacementAfterInitialInspectionPreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wormholed.sock")
	addr, err := net.ResolveUnixAddr("unix", path)
	if err != nil {
		t.Fatalf("resolve stale socket: %v", err)
	}
	stale, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatalf("create stale socket: %v", err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatalf("close stale socket: %v", err)
	}

	var replacement os.FileInfo
	err = removeStaleSocketWithHooks(path, staleSocketRemovalHooks{
		afterInitialInspection: func() {
			if err := os.Remove(path); err != nil {
				t.Fatalf("remove initially inspected socket: %v", err)
			}
			replacementListener, err := net.ListenUnix("unix", addr)
			if err != nil {
				t.Fatalf("create replacement socket: %v", err)
			}
			replacementListener.SetUnlinkOnClose(false)
			if err := replacementListener.Close(); err != nil {
				t.Fatalf("close replacement socket: %v", err)
			}
			replacement, err = os.Lstat(path)
			if err != nil {
				t.Fatalf("lstat replacement socket: %v", err)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "changed during stale-socket removal") {
		t.Fatalf("removeStaleSocketWithHooks error = %v, want replacement rejection", err)
	}
	preserved, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat preserved replacement: %v", err)
	}
	if !os.SameFile(replacement, preserved) {
		t.Fatal("replacement created after initial inspection was not preserved")
	}
}

func TestRemoveStaleSocket_NonSocketsPreserved(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
		mode  os.FileMode
	}{
		{
			name: "symlink",
			setup: func(t *testing.T, path string) {
				t.Helper()
				target := filepath.Join(filepath.Dir(path), "target")
				if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
					t.Fatalf("write symlink target: %v", err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatalf("create symlink: %v", err)
				}
			},
			mode: os.ModeSymlink,
		},
		{
			name: "directory",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatalf("create directory: %v", err)
				}
			},
			mode: os.ModeDir,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wormholed.sock")
			tt.setup(t, path)
			if err := removeStaleSocket(path); err == nil || !strings.Contains(err.Error(), "not a socket") {
				t.Fatalf("removeStaleSocket error = %v, want non-socket rejection", err)
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatalf("replacement was removed: %v", err)
			}
			if info.Mode()&tt.mode == 0 {
				t.Fatalf("preserved path mode = %v, want %v", info.Mode(), tt.mode)
			}
		})
	}
}

func requireOpenFileIdentity(t *testing.T, expected os.FileInfo) {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("read process descriptors: %v", err)
	}
	for _, entry := range entries {
		info, err := os.Stat(filepath.Join("/proc/self/fd", entry.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatalf("stat process descriptor %s: %v", entry.Name(), err)
		}
		if os.SameFile(expected, info) {
			return
		}
	}
	t.Fatal("checked socket inode is no longer referenced")
}

func TestRemoveStaleSocket_InodeSwapPreservesReplacement(t *testing.T) {
	tests := []struct {
		name    string
		replace func(*testing.T, string)
		assert  func(*testing.T, string)
	}{
		{
			name: "regular file",
			replace: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
					t.Fatalf("write replacement: %v", err)
				}
			},
			assert: func(t *testing.T, path string) {
				t.Helper()
				got, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read replacement: %v", err)
				}
				if string(got) != "replacement" {
					t.Fatalf("replacement contents = %q", got)
				}
			},
		},
		{
			name: "symlink",
			replace: func(t *testing.T, path string) {
				t.Helper()
				target := filepath.Join(filepath.Dir(path), "target")
				if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
					t.Fatalf("write target: %v", err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatalf("create replacement symlink: %v", err)
				}
			},
			assert: func(t *testing.T, path string) {
				t.Helper()
				info, err := os.Lstat(path)
				if err != nil {
					t.Fatalf("lstat replacement symlink: %v", err)
				}
				if info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("replacement mode = %v, want symlink", info.Mode())
				}
			},
		},
		{
			name: "unix socket",
			replace: func(t *testing.T, path string) {
				t.Helper()
				addr, err := net.ResolveUnixAddr("unix", path)
				if err != nil {
					t.Fatalf("resolve replacement socket: %v", err)
				}
				replacement, err := net.ListenUnix("unix", addr)
				if err != nil {
					t.Fatalf("create replacement socket: %v", err)
				}
				replacement.SetUnlinkOnClose(false)
				if err := replacement.Close(); err != nil {
					t.Fatalf("close replacement socket: %v", err)
				}
			},
			assert: func(t *testing.T, path string) {
				t.Helper()
				info, err := os.Lstat(path)
				if err != nil {
					t.Fatalf("lstat replacement socket: %v", err)
				}
				if info.Mode()&os.ModeSocket == 0 {
					t.Fatalf("replacement mode = %v, want socket", info.Mode())
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wormholed.sock")
			addr, err := net.ResolveUnixAddr("unix", path)
			if err != nil {
				t.Fatalf("resolve stale socket: %v", err)
			}
			stale, err := net.ListenUnix("unix", addr)
			if err != nil {
				t.Fatalf("create stale socket: %v", err)
			}
			stale.SetUnlinkOnClose(false)
			if err := stale.Close(); err != nil {
				t.Fatalf("close stale socket: %v", err)
			}
			checked, err := os.Lstat(path)
			if err != nil {
				t.Fatalf("lstat checked socket: %v", err)
			}

			err = removeStaleSocketWithHooks(path, staleSocketRemovalHooks{
				beforeQuarantine: func() {
					if err := os.Remove(path); err != nil {
						t.Fatalf("replace: remove checked socket: %v", err)
					}
					requireOpenFileIdentity(t, checked)
					tt.replace(t, path)
					replacement, err := os.Lstat(path)
					if err != nil {
						t.Fatalf("lstat replacement: %v", err)
					}
					if os.SameFile(checked, replacement) {
						t.Fatal("checked socket inode was released and reused")
					}
				},
				afterQuarantine: func(string) {
					requireOpenFileIdentity(t, checked)
				},
			})
			if err == nil || !strings.Contains(err.Error(), "changed during stale-socket removal") {
				t.Fatalf("removeStaleSocketWithHook error = %v, want inode-change rejection", err)
			}
			tt.assert(t, path)
		})
	}
}

func TestRemoveStaleSocket_PostQuarantineCollisionPreservesBothPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wormholed.sock")
	addr, err := net.ResolveUnixAddr("unix", path)
	if err != nil {
		t.Fatalf("resolve stale socket: %v", err)
	}
	stale, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatalf("create stale socket: %v", err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatalf("close stale socket: %v", err)
	}
	checked, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat checked socket: %v", err)
	}

	var quarantinePath string
	err = removeStaleSocketWithHooks(path, staleSocketRemovalHooks{
		beforeQuarantine: func() {
			if err := os.Remove(path); err != nil {
				t.Fatalf("remove checked socket: %v", err)
			}
			requireOpenFileIdentity(t, checked)
			if err := os.WriteFile(path, []byte("displaced"), 0o600); err != nil {
				t.Fatalf("write displaced replacement: %v", err)
			}
			replacement, err := os.Lstat(path)
			if err != nil {
				t.Fatalf("lstat displaced replacement: %v", err)
			}
			if os.SameFile(checked, replacement) {
				t.Fatal("checked socket inode was released and reused")
			}
		},
		afterQuarantine: func(movedPath string) {
			quarantinePath = movedPath
			requireOpenFileIdentity(t, checked)
			if err := os.WriteFile(path, []byte("newer"), 0o600); err != nil {
				t.Fatalf("write newer public path: %v", err)
			}
		},
	})
	if !errors.Is(err, syscall.EEXIST) {
		t.Fatalf("removeStaleSocketWithHooks error = %v, want EEXIST restoration collision", err)
	}
	if quarantinePath == "" || !strings.Contains(err.Error(), quarantinePath) {
		t.Fatalf("error %q does not report quarantine path %q", err, quarantinePath)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != "newer" {
		t.Fatalf("public path = %q, %v; want newer", got, readErr)
	}
	if got, readErr := os.ReadFile(quarantinePath); readErr != nil || string(got) != "displaced" {
		t.Fatalf("quarantined path = %q, %v; want displaced", got, readErr)
	}
}

func startTestDaemon(t *testing.T, profileName, socketPath string) *runningTestDaemon {
	t.Helper()
	return startTestDaemonWithRunner(t, profileName, socketPath, Run)
}

func startTestDaemonWithRunner(t *testing.T, profileName, socketPath string, runner func(context.Context, string) error) *runningTestDaemon {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d := &runningTestDaemon{cancel: cancel, errCh: make(chan error, 1)}
	go func() { d.errCh <- runner(ctx, profileName) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.Dial("unix", socketPath)
		if err == nil {
			_ = conn.Close()
			break
		}
		select {
		case runErr := <-d.errCh:
			cancel()
			t.Fatalf("Run returned before socket became ready: %v", runErr)
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("gatewayd socket did not become ready at %s", socketPath)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { d.stop(t) })
	return d
}

func (d *runningTestDaemon) stop(t *testing.T) {
	t.Helper()
	d.stopOnce.Do(func() {
		d.cancel()
		select {
		case err := <-d.errCh:
			if err != nil {
				t.Errorf("Run returned after cancellation: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Run did not stop within 5 seconds")
		}
	})
}

func waitForCondition(t *testing.T, timeout time.Duration, description string, condition func() (bool, error)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		ok, err := condition()
		if err != nil {
			t.Fatalf("wait for %s: %v", description, err)
		}
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type isolatedPush struct {
	NamespaceID string
	Title       string
}

type isolatedCoordServer struct {
	server *httptest.Server
	token  string
	mu     stdsync.Mutex
	accept bool
	tokens []string
	tools  []string
	pushes []isolatedPush
}

func newIsolatedCoordServer(t *testing.T, token string) *isolatedCoordServer {
	t.Helper()
	s := &isolatedCoordServer{token: token}
	s.server = httptest.NewServer(http.HandlerFunc(s.serveHTTP))
	t.Cleanup(s.server.Close)
	return s
}

func (s *isolatedCoordServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	var req mcpRpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var params mcpToolsCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	auth := r.Header.Get("Authorization")
	s.mu.Lock()
	s.tokens = append(s.tokens, auth)
	s.tools = append(s.tools, params.Name)
	s.mu.Unlock()
	if auth != "Bearer "+s.token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var result any
	switch params.Name {
	case "wormhole.agent.whoami":
		var args struct {
			ProjectID string `json:"project_id"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		result = map[string]any{"agent_id": "isolated-agent", "owner": "test", "model": "test", "capabilities": []string{}, "project_id": args.ProjectID, "permissions": []string{"task.create"}}
	case "wormhole.sync.bootstrap":
		var args struct {
			NamespaceID string `json:"namespace_id"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		result = gatewayTestBootstrapOutput(args.NamespaceID, "isolated-agent", "isolated-passport")
	case "wormhole.sync.incremental_pull":
		result = map[string]any{
			"updates": []any{}, "timestamp": time.Now().UTC().Format(time.RFC3339), "version": 1,
		}
	case "wormhole.sync.incremental_push":
		var in struct {
			NamespaceID string `json:"namespace_id"`
			Items       []struct {
				EntityType string `json:"entity_type"`
				EntityID   string `json:"entity_id"`
				Payload    struct {
					Title string `json:"title"`
				} `json:"payload"`
			} `json:"items"`
		}
		if err := json.Unmarshal(params.Arguments, &in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		accept := s.accept
		if accept {
			for _, item := range in.Items {
				s.pushes = append(s.pushes, isolatedPush{NamespaceID: in.NamespaceID, Title: item.Payload.Title})
			}
		}
		s.mu.Unlock()
		if !accept {
			writeToolRPCResponse(w, req.ID, nil, "temporarily offline")
			return
		}
		applied := make([]map[string]any, 0, len(in.Items))
		for _, item := range in.Items {
			applied = append(applied, map[string]any{"id": item.EntityID, "type": item.EntityType, "error": ""})
		}
		result = map[string]any{
			"items_received": len(in.Items), "applied": applied,
			"timestamp": time.Now().UTC().Format(time.RFC3339), "version": 1,
		}
	default:
		http.Error(w, fmt.Sprintf("unexpected tool %q", params.Name), http.StatusNotFound)
		return
	}
	writeToolRPCResponse(w, req.ID, result, "")
}

func writeToolRPCResponse(w http.ResponseWriter, id json.RawMessage, result any, toolErr string) {
	contentText, _ := json.Marshal(result)
	toolResult := map[string]any{
		"content": []map[string]string{{"type": "text", "text": string(contentText)}},
	}
	if toolErr != "" {
		toolResult["isError"] = true
		toolResult["content"] = []map[string]string{{"type": "text", "text": toolErr}}
	}
	resultRaw, _ := json.Marshal(toolResult)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0", "id": id, "result": json.RawMessage(resultRaw),
	})
}

func (s *isolatedCoordServer) setAccept(accept bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accept = accept
}

func (s *isolatedCoordServer) snapshot() ([]string, []string, []isolatedPush) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.tokens...), append([]string(nil), s.tools...), append([]isolatedPush(nil), s.pushes...)
}

func mcpInitialize(t *testing.T, conn net.Conn, reader *bufio.Reader) {
	t.Helper()
	req := mcpRpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "initialize", Params: json.RawMessage(`{}`)}
	reqRaw, _ := json.Marshal(req)
	if _, err := conn.Write(append(reqRaw, '\n')); err != nil {
		t.Fatalf("write initialize: %v", err)
	}
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read initialize response: %v", err)
	}
	var resp mcpRpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(line), &resp); err != nil {
		t.Fatalf("decode initialize response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("initialize error: %+v", resp.Error)
	}
	notification := mcpRpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"}
	notificationRaw, _ := json.Marshal(notification)
	if _, err := conn.Write(append(notificationRaw, '\n')); err != nil {
		t.Fatalf("write notifications/initialized: %v", err)
	}
}

func mcpCallTool(t *testing.T, conn net.Conn, reader *bufio.Reader, id int, tool string, args map[string]interface{}) mcpToolResponse {
	t.Helper()
	argsRaw := json.RawMessage(`{}`)
	if args != nil {
		argsRaw, _ = json.Marshal(args)
	}
	paramsRaw, _ := json.Marshal(mcpToolsCallParams{Name: tool, Arguments: argsRaw})
	idRaw, _ := json.Marshal(id)
	reqRaw, _ := json.Marshal(mcpRpcRequest{JSONRPC: "2.0", ID: idRaw, Method: "tools/call", Params: paramsRaw})
	if _, err := conn.Write(append(reqRaw, '\n')); err != nil {
		t.Fatalf("write tools/call: %v", err)
	}
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read tools/call response: %v", err)
	}
	var resp mcpRpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(line), &resp); err != nil {
		t.Fatalf("decode tools/call response: %v", err)
	}
	if resp.Error != nil {
		return mcpToolResponse{Error: resp.Error.Message}
	}
	var result mcpToolCallResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode tools/call result: %v", err)
	}
	if len(result.Content) == 0 {
		return mcpToolResponse{}
	}
	if result.IsError {
		return mcpToolResponse{Error: result.Content[0].Text}
	}
	return mcpToolResponse{Result: json.RawMessage(result.Content[0].Text)}
}

func TestRun_WhoAmIBindingFailClosed(t *testing.T) {
	coord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req mcpRpcRequest
		json.NewDecoder(r.Body).Decode(&req)
		var params mcpToolsCallParams
		_ = json.Unmarshal(req.Params, &params)
		var out any
		if params.Name == "wormhole.sync.bootstrap" {
			out = gatewayTestBootstrapOutput("project-1", "agent-1", "passport-1")
		} else if params.Name == "wormhole.sync.incremental_pull" {
			out = map[string]any{"updates": []any{}, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "version": 1}
		} else {
			out = map[string]any{
				"agent_id": "agent-1", "owner": "harley", "model": "claude-sonnet-5",
				"capabilities": []string{"code"}, "project_id": "project-1", "permissions": []string{"read_kb"},
			}
		}
		outRaw, _ := json.Marshal(out)
		result := map[string]any{"content": []map[string]string{{"type": "text", "text": string(outRaw)}}}
		resultRaw, _ := json.Marshal(result)
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": json.RawMessage(resultRaw)}
		json.NewEncoder(w).Encode(resp)
	}))
	defer coord.Close()

	home := t.TempDir()
	os.Setenv("HOME", home)
	defer os.Unsetenv("HOME")
	runDir := filepath.Join(home, "run")
	os.Setenv("XDG_RUNTIME_DIR", runDir)
	defer os.Unsetenv("XDG_RUNTIME_DIR")
	dataDir := filepath.Join(home, "data")
	os.Setenv("XDG_DATA_HOME", dataDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	credDir := filepath.Join(home, ".wormhole", "credentials")
	os.MkdirAll(credDir, 0o700)
	credData, _ := json.Marshal(map[string]string{
		"server": coord.URL, "project_id": "project-1", "agent_id": "agent-1", "passport_id": "passport-1", "token": "test-token",
	})
	os.WriteFile(filepath.Join(credDir, "default.json"), credData, 0o600)
	seedEnrolledGatewayCheckpoint(t, filepath.Join(dataDir, "wormhole", "wormholed.db"), coord.URL, "project-1", "agent-1", "passport-1", "test-token", "default")

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, "default") }()

	socketPath := filepath.Join(runDir, "wormhole", "wormholed.sock")
	var conn net.Conn
	var err error
	for i := 0; i < 100; i++ {
		conn, err = net.Dial("unix", socketPath)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		cancel()
		t.Fatalf("dial socket: %v", err)
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)
	mcpInitialize(t, conn, reader)

	resp := mcpCallTool(t, conn, reader, 2, "wormhole.agent.whoami", nil)
	if !strings.Contains(resp.Error, "invalid private request context") {
		cancel()
		t.Fatalf("whoami error = %q, want binding-aware fail-closed error", resp.Error)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned error after cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not shut down after context cancel")
	}
}

// TestRun_AgentRegisterReachesSchedulerAndEventBus asserts that Run wires a
// real scheduler.Scheduler and eventbus.EventBus into the localapi.Server it
// starts (P3), rather than the plain localapi.New(...) call that leaves
// sched/eb nil and makes wormhole.agent.register fail with "scheduler not
// available" (internal/runtime/localapi/localapi.go:657-658). A single
// credential profile should resolve to single-org NewWithRuntime wiring.
func TestRun_AgentRegisterBindingFailClosed(t *testing.T) {
	coord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req mcpRpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeToolRPCResponse(w, req.ID, gatewayTestBootstrapOutput("project-1", "agent-1", "passport-1"), "")
	}))
	defer coord.Close()

	home := t.TempDir()
	os.Setenv("HOME", home)
	defer os.Unsetenv("HOME")
	runDir := filepath.Join(home, "run")
	os.Setenv("XDG_RUNTIME_DIR", runDir)
	defer os.Unsetenv("XDG_RUNTIME_DIR")
	dataDir := filepath.Join(home, "data")
	os.Setenv("XDG_DATA_HOME", dataDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	credDir := filepath.Join(home, ".wormhole", "credentials")
	os.MkdirAll(credDir, 0o700)
	credData, _ := json.Marshal(map[string]string{
		"server": coord.URL, "project_id": "project-1", "agent_id": "agent-1", "passport_id": "passport-1", "token": "test-token",
	})
	os.WriteFile(filepath.Join(credDir, "default.json"), credData, 0o600)
	seedEnrolledGatewayCheckpoint(t, filepath.Join(dataDir, "wormhole", "wormholed.db"), coord.URL, "project-1", "agent-1", "passport-1", "test-token", "default")

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, "default") }()

	socketPath := filepath.Join(runDir, "wormhole", "wormholed.sock")
	var conn net.Conn
	var err error
	for i := 0; i < 100; i++ {
		conn, err = net.Dial("unix", socketPath)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		cancel()
		t.Fatalf("dial socket: %v", err)
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)
	mcpInitialize(t, conn, reader)

	resp := mcpCallTool(t, conn, reader, 2, "wormhole.agent.register", map[string]interface{}{
		"agent_id":     "agent-1",
		"capabilities": []string{"code"},
	})
	if !strings.Contains(resp.Error, "invalid private request context") {
		cancel()
		t.Fatalf("wormhole.agent.register error = %q, want binding-aware fail-closed error", resp.Error)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned error after cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not shut down after context cancel")
	}
}
