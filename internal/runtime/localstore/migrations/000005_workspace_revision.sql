ALTER TABLE workspace_bindings
ADD COLUMN workspace_revision INTEGER NOT NULL DEFAULT 1
CHECK(typeof(workspace_revision) = 'integer' AND workspace_revision >= 1);

DROP INDEX workspace_one_acceptance_eligible_candidate;

CREATE UNIQUE INDEX workspace_one_current_materialization
ON workspace_materializations(project_id, workspace_id)
WHERE state IN ('prepared', 'published', 'recovered_new');
