-- Git-aware Fabric streams are non-authoritative replicas. Git observation remains
-- the sole acceptance authority for portable state. Activity is a separate,
-- finite-retention operational ledger and never enters a portable stream version.

DO $roles$
DECLARE
    role_name text;
    role_row record;
BEGIN
    FOREACH role_name IN ARRAY ARRAY[
        'wormhole_activity_owner',
        'wormhole_fabric_runtime',
        'wormhole_activity_maintenance'
    ] LOOP
        SELECT rolname, rolsuper, rolbypassrls, rolcanlogin
          INTO role_row
          FROM pg_catalog.pg_roles
         WHERE rolname = role_name;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'migration 000021 requires pre-provisioned role %', role_name;
        END IF;
        IF role_row.rolsuper OR role_row.rolbypassrls THEN
            RAISE EXCEPTION 'migration 000021 refuses privileged role %', role_name;
        END IF;
    END LOOP;
    IF (SELECT rolcanlogin FROM pg_catalog.pg_roles WHERE rolname = 'wormhole_activity_owner') OR
       (SELECT rolcanlogin FROM pg_catalog.pg_roles WHERE rolname = 'wormhole_activity_maintenance') OR
       NOT (SELECT rolcanlogin FROM pg_catalog.pg_roles WHERE rolname = 'wormhole_fabric_runtime') THEN
        RAISE EXCEPTION 'migration 000021 activity role login attributes are invalid';
    END IF;
END
$roles$;

CREATE TABLE project_repository_bindings (
    project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    fabric_instance_id uuid NOT NULL,
    provider text NOT NULL CHECK (provider = 'github'),
    provider_repository_id text NOT NULL CHECK (provider_repository_id ~ '^[0-9]+$'),
    canonical_remote text NOT NULL,
    default_ref text NOT NULL CHECK (default_ref ~ '^refs/heads/[A-Za-z0-9._/-]+$'),
    visibility text NOT NULL CHECK (visibility IN ('public','private')),
    observer_credential_ref text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, fabric_instance_id),
    UNIQUE (project_id, fabric_instance_id, provider, provider_repository_id),
    CHECK ((visibility = 'public' AND observer_credential_ref = '') OR visibility = 'private')
);

CREATE TABLE fabric_streams (
    project_id uuid NOT NULL,
    fabric_instance_id uuid NOT NULL,
    stream_id uuid NOT NULL DEFAULT gen_random_uuid(),
    canonical_ref text NOT NULL CHECK (canonical_ref ~ '^refs/heads/[A-Za-z0-9._/-]+$'),
    ref_name text NOT NULL CHECK (ref_name ~ '^refs/heads/[A-Za-z0-9._/-]+$'),
    current_version bigint NOT NULL DEFAULT 0 CHECK (current_version BETWEEN 0 AND 9007199254740991),
    live_tree_digest text NOT NULL CHECK (live_tree_digest ~ '^sha256:[0-9a-f]{64}$'),
    accepted_tree_digest text NOT NULL CHECK (accepted_tree_digest ~ '^sha256:[0-9a-f]{64}$'),
    accepted_commit_sha text NOT NULL CHECK (accepted_commit_sha ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, fabric_instance_id, stream_id, canonical_ref),
    UNIQUE (project_id, fabric_instance_id, ref_name),
    CHECK (canonical_ref = ref_name),
    FOREIGN KEY (project_id, fabric_instance_id)
        REFERENCES project_repository_bindings(project_id, fabric_instance_id) ON DELETE CASCADE
);

CREATE TABLE fabric_stream_versions (
    project_id uuid NOT NULL,
    fabric_instance_id uuid NOT NULL,
    stream_id uuid NOT NULL,
    canonical_ref text NOT NULL,
    version bigint NOT NULL CHECK (version BETWEEN 0 AND 9007199254740991),
    transition_kind text NOT NULL CHECK (transition_kind IN ('initial','operation','accepted_ref')),
    accepted_commit_sha text NOT NULL CHECK (accepted_commit_sha ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
    canonical_live_tree bytea NOT NULL,
    live_tree_digest text NOT NULL CHECK (live_tree_digest ~ '^sha256:[0-9a-f]{64}$'),
    canonical_accepted_tree bytea NOT NULL,
    accepted_tree_digest text NOT NULL CHECK (accepted_tree_digest ~ '^sha256:[0-9a-f]{64}$'),
    operation_id uuid,
    canonical_operation_json bytea,
    operation_digest text CHECK (operation_digest ~ '^sha256:[0-9a-f]{64}$'),
    actor_envelope_json bytea,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, fabric_instance_id, stream_id, canonical_ref, version),
    FOREIGN KEY (project_id, fabric_instance_id, stream_id, canonical_ref)
        REFERENCES fabric_streams(project_id, fabric_instance_id, stream_id, canonical_ref) ON DELETE CASCADE,
    CHECK ((transition_kind = 'operation') = (canonical_operation_json IS NOT NULL)),
    CHECK ((canonical_operation_json IS NULL) = (operation_id IS NULL)),
    CHECK ((canonical_operation_json IS NULL) = (operation_digest IS NULL)),
    CHECK ((transition_kind = 'operation') = (actor_envelope_json IS NOT NULL))
);

CREATE TABLE fabric_workspace_stream_bindings (
    project_id uuid NOT NULL,
    fabric_instance_id uuid NOT NULL,
    stream_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    attachment_ref uuid NOT NULL,
    repository_provider text NOT NULL CHECK (repository_provider = 'github'),
    repository_immutable_id text NOT NULL CHECK (repository_immutable_id ~ '^[0-9]+$'),
    canonical_ref text NOT NULL CHECK (canonical_ref ~ '^refs/heads/[A-Za-z0-9._/-]+$'),
    ref_name text NOT NULL CHECK (ref_name ~ '^refs/heads/[A-Za-z0-9._/-]+$'),
    writable boolean NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    detached_at timestamptz,
    PRIMARY KEY (project_id, fabric_instance_id, stream_id, workspace_id, ref_name),
    UNIQUE (project_id, fabric_instance_id, stream_id, workspace_id, canonical_ref),
    UNIQUE (project_id, fabric_instance_id, attachment_ref),
    FOREIGN KEY (project_id, fabric_instance_id, stream_id, canonical_ref)
        REFERENCES fabric_streams(project_id, fabric_instance_id, stream_id, canonical_ref) ON DELETE CASCADE,
    FOREIGN KEY (project_id, fabric_instance_id, repository_provider, repository_immutable_id)
        REFERENCES project_repository_bindings(project_id, fabric_instance_id, provider, provider_repository_id)
        ON DELETE RESTRICT,
    CHECK (canonical_ref = ref_name),
    CHECK (detached_at IS NULL OR NOT writable)
);

