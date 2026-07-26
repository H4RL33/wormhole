// Package kb implements the Knowledge Base pillar's write path (RFC-0001
// §8.3): atomic, explicitly linked articles. Day 13 shipped this as
// plumbing only: no compliance checks (semantic dedup, conciseness, required
// links, all RFC-0001 §15 open-question territory per docs/kb-schema.md).
// Production writes use the approved provider-neutral embedding boundary and
// generation-scoped pgvector storage. StubEmbedder remains test-only.
//
// This package stays isolated per architecture.md R2: it does not import
// internal/core/tasks or internal/core/events. Link-target existence checks
// are done with raw SQL against the kb_articles table directly, the same way
// git.LinkCommit checks task existence without importing tasks.
package kb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/H4RL33/wormhole/internal/types"
)

var ErrPassportNotFound = errors.New("kb: agent not registered or has no passport for this project")
var ErrLinkedArticleNotFound = errors.New("kb: linked article not found")
var ErrArticleNotFound = errors.New("kb: article not found")
var ErrStableIDConflict = errors.New("kb: stable id conflict")
var ErrPreparedBootstrapEmbeddingUnavailable = errors.New("kb: prepared bootstrap embedding unavailable")

type ErrDedupViolation struct {
	ExistingID    string  `json:"existing_article_id"`
	ExistingTitle string  `json:"existing_article_title"`
	Similarity    float64 `json:"similarity"`
	Threshold     float64 `json:"threshold"`
}

func (e *ErrDedupViolation) Error() string {
	m := map[string]any{
		"error": "kb: write article: semantic duplicate found",
		"code":  "DEDUP_VIOLATION",
		"closest_article": map[string]any{
			"id":         e.ExistingID,
			"title":      e.ExistingTitle,
			"similarity": e.Similarity,
		},
		"suggestion": fmt.Sprintf("The article is too similar to '%s' (similarity %.2f >= threshold %.2f). Use the existing article, update it, or set the 'force' parameter to true to write it anyway.", e.ExistingTitle, e.Similarity, e.Threshold),
	}
	b, _ := json.Marshal(m)
	return string(b)
}

type ErrConcisenessViolation struct {
	Length    int `json:"length"`
	MaxLength int `json:"max_length"`
}

func (e *ErrConcisenessViolation) Error() string {
	m := map[string]any{
		"error": "kb: write article: conciseness ceiling exceeded",
		"code":  "CONCISENESS_VIOLATION",
		"details": map[string]any{
			"length":     e.Length,
			"max_length": e.MaxLength,
		},
		"suggestion": fmt.Sprintf("The article body is too long (%d characters). The maximum allowed length is %d characters. Please summarize the article to make it more concise.", e.Length, e.MaxLength),
	}
	b, _ := json.Marshal(m)
	return string(b)
}

type LinkSuggestion struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type ErrRequiredLinksViolation struct {
	ArticleType string           `json:"article_type"`
	LinkCount   int              `json:"link_count"`
	MinLinks    int              `json:"min_links"`
	Suggestions []LinkSuggestion `json:"suggestions"`
}

