//go:build linux

package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/localapi"
	"github.com/H4RL33/wormhole/internal/runtime/localidentity"
	"github.com/H4RL33/wormhole/internal/runtime/projectstate"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

const stage2ProcessProjectID = "00000000-0000-4000-8000-000000000001"

var stage2ProcessGatewayTools = []string{
	"wormhole.agent.list", "wormhole.agent.presence", "wormhole.agent.register",
	"wormhole.channel.create", "wormhole.channel.events", "wormhole.channel.list", "wormhole.channel.post", "wormhole.channel.subscribe",
	"wormhole.kb.get", "wormhole.kb.list", "wormhole.kb.write", "wormhole.sync.status",
	"wormhole.workspace.checkpoint", "wormhole.workspace.diff", "wormhole.workspace.import", "wormhole.workspace.stash", "wormhole.workspace.status",
}

// TestStage2LocalOnlyRealProcessAcceptance is the hermetic Stage 2 release
// topology. It executes the production daemon and setup primitives, then the
// production stdio bridge. Host service-manager installation is deliberately
// covered by the separate setup/systemd lifecycle tests; this test adds no
// bypass or alternative production configuration seam.
func TestStage2LocalOnlyRealProcessAcceptance(t *testing.T) {
	if testing.Short() {
		t.Skip("real-process acceptance")
	}
	wormholeBin := e2eBuildStdioBridgeBinary(t)
	if stdioBridgeBinErr != nil {
		t.Fatal(stdioBridgeBinErr)
	}
	gatewayBin := task4BuildGatewayBinary(t)
	if task4GatewayBinErr != nil {
		t.Fatal(task4GatewayBinErr)
	}

	gitFixture := newStage2ProcessGitFixture(t)
	cloneA := gitFixture.clone(t, "clone-a")
	runtimeA := newStage2ProcessRuntime(t, "clone-a")
	daemonA := runtimeA.startGateway(t, gatewayBin)
	setupA := runtimeA.bootstrap(t, cloneA, "Alice Clone A")
	bridgeA := runtimeA.startBridge(t, wormholeBin, cloneA)
	assertStage2ProcessToolsAndGuidance(t, bridgeA)

	sessionA := stage2ProcessAgentSession(t, runtimeA.identityRoot, "stage2-acceptance")
	agentA := sessionA.AgentID
	if !types.CanonicalUUID(agentA) || setupA.HumanID == "" || setupA.WorkspaceID == "" {
		t.Fatalf("private identity selection = human %q agent %q workspace %q", setupA.HumanID, agentA, setupA.WorkspaceID)
	}
	publishStage2ProcessActor(t, cloneA, agentA)
	stage2ProcessCall(t, bridgeA, "wormhole.workspace.import", nil)

	channel := stage2ProcessCall(t, bridgeA, "wormhole.channel.create", map[string]any{"name": "portable-review"})
	channelID := stage2ProcessString(t, channel, "id")
	article := stage2ProcessCall(t, bridgeA, "wormhole.kb.write", map[string]any{
		"title": "Portable decision", "body": "clone A\r\n", "frontmatter": map[string]any{"type": "decision"},
	})
	articleID := stage2ProcessString(t, article, "id")
	activity := stage2ProcessCall(t, bridgeA, "wormhole.channel.post", map[string]any{
		"channel_id": channelID, "event_type": "review.ready", "payload": map[string]any{"clone": "A"},
	})
	activityID := stage2ProcessString(t, activity, "id")
	stage2ProcessCall(t, bridgeA, "wormhole.agent.register", map[string]any{"agent_id": agentA, "capabilities": []string{"code", "review"}})
	stage2ProcessCall(t, bridgeA, "wormhole.agent.presence", map[string]any{"agent_id": agentA, "status": "busy"})
	assertStage2ProcessContainsID(t, stage2ProcessCall(t, bridgeA, "wormhole.channel.list", nil), "channels", channelID, true)
	assertStage2ProcessContainsID(t, stage2ProcessCall(t, bridgeA, "wormhole.kb.list", nil), "articles", articleID, true)
	assertStage2ProcessContainsID(t, stage2ProcessCall(t, bridgeA, "wormhole.channel.events", nil), "events", activityID, true)
	assertStage2ProcessContainsID(t, stage2ProcessCall(t, bridgeA, "wormhole.agent.list", nil), "agents", agentA, true)
	assertStage2ProcessOffline(t, stage2ProcessCall(t, bridgeA, "wormhole.sync.status", nil))
	assertStage2ProcessLegacyRows(t, runtimeA.dbPath, 1)

	// Restart both real processes against the same owner-private state.
	bridgeA.close(t)
	daemonA.Stop(t)
	daemonA = runtimeA.startGateway(t, gatewayBin)
	bridgeA = runtimeA.startBridge(t, wormholeBin, cloneA)
	assertStage2ProcessToolsAndGuidance(t, bridgeA)
	restartedSessionA := stage2ProcessAgentSession(t, runtimeA.identityRoot, "stage2-acceptance")
	if restartedSessionA.AgentID != agentA || restartedSessionA.SessionID == sessionA.SessionID {
		t.Fatalf("restart session = %+v, want durable agent %s and new session after %s", restartedSessionA, agentA, sessionA.SessionID)
	}
	assertStage2ProcessContainsID(t, stage2ProcessCall(t, bridgeA, "wormhole.channel.list", nil), "channels", channelID, true)
	articleAfterRestart := stage2ProcessCall(t, bridgeA, "wormhole.kb.get", map[string]any{"article_id": articleID})
	if stage2ProcessString(t, articleAfterRestart, "id") != articleID || stage2ProcessString(t, articleAfterRestart, "body") != "clone A\n" {
		t.Fatalf("article after restart = %s", articleAfterRestart)
	}
	assertStage2ProcessContainsID(t, stage2ProcessCall(t, bridgeA, "wormhole.channel.events", nil), "events", activityID, true)

	diff := stage2ProcessCall(t, bridgeA, "wormhole.workspace.diff", nil)
	if stage2ProcessString(t, diff, "candidate_digest") == "" || stage2ProcessString(t, diff, "publication_review_digest") == "" || stage2ProcessArrayLen(t, diff, "changes") < 3 {
		t.Fatalf("portable candidate diff = %s", diff)
	}
	beforeCheckpoint := captureStage2ProcessGit(t, cloneA, gitFixture.remote)
	stage2ProcessCall(t, bridgeA, "wormhole.workspace.checkpoint", nil)
	if after := captureStage2ProcessGit(t, cloneA, gitFixture.remote); after != beforeCheckpoint {
		t.Fatalf("checkpoint changed Git: before=%+v after=%+v", beforeCheckpoint, after)
	}
	stage2ProcessGitGrepAbsent(t, cloneA, activityID)

	stage2ProcessGit(t, cloneA, "config", "user.name", "Stage 2 Process")
	stage2ProcessGit(t, cloneA, "config", "user.email", "stage2@example.test")
	stage2ProcessGit(t, cloneA, "add", ".wormhole")
	stage2ProcessGit(t, cloneA, "commit", "-m", "test: accept portable channel and KB")
	stage2ProcessGit(t, cloneA, "push", "origin", "HEAD:main")
	acceptedCommit := stage2ProcessGitOutput(t, cloneA, "rev-parse", "HEAD")
	accepted := stage2ProcessCall(t, bridgeA, "wormhole.workspace.status", nil)
	if stage2ProcessString(t, accepted, "accepted_commit_sha") != acceptedCommit || stage2ProcessBool(t, accepted, "candidate_present") {
		t.Fatalf("Git acceptance status = %s, want commit %s and no candidate", accepted, acceptedCommit)
	}
	stage2ProcessGitGrepAbsent(t, cloneA, activityID)
	bridgeA.close(t)
	daemonA.Stop(t)

	cloneB := gitFixture.clone(t, "clone-b")
	runtimeB := newStage2ProcessRuntime(t, "clone-b")
	daemonB := runtimeB.startGateway(t, gatewayBin)
	setupB := runtimeB.bootstrap(t, cloneB, "Alice Clone B")
	bridgeB := runtimeB.startBridge(t, wormholeBin, cloneB)
	assertStage2ProcessToolsAndGuidance(t, bridgeB)
	sessionB := stage2ProcessAgentSession(t, runtimeB.identityRoot, "stage2-acceptance")
	if setupB.AcceptedDigest != stage2ProcessString(t, accepted, "candidate_digest") || setupB.WorkspaceID == setupA.WorkspaceID || setupB.HumanID == setupA.HumanID {
		t.Fatalf("fresh clone private/portable equivalence: A=%+v B=%+v accepted=%s", setupA, setupB, accepted)
	}
	if sessionB.AgentID == agentA || sessionB.SessionID == restartedSessionA.SessionID || sessionB.AccountableHumanID != setupB.HumanID {
		t.Fatalf("fresh clone private session = %+v, clone A agent/session = %s/%s", sessionB, agentA, restartedSessionA.SessionID)
	}
	assertStage2ProcessPortableActor(t, cloneB, agentA)
	assertStage2ProcessContainsID(t, stage2ProcessCall(t, bridgeB, "wormhole.channel.list", nil), "channels", channelID, true)
	articleB := stage2ProcessCall(t, bridgeB, "wormhole.kb.get", map[string]any{"article_id": articleID})
	if stage2ProcessString(t, articleB, "title") != stage2ProcessString(t, articleAfterRestart, "title") || stage2ProcessString(t, articleB, "body") != "clone A\n" {
		t.Fatalf("fresh clone KB = %s, want %s", articleB, articleAfterRestart)
	}
	assertStage2ProcessContainsID(t, stage2ProcessCall(t, bridgeB, "wormhole.channel.events", nil), "events", activityID, false)
	assertStage2ProcessContainsID(t, stage2ProcessCall(t, bridgeB, "wormhole.agent.list", nil), "agents", agentA, false)
	assertStage2ProcessOffline(t, stage2ProcessCall(t, bridgeB, "wormhole.sync.status", nil))
	assertStage2ProcessLegacyRows(t, runtimeB.dbPath, 0)
	assertStage2ProcessFreshPrivateState(t, runtimeA.dbPath, runtimeB.dbPath)
	assertStage2ProcessTrackedSurface(t, cloneB)
	bridgeB.close(t)
	daemonB.Stop(t)
}

