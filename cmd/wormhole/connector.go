package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	runtimeconfig "github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/config/connector"
)

type connectorCommandDependencies interface {
	Inspect(context.Context, connector.AdapterName) (connector.Availability, connector.ConnectorEntry, error)
	Plan(context.Context, connector.AdapterName, string, connector.ConnectorEntry) (connector.ConnectorEntry, connector.ConfirmedConnectorChange, error)
	Transaction(context.Context, connector.AdapterName, connector.ConnectorEntry, connector.ConfirmedConnectorChange) error
}

type productionConnectorCommands struct {
	store       *connector.Store
	adapters    map[connector.AdapterName]connector.Adapter
	desired     connector.ConnectorEntry
	unavailable map[connector.AdapterName]bool
}

func newProductionConnectorCommands() (connectorCommandDependencies, error) {
	if runtime.GOOS != "linux" {
		return nil, connector.ErrConnectorFilesystemUnsupported
	}
	if _, present := os.LookupEnv("CLAUDE_CONFIG_DIR"); present {
		return nil, connector.ErrUnsupportedConnectorEntry
	}
	root, err := canonicalCurrentDirectory()
	if err != nil {
		return nil, err
	}
	wormholeExecutable, err := canonicalCurrentExecutable()
	if err != nil {
		return nil, err
	}
	runner := runtimeconfig.NewCommandRunner()
	commands := &productionConnectorCommands{
		adapters:    map[connector.AdapterName]connector.Adapter{},
		desired:     connector.ConnectorEntry{State: connector.EntryPresent, Scope: connector.ScopeUser, Transport: connector.TransportStdio, Command: wormholeExecutable, Args: []string{"mcp"}, Env: []connector.EnvironmentVariable{}},
		unavailable: map[connector.AdapterName]bool{},
	}
	if executable, executableErr := canonicalNativeExecutable("codex"); executableErr == nil {
		adapter, adapterErr := connector.NewCodexAdapter(runner, executable, "wormhole")
		if adapterErr != nil {
			return nil, adapterErr
		}
		commands.adapters[connector.AdapterCodex] = adapter
	} else if errors.Is(executableErr, exec.ErrNotFound) {
		commands.unavailable[connector.AdapterCodex] = true
	} else {
		return nil, executableErr
	}
	if executable, executableErr := canonicalNativeExecutable("claude"); executableErr == nil {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return nil, homeErr
		}
		if !filepath.IsAbs(home) {
			return nil, connector.ErrUnsupportedConnectorEntry
		} else {
			adapter, adapterErr := connector.NewClaudeAdapterAt(runner, executable, "wormhole", filepath.Join(home, ".claude.json"), root)
			if adapterErr != nil {
				return nil, adapterErr
			} else {
				commands.adapters[connector.AdapterClaude] = adapter
			}
		}
	} else if errors.Is(executableErr, exec.ErrNotFound) {
		commands.unavailable[connector.AdapterClaude] = true
	} else {
		return nil, executableErr
	}
	return commands, nil
}

func (commands *productionConnectorCommands) Inspect(ctx context.Context, name connector.AdapterName) (connector.Availability, connector.ConnectorEntry, error) {
	adapter := commands.adapters[name]
	if adapter == nil || commands.unavailable[name] {
		return connector.Availability{Available: false}, connector.ConnectorEntry{State: connector.EntryAbsent}, nil
	}
	availability, err := adapter.Discover(ctx)
	if err != nil {
		if errors.Is(err, connector.ErrConnectorUnavailable) {
			return connector.Availability{Available: false}, connector.ConnectorEntry{State: connector.EntryAbsent}, nil
		}
		return connector.Availability{}, connector.ConnectorEntry{}, err
	}
	entry, err := adapter.Inspect(ctx)
	if err != nil {
		return connector.Availability{}, connector.ConnectorEntry{}, err
	}
	return availability, entry, nil
}

