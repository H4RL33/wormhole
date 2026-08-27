package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localapi"
)

var version = "dev"

func main() {
	exit := run(os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(exit)
}

func run(args []string, stdout, stderr io.Writer) int {
	// Public dispatch is the canonical Git-native command surface.
	if len(args) == 0 {
		usage(stderr)
		return 2
	}

	cmd := args[0]
	switch cmd {
	case "setup":
		return runSetup(context.Background(), args[1:], os.Stdin, stdout, stderr, setupDependencies{})
	case "connector":
		return runConnector(context.Background(), args[1:], os.Stdin, stdout, stderr, nil)
	case "whoami":
		return runWhoami(args[1:], stdout, stderr)
	case "status":
		return runWorkspaceCommand(localapi.WorkspaceOperationStatus, args[1:], stdout, stderr)
	case "diff":
		return runWorkspaceCommand(localapi.WorkspaceOperationDiff, args[1:], stdout, stderr)
	case "import":
		return runWorkspaceCommand(localapi.WorkspaceOperationImport, args[1:], stdout, stderr)
	case "checkpoint":
		return runWorkspaceCommand(localapi.WorkspaceOperationCheckpoint, args[1:], stdout, stderr)
	case "stash":
		return runWorkspaceCommand(localapi.WorkspaceOperationStash, args[1:], stdout, stderr)
	case "profile":
		return runProfile(args[1:], stdout, stderr)
	case "viewer-key":
		return runViewerKey(args[1:], stdout, stderr)
	case "integration":
		return runIntegrationCommand(args[1:], stdout, stderr)
	case "trial-metrics":
		return runTrialMetricsCommand(args[1:], stdout, stderr)
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
  wormhole setup [flags]                 confirm and resume canonical local setup
  wormhole connector list <adapter>      inspect a native harness connector
  wormhole connector install [--yes] <adapter>   transactionally install a connector
  wormhole connector remove [--yes] <adapter>    transactionally remove a connector
  wormhole whoami [flags]                show this agent's identity
  wormhole status                        show bound workspace state and publication review
  wormhole diff                          show the semantic workspace diff
  wormhole import                        import portable working-tree edits
  wormhole checkpoint [flags]            materialize the candidate tree
  wormhole stash [flags]                 stash the private overlay
  wormhole profile list [flags]          list stored credential profiles
  wormhole viewer-key create [flags]     issue a viewer passport
  wormhole integration preview [flags]  preview approved guidance repository changes
  wormhole integration apply [flags]    explicitly apply approved guidance
  wormhole integration status [flags]   inspect approved guidance and drift state
  wormhole integration update [flags]   explicitly apply a verified higher version
  wormhole integration remove [flags]   remove unchanged managed guidance
  wormhole integration rollback [flags]  apply the selected prior approved version
  wormhole trial-metrics validate [flags] [FILE|-]  validate a local trial metrics export
  wormhole trial-metrics format [flags] [FILE|-]    validate and format a local trial metrics export
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

// resolveCredentialsPath resolves the selected local credential profile path
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

func resolveModel(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return os.Getenv("WORMHOLE_MODEL")
}

func resolveAdminKey(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return os.Getenv("WORMHOLE_ADMIN_KEY")
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
		return profileEntry{}, fmt.Errorf("no stored credential profiles found under %s (run 'wormhole setup' first)", dir)
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

// doListChannels calls wormhole.channel.list with the supplied token.
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

func callGatewayTool(socketPath, tool string, arguments any) (json.RawMessage, error) {
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("gatewayd not running (dial %s: %w)", socketPath, err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)

	initialize, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "initialize", Params: json.RawMessage(`{}`)})
	if err != nil {
		return nil, fmt.Errorf("marshal Gateway initialize request: %w", err)
	}
	if _, err := conn.Write(append(initialize, '\n')); err != nil {
		return nil, fmt.Errorf("write Gateway initialize request: %w", err)
	}
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("read Gateway initialize response: %w", err)
	}
	var initResponse rpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(line), &initResponse); err != nil {
		return nil, fmt.Errorf("decode Gateway initialize response: %w", err)
	}
	if initResponse.Error != nil {
		return nil, fmt.Errorf("Gateway initialize: %s", initResponse.Error.Message)
	}
	initialized, _ := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"})
	if _, err := conn.Write(append(initialized, '\n')); err != nil {
		return nil, fmt.Errorf("write Gateway initialized notification: %w", err)
	}
	args, err := json.Marshal(arguments)
	if err != nil {
		return nil, fmt.Errorf("marshal %s arguments: %w", tool, err)
	}
	params, err := json.Marshal(toolsCallParams{Name: tool, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("marshal %s params: %w", tool, err)
	}
	call, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("2"), Method: "tools/call", Params: params})
	if err != nil {
		return nil, fmt.Errorf("marshal %s request: %w", tool, err)
	}
	if _, err := conn.Write(append(call, '\n')); err != nil {
		return nil, fmt.Errorf("write %s request: %w", tool, err)
	}
	line, err = reader.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("read %s response: %w", tool, err)
	}
	var callResponse rpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(line), &callResponse); err != nil {
		return nil, fmt.Errorf("decode %s response: %w", tool, err)
	}
	if callResponse.Error != nil {
		return nil, errors.New(callResponse.Error.Message)
	}
	var result toolCallResult
	if err := json.Unmarshal(callResponse.Result, &result); err != nil {
		return nil, fmt.Errorf("decode %s result: %w", tool, err)
	}
	if len(result.Content) == 0 {
		return nil, fmt.Errorf("%s: empty tool result content", tool)
	}
	if result.IsError {
		return nil, errors.New(result.Content[0].Text)
	}
	return json.RawMessage(result.Content[0].Text), nil
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

	key := resolveAdminKey(*adminKey)
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
