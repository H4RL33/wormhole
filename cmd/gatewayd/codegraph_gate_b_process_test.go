package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localapi"
	"github.com/H4RL33/wormhole/internal/runtime/localidentity"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestGatewayProcessCodeGraphLifecycleUsesDisabledBindingProvider(t *testing.T) {
	const (
		projectID   = "00000000-0000-4000-8000-000000000001"
		workspaceID = "00000000-0000-4000-8000-000000000011"
		journalID   = "00000000-0000-4000-8000-000000000031"
	)
	home, err := os.MkdirTemp("/tmp", "whcg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	runtimeDir := filepath.Join(home, "run")
	dataDir := filepath.Join(home, "data")
	env := append(os.Environ(), "HOME="+home, "XDG_RUNTIME_DIR="+runtimeDir, "XDG_DATA_HOME="+dataDir)
	checkout := filepath.Join(home, "checkout")
	if err := os.MkdirAll(checkout, 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(checkout)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	snapshot := state.Snapshot{
		Config:  state.ConfigV1{SnapshotVersion: 1, ProjectID: projectID, Handle: types.ProjectHandle{Namespace: "acme", Name: "wormhole"}},
		Project: state.ProjectV1{SchemaVersion: 1, Kind: "project", ID: projectID, Name: "Wormhole", Aliases: []string{}, CreatedAt: now, UpdatedAt: now, Extensions: state.ExtensionsV1{}},
		Actors:  map[string]state.Record[state.ActorV1]{}, Tasks: map[string]state.Record[state.TaskV1]{}, TaskLinks: map[string]state.Record[state.TaskLinkV1]{}, Articles: map[string]state.KBRecord{}, Channels: map[string]state.Record[state.ChannelV1]{}, Events: map[string]state.EventV1{}, GitLinks: map[string]state.Record[state.GitLinkV1]{},
	}
	tree, err := state.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := state.DecodeTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(dataDir, "wormhole", "wormholed.db")
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := localstore.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	binding := types.WorkspaceBinding{
		Scope:             types.WorkspaceScope{ProjectID: projectID, WorkspaceID: types.WorkspaceID(workspaceID)},
		Checkout:          types.CheckoutIdentity{CanonicalPath: checkout, Device: uint64(stat.Dev), Inode: uint64(stat.Ino)},
		AcceptedCommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", AcceptedTreeDigest: string(decoded.Digest),
	}
	if _, _, err := localstore.NewWorkspaceRepo(store.DB()).RegisterWorkspace(context.Background(), binding, tree); err != nil {
		t.Fatal(err)
	}
	identity, err := localidentity.Open(filepath.Join(dataDir, "wormhole", "identities"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identity.EnsureSelectedForSetup(context.Background(), journalID, types.ConfirmedIdentitySelection{DisplayName: "Local Owner"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	gatewayBinary := task4BuildGatewayBinary(t)
	if task4GatewayBinErr != nil {
		t.Fatal(task4GatewayBinErr)
	}
	socketPath := filepath.Join(runtimeDir, "wormhole", "wormholed.sock")
	startTask4ProcessDaemon(t, gatewayBinary, "ignored", env, socketPath)
	connection, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	reader := bufio.NewReader(connection)
	mcpInitialize(t, connection, reader)
	params, err := json.Marshal(map[string]any{
		"operation": "status", "project_id": projectID,
		"_wormhole_workspace": map[string]string{"working_directory": checkout},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(mcpRpcRequest{JSONRPC: "2.0", ID: json.RawMessage("7"), Method: "wormhole/code-graph/lifecycle", Params: params})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write(append(request, '\n')); err != nil {
		t.Fatal(err)
	}
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response mcpRpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(line), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Message != localapi.ErrCodeGraphUnavailable.Error() {
		t.Fatalf("configured lifecycle response = %s, want exact disabled-provider sentinel", line)
	}

	inspection, err := localstore.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer inspection.Close()
	for _, table := range []string{"codegraph_config", "codegraph_revisions", "codegraph_nodes", "codegraph_lifecycle"} {
		var count int
		if err := inspection.DB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("disabled provider started legacy runtime table %s", table)
		}
	}
	if strings.Contains(string(line), "credential") {
		t.Fatalf("disabled response exposed ambient credential path: %s", line)
	}
}
