package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func usage() string {
	return "usage: wormhole <command> [flags]\n\ncommands:\n  join          join a Wormhole project (RFC-0001 §8.5)\n  connect       join a project and register it as a Claude Code MCP connector\n  whoami        show the active (or a named) credential profile\n  profile list  list all stored credential profiles"
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
	case "connect":
		return runConnect(args[1:], stdout, stderr)
	case "whoami":
		return runWhoami(args[1:], stdout, stderr)
	case "profile":
		return runProfile(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "wormhole: unknown command %q\n\n%s\n", args[0], usage())
		return 2
	}
}

// rpcRequest/rpcResponse/rpcError/toolsCallParams/toolCallResult mirror
// internal/mcp's JSON-RPC 2.0 wire shapes (internal/mcp/jsonrpc.go,
// docs/mcp-protocol.md §3-§4). cmd/wormhole-cli cannot import internal/mcp
// (docs/architecture.md §2 module table restricts this package to
// internal/types and client-side code only, and mcp pulls in the server's
// registry/auth stack), so the wire contract is duplicated here instead,
// same pattern as registerAgentInput/registerAgentOutput below.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type toolCallResultContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolCallResult struct {
	Content []toolCallResultContent `json:"content"`
	IsError bool                    `json:"isError,omitempty"`
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
	Role         string   `json:"role,omitempty"`
}

type registerAgentOutput struct {
	AgentID      string    `json:"agent_id"`
	PassportID   string    `json:"passport_id"`
	Token        string    `json:"token"`
	Repositories []string  `json:"repositories"`
	Roles        []string  `json:"roles"`
	IssuedAt     time.Time `json:"issued_at"`
	Role         string    `json:"role,omitempty"`
}

// searchArticlesInput mirrors internal/mcp.SearchArticlesInput's JSON
// shape (internal/mcp/kb.go). searchArticlesOutput/articleSummary are a
// deliberately partial mirror of SearchArticlesOutput/ArticleSummary: the
// CLI only needs article_id and title for the join-time sync summary, and
// encoding/json safely ignores the other fields (body, frontmatter,
// author_agent_id, created_at, updated_at) on decode.
type searchArticlesInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

type articleSummary struct {
	ArticleID string `json:"article_id"`
	Title     string `json:"title"`
}

type searchArticlesOutput struct {
	Articles []articleSummary `json:"articles"`
}

type channelSummary struct {
	ChannelID string `json:"channel_id"`
	Name      string `json:"name"`
}

type listChannelsOutput struct {
	Channels []channelSummary `json:"channels"`
}

type postEventInput struct {
	ChannelID string          `json:"channel_id"`
	EventType string          `json:"event_type"`
	Payload   json.RawMessage `json:"payload"`
	Note      *string         `json:"note"`
}

type postEventOutput struct {
	EventID string `json:"event_id"`
}

type taskSummary struct {
	Status string `json:"status"`
}

type listTasksOutput struct {
	Tasks []taskSummary `json:"tasks"`
}

// credentials is what gets persisted to the token file after a successful
// join, so later join steps (Day 21 self-introduction) can reuse the
// issued token without re-registering.
type credentials struct {
	Server     string    `json:"server"`
	ProjectID  string    `json:"project_id"`
	AgentID    string    `json:"agent_id"`
	PassportID string    `json:"passport_id"`
	Token      string    `json:"token"`
	IssuedAt   time.Time `json:"issued_at"`
	// Role is the resolved role template name (Chapter 6's --role), empty
	// when join/connect ran without one. Chapter 8: read back by
	// listCredentialProfiles/resolveWhoamiProfile for `wormhole whoami` /
	// `wormhole profile list` display.
	Role string `json:"role,omitempty"`
}

