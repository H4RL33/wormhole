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
		var body struct {
			Label string `json:"label"`
		}
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
