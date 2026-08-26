package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

const commandOutputLimit = 1 << 20

// ErrCommandOutputLimit reports that stdout or stderr exceeded its independent
// one MiB capture bound. The child is still drained and reaped before return.
var ErrCommandOutputLimit = errors.New("config: command output limit exceeded")

// ErrCommandWaitLimit reports that a descendant retained the command's pipes
// after the process leader exited. The runner closes the pipes and terminates
// the remaining process group before returning.
var ErrCommandWaitLimit = errors.New("config: command wait limit exceeded")

// CommandExitError reports an ordinary non-zero process exit without embedding
// command arguments or captured output in the error.
type CommandExitError struct {
	ExitCode int
}

func (err *CommandExitError) Error() string {
	return fmt.Sprintf("config: command exited with status %d", err.ExitCode)
}

// CommandRunner is the sole no-shell subprocess seam shared by setup-owned
// service and connector operations.
type CommandRunner interface {
	Run(context.Context, string, ...string) (stdout []byte, stderr []byte, err error)
}

type execCommandRunner struct{}

// NewCommandRunner returns a bounded no-shell runner. It inherits the current
// process environment and never accepts a working directory, stdin, or shell
// command string from callers.
func NewCommandRunner() CommandRunner {
	return execCommandRunner{}
}

func (execCommandRunner) Run(ctx context.Context, executable string, args ...string) ([]byte, []byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	command := exec.Command(executable, args...)
	prepareCommandProcessGroup(command)
	command.WaitDelay = 250 * time.Millisecond
	stdout := &boundedCommandBuffer{limit: commandOutputLimit}
	stderr := &boundedCommandBuffer{limit: commandOutputLimit}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return nil, nil, fmt.Errorf("config: start command: %w", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()

	var waitErr error
	select {
	case waitErr = <-waited:
	case <-ctx.Done():
		_ = terminateCommandProcessGroup(command)
		<-waited
		return stdout.Bytes(), stderr.Bytes(), fmt.Errorf("config: command canceled: %w", ctx.Err())
	}
	if stdout.truncated || stderr.truncated {
		return stdout.Bytes(), stderr.Bytes(), ErrCommandOutputLimit
	}
	if errors.Is(waitErr, exec.ErrWaitDelay) {
		_ = terminateCommandProcessGroup(command)
		return stdout.Bytes(), stderr.Bytes(), ErrCommandWaitLimit
	}
	if waitErr == nil {
		return stdout.Bytes(), stderr.Bytes(), nil
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		return stdout.Bytes(), stderr.Bytes(), &CommandExitError{ExitCode: exitErr.ExitCode()}
	}
	return stdout.Bytes(), stderr.Bytes(), fmt.Errorf("config: wait for command: %w", waitErr)
}

type boundedCommandBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (buffer *boundedCommandBuffer) Write(data []byte) (int, error) {
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining > len(data) {
		remaining = len(data)
	}
	if remaining > 0 {
		_, _ = buffer.buffer.Write(data[:remaining])
	}
	if remaining < len(data) {
		buffer.truncated = true
	}
	// Report the full write so os/exec continues draining both pipes instead of
	// treating the intentional discard as io.ErrShortWrite.
	return len(data), nil
}

func (buffer *boundedCommandBuffer) Bytes() []byte {
	return append([]byte(nil), buffer.buffer.Bytes()...)
}
