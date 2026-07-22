package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDaemonMainHelperProcess(t *testing.T) {
	switch os.Getenv("WORMHOLED_MAIN_HELPER") {
	case "":
		return
	case "signal":
		runDaemonMain = func(ctx context.Context, _ string) error {
			fmt.Fprintln(os.Stdout, "ready")
			<-ctx.Done()
			return nil
		}
		main()
		return
	}
	runDaemonMain = func(context.Context, string) error {
		return errors.New("injected daemon startup failure")
	}
	main()
	t.Fatal("main returned without exiting")
}

func TestDaemonMainExitsCleanlyOnSIGTERM(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestDaemonMainHelperProcess$")
	command.Env = append(os.Environ(), "WORMHOLED_MAIN_HELPER=signal")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("daemon stdout pipe: %v", err)
	}
	if err := command.Start(); err != nil {
		t.Fatalf("start daemon helper: %v", err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})

	ready := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		ready <- line
	}()
	select {
	case line := <-ready:
		if line != "ready\n" {
			t.Fatalf("daemon helper readiness = %q, want ready", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon helper did not reach signal boundary")
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal daemon helper: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("daemon helper SIGTERM exit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon helper did not exit after SIGTERM")
	}
}

func TestDaemonMainExitsOneWhenStartupFails(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestDaemonMainHelperProcess$")
	command.Env = append(os.Environ(), "WORMHOLED_MAIN_HELPER=1")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("daemon main exited successfully, want status 1")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("daemon main error = %v, want exit status 1", err)
	}
	if !strings.Contains(string(output), "wormholed: injected daemon startup failure") {
		t.Fatalf("daemon main output = %q, want startup error", output)
	}
}

func TestRunMainSelectsProfileAndReportsFailures(t *testing.T) {
	for _, tt := range []struct {
		name        string
		args        []string
		runErr      error
		wantProfile string
	}{
		{name: "default profile", wantProfile: "default"},
		{name: "requested profile", args: []string{"work"}, wantProfile: "work"},
		{name: "failure", runErr: errors.New("bad config"), wantProfile: "default"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			err := runMain(context.Background(), tt.args, &stderr, func(_ context.Context, profile string) error {
				if profile != tt.wantProfile {
					t.Fatalf("profile = %q, want %q", profile, tt.wantProfile)
				}
				return tt.runErr
			})
			if !errors.Is(err, tt.runErr) {
				t.Fatalf("runMain error = %v, want %v", err, tt.runErr)
			}
			if tt.runErr != nil && !bytes.Contains(stderr.Bytes(), []byte("wormholed: bad config")) {
				t.Fatalf("stderr = %q, want daemon error", stderr.String())
			}
		})
	}
}
