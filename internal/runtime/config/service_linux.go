//go:build linux

package config

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	systemctlExecutable = "/usr/bin/systemctl"
	serviceLockName     = ".wormhole-gatewayd.lock"
	servicePhaseName    = ".wormhole-gatewayd.install.json"
)

type gatewayServiceHooks struct {
	getenv                   func(string) string
	homeDir                  func() (string, error)
	tempDir                  func() string
	dial                     func(context.Context, string, string) (net.Conn, error)
	sleep                    func(context.Context, time.Duration) error
	beforeCommit             func()
	beforeConditionalPublish func()
	fault                    func(string) error
}

type systemdGatewayService struct {
	runner CommandRunner
	hooks  gatewayServiceHooks
}

func NewGatewayService(runner CommandRunner) GatewayService {
	dialer := &net.Dialer{}
	return newGatewayServiceWithHooks(runner, gatewayServiceHooks{
		getenv: os.Getenv, homeDir: os.UserHomeDir, tempDir: os.TempDir,
		dial: dialer.DialContext,
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

func (service *systemdGatewayService) Inspect(ctx context.Context) (ServiceState, error) {
	if err := service.managerAvailable(ctx); err != nil {
		return ServiceState{}, err
	}
	paths, err := service.resolvePaths()
	if err != nil {
		return ServiceState{}, err
	}
	directoryFD, err := openSecureDirectory(filepath.Dir(paths.unitPath), false, true)
	if errors.Is(err, os.ErrNotExist) {
		return ServiceState{UnitDigest: AbsentServiceUnitDigest}, nil
	}
	if err != nil {
		return ServiceState{}, err
	}
	defer unix.Close(directoryFD)
	return service.inspectPinned(ctx, paths, directoryFD)
}

func (service *systemdGatewayService) confirmChange(ctx context.Context, executable string, prior ServiceState) (ConfirmedServiceChange, error) {
	if err := ctx.Err(); err != nil {
		return ConfirmedServiceChange{}, err
	}
	pinned, err := openSecureExecutable(executable, uint32(os.Geteuid()), true)
	if err != nil {
		return ConfirmedServiceChange{}, err
	}
	_ = unix.Close(pinned)
	paths, err := service.resolvePaths()
	if err != nil {
		return ConfirmedServiceChange{}, err
	}
	unit, err := renderGatewayUnit(executable, paths.runtimeRoot)
	if err != nil {
		return ConfirmedServiceChange{}, err
	}
	if prior.UnitDigest == "" {
		return ConfirmedServiceChange{}, fmt.Errorf("%w: prior unit digest is required", ErrServiceChangeDrift)
	}
	return ConfirmedServiceChange{Executable: executable, ExpectedPrior: prior, DesiredUnitDigest: serviceUnitDigest(unit)}, nil
}

func (service *systemdGatewayService) Install(ctx context.Context, change ConfirmedServiceChange) error {
	if err := service.managerAvailable(ctx); err != nil {
		return err
	}
	paths, desired, err := service.validateConfirmedChange(ctx, change)
	if err != nil {
		return err
	}
	current, err := service.inspectForInstall(ctx, paths)
	if err != nil {
		return err
	}
	if isDesiredServiceState(current, change.DesiredUnitDigest) {
		return nil
	}

	directoryFD, err := openSecureDirectory(filepath.Dir(paths.unitPath), false, true)
	if errors.Is(err, os.ErrNotExist) {
		if !sameServiceMutationState(current, change.ExpectedPrior) {
			return ErrServiceChangeDrift
		}
		directoryFD, err = openSecureDirectory(filepath.Dir(paths.unitPath), true, true)
	}
	if err != nil {
		return err
	}
	defer unix.Close(directoryFD)
	unlock, err := lockServiceDirectory(ctx, directoryFD)
	if err != nil {
		return err
	}
	defer unlock()
	if err := verifyPinnedDirectory(filepath.Dir(paths.unitPath), directoryFD); err != nil {
		return err
	}
	current, err = service.inspectPinned(ctx, paths, directoryFD)
	if err != nil {
		return err
	}
	record, hasRecord, err := readInstallRecord(directoryFD)
	if err != nil {
		return err
	}
	if hasRecord {
		if record.PriorDigest != change.ExpectedPrior.UnitDigest || record.DesiredDigest != change.DesiredUnitDigest {
			return ErrServiceChangeDrift
		}
		if current.UnitDigest != change.ExpectedPrior.UnitDigest && current.UnitDigest != change.DesiredUnitDigest {
			return ErrServiceChangeDrift
		}
	} else {
		if isDesiredServiceState(current, change.DesiredUnitDigest) {
			return nil
		}
		if !sameServiceMutationState(current, change.ExpectedPrior) {
			return ErrServiceChangeDrift
		}
		phase := installPrepared
		if current.UnitDigest == change.DesiredUnitDigest && current.Loaded && !current.ReloadNeeded {
			phase = installReloaded
		}
		record = installRecord{SchemaVersion: 1, PriorDigest: change.ExpectedPrior.UnitDigest, DesiredDigest: change.DesiredUnitDigest, Phase: phase}
		if err := writeInstallRecord(directoryFD, record); err != nil {
			return err
		}
	}
	if err := service.ensureRuntimeDirectories(paths); err != nil {
		return err
	}
	if record.Phase == installPrepared {
		snapshot, err := readUnitSnapshot(directoryFD)
		if err != nil {
			return err
		}
		switch snapshot.digest {
		case change.DesiredUnitDigest:
		case change.ExpectedPrior.UnitDigest:
			if err := service.publishUnitConditional(directoryFD, desired, snapshot.digest); err != nil {
				return err
			}
		default:
			return ErrServiceChangeDrift
		}
		if err := verifyPinnedDirectory(filepath.Dir(paths.unitPath), directoryFD); err != nil {
			return err
		}
		record.Phase = installUnitPublished
		if err := writeInstallRecord(directoryFD, record); err != nil {
			return err
		}
	}
	if record.Phase == installUnitPublished {
		if err := service.runSystemctl(ctx, "daemon-reload"); err != nil {
			return fmt.Errorf("config: reload gatewayd unit: %w", err)
		}
		loaded, err := service.inspectPinned(ctx, paths, directoryFD)
		if err != nil {
			return err
		}
		if loaded.UnitDigest != change.DesiredUnitDigest || !loaded.Loaded || loaded.ReloadNeeded {
			return ErrServiceStateUnknown
		}
		record.Phase = installReloaded
		if err := writeInstallRecord(directoryFD, record); err != nil {
			return err
		}
	}
	if record.Phase == installReloaded {
		state, err := service.inspectPinned(ctx, paths, directoryFD)
		if err != nil {
			return err
		}
		switch {
		case !state.Enabled:
			err = service.runSystemctl(ctx, "enable", "--now", gatewayServiceUnit)
		case !state.Active:
			err = service.runSystemctl(ctx, "start", gatewayServiceUnit)
		}
		if err != nil {
			return err
		}
		state, err = service.inspectPinned(ctx, paths, directoryFD)
		if err != nil {
			return err
		}
		if !isDesiredServiceState(state, change.DesiredUnitDigest) {
			return ErrServiceStateUnknown
		}
		record.Phase = installManagerApplied
		if err := writeInstallRecord(directoryFD, record); err != nil {
			return err
		}
	}
	final, err := service.inspectPinned(ctx, paths, directoryFD)
	if err != nil {
		return err
	}
	if !isDesiredServiceState(final, change.DesiredUnitDigest) {
		return ErrServiceStateUnknown
	}
	return removeInstallRecord(directoryFD)
}

func (service *systemdGatewayService) Start(ctx context.Context) error {
	if err := service.managerAvailable(ctx); err != nil {
		return err
	}
	paths, err := service.resolvePaths()
	if err != nil {
		return err
	}
	directoryFD, err := openSecureDirectory(filepath.Dir(paths.unitPath), false, true)
	if errors.Is(err, os.ErrNotExist) {
		return ErrServiceNotInstalled
	}
	if err != nil {
		return err
	}
	defer unix.Close(directoryFD)
	unlock, err := lockServiceDirectory(ctx, directoryFD)
	if err != nil {
		return err
	}
	defer unlock()
	state, err := service.inspectPinned(ctx, paths, directoryFD)
	if err != nil {
		return err
	}
	if !state.Installed {
		return ErrServiceNotInstalled
	}
	snapshot, err := readUnitSnapshot(directoryFD)
	if err != nil {
		return err
	}
	executable, ok := parseGatewayUnit(snapshot.bytes, paths.runtimeRoot)
	if !ok {
		return ErrServiceStateUnknown
	}
	executableFD, err := openSecureExecutable(executable, uint32(os.Geteuid()), true)
	if err != nil {
		return err
	}
	_ = unix.Close(executableFD)
	if !state.Loaded || state.ReloadNeeded {
		return ErrServiceStateUnknown
	}
	if state.Active {
		return nil
	}
	if err := service.runSystemctl(ctx, "start", gatewayServiceUnit); err != nil {
		return err
	}
	state, err = service.inspectPinned(ctx, paths, directoryFD)
	if err != nil {
		return err
	}
	if !state.Active || !state.Loaded || state.ReloadNeeded || state.UnitDigest != snapshot.digest {
		return ErrServiceStateUnknown
	}
	return nil
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
		} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if err := service.hooks.sleep(ctx, 25*time.Millisecond); err != nil {
			return fmt.Errorf("%w: %w", ErrServiceNotReady, err)
		}
	}
}

type servicePaths struct{ unitPath, runtimeRoot, socketPath string }

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
	if !filepath.IsAbs(configRoot) || !filepath.IsAbs(runtimeRoot) || filepath.Clean(configRoot) != configRoot || filepath.Clean(runtimeRoot) != runtimeRoot {
		return servicePaths{}, fmt.Errorf("%w: service roots must be canonical and absolute", ErrUnsafeServicePath)
	}
	return servicePaths{filepath.Join(configRoot, "systemd", "user", gatewayServiceUnit), runtimeRoot, filepath.Join(runtimeRoot, "wormhole", "wormholed.sock")}, nil
}