CREATE UNIQUE INDEX fabric_workspace_one_live_writable
    ON fabric_workspace_stream_bindings(project_id, fabric_instance_id, workspace_id)
    WHERE writable AND detached_at IS NULL;

CREATE TABLE fabric_stream_requests (
    project_id uuid NOT NULL,
    fabric_instance_id uuid NOT NULL,
    stream_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    ref_name text NOT NULL,
    operation_id uuid NOT NULL,
    canonical_operation_json bytea NOT NULL,
    operation_digest text NOT NULL CHECK (operation_digest ~ '^sha256:[0-9a-f]{64}$'),
    expected_stream_version bigint NOT NULL CHECK (expected_stream_version BETWEEN 0 AND 9007199254740991),
    expected_tree_digest text NOT NULL CHECK (expected_tree_digest ~ '^sha256:[0-9a-f]{64}$'),
    result text NOT NULL CHECK (result IN ('applied','conflict','rejected')),
    result_stream_version bigint NOT NULL CHECK (result_stream_version BETWEEN 0 AND 9007199254740991),
    actor_envelope_json bytea NOT NULL,
    conflict_json jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, fabric_instance_id, stream_id, ref_name, operation_id),
    FOREIGN KEY (project_id, fabric_instance_id, stream_id, workspace_id, ref_name)
        REFERENCES fabric_workspace_stream_bindings(project_id, fabric_instance_id, stream_id, workspace_id, ref_name)
        ON DELETE RESTRICT,
    CHECK ((result = 'conflict') = (conflict_json IS NOT NULL))
);

CREATE TABLE fabric_stream_conflicts (
    project_id uuid NOT NULL,
    fabric_instance_id uuid NOT NULL,
    stream_id uuid NOT NULL,
    canonical_ref text NOT NULL,
    conflict_id uuid NOT NULL DEFAULT gen_random_uuid(),
    detected_at_version bigint NOT NULL,
    conflict_kind text NOT NULL CHECK (conflict_kind IN ('operation_precondition','git_base_diverged')),
    base_tree_digest text NOT NULL CHECK (base_tree_digest ~ '^sha256:[0-9a-f]{64}$'),
    ours_tree_digest text NOT NULL CHECK (ours_tree_digest ~ '^sha256:[0-9a-f]{64}$'),
    theirs_tree_digest text NOT NULL CHECK (theirs_tree_digest ~ '^sha256:[0-9a-f]{64}$'),
    detail_json jsonb NOT NULL CHECK (jsonb_typeof(detail_json) = 'object'),
    state text NOT NULL CHECK (state IN ('open','resolved')),
    created_at timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz,
    PRIMARY KEY (project_id, fabric_instance_id, stream_id, canonical_ref, conflict_id),
    FOREIGN KEY (project_id, fabric_instance_id, stream_id, canonical_ref, detected_at_version)
        REFERENCES fabric_stream_versions(project_id, fabric_instance_id, stream_id, canonical_ref, version) ON DELETE RESTRICT,
    CHECK ((state = 'open' AND resolved_at IS NULL) OR (state = 'resolved' AND resolved_at IS NOT NULL))
);

CREATE TABLE fabric_public_actor_keys (
    project_id uuid NOT NULL,
    fabric_instance_id uuid NOT NULL,
    stream_id uuid NOT NULL,
    canonical_ref text NOT NULL,
    key_fingerprint text NOT NULL CHECK (key_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    public_key bytea NOT NULL CHECK (octet_length(public_key) = 32),
    actor_kind text NOT NULL CHECK (actor_kind IN ('human','agent')),
    human_principal_id uuid,
    agent_id uuid,
    accountable_human_id uuid,
    session_id uuid NOT NULL,
    harness_name text NOT NULL,
    harness_version text NOT NULL,
    model_name text NOT NULL DEFAULT '',
    model_version text NOT NULL DEFAULT '',
    source_version bigint NOT NULL,
    activated_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,
    PRIMARY KEY (project_id, fabric_instance_id, stream_id, canonical_ref, key_fingerprint),
    FOREIGN KEY (project_id, fabric_instance_id, stream_id, canonical_ref, source_version)
        REFERENCES fabric_stream_versions(project_id, fabric_instance_id, stream_id, canonical_ref, version) ON DELETE RESTRICT,
    CHECK ((actor_kind = 'human' AND human_principal_id IS NOT NULL AND agent_id IS NULL AND accountable_human_id IS NULL)
        OR (actor_kind = 'agent' AND human_principal_id IS NULL AND agent_id IS NOT NULL AND accountable_human_id IS NOT NULL)),
    CHECK ((model_name = '') = (model_version = ''))
);

CREATE TABLE public_request_nonces (
    project_id uuid NOT NULL,
    fabric_instance_id uuid NOT NULL,
    stream_id uuid NOT NULL,
    canonical_ref text NOT NULL,
    key_fingerprint text NOT NULL,
    nonce_hash text NOT NULL CHECK (nonce_hash ~ '^[0-9a-f]{64}$'),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, fabric_instance_id, stream_id, canonical_ref, key_fingerprint, nonce_hash),
    FOREIGN KEY (project_id, fabric_instance_id, stream_id, canonical_ref, key_fingerprint)
        REFERENCES fabric_public_actor_keys(project_id, fabric_instance_id, stream_id, canonical_ref, key_fingerprint)
        ON DELETE CASCADE
);

