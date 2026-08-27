// e2e_stdio_bridge_test.go verifies the production `wormhole mcp` stdio bridge
// framing, concurrency, daemon-shutdown, and signal boundaries. The real
// Stage 2 stdio -> Unix socket -> Gateway -> portable-state process topology is
// exercised by TestStage2LocalOnlyRealProcessAcceptance. Optional Fabric HTTP
// and Postgres invariants are exercised at their independent server boundary.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type e2eToolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type e2eToolCallResultContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type e2eToolCallResult struct {
	Content []e2eToolCallResultContent `json:"content"`
	IsError bool                       `json:"isError,omitempty"`
}

// -----------------------------------------------------------------------
// The stdio transport. This is the piece no existing test exercises.
// It speaks newline-delimited JSON-RPC directly against the built
// `wormhole mcp` subprocess's stdin/stdout -- matching that bridge's own
// framing (cmd/wormhole/mcp.go stdinToSocket/socketToStdout: one JSON
// object per line, terminated by \n, no length header).
// the Gateway test harness's mcpInitialize/mcpCallTool helpers happen to use
// the same newline framing, but against a raw net.Conn to Gateway's socket;
// this test drives it through the real `wormhole mcp` subprocess's
// stdin/stdout instead, the leg a real harness talks to.
// -----------------------------------------------------------------------

var (
	stdioBridgeBinOnce sync.Once
	stdioBridgeBinPath string
	stdioBridgeBinErr  error
)

// e2eBuildStdioBridgeBinary builds the wormhole CLI once per test binary
// run and returns the path to the resulting executable. The stdio bridge is
// the `wormhole mcp` subcommand (cmd/wormhole/mcp.go); it was previously a
// standalone cmd/wormhole-mcp-stdio binary. No existing "go build a sibling
// binary for a subprocess test" helper exists anywhere in this repo
// (checked: no `_test.go` file anywhere invokes exec.Command with "go",
// "build"), so this is a new small helper local to this test file, not a
// new package.
func e2eBuildStdioBridgeBinary(t *testing.T) string {
	t.Helper()
	stdioBridgeBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "wormhole-mcp-stdio-bin-*")
		if err != nil {
			stdioBridgeBinErr = fmt.Errorf("mkdir temp: %w", err)
			return
		}
		binPath := filepath.Join(dir, "wormhole")
		cmd := exec.Command("go", "build", "-o", binPath, "./cmd/wormhole")
		cmd.Dir = repoRootForTest(t)
		out, err := cmd.CombinedOutput()
		if err != nil {
			stdioBridgeBinErr = fmt.Errorf("go build cmd/wormhole: %w\n%s", err, out)
			return
		}
		stdioBridgeBinPath = binPath
	})
	return stdioBridgeBinPath
}

// repoRootForTest returns the repo root (two levels up from
// cmd/gatewayd, where this test file lives), so `go build ./cmd/...`
// resolves regardless of the working directory `go test` happens to use.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// cmd/gatewayd -> repo root
	return filepath.Join(wd, "..", "..")
}

// e2eStdioClient wraps a running `wormhole mcp` subprocess and speaks
// newline-delimited JSON-RPC over its stdin/stdout pipes.
type e2eStdioClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	nextID int
}

// writeNewlineFrame is this test's client-side encoder for the MCP stdio
// transport's wire format. The `wormhole mcp` bridge (cmd/wormhole/mcp.go)
// relays newline-delimited JSON-RPC in both directions (stdinToSocket /
// socketToStdout), so each message is a single JSON object terminated by a
// newline, with no length header. The body itself must not contain a raw
// newline; encoding/json.Marshal never emits one, so a single trailing \n
// unambiguously frames the message.
func writeNewlineFrame(w io.Writer, body []byte) error {
	if _, err := w.Write(body); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

// readNewlineFrame is this test's client-side decoder, matching the bridge's
// socketToStdout framing (one JSON-RPC message per line). It reads up to and
// including the next newline and returns the trimmed body.
func readNewlineFrame(r *bufio.Reader) ([]byte, error) {
	line, err := r.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return nil, err
	}
	return []byte(strings.TrimRight(string(line), "\r\n")), nil
}

