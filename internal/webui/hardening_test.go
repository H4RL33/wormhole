package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDashboardAPI_ReadOnlyEnforcedAtRouter asserts that POST/PUT/DELETE/PATCH
// against every /dashboard/api/* route registered by NewMux() is rejected by the
// router itself (405), never reaching a handler — read-only is a routing-level
// guarantee, not just a convention no handler happens to violate (Alpha-2 Chapter 11).
func TestDashboardAPI_ReadOnlyEnforcedAtRouter(t *testing.T) {
	h := &Handler{}
	srv := httptest.NewServer(h.NewMux())
	defer srv.Close()

	routes := []string{
		"/dashboard/api/projects/00000000-0000-0000-0000-000000000000/tasks",
		"/dashboard/api/projects/00000000-0000-0000-0000-000000000000/events",
		"/dashboard/api/projects/00000000-0000-0000-0000-000000000000/kb",
	}
	methods := []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch}

	for _, route := range routes {
		for _, method := range methods {
			t.Run(method+" "+route, func(t *testing.T) {
				req, err := http.NewRequest(method, srv.URL+route, nil)
				if err != nil {
					t.Fatalf("build request: %v", err)
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatalf("do request: %v", err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusMethodNotAllowed {
					t.Fatalf("status: got %d, want 405 (method must be rejected at the router)", resp.StatusCode)
				}
			})
		}
	}
}
