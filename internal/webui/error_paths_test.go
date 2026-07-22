package webui

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "github.com/lib/pq"

	"github.com/H4RL33/wormhole/internal/core/events"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/core/kb"
	"github.com/H4RL33/wormhole/internal/core/tasks"
)

func TestDashboardIndexServesEmbeddedHTML(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/dashboard/", nil)
	(&Handler{}).NewMux().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("content type: got %q", got)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "<!DOCTYPE html>") {
		t.Fatalf("body does not contain embedded dashboard HTML: %q", body)
	}
}

func TestDashboardListHandlersHideStoreErrors(t *testing.T) {
	db := closedPostgresHandle(t)
	eventsStore := events.NewStore(db)
	h := &Handler{
		Tasks:  tasks.NewStore(db, eventsStore),
		Events: eventsStore,
		KB:     kb.NewStore(db, kb.StubEmbedder{}, 0.85, 4000, 1, 1, 1),
	}

	tests := []struct {
		name    string
		handle  http.HandlerFunc
		message string
	}{
		{name: "tasks", handle: h.listTasks, message: "failed to list tasks"},
		{name: "events", handle: h.listEvents, message: "failed to list events"},
		{name: "kb", handle: h.listKB, message: "failed to list kb articles"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.SetPathValue("id", "project-id")
			recorder := httptest.NewRecorder()

			tt.handle(recorder, request)

			assertJSONError(t, recorder, http.StatusInternalServerError, tt.message)
		})
	}
}

func TestCreateViewerKeyValidatesInputAndHidesStoreErrors(t *testing.T) {
	t.Run("project id required", func(t *testing.T) {
		h := &Handler{}
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"label":"viewer"}`))
		recorder := httptest.NewRecorder()

		h.createViewerKey(recorder, request)

		assertJSONError(t, recorder, http.StatusBadRequest, "project id is required")
	})

	t.Run("malformed JSON", func(t *testing.T) {
		h := &Handler{}
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{"))
		request.SetPathValue("id", "project-id")
		recorder := httptest.NewRecorder()

		h.createViewerKey(recorder, request)

		assertJSONError(t, recorder, http.StatusBadRequest, "invalid request body")
	})

	t.Run("identity store failure", func(t *testing.T) {
		h := &Handler{Identity: identity.NewStore(closedPostgresHandle(t))}
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"label":"viewer"}`))
		request.SetPathValue("id", "project-id")
		recorder := httptest.NewRecorder()

		h.createViewerKey(recorder, request)

		assertJSONError(t, recorder, http.StatusInternalServerError, "failed to create viewer key")
	})
}

func closedPostgresHandle(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", "postgres://localhost/wormhole?sslmode=disable")
	if err != nil {
		t.Fatalf("open postgres handle: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close postgres handle: %v", err)
	}
	return db
}

func assertJSONError(t *testing.T, recorder *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status: got %d, want %d, body: %s", recorder.Code, status, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type: got %q", got)
	}
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v, body: %s", err, recorder.Body.String())
	}
	if got := body["error"]; got != message {
		t.Fatalf("error: got %q, want %q", got, message)
	}
}
