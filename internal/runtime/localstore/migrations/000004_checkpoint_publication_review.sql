ALTER TABLE workspace_materializations
  ADD COLUMN publication_review_json TEXT;

ALTER TABLE workspace_materializations
  ADD COLUMN prior_candidate_json TEXT;

ALTER TABLE workspace_materializations
  ADD COLUMN publication_review_proof_version INTEGER NOT NULL DEFAULT 0 CHECK(
    (publication_review_proof_version=0 AND publication_review_json IS NULL AND prior_candidate_json IS NULL) OR
    (publication_review_proof_version=1 AND publication_review_json IS NOT NULL AND prior_candidate_json IS NOT NULL)
  );
