package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	runtimeconfig "github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/localapi"
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

func TestExecuteCodeGraphLifecycleStoppedDaemonDoesNotMutateDatabase(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "absent"
		if existing {
			name = "existing"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", filepath.Join(root, "home"))
			t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
			t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "run"))
			paths, err := runtimeconfig.ResolveRuntimePaths()
			if err != nil {
				t.Fatal(err)
			}
			files := []string{paths.DBPath, paths.DBPath + "-wal", paths.DBPath + "-shm"}
			before := map[string]databaseFileSnapshot{}
			if existing {
				if err := os.MkdirAll(filepath.Dir(paths.DBPath), 0o700); err != nil {
					t.Fatal(err)
				}
				for index, path := range files {
					if err := os.WriteFile(path, []byte("opaque-owner-state-"+string(rune('a'+index))), 0o600); err != nil {
						t.Fatal(err)
					}
					stamp := time.Unix(1_700_000_000+int64(index), 123).UTC()
					if err := os.Chtimes(path, stamp, stamp); err != nil {
						t.Fatal(err)
					}
					before[path] = snapshotDatabaseFile(t, path)
				}
			}

			requests := []localapi.CodeGraphLifecycleRequest{
				{Operation: localapi.CodeGraphEnable, ProjectID: "project-a", Checkout: "/enable"},
				{Operation: localapi.CodeGraphDisable, ProjectID: "project-a"},
				{Operation: localapi.CodeGraphStatus, ProjectID: "project-a"},
				{Operation: localapi.CodeGraphRebuild, ProjectID: "project-a"},
				{Operation: localapi.CodeGraphCheckoutSet, ProjectID: "project-a", Checkout: "/set"},
				{Operation: localapi.CodeGraphCheckoutShow, ProjectID: "project-a"},
			}
			for _, request := range requests {
				_, err = executeCodeGraphLifecycle(context.Background(), request)
				if err == nil || !strings.Contains(err.Error(), "gatewayd not running") {
					t.Errorf("stopped Gateway %s error = %v, want gatewayd not running", request.Operation, err)
				}
				for _, path := range files {
					if !existing {
						if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
							t.Fatalf("stopped Gateway %s created %s: %v", request.Operation, path, statErr)
						}
						continue
					}
					after := snapshotDatabaseFile(t, path)
					if !before[path].equal(after) {
						t.Fatalf("stopped Gateway %s changed %s\nbefore=%+v\nafter=%+v", request.Operation, path, before[path], after)
					}
				}
			}
		})
	}
}

type databaseFileSnapshot struct {
	Bytes         string
	Mode          os.FileMode
	Size          int64
	ModTime       time.Time
	Device, Inode uint64
	Links         uint64
	UID, GID      uint32
}

func snapshotDatabaseFile(t *testing.T, path string) databaseFileSnapshot {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("%s metadata has type %T", path, info.Sys())
	}
	return databaseFileSnapshot{
		Bytes: string(raw), Mode: info.Mode(), Size: info.Size(), ModTime: info.ModTime(),
		Device: uint64(stat.Dev), Inode: stat.Ino, Links: uint64(stat.Nlink), UID: stat.Uid, GID: stat.Gid,
	}
}

func (snapshot databaseFileSnapshot) equal(other databaseFileSnapshot) bool {
	return snapshot.Bytes == other.Bytes && snapshot.Mode == other.Mode && snapshot.Size == other.Size &&
		snapshot.ModTime.Equal(other.ModTime) && snapshot.Device == other.Device && snapshot.Inode == other.Inode &&
		snapshot.Links == other.Links && snapshot.UID == other.UID && snapshot.GID == other.GID
}
