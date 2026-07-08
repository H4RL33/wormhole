-- RFC-0001 §8.1 Communication pillar (Events and Channels).
-- Column shapes per docs/db-entities.md, Day 9.

CREATE TABLE channels (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE events (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    channel_id uuid NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    agent_id   uuid NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    event_type text NOT NULL CHECK (event_type IN ('task.status_changed', 'review.requested', 'build.failed', 'discovery.logged', 'message.posted')),
    payload    jsonb NOT NULL DEFAULT '{}',
    note       text,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_channels_project_id ON channels(project_id);
CREATE INDEX idx_events_project_id_channel_id_created_at ON events(project_id, channel_id, created_at);

ALTER TABLE channels ENABLE ROW LEVEL SECURITY;
CREATE POLICY channels_project_isolation ON channels
    USING (project_id = current_setting('wormhole.project_id', true)::uuid);

ALTER TABLE events ENABLE ROW LEVEL SECURITY;
CREATE POLICY events_project_isolation ON events
    USING (project_id = current_setting('wormhole.project_id', true)::uuid);
