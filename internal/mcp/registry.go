// Package mcp exposes MCP (Model Context Protocol) tools to agents and external
// callers. Tools register into a Registry at boot; the server routes calls to
// handlers via envelope dispatch, applying auth middleware per RequiresAuth.
package mcp

import (
	"context"
	"encoding/json"

	"github.com/H4RL33/wormhole/internal/core/identity"
)

// Handler executes one MCP tool call. scope is nil when the tool's
// RequiresAuth is false; otherwise it is the AuthenticatedScope the auth
// middleware already resolved from the caller's bearer token
// (docs/architecture.md M4 — handlers never see a raw token). projectID is
// always populated from the call envelope, independent of auth, since
// project-scoped bootstrap calls (e.g. registration) need it before any
// token exists.
type Handler func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error)

// Tool is an MCP tool descriptor: name, docs, whether the auth middleware
// must resolve a scope before dispatch, and the handler itself.
type Tool struct {
	Name         string  `json:"name"`
	Description  string  `json:"description"`
	RequiresAuth bool    `json:"requires_auth"`
	Handler      Handler `json:"-"`
}

// Registry holds the set of MCP tools this server exposes. Empty at boot
// per Day 1 scope — tools register themselves as each pillar lands.
type Registry struct {
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

func (r *Registry) Register(t Tool) {
	r.tools[t.Name] = t
}

// Get returns the tool registered under name, if any.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

func (r *Registry) List() []Tool {
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}
