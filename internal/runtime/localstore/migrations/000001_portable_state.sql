CREATE TABLE gateway_schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE workspace_bindings (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  checkout_path TEXT NOT NULL,
  checkout_device INTEGER NOT NULL,
  checkout_inode INTEGER NOT NULL,
  repository_identity_json TEXT NOT NULL,
  accepted_ref TEXT NOT NULL,
  accepted_commit TEXT NOT NULL,
  accepted_digest TEXT NOT NULL,
  accepted_snapshot BLOB NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('clean','pending','conflicted','blocked')),
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id),
  UNIQUE(checkout_device,checkout_inode)
);
CREATE TABLE workspace_candidates (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  accepted_base_digest TEXT NOT NULL,
  working_tree_digest TEXT NOT NULL,
  direct_tree BLOB NOT NULL,
  rebased_tree BLOB,
  rebased_through_generation INTEGER NOT NULL DEFAULT 0,
  imported_by TEXT NOT NULL,
  imported_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id),
  FOREIGN KEY(project_id,workspace_id) REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE
);
CREATE TABLE workspace_overlay_operations (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  generation INTEGER NOT NULL CHECK(generation > 0),
  operation_id TEXT NOT NULL,
  operation_json TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('active','rebased','stashed','materialized')),
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id,generation),
  UNIQUE(project_id,workspace_id,operation_id),
  FOREIGN KEY(project_id,workspace_id) REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE
);
CREATE TABLE workspace_materializations (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  journal_id TEXT NOT NULL,
  expected_live_digest TEXT NOT NULL,
  accepted_base_digest TEXT NOT NULL,
  checkout_path TEXT NOT NULL,
  checkout_device INTEGER NOT NULL,
  checkout_inode INTEGER NOT NULL,
  prior_tree_digest TEXT NOT NULL,
  candidate_digest TEXT NOT NULL,
  through_generation INTEGER NOT NULL,
  prior_tree BLOB NOT NULL,
  candidate_tree BLOB NOT NULL,
  stage_path TEXT NOT NULL,
  backup_path TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('prepared','published','accepted','recovered_old','recovered_new')),
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id,journal_id),
  FOREIGN KEY(project_id,workspace_id) REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE
);
CREATE TABLE workspace_stashes (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  stash_id TEXT NOT NULL,
  source_base_digest TEXT NOT NULL,
  candidate_digest TEXT NOT NULL,
  source_tree BLOB NOT NULL,
  composed_tree BLOB NOT NULL,
  operations_json TEXT NOT NULL,
  through_generation INTEGER NOT NULL,
  actor_json TEXT NOT NULL,
  label TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id,stash_id),
  FOREIGN KEY(project_id,workspace_id) REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE
);
CREATE TABLE workspace_conflicts (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
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
  PRIMARY KEY(project_id,workspace_id,conflict_id),
  FOREIGN KEY(project_id,workspace_id) REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE
);
CREATE TABLE legacy_integration_state_migrations (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  source_digest TEXT NOT NULL,
  outcome TEXT NOT NULL CHECK(outcome IN ('imported_move_pending','migrated_and_moved','migrated_tracked_source_retained','ignored_unsafe')),
  backup_path TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL,
  migrated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id,source_digest),
  FOREIGN KEY(project_id,workspace_id) REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE
);
CREATE INDEX workspace_overlay_generation ON workspace_overlay_operations(project_id,workspace_id,generation);
CREATE INDEX workspace_open_conflicts ON workspace_conflicts(project_id,workspace_id,state);
CREATE INDEX workspace_recovery ON workspace_materializations(state,project_id,workspace_id);
CREATE UNIQUE INDEX legacy_integration_one_pending ON legacy_integration_state_migrations(project_id,workspace_id) WHERE outcome='imported_move_pending';
