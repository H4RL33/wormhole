package main

import (
	"bufio"
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
	"sort"
	"strings"
	"time"

	"github.com/H4RL33/wormhole/internal/config"
)

var version = "dev"

func main() {
	exit := run(os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(exit)
}

func run(args []string, stdout, stderr io.Writer) int {
	// Dispatch table: join, connect, whoami, profile, viewer-key, mcp
	if len(args) == 0 {
		usage(stderr)
		return 2
	}

	cmd := args[0]
	switch cmd {
	case "init":
		return runInit(args[1:], stdout, stderr)
	case "join":
		return runJoin(args[1:], stdout, stderr)
	case "connect":
		return runConnect(args[1:], stdout, stderr)
	case "whoami":
		return runWhoami(args[1:], stdout, stderr)
	case "profile":
		return runProfile(args[1:], stdout, stderr)
	case "viewer-key":
		return runViewerKey(args[1:], stdout, stderr)
	case "mcp":
		return runMCP(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", cmd)
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprintf(w, `wormhole - agent memory portal

version: %s

usage: wormhole <command> [flags]

commands:
  wormhole init                          interactive setup wizard
  wormhole join [flags]                  register this agent at a project
  wormhole connect [flags]               wire harnesses to credentials
  wormhole whoami [flags]                show this agent's identity
  wormhole profile list [flags]          list stored credential profiles
  wormhole viewer-key create [flags]     issue a viewer passport
  wormhole mcp                           stdio↔socket bridge for MCP harness (no flags)
  wormhole help                          show this message

`, version)
}

// Type definitions (mirrored from internal/mcp for client-side use)

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

type credentials struct {
	Server     string    `json:"server"`
	ProjectID  string    `json:"project_id"`
	AgentID    string    `json:"agent_id"`
	PassportID string    `json:"passport_id"`
	Token      string    `json:"token"`
	IssuedAt   time.Time `json:"issued_at"`
	Role       string    `json:"role,omitempty"`
}

type createViewerKeyRequest struct {
	Label string `json:"label"`
}

type createViewerKeyResponse struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Label     string `json:"label"`
	ViewerKey string `json:"viewer_key"`
}

type profileEntry struct {
	Name      string
	Project   string
	Role      string
	AgentID   string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// cliTokenTTL mirrors identity.tokenTTL for local display purposes
const cliTokenTTL = 30 * 24 * time.Hour

// profilesDir returns the directory where keyed credential profiles live
func profilesDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".wormhole", "credentials"), nil
}

// sanitizeComponent replaces any character outside [A-Za-z0-9._-] with "_"
func sanitizeComponent(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// validateProfileName rejects profile names that could escape the profiles directory
func validateProfileName(name string) error {
	if name == "" {
		return fmt.Errorf("profile name must not be empty")
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("profile name %q must not contain path separators", name)
	}
	if name == "." || name == ".." || strings.Contains(name, "..") {
		return fmt.Errorf("profile name %q must not contain %q", name, "..")
	}
	return nil
}

// defaultProfileName derives the keyed filename stem from project and role
func defaultProfileName(project, role string) string {
	if role == "" {
		role = "default"
	}
	return sanitizeComponent(project) + "__" + sanitizeComponent(role)
}

// resolveCredentialsPath picks where join/connect writes credentials
func resolveCredentialsPath(tokenFile, profile, project, role string) (string, error) {
	if tokenFile != "" {
		return tokenFile, nil
	}
	dir, err := profilesDir()
	if err != nil {
		return "", err
	}
	if profile != "" {
		if err := validateProfileName(profile); err != nil {
			return "", fmt.Errorf("--profile: %w", err)
		}
		return filepath.Join(dir, profile+".json"), nil
	}
	return filepath.Join(dir, defaultProfileName(project, role)+".json"), nil
}

// readCredentials loads and decodes one credentials JSON file
func readCredentials(path string) (credentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return credentials{}, err
	}
	var creds credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return credentials{}, fmt.Errorf("decode: %w", err)
	}
	return creds, nil
}

