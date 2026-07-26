package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localapi"
)

func TestRunConnectRejectsUnresolvedInputsAndUnsafeProfile(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "server", args: []string{"--project", "project-a", "--owner", "owner"}, want: "server:"},
		{name: "project", args: []string{"--server", "https://fabric.example", "--owner", "owner"}, want: "project:"},
		{name: "profile", args: []string{"--server", "https://fabric.example", "--project", "project-a", "--owner", "owner", "--profile", "../escape"}, want: "--profile:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateConfig(t)
			var stdout, stderr bytes.Buffer
			if code := runConnect(tt.args, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), tt.want) {
				t.Fatalf("runConnect code=%d stderr=%q, want validation %q", code, stderr.String(), tt.want)
			}
		})
	}
}

func TestRunJoinCoversResolutionDefaultsRoleMergeAndGatewayFailure(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{name: "server", args: []string{"--project", "project-a", "--owner", "owner"}, want: "server:"},
		{name: "project", args: []string{"--server", "https://fabric.example", "--owner", "owner"}, want: "project:"},
		{name: "profile", args: []string{"--server", "https://fabric.example", "--project", "project-a", "--owner", "owner", "--profile", "../escape"}, want: "--profile:"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			isolateConfig(t)
			var stdout, stderr bytes.Buffer
			if code := runJoin(tt.args, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), tt.want) {
				t.Fatalf("runJoin code=%d stderr=%q, want %q", code, stderr.String(), tt.want)
			}
		})
	}

	isolateConfig(t)
	failed := localapi.EnrolmentResult{
		Version: localapi.EnrolmentProtocolVersion, Code: localapi.EnrolmentPermissionsRejected,
		State: localapi.EnrolmentFailed, Message: "role denied",
	}
	received := fakeEnrolmentGateway(t, failed)
	var stdout, stderr bytes.Buffer
	code := runJoin([]string{
		"--server", "https://fabric.example", "--project", "project-a", "--owner", "owner",
		"--role", "reviewer", "--roles", "contributor", "--capabilities", "review,code",
	}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "permissions_rejected: role denied") {
		t.Fatalf("runJoin code=%d stderr=%q", code, stderr.String())
	}
	request := <-received
	if request.CredentialProfile != "project-a__reviewer" || strings.Join(request.Roles, ",") != "contributor,reviewer" || request.RequestedPermissions == nil {
		t.Fatalf("role-merged request = %+v", request)
	}
}

func TestDoRegisterViaSocketValidatesRemainingGatewayResponseBoundaries(t *testing.T) {
	okInit := `{"jsonrpc":"2.0","id":1,"result":{}}`
	tests := []struct {
		name string
		call string
		want string
	}{
		{name: "call read", call: "close-after-call", want: "read tools/call response"},
		{name: "call malformed", call: "not-json", want: "decode tools/call response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := gatewayTransportSocket(t, okInit, tt.call)
			_, reachable, err := doRegisterViaSocket(path, "project-a", registerAgentInput{})
			if !reachable || err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("doRegisterViaSocket reachable=%v error=%v, want %q", reachable, err, tt.want)
			}
		})
	}

	if _, reachable, err := doRegisterViaSocket(filepath.Join(t.TempDir(), "missing.sock"), "project-a", registerAgentInput{}); reachable || err != nil {
		t.Fatalf("unreachable Gateway = reachable %v, err %v", reachable, err)
	}
}

func TestRunConnectBuildsDefaultEmptyRegistrationAndReportsGatewayFailure(t *testing.T) {
	isolateConfig(t)
	failed := localapi.EnrolmentResult{
		Version: localapi.EnrolmentProtocolVersion, Code: localapi.EnrolmentInvalidProject,
		State: localapi.EnrolmentFailed, Message: "unknown project",
	}
	received := fakeEnrolmentGateway(t, failed)
	var stdout, stderr bytes.Buffer
	code := runConnect([]string{"--server", "https://fabric.example", "--project", "project-a", "--owner", "owner"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "invalid_project: unknown project") {
		t.Fatalf("runConnect code=%d stderr=%q", code, stderr.String())
	}
	request := <-received
	if request.CredentialProfile != "project-a__default" || request.RequestedPermissions == nil || len(request.RequestedPermissions) != 0 {
		t.Fatalf("default request = %+v", request)
	}
	if request.Capabilities != nil || request.Repositories != nil || request.Roles != nil {
		t.Fatalf("optional empty request fields = %+v, want nil", request)
	}
}

func TestRunConnectExplicitTargetReportsMissingHarnessExecutables(t *testing.T) {
	for _, target := range []string{"claude", "opencode"} {
		t.Run("missing stdio "+target, func(t *testing.T) {
			fakeEnrolmentGateway(t, persistedEnrolmentResult())
			var stdout, stderr bytes.Buffer
			missing := filepath.Join(t.TempDir(), "missing-wormhole")
			code := runConnect([]string{
				"--server", "https://fabric.example", "--project", "project-a", "--owner", "owner",
				"--target", target, "--stdio-bin", missing,
			}, &stdout, &stderr)
			if code != 1 || !strings.Contains(stderr.String(), "not found in PATH") {
				t.Fatalf("runConnect target=%s code=%d stderr=%q", target, code, stderr.String())
			}
		})
	}

	t.Run("missing claude", func(t *testing.T) {
		fakeEnrolmentGateway(t, persistedEnrolmentResult())
		var stdout, stderr bytes.Buffer
		code := runConnect([]string{
			"--server", "https://fabric.example", "--project", "project-a", "--owner", "owner",
			"--target", "claude", "--stdio-bin", os.Args[0], "--claude-bin", filepath.Join(t.TempDir(), "missing-claude"),
		}, &stdout, &stderr)
		if code != 1 || !strings.Contains(stderr.String(), "not found in PATH") {
			t.Fatalf("runConnect code=%d stderr=%q", code, stderr.String())
		}
	})
}

func TestRunConnectAutoDetectReportsWhenEveryHarnessFailsToWire(t *testing.T) {
	fakeEnrolmentGateway(t, persistedEnrolmentResult())
	bin := t.TempDir()
	claude := filepath.Join(bin, "claude")
	if err := os.WriteFile(claude, []byte("#!/bin/sh\nexit 9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Chdir(t.TempDir())
	var stdout, stderr bytes.Buffer
	code := runConnect([]string{"--server", "https://fabric.example", "--project", "project-a", "--owner", "owner"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "no harnesses could be wired") || !strings.Contains(stderr.String(), "claude not wired") {
		t.Fatalf("runConnect code=%d stderr=%q", code, stderr.String())
	}
}

func TestRunStatusValidatesProfileAndReportsUnavailableRuntime(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runStatus(nil, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "--profile is required") {
		t.Fatalf("missing profile code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runStatus([]string{"--profile", "../escape"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "--profile:") {
		t.Fatalf("unsafe profile code=%d stderr=%q", code, stderr.String())
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	dir := filepath.Join(home, ".wormhole", "credentials")
	writeTestCredentials(t, dir, "project-a", credentials{ProjectID: "project-a", IssuedAt: time.Now().UTC()})
	stdout.Reset()
	stderr.Reset()
	if code := runStatus([]string{"--profile", "project-a"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "gatewayd not running") {
		t.Fatalf("unavailable gateway code=%d stderr=%q", code, stderr.String())
	}
}
