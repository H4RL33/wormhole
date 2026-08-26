package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/H4RL33/wormhole/internal/runtime/config"
)

var claudeVersionPattern = regexp.MustCompile(`^2\.1\.220 \(Claude Code\)\n$`)

type ClaudeAdapter struct {
	runner         config.CommandRunner
	executable     string
	name           string
	userConfigPath string
	projectRoot    string
}

func NewClaudeAdapter(runner config.CommandRunner, executable, name string) (*ClaudeAdapter, error) {
	home, homeErr := os.UserHomeDir()
	root, rootErr := os.Getwd()
	if homeErr != nil || rootErr != nil {
		return nil, ErrInvalidConnectorPlan
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, ErrInvalidConnectorPlan
	}
	return NewClaudeAdapterAt(runner, executable, name, filepath.Join(home, ".claude.json"), canonicalRoot)
}

func NewClaudeAdapterAt(runner config.CommandRunner, executable, name, userConfigPath, projectRoot string) (*ClaudeAdapter, error) {
	if runner == nil || !validConnectorValue(executable) || name != "wormhole" || !filepath.IsAbs(userConfigPath) || filepath.Clean(userConfigPath) != userConfigPath || !filepath.IsAbs(projectRoot) || filepath.Clean(projectRoot) != projectRoot {
		return nil, ErrInvalidConnectorPlan
	}
	info, err := os.Lstat(projectRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalidConnectorPlan
	}
	resolved, err := filepath.EvalSymlinks(projectRoot)
	if err != nil || resolved != projectRoot {
		return nil, ErrInvalidConnectorPlan
	}
	return &ClaudeAdapter{runner: runner, executable: executable, name: name, userConfigPath: userConfigPath, projectRoot: projectRoot}, nil
}

func (a *ClaudeAdapter) AdapterName() AdapterName { return AdapterClaude }

func (a *ClaudeAdapter) Discover(ctx context.Context) (Availability, error) {
	stdout, stderr, err := a.runner.Run(ctx, a.executable, "--version")
	if len(stderr) != 0 {
		return Availability{}, ErrConnectorUnavailable
	}
	if err != nil {
		return Availability{}, redactedCommandError(err)
	}
	if !claudeVersionPattern.Match(stdout) {
		return Availability{}, ErrConnectorUnavailable
	}
	return Availability{Available: true, Version: "2.1.220"}, nil
}

func (a *ClaudeAdapter) Inspect(context.Context) (ConnectorEntry, error) {
	userRoot, userExists, err := readClaudeJSONObject(a.userConfigPath)
	if err != nil {
		return ConnectorEntry{}, err
	}
	projectRoot, projectExists, err := readClaudeJSONObject(filepath.Join(a.projectRoot, ".mcp.json"))
	if err != nil {
		return ConnectorEntry{}, err
	}

	var found []claudeScopedEntry
	if userExists {
		entry, exists, entryErr := claudeEntryAt(userRoot, "mcpServers", a.name)
		if entryErr != nil {
			return ConnectorEntry{}, entryErr
		}
		if exists {
			found = append(found, claudeScopedEntry{scope: ScopeUser, raw: entry})
		}
		local, exists, localErr := claudeProjectEntry(userRoot, a.projectRoot, a.name)
		if localErr != nil {
			return ConnectorEntry{}, localErr
		}
		if exists {
			found = append(found, claudeScopedEntry{raw: local})
		}
	}
	if projectExists {
		entry, exists, entryErr := claudeEntryAt(projectRoot, "mcpServers", a.name)
		if entryErr != nil {
			return ConnectorEntry{}, entryErr
		}
		if exists {
			found = append(found, claudeScopedEntry{raw: entry})
		}
	}
	if len(found) == 0 {
		return ConnectorEntry{State: EntryAbsent}, nil
	}
	if len(found) != 1 || found[0].scope != ScopeUser {
		return ConnectorEntry{}, ErrUnsupportedConnectorEntry
	}
	return decodeClaudeStdioEntry(found[0].raw)
}

type claudeScopedEntry struct {
	scope Scope
	raw   json.RawMessage
}

