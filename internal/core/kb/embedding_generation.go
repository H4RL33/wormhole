package kb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

var ErrEmbeddingGenerationMismatch = errors.New("kb: active embedding generation does not match configured model")
var ErrEmbeddingGenerationIncomplete = errors.New("kb: embedding generation is incomplete")
var ErrSemanticIndexUnavailable = errors.New("kb: semantic index unavailable")

const embeddingRebuildBatchSize = 64

type EmbeddingGenerationState string

const (
	EmbeddingGenerationBuilding EmbeddingGenerationState = "building"
	EmbeddingGenerationActive   EmbeddingGenerationState = "active"
	EmbeddingGenerationFailed   EmbeddingGenerationState = "failed"
	EmbeddingGenerationRetired  EmbeddingGenerationState = "retired"
)

type EmbeddingGeneration struct {
	ID          string
	ProjectID   string
	Descriptor  EmbeddingDescriptor
	State       EmbeddingGenerationState
	FailureCode string
	CreatedAt   time.Time
}

type embeddingArticleSnapshot struct {
	ID          string
	Title       string
	Body        string
	ContentHash string
}

func articleContentHash(title, body string) string {
	digest := sha256.Sum256([]byte(articleEmbeddingText(title, body)))
	return hex.EncodeToString(digest[:])
}

const embeddingGenerationColumns = `id, project_id, provider, model, version, dimension, state, COALESCE(failure_code, ''), created_at`

func scanEmbeddingGeneration(row *sql.Row) (EmbeddingGeneration, error) {
	var generation EmbeddingGeneration
	err := row.Scan(
		&generation.ID,
		&generation.ProjectID,
		&generation.Descriptor.Provider,
		&generation.Descriptor.Model,
		&generation.Descriptor.Version,
		&generation.Descriptor.Dimension,
		&generation.State,
		&generation.FailureCode,
		&generation.CreatedAt,
	)
	return generation, err
}

func validateStoredDescriptor(descriptor EmbeddingDescriptor) error {
	if descriptor.Provider == "" || descriptor.Model == "" || descriptor.Version == "" || descriptor.Dimension != approvedEmbeddingDimension {
		return ErrEmbeddingConfiguration
	}
	return nil
}

// CreateEmbeddingGeneration creates an isolated building generation. It does
// not change the currently active generation.
func (s *Store) CreateEmbeddingGeneration(ctx context.Context, projectID string, descriptor EmbeddingDescriptor) (EmbeddingGeneration, error) {
	if err := validateStoredDescriptor(descriptor); err != nil {
		return EmbeddingGeneration{}, err
	}
	tx, err := s.beginProjectTx(ctx, projectID, "create embedding generation")
	if err != nil {
		return EmbeddingGeneration{}, err
	}
	defer tx.Rollback()
	generation, err := scanEmbeddingGeneration(tx.QueryRowContext(ctx,
		`INSERT INTO kb_embedding_generations (project_id, provider, model, version, dimension, state)
		 VALUES ($1, $2, $3, $4, $5, 'building')
		 RETURNING `+embeddingGenerationColumns,
		projectID, descriptor.Provider, descriptor.Model, descriptor.Version, descriptor.Dimension,
	))
	if err != nil {
		return EmbeddingGeneration{}, fmt.Errorf("kb: create embedding generation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return EmbeddingGeneration{}, fmt.Errorf("kb: create embedding generation: commit: %w", err)
	}
	return generation, nil
}

// MarkEmbeddingGenerationFailed records a safe machine-readable failure code;
// provider response bodies and article text never enter this table.
func (s *Store) MarkEmbeddingGenerationFailed(ctx context.Context, projectID, generationID, failureCode string) error {
	if failureCode == "" {
		return fmt.Errorf("kb: mark embedding generation failed: empty failure code")
	}
	tx, err := s.beginProjectTx(ctx, projectID, "mark embedding generation failed")
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx,
		`UPDATE kb_embedding_generations
		 SET state = 'failed', failure_code = $3, failed_at = now()
		 WHERE id = $1 AND project_id = $2 AND state = 'building'`,
		generationID, projectID, failureCode,
	)
	if err != nil {
		return fmt.Errorf("kb: mark embedding generation failed: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return fmt.Errorf("kb: mark embedding generation failed: generation is not building")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("kb: mark embedding generation failed: commit: %w", err)
	}
	return nil
}

// StoreGenerationEmbedding writes one validated vector into a building
// generation. It is intentionally separate from provider invocation so a
// rebuild worker never holds a database transaction across a remote call.
func (s *Store) StoreGenerationEmbedding(ctx context.Context, projectID, generationID, articleID string, vector []float32) error {
	tx, err := s.beginProjectTx(ctx, projectID, "read generation article")
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var snapshot embeddingArticleSnapshot
	if err := tx.QueryRowContext(ctx,
		`SELECT id, title, body FROM kb_articles WHERE id = $1 AND project_id = $2`, articleID, projectID,
	).Scan(&snapshot.ID, &snapshot.Title, &snapshot.Body); err != nil {
		return fmt.Errorf("kb: store generation embedding: article lookup: %w", err)
	}
	snapshot.ContentHash = articleContentHash(snapshot.Title, snapshot.Body)
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("kb: store generation embedding: commit article lookup: %w", err)
	}
	_, err = s.storeGenerationEmbeddingBatch(ctx, projectID, generationID, []embeddingArticleSnapshot{snapshot}, [][]float32{vector})
	return err
}

