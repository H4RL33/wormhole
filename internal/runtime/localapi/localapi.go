// Package localapi is wormholed's local API: a Unix-domain-socket server
// coding harnesses connect to (RFC-0003 §6.1), speaking real MCP JSON-RPC
// 2.0 (initialize / notifications/initialized / tools/list / tools/call)
// over a persistent, newline-delimited-JSON connection per client. This
// replaced P1's one-shot {tool,args}->
// {result,error} bespoke protocol (localRequest/localResponse, now
// deleted) — see mcp.go for the tool registry, schema reflection, and
// per-message dispatch (dispatchMCPMessage) that implement the MCP surface
// on top of the handler methods below, which are unchanged internally.
//
// rpcRequest/rpcResponse/toolsCallParams/toolCallResult/whoAmIOutput mirror
// internal/mcp's JSON-RPC 2.0 wire shapes for talking to the Coordination
// Server (and, as of this MCP surface, for wormholed's own local socket
// too — see mcp.go). localapi cannot import internal/mcp (RFC-0003 §6.3
// keeps internal/runtime/* and internal/mcp separate trees), so the wire
// contract is duplicated here, same as cmd/wormhole already
// does for the same reason.
package localapi

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/eventbus"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/runtime/scheduler"
	syncpkg "github.com/H4RL33/wormhole/internal/runtime/sync"
)

const (
	// maxFrameBytes bounds one newline-delimited JSON-RPC message, including
	// its trailing newline. Eight bounded readers therefore retain at most
	// roughly 8 MiB for inbound frames.
	maxFrameBytes = 1 << 20
	// maxActiveConnections is intentionally small: wormholed is a per-user
	// local daemon, and eight persistent harness sessions cover normal local
	// concurrency while bounding handler and frame-buffer resources.
	maxActiveConnections = 8
	// handlerShutdownTimeout bounds graceful shutdown if a handler does not
	// observe cancellation after its connection is closed.
	handlerShutdownTimeout = time.Second
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type toolCallResultContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolCallResult struct {
	Content []toolCallResultContent `json:"content"`
	IsError bool                    `json:"isError,omitempty"`
}

type whoAmIOutput struct {
	AgentID      string   `json:"agent_id"`
	Owner        string   `json:"owner"`
	Model        string   `json:"model"`
	Capabilities []string `json:"capabilities"`
	ProjectID    string   `json:"project_id"`
	Permissions  []string `json:"permissions"`
}

// OrgContext holds org-specific routing info for a request (P5 multi-org support).
type OrgContext struct {
	OrgName   string             // which org to route to
	Creds     config.Credentials // credentials for this org
	ProjectID string             // project within this org
}

type connectionState struct {
	conn   net.Conn
	cancel context.CancelFunc
}

// Server is wormholed's local API socket server (RFC-0003 §6.1).
// P1 shipped whoami; P2 adds local-servable reads for tasks, events, and KB.
// P3 adds eventbus, scheduler, and subscription support.
// P5 adds multi-org support (RFC-0003 §7.1, §8.1).
type Server struct {
	listener   net.Listener
	socketPath string
	httpClient *http.Client

	// Single-org mode (P1-P4 backward compatibility)
	coordServer string
	token       string
	projectID   string

	// Multi-org mode (P5+)
	orgs       map[string]config.Org   // org_name → Org credentials
	bindings   []config.ProjectBinding // project_id → org_name mappings
	isMultiOrg bool                    // true if using multi-org mode

	// P2 local-read repositories (single-org mode)
	store *localstore.Store
	tr    *localstore.TaskRepo
	er    *localstore.EventRepo
	kb    *localstore.KBRepo
	qr    *syncpkg.QueueRepo

	eventbus  *eventbus.EventBus
	scheduler *scheduler.Scheduler
	closeOnce sync.Once
	closeErr  error
	shutdown  atomic.Bool
	handlers  chan struct{}
	// admissionMu makes handler registration atomic with shutdown. handlerWG
	// is only incremented while admissionMu is held and shutdown is false.
	admissionMu sync.Mutex
	handlerWG   sync.WaitGroup

	// conns tracks open connections for force-close on shutdown (issue #20).
	// Connections and their contexts are registered before their handler
	// goroutine starts.
	conns sync.Map // map[net.Conn]*connectionState
	// authorizationAgents binds each project to the agent id in the active
	// credential profile, preventing stale cache rows from authorizing it.
	authorizationAgents sync.Map // map[projectID]agentID

	// testBeforeHandlerStart is a deterministic test barrier between admission
	// and handler execution. Production servers leave it nil.
	testBeforeHandlerStart func()
	// testBeforeLocalWriteCommit injects a pre-commit abort for atomic-write
	// rollback tests. It does not claim to simulate a storage-engine commit failure.
	testBeforeLocalWriteCommit func(*sql.Tx) error

	// registry is the local MCP tool registry (mcp.go), built once at
	// construction time from the Server that will service every
	// connection's tools/call dispatch (design doc §5 subtask 2).
	registry *localRegistry
}

// New binds the Unix domain socket at socketPath. Callers must call Serve
// to start accepting connections, and Close to release the socket.
// Single-org mode (P1-P4).
func New(socketPath, coordServerURL, token, projectID string, store *localstore.Store, tr *localstore.TaskRepo, er *localstore.EventRepo, kb *localstore.KBRepo, qr *syncpkg.QueueRepo) (*Server, error) {
	ln, err := listenLocalSocket(socketPath)
	if err != nil {
		return nil, fmt.Errorf("localapi: listen on %s: %w", socketPath, err)
	}
	srv := &Server{
		listener:    ln,
		socketPath:  socketPath,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
		coordServer: coordServerURL,
		token:       token,
		projectID:   projectID,
		isMultiOrg:  false,
		store:       store,
		tr:          tr,
		er:          er,
		kb:          kb,
		qr:          qr,
		handlers:    make(chan struct{}, maxActiveConnections),
	}
	srv.registry = newLocalRegistry(srv)
	return srv, nil
}

// NewWithRuntime binds the Unix domain socket at socketPath and wires eventbus
// + scheduler (P3). Callers must call Serve to start accepting connections,
// and Close to release the socket.
// The socket is restricted to the owning user immediately after listen.
func NewWithRuntime(socketPath, coordServerURL, token, projectID string, store *localstore.Store, tr *localstore.TaskRepo, er *localstore.EventRepo, kb *localstore.KBRepo, eb *eventbus.EventBus, sched *scheduler.Scheduler, qr *syncpkg.QueueRepo) (*Server, error) {
	ln, err := listenLocalSocket(socketPath)
	if err != nil {
		return nil, fmt.Errorf("localapi: listen on %s: %w", socketPath, err)
	}
	srv := &Server{
		listener:    ln,
		socketPath:  socketPath,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
		coordServer: coordServerURL,
		token:       token,
		projectID:   projectID,
		isMultiOrg:  false,
		store:       store,
		tr:          tr,
		er:          er,
		kb:          kb,
		qr:          qr,
		eventbus:    eb,
		scheduler:   sched,
		handlers:    make(chan struct{}, maxActiveConnections),
	}
	srv.registry = newLocalRegistry(srv)
	return srv, nil
}

// NewMultiOrg binds the Unix domain socket and configures multi-org support (P5+, RFC-0003 §7.1).
// Orgs is a map of org_name → Org credentials. Bindings map project contexts to org names.
// Callers must call Serve to start accepting connections, and Close to release the socket.
// The socket is restricted to the owning user immediately after listen.
func NewMultiOrg(socketPath string, orgs map[string]config.Org, bindings []config.ProjectBinding, store *localstore.Store, tr *localstore.TaskRepo, er *localstore.EventRepo, kb *localstore.KBRepo, eb *eventbus.EventBus, sched *scheduler.Scheduler, qr *syncpkg.QueueRepo) (*Server, error) {
	if len(orgs) == 0 {
		return nil, fmt.Errorf("localapi: NewMultiOrg: no orgs provided")
	}
	ln, err := listenLocalSocket(socketPath)
	if err != nil {
		return nil, fmt.Errorf("localapi: listen on %s: %w", socketPath, err)
	}
	srv := &Server{
		listener:   ln,
		socketPath: socketPath,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		orgs:       orgs,
		bindings:   bindings,
		isMultiOrg: true,
		store:      store,
		tr:         tr,
		er:         er,
		kb:         kb,
		qr:         qr,
		eventbus:   eb,
		scheduler:  sched,
		handlers:   make(chan struct{}, maxActiveConnections),
	}
	srv.registry = newLocalRegistry(srv)
	return srv, nil
}

func listenLocalSocket(socketPath string) (net.Listener, error) {
	return listenLocalSocketWithChmod(socketPath, os.Chmod)
}

func listenLocalSocketWithChmod(socketPath string, chmod func(string, os.FileMode) error) (net.Listener, error) {
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	if err := chmod(socketPath, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("chmod socket %s: %w", socketPath, err)
	}
	return ln, nil
}

func (s *Server) logError(operation string, err error) {
	if err == nil {
		return
	}
	message := err.Error()
	secretSet := make(map[string]struct{}, len(s.orgs)+1)
	if s.token != "" {
		secretSet[s.token] = struct{}{}
	}
	for _, org := range s.orgs {
		if org.Credentials.Token != "" {
			secretSet[org.Credentials.Token] = struct{}{}
		}
	}
	secrets := make([]string, 0, len(secretSet))
	for secret := range secretSet {
		secrets = append(secrets, secret)
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		message = strings.ReplaceAll(message, secret, "[REDACTED]")
	}
	log.Printf("localapi: %s: %s", operation, message)
}

// resolveOrgContext returns the org/creds/projectID for a request (P5 multi-org).
// If single-org mode, always returns the configured org.
// If multi-org mode, looks up the project in bindings and returns corresponding org.
// RFC-0003 §7.1: project bindings are explicit, no implicit default.
func (s *Server) resolveOrgContext(projectID string) (OrgContext, error) {
	if !s.isMultiOrg {
		// Single-org mode: use configured credentials
		return OrgContext{
			OrgName:   "default",
			Creds:     config.Credentials{Server: s.coordServer, Token: s.token},
			ProjectID: s.projectID,
		}, nil
	}

	// Multi-org mode: look up binding for this project
	for _, binding := range s.bindings {
		if binding.ProjectID == projectID {
			org, ok := s.orgs[binding.OrgName]
			if !ok {
				return OrgContext{}, fmt.Errorf("localapi: org %q for project %q not found", binding.OrgName, projectID)
			}
			return OrgContext{
				OrgName:   binding.OrgName,
				Creds:     org.Credentials,
				ProjectID: projectID,
			}, nil
		}
	}

	// No binding found: RFC-0003 §7.1 requires explicit bindings, no implicit default
	return OrgContext{}, fmt.Errorf("localapi: no project binding for %q — RFC-0003 §7.1 requires explicit project bindings, no implicit default", projectID)
}

// Close stops accepting connections and releases the socket. Safe to call
// multiple times, and safe to call independently of ctx cancellation (i.e.
// without ever cancelling the ctx passed to Serve): either path marks
// shutdown as intentional so Serve returns nil instead of an accept error.
// Forces all open connections to close (issue #20).
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.admissionMu.Lock()
		s.shutdown.Store(true)
		s.closeErr = s.listener.Close()

		// Force-close all tracked open connections to prevent handle goroutines
		// from leaking on shutdown. Iterate conns and close each one (issue #20).
		s.conns.Range(func(_, value interface{}) bool {
			if state, ok := value.(*connectionState); ok {
				state.cancel()
				_ = state.conn.Close()
			}
			return true
		})
		s.admissionMu.Unlock()

		handlersDone := make(chan struct{})
		go func() {
			s.handlerWG.Wait()
			close(handlersDone)
		}()
		select {
		case <-handlersDone:
		case <-time.After(handlerShutdownTimeout):
			s.closeErr = errors.Join(s.closeErr, fmt.Errorf("localapi: timed out waiting for handlers to stop"))
		}
	})
	return s.closeErr
}

