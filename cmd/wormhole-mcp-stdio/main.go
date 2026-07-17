// Command wormhole-mcp-stdio is a plain bidirectional relay between the MCP
// stdio transport and wormholed's local Unix domain socket. Both sides speak
// the same framing -- newline-delimited JSON-RPC 2.0, one message per line,
// no embedded newlines (MCP stdio transport spec,
// https://modelcontextprotocol.io/specification/2025-06-18/basic/transports;
// wormholed socket side per
// docs/superpowers/plans/2026-07-16-wormholed-mcp-endpoint-design.md §2,
// §5 subtask 3) -- so this binary performs no framing translation, only
// copying. It carries no MCP semantics of its own -- initialize,
// tools/list, tools/call, and notification handling all live in wormholed
// on the other end of the socket (internal/runtime/localapi/mcp.go). This
// binary only finds message (line) boundaries and forwards them.
//
// Usage: wormhole-mcp-stdio
//
// A harness (Claude Code, OpenCode, or any MCP stdio client) spawns this as
// a subprocess and writes/reads newline-delimited JSON-RPC on its
// stdin/stdout; this process dials wormholed's socket and relays every
// message in both directions until either side disconnects or the process
// receives SIGINT/SIGTERM.
package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	sockPath := wormholedSocketPath()
	conn, err := net.DialTimeout("unix", sockPath, 5*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wormhole-mcp-stdio: dial wormholed socket %s: %v\n", sockPath, err)
		os.Exit(1)
	}
	defer conn.Close()

	if err := bridge(ctx, os.Stdin, os.Stdout, conn); err != nil {
		fmt.Fprintf(os.Stderr, "wormhole-mcp-stdio: %v\n", err)
		os.Exit(1)
	}
}

// wormholedSocketPath derives wormholed's local API socket path (no framing
// logic here -- see bridge, stdinToSocket, socketToStdout for the relay).
// Duplicated from cmd/wormhole-cli/main.go's wormholedSocketPath (main.go:257-267)
// rather than imported/shared, matching the design doc's (§4, §5 subtask 3)
// duplication posture: internal/runtime/* and cmd/* boundaries in this repo
// already duplicate this exact derivation once (wormhole-cli), and adding a
// second cmd/-side duplicate is the smaller/lower-risk call the design
// doc's subtask-3 breakdown explicitly leaves to this binary's implementer
// rather than factoring into a shared package.
//
// Note: the socket path is NOT scoped by credential profile -- profile only
// selects which credentials wormholed loads at startup (config.Load,
// internal/runtime/config/config.go), it does not change wormholedSocketPath
// itself (see cmd/wormhole-cli/main.go:261-267 and cmd/wormholed/main.go,
// wormholed.go: Run(ctx, profileName) passes profileName only to
// config.Load, never into the socket path). So this bridge takes no profile
// argument -- there is nothing for it to scope.
func wormholedSocketPath() string {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = filepath.Join(os.TempDir(), "wormhole-runtime")
	}
	return filepath.Join(runtimeDir, "wormhole", "wormholed.sock")
}

// bridge relays MCP JSON-RPC messages in both directions between a stdio
// MCP client (stdin/stdout) and wormholed's socket (conn). Both sides use
// the same newline-delimited JSON-RPC framing, so this is a straight copy
// with no re-framing. It performs no interpretation of message contents, so
// all MCP semantics (initialize, tools/list, tools/call, notifications)
// remain wormholed's responsibility on the other end of conn.
//
// Two goroutines do the actual copying: one drains stdin -> conn, the other
// drains conn -> stdout. Shutdown is synchronized the same way
// localapi.Server.Serve synchronizes its accept loop against ctx
// cancellation (internal/runtime/localapi/localapi.go, Serve/Close): a
// watcher goroutine closes conn when ctx is cancelled, which unblocks
// whichever goroutine is blocked on conn at the time. A genuine transport
// error on either side (as opposed to a clean EOF, which just means that
// side's peer stopped writing) also closes conn, so a permanently broken
// half doesn't leave the other half blocked forever.
func bridge(ctx context.Context, stdin io.Reader, stdout io.Writer, conn net.Conn) error {
	var closeOnce sync.Once
	forceClose := func() { closeOnce.Do(func() { conn.Close() }) }

	watcherDone := make(chan struct{})
	defer close(watcherDone)
	go func() {
		select {
		case <-ctx.Done():
			forceClose()
		case <-watcherDone:
		}
	}()

	var wg sync.WaitGroup
	errs := make(chan error, 2)

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := stdinToSocket(stdin, conn)
		if err != nil && err != io.EOF {
			// A genuine transport error on this side means the session
			// can't continue -- tear down the shared connection so the
			// other goroutine, likely blocked reading conn, doesn't hang
			// forever.
			forceClose()
		}
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
		if err != nil && err != io.EOF && first == nil {
			first = err
		}
	}
	return first
}

// stdinToSocket reads successive newline-delimited JSON-RPC messages off r
// and writes each one straight to conn with a trailing newline, matching
// wormholed's socket framing (internal/runtime/localapi/localapi.go's
// handle(), which reads with bufio.Reader.ReadBytes('\n')). Returns io.EOF
// on a clean end of input (r closes exactly on a line boundary), any other
// error -- including a non-nil, non-EOF error wrapping io.EOF when r closes
// mid-line, since a truncated final message is not a clean shutdown -- on a
// read or write failure.
func stdinToSocket(r io.Reader, conn net.Conn) error {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if err == io.EOF && len(line) > 0 {
			return fmt.Errorf("wormhole-mcp-stdio: stdin closed mid-message (no trailing newline)")
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
// framing as wormholed's socket. This is the direction that carries both
// tools/call responses and unsolicited server-to-client notifications
// (design doc §1, §2) -- it does not wait for or pair against anything
// written by stdinToSocket, since wormholed may push a notification (e.g.
// channel.subscribe delivery) at any time.
func socketToStdout(conn net.Conn, w io.Writer) error {
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadBytes('\n')
		if err == io.EOF && len(line) > 0 {
			return fmt.Errorf("wormhole-mcp-stdio: socket closed mid-message (no trailing newline)")
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