// listCredentialProfiles scans dir for "*.json" credential files
func listCredentialProfiles(dir string) ([]profileEntry, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read profiles directory: %w", err)
	}
	entries := make([]profileEntry, 0, len(files))
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, f.Name())
		creds, err := readCredentials(path)
		if err != nil {
			return nil, fmt.Errorf("read profile %q: %w", f.Name(), err)
		}
		entries = append(entries, profileEntry{
			Name:      strings.TrimSuffix(f.Name(), ".json"),
			Project:   creds.ProjectID,
			Role:      creds.Role,
			AgentID:   creds.AgentID,
			IssuedAt:  creds.IssuedAt,
			ExpiresAt: creds.IssuedAt.Add(cliTokenTTL),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// resolveWhoamiProfile picks which credentials file whoami reads when --profile is omitted
func resolveWhoamiProfile(dir string) (profileEntry, error) {
	entries, err := listCredentialProfiles(dir)
	if err != nil {
		return profileEntry{}, err
	}
	if len(entries) == 0 {
		return profileEntry{}, fmt.Errorf("no stored credential profiles found under %s (run 'wormhole join' or 'wormhole connect' first)", dir)
	}
	if len(entries) > 1 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name
		}
		return profileEntry{}, fmt.Errorf("multiple credential profiles found, specify --profile: %s", strings.Join(names, ", "))
	}
	return entries[0], nil
}

// callTool sends one JSON-RPC 2.0 "tools/call" request to server's /mcp endpoint
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

// doRegisterViaSocket attempts wormhole.agent.register through Gateway's local socket.
func doRegisterViaSocket(socketPath, project string, in registerAgentInput) (out registerAgentOutput, reachable bool, err error) {
	conn, dialErr := net.DialTimeout("unix", socketPath, 2*time.Second)
	if dialErr != nil {
		return registerAgentOutput{}, false, nil
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)

	initReq, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "initialize", Params: json.RawMessage(`{}`)})
	if err != nil {
		return registerAgentOutput{}, true, fmt.Errorf("marshal initialize request: %w", err)
	}
	if _, err := conn.Write(append(initReq, '\n')); err != nil {
		return registerAgentOutput{}, true, fmt.Errorf("write initialize to gatewayd socket: %w", err)
	}
	initLine, err := reader.ReadBytes('\n')
	if err != nil {
		return registerAgentOutput{}, true, fmt.Errorf("read initialize response from gatewayd socket: %w", err)
	}
	var initResp rpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(initLine), &initResp); err != nil {
		return registerAgentOutput{}, true, fmt.Errorf("decode initialize response: %w", err)
	}
	if initResp.Error != nil {
		return registerAgentOutput{}, true, fmt.Errorf("initialize: %s", initResp.Error.Message)
	}

	initializedNotif, err := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"})
	if err != nil {
		return registerAgentOutput{}, true, fmt.Errorf("marshal notifications/initialized: %w", err)
	}
	if _, err := conn.Write(append(initializedNotif, '\n')); err != nil {
		return registerAgentOutput{}, true, fmt.Errorf("write notifications/initialized to gatewayd socket: %w", err)
	}

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

	paramsRaw, err := json.Marshal(toolsCallParams{Name: "wormhole.agent.register", Arguments: argsWithProject})
	if err != nil {
		return registerAgentOutput{}, true, fmt.Errorf("marshal tools/call params: %w", err)
	}
	callReq, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("2"), Method: "tools/call", Params: paramsRaw})
	if err != nil {
		return registerAgentOutput{}, true, fmt.Errorf("marshal tools/call request: %w", err)
	}
	if _, err := conn.Write(append(callReq, '\n')); err != nil {
		return registerAgentOutput{}, true, fmt.Errorf("write tools/call to gatewayd socket: %w", err)
	}

	callLine, err := reader.ReadBytes('\n')
	if err != nil {
		return registerAgentOutput{}, true, fmt.Errorf("read tools/call response from gatewayd socket: %w", err)
	}
	var callResp rpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(callLine), &callResp); err != nil {
		return registerAgentOutput{}, true, fmt.Errorf("decode tools/call response: %w", err)
	}
	if callResp.Error != nil {
		return registerAgentOutput{}, true, fmt.Errorf("%s", callResp.Error.Message)
	}

	var result toolCallResult
	if err := json.Unmarshal(callResp.Result, &result); err != nil {
		return registerAgentOutput{}, true, fmt.Errorf("decode tools/call result: %w", err)
	}
	if len(result.Content) == 0 {
		return registerAgentOutput{}, true, fmt.Errorf("empty register result from gatewayd")
	}
	if result.IsError {
		return registerAgentOutput{}, true, fmt.Errorf("%s", result.Content[0].Text)
	}

	if err := json.Unmarshal([]byte(result.Content[0].Text), &out); err != nil {
		return registerAgentOutput{}, true, fmt.Errorf("decode register result: %w", err)
	}
	return out, true, nil
}

