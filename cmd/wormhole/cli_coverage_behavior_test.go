package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunDispatchesSupportedCommands(t *testing.T) {
	tests := []struct {
		name string
		args []string
		code int
		want string
	}{
		{name: "init", args: []string{"init", "unexpected"}, code: 2, want: "takes no arguments"},
		{name: "join", args: []string{"join", "--unknown"}, code: 2, want: "flag provided but not defined"},
		{name: "connect", args: []string{"connect", "--unknown"}, code: 2, want: "flag provided but not defined"},
		{name: "whoami", args: []string{"whoami", "--unknown"}, code: 2, want: "flag provided but not defined"},
		{name: "profile", args: []string{"profile", "unknown"}, code: 2, want: "wormhole profile list"},
		{name: "viewer key", args: []string{"viewer-key", "unknown"}, code: 2, want: "only \"create\" is supported"},
		{name: "mcp", args: []string{"mcp", "--unknown"}, code: 1, want: "no flags supported"},
		{name: "help", args: []string{"help"}, code: 0, want: "usage: wormhole"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(tt.args, &stdout, &stderr); code != tt.code {
				t.Fatalf("run(%q) code = %d, want %d", tt.args, code, tt.code)
			}
			if output := stdout.String() + stderr.String(); !strings.Contains(output, tt.want) {
				t.Fatalf("run(%q) output = %q, want containing %q", tt.args, output, tt.want)
			}
		})
	}
}

