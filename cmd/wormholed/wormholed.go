// Run wires config, localstore, and localapi into one running daemon
// instance, and blocks until ctx is cancelled (RFC-0003 §6.1). Split from
// main() so it's directly testable without touching os.Args/os.Exit or
// OS signals.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	stdsync "sync"
	"syscall"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/eventbus"
	"github.com/H4RL33/wormhole/internal/runtime/localapi"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/runtime/scheduler"
	"github.com/H4RL33/wormhole/internal/runtime/sync"
	"golang.org/x/sys/unix"
)

type syncEngine interface {
	Bootstrap(context.Context) error
	Start(context.Context)
	Stop()
}

type syncEngineFactory func(string, string, string, *sync.QueueRepo, *sync.AuditRepo, *localstore.TaskRepo, *localstore.KBRepo, sync.Config) (syncEngine, error)

func defaultSyncEngineFactory(server, token, projectID string, queueRepo *sync.QueueRepo, auditRepo *sync.AuditRepo, taskRepo *localstore.TaskRepo, kbRepo *localstore.KBRepo, cfg sync.Config) (syncEngine, error) {
	return sync.New(server, token, projectID, queueRepo, auditRepo, taskRepo, kbRepo, cfg)
}

var errSyncGroupStopped = errors.New("sync group: stopped")

// syncGroup owns the lifecycle of every per-binding sync engine in this
// wormholed process (RFC-0003 §7.1, §8.1, §8.2).
type syncGroup struct {
	engines            []syncEngine
	startOnce          stdsync.Once
	stopOnce           stdsync.Once
	mu                 stdsync.Mutex
	stopped            bool
	cancel             context.CancelFunc
	startErr           error
	testAfterBootstrap func()
}

func (g *syncGroup) Start(ctx context.Context) error {
	g.startOnce.Do(func() {
		g.startErr = g.start(ctx)
	})
	g.mu.Lock()
	stopped := g.stopped
	g.mu.Unlock()
	if stopped {
		return errSyncGroupStopped
	}
	return g.startErr
}

func (g *syncGroup) start(ctx context.Context) error {
	groupCtx, cancel := context.WithCancel(ctx)
	g.mu.Lock()
	if g.stopped {
		g.mu.Unlock()
		cancel()
		return errSyncGroupStopped
	}
	g.cancel = cancel
	g.mu.Unlock()

	bootstrapFailed := true
	defer func() {
		if bootstrapFailed {
			cancel()
			g.mu.Lock()
			g.cancel = nil
			g.mu.Unlock()
		}
	}()
	for i, engine := range g.engines {
		if err := groupCtx.Err(); err != nil {
			return fmt.Errorf("sync group: bootstrap canceled before engine %d: %w", i, err)
		}
		if err := engine.Bootstrap(groupCtx); err != nil {
			return fmt.Errorf("sync group: bootstrap engine %d: %w", i, err)
		}
	}
	if err := groupCtx.Err(); err != nil {
		return fmt.Errorf("sync group: bootstrap canceled: %w", err)
	}
	if g.testAfterBootstrap != nil {
		g.testAfterBootstrap()
	}

	// This lock is the bootstrap-to-start barrier. Stop either marks the
	// group terminal before this point (so no engine starts), or waits until
	// every authorized Start call returns before canceling/stopping them.
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopped {
		return errSyncGroupStopped
	}
	if err := groupCtx.Err(); err != nil {
		return fmt.Errorf("sync group: start canceled: %w", err)
	}
	for _, engine := range g.engines {
		engine.Start(groupCtx)
	}
	bootstrapFailed = false
	return nil
}

func (g *syncGroup) Stop() {
	g.stopOnce.Do(func() {
		g.mu.Lock()
		g.stopped = true
		cancel := g.cancel
		g.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		for i := len(g.engines) - 1; i >= 0; i-- {
			g.engines[i].Stop()
		}
	})
}

type syncBindingKey struct {
	server    string
	projectID string
	token     string
}

func newMultiOrgSyncGroup(orgs map[string]config.Org, bindings []config.ProjectBinding, queueRepo *sync.QueueRepo, auditRepo *sync.AuditRepo, taskRepo *localstore.TaskRepo, kbRepo *localstore.KBRepo, syncCfg sync.Config, factory syncEngineFactory) (*syncGroup, error) {
	group := &syncGroup{}
	projectBindings := make(map[string]syncBindingKey, len(bindings))
	engines := make(map[syncBindingKey]struct{}, len(bindings))
	for _, binding := range bindings {
		org, ok := orgs[binding.OrgName]
		if !ok {
			return nil, fmt.Errorf("wormholed: org %q for project binding %q not found", binding.OrgName, binding.ProjectID)
		}
		key := syncBindingKey{
			server: org.Credentials.Server, projectID: binding.ProjectID, token: org.Credentials.Token,
		}
		if existing, ok := projectBindings[binding.ProjectID]; ok && existing != key {
			return nil, fmt.Errorf("wormholed: conflicting project bindings for %q", binding.ProjectID)
		}
		projectBindings[binding.ProjectID] = key
		if _, ok := engines[key]; ok {
			continue
		}
		engine, err := factory(key.server, key.token, key.projectID, queueRepo, auditRepo, taskRepo, kbRepo, syncCfg)
		if err != nil {
			return nil, fmt.Errorf("wormholed: configure sync engine for project %q: %w", binding.ProjectID, err)
		}
		group.engines = append(group.engines, engine)
		engines[key] = struct{}{}
	}
	return group, nil
}

func Run(ctx context.Context, profileName string) error {
	if err := ensureSupportedPlatform(); err != nil {
		return err
	}
	return runWithSyncEngineFactory(ctx, profileName, defaultSyncEngineFactory)
}