CREATE TABLE fabric_activity_policy_versions (
    project_id uuid NOT NULL,
    fabric_instance_id uuid NOT NULL,
    stream_id uuid NOT NULL,
    canonical_ref text NOT NULL,
    policy_version bigint NOT NULL CHECK (policy_version BETWEEN 1 AND 9007199254740991),
    canonical_policy_json bytea NOT NULL,
    policy_digest text NOT NULL CHECK (policy_digest ~ '^sha256:[0-9a-f]{64}$'),
    schema_version integer NOT NULL CHECK (schema_version = 1),
    ordinary_max_age_seconds bigint NOT NULL CHECK (ordinary_max_age_seconds = 2592000),
    ordinary_max_rows bigint NOT NULL CHECK (ordinary_max_rows = 10000),
    terminal_default_age_seconds bigint NOT NULL CHECK (terminal_default_age_seconds = 2592000),
    terminal_maximum_age_seconds bigint NOT NULL CHECK (terminal_maximum_age_seconds = 31536000),
    terminal_retention_seconds bigint NOT NULL CHECK (terminal_retention_seconds BETWEEN 2592000 AND 31536000),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, fabric_instance_id, stream_id, canonical_ref, policy_version),
    UNIQUE (project_id, fabric_instance_id, stream_id, canonical_ref, policy_version, policy_digest),
    FOREIGN KEY (project_id, fabric_instance_id, stream_id, canonical_ref)
        REFERENCES fabric_streams(project_id, fabric_instance_id, stream_id, canonical_ref) ON DELETE CASCADE
);

CREATE TABLE fabric_activity_policy_current (
    project_id uuid NOT NULL,
    fabric_instance_id uuid NOT NULL,
    stream_id uuid NOT NULL,
    canonical_ref text NOT NULL,
    policy_version bigint NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, fabric_instance_id, stream_id, canonical_ref),
    FOREIGN KEY (project_id, fabric_instance_id, stream_id, canonical_ref, policy_version)
        REFERENCES fabric_activity_policy_versions(project_id, fabric_instance_id, stream_id, canonical_ref, policy_version)
        ON DELETE RESTRICT,
    FOREIGN KEY (project_id, fabric_instance_id, stream_id, canonical_ref)
        REFERENCES fabric_streams(project_id, fabric_instance_id, stream_id, canonical_ref) ON DELETE CASCADE
);

CREATE TABLE fabric_activity_stream_sequences (
    project_id uuid NOT NULL,
    fabric_instance_id uuid NOT NULL,
    stream_id uuid NOT NULL,
    canonical_ref text NOT NULL,
    high_watermark bigint NOT NULL DEFAULT 0 CHECK (high_watermark BETWEEN 0 AND 9007199254740991),
    PRIMARY KEY (project_id, fabric_instance_id, stream_id, canonical_ref),
    FOREIGN KEY (project_id, fabric_instance_id, stream_id, canonical_ref)
        REFERENCES fabric_streams(project_id, fabric_instance_id, stream_id, canonical_ref) ON DELETE CASCADE
);

CREATE TABLE fabric_activities (
    project_id uuid NOT NULL,
    fabric_instance_id uuid NOT NULL,
    stream_id uuid NOT NULL,
    canonical_ref text NOT NULL,
    source_workspace_id uuid NOT NULL,
    activity_id uuid NOT NULL,
    sequence bigint NOT NULL CHECK (sequence BETWEEN 1 AND 9007199254740991),
    activity_class text NOT NULL CHECK (activity_class IN ('ordinary','lifecycle')),
    canonical_activity_json bytea NOT NULL,
    activity_digest text NOT NULL CHECK (activity_digest ~ '^sha256:[0-9a-f]{64}$'),
    source_actor_json bytea NOT NULL,
    event_channel_id uuid,
    event_actor_id uuid,
    event_type text CHECK (event_type IN ('task.status_changed','review.requested','build.failed','discovery.logged','message.posted')),
    event_payload_json bytea,
    event_note text,
    event_created_at timestamptz,
    embedded_lifecycle_kind text CHECK (embedded_lifecycle_kind IN ('delivery','conflict','recovery','receipt')),
    embedded_lifecycle_reference_id uuid,
    created_at timestamptz NOT NULL,
    accepted_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, fabric_instance_id, stream_id, canonical_ref, source_workspace_id, activity_id),
    UNIQUE (project_id, fabric_instance_id, stream_id, canonical_ref, sequence),
    UNIQUE (project_id, fabric_instance_id, stream_id, canonical_ref, source_workspace_id, activity_id, activity_digest, sequence, accepted_at),
    FOREIGN KEY (project_id, fabric_instance_id, stream_id, source_workspace_id, canonical_ref)
        REFERENCES fabric_workspace_stream_bindings(project_id, fabric_instance_id, stream_id, workspace_id, canonical_ref)
        ON DELETE RESTRICT,
    CHECK ((event_channel_id IS NULL AND event_actor_id IS NULL AND event_type IS NULL AND event_payload_json IS NULL AND event_note IS NULL AND event_created_at IS NULL)
        OR (event_channel_id IS NOT NULL AND event_actor_id IS NOT NULL AND event_type IS NOT NULL AND event_payload_json IS NOT NULL AND event_created_at IS NOT NULL)),
    CHECK ((activity_class = 'ordinary' AND event_channel_id IS NOT NULL AND embedded_lifecycle_kind IS NULL AND embedded_lifecycle_reference_id IS NULL)
        OR (activity_class = 'lifecycle' AND embedded_lifecycle_kind IS NOT NULL AND embedded_lifecycle_reference_id IS NOT NULL))
);

CREATE TABLE fabric_activity_ingress_receipts (
    project_id uuid NOT NULL,
    fabric_instance_id uuid NOT NULL,
    stream_id uuid NOT NULL,
    canonical_ref text NOT NULL,
    source_workspace_id uuid NOT NULL,
    activity_id uuid NOT NULL,
    activity_digest text NOT NULL,
    sequence bigint NOT NULL,
    policy_version bigint NOT NULL,
    policy_digest text NOT NULL,
    accepted_at timestamptz NOT NULL,
    PRIMARY KEY (project_id, fabric_instance_id, stream_id, canonical_ref, source_workspace_id, activity_id),
    FOREIGN KEY (project_id, fabric_instance_id, stream_id, canonical_ref, source_workspace_id, activity_id, activity_digest, sequence, accepted_at)
        REFERENCES fabric_activities(project_id, fabric_instance_id, stream_id, canonical_ref, source_workspace_id, activity_id, activity_digest, sequence, accepted_at)
        ON DELETE CASCADE,
    FOREIGN KEY (project_id, fabric_instance_id, stream_id, canonical_ref, policy_version, policy_digest)
        REFERENCES fabric_activity_policy_versions(project_id, fabric_instance_id, stream_id, canonical_ref, policy_version, policy_digest)
        ON DELETE RESTRICT
);

