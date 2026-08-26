//go:build linux

package config

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSystemdConfirmedChangeBindsExactPriorUnitDigest(t *testing.T) {
	fixture := newSystemdFixture(t)
	first := confirmedServiceChange(t, fixture.service, fixture.executable)
	if err := fixture.service.Install(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	fixture.runner.enabled, fixture.runner.active = false, false
	prior, err := fixture.service.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	secondExecutable := filepath.Join(t.TempDir(), "gatewayd")
	if err := os.WriteFile(secondExecutable, []byte("second"), 0o700); err != nil {
		t.Fatal(err)
	}
	change, err := ConfirmGatewayServiceChange(t.Context(), fixture.service, secondExecutable)
	if err != nil {
		t.Fatal(err)
	}
	if change.ExpectedPrior.UnitDigest != prior.UnitDigest || change.ExpectedPrior.UnitDigest == "" || change.DesiredUnitDigest == "" {
		t.Fatalf("change lacks exact digests: %+v prior=%+v", change, prior)
	}
	unitPath := filepath.Join(fixture.configRoot, "systemd", "user", gatewayServiceUnit)
	if err := os.WriteFile(unitPath, []byte("third-state-unit"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.Install(t.Context(), change); !errors.Is(err, ErrServiceChangeDrift) {
		t.Fatalf("Install error = %v, want exact-unit drift", err)
	}
	got, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "third-state-unit" {
		t.Fatalf("third state overwritten: %q", got)
	}
}

func TestSystemdConditionalPublishPreservesSwapAfterFinalCompare(t *testing.T) {
	fixture := newSystemdFixture(t)
	service := fixture.service.(*systemdGatewayService)
	change := confirmedServiceChange(t, fixture.service, fixture.executable)
	unitPath := filepath.Join(fixture.configRoot, "systemd", "user", gatewayServiceUnit)
	competitor := []byte("post-compare-third-state")
	service.hooks.beforeConditionalPublish = func() {
		if err := os.WriteFile(unitPath, competitor, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := fixture.service.Install(t.Context(), change); !errors.Is(err, ErrServiceChangeDrift) {
		t.Fatalf("Install error = %v, want drift", err)
	}
	got, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(competitor) {
		t.Fatalf("conditional publish lost third state: %q", got)
	}
}

func TestSystemdInstallResumesReloadAfterPublishedUnit(t *testing.T) {
	fixture := newSystemdFixture(t)
	change := confirmedServiceChange(t, fixture.service, fixture.executable)
	fixture.runner.failDaemonReload = 1
	if err := fixture.service.Install(t.Context(), change); err == nil {
		t.Fatal("Install succeeded despite injected reload failure")
	}
	if err := fixture.service.Install(t.Context(), change); err != nil {
		t.Fatalf("resume Install: %v", err)
	}
	if got := fixture.runner.Count("daemon-reload"); got != 2 {
		t.Fatalf("daemon-reload count = %d, want 2", got)
	}
	state, err := fixture.service.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !state.Loaded || state.ReloadNeeded || !state.Enabled || !state.Active {
		t.Fatalf("resumed state = %+v", state)
	}
}

func TestSystemdInstallResumesAfterPublishedUnitDirectorySyncFailure(t *testing.T) {
	fixture := newSystemdFixture(t)
	service := fixture.service.(*systemdGatewayService)
	change := confirmedServiceChange(t, fixture.service, fixture.executable)
	failures := 1
	service.hooks.fault = func(point string) error {
		if point == "unit_directory_sync" && failures > 0 {
			failures--
			return errors.New("injected unit directory sync failure")
		}
		return nil
	}
	if err := fixture.service.Install(t.Context(), change); err == nil {
		t.Fatal("Install succeeded despite injected directory sync failure")
	}
	if err := fixture.service.Install(t.Context(), change); err != nil {
		t.Fatalf("resume Install: %v", err)
	}
	if got := fixture.runner.Count("daemon-reload"); got != 1 {
		t.Fatalf("daemon-reload count = %d, want 1", got)
	}
}

func TestSystemdInstallResumesAfterEnableFailure(t *testing.T) {
	fixture := newSystemdFixture(t)
	change := confirmedServiceChange(t, fixture.service, fixture.executable)
	fixture.runner.failEnable = 1
	if err := fixture.service.Install(t.Context(), change); err == nil {
		t.Fatal("Install succeeded despite injected enable failure")
	}
	if err := fixture.service.Install(t.Context(), change); err != nil {
		t.Fatalf("resume Install: %v", err)
	}
	if got := fixture.runner.Count("enable"); got != 2 {
		t.Fatalf("enable count = %d, want 2", got)
	}
}

func TestSystemdInstallResumesAfterStartFailure(t *testing.T) {
	fixture := newSystemdFixture(t)
	first := confirmedServiceChange(t, fixture.service, fixture.executable)
	if err := fixture.service.Install(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	fixture.runner.active = false
	change := confirmedServiceChange(t, fixture.service, fixture.executable)
	fixture.runner.failStart = 1
	if err := fixture.service.Install(t.Context(), change); err == nil {
		t.Fatal("Install succeeded despite injected start failure")
	}
	if err := fixture.service.Install(t.Context(), change); err != nil {
		t.Fatalf("resume Install: %v", err)
	}
	if got := fixture.runner.Count("start"); got != 2 {
		t.Fatalf("start count = %d, want 2", got)
	}
}

func TestSystemdInstallRequiresPostMutationReadback(t *testing.T) {
	fixture := newSystemdFixture(t)
	change := confirmedServiceChange(t, fixture.service, fixture.executable)
	fixture.runner.enableNoEffect = true
	if err := fixture.service.Install(t.Context(), change); !errors.Is(err, ErrServiceStateUnknown) {
		t.Fatalf("Install error = %v, want failed desired-state readback", err)
	}
}

func TestSystemdServicePreservesContextIdentity(t *testing.T) {
	fixture := newSystemdFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	fixture.runner.forcedError = context.Canceled
	if _, err := fixture.service.Inspect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Inspect error = %v, want context.Canceled", err)
	}

	fixture = newSystemdFixture(t)
	ctx, cancel = context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	fixture.runner.forcedError = context.DeadlineExceeded
	if err := fixture.service.WaitReady(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitReady error = %v, want context deadline", err)
	}
}

func TestSystemdInspectReportsActiveNotReady(t *testing.T) {
	fixture := newSystemdFixture(t)
	change := confirmedServiceChange(t, fixture.service, fixture.executable)
	if err := fixture.service.Install(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	state, err := fixture.service.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.Ready || state.Diagnostic != "gatewayd service is active but not ready" {
		t.Fatalf("non-ready state = %+v", state)
	}
}

func TestSystemdInspectRejectsAncestorSwapOnReadPath(t *testing.T) {
	fixture := newSystemdFixture(t)
	change := confirmedServiceChange(t, fixture.service, fixture.executable)
	if err := fixture.service.Install(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	original := fixture.configRoot + "-original"
	if err := os.Rename(fixture.configRoot, original); err != nil {
		t.Fatal(err)
	}
	attacker := t.TempDir()
	if err := os.Symlink(attacker, fixture.configRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Inspect(t.Context()); !errors.Is(err, ErrUnsafeServicePath) {
		t.Fatalf("Inspect error = %v, want unsafe swapped ancestor", err)
	}
}

func TestSystemdConditionalPublishRejectsAncestorSwap(t *testing.T) {
	fixture := newSystemdFixture(t)
	service := fixture.service.(*systemdGatewayService)
	change := confirmedServiceChange(t, fixture.service, fixture.executable)
	original := fixture.configRoot + "-original"
	attacker := t.TempDir()
	service.hooks.beforeConditionalPublish = func() {
		if err := os.Rename(fixture.configRoot, original); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(attacker, fixture.configRoot); err != nil {
			t.Fatal(err)
		}
	}
	if err := fixture.service.Install(t.Context(), change); !errors.Is(err, ErrUnsafeServicePath) && !errors.Is(err, ErrServiceChangeDrift) {
		t.Fatalf("Install error = %v, want unsafe path or drift", err)
	}
	if _, err := os.Stat(filepath.Join(attacker, "systemd", "user", gatewayServiceUnit)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("attacker path was modified: %v", err)
	}
}

func TestSystemdStartIgnoresSourceSwapAfterManagedInstall(t *testing.T) {
	fixture := newSystemdFixture(t)
	change := confirmedServiceChange(t, fixture.service, fixture.executable)
	if err := fixture.service.Install(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	fixture.runner.active = false
	if err := os.Remove(fixture.executable); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/bin/true", fixture.executable); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.Start(t.Context()); err != nil {
		t.Fatalf("Start uses mutable source executable: %v", err)
	}
}

func confirmedServiceChange(t *testing.T, service GatewayService, executable string) ConfirmedServiceChange {
	t.Helper()
	change, err := ConfirmGatewayServiceChange(t.Context(), service, executable)
	if err != nil {
		t.Fatal(err)
	}
	return change
}

func (runner *recordingServiceRunner) Count(action string) int {
	count := 0
	for _, call := range runner.calls {
		if len(call.args) > 1 && call.args[1] == action {
			count++
		}
	}
	return count
}

func TestReadinessRejectsInvalidFramesAndPreservesDeadline(t *testing.T) {
	valid := `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","serverInfo":{"name":"gatewayd"}}}` + "\n"
	tests := []struct {
		name  string
		frame string
	}{
		{"wrong id", strings.Replace(valid, `"id":1`, `"id":2`, 1)},
		{"wrong protocol", strings.Replace(valid, "2025-11-25", "2024-11-05", 1)},
		{"wrong name", strings.Replace(valid, "gatewayd", "wormhole", 1)},
		{"rpc error", `{"jsonrpc":"2.0","id":1,"error":{"code":-1,"message":"no"}}` + "\n"},
		{"no newline", strings.TrimSuffix(valid, "\n")},
		{"oversized", strings.Repeat("x", gatewayReadyMaxLine+1) + "\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, client := net.Pipe()
			defer client.Close()
			go func() {
				defer server.Close()
				_, _ = bufio.NewReader(server).ReadBytes('\n')
				_, _ = server.Write([]byte(test.frame))
			}()
			dial := func(context.Context, string, string) (net.Conn, error) { return client, nil }
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
			defer cancel()
			if err := probeGatewayReady(ctx, "ignored", dial); !errors.Is(err, ErrServiceNotReady) {
				t.Fatalf("probe error = %v", err)
			}
		})
	}

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	dial := func(context.Context, string, string) (net.Conn, error) { return client, nil }
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := probeGatewayReady(ctx, "ignored", dial); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline probe error = %v", err)
	}
}