func (s *Store) storeGenerationEmbeddingBatch(ctx context.Context, projectID, generationID string, snapshots []embeddingArticleSnapshot, vectors [][]float32) (int, error) {
	descriptor := s.embedder.Descriptor()
	if err := validateStoredDescriptor(descriptor); err != nil {
		return 0, err
	}
	texts := make([]string, len(snapshots))
	for i := range texts {
		texts[i] = "stored"
	}
	if err := validateEmbeddingBatch(texts, vectors, descriptor.Dimension); err != nil {
		return 0, err
	}
	tx, err := s.beginProjectTx(ctx, projectID, "store generation embedding")
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := lockEmbeddingProjectTx(ctx, tx, projectID); err != nil {
		return 0, err
	}
	var state EmbeddingGenerationState
	var stored EmbeddingDescriptor
	if err := tx.QueryRowContext(ctx,
		`SELECT state, provider, model, version, dimension
		 FROM kb_embedding_generations WHERE id = $1 AND project_id = $2 FOR UPDATE`,
		generationID, projectID,
	).Scan(&state, &stored.Provider, &stored.Model, &stored.Version, &stored.Dimension); err != nil {
		return 0, fmt.Errorf("kb: store generation embedding: generation lookup: %w", err)
	}
	if state != EmbeddingGenerationBuilding {
		return 0, fmt.Errorf("kb: store generation embedding: generation is not building")
	}
	if stored != descriptor {
		return 0, ErrEmbeddingGenerationMismatch
	}
	storedCount := 0
	for i, snapshot := range snapshots {
		var title, body string
		err := tx.QueryRowContext(ctx,
			`SELECT title, body FROM kb_articles WHERE id = $1 AND project_id = $2`, snapshot.ID, projectID,
		).Scan(&title, &body)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("kb: store generation embedding: article lookup: %w", err)
		}
		if articleContentHash(title, body) != snapshot.ContentHash {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO kb_article_embeddings
			 (project_id, article_id, generation_id, provider, model, version, dimension, content_hash, embedding)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::vector)
			 ON CONFLICT (generation_id, article_id)
			 DO UPDATE SET content_hash = EXCLUDED.content_hash, embedding = EXCLUDED.embedding, created_at = now()`,
			projectID, snapshot.ID, generationID, descriptor.Provider, descriptor.Model,
			descriptor.Version, descriptor.Dimension, snapshot.ContentHash, formatVectorLiteral(vectors[i]),
		); err != nil {
			return 0, fmt.Errorf("kb: store generation embedding: %w", err)
		}
		storedCount++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("kb: store generation embedding: commit: %w", err)
	}
	return storedCount, nil
}

// ActivateEmbeddingGeneration swaps generations atomically and only after the
// candidate contains exactly one vector for every current project article.
// The previous active generation and its vectors are retained as retired.
func (s *Store) ActivateEmbeddingGeneration(ctx context.Context, projectID, generationID string) error {
	tx, err := s.beginProjectTx(ctx, projectID, "activate embedding generation")
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockEmbeddingProjectTx(ctx, tx, projectID); err != nil {
		return err
	}
	if err := s.activateEmbeddingGenerationTx(ctx, tx, projectID, generationID, EmbeddingGenerationBuilding); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("kb: activate embedding generation: commit: %w", err)
	}
	return nil
}

// RebuildEmbeddingGeneration constructs a replacement beside the active
// generation. Provider calls happen outside transactions; short project-locked
// transactions store batches and the final catch-up verifies content hashes
// before retiring and activating generations atomically. If a retired
// generation for the configured descriptor still exactly matches current
// articles, it is reactivated without another paid provider call.
func (s *Store) RebuildEmbeddingGeneration(ctx context.Context, projectID string) (EmbeddingGeneration, error) {
	if generation, ok, err := s.restoreCurrentGeneration(ctx, projectID); err != nil {
		return EmbeddingGeneration{}, err
	} else if ok {
		return generation, nil
	}
	candidate, err := s.CreateEmbeddingGeneration(ctx, projectID, s.embedder.Descriptor())
	if err != nil {
		return EmbeddingGeneration{}, err
	}
	fail := func(rebuildErr error) (EmbeddingGeneration, error) {
		failureCode := "rebuild_failed"
		var failure *EmbeddingFailure
		if errors.As(rebuildErr, &failure) && failure.Code != "" {
			failureCode = failure.Code
		} else if errors.Is(rebuildErr, context.Canceled) || errors.Is(rebuildErr, context.DeadlineExceeded) {
			failureCode = "rebuild_cancelled"
		}
		candidate.State = EmbeddingGenerationFailed
		candidate.FailureCode = failureCode
		markCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if markErr := s.MarkEmbeddingGenerationFailed(markCtx, projectID, candidate.ID, failureCode); markErr != nil {
			return candidate, fmt.Errorf("kb: rebuild embedding generation: %v; mark failed: %w", rebuildErr, markErr)
		}
		return candidate, fmt.Errorf("kb: rebuild embedding generation: %w", rebuildErr)
	}

	for {
		snapshots, err := s.embeddingRebuildWork(ctx, projectID, candidate.ID)
		if err != nil {
			return fail(err)
		}
		if len(snapshots) == 0 {
			generation, err := s.finalizeEmbeddingGeneration(ctx, projectID, candidate.ID)
			if err == nil {
				return generation, nil
			}
			if !errors.Is(err, ErrEmbeddingGenerationIncomplete) {
				return fail(err)
			}
			continue
		}
		for start := 0; start < len(snapshots); start += embeddingRebuildBatchSize {
			end := start + embeddingRebuildBatchSize
			if end > len(snapshots) {
				end = len(snapshots)
			}
			batch := snapshots[start:end]
			texts := make([]string, len(batch))
			for i := range batch {
				texts[i] = articleEmbeddingText(batch[i].Title, batch[i].Body)
			}
			vectors, err := s.embedder.Embed(ctx, EmbeddingRequest{InputType: EmbeddingInputSearchDocument, Texts: texts, Mode: EmbeddingModeReembedding})
			if err != nil {
				return fail(err)
			}
			if err := validateEmbeddingBatch(texts, vectors, s.embedder.Descriptor().Dimension); err != nil {
				return fail(err)
			}
			if _, err := s.storeGenerationEmbeddingBatch(ctx, projectID, candidate.ID, batch, vectors); err != nil {
				return fail(err)
			}
		}
	}
}

func (s *Store) embeddingRebuildWork(ctx context.Context, projectID, generationID string) ([]embeddingArticleSnapshot, error) {
	tx, err := s.beginProjectTx(ctx, projectID, "inspect embedding rebuild")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx,
		`SELECT a.id, a.title, a.body, COALESCE(e.content_hash, '')
		 FROM kb_articles a
		 LEFT JOIN kb_article_embeddings e
		   ON e.project_id = a.project_id AND e.article_id = a.id AND e.generation_id = $2
		 WHERE a.project_id = $1
		 ORDER BY a.id`, projectID, generationID)
	if err != nil {
		return nil, fmt.Errorf("kb: inspect embedding rebuild: %w", err)
	}
	defer rows.Close()
	var work []embeddingArticleSnapshot
	for rows.Next() {
		var snapshot embeddingArticleSnapshot
		var storedHash string
		if err := rows.Scan(&snapshot.ID, &snapshot.Title, &snapshot.Body, &storedHash); err != nil {
			return nil, fmt.Errorf("kb: inspect embedding rebuild: scan: %w", err)
		}
		snapshot.ContentHash = articleContentHash(snapshot.Title, snapshot.Body)
		if storedHash != snapshot.ContentHash {
			work = append(work, snapshot)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("kb: inspect embedding rebuild: iterate: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("kb: inspect embedding rebuild: commit: %w", err)
	}
	return work, nil
}

func (s *Store) finalizeEmbeddingGeneration(ctx context.Context, projectID, generationID string) (EmbeddingGeneration, error) {
	tx, err := s.beginProjectTx(ctx, projectID, "finalize embedding generation")
	if err != nil {
		return EmbeddingGeneration{}, err
	}
	defer tx.Rollback()
	if err := lockEmbeddingProjectTx(ctx, tx, projectID); err != nil {
		return EmbeddingGeneration{}, err
	}
	complete, err := embeddingGenerationMatchesArticlesTx(ctx, tx, projectID, generationID)
	if err != nil {
		return EmbeddingGeneration{}, err
	}
	if !complete {
		return EmbeddingGeneration{}, ErrEmbeddingGenerationIncomplete
	}
	if err := s.activateEmbeddingGenerationTx(ctx, tx, projectID, generationID, EmbeddingGenerationBuilding); err != nil {
		return EmbeddingGeneration{}, err
	}
	generation, err := scanEmbeddingGeneration(tx.QueryRowContext(ctx,
		`SELECT `+embeddingGenerationColumns+` FROM kb_embedding_generations WHERE id = $1 AND project_id = $2`, generationID, projectID))
	if err != nil {
		return EmbeddingGeneration{}, fmt.Errorf("kb: finalize embedding generation: read active: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return EmbeddingGeneration{}, fmt.Errorf("kb: finalize embedding generation: commit: %w", err)
	}
	return generation, nil
}

func (s *Store) restoreCurrentGeneration(ctx context.Context, projectID string) (EmbeddingGeneration, bool, error) {
	tx, err := s.beginProjectTx(ctx, projectID, "restore embedding generation")
	if err != nil {
		return EmbeddingGeneration{}, false, err
	}
	defer tx.Rollback()
	if err := lockEmbeddingProjectTx(ctx, tx, projectID); err != nil {
		return EmbeddingGeneration{}, false, err
	}
	descriptor := s.embedder.Descriptor()
	active, err := activeEmbeddingGenerationTx(ctx, tx, projectID)
	if err == nil && active.Descriptor == descriptor {
		complete, matchErr := embeddingGenerationMatchesArticlesTx(ctx, tx, projectID, active.ID)
		if matchErr != nil {
			return EmbeddingGeneration{}, false, matchErr
		}
		if complete {
			if err := tx.Commit(); err != nil {
				return EmbeddingGeneration{}, false, err
			}
			return active, true, nil
		}
	}
	if err != nil && !errors.Is(err, ErrSemanticIndexUnavailable) {
		return EmbeddingGeneration{}, false, err
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT `+embeddingGenerationColumns+`
		 FROM kb_embedding_generations
		 WHERE project_id = $1 AND state = 'retired'
		   AND provider = $2 AND model = $3 AND version = $4 AND dimension = $5
		 ORDER BY retired_at DESC`,
		projectID, descriptor.Provider, descriptor.Model, descriptor.Version, descriptor.Dimension)
	if err != nil {
		return EmbeddingGeneration{}, false, err
	}
	var retired []EmbeddingGeneration
	for rows.Next() {
		var generation EmbeddingGeneration
		if err := rows.Scan(&generation.ID, &generation.ProjectID, &generation.Descriptor.Provider, &generation.Descriptor.Model, &generation.Descriptor.Version, &generation.Descriptor.Dimension, &generation.State, &generation.FailureCode, &generation.CreatedAt); err != nil {
			rows.Close()
			return EmbeddingGeneration{}, false, err
		}
		retired = append(retired, generation)
	}
	if err := rows.Close(); err != nil {
		return EmbeddingGeneration{}, false, err
	}
	for _, generation := range retired {
		complete, err := embeddingGenerationMatchesArticlesTx(ctx, tx, projectID, generation.ID)
		if err != nil {
			return EmbeddingGeneration{}, false, err
		}
		if !complete {
			continue
		}
		if err := s.activateEmbeddingGenerationTx(ctx, tx, projectID, generation.ID, EmbeddingGenerationRetired); err != nil {
			return EmbeddingGeneration{}, false, err
		}
		generation.State = EmbeddingGenerationActive
		if err := tx.Commit(); err != nil {
			return EmbeddingGeneration{}, false, err
		}
		return generation, true, nil
	}
	if err := tx.Commit(); err != nil {
		return EmbeddingGeneration{}, false, err
	}
	return EmbeddingGeneration{}, false, nil
}

