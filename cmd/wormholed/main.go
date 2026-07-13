package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	profile := "default"
	if len(os.Args) > 1 {
		profile = os.Args[1]
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := Run(ctx, profile); err != nil {
		fmt.Fprintf(os.Stderr, "wormholed: %v\n", err)
		os.Exit(1)
	}
}
