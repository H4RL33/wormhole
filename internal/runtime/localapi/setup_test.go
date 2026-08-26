package localapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/H4RL33/wormhole/internal/runtime/localidentity"
	"github.com/H4RL33/wormhole/internal/types"
)

func TestPrivateSetupEnsureIdentityRPCUsesServerOwnedStore(t *testing.T) {
	store, err := localidentity.Open(filepath.Join(t.TempDir(), "identities"))
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{identityStore: store}
	req := SetupIdentityRequest{
		JournalID: "00000000-0000-4000-8000-000000000031",
		Selection: types.ConfirmedIdentitySelection{DisplayName: "Alice Example", Email: "alice@example.test"},
	}
	first, err := server.PrivateSetupEnsureIdentityRPC(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := server.PrivateSetupEnsureIdentityRPC(context.Background(), req)
	if err != nil || first.HumanPrincipalID == "" || first.HumanPrincipalID != second.HumanPrincipalID {
		t.Fatalf("profiles = (%+v, %+v), err %v", first, second, err)
	}
	if _, exists := newLocalRegistry(server).Get(PrivateSetupEnsureIdentityRPCMethod); exists {
		t.Fatal("private setup RPC became a public MCP tool")
	}
}

func TestPrivateSetupEnsureIdentityRPCDispatchIsNotAnMCPTool(t *testing.T) {
	store, err := localidentity.Open(filepath.Join(t.TempDir(), "identities"))
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{identityStore: store, registry: newLocalRegistry(&Server{})}
	client, gateway := net.Pipe()
	defer client.Close()
	defer gateway.Close()
	req := SetupIdentityRequest{JournalID: "00000000-0000-4000-8000-000000000033", Selection: types.ConfirmedIdentitySelection{DisplayName: "Alice Example"}}
	params, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		server.dispatchMCPMessage(context.Background(), &mcpSession{initialized: true}, gateway, server.registry, rpcRequest{
			JSONRPC: "2.0", ID: json.RawMessage("1"), Method: PrivateSetupEnsureIdentityRPCMethod, Params: params,
		})
		close(done)
	}()
	line, err := bufio.NewReader(client).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	<-done
	var response rpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(line), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != nil {
		t.Fatalf("private dispatch error = %+v", response.Error)
	}
	var profile localidentity.PublicHumanProfile
	if err := json.Unmarshal(response.Result, &profile); err != nil || profile.HumanPrincipalID == "" {
		t.Fatalf("profile = %+v, err %v", profile, err)
	}
	if _, exists := server.registry.Get(PrivateSetupEnsureIdentityRPCMethod); exists {
		t.Fatal("private setup RPC became a public tool")
	}
}

func TestProductionIdentityRootComesOnlyFromPrivateDataHomeOutsideRepositories(t *testing.T) {
	dataHome := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_DATA_HOME", dataHome)
	store, err := OpenProductionIdentityStore()
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.EnsureSelectedForSetup(context.Background(), "00000000-0000-4000-8000-000000000032", types.ConfirmedIdentitySelection{DisplayName: "Alice Example"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataHome, "wormhole", "identities", "selected.json")); err != nil {
		t.Fatalf("selected identity not under XDG data home: %v", err)
	}

	repository := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", filepath.Join(repository, "private-data"))
	if _, err := OpenProductionIdentityStore(); !errors.Is(err, ErrIdentityRootInsideRepository) {
		t.Fatalf("repository-contained identity root error = %v", err)
	}
}
