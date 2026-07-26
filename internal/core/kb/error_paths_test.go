package kb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var errEmbeddingUnavailable = errors.New("embedding unavailable")

type unavailableEmbedder struct{}

func (unavailableEmbedder) Descriptor() EmbeddingDescriptor { return StubEmbedder{}.Descriptor() }

func (unavailableEmbedder) Embed(context.Context, EmbeddingRequest) ([][]float32, error) {
	return nil, errEmbeddingUnavailable
}

type oneDimensionEmbedder struct{}

func (oneDimensionEmbedder) Descriptor() EmbeddingDescriptor {
	return EmbeddingDescriptor{Provider: "test", Model: "one", Version: "1", Dimension: 1}
}

func (oneDimensionEmbedder) Embed(context.Context, EmbeddingRequest) ([][]float32, error) {
	return [][]float32{{1}}, nil
}

type transactionProbeEmbedder struct {
	db *sql.DB
}

type zeroNormEmbedder struct{}

func (zeroNormEmbedder) Descriptor() EmbeddingDescriptor { return StubEmbedder{}.Descriptor() }

func (zeroNormEmbedder) Embed(context.Context, EmbeddingRequest) ([][]float32, error) {
	return [][]float32{make([]float32, stubEmbeddingDim)}, nil
}

func TestWriteAndSearchRejectZeroNormEmbeddings(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "zero-norm-write-search")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)
	if _, err := s.WriteArticle(ctx, projectID, agentID, "Seed", "Active generation", nil, nil, true); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	s.embedder = zeroNormEmbedder{}
	if _, err := s.WriteArticle(ctx, projectID, agentID, "Rejected", "Zero norm", nil, nil, true); !errors.Is(err, ErrEmbeddingContract) {
		t.Fatalf("WriteArticle error = %v, want ErrEmbeddingContract", err)
	}
	if _, err := s.SearchArticles(ctx, projectID, agentID, "query", 10); !errors.Is(err, ErrEmbeddingContract) {
		t.Fatalf("SearchArticles error = %v, want ErrEmbeddingContract", err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM kb_articles WHERE project_id = $1`, projectID).Scan(&count); err != nil {
		t.Fatalf("count articles: %v", err)
	}
	if count != 1 {
		t.Fatalf("article count = %d, want only seed article", count)
	}
}

func (e transactionProbeEmbedder) Descriptor() EmbeddingDescriptor {
	return StubEmbedder{}.Descriptor()
}

func (e transactionProbeEmbedder) Embed(ctx context.Context, request EmbeddingRequest) ([][]float32, error) {
	probeCtx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if err := e.db.PingContext(probeCtx); err != nil {
		return nil, fmt.Errorf("provider called while database transaction held: %w", err)
	}
	return StubEmbedder{}.Embed(ctx, request)
}

func TestWriteAndSearchCallProviderOutsideDatabaseTransactions(t *testing.T) {
	s := testStore(t)
	s.db.SetMaxOpenConns(1)
	s.embedder = transactionProbeEmbedder{db: s.db}
	ctx := context.Background()
	projectID := createProject(t, s, "embedding-outside-transaction")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)
	if _, err := s.WriteArticle(ctx, projectID, agentID, "Outside", "Transaction", nil, nil, true); err != nil {
		t.Fatalf("WriteArticle: %v", err)
	}
	if _, err := s.SearchArticles(ctx, projectID, agentID, "Outside\n\nTransaction", 10); err != nil {
		t.Fatalf("SearchArticles: %v", err)
	}
}

func TestKBOperationsPropagateCanceledContext(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	operations := map[string]func() error{
		"write article": func() error {
			_, err := s.WriteArticle(ctx, uuid.NewString(), uuid.NewString(), "title", "body", nil, nil, true)
			return err
		},
		"ensure bootstrap article": func() error {
			_, err := s.EnsureBootstrapArticle(ctx, uuid.NewString(), uuid.NewString(), "onboarding", "title", "body", nil)
			return err
		},
		"search articles": func() error {
			_, err := s.SearchArticles(ctx, uuid.NewString(), uuid.NewString(), "query", 10)
			return err
		},
		"get article": func() error {
			_, err := s.GetArticle(ctx, uuid.NewString(), uuid.NewString(), uuid.NewString())
			return err
		},
		"get article links": func() error {
			_, err := s.GetArticleLinks(ctx, uuid.NewString(), uuid.NewString(), uuid.NewString())
			return err
		},
		"list articles": func() error {
			_, err := s.ListArticles(ctx, uuid.NewString())
			return err
		},
	}

	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			if err := operation(); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
		})
	}
}

func TestEnsureBootstrapArticleInTxRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "bootstrap-in-tx")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectID); err != nil {
		t.Fatalf("set project context: %v", err)
	}

	embedding, err := s.PrepareArticleEmbedding(ctx, "Onboarding", "Read this first.")
	if err != nil {
		t.Fatalf("PrepareArticleEmbedding: %v", err)
	}
	article, err := s.EnsureBootstrapArticleInTx(ctx, tx, projectID, agentID, "onboarding", "Onboarding", "Read this first.", nil, embedding)
	if err != nil {
		t.Fatalf("EnsureBootstrapArticleInTx: %v", err)
	}
	if article.ProjectID != projectID || article.AuthorAgentID != agentID || string(article.Frontmatter) != "{}" {
		t.Fatalf("article = %+v", article)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

func TestEnsureBootstrapArticleInTxRejectsEmptyKey(t *testing.T) {
	s := testStore(t)
	if _, err := s.EnsureBootstrapArticleInTx(context.Background(), nil, uuid.NewString(), uuid.NewString(), "", "title", "body", nil, nil); err == nil || !strings.Contains(err.Error(), "empty bootstrap key") {
		t.Fatalf("EnsureBootstrapArticleInTx error = %v, want empty bootstrap key", err)
	}
}

func TestWriteAndSearchPropagateEmbedderFailure(t *testing.T) {
	s := testStore(t)
	s.embedder = unavailableEmbedder{}
	ctx := context.Background()
	projectID := createProject(t, s, "embedder-failure")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)

	if _, err := s.WriteArticle(ctx, projectID, agentID, "title", "body", nil, nil, false); !errors.Is(err, errEmbeddingUnavailable) {
		t.Fatalf("WriteArticle error = %v, want errEmbeddingUnavailable", err)
	}
	if _, err := s.SearchArticles(ctx, projectID, agentID, "query", 10); !errors.Is(err, errEmbeddingUnavailable) {
		t.Fatalf("SearchArticles error = %v, want errEmbeddingUnavailable", err)
	}

	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM kb_articles WHERE project_id = $1`, projectID).Scan(&count); err != nil {
		t.Fatalf("count articles: %v", err)
	}
	if count != 0 {
		t.Fatalf("article count = %d, want 0", count)
	}
}

