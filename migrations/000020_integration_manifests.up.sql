-- Issue #54 M7: Fabric stores one active integration-manifest lineage per
-- project while retaining immutable version history and authenticated
-- revocation metadata. Gateway remains the approval and application authority.

CREATE TABLE integration_manifest_lineages (
    project_id          uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    manifest_id         uuid NOT NULL,
    active              boolean NOT NULL DEFAULT true,
    created_by_agent_id uuid NOT NULL REFERENCES agents(id),
    created_at          timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, manifest_id)
);

CREATE UNIQUE INDEX integration_manifest_lineages_one_active_per_project
    ON integration_manifest_lineages(project_id)
    WHERE active;

CREATE TABLE integration_manifest_versions (
    project_id            uuid NOT NULL,
    manifest_id           uuid NOT NULL,
    manifest_version      bigint NOT NULL CHECK (manifest_version BETWEEN 1 AND 9007199254740991),
    schema_version        integer NOT NULL CHECK (schema_version = 1),
    source                text NOT NULL CHECK (source = 'fabric'),
    authored_at           timestamptz NOT NULL,
    tool_contract_digest  text NOT NULL CHECK (tool_contract_digest ~ '^sha256:[0-9a-f]{64}$'),
    manifest_digest       text NOT NULL CHECK (manifest_digest ~ '^sha256:[0-9a-f]{64}$'),
    role_filters          jsonb NOT NULL,
    entries               jsonb NOT NULL,
    published_by_agent_id uuid NOT NULL REFERENCES agents(id),
    published_at          timestamptz NOT NULL DEFAULT now(),
    revoked_by_agent_id   uuid REFERENCES agents(id),
    revoked_at            timestamptz,
    PRIMARY KEY (project_id, manifest_id, manifest_version),
    FOREIGN KEY (project_id, manifest_id)
        REFERENCES integration_manifest_lineages(project_id, manifest_id) ON DELETE CASCADE,
    CHECK (jsonb_typeof(role_filters) = 'array'),
    CHECK (jsonb_typeof(entries) = 'array'),
    CHECK ((revoked_at IS NULL) = (revoked_by_agent_id IS NULL))
);

CREATE INDEX integration_manifest_versions_project_published
    ON integration_manifest_versions(project_id, published_at, manifest_id, manifest_version);
CREATE INDEX integration_manifest_versions_project_revoked
    ON integration_manifest_versions(project_id, revoked_at)
    WHERE revoked_at IS NOT NULL;

CREATE FUNCTION integration_manifest_versions_immutable_body()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.project_id, NEW.manifest_id, NEW.manifest_version, NEW.schema_version,
        NEW.source, NEW.authored_at, NEW.tool_contract_digest, NEW.manifest_digest,
        NEW.role_filters, NEW.entries, NEW.published_by_agent_id, NEW.published_at)
       IS DISTINCT FROM
       (OLD.project_id, OLD.manifest_id, OLD.manifest_version, OLD.schema_version,
        OLD.source, OLD.authored_at, OLD.tool_contract_digest, OLD.manifest_digest,
        OLD.role_filters, OLD.entries, OLD.published_by_agent_id, OLD.published_at) THEN
        RAISE EXCEPTION 'integration manifest historical body is immutable';
    END IF;
    IF OLD.revoked_at IS NOT NULL AND
       (NEW.revoked_at, NEW.revoked_by_agent_id) IS DISTINCT FROM
       (OLD.revoked_at, OLD.revoked_by_agent_id) THEN
        RAISE EXCEPTION 'integration manifest revocation is immutable';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER integration_manifest_versions_immutable_body_trigger
    BEFORE UPDATE ON integration_manifest_versions
    FOR EACH ROW EXECUTE FUNCTION integration_manifest_versions_immutable_body();

ALTER TABLE integration_manifest_lineages ENABLE ROW LEVEL SECURITY;
CREATE POLICY integration_manifest_lineages_project_isolation ON integration_manifest_lineages
    USING (project_id = current_setting('wormhole.project_id', true)::uuid)
    WITH CHECK (project_id = current_setting('wormhole.project_id', true)::uuid);

ALTER TABLE integration_manifest_versions ENABLE ROW LEVEL SECURITY;
CREATE POLICY integration_manifest_versions_project_isolation ON integration_manifest_versions
    USING (project_id = current_setting('wormhole.project_id', true)::uuid)
    WITH CHECK (project_id = current_setting('wormhole.project_id', true)::uuid);