type stage2ProcessGitFixture struct {
	root   string
	remote string
}

func newStage2ProcessGitFixture(t *testing.T) stage2ProcessGitFixture {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	if err := os.Mkdir(seed, 0o700); err != nil {
		t.Fatal(err)
	}
	stage2ProcessGit(t, root, "init", "--bare", "--initial-branch=main", remote)
	stage2ProcessGit(t, seed, "init", "--initial-branch=main")
	stage2ProcessGit(t, seed, "config", "user.name", "Stage 2 Seed")
	stage2ProcessGit(t, seed, "config", "user.email", "seed@example.test")
	stage2ProcessGit(t, seed, "remote", "add", "origin", remote)

	fixtureDir := filepath.Join(repoRootForTest(t), "internal", "types", "projectstate", "testdata", "v1", "valid", ".wormhole")
	tree := stage2ProcessReadTree(t, fixtureDir)
	snapshot, err := state.DecodeTree(tree)
	if err != nil {
		t.Fatalf("decode portable seed: %v", err)
	}
	snapshot.Config.Repository = types.RepositoryIdentity{}
	snapshot.Remotes = nil
	tree, err = state.EncodeTree(snapshot)
	if err != nil {
		t.Fatalf("encode portable seed: %v", err)
	}
	stage2ProcessWriteTree(t, filepath.Join(seed, ".wormhole"), tree)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("stage 2 process fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stage2ProcessGit(t, seed, "add", ".")
	stage2ProcessGit(t, seed, "commit", "-m", "test: seed portable project")
	stage2ProcessGit(t, seed, "push", "origin", "main")
	return stage2ProcessGitFixture{root: root, remote: remote}
}

func (f stage2ProcessGitFixture) clone(t *testing.T, name string) string {
	t.Helper()
	destination := filepath.Join(f.root, name)
	stage2ProcessGit(t, f.root, "clone", "--no-local", f.remote, destination)
	canonical, err := filepath.EvalSymlinks(destination)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

type stage2ProcessRuntime struct {
	home         string
	runtimeDir   string
	dataDir      string
	configDir    string
	dbPath       string
	socketPath   string
	identityRoot string
	env          []string
}

func newStage2ProcessRuntime(t *testing.T, name string) stage2ProcessRuntime {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	home := filepath.Join(root, "home")
	runtimeDir := filepath.Join(root, "run")
	dataDir := filepath.Join(root, "data")
	configDir := filepath.Join(root, "config")
	for _, directory := range []string{home, runtimeDir, dataDir, configDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return stage2ProcessRuntime{
		home: home, runtimeDir: runtimeDir, dataDir: dataDir, configDir: configDir,
		dbPath:       filepath.Join(dataDir, "wormhole", "wormholed.db"),
		socketPath:   filepath.Join(runtimeDir, "wormhole", "wormholed.sock"),
		identityRoot: filepath.Join(dataDir, "wormhole", "identities"),
		env: append(os.Environ(), "HOME="+home, "XDG_RUNTIME_DIR="+runtimeDir,
			"XDG_DATA_HOME="+dataDir, "XDG_CONFIG_HOME="+configDir),
	}
}

func (r stage2ProcessRuntime) startGateway(t *testing.T, gatewayBin string) *task4ProcessDaemon {
	t.Helper()
	return startTask4ProcessDaemon(t, gatewayBin, "default", r.env, r.socketPath)
}

func (r stage2ProcessRuntime) startBridge(t *testing.T, wormholeBin, checkout string) *e2eStdioClient {
	t.Helper()
	command := exec.Command(wormholeBin, "mcp")
	command.Dir = checkout
	command.Env = r.env
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatalf("start wormhole mcp: %v: %s", err, stderr.String())
	}
	client := &e2eStdioClient{cmd: command, stdin: stdin, stdout: bufio.NewReader(stdout)}
	initialize := client.call(t, "initialize", json.RawMessage(`{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"stage2-acceptance","version":"1","modelName":"process-fixture","modelVersion":"1"}}`))
	if initialize.Error != nil {
		t.Fatalf("initialize wormhole mcp: %+v", initialize.Error)
	}
	client.notify(t, "notifications/initialized")
	return client
}

func (c *e2eStdioClient) close(t *testing.T) {
	t.Helper()
	if c == nil || c.cmd == nil || c.cmd.ProcessState != nil {
		return
	}
	_ = c.stdin.Close()
	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wormhole mcp exit: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = c.cmd.Process.Kill()
		t.Fatal("wormhole mcp did not stop")
	}
}

type stage2SetupResult struct {
	HumanID        string
	WorkspaceID    string
	AcceptedDigest string
}

func (r stage2ProcessRuntime) bootstrap(t *testing.T, checkout, displayName string) stage2SetupResult {
	t.Helper()
	identity, err := localidentity.Open(r.identityRoot)
	if err != nil {
		t.Fatal(err)
	}
	capability, err := identity.CLICapability(t.Context())
	if err != nil {
		t.Fatalf("read production setup capability: %v", err)
	}
	client := openStage2PrivateClient(t, r.socketPath, capability)
	defer client.close()
	commit := stage2ProcessGitOutput(t, checkout, "rev-parse", "HEAD")
	workspaceRaw := client.call(t, localapi.PrivateSetupRegisterWorkspaceRPCMethod, localapi.SetupWorkspaceRequest{
		WorkingDirectory: checkout, ExpectedProjectID: stage2ProcessProjectID, ExpectedRepository: types.RepositoryIdentity{},
		ExpectedCommit: commit, ExpectedPriorDigest: localapi.DigestSetupWorkspaceAbsent(),
	})
	var workspace localapi.SetupWorkspaceReadback
	stage2ProcessDecode(t, workspaceRaw, &workspace)
	selection := types.ConfirmedIdentitySelection{DisplayName: displayName}
	identityRaw := client.call(t, localapi.PrivateSetupEnsureIdentityRPCMethod, localapi.SetupIdentityRequest{
		WorkingDirectory: checkout, JournalID: stage2ProcessSetupID(displayName), Selection: selection,
		ExpectedPriorDigest: localapi.DigestSetupIdentityUnselected(),
	})
	var selected localapi.SetupIdentityReadback
	stage2ProcessDecode(t, identityRaw, &selected)
	origin, err := projectstate.InspectPublicationOrigin(t.Context(), checkout)
	if err != nil {
		t.Fatal(err)
	}
	bindingDigest, err := projectstate.DigestPublicationBindingConstraint(types.RepositoryIdentity{}, origin)
	if err != nil {
		t.Fatal(err)
	}
	client.call(t, localapi.PrivateSetupPublicationRPCMethod, localapi.SetupPublicationRequest{
		WorkingDirectory: checkout, Classification: types.PublicationLocalOnly,
		ExpectedBindingDigest: config.StateDigest(bindingDigest),
		ExpectedPriorDigest: localapi.DigestSetupPublicationPredicate(localapi.SetupPublicationPredicate{
			Classification: types.PublicationUnclassified, PolicyRevision: 1, ObservedOriginDigest: origin, TransitionKind: "bootstrap",
		}),
	})
	client.call(t, localapi.PrivateSetupImportRPCMethod, localapi.SetupImportRequest{
		WorkingDirectory: checkout, ExpectedCommitSHA: commit, ExpectedTreeDigest: state.Digest(workspace.AcceptedTreeDigest),
		ExpectedPriorDigest: localapi.DigestSetupBasePredicate(localapi.SetupBasePredicate{CandidatePresent: false, CandidateDigest: state.Digest(workspace.AcceptedTreeDigest), WorkspaceState: "clean"}),
		DesiredDigest:       localapi.DigestSetupBasePredicate(localapi.SetupBasePredicate{CandidatePresent: true, CandidateDigest: state.Digest(workspace.AcceptedTreeDigest), WorkspaceState: "pending"}),
	})
	verifyRaw := client.call(t, localapi.PrivateSetupVerifyRPCMethod, localapi.SetupWorkingDirectoryRequest{
		WorkingDirectory: checkout, Identity: selection, ExpectedTree: state.Digest(workspace.AcceptedTreeDigest),
	})
	var verified localapi.SetupVerifyReadback
	stage2ProcessDecode(t, verifyRaw, &verified)
	return stage2SetupResult{HumanID: selected.HumanPrincipalID, WorkspaceID: string(workspace.WorkspaceID), AcceptedDigest: workspace.AcceptedTreeDigest}
}

type stage2PrivateClient struct {
	connection net.Conn
	reader     *bufio.Reader
	capability string
	nextID     int
}

func openStage2PrivateClient(t *testing.T, socketPath, capability string) *stage2PrivateClient {
	t.Helper()
	connection, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	client := &stage2PrivateClient{connection: connection, reader: bufio.NewReader(connection), capability: capability}
	client.rawCall(t, "initialize", map[string]any{
		"protocolVersion": "2025-11-25", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "wormhole-setup", "version": "stage2-process"},
	})
	client.notify(t, "notifications/initialized")
	return client
}