// Serve accepts connections until ctx is cancelled or the listener closes.
func (s *Server) Serve(ctx context.Context) error {
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			s.Close()
		case <-done:
		}
	}()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if ctx.Err() != nil || s.shutdown.Load() {
				if closeErr := s.Close(); closeErr != nil {
					return closeErr
				}
				return nil
			}
			return fmt.Errorf("localapi: accept: %w", err)
		}
		select {
		case s.handlers <- struct{}{}:
			s.admissionMu.Lock()
			if s.shutdown.Load() {
				s.admissionMu.Unlock()
				<-s.handlers
				_ = conn.Close()
				continue
			}
			handlerCtx, cancelHandler := context.WithCancel(ctx)
			state := &connectionState{conn: conn, cancel: cancelHandler}
			s.conns.Store(conn, state)
			s.handlerWG.Add(1)
			s.admissionMu.Unlock()
			go func(state *connectionState, handlerCtx context.Context) {
				defer func() { <-s.handlers }()
				defer s.handlerWG.Done()
				defer func() {
					state.cancel()
					s.conns.Delete(state.conn)
					_ = state.conn.Close()
				}()
				if s.testBeforeHandlerStart != nil {
					s.testBeforeHandlerStart()
				}
				s.handle(handlerCtx, state.conn)
			}(state, handlerCtx)
		default:
			_ = conn.Close()
		}
	}
}

