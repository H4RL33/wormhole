package config

import (
	"context"
	"errors"
	"testing"
)

func TestGatewayServiceUnsupportedFailsBeforeMutation(t *testing.T) {
	runner := &recordingServiceRunner{}
	service := newUnavailableGatewayService(runner)
	state, err := service.Inspect(t.Context())
	if state != (ServiceState{}) {
		t.Fatalf("state = %+v", state)
	}
	if !errors.Is(err, ErrServiceManagerUnavailable) || err.Error() != ErrServiceManagerUnavailable.Error() {
		t.Fatalf("Inspect error = %v", err)
	}
	if err := service.Install(t.Context(), ConfirmedServiceChange{}); !errors.Is(err, ErrServiceManagerUnavailable) {
		t.Fatalf("Install error = %v", err)
	}
	if err := service.Start(t.Context()); !errors.Is(err, ErrServiceManagerUnavailable) {
		t.Fatalf("Start error = %v", err)
	}
	if err := service.WaitReady(t.Context()); !errors.Is(err, ErrServiceManagerUnavailable) {
		t.Fatalf("WaitReady error = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("unsupported service mutated or executed: %+v", runner.calls)
	}
}

type serviceCall struct {
	executable string
	args       []string
}

type recordingServiceRunner struct {
	calls            []serviceCall
	enabled          bool
	active           bool
	unusable         bool
	enabledOutput    string
	unitPath         string
	loaded           bool
	needReload       bool
	failDaemonReload int
	failEnable       int
	failStart        int
	enableNoEffect   bool
	forcedError      error
}

func (runner *recordingServiceRunner) Run(_ context.Context, executable string, args ...string) ([]byte, []byte, error) {
	runner.calls = append(runner.calls, serviceCall{executable: executable, args: append([]string(nil), args...)})
	if runner.forcedError != nil {
		return nil, nil, runner.forcedError
	}
	if runner.unusable {
		return nil, nil, errors.New("not usable")
	}
	if len(args) < 2 || args[0] != "--user" {
		return nil, nil, errors.New("unexpected command")
	}
	switch args[1] {
	case "show-environment":
		return nil, nil, nil
	case "daemon-reload":
		if runner.failDaemonReload > 0 {
			runner.failDaemonReload--
			return nil, nil, errors.New("injected daemon-reload failure")
		}
		runner.loaded, runner.needReload = true, false
		return nil, nil, nil
	case "is-enabled":
		if runner.enabledOutput != "" {
			return []byte(runner.enabledOutput), nil, nil
		}
		if runner.enabled {
			return []byte("enabled\n"), nil, nil
		}
		return []byte("disabled\n"), nil, &CommandExitError{ExitCode: 1}
	case "is-active":
		if runner.active {
			return []byte("active\n"), nil, nil
		}
		return []byte("inactive\n"), nil, &CommandExitError{ExitCode: 3}
	case "show":
		fragment := ""
		if runner.loaded {
			fragment = runner.unitPath
		}
		needReload := "no"
		if runner.needReload || !runner.loaded {
			needReload = "yes"
		}
		return []byte("FragmentPath=" + fragment + "\nNeedDaemonReload=" + needReload + "\n"), nil, nil
	case "enable":
		if runner.failEnable > 0 {
			runner.failEnable--
			return nil, nil, errors.New("injected enable failure")
		}
		if !runner.enableNoEffect {
			runner.enabled, runner.active = true, true
		}
		return nil, nil, nil
	case "start":
		if runner.failStart > 0 {
			runner.failStart--
			return nil, nil, errors.New("injected start failure")
		}
		runner.active = true
		return nil, nil, nil
	default:
		return nil, nil, errors.New("unexpected systemctl argv")
	}
}