CREATE TABLE fabric_activity_lifecycle (
    project_id uuid NOT NULL,
    fabric_instance_id uuid NOT NULL,
    stream_id uuid NOT NULL,
    canonical_ref text NOT NULL,
    source_workspace_id uuid NOT NULL,
    activity_id uuid NOT NULL,
    lifecycle_kind text NOT NULL CHECK (lifecycle_kind IN ('delivery','conflict','recovery','receipt')),
    reference_id uuid NOT NULL,
    state text NOT NULL,
    policy_version bigint NOT NULL,
    policy_digest text NOT NULL,
    terminal_retention_seconds bigint NOT NULL CHECK (terminal_retention_seconds BETWEEN 2592000 AND 31536000),
    terminal_at timestamptz,
    expires_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, fabric_instance_id, stream_id, canonical_ref, source_workspace_id, activity_id, lifecycle_kind, reference_id),
    FOREIGN KEY (project_id, fabric_instance_id, stream_id, canonical_ref, source_workspace_id, activity_id)
        REFERENCES fabric_activities(project_id, fabric_instance_id, stream_id, canonical_ref, source_workspace_id, activity_id)
        ON DELETE CASCADE,
    FOREIGN KEY (project_id, fabric_instance_id, stream_id, canonical_ref, policy_version, policy_digest)
        REFERENCES fabric_activity_policy_versions(project_id, fabric_instance_id, stream_id, canonical_ref, policy_version, policy_digest)
        ON DELETE RESTRICT,
    CHECK ((lifecycle_kind = 'delivery' AND state IN ('pending','delivered','cancelled'))
        OR (lifecycle_kind = 'conflict' AND state IN ('open','resolved','cancelled'))
        OR (lifecycle_kind = 'recovery' AND state IN ('pending','blocked','recovered','cancelled'))
        OR (lifecycle_kind = 'receipt' AND state IN ('pending','confirmed','rejected','cancelled'))),
    CHECK ((state IN ('delivered','resolved','recovered','confirmed','rejected','cancelled') AND terminal_at IS NOT NULL AND expires_at IS NOT NULL)
        OR (state IN ('pending','open','blocked') AND terminal_at IS NULL AND expires_at IS NULL)),
    CHECK (expires_at IS NULL OR expires_at > terminal_at)
);

CREATE FUNCTION reject_fabric_immutable_history() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public AS $immutable$
BEGIN
    RAISE EXCEPTION 'fabric history is immutable' USING ERRCODE = '55000';
END
$immutable$;

CREATE TRIGGER fabric_stream_versions_immutable
    BEFORE UPDATE ON fabric_stream_versions
    FOR EACH ROW EXECUTE FUNCTION reject_fabric_immutable_history();
CREATE TRIGGER fabric_stream_requests_immutable
    BEFORE UPDATE ON fabric_stream_requests
    FOR EACH ROW EXECUTE FUNCTION reject_fabric_immutable_history();
CREATE TRIGGER fabric_activity_policy_versions_immutable
    BEFORE UPDATE ON fabric_activity_policy_versions
    FOR EACH ROW EXECUTE FUNCTION reject_fabric_immutable_history();
CREATE TRIGGER fabric_activities_immutable
    BEFORE UPDATE ON fabric_activities
    FOR EACH ROW EXECUTE FUNCTION reject_fabric_immutable_history();
CREATE TRIGGER fabric_activity_ingress_receipts_immutable
    BEFORE UPDATE ON fabric_activity_ingress_receipts
    FOR EACH ROW EXECUTE FUNCTION reject_fabric_immutable_history();

DO $rls$
DECLARE
    table_name text;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'project_repository_bindings','fabric_streams','fabric_stream_versions',
        'fabric_workspace_stream_bindings','fabric_stream_requests','fabric_stream_conflicts',
        'fabric_public_actor_keys','public_request_nonces','fabric_activity_policy_versions',
        'fabric_activity_policy_current','fabric_activity_stream_sequences','fabric_activities',
        'fabric_activity_ingress_receipts','fabric_activity_lifecycle'
    ] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
        EXECUTE format(
            'CREATE POLICY %I ON %I USING (project_id = NULLIF(current_setting(''wormhole.project_id'',true),'''')::uuid) WITH CHECK (project_id = NULLIF(current_setting(''wormhole.project_id'',true),'''')::uuid)',
            table_name || '_project_isolation', table_name);
    END LOOP;
END
$rls$;

CREATE FUNCTION fabric_publish_activity_policy_v1(
    p_project_id uuid, p_fabric_instance_id uuid, p_stream_id uuid, p_canonical_ref text,
    p_policy_version bigint, p_canonical_policy_json bytea, p_policy_digest text,
    p_schema_version integer, p_ordinary_max_age_seconds bigint, p_ordinary_max_rows bigint,
    p_terminal_default_age_seconds bigint, p_terminal_maximum_age_seconds bigint,
    p_terminal_retention_seconds bigint
) RETURNS TABLE(canonical_policy_json bytea, policy_digest text, policy_version bigint)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $publish$
DECLARE
    v_current bigint;
    v_existing_json bytea;
    v_existing_digest text;
