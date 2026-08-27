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
  workspace_revision INTEGER NOT NULL DEFAULT 1 CHECK(typeof(workspace_revision) = 'integer' AND workspace_revision >= 1),
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
  state TEXT NOT NULL CHECK(state IN ('active','rebased','stashed','materialized','discarded')),
  stashed_by_stash_id TEXT,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id,generation),
  UNIQUE(project_id,workspace_id,operation_id),
  CHECK(state='stashed' OR stashed_by_stash_id IS NULL),
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
  included_operations_json TEXT,
  publication_review_json TEXT,
  prior_candidate_json TEXT,
  publication_review_proof_version INTEGER NOT NULL DEFAULT 1 CHECK(
    (publication_review_proof_version=1 AND publication_review_json IS NOT NULL AND prior_candidate_json IS NOT NULL)
  ),
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
  FOREIGN KEY(project_id,workspace_id) REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE
);

CREATE TABLE workspace_publication_policies (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  repository_identity_json TEXT NOT NULL,
  origin_digest TEXT,
  classification TEXT NOT NULL CHECK(classification IN ('unclassified','local_only','public_git','private_git')),
  policy_revision INTEGER NOT NULL CHECK(policy_revision > 0),
  transition_kind TEXT NOT NULL CHECK(transition_kind IN ('bootstrap','configured','origin_invalidated','repository_invalidated')),
  changed_actor_json TEXT,
  changed_at TIMESTAMP,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id),
  CHECK((transition_kind='bootstrap' AND classification='unclassified' AND origin_digest IS NULL AND changed_actor_json IS NULL AND changed_at IS NULL) OR (transition_kind='configured' AND origin_digest IS NOT NULL AND changed_actor_json IS NOT NULL AND changed_at IS NOT NULL) OR (transition_kind IN ('origin_invalidated','repository_invalidated') AND classification='unclassified' AND origin_digest IS NOT NULL AND changed_actor_json IS NULL AND changed_at IS NOT NULL)),
  FOREIGN KEY(project_id,workspace_id) REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE
);

CREATE TABLE workspace_publication_policy_history (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  policy_revision INTEGER NOT NULL CHECK(policy_revision > 0),
  repository_identity_json TEXT NOT NULL,
  origin_digest TEXT,
  classification TEXT NOT NULL CHECK(classification IN ('unclassified','local_only','public_git','private_git')),
  transition_kind TEXT NOT NULL CHECK(transition_kind IN ('bootstrap','configured','origin_invalidated','repository_invalidated')),
  changed_actor_json TEXT,
  changed_at TIMESTAMP,
  recorded_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id,policy_revision),
  CHECK((transition_kind='bootstrap' AND classification='unclassified' AND origin_digest IS NULL AND changed_actor_json IS NULL AND changed_at IS NULL) OR (transition_kind='configured' AND origin_digest IS NOT NULL AND changed_actor_json IS NOT NULL AND changed_at IS NOT NULL) OR (transition_kind IN ('origin_invalidated','repository_invalidated') AND classification='unclassified' AND origin_digest IS NOT NULL AND changed_actor_json IS NULL AND changed_at IS NOT NULL)),
  FOREIGN KEY(project_id,workspace_id) REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE
);

CREATE TABLE fabric_profiles (
  profile_id TEXT NOT NULL,
  alias TEXT NOT NULL UNIQUE,
  fabric_instance_id TEXT NOT NULL,
  base_url TEXT NOT NULL,
  mode TEXT NOT NULL CHECK(mode IN ('public','private')),
  credential_ref TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(profile_id),
  UNIQUE(profile_id,fabric_instance_id),
  UNIQUE(fabric_instance_id)
);

