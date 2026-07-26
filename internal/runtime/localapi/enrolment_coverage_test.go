package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCallFabricEnrolmentValidatesEveryRPCResponseBoundary(t *testing.T) {
	tests := []struct {
		name     string
		response func(http.ResponseWriter)
		want     string
	}{
		{"malformed rpc", func(w http.ResponseWriter) { _, _ = w.Write([]byte("not-json")) }, "decode Fabric enrolment response"},
		{"rpc error", func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-1,"message":"denied"}}`))
		}, "denied"},
		{"malformed tool result", func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"bad"}`)) }, "decode Fabric enrolment tool result"},
		{"empty tool result", func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`))
		}, "empty Fabric enrolment result"},
		{"tool error", func(w http.ResponseWriter) { writeFabricCoverageToolResult(t, w, "policy denied", true) }, "policy denied"},
		{"malformed output", func(w http.ResponseWriter) { writeFabricCoverageToolResult(t, w, "not-json", false) }, "decode Fabric enrolment output"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { tt.response(w) }))
			defer server.Close()
			req := validEnrolmentRequest()
			req.FabricAddress = server.URL
			srv := &Server{httpClient: server.Client()}
			if _, err := srv.callFabricEnrolment(context.Background(), req, strings.Repeat("a", 64), false); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("callFabricEnrolment error = %v, want containing %q", err, tt.want)
			}
		})
	}

	req := validEnrolmentRequest()
	req.FabricAddress = "://bad"
	if _, err := (&Server{httpClient: http.DefaultClient}).callFabricEnrolment(context.Background(), req, strings.Repeat("a", 64), false); err == nil || !strings.Contains(err.Error(), "build Fabric enrolment request") {
		t.Fatalf("invalid Fabric address error = %v", err)
	}
}

func writeFabricCoverageToolResult(t *testing.T, w http.ResponseWriter, content string, isError bool) {
	t.Helper()
	result, err := json.Marshal(toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: content}}, IsError: isError})
	if err != nil {
		t.Fatal(err)
	}
	response, err := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("1"), Result: result})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(response)
}

func TestFabricEnrolmentFailureClassificationIsStableAndNonSecret(t *testing.T) {
	req := validEnrolmentRequest()
	for _, test := range []struct {
		err  error
		code EnrolmentResultCode
	}{
		{errors.New("idempotency conflict for secret-token"), EnrolmentDuplicateIdentity},
		{errors.New("invalid scope for secret-token"), EnrolmentInvalidProject},
		{errors.New("violates foreign key secret-token"), EnrolmentInvalidProject},
		{errors.New("network reset secret-token"), EnrolmentFabricUnreachable},
	} {
		result := classifyFabricEnrolmentFailure(req, test.err)
		if result.Code != test.code || strings.Contains(result.Message, "secret-token") {
			t.Fatalf("classification for %v = %+v", test.err, result)
		}
	}
	state, retryable, ok := EnrolmentResultContract(EnrolmentCheckpointPersistenceFailed)
	if !ok || state != EnrolmentAttentionRequired || retryable {
		t.Fatalf("checkpoint result contract = %q/%t/%t", state, retryable, ok)
	}
}