// callTool sends one JSON-RPC 2.0 "tools/call" request to server's single
// /mcp endpoint (docs/mcp-protocol.md §2-§4.1, internal/mcp/jsonrpc.go) and
// returns the decoded tool result's raw JSON. project_id is folded into
// arguments, not sent as a sibling field (§4.1 — there is no envelope
// field for it). token is optional: pass "" for tools that don't require
// auth (e.g. wormhole.agent.register); a non-empty token is sent as a
// bearer Authorization header for tools that do (e.g. wormhole.kb.search).
// A tool-handler failure (isError: true) and a JSON-RPC-level error both
// surface as a plain Go error — callers don't need to distinguish them,
// matching this function's pre-Chapter-4 behavior.
func callTool(client *http.Client, server, tool, projectID, token string, args any) (json.RawMessage, error) {
	argsRaw, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("marshal %s arguments: %w", tool, err)
	}
	var argsMap map[string]any
	if err := json.Unmarshal(argsRaw, &argsMap); err != nil {
		return nil, fmt.Errorf("decode %s arguments for project_id injection: %w", tool, err)
	}
	if argsMap == nil {
		argsMap = map[string]any{}
	}
	argsMap["project_id"] = projectID
	argsWithProject, err := json.Marshal(argsMap)
	if err != nil {
		return nil, fmt.Errorf("marshal %s arguments with project_id: %w", tool, err)
	}

	paramsRaw, err := json.Marshal(toolsCallParams{Name: tool, Arguments: argsWithProject})
	if err != nil {
		return nil, fmt.Errorf("marshal tools/call params: %w", err)
	}
	reqBody, err := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "tools/call",
		Params:  paramsRaw,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal JSON-RPC request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(server, "/")+"/mcp", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call %s: %w", tool, err)
	}
	defer resp.Body.Close()

	var rpcResp rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("%s", rpcResp.Error.Message)
	}

	var result toolCallResult
	if err := json.Unmarshal(rpcResp.Result, &result); err != nil {
		return nil, fmt.Errorf("decode tools/call result: %w", err)
	}
	if len(result.Content) == 0 {
		return nil, fmt.Errorf("%s: empty tool result content", tool)
	}
	if result.IsError {
		return nil, fmt.Errorf("%s", result.Content[0].Text)
	}
	return json.RawMessage(result.Content[0].Text), nil
}

// localSocketRequest/localSocketResponse mirror
// internal/runtime/localapi's localRequest/localResponse wire shapes
// (internal/runtime/localapi/localapi.go). cmd/wormhole-cli cannot import
// internal/runtime/localapi (docs/architecture.md §2 restricts this package
// to internal/types and client-side code only), so the wire contract is
// duplicated here, same pattern as rpcRequest/rpcResponse above and
// internal/runtime/config's own header comment on this precedent.
type localSocketRequest struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args,omitempty"`
}

type localSocketResponse struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// wormholedSocketPath derives wormholed's local API socket path, mirroring
// internal/runtime/config.Load's XDG_RUNTIME_DIR resolution
// (internal/runtime/config/config.go) exactly. Duplicated rather than
// imported for the same module-boundary reason as localSocketRequest above.
func wormholedSocketPath() string {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = filepath.Join(os.TempDir(), "wormhole-runtime")
	}
	return filepath.Join(runtimeDir, "wormhole", "wormholed.sock")
}

// doRegisterViaSocket attempts wormhole.agent.register through wormholed's
// local socket (RFC-0003 §8.1: "wormhole join... now targets wormholed").
// reachable=false means the socket wasn't dialable (wormholed not running);
// callers fall back to the direct --server path in that case, since RFC-0003
// §3.2/§6.1 doesn't mandate wormholed's availability for standalone CLI use.
// reachable=true with a non-nil error means the socket answered but the call
// itself failed — that error is real and must not be silently swallowed.
func doRegisterViaSocket(socketPath, project string, in registerAgentInput) (out registerAgentOutput, reachable bool, err error) {
	conn, dialErr := net.DialTimeout("unix", socketPath, 2*time.Second)
	if dialErr != nil {
		return registerAgentOutput{}, false, nil
	}
	defer conn.Close()

	argsRaw, err := json.Marshal(in)
	if err != nil {
		return registerAgentOutput{}, true, fmt.Errorf("marshal register arguments: %w", err)
	}
	var argsMap map[string]any
	if err := json.Unmarshal(argsRaw, &argsMap); err != nil {
		return registerAgentOutput{}, true, fmt.Errorf("decode register arguments for project_id injection: %w", err)
	}
	argsMap["project_id"] = project
	argsWithProject, err := json.Marshal(argsMap)
	if err != nil {
		return registerAgentOutput{}, true, fmt.Errorf("marshal register arguments with project_id: %w", err)
	}

	reqBody, err := json.Marshal(localSocketRequest{Tool: "wormhole.agent.register", Args: argsWithProject})
	if err != nil {
		return registerAgentOutput{}, true, fmt.Errorf("marshal local socket request: %w", err)
	}
	if _, err := conn.Write(append(reqBody, '\n')); err != nil {
		return registerAgentOutput{}, true, fmt.Errorf("write to wormholed socket: %w", err)
	}

	var resp localSocketResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return registerAgentOutput{}, true, fmt.Errorf("decode wormholed response: %w", err)
	}
	if resp.Error != "" {
		return registerAgentOutput{}, true, fmt.Errorf("%s", resp.Error)
	}

	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return registerAgentOutput{}, true, fmt.Errorf("decode register result: %w", err)
	}
	return out, true, nil
}

