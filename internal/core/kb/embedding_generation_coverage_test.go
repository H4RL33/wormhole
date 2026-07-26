package kb

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type generationCoverageEmbedder struct {
	descriptor EmbeddingDescriptor
	err        error
	malformed  bool
}

func (e generationCoverageEmbedder) Descriptor() EmbeddingDescriptor { return e.descriptor }

func (e generationCoverageEmbedder) Embed(_ context.Context, request EmbeddingRequest) ([][]float32, error) {
	if e.err != nil {
		return nil, e.err
	}
	vectors := make([][]float32, len(request.Texts))
	for i := range vectors {
		if e.malformed {
			vectors[i] = []float32{1}
		} else {
			vectors[i] = axisVector(0)
		}
	}
	return vectors, nil
}

func TestEmbeddingGenerationStateAndIdentityGuards(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	missingID := "00000000-0000-0000-0000-000000000001"
	projectID := createProject(t, s, "generation-state-guards")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	if _, err := s.CreateEmbeddingGeneration(ctx, projectID, EmbeddingDescriptor{}); !errors.Is(err, ErrEmbeddingConfiguration) {
		t.Fatalf("invalid descriptor error = %v, want ErrEmbeddingConfiguration", err)
	}
	if err := s.MarkEmbeddingGenerationFailed(ctx, projectID, missingID, ""); err == nil || !strings.Contains(err.Error(), "empty failure code") {
		t.Fatalf("empty failure code error = %v", err)
	}
	if err := s.MarkEmbeddingGenerationFailed(ctx, projectID, missingID, "provider_error"); err == nil || !strings.Contains(err.Error(), "not building") {
		t.Fatalf("missing generation failure error = %v", err)
	}
	if err := s.StoreGenerationEmbedding(ctx, projectID, missingID, missingID, axisVector(0)); err == nil || !strings.Contains(err.Error(), "article lookup") {
		t.Fatalf("missing article error = %v", err)
	}

	article, err := s.WriteArticle(ctx, projectID, agentID, "Guarded", "Embedding generation state.", nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.StoreGenerationEmbedding(ctx, projectID, missingID, article.ID, axisVector(0)); err == nil || !strings.Contains(err.Error(), "generation lookup") {
		t.Fatalf("missing generation store error = %v", err)
	}
	candidate, err := s.CreateEmbeddingGeneration(ctx, projectID, s.EmbeddingDescriptor())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkEmbeddingGenerationFailed(ctx, projectID, candidate.ID, "manual_failure"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkEmbeddingGenerationFailed(ctx, projectID, candidate.ID, "again"); err == nil || !strings.Contains(err.Error(), "not building") {
		t.Fatalf("repeat failure error = %v", err)
	}
	if err := s.StoreGenerationEmbedding(ctx, projectID, candidate.ID, article.ID, axisVector(0)); err == nil || !strings.Contains(err.Error(), "not building") {
		t.Fatalf("failed generation store error = %v", err)
	}
	if err := s.ActivateEmbeddingGeneration(ctx, projectID, missingID); err == nil || !strings.Contains(err.Error(), "lookup") {
		t.Fatalf("missing activation error = %v", err)
	}
	if err := s.ActivateEmbeddingGeneration(ctx, projectID, candidate.ID); err == nil || !strings.Contains(err.Error(), "not building") {
		t.Fatalf("failed activation error = %v", err)
	}
}

func TestEmbeddingGenerationDescriptorMismatchIsRejectedBeforePublication(t *testing.T) {
	base := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, base, "generation-descriptor-guard")
	agentID := createAgent(t, base)
	createPassport(t, base, agentID, projectID)
	article, err := base.WriteArticle(ctx, projectID, agentID, "Descriptor", "Must match.", nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := base.CreateEmbeddingGeneration(ctx, projectID, base.EmbeddingDescriptor())
	if err != nil {
		t.Fatal(err)
	}
	different := generationCoverageEmbedder{descriptor: base.EmbeddingDescriptor()}
	different.descriptor.Version += "-different"
	other := storeWithEmbedder(t, base, different)
	if err := other.StoreGenerationEmbedding(ctx, projectID, candidate.ID, article.ID, axisVector(0)); !errors.Is(err, ErrEmbeddingGenerationMismatch) {
		t.Fatalf("store mismatch error = %v, want ErrEmbeddingGenerationMismatch", err)
	}
	if err := other.ActivateEmbeddingGeneration(ctx, projectID, candidate.ID); !errors.Is(err, ErrEmbeddingGenerationMismatch) {
		t.Fatalf("activation mismatch error = %v, want ErrEmbeddingGenerationMismatch", err)
	}
	assertGenerationState(t, base, candidate.ID, EmbeddingGenerationBuilding)
}

func TestRebuildClassifiesCancellationAndMalformedProviderOutput(t *testing.T) {
	tests := []struct {
		name      string
		embedder  func(EmbeddingDescriptor) Embedder
		wantCode  string
		wantError error
	}{
		{
			name: "cancellation", wantCode: "rebuild_cancelled", wantError: context.Canceled,
			embedder: func(descriptor EmbeddingDescriptor) Embedder {
				descriptor.Version += "-cancelled"
				return generationCoverageEmbedder{descriptor: descriptor, err: context.Canceled}
			},
		},
		{
			name: "malformed vectors", wantCode: "dimension_mismatch", wantError: ErrEmbeddingDimension,
			embedder: func(descriptor EmbeddingDescriptor) Embedder {
				descriptor.Version += "-malformed"
				return generationCoverageEmbedder{descriptor: descriptor, malformed: true}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := testStore(t)
			ctx := context.Background()
			projectID := createProject(t, base, "generation-failure-"+tt.name)
			agentID := createAgent(t, base)
			createPassport(t, base, agentID, projectID)
			if _, err := base.WriteArticle(ctx, projectID, agentID, "Article", "Needs rebuilding.", nil, nil, true); err != nil {
				t.Fatal(err)
			}
			candidate, err := storeWithEmbedder(t, base, tt.embedder(base.EmbeddingDescriptor())).RebuildEmbeddingGeneration(ctx, projectID)
			if !errors.Is(err, tt.wantError) {
				t.Fatalf("RebuildEmbeddingGeneration error = %v, want %v", err, tt.wantError)
			}
			if candidate.State != EmbeddingGenerationFailed || candidate.FailureCode != tt.wantCode {
				t.Fatalf("failed candidate = %+v, want code %q", candidate, tt.wantCode)
			}
			assertGenerationState(t, base, candidate.ID, EmbeddingGenerationFailed)
		})
	}
}

func TestEmbeddingGenerationOperationsPropagateClosedDatabase(t *testing.T) {
	s := testStore(t)
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	descriptor := s.EmbeddingDescriptor()
	checks := []struct {
		name string
		call func() error
	}{
		{"create", func() error { _, err := s.CreateEmbeddingGeneration(ctx, "project", descriptor); return err }},
		{"mark failed", func() error { return s.MarkEmbeddingGenerationFailed(ctx, "project", "generation", "failed") }},
		{"store", func() error {
			return s.StoreGenerationEmbedding(ctx, "project", "generation", "article", axisVector(0))
		}},
		{"activate", func() error { return s.ActivateEmbeddingGeneration(ctx, "project", "generation") }},
		{"rebuild", func() error { _, err := s.RebuildEmbeddingGeneration(ctx, "project"); return err }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); err == nil || !strings.Contains(err.Error(), "begin tx") {
				t.Fatalf("closed database error = %v, want begin tx", err)
			}
		})
	}
}
