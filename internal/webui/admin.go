// Package webui: admin.go adds the one write route this package exposes —
// issuing a new viewer key — gated by a shared operator secret rather than
// a viewer key or agent token. This is a deliberate stopgap (issue #23):
// real human identity/auth doesn't exist yet (issue #22), so a single
// config-held secret gates who can mint dashboard access for a human.
package webui

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
)

// withAdminKey gates a handler behind Handler.AdminKey, compared against
// the X-Admin-Key request header with a constant-time comparison. If
// AdminKey is unset, every request is rejected with 503 — there is no
// insecure default. A missing or wrong header both return the same 403
// body, matching withViewerAuth's side-channel-neutral convention below.
func (h *Handler) withAdminKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.AdminKey == "" {
			writeJSONError(w, http.StatusServiceUnavailable, "dashboard admin key not configured")
			return
		}
		provided := r.Header.Get("X-Admin-Key")
		if subtle.ConstantTimeCompare([]byte(provided), []byte(h.AdminKey)) != 1 {
			writeJSONError(w, http.StatusForbidden, "invalid admin key")
			return
		}
		next(w, r)
	}
}

// createViewerKeyRequest is the POST /dashboard/api/projects/{id}/viewer-keys
// request body.
type createViewerKeyRequest struct {
	Label string `json:"label"`
}

// createViewerKeyResponse is returned once — ViewerKey is the raw key,
// never persisted or logged anywhere beyond this response body.
type createViewerKeyResponse struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Label     string `json:"label"`
	ViewerKey string `json:"viewer_key"`
}

// createViewerKey implements POST /dashboard/api/projects/{id}/viewer-keys.
func (h *Handler) createViewerKey(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if projectID == "" {
		writeJSONError(w, http.StatusBadRequest, "project id is required")
		return
	}

	var req createViewerKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Label == "" {
		writeJSONError(w, http.StatusBadRequest, "label is required")
		return
	}

	rawKey, id, err := h.Identity.CreateViewerKey(r.Context(), projectID, req.Label)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to create viewer key")
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, createViewerKeyResponse{
		ID:        id,
		ProjectID: projectID,
		Label:     req.Label,
		ViewerKey: rawKey,
	})
}
