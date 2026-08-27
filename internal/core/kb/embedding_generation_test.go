package kb

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
)

type semanticFixture struct {
	Cases []struct {
		Name         string                       `json:"name"`
		Query        string                       `json:"query"`
		Related      struct{ Title, Body string } `json:"related"`
		LexicalDecoy struct{ Title, Body string } `json:"lexical_decoy"`
	} `json:"cases"`
}

type meaningEmbedder struct {
	descriptor EmbeddingDescriptor
	mu         sync.Mutex
	vectors    map[string][]float32
	blockOnce  chan struct{}
	blocked    chan struct{}
	failMode   EmbeddingMode
}

func (e *meaningEmbedder) Descriptor() EmbeddingDescriptor { return e.descriptor }

func (e *meaningEmbedder) Embed(ctx context.Context, request EmbeddingRequest) ([][]float32, error) {
	if request.Mode == e.failMode {
		return nil, embeddingFailure("provider_timeout", true, 0, ErrEmbeddingProvider)
	}
	e.mu.Lock()
	block := e.blockOnce
	if block != nil && request.Mode == EmbeddingModeReembedding {
		e.blockOnce = nil
		close(e.blocked)
	}
	e.mu.Unlock()
	if block != nil && request.Mode == EmbeddingModeReembedding {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-block:
		}
	}
	vectors := make([][]float32, len(request.Texts))
	for i, text := range request.Texts {
		vector, ok := e.vectors[text]
		if !ok {
			vector = axisVector(3)
		}
		vectors[i] = append([]float32(nil), vector...)
	}
	return vectors, nil
}

func axisVector(axis int) []float32 {
	vector := make([]float32, approvedEmbeddingDimension)
	vector[axis] = 1
	return vector
}

func fixtureEmbedder(t *testing.T, version string) (*meaningEmbedder, semanticFixture) {
	t.Helper()
	data, err := os.ReadFile("../../../testdata/alpha/kb/semantic-low-overlap.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture semanticFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	vectors := make(map[string][]float32)
	for i, testCase := range fixture.Cases {
		axis := i * 2
		vectors[testCase.Query] = axisVector(axis)
		vectors[articleEmbeddingText(testCase.Related.Title, testCase.Related.Body)] = axisVector(axis)
		vectors[articleEmbeddingText(testCase.LexicalDecoy.Title, testCase.LexicalDecoy.Body)] = axisVector(axis + 1)
	}
	return &meaningEmbedder{
		descriptor: EmbeddingDescriptor{Provider: "fixture", Model: "meaning", Version: version, Dimension: approvedEmbeddingDimension},
		vectors:    vectors,
	}, fixture
}

func storeWithEmbedder(t *testing.T, dbStore *Store, embedder Embedder) *Store {
	t.Helper()
	return NewStore(dbStore.db, embedder, 1.1, 0, 0, 0, 0)
}

func TestLowLexicalOverlapSemanticRankingBeatsLexicalDecoys(t *testing.T) {
	base := testStore(t)
	embedder, fixture := fixtureEmbedder(t, "fixture-v1")
	s := storeWithEmbedder(t, base, embedder)
	ctx := context.Background()
	projectID := createProject(t, s, "meaning-bearing-ranking")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)
	for _, testCase := range fixture.Cases {
		related, err := s.WriteArticle(ctx, projectID, agentID, testCase.Related.Title, testCase.Related.Body, nil, nil, true)
		if err != nil {
			t.Fatalf("write related %s: %v", testCase.Name, err)
		}
		if _, err := s.WriteArticle(ctx, projectID, agentID, testCase.LexicalDecoy.Title, testCase.LexicalDecoy.Body, nil, nil, true); err != nil {
			t.Fatalf("write decoy %s: %v", testCase.Name, err)
		}
		got, metadata, err := s.SearchArticlesWithMetadata(ctx, projectID, agentID, testCase.Query, 2)
		if err != nil {
			t.Fatalf("search %s: %v", testCase.Name, err)
		}
		if len(got) != 2 || got[0].ID != related.ID {
			t.Fatalf("search %s order = %+v, want related %s first", testCase.Name, got, related.ID)
		}
		if !metadata.SemanticApplied || metadata.GenerationID == "" || metadata.Provider != "fixture" || metadata.Model != "meaning" || metadata.Version != "fixture-v1" || metadata.Dimension != approvedEmbeddingDimension || metadata.DistanceMetric != "cosine" {
			t.Fatalf("search %s metadata = %+v", testCase.Name, metadata)
		}
	}
}

