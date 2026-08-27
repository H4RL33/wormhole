// Run wires config, localstore, local identity, project state, and localapi
// into one user-level local-only supervisor and blocks until cancellation.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/localapi"
	"github.com/H4RL33/wormhole/internal/runtime/localidentity"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/runtime/projectstate"
)

func Run(ctx context.Context, _ string) error {
	if err := ensureSupportedPlatform(); err != nil {
		return err
	}
	return runLocalOnlySupervisor(ctx)
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

func runLocalOnlySupervisor(ctx context.Context) error {
	paths, err := config.ResolveRuntimePaths()
	if err != nil {
		return fmt.Errorf("load runtime paths: %w", err)
	}
	ownerLock, err := acquireDatabaseOwnerLock(paths.DBPath)
	if err != nil {
		return fmt.Errorf("acquire database owner lock: %w", err)
	}
	defer ownerLock.Close()
	store, err := localstore.Open(ownerLock.DatabasePath())
	if err != nil {
		return fmt.Errorf("open local store: %w", err)
	}
	defer store.Close()
	projectState, err := newProjectStateService(store)
	if err != nil {
		return err
	}
	if err := projectState.PrepareRegisteredWorkspaces(ctx); err != nil {
		return fmt.Errorf("prepare registered workspaces: %w", err)
	}
	manifestService, err := localapi.NewIntegrationManifestService(store)
	if err != nil {
		return fmt.Errorf("open integration manifest service: %w", err)
	}
	identity, err := localapi.OpenProductionIdentityStore()
	if err != nil {
		return fmt.Errorf("open local identity store: %w", err)
	}
	if err := identity.RecoverConnectionSessions(ctx); err != nil {
		return fmt.Errorf("recover local identity sessions: %w", err)
	}
	supervisor, err := newLocalSupervisorWithProjectState(store, projectState, identity)
	if err != nil {
		return err
	}
	defer supervisor.Close()
	if err := os.MkdirAll(filepath.Dir(paths.SocketPath), 0o700); err != nil {
		return fmt.Errorf("create socket directory: %w", err)
	}
	if err := removeStaleSocket(paths.SocketPath); err != nil {
		return err
	}
	server, err := supervisor.Listen(paths.SocketPath)
	if err != nil {
		return fmt.Errorf("start local api: %w", err)
	}
	server.SetVersion(gatewayVersion())
	server.SetIntegrationManifestService(manifestService)
	return server.Serve(ctx)
}

func newLocalSupervisor(store *localstore.Store, identity *localidentity.Store) (*localapi.Supervisor, error) {
	if store == nil || identity == nil {
		return nil, fmt.Errorf("construct Gateway supervisor: incomplete local dependencies")
	}
	service, err := newProjectStateService(store)
	if err != nil {
		return nil, err
	}
	return newLocalSupervisorWithProjectState(store, service, identity)
}

func newProjectStateService(store *localstore.Store) (*projectstate.Service, error) {
	if store == nil {
		return nil, fmt.Errorf("construct project state service: local store is required")
	}
	service, err := projectstate.NewService(localstore.NewWorkspaceRepo(store.DB()), projectstate.ServiceConfig{})
	if err != nil {
		return nil, fmt.Errorf("construct project state service: %w", err)
	}
	return service, nil
}

func newLocalSupervisorWithProjectState(store *localstore.Store, service *projectstate.Service, identity *localidentity.Store) (*localapi.Supervisor, error) {
	if store == nil || service == nil || identity == nil {
		return nil, fmt.Errorf("construct Gateway supervisor: incomplete local dependencies")
	}
	supervisor, err := localapi.NewSupervisor(localapi.SupervisorDependencies{
		Store: store, ProjectState: service, Identity: identity,
		Fabric:    localapi.NewLocalOnlyFabricRouter(),
		CodeGraph: localapi.NewDisabledCodeGraphProvider(),
	})
	if err != nil {
		return nil, fmt.Errorf("construct Gateway supervisor: %w", err)
	}
	return supervisor, nil
}
