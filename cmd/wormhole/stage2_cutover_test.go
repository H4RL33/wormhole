package main

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/H4RL33/wormhole/internal/runtime/localapi"
)

type recordingWorkspaceBackend struct {
	requests []localapi.WorkspaceCommandRequest
	result   localapi.WorkspaceCommandResult
}

func (b *recordingWorkspaceBackend) Execute(_ context.Context, request localapi.WorkspaceCommandRequest) (localapi.WorkspaceCommandResult, error) {
	b.requests = append(b.requests, request)
	b.result.Operation = request.Operation
	return b.result, nil
}

func TestStage2FinalTopLevelWorkspaceInventory(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("help exit = %d, stderr=%q", code, stderr.String())
	}
	help := stdout.String()
	commands := topLevelHelpCommands(help)
	for _, command := range []string{"status", "diff", "import", "checkpoint", "stash"} {
		if !commands[command] {
			t.Errorf("top-level help is missing %q", command)
		}
		stdout.Reset()
		stderr.Reset()
		if code := run([]string{command, "--help"}, &stdout, &stderr); code != 0 {
			t.Errorf("%s --help exit = %d, stderr=%q", command, code, stderr.String())
		}
	}
	for _, removed := range []string{"init", "join", "connect", "config"} {
		if commands[removed] {
			t.Errorf("top-level help retains removed command %q", removed)
		}
		stdout.Reset()
		stderr.Reset()
		if code := run([]string{removed}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "unknown command") {
			t.Errorf("removed command %q = exit %d, stderr=%q", removed, code, stderr.String())
		}
	}
	for _, forbidden := range []string{"code" + "-graph", "Code Graph", "code" + "_graph"} {
		if strings.Contains(help, forbidden) {
			t.Errorf("top-level help retains public graph text %q", forbidden)
		}
	}
}

func topLevelHelpCommands(help string) map[string]bool {
	commands := map[string]bool{}
	for _, line := range strings.Split(help, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "wormhole" {
			commands[fields[1]] = true
		}
	}
	return commands
}

func TestStage2WorkspaceCLIForwardsOnlyOperationArguments(t *testing.T) {
	backend := &recordingWorkspaceBackend{result: localapi.WorkspaceCommandResult{Operation: localapi.WorkspaceOperationStatus}}
	previous := workspaceBackendFactory
	workspaceBackendFactory = func() (workspaceCommandBackend, error) { return backend, nil }
	t.Cleanup(func() { workspaceBackendFactory = previous })

	commands := []struct {
		args []string
		want localapi.WorkspaceCommandRequest
	}{
		{args: []string{"status"}, want: localapi.WorkspaceCommandRequest{Operation: localapi.WorkspaceOperationStatus}},
		{args: []string{"diff"}, want: localapi.WorkspaceCommandRequest{Operation: localapi.WorkspaceOperationDiff}},
		{args: []string{"import"}, want: localapi.WorkspaceCommandRequest{Operation: localapi.WorkspaceOperationImport}},
		{args: []string{"checkpoint", "--publication-review-digest", "sha256:" + strings.Repeat("a", 64)}, want: localapi.WorkspaceCommandRequest{Operation: localapi.WorkspaceOperationCheckpoint, PublicationReviewDigest: "sha256:" + strings.Repeat("a", 64)}},
		{args: []string{"stash", "--request-id", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "--label", "pause work"}, want: localapi.WorkspaceCommandRequest{Operation: localapi.WorkspaceOperationStash, RequestID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Label: "pause work"}},
	}
	for _, command := range commands {
		var stdout, stderr bytes.Buffer
		if code := run(command.args, &stdout, &stderr); code != 0 {
			t.Fatalf("run(%v) exit = %d, stderr=%q", command.args, code, stderr.String())
		}
		got := backend.requests[len(backend.requests)-1]
		if !reflect.DeepEqual(got, command.want) {
			t.Fatalf("run(%v) request = %+v, want %+v", command.args, got, command.want)
		}
	}
}