func TestRebuildKeepsOldActiveThenCatchesUpConcurrentWriteAndSwapsAtomically(t *testing.T) {
	base := testStore(t)
	oldEmbedder, fixture := fixtureEmbedder(t, "fixture-v1")
	oldStore := storeWithEmbedder(t, base, oldEmbedder)
	ctx := context.Background()
	projectID := createProject(t, oldStore, "rebuild-catch-up")
	agentID := createAgent(t, oldStore)
	createPassport(t, oldStore, agentID, projectID)
	first := fixture.Cases[0]
	if _, err := oldStore.WriteArticle(ctx, projectID, agentID, first.Related.Title, first.Related.Body, nil, nil, true); err != nil {
		t.Fatal(err)
	}
	var oldGenerationID string
	if err := base.db.QueryRow(`SELECT id FROM kb_embedding_generations WHERE project_id = $1 AND state = 'active'`, projectID).Scan(&oldGenerationID); err != nil {
		t.Fatal(err)
	}

	newEmbedder, _ := fixtureEmbedder(t, "fixture-v2")
	releaseRebuild := make(chan struct{})
	newEmbedder.blockOnce = releaseRebuild
	newEmbedder.blocked = make(chan struct{})
	newStore := storeWithEmbedder(t, base, newEmbedder)
	type result struct {
		generation EmbeddingGeneration
		err        error
	}
	done := make(chan result, 1)
	go func() {
		generation, err := newStore.RebuildEmbeddingGeneration(ctx, projectID)
		done <- result{generation: generation, err: err}
	}()
	<-newEmbedder.blocked
	assertGenerationState(t, base, oldGenerationID, EmbeddingGenerationActive)
	results, metadata, err := oldStore.SearchArticlesWithMetadata(ctx, projectID, agentID, first.Query, 1)
	if err != nil || len(results) != 1 || metadata.GenerationID != oldGenerationID || metadata.Version != "fixture-v1" {
		t.Fatalf("old generation did not serve during rebuild: results=%+v metadata=%+v err=%v", results, metadata, err)
	}
	second := fixture.Cases[1]
	concurrent, err := oldStore.WriteArticle(ctx, projectID, agentID, second.Related.Title, second.Related.Body, nil, nil, true)
	if err != nil {
		t.Fatalf("concurrent write: %v", err)
	}
	close(releaseRebuild)
	rebuilt := <-done
	if rebuilt.err != nil {
		t.Fatalf("RebuildEmbeddingGeneration: %v", rebuilt.err)
	}
	if rebuilt.generation.ID == oldGenerationID || rebuilt.generation.Descriptor.Version != "fixture-v2" || rebuilt.generation.State != EmbeddingGenerationActive {
		t.Fatalf("rebuilt generation = %+v", rebuilt.generation)
	}
	assertGenerationState(t, base, oldGenerationID, EmbeddingGenerationRetired)
	var storedHash string
	if err := base.db.QueryRow(`SELECT content_hash FROM kb_article_embeddings WHERE generation_id = $1 AND article_id = $2`, rebuilt.generation.ID, concurrent.ID).Scan(&storedHash); err != nil {
		t.Fatalf("concurrent article missing from replacement: %v", err)
	}
	if storedHash != articleContentHash(concurrent.Title, concurrent.Body) {
		t.Fatalf("concurrent content hash = %q", storedHash)
	}
}

