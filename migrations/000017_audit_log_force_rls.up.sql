-- RFC-0001 §13 and issue #33: audit visibility and writes are enforced by
-- project-scoped RLS, including for an ordinary table owner.

DROP POLICY audit_log_project_isolation ON audit_log;
CREATE POLICY audit_log_project_isolation ON audit_log
    USING (project_id = current_setting('wormhole.project_id', true)::uuid)
    WITH CHECK (project_id = current_setting('wormhole.project_id', true)::uuid);

ALTER TABLE audit_log FORCE ROW LEVEL SECURITY;
