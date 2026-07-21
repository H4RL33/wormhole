package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// runMCP implements the MCP stdio↔socket bridge subcommand.
// It dials wormholed's local socket and relays newline-delimited JSON-RPC
// messages between stdin/stdout and the socket until either side closes
// or SIGINT/SIGTERM is received.
func runMCP(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintf(stderr, "wormhole mcp: no flags supported\n")
		return 1
	}

	socketPath := wormholedSocketPath()
	conn, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		fmt.Fprintf(stderr, "wormhole mcp: dial wormholed socket %s: %v\n", socketPath, err)
		return 1
	}
	defer conn.Close()

	// Handle SIGINT/SIGTERM to close gracefully
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		conn.Close()
		os.Exit(0)
	}()

	if err := bridge(os.Stdin, stdout, conn); err != nil {
		fmt.Fprintf(stderr, "wormhole mcp: %v\n", err)
		return 1
	}
	return 0
}

// bridge relays MCP JSON-RPC messages in both directions between a stdio
// MCP client (stdin/stdout) and wormholed's socket (conn). Both sides use
// the same newline-delimited JSON-RPC framing, so this is a straight copy
// with no re-framing. It performs no interpretation of message contents, so
// all MCP semantics (initialize, tools/list, tools/call, notifications)
// remain wormholed's responsibility on the other end of conn.
//
// Two goroutines do the actual copying: one drains stdin -> conn, the other
// drains conn -> stdout. Shutdown is synchronized by closing conn on signal
// or error, which unblocks whichever goroutine is blocked on conn at the time.
// A genuine transport error on either side also closes conn, so a permanently
// broken half doesn't leave the other half blocked forever.
func bridge(stdin io.Reader, stdout io.Writer, conn net.Conn) error {
	var closeOnce sync.Once
	forceClose := func() { closeOnce.Do(func() { conn.Close() }) }

	var wg sync.WaitGroup
	errs := make(chan error, 2)

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := stdinToSocket(stdin, conn)
		// Once stdin ends -- cleanly (EOF) or on a transport error -- the
		// client can send no further requests, so the session is over. Tear
		// down the shared connection so socketToStdout, otherwise blocked
		// reading conn, doesn't hang forever.
		forceClose()
		errs <- err
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		errs <- socketToStdout(conn, stdout)
	}()

	wg.Wait()
	close(errs)

	var first error
	for err := range errs {
		// net.ErrClosed is the expected result of our own forceClose tearing
		// down conn to unblock the reader; it is not a session error.
		if err != nil && err != io.EOF && !errors.Is(err, net.ErrClosed) && first == nil {
			first = err
		}
	}
	return first
}

// stdinToSocket reads successive newline-delimited JSON-RPC messages off r
// and writes each one straight to conn with a trailing newline, matching
// wormholed's socket framing. Returns io.EOF on a clean end of input
// (r closes exactly on a line boundary), any other error on a read or
// write failure.
func stdinToSocket(r io.Reader, conn net.Conn) error {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if err == io.EOF && len(line) > 0 {
			return fmt.Errorf("stdin closed mid-message (no trailing newline)")
		}
		if body := strings.TrimRight(line, "\r\n"); len(body) > 0 {
			if _, werr := conn.Write([]byte(body + "\n")); werr != nil {
				return werr
			}
		}
		if err != nil {
			return err
		}
	}
}

// socketToStdout reads successive newline-delimited JSON-RPC messages off
// conn and writes each one straight to w with a trailing newline -- no
// re-framing, since the MCP stdio transport uses the same newline-delimited
// framing as wormholed's socket. This direction carries both tools/call
// responses and unsolicited server-to-client notifications.
func socketToStdout(conn net.Conn, w io.Writer) error {
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadBytes('\n')
		if err == io.EOF && len(line) > 0 {
			return fmt.Errorf("socket closed mid-message (no trailing newline)")
		}
		if body := bytes.TrimRight(line, "\r\n"); len(body) > 0 {
			if _, werr := w.Write(append(body, '\n')); werr != nil {
				return werr
			}
		}
		if err != nil {
			return err
		}
	}
}
