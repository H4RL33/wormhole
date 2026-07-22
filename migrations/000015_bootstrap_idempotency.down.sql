DROP INDEX IF EXISTS idx_kb_articles_project_bootstrap_key;

ALTER TABLE kb_articles
    DROP COLUMN IF EXISTS bootstrap_key;

ALTER TABLE channels
    DROP CONSTRAINT IF EXISTS channels_project_id_name_key;
