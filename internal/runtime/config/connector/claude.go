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
	"strings"

	"github.com/H4RL33/wormhole/internal/runtime/config"
)

var claudeVersionPattern = regexp.MustCompile(`^2\.1\.220 \(Claude Code\)\n$`)

type ClaudeAdapter struct {
	runner            config.CommandRunner
	executable        string
	name              string
	userConfigPath    string
	workingDirectory  string
	localProjectKey   string
	resolveUserConfig func() (string, error)
}

func NewClaudeAdapter(runner config.CommandRunner, executable, name string) (*ClaudeAdapter, error) {
	userConfigPath, pathErr := resolveClaudeUserConfigPath()
	root, rootErr := os.Getwd()
	if pathErr != nil || rootErr != nil {
		return nil, ErrInvalidConnectorPlan
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, ErrInvalidConnectorPlan
	}
	adapter, err := NewClaudeAdapterAt(runner, executable, name, userConfigPath, canonicalRoot)
	if err != nil {
		return nil, err
	}
	adapter.resolveUserConfig = resolveClaudeUserConfigPath
	return adapter, nil
}

func NewClaudeAdapterAt(runner config.CommandRunner, executable, name, userConfigPath, workingDirectory string) (*ClaudeAdapter, error) {
	if _, present := os.LookupEnv("CLAUDE_CONFIG_DIR"); present {
		return nil, ErrUnsupportedConnectorEntry
	}
	if runner == nil || !validConnectorValue(executable) || name != "wormhole" || !filepath.IsAbs(userConfigPath) || filepath.Clean(userConfigPath) != userConfigPath || !filepath.IsAbs(workingDirectory) || filepath.Clean(workingDirectory) != workingDirectory {
		return nil, ErrInvalidConnectorPlan
	}
	info, err := os.Lstat(workingDirectory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalidConnectorPlan
	}
	resolved, err := filepath.EvalSymlinks(workingDirectory)
	if err != nil || resolved != workingDirectory {
		return nil, ErrInvalidConnectorPlan
	}
	localProjectKey, err := claudeLocalProjectKey(workingDirectory)
	if err != nil {
		return nil, ErrInvalidConnectorPlan
	}
	return &ClaudeAdapter{runner: runner, executable: executable, name: name, userConfigPath: userConfigPath, workingDirectory: workingDirectory, localProjectKey: localProjectKey}, nil
}

func resolveClaudeUserConfigPath() (string, error) {
	if _, present := os.LookupEnv("CLAUDE_CONFIG_DIR"); present {
		return "", ErrUnsupportedConnectorEntry
	}
	configDirectory, err := os.UserHomeDir()
	if err != nil {
		return "", ErrInvalidConnectorPlan
	}
	if !validConnectorValue(configDirectory) || !filepath.IsAbs(configDirectory) || filepath.Clean(configDirectory) != configDirectory {
		return "", ErrInvalidConnectorPlan
	}
	info, err := os.Lstat(configDirectory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 || !nativeConnectorFileOwner(info) {
		return "", ErrInvalidConnectorPlan
	}
	resolved, err := filepath.EvalSymlinks(configDirectory)
	if err != nil || resolved != configDirectory {
		return "", ErrInvalidConnectorPlan
	}
	return filepath.Join(configDirectory, ".claude.json"), nil
}

func claudeLocalProjectKey(workingDirectory string) (string, error) {
	for directory := workingDirectory; ; directory = filepath.Dir(directory) {
		if claudeGitDirectory(directory) == nil {
			return "", ErrInvalidConnectorPlan
		}
		marker := filepath.Join(directory, ".git")
		info, err := os.Lstat(marker)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return "", ErrInvalidConnectorPlan
			}
			if info.IsDir() {
				if claudeGitDirectory(marker) != nil {
					return "", ErrInvalidConnectorPlan
				}
				return directory, nil
			}
			if !info.Mode().IsRegular() {
				return "", ErrInvalidConnectorPlan
			}
			return claudeLinkedWorktreeRoot(marker)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", ErrInvalidConnectorPlan
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return workingDirectory, nil
		}
	}
}

