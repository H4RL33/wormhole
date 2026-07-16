package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// frameContentLength wraps body in the Content-Length-prefixed framing used
// by the MCP stdio transport (LSP convention): "Content-Length: N\r\n\r\n<body>".
func frameContentLength(body string) string {
	return fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
}

// readTestFrame reads exactly one Content-Length-framed message from r and
// returns its body, mirroring what a real MCP stdio client does when
// reading the bridge's stdout. (Named distinctly from main.go's
// readContentLengthFrame, which has a different signature -- this is test
// harness code emulating the client side, not the bridge under test.)
func readTestFrame(r *bufio.Reader) (string, error) {
	var contentLength int
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // blank line ends the header block
		}
		const prefix = "Content-Length:"
		if strings.HasPrefix(line, prefix) {
			var n int
			if _, err := fmt.Sscanf(strings.TrimSpace(line[len(prefix):]), "%d", &n); err != nil {
				return "", fmt.Errorf("malformed Content-Length header %q: %w", line, err)
			}
			contentLength = n
		}
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		return "", err
	}
	return string(body), nil
}

// startFakeSocket stands up a fake wormholed Unix domain socket in a temp
// dir and returns the listener. It implements no MCP semantics itself --
// callers drive the accepted connection directly, matching the real
// wormholed socket's newline-delimited JSON-RPC framing (design doc §2)
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

// TestBridge_StdinToSocket feeds a Content-Length-framed initialize request
// on stdin and asserts it round-trips through the fake socket (arrives
// newline-delimited) and the fake socket's newline-delimited response comes
// back out Content-Length-framed on stdout.
func TestBridge_StdinToSocket(t *testing.T) {
	ln := startFakeSocket(t)
	clientConn, serverConn := dialFakeSocket(t, ln)
	defer clientConn.Close()
	defer serverConn.Close()

	stdinR, stdinW := io.Pipe()
	stdout := &syncBuffer{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- bridge(ctx, stdinR, stdout, clientConn)
	}()

	reqBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	go func() {
		io.WriteString(stdinW, frameContentLength(reqBody))
	}()

	serverReader := bufio.NewReader(serverConn)
	line, err := serverReader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read request line from socket: %v", err)
	}
	got := strings.TrimSpace(string(line))
	if got != reqBody {
		t.Fatalf("socket received %q, want %q", got, reqBody)
	}

	respBody := `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25"}}`
	if _, err := serverConn.Write([]byte(respBody + "\n")); err != nil {
		t.Fatalf("write response to socket: %v", err)
	}

	stdoutStr := waitForStdout(t, stdout, respBody, 2*time.Second)
	stdoutReader := bufio.NewReader(strings.NewReader(stdoutStr))
	frameBody, err := readTestFrame(stdoutReader)
	if err != nil {
		t.Fatalf("read Content-Length frame from stdout: %v\nstdout was: %q", err, stdoutStr)
	}
	if frameBody != respBody {
		t.Fatalf("stdout frame body = %q, want %q", frameBody, respBody)
	}

	stdinW.Close()
	cancel()
	<-done
}

// TestBridge_UnsolicitedNotification asserts that a message the fake socket
// server writes without any corresponding stdin request (e.g. an MCP
// notification pushed by wormholed) still arrives Content-Length-framed on
// stdout -- the bridge must not require request/response pairing.
func TestBridge_UnsolicitedNotification(t *testing.T) {
	ln := startFakeSocket(t)
	clientConn, serverConn := dialFakeSocket(t, ln)
	defer clientConn.Close()
	defer serverConn.Close()

	stdinR, stdinW := io.Pipe()
	stdout := &syncBuffer{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- bridge(ctx, stdinR, stdout, clientConn)
	}()

	notif := `{"jsonrpc":"2.0","method":"notifications/wormhole.event","params":{"foo":"bar"}}`
	if _, err := serverConn.Write([]byte(notif + "\n")); err != nil {
		t.Fatalf("write notification to socket: %v", err)
	}

	stdoutStr := waitForStdout(t, stdout, notif, 2*time.Second)
	stdoutReader := bufio.NewReader(strings.NewReader(stdoutStr))
	frameBody, err := readTestFrame(stdoutReader)
	if err != nil {
		t.Fatalf("read Content-Length frame from stdout: %v\nstdout was: %q", err, stdoutStr)
	}
	if frameBody != notif {
		t.Fatalf("stdout frame body = %q, want %q", frameBody, notif)
	}

	stdinW.Close()
	cancel()
	<-done
}

// TestBridge_MalformedContentLength asserts a truncated/malformed
// Content-Length header on stdin is handled without panicking or hanging
// forever -- bridge must return (possibly with an error) rather than block.
func TestBridge_MalformedContentLength(t *testing.T) {
	ln := startFakeSocket(t)
	clientConn, serverConn := dialFakeSocket(t, ln)
	defer clientConn.Close()
	defer serverConn.Close()

	// Malformed header: "Content-Length: notanumber" followed by the
	// blank-line separator and no body at all, then EOF.
	stdin := strings.NewReader("Content-Length: notanumber\r\n\r\n")
	stdout := &syncBuffer{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("bridge panicked: %v", r)
			}
		}()
		done <- bridge(ctx, stdin, stdout, clientConn)
	}()

	select {
	case err := <-done:
		// Any return (nil or error) is fine as long as it doesn't hang or
		// panic. A panic would have been converted to an error above.
		_ = err
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("bridge hung on malformed Content-Length header")
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
