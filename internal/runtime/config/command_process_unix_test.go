//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package config

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCommandRunnerCancellationTerminatesProcessGroup(t *testing.T) {
	runner := NewCommandRunner()
	t.Setenv(commandRunnerHelper, "parent")
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	stdout, _, err := runner.Run(ctx, os.Args[0], "-test.run=^TestCommandRunnerHelper$")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run error = %v, want context deadline", err)
	}
	assertProcessGone(t, stdout)
}

func TestCommandRunnerBoundsInheritedPipeWaitAndCleansProcessGroup(t *testing.T) {
	runner := NewCommandRunner()
	t.Setenv(commandRunnerHelper, "orphan")
	started := time.Now()
	stdout, _, err := runner.Run(t.Context(), os.Args[0], "-test.run=^TestCommandRunnerHelper$")
	if !errors.Is(err, ErrCommandWaitLimit) {
		t.Fatalf("Run error = %v, want ErrCommandWaitLimit", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("inherited pipe wait was not bounded: %v", elapsed)
	}
	assertProcessGone(t, stdout)
}

func assertProcessGone(t *testing.T, output []byte) {
	t.Helper()
	childPID, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		t.Fatalf("parse child pid from %q: %v", output, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(childPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(childPID, syscall.SIGKILL)
			t.Fatalf("descendant process %d survived cleanup: %v", childPID, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