func TestRebuildFailurePreservesOldAndPriorDescriptorIsRecoverable(t *testing.T) {
	base := testStore(t)
	v1Embedder, fixture := fixtureEmbedder(t, "fixture-v1")
	v1Store := storeWithEmbedder(t, base, v1Embedder)
	ctx := context.Background()
	projectID := createProject(t, v1Store, "rebuild-failure-and-rollback")
	agentID := createAgent(t, v1Store)
	createPassport(t, v1Store, agentID, projectID)
	testCase := fixture.Cases[0]
	if _, err := v1Store.WriteArticle(ctx, projectID, agentID, testCase.Related.Title, testCase.Related.Body, nil, nil, true); err != nil {
		t.Fatal(err)
	}
	var v1ID string
	if err := base.db.QueryRow(`SELECT id FROM kb_embedding_generations WHERE project_id = $1 AND state = 'active'`, projectID).Scan(&v1ID); err != nil {
		t.Fatal(err)
	}

	failing, _ := fixtureEmbedder(t, "fixture-broken")
	failing.failMode = EmbeddingModeReembedding
	if _, err := storeWithEmbedder(t, base, failing).RebuildEmbeddingGeneration(ctx, projectID); err == nil {
		t.Fatal("failing rebuild succeeded")
	}
	assertGenerationState(t, base, v1ID, EmbeddingGenerationActive)
	var failedCount int
	if err := base.db.QueryRow(`SELECT count(*) FROM kb_embedding_generations WHERE project_id = $1 AND state = 'failed' AND failure_code = 'provider_timeout'`, projectID).Scan(&failedCount); err != nil || failedCount != 1 {
		t.Fatalf("failed generation count = %d, err=%v", failedCount, err)
	}

	v2Embedder, _ := fixtureEmbedder(t, "fixture-v2")
	v2, err := storeWithEmbedder(t, base, v2Embedder).RebuildEmbeddingGeneration(ctx, projectID)
	if err != nil {
		t.Fatalf("rebuild v2: %v", err)
	}
	assertGenerationState(t, base, v1ID, EmbeddingGenerationRetired)
	restored, err := v1Store.RebuildEmbeddingGeneration(ctx, projectID)
	if err != nil {
		t.Fatalf("restore v1: %v", err)
	}
	if restored.ID != v1ID || restored.State != EmbeddingGenerationActive {
		t.Fatalf("restored generation = %+v, want reused %s", restored, v1ID)
	}
	assertGenerationState(t, base, v2.ID, EmbeddingGenerationRetired)
}