// doRegister calls wormhole.agent.register (no auth required)
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

// doListTasks calls wormhole.task.list to retrieve all tasks
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

// writeCredentials persists creds to path as indented JSON
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

// runJoin implements join flow steps 1-2
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

	// Load configs
	localCfg, _ := config.LoadLocal()
	globalCfg, _ := config.LoadGlobal()

	// Resolve with precedence
	resolvedServer, err := config.ResolveServer(*server, localCfg, globalCfg)
	if err != nil {
		fmt.Fprintf(stderr, "wormhole join: server: %v\n", err)
		return 2
	}

	resolvedProject, err := config.ResolveProject(*project, localCfg)
	if err != nil {
		fmt.Fprintf(stderr, "wormhole join: project: %v\n", err)
		return 2
	}

	resolvedOwner, err := config.ResolveOwner(*owner, localCfg, globalCfg)
	if err != nil {
		fmt.Fprintf(stderr, "wormhole join: owner: %v\n", err)
		return 2
	}

	resolvedRepositories, _ := config.ResolveRepositories(*repositories)

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

	// --model: use harness self-report if flag empty
	resolvedModel := *model
	if resolvedModel == "" {
		resolvedModel = os.Getenv("WORMHOLE_MODEL") // harness injects this
	}

	reposList := splitOrNil(resolvedRepositories)

	in := registerAgentInput{
		Permissions:  splitOrEmpty(*permissions),
		Owner:        resolvedOwner,
		Model:        resolvedModel,
		Capabilities: splitOrNil(*capabilities),
		Repositories: reposList,
		Roles:        splitOrNil(*roles),
		Role:         *role,
	}

	path, err := resolveCredentialsPath(*tokenFile, *profile, resolvedProject, *role)
	if err != nil {
		fmt.Fprintf(stderr, "wormhole join: %v\n", err)
		return 1
	}

	out, viaSocket, sockErr := doRegisterViaSocket(gatewaySocketPath(), resolvedProject, in)
	if viaSocket && sockErr != nil {
		fmt.Fprintf(stderr, "wormhole join: %v\n", sockErr)
		return 1
	}
	if !viaSocket {
		var err error
		out, err = doRegister(http.DefaultClient, resolvedServer, resolvedProject, in)
		if err != nil {
			fmt.Fprintf(stderr, "wormhole join: %v\n", err)
			return 1
		}
	}

	creds := credentials{
		Server:     resolvedServer,
		ProjectID:  resolvedProject,
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
	fmt.Fprintf(stdout, "agent_id=%s passport_id=%s project=%s\n", out.AgentID, out.PassportID, resolvedProject)
	fmt.Fprintf(stdout, "credentials written to %s\n", path)

	kbQuery := *context
	if kbQuery == "" {
		// Build the semantic-sync query only from explicitly supplied signals.
		// Use the raw --owner flag, not resolvedOwner: the latter falls back to
		// git user.name/$USER, which is the developer's identity (semantic noise)
		// and would keep an otherwise-empty join from correctly skipping the sync.
		parts := []string{}
		if *owner != "" {
			parts = append(parts, *owner)
		}
		if resolvedModel != "" {
			parts = append(parts, resolvedModel)
		}
		parts = append(parts, in.Capabilities...)
		parts = append(parts, in.Roles...)
		kbQuery = strings.Join(parts, " ")
	}
	if kbQuery == "" {
		fmt.Fprintln(stdout, "Synchronising knowledge graph... skipped (no --context, capabilities, roles, owner, or model to build a query from)")
	} else {
		searchOut, searchErr := doSearch(http.DefaultClient, resolvedServer, resolvedProject, out.Token, kbQuery, *kbLimit)
		if searchErr != nil {
			fmt.Fprintf(stderr, "wormhole join: KB sync failed: %v\n", searchErr)
		} else {
			fmt.Fprintf(stdout, "Synchronising knowledge graph (%d relevant)...\n", len(searchOut.Articles))
			for _, a := range searchOut.Articles {
				fmt.Fprintf(stdout, "  - %s (%s)\n", a.Title, a.ArticleID)
			}
		}
	}

	channelsOut, chanErr := doListChannels(http.DefaultClient, resolvedServer, resolvedProject, out.Token)
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
				_, postErr := doPostEvent(http.DefaultClient, resolvedServer, resolvedProject, out.Token, introChan.ChannelID, "message.posted", payloadRaw, &introText)
				if postErr != nil {
					fmt.Fprintf(stderr, "wormhole join: self-introduction failed: %v\n", postErr)
				} else {
					fmt.Fprintln(stdout, "Introducing agent to #introductions...")
				}
			}
		}
	}

	tasksOut, tasksErr := doListTasks(http.DefaultClient, resolvedServer, resolvedProject, out.Token)
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