// handle services one connection as a persistent MCP JSON-RPC 2.0 session
// (design doc §2, §5 subtask 2): initialize -> notifications/initialized ->
// N x tools/list/tools/call, all on the same connection, until the client
// disconnects. This replaces the old one-shot ReadBytes-once/dispatch-once/
// close shape; mcpSession carries the per-connection lifecycle state
// (initialized) and write serialization (writeMu) that one-shot dispatch
// never needed.
func (s *Server) handle(ctx context.Context, conn net.Conn) {
	sess := &mcpSession{}
	reader := bufio.NewReaderSize(conn, maxFrameBytes)
	for {
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) || len(line) > maxFrameBytes {
			writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: rpcParseError, Message: "frame exceeds maximum size"}})
			return
		}
		if err != nil {
			if len(bytes.TrimSpace(line)) > 0 {
				writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: rpcParseError, Message: "frame must end with a newline"}})
			}
			return
		}
		if len(bytes.TrimSpace(line)) > 0 {
			req, decodeErr := decodeMCPLine(line)
			if decodeErr != nil {
				s.logError("decode JSON-RPC frame", decodeErr)
				writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: rpcParseError, Message: fmt.Sprintf("parse error: %v", decodeErr)}})
			} else {
				s.dispatchMCPMessage(ctx, sess, conn, s.registry, req)
			}
		}
	}
}

// isJoinRegisterArgs reports whether a wormhole.agent.register call's args
// are the join/passport-creation shape (RFC-0001 §9, cmd/wormhole's
// registerAgentInput: owner/model/capabilities/roles/permissions, no
// agent_id) rather than P3's local presence-registration shape (agent_id +
// capabilities). See the switch case in handle for why this dispatches on
// shape instead of a second tool name.
func isJoinRegisterArgs(args json.RawMessage) bool {
	var argMap map[string]interface{}
	if len(args) == 0 {
		return false
	}
	if err := json.Unmarshal(args, &argMap); err != nil {
		return false
	}
	_, hasAgentID := argMap["agent_id"]
	return !hasAgentID
}

