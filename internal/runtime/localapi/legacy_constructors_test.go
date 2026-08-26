package localapi

// Historical package tests exercise pre-Stage-2 handler behavior through this
// test-only topology. Production builds expose only NewSupervisor.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/eventbus"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/runtime/scheduler"
	syncpkg "github.com/H4RL33/wormhole/internal/runtime/sync"
)

func New(socketPath, coordServerURL, token, projectID string, store *localstore.Store, tr *localstore.TaskRepo, er *localstore.EventRepo, kb *localstore.KBRepo, qr *syncpkg.QueueRepo) (*Server, error) {
	return legacyTestServer(socketPath, coordServerURL, token, projectID, store, tr, er, kb, nil, nil, qr)
}

func NewWithRuntime(socketPath, coordServerURL, token, projectID string, store *localstore.Store, tr *localstore.TaskRepo, er *localstore.EventRepo, kb *localstore.KBRepo, eb *eventbus.EventBus, sched *scheduler.Scheduler, qr *syncpkg.QueueRepo) (*Server, error) {
	return legacyTestServer(socketPath, coordServerURL, token, projectID, store, tr, er, kb, eb, sched, qr)
}

func NewMultiOrg(socketPath string, orgs map[string]config.Org, bindings []config.ProjectBinding, store *localstore.Store, tr *localstore.TaskRepo, er *localstore.EventRepo, kb *localstore.KBRepo, eb *eventbus.EventBus, sched *scheduler.Scheduler, qr *syncpkg.QueueRepo) (*Server, error) {
	if len(orgs) == 0 {
		return nil, fmt.Errorf("localapi: NewMultiOrg: no orgs provided")
	}
	server, err := legacyTestServer(socketPath, "", "", "", store, tr, er, kb, eb, sched, qr)
	if err != nil {
		return nil, err
	}
	server.orgs, server.bindings, server.isMultiOrg = orgs, bindings, true
	return server, nil
}

func legacyTestServer(socketPath, coordServerURL, token, projectID string, store *localstore.Store, tr *localstore.TaskRepo, er *localstore.EventRepo, kb *localstore.KBRepo, eb *eventbus.EventBus, sched *scheduler.Scheduler, qr *syncpkg.QueueRepo) (*Server, error) {
	listener, err := listenLocalSocket(socketPath)
	if err != nil {
		return nil, fmt.Errorf("localapi: listen on %s: %w", socketPath, err)
	}
	server := &Server{listener: listener, socketPath: socketPath, httpClient: &http.Client{Timeout: 10 * time.Second}, coordServer: coordServerURL, token: token, projectID: projectID, store: store, tr: tr, er: er, kb: kb, qr: qr, eventbus: eb, scheduler: sched, handlers: make(chan struct{}, maxActiveConnections), serveReady: make(chan struct{})}
	if store != nil {
		server.gr = localstore.NewGitRepo(store.DB())
	}
	server.registry = newLocalRegistry(server)
	server.testCodeGraphLifecycle = server.executeLegacyTestCodeGraphLifecycle
	return server, nil
}

func (s *Server) executeLegacyTestCodeGraphLifecycle(ctx context.Context, request CodeGraphLifecycleRequest) (CodeGraphLifecycleStatus, error) {
	if request.ProjectID == "" {
		return CodeGraphLifecycleStatus{}, errors.New("code graph lifecycle: project_id is required")
	}
	if !codeGraphLifecycleOperationSupported(request.Operation) {
		return CodeGraphLifecycleStatus{}, errors.New("code graph lifecycle: unsupported operation")
	}
	binding := codeGraphRepositoryBinding{}
	if request.Operation == CodeGraphEnable || request.Operation == CodeGraphCheckoutSet || request.Operation == CodeGraphRebuild {
		var err error
		binding, err = s.resolveLegacyTestCodeGraphLifecycleBinding(ctx, request.ProjectID)
		if err != nil {
			return CodeGraphLifecycleStatus{}, err
		}
	}
	runtime, err := s.ensureCodeGraphRuntime(ctx, request.ProjectID)
	if err != nil {
		return CodeGraphLifecycleStatus{}, err
	}
	return runtime.Lifecycle.executeWithBinding(ctx, request, binding)
}

func (s *Server) resolveLegacyTestCodeGraphLifecycleBinding(ctx context.Context, projectID string) (codeGraphRepositoryBinding, error) {
	multi, err := config.LoadMultiOrg()
	if err != nil {
		if errors.Is(err, config.ErrNoCredentials) {
			return codeGraphRepositoryBinding{}, errors.New("code graph lifecycle: no credential profile binds this project")
		}
		return codeGraphRepositoryBinding{}, fmt.Errorf("code graph lifecycle: load credential inventory: %w", err)
	}
	matchedProfile := ""
	var matched config.Credentials
	for profile, org := range multi.Orgs {
		if org.Credentials.ProjectID != projectID {
			continue
		}
		if matchedProfile != "" {
			return codeGraphRepositoryBinding{}, errors.New("code graph lifecycle: multiple credential profiles bind this project")
		}
		matchedProfile, matched = profile, org.Credentials
	}
	if matchedProfile == "" {
		return codeGraphRepositoryBinding{}, errors.New("code graph lifecycle: no credential profile binds this project")
	}
	if s == nil || s.store == nil {
		return codeGraphRepositoryBinding{}, errors.New("code graph lifecycle: Gateway store unavailable")
	}
	if err := s.store.ValidateReadyCheckpoint(ctx, projectID, matched.AgentID, matched.PassportID, matchedProfile); err != nil {
		return codeGraphRepositoryBinding{}, fmt.Errorf("code graph lifecycle: active credential is not a ready bootstrapped checkpoint: %w", err)
	}
	return codeGraphRepositoryBinding{profile: matchedProfile, agent: matched.AgentID, passport: matched.PassportID}, nil
}