// runConnect implements wormhole connect
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
	stdioBin := fs.String("stdio-bin", "wormhole", "path to the wormhole binary")
	target := fs.String("target", "claude", "connector target: \"claude\" or \"opencode\"")
	openCodeConfig := fs.String("opencode-config", "", "path to the OpenCode config file (default: nearest opencode.json/.jsonc walking up to .git, else $HOME/.config/opencode/opencode.json)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// Load configs
	localCfg, _ := config.LoadLocal()
	globalCfg, _ := config.LoadGlobal()

	// Resolve with precedence
	// Validate --target before any resolution work so a typo fails fast.
	if *target != "claude" && *target != "opencode" {
		fmt.Fprintf(stderr, "wormhole connect: --target must be \"claude\" or \"opencode\", got %q\n", *target)
		return 2
	}

	resolvedServer, err := config.ResolveServer(*server, localCfg, globalCfg)
	if err != nil {
		fmt.Fprintf(stderr, "wormhole connect: server: %v\n", err)
		return 2
	}

	resolvedProject, err := config.ResolveProject(*project, localCfg)
	if err != nil {
		fmt.Fprintf(stderr, "wormhole connect: project: %v\n", err)
		return 2
	}

	resolvedOwner, err := config.ResolveOwner(*owner, localCfg, globalCfg)
	if err != nil {
		fmt.Fprintf(stderr, "wormhole connect: owner: %v\n", err)
		return 2
	}

	resolvedRepositories, _ := config.ResolveRepositories(*repositories)

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

	// --model: use harness self-report if flag empty
	resolvedModel := *model
	if resolvedModel == "" {
		resolvedModel = os.Getenv("WORMHOLE_MODEL")
	}

	reposList := splitOrNil(resolvedRepositories)

	in := registerAgentInput{
		Permissions:  splitOrEmpty(*permissions),
		Owner:        resolvedOwner,
		Model:        resolvedModel,
		Capabilities: splitOrNil(*capabilities),
		Repositories: reposList,
		Roles:        splitOrNil(*roles),
	}

	path, err := resolveCredentialsPath(*tokenFile, *profile, resolvedProject, "")
	if err != nil {
		fmt.Fprintf(stderr, "wormhole connect: %v\n", err)
		return 1
	}

	out, err := doRegister(http.DefaultClient, resolvedServer, resolvedProject, in)
	if err != nil {
		fmt.Fprintf(stderr, "wormhole connect: %v\n", err)
		return 1
	}

	creds := credentials{
		Server:     resolvedServer,
		ProjectID:  resolvedProject,
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
	fmt.Fprintf(stdout, "agent_id=%s passport_id=%s project=%s\n", out.AgentID, out.PassportID, resolvedProject)
	fmt.Fprintf(stdout, "credentials written to %s\n", path)

	socketPath := gatewaySocketPath()
	if conn, dialErr := net.DialTimeout("unix", socketPath, 2*time.Second); dialErr != nil {
		fmt.Fprintf(stderr, "wormhole connect: warning: gatewayd not running (dial %s: %v) — start gatewayd before using the harness\n", socketPath, dialErr)
	} else {
		conn.Close()
	}

	// Dispatch on whether --target was explicitly provided:
	//   explicit -> single-target inline wiring (deprecated, but still supported)
	//   unset    -> auto-detect and wire every harness present
	targetSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "target" {
			targetSet = true
		}
	})

	if targetSet {
		resolvedStdioBinPath, lookErr := exec.LookPath(*stdioBin)
		if lookErr != nil {
			if *target == "opencode" {
				fmt.Fprintf(stderr, "wormhole connect: %q not found in PATH — wire the connector manually: add mcp config with type:\"local\" and command:[\"%s\", \"mcp\"]\n", *stdioBin, *stdioBin)
			} else {
				fmt.Fprintf(stderr, "wormhole connect: %q not found in PATH — wire the connector manually:\n  claude mcp add %s -- %s mcp\n", *stdioBin, *connectorName, *stdioBin)
			}
			return 1
		}

		if *target == "opencode" {
			return runConnectOpenCode(*openCodeConfig, *connectorName, resolvedStdioBinPath, stdout, stderr)
		}

		if _, lookErr := exec.LookPath(*claudeBin); lookErr != nil {
			fmt.Fprintf(stderr, "wormhole connect: %q not found in PATH — wire the connector manually:\n  claude mcp add %s -- %s mcp\n", *claudeBin, *connectorName, *stdioBin)
			return 1
		}

		removeCmd := exec.Command(*claudeBin, "mcp", "remove", *connectorName, "-s", "local")
		removeCmd.Run()

		addCmd := exec.Command(*claudeBin, "mcp", "add", *connectorName, "--", resolvedStdioBinPath, "mcp")
		addCmd.Stdout = stdout
		addCmd.Stderr = stderr
		if err := addCmd.Run(); err != nil {
			fmt.Fprintf(stderr, "wormhole connect: claude mcp add failed: %v\n", err)
			return 1
		}

		fmt.Fprintf(stdout, "Connector %q registered (stdio via %s mcp).\n", *connectorName, resolvedStdioBinPath)
		return 0
	}

	// No --target: auto-detect every present harness and wire each one.
	harnesses, _ := detectHarnesses()
	if len(harnesses) == 0 {
		fmt.Fprintln(stderr, "wormhole connect: no harnesses detected (install the claude CLI, or add an opencode.json to this project)")
		return 1
	}
	wired := 0
	for _, h := range harnesses {
		if err := wireHarness(h, resolvedServer, resolvedProject); err != nil {
			fmt.Fprintf(stderr, "wormhole connect: %s not wired: %v\n", h.Name, err)
			continue
		}
		wired++
		fmt.Fprintf(stdout, "Connector wired for %s (%s).\n", h.Name, h.Path)
	}
	if wired == 0 {
		fmt.Fprintln(stderr, "wormhole connect: no harnesses could be wired")
		return 1
	}
	return 0
}

