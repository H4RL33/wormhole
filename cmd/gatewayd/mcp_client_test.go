package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
)

func gatewayTestBootstrapOutput(projectID, agentID, passportID string) map[string]any {
	now := time.Date(2026, 7, 25, 12, 0, 0, 123, time.UTC)
	org := types.BootstrapOrgConfigV1{
		SchemaVersion: types.BootstrapSchemaVersionV1,
		Project:       types.BootstrapProjectV1{ID: projectID, Name: "project", Owner: "owner", CreatedAt: now},
		Identity: types.BootstrapIdentityV1{
			Agent:       types.BootstrapAgentV1{ID: agentID, Owner: "owner", Model: "model", Capabilities: []string{}, CreatedAt: now},
			Passport:    types.BootstrapPassportV1{ID: passportID, AgentID: agentID, ProjectID: projectID, Repositories: []string{}, Roles: []string{}, IssuedAt: now},
			Permissions: []string{"task.create", "read_kb"},
		},
		Channels: []types.BootstrapChannelV1{}, Events: []types.BootstrapEventV1{}, Tasks: []types.BootstrapTaskV1{},
		KB: types.BootstrapKBV1{Articles: []types.BootstrapArticleV1{}}, IntegrationManifestMetadata: json.RawMessage(`null`),
	}
	return map[string]any{"org_config": org, "project_list": []string{}, "task_list": org.Tasks, "kb_list": org.KB.Articles, "timestamp": now.Format(time.RFC3339Nano), "version": 1}
}

type gateBMCPClient struct {
	connection net.Conn
	reader     *bufio.Reader
	nextID     int
}

func gateBDialMCPClient(t *testing.T, socketPath string) *gateBMCPClient {
	t.Helper()
	connection, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	client := &gateBMCPClient{connection: connection, reader: bufio.NewReader(connection), nextID: 10}
	mcpInitialize(t, connection, client.reader)
	return client
}

func (client *gateBMCPClient) Close() { _ = client.connection.Close() }

func (client *gateBMCPClient) mustCall(t *testing.T, tool string, args map[string]interface{}) json.RawMessage {
	t.Helper()
	result, err := client.call(tool, args)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("%s: %s", tool, result.Error)
	}
	return result.Result
}

func (client *gateBMCPClient) call(tool string, args map[string]interface{}) (mcpToolResponse, error) {
	return client.callAfterWrite(tool, args, nil)
}

func (client *gateBMCPClient) callAfterWrite(tool string, args map[string]interface{}, afterWrite func()) (mcpToolResponse, error) {
	client.nextID++
	argumentBytes, err := json.Marshal(args)
	if err != nil {
		return mcpToolResponse{}, err
	}
	paramsBytes, err := json.Marshal(mcpToolsCallParams{Name: tool, Arguments: argumentBytes})
	if err != nil {
		return mcpToolResponse{}, err
	}
	idBytes, err := json.Marshal(client.nextID)
	if err != nil {
		return mcpToolResponse{}, err
	}
	requestBytes, err := json.Marshal(mcpRpcRequest{JSONRPC: "2.0", ID: idBytes, Method: "tools/call", Params: paramsBytes})
	if err != nil {
		return mcpToolResponse{}, err
	}
	if err := client.connection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return mcpToolResponse{}, err
	}
	defer client.connection.SetDeadline(time.Time{})
	if _, err := client.connection.Write(append(requestBytes, '\n')); err != nil {
		return mcpToolResponse{}, err
	}
	if afterWrite != nil {
		afterWrite()
	}
	line, err := client.reader.ReadBytes('\n')
	if err != nil {
		return mcpToolResponse{}, err
	}
	var response mcpRpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(line), &response); err != nil {
		return mcpToolResponse{}, err
	}
	if response.Error != nil {
		return mcpToolResponse{Error: response.Error.Message}, nil
	}
	var result mcpToolCallResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		return mcpToolResponse{}, err
	}
	if result.IsError {
		if len(result.Content) == 0 {
			return mcpToolResponse{Error: "tool failed without content"}, nil
		}
		return mcpToolResponse{Error: result.Content[0].Text}, nil
	}
	if len(result.Content) == 0 {
		return mcpToolResponse{}, errors.New("tool returned no content")
	}
	return mcpToolResponse{Result: json.RawMessage(result.Content[0].Text)}, nil
}
