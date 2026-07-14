// Run wires config, localstore, and localapi into one running daemon
// instance, and blocks until ctx is cancelled (RFC-0003 §6.1). Split from
// main() so it's directly testable without touching os.Args/os.Exit or
// OS signals.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/localapi"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
)

func Run(ctx context.Context, profileName string) error {
	cfg, err := config.Load(profileName)
	if err != nil {
		return fmt.Errorf("wormholed: load config: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(cfg.SocketPath), 0o700); err != nil {
		return fmt.Errorf("wormholed: create socket directory: %w", err)
	}
	// A stale socket file from a previous unclean shutdown would make
	// net.Listen fail with "address already in use"; remove it first.
	_ = os.Remove(cfg.SocketPath)

	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o700); err != nil {
		return fmt.Errorf("wormholed: create data directory: %w", err)
	}

	store, err := localstore.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("wormholed: open local store: %w", err)
	}
	defer store.Close()

	srv, err := localapi.New(cfg.SocketPath, cfg.Credentials.Server, cfg.Credentials.Token, cfg.Credentials.ProjectID, store, localstore.NewTaskRepo(store.DB()), localstore.NewEventRepo(store.DB()), localstore.NewKBRepo(store.DB()))
	if err != nil {
		return fmt.Errorf("wormholed: start local api: %w", err)
	}
	defer srv.Close()

	return srv.Serve(ctx)
}