func (service *systemdGatewayService) managerAvailable(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if service.runner == nil {
		return ErrServiceManagerUnavailable
	}
	systemctlFD, err := openSecureExecutable(systemctlExecutable, 0, false)
	if err != nil {
		return ErrServiceManagerUnavailable
	}
	_ = unix.Close(systemctlFD)
	_, _, err = service.runner.Run(ctx, systemctlExecutable, "--user", "show-environment")
	if err != nil {
		if contextErr := commandContextError(ctx, err); contextErr != nil {
			return contextErr
		}
		return ErrServiceManagerUnavailable
	}
	return nil
}

func commandContextError(ctx context.Context, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return nil
}

func (service *systemdGatewayService) validateConfirmedChange(ctx context.Context, change ConfirmedServiceChange) (servicePaths, []byte, error) {
	if err := ctx.Err(); err != nil {
		return servicePaths{}, nil, err
	}
	executableFD, err := openSecureExecutable(change.Executable, uint32(os.Geteuid()), true)
	if err != nil {
		return servicePaths{}, nil, err
	}
	_ = unix.Close(executableFD)
	paths, err := service.resolvePaths()
	if err != nil {
		return servicePaths{}, nil, err
	}
	desired, err := renderGatewayUnit(change.Executable, paths.runtimeRoot)
	if err != nil {
		return servicePaths{}, nil, err
	}
	if change.ExpectedPrior.UnitDigest == "" || change.DesiredUnitDigest == "" || serviceUnitDigest(desired) != change.DesiredUnitDigest {
		return servicePaths{}, nil, ErrServiceChangeDrift
	}
	return paths, desired, nil
}

