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
