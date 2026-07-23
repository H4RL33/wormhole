ALTER TABLE audit_log NO FORCE ROW LEVEL SECURITY;

DROP POLICY audit_log_project_isolation ON audit_log;
CREATE POLICY audit_log_project_isolation ON audit_log
    USING (project_id = current_setting('wormhole.project_id', true)::uuid);
