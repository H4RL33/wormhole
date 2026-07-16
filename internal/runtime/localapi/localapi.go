// Package localapi is wormholed's local API: a Unix-domain-socket server
// coding harnesses connect to (RFC-0003 §6.1). Wire shapes (localRequest/
// localResponse) are P1's own minimal protocol — one JSON request per
// connection, one JSON response, connection closed. Later phases (P2+)
// extend this to a persistent, multiplexed, subscription-capable protocol;
// P1 deliberately keeps it to the smallest thing that proves the chain.
//
// rpcRequest/rpcResponse/toolsCallParams/toolCallResult/whoAmIOutput mirror
// internal/mcp's JSON-RPC 2.0 wire shapes for talking to the Coordination
// Server. localapi cannot import internal/mcp (RFC-0003 §6.3 keeps
// internal/runtime/* and internal/mcp separate trees), so the wire
// contract is duplicated here, same as cmd/wormhole-cli/main.go already
// does for the same reason.
package localapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
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

// localRequest is a P1/P2 local-socket request. P1: Tool only. P2+: args may be populated.
type localRequest struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args,omitempty"`
}

// localResponse is the P1 local-socket response.
type localResponse struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
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
}

// New binds the Unix domain socket at socketPath. Callers must call Serve
// to start accepting connections, and Close to release the socket.
// Single-org mode (P1-P4).
//
// Socket permissions (RFC-0003 OQ4, §7.2, P6 hardening): net.Listen("unix", path)
// creates a socket file with OS-default permissions (typically 0755 on most Unix variants).
// This means the socket is world-accessible by path. Access control relies on
// file-system-level permissions and the assumption that only the owning user's
// processes will dial it (RFC-0003 OQ4 conservative default: "same-user process trust
// assumed... unless a concrete threat model says otherwise; multi-user machine sharing
// a single wormholed is out of scope for v1").
//
// Production deployments concerned with multi-user isolation should implement
// stricter socket permissions (chmod 0700 after creation) or use an additional
// local authentication layer — currently out of scope per RFC-0003 OQ4.
func New(socketPath, coordServerURL, token, projectID string, store *localstore.Store, tr *localstore.TaskRepo, er *localstore.EventRepo, kb *localstore.KBRepo, qr *syncpkg.QueueRepo) (*Server, error) {
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("localapi: listen on %s: %w", socketPath, err)
	}
	return &Server{
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
	}, nil
}

// NewWithRuntime binds the Unix domain socket at socketPath and wires eventbus
// + scheduler (P3). Callers must call Serve to start accepting connections,
// and Close to release the socket.
// Socket permissions: see New() for RFC-0003 OQ4 assumptions and P6 hardening notes.
func NewWithRuntime(socketPath, coordServerURL, token, projectID string, store *localstore.Store, tr *localstore.TaskRepo, er *localstore.EventRepo, kb *localstore.KBRepo, eb *eventbus.EventBus, sched *scheduler.Scheduler, qr *syncpkg.QueueRepo) (*Server, error) {
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("localapi: listen on %s: %w", socketPath, err)
	}
	return &Server{
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
	}, nil
}

// NewMultiOrg binds the Unix domain socket and configures multi-org support (P5+, RFC-0003 §7.1).
// Orgs is a map of org_name → Org credentials. Bindings map project contexts to org names.
// Callers must call Serve to start accepting connections, and Close to release the socket.
// Socket permissions: see New() for RFC-0003 OQ4 assumptions and P6 hardening notes.
func NewMultiOrg(socketPath string, orgs map[string]config.Org, bindings []config.ProjectBinding, store *localstore.Store, tr *localstore.TaskRepo, er *localstore.EventRepo, kb *localstore.KBRepo, eb *eventbus.EventBus, sched *scheduler.Scheduler, qr *syncpkg.QueueRepo) (*Server, error) {
	if len(orgs) == 0 {
		return nil, fmt.Errorf("localapi: NewMultiOrg: no orgs provided")
	}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("localapi: listen on %s: %w", socketPath, err)
	}
	return &Server{
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
	}, nil
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
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.shutdown.Store(true)
		s.closeErr = s.listener.Close()
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
				return nil
			}
			return fmt.Errorf("localapi: accept: %w", err)
		}
		go s.handle(ctx, conn)
	}
}

