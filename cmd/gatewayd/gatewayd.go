// Run wires config, localstore, and localapi into one running daemon
// instance, and blocks until ctx is cancelled (RFC-0003 §6.1). Split from
// main() so it's directly testable without touching os.Args/os.Exit or
// OS signals.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	stdsync "sync"
	"syscall"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/eventbus"
	"github.com/H4RL33/wormhole/internal/runtime/localapi"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/runtime/scheduler"
	"github.com/H4RL33/wormhole/internal/runtime/sync"
)

type syncEngine interface {
	Bootstrap(context.Context) error
	Start(context.Context)
	Stop()
}

type syncStatusEngine interface {
	Status(context.Context) (sync.Status, error)
}

type bootstrapConfigurableSyncEngine interface {
	ConfigureBootstrap(*localstore.Store, string, string, *localstore.EnrolmentAttemptRecord) error
}

type integrationManifestConfigurableSyncEngine interface {
	ConfigureIntegrationManifestReceiver(sync.IntegrationManifestReceiver)
}

type eventAndGitConfigurableSyncEngine interface {
	ConfigureEventAndGitReplicas(*localstore.EventRepo, *localstore.GitRepo)
}

func wireEventAndGitReplicas(group *syncGroup, events *localstore.EventRepo, gitLinks *localstore.GitRepo) {
	if group == nil || events == nil || gitLinks == nil {
		return
	}
	for _, engine := range group.engines {
		if configurable, ok := engine.(eventAndGitConfigurableSyncEngine); ok {
			configurable.ConfigureEventAndGitReplicas(events, gitLinks)
		}
	}
}

func wireIntegrationManifestReceivers(group *syncGroup, receiver sync.IntegrationManifestReceiver) {
	if group == nil || receiver == nil {
		return
	}
	for _, engine := range group.engines {
		if configurable, ok := engine.(integrationManifestConfigurableSyncEngine); ok {
			configurable.ConfigureIntegrationManifestReceiver(receiver)
		}
	}
}

type syncEngineFactory func(string, string, string, *sync.QueueRepo, *sync.AuditRepo, *localstore.TaskRepo, *localstore.KBRepo, sync.Config) (syncEngine, error)

func defaultSyncEngineFactory(server, token, projectID string, queueRepo *sync.QueueRepo, auditRepo *sync.AuditRepo, taskRepo *localstore.TaskRepo, kbRepo *localstore.KBRepo, cfg sync.Config) (syncEngine, error) {
	return sync.New(server, token, projectID, queueRepo, auditRepo, taskRepo, kbRepo, cfg)
}

var errSyncGroupStopped = errors.New("sync group: stopped")

// syncGroup owns the lifecycle of every per-binding sync engine in this
// Gateway process (RFC-0003 §7.1, §8.1, §8.2).
type syncGroup struct {
	engines         []syncEngine
	readyEngines    []syncEngine
	projects        map[string]syncEngine
	notReady        map[string]bool
	startOnce       stdsync.Once
	stopOnce        stdsync.Once
	mu              stdsync.Mutex
	stopped         bool
	cancel          context.CancelFunc
	startErr        error
	testBeforeStart func()
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

	// Bootstrap is an enrolment transition, not a daemon restart step. A
	// ready Gateway starts incremental sync from its durable SQLite replica.
	if g.testBeforeStart != nil {
		g.testBeforeStart()
	}

	// This lock is the lifecycle start barrier. Stop either marks the
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
	engines := g.readyEngines
	if engines == nil {
		engines = g.engines
	}
	for _, engine := range engines {
		engine.Start(groupCtx)
	}
	return nil
}

// Status implements localapi.SyncStatusProvider without contacting Fabric.
func (g *syncGroup) Status(ctx context.Context, projectID string) (sync.Status, error) {
	engine, ok := g.projects[projectID]
	if !ok {
		return sync.Status{}, fmt.Errorf("sync group: project %q is not configured", projectID)
	}
	statusEngine, ok := engine.(syncStatusEngine)
	if !ok {
		return sync.Status{}, fmt.Errorf("sync group: project %q status unavailable", projectID)
	}
	status, err := statusEngine.Status(ctx)
	if err != nil {
		return sync.Status{}, err
	}
	if g.notReady[projectID] {
		status.State = sync.StateAttentionRequired
	}
	return status, nil
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

func serveWithSync(ctx context.Context, srv *localapi.Server, engines *syncGroup) error {
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx) }()
	select {
	case <-srv.Serving():
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		return <-serveErr
	}
	if err := engines.Start(ctx); err != nil {
		_ = srv.Close()
		<-serveErr
		return fmt.Errorf("start sync engines: %w", err)
	}
	defer engines.Stop()
	return <-serveErr
}

