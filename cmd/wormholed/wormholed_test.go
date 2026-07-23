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
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	runtimesync "github.com/H4RL33/wormhole/internal/runtime/sync"
)

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

func configureSecurityTestDaemon(t *testing.T) (string, syncEngineFactory) {
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
		"server": "http://127.0.0.1:1", "project_id": "project-1", "agent_id": "agent-1", "token": "test-token",
	})
	if err != nil {
		t.Fatalf("marshal credentials: %v", err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "default.json"), credData, 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	events := []string{}
	factory := func(string, string, string, *runtimesync.QueueRepo, *runtimesync.AuditRepo, *localstore.TaskRepo, *localstore.KBRepo, runtimesync.Config) (syncEngine, error) {
		return &fakeGroupEngine{name: "security-test", events: &events}, nil
	}
	return filepath.Join(runDir, "wormhole", "wormholed.sock"), factory
}

func TestRun_StalePathSocketReplaced(t *testing.T) {
	socketPath, factory := configureSecurityTestDaemon(t)
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

	runner := func(ctx context.Context, profileName string) error {
		return runWithSyncEngineFactory(ctx, profileName, factory)
	}
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
	socketPath, factory := configureSecurityTestDaemon(t)
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
	go func() { errCh <- runWithSyncEngineFactory(ctx, "default", factory) }()
	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "not a socket") {
			t.Fatalf("Run error = %v, want non-socket stale-path rejection", err)
		}
	case <-time.After(time.Second):
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
			t.Fatalf("wormholed socket did not become ready at %s", socketPath)
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
		result = map[string]any{
			"org_config": map[string]any{}, "project_list": []string{},
			"task_list": []any{}, "kb_list": []any{},
			"timestamp": time.Now().UTC().Format(time.RFC3339), "version": 1,
		}
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

type fakeGroupEngine struct {
	name         string
	bootstrapErr error
	events       *[]string
	startCalls   int
	stopCalls    int
}

func (e *fakeGroupEngine) Bootstrap(context.Context) error {
	*e.events = append(*e.events, "bootstrap "+e.name)
	return e.bootstrapErr
}

func (e *fakeGroupEngine) Start(context.Context) {
	e.startCalls++
	*e.events = append(*e.events, "start "+e.name)
}

func (e *fakeGroupEngine) Stop() {
	e.stopCalls++
	*e.events = append(*e.events, "stop "+e.name)
}

func TestSyncGroupBootstrapBarrierStartsNoneOnAnyFailure(t *testing.T) {
	events := []string{}
	first := &fakeGroupEngine{name: "first", events: &events}
	second := &fakeGroupEngine{name: "second", events: &events, bootstrapErr: fmt.Errorf("bootstrap failed")}
	third := &fakeGroupEngine{name: "third", events: &events}
	group := &syncGroup{engines: []syncEngine{first, second, third}}

	err := group.Start(context.Background())
	if err == nil {
		t.Fatal("syncGroup.Start returned nil, want bootstrap error")
	}
	want := []string{"bootstrap first", "bootstrap second"}
	if fmt.Sprint(events) != fmt.Sprint(want) {
		t.Fatalf("lifecycle events = %v, want %v", events, want)
	}
	if first.startCalls != 0 || second.startCalls != 0 || third.startCalls != 0 {
		t.Fatalf("start calls = (%d, %d, %d), want no engine started", first.startCalls, second.startCalls, third.startCalls)
	}
}

func TestSyncGroupBootstrapsEveryEngineBeforeStartingAny(t *testing.T) {
	events := []string{}
	first := &fakeGroupEngine{name: "first", events: &events}
	second := &fakeGroupEngine{name: "second", events: &events}
	group := &syncGroup{engines: []syncEngine{first, second}}
	if err := group.Start(context.Background()); err != nil {
		t.Fatalf("syncGroup.Start: %v", err)
	}
	want := []string{"bootstrap first", "bootstrap second", "start first", "start second"}
	if fmt.Sprint(events) != fmt.Sprint(want) {
		t.Fatalf("lifecycle events = %v, want %v", events, want)
	}
}

type blockingBootstrapEngine struct {
	entered    chan struct{}
	startCalls atomic.Int32
	stopCalls  atomic.Int32
}

type engineLifecycleCounts struct {
	mu          stdsync.Mutex
	constructed map[string]int
	bootstraps  map[string]int
	starts      map[string]int
}

