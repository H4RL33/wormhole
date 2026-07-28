package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const approvedRequestBody = `{"model":"embed-v4.0","texts":["dynamic onboarding text"],"input_type":"search_document","embedding_types":["float"],"output_dimension":1024,"max_tokens":8192,"truncate":"NONE"}`

func TestCohereHandlerEchoesApprovedEmbeddingContract(t *testing.T) {
	countPath := filepath.Join(t.TempDir(), "request-count")
	request := approvedCohereRequest(approvedRequestBody)
	response := httptest.NewRecorder()

	newCohereMock("release-image-smoke", countPath).handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("Cohere mock status = %d, want %d", response.Code, http.StatusOK)
	}
	var decoded struct {
		Texts      []string `json:"texts"`
		Embeddings struct {
			Float [][]float32 `json:"float"`
		} `json:"embeddings"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode Cohere mock response: %v", err)
	}
	if len(decoded.Texts) != 1 || decoded.Texts[0] != "dynamic onboarding text" {
		t.Fatalf("response texts = %q, want dynamic request text", decoded.Texts)
	}
	if len(decoded.Embeddings.Float) != 1 || len(decoded.Embeddings.Float[0]) != embeddingSize || decoded.Embeddings.Float[0][0] != 1 {
		t.Fatalf("response embedding shape/value is invalid")
	}
	assertRequestCount(t, countPath, "1")
}

func TestCohereHandlerRejectsInvalidRequestsAndCountsEveryAttempt(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		path        string
		authorize   string
		contentType string
		accept      string
		body        string
		want        int
	}{
		{name: "path", method: http.MethodPost, path: "/embed", authorize: "Bearer release-image-smoke", contentType: "application/json", accept: "application/json", body: approvedRequestBody, want: http.StatusNotFound},
		{name: "method", method: http.MethodGet, path: "/v1/embed", authorize: "Bearer release-image-smoke", contentType: "application/json", accept: "application/json", body: approvedRequestBody, want: http.StatusMethodNotAllowed},
		{name: "authorization", method: http.MethodPost, path: "/v1/embed", authorize: "Bearer wrong", contentType: "application/json", accept: "application/json", body: approvedRequestBody, want: http.StatusUnauthorized},
		{name: "content type", method: http.MethodPost, path: "/v1/embed", authorize: "Bearer release-image-smoke", contentType: "text/plain", accept: "application/json", body: approvedRequestBody, want: http.StatusUnsupportedMediaType},
		{name: "accept", method: http.MethodPost, path: "/v1/embed", authorize: "Bearer release-image-smoke", contentType: "application/json", accept: "text/plain", body: approvedRequestBody, want: http.StatusNotAcceptable},
		{name: "malformed", method: http.MethodPost, path: "/v1/embed", authorize: "Bearer release-image-smoke", contentType: "application/json", accept: "application/json", body: `{`, want: http.StatusBadRequest},
		{name: "trailing", method: http.MethodPost, path: "/v1/embed", authorize: "Bearer release-image-smoke", contentType: "application/json", accept: "application/json", body: approvedRequestBody + `{}`, want: http.StatusBadRequest},
		{name: "unknown field", method: http.MethodPost, path: "/v1/embed", authorize: "Bearer release-image-smoke", contentType: "application/json", accept: "application/json", body: strings.Replace(approvedRequestBody, `{`, `{"unknown":true,`, 1), want: http.StatusBadRequest},
		{name: "model", method: http.MethodPost, path: "/v1/embed", authorize: "Bearer release-image-smoke", contentType: "application/json", accept: "application/json", body: strings.Replace(approvedRequestBody, "embed-v4.0", "other", 1), want: http.StatusBadRequest},
		{name: "input type", method: http.MethodPost, path: "/v1/embed", authorize: "Bearer release-image-smoke", contentType: "application/json", accept: "application/json", body: strings.Replace(approvedRequestBody, "search_document", "search_query", 1), want: http.StatusBadRequest},
		{name: "empty texts", method: http.MethodPost, path: "/v1/embed", authorize: "Bearer release-image-smoke", contentType: "application/json", accept: "application/json", body: strings.Replace(approvedRequestBody, `["dynamic onboarding text"]`, `[]`, 1), want: http.StatusBadRequest},
		{name: "embedding type", method: http.MethodPost, path: "/v1/embed", authorize: "Bearer release-image-smoke", contentType: "application/json", accept: "application/json", body: strings.Replace(approvedRequestBody, `["float"]`, `["int8"]`, 1), want: http.StatusBadRequest},
		{name: "dimension", method: http.MethodPost, path: "/v1/embed", authorize: "Bearer release-image-smoke", contentType: "application/json", accept: "application/json", body: strings.Replace(approvedRequestBody, `"output_dimension":1024`, `"output_dimension":512`, 1), want: http.StatusBadRequest},
		{name: "max tokens", method: http.MethodPost, path: "/v1/embed", authorize: "Bearer release-image-smoke", contentType: "application/json", accept: "application/json", body: strings.Replace(approvedRequestBody, `"max_tokens":8192`, `"max_tokens":4096`, 1), want: http.StatusBadRequest},
		{name: "truncate", method: http.MethodPost, path: "/v1/embed", authorize: "Bearer release-image-smoke", contentType: "application/json", accept: "application/json", body: strings.Replace(approvedRequestBody, `"truncate":"NONE"`, `"truncate":"END"`, 1), want: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			countPath := filepath.Join(t.TempDir(), "request-count")
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("Authorization", test.authorize)
			request.Header.Set("Content-Type", test.contentType)
			request.Header.Set("Accept", test.accept)
			response := httptest.NewRecorder()

			newCohereMock("release-image-smoke", countPath).handler().ServeHTTP(response, request)

			if response.Code != test.want {
				t.Fatalf("status = %d, want %d", response.Code, test.want)
			}
			assertRequestCount(t, countPath, "1")
		})
	}
}

func TestCohereHandlerAcceptsExactlyOneConcurrentRequest(t *testing.T) {
	countPath := filepath.Join(t.TempDir(), "request-count")
	handler := newCohereMock("release-image-smoke", countPath).handler()
	const calls = 16
	statuses := make(chan int, calls)
	var wait sync.WaitGroup
	for range calls {
		wait.Add(1)
		go func() {
			defer wait.Done()
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, approvedCohereRequest(approvedRequestBody))
			statuses <- response.Code
		}()
	}
	wait.Wait()
	close(statuses)
	ok, conflicts := 0, 0
	for status := range statuses {
		switch status {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflicts++
		default:
			t.Fatalf("unexpected concurrent status %d", status)
		}
	}
	if ok != 1 || conflicts != calls-1 {
		t.Fatalf("statuses: ok=%d conflicts=%d, want 1/%d", ok, conflicts, calls-1)
	}
	assertRequestCount(t, countPath, "16")
}

func TestCohereHandlerDoesNotLogRequestContentOrCredential(t *testing.T) {
	var output bytes.Buffer
	prior := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(prior) })
	request := approvedCohereRequest(approvedRequestBody)
	response := httptest.NewRecorder()
	newCohereMock("release-image-smoke", filepath.Join(t.TempDir(), "request-count")).handler().ServeHTTP(response, request)
	for _, secret := range []string{"release-image-smoke", "dynamic onboarding text", "embedding"} {
		if strings.Contains(output.String(), secret) {
			t.Fatalf("logs leaked %q: %q", secret, output.String())
		}
	}
}

func TestGeneratedTLSMaterialAuthenticatesOnlyCohereHost(t *testing.T) {
	now := time.Now()
	certificate, caPEM, err := generateTLSMaterial(now)
	if err != nil {
		t.Fatalf("generate TLS material: %v", err)
	}
	if bytes.Contains(caPEM, []byte("PRIVATE KEY")) {
		t.Fatal("published CA contains private key material")
	}
	block, _ := pemDecode(caPEM)
	if block == nil {
		t.Fatal("published CA is not PEM")
	}
	ca, err := x509.ParseCertificate(block)
	if err != nil {
		t.Fatalf("parse CA: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: cohereHost, Roots: roots, CurrentTime: now}); err != nil {
		t.Fatalf("verify Cohere identity: %v", err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "example.com", Roots: roots, CurrentTime: now}); err == nil {
		t.Fatal("leaf certificate authenticated an unrelated host")
	}
}

func TestProbeHealthRequiresNoContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	if err := probeHealth(context.Background(), server.URL); err != nil {
		t.Fatalf("probe 204 health endpoint: %v", err)
	}

	notHealthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer notHealthy.Close()
	if err := probeHealth(context.Background(), notHealthy.URL); err == nil {
		t.Fatal("probe accepted non-204 health response")
	}
}

func approvedCohereRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/v1/embed", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer release-image-smoke")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	return request
}

func assertRequestCount(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read request count: %v", err)
	}
	if strings.TrimSpace(string(data)) != want {
		t.Fatalf("request count = %q, want %q", data, want)
	}
}

func pemDecode(data []byte) ([]byte, []byte) {
	block, rest := pem.Decode(data)
	if block == nil {
		return nil, rest
	}
	return block.Bytes, rest
}
