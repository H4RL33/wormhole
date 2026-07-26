package kb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	cohereEmbedEndpoint        = "https://api.cohere.com/v1/embed"
	approvedEmbeddingProvider  = "cohere"
	approvedEmbeddingModel     = "embed-v4.0"
	approvedEmbeddingVersion   = "4.0"
	approvedEmbeddingDimension = 1024
	maxEmbeddingInputs         = 64
	maxEmbeddingInput          = 32 * 1024
	maxEmbeddingBatch          = 1024 * 1024
	maxEmbeddingQuery          = 2000
	maxEmbeddingResponse       = 8 * 1024 * 1024
)

var (
	ErrEmbeddingConfiguration = errors.New("kb: embedding configuration invalid")
	ErrEmbeddingInput         = errors.New("kb: embedding input invalid")
	ErrEmbeddingProvider      = errors.New("kb: embedding provider unavailable")
	ErrEmbeddingContract      = errors.New("kb: embedding provider contract invalid")
	ErrEmbeddingDimension     = errors.New("kb: embedding dimension invalid")
)

type EmbeddingDescriptor struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Version   string `json:"version"`
	Dimension int    `json:"dimension"`
}

func ApprovedEmbeddingDescriptor() EmbeddingDescriptor {
	return EmbeddingDescriptor{
		Provider: approvedEmbeddingProvider, Model: approvedEmbeddingModel,
		Version: approvedEmbeddingVersion, Dimension: approvedEmbeddingDimension,
	}
}

type EmbeddingInputType string

const (
	EmbeddingInputSearchDocument EmbeddingInputType = "search_document"
	EmbeddingInputSearchQuery    EmbeddingInputType = "search_query"
)

type EmbeddingMode string

const (
	EmbeddingModeInteractive EmbeddingMode = "interactive"
	EmbeddingModeReembedding EmbeddingMode = "reembedding"
)

type EmbeddingRequest struct {
	InputType EmbeddingInputType
	Texts     []string
	Mode      EmbeddingMode
}

type embeddingPolicy struct {
	TotalBudget    time.Duration
	RequestTimeout time.Duration
	MaxAttempts    int
}

func embeddingAttemptPolicy(mode EmbeddingMode) embeddingPolicy {
	if mode == EmbeddingModeReembedding {
		return embeddingPolicy{TotalBudget: 30 * time.Second, RequestTimeout: 2 * time.Second, MaxAttempts: 3}
	}
	return embeddingPolicy{TotalBudget: 5 * time.Second, RequestTimeout: 2 * time.Second, MaxAttempts: 2}
}

// EmbeddingFailure is safe to return across the MCP boundary. It intentionally
// omits request text, response bodies, vectors, and credentials.
type EmbeddingFailure struct {
	Code            string `json:"code"`
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	Version         string `json:"version"`
	SemanticRanking bool   `json:"semantic_ranking"`
	Degraded        bool   `json:"degraded"`
	Fallback        string `json:"fallback"`
	Retryable       bool   `json:"retryable"`
	HTTPStatus      int    `json:"http_status,omitempty"`
	cause           error
}

func (e *EmbeddingFailure) Error() string {
	encoded, _ := json.Marshal(e)
	return string(encoded)
}

func (e *EmbeddingFailure) Unwrap() error { return e.cause }

type CohereEmbedder struct {
	apiKey   string
	endpoint string
	client   *http.Client
	sleep    func(context.Context, time.Duration) error
	jitter   func(time.Duration) time.Duration
}

func NewCohereEmbedder(apiKey string) (*CohereEmbedder, error) {
	return newCohereEmbedder(apiKey, cohereEmbedEndpoint, &http.Client{})
}

func newCohereEmbedder(apiKey, endpoint string, client *http.Client) (*CohereEmbedder, error) {
	if apiKey == "" || strings.TrimSpace(apiKey) != apiKey || endpoint == "" || client == nil {
		return nil, ErrEmbeddingConfiguration
	}
	return &CohereEmbedder{
		apiKey: apiKey, endpoint: endpoint, client: client,
		sleep: sleepEmbeddingBackoff,
		jitter: func(max time.Duration) time.Duration {
			if max <= 0 {
				return 0
			}
			return time.Duration(rand.Int64N(int64(max) + 1))
		},
	}, nil
}