func newEngineLifecycleCounts() *engineLifecycleCounts {
	return &engineLifecycleCounts{
		constructed: make(map[string]int), bootstraps: make(map[string]int), starts: make(map[string]int),
	}
}

func (c *engineLifecycleCounts) increment(counter map[string]int, projectID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	counter[projectID]++
}

func (c *engineLifecycleCounts) project(projectID string) (constructed, bootstraps, starts int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.constructed[projectID], c.bootstraps[projectID], c.starts[projectID]
}

type countingSyncEngine struct {
	syncEngine
	projectID string
	counts    *engineLifecycleCounts
}

func (e *countingSyncEngine) Bootstrap(ctx context.Context) error {
	e.counts.increment(e.counts.bootstraps, e.projectID)
	return e.syncEngine.Bootstrap(ctx)
}

func (e *countingSyncEngine) Start(ctx context.Context) {
	e.counts.increment(e.counts.starts, e.projectID)
	e.syncEngine.Start(ctx)
}

func countingEngineFactory(counts *engineLifecycleCounts) syncEngineFactory {
	return func(server, token, projectID string, queue *runtimesync.QueueRepo, audit *runtimesync.AuditRepo, tasks *localstore.TaskRepo, articles *localstore.KBRepo, cfg runtimesync.Config) (syncEngine, error) {
		engine, err := runtimesync.New(server, token, projectID, queue, audit, tasks, articles, cfg)
		if err != nil {
			return nil, err
		}
		counts.increment(counts.constructed, projectID)
		return &countingSyncEngine{syncEngine: engine, projectID: projectID, counts: counts}, nil
	}
}

func (e *blockingBootstrapEngine) Bootstrap(ctx context.Context) error {
	close(e.entered)
	<-ctx.Done()
	return ctx.Err()
}

func (e *blockingBootstrapEngine) Start(context.Context) { e.startCalls.Add(1) }
func (e *blockingBootstrapEngine) Stop()                 { e.stopCalls.Add(1) }

func TestSyncGroupStopDuringBootstrapCancelsAndPreventsStart(t *testing.T) {
	engine := &blockingBootstrapEngine{entered: make(chan struct{})}
	group := &syncGroup{engines: []syncEngine{engine}}
	errCh := make(chan error, 1)
	go func() { errCh <- group.Start(context.Background()) }()
	<-engine.entered
	group.Stop()
	if err := <-errCh; err == nil {
		t.Fatal("Start returned nil after Stop canceled bootstrap")
	}
	if got := engine.startCalls.Load(); got != 0 {
		t.Fatalf("Start calls = %d, want 0", got)
	}
	if got := engine.stopCalls.Load(); got != 1 {
		t.Fatalf("Stop calls = %d, want 1", got)
	}
}

func TestSyncGroupStopAfterBootstrapPreventsStart(t *testing.T) {
	events := []string{}
	engine := &fakeGroupEngine{name: "only", events: &events}
	reached := make(chan struct{})
	release := make(chan struct{})
	group := &syncGroup{
		engines: []syncEngine{engine},
		testAfterBootstrap: func() {
			close(reached)
			<-release
		},
	}
	errCh := make(chan error, 1)
	go func() { errCh <- group.Start(context.Background()) }()
	<-reached
	group.Stop()
	close(release)
	if err := <-errCh; err == nil {
		t.Fatal("Start returned nil after terminal Stop")
	}
	if engine.startCalls != 0 {
		t.Fatalf("Start calls = %d, want 0", engine.startCalls)
	}

	if err := group.Start(context.Background()); err == nil {
		t.Fatal("later Start returned nil after terminal Stop")
	}
	if engine.startCalls != 0 {
		t.Fatalf("later Start calls = %d, want 0", engine.startCalls)
	}
}

func TestSyncGroupStopIsIdempotent(t *testing.T) {
	events := []string{}
	first := &fakeGroupEngine{name: "first", events: &events}
	second := &fakeGroupEngine{name: "second", events: &events}
	group := &syncGroup{engines: []syncEngine{first, second}}
	if err := group.Start(context.Background()); err != nil {
		t.Fatalf("syncGroup.Start: %v", err)
	}
	group.Stop()
	group.Stop()
	if first.stopCalls != 1 || second.stopCalls != 1 {
		t.Fatalf("stop calls = (%d, %d), want (1, 1)", first.stopCalls, second.stopCalls)
	}
}