func (s *Server) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}
	var req localRequest
	if err := json.Unmarshal(bytes.TrimSpace(line), &req); err != nil {
		writeResponse(conn, localResponse{Error: fmt.Sprintf("localapi: decode request: %v", err)})
		return
	}

	switch req.Tool {
	case "wormhole.agent.whoami":
		out, err := s.proxyWhoAmI(ctx)
		if err != nil {
			writeResponse(conn, localResponse{Error: err.Error()})
			return
		}
		outRaw, _ := json.Marshal(out)
		writeResponse(conn, localResponse{Result: outRaw})

	case "wormhole.task.list":
		result, err := s.localListTasks(ctx, req.Args)
		if err != nil {
			writeResponse(conn, localResponse{Error: err.Error()})
			return
		}
		outRaw, _ := json.Marshal(result)
		writeResponse(conn, localResponse{Result: outRaw})

	case "wormhole.task.get":
		result, err := s.localGetTask(ctx, req.Args)
		if err != nil {
			writeResponse(conn, localResponse{Error: err.Error()})
			return
		}
		outRaw, _ := json.Marshal(result)
		writeResponse(conn, localResponse{Result: outRaw})

	case "wormhole.channel.list":
		result, err := s.localListChannels(ctx, req.Args)
		if err != nil {
			writeResponse(conn, localResponse{Error: err.Error()})
			return
		}
		outRaw, _ := json.Marshal(result)
		writeResponse(conn, localResponse{Result: outRaw})

	case "wormhole.channel.events":
		result, err := s.localListChannelEvents(ctx, req.Args)
		if err != nil {
			writeResponse(conn, localResponse{Error: err.Error()})
			return
		}
		outRaw, _ := json.Marshal(result)
		writeResponse(conn, localResponse{Result: outRaw})

	case "wormhole.kb.list":
		result, err := s.localListArticles(ctx, req.Args)
		if err != nil {
			writeResponse(conn, localResponse{Error: err.Error()})
			return
		}
		outRaw, _ := json.Marshal(result)
		writeResponse(conn, localResponse{Result: outRaw})

	case "wormhole.kb.get":
		result, err := s.localGetArticle(ctx, req.Args)
		if err != nil {
			writeResponse(conn, localResponse{Error: err.Error()})
			return
		}
		outRaw, _ := json.Marshal(result)
		writeResponse(conn, localResponse{Result: outRaw})

	case "wormhole.agent.register":
		if isJoinRegisterArgs(req.Args) {
			// RFC-0003 §8.1: `wormhole join` now targets wormholed, which
			// proxies passport creation to the Coordination Server. RFC-0001
			// §9 defines a single wormhole.agent.register tool for this and
			// for local presence registration (P3's handleAgentRegister
			// below); the two shapes never overlap in practice (join args
			// carry owner/model/etc., presence args carry agent_id), so
			// dispatch is by shape rather than inventing a second tool name.
			outRaw, err := s.proxyRegister(ctx, req.Args)
			if err != nil {
				writeResponse(conn, localResponse{Error: err.Error()})
				return
			}
			writeResponse(conn, localResponse{Result: outRaw})
			return
		}
		result, err := s.handleAgentRegister(ctx, req.Args)
		if err != nil {
			writeResponse(conn, localResponse{Error: err.Error()})
			return
		}
		outRaw, _ := json.Marshal(result)
		writeResponse(conn, localResponse{Result: outRaw})

	case "wormhole.agent.presence":
		result, err := s.handleAgentPresence(ctx, req.Args)
		if err != nil {
			writeResponse(conn, localResponse{Error: err.Error()})
			return
		}
		outRaw, _ := json.Marshal(result)
		writeResponse(conn, localResponse{Result: outRaw})

	case "wormhole.agent.list":
		result, err := s.handleAgentList(ctx, req.Args)
		if err != nil {
			writeResponse(conn, localResponse{Error: err.Error()})
			return
		}
		outRaw, _ := json.Marshal(result)
		writeResponse(conn, localResponse{Result: outRaw})

	case "wormhole.task.route":
		result, err := s.handleTaskRoute(ctx, req.Args)
		if err != nil {
			writeResponse(conn, localResponse{Error: err.Error()})
			return
		}
		outRaw, _ := json.Marshal(result)
		writeResponse(conn, localResponse{Result: outRaw})

	case "wormhole.channel.subscribe":
		s.handleChannelSubscribe(ctx, conn, req.Args)

	case "wormhole.task.create":
		result, err := s.handleTaskCreate(ctx, req.Args)
		if err != nil {
			writeResponse(conn, localResponse{Error: err.Error()})
			return
		}
		outRaw, _ := json.Marshal(result)
		writeResponse(conn, localResponse{Result: outRaw})

	case "wormhole.kb.write":
		result, err := s.handleKBWrite(ctx, req.Args)
		if err != nil {
			writeResponse(conn, localResponse{Error: err.Error()})
			return
		}
		outRaw, _ := json.Marshal(result)
		writeResponse(conn, localResponse{Result: outRaw})

	case "wormhole.channel.post":
		result, err := s.handleChannelPost(ctx, req.Args)
		if err != nil {
			writeResponse(conn, localResponse{Error: err.Error()})
			return
		}
		outRaw, _ := json.Marshal(result)
		writeResponse(conn, localResponse{Result: outRaw})

	default:
		writeResponse(conn, localResponse{Error: fmt.Sprintf("localapi: unsupported tool %q", req.Tool)})
	}
}