func (e *CohereEmbedder) Descriptor() EmbeddingDescriptor { return ApprovedEmbeddingDescriptor() }

func (e *CohereEmbedder) Embed(ctx context.Context, request EmbeddingRequest) ([][]float32, error) {
	if err := validateEmbeddingRequest(request); err != nil {
		return nil, err
	}
	body, err := json.Marshal(struct {
		Model           string   `json:"model"`
		Texts           []string `json:"texts"`
		InputType       string   `json:"input_type"`
		EmbeddingTypes  []string `json:"embedding_types"`
		OutputDimension int      `json:"output_dimension"`
		MaxTokens       int      `json:"max_tokens"`
		Truncate        string   `json:"truncate"`
	}{
		Model: approvedEmbeddingModel, Texts: request.Texts,
		InputType: string(request.InputType), EmbeddingTypes: []string{"float"},
		OutputDimension: approvedEmbeddingDimension, MaxTokens: 8192, Truncate: "NONE",
	})
	if err != nil {
		return nil, embeddingFailure("request_encoding_failed", false, 0, ErrEmbeddingInput)
	}

	policy := embeddingAttemptPolicy(request.Mode)
	totalCtx, cancel := context.WithTimeout(ctx, policy.TotalBudget)
	defer cancel()
	var last error
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		vectors, status, retryAfter, requestErr := e.doRequest(totalCtx, body, request.Texts, policy.RequestTimeout)
		if requestErr == nil {
			return vectors, nil
		}
		last = requestErr
		if attempt == policy.MaxAttempts || !retryableEmbeddingFailure(requestErr, status) || totalCtx.Err() != nil {
			break
		}
		delay := e.jitter(100 * time.Millisecond << (attempt - 1))
		if retryAfter > delay {
			delay = retryAfter
		}
		deadline, ok := totalCtx.Deadline()
		if ok && delay >= time.Until(deadline) {
			break
		}
		if err := e.sleep(totalCtx, delay); err != nil {
			last = err
			break
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if totalCtx.Err() != nil {
		return nil, embeddingFailure("provider_timeout", true, 0, ErrEmbeddingProvider)
	}
	return nil, last
}

func (e *CohereEmbedder) doRequest(ctx context.Context, body []byte, texts []string, timeout time.Duration) ([][]float32, int, time.Duration, error) {
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, 0, embeddingFailure("request_creation_failed", false, 0, ErrEmbeddingConfiguration)
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	response, err := e.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, 0, ctx.Err()
		}
		return nil, 0, 0, embeddingFailure("network_error", true, 0, ErrEmbeddingProvider)
	}
	defer response.Body.Close()
	retryAfter := parseRetryAfter(response.Header.Get("Retry-After"), time.Now())
	if response.StatusCode != http.StatusOK {
		retryable := retryableEmbeddingStatus(response.StatusCode)
		return nil, response.StatusCode, retryAfter, embeddingFailure("provider_http_error", retryable, response.StatusCode, ErrEmbeddingProvider)
	}

	var decoded struct {
		Texts      []string `json:"texts"`
		Embeddings struct {
			Float [][]float32 `json:"float"`
		} `json:"embeddings"`
	}
	reader := io.LimitReader(response.Body, maxEmbeddingResponse+1)
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(&decoded); err != nil {
		return nil, response.StatusCode, 0, embeddingFailure("invalid_response", false, response.StatusCode, ErrEmbeddingContract)
	}
	if !equalStrings(decoded.Texts, texts) {
		return nil, response.StatusCode, 0, embeddingFailure("response_order_mismatch", false, response.StatusCode, ErrEmbeddingContract)
	}
	if err := validateEmbeddingBatch(texts, decoded.Embeddings.Float, approvedEmbeddingDimension); err != nil {
		return nil, response.StatusCode, 0, err
	}
	return decoded.Embeddings.Float, response.StatusCode, 0, nil
}