func TestEmbeddingGenerationActivationIsCompleteAndAtomic(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "embedding-generation-activation")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)
	article, err := s.WriteArticle(ctx, projectID, agentID, "Runbook", "Deploy safely.", nil, nil, true)
	if err != nil {
		t.Fatalf("seed active generation: %v", err)
	}

	var oldGenerationID string
	if err := s.db.QueryRowContext(ctx,
		`SELECT id FROM kb_embedding_generations WHERE project_id = $1 AND state = 'active'`, projectID,
	).Scan(&oldGenerationID); err != nil {
		t.Fatalf("read active generation: %v", err)
	}
	candidate, err := s.CreateEmbeddingGeneration(ctx, projectID, s.EmbeddingDescriptor())
	if err != nil {
		t.Fatalf("CreateEmbeddingGeneration: %v", err)
	}
	if err := s.ActivateEmbeddingGeneration(ctx, projectID, candidate.ID); !errors.Is(err, ErrEmbeddingGenerationIncomplete) {
		t.Fatalf("incomplete activation error = %v, want ErrEmbeddingGenerationIncomplete", err)
	}
	assertGenerationState(t, s, oldGenerationID, EmbeddingGenerationActive)
	assertGenerationState(t, s, candidate.ID, EmbeddingGenerationBuilding)

	vector, err := s.PrepareArticleEmbedding(ctx, article.Title, article.Body)
	if err != nil {
		t.Fatalf("PrepareArticleEmbedding: %v", err)
	}
	if err := s.StoreGenerationEmbedding(ctx, projectID, candidate.ID, article.ID, vector); err != nil {
		t.Fatalf("StoreGenerationEmbedding: %v", err)
	}
	if err := s.ActivateEmbeddingGeneration(ctx, projectID, candidate.ID); err != nil {
		t.Fatalf("ActivateEmbeddingGeneration: %v", err)
	}
	assertGenerationState(t, s, oldGenerationID, EmbeddingGenerationRetired)
	assertGenerationState(t, s, candidate.ID, EmbeddingGenerationActive)

	var retained int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM kb_article_embeddings WHERE generation_id IN ($1, $2) AND article_id = $3`,
		oldGenerationID, candidate.ID, article.ID,
	).Scan(&retained); err != nil {
		t.Fatalf("count retained vectors: %v", err)
	}
	if retained != 2 {
		t.Fatalf("retained vector count = %d, want 2", retained)
	}
}

func TestEmbeddingGenerationFailurePreservesActiveGeneration(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "embedding-generation-failure")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)
	if _, err := s.WriteArticle(ctx, projectID, agentID, "Active", "Article", nil, nil, true); err != nil {
		t.Fatalf("seed active generation: %v", err)
	}
	var oldGenerationID string
	if err := s.db.QueryRowContext(ctx,
		`SELECT id FROM kb_embedding_generations WHERE project_id = $1 AND state = 'active'`, projectID,
	).Scan(&oldGenerationID); err != nil {
		t.Fatalf("read active generation: %v", err)
	}
	candidate, err := s.CreateEmbeddingGeneration(ctx, projectID, s.EmbeddingDescriptor())
	if err != nil {
		t.Fatalf("CreateEmbeddingGeneration: %v", err)
	}
	if err := s.MarkEmbeddingGenerationFailed(ctx, projectID, candidate.ID, "provider_timeout"); err != nil {
		t.Fatalf("MarkEmbeddingGenerationFailed: %v", err)
	}
	assertGenerationState(t, s, oldGenerationID, EmbeddingGenerationActive)
	assertGenerationState(t, s, candidate.ID, EmbeddingGenerationFailed)
}

func TestStoreGenerationEmbeddingRejectsZeroNormVector(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "embedding-generation-zero-norm")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)
	article, err := s.WriteArticle(ctx, projectID, agentID, "Article", "Non-zero active vector", nil, nil, true)
	if err != nil {
		t.Fatalf("seed article: %v", err)
	}
	candidate, err := s.CreateEmbeddingGeneration(ctx, projectID, s.EmbeddingDescriptor())
	if err != nil {
		t.Fatalf("CreateEmbeddingGeneration: %v", err)
	}
	err = s.StoreGenerationEmbedding(ctx, projectID, candidate.ID, article.ID, make([]float32, stubEmbeddingDim))
	if !errors.Is(err, ErrEmbeddingContract) {
		t.Fatalf("StoreGenerationEmbedding error = %v, want ErrEmbeddingContract", err)
	}
}

func TestActivateEmbeddingGenerationRejectsConfiguredDescriptorMismatchBeforeSwap(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "embedding-generation-descriptor-mismatch")
	active, err := s.CreateEmbeddingGeneration(ctx, projectID, s.EmbeddingDescriptor())
	if err != nil {
		t.Fatalf("create active candidate: %v", err)
	}
	if err := s.ActivateEmbeddingGeneration(ctx, projectID, active.ID); err != nil {
		t.Fatalf("activate initial zero-article generation: %v", err)
	}
	mismatched := s.EmbeddingDescriptor()
	mismatched.Model = "unapproved-model"
	candidate, err := s.CreateEmbeddingGeneration(ctx, projectID, mismatched)
	if err != nil {
		t.Fatalf("create mismatched candidate: %v", err)
	}
	if err := s.ActivateEmbeddingGeneration(ctx, projectID, candidate.ID); !errors.Is(err, ErrEmbeddingGenerationMismatch) {
		t.Fatalf("ActivateEmbeddingGeneration error = %v, want ErrEmbeddingGenerationMismatch", err)
	}
	assertGenerationState(t, s, active.ID, EmbeddingGenerationActive)
	assertGenerationState(t, s, candidate.ID, EmbeddingGenerationBuilding)
}

func TestActivateEmbeddingGenerationRejectsStaleContentHash(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "embedding-generation-stale-content")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)
	article, err := s.WriteArticle(ctx, projectID, agentID, "Current", "Article body", nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	var oldGenerationID string
	if err := s.db.QueryRow(`SELECT id FROM kb_embedding_generations WHERE project_id = $1 AND state = 'active'`, projectID).Scan(&oldGenerationID); err != nil {
		t.Fatal(err)
	}
	candidate, err := s.CreateEmbeddingGeneration(ctx, projectID, s.EmbeddingDescriptor())
	if err != nil {
		t.Fatal(err)
	}
	vector, err := s.PrepareArticleEmbedding(ctx, article.Title, article.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.StoreGenerationEmbedding(ctx, projectID, candidate.ID, article.ID, vector); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE kb_article_embeddings SET content_hash = $1 WHERE generation_id = $2`, strings.Repeat("a", 64), candidate.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ActivateEmbeddingGeneration(ctx, projectID, candidate.ID); !errors.Is(err, ErrEmbeddingGenerationIncomplete) {
		t.Fatalf("activation error = %v, want ErrEmbeddingGenerationIncomplete", err)
	}
	assertGenerationState(t, s, oldGenerationID, EmbeddingGenerationActive)
	assertGenerationState(t, s, candidate.ID, EmbeddingGenerationBuilding)
}

func assertGenerationState(t *testing.T, s *Store, generationID string, want EmbeddingGenerationState) {
	t.Helper()
	var got EmbeddingGenerationState
	if err := s.db.QueryRow(`SELECT state FROM kb_embedding_generations WHERE id = $1`, generationID).Scan(&got); err != nil {
		t.Fatalf("read generation %s: %v", generationID, err)
	}
	if got != want {
		t.Fatalf("generation %s state = %s, want %s", generationID, got, want)
	}
}
