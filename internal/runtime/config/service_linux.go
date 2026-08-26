//go:build linux

package config

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const systemctlExecutable = "/usr/bin/systemctl"

type serviceFilesystem interface {
	Lstat(string) (os.FileInfo, error)
	ReadFile(string) ([]byte, error)
	Mkdir(string, os.FileMode) error
	Chmod(string, os.FileMode) error
	CreateTemp(string, string) (*os.File, error)
	Rename(string, string) error
	Remove(string) error
	Open(string) (*os.File, error)
}

type osServiceFilesystem struct{}

func (osServiceFilesystem) Lstat(path string) (os.FileInfo, error) { return os.Lstat(path) }
func (osServiceFilesystem) ReadFile(path string) ([]byte, error)   { return os.ReadFile(path) }
func (osServiceFilesystem) Mkdir(path string, mode os.FileMode) error {
	return os.Mkdir(path, mode)
}
func (osServiceFilesystem) Chmod(path string, mode os.FileMode) error { return os.Chmod(path, mode) }
func (osServiceFilesystem) CreateTemp(path, pattern string) (*os.File, error) {
	return os.CreateTemp(path, pattern)
}
func (osServiceFilesystem) Rename(oldPath, newPath string) error { return os.Rename(oldPath, newPath) }
func (osServiceFilesystem) Remove(path string) error             { return os.Remove(path) }
func (osServiceFilesystem) Open(path string) (*os.File, error)   { return os.Open(path) }

type gatewayServiceHooks struct {
	filesystem   serviceFilesystem
	getenv       func(string) string
	homeDir      func() (string, error)
	tempDir      func() string
	dial         func(context.Context, string, string) (net.Conn, error)
	sleep        func(context.Context, time.Duration) error
	beforeCommit func()
}

type systemdGatewayService struct {
	runner CommandRunner
	hooks  gatewayServiceHooks
}

func NewGatewayService(runner CommandRunner) GatewayService {
	dialer := &net.Dialer{}
	return newGatewayServiceWithHooks(runner, gatewayServiceHooks{
		filesystem: osServiceFilesystem{},
		getenv:     os.Getenv,
		homeDir:    os.UserHomeDir,
		tempDir:    os.TempDir,
		dial:       dialer.DialContext,
		sleep: func(ctx context.Context, duration time.Duration) error {
			timer := time.NewTimer(duration)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
	})
}

func newGatewayServiceWithHooks(runner CommandRunner, hooks gatewayServiceHooks) GatewayService {
	return &systemdGatewayService{runner: runner, hooks: hooks}
}

func prepareCommandProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateCommandProcessGroup(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func (service *systemdGatewayService) Inspect(ctx context.Context) (ServiceState, error) {
	if err := service.managerAvailable(ctx); err != nil {
		return ServiceState{}, err
	}
	state, _, err := service.inspectAvailable(ctx)
	return state, err
}

func (service *systemdGatewayService) Install(ctx context.Context, change ConfirmedServiceChange) error {
	if err := service.managerAvailable(ctx); err != nil {
		return err
	}
	executable, err := service.validateExecutable(change.Executable)
	if err != nil {
		return err
	}
	paths, err := service.resolvePaths()
	if err != nil {
		return err
	}
	desired, err := renderGatewayUnit(executable, paths.runtimeRoot)
	if err != nil {
		return err
	}
	current, installed, err := service.inspectAvailable(ctx)
	if err != nil {
		return err
	}
	identical := current.Installed && string(installed.bytes) == string(desired)
	if !identical && !sameConfirmedServiceState(current, change.ExpectedPrior) {
		return ErrServiceChangeDrift
	}
	if err := service.ensureRuntimeDirectories(paths); err != nil {
		return err
	}
	if !identical {
		if err := service.writeUnitCAS(paths.unitPath, desired, installed); err != nil {
			return err
		}
		if err := service.runSystemctl(ctx, "daemon-reload"); err != nil {
			return fmt.Errorf("config: reload gatewayd unit: %w", err)
		}
		return service.runSystemctl(ctx, "enable", "--now", gatewayServiceUnit)
	}
	if current.Enabled && current.Active {
		return nil
	}
	if current.Enabled {
		return service.runSystemctl(ctx, "start", gatewayServiceUnit)
	}
	return service.runSystemctl(ctx, "enable", "--now", gatewayServiceUnit)
}

func (service *systemdGatewayService) Start(ctx context.Context) error {
	if err := service.managerAvailable(ctx); err != nil {
		return err
	}
	state, unit, err := service.inspectAvailable(ctx)
	if err != nil {
		return err
	}
	if !state.Installed {
		return ErrServiceNotInstalled
	}
	paths, err := service.resolvePaths()
	if err != nil {
		return err
	}
	executable, ok := parseGatewayUnit(unit.bytes, paths.runtimeRoot)
	if !ok {
		return ErrServiceStateUnknown
	}
	if _, err := service.validateExecutable(executable); err != nil {
		return err
	}
	if err := service.ensureRuntimeDirectories(paths); err != nil {
		return err
	}
	if state.Active {
		return nil
	}
	return service.runSystemctl(ctx, "start", gatewayServiceUnit)
}

func (service *systemdGatewayService) WaitReady(ctx context.Context) error {
	if err := service.managerAvailable(ctx); err != nil {
		return err
	}
	paths, err := service.resolvePaths()
	if err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %w", ErrServiceNotReady, err)
		}
		if err := probeGatewayReady(ctx, paths.socketPath, service.hooks.dial); err == nil {
			return nil
		}
		if err := service.hooks.sleep(ctx, 25*time.Millisecond); err != nil {
			return fmt.Errorf("%w: %w", ErrServiceNotReady, err)
		}
	}
}