// proxyRegister forwards a join-shaped wormhole.agent.register call to the
// Coordination Server, unauthenticated (matching cmd/wormhole's
// doRegister, which sends no bearer token for this call — a Passport
// doesn't exist yet). project_id is expected to already be present in args
// (cmd/wormhole's callTool folds it in before sending), so this simply
// forwards the args as given; no local caching, matching this call's write
// (not cacheable read) semantics.
func (s *Server) proxyRegister(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var argMap map[string]interface{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argMap); err != nil {
			return nil, fmt.Errorf("localapi: agent register: invalid args: %w", err)
		}
	}
	projectID, _ := argMap["project_id"].(string)

	orgCtx, err := s.resolveOrgContext(projectID)
	if err != nil {
		return nil, err
	}

	paramsRaw, err := json.Marshal(toolsCallParams{Name: "wormhole.agent.register", Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("localapi: marshal params: %w", err)
	}
	reqBody, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call", Params: paramsRaw})
	if err != nil {
		return nil, fmt.Errorf("localapi: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(orgCtx.Creds.Server, "/")+"/mcp", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("localapi: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("localapi: call coordination server: %w", err)
	}
	defer resp.Body.Close()

	var rpcResp rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("localapi: decode coordination server response: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, errors.New(rpcResp.Error.Message)
	}

	var result toolCallResult
	if err := json.Unmarshal(rpcResp.Result, &result); err != nil {
		return nil, fmt.Errorf("localapi: decode tools/call result: %w", err)
	}
	if len(result.Content) == 0 {
		return nil, errors.New("localapi: empty register result from coordination server")
	}
	if result.IsError {
		return nil, errors.New(result.Content[0].Text)
	}

	return json.RawMessage(result.Content[0].Text), nil
}

// proxyWhoAmI forwards wormhole.agent.whoami to the Coordination Server
// over its existing /mcp JSON-RPC 2.0 endpoint, then caches the result
// locally on success (RFC-0003 G4: local durability, best-effort here —
// a cache-write failure does not fail the caller's request).
func (s *Server) proxyWhoAmI(ctx context.Context) (whoAmIOutput, error) {
	orgCtx, err := s.resolveOrgContext(s.projectID)
	if err != nil {
		return whoAmIOutput{}, err
	}
	return s.fetchAndCacheWhoAmI(ctx, orgCtx)
}

func (s *Server) fetchAndCacheWhoAmI(ctx context.Context, orgCtx OrgContext) (whoAmIOutput, error) {
	argsRaw, _ := json.Marshal(map[string]string{"project_id": orgCtx.ProjectID})
	paramsRaw, err := json.Marshal(toolsCallParams{Name: "wormhole.agent.whoami", Arguments: argsRaw})
	if err != nil {
		return whoAmIOutput{}, fmt.Errorf("localapi: marshal params: %w", err)
	}
	reqBody, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call", Params: paramsRaw})
	if err != nil {
		return whoAmIOutput{}, fmt.Errorf("localapi: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(orgCtx.Creds.Server, "/")+"/mcp", bytes.NewReader(reqBody))
	if err != nil {
		return whoAmIOutput{}, fmt.Errorf("localapi: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+orgCtx.Creds.Token)

	resp, err := s.httpClient.Do(httpReq)
	if err != nil {
		return whoAmIOutput{}, fmt.Errorf("localapi: call coordination server: %w", err)
	}
	defer resp.Body.Close()

	var rpcResp rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return whoAmIOutput{}, fmt.Errorf("localapi: decode coordination server response: %w", err)
	}
	if rpcResp.Error != nil {
		return whoAmIOutput{}, errors.New(rpcResp.Error.Message)
	}

	var result toolCallResult
	if err := json.Unmarshal(rpcResp.Result, &result); err != nil {
		return whoAmIOutput{}, fmt.Errorf("localapi: decode tools/call result: %w", err)
	}
	if len(result.Content) == 0 {
		return whoAmIOutput{}, errors.New("localapi: empty whoami result from coordination server")
	}
	if result.IsError {
		return whoAmIOutput{}, errors.New(result.Content[0].Text)
	}

	var out whoAmIOutput
	if err := json.Unmarshal([]byte(result.Content[0].Text), &out); err != nil {
		return whoAmIOutput{}, fmt.Errorf("localapi: decode whoami output: %w", err)
	}

	cacheErr := s.store.CacheWhoAmI(ctx, localstore.WhoAmICache{
		AgentID:      out.AgentID,
		Owner:        out.Owner,
		Model:        out.Model,
		Capabilities: out.Capabilities,
		ProjectID:    out.ProjectID,
		Permissions:  out.Permissions,
		CachedAt:     time.Now().UTC(),
	})
	_ = cacheErr // best-effort: cache-write failure must not fail the caller's request (P1 scope)
	s.authorizationAgents.Store(out.ProjectID, out.AgentID)

	return out, nil
}

// SetAuthorizationAgent records the credential identity expected for a
// project before the first local tools/call is served.
func (s *Server) SetAuthorizationAgent(projectID, agentID string) {
	if projectID != "" && agentID != "" {
		s.authorizationAgents.Store(projectID, agentID)
	}
}

// WarmAuthorizationScopes refreshes the offline authorization cache for each
// configured project while the daemon is already online for sync bootstrap.
// Individual failures are returned after all bindings have been attempted;
// callers may continue serving, in which case uncached privileged calls fail
// closed at the local MCP boundary.
func (s *Server) WarmAuthorizationScopes(ctx context.Context) error {
	if !s.isMultiOrg {
		_, err := s.fetchAndCacheWhoAmI(ctx, OrgContext{OrgName: "default", Creds: config.Credentials{Server: s.coordServer, Token: s.token}, ProjectID: s.projectID})
		return err
	}
	var failures []string
	for _, binding := range s.bindings {
		orgCtx, err := s.resolveOrgContext(binding.ProjectID)
		if err == nil {
			_, err = s.fetchAndCacheWhoAmI(ctx, orgCtx)
		}
		if err != nil {
			failures = append(failures, binding.ProjectID+": "+err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("localapi: warm authorization scopes: %s", strings.Join(failures, "; "))
	}
	return nil
}

// authorizeLocalTool enforces the same per-action permission contract as the
// Coordination Server before a local MCP handler can read or mutate replica
// state. The daemon uses the last authenticated whoami scope cached for the
// requested project, which keeps already-enrolled agents functional offline;
// incremental_push independently rechecks every queued item server-side.
func (s *Server) authorizeLocalTool(ctx context.Context, tool localTool, args json.RawMessage) error {
	return s.authorizeLocalPermission(ctx, tool.RequiredPermission, args)
}

// authorizeLocalPermission checks one action against the exact cached
// agent-and-project scope selected by the request. Handlers that perform more
// than their registered primary action use this for their additional gates.
func (s *Server) authorizeLocalPermission(ctx context.Context, requiredPermission string, args json.RawMessage) error {
	if requiredPermission == "" {
		return nil
	}
	projectID := s.projectID
	var argMap map[string]interface{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argMap); err != nil {
			return fmt.Errorf("localapi: authorize %s: invalid args: %w", requiredPermission, err)
		}
		if supplied, ok := argMap["project_id"].(string); ok && supplied != "" {
			projectID = supplied
		}
	}
	orgCtx, err := s.resolveOrgContext(projectID)
	if err != nil {
		return err
	}
	var cached localstore.WhoAmICache
	if expectedAgent, ok := s.authorizationAgents.Load(orgCtx.ProjectID); ok {
		cached, err = s.store.GetCachedWhoAmIForAgentProject(ctx, expectedAgent.(string), orgCtx.ProjectID)
	} else {
		// Direct embedded users of localapi that do not configure credentials
		// retain the project-only lookup; production wormholed always sets the
		// exact credential identity before Serve.
		cached, err = s.store.GetCachedWhoAmIForProject(ctx, orgCtx.ProjectID)
	}
	if err != nil {
		if errors.Is(err, localstore.ErrNotFound) {
			return fmt.Errorf("permission denied: no authenticated scope cached for project %s; call wormhole.agent.whoami while online", orgCtx.ProjectID)
		}
		return fmt.Errorf("localapi: authorize %s: %w", requiredPermission, err)
	}
	for _, permission := range cached.Permissions {
		if permission == requiredPermission {
			return nil
		}
	}
	return fmt.Errorf("permission denied: requires %s", requiredPermission)
}

// localListTasks serves wormhole.task.list from the local SQLite replica.
// Args: {"status": "wip" (optional), "project_id": "xxx" (optional in single-org, required in multi-org)}.
func (s *Server) localListTasks(ctx context.Context, args json.RawMessage) (map[string]interface{}, error) {
	var argMap map[string]interface{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argMap); err != nil {
			return nil, fmt.Errorf("localapi: list tasks: invalid args: %w", err)
		}
	}

	// Extract project_id and resolve org context (multi-org aware)
	projectID := s.projectID // fallback to configured project in single-org mode
	if projectIDVal, ok := argMap["project_id"].(string); ok && projectIDVal != "" {
		projectID = projectIDVal
	}

	orgCtx, err := s.resolveOrgContext(projectID)
	if err != nil {
		return nil, err
	}

	status := (*string)(nil)
	if statusVal, ok := argMap["status"].(string); ok && statusVal != "" {
		status = &statusVal
	}

	tasks, err := s.tr.ListTasks(ctx, orgCtx.ProjectID, status)
	if err != nil {
		return nil, fmt.Errorf("localapi: list tasks: %w", err)
	}

	out := make([]interface{}, len(tasks))
	for i, t := range tasks {
		out[i] = map[string]interface{}{
			"id":             t.ID,
			"title":          t.Title,
			"description":    t.Description,
			"status":         t.Status,
			"priority":       t.Priority,
			"owner_agent_id": t.OwnerAgentID,
			"parent_task_id": t.ParentTaskID,
			"due_by":         t.DueBy,
			"created_at":     t.CreatedAt,
			"updated_at":     t.UpdatedAt,
		}
	}
	return map[string]interface{}{"tasks": out}, nil
}

// localGetTask serves wormhole.task.get from the local SQLite replica.
// Args: {"task_id": "xxx", "project_id": "yyy" (optional in single-org, required in multi-org)}.
func (s *Server) localGetTask(ctx context.Context, args json.RawMessage) (map[string]interface{}, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("localapi: get task: missing task_id argument")
	}
	var argMap map[string]interface{}
	if err := json.Unmarshal(args, &argMap); err != nil {
		return nil, fmt.Errorf("localapi: get task: invalid args: %w", err)
	}

	taskID, ok := argMap["task_id"].(string)
	if !ok || taskID == "" {
		return nil, fmt.Errorf("localapi: get task: missing task_id argument")
	}

	// Extract project_id and resolve org context (multi-org aware)
	projectID := s.projectID // fallback to configured project in single-org mode
	if projectIDVal, ok := argMap["project_id"].(string); ok && projectIDVal != "" {
		projectID = projectIDVal
	}

	orgCtx, err := s.resolveOrgContext(projectID)
	if err != nil {
		return nil, err
	}

	t, err := s.tr.GetTask(ctx, orgCtx.ProjectID, taskID)
	if errors.Is(err, localstore.ErrTaskNotFound) {
		return nil, fmt.Errorf("localapi: task not found")
	}
	if err != nil {
		return nil, fmt.Errorf("localapi: get task: %w", err)
	}

	return map[string]interface{}{
		"id":             t.ID,
		"title":          t.Title,
		"description":    t.Description,
		"status":         t.Status,
		"priority":       t.Priority,
		"owner_agent_id": t.OwnerAgentID,
		"parent_task_id": t.ParentTaskID,
		"due_by":         t.DueBy,
		"created_at":     t.CreatedAt,
		"updated_at":     t.UpdatedAt,
	}, nil
}

// localListChannels serves wormhole.channel.list from the local SQLite replica.
// Args: {"project_id": "xxx" (optional in single-org, required in multi-org)}.
func (s *Server) localListChannels(ctx context.Context, args json.RawMessage) (map[string]interface{}, error) {
	var argMap map[string]interface{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argMap); err != nil {
			return nil, fmt.Errorf("localapi: list channels: invalid args: %w", err)
		}
	}

	// Extract project_id and resolve org context (multi-org aware)
	projectID := s.projectID // fallback to configured project in single-org mode
	if projectIDVal, ok := argMap["project_id"].(string); ok && projectIDVal != "" {
		projectID = projectIDVal
	}

	orgCtx, err := s.resolveOrgContext(projectID)
	if err != nil {
		return nil, err
	}

	channels, err := s.er.ListChannels(ctx, orgCtx.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("localapi: list channels: %w", err)
	}

	out := make([]interface{}, len(channels))
	for i, ch := range channels {
		out[i] = map[string]interface{}{
			"id":   ch.ID,
			"name": ch.Name,
		}
	}
	return map[string]interface{}{"channels": out}, nil
}

// localListChannelEvents serves wormhole.channel.events from the local SQLite replica.
// Args: {"project_id": "xxx" (optional in single-org, required in multi-org)}.
func (s *Server) localListChannelEvents(ctx context.Context, args json.RawMessage) (map[string]interface{}, error) {
	var argMap map[string]interface{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argMap); err != nil {
			return nil, fmt.Errorf("localapi: list channel events: invalid args: %w", err)
		}
	}

	// Extract project_id and resolve org context (multi-org aware)
	projectID := s.projectID // fallback to configured project in single-org mode
	if projectIDVal, ok := argMap["project_id"].(string); ok && projectIDVal != "" {
		projectID = projectIDVal
	}

	orgCtx, err := s.resolveOrgContext(projectID)
	if err != nil {
		return nil, err
	}

	events, err := s.er.ListEventsByNamespace(ctx, orgCtx.ProjectID, 50, 0)
	if err != nil {
		return nil, fmt.Errorf("localapi: list channel events: %w", err)
	}

	out := make([]interface{}, len(events))
	for i, ev := range events {
		out[i] = map[string]interface{}{
			"id":         ev.ID,
			"channel_id": ev.ChannelID,
			"agent_id":   ev.AgentID,
			"event_type": ev.EventType,
			"payload":    json.RawMessage(ev.Payload),
			"note":       ev.Note,
			"created_at": ev.CreatedAt,
		}
	}
	return map[string]interface{}{"events": out}, nil
}

// localListArticles serves wormhole.kb.list from the local SQLite replica.
// Args: {"project_id": "xxx" (optional in single-org, required in multi-org)}.
func (s *Server) localListArticles(ctx context.Context, args json.RawMessage) (map[string]interface{}, error) {
	var argMap map[string]interface{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argMap); err != nil {
			return nil, fmt.Errorf("localapi: list articles: invalid args: %w", err)
		}
	}

	// Extract project_id and resolve org context (multi-org aware)
	projectID := s.projectID // fallback to configured project in single-org mode
	if projectIDVal, ok := argMap["project_id"].(string); ok && projectIDVal != "" {
		projectID = projectIDVal
	}

	orgCtx, err := s.resolveOrgContext(projectID)
	if err != nil {
		return nil, err
	}

	articles, err := s.kb.ListArticles(ctx, orgCtx.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("localapi: list articles: %w", err)
	}

	out := make([]interface{}, len(articles))
	for i, a := range articles {
		out[i] = map[string]interface{}{
			"id":              a.ID,
			"title":           a.Title,
			"body":            a.Body,
			"frontmatter":     json.RawMessage(a.Frontmatter),
			"author_agent_id": a.AuthorAgentID,
			"created_at":      a.CreatedAt,
			"updated_at":      a.UpdatedAt,
		}
	}
	return map[string]interface{}{"articles": out}, nil
}

