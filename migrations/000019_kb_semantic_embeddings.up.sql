-- Production KB embeddings are immutable members of a model generation.
-- A project can build a replacement generation without disturbing its
-- currently active search index, then atomically activate it once complete.

CREATE TABLE kb_embedding_generations (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    provider        text NOT NULL,
    model           text NOT NULL,
    version         text NOT NULL,
    dimension       integer NOT NULL CHECK (dimension = 1024),
    state           text NOT NULL CHECK (state IN ('building', 'active', 'failed', 'retired')),
    failure_code    text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    activated_at    timestamptz,
    failed_at       timestamptz,
    retired_at      timestamptz,
    UNIQUE (id, project_id),
    UNIQUE (id, project_id, provider, model, version, dimension),
    CHECK ((state = 'failed') = (failure_code IS NOT NULL)),
    CHECK (state <> 'active' OR activated_at IS NOT NULL),
    CHECK ((state = 'retired') = (retired_at IS NOT NULL))
);

CREATE UNIQUE INDEX kb_embedding_generations_one_active_per_project
    ON kb_embedding_generations(project_id)
    WHERE state = 'active';
CREATE INDEX kb_embedding_generations_project_created
    ON kb_embedding_generations(project_id, created_at DESC);

CREATE TABLE kb_article_embeddings (
    project_id      uuid NOT NULL,
    article_id      uuid NOT NULL,
    generation_id   uuid NOT NULL,
    provider        text NOT NULL,
    model           text NOT NULL,
    version         text NOT NULL,
    dimension       integer NOT NULL CHECK (dimension = 1024),
    content_hash    text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    embedding       vector(1024) NOT NULL CHECK (vector_dims(embedding) = 1024),
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (generation_id, article_id),
    FOREIGN KEY (article_id, project_id)
        REFERENCES kb_articles(id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (generation_id, project_id, provider, model, version, dimension)
        REFERENCES kb_embedding_generations(id, project_id, provider, model, version, dimension)
        ON DELETE CASCADE
);

CREATE INDEX kb_article_embeddings_project_generation
    ON kb_article_embeddings(project_id, generation_id);
CREATE INDEX kb_article_embeddings_article
    ON kb_article_embeddings(project_id, article_id);

ALTER TABLE kb_embedding_generations ENABLE ROW LEVEL SECURITY;
CREATE POLICY kb_embedding_generations_project_isolation ON kb_embedding_generations
    USING (project_id = current_setting('wormhole.project_id', true)::uuid)
    WITH CHECK (project_id = current_setting('wormhole.project_id', true)::uuid);

ALTER TABLE kb_article_embeddings ENABLE ROW LEVEL SECURITY;
CREATE POLICY kb_article_embeddings_project_isolation ON kb_article_embeddings
    USING (project_id = current_setting('wormhole.project_id', true)::uuid)
    WITH CHECK (project_id = current_setting('wormhole.project_id', true)::uuid);
