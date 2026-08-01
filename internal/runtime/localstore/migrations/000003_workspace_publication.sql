CREATE TABLE workspace_publication_policies (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  repository_identity_json TEXT NOT NULL,
  origin_digest TEXT,
  classification TEXT NOT NULL CHECK(classification IN (
    'unclassified','local_only','public_git','private_git'
  )),
  policy_revision INTEGER NOT NULL CHECK(policy_revision > 0),
  transition_kind TEXT NOT NULL CHECK(transition_kind IN (
    'bootstrap','configured','origin_invalidated','repository_invalidated'
  )),
  changed_actor_json TEXT,
  changed_at TIMESTAMP,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id),
  CHECK(
    (transition_kind='bootstrap' AND classification='unclassified' AND
      origin_digest IS NULL AND changed_actor_json IS NULL AND changed_at IS NULL) OR
    (transition_kind='configured' AND origin_digest IS NOT NULL AND
      changed_actor_json IS NOT NULL AND changed_at IS NOT NULL) OR
    (transition_kind IN ('origin_invalidated','repository_invalidated') AND
      classification='unclassified' AND origin_digest IS NOT NULL AND
      changed_actor_json IS NULL AND changed_at IS NOT NULL)
  ),
  FOREIGN KEY(project_id,workspace_id)
    REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE
);

CREATE TABLE workspace_publication_policy_history (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  policy_revision INTEGER NOT NULL CHECK(policy_revision > 0),
  repository_identity_json TEXT NOT NULL,
  origin_digest TEXT,
  classification TEXT NOT NULL CHECK(classification IN (
    'unclassified','local_only','public_git','private_git'
  )),
  transition_kind TEXT NOT NULL CHECK(transition_kind IN (
    'bootstrap','configured','origin_invalidated','repository_invalidated'
  )),
  changed_actor_json TEXT,
  changed_at TIMESTAMP,
  recorded_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id,policy_revision),
  CHECK(
    (transition_kind='bootstrap' AND classification='unclassified' AND
      origin_digest IS NULL AND changed_actor_json IS NULL AND changed_at IS NULL) OR
    (transition_kind='configured' AND origin_digest IS NOT NULL AND
      changed_actor_json IS NOT NULL AND changed_at IS NOT NULL) OR
    (transition_kind IN ('origin_invalidated','repository_invalidated') AND
      classification='unclassified' AND origin_digest IS NOT NULL AND
      changed_actor_json IS NULL AND changed_at IS NOT NULL)
  ),
  FOREIGN KEY(project_id,workspace_id)
    REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE
);

INSERT INTO workspace_publication_policies
  (project_id,workspace_id,repository_identity_json,origin_digest,classification,
   policy_revision,transition_kind,changed_actor_json,changed_at)
SELECT project_id,workspace_id,repository_identity_json,NULL,'unclassified',1,
       'bootstrap',NULL,NULL
FROM workspace_bindings;

INSERT INTO workspace_publication_policy_history
  (project_id,workspace_id,policy_revision,repository_identity_json,origin_digest,
   classification,transition_kind,changed_actor_json,changed_at)
SELECT project_id,workspace_id,1,repository_identity_json,NULL,'unclassified',
       'bootstrap',NULL,NULL
FROM workspace_bindings;
