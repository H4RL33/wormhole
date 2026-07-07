-- RFC-0001 §8.4/§13: token-based auth per agent identity, unforgeable.
-- Raw tokens are never stored — only a SHA-256 hash, generated
-- application-side before insert.

CREATE TABLE agent_tokens (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id    uuid NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    token_hash  text NOT NULL UNIQUE,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_agent_tokens_agent_id ON agent_tokens(agent_id);
