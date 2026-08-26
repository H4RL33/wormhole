package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
