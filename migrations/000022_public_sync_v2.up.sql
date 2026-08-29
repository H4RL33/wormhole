-- Sync v2 public-key-continuity identity, sessions, immutable transport audit, and resolver.
DO $preflight$
DECLARE
    runtime_role record;
    resolver_role record;
BEGIN
    SELECT rolcanlogin,rolsuper,rolbypassrls,rolinherit,rolcreatedb,rolcreaterole,rolreplication,rolconfig
      INTO runtime_role FROM pg_catalog.pg_roles WHERE rolname='wormhole_fabric_runtime';
    IF NOT FOUND OR NOT runtime_role.rolcanlogin OR runtime_role.rolsuper OR runtime_role.rolbypassrls OR
       runtime_role.rolinherit OR runtime_role.rolcreatedb OR runtime_role.rolcreaterole OR
       runtime_role.rolreplication OR runtime_role.rolconfig IS NOT NULL THEN
        RAISE EXCEPTION USING ERRCODE='55000', MESSAGE='migration 000022 refuses malformed wormhole_fabric_runtime';
    END IF;
    SELECT rolcanlogin,rolsuper,rolbypassrls,rolinherit,rolcreatedb,rolcreaterole,rolreplication,rolconfig
      INTO resolver_role FROM pg_catalog.pg_roles WHERE rolname='wormhole_attachment_resolver';
    IF NOT FOUND OR resolver_role.rolcanlogin OR resolver_role.rolsuper OR NOT resolver_role.rolbypassrls OR
       resolver_role.rolinherit OR resolver_role.rolcreatedb OR resolver_role.rolcreaterole OR
       resolver_role.rolreplication OR resolver_role.rolconfig IS NOT NULL THEN
        RAISE EXCEPTION USING ERRCODE='55000', MESSAGE='migration 000022 refuses malformed wormhole_attachment_resolver';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_catalog.pg_auth_members WHERE roleid IN ('wormhole_fabric_runtime'::regrole,'wormhole_attachment_resolver'::regrole) OR member IN ('wormhole_fabric_runtime'::regrole,'wormhole_attachment_resolver'::regrole)) THEN
        RAISE EXCEPTION USING ERRCODE='55000', MESSAGE='migration 000022 refuses runtime or resolver role membership';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_catalog.pg_class WHERE relowner='wormhole_attachment_resolver'::regrole)
       OR EXISTS (SELECT 1 FROM pg_catalog.pg_proc WHERE proowner='wormhole_attachment_resolver'::regrole)
       OR EXISTS (SELECT 1 FROM pg_catalog.pg_class c CROSS JOIN LATERAL aclexplode(COALESCE(c.relacl,acldefault(CASE c.relkind WHEN 'S' THEN 's'::"char" ELSE 'r'::"char" END,c.relowner))) a WHERE a.grantee='wormhole_attachment_resolver'::regrole)
       OR EXISTS (SELECT 1 FROM pg_catalog.pg_proc p CROSS JOIN LATERAL aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) a WHERE a.grantee='wormhole_attachment_resolver'::regrole)
       OR EXISTS (SELECT 1 FROM pg_catalog.pg_namespace n CROSS JOIN LATERAL aclexplode(COALESCE(n.nspacl,acldefault('n',n.nspowner))) a WHERE a.grantee='wormhole_attachment_resolver'::regrole) THEN
        RAISE EXCEPTION USING ERRCODE='55000', MESSAGE='migration 000022 refuses pre-existing resolver ownership or privilege';
    END IF;
    IF EXISTS (SELECT 1 FROM fabric_public_actor_keys WHERE actor_kind='agent') THEN
        RAISE EXCEPTION USING ERRCODE='55000', MESSAGE='migration 000022 cannot normalize agent actor keys';
    END IF;
    IF EXISTS (SELECT 1 FROM fabric_public_actor_keys WHERE revoked_at < activated_at) THEN
        RAISE EXCEPTION USING ERRCODE='55000', MESSAGE='migration 000022 refuses invalid actor-key chronology';
    END IF;
    IF EXISTS (SELECT 1 FROM fabric_workspace_stream_bindings GROUP BY fabric_instance_id,attachment_ref HAVING count(*)>1) THEN
        RAISE EXCEPTION USING ERRCODE='55000', MESSAGE='migration 000022 refuses duplicate Fabric attachment references';
    END IF;
    IF EXISTS (SELECT 1 FROM fabric_stream_conflicts WHERE state='resolved') THEN
        RAISE EXCEPTION USING ERRCODE='55000', MESSAGE='migration 000022 cannot invent conflict resolution evidence';
    END IF;