func (service *systemdGatewayService) inspectForInstall(ctx context.Context, paths servicePaths) (ServiceState, error) {
	directoryFD, err := openSecureDirectory(filepath.Dir(paths.unitPath), false, true)
	if errors.Is(err, os.ErrNotExist) {
		return ServiceState{UnitDigest: AbsentServiceUnitDigest}, nil
	}
	if err != nil {
		return ServiceState{}, err
	}
	defer unix.Close(directoryFD)
	return service.inspectPinned(ctx, paths, directoryFD)
}

func (service *systemdGatewayService) inspectPinned(ctx context.Context, paths servicePaths, directoryFD int) (ServiceState, error) {
	if err := verifyPinnedDirectory(filepath.Dir(paths.unitPath), directoryFD); err != nil {
		return ServiceState{}, err
	}
	snapshot, err := readUnitSnapshot(directoryFD)
	if err != nil {
		return ServiceState{}, err
	}
	state := ServiceState{UnitDigest: snapshot.digest, Installed: snapshot.exists}
	if !snapshot.exists {
		return state, nil
	}
	executable, managed := parseGatewayUnit(snapshot.bytes, paths.runtimeRoot)
	if !managed {
		state.Diagnostic = "gatewayd service definition is stale"
	} else {
		executableFD, err := openSecureExecutable(executable, uint32(os.Geteuid()), true)
		if err != nil {
			return ServiceState{}, err
		}
		_ = unix.Close(executableFD)
	}
	state.Enabled, err = service.readSystemctlState(ctx, "is-enabled", map[int]string{0: "enabled", 1: "disabled"}, "enabled")
	if err != nil {
		return ServiceState{}, err
	}
	state.Active, err = service.readSystemctlState(ctx, "is-active", map[int]string{0: "active", 3: "inactive"}, "active")
	if err != nil {
		return ServiceState{}, err
	}
	state.Loaded, state.ReloadNeeded, err = service.readLoadedState(ctx, paths.unitPath)
	if err != nil {
		return ServiceState{}, err
	}
	if state.Diagnostic == "" {
		switch {
		case !state.Enabled:
			state.Diagnostic = "gatewayd service is installed but disabled"
		case !state.Active:
			state.Diagnostic = "gatewayd service is enabled but inactive"
		case !state.Loaded || state.ReloadNeeded:
			state.Diagnostic = "gatewayd service definition requires reload"
		default:
			probeErr := probeGatewayReady(ctx, paths.socketPath, service.hooks.dial)
			if probeErr == nil {
				state.Ready = true
			} else if errors.Is(probeErr, context.Canceled) || errors.Is(probeErr, context.DeadlineExceeded) {
				return ServiceState{}, probeErr
			} else {
				state.Diagnostic = "gatewayd service is active but not ready"
			}
		}
	}
	return state, nil
}

