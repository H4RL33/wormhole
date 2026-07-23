package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// startFakeSocket stands up a fake Gateway Unix domain socket in a temp
// dir and returns the listener. It implements no MCP semantics itself --
// callers drive the accepted connection directly, matching the real
// Gateway socket's newline-delimited JSON-RPC framing (design doc §2)
// without needing the real localapi.Server.
func startFakeSocket(t *testing.T) net.Listener {
	t.Helper()
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "wormholed.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}

func dialFakeSocket(t *testing.T, ln net.Listener) (client net.Conn, server net.Conn) {
	t.Helper()
	serverConnCh := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			serverConnCh <- conn
		}
	}()
	client, err := net.Dial("unix", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial fake socket: %v", err)
	}
	server = <-serverConnCh
	return client, server
}

func waitForStdout(t *testing.T, stdout *syncBuffer, substr string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s := stdout.String()
		if strings.Contains(s, substr) {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q in stdout; got %q", substr, stdout.String())
	return ""
}

// TestBridge_StdinToSocket writes a newline-delimited JSON request directly
// to stdin and asserts it arrives on the fake socket unchanged (still
// newline-terminated), then writes a newline-delimited JSON response from
// the fake socket and asserts it arrives on stdout unchanged.
func TestBridge_StdinToSocket(t *testing.T) {
	ln := startFakeSocket(t)
	clientConn, serverConn := dialFakeSocket(t, ln)
	defer clientConn.Close()
	defer serverConn.Close()

	stdinR, stdinW := io.Pipe()
	stdout := &syncBuffer{}

	done := make(chan error, 1)
	go func() {
		done <- bridge(stdinR, stdout, clientConn)
	}()

	reqBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	go func() {
		io.WriteString(stdinW, reqBody+"\n")
	}()

	serverReader := bufio.NewReader(serverConn)
	line, err := serverReader.ReadString('\n')
	if err != nil {
		t.Fatalf("read request line from socket: %v", err)
	}
	got := strings.TrimSpace(line)
	if got != reqBody {
		t.Fatalf("socket received %q, want %q", got, reqBody)
	}

	respBody := `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25"}}`
	if _, err := serverConn.Write([]byte(respBody + "\n")); err != nil {
		t.Fatalf("write response to socket: %v", err)
	}

	stdoutStr := waitForStdout(t, stdout, respBody, 2*time.Second)
	got = strings.TrimSpace(stdoutStr)
	if got != respBody {
		t.Fatalf("stdout = %q, want %q", got, respBody)
	}

	stdinW.Close()
	<-done
}

// TestBridge_UnsolicitedNotification asserts that a message the fake socket
// server writes without any corresponding stdin request (e.g. an MCP
// notification pushed by Gateway) still arrives newline-delimited on
// stdout -- the bridge must not require request/response pairing.
func TestBridge_UnsolicitedNotification(t *testing.T) {
	ln := startFakeSocket(t)
	clientConn, serverConn := dialFakeSocket(t, ln)
	defer clientConn.Close()
	defer serverConn.Close()

	stdinR, stdinW := io.Pipe()
	stdout := &syncBuffer{}

	done := make(chan error, 1)
	go func() {
		done <- bridge(stdinR, stdout, clientConn)
	}()

	notif := `{"jsonrpc":"2.0","method":"notifications/wormhole.event","params":{"foo":"bar"}}`
	if _, err := serverConn.Write([]byte(notif + "\n")); err != nil {
		t.Fatalf("write notification to socket: %v", err)
	}

	stdoutStr := waitForStdout(t, stdout, notif, 2*time.Second)
	got := strings.TrimSpace(stdoutStr)
	if got != notif {
		t.Fatalf("stdout = %q, want %q", got, notif)
	}

	stdinW.Close()
	<-done
}

// TestBridge_PartialLineOnStdinEOF asserts that stdin closing mid-write --
// a partial line with no trailing newline, then EOF -- doesn't hang or
// panic bridge. There's no framing header to validate anymore, so the
// relevant edge case for line-based reading is a truncated final line
// rather than a malformed header.
func TestBridge_PartialLineOnStdinEOF(t *testing.T) {
	ln := startFakeSocket(t)
	clientConn, serverConn := dialFakeSocket(t, ln)
	defer clientConn.Close()
	defer serverConn.Close()

	// No trailing newline: stdin ends mid-message.
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"`)
	stdout := &syncBuffer{}

	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("bridge panicked: %v", r)
			}
		}()
		done <- bridge(stdin, stdout, clientConn)
	}()

	select {
	case err := <-done:
		// Any return (nil or error) is fine as long as it doesn't hang or
		// panic. A panic would have been converted to an error above.
		_ = err
	case <-time.After(2 * time.Second):
		t.Fatal("bridge hung on partial line / stdin EOF")
	}
}

// syncBuffer is a concurrency-safe bytes.Buffer wrapper: bridge's
// socket->stdout goroutine writes to stdout concurrently with the test
// goroutine polling it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestRunMCPRejectsFlagsAndUnavailableDaemon(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runMCP([]string{"--unsupported"}, &stdout, &stderr); code != 1 {
		t.Fatalf("runMCP(flags) code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no flags supported") {
		t.Fatalf("runMCP(flags) stderr = %q", stderr.String())
	}

	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	stdout.Reset()
	stderr.Reset()
	if code := runMCP(nil, &stdout, &stderr); code != 1 {
		t.Fatalf("runMCP(unavailable daemon) code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "dial gatewayd socket") {
		t.Fatalf("runMCP(unavailable daemon) stderr = %q", stderr.String())
	}
}