func embeddingGenerationMatchesArticlesTx(ctx context.Context, tx *sql.Tx, projectID, generationID string) (bool, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT a.title, a.body, COALESCE(e.content_hash, '')
		 FROM kb_articles a
		 LEFT JOIN kb_article_embeddings e
		   ON e.project_id = a.project_id AND e.article_id = a.id AND e.generation_id = $2
		 WHERE a.project_id = $1`, projectID, generationID)
	if err != nil {
		return false, fmt.Errorf("kb: verify embedding generation: %w", err)
	}
	defer rows.Close()
	articleCount := 0
	for rows.Next() {
		var title, body, storedHash string
		if err := rows.Scan(&title, &body, &storedHash); err != nil {
			return false, fmt.Errorf("kb: verify embedding generation: scan: %w", err)
		}
		articleCount++
		if storedHash != articleContentHash(title, body) {
			return false, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("kb: verify embedding generation: iterate: %w", err)
	}
	var embeddingCount int
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM kb_article_embeddings WHERE project_id = $1 AND generation_id = $2`, projectID, generationID,
	).Scan(&embeddingCount); err != nil {
		return false, fmt.Errorf("kb: verify embedding generation: count: %w", err)
	}
	return articleCount == embeddingCount, nil
}

func (s *Store) activateEmbeddingGenerationTx(ctx context.Context, tx *sql.Tx, projectID, generationID string, requiredState EmbeddingGenerationState) error {
	var state EmbeddingGenerationState
	var candidateDescriptor EmbeddingDescriptor
	if err := tx.QueryRowContext(ctx,
		`SELECT state, provider, model, version, dimension
		 FROM kb_embedding_generations WHERE id = $1 AND project_id = $2 FOR UPDATE`, generationID, projectID,
	).Scan(&state, &candidateDescriptor.Provider, &candidateDescriptor.Model, &candidateDescriptor.Version, &candidateDescriptor.Dimension); err != nil {
		return fmt.Errorf("kb: activate embedding generation: lookup: %w", err)
	}
	if state != requiredState {
		return fmt.Errorf("kb: activate embedding generation: generation is not %s", requiredState)
	}
	configuredDescriptor := s.embedder.Descriptor()
	if candidateDescriptor != configuredDescriptor {
		return fmt.Errorf("kb: activate embedding generation: %w: candidate=%s configured=%s", ErrEmbeddingGenerationMismatch, formatEmbeddingDescriptor(candidateDescriptor), formatEmbeddingDescriptor(configuredDescriptor))
	}
	complete, err := embeddingGenerationMatchesArticlesTx(ctx, tx, projectID, generationID)
	if err != nil {
		return err
	}
	if !complete {
		return fmt.Errorf("kb: activate embedding generation: %w", ErrEmbeddingGenerationIncomplete)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE kb_embedding_generations SET state = 'retired', retired_at = now()
		 WHERE project_id = $1 AND state = 'active' AND id <> $2`, projectID, generationID); err != nil {
		return fmt.Errorf("kb: activate embedding generation: retire active: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE kb_embedding_generations
		 SET state = 'active', activated_at = now(), retired_at = NULL
		 WHERE id = $1 AND project_id = $2`, generationID, projectID); err != nil {
		return fmt.Errorf("kb: activate embedding generation: activate candidate: %w", err)
	}
	return nil
}

