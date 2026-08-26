package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const commandRunnerHelper = "WORMHOLE_COMMAND_RUNNER_HELPER"

func TestCommandRunnerHelper(t *testing.T) {
	switch os.Getenv(commandRunnerHelper) {
	case "output":
		chunk := bytes.Repeat([]byte("x"), 64*1024)
		for range 20 {
			_, _ = os.Stdout.Write(chunk)
			_, _ = os.Stderr.Write(chunk)
		}
		os.Exit(0)
	case "child":
		for {
			time.Sleep(time.Second)
		}
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestCommandRunnerHelper$")
		child.Env = append(os.Environ(), commandRunnerHelper+"=child")
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Fprintln(os.Stdout, child.Process.Pid)
		_ = os.Stdout.Sync()
		for {
			time.Sleep(time.Second)
		}
	case "orphan":
		child := exec.Command(os.Args[0], "-test.run=^TestCommandRunnerHelper$")
		child.Env = append(os.Environ(), commandRunnerHelper+"=child")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stdout, child.Process.Pid)
		_ = os.Stdout.Sync()
		os.Exit(0)
	case "exit":
		_, _ = os.Stdout.WriteString("out")
		_, _ = os.Stderr.WriteString("err")
		os.Exit(7)
	}
}

func TestCommandRunnerUsesLiteralArgvWithoutShell(t *testing.T) {
	runner := NewCommandRunner()
	marker := filepath.Join(t.TempDir(), "shell-ran")
	literal := "$(touch " + marker + "); still-one-argument"
	stdout, stderr, err := runner.Run(t.Context(), "/bin/echo", literal)
	if err != nil {
		t.Fatalf("Run: %v (stderr=%q)", err, stderr)
	}
	if got, want := string(stdout), literal+"\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shell text executed: %v", err)
	}
}

func TestCommandRunnerBoundsStdoutAndStderrWithoutDeadlock(t *testing.T) {
	runner := NewCommandRunner()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	t.Setenv(commandRunnerHelper, "output")
	stdout, stderr, err := runner.Run(ctx, os.Args[0], "-test.run=^TestCommandRunnerHelper$")
	if !errors.Is(err, ErrCommandOutputLimit) {
		t.Fatalf("Run error = %v, want ErrCommandOutputLimit", err)
	}
	if len(stdout) != commandOutputLimit || len(stderr) != commandOutputLimit {
		t.Fatalf("bounded lengths = (%d,%d), want (%d,%d)", len(stdout), len(stderr), commandOutputLimit, commandOutputLimit)
	}
}

func TestCommandRunnerExitErrorRetainsBoundedStreams(t *testing.T) {
	runner := NewCommandRunner()
	t.Setenv(commandRunnerHelper, "exit")
	stdout, stderr, err := runner.Run(t.Context(), os.Args[0], "-test.run=^TestCommandRunnerHelper$")
	var exitErr *CommandExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode != 7 {
		t.Fatalf("Run error = %#v, want exit code 7", err)
	}
	if string(stdout) != "out" || string(stderr) != "err" {
		t.Fatalf("streams = %q/%q", stdout, stderr)
	}
	if strings.Contains(err.Error(), "out") || strings.Contains(err.Error(), "err") {
		t.Fatalf("exit error disclosed command output: %q", err)
	}
}