type syncBindingKey struct {
	server    string
	projectID string
	token     string
}

func newMultiOrgSyncGroup(orgs map[string]config.Org, bindings []config.ProjectBinding, store *localstore.Store, queueRepo *sync.QueueRepo, auditRepo *sync.AuditRepo, taskRepo *localstore.TaskRepo, kbRepo *localstore.KBRepo, syncCfg sync.Config, factory syncEngineFactory) (*syncGroup, error) {
	group := &syncGroup{readyEngines: []syncEngine{}, projects: make(map[string]syncEngine), notReady: make(map[string]bool)}
	projectBindings := make(map[string]syncBindingKey, len(bindings))
	engines := make(map[syncBindingKey]struct{}, len(bindings))
	for _, binding := range bindings {
		org, ok := orgs[binding.OrgName]
		if !ok {
			return nil, fmt.Errorf("org %q for project binding %q not found", binding.OrgName, binding.ProjectID)
		}
		key := syncBindingKey{
			server: org.Credentials.Server, projectID: binding.ProjectID, token: org.Credentials.Token,
		}
		if existing, ok := projectBindings[binding.ProjectID]; ok && existing != key {
			return nil, fmt.Errorf("conflicting project bindings for %q", binding.ProjectID)
		}
		projectBindings[binding.ProjectID] = key
		if _, ok := engines[key]; ok {
			group.projects[binding.ProjectID] = group.projects[key.projectID]
			continue
		}
		engine, err := factory(key.server, key.token, key.projectID, queueRepo, auditRepo, taskRepo, kbRepo, syncCfg)
		if err != nil {
			return nil, fmt.Errorf("configure sync engine for project %q: %w", binding.ProjectID, err)
		}
		if configurable, ok := engine.(bootstrapConfigurableSyncEngine); ok {
			if err := configurable.ConfigureBootstrap(store, org.Credentials.AgentID, org.Credentials.PassportID, nil); err != nil {
				return nil, fmt.Errorf("configure bootstrap identity for project %q: %w", binding.ProjectID, err)
			}
		}
		group.engines = append(group.engines, engine)
		group.projects[binding.ProjectID] = engine
		if err := store.ValidateReadyCheckpoint(context.Background(), binding.ProjectID, org.Credentials.AgentID, org.Credentials.PassportID, binding.OrgName); err != nil {
			group.notReady[binding.ProjectID] = true
		} else {
			group.readyEngines = append(group.readyEngines, engine)
		}
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
	afterInitialInspection func()
	beforeQuarantine       func()
	afterQuarantine        func(string)
}

type staleSocketIdentity struct {
	dev   uint64
	ino   uint64
	close func()
}

func removeStaleSocket(socketPath string) error {
	return removeStaleSocketWithHooks(socketPath, staleSocketRemovalHooks{})
}

func removeStaleSocketWithHook(socketPath string, beforeQuarantine func()) error {
	return removeStaleSocketWithHooks(socketPath, staleSocketRemovalHooks{beforeQuarantine: beforeQuarantine})
}

func removeStaleSocketWithHooks(socketPath string, hooks staleSocketRemovalHooks) error {
	expected, err := openStaleSocketIdentity(socketPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer expected.close()

	if hooks.afterInitialInspection != nil {
		hooks.afterInitialInspection()
	}
	conn, dialErr := net.DialTimeout("unix", socketPath, 250*time.Millisecond)
	if dialErr == nil {
		_ = conn.Close()
		return fmt.Errorf("active daemon is already listening on %s", socketPath)
	}
	if !errors.Is(dialErr, syscall.ECONNREFUSED) {
		return fmt.Errorf("cannot prove socket %s is stale: %w", socketPath, dialErr)
	}
	return quarantineAndRemoveSocket(socketPath, expected.dev, expected.ino, hooks)
}

func runWithSyncEngineFactory(ctx context.Context, profileName string, factory syncEngineFactory) error {
	paths, err := config.ResolveRuntimePaths()
	if err != nil {
		return fmt.Errorf("load runtime paths: %w", err)
	}
	ownerLock, err := acquireDatabaseOwnerLock(paths.DBPath)
	if err != nil {
		return fmt.Errorf("acquire database owner lock: %w", err)
	}
	defer ownerLock.Close()
	cfg, credentialErr := config.Load(profileName)
	preCredential := errors.Is(credentialErr, config.ErrCredentialsNotFound)
	if credentialErr != nil && !preCredential {
		return fmt.Errorf("load config: %w", credentialErr)
	}

	store, err := localstore.Open(ownerLock.DatabasePath())
	if err != nil {
		return fmt.Errorf("open local store: %w", err)
	}
	defer store.Close()
	manifestService, err := localapi.NewIntegrationManifestService(store)
	if err != nil {
		return fmt.Errorf("open integration manifest service: %w", err)
	}

	// Credentials and SQLite are resolved before the local endpoint. A stale
	// Unix socket from an unclean shutdown is replaceable; other file types
	// are rejected and preserved.
	if err := os.MkdirAll(filepath.Dir(paths.SocketPath), 0o700); err != nil {
		return fmt.Errorf("create socket directory: %w", err)
	}
	if err := removeStaleSocket(paths.SocketPath); err != nil {
		return err
	}

	er := localstore.NewEventRepo(store.DB())
	tr := localstore.NewTaskRepo(store.DB(), er)
	kb := localstore.NewKBRepo(store.DB())
	gr := localstore.NewGitRepo(store.DB())

	// Initialize sync repositories shared by the per-binding engines. Queue
	// operations remain namespace-scoped inside QueueRepo.
	queueRepo := sync.NewQueueRepo(store.DB())
	auditRepo := sync.NewAuditRepo(store.DB())
	syncCfg := sync.DefaultConfig()

	// A fresh Gateway deliberately serves the same-user enrolment endpoint
	// before credentials exist. It does not start bootstrap or incremental sync;
	// Task 4 owns the post-persistence transition.
	if preCredential {
		srv, err := localapi.NewWithRuntime(paths.SocketPath, "", "", "", store, tr, er, kb,
			eventbus.NewEventBus(), scheduler.NewScheduler(), queueRepo)
		if err != nil {
			return fmt.Errorf("start pre-credential local api: %w", err)
		}
		defer srv.Close()
		srv.SetVersion(gatewayVersion())
		srv.SetIntegrationManifestService(manifestService)
		srv.SetRecoveryOnlyProjects(nil, true)
		srv.SetEnrolmentRuntime(loadEnrolmentPolicy(), paths.CredentialsDir)
		srv.EnableEnrolmentBootstrap(syncCfg)
		return srv.Serve(ctx)
	}

	// P5: prefer multi-org wiring when more than one credential profile is
	// present. Single-profile deployments retain the resolved Load(profile)
	// credentials and exactly one engine.
	multiCfg, multiErr := config.LoadMultiOrg()
	useMultiOrg := multiErr == nil && len(multiCfg.Orgs) > 1
	var syncEngines *syncGroup
	if useMultiOrg {
		syncEngines, err = newMultiOrgSyncGroup(multiCfg.Orgs, multiCfg.Bindings, store, queueRepo, auditRepo, tr, kb, syncCfg, factory)
	} else {
		engine, engineErr := factory(cfg.Credentials.Server, cfg.Credentials.Token, cfg.Credentials.ProjectID, queueRepo, auditRepo, tr, kb, syncCfg)
		if engineErr != nil {
			return fmt.Errorf("configure sync engine: %w", engineErr)
		}
		if configurable, ok := engine.(bootstrapConfigurableSyncEngine); ok {
			if err := configurable.ConfigureBootstrap(store, cfg.Credentials.AgentID, cfg.Credentials.PassportID, nil); err != nil {
				return fmt.Errorf("configure sync bootstrap: %w", err)
			}
		}
		syncEngines = &syncGroup{
			engines:      []syncEngine{engine},
			readyEngines: []syncEngine{},
			projects:     map[string]syncEngine{cfg.Credentials.ProjectID: engine},
			notReady:     make(map[string]bool),
		}
		if err := store.ValidateReadyCheckpoint(ctx, cfg.Credentials.ProjectID, cfg.Credentials.AgentID, cfg.Credentials.PassportID, profileName); err != nil {
			syncEngines.notReady[cfg.Credentials.ProjectID] = true
		} else {
			syncEngines.readyEngines = []syncEngine{engine}
		}
	}
	if err != nil {
		return err
	}
	wireEventAndGitReplicas(syncEngines, er, gr)
	wireIntegrationManifestReceivers(syncEngines, manifestService)

	// P3: eventbus + scheduler are always constructed so agent registration,
	// presence, task routing, and subscriptions (wormhole.agent.register,
	// wormhole.task.route, etc.) work regardless of single- or multi-org mode.
	eb := eventbus.NewEventBus()
	sched := scheduler.NewScheduler()

	var srv *localapi.Server
	if useMultiOrg {
		srv, err = localapi.NewMultiOrg(paths.SocketPath, multiCfg.Orgs, multiCfg.Bindings, store, tr, er, kb, eb, sched, queueRepo)
	} else {
		srv, err = localapi.NewWithRuntime(paths.SocketPath, cfg.Credentials.Server, cfg.Credentials.Token, cfg.Credentials.ProjectID, store, tr, er, kb, eb, sched, queueRepo)
	}
	if err != nil {
		return fmt.Errorf("start local api: %w", err)
	}
	defer srv.Close()
	codeGraphProjects := []string{cfg.Credentials.ProjectID}
	if useMultiOrg {
		codeGraphProjects = codeGraphProjects[:0]
		for _, binding := range multiCfg.Bindings {
			codeGraphProjects = append(codeGraphProjects, binding.ProjectID)
		}
	}
	srv.SetVersion(gatewayVersion())
	srv.SetIntegrationManifestService(manifestService)
	srv.SetSyncStatusProvider(syncEngines)
	recoveryProjects := make([]string, 0, len(syncEngines.notReady))
	for projectID := range syncEngines.notReady {
		recoveryProjects = append(recoveryProjects, projectID)
	}
	_ = wireCodeGraphRuntimes(ctx, srv, store.DB(), codeGraphProjects, syncEngines.notReady)
	srv.SetRecoveryOnlyProjects(recoveryProjects, len(recoveryProjects) == len(syncEngines.projects))
	srv.SetEnrolmentRuntime(loadEnrolmentPolicy(), paths.CredentialsDir)
	srv.EnableEnrolmentBootstrap(syncCfg)
	if useMultiOrg {
		for _, binding := range multiCfg.Bindings {
			if org, ok := multiCfg.Orgs[binding.OrgName]; ok {
				srv.SetAuthorizationAgent(binding.ProjectID, org.Credentials.AgentID)
			}
		}
	} else {
		srv.SetAuthorizationAgent(cfg.Credentials.ProjectID, cfg.Credentials.AgentID)
	}

	// NewWithRuntime/NewMultiOrg has already bound the local socket. Only now
	// start network reconciliation; Engine.Start performs the first push-then-
	// pull cycle in its background goroutine and never gates local readiness.
	return serveWithSync(ctx, srv, syncEngines)
}

var errCodeGraphRecoveryOnly = errors.New("Code Graph unavailable while project recovery is required")

// wireCodeGraphRuntimes binds exactly one derivative runtime per ready project.
// Code Graph is disabled by default and non-authoritative, so an unavailable
// graph store must not prevent Gateway's enrolment/recovery surface from
// serving. The returned per-project outcomes exist for deterministic tests and
// future local health logging; callers deliberately continue on errors.
func wireCodeGraphRuntimes(ctx context.Context, srv *localapi.Server, db *sql.DB, projectIDs []string, recoveryOnly map[string]bool) map[string]error {
	outcomes := make(map[string]error, len(projectIDs))
	seen := make(map[string]struct{}, len(projectIDs))
	for _, projectID := range projectIDs {
		if _, duplicate := seen[projectID]; duplicate {
			continue
		}
		seen[projectID] = struct{}{}
		if recoveryOnly[projectID] {
			outcomes[projectID] = errCodeGraphRecoveryOnly
			continue
		}
		runtime, err := localapi.NewCodeGraphRuntime(ctx, db, projectID)
		outcomes[projectID] = err
		if err == nil {
			srv.SetCodeGraphRuntime(projectID, runtime)
		}
	}
	return outcomes
}

type environmentEnrolmentPolicy struct {
	roles       []string
	permissions []string
}

func (policy environmentEnrolmentPolicy) EnrolmentPermissionEnvelope(context.Context, string) (localapi.EnrolmentPermissionEnvelope, error) {
	return localapi.EnrolmentPermissionEnvelope{Roles: policy.roles, Permissions: policy.permissions}, nil
}

// loadEnrolmentPolicy reads the operator-controlled Gateway process
// environment. Absence is intentionally nil/deny-all; enrolment requests can
// never supply or expand this ceiling themselves.
func loadEnrolmentPolicy() localapi.EnrolmentPolicySource {
	roles, rolesSet := os.LookupEnv("WORMHOLE_ENROLMENT_ROLES")
	permissions, permissionsSet := os.LookupEnv("WORMHOLE_ENROLMENT_PERMISSIONS")
	if !rolesSet && !permissionsSet {
		return nil
	}
	return environmentEnrolmentPolicy{roles: splitPolicyValues(roles), permissions: splitPolicyValues(permissions)}
}

func splitPolicyValues(value string) []string {
	if strings.TrimSpace(value) == "" {
		return []string{}
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}
