// Command wormhole-mcp-stdio is a pure framing translator between the MCP
// stdio transport (Content-Length-prefixed framing, the LSP convention) and
// wormholed's local Unix domain socket (newline-delimited JSON-RPC 2.0, per
// docs/superpowers/plans/2026-07-16-wormholed-mcp-endpoint-design.md §2,
// §5 subtask 3). It carries no MCP semantics of its own -- initialize,
// tools/list, tools/call, and notification handling all live in wormholed
// on the other end of the socket (internal/runtime/localapi/mcp.go). This
// binary only finds message boundaries and re-frames them.
//
// Usage: wormhole-mcp-stdio
//
// A harness (Claude Code, OpenCode, or any MCP stdio client) spawns this as
// a subprocess and talks MCP stdio framing to its stdin/stdout; this
// process dials wormholed's socket and relays every message in both
// directions until either side disconnects or the process receives
// SIGINT/SIGTERM.
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
	"strconv"
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

// wormholedSocketPath derives wormholed's local API socket path. Duplicated
// from cmd/wormhole-cli/main.go's wormholedSocketPath (main.go:257-267)
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
// MCP client (stdin/stdout, Content-Length-prefixed framing) and wormholed's
// socket (conn, newline-delimited JSON-RPC framing). It performs no
// interpretation of message contents -- only framing translation -- so all
// MCP semantics (initialize, tools/list, tools/call, notifications) remain
// wormholed's responsibility on the other end of conn.
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
			// A genuine framing error (malformed Content-Length, truncated
			// body, transport error) on this side means the session can't
			// continue -- tear down the shared connection so the other
			// goroutine, likely blocked reading conn, doesn't hang forever.
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

// stdinToSocket reads successive Content-Length-framed MCP messages off r,
// strips the framing, and writes each message's raw JSON body followed by a
// newline to conn -- matching wormholed's newline-delimited socket framing
// (internal/runtime/localapi/localapi.go's handle(), which reads with
// bufio.Reader.ReadBytes('\n')). Returns io.EOF on a clean end of input,
// any other error on a malformed frame or write failure.
func stdinToSocket(r io.Reader, conn net.Conn) error {
	br := bufio.NewReader(r)
	for {
		body, err := readContentLengthFrame(br)
		if err != nil {
			return err
		}
		if _, err := conn.Write(append(body, '\n')); err != nil {
			return err
		}
	}
}

// socketToStdout reads successive newline-delimited JSON-RPC messages off
// conn and writes each one to w wrapped in Content-Length framing, the MCP
// stdio transport's convention. This is the direction that carries both
// tools/call responses and unsolicited server-to-client notifications
// (design doc §1, §2) -- it does not wait for or pair against anything
// written by stdinToSocket, since wormholed may push a notification (e.g.
// channel.subscribe delivery) at any time.
func socketToStdout(conn net.Conn, w io.Writer) error {
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadBytes('\n')
		if body := bytes.TrimRight(line, "\r\n"); len(body) > 0 {
			frame := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
			if _, werr := w.Write([]byte(frame)); werr != nil {
				return werr
			}
			if _, werr := w.Write(body); werr != nil {
				return werr
			}
		}
		if err != nil {
			return err
		}
	}
}

// readContentLengthFrame reads one Content-Length-prefixed MCP stdio
// message: a block of "Header: value\r\n" lines terminated by a blank line,
// followed by exactly Content-Length bytes of message body. Unknown headers
// (e.g. Content-Type, which the MCP stdio spec permits) are read and
// ignored. Returns io.EOF if the stream ends cleanly before any header
// bytes are read (a clean shutdown, not an error); any other error
// (malformed/non-numeric Content-Length, missing Content-Length header, a
// truncated body) is returned as-is so the caller can distinguish it from a
// clean end of input.
func readContentLengthFrame(r *bufio.Reader) ([]byte, error) {
	contentLength := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			continue // e.g. Content-Type: ignored, this bridge doesn't interpret bodies
		}
		n, convErr := strconv.Atoi(strings.TrimSpace(value))
		if convErr != nil {
			return nil, fmt.Errorf("wormhole-mcp-stdio: malformed Content-Length header %q: %w", line, convErr)
		}
		contentLength = n
	}
	if contentLength < 0 {
		return nil, fmt.Errorf("wormhole-mcp-stdio: message frame missing Content-Length header")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}