BEGIN
    IF p_project_id IS NULL OR p_fabric_instance_id IS NULL OR p_stream_id IS NULL OR
       p_canonical_ref IS NULL OR p_canonical_ref = '' OR p_policy_version IS NULL OR
       p_canonical_policy_json IS NULL OR p_policy_digest IS NULL THEN
        RAISE EXCEPTION 'activity policy input incomplete' USING ERRCODE = '22023';
    END IF;
    PERFORM 1 FROM public.fabric_streams
     WHERE project_id = p_project_id AND fabric_instance_id = p_fabric_instance_id
       AND stream_id = p_stream_id AND canonical_ref = p_canonical_ref
     FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'activity stream unavailable' USING ERRCODE = 'P0002';
    END IF;
    SELECT c.policy_version INTO v_current
      FROM public.fabric_activity_policy_current c
     WHERE c.project_id = p_project_id AND c.fabric_instance_id = p_fabric_instance_id
       AND c.stream_id = p_stream_id AND c.canonical_ref = p_canonical_ref
     FOR UPDATE;

    SELECT v.canonical_policy_json, v.policy_digest
      INTO v_existing_json, v_existing_digest
      FROM public.fabric_activity_policy_versions v
     WHERE v.project_id = p_project_id AND v.fabric_instance_id = p_fabric_instance_id
       AND v.stream_id = p_stream_id AND v.canonical_ref = p_canonical_ref
       AND v.policy_version = p_policy_version;
    IF FOUND THEN
        IF v_existing_json IS DISTINCT FROM p_canonical_policy_json OR v_existing_digest IS DISTINCT FROM p_policy_digest THEN
            RAISE EXCEPTION 'activity policy conflict' USING ERRCODE = 'P0001';
        END IF;
        IF v_current IS DISTINCT FROM p_policy_version THEN
            RAISE EXCEPTION 'activity policy conflict' USING ERRCODE = 'P0001';
        END IF;
        RETURN QUERY SELECT v_existing_json, v_existing_digest, p_policy_version;
        RETURN;
    END IF;
    IF (v_current IS NULL AND p_policy_version <> 1) OR
       (v_current IS NOT NULL AND p_policy_version <> v_current + 1) THEN
        RAISE EXCEPTION 'activity policy conflict' USING ERRCODE = 'P0001';
    END IF;
    INSERT INTO public.fabric_activity_policy_versions(
        project_id,fabric_instance_id,stream_id,canonical_ref,policy_version,
        canonical_policy_json,policy_digest,schema_version,ordinary_max_age_seconds,
        ordinary_max_rows,terminal_default_age_seconds,terminal_maximum_age_seconds,
        terminal_retention_seconds)
    VALUES (p_project_id,p_fabric_instance_id,p_stream_id,p_canonical_ref,p_policy_version,
        p_canonical_policy_json,p_policy_digest,p_schema_version,p_ordinary_max_age_seconds,
        p_ordinary_max_rows,p_terminal_default_age_seconds,p_terminal_maximum_age_seconds,
        p_terminal_retention_seconds);
    INSERT INTO public.fabric_activity_policy_current(project_id,fabric_instance_id,stream_id,canonical_ref,policy_version)
    VALUES (p_project_id,p_fabric_instance_id,p_stream_id,p_canonical_ref,p_policy_version)
    ON CONFLICT (project_id,fabric_instance_id,stream_id,canonical_ref)
    DO UPDATE SET policy_version = EXCLUDED.policy_version, updated_at = pg_catalog.transaction_timestamp();
    INSERT INTO public.fabric_activity_stream_sequences(project_id,fabric_instance_id,stream_id,canonical_ref,high_watermark)
    VALUES (p_project_id,p_fabric_instance_id,p_stream_id,p_canonical_ref,0)
    ON CONFLICT (project_id,fabric_instance_id,stream_id,canonical_ref) DO NOTHING;
    RETURN QUERY SELECT p_canonical_policy_json, p_policy_digest, p_policy_version;
END
$publish$;

CREATE FUNCTION fabric_accept_activity_v1(
    p_project_id uuid, p_fabric_instance_id uuid, p_stream_id uuid, p_canonical_ref text,
    p_source_workspace_id uuid, p_activity_id uuid, p_activity_class text,
    p_canonical_activity_json bytea, p_activity_digest text, p_source_actor_json bytea,
    p_event_channel_id uuid, p_event_actor_id uuid, p_event_type text, p_event_payload_json bytea,
    p_event_note text, p_event_created_at timestamptz,
    p_lifecycle_kind text, p_lifecycle_reference_id uuid, p_created_at timestamptz,
    p_policy_version bigint, p_policy_digest text
) RETURNS TABLE(activity_digest text, sequence bigint, policy_version bigint, policy_digest text, accepted_at timestamptz)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $accept$
DECLARE
    v_current_version bigint;
    v_current_digest text;
    v_retention bigint;
    v_existing_json bytea;
    v_existing_digest text;
    v_sequence bigint;
    v_original_policy_version bigint;
    v_original_policy_digest text;
    v_accepted_at timestamptz;
    v_initial_state text;