func (commands *productionConnectorCommands) Plan(ctx context.Context, name connector.AdapterName, action string, prior connector.ConnectorEntry) (connector.ConnectorEntry, connector.ConfirmedConnectorChange, error) {
	adapter := commands.adapters[name]
	if adapter == nil {
		return connector.ConnectorEntry{}, connector.ConfirmedConnectorChange{}, connector.ErrConnectorUnavailable
	}
	desired := commands.desired
	operation := connector.OperationInstall
	if action == "remove" {
		desired = connector.ConnectorEntry{State: connector.EntryAbsent}
		operation = connector.OperationRemove
	}
	if connector.EqualConnectorEntry(prior, desired) {
		return desired, connector.ConfirmedConnectorChange{}, nil
	}
	plan, err := adapter.Plan(ctx, prior, desired)
	if err != nil {
		return connector.ConnectorEntry{}, connector.ConfirmedConnectorChange{}, err
	}
	priorDigest, err := connector.DigestConnectorEntry(prior)
	if err != nil {
		return connector.ConnectorEntry{}, connector.ConfirmedConnectorChange{}, err
	}
	desiredDigest, err := connector.DigestConnectorEntry(desired)
	if err != nil {
		return connector.ConnectorEntry{}, connector.ConfirmedConnectorChange{}, err
	}
	change := connector.ConfirmedConnectorChange{Adapter: name, Name: "wormhole", Action: operation, PlanDigest: plan.Digest, ExpectedPriorDigest: priorDigest, DesiredDigest: desiredDigest}
	return desired, change, nil
}

func (commands *productionConnectorCommands) Transaction(ctx context.Context, name connector.AdapterName, desired connector.ConnectorEntry, change connector.ConfirmedConnectorChange) error {
	adapter := commands.adapters[name]
	if adapter == nil {
		return connector.ErrConnectorUnavailable
	}
	if change == (connector.ConfirmedConnectorChange{}) {
		return adapter.Verify(ctx, desired)
	}
	_, err := commands.applyConfirmed(ctx, adapter, desired, change)
	return err
}

func (commands *productionConnectorCommands) applyConfirmed(ctx context.Context, adapter connector.Adapter, desired connector.ConnectorEntry, change connector.ConfirmedConnectorChange) (connector.TransactionResult, error) {
	return commands.applyConfirmedFor(ctx, adapter, desired, change, "")
}

func (commands *productionConnectorCommands) applyConfirmedFor(ctx context.Context, adapter connector.Adapter, desired connector.ConnectorEntry, change connector.ConfirmedConnectorChange, owner runtimeconfig.StateDigest) (connector.TransactionResult, error) {
	if commands.store == nil {
		store, err := connector.OpenStore()
		if err != nil {
			return connector.TransactionResult{}, err
		}
		commands.store = store
	}
	if change.Action == connector.OperationRemove {
		return connector.RemoveTransactional(ctx, adapter, change, commands.store, commands.store, commands.store)
	}
	if owner != "" {
		return connector.ApplyTransactionalFor(ctx, adapter, desired, change, owner, commands.store, commands.store, commands.store)
	}
	return connector.ApplyTransactional(ctx, adapter, desired, change, commands.store, commands.store, commands.store)
}

func canonicalCurrentDirectory() (string, error) {
	current, err := os.Getwd()
	if err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(current)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(absolute))
	if err != nil || resolved != filepath.Clean(absolute) {
		return "", errors.New("working directory is not canonical")
	}
	return resolved, nil
}

func canonicalCurrentExecutable() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return canonicalExecutablePath(executable)
}

func canonicalNativeExecutable(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	return canonicalExecutablePath(path)
}

func canonicalExecutablePath(value string) (string, error) {
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(absolute))
	if err != nil || !filepath.IsAbs(resolved) || filepath.Clean(resolved) != resolved || resolved == string(filepath.Separator) {
		return "", errors.New("executable path is not canonical")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("executable path is not executable")
	}
	return resolved, nil
}