type staleSocketRemovalHooks struct {
	beforeQuarantine func()
	afterQuarantine  func(string)
}

func removeStaleSocket(socketPath string) error {
	return removeStaleSocketWithHooks(socketPath, staleSocketRemovalHooks{})
}

func removeStaleSocketWithHook(socketPath string, beforeQuarantine func()) error {
	return removeStaleSocketWithHooks(socketPath, staleSocketRemovalHooks{beforeQuarantine: beforeQuarantine})
}

func removeStaleSocketWithHooks(socketPath string, hooks staleSocketRemovalHooks) error {
	info, err := os.Lstat(socketPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("wormholed: inspect stale socket path: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("wormholed: stale socket path %s is not a socket", socketPath)
	}
	fd, err := unix.Open(socketPath, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("wormholed: open stale socket path: %w", err)
	}
	defer unix.Close(fd)

	var expected unix.Stat_t
	if err := unix.Fstat(fd, &expected); err != nil {
		return fmt.Errorf("wormholed: stat stale socket descriptor: %w", err)
	}
	conn, dialErr := net.DialTimeout("unix", socketPath, 250*time.Millisecond)
	if dialErr == nil {
		_ = conn.Close()
		return fmt.Errorf("wormholed: active daemon is already listening on %s", socketPath)
	}
	if !errors.Is(dialErr, syscall.ECONNREFUSED) {
		return fmt.Errorf("wormholed: cannot prove socket %s is stale: %w", socketPath, dialErr)
	}
	return quarantineAndRemoveSocket(socketPath, expected.Dev, expected.Ino, hooks)
}

func runWithSyncEngineFactory(ctx context.Context, profileName string, factory syncEngineFactory) error {
	cfg, err := config.Load(profileName)
	if err != nil {
		return fmt.Errorf("wormholed: load config: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(cfg.SocketPath), 0o700); err != nil {
		return fmt.Errorf("wormholed: create socket directory: %w", err)
	}
	// A stale Unix socket from an unclean shutdown is replaceable. Every
	// other file type is rejected and preserved: this path may contain user
	// data, and Lstat deliberately does not follow symlinks.
	if err := removeStaleSocket(cfg.SocketPath); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o700); err != nil {
		return fmt.Errorf("wormholed: create data directory: %w", err)
	}

	store, err := localstore.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("wormholed: open local store: %w", err)
	}
	defer store.Close()

	er := localstore.NewEventRepo(store.DB())
	tr := localstore.NewTaskRepo(store.DB(), er)
	kb := localstore.NewKBRepo(store.DB())

	// Initialize sync repositories shared by the per-binding engines. Queue
	// operations remain namespace-scoped inside QueueRepo.
	queueRepo := sync.NewQueueRepo(store.DB())
	auditRepo := sync.NewAuditRepo(store.DB())
	syncCfg := sync.DefaultConfig()

	// P5: prefer multi-org wiring when more than one credential profile is
	// present. Single-profile deployments retain the resolved Load(profile)
	// credentials and exactly one engine.
	multiCfg, multiErr := config.LoadMultiOrg()
	useMultiOrg := multiErr == nil && len(multiCfg.Orgs) > 1
	var syncEngines *syncGroup
	if useMultiOrg {
		syncEngines, err = newMultiOrgSyncGroup(multiCfg.Orgs, multiCfg.Bindings, queueRepo, auditRepo, tr, kb, syncCfg, factory)
	} else {
		engine, engineErr := factory(cfg.Credentials.Server, cfg.Credentials.Token, cfg.Credentials.ProjectID, queueRepo, auditRepo, tr, kb, syncCfg)
		if engineErr != nil {
			return fmt.Errorf("wormholed: configure sync engine: %w", engineErr)
		}
		syncEngines = &syncGroup{engines: []syncEngine{engine}}
	}
	if err != nil {
		return err
	}
	if err := syncEngines.Start(ctx); err != nil {
		return fmt.Errorf("wormholed: start sync engines: %w", err)
	}
	defer syncEngines.Stop()

	// P3: eventbus + scheduler are always constructed so agent registration,
	// presence, task routing, and subscriptions (wormhole.agent.register,
	// wormhole.task.route, etc.) work regardless of single- or multi-org mode.
	eb := eventbus.NewEventBus()
	sched := scheduler.NewScheduler()

	var srv *localapi.Server
	if useMultiOrg {
		srv, err = localapi.NewMultiOrg(cfg.SocketPath, multiCfg.Orgs, multiCfg.Bindings, store, tr, er, kb, eb, sched, queueRepo)
	} else {
		srv, err = localapi.NewWithRuntime(cfg.SocketPath, cfg.Credentials.Server, cfg.Credentials.Token, cfg.Credentials.ProjectID, store, tr, er, kb, eb, sched, queueRepo)
	}
	if err != nil {
		return fmt.Errorf("wormholed: start local api: %w", err)
	}
	defer srv.Close()
	if useMultiOrg {
		for _, binding := range multiCfg.Bindings {
			if org, ok := multiCfg.Orgs[binding.OrgName]; ok {
				srv.SetAuthorizationAgent(binding.ProjectID, org.Credentials.AgentID)
			}
		}
	} else {
		srv.SetAuthorizationAgent(cfg.Credentials.ProjectID, cfg.Credentials.AgentID)
	}
	// Sync bootstrap proves the coordination endpoints are reachable. Refresh
	// per-project permissions now so durable local writes remain authorized
	// when the daemon later operates offline. A failed refresh leaves that
	// project fail-closed at tools/call rather than preventing read-only use.
	_ = srv.WarmAuthorizationScopes(ctx)

	return srv.Serve(ctx)
}
