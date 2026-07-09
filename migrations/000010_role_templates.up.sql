-- RFC-0001 §8.1 Identity & Permissions pillar: role templates.
-- Pre-seeded role definitions with permission bundles and default task views.
-- This is Chapter 5 of the Alpha-2 roadmap: pure storage + read API, no MCP wiring yet.

CREATE TABLE role_templates (
    name                TEXT PRIMARY KEY,
    permission_bundle   jsonb NOT NULL,
    default_task_view   jsonb NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT now()
);

-- Seed rows: six pre-defined roles per Alpha-2 Chapter 5 spec.
-- Backend engineers, frontend engineers, and contributors can read/write tasks and KB.
-- Project managers and maintainers can additionally assign tasks.
-- Reviewers can only read tasks, write KB, and read channels.

INSERT INTO role_templates (name, permission_bundle, default_task_view)
VALUES
    ('backend-engineer',
     '["task.read", "task.write", "kb.read", "kb.write", "channel.read", "channel.write"]'::jsonb,
     '{"status": ["todo", "in_progress"], "assignee": "self"}'::jsonb),
    ('frontend-engineer',
     '["task.read", "task.write", "kb.read", "kb.write", "channel.read", "channel.write"]'::jsonb,
     '{"status": ["todo", "in_progress"], "assignee": "self"}'::jsonb),
    ('project-manager',
     '["task.read", "task.write", "kb.read", "kb.write", "channel.read", "channel.write", "task.assign"]'::jsonb,
     '{"status": [], "assignee": null}'::jsonb),
    ('contributor',
     '["task.read", "task.write", "kb.read", "kb.write", "channel.read", "channel.write"]'::jsonb,
     '{"status": [], "assignee": null}'::jsonb),
    ('reviewer',
     '["task.read", "kb.read", "kb.write", "channel.read", "channel.write"]'::jsonb,
     '{"status": [], "assignee": null}'::jsonb),
    ('maintainer',
     '["task.read", "task.write", "kb.read", "kb.write", "channel.read", "channel.write", "task.assign"]'::jsonb,
     '{"status": [], "assignee": null}'::jsonb);
