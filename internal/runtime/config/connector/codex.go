package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"

	"github.com/H4RL33/wormhole/internal/runtime/config"
)

var codexVersionPattern = regexp.MustCompile(`^codex-cli 0\.149\.0\n$`)

type CodexAdapter struct {
	runner     config.CommandRunner
	executable string
	name       string
}

func NewCodexAdapter(runner config.CommandRunner, executable, name string) (*CodexAdapter, error) {
	if runner == nil || !validConnectorValue(executable) || name != "wormhole" {
		return nil, ErrInvalidConnectorPlan
	}
	return &CodexAdapter{runner: runner, executable: executable, name: name}, nil
}

func (a *CodexAdapter) AdapterName() AdapterName { return AdapterCodex }

func (a *CodexAdapter) Discover(ctx context.Context) (Availability, error) {
	stdout, stderr, err := a.runner.Run(ctx, a.executable, "--version")
	if len(stderr) != 0 {
		return Availability{}, ErrConnectorUnavailable
	}
	if err != nil {
		return Availability{}, redactedCommandError(err)
	}
	if !codexVersionPattern.Match(stdout) {
		return Availability{}, ErrConnectorUnavailable
	}
	return Availability{Available: true, Version: "0.149.0"}, nil
}

func (a *CodexAdapter) Inspect(ctx context.Context) (ConnectorEntry, error) {
	stdout, stderr, err := a.runner.Run(ctx, a.executable, "mcp", "get", a.name, "--json")
	if err != nil {
		var exitErr *config.CommandExitError
		absent := []byte("Error: No MCP server named '" + a.name + "' found.\n")
		if errors.As(err, &exitErr) && exitErr.ExitCode == 1 && len(stdout) == 0 && bytes.Equal(stderr, absent) {
			return ConnectorEntry{State: EntryAbsent}, nil
		}
		return ConnectorEntry{}, redactedCommandError(err)
	}
	if len(stderr) != 0 || len(stdout) == 0 {
		return ConnectorEntry{}, ErrUnsupportedConnectorEntry
	}
	var document codexGetDocument
	if err := strictConnectorJSONDecode(stdout, &document); err != nil || document.Name != a.name || document.Enabled == nil || !*document.Enabled || !jsonNull(document.DisabledReason) || !jsonNull(document.EnabledTools) || !jsonNull(document.DisabledTools) || !jsonNull(document.StartupTimeoutSec) || !jsonNull(document.ToolTimeoutSec) {
		return ConnectorEntry{}, ErrUnsupportedConnectorEntry
	}
	var discriminator struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(document.Transport, &discriminator); err != nil || discriminator.Type != "stdio" {
		return ConnectorEntry{}, ErrUnsupportedConnectorEntry
	}
	var transport codexStdioTransport
	if err := strictConnectorJSONDecode(document.Transport, &transport); err != nil || transport.Type != "stdio" || !jsonNull(transport.Cwd) || transport.EnvVars == nil || len(*transport.EnvVars) != 0 || transport.Args == nil || transport.Env == nil {
		return ConnectorEntry{}, ErrUnsupportedConnectorEntry
	}
	values := map[string]string{}
	if !jsonNull(transport.Env) {
		if err := strictConnectorJSONDecode(transport.Env, &values); err != nil {
			return ConnectorEntry{}, ErrUnsupportedConnectorEntry
		}
	}
	environment := make([]EnvironmentVariable, 0, len(values))
	for name, value := range values {
		environment = append(environment, EnvironmentVariable{Name: name, Value: value})
	}
	sort.Slice(environment, func(first, second int) bool { return environment[first].Name < environment[second].Name })
	entry := ConnectorEntry{State: EntryPresent, Scope: ScopeUser, Transport: TransportStdio, Command: transport.Command, Args: append([]string{}, (*transport.Args)...), Env: environment}
	if err := validateConnectorEntry(entry); err != nil {
		return ConnectorEntry{}, ErrUnsupportedConnectorEntry
	}
	return entry, nil
}