// localGetArticle serves wormhole.kb.get from the local SQLite replica.
// Args: {"article_id": "xxx" (optional), "project_id": "yyy" (optional in single-org, required in multi-org)}.
// If article_id omitted returns all articles.
func (s *Server) localGetArticle(ctx context.Context, args json.RawMessage) (map[string]interface{}, error) {
	var argMap map[string]interface{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argMap); err != nil {
			return nil, fmt.Errorf("localapi: get article: invalid args: %w", err)
		}
	}

	// Extract project_id and resolve org context (multi-org aware)
	projectID := s.projectID // fallback to configured project in single-org mode
	if projectIDVal, ok := argMap["project_id"].(string); ok && projectIDVal != "" {
		projectID = projectIDVal
	}

	orgCtx, err := s.resolveOrgContext(projectID)
	if err != nil {
		return nil, err
	}

	articleID, _ := argMap["article_id"].(string)
	if articleID == "" {
		// fallback: list all articles in this project
		return s.localListArticles(ctx, args)
	}

	a, err := s.kb.GetArticle(ctx, orgCtx.ProjectID, articleID)
	if errors.Is(err, localstore.ErrArticleNotFound) {
		return nil, fmt.Errorf("localapi: article not found")
	}
	if err != nil {
		return nil, fmt.Errorf("localapi: get article: %w", err)
	}

	return map[string]interface{}{
		"id":              a.ID,
		"title":           a.Title,
		"body":            a.Body,
		"frontmatter":     json.RawMessage(a.Frontmatter),
		"author_agent_id": a.AuthorAgentID,
		"created_at":      a.CreatedAt,
		"updated_at":      a.UpdatedAt,
	}, nil
}

// =============================================================================
// P3 tools — agent registration, presence, listing, task routing, subscriptions
// =============================================================================

func (s *Server) beginLocalWrite(ctx context.Context) (*sql.Tx, error) {
	if s.store == nil {
		return nil, fmt.Errorf("localapi: local store not available")
	}
	return s.store.DB().BeginTx(ctx, nil)
}

