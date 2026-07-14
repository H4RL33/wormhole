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
	"github.com/H4RL33/wormhole/internal/runtime/eventbus"
	"github.com/H4RL33/wormhole/internal/runtime/localapi"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/runtime/scheduler"
	"github.com/H4RL33/wormhole/internal/runtime/sync"
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

	// Initialize sync engine repositories and engine (RFC-0003 §8.2).
	queueRepo := sync.NewQueueRepo(store.DB())
	auditRepo := sync.NewAuditRepo(store.DB())
	syncCfg := sync.DefaultConfig()
	syncEngine := sync.New(cfg.Credentials.Server, cfg.Credentials.Token, cfg.Credentials.ProjectID, queueRepo, auditRepo, syncCfg)

	tr := localstore.NewTaskRepo(store.DB())
	er := localstore.NewEventRepo(store.DB())
	kb := localstore.NewKBRepo(store.DB())

	// P3: eventbus + scheduler are always constructed so agent registration,
	// presence, task routing, and subscriptions (wormhole.agent.register,
	// wormhole.task.route, etc.) work regardless of single- or multi-org mode.
	eb := eventbus.NewEventBus()
	sched := scheduler.NewScheduler()

	// P5: prefer multi-org wiring when more than one credential profile is
	// present under ~/.wormhole/credentials/ — a single profile stays on the
	// single-org path so existing single-profile deployments (and cfg's
	// already-resolved Credentials) are unaffected.
	var srv *localapi.Server
	if multiCfg, mErr := config.LoadMultiOrg(); mErr == nil && len(multiCfg.Orgs) > 1 {
		srv, err = localapi.NewMultiOrg(cfg.SocketPath, multiCfg.Orgs, multiCfg.Bindings, store, tr, er, kb, eb, sched, queueRepo)
	} else {
		srv, err = localapi.NewWithRuntime(cfg.SocketPath, cfg.Credentials.Server, cfg.Credentials.Token, cfg.Credentials.ProjectID, store, tr, er, kb, eb, sched, queueRepo)
	}
	if err != nil {
		return fmt.Errorf("wormholed: start local api: %w", err)
	}
	defer srv.Close()

	// Start the background sync loop (RFC-0003 §8.2: incremental push/pull cycle).
	syncEngine.Start(ctx)
	defer syncEngine.Stop()

	return srv.Serve(ctx)
}
