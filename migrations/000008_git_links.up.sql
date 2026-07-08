-- RFC-0001 §8.6 Git integration (manual link only, no webhooks/CI/cloning/diff
-- storage). Column shapes per docs/db-entities.md's `## git_links` section.
-- task_id is nullable: wormhole.git.link_commit sets it, wormhole.git.request_review
-- (RFC-0001 §9, no task_id argument) does not.
--
-- The CHECK constraint requiring exactly one of commit_sha/pr_url non-null is a
-- controller inference, not RFC-literal: neither RFC-0001 nor db-entities.md states
-- it explicitly, but it follows from the two operations' shapes (link_commit always
-- sets commit_sha and leaves pr_url null; request_review always sets pr_url and
-- leaves commit_sha and task_id null).

CREATE TABLE git_links (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_id    uuid REFERENCES tasks(id) ON DELETE CASCADE,
    repo       text NOT NULL,
    commit_sha text,
    pr_url     text,
    summary    text NOT NULL,
    agent_id   uuid NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((commit_sha IS NOT NULL) != (pr_url IS NOT NULL))
);

CREATE INDEX idx_git_links_project_id ON git_links(project_id);
CREATE INDEX idx_git_links_task_id ON git_links(task_id);

ALTER TABLE git_links ENABLE ROW LEVEL SECURITY;
CREATE POLICY git_links_project_isolation ON git_links
    USING (project_id = current_setting('wormhole.project_id', true)::uuid);