BEGIN
    IF p_project_id IS NULL OR p_fabric_instance_id IS NULL OR p_stream_id IS NULL OR
       p_canonical_ref IS NULL OR p_canonical_ref = '' OR p_source_workspace_id IS NULL OR
       p_activity_id IS NULL OR p_canonical_activity_json IS NULL OR p_activity_digest IS NULL OR
       p_source_actor_json IS NULL OR p_policy_version IS NULL OR p_policy_digest IS NULL THEN
        RAISE EXCEPTION 'activity ingress input incomplete' USING ERRCODE = '22023';
    END IF;
    PERFORM 1 FROM public.fabric_workspace_stream_bindings
     WHERE project_id = p_project_id AND fabric_instance_id = p_fabric_instance_id
       AND stream_id = p_stream_id AND workspace_id = p_source_workspace_id
       AND canonical_ref = p_canonical_ref AND detached_at IS NULL
     FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'activity binding unavailable' USING ERRCODE = 'P0002';
    END IF;
    SELECT c.policy_version, v.policy_digest, v.terminal_retention_seconds
      INTO v_current_version, v_current_digest, v_retention
      FROM public.fabric_activity_policy_current c
      JOIN public.fabric_activity_policy_versions v
        USING (project_id,fabric_instance_id,stream_id,canonical_ref,policy_version)
     WHERE c.project_id = p_project_id AND c.fabric_instance_id = p_fabric_instance_id
       AND c.stream_id = p_stream_id AND c.canonical_ref = p_canonical_ref
     FOR UPDATE OF c;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'activity policy unavailable' USING ERRCODE = 'P0002';
    END IF;
    IF v_current_version IS DISTINCT FROM p_policy_version OR v_current_digest IS DISTINCT FROM p_policy_digest THEN
        RAISE EXCEPTION 'activity policy changed' USING ERRCODE = 'P0001';
    END IF;

    SELECT a.canonical_activity_json, a.activity_digest, r.sequence,
           r.policy_version, r.policy_digest, r.accepted_at
      INTO v_existing_json, v_existing_digest, v_sequence,
           v_original_policy_version, v_original_policy_digest, v_accepted_at
      FROM public.fabric_activities a
      JOIN public.fabric_activity_ingress_receipts r
        USING (project_id,fabric_instance_id,stream_id,canonical_ref,source_workspace_id,activity_id)
     WHERE a.project_id = p_project_id AND a.fabric_instance_id = p_fabric_instance_id
       AND a.stream_id = p_stream_id AND a.canonical_ref = p_canonical_ref
       AND a.source_workspace_id = p_source_workspace_id AND a.activity_id = p_activity_id
     FOR UPDATE OF a, r;
    IF FOUND THEN
        IF v_existing_json IS DISTINCT FROM p_canonical_activity_json OR v_existing_digest IS DISTINCT FROM p_activity_digest THEN
            RAISE EXCEPTION 'activity replay conflict' USING ERRCODE = 'P0001';
        END IF;
        RETURN QUERY SELECT v_existing_digest, v_sequence, v_original_policy_version, v_original_policy_digest, v_accepted_at;
        RETURN;
    END IF;

    UPDATE public.fabric_activity_stream_sequences
       SET high_watermark = high_watermark + 1
     WHERE project_id = p_project_id AND fabric_instance_id = p_fabric_instance_id
       AND stream_id = p_stream_id AND canonical_ref = p_canonical_ref
       AND high_watermark < 9007199254740991
     RETURNING high_watermark INTO v_sequence;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'activity sequence unavailable' USING ERRCODE = '22003';
    END IF;
    v_accepted_at := pg_catalog.transaction_timestamp();
    INSERT INTO public.fabric_activities(
        project_id,fabric_instance_id,stream_id,canonical_ref,source_workspace_id,activity_id,
        sequence,activity_class,canonical_activity_json,activity_digest,source_actor_json,
        event_channel_id,event_actor_id,event_type,event_payload_json,event_note,event_created_at,
        embedded_lifecycle_kind,embedded_lifecycle_reference_id,created_at,accepted_at)
    VALUES (p_project_id,p_fabric_instance_id,p_stream_id,p_canonical_ref,p_source_workspace_id,p_activity_id,
        v_sequence,p_activity_class,p_canonical_activity_json,p_activity_digest,p_source_actor_json,
        p_event_channel_id,p_event_actor_id,p_event_type,p_event_payload_json,p_event_note,p_event_created_at,
        p_lifecycle_kind,p_lifecycle_reference_id,p_created_at,v_accepted_at);
    INSERT INTO public.fabric_activity_ingress_receipts(
        project_id,fabric_instance_id,stream_id,canonical_ref,source_workspace_id,activity_id,
        activity_digest,sequence,policy_version,policy_digest,accepted_at)
    VALUES (p_project_id,p_fabric_instance_id,p_stream_id,p_canonical_ref,p_source_workspace_id,p_activity_id,
        p_activity_digest,v_sequence,p_policy_version,p_policy_digest,v_accepted_at);
    IF p_activity_class = 'lifecycle' THEN
        v_initial_state := CASE WHEN p_lifecycle_kind = 'conflict' THEN 'open' ELSE 'pending' END;
        INSERT INTO public.fabric_activity_lifecycle(
            project_id,fabric_instance_id,stream_id,canonical_ref,source_workspace_id,activity_id,
            lifecycle_kind,reference_id,state,policy_version,policy_digest,terminal_retention_seconds)
        VALUES (p_project_id,p_fabric_instance_id,p_stream_id,p_canonical_ref,p_source_workspace_id,p_activity_id,
            p_lifecycle_kind,p_lifecycle_reference_id,v_initial_state,p_policy_version,p_policy_digest,v_retention);
    END IF;
    RETURN QUERY SELECT p_activity_digest, v_sequence, p_policy_version, p_policy_digest, v_accepted_at;
END
$accept$;

CREATE FUNCTION fabric_transition_activity_lifecycle_v1(
    p_project_id uuid, p_fabric_instance_id uuid, p_stream_id uuid, p_canonical_ref text,
    p_source_workspace_id uuid, p_activity_id uuid, p_lifecycle_kind text,
    p_reference_id uuid, p_expected_state text, p_next_state text
) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $transition$
DECLARE
    v_state text;
    v_retention bigint;
    v_now timestamptz;
    v_terminal boolean;
    v_allowed boolean;