func (s *Server) commitLocalWrite(tx *sql.Tx) error {
	if s.testBeforeLocalWriteCommit != nil {
		if err := s.testBeforeLocalWriteCommit(tx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// handleAgentRegister registers an agent with the scheduler and eventbus.
// Args: {"agent_id": "x", "capabilities": ["code", "review"], "project_id": "xxx" (optional in single-org, required in multi-org)}
func (s *Server) handleAgentRegister(ctx context.Context, args json.RawMessage) (map[string]interface{}, error) {
	if s.scheduler == nil {
		return nil, fmt.Errorf("localapi: agent register: scheduler not available")
	}

	var argMap map[string]interface{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argMap); err != nil {
			return nil, fmt.Errorf("localapi: agent register: invalid args: %w", err)
		}
	}

	agentID, _ := argMap["agent_id"].(string)
	if agentID == "" {
		return nil, fmt.Errorf("localapi: agent register: missing agent_id")
	}

	// Extract project_id and resolve org context (multi-org aware)
	projectID := s.projectID // fallback to configured project in single-org mode
	if projectIDVal, ok := argMap["project_id"].(string); ok && projectIDVal != "" {
		projectID = projectIDVal
	}

	orgCtx, err := s.resolveOrgContext(projectID)
	if err != nil {
		return nil, err
	}

	caps := []string{}
	if rawCaps, ok := argMap["capabilities"]; ok {
		if capsList, ok := rawCaps.([]interface{}); ok {
			for _, c := range capsList {
				if cs, ok := c.(string); ok {
					caps = append(caps, cs)
				}
			}
		}
	}

	agent, err := s.scheduler.RegisterAgent(agentID, orgCtx.ProjectID, caps)
	if err != nil {
		return nil, fmt.Errorf("localapi: agent register: %w", err)
	}

	// Publish presence event to the eventbus. Scoped by namespace, event type,
	// and agent id (Finding 5); no single capability applies since an agent
	// can register with several.
	payload, _ := json.Marshal(map[string]interface{}{
		"agent":        agent.AgentID,
		"status":       string(scheduler.StatusOnline),
		"namespace":    orgCtx.ProjectID,
		"capabilities": agent.Capabilities,
	})
	if s.eventbus != nil {
		s.eventbus.Publish(ctx, orgCtx.ProjectID, "presence.online", "", agent.AgentID, payload)
	}

	return map[string]interface{}{
		"agent_id":     agent.AgentID,
		"namespace_id": agent.NamespaceID,
		"capabilities": agent.Capabilities,
		"status":       string(agent.Status),
	}, nil
}

// handleAgentPresence updates an agent's presence status.
// Args: {"agent_id": "x", "status": "busy", "project_id": "xxx" (optional in single-org, required in multi-org)}
func (s *Server) handleAgentPresence(ctx context.Context, args json.RawMessage) (map[string]interface{}, error) {
	if s.scheduler == nil {
		return nil, fmt.Errorf("localapi: agent presence: scheduler not available")
	}

	var argMap map[string]interface{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argMap); err != nil {
			return nil, fmt.Errorf("localapi: agent presence: invalid args: %w", err)
		}
	}

	agentID, _ := argMap["agent_id"].(string)
	statusStr, _ := argMap["status"].(string)
	if agentID == "" || statusStr == "" {
		return nil, fmt.Errorf("localapi: agent presence: missing agent_id or status")
	}

	// Extract project_id and resolve org context (multi-org aware)
	projectID := s.projectID // fallback to configured project in single-org mode
	if projectIDVal, ok := argMap["project_id"].(string); ok && projectIDVal != "" {
		projectID = projectIDVal
	}

	orgCtx, err := s.resolveOrgContext(projectID)
	if err != nil {
		return nil, err
	}

	err = s.scheduler.UpdatePresence(agentID, scheduler.AgentStatus(statusStr))
	if err != nil {
		return nil, fmt.Errorf("localapi: agent presence: %w", err)
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"agent":  agentID,
		"status": statusStr,
	})
	if s.eventbus != nil {
		s.eventbus.Publish(ctx, orgCtx.ProjectID, "presence."+statusStr, "", agentID, payload)
	}

	return map[string]interface{}{
		"agent_id": agentID,
		"status":   statusStr,
	}, nil
}

// handleAgentList returns all registered agents.
// Args: {"project_id": "xxx" (optional in single-org, required in multi-org)} — filters agents in this project.
func (s *Server) handleAgentList(ctx context.Context, args json.RawMessage) (map[string]interface{}, error) {
	if s.scheduler == nil {
		return nil, fmt.Errorf("localapi: agent list: scheduler not available")
	}

	var argMap map[string]interface{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argMap); err != nil {
			return nil, fmt.Errorf("localapi: agent list: invalid args: %w", err)
		}
	}

	// Extract project_id and resolve org context (multi-org aware)
	projectID := s.projectID // fallback to configured project in single-org mode
	if projectIDVal, ok := argMap["project_id"].(string); ok && projectIDVal != "" {
		projectID = projectIDVal
	}

	orgCtx, err := s.resolveOrgContext(projectID)
	if err != nil {
		return nil, err
	}

	agents := s.scheduler.ListAgents()
	// Filter agents to this project only
	var filtered []interface{}
	for _, a := range agents {
		if a.NamespaceID == orgCtx.ProjectID {
			filtered = append(filtered, map[string]interface{}{
				"agent_id":     a.AgentID,
				"namespace_id": a.NamespaceID,
				"capabilities": a.Capabilities,
				"status":       string(a.Status),
			})
		}
	}
	return map[string]interface{}{"agents": filtered}, nil
}

