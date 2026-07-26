package kb

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var _ Embedder = (*CohereEmbedder)(nil)

func TestProductionEmbeddingContractUsesCompileTimeConstants(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "embedding_provider.go", nil, 0)
	if err != nil {
		t.Fatalf("parse embedding_provider.go: %v", err)
	}
	wantConstants := map[string]bool{
		"cohereEmbedEndpoint":        false,
		"approvedEmbeddingProvider":  false,
		"approvedEmbeddingModel":     false,
		"approvedEmbeddingVersion":   false,
		"approvedEmbeddingDimension": false,
	}
	for _, declaration := range parsed.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			continue
		}
		for _, rawSpec := range general.Specs {
			spec, ok := rawSpec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range spec.Names {
				if _, required := wantConstants[name.Name]; required {
					wantConstants[name.Name] = true
				}
			}
		}
	}
	for name, found := range wantConstants {
		if !found {
			t.Errorf("%s is not a compile-time constant", name)
		}
	}
}

func TestCohereEmbeddingDescriptorAndArticleInput(t *testing.T) {
	if got := ApprovedEmbeddingDescriptor(); got != (EmbeddingDescriptor{
		Provider: "cohere", Model: "embed-v4.0", Version: "4.0", Dimension: 1024,
	}) {
		t.Fatalf("approved descriptor = %+v", got)
	}
	if got, want := articleEmbeddingText("Deployment", "Use the release workflow."), "Deployment\n\nUse the release workflow."; got != want {
		t.Fatalf("article embedding input = %q, want %q", got, want)
	}
	if _, err := NewCohereEmbedder(""); !errors.Is(err, ErrEmbeddingConfiguration) {
		t.Fatalf("empty API key error = %v, want ErrEmbeddingConfiguration", err)
	}
}

func TestCohereEmbedderSendsApprovedQueryContract(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embed" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		writeEmbeddingResponse(t, w, []string{"release safeguards"}, [][]float32{testVector(0.25)})
	}))
	defer server.Close()

	embedder := newTestCohereEmbedder(t, server.URL+"/v1/embed")
	vectors, err := embedder.Embed(context.Background(), EmbeddingRequest{
		InputType: EmbeddingInputSearchQuery,
		Texts:     []string{"release safeguards"},
		Mode:      EmbeddingModeInteractive,
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vectors) != 1 || len(vectors[0]) != 1024 || vectors[0][0] != 0.25 {
		t.Fatalf("vectors shape/value = %d/%d/%v", len(vectors), len(vectors[0]), vectors[0][0])
	}
	wantBody := map[string]any{
		"model":            "embed-v4.0",
		"texts":            []any{"release safeguards"},
		"input_type":       "search_query",
		"embedding_types":  []any{"float"},
		"output_dimension": float64(1024),
		"max_tokens":       float64(8192),
		"truncate":         "NONE",
	}
	if !reflect.DeepEqual(requestBody, wantBody) {
		t.Fatalf("request body = %#v, want %#v", requestBody, wantBody)
	}
}