func (service *systemdGatewayService) readSystemctlState(ctx context.Context, action string, recognized map[int]string, truth string) (bool, error) {
	stdout, _, err := service.runner.Run(ctx, systemctlExecutable, "--user", action, gatewayServiceUnit)
	exit := 0
	if err != nil {
		if contextErr := commandContextError(ctx, err); contextErr != nil {
			return false, contextErr
		}
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

func (service *systemdGatewayService) readLoadedState(ctx context.Context, unitPath string) (bool, bool, error) {
	stdout, _, err := service.runner.Run(ctx, systemctlExecutable, "--user", "show", gatewayServiceUnit, "--property=FragmentPath", "--property=NeedDaemonReload", "--no-pager")
	if err != nil {
		if contextErr := commandContextError(ctx, err); contextErr != nil {
			return false, false, contextErr
		}
		return false, false, ErrServiceStateUnknown
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(stdout), "\n"), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return false, false, ErrServiceStateUnknown
		}
		if _, duplicate := values[key]; duplicate {
			return false, false, ErrServiceStateUnknown
		}
		values[key] = value
	}
	if len(values) != 2 || (values["NeedDaemonReload"] != "yes" && values["NeedDaemonReload"] != "no") {
		return false, false, ErrServiceStateUnknown
	}
	return values["FragmentPath"] == unitPath && values["NeedDaemonReload"] == "no", values["NeedDaemonReload"] == "yes", nil
}

