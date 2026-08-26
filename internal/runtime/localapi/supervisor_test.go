package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	codegraphquery "github.com/H4RL33/wormhole/internal/runtime/codegraph/query"
	"github.com/H4RL33/wormhole/internal/runtime/localidentity"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/runtime/projectstate"
	syncpkg "github.com/H4RL33/wormhole/internal/runtime/sync"
	"github.com/H4RL33/wormhole/internal/types"
)

func TestNewSupervisorRejectsIncompleteDependencies(t *testing.T) {
	complete := supervisorTestDependencies(t)
	tests := []struct {
		name   string
		mutate func(*SupervisorDependencies)
	}{
		{name: "store", mutate: func(d *SupervisorDependencies) { d.Store = nil }},
		{name: "project state", mutate: func(d *SupervisorDependencies) { d.ProjectState = nil }},
		{name: "identity", mutate: func(d *SupervisorDependencies) { d.Identity = nil }},
		{name: "Fabric", mutate: func(d *SupervisorDependencies) { d.Fabric = nil }},
		{name: "Code Graph", mutate: func(d *SupervisorDependencies) { d.CodeGraph = nil }},
		{name: "typed nil Fabric", mutate: func(d *SupervisorDependencies) { var value *nilFabricRouter; d.Fabric = value }},
		{name: "typed nil Code Graph", mutate: func(d *SupervisorDependencies) { var value *nilCodeGraphProvider; d.CodeGraph = value }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies := complete
			test.mutate(&dependencies)
			if _, err := NewSupervisor(dependencies); !errors.Is(err, ErrIncompleteSupervisorDependencies) {
				t.Fatalf("NewSupervisor error = %v, want ErrIncompleteSupervisorDependencies", err)
			}
		})
	}

	closed := supervisorTestDependencies(t)
	if err := closed.Store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSupervisor(closed); !errors.Is(err, ErrIncompleteSupervisorDependencies) {
		t.Fatalf("closed Store error = %v, want ErrIncompleteSupervisorDependencies", err)
	}
}

func TestLocalOnlyFabricAndDisabledCodeGraphAreBindingScopedAndUnavailable(t *testing.T) {
	binding := privateRoutingTestBinding(t, t.TempDir(), "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011")
	fabric := NewLocalOnlyFabricRouter()
	if _, err := fabric.Status(context.Background(), binding); !errors.Is(err, ErrFabricUnavailable) {
		t.Fatalf("Fabric Status error = %v, want ErrFabricUnavailable", err)
	}
	if _, err := fabric.Call(context.Background(), binding, "wormhole.sync.status", nil); !errors.Is(err, ErrFabricUnavailable) {
		t.Fatalf("Fabric Call error = %v, want ErrFabricUnavailable", err)
	}
	graph := NewDisabledCodeGraphProvider()
	if _, err := graph.Status(context.Background(), binding); !errors.Is(err, ErrCodeGraphUnavailable) {
		t.Fatalf("Code Graph Status error = %v, want ErrCodeGraphUnavailable", err)
	}
	if _, err := graph.Query(context.Background(), binding, codegraphquery.Request{}); !errors.Is(err, ErrCodeGraphUnavailable) {
		t.Fatalf("Code Graph Query error = %v, want ErrCodeGraphUnavailable", err)
	}
	if _, err := graph.Rebuild(context.Background(), binding); !errors.Is(err, ErrCodeGraphUnavailable) {
		t.Fatalf("Code Graph Rebuild error = %v, want ErrCodeGraphUnavailable", err)
	}

	// The unavailable implementations carry no client, command, worker, or
	// mutable lifecycle state: calling them cannot start network or process work.
	for name, provider := range map[string]any{"Fabric": fabric, "Code Graph": graph} {
		kind := reflect.TypeOf(provider)
		if kind.Kind() == reflect.Pointer {
			kind = kind.Elem()
		}
		if kind.NumField() != 0 {
			t.Fatalf("%s unavailable provider has %d fields, want zero", name, kind.NumField())
		}
	}
}