func TestSyncGroupStartAfterSuccessfulStopReturnsTerminalErrorWithoutWork(t *testing.T) {
	events := []string{}
	engine := &fakeGroupEngine{name: "only", events: &events}
	group := &syncGroup{engines: []syncEngine{engine}}
	if err := group.Start(context.Background()); err != nil {
		t.Fatalf("initial Start: %v", err)
	}
	group.Stop()
	eventsBefore := append([]string(nil), events...)
	startCallsBefore := engine.startCalls

	if err := group.Start(context.Background()); !errors.Is(err, errSyncGroupStopped) {
		t.Fatalf("Start after Stop error = %v, want errSyncGroupStopped", err)
	}
	if engine.startCalls != startCallsBefore {
		t.Fatalf("Start calls after terminal Start = %d, want unchanged %d", engine.startCalls, startCallsBefore)
	}
	if fmt.Sprint(events) != fmt.Sprint(eventsBefore) {
		t.Fatalf("events after terminal Start = %v, want unchanged %v", events, eventsBefore)
	}
}

func TestNewMultiOrgSyncGroupValidatesBindings(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "bindings.db"))
	if err != nil {
		t.Fatalf("open local store: %v", err)
	}
	defer store.Close()
	queue := runtimesync.NewQueueRepo(store.DB())
	audit := runtimesync.NewAuditRepo(store.DB())
	taskRepo := localstore.NewTaskRepo(store.DB(), localstore.NewEventRepo(store.DB()))
	kbRepo := localstore.NewKBRepo(store.DB())
	orgs := map[string]config.Org{
		"org-a": {Name: "org-a", Credentials: config.Credentials{Server: "https://a.example", ProjectID: "project-1", Token: "token-a"}},
		"org-b": {Name: "org-b", Credentials: config.Credentials{Server: "https://b.example", ProjectID: "project-1", Token: "token-b"}},
		"alias": {Name: "alias", Credentials: config.Credentials{Server: "https://a.example", ProjectID: "project-1", Token: "token-a"}},
	}

	tests := []struct {
		name     string
		bindings []config.ProjectBinding
		wantErr  string
		wantLen  int
	}{
		{name: "missing org", bindings: []config.ProjectBinding{{ProjectID: "project-1", OrgName: "missing"}}, wantErr: "not found"},
		{name: "conflicting project", bindings: []config.ProjectBinding{{ProjectID: "project-1", OrgName: "org-a"}, {ProjectID: "project-1", OrgName: "org-b"}}, wantErr: "conflicting"},
		{name: "identical tuple deduplicated", bindings: []config.ProjectBinding{{ProjectID: "project-1", OrgName: "org-a"}, {ProjectID: "project-1", OrgName: "alias"}}, wantLen: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			group, err := newMultiOrgSyncGroup(orgs, tc.bindings, queue, audit, taskRepo, kbRepo, runtimesync.DefaultConfig(), defaultSyncEngineFactory)
			if tc.wantErr != "" {
				if err == nil || !bytes.Contains([]byte(err.Error()), []byte(tc.wantErr)) {
					t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("newMultiOrgSyncGroup: %v", err)
			}
			if len(group.engines) != tc.wantLen {
				t.Fatalf("engine count = %d, want %d", len(group.engines), tc.wantLen)
			}
		})
	}
}

// mcpInitialize sends initialize and notifications/initialized handshake.
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

	notif := mcpRpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"}
	notifRaw, _ := json.Marshal(notif)
	if _, err := conn.Write(append(notifRaw, '\n')); err != nil {
		t.Fatalf("write notifications/initialized: %v", err)
	}
}

// mcpCallTool sends a tools/call request and returns the result.
func mcpCallTool(t *testing.T, conn net.Conn, reader *bufio.Reader, id int, tool string, args map[string]interface{}) mcpToolResponse {
	t.Helper()

	var argsRaw json.RawMessage
	if args != nil {
		b, _ := json.Marshal(args)
		argsRaw = b
	} else {
		argsRaw = json.RawMessage(`{}`)
	}

	params := mcpToolsCallParams{Name: tool, Arguments: argsRaw}
	paramsRaw, _ := json.Marshal(params)
	idRaw, _ := json.Marshal(id)
	req := mcpRpcRequest{JSONRPC: "2.0", ID: idRaw, Method: "tools/call", Params: paramsRaw}
	reqRaw, _ := json.Marshal(req)
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
	if result.IsError {
		text := ""
		if len(result.Content) > 0 {
			text = result.Content[0].Text
		}
		return mcpToolResponse{Error: text}
	}
	if len(result.Content) == 0 {
		return mcpToolResponse{}
	}
	return mcpToolResponse{Result: json.RawMessage(result.Content[0].Text)}
}

