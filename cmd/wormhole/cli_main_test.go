package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// isolateConfig prevents resolution from the repository or the developer's
// global config. Enrolment lifecycle coverage lives in
// cli_main_join_socket_test.go; the removed legacy cases asserted direct
// Fabric registration, CLI credential writes, and Task 4 bootstrap behavior
// superseded by the Gateway-owned lifecycle.
func isolateConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "empty-config-home"))
}

func TestRun_NoArgs_PrintsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code: got %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "usage: wormhole") {
		t.Fatalf("stderr missing usage text: %q", stderr.String())
	}
}

func TestLinkedVersionAppearsInHelp(t *testing.T) {
	want := os.Getenv("WORMHOLE_EXPECT_LINKED_VERSION")
	if want == "" {
		want = "dev"
	}
	if version != want {
		t.Fatalf("linked version = %q, want %q", version, want)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("help exit code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "version: "+want) {
		t.Fatalf("help = %q, want linked version %q", stdout.String(), want)
	}
}

func TestRun_UnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"bogus"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code: got %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), `unknown command "bogus"`) {
		t.Fatalf("stderr missing unknown-command text: %q", stderr.String())
	}
}

func TestRun_WhoamiCommand_NoProfiles_PrintsError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := run([]string{"whoami"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit code: got 0, want non-zero")
	}
	if !strings.Contains(stderr.String(), "no stored credential profiles") {
		t.Fatalf("stderr: got %q", stderr.String())
	}
}

func TestRun_WhoamiCommand_SingleProfile_AutoSelects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".wormhole", "credentials")
	writeTestCredentials(t, dir, "proj-1__backend-engineer", credentials{
		ProjectID: "proj-1", Role: "backend-engineer", AgentID: "agent-1",
		IssuedAt: time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC),
	})

	var stdout, stderr bytes.Buffer
	code := run([]string{"whoami"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"proj-1__backend-engineer", "proj-1", "backend-engineer", "agent-1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q: got %q", want, out)
		}
	}
}

func TestRun_WhoamiCommand_MultipleProfiles_RequiresFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".wormhole", "credentials")
	issuedAt := time.Now()
	writeTestCredentials(t, dir, "proj-1__backend-engineer", credentials{ProjectID: "proj-1", Role: "backend-engineer", AgentID: "agent-1", IssuedAt: issuedAt})
	writeTestCredentials(t, dir, "proj-1__frontend-engineer", credentials{ProjectID: "proj-1", Role: "frontend-engineer", AgentID: "agent-2", IssuedAt: issuedAt})

	var stdout, stderr bytes.Buffer
	code := run([]string{"whoami"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit code: got 0, want non-zero (ambiguous profile)")
	}
	if !strings.Contains(stderr.String(), "--profile") {
		t.Fatalf("stderr should prompt for --profile: got %q", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"whoami", "--profile", "proj-1__frontend-engineer"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "agent-2") {
		t.Fatalf("stdout missing agent-2: got %q", stdout.String())
	}
}

func TestRun_WhoamiCommand_UnknownProfile_PrintsError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var stdout, stderr bytes.Buffer
	code := run([]string{"whoami", "--profile", "does-not-exist"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit code: got 0, want non-zero")
	}
}

func TestRun_ProfileListCommand_Empty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := run([]string{"profile", "list"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no stored credential profiles") {
		t.Fatalf("stdout: got %q", stdout.String())
	}
}

func TestRun_ProfileListCommand_ListsAll(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".wormhole", "credentials")
	issuedAt := time.Now()
	writeTestCredentials(t, dir, "proj-1__backend-engineer", credentials{ProjectID: "proj-1", Role: "backend-engineer", AgentID: "agent-1", IssuedAt: issuedAt})
	writeTestCredentials(t, dir, "proj-1__frontend-engineer", credentials{ProjectID: "proj-1", Role: "frontend-engineer", AgentID: "agent-2", IssuedAt: issuedAt})

	var stdout, stderr bytes.Buffer
	code := run([]string{"profile", "list"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"proj-1__backend-engineer", "proj-1__frontend-engineer", "agent-1", "agent-2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q: got %q", want, out)
		}
	}
}

func TestRun_ProfileCommand_UnknownSubcommand_PrintsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"profile", "bogus"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code: got %d, want 2", code)
	}
}
