//go:build linux

package config

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSystemdGatewayServiceUsesExactOwnerPrivatePaths(t *testing.T) {
	fixture := newSystemdFixture(t)
	if err := fixture.service.Install(t.Context(), ConfirmedServiceChange{Executable: fixture.executable}); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(fixture.configRoot, "systemd", "user", gatewayServiceUnit)
	info, err := os.Lstat(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("unit mode = %o", info.Mode().Perm())
	}
	directory, err := os.Lstat(filepath.Dir(unitPath))
	if err != nil {
		t.Fatal(err)
	}
	if directory.Mode().Perm() != 0o700 {
		t.Fatalf("unit directory mode = %o", directory.Mode().Perm())
	}
	bytes, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "[Unit]\nDescription=Wormhole Gateway\nAfter=default.target\n\n" +
		"[Service]\nType=simple\nExecStart=\"" + fixture.executable + "\"\n" +
		"Environment=\"XDG_RUNTIME_DIR=" + fixture.runtimeRoot + "\"\n" +
		"Restart=on-failure\nRestartSec=2s\n\n[Install]\nWantedBy=default.target\n"
	if string(bytes) != want {
		t.Fatalf("unit bytes = %q, want %q", bytes, want)
	}
	paths, err := ResolveRuntimePaths()
	if err != nil {
		t.Fatal(err)
	}
	if paths.SocketPath != filepath.Join(fixture.runtimeRoot, "wormhole", "wormholed.sock") {
		t.Fatalf("socket path = %q", paths.SocketPath)
	}
	for _, directoryPath := range []string{fixture.runtimeRoot, filepath.Dir(paths.SocketPath)} {
		runtimeInfo, err := os.Lstat(directoryPath)
		if err != nil {
			t.Fatal(err)
		}
		if !runtimeInfo.IsDir() || runtimeInfo.Mode().Perm() != 0o700 {
			t.Fatalf("runtime directory %s mode = %v", directoryPath, runtimeInfo.Mode())
		}
	}
}