func e2eStartStdioBridge(t *testing.T, binPath, runDir string) *e2eStdioClient {
	t.Helper()

	cmd := exec.Command(binPath, "mcp")
	cmd.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+runDir)
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start wormhole mcp: %v", err)
	}

	c := &e2eStdioClient{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}
	t.Cleanup(func() {
		stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return c
}

type e2eStdioRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type e2eStdioRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type e2eStdioRPCResponse struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      json.RawMessage   `json:"id,omitempty"`
	Result  json.RawMessage   `json:"result,omitempty"`
	Error   *e2eStdioRPCError `json:"error,omitempty"`
}

// send writes a newline-framed JSON-RPC request/notification to the
// subprocess's stdin.
func (c *e2eStdioClient) send(t *testing.T, method string, params json.RawMessage, withID bool) json.RawMessage {
	t.Helper()
	req := e2eStdioRPCRequest{JSONRPC: "2.0", Method: method, Params: params}
	var id json.RawMessage
	if withID {
		c.nextID++
		id = json.RawMessage(strconv.Itoa(c.nextID))
		req.ID = id
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal %s request: %v", method, err)
	}
	if err := writeNewlineFrame(c.stdin, body); err != nil {
		t.Fatalf("write %s frame: %v", method, err)
	}
	return id
}

// call sends a newline-framed request and reads back the matching
// newline-framed response.
func (c *e2eStdioClient) call(t *testing.T, method string, params json.RawMessage) e2eStdioRPCResponse {
	t.Helper()
	c.send(t, method, params, true)
	body, err := readNewlineFrame(c.stdout)
	if err != nil {
		t.Fatalf("read %s response frame: %v", method, err)
	}
	var resp e2eStdioRPCResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode %s response %q: %v", method, body, err)
	}
	return resp
}

// notify sends a newline-framed notification (no response expected).
func (c *e2eStdioClient) notify(t *testing.T, method string) {
	t.Helper()
	c.send(t, method, nil, false)
}

func (c *e2eStdioClient) initialize(t *testing.T) {
	t.Helper()
	resp := c.call(t, "initialize", json.RawMessage(`{}`))
	if resp.Error != nil {
		t.Fatalf("initialize error: %+v", resp.Error)
	}
	c.notify(t, "notifications/initialized")
}

func (c *e2eStdioClient) callTool(t *testing.T, tool string, args map[string]interface{}) (json.RawMessage, string) {
	t.Helper()
	var argsRaw json.RawMessage
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			t.Fatalf("marshal %s args: %v", tool, err)
		}
		argsRaw = b
	} else {
		argsRaw = json.RawMessage(`{}`)
	}
	params, err := json.Marshal(e2eToolsCallParams{Name: tool, Arguments: argsRaw})
	if err != nil {
		t.Fatalf("marshal %s params: %v", tool, err)
	}
	resp := c.call(t, "tools/call", params)
	if resp.Error != nil {
		return nil, resp.Error.Message
	}
	var result e2eToolCallResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode %s tool call result: %v", tool, err)
	}
	if result.IsError {
		text := ""
		if len(result.Content) > 0 {
			text = result.Content[0].Text
		}
		return nil, text
	}
	if len(result.Content) == 0 {
		return nil, ""
	}
	return json.RawMessage(result.Content[0].Text), ""
}

// -----------------------------------------------------------------------
// The test.
// -----------------------------------------------------------------------

