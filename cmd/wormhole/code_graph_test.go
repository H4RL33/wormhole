package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	codegraphconfig "github.com/H4RL33/wormhole/internal/runtime/codegraph/config"
	codegraphstore "github.com/H4RL33/wormhole/internal/runtime/codegraph/store"
	runtimeconfig "github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/localapi"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
)

func TestCodeGraphCommandsAreRegistered(t *testing.T) {
	commands := [][]string{
		{"config", "code-graph", "enable", "--help"},
		{"config", "code-graph", "disable", "--help"},
		{"config", "code-graph", "status", "--help"},
		{"config", "code-graph", "rebuild", "--help"},
		{"config", "code-graph", "checkout", "set", "--help"},
		{"config", "code-graph", "checkout", "show", "--help"},
	}
	for _, args := range commands {
		t.Run(strings.Join(args[:len(args)-1], " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(args, &stdout, &stderr); code != 0 {
				t.Fatalf("run(%v) = %d, stderr=%q", args, code, stderr.String())
			}
		})
	}
}

func TestCodeGraphNoninteractiveConfirmInvokesEnableAndDisable(t *testing.T) {
	var operations []localapi.CodeGraphLifecycleOperation
	call := func(_ context.Context, request localapi.CodeGraphLifecycleRequest) (localapi.CodeGraphLifecycleStatus, error) {
		operations = append(operations, request.Operation)
		return localapi.CodeGraphLifecycleStatus{ProjectID: request.ProjectID}, nil
	}
	for _, args := range [][]string{{"enable", "--project", "project-a", "--confirm"}, {"disable", "--project", "project-a", "--confirm"}} {
		var stdout, stderr bytes.Buffer
		if code := runConfigCodeGraph(args, strings.NewReader(""), &stdout, &stderr, false, call); code != 0 {
			t.Fatalf("runConfigCodeGraph(%v) = %d, stderr=%q", args, code, stderr.String())
		}
	}
	if len(operations) != 2 || operations[0] != localapi.CodeGraphEnable || operations[1] != localapi.CodeGraphDisable {
		t.Fatalf("operations = %v", operations)
	}
}

