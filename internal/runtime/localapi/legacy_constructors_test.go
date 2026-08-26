package localapi

// Historical package tests exercise pre-Stage-2 handler behavior through this
// test-only topology. Production builds expose only NewSupervisor.

import (
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
	return server, nil
}