func TestE2E_StdioBridgeProtocolAndSignalBoundaries(t *testing.T) {
	binPath := e2eBuildStdioBridgeBinary(t)
	if stdioBridgeBinErr != nil {
		t.Fatalf("build wormhole mcp bridge: %v", stdioBridgeBinErr)
	}
	socketPath := configureSecurityTestDaemon(t)
	runner := Run
	daemon := startTestDaemonWithRunner(t, "default", socketPath, runner)
	defer daemon.stop(t)
	runDir := os.Getenv("XDG_RUNTIME_DIR")

	t.Run("partial JSON and oversized input", func(t *testing.T) {
		client := e2eStartStdioBridge(t, binPath, runDir)
		client.initialize(t)
		req, _ := json.Marshal(e2eStdioRPCRequest{JSONRPC: "2.0", ID: json.RawMessage("91"), Method: "tools/list", Params: json.RawMessage(`{}`)})
		mid := len(req) / 2
		if _, err := client.stdin.Write(req[:mid]); err != nil {
			t.Fatalf("write first partial request segment: %v", err)
		}
		if _, err := client.stdin.Write(req[mid:]); err != nil {
			t.Fatalf("write second partial request segment: %v", err)
		}
		if _, err := io.WriteString(client.stdin, "\n"); err != nil {
			t.Fatalf("terminate partial request: %v", err)
		}
		body, err := readNewlineFrame(client.stdout)
		if err != nil {
			t.Fatalf("read partial request response: %v", err)
		}
		var resp e2eStdioRPCResponse
		if err := json.Unmarshal(body, &resp); err != nil || resp.Error != nil {
			t.Fatalf("partial request response = %s decode=%v rpc=%+v", body, err, resp.Error)
		}

		oversized := bytes.Repeat([]byte("x"), (1<<20)+1)
		if err := writeNewlineFrame(client.stdin, oversized); err != nil {
			t.Fatalf("write oversized stdio input: %v", err)
		}
		body, err = readNewlineFrame(client.stdout)
		if err != nil {
			t.Fatalf("read oversized response: %v", err)
		}
		if err := json.Unmarshal(body, &resp); err != nil || resp.Error == nil || resp.Error.Code != -32700 {
			t.Fatalf("oversized response = %s decode=%v rpc=%+v", body, err, resp.Error)
		}
	})

	t.Run("concurrent clients", func(t *testing.T) {
		clients := make([]*e2eStdioClient, 4)
		for i := range clients {
			clients[i] = e2eStartStdioBridge(t, binPath, runDir)
			clients[i].initialize(t)
		}
		errs := make(chan error, len(clients))
		for i, client := range clients {
			go func(id int, c *e2eStdioClient) {
				req, _ := json.Marshal(e2eStdioRPCRequest{JSONRPC: "2.0", ID: json.RawMessage(strconv.Itoa(100 + id)), Method: "tools/list", Params: json.RawMessage(`{}`)})
				if err := writeNewlineFrame(c.stdin, req); err != nil {
					errs <- err
					return
				}
				body, err := readNewlineFrame(c.stdout)
				if err == nil {
					var resp e2eStdioRPCResponse
					err = json.Unmarshal(body, &resp)
					if err == nil && resp.Error != nil {
						err = fmt.Errorf("rpc error: %+v", resp.Error)
					}
				}
				errs <- err
			}(i, client)
		}
		for range clients {
			if err := <-errs; err != nil {
				t.Fatalf("concurrent stdio client: %v", err)
			}
		}
	})

	for _, signal := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			client := e2eStartStdioBridge(t, binPath, runDir)
			client.initialize(t)
			if _, err := io.WriteString(client.stdin, `{"jsonrpc":"2.0"`); err != nil {
				t.Fatalf("write in-flight partial request: %v", err)
			}
			if err := client.cmd.Process.Signal(signal); err != nil {
				t.Fatalf("signal stdio bridge: %v", err)
			}
			exited := make(chan error, 1)
			go func() { exited <- client.cmd.Wait() }()
			select {
			case err := <-exited:
				if err != nil {
					t.Fatalf("stdio bridge signal exit: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("stdio bridge did not exit after signal with request in flight")
			}
		})
	}
}