// runConnectOpenCode implements the --target opencode branch of connect
func runConnectOpenCode(explicitPath, connectorName, resolvedStdioBinPath string, stdout, stderr io.Writer) int {
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

	if err := wireOpenCodeConfig(configPath, connectorName, resolvedStdioBinPath); err != nil {
		fmt.Fprintf(stderr, "wormhole connect: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Connector %q written to wormhole config in %s.\n", connectorName, configPath)
	return 0
}

// wireOpenCodeConfig merges an MCP connector entry into an OpenCode config file
// at configPath, preserving any existing keys. Shared by the explicit
// --target opencode path and auto-detection so both wire OpenCode identically.
func wireOpenCodeConfig(configPath, connectorName, stdioBinPath string) error {
	cfg := map[string]any{}
	if data, readErr := os.ReadFile(configPath); readErr == nil {
		if jsonErr := json.Unmarshal(data, &cfg); jsonErr != nil {
			return fmt.Errorf("parse existing %s: %w", configPath, jsonErr)
		}
	} else if !os.IsNotExist(readErr) {
		return fmt.Errorf("read %s: %w", configPath, readErr)
	}

	if _, ok := cfg["$schema"]; !ok {
		cfg["$schema"] = "https://opencode.ai/config.json"
	}

	mcp, ok := cfg["mcp"].(map[string]any)
	if !ok {
		mcp = map[string]any{}
	}
	mcp[connectorName] = map[string]any{
		"type":    "local",
		"command": []string{stdioBinPath, "mcp"},
		"enabled": true,
	}
	cfg["mcp"] = mcp

	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", configPath, err)
	}
	return nil
}

