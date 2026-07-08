-- RFC-0001 §8.2 Task Graph (Coordination pillar). Project -> Task -> Subtask
-- hierarchy via parent_task_id; status transitions emit task.status_changed
-- events (wired Day 11, not this migration). Column shapes per
-- docs/db-entities.md, extended Day 7.

CREATE TABLE tasks (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id     uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    parent_task_id uuid REFERENCES tasks(id) ON DELETE CASCADE,
    title          text NOT NULL,
    description    text NOT NULL DEFAULT '',
    owner_agent_id uuid REFERENCES agents(id),
    status         text NOT NULL DEFAULT 'todo' CHECK (status IN ('todo', 'wip', 'blocked', 'done')),
    priority       int NOT NULL DEFAULT 0,
    due_by         timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_tasks_project_id ON tasks(project_id);
CREATE INDEX idx_tasks_parent_task_id ON tasks(parent_task_id);

ALTER TABLE tasks ENABLE ROW LEVEL SECURITY;
CREATE POLICY tasks_project_isolation ON tasks
    USING (project_id = current_setting('wormhole.project_id', true)::uuid);

-- task_links: polymorphic links from a task to a KB article, commit, PR, or
-- event (architecture.md §6 -- "not ad hoc columns"). project_id is a
-- deliberate deviation from docs/db-entities.md's original sketch, added
-- here for D3 multi-tenancy (RLS requires it on every project-scoped
-- table); db-entities.md is updated in this same change.
CREATE TABLE task_links (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_id    uuid NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    link_type  text NOT NULL,
    target_ref text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_task_links_project_id ON task_links(project_id);
CREATE INDEX idx_task_links_task_id ON task_links(task_id);

ALTER TABLE task_links ENABLE ROW LEVEL SECURITY;
CREATE POLICY task_links_project_isolation ON task_links
    USING (project_id = current_setting('wormhole.project_id', true)::uuid);
