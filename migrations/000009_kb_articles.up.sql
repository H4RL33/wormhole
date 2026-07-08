-- RFC-0001 §8.3 Knowledge Base pillar (atomic, linked, semantic-searchable
-- articles). Column shapes per docs/kb-schema.md, Day 13. This is plumbing
-- only: no compliance checks (dedup, conciseness, required links) and no
-- embedding generation yet, both deferred (RFC-0001 §15 open question
-- territory, see docs/kb-schema.md).

CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE kb_articles (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title           text NOT NULL,
    body            text NOT NULL,
    frontmatter     jsonb NOT NULL DEFAULT '{}',
    -- embedding dimension is deliberately unspecified: no embedding model has
    -- been chosen yet (Day 14's concern). Column stays NULL until the
    -- embedding pipeline lands.
    embedding       vector,
    author_agent_id uuid NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- kb_links: explicit graph edges between articles (architecture.md §6 --
-- "graph, never folder/path hierarchy"). project_id is a deliberate
-- deviation from docs/kb-schema.md's original sketch, added here for D3
-- multi-tenancy (RLS requires it on every project-scoped table), same
-- rationale as migrations/000006_task_graph.up.sql's task_links comment.
CREATE TABLE kb_links (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    from_article_id uuid NOT NULL REFERENCES kb_articles(id) ON DELETE CASCADE,
    to_article_id   uuid NOT NULL REFERENCES kb_articles(id) ON DELETE CASCADE,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_kb_articles_project_id ON kb_articles(project_id);
CREATE INDEX idx_kb_links_project_id ON kb_links(project_id);
CREATE INDEX idx_kb_links_from_article_id ON kb_links(from_article_id);

ALTER TABLE kb_articles ENABLE ROW LEVEL SECURITY;
CREATE POLICY kb_articles_project_isolation ON kb_articles
    USING (project_id = current_setting('wormhole.project_id', true)::uuid);

ALTER TABLE kb_links ENABLE ROW LEVEL SECURITY;
CREATE POLICY kb_links_project_isolation ON kb_links
    USING (project_id = current_setting('wormhole.project_id', true)::uuid);
