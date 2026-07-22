-- RFC-0001 §13 and RFC-0003 §7.2: project metadata is tenant data, and
-- references between project-scoped rows must preserve the same project.

-- Fail closed before changing any constraint. Older schemas allowed these
-- references to cross projects; automatically changing tenant ownership would
-- be unsafe. Operators can locate the offending children with the joins named
-- in DETAIL, then either correct the child's project_id to match its parent or
-- delete the invalid child row before rerunning this migration.
DO $$
DECLARE
    violations text[] := ARRAY[]::text[];
BEGIN
    IF EXISTS (SELECT 1 FROM permissions c JOIN passports p ON p.id = c.passport_id WHERE c.project_id IS DISTINCT FROM p.project_id) THEN
        violations := array_append(violations, 'permissions.passport_id');
    END IF;
    IF EXISTS (SELECT 1 FROM tasks c JOIN tasks p ON p.id = c.parent_task_id WHERE c.project_id IS DISTINCT FROM p.project_id) THEN
        violations := array_append(violations, 'tasks.parent_task_id');
    END IF;
    IF EXISTS (SELECT 1 FROM task_links c JOIN tasks p ON p.id = c.task_id WHERE c.project_id IS DISTINCT FROM p.project_id) THEN
        violations := array_append(violations, 'task_links.task_id');
    END IF;
    IF EXISTS (SELECT 1 FROM events c JOIN channels p ON p.id = c.channel_id WHERE c.project_id IS DISTINCT FROM p.project_id) THEN
        violations := array_append(violations, 'events.channel_id');
    END IF;
    IF EXISTS (SELECT 1 FROM git_links c JOIN tasks p ON p.id = c.task_id WHERE c.task_id IS NOT NULL AND c.project_id IS DISTINCT FROM p.project_id) THEN
        violations := array_append(violations, 'git_links.task_id');
    END IF;
    IF EXISTS (SELECT 1 FROM kb_links c JOIN kb_articles p ON p.id = c.from_article_id WHERE c.project_id IS DISTINCT FROM p.project_id) THEN
        violations := array_append(violations, 'kb_links.from_article_id');
    END IF;
    IF EXISTS (SELECT 1 FROM kb_links c JOIN kb_articles p ON p.id = c.to_article_id WHERE c.project_id IS DISTINCT FROM p.project_id) THEN
        violations := array_append(violations, 'kb_links.to_article_id');
    END IF;
    IF cardinality(violations) > 0 THEN
        RAISE EXCEPTION 'migration 000016 refused legacy cross-project references'
            USING DETAIL = 'Offending relationships: ' || array_to_string(violations, ', '),
                  HINT = 'Join each named child relationship to its parent by id; update the child project_id to the parent project_id only after tenant validation, or delete the invalid child row, then rerun the migration.';
    END IF;
END $$;

ALTER TABLE projects ENABLE ROW LEVEL SECURITY;
CREATE POLICY projects_project_isolation ON projects
    USING (id = current_setting('wormhole.project_id', true)::uuid)
    WITH CHECK (id = current_setting('wormhole.project_id', true)::uuid);

ALTER TABLE passports
    ADD CONSTRAINT passports_id_project_id_key UNIQUE (id, project_id);
ALTER TABLE permissions
    ADD CONSTRAINT permissions_passport_project_fkey
    FOREIGN KEY (passport_id, project_id) REFERENCES passports(id, project_id) ON DELETE CASCADE;

ALTER TABLE tasks
    ADD CONSTRAINT tasks_id_project_id_key UNIQUE (id, project_id),
    ADD CONSTRAINT tasks_parent_project_fkey
    FOREIGN KEY (parent_task_id, project_id) REFERENCES tasks(id, project_id) ON DELETE CASCADE;
ALTER TABLE task_links
    ADD CONSTRAINT task_links_task_project_fkey
    FOREIGN KEY (task_id, project_id) REFERENCES tasks(id, project_id) ON DELETE CASCADE;

ALTER TABLE channels
    ADD CONSTRAINT channels_id_project_id_key UNIQUE (id, project_id);
ALTER TABLE events
    ADD CONSTRAINT events_channel_project_fkey
    FOREIGN KEY (channel_id, project_id) REFERENCES channels(id, project_id) ON DELETE CASCADE;
ALTER TABLE git_links
    ADD CONSTRAINT git_links_task_project_fkey
    FOREIGN KEY (task_id, project_id) REFERENCES tasks(id, project_id) ON DELETE CASCADE;

ALTER TABLE kb_articles
    ADD CONSTRAINT kb_articles_id_project_id_key UNIQUE (id, project_id);
ALTER TABLE kb_links
    ADD CONSTRAINT kb_links_from_article_project_fkey
    FOREIGN KEY (from_article_id, project_id) REFERENCES kb_articles(id, project_id) ON DELETE CASCADE,
    ADD CONSTRAINT kb_links_to_article_project_fkey
    FOREIGN KEY (to_article_id, project_id) REFERENCES kb_articles(id, project_id) ON DELETE CASCADE;
