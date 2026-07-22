-- RFC-0001 §8.1/§8.3: fixed project bootstrap records must be safe to
-- create concurrently without constraining ordinary KB article titles.

ALTER TABLE channels
    ADD CONSTRAINT channels_project_id_name_key UNIQUE (project_id, name);

ALTER TABLE kb_articles
    ADD COLUMN bootstrap_key text;

CREATE UNIQUE INDEX idx_kb_articles_project_bootstrap_key
    ON kb_articles(project_id, bootstrap_key)
    WHERE bootstrap_key IS NOT NULL;
