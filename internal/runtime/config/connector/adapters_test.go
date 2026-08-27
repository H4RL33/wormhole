package connector

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/H4RL33/wormhole/internal/runtime/config"
)

func newClaudeFixture(t *testing.T, runner config.CommandRunner, userJSON, projectJSON string) *ClaudeAdapter {
	t.Helper()
	root := t.TempDir()
	userPath := filepath.Join(root, ".claude.json")
	projectRoot := filepath.Join(root, "project")
	if err := os.Mkdir(projectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	userJSON = strings.ReplaceAll(userJSON, "$PROJECT_ROOT", projectRoot)
	if userJSON != "" {
		if err := os.WriteFile(userPath, []byte(userJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if projectJSON != "" {
		if err := os.WriteFile(filepath.Join(projectRoot, ".mcp.json"), []byte(projectJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	adapter, err := NewClaudeAdapterAt(runner, "claude", "wormhole", userPath, projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

type runnerReply struct {
	stdout []byte
	stderr []byte
	err    error
}

type recordingRunner struct {
	replies []runnerReply
	calls   [][]string
}

func (r *recordingRunner) Run(_ context.Context, executable string, args ...string) ([]byte, []byte, error) {
	r.calls = append(r.calls, append([]string{executable}, args...))
	if len(r.replies) == 0 {
		return nil, nil, errors.New("unexpected fake runner call")
	}
	reply := r.replies[0]
	r.replies = r.replies[1:]
	return reply.stdout, reply.stderr, reply.err
}

func TestCodexExactVersionDiscoveryAndStrictInspection(t *testing.T) {
	runner := &recordingRunner{replies: []runnerReply{
		{stdout: []byte("codex-cli 0.149.0\n")},
		{stdout: []byte(`{
  "name":"wormhole","enabled":true,"disabled_reason":null,
  "transport":{"type":"stdio","command":"/opt/wormhole","args":["mcp"],"env":{"ZETA":"two","ALPHA":"one"},"env_vars":[],"cwd":null},
  "enabled_tools":null,"disabled_tools":null,"startup_timeout_sec":null,"tool_timeout_sec":null
}`)},
	}}
	adapter, err := NewCodexAdapter(runner, "/usr/bin/codex", "wormhole")
	if err != nil {
		t.Fatal(err)
	}
	availability, err := adapter.Discover(t.Context())
	if err != nil || !availability.Available || availability.Version != "0.149.0" {
		t.Fatalf("Discover = %+v, %v", availability, err)
	}
	entry, err := adapter.Inspect(t.Context())
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	want := ConnectorEntry{State: EntryPresent, Scope: ScopeUser, Transport: TransportStdio, Command: "/opt/wormhole", Args: []string{"mcp"}, Env: []EnvironmentVariable{{Name: "ALPHA", Value: "one"}, {Name: "ZETA", Value: "two"}}}
	if !reflect.DeepEqual(entry, want) {
		t.Fatalf("entry = %#v, want %#v", entry, want)
	}
	if got := runner.calls; !reflect.DeepEqual(got, [][]string{{"/usr/bin/codex", "--version"}, {"/usr/bin/codex", "mcp", "get", "wormhole", "--json"}}) {
		t.Fatalf("calls = %#v", got)
	}
}

func TestCodexAndClaudeDiscoveryRejectsWarningsAndInexactVersions(t *testing.T) {
	for name, constructor := range map[string]func(config.CommandRunner) (Adapter, error){
		"codex": func(runner config.CommandRunner) (Adapter, error) {
			return NewCodexAdapter(runner, "codex", "wormhole")
		},
		"claude": func(runner config.CommandRunner) (Adapter, error) {
			return NewClaudeAdapter(runner, "claude", "wormhole")
		},
	} {
		t.Run(name+" stderr", func(t *testing.T) {
			runner := &recordingRunner{replies: []runnerReply{{stdout: []byte("codex-cli 0.149.0\n"), stderr: []byte("warning secret-token")}}}
			if name == "claude" {
				runner.replies[0].stdout = []byte("2.1.181 (Claude Code)\n")
			}
			adapter, _ := constructor(runner)
			if _, err := adapter.Discover(t.Context()); !errors.Is(err, ErrConnectorUnavailable) {
				t.Fatalf("error=%v", err)
			}
		})
		t.Run(name+" inexact", func(t *testing.T) {
			runner := &recordingRunner{replies: []runnerReply{{stdout: []byte("version latest\n")}}}
			adapter, _ := constructor(runner)
			if _, err := adapter.Discover(t.Context()); !errors.Is(err, ErrConnectorUnavailable) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	for name, version := range map[string]string{"codex": "codex-cli 0.145.0\n", "claude": "2.1.181 (Claude Code)\n"} {
		t.Run(name+" unsupported exact version", func(t *testing.T) {
			runner := &recordingRunner{replies: []runnerReply{{stdout: []byte(version)}}}
			var adapter Adapter
			if name == "codex" {
				adapter, _ = NewCodexAdapter(runner, "codex", "wormhole")
			} else {
				adapter = newClaudeFixture(t, runner, "", "")
			}
			if _, err := adapter.Discover(t.Context()); !errors.Is(err, ErrConnectorUnavailable) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestCodexAbsentAndUnsupportedEntries(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		runner := &recordingRunner{replies: []runnerReply{{stderr: []byte("Error: No MCP server named 'wormhole' found.\n"), err: &config.CommandExitError{ExitCode: 1}}}}
		adapter, _ := NewCodexAdapter(runner, "codex", "wormhole")
		entry, err := adapter.Inspect(t.Context())
		if err != nil || !EqualConnectorEntry(entry, ConnectorEntry{State: EntryAbsent}) {
			t.Fatalf("Inspect = %#v, %v", entry, err)
		}
	})
	for name, payload := range map[string]string{
		"http":    `{"name":"wormhole","enabled":true,"disabled_reason":null,"transport":{"type":"streamable_http","url":"https://example.test","bearer_token_env_var":"TOKEN","http_headers":null,"env_http_headers":null,"http_headers_helper":null},"enabled_tools":null,"disabled_tools":null,"startup_timeout_sec":null,"tool_timeout_sec":null}`,
		"unknown": `{"name":"wormhole","enabled":true,"disabled_reason":null,"transport":{"type":"stdio","command":"wormhole","args":["mcp"],"env":{},"env_vars":[],"cwd":null,"future":true},"enabled_tools":null,"disabled_tools":null,"startup_timeout_sec":null,"tool_timeout_sec":null}`,
		"oauth":   `{"name":"wormhole","enabled":true,"disabled_reason":null,"transport":{"type":"stdio","command":"wormhole","args":["mcp"],"env":{},"env_vars":["TOKEN"],"cwd":null},"enabled_tools":null,"disabled_tools":null,"startup_timeout_sec":null,"tool_timeout_sec":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			runner := &recordingRunner{replies: []runnerReply{{stdout: []byte(payload)}}}
			adapter, _ := NewCodexAdapter(runner, "codex", "wormhole")
			if _, err := adapter.Inspect(t.Context()); !errors.Is(err, ErrUnsupportedConnectorEntry) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestCodexCanonicalNullEnvironmentAndRequiredFields(t *testing.T) {
	canonical := `{"name":"wormhole","enabled":true,"disabled_reason":null,"transport":{"type":"stdio","command":"/opt/wormhole","args":["mcp"],"env":null,"env_vars":[],"cwd":null},"enabled_tools":null,"disabled_tools":null,"startup_timeout_sec":null,"tool_timeout_sec":null}`
	runner := &recordingRunner{replies: []runnerReply{{stdout: []byte(canonical)}}}
	adapter, _ := NewCodexAdapter(runner, "codex", "wormhole")
	entry, err := adapter.Inspect(t.Context())
	if err != nil {
		t.Fatalf("canonical null environment: %v", err)
	}
	if entry.Env == nil || len(entry.Env) != 0 {
		t.Fatalf("environment=%#v", entry.Env)
	}
	missing := strings.Replace(canonical, `,"tool_timeout_sec":null`, "", 1)
	runner = &recordingRunner{replies: []runnerReply{{stdout: []byte(missing)}}}
	adapter, _ = NewCodexAdapter(runner, "codex", "wormhole")
	if _, err := adapter.Inspect(t.Context()); !errors.Is(err, ErrUnsupportedConnectorEntry) {
		t.Fatalf("missing required field error=%v", err)
	}
}

func TestCodexMutationArgvContainsNoHealthCheck(t *testing.T) {
	runner := &recordingRunner{replies: []runnerReply{{}, {}}}
	adapter, _ := NewCodexAdapter(runner, "codex", "wormhole")
	prior := ConnectorEntry{State: EntryAbsent}
	desired := ConnectorEntry{State: EntryPresent, Scope: ScopeUser, Transport: TransportStdio, Command: "/opt/wormhole", Args: []string{"mcp"}, Env: []EnvironmentVariable{}}
	plan, _ := adapter.Plan(t.Context(), prior, desired)
	if err := adapter.Apply(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Remove(t.Context(), desired); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"codex", "mcp", "add", "wormhole", "--", "/opt/wormhole", "mcp"}, {"codex", "mcp", "remove", "wormhole"}}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v", runner.calls)
	}
	for _, call := range runner.calls {
		if strings.Contains(strings.Join(call, " "), " list") {
			t.Fatalf("health-check argv used: %#v", call)
		}
	}
}

func TestClaudeExactVersionDiscoveryAndStrictUserStdioInspection(t *testing.T) {
	runner := &recordingRunner{replies: []runnerReply{{stdout: []byte("2.1.220 (Claude Code)\n")}}}
	adapter := newClaudeFixture(t, runner, `{"mcpServers":{"wormhole":{"type":"stdio","command":"/opt/Worm Hole/wormhole","args":["mcp","two words",""],"env":{"TOKEN":"secret","EMPTY":""}}}}`, "")
	availability, err := adapter.Discover(t.Context())
	if err != nil || availability.Version != "2.1.220" {
		t.Fatalf("Discover = %+v, %v", availability, err)
	}
	entry, err := adapter.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := ConnectorEntry{State: EntryPresent, Scope: ScopeUser, Transport: TransportStdio, Command: "/opt/Worm Hole/wormhole", Args: []string{"mcp", "two words", ""}, Env: []EnvironmentVariable{{Name: "EMPTY", Value: ""}, {Name: "TOKEN", Value: "secret"}}}
	if !reflect.DeepEqual(entry, want) {
		t.Fatalf("entry = %#v", entry)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("inspection executed client health check: %#v", runner.calls)
	}
}

func TestClaudeAbsentPriorUsesNativeFiles(t *testing.T) {
	runner := &recordingRunner{}
	adapter := newClaudeFixture(t, runner, `{"mcpServers":{}}`, "")
	entry, err := adapter.Inspect(t.Context())
	if err != nil || !EqualConnectorEntry(entry, ConnectorEntry{State: EntryAbsent}) {
		t.Fatalf("Inspect=%#v, %v", entry, err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("calls=%#v", runner.calls)
	}
}

func TestClaudeRejectsHTTPUnknownAndHiddenScopes(t *testing.T) {
	for name, fixture := range map[string]struct{ user, project string }{
		"http":          {user: `{"mcpServers":{"wormhole":{"type":"http","url":"https://example.test/mcp"}}}`},
		"unknown":       {user: `{"mcpServers":{"wormhole":{"type":"stdio","command":"/opt/wormhole","args":["mcp"],"env":{},"future":true}}}`},
		"local scope":   {user: `{"mcpServers":{},"projects":{"$PROJECT_ROOT":{"mcpServers":{"wormhole":{"type":"stdio","command":"/opt/wormhole","args":["mcp"],"env":{}}}}}}`},
		"project scope": {user: `{"mcpServers":{}}`, project: `{"mcpServers":{"wormhole":{"type":"stdio","command":"/opt/wormhole","args":["mcp"],"env":{}}}}`},
		"duplicate":     {user: `{"mcpServers":{"wormhole":{"type":"stdio","command":"/one","args":[],"env":{}},"wormhole":{"type":"stdio","command":"/two","args":[],"env":{}}}}`},
	} {
		t.Run(name, func(t *testing.T) {
			runner := &recordingRunner{}
			adapter := newClaudeFixture(t, runner, fixture.user, fixture.project)
			if _, err := adapter.Inspect(t.Context()); !errors.Is(err, ErrUnsupportedConnectorEntry) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestClaudeMutationUsesExplicitUserScope(t *testing.T) {
	runner := &recordingRunner{replies: []runnerReply{{}, {}}}
	adapter, _ := NewClaudeAdapter(runner, "claude", "wormhole")
	prior := ConnectorEntry{State: EntryAbsent}
	desired := ConnectorEntry{State: EntryPresent, Scope: ScopeUser, Transport: TransportStdio, Command: "/opt/wormhole", Args: []string{"mcp"}, Env: []EnvironmentVariable{}}
	plan, _ := adapter.Plan(t.Context(), prior, desired)
	_ = adapter.Apply(t.Context(), plan)
	_ = adapter.Remove(t.Context(), desired)
	want := [][]string{{"claude", "mcp", "add", "--scope", "user", "wormhole", "--", "/opt/wormhole", "mcp"}, {"claude", "mcp", "remove", "--scope", "user", "wormhole"}}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v", runner.calls)
	}
}

func TestClaudeRollbackAddPlacesMultipleEnvironmentFlagsAfterName(t *testing.T) {
	runner := &recordingRunner{replies: []runnerReply{{}}}
	adapter := newClaudeFixture(t, runner, `{"mcpServers":{}}`, "")
	prior := ConnectorEntry{State: EntryPresent, Scope: ScopeUser, Transport: TransportStdio, Command: "/opt/prior server", Args: []string{"serve", "two words", ""}, Env: []EnvironmentVariable{{Name: "A", Value: "one"}, {Name: "EMPTY", Value: ""}}}
	if err := adapter.add(t.Context(), prior); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"claude", "mcp", "add", "--scope", "user", "wormhole", "--env", "A=one", "--env", "EMPTY=", "--", "/opt/prior server", "serve", "two words", ""}}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls=%#v", runner.calls)
	}
}

type blockedClaudeRunner struct {
	entered chan struct{}
	release chan struct{}
}

type blockedConnectorCoordinator struct {
	entered chan struct{}
	release chan struct{}
}

func (c *blockedConnectorCoordinator) WithOperationLock(ctx context.Context, _ AdapterName, _ string, operation func(context.Context) error) error {
	close(c.entered)
	<-c.release
	return operation(ctx)
}

func (r *blockedClaudeRunner) Run(context.Context, string, ...string) ([]byte, []byte, error) {
	select {
	case r.entered <- struct{}{}:
	default:
	}
	<-r.release
	return nil, nil, ErrConnectorUnavailable
}

func TestClaudeConfigDirPresenceAfterConstructionBlocksBeforeChildOrState(t *testing.T) {
	for name, value := range map[string]string{"empty": "", "malformed": "relative/config", "absolute": filepath.Join(t.TempDir(), "override")} {
		t.Run(name, func(t *testing.T) {
			priorOverride, overrideWasPresent := os.LookupEnv("CLAUDE_CONFIG_DIR")
			if err := os.Unsetenv("CLAUDE_CONFIG_DIR"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if overrideWasPresent {
					_ = os.Setenv("CLAUDE_CONFIG_DIR", priorOverride)
				} else {
					_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
				}
			})
			root := t.TempDir()
			userPath := filepath.Join(root, ".claude.json")
			working := filepath.Join(root, "work")
			if err := os.Mkdir(working, 0o700); err != nil {
				t.Fatal(err)
			}
			runner := &blockedClaudeRunner{entered: make(chan struct{}, 1), release: make(chan struct{})}
			adapter, err := NewClaudeAdapterAt(runner, "claude", "wormhole", userPath, working)
			if err != nil {
				t.Fatal(err)
			}
			prior := ConnectorEntry{State: EntryAbsent}
			desired := ConnectorEntry{State: EntryPresent, Scope: ScopeUser, Transport: TransportStdio, Command: "/opt/wormhole", Args: []string{"mcp"}, Env: []EnvironmentVariable{}}
			plan, _ := adapter.Plan(t.Context(), prior, desired)
			priorDigest, _ := DigestConnectorEntry(prior)
			desiredDigest, _ := DigestConnectorEntry(desired)
			change := ConfirmedConnectorChange{Adapter: AdapterClaude, Name: "wormhole", Action: OperationInstall, PlanDigest: plan.Digest, ExpectedPriorDigest: priorDigest, DesiredDigest: desiredDigest}
			backups := &memoryBackupStore{}
			journal := &memoryJournal{fault: map[OperationStage]error{}}
			coordinator := &blockedConnectorCoordinator{entered: make(chan struct{}), release: make(chan struct{})}

			result := make(chan error, 1)
			go func() {
				_, applyErr := ApplyTransactional(context.Background(), adapter, desired, change, backups, journal, coordinator)
				result <- applyErr
			}()
			<-coordinator.entered
			if err := os.Setenv("CLAUDE_CONFIG_DIR", value); err != nil {
				t.Fatal(err)
			}
			close(coordinator.release)
			select {
			case <-runner.entered:
				close(runner.release)
				<-result
				t.Fatal("Claude child started with CLAUDE_CONFIG_DIR present")
			case applyErr := <-result:
				if !errors.Is(applyErr, ErrUnsupportedConnectorEntry) {
					t.Fatalf("Apply error=%v", applyErr)
				}
			}
			if backups.puts != 0 || journal.activeCalls != 0 {
				t.Fatalf("backups=%d journal=%d", backups.puts, journal.activeCalls)
			}
			for _, path := range []string{userPath, filepath.Join(value, ".claude.json")} {
				if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("changed file %q: %v", path, statErr)
				}
			}
		})
	}
}

func TestClaudeConfigDirPresenceAtConstructionIsUnsupported(t *testing.T) {
	for name, value := range map[string]string{"empty": "", "malformed": "relative/config", "absolute": filepath.Join(t.TempDir(), "override")} {
		t.Run(name, func(t *testing.T) {
			working := t.TempDir()
			runner := &recordingRunner{}
			t.Setenv("CLAUDE_CONFIG_DIR", value)
			_, err := NewClaudeAdapterAt(runner, "claude", "wormhole", filepath.Join(t.TempDir(), ".claude.json"), working)
			if !errors.Is(err, ErrUnsupportedConnectorEntry) || len(runner.calls) != 0 {
				t.Fatalf("constructor error=%v calls=%#v", err, runner.calls)
			}
		})
	}
}

func runConnectorGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(t.TempDir(), "global.gitconfig"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func TestClaudeLocalScopeUsesMainRootForNormalAndLinkedWorktrees(t *testing.T) {
	root := t.TempDir()
	mainRoot := filepath.Join(root, "main")
	linkedRoot := filepath.Join(root, "linked")
	if err := os.Mkdir(mainRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	runConnectorGit(t, mainRoot, "init")
	runConnectorGit(t, mainRoot, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "initial")
	runConnectorGit(t, mainRoot, "worktree", "add", "-b", "linked-test", linkedRoot)
	userPath := filepath.Join(root, ".claude.json")
	data := `{"mcpServers":{"wormhole":{"type":"stdio","command":"/user","args":["mcp"],"env":{}}},"projects":{"` + mainRoot + `":{"mcpServers":{"wormhole":{"type":"stdio","command":"/local","args":["mcp"],"env":{}}}}}}`
	if err := os.WriteFile(userPath, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, worktree := range map[string]string{"normal": mainRoot, "linked": linkedRoot} {
		t.Run(name, func(t *testing.T) {
			nested := filepath.Join(worktree, "sub", "dir")
			if err := os.MkdirAll(nested, 0o700); err != nil {
				t.Fatal(err)
			}
			adapter, err := NewClaudeAdapterAt(&recordingRunner{}, "claude", "wormhole", userPath, nested)
			if err != nil {
				t.Fatal(err)
			}
			if adapter.localProjectKey != mainRoot {
				t.Fatalf("local key=%q want=%q", adapter.localProjectKey, mainRoot)
			}
			if _, err := adapter.Inspect(t.Context()); !errors.Is(err, ErrUnsupportedConnectorEntry) {
				t.Fatalf("hidden local error=%v", err)
			}
		})
	}
	if err := os.WriteFile(userPath, []byte(`{"mcpServers":{},"projects":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(linkedRoot, "sub", "dir")
	mainProjectFile := filepath.Join(mainRoot, ".mcp.json")
	projectEntry := []byte(`{"mcpServers":{"wormhole":{"type":"stdio","command":"/project","args":["mcp"],"env":{}}}}`)
	if err := os.WriteFile(mainProjectFile, projectEntry, 0o600); err != nil {
		t.Fatal(err)
	}
	adapter, err := NewClaudeAdapterAt(&recordingRunner{}, "claude", "wormhole", userPath, nested)
	if err != nil {
		t.Fatal(err)
	}
	if entry, err := adapter.Inspect(t.Context()); err != nil || entry.State != EntryAbsent {
		t.Fatalf("main-root project file leaked into cwd scope: entry=%#v err=%v", entry, err)
	}
	if err := os.WriteFile(filepath.Join(nested, ".mcp.json"), projectEntry, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Inspect(t.Context()); !errors.Is(err, ErrUnsupportedConnectorEntry) {
		t.Fatalf("cwd project scope error=%v", err)
	}
}

func TestClaudeRejectsMalformedAndBareGitTopology(t *testing.T) {
	for name, setup := range map[string]func(*testing.T, string){
		"malformed pointer": func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: missing\nextra\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"ambiguous pointer": func(t *testing.T, root string) {
			other := filepath.Join(filepath.Dir(root), "other")
			if err := os.Mkdir(other, 0o700); err != nil {
				t.Fatal(err)
			}
			runConnectorGit(t, other, "init")
			if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+filepath.Join(other, ".git")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"bare": func(t *testing.T, root string) { runConnectorGit(t, root, "init", "--bare") },
	} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "topology")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			setup(t, root)
			_, err := NewClaudeAdapterAt(&recordingRunner{}, "claude", "wormhole", filepath.Join(t.TempDir(), ".claude.json"), root)
			if !errors.Is(err, ErrInvalidConnectorPlan) {
				t.Fatalf("constructor error=%v", err)
			}
		})
	}
}

func TestFakeRunnersCannotReachRealClientConfiguration(t *testing.T) {
	runner := &recordingRunner{replies: []runnerReply{{stdout: []byte("codex-cli 0.149.0\n")}}}
	adapter, err := NewCodexAdapter(runner, "/definitely-not-a-real-codex", "wormhole")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := runner.calls; !reflect.DeepEqual(got, [][]string{{"/definitely-not-a-real-codex", "--version"}}) {
		t.Fatalf("calls=%#v", got)
	}
}

func TestAdaptersRejectNoncanonicalDesiredBeforeMutation(t *testing.T) {
	prior := ConnectorEntry{State: EntryAbsent}
	base := ConnectorEntry{State: EntryPresent, Scope: ScopeUser, Transport: TransportStdio, Command: "/opt/wormhole", Args: []string{"mcp"}, Env: []EnvironmentVariable{}}
	for name, constructor := range map[string]func(config.CommandRunner) (Adapter, error){
		"codex": func(runner config.CommandRunner) (Adapter, error) {
			return NewCodexAdapter(runner, "codex", "wormhole")
		},
		"claude": func(runner config.CommandRunner) (Adapter, error) {
			return NewClaudeAdapter(runner, "claude", "wormhole")
		},
	} {
		adapter, _ := constructor(&recordingRunner{})
		for mutation, desired := range map[string]ConnectorEntry{
			"relative command": func() ConnectorEntry { value := base; value.Command = "wormhole"; return value }(),
			"wrong args":       func() ConnectorEntry { value := base; value.Args = []string{"serve"}; return value }(),
			"environment": func() ConnectorEntry {
				value := base
				value.Env = []EnvironmentVariable{{Name: "TOKEN", Value: "secret"}}
				return value
			}(),
		} {
			t.Run(name+"/"+mutation, func(t *testing.T) {
				if _, err := adapter.Plan(t.Context(), prior, desired); !errors.Is(err, ErrUnsupportedConnectorEntry) {
					t.Fatalf("error=%v", err)
				}
			})
		}
	}
}