func TestSupervisorIsolatesMultipleWorkspacesThroughOneGateway(t *testing.T) {
	root := t.TempDir()
	store, err := localstore.Open(filepath.Join(root, "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := localstore.NewWorkspaceRepo(store.DB())
	projectID := "00000000-0000-4000-8000-000000000001"
	first := privateRoutingTestBinding(t, filepath.Join(root, "first"), projectID, "00000000-0000-4000-8000-000000000011")
	second := privateRoutingTestBinding(t, filepath.Join(root, "second"), projectID, "00000000-0000-4000-8000-000000000012")
	for _, binding := range []*types.WorkspaceBinding{&first, &second} {
		tree, digest := privateRoutingWorkspaceTree(t, binding.Scope.ProjectID, binding.Repository)
		binding.AcceptedTreeDigest = digest
		if _, _, err := repo.RegisterWorkspace(context.Background(), *binding, tree); err != nil {
			t.Fatal(err)
		}
	}
	service, err := projectstate.NewService(repo, projectstate.ServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := localidentity.Open(filepath.Join(root, "identities"))
	if err != nil {
		t.Fatal(err)
	}
	profile, err := identity.EnsureSelectedForSetup(context.Background(), "00000000-0000-4000-8000-000000000031", types.ConfirmedIdentitySelection{DisplayName: "Local Owner"})
	if err != nil {
		t.Fatal(err)
	}
	provider := &recordingCodeGraphProvider{err: ErrCodeGraphUnavailable}
	fabric := &recordingFabricRouter{err: ErrFabricUnavailable}
	supervisor, err := NewSupervisor(SupervisorDependencies{Store: store, ProjectState: service, Identity: identity, Fabric: fabric, CodeGraph: provider})
	if err != nil {
		t.Fatal(err)
	}
	server, err := supervisor.Listen(filepath.Join(root, "gateway.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close() })
	if server.projectState != service || server.identityStore != identity || server.fabricRouter == nil || server.codeGraphProvider == nil {
		t.Fatal("supervisor server did not retain the complete dependency graph")
	}
	if _, err := supervisor.Listen(filepath.Join(root, "second.sock")); !errors.Is(err, ErrSupervisorAlreadyListening) {
		t.Fatalf("second Listen error = %v, want ErrSupervisorAlreadyListening", err)
	}
	server.clock = func() time.Time { return time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC) }

	for _, want := range []types.WorkspaceBinding{first, second} {
		raw, err := json.Marshal(map[string]any{privateRequestContextKey: map[string]string{"working_directory": want.Checkout.CanonicalPath}})
		if err != nil {
			t.Fatal(err)
		}
		ctx, _, err := server.resolvePrivateRequest(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ResolvedBinding(ctx)
		if err != nil || got != want {
			t.Fatalf("cwd %q resolved (%+v, %v), want %+v", want.Checkout.CanonicalPath, got, err, want)
		}
		actor, err := ServerOwnedActor(ctx)
		if err != nil || actor.HumanPrincipalID != profile.HumanPrincipalID || actor.ActorKind != types.ActorHuman || actor.Assurance != types.AssuranceLocal {
			t.Fatalf("cwd %q actor = (%+v, %v), want local human %q", want.Checkout.CanonicalPath, actor, err, profile.HumanPrincipalID)
		}
	}
	if _, err := server.executePrivateCodeGraphLifecycle(WithResolvedBinding(context.Background(), first), CodeGraphLifecycleRequest{Operation: CodeGraphStatus, ProjectID: first.Scope.ProjectID}); !errors.Is(err, ErrCodeGraphUnavailable) {
		t.Fatalf("configured lifecycle error = %v, want ErrCodeGraphUnavailable", err)
	}
	if provider.calls != 1 || provider.binding != first {
		t.Fatalf("configured lifecycle provider calls=%d binding=%+v, want one exact %+v", provider.calls, provider.binding, first)
	}
	if _, loaded := server.codeGraphs.Load(first.Scope.ProjectID); loaded {
		t.Fatal("configured lifecycle started a legacy Code Graph runtime")
	}
	syncStatus := privateDispatchResult(t, server, first, "wormhole.sync.status", nil, nil)
	if !syncStatus.IsError || len(syncStatus.Content) != 1 || syncStatus.Content[0].Text != ErrFabricUnavailable.Error() {
		t.Fatalf("configured sync status = %+v, want exact local-only Fabric sentinel", syncStatus)
	}
	if fabric.calls != 1 || fabric.binding != first {
		t.Fatalf("configured Fabric calls=%d binding=%+v, want one exact %+v", fabric.calls, fabric.binding, first)
	}
	whoami := privateDispatchResult(t, server, first, "wormhole.agent.whoami", nil, nil)
	if !whoami.IsError || len(whoami.Content) != 1 || whoami.Content[0].Text != "localapi: binding-aware provider unavailable: wormhole.agent.whoami" {
		t.Fatalf("configured whoami = %+v, want exact fail-closed provider error", whoami)
	}
}

func TestSupervisorSchemaV6ReopensExactlyAndRefusesChangedLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	store, err := localstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := localstore.Open(path)
	if err != nil {
		t.Fatalf("reopen exact schema v6: %v", err)
	}
	service, err := projectstate.NewService(localstore.NewWorkspaceRepo(reopened.DB()), projectstate.ServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := localidentity.Open(filepath.Join(t.TempDir(), "identities"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSupervisor(SupervisorDependencies{Store: reopened, ProjectState: service, Identity: identity, Fabric: NewLocalOnlyFabricRouter(), CodeGraph: NewDisabledCodeGraphProvider()}); err != nil {
		t.Fatalf("construct over exact schema v6: %v", err)
	}
	if _, err := reopened.DB().Exec(`UPDATE gateway_schema_migrations SET version=5`); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localstore.Open(path); err == nil {
		t.Fatal("changed schema-v6 ledger reopened")
	} else {
		var unsupported localstore.ErrUnsupportedPrivateFormat
		if !errors.As(err, &unsupported) {
			t.Fatalf("changed ledger error = %v, want ErrUnsupportedPrivateFormat", err)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("refused schema changed during reopen")
	}
}

func TestSupervisorCloseOwnsServerButNotInjectedStore(t *testing.T) {
	dependencies := supervisorTestDependencies(t)
	supervisor, err := NewSupervisor(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	server, err := supervisor.Listen(filepath.Join(t.TempDir(), "gateway.sock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	if !server.shutdown.Load() {
		t.Fatal("Supervisor.Close did not close its server")
	}
	if err := dependencies.Store.DB().PingContext(context.Background()); err != nil {
		t.Fatalf("Supervisor.Close closed injected Store: %v", err)
	}
}

func TestSupervisorCloseBeforeListenAndConcurrentClose(t *testing.T) {
	closed, err := NewSupervisor(supervisorTestDependencies(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := closed.Listen(filepath.Join(t.TempDir(), "closed.sock")); !errors.Is(err, ErrSupervisorClosed) {
		t.Fatalf("Listen after Close error = %v, want ErrSupervisorClosed", err)
	}

	supervisor, err := NewSupervisor(supervisorTestDependencies(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Listen(filepath.Join(t.TempDir(), "gateway.sock")); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 8)
	for range 8 {
		go func() { errs <- supervisor.Close() }()
	}
	for range 8 {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent Close: %v", err)
		}
	}
}

func TestSupervisorConcurrentListenPublishesExactlyOneServer(t *testing.T) {
	supervisor, err := NewSupervisor(supervisorTestDependencies(t))
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	type outcome struct {
		server *Server
		err    error
	}
	outcomes := make(chan outcome, 8)
	root := t.TempDir()
	for index := range 8 {
		go func(index int) {
			server, err := supervisor.Listen(filepath.Join(root, fmt.Sprintf("gateway-%d.sock", index)))
			outcomes <- outcome{server: server, err: err}
		}(index)
	}
	successes, duplicates := 0, 0
	for range 8 {
		result := <-outcomes
		switch {
		case result.err == nil && result.server != nil:
			successes++
		case errors.Is(result.err, ErrSupervisorAlreadyListening) && result.server == nil:
			duplicates++
		default:
			t.Fatalf("concurrent Listen outcome = %+v", result)
		}
	}
	if successes != 1 || duplicates != 7 {
		t.Fatalf("concurrent Listen successes=%d duplicates=%d", successes, duplicates)
	}
}

func TestSupervisorCloseWaitsForAdmittedHandlerAndLeavesStoreOpen(t *testing.T) {
	dependencies := supervisorTestDependencies(t)
	supervisor, err := NewSupervisor(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	server, err := supervisor.Listen(filepath.Join(t.TempDir(), "gateway.sock"))
	if err != nil {
		t.Fatal(err)
	}
	admitted := make(chan struct{})
	release := make(chan struct{})
	server.testBeforeHandlerStart = func() {
		close(admitted)
		<-release
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background()) }()
	<-server.Serving()
	connection, err := net.Dial("unix", server.socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	<-admitted
	closeDone := make(chan error, 1)
	go func() { closeDone <- supervisor.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before admitted handler quiesced: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := dependencies.Store.DB().PingContext(context.Background()); err != nil {
		t.Fatalf("Store unavailable while Supervisor waits: %v", err)
	}
	close(release)
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-serveDone; err != nil {
		t.Fatal(err)
	}
	if err := dependencies.Store.DB().PingContext(context.Background()); err != nil {
		t.Fatalf("Supervisor closed injected Store: %v", err)
	}
}

func supervisorTestDependencies(t *testing.T) SupervisorDependencies {
	t.Helper()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service, err := projectstate.NewService(localstore.NewWorkspaceRepo(store.DB()), projectstate.ServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := localidentity.Open(filepath.Join(t.TempDir(), "identities"))
	if err != nil {
		t.Fatal(err)
	}
	return SupervisorDependencies{Store: store, ProjectState: service, Identity: identity, Fabric: NewLocalOnlyFabricRouter(), CodeGraph: NewDisabledCodeGraphProvider()}
}

type nilFabricRouter struct{}

func (*nilFabricRouter) Status(context.Context, types.WorkspaceBinding) (syncpkg.Status, error) {
	return syncpkg.Status{}, nil
}
func (*nilFabricRouter) Call(context.Context, types.WorkspaceBinding, string, json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}

type nilCodeGraphProvider struct{}

func (*nilCodeGraphProvider) Status(context.Context, types.WorkspaceBinding) (CodeGraphLifecycleStatus, error) {
	return CodeGraphLifecycleStatus{}, nil
}
func (*nilCodeGraphProvider) Query(context.Context, types.WorkspaceBinding, codegraphquery.Request) (codegraphquery.Result, error) {
	return codegraphquery.Result{}, nil
}
func (*nilCodeGraphProvider) Rebuild(context.Context, types.WorkspaceBinding) (CodeGraphLifecycleStatus, error) {
	return CodeGraphLifecycleStatus{}, nil
}

type recordingCodeGraphProvider struct {
	calls   int
	binding types.WorkspaceBinding
	err     error
}

type recordingFabricRouter struct {
	calls   int
	binding types.WorkspaceBinding
	err     error
}

func (p *recordingFabricRouter) Status(_ context.Context, binding types.WorkspaceBinding) (syncpkg.Status, error) {
	p.calls++
	p.binding = binding
	return syncpkg.Status{}, p.err
}
func (*recordingFabricRouter) Call(context.Context, types.WorkspaceBinding, string, json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}

func (p *recordingCodeGraphProvider) Status(_ context.Context, binding types.WorkspaceBinding) (CodeGraphLifecycleStatus, error) {
	p.calls++
	p.binding = binding
	return CodeGraphLifecycleStatus{}, p.err
}
func (*recordingCodeGraphProvider) Query(context.Context, types.WorkspaceBinding, codegraphquery.Request) (codegraphquery.Result, error) {
	return codegraphquery.Result{}, nil
}
func (*recordingCodeGraphProvider) Rebuild(context.Context, types.WorkspaceBinding) (CodeGraphLifecycleStatus, error) {
	return CodeGraphLifecycleStatus{}, nil
}
