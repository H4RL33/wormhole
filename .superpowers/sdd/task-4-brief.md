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