// resolveOpenCodeConfigPath decides which OpenCode config file to write
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

// runWhoami implements wormhole whoami
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

// runProfile dispatches wormhole profile <subcommand>
func runProfile(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "list" {
		fmt.Fprintln(stderr, "usage: wormhole profile list")
		return 2
	}
	return runProfileList(args[1:], stdout, stderr)
}

// runProfileList implements wormhole profile list
func runProfileList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("profile list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "wormhole profile list: takes no arguments")
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

// runViewerKeyCreate implements wormhole viewer-key create
func runViewerKeyCreate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("viewer-key create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "Wormhole server base URL (required)")
	project := fs.String("project", "", "project ID to issue the viewer key for (required)")
	label := fs.String("label", "", "human-readable label for this viewer key (required)")
	adminKey := fs.String("admin-key", "", "dashboard admin key (default: $WORMHOLE_ADMIN_KEY)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *server == "" || *project == "" || *label == "" {
		fmt.Fprintln(stderr, "wormhole viewer-key create: --server, --project, and --label are required")
		fs.Usage()
		return 2
	}

	key := *adminKey
	if key == "" {
		key = os.Getenv("WORMHOLE_ADMIN_KEY")
	}
	if key == "" {
		fmt.Fprintln(stderr, "wormhole viewer-key create: no admin key: pass --admin-key or set $WORMHOLE_ADMIN_KEY")
		return 2
	}

	reqBody, err := json.Marshal(createViewerKeyRequest{Label: *label})
	if err != nil {
		fmt.Fprintf(stderr, "wormhole viewer-key create: %v\n", err)
		return 1
	}

	url := *server + "/dashboard/api/projects/" + *project + "/viewer-keys"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		fmt.Fprintf(stderr, "wormhole viewer-key create: %v\n", err)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Key", key)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "wormhole viewer-key create: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		var errBody struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&errBody)
		if errBody.Error != "" {
			fmt.Fprintf(stderr, "wormhole viewer-key create: server: %s\n", errBody.Error)
		} else {
			fmt.Fprintf(stderr, "wormhole viewer-key create: server returned status %d\n", resp.StatusCode)
		}
		return 1
	}

	var out createViewerKeyResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		fmt.Fprintf(stderr, "wormhole viewer-key create: decode response: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Viewer key created (id=%s, project=%s).\n", out.ID, out.ProjectID)
	fmt.Fprintf(stdout, "viewer_key=%s\n", out.ViewerKey)
	fmt.Fprintln(stdout, "This key is shown once. Give it to the human who will use the dashboard,")
	fmt.Fprintln(stdout, "as the Authorization: Bearer value at /dashboard/.")
	return 0
}

// runViewerKey implements wormhole viewer-key
func runViewerKey(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "create" {
		fmt.Fprintln(stderr, "wormhole viewer-key: only \"create\" is supported\n\nusage: wormhole viewer-key create [flags]")
		return 2
	}
	return runViewerKeyCreate(args[1:], stdout, stderr)
}