func runConnector(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, dependencies connectorCommandDependencies) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: wormhole connector list|install|remove <codex|claude> [--yes]")
		return 2
	}
	action := args[0]
	if action != "list" && action != "install" && action != "remove" {
		fmt.Fprintln(stderr, "usage: wormhole connector list|install|remove <codex|claude> [--yes]")
		return 2
	}
	flags := flag.NewFlagSet("wormhole connector "+action, flag.ContinueOnError)
	var flagOutput bytes.Buffer
	flags.SetOutput(&flagOutput)
	yes := false
	if action != "list" {
		flags.BoolVar(&yes, "yes", false, "apply the rendered exact connector plan")
	}
	flags.Usage = func() {
		if action == "list" {
			fmt.Fprintln(&flagOutput, "usage: wormhole connector list <codex|claude>")
		} else {
			fmt.Fprintf(&flagOutput, "usage: wormhole connector %s [--yes] <codex|claude>\n", action)
		}
		flags.PrintDefaults()
	}
	flagArgs := make([]string, 0, len(args)-1)
	positionals := make([]string, 0, len(args)-1)
	for _, value := range args[1:] {
		if strings.HasPrefix(value, "-") {
			flagArgs = append(flagArgs, value)
		} else {
			positionals = append(positionals, value)
		}
	}
	if err := flags.Parse(flagArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = io.Copy(stdout, &flagOutput)
			return 0
		}
		_, _ = io.Copy(stderr, &flagOutput)
		return 2
	}
	positionals = append(positionals, flags.Args()...)
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "wormhole connector: exactly one adapter is required")
		return 2
	}
	adapter := connector.AdapterName(positionals[0])
	if adapter != connector.AdapterCodex && adapter != connector.AdapterClaude {
		fmt.Fprintln(stderr, "wormhole connector: adapter must be codex or claude")
		return 2
	}
	if dependencies == nil {
		var err error
		dependencies, err = newProductionConnectorCommands()
		if err != nil {
			fmt.Fprintln(stderr, "wormhole connector: connector state is unavailable")
			return 1
		}
	}
	availability, entry, err := dependencies.Inspect(ctx, adapter)
	if err != nil {
		fmt.Fprintln(stderr, "wormhole connector: native connector inspection failed")
		return 1
	}
	if action == "list" {
		state := "unavailable"
		if availability.Available {
			state = "available"
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s", adapter, state, entry.State)
		if entry.State == connector.EntryPresent {
			fmt.Fprintf(stdout, "\t%s\t%s", entry.Scope, entry.Transport)
		}
		fmt.Fprintln(stdout)
		return 0
	}
	if !availability.Available {
		fmt.Fprintln(stderr, "wormhole connector: native client unavailable")
		return 1
	}
	desired, change, err := dependencies.Plan(ctx, adapter, action, entry)
	if err != nil {
		fmt.Fprintln(stderr, "wormhole connector: native connector planning failed")
		return 1
	}
	fmt.Fprintf(stdout, "%s connector plan: %s wormhole\n", adapter, action)
	if change == (connector.ConfirmedConnectorChange{}) {
		fmt.Fprintln(stdout, "  no-op: exact desired state already present")
	} else {
		fmt.Fprintf(stdout, "  prior %s\n  desired %s\n  plan %s\n", change.ExpectedPriorDigest, change.DesiredDigest, change.PlanDigest)
	}
	if !yes {
		fmt.Fprint(stdout, "Apply connector change? [y/N] ")
		line, readErr := bufio.NewReader(io.LimitReader(stdin, 32)).ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			fmt.Fprintln(stderr, "wormhole connector: confirmation failed")
			return 1
		}
		answer := strings.ToLower(strings.TrimSpace(line))
		if answer != "y" && answer != "yes" {
			fmt.Fprintln(stderr, "wormhole connector: change was not confirmed")
			return 1
		}
	}
	if err := dependencies.Transaction(ctx, adapter, desired, change); err != nil {
		fmt.Fprintln(stderr, "wormhole connector: transaction failed")
		return 1
	}
	result := "installed"
	if action == "remove" {
		result = "removed"
	}
	fmt.Fprintf(stdout, "%s connector %s\n", adapter, result)
	return 0
}