// handleTaskRoute creates a task in localstore (the one true task ID and
// RFC-0001 §8.2 status, retrievable via wormhole.task.get/list) and routes it
// to a locally-registered agent via the scheduler's capability matching. The
// routing decision is recorded as an ownership change (TaskRepo.Assign), not
// a status transition (Findings 1/2).
// Args: {"capability": "code", "title": "x", "description": "y", "project_id": "xxx" (optional in single-org, required in multi-org)}
func (s *Server) handleTaskRoute(ctx context.Context, args json.RawMessage) (map[string]interface{}, error) {
	if s.scheduler == nil {
		return nil, fmt.Errorf("localapi: task route: scheduler not available")
	}
	if s.qr == nil {
		return nil, fmt.Errorf("localapi: task route: sync queue not available")
	}

	var argMap map[string]interface{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argMap); err != nil {
			return nil, fmt.Errorf("localapi: task route: invalid args: %w", err)
		}
	}

	capability, _ := argMap["capability"].(string)
	title, _ := argMap["title"].(string)
	desc, _ := argMap["description"].(string)
	if capability == "" {
		return nil, fmt.Errorf("localapi: task route: missing capability")
	}

	// Extract project_id and resolve org context (multi-org aware)
	projectID := s.projectID // fallback to configured project in single-org mode
	if projectIDVal, ok := argMap["project_id"].(string); ok && projectIDVal != "" {
		projectID = projectIDVal
	}

	orgCtx, err := s.resolveOrgContext(projectID)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeLocalPermission(ctx, "task.assign", args); err != nil {
		return nil, err
	}

	tx, err := s.beginLocalWrite(ctx)
	if err != nil {
		return nil, fmt.Errorf("localapi: task route: begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Create the task inside the same transaction as ownership and enqueue.
	// Its UUID remains the one true ID used by both scheduler and sync queue.
	createdTask, err := s.tr.CreateTaskTx(ctx, tx, orgCtx.ProjectID, title, desc, nil, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("localapi: task route: create: %w", err)
	}

	// The scheduler is used purely for capability matching / agent selection,
	// keyed by the localstore-generated task ID — it does not mint its own ID
	// or track a competing status (Finding 2).
	if _, err := s.scheduler.RegisterTask(orgCtx.ProjectID, capability, createdTask.ID); err != nil {
		return nil, fmt.Errorf("localapi: task route: register: %w", err)
	}
	schedulerTaskActive := true
	defer func() {
		if schedulerTaskActive {
			s.scheduler.RemoveTask(createdTask.ID)
		}
	}()

	agent, err := s.scheduler.AssignTask(createdTask.ID)
	if err != nil {
		return nil, fmt.Errorf("localapi: task route: assign scheduler: %w", err)
	}

	// Record the routing decision as an ownership change, mirroring
	// internal/core/tasks.Store.Assign — this is what makes the task's owner
	// visible via wormhole.task.get/list, and what a future status transition
	// (wormhole.task.update_status) will build on.
	assignedTask, err := s.tr.AssignTx(ctx, tx, orgCtx.ProjectID, createdTask.ID, agent.AgentID)
	if err != nil {
		return nil, fmt.Errorf("localapi: task route: assign: %w", err)
	}

	queuePayload := map[string]interface{}{
		"id":             assignedTask.ID,
		"namespace_id":   assignedTask.NamespaceID,
		"title":          assignedTask.Title,
		"description":    assignedTask.Description,
		"status":         assignedTask.Status,
		"priority":       assignedTask.Priority,
		"owner_agent_id": assignedTask.OwnerAgentID,
		"parent_task_id": assignedTask.ParentTaskID,
		"due_by":         assignedTask.DueBy,
		"created_at":     assignedTask.CreatedAt,
		"updated_at":     assignedTask.UpdatedAt,
	}
	payload, err := json.Marshal(queuePayload)
	if err != nil {
		return nil, fmt.Errorf("localapi: task route: marshal payload: %w", err)
	}
	if _, err := s.qr.EnqueueTx(ctx, tx, orgCtx.ProjectID, "task", assignedTask.ID, "create", payload, assignedTask.Priority); err != nil {
		return nil, fmt.Errorf("localapi: task route: enqueue sync: %w", err)
	}
	if err := s.commitLocalWrite(tx); err != nil {
		return nil, fmt.Errorf("localapi: task route: commit: %w", err)
	}
	schedulerTaskActive = false

	return map[string]interface{}{
		"task_id":      assignedTask.ID,
		"namespace_id": orgCtx.ProjectID,
		"capability":   capability,
		"title":        title,
		"description":  desc,
		"status":       assignedTask.Status,
		"assigned_to":  agent.AgentID,
		"agent_status": string(agent.Status),
	}, nil
}

// handleChannelSubscribe's old body moved to handleChannelSubscribeMCP
// (mcp.go): event delivery is now notifications/wormhole.event messages
// interleaved with other tools/call traffic on the same connection, not
// this connection's sole writer (design doc §1, §2).

// =============================================================================
// Local write tools — task.create, kb.write, channel.post. Each writes the
// entity to the local SQLite replica, then enqueues it on the outbound sync
// queue (RFC-0003 §8.2) so the sync engine pushes it to the Coordination
// Server on its next cycle. Namespace is resolved from s.projectID — the
// value fixed at socket-construction time — same as every other handler in
// this file (see localGetTask, localListChannelEvents, localGetArticle,
// handleTaskRoute, handleAgentRegister). A client-supplied namespace_id in
// the request args is never trusted for authorization: honoring it would let
// any caller dialing a socket bound to one org/project write into another
// org/project's namespace. If the request also supplies namespace_id, it is
// ignored in favor of the resolved value (consistent with how the rest of
// this file silently uses s.projectID regardless of request args).
// =============================================================================

// handleTaskCreate serves wormhole.task.create: creates a task locally and
// enqueues it for sync.
// Args: {"title": "y", "description": "z", "priority": 0,
//
//	"project_id": "xxx" (optional in single-org, required in multi-org),
//	"parent_task_id": "..." (optional), "due_by": "RFC3339..." (optional)}
//
// namespace_id, if present in args, is ignored — namespace is always resolved
// from project_id (with multi-org bindings in P5+), never from the request.
func (s *Server) handleTaskCreate(ctx context.Context, args json.RawMessage) (map[string]interface{}, error) {
	if s.qr == nil {
		return nil, fmt.Errorf("localapi: task create: sync queue not available")
	}

	var argMap map[string]interface{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argMap); err != nil {
			return nil, fmt.Errorf("localapi: task create: invalid args: %w", err)
		}
	}

	title, _ := argMap["title"].(string)
	description, _ := argMap["description"].(string)
	if title == "" {
		return nil, fmt.Errorf("localapi: task create: missing title")
	}

	// Extract project_id and resolve org context (multi-org aware)
	projectID := s.projectID // fallback to configured project in single-org mode
	if projectIDVal, ok := argMap["project_id"].(string); ok && projectIDVal != "" {
		projectID = projectIDVal
	}

	orgCtx, err := s.resolveOrgContext(projectID)
	if err != nil {
		return nil, err
	}

	priority := 0
	if p, ok := argMap["priority"].(float64); ok {
		priority = int(p)
	}

	var parentTaskID *string
	if pid, ok := argMap["parent_task_id"].(string); ok && pid != "" {
		parentTaskID = &pid
	}

	var dueBy *time.Time
	if db, ok := argMap["due_by"].(string); ok && db != "" {
		if t, err := time.Parse(time.RFC3339, db); err == nil {
			dueBy = &t
		}
	}

	tx, err := s.beginLocalWrite(ctx)
	if err != nil {
		return nil, fmt.Errorf("localapi: task create: begin transaction: %w", err)
	}
	defer tx.Rollback()

	task, err := s.tr.CreateTaskTx(ctx, tx, orgCtx.ProjectID, title, description, parentTaskID, priority, dueBy)
	if err != nil {
		return nil, fmt.Errorf("localapi: task create: %w", err)
	}

	out := map[string]interface{}{
		"id":             task.ID,
		"namespace_id":   task.NamespaceID,
		"title":          task.Title,
		"description":    task.Description,
		"status":         task.Status,
		"priority":       task.Priority,
		"owner_agent_id": task.OwnerAgentID,
		"parent_task_id": task.ParentTaskID,
		"due_by":         task.DueBy,
		"created_at":     task.CreatedAt,
		"updated_at":     task.UpdatedAt,
	}

	payload, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("localapi: task create: marshal payload: %w", err)
	}
	if _, err := s.qr.EnqueueTx(ctx, tx, orgCtx.ProjectID, "task", task.ID, "create", payload, task.Priority); err != nil {
		return nil, fmt.Errorf("localapi: task create: enqueue sync: %w", err)
	}
	if err := s.commitLocalWrite(tx); err != nil {
		return nil, fmt.Errorf("localapi: task create: commit: %w", err)
	}

	return out, nil
}