func writeResponse(conn net.Conn, resp localResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	conn.Write(append(data, '\n'))
}

// isJoinRegisterArgs reports whether a wormhole.agent.register call's args
// are the join/passport-creation shape (RFC-0001 §9, cmd/wormhole-cli's
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
// Coordination Server, unauthenticated (matching cmd/wormhole-cli's
// doRegister, which sends no bearer token for this call — a Passport
// doesn't exist yet). project_id is expected to already be present in args
// (cmd/wormhole-cli's callTool folds it in before sending), so this simply
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
	argsRaw, _ := json.Marshal(map[string]string{"project_id": s.projectID})
	paramsRaw, err := json.Marshal(toolsCallParams{Name: "wormhole.agent.whoami", Arguments: argsRaw})
	if err != nil {
		return whoAmIOutput{}, fmt.Errorf("localapi: marshal params: %w", err)
	}
	reqBody, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call", Params: paramsRaw})
	if err != nil {
		return whoAmIOutput{}, fmt.Errorf("localapi: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.coordServer, "/")+"/mcp", bytes.NewReader(reqBody))
	if err != nil {
		return whoAmIOutput{}, fmt.Errorf("localapi: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+s.token)

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

	return out, nil
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
			"payload":    string(ev.Payload),
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
			"frontmatter":     string(a.Frontmatter),
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
		"frontmatter":     string(a.Frontmatter),
		"author_agent_id": a.AuthorAgentID,
		"created_at":      a.CreatedAt,
		"updated_at":      a.UpdatedAt,
	}, nil
}

// =============================================================================
// P3 tools — agent registration, presence, listing, task routing, subscriptions
// =============================================================================

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

	// Create the task in localstore FIRST: its UUID is the one true task ID
	// (Finding 1). A creation failure is fatal to the request, not swallowed.
	createdTask, err := s.tr.CreateTask(ctx, orgCtx.ProjectID, title, desc, nil, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("localapi: task route: create: %w", err)
	}

	// The scheduler is used purely for capability matching / agent selection,
	// keyed by the localstore-generated task ID — it does not mint its own ID
	// or track a competing status (Finding 2).
	if _, err := s.scheduler.RegisterTask(orgCtx.ProjectID, capability, createdTask.ID); err != nil {
		return nil, fmt.Errorf("localapi: task route: register: %w", err)
	}

	agent, err := s.scheduler.AssignTask(createdTask.ID)
	if err != nil {
		return map[string]interface{}{
			"task_id":      createdTask.ID,
			"namespace_id": orgCtx.ProjectID,
			"capability":   capability,
			"title":        title,
			"description":  desc,
			"status":       createdTask.Status,
			"assigned_to":  "",
			"error":        err.Error(),
		}, nil
	}

	// Record the routing decision as an ownership change, mirroring
	// internal/core/tasks.Store.Assign — this is what makes the task's owner
	// visible via wormhole.task.get/list, and what a future status transition
	// (wormhole.task.update_status) will build on.
	assignedTask, err := s.tr.Assign(ctx, orgCtx.ProjectID, createdTask.ID, agent.AgentID)
	if err != nil {
		return nil, fmt.Errorf("localapi: task route: assign: %w", err)
	}

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

// handleChannelSubscribe creates an eventbus subscription for the caller's connection.
// Args: {"namespace": "x", "event_type": "presence.online", "capability": "code",
// "agent_id": "agent-a"} (any one or more). "namespace" and "project" are the same
// concept in this local runtime (s.projectID is passed as the eventbus namespace
// everywhere a publish happens), so there is no separate project dimension to add
// on top of namespace (Finding 5).
// The subscription ID is returned; events will be delivered on this connection as
// newline-delimited JSON messages until the subscriber calls close or the connection
// is dropped. This function blocks until the subscription is closed or ctx is
// cancelled (server shutdown), at which point it unsubscribes to release the
// eventbus's subscriber-map entry and let this goroutine exit (Finding 4).
func (s *Server) handleChannelSubscribe(ctx context.Context, conn net.Conn, args json.RawMessage) error {
	if s.eventbus == nil {
		return fmt.Errorf("localapi: channel subscribe: eventbus not available")
	}

	var argMap map[string]interface{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argMap); err != nil {
			return fmt.Errorf("localapi: channel subscribe: invalid args: %w", err)
		}
	}

	ns, _ := argMap["namespace"].(string)
	et, _ := argMap["event_type"].(string)
	capability, _ := argMap["capability"].(string)
	agentID, _ := argMap["agent_id"].(string)

	sub, err := s.eventbus.Subscribe(ns, et, capability, agentID)
	if err != nil {
		return fmt.Errorf("localapi: channel subscribe: %w", err)
	}

	// Return the subscription info first so the caller knows it was created.
	respJSON, _ := json.Marshal(map[string]string{
		"subscription_id": sub.ID,
		"namespace":        ns,
		"event_type":       et,
		"capability":       capability,
		"agent_id":         agentID,
	})
	writeResponse(conn, localResponse{Result: respJSON})

	// Block-deliver events on this connection until the subscription is closed
	// or ctx is cancelled (server shutdown). Either path unsubscribes so the
	// eventbus stops tracking this connection and this goroutine exits.
	for {
		select {
		case <-ctx.Done():
			s.eventbus.Unsubscribe(sub)
			return nil
		case <-sub.Done():
			return nil // unsubscription
		case payload, ok := <-sub.Events():
			if !ok {
				return nil // channel drained
			}
			writeResponse(conn, localResponse{Result: json.RawMessage(payload)})
		}
	}
}

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
//        "project_id": "xxx" (optional in single-org, required in multi-org),
//        "parent_task_id": "..." (optional), "due_by": "RFC3339..." (optional)}
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

	task, err := s.tr.CreateTask(ctx, orgCtx.ProjectID, title, description, parentTaskID, priority, dueBy)
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
	if _, err := s.qr.Enqueue(ctx, orgCtx.ProjectID, "task", task.ID, "create", payload, task.Priority); err != nil {
		return nil, fmt.Errorf("localapi: task create: enqueue sync: %w", err)
	}

	return out, nil
}

