package main

import (
	"bufio"
	"context"
	"errors"
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
	Transaction(context.Context, connector.AdapterName, string) error
}

type productionConnectorCommands struct {
	store       *connector.Store
	adapters    map[connector.AdapterName]connector.Adapter
	desired     connector.ConnectorEntry
	inspected   map[connector.AdapterName]connector.ConnectorEntry
	unavailable map[connector.AdapterName]bool
}

func newProductionConnectorCommands() (connectorCommandDependencies, error) {
	if runtime.GOOS != "linux" {
		return nil, connector.ErrConnectorFilesystemUnsupported
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
		adapters:  map[connector.AdapterName]connector.Adapter{},
		desired:   connector.ConnectorEntry{State: connector.EntryPresent, Scope: connector.ScopeUser, Transport: connector.TransportStdio, Command: wormholeExecutable, Args: []string{"mcp"}, Env: []connector.EnvironmentVariable{}},
		inspected: map[connector.AdapterName]connector.ConnectorEntry{}, unavailable: map[connector.AdapterName]bool{},
	}
	if executable, executableErr := canonicalNativeExecutable("codex"); executableErr == nil {
		adapter, adapterErr := connector.NewCodexAdapter(runner, executable, "wormhole")
		if adapterErr != nil {
			return nil, adapterErr
		}
		commands.adapters[connector.AdapterCodex] = adapter
	} else {
		commands.unavailable[connector.AdapterCodex] = true
	}
	if executable, executableErr := canonicalNativeExecutable("claude"); executableErr == nil {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil || !filepath.IsAbs(home) {
			commands.unavailable[connector.AdapterClaude] = true
		} else {
			adapter, adapterErr := connector.NewClaudeAdapterAt(runner, executable, "wormhole", filepath.Join(home, ".claude.json"), root)
			if adapterErr != nil {
				commands.unavailable[connector.AdapterClaude] = true
			} else {
				commands.adapters[connector.AdapterClaude] = adapter
			}
		}
	} else {
		commands.unavailable[connector.AdapterClaude] = true
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
	commands.inspected[name] = entry
	return availability, entry, nil
}

func (commands *productionConnectorCommands) Transaction(ctx context.Context, name connector.AdapterName, action string) error {
	adapter := commands.adapters[name]
	prior, inspected := commands.inspected[name]
	if adapter == nil || !inspected {
		return connector.ErrConnectorUnavailable
	}
	desired := commands.desired
	operation := connector.OperationInstall
	if action == "remove" {
		desired = connector.ConnectorEntry{State: connector.EntryAbsent}
		operation = connector.OperationRemove
	}
	if connector.EqualConnectorEntry(prior, desired) {
		return adapter.Verify(ctx, desired)
	}
	plan, err := adapter.Plan(ctx, prior, desired)
	if err != nil {
		return err
	}
	priorDigest, err := connector.DigestConnectorEntry(prior)
	if err != nil {
		return err
	}
	desiredDigest, err := connector.DigestConnectorEntry(desired)
	if err != nil {
		return err
	}
	change := connector.ConfirmedConnectorChange{Adapter: name, Name: "wormhole", Action: operation, PlanDigest: plan.Digest, ExpectedPriorDigest: priorDigest, DesiredDigest: desiredDigest}
	_, err = commands.applyConfirmed(ctx, adapter, desired, change)
	return err
}

func (commands *productionConnectorCommands) applyConfirmed(ctx context.Context, adapter connector.Adapter, desired connector.ConnectorEntry, change connector.ConfirmedConnectorChange) (connector.TransactionResult, error) {
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
	yes := false
	positionals := make([]string, 0, len(args)-1)
	for _, value := range args[1:] {
		if value == "--yes" {
			yes = true
			continue
		}
		positionals = append(positionals, value)
	}
	if action != "list" && action != "install" && action != "remove" {
		fmt.Fprintln(stderr, "usage: wormhole connector list|install|remove <codex|claude> [--yes]")
		return 2
	}
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
	fmt.Fprintf(stdout, "%s connector plan: %s wormhole\n", adapter, action)
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
	if err := dependencies.Transaction(ctx, adapter, action); err != nil {
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