func (e *ErrRequiredLinksViolation) Error() string {
	m := map[string]any{
		"error": "kb: write article: missing required links",
		"code":  "REQUIRED_LINKS_VIOLATION",
		"details": map[string]any{
			"article_type": e.ArticleType,
			"link_count":   e.LinkCount,
			"min_links":    e.MinLinks,
			"suggestions":  e.Suggestions,
		},
		"suggestion": fmt.Sprintf("Articles of type '%s' require at least %d links (got %d). We suggest linking to related articles such as: %s.", e.ArticleType, e.MinLinks, e.LinkCount, formatSuggestions(e.Suggestions)),
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func formatSuggestions(s []LinkSuggestion) string {
	var parts []string
	for _, item := range s {
		parts = append(parts, fmt.Sprintf("'%s' (%s)", item.Title, item.ID))
	}
	if len(parts) == 0 {
		return "none found"
	}
	return strings.Join(parts, ", ")
}

// Article is a single atomic knowledge base entry (RFC-0001 §8.3: one
// article = one fact, decision, or procedure).
type Article struct {
	ID            string
	ProjectID     string
	Title         string
	Body          string
	Frontmatter   json.RawMessage
	AuthorAgentID string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Embedder is the provider-neutral boundary for article, query, and rebuild
// embeddings. Implementations must preserve response order and return their
// immutable model descriptor with every process configuration.
type Embedder interface {
	Descriptor() EmbeddingDescriptor
	Embed(ctx context.Context, request EmbeddingRequest) ([][]float32, error)
}

// StubEmbedder is a deterministic, dependency-free test Embedder.
//
// It is NOT semantically meaningful: its output does not capture anything
// about the meaning of the input text. It exists only to make the
// wormhole.kb.write / wormhole.kb.search pipeline real and testable end to
// end without making unit/integration tests call the paid provider.
//
// Algorithm: sha256 the input text, take the first 16 bytes of the digest,
// map each byte b to a float32 via (float32(b)-128)/128.0, producing a
// fixed 1024-dimensional vector in roughly [-1, 1). It remains test-only;
// Fabric startup now requires the approved Cohere implementation.
type StubEmbedder struct{}

const stubEmbeddingDim = 1024

func (StubEmbedder) Descriptor() EmbeddingDescriptor {
	return EmbeddingDescriptor{Provider: "stub", Model: "sha256", Version: "test-v1", Dimension: stubEmbeddingDim}
}

func (StubEmbedder) Embed(ctx context.Context, request EmbeddingRequest) ([][]float32, error) {
	if err := validateEmbeddingRequest(request); err != nil {
		return nil, err
	}
	vectors := make([][]float32, len(request.Texts))
	for textIndex, text := range request.Texts {
		digest := sha256.Sum256([]byte(text))
		vector := make([]float32, stubEmbeddingDim)
		for i := 0; i < stubEmbeddingDim; i++ {
			vector[i] = (float32(digest[i%len(digest)]) - 128) / 128.0
		}
		vectors[textIndex] = vector
	}
	return vectors, nil
}

// formatVectorLiteral renders a []float32 as a pgvector text-input literal,
// e.g. "[0.1,0.2,0.3]" (comma-separated, no spaces), for use with a
// $N::vector cast. lib/pq has no native vector type, so this is done by
// hand.
func formatVectorLiteral(vec []float32) string {
	parts := make([]string, len(vec))
	for i, v := range vec {
		parts[i] = strconv.FormatFloat(float64(v), 'f', -1, 32)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

type Store struct {
	db                *sql.DB
	embedder          Embedder
	dedupThreshold    float64
	maxBodyLength     int
	minLinksDecision  int
	minLinksPolicy    int
	minLinksProcedure int
	bootstrapMu       sync.RWMutex
	bootstrapVectors  map[string][]float32
}

func NewStore(db *sql.DB, embedder Embedder, dedupThreshold float64, maxBodyLength int, minLinksDecision, minLinksPolicy, minLinksProcedure int) *Store {
	return &Store{
		db:                db,
		embedder:          embedder,
		dedupThreshold:    dedupThreshold,
		maxBodyLength:     maxBodyLength,
		minLinksDecision:  minLinksDecision,
		minLinksPolicy:    minLinksPolicy,
		minLinksProcedure: minLinksProcedure,
		bootstrapVectors:  make(map[string][]float32),
	}
}

const articleColumns = `id, project_id, title, body, frontmatter, author_agent_id, created_at, updated_at`

// WriteArticle inserts a new KB article and, within the same transaction,
// validates and inserts each requested link to an existing article
// (RFC-0001 §9 wormhole.kb.write(title, body, links[])). If any link target
// does not exist in-project, the whole write rolls back: no partial article
// with dangling links is ever left behind.
// WriteArticle inserts a new article, letting Postgres assign the id
// (gen_random_uuid() default).
func (s *Store) WriteArticle(ctx context.Context, projectID, agentID, title, body string, frontmatter json.RawMessage, linkTargetIDs []string, force bool) (Article, error) {
	return s.writeArticleWithOptionalID(ctx, "", "", projectID, agentID, title, body, frontmatter, linkTargetIDs, force)
}

// EnsureBootstrapArticle atomically creates a fixed system article or returns
// the existing article carrying bootstrapKey. Ordinary articles never set this
// nullable marker, so their titles remain unconstrained.
func (s *Store) EnsureBootstrapArticle(ctx context.Context, projectID, agentID, bootstrapKey, title, body string, frontmatter json.RawMessage) (Article, error) {
	if bootstrapKey == "" {
		return Article{}, fmt.Errorf("kb: ensure bootstrap article: empty bootstrap key")
	}
	return s.writeArticleWithOptionalID(ctx, "", bootstrapKey, projectID, agentID, title, body, frontmatter, nil, true)
}

// EnsureBootstrapArticleInTx is the transaction-scoped core of
// EnsureBootstrapArticle. The caller owns tx's lifecycle and must have set
// wormhole.project_id.
func (s *Store) EnsureBootstrapArticleInTx(ctx context.Context, tx *sql.Tx, projectID, agentID, bootstrapKey, title, body string, frontmatter json.RawMessage, embedding []float32) (Article, error) {
	if bootstrapKey == "" {
		return Article{}, fmt.Errorf("kb: ensure bootstrap article: empty bootstrap key")
	}
	return s.writeArticleInTxWithOptionalID(ctx, tx, "", bootstrapKey, projectID, agentID, title, body, frontmatter, nil, true, embedding)
}

// PrepareArticleEmbedding performs the paid provider call without opening a
// database transaction. Transaction-owning orchestration uses it before
// passing the resulting vector to EnsureBootstrapArticleInTx.
func (s *Store) PrepareArticleEmbedding(ctx context.Context, title, body string) ([]float32, error) {
	return embedOne(ctx, s.embedder, EmbeddingInputSearchDocument, articleEmbeddingText(title, body), EmbeddingModeInteractive)
}

func (s *Store) SetPreparedBootstrapEmbedding(bootstrapKey string, embedding []float32) error {
	if bootstrapKey == "" {
		return fmt.Errorf("kb: prepare bootstrap embedding: empty bootstrap key")
	}
	if err := validateEmbeddingBatch([]string{"bootstrap"}, [][]float32{embedding}, s.embedder.Descriptor().Dimension); err != nil {
		return fmt.Errorf("kb: prepare bootstrap embedding: %w", err)
	}
	prepared := append([]float32(nil), embedding...)
	s.bootstrapMu.Lock()
	s.bootstrapVectors[bootstrapKey] = prepared
	s.bootstrapMu.Unlock()
	return nil
}

func (s *Store) PreparedBootstrapEmbedding(bootstrapKey string) ([]float32, error) {
	s.bootstrapMu.RLock()
	embedding, ok := s.bootstrapVectors[bootstrapKey]
	s.bootstrapMu.RUnlock()
	if !ok {
		return nil, ErrPreparedBootstrapEmbeddingUnavailable
	}
	return append([]float32(nil), embedding...), nil
}

func (s *Store) EmbeddingDescriptor() EmbeddingDescriptor { return s.embedder.Descriptor() }

// WriteArticleWithID inserts a new article under the caller-supplied id
// instead of letting Postgres assign one. This exists for
// wormhole.sync.incremental_push (RFC-0003 §8.2), which must preserve the
// client's local-first article id so the server-side row is findable by the
// id the client already has; ordinary article writes (wormhole.kb.write)
// have no local id to preserve and keep calling WriteArticle.
func (s *Store) WriteArticleWithID(ctx context.Context, id, projectID, agentID, title, body string, frontmatter json.RawMessage, linkTargetIDs []string, force bool) (Article, error) {
	return s.writeArticleWithOptionalID(ctx, id, "", projectID, agentID, title, body, frontmatter, linkTargetIDs, force)
}

// writeArticleWithOptionalID is the shared transaction/validation core of
// WriteArticle and WriteArticleWithID (dedup, conciseness, required-links
// checks, and link-target insertion are unchanged and unaffected by id).
// An empty id lets the INSERT column list omit id, so Postgres's
// gen_random_uuid() default fires; a non-empty id is included in the column
// list and args, so the row is inserted under that exact id (a duplicate id
// surfaces as a normal primary-key unique-violation error, same as any
// other store error today).
func (s *Store) writeArticleWithOptionalID(ctx context.Context, id, bootstrapKey, projectID, agentID, title, body string, frontmatter json.RawMessage, linkTargetIDs []string, force bool) (Article, error) {
	if err := s.authorizePassport(ctx, projectID, agentID, "write article"); err != nil {
		return Article{}, err
	}
	embedding, err := s.PrepareArticleEmbedding(ctx, title, body)
	if err != nil {
		return Article{}, fmt.Errorf("kb: write article: embed: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Article{}, fmt.Errorf("kb: write article: begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectID); err != nil {
		return Article{}, fmt.Errorf("kb: write article: set project id: %w", err)
	}

	article, err := s.writeArticleInTxWithOptionalID(ctx, tx, id, bootstrapKey, projectID, agentID, title, body, frontmatter, linkTargetIDs, force, embedding)
	if err != nil {
		return Article{}, err
	}
	if err := tx.Commit(); err != nil {
		return Article{}, fmt.Errorf("kb: write article: commit: %w", err)
	}
	return article, nil
}

func (s *Store) writeArticleInTxWithOptionalID(ctx context.Context, tx *sql.Tx, id, bootstrapKey, projectID, agentID, title, body string, frontmatter json.RawMessage, linkTargetIDs []string, force bool, embedding []float32) (Article, error) {
	// Verify agent has a passport for this project.
	var dummy int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM passports WHERE agent_id = $1 AND project_id = $2", agentID, projectID).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return Article{}, fmt.Errorf("kb: write article: agent not registered or has no passport for this project: %w", ErrPassportNotFound)
	} else if err != nil {
		return Article{}, fmt.Errorf("kb: write article: passport lookup: %w", err)
	}

	if len(frontmatter) == 0 {
		frontmatter = json.RawMessage(`{}`)
	}
	if id != "" {
		existing, found, err := s.readStableIDReplay(ctx, tx, id, projectID, agentID, title, body, frontmatter, linkTargetIDs)
		if err != nil {
			return Article{}, err
		}
		if found {
			return existing, nil
		}
	}

	bodyRunes := utf8.RuneCountInString(body)
	if !force && s.maxBodyLength > 0 && bodyRunes > s.maxBodyLength {
		return Article{}, &ErrConcisenessViolation{
			Length:    bodyRunes,
			MaxLength: s.maxBodyLength,
		}
	}

	descriptor := s.embedder.Descriptor()
	if err := validateEmbeddingBatch([]string{articleEmbeddingText(title, body)}, [][]float32{embedding}, descriptor.Dimension); err != nil {
		return Article{}, fmt.Errorf("kb: write article: prepared embedding: %w", err)
	}
	generation, err := ensureActiveEmbeddingGenerationTx(ctx, tx, projectID, descriptor)
	if err != nil {
		return Article{}, fmt.Errorf("kb: write article: active embedding generation: %w", err)
	}

	if !force {
		var existingID string
		var existingTitle string
		var similarity float64

		// pgvector cosine distance <= 1 - threshold maps to similarity >= threshold
		err = tx.QueryRowContext(ctx,
			`SELECT a.id, a.title, (1 - (e.embedding <=> $1::vector)) AS similarity
			 FROM kb_articles a
			 JOIN kb_article_embeddings e
			   ON e.article_id = a.id AND e.project_id = a.project_id
			 WHERE a.project_id = $2 AND e.generation_id = $3
			 ORDER BY e.embedding <=> $1::vector
			 LIMIT 1`,
			formatVectorLiteral(embedding), projectID, generation.ID,
		).Scan(&existingID, &existingTitle, &similarity)

		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Article{}, fmt.Errorf("kb: write article: dedup query: %w", err)
		}

		if err == nil && similarity >= s.dedupThreshold {
			return Article{}, &ErrDedupViolation{
				ExistingID:    existingID,
				ExistingTitle: existingTitle,
				Similarity:    similarity,
				Threshold:     s.dedupThreshold,
			}
		}
	}

	if !force {
		var fm struct {
			Type string `json:"type"`
		}
		if len(frontmatter) > 0 {
			_ = json.Unmarshal(frontmatter, &fm)
		}
		minLinks := 0
		switch strings.ToLower(fm.Type) {
		case "decision":
			minLinks = s.minLinksDecision
		case "policy":
			minLinks = s.minLinksPolicy
		case "procedure":
			minLinks = s.minLinksProcedure
		}
		if minLinks > 0 && len(linkTargetIDs) < minLinks {
			// Retrieve closest articles as suggestions using pgvector
			rows, err := tx.QueryContext(ctx,
				`SELECT a.id, a.title
				 FROM kb_articles a
				 JOIN kb_article_embeddings e
				   ON e.article_id = a.id AND e.project_id = a.project_id
				 WHERE a.project_id = $1 AND e.generation_id = $3
				 ORDER BY e.embedding <=> $2::vector
				 LIMIT 3`,
				projectID, formatVectorLiteral(embedding), generation.ID,
			)
			if err != nil {
				return Article{}, fmt.Errorf("kb: write article: link suggestions query: %w", err)
			}
			defer rows.Close()

			var suggestions []LinkSuggestion
			for rows.Next() {
				var sugg LinkSuggestion
				if err := rows.Scan(&sugg.ID, &sugg.Title); err != nil {
					return Article{}, fmt.Errorf("kb: write article: scan link suggestion: %w", err)
				}
				suggestions = append(suggestions, sugg)
			}
			if err := rows.Err(); err != nil {
				return Article{}, fmt.Errorf("kb: write article: iterate suggestions error: %w", err)
			}
			return Article{}, &ErrRequiredLinksViolation{
				ArticleType: fm.Type,
				LinkCount:   len(linkTargetIDs),
				MinLinks:    minLinks,
				Suggestions: suggestions,
			}
		}
	}

	var row *sql.Row
	if bootstrapKey != "" {
		row = tx.QueryRowContext(ctx,
			`INSERT INTO kb_articles (project_id, title, body, frontmatter, author_agent_id, bootstrap_key)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 ON CONFLICT (project_id, bootstrap_key) WHERE bootstrap_key IS NOT NULL
			 DO UPDATE SET bootstrap_key = EXCLUDED.bootstrap_key
			 RETURNING `+articleColumns,
			projectID, title, body, frontmatter, agentID, bootstrapKey,
		)
	} else if id == "" {
		row = tx.QueryRowContext(ctx,
			`INSERT INTO kb_articles (project_id, title, body, frontmatter, author_agent_id)
			 VALUES ($1, $2, $3, $4, $5)
			 RETURNING `+articleColumns,
			projectID, title, body, frontmatter, agentID,
		)
	} else {
		row = tx.QueryRowContext(ctx,
			`INSERT INTO kb_articles (id, project_id, title, body, frontmatter, author_agent_id)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 ON CONFLICT (id) DO NOTHING
			 RETURNING `+articleColumns,
			id, projectID, title, body, frontmatter, agentID,
		)
	}

	var article Article
	err = row.Scan(&article.ID, &article.ProjectID, &article.Title, &article.Body, &article.Frontmatter, &article.AuthorAgentID, &article.CreatedAt, &article.UpdatedAt)
	if id != "" && errors.Is(err, sql.ErrNoRows) {
		var found bool
		article, found, err = s.readStableIDReplay(ctx, tx, id, projectID, agentID, title, body, frontmatter, linkTargetIDs)
		if err != nil {
			return Article{}, err
		}
		if !found {
			return Article{}, fmt.Errorf("kb: write article: %w", ErrStableIDConflict)
		}
	}
	if err != nil {
		return Article{}, fmt.Errorf("kb: write article: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO kb_article_embeddings
			 (project_id, article_id, generation_id, provider, model, version, dimension, content_hash, embedding)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::vector)
			 ON CONFLICT (generation_id, article_id) DO NOTHING`,
		projectID, article.ID, generation.ID, descriptor.Provider, descriptor.Model,
		descriptor.Version, descriptor.Dimension, articleContentHash(article.Title, article.Body), formatVectorLiteral(embedding),
	); err != nil {
		return Article{}, fmt.Errorf("kb: write article: store embedding: %w", err)
	}

	for _, targetID := range linkTargetIDs {
		err = tx.QueryRowContext(ctx, "SELECT 1 FROM kb_articles WHERE id = $1 AND project_id = $2", targetID, projectID).Scan(&dummy)
		if errors.Is(err, sql.ErrNoRows) {
			return Article{}, ErrLinkedArticleNotFound
		} else if err != nil {
			return Article{}, fmt.Errorf("kb: write article: link target lookup: %w", err)
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO kb_links (project_id, from_article_id, to_article_id) VALUES ($1, $2, $3)`,
			projectID, article.ID, targetID,
		); err != nil {
			return Article{}, fmt.Errorf("kb: write article: insert link: %w", err)
		}
	}

	return article, nil
}

func (s *Store) readStableIDReplay(ctx context.Context, tx *sql.Tx, id, projectID, agentID, title, body string, frontmatter json.RawMessage, linkTargetIDs []string) (Article, bool, error) {
	var article Article
	err := tx.QueryRowContext(ctx,
		`SELECT `+articleColumns+` FROM kb_articles WHERE id = $1 AND project_id = $2`, id, projectID,
	).Scan(&article.ID, &article.ProjectID, &article.Title, &article.Body, &article.Frontmatter, &article.AuthorAgentID, &article.CreatedAt, &article.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Article{}, false, nil
	}
	if err != nil {
		return Article{}, false, fmt.Errorf("kb: write article: inspect stable id: %w", err)
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT to_article_id FROM kb_links WHERE project_id = $1 AND from_article_id = $2 ORDER BY to_article_id`, projectID, id)
	if err != nil {
		return Article{}, false, fmt.Errorf("kb: write article: inspect stable id links: %w", err)
	}
	defer rows.Close()
	actualLinks := make([]string, 0)
	for rows.Next() {
		var target string
		if err := rows.Scan(&target); err != nil {
			return Article{}, false, fmt.Errorf("kb: write article: inspect stable id link: %w", err)
		}
		actualLinks = append(actualLinks, target)
	}
	if err := rows.Err(); err != nil {
		return Article{}, false, fmt.Errorf("kb: write article: inspect stable id links: %w", err)
	}
	expectedLinks := append([]string{}, linkTargetIDs...)
	sort.Strings(expectedLinks)
	var actualFrontmatter, expectedFrontmatter any
	if json.Unmarshal(article.Frontmatter, &actualFrontmatter) != nil || json.Unmarshal(frontmatter, &expectedFrontmatter) != nil {
		return Article{}, false, fmt.Errorf("kb: write article: %w", ErrStableIDConflict)
	}
	if article.Title != title || article.Body != body || article.AuthorAgentID != agentID ||
		!reflect.DeepEqual(actualFrontmatter, expectedFrontmatter) || !reflect.DeepEqual(actualLinks, expectedLinks) {
		return Article{}, false, fmt.Errorf("kb: write article: %w", ErrStableIDConflict)
	}
	return article, true, nil
}

type RankingMetadata struct {
	SemanticApplied bool   `json:"semantic_applied"`
	GenerationID    string `json:"generation_id,omitempty"`
	Provider        string `json:"provider,omitempty"`
	Model           string `json:"model,omitempty"`
	Version         string `json:"version,omitempty"`
	Dimension       int    `json:"dimension,omitempty"`
	DistanceMetric  string `json:"distance_metric,omitempty"`
}

// SearchArticles preserves the original domain API for internal callers.
// MCP callers use SearchArticlesWithMetadata so they can report whether
// semantic ranking actually occurred.
func (s *Store) SearchArticles(ctx context.Context, projectID, agentID, query string, limit int) ([]Article, error) {
	articles, _, err := s.SearchArticlesWithMetadata(ctx, projectID, agentID, query, limit)
	return articles, err
}

// SearchArticlesWithMetadata performs generation-scoped cosine ranking. The
// paid provider call occurs after authorization commits and before the search
// transaction starts.
func (s *Store) SearchArticlesWithMetadata(ctx context.Context, projectID, agentID, query string, limit int) ([]Article, RankingMetadata, error) {
	if limit <= 0 {
		limit = 10
	}
	if err := s.authorizePassport(ctx, projectID, agentID, "search articles"); err != nil {
		return nil, RankingMetadata{}, err
	}

	embedding, err := embedOne(ctx, s.embedder, EmbeddingInputSearchQuery, query, EmbeddingModeInteractive)
	if err != nil {
		return nil, RankingMetadata{}, fmt.Errorf("kb: search articles: embed query: %w", err)
	}

	tx, err := s.beginProjectTx(ctx, projectID, "search articles")
	if err != nil {
		return nil, RankingMetadata{}, err
	}
	defer tx.Rollback()
	generation, err := activeEmbeddingGenerationTx(ctx, tx, projectID)
	if err != nil {
		return nil, RankingMetadata{}, fmt.Errorf("kb: search articles: %w", err)
	}
	descriptor := s.embedder.Descriptor()
	if generation.Descriptor != descriptor {
		return nil, RankingMetadata{}, fmt.Errorf("kb: search articles: %w", ErrEmbeddingGenerationMismatch)
	}
	metadata := RankingMetadata{
		SemanticApplied: true,
		GenerationID:    generation.ID,
		Provider:        descriptor.Provider,
		Model:           descriptor.Model,
		Version:         descriptor.Version,
		Dimension:       descriptor.Dimension,
		DistanceMetric:  "cosine",
	}

	// We use the cosine distance operator (<=>) for semantic search ranking.
	// Unlike dot product, pgvector's cosine distance operator computes the normalized angle
	// (1 - cosine similarity) and does not require the vectors to be pre-normalized.
	// It only requires the vectors to be non-zero, which our stub embedding naturally guarantees.
	rows, err := tx.QueryContext(ctx,
		`SELECT a.id, a.project_id, a.title, a.body, a.frontmatter,
		        a.author_agent_id, a.created_at, a.updated_at
		 FROM kb_articles a
		 JOIN kb_article_embeddings e
		   ON e.article_id = a.id AND e.project_id = a.project_id
		 WHERE a.project_id = $1 AND e.generation_id = $2
		 ORDER BY e.embedding <=> $3::vector
		 LIMIT $4`,
		projectID, generation.ID, formatVectorLiteral(embedding), limit,
	)
	if err != nil {
		return nil, RankingMetadata{}, fmt.Errorf("kb: search articles: query: %w", err)
	}
	defer rows.Close()

	var articles []Article
	for rows.Next() {
		var article Article
		err = rows.Scan(&article.ID, &article.ProjectID, &article.Title, &article.Body, &article.Frontmatter, &article.AuthorAgentID, &article.CreatedAt, &article.UpdatedAt)
		if err != nil {
			return nil, RankingMetadata{}, fmt.Errorf("kb: search articles: scan: %w", err)
		}
		articles = append(articles, article)
	}
	if err := rows.Err(); err != nil {
		return nil, RankingMetadata{}, fmt.Errorf("kb: search articles: iterate: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, RankingMetadata{}, fmt.Errorf("kb: search articles: commit: %w", err)
	}

	return articles, metadata, nil
}

func (s *Store) authorizePassport(ctx context.Context, projectID, agentID, operation string) error {
	tx, err := s.beginProjectTx(ctx, projectID, operation)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var dummy int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM passports WHERE agent_id = $1 AND project_id = $2", agentID, projectID).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("kb: %s: agent not registered or has no passport for this project: %w", operation, ErrPassportNotFound)
	}
	if err != nil {
		return fmt.Errorf("kb: %s: passport lookup: %w", operation, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("kb: %s: commit authorization: %w", operation, err)
	}
	return nil
}

// GetArticle retrieves a single KB article by ID within the calling agent's
// project scope (RFC-0001 §8.3). Returns ErrArticleNotFound if the article
// does not exist or belongs to a different project. Returns
// ErrPassportNotFound if the agent has no passport for this project.
func (s *Store) GetArticle(ctx context.Context, projectID, agentID, articleID string) (Article, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Article{}, fmt.Errorf("kb: get article: begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectID); err != nil {
		return Article{}, fmt.Errorf("kb: get article: set project id: %w", err)
	}

	// Verify agent has a passport for this project.
	var dummy int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM passports WHERE agent_id = $1 AND project_id = $2", agentID, projectID).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return Article{}, fmt.Errorf("kb: get article: agent not registered or has no passport for this project: %w", ErrPassportNotFound)
	} else if err != nil {
		return Article{}, fmt.Errorf("kb: get article: passport lookup: %w", err)
	}

	var article Article
	err = tx.QueryRowContext(ctx,
		`SELECT `+articleColumns+` FROM kb_articles WHERE id = $1 AND project_id = $2`,
		articleID, projectID,
	).Scan(&article.ID, &article.ProjectID, &article.Title, &article.Body, &article.Frontmatter, &article.AuthorAgentID, &article.CreatedAt, &article.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Article{}, ErrArticleNotFound
	} else if err != nil {
		return Article{}, fmt.Errorf("kb: get article: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Article{}, fmt.Errorf("kb: get article: commit: %w", err)
	}
	return article, nil
}

// GetArticleLinks returns the articles that the given article links to
// (one-hop outbound traversal of the kb_links graph, RFC-0001 §8.3).
// Returns ErrArticleNotFound if the source article does not exist in this
// project. Returns an empty slice (not nil) if the article has no outbound
// links. Returns ErrPassportNotFound if the agent has no passport for this
// project.
func (s *Store) GetArticleLinks(ctx context.Context, projectID, agentID, articleID string) ([]Article, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("kb: get article links: begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectID); err != nil {
		return nil, fmt.Errorf("kb: get article links: set project id: %w", err)
	}

	// Verify agent has a passport for this project.
	var dummy int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM passports WHERE agent_id = $1 AND project_id = $2", agentID, projectID).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("kb: get article links: agent not registered or has no passport for this project: %w", ErrPassportNotFound)
	} else if err != nil {
		return nil, fmt.Errorf("kb: get article links: passport lookup: %w", err)
	}

	// Verify the source article exists in-project.
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM kb_articles WHERE id = $1 AND project_id = $2", articleID, projectID).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrArticleNotFound
	} else if err != nil {
		return nil, fmt.Errorf("kb: get article links: source article lookup: %w", err)
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT a.id, a.project_id, a.title, a.body, a.frontmatter, a.author_agent_id, a.created_at, a.updated_at
		 FROM kb_articles a
		 JOIN kb_links l ON l.to_article_id = a.id
		 WHERE l.from_article_id = $1 AND l.project_id = $2
		 ORDER BY a.created_at ASC`,
		articleID, projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("kb: get article links: query: %w", err)
	}
	defer rows.Close()

	articles := []Article{}
	for rows.Next() {
		var article Article
		err = rows.Scan(&article.ID, &article.ProjectID, &article.Title, &article.Body, &article.Frontmatter, &article.AuthorAgentID, &article.CreatedAt, &article.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("kb: get article links: scan: %w", err)
		}
		articles = append(articles, article)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("kb: get article links: iterate: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("kb: get article links: commit: %w", err)
	}
	return articles, nil
}

// ListArticles returns every KB article in the project, newest first. Unlike
// SearchArticles this has no query/similarity component — it's the plain
// listing the read-only dashboard needs (Alpha-2 Chapter 9).
func (s *Store) ListArticles(ctx context.Context, projectID string) ([]Article, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("kb: list articles: begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectID); err != nil {
		return nil, fmt.Errorf("kb: list articles: set project id: %w", err)
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT `+articleColumns+`
		 FROM kb_articles
		 WHERE project_id = $1
		 ORDER BY created_at DESC`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("kb: list articles: query: %w", err)
	}
	defer rows.Close()

	var articles []Article
	for rows.Next() {
		var article Article
		err = rows.Scan(&article.ID, &article.ProjectID, &article.Title, &article.Body, &article.Frontmatter, &article.AuthorAgentID, &article.CreatedAt, &article.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("kb: list articles: scan: %w", err)
		}
		articles = append(articles, article)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("kb: list articles: iterate: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("kb: list articles: commit: %w", err)
	}

	return articles, nil
}

// ListBootstrapInTx reads every project article through the caller's
// repeatable-read transaction in the bootstrap contract's deterministic order.
func (s *Store) ListBootstrapInTx(ctx context.Context, tx *sql.Tx, projectID string) ([]types.BootstrapArticleV1, error) {
	if tx == nil || projectID == "" {
		return nil, fmt.Errorf("kb: list bootstrap: invalid scope")
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT `+articleColumns+`
		FROM kb_articles
		WHERE project_id = $1
		ORDER BY created_at DESC, id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("kb: list bootstrap: query: %w", err)
	}
	defer rows.Close()
	articles := make([]types.BootstrapArticleV1, 0)
	for rows.Next() {
		var article types.BootstrapArticleV1
		if err := rows.Scan(&article.ID, &article.ProjectID, &article.Title, &article.Body,
			&article.Frontmatter, &article.AuthorAgentID, &article.CreatedAt, &article.UpdatedAt); err != nil {
			return nil, fmt.Errorf("kb: list bootstrap: scan: %w", err)
		}
		articles = append(articles, article)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("kb: list bootstrap: iterate: %w", err)
	}
	return articles, nil
}