// doRegister calls wormhole.agent.register (no auth required).
func doRegister(client *http.Client, server, project string, in registerAgentInput) (registerAgentOutput, error) {
	resultRaw, err := callTool(client, server, "wormhole.agent.register", project, "", in)
	if err != nil {
		return registerAgentOutput{}, err
	}
	var out registerAgentOutput
	if err := json.Unmarshal(resultRaw, &out); err != nil {
		return registerAgentOutput{}, fmt.Errorf("decode register result: %w", err)
	}
	return out, nil
}

// doSearch calls wormhole.kb.search with the token issued by doRegister
// (join flow step 2, RFC-0001 §8.5: relevant-article slice retrieval).
func doSearch(client *http.Client, server, project, token, query string, limit int) (searchArticlesOutput, error) {
	resultRaw, err := callTool(client, server, "wormhole.kb.search", project, token, searchArticlesInput{Query: query, Limit: limit})
	if err != nil {
		return searchArticlesOutput{}, err
	}
	var out searchArticlesOutput
	if err := json.Unmarshal(resultRaw, &out); err != nil {
		return searchArticlesOutput{}, fmt.Errorf("decode search result: %w", err)
	}
	return out, nil
}

// doListChannels calls wormhole.channel.list with the token issued by doRegister
// to list all channels.
func doListChannels(client *http.Client, server, project, token string) (listChannelsOutput, error) {
	resultRaw, err := callTool(client, server, "wormhole.channel.list", project, token, struct{}{})
	if err != nil {
		return listChannelsOutput{}, err
	}
	var out listChannelsOutput
	if err := json.Unmarshal(resultRaw, &out); err != nil {
		return listChannelsOutput{}, fmt.Errorf("decode list channels result: %w", err)
	}
	return out, nil
}

// doPostEvent calls wormhole.channel.post to post a self-introduction message
// to the introductions channel.
func doPostEvent(client *http.Client, server, project, token, channelID, eventType string, payload json.RawMessage, note *string) (postEventOutput, error) {
	in := postEventInput{
		ChannelID: channelID,
		EventType: eventType,
		Payload:   payload,
		Note:      note,
	}
	resultRaw, err := callTool(client, server, "wormhole.channel.post", project, token, in)
	if err != nil {
		return postEventOutput{}, err
	}
	var out postEventOutput
	if err := json.Unmarshal(resultRaw, &out); err != nil {
		return postEventOutput{}, fmt.Errorf("decode post event result: %w", err)
	}
	return out, nil
}