func claudeLinkedWorktreeRoot(marker string) (string, error) {
	gitdirValue, err := readClaudeGitPath(marker, "gitdir: ")
	if err != nil {
		return "", err
	}
	gitdir, err := canonicalClaudeGitPath(filepath.Dir(marker), gitdirValue)
	if err != nil {
		return "", err
	}
	commonValue, err := readClaudeGitPath(filepath.Join(gitdir, "commondir"), "")
	if err != nil {
		return "", err
	}
	commonDirectory, err := canonicalClaudeGitPath(gitdir, commonValue)
	if err != nil || filepath.Base(commonDirectory) != ".git" || claudeGitDirectory(commonDirectory) != nil {
		return "", ErrInvalidConnectorPlan
	}
	relativeGitdir, err := filepath.Rel(commonDirectory, gitdir)
	parts := strings.Split(relativeGitdir, string(filepath.Separator))
	if err != nil || len(parts) != 2 || parts[0] != "worktrees" || parts[1] == "" || parts[1] == "." || parts[1] == ".." {
		return "", ErrInvalidConnectorPlan
	}
	backlinkValue, err := readClaudeGitPath(filepath.Join(gitdir, "gitdir"), "")
	if err != nil {
		return "", err
	}
	backlink, err := canonicalClaudeGitPath(gitdir, backlinkValue)
	if err != nil || backlink != marker {
		return "", ErrInvalidConnectorPlan
	}
	mainRoot := filepath.Dir(commonDirectory)
	info, err := os.Lstat(mainRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", ErrInvalidConnectorPlan
	}
	resolved, err := filepath.EvalSymlinks(mainRoot)
	if err != nil || resolved != mainRoot {
		return "", ErrInvalidConnectorPlan
	}
	return mainRoot, nil
}

func readClaudeGitPath(path, prefix string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 || !nativeConnectorFileOwner(info) || info.Size() < 2 || info.Size() > maxConnectorValueBytes {
		return "", ErrInvalidConnectorPlan
	}
	file, err := os.Open(path)
	if err != nil {
		return "", ErrInvalidConnectorPlan
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", ErrInvalidConnectorPlan
	}
	data, err := io.ReadAll(io.LimitReader(file, maxConnectorValueBytes+1))
	if err != nil || int64(len(data)) != info.Size() || len(data) > maxConnectorValueBytes || !strings.HasSuffix(string(data), "\n") || strings.Count(string(data), "\n") != 1 {
		return "", ErrInvalidConnectorPlan
	}
	value := strings.TrimSuffix(string(data), "\n")
	if prefix != "" {
		if !strings.HasPrefix(value, prefix) {
			return "", ErrInvalidConnectorPlan
		}
		value = strings.TrimPrefix(value, prefix)
	}
	if !validConnectorValue(value) {
		return "", ErrInvalidConnectorPlan
	}
	return value, nil
}

func canonicalClaudeGitPath(base, value string) (string, error) {
	path := value
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return "", ErrInvalidConnectorPlan
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return "", ErrInvalidConnectorPlan
	}
	return path, nil
}

func claudeGitDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidConnectorPlan
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return ErrInvalidConnectorPlan
	}
	for _, name := range []string{"HEAD", "config"} {
		child, childErr := os.Lstat(filepath.Join(path, name))
		if childErr != nil || !child.Mode().IsRegular() || child.Mode()&os.ModeSymlink != 0 {
			return ErrInvalidConnectorPlan
		}
	}
	objects, err := os.Lstat(filepath.Join(path, "objects"))
	if err != nil || !objects.IsDir() || objects.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidConnectorPlan
	}
	return nil
}

func (a *ClaudeAdapter) validateUserConfigBinding() error {
	if _, present := os.LookupEnv("CLAUDE_CONFIG_DIR"); present {
		return ErrUnsupportedConnectorEntry
	}
	if a.resolveUserConfig == nil {
		return nil
	}
	path, err := a.resolveUserConfig()
	if err != nil || path != a.userConfigPath {
		return ErrUnsupportedConnectorEntry
	}
	return nil
}

func (a *ClaudeAdapter) AdapterName() AdapterName { return AdapterClaude }

func (a *ClaudeAdapter) Discover(ctx context.Context) (Availability, error) {
	if err := a.validateUserConfigBinding(); err != nil {
		return Availability{}, err
	}
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
	if err := a.validateUserConfigBinding(); err != nil {
		return ConnectorEntry{}, err
	}
	userRoot, userExists, err := readClaudeJSONObject(a.userConfigPath)
	if err != nil {
		return ConnectorEntry{}, err
	}
	projectRoot, projectExists, err := readClaudeJSONObject(filepath.Join(a.workingDirectory, ".mcp.json"))
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
		local, exists, localErr := claudeProjectEntry(userRoot, a.localProjectKey, a.name)
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
	if a.validateUserConfigBinding() != nil || validateConnectorEntry(entry) != nil || entry.State != EntryPresent {
		return ErrUnsupportedConnectorEntry
	}
	arguments := []string{"mcp", "add", "--scope", "user", a.name}
	for _, variable := range entry.Env {
		arguments = append(arguments, "--env", variable.Name+"="+variable.Value)
	}
	arguments = append(arguments, "--", entry.Command)
	arguments = append(arguments, entry.Args...)
	_, _, err := a.runner.Run(ctx, a.executable, arguments...)
	return redactedCommandError(err)
}

func (a *ClaudeAdapter) remove(ctx context.Context) error {
	if err := a.validateUserConfigBinding(); err != nil {
		return err
	}
	_, _, err := a.runner.Run(ctx, a.executable, "mcp", "remove", "--scope", "user", a.name)
	return redactedCommandError(err)
}
