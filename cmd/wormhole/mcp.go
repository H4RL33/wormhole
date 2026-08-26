package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
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

var errDuplicateMCPJSONMember = errors.New("duplicate MCP JSON member")

// runMCP implements the MCP stdio↔socket bridge subcommand.
// It dials Gateway's local socket and relays newline-delimited JSON-RPC
// messages between stdin/stdout and the socket until either side closes
// or SIGINT/SIGTERM is received.
func runMCP(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintf(stderr, "wormhole mcp: no flags supported\n")
		return 1
	}

	socketPath := gatewaySocketPath()
	conn, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		fmt.Fprintf(stderr, "wormhole mcp: dial gatewayd socket %s: %v\n", socketPath, err)
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
// MCP client (stdin/stdout) and Gateway's socket (conn). Both sides use the
// same newline-delimited JSON-RPC framing. The stdin half performs one narrow
// bridge responsibility: it overwrites tools/call's private cwd envelope.
// All public MCP semantics remain Gateway's responsibility.
//
// Two goroutines do the actual copying: one drains stdin -> conn, the other
// drains conn -> stdout. Shutdown is synchronized by closing conn on signal
// or error, which unblocks whichever goroutine is blocked on conn at the time.
// A genuine transport error on either side also closes conn, so a permanently
// broken half doesn't leave the other half blocked forever.
func bridge(stdin io.Reader, stdout io.Writer, conn net.Conn) error {
	var closeOnce sync.Once
	forceClose := func() { closeOnce.Do(func() { conn.Close() }) }

	errs := make(chan error, 2)

	go func() {
		errs <- stdinToSocket(stdin, conn)
	}()

	go func() {
		errs <- socketToStdout(conn, stdout)
	}()

	// Either half ending terminates the session. Closing conn releases the
	// socket reader/writer; closing a closable stdin (os.Stdin in production)
	// releases a bridge blocked waiting for another client frame after the
	// daemon has disappeared.
	firstResult := <-errs
	forceClose()
	if closer, ok := stdin.(io.Closer); ok {
		_ = closer.Close()
	}
	var first error
	for _, err := range []error{firstResult} {
		// net.ErrClosed is the expected result of our own forceClose tearing
		// down conn to unblock the reader; it is not a session error.
		if err != nil && err != io.EOF && !errors.Is(err, net.ErrClosed) && first == nil {
			first = err
		}
	}
	return first
}

// stdinToSocket reads successive newline-delimited JSON-RPC messages off r,
// observes cwd exactly once for each tools/call, overwrites that call's private
// context, and writes each frame to conn. Returns io.EOF on a clean end of input
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
			raw := json.RawMessage(body)
			method, inspectErr := inspectMCPMethod(raw)
			if errors.Is(inspectErr, errDuplicateMCPJSONMember) {
				return inspectErr
			}
			forwarded := append(json.RawMessage(nil), raw...)
			if inspectErr == nil && method == "tools/call" {
				cwd, cwdErr := os.Getwd()
				if cwdErr != nil {
					return fmt.Errorf("observe working directory: %w", cwdErr)
				}
				var contextErr error
				forwarded, contextErr = attachToolsCallPrivateRequestContext(raw, cwd)
				if contextErr != nil {
					return contextErr
				}
			}
			if _, werr := conn.Write(append(forwarded, '\n')); werr != nil {
				return werr
			}
		}
		if err != nil {
			return err
		}
	}
}

// attachPrivateRequestContext overwrites any harness-supplied private cwd on
// tools/call. Other MCP messages remain byte-for-byte unchanged.
func attachPrivateRequestContext(raw json.RawMessage, cwd string) (json.RawMessage, error) {
	method, err := inspectMCPMethod(raw)
	if err != nil {
		return nil, err
	}
	if method != "tools/call" {
		return append(json.RawMessage(nil), raw...), nil
	}
	return attachToolsCallPrivateRequestContext(raw, cwd)
}

func attachToolsCallPrivateRequestContext(raw json.RawMessage, cwd string) (json.RawMessage, error) {
	canonical, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("canonicalize working directory: %w", err)
	}
	canonical, err = filepath.EvalSymlinks(canonical)
	if err != nil {
		return nil, fmt.Errorf("canonicalize working directory: %w", err)
	}
	canonical = filepath.Clean(canonical)
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("canonicalize working directory: not a directory")
	}

	var request map[string]json.RawMessage
	if err := json.Unmarshal(raw, &request); err != nil || request == nil {
		return nil, fmt.Errorf("decode MCP request object")
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(request["params"], &params); err != nil || params == nil {
		return nil, fmt.Errorf("tools/call params must be an object")
	}
	arguments := map[string]json.RawMessage{}
	if current := params["arguments"]; len(current) != 0 && string(current) != "null" {
		if err := json.Unmarshal(current, &arguments); err != nil || arguments == nil {
			return nil, fmt.Errorf("tools/call arguments must be an object")
		}
	}
	privateContext, err := json.Marshal(struct {
		WorkingDirectory string `json:"working_directory"`
	}{WorkingDirectory: canonical})
	if err != nil {
		return nil, fmt.Errorf("encode private workspace context: %w", err)
	}
	arguments["_wormhole_workspace"] = privateContext
	params["arguments"], err = json.Marshal(arguments)
	if err != nil {
		return nil, fmt.Errorf("encode tools/call arguments: %w", err)
	}
	request["params"], err = json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("encode tools/call params: %w", err)
	}
	forwarded, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode MCP request: %w", err)
	}
	return forwarded, nil
}

func inspectMCPMethod(raw json.RawMessage) (string, error) {
	if err := rejectDuplicateMCPJSONMembers(raw); err != nil {
		return "", fmt.Errorf("decode MCP request for private context: %w", err)
	}
	var method struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(raw, &method); err != nil {
		return "", fmt.Errorf("decode MCP request for private context: %w", err)
	}
	return method.Method, nil
}

func rejectDuplicateMCPJSONMembers(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var visit func() error
	visit = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("non-string object member")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("%w: duplicate object member %q", errDuplicateMCPJSONMember, key)
				}
				seen[key] = struct{}{}
				if err := visit(); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := visit(); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		default:
			return errors.New("unexpected JSON delimiter")
		}
	}
	if err := visit(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

// socketToStdout reads successive newline-delimited JSON-RPC messages off
// conn and writes each one straight to w with a trailing newline -- no
// re-framing, since the MCP stdio transport uses the same newline-delimited
// framing as Gateway's socket. This direction carries both tools/call
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
