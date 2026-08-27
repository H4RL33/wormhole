package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteToolWrappersPropagateRPCFailuresAndReturnDecodedResults(t *testing.T) {
	errorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("1"), Error: &rpcError{Code: -32000, Message: "rejected"}})
	}))
	t.Cleanup(errorServer.Close)

	failedCalls := []func() error{
		func() error {
			_, err := doSearch(errorServer.Client(), errorServer.URL, "project", "token", "query", 1)
			return err
		},
		func() error {
			_, err := doListChannels(errorServer.Client(), errorServer.URL, "project", "token")
			return err
		},
		func() error {
			_, err := doPostEvent(errorServer.Client(), errorServer.URL, "project", "token", "channel", "posted", nil, nil)
			return err
		},
		func() error {
			_, err := doListTasks(errorServer.Client(), errorServer.URL, "project", "token")
			return err
		},
	}
	for index, call := range failedCalls {
		if err := call(); err == nil || !strings.Contains(err.Error(), "rejected") {
			t.Fatalf("failed wrapper call %d error = %v", index, err)
		}
	}

	successServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		result, _ := json.Marshal(toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: `{}`}}})
		_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("1"), Result: result})
	}))
	t.Cleanup(successServer.Close)
	successCalls := []func() error{
		func() error {
			_, err := doSearch(successServer.Client(), successServer.URL, "project", "token", "query", 1)
			return err
		},
		func() error {
			_, err := doListChannels(successServer.Client(), successServer.URL, "project", "token")
			return err
		},
		func() error {
			_, err := doPostEvent(successServer.Client(), successServer.URL, "project", "token", "channel", "posted", nil, nil)
			return err
		},
		func() error {
			_, err := doListTasks(successServer.Client(), successServer.URL, "project", "token")
			return err
		},
	}
	for index, call := range successCalls {
		if err := call(); err != nil {
			t.Fatalf("successful wrapper call %d: %v", index, err)
		}
	}
	if _, err := callTool(successServer.Client(), successServer.URL, "wormhole.test", "project", "", nil); err != nil {
		t.Fatalf("callTool nil arguments: %v", err)
	}
}

func TestCredentialWriteAndViewerKeyTransportBoundaries(t *testing.T) {
	credentialPath := filepath.Join(t.TempDir(), "nested", "credentials.json")
	if err := writeCredentials(credentialPath, credentials{}); err != nil {
		t.Fatalf("writeCredentials: %v", err)
	}

	for _, test := range []struct {
		name   string
		server string
		want   string
	}{
		{name: "invalid request URL", server: ":", want: "missing protocol scheme"},
		{name: "transport failure", server: "http://127.0.0.1:0", want: "connect"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runViewerKeyCreate([]string{"--server", test.server, "--project", "project", "--label", "viewer", "--admin-key", "admin"}, &stdout, &stderr)
			if code != 1 || !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("runViewerKeyCreate code=%d stderr=%q, want %q", code, stderr.String(), test.want)
			}
		})
	}
}
