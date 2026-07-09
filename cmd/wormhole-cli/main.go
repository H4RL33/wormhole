package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func usage() string {
	return "usage: wormhole <command> [flags]\n\ncommands:\n  join    join a Wormhole project (RFC-0001 §8.5)"
}

// run dispatches to a subcommand and returns the process exit code. It
// takes explicit args/stdout/stderr so subcommands are testable without
// touching os.Args or os.Exit.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage())
		return 2
	}
	switch args[0] {
	case "join":
		return runJoin(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "wormhole: unknown command %q\n\n%s\n", args[0], usage())
		return 2
	}
}

// callRequest/callResponse mirror internal/mcp.CallRequest/CallResponse's
// JSON shape (internal/mcp/server.go). cmd/wormhole-cli cannot import
// internal/mcp (docs/architecture.md §2 module table restricts this
// package to internal/types and client-side code only, and mcp pulls in
// the server's registry/auth stack), so the wire contract is duplicated
// here instead.
type callRequest struct {
	Tool      string          `json:"tool"`
	ProjectID string          `json:"project_id"`
	Arguments json.RawMessage `json:"arguments"`
}

type callResponse struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// registerAgentInput/registerAgentOutput mirror
// internal/mcp.RegisterAgentInput/RegisterAgentOutput's JSON shape
// (internal/mcp/agent.go), for the same reason as callRequest/callResponse.
type registerAgentInput struct {
	Permissions  []string `json:"permissions"`
	Owner        string   `json:"owner"`
	Model        string   `json:"model"`
	Capabilities []string `json:"capabilities"`
	Repositories []string `json:"repositories"`
	Roles        []string `json:"roles"`
}

type registerAgentOutput struct {
	AgentID      string    `json:"agent_id"`
	PassportID   string    `json:"passport_id"`
	Token        string    `json:"token"`
	Repositories []string  `json:"repositories"`
	Roles        []string  `json:"roles"`
	IssuedAt     time.Time `json:"issued_at"`
}

// credentials is what gets persisted to the token file after a successful
// join, so later join steps (Day 20 KB sync, Day 21 self-introduction) can
// reuse the issued token without re-registering.
type credentials struct {
	Server     string    `json:"server"`
	ProjectID  string    `json:"project_id"`
	AgentID    string    `json:"agent_id"`
	PassportID string    `json:"passport_id"`
	Token      string    `json:"token"`
	IssuedAt   time.Time `json:"issued_at"`
}

// doRegister calls wormhole.agent.register at server's /mcp/tools/call
// endpoint (cmd/wormhole-server/main.go) and decodes the result.
func doRegister(client *http.Client, server, project string, in registerAgentInput) (registerAgentOutput, error) {
	argsRaw, err := json.Marshal(in)
	if err != nil {
		return registerAgentOutput{}, fmt.Errorf("marshal register arguments: %w", err)
	}
	reqBody, err := json.Marshal(callRequest{Tool: "wormhole.agent.register", ProjectID: project, Arguments: argsRaw})
	if err != nil {
		return registerAgentOutput{}, fmt.Errorf("marshal call request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(server, "/")+"/mcp/tools/call", bytes.NewReader(reqBody))
	if err != nil {
		return registerAgentOutput{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return registerAgentOutput{}, fmt.Errorf("call wormhole.agent.register: %w", err)
	}
	defer resp.Body.Close()

	var callResp callResponse
	if err := json.NewDecoder(resp.Body).Decode(&callResp); err != nil {
		return registerAgentOutput{}, fmt.Errorf("decode response: %w", err)
	}
	if callResp.Error != "" {
		return registerAgentOutput{}, fmt.Errorf("%s", callResp.Error)
	}
	if resp.StatusCode != http.StatusOK {
		return registerAgentOutput{}, fmt.Errorf("server returned status %d", resp.StatusCode)
	}

	var out registerAgentOutput
	if err := json.Unmarshal(callResp.Result, &out); err != nil {
		return registerAgentOutput{}, fmt.Errorf("decode register result: %w", err)
	}
	return out, nil
}

// defaultTokenFilePath is where credentials land when --token-file isn't
// given: ~/.wormhole/credentials.json.
func defaultTokenFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".wormhole", "credentials.json"), nil
}

// writeCredentials persists creds to path as indented JSON, creating the
// parent directory if needed. File mode is 0600 (owner read/write only)
// since it contains a live bearer token.
func writeCredentials(path string, creds credentials) error {
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create credentials directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write credentials file: %w", err)
	}
	return nil
}

// runJoin implements join flow step 1 (RFC-0001 §8.5): it calls
// wormhole.agent.register to create a passport and grant permissions, then
// persists the issued credentials. KB sync, self-introduction, and the
// open-task summary are later join steps (Day 20+).
func runJoin(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "Wormhole server base URL (required)")
	project := fs.String("project", "", "project ID to join (required)")
	owner := fs.String("owner", "", "human/org owner of this agent identity")
	model := fs.String("model", "", "model identifier for this agent identity")
	capabilities := fs.String("capabilities", "", "comma-separated list of agent capabilities")
	repositories := fs.String("repositories", "", "comma-separated list of git repositories this identity is scoped to")
	roles := fs.String("roles", "", "comma-separated list of project-level roles")
	permissions := fs.String("permissions", "", "comma-separated list of permissions to request (e.g. task.create,kb.write)")
	tokenFile := fs.String("token-file", "", "path to write issued credentials to (default: ~/.wormhole/credentials.json)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *server == "" || *project == "" {
		fmt.Fprintln(stderr, "wormhole join: --server and --project are required")
		fs.Usage()
		return 2
	}

	splitOrNil := func(s string) []string {
		if s == "" {
			return nil
		}
		return strings.Split(s, ",")
	}
	// identity.Store.Register rejects a nil permissions slice with
	// ErrInvalidScope (internal/core/identity/identity.go:99); an empty
	// slice is fine, nil is not, so permissions always gets an explicit
	// default here even though the other flags can stay nil.
	splitOrEmpty := func(s string) []string {
		if s == "" {
			return []string{}
		}
		return strings.Split(s, ",")
	}

	in := registerAgentInput{
		Permissions:  splitOrEmpty(*permissions),
		Owner:        *owner,
		Model:        *model,
		Capabilities: splitOrNil(*capabilities),
		Repositories: splitOrNil(*repositories),
		Roles:        splitOrNil(*roles),
	}

	out, err := doRegister(http.DefaultClient, *server, *project, in)
	if err != nil {
		fmt.Fprintf(stderr, "wormhole join: %v\n", err)
		return 1
	}

	path := *tokenFile
	if path == "" {
		defaultPath, err := defaultTokenFilePath()
		if err != nil {
			fmt.Fprintf(stderr, "wormhole join: %v\n", err)
			return 1
		}
		path = defaultPath
	}

	creds := credentials{
		Server:     *server,
		ProjectID:  *project,
		AgentID:    out.AgentID,
		PassportID: out.PassportID,
		Token:      out.Token,
		IssuedAt:   out.IssuedAt,
	}
	if err := writeCredentials(path, creds); err != nil {
		fmt.Fprintf(stderr, "wormhole join: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, "Passport created.")
	fmt.Fprintf(stdout, "agent_id=%s passport_id=%s project=%s\n", out.AgentID, out.PassportID, *project)
	fmt.Fprintf(stdout, "credentials written to %s\n", path)
	fmt.Fprintln(stdout, "KB sync, self-introduction, and task summary land Day 20+ (RFC-0001 §8.5)")
	return 0
}
