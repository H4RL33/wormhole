# Viewer Key Issuance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a way to mint dashboard viewer keys — a `POST /dashboard/api/projects/{id}/viewer-keys` endpoint on `wormhole-server`, a `wormhole-cli viewer-key create` CLI command that calls it, and README docs for both.

**Architecture:** A new `internal/webui/admin.go` handler wraps the existing `identity.Store.CreateViewerKey`, gated by a shared operator secret (`Config.AdminKey` / `WORMHOLE_ADMIN_KEY`) compared with `crypto/subtle.ConstantTimeCompare`, registered on the same mux `NewMux` already returns. `cmd/wormhole-cli/viewer_key.go` adds a CLI subcommand that POSTs to it.

**Tech Stack:** Go stdlib (`net/http`, `crypto/subtle`, `encoding/json`), existing `internal/core/identity` package, existing `cmd/wormhole-cli` flag/dispatch pattern.

## Global Constraints

- Doc-only wording changes aside, this touches runtime code — every task must leave `go build ./... && go vet ./...` clean.
- Real-Postgres integration tests only (CONTRIBUTING.md: no mocking `database/sql`), following the existing `testDB`/`mustCreateProject`/`mustRegisterAgent` helpers already in `internal/webui/api_test.go` (same package, reusable as-is — do not duplicate them).
- `AdminKey` empty (unset) must fail closed: 503, never a default/fallback secret.
- 403 response body is `{"error": "invalid admin key"}` for both missing and wrong header — no distinguishing failure modes (matches `withViewerAuth`'s existing side-channel-neutral convention in `internal/webui/api.go`).
- The raw viewer key is returned exactly once in the response body — never logged, never persisted anywhere but its SHA-256 hash (already handled by `CreateViewerKey`; just don't log the response anywhere new).
- This is not an MCP tool and must not touch `AuthenticatedScope` or `internal/mcp` — it's a REST admin endpoint under `/dashboard`, same carve-out as the existing read-only routes.
- Do not implement viewer-key revocation/listing — issuance only (spec's explicit out-of-scope).

---

### Task 1: Config — `AdminKey` field

**Files:**
- Modify: `internal/types/config.go`
- Test: `internal/types/config_test.go` (new file — none exists yet for this package)

**Interfaces:**
- Produces: `types.Config.AdminKey string` — read by `internal/webui.Handler.AdminKey` (Task 2) and set from `cmd/wormhole-server/main.go` (Task 3).

- [ ] **Step 1: Write the failing test**

Create `internal/types/config_test.go`:

```go
package types

import (
	"os"
	"testing"
)

func TestLoadConfig_AdminKey(t *testing.T) {
	t.Run("unset by default", func(t *testing.T) {
		os.Unsetenv("WORMHOLE_ADMIN_KEY")
		cfg := LoadConfig()
		if cfg.AdminKey != "" {
			t.Fatalf("AdminKey: got %q, want empty when WORMHOLE_ADMIN_KEY is unset", cfg.AdminKey)
		}
	})

	t.Run("read from env", func(t *testing.T) {
		t.Setenv("WORMHOLE_ADMIN_KEY", "test-secret-123")
		cfg := LoadConfig()
		if cfg.AdminKey != "test-secret-123" {
			t.Fatalf("AdminKey: got %q, want %q", cfg.AdminKey, "test-secret-123")
		}
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/types/... -run TestLoadConfig_AdminKey -v`
Expected: FAIL — `cfg.AdminKey` doesn't compile (`Config` has no field `AdminKey`).

- [ ] **Step 3: Implement**

In `internal/types/config.go`, add the field to the `Config` struct and load it in `LoadConfig`:

```go
type Config struct {
	ListenAddr          string
	DatabaseURL         string
	KBDedupThreshold    float64
	KBMaxBodyLength     int
	KBMinLinksDecision  int
	KBMinLinksPolicy    int
	KBMinLinksProcedure int
	AdminKey            string
}
```

In `LoadConfig`'s returned struct literal, add:

```go
		AdminKey:            getEnv("WORMHOLE_ADMIN_KEY", ""),
```

(placed after `KBMinLinksProcedure` to match struct field order).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/types/... -run TestLoadConfig_AdminKey -v`
Expected: PASS (both subtests)

- [ ] **Step 5: Commit**

```bash
git add internal/types/config.go internal/types/config_test.go
git commit -m "feat(config): add AdminKey, loaded from WORMHOLE_ADMIN_KEY"
```

---

### Task 2: `internal/webui` — viewer-key issuance endpoint

**Files:**
- Modify: `internal/webui/api.go` (add `AdminKey` field to `Handler`, register the new route in `NewMux`)
- Create: `internal/webui/admin.go`
- Create: `internal/webui/admin_test.go`

**Interfaces:**
- Consumes: `types.Config.AdminKey` (Task 1, wired in by whoever constructs `Handler` — Task 3); `identity.Store.CreateViewerKey(ctx, projectID, label string) (rawKey, id string, err error)` (existing, `internal/core/identity/viewer_keys.go:21`); `writeJSON(w, v any)` / `writeJSONError(w, status int, message string)` (existing helpers in `api.go`).
- Produces: `Handler.AdminKey string` field; `POST /dashboard/api/projects/{id}/viewer-keys` route, registered inside `NewMux`.

- [ ] **Step 1: Write the failing test**

Create `internal/webui/admin_test.go` (same package as `api_test.go`, reuses its `testDB`/`mustCreateProject` helpers — do not redefine them):

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/webui/... -run TestCreateViewerKey -v`
Expected: FAIL to compile — `Handler` has no field `AdminKey`, and the route doesn't exist (404 on POST).

- [ ] **Step 3: Implement**

In `internal/webui/api.go`, add the field to `Handler`:

```go
// Handler serves the read-only dashboard API.
type Handler struct {
	Identity *identity.Store
	Tasks    *tasks.Store
	Events   *events.Store
	KB       *kb.Store
	AdminKey string
}
```

In `NewMux`, register the new route after the existing three:

```go
func (h *Handler) NewMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /dashboard/", serveIndex)
	mux.HandleFunc("GET /dashboard/api/projects/{id}/tasks", h.withViewerAuth(h.listTasks))
	mux.HandleFunc("GET /dashboard/api/projects/{id}/events", h.withViewerAuth(h.listEvents))
	mux.HandleFunc("GET /dashboard/api/projects/{id}/kb", h.withViewerAuth(h.listKB))
	mux.HandleFunc("POST /dashboard/api/projects/{id}/viewer-keys", h.withAdminKey(h.createViewerKey))
	return mux
}
```

Create `internal/webui/admin.go`:

```go
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
```

Note: `writeJSON` (`internal/webui/api.go:93-96`) only sets `Content-Type` and encodes — it never calls `w.WriteHeader` itself. Calling `w.WriteHeader(http.StatusCreated)` before `writeJSON(w, ...)`, as shown above, is safe and sets the 201 with no duplicate `WriteHeader` call.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/webui/... -run TestCreateViewerKey -v`
Expected: PASS (all 5 subtests). If Postgres isn't running, this will `Skip` — run `docker compose up -d db` and apply migrations first (see README Quickstart steps 1-2) if you need to actually execute it locally rather than trust a clean compile.

- [ ] **Step 5: Run full webui package tests to check no regression**

Run: `go test ./internal/webui/... -v`
Expected: PASS, including the pre-existing `TestDashboardAPI`.

- [ ] **Step 6: Commit**

```bash
git add internal/webui/api.go internal/webui/admin.go internal/webui/admin_test.go
git commit -m "feat(webui): add POST /dashboard/api/projects/{id}/viewer-keys

Gated by a shared operator secret (Handler.AdminKey) as a deliberate
stopgap ahead of real human auth (issues #22, #23)."
```

---

### Task 3: Wire `AdminKey` into `wormhole-server`

**Files:**
- Modify: `cmd/wormhole-server/main.go`

**Interfaces:**
- Consumes: `cfg.AdminKey` (Task 1), `webui.Handler.AdminKey` (Task 2).

- [ ] **Step 1: Implement**

In `cmd/wormhole-server/main.go`, find:

```go
	webuiHandler := &webui.Handler{
		Identity: identityStore,
		Tasks:    tasksStore,
		Events:   eventsStore,
		KB:       kbStore,
	}
```

and add the new field:

```go
	webuiHandler := &webui.Handler{
		Identity: identityStore,
		Tasks:    tasksStore,
		Events:   eventsStore,
		KB:       kbStore,
		AdminKey: cfg.AdminKey,
	}
```

This is a one-line change with no new testable behavior of its own (Task 2's tests already cover the `Handler` wiring; this task only threads the config value through in `main`). `cmd/wormhole-server` has no `main_test.go` — don't add one just for a single field assignment, per YAGNI. `cmd/wormhole-server/dashboard_test.go` and `m3_integration_test.go` construct their own `webui.Handler` literals directly and are unaffected by this change.

- [ ] **Step 2: Verify it builds**

Run: `go build ./... && go vet ./...`
Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add cmd/wormhole-server/main.go
git commit -m "feat(server): wire cfg.AdminKey into webui.Handler"
```

---

### Task 4: `wormhole-cli viewer-key create`

**Files:**
- Create: `cmd/wormhole-cli/viewer_key.go`
- Create: `cmd/wormhole-cli/viewer_key_test.go`
- Modify: `cmd/wormhole-cli/main.go` (dispatch + usage text)

**Interfaces:**
- Consumes: `POST /dashboard/api/projects/{id}/viewer-keys` (Task 2/3) — request `{"label": string}` with `X-Admin-Key` header, response `{"id","project_id","label","viewer_key"}` on 201, `{"error": string}` on non-2xx.
- Produces: `runViewerKeyCreate(args []string, stdout, stderr io.Writer) int`, dispatched from `run`'s switch in `main.go` on `args[0] == "viewer-key"` with subcommand `"create"`.

- [ ] **Step 1: Write the failing test**

Create `cmd/wormhole-cli/viewer_key_test.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestRunViewerKeyCreate_MissingRequiredFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runViewerKeyCreate([]string{}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code: got %d, want 2", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("--server")) {
		t.Fatalf("stderr should mention --server, got: %s", stderr.String())
	}
}

func TestRunViewerKeyCreate_NoAdminKeyAnywhere(t *testing.T) {
	os.Unsetenv("WORMHOLE_ADMIN_KEY")
	var stdout, stderr bytes.Buffer
	code := runViewerKeyCreate([]string{
		"--server", "http://example.invalid",
		"--project", "proj-1",
		"--label", "test viewer",
	}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code: got %d, want 2 (no admin key from flag or env)", code)
	}
}

func TestRunViewerKeyCreate_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/dashboard/api/projects/proj-1/viewer-keys" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-Admin-Key"); got != "sekrit" {
			t.Fatalf("X-Admin-Key: got %q, want %q", got, "sekrit")
		}
		var body struct{ Label string `json:"label"` }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.Label != "test viewer" {
			t.Fatalf("label: got %q, want %q", body.Label, "test viewer")
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{
			"id": "vk-1", "project_id": "proj-1", "label": body.Label, "viewer_key": "raw-key-abc",
		})
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := runViewerKeyCreate([]string{
		"--server", srv.URL,
		"--project", "proj-1",
		"--label", "test viewer",
		"--admin-key", "sekrit",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %s", code, stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte("raw-key-abc")) {
		t.Fatalf("stdout should contain the raw viewer key, got: %s", stdout.String())
	}
}

func TestRunViewerKeyCreate_AdminKeyFromEnv(t *testing.T) {
	t.Setenv("WORMHOLE_ADMIN_KEY", "env-secret")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Admin-Key"); got != "env-secret" {
			t.Fatalf("X-Admin-Key: got %q, want %q", got, "env-secret")
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{
			"id": "vk-2", "project_id": "proj-1", "label": "x", "viewer_key": "raw-key-xyz",
		})
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := runViewerKeyCreate([]string{
		"--server", srv.URL,
		"--project", "proj-1",
		"--label", "x",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %s", code, stderr.String())
	}
}

func TestRunViewerKeyCreate_ServerError_PrintsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid admin key"})
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := runViewerKeyCreate([]string{
		"--server", srv.URL,
		"--project", "proj-1",
		"--label", "x",
		"--admin-key", "wrong",
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code: got %d, want 1", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("invalid admin key")) {
		t.Fatalf("stderr should contain server error message, got: %s", stderr.String())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/wormhole-cli/... -run TestRunViewerKeyCreate -v`
Expected: FAIL to compile — `runViewerKeyCreate` doesn't exist yet.

- [ ] **Step 3: Implement**

Create `cmd/wormhole-cli/viewer_key.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
)

// createViewerKeyRequest/Response mirror internal/webui/admin.go's wire
// shapes for POST /dashboard/api/projects/{id}/viewer-keys.
type createViewerKeyRequest struct {
	Label string `json:"label"`
}

type createViewerKeyResponse struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Label     string `json:"label"`
	ViewerKey string `json:"viewer_key"`
}

// runViewerKeyCreate implements `wormhole-cli viewer-key create`: it POSTs
// to wormhole-server's admin-gated viewer-key endpoint and prints the raw
// key once. There is no MCP tool for this (RFC-0001 §14's dashboard is a
// REST-only carve-out) and no agent token is involved — auth is a shared
// operator secret (WORMHOLE_ADMIN_KEY), a deliberate stopgap (issue #23)
// ahead of real human identity/auth (issue #22).
func runViewerKeyCreate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("viewer-key create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "Wormhole server base URL (required)")
	project := fs.String("project", "", "project ID to issue the viewer key for (required)")
	label := fs.String("label", "", "human-readable label for this viewer key (required)")
	adminKey := fs.String("admin-key", "", "dashboard admin key (default: $WORMHOLE_ADMIN_KEY)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *server == "" || *project == "" || *label == "" {
		fmt.Fprintln(stderr, "wormhole viewer-key create: --server, --project, and --label are required")
		fs.Usage()
		return 2
	}

	key := *adminKey
	if key == "" {
		key = os.Getenv("WORMHOLE_ADMIN_KEY")
	}
	if key == "" {
		fmt.Fprintln(stderr, "wormhole viewer-key create: no admin key: pass --admin-key or set $WORMHOLE_ADMIN_KEY")
		return 2
	}

	reqBody, err := json.Marshal(createViewerKeyRequest{Label: *label})
	if err != nil {
		fmt.Fprintf(stderr, "wormhole viewer-key create: %v\n", err)
		return 1
	}

	url := *server + "/dashboard/api/projects/" + *project + "/viewer-keys"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		fmt.Fprintf(stderr, "wormhole viewer-key create: %v\n", err)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Key", key)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "wormhole viewer-key create: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		var errBody struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&errBody)
		if errBody.Error != "" {
			fmt.Fprintf(stderr, "wormhole viewer-key create: server: %s\n", errBody.Error)
		} else {
			fmt.Fprintf(stderr, "wormhole viewer-key create: server returned status %d\n", resp.StatusCode)
		}
		return 1
	}

	var out createViewerKeyResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		fmt.Fprintf(stderr, "wormhole viewer-key create: decode response: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Viewer key created (id=%s, project=%s).\n", out.ID, out.ProjectID)
	fmt.Fprintf(stdout, "viewer_key=%s\n", out.ViewerKey)
	fmt.Fprintln(stdout, "This key is shown once. Give it to the human who will use the dashboard,")
	fmt.Fprintln(stdout, "as the Authorization: Bearer value at /dashboard/.")
	return 0
}
```

In `cmd/wormhole-cli/main.go`, add dispatch in `run`'s switch statement:

```go
	case "viewer-key":
		if len(args) < 2 || args[1] != "create" {
			fmt.Fprintln(stderr, "wormhole viewer-key: only \"create\" is supported\n\nusage: wormhole viewer-key create [flags]")
			return 2
		}
		return runViewerKeyCreate(args[2:], stdout, stderr)
```

placed as a new `case` alongside the existing `"join"`, `"connect"`, `"whoami"`, `"profile"` cases. Update `usage()`'s returned string to add a line:

```
  viewer-key create   mint a dashboard viewer key for a project (requires an admin key)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/wormhole-cli/... -run TestRunViewerKeyCreate -v`
Expected: PASS (all 5 tests).

- [ ] **Step 5: Run full CLI package tests to check no regression**

Run: `go test ./cmd/wormhole-cli/... -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/wormhole-cli/viewer_key.go cmd/wormhole-cli/viewer_key_test.go cmd/wormhole-cli/main.go
git commit -m "feat(cli): add wormhole-cli viewer-key create"
```

---

### Task 5: README — document `WORMHOLE_ADMIN_KEY` setup and viewer-key issuance

**Files:**
- Modify: `README.md`

**Interfaces:**
- None (doc-only; references Task 1's `WORMHOLE_ADMIN_KEY` env var and Task 4's `wormhole-cli viewer-key create` command by their final names/flags).

- [ ] **Step 1: Implement**

In README.md's existing `## Human Dashboard` section (added by a prior doc-sync pass), replace the paragraph that currently reads:

> There is no CLI command to mint a viewer key yet — `identity.Store.CreateViewerKey`
> (`internal/core/identity/viewer_keys.go`) is the only way to issue one today,
> via a direct Go call or a `psql` insert into the `viewer_keys` table using the
> SHA-256 hex hash of your chosen key (the table stores `key_hash`, never the
> raw key — the same hashing `CreateViewerKey` does).

with:

```markdown
To issue a viewer key, `wormhole-server` needs an admin key configured:

```bash
export WORMHOLE_ADMIN_KEY="choose-a-long-random-secret"
```

Set this before starting `wormhole-server` (step 4 above) — it's read once at
startup. With that set, mint a viewer key:

```bash
wormhole-cli viewer-key create \
  --server http://localhost:8080 \
  --project 00000000-0000-0000-0000-000000000001 \
  --label "harley's laptop"
```

`--admin-key` can be passed explicitly instead of `$WORMHOLE_ADMIN_KEY` if the
CLI is running somewhere that doesn't share the server's environment. The
command prints the raw key once — give it to the human who'll use the
dashboard, as their `Authorization: Bearer <key>` value:

```bash
curl -H "Authorization: Bearer <viewer_key>" \
  http://localhost:8080/dashboard/api/projects/00000000-0000-0000-0000-000000000001/tasks
```

This admin-key gate is a deliberate stopgap, not real human authentication —
there's no per-human identity or audit trail yet (tracked separately).
```

Do not touch any other section of README.md.

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs(readme): document WORMHOLE_ADMIN_KEY and wormhole-cli viewer-key create"
```

---

### Task 6: Full-suite verification

**Files:** none (verification-only task).

- [ ] **Step 1: Run full build/vet/test**

```bash
go build ./... && go vet ./... && go test ./...
```

Expected: all packages `ok` (a pre-existing, known-flaky `TestWriteArticle_CrossProjectIsolation` DB-concurrency test may intermittently fail — if it does, re-run just that package with `-count=3` to confirm it's the known flake, not a regression, before treating the run as green).

- [ ] **Step 2: No commit needed for this task** — it's a verification gate, not a code change. If it fails for a reason other than the known flake, that's a signal to fix the responsible task before moving on, not to patch here.