BEGIN
    IF p_project_id IS NULL OR p_fabric_instance_id IS NULL OR p_stream_id IS NULL OR
       p_canonical_ref IS NULL OR p_canonical_ref = '' OR p_source_workspace_id IS NULL OR
       p_activity_id IS NULL OR p_lifecycle_kind IS NULL OR p_reference_id IS NULL OR
       p_expected_state IS NULL OR p_next_state IS NULL THEN
        RAISE EXCEPTION 'activity lifecycle input incomplete' USING ERRCODE = '22023';
    END IF;
    PERFORM 1 FROM public.fabric_workspace_stream_bindings
     WHERE project_id = p_project_id AND fabric_instance_id = p_fabric_instance_id
       AND stream_id = p_stream_id AND workspace_id = p_source_workspace_id
       AND canonical_ref = p_canonical_ref
     FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'activity binding unavailable' USING ERRCODE = 'P0002';
    END IF;
    PERFORM 1 FROM public.fabric_activities
     WHERE project_id = p_project_id AND fabric_instance_id = p_fabric_instance_id
       AND stream_id = p_stream_id AND canonical_ref = p_canonical_ref
       AND source_workspace_id = p_source_workspace_id AND activity_id = p_activity_id
     FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'activity not found' USING ERRCODE = 'P0002';
    END IF;
    SELECT state, terminal_retention_seconds INTO v_state, v_retention
      FROM public.fabric_activity_lifecycle
     WHERE project_id = p_project_id AND fabric_instance_id = p_fabric_instance_id
       AND stream_id = p_stream_id AND canonical_ref = p_canonical_ref
       AND source_workspace_id = p_source_workspace_id AND activity_id = p_activity_id
       AND lifecycle_kind = p_lifecycle_kind AND reference_id = p_reference_id
     FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'activity lifecycle not found' USING ERRCODE = 'P0002';
    END IF;
    IF v_state = p_next_state THEN
        RETURN;
    END IF;
    IF v_state IS DISTINCT FROM p_expected_state THEN
        RAISE EXCEPTION 'activity lifecycle conflict' USING ERRCODE = 'P0001';
    END IF;
    v_allowed := CASE p_lifecycle_kind
        WHEN 'delivery' THEN v_state = 'pending' AND p_next_state IN ('delivered','cancelled')
        WHEN 'conflict' THEN v_state = 'open' AND p_next_state IN ('resolved','cancelled')
        WHEN 'recovery' THEN (v_state = 'pending' AND p_next_state IN ('blocked','recovered','cancelled'))
            OR (v_state = 'blocked' AND p_next_state IN ('pending','recovered','cancelled'))
        WHEN 'receipt' THEN v_state = 'pending' AND p_next_state IN ('confirmed','rejected','cancelled')
        ELSE false
    END;
    IF NOT v_allowed THEN
        RAISE EXCEPTION 'activity lifecycle conflict' USING ERRCODE = 'P0001';
    END IF;
    v_terminal := p_next_state IN ('delivered','resolved','recovered','confirmed','rejected','cancelled');
    IF v_terminal THEN
        v_now := pg_catalog.transaction_timestamp();
        UPDATE public.fabric_activity_lifecycle
           SET state = p_next_state, terminal_at = v_now,
               expires_at = v_now + pg_catalog.make_interval(secs => v_retention), updated_at = v_now
         WHERE project_id = p_project_id AND fabric_instance_id = p_fabric_instance_id
           AND stream_id = p_stream_id AND canonical_ref = p_canonical_ref
           AND source_workspace_id = p_source_workspace_id AND activity_id = p_activity_id
           AND lifecycle_kind = p_lifecycle_kind AND reference_id = p_reference_id;
    ELSE
        UPDATE public.fabric_activity_lifecycle
           SET state = p_next_state, updated_at = pg_catalog.transaction_timestamp()
         WHERE project_id = p_project_id AND fabric_instance_id = p_fabric_instance_id
           AND stream_id = p_stream_id AND canonical_ref = p_canonical_ref
           AND source_workspace_id = p_source_workspace_id AND activity_id = p_activity_id
           AND lifecycle_kind = p_lifecycle_kind AND reference_id = p_reference_id;
    END IF;
END
$transition$;

CREATE FUNCTION fabric_prune_activities_v1(
    p_project_id uuid, p_fabric_instance_id uuid, p_stream_id uuid, p_canonical_ref text,
    p_source_workspace_id uuid, p_limit integer
) RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $prune$
DECLARE
    v_now timestamptz;
    v_ids uuid[];
    v_count integer;
BEGIN
    IF p_project_id IS NULL OR p_fabric_instance_id IS NULL OR p_stream_id IS NULL OR
       p_canonical_ref IS NULL OR p_canonical_ref = '' OR p_source_workspace_id IS NULL OR
       p_limit IS NULL OR p_limit < 1 OR p_limit > 1000 THEN
        RAISE EXCEPTION 'activity prune input invalid' USING ERRCODE = '22023';
    END IF;
    PERFORM 1 FROM public.fabric_workspace_stream_bindings
     WHERE project_id = p_project_id AND fabric_instance_id = p_fabric_instance_id
       AND stream_id = p_stream_id AND workspace_id = p_source_workspace_id
       AND canonical_ref = p_canonical_ref
     FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'activity binding unavailable' USING ERRCODE = 'P0002';
    END IF;
    v_now := pg_catalog.transaction_timestamp();
    WITH ranked AS (
        SELECT a.activity_id, a.created_at,
               row_number() OVER (ORDER BY a.created_at DESC, a.activity_id DESC) AS newest_rank
          FROM public.fabric_activities a
         WHERE a.project_id = p_project_id AND a.fabric_instance_id = p_fabric_instance_id
           AND a.stream_id = p_stream_id AND a.canonical_ref = p_canonical_ref
           AND a.source_workspace_id = p_source_workspace_id AND a.activity_class = 'ordinary'
           AND NOT EXISTS (
               SELECT 1 FROM public.fabric_activity_lifecycle l
                WHERE l.project_id = a.project_id AND l.fabric_instance_id = a.fabric_instance_id
                  AND l.stream_id = a.stream_id AND l.canonical_ref = a.canonical_ref
                  AND l.source_workspace_id = a.source_workspace_id AND l.activity_id = a.activity_id)
    ), eligible AS (
        SELECT a.activity_id, a.created_at
          FROM public.fabric_activities a
          JOIN ranked r ON r.activity_id = a.activity_id
         WHERE a.project_id = p_project_id AND a.fabric_instance_id = p_fabric_instance_id
           AND a.stream_id = p_stream_id AND a.canonical_ref = p_canonical_ref
           AND a.source_workspace_id = p_source_workspace_id
           AND (v_now >= a.created_at + pg_catalog.make_interval(secs => 2592000) OR r.newest_rank > 10000)
        UNION ALL
        SELECT a.activity_id, a.created_at
          FROM public.fabric_activities a
         WHERE a.project_id = p_project_id AND a.fabric_instance_id = p_fabric_instance_id
           AND a.stream_id = p_stream_id AND a.canonical_ref = p_canonical_ref
           AND a.source_workspace_id = p_source_workspace_id
           AND EXISTS (SELECT 1 FROM public.fabric_activity_lifecycle l
                WHERE l.project_id = a.project_id AND l.fabric_instance_id = a.fabric_instance_id
                  AND l.stream_id = a.stream_id AND l.canonical_ref = a.canonical_ref
                  AND l.source_workspace_id = a.source_workspace_id AND l.activity_id = a.activity_id)
           AND NOT EXISTS (SELECT 1 FROM public.fabric_activity_lifecycle l
                WHERE l.project_id = a.project_id AND l.fabric_instance_id = a.fabric_instance_id
                  AND l.stream_id = a.stream_id AND l.canonical_ref = a.canonical_ref
                  AND l.source_workspace_id = a.source_workspace_id AND l.activity_id = a.activity_id
                  AND (l.expires_at IS NULL OR l.expires_at > v_now))
    ), bounded AS (
        SELECT activity_id FROM eligible ORDER BY created_at, activity_id LIMIT p_limit
    )
    SELECT pg_catalog.array_agg(activity_id ORDER BY activity_id), count(*)::integer
      INTO v_ids, v_count FROM bounded;
    IF v_count = 0 THEN
        RETURN 0;
    END IF;
    PERFORM 1 FROM public.fabric_activities
     WHERE project_id = p_project_id AND fabric_instance_id = p_fabric_instance_id
       AND stream_id = p_stream_id AND canonical_ref = p_canonical_ref
       AND source_workspace_id = p_source_workspace_id AND activity_id = ANY(v_ids)
     ORDER BY created_at, activity_id FOR UPDATE;
    IF EXISTS (
        SELECT 1 FROM public.fabric_activity_lifecycle l
         WHERE l.project_id = p_project_id AND l.fabric_instance_id = p_fabric_instance_id
           AND l.stream_id = p_stream_id AND l.canonical_ref = p_canonical_ref
           AND l.source_workspace_id = p_source_workspace_id AND l.activity_id = ANY(v_ids)
           AND l.expires_at IS NULL) THEN
        RAISE EXCEPTION 'activity prune protection changed' USING ERRCODE = '40001';
    END IF;
    DELETE FROM public.fabric_activity_lifecycle
     WHERE project_id = p_project_id AND fabric_instance_id = p_fabric_instance_id
       AND stream_id = p_stream_id AND canonical_ref = p_canonical_ref
       AND source_workspace_id = p_source_workspace_id AND activity_id = ANY(v_ids);
    DELETE FROM public.fabric_activity_ingress_receipts
     WHERE project_id = p_project_id AND fabric_instance_id = p_fabric_instance_id
       AND stream_id = p_stream_id AND canonical_ref = p_canonical_ref
       AND source_workspace_id = p_source_workspace_id AND activity_id = ANY(v_ids);
    DELETE FROM public.fabric_activities
     WHERE project_id = p_project_id AND fabric_instance_id = p_fabric_instance_id
       AND stream_id = p_stream_id AND canonical_ref = p_canonical_ref
       AND source_workspace_id = p_source_workspace_id AND activity_id = ANY(v_ids);
    RETURN v_count;
