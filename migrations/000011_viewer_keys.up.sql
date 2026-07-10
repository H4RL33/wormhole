-- RFC-0001 §8.4/§13: human oversight role. A viewer key is a project-scoped,
-- read-only credential for the human-facing dashboard (RFC-0001 §14 V2, pulled
-- forward into Alpha 2 M3) — distinct from agent bearer tokens (agent_tokens),
-- never grants write access, never resolves to an agent identity.
-- Raw keys are never stored — only a SHA-256 hash, generated application-side
-- before insert.

CREATE TABLE viewer_keys (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id   uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    label        text NOT NULL,
    key_hash     text NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_viewer_keys_project_id ON viewer_keys(project_id);

ALTER TABLE viewer_keys ENABLE ROW LEVEL SECURITY;
CREATE POLICY viewer_keys_project_isolation ON viewer_keys
    USING (project_id = current_setting('wormhole.project_id', true)::uuid);