// doListTasks calls wormhole.task.list to retrieve all tasks.
func doListTasks(client *http.Client, server, project, token string) (listTasksOutput, error) {
	resultRaw, err := callTool(client, server, "wormhole.task.list", project, token, struct{}{})
	if err != nil {
		return listTasksOutput{}, err
	}
	var out listTasksOutput
	if err := json.Unmarshal(resultRaw, &out); err != nil {
		return listTasksOutput{}, fmt.Errorf("decode list tasks result: %w", err)
	}
	return out, nil
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

// runJoin implements join flow steps 1-2 (RFC-0001 §8.5): step 1 calls
// wormhole.agent.register to create a passport and grant permissions, then
// persists the issued credentials; step 2 retrieves a relevant KB slice
// via wormhole.kb.search, filtered against the agent's declared context.
// Self-introduction and the open-task summary are later join steps
// (Day 21+).
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
	role := fs.String("role", "", "role template name to resolve permissions from (e.g. backend-engineer)")
	permissions := fs.String("permissions", "", "comma-separated list of permissions to request (e.g. task.create,kb.write)")
	tokenFile := fs.String("token-file", "", "path to write issued credentials to (overrides --profile and the derived default)")
	profile := fs.String("profile", "", "profile name to store credentials under (default: derived from --project and --role, e.g. proj-1__backend-engineer)")
	context := fs.String("context", "", "explicit text to use for the KB semantic-sync query (default: built from owner/model/capabilities/roles)")
	kbLimit := fs.Int("kb-limit", 10, "max number of KB articles to retrieve during join sync")
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
		Role:         *role,
	}

	// Resolve (and validate) the credentials path before making any network
	// call: an invalid --profile name should fail fast, not after a
	// register round-trip against a possibly-unreachable server.
	path, err := resolveCredentialsPath(*tokenFile, *profile, *project, *role)
	if err != nil {
		fmt.Fprintf(stderr, "wormhole join: %v\n", err)
		return 1
	}

	// RFC-0003 §8.1: `wormhole join` now targets wormholed first. If its
	// local socket is reachable, registration is proxied through it
	// (internal/runtime/localapi's join-shaped wormhole.agent.register
	// dispatch); otherwise this falls back to the pre-RFC-0003 direct path,
	// since wormholed's availability isn't mandated for standalone CLI use.
	out, viaSocket, sockErr := doRegisterViaSocket(wormholedSocketPath(), *project, in)
	if viaSocket && sockErr != nil {
		fmt.Fprintf(stderr, "wormhole join: %v\n", sockErr)
		return 1
	}
	if !viaSocket {
		var err error
		out, err = doRegister(http.DefaultClient, *server, *project, in)
		if err != nil {
			fmt.Fprintf(stderr, "wormhole join: %v\n", err)
			return 1
		}
	}

	creds := credentials{
		Server:     *server,
		ProjectID:  *project,
		AgentID:    out.AgentID,
		PassportID: out.PassportID,
		Token:      out.Token,
		IssuedAt:   out.IssuedAt,
		Role:       *role,
	}
	if err := writeCredentials(path, creds); err != nil {
		fmt.Fprintf(stderr, "wormhole join: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, "Passport created.")
	fmt.Fprintf(stdout, "agent_id=%s passport_id=%s project=%s\n", out.AgentID, out.PassportID, *project)
	fmt.Fprintf(stdout, "credentials written to %s\n", path)

	kbQuery := *context
	if kbQuery == "" {
		parts := []string{}
		if *owner != "" {
			parts = append(parts, *owner)
		}
		if *model != "" {
			parts = append(parts, *model)
		}
		parts = append(parts, in.Capabilities...)
		parts = append(parts, in.Roles...)
		kbQuery = strings.Join(parts, " ")
	}
	if kbQuery == "" {
		fmt.Fprintln(stdout, "Synchronising knowledge graph... skipped (no --context, capabilities, roles, owner, or model to build a query from)")
	} else {
		searchOut, searchErr := doSearch(http.DefaultClient, *server, *project, out.Token, kbQuery, *kbLimit)
		if searchErr != nil {
			fmt.Fprintf(stderr, "wormhole join: KB sync failed: %v\n", searchErr)
		} else {
			fmt.Fprintf(stdout, "Synchronising knowledge graph (%d relevant)...\n", len(searchOut.Articles))
			for _, a := range searchOut.Articles {
				fmt.Fprintf(stdout, "  - %s (%s)\n", a.Title, a.ArticleID)
			}
		}
	}

	// Step 3: Self-introduction
	channelsOut, chanErr := doListChannels(http.DefaultClient, *server, *project, out.Token)
	if chanErr != nil {
		fmt.Fprintf(stderr, "wormhole join: self-introduction failed: %v\n", chanErr)
	} else {
		var introChan *channelSummary
		for _, ch := range channelsOut.Channels {
			if ch.Name == "introductions" {
				introChan = &ch
				break
			}
		}
		if introChan == nil {
			fmt.Fprintln(stderr, "wormhole join: introductions channel not found")
		} else {
			var introText string
			if *owner != "" && *model != "" {
				introText = fmt.Sprintf("%s (%s) joined the project.", *owner, *model)
			} else if *owner != "" {
				introText = fmt.Sprintf("%s joined the project.", *owner)
			} else if *model != "" {
				introText = fmt.Sprintf("%s joined the project.", *model)
			} else {
				introText = fmt.Sprintf("%s joined the project.", out.AgentID)
			}

			payloadStruct := struct {
				Text string `json:"text"`
			}{
				Text: introText,
			}
			payloadRaw, err := json.Marshal(payloadStruct)
			if err != nil {
				fmt.Fprintf(stderr, "wormhole join: self-introduction failed: %v\n", err)
			} else {
				_, postErr := doPostEvent(http.DefaultClient, *server, *project, out.Token, introChan.ChannelID, "message.posted", payloadRaw, &introText)
				if postErr != nil {
					fmt.Fprintf(stderr, "wormhole join: self-introduction failed: %v\n", postErr)
				} else {
					fmt.Fprintln(stdout, "Introducing agent to #introductions...")
				}
			}
		}
	}

	// Step 4: Task summary
	tasksOut, tasksErr := doListTasks(http.DefaultClient, *server, *project, out.Token)
	if tasksErr != nil {
		fmt.Fprintf(stderr, "wormhole join: task list failed: %v\n", tasksErr)
	} else {
		var openCount, doneCount int
		for _, t := range tasksOut.Tasks {
			switch strings.ToLower(t.Status) {
			case "todo", "wip", "blocked":
				openCount++
			case "done":
				doneCount++
			}
		}
		fmt.Fprintf(stdout, "Ready. %d open tasks, %d done.\n", openCount, doneCount)
	}

	return 0
}

// runConnect implements `wormhole connect`: it performs the same
// wormhole.agent.register call as `wormhole join` (via the same
// doRegister/writeCredentials helpers), then wires the issued token into
// Claude Code's MCP connector config by shelling out to the `claude` CLI
// (`claude mcp remove` then `claude mcp add -H`). Unlike `join`, it does
// not run the KB-sync/self-introduction/task-summary steps — those are
// join's concern for an already-connected identity, not connect's concern
// of wiring up the transport.
func runConnect(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "Wormhole server base URL (required)")
	project := fs.String("project", "", "project ID to join (required)")
	owner := fs.String("owner", "", "human/org owner of this agent identity")
	model := fs.String("model", "", "model identifier for this agent identity")
	capabilities := fs.String("capabilities", "", "comma-separated list of agent capabilities")
	repositories := fs.String("repositories", "", "comma-separated list of git repositories this identity is scoped to")
	roles := fs.String("roles", "", "comma-separated list of project-level roles")
	permissions := fs.String("permissions", "", "comma-separated list of permissions to request (e.g. task.create,kb.write)")
	tokenFile := fs.String("token-file", "", "path to write issued credentials to (overrides --profile and the derived default)")
	profile := fs.String("profile", "", "profile name to store credentials under (default: derived from --project, e.g. proj-1__default)")
	connectorName := fs.String("connector-name", "wormhole", "name to register the MCP connector under (claude mcp add/remove)")
	claudeBin := fs.String("claude-bin", "claude", "path to the claude CLI binary")
	target := fs.String("target", "claude", "connector target: \"claude\" or \"opencode\"")
	openCodeConfig := fs.String("opencode-config", "", "path to the OpenCode config file (default: nearest opencode.json/.jsonc walking up to .git, else $HOME/.config/opencode/opencode.json)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *server == "" || *project == "" {
		fmt.Fprintln(stderr, "wormhole connect: --server and --project are required")
		fs.Usage()
		return 2
	}
	if *target != "claude" && *target != "opencode" {
		fmt.Fprintf(stderr, "wormhole connect: --target: unknown value %q (must be \"claude\" or \"opencode\")\n", *target)
		fs.Usage()
		return 2
	}

	splitOrNil := func(s string) []string {
		if s == "" {
			return nil
		}
		return strings.Split(s, ",")
	}
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

	// Resolve (and validate) the credentials path before making any network
	// call: an invalid --profile name should fail fast, not after a
	// register round-trip against a possibly-unreachable server.
	path, err := resolveCredentialsPath(*tokenFile, *profile, *project, "")
	if err != nil {
		fmt.Fprintf(stderr, "wormhole connect: %v\n", err)
		return 1
	}

	out, err := doRegister(http.DefaultClient, *server, *project, in)
	if err != nil {
		fmt.Fprintf(stderr, "wormhole connect: %v\n", err)
		return 1
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
		fmt.Fprintf(stderr, "wormhole connect: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, "Passport created.")
	fmt.Fprintf(stdout, "agent_id=%s passport_id=%s project=%s\n", out.AgentID, out.PassportID, *project)
	fmt.Fprintf(stdout, "credentials written to %s\n", path)

	mcpURL := strings.TrimRight(*server, "/") + "/mcp"

	if *target == "opencode" {
		return runConnectOpenCode(*openCodeConfig, *connectorName, mcpURL, out.Token, stdout, stderr)
	}

	if _, lookErr := exec.LookPath(*claudeBin); lookErr != nil {
		fmt.Fprintf(stderr, "wormhole connect: %q not found in PATH — wire the connector manually:\n  claude mcp add --transport http %s %s -H \"Authorization: Bearer %s\"\n", *claudeBin, *connectorName, mcpURL, out.Token)
		return 1
	}

	removeCmd := exec.Command(*claudeBin, "mcp", "remove", *connectorName, "-s", "local")
	removeCmd.Run() // best-effort: fine if the connector wasn't registered yet

	addCmd := exec.Command(*claudeBin, "mcp", "add", "--transport", "http", *connectorName, mcpURL, "-H", "Authorization: Bearer "+out.Token)
	addCmd.Stdout = stdout
	addCmd.Stderr = stderr
	if err := addCmd.Run(); err != nil {
		fmt.Fprintf(stderr, "wormhole connect: claude mcp add failed: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Connector %q registered with %s (run /mcp inside Claude Code to reconnect).\n", *connectorName, mcpURL)
	return 0
}

// runConnectOpenCode implements the --target opencode branch of `wormhole
// connect`: it writes (or merges into) an OpenCode config file's mcp.<name>
// entry, per the opencode.ai/config.json schema (confirmed shape: $schema,
// mcp.<name>.{type, url, enabled, headers.Authorization}).
func runConnectOpenCode(explicitPath, connectorName, mcpURL, token string, stdout, stderr io.Writer) int {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "wormhole connect: %v\n", err)
		return 1
	}
	configPath, err := resolveOpenCodeConfigPath(explicitPath, cwd)
	if err != nil {
		fmt.Fprintf(stderr, "wormhole connect: %v\n", err)
		return 1
	}

	cfg := map[string]any{}
	if data, readErr := os.ReadFile(configPath); readErr == nil {
		if jsonErr := json.Unmarshal(data, &cfg); jsonErr != nil {
			fmt.Fprintf(stderr, "wormhole connect: parse existing %s: %v\n", configPath, jsonErr)
			return 1
		}
	} else if !os.IsNotExist(readErr) {
		fmt.Fprintf(stderr, "wormhole connect: read %s: %v\n", configPath, readErr)
		return 1
	}

	if _, ok := cfg["$schema"]; !ok {
		cfg["$schema"] = "https://opencode.ai/config.json"
	}

	mcp, ok := cfg["mcp"].(map[string]any)
	if !ok {
		mcp = map[string]any{}
	}
	mcp[connectorName] = map[string]any{
		"type":    "remote",
		"url":     mcpURL,
		"enabled": true,
		"headers": map[string]any{
			"Authorization": "Bearer " + token,
		},
	}
	cfg["mcp"] = mcp

	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		fmt.Fprintf(stderr, "wormhole connect: create config directory: %v\n", err)
		return 1
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "wormhole connect: encode config: %v\n", err)
		return 1
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		fmt.Fprintf(stderr, "wormhole connect: write %s: %v\n", configPath, err)
		return 1
	}

	fmt.Fprintf(stdout, "Connector %q written to wormhole config in %s.\n", connectorName, configPath)
	return 0
}