func TestCodeGraphCheckoutSetRequiresExactlyOnePath(t *testing.T) {
	called := false
	call := func(context.Context, localapi.CodeGraphLifecycleRequest) (localapi.CodeGraphLifecycleStatus, error) {
		called = true
		return localapi.CodeGraphLifecycleStatus{}, nil
	}
	for _, args := range [][]string{
		{"checkout", "set", "--project", "project-a", "--confirm"},
		{"checkout", "set", "--project", "project-a", "--confirm", "/one", "/two"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runConfigCodeGraph(args, strings.NewReader(""), &stdout, &stderr, false, call); code != 2 {
			t.Fatalf("runConfigCodeGraph(%v) = %d, want 2", args, code)
		}
	}
	if called {
		t.Fatal("invalid checkout operands invoked lifecycle")
	}
}

func TestCodeGraphDispatchesRebuildAndCheckoutSet(t *testing.T) {
	var requests []localapi.CodeGraphLifecycleRequest
	call := func(_ context.Context, request localapi.CodeGraphLifecycleRequest) (localapi.CodeGraphLifecycleStatus, error) {
		requests = append(requests, request)
		return localapi.CodeGraphLifecycleStatus{ProjectID: request.ProjectID}, nil
	}
	for _, args := range [][]string{
		{"rebuild", "--project", "project-a", "--confirm"},
		{"checkout", "set", "--project", "project-a", "--confirm", "/new-checkout"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runConfigCodeGraph(args, strings.NewReader(""), &stdout, &stderr, false, call); code != 0 {
			t.Fatalf("runConfigCodeGraph(%v) = %d, stderr=%q", args, code, stderr.String())
		}
	}
	if len(requests) != 2 || requests[0].Operation != localapi.CodeGraphRebuild ||
		requests[1].Operation != localapi.CodeGraphCheckoutSet || requests[1].Checkout != "/new-checkout" {
		t.Fatalf("requests = %+v", requests)
	}
}

func TestCodeGraphProjectDefaultsToNearestProjectConfig(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, ".wormhole")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("project = \"nearest-project\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(before) })
	call := func(_ context.Context, request localapi.CodeGraphLifecycleRequest) (localapi.CodeGraphLifecycleStatus, error) {
		if request.ProjectID != "nearest-project" {
			t.Fatalf("ProjectID = %q", request.ProjectID)
		}
		return localapi.CodeGraphLifecycleStatus{ProjectID: request.ProjectID}, nil
	}
	var stdout, stderr bytes.Buffer
	if code := runConfigCodeGraph([]string{"status"}, strings.NewReader(""), &stdout, &stderr, false, call); code != 0 {
		t.Fatalf("status exit = %d, stderr=%q", code, stderr.String())
	}
}

func TestCodeGraphMutationConfirmationAndResourceWarning(t *testing.T) {
	calls := 0
	call := func(_ context.Context, request localapi.CodeGraphLifecycleRequest) (localapi.CodeGraphLifecycleStatus, error) {
		calls++
		return localapi.CodeGraphLifecycleStatus{Enabled: true}, nil
	}

	var stdout, stderr bytes.Buffer
	if code := runConfigCodeGraph([]string{"enable", "--project", "project-a"}, strings.NewReader(""), &stdout, &stderr, false, call); code != 2 {
		t.Fatalf("noninteractive enable exit = %d, want 2", code)
	}
	if calls != 0 || !strings.Contains(stderr.String(), "--confirm") {
		t.Fatalf("noninteractive enable calls=%d stderr=%q", calls, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runConfigCodeGraph([]string{"enable", "--project", "project-a"}, strings.NewReader("yes\n"), &stdout, &stderr, true, call); code != 0 {
		t.Fatalf("interactive enable exit = %d, stderr=%q", code, stderr.String())
	}
	for _, phrase := range []string{"local", "experimental", "CPU", "memory", "disk", "I/O"} {
		if !strings.Contains(stdout.String(), phrase) {
			t.Errorf("interactive explanation missing %q: %q", phrase, stdout.String())
		}
	}

	stdout.Reset()
	stderr.Reset()
	if code := runConfigCodeGraph([]string{"disable", "--project", "project-a"}, strings.NewReader("no\n"), &stdout, &stderr, true, call); code != 1 {
		t.Fatalf("declined disable exit = %d, want 1", code)
	}
	for _, phrase := range []string{"completed revisions", "candidate revisions", "nodes", "files", "symbols", "edges", "diagnostics", "project Code Graph configuration", "Git", "working tree"} {
		if !strings.Contains(stdout.String(), phrase) {
			t.Errorf("disable warning missing %q: %q", phrase, stdout.String())
		}
	}
	if calls != 1 {
		t.Fatalf("disable confirmation calls=%d stdout=%q", calls, stdout.String())
	}
}

func TestCodeGraphStatusAndCheckoutShowDoNotRequireConfirmation(t *testing.T) {
	calls := 0
	call := func(_ context.Context, request localapi.CodeGraphLifecycleRequest) (localapi.CodeGraphLifecycleStatus, error) {
		calls++
		return localapi.CodeGraphLifecycleStatus{ProjectID: request.ProjectID, ActiveCheckout: "/repo"}, nil
	}
	for _, args := range [][]string{{"status", "--project", "project-a"}, {"checkout", "show", "--project", "project-a"}} {
		var stdout, stderr bytes.Buffer
		if code := runConfigCodeGraph(args, strings.NewReader(""), &stdout, &stderr, false, call); code != 0 {
			t.Fatalf("runConfigCodeGraph(%v) = %d, stderr=%q", args, code, stderr.String())
		}
	}
	if calls != 2 {
		t.Fatalf("read-only lifecycle calls = %d, want 2", calls)
	}
}

func TestExecuteCodeGraphLocalStatusShowAndDisableRequireNoCredentials(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "run"))
	paths, err := runtimeconfig.ResolveRuntimePaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.DBPath), 0o700); err != nil {
		t.Fatal(err)
	}
	local, err := localstore.Open(paths.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := codegraphstore.Open(context.Background(), local.DB(), "project-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := graph.PutProjectConfig(context.Background(), codegraphconfig.Project{
		ProjectID: "project-a", Enabled: true, CanonicalRemote: "https://example.invalid/repo.git",
		ActiveCheckout: "/checkout", ProjectSourceByteCeiling: codegraphconfig.DefaultProjectSourceByteCeiling,
	}); err != nil {
		t.Fatal(err)
	}
	if err := graph.CreateCandidate(context.Background(), codegraphstore.Revision{
		ProjectID: "project-a", ID: "candidate", IndexedCommit: strings.Repeat("a", 40), CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []localapi.CodeGraphLifecycleOperation{localapi.CodeGraphStatus, localapi.CodeGraphCheckoutShow} {
		status, err := executeCodeGraphLifecycle(context.Background(), localapi.CodeGraphLifecycleRequest{Operation: operation, ProjectID: "project-a"})
		if err != nil {
			t.Fatalf("%s without credentials = %v", operation, err)
		}
		if !status.Enabled || status.ActiveCheckout != "/checkout" {
			t.Fatalf("%s status = %+v", operation, status)
		}
	}
	if _, err := executeCodeGraphLifecycle(context.Background(), localapi.CodeGraphLifecycleRequest{Operation: localapi.CodeGraphDisable, ProjectID: "project-a"}); err != nil {
		t.Fatalf("disable without credentials = %v", err)
	}
	local, err = localstore.Open(paths.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	for _, table := range []string{"codegraph_config", "codegraph_revisions"} {
		var rows int
		if err := local.DB().QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE project_id = 'project-a'`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 0 {
			t.Fatalf("%s rows after credential-free disable = %d", table, rows)
		}
	}
}

func TestCodeGraphRejectsForbiddenKnobs(t *testing.T) {
	call := func(context.Context, localapi.CodeGraphLifecycleRequest) (localapi.CodeGraphLifecycleStatus, error) {
		return localapi.CodeGraphLifecycleStatus{}, errors.New("must not be called")
	}
	for _, args := range [][]string{
		{"rebuild", "--project", "project-a", "--confirm", "--warpspeed"},
		{"rebuild", "--project", "project-a", "--confirm", "--in-place"},
		{"pause", "--project", "project-a"},
		{"resume", "--project", "project-a"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runConfigCodeGraph(args, strings.NewReader(""), &stdout, &stderr, false, call); code != 2 {
			t.Fatalf("forbidden args %v exit = %d, want 2", args, code)
		}
	}
}
