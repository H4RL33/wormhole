package main

import (
	"bufio"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

func gatewayTransportSocket(t *testing.T, initializeResponse, callResponse string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wormholed.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		if _, err := reader.ReadBytes('\n'); err != nil {
			return
		}
		if initializeResponse != "" {
			_, _ = conn.Write(append([]byte(initializeResponse), '\n'))
		}
		if callResponse == "" {
			return
		}
		if _, err := reader.ReadBytes('\n'); err != nil { // initialized notification
			return
		}
		if _, err := reader.ReadBytes('\n'); err != nil { // tools/call request
			return
		}
		if callResponse != "close-after-call" {
			_, _ = conn.Write(append([]byte(callResponse), '\n'))
		}
	}()
	return path
}

func toolRPCResponse(t *testing.T, content string, isError bool) string {
	t.Helper()
	result, err := json.Marshal(toolCallResult{
		Content: []toolCallResultContent{{Type: "text", Text: content}},
		IsError: isError,
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("2"), Result: result})
	if err != nil {
		t.Fatal(err)
	}
	return string(response)
}

func TestCallGatewayToolValidatesEveryGatewayResponseBoundary(t *testing.T) {
	okInit := `{"jsonrpc":"2.0","id":1,"result":{}}`
	tests := []struct {
		name string
		init string
		call string
		want string
	}{
		{"initialize read", "", "", "read Gateway initialize response"},
		{"initialize malformed", "not-json", "", "decode Gateway initialize response"},
		{"initialize rpc error", `{"jsonrpc":"2.0","id":1,"error":{"code":-1,"message":"not ready"}}`, "", "Gateway initialize: not ready"},
		{"call read", okInit, "close-after-call", "read wormhole.test response"},
		{"call malformed", okInit, "not-json", "decode wormhole.test response"},
		{"call rpc error", okInit, `{"jsonrpc":"2.0","id":2,"error":{"code":-1,"message":"denied"}}`, "denied"},
		{"tool result malformed", okInit, `{"jsonrpc":"2.0","id":2,"result":"bad"}`, "decode wormhole.test result"},
		{"tool result empty", okInit, `{"jsonrpc":"2.0","id":2,"result":{"content":[]}}`, "empty tool result content"},
		{"tool result error", okInit, toolRPCResponse(t, "tool denied", true), "tool denied"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := gatewayTransportSocket(t, tt.init, tt.call)
			_, err := callGatewayTool(path, "wormhole.test", map[string]string{"project_id": "project-a"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("callGatewayTool error = %v, want containing %q", err, tt.want)
			}
		})
	}

	path := gatewayTransportSocket(t, okInit, toolRPCResponse(t, `{"state":"online"}`, false))
	raw, err := callGatewayTool(path, "wormhole.test", map[string]string{"project_id": "project-a"})
	if err != nil || string(raw) != `{"state":"online"}` {
		t.Fatalf("successful tool result = %s, err=%v", raw, err)
	}
	if _, err := callGatewayTool(filepath.Join(t.TempDir(), "missing.sock"), "wormhole.test", nil); err == nil || !strings.Contains(err.Error(), "gatewayd not running") {
		t.Fatalf("unreachable Gateway error = %v", err)
	}
}