func (s *Store) beginProjectTx(ctx context.Context, projectID, operation string) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("kb: %s: begin tx: %w", operation, err)
	}
	if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectID); err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("kb: %s: set project id: %w", operation, err)
	}
	return tx, nil
}

func activeEmbeddingGenerationTx(ctx context.Context, tx *sql.Tx, projectID string) (EmbeddingGeneration, error) {
	generation, err := scanEmbeddingGeneration(tx.QueryRowContext(ctx,
		`SELECT `+embeddingGenerationColumns+`
		 FROM kb_embedding_generations WHERE project_id = $1 AND state = 'active'`, projectID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return EmbeddingGeneration{}, ErrSemanticIndexUnavailable
	}
	if err != nil {
		return EmbeddingGeneration{}, fmt.Errorf("kb: active embedding generation: %w", err)
	}
	return generation, nil
}

func ensureActiveEmbeddingGenerationTx(ctx context.Context, tx *sql.Tx, projectID string, descriptor EmbeddingDescriptor) (EmbeddingGeneration, error) {
	if err := validateStoredDescriptor(descriptor); err != nil {
		return EmbeddingGeneration{}, err
	}
	if err := lockEmbeddingProjectTx(ctx, tx, projectID); err != nil {
		return EmbeddingGeneration{}, err
	}
	generation, err := activeEmbeddingGenerationTx(ctx, tx, projectID)
	if errors.Is(err, ErrSemanticIndexUnavailable) {
		generation, err = scanEmbeddingGeneration(tx.QueryRowContext(ctx,
			`INSERT INTO kb_embedding_generations
			 (project_id, provider, model, version, dimension, state, activated_at)
			 VALUES ($1, $2, $3, $4, $5, 'active', now())
			 RETURNING `+embeddingGenerationColumns,
			projectID, descriptor.Provider, descriptor.Model, descriptor.Version, descriptor.Dimension,
		))
	}
	if err != nil {
		return EmbeddingGeneration{}, err
	}
	if generation.Descriptor != descriptor {
		return EmbeddingGeneration{}, fmt.Errorf("%w: active=%s configured=%s", ErrEmbeddingGenerationMismatch, formatEmbeddingDescriptor(generation.Descriptor), formatEmbeddingDescriptor(descriptor))
	}
	return generation, nil
}

// lockEmbeddingProjectTx serializes article writes, rebuild vector writes, and
// generation activation for one project. This makes the activation
// completeness count stable until the active-generation swap commits.
func lockEmbeddingProjectTx(ctx context.Context, tx *sql.Tx, projectID string) error {
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 5760469405417790533))`, projectID); err != nil {
		return fmt.Errorf("kb: lock embedding project: %w", err)
	}
	return nil
}