END
$prune$;

ALTER TABLE fabric_activity_policy_versions OWNER TO wormhole_activity_owner;
ALTER TABLE fabric_activity_policy_current OWNER TO wormhole_activity_owner;
ALTER TABLE fabric_activity_stream_sequences OWNER TO wormhole_activity_owner;
ALTER TABLE fabric_activities OWNER TO wormhole_activity_owner;
ALTER TABLE fabric_activity_ingress_receipts OWNER TO wormhole_activity_owner;
ALTER TABLE fabric_activity_lifecycle OWNER TO wormhole_activity_owner;
ALTER FUNCTION fabric_publish_activity_policy_v1 OWNER TO wormhole_activity_owner;
ALTER FUNCTION fabric_accept_activity_v1 OWNER TO wormhole_activity_owner;
ALTER FUNCTION fabric_transition_activity_lifecycle_v1 OWNER TO wormhole_activity_owner;
ALTER FUNCTION fabric_prune_activities_v1 OWNER TO wormhole_activity_owner;

-- The NOLOGIN Activity owner must take row locks on the portable stream and
-- attachment parents without owning them. Column-limited UPDATE is the minimum
-- PostgreSQL privilege that permits SELECT ... FOR UPDATE; runtime receives no
-- corresponding portable-table privilege.
GRANT SELECT ON TABLE fabric_streams, fabric_workspace_stream_bindings TO wormhole_activity_owner;
GRANT UPDATE (updated_at) ON TABLE fabric_streams TO wormhole_activity_owner;
GRANT UPDATE (detached_at) ON TABLE fabric_workspace_stream_bindings TO wormhole_activity_owner;

REVOKE ALL ON TABLE fabric_activity_policy_versions, fabric_activity_policy_current,
    fabric_activity_stream_sequences, fabric_activities, fabric_activity_ingress_receipts,
    fabric_activity_lifecycle FROM PUBLIC;
REVOKE ALL ON TABLE fabric_activity_policy_versions, fabric_activity_policy_current,
    fabric_activity_stream_sequences, fabric_activities, fabric_activity_ingress_receipts,
    fabric_activity_lifecycle FROM wormhole_fabric_runtime, wormhole_activity_maintenance;
GRANT SELECT ON TABLE fabric_activity_policy_versions, fabric_activity_policy_current,
    fabric_activity_stream_sequences, fabric_activities, fabric_activity_ingress_receipts,
    fabric_activity_lifecycle TO wormhole_fabric_runtime;

REVOKE ALL ON FUNCTION fabric_publish_activity_policy_v1 FROM PUBLIC;
REVOKE ALL ON FUNCTION fabric_accept_activity_v1 FROM PUBLIC;
REVOKE ALL ON FUNCTION fabric_transition_activity_lifecycle_v1 FROM PUBLIC;
REVOKE ALL ON FUNCTION fabric_prune_activities_v1 FROM PUBLIC;
REVOKE ALL ON FUNCTION fabric_publish_activity_policy_v1 FROM wormhole_fabric_runtime, wormhole_activity_maintenance;
REVOKE ALL ON FUNCTION fabric_accept_activity_v1 FROM wormhole_fabric_runtime, wormhole_activity_maintenance;
REVOKE ALL ON FUNCTION fabric_transition_activity_lifecycle_v1 FROM wormhole_fabric_runtime, wormhole_activity_maintenance;
REVOKE ALL ON FUNCTION fabric_prune_activities_v1 FROM wormhole_fabric_runtime, wormhole_activity_maintenance;
GRANT EXECUTE ON FUNCTION fabric_publish_activity_policy_v1 TO wormhole_fabric_runtime;
GRANT EXECUTE ON FUNCTION fabric_accept_activity_v1 TO wormhole_fabric_runtime;
GRANT EXECUTE ON FUNCTION fabric_transition_activity_lifecycle_v1 TO wormhole_fabric_runtime;
GRANT EXECUTE ON FUNCTION fabric_prune_activities_v1 TO wormhole_activity_maintenance;