func TestRun_EndToEndWhoAmI(t *testing.T) {
	coord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req mcpRpcRequest
		json.NewDecoder(r.Body).Decode(&req)
		out := map[string]any{
			"agent_id": "agent-1", "owner": "harley", "model": "claude-sonnet-5",
			"capabilities": []string{"code"}, "project_id": "project-1", "permissions": []string{"read_kb"},
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
		"server": coord.URL, "project_id": "project-1", "agent_id": "agent-1", "token": "test-token",
	})
	os.WriteFile(filepath.Join(credDir, "default.json"), credData, 0o600)

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
	if resp.Error != "" {
		cancel()
		t.Fatalf("got error: %s", resp.Error)
	}
	var out struct {
		AgentID string `json:"agent_id"`
	}
	json.Unmarshal(resp.Result, &out)
	if out.AgentID != "agent-1" {
		cancel()
		t.Fatalf("got agent_id %q, want agent-1", out.AgentID)
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
func TestRun_AgentRegisterReachesSchedulerAndEventBus(t *testing.T) {
	coord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req mcpRpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeToolRPCResponse(w, req.ID, map[string]any{
			"org_config": map[string]any{}, "project_list": []string{},
			"task_list": []any{}, "kb_list": []any{},
			"timestamp": time.Now().UTC().Format(time.RFC3339), "version": 1,
		}, "")
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
		"server": coord.URL, "project_id": "project-1", "agent_id": "agent-1", "token": "test-token",
	})
	os.WriteFile(filepath.Join(credDir, "default.json"), credData, 0o600)

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
	if resp.Error != "" {
		cancel()
		t.Fatalf("wormhole.agent.register returned error: %s", resp.Error)
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

func TestRun_MultiOrgSyncIsolation(t *testing.T) {
	const (
		projectA = "project-org-a"
		projectB = "project-org-b"
		tokenA   = "token-org-a"
		tokenB   = "token-org-b"
		titleA   = "only org A receives this task"
		titleB   = "only org B receives this task"
	)
	coordA := newIsolatedCoordServer(t, tokenA)
	coordB := newIsolatedCoordServer(t, tokenB)
	engineCounts := newEngineLifecycleCounts()
	runner := func(ctx context.Context, profileName string) error {
		return runWithSyncEngineFactory(ctx, profileName, countingEngineFactory(engineCounts))
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	runDir := filepath.Join(home, "run")
	t.Setenv("XDG_RUNTIME_DIR", runDir)
	dataDir := filepath.Join(home, "data")
	t.Setenv("XDG_DATA_HOME", dataDir)
	credDir := filepath.Join(home, ".wormhole", "credentials")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatalf("create credentials directory: %v", err)
	}
	for _, profile := range []struct {
		name, server, project, token string
	}{
		{name: "org-a", server: coordA.server.URL, project: projectA, token: tokenA},
		{name: "org-b", server: coordB.server.URL, project: projectB, token: tokenB},
	} {
		data, err := json.Marshal(map[string]string{
			"server": profile.server, "project_id": profile.project,
			"agent_id": "agent-" + profile.name, "token": profile.token,
		})
		if err != nil {
			t.Fatalf("marshal %s credentials: %v", profile.name, err)
		}
		if err := os.WriteFile(filepath.Join(credDir, profile.name+".json"), data, 0o600); err != nil {
			t.Fatalf("write %s credentials: %v", profile.name, err)
		}
	}

	socketPath := filepath.Join(runDir, "wormhole", "wormholed.sock")
	firstRun := startTestDaemonWithRunner(t, "org-a", socketPath, runner)
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial first daemon: %v", err)
	}
	reader := bufio.NewReader(conn)
	mcpInitialize(t, conn, reader)
	for i, tc := range []struct {
		project, title string
	}{{project: projectA, title: titleA}, {project: projectB, title: titleB}} {
		resp := mcpCallTool(t, conn, reader, i+2, "wormhole.task.create", map[string]interface{}{
			"project_id": tc.project, "title": tc.title, "description": "multi-org queue isolation", "priority": 0,
		})
		if resp.Error != "" {
			_ = conn.Close()
			t.Fatalf("create task for %s: %s", tc.project, resp.Error)
		}
	}
	_ = conn.Close()
	firstRun.stop(t)
	for _, projectID := range []string{projectA, projectB} {
		constructed, bootstraps, starts := engineCounts.project(projectID)
		if constructed != 1 || bootstraps != 1 || starts != 1 {
			t.Fatalf("first run %s engine counts = constructed:%d bootstrap:%d start:%d, want 1/1/1", projectID, constructed, bootstraps, starts)
		}
	}

	dbPath := filepath.Join(dataDir, "wormhole", "wormholed.db")
	inspectionStore, err := localstore.Open(dbPath)
	if err != nil {
		t.Fatalf("open persisted local store: %v", err)
	}
	defer inspectionStore.Close()
	queue := runtimesync.NewQueueRepo(inspectionStore.DB())
	if _, err := inspectionStore.DB().ExecContext(context.Background(), `UPDATE sync_queue SET priority = 2 WHERE delivered_at IS NULL`); err != nil {
		t.Fatalf("promote persisted queue rows for fast retry: %v", err)
	}
	for _, projectID := range []string{projectA, projectB} {
		pending, err := queue.ListPending(context.Background(), projectID, 10)
		if err != nil {
			t.Fatalf("list %s pending queue after restart boundary: %v", projectID, err)
		}
		if len(pending) != 1 {
			t.Fatalf("%s pending queue length = %d, want 1", projectID, len(pending))
		}
	}

	coordA.setAccept(true)
	secondRun := startTestDaemonWithRunner(t, "org-a", socketPath, runner)
	waitForCondition(t, 5*time.Second, "org A queue to drain", func() (bool, error) {
		pending, err := queue.ListPending(context.Background(), projectA, 10)
		return len(pending) == 0, err
	})
	pendingB, err := queue.ListPending(context.Background(), projectB, 10)
	if err != nil {
		t.Fatalf("list org B queue while org A drains: %v", err)
	}
	if len(pendingB) != 1 {
		t.Fatalf("org B queue length while endpoint B is offline = %d, want 1", len(pendingB))
	}

	coordB.setAccept(true)
	waitForCondition(t, 5*time.Second, "org B queue to drain independently", func() (bool, error) {
		pending, err := queue.ListPending(context.Background(), projectB, 10)
		return len(pending) == 0, err
	})
	secondRun.stop(t)
	for _, projectID := range []string{projectA, projectB} {
		constructed, bootstraps, starts := engineCounts.project(projectID)
		if constructed != 2 || bootstraps != 2 || starts != 2 {
			t.Fatalf("two runs %s engine counts = constructed:%d bootstrap:%d start:%d, want 2/2/2", projectID, constructed, bootstraps, starts)
		}
	}

	for name, snapshot := range map[string]struct {
		token   string
		project string
		title   string
		server  *isolatedCoordServer
	}{
		"org A": {token: tokenA, project: projectA, title: titleA, server: coordA},
		"org B": {token: tokenB, project: projectB, title: titleB, server: coordB},
	} {
		tokens, tools, pushes := snapshot.server.snapshot()
		if len(tokens) == 0 {
			t.Fatalf("%s endpoint received no authenticated sync calls", name)
		}
		for _, token := range tokens {
			if token != "Bearer "+snapshot.token {
				t.Errorf("%s endpoint token = %q, want its own bearer token", name, token)
			}
		}
		bootstrapCalls := 0
		for _, tool := range tools {
			if tool == "wormhole.sync.bootstrap" {
				bootstrapCalls++
			}
		}
		if bootstrapCalls != 2 {
			t.Errorf("%s bootstrap calls = %d, want exactly 2 across two daemon runs (tools=%v)", name, bootstrapCalls, tools)
		}
		if len(pushes) == 0 {
			t.Fatalf("%s accepted no pushes, tools = %v", name, tools)
		}
		for _, push := range pushes {
			if push.NamespaceID != snapshot.project || push.Title != snapshot.title {
				t.Errorf("%s accepted push = %+v, want only namespace %q title %q", name, push, snapshot.project, snapshot.title)
			}
		}
	}
}