// resolveOpenCodeConfigPath decides which OpenCode config file to write.
// An explicit path always wins. Otherwise it walks up from cwd looking for
// opencode.json or opencode.jsonc, stopping (inclusive) at the first
// directory containing .git; if none is found by then, it falls back to
// the global $HOME/.config/opencode/opencode.json.
func resolveOpenCodeConfigPath(explicit, cwd string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}

	dir := cwd
	for {
		for _, name := range []string{"opencode.json", "opencode.jsonc"} {
			candidate := filepath.Join(dir, name)
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve opencode config path: %w", err)
	}
	return filepath.Join(home, ".config", "opencode", "opencode.json"), nil
}

// runWhoami implements `wormhole whoami`: prints one stored credential
// profile's identifying fields (project, role, agent ID, issued/expiry
// times), reading local files only — no server call. With --profile it
// reads that named profile; without it, it auto-selects the sole stored
// profile, or errors if zero or more than one exist (Chapter 8 roadmap:
// "no silent default when more than one profile exists").
func runWhoami(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("whoami", flag.ContinueOnError)
	fs.SetOutput(stderr)
	profile := fs.String("profile", "", "profile name to inspect (default: the sole stored profile, if only one exists)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	dir, err := profilesDir()
	if err != nil {
		fmt.Fprintf(stderr, "wormhole whoami: %v\n", err)
		return 1
	}

	var entry profileEntry
	if *profile != "" {
		if verr := validateProfileName(*profile); verr != nil {
			fmt.Fprintf(stderr, "wormhole whoami: --profile: %v\n", verr)
			return 2
		}
		creds, rerr := readCredentials(filepath.Join(dir, *profile+".json"))
		if rerr != nil {
			fmt.Fprintf(stderr, "wormhole whoami: profile %q: %v\n", *profile, rerr)
			return 1
		}
		entry = profileEntry{
			Name:      *profile,
			Project:   creds.ProjectID,
			Role:      creds.Role,
			AgentID:   creds.AgentID,
			IssuedAt:  creds.IssuedAt,
			ExpiresAt: creds.IssuedAt.Add(cliTokenTTL),
		}
	} else {
		resolved, rerr := resolveWhoamiProfile(dir)
		if rerr != nil {
			fmt.Fprintf(stderr, "wormhole whoami: %v\n", rerr)
			return 1
		}
		entry = resolved
	}

	role := entry.Role
	if role == "" {
		role = "(none)"
	}
	fmt.Fprintf(stdout, "profile=%s project=%s role=%s agent_id=%s issued_at=%s expires_at=%s\n",
		entry.Name, entry.Project, role, entry.AgentID,
		entry.IssuedAt.Format(time.RFC3339), entry.ExpiresAt.Format(time.RFC3339))
	return 0
}

// runProfile dispatches `wormhole profile <subcommand>`. Only "list" exists
// as of Chapter 8.
func runProfile(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "list" {
		fmt.Fprintln(stderr, "usage: wormhole profile list")
		return 2
	}
	return runProfileList(args[1:], stdout, stderr)
}

// runProfileList implements `wormhole profile list`: prints every stored
// credential profile's name, project, role, agent ID, and expiry, reading
// local files only — no server call.
func runProfileList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("profile list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	dir, err := profilesDir()
	if err != nil {
		fmt.Fprintf(stderr, "wormhole profile list: %v\n", err)
		return 1
	}
	entries, err := listCredentialProfiles(dir)
	if err != nil {
		fmt.Fprintf(stderr, "wormhole profile list: %v\n", err)
		return 1
	}
	if len(entries) == 0 {
		fmt.Fprintln(stdout, "no stored credential profiles")
		return 0
	}
	for _, e := range entries {
		role := e.Role
		if role == "" {
			role = "(none)"
		}
		fmt.Fprintf(stdout, "%s  project=%s role=%s agent_id=%s expires_at=%s\n",
			e.Name, e.Project, role, e.AgentID, e.ExpiresAt.Format(time.RFC3339))
	}
	return 0
}