CREATE TABLE workspace_fabric_bindings (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  profile_id TEXT NOT NULL,
  fabric_instance_id TEXT NOT NULL,
  remote_project_id TEXT NOT NULL,
  stream_id TEXT NOT NULL,
  attachment_ref TEXT NOT NULL,
  repository_provider TEXT NOT NULL,
  repository_immutable_id TEXT NOT NULL,
  canonical_ref TEXT NOT NULL,
  writable INTEGER NOT NULL CHECK(writable IN (0,1)),
  state TEXT NOT NULL CHECK(state IN ('active','detached')),
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  detached_at TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id),
  UNIQUE(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id),
  UNIQUE(fabric_instance_id,attachment_ref),
  FOREIGN KEY(project_id,workspace_id)
    REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE,
  FOREIGN KEY(profile_id,fabric_instance_id)
    REFERENCES fabric_profiles(profile_id,fabric_instance_id) ON DELETE RESTRICT,
  CHECK((state='active' AND detached_at IS NULL) OR
        (state='detached' AND writable=0 AND detached_at IS NOT NULL))
);

CREATE UNIQUE INDEX workspace_one_active_writable_fabric
  ON workspace_fabric_bindings(project_id,workspace_id)
  WHERE writable=1 AND state='active';

CREATE TABLE fabric_cursors (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  fabric_instance_id TEXT NOT NULL,
  remote_project_id TEXT NOT NULL,
  stream_id TEXT NOT NULL,
  stream_version INTEGER NOT NULL CHECK(stream_version >= 0),
  pull_cursor TEXT NOT NULL DEFAULT '',
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id),
  FOREIGN KEY(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id)
    REFERENCES workspace_fabric_bindings(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id)
    ON DELETE CASCADE
);

CREATE TABLE legacy_fabric_profile_recoveries (
  recovery_id TEXT PRIMARY KEY,
  source_server_url TEXT NOT NULL,
  source_project_id TEXT NOT NULL,
  source_credential_path_hash TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('quarantined','completed','rejected')),
  completed_profile_id TEXT,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  completed_at TIMESTAMP,
  FOREIGN KEY(completed_profile_id) REFERENCES fabric_profiles(profile_id) ON DELETE RESTRICT
);

CREATE TABLE legacy_fabric_hint_recoveries (
  recovery_id TEXT NOT NULL,
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  source_hint_json TEXT NOT NULL,
  reason TEXT NOT NULL CHECK(reason IN ('missing_fabric_instance','missing_stream','ambiguous_workspace','fork_mismatch')),
  state TEXT NOT NULL CHECK(state IN ('quarantined','completed','rejected')),
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  completed_at TIMESTAMP,
  PRIMARY KEY(recovery_id,project_id,workspace_id),
  FOREIGN KEY(project_id,workspace_id) REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE
);

CREATE TABLE legacy_sync_queue_recoveries (
  id TEXT PRIMARY KEY,
  namespace_id TEXT NOT NULL,
  entity_type TEXT NOT NULL,
  entity_id TEXT NOT NULL,
  operation TEXT NOT NULL,
  payload TEXT NOT NULL,
  priority INTEGER NOT NULL,
  created_at TIMESTAMP NOT NULL,
  updated_at TIMESTAMP NOT NULL,
  reason TEXT NOT NULL CHECK(reason='missing_immutable_binding')
);

CREATE TABLE legacy_sync_history (
  id TEXT PRIMARY KEY,
  namespace_id TEXT NOT NULL,
  record_kind TEXT NOT NULL CHECK(record_kind IN ('delivered_queue','conflict_audit')),
  record_json TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL
);

CREATE INDEX workspace_overlay_generation ON workspace_overlay_operations(project_id,workspace_id,generation);
CREATE INDEX workspace_open_conflicts ON workspace_conflicts(project_id,workspace_id,state);
CREATE INDEX workspace_recovery ON workspace_materializations(state,project_id,workspace_id);
CREATE UNIQUE INDEX legacy_integration_one_pending ON legacy_integration_state_migrations(project_id,workspace_id) WHERE outcome='imported_move_pending';
CREATE UNIQUE INDEX workspace_one_open_semantic_conflict ON workspace_conflicts(project_id,workspace_id,conflict_id) WHERE state='open';
CREATE UNIQUE INDEX workspace_one_current_materialization ON workspace_materializations(project_id,workspace_id) WHERE state IN ('prepared','published','recovered_new');

CREATE INDEX sync_queue_pending
  ON sync_queue(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,priority DESC,created_at)
  WHERE delivered_at IS NULL;
CREATE INDEX sync_audit_recent
  ON sync_audit(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,created_at DESC);

INSERT INTO gateway_schema_migrations(version) VALUES (7);
