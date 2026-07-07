-- RFC-0001 §8.4: append-only log of actions taken under an identity.
-- Immutable by convention — no UPDATE/DELETE path is exposed by the Go
-- Store; this migration does not add a DB-level trigger to enforce it
-- (matches the rest of the schema, which relies on application-level
-- discipline rather than triggers).

CREATE TABLE audit_log (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id    uuid NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    project_id  uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    action      text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_log_agent_id ON audit_log(agent_id);
CREATE INDEX idx_audit_log_project_id ON audit_log(project_id);

ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;
CREATE POLICY audit_log_project_isolation ON audit_log
    USING (project_id = current_setting('wormhole.project_id', true)::uuid);
