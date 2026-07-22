package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

var runDaemonMain = Run

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := runMain(ctx, os.Args[1:], os.Stderr, runDaemonMain); err != nil {
		os.Exit(1)
	}
}

// runMain adapts command-line arguments to the daemon runtime. Keeping the
// signal and os.Exit boundary in main leaves profile selection and failures
// directly testable.
func runMain(ctx context.Context, args []string, stderr io.Writer, run func(context.Context, string) error) error {
	profile := "default"
	if len(args) > 0 {
		profile = args[0]
	}
	if err := run(ctx, profile); err != nil {
		fmt.Fprintf(stderr, "wormholed: %v\n", err)
		return err
	}
	return nil
}
