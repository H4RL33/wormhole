// Package webui implements the read-only human dashboard API (RFC-0001 §14
// V2, pulled forward into Alpha-2 M3). It is a plain JSON REST surface, not
// MCP/JSON-RPC — RFC-0001 §14 plus ROADMAP-ALPHA2.md's M3 scope sanction the
// human read-only dashboard as an exception to "every capability is an MCP
// tool", not a precedent for further REST endpoints. This package is mounted
// under /dashboard in cmd/wormhole-server/main.go.
package webui

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/H4RL33/wormhole/internal/core/events"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/core/kb"
	"github.com/H4RL33/wormhole/internal/core/tasks"
)

// indexHTML is Task 1's static dashboard page, embedded at build time so
// cmd/wormhole-server/main.go can serve it without a runtime filesystem
// dependency.
//
//go:embed static/index.html
var indexHTML []byte

// serveIndex serves the embedded static dashboard page.
func serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

// Handler serves the read-only dashboard API.
type Handler struct {
	Identity *identity.Store
	Tasks    *tasks.Store
	Events   *events.Store
	KB       *kb.Store
	AdminKey string
}

// NewMux returns the dashboard API's routes, mounted under /dashboard in
// cmd/wormhole-server/main.go.
func (h *Handler) NewMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /dashboard/", serveIndex)
	mux.HandleFunc("GET /dashboard/api/projects/{id}/tasks", h.withViewerAuth(h.listTasks))
	mux.HandleFunc("GET /dashboard/api/projects/{id}/events", h.withViewerAuth(h.listEvents))
	mux.HandleFunc("GET /dashboard/api/projects/{id}/kb", h.withViewerAuth(h.listKB))
	mux.HandleFunc("POST /dashboard/api/projects/{id}/viewer-keys", h.withAdminKey(h.createViewerKey))
	return mux
}

// withViewerAuth resolves the Authorization: Bearer <key> header against
// identity.Store.ResolveViewerKey, scoped to the {id} path param's project.
// Any failure (missing header, malformed header, unknown key, key belongs to
// a different project) returns the same 403 JSON error —
// docs/architecture.md §3.4's single-error-shape rule applies to this
// human-facing boundary too.
// INVARIANT: every route registered by NewMux must have a non-empty {id} path segment;
// ResolveViewerKey("", token) accepts any valid viewer key for any project.
func (h *Handler) withViewerAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := r.PathValue("id")
		token := extractBearerToken(r.Header.Get("Authorization"))
		if _, err := h.Identity.ResolveViewerKey(r.Context(), projectID, token); err != nil {
			writeJSONError(w, http.StatusForbidden, "invalid or unauthorized viewer key")
			return
		}
		next(w, r)
	}
}

// extractBearerToken parses "Bearer <token>" from an Authorization header
// value, returning "" if the header is missing or malformed. A malformed
// header is an auth failure handled uniformly by withViewerAuth, not a
// server error — this function never panics.
func extractBearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimPrefix(header, prefix)
}

// writeJSONError writes {"error": message} with the given status code.
func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// writeJSON writes v as a JSON body with Content-Type: application/json.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (h *Handler) listTasks(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	result, err := h.Tasks.List(r.Context(), projectID, nil)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list tasks")
		return
	}
	if result == nil {
		result = []tasks.Task{}
	}
	writeJSON(w, result)
}

func (h *Handler) listEvents(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	result, err := h.Events.ListEventsByProject(r.Context(), projectID, 100, 0)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list events")
		return
	}
	if result == nil {
		result = []events.Event{}
	}
	writeJSON(w, result)
}

func (h *Handler) listKB(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	result, err := h.KB.ListArticles(r.Context(), projectID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list kb articles")
		return
	}
	if result == nil {
		result = []kb.Article{}
	}
	writeJSON(w, result)
}