// handleKBWrite serves wormhole.kb.write: writes a KB article locally and
// enqueues it for sync.
// Args: {"agent_id": "y", "title": "z", "body": "...",
//
//	"project_id": "xxx" (optional in single-org, required in multi-org),
//	"frontmatter": {...} (optional)}
//
// namespace_id, if present in args, is ignored — namespace is always resolved
// from project_id (with multi-org bindings in P5+), never from the request.
func (s *Server) handleKBWrite(ctx context.Context, args json.RawMessage) (map[string]interface{}, error) {
	if s.qr == nil {
		return nil, fmt.Errorf("localapi: kb write: sync queue not available")
	}

	var argMap map[string]interface{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argMap); err != nil {
			return nil, fmt.Errorf("localapi: kb write: invalid args: %w", err)
		}
	}

	agentID, _ := argMap["agent_id"].(string)
	title, _ := argMap["title"].(string)
	body, _ := argMap["body"].(string)
	if title == "" {
		return nil, fmt.Errorf("localapi: kb write: missing title")
	}

	// Extract project_id and resolve org context (multi-org aware)
	projectID := s.projectID // fallback to configured project in single-org mode
	if projectIDVal, ok := argMap["project_id"].(string); ok && projectIDVal != "" {
		projectID = projectIDVal
	}

	orgCtx, err := s.resolveOrgContext(projectID)
	if err != nil {
		return nil, err
	}

	var frontmatter json.RawMessage
	if fm, ok := argMap["frontmatter"]; ok {
		if fmRaw, err := json.Marshal(fm); err == nil {
			frontmatter = fmRaw
		}
	}

	tx, err := s.beginLocalWrite(ctx)
	if err != nil {
		return nil, fmt.Errorf("localapi: kb write: begin transaction: %w", err)
	}
	defer tx.Rollback()

	article, err := s.kb.WriteArticleTx(ctx, tx, orgCtx.ProjectID, agentID, title, body, frontmatter)
	if err != nil {
		return nil, fmt.Errorf("localapi: kb write: %w", err)
	}

	out := map[string]interface{}{
		"id":              article.ID,
		"namespace_id":    article.NamespaceID,
		"title":           article.Title,
		"body":            article.Body,
		"frontmatter":     json.RawMessage(article.Frontmatter),
		"author_agent_id": article.AuthorAgentID,
		"created_at":      article.CreatedAt,
		"updated_at":      article.UpdatedAt,
	}

	payload, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("localapi: kb write: marshal payload: %w", err)
	}
	// KB articles have no priority concept (internal/core/kb has no Priority
	// field, unlike tasks) — 0 here is the correct default, not a placeholder
	// for a value that should have been threaded through.
	if _, err := s.qr.EnqueueTx(ctx, tx, orgCtx.ProjectID, "kb", article.ID, "create", payload, 0); err != nil {
		return nil, fmt.Errorf("localapi: kb write: enqueue sync: %w", err)
	}
	if err := s.commitLocalWrite(tx); err != nil {
		return nil, fmt.Errorf("localapi: kb write: commit: %w", err)
	}

	return out, nil
}

// handleChannelPost serves wormhole.channel.post: publishes a durable event
// to a channel locally and enqueues it for sync.
// Args: {"channel_id": "y", "agent_id": "z",
//
//	"event_type": "discovery.logged",
//	"project_id": "xxx" (optional in single-org, required in multi-org),
//	"payload": {...} (optional), "note": "..." (optional)}
//
// namespace_id, if present in args, is ignored — namespace is always resolved
// from project_id (with multi-org bindings in P5+), never from the request.
func (s *Server) handleChannelPost(ctx context.Context, args json.RawMessage) (map[string]interface{}, error) {
	if s.qr == nil {
		return nil, fmt.Errorf("localapi: channel post: sync queue not available")
	}

	var argMap map[string]interface{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argMap); err != nil {
			return nil, fmt.Errorf("localapi: channel post: invalid args: %w", err)
		}
	}

	channelID, _ := argMap["channel_id"].(string)
	agentID, _ := argMap["agent_id"].(string)
	eventType, _ := argMap["event_type"].(string)
	if channelID == "" || eventType == "" {
		return nil, fmt.Errorf("localapi: channel post: missing channel_id or event_type")
	}

	// Extract project_id and resolve org context (multi-org aware)
	projectID := s.projectID // fallback to configured project in single-org mode
	if projectIDVal, ok := argMap["project_id"].(string); ok && projectIDVal != "" {
		projectID = projectIDVal
	}

	orgCtx, err := s.resolveOrgContext(projectID)
	if err != nil {
		return nil, err
	}

	var eventPayload json.RawMessage
	if p, ok := argMap["payload"]; ok {
		if pRaw, err := json.Marshal(p); err == nil {
			eventPayload = pRaw
		}
	}

	var note *string
	if n, ok := argMap["note"].(string); ok && n != "" {
		note = &n
	}

	tx, err := s.beginLocalWrite(ctx)
	if err != nil {
		return nil, fmt.Errorf("localapi: channel post: begin transaction: %w", err)
	}
	defer tx.Rollback()

	ev, err := s.er.PublishEventTx(ctx, tx, orgCtx.ProjectID, channelID, agentID, eventType, eventPayload, note)
	if err != nil {
		return nil, fmt.Errorf("localapi: channel post: %w", err)
	}

	out := map[string]interface{}{
		"id":           ev.ID,
		"namespace_id": ev.NamespaceID,
		"channel_id":   ev.ChannelID,
		"agent_id":     ev.AgentID,
		"event_type":   ev.EventType,
		"payload":      json.RawMessage(ev.Payload),
		"note":         ev.Note,
		"created_at":   ev.CreatedAt,
	}

	payload, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("localapi: channel post: marshal payload: %w", err)
	}
	// Events have no priority concept (internal/core/events has no Priority
	// field, unlike tasks) — 0 here is the correct default, not a placeholder
	// for a value that should have been threaded through.
	if _, err := s.qr.EnqueueTx(ctx, tx, orgCtx.ProjectID, "event", ev.ID, "create", payload, 0); err != nil {
		return nil, fmt.Errorf("localapi: channel post: enqueue sync: %w", err)
	}
	if err := s.commitLocalWrite(tx); err != nil {
		return nil, fmt.Errorf("localapi: channel post: commit: %w", err)
	}

	return out, nil
}

// handleChannelCreate creates a durable local channel and queues it for
// server synchronization in the same SQLite transaction.
func (s *Server) handleChannelCreate(ctx context.Context, args json.RawMessage) (map[string]interface{}, error) {
	if s.qr == nil {
		return nil, fmt.Errorf("localapi: channel create: sync queue not available")
	}
	var argMap map[string]interface{}
	if err := json.Unmarshal(args, &argMap); err != nil {
		return nil, fmt.Errorf("localapi: channel create: invalid args: %w", err)
	}
	name, _ := argMap["name"].(string)
	if name == "" {
		return nil, fmt.Errorf("localapi: channel create: missing name")
	}
	projectID := s.projectID
	if value, ok := argMap["project_id"].(string); ok && value != "" {
		projectID = value
	}
	orgCtx, err := s.resolveOrgContext(projectID)
	if err != nil {
		return nil, err
	}
	tx, err := s.beginLocalWrite(ctx)
	if err != nil {
		return nil, fmt.Errorf("localapi: channel create: begin transaction: %w", err)
	}
	defer tx.Rollback()
	channelID, err := s.er.CreateChannelTx(ctx, tx, orgCtx.ProjectID, name)
	if err != nil {
		return nil, fmt.Errorf("localapi: channel create: %w", err)
	}
	out := map[string]interface{}{"id": channelID, "namespace_id": orgCtx.ProjectID, "name": name}
	payload, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("localapi: channel create: marshal payload: %w", err)
	}
	if _, err := s.qr.EnqueueTx(ctx, tx, orgCtx.ProjectID, "channel", channelID, "create", payload, 0); err != nil {
		return nil, fmt.Errorf("localapi: channel create: enqueue sync: %w", err)
	}
	if err := s.commitLocalWrite(tx); err != nil {
		return nil, fmt.Errorf("localapi: channel create: commit: %w", err)
	}
	return out, nil
}
