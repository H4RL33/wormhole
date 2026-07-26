-- RFC-0003 §8.1: Fabric-side, project-scoped idempotency for Gateway
-- enrolment. Raw bearer tokens never enter this table; token_id references the
-- hashed credential record in agent_tokens.

CREATE TABLE agent_enrolments (
    project_id       uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    idempotency_key  uuid NOT NULL,
    request_hash     char(64) NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    state            text NOT NULL CHECK (state IN ('registration_in_progress', 'registered')),
    agent_id         uuid REFERENCES agents(id) ON DELETE CASCADE,
    passport_id      uuid REFERENCES passports(id) ON DELETE CASCADE,
    token_id         uuid REFERENCES agent_tokens(id) ON DELETE SET NULL,
    reissue_count    smallint NOT NULL DEFAULT 0 CHECK (reissue_count BETWEEN 0 AND 1),
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, idempotency_key),
    CHECK (
        (state = 'registration_in_progress' AND agent_id IS NULL AND passport_id IS NULL AND token_id IS NULL)
        OR
        (state = 'registered' AND agent_id IS NOT NULL AND passport_id IS NOT NULL AND token_id IS NOT NULL)
    )
);

CREATE INDEX idx_agent_enrolments_agent_id ON agent_enrolments(agent_id);
CREATE UNIQUE INDEX idx_agent_enrolments_passport_id ON agent_enrolments(passport_id);

ALTER TABLE agent_enrolments ENABLE ROW LEVEL SECURITY;
CREATE POLICY agent_enrolments_project_isolation ON agent_enrolments
    USING (project_id = current_setting('wormhole.project_id', true)::uuid)
    WITH CHECK (project_id = current_setting('wormhole.project_id', true)::uuid);
