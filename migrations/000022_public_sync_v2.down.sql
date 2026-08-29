DO $down_gate$
BEGIN
    IF EXISTS(SELECT 1 FROM fabric_public_actor_keys)
       OR EXISTS(SELECT 1 FROM fabric_public_agent_sessions)
       OR EXISTS(SELECT 1 FROM fabric_workspace_stream_bindings)
       OR EXISTS(SELECT 1 FROM fabric_stream_conflicts WHERE resolution_operation_id IS NOT NULL OR resolution_version IS NOT NULL)
       OR EXISTS(SELECT 1 FROM audit_log WHERE agent_id IS NULL OR actor_kind<>'agent' OR human_principal_id IS NOT NULL OR accountable_human_id IS NOT NULL OR session_id IS NOT NULL OR harness_name<>'' OR harness_version<>'' OR model_name<>'' OR model_version<>'' OR assurance<>'unknown' OR actor_envelope_json IS NOT NULL OR request_digest IS NOT NULL OR canonical_payload_json<>'\x7b7d'::bytea OR occurred_at<>created_at) THEN
        RAISE EXCEPTION USING ERRCODE='55000', MESSAGE='migration 000022 down refuses non-representable public sync evidence';
    END IF;
END
$down_gate$;

REVOKE EXECUTE ON FUNCTION fabric_resolve_attachment_project_v1(uuid,uuid) FROM wormhole_fabric_runtime;
REVOKE SELECT ON TABLE fabric_workspace_stream_bindings FROM wormhole_attachment_resolver;
DROP FUNCTION fabric_resolve_attachment_project_v1(uuid,uuid);

REVOKE USAGE ON SEQUENCE audit_log_seq_seq FROM wormhole_fabric_runtime;
REVOKE SELECT,INSERT ON TABLE audit_log FROM wormhole_fabric_runtime;
REVOKE SELECT,INSERT,UPDATE ON TABLE fabric_public_agent_sessions FROM wormhole_fabric_runtime;
REVOKE INSERT ON TABLE public_request_nonces FROM wormhole_fabric_runtime;
REVOKE SELECT,INSERT ON TABLE fabric_public_actor_keys FROM wormhole_fabric_runtime;
REVOKE SELECT,INSERT,UPDATE ON TABLE fabric_stream_conflicts FROM wormhole_fabric_runtime;
REVOKE SELECT,INSERT ON TABLE fabric_stream_requests FROM wormhole_fabric_runtime;
REVOKE SELECT,INSERT,UPDATE ON TABLE fabric_workspace_stream_bindings FROM wormhole_fabric_runtime;
REVOKE SELECT,INSERT ON TABLE fabric_stream_versions FROM wormhole_fabric_runtime;
REVOKE SELECT,INSERT,UPDATE ON TABLE fabric_streams FROM wormhole_fabric_runtime;
REVOKE SELECT ON TABLE project_repository_bindings FROM wormhole_fabric_runtime;

DROP TRIGGER audit_log_immutable ON audit_log;
DROP FUNCTION reject_audit_log_mutation_v1();
DROP TABLE fabric_public_agent_sessions;

ALTER TABLE fabric_stream_conflicts
    DROP CONSTRAINT fabric_stream_conflicts_resolution_request_fkey,
    DROP CONSTRAINT fabric_stream_conflicts_resolution_operation_fkey,
    DROP CONSTRAINT fabric_stream_conflicts_resolution_pair_check,
    DROP CONSTRAINT fabric_stream_conflicts_resolution_version_check,
    DROP COLUMN resolution_operation_id,
    DROP COLUMN resolution_version;
ALTER TABLE fabric_stream_versions DROP CONSTRAINT fabric_stream_versions_resolution_identity_key;

DROP INDEX fabric_workspace_binding_one_live_public_issuer_idx;
ALTER TABLE fabric_workspace_stream_bindings
    DROP CONSTRAINT fabric_workspace_binding_public_issuer_fkey,
    DROP CONSTRAINT fabric_workspace_binding_source_version_fkey,
    DROP CONSTRAINT fabric_workspace_binding_public_issuer_pair_check,
    DROP CONSTRAINT fabric_workspace_binding_public_issuer_format_check,
    DROP CONSTRAINT fabric_workspace_binding_source_version_check;
DROP INDEX public_request_nonces_expiry_idx;
DROP INDEX fabric_public_actor_keys_active_human_idx;
ALTER TABLE fabric_public_actor_keys
    DROP CONSTRAINT fabric_public_actor_keys_source_identity_key,
    DROP CONSTRAINT fabric_public_actor_keys_human_identity_key,
    DROP CONSTRAINT fabric_public_actor_keys_revocation_check,
    DROP CONSTRAINT fabric_public_actor_keys_human_shape_check,
    ALTER COLUMN session_id SET NOT NULL,
    ADD CONSTRAINT fabric_public_actor_keys_actor_kind_check CHECK(actor_kind IN ('human','agent')),
    ADD CONSTRAINT fabric_public_actor_keys_check CHECK((actor_kind='human' AND human_principal_id IS NOT NULL AND agent_id IS NULL AND accountable_human_id IS NULL) OR (actor_kind='agent' AND human_principal_id IS NULL AND agent_id IS NOT NULL AND accountable_human_id IS NOT NULL));

ALTER TABLE fabric_workspace_stream_bindings
    DROP CONSTRAINT fabric_workspace_binding_route_attachment_key,
    DROP CONSTRAINT fabric_workspace_binding_activity_source_fabric_key,
    DROP CONSTRAINT fabric_workspace_binding_attachment_fabric_key,
    DROP COLUMN public_issuer_key_fingerprint,
    DROP COLUMN source_version,
    DROP COLUMN activity_source_ref;

ALTER TABLE audit_log
    DROP CONSTRAINT audit_log_transport_shape_check,
    DROP CONSTRAINT audit_log_request_digest_matches_payload_check,
    DROP CONSTRAINT audit_log_request_digest_check,
    DROP CONSTRAINT audit_log_metadata_bounds_check,
    DROP CONSTRAINT audit_log_model_pair_check,
    DROP CONSTRAINT audit_log_assurance_check,
    DROP CONSTRAINT audit_log_actor_kind_check,
    DROP COLUMN request_digest,
    DROP COLUMN canonical_payload_json,
    DROP COLUMN actor_envelope_json,
    DROP COLUMN occurred_at,
    DROP COLUMN assurance,
    DROP COLUMN model_version,
    DROP COLUMN model_name,
    DROP COLUMN harness_version,
    DROP COLUMN harness_name,
    DROP COLUMN session_id,
    DROP COLUMN accountable_human_id,
    DROP COLUMN human_principal_id,
    DROP COLUMN actor_kind,
    ALTER COLUMN agent_id SET NOT NULL;