func TestRunMCPRelaysDaemonResponse(t *testing.T) {
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	socketPath := filepath.Join(runtimeDir, "wormhole", "wormholed.sock")
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		t.Fatalf("create socket directory: %v", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	serverDone := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer conn.Close()
		_, writeErr := io.WriteString(conn, "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/ready\"}\n")
		serverDone <- writeErr
	}()

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	originalStdin := os.Stdin
	os.Stdin = stdinR
	t.Cleanup(func() {
		os.Stdin = originalStdin
		_ = stdinR.Close()
		_ = stdinW.Close()
	})

	var stdout, stderr bytes.Buffer
	if code := runMCP(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("runMCP code = %d, want 0 (stderr=%q)", code, stderr.String())
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("serve response: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "notifications/ready") {
		t.Fatalf("stdout = %q, want relayed daemon notification", got)
	}
}

func TestToolWrappersRejectMalformedPayloads(t *testing.T) {
	result := `{"content":[{"type":"text","text":"not-json"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":`+result+`}`)
	}))
	defer server.Close()

	tests := []struct {
		name string
		call func() error
		want string
	}{
		{name: "register", call: func() error { _, err := doRegister(server.Client(), server.URL, "p", registerAgentInput{}); return err }, want: "decode register result"},
		{name: "search", call: func() error { _, err := doSearch(server.Client(), server.URL, "p", "t", "query", 1); return err }, want: "decode search result"},
		{name: "channels", call: func() error { _, err := doListChannels(server.Client(), server.URL, "p", "t"); return err }, want: "decode list channels result"},
		{name: "post event", call: func() error {
			_, err := doPostEvent(server.Client(), server.URL, "p", "t", "c", "message.posted", nil, nil)
			return err
		}, want: "decode post event result"},
		{name: "tasks", call: func() error { _, err := doListTasks(server.Client(), server.URL, "p", "t"); return err }, want: "decode list tasks result"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestCallToolReportsRequestConstructionAndTransportErrors(t *testing.T) {
	_, err := callTool(http.DefaultClient, "https://example.invalid", "wormhole.task.list", "p", "", struct {
		Unsupported chan int `json:"unsupported"`
	}{Unsupported: make(chan int)})
	if err == nil || !strings.Contains(err.Error(), "marshal wormhole.task.list arguments") {
		t.Fatalf("unsupported argument error = %v", err)
	}

	_, err = callTool(http.DefaultClient, "://invalid", "wormhole.task.list", "p", "", struct{}{})
	if err == nil || !strings.Contains(err.Error(), "build request") {
		t.Fatalf("invalid URL error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	client := server.Client()
	server.Close()
	_, err = callTool(client, server.URL, "wormhole.task.list", "p", "", struct{}{})
	if err == nil || !strings.Contains(err.Error(), "call wormhole.task.list") {
		t.Fatalf("transport error = %v", err)
	}
}

func TestRunConnectOpenCodeReportsConfigWriteFailure(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := runConnectOpenCode(filepath.Join(blocker, "opencode.json"), "wormhole", "wormhole", &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "read") {
		t.Fatalf("runConnectOpenCode code=%d stderr=%q, want read failure", code, stderr.String())
	}
}

func TestHarnessWiringReportsMissingAndFailingBinaries(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if err := wireClaudeMCP("claude", "", ""); err == nil || !strings.Contains(err.Error(), "wormhole binary not found") {
		t.Fatalf("wireClaudeMCP missing wormhole error = %v", err)
	}
	if err := wireOpenCodeMCP(filepath.Join(t.TempDir(), "opencode.json"), "", ""); err == nil || !strings.Contains(err.Error(), "wormhole binary not found") {
		t.Fatalf("wireOpenCodeMCP missing wormhole error = %v", err)
	}

	binDir := t.TempDir()
	wormholePath := filepath.Join(binDir, "wormhole")
	if err := os.WriteFile(wormholePath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write wormhole executable: %v", err)
	}
	t.Setenv("PATH", binDir)
	if err := wireClaudeMCP("/bin/false", "", ""); err == nil || !strings.Contains(err.Error(), "claude mcp add failed") {
		t.Fatalf("wireClaudeMCP failed command error = %v", err)
	}
}

func TestFilesystemFailuresAreReported(t *testing.T) {
	dir := t.TempDir()
	if err := writeCredentials(dir, credentials{}); err == nil || !strings.Contains(err.Error(), "write credentials file") {
		t.Fatalf("writeCredentials directory target error = %v", err)
	}

	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if code := initWizard(strings.NewReader("\n\n\n"), &stdout, &stderr, filepath.Join(blocker, "config.toml")); code != 1 {
		t.Fatalf("initWizard code = %d, want 1 (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "save config") {
		t.Fatalf("initWizard stderr = %q, want save failure", stderr.String())
	}
}

func TestHomeAndProfileResolutionFailuresAreReported(t *testing.T) {
	t.Run("home unavailable", func(t *testing.T) {
		t.Setenv("HOME", "")
		if _, err := profilesDir(); err == nil || !strings.Contains(err.Error(), "resolve home directory") {
			t.Fatalf("profilesDir error = %v", err)
		}
		if _, err := resolveCredentialsPath("", "", "p", ""); err == nil {
			t.Fatal("resolveCredentialsPath error = nil, want home resolution failure")
		}
		if _, err := resolveOpenCodeConfigPath("", t.TempDir()); err == nil || !strings.Contains(err.Error(), "resolve opencode config path") {
			t.Fatalf("resolveOpenCodeConfigPath error = %v", err)
		}

		var stdout, stderr bytes.Buffer
		if code := runWhoami(nil, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "resolve home directory") {
			t.Fatalf("runWhoami code=%d stderr=%q", code, stderr.String())
		}
		stdout.Reset()
		stderr.Reset()
		if code := runProfileList(nil, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "resolve home directory") {
			t.Fatalf("runProfileList code=%d stderr=%q", code, stderr.String())
		}
	})

	t.Run("profiles path is a file", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		path := filepath.Join(home, ".wormhole", "credentials")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("create config directory: %v", err)
		}
		if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
			t.Fatalf("write profiles blocker: %v", err)
		}
		if _, err := resolveWhoamiProfile(path); err == nil || !strings.Contains(err.Error(), "read profiles directory") {
			t.Fatalf("resolveWhoamiProfile error = %v", err)
		}

		var stdout, stderr bytes.Buffer
		if code := runProfileList(nil, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "read profiles directory") {
			t.Fatalf("runProfileList code=%d stderr=%q", code, stderr.String())
		}
	})
}

func TestFlagParsersRejectUnknownFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runProfileList([]string{"--unknown"}, &stdout, &stderr); code != 2 {
		t.Fatalf("runProfileList code = %d, want 2", code)
	}
	stdout.Reset()
	stderr.Reset()
	if code := runViewerKeyCreate([]string{"--unknown"}, &stdout, &stderr); code != 2 {
		t.Fatalf("runViewerKeyCreate code = %d, want 2", code)
	}
}

func TestSocketHelpersCoverFallbackAndReadFailures(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	if got := gatewaySocketPath(); !strings.HasSuffix(got, filepath.Join("wormhole-runtime", "wormhole", "wormholed.sock")) {
		t.Fatalf("fallback socket path = %q", got)
	}

	path := filepath.Join(t.TempDir(), "wormholed.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		reader := bufio.NewReader(conn)
		_, _ = reader.ReadBytes('\n')
		_ = conn.Close()
	}()
	_, reachable, err := doRegisterViaSocket(path, "p", registerAgentInput{})
	if !reachable || err == nil || !strings.Contains(err.Error(), "read initialize response") {
		t.Fatalf("doRegisterViaSocket reachable=%v error=%v, want reachable read error", reachable, err)
	}

	malformedPath := registerFailureSocket(t,
		`{"jsonrpc":"2.0","id":1,"result":{}}`,
		`{"jsonrpc":"2.0","id":2,"result":"not-a-tool-result"}`,
	)
	_, reachable, err = doRegisterViaSocket(malformedPath, "p", registerAgentInput{})
	if !reachable || err == nil || !strings.Contains(err.Error(), "decode tools/call result") {
		t.Fatalf("malformed tool result reachable=%v error=%v", reachable, err)
	}
}

type alwaysFailWriter struct{ err error }

func (w alwaysFailWriter) Write([]byte) (int, error) { return 0, w.err }

func TestSocketToStdoutReportsWriterAndTruncationErrors(t *testing.T) {
	t.Run("writer", func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()
		wantErr := errors.New("output unavailable")
		go func() { _, _ = io.WriteString(server, "{}\n") }()
		if err := socketToStdout(client, alwaysFailWriter{err: wantErr}); !errors.Is(err, wantErr) {
			t.Fatalf("socketToStdout error = %v, want %v", err, wantErr)
		}
	})

	t.Run("truncated frame", func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		go func() {
			_, _ = io.WriteString(server, "{\"jsonrpc\":")
			_ = server.Close()
		}()
		if err := socketToStdout(client, io.Discard); err == nil || !strings.Contains(err.Error(), "socket closed mid-message") {
			t.Fatalf("socketToStdout error = %v, want truncated-frame error", err)
		}
	})
}

func TestStdinToSocketReportsWriteFailure(t *testing.T) {
	client, server := net.Pipe()
	_ = server.Close()
	defer client.Close()
	if err := stdinToSocket(strings.NewReader("{}\n"), client); err == nil {
		t.Fatal("stdinToSocket error = nil, want closed socket write failure")
	}
}
