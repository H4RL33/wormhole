-- RFC-0001 §13 and RFC-0003 §7.2: project metadata is tenant data, and
-- references between project-scoped rows must preserve the same project.

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
