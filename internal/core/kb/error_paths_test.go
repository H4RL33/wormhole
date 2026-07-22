package kb

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

var errEmbeddingUnavailable = errors.New("embedding unavailable")

type unavailableEmbedder struct{}

func (unavailableEmbedder) Embed(context.Context, string) ([]float32, error) {
	return nil, errEmbeddingUnavailable
}

type oneDimensionEmbedder struct{}

func (oneDimensionEmbedder) Embed(context.Context, string) ([]float32, error) {
	return []float32{1}, nil
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

	article, err := s.EnsureBootstrapArticleInTx(ctx, tx, projectID, agentID, "onboarding", "Onboarding", "Read this first.", nil)
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
	if _, err := s.EnsureBootstrapArticleInTx(context.Background(), nil, uuid.NewString(), uuid.NewString(), "", "title", "body", nil); err == nil || !strings.Contains(err.Error(), "empty bootstrap key") {
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

	if _, err := s.WriteArticle(ctx, projectID, agentID, "New", "Mismatched write", nil, nil, false); err == nil || !strings.Contains(err.Error(), "dedup query") {
		t.Fatalf("WriteArticle error = %v, want dedup dimension error", err)
	}
	if _, err := s.SearchArticles(ctx, projectID, agentID, "Mismatched search", 10); err == nil || !strings.Contains(err.Error(), "search articles: query") {
		t.Fatalf("SearchArticles error = %v, want query dimension error", err)
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
	if _, err := s.WriteArticleWithID(ctx, id, projectID, agentID, "Replacement", "Replacement body", nil, nil, true); err == nil || !strings.Contains(err.Error(), "write article") {
		t.Fatalf("duplicate WriteArticleWithID error = %v, want wrapped insert error", err)
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