func TestSystemdGatewayServiceInstallIsActiveIdempotent(t *testing.T) {
	fixture := newSystemdFixture(t)
	change := ConfirmedServiceChange{Executable: fixture.executable}
	if err := fixture.service.Install(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	mutationsAfterFirst := mutatingSystemdCalls(fixture.runner.calls)
	if err := fixture.service.Install(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	if got := mutatingSystemdCalls(fixture.runner.calls); !reflect.DeepEqual(got, mutationsAfterFirst) {
		t.Fatalf("second install mutated service: before=%v after=%v", mutationsAfterFirst, got)
	}
	state, err := fixture.service.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !state.Installed || !state.Enabled || !state.Active || state.Diagnostic != "" {
		t.Fatalf("state = %+v", state)
	}
}

func TestSystemdGatewayServiceRepairsOnlyRecognizedIncompleteState(t *testing.T) {
	fixture := newSystemdFixture(t)
	change := ConfirmedServiceChange{Executable: fixture.executable}
	if err := fixture.service.Install(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	fixture.runner.active = false
	before := len(fixture.runner.calls)
	if err := fixture.service.Install(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	calls := fixture.runner.calls[before:]
	if got := mutatingSystemdCalls(calls); len(got) != 1 || !strings.Contains(got[0], " start ") {
		t.Fatalf("inactive repair calls = %v", calls)
	}

	fixture.runner.enabled, fixture.runner.active = false, false
	before = len(fixture.runner.calls)
	if err := fixture.service.Install(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	calls = fixture.runner.calls[before:]
	if got := mutatingSystemdCalls(calls); len(got) != 1 || !strings.Contains(got[0], " enable --now ") {
		t.Fatalf("disabled repair calls = %v", calls)
	}
}

func TestSystemdGatewayServiceUnavailableManagerDoesNotWrite(t *testing.T) {
	fixture := newSystemdFixture(t)
	fixture.runner.unusable = true
	err := fixture.service.Install(t.Context(), ConfirmedServiceChange{Executable: fixture.executable})
	if !errors.Is(err, ErrServiceManagerUnavailable) || err.Error() != ErrServiceManagerUnavailable.Error() {
		t.Fatalf("Install error = %v", err)
	}
	unitPath := filepath.Join(fixture.configRoot, "systemd", "user", gatewayServiceUnit)
	if _, statErr := os.Lstat(unitPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unit mutated before manager check: %v", statErr)
	}
	if mutating := mutatingSystemdCalls(fixture.runner.calls); len(mutating) != 0 {
		t.Fatalf("mutating calls = %v", mutating)
	}
}

func TestSystemdGatewayServiceRejectsUnknownStateOutput(t *testing.T) {
	fixture := newSystemdFixture(t)
	if err := fixture.service.Install(t.Context(), ConfirmedServiceChange{Executable: fixture.executable}); err != nil {
		t.Fatal(err)
	}
	fixture.runner.enabledOutput = "generated\n"
	if _, err := fixture.service.Inspect(t.Context()); !errors.Is(err, ErrServiceStateUnknown) {
		t.Fatalf("Inspect error = %v, want ErrServiceStateUnknown", err)
	}
}

func TestSystemdGatewayServiceStartIsInstalledAndActiveIdempotent(t *testing.T) {
	fixture := newSystemdFixture(t)
	if err := fixture.service.Start(t.Context()); !errors.Is(err, ErrServiceNotInstalled) {
		t.Fatalf("uninstalled Start error = %v", err)
	}
	if err := fixture.service.Install(t.Context(), ConfirmedServiceChange{Executable: fixture.executable}); err != nil {
		t.Fatal(err)
	}
	before := len(mutatingSystemdCalls(fixture.runner.calls))
	if err := fixture.service.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if after := len(mutatingSystemdCalls(fixture.runner.calls)); after != before {
		t.Fatalf("active Start mutated service: before=%d after=%d", before, after)
	}
}

func TestSystemdGatewayServiceUnitCASPreservesConcurrentReplacement(t *testing.T) {
	fixture := newSystemdFixture(t)
	service := fixture.service.(*systemdGatewayService)
	unitPath := filepath.Join(fixture.configRoot, "systemd", "user", gatewayServiceUnit)
	competitor := []byte("concurrent-owner-state")
	service.hooks.beforeCommit = func() {
		if err := os.WriteFile(unitPath, competitor, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	err := service.Install(t.Context(), ConfirmedServiceChange{Executable: fixture.executable})
	if !errors.Is(err, ErrServiceChangeDrift) {
		t.Fatalf("Install error = %v, want drift", err)
	}
	got, readErr := os.ReadFile(unitPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(competitor) {
		t.Fatalf("concurrent state overwritten: %q", got)
	}
	if mutating := mutatingSystemdCalls(fixture.runner.calls); len(mutating) != 0 {
		t.Fatalf("CAS conflict caused manager mutation: %v", mutating)
	}
}

func TestSystemdGatewayServiceWaitReadyRequiresExactGatewayIdentity(t *testing.T) {
	fixture := newSystemdFixture(t)
	shortRuntime, err := os.MkdirTemp("", "wh-ready-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(shortRuntime) })
	t.Setenv("XDG_RUNTIME_DIR", shortRuntime)
	paths, err := ResolveRuntimePaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.SocketPath), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", paths.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	requestSeen := make(chan map[string]any, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		line, _ := bufio.NewReader(connection).ReadBytes('\n')
		var request map[string]any
		_ = json.Unmarshal(line, &request)
		requestSeen <- request
		_, _ = connection.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","serverInfo":{"name":"gatewayd","version":"test"},"capabilities":{"tools":{}}}}` + "\n"))
	}()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := fixture.service.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	request := <-requestSeen
	params, ok := request["params"].(map[string]any)
	if !ok {
		t.Fatalf("request params = %#v", request["params"])
	}
	client, ok := params["clientInfo"].(map[string]any)
	if !ok || client["name"] != "wormhole-setup" || request["method"] != "initialize" {
		t.Fatalf("initialize request = %#v", request)
	}
}

func TestSystemdGatewayServiceFallbackRuntimeMatchesSocket(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_RUNTIME_DIR", "")
	temporaryRoot := filepath.Join(home, "tmp")
	if err := os.Mkdir(temporaryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", temporaryRoot)
	executable := filepath.Join(t.TempDir(), "gatewayd")
	if err := os.WriteFile(executable, []byte("gateway"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &recordingServiceRunner{}
	service := NewGatewayService(runner)
	if err := service.Install(t.Context(), ConfirmedServiceChange{Executable: executable}); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(home, "config", "systemd", "user", gatewayServiceUnit)
	unit, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	wantRuntime := filepath.Join(home, "tmp", "wormhole-runtime")
	if !strings.Contains(string(unit), `Environment="XDG_RUNTIME_DIR=`+wantRuntime+`"`) {
		t.Fatalf("fallback unit = %q", unit)
	}
	paths, err := ResolveRuntimePaths()
	if err != nil {
		t.Fatal(err)
	}
	if paths.SocketPath != filepath.Join(wantRuntime, "wormhole", "wormholed.sock") {
		t.Fatalf("fallback socket = %q", paths.SocketPath)
	}
}

func TestSystemdGatewayServiceRejectsUnsafeUnitAndExecutable(t *testing.T) {
	fixture := newSystemdFixture(t)
	unitDir := filepath.Join(fixture.configRoot, "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(unitDir, gatewayServiceUnit)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.Install(t.Context(), ConfirmedServiceChange{Executable: fixture.executable}); !errors.Is(err, ErrUnsafeServicePath) {
		t.Fatalf("symlink unit error = %v", err)
	}

	other := newSystemdFixture(t)
	symlinkExecutable := filepath.Join(t.TempDir(), "gatewayd")
	if err := os.Symlink(other.executable, symlinkExecutable); err != nil {
		t.Fatal(err)
	}
	if err := other.service.Install(t.Context(), ConfirmedServiceChange{Executable: symlinkExecutable}); !errors.Is(err, ErrUnsafeServicePath) {
		t.Fatalf("symlink executable error = %v", err)
	}
}

func TestRepositoryContentIsNeverInstalledOrExecuted(t *testing.T) {
	fixture := newSystemdFixture(t)
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repositoryExecutable := filepath.Join(workingDirectory, "test-repository-gatewayd")
	if err := os.WriteFile(repositoryExecutable, []byte("#!/bin/sh\ntouch should-not-run\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(repositoryExecutable) })
	err = fixture.service.Install(t.Context(), ConfirmedServiceChange{Executable: repositoryExecutable})
	if !errors.Is(err, ErrRepositoryContent) {
		t.Fatalf("repository executable error = %v", err)
	}
	if mutating := mutatingSystemdCalls(fixture.runner.calls); len(mutating) != 0 {
		t.Fatalf("repository executable caused mutations: %v", mutating)
	}
}

func TestRepositoryExecutableIsRejectedOutsideCallerWorkingDirectory(t *testing.T) {
	fixture := newSystemdFixture(t)
	repository := t.TempDir()
	if err := os.WriteFile(filepath.Join(repository, ".git"), []byte("gitdir: elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(repository, "gatewayd")
	if err := os.WriteFile(executable, []byte("gateway"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.Install(t.Context(), ConfirmedServiceChange{Executable: executable}); !errors.Is(err, ErrRepositoryContent) {
		t.Fatalf("repository executable error = %v", err)
	}
	if mutating := mutatingSystemdCalls(fixture.runner.calls); len(mutating) != 0 {
		t.Fatalf("repository executable caused mutations: %v", mutating)
	}
}

func TestSystemdGatewayServiceRejectsSymlinkedExecutableAndRootAncestors(t *testing.T) {
	fixture := newSystemdFixture(t)
	realExecutableRoot := t.TempDir()
	executable := filepath.Join(realExecutableRoot, "gatewayd")
	if err := os.WriteFile(executable, []byte("gateway"), 0o700); err != nil {
		t.Fatal(err)
	}
	aliasRoot := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realExecutableRoot, aliasRoot); err != nil {
		t.Fatal(err)
	}
	aliasExecutable := filepath.Join(aliasRoot, "gatewayd")
	if err := fixture.service.Install(t.Context(), ConfirmedServiceChange{Executable: aliasExecutable}); !errors.Is(err, ErrUnsafeServicePath) {
		t.Fatalf("symlinked executable ancestor error = %v", err)
	}

	other := newSystemdFixture(t)
	realConfigParent := t.TempDir()
	aliasConfigParent := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realConfigParent, aliasConfigParent); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(aliasConfigParent, "config"))
	if err := other.service.Install(t.Context(), ConfirmedServiceChange{Executable: other.executable}); !errors.Is(err, ErrUnsafeServicePath) {
		t.Fatalf("symlinked config ancestor error = %v", err)
	}
	if mutating := mutatingSystemdCalls(other.runner.calls); len(mutating) != 0 {
		t.Fatalf("unsafe ancestor caused manager mutation: %v", mutating)
	}
}

type systemdFixture struct {
	service     GatewayService
	runner      *recordingServiceRunner
	configRoot  string
	runtimeRoot string
	executable  string
}

func newSystemdFixture(t *testing.T) systemdFixture {
	t.Helper()
	home := t.TempDir()
	configRoot := filepath.Join(home, "config")
	runtimeRoot := filepath.Join(home, "run")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_RUNTIME_DIR", runtimeRoot)
	executable := filepath.Join(t.TempDir(), "gatewayd")
	if err := os.WriteFile(executable, []byte("gateway"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &recordingServiceRunner{}
	return systemdFixture{service: NewGatewayService(runner), runner: runner, configRoot: configRoot, runtimeRoot: runtimeRoot, executable: executable}
}

func mutatingSystemdCalls(calls []serviceCall) []string {
	var mutations []string
	for _, call := range calls {
		joined := call.executable + " " + strings.Join(call.args, " ") + " "
		if strings.Contains(joined, " daemon-reload ") || strings.Contains(joined, " enable ") || strings.Contains(joined, " start ") {
			mutations = append(mutations, joined)
		}
	}
	return mutations
}
