package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/H4RL33/wormhole/internal/core/identity"
)

func TestCreateViewerKey(t *testing.T) {
	db := testDB(t)
	identityStore := identity.NewStore(db)
	projectID := mustCreateProject(t, db, "webui-admin-project")

	post := func(h *Handler, path, adminKeyHeader string, body []byte) (*http.Response, []byte) {
		srv := httptest.NewServer(h.NewMux())
		defer srv.Close()
		req, err := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if adminKeyHeader != "" {
			req.Header.Set("X-Admin-Key", adminKeyHeader)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do request: %v", err)
		}
		defer resp.Body.Close()
		buf := make([]byte, 0, 4096)
		tmp := make([]byte, 4096)
		for {
			n, rerr := resp.Body.Read(tmp)
			buf = append(buf, tmp[:n]...)
			if rerr != nil {
				break
			}
		}
		return resp, buf
	}

	t.Run("happy path: valid admin key creates a working viewer key", func(t *testing.T) {
		h := &Handler{Identity: identityStore, AdminKey: "correct-secret"}
		body, _ := json.Marshal(map[string]string{"label": "ops-created viewer"})
		resp, respBody := post(h, "/dashboard/api/projects/"+projectID+"/viewer-keys", "correct-secret", body)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status: got %d, want 201, body: %s", resp.StatusCode, respBody)
		}
		var out struct {
			ID        string `json:"id"`
			ProjectID string `json:"project_id"`
			Label     string `json:"label"`
			ViewerKey string `json:"viewer_key"`
		}
		if err := json.Unmarshal(respBody, &out); err != nil {
			t.Fatalf("decode: %v, body: %s", err, respBody)
		}
		if out.ProjectID != projectID || out.Label != "ops-created viewer" || out.ViewerKey == "" || out.ID == "" {
			t.Fatalf("response fields: got %+v", out)
		}

		// The returned viewer_key must actually authenticate against an
		// existing read-only route.
		if _, err := identityStore.ResolveViewerKey(context.Background(), projectID, out.ViewerKey); err != nil {
			t.Fatalf("returned viewer_key does not resolve: %v", err)
		}
	})

	t.Run("wrong admin key: 403", func(t *testing.T) {
		h := &Handler{Identity: identityStore, AdminKey: "correct-secret"}
		body, _ := json.Marshal(map[string]string{"label": "x"})
		resp, respBody := post(h, "/dashboard/api/projects/"+projectID+"/viewer-keys", "wrong-secret", body)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status: got %d, want 403, body: %s", resp.StatusCode, respBody)
		}
	})

	t.Run("missing admin key header: 403", func(t *testing.T) {
		h := &Handler{Identity: identityStore, AdminKey: "correct-secret"}
		body, _ := json.Marshal(map[string]string{"label": "x"})
		resp, respBody := post(h, "/dashboard/api/projects/"+projectID+"/viewer-keys", "", body)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status: got %d, want 403, body: %s", resp.StatusCode, respBody)
		}
	})

	t.Run("AdminKey unset on Handler: 503", func(t *testing.T) {
		h := &Handler{Identity: identityStore, AdminKey: ""}
		body, _ := json.Marshal(map[string]string{"label": "x"})
		resp, respBody := post(h, "/dashboard/api/projects/"+projectID+"/viewer-keys", "anything", body)
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("status: got %d, want 503, body: %s", resp.StatusCode, respBody)
		}
	})

	t.Run("missing label: 400", func(t *testing.T) {
		h := &Handler{Identity: identityStore, AdminKey: "correct-secret"}
		body, _ := json.Marshal(map[string]string{"label": ""})
		resp, respBody := post(h, "/dashboard/api/projects/"+projectID+"/viewer-keys", "correct-secret", body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status: got %d, want 400, body: %s", resp.StatusCode, respBody)
		}
	})
}