func validateEmbeddingRequest(request EmbeddingRequest) error {
	if request.Mode != EmbeddingModeInteractive && request.Mode != EmbeddingModeReembedding {
		return embeddingFailure("invalid_mode", false, 0, ErrEmbeddingInput)
	}
	if request.InputType != EmbeddingInputSearchDocument && request.InputType != EmbeddingInputSearchQuery {
		return embeddingFailure("invalid_input_type", false, 0, ErrEmbeddingInput)
	}
	if len(request.Texts) == 0 || len(request.Texts) > maxEmbeddingInputs {
		return embeddingFailure("invalid_batch_count", false, 0, ErrEmbeddingInput)
	}
	if request.InputType == EmbeddingInputSearchQuery && len(request.Texts) != 1 {
		return embeddingFailure("invalid_query_count", false, 0, ErrEmbeddingInput)
	}
	total := 0
	for _, text := range request.Texts {
		if text == "" || !utf8.ValidString(text) || len(text) > maxEmbeddingInput {
			return embeddingFailure("invalid_input_size", false, 0, ErrEmbeddingInput)
		}
		if request.InputType == EmbeddingInputSearchQuery && utf8.RuneCountInString(text) > maxEmbeddingQuery {
			return embeddingFailure("query_too_long", false, 0, ErrEmbeddingInput)
		}
		total += len(text)
	}
	if total > maxEmbeddingBatch {
		return embeddingFailure("batch_too_large", false, 0, ErrEmbeddingInput)
	}
	return nil
}

func validateEmbeddingBatch(texts []string, vectors [][]float32, dimension int) error {
	if len(vectors) != len(texts) {
		return embeddingFailure("response_count_mismatch", false, 0, ErrEmbeddingContract)
	}
	for _, vector := range vectors {
		if len(vector) != dimension {
			return embeddingFailure("dimension_mismatch", false, 0, ErrEmbeddingDimension)
		}
		var normSquared float64
		for _, value := range vector {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return embeddingFailure("non_finite_vector", false, 0, ErrEmbeddingContract)
			}
			normSquared += float64(value) * float64(value)
		}
		if normSquared == 0 {
			return embeddingFailure("zero_norm_vector", false, 0, ErrEmbeddingContract)
		}
	}
	return nil
}

func embedOne(ctx context.Context, embedder Embedder, inputType EmbeddingInputType, text string, mode EmbeddingMode) ([]float32, error) {
	if embedder == nil {
		return nil, embeddingFailure("provider_unavailable", false, 0, ErrEmbeddingConfiguration)
	}
	descriptor := embedder.Descriptor()
	if descriptor.Provider == "" || descriptor.Model == "" || descriptor.Version == "" || descriptor.Dimension <= 0 {
		return nil, embeddingFailure("invalid_descriptor", false, 0, ErrEmbeddingConfiguration)
	}
	vectors, err := embedder.Embed(ctx, EmbeddingRequest{InputType: inputType, Texts: []string{text}, Mode: mode})
	if err != nil {
		return nil, err
	}
	if err := validateEmbeddingBatch([]string{text}, vectors, descriptor.Dimension); err != nil {
		return nil, err
	}
	return vectors[0], nil
}

func embeddingFailure(code string, retryable bool, status int, cause error) error {
	return &EmbeddingFailure{
		Code: code, Provider: approvedEmbeddingProvider, Model: approvedEmbeddingModel,
		Version: approvedEmbeddingVersion, SemanticRanking: false, Degraded: true,
		Fallback: "none", Retryable: retryable, HTTPStatus: status, cause: cause,
	}
}

func retryableEmbeddingFailure(err error, status int) bool {
	var failure *EmbeddingFailure
	return errors.As(err, &failure) && failure.Retryable && (status == 0 || retryableEmbeddingStatus(status))
}

func retryableEmbeddingStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil && when.After(now) {
		return when.Sub(now)
	}
	return 0
}

func sleepEmbeddingBackoff(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func articleEmbeddingText(title, body string) string { return title + "\n\n" + body }

func formatEmbeddingDescriptor(descriptor EmbeddingDescriptor) string {
	return fmt.Sprintf("%s/%s/%s/%d", descriptor.Provider, descriptor.Model, descriptor.Version, descriptor.Dimension)
}
