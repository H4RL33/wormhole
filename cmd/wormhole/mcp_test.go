package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrivateContextBridgeOverwritesForgedCWD(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wormhole.task.list","arguments":{"_wormhole_workspace":{"working_directory":"/forged"},"status":"todo"}}}`)

	got, err := attachPrivateRequestContext(raw, nested)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got, []byte("/forged")) {
		t.Fatalf("forged cwd survived bridge rewrite: %s", got)
	}
	var envelope struct {
		Params struct {
			Arguments map[string]json.RawMessage `json:"arguments"`
		} `json:"params"`
	}
	if err := json.Unmarshal(got, &envelope); err != nil {
		t.Fatal(err)
	}
	var privateContext struct {
		WorkingDirectory string `json:"working_directory"`
	}
	if err := json.Unmarshal(envelope.Params.Arguments["_wormhole_workspace"], &privateContext); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(nested)
	if err != nil {
		t.Fatal(err)
	}
	if privateContext.WorkingDirectory != want {
		t.Fatalf("working_directory = %q, want %q", privateContext.WorkingDirectory, want)
	}
}

func TestPrivateContextBridgeLeavesNonToolRequestsUnchanged(t *testing.T) {
	raw := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	got, err := attachPrivateRequestContext(raw, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("initialize changed: got %s want %s", got, raw)
	}
}

func TestPrivateContextBridgeDoesNotResolveCWDForInitialize(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(gone, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(gone); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	forwarded := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(server).ReadString('\n')
		forwarded <- line
	}()
	raw := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	if err := stdinToSocket(strings.NewReader(raw+"\n"), client); err != io.EOF {
		t.Fatalf("stdinToSocket initialize error = %v, want io.EOF", err)
	}
	if got := <-forwarded; got != raw+"\n" {
		t.Fatalf("forwarded initialize = %q, want %q", got, raw+"\n")
	}
}

func TestPrivateContextBridgeRejectsDuplicateMembersBeforeRewrite(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	forwarded := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(server).ReadString('\n')
		forwarded <- line
	}()
	raw := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wormhole.task.list","arguments":{"_wormhole_workspace":{"working_directory":"/first"},"_wormhole_workspace":{"working_directory":"/second"}}}}`
	err := stdinToSocket(strings.NewReader(raw+"\n"), client)
	if err == nil || !strings.Contains(err.Error(), "duplicate object member") {
		t.Fatalf("stdinToSocket duplicate error = %v", err)
	}
	_ = client.Close()
	if got := <-forwarded; got != "" {
		t.Fatalf("duplicate request was forwarded: %q", got)
	}
}

func TestPrivateContextBridgeForwardsMalformedFrameForGatewayParseResponse(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	if err := server.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- stdinToSocket(strings.NewReader("not-json\n"), client) }()
	line, err := bufio.NewReader(server).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if line != "not-json\n" {
		t.Fatalf("forwarded malformed frame = %q", line)
	}
	if err := <-done; !errors.Is(err, io.EOF) {
		t.Fatalf("stdinToSocket error = %v, want io.EOF", err)
	}
}