func TestWriteAndSearchRejectMismatchedEmbeddingDimensions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "embedding-dimension-mismatch")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)
	if _, err := s.WriteArticle(ctx, projectID, agentID, "Existing", "Stored with the configured dimension", nil, nil, true); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	s.embedder = oneDimensionEmbedder{}

	if _, err := s.WriteArticle(ctx, projectID, agentID, "New", "Mismatched write", nil, nil, false); !errors.Is(err, ErrEmbeddingConfiguration) {
		t.Fatalf("WriteArticle error = %v, want embedding configuration error", err)
	}
	if _, err := s.SearchArticles(ctx, projectID, agentID, "Mismatched search", 10); !errors.Is(err, ErrEmbeddingGenerationMismatch) {
		t.Fatalf("SearchArticles error = %v, want generation mismatch", err)
	}
}

func TestWriteArticleWithIDDuplicatePreservesOriginal(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "duplicate-article-id")
	agentID := createAgent(t, s)
	createPassport(t, s, agentID, projectID)
	id := uuid.NewString()

	if _, err := s.WriteArticleWithID(ctx, id, projectID, agentID, "Original", "Original body", nil, nil, true); err != nil {
		t.Fatalf("first WriteArticleWithID: %v", err)
	}
	replayed, err := s.WriteArticleWithID(ctx, id, projectID, agentID, "Original", "Original body", nil, nil, true)
	if err != nil {
		t.Fatalf("identical WriteArticleWithID replay: %v", err)
	}
	if replayed.ID != id || replayed.Title != "Original" {
		t.Fatalf("identical replay = %+v, want original article", replayed)
	}
	if _, err := s.WriteArticleWithID(ctx, id, projectID, agentID, "Replacement", "Replacement body", nil, nil, true); !errors.Is(err, ErrStableIDConflict) || !strings.Contains(err.Error(), "write article") {
		t.Fatalf("mismatched WriteArticleWithID error = %v, want ErrStableIDConflict", err)
	}

	article, err := s.GetArticle(ctx, projectID, agentID, id)
	if err != nil {
		t.Fatalf("GetArticle: %v", err)
	}
	if article.Title != "Original" || article.Body != "Original body" {
		t.Fatalf("article after duplicate = %+v, want original", article)
	}
}

func TestDedupViolationErrorJSONContract(t *testing.T) {
	err := (&ErrDedupViolation{ExistingID: "article-1", ExistingTitle: "Existing", Similarity: 0.91, Threshold: 0.85}).Error()
	var body map[string]any
	if jsonErr := json.Unmarshal([]byte(err), &body); jsonErr != nil {
		t.Fatalf("Error returned invalid JSON: %v", jsonErr)
	}
	if body["code"] != "DEDUP_VIOLATION" || !strings.Contains(body["suggestion"].(string), "Existing") {
		t.Fatalf("error JSON = %#v", body)
	}
}

func TestRequiredLinksViolationWithoutSuggestionsSaysNoneFound(t *testing.T) {
	message := (&ErrRequiredLinksViolation{ArticleType: "policy", MinLinks: 1}).Error()
	if !strings.Contains(message, "none found") {
		t.Fatalf("Error() = %q, want none-found guidance", message)
	}
}