func TestCohereEmbedderBatchesArticleDocumentsAndPreservesOrder(t *testing.T) {
	texts := []string{
		articleEmbeddingText("First", "One"),
		articleEmbeddingText("Second", "Two"),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Texts     []string `json:"texts"`
			InputType string   `json:"input_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(body.Texts, texts) || body.InputType != "search_document" {
			t.Fatalf("document batch = %+v/%q", body.Texts, body.InputType)
		}
		writeEmbeddingResponse(t, w, texts, [][]float32{testVector(0.1), testVector(0.2)})
	}))
	defer server.Close()
	embedder := newTestCohereEmbedder(t, server.URL)
	vectors, err := embedder.Embed(context.Background(), EmbeddingRequest{
		InputType: EmbeddingInputSearchDocument, Texts: texts, Mode: EmbeddingModeReembedding,
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vectors) != 2 || vectors[0][0] != 0.1 || vectors[1][0] != 0.2 {
		t.Fatalf("ordered vectors = %#v", []float32{vectors[0][0], vectors[1][0]})
	}
}

func TestCohereEmbedderRejectsResponseOrderMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeEmbeddingResponse(t, w, []string{"second", "first"}, [][]float32{testVector(1), testVector(2)})
	}))
	defer server.Close()
	embedder := newTestCohereEmbedder(t, server.URL)
	_, err := embedder.Embed(context.Background(), EmbeddingRequest{
		InputType: EmbeddingInputSearchDocument, Texts: []string{"first", "second"}, Mode: EmbeddingModeInteractive,
	})
	if !errors.Is(err, ErrEmbeddingContract) {
		t.Fatalf("Embed error = %v, want ErrEmbeddingContract", err)
	}
	if strings.Contains(err.Error(), "first") || strings.Contains(err.Error(), "second") {
		t.Fatalf("provider error exposed input text: %v", err)
	}
}

func TestCohereEmbedderValidatesLimitsAndProviderContract(t *testing.T) {
	embedder := newTestCohereEmbedder(t, "http://127.0.0.1:1/v1/embed")
	tests := []struct {
		name string
		req  EmbeddingRequest
	}{
		{"empty batch", EmbeddingRequest{InputType: EmbeddingInputSearchDocument, Mode: EmbeddingModeInteractive}},
		{"too many", EmbeddingRequest{InputType: EmbeddingInputSearchDocument, Texts: make([]string, 65), Mode: EmbeddingModeInteractive}},
		{"input too large", EmbeddingRequest{InputType: EmbeddingInputSearchDocument, Texts: []string{strings.Repeat("x", 32*1024+1)}, Mode: EmbeddingModeInteractive}},
		{"query too long", EmbeddingRequest{InputType: EmbeddingInputSearchQuery, Texts: []string{strings.Repeat("q", 2001)}, Mode: EmbeddingModeInteractive}},
		{"aggregate too large", EmbeddingRequest{InputType: EmbeddingInputSearchDocument, Texts: repeatedEmbeddingInputs(33, 32*1024), Mode: EmbeddingModeReembedding}},
		{"invalid type", EmbeddingRequest{InputType: "classification", Texts: []string{"x"}, Mode: EmbeddingModeInteractive}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := embedder.Embed(context.Background(), test.req); !errors.Is(err, ErrEmbeddingInput) {
				t.Fatalf("Embed error = %v, want ErrEmbeddingInput", err)
			}
		})
	}

	if err := validateEmbeddingBatch([]string{"a"}, [][]float32{{1}}, 1024); !errors.Is(err, ErrEmbeddingDimension) {
		t.Fatalf("dimension error = %v", err)
	}
	if err := validateEmbeddingBatch([]string{"a"}, [][]float32{testVector(float32(math.NaN()))}, 1024); !errors.Is(err, ErrEmbeddingContract) {
		t.Fatalf("finite-value error = %v", err)
	}
	if err := validateEmbeddingBatch([]string{"a"}, [][]float32{make([]float32, 1024)}, 1024); !errors.Is(err, ErrEmbeddingContract) || !strings.Contains(err.Error(), "zero_norm_vector") {
		t.Fatalf("zero-norm error = %v, want zero_norm_vector contract failure", err)
	}
	if err := validateEmbeddingBatch([]string{"a", "b"}, [][]float32{testVector(1)}, 1024); !errors.Is(err, ErrEmbeddingContract) {
		t.Fatalf("count/order error = %v", err)
	}
}

func repeatedEmbeddingInputs(count, size int) []string {
	texts := make([]string, count)
	for i := range texts {
		texts[i] = strings.Repeat("x", size)
	}
	return texts
}

func TestCohereEmbedderRetriesOnlyApprovedFailures(t *testing.T) {
	t.Run("retries service unavailable", func(t *testing.T) {
		var attempts atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if attempts.Add(1) == 1 {
				w.Header().Set("Retry-After", "0")
				http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
				return
			}
			writeEmbeddingResponse(t, w, []string{"query"}, [][]float32{testVector(0.5)})
		}))
		defer server.Close()
		embedder := newTestCohereEmbedder(t, server.URL)
		if _, err := embedder.Embed(context.Background(), EmbeddingRequest{InputType: EmbeddingInputSearchQuery, Texts: []string{"query"}, Mode: EmbeddingModeInteractive}); err != nil {
			t.Fatalf("Embed: %v", err)
		}
		if attempts.Load() != 2 {
			t.Fatalf("attempts = %d, want 2", attempts.Load())
		}
	})

	t.Run("does not retry authentication rejection", func(t *testing.T) {
		var attempts atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts.Add(1)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}))
		defer server.Close()
		embedder := newTestCohereEmbedder(t, server.URL)
		_, err := embedder.Embed(context.Background(), EmbeddingRequest{InputType: EmbeddingInputSearchQuery, Texts: []string{"query"}, Mode: EmbeddingModeInteractive})
		if !errors.Is(err, ErrEmbeddingProvider) {
			t.Fatalf("Embed error = %v, want ErrEmbeddingProvider", err)
		}
		if attempts.Load() != 1 {
			t.Fatalf("attempts = %d, want 1", attempts.Load())
		}
	})
}

func TestEmbeddingOperationBudgets(t *testing.T) {
	interactive := embeddingAttemptPolicy(EmbeddingModeInteractive)
	if interactive.TotalBudget != 5*time.Second || interactive.RequestTimeout != 2*time.Second || interactive.MaxAttempts != 2 {
		t.Fatalf("interactive policy = %+v", interactive)
	}
	rebuild := embeddingAttemptPolicy(EmbeddingModeReembedding)
	if rebuild.TotalBudget != 30*time.Second || rebuild.RequestTimeout != 2*time.Second || rebuild.MaxAttempts != 3 {
		t.Fatalf("reembedding policy = %+v", rebuild)
	}
}

func TestProviderResponseFixtureCoversRebuildAndLiveGate(t *testing.T) {
	data, err := os.ReadFile("../../../testdata/alpha/kb/provider-responses.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		ResponseCases []struct {
			Name                      string `json:"name"`
			Mode                      string `json:"mode"`
			MaxBatchCount             int    `json:"max_batch_count"`
			DatabaseTransactionDuring bool   `json:"database_transaction_during_request"`
			CandidateState            string `json:"candidate_state"`
			PreviousGenerationState   string `json:"previous_generation_state"`
		} `json:"response_cases"`
		LiveAcceptance struct {
			EnabledBy     string `json:"enabled_by"`
			CredentialEnv string `json:"credential_env"`
			Default       string `json:"default"`
		} `json:"live_acceptance"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	cases := make(map[string]struct {
		mode, candidate, previous string
		batch                     int
		inTx                      bool
	})
	for _, testCase := range fixture.ResponseCases {
		cases[testCase.Name] = struct {
			mode, candidate, previous string
			batch                     int
			inTx                      bool
		}{testCase.Mode, testCase.CandidateState, testCase.PreviousGenerationState, testCase.MaxBatchCount, testCase.DatabaseTransactionDuring}
	}
	rebuild := cases["reembedding_batch"]
	if rebuild.mode != "reembedding" || rebuild.batch != embeddingRebuildBatchSize || rebuild.inTx {
		t.Fatalf("reembedding fixture = %+v", rebuild)
	}
	failure := cases["failed_candidate_preserves_active"]
	if failure.candidate != "failed" || failure.previous != "active" {
		t.Fatalf("failed-candidate fixture = %+v", failure)
	}
	if fixture.LiveAcceptance.EnabledBy != "WORMHOLE_COHERE_LIVE_TEST=1" || fixture.LiveAcceptance.CredentialEnv != "WORMHOLE_COHERE_API_KEY" || fixture.LiveAcceptance.Default != "skipped" {
		t.Fatalf("live acceptance fixture = %+v", fixture.LiveAcceptance)
	}
}

func TestCohereLiveLowOverlapRankingOptIn(t *testing.T) {
	if os.Getenv("WORMHOLE_COHERE_LIVE_TEST") != "1" {
		t.Skip("set WORMHOLE_COHERE_LIVE_TEST=1 to run the paid Cohere acceptance test")
	}
	apiKey := os.Getenv("WORMHOLE_COHERE_API_KEY")
	if apiKey == "" {
		t.Fatal("WORMHOLE_COHERE_LIVE_TEST=1 requires WORMHOLE_COHERE_API_KEY")
	}
	embedder, err := NewCohereEmbedder(apiKey)
	if err != nil {
		t.Fatalf("configure live Cohere embedder: %v", err)
	}
	_, fixture := fixtureEmbedder(t, "unused")
	for _, testCase := range fixture.Cases {
		documents, err := embedder.Embed(context.Background(), EmbeddingRequest{
			InputType: EmbeddingInputSearchDocument,
			Texts: []string{
				articleEmbeddingText(testCase.Related.Title, testCase.Related.Body),
				articleEmbeddingText(testCase.LexicalDecoy.Title, testCase.LexicalDecoy.Body),
			},
			Mode: EmbeddingModeInteractive,
		})
		if err != nil {
			t.Fatalf("live Cohere documents for case %s: %v", testCase.Name, err)
		}
		queries, err := embedder.Embed(context.Background(), EmbeddingRequest{
			InputType: EmbeddingInputSearchQuery, Texts: []string{testCase.Query}, Mode: EmbeddingModeInteractive,
		})
		if err != nil {
			t.Fatalf("live Cohere query for case %s: %v", testCase.Name, err)
		}
		related := cosineSimilarity(queries[0], documents[0])
		decoy := cosineSimilarity(queries[0], documents[1])
		if related <= decoy {
			t.Fatalf("live Cohere case %s ranked decoy first (related=%f decoy=%f)", testCase.Name, related, decoy)
		}
	}
}

func cosineSimilarity(left, right []float32) float64 {
	var dot, leftNorm, rightNorm float64
	for i := range left {
		a, b := float64(left[i]), float64(right[i])
		dot += a * b
		leftNorm += a * a
		rightNorm += b * b
	}
	return dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm))
}

func newTestCohereEmbedder(t *testing.T, endpoint string) *CohereEmbedder {
	t.Helper()
	embedder, err := newCohereEmbedder("test-key", endpoint, &http.Client{})
	if err != nil {
		t.Fatal(err)
	}
	embedder.sleep = func(context.Context, time.Duration) error { return nil }
	embedder.jitter = func(time.Duration) time.Duration { return 0 }
	return embedder
}

func writeEmbeddingResponse(t *testing.T, w http.ResponseWriter, texts []string, vectors [][]float32) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"texts": texts,
		"embeddings": map[string]any{
			"float": vectors,
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func testVector(value float32) []float32 {
	vector := make([]float32, 1024)
	for i := range vector {
		vector[i] = value
	}
	return vector
}