func (c *stage2PrivateClient) call(t *testing.T, method string, request any) json.RawMessage {
	t.Helper()
	requestRaw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return c.rawCall(t, method, map[string]any{"capability": c.capability, "request": json.RawMessage(requestRaw)})
}

func (c *stage2PrivateClient) rawCall(t *testing.T, method string, params any) json.RawMessage {
	t.Helper()
	c.nextID++
	request := map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method, "params": params}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeNewlineFrame(c.connection, encoded); err != nil {
		t.Fatal(err)
	}
	responseBody, err := readNewlineFrame(c.reader)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		t.Fatalf("decode private %s response %q: %v", method, responseBody, err)
	}
	if response.Error != nil {
		t.Fatalf("private %s error %d: %s", method, response.Error.Code, response.Error.Message)
	}
	return response.Result
}

func (c *stage2PrivateClient) notify(t *testing.T, method string) {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeNewlineFrame(c.connection, encoded); err != nil {
		t.Fatal(err)
	}
}

func (c *stage2PrivateClient) close() {
	_ = c.connection.Close()
}

func stage2ProcessSetupID(displayName string) string {
	if strings.Contains(displayName, "Clone B") {
		return "00000000-0000-4000-8000-000000000032"
	}
	return "00000000-0000-4000-8000-000000000031"
}

