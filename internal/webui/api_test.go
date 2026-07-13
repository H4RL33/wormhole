package webui

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	_ "github.com/lib/pq"

	"github.com/H4RL33/wormhole/internal/core/events"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/core/kb"
	"github.com/H4RL33/wormhole/internal/core/tasks"
	"github.com/H4RL33/wormhole/internal/types"
)

// testDB opens an independent Postgres connection, skipping the test if
// Postgres is not reachable — mirrors internal/mcp/server_test.go's testDB
// and identity_test.go's testStore skip pattern (T1: real Postgres, no
// mocking).
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	cfg := types.LoadConfig()
	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		if os.Getenv("WORMHOLE_INTEGRATION_REQUIRED") == "1" {
			t.Fatalf("postgres required but not reachable: %v", err)
		}
		t.Skipf("postgres not reachable (%v); run `docker compose up -d db` and apply migrations before running this test", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// mustCreateProject inserts a project directly, mirroring
// internal/mcp/agent_test.go's mustCreateProject.
func mustCreateProject(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`INSERT INTO projects (name, owner) VALUES ($1, $2) RETURNING id`, name, "harley").Scan(&id); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM projects WHERE id = $1`, id); err != nil {
			t.Logf("cleanup: delete project %s: %v", id, err)
		}
	})
	return id
}

// mustRegisterAgent registers an agent (with passport) in projectID,
// mirroring internal/mcp/channel_test.go's mustRegisterAgent.
func mustRegisterAgent(t *testing.T, identityStore *identity.Store, projectID string) (agentID string) {
	t.Helper()
	agent, _, _, err := identityStore.Register(context.Background(), projectID, []string{"event.publish"}, "harley", "claude", nil, nil, nil)
	if err != nil {
		t.Fatalf("register agent: %v", err)
	}
	return agent.ID
}

// TestDashboardAPI seeds a project directly via the core stores (mirroring
// internal/mcp's integration test seeding style, e.g. m3_integration_test.go),
// issues a viewer key for it, and asserts all three GET routes reflect the
// seeded state when hit through a real HTTP server via the viewer key.
func TestDashboardAPI(t *testing.T) {
	db := testDB(t)
	identityStore := identity.NewStore(db)
	eventsStore := events.NewStore(db)
	tasksStore := tasks.NewStore(db, eventsStore)
	kbStore := kb.NewStore(db, kb.StubEmbedder{}, 0.85, 4000, 1, 1, 1)

	projectID := mustCreateProject(t, db, "webui-dashboard-project")
	agentID := mustRegisterAgent(t, identityStore, projectID)

	task, err := tasksStore.Create(context.Background(), projectID, "seed task", "seed description", nil, 1, nil)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	channel, err := eventsStore.CreateChannel(context.Background(), projectID, "seed-channel")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	seedNote := "hello"
	event, err := eventsStore.PublishEvent(context.Background(), projectID, channel.ID, agentID, "message.posted", json.RawMessage(`{"text":"hello"}`), &seedNote)
	if err != nil {
		t.Fatalf("publish event: %v", err)
	}

	article, err := kbStore.WriteArticle(context.Background(), projectID, agentID, "seed article", "seed body", json.RawMessage(`{}`), nil, false)
	if err != nil {
		t.Fatalf("write article: %v", err)
	}

	rawKey, _, err := identityStore.CreateViewerKey(context.Background(), projectID, "dashboard viewer")
	if err != nil {
		t.Fatalf("create viewer key: %v", err)
	}

	otherProjectID := mustCreateProject(t, db, "webui-other-project")
	otherRawKey, _, err := identityStore.CreateViewerKey(context.Background(), otherProjectID, "other viewer")
	if err != nil {
		t.Fatalf("create viewer key for other project: %v", err)
	}

	h := &Handler{Identity: identityStore, Tasks: tasksStore, Events: eventsStore, KB: kbStore}
	srv := httptest.NewServer(h.NewMux())
	defer srv.Close()

	get := func(path, bearer string) (*http.Response, []byte) {
		req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		if bearer != "" {
			req.Header.Set("Authorization", bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do request: %v", err)
		}
		defer resp.Body.Close()
		var buf []byte
		buf = make([]byte, 0, 4096)
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

	t.Run("tasks happy path", func(t *testing.T) {
		resp, body := get("/dashboard/api/projects/"+projectID+"/tasks", "Bearer "+rawKey)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: got %d, want 200, body: %s", resp.StatusCode, body)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
			t.Fatalf("content-type: got %q", ct)
		}
		var got []tasks.Task
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode: %v, body: %s", err, body)
		}
		if len(got) != 1 || got[0].ID != task.ID {
			t.Fatalf("tasks: got %+v, want single task %s", got, task.ID)
		}
	})

	t.Run("events happy path", func(t *testing.T) {
		resp, body := get("/dashboard/api/projects/"+projectID+"/events", "Bearer "+rawKey)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: got %d, want 200, body: %s", resp.StatusCode, body)
		}
		var got []events.Event
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode: %v, body: %s", err, body)
		}
		if len(got) != 1 || got[0].ID != event.ID {
			t.Fatalf("events: got %+v, want single event %s", got, event.ID)
		}
	})

	t.Run("kb happy path", func(t *testing.T) {
		resp, body := get("/dashboard/api/projects/"+projectID+"/kb", "Bearer "+rawKey)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: got %d, want 200, body: %s", resp.StatusCode, body)
		}
		var got []kb.Article
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode: %v, body: %s", err, body)
		}
		if len(got) != 1 || got[0].ID != article.ID {
			t.Fatalf("kb articles: got %+v, want single article %s", got, article.ID)
		}
	})

	t.Run("missing auth header", func(t *testing.T) {
		resp, body := get("/dashboard/api/projects/"+projectID+"/tasks", "")
		assertForbidden(t, resp, body)
	})

	t.Run("malformed auth header", func(t *testing.T) {
		resp, body := get("/dashboard/api/projects/"+projectID+"/tasks", "not-a-bearer-token")
		assertForbidden(t, resp, body)
	})

	t.Run("garbage viewer key", func(t *testing.T) {
		resp, body := get("/dashboard/api/projects/"+projectID+"/tasks", "Bearer garbage-key-not-real")
		assertForbidden(t, resp, body)
	})

	t.Run("wrong project viewer key", func(t *testing.T) {
		resp, body := get("/dashboard/api/projects/"+projectID+"/tasks", "Bearer "+otherRawKey)
		assertForbidden(t, resp, body)
	})

	t.Run("empty results serialize as empty array not null", func(t *testing.T) {
		emptyProjectID := mustCreateProject(t, db, "webui-empty-project")
		emptyRawKey, _, err := identityStore.CreateViewerKey(context.Background(), emptyProjectID, "empty viewer")
		if err != nil {
			t.Fatalf("create viewer key: %v", err)
		}

		for _, path := range []string{"tasks", "events", "kb"} {
			resp, body := get("/dashboard/api/projects/"+emptyProjectID+"/"+path, "Bearer "+emptyRawKey)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s status: got %d, want 200, body: %s", path, resp.StatusCode, body)
			}
			trimmed := string(body)
			for len(trimmed) > 0 && (trimmed[len(trimmed)-1] == '\n' || trimmed[len(trimmed)-1] == '\r') {
				trimmed = trimmed[:len(trimmed)-1]
			}
			if trimmed != "[]" {
				t.Fatalf("%s empty-result body: got %q, want %q", path, trimmed, "[]")
			}
		}
	})
}

func assertForbidden(t *testing.T, resp *http.Response, body []byte) {
	t.Helper()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status: got %d, want 403, body: %s", resp.StatusCode, body)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &errBody); err != nil {
		t.Fatalf("decode error body: %v, body: %s", err, body)
	}
	if errBody.Error == "" {
		t.Fatalf("error body missing message: %s", body)
	}
}