func (service *systemdGatewayService) runSystemctl(ctx context.Context, args ...string) error {
	_, _, err := service.runner.Run(ctx, systemctlExecutable, append([]string{"--user"}, args...)...)
	if contextErr := commandContextError(ctx, err); contextErr != nil {
		return contextErr
	}
	return err
}

func sameServiceMutationState(left, right ServiceState) bool {
	return left.Installed == right.Installed && left.Enabled == right.Enabled && left.Active == right.Active && left.UnitDigest == right.UnitDigest && left.Loaded == right.Loaded && left.ReloadNeeded == right.ReloadNeeded
}

func isDesiredServiceState(state ServiceState, digest ServiceUnitDigest) bool {
	return state.Installed && state.Enabled && state.Active && state.UnitDigest == digest && state.Loaded && !state.ReloadNeeded
}

type unitSnapshot struct {
	exists bool
	digest ServiceUnitDigest
	bytes  []byte
}

func readUnitSnapshot(directoryFD int) (unitSnapshot, error) {
	fd, err := unix.Openat(directoryFD, gatewayServiceUnit, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return unitSnapshot{digest: AbsentServiceUnitDigest}, nil
	}
	if err != nil {
		return unitSnapshot{}, fmt.Errorf("%w: open gatewayd unit", ErrUnsafeServicePath)
	}
	file := os.NewFile(uintptr(fd), gatewayServiceUnit)
	defer file.Close()
	if err := validateRegularFD(fd, uint32(os.Geteuid()), 0o600, false); err != nil {
		return unitSnapshot{}, err
	}
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil {
		return unitSnapshot{}, fmt.Errorf("config: read gatewayd unit: %w", err)
	}
	if len(data) > 64<<10 {
		return unitSnapshot{}, fmt.Errorf("%w: oversized unit", ErrUnsafeServicePath)
	}
	return unitSnapshot{true, serviceUnitDigest(data), data}, nil
}

type installPhase string

const (
	installPrepared       installPhase = "prepared"
	installUnitPublished  installPhase = "unit_published"
	installReloaded       installPhase = "reloaded"
	installManagerApplied installPhase = "manager_applied"
)

type installRecord struct {
	SchemaVersion int               `json:"schema_version"`
	PriorDigest   ServiceUnitDigest `json:"prior_digest"`
	DesiredDigest ServiceUnitDigest `json:"desired_digest"`
	Phase         installPhase      `json:"phase"`
}