// handleKBWrite serves wormhole.kb.write: writes a KB article locally and
// enqueues it for sync.
// Args: {"agent_id": "y", "title": "z", "body": "...",
//        "project_id": "xxx" (optional in single-org, required in multi-org),
//        "frontmatter": {...} (optional)}
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

	article, err := s.kb.WriteArticle(ctx, orgCtx.ProjectID, agentID, title, body, frontmatter)
	if err != nil {
		return nil, fmt.Errorf("localapi: kb write: %w", err)
	}

	out := map[string]interface{}{
		"id":              article.ID,
		"namespace_id":    article.NamespaceID,
		"title":           article.Title,
		"body":            article.Body,
		"frontmatter":     string(article.Frontmatter),
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
	if _, err := s.qr.Enqueue(ctx, orgCtx.ProjectID, "kb", article.ID, "create", payload, 0); err != nil {
		return nil, fmt.Errorf("localapi: kb write: enqueue sync: %w", err)
	}

	return out, nil
}

// handleChannelPost serves wormhole.channel.post: publishes a durable event
// to a channel locally and enqueues it for sync.
// Args: {"channel_id": "y", "agent_id": "z",
//        "event_type": "discovery.logged",
//        "project_id": "xxx" (optional in single-org, required in multi-org),
//        "payload": {...} (optional), "note": "..." (optional)}
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

	ev, err := s.er.PublishEvent(ctx, orgCtx.ProjectID, channelID, agentID, eventType, eventPayload, note)
	if err != nil {
		return nil, fmt.Errorf("localapi: channel post: %w", err)
	}

	out := map[string]interface{}{
		"id":           ev.ID,
		"namespace_id": ev.NamespaceID,
		"channel_id":   ev.ChannelID,
		"agent_id":     ev.AgentID,
		"event_type":   ev.EventType,
		"payload":      string(ev.Payload),
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
	if _, err := s.qr.Enqueue(ctx, orgCtx.ProjectID, "event", ev.ID, "create", payload, 0); err != nil {
		return nil, fmt.Errorf("localapi: channel post: enqueue sync: %w", err)
	}

	return out, nil
}
