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
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
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

// localRequest is the P1 local-socket request: one tool call, no
// arguments needed yet (whoami takes none beyond project_id, which the
// Server already knows from its own config).
type localRequest struct {
	Tool string `json:"tool"`
}

// localResponse is the P1 local-socket response.
type localResponse struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Server is wormholed's local API socket server.
type Server struct {
	listener    net.Listener
	socketPath  string
	httpClient  *http.Client
	coordServer string
	token       string
	projectID   string
	store       *localstore.Store
}

// New binds the Unix domain socket at socketPath. Callers must call Serve
// to start accepting connections, and Close to release the socket.
func New(socketPath, coordServerURL, token, projectID string, store *localstore.Store) (*Server, error) {
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
		store:       store,
	}, nil
}

// Close stops accepting connections and releases the socket.
func (s *Server) Close() error {
	return s.listener.Close()
}

// Serve accepts connections until ctx is cancelled or the listener closes.
func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		s.listener.Close()
	}()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
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
	default:
		writeResponse(conn, localResponse{Error: fmt.Sprintf("localapi: unsupported tool %q in P1 walking skeleton", req.Tool)})
	}
}

func writeResponse(conn net.Conn, resp localResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	conn.Write(append(data, '\n'))
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