func assertStage2ProcessToolsAndGuidance(t *testing.T, client *e2eStdioClient) {
	t.Helper()
	response := client.call(t, "tools/list", json.RawMessage(`{}`))
	if response.Error != nil {
		t.Fatalf("tools/list: %+v", response.Error)
	}
	var listed struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	stage2ProcessDecode(t, response.Result, &listed)
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		if tool.Name == "" || strings.TrimSpace(tool.Description) == "" {
			t.Fatalf("incomplete real-process descriptor: %+v", tool)
		}
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	want := append([]string(nil), stage2ProcessGatewayTools...)
	sort.Strings(want)
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("real Gateway tools = %q, want exact 17 %q", names, want)
	}

	root := repoRootForTest(t)
	guidance, err := os.ReadFile(filepath.Join(root, "testdata", "alpha", "manifests", "generated-guidance", "wormhole-tool-use.md"))
	if err != nil {
		t.Fatal(err)
	}
	manifestRaw, err := os.ReadFile(filepath.Join(root, "testdata", "alpha", "manifests", "generated-guidance", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Entries []struct {
			Target  string `json:"target"`
			Content string `json:"content"`
			Digest  string `json:"content_digest"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	matched := false
	for _, entry := range manifest.Entries {
		if entry.Target != ".agents/skills/wormhole-tool-use/SKILL.md" {
			continue
		}
		matched = true
		if entry.Content != string(guidance) || entry.Digest != stage2ProcessSHA256(guidance) {
			t.Fatalf("generated tool guidance bytes/digest differ from manifest")
		}
	}
	if !matched {
		t.Fatal("generated tool guidance missing from manifest")
	}
	for _, name := range want {
		if !bytes.Contains(guidance, []byte("## `"+name+"`")) {
			t.Errorf("generated guidance missing real-process tool %s", name)
		}
	}
	for _, removed := range []string{"wormhole.agent.enrol", "wormhole.agent.get_guidance", "wormhole.agent.whoami", "wormhole.kb.search", "wormhole.task.list", "wormhole.git.link_commit"} {
		if bytes.Contains(guidance, []byte("## `"+removed+"`")) {
			t.Errorf("generated Gateway guidance retains removed tool %s", removed)
		}
	}
}

func stage2ProcessCall(t *testing.T, client *e2eStdioClient, tool string, arguments map[string]any) json.RawMessage {
	t.Helper()
	raw, callErr := client.callTool(t, tool, arguments)
	if callErr != "" {
		t.Fatalf("%s: %s", tool, callErr)
	}
	if len(raw) == 0 {
		t.Fatalf("%s returned empty result", tool)
	}
	return raw
}

func stage2ProcessAgentID(t *testing.T, root, harness string) string {
	t.Helper()
	return stage2ProcessAgentSession(t, root, harness).AgentID
}

func stage2ProcessAgentSession(t *testing.T, root, harness string) localidentity.ConnectionSession {
	t.Helper()
	identity, err := localidentity.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := identity.ConnectionSessions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for i := range sessions {
		if sessions[i].HarnessName == harness && sessions[i].AgentID != "" && sessions[i].EndedAt == nil {
			if sessions[i].SessionID == "" || sessions[i].AccountableHumanID == "" || sessions[i].ModelName != "process-fixture" {
				t.Fatalf("incomplete selected agent/session provenance: %+v", sessions[i])
			}
			return sessions[i]
		}
	}
	t.Fatalf("no active %s agent session in %+v", harness, sessions)
	return localidentity.ConnectionSession{}
}

func publishStage2ProcessActor(t *testing.T, checkout, agentID string) {
	t.Helper()
	tree := stage2ProcessReadTree(t, filepath.Join(checkout, ".wormhole"))
	snapshot, err := state.DecodeTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	actor := state.ActorV1{
		SchemaVersion: 1, Kind: "actor", ID: agentID, ActorKind: types.ActorAgent,
		DisplayName: "Stage 2 Process Agent", PublicKeys: []state.PublicKeyV1{}, Extensions: state.ExtensionsV1{},
	}
	snapshot.Actors[agentID] = state.Record[state.ActorV1]{Value: &actor}
	tree, err = state.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	stage2ProcessWriteTree(t, filepath.Join(checkout, ".wormhole"), tree)
}

func assertStage2ProcessPortableActor(t *testing.T, checkout, agentID string) {
	t.Helper()
	tree := stage2ProcessReadTree(t, filepath.Join(checkout, ".wormhole"))
	snapshot, err := state.DecodeTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	record, ok := snapshot.Actors[agentID]
	if !ok || record.Value == nil || record.Value.ID != agentID || record.Value.ActorKind != types.ActorAgent {
		t.Fatalf("portable actor %s in fresh clone = %+v", agentID, record)
	}
}

func stage2ProcessString(t *testing.T, raw json.RawMessage, field string) string {
	t.Helper()
	var object map[string]any
	stage2ProcessDecode(t, raw, &object)
	value, ok := object[field].(string)
	if !ok {
		t.Fatalf("%s is not a string in %s", field, raw)
	}
	return value
}

func stage2ProcessBool(t *testing.T, raw json.RawMessage, field string) bool {
	t.Helper()
	var object map[string]any
	stage2ProcessDecode(t, raw, &object)
	value, ok := object[field].(bool)
	if !ok {
		t.Fatalf("%s is not a bool in %s", field, raw)
	}
	return value
}

func stage2ProcessArrayLen(t *testing.T, raw json.RawMessage, field string) int {
	t.Helper()
	var object map[string]any
	stage2ProcessDecode(t, raw, &object)
	value, ok := object[field].([]any)
	if !ok {
		t.Fatalf("%s is not an array in %s", field, raw)
	}
	return len(value)
}

func assertStage2ProcessContainsID(t *testing.T, raw json.RawMessage, field, id string, want bool) {
	t.Helper()
	var object map[string]any
	stage2ProcessDecode(t, raw, &object)
	if object[field] == nil {
		if want {
			t.Fatalf("%s is empty, want %s: %s", field, id, raw)
		}
		return
	}
	items, ok := object[field].([]any)
	if !ok {
		t.Fatalf("%s is not an array in %s", field, raw)
	}
	found := false
	for _, item := range items {
		entry, objectOK := item.(map[string]any)
		if objectOK && (entry["id"] == id || entry["agent_id"] == id) {
			found = true
		}
	}
	if found != want {
		t.Fatalf("%s contains %s = %t, want %t: %s", field, id, found, want, raw)
	}
}

func assertStage2ProcessOffline(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var status struct {
		State         string `json:"state"`
		PendingWrites int    `json:"pending_writes"`
	}
	stage2ProcessDecode(t, raw, &status)
	if status.State != "offline" || status.PendingWrites != 0 {
		t.Fatalf("local-only sync status = %+v", status)
	}
}

func assertStage2ProcessLegacyRows(t *testing.T, dbPath string, wantEvents int) {
	t.Helper()
	database := openStage2ProcessDB(t, dbPath)
	defer database.Close()
	for _, table := range []string{"channels", "kb_articles", "tasks", "git_links", "sync_queue", "sync_audit", "enrolment_attempts", "bootstrap_metadata"} {
		if got := stage2ProcessTableCount(t, database, table); got != 0 {
			t.Errorf("non-portable legacy/sync table %s has %d rows, want zero", table, got)
		}
	}
	if got := stage2ProcessTableCount(t, database, "events"); got != wantEvents {
		t.Errorf("clone-local operational events = %d, want %d", got, wantEvents)
	}
}

func assertStage2ProcessFreshPrivateState(t *testing.T, firstPath, secondPath string) {
	t.Helper()
	if firstPath == secondPath {
		t.Fatal("two clones share private database path")
	}
	firstInfo, err := os.Stat(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	secondInfo, err := os.Stat(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(firstInfo, secondInfo) {
		t.Fatal("two clones share private database inode")
	}
	first := openStage2ProcessDB(t, firstPath)
	defer first.Close()
	second := openStage2ProcessDB(t, secondPath)
	defer second.Close()
	if stage2ProcessTableCount(t, first, "workspace_overlay_operations") < 2 || stage2ProcessTableCount(t, first, "workspace_materializations") < 1 {
		t.Fatal("clone A lacks private overlay/materialization evidence")
	}
	for _, table := range []string{"workspace_overlay_operations", "workspace_materializations", "workspace_stashes", "workspace_transition_receipts"} {
		if got := stage2ProcessTableCount(t, second, table); got != 0 {
			t.Errorf("fresh clone B private table %s has %d rows, want zero", table, got)
		}
	}
}

func openStage2ProcessDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.PingContext(t.Context()); err != nil {
		database.Close()
		t.Fatal(err)
	}
	return database
}

func stage2ProcessTableCount(t *testing.T, database *sql.DB, table string) int {
	t.Helper()
	var count int
	if err := database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func assertStage2ProcessTrackedSurface(t *testing.T, checkout string) {
	t.Helper()
	root := filepath.Join(checkout, ".wormhole")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		portable := relative == "config.toml" || strings.HasPrefix(filepath.ToSlash(relative), "state/v1/")
		if !portable || strings.Contains(strings.ToLower(relative), "private") || strings.HasSuffix(relative, ".db") {
			return fmt.Errorf("non-portable tracked file %s", relative)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

type stage2ProcessGitState struct {
	Head   string
	Index  string
	Remote string
}

func captureStage2ProcessGit(t *testing.T, checkout, remote string) stage2ProcessGitState {
	t.Helper()
	return stage2ProcessGitState{
		Head:   stage2ProcessGitOutput(t, checkout, "rev-parse", "HEAD"),
		Index:  stage2ProcessGitOutput(t, checkout, "write-tree"),
		Remote: stage2ProcessGitOutput(t, remote, "rev-parse", "refs/heads/main"),
	}
}

func stage2ProcessGitGrepAbsent(t *testing.T, checkout, needle string) {
	t.Helper()
	tree := stage2ProcessReadTree(t, filepath.Join(checkout, ".wormhole"))
	for _, file := range tree {
		if bytes.Contains(file.Data, []byte(needle)) {
			t.Fatalf("non-portable activity %s entered tracked %s", needle, file.Path)
		}
	}
}

func stage2ProcessGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	_ = stage2ProcessGitOutput(t, directory, arguments...)
}

func stage2ProcessGitOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %s %s: %v: %s", directory, strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func stage2ProcessReadTree(t *testing.T, root string) state.Tree {
	t.Helper()
	tree := state.Tree{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		tree = append(tree, state.File{Path: filepath.ToSlash(relative), Data: data})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(tree, func(i, j int) bool { return tree[i].Path < tree[j].Path })
	return tree
}

func stage2ProcessWriteTree(t *testing.T, root string, tree state.Tree) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, file := range tree {
		path := filepath.Join(root, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, file.Data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func stage2ProcessDecode(t *testing.T, raw []byte, destination any) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		t.Fatalf("trailing JSON in %q", raw)
	}
}

func stage2ProcessSHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}