func readClaudeJSONObject(path string) (map[string]json.RawMessage, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 || !nativeConnectorFileOwner(info) || info.Size() < 2 || info.Size() > maxConnectorRecordBytes {
		return nil, false, ErrUnsupportedConnectorEntry
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false, ErrUnsupportedConnectorEntry
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, false, ErrUnsupportedConnectorEntry
	}
	data, err := io.ReadAll(io.LimitReader(file, maxConnectorRecordBytes+1))
	if err != nil || int64(len(data)) != info.Size() || len(data) > maxConnectorRecordBytes {
		return nil, false, ErrUnsupportedConnectorEntry
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanConnectorJSONValue(decoder); err != nil {
		return nil, false, ErrUnsupportedConnectorEntry
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, false, ErrUnsupportedConnectorEntry
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil || root == nil {
		return nil, false, ErrUnsupportedConnectorEntry
	}
	return root, true, nil
}

func claudeProjectEntry(root map[string]json.RawMessage, projectRoot, name string) (json.RawMessage, bool, error) {
	raw, exists := root["projects"]
	if !exists {
		return nil, false, nil
	}
	var projects map[string]json.RawMessage
	if err := json.Unmarshal(raw, &projects); err != nil || projects == nil {
		return nil, false, ErrUnsupportedConnectorEntry
	}
	project, exists := projects[projectRoot]
	if !exists {
		return nil, false, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(project, &object); err != nil || object == nil {
		return nil, false, ErrUnsupportedConnectorEntry
	}
	return claudeEntryAt(object, "mcpServers", name)
}

func claudeEntryAt(root map[string]json.RawMessage, key, name string) (json.RawMessage, bool, error) {
	raw, exists := root[key]
	if !exists {
		return nil, false, nil
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil || entries == nil {
		return nil, false, ErrUnsupportedConnectorEntry
	}
	entry, exists := entries[name]
	return entry, exists, nil
}

func decodeClaudeStdioEntry(raw json.RawMessage) (ConnectorEntry, error) {
	var wire struct {
		Type    string            `json:"type"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
	}
	if err := strictConnectorJSONDecode(raw, &wire); err != nil || wire.Type != "stdio" || wire.Args == nil || wire.Env == nil {
		return ConnectorEntry{}, ErrUnsupportedConnectorEntry
	}
	names := make([]string, 0, len(wire.Env))
	for name := range wire.Env {
		names = append(names, name)
	}
	sort.Strings(names)
	environment := make([]EnvironmentVariable, 0, len(names))
	for _, name := range names {
		environment = append(environment, EnvironmentVariable{Name: name, Value: wire.Env[name]})
	}
	entry := ConnectorEntry{State: EntryPresent, Scope: ScopeUser, Transport: TransportStdio, Command: wire.Command, Args: wire.Args, Env: environment}
	if validateConnectorEntry(entry) != nil {
		return ConnectorEntry{}, ErrUnsupportedConnectorEntry
	}
	return entry, nil
}

func (a *ClaudeAdapter) Plan(_ context.Context, prior, desired ConnectorEntry) (ChangePlan, error) {
	if !claudeRepresentableEntry(prior) {
		return ChangePlan{}, ErrUnsupportedConnectorEntry
	}
	action := OperationInstall
	if desired.State == EntryAbsent {
		action = OperationRemove
	} else if err := validateDesiredWormholeEntry(desired); err != nil {
		return ChangePlan{}, err
	}
	return BuildChangePlan(AdapterClaude, a.name, action, prior, desired)
}

func (a *ClaudeAdapter) Apply(ctx context.Context, plan ChangePlan) error {
	if err := a.validatePlan(plan); err != nil || plan.Action != OperationInstall || validateDesiredWormholeEntry(plan.Desired) != nil {
		return ErrInvalidConnectorPlan
	}
	return a.add(ctx, plan.Desired)
}

func (a *ClaudeAdapter) Verify(ctx context.Context, desired ConnectorEntry) error {
	observed, err := a.Inspect(ctx)
	if err != nil {
		return err
	}
	if !EqualConnectorEntry(observed, desired) {
		return ErrConnectorStateDrift
	}
	return nil
}

func (a *ClaudeAdapter) Rollback(ctx context.Context, plan ChangePlan) error {
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

func (a *ClaudeAdapter) Remove(ctx context.Context, prior ConnectorEntry) error {
	if validateConnectorEntry(prior) != nil || prior.State != EntryPresent {
		return ErrInvalidConnectorEntry
	}
	return a.remove(ctx)
}

func (a *ClaudeAdapter) validatePlan(plan ChangePlan) error {
	rebuilt, err := BuildChangePlan(AdapterClaude, a.name, plan.Action, plan.Prior, plan.Desired)
	if err != nil || rebuilt.Digest != plan.Digest {
		return ErrInvalidConnectorPlan
	}
	if !claudeRepresentableEntry(plan.Prior) || (plan.Desired.State == EntryPresent && validateDesiredWormholeEntry(plan.Desired) != nil) {
		return ErrUnsupportedConnectorEntry
	}
	return nil
}

func claudeRepresentableEntry(entry ConnectorEntry) bool {
	return entry.State == EntryAbsent || (validateConnectorEntry(entry) == nil && entry.State == EntryPresent)
}

func (a *ClaudeAdapter) add(ctx context.Context, entry ConnectorEntry) error {
	if validateConnectorEntry(entry) != nil || entry.State != EntryPresent {
		return ErrUnsupportedConnectorEntry
	}
	arguments := []string{"mcp", "add", "--scope", "user"}
	for _, variable := range entry.Env {
		arguments = append(arguments, "--env", variable.Name+"="+variable.Value)
	}
	arguments = append(arguments, a.name, "--", entry.Command)
	arguments = append(arguments, entry.Args...)
	_, _, err := a.runner.Run(ctx, a.executable, arguments...)
	return redactedCommandError(err)
}

func (a *ClaudeAdapter) remove(ctx context.Context) error {
	_, _, err := a.runner.Run(ctx, a.executable, "mcp", "remove", "--scope", "user", a.name)
	return redactedCommandError(err)
}