type servicePaths struct {
	unitPath    string
	runtimeRoot string
	socketPath  string
}

func (service *systemdGatewayService) resolvePaths() (servicePaths, error) {
	configRoot := service.hooks.getenv("XDG_CONFIG_HOME")
	if configRoot == "" {
		home, err := service.hooks.homeDir()
		if err != nil {
			return servicePaths{}, fmt.Errorf("config: resolve service home: %w", err)
		}
		configRoot = filepath.Join(home, ".config")
	}
	runtimeRoot := service.hooks.getenv("XDG_RUNTIME_DIR")
	if runtimeRoot == "" {
		runtimeRoot = filepath.Join(service.hooks.tempDir(), "wormhole-runtime")
	}
	if !filepath.IsAbs(configRoot) || !filepath.IsAbs(runtimeRoot) {
		return servicePaths{}, fmt.Errorf("%w: service roots must be absolute", ErrUnsafeServicePath)
	}
	return servicePaths{
		unitPath:    filepath.Join(configRoot, "systemd", "user", gatewayServiceUnit),
		runtimeRoot: filepath.Clean(runtimeRoot),
		socketPath:  filepath.Join(filepath.Clean(runtimeRoot), "wormhole", "wormholed.sock"),
	}, nil
}

func (service *systemdGatewayService) managerAvailable(ctx context.Context) error {
	if service.runner == nil {
		return ErrServiceManagerUnavailable
	}
	_, _, err := service.runner.Run(ctx, systemctlExecutable, "--user", "show-environment")
	if err != nil {
		return ErrServiceManagerUnavailable
	}
	return nil
}

type unitSnapshot struct {
	exists bool
	info   os.FileInfo
	bytes  []byte
}

