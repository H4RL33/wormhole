-- RFC-0001 §8.4/§13: token-based auth per agent identity, unforgeable.
-- Raw tokens are never stored — only a SHA-256 hash, generated
-- application-side before insert.

CREATE TABLE agent_tokens (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id     uuid NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    project_id   uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    permissions  jsonb NOT NULL,
    token_hash   text NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (jsonb_typeof(permissions) = 'array')
);

CREATE INDEX idx_agent_tokens_agent_id ON agent_tokens(agent_id);
CREATE INDEX idx_agent_tokens_project_id ON agent_tokens(project_id);

ALTER TABLE agent_tokens ENABLE ROW LEVEL SECURITY;
CREATE POLICY agent_tokens_project_isolation ON agent_tokens
    USING (project_id = current_setting('wormhole.project_id', true)::uuid);