func (a *CodexAdapter) Plan(_ context.Context, prior, desired ConnectorEntry) (ChangePlan, error) {
	action := OperationInstall
	if desired.State == EntryAbsent {
		action = OperationRemove
	} else if err := validateDesiredWormholeEntry(desired); err != nil {
		return ChangePlan{}, err
	}
	return BuildChangePlan(AdapterCodex, a.name, action, prior, desired)
}

func (a *CodexAdapter) Apply(ctx context.Context, plan ChangePlan) error {
	if err := a.validatePlan(plan); err != nil || plan.Action != OperationInstall || validateDesiredWormholeEntry(plan.Desired) != nil {
		return ErrInvalidConnectorPlan
	}
	return a.add(ctx, plan.Desired)
}

func (a *CodexAdapter) Verify(ctx context.Context, desired ConnectorEntry) error {
	observed, err := a.Inspect(ctx)
	if err != nil {
		return err
	}
	if !EqualConnectorEntry(observed, desired) {
		return ErrConnectorStateDrift
	}
	return nil
}

func (a *CodexAdapter) Rollback(ctx context.Context, plan ChangePlan) error {
	if err := a.validatePlan(plan); err != nil {
		return err
	}
	current, err := a.Inspect(ctx)
	if err != nil {
		return err
	}
	if EqualConnectorEntry(current, plan.Prior) {
		return nil
	}
	if !EqualConnectorEntry(current, plan.Desired) {
		return ErrConnectorStateDrift
	}
	if plan.Prior.State == EntryAbsent {
		err = a.remove(ctx)
	} else {
		err = a.add(ctx, plan.Prior)
	}
	if err != nil {
		return err
	}
	return a.Verify(ctx, plan.Prior)
}

func (a *CodexAdapter) Remove(ctx context.Context, prior ConnectorEntry) error {
	if validateConnectorEntry(prior) != nil || prior.State != EntryPresent {
		return ErrInvalidConnectorEntry
	}
	return a.remove(ctx)
}

func (a *CodexAdapter) validatePlan(plan ChangePlan) error {
	rebuilt, err := BuildChangePlan(AdapterCodex, a.name, plan.Action, plan.Prior, plan.Desired)
	if err != nil || rebuilt.Digest != plan.Digest {
		return ErrInvalidConnectorPlan
	}
	if plan.Desired.State == EntryPresent && validateDesiredWormholeEntry(plan.Desired) != nil {
		return ErrUnsupportedConnectorEntry
	}
	return nil
}

func (a *CodexAdapter) add(ctx context.Context, entry ConnectorEntry) error {
	if validateConnectorEntry(entry) != nil || entry.State != EntryPresent {
		return ErrInvalidConnectorEntry
	}
	arguments := []string{"mcp", "add"}
	for _, variable := range entry.Env {
		arguments = append(arguments, "--env", variable.Name+"="+variable.Value)
	}
	arguments = append(arguments, a.name, "--", entry.Command)
	arguments = append(arguments, entry.Args...)
	_, _, err := a.runner.Run(ctx, a.executable, arguments...)
	return redactedCommandError(err)
}

func (a *CodexAdapter) remove(ctx context.Context) error {
	_, _, err := a.runner.Run(ctx, a.executable, "mcp", "remove", a.name)
	return redactedCommandError(err)
}

type codexGetDocument struct {
	Name              string          `json:"name"`
	Enabled           *bool           `json:"enabled"`
	DisabledReason    json.RawMessage `json:"disabled_reason"`
	Transport         json.RawMessage `json:"transport"`
	EnabledTools      json.RawMessage `json:"enabled_tools"`
	DisabledTools     json.RawMessage `json:"disabled_tools"`
	StartupTimeoutSec json.RawMessage `json:"startup_timeout_sec"`
	ToolTimeoutSec    json.RawMessage `json:"tool_timeout_sec"`
}

type codexStdioTransport struct {
	Type    string          `json:"type"`
	Command string          `json:"command"`
	Args    *[]string       `json:"args"`
	Env     json.RawMessage `json:"env"`
	EnvVars *[]string       `json:"env_vars"`
	Cwd     json.RawMessage `json:"cwd"`
}

func jsonNull(value json.RawMessage) bool { return bytes.Equal(value, []byte("null")) }
