-- RFC-0001 §8.4 identity/permissions, §13 multi-tenancy.
-- Day 2 scope: projects, agents, passports, permissions.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE projects (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text NOT NULL,
    owner       text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE agents (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner        text NOT NULL,
    model        text NOT NULL,
    capabilities jsonb NOT NULL DEFAULT '[]',
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- Passport: portable identity record an agent presents when joining a
-- project (RFC §8.4, §8.5). This is what scopes an otherwise
-- project-agnostic agent identity to a given project.
CREATE TABLE passports (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id      uuid NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    project_id    uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    repositories  jsonb NOT NULL DEFAULT '[]',
    roles         jsonb NOT NULL DEFAULT '[]',
    issued_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (agent_id, project_id)
);

CREATE INDEX idx_passports_project_id ON passports(project_id);

CREATE TABLE permissions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    passport_id  uuid NOT NULL REFERENCES passports(id) ON DELETE CASCADE,
    project_id   uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    action       text NOT NULL,
    granted      boolean NOT NULL DEFAULT false
);

CREATE INDEX idx_permissions_project_id ON permissions(project_id);

-- Row-level project scoping (RFC §13): every project-scoped table gets
-- RLS enabled, policy compares row's project_id against the session's
-- current project (set per-connection/per-request via
-- `SET LOCAL wormhole.project_id = '<uuid>'`). `projects` and `agents`
-- are intentionally excluded — a project row is not scoped to itself,
-- and an agent identity spans projects by design (RFC §8.4).

ALTER TABLE passports ENABLE ROW LEVEL SECURITY;
CREATE POLICY passports_project_isolation ON passports
    USING (project_id = current_setting('wormhole.project_id', true)::uuid);

ALTER TABLE permissions ENABLE ROW LEVEL SECURITY;
CREATE POLICY permissions_project_isolation ON permissions
    USING (project_id = current_setting('wormhole.project_id', true)::uuid);