func (service *systemdGatewayService) inspectAvailable(ctx context.Context) (ServiceState, unitSnapshot, error) {
	paths, err := service.resolvePaths()
	if err != nil {
		return ServiceState{}, unitSnapshot{}, err
	}
	info, err := service.hooks.filesystem.Lstat(paths.unitPath)
	if errors.Is(err, os.ErrNotExist) {
		return ServiceState{}, unitSnapshot{}, nil
	}
	if err != nil {
		return ServiceState{}, unitSnapshot{}, fmt.Errorf("config: inspect gatewayd unit: %w", err)
	}
	if err := validateOwnerFile(info, 0o600); err != nil {
		return ServiceState{}, unitSnapshot{}, err
	}
	if info.Size() > 64<<10 {
		return ServiceState{}, unitSnapshot{}, fmt.Errorf("%w: oversized unit", ErrUnsafeServicePath)
	}
	unit, err := service.hooks.filesystem.ReadFile(paths.unitPath)
	if err != nil {
		return ServiceState{}, unitSnapshot{}, fmt.Errorf("config: read gatewayd unit: %w", err)
	}
	snapshot := unitSnapshot{exists: true, info: info, bytes: unit}
	state := ServiceState{Installed: true}
	if executable, ok := parseGatewayUnit(unit, paths.runtimeRoot); !ok {
		state.Diagnostic = "gatewayd service definition is stale"
	} else if _, err := service.validateExecutable(executable); err != nil {
		return ServiceState{}, unitSnapshot{}, err
	}
	state.Enabled, err = service.readSystemctlState(ctx, "is-enabled", map[int]string{0: "enabled", 1: "disabled"}, "enabled")
	if err != nil {
		return ServiceState{}, unitSnapshot{}, err
	}
	state.Active, err = service.readSystemctlState(ctx, "is-active", map[int]string{0: "active", 3: "inactive"}, "active")
	if err != nil {
		return ServiceState{}, unitSnapshot{}, err
	}
	if state.Diagnostic == "" {
		switch {
		case !state.Enabled:
			state.Diagnostic = "gatewayd service is installed but disabled"
		case !state.Active:
			state.Diagnostic = "gatewayd service is enabled but inactive"
		default:
			if probeGatewayReady(ctx, paths.socketPath, service.hooks.dial) == nil {
				state.Ready = true
			}
		}
	}
	return state, snapshot, nil
}

func (service *systemdGatewayService) readSystemctlState(ctx context.Context, action string, recognized map[int]string, truth string) (bool, error) {
	stdout, _, err := service.runner.Run(ctx, systemctlExecutable, "--user", action, gatewayServiceUnit)
	exit := 0
	if err != nil {
		var exitErr *CommandExitError
		if !errors.As(err, &exitErr) {
			return false, ErrServiceStateUnknown
		}
		exit = exitErr.ExitCode
	}
	value := strings.TrimSuffix(string(stdout), "\n")
	expected, ok := recognized[exit]
	if !ok || value != expected {
		return false, ErrServiceStateUnknown
	}
	return value == truth, nil
}

func (service *systemdGatewayService) runSystemctl(ctx context.Context, args ...string) error {
	_, _, err := service.runner.Run(ctx, systemctlExecutable, append([]string{"--user"}, args...)...)
	return err
}

func sameConfirmedServiceState(current, expected ServiceState) bool {
	return current.Installed == expected.Installed && current.Enabled == expected.Enabled && current.Active == expected.Active && current.Ready == expected.Ready
}

func (service *systemdGatewayService) validateExecutable(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", fmt.Errorf("%w: gatewayd executable must be canonical and absolute", ErrUnsafeServicePath)
	}
	if root := findRepositoryRoot(service.hooks.filesystem, filepath.Dir(path)); root != "" && pathWithin(root, path) {
		return "", ErrRepositoryContent
	}
	if err := validateTrustedDirectoryAncestors(service.hooks.filesystem, filepath.Dir(path)); err != nil {
		return "", err
	}
	info, err := service.hooks.filesystem.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("%w: inspect gatewayd executable", ErrUnsafeServicePath)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 {
		return "", fmt.Errorf("%w: gatewayd executable must be a non-writable regular owner executable", ErrUnsafeServicePath)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return "", fmt.Errorf("%w: gatewayd executable ownership", ErrUnsafeServicePath)
	}
	return path, nil
}

