package mcp

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/H4RL33/wormhole/internal/core/identity"
)

// CallRequest is the envelope for a single MCP tool invocation over the
// /mcp/tools/call HTTP endpoint. project_id is always required, even for
// tools that don't need auth yet (e.g. registration), because it's the
// scope every identity operation is keyed on (RFC-0001 §13).
type CallRequest struct {
	Tool      string          `json:"tool"`
	ProjectID string          `json:"project_id"`
	Arguments json.RawMessage `json:"arguments"`
}

// CallResponse is the envelope returned for a tool call: exactly one of
// Result or Error is populated.
type CallResponse struct {
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// NewCallHandler builds the /mcp/tools/call endpoint. It decodes the call
// envelope, looks up the tool, and — for tools with RequiresAuth — resolves
// the caller's bearer token via identityStore.WhoAmI before dispatch
// (docs/architecture.md M4: auth happens once, at this boundary; handlers
// receive an already-resolved scope, never a raw token).
func NewCallHandler(registry *Registry, identityStore *identity.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req CallRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeCallResponse(w, http.StatusBadRequest, CallResponse{Error: "invalid request body"})
			return
		}

		tool, ok := registry.Get(req.Tool)
		if !ok {
			writeCallResponse(w, http.StatusNotFound, CallResponse{Error: "unknown tool: " + req.Tool})
			return
		}

		var scope *identity.AuthenticatedScope
		if tool.RequiresAuth {
			token := bearerToken(r.Header.Get("Authorization"))
			if token == "" {
				writeCallResponse(w, http.StatusUnauthorized, CallResponse{Error: "missing bearer token"})
				return
			}
			resolved, err := identityStore.WhoAmI(r.Context(), req.ProjectID, token)
			if errors.Is(err, identity.ErrInvalidToken) {
				writeCallResponse(w, http.StatusUnauthorized, CallResponse{Error: "invalid or expired token"})
				return
			}
			if err != nil {
				writeCallResponse(w, http.StatusInternalServerError, CallResponse{Error: "auth resolution failed"})
				return
			}
			scope = &resolved
		}

		result, err := tool.Handler(r.Context(), scope, req.ProjectID, req.Arguments)
		if err != nil {
			writeCallResponse(w, http.StatusBadRequest, CallResponse{Error: err.Error()})
			return
		}
		writeCallResponse(w, http.StatusOK, CallResponse{Result: result})
	}
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimPrefix(header, prefix)
}

func writeCallResponse(w http.ResponseWriter, status int, resp CallResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(resp)
}
