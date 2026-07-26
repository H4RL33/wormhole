package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runtimeconfig "github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/localapi"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
)

func TestCodeGraphCLIRejectsRemainingCommandAndInteractionFailures(t *testing.T) {
	callErr := errors.New("lifecycle unavailable")
	failingCall := func(context.Context, localapi.CodeGraphLifecycleRequest) (localapi.CodeGraphLifecycleStatus, error) {
		return localapi.CodeGraphLifecycleStatus{}, callErr
	}
	tests := []struct {
		name        string
		args        []string
		stdin       string
		interactive bool
		wantCode    int
		want        string
	}{
		{"checkout missing subcommand", []string{"checkout"}, "", false, 2, "checkout <set|show>"},
		{"checkout unknown subcommand", []string{"checkout", "remove"}, "", false, 2, "unknown code-graph checkout"},
		{"status operand", []string{"status", "--project", "project-a", "extra"}, "", false, 2, "unexpected operand"},
		{"unknown flag", []string{"status", "--project", "project-a", "--unknown"}, "", false, 2, "flag provided"},
		{"interactive eof", []string{"rebuild", "--project", "project-a"}, "", true, 1, "read confirmation"},
		{"lifecycle failure", []string{"rebuild", "--project", "project-a", "--confirm"}, "", false, 1, callErr.Error()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := runConfigCodeGraph(tt.args, strings.NewReader(tt.stdin), &stdout, &stderr, tt.interactive, failingCall); code != tt.wantCode || !strings.Contains(stderr.String(), tt.want) {
				t.Fatalf("code=%d stderr=%q, want code=%d containing %q", code, stderr.String(), tt.wantCode, tt.want)
			}
		})
	}
	var stdout, stderr bytes.Buffer
	if code := runConfig(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("empty config code = %d", code)
	}
	if code := runConfig([]string{"other"}, &stdout, &stderr); code != 2 {
		t.Fatalf("unknown config code = %d", code)
	}
}

func TestResolveCodeGraphProjectRequiresExplicitOrNearestConfiguration(t *testing.T) {
	if got, err := resolveCodeGraphProject("  project-a  "); err != nil || got != "project-a" {
		t.Fatalf("explicit project = %q, err=%v", got, err)
	}
	root := t.TempDir()
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(before) })
	if _, err := resolveCodeGraphProject(""); err == nil {
		t.Fatal("missing nearest project configuration succeeded")
	}
}

func TestExecuteCodeGraphLifecycleRejectsAmbiguousAndUnreadyCredentials(t *testing.T) {
	tests := []struct {
		name     string
		profiles int
		want     string
	}{
		{"no credentials", 0, "no credential"},
		{"ambiguous credentials", 2, "multiple credential profiles"},
		{"checkpoint not ready", 1, "not a ready bootstrapped checkpoint"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", filepath.Join(root, "home"))
			t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
			t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "run"))
			paths, err := runtimeconfig.ResolveRuntimePaths()
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < tt.profiles; i++ {
				profile := "profile-" + string(rune('a'+i))
				if _, err := runtimeconfig.WriteCredentialProfile(paths.CredentialsDir, profile, runtimeconfig.Credentials{
					Server: "https://fabric.example", ProjectID: "project-a", AgentID: "agent-" + profile,
					PassportID: "passport-" + profile, Token: "secret",
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(filepath.Dir(paths.DBPath), 0o700); err != nil {
				t.Fatal(err)
			}
			store, err := localstore.Open(paths.DBPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = executeCodeGraphLifecycle(context.Background(), localapi.CodeGraphLifecycleRequest{
				Operation: localapi.CodeGraphRebuild, ProjectID: "project-a",
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("executeCodeGraphLifecycle error = %v, want containing %q", err, tt.want)
			}
		})
	}
}
