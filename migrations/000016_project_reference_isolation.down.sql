ALTER TABLE kb_links
    DROP CONSTRAINT IF EXISTS kb_links_to_article_project_fkey,
    DROP CONSTRAINT IF EXISTS kb_links_from_article_project_fkey;
ALTER TABLE kb_articles
    DROP CONSTRAINT IF EXISTS kb_articles_id_project_id_key;

ALTER TABLE git_links
    DROP CONSTRAINT IF EXISTS git_links_task_project_fkey;
ALTER TABLE events
    DROP CONSTRAINT IF EXISTS events_channel_project_fkey;
ALTER TABLE channels
    DROP CONSTRAINT IF EXISTS channels_id_project_id_key;

ALTER TABLE task_links
    DROP CONSTRAINT IF EXISTS task_links_task_project_fkey;
ALTER TABLE tasks
    DROP CONSTRAINT IF EXISTS tasks_parent_project_fkey,
    DROP CONSTRAINT IF EXISTS tasks_id_project_id_key;

ALTER TABLE permissions
    DROP CONSTRAINT IF EXISTS permissions_passport_project_fkey;
ALTER TABLE passports
    DROP CONSTRAINT IF EXISTS passports_id_project_id_key;

DROP POLICY IF EXISTS projects_project_isolation ON projects;
ALTER TABLE projects DISABLE ROW LEVEL SECURITY;