END
$preflight$;

ALTER TABLE fabric_workspace_stream_bindings
    ADD COLUMN activity_source_ref uuid NOT NULL DEFAULT gen_random_uuid(),
    ADD COLUMN source_version bigint,
    ADD COLUMN public_issuer_key_fingerprint text,
    ADD CONSTRAINT fabric_workspace_binding_attachment_fabric_key UNIQUE(fabric_instance_id,attachment_ref),
    ADD CONSTRAINT fabric_workspace_binding_activity_source_fabric_key UNIQUE(fabric_instance_id,activity_source_ref),
    ADD CONSTRAINT fabric_workspace_binding_route_attachment_key UNIQUE(project_id,fabric_instance_id,stream_id,workspace_id,canonical_ref,attachment_ref),
    ADD CONSTRAINT fabric_workspace_binding_source_version_check CHECK(source_version IS NULL OR source_version BETWEEN 0 AND 9007199254740991),
    ADD CONSTRAINT fabric_workspace_binding_public_issuer_format_check CHECK(public_issuer_key_fingerprint IS NULL OR public_issuer_key_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    ADD CONSTRAINT fabric_workspace_binding_public_issuer_pair_check CHECK((source_version IS NULL)=(public_issuer_key_fingerprint IS NULL)),
    ADD CONSTRAINT fabric_workspace_binding_source_version_fkey FOREIGN KEY(project_id,fabric_instance_id,stream_id,canonical_ref,source_version) REFERENCES fabric_stream_versions(project_id,fabric_instance_id,stream_id,canonical_ref,version) ON DELETE RESTRICT;

UPDATE fabric_public_actor_keys
   SET session_id=NULL,harness_name='',harness_version='',model_name='',model_version='';
ALTER TABLE fabric_public_actor_keys
    ALTER COLUMN session_id DROP NOT NULL,
    DROP CONSTRAINT fabric_public_actor_keys_actor_kind_check,
    DROP CONSTRAINT fabric_public_actor_keys_check,
    ADD CONSTRAINT fabric_public_actor_keys_human_shape_check CHECK(actor_kind='human' AND human_principal_id IS NOT NULL AND agent_id IS NULL AND accountable_human_id IS NULL AND session_id IS NULL AND harness_name='' AND harness_version='' AND model_name='' AND model_version=''),
    ADD CONSTRAINT fabric_public_actor_keys_human_identity_key UNIQUE(project_id,fabric_instance_id,stream_id,canonical_ref,key_fingerprint,human_principal_id),
    ADD CONSTRAINT fabric_public_actor_keys_source_identity_key UNIQUE(project_id,fabric_instance_id,stream_id,canonical_ref,source_version,key_fingerprint),
    ADD CONSTRAINT fabric_public_actor_keys_revocation_check CHECK(revoked_at IS NULL OR revoked_at>=activated_at);
CREATE INDEX fabric_public_actor_keys_active_human_idx ON fabric_public_actor_keys(project_id,fabric_instance_id,stream_id,canonical_ref,key_fingerprint,human_principal_id) WHERE revoked_at IS NULL;

ALTER TABLE fabric_workspace_stream_bindings
    ADD CONSTRAINT fabric_workspace_binding_public_issuer_fkey FOREIGN KEY(project_id,fabric_instance_id,stream_id,canonical_ref,source_version,public_issuer_key_fingerprint) REFERENCES fabric_public_actor_keys(project_id,fabric_instance_id,stream_id,canonical_ref,source_version,key_fingerprint) ON DELETE RESTRICT;
CREATE UNIQUE INDEX fabric_workspace_binding_one_live_public_issuer_idx ON fabric_workspace_stream_bindings(fabric_instance_id,stream_id,canonical_ref,public_issuer_key_fingerprint) WHERE public_issuer_key_fingerprint IS NOT NULL AND detached_at IS NULL;

CREATE INDEX public_request_nonces_expiry_idx ON public_request_nonces(project_id,expires_at);

ALTER TABLE fabric_stream_versions
    ADD CONSTRAINT fabric_stream_versions_resolution_identity_key UNIQUE(project_id,fabric_instance_id,stream_id,canonical_ref,version,operation_id);
ALTER TABLE fabric_stream_conflicts
    ADD COLUMN resolution_operation_id uuid,
    ADD COLUMN resolution_version bigint,
    ADD CONSTRAINT fabric_stream_conflicts_resolution_version_check CHECK(resolution_version IS NULL OR resolution_version BETWEEN 0 AND 9007199254740991),
    ADD CONSTRAINT fabric_stream_conflicts_resolution_pair_check CHECK((state='open' AND resolution_operation_id IS NULL AND resolution_version IS NULL) OR (state='resolved' AND resolution_operation_id IS NOT NULL AND resolution_version IS NOT NULL)),
    ADD CONSTRAINT fabric_stream_conflicts_resolution_operation_fkey FOREIGN KEY(project_id,fabric_instance_id,stream_id,canonical_ref,resolution_version,resolution_operation_id) REFERENCES fabric_stream_versions(project_id,fabric_instance_id,stream_id,canonical_ref,version,operation_id) ON DELETE RESTRICT,
    ADD CONSTRAINT fabric_stream_conflicts_resolution_request_fkey FOREIGN KEY(project_id,fabric_instance_id,stream_id,canonical_ref,resolution_operation_id) REFERENCES fabric_stream_requests(project_id,fabric_instance_id,stream_id,ref_name,operation_id) ON DELETE RESTRICT;

CREATE TABLE fabric_public_agent_sessions (
    project_id uuid NOT NULL,
    fabric_instance_id uuid NOT NULL,
    stream_id uuid NOT NULL,
    canonical_ref text NOT NULL,
    workspace_id uuid NOT NULL,
    attachment_ref uuid NOT NULL,
    session_id uuid NOT NULL DEFAULT gen_random_uuid(),
    issuer_key_fingerprint text NOT NULL,
    agent_id uuid NOT NULL,
    accountable_human_id uuid NOT NULL,
    source_version bigint NOT NULL,
    harness_name text NOT NULL,
    harness_version text NOT NULL,
    model_name text NOT NULL DEFAULT '',
    model_version text NOT NULL DEFAULT '',
    issued_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    CONSTRAINT fabric_public_agent_sessions_pkey PRIMARY KEY(project_id,fabric_instance_id,stream_id,canonical_ref,workspace_id,session_id),
    CONSTRAINT fabric_public_agent_sessions_fabric_session_key UNIQUE(fabric_instance_id,session_id),
    CONSTRAINT fabric_public_agent_sessions_binding_fkey FOREIGN KEY(project_id,fabric_instance_id,stream_id,workspace_id,canonical_ref,attachment_ref) REFERENCES fabric_workspace_stream_bindings(project_id,fabric_instance_id,stream_id,workspace_id,canonical_ref,attachment_ref) ON DELETE RESTRICT,
    CONSTRAINT fabric_public_agent_sessions_issuer_human_fkey FOREIGN KEY(project_id,fabric_instance_id,stream_id,canonical_ref,issuer_key_fingerprint,accountable_human_id) REFERENCES fabric_public_actor_keys(project_id,fabric_instance_id,stream_id,canonical_ref,key_fingerprint,human_principal_id) ON DELETE RESTRICT,
    CONSTRAINT fabric_public_agent_sessions_source_version_fkey FOREIGN KEY(project_id,fabric_instance_id,stream_id,canonical_ref,source_version) REFERENCES fabric_stream_versions(project_id,fabric_instance_id,stream_id,canonical_ref,version) ON DELETE RESTRICT,
    CONSTRAINT fabric_public_agent_sessions_fingerprint_check CHECK(issuer_key_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT fabric_public_agent_sessions_source_version_check CHECK(source_version BETWEEN 0 AND 9007199254740991),
    CONSTRAINT fabric_public_agent_sessions_metadata_check CHECK(octet_length(harness_name) BETWEEN 1 AND 128 AND octet_length(harness_version) BETWEEN 1 AND 128 AND octet_length(model_name)<=128 AND octet_length(model_version)<=128 AND ((model_name='')=(model_version=''))),
    CONSTRAINT fabric_public_agent_sessions_expiry_check CHECK(expires_at=issued_at+interval '24 hours'),
    CONSTRAINT fabric_public_agent_sessions_revocation_check CHECK(revoked_at IS NULL OR revoked_at>=issued_at)
);
CREATE UNIQUE INDEX fabric_public_agent_sessions_one_active_idx ON fabric_public_agent_sessions(fabric_instance_id,attachment_ref,issuer_key_fingerprint,agent_id) WHERE revoked_at IS NULL;
CREATE INDEX fabric_public_agent_sessions_expiry_idx ON fabric_public_agent_sessions(project_id,expires_at) WHERE revoked_at IS NULL;
ALTER TABLE fabric_public_agent_sessions ENABLE ROW LEVEL SECURITY;
CREATE POLICY fabric_public_agent_sessions_project_isolation ON fabric_public_agent_sessions FOR ALL TO PUBLIC USING(project_id=NULLIF(current_setting('wormhole.project_id',true),'')::uuid) WITH CHECK(project_id=NULLIF(current_setting('wormhole.project_id',true),'')::uuid);
ALTER TABLE fabric_public_agent_sessions FORCE ROW LEVEL SECURITY;

ALTER TABLE audit_log
    ADD COLUMN actor_kind text NOT NULL DEFAULT 'agent',
    ADD COLUMN human_principal_id uuid,
    ADD COLUMN accountable_human_id uuid,
    ADD COLUMN session_id uuid,
    ADD COLUMN harness_name text NOT NULL DEFAULT '',
    ADD COLUMN harness_version text NOT NULL DEFAULT '',
    ADD COLUMN model_name text NOT NULL DEFAULT '',
    ADD COLUMN model_version text NOT NULL DEFAULT '',
    ADD COLUMN assurance text NOT NULL DEFAULT 'unknown',
    ADD COLUMN occurred_at timestamptz,
    ADD COLUMN actor_envelope_json bytea,
    ADD COLUMN canonical_payload_json bytea NOT NULL DEFAULT '\x7b7d'::bytea,
    ADD COLUMN request_digest text;
UPDATE audit_log SET occurred_at=created_at;
ALTER TABLE audit_log
    ALTER COLUMN occurred_at SET NOT NULL,
    ALTER COLUMN occurred_at SET DEFAULT now(),
    ALTER COLUMN agent_id DROP NOT NULL,
    ADD CONSTRAINT audit_log_actor_kind_check CHECK(actor_kind IN ('human','agent')),
    ADD CONSTRAINT audit_log_assurance_check CHECK(assurance IN ('unknown','public-key-continuity')),
    ADD CONSTRAINT audit_log_model_pair_check CHECK((model_name='')=(model_version='')),
    ADD CONSTRAINT audit_log_metadata_bounds_check CHECK(octet_length(harness_name)<=128 AND octet_length(harness_version)<=128 AND octet_length(model_name)<=128 AND octet_length(model_version)<=128),
    ADD CONSTRAINT audit_log_request_digest_check CHECK(request_digest IS NULL OR request_digest ~ '^sha256:[0-9a-f]{64}$'),
    ADD CONSTRAINT audit_log_request_digest_matches_payload_check CHECK(request_digest IS NULL OR request_digest='sha256:'||encode(digest(canonical_payload_json,'sha256'),'hex')),
    ADD CONSTRAINT audit_log_transport_shape_check CHECK((assurance='unknown' AND actor_kind='agent' AND agent_id IS NOT NULL AND human_principal_id IS NULL AND accountable_human_id IS NULL AND session_id IS NULL AND harness_name='' AND harness_version='' AND model_name='' AND model_version='' AND actor_envelope_json IS NULL AND request_digest IS NULL AND canonical_payload_json='\x7b7d'::bytea) OR (assurance='public-key-continuity' AND actor_kind='human' AND human_principal_id IS NOT NULL AND agent_id IS NULL AND accountable_human_id IS NULL AND session_id IS NULL AND harness_name='' AND harness_version='' AND model_name='' AND model_version='' AND actor_envelope_json IS NOT NULL AND request_digest IS NOT NULL) OR (assurance='public-key-continuity' AND actor_kind='agent' AND human_principal_id IS NULL AND agent_id IS NOT NULL AND accountable_human_id IS NOT NULL AND session_id IS NOT NULL AND harness_name<>'' AND harness_version<>'' AND actor_envelope_json IS NOT NULL AND request_digest IS NOT NULL));

CREATE FUNCTION reject_audit_log_mutation_v1() RETURNS trigger LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog,public AS $audit_immutable$
BEGIN
    RAISE EXCEPTION USING ERRCODE='55000', MESSAGE='audit_log is immutable';
END
$audit_immutable$;
REVOKE ALL ON FUNCTION reject_audit_log_mutation_v1() FROM PUBLIC;
CREATE TRIGGER audit_log_immutable BEFORE UPDATE OR DELETE ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_audit_log_mutation_v1();

GRANT SELECT ON TABLE fabric_workspace_stream_bindings TO wormhole_attachment_resolver;
CREATE FUNCTION fabric_resolve_attachment_project_v1(p_fabric_instance_id uuid,p_attachment_ref uuid) RETURNS uuid LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $resolver$
    SELECT b.project_id FROM public.fabric_workspace_stream_bindings b WHERE b.fabric_instance_id=p_fabric_instance_id AND b.attachment_ref=p_attachment_ref AND b.detached_at IS NULL
$resolver$;
REVOKE ALL ON FUNCTION fabric_resolve_attachment_project_v1(uuid,uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION fabric_resolve_attachment_project_v1(uuid,uuid) TO wormhole_fabric_runtime;
ALTER FUNCTION fabric_resolve_attachment_project_v1(uuid,uuid) OWNER TO wormhole_attachment_resolver;

GRANT SELECT ON TABLE project_repository_bindings TO wormhole_fabric_runtime;
GRANT SELECT,INSERT,UPDATE ON TABLE fabric_streams TO wormhole_fabric_runtime;
GRANT SELECT,INSERT ON TABLE fabric_stream_versions TO wormhole_fabric_runtime;
GRANT SELECT,INSERT,UPDATE ON TABLE fabric_workspace_stream_bindings TO wormhole_fabric_runtime;
GRANT SELECT,INSERT ON TABLE fabric_stream_requests TO wormhole_fabric_runtime;
GRANT SELECT,INSERT,UPDATE ON TABLE fabric_stream_conflicts TO wormhole_fabric_runtime;
GRANT SELECT,INSERT ON TABLE fabric_public_actor_keys TO wormhole_fabric_runtime;
GRANT INSERT ON TABLE public_request_nonces TO wormhole_fabric_runtime;
GRANT SELECT,INSERT,UPDATE ON TABLE fabric_public_agent_sessions TO wormhole_fabric_runtime;
GRANT SELECT,INSERT ON TABLE audit_log TO wormhole_fabric_runtime;
GRANT USAGE ON SEQUENCE audit_log_seq_seq TO wormhole_fabric_runtime;
