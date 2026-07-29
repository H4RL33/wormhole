CREATE TABLE workspace_conflicts_v2 (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  occurrence_id TEXT NOT NULL,
  conflict_id TEXT NOT NULL,
  record_kind TEXT NOT NULL,
  record_id TEXT NOT NULL,
  field_path TEXT NOT NULL,
  conflict_kind TEXT NOT NULL,
  base_json TEXT NOT NULL,
  ours_json TEXT NOT NULL,
  theirs_json TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('open','resolved')),
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  resolved_at TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id,occurrence_id),
  FOREIGN KEY(project_id,workspace_id)
    REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE
);

CREATE TABLE portable_transition_copy_counts (
  name TEXT PRIMARY KEY,
  source_count INTEGER NOT NULL,
  destination_count INTEGER NOT NULL,
  CHECK(source_count = destination_count)
);

INSERT INTO workspace_conflicts_v2
  (project_id,workspace_id,occurrence_id,conflict_id,record_kind,record_id,field_path,
   conflict_kind,base_json,ours_json,theirs_json,state,created_at,resolved_at)
SELECT
  project_id,workspace_id,conflict_id,conflict_id,record_kind,record_id,field_path,
  conflict_kind,base_json,ours_json,theirs_json,state,created_at,resolved_at
FROM workspace_conflicts;

INSERT INTO portable_transition_copy_counts(name,source_count,destination_count)
VALUES (
  'workspace_conflicts',
  (SELECT count(*) FROM workspace_conflicts),
  (SELECT count(*) FROM workspace_conflicts_v2)
);

DROP TABLE workspace_conflicts;
ALTER TABLE workspace_conflicts_v2 RENAME TO workspace_conflicts;
CREATE UNIQUE INDEX workspace_one_open_semantic_conflict
  ON workspace_conflicts(project_id,workspace_id,conflict_id)
  WHERE state='open';
CREATE INDEX workspace_open_conflicts
  ON workspace_conflicts(project_id,workspace_id,state);

CREATE TABLE workspace_overlay_operations_v2 (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  generation INTEGER NOT NULL CHECK(generation > 0),
  operation_id TEXT NOT NULL,
  operation_json TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('active','rebased','stashed','materialized','discarded')),
  stashed_by_stash_id TEXT,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id,generation),
  UNIQUE(project_id,workspace_id,operation_id),
  CHECK(state='stashed' OR stashed_by_stash_id IS NULL),
  FOREIGN KEY(project_id,workspace_id)
    REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE
);

INSERT INTO workspace_overlay_operations_v2
  (project_id,workspace_id,generation,operation_id,operation_json,state,stashed_by_stash_id,created_at)
SELECT
  project_id,workspace_id,generation,operation_id,operation_json,state,NULL,created_at
FROM workspace_overlay_operations;

INSERT INTO portable_transition_copy_counts(name,source_count,destination_count)
VALUES (
  'workspace_overlay_operations',
  (SELECT count(*) FROM workspace_overlay_operations),
  (SELECT count(*) FROM workspace_overlay_operations_v2)
);

DROP TABLE workspace_overlay_operations;
ALTER TABLE workspace_overlay_operations_v2 RENAME TO workspace_overlay_operations;
CREATE INDEX workspace_overlay_generation
  ON workspace_overlay_operations(project_id,workspace_id,generation);

ALTER TABLE workspace_materializations
  ADD COLUMN included_operations_json TEXT;

CREATE TABLE workspace_transition_receipts (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  request_id TEXT NOT NULL,
  action TEXT NOT NULL CHECK(action IN ('stash','restore','discard')),
  request_digest TEXT NOT NULL,
  actor_json TEXT NOT NULL,
  result_json TEXT NOT NULL,
  outcome TEXT NOT NULL CHECK(outcome IN ('clean','conflicted')),
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id,request_id),
  FOREIGN KEY(project_id,workspace_id)
    REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX workspace_one_acceptance_eligible_candidate
  ON workspace_materializations(project_id,workspace_id)
  WHERE state IN ('published','recovered_new');

DROP TABLE portable_transition_copy_counts;