func readInstallRecord(directoryFD int) (installRecord, bool, error) {
	data, exists, err := readOwnerFileAt(directoryFD, servicePhaseName, 16<<10)
	if err != nil || !exists {
		return installRecord{}, exists, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record installRecord
	if err := decoder.Decode(&record); err != nil {
		return installRecord{}, false, fmt.Errorf("%w: invalid service phase", ErrServiceChangeDrift)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return installRecord{}, false, fmt.Errorf("%w: trailing service phase data", ErrServiceChangeDrift)
	}
	validPhase := record.Phase == installPrepared || record.Phase == installUnitPublished || record.Phase == installReloaded || record.Phase == installManagerApplied
	if record.SchemaVersion != 1 || record.PriorDigest == "" || record.DesiredDigest == "" || !validPhase {
		return installRecord{}, false, fmt.Errorf("%w: invalid service phase", ErrServiceChangeDrift)
	}
	return record, true, nil
}

func writeInstallRecord(directoryFD int, record installRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return atomicWriteOwnerFileAt(directoryFD, servicePhaseName, append(data, '\n'))
}

func removeInstallRecord(directoryFD int) error {
	if err := unix.Unlinkat(directoryFD, servicePhaseName, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("config: remove service phase: %w", err)
	}
	return unix.Fsync(directoryFD)
}

func readOwnerFileAt(directoryFD int, name string, limit int64) ([]byte, bool, error) {
	fd, err := unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("%w: open %s", ErrUnsafeServicePath, name)
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	if err := validateRegularFD(fd, uint32(os.Geteuid()), 0o600, false); err != nil {
		return nil, false, err
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, false, fmt.Errorf("%w: invalid %s", ErrUnsafeServicePath, name)
	}
	return data, true, nil
}

func atomicWriteOwnerFileAt(directoryFD int, name string, data []byte) error {
	temporary, err := randomTemporaryName(".wormhole-service-phase-")
	if err != nil {
		return err
	}
	fd, err := unix.Openat(directoryFD, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		_ = unix.Close(fd)
		if !committed {
			_ = unix.Unlinkat(directoryFD, temporary, 0)
		}
	}()
	if err := writeAllFD(fd, data); err != nil {
		return err
	}
	if err := unix.Fsync(fd); err != nil {
		return err
	}
	if err := unix.Renameat(directoryFD, temporary, directoryFD, name); err != nil {
		return err
	}
	committed = true
	return unix.Fsync(directoryFD)
}

func (service *systemdGatewayService) publishUnitConditional(directoryFD int, desired []byte, expected ServiceUnitDigest) error {
	temporary, err := randomTemporaryName(".wormhole-gatewayd-unit-")
	if err != nil {
		return err
	}
	fd, err := unix.Openat(directoryFD, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	keepTemporary := true
	defer func() {
		_ = unix.Close(fd)
		if keepTemporary {
			_ = unix.Unlinkat(directoryFD, temporary, 0)
		}
	}()
	if err := writeAllFD(fd, desired); err != nil {
		return err
	}
	if err := unix.Fsync(fd); err != nil {
		return err
	}
	if service.hooks.beforeCommit != nil {
		service.hooks.beforeCommit()
	}
	if service.hooks.beforeConditionalPublish != nil {
		service.hooks.beforeConditionalPublish()
	}
	if expected == AbsentServiceUnitDigest {
		if err := unix.Renameat2(directoryFD, temporary, directoryFD, gatewayServiceUnit, unix.RENAME_NOREPLACE); err != nil {
			if errors.Is(err, unix.EEXIST) {
				return ErrServiceChangeDrift
			}
			return err
		}
		keepTemporary = false
	} else {
		if err := unix.Renameat2(directoryFD, temporary, directoryFD, gatewayServiceUnit, unix.RENAME_EXCHANGE); err != nil {
			if errors.Is(err, unix.ENOENT) {
				return ErrServiceChangeDrift
			}
			return err
		}
		displaced, _, readErr := readOwnerFileAt(directoryFD, temporary, 64<<10)
		if readErr != nil || serviceUnitDigest(displaced) != expected {
			if rollbackErr := unix.Renameat2(directoryFD, temporary, directoryFD, gatewayServiceUnit, unix.RENAME_EXCHANGE); rollbackErr != nil {
				return fmt.Errorf("config: preserve concurrent service unit: %w", rollbackErr)
			}
			return ErrServiceChangeDrift
		}
		if err := unix.Unlinkat(directoryFD, temporary, 0); err != nil {
			return err
		}
		keepTemporary = false
	}
	if service.hooks.fault != nil {
		if err := service.hooks.fault("unit_directory_sync"); err != nil {
			return err
		}
	}
	return unix.Fsync(directoryFD)
}

func writeAllFD(fd int, data []byte) error {
	for len(data) > 0 {
		n, err := unix.Write(fd, data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func randomTemporaryName(prefix string) (string, error) {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(value[:]), nil
}

func lockServiceDirectory(ctx context.Context, directoryFD int) (func(), error) {
	fd, err := unix.Openat(directoryFD, serviceLockName, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	if err := validateRegularFD(fd, uint32(os.Geteuid()), 0o600, false); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	for {
		if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err == nil {
			return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = unix.Close(fd) }, nil
		} else if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = unix.Close(fd)
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			_ = unix.Close(fd)
			return nil, err
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func openSecureDirectory(path string, create, exactOwner bool) (int, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return -1, fmt.Errorf("%w: directory must be canonical and absolute", ErrUnsafeServicePath)
	}
	current, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	components := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for index, component := range components {
		if component == "" {
			continue
		}
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(openErr, unix.ENOENT) && create {
			if mkdirErr := unix.Mkdirat(current, component, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				_ = unix.Close(current)
				return -1, mkdirErr
			}
			next, openErr = unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if openErr != nil {
			_ = unix.Close(current)
			if errors.Is(openErr, unix.ENOENT) {
				return -1, os.ErrNotExist
			}
			return -1, fmt.Errorf("%w: open directory component %s", ErrUnsafeServicePath, component)
		}
		_ = unix.Close(current)
		current = next
		final := index == len(components)-1
		if err := validateDirectoryFD(current, final && exactOwner); err != nil {
			_ = unix.Close(current)
			return -1, err
		}
	}
	return current, nil
}

func validateDirectoryFD(fd int, exactOwner bool) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	uid := uint32(os.Geteuid())
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || (stat.Uid != uid && stat.Uid != 0) {
		return fmt.Errorf("%w: untrusted directory", ErrUnsafeServicePath)
	}
	if exactOwner {
		if stat.Uid != uid || stat.Mode&0o7777 != 0o700 {
			return fmt.Errorf("%w: owner-private directory required", ErrUnsafeServicePath)
		}
		return nil
	}
	writable := stat.Mode&0o022 != 0
	rootSticky := stat.Uid == 0 && stat.Mode&unix.S_ISVTX != 0
	if writable && !rootSticky {
		return fmt.Errorf("%w: writable directory ancestor", ErrUnsafeServicePath)
	}
	return nil
}

func verifyPinnedDirectory(path string, pinnedFD int) error {
	freshFD, err := openSecureDirectory(path, false, true)
	if err != nil {
		return err
	}
	defer unix.Close(freshFD)
	var pinned, fresh unix.Stat_t
	if unix.Fstat(pinnedFD, &pinned) != nil || unix.Fstat(freshFD, &fresh) != nil || pinned.Dev != fresh.Dev || pinned.Ino != fresh.Ino {
		return ErrServiceChangeDrift
	}
	return nil
}

func openSecureExecutable(path string, ownerUID uint32, rejectRepository bool) (int, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return -1, fmt.Errorf("%w: executable must be canonical and absolute", ErrUnsafeServicePath)
	}
	if rejectRepository && repositoryMarkerInAncestry(path) {
		return -1, ErrRepositoryContent
	}
	directoryFD, err := openSecureDirectory(filepath.Dir(path), false, false)
	if err != nil {
		return -1, err
	}
	defer unix.Close(directoryFD)
	fd, err := unix.Openat(directoryFD, filepath.Base(path), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return -1, fmt.Errorf("%w: open executable", ErrUnsafeServicePath)
	}
	if err := validateRegularFD(fd, ownerUID, 0, true); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}
func repositoryMarkerInAncestry(path string) bool {
	for current := filepath.Dir(path); ; current = filepath.Dir(current) {
		var stat unix.Stat_t
		if err := unix.Lstat(filepath.Join(current, ".git"), &stat); err == nil {
			return true
		}
		if current == "/" {
			return false
		}
	}
}

func validateRegularFD(fd int, ownerUID uint32, mode uint32, executable bool) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != ownerUID || stat.Nlink != 1 || stat.Mode&0o022 != 0 {
		return fmt.Errorf("%w: unsafe regular file", ErrUnsafeServicePath)
	}
	if mode != 0 && stat.Mode&0o7777 != mode {
		return fmt.Errorf("%w: unsafe file mode", ErrUnsafeServicePath)
	}
	if executable && stat.Mode&0o111 == 0 {
		return fmt.Errorf("%w: executable mode required", ErrUnsafeServicePath)
	}
	return nil
}

func (service *systemdGatewayService) ensureRuntimeDirectories(paths servicePaths) error {
	fd, err := openSecureDirectory(filepath.Dir(paths.socketPath), true, true)
	if err != nil {
		return err
	}
	return unix.Close(fd)
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