func findRepositoryRoot(filesystem serviceFilesystem, start string) string {
	current, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	for {
		if _, err := filesystem.Lstat(filepath.Join(current, ".git")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func renderGatewayUnit(executable, runtimeRoot string) ([]byte, error) {
	escapedExecutable, err := escapeSystemdValue(executable)
	if err != nil {
		return nil, err
	}
	escapedRuntime, err := escapeSystemdValue(runtimeRoot)
	if err != nil {
		return nil, err
	}
	return []byte("[Unit]\nDescription=Wormhole Gateway\nAfter=default.target\n\n[Service]\nType=simple\nExecStart=\"" + escapedExecutable + "\"\nEnvironment=\"XDG_RUNTIME_DIR=" + escapedRuntime + "\"\nRestart=on-failure\nRestartSec=2s\n\n[Install]\nWantedBy=default.target\n"), nil
}

func escapeSystemdValue(value string) (string, error) {
	var escaped strings.Builder
	for _, character := range value {
		if character == '$' || character < 0x20 || character == 0x7f {
			return "", fmt.Errorf("%w: unsafe service value", ErrUnsafeServicePath)
		}
		switch character {
		case '\\', '"':
			escaped.WriteByte('\\')
			escaped.WriteRune(character)
		case '%':
			escaped.WriteString("%%")
		default:
			escaped.WriteRune(character)
		}
	}
	return escaped.String(), nil
}

func parseGatewayUnit(unit []byte, runtimeRoot string) (string, bool) {
	prefix := "[Unit]\nDescription=Wormhole Gateway\nAfter=default.target\n\n[Service]\nType=simple\nExecStart=\""
	middle := "\"\nEnvironment=\"XDG_RUNTIME_DIR="
	suffix := "\"\nRestart=on-failure\nRestartSec=2s\n\n[Install]\nWantedBy=default.target\n"
	text := string(unit)
	if !strings.HasPrefix(text, prefix) || !strings.HasSuffix(text, suffix) {
		return "", false
	}
	remaining := strings.TrimSuffix(strings.TrimPrefix(text, prefix), suffix)
	parts := strings.Split(remaining, middle)
	if len(parts) != 2 {
		return "", false
	}
	executable, ok := unescapeSystemdValue(parts[0])
	if !ok {
		return "", false
	}
	runtime, ok := unescapeSystemdValue(parts[1])
	return executable, ok && runtime == runtimeRoot
}

func unescapeSystemdValue(value string) (string, bool) {
	var decoded strings.Builder
	for index := 0; index < len(value); index++ {
		switch value[index] {
		case '\\':
			index++
			if index == len(value) || (value[index] != '\\' && value[index] != '"') {
				return "", false
			}
			decoded.WriteByte(value[index])
		case '%':
			if index+1 >= len(value) || value[index+1] != '%' {
				return "", false
			}
			decoded.WriteByte('%')
			index++
		default:
			if value[index] == '$' || value[index] < 0x20 || value[index] == 0x7f {
				return "", false
			}
			decoded.WriteByte(value[index])
		}
	}
	return decoded.String(), true
}

func validateOwnerFile(info os.FileInfo, mode os.FileMode) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != mode || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return fmt.Errorf("%w: require owner-only regular unit", ErrUnsafeServicePath)
	}
	return nil
}

func (service *systemdGatewayService) writeUnitCAS(path string, desired []byte, expected unitSnapshot) error {
	directory := filepath.Dir(path)
	if err := service.ensureUnitDirectory(directory); err != nil {
		return err
	}
	temporary, err := service.hooks.filesystem.CreateTemp(directory, ".wormhole-gatewayd-*.tmp")
	if err != nil {
		return fmt.Errorf("config: create temporary gatewayd unit: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = service.hooks.filesystem.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("config: restrict temporary gatewayd unit: %w", err)
	}
	if _, err := temporary.Write(desired); err != nil {
		return fmt.Errorf("config: write temporary gatewayd unit: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("config: sync temporary gatewayd unit: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("config: close temporary gatewayd unit: %w", err)
	}
	if service.hooks.beforeCommit != nil {
		service.hooks.beforeCommit()
	}
	currentInfo, statErr := service.hooks.filesystem.Lstat(path)
	if !expected.exists && errors.Is(statErr, os.ErrNotExist) {
		// Expected absence remains absent.
	} else if statErr != nil || !expected.exists || !os.SameFile(currentInfo, expected.info) {
		return ErrServiceChangeDrift
	} else {
		if err := validateOwnerFile(currentInfo, 0o600); err != nil {
			return ErrServiceChangeDrift
		}
		current, err := service.hooks.filesystem.ReadFile(path)
		if err != nil || string(current) != string(expected.bytes) {
			return ErrServiceChangeDrift
		}
	}
	if err := service.hooks.filesystem.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("config: commit gatewayd unit: %w", err)
	}
	committed = true
	dir, err := service.hooks.filesystem.Open(directory)
	if err != nil {
		return fmt.Errorf("config: open service directory for sync: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("config: sync service directory: %w", err)
	}
	return nil
}

func (service *systemdGatewayService) ensureUnitDirectory(directory string) error {
	if err := validateTrustedDirectoryAncestors(service.hooks.filesystem, directory); err != nil {
		return err
	}
	configRoot := filepath.Dir(filepath.Dir(directory))
	paths := []string{configRoot, filepath.Join(configRoot, "systemd"), directory}
	for index, path := range paths {
		info, err := service.hooks.filesystem.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			if err := service.hooks.filesystem.Mkdir(path, 0o700); err != nil {
				return fmt.Errorf("config: create service directory: %w", err)
			}
			info, err = service.hooks.filesystem.Lstat(path)
		}
		if err != nil {
			return fmt.Errorf("config: inspect service directory: %w", err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("%w: service directory", ErrUnsafeServicePath)
		}
		if index == len(paths)-1 && info.Mode().Perm() != 0o700 {
			if err := service.hooks.filesystem.Chmod(path, 0o700); err != nil {
				return fmt.Errorf("config: restrict service directory: %w", err)
			}
		}
	}
	return nil
}

func (service *systemdGatewayService) ensureRuntimeDirectories(paths servicePaths) error {
	if err := validateTrustedDirectoryAncestors(service.hooks.filesystem, filepath.Dir(paths.socketPath)); err != nil {
		return err
	}
	for _, path := range []string{paths.runtimeRoot, filepath.Dir(paths.socketPath)} {
		info, err := service.hooks.filesystem.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			if err := service.hooks.filesystem.Mkdir(path, 0o700); err != nil {
				return fmt.Errorf("config: create runtime directory: %w", err)
			}
			info, err = service.hooks.filesystem.Lstat(path)
		}
		if err != nil {
			return fmt.Errorf("config: inspect runtime directory: %w", err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm() != 0o700 {
			return fmt.Errorf("%w: runtime directory must be owner-only", ErrUnsafeServicePath)
		}
	}
	return nil
}

// validateTrustedDirectoryAncestors rejects aliases before any path-based
// mutation. Root-owned non-writable directories are trusted; a root-owned
// sticky shared directory such as /tmp is the sole writable-ancestor case.
func validateTrustedDirectoryAncestors(filesystem serviceFilesystem, path string) error {
	current := filepath.Clean(path)
	for {
		info, err := filesystem.Lstat(current)
		if err == nil {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || (stat.Uid != uint32(os.Geteuid()) && stat.Uid != 0) {
				return fmt.Errorf("%w: untrusted directory ancestor %s", ErrUnsafeServicePath, current)
			}
			writable := info.Mode().Perm()&0o022 != 0
			rootSticky := stat.Uid == 0 && info.Mode()&os.ModeSticky != 0
			if writable && !rootSticky {
				return fmt.Errorf("%w: writable directory ancestor %s", ErrUnsafeServicePath, current)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: inspect directory ancestor %s", ErrUnsafeServicePath, current)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
		current = parent
	}
}
