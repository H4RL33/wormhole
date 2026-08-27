package localapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sync"

	"github.com/H4RL33/wormhole/internal/runtime/eventbus"
	"github.com/H4RL33/wormhole/internal/runtime/localidentity"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/runtime/projectstate"
	"github.com/H4RL33/wormhole/internal/runtime/scheduler"
	syncpkg "github.com/H4RL33/wormhole/internal/runtime/sync"
)

var (
	ErrIncompleteSupervisorDependencies = errors.New("gateway: incomplete supervisor dependencies")
	ErrSupervisorAlreadyListening       = errors.New("gateway: supervisor is already listening")
	ErrSupervisorClosed                 = errors.New("gateway: supervisor is closed")
)

type SupervisorDependencies struct {
	Store        *localstore.Store
	ProjectState *projectstate.Service
	Identity     *localidentity.Store
	Fabric       FabricRouter
}

// Supervisor owns the one local API server and its listener. Its injected
// Store remains owned by the caller and is never closed by Supervisor.Close.
type Supervisor struct {
	dependencies SupervisorDependencies
	events       *localstore.EventRepo
	tasks        *localstore.TaskRepo
	articles     *localstore.KBRepo
	gitLinks     *localstore.GitRepo
	queue        *syncpkg.QueueRepo
	eventBus     *eventbus.EventBus
	scheduler    *scheduler.Scheduler

	mu     sync.Mutex
	server *Server
	closed bool
}

func NewSupervisor(dependencies SupervisorDependencies) (*Supervisor, error) {
	if dependencies.Store == nil || dependencies.ProjectState == nil || dependencies.Identity == nil || interfaceNil(dependencies.Fabric) {
		return nil, ErrIncompleteSupervisorDependencies
	}
	if dependencies.Store.DB() == nil || dependencies.Store.DB().PingContext(context.Background()) != nil {
		return nil, fmt.Errorf("%w: local store is unavailable", ErrIncompleteSupervisorDependencies)
	}
	events := localstore.NewEventRepo(dependencies.Store.DB())
	return &Supervisor{
		dependencies: dependencies,
		events:       events,
		tasks:        localstore.NewTaskRepo(dependencies.Store.DB(), events),
		articles:     localstore.NewKBRepo(dependencies.Store.DB()),
		gitLinks:     localstore.NewGitRepo(dependencies.Store.DB()),
		queue:        syncpkg.NewQueueRepo(dependencies.Store.DB()),
		eventBus:     eventbus.NewEventBus(),
		scheduler:    scheduler.NewScheduler(),
	}, nil
}

func interfaceNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

// Listen binds the sole local endpoint for this supervisor. The registry is
// constructed only after all dependencies and the owner-only socket exist.
func (s *Supervisor) Listen(socketPath string) (*Server, error) {
	if s == nil {
		return nil, ErrIncompleteSupervisorDependencies
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrSupervisorClosed
	}
	if s.server != nil {
		return nil, ErrSupervisorAlreadyListening
	}
	listener, err := listenLocalSocket(socketPath)
	if err != nil {
		return nil, fmt.Errorf("localapi: listen on %s: %w", socketPath, err)
	}
	server := &Server{
		listener:      listener,
		socketPath:    socketPath,
		httpClient:    &http.Client{},
		projectState:  s.dependencies.ProjectState,
		actorResolver: s.dependencies.Identity,
		identityStore: s.dependencies.Identity,
		fabricRouter:  s.dependencies.Fabric,
		store:         s.dependencies.Store,
		tr:            s.tasks,
		er:            s.events,
		kb:            s.articles,
		gr:            s.gitLinks,
		qr:            s.queue,
		eventbus:      s.eventBus,
		scheduler:     s.scheduler,
		handlers:      make(chan struct{}, maxActiveConnections),
		serveReady:    make(chan struct{}),
	}
	server.registry = newLocalRegistry(server)
	s.server = server
	return server, nil
}

func (s *Supervisor) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.closed = true
	server := s.server
	s.mu.Unlock()
	if server == nil {
		return nil
	}
	return server.Close()
}
