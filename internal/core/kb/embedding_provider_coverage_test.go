package kb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEmbeddingProviderRejectsRemainingConfigurationAndInputShapes(t *testing.T) {
	for _, configure := range []struct {
		key      string
		endpoint string
		client   *http.Client
	}{
		{" key", "https://example.invalid", &http.Client{}},
		{"key", "", &http.Client{}},
		{"key", "https://example.invalid", nil},
	} {
		if _, err := newCohereEmbedder(configure.key, configure.endpoint, configure.client); !errors.Is(err, ErrEmbeddingConfiguration) {
			t.Fatalf("configuration %+v error = %v", configure, err)
		}
	}
	embedder := newTestCohereEmbedder(t, "http://127.0.0.1:1")
	requests := []EmbeddingRequest{
		{InputType: EmbeddingInputSearchDocument, Texts: []string{"x"}, Mode: "unknown"},
		{InputType: EmbeddingInputSearchQuery, Texts: []string{"one", "two"}, Mode: EmbeddingModeInteractive},
		{InputType: EmbeddingInputSearchDocument, Texts: []string{""}, Mode: EmbeddingModeInteractive},
		{InputType: EmbeddingInputSearchDocument, Texts: []string{string([]byte{0xff})}, Mode: EmbeddingModeInteractive},
	}
	for _, request := range requests {
		if _, err := embedder.Embed(context.Background(), request); !errors.Is(err, ErrEmbeddingInput) {
			t.Fatalf("request %+v error = %v, want ErrEmbeddingInput", request, err)
		}
	}
}

func TestEmbeddingRetryAfterAndBackoffHonorServerAndContext(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	if got := parseRetryAfter("", now); got != 0 {
		t.Fatalf("empty Retry-After = %v", got)
	}
	if got := parseRetryAfter("3", now); got != 3*time.Second {
		t.Fatalf("numeric Retry-After = %v", got)
	}
	if got := parseRetryAfter(now.Add(5*time.Second).Format(http.TimeFormat), now); got != 5*time.Second {
		t.Fatalf("date Retry-After = %v", got)
	}
	for _, value := range []string{"-1", "invalid", now.Add(-time.Second).Format(http.TimeFormat)} {
		if got := parseRetryAfter(value, now); got != 0 {
			t.Fatalf("invalid Retry-After %q = %v", value, got)
		}
	}
	if err := sleepEmbeddingBackoff(context.Background(), 0); err != nil {
		t.Fatalf("zero backoff: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepEmbeddingBackoff(canceled, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled backoff error = %v", err)
	}
}

func TestCohereEmbedderClassifiesMalformedResponsesAndCanceledBackoff(t *testing.T) {
	t.Run("malformed success response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("not-json"))
		}))
		defer server.Close()
		embedder := newTestCohereEmbedder(t, server.URL)
		if _, err := embedder.Embed(context.Background(), EmbeddingRequest{InputType: EmbeddingInputSearchQuery, Texts: []string{"query"}, Mode: EmbeddingModeInteractive}); !errors.Is(err, ErrEmbeddingContract) {
			t.Fatalf("malformed response error = %v", err)
		}
	})

	t.Run("backoff interruption", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "retry", http.StatusServiceUnavailable)
		}))
		defer server.Close()
		embedder := newTestCohereEmbedder(t, server.URL)
		stop := errors.New("backoff stopped")
		embedder.sleep = func(context.Context, time.Duration) error { return stop }
		_, err := embedder.Embed(context.Background(), EmbeddingRequest{InputType: EmbeddingInputSearchQuery, Texts: []string{"query"}, Mode: EmbeddingModeInteractive})
		if !errors.Is(err, stop) {
			t.Fatalf("backoff error = %v, want %v", err, stop)
		}
	})
}

func TestCohereEmbedderCoversJitterRetryDeadlineAndRequestBoundaries(t *testing.T) {
	embedder := newTestCohereEmbedder(t, "http://127.0.0.1:1")
	if got := embedder.Descriptor(); got != ApprovedEmbeddingDescriptor() {
		t.Fatalf("Descriptor = %+v", got)
	}
	if delay := embedder.jitter(0); delay != 0 {
		t.Fatalf("zero jitter = %v", delay)
	}
	if delay := embedder.jitter(time.Nanosecond); delay < 0 || delay > time.Nanosecond {
		t.Fatalf("bounded jitter = %v", delay)
	}

	t.Run("server retry-after controls backoff", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "retry", http.StatusServiceUnavailable)
		}))
		defer server.Close()
		embedder := newTestCohereEmbedder(t, server.URL)
		embedder.jitter = func(time.Duration) time.Duration { return 0 }
		stop := errors.New("stop after observing delay")
		embedder.sleep = func(_ context.Context, delay time.Duration) error {
			if delay != time.Second {
				t.Fatalf("retry delay = %v, want 1s", delay)
			}
			return stop
		}
		if _, err := embedder.Embed(context.Background(), EmbeddingRequest{InputType: EmbeddingInputSearchQuery, Texts: []string{"query"}, Mode: EmbeddingModeInteractive}); !errors.Is(err, stop) {
			t.Fatalf("Embed retry delay error = %v", err)
		}
	})

	t.Run("retry delay exceeds total deadline", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "30")
			http.Error(w, "retry", http.StatusServiceUnavailable)
		}))
		defer server.Close()
		embedder := newTestCohereEmbedder(t, server.URL)
		embedder.jitter = func(time.Duration) time.Duration { return 0 }
		if _, err := embedder.Embed(context.Background(), EmbeddingRequest{InputType: EmbeddingInputSearchQuery, Texts: []string{"query"}, Mode: EmbeddingModeInteractive}); !errors.Is(err, ErrEmbeddingProvider) {
			t.Fatalf("Embed long retry delay error = %v", err)
		}
	})

	t.Run("caller cancellation", func(t *testing.T) {
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := embedder.Embed(canceled, EmbeddingRequest{InputType: EmbeddingInputSearchQuery, Texts: []string{"query"}, Mode: EmbeddingModeInteractive}); !errors.Is(err, context.Canceled) {
			t.Fatalf("Embed canceled error = %v", err)
		}
	})

	t.Run("invalid endpoint", func(t *testing.T) {
		embedder := newTestCohereEmbedder(t, ":")
		if _, _, _, err := embedder.doRequest(context.Background(), []byte(`{}`), []string{"query"}, time.Second); !errors.Is(err, ErrEmbeddingConfiguration) {
			t.Fatalf("doRequest invalid endpoint error = %v", err)
		}
	})

	t.Run("invalid vector contract", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"texts": []string{"query"}, "embeddings": map[string]any{"float": [][]float32{{1}}}})
		}))
		defer server.Close()
		embedder := newTestCohereEmbedder(t, server.URL)
		if _, err := embedder.Embed(context.Background(), EmbeddingRequest{InputType: EmbeddingInputSearchQuery, Texts: []string{"query"}, Mode: EmbeddingModeInteractive}); !errors.Is(err, ErrEmbeddingDimension) {
			t.Fatalf("Embed invalid dimension error = %v", err)
		}
	})
}
