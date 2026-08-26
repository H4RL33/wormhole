//go:build linux

package config

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
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
		return service.inspectAbsent(ctx, paths)
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
	defer unix.Close(pinned)
	executableDigest, err := digestExecutableFD(pinned)
	if err != nil {
		return ConfirmedServiceChange{}, err
	}
	paths, err := service.resolvePaths()
	if err != nil {
		return ConfirmedServiceChange{}, err
	}
	unit, err := renderGatewayUnit(managedExecutablePath(paths, executableDigest), paths.runtimeRoot)
	if err != nil {
		return ConfirmedServiceChange{}, err
	}
	if prior.UnitDigest == "" {
		return ConfirmedServiceChange{}, fmt.Errorf("%w: prior unit digest is required", ErrServiceChangeDrift)
	}
	return ConfirmedServiceChange{Executable: executable, ExecutableDigest: executableDigest, ExpectedPrior: prior, DesiredUnitDigest: serviceUnitDigest(unit)}, nil
}

func (service *systemdGatewayService) Install(ctx context.Context, change ConfirmedServiceChange) error {
	if err := service.managerAvailable(ctx); err != nil {
		return err
	}
	paths, desired, sourceFD, err := service.validateConfirmedChange(ctx, change)
	if err != nil {
		return err
	}
	defer unix.Close(sourceFD)
	current, err := service.inspectForInstall(ctx, paths)
	if err != nil && !isRecoverableOwnedUnitAbsence(current, err) {
		return err
	}
	directoryFD, err := openSecureDirectory(filepath.Dir(paths.unitPath), false, true)
	if errors.Is(err, os.ErrNotExist) {
		if !sameServiceMutationState(current, change.ExpectedPrior) {
			return ErrServiceChangeDrift
		}
		directoryFD, err = openSecureDirectoryWithFault(filepath.Dir(paths.unitPath), true, true, service.hooks.fault)
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
	if err != nil && !isRecoverableOwnedUnitAbsence(current, err) {
		return err
	}
	record, hasRecord, err := readInstallRecord(directoryFD)
	if err != nil {
		return err
	}
	if hasRecord {
		if record.Prior != serviceMutationStateFrom(change.ExpectedPrior) || record.DesiredDigest != change.DesiredUnitDigest {
			return ErrServiceChangeDrift
		}
		if record.Phase == installPrepared && record.Publish != nil && record.Publish.Stage != publishValidated {
			if err := service.recoverUnitPublish(directoryFD, &record); err != nil {
				return err
			}
			current, err = service.inspectPinned(ctx, paths, directoryFD)
			if err != nil {
				return err
			}
		}
		if isDesiredServiceState(current, change.DesiredUnitDigest, paths.unitPath) {
			return finishInstallRecord(directoryFD, record)
		}
		if !permittedInstallPhaseState(record, current, paths.unitPath) {
			return ErrServiceChangeDrift
		}
	} else {
		if isDesiredServiceState(current, change.DesiredUnitDigest, paths.unitPath) {
			return nil
		}
		if !sameServiceMutationState(current, change.ExpectedPrior) {
			return ErrServiceChangeDrift
		}
		phase := installPrepared
		if current.UnitDigest == change.DesiredUnitDigest && current.Loaded && !current.ReloadNeeded {
			phase = installReloaded
		}
		record = installRecord{SchemaVersion: 2, Prior: serviceMutationStateFrom(change.ExpectedPrior), DesiredDigest: change.DesiredUnitDigest, Phase: phase}
		if err := writeInstallRecord(directoryFD, record); err != nil {
			return err
		}
	}
	if err := service.ensureRuntimeDirectories(paths); err != nil {
		return err
	}
	if _, err := service.ensureManagedExecutable(paths, sourceFD, change.ExecutableDigest); err != nil {
		return err
	}
	if record.Phase == installPrepared {
		snapshot, err := readUnitSnapshot(directoryFD)
		if err != nil {
			return err
		}
		if record.Publish != nil {
			if err := service.recoverUnitPublish(directoryFD, &record); err != nil {
				return err
			}
		} else if snapshot.digest == change.ExpectedPrior.UnitDigest {
			observed, inspectErr := service.inspectPinned(ctx, paths, directoryFD)
			if inspectErr != nil || !sameServiceMutationState(observed, change.ExpectedPrior) {
				if inspectErr != nil {
					return inspectErr
				}
				return ErrServiceChangeDrift
			}
			if err := service.publishUnitConditional(directoryFD, desired, snapshot, &record); err != nil {
				return err
			}
		} else {
			return ErrServiceChangeDrift
		}
		if err := verifyPinnedDirectory(filepath.Dir(paths.unitPath), directoryFD); err != nil {
			return err
		}
		published, inspectErr := service.inspectPinned(ctx, paths, directoryFD)
		if inspectErr != nil || !isPublishedIntermediate(record, serviceMutationStateFrom(published)) {
			if inspectErr != nil {
				return inspectErr
			}
			return ErrServiceChangeDrift
		}
		record.Phase = installUnitPublished
		if err := writeInstallRecord(directoryFD, record); err != nil {
			return err
		}
	}
	if record.Phase == installUnitPublished {
		observed, inspectErr := service.inspectPinned(ctx, paths, directoryFD)
		if inspectErr != nil {
			return inspectErr
		}
		observedMutation := serviceMutationStateFrom(observed)
		if isReloadedIntermediate(record, observedMutation, paths.unitPath) {
			record.Phase = installReloaded
			if err := writeInstallRecord(directoryFD, record); err != nil {
				return err
			}
		} else if !isPublishedIntermediate(record, observedMutation) {
			return ErrServiceChangeDrift
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
		if !isReloadedIntermediate(record, serviceMutationStateFrom(loaded), paths.unitPath) {
			return ErrServiceChangeDrift
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
		stateMutation := serviceMutationStateFrom(state)
		if !isReloadedIntermediate(record, stateMutation, paths.unitPath) && !isManagerIntermediate(record, stateMutation, paths.unitPath) {
			return ErrServiceChangeDrift
		}
		switch {
		case !state.Enabled:
			err = service.runSystemctlWithExecutable(ctx, paths, change.ExecutableDigest, "enable", "--now", gatewayServiceUnit)
		case !state.Active:
			err = service.runSystemctlWithExecutable(ctx, paths, change.ExecutableDigest, "start", gatewayServiceUnit)
		}
		if err != nil {
			return err
		}
		state, err = service.inspectPinned(ctx, paths, directoryFD)
		if err != nil {
			return err
		}
		if !isDesiredServiceState(state, change.DesiredUnitDigest, paths.unitPath) {
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
	if !isDesiredServiceState(final, change.DesiredUnitDigest, paths.unitPath) {
		return ErrServiceStateUnknown
	}
	return finishInstallRecord(directoryFD, record)
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
	executableIdentity, executableDigest, err := service.validateManagedExecutable(paths, executable)
	if err != nil {
		return err
	}
	if !state.Loaded || state.ReloadNeeded {
		return ErrServiceStateUnknown
	}
	if state.Active {
		return nil
	}
	if err := service.runSystemctlWithExpectedExecutable(ctx, paths, executableDigest, executableIdentity, "start", gatewayServiceUnit); err != nil {
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

type servicePaths struct{ unitPath, runtimeRoot, socketPath, executableRoot string }

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
	return servicePaths{
		unitPath: filepath.Join(configRoot, "systemd", "user", gatewayServiceUnit), runtimeRoot: runtimeRoot,
		socketPath:     filepath.Join(runtimeRoot, "wormhole", "wormholed.sock"),
		executableRoot: filepath.Join(configRoot, "wormhole", "service-bin"),
	}, nil
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

func (service *systemdGatewayService) validateConfirmedChange(ctx context.Context, change ConfirmedServiceChange) (servicePaths, []byte, int, error) {
	if err := ctx.Err(); err != nil {
		return servicePaths{}, nil, -1, err
	}
	executableFD, err := openSecureExecutable(change.Executable, uint32(os.Geteuid()), true)
	if err != nil {
		return servicePaths{}, nil, -1, err
	}
	executableDigest, err := digestExecutableFD(executableFD)
	if err != nil || executableDigest != change.ExecutableDigest {
		_ = unix.Close(executableFD)
		return servicePaths{}, nil, -1, ErrServiceChangeDrift
	}
	paths, err := service.resolvePaths()
	if err != nil {
		_ = unix.Close(executableFD)
		return servicePaths{}, nil, -1, err
	}
	desired, err := renderGatewayUnit(managedExecutablePath(paths, executableDigest), paths.runtimeRoot)
	if err != nil {
		_ = unix.Close(executableFD)
		return servicePaths{}, nil, -1, err
	}
	if change.ExpectedPrior.UnitDigest == "" || !validServiceUnitDigest(change.ExecutableDigest, false) || !validServiceUnitDigest(change.DesiredUnitDigest, false) || serviceUnitDigest(desired) != change.DesiredUnitDigest {
		_ = unix.Close(executableFD)
		return servicePaths{}, nil, -1, ErrServiceChangeDrift
	}
	return paths, desired, executableFD, nil
}

func (service *systemdGatewayService) inspectForInstall(ctx context.Context, paths servicePaths) (ServiceState, error) {
	directoryFD, err := openSecureDirectory(filepath.Dir(paths.unitPath), false, true)
	if errors.Is(err, os.ErrNotExist) {
		return service.inspectAbsent(ctx, paths)
	}
	if err != nil {
		return ServiceState{}, err
	}
	defer unix.Close(directoryFD)
	return service.inspectPinned(ctx, paths, directoryFD)
}

func (service *systemdGatewayService) inspectPinned(ctx context.Context, paths servicePaths, directoryFD int) (ServiceState, error) {
	snapshot := unitSnapshot{digest: AbsentServiceUnitDigest}
	var err error
	if directoryFD >= 0 {
		if err := verifyPinnedDirectory(filepath.Dir(paths.unitPath), directoryFD); err != nil {
			return ServiceState{}, err
		}
		snapshot, err = readUnitSnapshot(directoryFD)
		if err != nil {
			return ServiceState{}, err
		}
	}
	state := ServiceState{UnitDigest: snapshot.digest, Installed: snapshot.exists}
	if snapshot.exists {
		executable, managed := parseGatewayUnit(snapshot.bytes, paths.runtimeRoot)
		if !managed {
			state.Diagnostic = "gatewayd service definition is stale"
		} else {
			if _, _, err := service.validateManagedExecutable(paths, executable); err != nil {
				return ServiceState{}, err
			}
		}
	}
	state.Enabled, err = service.readSystemctlState(ctx, "is-enabled", map[int]string{0: "enabled", 1: "disabled"}, "enabled")
	if err != nil {
		return ServiceState{}, err
	}
	state.Active, err = service.readSystemctlState(ctx, "is-active", map[int]string{0: "active", 3: "inactive"}, "active")
	if err != nil {
		return ServiceState{}, err
	}
	state.ManagerFragmentPath, state.ReloadNeeded, err = service.readLoadedState(ctx)
	if err != nil {
		return ServiceState{}, err
	}
	state.Loaded = state.ManagerFragmentPath == paths.unitPath
	if !snapshot.exists {
		if state.Enabled || state.Active || state.Loaded || state.ReloadNeeded || state.ManagerFragmentPath != "" {
			state.Diagnostic = "gatewayd manager retains an unowned service definition"
			return state, ErrServiceStateUnknown
		}
		return state, nil
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

func (service *systemdGatewayService) readLoadedState(ctx context.Context) (string, bool, error) {
	stdout, _, err := service.runner.Run(ctx, systemctlExecutable, "--user", "show", gatewayServiceUnit, "--property=FragmentPath", "--property=NeedDaemonReload", "--no-pager")
	if err != nil {
		if contextErr := commandContextError(ctx, err); contextErr != nil {
			return "", false, contextErr
		}
		return "", false, ErrServiceStateUnknown
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(stdout), "\n"), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return "", false, ErrServiceStateUnknown
		}
		if _, duplicate := values[key]; duplicate {
			return "", false, ErrServiceStateUnknown
		}
		values[key] = value
	}
	if len(values) != 2 || (values["NeedDaemonReload"] != "yes" && values["NeedDaemonReload"] != "no") {
		return "", false, ErrServiceStateUnknown
	}
	return values["FragmentPath"], values["NeedDaemonReload"] == "yes", nil
}

func (service *systemdGatewayService) runSystemctl(ctx context.Context, args ...string) error {
	_, _, err := service.runner.Run(ctx, systemctlExecutable, append([]string{"--user"}, args...)...)
	if contextErr := commandContextError(ctx, err); contextErr != nil {
		return contextErr
	}
	return err
}

func managedExecutablePath(paths servicePaths, digest ServiceUnitDigest) string {
	return filepath.Join(paths.executableRoot, "gatewayd-"+strings.TrimPrefix(string(digest), "sha256:"))
}

func digestExecutableFD(fd int) (ServiceUnitDigest, error) {
	hasher := sha256.New()
	buffer := make([]byte, 32<<10)
	var offset int64
	for {
		count, err := unix.Pread(fd, buffer, offset)
		if count > 0 {
			_, _ = hasher.Write(buffer[:count])
			offset += int64(count)
		}
		if errors.Is(err, io.EOF) || count == 0 {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return ServiceUnitDigest(fmt.Sprintf("sha256:%x", hasher.Sum(nil))), nil
}

func (service *systemdGatewayService) ensureManagedExecutable(paths servicePaths, sourceFD int, digest ServiceUnitDigest) (serviceFileIdentity, error) {
	directoryFD, err := openSecureDirectoryWithFault(paths.executableRoot, true, true, service.hooks.fault)
	if err != nil {
		return serviceFileIdentity{}, err
	}
	defer unix.Close(directoryFD)
	name := filepath.Base(managedExecutablePath(paths, digest))
	if identity, exists, err := readManagedExecutableAt(directoryFD, name, digest); err != nil || exists {
		return identity, err
	}
	temporary, err := randomTemporaryName(".wormhole-gatewayd-executable-")
	if err != nil {
		return serviceFileIdentity{}, err
	}
	fd, err := unix.Openat(directoryFD, temporary, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o500)
	if err != nil {
		return serviceFileIdentity{}, err
	}
	keep := true
	defer func() {
		_ = unix.Close(fd)
		if keep {
			_ = unix.Unlinkat(directoryFD, temporary, 0)
		}
	}()
	buffer := make([]byte, 32<<10)
	var offset int64
	for {
		count, readErr := unix.Pread(sourceFD, buffer, offset)
		if count > 0 {
			if err := writeAllFD(fd, buffer[:count]); err != nil {
				return serviceFileIdentity{}, err
			}
			offset += int64(count)
		}
		if count == 0 {
			break
		}
		if readErr != nil {
			return serviceFileIdentity{}, readErr
		}
	}
	if err := unix.Fchmod(fd, 0o500); err != nil {
		return serviceFileIdentity{}, err
	}
	if err := unix.Fsync(fd); err != nil {
		return serviceFileIdentity{}, err
	}
	copiedDigest, err := digestExecutableFD(fd)
	if err != nil {
		return serviceFileIdentity{}, err
	}
	if copiedDigest != digest {
		return serviceFileIdentity{}, ErrServiceChangeDrift
	}
	if err := unix.Renameat2(directoryFD, temporary, directoryFD, name, unix.RENAME_NOREPLACE); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return serviceFileIdentity{}, err
		}
		return readExistingManagedExecutable(directoryFD, name, digest)
	}
	keep = false
	if err := unix.Fsync(directoryFD); err != nil {
		return serviceFileIdentity{}, err
	}
	identity, exists, err := readManagedExecutableAt(directoryFD, name, digest)
	if err != nil || !exists {
		return serviceFileIdentity{}, err
	}
	return identity, nil
}

func readExistingManagedExecutable(directoryFD int, name string, digest ServiceUnitDigest) (serviceFileIdentity, error) {
	identity, exists, err := readManagedExecutableAt(directoryFD, name, digest)
	if err != nil {
		return serviceFileIdentity{}, err
	}
	if !exists {
		return serviceFileIdentity{}, ErrServiceChangeDrift
	}
	return identity, nil
}

func readManagedExecutableAt(directoryFD int, name string, digest ServiceUnitDigest) (serviceFileIdentity, bool, error) {
	fd, err := unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return serviceFileIdentity{}, false, nil
	}
	if err != nil {
		return serviceFileIdentity{}, false, ErrUnsafeServicePath
	}
	defer unix.Close(fd)
	if err := validateRegularFD(fd, uint32(os.Geteuid()), 0o500, true); err != nil {
		return serviceFileIdentity{}, false, err
	}
	actual, err := digestExecutableFD(fd)
	if err != nil {
		return serviceFileIdentity{}, false, err
	}
	if actual != digest {
		return serviceFileIdentity{}, false, ErrServiceChangeDrift
	}
	identity, err := serviceFileIdentityFromFD(fd, actual)
	return identity, true, err
}

func (service *systemdGatewayService) validateManagedExecutable(paths servicePaths, path string) (serviceFileIdentity, ServiceUnitDigest, error) {
	if repositoryMarkerInAncestry(path) || filepath.Dir(path) != paths.executableRoot {
		return serviceFileIdentity{}, "", ErrUnsafeServicePath
	}
	base := filepath.Base(path)
	prefix := "gatewayd-"
	if !strings.HasPrefix(base, prefix) {
		return serviceFileIdentity{}, "", ErrUnsafeServicePath
	}
	digest := ServiceUnitDigest("sha256:" + strings.TrimPrefix(base, prefix))
	if !validServiceUnitDigest(digest, false) || managedExecutablePath(paths, digest) != path {
		return serviceFileIdentity{}, "", ErrUnsafeServicePath
	}
	directoryFD, err := openSecureDirectory(paths.executableRoot, false, true)
	if err != nil {
		return serviceFileIdentity{}, "", err
	}
	defer unix.Close(directoryFD)
	identity, exists, err := readManagedExecutableAt(directoryFD, base, digest)
	if err != nil {
		return serviceFileIdentity{}, "", err
	}
	if !exists {
		return serviceFileIdentity{}, "", ErrUnsafeServicePath
	}
	return identity, digest, nil
}

func (service *systemdGatewayService) runSystemctlWithExecutable(ctx context.Context, paths servicePaths, digest ServiceUnitDigest, args ...string) error {
	identity, actual, err := service.validateManagedExecutable(paths, managedExecutablePath(paths, digest))
	if err != nil || actual != digest {
		return ErrServiceChangeDrift
	}
	return service.runSystemctlWithExpectedExecutable(ctx, paths, digest, identity, args...)
}

func (service *systemdGatewayService) runSystemctlWithExpectedExecutable(ctx context.Context, paths servicePaths, digest ServiceUnitDigest, expected serviceFileIdentity, args ...string) error {
	if len(args) == 0 {
		return ErrServiceStateUnknown
	}
	if err := service.publishFault("before_systemd_" + args[0]); err != nil {
		return err
	}
	identity, actual, err := service.validateManagedExecutable(paths, managedExecutablePath(paths, digest))
	if err != nil || actual != digest || identity != expected {
		return ErrServiceChangeDrift
	}
	if err := service.runSystemctl(ctx, args...); err != nil {
		return err
	}
	identity, actual, err = service.validateManagedExecutable(paths, managedExecutablePath(paths, digest))
	if err != nil || actual != digest || identity != expected {
		return ErrServiceChangeDrift
	}
	return nil
}

func sameServiceMutationState(left, right ServiceState) bool {
	return serviceMutationStateFrom(left) == serviceMutationStateFrom(right)
}

func isRecoverableOwnedUnitAbsence(state ServiceState, err error) bool {
	return errors.Is(err, ErrServiceStateUnknown) && !state.Installed && state.UnitDigest == AbsentServiceUnitDigest
}

func isDesiredServiceState(state ServiceState, digest ServiceUnitDigest, unitPath string) bool {
	return state.Installed && state.Enabled && state.Active && state.UnitDigest == digest && state.Loaded && !state.ReloadNeeded && state.ManagerFragmentPath == unitPath
}

func (service *systemdGatewayService) inspectAbsent(ctx context.Context, paths servicePaths) (ServiceState, error) {
	return service.inspectPinned(ctx, paths, -1)
}

type unitSnapshot struct {
	exists bool
	digest ServiceUnitDigest
	bytes  []byte
	file   serviceFileIdentity
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
	identity, err := serviceFileIdentityFromFD(fd, serviceUnitDigest(data))
	if err != nil {
		return unitSnapshot{}, err
	}
	return unitSnapshot{true, identity.Digest, data, identity}, nil
}

type installPhase string

const (
	installPrepared       installPhase = "prepared"
	installUnitPublished  installPhase = "unit_published"
	installReloaded       installPhase = "reloaded"
	installManagerApplied installPhase = "manager_applied"
)

type installRecord struct {
	SchemaVersion int                  `json:"schema_version"`
	Prior         serviceMutationState `json:"prior"`
	DesiredDigest ServiceUnitDigest    `json:"desired_digest"`
	Phase         installPhase         `json:"phase"`
	Publish       *unitPublishRecord   `json:"publish,omitempty"`
}

type serviceFileIdentity struct {
	Device uint64            `json:"device"`
	Inode  uint64            `json:"inode"`
	Digest ServiceUnitDigest `json:"digest"`
}

type unitPublishRecord struct {
	Stage     string              `json:"stage"`
	Temporary string              `json:"temporary"`
	Rescue    string              `json:"rescue"`
	Expected  serviceFileIdentity `json:"expected"`
	Desired   serviceFileIdentity `json:"desired"`
	WasAbsent bool                `json:"was_absent"`
}

const (
	publishExchangeReady = "exchange_ready"
	publishValidated     = "validated"
	publishRestoring     = "restoring"
)

type serviceMutationState struct {
	Installed           bool              `json:"installed"`
	Enabled             bool              `json:"enabled"`
	Active              bool              `json:"active"`
	UnitDigest          ServiceUnitDigest `json:"unit_digest"`
	Loaded              bool              `json:"loaded"`
	ReloadNeeded        bool              `json:"reload_needed"`
	ManagerFragmentPath string            `json:"manager_fragment_path"`
}

func serviceMutationStateFrom(state ServiceState) serviceMutationState {
	return serviceMutationState{
		Installed: state.Installed, Enabled: state.Enabled, Active: state.Active,
		UnitDigest: state.UnitDigest, Loaded: state.Loaded, ReloadNeeded: state.ReloadNeeded,
		ManagerFragmentPath: state.ManagerFragmentPath,
	}
}

func permittedInstallPhaseState(record installRecord, state ServiceState, unitPath string) bool {
	current := serviceMutationStateFrom(state)
	switch record.Phase {
	case installPrepared:
		return current == record.Prior || isPublishedIntermediate(record, current)
	case installUnitPublished:
		return isPublishedIntermediate(record, current) || isReloadedIntermediate(record, current, unitPath)
	case installReloaded:
		return isReloadedIntermediate(record, current, unitPath) || isManagerIntermediate(record, current, unitPath)
	case installManagerApplied:
		return isDesiredMutationState(current, record.DesiredDigest, unitPath)
	default:
		return false
	}
}

func isPublishedIntermediate(record installRecord, current serviceMutationState) bool {
	prior := record.Prior
	return current.Installed && current.UnitDigest == record.DesiredDigest &&
		current.Enabled == prior.Enabled && current.Active == prior.Active &&
		current.Loaded == prior.Loaded && current.ManagerFragmentPath == prior.ManagerFragmentPath &&
		(current.ReloadNeeded == prior.ReloadNeeded || current.ReloadNeeded)
}

func isReloadedIntermediate(record installRecord, current serviceMutationState, unitPath string) bool {
	return current.Installed && current.UnitDigest == record.DesiredDigest && current.Loaded && !current.ReloadNeeded &&
		current.ManagerFragmentPath == unitPath && current.Enabled == record.Prior.Enabled && current.Active == record.Prior.Active
}

func isManagerIntermediate(record installRecord, current serviceMutationState, unitPath string) bool {
	if !current.Installed || current.UnitDigest != record.DesiredDigest || !current.Loaded || current.ReloadNeeded || current.ManagerFragmentPath != unitPath {
		return false
	}
	prior := record.Prior
	if current.Enabled == prior.Enabled && current.Active == prior.Active {
		return true
	}
	if !prior.Enabled && current.Enabled && (current.Active == prior.Active || current.Active) {
		return true
	}
	return prior.Enabled && current.Enabled && !prior.Active && current.Active
}

func isDesiredMutationState(current serviceMutationState, digest ServiceUnitDigest, unitPath string) bool {
	return current.Installed && current.Enabled && current.Active && current.UnitDigest == digest && current.Loaded && !current.ReloadNeeded && current.ManagerFragmentPath == unitPath
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
	canonical, marshalErr := json.Marshal(record)
	canonical = append(canonical, '\n')
	if marshalErr != nil || !bytes.Equal(data, canonical) || record.SchemaVersion != 2 || !validServiceUnitDigest(record.Prior.UnitDigest, true) || !validServiceUnitDigest(record.DesiredDigest, false) || !validPhase {
		return installRecord{}, false, fmt.Errorf("%w: invalid service phase", ErrServiceChangeDrift)
	}
	if record.Prior.Installed != (record.Prior.UnitDigest != AbsentServiceUnitDigest) || (record.Prior.Loaded && record.Prior.ManagerFragmentPath == "") || !validInstallRecordPublish(record) {
		return installRecord{}, false, fmt.Errorf("%w: inconsistent service phase", ErrServiceChangeDrift)
	}
	return record, true, nil
}

func validServiceUnitDigest(digest ServiceUnitDigest, allowAbsent bool) bool {
	if allowAbsent && digest == AbsentServiceUnitDigest {
		return true
	}
	text := string(digest)
	if len(text) != len("sha256:")+64 || !strings.HasPrefix(text, "sha256:") {
		return false
	}
	for _, character := range text[len("sha256:"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func writeInstallRecord(directoryFD int, record installRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return atomicWriteOwnerFileAt(directoryFD, servicePhaseName, append(data, '\n'))
}

func validInstallRecordPublish(record installRecord) bool {
	publish := record.Publish
	if publish == nil {
		if record.Phase == installPrepared {
			return true
		}
		return (record.Phase == installReloaded || record.Phase == installManagerApplied) &&
			record.Prior.UnitDigest == record.DesiredDigest && record.Prior.Loaded && !record.Prior.ReloadNeeded
	}
	validStage := publish.Stage == publishExchangeReady || publish.Stage == publishValidated || publish.Stage == publishRestoring
	validName := func(name, prefix string) bool {
		return strings.HasPrefix(name, prefix) && filepath.Base(name) == name && len(name) > len(prefix)
	}
	if !validStage || (record.Phase != installPrepared && publish.Stage != publishValidated) ||
		!validName(publish.Temporary, ".wormhole-gatewayd-unit-") || !validName(publish.Rescue, ".wormhole-gatewayd-rescue-") ||
		publish.Desired.Device == 0 || publish.Desired.Inode == 0 || !validServiceUnitDigest(publish.Desired.Digest, false) {
		return false
	}
	if publish.Desired.Digest != record.DesiredDigest || publish.Expected.Digest != record.Prior.UnitDigest || publish.WasAbsent != (record.Prior.UnitDigest == AbsentServiceUnitDigest) {
		return false
	}
	if publish.WasAbsent {
		return publish.Expected == (serviceFileIdentity{Digest: AbsentServiceUnitDigest})
	}
	return publish.Expected.Device != 0 && publish.Expected.Inode != 0 && validServiceUnitDigest(publish.Expected.Digest, false)
}

func removeInstallRecord(directoryFD int) error {
	if err := unix.Unlinkat(directoryFD, servicePhaseName, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("config: remove service phase: %w", err)
	}
	return unix.Fsync(directoryFD)
}

func finishInstallRecord(directoryFD int, record installRecord) error {
	if record.Publish != nil {
		if record.Publish.Stage != publishValidated {
			return ErrServiceChangeDrift
		}
		if _, exists, err := readServiceFileIdentityAt(directoryFD, record.Publish.Rescue); err != nil || exists {
			if err != nil {
				return err
			}
			return ErrServiceChangeDrift
		}
		temporary, exists, err := readServiceFileIdentityAt(directoryFD, record.Publish.Temporary)
		if err != nil {
			return err
		}
		if exists {
			if record.Publish.WasAbsent || temporary != record.Publish.Expected {
				return ErrServiceChangeDrift
			}
			if err := unix.Unlinkat(directoryFD, record.Publish.Temporary, 0); err != nil {
				return err
			}
			if err := unix.Fsync(directoryFD); err != nil {
				return err
			}
		}
	}
	return removeInstallRecord(directoryFD)
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

func (service *systemdGatewayService) publishUnitConditional(directoryFD int, desired []byte, expected unitSnapshot, record *installRecord) error {
	temporary, err := randomTemporaryName(".wormhole-gatewayd-unit-")
	if err != nil {
		return err
	}
	fd, err := unix.Openat(directoryFD, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	journaled := false
	defer func() {
		_ = unix.Close(fd)
		if !journaled {
			_ = unix.Unlinkat(directoryFD, temporary, 0)
		}
	}()
	if err := writeAllFD(fd, desired); err != nil {
		return err
	}
	if err := unix.Fsync(fd); err != nil {
		return err
	}
	desiredIdentity, err := serviceFileIdentityFromFD(fd, serviceUnitDigest(desired))
	if err != nil {
		return err
	}
	rescue, err := randomTemporaryName(".wormhole-gatewayd-rescue-")
	if err != nil {
		return err
	}
	expectedIdentity := expected.file
	if !expected.exists {
		expectedIdentity = serviceFileIdentity{Digest: AbsentServiceUnitDigest}
	}
	record.Publish = &unitPublishRecord{
		Stage: publishExchangeReady, Temporary: temporary, Rescue: rescue,
		Expected: expectedIdentity, Desired: desiredIdentity, WasAbsent: !expected.exists,
	}
	if err := writeInstallRecord(directoryFD, *record); err != nil {
		return err
	}
	journaled = true
	if service.hooks.beforeCommit != nil {
		service.hooks.beforeCommit()
	}
	if service.hooks.beforeConditionalPublish != nil {
		service.hooks.beforeConditionalPublish()
	}
	if !expected.exists {
		if err := unix.Renameat2(directoryFD, temporary, directoryFD, gatewayServiceUnit, unix.RENAME_NOREPLACE); err != nil {
			if errors.Is(err, unix.EEXIST) {
				return ErrServiceChangeDrift
			}
			return err
		}
	} else {
		if err := unix.Renameat2(directoryFD, temporary, directoryFD, gatewayServiceUnit, unix.RENAME_EXCHANGE); err != nil {
			if errors.Is(err, unix.ENOENT) {
				return ErrServiceChangeDrift
			}
			return err
		}
	}
	if err := service.publishFault("publish_after_exchange"); err != nil {
		return err
	}
	if err := service.validateUnitExchange(directoryFD, record); err != nil {
		return err
	}
	if service.hooks.fault != nil {
		if err := service.hooks.fault("unit_directory_sync"); err != nil {
			return err
		}
	}
	return unix.Fsync(directoryFD)
}

func (service *systemdGatewayService) recoverUnitPublish(directoryFD int, record *installRecord) error {
	if record.Publish == nil || !validInstallRecordPublish(*record) {
		return ErrServiceChangeDrift
	}
	publish := record.Publish
	if publish.Stage == publishValidated {
		target, exists, err := readServiceFileIdentityAt(directoryFD, gatewayServiceUnit)
		if err != nil {
			return err
		}
		if !exists || target != publish.Desired {
			return ErrServiceChangeDrift
		}
		return nil
	}
	if publish.Stage == publishRestoring {
		return service.resumeUnitRestore(directoryFD, record)
	}
	target, targetExists, err := readServiceFileIdentityAt(directoryFD, gatewayServiceUnit)
	if err != nil {
		return err
	}
	temporary, temporaryExists, err := readServiceFileIdentityAt(directoryFD, publish.Temporary)
	if err != nil {
		return err
	}
	if publish.WasAbsent {
		switch {
		case !targetExists && temporaryExists && temporary == publish.Desired:
			if err := unix.Renameat2(directoryFD, publish.Temporary, directoryFD, gatewayServiceUnit, unix.RENAME_NOREPLACE); err != nil {
				return ErrServiceChangeDrift
			}
			return service.validateUnitExchange(directoryFD, record)
		case targetExists && target == publish.Desired && !temporaryExists:
			publish.Stage = publishValidated
			return writeInstallRecord(directoryFD, *record)
		default:
			return ErrServiceChangeDrift
		}
	}
	switch {
	case targetExists && target == publish.Expected && temporaryExists && temporary == publish.Desired:
		if err := unix.Renameat2(directoryFD, publish.Temporary, directoryFD, gatewayServiceUnit, unix.RENAME_EXCHANGE); err != nil {
			return ErrServiceChangeDrift
		}
		return service.validateUnitExchange(directoryFD, record)
	case targetExists && target == publish.Desired && temporaryExists && temporary == publish.Expected:
		publish.Stage = publishValidated
		return writeInstallRecord(directoryFD, *record)
	case targetExists && target == publish.Desired && temporaryExists:
		return service.preserveMismatchedExchange(directoryFD, record)
	default:
		return ErrServiceChangeDrift
	}
}

func (service *systemdGatewayService) validateUnitExchange(directoryFD int, record *installRecord) error {
	if err := service.publishFault("publish_during_validation"); err != nil {
		return err
	}
	publish := record.Publish
	target, targetExists, err := readServiceFileIdentityAt(directoryFD, gatewayServiceUnit)
	if err != nil {
		return err
	}
	temporary, temporaryExists, err := readServiceFileIdentityAt(directoryFD, publish.Temporary)
	if err != nil {
		return err
	}
	valid := targetExists && target == publish.Desired
	if publish.WasAbsent {
		valid = valid && !temporaryExists
	} else {
		valid = valid && temporaryExists && temporary == publish.Expected
	}
	if valid {
		publish.Stage = publishValidated
		return writeInstallRecord(directoryFD, *record)
	}
	return service.preserveMismatchedExchange(directoryFD, record)
}

func (service *systemdGatewayService) preserveMismatchedExchange(directoryFD int, record *installRecord) error {
	if err := service.publishFault("publish_before_restore"); err != nil {
		return err
	}
	publish := record.Publish
	target, targetExists, err := readServiceFileIdentityAt(directoryFD, gatewayServiceUnit)
	if err != nil {
		return err
	}
	_, temporaryExists, err := readServiceFileIdentityAt(directoryFD, publish.Temporary)
	if err != nil {
		return err
	}
	if !targetExists || target != publish.Desired || !temporaryExists {
		return ErrServiceChangeDrift
	}
	publish.Stage = publishRestoring
	if err := writeInstallRecord(directoryFD, *record); err != nil {
		return err
	}
	if err := unix.Renameat2(directoryFD, gatewayServiceUnit, directoryFD, publish.Rescue, unix.RENAME_NOREPLACE); err != nil {
		return ErrServiceChangeDrift
	}
	if err := unix.Fsync(directoryFD); err != nil {
		return err
	}
	if err := service.publishFault("publish_after_rescue"); err != nil {
		return err
	}
	return service.resumeUnitRestore(directoryFD, record)
}

func (service *systemdGatewayService) resumeUnitRestore(directoryFD int, record *installRecord) error {
	publish := record.Publish
	_, targetExists, err := readServiceFileIdentityAt(directoryFD, gatewayServiceUnit)
	if err != nil {
		return err
	}
	_, temporaryExists, err := readServiceFileIdentityAt(directoryFD, publish.Temporary)
	if err != nil {
		return err
	}
	rescue, rescueExists, err := readServiceFileIdentityAt(directoryFD, publish.Rescue)
	if err != nil {
		return err
	}
	if targetExists {
		return ErrServiceChangeDrift
	}
	if !temporaryExists || !rescueExists || rescue != publish.Desired {
		return ErrServiceChangeDrift
	}
	if err := unix.Renameat2(directoryFD, publish.Temporary, directoryFD, gatewayServiceUnit, unix.RENAME_NOREPLACE); err != nil {
		return ErrServiceChangeDrift
	}
	if err := unix.Fsync(directoryFD); err != nil {
		return err
	}
	return ErrServiceChangeDrift
}

func (service *systemdGatewayService) publishFault(point string) error {
	if service.hooks.fault == nil {
		return nil
	}
	return service.hooks.fault(point)
}

func serviceFileIdentityFromFD(fd int, digest ServiceUnitDigest) (serviceFileIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return serviceFileIdentity{}, err
	}
	return serviceFileIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, Digest: digest}, nil
}

func readServiceFileIdentityAt(directoryFD int, name string) (serviceFileIdentity, bool, error) {
	fd, err := unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return serviceFileIdentity{}, false, nil
	}
	if err != nil {
		return serviceFileIdentity{}, false, fmt.Errorf("%w: open service evidence", ErrUnsafeServicePath)
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	if err := validateRegularFD(fd, uint32(os.Geteuid()), 0o600, false); err != nil {
		return serviceFileIdentity{}, false, err
	}
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return serviceFileIdentity{}, false, fmt.Errorf("%w: invalid service evidence", ErrUnsafeServicePath)
	}
	identity, err := serviceFileIdentityFromFD(fd, serviceUnitDigest(data))
	return identity, true, err
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
	return openSecureDirectoryWithFault(path, create, exactOwner, nil)
}

func openSecureDirectoryWithFault(path string, create, exactOwner bool, fault func(string) error) (int, error) {
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
		created := false
		if errors.Is(openErr, unix.ENOENT) && create {
			if mkdirErr := unix.Mkdirat(current, component, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				_ = unix.Close(current)
				return -1, mkdirErr
			}
			created = true
			next, openErr = unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if openErr != nil {
			_ = unix.Close(current)
			if errors.Is(openErr, unix.ENOENT) {
				return -1, os.ErrNotExist
			}
			return -1, fmt.Errorf("%w: open directory component %s", ErrUnsafeServicePath, component)
		}
		if create {
			if created && fault != nil {
				if err := fault("directory_parent_sync"); err != nil {
					_ = unix.Close(next)
					_ = unix.Close(current)
					return -1, err
				}
			}
			if err := unix.Fsync(current); err != nil {
				_ = unix.Close(next)
				_ = unix.Close(current)
				return -1, err
			}
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
	fd, err := openSecureDirectoryWithFault(filepath.Dir(paths.socketPath), true, true, service.hooks.fault)
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
