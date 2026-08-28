package sync

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/H4RL33/wormhole/internal/types"
)

func TestV2EngineStatusPreservesExactLocalWire(t *testing.T) {
	engine := NewV2Engine()
	status, err := engine.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Status{State: StateOffline, PendingWrites: 0}
	if status != want {
		t.Fatalf("status = %+v, want %+v", status, want)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"state":"offline","pending_writes":0}` {
		t.Fatalf("status JSON = %s", raw)
	}
}

func TestNilV2EngineStatusFailsWithoutPanic(t *testing.T) {
	var engine *V2Engine
	status, err := engine.Status(context.Background())
	if err == nil || status != (Status{}) {
		t.Fatalf("nil status = %+v, %v", status, err)
	}
}

type v2CredentialSourceFixture struct{}

func (v2CredentialSourceFixture) Read(context.Context, string) (string, error) {
	return "credential", nil
}

type v2RouteSourceFixture struct{}

func (v2RouteSourceFixture) GetRoute(context.Context, types.WorkspaceScope) (types.FabricBinding, types.FabricProfile, error) {
	return types.FabricBinding{}, types.FabricProfile{}, errors.New("fixture")
}

func TestActivityV1DependenciesSurviveV2Shell(t *testing.T) {
	var credential CredentialSource = v2CredentialSourceFixture{}
	var route FabricRouteSource = v2RouteSourceFixture{}
	if credential == nil || route == nil {
		t.Fatal("Activity v1 dependency interfaces are unavailable")
	}
}
